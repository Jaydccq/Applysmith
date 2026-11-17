// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"applysmith/internal/store"
)

const (
	runKind      = "evaluate"
	runPrincipal = "evaluation"
)

func (e *Evaluator) stamp() string { return e.d.Now().UTC().Format(time.RFC3339) }

// createJob records the run, its idempotency claim and its evaluation row in
// one transaction. The key is the idempotency key of the run.
func (e *Evaluator) createJob(ctx context.Context, req Request, identityKey, idemKey string) (string, error) {
	ref, err := e.claimRun(ctx, store.RunRequest{Principal: runPrincipal, Operation: "evaluate", Key: idemKey}, req, identityKey)
	return ref.RunID, err
}

// claimRun is CreateRun for an evaluation: a run that is created also gets its
// evaluation row in the claim's transaction, so no run exists without a job.
func (e *Evaluator) claimRun(ctx context.Context, rr store.RunRequest, req Request, identityKey string) (store.RunRef, error) {
	in, err := json.Marshal(req)
	if err != nil {
		return store.RunRef{}, err
	}
	rr.Kind, rr.Input = runKind, in
	rr.Created = func(tx *sql.Tx, runID string) error { return e.insertJob(ctx, tx, runID, req, identityKey) }
	return e.d.Store.CreateRun(ctx, rr)
}

// insertJob records the evaluation row of a run.
func (e *Evaluator) insertJob(ctx context.Context, tx *sql.Tx, runID string, req Request, identityKey string) error {
	var inbox any
	if req.InboxRowID > 0 {
		inbox = req.InboxRowID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, source, posting_url, inbox_row_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, runID, identityKey, string(req.Mode), string(req.Priority), req.Source, req.URL, inbox, e.stamp())
	return err
}

// setPhase moves the run to a phase and tells the stream.
func (e *Evaluator) setPhase(ctx context.Context, runID string, p Phase) error {
	status := "running"
	if p == PhaseQueued {
		status = "queued"
	}
	err := e.d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, stage = ?, updated_at = ? WHERE id = ? AND status IN ('queued', 'running')`, status, string(p), e.stamp(), runID)
		return err
	})
	if err == nil && e.d.Emit != nil {
		e.d.Emit(Event{RunID: runID, Phase: p})
	}
	return err
}

// finish records the terminal outcome of a job and emits the final event.
func (e *Evaluator) finish(runID string, res Result, detail map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res.RunID = runID
	status := "succeeded"
	switch {
	case res.Outcome == OutcomeFailed && res.Reason == ReasonCancelled:
		status = "cancelled"
	case res.Outcome == OutcomeFailed:
		status = "failed"
	case res.Review:
		status = "review_required"
	}
	reason := ""
	if res.Outcome == OutcomeFailed {
		reason = res.Reason
	}
	if res.Reason != "" && res.Outcome != OutcomeFailed {
		detail["note"] = res.Reason
	}
	detail["decision"] = res.Decision
	detail["score"] = res.Score
	detail["recommendation"] = res.Recommendation
	dj, _ := json.Marshal(detail)
	var number any
	if res.ReportNumber > 0 {
		number = res.ReportNumber
	}
	now := e.stamp()
	err := e.d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, stage = '', failure_reason = ?, updated_at = ?, finished_at = ? WHERE id = ?`, status, reason, now, now, runID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE evaluation_jobs SET outcome = ?, report_number = ?, detail_json = ?, finished_at = ? WHERE run_id = ?`,
			string(res.Outcome), number, string(dj), now, runID)
		return err
	})
	if err != nil {
		e.d.Log.Error("evaluate: recording the outcome failed", "run", runID, "err", err)
	}
	if e.d.Emit != nil {
		e.d.Emit(Event{RunID: runID, Final: true, Outcome: res.Outcome, Result: &res})
	}
}

// finalReport returns the number of the newest final-kind report for an
// identity key. A screening or gated report does not count.
func (e *Evaluator) finalReport(ctx context.Context, key string) (int64, bool) {
	var n int64
	err := e.d.Store.Read().QueryRowContext(ctx, `SELECT report_number FROM report_index WHERE identity_key = ? AND kind = 'final' ORDER BY report_number DESC LIMIT 1`, key).Scan(&n)
	return n, err == nil
}

// JobOutcome reads a job's outcome and phase; found is false for an unknown run id. A job
// still running has an empty Outcome.
func JobOutcome(ctx context.Context, st *store.Store, runID string) (Result, string, bool, error) {
	var o outcomeRow
	err := st.Read().QueryRowContext(ctx, `SELECT `+outcomeCols+`
		FROM runs r JOIN evaluation_jobs j ON j.run_id = r.id WHERE r.id = ?`, runID).Scan(o.dest()...)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, "", false, nil
	}
	if err != nil {
		return Result{}, "", false, err
	}
	return o.result(), o.stage.String, true, nil
}

// OutcomesSince reads, in one query, the outcome of every job that ended at
// or after since, the earliest first.
func OutcomesSince(ctx context.Context, st *store.Store, since time.Time) ([]Result, error) {
	rows, err := st.Read().QueryContext(ctx, `SELECT `+outcomeCols+`
		FROM evaluation_jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.outcome IS NOT NULL AND j.finished_at >= ? ORDER BY j.finished_at, j.run_id`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var o outcomeRow
		if err := rows.Scan(o.dest()...); err != nil {
			return nil, err
		}
		out = append(out, o.result())
	}
	return out, rows.Err()
}

// outcomeCols are the columns a job's outcome is read from, of its run record
// r and its job row j, in the order outcomeRow.dest scans them.
const outcomeCols = `r.id, r.status, r.stage, r.failure_reason, j.outcome, j.report_number, j.detail_json, j.posting_url, j.inbox_row_id`

type outcomeRow struct {
	runID                          string
	outcome, reason, status, stage sql.NullString
	number, inbox                  sql.NullInt64
	detail, posting                string
}

func (o *outcomeRow) dest() []any {
	return []any{&o.runID, &o.status, &o.stage, &o.reason, &o.outcome, &o.number, &o.detail, &o.posting, &o.inbox}
}

func (o *outcomeRow) result() Result {
	res := Result{RunID: o.runID, Outcome: Outcome(o.outcome.String), ReportNumber: o.number.Int64, Reason: o.reason.String, Review: o.status.String == "review_required",
		PostingURL: o.posting, InboxRowID: o.inbox.Int64}
	var d struct {
		Decision, Note, Explanation, Recommendation string
		Score                                       *float64
		ReviewReason                                string `json:"review_reason"`
		Closed                                      bool
	}
	_ = json.Unmarshal([]byte(o.detail), &d)
	res.Decision, res.Explanation, res.ReviewReason, res.Closed = d.Decision, d.Explanation, d.ReviewReason, d.Closed
	res.Score, res.Recommendation = d.Score, d.Recommendation
	if res.Reason == "" {
		res.Reason = d.Note
	}
	return res
}

func (e *Evaluator) fail(reason string, err error) error {
	return &JobError{Reason: reason, Err: err}
}
