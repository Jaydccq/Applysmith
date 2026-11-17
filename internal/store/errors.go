package store

import (
	"context"
	"errors"
	"fmt"

	"modernc.org/sqlite"
)

// Typed failures. Callers branch with errors.Is; none of them may be turned
// into a successful safety decision.
var (
	ErrBusy             = errors.New("store: busy")
	ErrDiskFull         = errors.New("store: disk full")
	ErrIO               = errors.New("store: i/o error")
	ErrCorrupt          = errors.New("store: database corrupt")
	ErrSchemaTooNew     = errors.New("store: schema-too-new")
	ErrChecksumMismatch = errors.New("store: migration checksum mismatch")
	ErrConflict         = errors.New("store: conflict")
	ErrNotFound         = errors.New("store: not found")
	ErrInvalidBackup    = errors.New("store: invalid backup")
	ErrUnsafePath       = errors.New("store: unsafe path")
	ErrConcurrentChange = errors.New("store: workspace files changed during backup")
	ErrNoWorkspaceID    = errors.New("store: the database carries no workspace identifier")
	ErrReadOnly         = errors.New("store: opened read-only")
	ErrMigrationPending = errors.New("store: migration-pending")
)

// SQLite primary result codes (sqlite.org/rescode.html).
const (
	codeBusy     = 5
	codeLocked   = 6
	codeIOErr    = 10
	codeCorrupt  = 11
	codeFull     = 13
	codeCantOpen = 14
	codeNotADB   = 26
)

// typed maps a driver error onto the package's sentinel errors and keeps the
// original error in the chain for diagnostics.
func typed(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	var kind error
	switch se.Code() & 0xff {
	case codeBusy, codeLocked:
		kind = ErrBusy
	case codeFull:
		kind = ErrDiskFull
	case codeIOErr, codeCantOpen:
		kind = ErrIO
	case codeCorrupt, codeNotADB:
		kind = ErrCorrupt
	default:
		return err
	}
	if errors.Is(err, kind) {
		return err
	}
	return fmt.Errorf("%w: %w", kind, err)
}

// waitError distinguishes "the caller gave up" from "the bounded wait for the
// writer ran out".
func waitError(parent context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: writer not available within the busy timeout", ErrBusy)
	}
	return typed(err)
}
