package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// repoTree loads the repository that contains this package.
func repoTree(t *testing.T) *Tree {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(wd, "..", "..")
	if dir := os.Getenv("APPLYSMITH_GUARD_ROOT"); dir != "" {
		// A publication export has no git history; the release pipeline names
		// its directory here.
		tree, err := LoadDirTree(dir)
		if err != nil {
			t.Fatal(err)
		}
		return tree
	}
	tree, err := LoadGitTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// TestRepository runs every rule against the files git would track. This is
// the entry point behind `go test ./internal/guard/...` and `make verify`.
func TestRepository(t *testing.T) {
	tree := repoTree(t)
	// The tree holds thousands of files; far fewer means it did not load and
	// every rule would pass on nothing.
	if len(tree.Paths) < 1000 {
		t.Fatalf("the tree has %d paths, want at least 1000", len(tree.Paths))
	}
	allow, problems := LoadAllowlist(tree)
	t.Run("allowlist", func(t *testing.T) {
		for _, p := range problems {
			t.Error(p)
		}
	})

	ran := map[string]bool{}
	rules := Rules()
	for _, r := range rules {
		t.Run(r.Name, func(t *testing.T) {
			found, err := r.Check(tree)
			if IsSkipped(err) {
				// Loud on both channels: the notice survives non-verbose output
				// of a failing package, and t.Skip shows under -v.
				fmt.Fprintf(os.Stderr, "GUARD NOTICE: rule %s: %v\n", r.Name, err)
				t.Skip(err)
			}
			if err != nil {
				t.Fatalf("rule could not run: %v", err)
			}
			ran[r.Name] = true
			for _, f := range allow.Suppress(found) {
				t.Error(f)
			}
		})
	}

	t.Run("allowlist-unused", func(t *testing.T) {
		if len(ran) == 0 {
			t.Skip("no rule ran, so no entry can be judged")
		}
		for _, f := range allow.Unused(ran) {
			t.Error(f)
		}
	})
}

// TestRepositoryRules_AreEveryRule pins the rule list, so dropping a rule from
// Rules() fails here rather than silently checking less.
func TestRepositoryRules_AreEveryRule(t *testing.T) {
	var got []string
	for _, r := range Rules() {
		got = append(got, r.Name)
	}
	want := []string{
		ruleTokens, ruleRetired, ruleEmail, rulePhone, ruleHomePath, ruleSecret, ruleGitleaks,
		ruleLanguage, ruleComment, ruleBudget, ruleFileLength, ruleGofmt, ruleBuildOutput, ruleInternalPath,
		ruleDeps, ruleRegexp, ruleLayers, rulePure, ruleProvenance, ruleSimilarity, ruleDocSources,
		ruleNotice, ruleNoticeDrift, ruleCaptchaScope, ruleLoopbackListener,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Rules() = %q, want %q", got, want)
	}
}
