package knowledgeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
)

func toKBEntry(e kb.Entry) wire.KBDocEntry {
	out := wire.KBDocEntry{ID: e.ID, Kind: e.Kind, Path: e.Path, Category: e.Category, Title: e.Title, Description: e.Description,
		ModifiedAt: e.ModifiedAt, FileHash: e.FileHash, Tags: apikit.NonNil(e.Tags), Principles: apikit.NonNil(e.Principles), SourceType: e.SourceType,
		SourceURL: e.SourceURL, Lint: make([]wire.KBLint, 0, len(e.Lint))}
	for _, f := range e.Lint {
		out.Lint = append(out.Lint, wire.KBLint{Code: f.Code, Message: f.Message})
	}
	return out
}

func WireKBDocument(d kb.Document) wire.KBDocument {
	out := wire.KBDocument{Entry: toKBEntry(d.Entry), Body: d.Body, HasPrevious: d.HasPrevious, Sections: make([]wire.KBSection, 0, len(d.Sections))}
	for _, s := range d.Sections {
		out.Sections = append(out.Sections, wire.KBSection{Heading: s.Heading, Level: s.Level, Text: s.Text})
	}
	out.Links, out.LinkedFrom = make([]wire.KBPageLink, 0, len(d.Links)), make([]wire.KBPageRef, 0, len(d.LinkedFrom))
	for _, l := range d.Links {
		out.Links = append(out.Links, wire.KBPageLink{Label: l.Label, Path: l.Path, ID: l.ID})
	}
	for _, r := range d.LinkedFrom {
		out.LinkedFrom = append(out.LinkedFrom, wire.KBPageRef{ID: r.ID, Title: r.Title})
	}
	return out
}

func (s *Handlers) ListDocuments(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	// q narrows the list to the documents that hold every word of it (kb.Find).
	var list []kb.Entry
	if q := c.R.URL.Query().Get("q"); q != "" {
		list, err = svc.Find(c.R.Context(), q)
	} else {
		list, err = svc.List(c.R.Context())
	}
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	out := wire.KBDocumentList{Entries: make([]wire.KBDocEntry, 0, len(list)), Uses: []wire.KBStoryUse{}}
	for _, e := range list {
		out.Entries = append(out.Entries, toKBEntry(e))
	}
	iv, err := s.Interviews()
	if err != nil {
		return apikit.Reply{}, err
	}
	uses, err := iv.StoryUses(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	for _, e := range list {
		for _, u := range uses[e.ID] {
			out.Uses = append(out.Uses, wire.KBStoryUse{StoryID: e.ID, CandidacyID: u.Candidacy, Company: u.Company, Role: u.Role})
		}
	}
	// One brief is built at a time; a page opened during the build follows it.
	building, err := s.workingRuns(c.R.Context(), BriefKind, "$.company")
	if err != nil {
		return apikit.Reply{}, err
	}
	for _, run := range building {
		out.BriefRunID = run
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) GetDocument(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	doc, err := svc.Show(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: WireKBDocument(doc)}, nil
}

func (s *Handlers) CreateSnippet(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBSnippetCreate
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	pub, err := svc.CreateSnippet(c.R.Context(), kb.SnippetInput{Title: req.Title, Category: req.Category, Tags: req.Tags, Body: req.Body})
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	doc, err := svc.Show(c.R.Context(), pub.SnippetID)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Data: WireKBDocument(doc)}, nil
}

func (s *Handlers) ReviseDocument(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBDocumentRevise
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	doc, err := svc.Revise(c.R.Context(), c.R.PathValue("id"), kb.Revision{Title: req.Title, Tags: req.Tags, Body: req.Body}, req.ExpectedHash)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: WireKBDocument(doc)}, nil
}

// PreviousDocument returns the text the last save of a document replaced.
func (s *Handlers) PreviousDocument(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	p, err := svc.PreviousVersion(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	at, _ := time.Parse(time.RFC3339, p.SavedAt)
	return apikit.Reply{Data: wire.KBPrevious{SavedAt: wire.Time{Time: at}, Title: p.Title, Tags: apikit.NonNil(p.Tags), Body: p.Body}}, nil
}

func (s *Handlers) DeleteDocument(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	token, err := svc.Delete(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: wire.KBUndo{Undo: token}}, nil
}

// Restore undoes a knowledge delete by the token the delete answered.
func (s *Handlers) Restore(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBUndo
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	r, err := svc.Restore(c.R.Context(), req.Undo)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: wire.KBRestored{Kind: r.Kind, Name: r.Name}}, nil
}

// Brief starts building or refreshing a company brief from the site its
// posting pages declare, as a background run: the site is read page by page and
// a model condenses it, which outlasts a request. A posting that declares no
// link is a validation failure the page can act on; a site that declares none,
// or has no usable page, ends the run as failed with that reason.
func (s *Handlers) Brief(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBBriefRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if strings.TrimSpace(req.ApplicationURL) == "" && strings.TrimSpace(req.JobURL) == "" {
		return apikit.Reply{}, apikit.InvalidField("jobUrl", "a posting link is required: the company's own site is read from the posting, never guessed from its name")
	}
	input, _ := json.Marshal(map[string]any{"company": req.Company})
	return s.StartPass(c.R.Context(), apikit.Pass{Operation: "kb.brief", Kind: BriefKind, Stage: "brief", Claim: "kb-brief",
		Busy: "a company brief is already being built", Key: key, Principal: c.P.ID, Input: input,
		Run: func(ctx context.Context, progress func(string)) (string, error) {
			progress("Reading the company's site")
			b, err := svc.EnsureBrief(ctx, kb.BriefRequest{Company: req.Company, ApplicationURL: req.ApplicationURL, JobURL: req.JobURL, Refresh: req.Refresh})
			switch {
			case errors.Is(err, kb.ErrNoSite):
				return "", errors.New("the posting declares no company website")
			case errors.Is(err, kb.ErrNoBrief):
				return "", errors.New("the company's site has no usable page")
			case err != nil:
				return "", KBError(err)
			}
			switch {
			case b.Manual:
				return "Kept the brief you edited; a refresh never replaces it.", nil
			case b.Task != "":
				return "Brief built with the " + b.Task + " model task.", nil
			}
			return "Brief built from the company's own site text; no model condensed it.", nil
		}})
}

// The run kinds of the Knowledge page's generations; the board shows each as a
// task.
const (
	BriefKind   = "kb.brief"
	ProposeKind = "kb.propose"
)

func (s *Handlers) CreateStory(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.KBStoryCreate
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	saved, err := svc.AddStory(c.R.Context(), kb.StoryInput{Title: req.Title, SourceType: req.SourceType, Principles: req.Principles,
		Situation: req.Situation, Task: req.Task, Action: req.Action, Result: req.Result})
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	out := wire.KBStorySaved{DraftID: apikit.Safe64(saved.DraftID), Published: saved.Published, Path: saved.Path, Findings: make([]wire.KBStoryFinding, 0, len(saved.Findings))}
	for _, f := range saved.Findings {
		out.Findings = append(out.Findings, wire.KBStoryFinding{Code: f.Code, Severity: f.Severity, Message: f.Message})
	}
	if saved.Published {
		s.Refresh(c.R.Context())
	}
	s.proposals.set(req.Title, true)
	return apikit.Reply{Status: http.StatusCreated, Data: out}, nil
}

// storyHold keeps what the last story proposal run found until the next one,
// so the page can read it however long the user was away. Nothing reaches the
// workspace until a proposal is approved. One dismissed or saved stays gone,
// by title, from this list and later runs while the server runs.
type storyHold struct {
	mu   sync.Mutex
	list []wire.KBStoryProposal
	gone map[string]bool
}

// shown is the list without what is gone.
func (h *storyHold) shown() wire.KBStoryProposals {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := slices.DeleteFunc(append([]wire.KBStoryProposal{}, h.list...), func(p wire.KBStoryProposal) bool { return h.gone[kb.Slug(p.Title)] })
	return wire.KBStoryProposals{Proposals: out}
}

// set marks a title gone or back, and says whether the list holds it.
func (h *storyHold) set(title string, gone bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gone == nil {
		h.gone = map[string]bool{}
	}
	h.gone[kb.Slug(title)] = gone
	return slices.ContainsFunc(h.list, func(p wire.KBStoryProposal) bool { return kb.Slug(p.Title) == kb.Slug(title) })
}

// ProposeStories starts reading the CV for stories as a background run: one
// model call with a budget of minutes. A task with no model is refused at once.
func (s *Handlers) ProposeStories(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	if _, err := router.Resolve(c.R.Context(), "story-extraction"); errors.Is(err, llm.ErrNoRoute) {
		return apikit.Reply{}, apikit.WithReason(apikit.Unavailable(llm.NoModel("the story-extraction task")), wire.ReasonNoModelRoute)
	}
	return s.StartPass(c.R.Context(), apikit.Pass{Operation: "kb.propose", Kind: ProposeKind, Stage: "propose", Claim: "kb-propose",
		Busy: "stories are already being proposed", Key: key, Principal: c.P.ID, Input: []byte("{}"),
		Run: func(ctx context.Context, progress func(string)) (string, error) {
			progress("Reading your CV for stories")
			got, err := svc.ProposeStories(ctx)
			if err != nil {
				return "", KBError(err)
			}
			list := make([]wire.KBStoryProposal, 0, len(got))
			for _, p := range got {
				list = append(list, wire.KBStoryProposal{Title: p.Title, Principles: apikit.NonNil(p.Principles), Situation: p.Situation, Task: p.Task, Action: p.Action, Result: p.Result})
			}
			s.proposals.mu.Lock()
			s.proposals.list = list
			s.proposals.mu.Unlock()
			return fmt.Sprintf("%d proposed", len(list)), nil
		}})
}

// StoryProposals answers what the last proposal run found.
func (s *Handlers) StoryProposals(c *apikit.Call) (apikit.Reply, error) {
	return apikit.Reply{Data: s.proposals.shown()}, nil
}

// StoryProposalSet dismisses or restores a proposal by title and answers
// the list left.
func (s *Handlers) StoryProposalSet(gone bool) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		var req wire.KBStoryProposalRef
		if e := apikit.Decode(c.Body, &req); e != nil {
			return apikit.Reply{}, e
		}
		if !s.proposals.set(req.Title, gone) {
			return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no proposal has that title")
		}
		return apikit.Reply{Data: s.proposals.shown()}, nil
	}
}

// ImportFile uses the bounded raw-upload body, with the file name/category
// in the query. Importing creates a draft, never a searchable document.
func (s *Handlers) ImportFile(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	data, err := io.ReadAll(io.LimitReader(c.R.Body, (5<<20)+1))
	if err != nil {
		return apikit.Reply{}, err
	}
	if len(data) > 5<<20 {
		return apikit.Reply{}, apikit.NewErr(wire.CodePayloadTooLarge, "knowledge imports are limited to 5 MiB")
	}
	d, err := svc.ImportFile(c.R.Context(), kb.ImportInput{Name: apikit.QueryOf(c).Str("name"), Category: apikit.QueryOf(c).Str("category"), Data: data})
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Data: toKBDraft(d)}, nil
}

func toKBDraft(d kb.Draft) wire.KBDraft {
	out := wire.KBDraft{ID: apikit.Safe64(d.ID), Revision: d.Revision, Status: d.Status, Kind: d.Kind, Origin: d.Origin, Category: d.Category,
		SnippetID: d.SnippetID, Markdown: d.Markdown, Body: kb.BodyOf(d.Markdown), Facts: make([]wire.KBDraftFact, 0, len(d.Facts))}
	for _, f := range d.Facts {
		out.Facts = append(out.Facts, wire.KBDraftFact{Text: f.Text, Origin: f.Origin, Pending: f.Pending})
	}
	if d.Kind == kb.DraftStory {
		if sn, err := kb.ParseSnippet(d.SnippetID+".md", []byte(d.Markdown)); err == nil {
			out.Principles = sn.Principles
		}
	}
	return out
}

// Drafts lists every unpublished draft, newest first.
func (s *Handlers) Drafts(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := svc.ListDrafts(c.R.Context(), false)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	out := wire.KBDraftList{Drafts: []wire.KBDraft{}}
	for _, d := range list {
		if d.Status != kb.StatusPublished {
			out.Drafts = append(out.Drafts, toKBDraft(d))
		}
	}
	return apikit.Reply{Data: out}, nil
}

// DraftChange edits (PATCH), confirms statements of (POST) or rejects
// (DELETE) a draft.
func (s *Handlers) DraftChange(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	if c.R.Method == http.MethodDelete {
		token, err := svc.DeleteDraft(c.R.Context(), id)
		if err != nil {
			return apikit.Reply{}, KBError(err)
		}
		return apikit.Reply{Data: wire.KBUndo{Undo: token}}, nil
	}
	var req wire.KBDraftChange
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	var d kb.Draft
	switch {
	case c.R.Method == http.MethodPatch && req.Principles != nil:
		d, err = svc.SetDraftPrinciples(c.R.Context(), id, req.Principles)
	case c.R.Method == http.MethodPatch:
		d, err = svc.EditDraft(c.R.Context(), id, req.Markdown)
	default:
		d, err = svc.ConfirmDraftFacts(c.R.Context(), id, req.Facts)
	}
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: toKBDraft(d)}, nil
}

// PublishDraft publishes a draft, a story through the story gates.
func (s *Handlers) PublishDraft(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Knowledge()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	d, err := svc.GetDraft(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	publish := svc.PublishDraft
	if d.Kind == kb.DraftStory {
		publish = svc.PublishStory
	}
	pub, err := publish(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	doc, err := svc.Show(c.R.Context(), pub.SnippetID)
	if err != nil {
		return apikit.Reply{}, KBError(err)
	}
	return apikit.Reply{Data: WireKBDocument(doc)}, nil
}
