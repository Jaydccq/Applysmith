package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTokens(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantTokens   int
		wantProblems []string // detail substrings
		wantLine     int      // line of the first problem
	}{
		{"literals and comments", "# comment\n\nalpha-name\n  Beta Name  \n", 2, nil, 0},
		{"regular expression", "re:Ada\\s+Example\n", 1, nil, 0},
		{"two characters is too short", "ok-token\nab\n", 1, []string{"shorter than 3"}, 2},
		{"invalid expression", "re:(unclosed\n", 0, []string{"invalid regular expression"}, 1},
		{"empty list", "# only a comment\n\n", 0, []string{"no usable token"}, 0},
		{"byte order mark", "\xef\xbb\xbfalpha-name\n", 1, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toks, problems := parseTokens(ruleTokens, envTokens, tc.content)
			if len(toks) != tc.wantTokens {
				t.Errorf("tokens = %d, want %d", len(toks), tc.wantTokens)
			}
			if len(problems) != len(tc.wantProblems) {
				t.Fatalf("problems = %v, want %v", problems, tc.wantProblems)
			}
			for i, want := range tc.wantProblems {
				if !strings.Contains(problems[i].Detail, want) {
					t.Errorf("problem %q lacks %q", problems[i].Detail, want)
				}
				if problems[i].Line != tc.wantLine {
					t.Errorf("problem line = %d, want %d", problems[i].Line, tc.wantLine)
				}
			}
		})
	}
}

func TestScanTokens(t *testing.T) {
	const secretWord = "zorblax"
	tree := dirTree(t, map[string]string{
		"notes/a.txt":        "first line\nsome ZorBlax here\n",
		"notes/b.txt":        "Ada Example and Ada   Example\n",
		"clean.txt":          "nothing to see\n",
		"zorblax-fixture.md": "path match only\n",
		"bin.dat":            "binary " + secretWord + "\x00tail",
	})
	toks, problems := parseTokens(ruleTokens, envTokens, secretWord+"\nre:Ada\\s{2,}Example\n")
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	got, err := scanTokens(ruleTokens, tree, toks)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		path      string
		line, col int
		ordinal   string
	}
	wants := []want{
		{"notes/a.txt", 2, 6, "#1"},        // content, any letter case
		{"notes/b.txt", 1, 17, "#2"},       // regular expression
		{"zorblax-fixture.md", 0, 1, "#1"}, // path
	}
	if len(got) != len(wants) {
		t.Fatalf("findings = %v", got)
	}
	for _, w := range wants {
		found := false
		for _, f := range got {
			if f.Path == w.path && f.Line == w.line && f.Col == w.col && strings.HasSuffix(f.Detail, w.ordinal) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing finding %+v in %v", w, got)
		}
	}
	// Binary content is not scanned, and no output carries the token text.
	for _, f := range got {
		if f.Path == "bin.dat" {
			t.Error("binary file content was scanned")
		}
		s := strings.ToLower(strings.Replace(f.String(), f.Path, "", 1)) // a path is reported by design
		if strings.Contains(s, secretWord) || strings.Contains(s, "example") {
			t.Errorf("output reveals protected text: %q", f.String())
		}
		if f.Excerpt != "" || f.Value != "" {
			t.Errorf("token finding carries a value: %+v", f)
		}
	}
}

func TestTokenRule(t *testing.T) {
	rule := tokenRule(ruleTokens, envTokens)
	tree := dirTree(t, map[string]string{"a.txt": "hello\n"})
	outside := t.TempDir()
	write := func(dir, content string) string {
		p := filepath.Join(dir, "tokens.txt")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("unset in development is a loud skip", func(t *testing.T) {
		t.Setenv(envTokens, "")
		_, err := rule.Check(tree)
		if !IsSkipped(err) || !strings.Contains(err.Error(), "did NOT run") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unset in release mode fails", func(t *testing.T) {
		t.Setenv(envTokens, "")
		rel := *tree
		rel.Release = true
		fs, err := rule.Check(&rel)
		if err != nil || len(fs) != 1 || !strings.Contains(fs[0].Detail, "required for release") {
			t.Fatalf("fs=%v err=%v", fs, err)
		}
	})
	t.Run("list inside the repository fails before scanning", func(t *testing.T) {
		t.Setenv(envTokens, write(tree.Root, "hello\n"))
		fs, _ := rule.Check(tree)
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "outside the repository") {
			t.Fatalf("fs=%v", fs)
		}
	})
	t.Run("unreadable list fails", func(t *testing.T) {
		t.Setenv(envTokens, filepath.Join(outside, "missing.txt"))
		fs, _ := rule.Check(tree)
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "unreadable") {
			t.Fatalf("fs=%v", fs)
		}
	})
	t.Run("two-character token is a configuration failure", func(t *testing.T) {
		t.Setenv(envTokens, write(outside, "hello\nab\n"))
		fs, _ := rule.Check(tree)
		if len(fs) != 1 || fs[0].Line != 2 {
			t.Fatalf("fs=%v", fs)
		}
	})
	t.Run("match is reported", func(t *testing.T) {
		t.Setenv(envTokens, write(outside, "HELLO\n"))
		fs, err := rule.Check(tree)
		if err != nil || len(fs) != 1 || fs[0].Path != "a.txt" {
			t.Fatalf("fs=%v err=%v", fs, err)
		}
	})
}

// TestTokenScanOnGitTree seeds a fake token into a tracked file of a real
// repository, then removes it.
func TestTokenScanOnGitTree(t *testing.T) {
	const fake = "qwertyfake-token"
	root := gitRepo(t, map[string]string{"src/main.go": "package main\n\nvar x = \"" + fake + "\"\n", ".gitignore": "ignored.txt\n"})
	writeFile(t, root, "untracked.txt", "also "+fake+"\n")
	writeFile(t, root, "ignored.txt", fake+"\n")
	list := filepath.Join(t.TempDir(), "tokens.txt")
	if err := os.WriteFile(list, []byte("# fake\n"+fake+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envTokens, list)

	tree, err := LoadGitTree(root)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := tokenRule(ruleTokens, envTokens).Check(tree)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(fs); len(got) != 2 || got[0] != "src/main.go" || got[1] != "untracked.txt" {
		t.Fatalf("expected the tracked and the untracked file, not the ignored one: %v", fs)
	}

	writeFile(t, root, "src/main.go", "package main\n")
	writeFile(t, root, "untracked.txt", "clean\n")
	tree, _ = LoadGitTree(root)
	if fs, _ := tokenRule(ruleTokens, envTokens).Check(tree); len(fs) != 0 {
		t.Fatalf("after removal: %v", fs)
	}
}

func TestRetiredNamesSkipPlanningTreeOutsideRelease(t *testing.T) {
	rule := tokenRule(ruleRetired, envRetired)
	tree := dirTree(t, map[string]string{
		"openspec/plan.md": "migrated away from oldname\n",
		"docs/a.md":        "clean\n",
	})
	list := filepath.Join(t.TempDir(), "retired.txt")
	if err := os.WriteFile(list, []byte("oldname\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envRetired, list)
	if fs, err := rule.Check(tree); err != nil || len(fs) != 0 {
		t.Fatalf("development: fs=%v err=%v", fs, err)
	}
	rel := *tree
	rel.Release = true
	if fs, err := rule.Check(&rel); err != nil || len(fs) != 1 || fs[0].Path != "openspec/plan.md" {
		t.Fatalf("release: fs=%v err=%v", fs, err)
	}
}
