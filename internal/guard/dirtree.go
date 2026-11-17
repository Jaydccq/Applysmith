package guard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// LoadDirTree lists the files under root for a tree that has no git history,
// such as a publication export. Ignore rules come from the .gitignore files
// inside root, evaluated by git against a throwaway repository so that no
// .git directory ever appears in root. A root that holds a .git entry is
// refused: an export must not carry history.
func LoadDirTree(root string) (*Tree, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		return nil, errors.New("directory tree holds a .git entry; an export must not carry history")
	}
	scratch, err := os.MkdirTemp("", "applysmith-guard-gitdir-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	gitDir := filepath.Join(scratch, "git")
	if out, err := isolatedGit("", "init", "--bare", "-q", gitDir); err != nil {
		return nil, fmt.Errorf("%w: %s", err, out)
	}
	list, err := isolatedGit(root, "--git-dir", gitDir, "--work-tree", root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, list)
	}
	t := &Tree{Root: root, Release: os.Getenv("APPLYSMITH_GUARD_RELEASE") == "1"}
	for _, p := range strings.Split(string(list), "\x00") {
		if p == "" {
			continue
		}
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		t.Paths = append(t.Paths, p)
	}
	sort.Strings(t.Paths)
	return t, nil
}

// isolatedGit runs git without the user's or the system's configuration, so a
// global ignore file cannot change which files a tree contains.
func isolatedGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return stderr.Bytes(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
