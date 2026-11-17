//go:build darwin

package main

import (
	"fmt"
	"strings"
)

func mb(b int64) string { return fmt.Sprintf("%.1f MB (%s bytes)", float64(b)/1e6, commas(b)) }

func commas(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func ms(v float64) string { return fmt.Sprintf("%.0f", v) }

// render writes the measured tables of the page.
func render(r Results) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	e := r.Env
	w("Measured at commit `%s`%s on %s (%d cores, %d GB), macOS %s, Darwin %s.", e.Commit, map[bool]string{true: " plus uncommitted changes", false: ""}[e.Dirty], e.Machine, e.CPUs, e.MemoryGB, e.MacOS, e.Kernel)
	w("")
	w("| Tool | Version |")
	w("|---|---|")
	w("| Go | %s |", e.Go)
	w("| bun | %s |", e.Bun)
	w("| Chrome | %s |", e.Chrome)
	w("| mygo | %s |", e.Mygo)
	w("")
	w("One-minute load average at the start and end of the run: %.1f and %.1f (limit for a quiet sample: %.1f, %d cores).", r.LoadStart, r.LoadEnd, r.Params.MaxLoad, e.CPUs)
	w("")

	w("### Startup to a served dashboard")
	w("")
	w("Time from process start until the lock names the process, `/api/health` answers `ok`, the document address answers the sessionless landing page (401) and the dashboard's index document comes back with its root element. The one-time launch code is never redeemed, because the app's own window needs it. The app is started with the login-item switch, so its window stays hidden. %d samples per case; median and 90th percentile (nearest rank) in milliseconds. Cold is a fresh copy of the artifact and a new workspace, warm is the same workspace and copy launched again.", r.Params.StartSamples)
	w("")
	w("| Target | Cold median | Cold p90 | Warm median | Warm p90 | Samples above the load limit |")
	w("|---|---|---|---|---|---|")
	for _, s := range r.Startup {
		w("| %s | %s | %s | %s | %s | %d of %d |", s.Target, ms(s.ColdMS.Median), ms(s.ColdMS.P90), ms(s.WarmMS.Median), ms(s.WarmMS.P90), s.Loaded, len(s.Cold)+len(s.Warm))
	}
	w("")

	w("### Resident memory of the whole process tree")
	w("")
	w("Summed `ps` RSS of the process and everything below it, read %.0f s after the dashboard was served, in MB. The app's WebKit helpers (GPU, networking, page content) are children of launchd, so they are found by being new since the launch, accountable to the same process, and gone after the app quits. Pages shared between processes are counted once per process, so a sum is an upper bound.", r.Params.SettleS)
	w("")
	w("| Target | Cold median | Cold p90 | Warm median | Warm p90 | Warm median, by kind |")
	w("|---|---|---|---|---|---|")
	for _, s := range r.Startup {
		w("| %s | %.0f | %.0f | %.0f | %.0f | %s |", s.Target, s.ColdRSS.Median, s.ColdRSS.P90, s.WarmRSS.Median, s.WarmRSS.P90, kinds(s.Breakdown))
	}
	w("")
	for _, s := range r.Suites {
		switch s.Name {
		case "lane-chrome":
			w("Chrome with one apply-lane tab and one scan-lane tab open on a fixture page (headless, a throwaway profile, held 10 s): the largest sum of all Chrome processes at any one-second sample was median %.0f MB, p90 %.0f MB, max %.0f MB over %d runs.", s.Chrome.Median, s.Chrome.P90, s.Chrome.Max, s.Chrome.N)
		case "browser":
			w("Chrome while the whole browser-tagged suite runs (up to three mock-board scenarios at once, each in its own tabs): the largest sum was median %.0f MB, p90 %.0f MB, max %.0f MB over %d runs; the whole `go test` tree peaked at median %.0f MB, max %.0f MB.", s.Chrome.Median, s.Chrome.P90, s.Chrome.Max, s.Chrome.N, s.PeakTree.Median, s.PeakTree.Max)
		}
		w("")
	}

	w("### Synthetic scan and fill workloads")
	w("")
	w("Each scenario is an existing test that serves a fixture board or a mock applicant-tracking board on loopback. A scenario completes when its own assertions pass; the executions are the scenarios times the browser-suite or unit-suite runs, skipped scenarios excluded.")
	w("")
	w("| Workload | Scenarios | Executions | Completed | Failed | Skipped | Completion | Failure | Scenario time median / p90 (ms) |")
	w("|---|---|---|---|---|---|---|---|---|")
	for _, x := range r.Workloads {
		w("| %s | %d | %d | %d | %d | %d | %.1f%% | %.1f%% | %s / %s |", x.Name, x.Count, x.Executions, x.Pass, x.Fail, x.Skip, x.CompletePct, x.FailPct, ms(x.ElapsedMS.Median), ms(x.ElapsedMS.P90))
	}
	w("")
	for _, x := range r.Workloads {
		if len(x.Failed) > 0 {
			w("Failed in %s: %s.", x.Name, strings.Join(x.Failed, ", "))
		}
	}
	w("")

	w("### Artifact sizes")
	w("")
	s := r.Sizes
	w("| Artifact | Size |")
	w("|---|---|")
	w("| `Applysmith.app` (sum of file bytes) | %s |", mb(s.AppBundle))
	w("| `Contents/MacOS/Applysmith` | %s |", mb(s.AppBinary))
	w("| disk image | %s |", mb(s.DiskImage))
	w("| `applysmith` command (`make build`, not stripped) | %s |", mb(s.CLIBinary))
	w("| embedded engine bundle (`internal/engine/generated`) | %s |", mb(s.EngineBundle))
	w("| embedded extension build (`internal/extension/generated`) | %s |", mb(s.ExtensionBuilt))
	w("| embedded dashboard assets (`web/dist`) | %s |", mb(s.DashboardAsset))
	w("")

	w("### Comparison with the frozen old system")
	w("")
	b.WriteString(renderBaseline(r))
	w("")
	w("### Test-suite durations")
	w("")
	w("Wall-clock seconds of the whole command, %d runs, with the largest one-minute load average seen during a run.", r.Params.SuiteRuns)
	w("")
	w("| Suite | Command | Fastest | Median | p90 | Slowest | Max load | Scenarios passed / failed / skipped (last run) | Failed packages |")
	w("|---|---|---|---|---|---|---|---|---|")
	for _, s := range r.Suites {
		last := s.Runs[len(s.Runs)-1]
		var maxLoad float64
		for _, x := range s.Runs {
			if x.LoadMax > maxLoad {
				maxLoad = x.LoadMax
			}
		}
		w("| %s | `%s` | %.0f | %.0f | %.0f | %.0f | %.1f | %d / %d / %d | %d |", s.Name, s.Command, s.WallS.Min, s.WallS.Median, s.WallS.P90, s.WallS.Max, maxLoad, last.Pass, last.Fail, last.Skip, last.PkgFail)
	}
	w("")
	for _, s := range r.Suites {
		var parts []string
		for i, x := range s.Runs {
			parts = append(parts, fmt.Sprintf("%d: %.0f s, %d passed, %d failed, load %.1f at the start%s and up to %.1f during", i+1, x.WallS, x.Pass, x.Fail, x.LoadStart, map[bool]string{true: " (above the limit)", false: ""}[x.Loaded], x.LoadMax))
		}
		w("Runs of %s: %s.", s.Name, strings.Join(parts, "; "))
		w("")
		for i, x := range s.Runs {
			for _, f := range x.Failures {
				tail := f.Tail
				if len(tail) > 3 {
					tail = tail[len(tail)-3:]
				}
				name := f.Test
				if name == "" {
					name = "the whole package"
				}
				w("- %s run %d, %s, %s: `%s`", s.Name, i+1, f.Package, name, strings.ReplaceAll(strings.Join(tail, " | "), "`", "'"))
			}
		}
		w("")
	}
	if len(r.Orphans) == 0 {
		w("After the run no test Chrome and no app process started by it was left running.")
	} else {
		w("Left running after the run: %s.", strings.Join(r.Orphans, "; "))
	}
	return b.String()
}

func kinds(m Memory) string {
	var parts []string
	for _, k := range []string{"app", "webview", "chrome", "test-binary"} {
		if v, ok := m.ByKindMB[k]; ok {
			parts = append(parts, fmt.Sprintf("%s %.0f", k, v))
		}
	}
	return strings.Join(parts, ", ") + fmt.Sprintf(" (%d process%s)", m.Processes, map[bool]string{true: "", false: "es"}[m.Processes == 1])
}
