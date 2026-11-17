// Clean-room: docs/provenance.md#CR-AGENT-TASKS

package api

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

// With no model for the agent, "Let the agent do it" is refused before it
// makes anything: its turn would end at no_model_route. The refusal is the
// turn's own words, which name Settings, Models, and no conversation or task
// is left behind.
func TestAgentIdeas_APressWithoutAnAgentModelIsRefused(t *testing.T) {
	h := newRoutingHarness(t)
	h.addInbox("https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001")
	r := h.chat(h.session(), "POST", "/api/agent/ideas/evaluate_inbox", nil)
	e, _ := r.json()["error"].(map[string]any)
	if msg, _ := e["message"].(string); r.StatusCode != 503 || r.errReason() != wire.ReasonNoModelRoute || !strings.Contains(msg, "Settings, Models") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var made int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT (SELECT count(*) FROM chat_sessions) + (SELECT count(*) FROM agent_tasks)`).Scan(&made); err != nil || made != 0 {
		t.Fatalf("%d conversations and tasks made (%v)", made, err)
	}
}

// "Let the agent do it" on the inbox: with nothing waiting there is nothing to
// hand over; with two postings waiting (and a newer one whose evaluation
// failed twice, which waits for the user), a press queues their evaluations in a
// new conversation whose task asks, in the app's words and with the count
// alone, for them to be judged. The task waits until both evaluations have
// ended and been told to the conversation, then takes its first turn by itself
// under that request. A second press while it works opens the same task.
func TestAgentIdeas_APressHandsTheInboxOverAsATaskThatStartsByItself(t *testing.T) {
	var mu sync.Mutex
	var prompt string
	script := func(p *scriptedPi) {
		status, cat := p.bridge("GET", "/internal/agent/catalog", nil)
		tools, ok := cat["tools"].([]any)
		if status != 200 || !ok {
			return
		}
		var names []string
		for _, tl := range tools {
			names = append(names, tl.(map[string]any)["name"].(string))
		}
		p.bridge("POST", "/internal/agent/hello", map[string]any{"tools": names})
		<-p.prompt
		_, tc := p.bridge("GET", "/internal/agent/turn-context", nil)
		mu.Lock()
		// What the model is given: the system prompt and the packed prefix,
		// whose last message carries the turn's records (the task block).
		prompt, _ = tc["systemPrompt"].(string)
		pre, _ := tc["prefix"].([]any)
		for _, m := range pre {
			if mm, ok := m.(map[string]any); ok {
				text, _ := mm["text"].(string)
				prompt += "\n" + text
			}
		}
		mu.Unlock()
		<-p.gate
		args := map[string]any{"outcome": "Evaluated both; Example Co is worth applying to.", "step": "Closing the task"}
		p.emit(assistantLine([]map[string]any{{"type": "toolCall", "id": "c1", "name": "task_done", "arguments": args}}, "toolUse"))
		p.emit(map[string]any{"type": "tool_execution_start", "toolCallId": "c1", "toolName": "task_done", "args": args})
		_, res := p.bridge("POST", "/internal/agent/tool-call", map[string]any{"toolCallId": "c1", "name": "task_done", "args": args})
		p.emit(map[string]any{"type": "tool_execution_end", "toolCallId": "c1", "toolName": "task_done",
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": res["text"]}}}, "isError": res["isError"]})
		p.emit(assistantLine([]map[string]any{{"type": "text", "text": "Both are evaluated."}}, "stop"))
		p.emit(map[string]any{"type": "agent_settled"})
	}
	h, l := chatHarness(t, script)
	cookie := h.session()
	press := func(idea string) result { return h.chat(cookie, "POST", "/api/agent/ideas/"+idea, nil) }

	if r := press("evaluate_everything"); r.StatusCode != 404 {
		t.Fatalf("an unknown idea: %d %s", r.StatusCode, r.body)
	}
	if r := press("evaluate_inbox"); r.StatusCode != 409 {
		t.Fatalf("an empty inbox: %d %s", r.StatusCode, r.body)
	}
	const failed = "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000003"
	for _, u := range []string{"https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001", "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000002", failed} {
		h.addInbox(u)
	}
	if err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE inbox SET attempts = 2, first_seen_at = '2999-01-01T00:00:00Z' WHERE url = ?`, failed)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := press("evaluate_inbox")
	if r.StatusCode != 202 {
		t.Fatalf("press: %d %s", r.StatusCode, r.body)
	}
	started := decode[wire.AgentIdeaStarted](t, r)
	if again := decode[wire.AgentIdeaStarted](t, press("evaluate_inbox")); again != started {
		t.Fatalf("a second press started %+v, not %+v", again, started)
	}
	l.release()

	var task wire.AgentTask
	for deadline := time.Now().Add(20 * time.Second); ; {
		list := decode[wire.AgentTaskList](t, h.chat(cookie, "GET", "/api/agent/tasks", nil))
		if len(list.Tasks) != 1 {
			t.Fatalf("tasks: %+v", list.Tasks)
		}
		if task = list.Tasks[0]; task.State == "done" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if task.ID != started.TaskID || task.SessionID != started.SessionID || task.Idea != "evaluate_inbox" || task.State != "done" || task.Turns != 1 {
		t.Fatalf("task = %+v", task)
	}
	if !strings.HasPrefix(task.Mandate, "Evaluate the 2 newest postings waiting in my pipeline inbox (their evaluations are queued in this conversation)") {
		t.Fatalf("mandate = %q", task.Mandate)
	}
	detail := decode[wire.ChatSessionDetail](t, h.chat(cookie, "GET", "/api/chat/sessions/"+started.SessionID, nil))
	// Each evaluation's end is told before the task's first turn. (This
	// server has no model key, so they end as failures, which are ends too.)
	told, turned := 0, -1
	for i, m := range detail.Messages {
		switch {
		case m.Role == "note" && strings.Contains(m.Text, "goes on by itself (turn 1 of"):
			turned = i
		case m.Role == "note" && turned < 0:
			told++
		}
	}
	if told != 2 || turned < 0 {
		t.Fatalf("%d runs told before the task's turn (at %d): %+v", told, turned, detail.Messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(prompt, "Delegated task ("+task.ID) || !strings.Contains(prompt, "Evaluate the 2 newest postings") {
		t.Fatalf("the turn was not the task's:\n%s", prompt)
	}
}
