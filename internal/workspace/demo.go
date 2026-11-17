package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DemoMarkerName is the file in the state directory that marks a demo
// workspace. It is part of the workspace's configuration but none of the
// edited files: no route, command or dashboard action writes or removes it,
// and this package offers only a way to create it.
const DemoMarkerName = "demo-workspace"

const demoMarkerText = "This workspace is the Applysmith demo. It holds the fictional example persona only: it never submits, opens an account, or sends mail or a chat message, and it reads mail only to type a site's emailed verification code when the persona's address is a plus alias of the owner's own connected Gmail account.\n"

// DemoMarked reports whether the state directory carries the demo marker.
// Anything present under the name counts, so a marker cannot be disarmed by
// rewriting its content.
func DemoMarked(stateDir string) bool {
	_, err := os.Lstat(filepath.Join(stateDir, DemoMarkerName))
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// IsDemo reports whether the workspace carries the demo marker.
func (w *Workspace) IsDemo() bool { return DemoMarked(w.StateDir()) }

// MarkDemo writes the demo marker. It never removes or replaces one, so it is
// safe to repeat.
func (w *Workspace) MarkDemo() error {
	if err := os.MkdirAll(w.StateDir(), 0o700); err != nil {
		return err
	}
	_, err := createExclusive(filepath.Join(w.StateDir(), DemoMarkerName), []byte(demoMarkerText))
	return err
}

// Holds reports whether the directory already holds a workspace: its state
// directory or any of the workspace files exists.
func (w *Workspace) Holds() bool {
	if _, err := os.Lstat(w.StateDir()); err == nil {
		return true
	}
	for _, f := range Files {
		if _, err := os.Lstat(w.Path(f)); err == nil {
			return true
		}
	}
	return false
}
