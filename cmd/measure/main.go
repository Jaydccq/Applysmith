//go:build darwin

// Command measure records startup, process-tree memory, synthetic scan and fill
// completion, artifact sizes and test durations on the machine it runs on, and
// writes them to a machine-readable file and into the measured block of a page.
// It uses only synthetic workspaces under a temporary directory, the existing
// fixture boards and mock sites of the test suites, and loopback; it starts
// nothing that it does not stop. `make measure` builds the artifacts first.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const chromeBinary = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

func main() {
	repo := flag.String("repo", ".", "the repository to measure")
	bundle := flag.String("bundle", "app/build/darwin-arm64/Applysmith.app", "packaged app bundle")
	dmg := flag.String("dmg", "app/build/darwin-arm64/Applysmith 0.0.1 arm64.dmg", "disk image")
	cli := flag.String("cli", "bin/applysmith", "the applysmith command built with `make build`")
	samples := flag.Int("samples", 7, "startup samples per case")
	runs := flag.Int("runs", 3, "runs of each test suite")
	settle := flag.Duration("settle", 4*time.Second, "wait after the dashboard is served before memory is read")
	maxLoad := flag.Float64("max-load", float64(runtime.NumCPU()), "one-minute load average at or below which a sample is quiet")
	loadWait := flag.Duration("load-wait", 5*time.Minute, "longest wait for the load to fall before a sample is taken anyway and marked")
	jsonOut := flag.String("json", "docs/architecture/performance.json", "machine-readable results")
	page := flag.String("page", "docs/architecture/performance.md", "page whose measured block is rewritten")
	only := flag.String("only", "sizes,startup,suites", "sections to run")
	suites := flag.String("suites", "unit,browser,lane-chrome", "test suites to run when the suites section runs")
	rerender := flag.Bool("render-only", false, "rewrite the page from the existing -json file without measuring")
	flag.Parse()

	if *rerender {
		if err := renderOnly(*repo, *jsonOut, *page); err != nil {
			fmt.Fprintln(os.Stderr, "measure:", err)
			os.Exit(1)
		}
		return
	}

	if err := measureAll(*repo, *bundle, *dmg, *cli, *samples, *runs, *settle, Load{Max: *maxLoad, Wait: *loadWait}, *jsonOut, *page, *only, *suites); err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		os.Exit(1)
	}
}

func measureAll(repo, bundle, dmg, cli string, samples, runs int, settle time.Duration, load Load, jsonOut, page, only, suiteNames string) error {
	if runs < 1 {
		return fmt.Errorf("-runs %d: each suite needs at least one run", runs)
	}
	repo, _ = filepath.Abs(repo)
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(repo, p)
	}
	bundle, dmg, cli = abs(bundle), abs(dmg), abs(cli)
	if free := freeGB(repo); free < 4 {
		return fmt.Errorf("only %.1f GB free; stop below 4 GB", free)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	want := map[string]bool{}
	for _, s := range strings.Split(only, ",") {
		want[strings.TrimSpace(s)] = true
	}
	r := Results{Schema: 1, Generated: time.Now().UTC().Format(time.RFC3339), Env: readEnvironment(repo, chromeBinary), LoadStart: load1(),
		Params: Params{StartSamples: samples, SuiteRuns: runs, SettleS: settle.Seconds(), MaxLoad: load.Max, LoadWaitS: load.Wait.Seconds()}}
	baseline := map[int]bool{}
	for _, p := range processTable() {
		baseline[p.PID] = true
	}

	if want["sizes"] {
		r.Sizes = measureSizes(repo, bundle, dmg, cli)
	}
	if want["startup"] {
		targets := []Target{
			{Name: "Applysmith.app", Artifact: bundle, WebView: true,
				Exec: func(c string) string { return filepath.Join(c, "Contents", "MacOS", "Applysmith") },
				Args: func(string) []string { return []string{"--mygo-opened-at-login"} }},
			{Name: "applysmith serve", Artifact: cli,
				Exec: func(c string) string { return c },
				Args: func(ws string) []string { return []string{"serve", "--workspace", ws, "--port", "0"} }},
		}
		for _, t := range targets {
			logf("startup: %s, %d cold and %d warm samples", t.Name, samples, samples)
			res, err := measureStartup(ctx, t, samples, settle, load)
			if err != nil {
				return fmt.Errorf("startup of %s: %w", t.Name, err)
			}
			r.Startup = append(r.Startup, res)
		}
	}
	if want["suites"] {
		specs := []SuiteSpec{
			{"unit", []string{"-tags", "dashboard", "./..."}, nil},
			{"browser", []string{"-tags", "browser", "-p", "1", "-timeout", "25m", "./internal/browser/...", "./internal/engine/...", "./internal/extension/...", "./internal/apply/...", "./internal/discovery/..."}, nil},
			{"lane-chrome", []string{"-tags", "browser", "-run", "^TestMeasure_LaneChromeHeld$", "./internal/browser/"}, []string{"APPLYSMITH_MEASURE_HOLD=10s"}},
		}
		byName := map[string][]SuiteRun{}
		for _, sp := range specs {
			if !strings.Contains(","+suiteNames+",", ","+sp.Name+",") {
				continue
			}
			for i := 0; i < runs; i++ {
				logf("suite %s, run %d of %d", sp.Name, i+1, runs)
				run, err := runSuite(ctx, repo, sp, load)
				if err != nil {
					return err
				}
				logf("suite %s: %.0f s, %d passed, %d failed, %d skipped, load max %.1f", sp.Name, run.WallS, run.Pass, run.Fail, run.Skip, run.LoadMax)
				byName[sp.Name] = append(byName[sp.Name], run)
			}
			r.Suites = append(r.Suites, summarizeSuite(sp.Name, "go test -tags "+sp.Args[1]+" "+strings.Join(sp.Args[2:], " "), byName[sp.Name]))
		}
		if len(byName["unit"]) > 0 && len(byName["browser"]) > 0 {
			r.Workloads = workloads(byName["unit"], byName["browser"])
		}
	}
	r.LoadEnd = load1()
	r.Orphans = leftovers(baseline)
	if err := writeJSON(abs(jsonOut), r); err != nil {
		return err
	}
	return writePage(abs(page), r)
}

func writePage(page string, r Results) error {
	raw, err := os.ReadFile(page)
	if err != nil {
		return err
	}
	updated, err := replaceBlock(string(raw), render(r))
	if err != nil {
		return err
	}
	return os.WriteFile(page, []byte(updated), 0o644)
}

// renderOnly rewrites the page from a results file, for a change to the tables.
func renderOnly(repo, jsonPath, page string) error {
	repo, _ = filepath.Abs(repo)
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(repo, p)
	}
	raw, err := os.ReadFile(abs(jsonPath))
	if err != nil {
		return err
	}
	var r Results
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	return writePage(abs(page), r)
}

// leftovers lists processes that this run could have started and that are
// still running: test Chromes with a throwaway profile and processes running
// out of the harness's scratch directories.
func leftovers(baseline map[int]bool) []string {
	var out []string
	for _, p := range processTable() {
		if baseline[p.PID] {
			continue
		}
		if strings.Contains(p.Command, "applysmith-measure-") || (strings.Contains(p.Command, "Google Chrome") && strings.Contains(p.Command, "--user-data-dir=") && strings.Contains(p.Command, "applysmith-") && p.PPID == 1) {
			out = append(out, fmt.Sprintf("pid %d", p.PID))
		}
	}
	return out
}

func freeGB(path string) float64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0
	}
	return float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
}
