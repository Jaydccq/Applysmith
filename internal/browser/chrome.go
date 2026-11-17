package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/lazyregexp"
)

// MinChromeMajor is the oldest Google Chrome major version the lanes accept.
// Documented in docs/architecture/browser.md.
const MinChromeMajor = 120

// Chrome is an accepted Google Chrome installation.
type Chrome struct {
	Path    string
	Version string // for example "155.0.8059.40"
	Major   int
}

// chromeBinEnv names the only candidate when set.
const chromeBinEnv = "APPLYSMITH_CHROME_BIN"

var versionRE = lazyregexp.New(`^Google Chrome (\d+)\.(\S*)`)

// chromeCandidates lists the standard install locations for the platform.
func chromeCandidates(goos, home string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		app := "Google Chrome.app/Contents/MacOS/Google Chrome"
		c := []string{filepath.Join("/Applications", app)}
		if home != "" {
			c = append(c, filepath.Join(home, "Applications", app))
		}
		return c
	case "windows":
		var c []string
		for _, v := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if base := getenv(v); base != "" {
				c = append(c, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
		return c
	default:
		return []string{
			"/opt/google/chrome/chrome",
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
		}
	}
}

// DiscoverChrome finds an acceptable Google Chrome. When APPLYSMITH_CHROME_BIN is
// set it is the only candidate; no other browser is ever substituted.
func DiscoverChrome(ctx context.Context, getenv func(string) string) (Chrome, error) {
	home, _ := os.UserHomeDir()
	var candidates []string
	if p := getenv(chromeBinEnv); p != "" {
		candidates = []string{p}
	} else {
		candidates = chromeCandidates(runtime.GOOS, home, getenv)
	}
	for _, p := range candidates {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		return checkChromeCached(ctx, p, st)
	}
	return Chrome{}, newError(CodeChromeMissing,
		"no Google Chrome executable found; searched %s; install Google Chrome or set %s to its executable",
		strings.Join(candidates, ", "), chromeBinEnv)
}

type versionCheck struct {
	chrome Chrome
	err    error
}

// checked holds the answer of `--version` per executable path, size and
// modification time: running Chrome costs about 40 ms and the setup state asks
// on every dashboard load. An update replaces the executable, so its key
// changes. Only a verdict on the version line is kept: a probe that could not
// run (a timeout, a failed exec) is made again.
var checked sync.Map

func checkChromeCached(ctx context.Context, path string, st os.FileInfo) (Chrome, error) {
	key := path + "\x00" + strconv.FormatInt(st.Size(), 10) + "\x00" + st.ModTime().UTC().Format(time.RFC3339Nano)
	if v, ok := checked.Load(key); ok {
		r := v.(versionCheck)
		return r.chrome, r.err
	}
	c, ran, err := checkChrome(ctx, path)
	if ran {
		checked.Store(key, versionCheck{c, err})
	}
	return c, err
}

// checkChrome reads the version without opening a window. ran is false when
// the executable gave no version line to judge.
func checkChrome(ctx context.Context, path string) (c Chrome, ran bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = browserEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return Chrome{}, false, wrapError(CodeChromeUnsupported, err, "%s did not report a version; correct %s or install Google Chrome", path, chromeBinEnv)
	}
	line := strings.TrimSpace(string(out))
	m := versionRE.FindStringSubmatch(line)
	if m == nil {
		return Chrome{}, true, newError(CodeChromeUnsupported,
			"%s reports %q, but only Google Chrome is supported; install Google Chrome or correct %s", path, line, chromeBinEnv)
	}
	major, _ := strconv.Atoi(m[1])
	if major < MinChromeMajor {
		return Chrome{}, true, newError(CodeChromeTooOld,
			"Google Chrome %d found at %s, version %d or newer is required; update Google Chrome", major, path, MinChromeMajor)
	}
	return Chrome{Path: path, Version: strings.TrimPrefix(line, "Google Chrome "), Major: major}, true, nil
}
