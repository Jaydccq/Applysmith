// Package release runs the publication pipeline: verify the private tree,
// scan its history, export the tracked tree without internal working material,
// and check, build and test that exact export. It never pushes or publishes.
package release

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"applysmith/internal/guard"
)

// ExportResult says what an export copied and what it left out.
type ExportResult struct {
	Copied   int
	Excluded []string // class B paths that were tracked and are not in the export
}

func gitOut(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// CheckClean fails when the working tree differs from what git tracks, since
// an export of a dirty tree is not the tree that was committed.
func CheckClean(repo string) error {
	out, err := gitOut(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	var entries []string
	for _, e := range strings.Split(string(out), "\x00") {
		if e != "" {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	if len(entries) > 10 {
		entries = append(entries[:10], fmt.Sprintf("... and %d more", len(entries)-10))
	}
	return fmt.Errorf("the working tree is not clean; commit or remove:\n  %s", strings.Join(entries, "\n  "))
}

// Export copies the files git tracks at repo into out, leaving out every
// class B path. out must be absent or empty. The copy has no .git entry.
func Export(repo, out string) (ExportResult, error) {
	var res ExportResult
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return res, fmt.Errorf("export directory %s is not empty", out)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	list, err := gitOut(repo, "ls-files", "-z", "--cached")
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return res, err
	}
	for _, p := range strings.Split(string(list), "\x00") {
		if p == "" {
			continue
		}
		if guard.ClassBReason(p) != "" {
			res.Excluded = append(res.Excluded, p)
			continue
		}
		src := filepath.Join(repo, filepath.FromSlash(p))
		fi, err := os.Lstat(src)
		if err != nil {
			return res, err
		}
		if !fi.Mode().IsRegular() {
			continue // a gitlink or symlink; the guard fails the private tree on a gitlink
		}
		if err := copyFile(src, filepath.Join(out, filepath.FromSlash(p)), fi.Mode().Perm()); err != nil {
			return res, err
		}
		res.Copied++
	}
	return res, nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
