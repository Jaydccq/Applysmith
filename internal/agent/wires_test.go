package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/llm"
)

// planAndChatRoute is the agent row of a ChatGPT plan primary with a Chat
// Completions fallback: two wires, so pi routes it.
func planAndChatRoute() llm.AgentRoute {
	plan := llm.ProviderConfig{ID: "chatgpt", Kind: llm.KindChatGPT, BaseURL: "https://api.openai.com/v1", Preset: "chatgpt"}
	ds := llm.ProviderConfig{ID: "ds", Kind: llm.KindOpenAI, BaseURL: "https://api.deepseek.example.test", Preset: "deepseek"}
	return llm.AgentRoute{
		Resolved: llm.Resolved{Task: llm.Task{ID: llm.AgentTask}, Provider: "chatgpt", Model: "gpt-6.1-sol", Thinking: llm.ThinkMedium, MaxOutputTokens: 4000,
			Fallbacks: []llm.Fallback{{Provider: "ds", Model: "deepseek-flash"}}},
		Config: plan,
		Heads: []llm.AgentHead{
			{Wire: llm.WireResponses, Config: plan, Model: "gpt-6.1-sol"},
			{Wire: llm.WireOpenAI, Config: ds, Model: "deepseek-flash"},
		},
	}
}

// A row that spans wires is declared as one physical provider per wire, each
// with the api and the quirks of its own head; the process is started on the
// virtual model, told the order to try them in, and given pi's retry.
func TestSupervisor_ARowThatSpansWiresStartsPiOnTheVirtualModelWithRetryOn(t *testing.T) {
	f := newFixture(t)
	f.router.route = planAndChatRoute()
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.send("hello")
	f.terminal()
	f.idle()

	p := f.launcher.started()[0]
	if args := strings.Join(p.spec.Args, " "); !strings.Contains(args, "--provider applysmith --model agent") {
		t.Fatalf("args = %s", args)
	}
	var spec struct {
		Provider, ID string
		Models       []struct{ Provider, ID string }
	}
	for _, kv := range p.spec.Env {
		if v, ok := strings.CutPrefix(kv, "APPLYSMITH_AGENT_ROUTE="); ok {
			if err := json.Unmarshal([]byte(v), &spec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if spec.Provider != "applysmith" || spec.ID != "agent" || len(spec.Models) != 2 ||
		spec.Models[0].Provider != "applysmith-responses" || spec.Models[0].ID != "gpt-6.1-sol" ||
		spec.Models[1].Provider != "applysmith-chat" || spec.Models[1].ID != "deepseek-flash" {
		t.Fatalf("route env = %+v", spec)
	}
	b, err := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]struct {
			API    string           `json:"api"`
			Models []map[string]any `json:"models"`
		}
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	rs, ch := doc.Providers["applysmith-responses"], doc.Providers["applysmith-chat"]
	if len(doc.Providers) != 2 || rs.API != "openai-responses" || rs.Models[0]["id"] != "gpt-6.1-sol" || rs.Models[0]["compat"] != nil ||
		ch.API != "openai-completions" || ch.Models[0]["id"] != "deepseek-flash" || ch.Models[0]["compat"] == nil {
		t.Fatalf("models.json = %s", b)
	}
	if p.autoRetry != "true" {
		t.Errorf("set_auto_retry enabled = %q", p.autoRetry)
	}
	if set, _ := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "settings.json")); string(set) != retrySettings {
		t.Errorf("settings.json = %s", set)
	}
}

// A row of one wire keeps pi's retry off, whatever settings.json holds (it is
// the same for every process): the gateway walks its candidates, so pi's retry
// would walk them again. A pi login keeps its retry, sent explicitly too.
func TestSupervisor_ARowOfOneWireKeepsPisRetryOff(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.send("hello")
	f.terminal()
	f.idle()
	p := f.launcher.started()[0]
	if p.autoRetry != "false" {
		t.Errorf("set_auto_retry enabled = %q", p.autoRetry)
	}
	for _, kv := range p.spec.Env {
		if strings.HasPrefix(kv, "APPLYSMITH_AGENT_ROUTE=") {
			t.Errorf("a one-wire row carries a route: %s", kv)
		}
	}
	if set, _ := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "settings.json")); string(set) != retrySettings {
		t.Errorf("settings.json must be the same for every row: %s", set)
	}
}
