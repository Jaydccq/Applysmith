//go:build darwin

package main

import (
	"fmt"
	"strings"
)

// baselineRow compares one metric with what the frozen old system recorded
// about itself. Old holds only figures that appear in the old repository's own
// documents at the frozen commit; where none is comparable it says so and
// names the closest recorded figure only as context.
type baselineRow struct {
	Metric     string
	Old        string
	Source     string
	Comparable string
	New        func(Results) string
}

func startupOf(r Results, name string) (StartResult, bool) {
	for _, s := range r.Startup {
		if s.Target == name {
			return s, true
		}
	}
	return StartResult{}, false
}

func newStartup(r Results) string {
	s, ok := startupOf(r, "Applysmith.app")
	if !ok {
		return "not measured in this run"
	}
	return fmt.Sprintf("cold %s ms, warm %s ms (medians, empty workspace)", ms(s.ColdMS.Median), ms(s.WarmMS.Median))
}

func newMemory(r Results) string {
	s, ok := startupOf(r, "Applysmith.app")
	if !ok {
		return "not measured in this run"
	}
	return fmt.Sprintf("%.0f MB idle, app and WebView helpers (median, warm)", s.WarmRSS.Median)
}

func newChrome(r Results) string {
	for _, s := range r.Suites {
		if s.Name == "lane-chrome" && s.Chrome.N > 0 {
			return fmt.Sprintf("%.0f MB, two lane tabs on a fixture page (peak, median of %d runs, headless)", s.Chrome.Median, s.Chrome.N)
		}
	}
	return "not measured in this run"
}

func newRates(Results) string { return "see the workload table above" }

var baseline = []baselineRow{
	{"Disk image", "`Applysmith-1.3.0-arm64.dmg`: 160 MB (2026-07-11 record); 178 MB (2026-08-07 record)",
		"docs/exec-plans/active/2026-07-11-operator-projection-cutover-1-apply-next.md; 2026-08-07-desktop-package-fsevents-bundle-failure.md",
		"Yes: the same kind of artifact (the arm64 disk image of the desktop app). Built with different tools on different dates.",
		func(r Results) string { return mb(r.Sizes.DiskImage) }},
	{"Startup to a served dashboard", "none. The old documents record `/v1/health` latency (250 ms before and 1.7 ms after a cache, on a real data set), watchdog boots of 25 to 343 s on a starved host, and a 60 s Chrome connect timeout, but no cold or warm time to a served dashboard on a clean workspace.",
		"docs/exec-plans/active/2026-09-08-health-probe-startup-contention.md; docs/runbooks/perf-debugging.md",
		"No comparable baseline.", newStartup},
	{"Resident memory of the idle app", "Electron alone: 363 MB idle, 1,034 MB peak during an Indeed scan, 446 MB after with no tabs, in a harness with the in-app browser. The bridge process: 94 to 275 MB on a host holding a 268 MB database.",
		"docs/exec-plans/active/2026-09-30-in-app-browser.md; 2026-09-16-scale-bridge-data-layer.md",
		"No comparable baseline: the old figures exclude the bridge or exclude the shell, were taken with live data and a live scan, and are not a whole-tree reading of an idle app on an empty workspace.", newMemory},
	{"Scan or apply Chrome", "479 MB after a clean headless restart; 3.6 to 4.0 GB and 7.0 GB when renderers were not reaped. The architecture document budgeted 200 to 400 MB.",
		"docs/exec-plans/active/2026-07-27-schedule-plan-live-verification.md; 2026-07-20-linkedin-newgrad-deepseek-rerun.md",
		"No comparable baseline: live LinkedIn and ATS sessions against fixture pages.", newChrome},
	{"Synthetic scan and fill completion", "none. The old documents record live runs (for example 88 rows, a fill of 7 of 7 fields in 12.5 s on one live form), not completion rates over fixture boards.",
		"docs/exec-plans/active/2026-07-27-schedule-plan-live-verification.md; 2026-09-30-in-app-browser.md",
		"No comparable baseline.", newRates},
	{"Test-suite duration", "none. The full-verify wall clock was recorded as not measured.",
		"docs/exec-plans/active/2026-07-28-bound-verify-concurrency.md",
		"No comparable baseline.", func(r Results) string {
			var p []string
			for _, s := range r.Suites {
				p = append(p, fmt.Sprintf("%s %.0f s", s.Name, s.WallS.Median))
			}
			return strings.Join(p, ", ") + " (median)"
		}},
}

func renderBaseline(r Results) string {
	var b strings.Builder
	b.WriteString("| Metric | Old system, as it recorded itself | Source at commit `c0cb9598b` | Comparable? | This system |\n|---|---|---|---|---|\n")
	for _, row := range baseline {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", row.Metric, row.Old, row.Source, row.Comparable, row.New(r))
	}
	return b.String()
}
