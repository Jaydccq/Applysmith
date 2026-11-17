// Package store owns the workspace SQLite database: connection policy, schema
// migrations, idempotent run creation, report-artifact commits and the
// consistent backup/restore path. The caller holds the exclusive workspace
// lock before Open.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: cross-compiles with CGO_ENABLED=0
)

const (
	// StateDir and DBFile locate the database inside a workspace.
	StateDir = ".applysmith"
	DBFile   = "applysmith.db"

	defaultBusyTimeout = 5 * time.Second
	defaultQueueDepth  = 64
	timeLayout         = time.RFC3339
)

// readConns is how many reads run at once. The dashboard alone keeps up to
// five in flight (a browser opens six connections to a host and the board
// stream holds one), and the menu bar, the extension, the board sync and the
// runs read beside it: with four, two slow reads were enough to queue the
// dashboard's quick ones behind them.
const readConns = 8

// Options tunes Open. The zero value gives the production policy.
type Options struct {
	// BusyTimeout bounds both the cross-process SQLite lock wait and the
	// in-process wait for the single writer. Default 5s.
	BusyTimeout time.Duration
	// QueueDepth bounds writers waiting for the writer connection; more than
	// this fails immediately with ErrBusy. Default 64.
	QueueDepth int
	// BackupDir receives the validated pre-upgrade snapshot. Default
	// <db dir>/backups.
	BackupDir string
	// Migrations replaces the embedded migration set (tests).
	Migrations fs.FS
	// WorkspaceID is the workspace's durable identifier (workspace.ID). Open
	// stamps it into the database and refuses a database stamped with
	// another one, so a copied database cannot masquerade as this workspace.
	// Empty leaves the database unstamped; Backup then fails.
	WorkspaceID string
	// Now replaces the clock (tests).
	Now func() time.Time
	// Shared joins a database another process may be using: Open takes no
	// part in startup reconciliation (it would mark the live process's runs
	// interrupted) and refuses, with ErrMigrationPending, a database whose
	// schema is behind or absent. The caller then opens it exclusively.
	Shared bool
}

// Store is an open workspace database: one writer connection that runs
// short immediate transactions, and a small pool of query-only readers.
type Store struct {
	path  string
	rw    *sql.DB
	ro    *sql.DB
	queue chan struct{}
	busy  time.Duration
	now   func() time.Time
	// workspaceID is the caller's identifier; instance names this start of
	// the store so leases of an earlier start can be recognised.
	workspaceID string
	instance    string
}

// Open opens (creating if needed) the database at path and applies pending
// migrations before returning, so a returned Store is always at the current
// schema. A database newer than this binary is refused before any write.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = defaultBusyTimeout
	}
	if opts.QueueDepth <= 0 {
		opts.QueueDepth = defaultQueueDepth
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	migs, err := loadMigrations(opts.Migrations)
	if err != nil {
		return nil, err
	}
	if opts.Shared {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%w: the database does not exist yet", ErrMigrationPending)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIO, err)
	}
	// A new database starts as a copy of the image the migrations left an
	// earlier one in this process, when there is one (newImages).
	set := migrationSet(migs)
	fresh := !opts.Shared && absent(path)
	seeded := fresh && seedNew(path, set)

	ro, err := sql.Open("sqlite", dsn(path, opts.BusyTimeout, true))
	if err != nil {
		return nil, typed(err)
	}
	ro.SetMaxOpenConns(readConns)
	ro.SetMaxIdleConns(readConns)
	closeRO := func(err error) (*Store, error) { _ = ro.Close(); return nil, err }

	// Refuse a newer schema and a changed migration before the writer
	// connection (which switches the file to WAL) exists.
	applied, err := readApplied(ctx, ro)
	if err != nil {
		return closeRO(err)
	}
	if err := checkApplied(applied, migs); err != nil {
		return closeRO(err)
	}
	if opts.Shared && len(applied) < len(migs) {
		return closeRO(fmt.Errorf("%w: the database is at schema %d, this binary needs %d", ErrMigrationPending, len(applied), len(migs)))
	}

	rw, err := sql.Open("sqlite", dsn(path, opts.BusyTimeout, false))
	if err != nil {
		return closeRO(typed(err))
	}
	rw.SetMaxOpenConns(1)
	s := &Store{path: path, rw: rw, ro: ro, queue: make(chan struct{}, opts.QueueDepth), busy: opts.BusyTimeout, now: opts.Now,
		workspaceID: opts.WorkspaceID, instance: NewID("inst")}
	fail := func(err error) (*Store, error) { _ = rw.Close(); return closeRO(err) }

	if !opts.Shared {
		if err := s.migrate(ctx, migs, applied, opts.BackupDir); err != nil {
			return fail(err)
		}
		if fresh && !seeded {
			s.migratedNew(ctx, set)
		}
	}
	if err := s.ensureWorkspaceID(ctx); err != nil {
		return fail(err)
	}
	if !opts.Shared {
		if err := s.ReconcileStartup(ctx); err != nil {
			return fail(err)
		}
	}
	return s, nil
}

// OpenWorkspace opens the database of the workspace at root and settles the
// publish journals a dead process left, so every entry point (server, CLI,
// setup) starts from the same state. A crash during a report publish would
// otherwise leave orphan files that block the number's reuse.
func OpenWorkspace(ctx context.Context, root string, opts Options) (*Store, error) {
	s, err := Open(ctx, filepath.Join(root, StateDir, DBFile), opts)
	if err != nil {
		return nil, err
	}
	if err := s.RecoverArtifacts(ctx, root); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("recover report artifacts: %w", err)
	}
	return s, nil
}

// OpenReadOnly opens an existing database for queries only. It applies no
// migration, takes no workspace lock and never writes, so a status or doctor
// run is safe beside a running server. A missing file is an error wrapping
// fs.ErrNotExist; a table the file lacks surfaces when it is queried.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIO, err)
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", defaultBusyTimeout.Milliseconds()))
	q.Add("_pragma", "query_only(1)")
	ro, err := sql.Open("sqlite", fileURL(path, q))
	if err != nil {
		return nil, typed(err)
	}
	ro.SetMaxOpenConns(readConns)
	if err := ro.PingContext(ctx); err != nil {
		_ = ro.Close()
		return nil, typed(err)
	}
	return &Store{path: path, ro: ro, busy: defaultBusyTimeout, now: time.Now}, nil
}

// checkpointTimeout bounds how long Close waits for readers before giving up
// on truncating the WAL; the next open recovers it.
var checkpointTimeout = 2 * time.Second

// Close checkpoints the WAL and releases both pools. A reader that keeps the
// checkpoint from finishing delays Close by at most checkpointTimeout.
func (s *Store) Close() error {
	if s.rw == nil {
		return s.ro.Close()
	}
	return errors.Join(s.checkpoint(), s.rw.Close(), s.ro.Close())
}

func (s *Store) checkpoint() error {
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	conn, err := s.rw.Conn(ctx)
	if err != nil {
		return typed(err)
	}
	defer conn.Close()
	// The connection's busy timeout is the wait for readers; the pooled writer
	// gets its own back afterwards.
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", checkpointTimeout.Milliseconds())); err != nil {
		return typed(err)
	}
	defer conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA busy_timeout = %d", s.busy.Milliseconds()))
	_, err = conn.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return typed(err)
}

// Checkpoint folds the write-ahead log into the database file and truncates
// it. It waits for readers up to the checkpoint timeout.
func (s *Store) Checkpoint() error { return s.checkpoint() }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// warmWait is how long WarmReaders waits for a free read connection. One that
// is busy is already open, and holding the others while waiting would keep
// them from the requests.
const warmWait = 50 * time.Millisecond

// WarmReaders opens every connection of the query-only pool and has each
// parse the schema, so the first requests after a start do not pay for that.
func (s *Store) WarmReaders(ctx context.Context) error {
	conns := make([]*sql.Conn, 0, readConns)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range readConns {
		wait, cancel := context.WithTimeout(ctx, warmWait)
		c, err := s.ro.Conn(wait)
		cancel()
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return typed(err)
		}
		conns = append(conns, c)
		var n int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&n); err != nil {
			return typed(err)
		}
	}
	return nil
}

// Read returns the query-only pool. Statements that write fail on it.
func (s *Store) Read() *sql.DB { return s.ro }

// Write runs fn in one short immediate transaction on the single writer.
// fn must not call a browser, a provider or wait for a user: it holds the
// only writer. If the writer is not available within the busy timeout, or the
// queue is full, it returns ErrBusy and fn never runs, so the caller must not
// perform the external side effect that fn would have guarded.
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.rw == nil {
		return ErrReadOnly
	}
	select {
	case s.queue <- struct{}{}:
		defer func() { <-s.queue }()
	default:
		return fmt.Errorf("%w: write queue full", ErrBusy)
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.busy)
	conn, err := s.rw.Conn(waitCtx)
	cancel()
	if err != nil {
		return waitError(ctx, err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return waitError(ctx, err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return typed(err)
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return typed(err)
	}
	return nil
}

func (s *Store) stamp() string { return s.now().UTC().Format(timeLayout) }

func (s *Store) ensureWorkspaceID(ctx context.Context) error {
	if s.workspaceID == "" {
		return nil
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_meta (key, value) VALUES ('workspace_id', ?)`, s.workspaceID); err != nil {
			return err
		}
		stamped, err := metaValue(ctx, tx, "workspace_id")
		if err != nil {
			return err
		}
		if stamped != s.workspaceID {
			return fmt.Errorf("%w: the database belongs to another workspace", ErrConflict)
		}
		return nil
	})
}

func metaValue(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, typed(err)
}

// dsn builds a modernc connection string. Every connection sets foreign_keys
// and the busy timeout; the writer also sets WAL and synchronous=FULL and
// begins transactions with BEGIN IMMEDIATE so lock waits happen at begin.
func dsn(path string, busy time.Duration, readOnly bool) string {
	q := url.Values{}
	pragma := func(p string) { q.Add("_pragma", p) }
	pragma(fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	pragma("foreign_keys(1)")
	if readOnly {
		pragma("query_only(1)")
	} else {
		pragma("journal_mode(WAL)")
		pragma("synchronous(2)")
		q.Set("_txlock", "immediate")
	}
	return fileURL(path, q)
}

func fileURL(path string, q url.Values) string {
	p := filepath.ToSlash(path)
	if len(p) > 0 && p[0] != '/' {
		p = "/" + p // Windows drive paths
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String()
}

// NewID returns prefix, an underscore and 24 random hex digits: the id of a
// new row.
func NewID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
