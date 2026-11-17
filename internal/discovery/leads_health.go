package discovery

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// silentRuns is how many scheduled runs in a row may find nothing before the
// source is called silent.
const silentRuns = 3

// SourceAlarm is a scheduled discovery source that stopped producing.
type SourceAlarm struct {
	Source string
	// Status is the status text of the run that raised it.
	Status string
	// LastProductive is when the source last inserted a row.
	LastProductive time.Time
	// Action is what the user has to do, set when the status names a sign-in,
	// session, credential or key problem, or a billing, credit or quota one.
	Action string
	// Retryable is false when Action is set: another run cannot fix it.
	Retryable bool
}

var (
	authProblemRE    = lazyregexp.New(`(?i)sign[- ]?in|session|credential|api key|\bkey\b|auth|login|password`)
	billingProblemRE = lazyregexp.New(`(?i)billing|credit|quota|payment|insufficient|spending limit`)
)

// MarkScheduled records that a run was started by the scheduler, so the
// silent-source check counts only scheduled runs.
func (r *Repo) MarkScheduled(ctx context.Context, runID string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE scan_runs SET record_json = json_set(record_json, '$.params.scheduled', json('true')) WHERE id = ?`, runID)
		return err
	})
}

// SourceAlarmFor decides whether a finished scheduled run raises an alarm: the
// source inserted a row in an earlier run, and this run ended failed, blocked
// or needs-sign-in, or the last three scheduled runs all discovered nothing.
// A source that never produced and a skipped run raise none.
func (r *Repo) SourceAlarmFor(ctx context.Context, rec Record) (*SourceAlarm, error) {
	if rec.Status == RunSkipped || rec.Status == RunRunning {
		return nil, nil
	}
	var last sql.NullString
	err := r.s.Read().QueryRowContext(ctx, `SELECT max(finished_at) FROM scan_runs WHERE source = ? AND id <> ? AND status IN ('completed', 'failed', 'blocked', 'needs-sign-in')
		AND coalesce(json_extract(record_json, '$.counts.inserted'), 0) > 0`, rec.Source, rec.ID).Scan(&last)
	if err != nil || !last.Valid {
		return nil, err
	}
	failed := rec.Status == RunFailed || rec.Status == RunBlocked || rec.Status == RunNeedsSignIn
	silent := false
	if !failed {
		if silent, err = r.silentSource(ctx, rec.Source); err != nil {
			return nil, err
		}
	}
	if !failed && !silent {
		return nil, nil
	}
	text := rec.Status
	if silent {
		text = fmt.Sprintf("%d scheduled runs in a row discovered nothing", silentRuns)
	}
	if d := strings.TrimSpace(firstNonEmpty(rec.Error, rec.Reason)); d != "" {
		text += ": " + d
	}
	a := &SourceAlarm{Source: rec.Source, Status: text, LastProductive: parseStamp(last), Retryable: true}
	switch {
	case rec.Status == RunNeedsSignIn || authProblemRE.MatchString(text):
		a.Action, a.Retryable = "Sign in or fix the key for "+rec.Source+", then run it again.", false
	case billingProblemRE.MatchString(text):
		a.Action, a.Retryable = "Fix the billing, credit or quota of "+rec.Source+", then run it again.", false
	}
	return a, nil
}

// silentSource reports whether the latest scheduled runs of a source, not
// counting skipped ones, all discovered nothing.
func (r *Repo) silentSource(ctx context.Context, source string) (bool, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT coalesce(json_extract(record_json, '$.counts.discovered'), 0) FROM scan_runs
		WHERE source = ? AND status NOT IN ('running', 'skipped') AND coalesce(json_extract(record_json, '$.params.scheduled'), 0) = 1
		ORDER BY started_at DESC, id DESC LIMIT ?`, source, silentRuns)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var d int
		if err := rows.Scan(&d); err != nil {
			return false, err
		}
		if d != 0 {
			return false, nil
		}
		n++
	}
	return n == silentRuns, rows.Err()
}
