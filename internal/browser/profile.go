package browser

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// everydayChromeDirs lists where the user's own Chrome keeps its data.
func everydayChromeDirs(goos, home string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Google", "Chrome")}
	case "windows":
		if base := getenv("LocalAppData"); base != "" {
			return []string{filepath.Join(base, "Google", "Chrome", "User Data")}
		}
		return []string{filepath.Join(home, "AppData", "Local", "Google", "Chrome", "User Data")}
	default:
		return []string{filepath.Join(home, ".config", "google-chrome")}
	}
}

// canonical resolves symlinks through the longest existing prefix of p, so a
// profile path that does not exist yet still compares correctly.
func canonical(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for cur := p; ; cur = filepath.Dir(cur) {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest), nil
		}
		if filepath.Dir(cur) == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

// foldCase reports whether the platform's usual file systems ignore case.
func foldCase() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }

func within(parent, child string) bool {
	if foldCase() {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sameOrInside reports whether child is, or lies inside, parent by file
// identity: walking child's ancestors and comparing them with parent, which
// holds whatever the case or the links of the spelling.
func sameOrInside(parent, child string) bool {
	pst, err := os.Stat(parent)
	if err != nil {
		return false
	}
	for cur := child; ; cur = filepath.Dir(cur) {
		if st, err := os.Stat(cur); err == nil && os.SameFile(pst, st) {
			return true
		}
		if filepath.Dir(cur) == cur {
			return false
		}
	}
}

// overlapsEveryday reports whether dir equals, contains or lies inside the
// user's everyday Chrome data directory.
func overlapsEveryday(dir, home string, getenv func(string) string) bool {
	canon, err := canonical(dir)
	if err != nil {
		return true
	}
	for _, d := range everydayChromeDirs(runtime.GOOS, home, getenv) {
		cd, err := canonical(d)
		if err != nil {
			continue
		}
		if within(cd, canon) || within(canon, cd) || sameOrInside(cd, canon) || sameOrInside(canon, cd) {
			return true
		}
	}
	return false
}

// prepareProfile validates the profile directory against the user's everyday
// Chrome data, creates it with mode 0700, and returns its canonical path. A
// profile equal to, containing or inside an everyday directory is refused
// before anything is created.
func prepareProfile(dir, home string, getenv func(string) string) (string, error) {
	if dir == "" {
		return "", newError(CodeProfileUnsafe, "no profile directory configured")
	}
	canon, err := canonical(dir)
	if err != nil {
		return "", wrapError(CodeProfileUnsafe, err, "cannot resolve profile directory")
	}
	if overlapsEveryday(canon, home, getenv) {
		return "", newError(CodeProfileUnsafe, "profile directory %s overlaps the everyday Chrome data directory", canon)
	}
	if err := os.MkdirAll(canon, 0o700); err != nil {
		return "", wrapError(CodeProfileUnsafe, err, "cannot create profile directory")
	}
	if err := os.Chmod(canon, 0o700); err != nil {
		return "", wrapError(CodeProfileUnsafe, err, "cannot restrict profile directory")
	}
	return canon, nil
}

// lockHolder returns the pid named by SingletonLock, whose link target is
// "<host>-<pid>", or 0 when there is none.
func lockHolder(profile string) int {
	target, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		return 0
	}
	i := strings.LastIndex(target, "-")
	if i < 0 {
		return 0
	}
	pid, _ := strconv.Atoi(target[i+1:])
	return pid
}

// clearStaleSingleton removes SingletonLock, SingletonCookie and SingletonSocket
// left by a process that is gone. A live holder is profile-in-use.
func clearStaleSingleton(profile string) error {
	if pid := lockHolder(profile); pid > 0 && processAlive(pid) {
		return newError(CodeProfileInUse, "profile %s is locked by running process %d", profile, pid)
	}
	for _, n := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		if err := os.Remove(filepath.Join(profile, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return wrapError(CodeProfileInUse, err, "cannot remove stale %s", n)
		}
	}
	return nil
}
