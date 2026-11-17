package cliinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fixture is a temporary home and an app bundle binary inside it.
func fixture(t *testing.T) Target {
	t.Helper()
	root := t.TempDir()
	exe := filepath.Join(root, "Apps", "Applysmith.app", "Contents", "MacOS", "Applysmith")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return Target{Home: home, Exe: exe}
}

func mustState(t *testing.T, tg Target, want State) {
	t.Helper()
	got, err := tg.Inspect()
	if err != nil || got != want {
		t.Fatalf("state %q (err %v), want %q", got, err, want)
	}
}

func TestInstall_CreatesTheLinkAndTheFolder(t *testing.T) {
	tg := fixture(t)
	mustState(t, tg, NotInstalled)
	changed, err := tg.Install()
	if err != nil || !changed {
		t.Fatalf("install: changed %v err %v", changed, err)
	}
	dest, err := os.Readlink(tg.Link())
	if err != nil || dest != tg.Exe {
		t.Fatalf("link points at %q (err %v), want %q", dest, err, tg.Exe)
	}
	mustState(t, tg, Installed)
}

func TestInstall_AlreadyOursChangesNothing(t *testing.T) {
	tg := fixture(t)
	if _, err := tg.Install(); err != nil {
		t.Fatal(err)
	}
	changed, err := tg.Install()
	if err != nil || changed {
		t.Fatalf("second install: changed %v err %v", changed, err)
	}
}

func TestInstall_PointsAStaleLinkAtThisApp(t *testing.T) {
	tg := fixture(t)
	old := Target{Home: tg.Home, Exe: filepath.Join(filepath.Dir(tg.Exe), "..", "..", "..", "Old", "Applysmith.app", "Contents", "MacOS", "Applysmith")}
	old.Exe = filepath.Clean(old.Exe)
	if err := os.MkdirAll(old.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old.Exe, tg.Link()); err != nil { // dangling, as after the app was moved
		t.Fatal(err)
	}
	mustState(t, tg, Stale)
	changed, err := tg.Install()
	if err != nil || !changed {
		t.Fatalf("install over a stale link: changed %v err %v", changed, err)
	}
	mustState(t, tg, Installed)
}

func TestInstall_RefusesWhatIsNotOurs(t *testing.T) {
	tg := fixture(t)
	if err := os.MkdirAll(tg.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "applysmith-other")
	if err := os.WriteFile(other, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, make := range map[string]func() error{
		"regular file":     func() error { return os.WriteFile(tg.Link(), []byte("mine"), 0o755) },
		"foreign symlink":  func() error { return os.Symlink(other, tg.Link()) },
		"directory":        func() error { return os.Mkdir(tg.Link(), 0o755) },
		"link into a tool": func() error { return os.Symlink(filepath.Join(filepath.Dir(other), "bin", "applysmith"), tg.Link()) },
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.RemoveAll(tg.Link())
			if err := make(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(tg.Link())
			mustState(t, tg, Foreign)
			if _, err := tg.Install(); !errors.Is(err, ErrForeign) {
				t.Fatalf("install: %v, want ErrForeign", err)
			}
			if _, err := tg.Remove(); !errors.Is(err, ErrForeign) {
				t.Fatalf("remove: %v, want ErrForeign", err)
			}
			after, err := os.Lstat(tg.Link())
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("the file at the link path was touched (err %v)", err)
			}
		})
	}
}

func TestInstall_NeedsAnInstalledApp(t *testing.T) {
	tg := fixture(t)
	tg.Exe = filepath.Join(t.TempDir(), "applysmith")
	if _, err := tg.Install(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("install from a loose binary: %v, want ErrUnavailable", err)
	}
	if _, err := os.Lstat(tg.Link()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a link was made anyway: %v", err)
	}
}

func TestRemove_DeletesOnlyOurLink(t *testing.T) {
	tg := fixture(t)
	if changed, err := tg.Remove(); err != nil || changed {
		t.Fatalf("remove with nothing there: changed %v err %v", changed, err)
	}
	if _, err := tg.Install(); err != nil {
		t.Fatal(err)
	}
	if changed, err := tg.Remove(); err != nil || !changed {
		t.Fatalf("remove: changed %v err %v", changed, err)
	}
	mustState(t, tg, NotInstalled)
	if _, err := os.Stat(tg.Exe); err != nil {
		t.Fatalf("the app binary was removed: %v", err)
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestOnPath(t *testing.T) {
	tg := fixture(t)
	ctx := context.Background()
	if tg.OnPath(ctx, env(map[string]string{"PATH": "/usr/bin:/bin"})) {
		t.Error("a PATH without the folder reads as on PATH")
	}
	if !tg.OnPath(ctx, env(map[string]string{"PATH": "/usr/bin:" + tg.Dir() + "/:/bin"})) {
		t.Error("a PATH with the folder (trailing slash) reads as off PATH")
	}
}

// A program started from the Finder has a short PATH; the login shell knows the
// real one.
func TestOnPath_AsksTheLoginShell(t *testing.T) {
	tg := fixture(t)
	script := func(path string) string {
		p := filepath.Join(t.TempDir(), "fakeshell")
		body := "#!/bin/sh\necho 'profile noise'\nprintf '%s%s%s' __APPLYSMITH_PATH_START__ '" + path + "' __APPLYSMITH_PATH_END__\necho trailing\n"
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	short := map[string]string{"PATH": "/usr/bin:/bin"}
	short["SHELL"] = script("/usr/bin:" + tg.Dir())
	if !tg.OnPath(context.Background(), env(short)) {
		t.Error("the login shell's PATH names the folder but it read as off PATH")
	}
	short["SHELL"] = script("/usr/bin")
	if tg.OnPath(context.Background(), env(short)) {
		t.Error("the login shell's PATH lacks the folder but it read as on PATH")
	}
	failing := filepath.Join(t.TempDir(), "failing")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	short["SHELL"] = failing
	if tg.OnPath(context.Background(), env(short)) {
		t.Error("a shell that fails must read as off PATH")
	}
	// A shell named by a relative path is never run, even one that would
	// answer with the folder.
	answering := script("/usr/bin:" + tg.Dir())
	t.Chdir(filepath.Dir(answering))
	short["SHELL"] = "./" + filepath.Base(answering)
	if tg.OnPath(context.Background(), env(short)) {
		t.Error("a relative shell name must not be run")
	}
}
