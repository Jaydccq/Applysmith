package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// join builds a string from parts so a test can hold a synthetic positive
// (an address, a key shape) without writing the literal into its own source,
// where the guard would report it.
func join(parts ...string) string { return strings.Join(parts, "") }

// dirTree writes files (slash path -> content) under a temporary root and
// returns a Tree over them. It does not use git.
func dirTree(t *testing.T, files map[string]string) *Tree {
	t.Helper()
	root := t.TempDir()
	tree := &Tree{Root: root}
	for p, content := range files {
		writeFile(t, root, p, content)
		tree.Paths = append(tree.Paths, p)
	}
	sort.Strings(tree.Paths)
	return tree
}

func writeFile(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitRepo initialises a repository with the given tracked files.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for p, c := range files {
		writeFile(t, root, p, c)
	}
	run("add", ".")
	return root
}

func paths(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

func execCommand(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}
