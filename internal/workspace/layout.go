package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Directories and files below the workspace root.
const (
	StateDirName = ".applysmith"
	idFileName   = "workspace-id"
	idBytes      = 16
)

var (
	// userDirs hold user-owned or application-written artefacts.
	userDirs = []string{"resumes", "knowledge", "reports", "documents"}
	// stateDirs are the state subdirectories init creates; browser/ and the
	// database are created by their owners.
	stateDirs = []string{"backups", "logs", "snapshots", "downloads"}
)

// Workspace is one resolved workspace directory.
type Workspace struct {
	Root string

	now func() time.Time

	beforeRename func() error // test hook: runs after the temp file is flushed
}

// Open wraps an already validated root (see Resolve).
func Open(root string) *Workspace {
	return &Workspace{Root: filepath.Clean(root), now: time.Now}
}

func (w *Workspace) Path(f File) string { return filepath.Join(w.Root, f.Name()) }
func (w *Workspace) StateDir() string   { return filepath.Join(w.Root, StateDirName) }
func (w *Workspace) BackupsDir() string { return filepath.Join(w.StateDir(), "backups") }

// PathKey is the identifier health reports as workspaceId: the first 12
// hexadecimal characters of the SHA-256 of the canonical workspace path.
func PathKey(root string) string {
	canon, err := filepath.EvalSymlinks(root)
	if err != nil {
		canon = filepath.Clean(root)
	}
	sum := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(sum[:])[:12]
}

// pathLocks is shared by every handle: serialisation must hold for two
// Open calls on the same directory, not just for one handle.
var pathLocks sync.Map // canonical absolute path -> *sync.Mutex

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

func (w *Workspace) lock(path string) func() {
	m, _ := pathLocks.LoadOrStore(canonicalPath(path), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ID returns the workspace's opaque identifier, creating it on first use. It
// lives in the state directory, so it follows the workspace rather than its
// path, and a restore into a new workspace gets a new one. Keychain account
// names embed it, so a file that exists but does not hold a valid identifier
// is an error: silently minting another would orphan every stored secret.
func (w *Workspace) ID() (string, error) {
	if err := os.MkdirAll(w.StateDir(), 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(w.StateDir(), idFileName)
	if id, err := readID(path); !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	err := createID(path, NewID(), w.beforeRename)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	// Another creator may have won the race; its value is the identifier.
	return readID(path)
}

// ExistingID returns the identifier when the workspace already has one and
// creates nothing, so a read-only caller can tell "not initialised" apart.
func (w *Workspace) ExistingID() (string, bool, error) {
	id, err := readID(filepath.Join(w.StateDir(), idFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	return id, err == nil, err
}

// NewID returns a fresh random workspace identifier.
func NewID() string {
	raw := make([]byte, idBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(raw)
}

// CreateID writes id as the identifier of the workspace whose state directory
// is stateDir. It never replaces an existing identifier.
func CreateID(stateDir, id string) error {
	if !validID(id) {
		return newError(CodeWorkspaceIDInvalid, "", "not a workspace identifier")
	}
	return createID(filepath.Join(stateDir, idFileName), id, nil)
}

func validID(id string) bool {
	if len(id) != 2*idBytes {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func readID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if !validID(id) {
		return "", newError(CodeWorkspaceIDInvalid, idFileName, "the file exists but does not hold a workspace identifier; restore it from a backup or remove it only if no secret was stored for this workspace")
	}
	return id, nil
}

// createID publishes the identifier atomically: the bytes are flushed in a
// temporary file that is hard-linked to the final name, so a reader sees the
// whole identifier or no file, and a second creator gets fs.ErrExist instead
// of replacing the first one's value.
func createID(path, id string, beforeLink func() error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+idFileName+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(id + "\n")
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && beforeLink != nil {
		err = beforeLink()
	}
	if err == nil {
		err = os.Link(tmp.Name(), path)
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

// Confine resolves a workspace-relative path supplied by a configuration file
// or an API request. It rejects absolute paths, parent traversal and symbolic
// links that lead outside the workspace.
func (w *Workspace) Confine(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) || filepath.VolumeName(rel) != "" {
		return "", newError(CodePathOutside, rel, "path must be relative to the workspace")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", newError(CodePathOutside, rel, "path escapes the workspace")
	}
	full := filepath.Join(w.Root, clean)
	root, err := filepath.EvalSymlinks(w.Root)
	if err != nil {
		return "", err
	}
	resolved, err := evalExisting(full)
	if err != nil {
		return "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", newError(CodePathOutside, rel, "path resolves outside the workspace")
	}
	return full, nil
}

// evalExisting resolves symbolic links in the longest existing prefix of path.
func evalExisting(path string) (string, error) {
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		tail = append(tail, filepath.Base(path))
		path = parent
	}
}
