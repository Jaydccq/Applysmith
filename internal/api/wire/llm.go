package wire

// LLMProvider is one configured provider. It carries no secret; KeyConfigured
// says whether a key is stored or supplied.
type LLMProvider struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	BaseURL          string `json:"baseUrl,omitempty"`
	Preset           string `json:"preset,omitempty"`
	Program          string `json:"program,omitempty"`
	StructuredOutput bool   `json:"structuredOutput"`
	Embeddings       bool   `json:"embeddings"`
	KeyConfigured    bool   `json:"keyConfigured"`
}

// LLMFallback names a provider and model tried after the primary.
type LLMFallback struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// LLMRoute is one routing row as stored: a PATCH replaces it in full.
type LLMRoute struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	Thinking        string  `json:"thinking,omitempty"`
	MaxOutputTokens SafeInt `json:"maxOutputTokens,omitempty"`
	TimeoutSeconds  SafeInt `json:"timeoutSeconds,omitempty"`
	// FirstTokenSeconds abandons a streaming attempt whose first stream event
	// is later than this, without resting the model; 0 is off.
	FirstTokenSeconds SafeInt       `json:"firstTokenSeconds,omitempty"`
	Fallbacks         []LLMFallback `json:"fallbacks,omitempty"`
}

// LLMGroup is a routing group: a task row names it as provider "group" and the
// group's name as its model. Mode is order, rotate or usage; a PATCH replaces
// the group in full.
type LLMGroup struct {
	Mode    string        `json:"mode"`
	Members []LLMFallback `json:"members"`
}

// LLMBenchResult is one candidate's score in a bench: how many cases it
// answered correctly, wrongly or not at all, how fast, and at what cost.
type LLMBenchResult struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Correct  SafeInt  `json:"correct"`
	Wrong    SafeInt  `json:"wrong"`
	NoAnswer SafeInt  `json:"noAnswer"`
	Errors   SafeInt  `json:"errors"`
	P50Ms    SafeInt  `json:"p50Ms"`
	P95Ms    SafeInt  `json:"p95Ms"`
	CostUSD  *float64 `json:"costUsd,omitempty"`
}

// LLMBench is the latest bench of a task, best candidate first. It is made by
// `applysmith llm bench`.
type LLMBench struct {
	At      Time             `json:"at"`
	Cases   SafeInt          `json:"cases"`
	Results []LLMBenchResult `json:"results"`
}

// LLMTaskRoute is the effective routing of one task and where it came from.
// Unrouted and Broken explain a task that has no usable route.
type LLMTaskRoute struct {
	Task              string        `json:"task"`
	Group             string        `json:"group"`
	Class             string        `json:"class"`
	Purpose           string        `json:"purpose,omitempty"`
	Provider          string        `json:"provider,omitempty"`
	Model             string        `json:"model,omitempty"`
	Thinking          string        `json:"thinking,omitempty"`
	MaxOutputTokens   SafeInt       `json:"maxOutputTokens,omitempty"`
	TimeoutSeconds    SafeInt       `json:"timeoutSeconds,omitempty"`
	FirstTokenSeconds SafeInt       `json:"firstTokenSeconds,omitempty"`
	Fallbacks         []LLMFallback `json:"fallbacks,omitempty"`
	Source            string        `json:"source,omitempty"`
	Bench             *LLMBench     `json:"bench,omitempty"`
	Unrouted          string        `json:"unrouted,omitempty"`
	Broken            string        `json:"broken,omitempty"`
	// BudgetUSD is the task's daily budget, 0 for none, and SpentTodayUSD the
	// metered spend of the local day it is held to; BudgetReached says the task
	// runs on the cheapest model for the rest of the day. Budgetable is false
	// for a task the spend limits never move to a cheaper model.
	BudgetUSD     float64 `json:"budgetUsd,omitempty"`
	SpentTodayUSD float64 `json:"spentTodayUsd,omitempty"`
	BudgetReached bool    `json:"budgetReached,omitempty"`
	Budgetable    bool    `json:"budgetable,omitempty"`
}

// LLMPreset is a shipped one-key or command-line preset.
type LLMPreset struct {
	Name          string `json:"name"`
	Label         string `json:"label"`
	Kind          string `json:"kind"`
	KeyURL        string `json:"keyUrl,omitempty"`
	Program       string `json:"program,omitempty"`
	InSetup       bool   `json:"inSetup"`
	KeyConfigured bool   `json:"keyConfigured"`
	// Subscription says calls use the plan's allowance, not per-call billing.
	Subscription bool `json:"subscription"`
}

// LLMRest is a candidate that failed lately and goes behind the others until
// Until. An empty Model means every model of the provider.
type LLMRest struct {
	Provider string  `json:"provider"`
	Model    string  `json:"model,omitempty"`
	Why      string  `json:"why"`
	Until    Time    `json:"until"`
	Failures SafeInt `json:"failures"`
}

// LLMRouting is everything the Models settings page shows. CapSpentUSD is what
// the daily cap is held to: the metered spend of this computer's day, from
// local midnight to the next.
type LLMRouting struct {
	Preset         string         `json:"preset"`
	PresetFallback string         `json:"presetFallback,omitempty"`
	DailyUSDCap    float64        `json:"dailyUsdCap"`
	CapSpentUSD    float64        `json:"capSpentUsd"`
	Providers      []LLMProvider  `json:"providers"`
	Presets        []LLMPreset    `json:"presets"`
	Tasks          []LLMTaskRoute `json:"tasks"`
	// Groups are the routing groups by name.
	Groups map[string]LLMGroup `json:"groups"`
	// LastTest is the newest provider test, null before the first. Testing
	// names the provider whose test is running now.
	LastTest *LLMTestResult `json:"lastTest" tstype:"LLMTestResult | null"`
	Testing  string         `json:"testing,omitempty"`
	// KnownModels are the models worth offering in a picker, per configured
	// provider; any valid model id may still be routed.
	KnownModels []LLMFallback `json:"knownModels"`
	// Rests lists the candidates resting now (kept in memory, so it is empty
	// after a restart).
	Rests []LLMRest `json:"rests"`
	// Offline is set while the offline simulation serves every task.
	Offline bool `json:"offline,omitempty"`
}

// LLMRoutingPatch replaces the named rows in full; a null row clears the
// task's row. Groups does the same for the named groups, and a null group is
// deleted, which is refused while a row names it. Budgets sets the named tasks'
// daily budgets, 0 removing one. The whole patch is validated before anything
// changes.
type LLMRoutingPatch struct {
	Routes      map[string]*LLMRoute `json:"routes,omitempty"`
	Groups      map[string]*LLMGroup `json:"groups,omitempty"`
	DailyUSDCap *float64             `json:"dailyUsdCap,omitempty"`
	Budgets     map[string]float64   `json:"budgets,omitempty"`
}

// LLMPresetRequest makes a preset active, optionally with a fallback preset.
type LLMPresetRequest struct {
	Name     string `json:"name"`
	Fallback string `json:"fallback,omitempty"`
}

// LLMKeyRequest stores a provider key. The key is never returned.
type LLMKeyRequest struct {
	Key string `json:"key"`
}

// LLMKeyStatus reports whether a provider has a key, never the key.
type LLMKeyStatus struct {
	Provider      string `json:"provider"`
	KeyConfigured bool   `json:"keyConfigured"`
}

// LLMCostRow is one line of the cost report.
type LLMCostRow struct {
	Name         string  `json:"name"`
	Calls        SafeInt `json:"calls"`
	SuccessRate  float64 `json:"successRate"`
	FallbackRate float64 `json:"fallbackRate"`
	CostUSD      float64 `json:"costUsd"`
	Unpriced     SafeInt `json:"unpriced"`
	P50Ms        SafeInt `json:"p50Ms"`
	P95Ms        SafeInt `json:"p95Ms"`
	// RateLimitedRate is the share refused as rate-limited; CacheHitRate is the
	// cached share of the input tokens, absent when none was reported.
	RateLimitedRate float64  `json:"rateLimitedRate"`
	CacheHitRate    *float64 `json:"cacheHitRate,omitempty"`
}

// LLMCost is the cost report of the last Days days.
type LLMCost struct {
	Days       SafeInt      `json:"days"`
	ByTask     []LLMCostRow `json:"byTask"`
	ByProvider []LLMCostRow `json:"byProvider"`
}

// ChatGPTStatus is the Sign in with ChatGPT state of the chatgpt provider. It
// carries no token. PlanUsage is false for a sign-in that did not grant the
// plan-usage scope. Message explains a signed-out state, a sign-in without plan
// usage or the failure of the last attempt.
type ChatGPTStatus struct {
	SignedIn  bool   `json:"signedIn"`
	PlanUsage bool   `json:"planUsage"`
	Account   string `json:"account,omitempty"`
	Pending   bool   `json:"pending"`
	Message   string `json:"message,omitempty"`
}

// ChatGPTSignInStarted holds the address the user opens in the system browser.
// The outcome arrives in ChatGPTStatus.
type ChatGPTSignInStarted struct {
	URL string `json:"url"`
}

// ChatGPTSignOutRequest ends the sign-in. Forget also drops the registration
// and the account, so another account can sign in.
type ChatGPTSignOutRequest struct {
	Forget bool `json:"forget,omitempty"`
}

// ChatGPTSignOutResult says whether OpenAI confirmed the revocation. When it did
// not, the tokens are gone from this computer all the same and the user can
// disconnect the app in ChatGPT settings.
type ChatGPTSignOutResult struct {
	Revoked bool `json:"revoked"`
}

// ChatGPTModel is a model the signed-in account lists.
type ChatGPTModel struct {
	Slug string `json:"slug"`
}

// ChatGPTModels is the account's model list.
type ChatGPTModels struct {
	Models []ChatGPTModel `json:"models"`
}
