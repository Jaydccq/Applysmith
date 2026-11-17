// Package knowledgeapi serves the knowledge routes of the local API: search,
// story coverage, repository sources, story mining, documents, drafts, company
// briefs and story proposals. internal/api registers the handlers.
package knowledgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/interview"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

// Deps is what the knowledge handlers use from the server. The store and the
// log exist only once the server has started, so they are read through
// functions.
type Deps struct {
	Knowledge  func() (*kb.Service, error)
	Interviews func() (*interview.Service, error)
	Router     func() (*llm.Router, error)
	StartPass  func(ctx context.Context, p apikit.Pass) (apikit.Reply, error)
	Busy       func(claim string) bool // a pass holding claim is running
	Store      func() *store.Store
	Log        func() *slog.Logger
}

// Handlers serves the knowledge routes.
type Handlers struct {
	Deps
	proposals storyHold // what the last story proposal run found
}

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// KBError maps a knowledge-base failure onto the API errors.
func KBError(err error) error {
	switch {
	case errors.Is(err, kb.ErrInvalid), errors.Is(err, kb.ErrSecret):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
	case errors.Is(err, kb.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "%s", err.Error())
	case errors.Is(err, kb.ErrStale):
		return apikit.NewErr(wire.CodeRevisionConflict, "%s", err.Error())
	case errors.Is(err, kb.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, kb.ErrUnavailable):
		return apikit.Unavailable(err.Error())
	}
	return err
}

// toKBPrinciples joins each grade to the question patterns of its competency,
// worded as prompts ("Tell me about a time you failed.").
func toKBPrinciples(in []kb.PrincipleGrade, comps []kb.Competency) []wire.KBPrincipleGrade {
	questions := map[string][]string{}
	for _, c := range comps {
		for _, q := range c.QuestionPatterns {
			q = strings.TrimSpace(q)
			if q == "" {
				continue
			}
			if !strings.ContainsAny(q[len(q)-1:], ".?!") {
				q += "."
			}
			r, n := utf8.DecodeRuneInString(q)
			questions[c.ID] = append(questions[c.ID], string(unicode.ToUpper(r))+q[n:])
		}
	}
	out := make([]wire.KBPrincipleGrade, 0, len(in))
	for _, p := range in {
		out = append(out, wire.KBPrincipleGrade{ID: p.ID, Name: p.Name, Grade: p.Grade, Stories: apikit.NonNil(p.Stories), Missing: apikit.NonNil(p.Missing), Suggest: apikit.NonNil(p.Suggest), Questions: apikit.NonNil(questions[p.ID])})
	}
	return out
}

func toKBShapes(in []kb.ShapeGrade) []wire.KBShapeGrade {
	out := make([]wire.KBShapeGrade, 0, len(in))
	for _, p := range in {
		out = append(out, wire.KBShapeGrade{ID: p.ID, Name: p.Name, Need: p.Need, Have: p.Have, Grade: p.Grade, Stories: apikit.NonNil(p.Stories)})
	}
	return out
}

func (s *Handlers) Coverage(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	cov, err := svc.CoverageIndexed(c.R.Context())
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	comps, err := svc.Competencies()
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: wire.KBCoverage{Principles: toKBPrinciples(cov.Principles, comps), Shapes: toKBShapes(cov.Shapes)}}, nil
}

// Refresh reconciles the knowledge index with the files. The story
// views are served from the index as it stands, so this is what makes an
// edited file visible to them; a failure leaves the previous index in place.
func (s *Handlers) Refresh(ctx context.Context) {
	svc, err := s.Knowledge()
	if err != nil {
		return
	}
	if _, err := svc.Sync(ctx); err != nil && ctx.Err() == nil && s.Log() != nil {
		s.Log().Warn("knowledge index not refreshed", "error", err.Error())
	}
}

func toKBRepo(r kb.Repo) wire.KBRepo {
	return wire.KBRepo{ID: apikit.Safe64(r.ID), Owner: r.Owner, Name: r.Name, Branch: r.Branch, CommitSHA: r.CommitSHA, Status: r.Status,
		Report: toKBReport(r.Report), CreatedAt: r.CreatedAt, IngestedAt: r.IngestedAt}
}

func toKBReport(r kb.IngestReport) wire.KBIngestReport {
	out := wire.KBIngestReport{Status: r.Status, Commit: r.Commit, Branch: r.Branch, Listing: r.Listing, Considered: r.Considered,
		Read: r.Read, Reused: r.Reused, Fetched: r.Fetched, Skipped: make([]wire.KBFileNote, 0, len(r.Skipped)),
		Redacted: make([]wire.KBFileNote, 0, len(r.Redacted)), Drafts: make([]wire.SafeInt, 0, len(r.Drafts)), Notes: apikit.NonNil(r.Notes)}
	for _, s := range r.Skipped {
		out.Skipped = append(out.Skipped, wire.KBFileNote{Path: s.Path, Reason: s.Reason})
	}
	for _, s := range r.Redacted {
		out.Redacted = append(out.Redacted, wire.KBFileNote{Path: s.Path, Reason: s.Reason})
	}
	for _, d := range r.Drafts {
		out.Drafts = append(out.Drafts, apikit.Safe64(d))
	}
	return out
}

func (s *Handlers) kbRepoList(c *apikit.Call, svc *kb.Service) (wire.KBRepoList, error) {
	repos, err := svc.ListRepos(c.R.Context())
	if err != nil {
		return wire.KBRepoList{}, err
	}
	// A read that is still working is named on its repository, so a page
	// opened after the read began follows it instead of offering another.
	reading, err := s.workingRuns(c.R.Context(), IngestKind, "$.id")
	if err != nil {
		return wire.KBRepoList{}, err
	}
	out := wire.KBRepoList{Repos: make([]wire.KBRepo, 0, len(repos))}
	for _, r := range repos {
		w := toKBRepo(r)
		w.ReadingRunID = reading[strconv.FormatInt(r.ID, 10)]
		out.Repos = append(out.Repos, w)
	}
	return out, nil
}

// workingRuns maps the input field at path of each run of kind that has not
// ended to the run's id.
func (s *Handlers) workingRuns(ctx context.Context, kind, path string) (map[string]string, error) {
	rows, err := s.Store().Read().QueryContext(ctx, `SELECT id, COALESCE(json_extract(input_json, ?), '') FROM runs WHERE kind = ? AND status IN ('queued', 'running')`, path, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var run, key string
		if err := rows.Scan(&run, &key); err != nil {
			return nil, err
		}
		out[key] = run
	}
	return out, rows.Err()
}

func (s *Handlers) ListSources(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	out, err := s.kbRepoList(c, svc)
	return apikit.Reply{Data: out}, err
}

func (s *Handlers) AddSource(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBRepoAdd
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	r, err := svc.AddRepo(c.R.Context(), req.Repo)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Data: toKBRepo(r)}, nil
}

// Ingest starts reading one repository as a background run, under the run
// contract: 202 with the run id, the work on the server's own context, so
// leaving the page does not stop it.
func (s *Handlers) Ingest(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.KBIngestRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	repo, err := svc.GetRepo(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	name := repo.Owner + "/" + repo.Name
	input, _ := json.Marshal(map[string]any{"id": id, "repo": name, "force": req.Force})
	return s.StartPass(c.R.Context(), apikit.Pass{Operation: "kb.ingest", Kind: IngestKind, Stage: "ingest", Claim: kbIngestClaim(id),
		Busy: name + " is already being read", Key: key, Principal: c.P.ID, Input: input,
		Run: func(ctx context.Context, progress func(string)) (string, error) {
			progress("Reading " + name + " from GitHub")
			rep, err := svc.IngestRepo(ctx, id, req.Force)
			if err != nil {
				return "", err
			}
			return rep.Status, nil
		}})
}

// kbIngestClaim is the claim a read of one repository holds while it runs.
func kbIngestClaim(id int64) string { return fmt.Sprintf("kb-ingest-%d", id) }

// IngestKind is the run kind of a repository read; the board shows it as a task.
const IngestKind = "kb.ingest"

// IngestSentence words what a read of a repository came to.
func IngestSentence(r kb.Repo, rep kb.IngestReport) string {
	name := r.Owner + "/" + r.Name
	if rep.Status == kb.IngestUpToDate {
		return fmt.Sprintf("%s is up to date at %.7s", name, r.CommitSHA)
	}
	return fmt.Sprintf("Read %s at %.7s: %d %s", name, r.CommitSHA, len(rep.Drafts), map[bool]string{true: "draft", false: "drafts"}[len(rep.Drafts) == 1])
}

func (s *Handlers) RemoveSource(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	// A read in progress would go on writing drafts for a repository that is gone.
	if s.Busy(kbIngestClaim(id)) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "This repository is being read right now. Cancel the run or wait for it to finish, then remove it.")
	}
	token, err := svc.RemoveRepo(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: wire.KBUndo{Undo: token}}, nil
}

func toKBSession(m kb.MiningSession) wire.KBMiningSession {
	out := wire.KBMiningSession{ID: m.ID, Quadrant: m.Quadrant, SourceType: m.SourceType, State: m.State, NotesFrom: m.NotesFrom,
		Fields: map[string]wire.KBMiningValue{}, Log: make([]wire.KBMiningEntry, 0, len(m.Log)), DraftID: apikit.Safe64(m.DraftID),
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Ignored: m.Ignored}
	for k, v := range m.Fields {
		out.Fields[k] = wire.KBMiningValue{Text: v.Text, Source: v.Source}
	}
	for _, e := range m.Log {
		out.Log = append(out.Log, wire.KBMiningEntry{Role: e.Role, Field: e.Field, Text: e.Text, At: e.At})
	}
	if m.Next != nil {
		out.Next = &wire.KBMiningQuestion{Field: m.Next.Field, Text: m.Next.Text}
	}
	return out
}

func (s *Handlers) kbMiningList(c *apikit.Call, svc *kb.Service) (wire.KBMiningList, error) {
	list, err := svc.ListMining(c.R.Context())
	if err != nil {
		return wire.KBMiningList{}, err
	}
	out := wire.KBMiningList{Sessions: make([]wire.KBMiningSession, 0, len(list))}
	for _, m := range list {
		out.Sessions = append(out.Sessions, toKBSession(m))
	}
	return out, nil
}

func (s *Handlers) ListMining(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	out, err := s.kbMiningList(c, svc)
	return apikit.Reply{Data: out}, err
}

func (s *Handlers) StartMining(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBMiningStart
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	m, err := svc.StartMining(c.R.Context(), kb.MiningStart{Quadrant: req.Quadrant, Notes: req.Notes, NotesFile: req.NotesFile, Target: req.Target})
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Location: "/api/kb/stories/mining/" + m.ID, Data: toKBSession(m)}, nil
}

func (s *Handlers) SuggestQuadrants(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	sug, err := svc.SuggestQuadrantsIndexed(c.R.Context())
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	out := wire.KBQuadrantSuggestions{Quadrants: make([]wire.KBQuadrantSuggestion, 0, len(sug))}
	for _, q := range sug {
		out.Quadrants = append(out.Quadrants, wire.KBQuadrantSuggestion{Quadrant: q.Quadrant, Score: q.Score, Reasons: apikit.NonNil(q.Reasons)})
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) GetMining(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	m, err := svc.GetMining(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: toKBSession(m)}, nil
}

func (s *Handlers) MiningReply(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBMiningReply
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	var m kb.MiningSession
	switch {
	case req.ExpectedRevision <= 0:
		return apikit.Reply{}, apikit.InvalidField("expectedRevision", "a reply quotes the length of the transcript it was made against")
	case req.Field == "":
		m, err = svc.MiningReply(c.R.Context(), c.R.PathValue("id"), req.Text, req.ExpectedRevision)
	default:
		m, err = svc.ReviseMining(c.R.Context(), c.R.PathValue("id"), req.Field, req.Text, req.ExpectedRevision)
	}
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: toKBSession(m)}, nil
}

func (s *Handlers) SaveMining(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	d, err := svc.SaveMining(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: toKBDraft(d)}, nil
}

func (s *Handlers) DeleteMining(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	token, err := svc.DeleteMining(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: wire.KBUndo{Undo: token}}, nil
}
