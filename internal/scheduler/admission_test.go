package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// gate is a job body that blocks until released and reports when it started.
type gate struct {
	started chan string
	release chan struct{}
}

func (h *harness) newGate() *gate {
	h.async = true
	return &gate{started: make(chan string, 64), release: make(chan struct{})}
}

func (g *gate) job() Job {
	return func(ctx context.Context, in Invocation) (string, error) {
		g.started <- in.Schedule
		select {
		case <-g.release:
			return "released", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (g *gate) waitStarted(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-g.started:
		case <-time.After(5 * time.Second):
			t.Fatal("a job did not start")
		}
	}
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	err := h.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) leaseRows() int {
	var n int
	if err := h.st.Read().QueryRow(`SELECT count(*) FROM resource_leases`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestScheduler_Admission_OfflineDeferralThenFire(t *testing.T) {
	h := newHarness(t)
	h.kind("test.net", nil, true, nil)
	h.start()
	sc := h.add("a", "test.net", hourly(), PolicySkip)
	h.online = false
	h.step(time.Hour) // 07:00 falls due while offline
	h.step(20 * time.Minute)
	if h.called("test.net") != 0 {
		t.Fatal("a network job started while offline")
	}
	h.online = true
	h.step(time.Minute)
	if h.called("test.net") != 1 {
		t.Fatalf("calls %d after connectivity returned", h.called("test.net"))
	}
	var deferred, fired int
	for _, r := range h.history(sc.ID) {
		switch r.Outcome {
		case OutcomeDeferred:
			deferred++
			if r.Reason != ReasonOffline {
				t.Errorf("deferral reason %q", r.Reason)
			}
		case OutcomeFired:
			fired++
		}
	}
	if deferred != 1 || fired != 1 {
		t.Fatalf("history shows %d deferrals and %d fires, want one each", deferred, fired)
	}
}

func TestScheduler_Admission_SameScheduleStillRunningIsBusy(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.block", nil, false, g.job())
	h.start()
	sc := h.add("a", "test.block", hourly(), PolicySkip)
	h.step(time.Hour)
	g.waitStarted(t, 1)
	h.step(time.Hour) // the next occurrence while the first still runs
	close(g.release)
	h.s.Wait()
	if d := h.decisions(sc.ID); !eq(d, "fired", "skipped/busy") {
		t.Fatalf("decisions %v", d)
	}
}

func TestScheduler_Admission_UnknownKindAfterUpgrade(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.exec(`UPDATE schedules SET job_kind = 'discover.removed' WHERE id = ?`, sc.ID)
	h.step(time.Hour)
	if d := h.decisions(sc.ID); !eq(d, "skipped/unknown-kind") {
		t.Fatalf("decisions %v", d)
	}
	views, err := h.mgr.List(context.Background())
	if err != nil || len(views) != 1 || !views[0].UnknownKind {
		t.Fatalf("the schedule is not flagged: %+v %v", views, err)
	}
}

func TestScheduler_Admission_CatalogDriftIsSkippedAsValidation(t *testing.T) {
	h := newHarness(t)
	if err := h.reg.Register(Kind{Name: "test.opts", Run: h.record("test.opts"), Fields: []Field{{Name: "mode", Type: FieldString, Enum: []string{"a", "b"}}}}); err != nil {
		t.Fatal(err)
	}
	h.start()
	sc, err := h.mgr.Create(context.Background(), NewSchedule{Name: "a", Kind: "test.opts", Preset: hourly(), Inputs: map[string]any{"mode": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE schedules SET inputs_json = '{"removedOption":true}' WHERE id = ?`, sc.ID)
	h.step(time.Hour)
	rows := h.history(sc.ID)
	if len(rows) == 0 || rows[0].Reason != ReasonValidation || !strings.Contains(rows[0].Detail, "removedOption") {
		t.Fatalf("history %+v", rows)
	}
}

func TestScheduler_Admission_ThreeRunAndEightWait(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.block", nil, false, g.job())
	h.start()
	var ids []int64
	for i := range 12 {
		ids = append(ids, h.add(fmt.Sprintf("s%02d", i), "test.block", dailyAt("07:00"), PolicySkip).ID)
	}
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 3)
	h.s.runMu.Lock()
	running, queued := len(h.s.running), len(h.s.queue)
	h.s.runMu.Unlock()
	if running != 3 || queued != 8 {
		t.Fatalf("%d running and %d waiting, want 3 and 8", running, queued)
	}
	deferred := 0
	for _, id := range ids {
		for _, r := range h.history(id) {
			if r.Outcome == OutcomeDeferred && r.Reason == ReasonBusy {
				deferred++
			}
		}
	}
	if deferred != 1 {
		t.Fatalf("%d occurrences deferred as busy, want 1", deferred)
	}
	close(g.release)
	h.s.Wait()
	h.step(time.Minute)
	h.s.Wait()
	fired := 0
	for _, id := range ids {
		for _, d := range h.decisions(id) {
			if d == "fired" {
				fired++
			}
		}
	}
	if fired != 12 {
		t.Fatalf("%d of 12 fired once the queue drained", fired)
	}
}

func TestScheduler_Leases_ConflictingJobsAreDeferredThenStart(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("liveness.audit", []Lease{LeaseBrowser, LeaseTracker}, false, g.job())
	h.kind("gmail.sync", []Lease{LeaseGmail, LeaseTracker}, false, nil)
	h.start()
	audit := h.add("audit", "liveness.audit", dailyAt("07:00"), PolicySkip)
	sync := h.add("sync", "gmail.sync", dailyAt("07:05"), PolicySkip)
	h.stepBy(time.Hour+6*time.Minute, 2*time.Minute)
	g.waitStarted(t, 1)
	if h.called("gmail.sync") != 0 {
		t.Fatal("the sync overlapped the audit")
	}
	var detail string
	for _, r := range h.history(sync.ID) {
		if r.Outcome == OutcomeDeferred {
			detail = r.Detail
		}
	}
	if !strings.Contains(detail, "tracker:write") || !strings.Contains(detail, "liveness.audit") {
		t.Fatalf("the deferral does not name the lease and holder: %q", detail)
	}
	close(g.release)
	h.s.Wait()
	h.step(time.Minute)
	h.s.Wait()
	if h.called("gmail.sync") != 1 || !eq(h.decisions(audit.ID), "fired") {
		t.Fatalf("sync calls %d, audit %v", h.called("gmail.sync"), h.decisions(audit.ID))
	}
}

func TestScheduler_Leases_DisjointJobsOverlap(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("discover.api", []Lease{LeaseEvaluation}, false, g.job())
	h.kind("cost.report", nil, false, g.job())
	h.start()
	h.add("scan", "discover.api", dailyAt("07:00"), PolicySkip)
	h.add("cost", "cost.report", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 2) // both are running at the same time
	close(g.release)
	h.s.Wait()
}

func TestScheduler_Leases_ReleasedWhenAJobPanics(t *testing.T) {
	h := newHarness(t)
	h.kind("test.boom", []Lease{LeaseTracker}, false, func(context.Context, Invocation) (string, error) { panic("boom") })
	h.kind("test.next", []Lease{LeaseTracker}, false, nil)
	h.start()
	boom := h.add("boom", "test.boom", dailyAt("07:00"), PolicySkip)
	h.add("next", "test.next", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	h.step(time.Minute)
	if h.leaseRows() != 0 {
		t.Fatalf("%d leases remain after a panic", h.leaseRows())
	}
	if h.called("test.next") != 1 {
		t.Fatal("the next waiting job did not start")
	}
	rows := h.history(boom.ID)
	if rows[0].Outcome != OutcomeFired || rows[0].Reason != ReasonError || !strings.Contains(rows[0].Detail, "panicked") {
		t.Fatalf("history %+v", rows[0])
	}
}

func TestScheduler_Leases_CapabilityMismatchFailsRegistration(t *testing.T) {
	r := NewRegistry()
	noop := func(context.Context, Invocation) (string, error) { return "", nil }
	for name, k := range map[string]Kind{
		"drives the scan window without browser":          {Name: "a", Run: noop, Drives: Drives{ScanWindow: true}},
		"browser without driving the window":              {Name: "b", Run: noop, Leases: []Lease{LeaseBrowser}},
		"reads gmail without the lease":                   {Name: "c", Run: noop, Drives: Drives{GmailRead: true}},
		"queues page-reading evaluations without browser": {Name: "e", Run: noop, Leases: []Lease{LeaseEvaluation}, Drives: Drives{Evaluation: true, EvaluationReadsPages: true}},
		"unknown lease": {Name: "d", Run: noop, Leases: []Lease{"apply"}},
	} {
		if err := r.Register(k); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := r.Register(Kind{Name: "ok", Run: noop, Leases: []Lease{LeaseGmail, LeaseTracker}, Drives: Drives{GmailRead: true, TrackerWrite: true}}); err != nil {
		t.Fatal(err)
	}
	k, _ := r.Get("ok")
	if len(k.Leases) != 2 {
		t.Fatalf("leases %v", k.Leases)
	}
}

// A kind that queues evaluations holds the browser lease without driving the
// window itself; the rule still ties the lease to a capability.
func TestScheduler_Leases_PageReadingEvaluationHoldsBrowser(t *testing.T) {
	r := NewRegistry()
	noop := func(context.Context, Invocation) (string, error) { return "", nil }
	err := r.Register(Kind{Name: "queues.evaluations", Run: noop, Leases: []Lease{LeaseBrowser, LeaseEvaluation},
		Drives: Drives{Evaluation: true, EvaluationReadsPages: true}})
	if err != nil {
		t.Fatalf("browser and llm:evaluation for a kind that queues page-reading evaluations: %v", err)
	}
	err = r.Register(Kind{Name: "plain.evaluation", Run: noop, Leases: []Lease{LeaseBrowser, LeaseEvaluation}, Drives: Drives{Evaluation: true}})
	if err == nil || !strings.Contains(err.Error(), "browser") {
		t.Fatalf("browser without any use of the window was accepted: %v", err)
	}
}

func TestScheduler_Leases_ExpiredLeaseIsTakenOverByASecondScheduler(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.lease", []Lease{LeaseTracker}, false, g.job())
	h.kind("test.other", []Lease{LeaseTracker}, false, nil)
	a := h.scheduler("sched_a")
	b := h.scheduler("sched_b")
	ctx := context.Background()
	h.add("a-job", "test.lease", dailyAt("07:00"), PolicySkip)
	other := h.add("b-job", "test.other", dailyAt("07:30"), PolicySkip)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(context.Background()) })
	// Scheduler A is the only one that ticks until it dies.
	for range 12 {
		h.clock.Advance(5 * time.Minute)
		a.Tick(ctx)
	}
	g.waitStarted(t, 1) // A's job holds tracker:write from 07:00
	for range 14 {
		h.clock.Advance(2 * time.Minute)
		a.Tick(ctx) // the last renewal is at 07:28
	}
	h.clock.Advance(time.Minute)
	a.Tick(ctx)                      // 07:29
	h.clock.Advance(2 * time.Minute) // 07:31
	b.Tick(ctx)
	b.Wait()
	if h.called("test.other") != 0 {
		t.Fatal("B started while A held the lease")
	}
	if got := h.history(other.ID); len(got) == 0 || got[0].Outcome != OutcomeDeferred || got[0].Reason != ReasonLease {
		t.Fatalf("B's history %+v", got)
	}
	// A stops renewing (its process died). Past the lease TTL B takes over.
	h.clock.Advance(4 * time.Minute) // 07:35, after the lease expired at 07:34
	b.Tick(ctx)
	b.Wait()
	if h.called("test.other") != 1 {
		t.Fatalf("B did not take over the expired lease; calls %d", h.called("test.other"))
	}
	close(g.release)
	a.Wait()
}

func TestScheduler_Leases_HeldLeaseIsRenewedByTheTick(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.lease", []Lease{LeaseTracker}, false, g.job())
	h.start()
	h.add("a-job", "test.lease", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 1)
	h.stepBy(2*leaseTTL, time.Minute)
	busy, err := h.s.leases.free(context.Background(), []Lease{LeaseTracker})
	if err != nil || busy == nil {
		t.Fatalf("the running job lost its lease: %v %v", busy, err)
	}
	close(g.release)
	h.s.Wait()
}

func TestScheduler_Retry_TransientThenSuccess(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	n := 0
	h.kind("test.flaky", nil, true, func(context.Context, Invocation) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return "", Transient(errors.New("connection reset"))
		}
		return "2 rows", nil
	})
	h.start()
	sc := h.add("a", "test.flaky", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	h.s.Wait()
	rows := h.history(sc.ID)
	if rows[0].Attempts != 2 || h.get(sc.ID).LastResult != ResultOK || rows[0].Summary != "2 rows" {
		t.Fatalf("attempts %d result %q summary %q", rows[0].Attempts, h.get(sc.ID).LastResult, rows[0].Summary)
	}
	if len(h.sleeps) != 1 || h.sleeps[0] != 30*time.Second {
		t.Fatalf("waits %v", h.sleeps)
	}
}

func TestScheduler_Retry_SignInNotRetriedRaisesNeedsYou(t *testing.T) {
	h := newHarness(t)
	h.kind("test.board", []Lease{LeaseBrowser}, true, func(context.Context, Invocation) (string, error) {
		return "", NeedsYou(ReasonNeedsSignIn, errors.New("the board shows a sign-in page"))
	})
	h.start()
	sc := h.add("a", "test.board", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	h.s.Wait()
	rows := h.history(sc.ID)
	if rows[0].Attempts != 1 || rows[0].Reason != ReasonNeedsSignIn || h.get(sc.ID).LastResult != ResultFailed {
		t.Fatalf("history %+v", rows[0])
	}
	if len(h.sleeps) != 0 || len(h.needs) != 1 || h.needs[0].Reason != ReasonNeedsSignIn || h.needs[0].Schedule != "a" {
		t.Fatalf("waits %v, needs-you %+v", h.sleeps, h.needs)
	}
}

// A needs-you item names the schedule of the failed run, and the next run of
// that schedule that succeeds reports the schedule recovered; a failed run and
// a success of another schedule do not.
func TestScheduler_NeedsYou_ALaterSuccessOfTheScheduleReportsItRecovered(t *testing.T) {
	h := newHarness(t)
	signedIn := map[string]bool{"b": true}
	h.kind("test.board", []Lease{LeaseBrowser}, true, func(_ context.Context, in Invocation) (string, error) {
		if !signedIn[in.Schedule] {
			return "", NeedsYou(ReasonNeedsSignIn, errors.New("the board shows a sign-in page"))
		}
		return "scanned", nil
	})
	h.start()
	a := h.add("a", "test.board", dailyAt("23:00"), PolicySkip)
	b := h.add("b", "test.board", dailyAt("23:00"), PolicySkip)
	run := func(sc Schedule) {
		h.clock.Advance(time.Minute)
		if _, err := h.s.RunNow(context.Background(), sc.ID); err != nil {
			t.Fatal(err)
		}
		h.s.Wait()
	}
	run(a)
	run(b)
	if len(h.needs) != 1 || h.needs[0].ScheduleID != a.ID || fmt.Sprint(h.recovered) != fmt.Sprint([]int64{b.ID}) {
		t.Fatalf("a failed, b succeeded: needs-you %+v, recovered %v", h.needs, h.recovered)
	}
	signedIn["a"] = true
	run(a)
	if fmt.Sprint(h.recovered) != fmt.Sprint([]int64{b.ID, a.ID}) {
		t.Fatalf("recovered %v after a succeeded", h.recovered)
	}
}

func TestScheduler_Retry_KindWithoutNetworkIsNeverRetried(t *testing.T) {
	h := newHarness(t)
	h.kind("test.local", nil, false, func(context.Context, Invocation) (string, error) {
		return "", Transient(errors.New("disk busy"))
	})
	h.start()
	sc := h.add("a", "test.local", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	h.s.Wait()
	if rows := h.history(sc.ID); rows[0].Attempts != 1 || len(h.sleeps) != 0 {
		t.Fatalf("attempts %d waits %v", rows[0].Attempts, h.sleeps)
	}
}

func TestScheduler_Leases_ActiveHolderSurvivesClockGap(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual-%t", manual), func(t *testing.T) {
			h := newHarness(t)
			g := h.newGate()
			h.kind("test.first", []Lease{LeaseTracker}, false, g.job())
			h.kind("test.second", []Lease{LeaseTracker}, false, nil)
			h.start()
			first := h.add("first", "test.first", dailyAt("23:00"), PolicyFireOnce)
			second := h.add("second", "test.second", dailyAt("06:05"), PolicyFireOnce)
			run, err := h.s.RunNow(context.Background(), first.ID)
			if err != nil {
				t.Fatal(err)
			}
			g.waitStarted(t, 1)
			h.clock.Suspend(6 * time.Minute)
			if manual {
				_, err = h.s.RunNow(context.Background(), second.ID)
				var refused *RefusedError
				if !errors.As(err, &refused) || refused.Reason != ReasonLease {
					t.Fatalf("expired live holder admitted competing manual run: %v", err)
				}
			} else {
				h.tick()
				if rows := h.history(second.ID); len(rows) != 1 || rows[0].Outcome != OutcomeDeferred || rows[0].Reason != ReasonLease {
					t.Fatalf("expired live holder did not defer competing schedule: %+v", rows)
				}
			}
			if n := h.called("test.second"); n != 0 {
				t.Fatalf("%d competing calls before release", n)
			}
			if !h.s.Cancel(run) {
				t.Fatal("could not cancel holder")
			}
			h.s.Wait()
			h.clock.Advance(time.Millisecond)
			if _, err := h.s.RunNow(context.Background(), second.ID); err != nil {
				t.Fatal(err)
			}
			h.s.Wait()
			if n := h.called("test.second"); n != 1 {
				t.Fatalf("calls after release=%d", n)
			}
		})
	}
}
