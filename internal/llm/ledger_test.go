package llm

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLLM_Ledger_RowPerAttemptWithFallbackFrom(t *testing.T) {
	e, _, _ := pair(t, statusHandler(429, `{}`), okHandler("from b"))
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", RunID: "run_1"}); err != nil {
		t.Fatal(err)
	}
	var runID, kind, finish string
	var in, out, latency int
	err := e.st.Read().QueryRow(`SELECT run_id, provider_kind, finish_reason, input_tokens, output_tokens, latency_ms FROM llm_ledger WHERE attempt = 2`).
		Scan(&runID, &kind, &finish, &in, &out, &latency)
	if err != nil || runID != "run_1" || kind != KindOpenAI || finish != "stop" || in != 10 || out != 5 {
		t.Fatalf("row = %q %q %q %d %d %v", runID, kind, finish, in, out, err)
	}
	rows := e.ledger()
	if len(rows) != 2 || rows[0].Attempt != 1 || rows[1].Attempt != 2 || rows[1].Replaced != "a/m1" {
		t.Fatalf("ledger = %+v", rows)
	}
	var calls int
	_ = e.st.Read().QueryRow(`SELECT count(DISTINCT call_id) FROM llm_ledger`).Scan(&calls)
	if calls != 1 {
		t.Fatalf("the attempts of one call must share a call id, got %d ids", calls)
	}
}

// The call's error and the ledger keep the two apart, each from the cause the
// attempt was cut with; TestLLM_Walk_TimeoutAdvances covers the walk moving on.
func TestLLM_Ledger_TimeoutAndStallDistinct(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request, int)
		stall   time.Duration
		want    Kind
	}{
		{"silent stream", func(w http.ResponseWriter, r *http.Request, n int) { <-r.Context().Done() }, 100 * time.Millisecond, KindStreamStalled},
		{"live stream past the row timeout", keepAlive, time.Minute, KindTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.provider(oaProvider("a", newOAServer(t, tc.handler)), fakeKey("ka"))
			e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff, TimeoutSeconds: 1})
			e.r.stall = tc.stall
			_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
			wantKind(t, err, tc.want)
			if rows := e.ledger(); len(rows) != 1 || rows[0].Outcome != string(tc.want) {
				t.Fatalf("ledger = %+v", rows)
			}
		})
	}
}

func TestLLM_Ledger_WriteFailureDoesNotFailCall(t *testing.T) {
	e, _, _ := pair(t, okHandler("fine"), okHandler("x"))
	if err := e.st.Write(bg(), func(tx *sql.Tx) error { _, err := tx.Exec(`DROP TABLE llm_ledger`); return err }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Text != "fine" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if time.Since(start) > ledgerWriteTimeout {
		t.Fatal("the ledger failure delayed the call")
	}
}

func TestLLM_Ledger_CLIProcessWritesWorkspaceDB(t *testing.T) {
	e, _, _ := pair(t, okHandler("from a"), okHandler("x"))
	cli := New(e.st, e.vault, WithGetenv(func(k string) string { return e.envs[k] }), WithClock(e.clock.now))
	if _, err := cli.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if rows := e.ledger(); len(rows) != 1 {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Ledger_NoPromptBodiesByDefault(t *testing.T) {
	e, _, _ := pair(t, okHandler("completion-marker-xyz"), okHandler("x"))
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "prompt-marker-xyz"}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_capture`).Scan(&n)
	if n != 0 {
		t.Fatal("bodies were captured with capture off")
	}
	rows, _ := e.st.Read().Query(`SELECT call_id||task||provider_id||model||outcome||finish_reason||run_id||error_text FROM llm_ledger`)
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		if strings.Contains(s, "marker-xyz") {
			t.Fatalf("a ledger text column holds prompt or completion text: %s", s)
		}
	}
}

func TestLLM_DebugCapture_ExpiresAndRedacts(t *testing.T) {
	e, _, _ := pair(t, okHandler("completion-marker"), okHandler("x"))
	if err := e.r.SetDebugCapture(bg(), 10); err != nil {
		t.Fatal(err)
	}
	secret := fakeKey("pr")
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "use " + secret + " now"}); err != nil {
		t.Fatal(err)
	}
	var prompt, completion string
	if err := e.st.Read().QueryRow(`SELECT prompt, completion FROM llm_capture`).Scan(&prompt, &completion); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, secret) || !strings.Contains(prompt, redacted) || completion != "completion-marker" {
		t.Fatalf("captured prompt %q completion %q", prompt, completion)
	}
	e.clock.advance(11 * time.Minute)
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "later"}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_capture`).Scan(&n)
	if n != 1 {
		t.Fatalf("captured rows = %d, capture did not expire", n)
	}
	// Captured rows go after 24 hours; ledger rows after 180 days.
	e.clock.advance(25 * time.Hour)
	if err := e.r.Prune(bg()); err != nil {
		t.Fatal(err)
	}
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_capture`).Scan(&n)
	var ledger int
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_ledger`).Scan(&ledger)
	if n != 0 || ledger != 2 {
		t.Fatalf("after prune: capture %d ledger %d", n, ledger)
	}
	e.clock.advance(181 * 24 * time.Hour)
	_ = e.r.Prune(bg())
	_ = e.st.Read().QueryRow(`SELECT count(*) FROM llm_ledger`).Scan(&ledger)
	if ledger != 0 {
		t.Fatalf("ledger rows after 180 days = %d", ledger)
	}
}

func TestLLM_CostReport_Columns(t *testing.T) {
	e, _, _ := pair(t, statusHandler(429, `{}`), okHandler("from b"))
	for i := 0; i < 2; i++ {
		if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
			t.Fatal(err)
		}
		e.clock.advance(2 * time.Minute) // past the rest the 429 set
	}
	byTask, byProv, err := e.r.CostReport(bg(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(byTask) != 1 || byTask[0].Name != "narrative" || byTask[0].Calls != 2 || byTask[0].SuccessRate != 1 ||
		byTask[0].FallbackRate != 1 || byTask[0].Unpriced != 4 {
		t.Fatalf("by task = %+v", byTask)
	}
	if len(byProv) != 2 || byProv[0].Name != "a" || byProv[0].SuccessRate != 0 || byProv[1].SuccessRate != 1 || byProv[1].FallbackRate != 1 {
		t.Fatalf("by provider = %+v", byProv)
	}
}

// Ten attempts of one task on one provider: seven successes of 100..700 ms
// (1000 input tokens each, 300 of them cached), two rate-limited refusals
// and one timeout. By hand: p50 is the 4th success (400), p95 the 7th (700),
// 7 of 10 succeed, 2 of 10 are rate-limited, 2100 of 7000 input tokens hit
// the cache. A failure's 50 ms would move p50 if failures were counted. An
// eleventh attempt the offline simulation had no script for asked no model
// and changes none of these.
func TestLLM_CostReport_HealthFromKnownCalls(t *testing.T) {
	e := newEnv(t)
	at := timeText(e.clock.now())
	add := func(id, outcome string, ms, input, cached int) {
		t.Helper()
		if err := e.st.Write(bg(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source,
				outcome, input_tokens, cached_input_tokens, billing, latency_ms) VALUES (?,?,?,?,?,?,1,'primary',?,?,?,'metered',?)`,
				id, at, "narrative", "a", "fake", "m", outcome, input, cached, ms)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 7; i++ {
		add("ok"+strconv.Itoa(i), "ok", i*100, 1000, 300)
	}
	add("rl1", "rate-limited", 50, 0, 0)
	add("rl2", "rate-limited", 50, 0, 0)
	add("to", "timeout", 50, 0, 0)
	add("nf", string(KindFakeFixture), 0, 0, 0)
	byTask, byProv, err := e.r.CostReport(bg(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []ReportRow{byTask[0], byProv[0]} {
		if row.Calls != 10 || row.SuccessRate != 0.7 || row.RateLimited != 0.2 || row.P50 != 400*time.Millisecond || row.P95 != 700*time.Millisecond ||
			row.CacheHit == nil || *row.CacheHit != 0.3 {
			t.Fatalf("%s row = %+v", row.Name, row)
		}
	}
}

// Token totals: two runs of two calls each, one of them retried after an
// aborted attempt, and one call with no run. By hand: input 1000+1000+400+1000
// +500 = 3900, output 200+200+50+200+100 = 750, the aborted attempt wastes
// 400+50 = 450, and 3900+750 over the two runs is 2325 per run.
func TestLLM_CostReport_TokenTotals(t *testing.T) {
	e := newEnv(t)
	at := timeText(e.clock.now())
	add := func(call, run, outcome string, attempt, input, output int) {
		t.Helper()
		if err := e.st.Write(bg(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source,
				outcome, input_tokens, output_tokens, billing, latency_ms, run_id) VALUES (?,?,?,?,?,?,?,'primary',?,?,?,'subscription',100,?)`,
				call, at, "apply-complete", "a", "fake", "m", attempt, outcome, input, output, run)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("c1", "run-1", "ok", 1, 1000, 200)
	add("c2", "run-1", "ok", 1, 1000, 200)
	add("c3", "run-2", "aborted", 1, 400, 50)
	add("c3", "run-2", "ok", 2, 1000, 200)
	add("c4", "", "ok", 1, 500, 100)
	byTask, byProv, err := e.r.CostReport(bg(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []ReportRow{byTask[0], byProv[0]} {
		if row.InputTokens != 3900 || row.OutputTokens != 750 || row.WastedTokens != 450 || row.Runs != 2 || row.PerRunTokens() != 2325 {
			t.Fatalf("%s row = %+v", row.Name, row)
		}
	}
	if byTask[0].Calls != 4 || byProv[0].Calls != 5 {
		t.Fatalf("calls: task %d, provider %d", byTask[0].Calls, byProv[0].Calls)
	}
}

// A provider is refused when at least three quota or 401 replies follow its
// last success; other failures neither count nor end the run, and a success
// ends it.
func TestLLM_Ledger_RefusedProvidersSinceTheLastSuccess(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	n := 0
	write := func(provider, task, outcome string, status int) {
		n++
		e.r.writeLedger(bg(), LedgerRow{CallID: "c" + strconv.Itoa(n), StartedAt: at.Add(time.Duration(n) * time.Minute), Task: task,
			ProviderID: provider, ProviderKind: KindOpenAI, Model: "m", Attempt: 1, CandidateSource: "primary", Billing: "metered",
			Outcome: outcome, HTTPStatus: status}, nil, false)
	}
	// openrouter: out of credit after a success, a network blip in between.
	write("openrouter", "embedding", "ok", 0)
	write("openrouter", "embedding", string(KindQuota), 403)
	write("openrouter", "embedding", string(KindNetwork), 0)
	write("openrouter", "option-pick", string(KindQuota), 403)
	write("openrouter", "embedding", string(KindQuota), 403)
	// dead: three 401s.
	for range 3 {
		write("dead", "narrative", string(KindAuth), 401)
	}
	// healed: refused three times, then a success.
	for range 3 {
		write("healed", "narrative", string(KindQuota), 402)
	}
	write("healed", "narrative", "ok", 0)
	// forbidden: a 403 that is not out of credit is no refusal of the key.
	for range 3 {
		write("forbidden", "narrative", string(KindAuth), 403)
	}
	// short: two refusals are not yet a run.
	write("short", "narrative", string(KindQuota), 402)
	write("short", "narrative", string(KindQuota), 402)

	got, err := e.r.RefusedProviders(bg(), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("refused = %+v", got)
	}
	or, dead := got[0], got[1]
	if or.Provider != "openrouter" || or.Why != RestQuota || or.Calls != 3 || !or.Since.Equal(at.Add(2*time.Minute)) ||
		strings.Join(or.Tasks, ",") != "embedding,option-pick" {
		t.Errorf("openrouter = %+v", or)
	}
	if dead.Provider != "dead" || dead.Why != RestDeadKey || dead.Calls != 3 {
		t.Errorf("dead = %+v", dead)
	}
	// Rows before since are not read.
	if got, _ := e.r.RefusedProviders(bg(), at.Add(time.Hour)); len(got) != 0 {
		t.Errorf("a later window = %+v", got)
	}
}
