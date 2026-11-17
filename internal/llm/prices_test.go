package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLM_Price_UnknownModelUnpricedNotGuessed(t *testing.T) {
	e, _ := capEnv(t)
	e.route("narrative", Route{Provider: "a", Model: "model-not-in-table", Thinking: ThinkOff})
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Billing != BillingUnpriced || resp.CostUSD != nil {
		t.Fatalf("resp = %+v err %v", resp, err)
	}
	if rows := e.ledger(); rows[0].Cost != nil || rows[0].Billing != "unpriced" {
		t.Fatalf("ledger = %+v", rows)
	}
	// It does not count toward the cap.
	if spend := e.r.spend(bg(), true).total; spend != 0 {
		t.Fatalf("spend = %v", spend)
	}
}

func TestLLM_Price_CachedInputIsSubset(t *testing.T) {
	p := Price{InputPerM: 1.0, CachedInputPerM: 0.1, OutputPerM: 2.0}
	// 200 tokens at the input rate, 800 at the cached rate.
	got := p.Cost(Usage{Input: 1000, CachedInput: 800})
	want := (200*1.0 + 800*0.1) / 1e6
	if got != roundMicro(want) {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	if over := p.Cost(Usage{Input: 100, CachedInput: 500}); over != roundMicro(100*0.1/1e6) {
		t.Fatalf("cached beyond the input total = %v", over)
	}
}

func TestLLM_Price_RoundsToSixDecimals(t *testing.T) {
	p := Price{InputPerM: 0.1, CachedInputPerM: 0.1, OutputPerM: 0.1}
	if got := p.Cost(Usage{Input: 1, Output: 1}); got != 0.0 {
		t.Fatalf("0.2 micro-dollars rounds to %v", got)
	}
	if got := p.Cost(Usage{Input: 12345}); got != 0.001235 {
		t.Fatalf("cost = %v, want 0.001235", got)
	}
	if got := fmt.Sprint((Price{InputPerM: 0.02}).Cost(Usage{Input: 1000})); got != "2e-05" {
		t.Fatalf("a 0.00002-dollar call must be exact, got %s", got)
	}
}

func TestLLM_Price_BillingClasses(t *testing.T) {
	e := newEnv(t)
	s := e.load()
	if c := e.r.costing(bg(), s, ProviderConfig{ID: "x", Kind: KindCLI}, "sonnet", Usage{Input: 5}); c.Billing != BillingSubscription || *c.Cost != 0 {
		t.Fatalf("cli = %+v", c)
	}
	if c := e.r.costing(bg(), s, ProviderConfig{ID: "x", Kind: KindOpenAI}, "vendor/m:free", Usage{Input: 5}); c.Billing != BillingFree || *c.Cost != 0 {
		t.Fatalf("free = %+v", c)
	}
	if c := e.r.costing(bg(), s, ProviderConfig{ID: "x", Kind: KindFake}, "m", Usage{Input: 5}); c.Billing != BillingFree {
		t.Fatalf("fake = %+v", c)
	}
	p := ProviderConfig{ID: "x", Kind: KindOpenAI, Preset: "openai"}
	if c := e.r.costing(bg(), s, p, "gpt-6.1-sol", Usage{Input: 1000000, Output: 1000000}); c.Billing != BillingMetered || *c.Cost != 12 {
		t.Fatalf("metered = %+v", c)
	}
}

func TestLLM_Price_UserDefinedWins(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "mine", Kind: KindOpenAI, BaseURL: "https://example.com/v1"}, "")
	if err := e.r.SetPrice(bg(), "mine", "custom-model", Price{InputPerM: 4, CachedInputPerM: 1, OutputPerM: 8}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.load().Provider("mine")
	c := e.r.costing(bg(), e.load(), p, "custom-model", Usage{Input: 1000000, Output: 500000})
	if c.Billing != BillingMetered || *c.Cost != 8 {
		t.Fatalf("costing = %+v", c)
	}
	// A user price beats the embedded one for a known model.
	op := ProviderConfig{ID: "a", Kind: KindOpenAI, Preset: "openai", BaseURL: "https://example.com/v1"}
	e.provider(op, "")
	if c := e.r.costing(bg(), e.load(), op, "gpt-6.1-sol", Usage{Input: 1000000}); *c.Cost != 2 {
		t.Fatalf("embedded cost = %v, want 2", *c.Cost)
	}
	if err := e.r.SetPrice(bg(), "a", "gpt-6.1-sol", Price{InputPerM: 100}); err != nil {
		t.Fatal(err)
	}
	if c := e.r.costing(bg(), e.load(), op, "gpt-6.1-sol", Usage{Input: 1000000}); *c.Cost != 100 {
		t.Fatalf("cost = %v", *c.Cost)
	}
}

func catalogServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

const catalogJSON = `{"data":[
 {"id":"openai/gpt-6.1-sol","pricing":{"prompt":"0.000003","completion":"0.00001","input_cache_read":"0.0000002"}},
 {"id":"openai/gpt-6-luna","pricing":{"prompt":"0.0000001","completion":"0.0000005"}},
 {"id":"vendor/unrelated","pricing":{"prompt":"0.1","completion":"0.2"}}]}`

func TestLLM_Price_UpdateChangesAPrice(t *testing.T) {
	srv := catalogServer(t, catalogJSON, 200)
	e := newEnv(t, WithCatalogURL(srv.URL))
	changes, err := e.r.UpdatePrices(bg())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Key != "openrouter/openai/gpt-6.1-sol" || changes[0].Old.InputPerM != 2 || changes[0].New.InputPerM != 3 || changes[0].New.CachedInputPerM != 0.2 {
		t.Fatalf("changes = %+v", changes)
	}
	var source string
	if err := e.st.Read().QueryRow(`SELECT source FROM llm_prices WHERE key = 'openrouter/openai/gpt-6.1-sol'`).Scan(&source); err != nil || source != srv.URL {
		t.Fatalf("stored source %q err %v", source, err)
	}
	// The refreshed price is the one in use.
	p := ProviderConfig{ID: "or", Kind: KindOpenAI, Preset: "openrouter"}
	if c := e.r.costing(bg(), e.load(), p, "openai/gpt-6.1-sol", Usage{Input: 1000000}); *c.Cost != 3 {
		t.Fatalf("cost = %v", *c.Cost)
	}
	if again, _ := e.r.UpdatePrices(bg()); len(again) != 0 {
		t.Fatalf("an unchanged catalog reported changes: %+v", again)
	}
	if _, ok := e.load().Prices["unrelated"]; ok {
		t.Fatal("an unrouted catalog id was stored")
	}
}

func TestLLM_PriceCheck_DriftAndFetchFailure(t *testing.T) {
	srv := catalogServer(t, catalogJSON, 200)
	e := newEnv(t, WithCatalogURL(srv.URL))
	drift, err := e.r.CheckPriceDrift(bg())
	if err != nil {
		t.Fatal(err)
	}
	var priceDrift, missing int
	for _, d := range drift {
		if d.Missing {
			missing++
		} else {
			priceDrift++
		}
	}
	if priceDrift != 1 || missing != 1 { // gpt-6.1-sol input differs; text-embedding-3-small is not listed
		t.Fatalf("drift = %+v", drift)
	}
	var n int
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_price_checks`).Scan(&n)
	if n != 1 {
		t.Fatalf("recorded checks = %d", n)
	}
	bad := catalogServer(t, ``, 500)
	e2 := newEnv(t, WithCatalogURL(bad.URL))
	if _, err := e2.r.CheckPriceDrift(bg()); err == nil {
		t.Fatal("a failed fetch must be an error")
	}
}

func TestLLM_Watch_GoneModelReportedRowUnchanged(t *testing.T) {
	srv := catalogServer(t, `{"data":[{"id":"openai/gpt-5","pricing":{"prompt":"0.000001","completion":"0.00001"}}]}`, 200)
	e := newEnv(t, WithCatalogURL(srv.URL))
	e.provider(ProviderConfig{ID: "or", Kind: KindOpenAI, Preset: "openrouter", Embeddings: true, BaseURL: "https://openrouter.ai/api/v1"}, "")
	e.route("narrative", Route{Provider: "or", Model: "retired/model", Thinking: ThinkOff, Fallbacks: []Fallback{{"or", "openai/gpt-5"}}})
	e.route("embedding", Route{Provider: "or", Model: "gone/embed"})
	before := e.load().Routes
	gone, err := e.r.RoutedModelsMissing(bg())
	if err != nil || len(gone) != 1 || gone[0] != (GoneModel{"narrative", "retired/model"}) {
		t.Fatalf("gone = %+v err %v", gone, err)
	}
	if after := e.load().Routes; len(after) != len(before) || after["narrative"].Model != "retired/model" {
		t.Fatal("the check changed a row")
	}
}

func TestLLM_Watch_CatalogUnreachableReportsNothing(t *testing.T) {
	srv := catalogServer(t, ``, 503)
	e := newEnv(t, WithCatalogURL(srv.URL))
	e.provider(ProviderConfig{ID: "or", Kind: KindOpenAI, Preset: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, "")
	e.route("narrative", Route{Provider: "or", Model: "x/y", Thinking: ThinkOff})
	gone, err := e.r.RoutedModelsMissing(bg())
	if err == nil || len(gone) != 0 {
		t.Fatalf("gone %v err %v", gone, err)
	}
}

// Every USD-priced metered preset model has an embedded price and every
// embedded price belongs to a preset model. CNY-only presets require an
// instance-specific user USD price before a call, enforced by the walk. A
// subscription preset is billed $0, so a reference rate for it is optional.
func TestLLM_Price_EmbeddedTableMatchesPresets(t *testing.T) {
	want, optional := map[string]bool{}, map[string]bool{}
	for _, p := range presets {
		if p.Kind == KindCLI || p.Kind == KindChatGPT || p.Name == "nvidia" || p.RequireUserPrice {
			continue
		}
		if p.Subscription {
			for _, m := range p.Models {
				optional[p.Name+"/"+m] = true
			}
			continue
		}
		models := []string{p.Embedding}
		for _, m := range p.Models {
			models = append(models, m)
		}
		for _, m := range models {
			if m != "" {
				want[p.Name+"/"+m] = true
			}
		}
	}
	for k := range want {
		if _, ok := embeddedPrices[k]; !ok {
			t.Errorf("preset model %s has no embedded price", k)
		}
	}
	for k := range embeddedPrices {
		if !want[k] && !optional[k] {
			t.Errorf("embedded price %s belongs to no preset model", k)
		}
	}
}
