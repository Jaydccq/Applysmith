package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

const versionTimeout = 5 * time.Second

// ErrNotFound means no cloudflared executable was found.
var ErrNotFound = errors.New("cloudflared was not found; install it (for example `brew install cloudflared`, or see https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/) or set remote_access.tunnel.binary in policy.yml")

// Finder locates the executable and asks it for its version. The zero value
// searches the executable path and the standard package-manager locations.
type Finder struct {
	LookPath   func(string) (string, error)
	Candidates []string
	Version    func(ctx context.Context, path string) (string, error)
}

func (f Finder) lookPath() func(string) (string, error) {
	if f.LookPath != nil {
		return f.LookPath
	}
	return exec.LookPath
}

func (f Finder) candidates() []string {
	if f.Candidates != nil {
		return f.Candidates
	}
	return standardLocations()
}

func standardLocations() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		return []string{`C:\Program Files (x86)\cloudflared\cloudflared.exe`, `C:\Program Files\cloudflared\cloudflared.exe`,
			filepath.Join(home, "scoop", "shims", "cloudflared.exe")}
	}
	return []string{"/opt/homebrew/bin/cloudflared", "/usr/local/bin/cloudflared", "/usr/bin/cloudflared", "/snap/bin/cloudflared",
		filepath.Join(home, ".local", "bin", "cloudflared")}
}

// Locate resolves binary ("auto" or a path), verifies it by running its
// version command with a 5 second limit and returns the path and version.
func (f Finder) Locate(ctx context.Context, binary string) (path, version string, err error) {
	switch {
	case binary == "" || binary == "auto":
		path, err = f.search()
	default:
		if _, serr := os.Stat(binary); serr != nil {
			path, err = f.lookPath()(binary)
		} else {
			path = binary
		}
	}
	if err != nil || path == "" {
		return "", "", ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	probe := f.Version
	if probe == nil {
		probe = execVersion
	}
	version, err = probe(ctx, path)
	if err != nil {
		return "", "", fmt.Errorf("%s did not answer its version command: %w", filepath.Base(path), err)
	}
	return path, version, nil
}

func (f Finder) search() (string, error) {
	if p, err := f.lookPath()("cloudflared"); err == nil {
		return p, nil
	}
	for _, c := range f.candidates() {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", ErrNotFound
}

var versionRE = lazyregexp.New(`(?i)version\s+(\S+)`)

func execVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if m := versionRE.FindStringSubmatch(line); m != nil {
		return m[1], nil
	}
	if line == "" {
		return "", errors.New("empty answer")
	}
	return line[:min(len(line), 80)], nil
}
