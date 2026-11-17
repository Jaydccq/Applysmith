// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/llm"
)

// stubModel blocks every call until it is released, and records concurrency.
type stubModel struct {
	mu        sync.Mutex
	active    int
	maxActive int
	order     []string
	release   chan struct{}
}

func newStub() *stubModel { return &stubModel{release: make(chan struct{}, 64)} }

func (s *stubModel) Structured(ctx context.Context, r llm.Request, _ json.RawMessage, out any) (*llm.Response, error) {
	s.mu.Lock()
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	s.order = append(s.order, r.RunID)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active--; s.mu.Unlock() }()
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	text := deepReply(deepOpts{})
	if r.Task == "quick-screen" {
		text = screenReply(4.6, "advance", "")
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return nil, err
	}
	return &llm.Response{Provider: "stub"}, nil
}

func (s *stubModel) entered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *stubModel) peak() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxActive
}

func (s *stubModel) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

// waitFor polls cond until it holds. The deadline only stops a hang: the
// conditions are states a job reaches, not times, so it is set well past what
// a loaded machine needs.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func startPool(t *testing.T, e *tenv, opts PoolOptions) *Pool {
	t.Helper()
	p := NewPool(e.ev, opts)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func submit(t *testing.T, p *Pool, marker string, mods ...func(*Request)) Handle {
	t.Helper()
	h, err := p.Submit(context.Background(), req(marker, mods...))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func scan(r *Request) { r.Priority = PriorityScan }

func TestPool_ProviderLimitBoundsJobsInFlight(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 2 }})
	var hs []Handle
	for i := 0; i < 5; i++ {
		hs = append(hs, submit(t, p, fmt.Sprintf("lim%d", i)))
	}
	// Both active jobs must be inside the model before anything is released,
	// or the first could finish before the second arrives.
	waitFor(t, "two jobs inside the model", func() bool { return stub.entered() == 2 })
	if st := p.Stats(); st.Active != 2 || st.Queued != 3 {
		t.Fatalf("stats = %+v", st)
	}
	for i := 0; i < 5; i++ {
		stub.release <- struct{}{}
	}
	for _, h := range hs {
		if res, err := p.Wait(context.Background(), h.RunID); err != nil || res.Outcome != OutcomeReported {
			t.Fatalf("result %+v err %v", res, err)
		}
	}
	if got := stub.peak(); got != 2 {
		t.Fatalf("max in flight = %d", got)
	}
}

func TestPool_TotalIsCappedAtEight(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 64 }})
	for i := 0; i < 12; i++ {
		submit(t, p, fmt.Sprintf("cap%d", i))
	}
	waitFor(t, "eight jobs active", func() bool { return p.Stats().Active == PoolMaxJobs })
	time.Sleep(30 * time.Millisecond)
	if st := p.Stats(); st.Active != PoolMaxJobs || st.Queued != 4 {
		t.Fatalf("stats = %+v", st)
	}
	for i := 0; i < 12; i++ {
		stub.release <- struct{}{}
	}
}

func TestPool_QueueFull(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 1 }, QueueMax: 3})
	submit(t, p, "qf0")
	waitFor(t, "first job active", func() bool { return p.Stats().Active == 1 })
	for i := 1; i <= 3; i++ {
		submit(t, p, fmt.Sprintf("qf%d", i))
	}
	_, err := p.Submit(context.Background(), req("qf4"))
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v", err)
	}
	for i := 0; i < 4; i++ {
		stub.release <- struct{}{}
	}
}

func TestPool_UserRequestOvertakesScanRequests(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 1 }})
	first := submit(t, p, "pr0", scan)
	waitFor(t, "first job active", func() bool { return p.Stats().Active == 1 })
	var scans []Handle
	for i := 1; i <= 20; i++ {
		scans = append(scans, submit(t, p, fmt.Sprintf("pr%d", i), scan))
	}
	user := submit(t, p, "pru")
	stub.release <- struct{}{}
	waitFor(t, "second call", func() bool { return stub.calls() == 2 })
	stub.mu.Lock()
	second := stub.order[1]
	stub.mu.Unlock()
	if second != user.RunID {
		t.Fatalf("second job = %s, want the user's %s (first %s, scans %v)", second, user.RunID, first.RunID, scans[0].RunID)
	}
	for i := 0; i < 21; i++ {
		stub.release <- struct{}{}
	}
}

func TestPool_ConcurrentIdenticalRequestsCollapseIntoOneEvaluation(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{})
	a := submit(t, p, "same")
	waitFor(t, "evaluating", func() bool { return stub.calls() == 1 })
	b := submit(t, p, "same")
	if !b.Attached || b.RunID != a.RunID {
		t.Fatalf("a %+v b %+v", a, b)
	}
	stub.release <- struct{}{}
	ra, err1 := p.Wait(context.Background(), a.RunID)
	rb, err2 := p.Wait(context.Background(), b.RunID)
	if err1 != nil || err2 != nil || ra.ReportNumber != rb.ReportNumber || ra.ReportNumber == 0 {
		t.Fatalf("a %+v b %+v", ra, rb)
	}
	if stub.calls() != 1 || e.count("report_index") != 1 {
		t.Fatalf("calls %d reports %d", stub.calls(), e.count("report_index"))
	}
}

func TestPool_RestartEndsLiveJobsInterruptedAndReleasesInboxRowsWithoutRedispatch(t *testing.T) {
	e := newTenv(t)
	var ids []string
	for i := 1; i <= 3; i++ {
		r, _ := NormalizeRequest(req(fmt.Sprintf("rs%d", i), func(r *Request) { r.InboxRowID = int64(i) }))
		id, err := e.ev.createJob(context.Background(), r, requestKey(r), newIdempotencyKey())
		if err != nil {
			t.Fatal(err)
		}
		if err := e.ev.setPhase(context.Background(), id, PhaseEvaluating); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		e.inbox.queued[int64(i)] = true
	}
	runsBefore := e.count("runs")
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := NewPool(e.ev, PoolOptions{})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i, id := range ids {
		var status, reason string
		_ = e.st.Read().QueryRow(`SELECT status, failure_reason FROM runs WHERE id = ?`, id).Scan(&status, &reason)
		if status != "failed" || reason != ReasonInterrupted {
			t.Fatalf("run %s: %s %s", id, status, reason)
		}
		if !strings.Contains(e.inbox.released[int64(i+1)], "interrupted") {
			t.Fatalf("inbox row %d: %v", i+1, e.inbox.released)
		}
		got, _, _, _ := JobOutcome(context.Background(), e.st, id)
		if got.Outcome != OutcomeFailed {
			t.Fatalf("outcome = %+v", got)
		}
	}
	if e.count("runs") != runsBefore || stub.calls() != 0 || e.count("report_index") != 0 {
		t.Fatal("nothing may be re-dispatched or create a run or a report number")
	}
	// A second start releases nothing twice.
	e.inbox.released = map[int64]string{}
	if n, err := e.ev.RecoverInterrupted(context.Background()); err != nil || n != 0 || len(e.inbox.released) != 0 {
		t.Fatalf("second recovery: %d %v %v", n, err, e.inbox.released)
	}
}

func TestPool_StoreReconcileAlreadyEndsEvaluationRunsFailed(t *testing.T) {
	e := newTenv(t)
	r, _ := NormalizeRequest(req("rc0", func(r *Request) { r.InboxRowID = 5 }))
	id, _ := e.ev.createJob(context.Background(), r, requestKey(r), newIdempotencyKey())
	e.inbox.queued[5] = true
	_ = e.ev.setPhase(context.Background(), id, PhaseEvaluating)
	if err := e.st.ReconcileStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	_ = e.st.Read().QueryRow(`SELECT status, failure_reason FROM runs WHERE id = ?`, id).Scan(&status, &reason)
	if status != "failed" || reason != ReasonInterrupted {
		t.Fatalf("%s %s", status, reason)
	}
	if n, err := e.ev.RecoverInterrupted(context.Background()); err != nil || n != 1 || e.inbox.released[5] == "" {
		t.Fatalf("recovery after the generic reconcile: %d %v %v", n, err, e.inbox.released)
	}
}

func TestPool_WholeJobTimeoutWritesNothing(t *testing.T) {
	old := jobTimeout
	jobTimeout = 60 * time.Millisecond
	t.Cleanup(func() { jobTimeout = old })
	e := newTenv(t)
	e.model = newStub()
	e.rebuild()
	p := startPool(t, e, PoolOptions{})
	h := submit(t, p, "to", func(r *Request) { r.InboxRowID = 4 })
	res, err := p.Wait(context.Background(), h.RunID)
	if err != nil || res.Outcome != OutcomeFailed || res.Reason != ReasonTimeout {
		t.Fatalf("result %+v err %v", res, err)
	}
	if e.count("report_index") != 0 || len(e.entries()) != 0 || e.inbox.released[4] == "" {
		t.Fatal("a timed-out job writes no report or tracker row and frees its inbox row")
	}
	var arts int
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM report_artifacts`).Scan(&arts)
	if arts != 0 {
		t.Fatal("no report number may be consumed")
	}
}

func TestPool_CancelStopsAnActiveAndAQueuedJobWithoutAReport(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 1 }})
	active := submit(t, p, "ca0")
	waitFor(t, "active", func() bool { return p.Stats().Active == 1 })
	queued := submit(t, p, "ca1")
	if !p.Cancel(queued.RunID) {
		t.Fatal("queued job not found")
	}
	if !p.Cancel(active.RunID) {
		t.Fatal("active job not found")
	}
	for _, h := range []Handle{active, queued} {
		res, err := p.Wait(context.Background(), h.RunID)
		if err != nil || res.Outcome != OutcomeFailed || res.Reason != ReasonCancelled {
			t.Fatalf("result %+v err %v", res, err)
		}
		var status string
		_ = e.st.Read().QueryRow(`SELECT status FROM runs WHERE id = ?`, h.RunID).Scan(&status)
		if status != "cancelled" {
			t.Fatalf("run status = %s", status)
		}
	}
	if e.count("report_index") != 0 {
		t.Fatal("a cancelled job writes no report")
	}
	if p.Cancel("run_missing") {
		t.Fatal("an unknown run cannot be cancelled")
	}
}

func TestPool_SubmitValidatesAndDefaults(t *testing.T) {
	e := newTenv(t)
	p := startPool(t, e, PoolOptions{})
	if _, err := p.Submit(context.Background(), Request{}); err == nil {
		t.Fatal("an empty request is refused")
	}
	if _, err := p.Submit(context.Background(), Request{URL: "https://jobs.example.test/p/x", Mode: "nope"}); err == nil {
		t.Fatal("an unknown mode is refused")
	}
	r, _ := NormalizeRequest(Request{URL: "https://jobs.example.test/p/x"})
	if r.Mode != ModeDeep || r.Priority != PriorityUser {
		t.Fatalf("defaults = %+v", r)
	}
	cold := NewPool(e.ev, PoolOptions{})
	if _, err := cold.Submit(context.Background(), req("cold")); err == nil {
		t.Fatal("a pool that has not started refuses work")
	}
}

// doneProbe is a context that says when it is first asked for Done.
type doneProbe struct {
	context.Context
	once  sync.Once
	asked chan struct{}
}

func (c *doneProbe) Done() <-chan struct{} {
	c.once.Do(func() { close(c.asked) })
	return c.Context.Done()
}

func TestPool_StatsAnswersWhileASubmitWaitsForTheWriter(t *testing.T) {
	e := newTenv(t)
	p := startPool(t, e, PoolOptions{})
	hold, started := make(chan struct{}), make(chan struct{})
	go func() {
		_ = e.st.Write(context.Background(), func(*sql.Tx) error { close(started); <-hold; return nil })
	}()
	<-started
	submitted := make(chan error, 1)
	probe := &doneProbe{Context: context.Background(), asked: make(chan struct{})}
	go func() {
		_, err := p.Submit(probe, req("slowwrite"))
		submitted <- err
	}()
	// The store's Write derives its wait for the writer from the caller's
	// context, which asks for Done: from then on the submit waits on the
	// held writer.
	select {
	case <-probe.asked:
	case <-time.After(30 * time.Second):
		t.Fatal("the submit never reached the database write")
	}
	got := make(chan Stats, 1)
	go func() { got <- p.Stats() }()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("Stats blocked behind a submit's database write")
	}
	close(hold)
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
}

func TestPool_TheConcurrencyLimitIsReadOutsideThePoolLock(t *testing.T) {
	e := newTenv(t)
	var p *Pool
	p = startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { p.Stats(); return 2 }})
	done := make(chan error, 1)
	go func() {
		_, err := p.Submit(context.Background(), req("limitlock"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the limit is read while the pool lock is held")
	}
}

func TestPool_CloseSettlesQueuedJobs(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := NewPool(e.ev, PoolOptions{Concurrency: func(context.Context) int { return 1 }})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	submit(t, p, "cq0")
	waitFor(t, "active", func() bool { return stub.entered() == 1 })
	queued := submit(t, p, "cq1", func(r *Request) { r.InboxRowID = 8 })
	p.Close()
	waited := make(chan Result, 1)
	go func() {
		res, _ := p.Wait(context.Background(), queued.RunID)
		waited <- res
	}()
	select {
	case res := <-waited:
		if res.Outcome != OutcomeFailed || res.Reason != ReasonInterrupted {
			t.Fatalf("result = %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait on a job that was queued at shutdown never returns")
	}
	var status string
	_ = e.st.Read().QueryRow(`SELECT status FROM runs WHERE id = ?`, queued.RunID).Scan(&status)
	if status != "failed" || e.inbox.released[8] == "" {
		t.Fatalf("run %s, inbox released %v", status, e.inbox.released)
	}
}

func TestPool_DrainWaitsForEveryJob(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 1 }})
	var hs []Handle
	for i := 0; i < 3; i++ {
		hs = append(hs, submit(t, p, fmt.Sprintf("dr%d", i)))
	}
	drained := make(chan error, 1)
	go func() { drained <- p.Drain(context.Background()) }()
	select {
	case <-drained:
		t.Fatal("Drain returned while jobs were still queued")
	case <-time.After(50 * time.Millisecond):
	}
	for i := 0; i < 3; i++ {
		stub.release <- struct{}{}
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	for _, h := range hs {
		if res, err := p.Wait(context.Background(), h.RunID); err != nil || res.Outcome != OutcomeReported {
			t.Fatalf("result %+v err %v", res, err)
		}
	}
}

func TestPool_DrainStopsWhenItsContextEnds(t *testing.T) {
	e := newTenv(t)
	e.model = newStub()
	e.rebuild()
	p := startPool(t, e, PoolOptions{})
	submit(t, p, "drc")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := p.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestPool_QueuedSettlementRemainsWaitable(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	entered, release := make(chan struct{}), make(chan struct{})
	e.ev.d.ScanOutcome = func(_ context.Context, scan, job string, res Result) error {
		if scan == "blocked" {
			close(entered)
			<-release
		}
		return nil
	}
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 1 }})
	active := submit(t, p, "review-settle-active")
	waitFor(t, "active model", func() bool { return stub.calls() == 1 })
	queued := submit(t, p, "review-settle-queued", func(r *Request) { r.ScanRun = "blocked" })
	joined, err := p.Submit(t.Context(), req("review-settle-queued", func(r *Request) { r.ScanRun = "joined" }))
	if err != nil || !joined.Attached {
		t.Fatalf("join=%+v %v", joined, err)
	}
	cancelDone := make(chan struct{})
	go func() { p.Cancel(queued.RunID); close(cancelDone) }()
	<-entered
	defer func() { close(release); <-cancelDone }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	res, err := p.Wait(ctx, queued.RunID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait returned before queued/joined settlement: result=%+v err=%v", res, err)
	}
	p.Cancel(active.RunID)
	if _, err := p.Wait(t.Context(), active.RunID); err != nil {
		t.Fatal(err)
	}
	dctx, dcancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer dcancel()
	if err := p.Drain(dctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Drain missed still-settling queued job: %v", err)
	}
}

func TestPool_CloseWaitsForConcurrentQueuedCancellation(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	entered, release := make(chan struct{}), make(chan struct{})
	e.ev.d.ScanOutcome = func(_ context.Context, scan, job string, res Result) error {
		if scan == "blocked" {
			close(entered)
			<-release
		}
		return nil
	}
	p := startPool(t, e, PoolOptions{Concurrency: func(context.Context) int { return 1 }})
	submit(t, p, "review-close-active")
	waitFor(t, "active model", func() bool { return stub.calls() == 1 })
	queued := submit(t, p, "review-close-queued", func(r *Request) { r.ScanRun = "blocked" })
	cancelDone := make(chan struct{})
	go func() { p.Cancel(queued.RunID); close(cancelDone) }()
	<-entered
	closeDone := make(chan struct{})
	go func() { p.Close(); close(closeDone) }()
	defer func() { close(release); <-cancelDone; <-closeDone }()
	if !p.Cancel(queued.RunID) {
		t.Error("settling cancellation should remain idempotent")
	}
	select {
	case <-closeDone:
		t.Error("Close returned while queued cancellation was settling")
	case <-time.After(40 * time.Millisecond):
	}
}

func TestPool_KeyedOriginAfterJobCompletion(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	var results []Result
	e.ev.d.ScanOutcome = func(_ context.Context, scan, job string, res Result) error {
		if scan != "late" {
			t.Errorf("unexpected scan=%s", scan)
		}
		results = append(results, res)
		return nil
	}
	p := startPool(t, e, PoolOptions{})
	h := submit(t, p, "review-keyed-late")
	waitFor(t, "active model", func() bool { return stub.calls() == 1 })
	p.mu.Lock()
	job := p.byRun[h.RunID]
	p.mu.Unlock()
	stub.release <- struct{}{}
	original, err := p.Wait(t.Context(), h.RunID)
	if err != nil {
		t.Fatal(err)
	}
	late := req("review-keyed-late", func(r *Request) { r.ScanRun = "late" })
	p.attachOrigin(job, late)
	p.attachOrigin(job, late)
	if len(results) != 1 || results[0].Outcome != original.Outcome || results[0].ReportNumber != original.ReportNumber {
		t.Fatalf("late results=%+v original=%+v", results, original)
	}
}

// A request made once a job's run reads finished is a new evaluation: "Evaluate
// again" on a failure must not join the job that failed. The request is made
// from the final event, right after the outcome was recorded.
func TestPool_ARequestAfterTheOutcomeIsRecordedStartsANewJob(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	var p *Pool
	var once sync.Once
	var again Handle
	var againErr error
	e.ev.d.Emit = func(ev Event) {
		if ev.Final {
			once.Do(func() { again, againErr = p.Submit(context.Background(), req("again-after")) })
		}
	}
	p = startPool(t, e, PoolOptions{})
	first := submit(t, p, "again-after")
	for range 4 {
		stub.release <- struct{}{}
	}
	if _, err := p.Wait(t.Context(), first.RunID); err != nil {
		t.Fatal(err)
	}
	if againErr != nil || again.Attached || again.RunID == "" || again.RunID == first.RunID {
		t.Fatalf("first %+v again %+v %v", first, again, againErr)
	}
	if _, err := p.Wait(t.Context(), again.RunID); err != nil {
		t.Fatal(err)
	}
}
