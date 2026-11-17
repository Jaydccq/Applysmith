// Package modelsapi serves the Models page in the local API: task routing,
// presets, provider keys, the provider test and the cost report.
// internal/api registers the handlers.
package modelsapi

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// Deps is what the Models page's handlers use from the server.
type Deps struct {
	Router           func() (*llm.Router, error)
	DB               func() (*store.Store, error)
	Vault            *vault.Vault      // which providers have a key
	VaultError       func(error) error // shared with the other keychain routes
	IsInfrastructure func(error) bool  // a machine failure is not answered as a refused preset
	Notify           func(topics ...string)
	Log              func() *slog.Logger    // set when the server starts
	BG               func() context.Context // the provider test outlives its request
	BGWG             *sync.WaitGroup
}

// Handlers serves the Models page's routes.
type Handlers struct {
	Deps
	Test providerTest
}

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// needsKey says whether a provider kind is called with an API key.
func needsKey(kind string) bool { return kind == llm.KindOpenAI || kind == llm.KindAnthropic }

func (s *Handlers) keyConfigured(providerID string) bool {
	return s.Vault.Has(llm.KeyName(providerID))
}

// keysConfigured answers for several providers at once, the lookups the vault
// has not kept running side by side.
func (s *Handlers) keysConfigured(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	asked := map[string]bool{}
	var mu sync.Mutex // guards looked, which the lookups fill side by side
	looked := map[string]bool{}
	var wg sync.WaitGroup
	for _, id := range ids {
		if asked[id] {
			continue
		}
		asked[id] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			configured := s.keyConfigured(id)
			mu.Lock()
			looked[id] = configured
			mu.Unlock()
		}()
	}
	wg.Wait()
	for id, configured := range looked {
		out[id] = configured
	}
	return out
}

// WarmKeyCache looks the presets' keys up while the server starts, so the
// first Settings or Home load does not wait for the keychain.
func (s *Handlers) WarmKeyCache() {
	var ids []string
	for _, p := range llm.Presets() {
		if needsKey(p.Kind) {
			ids = append(ids, p.Name)
		}
	}
	s.keysConfigured(ids)
}

func toFallbacks(in []llm.Fallback) []wire.LLMFallback {
	if len(in) == 0 {
		return nil
	}
	out := make([]wire.LLMFallback, len(in))
	for i, f := range in {
		out[i] = wire.LLMFallback{Provider: f.Provider, Model: f.Model}
	}
	return out
}

func fromFallbacks(in []wire.LLMFallback) []llm.Fallback {
	if len(in) == 0 {
		return nil
	}
	out := make([]llm.Fallback, len(in))
	for i, f := range in {
		out[i] = llm.Fallback{Provider: f.Provider, Model: f.Model}
	}
	return out
}

// View is RoutingView with the test this server runs now.
func (s *Handlers) View(ctx context.Context, router *llm.Router) (wire.LLMRouting, error) {
	st, err := s.DB()
	if err != nil {
		return wire.LLMRouting{}, err
	}
	out, err := RoutingView(ctx, st, router, s.keysConfigured)
	out.Testing = s.Test.running()
	return out, err
}

// RoutingView assembles the Models page: providers, presets and the effective
// route of every task. keysConfigured says which providers have a stored key.
func RoutingView(ctx context.Context, st *store.Store, router *llm.Router, keysConfigured func([]string) map[string]bool) (wire.LLMRouting, error) {
	settings, err := llm.LoadSettings(ctx, st)
	if err != nil {
		return wire.LLMRouting{}, err
	}
	table, err := router.Show(ctx)
	if err != nil {
		return wire.LLMRouting{}, err
	}
	out := wire.LLMRouting{
		Preset: settings.Preset, PresetFallback: settings.PresetFallback, Offline: settings.Offline, DailyUSDCap: settings.DailyUSDCap,
		Providers: []wire.LLMProvider{}, Presets: []wire.LLMPreset{}, Tasks: []wire.LLMTaskRoute{}, Rests: []wire.LLMRest{},
		Groups: map[string]wire.LLMGroup{},
	}
	for name, g := range settings.Groups {
		out.Groups[name] = wire.LLMGroup{Mode: g.Mode, Members: toFallbacks(g.Members)}
	}
	out.CapSpentUSD, _ = router.DailySpend(ctx)
	spent := router.TaskSpend(ctx)
	out.KnownModels = append([]wire.LLMFallback{}, toFallbacks(llm.KnownModels(settings))...)
	for _, rest := range router.Rests() {
		out.Rests = append(out.Rests, wire.LLMRest{Provider: rest.Provider, Model: rest.Model, Why: rest.Why,
			Until: wire.Time{Time: rest.Until.UTC()}, Failures: apikit.Safe(rest.Failures)})
	}
	var keyed []string
	for _, p := range settings.Providers {
		if needsKey(p.Kind) {
			keyed = append(keyed, p.ID)
		}
	}
	for _, p := range llm.Presets() {
		if needsKey(p.Kind) {
			keyed = append(keyed, p.Name)
		}
	}
	configured := keysConfigured(keyed)
	for _, p := range settings.Providers {
		out.Providers = append(out.Providers, wire.LLMProvider{
			ID: p.ID, Kind: p.Kind, BaseURL: p.BaseURL, Preset: p.Preset, Program: p.Program, StructuredOutput: p.StructuredOutput,
			Embeddings: p.Embeddings, KeyConfigured: configured[p.ID],
		})
	}
	for _, p := range llm.Presets() {
		out.Presets = append(out.Presets, wire.LLMPreset{
			Name: p.Name, Label: p.Label, Kind: p.Kind, KeyURL: p.KeyURL, Program: p.Program, InSetup: p.InSetup,
			KeyConfigured: configured[p.Name], Subscription: p.UsesPlan(),
		})
	}
	if ts, err := router.TestStatus(ctx); err == nil && ts.Result != nil {
		out.LastTest = WireTestResult(*ts.Result)
	}
	resolved := map[string]llm.Resolved{}
	for _, r := range table.Rows {
		resolved[r.Task.ID] = r
	}
	benches, err := router.BenchLatest(ctx)
	if err != nil {
		return wire.LLMRouting{}, err
	}
	for _, t := range llm.Tasks() {
		row := wire.LLMTaskRoute{Task: t.ID, Group: t.Group, Class: string(t.Class), Purpose: t.Purpose,
			BudgetUSD: settings.Budgets[t.ID], SpentTodayUSD: spent[t.ID], BudgetReached: router.BudgetReached(ctx, t.ID), Budgetable: llm.Budgetable(t)}
		if r, ok := resolved[t.ID]; ok {
			row.Provider, row.Model, row.Thinking = r.Provider, r.Model, r.Thinking
			row.MaxOutputTokens, row.TimeoutSeconds = apikit.Safe(r.MaxOutputTokens), apikit.Safe(int(r.Timeout.Seconds()))
			row.FirstTokenSeconds = apikit.Safe(int(r.FirstToken.Seconds()))
			row.Fallbacks, row.Source = toFallbacks(r.Fallbacks), r.Source
		}
		if b, ok := benches[t.ID]; ok {
			row.Bench = WireBench(b)
		}
		row.Unrouted = table.Unrouted[t.ID]
		if err := table.Broken[t.ID]; err != nil {
			row.Broken = err.Error()
		}
		out.Tasks = append(out.Tasks, row)
	}
	return out, nil
}

func WireBench(b llm.BenchRun) *wire.LLMBench {
	at, _ := time.Parse("2006-01-02T15:04:05.000Z", b.At)
	out := &wire.LLMBench{At: wire.Time{Time: at.UTC()}, Cases: apikit.Safe(b.Cases), Results: []wire.LLMBenchResult{}}
	for _, r := range b.Results {
		out.Results = append(out.Results, wire.LLMBenchResult{Provider: r.Provider, Model: r.Model, Correct: apikit.Safe(r.Correct), Wrong: apikit.Safe(r.Wrong),
			NoAnswer: apikit.Safe(r.NoAnswer), Errors: apikit.Safe(r.Errors), P50Ms: apikit.Safe64(r.P50Ms), P95Ms: apikit.Safe64(r.P95Ms), CostUSD: r.CostUSD})
	}
	return out
}

func (s *Handlers) Routing(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	view, err := s.View(c.R.Context(), router)
	return apikit.Reply{Data: view}, err
}

// PatchRouting replaces the named rows in full, validating the whole patch
// before anything is stored.
func (s *Handlers) PatchRouting(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.LLMRoutingPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.DailyUSDCap != nil && *req.DailyUSDCap < 0 {
		return apikit.Reply{}, apikit.InvalidField("dailyUsdCap", "dailyUsdCap must not be negative")
	}
	for _, id := range slices.Sorted(maps.Keys(req.Budgets)) {
		if err := llm.ValidateBudget(id, req.Budgets[id]); err != nil {
			return apikit.Reply{}, apikit.InvalidField("budgets."+id, "%s", err.Error())
		}
	}
	settings, err := llm.LoadSettings(c.R.Context(), st)
	if err != nil {
		return apikit.Reply{}, err
	}
	groups := map[string]*llm.Group{}
	for name, g := range req.Groups {
		if g != nil {
			groups[name] = &llm.Group{Mode: g.Mode, Members: fromFallbacks(g.Members)}
		} else {
			groups[name] = nil
		}
	}
	tasks := make([]string, 0, len(req.Routes))
	for id := range req.Routes {
		tasks = append(tasks, id)
	}
	sort.Strings(tasks)
	patch := map[string]*llm.Route{}
	for _, id := range tasks {
		row := req.Routes[id]
		if row == nil {
			if _, ok := llm.LookupTask(id); !ok {
				return apikit.Reply{}, apikit.InvalidField("routes."+id, "routes.%s: unknown task id", id)
			}
			patch[id] = nil
			continue
		}
		r := llm.Route{Provider: row.Provider, Model: row.Model, Thinking: row.Thinking, MaxOutputTokens: int(row.MaxOutputTokens),
			TimeoutSeconds: int(row.TimeoutSeconds), FirstTokenSeconds: int(row.FirstTokenSeconds), Fallbacks: fromFallbacks(row.Fallbacks)}
		patch[id] = &r
	}
	// Rows are checked against the groups of the same patch.
	settings, err = settings.WithPatch(patch, groups)
	if err != nil {
		return apikit.Reply{}, apikit.InvalidField("groups", "%s", err.Error())
	}
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		if groups[name] == nil {
			continue
		}
		if err := settings.ValidateGroup(name, *groups[name]); err != nil {
			return apikit.Reply{}, apikit.InvalidField("groups."+name, "%s", err.Error())
		}
	}
	for _, id := range tasks {
		if patch[id] == nil {
			continue
		}
		if err := settings.ValidateRoute(id, *patch[id]); err != nil {
			return apikit.Reply{}, apikit.InvalidField("routes."+id, "%s", err.Error())
		}
	}
	if len(patch) > 0 || len(groups) > 0 {
		if err := router.PatchRouting(c.R.Context(), patch, groups); err != nil {
			return apikit.Reply{}, err
		}
	}
	if req.DailyUSDCap != nil {
		if err := router.SetDailyCap(c.R.Context(), *req.DailyUSDCap); err != nil {
			return apikit.Reply{}, err
		}
	}
	if len(req.Budgets) > 0 {
		if err := router.SetBudgets(c.R.Context(), req.Budgets); err != nil {
			return apikit.Reply{}, err
		}
	}
	view, err := s.View(c.R.Context(), router)
	return apikit.Reply{Data: view}, err
}

func (s *Handlers) ConfigurePreset(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.LLMPresetRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if _, ok := llm.PresetByName(req.Name); !ok {
		return apikit.Reply{}, apikit.InvalidField("name", "unknown preset %q; valid presets: %s", req.Name, strings.Join(llm.PresetNames(), ", "))
	}
	if req.Fallback != "" {
		if _, ok := llm.PresetByName(req.Fallback); !ok {
			return apikit.Reply{}, apikit.InvalidField("fallback", "unknown fallback preset %q; valid presets: %s", req.Fallback, strings.Join(llm.PresetNames(), ", "))
		}
	}
	err = router.ConfigurePreset(c.R.Context(), req.Name, llm.ConfigureOptions{Fallback: req.Fallback})
	var setup *llm.SetupError
	if errors.As(err, &setup) {
		return apikit.Reply{}, apikit.InvalidField("name", "%s", setup.Message)
	}
	if s.IsInfrastructure(err) {
		return apikit.Reply{}, err
	}
	if err != nil {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	}
	view, err := s.View(c.R.Context(), router)
	return apikit.Reply{Data: view}, err
}

// keyTarget resolves the {provider} path value to a provider or preset that
// takes an API key.
func (s *Handlers) keyTarget(c *apikit.Call) (string, error) {
	st, err := s.DB()
	if err != nil {
		return "", err
	}
	id := c.R.PathValue("provider")
	kind := ""
	if p, ok := llm.PresetByName(id); ok {
		kind = p.Kind
	}
	if settings, err := llm.LoadSettings(c.R.Context(), st); err == nil {
		if p, ok := settings.Provider(id); ok {
			kind = p.Kind
		}
	}
	switch {
	case kind == "":
		return "", apikit.NewErr(wire.CodeNotFound, "no such provider or preset")
	case !needsKey(kind):
		return "", apikit.NewErr(wire.CodeConflict, "the %s kind takes no API key", kind)
	}
	return id, nil
}

func (s *Handlers) SetKey(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, err := s.keyTarget(c)
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.LLMKeyRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if err := llm.ValidateAPIKey(req.Key); err != nil {
		return apikit.Reply{}, apikit.InvalidField("key", "%s", err.Error())
	}
	if err := router.SetKey(id, req.Key); err != nil {
		return apikit.Reply{}, s.VaultError(err)
	}
	return apikit.Reply{Data: wire.LLMKeyStatus{Provider: id, KeyConfigured: s.keyConfigured(id)}}, nil
}

func (s *Handlers) ClearKey(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, err := s.keyTarget(c)
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := router.ClearKey(id); err != nil {
		return apikit.Reply{}, s.VaultError(err)
	}
	return apikit.Reply{Data: wire.LLMKeyStatus{Provider: id, KeyConfigured: s.keyConfigured(id)}}, nil
}

func (s *Handlers) Cost(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	days := q.Integer("days", 7)
	if q.Err == nil && (days < 1 || days > 90) {
		q.Fail(apikit.InvalidField("days", "days must be between 1 and 90"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	byTask, byProvider, err := router.CostReport(c.R.Context(), days)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.LLMCost{Days: apikit.Safe(days), ByTask: costRows(byTask), ByProvider: costRows(byProvider)}}, nil
}

func costRows(in []llm.ReportRow) []wire.LLMCostRow {
	out := make([]wire.LLMCostRow, len(in))
	for i, r := range in {
		out[i] = wire.LLMCostRow{Name: r.Name, Calls: apikit.Safe(r.Calls), SuccessRate: r.SuccessRate, FallbackRate: r.FallbackRate,
			CostUSD: r.CostUSD, Unpriced: apikit.Safe(r.Unpriced), P50Ms: apikit.Safe64(r.P50.Milliseconds()), P95Ms: apikit.Safe64(r.P95.Milliseconds()),
			RateLimitedRate: r.RateLimited, CacheHitRate: r.CacheHit}
	}
	return out
}

// providerTest is the one provider test that may run at a time. The call can
// take longer than a request may (a command-line provider gets 90 seconds), so
// the route starts it and the routing view reports it.
type providerTest struct {
	mu       sync.Mutex
	provider string
}

func (p *providerTest) running() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.provider
}

func (p *providerTest) Begin(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != "" {
		return false
	}
	p.provider = id
	return true
}

func (p *providerTest) End() {
	p.mu.Lock()
	p.provider = ""
	p.mu.Unlock()
}

func WireTestResult(r llm.TestResult) *wire.LLMTestResult {
	at, _ := time.Parse("2006-01-02T15:04:05.000Z", r.TestedAt)
	return &wire.LLMTestResult{Provider: r.Provider, Model: r.Model, Outcome: r.Outcome, ErrorClass: r.ErrorClass, Fix: r.Fix,
		LatencyMs: apikit.Safe(r.LatencyMS), TestedAt: wire.Time{Time: at.UTC()}, CostUSD: r.CostUSD, Note: r.Note}
}

// TestProvider starts the live test of one configured provider: one fixed
// short prompt, classified on failure. It answers 202 at once. The body may
// name the model to test.
func (s *Handlers) TestProvider(c *apikit.Call) (apikit.Reply, error) {
	var req wire.LLMTestRequest
	if len(c.Body) > 0 {
		if e := apikit.Decode(c.Body, &req); e != nil {
			return apikit.Reply{}, e
		}
	}
	if req.Model != "" {
		if err := llm.CheckTestModel(req.Model); err != nil {
			return apikit.Reply{}, apikit.InvalidField("model", "%s", err.Error())
		}
	}
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("provider")
	settings, err := llm.LoadSettings(c.R.Context(), st)
	if err != nil {
		return apikit.Reply{}, err
	}
	if _, ok := settings.Provider(id); !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "provider %q is not configured", id)
	}
	if !s.Test.Begin(id) {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "a provider test is already running"), "busy")
	}
	s.BGWG.Add(1)
	go func() {
		defer s.BGWG.Done()
		defer s.Test.End()
		if _, err := router.Test(s.BG(), id, req.Model); err != nil && s.Log() != nil {
			s.Log().Warn("the provider test could not be saved", "provider", id, "error", err.Error())
		}
		s.Notify("configuration")
	}()
	return apikit.Reply{Status: http.StatusAccepted, Data: wire.LLMTestStarted{Provider: id}}, nil
}
