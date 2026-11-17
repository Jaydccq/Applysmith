package llm

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// CallFilter narrows the ledger rows. Outcome "failed" keeps every attempt
// that did not end ok; any other value is matched exactly. From is inclusive
// and To exclusive; a zero time is no bound. After keeps only attempts with a
// ledger id above it and lists them oldest first, so a live view follows the
// ledger from the last id it has seen.
type CallFilter struct {
	Task, Provider, Outcome string
	From, To                time.Time
	After                   *int64
}

// CallRow is one ledger attempt as the usage table shows it. It carries no
// prompt or completion text.
type CallRow struct {
	ID           int64
	CallID       string // the attempts of one model call share it
	StartedAt    time.Time
	Task         string
	ProviderID   string
	Model        string
	Attempt      int
	Outcome      string
	InputTokens  int
	OutputTokens int
	CachedInput  int // of InputTokens, served from the provider's prompt cache
	CostUSD      *float64
	Latency      time.Duration
	TTFT         *time.Duration
	Downgraded   bool
	RunID        string
	// Reason is why the attempt ran on this candidate; see the Reason constants.
	Reason string
}

func (f CallFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Task != "" {
		conds, args = append(conds, "task = ?"), append(args, f.Task)
	}
	if f.Provider != "" {
		conds, args = append(conds, "provider_id = ?"), append(args, f.Provider)
	}
	switch f.Outcome {
	case "":
	case "failed":
		conds = append(conds, "outcome <> 'ok'")
	default:
		conds, args = append(conds, "outcome = ?"), append(args, f.Outcome)
	}
	if !f.From.IsZero() {
		conds, args = append(conds, "started_at >= ?"), append(args, timeText(f.From))
	}
	if !f.To.IsZero() {
		conds, args = append(conds, "started_at < ?"), append(args, timeText(f.To))
	}
	if f.After != nil {
		conds, args = append(conds, "id > ?"), append(args, *f.After)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const callColumns = `id, call_id, started_at, task, provider_id, model, attempt, outcome, input_tokens, output_tokens, cached_input_tokens, cost_usd, latency_ms, ttft_ms, downgraded, run_id, reason`

// scanCall reads the callColumns and then any extra columns into extra.
func scanCall(rows *sql.Rows, extra ...any) (CallRow, error) {
	var c CallRow
	var started string
	var cost sql.NullFloat64
	var latency, down int64
	var ttft sql.NullInt64
	dest := append([]any{&c.ID, &c.CallID, &started, &c.Task, &c.ProviderID, &c.Model, &c.Attempt, &c.Outcome, &c.InputTokens, &c.OutputTokens, &c.CachedInput, &cost, &latency, &ttft, &down, &c.RunID, &c.Reason}, extra...)
	if err := rows.Scan(dest...); err != nil {
		return CallRow{}, err
	}
	c.StartedAt = parseLedgerTime(started)
	if cost.Valid {
		c.CostUSD = &cost.Float64
	}
	if ttft.Valid {
		d := time.Duration(ttft.Int64) * time.Millisecond
		c.TTFT = &d
	}
	c.Latency, c.Downgraded = time.Duration(latency)*time.Millisecond, down != 0
	return c, nil
}

func parseLedgerTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05.000Z", s)
	return t
}

// Calls returns one page of ledger attempts, newest first (oldest first when
// the filter has After), and the number of attempts the filter matches.
func (r *Router) Calls(ctx context.Context, f CallFilter, limit, offset int) ([]CallRow, int, error) {
	where, args := f.where()
	var total int
	if err := r.store.Read().QueryRowContext(ctx, `SELECT count(*) FROM llm_ledger`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	var out []CallRow
	err := r.eachCall(ctx, f, `LIMIT ? OFFSET ?`, []any{limit, offset}, func(c CallRow) error {
		out = append(out, c)
		return nil
	})
	return out, total, err
}

// EachCall calls fn for every matching attempt, newest first, for an export.
func (r *Router) EachCall(ctx context.Context, f CallFilter, fn func(CallRow) error) error {
	return r.eachCall(ctx, f, "", nil, fn)
}

func (r *Router) eachCall(ctx context.Context, f CallFilter, tail string, extra []any, fn func(CallRow) error) error {
	where, args := f.where()
	order := "started_at DESC, id DESC"
	if f.After != nil {
		order = "id"
	}
	rows, err := r.store.Read().QueryContext(ctx, `SELECT `+callColumns+` FROM llm_ledger`+where+` ORDER BY `+order+` `+tail, append(args, extra...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return rows.Err()
}

// DayCost is the spend of one local day.
type DayCost struct {
	Day     string // YYYY-MM-DD
	CostUSD float64
	Calls   int
}

// CostByDay reports each of the last N local days of the clock's zone, today
// last, with days that have no call as zero. Calls counts model calls, not
// attempts.
func (r *Router) CostByDay(ctx context.Context, days int) ([]DayCost, error) {
	now := r.now()
	y, m, d := now.Date()
	out := make([]DayCost, days)
	at := map[string]int{}
	for i := range out {
		out[i].Day = time.Date(y, m, d-(days-1)+i, 0, 0, 0, 0, now.Location()).Format(time.DateOnly)
		at[out[i].Day] = i
	}
	rows, err := r.store.Read().QueryContext(ctx, `SELECT started_at, COALESCE(cost_usd, 0), call_id FROM llm_ledger WHERE started_at >= ?`,
		timeText(time.Date(y, m, d-(days-1), 0, 0, 0, 0, now.Location())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := map[[2]string]bool{}
	for rows.Next() {
		var started, call string
		var usd float64
		if err := rows.Scan(&started, &usd, &call); err != nil {
			return nil, err
		}
		day := parseLedgerTime(started).In(now.Location()).Format(time.DateOnly)
		i, ok := at[day]
		if !ok {
			continue
		}
		out[i].CostUSD += usd
		if k := [2]string{day, call}; !calls[k] {
			calls[k] = true
			out[i].Calls++
		}
	}
	for i := range out {
		out[i].CostUSD = roundMicro(out[i].CostUSD)
	}
	return out, rows.Err()
}

// TraceSummary is the model calls one run (or conversation) made.
type TraceSummary struct {
	RunID      string
	FirstStart time.Time
	Calls      int
	CostUSD    float64
	// Tasks are the distinct tasks the calls were for, in name order.
	Tasks []string
}

// Traces lists the runs that made model calls, most recent first.
func (r *Router) Traces(ctx context.Context, limit int) ([]TraceSummary, error) {
	rows, err := r.store.Read().QueryContext(ctx, `SELECT run_id, MIN(started_at), COUNT(DISTINCT call_id), COALESCE(SUM(cost_usd), 0), COALESCE(GROUP_CONCAT(DISTINCT task), '')
		FROM llm_ledger WHERE run_id <> '' GROUP BY run_id ORDER BY MIN(started_at) DESC, run_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceSummary
	for rows.Next() {
		var t TraceSummary
		var first, tasks string
		if err := rows.Scan(&t.RunID, &first, &t.Calls, &t.CostUSD, &tasks); err != nil {
			return nil, err
		}
		if tasks != "" {
			t.Tasks = strings.Split(tasks, ",")
			slices.Sort(t.Tasks)
		}
		t.FirstStart = parseLedgerTime(first)
		t.CostUSD = roundMicro(t.CostUSD)
		out = append(out, t)
	}
	return out, rows.Err()
}

// CallSpan is one attempt of a trace with the error text the ledger kept,
// redacted when it was written.
type CallSpan struct {
	CallRow
	ErrorText string
}

// TraceCalls returns the attempts a run made, oldest first.
func (r *Router) TraceCalls(ctx context.Context, runID string) ([]CallSpan, error) {
	rows, err := r.store.Read().QueryContext(ctx, `SELECT `+callColumns+`, error_text FROM llm_ledger WHERE run_id = ? ORDER BY started_at, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CallSpan
	for rows.Next() {
		var errText string
		c, err := scanCall(rows, &errText)
		if err != nil {
			return nil, err
		}
		out = append(out, CallSpan{CallRow: c, ErrorText: Redact(errText)})
	}
	return out, rows.Err()
}
