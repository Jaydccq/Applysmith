package store

import (
	"context"
	"database/sql"
)

// ReconcileStartup settles what a dead process left behind. Open holds the
// exclusive workspace lock, so no live runner owns any of it:
//   - runs still queued or running become review_required (failed for a
//     scheduled occurrence, which never submits, for an extension fill or a fill of
//     the active tab, which
//     holds nothing to review, and for an evaluation, whose inbox row the
//     evaluation pool returns to pending) with reason `interrupted`, and
//     nothing resumes them;
//   - a schedule no longer points at an interrupted run;
//   - resource leases of an earlier start, and expired ones, are cleared.
//
// Open calls it.
func (s *Store) ReconcileStartup(ctx context.Context) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		if err := interruptRuns(ctx, tx, now); err != nil {
			return err
		}
		stmts := []struct {
			q    string
			args []any
		}{
			{`DELETE FROM resource_leases WHERE owner_instance <> ? OR expires_at <= ?`, []any{s.instance, now}},
		}
		for _, st := range stmts {
			if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// interruptRuns ends the runs a dead process left queued or running, and frees
// the schedules that point at them. Startup and restore both run it, so a run
// ends the same way after either; the evaluation pool's recovery settles an
// evaluation's job by the reason `interrupted`.
func interruptRuns(ctx context.Context, tx *sql.Tx, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE schedules SET active_run_id = NULL, last_result = 'failed' WHERE active_run_id IN (SELECT id FROM runs WHERE status IN ('queued', 'running'))`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE runs SET status = CASE WHEN schedule_id IS NULL AND kind NOT IN ('evaluate', 'extension-fill', 'autofill-tab') THEN 'review_required' ELSE 'failed' END,
		failure_reason = 'interrupted', updated_at = ?1, finished_at = ?1 WHERE status IN ('queued', 'running')`, now)
	return err
}
