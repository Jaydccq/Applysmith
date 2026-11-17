package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type gateRig struct {
	active  atomic.Int32
	started atomic.Int32
	held    atomic.Int32
	touched atomic.Int32
	freed   atomic.Int32
	holdErr error
	gate    *RunGate
}

type line struct{ r *gateRig }

func (l line) Touch(context.Context)   { l.r.touched.Add(1) }
func (l line) Release(context.Context) { l.r.freed.Add(1) }

// newGateRig builds a gate whose queued starts end with the test.
func newGateRig(t *testing.T, life context.Context) *gateRig {
	life, cancel := context.WithCancel(life)
	t.Cleanup(cancel)
	r := &gateRig{}
	r.gate = &RunGate{
		Life:   life,
		Active: func(context.Context) (int, error) { return int(r.active.Load()), nil },
		Hold: func(_ context.Context, _, label string) (QueueNote, error) {
			if r.holdErr != nil {
				return nil, r.holdErr
			}
			r.held.Add(1)
			return line{r}, nil
		},
		Poll: 5 * time.Millisecond, Wait: 2 * time.Second,
	}
	return r
}

func (r *gateRig) tools() []Tool {
	run := func(context.Context, ToolCall) (ToolResult, error) {
		r.started.Add(1)
		return ToolResult{Text: "started", RunID: "run_x"}, nil
	}
	var out []Tool
	for _, name := range []string{"evaluate_job", "scan_portal", "autofill_application", "apply_from_list", "tracker_query", "tailor_resume"} {
		out = append(out, Tool{Name: name, Label: name, Run: run})
	}
	return r.gate.wrap(out)
}

func (r *gateRig) call(t *testing.T, name string) ToolResult {
	t.Helper()
	return r.callWith(t, name, ToolCall{})
}

func (r *gateRig) callWith(t *testing.T, name string, c ToolCall) ToolResult {
	t.Helper()
	for _, tl := range r.tools() {
		if tl.Name == name {
			res, err := tl.Run(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			return res
		}
	}
	t.Fatalf("no tool %s", name)
	return ToolResult{}
}

func eventuallyGate(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A run counts as going only once its start has recorded it, so the count and
// the start are one step: two starts that find the last slot together, and two
// queued starts that see it free on the same tick, take it one at a time.
func TestRunGate_StartsThatFindTheLastSlotTogetherTakeItOneAtATime(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newGateRig(t, life)
	r.active.Store(MaxAgentRuns - 1)
	slow := func(context.Context, ToolCall) (ToolResult, error) {
		time.Sleep(20 * time.Millisecond) // the start records its run at its end
		r.active.Add(1)
		r.started.Add(1)
		return ToolResult{Text: "started", RunID: "run_x"}, nil
	}
	tools := r.gate.wrap([]Tool{{Name: "evaluate_job", Label: "evaluate_job", Run: slow}, {Name: "scan_portal", Label: "scan_portal", Run: slow}})
	var wg sync.WaitGroup
	for _, tl := range tools {
		wg.Go(func() {
			if _, err := tl.Run(context.Background(), ToolCall{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if r.started.Load() != 1 || r.held.Load() != 1 || r.active.Load() != MaxAgentRuns {
		t.Fatalf("started %d, queued %d, going %d; want one start, one queued, %d going", r.started.Load(), r.held.Load(), r.active.Load(), MaxAgentRuns)
	}

	// Two queued starts and one slot.
	q := newGateRig(t, life)
	q.active.Store(MaxAgentRuns)
	slowQ := func(context.Context, ToolCall) (ToolResult, error) {
		time.Sleep(20 * time.Millisecond)
		q.active.Add(1)
		q.started.Add(1)
		return ToolResult{Text: "started", RunID: "run_x"}, nil
	}
	for _, tl := range q.gate.wrap([]Tool{{Name: "evaluate_job", Label: "evaluate_job", Run: slowQ}, {Name: "scan_portal", Label: "scan_portal", Run: slowQ}}) {
		if res, err := tl.Run(context.Background(), ToolCall{}); err != nil || !strings.HasPrefix(res.Text, "Queued:") {
			t.Fatalf("%s = %+v, %v", tl.Name, res, err)
		}
	}
	q.active.Store(MaxAgentRuns - 1)
	eventuallyGate(t, "a queued start to run", func() bool { return q.started.Load() >= 1 })
	time.Sleep(60 * time.Millisecond)
	if q.started.Load() != 1 || q.active.Load() != MaxAgentRuns {
		t.Fatalf("queued starts run = %d, going %d; want one, and %d going", q.started.Load(), q.active.Load(), MaxAgentRuns)
	}
}

func TestRunGate_AThirdStartIsQueuedAndTheModelIsToldSo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newGateRig(t, context.Background())
		r.active.Store(2)
		res := r.call(t, "evaluate_job")
		if res.RunID != "" || !strings.HasPrefix(res.Text, "Queued:") || !strings.Contains(res.Text, "begins by itself") {
			t.Fatalf("result = %+v", res)
		}
		if r.held.Load() != 1 {
			t.Fatalf("the queued start shows %d lines on the board", r.held.Load())
		}
		time.Sleep(40 * time.Millisecond)
		if r.started.Load() != 0 {
			t.Fatal("the start ran while two runs were going")
		}
		r.active.Store(1)
		eventuallyGate(t, "the queued start to run", func() bool { return r.started.Load() == 1 })
		eventuallyGate(t, "the board line to go", func() bool { return r.freed.Load() == 1 })
		if r.touched.Load() == 0 {
			t.Error("the board line was never touched while it waited, so the stale sweep would finish it")
		}
		time.Sleep(30 * time.Millisecond)
		if r.started.Load() != 1 {
			t.Errorf("the queued start ran %d times", r.started.Load())
		}
	})
}

func TestRunGate_EveryToolThatStartsARunIsGatedAndNoOtherIs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, name := range []string{"evaluate_job", "scan_portal", "autofill_application", "apply_from_list"} {
			r := newGateRig(t, context.Background())
			r.active.Store(2)
			if res := r.call(t, name); !strings.HasPrefix(res.Text, "Queued:") {
				t.Errorf("%s was not queued: %+v", name, res)
			}
		}
		for _, name := range []string{"tracker_query", "tailor_resume"} {
			r := newGateRig(t, context.Background())
			r.active.Store(2)
			if res := r.call(t, name); res.Text != "started" {
				t.Errorf("%s was held back: %+v", name, res)
			}
		}
	})
}

func TestRunGate_QueuedStartsAllRunOnceSlotsFree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newGateRig(t, context.Background())
		r.active.Store(2)
		for _, source := range []string{"linkedin", "indeed", "jobright"} {
			r.callWith(t, "scan_portal", ToolCall{Args: json.RawMessage(`{"source":"` + source + `"}`)})
		}
		if r.held.Load() != 3 {
			t.Fatalf("held %d", r.held.Load())
		}
		r.active.Store(0)
		eventuallyGate(t, "all three to run", func() bool { return r.started.Load() == 3 })
	})
}

func TestRunGate_TheSameStartIsQueuedOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newGateRig(t, context.Background())
		r.active.Store(2)
		call := ToolCall{Args: json.RawMessage(`{"source":"jobright"}`)}
		if res := r.callWith(t, "scan_portal", call); !strings.Contains(res.Text, "This start (scan_portal)") {
			t.Fatalf("first = %+v", res)
		}
		if res := r.callWith(t, "scan_portal", call); !strings.Contains(res.Text, "already queued") || r.held.Load() != 1 {
			t.Fatalf("second = %+v, held %d", res, r.held.Load())
		}
		r.active.Store(0)
		eventuallyGate(t, "the start to run", func() bool { return r.started.Load() == 1 })
		eventuallyGate(t, "the board line to go", func() bool { return r.freed.Load() == 1 })
		r.active.Store(2)
		if res := r.callWith(t, "scan_portal", call); !strings.HasPrefix(res.Text, "Queued:") {
			t.Fatalf("after the first began, the start queues again: %+v", res)
		}
	})
}

// A server that stops ends the wait without running the start, and keeps the
// stored start; the next start tells its conversation it did not begin and
// takes it away, never running it.
func TestRunGate_AServerThatStopsEndsTheWaitAndTheNextStartTellsTheConversation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		life, stop := context.WithCancel(context.Background())
		r := newGateRig(t, life)
		var mu sync.Mutex
		var told []string
		tell := func(_ context.Context, session, body string) {
			mu.Lock()
			told = append(told, session+": "+body)
			mu.Unlock()
		}
		r.gate.Tell = tell
		var held []string
		r.gate.Hold = func(_ context.Context, session, label string) (QueueNote, error) {
			held = append(held, session+" "+label)
			r.held.Add(1)
			return line{r}, nil
		}
		r.active.Store(2)
		tl := r.gate.wrap([]Tool{{Name: "scan_portal", Label: "Starting a scan", Run: func(context.Context, ToolCall) (ToolResult, error) {
			r.started.Add(1)
			return ToolResult{Text: "started", RunID: "run_x"}, nil
		}}})[0]
		if _, err := tl.Run(context.Background(), ToolCall{Turn: &Turn{SessionID: "ses_s"}}); err != nil {
			t.Fatal(err)
		}
		if len(held) != 1 || held[0] != "ses_s Starting a scan" {
			t.Fatalf("held %q: the stored start names its conversation", held)
		}
		stop()
		r.active.Store(0)
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		n := len(told)
		mu.Unlock()
		if r.started.Load() != 0 || r.freed.Load() != 0 || n != 0 {
			t.Fatalf("after the stop: started %d, freed %d, told %d; the start stays stored for the next start", r.started.Load(), r.freed.Load(), n)
		}

		next := newGateRig(t, context.Background())
		next.gate.Tell = tell
		next.gate.Left = func(context.Context) ([]LeftStart, error) {
			return []LeftStart{{SessionID: "ses_s", Label: "Starting a scan", Note: line{next}}}, nil
		}
		if err := next.gate.Settle(context.Background()); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		want := "ses_s: The queued start (a scan) did not begin: the server stopped before one of my runs finished; ask again to start it."
		if len(told) != 1 || told[0] != want {
			t.Fatalf("told %q, want %q", told, want)
		}
		if next.freed.Load() != 1 || next.started.Load() != 0 || r.started.Load() != 0 {
			t.Fatalf("freed %d, started %d/%d: the left start is taken away and never run", next.freed.Load(), next.started.Load(), r.started.Load())
		}
	})
}
