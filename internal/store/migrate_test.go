package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func coreSQL(t *testing.T) []byte {
	t.Helper()
	b, err := embeddedMigrations.ReadFile("migrations/0001_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// v1 is the schema of the previous release: the core migration alone.
func v1(t *testing.T) fs.FS {
	return fstest.MapFS{"0001_core.sql": {Data: coreSQL(t)}}
}

func twoVersions(t *testing.T, second string) fs.FS {
	return fstest.MapFS{
		"0001_core.sql":  {Data: coreSQL(t)},
		"0002_extra.sql": {Data: []byte(second)},
	}
}

func TestPreviousVersionUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), StateDir, DBFile)
	backups := filepath.Join(t.TempDir(), "bk")

	// Previous release: embedded schema only.
	old, err := Open(ctx, path, Options{Migrations: v1(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateRun(ctx, RunRequest{Principal: "cli", Operation: "op", Key: "k", Kind: "scan", Input: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	next := twoVersions(t, `CREATE TABLE extra (id INTEGER PRIMARY KEY) STRICT;`)
	s, err := Open(ctx, path, Options{Migrations: next, BackupDir: backups})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.ro.QueryRow(`SELECT count(*) FROM extra`).Scan(&n); err != nil {
		t.Fatalf("new table missing: %v", err)
	}
	if err := s.ro.QueryRow(`SELECT count(*) FROM runs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("existing data lost: n=%d err=%v", n, err)
	}
	snaps, _ := filepath.Glob(filepath.Join(backups, "pre-upgrade-v1-*.db"))
	if len(snaps) != 1 {
		t.Fatalf("pre-upgrade snapshot count = %d", len(snaps))
	}
	if err := verifyDatabaseFile(ctx, snaps[0], mustLoad(t, v1(t))); err != nil {
		t.Errorf("snapshot does not verify: %v", err)
	}
	if fi, _ := os.Stat(snaps[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %v", fi.Mode().Perm())
	}
}

func TestUpgradeKeepsTheNewestPreUpgradeSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), DBFile)
	backups := t.TempDir()
	old, err := Open(ctx, path, Options{Migrations: v1(t)})
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	// Earlier upgrades left these; v10 is newer than v9 though it sorts first.
	earlier := []string{"pre-upgrade-v8-20200101T000000.000000000Z.db", "pre-upgrade-v10-20200301T000000.000000000Z.db",
		"pre-upgrade-v9-20200201T000000.000000000Z.db", "profile.yml.1577836800"}
	for _, name := range earlier {
		if err := os.WriteFile(filepath.Join(backups, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(ctx, path, Options{Migrations: twoVersions(t, `CREATE TABLE extra (id INTEGER PRIMARY KEY) STRICT;`), BackupDir: backups})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	left, _ := os.ReadDir(backups)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	fresh, _ := filepath.Glob(filepath.Join(backups, "pre-upgrade-v1-*.db"))
	want := []string{filepath.Base(strings.Join(fresh, "")), "pre-upgrade-v10-20200301T000000.000000000Z.db", "pre-upgrade-v9-20200201T000000.000000000Z.db", "profile.yml.1577836800"}
	if len(fresh) != 1 || strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("left = %v, want %v", names, want)
	}
}

func mustLoad(t *testing.T, fsys fs.FS) []migration {
	t.Helper()
	m, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEmptyDatabaseTakesNoPreUpgradeBackup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), StateDir)
	s, err := Open(context.Background(), filepath.Join(dir, DBFile), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup dir created for an empty database: %v", err)
	}
}

func TestNewerSchemaIsRefusedBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), DBFile)
	newer, err := Open(ctx, path, Options{Migrations: twoVersions(t, `CREATE TABLE extra (id INTEGER PRIMARY KEY) STRICT;`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newer.CreateRun(ctx, RunRequest{Principal: "cli", Operation: "op", Key: "k", Kind: "scan", Input: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := newer.Close(); err != nil {
		t.Fatal(err)
	}

	before := fileState(t, path)
	_, err = Open(ctx, path, Options{Migrations: v1(t)}) // the older binary
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
	if !strings.Contains(err.Error(), "newer applysmith") {
		t.Errorf("no recovery instruction: %v", err)
	}
	if after := fileState(t, path); after != before {
		t.Errorf("database file changed: %s -> %s", before, after)
	}
}

func fileState(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(b)
}

func TestChangedMigrationChecksumIsRefused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), DBFile)
	s, err := Open(ctx, path, Options{Migrations: v1(t)})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	tampered := fstest.MapFS{"0001_core.sql": {Data: append(coreSQL(t), []byte("\n-- edited after release\n")...)}}
	if _, err := Open(ctx, path, Options{Migrations: tampered}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
}

func TestFailedMigrationLeavesPriorSchemaUsable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), DBFile)
	s, err := Open(ctx, path, Options{Migrations: v1(t)})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// The first statement succeeds, the second fails: nothing may remain.
	bad := twoVersions(t, `CREATE TABLE half (id INTEGER PRIMARY KEY); INSERT INTO missing_table VALUES (1);`)
	backups := t.TempDir()
	if _, err := Open(ctx, path, Options{Migrations: bad, BackupDir: backups}); err == nil {
		t.Fatal("broken migration applied")
	}
	snaps, _ := filepath.Glob(filepath.Join(backups, "pre-upgrade-v1-*.db"))
	if len(snaps) != 1 {
		t.Fatalf("pre-upgrade snapshots = %d", len(snaps))
	}
	if err := verifyDatabaseFile(ctx, snaps[0], mustLoad(t, v1(t))); err != nil {
		t.Fatalf("pre-upgrade snapshot fails its integrity check: %v", err)
	}
	again, err := Open(ctx, path, Options{Migrations: v1(t)})
	if err != nil {
		t.Fatalf("prior schema unusable: %v", err)
	}
	defer again.Close()
	var n int
	if err := again.ro.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations = %d, %v", n, err)
	}
	if err := again.ro.QueryRow(`SELECT count(*) FROM half`).Scan(&n); err == nil {
		t.Fatal("partial migration leaked a table")
	}
}

func TestTransactionRollback(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	sentinel := errors.New("stop")
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(insertEntry); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, s.ro, `SELECT count(*) FROM tracker_entries`); n != 0 {
		t.Fatal("rolled-back row is visible")
	}
}

func TestMigrationFilesMustBeContiguous(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{"0002_x.sql": {Data: []byte("SELECT 1;")}})
	if err == nil {
		t.Fatal("gap accepted")
	}
}

func TestIOFailuresAreTyped(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// A directory where the database file should be: SQLITE_CANTOPEN.
	if _, err := Open(ctx, dir, Options{}); !errors.Is(err, ErrIO) {
		t.Fatalf("directory as database: %v", err)
	}
	// A regular file where the state directory should be.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, filepath.Join(blocker, DBFile), Options{}); !errors.Is(err, ErrIO) {
		t.Fatalf("file as state dir: %v", err)
	}
	// A file that is not a database.
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not sqlite ", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, junk, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("junk file: %v", err)
	}
}

func TestLeadEvaluationCategoryUpgradePreservesEvidenceWithoutGuessing(t *testing.T) {
	ctx := context.Background()
	entries, err := embeddedMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	previous := fstest.MapFS{}
	for _, entry := range entries {
		if entry.Name() >= "0044_" {
			continue
		}
		body, err := embeddedMigrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: body}
	}
	path := filepath.Join(t.TempDir(), DBFile)
	old, err := Open(ctx, path, Options{Migrations: previous})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO leads (source,dedup_key,category,evidence_url,evidence_text,description,created_at,updated_at) VALUES ('signals','owned:review','eval-failed','https://example.com/jobs/uncertain','Owned evidence','Responsibilities and requirements: original text is ambiguous','2026-10-16T00:00:00Z','2026-10-16T00:00:00Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	backups := t.TempDir()
	upgraded, err := Open(ctx, path, Options{BackupDir: backups})
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var original, evidence string
	if err := upgraded.Read().QueryRowContext(ctx, `SELECT evaluation_category,evidence_text FROM leads WHERE dedup_key='owned:review'`).Scan(&original, &evidence); err != nil || original != "" || evidence != "Owned evidence" {
		t.Fatalf("original=%q evidence=%q err=%v", original, evidence, err)
	}
	snaps, err := filepath.Glob(filepath.Join(backups, "pre-upgrade-v43-*.db"))
	if err != nil || len(snaps) != 1 {
		t.Fatalf("backups=%v err=%v", snaps, err)
	}
	if err := verifyDatabaseFile(ctx, snaps[0], mustLoad(t, previous)); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE leads SET evaluation_category='invented'`)
		return err
	}); err == nil {
		t.Fatal("unknown evaluation category was accepted")
	}
}

// A new database from the third of a migration set on is a copy of the image
// the second one left, and is indistinguishable from a migrated one: every
// migration applied, every table, its own workspace id, a sound file.
func TestNewDatabaseStartsFromTheImageOfAnEarlierOne(t *testing.T) {
	ctx := context.Background()
	// A set of this run's own, so other tests and earlier runs (-count) have
	// made no database of it.
	fsys := twoVersions(t, `CREATE TABLE image_probe (id INTEGER PRIMARY KEY) STRICT; -- `+NewID("run"))
	migs, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	set := migrationSet(migs)
	open := func(id string) *Store {
		t.Helper()
		s, err := Open(ctx, filepath.Join(t.TempDir(), StateDir, DBFile), Options{Migrations: fsys, WorkspaceID: id})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	kept := func() bool {
		newImages.Lock()
		defer newImages.Unlock()
		return newImages.image[set] != nil
	}
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333", "44444444444444444444444444444444"}
	open(ids[0])
	if kept() {
		t.Fatal("the first new database of a process keeps an image")
	}
	open(ids[1])
	if !kept() {
		t.Fatal("the second new database keeps no image")
	}
	for _, id := range ids[2:] {
		s := open(id)
		var n int
		if err := s.ro.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("applied = %d, %v; want 2", n, err)
		}
		if err := s.ro.QueryRow(`SELECT count(*) FROM image_probe`).Scan(&n); err != nil {
			t.Fatalf("the copy lacks the last migration's table: %v", err)
		}
		if got, err := metaValue(ctx, s.ro, "workspace_id"); err != nil || got != id {
			t.Fatalf("workspace id = %q, %v; want %q (the image holds none)", got, err, id)
		}
		if err := checkIntegrity(ctx, s.ro); err != nil {
			t.Fatal(err)
		}
	}
	newImages.Lock()
	migrated := newImages.migrated[set]
	newImages.Unlock()
	if migrated != 2 {
		t.Fatalf("the migrations made %d new databases; the copies should have made the later ones", migrated)
	}
}

// A path with anything SQLite would read into a new database is not new.
func TestAbsentNeedsNoDatabaseWALOrSharedMemoryFile(t *testing.T) {
	for _, leftover := range []string{"", DBFile, DBFile + "-wal", DBFile + "-shm"} {
		dir := t.TempDir()
		if leftover != "" {
			if err := os.WriteFile(filepath.Join(dir, leftover), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got, want := absent(filepath.Join(dir, DBFile)), leftover == ""; got != want {
			t.Errorf("absent with %q present = %v, want %v", leftover, got, want)
		}
	}
}

// upgradeFrom opens a database at the migrations numbered before first, runs
// seed there, and opens it again at the whole set.
func upgradeFrom(t *testing.T, first string, seed ...string) *Store {
	t.Helper()
	ctx := context.Background()
	entries, err := embeddedMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	previous := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() < first {
			body, _ := embeddedMigrations.ReadFile("migrations/" + e.Name())
			previous[e.Name()] = &fstest.MapFile{Data: body}
		}
	}
	path := filepath.Join(t.TempDir(), DBFile)
	old, err := Open(ctx, path, Options{Migrations: previous})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range seed {
		if err := exec(t, old, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, Options{BackupDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The dead tables go with their rows; the live ledger and runs are kept.
func TestDeadTablesDropOverRows(t *testing.T) {
	s := upgradeFrom(t, "0065_",
		`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES ('r1', 'apply', 'succeeded', 'x', 'x')`,
		`INSERT INTO submit_claims (identity_key, run_id, status, claimed_at, updated_at) VALUES ('greenhouse:example/1', 'r1', 'submitted', 'x', 'x')`,
		`INSERT INTO scan_runs (id, source, status, started_at, updated_at) VALUES ('s1', 'greenhouse', 'completed', 'x', 'x')`,
		`INSERT INTO scan_run_jobs (job_id, run_id) VALUES ('r1', 's1')`,
		`INSERT INTO submissions (identity_key, url, host, state, source, observed_at, created_at, updated_at) VALUES ('greenhouse:example/1', 'u', 'h', 'submitted', 'press', 'x', 'x', 'x')`)
	if n := count(t, s.Read(), `SELECT count(*) FROM sqlite_schema WHERE name IN ('submit_claims', 'scan_run_jobs')`); n != 0 {
		t.Errorf("%d dead tables left", n)
	}
	if n := count(t, s.Read(), `SELECT (SELECT count(*) FROM runs) + (SELECT count(*) FROM scan_runs) + (SELECT count(*) FROM submissions)`); n != 3 {
		t.Errorf("live rows = %d, want 3", n)
	}
}

// The dead columns go; every other column of their rows is kept.
func TestDeadColumnsDropOverRows(t *testing.T) {
	s := upgradeFrom(t, "0066_",
		`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES ('r1', 'evaluate', 'succeeded', 'x', 'x')`,
		`INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, inbox_released, outcome, created_at) VALUES ('r1', 'k1', 'auto', 'user', 1, 'reported', 'x')`,
		`INSERT INTO report_artifacts (kind, path, status, planned_hash, content_hash, created_at, updated_at) VALUES ('report', 'reports/1.md', 'ready', 'p', 'c', 'x', 'x')`,
		`INSERT INTO apply_runs (run_id, started_at, caller, posting_url, identity_key, ladder_json, budget_json) VALUES ('r1', 'x', 'cli', 'u', 'k1', '{"a":1}', '{"b":2}')`,
		`INSERT INTO gmail_runs (id, trigger_kind, status, mode, started_at, classifier_version, query_version, stages_json, counts_json) VALUES (1, 'cli', 'succeeded', 'bootstrap', 'x', 'v', 'v', '[]', '{}')`,
		`INSERT INTO gmail_evidence (message_id, thread_id, history_id, received_ms, from_header, to_header, cc_header, subject, snippet, labels, first_run_id, first_seen_at)
			VALUES ('m1', 't1', '', 1, 'f', 't', 'c', 's', 'sn', '[]', 1, 'x')`)
	got := ""
	if err := s.Read().QueryRow(`SELECT j.outcome || r.content_hash || a.identity_key || e.thread_id FROM evaluation_jobs j, report_artifacts r, apply_runs a, gmail_evidence e`).Scan(&got); err != nil || got != "reportedck1t1" {
		t.Errorf("kept columns = %q, %v", got, err)
	}
	if n := count(t, s.Read(), `SELECT count(*) FROM (SELECT name FROM pragma_table_info('apply_runs') UNION ALL SELECT name FROM pragma_table_info('evaluation_jobs')
		UNION ALL SELECT name FROM pragma_table_info('report_artifacts') UNION ALL SELECT name FROM pragma_table_info('gmail_evidence'))
		WHERE name IN ('ladder_json', 'budget_json', 'inbox_released', 'planned_hash', 'history_id')`); n != 0 {
		t.Errorf("dead columns left")
	}
}

// The rebuilt event table keeps every row, its id sequence and its append-only
// triggers, and refuses the kind nothing writes.
func TestTrackerEventRebuildKeepsRowsAndTriggers(t *testing.T) {
	s := upgradeFrom(t, "0067_", insertEntry,
		`INSERT INTO tracker_events (id, entry_id, at, actor, kind, new_status) VALUES (5, 1, 'x', 'user', 'created', 'evaluated'), (9, 1, 'y', 'user', 'note_added', NULL)`)
	if n := count(t, s.Read(), `SELECT count(*) FROM tracker_events WHERE (id = 5 AND kind = 'created') OR (id = 9 AND at = 'y')`); n != 2 {
		t.Fatalf("rows kept = %d", n)
	}
	for _, q := range []string{
		`UPDATE tracker_events SET actor = 'agent' WHERE id = 5`,
		`DELETE FROM tracker_events WHERE id = 5`,
		`INSERT INTO tracker_events (entry_id, at, actor, kind) VALUES (1, 'z', 'user', 'applied_by_changed')`,
	} {
		if err := exec(t, s, q); err == nil {
			t.Errorf("accepted: %s", q)
		}
	}
	if err := exec(t, s, `INSERT INTO tracker_events (entry_id, at, actor, kind) VALUES (1, 'z', 'user', 'archived')`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s.Read(), `SELECT max(id) FROM tracker_events`); n != 10 {
		t.Errorf("next event id = %d, want 10", n)
	}
}
