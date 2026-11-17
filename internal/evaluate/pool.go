// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"applysmith/internal/discovery"
	"applysmith/internal/identity"
	"applysmith/internal/llm"
)

// Pool limits.
const (
	QueueMax    = 500
	PoolMaxJobs = 8
)

// Stats are the pool's counts.
type Stats struct {
	Active int `json:"active"`
	Queued int `json:"queued"`
}

// PoolOptions configures a Pool.
type PoolOptions struct {
	// Concurrency returns how many jobs may be in flight now: the in-flight
	// call cap of the routed provider kind. It is read at every dispatch so a
	// settings change applies at once. Nil means 4. The pool also caps all
	// jobs at PoolMaxJobs.
	Concurrency func(ctx context.Context) int
	// QueueMax overrides the queue bound (tests); zero means QueueMax.
	QueueMax int
}

// outcomeOrigin is a caller whose inbox/scan must receive a shared result.
// It carries no posting text or profile data.
type outcomeOrigin struct {
	inbox int64
	scan  string
}

func originOf(r Request) outcomeOrigin { return outcomeOrigin{r.InboxRowID, r.ScanRun} }

type pending struct {
	runID    string
	key      string
	req      Request
	done     chan struct{}
	res      Result
	cancel   context.CancelCauseFunc
	joined   []outcomeOrigin
	seen     map[outcomeOrigin]bool
	settling bool // removed from dispatch, but still visible to Wait/Drain
}

// Pool runs evaluations: a FIFO queue in two priority classes, user-started
// before scan-started, in front of a bounded set of workers.
type Pool struct {
	ev   *Evaluator
	opts PoolOptions

	// submitMu serializes submissions across their database write, so the
	// in-memory state needs p.mu only briefly; every addition to the queue
	// holds it. Lock order: submitMu, then mu.
	submitMu sync.Mutex
	mu       sync.Mutex
	ctx      context.Context
	stop     context.CancelCauseFunc
	started  bool
	user     []*pending
	scan     []*pending
	active   int
	inflight map[string]*pending // identity key -> job, queued or running
	byRun    map[string]*pending
	wg       sync.WaitGroup
}

// NewPool returns a stopped pool; call Start to recover and accept work.
func NewPool(ev *Evaluator, opts PoolOptions) *Pool {
	if opts.QueueMax <= 0 {
		opts.QueueMax = QueueMax
	}
	return &Pool{ev: ev, opts: opts, inflight: map[string]*pending{}, byRun: map[string]*pending{}}
}

// Start settles the jobs an earlier process left behind, then accepts work.
// Nothing left behind is re-dispatched.
func (p *Pool) Start(ctx context.Context) error {
	if _, err := p.ev.RecoverInterrupted(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctx, p.stop = context.WithCancelCause(context.Background())
	p.started = true
	return nil
}

// Close stops accepting work, interrupts running jobs and settles queued ones
// (both end failed with reason interrupted, and their inbox rows return to
// pending with the attempt unspent: the shutdown decided nothing about the
// posting) and waits for them.
func (p *Pool) Close() {
	p.submitMu.Lock() // a submission past its check finishes before the queue is drained
	p.mu.Lock()
	if p.stop != nil {
		p.stop(errShutdown)
	}
	p.started = false
	queued := append(p.user, p.scan...)
	p.user, p.scan = nil, nil
	p.wg.Add(len(queued))
	for _, job := range queued {
		job.settling = true
		delete(p.inflight, job.key)
	}
	p.mu.Unlock()
	p.submitMu.Unlock()
	for _, job := range queued {
		p.settleQueued(job, ReasonInterrupted)
	}
	p.wg.Wait()
}

// Drain waits until no job is queued or running, or the context ends. A
// caller that enqueues work it does not wait on (a scan) drains before it
// closes the pool.
func (p *Pool) Drain(ctx context.Context) error {
	for {
		p.mu.Lock()
		var next *pending
		for _, job := range p.byRun {
			next = job
			break
		}
		p.mu.Unlock()
		if next == nil {
			return nil
		}
		select {
		case <-next.done:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// Handle identifies a submitted job.
type Handle struct {
	RunID string
	// Attached is true when the request joined an active job for the same
	// posting and so receives that job's outcome.
	Attached bool
	// Replayed is true when an Idempotency-Key had been used before: the
	// handle is the original run and nothing was queued.
	Replayed bool
}

// NormalizeRequest fills the defaults of a request.
func NormalizeRequest(r Request) (Request, error) {
	if r.Priority == "" {
		r.Priority = PriorityUser
	}
	if r.Mode == "" {
		r.Mode = ModeDeep
	}
	switch r.Mode {
	case ModeAuto, ModeScreen, ModeDeep:
	default:
		return r, fmt.Errorf("evaluate: unknown mode %q", r.Mode)
	}
	if r.Priority != PriorityUser && r.Priority != PriorityScan {
		return r, fmt.Errorf("evaluate: unknown priority %q", r.Priority)
	}
	if r.URL == "" && r.Text == "" {
		return r, errors.New("evaluate: a posting URL or posting text is required")
	}
	return r, nil
}

func requestKey(r Request) string {
	return identity.Posting{URL: r.URL, Source: r.Source, Company: r.Company, Role: r.Title, Location: r.Location, Text: r.Text}.IdentityKey()
}

func newIdempotencyKey() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "ev_" + hex.EncodeToString(b[:])
}

// Submit queues a request. Two requests for one posting that are active at
// once collapse into one job. A request beyond the queue bound is refused with
// ErrQueueFull.
func (p *Pool) Submit(ctx context.Context, r Request) (Handle, error) {
	r, key, err := prepare(r)
	if err != nil {
		return Handle{}, err
	}
	p.submitMu.Lock()
	defer p.submitMu.Unlock()
	h, ok, err := p.admit(key, r)
	if ok || err != nil {
		return h, err
	}
	runID, err := p.ev.createJob(ctx, r, key, newIdempotencyKey())
	if err != nil {
		return Handle{}, err
	}
	p.enqueue(runID, key, r)
	return Handle{RunID: runID}, nil
}

// prepare normalizes a request and derives the identity key of its posting.
func prepare(r Request) (Request, string, error) {
	r, err := NormalizeRequest(r)
	if err != nil {
		return r, "", err
	}
	key := requestKey(r)
	if key == "" {
		return r, "", errors.New("evaluate: the request carries nothing to identify the posting")
	}
	return r, key, nil
}

// admit decides, under the pool lock, whether a request for the posting joins
// a job already active (ok is true and the handle is that job's), is refused,
// or needs a new job. The caller holds submitMu, so nothing else adds a job
// before the caller does.
func (p *Pool) admit(key string, req Request) (h Handle, ok bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return Handle{}, false, errors.New("evaluate: the pool is not running")
	}
	if existing, joined := p.inflight[key]; joined {
		existing.addOrigin(req)
		return Handle{RunID: existing.runID, Attached: true}, true, nil
	}
	if len(p.user)+len(p.scan) >= p.opts.QueueMax {
		return Handle{}, false, ErrQueueFull
	}
	return Handle{}, false, nil
}

// enqueue adds a created job to the queue. The caller holds submitMu, which
// Close takes before it stops the pool, so the pool is still running.
func (p *Pool) enqueue(runID, key string, r Request) {
	n := p.concurrency()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enqueueLocked(runID, key, r, n)
}

func (p *Pool) enqueueLocked(runID, key string, r Request, limit int) {
	job := &pending{runID: runID, key: key, req: r, done: make(chan struct{}), seen: map[outcomeOrigin]bool{originOf(r): true}}
	p.inflight[key], p.byRun[runID] = job, job
	if r.Priority == PriorityUser {
		p.user = append(p.user, job)
	} else {
		p.scan = append(p.scan, job)
	}
	p.dispatchLocked(limit)
}

// concurrency reads the in-flight limit. It runs without the pool lock: the
// function behind it reads settings from the database.
func (p *Pool) concurrency() int {
	n := 4
	if p.opts.Concurrency != nil {
		p.mu.Lock()
		ctx := p.ctx
		p.mu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		n = p.opts.Concurrency(ctx)
	}
	return max(1, min(n, PoolMaxJobs))
}

// dispatchLocked starts queued jobs while there is room, user-started first.
func (p *Pool) dispatchLocked(limit int) {
	for p.started && p.active < limit {
		var job *pending
		switch {
		case len(p.user) > 0:
			job, p.user = p.user[0], p.user[1:]
		case len(p.scan) > 0:
			job, p.scan = p.scan[0], p.scan[1:]
		default:
			return
		}
		p.active++
		ctx, cancel := context.WithCancelCause(p.ctx)
		job.cancel = cancel
		p.wg.Add(1)
		go p.work(ctx, job)
	}
}

func (p *Pool) work(ctx context.Context, job *pending) {
	defer p.wg.Done()
	res, detail := p.ev.execute(ctx, job.runID, job.req, job.key)
	// The job takes no more joins before its outcome is recorded: a request
	// made after the run reads finished ("Evaluate again" on a failure) is a
	// new evaluation, not a join of the one that ended. The origins that
	// joined in time receive the outcome first.
	p.mu.Lock()
	delete(p.inflight, job.key)
	origins := job.joined
	job.joined = nil
	p.mu.Unlock()
	for _, origin := range origins {
		p.settleOrigin(job.runID, origin, res)
	}
	p.ev.finish(job.runID, res, detail)
	n := p.concurrency()
	p.mu.Lock()
	job.cancel(nil)
	job.res = res
	delete(p.byRun, job.runID)
	p.active--
	close(job.done)
	p.dispatchLocked(n)
	p.mu.Unlock()
}

// Cancel stops a job at its next phase boundary without writing a report. A
// queued job never starts.
func (p *Pool) Cancel(runID string) bool {
	p.mu.Lock()
	job, ok := p.byRun[runID]
	if !ok {
		p.mu.Unlock()
		return false
	}
	if job.settling {
		p.mu.Unlock()
		return true
	}
	if job.cancel != nil {
		job.cancel(errCancelled)
		p.mu.Unlock()
		return true
	}
	job.settling = true
	p.wg.Add(1)
	p.user, p.scan = remove(p.user, job), remove(p.scan, job)
	delete(p.inflight, job.key)
	p.mu.Unlock()
	p.settleQueued(job, ReasonCancelled)
	return true
}

// settleQueued ends a job that was taken off the queue before it started:
// failed with the reason, its inbox row returned, its waiters released.
func (p *Pool) settleQueued(job *pending, reason string) {
	defer p.wg.Done()
	j := &jobRun{e: p.ev, runID: job.runID, req: job.req, key: job.key, detail: map[string]any{}}
	res := Result{RunID: job.runID, Outcome: OutcomeFailed, Reason: reason}
	if reason == ReasonInterrupted {
		res.charge = discovery.ChargeNone
	}
	j.settle(res)
	for _, origin := range job.joined {
		p.settleOrigin(job.runID, origin, res)
	}
	p.ev.finish(job.runID, res, j.detail)
	p.mu.Lock()
	job.res = res
	delete(p.byRun, job.runID)
	close(job.done)
	p.mu.Unlock()
}

func remove(q []*pending, job *pending) []*pending {
	out := q[:0:0]
	for _, x := range q {
		if x != job {
			out = append(out, x)
		}
	}
	return out
}

// Wait returns a job's outcome once it has one.
func (p *Pool) Wait(ctx context.Context, runID string) (Result, error) {
	p.mu.Lock()
	job, ok := p.byRun[runID]
	p.mu.Unlock()
	if ok {
		select {
		case <-job.done:
			return job.res, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	res, _, found, err := JobOutcome(ctx, p.ev.d.Store, runID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, fmt.Errorf("evaluate: no job %s", runID)
	}
	return res, nil
}

// Stats reports the active and queued counts.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Active: p.active, Queued: len(p.user) + len(p.scan)}
}

// RecoverInterrupted ends every evaluation run that was not terminal as
// failed with reason interrupted and returns the inbox rows still queued to
// pending. It runs before the pool accepts work, so no job is in flight and
// every queued row is one whose job a dead process lost, or never created. It
// creates no run and re-dispatches nothing, so a restart cannot repeat model
// work or consume report numbers. The rows keep the attempt spent, unlike
// those a shutdown releases: a process that died with an evaluation in flight
// may have died of the posting, and the two attempts bound one that keeps
// taking the server down. It returns how many rows it released.
func (e *Evaluator) RecoverInterrupted(ctx context.Context) (int, error) {
	now := e.stamp()
	err := e.d.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE runs SET status = 'failed', stage = '', failure_reason = 'interrupted', updated_at = ?1, finished_at = ?1 WHERE kind = 'evaluate' AND status IN ('queued', 'running')`,
			`UPDATE evaluation_jobs SET outcome = 'failed', finished_at = ?1 WHERE outcome IS NULL AND run_id IN (SELECT id FROM runs WHERE kind = 'evaluate' AND failure_reason = 'interrupted')`,
		} {
			if _, err := tx.ExecContext(ctx, q, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || e.d.Inbox == nil {
		return 0, err
	}
	return e.d.Inbox.ReleaseQueued(ctx, "evaluation interrupted by a restart")
}

// LLMConcurrency returns the pool's concurrency function: the in-flight call
// cap of the provider kind the deep-eval task is routed to.
func LLMConcurrency(r *llm.Router, e *Evaluator) func(context.Context) int {
	return func(ctx context.Context) int {
		res, err := r.Resolve(ctx, "deep-eval")
		if err != nil {
			return 4
		}
		if res.Provider == llm.OfflineProviderID {
			return 8
		}
		s, err := llm.LoadSettings(ctx, e.d.Store)
		if err != nil {
			return 4
		}
		if p, ok := s.Provider(res.Provider); ok {
			return p.Concurrency()
		}
		return 4
	}
}

// RunNew creates a job for the request and runs it to its outcome in the
// calling goroutine, outside the pool.
func (e *Evaluator) RunNew(ctx context.Context, r Request) (Result, error) {
	r, key, err := prepare(r)
	if err != nil {
		return Result{}, err
	}
	id, err := e.createJob(ctx, r, key, newIdempotencyKey())
	if err != nil {
		return Result{}, err
	}
	return e.Run(ctx, id, r, key), nil
}

// addOrigin runs under the pool lock and keeps repeated callers idempotent.
func (j *pending) addOrigin(req Request) {
	o := originOf(req)
	if o == (outcomeOrigin{}) || j.seen[o] {
		return
	}
	j.seen[o] = true
	j.joined = append(j.joined, o)
}

func (p *Pool) settleOrigin(runID string, origin outcomeOrigin, res Result) {
	j := &jobRun{e: p.ev, runID: runID, req: Request{InboxRowID: origin.inbox, ScanRun: origin.scan}}
	j.settle(res)
}
