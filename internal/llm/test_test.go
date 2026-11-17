package llm

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func presetOverServer(t *testing.T, name string, h func(http.ResponseWriter, *http.Request, int)) (*env, *oaServer) {
	t.Helper()
	s := newOAServer(t, h)
	e := newEnv(t)
	pr, _ := PresetByName(name)
	cfg := pr.ProviderConfig()
	cfg.BaseURL = s.URL
	e.provider(cfg, fakeKey("ts"))
	if err := UpdateSettings(bg(), e.st, e.clock.now(), func(st *Settings) error { st.Preset = name; return nil }); err != nil {
		t.Fatal(err)
	}
	return e, s
}

func TestLLM_Test_SavesResultWithoutText(t *testing.T) {
	e, s := presetOverServer(t, "deepseek", okHandler("ok"))
	res, err := e.r.Test(bg(), "", "")
	if err != nil || !res.OK() || res.Provider != "deepseek" || res.Model != "deepseek-flash" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if got, _ := s.last["max_tokens"].(float64); got != 16 {
		t.Fatalf("max_tokens = %v, want 16", s.last["max_tokens"])
	}
	var raw string
	if err := e.st.Read().QueryRow(`SELECT value FROM llm_settings WHERE key = 'last_test'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, testPrompt) || strings.Contains(raw, `"text"`) {
		t.Fatalf("saved record holds text: %s", raw)
	}
	if rows := e.ledger(); len(rows) != 1 || rows[0].Task != testTask {
		t.Fatalf("ledger = %+v", rows)
	}
}

// The owner tries a model id other than the preset's before routing to it.
func TestLLM_Test_AnExplicitModelIsTheOneSentAndARetiredOneIsRefused(t *testing.T) {
	e, s := presetOverServer(t, "deepseek", okHandler("ok"))
	res, err := e.r.Test(bg(), "deepseek", "deepseek-v4-pro")
	if err != nil || !res.OK() || res.Model != "deepseek-v4-pro" || s.last["model"] != "deepseek-v4-pro" {
		t.Fatalf("res = %+v err = %v sent %v", res, err, s.last["model"])
	}
	if res, err := e.r.Test(bg(), "deepseek", "deepseek-chat"); err != nil || res.ErrorClass != ClassSetup || s.calls != 1 {
		t.Fatalf("a retired model: res = %+v err = %v after %d calls", res, err, s.calls)
	}
}

// A test that cannot start still ends with a saved result naming why, which
// is all the page and the command that started it can show.
func TestLLM_Test_ThatCannotStartIsSavedAsFailedWithTheReason(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "bare", Kind: KindFake}, "")
	if _, err := e.r.Test(bg(), "bare", ""); err != nil {
		t.Fatal(err)
	}
	ts, err := e.r.TestStatus(bg())
	if err != nil || ts.Result == nil || ts.Result.Provider != "bare" || ts.Result.OK() ||
		!strings.Contains(ts.Result.Fix, `no model is known for provider "bare"`) {
		t.Fatalf("status = %+v err = %v", ts.Result, err)
	}
}

func TestLLM_Test_ClassifiesFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		class  string
	}{
		{"authentication", 401, `{"error":"bad key"}`, ClassAuth},
		{"rate limit", 429, `{"error":"slow down"}`, ClassRateLimit},
		{"billing status", 402, `{"error":"pay"}`, ClassBilling},
		{"billing text", 429, `{"error":{"message":"You exceeded your current quota"}}`, ClassBilling},
		{"model missing", 404, `{"error":"no such model"}`, ClassModelNotFound},
		{"other", 400, `{"error":"bad shape"}`, ClassOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := presetOverServer(t, "deepseek", statusHandler(c.status, c.body))
			res, err := e.r.Test(bg(), "deepseek", "")
			if err != nil || res.OK() || res.ErrorClass != c.class || res.Fix == "" {
				t.Fatalf("res = %+v err = %v", res, err)
			}
			if strings.Contains(res.Fix, fakeKey("ts")) {
				t.Fatal("the fix text holds the key")
			}
			if ts, _ := e.r.TestStatus(bg()); ts.Result == nil || ts.Result.ErrorClass != c.class {
				t.Fatalf("status = %+v", ts)
			}
		})
	}
}

func TestLLM_Test_TruncatedReplyStillProvesTheRoute(t *testing.T) {
	e, _ := presetOverServer(t, "deepseek", func(w http.ResponseWriter, r *http.Request, n int) { oaStream(w, "length", 10, 16, 0, "o") })
	res, err := e.r.Test(bg(), "", "")
	if err != nil || !res.OK() || res.Note == "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestLLM_Offline_SelectionRoutesEveryTaskToTheFakeProvider(t *testing.T) {
	e := newEnv(t, WithFake(NewFake(Fixture{Task: "quick-screen", Contains: "p", Steps: []Step{{Text: "sim"}}})))
	if err := e.r.SetOffline(bg(), true); err != nil {
		t.Fatal(err)
	}
	resp, err := e.r.Complete(bg(), Request{Task: "quick-screen", Prompt: "p"})
	if err != nil || resp.Provider != offlineProvider {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	res, err := e.r.Test(bg(), "", "")
	if err != nil || !res.OK() || res.Provider != offlineProvider {
		t.Fatalf("offline test = %+v err = %v", res, err)
	}
	// Choosing a real preset leaves offline simulation.
	_ = e.r.SetKey("deepseek", fakeKey("of"))
	if err := e.r.ConfigurePreset(bg(), "deepseek", ConfigureOptions{}); err != nil {
		t.Fatal(err)
	}
	if s := e.load(); s.Offline || s.Preset != "deepseek" {
		t.Fatalf("settings = %+v", s)
	}
}

func TestLLM_Keys_PlaceholdersAndShortValuesAreRejectedBeforeStorage(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"", "short", "has a space in the key 1234", "your_key_here_please", "<paste-your-key-here>", strings.Repeat("x", 20)} {
		if err := e.r.SetKey("deepseek", bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if len(e.mem.Accounts()) != 0 {
		t.Fatal("a rejected key reached the vault")
	}
}

func TestLLM_Bench_RankingOrder(t *testing.T) {
	cost := func(v float64) *float64 { return &v }
	rs := []BenchResult{
		{Model: "slow-cheap", Correct: 10, Wrong: 1, P50Ms: 900, CostUSD: cost(0.01)},
		{Model: "no-wrong-few-right", Correct: 6, Wrong: 0, P50Ms: 300, CostUSD: cost(0.02)},
		{Model: "no-wrong-most-right-slow", Correct: 9, Wrong: 0, P50Ms: 800, CostUSD: cost(0.02)},
		{Model: "no-wrong-most-right-fast-dear", Correct: 9, Wrong: 0, P50Ms: 200, CostUSD: cost(0.09)},
		{Model: "tie-unknown-cost", Correct: 9, Wrong: 0, P50Ms: 200},
		{Model: "tie-cheap", Correct: 9, Wrong: 0, P50Ms: 200, CostUSD: cost(0.01)},
	}
	rankBench(rs)
	var got []string
	for _, r := range rs {
		got = append(got, r.Model)
	}
	want := "tie-cheap,no-wrong-most-right-fast-dear,tie-unknown-cost,no-wrong-most-right-slow,no-wrong-few-right,slow-cheap"
	if strings.Join(got, ",") != want {
		t.Fatalf("order = %s\nwant    %s", strings.Join(got, ","), want)
	}
}

// benchFixtures scripts one model of the fake provider over the first cases:
// reply[i] answers case i, "" scripts a failed call.
func benchFixtures(model string, latency time.Duration, cases []BenchCase, reply []string) []Fixture {
	var fx []Fixture
	for i, c := range cases {
		step := Step{Text: reply[i], Latency: latency}
		if reply[i] == "" {
			step = Step{Fail: KindBadRequest, Status: 400}
		}
		fx = append(fx, Fixture{Task: benchTask, Model: model, Contains: c.Question, Steps: []Step{step}})
	}
	return fx
}

func TestLLM_Bench_ScoresEachCandidateAndPicksTheWinner(t *testing.T) {
	cases := DefaultBenchCases()[:4] // Yes, No, Master's degree, 5-9
	right := []string{`{"option":"Yes"}`, `{"option":"No"}`, `{"option":"Master's degree"}`, `{"option":"5-9"}`}
	var fx []Fixture
	fx = append(fx, benchFixtures("steady", 80*time.Millisecond, cases, right)...)
	fx = append(fx, benchFixtures("quick", 0, cases, right)...)
	fx = append(fx, benchFixtures("sloppy", 0, cases, []string{`{"option":"No"}`, `{"abstain":true}`, `{"option":"Doctorate"}`, ""})...)
	e := newEnv(t, WithFake(NewFake(fx...)))
	e.provider(ProviderConfig{ID: "fk", Kind: KindFake}, "")
	run, err := e.r.Bench(bg(), "option-pick", []Fallback{{"fk", "steady"}, {"fk", "sloppy"}, {"fk", "quick"}}, cases)
	if err != nil {
		t.Fatal(err)
	}
	if run.Cases != 4 || len(run.Results) != 3 {
		t.Fatalf("run = %+v", run)
	}
	var order []string
	for _, r := range run.Results {
		order = append(order, r.Model)
	}
	if strings.Join(order, ",") != "quick,steady,sloppy" {
		t.Fatalf("order = %v (%+v)", order, run.Results)
	}
	q, st, sl := run.Results[0], run.Results[1], run.Results[2]
	if q.Correct != 4 || q.Wrong != 0 || q.NoAnswer != 0 || st.Correct != 4 || st.P50Ms < 70 || q.P50Ms >= st.P50Ms {
		t.Fatalf("quick = %+v steady = %+v", q, st)
	}
	if sl.Correct != 0 || sl.Wrong != 2 || sl.NoAnswer != 2 || sl.Errors != 1 {
		t.Fatalf("sloppy = %+v", sl)
	}
	if q.CostUSD == nil || *q.CostUSD != 0 {
		t.Fatalf("a free provider's cost = %v", q.CostUSD)
	}
	// Every call went through the router and is in the ledger under its task.
	var n int
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_ledger WHERE task = 'option-pick' AND run_id = 'bench'`).Scan(&n)
	if n != 12 {
		t.Fatalf("ledger rows = %d, want 12", n)
	}
	latest, err := e.r.BenchLatest(bg())
	if err != nil || len(latest["option-pick"].Results) != 3 || latest["option-pick"].Results[0].Model != "quick" {
		t.Fatalf("latest = %+v err = %v", latest, err)
	}
}

func TestLLM_Bench_ApplySetsTheWinnerAndKeepsTheRestOfTheRow(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "fk", Kind: KindFake}, "")
	e.route("option-pick", Route{Provider: "fk", Model: "old", Thinking: ThinkLow, TimeoutSeconds: 30, Fallbacks: []Fallback{{"fk", "new"}, {"fk", "other"}}})
	run := BenchRun{Task: "option-pick", Cases: 4, Results: []BenchResult{{Provider: "fk", Model: "new", Correct: 4}, {Provider: "fk", Model: "old", Correct: 3, Wrong: 1}}}
	won, err := e.r.ApplyBench(bg(), run)
	if err != nil || won.Model != "new" {
		t.Fatalf("won = %+v err = %v", won, err)
	}
	row := e.load().Routes["option-pick"]
	if row.Provider != "fk" || row.Model != "new" || row.Thinking != ThinkLow || row.TimeoutSeconds != 30 || len(row.Fallbacks) != 1 || row.Fallbacks[0].Model != "other" {
		t.Fatalf("row = %+v", row)
	}
	if _, err := e.r.ApplyBench(bg(), BenchRun{Task: "option-pick", Results: []BenchResult{{Provider: "fk", Model: "zero", NoAnswer: 4}}}); err == nil {
		t.Fatal("a winner with no correct answer was applied")
	}
	if got := e.load().Routes["option-pick"].Model; got != "new" {
		t.Fatalf("the refused apply changed the route to %s", got)
	}
}

func TestLLM_Bench_RefusesWhatItCannotMeasure(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "fk", Kind: KindFake}, "")
	cases := DefaultBenchCases()[:1]
	for name, call := range map[string]func() error{
		"another task": func() error { _, err := e.r.Bench(bg(), "narrative", []Fallback{{"fk", "m"}}, cases); return err },
		"no candidate": func() error { _, err := e.r.Bench(bg(), "option-pick", nil, cases); return err },
		"unknown provider": func() error {
			_, err := e.r.Bench(bg(), "option-pick", []Fallback{{"nowhere", "m"}}, cases)
			return err
		},
		"bad model id": func() error { _, err := e.r.Bench(bg(), "option-pick", []Fallback{{"fk", "-x"}}, cases); return err },
		"no cases":     func() error { _, err := e.r.Bench(bg(), "option-pick", []Fallback{{"fk", "m"}}, nil); return err },
	} {
		if call() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := checkBenchCases(DefaultBenchCases()); err != nil {
		t.Fatalf("the built-in cases: %v", err)
	}
	if _, err := ParseBenchCases([]byte(`[{"question":"q","options":["a","b"],"expect":"c"}]`)); err == nil || !strings.Contains(err.Error(), "not one of its options") {
		t.Fatalf("an expectation outside the options: %v", err)
	}
	if got, err := ParseBenchCases([]byte(`[{"question":"q","options":["a","b"],"expect":"NONE"}]`)); err != nil || len(got) != 1 {
		t.Fatalf("a valid file: %v %v", got, err)
	}
}
