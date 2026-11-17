package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// The live test sends one fixed short prompt on a cheap, thinking-off task.
const (
	testTask      = "feed-classify"
	testPrompt    = "Reply with the single word ok."
	testMaxTokens = 16
	testAPILimit  = 30 * time.Second
	testCLILimit  = 90 * time.Second
)

// Error classes of a failed test.
const (
	ClassAuth          = "auth"
	ClassRateLimit     = "rate-limit"
	ClassBilling       = "billing"
	ClassNetwork       = "network"
	ClassModelNotFound = "model-not-found"
	ClassTimeout       = "timeout"
	ClassSetup         = "setup"
	ClassOther         = "other"
)

// TestResult is the saved outcome of a test call. It holds no prompt and no
// response text.
type TestResult struct {
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Outcome    string   `json:"outcome"` // ok or failed
	ErrorClass string   `json:"error_class,omitempty"`
	Fix        string   `json:"fix,omitempty"`
	LatencyMS  int      `json:"latency_ms"`
	TestedAt   string   `json:"tested_at"`
	CostUSD    *float64 `json:"cost_usd,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// OK reports whether the test succeeded.
func (t TestResult) OK() bool { return t.Outcome == "ok" }

var billingText = lazyregexp.New(`(?i)insufficient[ _](funds|quota|credit|balance)|exceeded your (current )?quota|out of credits|billing|payment required|credit balance`)

// ClassifyTest maps a failed call to a test error class and a one-line fix.
func ClassifyTest(err error, providerID string) (class, fix string) {
	var e *Error
	if !errors.As(err, &e) {
		if errors.Is(err, context.DeadlineExceeded) {
			return ClassTimeout, "retry; if it keeps timing out, check the network or pick a faster model"
		}
		return ClassOther, "run `applysmith llm show` and check the provider settings"
	}
	switch {
	case e.Status == 402 || (billingText.MatchString(e.Message) && e.Kind != KindNetwork):
		return ClassBilling, "add credit or fix billing in the provider's console, then run `applysmith llm test` again"
	}
	switch e.Kind {
	case KindAuth, KindUnconfigured:
		if p, ok := PresetByName(providerID); ok && p.Kind == KindCLI {
			return ClassAuth, "sign the " + p.Program + " command-line tool in again (run `" + p.Program + "` once in a terminal), then test again"
		}
		return ClassAuth, "paste a new key with `applysmith llm keys set " + providerID + "`"
	case KindRateLimited:
		return ClassRateLimit, "wait a minute and test again, or use a key with a higher limit"
	case KindNetwork:
		return ClassNetwork, "check the internet connection and any proxy, then test again"
	case KindModelGone:
		return ClassModelNotFound, "the model is not available to this key; choose another with `applysmith llm set <task> " + providerID + "/<model>`"
	case KindTimeout, KindStreamStalled, KindDeadline:
		return ClassTimeout, "retry; if it keeps timing out, check the network or pick a faster model"
	}
	return ClassOther, "run `applysmith llm show` and check the provider settings"
}

// classifyChatGPTTest words the fix of a failed ChatGPT plan test: it has no
// API key to paste, and a usage limit is neither billing nor a rate limit.
func classifyChatGPTTest(kind Kind, class, fix string) (string, string) {
	switch {
	case kind == KindQuota:
		return ClassBilling, "the ChatGPT plan's usage limit is reached; check ChatGPT settings, Usage, and test again later"
	case class == ClassAuth:
		return class, "sign in with ChatGPT again under Settings, Providers or with `applysmith llm signin chatgpt`"
	}
	return class, fix
}

// CheckTestModel refuses a model a test may not name: a malformed id or a
// retired model.
func CheckTestModel(model string) error {
	if err := ValidModelID(model); err != nil {
		return err
	}
	if IsRetired(model) {
		return fmt.Errorf("%s is retired; test a current model", model)
	}
	return nil
}

// Test sends the fixed prompt through one provider and saves the result. An
// empty providerID tests the active preset's provider; an empty model tests
// the provider's usual model (testModel). A reply cut at the 16-token ceiling
// still proves the key, the model and the route, so it counts as a success.
// A test that cannot start for a named provider is saved as failed (class
// setup) with the fix, so the user who started it sees why.
func (r *Router) Test(ctx context.Context, providerID, model string) (TestResult, error) {
	setupFail := func(fix string) (TestResult, error) {
		res := TestResult{Provider: providerID, Model: model, Outcome: "failed", ErrorClass: ClassSetup, Fix: fix, TestedAt: timeText(r.now())}
		return res, r.saveTest(ctx, res)
	}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return TestResult{}, err
	}
	var cfg ProviderConfig
	switch {
	case r.offline || s.Offline:
		cfg, model = ProviderConfig{ID: offlineProvider, Kind: KindFake}, "fake"
	default:
		if providerID == "" {
			providerID = s.Preset
		}
		if providerID == "" {
			return TestResult{}, &SetupError{Message: "no provider is configured; run `applysmith llm configure --preset <name> --key-stdin`"}
		}
		var ok bool
		if cfg, ok = s.Provider(providerID); !ok {
			return setupFail(fmt.Sprintf("provider %q is not configured; run `applysmith llm configure --preset %s --key-stdin`", providerID, providerID))
		}
		if model != "" {
			if err := CheckTestModel(model); err != nil {
				return setupFail(err.Error())
			}
		}
		if model == "" {
			model = testModel(s, cfg)
		}
		if model == "" {
			return setupFail(fmt.Sprintf("no model is known for provider %q; route a task to it with `applysmith llm set`", providerID))
		}
	}
	if cfg.Kind == KindFake {
		r.fake.addTestFixture()
	}
	limit := testAPILimit
	if cfg.Kind == KindCLI {
		limit = testCLILimit
	}
	start := r.now()
	cctx, cancel := context.WithTimeout(ctx, limit+5*time.Second)
	defer cancel()
	resp, callErr := r.Complete(cctx, Request{Task: testTask, Prompt: testPrompt, MaxOutputTokens: testMaxTokens,
		Model: cfg.ID + "/" + model, Timeout: limit})
	res := TestResult{Provider: cfg.ID, Model: model, Outcome: "ok", LatencyMS: int(r.now().Sub(start).Milliseconds()), TestedAt: timeText(r.now())}
	switch kind := KindOf(callErr); {
	case callErr == nil:
		res.CostUSD = resp.CostUSD
	case kind == KindTruncated || kind == KindEmptyCompletion || kind == KindRunaway:
		res.Note = "the reply was cut at the " + fmt.Sprint(testMaxTokens) + "-token limit; the key, model and route work"
	default:
		res.Outcome = "failed"
		res.ErrorClass, res.Fix = ClassifyTest(callErr, cfg.ID)
		if cfg.Kind == KindChatGPT {
			res.ErrorClass, res.Fix = classifyChatGPTTest(kind, res.ErrorClass, res.Fix)
		}
	}
	if err := r.saveTest(ctx, res); err != nil {
		return res, err
	}
	return res, nil
}

// testModel is the model a test uses: the preset's fast model, else the first
// stored route that names the provider.
func testModel(s Settings, cfg ProviderConfig) string {
	if p, ok := PresetByName(cfg.Preset); ok {
		if t, ok := LookupTask(testTask); ok {
			if m := p.ModelFor(t); m != "" {
				return m
			}
		}
	}
	for _, t := range Tasks() {
		if row, ok := s.Routes[t.ID]; ok && row.Provider == cfg.ID {
			return row.Model
		}
	}
	return ""
}

func (r *Router) saveTest(ctx context.Context, res TestResult) error {
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return r.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO llm_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, keyLastTest, string(b), timeText(r.now()))
		return err
	})
}

// TestStatus is the latest saved test and whether the provider configuration
// changed after it.
type TestStatus struct {
	Result *TestResult
	// ConfigChangedAt is the time of the newest provider, preset or offline
	// change, empty when none was ever stored.
	ConfigChangedAt string
	// Stale is true when the configuration changed after the test.
	Stale bool
	Age   time.Duration
}

// TestStatus reads the cached test result; it makes no call.
func (r *Router) TestStatus(ctx context.Context) (TestStatus, error) {
	rows, err := r.store.Read().QueryContext(ctx, `SELECT key, value, updated_at FROM llm_settings WHERE key IN (?, ?, ?, ?, ?)`,
		keyLastTest, keyProviders, keyPreset, keyFallback, keyOffline)
	if err != nil {
		return TestStatus{}, err
	}
	defer rows.Close()
	var st TestStatus
	var tested string
	for rows.Next() {
		var k, v, at string
		if err := rows.Scan(&k, &v, &at); err != nil {
			return TestStatus{}, err
		}
		if k == keyLastTest {
			var res TestResult
			if json.Unmarshal([]byte(v), &res) == nil {
				st.Result, tested = &res, res.TestedAt
			}
			continue
		}
		if at > st.ConfigChangedAt {
			st.ConfigChangedAt = at
		}
	}
	if err := rows.Err(); err != nil {
		return TestStatus{}, err
	}
	if st.Result != nil {
		st.Stale = st.ConfigChangedAt > tested
		if t, err := time.Parse(time.RFC3339Nano, tested); err == nil {
			st.Age = r.now().Sub(t)
		}
	}
	return st, nil
}

// CoreLoopTasks are the tasks the daily loop needs; the doctor checks that
// each one is routed to a usable provider.
var CoreLoopTasks = []string{"quick-screen", "deep-eval", "jd-extract", "option-pick", "apply-check", "apply-complete", "narrative", "resume-write", "cover-letter"}

// Bench replays a small set of cases through each candidate route and records
// how often each answered correctly, wrongly or not at all, how fast and at
// what cost, so a task's model can be chosen from a measurement. It starts
// with the option-pick task.

const (
	benchTask       = "option-pick"
	benchKeyPrefix  = "bench:"
	benchNone       = "NONE"
	maxBenchCases   = 100
	maxBenchCands   = 8
	benchRunID      = "bench"
	benchOptionRule = `You choose one option of a form question for a job applicant.
Use ONLY the verified facts you are given. Choose the option a fact supports, and cite the ids of the facts you rely on.
If no fact supports any option, abstain. Never invent a fact.
Reply with one JSON object and nothing else: {"option": "<the option label exactly>", "facts": ["f1"]} or {"abstain": true}.`
)

// BenchCase is one question with its options and the answer a careful model
// gives: an option label, or NONE when no fact supports any option.
type BenchCase struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Facts    []string `json:"facts,omitempty"`
	Expect   string   `json:"expect"`
}

// BenchResult is one candidate's score over the cases.
type BenchResult struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Correct  int    `json:"correct"`
	Wrong    int    `json:"wrong"`
	// NoAnswer counts abstentions where an option was expected and calls that
	// failed; Errors is the failed part of it.
	NoAnswer int      `json:"no_answer"`
	Errors   int      `json:"errors"`
	P50Ms    int64    `json:"p50_ms"`
	P95Ms    int64    `json:"p95_ms"`
	CostUSD  *float64 `json:"cost_usd,omitempty"`
}

// BenchRun is the latest bench of a task, best candidate first.
type BenchRun struct {
	Task    string        `json:"task"`
	At      string        `json:"at"`
	Cases   int           `json:"cases"`
	Results []BenchResult `json:"results"`
}

// DefaultBenchCases is a small invented set (an invented applicant, no real
// data) with a few questions no fact answers.
func DefaultBenchCases() []BenchCase {
	yn := []string{"Yes", "No"}
	return []BenchCase{
		{"Are you legally authorized to work in the United States?", yn, []string{"The applicant is authorized to work in the United States without sponsorship."}, "Yes"},
		{"Will you now or in the future require visa sponsorship?", yn, []string{"The applicant is authorized to work in the United States without sponsorship."}, "No"},
		{"What is your highest level of education?", []string{"High school", "Bachelor's degree", "Master's degree", "Doctorate"}, []string{"The applicant holds a master's degree in computer science."}, "Master's degree"},
		{"How many years of professional software experience do you have?", []string{"0-1", "2-4", "5-9", "10+"}, []string{"The applicant has worked as a software engineer for 7 years."}, "5-9"},
		{"Are you willing to relocate?", yn, nil, benchNone},
		{"What are your pronouns?", []string{"he/him", "she/her", "they/them", "Prefer not to say"}, nil, benchNone},
		{"Do you hold an active security clearance?", yn, []string{"The applicant has no security clearance."}, "No"},
		{"Which language do you know best?", []string{"Python", "Java", "Go", "Ruby"}, []string{"The applicant's strongest language is Go."}, "Go"},
		{"Are you at least 18 years old?", yn, []string{"The applicant is 34 years old."}, "Yes"},
		{"Have you previously worked for Example Corp?", yn, []string{"The applicant has never worked for Example Corp."}, "No"},
		{"What is your desired salary range?", []string{"Under 80k", "80k to 120k", "120k to 160k", "Over 160k"}, nil, benchNone},
		{"What work arrangement do you prefer?", []string{"Remote", "Hybrid", "On-site"}, []string{"The applicant wants a fully remote role."}, "Remote"},
	}
}

// ParseBenchCases reads a JSON array of cases and checks each one.
func ParseBenchCases(data []byte) ([]BenchCase, error) {
	var cases []BenchCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("the cases file is not a JSON array of cases: %w", err)
	}
	return cases, checkBenchCases(cases)
}

func checkBenchCases(cases []BenchCase) error {
	if len(cases) == 0 || len(cases) > maxBenchCases {
		return fmt.Errorf("a bench takes 1 to %d cases, got %d", maxBenchCases, len(cases))
	}
	for i, c := range cases {
		if strings.TrimSpace(c.Question) == "" || len(c.Options) < 2 {
			return fmt.Errorf("case %d needs a question and at least two options", i+1)
		}
		if c.Expect != benchNone && !slices.Contains(c.Options, c.Expect) {
			return fmt.Errorf("case %d expects %q, which is not one of its options (use %s for no answer)", i+1, c.Expect, benchNone)
		}
	}
	return nil
}

func benchPrompt(c BenchCase) string {
	var b strings.Builder
	b.WriteString("=== VERIFIED FACTS (the applicant's own; cite by id) ===\n")
	if len(c.Facts) == 0 {
		b.WriteString("(none)\n")
	}
	for i, f := range c.Facts {
		fmt.Fprintf(&b, "[f%d] %s\n", i+1, f)
	}
	fmt.Fprintf(&b, "\n=== QUESTION ===\n%s\nOptions:\n", c.Question)
	for _, o := range c.Options {
		fmt.Fprintf(&b, "- %s\n", o)
	}
	return b.String()
}

// Verdicts of one bench case.
const (
	benchCorrect  = "correct"
	benchWrong    = "wrong"
	benchNoAnswer = "no-answer"
)

// benchVerdict scores a reply: the expected option, or an abstention where
// none was expected, is correct; any other option is wrong; an abstention
// where an option was expected is no answer.
func benchVerdict(c BenchCase, text string) string {
	option, abstain := readBenchReply(text)
	switch {
	case abstain || option == "":
		if c.Expect == benchNone {
			return benchCorrect
		}
		return benchNoAnswer
	case c.Expect != benchNone && strings.EqualFold(option, c.Expect):
		return benchCorrect
	}
	return benchWrong
}

func readBenchReply(text string) (option string, abstain bool) {
	text = strings.TrimSpace(text)
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		var raw struct {
			Option  string `json:"option"`
			Abstain bool   `json:"abstain"`
		}
		if json.Unmarshal([]byte(text[i:j+1]), &raw) == nil {
			return strings.TrimSpace(raw.Option), raw.Abstain
		}
	}
	first, _, _ := strings.Cut(text, "\n")
	first = strings.Trim(strings.TrimSpace(first), "\"'`")
	return first, strings.HasPrefix(strings.ToLower(first), "abstain")
}

// rankBench orders results best first: fewest wrong, then most correct, then
// the lowest median latency, then the lowest cost (an unknown cost last).
func rankBench(rs []BenchResult) {
	cost := func(r BenchResult) float64 {
		if r.CostUSD == nil {
			return math.Inf(1)
		}
		return *r.CostUSD
	}
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		switch {
		case a.Wrong != b.Wrong:
			return a.Wrong < b.Wrong
		case a.Correct != b.Correct:
			return a.Correct > b.Correct
		case a.P50Ms != b.P50Ms:
			return a.P50Ms < b.P50Ms
		}
		return cost(a) < cost(b)
	})
}

// Bench runs the cases through each candidate in turn, one call at a time so
// latencies are comparable, and saves the result as the task's latest bench.
func (r *Router) Bench(ctx context.Context, task string, cands []Fallback, cases []BenchCase) (BenchRun, error) {
	if task != benchTask {
		return BenchRun{}, fmt.Errorf("bench supports the %s task only", benchTask)
	}
	if err := checkBenchCases(cases); err != nil {
		return BenchRun{}, err
	}
	if len(cands) == 0 || len(cands) > maxBenchCands {
		return BenchRun{}, fmt.Errorf("a bench takes 1 to %d candidates, got %d", maxBenchCands, len(cands))
	}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return BenchRun{}, err
	}
	for _, c := range cands {
		if _, ok := s.Provider(c.Provider); !ok {
			return BenchRun{}, fmt.Errorf("provider %q is not configured", c.Provider)
		}
		if err := ValidModelID(c.Model); err != nil {
			return BenchRun{}, err
		}
	}
	run := BenchRun{Task: task, At: timeText(r.now()), Cases: len(cases)}
	for _, cand := range cands {
		res := BenchResult{Provider: cand.Provider, Model: cand.Model}
		var lat []time.Duration
		var cost float64
		priced := 0
		for _, c := range cases {
			start := time.Now()
			resp, err := r.Complete(ctx, Request{Task: task, System: benchOptionRule, Prompt: benchPrompt(c), Model: cand.Provider + "/" + cand.Model, RunID: benchRunID})
			if err != nil {
				if ctx.Err() != nil {
					return BenchRun{}, ctx.Err()
				}
				res.NoAnswer++
				res.Errors++
				continue
			}
			lat = append(lat, time.Since(start))
			if resp.CostUSD != nil {
				cost, priced = cost+*resp.CostUSD, priced+1
			}
			switch benchVerdict(c, resp.Text) {
			case benchCorrect:
				res.Correct++
			case benchWrong:
				res.Wrong++
			default:
				res.NoAnswer++
			}
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		res.P50Ms, res.P95Ms = percentile(lat, 0.50).Milliseconds(), percentile(lat, 0.95).Milliseconds()
		if priced > 0 {
			c := roundMicro(cost)
			res.CostUSD = &c
		}
		run.Results = append(run.Results, res)
	}
	rankBench(run.Results)
	return run, r.saveBench(ctx, run)
}

func (r *Router) saveBench(ctx context.Context, run BenchRun) error {
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return r.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO llm_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, benchKeyPrefix+run.Task, string(b), timeText(r.now()))
		return err
	})
}

// BenchLatest returns the latest bench of each task that has one.
func (r *Router) BenchLatest(ctx context.Context) (map[string]BenchRun, error) {
	rows, err := r.store.Read().QueryContext(ctx, `SELECT value FROM llm_settings WHERE key LIKE ?`, benchKeyPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]BenchRun{}
	for rows.Next() {
		var v string
		var run BenchRun
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(v), &run) == nil && run.Task != "" {
			out[run.Task] = run
		}
	}
	return out, rows.Err()
}

// ApplyBench routes the run's winner as the task's primary. The task keeps the
// rest of its row (thinking, limits and fallbacks, minus the winner). A winner
// that got no case right is refused.
func (r *Router) ApplyBench(ctx context.Context, run BenchRun) (Fallback, error) {
	if len(run.Results) == 0 || run.Results[0].Correct == 0 {
		return Fallback{}, errors.New("no candidate answered a case correctly; the route is unchanged")
	}
	w := run.Results[0]
	win := Fallback{Provider: w.Provider, Model: w.Model}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return Fallback{}, err
	}
	row, ok := s.Routes[run.Task]
	if res, err := r.resolveWith(s, run.Task); !ok || err != nil || res.Source != SourceTable {
		row = Route{}
		if err == nil {
			row.Fallbacks = res.Fallbacks
		}
	}
	row.Provider, row.Model = win.Provider, win.Model
	row.Fallbacks = slices.DeleteFunc(slices.Clone(row.Fallbacks), func(f Fallback) bool { return f == win })
	return win, r.PatchRoutes(ctx, map[string]*Route{run.Task: &row})
}
