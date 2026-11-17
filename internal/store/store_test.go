package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testWorkspaceID = "0123456789abcdef0123456789abcdef"

func openT(t *testing.T, opts Options) *Store {
	t.Helper()
	if opts.WorkspaceID == "" {
		opts.WorkspaceID = testWorkspaceID
	}
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), StateDir, DBFile), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func pragma(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestConnectionPolicy(t *testing.T) {
	s := openT(t, Options{})
	for _, db := range []*sql.DB{s.rw, s.ro} {
		if got := pragma(t, db, "foreign_keys"); got != "1" {
			t.Errorf("foreign_keys = %s", got)
		}
		if got := pragma(t, db, "busy_timeout"); got != "5000" {
			t.Errorf("busy_timeout = %s", got)
		}
	}
	if got := pragma(t, s.rw, "journal_mode"); got != "wal" {
		t.Errorf("journal_mode = %s", got)
	}
	if got := pragma(t, s.rw, "synchronous"); got != "2" {
		t.Errorf("synchronous = %s (want FULL)", got)
	}
	if _, err := s.ro.Exec(`INSERT INTO workspace_meta VALUES ('x', 'y')`); err == nil {
		t.Error("read pool accepted a write")
	}
	// A checkpoint waits for readers on its own timeout and hands the writer
	// back with the writer's.
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if got := pragma(t, s.rw, "busy_timeout"); got != "5000" {
		t.Errorf("busy_timeout after a checkpoint = %s", got)
	}
}

func TestEmptyDatabaseMigrates(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	var n int
	if err := s.ro.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != SchemaVersion() {
		t.Fatalf("applied = %d, %v; want %d", n, err, SchemaVersion())
	}
	for _, table := range []string{"runs", "idempotency_keys", "tracker_entries", "tracker_events", "schedules", "report_artifacts", "device_sessions", "resource_leases"} {
		if err := s.ro.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	if id, err := metaValue(ctx, s.ro, "workspace_id"); err != nil || id != testWorkspaceID {
		t.Errorf("workspace id = %q, %v", id, err)
	}
	if err := checkIntegrity(ctx, s.ro); err != nil {
		t.Error(err)
	}
}

func TestForeignKeyEnforcement(t *testing.T) {
	s := openT(t, Options{})
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO tracker_events (entry_id, at, actor, kind) VALUES (999, 'now', 'user', 'created')`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("want foreign key failure, got %v", err)
	}
}

const insertEntry = `INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2026-01-01', 'Example Corp', 'Engineer', 'evaluated', 'greenhouse:example/1', 'now', 'now')`

func TestTrackerEventConsistency(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(insertEntry); err != nil {
			return err
		}
		// 'bogus' violates the kind constraint, as a failing event write would.
		_, err := tx.Exec(`INSERT INTO tracker_events (entry_id, at, actor, kind) VALUES (1, 'now', 'user', 'bogus')`)
		return err
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	if n := count(t, s.ro, `SELECT (SELECT count(*) FROM tracker_entries) + (SELECT count(*) FROM tracker_events)`); n != 0 {
		t.Fatalf("partial commit: %d rows", n)
	}
}

func TestTrackerRowConstraints(t *testing.T) {
	s := openT(t, Options{})
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2026-01-01', 'Example Corp', '  ', 'evaluated', 'greenhouse:example/1', 'now', 'now')`)
		return err
	})
	if err == nil {
		t.Fatal("blank role accepted")
	}
}

func TestWriterContentionReturnsBusy(t *testing.T) {
	t.Run("another connection holds the database lock", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), DBFile)
		a, err := Open(context.Background(), path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		b, err := Open(context.Background(), path, Options{BusyTimeout: 200 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()

		held, release := make(chan struct{}), make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.Write(context.Background(), func(tx *sql.Tx) error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		start := time.Now()
		ran := false
		err = b.Write(context.Background(), func(*sql.Tx) error { ran = true; return nil })
		close(release)
		wg.Wait()
		if !errors.Is(err, ErrBusy) || ran {
			t.Fatalf("err = %v, ran = %v; want ErrBusy without running", err, ran)
		}
		if d := time.Since(start); d < 150*time.Millisecond || d > 3*time.Second {
			t.Errorf("waited %v, want about the 200ms busy timeout", d)
		}
	})

	t.Run("in-process writer wait is bounded", func(t *testing.T) {
		s := openT(t, Options{BusyTimeout: 100 * time.Millisecond})
		held, release := make(chan struct{}), make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.Write(context.Background(), func(*sql.Tx) error { close(held); <-release; return nil })
		}()
		<-held
		err := s.Write(context.Background(), func(*sql.Tx) error { return nil })
		close(release)
		<-done
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("queue is bounded", func(t *testing.T) {
		s := openT(t, Options{QueueDepth: 1})
		held, release := make(chan struct{}), make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.Write(context.Background(), func(*sql.Tx) error { close(held); <-release; return nil })
		}()
		<-held
		err := s.Write(context.Background(), func(*sql.Tx) error { return nil })
		close(release)
		<-done
		if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "queue full") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("caller cancellation is not reported as busy", func(t *testing.T) {
		s := openT(t, Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Write(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDiskFullIsATypedFailure(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	var pages int
	if err := s.rw.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	// The writer pool holds one connection, so the limit sticks.
	if _, err := s.rw.Exec(`PRAGMA max_page_count = ` + strconv.Itoa(pages+4)); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateRun(ctx, RunRequest{Principal: "cli", Operation: "apply", Key: "k", Kind: "apply", Input: make([]byte, 1<<20)})
	if !errors.Is(err, ErrDiskFull) {
		t.Fatalf("err = %v, want ErrDiskFull", err)
	}
	if _, err := s.GetRun(ctx, "anything"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("store unusable after failure: %v", err)
	}
	if n := count(t, s.ro, `SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("run committed despite failure")
	}
}

func TestIntegrityCheckDetectsCorruption(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	// Break a foreign key behind the engine's back.
	if _, err := s.rw.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`INSERT INTO tracker_events (entry_id, at, actor, kind) VALUES (42, 'now', 'user', 'created')`); err != nil {
		t.Fatal(err)
	}
	if err := checkIntegrity(ctx, s.ro); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}
