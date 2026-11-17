package llm

import (
	"cmp"
	"context"
	"math"
	"sort"
	"time"
)

// ReportRow is one line of the cost report.
type ReportRow struct {
	Name         string
	Calls        int
	SuccessRate  float64
	FallbackRate float64
	CostUSD      float64
	Unpriced     int
	P50, P95     time.Duration
	// RateLimited is the share of calls (tasks) or attempts (providers) the
	// provider refused as rate-limited. CacheHit is the cached share of the
	// input tokens, or nil when no cached token was ever reported.
	RateLimited float64
	CacheHit    *float64
	// InputTokens, OutputTokens and CachedTokens are the totals of every
	// attempt; WastedTokens are the input and output tokens of the attempts
	// that did not succeed (an aborted, timed-out or refused attempt is
	// billed and its reply is thrown away). Runs counts the distinct runs
	// (an application or an evaluation) whose calls carried one.
	InputTokens, OutputTokens, CachedTokens, WastedTokens int
	Runs                                                  int
}

// PerRunTokens is the average input and output tokens of one run, or 0 when
// no call carried a run.
func (r ReportRow) PerRunTokens() int {
	if r.Runs == 0 {
		return 0
	}
	return (r.InputTokens + r.OutputTokens) / r.Runs
}

type reportAcc struct {
	calls, ok, fallback, unpriced, limited, input, cached int
	output, wasted                                        int
	cost                                                  float64
	latencies                                             []time.Duration
	runs                                                  map[string]bool
}

// addRun counts a run id once.
func (a *reportAcc) addRun(id string) {
	if id == "" {
		return
	}
	if a.runs == nil {
		a.runs = map[string]bool{}
	}
	a.runs[id] = true
}

func (a *reportAcc) row(name string) ReportRow {
	row := ReportRow{Name: name, Calls: a.calls, CostUSD: roundMicro(a.cost), Unpriced: a.unpriced,
		InputTokens: a.input, OutputTokens: a.output, CachedTokens: a.cached, WastedTokens: a.wasted, Runs: len(a.runs)}
	if a.calls > 0 {
		row.SuccessRate = float64(a.ok) / float64(a.calls)
		row.FallbackRate = float64(a.fallback) / float64(a.calls)
		row.RateLimited = float64(a.limited) / float64(a.calls)
	}
	if a.cached > 0 && a.input > 0 {
		hit := float64(a.cached) / float64(a.input)
		row.CacheHit = &hit
	}
	sort.Slice(a.latencies, func(i, j int) bool { return a.latencies[i] < a.latencies[j] })
	row.P50, row.P95 = percentile(a.latencies, 0.50), percentile(a.latencies, 0.95)
	return row
}

// percentile is the nearest-rank percentile of sorted durations: the
// smallest value at or above the share p of the samples.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[max(int(math.Ceil(float64(len(sorted))*p))-1, 0)]
}

// CostReport reports the last N days per task (counting calls) and per
// provider (counting attempts): calls, success rate, fallback rate, cost,
// unpriced calls, rate-limited share, cached-input share, token totals (with
// the tokens of failed attempts and the average per run) and p50 and p95
// latency of the successes. An attempt the offline simulation has no script
// for asked no model, so it is neither a call nor a failure here; the call
// list still shows it.
func (r *Router) CostReport(ctx context.Context, days int) (byTask, byProvider []ReportRow, err error) {
	since := timeText(r.now().Add(-time.Duration(days) * 24 * time.Hour))
	rows, err := r.store.Read().QueryContext(ctx, `SELECT call_id, task, provider_id, outcome, candidate_source, attempt, reason,
		COALESCE(cost_usd, 0), billing, latency_ms, input_tokens, cached_input_tokens, output_tokens, run_id FROM llm_ledger WHERE started_at >= ? AND outcome <> ?`,
		since, string(KindFakeFixture))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type callAcc struct {
		task               string
		ok, multi, limited bool
		cost               float64
		unpriced           int
		input, cached      int
		output, wasted     int
		runID              string
		latency            time.Duration
	}
	calls := map[string]*callAcc{}
	prov := map[string]*reportAcc{}
	for rows.Next() {
		var callID, task, provider, outcome, source, reason, billing, runID string
		var attempt, latencyMS, input, cached, output int
		var cost float64
		if err := rows.Scan(&callID, &task, &provider, &outcome, &source, &attempt, &reason, &cost, &billing, &latencyMS, &input, &cached, &output, &runID); err != nil {
			return nil, nil, err
		}
		c := calls[callID]
		if c == nil {
			c = &callAcc{task: task}
			calls[callID] = c
		}
		lat := time.Duration(latencyMS) * time.Millisecond
		limited := outcome == string(KindRateLimited)
		c.cost += cost
		c.input += input
		c.cached += cached
		c.output += output
		c.runID = cmp.Or(c.runID, runID)
		wasted := 0
		if outcome != "ok" {
			wasted = input + output
		}
		c.wasted += wasted
		c.limited = c.limited || limited
		if billing == BillingUnpriced {
			c.unpriced++
		}
		if attempt > 1 && reason != ReasonRetry {
			c.multi = true
		}
		if outcome == "ok" {
			c.ok, c.latency = true, lat
		}
		p := prov[provider]
		if p == nil {
			p = &reportAcc{}
			prov[provider] = p
		}
		p.calls++
		p.cost += cost
		p.input += input
		p.cached += cached
		p.output += output
		p.wasted += wasted
		p.addRun(runID)
		if limited {
			p.limited++
		}
		if billing == BillingUnpriced {
			p.unpriced++
		}
		if source == "fallback" {
			p.fallback++
		}
		if outcome == "ok" {
			p.ok++
			p.latencies = append(p.latencies, lat)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	tasks := map[string]*reportAcc{}
	for _, c := range calls {
		t := tasks[c.task]
		if t == nil {
			t = &reportAcc{}
			tasks[c.task] = t
		}
		t.calls++
		t.cost += c.cost
		t.unpriced += c.unpriced
		t.input += c.input
		t.cached += c.cached
		t.output += c.output
		t.wasted += c.wasted
		t.addRun(c.runID)
		if c.limited {
			t.limited++
		}
		if c.multi {
			t.fallback++
		}
		if c.ok {
			t.ok++
			t.latencies = append(t.latencies, c.latency)
		}
	}
	return collect(tasks), collect(prov), nil
}

func collect(m map[string]*reportAcc) []ReportRow {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ReportRow, 0, len(names))
	for _, n := range names {
		out = append(out, m[n].row(n))
	}
	return out
}
