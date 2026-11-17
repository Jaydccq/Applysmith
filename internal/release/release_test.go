package release

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/guard"
)

// gitEnv is the environment of every git command in these tests: no global or
// system configuration, and a fixed author.
func gitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Alex Example", "GIT_AUTHOR_EMAIL=alex@example.com",
		"GIT_COMMITTER_NAME=Alex Example", "GIT_COMMITTER_EMAIL=alex@example.com")
}

// newRepo makes a committed git repository holding files.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	for p, c := range files {
		write(t, repo, p, c)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "initial")
	return repo
}

func write(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// internalMaterialGuard runs the guard's internal-material rule in release
// mode over the export, which is the rule the full guard applies to class B
// paths.
func internalMaterialGuard(export string) error {
	tree, err := guard.LoadDirTree(export)
	if err != nil {
		return err
	}
	tree.Release = true
	for _, r := range guard.Rules() {
		if r.Name != "internal-material" {
			continue
		}
		found, err := r.Check(tree)
		if err != nil {
			return err
		}
		if len(found) > 0 {
			return findingsError(found)
		}
	}
	return nil
}

// run executes the pipeline and removes the export a failing run leaves behind.
func run(t *testing.T, cfg Config) Report {
	t.Helper()
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rep.Export) })
	return rep
}

const goMod = "module example.test/p\n\ngo 1.21\n"

func sh(script string) []Cmd { return []Cmd{{Argv: []string{"sh", "-c", script}}} }

func stepOf(t *testing.T, rep Report, name string) StepResult {
	t.Helper()
	for _, s := range rep.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step %q in %v", name, rep.Steps)
	return StepResult{}
}

func baseConfig(repo string) Config {
	return Config{Repo: repo, Guard: internalMaterialGuard}
}

func TestRun_RetainedPrivateOpenspecDoesNotBreakOrchestration(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"openspec/changes/x/tasks.md": "- [ ] 1.1 a\n",
		"AGENTS.md":                   "rules\n",
		"docs/exec-plans/p.md":        "plan\n",
		"main.go":                     "package main\n\nfunc main() {}\n",
		"go.mod":                      goMod,
	})
	cfg := baseConfig(repo)
	cfg.Keep = true
	// The private tree still holds its planning record when it is verified.
	cfg.Verify = sh("test -f openspec/changes/x/tasks.md && test -f AGENTS.md")
	// The export holds none of it, yet builds.
	cfg.Build = sh("! test -e openspec && ! test -e AGENTS.md && ! test -e docs/exec-plans && go build -o /dev/null ./...")
	rep := run(t, cfg)
	if rep.Failed() {
		var b bytes.Buffer
		rep.Print(&b)
		t.Fatalf("pipeline failed:\n%s", b.String())
	}
	if len(rep.Excluded) != 3 {
		t.Errorf("excluded = %v, want the three class B paths", rep.Excluded)
	}
	if _, err := os.Stat(filepath.Join(rep.Export, ".git")); err == nil {
		t.Error("the export carries a .git entry")
	}
	if rep.Metrics == nil || rep.Metrics.Files != 2 || rep.Metrics.GoLines != 3 {
		t.Errorf("metrics = %+v", rep.Metrics)
	}
}

func TestRun_PlantedExportOpenspecFailsReleaseMode(t *testing.T) {
	repo := newRepo(t, map[string]string{"main.go": "package main\n", "go.mod": goMod})
	cfg := baseConfig(repo)
	// Something puts planning material into the export after the filter ran.
	cfg.Install = sh("mkdir -p openspec/specs && echo planted > openspec/specs/a.md")
	cfg.Build = sh("true")
	rep := run(t, cfg)
	g := stepOf(t, rep, "guard-release")
	if g.Status != Failed || !strings.Contains(g.Detail, "openspec/specs/a.md") {
		t.Fatalf("guard-release = %+v, want a failure naming the planted path", g)
	}
	if b := stepOf(t, rep, "build-export"); b.Status != Skipped {
		t.Errorf("build-export = %+v, want skipped after the guard failed", b)
	}
	if !rep.Failed() {
		t.Error("report does not fail")
	}
}

func TestRun_MissingExportBuildInputFails(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"go.mod":    goMod,
		"main.go":   "package main\n\nimport _ \"embed\"\n\n//go:embed AGENTS.md\nvar rules string\n\nfunc main() { _ = rules }\n",
		"AGENTS.md": "rules\n",
	})
	t.Setenv("GOWORK", "off")
	cfg := baseConfig(repo)
	cfg.Verify = []Cmd{{Argv: []string{"go", "build", "-o", "/dev/null", "./..."}}}
	cfg.Build = []Cmd{{Argv: []string{"go", "build", "-o", "/dev/null", "./..."}}}
	rep := run(t, cfg)
	if v := stepOf(t, rep, "verify-private"); v.Status != OK {
		t.Fatalf("the private tree must build: %+v", v)
	}
	b := stepOf(t, rep, "build-export")
	if b.Status != Failed || !strings.Contains(b.Detail, "AGENTS.md") {
		t.Fatalf("build-export = %+v, want a failure naming the missing input", b)
	}
}

func TestRun_HistorySecretFailsWithoutStoppingIndependentSteps(t *testing.T) {
	repo := newRepo(t, map[string]string{"main.go": "package main\n", "go.mod": goMod})
	write(t, repo, "old.env", "K="+strings.Join([]string{"gh", "p_", strings.Repeat("a1B2", 8)}, "")+"\n")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "add"}, {"rm", "-q", "old.env"}, {"commit", "-q", "-m", "rm"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cfg := baseConfig(repo)
	cfg.Verify = sh("true")
	cfg.Build = sh("true")
	rep := run(t, cfg)
	h := stepOf(t, rep, "history-scan")
	if h.Status != Failed || !strings.Contains(h.Detail, "old.env") {
		t.Fatalf("history-scan = %+v", h)
	}
	if e := stepOf(t, rep, "export"); e.Status != OK {
		t.Errorf("export = %+v, want it to run despite the history finding", e)
	}
	if !rep.Failed() {
		t.Error("report does not fail")
	}
}

func TestRun_UncommittedChangesBlockTheExport(t *testing.T) {
	repo := newRepo(t, map[string]string{"main.go": "package main\n"})
	write(t, repo, "new.go", "package main\n")
	rep := run(t, baseConfig(repo))
	if c := stepOf(t, rep, "clean-tree"); c.Status != Failed || !strings.Contains(c.Detail, "new.go") {
		t.Errorf("clean-tree = %+v", c)
	}
	if e := stepOf(t, rep, "export"); e.Status != Skipped {
		t.Errorf("export = %+v, want skipped", e)
	}
}

func TestRun_ReleaseModeRequiresListsAndGitleaks(t *testing.T) {
	repo := newRepo(t, map[string]string{"main.go": "package main\n"})
	t.Setenv("APPLYSMITH_PII_TOKENS", "")
	t.Setenv("APPLYSMITH_RETIRED_NAMES", "")
	t.Setenv("PATH", t.TempDir())
	cfg := baseConfig(repo)
	cfg.RequireLists, cfg.RequireGitleaks = true, true
	rep := run(t, cfg)
	d := stepOf(t, rep, "tools").Detail
	for _, want := range []string{"APPLYSMITH_PII_TOKENS", "APPLYSMITH_RETIRED_NAMES", "gitleaks is not on PATH"} {
		if !strings.Contains(d, want) {
			t.Errorf("tools detail %q lacks %q", d, want)
		}
	}
}

func TestRun_GitleaksRunsOnHistoryAndExport(t *testing.T) {
	repo := newRepo(t, map[string]string{"main.go": "package main\n"})
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	write(t, bin, "gitleaks", fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\n", log))
	if err := os.Chmod(filepath.Join(bin, "gitleaks"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := baseConfig(repo)
	cfg.RequireGitleaks = true
	cfg.Verify, cfg.Build = sh("true"), sh("true")
	rep := run(t, cfg)
	if rep.Failed() {
		t.Fatalf("steps = %+v", rep.Steps)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "git ") || !strings.Contains(lines[0], "--log-opts=--all --full-history") || !strings.HasPrefix(lines[1], "dir ") {
		t.Fatalf("gitleaks calls = %q, want every history ref and a directory export scan", lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, "--config ") || !strings.Contains(line, "--ignore-gitleaks-allow") || !strings.Contains(line, "--gitleaks-ignore-path ") {
			t.Errorf("scanner configuration or suppression refusal missing: %q", line)
		}
	}
}

func TestRun_PublishTreeStopsAfterTheGuard(t *testing.T) {
	repo := newRepo(t, map[string]string{"AGENTS.md": "x\n", "main.go": "package main\n"})
	out := filepath.Join(t.TempDir(), "tree")
	cfg := baseConfig(repo)
	cfg.PublishTree, cfg.Out = true, out
	rep := run(t, cfg)
	if rep.Failed() {
		t.Fatalf("steps = %+v", rep.Steps)
	}
	for _, s := range rep.Steps {
		if strings.Contains(s.Name, "build") || strings.Contains(s.Name, "verify") || strings.Contains(s.Name, "history") {
			t.Errorf("publish-tree ran %s", s.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "main.go")); err != nil {
		t.Error("export missing main.go")
	}
	if _, err := os.Stat(filepath.Join(out, "AGENTS.md")); err == nil {
		t.Error("export holds a class B file")
	}
}

func TestExport_RefusesANonEmptyDirectory(t *testing.T) {
	repo := newRepo(t, map[string]string{"main.go": "package main\n"})
	out := t.TempDir()
	write(t, out, "stale.txt", "x")
	if _, err := Export(repo, out); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestPackageBound(t *testing.T) {
	if err := checkAppBound(maxAppBytes); err != nil {
		t.Errorf("a bundle at the bound failed: %v", err)
	}
	if err := checkAppBound(maxAppBytes + 1); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Errorf("err = %v, want a failure naming the bound", err)
	}
}

func TestRun_ScannerFindingDoesNotSkipTheBuild(t *testing.T) {
	repo := newRepo(t, map[string]string{"main.go": "package main\n"})
	bin := t.TempDir()
	write(t, bin, "gitleaks", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(bin, "gitleaks"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := baseConfig(repo)
	cfg.RequireGitleaks = true
	cfg.Verify, cfg.Build = sh("true"), sh("true")
	rep := run(t, cfg)
	if g := stepOf(t, rep, "gitleaks-export"); g.Status != Failed {
		t.Errorf("gitleaks-export = %+v", g)
	}
	if b := stepOf(t, rep, "build-export"); b.Status != OK {
		t.Errorf("build-export = %+v, want it to run", b)
	}
	if !rep.Failed() {
		t.Error("report does not fail")
	}
}

func TestSummarizeKeepsEveryFailureAheadOfTheTail(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- FAIL: TestEarly (0.00s)\nFAIL\texample.test/early\t0.1s\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "ok  \texample.test/p%d\t0.1s\n", i)
	}
	got := summarize(b.String())
	if !strings.HasPrefix(got, "failing:\n  --- FAIL: TestEarly") || !strings.Contains(got, "example.test/early") || !strings.Contains(got, "earlier lines omitted") {
		t.Errorf("summary = %q", got)
	}
}
