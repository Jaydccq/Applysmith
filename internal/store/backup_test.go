package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

const secretMarker = "synthetic-cookie-secret-value"

type fixture struct {
	root string
	s    *Store
	ctx  context.Context
}

// newFixture builds a workspace with user files, runtime secrets that must
// never be exported, and database rows for every restore rule.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(root, StateDir, DBFile), Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("profile.yml", "name: Alex Example\n")
	write("cv.md", "# Alex Example\n")
	write("knowledge/notes.md", "note\n")
	write("reports/001-example-corp.md", "# report\n")
	write(".applysmith/browser/Default/Cookies", secretMarker)
	write(".applysmith/logs/server.log", secretMarker)
	write("resumes/ada.pdf", "%PDF synthetic")

	if _, err := publishOne(ctx, s, root, func(int64) string { return "reports/002-example.md" }, []byte("# two\n")); err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			insertEntry,
			`INSERT INTO tracker_events (entry_id, at, actor, kind, new_status) VALUES (1, 'now', 'user', 'created', 'evaluated')`,
			`INSERT INTO schedules (id, name, job_kind, enabled, dtstart, active_run_id, last_result, created_at, updated_at) VALUES (1, 'daily', 'scan', 1, 'x', 'sched-run', 'running', 'x', 'x')`,
			`INSERT INTO runs (id, kind, status, schedule_id, scheduled_for, created_at, updated_at) VALUES ('sched-run', 'scan', 'running', 1, '2026-01-01T07:30:00Z', 'x', 'x')`,
			`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES ('apply-run', 'apply', 'running', 'x', 'x')`,
			`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES ('done-run', 'apply', 'succeeded', 'x', 'x')`,
			`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES ('eval-run', 'evaluate', 'running', 'x', 'x')`,
			`INSERT INTO submissions (identity_key, url, host, state, source, observed_at, created_at, updated_at, claim_id, pid, claimed_at)
				VALUES ('greenhouse:example/1', 'https://example.com/1', 'example.com', 'submitting', 'press', 'x', 'x', 'x', 'c1', 1, 'x'),
				('greenhouse:example/2', 'https://example.com/2', 'example.com', 'submitted', 'press', 'x', 'x', 'x', 'c2', 1, 'x')`,
			`INSERT INTO resource_leases (name, holder, owner_pid, owner_instance, acquired_at, expires_at) VALUES ('browser', 'sched-run', 1, 'inst_old', 'x', '2099-01-01T00:00:00Z')`,
			`INSERT INTO device_sessions (id, label, token_sha256, created_at) VALUES ('dev1', 'phone', 'abc', 'x')`,
			`INSERT INTO notify_settings (key, value, updated_at) VALUES ('desktop', 'true', 'x'), ('telegram', 'true', 'x'), ('failed_scans', 'true', 'x')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{root: root, s: s, ctx: ctx}
}

func (f *fixture) backup(t *testing.T) (string, *Manifest) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bk")
	m, err := f.s.Backup(f.ctx, BackupOptions{Out: out, WorkspaceRoot: f.root, ProductVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return out, m
}

func restoreTo(t *testing.T, f *fixture, dir string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), "restored")
	if err := RestoreBackup(f.ctx, dir, to, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return to
}

func openRestored(t *testing.T, to string) *Store {
	t.Helper()
	id, err := workspace.Open(to).ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), filepath.Join(to, StateDir, DBFile), Options{WorkspaceID: id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBackupIncludesWALCommits(t *testing.T) {
	f := newFixture(t)
	if fi, err := os.Stat(f.s.Path() + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("premise: rows should live only in the WAL (%v)", err)
	}
	dir, m := f.backup(t)
	if _, err := VerifyBackup(f.ctx, dir); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != SchemaVersion() || m.WorkspaceID == "" || m.ProductVersion != "test" {
		t.Errorf("manifest = %+v", m)
	}
	r := openRestored(t, restoreTo(t, f, dir))
	if n := count(t, r.Read(), `SELECT count(*) FROM tracker_entries`); n != 1 {
		t.Errorf("tracker rows = %d", n)
	}
	if n := count(t, r.Read(), `SELECT count(*) FROM tracker_events`); n != 1 {
		t.Errorf("events = %d", n)
	}
	if n := count(t, r.Read(), `SELECT count(*) FROM submissions`); n != 2 {
		t.Errorf("ledger rows = %d", n)
	}
	if n := count(t, r.Read(), `SELECT count(*) FROM report_artifacts WHERE status = 'ready'`); n != 1 {
		t.Errorf("artifacts = %d", n)
	}
	if err := checkIntegrity(f.ctx, r.ro); err != nil {
		t.Error(err)
	}
}

func TestOfflineBackupOfClosedDatabase(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bk")
	if _, err := BackupOffline(f.ctx, f.s.Path(), BackupOptions{Out: out, WorkspaceRoot: f.root}); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(f.ctx, out); err != nil {
		t.Fatal(err)
	}
}

// `applysmith backup verify bk`: a relative directory works like an absolute one.
func TestRelativeBackupDirectoryVerifiesAndRestores(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.backup(t)
	t.Chdir(filepath.Dir(dir))
	if _, err := VerifyBackup(f.ctx, filepath.Base(dir)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := RestoreBackup(f.ctx, filepath.Base(dir), "restored", RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	openRestored(t, filepath.Join(filepath.Dir(dir), "restored"))
}

func TestRestoredSchedulerAndSubmitUncertainty(t *testing.T) {
	f := newFixture(t)
	dir, m := f.backup(t)
	r := openRestored(t, restoreTo(t, f, dir))
	q := r.Read()

	if n := count(t, q, `SELECT count(*) FROM schedules WHERE enabled = 1`); n != 0 {
		t.Errorf("enabled schedules after restore = %d", n)
	}
	if n := count(t, q, `SELECT count(*) FROM schedules WHERE active_run_id IS NOT NULL`); n != 0 {
		t.Errorf("schedule still points at a run")
	}
	if n := count(t, q, `SELECT count(*) FROM resource_leases`); n != 0 {
		t.Errorf("leases = %d", n)
	}
	if n := count(t, q, `SELECT count(*) FROM device_sessions`); n != 0 {
		t.Errorf("device sessions = %d", n)
	}
	// The live ledger: a press in flight in the backup may have submitted.
	if n := count(t, q, `SELECT count(*) FROM submissions WHERE state = 'submitting'`); n != 0 {
		t.Errorf("submitting ledger rows = %d", n)
	}
	if n := count(t, q, `SELECT count(*) FROM submissions WHERE identity_key = 'greenhouse:example/1' AND state = 'may-have-submitted'`); n != 1 {
		t.Errorf("interrupted press not marked uncertain")
	}
	if n := count(t, q, `SELECT count(*) FROM submissions WHERE state = 'submitted'`); n != 1 {
		t.Errorf("settled press changed")
	}
	// Runs end as a restart ends them (ReconcileStartup), so the evaluation
	// pool's recovery settles a restored evaluation's job.
	runs := func(db *sql.DB) string {
		var out string
		if err := db.QueryRow(`SELECT group_concat(id || ' ' || status || ' ' || failure_reason || ' ' || (finished_at IS NOT NULL), ', ') FROM (SELECT * FROM runs ORDER BY id)`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	path := f.s.Path()
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(f.ctx, path, Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got, want := runs(q), runs(restarted.Read()); got != want {
		t.Errorf("restored runs:\n %s\nafter a restart:\n %s", got, want)
	}
	if n := count(t, q, `SELECT count(*) FROM notify_settings WHERE key IN ('desktop', 'telegram') AND value = 'false'`); n != 2 {
		t.Errorf("desktop and telegram notices not both off (%d)", n)
	}
	if n := count(t, q, `SELECT count(*) FROM notify_settings WHERE key = 'failed_scans' AND value = 'true'`); n != 1 {
		t.Errorf("which notices to send changed")
	}
	id, _ := metaValue(f.ctx, r.ro, "workspace_id")
	if id == m.WorkspaceID || id == "" {
		t.Errorf("restored workspace kept identity %q", id)
	}
	if from, _ := metaValue(f.ctx, q, "restored_from"); from != m.WorkspaceID {
		t.Errorf("restored_from = %q", from)
	}
	// The original is untouched.
	if n := count(t, restarted.Read(), `SELECT count(*) FROM schedules WHERE enabled = 1`); n != 1 {
		t.Errorf("source schedules changed")
	}
}

func TestBackupExcludesCredentialsAndRuntimeState(t *testing.T) {
	f := newFixture(t)
	dir, m := f.backup(t)
	var seen []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		seen = append(seen, filepath.ToSlash(rel))
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte(secretMarker)) {
			t.Errorf("%s carries runtime secret", rel)
		}
		return nil
	})
	for _, rel := range seen {
		if strings.Contains(rel, ".applysmith") || strings.Contains(rel, "Cookies") || strings.HasSuffix(rel, ".log") {
			t.Errorf("runtime state exported: %s", rel)
		}
	}
	want := map[string]bool{"profile.yml": false, "cv.md": false, "knowledge/notes.md": false, "reports/001-example-corp.md": false, "resumes/ada.pdf": false}
	for _, e := range m.Files {
		if _, ok := want[e.Path]; ok {
			want[e.Path] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Errorf("user file %s missing from backup", p)
		}
	}
	if !strings.Contains(m.Excluded, "set up again") {
		t.Errorf("manifest does not state that credentials need separate setup")
	}
}

func TestPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	f := newFixture(t)
	dir, _ := f.backup(t)
	to := restoreTo(t, f, dir)
	for _, root := range []string{dir, to} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, _ := d.Info()
			want := fs.FileMode(0o600)
			if d.IsDir() {
				want = 0o700
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode %v, want %v", p, info.Mode().Perm(), want)
			}
			return nil
		})
	}
}

func TestCorruptBackupIsRefused(t *testing.T) {
	for _, target := range []string{"files/profile.yml", "applysmith.db"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			dir, _ := f.backup(t)
			p := filepath.Join(dir, filepath.FromSlash(target))
			b, _ := os.ReadFile(p)
			b[len(b)/2] ^= 0xff
			if err := os.WriteFile(p, b, 0o600); err != nil {
				t.Fatal(err)
			}
			to := filepath.Join(t.TempDir(), "restored")
			err := RestoreBackup(f.ctx, dir, to, RestoreOptions{})
			if !errors.Is(err, ErrInvalidBackup) {
				t.Fatalf("err = %v", err)
			}
			assertNoWorkspace(t, to)
		})
	}
}

// A database that matches its manifest hash but is internally damaged must
// still fail the integrity gate.
func TestDamagedDatabaseWithValidHashIsRefused(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.backup(t)
	db, err := sql.Open("sqlite", fileURL(filepath.Join(dir, "applysmith.db"), nil))
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite a row to dangle, with foreign keys off, then re-seal the hash.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tracker_events (entry_id, at, actor, kind) VALUES (77, 'x', 'user', 'created')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	resealManifest(t, dir, func(m *Manifest) {})
	if _, err := VerifyBackup(f.ctx, dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func resealManifest(t *testing.T, dir string, mutate func(*Manifest)) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if m.Database, err = hashFile(context.Background(), filepath.Join(dir, backupDBName), backupDBName); err != nil {
		t.Fatal(err)
	}
	mutate(&m)
	body, _ = json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, manifestName), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoWorkspace(t *testing.T, to string) {
	t.Helper()
	if _, err := os.Stat(to); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a restored workspace exists at %s (%v)", to, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(to))
	for _, e := range entries {
		t.Errorf("leftover staging entry %s", e.Name())
	}
}

func TestPathEscapingManifestIsRefused(t *testing.T) {
	for _, bad := range []string{"../escape.md", "/etc/passwd", "a/../../escape", ".applysmith/browser/Cookies", ".applysmith/applysmith.db", `a\..\b`, "C:/x"} {
		t.Run(bad, func(t *testing.T) {
			f := newFixture(t)
			dir, _ := f.backup(t)
			resealManifest(t, dir, func(m *Manifest) {
				m.Files = append(m.Files, FileEntry{Path: bad, SHA256: sha256Hex(nil), Size: 0})
			})
			to := filepath.Join(t.TempDir(), "restored")
			err := RestoreBackup(f.ctx, dir, to, RestoreOptions{})
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("err = %v, want ErrUnsafePath", err)
			}
			assertNoWorkspace(t, to)
		})
	}
}

func TestSymlinkAndUnlistedFilesInBackupAreRefused(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.backup(t)
	to := filepath.Join(t.TempDir(), "restored")

	if err := os.WriteFile(filepath.Join(dir, "files", "extra.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(f.ctx, dir, to, RestoreOptions{}); !errors.Is(err, ErrInvalidBackup) {
		t.Fatalf("unlisted file: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "files", "extra.md"))

	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.md")
		_ = os.WriteFile(outside, []byte("x"), 0o600)
		profile := filepath.Join(dir, "files", "profile.yml")
		_ = os.Remove(profile)
		if err := os.Symlink(outside, profile); err != nil {
			t.Fatal(err)
		}
		if err := RestoreBackup(f.ctx, dir, to, RestoreOptions{}); err == nil {
			t.Fatal("symlinked backup entry accepted")
		}
	}
	assertNoWorkspace(t, to)
}

func TestUnsupportedSchemaBackupIsRefused(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.backup(t)
	db, _ := sql.Open("sqlite", fileURL(filepath.Join(dir, "applysmith.db"), nil))
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (9999, 'future', 'abc', 'x')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	resealManifest(t, dir, func(*Manifest) {})
	to := filepath.Join(t.TempDir(), "restored")
	if err := RestoreBackup(f.ctx, dir, to, RestoreOptions{}); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v", err)
	}
	assertNoWorkspace(t, to)
}

func TestRestoreOnlyIntoNewEmptyWorkspace(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.backup(t)

	occupied := t.TempDir()
	_ = os.WriteFile(filepath.Join(occupied, "keep.md"), []byte("mine"), 0o600)
	if err := RestoreBackup(f.ctx, dir, occupied, RestoreOptions{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-empty target: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(occupied, "keep.md")); string(b) != "mine" {
		t.Error("existing workspace modified")
	}

	empty := t.TempDir()
	if err := RestoreBackup(f.ctx, dir, empty, RestoreOptions{}); err != nil {
		t.Fatalf("empty existing directory should work: %v", err)
	}
	if _, err := os.Stat(filepath.Join(empty, StateDir, DBFile)); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(empty, StateDir, DBFile+"-wal")); err == nil {
		t.Error("restored database left a WAL file")
	}
	if b, _ := os.ReadFile(filepath.Join(empty, "cv.md")); string(b) != "# Alex Example\n" {
		t.Error("user file not restored")
	}
}

func TestBackupOutputRules(t *testing.T) {
	f := newFixture(t)
	t.Run("inside the workspace", func(t *testing.T) {
		out := filepath.Join(f.root, "reports", "backup")
		if _, err := f.s.Backup(f.ctx, BackupOptions{Out: out, WorkspaceRoot: f.root}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("directory created inside the workspace")
		}
	})
	t.Run("existing directory", func(t *testing.T) {
		out := t.TempDir()
		if _, err := f.s.Backup(f.ctx, BackupOptions{Out: out, WorkspaceRoot: f.root}); !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("symlinked workspace file", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip()
		}
		link := filepath.Join(f.root, "knowledge", "link.md")
		if err := os.Symlink(filepath.Join(f.root, "cv.md"), link); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(link)
		out := filepath.Join(t.TempDir(), "bk")
		if _, err := f.s.Backup(f.ctx, BackupOptions{Out: out, WorkspaceRoot: f.root}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("partial backup left behind")
		}
	})
}

func TestConcurrentUserFileChangeFailsBackup(t *testing.T) {
	f := newFixture(t)
	for name, mutate := range map[string]func(){
		"rewritten": func() { _ = os.WriteFile(filepath.Join(f.root, "cv.md"), []byte("# Changed\n"), 0o600) },
		"added":     func() { _ = os.WriteFile(filepath.Join(f.root, "knowledge", "new.md"), []byte("n"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "bk")
			_, err := f.s.Backup(f.ctx, BackupOptions{Out: out, WorkspaceRoot: f.root, afterCopy: mutate})
			if !errors.Is(err, ErrConcurrentChange) {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Stat(out); err == nil {
				t.Error("inconsistent backup left behind")
			}
		})
	}
}

func TestBackupDrainsWriters(t *testing.T) {
	f := newFixture(t)
	held, release := make(chan struct{}), make(chan struct{})
	var writerDone atomic.Bool
	writer := make(chan error, 1)
	go func() {
		writer <- f.s.Write(f.ctx, func(tx *sql.Tx) error {
			close(held)
			<-release
			if _, err := tx.Exec(`INSERT INTO workspace_meta (key, value) VALUES ('late', '1')`); err != nil {
				return err
			}
			writerDone.Store(true)
			return nil
		})
	}()
	<-held

	out := filepath.Join(t.TempDir(), "bk")
	acquired := make(chan bool, 1)
	res := make(chan error, 1)
	go func() {
		_, err := f.s.Backup(f.ctx, BackupOptions{Out: out, WorkspaceRoot: f.root,
			afterAcquire: func() { acquired <- writerDone.Load() }})
		res <- err
	}()
	select {
	case <-acquired:
		t.Fatal("backup took the writer connection while a writer was active")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
	if !<-acquired {
		t.Fatal("backup proceeded before the writer finished")
	}
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	db, err := openImmutable(filepath.Join(out, backupDBName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := count(t, db, `SELECT count(*) FROM workspace_meta WHERE key = 'late'`); n != 1 {
		t.Errorf("snapshot missed the drained writer's commit")
	}
}

// addFile puts a file into the backup and re-seals the manifest, as a
// hash-consistent crafted backup would.
func addFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, backupFilesDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	resealManifest(t, dir, func(m *Manifest) {
		m.Files = append(m.Files, FileEntry{Path: rel, SHA256: sha256Hex([]byte(body)), Size: int64(len(body))})
	})
}

func TestManifestEntriesOutsideTheAllowlistAreRefused(t *testing.T) {
	for _, rel := range []string{"evil.sh", "bin/applysmith", "reports-extra/x.md", "Library/LaunchAgents/x.plist"} {
		t.Run(rel, func(t *testing.T) {
			f := newFixture(t)
			dir, _ := f.backup(t)
			addFile(t, dir, rel, "payload")
			if _, err := VerifyBackup(f.ctx, dir); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("verify: %v", err)
			}
			to := filepath.Join(t.TempDir(), "restored")
			if err := RestoreBackup(f.ctx, dir, to, RestoreOptions{}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("restore: %v", err)
			}
			assertNoWorkspace(t, to)
		})
	}
	f := newFixture(t)
	dir, _ := f.backup(t)
	addFile(t, dir, "knowledge/new/deep.md", "ok")
	if _, err := VerifyBackup(f.ctx, dir); err != nil {
		t.Fatalf("allowlisted entry refused: %v", err)
	}
}

func TestManifestIdentityMustMatchDatabase(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"schema":    func(m *Manifest) { m.SchemaVersion = 0 },
		"workspace": func(m *Manifest) { m.WorkspaceID = "ws_other" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			dir, _ := f.backup(t)
			resealManifest(t, dir, mutate)
			if _, err := VerifyBackup(f.ctx, dir); !errors.Is(err, ErrInvalidBackup) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A demo workspace restores as a demo: the marker travels with the backup, so
// the restored copy still keeps the fictional persona on the machine. A real
// workspace's backup restores without one.
func TestBackupCarriesTheDemoMarkerIntoTheRestoredWorkspace(t *testing.T) {
	real := newFixture(t)
	realDir, realManifest := real.backup(t)
	if realManifest.Demo || workspace.Open(restoreTo(t, real, realDir)).IsDemo() {
		t.Fatal("a backup of a real workspace restored as a demo")
	}

	demo := newFixture(t)
	if err := workspace.Open(demo.root).MarkDemo(); err != nil {
		t.Fatal(err)
	}
	dir, m := demo.backup(t)
	if !m.Demo {
		t.Fatal("the manifest of a demo backup does not say so")
	}
	if _, err := VerifyBackup(demo.ctx, dir); err != nil {
		t.Fatal(err)
	}
	to := restoreTo(t, demo, dir)
	if !workspace.Open(to).IsDemo() {
		t.Fatal("the restored workspace lost the demo marker")
	}
	if !openRestored(t, to).Demo() {
		t.Fatal("the restored store does not see the marker")
	}
	if workspace.Open(demo.root).IsDemo() != true {
		t.Fatal("the original lost its marker")
	}
}
