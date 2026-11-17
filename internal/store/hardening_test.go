package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func TestRestoreRefusesBytesChangedAfterVerification(t *testing.T) {
	f := newFixture(t)
	swaps := map[string]func(t *testing.T, dir string){
		"same size, other bytes": func(t *testing.T, dir string) {
			target := filepath.Join(dir, backupFilesDir, "cv.md")
			orig, _ := os.ReadFile(target)
			if err := os.WriteFile(target, bytes.Repeat([]byte("X"), len(orig)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"replaced by a symlink": func(t *testing.T, dir string) {
			target := filepath.Join(dir, backupFilesDir, "cv.md")
			orig, _ := os.ReadFile(target)
			outside := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.WriteFile(outside, orig, 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(target)
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
		},
		"database rewritten": func(t *testing.T, dir string) {
			db := filepath.Join(dir, backupDBName)
			b, _ := os.ReadFile(db)
			b[len(b)-1] ^= 0xff
			if err := os.WriteFile(db, b, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, swap := range swaps {
		t.Run(name, func(t *testing.T) {
			dir, _ := f.backup(t)
			to := filepath.Join(t.TempDir(), "restored")
			err := RestoreBackup(f.ctx, dir, to, RestoreOptions{afterVerify: func() { swap(t, dir) }})
			if !errors.Is(err, ErrInvalidBackup) && !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("err = %v", err)
			}
			if _, statErr := os.Stat(to); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a restored workspace exists after the refusal: %v", statErr)
			}
		})
	}
}

func TestRestoreGivesTheNewWorkspaceItsOwnIdentity(t *testing.T) {
	f := newFixture(t)
	dir, m := f.backup(t)
	if m.WorkspaceID != testWorkspaceID {
		t.Fatalf("the manifest names %q, not the caller's workspace id", m.WorkspaceID)
	}
	to := restoreTo(t, f, dir)
	id, err := workspace.Open(to).ID()
	if err != nil || id == testWorkspaceID {
		t.Fatalf("restored id = %q, %v", id, err)
	}
	r := openRestored(t, to) // refuses a database stamped with another id
	if stamped, _ := metaValue(f.ctx, r.ro, "workspace_id"); stamped != id {
		t.Errorf("database stamp %q differs from the id file %q", stamped, id)
	}
	if from, _ := metaValue(f.ctx, r.Read(), "restored_from"); from != testWorkspaceID {
		t.Errorf("restored_from = %q", from)
	}
}

func TestBackupNeedsAWorkspaceIdentifier(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), StateDir, DBFile), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := filepath.Join(t.TempDir(), "bk")
	if _, err := s.Backup(context.Background(), BackupOptions{Out: out}); !errors.Is(err, ErrNoWorkspaceID) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a partial backup was left behind")
	}
}

func TestBackupLeavesTheWriterFreeDuringTheCopy(t *testing.T) {
	f := newFixture(t)
	var writeErr error
	_, err := f.s.Backup(f.ctx, BackupOptions{Out: filepath.Join(t.TempDir(), "bk"), WorkspaceRoot: f.root,
		afterVacuum: func() {
			ctx, cancel := context.WithTimeout(f.ctx, time.Second)
			defer cancel()
			writeErr = f.s.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO workspace_meta (key, value) VALUES ('during-copy', '1')`)
				return err
			})
		}})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatalf("a write during the file copy failed: %v", writeErr)
	}
}

func TestBackupStopsWhenItsContextEnds(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	out := filepath.Join(t.TempDir(), "bk")
	_, err := f.s.Backup(ctx, BackupOptions{Out: out, WorkspaceRoot: f.root, afterVacuum: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("a cancelled backup left its output")
	}
	// The store is still usable and a new backup works.
	if _, err := f.s.Backup(f.ctx, BackupOptions{Out: filepath.Join(t.TempDir(), "bk2"), WorkspaceRoot: f.root}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyStopsMidFileWhenTheContextEnds(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(src, bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := copyHashed(ctx, src, filepath.Join(t.TempDir(), "dst")); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy: %v", err)
	}
	if _, _, err := hashPath(ctx, src); !errors.Is(err, context.Canceled) {
		t.Fatalf("hash: %v", err)
	}
}

func TestCloseDoesNotWaitLongerThanTheCheckpointTimeout(t *testing.T) {
	old := checkpointTimeout
	checkpointTimeout = 150 * time.Millisecond
	defer func() { checkpointTimeout = old }()
	s := openT(t, Options{BusyTimeout: 30 * time.Second})
	ctx := context.Background()
	if err := exec(t, s, `INSERT INTO workspace_meta (key, value) VALUES ('a', '1')`); err != nil {
		t.Fatal(err)
	}
	// A reader pinned to an older snapshot keeps TRUNCATE from finishing.
	readerDB, err := sql.Open("sqlite", dsn(s.Path(), time.Second, true))
	if err != nil {
		t.Fatal(err)
	}
	defer readerDB.Close()
	tx, err := readerDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM workspace_meta`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := exec(t, s, `INSERT INTO workspace_meta (key, value) VALUES ('b', '1')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = s.Close()
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %v", d)
	}
}

func TestPublishFlushFailureLeavesNoFilesAndNoRow(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	root := t.TempDir()
	boom := errors.New("injected power failure")
	var flushed []string
	syncParent = func(dir string) error {
		if dir != filepath.Join(root, "reports") {
			return realSyncParent(dir)
		}
		flushed = append(flushed, dir)
		return boom
	}
	defer func() { syncParent = realSyncParent }()

	_, err := publishOne(ctx, s, root, func(int64) string { return "reports/001-x.md" }, []byte("body"))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if len(flushed) != 1 {
		t.Fatalf("flushed %v", flushed)
	}
	if n := count(t, s.ro, `SELECT count(*) FROM report_artifacts`); n != 0 {
		t.Fatalf("%d rows; a row must not exist before the name is durable", n)
	}
	if got := publishFilesOnly(t, root); len(got) != 0 {
		t.Fatalf("files left behind: %v", got)
	}
	// The rolled-back number is reused without a conflict.
	syncParent = realSyncParent
	if a, err := publishOne(ctx, s, root, func(int64) string { return "reports/001-x.md" }, []byte("body")); err != nil || a.Number != 1 {
		t.Fatalf("republish = %+v, %v", a, err)
	}
}
