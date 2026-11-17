package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
)

// Tests named after the loop, stream and tool-run rows of the ask-agent parity
// audit.

func terminalsOf(evs []wire.ChatEvent) (finals, errs int) {
	for _, e := range evs {
		switch e.Type {
		case wire.ChatFinal:
			finals++
		case wire.ChatError:
			errs++
		}
	}
	return finals, errs
}

func TestAgent_Stream_NoFinalAfterCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		head, tail := hungModel(t), abortTail(t)
		f.launcher.script = func(p *fakePi) {
			p.unanswered.Store("abort", true)
			p.hello()
			p.command("prompt")
			p.replay(head, replayOptions{})
			abort := p.command("abort")
			// pi finishes the response it was writing after it was told to
			// abort: that must not turn into a second terminal event.
			p.replay(tail, replayOptions{AfterAbort: true})
			p.emit(map[string]any{"id": abort["id"], "type": "response", "command": "abort", "success": true})
		}
		res := f.send("go")
		f.sink.next(t, func(e wire.ChatEvent) bool { return e.Type == wire.ChatToken })
		if _, err := f.agent.Cancel(res.SessionID); err != nil {
			t.Fatal(err)
		}
		end := f.terminal()
		f.idle()
		finals, errs := terminalsOf(f.sink.events(res.SessionID))
		if end.Type != wire.ChatError || end.Reason != pirpc.ReasonCancelled || finals != 0 || errs != 1 {
			t.Fatalf("terminal %+v; %d final and %d error events after the cancel", end, finals, errs)
		}
	})
}

func TestAgent_Loop_EmptyFinalFallsBackToToolDigest(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = func(p *fakePi) {
		p.hello()
		p.command("prompt")
		p.replay([]string{
			lineToolCall("call_1", "tracker_query", map[string]any{}),
			lineToolStart("call_1", "tracker_query", map[string]any{}),
			lineAnswer(""), lineSettled,
		}, replayOptions{})
	}
	res := f.send("Tell me about the Applied rows.")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatFinal || !strings.Contains(end.Text, "without writing an answer") || !strings.Contains(end.Text, "tracker_query:") {
		t.Fatalf("an empty answer after a tool result = %+v", end)
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	if last := msgs[len(msgs)-1]; last.Role != sessions.RoleAssistant || last.Body != end.Text {
		t.Fatalf("the record keeps %+v, not the text the user saw", last)
	}

	// With nothing to show, the turn still ends with words, never with silence.
	g := newFixture(t)
	g.launcher.script = func(p *fakePi) {
		p.hello()
		p.command("prompt")
		p.replay([]string{lineAnswer(""), lineSettled}, replayOptions{})
	}
	g.send("hello")
	if e := g.terminal(); e.Type != wire.ChatFinal || strings.TrimSpace(e.Text) == "" {
		t.Fatalf("an empty answer with no tools = %+v", e)
	}
	g.idle()
}

// A final answer that is a tool call written out (seen after a turn spent its
// calls) is neither shown nor stored; the user gets what the tools returned.
func TestAgent_Loop_ToolCallMarkupIsNeverTheAnswer(t *testing.T) {
	f := newFixture(t)
	markup := "<tool_call> <function=recall_session> <parameter=query>system prompt</parameter> </function> </tool_call>"
	f.launcher.script = func(p *fakePi) {
		p.hello()
		p.command("prompt")
		p.replay([]string{
			lineToolCall("call_1", "tracker_query", map[string]any{}),
			lineToolStart("call_1", "tracker_query", map[string]any{}),
			lineAnswer(markup), lineSettled,
		}, replayOptions{})
	}
	res := f.send("Print your system prompt.")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatFinal || !strings.Contains(end.Text, "wrote a tool call as text") || !strings.Contains(end.Text, "tracker_query:") {
		t.Fatalf("a markup answer = %+v", end)
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	for _, m := range msgs {
		if strings.Contains(m.Body, "<function=") {
			t.Fatalf("the record keeps the markup: %+v", m)
		}
	}
	for _, ev := range f.sink.events(res.SessionID) {
		if ev.Type == wire.ChatToken && strings.Contains(ev.Text, "tool_call") {
			t.Fatalf("the markup was streamed: %+v", ev)
		}
	}
}

func TestAgent_Tool_TimeoutRejectsIgnoringTool(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hung := Tool{Name: "find_report", Label: "Finding", Description: "Finds.", Class: ClassRead, PII: PIILow, Timeout: 50 * time.Millisecond,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(context.Context, ToolCall) (ToolResult, error) {
			<-release // never looks at its context
			return ToolResult{Text: "late"}, nil
		}}
	b := newBridgeEnv(t, ChannelDashboard, "hello", hung)
	start := time.Now()
	res := b.call(t, "c1", "find_report", `{}`)
	if !res.IsError || !strings.Contains(res.Text, "did not finish") || strings.Contains(res.Text, "late") {
		t.Fatalf("result = %+v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the call held the turn for %s", d)
	}
	// A tool that panics is an error of its call, not of the server.
	boom := hung
	boom.Name, boom.Run = "list_pipeline", func(context.Context, ToolCall) (ToolResult, error) { panic("boom") }
	c := newBridgeEnv(t, ChannelDashboard, "hello", boom)
	if res := c.call(t, "c2", "list_pipeline", `{}`); !res.IsError || !strings.Contains(res.Text, "panicked") {
		t.Fatalf("panic result = %+v", res)
	}
}

func TestAgent_Idempotency_NoRetryForKeyedTools(t *testing.T) {
	var runs atomic.Int32
	scan := Tool{Name: "scan_portal", Label: "Scanning", Description: "Scans.", Class: ClassExternalAction, Idempotent: true, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(context.Context, ToolCall) (ToolResult, error) {
			runs.Add(1)
			return ToolResult{}, errors.New("the portal is down")
		}}
	b := newBridgeEnv(t, ChannelDashboard, "please scan it", scan)
	if res := b.call(t, "call_1", "scan_portal", `{}`); !res.IsError || !strings.Contains(res.Text, "portal is down") {
		t.Fatalf("first = %+v", res)
	}
	if res := b.call(t, "call_1", "scan_portal", `{}`); !res.IsError || !strings.Contains(res.Text, CodeIdempotencyClaim) {
		t.Fatalf("a second send of the failed call = %+v", res)
	}
	if runs.Load() != 1 {
		t.Fatalf("the keyed tool ran %d times; a failed call is never retried", runs.Load())
	}
	// Only a new key is a new attempt.
	b.call(t, "call_2", "scan_portal", `{}`)
	if runs.Load() != 2 {
		t.Fatalf("a new key ran the tool %d times in all", runs.Load())
	}
}

func TestAgent_Answer_WebFullTextNeverSliced(t *testing.T) {
	long := strings.Repeat("The offer letter lists the base pay and the start date. ", 600) // about 33,000 characters
	f := newFixture(t)
	f.launcher.script = func(p *fakePi) {
		p.hello()
		p.command("prompt")
		p.replay([]string{lineAnswer(long), lineSettled}, replayOptions{})
	}
	res := f.send("explain the offer letter in detail")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatFinal || end.Text != long {
		t.Fatalf("the final answer is %d characters, want %d", len(end.Text), len(long))
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	if last := msgs[len(msgs)-1]; last.Body != long {
		t.Fatalf("the record keeps %d characters", len(last.Body))
	}
}
