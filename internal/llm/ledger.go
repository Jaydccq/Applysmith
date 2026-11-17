package llm

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"time"
)

// LedgerRow is one attempt of one model call.
type LedgerRow struct {
	CallID          string
	StartedAt       time.Time
	Task            string
	ProviderID      string
	ProviderKind    string
	Model           string
	Attempt         int
	Replaced        string // "provider/model" of the candidate this attempt replaced
	Reason          string // why the attempt ran on this candidate; see the Reason constants
	CandidateSource string // primary, fallback or override
	Outcome         string // ok or a failure kind
	HTTPStatus      int
	Usage           Usage
	CostUSD         *float64
	Billing         string
	Latency         time.Duration
	TTFT            *time.Duration
	FinishReason    string
	Clamped         bool
	Downgraded      bool
	RunID           string
	ErrorText       string
}

// Why an attempt ran on its candidate. The configured primary of a call has no
// reason.
const (
	ReasonFallback     = "fallback"      // an earlier candidate failed
	ReasonFirstToken   = "first-token"   // the earlier candidate's first token was late
	ReasonRetry        = "retry"         // the same candidate again, after the short delay its 429 asked for
	ReasonRested       = "rested"        // an earlier-configured candidate was resting
	ReasonCapDowngrade = "cap-downgrade" // the daily cap moved a cheaper candidate first
	ReasonAffinity     = "affinity"      // the agent conversation stayed on the candidate that answered it last
	ReasonRotate       = "rotate"        // a rotate group started this call at a member other than the first listed
	ReasonUsage        = "usage"         // a usage group put its least-used member in front of the first listed
)

// capture holds prompt and completion text for debug capture.
type capture struct{ prompt, completion string }

// writeLedger stores one row and, while debug capture is on, the redacted
// texts. A failure is logged and never reaches the caller.
func (r *Router) writeLedger(ctx context.Context, row LedgerRow, cp *capture, captureOn bool) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
	defer cancel()
	err := r.store.Write(wctx, func(tx *sql.Tx) error {
		var ttft any
		if row.TTFT != nil {
			ttft = row.TTFT.Milliseconds()
		}
		var cost any
		if row.CostUSD != nil {
			cost = *row.CostUSD
		}
		var status any
		if row.HTTPStatus != 0 {
			status = row.HTTPStatus
		}
		var replaced any
		if row.Replaced != "" {
			replaced = row.Replaced
		}
		res, err := tx.ExecContext(wctx, `INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt,
			replaced, candidate_source, outcome, http_status, input_tokens, output_tokens, cached_input_tokens, reasoning_tokens,
			cost_usd, billing, latency_ms, ttft_ms, finish_reason, clamped, downgraded, run_id, error_text, reason)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			row.CallID, timeText(row.StartedAt), row.Task, row.ProviderID, row.ProviderKind, row.Model, row.Attempt,
			replaced, row.CandidateSource, row.Outcome, status, row.Usage.Input, row.Usage.Output, row.Usage.CachedInput, row.Usage.Reasoning,
			cost, row.Billing, row.Latency.Milliseconds(), ttft, row.FinishReason, b2i(row.Clamped), b2i(row.Downgraded), row.RunID, row.ErrorText, row.Reason)
		if err != nil {
			return err
		}
		if captureOn && cp != nil {
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(wctx, `INSERT INTO llm_capture (ledger_id, captured_at, prompt, completion) VALUES (?,?,?,?)`,
				id, timeText(r.now()), Redact(cp.prompt), Redact(cp.completion))
			return err
		}
		return nil
	})
	if err != nil {
		r.log.Error("llm: ledger write failed", "err", err)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Retention periods.
const (
	ledgerRetention  = 180 * 24 * time.Hour
	captureRetention = 24 * time.Hour
)

// Prune is the retention sweep: ledger rows older than 180 days and captured
// texts older than 24 hours are deleted.
func (r *Router) Prune(ctx context.Context) error {
	now := r.now()
	return r.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM llm_ledger WHERE started_at < ?`, timeText(now.Add(-ledgerRetention))); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM llm_capture WHERE captured_at < ?`, timeText(now.Add(-captureRetention)))
		return err
	})
}

// SetDebugCapture turns debug capture on for 1 to 120 minutes; it switches
// itself off at expiry. Zero minutes turns it off.
func (r *Router) SetDebugCapture(ctx context.Context, minutes int) error {
	if minutes < 0 || minutes > 120 {
		return &SetupError{Message: "debug capture lasts 1 to 120 minutes"}
	}
	until := ""
	if minutes > 0 {
		until = timeText(r.now().Add(time.Duration(minutes) * time.Minute))
	}
	return r.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO llm_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, keyCapture, until, timeText(r.now()))
		return err
	})
}

func (r *Router) captureOn(ctx context.Context) bool {
	_, on := r.DebugCaptureUntil(ctx)
	return on
}

// DebugCaptureUntil reports whether debug capture is on and when it ends.
func (r *Router) DebugCaptureUntil(ctx context.Context) (time.Time, bool) {
	var until string
	err := r.store.Read().QueryRowContext(ctx, `SELECT value FROM llm_settings WHERE key = ?`, keyCapture).Scan(&until)
	if err != nil || until == "" || timeText(r.now()) >= until {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z", until)
	return t, err == nil
}

// RefusedProvider is a provider that refused every call since its last
// success: out of credit (Why RestQuota) or holding a dead key (RestDeadKey).
type RefusedProvider struct {
	Provider string
	Why      string
	// Since is the first refused call after the last success.
	Since time.Time
	Calls int
	Tasks []string
}

// refusedRun is how many refused calls in a row make a provider refused, so
// one stray refusal never reaches the user.
const refusedRun = 3

// RefusedProviders reads the ledger from since on: the providers whose calls
// since their last success include at least three refusals, each one a
// quota-exhausted reply or a 401. Other failures neither count nor end a run;
// a success ends it.
func (r *Router) RefusedProviders(ctx context.Context, since time.Time) ([]RefusedProvider, error) {
	rows, err := r.store.Read().QueryContext(ctx, `SELECT provider_id, task, outcome, COALESCE(http_status, 0), started_at
		FROM llm_ledger WHERE started_at >= ? ORDER BY started_at, id`, timeText(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := map[string]*RefusedProvider{}
	var order []string
	for rows.Next() {
		var provider, task, outcome, at string
		var status int
		if err := rows.Scan(&provider, &task, &outcome, &status, &at); err != nil {
			return nil, err
		}
		why := ""
		switch {
		case outcome == "ok":
			delete(runs, provider)
			continue
		case outcome == string(KindQuota):
			why = RestQuota
		case outcome == string(KindAuth) && status == http.StatusUnauthorized:
			why = RestDeadKey
		default:
			continue
		}
		run := runs[provider]
		if run == nil {
			started, _ := time.Parse(time.RFC3339Nano, at)
			run = &RefusedProvider{Provider: provider, Since: started}
			runs[provider] = run
			if !slices.Contains(order, provider) {
				order = append(order, provider)
			}
		}
		run.Why, run.Calls = why, run.Calls+1
		if !slices.Contains(run.Tasks, task) {
			run.Tasks = append(run.Tasks, task)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []RefusedProvider
	for _, p := range order {
		if run := runs[p]; run != nil && run.Calls >= refusedRun {
			out = append(out, *run)
		}
	}
	return out, nil
}
