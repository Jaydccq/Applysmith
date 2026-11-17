package api

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/documents"
	"applysmith/internal/engine"
	"applysmith/internal/evaluate"
	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// startWait bounds how long a start waits for the run to name itself; the
// specification gives a run-starting tool 5 seconds to answer.
const startWait = 5 * time.Second

// applyStack is the application chain wired to this workspace. It starts runs
// for the Ask Agent, one at a time because they share the apply window, and
// keeps the runs board current while they work.
type applyStack struct {
	s      *Server
	d      *domain
	runner *apply.Runner
	// journal records the rows of every list.
	journal *apply.BatchJournal
	// salt hashes the user's message for the journal; the text is never kept.
	salt string

	busy windowHold
	// resumed makes the recorder watches of an earlier process start once, the
	// first time the apply window is reached.
	resumed sync.Once
	// fills are the fills of the window's active tab, which exclude a run.
	fills activeFills
	// mu guards closed, cancels and queue.
	mu      sync.Mutex
	closed  bool
	cancels map[string]context.CancelFunc
	// queue are the confirmed starts that wait for the apply window, oldest
	// first; touching is true while a worker keeps their entries alive.
	queue    []*queuedStart
	touching bool
	// lent is the run an Ask Agent turn works in its kept tab: the window stays
	// with it until lentUntil, so no start, queued start, other run or fill
	// takes the window between two calls of the turn (lendIdle).
	lent      string
	lentUntil time.Time
	wg        sync.WaitGroup
}

// lendIdle is how long the apply window stays with a run an agent works after
// its last call; apply_continue and apply_leave give it back at once.
const lendIdle = time.Minute

// lentAway reports whether the window is lent to a run other than run (empty
// for a new start). Call with mu held.
func (a *applyStack) lentAway(run string) bool {
	return a.lent != "" && a.lent != run && a.s.now().Before(a.lentUntil)
}

// lend keeps the window for run until lendIdle from now; a queued start takes
// it after that.
func (a *applyStack) lend(run string) {
	a.mu.Lock()
	a.lent, a.lentUntil = run, a.s.now().Add(lendIdle)
	a.mu.Unlock()
	time.AfterFunc(lendIdle, a.next)
}

// unlend gives the window back from run at once.
func (a *applyStack) unlend(run string) {
	a.mu.Lock()
	if a.lent == run {
		a.lent = ""
	}
	a.mu.Unlock()
	go a.next()
}

var _ agent.ApplyRunner = (*applyStack)(nil)

// newApplyStack builds the runner over the domain's services. The browser is
// reached through Config.ApplyWindow, which the launcher provides; without it
// every run stops at preflight and says so.
func (s *Server) newApplyStack(d *domain) (*applyStack, error) {
	st := s.store.Load()
	ws := s.cfg.Workspace
	salt, err := agent.AuditSalt(ws.StateDir())
	if err != nil {
		return nil, fmt.Errorf("read the audit salt: %w", err)
	}
	a := &applyStack{s: s, d: d, journal: apply.NewBatchJournal(st, s.now), salt: salt}
	a.busy.freed = func() { go a.next() }
	matcher, matches := d.bind, d.matches
	page := apply.PageDeps{
		Applicant: func(context.Context) (json.RawMessage, error) {
			ap, err := d.bank.Applicant(s.now())
			if err != nil {
				return nil, err
			}
			return json.Marshal(ApplicantSnapshot(ap))
		},
		Matcher: matcher, Matches: matches, Pin: matcher.Pinned,
		Facts: func(context.Context) (match.Facts, error) {
			p, ok := d.bank.Profile()
			if !ok {
				return match.Facts{}, errors.New("the profile cannot be read")
			}
			return bind.FactsFromProfile(p), nil
		},
		Documents: d.docs,
		// The engine's component drivers (ADP sdf.commit, react-widgets
		// dropdowns, Ashby's save queue) act through the main-world helper; the
		// apply host asks for it so they work outside the tests.
		MainWorld:     true,
		ProfileResume: a.profileResume,
		Transcript:    a.transcript,
		Settings: func() apply.Settings {
			v := d.bank.Settings(context.Background())
			return apply.Settings{AutoAcceptLegalDeclarations: v.AutoAcceptLegalDeclarations, AllowGoogleSignIn: v.AllowGoogleSignIn}
		},
		Poster:      learnPoster{d.learn, s.log},
		Tracker:     d.tracker,
		Credentials: d.creds,
		Sessions:    s.ApplySessions(),
		Policy:      d.accounts,
		Signals:     d.accounts,
		Model:       d.router,
		Own:         a.own,
		OwnSearch:   matcher.OwnSearch,
		Knowledge:   a.knowledge,
		ReportText:  a.reportText,
		Posting:     a.boardPosting,
		Revision:    a.revision,
		StageDir:    filepath.Join(ws.StateDir(), "apply", "uploads"),
		SnapshotDir: filepath.Join(ws.StateDir(), "apply", "snapshots"),
		Keep:        a.newKeeper(),
		Solver:      apply.SolverFactory(apply.SolverSource{Vault: s.cfg.Vault, Workspace: ws, Profile: d.bank.Profile, Log: s.log, Now: s.now}),
	}
	if d.pool != nil && d.docs != nil {
		page.Letters = applyLetters{s: s, d: d}
	}
	if l := d.gmail.Lookups; l != nil {
		page.Receipts, page.Mail, page.Codes = l, l, l
	}
	a.runner = apply.New(apply.Deps{
		Store: st, Ledger: d.ledger, Audit: apply.NewAudit(st, salt, s.now), Reports: apply.SQLReports{DB: st.Read()},
		Env: applyEnv{a}, Now: s.now, Build: s.cfg.BuildID, Salt: salt, Board: a.progress, Page: page, Mirror: s.mirrorSources(d),
		PostingClosed: func(ctx context.Context, postingURL string, l discovery.Liveness) {
			defer s.notify("scans")
			if err := (&discovery.Checker{Repo: d.repo, Tracker: d.tracker}).Record(ctx, postingURL, l, 0); err != nil {
				s.log.Warn("a closed posting could not be recorded", "error", err.Error())
			}
		},
	})
	return a, nil
}

// boardPosting reads a posting from its public board for a run whose
// application page does not show it.
func (a *applyStack) boardPosting(ctx context.Context, postingURL string) (apply.PostingRead, error) {
	p, err := a.s.readBoardPosting(ctx, postingURL)
	if err != nil {
		return apply.PostingRead{}, err
	}
	return apply.PostingRead{Title: p.Title, Location: p.Location, Text: p.Text}, nil
}

// close stops accepting runs and waits for the running ones, which end because
// the server's background context is already cancelled.
func (a *applyStack) close() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.wg.Wait()
}

// driving reports whether an application run holds the apply window, or an
// agent's turn has it lent.
func (a *applyStack) driving() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy.Load() || a.lentAway("")
}

// reserveFill takes the fill of tab, refusing while a run holds the window or
// a fill of that tab is already working.
func (a *applyStack) reserveFill(tab string) *apikit.Error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.closed:
		return apikit.Unavailable("the server is stopping")
	case a.busy.Load() || a.lentAway(""):
		return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "an application run is driving the apply window; wait for it to finish"), wire.ReasonLaneBusy)
	case !a.fills.reserve(tab):
		return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "a fill of this tab is already working"), wire.ReasonAlreadyRunning)
	}
	return nil
}

// endFill lets go of a fill: the tab stays open for the user, and a queued
// start may take the window.
func (a *applyStack) endFill(tab string, f *apply.Fill) {
	f.Close(context.WithoutCancel(a.s.bg))
	a.fills.release(tab)
	go a.next()
}

// resume continues the batches a dead process left open, from their next
// pending row and never past their original deadline. A row that was started
// is read as possibly submitted and is not run again. No user is present after
// a restart to confirm a submit, so a submitting batch goes on filling only and
// each row it filled waits on the board for a fresh confirmation. Batches run
// one after another in the window, and a window a user run holds ends the
// resume: the rows stay pending until the next start.
func (a *applyStack) resume() {
	ctx := a.s.bg
	open, err := a.journal.Recover(ctx)
	if err != nil {
		a.s.log.Warn("the apply journal could not be read", "error", err.Error())
		return
	}
	if len(open) == 0 {
		return
	}
	a.mu.Lock()
	if a.closed || a.fills.any() || !a.busy.CompareAndSwap(false, true) {
		a.mu.Unlock()
		return
	}
	a.wg.Go(func() {
		defer a.busy.Store(false)
		for _, b := range open {
			a.resumeBatch(ctx, b)
		}
	})
	a.mu.Unlock()
}

func (a *applyStack) resumeBatch(ctx context.Context, b apply.OpenBatch) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	// No user started the resumed rows: they only fill and create no account.
	opt := apply.ListOptions{BatchID: b.ID, Caller: b.Meta.Caller, Tool: b.Meta.Tool,
		ConversationID: b.Meta.ConversationID, UserMessageHash: b.Meta.MessageHash, Deadline: b.Meta.Deadline, HasSubmission: a.hasSubmission, Journal: a.journal.For(b.ID), JournalIndex: b.Index}
	first := make(chan startResult, 1)
	a.drive(ctx, stop, b.Rows, opt, nil, first, b.Meta.Submit)
	if err := a.journal.Close(context.WithoutCancel(ctx), b.ID); err != nil {
		a.s.log.Warn("an apply batch could not be closed", "batch", b.ID, "error", err.Error())
	}
}

// batchBudget is the deadline of a list: 10 minutes per row, at most 8 hours.
func batchBudget(rows int) time.Duration {
	return min(time.Duration(rows)*10*time.Minute, 8*time.Hour)
}

// startResult is what a start tells its caller once the first run has an id,
// or why no run could start.
type startResult struct {
	id  string
	err error
}

// StartApply begins the requested rows of the Ask Agent in the background. A
// list tool's rows are journaled as a batch; one posting is a list of one row
// that is not. A start while the apply window is held or starts wait for it is
// refused: the agent's tools answer at once. A conversation's call to submit
// the posting of its own run whose filled form waits at Submit in its kept tab
// settles that form's hold, as "Submit this form" does (heldForm).
func (a *applyStack) StartApply(ctx context.Context, req agent.ApplyRequest) (string, error) {
	if len(req.Rows) == 0 {
		return "", errors.New("no posting to apply to")
	}
	if id := a.heldForm(ctx, req); id != "" {
		_, err := a.submitKept(ctx, id, req.Caller)
		return id, err
	}
	if req.Tool == gates.ToolApplyFromList && !req.Selected {
		sel, err := a.selectRows(ctx, req)
		if err != nil {
			return "", err
		}
		req.Rows = sel
	}
	l, err := a.launch(ctx, req, false)
	return l.id, err
}

// selectRows lets the list selection leave out what a list from the Ask Agent
// must not run: a duplicate, a stop that still holds. The tool already applied
// the skip list.
func (a *applyStack) selectRows(ctx context.Context, req agent.ApplyRequest) ([]agent.ApplyRow, error) {
	skip, err := a.s.batchSkipEntries(ctx)
	if err != nil {
		return nil, err
	}
	cands := make([]apply.Candidate, len(req.Rows))
	for i, r := range req.Rows {
		cands[i] = apply.Candidate{URL: r.URL, Company: r.Company, Role: r.Role}
	}
	res, err := a.s.selector().Select(ctx, cands, apply.SelectOptions{Submit: req.Submit, Now: a.s.now(), Build: a.s.cfg.BuildID, SkipList: skip, Limit: len(cands)})
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		why := "nothing on the list can run"
		if len(res.Skipped) > 0 {
			why += ": " + res.Skipped[0].Company + " - " + res.Skipped[0].Reason
		}
		return nil, errors.New(why)
	}
	rows := make([]agent.ApplyRow, len(res.Rows))
	for i, r := range res.Rows {
		rows[i] = agent.ApplyRow{URL: r.URL, EmployerURL: r.EmployerURL, Company: r.Company, Role: r.Role, Named: r.Named, Mirrors: r.Mirrors}
	}
	return rows, nil
}

// runList journals a batch, runs its rows and closes it. The first run to
// exist names the start; firstID, when set, is the id it takes. Every start
// that reaches it is the user's own: the dashboard session, a typed command,
// or an Ask Agent call in a dashboard turn the user typed.
func (a *applyStack) runList(ctx context.Context, req agent.ApplyRequest, rows []apply.ListRow, batch string, first chan<- startResult, firstID string) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	opt := apply.ListOptions{BatchID: batch, Submit: req.Submit, ExplicitRequest: req.ExplicitRequest, UserStarted: true, Caller: req.Caller, Tool: req.Tool,
		ConversationID: req.ConversationID, UserMessage: req.UserMessage, OwnWords: req.OwnWords, FirstRunID: firstID}
	if batch != "" {
		opt.Deadline = a.s.now().Add(batchBudget(len(rows)))
		opt.HasSubmission = a.hasSubmission
		meta := apply.BatchMeta{Caller: req.Caller, Tool: req.Tool, Submit: req.Submit, Explicit: req.ExplicitRequest, Deadline: opt.Deadline, ConversationID: req.ConversationID}
		if req.UserMessage != "" {
			meta.MessageHash = gates.MessageHash(a.salt, req.UserMessage)
		}
		if err := a.journal.Create(ctx, batch, meta, rows); err != nil {
			first <- startResult{err: fmt.Errorf("the list could not be journaled: %w", err)}
			return
		}
		opt.Journal = a.journal.For(batch)
		defer func() {
			if err := a.journal.Close(context.WithoutCancel(ctx), batch); err != nil {
				a.s.log.Warn("an apply batch could not be closed", "batch", batch, "error", err.Error())
			}
		}()
	}
	a.drive(ctx, stop, rows, opt, &req, first, false)
}

// drive runs rows through the list driver and follows each run on the board.
// Each run records the request it came from: a list the Ask Agent started with
// its conversation, any other with the user's own start. reconfirm marks a
// submitting batch that resumed after a restart and now only fills.
func (a *applyStack) drive(ctx context.Context, stop context.CancelFunc, rows []apply.ListRow, opt apply.ListOptions, req *agent.ApplyRequest, first chan<- startResult, reconfirm bool) {
	var once sync.Once
	opt.Started = func(i int, id string) {
		a.track(id, stop)
		origin := notify.OriginUser
		if req != nil && req.ConversationID != "" {
			origin = notify.OriginAgent
			a.recordOrigin(ctx, id, rows[i], *req)
		} else {
			a.recordStart(ctx, id, rows[i], opt)
		}
		a.open(ctx, id, rows[i], opt.BatchID, origin)
		once.Do(func() { first <- startResult{id: id} })
		a.retireEarlier(ctx, id, rows[i].URL)
	}
	opt.Landed = func(r apply.ListResult) {
		if r.Run != nil {
			run := *r.Run
			run.Outcome = heldAfterRestart(run.Outcome, reconfirm)
			a.finish(&run)
			a.untrack(r.Run.ID)
		}
	}
	res := a.runner.RunList(ctx, rows, opt)
	once.Do(func() { first <- startResult{err: errors.New(noRunReason(res))} })
}

// restartHold is the board detail of a row that a submitting batch filled
// after a restart.
const restartHold = "the list resumed after a restart with no one present, so this row was only filled; confirm it again from the dashboard to submit"

// heldAfterRestart puts a row that a resumed submitting batch filled on the
// board as waiting for the user's confirmation; any other outcome keeps its own
// place.
func heldAfterRestart(o apply.Outcome, reconfirm bool) apply.Outcome {
	if reconfirm && o.Kind == apply.OutcomeFilled {
		o.Board, o.Reason = apply.HoldConfirmation, restartHold
	}
	return o
}

// noRunReason says why a list started no run.
func noRunReason(res []apply.ListResult) string {
	for _, r := range res {
		if r.Note != "" {
			return "no application could start: " + r.Note
		}
	}
	return "no application could start"
}

// hasSubmission reports whether the ledger holds a submission for the posting;
// a ledger that cannot be read counts as holding one.
func (a *applyStack) hasSubmission(ctx context.Context, row apply.ListRow) (bool, error) {
	key, stp := apply.IdentityKey(row.URL)
	if stp != nil {
		return true, stp
	}
	var n int
	err := a.s.store.Load().Read().QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE identity_key = ? OR url = ?`, key, row.URL).Scan(&n)
	return n > 0, err
}

// open puts the run on the board before its first stage. The entry of a
// queued start becomes its first run's.
func (a *applyStack) open(ctx context.Context, id string, row apply.ListRow, batch string, origin notify.Origin) {
	title, stage := applyTitle(a.named(ctx, row)), string(apply.StagePreflight)
	e, err := a.d.notify.board.Open(context.WithoutCancel(ctx), notify.OpenArgs{ID: id, Kind: notify.KindApply, Origin: origin, Title: title,
		Stage: stage, BatchID: batch, Link: "/runs/" + id, Ref: "apply:" + id})
	if err == nil && e.Stage == wire.BoardStageQueued {
		detail := ""
		_, err = a.d.notify.board.Update(context.WithoutCancel(ctx), id, notify.Patch{Title: &title, Stage: &stage, Detail: &detail})
	}
	if err != nil {
		a.s.log.Warn("an application run could not be put on the board", "run", id, "error", err.Error())
	}
}

// named fills the company and role a row leaves blank, or holds a placeholder
// in (a bound report carries one), with what the workspace knows of its
// posting (notify.Board.Named). An address that cannot be keyed adds nothing.
func (a *applyStack) named(ctx context.Context, row apply.ListRow) apply.ListRow {
	key, _ := identity.URLKey(row.URL)
	n := a.d.notify.board.Named(ctx, key, notify.Names{Company: row.Company, Role: row.Role})
	row.Company, row.Role = n.Company, n.Role
	return row
}

// recordedRow is a run's posting with the names its record keeps: the bound
// report's, else those it was started with.
func recordedRow(run *apply.Run) apply.ListRow {
	row := apply.ListRow{URL: run.Input.URL, Company: run.Input.Company, Role: run.Input.Role}
	if b := run.Report; b != nil {
		row.Company, row.Role = cmp.Or(b.Company, row.Company), cmp.Or(b.Role, row.Role)
	}
	return row
}

// applyTitle names a run by the company and, when known, the role. A row
// without a company takes the board name from its ATS address, and only then
// the host, so runs on the same ATS do not read alike.
func applyTitle(row apply.ListRow) string {
	company := cmp.Or(strings.TrimSpace(row.Company), notify.PostingSlug(row.URL))
	if company == "" {
		return "Apply to a posting"
	}
	return notify.ApplyTitle(notify.Names{Company: company, Role: strings.TrimSpace(row.Role)})
}

// progress follows the run's stages on the board. The last update is left to
// finish, which knows the outcome.
func (a *applyStack) progress(u apply.BoardUpdate) {
	if u.Done {
		return
	}
	ctx := context.WithoutCancel(a.s.bg)
	var err error
	if u.Detail == "working" {
		err = a.d.notify.board.Touch(ctx, u.RunID)
	} else {
		stage, detail := string(u.Stage), u.Detail
		_, err = a.d.notify.board.Update(ctx, u.RunID, notify.Patch{Stage: &stage, Detail: &detail})
	}
	if err != nil {
		a.s.log.Warn("an application run could not be followed on the board", "run", u.RunID, "error", err.Error())
	}
}

// finish moves the entry to where the outcome puts it: waiting for the user,
// stopped by a missing precondition, or done. Its stage becomes the one the
// run stopped at (none for a run that filled or submitted), not the record
// and learn stages that run after every outcome.
func (a *applyStack) finish(run *apply.Run) {
	to, mv := boardEnd(run.Outcome, a.s.cfg.Workspace.IsDemo())
	if s := submittedSentence(run); s != "" {
		mv.Detail = s
	}
	mv.FocusTab = a.focusTab(run.ID)
	ctx := context.WithoutCancel(a.s.bg)
	// The title is written before the move announces the end: the run may have
	// learned its posting's names after its entry opened, from the report it
	// bound, a tracker row made meanwhile or the page it opened. What the page
	// states ranks last and fills only what the request, the report and the
	// workspace left blank. An end that learned no company keeps the title the
	// entry has: a run that went on after Done did not open the page again.
	stage := string(run.Outcome.Stage)
	row := a.named(ctx, recordedRow(run))
	row.Company, row.Role = cmp.Or(row.Company, run.Stated.Company), cmp.Or(row.Role, run.Stated.Role)
	patch := notify.Patch{Stage: &stage}
	if strings.TrimSpace(row.Company) != "" {
		title := applyTitle(row)
		patch.Title = &title
	}
	if _, err := a.d.notify.board.Update(ctx, run.ID, patch); err != nil {
		a.s.log.Warn("an application run's stop stage could not be set on the board", "run", run.ID, "error", err.Error())
	}
	if _, err := a.d.notify.board.Move(ctx, run.ID, to, mv); err != nil {
		a.s.log.Warn("an application run could not be finished on the board", "run", run.ID, "error", err.Error())
	}
}

// boardEnd maps a run's outcome to a board state. A stop the chain labelled
// for the user waits in the inbox; a stop with no label is a failed run. A
// filled run of the demo workspace waits for no Submit: none is ever pressed
// there, and the run's page offers none. Neither does any other filled run
// that is not held for confirmation: a dry run keeps its tab only when the
// user started it for one posting from the dashboard or the CLI (held), so an
// Ask Agent's or a list row's dry run has closed its tab (live, the agent told
// a user a closed Nuro form was "waiting for your Submit").
func boardEnd(o apply.Outcome, demo bool) (notify.State, notify.MoveArgs) {
	detail := o.Reason
	switch o.Kind {
	case apply.OutcomeSubmitted:
		if !o.Confirmed {
			detail = "Pressed Submit; the confirmation is not read yet."
		}
		return notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK, Detail: detail}
	case apply.OutcomeFilled:
		if o.Board == apply.HoldConfirmation {
			return notify.NeedsYou, notify.MoveArgs{Reason: "confirmation", Detail: "filled; Submit waits for your confirmation in the dashboard. " + o.Reason}
		}
		if demo {
			return notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK, Detail: "Filled every page and stopped before Submit: a demo workspace never submits."}
		}
		return notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK, Detail: "Filled every page and stopped before Submit, as asked; the tab is closed, so nothing was sent and nothing waits for you. Ask to apply and submit to send it."}
	}
	switch r := o.Board; r {
	case "sign-in", "answer", "challenge", "review", "one-time-code", "may-have-submitted":
		return notify.NeedsYou, notify.MoveArgs{Reason: r, Detail: detail}
	case "needs-user":
		return notify.NeedsYou, notify.MoveArgs{Reason: "other", Detail: detail}
	case "credential-refused", "lockout", "lane-unavailable":
		return notify.Blocked, notify.MoveArgs{Reason: r, Detail: detail}
	}
	if strings.HasPrefix(o.Reason, "cancelled") {
		return notify.Done, notify.MoveArgs{Outcome: notify.OutcomeCancelled, Detail: detail}
	}
	return notify.Done, notify.MoveArgs{Outcome: notify.OutcomeFailed, Detail: detail}
}

// applyEnv is the machine as the preflight asks about it. The server answering
// is the health check; the answer bank and profile are read in process.
type applyEnv struct{ a *applyStack }

func (e applyEnv) Health(context.Context) (int, error) { return 200, nil }

func (e applyEnv) AnswerBank(context.Context) (string, int, error) {
	l, err := e.a.d.bank.Bank()
	return workspace.FileAnswerBank.Name(), len(l.Bank.Entries), err
}

func (e applyEnv) Profile(ctx context.Context) (apply.ProfileCheck, error) {
	ap, err := e.a.d.bank.Applicant(e.a.s.now())
	if err != nil {
		return apply.ProfileCheck{}, err
	}
	var pc apply.ProfileCheck
	for _, f := range []struct{ name, value string }{{"full name", ap.FullName}, {"email", ap.Email}, {"phone", ap.Phone}} {
		if strings.TrimSpace(f.value) == "" {
			pc.Missing = append(pc.Missing, f.name)
		}
	}
	if _, _, err := e.a.profileResume(ctx, nil); err != nil {
		pc.ResumeErr = err.Error()
	}
	return pc, nil
}

func (e applyEnv) Attach(ctx context.Context) (apply.Window, error) {
	if e.a.s.cfg.ApplyWindow == nil {
		return nil, errors.New("the application browser is not available in this build")
	}
	win, err := e.a.s.cfg.ApplyWindow(ctx)
	if err == nil {
		e.a.resumeWatches(ctx, win)
	}
	return win, err
}

// profileResume is the roster résumé a run uploads when the candidacy has no
// verified tailored one: the entry that best matches the bound report
// (documents.RankReport: the role's title and the posting's own words, no
// model), the default when nothing matches or no report is bound. Why names
// the entry and the reason in a few words.
func (a *applyStack) profileResume(ctx context.Context, rep *apply.ReportBinding) (*engine.ResumePayload, string, error) {
	roster, err := a.s.cfg.Workspace.Roster()
	if err != nil {
		return nil, "", err
	}
	plain := make([]workspace.Resume, len(roster))
	for i, r := range roster {
		plain[i] = r.Resume
	}
	ranked := documents.RankResumes(plain, documents.RankPosting{})
	if rep != nil {
		md, _ := a.reportText(ctx, rep.Path)
		ranked = documents.RankReport(plain, rep.Role, rep.Company, md)
	}
	if ranked.Chosen == "" {
		return nil, "", errors.New("the profile names no default résumé")
	}
	e, data, err := a.s.cfg.Workspace.ReadResume(ranked.Chosen)
	if err != nil {
		return nil, "", err
	}
	pl := &engine.ResumePayload{FileName: e.FileName, MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(data)}
	return pl, resumeWhy(e, ranked.Ranked[0], len(roster), rep), nil
}

// transcript is the academic transcript stored in the Answer Bank; nil when
// none is stored.
func (a *applyStack) transcript(context.Context) (*engine.ResumePayload, error) {
	name, data, err := a.s.cfg.Workspace.ReadTranscript()
	if err != nil || name == "" {
		return nil, err
	}
	return &engine.ResumePayload{FileName: name, MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(data)}, nil
}

// resumeWhy names the roster entry a run uploads and why.
func resumeWhy(e workspace.RosterEntry, top documents.RankedResume, entries int, rep *apply.ReportBinding) string {
	name := e.Label
	if name == "" {
		name = e.ID
	}
	switch {
	case entries == 1:
		return fmt.Sprintf("%q, the only one on the roster", name)
	case rep == nil:
		return fmt.Sprintf("%q, the default: the run has no evaluated report to match", name)
	case top.Reason == documents.ReasonDefault:
		return fmt.Sprintf("%q, the default: no entry matches report #%03d", name, rep.Number)
	}
	terms := strings.Split(top.Reason, ", ")
	return fmt.Sprintf("%q, the best match for report #%03d (%s)", name, rep.Number, strings.Join(terms[:min(len(terms), 5)], ", "))
}

// reportText reads the evaluated report a run is bound to. The path comes from
// the report index and must stay inside the workspace.
func (a *applyStack) reportText(_ context.Context, path string) (string, error) {
	root := a.s.cfg.Workspace.Root
	full := filepath.Clean(filepath.Join(root, path))
	if rel, err := filepath.Rel(root, full); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("the report path is outside the workspace")
	}
	b, err := os.ReadFile(full)
	return string(b), err
}

// revision changes when the answer bank, profile or CV is saved.
func (a *applyStack) revision(context.Context) string {
	ws := a.s.cfg.Workspace
	var parts []string
	for _, f := range []workspace.File{workspace.FileAnswerBank, workspace.FileProfile, workspace.FileCV} {
		if c, err := ws.Read(f); err == nil {
			parts = append(parts, c.Revision)
		}
	}
	return strings.Join(parts, "|")
}

// learnPoster hands the chain's learning events to the answer-bank learning
// service, each carrying the run that produced it. An event the service
// rejects is counted in the log by reason, never by content.
type learnPoster struct {
	svc *learn.Service
	log *slog.Logger
}

func (p learnPoster) PostEvents(ctx context.Context, events []apply.LearnEvent) error {
	ups := make([]learn.Upload, len(events))
	for i, ev := range events {
		ups[i] = learn.Upload{EventID: ev.EventID, Kind: ev.Kind, Host: ev.Host, Question: ev.Question, SectionPath: ev.Section, ControlKind: ev.ControlKind, InputType: ev.InputType,
			Autocomplete: ev.Autocomplete, Options: ev.Options, At: time.UnixMilli(ev.Time), Reason: ev.Reason, Before: ev.Before, After: ev.After,
			OptionLabel: ev.OptionLabel, EntryID: ev.EntryID, ReportNumber: ev.ReportNumber, Selector: ev.Selector, TrustedInput: ev.TrustedInput, RunID: ev.RunID}
	}
	res, err := p.svc.Ingest(ctx, ups)
	if errors.Is(err, learn.ErrBatchSize) {
		return apply.ErrTooLarge
	}
	if err != nil {
		return err
	}
	if len(res.Rejected) > 0 && p.log != nil {
		reasons := map[string]int{}
		for _, r := range res.Rejected {
			reasons[r.Reason]++
		}
		p.log.Warn("learning events from an application run were rejected", "rejected", len(res.Rejected), "reasons", reasons)
	}
	p.svc.AfterIngest()
	return nil
}

// applyPrincipal claims the runs an application run starts for itself.
const applyPrincipal = "apply-chain"

// applyLetters is the apply chain's way to a cover letter: a full evaluation
// of the posting in the evaluation pool, then the candidacy's letter with its
// PDF in the document service. Both show on the runs board like any other run.
type applyLetters struct {
	s *Server
	d *domain
}

var _ apply.Letters = applyLetters{}

func (l applyLetters) Evaluate(ctx context.Context, postingURL string) (*apply.ReportBinding, error) {
	h, err := l.d.pool.SubmitKeyed(ctx, evaluate.Claim{Principal: applyPrincipal, Key: "ap-" + randomKey()},
		evaluate.Request{URL: postingURL, Mode: evaluate.ModeDeep, Priority: evaluate.PriorityUser, Applying: true})
	if err != nil {
		return nil, err
	}
	l.s.notify("runs")
	res, err := l.d.pool.Wait(ctx, h.RunID)
	if err != nil {
		return nil, err
	}
	if res.Outcome == evaluate.OutcomeFailed {
		return nil, fmt.Errorf("%s", firstOf(res.Explanation, res.Reason, "unknown error"))
	}
	if res.ReportNumber == 0 {
		return nil, nil
	}
	return apply.SQLReports{DB: l.s.store.Load().Read()}.ByNumber(ctx, res.ReportNumber)
}

func (l applyLetters) Write(ctx context.Context, candidacy int64) error {
	h, err := l.d.docs.Submit(ctx, documents.Claim{Principal: applyPrincipal, Key: "ap-" + randomKey()},
		documents.Request{Action: documents.ActionCover, Mode: documents.ModeTailored, CandidacyID: candidacy, PDF: true, Asked: true, Actor: tracker.ActorAgent})
	if err != nil {
		return err
	}
	l.s.notify("runs")
	res, err := l.d.docs.Wait(ctx, h.RunID)
	if err != nil {
		return err
	}
	if !res.Published {
		return errors.New(firstOf(res.Reason, "the letter was not published"))
	}
	return nil
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// atsNames are the applicant systems the chain names, as the user knows them.
var atsNames = map[string]string{apply.ATSGreenhouse: "Greenhouse", apply.ATSLever: "Lever", apply.ATSAshby: "Ashby", apply.ATSWorkday: "Workday", apply.ATSLinkedIn: "LinkedIn"}

// confirmWords say which evidence confirmed a submission.
var confirmWords = map[string]string{apply.ConfirmPage: "the page", apply.ConfirmATSResponse: "the application system's reply", apply.ConfirmReceipt: "the confirmation email"}

// submittedSentence is the outcome of a run whose Submit was confirmed: where it
// went, what confirmed it and the tracker row it set. "" for any other outcome.
func submittedSentence(run *apply.Run) string {
	o := run.Outcome
	if o.Kind != apply.OutcomeSubmitted || !o.Confirmed {
		return ""
	}
	company := run.Input.Company
	if run.Report != nil && run.Report.Company != "" {
		company = run.Report.Company
	}
	host := ""
	if u, err := url.Parse(run.Input.URL); err == nil {
		host = strings.TrimPrefix(u.Hostname(), "www.")
	}
	system, ok := atsNames[run.ATS]
	if !ok {
		system = host
	}
	where := strings.TrimSpace(company)
	switch {
	case where == "":
		where = cmp.Or(system, "the posting")
	case system != "":
		where += " (" + system + ")"
	}
	s := "Submitted to " + where + ", confirmed by " + cmp.Or(confirmWords[o.ConfirmSource], "the page")
	if run.TrackerRow > 0 {
		s += fmt.Sprintf("; tracker row %d is Applied (agent)", run.TrackerRow)
	}
	return s + "."
}
