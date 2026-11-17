package llm

import (
	"net/http"
	"strings"
	"testing"
)

func TestLLM_ProviderCatalog_CallableAPIEndpoints(t *testing.T) {
	for name, endpoint := range map[string]string{
		"opencode-go": "https://opencode.ai/inference/go/openai/v1",
		"minimax":     "https://api.minimax.io/v1", "minimax-cn": "https://api.minimax.cn/v1",
		"zai": "https://api.z.ai/api/paas/v4", "bigmodel": "https://open.bigmodel.cn/api/paas/v4",
		"xai": "https://api.x.ai/v1", "kimi-code": "https://api.kimi.com/coding/v1",
	} {
		pr, ok := PresetByName(name)
		if !ok || pr.Kind != KindOpenAI || pr.BaseURL != endpoint || !pr.InSetup {
			t.Errorf("%s = %+v", name, pr)
		}
		if pr.Embedding != "" || pr.WebSearch {
			t.Errorf("%s advertises an unimplemented endpoint capability", name)
		}
	}
	for model, want := range map[string]bool{"MiniMax-M3.1-Flash": true, "grok-4.7": true, "glm-5.3-flash": true, "glm-5.3": false} {
		if got := TakesImages(model); got != want {
			t.Errorf("TakesImages(%q) = %v, want %v", model, got, want)
		}
	}
}

// Every call is sent to a local fake server; this never spends a provider key.
func TestLLM_ProviderCatalog_RequestSemantics(t *testing.T) {
	for _, name := range []string{"opencode-go", "minimax", "minimax-cn", "zai", "bigmodel", "xai"} {
		for _, thinking := range []string{ThinkOff, ThinkMinimal, ThinkLow, ThinkMedium, ThinkHigh, ThinkXHigh} {
			t.Run(name+"/"+thinking, func(t *testing.T) {
				pr, _ := PresetByName(name)
				model := pr.Models[ClassFast]
				e := newEnv(t)
				srv := newOAServer(t, okHandler("ok"))
				p := pr.ProviderConfig()
				p.BaseURL = srv.URL
				e.provider(p, fakeKey("catalog"))
				if pr.RequireUserPrice {
					if err := e.r.SetPrice(bg(), p.ID, model, Price{1, 1, 1}); err != nil {
						t.Fatal(err)
					}
				}
				e.route("jd-extract", Route{Provider: p.ID, Model: model, Thinking: thinking, MaxOutputTokens: 4096})
				if _, err := e.r.Complete(bg(), Request{Task: "jd-extract", Prompt: "hi"}); err != nil {
					t.Fatal(err)
				}
				want := thinking
				switch thinking {
				case ThinkOff, ThinkMinimal:
					want = "low"
				case ThinkXHigh:
					want = pr.MaxEffort
				case ThinkMedium:
					if name == "zai" || name == "bigmodel" || name == "opencode-go" {
						want = "high"
					}
				}
				if srv.last["reasoning_effort"] != want || srv.last[pr.MaxTokensParam] != float64(4096) {
					t.Fatalf("request = %v, want effort %s and ceiling field %s", srv.last, want, pr.MaxTokensParam)
				}
				if _, sent := srv.last["temperature"]; sent {
					t.Fatal("mandatory-reasoning model must keep provider temperature")
				}
			})
		}
	}
}

func TestLLM_ProviderCatalog_GatewayPreservesRequiredToolHistory(t *testing.T) {
	for _, name := range []string{"opencode-go", "minimax", "minimax-cn", "zai", "bigmodel", "xai"} {
		pr, _ := PresetByName(name)
		body := agentBody(WireOpenAI, piRequest(), candidate{provider: pr.ProviderConfig(), model: pr.Models[ClassFast]}, ThinkMedium, "session")
		msgs := body["messages"].([]any)
		assistant := msgs[2].(map[string]any)
		_, kept := assistant["reasoning_content"]
		if kept != (name == "minimax" || name == "minimax-cn") || assistant["tool_calls"] == nil {
			t.Fatalf("%s history = %v", name, assistant)
		}
		if name == "zai" || name == "bigmodel" || name == "opencode-go" {
			if body["tool_stream"] != true || body["reasoning_effort"] != "high" {
				t.Fatalf("GLM gateway request = %v", body)
			}
		}
		if body[pr.MaxTokensParam] != 16000 {
			t.Fatalf("%s ceiling = %v", name, body)
		}
	}
}

func TestLLM_ProviderCatalog_RegionalPriceRequiredPerInstance(t *testing.T) {
	for _, name := range []string{"minimax-cn", "bigmodel"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			pr, _ := PresetByName(name)
			if err := e.r.SetKey(name, fakeKey("region")); err != nil {
				t.Fatal(err)
			}
			// Configuration must succeed before prices can be set.
			if err := e.r.ConfigurePreset(bg(), name, ConfigureOptions{}); err != nil {
				t.Fatal(err)
			}
			srv := newOAServer(t, okHandler("ok"))
			p := pr.ProviderConfig()
			p.ID, p.BaseURL = "regional-account", srv.URL
			e.provider(p, fakeKey("region"))
			model := pr.Models[ClassFast]
			e.route("jd-extract", Route{Provider: p.ID, Model: model, Thinking: ThinkOff})
			// A different instance's price cannot authorize this instance's spend.
			if err := e.r.SetPrice(bg(), name, model, Price{1, 1, 1}); err != nil {
				t.Fatal(err)
			}
			_, err := e.r.Complete(bg(), Request{Task: "jd-extract", Prompt: "hi"})
			if KindOf(err) != KindUnconfigured || srv.count() != 0 {
				t.Fatalf("missing USD price: calls = %d, err = %v", srv.count(), err)
			}
			if err := e.r.SetPrice(bg(), p.ID, model, Price{1, 1, 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.r.Complete(bg(), Request{Task: "jd-extract", Prompt: "hi"}); err != nil {
				t.Fatal(err)
			}
			if srv.count() != 1 {
				t.Fatalf("calls after price configured = %d", srv.count())
			}
		})
	}
}

func TestLLM_ProviderCatalog_OpenCodeGoSessionAndSubscription(t *testing.T) {
	e := newEnv(t)
	pr, _ := PresetByName("opencode-go")
	model := pr.Models[ClassFast]
	seen := make(chan string, 4)
	srv := newOAServer(t, func(w http.ResponseWriter, req *http.Request, n int) {
		if req.URL.Path != "/chat/completions" || req.UserAgent() != "applysmith/1.0" {
			t.Errorf("OpenCode request path=%s user agent=%s", req.URL.Path, req.UserAgent())
		}
		seen <- req.Header.Get("x-opencode-session")
		okHandler("ok")(w, req, n)
	})
	p := pr.ProviderConfig()
	p.ID, p.BaseURL = "my-go-account", srv.URL
	e.provider(p, fakeKey("go-subscription"))
	e.route("jd-extract", Route{Provider: p.ID, Model: model, Thinking: ThinkOff})
	// Reference-rate overrides must not turn plan usage into a cash charge.
	if err := e.r.SetPrice(bg(), p.ID, model, Price{100, 100, 100}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		resp, err := e.r.Complete(bg(), Request{Task: "jd-extract", Prompt: "hi", RunID: "scan-session"})
		if err != nil || resp.Billing != BillingSubscription || resp.CostUSD == nil || *resp.CostUSD != 0 {
			t.Fatalf("subscription response=%+v err=%v", resp, err)
		}
		if session := <-seen; session != "scan-session" {
			t.Fatalf("session=%q", session)
		}
	}
	e.route(AgentTask, Route{Provider: p.ID, Model: model, Thinking: ThinkMedium})
	if _, err := e.agentCall("agent-session", "turn", piRequest()); err != nil {
		t.Fatal(err)
	}
	if session := <-seen; session != "agent-session" {
		t.Fatalf("gateway session=%q", session)
	}
	if _, err := e.agentCall("", "standalone-turn", piRequest()); err != nil {
		t.Fatal(err)
	}
	if session := <-seen; strings.TrimSpace(session) == "" {
		t.Fatal("gateway standalone call must identify its call ID")
	}
	for _, row := range e.ledger() {
		if row.Billing != BillingSubscription || row.Cost == nil || *row.Cost != 0 {
			t.Fatalf("subscription ledger=%+v", row)
		}
	}
	if spend := e.r.spend(bg(), true).total; spend != 0 {
		t.Fatalf("plan allowance is not metered cash spend: %v", spend)
	}
	if _, metered := e.r.pairCost(bg(), e.load(), p, model); metered {
		t.Fatal("subscription allowance must not become a metered downgrade comparison")
	}
}

func TestLLM_ProviderCatalog_OpenCodeGoSingleCallGetsSession(t *testing.T) {
	e := newEnv(t)
	pr, _ := PresetByName("opencode-go")
	srv := newOAServer(t, func(w http.ResponseWriter, req *http.Request, n int) {
		if session := req.Header.Get("x-opencode-session"); strings.TrimSpace(session) == "" {
			t.Error("standalone call must have a non-empty stable call ID")
		}
		okHandler("ok")(w, req, n)
	})
	p := pr.ProviderConfig()
	p.BaseURL = srv.URL
	e.provider(p, fakeKey("go-single"))
	e.route("jd-extract", Route{Provider: p.ID, Model: pr.Models[ClassFast], Thinking: ThinkOff})
	if _, err := e.r.Complete(bg(), Request{Task: "jd-extract", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
}

// Kimi Code's terms require the client's real identity, and its calls use the
// member's plan allowance. A local fake server stands in for the endpoint.
func TestLLM_ProviderCatalog_KimiCodeIdentifiesItselfAndBillsThePlan(t *testing.T) {
	e := newEnv(t)
	pr, _ := PresetByName("kimi-code")
	model := pr.Models[ClassFast]
	agents := make(chan string, 4)
	srv := newOAServer(t, func(w http.ResponseWriter, req *http.Request, n int) {
		agents <- req.UserAgent()
		okHandler("ok")(w, req, n)
	})
	p := pr.ProviderConfig()
	p.BaseURL = srv.URL
	e.provider(p, fakeKey("kimi-member"))
	e.route("jd-extract", Route{Provider: p.ID, Model: model, Thinking: ThinkOff})
	resp, err := e.r.Complete(bg(), Request{Task: "jd-extract", Prompt: "hi"})
	if err != nil || resp.Billing != BillingSubscription || resp.CostUSD == nil || *resp.CostUSD != 0 {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	if ua := <-agents; ua != "applysmith/1.0" {
		t.Fatalf("task call user agent = %q", ua)
	}
	e.route(AgentTask, Route{Provider: p.ID, Model: model, Thinking: ThinkMedium})
	if _, err := e.agentCall("agent-session", "turn", piRequest()); err != nil {
		t.Fatal(err)
	}
	if ua := <-agents; ua != "applysmith/1.0" {
		t.Fatalf("agent call user agent = %q", ua)
	}
}
