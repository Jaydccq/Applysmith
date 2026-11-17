package api

import (
	"slices"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// testSecret is built at run time so no credential-shaped literal is in the source.
var testSecret = "sk" + "-" + strings.Repeat("ab12", 8)

func taskRow(t *testing.T, view wire.LLMRouting, task string) wire.LLMTaskRoute {
	t.Helper()
	for _, row := range view.Tasks {
		if row.Task == task {
			return row
		}
	}
	t.Fatalf("task %s is not listed", task)
	return wire.LLMTaskRoute{}
}

func routing(t *testing.T, r result) wire.LLMRouting {
	t.Helper()
	var env wire.Success[wire.LLMRouting]
	strictInto(t, r.body, &env)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	return env.Data
}

func (h *harness) configurePreset(name string) wire.LLMRouting {
	h.t.Helper()
	if k := h.send("PUT", "/api/llm/keys/"+name, map[string]any{"key": testSecret}); k.StatusCode != 200 {
		h.t.Fatalf("key: %d %s", k.StatusCode, k.body)
	}
	return routing(h.t, h.post("/api/llm/preset", map[string]any{"name": name}))
}

func TestRoutingListsEveryTaskWithItsReasonBeforeSetup(t *testing.T) {
	h := newRoutingHarness(t)
	view := routing(t, h.get("/api/llm/routing"))
	if len(view.Tasks) != len(llm.Tasks()) || view.Preset != "" || len(view.Providers) != 0 {
		t.Fatalf("view: %+v", view)
	}
	if row := taskRow(t, view, "deep-eval"); row.Provider != "" || row.Unrouted == "" || row.Group != "evaluation" || row.Class != "deep" || !strings.HasPrefix(row.Purpose, "Scores a posting") {
		t.Errorf("deep-eval before setup: %+v", row)
	}
	if len(view.Presets) != len(llm.Presets()) {
		t.Errorf("%d presets listed", len(view.Presets))
	}
	if view.Rests == nil || len(view.Rests) != 0 {
		t.Errorf("rests = %#v, want an empty list", view.Rests)
	}
	for _, p := range view.Presets {
		if p.KeyConfigured {
			t.Errorf("preset %s claims a key", p.Name)
		}
	}
}

func TestLLMComponentReflectsConfiguredRoutingWithoutProviderCalls(t *testing.T) {
	h := newRoutingHarness(t)
	h.srv.setComponents()
	if got := componentStatus(t, h, "llm"); got != wire.ComponentUnconfigured {
		t.Fatalf("no routes: %s", got)
	}
	// Health reads the routing now: the provider just chosen shows without
	// waiting for the cached components to be taken again.
	h.configurePreset("openrouter")
	if got := string(jsonBody(h.get("/api/health").data()["components"])); !strings.Contains(got, `"name":"llm","status":"ok"`) {
		t.Fatalf("configured routes: %s", got)
	}
	if h.rowCount("llm_ledger") != 0 {
		t.Fatal("configuration health called a provider")
	}
	simulated := newDomainHarness(t)
	simulated.srv.setComponents()
	if got := componentStatus(t, simulated, "llm"); got != wire.ComponentDegraded {
		t.Fatalf("offline simulation: %s", got)
	}
}

func TestPresetNeedsAKeyThenRoutesEveryTask(t *testing.T) {
	h := newRoutingHarness(t)
	refused := h.post("/api/llm/preset", map[string]any{"name": "openrouter"})
	if refused.StatusCode != 422 || !strings.Contains(refused.json()["error"].(map[string]any)["message"].(string), "needs an API key") {
		t.Fatalf("preset without a key: %d %s", refused.StatusCode, refused.body)
	}
	view := h.configurePreset("openrouter")
	if view.Preset != "openrouter" || len(view.Providers) != 1 || !view.Providers[0].KeyConfigured || view.Providers[0].Kind != llm.KindOpenAI {
		t.Fatalf("view: %+v", view)
	}
	if row := taskRow(t, view, "quick-screen"); row.Provider != "openrouter" || row.Model == "" || row.Source != "preset" {
		t.Errorf("quick-screen: %+v", row)
	}
	var keyed bool
	for _, p := range view.Presets {
		keyed = keyed || (p.Name == "openrouter" && p.KeyConfigured)
	}
	if !keyed {
		t.Error("the preset list does not show the stored key")
	}
	for name, body := range map[string]map[string]any{"unknown preset": {"name": "nowhere"}, "unknown fallback": {"name": "openrouter", "fallback": "nowhere"}} {
		if r := h.post("/api/llm/preset", body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
}

func TestKeysAreStoredAndClearedWithoutBeingEchoed(t *testing.T) {
	h := newRoutingHarness(t)
	put := h.send("PUT", "/api/llm/keys/deepseek", map[string]any{"key": testSecret})
	var env wire.Success[wire.LLMKeyStatus]
	strictInto(t, put.body, &env)
	if put.StatusCode != 200 || env.Data.Provider != "deepseek" || !env.Data.KeyConfigured || strings.Contains(string(put.body), testSecret) {
		t.Fatalf("put: %d %s", put.StatusCode, put.body)
	}
	if strings.Contains(string(h.get("/api/llm/routing").body), testSecret) {
		t.Error("the routing view echoed the key")
	}
	if strings.Contains(h.logText(), testSecret) {
		t.Error("the key reached the server log")
	}
	del := h.send("DELETE", "/api/llm/keys/deepseek", nil)
	if del.StatusCode != 200 || del.data()["keyConfigured"] != false {
		t.Errorf("clear: %d %s", del.StatusCode, del.body)
	}
	if again := h.send("DELETE", "/api/llm/keys/deepseek", nil); again.StatusCode != 404 {
		t.Errorf("clear twice: %d %s", again.StatusCode, again.body)
	}
	for name, c := range map[string]struct {
		path string
		body map[string]any
		want int
	}{
		"unknown provider": {"/api/llm/keys/nowhere", map[string]any{"key": "x"}, 404},
		"cli takes no key": {"/api/llm/keys/codex", map[string]any{"key": "x"}, 409},
		"empty key":        {"/api/llm/keys/deepseek", map[string]any{"key": ""}, 422},
		"placeholder key":  {"/api/llm/keys/deepseek", map[string]any{"key": "<your-api-key-here>"}, 422},
		"unknown field":    {"/api/llm/keys/deepseek", map[string]any{"key": "x", "value": "y"}, 422},
	} {
		if r := h.send("PUT", c.path, c.body); r.StatusCode != c.want {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
}

func TestKeyRoutesAreAdminAndSecret(t *testing.T) {
	for _, r := range Routes() {
		if strings.HasPrefix(r.Path, "/api/llm/keys/") && (r.Access != AccessAdmin || !r.Secret || r.Remote != RemoteDenied) {
			t.Errorf("%s: access %s secret %v remote %s", r.Pattern(), r.Access, r.Secret, r.Remote)
		}
	}
}

func TestRoutingPatchReplacesRowsAtomically(t *testing.T) {
	h := newRoutingHarness(t)
	h.configurePreset("openrouter")
	row := map[string]any{"provider": "openrouter", "model": "openai/gpt-5-mini", "thinking": "low", "maxOutputTokens": 3000, "timeoutSeconds": 45,
		"firstTokenSeconds": 12, "fallbacks": []map[string]any{}}
	ok := h.send("PATCH", "/api/llm/routing", map[string]any{"routes": map[string]any{"quick-screen": row}})
	view := routing(t, ok)
	got := taskRow(t, view, "quick-screen")
	if got.Source != "table" || got.Model != "openai/gpt-5-mini" || got.MaxOutputTokens != 3000 || got.TimeoutSeconds != 45 || got.Thinking != "low" || got.FirstTokenSeconds != 12 {
		t.Fatalf("patched row: %+v", got)
	}
	// The picker is offered the configured provider's models, the patched one included.
	var offered []string
	for _, k := range view.KnownModels {
		if k.Provider != "openrouter" {
			t.Errorf("a model of %s is offered although it is not configured", k.Provider)
		}
		offered = append(offered, k.Model)
	}
	if !slices.Contains(offered, "openai/gpt-6.1-sol") || !slices.Contains(offered, "openai/gpt-5-mini") || slices.Contains(offered, "openai/text-embedding-3-small") {
		t.Errorf("known models = %v", offered)
	}
	// A deadline that is not shorter than the timeout is refused with the field's message.
	late := map[string]any{"provider": "openrouter", "model": "m", "timeoutSeconds": 10, "firstTokenSeconds": 10}
	if r := h.send("PATCH", "/api/llm/routing", map[string]any{"routes": map[string]any{"quick-screen": late}}); r.StatusCode != 422 || !strings.Contains(string(r.body), "first_token_seconds") {
		t.Fatalf("late first-token deadline: %d %s", r.StatusCode, r.body)
	}
	// One valid row and one invalid row: the answer is an error and neither changes.
	bad := h.send("PATCH", "/api/llm/routing", map[string]any{"routes": map[string]any{
		"narrative":    map[string]any{"provider": "openrouter", "model": "openai/gpt-5"},
		"deep-eval":    map[string]any{"provider": "nowhere", "model": "m"},
		"quick-screen": nil,
	}})
	if bad.StatusCode != 422 || bad.errCode() != "validation_failed" {
		t.Fatalf("invalid patch: %d %s", bad.StatusCode, bad.body)
	}
	after := routing(t, h.get("/api/llm/routing"))
	if r := taskRow(t, after, "narrative"); r.Source != "preset" {
		t.Errorf("narrative changed by a rejected patch: %+v", r)
	}
	if r := taskRow(t, after, "quick-screen"); r.Source != "table" {
		t.Errorf("quick-screen cleared by a rejected patch: %+v", r)
	}
	// Null clears a row back to the preset.
	cleared := routing(t, h.send("PATCH", "/api/llm/routing", map[string]any{"routes": map[string]any{"quick-screen": nil}}))
	if r := taskRow(t, cleared, "quick-screen"); r.Source != "preset" {
		t.Errorf("cleared row: %+v", r)
	}
	for name, body := range map[string]map[string]any{
		"unknown task": {"routes": map[string]any{"no-such-task": map[string]any{"provider": "openrouter", "model": "m"}}},
		"negative cap": {"dailyUsdCap": -1},
		"budget bound": {"budgets": map[string]any{"narrative": llm.MaxTaskBudgetUSD + 1}},
	} {
		if r := h.send("PATCH", "/api/llm/routing", body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
}

func TestDailyCapAndTaskBudgetsAreEditable(t *testing.T) {
	h := newRoutingHarness(t)
	if view := routing(t, h.get("/api/llm/routing")); view.DailyUSDCap != llm.DefaultDailyCapUSD {
		t.Fatalf("default cap %v", view.DailyUSDCap)
	}
	if view := routing(t, h.send("PATCH", "/api/llm/routing", map[string]any{"dailyUsdCap": 5.5})); view.DailyUSDCap != 5.5 {
		t.Errorf("cap after patch %v", view.DailyUSDCap)
	}
	if view := routing(t, h.send("PATCH", "/api/llm/routing", map[string]any{"dailyUsdCap": 0})); view.DailyUSDCap != 0 {
		t.Errorf("cap 0 disables the cap, got %v", view.DailyUSDCap)
	}
	for _, usd := range []float64{0.75, 0} {
		view := routing(t, h.send("PATCH", "/api/llm/routing", map[string]any{"budgets": map[string]any{"narrative": usd}}))
		if r := taskRow(t, view, "narrative"); r.BudgetUSD != usd || !r.Budgetable {
			t.Errorf("budget %v: row %+v", usd, r)
		}
	}
}

func TestCostReportCountsTheCallsOfAnEvaluation(t *testing.T) {
	h := newDomainHarness(t)
	h.evaluate("cost")
	r := h.get("/api/llm/cost?days=7")
	var env wire.Success[wire.LLMCost]
	strictInto(t, r.body, &env)
	var deep int
	for _, row := range env.Data.ByTask {
		if row.Name == "deep-eval" {
			deep = int(row.Calls)
		}
	}
	if r.StatusCode != 200 || env.Data.Days != 7 || deep != 1 || len(env.Data.ByProvider) == 0 {
		t.Fatalf("cost: %s", r.body)
	}
	for _, bad := range []string{"?days=91"} {
		if r := h.get("/api/llm/cost" + bad); r.StatusCode != 422 {
			t.Errorf("%s: %d", bad, r.StatusCode)
		}
	}
}

func TestKeyStatusInTheRoutingViewFollowsKeysStoredAndClearedHere(t *testing.T) {
	h := newRoutingHarness(t)
	has := func() bool {
		for _, p := range routing(t, h.get("/api/llm/routing")).Presets {
			if p.Name == "deepseek" {
				return p.KeyConfigured
			}
		}
		t.Fatal("deepseek is not listed")
		return false
	}
	if has() {
		t.Fatal("a key before any is stored")
	}
	h.send("PUT", "/api/llm/keys/deepseek", map[string]any{"key": testSecret})
	if !has() {
		t.Error("the view kept the cached answer after the key was stored")
	}
	h.send("DELETE", "/api/llm/keys/deepseek", nil)
	if has() {
		t.Error("the view kept the cached answer after the key was cleared")
	}
}
