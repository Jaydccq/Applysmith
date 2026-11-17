package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func fakeHome(t *testing.T) (home, everyday string) {
	t.Helper()
	home = t.TempDir()
	everyday = everydayChromeDirs(runtime.GOOS, home, func(string) string { return "" })[0]
	return home, everyday
}

func TestBrowser_Launch_RefusesEverydayProfile(t *testing.T) {
	home, everyday := fakeHome(t)
	if err := os.MkdirAll(everyday, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"equal":      everyday,
		"inside":     filepath.Join(everyday, "Profile 9"),
		"containing": filepath.Dir(everyday),
	} {
		_, err := prepareProfile(dir, home, func(string) string { return "" })
		if code, _ := CodeOf(err); code != CodeProfileUnsafe {
			t.Errorf("%s: code %q err %v, want profile-unsafe", name, code, err)
		}
	}
	if _, err := os.Stat(filepath.Join(everyday, "Profile 9")); err == nil {
		t.Fatal("a refused profile directory was created")
	}
}

func TestBrowser_Launch_RefusesSymlinkIntoEverydayProfile(t *testing.T) {
	home, everyday := fakeHome(t)
	if err := os.MkdirAll(everyday, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "profile")
	if err := os.Symlink(everyday, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := prepareProfile(link, home, func(string) string { return "" }); err == nil {
		t.Fatal("a symlink to the everyday profile was accepted")
	}
}

func TestBrowser_PrepareProfile_CreatesPrivateDirectory(t *testing.T) {
	home, _ := fakeHome(t)
	dir := filepath.Join(t.TempDir(), "a", "b", "profile")
	got, err := prepareProfile(dir, home, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(got)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("profile mode %v err %v, want 0700", st.Mode().Perm(), err)
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 99000; pid < 99999; pid++ {
		if !processAlive(pid) {
			return pid
		}
	}
	t.Skip("no dead pid found")
	return 0
}

func TestBrowser_Launch_ClearsStaleSingletonArtifacts(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("example-host-"+strconv.Itoa(deadPID(t)), filepath.Join(dir, "SingletonLock")); err != nil {
		t.Skip("symlinks unavailable")
	}
	for _, n := range []string{"SingletonCookie", "SingletonSocket"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := clearStaleSingleton(dir); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s was not removed", n)
		}
	}
}

func TestBrowser_Launch_LiveSingletonHolderIsProfileInUse(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("example-host-"+strconv.Itoa(os.Getpid()), filepath.Join(dir, "SingletonLock")); err != nil {
		t.Skip("symlinks unavailable")
	}
	err := clearStaleSingleton(dir)
	if code, _ := CodeOf(err); code != CodeProfileInUse {
		t.Fatalf("code %q err %v, want profile-in-use", code, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "SingletonLock")); err != nil {
		t.Fatal("a live holder's lock was removed")
	}
}
