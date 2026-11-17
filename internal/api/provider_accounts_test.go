package api

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/vault"
)

func TestProviderAccounts_AddIndependentKeysWithoutChangingRoutes(t *testing.T) {
	h := newRoutingHarness(t)
	before := h.configurePreset("openrouter")
	for _, id := range []string{"deepseek-personal", "deepseek-work"} {
		view := routing(t, h.send("PUT", "/api/llm/providers/"+id, map[string]any{"preset": "deepseek"}))
		p := view.Providers[len(view.Providers)-1]
		if p.ID != id || p.Kind != llm.KindOpenAI || p.Preset != "deepseek" || p.KeyConfigured {
			t.Fatalf("provider = %+v", p)
		}
		if view.Preset != before.Preset || !reflect.DeepEqual(view.Tasks, before.Tasks) {
			t.Fatal("adding an account changed existing routing")
		}
		key := testSecret + id
		if r := h.send("PUT", "/api/llm/keys/"+id, map[string]any{"key": key}); r.StatusCode != 200 {
			t.Fatalf("save key: %d %s", r.StatusCode, r.body)
		}
	}
	view := routing(t, h.get("/api/llm/routing"))
	if len(view.Providers) != 3 || !view.Providers[1].KeyConfigured || !view.Providers[2].KeyConfigured {
		t.Fatalf("providers = %+v", view.Providers)
	}
	if strings.Contains(string(h.get("/api/llm/routing").body), testSecret) || strings.Contains(h.logText(), testSecret) {
		t.Fatal("a key reached the view or log")
	}
	view = routing(t, h.send("DELETE", "/api/llm/providers/deepseek-work", nil))
	if len(view.Providers) != 2 || h.vault.Has(llm.KeyName("deepseek-work")) || !h.vault.Has(llm.KeyName("deepseek-personal")) {
		t.Fatal("deleting an account did not remove only its key and config")
	}
	if r := h.send("DELETE", "/api/llm/providers/deepseek-work", nil); r.StatusCode != 404 {
		t.Fatalf("delete twice: %d %s", r.StatusCode, r.body)
	}
}

func TestProviderAccounts_CustomAndInvalidInput(t *testing.T) {
	h := newRoutingHarness(t)
	view := routing(t, h.send("PUT", "/api/llm/providers/private", map[string]any{"kind": llm.KindOpenAI, "baseUrl": "http://127.0.0.1:9000/v1", "structuredOutput": true, "embeddings": true}))
	if p := view.Providers[0]; p.ID != "private" || !p.StructuredOutput || !p.Embeddings {
		t.Fatalf("provider = %+v", p)
	}
	for name, body := range map[string]map[string]any{
		"unknown preset":        {"preset": "missing"},
		"program override":      {"preset": "deepseek", "program": "claude"},
		"custom program":        {"kind": llm.KindCLI, "program": "anything"},
		"remote plaintext":      {"kind": llm.KindOpenAI, "baseUrl": "http://example.com/v1"},
		"userinfo":              {"kind": llm.KindOpenAI, "baseUrl": "https://name:secret@example.com/v1"},
		"query":                 {"kind": llm.KindOpenAI, "baseUrl": "https://example.com/v1?api_key=secret"},
		"credentials":           {"preset": "deepseek", "key": testSecret},
		"false managed account": {"preset": "codex"},
	} {
		t.Run(name, func(t *testing.T) {
			r := h.send("PUT", "/api/llm/providers/invalid", body)
			if r.StatusCode != 422 {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if strings.Contains(string(r.body), "name:secret") || strings.Contains(string(r.body), "api_key=secret") {
				t.Fatal("the invalid endpoint exposed a credential")
			}
		})
	}
	for _, id := range []string{"Bad-ID", "group"} {
		if r := h.send("PUT", "/api/llm/providers/"+id, map[string]any{"preset": "deepseek"}); r.StatusCode != 422 {
			t.Fatalf("invalid id %q: %d %s", id, r.StatusCode, r.body)
		}
	}
	routing(t, h.send("PUT", "/api/llm/providers/codex", map[string]any{"preset": "codex"}))
	if got := len(routing(t, h.get("/api/llm/routing")).Providers); got != 2 {
		t.Fatalf("invalid requests added providers: %d", got)
	}
}

func TestProviderAccounts_DeletionRefusesEveryReference(t *testing.T) {
	for _, use := range []string{"preset", "preset fallback", "task", "fallback", "group"} {
		t.Run(use, func(t *testing.T) {
			h := newRoutingHarness(t)
			routing(t, h.send("PUT", "/api/llm/providers/deepseek", map[string]any{"preset": "deepseek"}))
			if r := h.send("PUT", "/api/llm/keys/deepseek", map[string]any{"key": testSecret}); r.StatusCode != 200 {
				t.Fatal(string(r.body))
			}
			st, _ := h.srv.db()
			if err := llm.UpdateSettings(context.Background(), st, h.clock.now(), func(s *llm.Settings) error {
				switch use {
				case "preset":
					s.Preset = "deepseek"
				case "preset fallback":
					s.PresetFallback = "deepseek"
				case "task":
					s.Routes["quick-screen"] = llm.Route{Provider: "deepseek", Model: "deepseek-flash"}
				case "fallback":
					primary, _ := llm.PresetByName("openrouter")
					s.Providers = append(s.Providers, primary.ProviderConfig())
					s.Routes["quick-screen"] = llm.Route{Provider: "openrouter", Model: "openai/gpt-6-luna", Fallbacks: []llm.Fallback{{Provider: "deepseek", Model: "deepseek-flash"}}}
				case "group":
					s.Groups["pool"] = llm.Group{Mode: "order", Members: []llm.Fallback{{Provider: "deepseek", Model: "deepseek-flash"}}}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if r := h.send("DELETE", "/api/llm/providers/deepseek", nil); r.StatusCode != 409 {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if !h.vault.Has(llm.KeyName("deepseek")) {
				t.Fatal("refused delete removed the key")
			}
		})
	}
}

func TestProviderAccounts_EndpointEditRequiresRemovingKey(t *testing.T) {
	h := newRoutingHarness(t)
	routing(t, h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "deepseek"}))
	h.send("PUT", "/api/llm/keys/personal", map[string]any{"key": testSecret})
	if r := h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "deepseek", "baseUrl": "https://example.com/v1"}); r.StatusCode != 409 {
		t.Fatalf("endpoint with key: %d %s", r.StatusCode, r.body)
	}
	h.send("DELETE", "/api/llm/keys/personal", nil)
	view := routing(t, h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "deepseek", "baseUrl": "https://example.com/v1"}))
	if view.Providers[0].BaseURL != "https://example.com/v1" {
		t.Fatal("endpoint did not change")
	}
}

func TestProviderAccounts_KeychainFailurePreservesConfig(t *testing.T) {
	h := newRoutingHarness(t)
	routing(t, h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "deepseek"}))
	h.send("PUT", "/api/llm/keys/personal", map[string]any{"key": testSecret})
	tok := h.token()
	h.mem.FailWith(vault.ErrDenied)
	r := h.sendWith(tok, "DELETE", "/api/llm/providers/personal", nil)
	h.mem.FailWith(nil)
	if r.StatusCode != 503 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if view := routing(t, h.get("/api/llm/routing")); len(view.Providers) != 1 || !view.Providers[0].KeyConfigured {
		t.Fatal("keychain failure removed configuration or key")
	}
}

func TestProviderAccounts_AuthenticationAndRouteClasses(t *testing.T) {
	h := newRoutingHarness(t)
	for _, method := range []string{"PUT", "DELETE"} {
		if r := h.do(call{method: method, path: "/api/llm/providers/personal"}); r.StatusCode != 401 {
			t.Fatalf("anonymous %s: %d %s", method, r.StatusCode, r.body)
		}
	}
	var seen []string
	for _, r := range Routes() {
		if r.Path == "/api/llm/providers/{provider}" {
			seen = append(seen, r.Method)
			if r.Access != AccessAdmin || r.Remote != RemoteDenied || (r.Method == "DELETE" && !r.Secret) {
				t.Fatalf("route = %+v", r)
			}
		}
	}
	if !slices.Contains(seen, "PUT") || !slices.Contains(seen, "DELETE") {
		t.Fatal("routes absent")
	}
}

func TestProviderAccounts_ModelPricesAreExplicitUSD(t *testing.T) {
	h := newRoutingHarness(t)
	routing(t, h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "deepseek"}))
	price := map[string]any{"model": "custom-model", "inputPerM": 1.5, "cachedInputPerM": 0.25, "outputPerM": 3.0}
	view := routing(t, h.send("PUT", "/api/llm/prices/personal", price))
	if !slices.ContainsFunc(view.KnownModels, func(m wire.LLMFallback) bool { return m.Provider == "personal" && m.Model == "custom-model" }) {
		t.Fatal("the priced model was not offered")
	}
	st, _ := h.srv.db()
	settings, err := llm.LoadSettings(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if got := settings.Prices["personal/custom-model"]; got.InputPerM != 1.5 || got.CachedInputPerM != 0.25 || got.OutputPerM != 3 {
		t.Fatalf("price = %+v", got)
	}
	for _, field := range []string{"outputPerM"} {
		bad := map[string]any{"model": "custom-model", "inputPerM": 1.5, "cachedInputPerM": 0.25, "outputPerM": 3.0}
		bad[field] = -1
		if r := h.send("PUT", "/api/llm/prices/personal", bad); r.StatusCode != 422 {
			t.Fatalf("%s: %d %s", field, r.StatusCode, r.body)
		}
	}
	if r := h.send("PUT", "/api/llm/prices/missing", price); r.StatusCode != 404 {
		t.Fatalf("missing: %d %s", r.StatusCode, r.body)
	}
	if r := h.send("PUT", "/api/llm/prices/personal", map[string]any{"model": "bad model"}); r.StatusCode != 422 {
		t.Fatalf("model: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{method: "PUT", path: "/api/llm/prices/personal"}); r.StatusCode != 401 {
		t.Fatalf("anonymous: %d %s", r.StatusCode, r.body)
	}
	routing(t, h.send("DELETE", "/api/llm/providers/personal", nil))
	settings, err = llm.LoadSettings(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Prices["personal/custom-model"]; ok {
		t.Fatal("removal left the account's model price")
	}
}

func TestProviderAccounts_PricesRequireEveryExplicitValue(t *testing.T) {
	h := newRoutingHarness(t)
	routing(t, h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "deepseek"}))
	st, _ := h.srv.db()
	for _, field := range []string{"cachedInputPerM"} {
		for _, shape := range []string{"omitted", "null", "partial"} {
			t.Run(field+" "+shape, func(t *testing.T) {
				body := map[string]any{"model": "glm-5.3", "inputPerM": 1.0, "cachedInputPerM": 0.1, "outputPerM": 2.0}
				switch shape {
				case "omitted":
					delete(body, field)
				case "null":
					body[field] = nil
				case "partial":
					body = map[string]any{"model": "glm-5.3", field: 0.0}
				}
				if r := h.send("PUT", "/api/llm/prices/personal", body); r.StatusCode != 422 {
					t.Fatalf("incomplete price accepted: %d %s", r.StatusCode, r.body)
				}
				settings, err := llm.LoadSettings(context.Background(), st)
				if err != nil {
					t.Fatal(err)
				}
				if len(settings.Prices) != 0 {
					t.Fatalf("rejected price was stored: %+v", settings.Prices)
				}
			})
		}
	}
	if r := h.send("PUT", "/api/llm/prices/personal", map[string]any{"model": "glm-5.3"}); r.StatusCode != 422 {
		t.Fatalf("model only: %d %s", r.StatusCode, r.body)
	}
	settings, err := llm.LoadSettings(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Prices) != 0 {
		t.Fatal("model-only request stored a price")
	}
	routing(t, h.send("PUT", "/api/llm/prices/personal", map[string]any{"model": "glm-5.3", "inputPerM": 0.0, "cachedInputPerM": 0.0, "outputPerM": 0.0}))
	settings, err = llm.LoadSettings(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if price, ok := settings.Prices["personal/glm-5.3"]; !ok || price != (llm.Price{}) {
		t.Fatalf("explicit zero price was not stored: %+v", settings.Prices)
	}
}

func TestProviderAccounts_EditingCapabilitiesPreservesRoutes(t *testing.T) {
	h := newRoutingHarness(t)
	routing(t, h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "openai"}))
	routing(t, h.send("PATCH", "/api/llm/routing", map[string]any{"routes": map[string]any{"embedding": map[string]any{"provider": "personal", "model": "text-embedding-3-small"}}}))
	if r := h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "openai", "embeddings": false}); r.StatusCode != 409 {
		t.Fatalf("capability edit: %d %s", r.StatusCode, r.body)
	}
	if view := routing(t, h.get("/api/llm/routing")); !view.Providers[0].Embeddings || taskRow(t, view, "embedding").Broken != "" {
		t.Fatal("refused edit broke the embedding route")
	}
}

func TestProviderAccounts_EnvironmentKeyCannotBeDeleted(t *testing.T) {
	h := newRoutingHarness(t)
	routing(t, h.send("PUT", "/api/llm/providers/personal", map[string]any{"preset": "deepseek"}))
	h.srv.cfg.Vault = newVaultWithEnv(t, h.ws, h.mem, vault.EnvName(llm.KeyName("personal")), testSecret)
	if r := h.send("DELETE", "/api/llm/providers/personal", nil); r.StatusCode != 409 {
		t.Fatalf("environment key: %d %s", r.StatusCode, r.body)
	}
	if len(routing(t, h.get("/api/llm/routing")).Providers) != 1 {
		t.Fatal("refused deletion removed config")
	}
}
