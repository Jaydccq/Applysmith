package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// DefaultDailyCapUSD is the cap when none is stored.
const DefaultDailyCapUSD = 2.00

// MaxTaskBudgetUSD bounds a task's daily budget.
const MaxTaskBudgetUSD = 1000

const (
	minCeiling     = 16
	maxCeiling     = 400000
	minThinkingMax = 2048
	maxFallbacks   = 4
	minRowTimeout  = time.Second
	maxRowTimeout  = 30 * time.Minute
	maxFirstToken  = 600
)

// Fallback is one fallback candidate of a routing row.
type Fallback struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Route is a routing row. Zero values inherit the task's preset defaults.
type Route struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Thinking        string `json:"thinking,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	// FirstTokenSeconds abandons a streaming attempt whose first stream event
	// is later than this and moves to the next candidate; 0 is off.
	FirstTokenSeconds int        `json:"first_token_seconds,omitempty"`
	Fallbacks         []Fallback `json:"fallbacks,omitempty"`
}

// Price is a price in USD per million tokens.
type Price struct {
	InputPerM       float64 `json:"input_per_m"`
	CachedInputPerM float64 `json:"cached_input_per_m"`
	OutputPerM      float64 `json:"output_per_m"`
}

// Settings is the LLM configuration stored in the application database. It
// holds no secret.
type Settings struct {
	Providers []ProviderConfig `json:"providers"`
	Routes    map[string]Route `json:"routes"`
	// Preset is the active preset name; PresetFallback is the id of a provider
	// whose equivalent model becomes the first fallback of every preset row.
	Preset         string `json:"preset"`
	PresetFallback string `json:"preset_fallback"`
	// Offline routes every task to the fake provider. It is the setup choice
	// for a user with no key yet and is never set implicitly.
	Offline     bool `json:"offline"`
	DailyUSDCap float64
	// Budgets holds the daily budgets in USD keyed by task id; a task with
	// none has no budget of its own.
	Budgets map[string]float64 `json:"budgets"`
	// Groups holds the routing groups (groups.go) keyed by name; a routing row
	// names one with provider GroupProvider and the name as its model.
	Groups map[string]Group `json:"groups"`
	// Prices holds user-set prices keyed "<provider id>/<model>".
	Prices map[string]Price `json:"prices"`
}

// Provider returns the provider with the given id.
func (s Settings) Provider(id string) (ProviderConfig, bool) {
	for _, p := range s.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderConfig{}, false
}

const (
	keyProviders = "providers"
	keyRoutes    = "routes"
	keyPreset    = "preset"
	keyFallback  = "preset_fallback"
	keyOffline   = "offline"
	keyLastTest  = "last_test"
	keyCap       = "daily_usd_cap"
	keyBudgets   = "task_budgets"
	keyGroups    = "groups"
	keyPrices    = "prices"
	keyCapture   = "capture_until"
)

func timeText(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func readKV(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM llm_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kv := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		kv[k] = v
	}
	return kv, rows.Err()
}

func decodeSettings(kv map[string]string) (Settings, error) {
	s := Settings{Routes: map[string]Route{}, Prices: map[string]Price{}, Budgets: map[string]float64{}, Groups: map[string]Group{}, DailyUSDCap: DefaultDailyCapUSD}
	for key, dst := range map[string]any{keyProviders: &s.Providers, keyRoutes: &s.Routes, keyPrices: &s.Prices, keyBudgets: &s.Budgets, keyGroups: &s.Groups} {
		if raw, ok := kv[key]; ok {
			if err := json.Unmarshal([]byte(raw), dst); err != nil {
				return s, fmt.Errorf("llm settings %s: %w", key, err)
			}
		}
	}
	if s.Routes == nil {
		s.Routes = map[string]Route{}
	}
	if s.Prices == nil {
		s.Prices = map[string]Price{}
	}
	if s.Budgets == nil {
		s.Budgets = map[string]float64{}
	}
	if s.Groups == nil {
		s.Groups = map[string]Group{}
	}
	s.Preset, s.PresetFallback = kv[keyPreset], kv[keyFallback]
	s.Offline = kv[keyOffline] == "true"
	if raw, ok := kv[keyCap]; ok {
		if err := json.Unmarshal([]byte(raw), &s.DailyUSDCap); err != nil {
			return s, fmt.Errorf("llm settings %s: %w", keyCap, err)
		}
	}
	return s, nil
}

// LoadSettings reads the current settings. It is called on every model call,
// so a change applies without a restart.
func LoadSettings(ctx context.Context, st *store.Store) (Settings, error) {
	kv, err := readKV(ctx, st.Read())
	if err != nil {
		return Settings{}, err
	}
	return decodeSettings(kv)
}

// UpdateSettings applies fn to the stored settings and writes the result in one
// transaction. The whole result is validated first; on a validation error
// nothing changes.
func UpdateSettings(ctx context.Context, st *store.Store, now time.Time, fn func(*Settings) error) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		kv, err := readKV(ctx, tx)
		if err != nil {
			return err
		}
		s, err := decodeSettings(kv)
		if err != nil {
			return err
		}
		before := map[string]string{}
		for id, row := range s.Routes {
			b, _ := json.Marshal(row)
			before[id] = string(b)
		}
		for name, g := range s.Groups {
			b, _ := json.Marshal(g)
			before[groupKey(name)] = string(b)
		}
		if err := fn(&s); err != nil {
			return err
		}
		if err := s.validate(before); err != nil {
			return err
		}
		out := map[string]any{keyProviders: s.Providers, keyRoutes: s.Routes, keyPrices: s.Prices,
			keyPreset: s.Preset, keyFallback: s.PresetFallback, keyCap: s.DailyUSDCap, keyOffline: s.Offline, keyBudgets: s.Budgets, keyGroups: s.Groups}
		for k, v := range out {
			var text string
			if str, ok := v.(string); ok {
				text = str
			} else {
				b, err := json.Marshal(v)
				if err != nil {
					return err
				}
				text = string(b)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO llm_settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at WHERE value <> excluded.value`,
				k, text, timeText(now)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Validate checks the providers, every routing row, the cap and the budgets.
func (s Settings) Validate() error { return s.validate(nil) }

// validate checks the providers and the cap, and the routing rows and groups
// that differ from before: a stored row that became invalid because its
// provider was removed stays stored, is reported and is skipped at resolution.
func (s Settings) validate(before map[string]string) error {
	var problems []string
	seen := map[string]bool{}
	for _, p := range s.Providers {
		if err := p.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("provider %q: %v", p.ID, err))
		}
		if seen[p.ID] {
			problems = append(problems, fmt.Sprintf("provider %q is listed twice", p.ID))
		}
		seen[p.ID] = true
	}
	for _, name := range slices.Sorted(maps.Keys(s.Groups)) {
		if b, _ := json.Marshal(s.Groups[name]); before != nil && before[groupKey(name)] == string(b) {
			continue
		}
		if err := s.ValidateGroup(name, s.Groups[name]); err != nil {
			problems = append(problems, err.Error())
		}
	}
	ids := make([]string, 0, len(s.Routes))
	for id := range s.Routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if b, _ := json.Marshal(s.Routes[id]); before != nil && before[id] == string(b) {
			continue
		}
		if err := s.ValidateRoute(id, s.Routes[id]); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if s.DailyUSDCap < 0 {
		problems = append(problems, "daily_usd_cap: must not be negative")
	}
	for _, id := range slices.Sorted(maps.Keys(s.Budgets)) {
		if err := ValidateBudget(id, s.Budgets[id]); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Budgetable reports whether a task may have a daily budget: the spend limits
// move it to a cheaper model. A task they never move takes none, and neither
// do the agent rows, whose turns only the daily cap downgrades or pauses.
func Budgetable(t Task) bool {
	return !exemptFromDowngrade[t.ID] && t.Class != ClassEmbedding && !agentRow(t.ID)
}

// ValidateBudget checks a task's daily budget in USD; 0 means none.
func ValidateBudget(taskID string, usd float64) error {
	task, ok := LookupTask(taskID)
	switch {
	case !ok:
		return fmt.Errorf("budgets.%s: unknown task id", taskID)
	case !Budgetable(task):
		return fmt.Errorf("budgets.%s: the spend limits never move this task to a cheaper model, so it takes no budget", taskID)
	case !(usd >= 0 && usd <= MaxTaskBudgetUSD):
		return fmt.Errorf("budgets.%s: must be between 0 and %d US dollars a day", taskID, MaxTaskBudgetUSD)
	}
	return nil
}

var modelIDShape = lazyregexp.New(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)

// ValidModelID checks a model id. It never starts with a hyphen, so an id can
// not turn into an option when it reaches a command line.
func ValidModelID(id string) error {
	if !modelIDShape.MatchString(id) {
		return fmt.Errorf("%q is not a model id: 1 to 128 letters, digits and . _ : / @ + -, starting with a letter or digit", id)
	}
	return nil
}

// ValidateRoute checks one routing row against the configured providers.
func (s Settings) ValidateRoute(taskID string, r Route) error {
	task, ok := LookupTask(taskID)
	if !ok {
		return fmt.Errorf("routes.%s: unknown task id", taskID)
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("routes.%s: %s", taskID, fmt.Sprintf(format, args...))
	}
	primary := []Fallback{{r.Provider, r.Model}}
	if r.Provider == GroupProvider {
		g, ok := s.Groups[r.Model]
		switch {
		case !ok:
			return fail("group %q is not defined", r.Model)
		case task.Class == ClassEmbedding:
			return fail("an embedding task takes no group: vectors of different models cannot be compared")
		}
		primary = g.Members
	} else if err := ValidModelID(r.Model); err != nil {
		return fail("model: %v", err)
	}
	if r.Thinking != "" && !validThinking(r.Thinking) {
		return fail("thinking: %q is not one of %s", r.Thinking, strings.Join(thinkingLevels, ", "))
	}
	if r.MaxOutputTokens != 0 && (r.MaxOutputTokens < minCeiling || r.MaxOutputTokens > maxCeiling) {
		return fail("max_output_tokens: must be between %d and %d", minCeiling, maxCeiling)
	}
	if t := time.Duration(r.TimeoutSeconds) * time.Second; r.TimeoutSeconds != 0 && (t < minRowTimeout || t > maxRowTimeout) {
		return fail("timeout_seconds: must be between 1 and 1800")
	}
	if r.FirstTokenSeconds < 0 || r.FirstTokenSeconds > maxFirstToken {
		return fail("first_token_seconds: must be 0 (off) or between 1 and %d", maxFirstToken)
	}
	if r.FirstTokenSeconds != 0 && r.TimeoutSeconds != 0 && r.FirstTokenSeconds >= r.TimeoutSeconds {
		return fail("first_token_seconds: must be shorter than timeout_seconds")
	}
	thinking := r.Thinking
	if thinking == "" {
		thinking = task.Thinking
	}
	if thinking != ThinkOff && thinking != "" && r.MaxOutputTokens != 0 && r.MaxOutputTokens < minThinkingMax {
		return fail("max_output_tokens: the ceiling must be at least %d or thinking must be off, because reasoning and answer share it", minThinkingMax)
	}
	if len(r.Fallbacks) > maxFallbacks {
		return fail("fallbacks: at most %d", maxFallbacks)
	}
	cands := append(slices.Clone(primary), r.Fallbacks...)
	for i, c := range cands {
		if c.Provider == GroupProvider && i >= len(primary) {
			return fail("fallbacks[%d]: a group is a primary, not a fallback", i-len(primary))
		}
		if _, isPi := PiLogin(c.Provider); isPi {
			if taskID != AgentTask || i > 0 {
				return fail("provider %q is a login of the agent runtime; only the primary of %s may use one", c.Provider, AgentTask)
			}
			continue
		}
		p, ok := s.Provider(c.Provider)
		if !ok {
			return fail("provider %q is not configured", c.Provider)
		}
		if i >= len(primary) {
			if err := ValidModelID(c.Model); err != nil {
				return fail("fallbacks[%d].model: %v", i-len(primary), err)
			}
		}
		if err := CheckCapability(task, p); err != nil {
			return fail("%v", err)
		}
	}
	return nil
}

// BrokenRoutes lists the stored rows that no longer validate, for the doctor.
func (s Settings) BrokenRoutes() map[string]error {
	out := map[string]error{}
	for id, r := range s.Routes {
		if err := s.ValidateRoute(id, r); err != nil {
			out[id] = err
		}
	}
	return out
}
