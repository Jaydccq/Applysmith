package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// LockFileName is the server lock inside the workspace state directory.
const LockFileName = "server.lock"

// LockInfo is the document the lock holder writes into the lock file so that
// other entry points find the running server.
type LockInfo struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	PathKey   string `json:"workspaceId"`
	BuildID   string `json:"buildId"`
	StartedAt string `json:"startedAt"`
	Origin    string `json:"origin"`
	// LaunchCode is the current one-time code that opens the dashboard. The
	// state directory is private to the user, which is what protects it.
	LaunchCode string `json:"launchCode,omitempty"`
}

// Lock is the exclusive operating-system lock on a workspace. The lock dies
// with its process, so a crashed server never blocks the next one.
type Lock struct {
	f    *os.File
	size int // length of the document on disk
}

// LockPath is the lock file of a workspace state directory.
func LockPath(stateDir string) string { return filepath.Join(stateDir, LockFileName) }

// AcquireLock takes the exclusive lock, creating the state directory when it
// is missing. When another process holds it the error is a StartupError that
// names the holder's origin if the holder is a server.
func AcquireLock(stateDir string) (*Lock, error) {
	l, err := tryLock(stateDir)
	if err != nil {
		return nil, err
	}
	if err := l.f.Truncate(0); err != nil {
		_ = l.Release()
		return nil, err
	}
	return l, nil
}

// LockHeld reports whether another process holds the lock right now. It only
// looks: a missing state directory or lock file means nobody holds the lock,
// and nothing is created or written.
func LockHeld(stateDir string) (bool, error) {
	f, err := os.Open(LockPath(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	held, err := lockFile(f, true)
	if err != nil || held {
		return held, err
	}
	return false, unlockFile(f)
}

// A probe or a process that is about to take the lock holds it for an instant;
// a few short retries tell that apart from a real holder.
const (
	lockAttempts  = 5
	lockRetryWait = 25 * time.Millisecond
)

func tryLock(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(LockPath(stateDir), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		held, err := lockFile(f, false)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if !held {
			return &Lock{f: f}, nil
		}
		if attempt == lockAttempts {
			_ = f.Close()
			return nil, heldError(stateDir)
		}
		time.Sleep(lockRetryWait)
	}
}

// heldError describes the holder. The holder writes its document just after
// binding, so a short wait avoids reporting an unknown holder for a server
// that is still starting. The document alone is not believed: a previous
// server leaves its document behind, so the server it names must answer for
// the same workspace.
func heldError(stateDir string) error {
	for range 20 {
		if info, err := ReadLockInfo(stateDir); err == nil && answersAs(info) {
			return &StartupError{Code: CodeServerAlreadyRunning,
				Message: fmt.Sprintf("a server for this workspace is already running at %s (pid %d)", info.Origin, info.PID)}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &StartupError{Code: CodeWorkspaceLocked,
		Message: "another Applysmith process holds this workspace (a backup or a server that is still starting)"}
}

// answersAs reports whether the server named by a lock document is alive and
// reports the workspace key the document claims.
func answersAs(info LockInfo) bool {
	addr, ok := loopbackAddr(info.Origin)
	if !ok {
		return false
	}
	h, ok := ProbeServer(addr)
	return ok && h.PathKey == info.PathKey
}

// ReadLockInfo returns the document of the current or last lock holder. It
// does not say whether that holder is alive; see LockHeld.
func ReadLockInfo(stateDir string) (LockInfo, error) {
	var info LockInfo
	b, err := os.ReadFile(LockPath(stateDir))
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return LockInfo{}, err
	}
	if info.Origin == "" || info.PID == 0 {
		return LockInfo{}, errors.New("lock file holds no server document")
	}
	return info, nil
}

// WriteInfo publishes the server document into the lock file.
func (l *Lock) WriteInfo(info LockInfo) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	// A refresh overwrites the document in place and pads a shorter one with
	// spaces, which JSON ignores, so a reader never sees a torn or empty file.
	if pad := l.size - len(b) - 1; pad > 0 {
		b = append(b, bytes.Repeat([]byte{' '}, pad)...)
	}
	b = append(b, '\n')
	if _, err := l.f.WriteAt(b, 0); err != nil {
		return err
	}
	l.size = len(b)
	return l.f.Sync()
}

// Held reports whether this process still holds the lock on the file at the
// lock path: the open file must be the file the path names, so a lock file that
// was removed or replaced under a running server reads as lost.
func (l *Lock) Held() bool {
	if l == nil || l.f == nil {
		return false
	}
	open, err := l.f.Stat()
	if err != nil {
		return false
	}
	named, err := os.Stat(l.f.Name())
	return err == nil && os.SameFile(open, named)
}

// Release drops the lock. The file stays so that a later reader can tell a
// stale lock from none.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	return errors.Join(err, l.f.Close())
}
