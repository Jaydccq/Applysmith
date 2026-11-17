package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
)

func TestAgent_MessageLimit_RejectsAtEightThousandAndOneAndCreatesNothing(t *testing.T) {
	f := newFixture(t)
	_, err := f.agent.Send(context.Background(), SendRequest{Text: strings.Repeat("a", wire.MaxChatMessage+1)})
	if !errors.Is(err, ErrMessageTooLong) || !strings.Contains(err.Error(), "8000") {
		t.Fatalf("err = %v", err)
	}
	if _, total, _ := f.sessions.List(context.Background(), sessions.ListQuery{}); total != 0 {
		t.Fatalf("%d conversations were created", total)
	}
	if _, err := f.agent.Send(context.Background(), SendRequest{Text: "   "}); !errors.Is(err, ErrEmptyMessage) {
		t.Fatalf("blank message = %v", err)
	}
	if _, err := f.agent.Send(context.Background(), SendRequest{SessionID: "ses_missing", Text: "hi"}); err == nil {
		t.Fatal("a message to a missing conversation was accepted")
	}
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.send(strings.Repeat("a", wire.MaxChatMessage)) // exactly the limit is fine
	f.terminal()
	f.idle()
}

func TestAgent_OnlyOneTurnRunsPerConversation(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = func(p *fakePi) {
		p.unanswered.Store("abort", true)
		p.hello()
		p.command("prompt")
		p.command("abort")
	}
	res := f.send("first")
	f.sink.next(t, func(e wire.ChatEvent) bool { return e.Type == wire.ChatTurnStart })
	if _, err := f.agent.Send(context.Background(), SendRequest{SessionID: res.SessionID, Text: "second"}); !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("second message = %v", err)
	}
	if id, ok := f.agent.ActiveTurn(res.SessionID); !ok || id != res.TurnID {
		t.Fatalf("active turn = %q %v", id, ok)
	}
	if _, err := f.agent.Cancel(res.SessionID); err != nil {
		t.Fatal(err)
	}
	f.terminal()
	f.idle()
	if _, ok := f.agent.ActiveTurn(res.SessionID); ok {
		t.Fatal("the turn is still active after it ended")
	}
}

func TestAgent_Start_EndsTurnsAStoppedProcessLeftRunningAndStandsInForTheirCalls(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sess, _ := f.sessions.Create(ctx, sessions.Dashboard, "")
	if err := f.sessions.StartTurn(ctx, sessions.Turn{ID: "turn_old", SessionID: sess.ID, Channel: sessions.Dashboard, Origin: sessions.OriginModel}); err != nil {
		t.Fatal(err)
	}
	f.sessions.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: "turn_old", Role: sessions.RoleToolCall, ToolName: "scan_portal", ToolCallID: "call_1", Body: "{}", OK: true})
	if err := f.agent.Start(ctx); err != nil {
		t.Fatal(err)
	}
	turns, _ := f.sessions.Turns(ctx, sess.ID)
	if turns[0].Status != sessions.TurnError || turns[0].Reason != pirpc.ReasonUnavailable {
		t.Fatalf("turn = %+v", turns[0])
	}
	msgs, _ := f.sessions.Messages(ctx, sess.ID)
	if last := msgs[len(msgs)-1]; last.Role != sessions.RoleToolResult || !last.SystemMade || !strings.Contains(last.Body, "runs board") {
		t.Fatalf("last row = %+v", last)
	}
}

func TestStepPhrase(t *testing.T) {
	label := "Reading the tracker"
	for _, tc := range []struct{ name, in, want string }{
		{"shown as given", "Reading the Example Corp posting", "Reading the Example Corp posting"},
		{"absent uses the label", "", label},
		{"blank uses the label", "   ", label},
		{"control characters are removed", "Reading\x00 the\x1b[31m posting\n", "Reading the[31m posting"},
		{"cut to sixty characters", strings.Repeat("a", 59) + " bbbbbbbbbb", strings.Repeat("a", 59)},
		{"two hundred characters use the label", strings.Repeat("a", 200), label},
		{"one hundred and ninety-nine are cut", strings.Repeat("a", 199), strings.Repeat("a", 60)},
		{"a key-shaped phrase uses the label", "Using sk-" + strings.Repeat("a", 8) + "x" + strings.Repeat("b", 24), label},
	} {
		if got := StepPhrase(tc.in, label); got != tc.want {
			t.Errorf("%s: StepPhrase = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPacker_NewestFirstWithinTheBudgetAndToolTrafficAsText(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sess, _ := f.sessions.Create(ctx, sessions.Dashboard, "")
	add := func(turn string, role sessions.Role, body, tool, call string) {
		f.sessions.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: turn, Role: role, Body: body, ToolName: tool, ToolCallID: call, OK: true})
	}
	add("t1", sessions.RoleUser, "oldest question", "", "")
	add("t1", sessions.RoleAssistant, strings.Repeat("old answer ", 50), "", "")
	add("t2", sessions.RoleUser, "how many are applied?", "", "")
	add("t2", sessions.RoleToolCall, `{"status":"applied"}`, "tracker_query", "c1")
	f.sessions.AppendResult(ctx, sessions.Message{SessionID: sess.ID, TurnID: "t2", ToolCallID: "c1", Body: "3 rows", OK: true})
	add("t2", sessions.RoleAssistant, "Three.", "", "")
	add("t3", sessions.RoleUser, "the current message", "", "")

	pst := time.FixedZone("PST", -8*3600)
	p := ContextPacker{Sessions: f.sessions, Now: func() time.Time { return time.Date(2026, 11, 2, 19, 40, 0, 0, pst) }}
	system, prefix, err := p.Pack(ctx, &Turn{ID: "t3", SessionID: sess.ID, Channel: ChannelDashboard})
	if err != nil || !strings.Contains(system, "Applysmith") {
		t.Fatalf("system = %q, %v", system, err)
	}
	// The model is told the date, after the fixed rules so their cache holds:
	// without it, "until the end of November" was asked back as "which year?".
	if !strings.HasPrefix(system, systemPrompt(ChannelDashboard)) || !strings.Contains(system, "Today is Monday, 2 November 2026 (local time zone PST)") {
		t.Fatalf("system = %q", system)
	}
	var roles []string
	for _, m := range prefix {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, " ") != "user assistant user assistant" {
		t.Fatalf("roles = %v", roles)
	}
	if got := prefix[3].Text; !strings.Contains(got, `[tool tracker_query {"status":"applied"} -> `+FenceOpen+` tool="tracker_query">> 3 rows `+FenceClose+`]`) || !strings.HasSuffix(got, "Three.") {
		t.Fatalf("tool traffic = %q", got)
	}
	// The head (system rules and the date) plus the answer reserve leave room
	// for the newest turn only: t2 is about 25 tokens, t1 about 140.
	head := sessions.EstimateTokens(systemPrompt(ChannelDashboard)+dateLine(p.Now())) + AnswerReserve
	_, small, _ := ContextPacker{Sessions: f.sessions, Budget: head + 60, Now: p.Now}.Pack(ctx, &Turn{ID: "t3", SessionID: sess.ID, Channel: ChannelDashboard})
	if len(small) != 2 || !strings.Contains(small[1].Text, "Three.") {
		t.Fatalf("under a small budget only the newest turn fits: %+v", small)
	}
	sysSched, _, _ := p.Pack(ctx, &Turn{ID: "t3", SessionID: sess.ID, Channel: ChannelScheduled})
	if !strings.Contains(sysSched, "read and draft") {
		t.Error("a scheduled turn is not told it cannot change anything")
	}
	sysTG, _, _ := p.Pack(ctx, &Turn{ID: "t3", SessionID: sess.ID, Channel: ChannelTelegram})
	if !strings.Contains(sysTG, "Telegram chat on the person's phone") || strings.Contains(system, "Telegram chat") {
		t.Error("only a Telegram turn is told it answers in a chat on a phone")
	}
}

// The note reaches pi at the end of the message; the stored message, which
// later turns, the title and the intent checks read, stays the user's own.
func TestAgent_TheLanguageNoteIsSentButNotStored(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	msg := "\u4e3a\u4ec0\u4e48\u8df3\u8fc7\u4e86 Ramp\uff1f"
	res := f.send(msg)
	f.terminal()
	f.idle()
	p := f.launcher.started()[0]
	p.inMu.Lock()
	prompts := append([]string(nil), p.prompts...)
	p.inMu.Unlock()
	if len(prompts) != 1 || !strings.HasPrefix(prompts[0], msg) || !strings.HasSuffix(prompts[0], languageNote(msg)) || languageNote(msg) == "" {
		t.Fatalf("prompts = %q", prompts)
	}
	msgs, err := f.sessions.Messages(context.Background(), res.SessionID)
	if err != nil || len(msgs) == 0 || msgs[0].Body != msg {
		t.Fatalf("stored = %+v, %v", msgs, err)
	}
}
