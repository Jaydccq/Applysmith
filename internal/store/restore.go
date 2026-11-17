package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"applysmith/internal/workspace"
)

// RestoreOptions parameterizes a restore.
type RestoreOptions struct {
	Now func() time.Time

	// afterVerify runs between the verification and the copy (tests).
	afterVerify func()
}

// RestoreBackup verifies dir and restores it into to, which must not exist or
// must be an empty directory. The new workspace is assembled in a private
// sibling directory and moved into place only once complete, so a refusal or
// failure leaves nothing usable at the destination. The source backup is
// never modified.
//
// The restored database is made safe to open: device and dashboard sessions and
// execution leases are cleared, schedules and outgoing notifications are disabled,
// interrupted runs end as a restart ends them (ReconcileStartup), and a
// submitting row of the submission ledger becomes may-have-submitted, which
// blocks automatic resubmission. A fresh
// workspace id is stamped so credentials and server identity are provisioned
// anew: the new workspace gets its own identifier file, and the database is
// stamped with the same value.
func RestoreBackup(ctx context.Context, dir, to string, opts RestoreOptions) (err error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	m, err := VerifyBackup(ctx, dir)
	if err != nil {
		return err
	}
	if opts.afterVerify != nil {
		opts.afterVerify()
	}
	to, err = filepath.Abs(to)
	if err != nil {
		return err
	}
	if err := requireEmptyOrMissing(to); err != nil {
		return err
	}
	parent := filepath.Dir(to)
	if _, err := os.Stat(parent); err != nil {
		return fmt.Errorf("%w: %w", ErrIO, err)
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(to)+".restore-")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIO, err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stage) // staging dir created by MkdirTemp above
		}
	}()
	if err = os.Chmod(stage, 0o700); err != nil {
		return err
	}

	// Each copy is measured as it is written and must equal the manifest, so
	// bytes that changed after the verification are refused, not restored.
	for _, e := range m.Files {
		dst := filepath.Join(stage, filepath.FromSlash(e.Path))
		if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return diskError(err)
		}
		if err = copyVerified(ctx, entryPath(dir, e, false), dst, e); err != nil {
			return err
		}
	}
	dbDst := filepath.Join(stage, StateDir, DBFile)
	if err = os.MkdirAll(filepath.Dir(dbDst), 0o700); err != nil {
		return diskError(err)
	}
	if err = copyVerified(ctx, entryPath(dir, m.Database, true), dbDst, m.Database); err != nil {
		return err
	}
	newID := workspace.NewID()
	if err = workspace.CreateID(filepath.Dir(dbDst), newID); err != nil {
		return fmt.Errorf("%w: %w", ErrIO, err)
	}
	if err = reconcileRestored(ctx, dbDst, m.WorkspaceID, newID, opts.Now().UTC()); err != nil {
		return err
	}
	if m.Demo {
		if err = workspace.Open(stage).MarkDemo(); err != nil {
			return fmt.Errorf("%w: %w", ErrIO, err)
		}
	}

	if err = os.Remove(to); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrIO, err)
	}
	if err = os.Rename(stage, to); err != nil {
		return fmt.Errorf("%w: %w", ErrIO, err)
	}
	return nil
}

func requireEmptyOrMissing(to string) error {
	entries, err := os.ReadDir(to)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%w: restore target %s is not an empty directory", ErrConflict, to)
	case len(entries) > 0:
		return fmt.Errorf("%w: restore target %s is not empty", ErrConflict, to)
	}
	return nil
}

func copyVerified(ctx context.Context, src, dst string, want FileEntry) error {
	sum, n, err := copyHashed(ctx, src, dst)
	if err != nil {
		return err
	}
	if sum != want.SHA256 || n != want.Size {
		return fmt.Errorf("%w: %s changed after it was verified", ErrInvalidBackup, want.Path)
	}
	return nil
}

// reconcileRestored applies the restore-time safety changes in one
// transaction and then proves the result with an integrity check.
func reconcileRestored(ctx context.Context, dbPath, originalID, newID string, at time.Time) error {
	now := at.Format(timeLayout)
	db, err := sql.Open("sqlite", dsn(dbPath, defaultBusyTimeout, false))
	if err != nil {
		return typed(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return typed(err)
	}
	defer tx.Rollback()
	if err := interruptRuns(ctx, tx, now); err != nil {
		return fmt.Errorf("store: restore reconcile: %w", typed(err))
	}
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM device_sessions`, nil},
		{`DELETE FROM dashboard_sessions`, nil},
		{`DELETE FROM resource_leases`, nil},
		{`UPDATE schedules SET enabled = 0, active_run_id = NULL, last_result = CASE WHEN last_result = 'running' THEN 'failed' ELSE last_result END`, nil},
		// The submission ledger's stamps carry milliseconds (internal/ledger).
		{`UPDATE submissions SET state = 'may-have-submitted', updated_at = ? WHERE state = 'submitting'`, []any{at.Format("2006-01-02T15:04:05.000Z")}},
		{`INSERT INTO workspace_meta (key, value) VALUES ('workspace_id', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, []any{newID}},
		{`INSERT INTO workspace_meta (key, value) VALUES ('restored_from', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, []any{originalID}},
		{`INSERT INTO workspace_meta (key, value) VALUES ('restored_at', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, []any{now}},
		// The notify package's own channel switches; they stay off until the user
		// turns them on under Settings, Notifications.
		{`INSERT INTO notify_settings (key, value, updated_at) VALUES ('desktop', 'false', ?), ('telegram', 'false', ?)
			ON CONFLICT (key) DO UPDATE SET value = 'false', updated_at = excluded.updated_at`, []any{now, now}},
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s.q, s.args...); err != nil {
			return fmt.Errorf("store: restore reconcile: %w", typed(err))
		}
	}
	if err := tx.Commit(); err != nil {
		return typed(err)
	}
	if err := checkIntegrity(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return typed(err)
	}
	return nil
}
