package llm

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// pair builds a primary "a" and a fallback "b" over two servers, both with keys,
// and routes narrative a/m1 -> b/m2 with thinking off.
func pair(t *testing.T, a, b func(w http.ResponseWriter, r *http.Request, n int)) (*env, *oaServer, *oaServer) {
	t.Helper()
	e := newEnv(t)
	sa, sb := newOAServer(t, a), newOAServer(t, b)
	e.provider(oaProvider("a", sa), fakeKey("ka"))
	e.provider(oaProvider("b", sb), fakeKey("kb"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, Fallbacks: []Fallback{{"b", "m2"}}})
	return e, sa, sb
}

func okHandler(text string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) { oaStream(w, "stop", 10, 5, 0, text) }
}

func statusHandler(status int, body string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) { oaFail(w, status, body) }
}

func TestLLM_Walk_AdvancesOnlyOnFallbackKinds(t *testing.T) {
	hang := func(w http.ResponseWriter, r *http.Request, n int) { <-r.Context().Done() }
	cases := []struct {
		name    string
		primary func(http.ResponseWriter, *http.Request, int)
		advance bool
		kind    Kind
		stall   time.Duration
	}{
		{"429 rate limit", statusHandler(429, `{}`), true, KindRateLimited, 0},
		{"503 overloaded", statusHandler(503, `{}`), true, KindOverloaded, 0},
		{"404 model gone", statusHandler(404, `{}`), true, KindModelGone, 0},
		{"400 model unavailable body", statusHandler(400, `{"error":{"message":"The model m1 does not exist"}}`), true, KindModelGone, 0},
		{"empty completion", func(w http.ResponseWriter, r *http.Request, n int) { oaStream(w, "stop", 3, 0, 0) }, true, KindEmptyCompletion, 0},
		{"network reset", func(w http.ResponseWriter, r *http.Request, n int) { panic(http.ErrAbortHandler) }, true, KindNetwork, 0},
		{"stalled stream", hang, true, KindStreamStalled, 150 * time.Millisecond},
		{"401 auth", statusHandler(401, `{}`), false, KindAuth, 0},
		{"400 bad request", statusHandler(400, `{"error":{"message":"max_tokens is too large"}}`), false, KindBadRequest, 0},
		{"501 not advancing", statusHandler(501, `{}`), false, KindOther, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, sa, sb := pair(t, c.primary, okHandler("from b"))
			if c.stall > 0 {
				e.r.stall = c.stall
			}
			resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
			rows := e.ledger()
			if c.advance {
				if err != nil || resp.Text != "from b" || resp.Provider != "b" || !resp.Fallback {
					t.Fatalf("resp = %+v, err = %v", resp, err)
				}
				if len(rows) != 2 || rows[0].Outcome != string(c.kind) || rows[1].Outcome != "ok" || rows[1].Source != "fallback" || rows[1].Replaced != "a/m1" {
					t.Fatalf("ledger = %+v", rows)
				}
				return
			}
			wantKind(t, err, c.kind)
			if sb.count() != 0 || sa.count() != 1 || len(rows) != 1 || rows[0].Outcome != string(c.kind) {
				t.Fatalf("fallbacks were tried: b=%d ledger=%+v", sb.count(), rows)
			}
		})
	}
}

// keepAlive streams empty events forever: the stream is alive but never ends.
func keepAlive(w http.ResponseWriter, r *http.Request, n int) {
	w.Header().Set("Content-Type", "text/event-stream")
	for r.Context().Err() == nil {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{}}]}\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLLM_Walk_TimeoutAdvances(t *testing.T) {
	e, _, _ := pair(t, keepAlive, okHandler("from b"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, TimeoutSeconds: 1, Fallbacks: []Fallback{{"b", "m2"}}})
	e.r.stall = time.Minute
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "b" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	if rows := e.ledger(); rows[0].Outcome != "timeout" {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Walk_RateLimitAdvancesAndRecordsTwoRows(t *testing.T) {
	e, _, _ := pair(t, statusHandler(429, `{}`), okHandler("from b"))
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	rows := e.ledger()
	if len(rows) != 2 || rows[0].Status != 429 || rows[0].Outcome != "rate-limited" || rows[1].Source != "fallback" {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Walk_AllFailCarriesEachAttempt(t *testing.T) {
	e, _, _ := pair(t, statusHandler(429, `{"error":"slow down"}`), statusHandler(503, `{"error":"busy"}`))
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	ce := wantKind(t, err, KindOverloaded)
	if len(ce.Attempts) != 2 || ce.Attempts[0].Kind != KindRateLimited || ce.Attempts[1].Kind != KindOverloaded || ce.Attempts[0].Message == "" {
		t.Fatalf("attempts = %+v", ce.Attempts)
	}
}

func TestLLM_Walk_CallerCancelsAbortsUpstream(t *testing.T) {
	e, _, sb := pair(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, okHandler("from b"))
	ctx, cancel := context.WithCancel(bg())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := e.r.Complete(ctx, Request{Task: "narrative", Prompt: "hi"})
	wantKind(t, err, KindAborted)
	if d := time.Since(start); d > 30*time.Second { // unaborted, the stall budget of 45 s ends it
		t.Fatalf("abort took %v", d)
	}
	if sb.count() != 0 {
		t.Fatal("a fallback was tried after a cancel")
	}
}

func TestLLM_Walk_FailureAfterStreamingBeganReturnsTheError(t *testing.T) {
	brokenStream := func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	t.Run("content reached the caller", func(t *testing.T) {
		e, _, sb := pair(t, brokenStream, okHandler("from b"))
		var got strings.Builder
		_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", OnDelta: func(s string) { got.WriteString(s) }})
		wantKind(t, err, KindNetwork)
		if got.String() != "par" || sb.count() != 0 {
			t.Fatalf("delivered %q, fallback calls %d", got.String(), sb.count())
		}
	})
	t.Run("nothing reached the caller", func(t *testing.T) {
		e, _, _ := pair(t, brokenStream, okHandler("from b"))
		resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		if err != nil || resp.Provider != "b" {
			t.Fatalf("resp = %+v err = %v", resp, err)
		}
	})
}

func TestLLM_Unconfigured_NoRequestThenFallback(t *testing.T) {
	e := newEnv(t)
	sa, sb := newOAServer(t, okHandler("from a")), newOAServer(t, okHandler("from b"))
	e.provider(oaProvider("a", sa), "") // no key
	e.provider(oaProvider("b", sb), fakeKey("kb"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, Fallbacks: []Fallback{{"b", "m2"}}})
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "b" || sa.count() != 0 {
		t.Fatalf("resp = %+v err = %v primary calls = %d", resp, err, sa.count())
	}
	if rows := e.ledger(); len(rows) != 2 || rows[0].Outcome != "unconfigured" {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Unconfigured_AloneFailsBeforeAnyRequest(t *testing.T) {
	e := newEnv(t)
	s := newOAServer(t, okHandler("x"))
	e.provider(oaProvider("a", s), "")
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	wantKind(t, err, KindUnconfigured)
	if s.count() != 0 {
		t.Fatal("a request was made without a key")
	}
}

func TestLLM_EmptyLengthCompletion_AdvancesNonEmptyTruncatedDoesNot(t *testing.T) {
	t.Run("reasoning ate the ceiling", func(t *testing.T) {
		e, _, _ := pair(t, func(w http.ResponseWriter, r *http.Request, n int) { oaStream(w, "length", 10, 100, 0) }, okHandler("from b"))
		resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		if err != nil || resp.Provider != "b" {
			t.Fatalf("resp = %+v err = %v", resp, err)
		}
		if rows := e.ledger(); rows[0].Outcome != "empty-completion" || rows[0].Finish != "length" {
			t.Fatalf("ledger = %+v", rows)
		}
	})
	t.Run("truncated reply with text", func(t *testing.T) {
		e, _, sb := pair(t, func(w http.ResponseWriter, r *http.Request, n int) { oaStream(w, "length", 10, 100, 0, "half an ans") }, okHandler("from b"))
		_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		ce := wantKind(t, err, KindTruncated)
		if ce.Partial != "half an ans" || sb.count() != 0 {
			t.Fatalf("partial %q, fallback calls %d", ce.Partial, sb.count())
		}
	})
}

func TestLLM_Override_SingleCandidateStillLedgered(t *testing.T) {
	e, _, sb := pair(t, statusHandler(429, `{}`), okHandler("from b"))
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", Model: "a/m9"})
	wantKind(t, err, KindRateLimited)
	if sb.count() != 0 {
		t.Fatal("an override walked to a fallback")
	}
	rows := e.ledger()
	if len(rows) != 1 || rows[0].Task != "narrative" || rows[0].Source != "override" || rows[0].Model != "m9" {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Deadline_ClampsAttemptAndMarksLedger(t *testing.T) {
	e := newEnv(t)
	s := newOAServer(t, keepAlive)
	e.provider(oaProvider("a", s), fakeKey("ka"))
	e.route("deep-eval", Route{Provider: "a", Model: "m1", Thinking: ThinkOff}) // row timeout is 600 s
	start := time.Now()
	_, err := e.r.Complete(bg(), Request{Task: "deep-eval", Prompt: "hi", Deadline: time.Now().Add(900 * time.Millisecond)})
	if err == nil {
		t.Fatal("want a failure")
	}
	if d := time.Since(start); d > 30*time.Second { // the row timeout is 600 s
		t.Fatalf("the attempt ran %v", d)
	}
	rows := e.ledger()
	if len(rows) != 1 || !rows[0].Clamped || rows[0].Outcome != "timeout" {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Deadline_BudgetSpentStopsTheWalk(t *testing.T) {
	e, _, sb := pair(t, keepAlive, okHandler("from b"))
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", Deadline: time.Now().Add(900 * time.Millisecond)})
	wantKind(t, err, KindDeadline)
	if sb.count() != 0 {
		t.Fatal("a fallback started after the budget was spent")
	}
}

func TestLLM_Walk_NoAttemptBelowFloor_DeadlineStopWritesDistinctRow(t *testing.T) {
	e, _, sb := pair(t, statusHandler(503, `{}`), okHandler("from b"))
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", Deadline: time.Now().Add(300 * time.Millisecond)})
	if !isDeadline(err) {
		t.Fatalf("err = %v", err)
	}
	// The floor applies at the first attempt too: 300 ms is under 500 ms, so
	// the primary is the unreached candidate.
	rows := e.ledger()
	if len(rows) != 1 || rows[0].Outcome != "deadline-exhausted" || rows[0].Provider != "a" || sb.count() != 0 {
		t.Fatalf("ledger = %+v", rows)
	}
}

func isDeadline(err error) bool { return KindOf(err) == KindDeadline }

func TestLLM_Walk_FallbackNotStartedWithLessThanFloorLeft(t *testing.T) {
	slow503 := func(w http.ResponseWriter, r *http.Request, n int) {
		time.Sleep(700 * time.Millisecond)
		oaFail(w, 503, `{}`)
	}
	e, _, sb := pair(t, slow503, okHandler("from b"))
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", Deadline: time.Now().Add(1100 * time.Millisecond)})
	if !isDeadline(err) {
		t.Fatalf("err = %v", err)
	}
	rows := e.ledger()
	if len(rows) != 2 || rows[0].Outcome != "overloaded" || rows[1].Outcome != "deadline-exhausted" || rows[1].Provider != "b" || sb.count() != 0 {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Stall_RowTimeoutShorterThanTheStallBudget(t *testing.T) {
	// A row timeout shorter than the stall budget is the budget: a stream silent
	// for it is stalled, not timed out (llm.md, "Deadlines").
	e, _, _ := pair(t, func(w http.ResponseWriter, r *http.Request, n int) { <-r.Context().Done() }, okHandler("from b"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, TimeoutSeconds: 1, Fallbacks: []Fallback{{"b", "m2"}}})
	e.r.stall = time.Minute
	start := time.Now()
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if rows := e.ledger(); rows[0].Outcome != string(KindStreamStalled) || time.Since(start) > 30*time.Second {
		t.Fatalf("outcome = %s after %v, want stream-stalled at the 1 s row timeout", rows[0].Outcome, time.Since(start))
	}
}

func TestLLM_RateLimit_ShortRetryAfterWaitedOnce(t *testing.T) {
	var slept []time.Duration
	e, sa, sb := pair(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "3")
			oaFail(w, 429, `{}`)
			return
		}
		oaStream(w, "stop", 1, 1, 0, "from a")
	}, okHandler("from b"))
	e.r.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "a" || resp.Fallback || sa.count() != 2 || sb.count() != 0 {
		t.Fatalf("resp = %+v err = %v a=%d b=%d", resp, err, sa.count(), sb.count())
	}
	if len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("slept = %v", slept)
	}
	if rows := e.ledger(); len(rows) != 2 || rows[1].Replaced != "" || rows[1].Provider != "a" {
		t.Fatalf("ledger = %+v", rows)
	}
	if got := e.reasons(); got != ","+ReasonRetry {
		t.Fatalf("reasons = %q: the retry on the same candidate is not a fallback", got)
	}
	byTask, byProv, err := e.r.CostReport(bg(), 1)
	if err != nil || byTask[0].FallbackRate != 0 || byProv[0].FallbackRate != 0 {
		t.Fatalf("report counts the retry as a fallback: task %+v provider %+v err %v", byTask, byProv, err)
	}
}

func TestLLM_RateLimit_LongOrRepeatedDelayAdvances(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry string
	}{{"long delay", "30"}, {"no delay", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			e, sa, _ := pair(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				oaFail(w, 429, `{}`)
			}, okHandler("from b"))
			slept := 0
			e.r.sleep = func(context.Context, time.Duration) error { slept++; return nil }
			resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
			if err != nil || resp.Provider != "b" || sa.count() != 1 || slept != 0 {
				t.Fatalf("resp = %+v err = %v a=%d slept=%d", resp, err, sa.count(), slept)
			}
		})
	}
	t.Run("only once", func(t *testing.T) {
		e, sa, _ := pair(t, func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("Retry-After", "1")
			oaFail(w, 429, `{}`)
		}, okHandler("from b"))
		resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		if err != nil || resp.Provider != "b" || sa.count() != 2 {
			t.Fatalf("resp = %+v err = %v a=%d", resp, err, sa.count())
		}
	})
}

// Four calls fill the openai cap and are held; three more queue one at a time
// and, as slots free one by one, reach the server in the order they queued,
// never more than four at once.
func TestLLM_Concurrency_CapRespectedAndWaitersServedInOrder(t *testing.T) {
	var mu sync.Mutex
	inflight, peak := 0, 0
	var order []string
	hold := make(chan struct{})
	e := newEnv(t)
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = decodeBody(r, &body)
		mu.Lock()
		inflight++
		peak = max(peak, inflight)
		order = append(order, body.Messages[len(body.Messages)-1].Content)
		mu.Unlock()
		<-hold
		mu.Lock()
		inflight--
		mu.Unlock()
		oaStream(w, "stop", 1, 1, 0, "ok")
	})
	e.provider(oaProvider("a", s), fakeKey("ka"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
	var wg sync.WaitGroup
	call := func(prompt string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: prompt}); err != nil {
				t.Error(err)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		call("held")
	}
	for s.count() < 4 {
		time.Sleep(5 * time.Millisecond)
	}
	l := e.r.limiterFor("a")
	for i, p := range []string{"w1", "w2", "w3"} {
		call(p)
		waitWaiters(t, l, i+1)
	}
	// Free one slot at a time, so each grant reaches the server before the next.
	for n := 5; n <= 7; n++ {
		hold <- struct{}{}
		for s.count() < n {
			time.Sleep(time.Millisecond)
		}
	}
	close(hold)
	wg.Wait()
	if peak != 4 {
		t.Fatalf("peak in-flight = %d, want the cap of 4", peak)
	}
	if got := strings.Join(order[4:], ","); got != "w1,w2,w3" {
		t.Fatalf("waiters served as %s, want w1,w2,w3", got)
	}
}

func TestLLM_Concurrency_WaitCountsTowardTheDeadline(t *testing.T) {
	e := newEnv(t)
	hold := make(chan struct{})
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) { <-hold; oaStream(w, "stop", 1, 1, 0, "ok") })
	p := oaProvider("a", s)
	p.MaxConcurrency = 1
	e.provider(p, fakeKey("ka"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
	first := make(chan error, 1)
	go func() {
		_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		first <- err
	}()
	for s.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", Deadline: time.Now().Add(700 * time.Millisecond)})
	close(hold)
	if !isDeadline(err) || s.count() != 1 {
		t.Fatalf("err = %v, requests = %d", err, s.count())
	}
	if err := <-first; err != nil {
		t.Fatalf("the call holding the slot: %v", err)
	}
}

func TestLLM_Unknown_TaskMakesNoRequest(t *testing.T) {
	e, sa, _ := pair(t, okHandler("x"), okHandler("y"))
	_, err := e.r.Complete(bg(), Request{Task: "self-heal", Prompt: "hi"})
	wantKind(t, err, KindUnknownTask)
	if sa.count() != 0 || len(e.ledger()) != 0 {
		t.Fatal("an unknown task reached a provider")
	}
}

func TestLLM_Rest_ARestingPrimaryGoesBehindAndComesBack(t *testing.T) {
	e, sa, sb := pair(t, statusHandler(503, `{}`), okHandler("from b"))
	call := func() *Response {
		t.Helper()
		resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	call()
	if rests := e.r.Rests(); len(rests) != 1 || rests[0].Provider != "a" || rests[0].Model != "m1" || rests[0].Why != RestUnavailable || rests[0].Failures != 1 {
		t.Fatalf("rests = %+v", rests)
	}
	e.clock.advance(30 * time.Second)
	call()
	rows := e.ledger()
	if sa.count() != 1 || sb.count() != 2 || len(rows) != 3 || rows[2].Provider != "b" || rows[2].Attempt != 1 {
		t.Fatalf("a=%d b=%d ledger=%s", sa.count(), sb.count(), outcomes(rows))
	}
	if got := e.reasons(); got != ",fallback,rested" {
		t.Fatalf("reasons = %q", got)
	}
	// The rest is over: the configured primary is asked first again, fails
	// again and now rests two minutes.
	e.clock.advance(31 * time.Second)
	call()
	if sa.count() != 2 {
		t.Fatalf("a was not tried after its rest: %d", sa.count())
	}
	rests := e.r.Rests()
	if len(rests) != 1 || rests[0].Failures != 2 || rests[0].Until.Sub(e.clock.now()) != 2*time.Minute {
		t.Fatalf("rests = %+v", rests)
	}
}

func TestLLM_Rest_EveryCandidateRestingKeepsTheConfiguredOrder(t *testing.T) {
	e, sa, sb := pair(t, statusHandler(503, `{}`), statusHandler(503, `{}`))
	for i := 0; i < 2; i++ {
		if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err == nil {
			t.Fatal("want an error")
		}
	}
	if len(e.r.Rests()) != 2 || sa.count() != 2 || sb.count() != 2 {
		t.Fatalf("rests = %+v a=%d b=%d", e.r.Rests(), sa.count(), sb.count())
	}
	if got := outcomes(e.ledger()); got != "a:overloaded,b:overloaded,a:overloaded,b:overloaded" {
		t.Fatalf("ledger = %s", got)
	}
}

func TestLLM_Rest_SuccessClearsTheRestAndTheCount(t *testing.T) {
	healthy := false
	e, _, _ := pair(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if healthy {
			oaStream(w, "stop", 1, 1, 0, "from a")
			return
		}
		oaFail(w, 503, `{}`)
	}, okHandler("from b"))
	e.r.noteFailure(candidate{provider: ProviderConfig{ID: "a"}, model: "m1"}, &Error{Kind: KindTimeout})
	e.r.noteFailure(candidate{provider: ProviderConfig{ID: "a"}, model: "m1"}, &Error{Kind: KindTimeout})
	e.clock.advance(3 * time.Minute)
	healthy = true
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "a" || len(e.r.Rests()) != 0 {
		t.Fatalf("resp = %+v err = %v rests = %+v", resp, err, e.r.Rests())
	}
	healthy = false
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if rests := e.r.Rests(); len(rests) != 1 || rests[0].Failures != 1 {
		t.Fatalf("the failure count survived a success: %+v", rests)
	}
}

func TestLLM_Rest_Classes(t *testing.T) {
	cand := candidate{provider: ProviderConfig{ID: "a"}, model: "m1"}
	cases := []struct {
		name   string
		fail   Error
		times  int
		why    string
		wide   bool
		length time.Duration
	}{
		{"quota default", Error{Kind: KindQuota}, 1, RestQuota, true, 30 * time.Minute},
		{"quota with a reset time", Error{Kind: KindQuota, retryAfter: 7200}, 1, RestQuota, true, 2 * time.Hour},
		{"quota reset is capped", Error{Kind: KindQuota, retryAfter: 10 * 86400}, 1, RestQuota, true, 24 * time.Hour},
		{"429 with Retry-After", Error{Kind: KindRateLimited, retryAfter: 90}, 1, RestRateLimit, false, 90 * time.Second},
		{"429 Retry-After is capped at an hour", Error{Kind: KindRateLimited, retryAfter: 7200}, 1, RestRateLimit, false, time.Hour},
		{"short 429 starts at a minute", Error{Kind: KindRateLimited}, 1, RestRateLimit, false, time.Minute},
		{"short 429 stops at 30 minutes", Error{Kind: KindRateLimited}, 9, RestRateLimit, false, 30 * time.Minute},
		{"timeout doubles", Error{Kind: KindTimeout}, 3, RestUnavailable, false, 4 * time.Minute},
		{"5xx stops at 10 minutes", Error{Kind: KindOverloaded}, 6, RestUnavailable, false, 10 * time.Minute},
		{"connection failure", Error{Kind: KindNetwork}, 1, RestUnavailable, false, time.Minute},
		{"no connection could be made", Error{Kind: KindNetwork, Unreached: true}, 1, RestUnavailable, true, time.Minute},
		{"model gone", Error{Kind: KindModelGone}, 1, RestModelGone, false, 30 * time.Minute},
		{"401 is a dead key", Error{Kind: KindAuth, Status: 401}, 1, RestDeadKey, true, 30 * time.Minute},
		{"403 that names a revoked key", Error{Kind: KindAuth, Status: 403, Message: "the key was revoked"}, 1, RestDeadKey, true, 30 * time.Minute},
		{"403 without a verdict does not rest", Error{Kind: KindAuth, Status: 403, Message: "no access to this model"}, 1, "", false, 0},
		{"bad request does not rest", Error{Kind: KindBadRequest, Status: 400}, 1, "", false, 0},
		{"a missing key does not rest", Error{Kind: KindUnconfigured}, 1, "", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			for i := 0; i < c.times; i++ {
				f := c.fail
				e.r.noteFailure(cand, &f)
			}
			rests := e.r.Rests()
			if c.why == "" {
				if len(rests) != 0 {
					t.Fatalf("rested: %+v", rests)
				}
				return
			}
			want := Rest{Provider: "a", Model: "m1", Why: c.why, Failures: c.times, Until: e.clock.now().Add(c.length)}
			if c.wide {
				want.Model = ""
			}
			if len(rests) != 1 || rests[0] != want {
				t.Fatalf("rests = %+v, want %+v", rests, want)
			}
			// A whole-provider rest covers every model, a model rest only its own.
			other := candidate{provider: ProviderConfig{ID: "a"}, model: "m2"}
			if _, resting := e.r.restOf(other); resting != c.wide {
				t.Fatalf("another model of the provider rests = %v, want %v", resting, c.wide)
			}
		})
	}
}

// closedAddress is a local address nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestLLM_Rest_AFailureToConnectIsUnreachedAndAResetIsNot(t *testing.T) {
	_, err := http.Get("http://" + closedAddress(t) + "/")
	if f := classifyError(err); f.Kind != KindNetwork || !f.Unreached {
		t.Fatalf("refused connection = %+v", f)
	}
	// The resolver's answer for a name that does not exist; built, not looked up.
	err = &url.Error{Op: "Post", URL: "https://no-such-host.invalid/v1/chat/completions",
		Err: &net.DNSError{Err: "no such host", Name: "no-such-host.invalid", IsNotFound: true}}
	if f := classifyError(err); f.Kind != KindNetwork || !f.Unreached {
		t.Fatalf("unresolved name = %+v", f)
	}
	// Live: the connection opened, then the system timed out the packets it
	// could not deliver ("read: operation timed out" after 18 s); the path is
	// broken for every model.
	dropped := &url.Error{Op: "Post", URL: "https://api.example.test/chat/completions",
		Err: &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ETIMEDOUT)}}
	if f := classifyError(dropped); f.Kind != KindNetwork || !f.Unreached {
		t.Fatalf("timed-out path = %+v", f)
	}
	// A reset is the server's own answer: it was reached.
	reset := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	if f := classifyError(reset); f.Kind != KindNetwork || f.Unreached {
		t.Fatalf("reset = %+v", f)
	}
}

// A provider whose address cannot be reached rests with every model, so the
// next call on another of its models goes to the fallback without waiting on
// the connection again (live: every turn spent the dial timeout on a second
// model of an unreachable provider).
func TestLLM_Rest_AnUnreachableProviderRestsEveryModel(t *testing.T) {
	e := newEnv(t)
	sb := newOAServer(t, okHandler("from b"))
	e.provider(ProviderConfig{ID: "a", Kind: KindOpenAI, BaseURL: "http://" + closedAddress(t)}, fakeKey("ka"))
	e.provider(oaProvider("b", sb), fakeKey("kb"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, Fallbacks: []Fallback{{"b", "m2"}}})
	e.route("quick-screen", Route{Provider: "a", Model: "m3", Thinking: ThinkOff, Fallbacks: []Fallback{{"b", "m2"}}})
	if resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || resp.Provider != "b" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if resp, err := e.r.Complete(bg(), Request{Task: "quick-screen", Prompt: "hi"}); err != nil || resp.Provider != "b" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if got := outcomes(e.ledger()); got != "a:network,b:ok,b:ok" {
		t.Fatalf("ledger = %s", got)
	}
}

func TestLLM_Rest_FailuresAreForgottenAfterAQuietSpell(t *testing.T) {
	e := newEnv(t)
	cand := candidate{provider: ProviderConfig{ID: "a"}, model: "m1"}
	e.r.noteFailure(cand, &Error{Kind: KindTimeout})
	e.r.noteFailure(cand, &Error{Kind: KindTimeout})
	e.clock.advance(time.Hour)
	e.r.noteFailure(cand, &Error{Kind: KindTimeout})
	if rests := e.r.Rests(); len(rests) != 1 || rests[0].Failures != 1 {
		t.Fatalf("rests = %+v", rests)
	}
	e.r.ClearRests("a")
	if len(e.r.Rests()) != 0 {
		t.Fatal("ClearRests left a rest")
	}
}

func TestLLM_Rest_HTTPClassification(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		kind   Kind
	}{
		{402, `{}`, KindQuota},
		{429, `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`, KindQuota},
		{429, `{"error":{"message":"Rate limit reached for requests"}}`, KindRateLimited},
		{403, `{"error":{"message":"Key limit exceeded (total limit). Manage it using the keys page"}}`, KindQuota},
		{403, `{"error":{"message":"Forbidden"}}`, KindAuth},
		{401, `{"error":{"message":"Key limit exceeded"}}`, KindAuth},
		{408, `{"error":{"message":"Your request timed out"}}`, KindTimeout},
		{425, `{}`, KindOverloaded},
	} {
		if got := classifyHTTP(c.status, c.body).Kind; got != c.kind {
			t.Errorf("%d %s = %s, want %s", c.status, c.body, got, c.kind)
		}
	}
	// A provider's own request timeout is a failure of that attempt, so the
	// walk goes on to the next candidate.
	if !advances[classifyHTTP(408, `{}`).Kind] || !advances[classifyHTTP(425, `{}`).Kind] {
		t.Error("a 408 or a 425 ends the walk")
	}
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		header, body string
		want         int
	}{
		{"30", ``, 30},
		{now.Add(2 * time.Minute).Format(http.TimeFormat), ``, 120},
		{"", `{"error":{"resets_in_seconds":3600}}`, 3600},
		{"", `{"details":[{"retryDelay":"34s"}]}`, 34},
		{"", `Please try again in 20 seconds.`, 20},
		{"", `{}`, 0},
	} {
		if got := retryHint(c.header, c.body, now); got != c.want {
			t.Errorf("retryHint(%q, %q) = %d, want %d", c.header, c.body, got, c.want)
		}
	}
}

func TestLLM_Rest_AQuotaFailureAdvancesAndRestsTheProvider(t *testing.T) {
	e, _, sb := pair(t, statusHandler(402, `{}`), okHandler("from b"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, Fallbacks: []Fallback{{"a", "m3"}, {"b", "m2"}}})
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "b" || sb.count() != 1 {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	// The provider's other model was not tried: the quota is the account's.
	if got := outcomes(e.ledger()); got != "a:quota-exhausted,a:quota-exhausted,b:ok" {
		t.Fatalf("ledger = %s", got)
	}
}

// slowStart waits before the first stream event, as long as the caller stays.
func slowStart(d time.Duration, then func(http.ResponseWriter, *http.Request, int)) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		select {
		case <-time.After(d):
			then(w, r, n)
		case <-r.Context().Done():
		}
	}
}

func TestLLM_FirstToken_LateStartMovesOnWithoutRestingTheSlowOne(t *testing.T) {
	e, sa, sb := pair(t, slowStart(5*time.Second, okHandler("from a")), okHandler("from b"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, FirstTokenSeconds: 1, Fallbacks: []Fallback{{"b", "m2"}}})
	start := time.Now()
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "b" || resp.Text != "from b" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if d := time.Since(start); d < time.Second || d > 4*time.Second { // the slow start is 5 s
		t.Fatalf("took %v, want about the first-token deadline", d)
	}
	if got := outcomes(e.ledger()); got != "a:first-token-late,b:ok" {
		t.Fatalf("ledger = %s", got)
	}
	if got := e.reasons(); got != ",first-token" {
		t.Fatalf("reasons = %q", got)
	}
	if rests := e.r.Rests(); len(rests) != 0 {
		t.Fatalf("a slow model rested: %+v", rests)
	}
	// It is merely slow: the next call asks it first again.
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || sa.count() != 2 || sb.count() != 2 {
		t.Fatalf("err = %v a=%d b=%d", err, sa.count(), sb.count())
	}
}

func TestLLM_FirstToken_RowValidation(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "a", Kind: KindOpenAI, BaseURL: "http://127.0.0.1:1"}, fakeKey("ka"))
	s := e.load()
	for _, c := range []struct {
		name string
		row  Route
		ok   bool
	}{
		{"off", Route{Provider: "a", Model: "m1", Thinking: ThinkOff}, true},
		{"set", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, FirstTokenSeconds: 20, TimeoutSeconds: 60}, true},
		{"negative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, FirstTokenSeconds: -1}, false},
		{"too long", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, FirstTokenSeconds: 601}, false},
		{"not shorter than the timeout", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, FirstTokenSeconds: 60, TimeoutSeconds: 60}, false},
	} {
		err := s.ValidateRoute("narrative", c.row)
		if (err == nil) != c.ok || (err != nil && !strings.Contains(err.Error(), "first_token_seconds")) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}
