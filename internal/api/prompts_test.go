// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package api

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/agent/triggers"
	"applysmith/internal/api/wire"
)

func TestPromptsAPI_CreateListEditPauseResumeDeleteBothKinds(t *testing.T) {
	h := newDomainHarness(t)
	r := h.post("/api/agent/prompts", wire.AgentPromptCreate{Text: "Summarise the replies I owe", Cadence: &wire.GoalCadence{Days: []string{"mon", "thu"}, Time: "08:00"}})
	c := decode[wire.AgentPrompt](t, r)
	if r.StatusCode != 201 || c.Kind != "cadence" || c.State != wire.PromptActive || c.NextRun == nil || c.Cadence.Time != "08:00" {
		t.Fatalf("cadence create: %d %s", r.StatusCode, r.body)
	}
	r = h.post("/api/agent/prompts", wire.AgentPromptCreate{Text: "Draft a reply", Trigger: &wire.PromptTriggerSpec{Signals: []string{"recruiter_reply_needed"}, Company: "Example Corp"}})
	tr := decode[wire.AgentPrompt](t, r)
	if r.StatusCode != 201 || tr.Kind != "trigger" || tr.State != wire.PromptActive || tr.NextRun != nil || len(tr.Firings) != 0 || tr.Trigger.Company != "Example Corp" {
		t.Fatalf("trigger create: %d %s", r.StatusCode, r.body)
	}
	list := decode[wire.AgentPromptList](t, h.get("/api/agent/prompts"))
	if len(list.Prompts) != 2 || list.Limit != 10 {
		t.Fatalf("list = %+v", list)
	}
	text := "Draft a short reply"
	r = h.send("PATCH", "/api/agent/prompts/"+tr.ID, wire.AgentPromptPatch{Text: &text, Trigger: &wire.PromptTriggerSpec{Signals: []string{"rejection"}}})
	if got := decode[wire.AgentPrompt](t, r); r.StatusCode != 200 || got.Text != text || got.Trigger.Signals[0] != "rejection" || got.Trigger.Company != "" {
		t.Fatalf("edit: %d %s", r.StatusCode, r.body)
	}
	paused := wire.PromptPaused
	r = h.send("PATCH", "/api/agent/prompts/"+tr.ID, wire.AgentPromptPatch{State: &paused})
	if got := decode[wire.AgentPrompt](t, r); got.State != wire.PromptPaused {
		t.Fatalf("pause: %s", r.body)
	}
	active := wire.PromptActive
	r = h.send("PATCH", "/api/agent/prompts/"+tr.ID, wire.AgentPromptPatch{State: &active})
	if got := decode[wire.AgentPrompt](t, r); got.State != wire.PromptActive {
		t.Fatalf("resume: %s", r.body)
	}
	for _, cs := range []struct {
		name, method, path string
		body               any
		status             int
	}{
		{"unknown", "PATCH", "/api/agent/prompts/prompt_none", wire.AgentPromptPatch{Text: &text}, 404},
		{"unknown field", "PATCH", "/api/agent/prompts/" + tr.ID, map[string]any{"owner": "x"}, 422},
		{"state proposed", "PATCH", "/api/agent/prompts/" + tr.ID, map[string]any{"state": "proposed"}, 422},
		{"acts", "POST", "/api/agent/prompts", wire.AgentPromptCreate{Text: "every morning apply to the best new posting", Cadence: &wire.GoalCadence{Days: []string{"mon"}, Time: "09:00"}}, 422},
		{"confirm an active prompt", "POST", "/api/agent/prompts/" + tr.ID + "/confirm", nil, 409},
	} {
		if r := h.send(cs.method, cs.path, cs.body); r.StatusCode != cs.status {
			t.Errorf("%s: %d %s", cs.name, r.StatusCode, r.body)
		}
	}
	if r := h.send("DELETE", "/api/agent/prompts/"+c.ID, nil); r.StatusCode != 200 {
		t.Fatalf("delete: %d %s", r.StatusCode, r.body)
	}
	if got := decode[wire.AgentPromptList](t, h.get("/api/agent/prompts")); len(got.Prompts) != 1 {
		t.Fatalf("after delete: %+v", got)
	}
}

func TestPromptsAPI_EleventhPromptIsAConflict(t *testing.T) {
	h := newDomainHarness(t)
	body := wire.AgentPromptCreate{Text: "Summarise", Cadence: &wire.GoalCadence{Days: []string{"mon"}, Time: "09:00"}}
	for i := range triggers.MaxPrompts {
		if r := h.post("/api/agent/prompts", body); r.StatusCode != 201 {
			t.Fatalf("prompt %d: %d %s", i, r.StatusCode, r.body)
		}
	}
	if r := h.post("/api/agent/prompts", body); r.StatusCode != 409 || !strings.Contains(string(r.body), "delete one") {
		t.Fatalf("eleventh: %d %s", r.StatusCode, r.body)
	}
}

func TestPromptsAPI_AgentProposalWaitsForTheUsersConfirmButton(t *testing.T) {
	h := newDomainHarness(t)
	d, err := h.srv.dom()
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.prompts.Create(context.Background(), triggers.NewPrompt{Text: "Draft a reply", Trigger: triggers.Trigger{Types: []string{triggers.TypeReply}}, Origin: triggers.OriginAgent}, triggers.ActorAgent)
	if err != nil {
		t.Fatal(err)
	}
	got := decode[wire.AgentPromptList](t, h.get("/api/agent/prompts")).Prompts
	if len(got) != 1 || got[0].State != "proposed" || got[0].Origin != "agent" {
		t.Fatalf("list = %+v", got)
	}
	text, active := "Draft a longer reply", wire.PromptActive
	if r := h.send("PATCH", "/api/agent/prompts/"+p.ID, wire.AgentPromptPatch{Text: &text, State: &active}); r.StatusCode != 409 {
		t.Fatalf("activating a proposal by edit: %d %s", r.StatusCode, r.body)
	}
	if got := decode[wire.AgentPromptList](t, h.get("/api/agent/prompts")).Prompts; got[0].Text != "Draft a reply" {
		t.Fatalf("the refused edit changed the prompt: %+v", got[0])
	}
	r := h.send("POST", "/api/agent/prompts/"+p.ID+"/confirm", nil)
	if c := decode[wire.AgentPrompt](t, r); r.StatusCode != 200 || c.State != wire.PromptActive {
		t.Fatalf("confirm: %d %s", r.StatusCode, r.body)
	}
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM gate_decisions WHERE gate = 'scheduled_prompt' AND code IN ('prompt_proposed', 'prompt_confirmed')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
}
