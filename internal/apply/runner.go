package apply

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// BoardUpdate is one runs-board entry update: the stage the run is in and,
// when it stopped on something the user can act on, the board reason.
type BoardUpdate struct {
	RunID  string
	Stage  Stage
	Detail string
	// Reason is the board vocabulary for a stop that needs the user or is
	// blocked ("sign-in", "answer", "challenge", "review", "one-time-code",
	// "may-have-submitted", "credential-refused", "lockout", "lane-unavailable").
	Reason string
	Done   bool
}

// Hooks are test seams for failures that cannot be caused from outside: a
// process that dies, a response that is lost. Production leaves them nil.
type Hooks struct {
	// AfterClaim runs once the press claim is held and before the press.
	AfterClaim func()
	// AfterPress runs once the press input was dispatched and before the
	// verdict is read.
	AfterPress func()
}

// Deps is everything a runner needs. Fields a stage does not need may be nil
// until a later layer's stage runs.
type Deps struct {
	Store   *store.Store
	Ledger  *ledger.Ledger
	Audit   *Audit
	Reports Reports
	Env     Environment
	Now     func() time.Time
	Build   string
	Salt    string
	Board   func(BoardUpdate)
	Hooks   Hooks
	// Preflight timing; zero takes the specified clocks.
	Timing PreflightTiming
	// PingEvery is how often the runs board hears from a run that is working
	// (a long page reports progress pings); zero is 30 seconds.
	PingEvery time.Duration
	// Drive runs the browser stages (account, fill, check, fix, submit,
	// confirm) for a run whose resolve and duplicate guard passed. It is set by
	// New to the page driver.
	Drive func(ctx context.Context, e *Exec) *stop

	// Mirror resolves a job-board mirror to the employer's posting; nil
	// resolves nothing, so an unresolved mirror stops as aggregator-only.
	Mirror *MirrorSources

	// PostingClosed is told a posting the run found closed (HTTP 404 or 410,
	// a redirect to the site's error page or careers home) as the posting's
	// liveness result, so its inbox rows expire and are not offered again;
	// nil tells no one.
	PostingClosed func(ctx context.Context, postingURL string, l discovery.Liveness)

	// Browser-stage collaborators, used by the page driver.
	Page PageDeps
}

// Runner drives every application through the one staged runner.
type Runner struct {
	d Deps

	mu     sync.Mutex
	lastMs int64
	live   atomic.Int32
	// work is what the Ask Agent's apply tools remember of each run's page,
	// by run id (operate.go).
	work sync.Map
}

// demo reports whether the runner's workspace carries the demo marker. It is
// read from the database's state directory, so no caller can leave it out.
func (r *Runner) demo() bool { return r.d.Store.Demo() }

// Live reports whether a run is driving the apply window. The session-import
// routes refuse the user's bulk sync while it is true (the run's own request
// for its host is exempt); wire it as api.Config.ApplyRunLive.
func (r *Runner) Live() bool { return r.live.Load() > 0 }

// MainWorld reports whether fills install the engine's main-world helper.
func (r *Runner) MainWorld() bool { return r.d.Page.MainWorld }

// New returns a runner.
func New(d Deps) *Runner {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Page.Declines == nil {
		d.Page.Declines = NewDeclines(d.Store, d.Now)
	}
	r := &Runner{d: d}
	if r.d.Drive == nil {
		r.d.Drive = r.drivePages
	}
	return r
}

// Preflight takes the environment verdict of one invocation. A list takes it
// once and passes it to every row through Input.Preflight.
func (r *Runner) Preflight(ctx context.Context) *Verdict {
	return Preflight(ctx, r.d.Env, r.d.Timing)
}

// NewRunID returns an id that is unique in this process and whose time part
// is strictly increasing, so two runs started in one millisecond differ.
func (r *Runner) NewRunID() string {
	r.mu.Lock()
	ms := r.d.Now().UnixMilli()
	if ms <= r.lastMs {
		ms = r.lastMs + 1
	}
	r.lastMs = ms
	r.mu.Unlock()
	var salt [3]byte
	_, _ = rand.Read(salt[:])
	return fmt.Sprintf("run_%s_%s", time.UnixMilli(ms).UTC().Format(runIDTimePart), hex.EncodeToString(salt[:]))
}

// Exec is the working state of one run; stages read and extend it.
type Exec struct {
	r    *Runner
	Run  *Run
	Ctx  context.Context
	Cls  Classification
	Ver  *Verdict
	sess *session
	// sessionAsked marks the hosts a session import was already requested
	// for in this run; it asks once per host.
	sessionAsked map[string]bool
	// cur is the stage the run last recorded, for the board's progress pings.
	cur    atomic.Value
	solver captcha.Solver
	// demoRefusal is the reason a demo workspace refused a solve, which the
	// bot check it leads to names.
	demoRefusal string
	// spent is the solves an earlier leg of a resumed run made.
	spent int
	codes *codeLane
	// deliverySent is set once the delivery-choice Send was pressed (one walk,
	// one Send); codeSentAt is that press; codeRequests counts the Send and
	// Resend presses per host for the code-request gate.
	deliverySent bool
	codeSentAt   time.Time
	codeRequests map[string]int
	// codeSeenAt is when the walk first saw the code dialog it is working on
	// (a demo bounds its mail lookup by it); codeResent counts the Resend
	// presses of the walk.
	codeSeenAt time.Time
	codeResent int
	// walkPressedAt is the walk's latest press of a page control: a code
	// dialog it brings mails its code after it.
	walkPressedAt time.Time
	// budget counts the model calls of the ladder and the page decisions.
	budget callBudget
	// signalled marks the detection signals already recorded for the run.
	signalled map[string]bool
	// combined is the hand-over of a create-account form that is also the
	// application to the walk; resends counts the verification-email presses.
	combined *combinedAccount
	resends  int
	// resets counts the password resets started per host in this run.
	resets map[string]int
	// gate is the walker's tab at a login gate, tagged for the account stage.
	gate *gateTab
	// reentries counts the account stage's re-entries at login gates.
	reentries int
	// hops counts the moves to another page of the application and visited
	// holds the addresses they opened, so a run cannot go round in a circle.
	hops    int
	visited map[string]bool
	// gateSeen holds the addresses the account stage probed.
	gateSeen map[string]bool
	// resume is set for a run that continues one the user finished a step of.
	// reread is how many page reports its earlier legs left, until this leg
	// reports a page; solved is the captcha lines they left.
	resume *ResumeInput
	reread int
	solved []string
	// held is why a run that would submit stopped before the press, to be
	// confirmed by the user.
	held string
	// noModel makes every model call of the chain read as unavailable: the
	// Ask Agent's apply tools answer in the place of the ladder's model rungs,
	// the fix planner and the review, which an operation asks for by name.
	noModel bool
}

// unattended reports whether nobody watches the run: the scheduler started it.
// The press gate refuses every control of such a run.
func (e *Exec) unattended() bool { return e.Run.Input.Caller == gates.CallerScheduler }

// Run executes one run to its verdict. It never panics a caller: every stop
// is an outcome, and record and learn always run.
func (r *Runner) RunOne(ctx context.Context, in Input) *Run {
	if in.Tool == "" {
		in.Tool = defaultTool
	}
	r.live.Add(1)
	defer r.live.Add(-1)
	id := in.RunID
	if id == "" {
		id = r.NewRunID()
	}
	run := &Run{ID: id, Input: in, StartedAt: r.d.Now(), Build: r.d.Build, Outcome: Outcome{Kind: outcomeRunning}}
	run.MessageHash = in.UserMessageHash
	if in.UserMessage != "" {
		run.MessageHash = gates.MessageHash(r.d.Salt, in.UserMessage)
	}
	if key, stp := IdentityKey(in.URL); stp == nil {
		// Known from the start, so a run that stops before resolve is still the
		// newest attempt for its posting.
		run.Identity = key
	}
	e := &Exec{r: r, Run: run, Ctx: ctx}
	// The record comes first: Started hands the id to a caller who reads the
	// run by it at once.
	if err := saveRun(context.WithoutCancel(ctx), r.d.Store, run); err != nil {
		run.Warnings = append(run.Warnings, "the run record could not be created: "+err.Error())
	}
	if in.Started != nil {
		in.Started(run.ID)
	}
	stopPing := r.ping(e)
	defer stopPing()
	if err := r.d.Page.Declines.Load(context.WithoutCancel(ctx)); err != nil {
		run.Warnings = append(run.Warnings, "the decline memory could not be read: "+err.Error())
	}
	r.exec(e)
	r.finish(e)
	return run
}

// ping tells the runs board that the run is still working, at least every 60
// seconds, from the stage it is in. It stops when the run ends.
func (r *Runner) ping(e *Exec) (stop func()) {
	if r.d.Board == nil {
		return func() {}
	}
	every := r.d.PingEvery
	if every == 0 {
		every = 30 * time.Second
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				st, _ := e.cur.Load().(Stage)
				r.d.Board(BoardUpdate{RunID: e.Run.ID, Stage: st, Detail: "working"})
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

// failedStage is the stage whose stop decided the outcome, or "".
func failedStage(run *Run) Stage {
	if run.Outcome.Kind == OutcomeStopped {
		return run.Outcome.Stage
	}
	return ""
}

func (r *Runner) exec(e *Exec) {
	if h := e.Run.Input.Hold; h != nil {
		if err := h.Acquire(e.Ctx); err != nil {
			r.fail(e, cancelledStop(StagePreflight))
			return
		}
	}
	steps := []struct {
		st Stage
		fn func() (string, *stop)
	}{
		{StagePreflight, func() (string, *stop) { return r.preflight(e) }},
		{StageResolve, func() (string, *stop) { return r.resolve(e) }},
	}
	for _, s := range steps {
		if !r.stage(e, s.st, s.fn) {
			return
		}
	}
	if st := r.drive(e); st != nil {
		r.fail(e, st)
	}
}

// drive runs the browser stages, which record their own stage lines.
func (r *Runner) drive(e *Exec) *stop {
	if err := e.Ctx.Err(); err != nil {
		return cancelledStop(StageAccount)
	}
	if e.resume != nil {
		return r.resumePages(e.Ctx, e)
	}
	if r.d.Drive == nil {
		return &stop{class: ClassEnvironment, reason: "no page driver is configured", at: StageFill}
	}
	return r.d.Drive(e.Ctx, e)
}

func cancelledStop(before Stage) *stop {
	return &stop{class: ClassEnvironment, reason: "cancelled before " + string(before), at: before}
}

// stage records one stage: status, detail and timing. A stop ends the run.
func (r *Runner) stage(e *Exec, st Stage, fn func() (string, *stop)) bool {
	if err := e.Ctx.Err(); err != nil {
		s := cancelledStop(st)
		r.record(e, st, StatusFailed, s.reason, r.d.Now())
		r.fail(e, s)
		return false
	}
	start := r.d.Now()
	detail, s := fn()
	if s != nil {
		if s.at != "" && s.at != st {
			// The stage decided a stop that belongs to a later stage: it ran
			// to its end, and the later stage reads the refusal.
			r.record(e, st, StatusOK, detail, start)
			r.record(e, s.at, StatusFailed, s.reason, start)
		} else {
			r.record(e, st, StatusFailed, firstNonEmpty(detail, s.reason), start)
			s.at = st
		}
		r.fail(e, s)
		return false
	}
	r.record(e, st, StatusOK, detail, start)
	return true
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// record appends a stage line and publishes the boundary.
func (r *Runner) record(e *Exec, st Stage, status Status, detail string, start time.Time) {
	e.cur.Store(st)
	e.Run.Stages = append(e.Run.Stages, StageLine{Stage: st, Status: status, Detail: clip(vault.Redact(detail), maxDetailLen), StartedAt: start, Ms: r.d.Now().Sub(start).Milliseconds()})
	r.persist(e)
	if r.d.Board != nil {
		r.d.Board(BoardUpdate{RunID: e.Run.ID, Stage: st, Detail: detail})
	}
}

// fail makes a stop the run's outcome. The first stop wins, and a run that
// submitted stays submitted: a fault after the press is kept as a warning,
// because the application was sent and the run must not read as one to attempt
// again.
func (r *Runner) fail(e *Exec, s *stop) {
	if e.Run.Outcome.Kind == OutcomeStopped {
		return
	}
	if e.Run.Outcome.Kind == OutcomeSubmitted {
		e.Run.Warnings = append(e.Run.Warnings, "after the submit: "+clip(vault.Redact(s.reason), maxReasonLen))
		return
	}
	if s.at == "" {
		s.at = StageFill
	}
	// A stop that a cancel caused is a cancellation, whatever the stage made of
	// the broken context; after a press the verdict is what the run reports.
	if e.Ctx.Err() != nil && !e.Run.Pressed && !strings.HasPrefix(s.reason, "cancelled") {
		s.reason, s.board = fmt.Sprintf("cancelled during %s: %s", s.at, s.reason), ""
	}
	e.Run.Outcome = Outcome{Kind: OutcomeStopped, Stage: s.at, Class: s.class, Reason: clip(vault.Redact(s.reason), maxReasonLen), Board: s.board}
}

func (r *Runner) persist(e *Exec) {
	if err := saveRun(context.WithoutCancel(e.Ctx), r.d.Store, e.Run); err != nil {
		e.Run.Warnings = append(e.Run.Warnings, "the run record could not be written: "+err.Error())
	}
}

// audit writes one gate decision for the run. A failed write is returned so
// the caller can refuse the action it guards.
func (r *Runner) audit(e *Exec, d gates.Decision, target string, trace bool) error {
	if r.d.Audit == nil {
		return &ErrAudit{Err: errors.New("no audit writer is configured")}
	}
	in := e.Run.Input
	err := r.d.Audit.Record(context.WithoutCancel(e.Ctx), d, gates.AuditContext{
		RunID: e.Run.ID, Caller: in.Caller, Tool: in.Tool, ConversationID: in.ConversationID,
		UserMessage: in.UserMessage, UserMessageHash: in.UserMessageHash, TargetURL: target, Trace: trace,
	})
	if err != nil {
		return &ErrAudit{Err: err}
	}
	return nil
}

func (r *Runner) preflight(e *Exec) (string, *stop) {
	v := e.Run.Input.Preflight
	if v == nil && e.Run.Input.PreflightFn != nil {
		v = e.Run.Input.PreflightFn(e.Ctx)
	}
	if v == nil {
		v = r.Preflight(e.Ctx)
	}
	e.Ver = v
	if v.Blocked() {
		return "", &stop{class: ClassEnvironment, reason: "preflight blocked: " + v.Summary()}
	}
	return "environment ready", nil
}

func (r *Runner) resolve(e *Exec) (string, *stop) {
	run := e.Run
	in := run.Input
	key, s := IdentityKey(in.URL)
	if s != nil {
		return "", s
	}
	var report string
	if r.d.Reports != nil {
		b, err := r.d.Reports.Bind(e.Ctx, key)
		if err != nil {
			return "", &stop{class: ClassEnvironment, reason: "the report index cannot be read: " + err.Error()}
		}
		if b != nil {
			run.Report = b
			report = fmt.Sprintf(" report=%d", b.Number)
		}
	}
	// A mirror is resolved once per list row: a row that arrives with its
	// employer's posting is classified as it is. Otherwise the mirror is
	// resolved here, and an unresolved one stops before any browser work.
	via := ""
	if IsAggregatorURL(in.URL) && in.EmployerURL == "" {
		hint := MirrorHint{Company: in.Company, Role: in.Role}
		if run.Report != nil {
			hint = MirrorHint{Company: run.Report.Company, Role: run.Report.Role}
		}
		res := r.d.Mirror.Resolve(e.Ctx, in.URL, hint)
		if res.Reason != "" {
			return res.Reason, &stop{class: ClassPosting, reason: res.Reason}
		}
		run.Input.EmployerURL, via = res.URL, " (resolved off an aggregator posting via "+res.Via+")"
	}
	cls, s := Classify(e.target())
	if s != nil {
		return "", s
	}
	run.Identity, run.ATS, run.Tenant = key, cls.ATS, cls.Tenant
	e.Cls = cls
	detail := fmt.Sprintf("ats=%s tenant=%s", cls.ATS, cls.Tenant) + via + report
	// The duplicate guard runs after resolve and before the browser is used.
	if d, s := r.guard(e); s != nil {
		if s.at != "" {
			return detail, s
		}
		return detail + "; " + d.Reason, s
	}
	if run.Correction {
		detail += fmt.Sprintf("; correction of ledger row %d", run.Supersedes)
	}
	return detail, nil
}

// guard evaluates the duplicate guard over the ledger and tracker evidence.
// A dry run is never blocked by it; unreadable evidence blocks a submitting
// run.
func (r *Runner) guard(e *Exec) (gates.Decision, *stop) {
	in := e.Run.Input
	if !in.Submit {
		d := gates.DuplicateGuard(gates.DuplicateInput{Submit: false})
		if err := r.audit(e, d, in.URL, false); err != nil {
			return d, &stop{class: ClassEnvironment, reason: err.Error()}
		}
		return d, nil
	}
	if r.d.Ledger == nil {
		return gates.Decision{}, &stop{class: ClassEnvironment, reason: "no submission ledger is configured"}
	}
	req := e.claimRequest()
	j, err := r.d.Ledger.Guard(e.Ctx, req)
	for _, d := range j.Decisions {
		if aerr := r.audit(e, d, in.URL, false); aerr != nil {
			return d, &stop{class: ClassEnvironment, reason: aerr.Error()}
		}
	}
	d := j.Last()
	if err != nil {
		class := Class(d.Class)
		if class == "" {
			class = ClassEnvironment
		}
		s := &stop{class: class, reason: d.Reason}
		if d.Reason == "" {
			s.reason = "the duplicate guard could not decide: " + err.Error()
		}
		if req.Correction != nil {
			// A correction is refused where it would have re-sent: on the
			// submit stage, before the browser is used.
			s.at = StageSubmit
		}
		return d, s
	}
	e.Run.Correction, e.Run.Supersedes = j.Supersedes != 0, j.Supersedes
	return d, nil
}

// claimRequest names the posting for the ledger: company and role from the
// bound report, else the list row.
func (e *Exec) claimRequest() ledger.ClaimRequest {
	in, run := e.Run.Input, e.Run
	company, role := in.Company, in.Role
	var num *int64
	if run.Report != nil {
		company, role = run.Report.Company, run.Report.Role
		n := run.Report.Number
		num = &n
	}
	host := ""
	if u, err := parseHost(in.URL); err == nil {
		host = u
	}
	req := ledger.ClaimRequest{URL: redactURL(in.URL), Host: host, Company: company, Role: role, Source: ledger.SourceApplyChain,
		RunID: run.ID, RunStartedAt: run.StartedAt, Resubmit: in.Resubmit, ReportNumber: num}
	if in.Submit && gates.CorrectionAsked(in.OwnWords) {
		// Only the user's own message makes a run a correction request; the
		// door then decides it on that message and the ledger.
		req.Correction = &ledger.CorrectionRequest{Caller: in.Caller, SinglePosting: in.BatchID == "" && in.Tool == ToolAutofill, Message: in.OwnWords}
	}
	return req
}

// finish marks the stages that did not run, then runs record and learn, which
// never raise.
func (r *Runner) finish(e *Exec) {
	run := e.Run
	if run.Outcome.Kind == outcomeRunning {
		run.Outcome = Outcome{Kind: OutcomeFilled}
		if e.held != "" {
			run.Outcome.Board, run.Outcome.Reason = HoldConfirmation, clip(vault.Redact(e.held), maxReasonLen)
		}
	}
	failed := failedStage(run)
	seen := map[Stage]bool{}
	for _, l := range run.Stages {
		seen[l.Stage] = true
	}
	for _, st := range Stages {
		if seen[st] || st == StageRecord || st == StageLearn {
			continue
		}
		detail := "not requested"
		if failed != "" {
			detail = "not attempted: " + string(failed) + " failed"
		} else if !run.Input.Submit && (st == StageSubmit || st == StageConfirm) {
			detail = "dry run: Submit not pressed"
		}
		run.Stages = append(run.Stages, StageLine{Stage: st, Status: StatusSkipped, Detail: detail, StartedAt: r.d.Now()})
	}
	ctx := context.WithoutCancel(e.Ctx)
	e.Ctx = ctx
	r.recordSignals(e)
	r.runRecord(e)
	r.runLearn(e)
	run.FinishedAt = r.d.Now()
	r.persist(e)
	if err := saveBounds(ctx, r.d.Store, run.ID, e.bounds()); err != nil {
		run.Warnings = append(run.Warnings, "the run's bounds could not be written: "+err.Error())
	}
	if r.d.Board != nil {
		r.d.Board(BoardUpdate{RunID: run.ID, Stage: StageLearn, Detail: string(run.Outcome.Kind), Reason: boardReason(run.Outcome), Done: true})
	}
}
