package llm

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// capEnv has an openai-preset provider "a" over a recording server, so gpt-6.1-sol
// (the primary) and gpt-6-luna (the preset's fast model) are both priced.
func capEnv(t *testing.T, opts ...Option) (*env, *oaServer) {
	t.Helper()
	s := newOAServer(t, okHandler("ok"))
	e := newEnv(t, opts...)
	p := oaProvider("a", s)
	p.Preset = "openai"
	e.provider(p, fakeKey("cp"))
	e.route("narrative", Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff})
	e.route("apply-check", Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff})
	e.route("gmail-digest", Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff})
	return e, s
}

// addSpend records metered spend of narrative in today's ledger.
func addSpend(t *testing.T, e *env, usd float64) { seedSpend(t, e, "narrative", e.clock.now(), usd) }

// seedSpend records a task's metered spend at a time.
func seedSpend(t *testing.T, e *env, task string, at time.Time, usd float64) {
	t.Helper()
	err := e.st.Write(bg(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, cost_usd, billing)
			VALUES ('seed', ?, ?, 'a', 'openai-compatible', 'gpt-6.1-sol', 1, 'primary', 'ok', ?, 'metered')`, timeText(at), task, usd)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Unattended evaluation waits while a task an evaluation calls has no model;
// a route for each of them, or the offline simulation, ends the wait.
func TestLLM_EvaluationUnrouted_NamesATaskUntilEachHasAModel(t *testing.T) {
	e, _ := capEnv(t)
	row := Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff}
	for _, step := range []struct{ route, want string }{{"", "jd-extract"}, {"jd-extract", "quick-screen"}, {"quick-screen", "deep-eval"}, {"deep-eval", ""}} {
		if step.route != "" {
			e.route(step.route, row)
		}
		if got := e.r.EvaluationUnrouted(bg()); got != step.want {
			t.Fatalf("after routing %q: %q, want %q", step.route, got, step.want)
		}
	}
	if got := newEnv(t, WithOfflineSimulation()).r.EvaluationUnrouted(bg()); got != "" {
		t.Fatalf("offline: %q", got)
	}
}

func TestLLM_Cap_DefaultTwoDollarsZeroDisables(t *testing.T) {
	e := newEnv(t)
	if s := e.load(); s.DailyUSDCap != 2.00 {
		t.Fatalf("default cap = %v", s.DailyUSDCap)
	}
	if err := e.r.SetDailyCap(bg(), 0); err != nil {
		t.Fatal(err)
	}
	if s := e.load(); s.DailyUSDCap != 0 {
		t.Fatalf("cap = %v", s.DailyUSDCap)
	}
}

func TestLLM_Downgrade_CapReached(t *testing.T) {
	e, models := capEnv(t)
	addSpend(t, e, 2.05)
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if models.lastModel() != "gpt-6-luna" || !resp.Downgraded {
		t.Fatalf("served by %s, downgraded=%v", models.lastModel(), resp.Downgraded)
	}
	if rows := e.ledger(); !rows[len(rows)-1].Downgraded {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Downgrade_ExemptTasks(t *testing.T) {
	e, models := capEnv(t)
	addSpend(t, e, 2.05)
	for _, task := range []string{"apply-check", "gmail-digest"} {
		if _, err := e.r.Complete(bg(), Request{Task: task, Prompt: "hi"}); err != nil {
			t.Fatal(err)
		}
		if models.lastModel() != "gpt-6.1-sol" {
			t.Fatalf("%s ran on %s, want its configured route", task, models.lastModel())
		}
	}
	want := map[string]bool{"option-pick": true, "apply-check": true, "apply-complete": true, "apply-page-decision": true, "apply-fix": true, "gmail-digest": true}
	if len(exemptFromDowngrade) != len(want) {
		t.Fatalf("exempt = %v", exemptFromDowngrade)
	}
	for k := range want {
		if !exemptFromDowngrade[k] {
			t.Errorf("%s is not exempt", k)
		}
	}
}

func TestLLM_Downgrade_NothingCheaper(t *testing.T) {
	e, models := capEnv(t)
	e.route("narrative", Route{Provider: "a", Model: "gpt-6-luna", Thinking: ThinkOff})
	addSpend(t, e, 2.05)
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || models.lastModel() != "gpt-6-luna" || resp.Downgraded {
		t.Fatalf("model %s downgraded %v err %v", models.lastModel(), resp.Downgraded, err)
	}
}

func TestLLM_Downgrade_CapDisabled(t *testing.T) {
	e, models := capEnv(t)
	if err := e.r.SetDailyCap(bg(), 0); err != nil {
		t.Fatal(err)
	}
	addSpend(t, e, 500)
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || models.lastModel() != "gpt-6.1-sol" {
		t.Fatalf("model %s err %v", models.lastModel(), err)
	}
}

func TestLLM_Downgrade_UnpricedOrFreePrimaryKeepsTheOrder(t *testing.T) {
	for _, model := range []string{"custom-unpriced", "vendor/model:free"} {
		t.Run(model, func(t *testing.T) {
			e, models := capEnv(t)
			e.route("narrative", Route{Provider: "a", Model: model, Thinking: ThinkOff})
			addSpend(t, e, 2.05)
			if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || models.lastModel() != model {
				t.Fatalf("model %s err %v", models.lastModel(), err)
			}
		})
	}
	t.Run("subscription primary", func(t *testing.T) {
		e := newEnv(t)
		cands := []candidate{{provider: ProviderConfig{ID: "c", Kind: KindCLI, Program: "claude"}, model: "sonnet"}}
		if out := e.r.downgradeOrder(bg(), e.load(), cands); out[0].downgraded {
			t.Fatal("a subscription primary was downgraded")
		}
	})
}

func TestLLM_Downgrade_StrictlyCheaperPaidOnly(t *testing.T) {
	e := newEnv(t)
	oa := ProviderConfig{ID: "a", Kind: KindOpenAI, BaseURL: "http://127.0.0.1:1", Preset: "openai"}
	ds := ProviderConfig{ID: "d", Kind: KindOpenAI, BaseURL: "http://127.0.0.1:1", Preset: "deepseek"}
	s := e.load()
	cands := []candidate{
		{provider: oa, model: "gpt-6.1-sol", source: "primary"},
		{provider: ds, model: "deepseek-flash", source: "fallback"},
		{provider: oa, model: "gpt-6.1-sol", source: "fallback"}, // as expensive as the primary: not a target
		{provider: oa, model: "x:free", source: "fallback"},
	}
	out := e.r.downgradeOrder(bg(), s, cands)
	// gpt-6-luna (the preset's fast model, 0.60 per pair) vs deepseek-flash (1.50): the cheapest wins.
	// Candidates dearer than the head are dropped; the configured primary stays last, for an auth failure.
	if len(out) != 2 || out[0].model != "gpt-6-luna" || !out[0].downgraded || out[0].authOnly ||
		out[1].model != "gpt-6.1-sol" || out[1].downgraded || !out[1].authOnly {
		t.Fatalf("order = %+v", out)
	}
	for _, c := range out {
		if c.downgraded && isFreeModel(c.model) {
			t.Fatal("a free-tier id became the downgrade head")
		}
	}
}

func TestLLM_Downgrade_AuthOnHeadContinuesToPrimary(t *testing.T) {
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		var b struct{ Model string }
		_ = decodeBody(r, &b)
		if b.Model == "gpt-6-luna" {
			oaFail(w, 401, `{}`)
			return
		}
		oaStream(w, "stop", 1, 1, 0, "ok")
	})
	e := newEnv(t)
	p := oaProvider("a", s)
	p.Preset = "openai"
	e.provider(p, fakeKey("ah"))
	e.route("narrative", Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff})
	addSpend(t, e, 2.05)
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Model != "gpt-6.1-sol" || strings.Join(s.models, ",") != "gpt-6-luna,gpt-6.1-sol" {
		t.Fatalf("resp %+v err %v models %v", resp, err, s.models)
	}
	// An auth failure on the configured primary ends the call.
	e2, _, _ := pair(t, statusHandler(401, `{}`), okHandler("b"))
	if _, err := e2.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); KindOf(err) != KindAuth {
		t.Fatalf("err = %v", err)
	}
}

func TestLLM_Ledger_DowngradedFlagOnlyWhenHeadChanged(t *testing.T) {
	e, _ := capEnv(t)
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	addSpend(t, e, 2.05)
	e.clock.advance(20 * time.Second)
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	var flags []bool
	for _, r := range e.ledger() {
		if r.Outcome == "ok" && r.Task == "narrative" && r.Provider == "a" && r.Input > 0 {
			flags = append(flags, r.Downgraded)
		}
	}
	if len(flags) != 2 || flags[0] || !flags[1] {
		t.Fatalf("flags = %v", flags)
	}
}

func TestLLM_Cap_OverrideRunsAsTold(t *testing.T) {
	e, models := capEnv(t)
	addSpend(t, e, 2.05)
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", Model: "a/gpt-6.1-sol"})
	if err != nil || models.lastModel() != "gpt-6.1-sol" || resp.Downgraded {
		t.Fatalf("model %s err %v", models.lastModel(), err)
	}
}

func TestLLM_Cap_RaisedCapLiftsDowngradeWithin15s(t *testing.T) {
	e, models := capEnv(t)
	addSpend(t, e, 2.05)
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || models.lastModel() != "gpt-6-luna" {
		t.Fatalf("model %s err %v", models.lastModel(), err)
	}
	if err := e.r.SetDailyCap(bg(), 10); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(5 * time.Second) // inside the refresh window: the old read is reused
	_, _ = e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if models.lastModel() != "gpt-6-luna" {
		t.Fatalf("the cap was re-read inside 15 s: %s", models.lastModel())
	}
	e.clock.advance(11 * time.Second)
	_, _ = e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if models.lastModel() != "gpt-6.1-sol" {
		t.Fatalf("the raised cap was not applied after the refresh: %s", models.lastModel())
	}
}

func TestLLM_Cap_SumsTheDayMeteredOnly(t *testing.T) {
	e, _ := capEnv(t)
	add := func(started time.Time, billing string, cost any) {
		err := e.st.Write(bg(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, cost_usd, billing)
				VALUES ('x', ?, 'narrative', 'a', 'k', 'm', 1, 'primary', 'ok', ?, ?)`, timeText(started), cost, billing)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	now := e.clock.now()
	add(now, "metered", 0.50)
	add(now.Add(-time.Hour), "metered", 0.25)
	add(now, "unpriced", nil)
	add(now, "subscription", 0.0)
	add(now.Add(-24*time.Hour), "metered", 9.0) // yesterday
	add(now.Add(13*time.Hour), "metered", 7.0)  // tomorrow
	if spend := e.r.spend(bg(), true).total; spend != 0.75 {
		t.Fatalf("spend = %v", spend)
	}
}

// The cap's day runs from local midnight to the next local midnight in the
// clock's zone, so a 23- or 25-hour day around a daylight-saving change counts
// whole, and the first read after midnight starts the new day even inside the
// 15-second reuse window.
func TestLLM_Cap_DayIsLocalMidnightToMidnight(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	at := func(mo time.Month, d, h, m int) time.Time { return time.Date(2026, mo, d, h, m, 0, 0, ny) }
	for _, c := range []struct {
		name                      string
		before, first, last, next time.Time
	}{
		{"spring forward, 23 hours", at(3, 7, 23, 30), at(3, 8, 0, 30), at(3, 8, 23, 30), at(3, 9, 0, 30)},
		{"fall back, 25 hours", at(10, 31, 23, 30), at(11, 1, 0, 30), at(11, 1, 23, 30), at(11, 2, 0, 30)},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := capEnv(t)
			if err := e.r.SetDailyCap(bg(), 3); err != nil {
				t.Fatal(err)
			}
			seedSpend(t, e, "narrative", c.before, 8)
			seedSpend(t, e, "narrative", c.first, 1)
			seedSpend(t, e, "narrative", c.last, 0.5)
			seedSpend(t, e, "narrative", c.next, 4)
			e.clock.t = c.last.Add(29*time.Minute + 55*time.Second) // 23:59:55
			if spend, _ := e.r.DailySpend(bg()); spend != 1.5 || e.r.EvaluationHeld(bg()) {
				t.Fatalf("23:59:55: spend %v, held %v; want 1.5 under the cap", spend, e.r.EvaluationHeld(bg()))
			}
			e.clock.advance(10 * time.Second)
			if spend, _ := e.r.DailySpend(bg()); spend != 4 || !e.r.EvaluationHeld(bg()) {
				t.Fatalf("00:00:05: spend %v, held %v; want the new day's 4 over the cap", spend, e.r.EvaluationHeld(bg()))
			}
		})
	}
}

// A task at or over its own daily budget behaves as every task does over the
// cap: its calls move to the cheapest model, and when it is an evaluation
// task, unattended evaluation waits. Another task's spend does not count.
func TestLLM_Budget_AtOrOverMovesTheTaskAndHoldsEvaluation(t *testing.T) {
	for _, c := range []struct {
		name  string
		tasks []string
		spent float64
		want  bool
	}{
		{"under", []string{"narrative", "deep-eval"}, 0.99, false},
		{"at", []string{"narrative", "deep-eval"}, 1, true},
		{"over", []string{"narrative", "deep-eval"}, 1.5, true},
		{"another task over", []string{"cover-letter"}, 5, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, models := capEnv(t)
			if err := e.r.SetDailyCap(bg(), 0); err != nil {
				t.Fatal(err)
			}
			if err := e.r.SetBudgets(bg(), map[string]float64{"narrative": 1, "deep-eval": 1}); err != nil {
				t.Fatal(err)
			}
			for _, task := range c.tasks {
				seedSpend(t, e, task, e.clock.now(), c.spent)
			}
			resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Downgraded != c.want || (models.lastModel() == "gpt-6-luna") != c.want {
				t.Errorf("served by %s, downgraded %v; want downgraded %v", models.lastModel(), resp.Downgraded, c.want)
			}
			if held := e.r.EvaluationHeld(bg()); held != c.want {
				t.Errorf("evaluation held %v, want %v", held, c.want)
			}
		})
	}
}

type sinkRec struct {
	mu     sync.Mutex
	alerts []CapAlert
	accept bool
}

func (s *sinkRec) sink(_ context.Context, a CapAlert) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, a)
	return s.accept
}

func (s *sinkRec) n() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.alerts) }

func TestLLM_CapAlert_OncePerDayReleaseRetry10m(t *testing.T) {
	rec := &sinkRec{accept: true}
	e, _ := capEnv(t, WithAlertSink(rec.sink))
	addSpend(t, e, 2.05)
	for i := 0; i < 50; i++ {
		e.clock.advance(20 * time.Second) // force a re-read each call
		if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	if rec.n() != 1 {
		t.Fatalf("alerts = %d, want 1", rec.n())
	}
	a := rec.alerts[0]
	if a.CapUSD != 2 || a.SpendUSD < 2.05 || !strings.Contains(a.Message, "cheaper models") || !strings.Contains(a.Message, "daily_usd_cap") {
		t.Fatalf("alert = %+v", a)
	}
	t.Run("new day", func(t *testing.T) {
		e.clock.advance(24 * time.Hour)
		addSpend(t, e, 2.5)
		e.r.Tick(bg())
		if rec.n() != 2 {
			t.Fatalf("alerts = %d, want a new one on a new day", rec.n())
		}
	})
}

func TestLLM_CapAlert_NonDeliveringProcessDoesNotClaim(t *testing.T) {
	e, _ := capEnv(t) // no sink: a CLI or job process
	addSpend(t, e, 2.05)
	e.r.Tick(bg())
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_cap_alerts`).Scan(&n)
	if n != 0 {
		t.Fatal("a process without a sink claimed the alert")
	}
	// The server, with a sink, still can.
	rec := &sinkRec{accept: true}
	server := New(e.st, e.vault, WithClock(e.clock.now), WithAlertSink(rec.sink))
	server.Tick(bg())
	if rec.n() != 1 {
		t.Fatalf("server alerts = %d", rec.n())
	}
}

func TestLLM_CapAlert_PanickingSinkNeverFailsACall(t *testing.T) {
	e, _ := capEnv(t, WithAlertSink(func(context.Context, CapAlert) bool { panic("boom") }))
	addSpend(t, e, 3)
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
}

// A head that fails with a fallback kind must not hand the call to the
// configured primary: that would serve an over-cap call from the dear model.
func TestLLM_Downgrade_CapIsNotBypassedThroughFallback(t *testing.T) {
	for _, status := range []int{429, 503, 500, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
				var b struct{ Model string }
				_ = decodeBody(r, &b)
				if b.Model == "gpt-6-luna" {
					oaFail(w, status, `{}`)
					return
				}
				oaStream(w, "stop", 1, 1, 0, "dear model answered")
			})
			e := newEnv(t)
			p := oaProvider("a", s)
			p.Preset = "openai"
			e.provider(p, fakeKey("bp"))
			e.route("narrative", Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff})
			addSpend(t, e, 2.05)
			_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
			if err == nil {
				t.Fatal("the call was served although the head failed")
			}
			if strings.Join(s.models, ",") != "gpt-6-luna" {
				t.Fatalf("models called = %v, want only the cheap head", s.models)
			}
			for _, r := range e.ledger() {
				if r.Task == "narrative" && r.Input == 0 && r.Outcome != "ok" && !r.Downgraded {
					t.Fatalf("a downgraded attempt is not marked: %+v", r)
				}
			}
		})
	}
}

func TestLLM_Downgrade_NoDearerCandidateServesAnOverCapCall(t *testing.T) {
	e := newEnv(t)
	cheap := newOAServer(t, statusHandler(503, `{}`))
	mid := newOAServer(t, okHandler("mid answered"))
	dear := newOAServer(t, okHandler("dear answered"))
	for id, s := range map[string]*oaServer{"cheap": cheap, "mid": mid, "dear": dear} {
		p := oaProvider(id, s)
		p.Preset = "openai"
		e.provider(p, fakeKey("nd"))
	}
	// gpt-6-luna is cheaper than gpt-6.1-sol; the fallback "mid" runs the same
	// cheap model on another endpoint, so it is no dearer than the head.
	e.route("narrative", Route{Provider: "dear", Model: "gpt-6.1-sol", Thinking: ThinkOff,
		Fallbacks: []Fallback{{"cheap", "gpt-6-luna"}, {"mid", "gpt-6-luna"}}})
	addSpend(t, e, 2.05)
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "mid" || !resp.Downgraded || dear.count() != 0 {
		t.Fatalf("resp %+v err %v dear calls %d", resp, err, dear.count())
	}
}

func TestLLM_Downgrade_NeverFreeTier(t *testing.T) {
	e := newEnv(t)
	p := ProviderConfig{ID: "a", Kind: KindOpenAI, BaseURL: "http://127.0.0.1:1", Preset: "openai"}
	e.provider(p, "")
	// A user price makes the free-tier id the cheapest candidate on paper; it
	// must still never become the head, and an unpriced id is no target either.
	if err := e.r.SetPrice(bg(), "a", "vendor/cheap:free", Price{InputPerM: 0.01, OutputPerM: 0.01}); err != nil {
		t.Fatal(err)
	}
	cands := []candidate{
		{provider: p, model: "gpt-6.1-sol", source: "primary"},
		{provider: p, model: "vendor/cheap:free", source: "fallback"},
		{provider: p, model: "vendor/unpriced", source: "fallback"},
	}
	for _, c := range e.r.downgradeOrder(bg(), e.load(), cands) {
		if c.downgraded && (isFreeModel(c.model) || c.model == "vendor/unpriced") {
			t.Fatalf("downgrade landed on %s", c.model)
		}
	}
	out := e.r.downgradeOrder(bg(), e.load(), cands[:2])
	if out[0].model != "gpt-6-luna" {
		t.Fatalf("head = %s, want the preset's fast model, not the free tier", out[0].model)
	}
}
