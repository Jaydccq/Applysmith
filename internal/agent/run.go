package agent

import (
	"context"
	"sync"
	"sync/atomic"

	harnessv "applysmith/internal/agent/harness"
	"applysmith/internal/api/wire"
)

// MaxToolCalls is the most tool calls a turn may make, and
// MaxStepwiseToolCalls the most once it has called a stepwise tool (AC-5): a
// desktop task or a stopped run's questions take one control or one answer
// per call. MaxRunToolCalls is the most a turn bound to one run may make
// (Turn.FinishRun): it works an application page by page. Each stays inside
// the turn's time limit.
const (
	MaxToolCalls         = 12
	MaxStepwiseToolCalls = 40
	MaxRunToolCalls      = 80
)

// run is the state one turn shares between the supervisor loop that reads pi's
// events and the bridge that answers the extension's calls.
type run struct {
	turn *Turn
	emit func(wire.ChatEvent)

	// callCtx ends when the turn is cancelled or over; every forwarded call
	// runs under it.
	callCtx     context.Context
	cancelCalls context.CancelFunc

	calls    atomic.Int32 // tool calls counted so far, forwarded and unknown
	stepwise atomic.Bool  // the turn called a stepwise tool, so it has the longer limit
	inflight atomic.Int32 // forwarded calls being answered

	helloOnce sync.Once
	helloCh   chan struct{}
	badOnce   sync.Once
	helloBadC chan struct{} // closed when pi reported a tool set other than the catalog
	helloBad  atomic.Value  // string: why the tool set pi reported was refused

	packedChars atomic.Int64 // size of the earlier turns handed to pi, for usage estimates
	capStop     atomic.Bool  // the gateway refused a call of the turn for the daily cap

	// harnessOnce pins the harness version of the process, so the catalog's
	// tool notes and the prompt's guidance of one turn come from one version.
	harnessOnce sync.Once
	harness     harnessv.Harness

	mu   sync.Mutex
	full map[string]kept // call id -> the result as the record keeps it
	runs []string        // background runs the turn started
	// answered holds the first answer to each distinct call (pirpc.CallKey),
	// so a repeat is answered again without running or counting; repeats
	// counts the repeats answered so.
	answered map[string]answer
	repeats  int
}

// answer is a call's result as the model got it and, when the bridge kept
// one (recorded), as the record keeps it.
type answer struct {
	res      wire.AgentToolResult
	kept     kept
	recorded bool
}

// callLimit is the most tool calls the turn may make now.
func (r *run) callLimit() int32 {
	switch {
	case r.turn.FinishRun != "":
		return MaxRunToolCalls
	case r.stepwise.Load():
		return MaxStepwiseToolCalls
	}
	return MaxToolCalls
}

// remember keeps the first answer to a call.
func (r *run) remember(key string, a answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.answered[key]; !ok {
		r.answered[key] = a
	}
}

// forgetAnswers drops the kept answers, after a call that may change them.
func (r *run) forgetAnswers() {
	r.mu.Lock()
	clear(r.answered)
	r.mu.Unlock()
}

// repeat returns the first answer to a call made again, with how many repeats
// the turn has made, this one included.
func (r *run) repeat(key string) (answer, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.answered[key]
	if ok {
		r.repeats++
	}
	return a, r.repeats, ok
}

func newRun(parent context.Context, t *Turn, emit func(wire.ChatEvent)) *run {
	ctx, cancel := context.WithCancel(parent)
	return &run{turn: t, emit: emit, callCtx: ctx, cancelCalls: cancel, helloCh: make(chan struct{}), helloBadC: make(chan struct{}), full: map[string]kept{}, answered: map[string]answer{}}
}

// kept is a result as the record stores it; outside says it came from a tool
// that returns text written by third parties; runID names the run the call
// started.
type kept struct {
	text    string
	outside bool
	runID   string
}

func (r *run) keepResult(callID string, k kept) {
	r.mu.Lock()
	r.full[callID] = k
	r.mu.Unlock()
}

// peekResult returns the recorded result of a call and leaves it.
func (r *run) peekResult(callID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.full[callID]
	return k.text, ok
}

// peekKept returns the recorded result of a call with its origin and leaves it.
func (r *run) peekKept(callID string) (kept, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.full[callID]
	return k, ok
}

// takeResult returns and forgets the recorded result of a call.
func (r *run) takeResult(callID string) (kept, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.full[callID]
	delete(r.full, callID)
	return k, ok
}

func (r *run) noteRun(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	r.runs = append(r.runs, id)
	r.mu.Unlock()
}

// startedRuns lists the runs the turn started, in order.
func (r *run) startedRuns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.runs...)
}
