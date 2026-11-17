package apply

import (
	"context"
	"errors"
	"strings"

	"testing"
	"testing/synctest"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

const pageURL = "https://careers.example.test/jobs/7"

func TestApply_Stages_SkippedAfterAStopRecordAndLearnStillRun(t *testing.T) {
	g := newRig(t, nil)
	run := g.r.RunOne(context.Background(), Input{URL: "mailto:hr@example.test", Caller: gates.CallerCLI})
	mustStop(t, run, StageResolve, ClassPosting)
	for _, st := range []Stage{StageAccount, StageFill, StageCheck, StageFix, StageSubmit, StageConfirm} {
		l := stageOf(run, st)
		if l.Status != StatusSkipped || l.Detail != "not attempted: resolve failed" {
			t.Errorf("%s: %+v", st, l)
		}
	}
	for _, st := range []Stage{StageRecord, StageLearn} {
		if stageOf(run, st).Status != StatusOK {
			t.Errorf("%s did not run", st)
		}
	}
}

func TestApply_Preflight_OneVerdictPerInvocationNoRetry(t *testing.T) {
	g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { return nil } })
	v := g.r.Preflight(context.Background())
	if v.Blocked() {
		t.Fatalf("verdict %s", v.Summary())
	}
	for range 3 {
		run := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerApplyList, Preflight: v})
		if run.Outcome.Kind != OutcomeFilled {
			t.Fatalf("outcome %+v", run.Outcome)
		}
	}
	if g.env.attached != 1 || g.env.healthN != 1 {
		t.Fatalf("attached %d, health probes %d: the list took more than one verdict", g.env.attached, g.env.healthN)
	}
}

func TestApply_Preflight_ServerStillStartingAndSeveralFaultsAtOnce(t *testing.T) {
	g := newRig(t, nil)
	g.env.healthSeq = []healthReply{{err: errNoHTTP}, {status: 200}}
	v := g.r.Preflight(context.Background())
	if v.Blocked() || g.env.healthN != 2 {
		t.Fatalf("blocked %v after %d probes", v.Blocked(), g.env.healthN)
	}
	g.env.bankN = 0
	g.env.profile.ResumeErr = "no such file"
	v = g.r.Preflight(context.Background())
	if !v.Blocked() {
		t.Fatal("not blocked")
	}
	s := v.Summary()
	if !strings.Contains(s, "answer bank") || !strings.Contains(s, "answer-bank.yml") || !strings.Contains(s, "résumé") {
		t.Fatalf("summary %q", s)
	}
}

// hungEnv is an environment whose health probe never answers.
type hungEnv struct{ *fakeEnv }

func (hungEnv) Health(ctx context.Context) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestApply_Preflight_HungProbesEndWithinTheWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		c := healthCheck(context.Background(), hungEnv{readyEnv()}, PreflightTiming{}.withDefaults())
		if took := time.Since(start); c.OK || took > 60*time.Second {
			t.Fatalf("check %+v after %s, want a failure within 60s", c, took)
		}
	})
}

func TestApply_Preflight_BlockedVerdictStopsEveryRowWithoutABrowser(t *testing.T) {
	g := newRig(t, nil)
	g.env.bankN = 0
	v := g.r.Preflight(context.Background())
	for range 8 {
		run := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerApplyList, Preflight: v})
		mustStop(t, run, StagePreflight, ClassEnvironment)
		if stageOf(run, StageResolve).Detail != "not attempted: preflight failed" {
			t.Fatalf("resolve line %+v", stageOf(run, StageResolve))
		}
	}
	if g.env.attached != 1 {
		t.Fatalf("attached %d times", g.env.attached)
	}
}

func TestApply_RunID_StrictlyIncreasingWithinOneTick(t *testing.T) {
	g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { return nil } })
	prev := ""
	for range 5 {
		run := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
		if run.ID <= prev {
			t.Fatalf("%s did not follow %s", run.ID, prev)
		}
		prev = run.ID
	}
	if g.count(`SELECT count(DISTINCT run_id) FROM apply_runs`) != 5 {
		t.Fatal("rows collided")
	}
}

func TestApply_Cancel_CheckedBeforeEachStage(t *testing.T) {
	g := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := g.r.RunOne(ctx, Input{URL: pageURL, Caller: gates.CallerCLI})
	mustStop(t, run, StagePreflight, ClassEnvironment)
	if !strings.Contains(run.Outcome.Reason, "cancelled") {
		t.Fatalf("reason %q", run.Outcome.Reason)
	}
	if stageOf(run, StageRecord).Status != StatusOK {
		t.Fatal("record did not run after a cancel")
	}
	if g.count(`SELECT count(*) FROM apply_runs WHERE outcome_kind = 'stopped' AND finished_at IS NOT NULL`) != 1 {
		t.Fatal("not durable")
	}
}

func TestApply_Record_NoSecretInStageDetail(t *testing.T) {
	g := newRig(t, func(d *Deps) {
		d.Drive = func(ctx context.Context, e *Exec) *stop {
			// The account stage records its stop as the browser stages do.
			var s *stop
			e.r.stage(e, StageAccount, func() (string, *stop) {
				s = &stop{class: ClassCredential, reason: "password hunter2-example-secret was refused", at: StageAccount}
				return "", s
			})
			return s
		}
	})
	vaultRegister(t, "hunter2-example-secret")
	run := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
	if strings.Contains(run.Outcome.Reason, "hunter2-example-secret") {
		t.Fatalf("reason %q", run.Outcome.Reason)
	}
	details := 0
	for _, l := range run.Stages {
		if l.Stage == StageAccount && l.Detail != "" {
			details++
		}
		if strings.Contains(l.Detail, "hunter2-example-secret") {
			t.Fatalf("%s line detail %q", l.Stage, l.Detail)
		}
	}
	if details == 0 {
		t.Fatalf("stages %+v: the account stage's stop is written in its line", run.Stages)
	}
	var reason string
	if err := g.st.Read().QueryRow(`SELECT outcome_reason FROM apply_runs WHERE run_id = ?`, run.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reason, "hunter2-example-secret") {
		t.Fatalf("stored reason %q", reason)
	}
}

func TestApply_Audit_DryRunGuardAndGateRowsAreWritten(t *testing.T) {
	g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { return nil } })
	run := g.r.RunOne(context.Background(), Input{URL: pageURL + "?utm_source=x#frag", Caller: gates.CallerCLI})
	if run.Outcome.Kind != OutcomeFilled || g.audits(gates.GateDuplicateGuard) != 1 {
		t.Fatalf("outcome %+v, guard rows %d", run.Outcome, g.audits(gates.GateDuplicateGuard))
	}
	var host, path, hash string
	if err := g.st.Read().QueryRow(`SELECT target_host, target_path, COALESCE(user_message_hash, '') FROM gate_decisions WHERE run_id = ?`, run.ID).Scan(&host, &path, &hash); err != nil {
		t.Fatal(err)
	}
	if host != "careers.example.test" || path != "/jobs/7" {
		t.Fatalf("target %s %s", host, path)
	}
}

func TestApply_Sweep_RunningRowsEndedAfterACrash(t *testing.T) {
	g := newRig(t, nil)
	run := &Run{ID: "run_dead", StartedAt: t0, Input: Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome: Outcome{Kind: outcomeRunning}}
	if err := saveRun(context.Background(), g.st, run); err != nil {
		t.Fatal(err)
	}
	n, err := SweepRuns(context.Background(), g.st, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("swept %d, %v", n, err)
	}
}

// foreignWindow is a browser the managed one did not start: driving it at all
// is the failure.
type foreignWindow struct{ opened int }

func (w *foreignWindow) OpenTab(context.Context, string, engine.Host) (Tab, error) {
	w.opened++
	return nil, errors.New("a foreign browser was driven")
}

func TestApply_Preflight_ForeignBrowserRefused(t *testing.T) {
	// The managed browser refuses to attach to a listener it did not launch and
	// reports that as a failed attach; the run is an environment stop that names
	// the apply window, and nothing drives any other browser.
	foreign := &foreignWindow{}
	g := newRig(t, nil)
	g.env.window = foreign
	g.env.attachErr = errors.New("launch-failed: the debugging port is held by a process this app did not start")
	run := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
	mustStop(t, run, StagePreflight, ClassEnvironment)
	if !strings.Contains(run.Outcome.Reason, "apply window") || !strings.Contains(run.Outcome.Reason, "did not start") {
		t.Fatalf("reason %q", run.Outcome.Reason)
	}
	if foreign.opened != 0 || stageOf(run, StageAccount).Status != StatusSkipped {
		t.Fatalf("tabs opened on the foreign browser: %d; account line %+v", foreign.opened, stageOf(run, StageAccount))
	}
}

// The first page a run opens names its posting. A run started from a bare
// address learns its role and employer from that page's JobPosting data, once,
// and keeps them apart from the request: the record's company and role are what
// a retry sends back as the request and what the captcha skip-list gate reads,
// and neither may take a page's word.
func TestApply_Run_TheFirstPageStatesItsPostingAndTheRequestIsLeftAlone(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) {
		d.Page.Timing = WalkTiming{AfterForm: 200 * time.Millisecond, First: 100 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 100 * time.Millisecond}
	})
	a.g.env.window = scriptWindow{a.tab}
	a.tab.set(appPage())
	// What probe.js hands over for a page whose data carries markup and a line break.
	a.tab.posting = map[string]string{"title": "<b>Platform Engineer</b>\n(Remote)", "company": "Northwind Example"}
	run := a.g.r.RunOne(context.Background(), Input{URL: loginURL, Caller: gates.CallerCLI})
	if run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if run.Stated != (Stated{Company: "Northwind Example", Role: "Platform Engineer (Remote)"}) || a.tab.postingReads != 1 {
		t.Fatalf("stated %+v after %d reads, want the cleaned names after one", run.Stated, a.tab.postingReads)
	}
	if run.company() != "" || run.role() != "" {
		t.Errorf("the run's company %q and role %q took the page's word", run.company(), run.role())
	}
	var company, role string
	if err := a.g.st.Read().QueryRow(`SELECT company, role FROM apply_runs WHERE run_id = ?`, run.ID).Scan(&company, &role); err != nil || company != "" || role != "" {
		t.Errorf("the record holds company %q and role %q (%v), want the request's: none", company, role, err)
	}
}

// A fault after the application was sent never takes the submission back: a
// panic in the confirm stage's bookkeeping ends as a warning on a submitted
// run, so the run is not read as stopped and attempted again.
func TestApply_Runner_AFaultAfterTheSubmitLeavesTheRunSubmitted(t *testing.T) {
	g := newRig(t, nil)
	e := exec1(g, Input{URL: pageURL, Caller: gates.CallerCLI, Submit: true}, Outcome{Kind: OutcomeSubmitted, Confirmed: true, ConfirmSource: ConfirmPage})
	g.r.fail(e, internalError("boom", nil))
	o := e.Run.Outcome
	if o.Kind != OutcomeSubmitted || !o.Confirmed || o.ConfirmSource != ConfirmPage {
		t.Fatalf("outcome %+v: a submitted run was overwritten", o)
	}
	if len(e.Run.Warnings) != 1 || !strings.Contains(e.Run.Warnings[0], "boom") {
		t.Fatalf("warnings %v: the fault is kept as a warning", e.Run.Warnings)
	}
}
