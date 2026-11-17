package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration struct {
	version  int
	name     string
	checksum string
	sql      string
}

type appliedMigration struct {
	version  int
	checksum string
}

// SchemaVersion is the newest schema this binary supports.
func SchemaVersion() int {
	migs, err := loadMigrations(nil)
	if err != nil || len(migs) == 0 {
		return 0
	}
	return migs[len(migs)-1].version
}

// loadMigrations reads NNNN_name.sql files; versions must be 1..n with no gap.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	dir := "."
	if fsys == nil {
		sub, err := fs.Sub(embeddedMigrations, "migrations")
		if err != nil {
			return nil, err
		}
		fsys = sub
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var migs []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, name, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil || v < 1 {
			return nil, fmt.Errorf("store: migration file %q must be named NNNN_name.sql", e.Name())
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		migs = append(migs, migration{version: v, name: name, checksum: hex.EncodeToString(sum[:]), sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i, m := range migs {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration versions must be contiguous from 1, found %d at position %d", m.version, i+1)
		}
	}
	return migs, nil
}

// readApplied lists applied migrations; a database without the bookkeeping
// table has none.
func readApplied(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]appliedMigration, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, typed(err)
	}
	defer rows.Close()
	var out []appliedMigration
	for rows.Next() {
		var a appliedMigration
		if err := rows.Scan(&a.version, &a.checksum); err != nil {
			return nil, typed(err)
		}
		out = append(out, a)
	}
	return out, typed(rows.Err())
}

// checkApplied refuses a schema newer than the binary and any applied
// migration whose file changed.
func checkApplied(applied []appliedMigration, migs []migration) error {
	for _, a := range applied {
		if a.version > len(migs) {
			return fmt.Errorf("%w: database is at schema %d, this binary supports %d; run a newer applysmith or restore a backup made by this version", ErrSchemaTooNew, a.version, len(migs))
		}
	}
	for _, a := range applied {
		if migs[a.version-1].checksum != a.checksum {
			return fmt.Errorf("%w: migration %04d_%s differs from the one applied", ErrChecksumMismatch, a.version, migs[a.version-1].name)
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context, migs []migration, applied []appliedMigration, backupDir string) error {
	if _, err := s.rw.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT`); err != nil {
		return typed(err)
	}
	current := len(applied)
	if current >= len(migs) {
		return nil
	}
	// A nonempty database gets a validated snapshot before it changes shape.
	if current > 0 {
		if backupDir == "" {
			backupDir = filepath.Join(filepath.Dir(s.path), "backups")
		}
		if err := s.preUpgradeBackup(ctx, backupDir, current, migs); err != nil {
			return fmt.Errorf("store: pre-upgrade backup: %w", err)
		}
	}
	for _, m := range migs[current:] {
		err := s.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
				m.version, m.name, m.checksum, s.stamp())
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %04d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

func (s *Store) preUpgradeBackup(ctx context.Context, dir string, version int, migs []migration) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrIO, err)
	}
	dst := filepath.Join(dir, fmt.Sprintf("pre-upgrade-v%d-%s.db", version, s.now().UTC().Format("20060102T150405.000000000Z")))
	conn, err := s.rw.Conn(ctx)
	if err != nil {
		return typed(err)
	}
	defer conn.Close()
	if err := vacuumInto(ctx, conn, dst); err != nil {
		return err
	}
	if err := verifyDatabaseFile(ctx, dst, migs); err != nil {
		_ = os.Remove(dst)
		return err
	}
	prunePreUpgrade(dir)
	return nil
}

// preUpgradeKept is how many pre-upgrade snapshots stay. Each is a whole copy
// of the database, and only the newest returns to the schema before the last
// upgrade.
const preUpgradeKept = 3

// prunePreUpgrade removes all but the newest preUpgradeKept snapshots in dir,
// ordered by the time that ends each name (the version would sort v10 before
// v9). Other files in dir are never touched.
func prunePreUpgrade(dir string) {
	snaps, _ := filepath.Glob(filepath.Join(dir, "pre-upgrade-v*.db"))
	stamp := func(p string) string { return p[strings.LastIndex(p, "-")+1:] }
	sort.Slice(snaps, func(i, j int) bool { return stamp(snaps[i]) < stamp(snaps[j]) })
	for len(snaps) > preUpgradeKept {
		_ = os.Remove(snaps[0])
		snaps = snaps[1:]
	}
}

// vacuumInto writes a consistent, WAL-inclusive snapshot of the database the
// connection is attached to. VACUUM INTO reads inside one transaction, so it
// never copies a live WAL file piecemeal.
func vacuumInto(ctx context.Context, conn *sql.Conn, dst string) error {
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		_ = os.Remove(dst)
		return typed(err)
	}
	return typed(os.Chmod(dst, 0o600))
}

// checkIntegrity reports corruption or dangling foreign keys.
func checkIntegrity(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return typed(err)
	}
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			return typed(err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	rows.Close()
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrCorrupt, strings.Join(problems, "; "))
	}
	fk, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return typed(err)
	}
	defer fk.Close()
	if fk.Next() {
		var table, parent sql.NullString
		var rowid sql.NullInt64
		var idx int
		_ = fk.Scan(&table, &rowid, &parent, &idx)
		return fmt.Errorf("%w: foreign key violation in %s (row %d) referencing %s", ErrCorrupt, table.String, rowid.Int64, parent.String)
	}
	return nil
}

// verifyDatabaseFile opens a database file without touching it (immutable,
// read-only) and checks integrity and the migration record.
func verifyDatabaseFile(ctx context.Context, path string, migs []migration) error {
	db, err := openImmutable(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := checkIntegrity(ctx, db); err != nil {
		return err
	}
	applied, err := readApplied(ctx, db)
	if err != nil {
		return err
	}
	return checkApplied(applied, migs)
}

func openImmutable(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIO, err)
	}
	q := map[string][]string{"mode": {"ro"}, "immutable": {"1"}}
	db, err := sql.Open("sqlite", fileURL(path, q))
	if err != nil {
		return nil, typed(err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// newImages keeps, per migration set, the file the migrations leave a new
// database, from the second new database this process creates on. Migrating a
// new database costs about 200 ms, nearly all of it SQLite reading the whole
// schema again after each ALTER TABLE, while writing a copy of the image costs
// about a millisecond. The server and the command line create at most one new
// database per process, so they keep nothing; the agent's improvement probes
// create one per replayed turn, and a test binary one per test.
var newImages = struct {
	sync.Mutex
	migrated map[string]int // new databases the migrations made, per set
	image    map[string][]byte
}{migrated: map[string]int{}, image: map[string][]byte{}}

// migrationSet names a list of migrations by their checksums.
func migrationSet(migs []migration) string {
	var b strings.Builder
	for _, m := range migs {
		b.WriteString(m.checksum)
	}
	return b.String()
}

// absent reports a database path with no file, and no WAL or shared-memory
// file beside it that SQLite would read into a new one.
func absent(path string) bool {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// seedNew writes the kept image of the migration set at path, which must be
// absent, and reports whether it did. Opening the copy finds every migration
// applied.
func seedNew(path, set string) bool {
	newImages.Lock()
	img := newImages.image[set]
	newImages.Unlock()
	if img == nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	_, err = f.Write(img)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return false
	}
	return true
}

// migratedNew counts a new database the migrations just made and keeps the
// image of the second one. It runs before the database holds anything of its
// own, such as the workspace id. Without an image the next new database
// migrates, so a failure here changes nothing but the time.
func (s *Store) migratedNew(ctx context.Context, set string) {
	newImages.Lock()
	n := newImages.migrated[set]
	newImages.migrated[set] = n + 1
	kept := newImages.image[set] != nil
	newImages.Unlock()
	if n == 0 || kept {
		return
	}
	dir, err := os.MkdirTemp("", "applysmith-store-image-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	conn, err := s.rw.Conn(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	dst := filepath.Join(dir, "image.db")
	if err := vacuumInto(ctx, conn, dst); err != nil {
		return
	}
	img, err := os.ReadFile(dst)
	if err != nil {
		return
	}
	newImages.Lock()
	newImages.image[set] = img
	newImages.Unlock()
}
