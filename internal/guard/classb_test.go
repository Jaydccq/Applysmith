package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassBReason(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"openspec/changes/x/tasks.md", true},
		{"AGENTS.md", true},
		{"docs/CLAUDE.md", true},
		{".claude/settings.json", true},
		{".codex/hooks.json", true},
		{".github/hooks/pre.json", true},
		{".github/copilot-instructions.md", true},
		{"docs/exec-plans/active/p.md", true},
		{"plans/next.md", true},
		{"docs/research/ats.md", true},
		{"docs/parity/forms.md", true},
		{"docs/parity-audit-forms.md", true},
		{"MEMORY.md", true},
		{"internal/guard/guard.go", false},
		{"internal/agent/memory/store.go", false},
		{"docs/architecture/overview.md", false},
		{"skills/applysmith/SKILL.md", false},
		{".github/workflows/ci.yml", false},
		{"internal/x/notes/notes.go", false},
	}
	for _, c := range cases {
		if got := ClassBReason(c.path) != ""; got != c.want {
			t.Errorf("ClassBReason(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestInternalMaterial_ClassBAcceptedInDevelopmentFailsInRelease(t *testing.T) {
	tree := dirTree(t, map[string]string{"openspec/changes/x/tasks.md": "- [ ] a\n", "AGENTS.md": "rules\n", "main.go": "package main\n"})
	found, err := checkInternalPaths(tree)
	if err != nil || len(found) != 0 {
		t.Fatalf("development mode: %v, %v", found, err)
	}
	tree.Release = true
	found, err = checkInternalPaths(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Path != "AGENTS.md" || found[1].Path != "openspec/changes/x/tasks.md" {
		t.Fatalf("release mode findings = %v", found)
	}
}

func TestInternalMaterial_ClassAFailsInEveryMode(t *testing.T) {
	for _, release := range []bool{false, true} {
		tree := dirTree(t, map[string]string{"cv.md": "x\n", "notes.bak": "x\n"})
		tree.Release = release
		found, _ := checkInternalPaths(tree)
		if len(found) != 2 {
			t.Errorf("release=%v: findings = %v, want both class A paths", release, found)
		}
	}
}

func TestLoadDirTree_ListsAnExportWithoutGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "web/dist/\nnode_modules/\n")
	writeFile(t, root, "main.go", "package main\n")
	writeFile(t, root, "openspec/specs/a.md", "x\n")
	writeFile(t, root, "web/dist/index.html", "<p>\n")
	writeFile(t, root, "web/node_modules/a/index.js", "x\n")
	tree, err := LoadDirTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tree.Paths, ","); got != ".gitignore,main.go,openspec/specs/a.md" {
		t.Errorf("paths = %s", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		t.Error("loading a directory tree created a .git entry")
	}
}

func TestLoadDirTree_RefusesAHistory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	if _, err := LoadDirTree(root); err == nil || !strings.Contains(err.Error(), ".git") {
		t.Fatalf("err = %v, want a refusal naming .git", err)
	}
}
