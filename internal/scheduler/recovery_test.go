package scheduler

import (
	"context"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"
)

func TestScheduler_Recovery_SleepNotMistakenForStarvation(t *testing.T) {
	// A closed lid overnight: the wall clock ran 11 hours while the process was
	// awake for under two. The suspended clock must not read as starvation.
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.tick()
	h.clock.Advance(7024 * time.Second)
	h.clock.Suspend(33356 * time.Second)
	h.tick()
	d := h.decisions(sc.ID)
	if len(d) < 10 {
		t.Fatalf("decisions %v", d)
	}
	for _, x := range d {
		if x != "skipped/host-asleep" {
			t.Fatalf("a sleeping host was recorded as %s: %v", x, d)
		}
	}
	// The line between the two: awake for 75 percent of the gap or more reads as starved.
	for _, c := range []struct {
		awake time.Duration
		want  string
	}{{74 * time.Minute, "skipped/host-asleep"}, {75 * time.Minute, "skipped/host-starved"}, {100 * time.Minute, "skipped/host-starved"}} {
		t.Run(c.awake.String(), func(t *testing.T) {
			h := newHarness(t)
			h.kind("test.fast", nil, false, nil)
			h.start()
			sc := h.add("a", "test.fast", hourly(), PolicySkip)
			h.tick()
			h.clock.Advance(c.awake)
			h.clock.Suspend(100*time.Minute - c.awake)
			h.tick()
			if d := h.decisions(sc.ID); !eq(d, c.want) {
				t.Fatalf("awake %s of 100 minutes: %v, want %s", c.awake, d, c.want)
			}
		})
	}
}

func TestScheduler_Recovery_DoesNotRepeatOnNextTick(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.tick()
	h.clock.Suspend(4*time.Hour + 30*time.Minute)
	h.tick()
	want := h.decisions(sc.ID)
	if len(want) != 4 {
		t.Fatalf("decisions %v", want)
	}
	h.stepBy(30*time.Minute, time.Minute) // 11:00 falls due on an ordinary tick
	got := h.decisions(sc.ID)
	if len(got) != 5 || got[4] != "fired" || !slices.Equal(got[:4], want) {
		t.Fatalf("the pass repeated or the next tick was read as a gap: %v", got)
	}
	// A restart's pass is not repeated by the first tick after it either.
	h2 := newHarness(t)
	h2.kind("test.fast", nil, false, nil)
	s2 := h2.add("b", "test.fast", hourly(), PolicySkip)
	h2.clock.Advance(3*time.Hour + 10*time.Minute)
	h2.start()
	n := len(h2.decisions(s2.ID))
	h2.clock.Advance(time.Minute)
	h2.tick()
	if got := len(h2.decisions(s2.ID)); got != n {
		t.Fatalf("the first tick after the start decided %d more occurrences", got-n)
	}
}

func TestScheduler_Admission_CheckOrder(t *testing.T) {
	h := newHarness(t)
	h.kind("test.net", []Lease{LeaseTracker}, true, nil)
	h.start()
	ctx := context.Background()
	h.exec(`INSERT INTO resource_leases (name, holder, owner_pid, owner_instance, acquired_at, expires_at)
		VALUES ('tracker:write', 'someone', 1, 'other', ?, ?)`, ts(baseTime), ts(baseTime.Add(48*time.Hour)))
	// Every case fails the check under test and every later one as well.
	bad := map[string]any{"bogus": 1}
	full := func() {
		h.s.runMu.Lock()
		defer h.s.runMu.Unlock()
		for i := range maxRunning {
			h.s.running[fmt.Sprint("r", i)] = &job{}
		}
		for range maxWaiting {
			h.s.queue = append(h.s.queue, &job{})
		}
	}
	steps := []struct {
		reason  string
		sc      Schedule
		offline bool
		defer_  bool
	}{
		{ReasonDisabled, Schedule{Enabled: false, Kind: "gone", Inputs: bad, ActiveRunID: "run_x"}, true, false},
		{ReasonUnknownKind, Schedule{Enabled: true, Kind: "gone", Inputs: bad, ActiveRunID: "run_x"}, true, false},
		{ReasonValidation, Schedule{Enabled: true, Kind: "test.net", Inputs: bad, ActiveRunID: "run_x"}, true, false},
		{ReasonOffline, Schedule{Enabled: true, Kind: "test.net", Inputs: map[string]any{}, ActiveRunID: "run_x"}, true, true},
		{ReasonBusy, Schedule{Enabled: true, Kind: "test.net", Inputs: map[string]any{}, ActiveRunID: "run_x"}, false, false},
		{ReasonLease, Schedule{Enabled: true, Kind: "test.net", Inputs: map[string]any{}}, false, true},
	}
	for _, c := range steps {
		h.online = !c.offline
		h.s.reach = reachCache{}
		v := h.s.admit(ctx, c.sc, false)
		if v.ok || v.reason != c.reason || v.deferrable != c.defer_ {
			t.Errorf("want %s (deferrable %v): got %+v", c.reason, c.defer_, v)
		}
	}
	// The last check: a full queue defers, whatever lease state the kind has.
	h.exec(`DELETE FROM resource_leases`)
	h.online = true
	h.s.reach = reachCache{}
	full()
	v := h.s.admit(ctx, Schedule{Enabled: true, Kind: "test.net", Inputs: map[string]any{}}, false)
	if v.ok || v.reason != ReasonBusy || !v.deferrable {
		t.Errorf("full queue: %+v", v)
	}
	h.s.runMu.Lock()
	h.s.running, h.s.queue = map[string]*job{}, nil
	h.s.runMu.Unlock()
	if v := h.s.admit(ctx, Schedule{Enabled: true, Kind: "test.net", Inputs: map[string]any{}}, false); !v.ok {
		t.Errorf("nothing in the way: %+v", v)
	}
	// A manual run passes the enabled flag and nothing else.
	if v := h.s.admit(ctx, Schedule{Enabled: false, Kind: "test.net", Inputs: map[string]any{}}, true); !v.ok {
		t.Errorf("manual run of a disabled schedule: %+v", v)
	}
}

func TestScheduler_Offline_DialAnyNeedsOneHost(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	down, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := down.Addr().String()
	down.Close()
	old := probeHosts
	defer func() { probeHosts = old }()
	probeHosts = []string{closed, up.Addr().String()}
	if !dialAny(context.Background()) {
		t.Fatal("one reachable host was not enough")
	}
	probeHosts = []string{closed}
	if dialAny(context.Background()) {
		t.Fatal("no reachable host still read as online")
	}
}
