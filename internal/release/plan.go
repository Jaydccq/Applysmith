package release

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// maxAppBytes is the bound on the packaged app from the desktop-shell
// capability, taken as decimal megabytes so a bundle near the limit fails.
const maxAppBytes = 80_000_000

// DefaultConfig is the pipeline the repository's Makefile runs.
func DefaultConfig(repo string) Config {
	bun := bunPath()
	return Config{
		Repo:            repo,
		RequireLists:    true,
		RequireGitleaks: true,
		Verify:          []Cmd{{Argv: []string{"make", "verify"}}},
		Install:         []Cmd{{Dir: "web", Argv: []string{bun, "install", "--frozen-lockfile"}}},
		Guard:           guardExport,
		Build:           []Cmd{{Argv: []string{"make", "build"}}},
		Test: []Cmd{
			{Argv: []string{"go", "vet", "-tags", "dashboard", "./..."}},
			{Argv: []string{"go", "test", "-p", "2", "-parallel", "2", "-count=1", "-tags", "dashboard", "./..."}},
			{Dir: "web", Argv: []string{bun, "run", "typecheck"}},
			{Dir: "web", Argv: []string{bun, "run", "test"}},
			{Dir: "engine", Argv: []string{bun, "install", "--frozen-lockfile"}},
			{Dir: "engine", Argv: []string{bun, "run", "test"}},
			{Dir: "engine", Argv: []string{bun, "run", "typecheck"}},
			{Dir: "extension", Argv: []string{bun, "install", "--frozen-lockfile"}},
			{Dir: "extension", Argv: []string{bun, "run", "test"}},
			{Dir: "extension", Argv: []string{bun, "run", "typecheck"}},
		},
		Package: packageExport,
	}
}

func bunPath() string {
	if p, err := exec.LookPath("bun"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".bun", "bin", "bun")
}

// guardExport runs the export's own guard tests in directory mode and release
// mode. The export carries its copy of the guard, so an export that lost a
// guard file fails here too.
func guardExport(export string) error {
	return runCmd(export, []string{"APPLYSMITH_GUARD_RELEASE=1", "APPLYSMITH_GUARD_ROOT=" + export},
		[]string{"go", "test", "-count=1", "-run", "^TestRepository$", "./internal/guard/"})
}

// packageExport builds the ad-hoc signed application bundle and disk image from
// the export and measures the bundle. Quarantined first launch and a fresh
// macOS user are separate acceptance checks for this ad-hoc distribution.
func packageExport(export string) (PackageResult, error) {
	var res PackageResult
	if runtime.GOOS != "darwin" {
		return res, errors.New("packaging builds a macOS application and runs on macOS only")
	}
	if err := runCmd(filepath.Join(export, "app"), nil, []string{"go", "tool", "mygo", "build"}); err != nil {
		return res, err
	}
	bundles, err := filepath.Glob(filepath.Join(export, "app", "build", "*", "*.app"))
	if err != nil || len(bundles) != 1 {
		return res, fmt.Errorf("expected one application bundle under app/build, found %d", len(bundles))
	}
	images, err := filepath.Glob(filepath.Join(export, "app", "build", "*", "*.dmg"))
	if err != nil || len(images) != 1 {
		return res, fmt.Errorf("expected one disk image under app/build, found %d", len(images))
	}
	size, err := dirBytes(bundles[0])
	if err != nil {
		return res, err
	}
	res.Path = strings.TrimPrefix(bundles[0], export+string(filepath.Separator))
	res.Bytes = size
	res.Pending = []string{"Quarantined first launch through Open Anyway and fresh macOS-user acceptance of this exact ad-hoc signed artifact: task 7.4"}
	return res, checkAppBound(size)
}

func checkAppBound(size int64) error {
	if size > maxAppBytes {
		return fmt.Errorf("packaged app is %s, above the %s bound", humanBytes(size), humanBytes(maxAppBytes))
	}
	return nil
}

func dirBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			total += fi.Size()
		}
		return nil
	})
	return total, err
}
