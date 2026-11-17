// Package cliinstall puts the applysmith command on the user's PATH by linking
// ~/.local/bin/applysmith to the binary inside the installed app. The binary
// answers as the command line when it is started under that name. Nothing here
// edits a shell profile or touches a file that is not this link.
package cliinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// State is what ~/.local/bin/applysmith is.
type State string

const (
	// NotInstalled: nothing is at the link path.
	NotInstalled State = "not-installed"
	// Installed: the link points at this app's binary.
	Installed State = "installed"
	// Stale: the link points at an app binary elsewhere, for example the app
	// before it was moved.
	Stale State = "stale"
	// Foreign: something else is at the link path; it is never touched.
	Foreign State = "foreign"
)

var (
	// ErrUnavailable: the running binary is not inside an installed app, so
	// there is nothing stable to link to.
	ErrUnavailable = errors.New("the running program is not inside an installed app")
	// ErrForeign: the link path holds something that is not our link.
	ErrForeign = errors.New("a different file is already there")
)

const (
	// Name is the command and the link's file name.
	Name         = "applysmith"
	bundleMarker = ".app" + string(filepath.Separator) + "Contents" + string(filepath.Separator) + "MacOS" + string(filepath.Separator)
	shellWait    = 3 * time.Second
)

// Target is one installation: the home directory and the resolved path of the
// app binary the link should name.
type Target struct{ Home, Exe string }

// Dir is the folder that holds the link.
func (t Target) Dir() string { return filepath.Join(t.Home, ".local", "bin") }

// Link is the path of the link.
func (t Target) Link() string { return filepath.Join(t.Dir(), Name) }

// InBundle reports whether exe is the executable of an app bundle.
func InBundle(exe string) bool {
	return filepath.IsAbs(exe) && strings.Contains(exe, bundleMarker)
}

// Available reports whether the link can be made for this target.
func (t Target) Available() bool { return filepath.IsAbs(t.Home) && InBundle(t.Exe) }

// ours reports whether dest is where an app binary like ours lives.
func (t Target) ours(dest string) bool {
	return InBundle(dest) && filepath.Base(dest) == filepath.Base(t.Exe)
}

// Inspect says what is at the link path.
func (t Target) Inspect() (State, error) {
	info, err := os.Lstat(t.Link())
	if errors.Is(err, os.ErrNotExist) {
		return NotInstalled, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return Foreign, nil
	}
	dest, err := os.Readlink(t.Link())
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(t.Dir(), dest)
	}
	switch {
	case dest == t.Exe:
		return Installed, nil
	case t.ours(dest):
		return Stale, nil
	}
	return Foreign, nil
}

// Install makes the link, or points a stale one at this app, and reports
// whether anything changed. A file or link that is not ours is refused.
func (t Target) Install() (bool, error) {
	if !t.Available() {
		return false, ErrUnavailable
	}
	state, err := t.Inspect()
	if err != nil {
		return false, err
	}
	switch state {
	case Installed:
		return false, nil
	case Foreign:
		return false, fmt.Errorf("%s: %w", t.Link(), ErrForeign)
	}
	if err := os.MkdirAll(t.Dir(), 0o755); err != nil {
		return false, err
	}
	if state == NotInstalled {
		return true, os.Symlink(t.Exe, t.Link())
	}
	tmp := fmt.Sprintf("%s.new-%d", t.Link(), os.Getpid())
	if err := os.Symlink(t.Exe, tmp); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, t.Link()); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// Remove deletes our link and reports whether there was one. A file or link
// that is not ours is refused.
func (t Target) Remove() (bool, error) {
	state, err := t.Inspect()
	if err != nil {
		return false, err
	}
	switch state {
	case NotInstalled:
		return false, nil
	case Foreign:
		return false, fmt.Errorf("%s: %w", t.Link(), ErrForeign)
	}
	return true, os.Remove(t.Link())
}

// OnPath reports whether the folder of the link is on the user's PATH. A
// program started from the Finder has a short PATH of its own, so when the
// process environment does not name the folder, the user's login shell is
// asked; its answer is bounded and a shell that fails reads as "not on PATH".
func (t Target) OnPath(ctx context.Context, getenv func(string) string) bool {
	if listHas(getenv("PATH"), t.Dir()) {
		return true
	}
	return listHas(shellPath(ctx, getenv("SHELL")), t.Dir())
}

func listHas(list, dir string) bool {
	for _, p := range filepath.SplitList(list) {
		if p != "" && filepath.Clean(p) == dir {
			return true
		}
	}
	return false
}

const (
	pathStart = "__APPLYSMITH_PATH_START__"
	pathEnd   = "__APPLYSMITH_PATH_END__"
)

// shellPath runs an interactive login shell to print its PATH between markers,
// which keeps a profile's own output out of the answer.
func shellPath(ctx context.Context, shell string) string {
	if !filepath.IsAbs(shell) {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, shellWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-i", "-l", "-c", `printf '%s%s%s' `+pathStart+` "$PATH" `+pathEnd)
	cmd.WaitDelay = time.Second // a background child of the profile must not hold the pipe open
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	_, rest, ok := strings.Cut(string(out), pathStart)
	if !ok {
		return ""
	}
	list, _, ok := strings.Cut(rest, pathEnd)
	if !ok {
		return ""
	}
	return list
}

// ProfileHint returns the shell profile file and the one line that puts the
// link's folder on PATH. It is shown to the user and never written.
func ProfileHint(shell string) (file, line string) {
	switch filepath.Base(shell) {
	case "zsh":
		return "~/.zshrc", `export PATH="$HOME/.local/bin:$PATH"`
	case "bash":
		return "~/.bash_profile", `export PATH="$HOME/.local/bin:$PATH"`
	case "fish":
		return "~/.config/fish/config.fish", `fish_add_path $HOME/.local/bin`
	}
	return "~/.profile", `export PATH="$HOME/.local/bin:$PATH"`
}
