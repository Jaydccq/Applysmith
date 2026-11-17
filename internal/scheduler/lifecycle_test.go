package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/store"
)

func TestScheduler_Manual_BlockedByALeaseNamesTheHolder(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("liveness.audit", []Lease{LeaseTracker, LeaseBrowser}, false, g.job())
	h.kind("gmail.sync", []Lease{LeaseTracker, LeaseGmail}, false, nil)
	h.start()
	h.add("audit", "liveness.audit", dailyAt("07:00"), PolicySkip)
	sync := h.add("sync", "gmail.sync", dailyAt("23:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 1)
	_, err := h.s.RunNow(context.Background(), sync.ID)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Reason != ReasonLease || !strings.Contains(refused.Detail, "liveness.audit") {
		t.Fatalf("err = %v", err)
	}
	close(g.release)
	h.s.Wait()
}

func TestScheduler_Manual_DisabledScheduleRunsAndIsRecordedManual(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", dailyAt("23:00"), PolicySkip)
	off := false
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	cursor := h.get(sc.ID).LastFiredAt
	h.clock.Advance(time.Hour)
	runID, err := h.s.RunNow(context.Background(), sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	rows := h.history(sc.ID)
	if len(rows) != 1 || rows[0].Outcome != OutcomeManual || rows[0].RunID != runID || rows[0].FinishedAt.IsZero() {
		t.Fatalf("history %+v", rows)
	}
	got := h.get(sc.ID)
	if got.Enabled || got.LastResult != ResultOK || !got.LastFiredAt.Equal(cursor) {
		t.Fatalf("a manual run moved the cursor or enabled the schedule: %+v", got)
	}
}

func TestScheduler_Manual_RefusedForAnUnknownKindAndBadInputs(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", dailyAt("23:00"), PolicySkip)
	h.exec(`UPDATE schedules SET inputs_json = '{"x":1}' WHERE id = ?`, sc.ID)
	_, err := h.s.RunNow(context.Background(), sc.ID)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Reason != ReasonValidation {
		t.Fatalf("err = %v", err)
	}
	if _, err := h.s.RunNow(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestScheduler_Manual_DeleteWhileRunningConflictsUnlessCancelled(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.block", []Lease{LeaseTracker}, false, g.job())
	h.start()
	sc := h.add("a", "test.block", dailyAt("23:00"), PolicySkip)
	runID, err := h.s.RunNow(context.Background(), sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	g.waitStarted(t, 1)
	if err := h.s.Remove(context.Background(), sc.ID, false); !errors.Is(err, ErrRunning) {
		t.Fatalf("delete without the flag: %v", err)
	}
	if _, err := h.mgr.Repo().Get(context.Background(), sc.ID); err != nil {
		t.Fatalf("the schedule did not remain: %v", err)
	}
	if err := h.s.Remove(context.Background(), sc.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.Repo().Get(context.Background(), sc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the schedule remains: %v", err)
	}
	run, err := h.st.GetRun(context.Background(), runID)
	if err != nil || run.Status != "cancelled" {
		t.Fatalf("run %+v %v", run, err)
	}
	if h.leaseRows() != 0 {
		t.Fatal("the cancelled job kept its lease")
	}
}

func TestScheduler_Stop_CancelsWaitsMarksInterrupted(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.block", []Lease{LeaseTracker}, false, g.job())
	h.start()
	sc := h.add("a", "test.block", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 1)
	h.s.Stop(context.Background())
	run := h.get(sc.ID)
	if run.ActiveRunID != "" || run.LastResult != ResultFailed {
		t.Fatalf("schedule %+v", run)
	}
	rows := h.history(sc.ID)
	if rows[0].Outcome != OutcomeInterrupted || rows[0].Reason != ReasonInterrupted {
		t.Fatalf("history %+v", rows[0])
	}
	if h.leaseRows() != 0 {
		t.Fatal("a lease remains after the stop")
	}
	if h.s.Health().Status != "down" {
		t.Fatalf("health %+v", h.s.Health())
	}
}

func TestScheduler_Stop_MarksJobsThatIgnoreCancellationInterrupted(t *testing.T) {
	old := stopWait
	stopWait = 100 * time.Millisecond
	defer func() { stopWait = old }()
	h := newHarness(t)
	stuck := make(chan struct{})
	started := make(chan struct{})
	h.kind("test.stuck", nil, false, func(context.Context, Invocation) (string, error) {
		close(started)
		<-stuck
		return "", nil
	})
	h.start()
	sc := h.add("a", "test.stuck", dailyAt("07:00"), PolicySkip)
	h.async = true
	h.stepBy(time.Hour, 5*time.Minute)
	<-started
	h.s.Stop(context.Background())
	if rows := h.history(sc.ID); rows[0].Outcome != OutcomeInterrupted {
		t.Fatalf("history %+v", rows[0])
	}
	close(stuck)
	h.s.Wait()
	run := h.history(sc.ID)[0]
	if run.Outcome != OutcomeInterrupted {
		t.Fatalf("a late finish rewrote the row: %+v", run)
	}
}

func TestScheduler_Start_SweepBeforeRecoveryBeforeFirstTick(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	sc := h.add("a", "test.fast", hourly(), PolicyFireOnce)
	// A dead process left a run claimed and a lease behind.
	s1 := h.scheduler("sched_old")
	runID, err := s1.Repo().ClaimRun(context.Background(), Claim{Schedule: h.get(sc.ID), ScheduledFor: baseTime.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO resource_leases (name, holder, owner_pid, owner_instance, acquired_at, expires_at)
		VALUES ('browser', 'dead', 1, 'sched_old', ?, ?)`, ts(baseTime), ts(baseTime.Add(time.Minute)))
	h.clock.Advance(2*time.Hour + 10*time.Minute)
	h.start()
	run, _ := h.st.GetRun(context.Background(), runID)
	if run.Status != "failed" || run.FailureReason != "interrupted" {
		t.Fatalf("run %+v", run)
	}
	if h.leaseRows() != 0 {
		t.Fatal("a stale lease remains")
	}
	// The pass after the sweep fired the latest missed occurrence under fire-once.
	if d := h.decisions(sc.ID); !eq(d, "interrupted/interrupted", "fired") {
		t.Fatalf("decisions %v", d)
	}
	h.s.Wait()
}

func TestScheduler_Recovery_InterruptedNotRetried(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	s1 := h.scheduler("sched_old")
	if _, err := s1.Repo().ClaimRun(context.Background(), Claim{Schedule: h.get(sc.ID), ScheduledFor: baseTime.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour + time.Minute)
	for range 2 { // idempotent
		s := h.scheduler("sched_new")
		if err := s.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.Stop(context.Background())
	}
	if h.called("test.fast") != 0 {
		t.Fatal("an interrupted run was started again")
	}
	if d := h.decisions(sc.ID); !eq(d, "interrupted/interrupted") {
		t.Fatalf("decisions %v", d)
	}
}

func TestScheduler_Health_OkDegradedDown(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	if got := h.scheduler("sched_a").Health().Status; got != "down" {
		t.Fatalf("before start: %s", got)
	}
	h.start()
	if got := h.s.Health().Status; got != "ok" {
		t.Fatalf("after start: %s", got)
	}
	h.clock.Advance(3 * time.Minute)
	if got := h.s.Health(); got.Status != "degraded" {
		t.Fatalf("after 3 minutes without a tick: %+v", got)
	}
	h.tick()
	if got := h.s.Health().Status; got != "ok" {
		t.Fatalf("after a tick: %s", got)
	}
}

func TestScheduler_Lock_LostStopsTicking(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	held := true
	var mu sync.Mutex
	s := h.scheduler("sched_a", func(o *Options) {
		o.LockHeld = func() bool { mu.Lock(); defer mu.Unlock(); return held }
	})
	h.s = s
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	mu.Lock()
	held = false
	mu.Unlock()
	h.step(time.Hour)
	s.Tick(context.Background())
	s.Wait()
	if h.called("test.fast") != 0 || len(h.decisions(sc.ID)) != 0 {
		t.Fatal("the scheduler ticked without the lock")
	}
	if got := s.Health(); got.Status != "down" {
		t.Fatalf("health %+v", got)
	}
}

func TestScheduler_Timers_FailureIsolated(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	h.s.AddTimer(EveryTick("failing", "always fails", func(context.Context) error { return errors.New("boom") }))
	h.s.AddTimer(EveryTick("panicking", "always panics", func(context.Context) error { panic("boom") }))
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.step(time.Hour)
	if d := h.decisions(sc.ID); !eq(d, "fired") {
		t.Fatalf("user schedule: %v", d)
	}
}

func TestScheduler_Timers_LimitPerRun(t *testing.T) {
	old := timerLimit
	timerLimit = 50 * time.Millisecond
	defer func() { timerLimit = old }()
	h := newHarness(t)
	h.start()
	done := make(chan error, 1)
	h.s.AddTimer(Every("slow", "blocks", time.Nanosecond, func(ctx context.Context) error {
		<-ctx.Done()
		done <- ctx.Err()
		return ctx.Err()
	}))
	h.tick()
	h.clock.Advance(time.Second)
	h.tick()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the timer was not limited")
	}
}

func TestScheduler_Timers_RetentionCatchesUpAfterSleepingThrough0400(t *testing.T) {
	h := newHarness(t)
	h.start()
	runs := 0
	h.s.AddRetention("counting", func(context.Context) error { runs++; return nil })
	h.tick()
	h.clock.Suspend(30 * time.Hour) // slept through 04:00
	h.tick()
	if runs != 2 {
		t.Fatalf("the missed slot was not served on wake: %d", runs)
	}
}

func TestScheduler_Timers_RetentionRuleFailureDoesNotStopTheOthers(t *testing.T) {
	h := newHarness(t)
	h.start()
	ran := false
	h.s.AddRetention("failing", func(context.Context) error { return errors.New("disk") })
	h.s.AddRetention("panicking", func(context.Context) error { panic("x") })
	h.s.AddRetention("fine", func(context.Context) error { ran = true; return nil })
	h.tick()
	if !ran {
		t.Fatal("a later rule did not run")
	}
}

// New0 builds a scheduler to list its default timers.
func New0(t *testing.T, h *harness) *Scheduler { return h.scheduler("sched_listing") }

func TestScheduler_History_RetentionDeletesAfter90Days(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.step(2 * time.Hour)
	if len(h.history(sc.ID)) == 0 {
		t.Fatal("no history")
	}
	h.clock.Advance(91 * 24 * time.Hour)
	n, err := h.mgr.Repo().PruneHistory(context.Background())
	if err != nil || n == 0 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	if rows := h.history(sc.ID); len(rows) != 0 {
		t.Fatalf("%d rows remain", len(rows))
	}
}

func TestScheduler_History_LinksToRunsBoardRun(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.step(time.Hour)
	row := h.history(sc.ID)[0]
	run, err := h.st.GetRun(context.Background(), row.RunID)
	if err != nil || run.Kind != "test.fast" || run.Status != "succeeded" {
		t.Fatalf("run %+v %v", run, err)
	}
}

func TestScheduler_NoSubmit_RegistrationRejectsSubmitCapable(t *testing.T) {
	r := NewRegistry()
	noop := func(context.Context, Invocation) (string, error) { return "", nil }
	if err := r.Register(Kind{Name: "x.y", Run: noop, SubmitCapable: true}); err == nil || !strings.Contains(err.Error(), "submit") {
		t.Fatalf("err = %v", err)
	}
	for _, name := range []string{"apply.list", "apply.one", "apply-chain"} {
		if err := r.Register(Kind{Name: name, Run: noop}); err == nil {
			t.Errorf("kind %q was registered", name)
		}
	}
	if err := r.Register(Kind{Name: "x.y", Run: noop, Fields: []Field{{Name: "submit", Type: FieldBool}}}); err == nil {
		t.Error("a kind that accepts a submit input was registered")
	}
	if err := r.Register(Kind{Name: "x.z", Run: noop, Fields: []Field{{Name: "postingUrl", Type: FieldString}}}); err == nil {
		t.Error("a kind that accepts a posting url was registered")
	}
}

func TestScheduler_NoSubmit_ApplyKindAndSubmitInputRejectedAtCreate(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	_, err := h.mgr.Create(context.Background(), NewSchedule{Name: "a", Kind: "apply.list", Preset: hourly()})
	var inv *InvalidError
	if !errors.As(err, &inv) || inv.Field != "kind" || !strings.Contains(inv.Message, "test.fast") {
		t.Fatalf("apply kind: %v", err)
	}
	_, err = h.mgr.Create(context.Background(), NewSchedule{Name: "b", Kind: "test.fast", Preset: hourly(), Inputs: map[string]any{"submit": true}})
	if !errors.As(err, &inv) || inv.Field != "inputs" || !strings.Contains(inv.Message, `"submit"`) {
		t.Fatalf("submit input: %v", err)
	}
}

func TestScheduler_Registry_UnknownKeyRejectedNamingIt(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	_, err := h.mgr.Create(context.Background(), NewSchedule{Name: "a", Kind: "test.fast", Preset: hourly(), Inputs: map[string]any{"bogus": 1}})
	if err == nil || !strings.Contains(err.Error(), `"bogus"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduler_TryLeases_HoldsTheLeaseForAWorkerBesideTheQueue(t *testing.T) {
	h := newHarness(t)
	a, b := h.scheduler("sched_a"), h.scheduler("sched_b")
	ctx := context.Background()
	release, ok, err := a.TryLeases(ctx, "ask-agent-triggers", LeaseEvaluation)
	if err != nil || !ok {
		t.Fatalf("first take: ok=%v err=%v", ok, err)
	}
	if _, ok, err := b.TryLeases(ctx, "other", LeaseEvaluation); err != nil || ok {
		t.Fatalf("a held lease was taken: ok=%v err=%v", ok, err)
	}
	release()
	if release2, ok, err := b.TryLeases(ctx, "other", LeaseEvaluation); err != nil || !ok {
		t.Fatalf("after release: ok=%v err=%v", ok, err)
	} else {
		release2()
	}
}

// snapshotRig is a workspace with a user file, a snapshot directory beside it
// and a harness store that carries a workspace identity.
type snapshotRig struct {
	root, dir string
}

func newSnapshotRig(t *testing.T, h *harness) snapshotRig {
	t.Helper()
	base := t.TempDir()
	r := snapshotRig{root: filepath.Join(base, "ws"), dir: filepath.Join(base, "ws-backups")}
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "cv.md"), []byte("# CV\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO workspace_meta (key, value) VALUES ('workspace_id', '0123456789abcdef0123456789abcdef')`)
	return r
}

func (r snapshotRig) names(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(r.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestScheduler_Timers_SnapshotOncePerLocalDayAt0200(t *testing.T) {
	h := newHarness(t)
	rig := newSnapshotRig(t, h)
	h.start()
	h.s.AddSnapshots(SnapshotOptions{Root: rig.root, Dir: rig.dir, Version: "test"})
	h.tick() // 06:00 on a fresh database: the 02:00 slot has not been served
	if got := rig.names(t); !eq(got, "snapshot-20260504-060000") {
		t.Fatalf("after the first tick: %v", got)
	}
	// It is the verified backup `applysmith backup create` writes.
	m, err := store.VerifyBackup(context.Background(), filepath.Join(rig.dir, "snapshot-20260504-060000"))
	if err != nil || m.ProductVersion != "test" || len(m.Files) != 1 || m.Files[0].Path != "cv.md" {
		t.Fatalf("manifest %+v, %v", m, err)
	}
	h.tick()
	if len(rig.names(t)) != 1 {
		t.Fatalf("a second snapshot in one slot: %v", rig.names(t))
	}
	h.clock.Suspend(19*time.Hour + 30*time.Minute) // 01:30 next day
	h.tick()
	if len(rig.names(t)) != 1 {
		t.Fatalf("a snapshot before 02:00: %v", rig.names(t))
	}
	h.clock.Advance(40 * time.Minute) // 02:10
	h.tick()
	if got := rig.names(t); !eq(got, "snapshot-20260504-060000", "snapshot-20260505-021000") {
		t.Fatalf("after 02:00: %v", got)
	}
	h.tick()
	if len(rig.names(t)) != 2 {
		t.Fatalf("two snapshots in one slot: %v", rig.names(t))
	}
}

func TestScheduler_Timers_SnapshotCatchesUpAfterSleepingThrough0200(t *testing.T) {
	h := newHarness(t)
	rig := newSnapshotRig(t, h)
	h.start()
	h.s.AddSnapshots(SnapshotOptions{Root: rig.root, Dir: rig.dir, Version: "test"})
	h.tick()
	h.clock.Suspend(30 * time.Hour) // slept through 02:00
	h.tick()
	if got := rig.names(t); len(got) != 2 {
		t.Fatalf("the missed slot was not served on wake: %v", got)
	}
}

func TestScheduler_Timers_SnapshotKeepsABoundedNumberOfItsOwn(t *testing.T) {
	h := newHarness(t)
	rig := newSnapshotRig(t, h)
	h.start()
	h.s.AddSnapshots(SnapshotOptions{Root: rig.root, Dir: rig.dir, Version: "test", Keep: 3})
	// Things the user put there are never touched: a note, a lookalike with no
	// manifest, and a snapshot-shaped name that is a file.
	if err := os.MkdirAll(filepath.Join(rig.dir, "snapshot-notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rig.dir, "snapshot-20200101-000000"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rig.dir, "snapshot-20190101-000000"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		h.tick()
		h.clock.Suspend(24 * time.Hour)
	}
	var snaps []string
	for _, n := range rig.names(t) {
		if strings.HasPrefix(n, "snapshot-2026") {
			snaps = append(snaps, n)
		}
	}
	if !eq(snaps, "snapshot-20260507-060000", "snapshot-20260508-060000", "snapshot-20260509-060000") {
		t.Fatalf("kept %v: the oldest go first", snaps)
	}
	for _, n := range []string{"snapshot-notes", "snapshot-20200101-000000", "snapshot-20190101-000000"} {
		if _, err := os.Stat(filepath.Join(rig.dir, n)); err != nil {
			t.Errorf("%s was removed: %v", n, err)
		}
	}
}

func TestScheduler_Timers_SnapshotSizeBudgetKeepsTwoRecoveryPoints(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"snapshot-20260501-020000", "snapshot-20260502-020000", "snapshot-20260503-020000", "snapshot-20260504-020000"} {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o700); err != nil {
			t.Fatal(err)
		}
		for f, size := range map[string]int{"manifest.json": 10, "applysmith.db": 1000} {
			if err := os.WriteFile(filepath.Join(dir, n, f), make([]byte, size), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Each is 1,010 bytes; a budget of 1 would remove them all but two.
	removed, err := PruneSnapshots(dir, SnapshotKeep, 1)
	if err != nil || removed != 2 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshot-20260503-020000")); err != nil {
		t.Fatalf("the newer two must stay: %v", err)
	}
	if removed, err = PruneSnapshots(filepath.Join(dir, "nothing"), 3, 1); err != nil || removed != 0 {
		t.Fatalf("a missing directory: %d, %v", removed, err)
	}
}

func TestScheduler_Timers_SnapshotFailureIsRecordedAndRetriedAfterAnHour(t *testing.T) {
	h := newHarness(t)
	rig := newSnapshotRig(t, h)
	h.start()
	tm := &snapshotTimer{s: h.s, o: SnapshotOptions{Root: rig.root, Dir: filepath.Join(rig.root, "inside"), Version: "test", Keep: SnapshotKeep, Budget: SnapshotBudget, Hour: SnapshotHour}}
	h.s.AddTimer(tm)
	state := func() snapshotState {
		st, err := tm.load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	h.tick() // the directory lies inside the workspace: refused
	if st := state(); st.Error == "" || st.Slot != "" || st.Retry == "" {
		t.Fatalf("state after a failure: %+v", st)
	}
	if got := rig.names(t); len(got) != 0 {
		t.Fatalf("a failed snapshot left %v", got)
	}
	tm.o.Dir = rig.dir
	h.clock.Advance(30 * time.Minute)
	h.tick()
	if st := state(); st.Slot != "" || len(rig.names(t)) != 0 {
		t.Fatalf("retried before the hour: %+v", st)
	}
	h.clock.Advance(31 * time.Minute)
	h.tick()
	if st := state(); st.Error != "" || st.Slot == "" || st.Path == "" || len(rig.names(t)) != 1 {
		t.Fatalf("state after the retry: %+v, %v", st, rig.names(t))
	}
}

func TestScheduler_Timers_ALongTimerDeclaresItsLimit(t *testing.T) {
	h := newHarness(t)
	h.start()
	got := make(chan time.Duration, 1)
	h.s.AddTimer(&limitedTimer{fn: func(ctx context.Context) {
		d, _ := ctx.Deadline()
		got <- time.Until(d)
	}})
	h.tick()
	if left := <-got; left < 5*time.Minute {
		t.Fatalf("the timer's own limit was not used: %s left", left)
	}
	if (&snapshotTimer{}).Limit() != snapshotLimit || snapshotLimit <= timerLimit {
		t.Fatal("the snapshot timer must outlast the default limit")
	}
}

type limitedTimer struct{ fn func(context.Context) }

func (*limitedTimer) Name() string                                  { return "limited" }
func (*limitedTimer) Describe() string                              { return "has its own limit" }
func (*limitedTimer) Status(context.Context, time.Time) TimerStatus { return TimerStatus{Active: true} }
func (*limitedTimer) Limit() time.Duration                          { return 10 * time.Minute }
func (l *limitedTimer) Tick(ctx context.Context, _ time.Time) error { l.fn(ctx); return nil }

func TestScheduler_Stop_SettlesQueuedRuns(t *testing.T) {
	h := newHarness(t)
	started := make(chan struct{}, 3)
	h.kind("test.shutdown", nil, false, func(ctx context.Context, _ Invocation) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	h.start()
	var queued string
	for n := 0; n < 4; n++ {
		sc := h.add(fmt.Sprintf("queued-review-%d", n), "test.shutdown", dailyAt("23:00"), PolicySkip)
		id, err := h.s.RunNow(context.Background(), sc.ID)
		if err != nil {
			t.Fatal(err)
		}
		queued = id
	}
	for n := 0; n < 3; n++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	h.s.Stop(context.Background())
	got, err := h.st.GetRun(context.Background(), queued)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.FailureReason != "interrupted" {
		t.Fatalf("queued run survived graceful shutdown: %+v", got)
	}
}

func TestScheduler_RunNowAdmissionCannotOutliveStop(t *testing.T) {
	h := newHarness(t)
	h.kind("test.after-stop", nil, false, nil)
	h.start()
	sc := h.add("after-stop-review", "test.after-stop", dailyAt("23:00"), PolicySkip)
	h.s.admitMu.Lock()
	locked := true
	defer func() {
		if locked {
			h.s.admitMu.Unlock()
		}
	}()
	type result struct {
		id  string
		err error
	}
	resultCh := make(chan result, 1)
	go func() { id, err := h.s.RunNow(context.Background(), sc.ID); resultCh <- result{id, err} }()
	deadline := time.Now().Add(time.Second)
	for {
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		if strings.Contains(string(stack[:n]), "(*Scheduler).RunNow") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunNow did not reach admission")
		}
		runtime.Gosched()
	}
	stopped := make(chan struct{})
	go func() { h.s.Stop(context.Background()); close(stopped) }()
	// A correct admission barrier keeps Stop blocked until the in-flight admission exits.
	select {
	case <-stopped:
		t.Fatal("Stop returned while an admission held the admission lock")
	case <-time.After(50 * time.Millisecond):
	}
	h.s.admitMu.Unlock()
	locked = false
	res := <-resultCh
	<-stopped
	if res.err == nil {
		got, err := h.st.GetRun(context.Background(), res.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "queued" || got.Status == "running" {
			t.Fatalf("admission outlived stop: %+v", got)
		}
	}
}

func TestScheduler_ForcedInterruptedRunCannotPublishSuccessLater(t *testing.T) {
	old := stopWait
	stopWait = time.Millisecond
	defer func() { stopWait = old }()
	h := newHarness(t)
	finishes := make(chan Finish, 4)
	started := make(chan struct{})
	release := make(chan struct{})
	h.kind("test.late-stream", nil, false, func(context.Context, Invocation) (string, error) {
		close(started)
		<-release
		return "finished after forced stop", nil
	})
	h.start()
	h.s.o.Ended = func(f Finish) { finishes <- f }
	sc := h.add("late-stream-review", "test.late-stream", dailyAt("23:00"), PolicySkip)
	id, err := h.s.RunNow(context.Background(), sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	h.s.Stop(context.Background())
	stored, err := h.st.GetRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || stored.FailureReason != ReasonInterrupted {
		t.Fatalf("forced stop not settled: %+v", stored)
	}
	close(release)
	h.s.Wait()
	close(finishes)
	for f := range finishes {
		if f.Status == "succeeded" {
			t.Fatalf("durably interrupted run published success to stream: %+v", f)
		}
	}
}

func TestScheduler_TryLeases_ActiveTimerSurvivesClockGap(t *testing.T) {
	h := newHarness(t)
	s := h.scheduler("sched_a")
	ctx := context.Background()
	release, ok, err := s.TryLeases(ctx, "timer", LeaseEvaluation)
	if err != nil || !ok {
		t.Fatalf("timer take: ok=%v err=%v", ok, err)
	}
	defer release()
	h.clock.Suspend(2 * leaseTTL)
	if busy, err := s.leases.free(ctx, []Lease{LeaseEvaluation}); err != nil || busy == nil || busy.Holder != "timer" {
		t.Fatalf("expired timer admission: %+v %v", busy, err)
	}
	// Disjoint work still runs and cannot sweep the live timer's expired row.
	other, ok, err := s.TryLeases(ctx, "other", LeaseTracker)
	if err != nil || !ok {
		t.Fatalf("disjoint work: ok=%v err=%v", ok, err)
	}
	other()
	if _, ok, err := s.TryLeases(ctx, "competing", LeaseEvaluation); err != nil || ok {
		t.Fatalf("overlapping timer: ok=%v err=%v", ok, err)
	}
	release()
	last, ok, err := s.TryLeases(ctx, "competing", LeaseEvaluation)
	if err != nil || !ok {
		t.Fatalf("after timer release: ok=%v err=%v", ok, err)
	}
	last()
}

func TestScheduler_TryLeases_ReleaseOnlyEndsItsOwnAcquisition(t *testing.T) {
	h := newHarness(t)
	s := h.scheduler("sched_a")
	ctx := context.Background()
	first, ok, err := s.TryLeases(ctx, "timer", LeaseEvaluation)
	if err != nil || !ok {
		t.Fatalf("first: ok=%v err=%v", ok, err)
	}
	defer first()
	empty, ok, err := s.TryLeases(ctx, "timer")
	if err != nil || !ok {
		t.Fatalf("empty acquisition: ok=%v err=%v", ok, err)
	}
	empty() // acquiring no resources cannot release an existing holder
	if busy, err := s.leases.free(ctx, []Lease{LeaseEvaluation}); err != nil || busy == nil {
		t.Fatalf("empty acquisition released the holder: %+v %v", busy, err)
	}
	if _, ok, err := s.TryLeases(ctx, "timer", LeaseTracker); err == nil || ok {
		t.Fatalf("duplicate holder acquired another resource: ok=%v err=%v", ok, err)
	}
	first()
	second, ok, err := s.TryLeases(ctx, "timer", LeaseEvaluation)
	if err != nil || !ok {
		t.Fatalf("second: ok=%v err=%v", ok, err)
	}
	defer second()
	first() // a stale callback cannot release the timer's new acquisition
	h.clock.Suspend(2 * leaseTTL)
	if _, ok, err := s.TryLeases(ctx, "competing", LeaseEvaluation); err != nil || ok {
		t.Fatalf("stale callback ended the new acquisition: ok=%v err=%v", ok, err)
	}
}
