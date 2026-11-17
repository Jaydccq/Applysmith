package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduler_Tick_GraceCountsAsDue(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", dailyAt("07:00"), PolicySkip)
	h.stepBy(55*time.Minute, 5*time.Minute)                                // 06:55
	h.clock.Advance(4*time.Minute + 59*time.Second + 400*time.Millisecond) // 06:59:59.4, 600 ms early
	h.tick()
	if h.called("test.fast") != 0 {
		t.Fatal("an occurrence 600 ms away was already due")
	}
	h.clock.Advance(300 * time.Millisecond) // 06:59:59.7, 300 ms early
	h.tick()
	if h.called("test.fast") != 1 {
		t.Fatalf("an occurrence 300 ms away did not count as due; calls %d", h.called("test.fast"))
	}
	if got := h.get(sc.ID).LastFiredAt; !got.Equal(time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("the decision is recorded at %s, not at the scheduled instant", got)
	}
}

func TestScheduler_Tick_LongJobNeverDelaysTick(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.block", nil, false, g.job())
	h.kind("test.fast", nil, false, nil)
	h.start()
	h.add("slow", "test.block", dailyAt("07:00"), PolicySkip)
	fast := h.add("fast", "test.fast", dailyAt("07:30"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 1)
	done := make(chan struct{})
	go func() {
		h.stepBy(30*time.Minute, 5*time.Minute) // ticks while the first job still runs
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a tick waited for the running job")
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.called("test.fast") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.called("test.fast") != 1 || !eq(h.decisions(fast.ID), "fired") {
		t.Fatalf("the second schedule did not fire beside the long job: calls %d", h.called("test.fast"))
	}
	close(g.release)
	h.s.Wait()
}

func TestScheduler_Admission_OrderedByScheduledInstant(t *testing.T) {
	h := newHarness(t)
	h.async = true
	var mu sync.Mutex
	gates := map[string]chan struct{}{}
	started := make(chan string, 16)
	gateOf := func(name string) chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		if gates[name] == nil {
			gates[name] = make(chan struct{})
		}
		return gates[name]
	}
	h.kind("test.hold", nil, false, func(ctx context.Context, in Invocation) (string, error) {
		started <- in.Schedule
		select {
		case <-gateOf(in.Schedule):
		case <-ctx.Done():
		}
		return "", nil
	})
	h.start()
	for _, n := range []string{"x1", "x2", "x3"} {
		h.add(n, "test.hold", dailyAt("07:00"), PolicySkip)
	}
	// Created in id order a, b, c; due in the order b, c, a.
	h.add("a", "test.hold", dailyAt("07:04"), PolicySkip)
	h.add("b", "test.hold", dailyAt("07:01"), PolicySkip)
	h.add("c", "test.hold", dailyAt("07:03"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute) // 07:00: the three slots fill
	for range 3 {
		<-started
	}
	h.stepBy(5*time.Minute, 5*time.Minute) // 07:05: a, b and c fall due in one tick and wait
	h.s.runMu.Lock()
	waiting := len(h.s.queue)
	h.s.runMu.Unlock()
	if waiting != 3 {
		t.Fatalf("%d jobs wait, want 3", waiting)
	}
	var order []string
	for _, free := range []string{"x1", "x2", "x3"} {
		close(gateOf(free))
		select {
		case n := <-started:
			order = append(order, n)
		case <-time.After(5 * time.Second):
			t.Fatalf("no waiting job started after %s ended; order so far %v", free, order)
		}
	}
	if strings.Join(order, "") != "bca" {
		t.Fatalf("waiting jobs started in the order %v, want b c a (by scheduled instant)", order)
	}
	for _, n := range []string{"a", "b", "c"} {
		close(gateOf(n))
	}
	h.s.Wait()
}

func TestScheduler_Recovery_RunsOnGapOver5Minutes(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	// Under the skip policy only a recovery pass skips; an ordinary tick fires.
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.stepBy(55*time.Minute, 5*time.Minute) // 06:55
	h.stepBy(5*time.Minute, 5*time.Minute)  // 07:00, exactly five minutes after the last tick
	if d := h.decisions(sc.ID); !eq(d, "fired") {
		t.Fatalf("a five-minute tick was read as a gap: %v", d)
	}
	h.stepBy(115*time.Minute, 5*time.Minute) // 08:55 (08:00 fires)
	h.clock.Advance(5*time.Minute + time.Second)
	h.tick() // 09:00:01, five minutes and a second after the last tick
	if d := h.decisions(sc.ID); !eq(d, "fired", "fired", "skipped/host-starved") {
		t.Fatalf("a gap over five minutes did not run the recovery pass: %v", d)
	}
}

// Under the skip policy a recovery pass skips what fell due before it. An
// occurrence inside the grace ahead of the pass was not missed: it fires, as
// on any tick.
func TestScheduler_Recovery_AnOccurrenceAheadOfTheGapIsNotMissed(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.stepBy(50*time.Minute, 5*time.Minute) // 06:50
	h.clock.Advance(129*time.Minute + 59*time.Second + 700*time.Millisecond)
	h.tick() // 08:59:59.7 after a gap: 07:00 and 08:00 were missed, 09:00 is 300 ms ahead
	if d := h.decisions(sc.ID); !eq(d, "skipped/host-starved", "skipped/host-starved", "fired") {
		t.Fatalf("decisions %v", d)
	}
	// The same at a start: the server comes up 300 ms before the hour.
	h2 := newHarness(t)
	h2.kind("test.fast", nil, false, nil)
	s2 := h2.add("b", "test.fast", hourly(), PolicySkip)
	h2.clock.Advance(59*time.Minute + 59*time.Second + 700*time.Millisecond)
	h2.start()
	h2.s.Wait()
	if d := h2.decisions(s2.ID); !eq(d, "fired") {
		t.Fatalf("decisions at a start %v", d)
	}
}

func TestScheduler_Start_StopDuringStartNeverTicks(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.add("a", "test.fast", hourly(), PolicySkip)
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var waits atomic.Int32
	s := h.scheduler("sched_a", func(o *Options) {
		o.LockHeld = func() bool {
			once.Do(func() { close(entered); <-resume })
			return true
		}
		o.Wait = func(ctx context.Context, _ time.Duration) error {
			waits.Add(1)
			<-ctx.Done()
			return ctx.Err()
		}
	})
	started, stopped := make(chan error, 1), make(chan struct{})
	go func() { started <- s.Start(context.Background()) }()
	<-entered // the first tick of Start is running
	go func() { s.Stop(context.Background()); close(stopped) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		down := s.stopped
		s.mu.Unlock()
		if down {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(resume)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	<-stopped
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	loop := s.loopDone
	s.mu.Unlock()
	if loop != nil || waits.Load() != 0 {
		t.Fatalf("the tick loop started after the stop (loop %v, waits %d)", loop != nil, waits.Load())
	}
}

func TestScheduler_Claim_EditBetweenSnapshotAndClaim(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	occ := time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)
	snap := h.get(sc.ID)
	h.clock.Advance(time.Minute) // the edit carries a later stamp than the snapshot
	name := "renamed"
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute) // the edit has a later stamp than the snapshot
	if _, err := h.s.Repo().ClaimRun(context.Background(), Claim{Schedule: snap, ScheduledFor: occ}); !errors.Is(err, errStaleSchedule) {
		t.Fatalf("claim of an edited schedule: %v", err)
	}
	if res, _, _ := h.s.tryFire(context.Background(), snap, occ, ""); res != unchanged {
		t.Fatalf("firing from the stale snapshot gave %v", res)
	}
	if rows := h.history(sc.ID); len(rows) != 0 || !h.get(sc.ID).LastFiredAt.Equal(snap.LastFiredAt) {
		t.Fatalf("the refused claim left a trace: %+v", rows)
	}
	// Neither decided twice nor lost: the next tick reads the schedule again and fires it.
	h.stepBy(time.Hour, 5*time.Minute)
	if d := h.decisions(sc.ID); !eq(d, "fired") {
		t.Fatalf("the occurrence was lost: %v", d)
	}
	// A schedule deleted after the snapshot is not claimed either.
	snap = h.get(sc.ID)
	h.s.Wait()
	if err := h.mgr.Delete(context.Background(), sc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Repo().ClaimRun(context.Background(), Claim{Schedule: snap, ScheduledFor: occ.Add(2 * time.Hour)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim of a deleted schedule: %v", err)
	}
}

func TestScheduler_Claim_UniqueKeyScheduleAndInstant(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	s1 := h.scheduler("sched_a")
	a := h.add("a", "test.fast", hourly(), PolicySkip)
	b := h.add("b", "test.fast", hourly(), PolicySkip)
	occ := time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)
	runID, err := s1.Repo().ClaimRun(context.Background(), Claim{Schedule: h.get(a.ID), ScheduledFor: occ})
	if err != nil {
		t.Fatal(err)
	}
	// The claim wrote the run, the history row and the cursor in one step,
	// before any job started.
	got := h.get(a.ID)
	rows := h.history(a.ID)
	if !got.LastFiredAt.Equal(occ) || got.ActiveRunID != runID || got.LastResult != ResultRunning || len(rows) != 1 || rows[0].RunID != runID {
		t.Fatalf("after the claim: %+v %+v", got, rows)
	}
	insert := func(id string, schedule int64, at time.Time) error {
		return h.st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO runs (id, kind, status, schedule_id, scheduled_for, created_at, updated_at) VALUES (?, 'test.fast', 'queued', ?, ?, ?, ?)`,
				id, schedule, ts(at), ts(h.clock.Now()), ts(h.clock.Now()))
			return err
		})
	}
	if err := insert("run_duplicate", a.ID, occ); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("a second run for the same schedule and instant was accepted: %v", err)
	}
	if err := insert("run_other_schedule", b.ID, occ); err != nil {
		t.Fatalf("the same instant on another schedule was refused: %v", err)
	}
	if err := insert("run_other_instant", a.ID, occ.Add(time.Hour)); err != nil {
		t.Fatalf("another instant of the same schedule was refused: %v", err)
	}
}

func TestScheduler_Recovery_LiveJobNeverInterrupted(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.block", []Lease{LeaseTracker}, false, g.job())
	h.start()
	sc := h.add("a", "test.block", dailyAt("07:00"), PolicyFireOnce)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 1)
	runID := h.get(sc.ID).ActiveRunID
	h.clock.Suspend(3 * time.Hour) // a gap: the recovery pass runs while the job is alive
	h.tick()
	run, err := h.st.GetRun(context.Background(), runID)
	if err != nil || run.Status != "running" || run.FailureReason != "" {
		t.Fatalf("the recovery pass touched a live run: %+v %v", run, err)
	}
	if got := h.get(sc.ID); got.ActiveRunID != runID || got.LastResult != ResultRunning {
		t.Fatalf("schedule after the pass: %+v", got)
	}
	close(g.release)
	h.s.Wait()
	if d := h.decisions(sc.ID); !eq(d, "fired") || h.get(sc.ID).LastResult != ResultOK {
		t.Fatalf("decisions %v result %q", d, h.get(sc.ID).LastResult)
	}
}

func TestScheduler_Recovery_LookbackLimits(t *testing.T) {
	t.Run("seven days", func(t *testing.T) {
		h := newHarness(t)
		h.kind("test.fast", nil, false, nil)
		h.start()
		sc := h.add("a", "test.fast", hourly(), PolicySkip)
		h.tick()
		h.clock.Suspend(10 * 24 * time.Hour)
		h.tick()
		rows := h.history(sc.ID)
		floor := h.clock.Now().Add(-lookback)
		if len(rows) < 167 || len(rows) > 169 {
			t.Fatalf("%d occurrences were decided after ten days, want the seven days (168)", len(rows))
		}
		for _, r := range rows {
			if r.ScheduledFor.Before(floor) {
				t.Fatalf("an occurrence older than seven days was decided: %s", r.ScheduledFor)
			}
		}
	})
	t.Run("thousand occurrences", func(t *testing.T) {
		h := newHarness(t)
		h.kind("test.fast", nil, false, nil)
		h.start()
		sc, err := h.mgr.Create(context.Background(), NewSchedule{Name: "often", Kind: "test.fast", Policy: PolicySkip,
			Preset: Preset{Kind: PresetRRule, RRule: "FREQ=MINUTELY;INTERVAL=5", Timezone: "UTC"}})
		if err != nil {
			t.Fatal(err)
		}
		h.tick()
		h.clock.Suspend(7 * 24 * time.Hour)
		h.tick()
		rows, err := h.mgr.Repo().History(context.Background(), sc.ID, 5000)
		if err != nil || len(rows) != lookbackMax {
			t.Fatalf("%d occurrences decided (%v), want %d", len(rows), err, lookbackMax)
		}
		// The newest thousand are kept; the oldest of them is about 83 hours back.
		oldest := rows[len(rows)-1].ScheduledFor
		if want := h.clock.Now().Add(-time.Duration(lookbackMax+1) * 5 * time.Minute); oldest.Before(want) {
			t.Fatalf("the oldest decided occurrence is %s, before %s", oldest, want)
		}
	})
}
