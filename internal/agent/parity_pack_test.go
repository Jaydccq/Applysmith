package agent

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/agent/sessions"
)

// Tests named after the context-packing and compaction rows of the ask-agent
// parity audit.

func TestAgent_Pack_NoOrphanToolResultAfterDrop(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sid := newSession(t, f)
	add := func(m sessions.Message) {
		m.SessionID, m.OK = sid, true
		if m.Role == sessions.RoleToolResult {
			if _, err := f.sessions.AppendResult(ctx, m); err != nil {
				t.Fatal(err)
			}
			return
		}
		if _, err := f.sessions.Append(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// A turn whose call row is gone, as when the older half of a history was
	// cut away: its result is left alone.
	add(sessions.Message{TurnID: "t1", Role: sessions.RoleUser, Body: "look it up"})
	add(sessions.Message{TurnID: "t1", Role: sessions.RoleToolResult, ToolName: "tracker_query", ToolCallID: "call_gone", Body: "ORPHAN RESULT TEXT"})
	add(sessions.Message{TurnID: "t1", Role: sessions.RoleAssistant, Body: "done"})
	add(sessions.Message{TurnID: "t2", Role: sessions.RoleUser, Body: "again"})
	add(sessions.Message{TurnID: "t2", Role: sessions.RoleToolCall, ToolName: "tracker_query", ToolCallID: "call_here", Body: `{}`})
	add(sessions.Message{TurnID: "t2", Role: sessions.RoleToolResult, ToolName: "tracker_query", ToolCallID: "call_here", Body: "PAIRED RESULT TEXT"})
	add(sessions.Message{TurnID: "t2", Role: sessions.RoleAssistant, Body: "again done"})

	_, prefix, err := ContextPacker{Sessions: f.sessions}.Pack(ctx, &Turn{ID: "t3", SessionID: sid, Channel: ChannelDashboard})
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, m := range prefix {
		if m.Role != "user" && m.Role != "assistant" {
			t.Errorf("a %s message in the prefix: tool results travel inside the assistant's text, so none can be left without its call", m.Role)
		}
		all.WriteString(m.Text + "\n")
	}
	if strings.Contains(all.String(), "ORPHAN RESULT TEXT") || !strings.Contains(all.String(), "PAIRED RESULT TEXT") {
		t.Fatalf("prompt = %s", all.String())
	}
	// A turn is never split: dropping by budget drops whole turns.
	pk, _ := ContextPacker{Sessions: f.sessions}.load(ctx, &Turn{ID: "t3", SessionID: sid, Channel: ChannelDashboard})
	pk.budget = pk.headTokens() + AnswerReserve + turnTokens(pk.turns[1]) + 1
	kept, _ := pk.fit()
	if len(kept) != 1 || kept[0].id != "t2" || len(kept[0].msgs) != 4 {
		t.Fatalf("kept = %+v", kept)
	}
}
