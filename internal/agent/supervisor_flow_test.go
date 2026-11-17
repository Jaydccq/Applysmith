package agent

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
)

func hasCommand(p *fakePi, typ string) bool {
	for _, c := range p.commandTypes() {
		if c == typ {
			return true
		}
	}
	return false
}

func TestSupervisor_Steer_ReachesTheModelAfterTheStepsToolCallsFinish(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = standard(t, "turn-steer.jsonl", replayOptions{Before: func(p *fakePi, _ int, rec pirpc.Record) bool {
		if rec.Type == pirpc.TypeQueueUpdate && len(rec.Steering) > 0 {
			p.command("steer")
		}
		return true
	}})
	res := f.send("go")
	f.sink.next(t, func(e wire.ChatEvent) bool { return e.Type == wire.ChatToolCall })
	turnID, queued, err := f.agent.Steer(res.SessionID, "Please be brief.")
	if err != nil || turnID != res.TurnID || queued != 1 {
		t.Fatalf("steer = %q %d %v", turnID, queued, err)
	}
	f.terminal()
	f.idle()

	got := types(f.sink.events(res.SessionID))
	if i, j := strings.Index(got, "tool.result"), strings.Index(got, "steer"); i < 0 || j < i {
		t.Fatalf("the steering text must follow the step's tool result: %s", got)
	}
	var steer wire.ChatEvent
	for _, e := range f.sink.events(res.SessionID) {
		if e.Type == wire.ChatSteerEvent {
			steer = e
		}
	}
	if steer.Text != "Please be brief." {
		t.Fatalf("steer event = %+v", steer)
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	found := false
	for _, m := range msgs {
		found = found || (m.Role == sessions.RoleSteer && m.Body == "Please be brief.")
	}
	if !found {
		t.Fatal("the steering message is not in the record")
	}
}

func TestSteer_AtMostFiveQueuedAndAnUnknownTurnConflicts(t *testing.T) {
	a := newActiveTurn("turn_1", "ses_1")
	for i := 1; i <= MaxQueuedSteers; i++ {
		if n, err := a.TrySteer("x"); err != nil || n != i {
			t.Fatalf("steer %d = %d, %v", i, n, err)
		}
	}
	if _, err := a.TrySteer("x"); err != ErrSteerFull {
		t.Fatalf("sixth steer = %v", err)
	}
	<-a.steerCh
	a.delivered()
	if _, err := a.TrySteer("x"); err != nil {
		t.Fatalf("after one was delivered: %v", err)
	}

	f := newFixture(t)
	if _, _, err := f.agent.Steer("ses_none", "hello"); err != ErrNoTurn {
		t.Fatalf("steering without a turn = %v", err)
	}
	if _, err := f.agent.Cancel("ses_none"); err != ErrNoTurn {
		t.Fatalf("cancelling without a turn = %v", err)
	}
}

func TestSupervisor_CostCap_AGatewayRefusalEndsTheTurnAsCostCap(t *testing.T) {
	f := newFixture(t)
	f.router.gwErr = &llm.Error{Kind: llm.KindCostCap, Task: llm.AgentTask, Message: "the daily model spend cap is reached"}
	lines := transcript(t, "turn-provider-error.jsonl")
	f.launcher.script = func(p *fakePi) {
		p.hello()
		p.command("prompt")
		// pi's model call reaches the gateway, which refuses it; pi then reports
		// the failure as a provider error.
		if err := p.bridge.ModelCall(context.Background(), p.token, llm.AgentCall{Wire: llm.WireOpenAI, Body: []byte(`{}`)}, httptest.NewRecorder()); llm.KindOf(err) != llm.KindCostCap {
			t.Errorf("gateway answer = %v", err)
		}
		p.replay(lines, replayOptions{})
	}
	res := f.send("hello")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatError || end.Reason != pirpc.ReasonCostCap {
		t.Fatalf("terminal = %+v", end)
	}
	if calls := f.router.calls; len(calls) != 1 || calls[0].SessionID != res.SessionID || calls[0].TurnID != res.TurnID {
		t.Fatalf("the call was not served under the turn's conversation: %+v", calls)
	}
}
func TestSupervisor_UnknownToolIsAuditedFromTheStartEventAndCountsTowardTheLimit(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = func(p *fakePi) {
		p.hello()
		p.command("prompt")
		p.replay([]string{lineToolCall("call_1", "shell", map[string]any{"cmd": "ls", "step": "Looking"}), lineToolStart("call_1", "shell", map[string]any{"cmd": "ls", "step": "Looking"}),
			lineAnswer("I cannot do that."), lineSettled}, replayOptions{})
	}
	res := f.send("list files")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatFinal {
		t.Fatalf("terminal = %+v", end)
	}
	audit, _ := f.sessions.Decisions(context.Background(), res.TurnID)
	if len(audit) != 1 || audit[0].Tool != "shell" || audit[0].Code != CodeUnknownTool || audit[0].Verdict != gates.Deny {
		t.Fatalf("audit = %+v", audit)
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	last := msgs[len(msgs)-2]
	if last.Role != sessions.RoleToolResult || last.OK || !strings.Contains(last.Body, "not found") {
		t.Fatalf("the result pi gave for the unknown tool is recorded: %+v", last)
	}
}

func TestSupervisor_HelloWithABuiltInToolMarksTheProcessUnusable(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = func(p *fakePi) {
		p.hello("bash")
		p.command("prompt")
	}
	f.send("hello")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatError || end.Reason != pirpc.ReasonUnavailable || !strings.Contains(end.Detail, "not the catalog") {
		t.Fatalf("terminal = %+v", end)
	}
	if hasCommand(f.launcher.started()[0], "prompt") {
		t.Fatal("a prompt was sent to a process with a built-in tool active")
	}
}

func TestSupervisor_PiDiesMidTurn_EndsUnavailableWithoutReplayAndKeepsTheClaim(t *testing.T) {
	started := make(chan struct{}, 1)
	ran := 0
	var mu sync.Mutex
	scan := Tool{Name: "scan_portal", Label: "Scanning", Description: "Scans.", Class: ClassExternalAction, Idempotent: true, PII: PIINone, Timeout: 5 * time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{"source":{"type":"string"}}}`,
		Run: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			mu.Lock()
			ran++
			mu.Unlock()
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ToolResult{}, ctx.Err()
		}}
	f := newFixture(t, scan)
	f.launcher.script = func(p *fakePi) {
		p.hello()
		p.command("prompt")
		args := map[string]any{"source": "example"}
		p.line(lineToolCall("call_7", "scan_portal", args))
		p.line(lineToolStart("call_7", "scan_portal", args))
		go p.forward("call_7", "scan_portal", mustJSON(t, args))
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Error("the forwarded call never started")
		}
		p.exit() // killed with the call in flight
	}
	res := f.send("please scan the example portal")
	end := f.terminal()
	f.idle()

	if end.Type != wire.ChatError || end.Reason != pirpc.ReasonUnavailable {
		t.Fatalf("terminal = %+v", end)
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	var result sessions.Message
	for _, m := range msgs {
		if m.Role == sessions.RoleToolResult {
			result = m
		}
	}
	if !result.SystemMade || result.ToolCallID != "call_7" || !strings.Contains(result.Body, "may or may not have run") || strings.Contains(strings.ToLower(result.Body), "retry") {
		t.Fatalf("stand-in = %+v", result)
	}
	if fresh, _ := f.sessions.Claim(context.Background(), "scan_portal", "call_7"); fresh {
		t.Fatal("the idempotency claim did not survive the process")
	}
	mu.Lock()
	defer mu.Unlock()
	if ran != 1 || len(f.launcher.started()) != 1 {
		t.Fatalf("the call ran %d times in %d processes: nothing may be replayed", ran, len(f.launcher.started()))
	}
}

func TestSupervisor_StandInIsInThePackedContextOfTheNextTurnAndNeverSaysRetry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sess, _ := f.sessions.Create(ctx, sessions.Dashboard, "")
	f.sessions.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: "turn_old", Role: sessions.RoleUser, Body: "scan the example portal", OK: true})
	f.sessions.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: "turn_old", Role: sessions.RoleToolCall, ToolName: "scan_portal", ToolCallID: "call_1", Body: `{"source":"example"}`, OK: true})
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.sendTo(sess.ID, "what happened?")
	f.terminal()
	f.idle()

	got := f.contexts.all()
	if len(got) != 1 {
		t.Fatalf("%d contexts", len(got))
	}
	var prefix strings.Builder
	for _, m := range got[0].Prefix {
		prefix.WriteString(m.Role + ": " + m.Text + "\n")
	}
	for _, want := range []string{"user: scan the example portal", "[tool scan_portal", "may or may not have run", "runs board"} {
		if !strings.Contains(prefix.String(), want) {
			t.Errorf("prefix lacks %q:\n%s", want, prefix.String())
		}
	}
	if strings.Contains(strings.ToLower(prefix.String()), "try again") || strings.Contains(strings.ToLower(prefix.String()), "retry") {
		t.Errorf("the stand-in suggests trying again:\n%s", prefix.String())
	}
	if strings.Contains(prefix.String(), "what happened?") {
		t.Error("the new message belongs to the prompt, not the prefix")
	}
}

func TestSupervisor_ForkStartsAFreshProcessWithTheCopiedHistoryAndNoClaims(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	first := f.send("Tell me about the Applied rows.")
	f.terminal()
	f.idle()
	msgs, _ := f.sessions.Messages(context.Background(), first.SessionID)
	fork, err := f.sessions.Fork(context.Background(), first.SessionID, msgs[len(msgs)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh, _ := f.sessions.Claim(context.Background(), "autofill_application", "call_x"); !fresh {
		t.Fatal("setup")
	}
	f.sendTo(fork.ID, "and rejected?")
	f.terminal()
	f.idle()

	if n := len(f.launcher.started()); n != 2 {
		t.Fatalf("%d processes; a fork needs its own", n)
	}
	for i, p := range f.launcher.started() {
		if cmds := p.commandTypes(); !slices.Contains(cmds, "new_session") || slices.Index(cmds, "new_session") > slices.Index(cmds, "prompt") {
			t.Errorf("process %d: commands = %v; every turn begins a new pi session", i, cmds)
		}
	}
	ctxs := f.contexts.all()
	if len(ctxs) != 2 || len(ctxs[0].Prefix) != 0 || len(ctxs[1].Prefix) == 0 {
		t.Fatalf("contexts = %+v", ctxs)
	}
	if first := ctxs[1].Prefix[0]; first.Role != "user" || !strings.Contains(first.Text, "Applied") {
		t.Fatalf("the fork's prefix = %+v", ctxs[1].Prefix)
	}
	if fresh, _ := f.sessions.Claim(context.Background(), "autofill_application", "call_x"); fresh {
		t.Fatal("setup claim vanished")
	}
}

// lightRoute is an agent-light row beside the fixture's agent-kernel one.
func lightRoute() *llm.AgentRoute {
	return &llm.AgentRoute{
		Resolved: llm.Resolved{Task: llm.Task{ID: llm.AgentLightTask}, Provider: "cheap", Model: "light-1", Thinking: llm.ThinkLow, MaxOutputTokens: 2000},
		Config:   llm.ProviderConfig{ID: "cheap", Kind: llm.KindOpenAI, BaseURL: "http://127.0.0.1:1/v1"},
	}
}

// modelCallThenAnswer makes one model request through the bridge, as pi does
// once it has the prompt, then plays an ordinary turn.
func modelCallThenAnswer(t *testing.T) func(p *fakePi) {
	play := standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	return func(p *fakePi) {
		_ = p.bridge.ModelCall(context.Background(), p.token, llm.AgentCall{Wire: llm.WireOpenAI, Body: []byte(`{}`)}, httptest.NewRecorder())
		play(p)
	}
}

// With agent-light routed, the classifier's answer picks the row the whole
// turn runs on: pi's provider and model, its thinking level, the gateway task
// and the cap check all follow it, and anything but the exact answer is heavy.
func TestIntent_TheClassifiersAnswerPicksTheRowTheWholeTurnRunsOn(t *testing.T) {
	zh := "\u6211\u6295\u4e86\u591a\u5c11\u4e2a\u516c\u53f8\uff1f" // how many companies have I applied to
	long := strings.Repeat("x", 290) + "TAIL-BEYOND-300-CHARS"
	cases := []struct {
		name, message, answer string
		err                   error
		light                 bool
	}{
		{name: "light", message: "Which interviews do I have this month?", answer: `{"tier":"light"}`, light: true},
		{name: "heavy", message: "Plan my week of applications.", answer: `{"tier":"heavy"}`},
		{name: "a message in Chinese", message: zh, answer: " {\"tier\":\"light\"}\n", light: true},
		{name: "prose", message: "Which interviews do I have this month?", answer: `This is a light question.`},
		{name: "a code fence", message: "Which interviews do I have this month?", answer: "```json\n{\"tier\":\"light\"}\n```"},
		{name: "an extra field", message: "Which interviews do I have this month?", answer: `{"tier":"light","why":"short"}`},
		{name: "text after the object", message: "Which interviews do I have this month?", answer: `{"tier":"light"} ok`},
		{name: "an unknown tier", message: "Which interviews do I have this month?", answer: `{"tier":"medium"}`},
		{name: "a failed call", message: "Which interviews do I have this month?", err: errors.New("provider down")},
		{name: "after a long answer", message: "And the other one?", answer: `{"tier":"light"}`, light: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.router.light, f.router.tierText, f.router.tierErr = lightRoute(), c.answer, c.err
			f.launcher.script = modelCallThenAnswer(t)
			sess, err := f.sessions.Create(context.Background(), sessions.Dashboard, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.sessions.Append(context.Background(), sessions.Message{SessionID: sess.ID, Role: sessions.RoleAssistant, Body: long, OK: true}); err != nil {
				t.Fatal(err)
			}
			res := f.sendTo(sess.ID, c.message)
			end := f.terminal()
			f.idle()
			if end.Type != wire.ChatFinal {
				t.Fatalf("terminal = %+v", end)
			}

			provider, model, level, task, tier := "applysmith", "scripted-1", "medium", llm.AgentTask, TierHeavy
			if c.light {
				provider, model, level, task, tier = "applysmith-light", "light-1", "low", llm.AgentLightTask, TierLight
			}
			p := f.launcher.started()[0]
			if args := strings.Join(p.spec.Args, " "); !strings.Contains(args, "--provider "+provider+" --model "+model) {
				t.Errorf("args = %s", args)
			}
			if p.thinking != level {
				t.Errorf("thinking = %q, want %q", p.thinking, level)
			}
			if models, _ := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "models.json")); !strings.Contains(string(models), `"`+provider+`"`) {
				t.Errorf("models.json lacks %s: %s", provider, models)
			}
			if len(f.router.calls) != 1 || f.router.calls[0].Task != task || !slices.Equal(f.router.capAsked, []string{task}) {
				t.Errorf("gateway calls %+v, cap checked for %v; want %s", f.router.calls, f.router.capAsked, task)
			}
			turns, _ := f.sessions.Turns(context.Background(), res.SessionID)
			if turns[0].Tier != tier {
				t.Errorf("recorded tier = %q, want %q", turns[0].Tier, tier)
			}
			if len(f.router.asked) != 1 {
				t.Fatalf("classifier called %d times", len(f.router.asked))
			}
			q := f.router.asked[0]
			prev := "<previous_answer>\n" + strings.Repeat("x", 290)
			if q.Task != llm.IntentTask || q.RunID != res.SessionID || !strings.Contains(q.Prompt, "<message>\n"+c.message+"\n</message>") ||
				!strings.HasPrefix(q.Prompt, prev) || strings.Contains(q.Prompt, "TAIL-BEYOND") || !strings.Contains(q.Prompt, "truncated") {
				t.Errorf("classifier request = %+v", q)
			}
		})
	}
}

// The daily cap is checked for the row the turn will run on: a light turn is
// not stopped by the kernel row's cap, nor a heavy one by the light row's.
func TestIntent_TheCapStopsOnlyTheTurnsOwnRow(t *testing.T) {
	cases := []struct {
		name                string
		tier                string
		kernelCap, lightCap bool
		stopped             bool
	}{
		{"light under its own cap", TierLight, true, false, false},
		{"light over its own cap", TierLight, false, true, true},
		{"heavy under its own cap", TierHeavy, false, true, false},
		{"heavy over its own cap", TierHeavy, true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.router.light, f.router.tierText = lightRoute(), `{"tier":"`+c.tier+`"}`
			f.router.blocked, f.router.lightBlock = c.kernelCap, c.lightCap
			f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
			f.send("Which interviews do I have this month?")
			end := f.terminal()
			f.idle()
			started := len(f.launcher.started()) > 0
			if started == c.stopped || (c.stopped && !strings.Contains(end.Text+end.Detail, "daily model spend cap")) {
				t.Fatalf("pi started %v, terminal %+v; want stopped %v", started, end, c.stopped)
			}
		})
	}
}
