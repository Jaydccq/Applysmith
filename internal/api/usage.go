package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// conversationTrace is the kind of a trace whose id is an Ask Agent session.
const conversationTrace = "conversation"

// callFilter reads the call table's filters. A date is a local day; "to"
// keeps that whole day.
func callFilter(q *apikit.Query) llm.CallFilter {
	f := llm.CallFilter{Task: q.Str("task"), Provider: q.Str("provider"), Outcome: q.Str("outcome"), From: q.Day("from")}
	if q.Str("after") != "" {
		after := int64(q.Integer("after", 0))
		if after < 0 {
			q.Fail(apikit.InvalidField("after", "after must be a ledger id, 0 or more"))
		}
		f.After = &after
	}
	if to := q.Day("to"); !to.IsZero() {
		f.To = to.AddDate(0, 0, 1)
	}
	return f
}

func toUsageCall(c llm.CallRow) wire.UsageCall {
	out := wire.UsageCall{ID: apikit.Safe64(c.ID), CallID: c.CallID, StartedAt: wire.Time{Time: c.StartedAt}, Task: c.Task, Provider: c.ProviderID, Model: c.Model,
		Attempt: apikit.Safe(c.Attempt), Outcome: c.Outcome, InputTokens: apikit.Safe(c.InputTokens), OutputTokens: apikit.Safe(c.OutputTokens), CachedTokens: apikit.Safe(c.CachedInput), CostUSD: c.CostUSD,
		LatencyMs: apikit.Safe64(c.Latency.Milliseconds()), Downgraded: c.Downgraded, RunID: c.RunID, Reason: c.Reason}
	if c.TTFT != nil {
		ttft := apikit.Safe64(c.TTFT.Milliseconds())
		out.TTFTMs = &ttft
	}
	return out
}

func (s *Server) usageCalls(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	page, size := q.Paging(50)
	f := callFilter(q)
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	rows, total, err := d.router.Calls(c.R.Context(), f, size, (page-1)*size)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.UsageCallPage{Calls: make([]wire.UsageCall, len(rows)), Total: apikit.Safe(total)}
	for i, r := range rows {
		out.Calls[i] = toUsageCall(r)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) usageCallsExport(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	f := callFilter(q)
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"started_at", "task", "provider", "model", "attempt", "outcome", "input_tokens", "output_tokens", "cost_usd", "latency_ms", "downgraded", "run_id", "reason"})
	err = d.router.EachCall(c.R.Context(), f, func(r llm.CallRow) error {
		cost := ""
		if r.CostUSD != nil {
			cost = strconv.FormatFloat(*r.CostUSD, 'f', -1, 64)
		}
		return cw.Write([]string{r.StartedAt.UTC().Format(time.RFC3339), tracker.CSVSafe(r.Task), tracker.CSVSafe(r.ProviderID), tracker.CSVSafe(r.Model),
			strconv.Itoa(r.Attempt), tracker.CSVSafe(r.Outcome), strconv.Itoa(r.InputTokens), strconv.Itoa(r.OutputTokens), cost,
			strconv.FormatInt(r.Latency.Milliseconds(), 10), strconv.FormatBool(r.Downgraded), tracker.CSVSafe(r.RunID), tracker.CSVSafe(r.Reason)})
	})
	if err != nil {
		return apikit.Reply{}, err
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return apikit.Reply{}, err
	}
	h := c.W.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="model-calls.csv"`)
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(buf.Bytes())
	c.Handled = true
	return apikit.Reply{}, nil
}

func (s *Server) usageDays(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	days := q.Integer("days", 30)
	if q.Err == nil && (days < 1 || days > 90) {
		q.Fail(apikit.InvalidField("days", "days must be between 1 and 90"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	rows, err := d.router.CostByDay(c.R.Context(), days)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.UsageDays{Days: make([]wire.UsageDay, len(rows))}
	for i, r := range rows {
		out.Days[i] = wire.UsageDay{Day: r.Day, CostUSD: r.CostUSD, Calls: apikit.Safe(r.Calls)}
	}
	return apikit.Reply{Data: out}, nil
}

// traceOf joins a summary with its run record when there is one, and with the
// runs board entry of the same id, which the application, scan and evaluation
// runs have and which names the posting.
func traceOf(ctx context.Context, st *store.Store, board *notify.Board, sum llm.TraceSummary) (wire.UsageTrace, *store.Run, error) {
	t := wire.UsageTrace{RunID: sum.RunID, StartedAt: wire.Time{Time: sum.FirstStart}, Calls: apikit.Safe(sum.Calls), CostUSD: sum.CostUSD}
	run, err := st.GetRun(ctx, sum.RunID)
	var rec *store.Run
	switch {
	case errors.Is(err, store.ErrNotFound):
		if strings.HasPrefix(sum.RunID, "ses_") {
			t.Kind, t.Title = conversationTrace, "Ask Agent conversation"
			return t, nil, nil
		}
	case err != nil:
		return t, nil, err
	default:
		rec = &run
		t.Kind, t.Status, t.Stage, t.StartedAt = run.Kind, run.Status, run.Stage, apikit.Stamp(run.CreatedAt)
	}
	if board != nil {
		e, err := board.Get(ctx, sum.RunID)
		switch {
		case err == nil:
			t.Title = e.Title
			if rec == nil {
				t.Kind, t.Stage, t.StartedAt = string(e.Kind), e.Stage, wire.Time{Time: e.StartedAt.UTC()}
				t.Status = string(e.State)
				if e.State == notify.Done && e.Outcome != "" {
					t.Status = string(e.Outcome)
				}
			} else if t.Stage == "" {
				t.Stage = e.Stage
			}
		case !errors.Is(err, notify.ErrNotFound):
			return t, nil, err
		}
	}
	if t.Title == "" {
		t.Title = t.Kind
	}
	if t.Title == "" {
		t.Title = strings.Join(sum.Tasks, ", ")
	}
	return t, rec, nil
}

func (s *Server) usageTraces(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	limit := q.Integer("limit", 25)
	if q.Err == nil && (limit < 1 || limit > 100) {
		q.Fail(apikit.InvalidField("limit", "limit must be between 1 and 100"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	sums, err := d.router.Traces(c.R.Context(), limit)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.UsageTraceList{Traces: make([]wire.UsageTrace, len(sums))}
	for i, sum := range sums {
		if out.Traces[i], _, err = traceOf(c.R.Context(), st, d.notify.board, sum); err != nil {
			return apikit.Reply{}, err
		}
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) usageTrace(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	calls, err := d.router.TraceCalls(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	sum := llm.TraceSummary{RunID: id, Calls: len(calls)}
	for _, call := range calls {
		if call.CostUSD != nil {
			sum.CostUSD += *call.CostUSD
		}
		if !slices.Contains(sum.Tasks, call.Task) {
			sum.Tasks = append(sum.Tasks, call.Task)
		}
	}
	if len(calls) > 0 {
		sum.FirstStart = calls[0].StartedAt
	}
	trace, run, err := traceOf(c.R.Context(), st, d.notify.board, sum)
	if err != nil {
		return apikit.Reply{}, err
	}
	if run == nil && len(calls) == 0 {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such run or conversation has model calls")
	}
	start := trace.StartedAt.Time
	spans := make([]wire.UsageSpan, 0, len(calls)+1)
	if run != nil {
		end := apikit.Stamp(run.UpdatedAt).Time
		if run.FinishedAt != "" {
			end = apikit.Stamp(run.FinishedAt).Time
		}
		spans = append(spans, wire.UsageSpan{Kind: wire.SpanRun, Name: run.Kind, DurationMs: apikit.Safe64(max(end.Sub(start).Milliseconds(), 0)), Outcome: run.Status, Error: llm.Redact(run.FailureReason)})
	}
	for _, call := range calls {
		spans = append(spans, wire.UsageSpan{Kind: wire.SpanCall, Name: call.Task, StartMs: apikit.Safe64(max(call.StartedAt.Sub(start).Milliseconds(), 0)),
			DurationMs: apikit.Safe64(call.Latency.Milliseconds()), Outcome: call.Outcome, Provider: call.ProviderID, Model: call.Model, Attempt: apikit.Safe(call.Attempt),
			InputTokens: apikit.Safe(call.InputTokens), OutputTokens: apikit.Safe(call.OutputTokens), CostUSD: call.CostUSD, Reason: call.Reason, Error: call.ErrorText})
	}
	return apikit.Reply{Data: wire.UsageTraceDetail{Trace: trace, Spans: spans}}, nil
}

// skipEntries is the batch skip list the pages show: the profile's own list,
// or the default when the profile has none.
func (s *Server) skipEntries() ([]string, error) {
	p, err := s.skipProfile()
	if err != nil {
		return nil, err
	}
	return p.EffectiveSkipCompanies(), nil
}

// batchSkipEntries is the skip list a batch runs under, as the skip-list gate
// resolves it. A list past the gate's bounds stops the batch before any row
// runs, and that refusal is audited. A key that is no list of strings fails
// the profile's own validation first.
func (s *Server) batchSkipEntries(ctx context.Context) ([]string, error) {
	p, err := s.skipProfile()
	if err != nil {
		return nil, err
	}
	list, d := gates.ResolveSkipList(gates.SkipListConfig{Present: p.SkipListPresent(), Entries: p.BatchSkipCompanies})
	if d.Allowed() {
		return list, nil
	}
	if err := s.recordGateAs(ctx, gates.CallerApplyList, d); err != nil {
		return nil, err
	}
	return nil, apikit.NewErr(wire.CodeUnavailable, "%s", d.Reason)
}

// skipProfile reads the profile the skip list lives in.
func (s *Server) skipProfile() (*workspace.Profile, error) {
	raw, err := s.cfg.Workspace.Read(workspace.FileProfile)
	if err != nil {
		return nil, err
	}
	p, rep := workspace.ParseProfile(raw.Data)
	if !rep.OK() {
		return nil, apikit.NewErr(wire.CodeUnavailable, "profile.yml has errors, so the skip list cannot be read")
	}
	return &p, nil
}
