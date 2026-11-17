package api

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// seedUsage writes ledger rows for one run, one conversation and one call
// from an earlier day, and returns the run id.
func seedUsage(t *testing.T, h *harness) string {
	t.Helper()
	st := h.srv.store.Load()
	ref, err := st.CreateRun(context.Background(), store.RunRequest{Principal: "p", Operation: "x", Key: "usage-key", Kind: "scan", Input: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(context.Background(), ref.RunID)
	if err != nil {
		t.Fatal(err)
	}
	now := apikit.Stamp(run.CreatedAt).Time.Add(time.Second)
	row := func(call, run, task, provider, outcome string, at time.Time, cost any, errText string) []any {
		return []any{call, at.Format("2006-01-02T15:04:05.000Z"), task, provider, "m", 1, outcome, 120, 30, cost, 700, run, errText}
	}
	rows := [][]any{
		row("c1", ref.RunID, "scan.rank", "alpha", "ok", now, 0.5, ""),
		row("c2", ref.RunID, "scan.rank", "alpha", "timeout", now.Add(2*time.Second), nil, "deadline"),
		row("c3", "ses_chat", "agent-kernel", "beta", "ok", now.Add(time.Second), 0.25, ""),
		row("c4", "", "embedding", "=beta", "ok", now.Add(-48*time.Hour), nil, ""),
	}
	err = st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome,
				input_tokens, output_tokens, cost_usd, billing, latency_ms, run_id, error_text)
				VALUES (?,?,?,?,'openai-compatible',?,1,'primary',?,?,?,?,'metered',?,?,?)`, r[0], r[1], r[2], r[3], r[4], r[6], r[7], r[8], r[9], r[10], r[11], r[12], r[5]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ref.RunID
}

func TestUsageCallsPageAndFilter(t *testing.T) {
	h := newDomainHarness(t)
	seedUsage(t, h)
	get := func(q string) wire.UsageCallPage {
		r := h.get("/api/llm/calls" + q)
		var env wire.Success[wire.UsageCallPage]
		strictInto(t, r.body, &env)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", q, r.StatusCode, r.body)
		}
		return env.Data
	}
	if p := get(""); p.Total != 4 || len(p.Calls) != 4 || p.Calls[0].Task != "scan.rank" || p.Calls[0].Outcome != "timeout" {
		t.Fatalf("all = %+v", p)
	}
	if p := get("?pageSize=2&page=2"); p.Total != 4 || len(p.Calls) != 2 {
		t.Errorf("second page = %+v", p)
	}
	if p := get("?task=scan.rank&outcome=failed"); p.Total != 1 || p.Calls[0].CostUSD != nil || p.Calls[0].InputTokens != 120 || p.Calls[0].LatencyMs != 700 {
		t.Errorf("failed scan.rank = %+v", p)
	}
	if r := h.get("/api/llm/calls?from=yesterday"); r.StatusCode != 422 {
		t.Errorf("from=yesterday: %d", r.StatusCode)
	}
}

// The live view follows the ledger by id: after returns what was written past
// a cursor, in write order whatever the attempts' start times say, bounded by
// pageSize, so a client that was away catches up a page at a time.
func TestUsageCallsAfterCursorFollowsWriteOrder(t *testing.T) {
	h := newDomainHarness(t)
	seedUsage(t, h) // ids 1..4; c4 started two days before the rest, c3 before c2
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE llm_ledger SET cached_input_tokens = 100, ttft_ms = 250 WHERE id = 3`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(q string) []int {
		r := h.get("/api/llm/calls" + q)
		var env wire.Success[wire.UsageCallPage]
		strictInto(t, r.body, &env)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", q, r.StatusCode, r.body)
		}
		var out []int
		for _, c := range env.Data.Calls {
			out = append(out, int(c.ID))
		}
		return out
	}
	for q, want := range map[string][]int{"?after=1&pageSize=2": {2, 3}, "?after=4": nil} {
		if got := ids(q); !slices.Equal(got, want) {
			t.Errorf("%s: ids = %v, want %v", q, got, want)
		}
	}
	r := h.get("/api/llm/calls?after=2&pageSize=1")
	var env wire.Success[wire.UsageCallPage]
	strictInto(t, r.body, &env)
	if c := env.Data.Calls[0]; c.CallID != "c3" || c.CachedTokens != 100 || c.TTFTMs == nil || *c.TTFTMs != 250 {
		t.Errorf("attempt 3 = %+v", c)
	}
}

func TestUsageCallsExportIsFilteredCSVWithoutFormulas(t *testing.T) {
	h := newDomainHarness(t)
	seedUsage(t, h)
	r := h.get("/api/llm/calls/export?outcome=ok")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") || !strings.Contains(r.Header.Get("Content-Disposition"), "model-calls.csv") {
		t.Fatalf("export: %d %v", r.StatusCode, r.Header)
	}
	rows, err := csv.NewReader(strings.NewReader(string(r.body))).ReadAll()
	if err != nil || len(rows) != 4 || rows[0][0] != "started_at" || len(rows[0]) != 13 || rows[0][12] != "reason" {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	var formula bool
	for _, row := range rows[1:] {
		if row[5] != "ok" {
			t.Errorf("a failed attempt was exported: %v", row)
		}
		formula = formula || row[2] == "'=beta"
	}
	if !formula {
		t.Errorf("a cell that starts with = must be neutralised: %v", rows)
	}
	if r := h.get("/api/llm/calls/export?from=nope"); r.StatusCode != 422 {
		t.Errorf("bad filter: %d", r.StatusCode)
	}
}

func TestUsageCostByDay(t *testing.T) {
	h := newDomainHarness(t)
	seedUsage(t, h)
	var env wire.Success[wire.UsageDays]
	strictInto(t, h.get("/api/llm/cost/daily?days=3").body, &env)
	d := env.Data.Days
	if len(d) != 3 || d[2].Day != h.srv.now().UTC().Format("2006-01-02") || d[2].CostUSD != 0.75 || d[2].Calls != 3 || d[1].Calls != 0 || d[0].Calls != 1 {
		t.Fatalf("days = %+v", d)
	}
}

func TestUsageTracesListAndWaterfall(t *testing.T) {
	h := newDomainHarness(t)
	runID := seedUsage(t, h)
	var list wire.Success[wire.UsageTraceList]
	strictInto(t, h.get("/api/llm/traces").body, &list)
	if len(list.Data.Traces) != 2 {
		t.Fatalf("traces = %+v", list.Data)
	}
	kinds := map[string]wire.UsageTrace{}
	for _, tr := range list.Data.Traces {
		kinds[tr.Kind] = tr
	}
	if kinds["scan"].RunID != runID || kinds["scan"].Calls != 2 || kinds["scan"].Status == "" || kinds[conversationTrace].RunID != "ses_chat" {
		t.Fatalf("kinds = %+v", kinds)
	}

	var detail wire.Success[wire.UsageTraceDetail]
	strictInto(t, h.get("/api/llm/traces/"+runID).body, &detail)
	sp := detail.Data.Spans
	if len(sp) != 3 || sp[0].Kind != wire.SpanRun || sp[0].Name != "scan" || sp[1].Kind != wire.SpanCall || sp[1].Name != "scan.rank" || sp[1].DurationMs != 700 ||
		sp[2].Outcome != "timeout" || sp[2].Error != "deadline" || sp[2].StartMs <= sp[1].StartMs {
		t.Fatalf("spans = %+v", sp)
	}
	var chat wire.Success[wire.UsageTraceDetail]
	strictInto(t, h.get("/api/llm/traces/ses_chat").body, &chat)
	if len(chat.Data.Spans) != 1 || chat.Data.Spans[0].Kind != wire.SpanCall || chat.Data.Spans[0].StartMs != 0 {
		t.Fatalf("conversation spans = %+v", chat.Data.Spans)
	}
	if r := h.get("/api/llm/traces/run_none"); r.StatusCode != 404 {
		t.Errorf("unknown trace: %d", r.StatusCode)
	}
}

// A batch resolves its skip list through the skip-list gate: a list past the
// gate's bounds stops the batch before any row runs, and the refusal is audited.
func TestApplyBatch_ASkipListPastItsBoundsStopsTheBatch(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	write := func(entries []string) {
		src := "version: 1\nbatch_skip_companies:\n  - " + strings.Join(entries, "\n  - ") + "\n"
		if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("Example %d", i)
		}
		return out
	}
	for code, entries := range map[string][]string{
		gates.CodeSkipListTooLong:   names(gates.MaxSkipEntries + 1),
		gates.CodeSkipListEntryLong: {strings.Repeat("a", gates.MaxSkipEntryLength+1)},
	} {
		write(entries)
		r := h.post("/api/apply/batches", wire.ApplyBatchRequest{})
		if r.StatusCode != 503 || !strings.Contains(string(r.body), "the batch does not start") {
			t.Errorf("%s: %d %s", code, r.StatusCode, r.body)
		}
		if n := h.rowCount("gate_decisions WHERE gate = 'batch_skip_list_config' AND verdict = 'deny' AND code = '" + code + "'"); n != 1 {
			t.Errorf("%s: %d audited refusals, want 1", code, n)
		}
	}
	write(names(gates.MaxSkipEntries))
	if r := h.post("/api/apply/batches", wire.ApplyBatchRequest{}); r.StatusCode == 503 {
		t.Errorf("a list at the bound stopped the batch: %s", r.body)
	}
	if n := h.rowCount("apply_batches"); n != 0 {
		t.Errorf("%d batches were journaled with nothing to run", n)
	}
}

func TestApplySkipStatusReadsTheEffectiveList(t *testing.T) {
	h := newDomainHarness(t)
	status := func(company string) wire.ApplySkipStatus {
		r := h.get("/api/apply/skip-status?company=" + company)
		var env wire.Success[wire.ApplySkipStatus]
		strictInto(t, r.body, &env)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", company, r.StatusCode, r.body)
		}
		return env.Data
	}
	if s := status("Google"); !s.Listed || s.Entry != "Alphabet" {
		t.Errorf("the default list covers Alphabet by alias: %+v", s)
	}
	if s := status("Example+Labs"); s.Listed || s.Entry != "" {
		t.Errorf("an unlisted company: %+v", s)
	}
	write := func(src string) {
		if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("version: 1\nbatch_skip_companies:\n  - Example Labs\n")
	if s := status("Example+Labs+Inc"); !s.Listed || s.Entry != "Example Labs" {
		t.Errorf("a present key is the whole list: %+v", s)
	}
	if r := h.get("/api/apply/skip-status"); r.StatusCode != 422 {
		t.Errorf("no company: %d", r.StatusCode)
	}
	write("version: not-a-number\n")
	if r := h.get("/api/apply/skip-status?company=Example"); r.StatusCode != 503 {
		t.Errorf("a broken profile must not read as not listed: %d %s", r.StatusCode, r.body)
	}
}
