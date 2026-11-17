package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
)

// MigrationState is what a database file holds compared with this binary.
type MigrationState struct {
	Exists    bool
	Applied   int // newest applied schema version, 0 for none
	Supported int // newest schema version this binary knows
}

// Pending reports whether this binary has migrations the file lacks.
func (m MigrationState) Pending() bool { return m.Applied < m.Supported }

// InspectMigrations reads a database's migration record without migrating,
// creating or locking it, so it is safe while a server runs. It returns
// ErrSchemaTooNew or ErrChecksumMismatch exactly as Open would.
func InspectMigrations(ctx context.Context, path string) (MigrationState, error) {
	migs, err := loadMigrations(nil)
	if err != nil {
		return MigrationState{}, err
	}
	st := MigrationState{Supported: len(migs)}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		return st, fmt.Errorf("%w: %w", ErrIO, err)
	}
	st.Exists = true
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", defaultBusyTimeout.Milliseconds()))
	db, err := sql.Open("sqlite", fileURL(path, q))
	if err != nil {
		return st, typed(err)
	}
	defer db.Close()
	return inspect(ctx, db, st, migs)
}

// Migrations is InspectMigrations through the store's own readers. A new
// connection reads the whole schema before its first query, which made this
// check a large part of the setup state the dashboard asks for on Home.
func (s *Store) Migrations(ctx context.Context) (MigrationState, error) {
	migs, err := loadMigrations(nil)
	if err != nil {
		return MigrationState{}, err
	}
	return inspect(ctx, s.ro, MigrationState{Exists: true, Supported: len(migs)}, migs)
}

func inspect(ctx context.Context, db *sql.DB, st MigrationState, migs []migration) (MigrationState, error) {
	applied, err := readApplied(ctx, db)
	if err != nil {
		return st, err
	}
	for _, a := range applied {
		st.Applied = max(st.Applied, a.version)
	}
	return st, checkApplied(applied, migs)
}
