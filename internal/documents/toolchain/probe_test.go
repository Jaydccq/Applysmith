package toolchain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeTools is a machine with every component present, built from shell
// scripts that print the version lines the real tools print. Each field can be
// replaced to simulate an upgrade or a missing piece.
type fakeTools struct {
	t       *testing.T
	bin     string
	texTree string
	root    Root
}

func newFakeTools(t *testing.T) *fakeTools {
	t.Helper()
	dir := t.TempDir()
	f := &fakeTools{t: t, bin: filepath.Join(dir, "bin"), texTree: filepath.Join(dir, "tex"), root: Root(filepath.Join(dir, "root"))}
	for _, d := range []string{f.bin, f.texTree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.script(f.bin, "lualatex", `echo "This is LuaHBTeX, Version 1.21.0 (TeX Live 2025)"`)
	f.script(f.bin, "xelatex", `echo "XeTeX 3.141592653-2.6-0.999997 (TeX Live 2025)"`)
	f.script(f.bin, "pdftotext", `echo "pdftotext version 26.10.0" >&2`)
	f.script(f.bin, "pdftoppm", `echo "pdftoppm version 26.10.0" >&2`)
	f.script(f.bin, "kpsewhich", `for f in "$@"; do [ "$f" = "--" ] && continue; [ -f "`+f.texTree+`/$f" ] && echo "`+f.texTree+`/$f"; done; exit 0`)
	for _, p := range lock.TeX.Packages {
		f.write(f.texTree, p.Witness, "x")
	}
	f.setModerncv("2.4.1")
	f.venv("3.14.7", lock.Pypdf.Version)
	return f
}

func (f *fakeTools) script(dir, name, body string) {
	f.t.Helper()
	f.write(dir, name, "#!/bin/sh\n"+body+"\n")
	if err := os.Chmod(filepath.Join(dir, name), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeTools) write(dir, name, content string) {
	f.t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeTools) setModerncv(version string) {
	f.write(f.texTree, "moderncv.cls", `\ProvidesClass{moderncv}[2024-07-18 v`+version+` modern curriculum vitae]`+"\n")
}

func (f *fakeTools) venv(python, pypdf string) {
	f.script(filepath.Dir(f.root.venvPython()), "python", `printf '%s\n%s\n' `+python+` `+pypdf)
}

func (f *fakeTools) cfg() Config { return Config{Root: f.root, SearchDirs: []string{f.bin}} }

func (f *fakeTools) probe() Report {
	rep, _ := Probe(context.Background(), f.cfg())
	return rep
}

func TestProbe_AllMissing(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	rep, _ := Probe(context.Background(), Config{Root: Root(filepath.Join(empty, "root")), SearchDirs: []string{empty}})
	if rep.State != StateUnavailable || rep.Code != CodeUnavailable {
		t.Fatalf("state = %s code = %q", rep.State, rep.Code)
	}
	want := []string{"lualatex", "xelatex", "moderncv", "tex-packages", "python-venv", "pdftotext", "pdftoppm"}
	if !reflect.DeepEqual(rep.Missing, want) {
		t.Fatalf("missing = %v, want %v", rep.Missing, want)
	}
	for _, frag := range []string{"TeX", "poppler", "documents toolchain setup"} {
		if !strings.Contains(strings.ToLower(rep.Fix), strings.ToLower(frag)) {
			t.Errorf("fix %q does not mention %q", rep.Fix, frag)
		}
	}
}

func TestProbe_MissingTeXPackageIsNamed(t *testing.T) {
	t.Parallel()
	f := newFakeTools(t)
	if err := os.Remove(filepath.Join(f.texTree, lock.TeX.Packages[0].Witness)); err != nil {
		t.Fatal(err)
	}
	rep := f.probe()
	if !reflect.DeepEqual(rep.Missing, []string{"tex-packages"}) {
		t.Fatalf("missing = %v", rep.Missing)
	}
	for _, c := range rep.Components {
		if c.Name == "tex-packages" && !strings.Contains(c.Detail, lock.TeX.Packages[0].Name) {
			t.Errorf("detail %q does not name %s", c.Detail, lock.TeX.Packages[0].Name)
		}
	}
}

func TestProbe_ReadOnlyDoctorNeedsVerification(t *testing.T) {
	t.Parallel()
	f := newFakeTools(t)
	rep := f.probe()
	if rep.State != StateVerificationNeeded || rep.Smoke != SmokeNone {
		t.Fatalf("state = %s smoke = %s", rep.State, rep.Smoke)
	}
	if !strings.Contains(rep.Fix, "toolchain smoke") {
		t.Errorf("fix = %q", rep.Fix)
	}
	if msg := (&UnavailableError{Report: rep}).Error(); !strings.Contains(msg, "toolchain smoke") {
		t.Errorf("the unavailable error %q does not name the smoke", msg)
	}
	if _, err := os.Stat(filepath.Join(f.root.Path(), "home")); !os.IsNotExist(err) {
		t.Error("a probe must not create the TeX home or any other state")
	}
	if _, err := os.Stat(f.root.smokeDir()); !os.IsNotExist(err) {
		t.Error("a probe must not write the smoke record")
	}
}

func TestProbe_CurrentSmokeIsReady(t *testing.T) {
	t.Parallel()
	f := newFakeTools(t)
	rep := f.probe()
	if err := writeSmoke(f.root, SmokeRecord{Key: rep.Key, Passed: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.probe(); got.State != StateReady || got.Smoke != SmokeCurrent {
		t.Fatalf("state = %s smoke = %s", got.State, got.Smoke)
	}
}

func TestProbe_ToolUpgradeInvalidatesSmoke(t *testing.T) {
	t.Parallel()
	f := newFakeTools(t)
	if err := writeSmoke(f.root, SmokeRecord{Key: f.probe().Key, Passed: true}); err != nil {
		t.Fatal(err)
	}
	f.script(f.bin, "lualatex", `echo "This is LuaHBTeX, Version 1.22.0 (TeX Live 2026)"`)
	got := f.probe()
	if got.State != StateVerificationNeeded || got.Smoke != SmokeNone {
		t.Fatalf("state = %s smoke = %s, want no smoke for the changed engine", got.State, got.Smoke)
	}
}

func TestWriteSmoke_KeepsTheNewestRecordsOnly(t *testing.T) {
	t.Parallel()
	root := Root(t.TempDir())
	for i := 0; i < maxSmokeRecords+3; i++ {
		key := fmt.Sprintf("key%02d", i)
		if err := writeSmoke(root, SmokeRecord{Key: key, Passed: true}); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(time.Duration(i-100) * time.Minute)
		if err := os.Chtimes(root.smokeFile(key), old, old); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(root.smokeDir())
	if len(entries) != maxSmokeRecords {
		t.Fatalf("%d records kept, want %d", len(entries), maxSmokeRecords)
	}
	if err := writeSmoke(root, SmokeRecord{Key: "../escape", Passed: true}); err == nil {
		t.Error("a key that is not a file name was accepted")
	}
}

func TestProbe_FailedSmokeIsNotReady(t *testing.T) {
	t.Parallel()
	f := newFakeTools(t)
	if err := writeSmoke(f.root, SmokeRecord{Key: f.probe().Key, Passed: false}); err != nil {
		t.Fatal(err)
	}
	if got := f.probe(); got.State != StateVerificationNeeded || got.Smoke != SmokeFailed {
		t.Fatalf("state = %s smoke = %s", got.State, got.Smoke)
	}
}

func TestProbe_UnsupportedVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*fakeTools)
		want   string
	}{
		{"xpdf pdftotext", func(f *fakeTools) { f.script(f.bin, "pdftotext", `echo "pdftotext version 4.05" >&2`) }, "pdftotext"},
		{"old moderncv", func(f *fakeTools) { f.setModerncv("2.3.1") }, "moderncv"},
		{"pypdf not the locked version", func(f *fakeTools) { f.venv("3.14.7", "6.0.0") }, "python-venv"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeTools(t)
			tc.change(f)
			rep := f.probe()
			if rep.State != StateUnavailable || !reflect.DeepEqual(rep.Missing, []string{tc.want}) {
				t.Fatalf("state = %s missing = %v, want only %s", rep.State, rep.Missing, tc.want)
			}
		})
	}
}

func TestLock_PinsAreComplete(t *testing.T) {
	l := lock
	if l.SchemaVersion != 1 || len(l.Pypdf.WheelSHA256) != 64 || l.Pypdf.Version == "" {
		t.Fatalf("lock = %+v", l)
	}
	reqs := string(mustAsset(t, "requirements.txt"))
	if !strings.Contains(reqs, "pypdf=="+l.Pypdf.Version) || !strings.Contains(reqs, "--hash=sha256:"+l.Pypdf.WheelSHA256) {
		t.Error("requirements.txt does not pin the locked pypdf wheel by hash")
	}
	seen := map[string]bool{}
	for _, p := range l.TeX.Packages {
		if p.Name == "" || p.Witness == "" || p.Revision == "" || seen[p.Name] || seen[p.Witness] {
			t.Errorf("package %+v is incomplete or duplicated", p)
		}
		seen[p.Name], seen[p.Witness] = true, true
	}
	for _, need := range []string{"moderncv", "fontspec", "fancyhdr", "luatexbase", "pgf", "fontawesome5", "academicons", "hyperref", "geometry"} {
		if !seen[need] {
			t.Errorf("closure lacks %s", need)
		}
	}
}

// A build directory a crashed process left behind is removed by the next
// build; a recent one, which may be another build's, stays.
func TestNewBuildDir_RemovesScratchACrashLeftBehind(t *testing.T) {
	t.Parallel()
	tc := &Toolchain{Root: Root(t.TempDir())}
	old, recent := filepath.Join(tc.Root.builds(), "smoke-old"), filepath.Join(tc.Root.builds(), "smoke-recent")
	for _, d := range []string{old, recent} {
		if err := os.MkdirAll(filepath.Join(d, "resume"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-2 * staleBuildAge)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	build, err := tc.newBuildDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the stale build directory was kept")
	}
	for _, d := range []string{recent, build} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s: %v", filepath.Base(d), err)
		}
	}
}
