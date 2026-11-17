package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestScheduler_Deferral_EndsAtSixHoursOrNextOccurrence(t *testing.T) {
	t.Run("six hours", func(t *testing.T) {
		h := newHarness(t)
		h.kind("test.net", nil, true, nil)
		h.start()
		h.online = false
		sc := h.add("a", "test.net", dailyAt("07:00"), PolicySkip)
		h.stepBy(6*time.Hour+55*time.Minute, 5*time.Minute) // 12:55
		if d := h.decisions(sc.ID); len(d) != 0 {
			t.Fatalf("decided before the limit: %v", d)
		}
		h.stepBy(5*time.Minute, 5*time.Minute) // 13:00
		if d := h.decisions(sc.ID); !eq(d, "skipped/offline") {
			t.Fatalf("after six hours: %v", d)
		}
	})
	t.Run("next occurrence", func(t *testing.T) {
		h := newHarness(t)
		h.kind("test.net", nil, true, nil)
		h.start()
		h.online = false
		sc := h.add("a", "test.net", hourly(), PolicySkip)
		h.stepBy(2*time.Hour+30*time.Minute, 5*time.Minute) // 08:30: 07:00 was superseded by 08:00
		if d := h.decisions(sc.ID); !eq(d, "skipped/offline") {
			t.Fatalf("decisions %v", d)
		}
		h.online = true
		h.stepBy(5*time.Minute, 5*time.Minute)
		if d := h.decisions(sc.ID); !eq(d, "skipped/offline", "fired") {
			t.Fatalf("decisions after the connection returned: %v", d)
		}
	})
}

func TestScheduler_Admission_LaunchFailureRecordedError(t *testing.T) {
	h := newHarness(t)
	h.kind("test.broken", nil, false, func(context.Context, Invocation) (string, error) {
		return "", errors.New("the scanner could not start: no browser window")
	})
	h.start()
	sc := h.add("a", "test.broken", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	rows := h.history(sc.ID)
	if len(rows) != 1 || rows[0].Outcome != OutcomeFired || rows[0].Reason != ReasonError || !strings.Contains(rows[0].Detail, "no browser window") || rows[0].Attempts != 1 {
		t.Fatalf("history %+v", rows)
	}
	got := h.get(sc.ID)
	if got.LastResult != ResultFailed || got.ActiveRunID != "" || got.LastReason != ReasonError || !strings.Contains(got.LastDetail, "no browser window") {
		t.Fatalf("schedule %+v", got)
	}
	run, err := h.st.GetRun(context.Background(), rows[0].RunID)
	if err != nil || run.Status != "failed" || run.FailureReason != ReasonError {
		t.Fatalf("run %+v %v", run, err)
	}
}

func TestScheduler_Result_ReasonIsTheError(t *testing.T) {
	h := newHarness(t)
	h.kind("test.board", []Lease{LeaseBrowser}, true, func(context.Context, Invocation) (string, error) {
		// The job names what failed; a footer line it also printed is not the reason.
		return "", NeedsYou(ReasonNeedsSignIn, errors.New("the example board shows a sign-in page\nScan run summary: /tmp/example"))
	})
	h.kind("test.plain", nil, false, func(context.Context, Invocation) (string, error) {
		return "", fmt.Errorf("reading the board: %w", errors.New("unexpected end of the listing"))
	})
	h.start()
	board := h.add("board", "test.board", dailyAt("07:00"), PolicySkip)
	plain := h.add("plain", "test.plain", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	if r := h.history(board.ID)[0]; r.Reason != ReasonNeedsSignIn || !strings.HasPrefix(r.Detail, "the example board shows a sign-in page") {
		t.Errorf("typed failure: %+v", r)
	}
	if r := h.history(plain.ID)[0]; r.Reason != ReasonError || r.Detail != "reading the board: unexpected end of the listing" {
		t.Errorf("plain failure: %+v", r)
	}
	if len(h.needs) != 1 || !strings.HasPrefix(h.needs[0].Message, "the example board shows a sign-in page") {
		t.Errorf("needs-you item: %+v", h.needs)
	}
}

func TestScheduler_Retry_ClassifiesBlockers(t *testing.T) {
	timeout := &net.DNSError{Err: "i/o timeout", IsTimeout: true}
	for _, c := range []struct {
		name   string
		err    error
		class  FailureClass
		reason string
	}{
		{"network error", timeout, FailTransient, ""},
		{"wrapped network error", fmt.Errorf("fetching: %w", timeout), FailTransient, ""},
		{"deadline", context.DeadlineExceeded, FailTransient, ""},
		{"marked transient", Transient(errors.New("provider rate limit")), FailTransient, ""},
		{"sign-in", NeedsYou(ReasonNeedsSignIn, errors.New("login page")), FailNeedsYou, ReasonNeedsSignIn},
		{"checkpoint or challenge", NeedsYou("", errors.New("the board asks to verify you are human")), FailNeedsYou, ReasonNeedsYou},
		{"sign-in", fmt.Errorf("sync: %w", NeedsYou("needs-gmail-sign-in", errors.New("login refused"))), FailNeedsYou, "needs-gmail-sign-in"},
		{"anything else", errors.New("unexpected listing"), FailPermanent, ""},
	} {
		class, reason := classify(c.err)
		if class != c.class || reason != c.reason {
			t.Errorf("%s: class %d reason %q, want %d %q", c.name, class, reason, c.class, c.reason)
		}
	}
	// Transient failures are retried; the others are not, and a blocker that
	// needs the user raises the item the advice text becomes.
	h := newHarness(t)
	for name, err := range map[string]error{"test.challenge": NeedsYou("", errors.New("complete the check in the browser window")),
		"test.plain": errors.New("unexpected listing")} {
		h.kind(name, nil, true, func(context.Context, Invocation) (string, error) { return "", err })
	}
	h.start()
	challenge := h.add("challenge", "test.challenge", dailyAt("07:00"), PolicySkip)
	plain := h.add("plain", "test.plain", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	h.s.Wait()
	for _, id := range []int64{challenge.ID, plain.ID} {
		if r := h.history(id)[0]; r.Attempts != 1 {
			t.Errorf("schedule %d was attempted %d times", id, r.Attempts)
		}
	}
	if len(h.sleeps) != 0 || len(h.needs) != 1 || h.needs[0].Reason != ReasonNeedsYou || !strings.Contains(h.needs[0].Message, "complete the check") {
		t.Errorf("waits %v, needs-you %+v", h.sleeps, h.needs)
	}
}

func TestScheduler_Leases_FixedSetOfFour(t *testing.T) {
	if fmt.Sprint(Leases) != "[browser llm:evaluation tracker:write gmail:read]" {
		t.Fatalf("leases %v", Leases)
	}
	h := newHarness(t)
	for _, name := range []string{"apply", "reports:publish", "metrics:projection", "kb:ingest", "repo:local-branch"} {
		err := h.st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO resource_leases (name, holder, owner_pid, owner_instance, acquired_at, expires_at) VALUES (?, 'x', 1, 'i', ?, ?)`,
				name, ts(baseTime), ts(baseTime.Add(time.Hour)))
			return err
		})
		if err == nil {
			t.Errorf("the store accepted a lease named %q", name)
		}
	}
	noop := func(context.Context, Invocation) (string, error) { return "", nil }
	for _, l := range []Lease{"reports:publish", "kb:ingest", "apply"} {
		if err := NewRegistry().Register(Kind{Name: "x.y", Run: noop, Leases: []Lease{l}}); err == nil {
			t.Errorf("a kind declared the lease %q", l)
		}
	}
}

func TestScheduler_Leases_AllOrNothing(t *testing.T) {
	h := newHarness(t)
	a, b := h.scheduler("sched_a"), h.scheduler("sched_b")
	ctx := context.Background()
	h.exec(`INSERT INTO resource_leases (name, holder, owner_pid, owner_instance, acquired_at, expires_at)
		VALUES ('tracker:write', 'other job', 1, 'sched_b', ?, ?)`, ts(baseTime), ts(baseTime.Add(time.Hour)))
	busy, err := a.leases.acquire(ctx, "wants two", []Lease{LeaseBrowser, LeaseTracker})
	if err != nil || busy == nil || busy.Lease != LeaseTracker || busy.Holder != "other job" {
		t.Fatalf("acquire: %+v %v", busy, err)
	}
	if n := h.leaseRows(); n != 1 {
		t.Fatalf("a refused acquire took %d of the leases it could (rows %d)", n-1, n)
	}
	h.exec(`DELETE FROM resource_leases`)
	if busy, err := a.leases.acquire(ctx, "wants two", []Lease{LeaseBrowser, LeaseTracker}); err != nil || busy != nil || h.leaseRows() != 2 {
		t.Fatalf("free acquire: %+v %v rows %d", busy, err, h.leaseRows())
	}
	// A lease has one holder.
	if busy, _ := b.leases.acquire(ctx, "second", []Lease{LeaseBrowser}); busy == nil || busy.Holder != "wants two" {
		t.Fatalf("a held lease was taken: %+v", busy)
	}
}

func TestScheduler_Registry_CapabilityLeaseSelfCheck(t *testing.T) {
	noop := func(context.Context, Invocation) (string, error) { return "", nil }
	pairs := []struct {
		lease Lease
		does  func(*Drives)
	}{
		{LeaseBrowser, func(d *Drives) { d.ScanWindow = true }},
		{LeaseEvaluation, func(d *Drives) { d.Evaluation = true }},
		{LeaseTracker, func(d *Drives) { d.TrackerWrite = true }},
		{LeaseGmail, func(d *Drives) { d.GmailRead = true }},
	}
	for _, p := range pairs {
		var drives Drives
		p.does(&drives)
		if err := NewRegistry().Register(Kind{Name: "ok.kind", Run: noop, Leases: []Lease{p.lease}, Drives: drives}); err != nil {
			t.Errorf("%s with its capability: %v", p.lease, err)
		}
		if err := NewRegistry().Register(Kind{Name: "no.lease", Run: noop, Drives: drives}); err == nil {
			t.Errorf("the capability of %s without the lease was accepted", p.lease)
		}
		if err := NewRegistry().Register(Kind{Name: "no.cap", Run: noop, Leases: []Lease{p.lease}}); err == nil {
			t.Errorf("the lease %s without its capability was accepted", p.lease)
		}
	}
	// A registry that holds a mismatch refuses to start the scheduler.
	h := newHarness(t)
	h.reg.kinds["bad.kind"] = Kind{Name: "bad.kind", Run: noop, Drives: Drives{GmailRead: true}}
	if _, err := New(Options{Store: h.st, Registry: h.reg, Clock: h.clock}); err == nil {
		t.Fatal("a scheduler started over a registry whose kind breaks the self-check")
	}
}

func TestScheduler_Leases_ReleasedWhenJobReturns(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.hold", []Lease{LeaseTracker}, false, g.job())
	h.start()
	h.add("a", "test.hold", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 1)
	if h.leaseRows() != 1 {
		t.Fatalf("%d lease rows while the job runs", h.leaseRows())
	}
	close(g.release)
	h.s.Wait()
	if h.leaseRows() != 0 {
		t.Fatalf("%d lease rows after the job returned", h.leaseRows())
	}
}

func TestScheduler_History_CompletedWorkNeverFailedByTimeout(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.long", []Lease{LeaseEvaluation}, false, g.job())
	h.start()
	sc := h.add("a", "test.long", dailyAt("07:00"), PolicySkip)
	h.stepBy(time.Hour, 5*time.Minute)
	g.waitStarted(t, 1)
	h.stepBy(10*time.Hour, 5*time.Minute) // far past any scan's budget; the scheduler keeps no watchdog
	if got := h.get(sc.ID); got.LastResult != ResultRunning || h.leaseRows() != 1 {
		t.Fatalf("the long job was ended by the scheduler: %+v (leases %d)", got, h.leaseRows())
	}
	close(g.release)
	h.s.Wait()
	rows := h.history(sc.ID)
	if got := h.get(sc.ID); got.LastResult != ResultOK || rows[0].Reason != "" || rows[0].Summary != "released" {
		t.Fatalf("the job's own result was not kept: %+v %+v", got, rows[0])
	}
}

func TestScheduler_History_RowShape(t *testing.T) {
	h := newHarness(t)
	h.kind("test.count", nil, false, func(context.Context, Invocation) (string, error) { return "3 discovered, 1 new", nil })
	h.start()
	fired := h.add("fired", "test.count", hourly(), PolicySkip)
	skipped := h.add("skipped", "test.count", hourly(), PolicySkip)
	h.exec(`UPDATE schedules SET inputs_json = '{"gone":1}' WHERE id = ?`, skipped.ID)
	manual := h.add("manual", "test.count", dailyAt("23:00"), PolicySkip)
	h.step(time.Hour)
	runID, err := h.s.RunNow(context.Background(), manual.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	at := time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)
	f := h.history(fired.ID)[0]
	if f.ScheduleID != fired.ID || !f.ScheduledFor.Equal(at) || f.Outcome != OutcomeFired || f.Reason != "" || f.RunID == "" ||
		f.StartedAt.IsZero() || f.FinishedAt.IsZero() || f.Attempts != 1 || f.Summary != "3 discovered, 1 new" || f.CreatedAt.IsZero() {
		t.Errorf("fired row: %+v", f)
	}
	s := h.history(skipped.ID)[0]
	if s.Outcome != OutcomeSkipped || s.Reason != ReasonValidation || !strings.Contains(s.Detail, `"gone"`) || s.RunID != "" || !s.ScheduledFor.Equal(at) {
		t.Errorf("skipped row: %+v", s)
	}
	if m := h.history(manual.ID)[0]; m.Outcome != OutcomeManual || m.RunID != runID || m.FinishedAt.IsZero() || m.Summary == "" {
		t.Errorf("manual row: %+v", m)
	}
	// The fourth outcome: a run that a dead process left behind.
	other := h.scheduler("sched_old")
	ev := h.add("interrupted", "test.count", hourly(), PolicySkip)
	if _, err := other.Repo().ClaimRun(context.Background(), Claim{Schedule: h.get(ev.ID), ScheduledFor: h.clock.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	h.s.Stop(context.Background())
	s2 := h.scheduler("sched_new")
	if err := s2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s2.Stop(context.Background())
	if r := h.history(ev.ID)[0]; r.Outcome != OutcomeInterrupted || r.Reason != ReasonInterrupted || r.FinishedAt.IsZero() {
		t.Errorf("interrupted row: %+v", r)
	}
}

func TestScheduler_Manual_SameAdmissionExceptEnabled(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.net", nil, true, nil)
	h.kind("test.block", nil, false, g.job())
	h.start()
	net := h.add("net", "test.net", dailyAt("23:00"), PolicySkip)
	blocked := h.add("blocked", "test.block", dailyAt("23:00"), PolicySkip)
	refused := func(id int64, want string) {
		t.Helper()
		_, err := h.s.RunNow(context.Background(), id)
		var r *RefusedError
		if !errors.As(err, &r) || r.Reason != want {
			t.Errorf("run of schedule %d: %v, want a refusal for %s", id, err, want)
		}
	}
	h.online = false
	h.s.reach = reachCache{}
	refused(net.ID, ReasonOffline)
	h.online = true
	h.s.reach = reachCache{}
	if _, err := h.s.RunNow(context.Background(), blocked.ID); err != nil {
		t.Fatal(err)
	}
	g.waitStarted(t, 1)
	refused(blocked.ID, ReasonBusy) // the same schedule is still running
	// Disabling is the one check a manual run does not make.
	off := false
	if _, err := h.mgr.Update(context.Background(), net.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.RunNow(context.Background(), net.ID); err != nil {
		t.Fatalf("a disabled schedule was refused: %v", err)
	}
	close(g.release)
	h.s.Wait()
}

// A kind's follow-up (Kind.Then) runs once right after a run of it succeeds,
// under the follow-up kind's own leases and with none of the schedule's
// inputs, as a run of that schedule that is no occurrence. A run that left it
// nothing or failed has none, and so does every run while an enabled schedule
// of the follow-up's kind exists: that schedule is the user's choice.
func TestScheduler_FollowUp_RunsOnceAfterASuccessUnlessTheUserSchedulesIt(t *testing.T) {
	for _, c := range []struct {
		name               string
		due, fail, planned bool
		want               int
	}{
		{"succeeded with work left", true, false, false, 1},
		{"nothing left", false, false, false, 0},
		{"failed", true, true, false, 0},
		{"the user schedules the follow-up kind", true, false, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			var seen []string
			h.kind("test.drain", []Lease{LeaseBrowser, LeaseEvaluation}, true, func(ctx context.Context, in Invocation) (string, error) {
				var holder string
				err := h.st.Read().QueryRowContext(ctx, `SELECT holder FROM resource_leases WHERE name = 'browser'`).Scan(&holder)
				seen = append(seen, fmt.Sprintf("%d inputs, browser held by the drain: %v", len(in.Inputs), err == nil && strings.HasPrefix(holder, "test.drain ")))
				return "drained", nil
			})
			err := h.reg.Register(Kind{Name: "test.scan", Leases: []Lease{LeaseEvaluation}, Drives: Drives{Evaluation: true}, Network: true,
				Fields: []Field{{Name: "limit", Type: FieldInt, Min: 1}},
				Run: func(context.Context, Invocation) (string, error) {
					if c.fail {
						return "", errors.New("the board is down")
					}
					return "scanned", nil
				},
				Then: &FollowUp{Kind: "test.drain", Due: func(context.Context) (bool, error) { return c.due, nil }}})
			if err != nil {
				t.Fatal(err)
			}
			h.start()
			if c.planned {
				h.add("drain", "test.drain", dailyAt("23:00"), PolicySkip)
			}
			sc, err := h.mgr.Create(context.Background(), NewSchedule{Name: "scan", Kind: "test.scan", Inputs: map[string]any{"limit": 25}, Preset: dailyAt("23:00")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.s.RunNow(context.Background(), sc.ID); err != nil {
				t.Fatal(err)
			}
			h.s.Wait()
			if len(seen) != c.want || c.want == 1 && seen[0] != "0 inputs, browser held by the drain: true" {
				t.Fatalf("follow-up runs: %q, want %d", seen, c.want)
			}
			var n, occurrences int
			if err := h.st.Read().QueryRow(`SELECT count(*), count(scheduled_for) FROM runs WHERE kind = 'test.drain' AND schedule_id = ? AND status = 'succeeded'`, sc.ID).Scan(&n, &occurrences); err != nil || n != c.want || occurrences != 0 {
				t.Fatalf("follow-up records %d (%d with an instant), %v", n, occurrences, err)
			}
			if rows := h.history(sc.ID); len(rows) != 1 || h.get(sc.ID).ActiveRunID != "" {
				t.Errorf("the follow-up touched the schedule's decisions: %+v", rows)
			}
		})
	}
}

func TestScheduler_Cancel_ReleasesLeases(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	h.kind("test.hold", []Lease{LeaseTracker}, false, g.job())
	h.start()
	running := h.add("running", "test.hold", dailyAt("23:00"), PolicySkip)
	queued := h.add("queued", "test.hold", dailyAt("23:30"), PolicySkip)
	first, err := h.s.RunNow(context.Background(), running.ID)
	if err != nil {
		t.Fatal(err)
	}
	g.waitStarted(t, 1)
	second, err := h.s.RunNow(context.Background(), queued.ID)
	if err == nil {
		t.Fatalf("a second run behind the lease was admitted (%s)", second)
	}
	if h.leaseRows() != 1 {
		t.Fatalf("%d lease rows", h.leaseRows())
	}
	if !h.s.Cancel(first) {
		t.Fatal("the running job is not known")
	}
	h.s.Wait()
	run, err := h.st.GetRun(context.Background(), first)
	if err != nil || run.Status != "cancelled" {
		t.Fatalf("run %+v %v", run, err)
	}
	if h.leaseRows() != 0 {
		t.Fatal("the cancelled job kept its lease")
	}
	if got := h.get(running.ID); got.ActiveRunID != "" || got.LastResult != ResultFailed {
		t.Fatalf("schedule %+v", got)
	}
	// A cancel is recorded as a failed result with the reason cancelled: the
	// schedule keeps its closed set of results, the history row says why.
	var row HistoryRow
	for _, r := range h.history(running.ID) {
		if r.RunID == first {
			row = r
		}
	}
	if row.Reason != ReasonCancelled || row.FinishedAt.IsZero() || run.FailureReason != ReasonCancelled {
		t.Fatalf("history row %+v, run %+v", row, run)
	}
	if h.s.Cancel("run_unknown") {
		t.Fatal("an unknown run was cancelled")
	}
	if _, err := h.s.RunNow(context.Background(), queued.ID); err != nil {
		t.Fatalf("the freed lease was not usable: %v", err)
	}
	close(g.release)
	h.s.Wait()
}

func TestScheduler_Timers_RetentionSweepRunsRules(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.step(2 * time.Hour)
	if len(h.history(sc.ID)) == 0 {
		t.Fatal("no history")
	}
	var ran []string
	h.s.AddRetention("one", func(context.Context) error { ran = append(ran, "one"); return nil })
	h.s.AddRetention("two", func(context.Context) error { ran = append(ran, "two"); return nil })
	h.clock.Suspend(92 * 24 * time.Hour) // the history is now older than 90 days
	h.tick()
	h.s.Wait()
	if fmt.Sprint(ran) != "[one two]" {
		t.Fatalf("the sweep ran %v", ran)
	}
	if rows := h.history(sc.ID); len(rows) != 0 && rows[len(rows)-1].CreatedAt.Before(h.clock.Now().Add(-HistoryRetention)) {
		t.Fatalf("the sweep left history older than 90 days: %d rows", len(rows))
	}
}

func TestScheduler_Leases_FailedReleaseStopsRenewingFinishedHolder(t *testing.T) {
	h := newHarness(t)
	s := h.scheduler("sched_a")
	ctx := context.Background()
	if busy, err := s.leases.acquire(ctx, "finished", []Lease{LeaseTracker}); err != nil || busy != nil {
		t.Fatalf("acquire: %+v %v", busy, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.leases.release(cancelled, "finished"); err == nil {
		t.Fatal("cancelled release unexpectedly succeeded")
	}
	if n := h.leaseRows(); n != 1 {
		t.Fatalf("failed release left %d rows", n)
	}
	h.clock.Advance(leaseTTL / 2)
	if err := s.leases.renew(ctx); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(leaseTTL/2 + time.Millisecond)
	if busy, err := s.leases.acquire(ctx, "next", []Lease{LeaseTracker}); err != nil || busy != nil {
		t.Fatalf("finished holder was renewed after failed release: %+v %v", busy, err)
	}
}

func TestScheduler_Leases_OldReleaseDoesNotDeleteReplacement(t *testing.T) {
	h := newHarness(t)
	a, b := h.scheduler("sched_a"), h.scheduler("sched_b")
	ctx := context.Background()
	if busy, err := a.leases.acquire(ctx, "same-holder", []Lease{LeaseTracker}); err != nil || busy != nil {
		t.Fatalf("first: %+v %v", busy, err)
	}
	h.clock.Advance(leaseTTL + time.Millisecond)
	if busy, err := b.leases.acquire(ctx, "same-holder", []Lease{LeaseTracker}); err != nil || busy != nil {
		t.Fatalf("takeover: %+v %v", busy, err)
	}
	if err := a.leases.release(ctx, "same-holder"); err != nil {
		t.Fatal(err)
	}
	if n := h.leaseRows(); n != 1 {
		t.Fatalf("old release deleted replacement: %d rows", n)
	}
	if busy, err := a.leases.acquire(ctx, "next", []Lease{LeaseTracker}); err != nil || busy == nil {
		t.Fatalf("replacement did not retain ownership: %+v %v", busy, err)
	}
}

func TestScheduler_Leases_ReusedHolderDoesNotRenewFailedRelease(t *testing.T) {
	h := newHarness(t)
	s := h.scheduler("sched_a")
	ctx := context.Background()
	if busy, err := s.leases.acquire(ctx, "timer", []Lease{LeaseTracker}); err != nil || busy != nil {
		t.Fatalf("first: %+v %v", busy, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.leases.release(cancelled, "timer"); err == nil {
		t.Fatal("cancelled release unexpectedly succeeded")
	}
	if busy, err := s.leases.acquire(ctx, "timer", []Lease{LeaseEvaluation}); err != nil || busy != nil {
		t.Fatalf("reused label: %+v %v", busy, err)
	}
	h.clock.Suspend(2 * leaseTTL)
	if err := s.leases.renew(ctx); err != nil {
		t.Fatal(err)
	}
	if busy, err := s.leases.acquire(ctx, "other", []Lease{LeaseTracker}); err != nil || busy != nil {
		t.Fatalf("old resource attached to new holder lifetime: %+v %v", busy, err)
	}
}

// A pause and resume moves the cursor to the resume, but the list's last
// result time stays the time its run started (as History records it).
func TestScheduler_LastResultAt_IsTheRunNotTheCursor(t *testing.T) {
	h := newHarness(t)
	h.kind("test.count", nil, false, func(context.Context, Invocation) (string, error) { return "1 new", nil })
	h.start()
	sc := h.add("a", "test.count", dailyAt("07:00"), PolicySkip)
	h.step(time.Hour)
	h.s.Wait()
	off, on := false, true
	h.step(3 * time.Hour)
	for _, p := range []Patch{{Enabled: &off}, {Enabled: &on}} {
		if _, err := h.mgr.Update(context.Background(), sc.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	got, ran := h.get(sc.ID), h.history(sc.ID)[0]
	if got.LastResult != ResultOK || ran.StartedAt.IsZero() || !got.LastResultAt.Equal(ran.StartedAt) || got.LastFiredAt.Equal(ran.StartedAt) {
		t.Fatalf("last result at %v, the run started %v (cursor %v)", got.LastResultAt, ran.StartedAt, got.LastFiredAt)
	}
}
