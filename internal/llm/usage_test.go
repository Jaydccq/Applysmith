package llm

import (
	"testing"
	"time"
)

func seed(t *testing.T, e *env, rows ...LedgerRow) {
	t.Helper()
	for _, row := range rows {
		e.r.writeLedger(bg(), row, nil, false)
	}
}

func costOf(v float64) *float64 { return &v }

func usageRows(e *env) []LedgerRow {
	now := e.clock.now()
	return []LedgerRow{
		{CallID: "c1", StartedAt: now.Add(-2 * time.Hour), Task: "scan.rank", ProviderID: "a", ProviderKind: KindOpenAI, Model: "m1", Attempt: 1, CandidateSource: "primary",
			Outcome: "ok", Usage: Usage{Input: 100, Output: 20}, CostUSD: costOf(0.5), Billing: BillingMetered, Latency: 800 * time.Millisecond, RunID: "run_1"},
		{CallID: "c2", StartedAt: now.Add(-time.Hour), Task: "evaluation.deep", ProviderID: "a", ProviderKind: KindOpenAI, Model: "m1", Attempt: 1, CandidateSource: "primary",
			Outcome: "rate-limited", Billing: BillingMetered, Latency: 100 * time.Millisecond, RunID: "run_1", ErrorText: "limit"},
		{CallID: "c2", StartedAt: now.Add(-time.Hour + time.Second), Task: "evaluation.deep", ProviderID: "b", ProviderKind: KindOpenAI, Model: "m2", Attempt: 2, CandidateSource: "fallback",
			Outcome: "ok", Usage: Usage{Input: 10, Output: 5}, CostUSD: costOf(0.25), Billing: BillingMetered, Latency: 2 * time.Second, RunID: "run_1", Downgraded: true, Reason: ReasonFallback},
		{CallID: "c3", StartedAt: now.Add(-30 * time.Hour), Task: "scan.rank", ProviderID: "b", ProviderKind: KindOpenAI, Model: "m2", Attempt: 1, CandidateSource: "primary",
			Outcome: "ok", Billing: BillingUnpriced, Latency: time.Second},
	}
}

func TestUsage_CallsFilterAndPage(t *testing.T) {
	e := newEnv(t)
	seed(t, e, usageRows(e)...)
	for _, tc := range []struct {
		name string
		f    CallFilter
		want int
	}{
		{"all", CallFilter{}, 4},
		{"task", CallFilter{Task: "scan.rank"}, 2},
		{"provider", CallFilter{Provider: "b"}, 2},
		{"outcome ok", CallFilter{Outcome: "ok"}, 3},
		{"outcome failed", CallFilter{Outcome: "failed"}, 1},
		{"outcome kind", CallFilter{Outcome: "rate-limited"}, 1},
		{"from", CallFilter{From: e.clock.now().Add(-3 * time.Hour)}, 3},
		{"to is exclusive", CallFilter{To: e.clock.now().Add(-3 * time.Hour)}, 1},
	} {
		if _, total, err := e.r.Calls(bg(), tc.f, 50, 0); err != nil || total != tc.want {
			t.Errorf("%s: total %d err %v, want %d", tc.name, total, err, tc.want)
		}
	}
	page, total, err := e.r.Calls(bg(), CallFilter{}, 2, 0)
	if err != nil || total != 4 || len(page) != 2 {
		t.Fatalf("page = %d rows of %d, %v", len(page), total, err)
	}
	if page[0].Outcome != "ok" || page[0].Model != "m2" || !page[0].Downgraded || page[0].CostUSD == nil || *page[0].CostUSD != 0.25 ||
		page[0].InputTokens != 10 || page[0].Latency != 2*time.Second || page[1].Outcome != "rate-limited" || page[1].CostUSD != nil {
		t.Fatalf("rows newest first = %+v", page)
	}
}

func TestUsage_TracesListAndDetail(t *testing.T) {
	e := newEnv(t)
	seed(t, e, usageRows(e)...)
	list, err := e.r.Traces(bg(), 10)
	if err != nil || len(list) != 1 || list[0].RunID != "run_1" || list[0].Calls != 2 || list[0].CostUSD != 0.75 {
		t.Fatalf("traces = %+v, %v", list, err)
	}
	spans, err := e.r.TraceCalls(bg(), "run_1")
	if err != nil || len(spans) != 3 || spans[0].Task != "scan.rank" || spans[1].ErrorText != "limit" || spans[2].Attempt != 2 {
		t.Fatalf("spans = %+v, %v", spans, err)
	}
	if spans, _ := e.r.TraceCalls(bg(), "run_none"); len(spans) != 0 {
		t.Errorf("unknown run has spans %+v", spans)
	}
}
