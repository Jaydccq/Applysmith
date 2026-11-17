package llm

import (
	"errors"
	"testing"
)

func agentEnv(t *testing.T) *env {
	t.Helper()
	s := newOAServer(t, okHandler("ok"))
	e := newEnv(t)
	p := oaProvider("a", s)
	p.Preset = "openai"
	e.provider(p, fakeKey("ag"))
	e.route(AgentTask, Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkMedium, MaxOutputTokens: 4000,
		Fallbacks: []Fallback{{Provider: "a", Model: "gpt-6-luna"}}})
	return e
}

func TestLLM_AgentRoute_ResolvesTheRow(t *testing.T) {
	e := agentEnv(t)
	got, err := e.r.AgentRoute(bg(), AgentTask)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "a" || got.Model != "gpt-6.1-sol" || got.MaxOutputTokens != 4000 || got.Config.Preset != "openai" || got.PiLogin != "" {
		t.Fatalf("route = %+v", got)
	}
}

func TestLLM_AgentRoute_RefusesWhatPiCannotCall(t *testing.T) {
	// No route at all: the preset default for agent-kernel is unrouted under a CLI preset.
	e := newEnv(t)
	if _, err := e.r.AgentRoute(bg(), AgentTask); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("no route: %v", err)
	}
	// The fake kind offers tools but has no endpoint for pi to call.
	e = newEnv(t)
	e.provider(ProviderConfig{ID: "f", Kind: KindFake}, "")
	e.route(AgentTask, Route{Provider: "f", Model: "scripted"})
	if _, err := e.r.AgentRoute(bg(), AgentTask); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("fake provider: %v", err)
	}
	// A provider with no stored key is unconfigured before any process starts.
	e = newEnv(t)
	s := newOAServer(t, okHandler("ok"))
	e.provider(oaProvider("a", s), "")
	e.route(AgentTask, Route{Provider: "a", Model: "some-model"})
	if _, err := e.r.AgentRoute(bg(), AgentTask); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("missing key: %v", err)
	}
	// The offline simulation stands in for the router's own calls only, even
	// over a route pi could call.
	if err := e.r.SetOffline(bg(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.AgentRoute(bg(), AgentTask); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline: %v", err)
	}
}

func TestLLM_PiLogin_ParsesOnlyThePiProviderForm(t *testing.T) {
	cases := map[string]string{"pi:openai-codex": "openai-codex", "pi:anthropic": "anthropic", "pi:a.b_c-1": "a.b_c-1"}
	for in, want := range cases {
		if got, ok := PiLogin(in); !ok || got != want {
			t.Errorf("PiLogin(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "pi:", "pi:../x", "pi:Upper", "pi:a b", "openai", "pi:-lead", "PI:x"} {
		if _, ok := PiLogin(in); ok {
			t.Errorf("PiLogin(%q) is accepted", in)
		}
	}
}

func TestLLM_PiLogin_OnlyThePrimaryOfTheAgentRowMayUseOne(t *testing.T) {
	e := agentEnv(t)
	ok := Route{Provider: "pi:openai-codex", Model: "gpt-6.1-sol"}
	if err := e.r.PatchRoutes(bg(), map[string]*Route{AgentTask: &ok}); err != nil {
		t.Fatalf("the agent row: %v", err)
	}
	for name, patch := range map[string]map[string]*Route{
		"another task":      {"deep-eval": {Provider: "pi:openai-codex", Model: "gpt-6.1-sol"}},
		"as a fallback":     {AgentTask: {Provider: "a", Model: "gpt-6.1-sol", Fallbacks: []Fallback{{Provider: "pi:openai-codex", Model: "gpt-6-luna"}}}},
		"as a fallback too": {"deep-eval": {Provider: "a", Model: "gpt-6.1-sol", Fallbacks: []Fallback{{Provider: "pi:openai-codex", Model: "gpt-6-luna"}}}},
		"a malformed id":    {AgentTask: {Provider: "pi:../x", Model: "gpt-6.1-sol"}},
	} {
		if err := e.r.PatchRoutes(bg(), patch); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestLLM_PiLogin_RouteHasNoKeyIsASubscriptionAndWritesAZeroCostPiRow(t *testing.T) {
	e := agentEnv(t)
	if err := e.r.PatchRoutes(bg(), map[string]*Route{AgentTask: {Provider: "pi:openai-codex", Model: "gpt-6.1-sol", Thinking: ThinkMedium}}); err != nil {
		t.Fatal(err)
	}
	route, err := e.r.AgentRoute(bg(), AgentTask)
	if err != nil {
		t.Fatal(err)
	}
	if route.PiLogin != "openai-codex" || route.Config.ID != "pi:openai-codex" {
		t.Fatalf("route = %+v", route)
	}
	e.r.RecordAgentUsage(bg(), route, AgentUsage{StartedAt: e.clock.now(), SessionID: "ses_9", Outcome: "ok", Usage: Usage{Input: 5000, Output: 400}})
	var kind, prov, billing string
	var cost float64
	if err := e.st.Read().QueryRow(`SELECT provider_kind, provider_id, billing, cost_usd FROM llm_ledger WHERE run_id = 'ses_9'`).Scan(&kind, &prov, &billing, &cost); err != nil {
		t.Fatal(err)
	}
	if kind != LedgerKindPi || prov != "pi:openai-codex" || billing != BillingSubscription || cost != 0 {
		t.Fatalf("row = %s %s %s %v", kind, prov, billing, cost)
	}
	if spend, _ := e.r.DailySpend(bg()); spend != 0 {
		t.Fatalf("a subscription turn counted %v toward the cap", spend)
	}
}

func TestLLM_PiLogin_TheEnvironmentRowAcceptsItForTheAgentOnly(t *testing.T) {
	e := agentEnv(t)
	e.envs[EnvVar(AgentTask)] = "pi:openai-codex/gpt-6.1-sol"
	if err := e.r.PatchRoutes(bg(), map[string]*Route{AgentTask: nil}); err != nil {
		t.Fatal(err)
	}
	route, err := e.r.AgentRoute(bg(), AgentTask)
	if err != nil || route.PiLogin != "openai-codex" || route.Model != "gpt-6.1-sol" {
		t.Fatalf("route = %+v, %v", route, err)
	}
	e.envs[EnvVar("deep-eval")] = "pi:openai-codex/gpt-6.1-sol"
	if res, err := e.r.Resolve(bg(), "deep-eval"); err == nil && res.Provider == "pi:openai-codex" {
		t.Fatalf("another task resolved to a pi login: %+v", res)
	}
}

// agent-light is the row of light turns and turns intent routing on, so no
// preset routes it; it follows agent-kernel's rules otherwise.
func TestLLM_AgentLight_IsOptInAndFollowsTheKernelsRules(t *testing.T) {
	t.Run("a preset leaves it unrouted without calling it missing", func(t *testing.T) {
		e := newEnv(t)
		_ = e.r.SetKey("deepseek", fakeKey("k"))
		if err := e.r.ConfigurePreset(bg(), "deepseek", ConfigureOptions{}); err != nil {
			t.Fatal(err)
		}
		table, err := e.r.Show(bg())
		if err != nil {
			t.Fatal(err)
		}
		routed := map[string]bool{}
		for _, r := range table.Rows {
			routed[r.Task.ID] = true
		}
		if _, missing := table.Unrouted[AgentLightTask]; !routed[AgentTask] || !routed[IntentTask] || routed[AgentLightTask] || missing {
			t.Fatalf("rows %v, unrouted %v", routed, table.Unrouted)
		}
		if _, err := e.r.AgentRoute(bg(), AgentLightTask); !errors.Is(err, ErrNoRoute) {
			t.Fatalf("light route: %v", err)
		}
	})
	t.Run("a route resolves for the gateway", func(t *testing.T) {
		e := agentEnv(t)
		e.route(AgentLightTask, Route{Provider: "a", Model: "gpt-6-luna", Thinking: ThinkLow})
		got, err := e.r.AgentRoute(bg(), AgentLightTask)
		if err != nil || got.Task.ID != AgentLightTask || got.Model != "gpt-6-luna" || got.PiLogin != "" {
			t.Fatalf("route = %+v, %v", got, err)
		}
	})
	t.Run("what the kernel row refuses it refuses", func(t *testing.T) {
		e := agentEnv(t)
		e.provider(ProviderConfig{ID: "cli", Kind: KindCLI, Program: "codex"}, "")
		for name, row := range map[string]Route{
			"a pi login":        {Provider: "pi:openai-codex", Model: "gpt-6.1-sol"},
			"a pi login backup": {Provider: "a", Model: "gpt-6.1-sol", Fallbacks: []Fallback{{Provider: "pi:openai-codex", Model: "gpt-6-luna"}}},
			"a cli provider":    {Provider: "cli", Model: "default"},
		} {
			if err := e.r.PatchRoutes(bg(), map[string]*Route{AgentLightTask: &row}); err == nil {
				t.Errorf("%s was accepted", name)
			}
		}
		if err := ValidateBudget(AgentLightTask, 1); err == nil {
			t.Error("a daily budget was accepted")
		}
	})
	t.Run("only agent rows are agent routes", func(t *testing.T) {
		if _, err := agentEnv(t).r.AgentRoute(bg(), "deep-eval"); KindOf(err) != KindUnknownTask {
			t.Fatalf("deep-eval: %v", err)
		}
	})
}
