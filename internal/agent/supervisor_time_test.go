package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/api/wire"
)

// These tests run on the synctest clock: every wait is a condition, and the
// elapsed time they assert is the clock's, so a loaded machine cannot fail them.

func hungModel(t *testing.T) []string {
	lines := transcript(t, "turn-abort.jsonl")
	// Up to and including the first streamed text; the rest is what pi writes
	// after the abort.
	for i, l := range lines {
		if strings.Contains(l, `"text_delta"`) {
			return lines[:i+1]
		}
	}
	t.Fatal("no text delta in the abort transcript")
	return nil
}

func abortTail(t *testing.T) []string {
	lines := transcript(t, "turn-abort.jsonl")
	for i, l := range lines {
		if strings.Contains(l, `"text_delta"`) {
			return lines[i+1:]
		}
	}
	return nil
}

func TestSupervisor_Cancel_AnAbortNobodyAnswersKillsPiAtOneAndAHalfSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		head := hungModel(t)
		f.launcher.script = func(p *fakePi) {
			p.unanswered.Store("abort", true)
			p.hello()
			p.command("prompt")
			p.replay(head, replayOptions{})
		}
		res := f.send("go")
		f.sink.next(t, func(e wire.ChatEvent) bool { return e.Type == wire.ChatToken })
		t0 := time.Now()
		if _, err := f.agent.Cancel(res.SessionID); err != nil {
			t.Fatal(err)
		}
		end := f.terminal()
		if time.Since(t0) != 0 || end.Reason != pirpc.ReasonCancelled {
			t.Fatalf("terminal %+v after %s", end, time.Since(t0))
		}
		<-f.launcher.started()[0].killed
		if d := time.Since(t0); d != 1500*time.Millisecond {
			t.Fatalf("pi was killed after %s, want 1.5s", d)
		}
		f.idle()
		if AbortWait >= 2*time.Second {
			t.Fatal("the abort wait leaves no room inside the cancel bound")
		}
	})
}

func TestSupervisor_Cancel_StopsARunningReadToolAndNamesTheRunsItStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stopped := make(chan time.Time, 1)
		slow := Tool{Name: "tracker_query", Label: "Counting", Description: "Counts.", Class: ClassRead, PII: PIILow, Timeout: 20 * time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
			Run: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
				<-ctx.Done()
				stopped <- time.Now()
				return ToolResult{}, ctx.Err()
			}}
		scan := Tool{Name: "scan_portal", Label: "Scanning", Description: "Scans.", Class: ClassExternalAction, Idempotent: true, PII: PIINone, Timeout: 5 * time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
			Run: func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Text: "Started run run_42.", RunID: "run_42"}, nil
			}}
		f := newFixture(t, slow, scan)
		f.launcher.script = func(p *fakePi) {
			p.unanswered.Store("abort", true)
			p.hello()
			p.command("prompt")
			p.line(lineToolCall("call_1", "scan_portal", map[string]any{}))
			p.line(lineToolStart("call_1", "scan_portal", map[string]any{}))
			p.finishCall(mustRecord(t, lineToolStart("call_1", "scan_portal", map[string]any{})))
			p.line(lineToolCall("call_2", "tracker_query", map[string]any{}))
			p.line(lineToolStart("call_2", "tracker_query", map[string]any{}))
			go p.finishCall(mustRecord(t, lineToolStart("call_2", "tracker_query", map[string]any{})))
			abort := p.command("abort")
			p.emit(map[string]any{"id": abort["id"], "type": "response", "command": "abort", "success": true})
		}
		res := f.send("please scan the example portal and count my rows")
		f.sink.next(t, func(e wire.ChatEvent) bool { return e.Type == wire.ChatToolCall && e.ToolCallID == "call_2" })
		synctest.Wait()

		t0 := time.Now()
		got, err := f.agent.Cancel(res.SessionID)
		if err != nil || len(got.Runs) != 1 || got.Runs[0] != "run_42" {
			t.Fatalf("cancel = %+v, %v; the response must name the run the turn started", got, err)
		}
		at := <-stopped
		if d := at.Sub(t0); d >= 2*time.Second {
			t.Fatalf("the running tool stopped %s after the cancel", d)
		}
		f.terminal()
		f.idle()
	})
}

func mustRecord(t *testing.T, line string) pirpc.Record {
	t.Helper()
	rec, err := pirpc.Decode([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestSupervisor_Stall_SilenceForTwoMinutesEndsTheTurnAndKillsPi(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		f.launcher.script = func(p *fakePi) {
			p.hello()
			p.command("prompt")
			p.line(`{"type":"agent_start"}`)
		}
		t0 := time.Now()
		f.send("go")
		end := f.terminal()
		if end.Type != wire.ChatError || end.Reason != pirpc.ReasonUnavailable || end.Detail != "stalled" {
			t.Fatalf("terminal = %+v", end)
		}
		<-f.launcher.started()[0].killed
		if d := time.Since(t0); d != StallLimit {
			t.Fatalf("the stall was declared after %s, want %s", d, StallLimit)
		}
		f.idle()
	})
}

func TestSupervisor_Stall_ACallInFlightIsNotSilence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		stubborn := Tool{Name: "tracker_query", Label: "Counting", Description: "Counts.", Class: ClassRead, PII: PIILow, Timeout: 20 * time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
			Run: func(context.Context, ToolCall) (ToolResult, error) {
				// Ignores its context, as a tool stuck on a slow dependency can.
				time.Sleep(300 * time.Second)
				close(release)
				return ToolResult{Text: "done"}, nil
			}}
		f := newFixture(t, stubborn)
		f.launcher.script = func(p *fakePi) {
			p.hello()
			p.command("prompt")
			args := map[string]any{}
			p.line(lineToolCall("call_1", "tracker_query", args))
			p.line(lineToolStart("call_1", "tracker_query", args))
			p.finishCall(mustRecord(t, lineToolStart("call_1", "tracker_query", args)))
			p.line(lineAnswer("Done."))
			p.line(lineSettled)
		}
		t0 := time.Now()
		f.send("count my rows")
		end := f.terminal()
		<-release
		if end.Type != wire.ChatFinal {
			t.Fatalf("terminal = %+v: a call in flight for 300 seconds must not count as silence", end)
		}
		if d := time.Since(t0); d < 300*time.Second {
			t.Fatalf("the turn ended after %s, before the call returned", d)
		}
		f.idle()
	})
}

func TestSupervisor_TimeLimit_TenMinutesEndsAnEndlesslyActiveTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		f.launcher.script = endlesslyActive
		t0 := time.Now()
		f.send("go")
		end := f.terminal()
		if end.Type != wire.ChatError || end.Reason != pirpc.ReasonTimeLimit {
			t.Fatalf("terminal = %+v", end)
		}
		if d := time.Since(t0); d != TurnLimit {
			t.Fatalf("limit reached after %s, want %s", d, TurnLimit)
		}
		f.idle()
	})
}

// endlesslyActive is a pi that stays busy forever and answers the abort late.
func endlesslyActive(p *fakePi) {
	p.unanswered.Store("abort", true)
	p.hello()
	p.command("prompt")
	for {
		select {
		case <-p.done:
			return
		case <-time.After(time.Minute):
			p.line(`{"type":"queue_update","steering":[],"followUp":[]}`)
		}
		if hasCommandType(p, "abort") {
			p.emit(map[string]any{"id": "late", "type": "response", "command": "abort", "success": true})
			return
		}
	}
}

func TestSupervisor_Restart_TenFailedStartsInARowKeepTheLoopOffUntilReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		f.launcher.startErr = context.DeadlineExceeded
		for i := 0; i < failuresUntilOff; i++ {
			time.Sleep(time.Minute)
			f.send("go")
			if end := f.terminal(); end.Reason != pirpc.ReasonUnavailable || !strings.Contains(end.Detail, "did not start") {
				t.Fatalf("attempt %d = %+v", i, end)
			}
			f.idle()
		}
		time.Sleep(time.Hour)
		f.launcher.startErr = nil
		f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
		f.send("go")
		end := f.terminal()
		f.idle()
		if end.Type != wire.ChatError || !strings.Contains(end.Detail, "applysmith doctor") {
			t.Fatalf("after ten failures = %+v", end)
		}
		f.sup.Reset()
		f.send("go")
		if end := f.terminal(); end.Type != wire.ChatFinal {
			t.Fatalf("after a reset = %+v", end)
		}
		f.idle()
	})
}

// Background turns (a schedule's, a digest's, a task's own) never hold the last
// process slot, so a person's message does not wait behind work nobody is
// watching; the next background turn waits for a background turn to end.
func TestSupervisor_Processes_BackgroundTurnsLeaveASlotForAPerson(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		var mu sync.Mutex
		var releases []chan struct{}
		lines := transcript(t, "turn-tool-then-answer.jsonl")
		f.launcher.script = func(p *fakePi) {
			rel := make(chan struct{})
			mu.Lock()
			releases = append(releases, rel)
			mu.Unlock()
			p.hello()
			p.command("prompt")
			<-rel
			p.replay(lines, replayOptions{})
		}
		release := func(i int) {
			mu.Lock()
			defer mu.Unlock()
			close(releases[i])
		}
		for range MaxProcesses {
			go func() {
				_, _ = f.agent.RunScheduled(context.Background(), ScheduledRequest{Title: "Morning check", Prompt: "what changed overnight"})
			}()
		}
		synctest.Wait()
		if n := len(f.launcher.started()); n != MaxProcesses-1 {
			t.Fatalf("%d background processes started, want %d: one slot stays for a person", n, MaxProcesses-1)
		}
		f.send("how many applications are waiting")
		synctest.Wait()
		if n := len(f.launcher.started()); n != MaxProcesses {
			t.Fatalf("%d processes after a person's message, want %d: the person's turn starts at once", n, MaxProcesses)
		}
		release(0)
		synctest.Wait()
		if n := len(f.launcher.started()); n != MaxProcesses+1 {
			t.Fatalf("%d processes after a background turn ended, want the waiting one to start", n)
		}
		mu.Lock()
		for _, r := range releases[1:] {
			close(r)
		}
		mu.Unlock()
		synctest.Wait()
		f.agent.Close()
	})
}

func TestSupervisor_Processes_AtMostThreeRunAtOnceAndAFourthTurnWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		var mu sync.Mutex
		var releases []chan struct{}
		lines := transcript(t, "turn-tool-then-answer.jsonl")
		f.launcher.script = func(p *fakePi) {
			rel := make(chan struct{})
			mu.Lock()
			releases = append(releases, rel)
			mu.Unlock()
			p.hello()
			p.command("prompt")
			<-rel
			p.replay(lines, replayOptions{})
		}
		var results []SendResult
		for range 4 {
			results = append(results, f.send("go"))
		}
		synctest.Wait()
		if n := len(f.launcher.started()); n != MaxProcesses {
			t.Fatalf("%d processes started, want %d", n, MaxProcesses)
		}
		mu.Lock()
		first := releases[0]
		mu.Unlock()
		close(first)
		f.terminal()
		synctest.Wait()
		if n := len(f.launcher.started()); n != 4 {
			t.Fatalf("%d processes after one turn ended, want the fourth to start", n)
		}
		mu.Lock()
		for _, r := range releases[1:] {
			close(r)
		}
		mu.Unlock()
		synctest.Wait()
		f.agent.Close()
		_ = results
	})
}
