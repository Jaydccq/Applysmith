package toolchain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"applysmith/internal/documents/assets"
)

const setupTimeout = 5 * time.Minute

// External is a tool the user installs outside the app: how, and how much disk
// it takes. None of them is part of the app bundle.
type External struct {
	Name    string
	Install string
	Size    string
}

// Budget is what an explicit setup downloads and writes, shown before it runs.
type Budget struct {
	Downloads []string
	DiskBytes int64
	External  []External
}

// SetupBudget lists the downloads and disk use of Setup. The external tools
// are only described: Setup never installs a TeX distribution or Poppler.
func SetupBudget() Budget {
	return Budget{
		Downloads: []string{"pypdf " + lock.Pypdf.Version + " wheel (about 0.4 MB) from the Python package index, verified against a locked SHA-256"},
		DiskBytes: 30 << 20,
		// Sizes are the measured ones in docs/architecture/document-toolchain.md.
		External: []External{
			{Name: "TeX", Install: fixTeX, Size: "a full TeX Live is about 9.1 GiB, of which the templates read about 19.7 MiB; a minimal install was not measured"},
			{Name: "Poppler", Install: fixPoppler, Size: "about 81 MiB, and Homebrew may upgrade shared libraries too (about 206 MiB in the measured case)"},
			{Name: "Python 3.11 or newer", Install: "use the system Python, or install one (macOS: `brew install python`); setup builds its private venv on it", Size: "Homebrew python@3.14 is about 88 MiB when none is installed"},
		},
	}
}

// ErrNoPython is returned when no interpreter in the supported range exists.
var ErrNoPython = errors.New("toolchain: no Python 3.11 or newer was found")

// Setup prepares the private venv from the locked requirements. It does not
// touch any global Python installation and never installs TeX or Poppler.
func Setup(ctx context.Context, cfg Config) error {
	dirs := cfg.SearchDirs
	if len(dirs) == 0 {
		dirs = SearchDirs(os.Getenv)
	}
	root := cfg.Root
	for _, d := range []string{root.Path(), root.home(), root.texVar(), root.texConfig()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if venvHealthy(ctx, root, dirs) {
		return nil
	}
	py, err := findPython(ctx, root, dirs)
	if err != nil {
		return err
	}
	// The venv directory is ours; a broken one is replaced, nothing else is.
	if err := os.RemoveAll(root.venv()); err != nil {
		return err
	}
	env := append(root.minimalEnv(), "PIP_CONFIG_FILE="+os.DevNull, "PIP_DISABLE_PIP_VERSION_CHECK=1", "PIP_NO_INPUT=1")
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if _, err := run(ctx, command{Path: py, Env: env, Timeout: setupTimeout, Args: []string{"-I", "-m", "venv", root.venv()}}); err != nil {
		return fmt.Errorf("create the venv: %w", err)
	}
	reqs, err := assets.Read("requirements.txt")
	if err != nil {
		return err
	}
	file := filepath.Join(root.Path(), "requirements.txt")
	if err := os.WriteFile(file, reqs, 0o600); err != nil {
		return err
	}
	_, err = run(ctx, command{Path: root.venvPython(), Env: env, Timeout: setupTimeout,
		Args: []string{"-I", "-m", "pip", "install", "--require-hashes", "--only-binary=:all:", "--no-deps", "--no-cache-dir", "-r", file}})
	if err != nil {
		return fmt.Errorf("install the locked requirements: %w", err)
	}
	return nil
}

func venvHealthy(ctx context.Context, root Root, dirs []string) bool {
	p := prober{ctx: ctx, cfg: Config{Root: root}, dirs: dirs}
	p.venv()
	return len(p.comps) == 1 && p.comps[0].Status == StatusOK
}

// findPython returns the first interpreter that satisfies the lock.
func findPython(ctx context.Context, root Root, dirs []string) (string, error) {
	for _, name := range []string{"python3.14", "python3.13", "python3.12", "python3.11", "python3"} {
		path, ok := lookup(dirs, name)
		if !ok {
			continue
		}
		out, err := run(ctx, command{Path: path, Env: root.minimalEnv(), Timeout: HelperTimeout,
			Args: []string{"-I", "-c", "import sys; print('%d.%d.%d' % sys.version_info[:3])"}})
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(out.Stdout)); !versionLess(v, lock.Python.Min) {
			return path, nil
		}
	}
	return "", ErrNoPython
}
