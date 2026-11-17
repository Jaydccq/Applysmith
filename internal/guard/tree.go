// Package guard holds the release-hygiene checks. Each rule is a function
// over a Tree that returns Findings; guard_test.go runs every rule against the
// repository so `go test ./internal/guard/...` is the single entry point.
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
	"unicode/utf8"
)

// Finding is one rule violation. It never carries a protected value in its
// printed form: Excerpt is already masked, and Value (the exact matched text,
// used only to match allowlist entries) is not printed.
type Finding struct {
	Rule    string
	Path    string
	Line    int // 1-based; 0 when the finding is about the file as a whole
	Col     int // 1-based rune column; 0 when unknown
	Detail  string
	Excerpt string
	Value   string
}

func (f Finding) String() string {
	loc := f.Path
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", loc, f.Line)
		if f.Col > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Col)
		}
	}
	s := fmt.Sprintf("%s: %s: %s", f.Rule, loc, f.Detail)
	if f.Excerpt != "" {
		s += " [" + f.Excerpt + "]"
	}
	return s
}

// mask keeps the first character of a match and replaces the rest with
// asterisks, so a report locates a value without reproducing it.
func mask(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return ""
	}
	return string(r) + strings.Repeat("*", utf8.RuneCountInString(s)-1)
}

// Tree is the set of files a guard run looks at: the files git tracks plus
// untracked files that git does not ignore.
type Tree struct {
	Root     string
	Paths    []string // slash-separated, relative to Root, sorted, existing regular files
	Gitlinks []string // tracked submodule entries
	// Release is true when the run is a publication check; optional inputs
	// that are missing then fail instead of skipping.
	Release bool
	// packages is the go list graph, listed once for the layer and purity
	// rules; the rules run one after another.
	packages *packageList
}

// LoadGitTree lists the files of the git worktree that contains dir.
func LoadGitTree(dir string) (*Tree, error) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(string(top))
	list, err := git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	staged, err := git(root, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	t := &Tree{Root: root, Release: os.Getenv("APPLYSMITH_GUARD_RELEASE") == "1"}
	for _, entry := range strings.Split(string(staged), "\x00") {
		// "<mode> <object> <stage>\t<path>"
		if strings.HasPrefix(entry, "160000 ") {
			if _, p, ok := strings.Cut(entry, "\t"); ok {
				t.Gitlinks = append(t.Gitlinks, p)
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range strings.Split(string(list), "\x00") {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || !fi.Mode().IsRegular() {
			continue // deleted in the worktree, a symlink or a gitlink directory
		}
		t.Paths = append(t.Paths, p)
	}
	sort.Strings(t.Paths)
	return t, nil
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (t *Tree) abs(p string) string { return filepath.Join(t.Root, filepath.FromSlash(p)) }

// Read returns the content of a file in the tree.
func (t *Tree) Read(p string) ([]byte, error) { return os.ReadFile(t.abs(p)) }

// Has reports whether the tree contains the path.
func (t *Tree) Has(p string) bool {
	i := sort.SearchStrings(t.Paths, p)
	return i < len(t.Paths) && t.Paths[i] == p
}

// isBinary applies the rule shared by every content scan: a NUL byte in the
// first 8 KiB marks the file as binary.
func isBinary(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// textFile is a file whose content scans apply.
type textFile struct {
	Path  string
	Data  []byte
	Lines []string
}

// eachText calls fn for every non-binary file whose path passes keep.
func (t *Tree) eachText(keep func(string) bool, fn func(textFile)) error {
	var errs []error
	for _, p := range t.Paths {
		if keep != nil && !keep(p) {
			continue
		}
		data, err := t.Read(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if isBinary(data) {
			continue
		}
		fn(textFile{Path: p, Data: data, Lines: strings.Split(string(data), "\n")})
	}
	return errors.Join(errs...)
}

// runeCol converts a byte offset within line to a 1-based rune column.
func runeCol(line string, byteOff int) int {
	if byteOff > len(line) {
		byteOff = len(line)
	}
	return utf8.RuneCountInString(line[:byteOff]) + 1
}

func ext(p string) string { return strings.ToLower(filepath.Ext(p)) }
