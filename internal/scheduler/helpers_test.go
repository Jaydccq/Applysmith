package scheduler

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"applysmith/internal/store"
)

// fakeClock moves the wall clock and the monotonic clock together with
// Advance and the wall clock alone with Suspend, which is what a sleeping
// machine does.
type fakeClock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *fakeClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall, c.mono = c.wall.Add(d), c.mono+d
}

func (c *fakeClock) Suspend(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
}

var baseTime = time.Date(2026, 5, 4, 6, 0, 0, 0, time.UTC) // a Monday

type harness struct {
	t      *testing.T
	st     *store.Store
	path   string
	clock  *fakeClock
	reg    *Registry
	s      *Scheduler
	mgr    *Manager
	online bool
	async  bool // ticks do not wait for jobs, because a test holds some open

	mu        sync.Mutex
	calls     map[string][]time.Time
	sleeps    []time.Duration
	needs     []NeedsYouItem
	recovered []int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, store.Options{})
}

// newHarnessWith is newHarness over a store opened with opts (its clock is
// always the harness clock).
func newHarnessWith(t *testing.T, opts store.Options) *harness {
	t.Helper()
	h := &harness{t: t, clock: &fakeClock{wall: baseTime}, reg: NewRegistry(), online: true, calls: map[string][]time.Time{},
		path: filepath.Join(t.TempDir(), "applysmith.db")}
	opts.Now = h.clock.Now
	st, err := store.Open(context.Background(), h.path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h.st = st
	h.mgr = NewManager(st, h.reg, h.clock.Now)
	return h
}

// kind registers a test kind whose drives follow its leases.
func (h *harness) kind(name string, leases []Lease, network bool, run Job) {
	h.t.Helper()
	k := Kind{Name: name, Leases: leases, Network: network, Run: run,
		Drives: Drives{ScanWindow: has(leases, LeaseBrowser), Evaluation: has(leases, LeaseEvaluation),
			TrackerWrite: has(leases, LeaseTracker), GmailRead: has(leases, LeaseGmail)}}
	if run == nil {
		k.Run = h.record(name)
	}
	if err := h.reg.Register(k); err != nil {
		h.t.Fatal(err)
	}
}

func has(ls []Lease, l Lease) bool {
	for _, x := range ls {
		if x == l {
			return true
		}
	}
	return false
}

// record is a job that notes when it ran and succeeds.
func (h *harness) record(name string) Job {
	return func(ctx context.Context, in Invocation) (string, error) {
		h.mu.Lock()
		h.calls[name] = append(h.calls[name], h.clock.Now())
		h.mu.Unlock()
		return "ran " + name, nil
	}
}

func (h *harness) called(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls[name])
}

// scheduler builds a scheduler on the harness store. The loop never sleeps
// for real: Wait blocks until the loop is stopped, and Sleep records the wait.
func (h *harness) scheduler(instance string, mod ...func(*Options)) *Scheduler {
	h.t.Helper()
	o := Options{Store: h.st, Registry: h.reg, Clock: h.clock, Instance: instance,
		Probe: func(context.Context) bool { return h.online },
		Wait:  func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() },
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.mu.Lock()
			h.sleeps = append(h.sleeps, d)
			h.mu.Unlock()
			return ctx.Err()
		},
		NeedsYou: func(_ context.Context, it NeedsYouItem) {
			h.mu.Lock()
			h.needs = append(h.needs, it)
			h.mu.Unlock()
		},
		Recovered: func(_ context.Context, id int64) {
			h.mu.Lock()
			h.recovered = append(h.recovered, id)
			h.mu.Unlock()
		},
		Zone: time.UTC}
	for _, m := range mod {
		m(&o)
	}
	s, err := New(o)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// start builds the default scheduler and starts it.
func (h *harness) start() {
	h.t.Helper()
	h.s = h.scheduler("sched_a")
	if err := h.s.Start(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { h.s.Stop(context.Background()) })
}

// tick runs one tick and waits for the jobs and timers it started.
func (h *harness) tick() {
	h.t.Helper()
	h.s.Tick(context.Background())
	if !h.async {
		h.s.Wait()
	}
}

func (h *harness) add(name, kind string, p Preset, policy string) Schedule {
	h.t.Helper()
	s, err := h.mgr.Create(context.Background(), NewSchedule{Name: name, Kind: kind, Preset: p, Policy: policy})
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) get(id int64) Schedule {
	h.t.Helper()
	s, err := h.mgr.Repo().Get(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) history(id int64) []HistoryRow {
	h.t.Helper()
	rows, err := h.mgr.Repo().History(context.Background(), id, 500)
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

// decisions returns the decision rows (fired, skipped, interrupted) oldest first,
// as "outcome/reason".
func (h *harness) decisions(id int64) []string {
	h.t.Helper()
	rows := h.history(id)
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		switch r.Outcome {
		case OutcomeFired, OutcomeSkipped, OutcomeInterrupted:
			if r.Reason != "" {
				out = append(out, r.Outcome+"/"+r.Reason)
			} else {
				out = append(out, r.Outcome)
			}
		}
	}
	return out
}

func hourly() Preset {
	return Preset{Kind: PresetRRule, RRule: "FREQ=HOURLY;BYMINUTE=0;BYSECOND=0", Timezone: "UTC"}
}

func dailyAt(clock string) Preset { return Preset{Kind: PresetDaily, Time: clock, Timezone: "UTC"} }

func eq(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// corruptPreset stores a preset that no longer parses.
func (h *harness) corruptPreset(id int64) {
	h.t.Helper()
	err := h.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE schedules SET preset = '{"kind":"rrule","rrule":"NOT A RULE","timezone":"UTC"}' WHERE id = ?`, id)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

// step advances the clock by total in one-minute ticks, the way a running
// server sees time pass.
func (h *harness) step(total time.Duration) { h.stepBy(total, time.Minute) }

// stepBy is step with a coarser tick; five minutes is the longest tick that
// is not read as a gap.
func (h *harness) stepBy(total, size time.Duration) {
	h.t.Helper()
	for total > 0 {
		d := min(total, size)
		h.clock.Advance(d)
		h.tick()
		total -= d
	}
}
