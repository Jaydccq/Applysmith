package api

import (
	"strings"
	"testing"

	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/notify"
)

// Tests named after the Ask Agent rows of the parity audit that live at the
// API.

func TestAgent_Done_FreshReadNoModelTurnNoSubmitApproval(t *testing.T) {
	gate := gatedWindow{release: make(chan struct{})}
	h := newDomainHarness(t, gate.fn)
	waitingRun(t, h, "run_1", "sign-in", "tab-1", true)
	chat := sessionOfRun(t, h, "run_1")
	waitNotes(t, h, chat, 1)
	turnsOf := func() (n int) {
		_ = h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM chat_turns WHERE session_id = ?`, chat).Scan(&n)
		return n
	}

	// A held submit waiting for a confirmation is never confirmed by Done.
	waitingRun(t, h, "run_confirm", "confirmation", "tab-2", true)
	if r := h.post("/api/runs/run_confirm/done", nil); r.StatusCode != 409 || !strings.Contains(string(r.body), "confirmation") {
		t.Fatalf("Done on a run that waits for a confirmation: %d %s", r.StatusCode, r.body)
	}
	if e := entryOf(t, h, "run_confirm"); e.State != notify.NeedsYou {
		t.Fatalf("the run moved to %s", e.State)
	}

	r := h.post("/api/runs/run_1/done", nil)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var env wire.Success[wire.BoardEntry]
	strictInto(t, r.body, &env)
	if env.Data.State != string(notify.Working) {
		t.Fatalf("the run is %s after Done", env.Data.State)
	}
	// The conversation is told by a system-made line after a fresh read of the
	// page; no turn starts and nobody speaks as the user or the assistant.
	h.domainOf().completion.resumed("run_1", applyapi.DoneFromDashboard, apply.PageGlance{URL: "https://jobs.example.test/p/1", Title: "Apply", Controls: 4})
	notes := waitNotes(t, h, chat, 2)
	if last := notes[len(notes)-1]; !last.SystemMade || !strings.Contains(last.Body, "I read the page again") {
		t.Fatalf("note = %+v", last)
	}
	if n := turnsOf(); n != 0 {
		t.Fatalf("%d model turns started by Done", n)
	}
	ms, _ := h.domainOf().sessions.Messages(t.Context(), chat)
	for _, m := range ms {
		if m.Role != chatsessions.RoleNote {
			t.Errorf("Done wrote a %s message", m.Role)
		}
	}
	close(gate.release)
	waitEntry(t, h, "run_1", func(e notify.Entry) bool { return e.State != notify.Working })
}

func TestAgent_Model_ConversationOverrideDecision(t *testing.T) {
	// Decision (audit open question 8): no per-conversation model override. A
	// message names no model, and the turn runs on the agent-kernel row.
	h, l := chatHarness(t, trackerTurn)
	cookie := h.session()
	for _, body := range []map[string]any{
		{"text": "count them", "model": "other/model"},
	} {
		if r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", body); r.StatusCode != 422 {
			t.Errorf("%v: %d %s; an unknown field must be refused", body, r.StatusCode, r.body)
		}
	}
	if l.count() != 0 {
		t.Fatalf("%d processes started for refused messages", l.count())
	}
}
