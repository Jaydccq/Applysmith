package llm

import (
	"context"
	"database/sql"
	"math"
)

// Billing classes of a ledger row.
const (
	BillingMetered      = "metered"
	BillingSubscription = "subscription"
	BillingFree         = "free"
	BillingUnpriced     = "unpriced"
)

// Embedded prices in USD per million tokens, keyed "<preset>/<model>". Models
// absent here are unpriced and are never given a guessed cost; `applysmith llm
// prices update` refreshes the OpenRouter rows and a user price wins over all.
// DeepSeek rows carry the peak rate and Gemini rows the rate in force until the
// end of 2026; docs/architecture/llm.md records every source and date.
var embeddedPrices = map[string]Price{
	"openai/gpt-6-luna":                        {0.10, 0.01, 0.50},
	"openai/gpt-6.1-sol":                       {2, 0.10, 10},
	"openai/text-embedding-3-small":            {0.02, 0.02, 0},
	"openrouter/openai/gpt-6-luna":             {0.10, 0.01, 0.50},
	"openrouter/openai/gpt-6.1-sol":            {2, 0.10, 10},
	"openrouter/openai/text-embedding-3-small": {0.02, 0.02, 0},
	"anthropic/claude-haiku-5-5":               {0.10, 0.01, 0.50},
	"anthropic/claude-sonnet-5-5":              {2, 0.10, 10},
	"anthropic/claude-opus-5-5":                {4, 0.20, 20},
	"deepseek/deepseek-flash":                  {0.30, 0.006, 1.20},
	"deepseek/deepseek-v4-pro":                 {1.32, 0.044, 3.96},
	"gemini/gemini-3.5-flash-lite":             {0.30, 0.03, 2.50},
	"gemini/gemini-3.8-flash":                  {0.75, 0.075, 3.75},
	"gemini/gemini-embedding-001":              {0.15, 0.15, 0},
	// OpenCode Go rates value included allowance; costing records subscription/$0.
	"opencode-go/glm-5.3-flash":  {0.15, 0.03, 0.50},
	"opencode-go/glm-5.3":        {1.4, 0.26, 4.4},
	"minimax/MiniMax-M3.1-Flash": {0.15, 0.015, 0.60},
	"zai/glm-5.3-flash":          {0.15, 0.03, 0.50},
	"zai/glm-5.3":                {1.4, 0.26, 4.4},
	"xai/grok-4.7":               {2, 0.50, 6},
}

// priceKey is the key of a model in the embedded and refreshed tables: the
// preset namespace of the provider, or its id for a custom provider.
func priceKey(p ProviderConfig, model string) string {
	ns := p.Preset
	if ns == "" {
		ns = p.ID
	}
	return ns + "/" + model
}

// priceFor finds the price of a model: a user price, then a refreshed price,
// then the embedded table.
func (r *Router) priceFor(ctx context.Context, s Settings, p ProviderConfig, model string) (Price, bool) {
	if up, ok := s.Prices[p.ID+"/"+model]; ok {
		return up, true
	}
	key := priceKey(p, model)
	var pr Price
	err := r.store.Read().QueryRowContext(ctx, `SELECT input_per_m, cached_per_m, output_per_m FROM llm_prices WHERE key = ?`, key).
		Scan(&pr.InputPerM, &pr.CachedInputPerM, &pr.OutputPerM)
	if err == nil {
		return pr, true
	}
	if err != sql.ErrNoRows {
		r.warnOnce("prices-read", "refreshed prices unreadable: "+err.Error())
	}
	pr, ok := embeddedPrices[key]
	return pr, ok
}

// cacheWriteFactor is the price of a prompt-cache write relative to plain
// input (the five-minute cache of the Messages API).
const cacheWriteFactor = 1.25

// Cost computes the cost of a usage. Cached input and cache-write tokens are
// subsets of the input total; they are priced at the cached rate and at
// cacheWriteFactor times the input rate. The result is rounded to
// micro-dollars.
func (pr Price) Cost(u Usage) float64 {
	cached := min(max(u.CachedInput, 0), u.Input)
	written := min(max(u.CacheWrite, 0), u.Input-cached)
	c := float64(u.Input-cached-written)*pr.InputPerM + float64(cached)*pr.CachedInputPerM +
		float64(written)*pr.InputPerM*cacheWriteFactor + float64(u.Output)*pr.OutputPerM
	return math.Round(c) / 1e6
}

// roundMicro rounds to six decimals.
func roundMicro(x float64) float64 { return math.Round(x*1e6) / 1e6 }

// Costing is the billing class and cost of one attempt. Cost is nil when the
// model is unpriced.
type Costing struct {
	Billing string
	Cost    *float64
}

// The list price of the provider's web search, billed apart from the tokens
// (OpenRouter's web plugin on the exa engine, auto mode): one request with up
// to ten results, and each result beyond those.
const (
	searchUSDPerRequest     = 0.007
	searchIncludedResults   = 10
	searchUSDPerExtraResult = 0.001
)

// searchFee is the fee of one search that asked for this many results.
func searchFee(results int) float64 {
	return searchUSDPerRequest + float64(max(0, results-searchIncludedResults))*searchUSDPerExtraResult
}

// withSearch adds the search fee of a call that asked for results. An unpriced
// model stays unpriced: its token cost, and so the total, is unknown.
func (c Costing) withSearch(results int) Costing {
	if results <= 0 || c.Cost == nil || c.Billing == BillingSubscription {
		return c
	}
	total := roundMicro(*c.Cost + searchFee(results))
	return Costing{Billing: BillingMetered, Cost: &total}
}

func (r *Router) costing(ctx context.Context, s Settings, p ProviderConfig, model string, u Usage) Costing {
	zero := 0.0
	switch {
	case subscriptionProvider(p):
		return Costing{BillingSubscription, &zero}
	case p.Kind == KindFake, isFreeModel(model):
		return Costing{BillingFree, &zero}
	}
	pr, ok := r.priceFor(ctx, s, p, model)
	if !ok {
		return Costing{Billing: BillingUnpriced}
	}
	c := pr.Cost(u)
	return Costing{BillingMetered, &c}
}

// pairCost is the comparison price of a model: one million input plus one
// million output tokens. ok is false for a model without a metered price.
func (r *Router) pairCost(ctx context.Context, s Settings, p ProviderConfig, model string) (float64, bool) {
	if subscriptionProvider(p) || p.Kind == KindFake || isFreeModel(model) {
		return 0, false
	}
	pr, ok := r.priceFor(ctx, s, p, model)
	return pr.InputPerM + pr.OutputPerM, ok
}

// subscriptionProvider includes API transports whose preset uses plan allowance.
func subscriptionProvider(p ProviderConfig) bool {
	pr, _ := PresetByName(p.Preset)
	return p.Kind == KindCLI || p.Kind == KindChatGPT || pr.UsesPlan()
}
