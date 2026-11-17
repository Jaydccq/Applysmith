package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"applysmith/internal/store"
)

// Timing and capacity rules of the tick loop and the runner.
const (
	maxTick       = 60 * time.Second
	minTick       = 50 * time.Millisecond
	dueGrace      = 500 * time.Millisecond
	gapThreshold  = 5 * time.Minute
	starvedRatio  = 0.75
	lookback      = 7 * 24 * time.Hour
	lookbackMax   = 1000
	lookbackScan  = int(lookback/minOccurrenceGap) + 1
	fireAllCap    = 24
	deferLimit    = 6 * time.Hour
	healthWindow  = 2 * time.Minute
	maxRunning    = 3
	maxWaiting    = 8
	chainPollWait = 5 * time.Second
	retryWake     = 10 * time.Second
	maxAttempts   = 3
)

// stopWait is how long a graceful stop waits for running jobs.
var stopWait = 10 * time.Second

// retryWaits are the waits before the second and the third attempt.
var retryWaits = [...]time.Duration{30 * time.Second, 2 * time.Minute}

// NeedsYouItem is a failure that waits for the user: a sign-in, a checkpoint,
// a challenge or an authorization error. The notification policy turns it
// into a needs-you item on the runs board.
type NeedsYouItem struct {
	RunID string
	// ScheduleID is the schedule of the failed run; Options.Recovered names it
	// again once a later run of it succeeds.
	ScheduleID int64
	Schedule   string
	Kind       string
	Reason     string
	Message    string
}

// Options configures a Scheduler.
type Options struct {
	Store    *store.Store
	Registry *Registry
	Clock    Clock
	// Instance names this scheduler in the lease table; empty picks a random
	// one.
	Instance string
	// LockHeld reports whether the process still holds the workspace lock. The
	// scheduler stops ticking at once when it returns false. Nil means held.
	LockHeld func() bool
	// Probe answers whether the host can reach the internet. Nil probes two
	// well-known hosts, with the answer cached for 30 seconds.
	Probe func(ctx context.Context) bool
	// Wait sleeps between ticks. Nil uses the real clock.
	Wait func(ctx context.Context, d time.Duration) error
	// Sleep waits between the attempts of a retried job and between the polls
	// of a replay. Nil uses the real clock.
	Sleep func(ctx context.Context, d time.Duration) error
	// NeedsYou raises a needs-you item. Nil drops it; the run still ends failed
	// with the reason. The runs board delivers it once the notification policy
	// exists (task 5.4).
	NeedsYou func(ctx context.Context, item NeedsYouItem)
	// Recovered tells that a run of the schedule succeeded, so what NeedsYou
	// raised for its earlier runs waits no longer. Nil drops it.
	Recovered func(ctx context.Context, scheduleID int64)
	// Changed tells the dashboard that schedules or runs changed.
	Changed func()
	// Progress carries each line a job reports to the run's stream; Ended
	// tells the stream how the run ended. Both may be nil.
	Progress func(runID, line string)
	Ended    func(f Finish)
	Log      *slog.Logger
	// Zone is the time zone of the built-in timers; nil means the machine's.
	Zone *time.Location
}

// Scheduler decides and runs the occurrences of the stored schedules.
type Scheduler struct {
	o    Options
	repo *Repo
	// Manager edits schedules; it works without a running scheduler.
	Manager *Manager
	leases  *leaseSet
	reg     *Registry
	clock   Clock
	log     *slog.Logger
	wait    func(ctx context.Context, d time.Duration) error
	sleep   func(ctx context.Context, d time.Duration) error

	tickMu  sync.Mutex // one tick at a time
	admitMu sync.Mutex // one decision at a time
	runMu   sync.Mutex // guards running, queue and chains
	running map[string]*job
	queue   []*job
	chains  map[int64]bool
	wg      sync.WaitGroup

	mu        sync.Mutex // guards the fields below
	started   bool
	stopped   bool
	lost      bool
	ticked    bool
	lastWall  time.Time
	lastMono  time.Duration
	lastTick  time.Time
	loopStop  context.CancelFunc
	loopDone  chan struct{}
	runCtx    context.Context
	runCancel context.CancelCauseFunc
	timers    []Timer
	ret       *retention
	timerBusy map[string]bool
	reach     reachCache
}

var (
	errStopping  = errors.New("the server is stopping")
	errCancelled = errors.New("the run was cancelled")
)

// New builds a scheduler. It does not touch the database until Start.
func New(o Options) (*Scheduler, error) {
	switch {
	case o.Store == nil:
		return nil, errors.New("scheduler: a store is required")
	case o.Registry == nil:
		return nil, errors.New("scheduler: a registry is required")
	}
	if o.Clock == nil {
		o.Clock = NewSystemClock()
	}
	if o.Instance == "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		o.Instance = "sched_" + hex.EncodeToString(b[:])
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Wait == nil {
		o.Wait = sleepContext
	}
	if o.Sleep == nil {
		o.Sleep = sleepContext
	}
	if o.Zone == nil {
		o.Zone = time.Local
	}
	for _, k := range o.Registry.All() {
		if err := checkKind(k); err != nil {
			return nil, err
		}
	}
	s := &Scheduler{o: o, reg: o.Registry, clock: o.Clock, log: o.Log, wait: o.Wait, sleep: o.Sleep,
		repo: NewRepo(o.Store, o.Clock.Now), running: map[string]*job{}, chains: map[int64]bool{}, timerBusy: map[string]bool{}}
	s.Manager = &Manager{repo: s.repo, reg: o.Registry, now: o.Clock.Now}
	s.leases = newLeaseSet(o.Store, o.Clock.Now, o.Instance)
	s.ret = &retention{s: s}
	s.ret.rules = []retentionRule{{"schedule history", func(ctx context.Context) error { _, err := s.repo.PruneHistory(ctx); return err }}}
	s.AddTimer(Every("wal-checkpoint", "Every 15 minutes: folds the write-ahead log into the database file", walEvery,
		func(context.Context) error { return o.Store.Checkpoint() }))
	s.AddTimer(s.ret)
	s.runCtx, s.runCancel = context.WithCancelCause(context.Background())
	return s, nil
}

// Repo exposes the schedule store.
func (s *Scheduler) Repo() *Repo { return s.repo }

// Registry returns the job kind registry.
func (s *Scheduler) Registry() *Registry { return s.reg }

func (s *Scheduler) changed() {
	if s.o.Changed != nil {
		s.o.Changed()
	}
}

// Start settles what a dead process left behind, runs the missed-fire pass and
// starts the tick loop. Before the first tick it marks every run that is still
// queued or running as failed with reason interrupted and releases stale
// leases; nothing is retried.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("scheduler: already started")
	}
	s.started = true
	s.mu.Unlock()
	if _, err := s.repo.SweepInterrupted(ctx); err != nil {
		return fmt.Errorf("scheduler: settle interrupted runs: %w", err)
	}
	if err := s.leases.sweep(ctx, false); err != nil {
		return fmt.Errorf("scheduler: release stale leases: %w", err)
	}
	// The process did not run until now: whatever fell due while it was
	// stopped is a gap of the host-asleep kind.
	next := s.tick(ctx, &gapInfo{reason: ReasonHostAsleep})
	s.mu.Lock()
	if s.stopped {
		// A stop that arrived during the first tick wins; no loop starts after it.
		s.mu.Unlock()
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.loopStop, s.loopDone = cancel, make(chan struct{})
	done := s.loopDone
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.loop(loopCtx, next)
	}()
	return nil
}

// loop wakes at the next due instant, at most 60 seconds after the last wake
// and at least 50 milliseconds apart.
func (s *Scheduler) loop(ctx context.Context, next time.Duration) {
	for {
		if err := s.wait(ctx, wakeAfter(next)); err != nil {
			return
		}
		next = s.Tick(ctx)
	}
}

// wakeAfter bounds the wait for the next tick.
func wakeAfter(untilNext time.Duration) time.Duration {
	return min(max(untilNext, minTick), maxTick)
}

// Stop cancels running jobs, waits up to ten seconds for them to end and marks
// queued jobs and any still running as failed with reason interrupted.
func (s *Scheduler) Stop(ctx context.Context) {
	s.mu.Lock()
	if s.stopped || !s.started {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	stop, done := s.loopStop, s.loopDone
	s.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	s.tickMu.Lock() // a tick in progress ends before the jobs are cancelled
	s.tickMu.Unlock()
	// Admission may also come from RunNow or a replay outside the tick.
	// Serialize cancellation with claims before draining unstarted jobs.
	s.admitMu.Lock()
	s.runCancel(errStopping)
	s.runMu.Lock()
	queued := s.queue
	s.queue = nil
	s.runMu.Unlock()
	s.admitMu.Unlock()
	for _, j := range queued {
		s.finishQueued(j, Finish{RunID: j.runID, Status: "failed", Reason: ReasonInterrupted,
			Detail: "the server stopped before the job started", At: s.clock.Now(), Interrupted: true})
	}
	ended := make(chan struct{})
	go func() { s.wg.Wait(); close(ended) }()
	wait := time.NewTimer(stopWait)
	defer wait.Stop()
	select {
	case <-ended:
	case <-wait.C:
		s.log.Warn("scheduler jobs did not end within the stop window; marking them interrupted")
		bg := context.WithoutCancel(ctx)
		if _, err := s.repo.SweepInterrupted(bg); err != nil {
			s.log.Error("marking running jobs interrupted failed", "error", err.Error())
		}
	}
	if err := s.leases.sweep(context.WithoutCancel(ctx), true); err != nil {
		s.log.Error("releasing leases failed", "error", err.Error())
	}
}

// Wait blocks until the running jobs, replays and timers have ended. Tests use
// it; the server uses Stop.
func (s *Scheduler) Wait() { s.wg.Wait() }

// Health is the state of the scheduler component of the health document.
type Health struct {
	Status   string // ok, degraded or down
	Detail   string
	LastTick time.Time
}

// Health reports ok while ticks happen at least once per two minutes,
// degraded when they do not, and down when no scheduler runs.
func (s *Scheduler) Health() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := Health{LastTick: s.lastTick}
	switch {
	case !s.started || s.stopped:
		h.Status, h.Detail = "down", "the scheduler is not running"
	case s.lost:
		h.Status, h.Detail = "down", "the workspace lock was lost"
	case s.lastTick.IsZero() || s.clock.Now().Sub(s.lastTick) > healthWindow:
		h.Status, h.Detail = "degraded", "ticks are not keeping up"
	default:
		h.Status = "ok"
	}
	return h
}

// gapInfo describes the time during which ticks did not run.
type gapInfo struct {
	reason string
	wall   time.Duration
	awake  time.Duration
}

func (g *gapInfo) detail() string {
	if g.wall == 0 {
		return "the server was not running"
	}
	return fmt.Sprintf("gap=%s awake=%s", g.wall.Round(time.Second), g.awake.Round(time.Second))
}

// Tick runs one pass and returns how long to wait for the next one.
func (s *Scheduler) Tick(ctx context.Context) time.Duration { return s.tick(ctx, nil) }

func (s *Scheduler) tick(ctx context.Context, startGap *gapInfo) time.Duration {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if s.o.LockHeld != nil && !s.o.LockHeld() {
		s.mu.Lock()
		s.lost = true
		s.mu.Unlock()
		return maxTick
	}
	s.mu.Lock()
	if s.lost || s.stopped {
		s.mu.Unlock()
		return maxTick
	}
	now, mono := s.clock.Now(), s.clock.Mono()
	gap := startGap
	if gap == nil && s.ticked && now.Sub(s.lastWall) > gapThreshold {
		wall, awake := now.Sub(s.lastWall), mono-s.lastMono
		gap = &gapInfo{reason: ReasonPolicy, wall: wall, awake: awake}
		// The monotonic clock covers most of the gap when the process was
		// awake but starved; when it does not, the host slept.
		if float64(awake) >= starvedRatio*float64(wall) {
			gap.reason = ReasonHostStarved
		} else {
			gap.reason = ReasonHostAsleep
		}
	}
	s.ticked, s.lastWall, s.lastMono, s.lastTick = true, now, mono, now
	s.mu.Unlock()

	if err := s.leases.renew(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("renewing leases failed", "error", err.Error())
	}
	next := maxTick
	scheds, err := s.repo.ListEnabled(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("reading schedules failed", "error", err.Error())
		}
	}
	ids := make([]int64, 0, len(scheds))
	for _, sc := range scheds {
		if d := s.evaluate(ctx, sc, now, gap); d > 0 && d < next {
			next = d
		}
		ids = append(ids, sc.ID)
	}
	if err := s.repo.Touch(ctx, ids, now); err != nil && ctx.Err() == nil {
		s.log.Warn("recording the evaluation failed", "error", err.Error())
	}
	s.runTimers(ctx, now)
	s.dispatch(ctx)
	return next
}
