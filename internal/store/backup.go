package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"applysmith/internal/workspace"
)

const (
	manifestName   = "manifest.json"
	backupDBName   = "applysmith.db"
	backupFilesDir = "files"
	manifestFormat = 1
)

// WorkspaceIncludes are the user configuration and artifacts a backup carries.
// It is an allowlist: runtime state (.applysmith: browser profile, cookies, logs,
// downloads) and keychain values are never reachable through it.
var WorkspaceIncludes = []string{
	"profile.yml", "answer-bank.yml", "portals.yml", "policy.yml",
	"cv.md", "narrative.md", "voice.md",
	"resumes", "transcript", "knowledge", "reports", "documents",
}

const excludedNotice = "Browser profiles, cookies, keychain secrets, logs and temporary downloads are not included; API keys and sign-in credentials must be set up again on the restored workspace."

// FileEntry names one backed-up file by its manifest-relative path.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest describes a backup directory.
type Manifest struct {
	Format         int    `json:"format"`
	ProductVersion string `json:"product_version"`
	SchemaVersion  int    `json:"schema_version"`
	WorkspaceID    string `json:"workspace_id"`
	// Demo says the backed-up workspace carried the demo marker, which the
	// restored workspace carries too.
	Demo      bool        `json:"demo,omitempty"`
	CreatedAt string      `json:"created_at"`
	Database  FileEntry   `json:"database"`
	Files     []FileEntry `json:"files"`
	Excluded  string      `json:"excluded"`
}

// BackupOptions parameterizes a backup.
type BackupOptions struct {
	// Out must not exist; it is created 0700 outside WorkspaceRoot.
	Out string
	// WorkspaceRoot is the tree holding user files; empty backs up the
	// database alone.
	WorkspaceRoot  string
	ProductVersion string
	Now            func() time.Time

	// afterAcquire runs once the writer connection is held (Store.Backup only).
	afterAcquire func()
	// afterVacuum runs once the database snapshot is written and the writer
	// has been released.
	afterVacuum func()
	// afterCopy runs between the user-file copy and the final revision check.
	afterCopy func()
}

// Backup snapshots a live store. It takes the only writer connection while the
// database snapshot is written, so active writers are drained and new ones
// queue (and fail with ErrBusy past the busy timeout) instead of racing it.
// The writer is released as soon as the snapshot exists; copying the user
// files then runs beside normal writes. Cancelling ctx stops the copy and
// removes the partial output.
func (s *Store) Backup(ctx context.Context, opts BackupOptions) (*Manifest, error) {
	conn, err := s.rw.Conn(ctx)
	if err != nil {
		return nil, typed(err)
	}
	defer conn.Close()
	if opts.afterAcquire != nil {
		opts.afterAcquire()
	}
	return backup(ctx, conn, conn.Close, opts)
}

// BackupOffline snapshots a database nobody else has open. The caller holds
// the workspace lock; the database is not migrated.
func BackupOffline(ctx context.Context, dbPath string, opts BackupOptions) (*Manifest, error) {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", defaultBusyTimeout.Milliseconds()))
	db, err := sql.Open("sqlite", fileURL(dbPath, q))
	if err != nil {
		return nil, typed(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, typed(err)
	}
	defer conn.Close()
	return backup(ctx, conn, conn.Close, opts)
}

// backup writes the snapshot through conn, calls release once the database
// snapshot is complete, and then copies the user files.
func backup(ctx context.Context, conn *sql.Conn, release func() error, opts BackupOptions) (m *Manifest, err error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	out, err := newOutputDir(opts.Out, opts.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(out) // created by newOutputDir moments ago
		}
	}()

	var before []sourceFile
	if opts.WorkspaceRoot != "" {
		if before, err = listSources(opts.WorkspaceRoot); err != nil {
			return nil, err
		}
	}

	dbOut := filepath.Join(out, backupDBName)
	err = vacuumInto(ctx, conn, dbOut)
	_ = release() // closing twice is harmless; the caller's defer covers early returns
	if err != nil {
		return nil, err
	}
	if opts.afterVacuum != nil {
		opts.afterVacuum()
	}
	dbEntry, err := hashFile(ctx, dbOut, backupDBName)
	if err != nil {
		return nil, err
	}
	migs, err := loadMigrations(nil)
	if err != nil {
		return nil, err
	}
	version, wsID, err := snapshotIdentity(ctx, dbOut, migs)
	if err != nil {
		return nil, err
	}

	entries, err := copySources(ctx, opts.WorkspaceRoot, filepath.Join(out, backupFilesDir), before)
	if err != nil {
		return nil, err
	}
	if opts.afterCopy != nil {
		opts.afterCopy()
	}
	if opts.WorkspaceRoot != "" {
		after, lerr := listSources(opts.WorkspaceRoot)
		if lerr != nil {
			return nil, lerr
		}
		if !sameSources(before, after) {
			return nil, ErrConcurrentChange
		}
		if err = recheckSources(ctx, opts.WorkspaceRoot, entries); err != nil {
			return nil, err
		}
	}

	m = &Manifest{
		Format: manifestFormat, ProductVersion: opts.ProductVersion, SchemaVersion: version, WorkspaceID: wsID,
		CreatedAt: opts.Now().UTC().Format(timeLayout), Database: dbEntry, Files: entries, Excluded: excludedNotice,
		Demo: opts.WorkspaceRoot != "" && workspace.Open(opts.WorkspaceRoot).IsDemo(),
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(out, manifestName), body, 0o600); err != nil {
		return nil, diskError(err)
	}
	return m, nil
}

// newOutputDir creates the new backup directory. It refuses an existing path
// and any location inside the workspace it copies from.
func newOutputDir(out, root string) (string, error) {
	if out == "" {
		return "", errors.New("store: backup output directory required")
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", fmt.Errorf("%w: output %s already exists", ErrConflict, abs)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrIO, err)
	}
	if root != "" {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrIO, err)
		}
		if inside(r, filepath.Join(parent, filepath.Base(abs))) {
			return "", fmt.Errorf("%w: backup output must be outside the workspace", ErrUnsafePath)
		}
	}
	if err := os.Mkdir(abs, 0o700); err != nil {
		return "", fmt.Errorf("%w: %w", ErrIO, err)
	}
	return abs, nil
}

func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func snapshotIdentity(ctx context.Context, dbPath string, migs []migration) (int, string, error) {
	db, err := openImmutable(dbPath)
	if err != nil {
		return 0, "", err
	}
	defer db.Close()
	applied, err := readApplied(ctx, db)
	if err != nil {
		return 0, "", err
	}
	if err := checkApplied(applied, migs); err != nil {
		return 0, "", err
	}
	id, err := metaValue(ctx, db, "workspace_id")
	if errors.Is(err, ErrNotFound) {
		return 0, "", ErrNoWorkspaceID
	}
	if err != nil {
		return 0, "", err
	}
	return len(applied), id, nil
}

type sourceFile struct {
	rel     string
	size    int64
	modTime int64
}

// listSources walks the allowlist. A symlink or special file in the tree
// fails the backup rather than being followed or silently skipped.
func listSources(root string) ([]sourceFile, error) {
	var out []sourceFile
	for _, inc := range WorkspaceIncludes {
		start := filepath.Join(root, filepath.FromSlash(inc))
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && p == start {
					return nil
				}
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				return fmt.Errorf("%w: symlink %s in workspace", ErrUnsafePath, rel)
			case d.IsDir():
				return nil
			case !d.Type().IsRegular():
				return fmt.Errorf("%w: %s is not a regular file", ErrUnsafePath, rel)
			case strings.HasSuffix(d.Name(), ".pending"):
				return nil // unpublished temp sibling
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out = append(out, sourceFile{rel: rel, size: info.Size(), modTime: info.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

func sameSources(a, b []sourceFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func copySources(ctx context.Context, root, dstDir string, files []sourceFile) ([]FileEntry, error) {
	var out []FileEntry
	for _, f := range files {
		dst := filepath.Join(dstDir, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, diskError(err)
		}
		sum, n, err := copyHashed(ctx, filepath.Join(root, filepath.FromSlash(f.rel)), dst)
		if err != nil {
			return nil, err
		}
		out = append(out, FileEntry{Path: f.rel, SHA256: sum, Size: n})
	}
	return out, nil
}

// ctxReader stops a long copy or hash when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// openRegular opens a regular file without following a link: the path is
// inspected first and the handle must be that very file, so a symlink swapped
// in between is refused.
func openRegular(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIO, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnsafePath, filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIO, err)
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s changed while it was opened", ErrUnsafePath, filepath.Base(path))
	}
	return f, nil
}

// copyHashed copies src to a new file dst and returns the SHA-256 and length
// of the bytes it copied.
func copyHashed(ctx context.Context, src, dst string) (string, int64, error) {
	in, err := openRegular(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrIO, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), ctxReader{ctx, in})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, copyError(err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// copyError keeps a cancelled context distinguishable from a disk failure.
func copyError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("store: backup interrupted: %w", err)
	}
	return diskError(err)
}

// recheckSources hashes the sources again: a file rewritten with the same
// size and timestamp still fails the backup.
func recheckSources(ctx context.Context, root string, copied []FileEntry) error {
	for _, e := range copied {
		sum, _, err := hashPath(ctx, filepath.Join(root, filepath.FromSlash(e.Path)))
		if err != nil {
			return err
		}
		if sum != e.SHA256 {
			return ErrConcurrentChange
		}
	}
	return nil
}

func hashPath(ctx context.Context, p string) (string, int64, error) {
	f, err := openRegular(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx, f})
	if err != nil {
		return "", 0, copyError(err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func hashFile(ctx context.Context, p, rel string) (FileEntry, error) {
	sum, n, err := hashPath(ctx, p)
	return FileEntry{Path: rel, SHA256: sum, Size: n}, err
}

// entryPath maps a manifest entry to its place inside a backup directory.
func entryPath(dir string, e FileEntry, isDB bool) string {
	if isDB {
		return filepath.Join(dir, backupDBName)
	}
	return filepath.Join(dir, backupFilesDir, filepath.FromSlash(e.Path))
}
