package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestScheduler_Exactly_DoubleEvaluationStartsOneJob(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.step(time.Hour) // 07:00:00
	h.tick()
	h.tick()
	if n := h.called("test.fast"); n != 1 {
		t.Fatalf("job ran %d times", n)
	}
	if d := h.decisions(sc.ID); !eq(d, "fired") {
		t.Fatalf("decisions %v", d)
	}
	// The claim itself refuses a second decision of the same occurrence.
	cur := h.get(sc.ID)
	_, err := h.s.Repo().ClaimRun(context.Background(), Claim{Schedule: cur, ScheduledFor: time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)})
	if !errors.Is(err, ErrDecided) {
		t.Fatalf("second claim: %v", err)
	}
}

func TestScheduler_Exactly_CrashBetweenClaimAndLaunch(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	s1 := h.scheduler("sched_a")
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	occ := time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)
	// The process stops after the run row is inserted and before the job starts.
	runID, err := s1.Repo().ClaimRun(context.Background(), Claim{Schedule: h.get(sc.ID), ScheduledFor: occ})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour + 5*time.Second)
	s2 := h.scheduler("sched_b")
	if err := s2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s2.Stop(context.Background())
	s2.Tick(context.Background())
	s2.Wait()
	run, err := h.st.GetRun(context.Background(), runID)
	if err != nil || run.Status != "failed" || run.FailureReason != "interrupted" {
		t.Fatalf("run = %+v %v", run, err)
	}
	if n := h.called("test.fast"); n != 0 {
		t.Fatalf("the interrupted occurrence ran %d times", n)
	}
	if d := h.decisions(sc.ID); !eq(d, "interrupted/interrupted") {
		t.Fatalf("decisions %v", d)
	}
}

func TestScheduler_Exactly_OnceAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicyFireOnce)
	h.step(time.Hour)
	h.s.Stop(context.Background())
	// A new process on the same database, a few seconds later.
	h.clock.Advance(3 * time.Second)
	s2 := h.scheduler("sched_b")
	if err := s2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s2.Stop(context.Background())
	s2.Tick(context.Background())
	s2.Wait()
	if n := h.called("test.fast"); n != 1 {
		t.Fatalf("the occurrence ran %d times across the restart", n)
	}
	if d := h.decisions(sc.ID); !eq(d, "fired") {
		t.Fatalf("decisions %v", d)
	}
}

func TestScheduler_Missed_SkipPolicyAfterSleep(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.tick()
	h.clock.Suspend(4*time.Hour + 30*time.Minute) // 10:30; 07, 08, 09 and 10 fell due
	h.tick()
	want := []string{"skipped/host-asleep", "skipped/host-asleep", "skipped/host-asleep", "skipped/host-asleep"}
	if d := h.decisions(sc.ID); !eq(d, want...) || h.called("test.fast") != 0 {
		t.Fatalf("decisions %v, calls %d", d, h.called("test.fast"))
	}
	if got := h.get(sc.ID).LastFiredAt; !got.Equal(time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("last_fired_at = %s", got)
	}
}

func TestScheduler_Missed_FireOnce(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicyFireOnce)
	h.tick()
	h.clock.Suspend(3*time.Hour + 30*time.Minute) // 09:30; 07, 08 and 09 fell due
	h.tick()
	want := []string{"skipped/host-asleep", "skipped/host-asleep", "fired"}
	if d := h.decisions(sc.ID); !eq(d, want...) || h.called("test.fast") != 1 {
		t.Fatalf("decisions %v, calls %d", d, h.called("test.fast"))
	}
	rows := h.history(sc.ID)
	if !strings.Contains(rows[0].Detail, "gap=") {
		t.Fatalf("the fire does not name the gap: %q", rows[0].Detail)
	}
}

func TestScheduler_Missed_FireAllCapsAt24(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", Preset{Kind: PresetRRule, RRule: "FREQ=MINUTELY;INTERVAL=10", Timezone: "UTC"}, PolicyFireAll)
	h.tick()
	h.clock.Suspend(5*time.Hour + 5*time.Minute) // 30 occurrences
	h.tick()
	h.s.Wait()
	d := h.decisions(sc.ID)
	var fired, skipped int
	for _, x := range d {
		switch x {
		case "fired":
			fired++
		case "skipped/policy":
			skipped++
		}
	}
	if fired != 24 || skipped != 6 || len(d) != 30 {
		t.Fatalf("fired %d skipped %d of %d: %v", fired, skipped, len(d), d)
	}
	rows := h.history(sc.ID)
	var firedFor []time.Time
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Outcome == OutcomeFired {
			firedFor = append(firedFor, rows[i].ScheduledFor)
		}
	}
	for i := 1; i < len(firedFor); i++ {
		if !firedFor[i].After(firedFor[i-1]) {
			t.Fatalf("replays out of order: %v", firedFor)
		}
	}
	if got := h.get(sc.ID); got.ActiveRunID != "" || got.LastResult != ResultSkipped {
		t.Fatalf("after the replay: active %q result %q", got.ActiveRunID, got.LastResult)
	}
}

func TestScheduler_Missed_StarvedProcessReadsHostStarved(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.tick()
	h.clock.Advance(100 * time.Minute) // ticks stopped for 100 minutes while the monotonic clock ran
	h.tick()
	if d := h.decisions(sc.ID); !eq(d, "skipped/host-starved") {
		t.Fatalf("decisions %v", d)
	}
}

func TestScheduler_Missed_ReEnabledScheduleReplaysNothing(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicyFireAll)
	off := false
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Hour)
	h.clock.Suspend(46 * time.Hour)
	h.tick()
	on := true
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if d := h.decisions(sc.ID); len(d) != 0 || h.called("test.fast") != 0 {
		t.Fatalf("decisions %v, calls %d", d, h.called("test.fast"))
	}
	if got := h.get(sc.ID).LastFiredAt; !got.Equal(h.clock.Now()) {
		t.Fatalf("enabling set last_fired_at to %s, want %s", got, h.clock.Now())
	}
}

func TestScheduler_Recurrence_EndedRuleDisablesSchedule(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc, err := h.mgr.Create(context.Background(), NewSchedule{Name: "a", Kind: "test.fast", Policy: PolicyFireOnce,
		Preset:  Preset{Kind: PresetRRule, RRule: "FREQ=HOURLY;COUNT=3;BYMINUTE=0;BYSECOND=0", Timezone: "UTC"},
		DTStart: baseTime.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	h.step(4 * time.Hour)
	got := h.get(sc.ID)
	if got.Enabled {
		t.Fatal("the schedule is still enabled after its last occurrence")
	}
	if h.called("test.fast") != 3 {
		t.Fatalf("job ran %d times", h.called("test.fast"))
	}
	var ended bool
	for _, r := range h.history(sc.ID) {
		ended = ended || r.Outcome == OutcomeEnded
	}
	if !ended {
		t.Fatalf("history does not say why: %+v", h.history(sc.ID))
	}
}

func TestScheduler_Tick_BadPresetDoesNotBreakOtherSchedules(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	bad := h.add("bad", "test.fast", hourly(), PolicySkip)
	good := h.add("good", "test.fast", hourly(), PolicySkip)
	h.corruptPreset(bad.ID)
	h.step(time.Hour)
	if d := h.decisions(good.ID); !eq(d, "fired") {
		t.Fatalf("good schedule: %v", d)
	}
}

func TestScheduler_Tick_PicksUpEditWithin60s(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", dailyAt("20:00"), PolicySkip)
	p := dailyAt("06:01")
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Preset: &p}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(61 * time.Second)
	h.tick()
	if h.called("test.fast") != 1 {
		t.Fatalf("the edited time did not fire; calls %d", h.called("test.fast"))
	}
}

// Editing a schedule's time to one already past today starts nothing: the
// first occurrence of the edited recurrence is the next one after the edit.
func TestScheduler_Tick_AnEditToATimeAlreadyPastWaitsForItsNextOccurrence(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", dailyAt("20:00"), PolicyFireOnce)
	h.stepBy(6*time.Hour, 5*time.Minute) // 12:00
	p := dailyAt("09:00")
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Preset: &p}); err != nil {
		t.Fatal(err)
	}
	h.stepBy(10*time.Minute, time.Minute)
	if d := h.decisions(sc.ID); len(d) != 0 || h.called("test.fast") != 0 {
		t.Fatalf("09:00 was past when the schedule was edited at 12:00, and it ran at once: %v", d)
	}
	h.stepBy(21*time.Hour, 5*time.Minute) // 09:10 the next day
	if d := h.decisions(sc.ID); !eq(d, "fired") {
		t.Fatalf("the edited time did not fire the next day: %v", d)
	}
	// An edit that leaves the recurrence alone leaves the cursor too.
	before := h.get(sc.ID).LastFiredAt
	name := "renamed"
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if got := h.get(sc.ID).LastFiredAt; !got.Equal(before) {
		t.Fatalf("renaming moved the cursor from %s to %s", before, got)
	}
}
