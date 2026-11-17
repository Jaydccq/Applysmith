package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/origins"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/vault"
)

// maxStoredMessage bounds the user's message kept with an application run.
const maxStoredMessage = 2000

// recordOrigin notes the conversation and the request a run of the Ask Agent
// came from, which a Done and the run's completion message read.
func (a *applyStack) recordOrigin(ctx context.Context, id string, row apply.ListRow, req agent.ApplyRequest) {
	err := a.d.origins.RecordApply(context.WithoutCancel(ctx), req.ConversationID, origins.ApplyRequest{
		RunID: id, TurnID: req.TurnID, Caller: string(req.Caller), Tool: req.Tool, Submit: req.Submit, Explicit: req.ExplicitRequest,
		Outside: req.Outside, Confirmed: req.Confirmed, UserMessage: truncate(req.UserMessage, maxStoredMessage),
		URL: row.URL, EmployerURL: row.EmployerURL, Company: row.Company, Role: row.Role, Named: row.Named, Resubmit: row.Resubmit})
	if err != nil {
		a.s.log.Warn("the origin of an application run was not recorded", "run", id, "error", err.Error())
	}
}

// recordStart notes the request a run the user started came from, which a
// Done, a saved answer and "Submit this form" read.
func (a *applyStack) recordStart(ctx context.Context, id string, row apply.ListRow, opt apply.ListOptions) {
	err := apply.SaveRequest(context.WithoutCancel(ctx), a.s.store.Load(), apply.StartRequest{RunID: id, Caller: opt.Caller, Tool: opt.Tool,
		Submit: opt.Submit, Explicit: opt.ExplicitRequest, URL: row.URL, EmployerURL: row.EmployerURL, Company: row.Company, Role: row.Role,
		Named: row.Named, Resubmit: row.Resubmit}, a.s.now())
	if err != nil {
		a.s.log.Warn("the request of an application run was not recorded", "run", id, "error", err.Error())
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// doneRun is POST /api/runs/{id}/done.
func (s *Server) doneRun(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	e, err := d.apply.resumeRun(c.R.Context(), c.R.PathValue("id"), applyapi.DoneFromDashboard)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: toBoardEntry(d.notify.board.Shown(c.R.Context(), e))}, nil
}

// resumeRun is the Done action: a run waiting for the user goes on from the
// page its kept tab shows. A press from Telegram stops the run before the final
// Submit, which only the dashboard confirms. Done is not an input to any gate:
// the run's submit gate decides again from the original request.
func (a *applyStack) resumeRun(ctx context.Context, id, by string) (notify.Entry, error) {
	return a.goOn(ctx, id, by, "Resumed from "+by+".", false)
}

// goOn continues a run that waits for a step the user finished, for Done and
// for the last saved answer; note is the entry's detail meanwhile. ack
// acknowledges the entry once the run ends done.
func (a *applyStack) goOn(ctx context.Context, id, by, note string, ack bool) (notify.Entry, error) {
	entry, err := a.d.notify.board.Get(ctx, id)
	switch {
	case errors.Is(err, notify.ErrNotFound):
		return notify.Entry{}, apikit.NewErr(wire.CodeNotFound, "there is no such run")
	case err != nil:
		return notify.Entry{}, err
	case !notify.CanResume(entry):
		return notify.Entry{}, notWaiting(entry)
	}
	l := leg{note: note, move: a.d.notify.board.Resume, ack: ack}
	in, err := a.requestOf(ctx, entry, id, by)
	if err != nil {
		return notify.Entry{}, err
	}
	l.in = in
	if entry.Origin == notify.OriginAgent {
		l.in.Read = func(g apply.PageGlance) { a.d.completion.resumed(id, by, g) }
		l.moved = func(ctx context.Context) {
			if err := a.d.origins.Pressed(ctx, id, by); err != nil {
				a.s.log.Warn("a Done press was not recorded", "run", id, "error", err.Error())
			}
		}
	}
	return a.continueRun(ctx, id, l)
}

// requestOf rebuilds the request a run was started from, the one every gate of
// a leg that goes on in its kept tab decides from again: a run of the Ask Agent
// from its conversation's recorded call, any other from the start record.
func (a *applyStack) requestOf(ctx context.Context, entry notify.Entry, id, by string) (apply.ResumeInput, error) {
	if entry.Origin == notify.OriginAgent {
		req, org, err := a.d.origins.Request(ctx, id)
		if errors.Is(err, origins.ErrNotFound) {
			return apply.ResumeInput{}, errNoRequest()
		}
		if err != nil {
			return apply.ResumeInput{}, err
		}
		in := resumeInput(req, org, id, by)
		in.Recheck = a.recheck(req)
		return in, nil
	}
	q, err := apply.LoadRequest(ctx, a.s.store.Load(), id)
	if errors.Is(err, apply.ErrNoRequest) {
		return apply.ResumeInput{}, errNoRequest()
	}
	if err != nil {
		return apply.ResumeInput{}, err
	}
	return startInput(q, by), nil
}

func errNoRequest() *apikit.Error {
	return apikit.NewErr(wire.CodeConflict, "no request is recorded for this run, so it cannot be resumed")
}

// submitKeptRun is POST /api/apply/runs/{id}/submit, "Submit this form": the
// dashboard sends it only after the confirmation that names the company, the
// role and the host, as for Autofill and submit.
func (s *Server) submitKeptRun(c *apikit.Call) (apikit.Reply, error) {
	caller, e := applyapi.StartCaller(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.ApplySubmitRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if e := applyapi.ConfirmSubmit(true, req.Confirmed); e != nil {
		return apikit.Reply{}, e
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if _, err := d.apply.submitKept(c.R.Context(), id, caller); err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + id, Data: wire.RunAccepted{RunID: id}}, nil
}

// submitKept goes on with a run whose filled form waits at Submit in its kept
// tab, now confirmed to submit: by "Submit this form" on the board, or by the
// Ask Agent's call to submit that posting (AC-6). The entry's move settles the
// hold in one write, so of two confirmations the second is refused before
// anything is pressed. The run's request becomes the original one with the
// confirmed submit (a run the user started: made by caller); the run walks the
// page again and decides every check and gate again before it presses.
func (a *applyStack) submitKept(ctx context.Context, id string, caller gates.Caller) (notify.Entry, error) {
	entry, err := a.d.notify.board.Get(ctx, id)
	switch {
	case errors.Is(err, notify.ErrNotFound):
		return notify.Entry{}, apikit.NewErr(wire.CodeNotFound, "there is no such run")
	case err != nil:
		return notify.Entry{}, err
	case !notify.CanSubmitKept(entry):
		return notify.Entry{}, apikit.NewErr(wire.CodeConflict, "only a filled form waiting at Submit in its kept tab can be submitted from here; this run's was already settled or no longer waits")
	}
	l := leg{note: "Submit this form was confirmed; the form is checked again before Submit.", move: a.d.notify.board.ResumeToSubmit}
	// Once the hold is settled the run's request is recorded with the confirmed
	// submit, so a later Done decides from it; one that could not be written
	// leaves the earlier request, which never submits more.
	if entry.Origin == notify.OriginAgent {
		// As with the conversation's confirmation card, a local token never
		// confirms the held submit of a run the Ask Agent started: only the
		// dashboard session is the user's own act (HeldSubmitConfirmation).
		if caller == gates.CallerCLI {
			return notify.Entry{}, apikit.NewErr(wire.CodeForbidden, "the filled form of a run the Ask Agent started is submitted from the dashboard, not with a local token")
		}
		req, org, err := a.d.origins.Request(ctx, id)
		if errors.Is(err, origins.ErrNotFound) {
			return notify.Entry{}, errNoRequest()
		}
		if err != nil {
			return notify.Entry{}, err
		}
		l.in = a.confirmedInput(req, org, id)
		l.moved = func(ctx context.Context) {
			if err := a.d.origins.Confirm(context.WithoutCancel(ctx), id); err != nil {
				a.s.log.Warn("a confirmed submit was not recorded with its run", "run", id, "error", err.Error())
			}
		}
		return a.continueRun(ctx, id, l)
	}
	st := a.s.store.Load()
	q, err := apply.LoadRequest(ctx, st, id)
	if errors.Is(err, apply.ErrNoRequest) {
		return notify.Entry{}, errNoRequest()
	}
	if err != nil {
		return notify.Entry{}, err
	}
	q.Caller, q.Submit, q.Explicit = caller, true, true
	l.in = apply.ResumeInput{Input: q.Input()}
	l.moved = func(ctx context.Context) {
		if err := apply.SaveRequest(context.WithoutCancel(ctx), st, q, a.s.now()); err != nil {
			a.s.log.Warn("a confirmed submit was not recorded with its run", "run", id, "error", err.Error())
		}
	}
	return a.continueRun(ctx, id, l)
}

// confirmedInput continues a run of the Ask Agent whose filled form the user
// confirmed: its conversation's request, now submitting explicitly with the
// confirmation the agent's submit gate asks for, so that gate allows the press
// and nothing holds it.
func (a *applyStack) confirmedInput(req origins.ApplyRequest, org origins.Origin, id string) apply.ResumeInput {
	req.Submit, req.Explicit, req.Confirmed = true, true, true
	in := resumeInput(req, org, id, "")
	in.Recheck = a.recheck(req)
	return in
}

// heldForm names the run of a conversation whose filled form waits at Submit in
// its kept tab, for the one posting a call of that conversation explicitly asks
// to submit: the newest run of that posting, when the conversation started it
// and Submit this form applies to its entry. The call then confirms that form
// instead of filling the posting again (AC-6), and the conversation is told how
// it goes. It is "" otherwise, and the call starts a run.
func (a *applyStack) heldForm(ctx context.Context, req agent.ApplyRequest) string {
	if req.Tool != gates.ToolAutofillApplication || !req.Submit || !req.ExplicitRequest || len(req.Rows) != 1 {
		return ""
	}
	key, stp := apply.IdentityKey(req.Rows[0].URL)
	if stp != nil {
		return ""
	}
	var id string
	if err := a.s.store.Load().Read().QueryRowContext(ctx, `SELECT run_id FROM apply_runs WHERE identity_key = ? ORDER BY started_at DESC, run_id DESC LIMIT 1`,
		key).Scan(&id); err != nil {
		return ""
	}
	if e, err := a.d.notify.board.Get(ctx, id); err != nil || !notify.CanSubmitKept(e) {
		return ""
	}
	if o, err := a.d.origins.Get(ctx, id); err != nil || o.SessionID != req.ConversationID {
		return ""
	}
	return id
}

// leg is how a waiting run goes on in its kept tab.
type leg struct {
	in apply.ResumeInput
	// note is the board detail of the move back to working; move checks that
	// the entry still waits for this press and moves it, in one write.
	note string
	move func(ctx context.Context, id, detail string) (notify.Entry, error)
	// moved runs once the entry moved, to record the press.
	moved func(context.Context)
	// ack acknowledges the entry when the run ends done.
	ack bool
}

// continueRun moves a waiting run back to working and goes on in the tab it
// kept. The entry moves first, in one write, so of two presses the second is
// refused; a tab that is gone blocks the entry instead.
func (a *applyStack) continueRun(ctx context.Context, id string, l leg) (notify.Entry, error) {
	if e := a.takeLane(id); e != nil {
		return notify.Entry{}, e
	}
	a.unlend(id) // the run goes on by itself, holding the window
	entry, err := l.move(ctx, id, l.note)
	switch {
	case errors.Is(err, notify.ErrStateChanged):
		a.busy.Store(false)
		return notify.Entry{}, apikit.NewErr(wire.CodeConflict, "the run is no longer waiting for you; it was resumed or has changed")
	case err != nil:
		a.busy.Store(false)
		return notify.Entry{}, err
	}
	if l.moved != nil {
		l.moved(ctx)
	}
	tab := a.tabOf(ctx, id)
	if tab == "" {
		a.busy.Store(false)
		return a.lostTab(ctx, id), nil
	}
	in := l.in
	in.RunID, in.TabID = id, tab
	rctx, stop := context.WithCancel(a.s.bg)
	a.track(id, stop)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		a.untrack(id)
		stop()
		a.busy.Store(false)
		return entry, apikit.Unavailable("the server is stopping")
	}
	a.wg.Go(func() {
		defer a.busy.Store(false)
		defer a.untrack(id)
		defer stop()
		a.finish(a.runner.Resume(rctx, in))
		if l.ack {
			a.ackEnded(id)
		}
	})
	a.mu.Unlock()
	return entry, nil
}

// ackEnded acknowledges an entry whose continued run finished well: the step
// the user took was the last one the run needed. A failure or a new stop stays
// for the user to see.
func (a *applyStack) ackEnded(id string) {
	ctx := context.WithoutCancel(a.s.bg)
	e, err := a.d.notify.board.Get(ctx, id)
	if err != nil || e.State != notify.Done || e.Outcome != notify.OutcomeOK {
		return
	}
	if _, err := a.d.notify.board.Ack(ctx, id); err != nil {
		a.s.log.Warn("a continued run's entry could not be acknowledged", "run", id, "error", err.Error())
	}
}

// takeLane reserves the apply window for run, a resumed run or one an agent
// works; a window lent to another run is busy.
func (a *applyStack) takeLane(run string) *apikit.Error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.closed:
		return apikit.Unavailable("the server is stopping")
	case a.fills.any() || a.lentAway(run) || !a.busy.CompareAndSwap(false, true):
		return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", applyapi.ErrLaneBusy.Error()), wire.ReasonLaneBusy)
	}
	return nil
}

func notWaiting(e notify.Entry) *apikit.Error {
	switch {
	case e.State == notify.Working:
		return apikit.NewErr(wire.CodeConflict, "the run is already going on")
	case e.State == notify.NeedsYou && (e.Reason == "confirmation" || e.Reason == "may-have-submitted"):
		return apikit.NewErr(wire.CodeConflict, "this run waits for a confirmation or a check of whether it was submitted, which Done does not give")
	case e.State == notify.NeedsYou:
		return apikit.NewErr(wire.CodeConflict, "Done applies only to application runs you or the assistant started")
	}
	return apikit.NewErr(wire.CodeConflict, "the run is not waiting for you")
}

// lostTab is a Done on a run whose kept tab is gone.
func (a *applyStack) lostTab(ctx context.Context, id string) notify.Entry {
	e, err := a.d.notify.board.Move(ctx, id, notify.Blocked, notify.MoveArgs{Reason: "lane-unavailable", Detail: "The tab kept for this run was closed."})
	if err != nil {
		a.s.log.Warn("a run with no tab could not be blocked", "run", id, "error", err.Error())
	}
	return e
}

// resumeInput rebuilds the request the run was started from. Nothing a Done
// press says reaches it. A recorded Ask Agent request was a call in a turn the
// user typed, so the run is one the user started.
func resumeInput(req origins.ApplyRequest, org origins.Origin, id, by string) apply.ResumeInput {
	in := apply.Input{URL: req.URL, EmployerURL: req.EmployerURL, Submit: req.Submit, ExplicitRequest: req.Explicit, UserStarted: true, Resubmit: req.Resubmit,
		NamedRetry: req.Named, Caller: gates.Caller(req.Caller), Tool: req.Tool, Company: req.Company, Role: req.Role,
		ConversationID: org.SessionID, UserMessage: req.UserMessage, OwnWords: agent.OwnText(req.UserMessage)}
	out := apply.ResumeInput{RunID: id, Input: in}
	if req.Submit {
		out.HoldSubmit = heldBy[by]
	}
	return out
}

// heldBy is why a run that goes on after a press stops before its final
// Submit: the Ask Agent read the page's questions, which are outside content.
var heldBy = map[string]string{
	applyapi.DoneFromAgent: "the Ask Agent saved the last answer after reading the page, so Submit waits for your confirmation",
}

// startInput rebuilds the request of a run the user started; the run's own
// submit authorization decides from it again. A press by the Ask Agent holds
// the final Submit for the dashboard.
func startInput(q apply.StartRequest, by string) apply.ResumeInput {
	out := apply.ResumeInput{RunID: q.RunID, Input: q.Input()}
	if q.Submit {
		out.HoldSubmit = heldBy[by]
	}
	return out
}

// recheck is the Ask Agent's submit gate again, from the original request.
func (a *applyStack) recheck(req origins.ApplyRequest) func(context.Context) gates.Decision {
	if !req.Submit {
		return nil
	}
	return func(ctx context.Context) gates.Decision {
		return agent.SubmitAgain(ctx, req, a.s.apply.AgentApplyLanes())
	}
}

// ---- what the conversation is told

// completionNotes tells a run's conversation how it went. It is an observer of
// the runs board, beside the announcement policy: the conversation gets a
// message when a run it started reaches done, needs-you or blocked, and a
// Telegram conversation is told through the policy's announcement of the same
// entry, which keeps quiet hours, de-duplication and withdrawal. Each end is
// marked told on the run's origin once its line is stored, and a delegated
// task waits for an end until then (origins.ActiveIn). An end the queue lost (a
// stop, a full queue, an origin recorded after its run ended) is told by a pass
// over the stored ends: at start, then every tellEvery for the ends older than
// that.
type completionNotes struct {
	s *Server
	d *domain
	// q carries the lines to store, in the order the runs changed.
	q chan func()
}

// completionQueue bounds the lines waiting to be stored; a line past it is
// dropped with a warning, because the board must never wait on a conversation.
// A dropped end is told by a later pass.
const completionQueue = 256

// tellEvery is how often the worker passes over the ends nobody told, and how
// old an end must be for that pass, so the queue tells the newer ones in order.
const tellEvery = 10 * time.Second

func newCompletionNotes(s *Server, d *domain) completionNotes {
	return completionNotes{s: s, d: d, q: make(chan func(), completionQueue)}
}

// run tells the ends a stopped process left untold, then stores the lines one
// at a time until ctx ends.
func (c completionNotes) run(ctx context.Context) {
	c.tellUntold(ctx, c.s.now())
	tick := time.NewTicker(tellEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.tellUntold(ctx, c.s.now().Add(-tellEvery))
		case f := <-c.q:
			f()
		}
	}
}

func (c completionNotes) enqueue(f func()) {
	select {
	case c.q <- f:
	default:
		c.s.log.Warn("a run's completion message was dropped: too many are waiting")
	}
}

var _ notify.Observer = completionNotes{}

// Changed implements notify.Observer. A run that goes on again has its told
// end cleared at once, so its next end counts for its task even when it is
// the same as the last.
func (c completionNotes) Changed(ctx context.Context, before *notify.Entry, after notify.Entry) {
	switch after.State {
	case notify.Done, notify.NeedsYou, notify.Blocked:
	case notify.Working:
		if before != nil && before.State != notify.Working {
			if err := c.d.origins.Told(ctx, after.ID, ""); err != nil {
				c.s.log.Warn("a run going on again was not marked untold", "run", after.ID, "error", err.Error())
			}
		}
		return
	default:
		return
	}
	if before != nil && before.State == after.State && before.Reason == after.Reason && before.Outcome == after.Outcome {
		return
	}
	c.enqueue(func() {
		ctx := context.WithoutCancel(c.s.bg)
		org, err := c.d.origins.Get(ctx, after.ID)
		if err != nil {
			return
		}
		if c.tellEnd(ctx, org, after) {
			c.wakeTasks()
		}
	})
}

// tellUntold tells the ends that came at or before by and were not told, in
// the order they came.
func (c completionNotes) tellUntold(ctx context.Context, by time.Time) {
	ends, err := c.d.origins.Untold(ctx, by)
	if err != nil {
		c.s.log.Warn("the ends of the agent's runs could not be read", "error", err.Error())
		return
	}
	told := false
	for _, org := range ends {
		e, err := c.d.notify.board.Get(ctx, org.RunID)
		if err == nil && c.tellEnd(ctx, org, e) {
			told = true
		}
	}
	if told {
		c.wakeTasks()
	}
}

// tellEnd stores the line of one end of a run and marks it told, unless it was
// told already or the run goes on again; a line not stored is tried by a later
// pass.
func (c completionNotes) tellEnd(ctx context.Context, org origins.Origin, e notify.Entry) bool {
	switch e.State {
	case notify.Done, notify.NeedsYou, notify.Blocked:
	default:
		return false
	}
	end := origins.EndOf(e)
	if org.Told == end {
		return false
	}
	body, outside := completionText(e, c.d.narrator.outcome(ctx, e.Kind, e.ID))
	if c.note(org, body, outside) != nil {
		return false
	}
	if err := c.d.origins.Told(ctx, org.RunID, end); err != nil {
		c.s.log.Warn("a run's end was told but not marked told", "run", org.RunID, "error", err.Error())
	}
	return true
}

// told appends a line about a start the run cap queued and that did not begin.
func (c completionNotes) told(sessionID, body string) {
	c.enqueue(func() {
		_ = c.note(origins.Origin{SessionID: sessionID}, body, false)
		c.wakeTasks()
	})
}

// Retired implements notify.Observer.
func (completionNotes) Retired(context.Context, notify.Entry) {}

// note appends a system-made line to a run's conversation, naming the run, so
// the conversation can offer "Let the agent finish" under it. A conversation
// that was deleted meanwhile has nowhere to hold it.
func (c completionNotes) note(org origins.Origin, body string, outside bool) error {
	_, err := c.d.sessions.Append(context.WithoutCancel(c.s.bg), chatsessions.Message{SessionID: org.SessionID, Role: chatsessions.RoleNote,
		Body: agent.ScrubNote(vault.Redact(body), c.noteRoots()), OK: true, SystemMade: true, OutsideContent: outside, Envelope: chatsessions.Envelope{RunID: org.RunID}})
	if err != nil && !errors.Is(err, context.Canceled) {
		c.s.log.Warn("a run's completion message was not stored", "run", org.RunID, "error", err.Error())
	}
	return err
}

// wakeTasks lets a task waiting on its conversation's runs read the new lines
// in its next turn.
func (c completionNotes) wakeTasks() {
	if c.d.agent != nil {
		c.d.agent.WakeTasks()
	}
}

// noteRoots are the directories whose paths a note does not show: the state
// directory and the home directory.
func (c completionNotes) noteRoots() []string {
	home, _ := os.UserHomeDir()
	return []string{c.s.cfg.Workspace.StateDir(), home}
}

// resumed tells the conversation that the user reported finishing a step and what
// the page shows now. The page text makes it outside content.
func (c completionNotes) resumed(runID, by string, g apply.PageGlance) {
	org, err := c.d.origins.Get(context.WithoutCancel(c.s.bg), runID)
	if err != nil {
		return
	}
	body := fmt.Sprintf("You reported finishing the step for this run (Done in %s). I read the page again. %s I am continuing from there.", by, g.Summary())
	if by == applyapi.DoneFromAgent {
		body = fmt.Sprintf("The Ask Agent saved the last answer this run waited for. I read the page again. %s I am continuing from there; Submit waits for you.", g.Summary())
	}
	c.enqueue(func() {
		_ = c.note(org, body, true)
		c.wakeTasks()
	})
}

var needsYouWords = map[string]string{
	"sign-in": "a sign-in", "answer": "an answer", "challenge": "a challenge", "review": "a review", "one-time-code": "a verification code",
	"confirmation": "a confirmation", "may-have-submitted": "a check of whether it was submitted", "other": "something from you",
}

// completionText says how a run ended or why it waits, with the run's detail.
// A detail can carry text from a page, so it is outside content, unless it is
// the narrator's own sentence for the run (system, an evaluation's score or a
// scan's counts).
func completionText(e notify.Entry, system string) (body string, outside bool) {
	var b string
	switch e.State {
	case notify.Done:
		switch e.Outcome {
		case notify.OutcomeOK:
			b = fmt.Sprintf("The run %q finished.", e.Title)
		case notify.OutcomeFailed:
			b = fmt.Sprintf("The run %q failed.", e.Title)
		case notify.OutcomeCancelled:
			b = fmt.Sprintf("The run %q was cancelled.", e.Title)
		default:
			b = fmt.Sprintf("The run %q was interrupted before it finished.", e.Title)
		}
	case notify.NeedsYou:
		b = fmt.Sprintf("The run %q needs %s.", e.Title, needsYouWords[e.Reason])
		if notify.CanResume(e) {
			b += " Finish it in the kept browser tab, then press Done on the Runs page."
		}
	default:
		b = fmt.Sprintf("The run %q is stopped (%s).", e.Title, e.Reason)
	}
	if e.Detail != "" && e.Reason != "one-time-code" {
		b += " " + e.Detail
		outside = e.Detail != system
	}
	return b + " Details: " + e.Link, outside
}

// ---- the cap on runs the Ask Agent has going

// runGate keeps the Ask Agent to two runs at once. A start past the cap waits
// as a line on the runs board and a row of agent_queued_starts, which a stopped
// server leaves for the next start to tell.
func (s *Server) runGate(d *domain) *agent.RunGate {
	line := func(id string) queuedLine { return queuedLine{board: d.notify.board, origins: d.origins, id: id} }
	return &agent.RunGate{
		Life:   s.bg,
		Active: func(ctx context.Context) (int, error) { return d.origins.Active(ctx) },
		Tell:   func(_ context.Context, sessionID, body string) { d.completion.told(sessionID, body) },
		Hold: func(ctx context.Context, sessionID, label string) (agent.QueueNote, error) {
			ctx = context.WithoutCancel(ctx)
			title := "Queued: " + queuedLabel(label)
			e, err := d.notify.board.Open(ctx, notify.OpenArgs{Kind: notify.KindAgent, Origin: notify.OriginAgent, Title: title,
				Stage: "queued", Detail: "Queued: two runs the assistant started are still going. It starts when one finishes.", Link: "/runs"})
			if err != nil {
				return nil, err
			}
			if sessionID != "" {
				if err := d.origins.Queue(ctx, origins.QueuedStart{EntryID: e.ID, SessionID: sessionID, Label: label}); err != nil {
					_ = d.notify.board.Delete(ctx, e.ID)
					return nil, err
				}
			}
			return line(e.ID), nil
		},
		Left: func(ctx context.Context) ([]agent.LeftStart, error) {
			qs, err := d.origins.QueuedStarts(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]agent.LeftStart, 0, len(qs))
			for _, q := range qs {
				out = append(out, agent.LeftStart{SessionID: q.SessionID, Label: q.Label, Note: line(q.EntryID)})
			}
			return out, nil
		},
	}
}

// queuedLine is the board entry and the stored row of a start that waits for
// a free slot.
type queuedLine struct {
	board   *notify.Board
	origins *origins.Store
	id      string
}

func (q queuedLine) Touch(ctx context.Context) { _ = q.board.Touch(ctx, q.id) }

func (q queuedLine) Release(ctx context.Context) {
	_ = q.board.Delete(ctx, q.id)
	_ = q.origins.Unqueue(ctx, q.id)
}

func queuedLabel(label string) string {
	if label != "" {
		return label
	}
	return "a run"
}
