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
	"time"

	"applysmith/internal/guard"
)

// Cmd is one external command of the pipeline.
type Cmd struct {
	Dir  string // relative to the tree the command runs in; empty means its root
	Argv []string
}

func (c Cmd) String() string { return strings.Join(c.Argv, " ") }

// PackageResult describes the packaged desktop app.
type PackageResult struct {
	Path    string
	Bytes   int64
	Pending []string // parts of packaging that are not available yet
}

// Config describes one pipeline run. The zero value of each command list
// skips that step, which is how the tests exercise one rule at a time.
type Config struct {
	Repo string
	// Out is the export directory; empty creates a fresh one under the system
	// temporary directory and removes it after a passing run unless Keep is set.
	Out  string
	Keep bool
	Log  io.Writer

	// PublishTree stops after the directory-mode guard on the export.
	PublishTree bool
	// RequireLists fails the run when the personal-token or retired-name list
	// is not named, since release mode cannot judge the export without them.
	RequireLists bool
	// RequireGitleaks fails the run when gitleaks is not on PATH.
	RequireGitleaks bool

	Verify  []Cmd // in the private tree, development mode
	Install []Cmd // in the export: locked dependency installation
	Guard   func(export string) error
	Build   []Cmd
	Test    []Cmd
	Package func(export string) (PackageResult, error)
}

// Status of one step.
type Status string

const (
	OK      Status = "ok"
	Failed  Status = "FAIL"
	Skipped Status = "skip"
)

// StepResult is the outcome of one step.
type StepResult struct {
	Name    string
	Status  Status
	Detail  string
	Elapsed time.Duration
}

// Report is the outcome of a run.
type Report struct {
	Steps    []StepResult
	Export   string
	Excluded []string
	Metrics  *guard.Metrics
	Package  *PackageResult
	History  guard.HistoryStats
}

// Failed reports whether any step failed.
func (r Report) Failed() bool {
	for _, s := range r.Steps {
		if s.Status == Failed {
			return true
		}
	}
	return false
}

type step struct {
	name string
	// chain steps depend on the export: after one fails the later chain steps
	// are skipped, while independent steps still run.
	chain bool
	// soft steps are skipped like chain steps but, when they fail, leave the
	// later steps to run: a scanner finding says nothing about the build.
	soft bool
	run  func() (string, error)
}

// Run executes the pipeline and returns its report. The error is non-nil only
// when the pipeline could not run at all.
func Run(cfg Config) (Report, error) {
	var rep Report
	repo, err := filepath.Abs(cfg.Repo)
	if err != nil {
		return rep, err
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	out := cfg.Out
	removeOut := false
	if out == "" {
		out, err = os.MkdirTemp("", "applysmith-release-")
		if err != nil {
			return rep, err
		}
		removeOut = !cfg.Keep
	}
	if out, err = filepath.Abs(out); err != nil {
		return rep, err
	}
	rep.Export = out

	exportEnv := []string{"APPLYSMITH_GUARD_RELEASE=1", "APPLYSMITH_GUARD_ROOT=" + out}
	// The private tree is judged in development mode whatever the caller's
	// environment says.
	privateEnv := []string{"APPLYSMITH_GUARD_RELEASE=0", "APPLYSMITH_GUARD_ROOT="}

	runCmds := func(root string, env []string, cmds []Cmd) (string, error) {
		for _, c := range cmds {
			if err := runCmd(filepath.Join(root, c.Dir), env, c.Argv); err != nil {
				return "", fmt.Errorf("%s: %w", c, err)
			}
		}
		return fmt.Sprintf("%d command(s)", len(cmds)), nil
	}

	history := []step{
		{name: "history-scan", run: func() (string, error) {
			found, stats, err := guard.ScanHistory(repo)
			rep.History = stats
			if err != nil {
				return "", err
			}
			detail := fmt.Sprintf("%d commits, %d blobs read, %d skipped", stats.Commits, stats.Blobs, stats.Skipped)
			if len(found) > 0 {
				return detail, findingsError(found)
			}
			return detail, nil
		}},
		{name: "gitleaks-history", run: func() (string, error) {
			return gitleaks(cfg, []string{"git", repo, "--log-opts=--all --full-history", "--redact", "--no-banner"})
		}},
	}
	export := []step{
		{name: "export", chain: true, run: func() (string, error) {
			res, err := Export(repo, out)
			rep.Excluded = res.Excluded
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%d files copied, %d class B paths left out", res.Copied, len(res.Excluded)), nil
		}},
		{name: "install", chain: true, run: func() (string, error) { return runCmds(out, exportEnv, cfg.Install) }},
		{name: "guard-release", chain: true, run: func() (string, error) {
			if cfg.Guard == nil {
				return "", errors.New("no guard configured")
			}
			return "", cfg.Guard(out)
		}},
	}
	checks := []step{
		{name: "gitleaks-export", chain: true, soft: true, run: func() (string, error) {
			return gitleaks(cfg, []string{"dir", out, "--redact", "--no-banner"})
		}},
		{name: "verify-private", run: func() (string, error) { return runCmds(repo, privateEnv, cfg.Verify) }},
		{name: "build-export", chain: true, run: func() (string, error) { return runCmds(out, exportEnv, cfg.Build) }},
		{name: "test-export", chain: true, run: func() (string, error) { return runCmds(out, exportEnv, cfg.Test) }},
		{name: "package-export", chain: true, run: func() (string, error) {
			if cfg.Package == nil {
				return "not configured", nil
			}
			res, err := cfg.Package(out)
			if res.Path != "" {
				rep.Package = &res
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s, %s", res.Path, humanBytes(res.Bytes)), nil
		}},
	}

	// The cheap checks that can reject the run come first; the long build and
	// test steps come last.
	steps := []step{
		{name: "tools", run: func() (string, error) { return checkTools(cfg) }},
		{name: "clean-tree", chain: true, run: func() (string, error) { return "", CheckClean(repo) }},
	}
	if !cfg.PublishTree {
		steps = append(steps, history...)
	}
	steps = append(steps, export...)
	if !cfg.PublishTree {
		steps = append(steps, checks...)
	}

	chainBroken := false
	for _, s := range steps {
		if s.chain && chainBroken {
			rep.Steps = append(rep.Steps, StepResult{Name: s.name, Status: Skipped, Detail: "an earlier export step failed"})
			continue
		}
		fmt.Fprintf(cfg.Log, "-> %s\n", s.name)
		start := time.Now()
		detail, err := s.run()
		res := StepResult{Name: s.name, Status: OK, Detail: detail, Elapsed: time.Since(start)}
		if err != nil {
			res.Status, res.Detail = Failed, err.Error()
			if s.chain && !s.soft {
				chainBroken = true
			}
		}
		rep.Steps = append(rep.Steps, res)
	}

	if tree, err := guard.LoadDirTree(out); err == nil {
		if m, err := guard.Measure(tree); err == nil {
			rep.Metrics = &m
		}
	}
	if removeOut && !rep.Failed() {
		os.RemoveAll(out)
	}
	return rep, nil
}

func checkTools(cfg Config) (string, error) {
	var missing []string
	if cfg.RequireLists {
		for _, v := range []string{"APPLYSMITH_PII_TOKENS", "APPLYSMITH_RETIRED_NAMES"} {
			if os.Getenv(v) == "" {
				missing = append(missing, "$"+v+" (the list is required for release)")
			}
		}
	}
	if cfg.RequireGitleaks {
		if _, err := exec.LookPath("gitleaks"); err != nil {
			missing = append(missing, "gitleaks is not on PATH (release mode requires it)")
		}
	}
	if len(missing) > 0 {
		return "", errors.New(strings.Join(missing, "; "))
	}
	return "", nil
}

func gitleaks(cfg Config, args []string) (string, error) {
	if !cfg.RequireGitleaks {
		return "not required", nil
	}
	scratch, err := os.MkdirTemp("", "applysmith-release-gitleaks-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	config := filepath.Join(scratch, "config.toml")
	if err := os.WriteFile(config, []byte(guard.GitleaksConfig), 0o600); err != nil {
		return "", err
	}
	args = append(args, "--config", config, "--ignore-gitleaks-allow", "--gitleaks-ignore-path", scratch)
	if err := runCmd("", nil, append([]string{"gitleaks"}, args...)); err != nil {
		return "", err
	}
	return "no leaks", nil
}

func findingsError(found []guard.Finding) error {
	var lines []string
	for i, f := range found {
		if i == 10 {
			lines = append(lines, fmt.Sprintf("... and %d more", len(found)-10))
			break
		}
		lines = append(lines, f.String())
	}
	return errors.New(strings.Join(lines, "\n"))
}

// runCmd runs argv in dir with extra environment entries and returns an error
// that carries the tail of the command's output.
func runCmd(dir string, env []string, argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w\n%s", err, summarize(buf.String()))
	}
	return nil
}

// summarize keeps the end of a command's output and, ahead of it, every
// failing test and package line, which a long passing run would otherwise push
// out of view.
func summarize(out string) string {
	var failing []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "--- FAIL") || strings.HasPrefix(l, "FAIL\t") {
			failing = append(failing, strings.TrimSpace(l))
		}
	}
	end := tail(out, 40)
	if len(failing) == 0 {
		return end
	}
	if len(failing) > 30 {
		failing = append(failing[:30], fmt.Sprintf("... and %d more", len(failing)-30))
	}
	return "failing:\n  " + strings.Join(failing, "\n  ") + "\n" + end
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("... %d earlier lines omitted", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

func humanBytes(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }
