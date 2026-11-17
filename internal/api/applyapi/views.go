// Package applyapi serves the views of the application runs in the local API:
// a run's record and the questions it left, a list's record, the Apply Next
// lanes and the answers that move waiting runs on. internal/api registers the
// handlers and keeps the apply chain that runs them.
package applyapi

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"applysmith/internal/agent"
	"applysmith/internal/agent/origins"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/documents"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/ledger"
	"applysmith/internal/notify"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// Deps is what the apply views use from the server: its services, the apply
// chain's state they read and the run they continue or start through it, the
// Ask Agent a takeover hands a run to, and api's error mappings. The log
// exists only once the server has started, so it is read through a function.
type Deps struct {
	DB       func() (*store.Store, error)
	Tracker  func() (*tracker.Tracker, error)
	Docs     func() (*documents.Service, error) // whether a form-asked letter waits for the floor
	Ledger   func() (*ledger.Ledger, error)
	Learning func() (*learn.Service, error)
	Bind     func() (*bind.Binding, error)
	Board    func() (*notify.Board, error)
	// Evidence is the duplicate and stop evidence the list selection reads.
	Evidence    func() apply.SQLEvidence
	SkipEntries func() ([]string, error)
	WireEdits   func(ctx context.Context, svc *learn.Service, runID string) ([]wire.AnswerEdit, error)
	// The apply chain: a start, a waiting run that goes on in its kept tab,
	// and what the chain holds of a run or a posting.
	StartApply    func(ctx context.Context, req agent.ApplyRequest) (string, error)
	GoOn          func(ctx context.Context, id, by, note string, ack bool) (notify.Entry, error)
	KeptTab       func(runID string) bool
	FormWaits     func(ctx context.Context, runID string) bool
	QueuedPosting func(postingURL string) bool
	// The Ask Agent a stopped run is handed to.
	ModelMissing func(ctx context.Context) error
	Origin       func(ctx context.Context, runID string) (origins.Origin, error)
	Session      func(ctx context.Context, id string) (chatsessions.Session, error)
	Send         func(ctx context.Context, req agent.SendRequest) (agent.SendResult, error)
	// The error mappings and tracker forms api's other routes share.
	LearnError      func(error) error
	AnswerBankError func(error) error
	ChatError       func(error) error
	ConfigError     func(error) error
	TrackerError    func(error) error
	Actor           func(apikit.Principal) (tracker.Actor, *apikit.Error)
	WireCandidacy   func(tracker.Entry) wire.Candidacy
	Notify          func(topics ...string)
	Now             func() time.Time
	Log             func() *slog.Logger
	Build           string
	Workspace       *workspace.Workspace
}

// Handlers serves the apply views.
type Handlers struct{ Deps }

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// ErrLaneBusy is a start refused because another run holds the apply window.
var ErrLaneBusy = errors.New("an application run is already working in the apply window; wait for it to finish")

// StartCaller is who the run is recorded for. Only the dashboard session and a
// typed command may start a run, and only they may submit.
func StartCaller(c *apikit.Call) (gates.Caller, *apikit.Error) {
	switch c.P.Kind {
	case apikit.PrincipalDashboard:
		return gates.CallerDashboard, nil
	case apikit.PrincipalLocalToken:
		return gates.CallerCLI, nil
	}
	return "", apikit.NewErr(wire.CodeForbidden, "applications are started in the dashboard or from the command line")
}

// ConfirmSubmit refuses a submit-class request the user did not confirm: the
// dashboard sets the flag only after its confirmation dialog.
func ConfirmSubmit(submit, confirmed bool) *apikit.Error {
	if submit && !confirmed {
		return apikit.InvalidField("confirmed", "a run that submits needs the user's explicit confirmation")
	}
	return nil
}

// RefuseStart maps a refused start to the envelope.
func RefuseStart(err error) error {
	if errors.Is(err, ErrLaneBusy) {
		return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", err.Error()), wire.ReasonLaneBusy)
	}
	return err
}

// Who pressed Done. The Ask Agent presses it when it saves the last answer a
// run waits for (AC-4).
const (
	DoneFromDashboard = "dashboard"
	DoneFromAgent     = "agent"
)

// runRecord is one apply_runs row as the views read it.
type runRecord struct {
	id, batch, caller, posting, identity, ats, company, role string
	started, finished                                        string
	report                                                   sql.NullInt64
	kind, stage, class, reason                               string
	confirmed, submit                                        bool
	stagesJSON, pagesJSON                                    string
}

const runColumns = `run_id, COALESCE(batch_id, ''), caller, posting_url, identity_key, ats, company, role, started_at, COALESCE(finished_at, ''),
	report_number, outcome_kind, outcome_stage, outcome_class, outcome_reason, confirmed, submit, stages_json, pages_json`

func scanRun(sc interface{ Scan(...any) error }) (runRecord, error) {
	var r runRecord
	err := sc.Scan(&r.id, &r.batch, &r.caller, &r.posting, &r.identity, &r.ats, &r.company, &r.role, &r.started, &r.finished, &r.report,
		&r.kind, &r.stage, &r.class, &r.reason, &r.confirmed, &r.submit, &r.stagesJSON, &r.pagesJSON)
	return r, err
}

func (r runRecord) outcome() apply.Outcome {
	return apply.Outcome{Kind: apply.OutcomeKind(r.kind), Stage: apply.Stage(r.stage), Class: apply.Class(r.class), Reason: r.reason, Confirmed: r.confirmed}
}

func (r runRecord) wireOutcome() wire.ApplyOutcome {
	o := r.outcome()
	return wire.ApplyOutcome{Kind: r.kind, Stage: r.stage, Class: r.class, Reason: r.reason, Confirmed: r.confirmed, Owner: apply.OwnerOf(o)}
}

func modeOf(submit bool) string {
	if submit {
		return wire.ApplyModeSubmit
	}
	return wire.ApplyModeDryRun
}

func (s *Handlers) loadRun(ctx context.Context, id string) (runRecord, error) {
	st, err := s.DB()
	if err != nil {
		return runRecord{}, err
	}
	r, err := scanRun(st.Read().QueryRowContext(ctx, `SELECT `+runColumns+` FROM apply_runs WHERE run_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, apikit.NewErr(wire.CodeNotFound, "no application run is called %q", id)
	}
	return r, err
}

func (s *Handlers) GetRun(c *apikit.Call) (apikit.Reply, error) {
	r, err := s.loadRun(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, err
	}
	var lines []apply.StageLine
	_ = json.Unmarshal([]byte(r.stagesJSON), &lines)
	var pages []json.RawMessage
	_ = json.Unmarshal([]byte(r.pagesJSON), &pages)
	view := wire.ApplyRunView{RunID: r.id, BatchID: r.batch, Mode: modeOf(r.submit), Caller: r.caller, Company: r.company, Role: r.role,
		Host: postingHost(r.posting), ATS: r.ats, StartedAt: apikit.Stamp(r.started), FinishedAt: apikit.StampPtr(r.finished), Outcome: r.wireOutcome(),
		Stages: make([]wire.ApplyStage, 0, len(lines)), Pages: apikit.Safe(len(pages))}
	for _, l := range lines {
		view.Stages = append(view.Stages, wire.ApplyStage{Stage: string(l.Stage), Status: string(l.Status), Detail: l.Detail, StartedAt: wire.Time{Time: l.StartedAt}, Ms: apikit.Safe64(l.Ms)})
	}
	if r.report.Valid {
		n := apikit.Safe64(r.report.Int64)
		view.ReportNumber = &n
	}
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	var cid int64
	if err := st.Read().QueryRowContext(c.R.Context(), `SELECT id FROM tracker_entries
		WHERE archived_at IS NULL AND (identity_key = ?1 OR id IN (SELECT entry_id FROM tracker_aliases WHERE identity_key = ?1)) ORDER BY id LIMIT 1`, r.identity).Scan(&cid); err == nil {
		n := apikit.Safe64(cid)
		view.CandidacyID = &n
	}
	view.KeptTab = s.KeptTab(r.id)
	if view.Questions, err = s.wireQuestions(c.R.Context(), r); err != nil {
		return apikit.Reply{}, err
	}
	if svc, err := s.Learning(); err == nil {
		if view.Edits, err = s.WireEdits(c.R.Context(), svc, r.id); err != nil {
			return apikit.Reply{}, err
		}
	}
	// A cover letter left to the user: the override, like the retry that
	// follows it, acts on the report of the posting's tracker row.
	var letter bool
	if view.Uploads, letter = r.slots(); letter && view.CandidacyID != nil {
		var rep sql.NullInt64
		_ = st.Read().QueryRowContext(c.R.Context(), `SELECT report_number FROM tracker_entries WHERE id = ?`, cid).Scan(&rep)
		view.LetterOverride = rep.Valid && s.letterFloor(c.R.Context()) && s.belowFloor(c.R.Context(), rep.Int64)
	}
	if r.kind == string(apply.OutcomeSubmitted) {
		msg, ok, err := ledger.New(st, s.Now).CorrectionOffer(c.R.Context(), s.fullURL(c.R.Context(), r))
		if err != nil {
			return apikit.Reply{}, err
		}
		if ok {
			view.Correction = &wire.ApplyCorrection{Message: msg}
		}
	}
	return apikit.Reply{Data: view}, nil
}

// fullURL is the address a retry opens. The run record keeps the address
// without its query, which a posting may need, so the batch row or the tracker
// row that holds the whole one is preferred.
func (s *Handlers) fullURL(ctx context.Context, r runRecord) string {
	st, err := s.DB()
	if err != nil {
		return r.posting
	}
	var full string
	if r.batch != "" {
		_ = st.Read().QueryRowContext(ctx, `SELECT url FROM apply_batch_rows WHERE run_id = ?`, r.id).Scan(&full)
	}
	if full == "" {
		_ = st.Read().QueryRowContext(ctx, `SELECT posting_url FROM tracker_entries WHERE archived_at IS NULL AND identity_key = ? AND posting_url <> '' ORDER BY id LIMIT 1`, r.identity).Scan(&full)
	}
	if full != "" && strings.HasPrefix(full, r.posting) {
		return full
	}
	return r.posting
}

// retryApplyRun starts the same posting again as a new run. A submitting retry
// needs the confirmation of the dialog that named the company, role and host.
func (s *Handlers) RetryRun(c *apikit.Call) (apikit.Reply, error) {
	caller, e := StartCaller(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.ApplyRetryRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if e := ConfirmSubmit(req.Submit, req.Confirmed); e != nil {
		return apikit.Reply{}, e
	}
	r, err := s.loadRun(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, err
	}
	if r.finished == "" {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "the run is still working"), wire.ReasonAlreadyRunning)
	}
	id, err := s.StartApply(c.R.Context(), agent.ApplyRequest{Tool: gates.ToolAutofillApplication, Submit: req.Submit, ExplicitRequest: true, Caller: caller,
		Rows: []agent.ApplyRow{{URL: s.fullURL(c.R.Context(), r), Company: r.company, Role: r.role}}})
	if err != nil {
		return apikit.Reply{}, RefuseStart(err)
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + id, Data: wire.RunAccepted{RunID: id}}, nil
}

// answerKinds are the controls an entry can answer; a checkbox (a consent, a
// declaration or a group), a file, a date and the contact boxes stay in the form.
var answerKinds = map[string]bool{"text": true, "textarea": true, "number": true, "select": true, "radio-group": true, "custom-listbox": true, "combobox": true}

var signatureRE = lazyregexp.New(`(?i)\b(?:signature|initials)\b`)

// runQuestion is a question with the host its entry is scoped to.
type runQuestion struct {
	wire.ApplyQuestion
	host string
	// posting is the application the run made: "this company" is its company.
	posting match.Posting
	// after is the question of its page its wording starts with: the engine
	// names a follow-up ("If yes, please explain") after the question before
	// it, whose answer can settle it.
	after *wire.ApplyQuestion
}

// routed is what the bank answers the question with now, asked after the
// question it follows as its page asked it.
func (q runQuestion) routed(route learn.Router) learn.Routed {
	if q.after != nil {
		return route.RouteAfter(q.host, q.posting, q.after.Question, q.after.Options, q.Question, q.Options)
	}
	return route.Route(q.host, q.posting, q.Question, q.Options)
}

// questions are a stopped run's needs-user leftovers an entry can answer, once
// per question, each with where the user's answer starts to apply (F-4). A
// secret, a self-identification or contact question, a signature and a
// statement only observation answers are never offered.
func (r runRecord) questions() []runQuestion {
	if r.finished == "" || r.kind == string(apply.OutcomeSubmitted) {
		return nil
	}
	var pages []apply.PageReport
	_ = json.Unmarshal([]byte(r.pagesJSON), &pages)
	var out []runQuestion
	seen := map[string]bool{}
	for _, p := range pages {
		var asked []wire.ApplyQuestion
		for _, l := range p.Leftovers {
			q := strings.TrimRight(match.Sanitize(l.Label, nil), " *")
			if l.Draft == nil || q == "" {
				continue
			}
			var after *wire.ApplyQuestion
			for i := len(asked) - 1; i >= 0 && after == nil; i-- {
				if strings.HasPrefix(q, asked[i].Question+" ") {
					before := asked[i]
					after = &before
				}
			}
			asked = append(asked, wire.ApplyQuestion{Question: q, Options: l.Draft.Options})
			if seen[strings.ToLower(q)] || !answerKinds[l.Draft.Kind] || signatureRE.MatchString(q) ||
				learn.Classify(q, l.Draft.Kind, "", "", l.Draft.Options) != learn.WithheldNone || gates.ClassifyStatement(q) == gates.StatementObservable {
				continue
			}
			seen[strings.ToLower(q)] = true
			out = append(out, runQuestion{wire.ApplyQuestion{Question: q, Kind: l.Draft.Kind, Options: l.Draft.Options, Everywhere: !learn.NamesCompany(q, r.company)},
				cmp.Or(learn.HostOf(p.URL), learn.HostOf(r.posting)), match.Posting{Company: r.company, Title: r.role}, after})
		}
	}
	return out
}

// inTab are the required fields a stopped run left that its questions do not
// ask, once each by label: a value the site refused, a signature, a withheld
// class. Only the user completes them, in the kept tab.
func (r runRecord) inTab() []string {
	if r.finished == "" || r.kind == string(apply.OutcomeSubmitted) {
		return nil
	}
	seen := map[string]bool{}
	for _, q := range r.questions() {
		seen[strings.ToLower(q.Question)] = true
	}
	var pages []apply.PageReport
	_ = json.Unmarshal([]byte(r.pagesJSON), &pages)
	var out []string
	for _, p := range pages {
		for _, l := range p.Leftovers {
			q := strings.TrimRight(match.Sanitize(l.Label, nil), " *")
			if q == "" || seen[strings.ToLower(q)] {
				continue
			}
			seen[strings.ToLower(q)] = true
			out = append(out, q)
		}
	}
	return out
}

// slots are what a stopped run left of the documents its form asked for: the
// required upload slots, by label and once each, and whether one of its
// leftovers was the cover letter's slot or box.
func (r runRecord) slots() (uploads []string, letter bool) {
	if r.finished == "" || r.kind == string(apply.OutcomeSubmitted) {
		return nil, false
	}
	var pages []apply.PageReport
	_ = json.Unmarshal([]byte(r.pagesJSON), &pages)
	seen := map[string]bool{}
	for _, p := range pages {
		for _, l := range p.Leftovers {
			if l.Draft == nil {
				continue
			}
			letter = letter || apply.LetterSlot(l.Draft.Kind, l.Label)
			if f := strings.TrimRight(match.Sanitize(l.Label, nil), " *"); l.Draft.Kind == "file" && f != "" && !seen[strings.ToLower(f)] {
				seen[strings.ToLower(f)] = true
				uploads = append(uploads, f)
			}
		}
	}
	return uploads, letter
}

// wireQuestions are a run's open questions with the entry that answers each now.
func (s *Handlers) wireQuestions(ctx context.Context, r runRecord) ([]wire.ApplyQuestion, error) {
	qs := r.questions()
	if len(qs) == 0 {
		return nil, nil
	}
	route, err := s.Router()
	if err != nil {
		return nil, err
	}
	qs = s.unsettled(ctx, route, qs)
	out := make([]wire.ApplyQuestion, 0, len(qs))
	for _, q := range qs {
		got := q.routed(route).Outright()
		q.AnsweredBy, q.Answer = got.EntryID, got.Answer
		q.Draftable = len(q.Options) == 0 && (q.Kind == "textarea" || q.Kind == "text") && assist.Draftable(q.Question, q.Kind == "textarea")
		out = append(out, q.ApplyQuestion)
	}
	return out, nil
}

// Router reads the answer bank once for the questions of a request.
func (s *Handlers) Router() (learn.Router, error) {
	svc, err := s.Learning()
	if err != nil {
		return learn.Router{}, err
	}
	route, err := svc.Routes()
	if err != nil {
		return learn.Router{}, s.AnswerBankError(err)
	}
	return route, nil
}

// unsettled are the questions of qs the fill does not answer No now. A question
// the matcher routes to no entry, or to a skill gate, is answered No by the
// fill when its own search of the user's information (bind.Binding.OwnSearch,
// over the inputs the fill reads, the knowledge base among them) finds none of
// what it asks about and the question has a No to give (apply.AbsenceNo), so
// the views, whose routing reads no knowledge base, leave it out (F-10). A
// follow-up, and every question of a search that fails, stays. A question the
// matcher now answers outright with no entry is left out too: the profile
// answers it (a relationship list the user confirmed complete).
func (s *Handlers) unsettled(ctx context.Context, route learn.Router, qs []runQuestion) []runQuestion {
	no := map[int]bool{}
	gots := make([]learn.Routed, len(qs))
	for i, q := range qs {
		gots[i] = q.routed(route)
		no[i] = gots[i].Outcome == match.OutcomeFilled && gots[i].EntryID == ""
	}
	b, err := s.Bind()
	if err != nil || b == nil {
		return without(qs, no)
	}
	byHost := map[string][]int{}
	for i, q := range qs {
		got := gots[i]
		if _, no := apply.AbsenceNo(q.Options); !no || q.after != nil || got.Outcome != match.OutcomeAbstained ||
			got.Reason != match.ReasonNoEntry && got.Reason != match.ReasonPlaceholderEmpty || got.EntryID != "" && !route.SkillGate(got.EntryID) {
			continue
		}
		byHost[q.host] = append(byHost[q.host], i)
	}
	for host, at := range byHost {
		req := engine.MatchRequest{Host: host}
		for _, i := range at {
			req.Questions = append(req.Questions, engine.WireQuestion{Selector: "q" + strconv.Itoa(i), Text: qs[i].Question, Kind: qs[i].Kind, Options: qs[i].Options})
		}
		got, err := b.OwnSearch(ctx, req)
		for k, i := range at {
			no[i] = err == nil && len(got) == len(at) && got[k].Absent()
		}
	}
	return without(qs, no)
}

// without are the questions of qs whose index drop does not hold.
func without(qs []runQuestion, drop map[int]bool) []runQuestion {
	var out []runQuestion
	for i, q := range qs {
		if !drop[i] {
			out = append(out, q)
		}
	}
	return out
}

// askingRun is an application run in the Needs you inbox and the questions it
// left.
type askingRun struct {
	entry notify.Entry
	run   runRecord
	qs    []runQuestion
}

// WaitingRuns are the application runs in the inbox that wait for the user
// with questions, each with those an entry can answer (none when the fill now
// answers them all), the newest first; also is a run read even when its entry
// was acknowledged.
func (s *Handlers) WaitingRuns(ctx context.Context, also string, route learn.Router) ([]askingRun, error) {
	board, err := s.Board()
	if err != nil {
		return nil, err
	}
	entries, err := board.Inbox(ctx)
	if err != nil {
		return nil, err
	}
	if also != "" && !slices.ContainsFunc(entries, func(e notify.Entry) bool { return e.ID == also }) {
		if e, err := board.Get(ctx, also); err == nil {
			entries = append(entries, e)
		}
	}
	var out []askingRun
	left := func(qs []runQuestion) []runQuestion {
		if len(qs) == 0 {
			return nil
		}
		return s.unsettled(ctx, route, qs)
	}
	for _, e := range entries {
		if e.Kind != notify.KindApply || e.State != notify.NeedsYou {
			continue
		}
		r, err := s.loadRun(ctx, e.ID)
		var missing *apikit.Error
		if errors.As(err, &missing) && missing.Code == wire.CodeNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if qs := r.questions(); len(qs) > 0 {
			out = append(out, askingRun{entry: e, run: r, qs: qs})
		}
	}
	// A question routed is a whole matcher pass (some 10 ms on a bank of 300
	// entries), and a few hundred runs wait: they are routed side by side.
	parallel(len(out), func(i int) { out[i].qs = left(out[i].qs) })
	slices.SortStableFunc(out, func(a, b askingRun) int { return b.entry.StateAt.Compare(a.entry.StateAt) })
	return out, nil
}

// open are the questions of a waiting run the bank does not answer outright.
func (w askingRun) open(route learn.Router) int {
	n := 0
	for _, q := range w.qs {
		if q.routed(route).Outright().EntryID == "" {
			n++
		}
	}
	return n
}

// ID names the waiting run.
func (w askingRun) ID() string { return w.run.id }

// Asked counts every question the run left, the settled ones too.
func (w askingRun) Asked() int { return len(w.run.questions()) }

// OpenQuestions counts the open questions of each waiting run, read before an
// answer is saved so the runs that answer completes can be told apart.
func (s *Handlers) OpenQuestions(ctx context.Context) map[string]int {
	route, err := s.Router()
	if err != nil {
		return nil
	}
	runs, err := s.WaitingRuns(ctx, "", route)
	if err != nil || len(runs) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, w := range runs {
		out[w.run.id] = w.open(route)
	}
	return out
}

// AskedQuestions are the questions waiting runs left that the bank cannot
// answer yet, once each: the matcher finds no entry for them, the entry it
// finds is blank, or it leaves to the user a statement about this application
// that only an entry the user wrote answers. Wordings that read the same once
// normalized are one question, and so are the questions that reach a blank
// entry their every-site answer is written into, since that answer answers
// them all; the newest waiting run's wording is the one shown and answered.
func AskedQuestions(route learn.Router, runs []askingRun) []wire.AnswerBankAsked {
	out := []wire.AnswerBankAsked{}
	at := map[string]int{}
	counted := map[string]bool{}
	for _, w := range runs {
		for _, q := range w.qs {
			got := q.routed(route)
			statement := got.Reason == match.ReasonContextPolicy && match.IsUserStatement(q.Question)
			if got.Outcome != match.OutcomeAbstained || got.Reason != match.ReasonNoEntry && got.Reason != match.ReasonPlaceholderEmpty && !statement {
				continue
			}
			key, blank := "question:"+learn.Normalize(q.Question), q.Everywhere && route.FillsBlank(got)
			if blank {
				key = "entry:" + got.EntryID
			}
			i, seen := at[key]
			if !seen {
				i, at[key] = len(out), len(out)
				out = append(out, wire.AnswerBankAsked{Question: q.Question, Kind: q.Kind, Options: q.Options, RunID: w.run.id, Host: q.host, Everywhere: q.Everywhere})
				if blank {
					out[i].EntryID = got.EntryID
				}
			}
			if !counted[key+"\x00"+w.run.id] {
				counted[key+"\x00"+w.run.id] = true
				out[i].Runs++
			}
		}
	}
	return out
}

// AskedLine leads the day's waiting runs with the question most of them left
// that the bank cannot answer yet, once two or more wait on it: one answer in
// the Answer Bank answers it on every one, and each it completes goes on. The
// relationship lists whose mark as complete would answer what they left follow
// (listLines). A bank that cannot be read leaves the lines out. It routes every
// waiting run's questions, so only the day told in words reads it.
func (s *Handlers) AskedLine(ctx context.Context) ([]wire.DayLine, error) {
	route, err := s.Router()
	if err != nil {
		return nil, nil
	}
	runs, err := s.WaitingRuns(ctx, "", route)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	lists := listLines(route, runs, s.Now().Format(time.DateOnly))
	var top wire.AnswerBankAsked
	for _, a := range AskedQuestions(route, runs) {
		if a.Runs > top.Runs {
			top = a
		}
	}
	if top.Runs < 2 {
		return lists, nil
	}
	return append([]wire.DayLine{{Text: fmt.Sprintf("%d waiting applications ask %q: one answer in the Answer Bank answers them all.", top.Runs, top.Question),
		Link: "/answer-bank?show=needs"}}, lists...), nil
}

// listTitles are the Profile page's names of the relationship lists.
var listTitles = map[match.RelationshipFamily]string{
	match.FamilyEmployment: "Companies you worked for", match.FamilyRelative: "Companies where a relative works",
	match.FamilyFriend: "Companies where a friend works", match.FamilyPriorApplication: "Companies you applied to before",
	match.FamilyPriorInterview: "Companies that interviewed you",
}

// listLines name each relationship list on Profile whose mark as complete on
// day would answer a question waiting runs left about their company ("Internal
// Candidate"), with how many runs it answers. Only the user knows a list is
// complete, so the line asks and never marks. Only the relationship lane reads
// a list's mark, so only a question it may take is routed again per list: the
// waiting runs' every question routed six times did not fit activity_today's
// budget.
func listLines(route learn.Router, runs []askingRun, day string) []wire.DayLine {
	type open struct {
		run int
		q   runQuestion
	}
	var asks []open
	for i, w := range runs {
		for _, q := range w.qs {
			if match.MayAskRelationship(q.Question) && q.routed(route).Outright().EntryID == "" {
				asks = append(asks, open{i, q})
			}
		}
	}
	var out []wire.DayLine
	for _, family := range match.RelationshipFamilies {
		marked, fills := route.Confirming(family, day), make([]bool, len(asks))
		parallel(len(asks), func(k int) {
			then := asks[k].q.routed(marked)
			fills[k] = then.Outcome == match.OutcomeFilled && then.EntryID == ""
		})
		answered := map[int]bool{}
		for k, a := range asks {
			if fills[k] {
				answered[a.run] = true
			}
		}
		n := len(answered)
		if n > 0 {
			out = append(out, wire.DayLine{Text: fmt.Sprintf("If your list %q on Profile is complete, mark it complete: it answers what %d waiting %s about it.",
				listTitles[family], n, apikit.OneOr(n, "application asks", "applications ask")), Link: "/profile?tab=identity"})
		}
	}
	return out
}

// AnswerQuestion saves the user's answer to one question of a stopped run
// as their own entry.
func (s *Handlers) AnswerQuestion(c *apikit.Call) (apikit.Reply, error) {
	var req wire.ApplyAnswerRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	everywhere := req.Everywhere
	saved, err := s.saveRunAnswer(c.R.Context(), c.R.PathValue("id"), req.Question, req.Answer, &everywhere, DoneFromDashboard)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: saved}, nil
}

// saveRunAnswer saves an answer to one question of a stopped run as the
// user's own entry, for the questions form and for the Ask Agent (by). The
// question, its options and host come from the record; without a choice of
// everywhere the question's own starting choice applies (F-4).
func (s *Handlers) saveRunAnswer(ctx context.Context, id, question, answer string, everywhere *bool, by string) (wire.ApplyAnswerSaved, error) {
	r, err := s.loadRun(ctx, id)
	if err != nil {
		return wire.ApplyAnswerSaved{}, err
	}
	qs := r.questions()
	i := slices.IndexFunc(qs, func(q runQuestion) bool { return q.Question == question })
	if i < 0 {
		return wire.ApplyAnswerSaved{}, apikit.NewErr(wire.CodeNotFound, "the run left no such question for you")
	}
	q, answer := qs[i], strings.TrimSpace(answer)
	if answer == "" {
		return wire.ApplyAnswerSaved{}, apikit.InvalidField("answer", "write an answer first")
	}
	if len(q.Options) > 0 {
		o := slices.IndexFunc(q.Options, func(o string) bool { return strings.EqualFold(strings.TrimSpace(o), answer) })
		if o < 0 {
			return wire.ApplyAnswerSaved{}, apikit.InvalidField("answer", "pick one of the question's options")
		}
		answer = q.Options[o]
	}
	if everywhere == nil {
		everywhere = &q.Everywhere
	}
	svc, err := s.Learning()
	if err != nil {
		return wire.ApplyAnswerSaved{}, err
	}
	before := s.OpenQuestions(ctx)
	res, err := svc.AddEntry(ctx, learn.AddEntryRequest{Question: q.Question, Answer: answer, Options: q.Options, Host: q.host, Everywhere: *everywhere})
	if err != nil {
		return wire.ApplyAnswerSaved{}, s.LearnError(err)
	}
	return wire.ApplyAnswerSaved{EntryID: res.EntryID, Continued: s.MoveAnswered(ctx, before, r.id, by)}, nil
}

// AnsweredDetail is what the entry of a waiting run whose every question has
// an answer says while the run waits for its retry.
const AnsweredDetail = "Every question has an answer now; retry the run to fill the form with them."

// MoveAnswered goes on with the waiting runs a saved answer completed (F-3,
// flow round 3): the run it was saved to (from) once none of its questions is
// open, as its own Done would, and any other run that waits on answers alone
// with a question open before the save (before) and none after. Each goes on
// in the tab it kept, as a Done by whoever saved the answer, and its entry is
// acknowledged when it finishes well; the apply window takes one run at a time,
// the one the answer was saved to first. One that cannot go on, or whose tab
// is gone, keeps its Done or its retry, and its entry says every question has
// an answer. It reports whether the run the answer was saved to went on.
func (s *Handlers) MoveAnswered(ctx context.Context, before map[string]int, from, by string) bool {
	board, err := s.Board()
	if err != nil || from == "" && len(before) == 0 {
		return false
	}
	runs, err := s.AnsweredRuns(ctx, before, from)
	if err != nil {
		return false
	}
	note := "Resumed with the answers you saved."
	if by == DoneFromAgent {
		note = "Resumed with the answers the Ask Agent saved; Submit waits for your confirmation."
	}
	continued := false
	for _, w := range runs {
		detail := AnsweredDetail
		if s.KeptTab(w.run.id) && notify.CanResume(w.entry) {
			_, err := s.GoOn(ctx, w.run.id, by, note, true)
			if err == nil {
				continued = continued || w.run.id == from
				continue
			}
			// Only a held window leaves the run waiting as it was; after any other
			// refusal its entry is no longer this run's to word.
			var busy *apikit.Error
			if !errors.As(err, &busy) || busy.Details == nil || busy.Details.Reason != wire.ReasonLaneBusy {
				s.Log().Info("a run whose questions were all answered did not go on", "run", w.run.id, "error", err.Error())
				continue
			}
			detail = "Every question has an answer now; Done goes on in the kept tab."
		}
		if _, err := board.Update(ctx, w.run.id, notify.Patch{Detail: &detail}); err != nil {
			s.Log().Warn("an answered run's entry could not say so", "run", w.run.id, "error", err.Error())
		}
	}
	return continued
}

// AnsweredRuns are the runs MoveAnswered moves, the run the answer was saved to
// (from) first: the user is looking at it. They are all chosen before the first
// goes on, so a leg that ends while the others are routed never frees the
// window the next is told it waits for.
func (s *Handlers) AnsweredRuns(ctx context.Context, before map[string]int, from string) ([]askingRun, error) {
	route, err := s.Router()
	if err != nil {
		return nil, err
	}
	runs, err := s.WaitingRuns(ctx, from, route)
	if err != nil {
		s.Log().Warn("the runs waiting for answers could not be read", "error", err.Error())
		return nil, err
	}
	if i := slices.IndexFunc(runs, func(w askingRun) bool { return w.run.id == from }); i > 0 {
		first := runs[i]
		runs = slices.Insert(slices.Delete(runs, i, i+1), 0, first)
	}
	return slices.DeleteFunc(runs, func(w askingRun) bool {
		return w.open(route) > 0 || w.run.id != from && (before[w.run.id] == 0 || w.entry.Reason != "answer")
	}), nil
}

// agentRuns serves the Ask Agent's run_questions and answer_run_question from
// the records, lookups and save of the run's questions form (AC-4).
type agentRuns struct{ s *Handlers }

var _ agent.StoppedRuns = agentRuns{}

// StoppedRuns is the Ask Agent's way to the questions of a stopped run.
func (s *Handlers) StoppedRuns() agent.StoppedRuns { return agentRuns{s} }

// InTab are the required fields of a stopped run no answer saved here fills.
func (a agentRuns) InTab(ctx context.Context, runID string) ([]string, error) {
	r, err := a.s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return r.inTab(), nil
}

// Questions are a run's questions, each with the answers the bank and the
// profile give it now: the entry the form shows, and what answer_bank_lookup's
// preview fills outright. A match the bank holds for review is not one: saved
// by the agent it would become the user's own confirmed entry. A follow-up the
// answer before it settles is no question: its "N/A" saved as an entry would
// answer the follow-up under a Yes too.
func (a agentRuns) Questions(ctx context.Context, runID string) ([]agent.RunQuestion, error) {
	b, err := a.s.Bind()
	if err != nil {
		return nil, err
	}
	r, err := a.s.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	route, err := a.s.Router()
	if err != nil {
		return nil, err
	}
	var out []agent.RunQuestion
	for _, q := range a.s.unsettled(ctx, route, r.questions()) {
		got := q.routed(route)
		// A follow-up the answer before it settles waits on nothing of its own.
		if got.ByParent {
			continue
		}
		got = got.Outright()
		mq := match.Question{Selector: "q", Text: q.Question, Kind: match.KindText}
		if len(q.Options) > 0 {
			mq.Kind = match.KindSelect
			for _, o := range q.Options {
				mq.Options = append(mq.Options, match.Option{Value: o, Label: o})
			}
		}
		pre, err := b.Preview(ctx, bind.Request{Host: q.host, Questions: []match.Question{mq}})
		if err != nil {
			return nil, a.s.AnswerBankError(err)
		}
		found := []string{got.Answer}
		for _, res := range pre.Results {
			if res.Outcome == match.OutcomeFilled {
				found = append(append(found, res.ChosenLabel, res.Answer), res.ChosenLabels...)
			}
		}
		found = slices.DeleteFunc(found, func(f string) bool { return strings.TrimSpace(f) == "" })
		slices.Sort(found)
		out = append(out, agent.RunQuestion{Question: q.Question, Kind: q.Kind, Options: q.Options, Found: slices.Compact(found)})
	}
	return out, nil
}

// SaveAnswer saves an answer as the questions form does. The last one
// continues the run with its Submit held for the user.
func (a agentRuns) SaveAnswer(ctx context.Context, runID, question, answer string, everywhere *bool) (agent.RunAnswerSaved, error) {
	saved, err := a.s.saveRunAnswer(ctx, runID, question, answer, everywhere, DoneFromAgent)
	return agent.RunAnswerSaved{EntryID: saved.EntryID, Continued: saved.Continued}, err
}

// TakeoverRun is POST /api/apply/runs/{id}/takeover, "Let the agent finish"
// (AC-4): an Ask Agent turn for a stopped run whose tab is kept, in the
// dashboard conversation that started the run, else a new one.
func (s *Handlers) TakeoverRun(c *apikit.Call) (apikit.Reply, error) {
	board, err := s.Board()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx, id := c.R.Context(), c.R.PathValue("id")
	e, err := board.Get(ctx, id)
	switch {
	case errors.Is(err, notify.ErrNotFound):
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "there is no such run")
	case err != nil:
		return apikit.Reply{}, err
	case !notify.CanResume(e) || !s.KeptTab(id):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the agent can finish only an application run that waits for you in the tab it kept")
	}
	if err := s.ModelMissing(ctx); err != nil {
		return apikit.Reply{}, err
	}
	r, err := s.loadRun(ctx, id)
	if err != nil {
		return apikit.Reply{}, err
	}
	sid := ""
	if org, err := s.Origin(ctx, id); err == nil {
		if sess, err := s.Session(ctx, org.SessionID); err == nil && sess.Channel == chatsessions.Dashboard {
			sid = sess.ID
		}
	}
	res, err := s.Send(ctx, agent.SendRequest{SessionID: sid, Channel: agent.ChannelDashboard, FinishRun: id,
		Text: agent.FinishMessage(id, r.company, r.role, postingHost(r.posting))})
	if err != nil {
		return apikit.Reply{}, s.ChatError(err)
	}
	s.Notify("runs")
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/chat/sessions/" + res.SessionID + "/events", Data: wire.TakeoverStarted{SessionID: res.SessionID}}, nil
}

// GetBatch answers the record of a list: its rows with their state and
// outcome, and the stopped rows grouped by who has the next step.
func (s *Handlers) GetBatch(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx, id := c.R.Context(), c.R.PathValue("id")
	var v wire.ApplyBatchView
	var created, closed, caller string
	var submit bool
	err = st.Read().QueryRowContext(ctx, `SELECT batch_id, created_at, COALESCE(closed_at, ''), caller, submit FROM apply_batches WHERE batch_id = ?`, id).
		Scan(&v.BatchID, &created, &closed, &caller, &submit)
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no application batch is called %q", id)
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	v.State, v.Mode, v.Caller = wire.BatchOpen, modeOf(submit), caller
	if closed != "" {
		v.State = wire.BatchClosed
	}
	v.CreatedAt = apikit.Stamp(created)
	v.Rows, v.Owners, v.Defects = []wire.ApplyBatchRow{}, []wire.ApplyOwnerGroup{}, []wire.ApplyDefectShape{}
	rows, err := st.Read().QueryContext(ctx, `SELECT b.idx, b.url, b.company, b.role, b.mirrors, b.state, b.run_id, b.note,
		r.run_id, COALESCE(r.finished_at, ''), r.outcome_kind, r.outcome_stage, r.outcome_class, r.outcome_reason, r.confirmed
		FROM apply_batch_rows b LEFT JOIN apply_runs r ON r.run_id = b.run_id WHERE b.batch_id = ? ORDER BY b.idx`, id)
	if err != nil {
		return apikit.Reply{}, err
	}
	defer rows.Close()
	var results []apply.ListResult
	for rows.Next() {
		var idx, mirrors int
		var u, company, role, state, runID, note string
		var rid, fin, kind, stage, class, reason sql.NullString
		var confirmed sql.NullBool
		if err := rows.Scan(&idx, &u, &company, &role, &mirrors, &state, &runID, &note, &rid, &fin, &kind, &stage, &class, &reason, &confirmed); err != nil {
			return apikit.Reply{}, err
		}
		row := wire.ApplyBatchRow{Index: apikit.Safe(idx), Company: company, Role: role, Host: postingHost(u), Mirrors: apikit.Safe(mirrors), State: state, RunID: runID, Note: note}
		if state == wire.BatchRowStarted && v.State == wire.BatchClosed && note == "" {
			row.Note = "interrupted: possibly submitted; the row is not run again"
		}
		if rid.Valid {
			o := apply.Outcome{Kind: apply.OutcomeKind(kind.String), Stage: apply.Stage(stage.String), Class: apply.Class(class.String), Reason: reason.String, Confirmed: confirmed.Bool}
			row.Outcome = &wire.ApplyOutcome{Kind: kind.String, Stage: stage.String, Class: class.String, Reason: reason.String, Confirmed: confirmed.Bool, Owner: apply.OwnerOf(o)}
			results = append(results, apply.ListResult{Index: idx, Row: apply.ListRow{URL: u, Company: company, Role: role}, Run: &apply.Run{ID: rid.String, Outcome: o}})
		}
		v.Rows = append(v.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return apikit.Reply{}, err
	}
	snap := filepath.Join(s.Workspace.StateDir(), "apply", "snapshots")
	ans := apply.Answer(results, func(runID string) string {
		p := filepath.Join(snap, runID+"-stop.png")
		if _, err := os.Stat(p); err != nil {
			return ""
		}
		return filepath.Join(".applysmith", "apply", "snapshots", runID+"-stop.png")
	})
	for _, g := range ans.Owners {
		v.Owners = append(v.Owners, wire.ApplyOwnerGroup{Owner: g.Owner, Companies: g.Companies})
	}
	for _, d := range ans.Defects {
		v.Defects = append(v.Defects, wire.ApplyDefectShape{Stage: string(d.Stage), Class: string(d.Class), Reason: d.Reason, Companies: d.Companies, Snapshot: d.Snapshot})
	}
	return apikit.Reply{Data: v}, nil
}

// parallel calls f for 0..n-1 on as many goroutines as the machine runs Go on.
func parallel(n int, f func(int)) {
	var g errgroup.Group
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i := range n {
		g.Go(func() error { f(i); return nil })
	}
	_ = g.Wait()
}
