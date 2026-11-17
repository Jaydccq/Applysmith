package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/gmailapi"
	"applysmith/internal/api/knowledgeapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/documents"
	"applysmith/internal/evaluate"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
	"applysmith/internal/store"
)

// runStreams keeps the event stream of each run that is active or finished a
// moment ago. A subscriber to a run with no stream gets the stored state and
// nothing more, because nothing is left to say.
type runStreams struct {
	mu    sync.Mutex
	epoch string
	items map[string]*eventStream
}

func newRunStreams(epoch string) *runStreams {
	return &runStreams{epoch: epoch, items: map[string]*eventStream{}}
}

func (r *runStreams) stream(id string) *eventStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	es := r.items[id]
	if es == nil {
		es = newEventStream(r.epoch)
		r.items[id] = es
	}
	return es
}

// streamFor is the stream a subscriber to run joins. A run that ended a moment
// ago still has its stream, so a client that resumes with the id of the last
// event it read is recognised. A run whose stream is gone has nothing more to
// say, so its subscriber gets a stream of its own that is not kept:
// subscribing must not grow the table.
func (r *runStreams) streamFor(id string, run store.Run) *eventStream {
	if !terminal(run.Status) {
		return r.stream(id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if es := r.items[id]; es != nil {
		return es
	}
	return newEventStream(r.epoch)
}

func (r *runStreams) publish(id, name string, payload any) { r.stream(id).publish(name, payload) }

// retire drops the stream once late subscribers have had time to read the
// final event.
func (r *runStreams) retire(id string) {
	time.AfterFunc(runRetention, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.items, id)
	})
}

func terminal(status string) bool {
	switch status {
	case wire.RunSucceeded, wire.RunFailed, wire.RunCancelled, wire.RunReviewRequired:
		return true
	}
	return false
}

func toRunView(r store.Run) wire.RunView {
	return wire.RunView{
		RunID: r.ID, Kind: r.Kind, Status: r.Status, Stage: r.Stage, FailureReason: r.FailureReason,
		CreatedAt: apikit.Stamp(r.CreatedAt), UpdatedAt: apikit.Stamp(r.UpdatedAt), FinishedAt: apikit.StampPtr(r.FinishedAt),
	}
}

func (s *Server) getRun(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	s.syncBoard()
	run, err := st.GetRun(c.R.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		// A task that has no run record, such as an authorization to renew, is
		// still an entry on the board.
		if entry, ok := s.boardEntry(c.R.Context(), id); ok {
			view := viewOfEntry(entry)
			view.Entry.Fix = s.fixOf(c.R.Context(), entry)
			view.Conversation = s.conversationOf(c.R.Context(), id)
			return apikit.Reply{Data: view}, nil
		}
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	view := toRunView(run)
	if entry, ok := s.boardEntry(c.R.Context(), id); ok {
		e := toBoardEntry(entry)
		e.Fix = s.fixOf(c.R.Context(), entry)
		view.Entry = &e
	}
	view.Conversation = s.conversationOf(c.R.Context(), id)
	return apikit.Reply{Data: view}, nil
}

// conversationOf is the Ask Agent conversation that started a run, empty when
// none did.
func (s *Server) conversationOf(ctx context.Context, runID string) string {
	d, err := s.dom()
	if err != nil {
		return ""
	}
	o, err := d.origins.Get(ctx, runID)
	if err != nil {
		return ""
	}
	return o.SessionID
}

// Where the fix of a waiting entry is made when it is not the kept tab.
const (
	fixApplications = "/settings#settings-applications"
	fixRuntime      = "/settings#settings-runtime"
	fixGmail        = "/settings#settings-gmail"
	fixSignIn       = "/settings#settings-signin"
)

// fixOf is the one-click fix of a waiting entry: an application run that the
// account creation gate stopped for a setting only the user changes, or that
// could not read a mail because Gmail is not connected, points at that
// setting; one whose saved password was refused points at the sign-in
// credentials with the refusing host filled in; a scan's sign-in item points
// at the session sync of its site. Nothing else has one.
func (s *Server) fixOf(ctx context.Context, e notify.Entry) *wire.EntryFix {
	if !e.Waiting() {
		return nil
	}
	if source, ok := strings.CutPrefix(e.ID, scanSignInID); ok && e.Kind == notify.KindTask {
		return scanSignInFix(source)
	}
	if e.Kind != notify.KindApply {
		return nil
	}
	st, err := s.db()
	if err != nil {
		return nil
	}
	var raw string
	if st.Read().QueryRowContext(ctx, `SELECT signins_json FROM apply_runs WHERE run_id = ?`, e.ID).Scan(&raw) != nil {
		return nil
	}
	var recs []apply.SigninRecord
	if json.Unmarshal([]byte(raw), &recs) != nil {
		return nil
	}
	if fix := signinFix(recs); fix != nil || e.Reason != "credential-refused" {
		return fix
	}
	return passwordFix(recs)
}

// passwordFix points a stop for a refused or run-out saved password at the
// site override of the host that refused it, where a new one is saved.
func passwordFix(recs []apply.SigninRecord) *wire.EntryFix {
	for _, r := range recs {
		if r.Host != "" && r.Succeeded == "" && (r.Exhausted || r.Held || r.Refusals > 0) {
			host := strings.ToLower(r.Host)
			return &wire.EntryFix{To: "/settings?site=" + url.QueryEscape(host) + "#settings-signin", Label: "Change the saved password for " + host}
		}
	}
	return &wire.EntryFix{To: fixSignIn, Label: "Open the sign-in credentials"}
}

// signinFix is the fix the sign-in records of a stopped run point at.
func signinFix(recs []apply.SigninRecord) *wire.EntryFix {
	for _, r := range recs {
		switch r.Signup {
		case gates.CodeCooldownActive:
			return &wire.EntryFix{To: fixApplications, Label: "See the cooldown"}
		case gates.CodeAgreementNeedsSetting:
			return &wire.EntryFix{To: fixRuntime, Label: "Open the legal declarations setting"}
		}
		if r.Mail == apply.MailNotConnected {
			return &wire.EntryFix{To: fixGmail, Label: "Connect Gmail"}
		}
	}
	return nil
}

// scanSignInFix points a scan's sign-in item at the session sync of the
// source's site, which the user starts (the run's own sync covers application
// runs only), and names the source so the item can run the scan again.
func scanSignInFix(source string) *wire.EntryFix {
	info, err := discovery.LookupSource(source)
	if err != nil || info.Host == "" || forbiddenSyncDomain(info.Host) {
		return &wire.EntryFix{To: "/browser", Label: "Sign in in the app browser", Scan: source}
	}
	return &wire.EntryFix{To: "/browser?sync=" + url.QueryEscape(info.Host), Label: "Sync the sign-in from your Chrome", Scan: source}
}

// boardEntry reads an entry as the dashboard shows it (notify.Board.Shown).
func (s *Server) boardEntry(ctx context.Context, id string) (notify.Entry, bool) {
	d := s.domain.Load()
	if d == nil || d.notify == nil {
		return notify.Entry{}, false
	}
	e, err := d.notify.board.Get(ctx, id)
	if err != nil {
		return notify.Entry{}, false
	}
	return d.notify.board.Shown(ctx, e), true
}

// viewOfEntry is the run view of an entry that has no run record.
func viewOfEntry(e notify.Entry) wire.RunView {
	status := wire.RunRunning
	if e.State == notify.Done {
		switch e.Outcome {
		case notify.OutcomeOK:
			status = wire.RunSucceeded
		case notify.OutcomeCancelled:
			status = wire.RunCancelled
		default:
			status = wire.RunFailed
		}
	}
	be := toBoardEntry(e)
	return wire.RunView{RunID: e.ID, Kind: string(e.Kind), Status: status, Stage: e.Stage, CreatedAt: be.StartedAt, UpdatedAt: be.UpdatedAt,
		FinishedAt: be.FinishedAt, Entry: &be}
}

// runEvents streams a run: its stored state first, then each phase or
// progress line, ending with the final event. A run that has ended gets its
// state and final event and the stream closes.
func (s *Server) runEvents(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	run, err := st.GetRun(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	preamble := func() ([]sseEvent, bool) {
		run, err := st.GetRun(c.R.Context(), id)
		if err != nil {
			return nil, true
		}
		events := []sseEvent{newEvent("state", toRunView(run))}
		if !terminal(run.Status) {
			return events, false
		}
		return append(events, newEvent("final", s.storedFinal(c, run))), true
	}
	return apikit.Reply{}, s.serveStream(c, s.runs.streamFor(id, run), preamble)
}

// storedFinal rebuilds the final event of a run that ended before the stream
// was opened.
func (s *Server) storedFinal(c *apikit.Call, run store.Run) wire.RunFinal {
	final := wire.RunFinal{RunID: run.ID, Status: run.Status, Reason: run.FailureReason}
	if run.Kind == "documents" {
		if svc := s.docsService(); svc != nil {
			if res, err := svc.Outcome(c.R.Context(), run.ID); err == nil {
				final = documentFinal(run.ID, run.Status, res)
			}
		}
		return final
	}
	if run.Kind != "evaluate" {
		return final
	}
	if res, _, found, err := evaluate.JobOutcome(c.R.Context(), s.store.Load(), run.ID); err == nil && found {
		final = finalOf(run.ID, run.Status, res)
	}
	return final
}

func finalOf(runID, status string, res evaluate.Result) wire.RunFinal {
	f := wire.RunFinal{RunID: runID, Status: status, Outcome: string(res.Outcome), Reason: res.Reason, Decision: res.Decision, Review: res.Review}
	if res.ReportNumber > 0 {
		n := apikit.Safe64(res.ReportNumber)
		f.ReportNumber = &n
	}
	return f
}

// EvaluationStatus is the run status a finished job ends in; it matches what
// the evaluator stores.
func EvaluationStatus(res evaluate.Result) string {
	switch {
	case res.Outcome == evaluate.OutcomeFailed && res.Reason == evaluate.ReasonCancelled:
		return wire.RunCancelled
	case res.Outcome == evaluate.OutcomeFailed:
		return wire.RunFailed
	case res.Review:
		return wire.RunReviewRequired
	}
	return wire.RunSucceeded
}

// onEvaluationEvent carries an evaluation's progress to the run stream and
// tells the board what changed.
func (s *Server) onEvaluationEvent(ev evaluate.Event) {
	if !ev.Final {
		s.runs.publish(ev.RunID, "phase", wire.RunPhase{RunID: ev.RunID, Phase: string(ev.Phase)})
		s.notify("runs")
		return
	}
	res := evaluate.Result{Outcome: ev.Outcome}
	if ev.Result != nil {
		res = *ev.Result
	}
	s.runs.publish(ev.RunID, "final", finalOf(ev.RunID, EvaluationStatus(res), res))
	s.runs.retire(ev.RunID)
	s.notify("runs")
	if res.ReportNumber > 0 {
		s.notify("reports", "tracker")
	}
}

// notify bumps board topics; a failure is a defect of the caller's topic name.
// A change to the runs first lets the runs board follow the run records, so a
// read after the notice already sees the entry.
func (s *Server) notify(topics ...string) {
	if slices.Contains(topics, "runs") {
		s.syncBoard()
	}
	s.notifyTopics(topics...)
}

// notifyTopics advances board topics without touching the runs board; the
// board itself reports its changes through it.
func (s *Server) notifyTopics(topics ...string) {
	for _, t := range topics {
		if err := s.Notify(t, ""); err != nil && s.log != nil {
			s.log.Error("board notice failed", "topic", t, "error", err.Error())
		}
	}
}

// cancellableKinds are the runs the server's own tasks can stop; a scheduled
// job is stopped through the scheduler. A pass over the leads or the registry
// stops between items; a scan stops between boards and pages.
var cancellableKinds = []string{"evaluate", "scan", "documents", "leads", "registry", "signals", knowledgeapi.IngestKind, knowledgeapi.BriefKind, knowledgeapi.ProposeKind, gmailapi.DigestKind, interviewCoachKind}

func (s *Server) cancelRun(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	run, err := st.GetRun(c.R.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		// An application run lives on the board only.
		if entry, ok := s.boardEntry(c.R.Context(), id); ok && entry.Kind == notify.KindApply {
			return s.cancelApplyRun(c, d, entry)
		}
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	if terminal(run.Status) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the run has already ended")
	}
	scheduled := false
	if d.sched != nil {
		_, scheduled = d.sched.Registry().Get(run.Kind)
	}
	if !slices.Contains(cancellableKinds, run.Kind) && !scheduled {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "a %s run cannot be cancelled", run.Kind)
	}
	// The stream hears about the request before the run can end, so the
	// cancelled event always follows the cancelling one.
	cancelling := toRunView(run)
	cancelling.Status = wire.RunCancelling
	s.runs.publish(id, "state", cancelling)
	var accepted bool
	switch run.Kind {
	case "evaluate":
		accepted = d.pool.Cancel(id)
	case "documents":
		accepted = d.docs.Cancel(id)
	case "scan", "leads", "registry", "signals", knowledgeapi.IngestKind, knowledgeapi.BriefKind, knowledgeapi.ProposeKind, gmailapi.DigestKind, interviewCoachKind:
		accepted = d.scans.cancel(id)
	default:
		accepted = d.sched.Cancel(id)
	}
	if !accepted {
		s.runs.publish(id, "state", toRunView(run))
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the run is not active in this server")
	}
	run, err = st.GetRun(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	view := toRunView(run)
	if !terminal(run.Status) {
		view.Status = wire.RunCancelling
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + id, Data: view}, nil
}

// docsService is the document service, or nil before the server is ready.
func (s *Server) docsService() *documents.Service {
	if d := s.domain.Load(); d != nil {
		return d.docs
	}
	return nil
}

// endScheduledStream closes the stream of a scheduled run with how it ended.
func (s *Server) endScheduledStream(f scheduler.Finish) {
	final := wire.RunFinal{RunID: f.RunID, Status: f.Status, Outcome: f.Summary}
	if f.Status != wire.RunSucceeded {
		final.Reason = f.Detail
	}
	s.runs.publish(f.RunID, "final", final)
	s.runs.retire(f.RunID)
}

// documentFinal is the final event of a document run.
func documentFinal(runID, status string, res documents.Result) wire.RunFinal {
	f := wire.RunFinal{RunID: runID, Status: status, Reason: res.Reason}
	if res.Published {
		f.Outcome = "published"
	} else {
		f.Outcome = "failed"
	}
	for _, id := range res.Documents {
		f.Documents = append(f.Documents, apikit.Safe64(id))
	}
	return f
}

// onDocumentEvent carries a document run's progress to the run stream.
func (s *Server) onDocumentEvent(ev documents.Event) {
	if !ev.Final {
		s.runs.publish(ev.RunID, "phase", wire.RunPhase{RunID: ev.RunID, Phase: ev.Stage})
		s.notify("runs")
		return
	}
	res := documents.Result{}
	if ev.Result != nil {
		res = *ev.Result
	}
	status := wire.RunSucceeded
	switch {
	case res.Published:
	case res.Reason == documents.ReasonCancelled:
		status = wire.RunCancelled
	default:
		status = wire.RunFailed
	}
	s.runs.publish(ev.RunID, "final", documentFinal(ev.RunID, status, res))
	s.runs.retire(ev.RunID)
	s.notify("runs", "tracker")
}

// documentsSentence is what a documents run that succeeded came to: the
// versions it wrote, the prep pack revision it stored, or the proposal that
// waits for review, and that a model wrote none of it when none did.
func (n narrator) documentsSentence(ctx context.Context, runID string) string {
	if n.docs == nil {
		return ""
	}
	res, err := n.docs.Outcome(ctx, runID)
	if err != nil || !res.Published {
		return ""
	}
	var made []string
	for _, id := range res.Documents {
		d, err := n.docs.Get(ctx, id)
		if err != nil {
			continue
		}
		name := "résumé"
		if d.Kind == documents.KindCover {
			name = "cover letter"
		}
		made = append(made, fmt.Sprintf("%s version %d", name, d.Version))
	}
	var s string
	switch {
	case res.Pack != nil:
		s = fmt.Sprintf("Prep pack revision %d is ready.", res.Pack.Revision)
	case res.Proposal > 0:
		s = "A proposed revision waits for your review."
	case len(made) == 1:
		s = made[0] + " is ready to review."
	case len(made) > 1:
		s = strings.Join(made, " and ") + " are ready to review."
	default:
		return ""
	}
	if res.Generation == documents.GenDeterministic && res.Pack == nil {
		s += " Written without a model; Revision details says why."
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
