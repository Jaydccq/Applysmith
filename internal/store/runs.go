package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// RunRequest creates a run under an idempotency key.
type RunRequest struct {
	Principal string // authenticated caller
	Operation string // e.g. "apply.start"
	Key       string // the Idempotency-Key header value
	Kind      string
	// Input is the normalized request body; its hash decides replay vs conflict.
	Input []byte
	// Admit, when set, runs inside the claim's transaction after a replay has
	// been ruled out and before anything is inserted; its error aborts the
	// request and is returned as it is. A request that only repeats an earlier
	// one never reaches it.
	Admit func(ctx context.Context) error
	// Created, when set, runs in the same transaction after the run and the
	// claim are inserted, with the new run's id: rows the run cannot exist
	// without commit with it, or nothing does. It does not run for a replay or
	// an Attach.
	Created func(tx *sql.Tx, runID string) error
	// Attach, when set, binds the key to that existing run instead of creating
	// one: a request that joined work already under way.
	Attach string
}

// RunRef identifies the run a key resolved to.
type RunRef struct {
	RunID    string
	Replayed bool
}

// CreateRun inserts the key claim and the run in one transaction. Repeating a
// key with the same input returns the original run id (also after a restart);
// the same key with different input fails with ErrConflict.
func (s *Store) CreateRun(ctx context.Context, req RunRequest) (RunRef, error) {
	if req.Principal == "" || req.Operation == "" || req.Key == "" || req.Kind == "" {
		return RunRef{}, errors.New("store: run request needs principal, operation, key and kind")
	}
	sum := sha256.Sum256(req.Input)
	hash := hex.EncodeToString(sum[:])
	var ref RunRef
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var runID, prior string
		err := tx.QueryRowContext(ctx, `SELECT run_id, input_hash FROM idempotency_keys WHERE principal = ? AND operation = ? AND key = ?`,
			req.Principal, req.Operation, req.Key).Scan(&runID, &prior)
		switch {
		case err == nil:
			if prior != hash {
				return fmt.Errorf("%w: idempotency key reused with different input", ErrConflict)
			}
			ref = RunRef{RunID: runID, Replayed: true}
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if req.Admit != nil {
			if err := req.Admit(ctx); err != nil {
				return err
			}
		}
		now := s.stamp()
		if req.Attach != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys (principal, operation, key, input_hash, run_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				req.Principal, req.Operation, req.Key, hash, req.Attach, now); err != nil {
				return err
			}
			ref = RunRef{RunID: req.Attach}
			return nil
		}
		runID = NewID("run")
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, kind, status, input_json, created_at, updated_at) VALUES (?, ?, 'queued', ?, ?, ?)`,
			runID, req.Kind, string(req.Input), now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys (principal, operation, key, input_hash, run_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			req.Principal, req.Operation, req.Key, hash, runID, now); err != nil {
			return err
		}
		ref = RunRef{RunID: runID}
		if req.Created != nil {
			return req.Created(tx, runID)
		}
		return nil
	})
	return ref, err
}

// Run is a row of the runs table as a client sees it.
type Run struct {
	ID            string
	Kind          string
	Status        string
	Stage         string
	FailureReason string
	CreatedAt     string
	UpdatedAt     string
	FinishedAt    string // empty until the run ends
}

// GetRun returns one run, or ErrNotFound.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	var finished sql.NullString
	err := s.ro.QueryRowContext(ctx, `SELECT id, kind, status, stage, failure_reason, created_at, updated_at, finished_at FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.Kind, &r.Status, &r.Stage, &r.FailureReason, &r.CreatedAt, &r.UpdatedAt, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	r.FinishedAt = finished.String
	return r, typed(err)
}

// SetRun moves a run that has not ended to a status. Ending statuses stamp
// finished_at. A run that already ended is left as it is.
func (s *Store) SetRun(ctx context.Context, id, status, stage, failureReason string) error {
	now := s.stamp()
	finished := any(nil)
	switch status {
	case "succeeded", "failed", "cancelled", "review_required":
		finished = now
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, stage = ?, failure_reason = ?, updated_at = ?, finished_at = ?
			WHERE id = ? AND status IN ('queued', 'running')`, status, stage, failureReason, now, finished, id)
		return err
	})
}

// SetRunOutcome records the line a run ended with, which the run board shows
// as what it came to.
func (s *Store) SetRunOutcome(ctx context.Context, id, outcome string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET outcome = ? WHERE id = ?`, outcome, id)
		return err
	})
}

// CountOpenRuns counts the runs of a kind that have not ended.
func (s *Store) CountOpenRuns(ctx context.Context, kind string) (int, error) {
	var n int
	err := s.ro.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE kind = ? AND status IN ('queued', 'running')`, kind).Scan(&n)
	return n, typed(err)
}

// TouchRun marks a run of the kind that has not ended as active now. A run of
// another kind is left alone.
func (s *Store) TouchRun(ctx context.Context, id, kind string) error {
	now := s.stamp()
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = ? WHERE id = ? AND kind = ? AND status IN ('queued', 'running')`, now, id, kind)
		return err
	})
}

// IdleRuns lists the runs of a kind that have not ended and were last active
// more than idle ago.
func (s *Store) IdleRuns(ctx context.Context, kind string, idle time.Duration) ([]string, error) {
	cutoff := s.now().Add(-idle).UTC().Format(timeLayout)
	rows, err := s.ro.QueryContext(ctx, `SELECT id FROM runs WHERE kind = ? AND status IN ('queued', 'running') AND updated_at < ?`, kind, cutoff)
	if err != nil {
		return nil, typed(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, typed(err)
		}
		ids = append(ids, id)
	}
	return ids, typed(rows.Err())
}
