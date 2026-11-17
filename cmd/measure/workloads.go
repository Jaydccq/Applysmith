//go:build darwin

package main

import (
	"sort"
	"strings"
)

// Workload is a pinned set of the existing synthetic scenarios: the mock
// applicant-tracking boards and the fixture job boards the test suites serve.
type Workload struct {
	Name        string   `json:"name"`
	What        string   `json:"what"`
	Tests       []string `json:"tests"`
	Count       int      `json:"count"`
	Executions  int      `json:"executions"`
	Pass        int      `json:"pass"`
	Fail        int      `json:"fail"`
	Skip        int      `json:"skip"`
	CompletePct float64  `json:"completionPercent"`
	FailPct     float64  `json:"failurePercent"`
	Failed      []string `json:"failedScenarios"`
	// ElapsedMS summarises how long one scenario ran, per scenario and run.
	ElapsedMS Summary `json:"scenarioElapsedMS"`
}

const (
	applyPkg     = "applysmith/internal/apply"
	discoveryPkg = "applysmith/internal/discovery"

	fillSuccess = "TestE2E_Apply_DryRunThenSubmitSucceedsWithExactlyOnePress/"
	fillRefusal = "TestE2E_Apply_ServiceRefusalStopsAsDefectAndLeavesNoSubmission/"
)

// scanBoardPrefixes name the unit tests that serve fixture job boards over
// HTTP: the sweep of registry boards and the board-backed sources.
var scanBoardPrefixes = []string{"TestSweep_", "TestRegistrySweep_", "TestHTTPSources_", "TestSources_", "TestAmazon_", "TestA16Z_"}

func topLevel(runs []SuiteRun, pkg string) map[string]bool {
	set := map[string]bool{}
	for _, r := range runs {
		for _, o := range r.Outcomes {
			if o.Package == pkg && !strings.Contains(o.Test, "/") {
				set[o.Test] = true
			}
		}
	}
	return set
}

// workloads derives the fill and scan workloads from the unit and browser
// runs. The mock-ATS suite is the apply tests that exist only under the
// browser tag, and the Chrome fixture boards are the discovery tests that do.
func workloads(unit, browser []SuiteRun) []Workload {
	unitApply, unitDisc := topLevel(unit, applyPkg), topLevel(unit, discoveryPkg)
	pick := func(runs []SuiteRun, pkg string, keep func(string) bool) (names []string, outs []TestOutcome) {
		seen := map[string]bool{}
		for _, r := range runs {
			for _, o := range r.Outcomes {
				if o.Package == pkg && keep(o.Test) {
					outs = append(outs, o)
					seen[o.Test] = true
				}
			}
		}
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		return names, outs
	}
	flat := func(test string) bool { return !strings.Contains(test, "/") }
	defs := []struct {
		name, what string
		runs       []SuiteRun
		pkg        string
		keep       func(string) bool
	}{
		{"fill: mock-ATS board, dry run then submit", "One scenario per mock board (Greenhouse, Lever, Ashby, Workday, LinkedIn): fill without pressing, then submit, with one press, a confirmed ledger row and a duplicate refused.",
			browser, applyPkg, func(t string) bool { return strings.HasPrefix(t, fillSuccess) }},
		{"fill: mock-ATS board, service refusal", "The same five boards answering 422: the run must stop as a defect with nothing recorded, then succeed once the board accepts.",
			browser, applyPkg, func(t string) bool { return strings.HasPrefix(t, fillRefusal) }},
		{"fill: whole mock-ATS suite", "Every top-level apply test that exists only under the browser tag: the mock boards, captcha and account flows, hand-off, resume, list and process-death cases.",
			browser, applyPkg, func(t string) bool { return flat(t) && !unitApply[t] }},
		{"scan: fixture boards served over HTTP", "The registry sweep and the board-backed sources against local fixture boards, with the failure cases (rate limit, offline host, layout change, retries).",
			unit, discoveryPkg, func(t string) bool {
				for _, p := range scanBoardPrefixes {
					if strings.HasPrefix(t, p) {
						return flat(t)
					}
				}
				return false
			}},
		{"scan: fixture boards read in headless Chrome", "One scenario per browser-read source (LinkedIn jobs and feed, Indeed, ZipRecruiter, Google Jobs, Glassdoor, new-grad feed, Jobright) on a local fixture page.",
			browser, discoveryPkg, func(t string) bool { return flat(t) && !unitDisc[t] }},
	}
	var out []Workload
	for _, d := range defs {
		names, outs := pick(d.runs, d.pkg, d.keep)
		out = append(out, rate(d.name, d.what, names, outs))
	}
	return out
}

func rate(name, what string, names []string, outs []TestOutcome) Workload {
	w := Workload{Name: name, What: what, Tests: names, Count: len(names)}
	failed := map[string]bool{}
	var ms []float64
	for _, o := range outs {
		switch o.Result {
		case "pass":
			w.Pass++
			ms = append(ms, o.ElapsedMS)
		case "fail":
			w.Fail++
			failed[o.Test] = true
			ms = append(ms, o.ElapsedMS)
		default:
			w.Skip++
		}
	}
	w.Executions = w.Pass + w.Fail
	if w.Executions > 0 {
		w.CompletePct = 100 * float64(w.Pass) / float64(w.Executions)
		w.FailPct = 100 * float64(w.Fail) / float64(w.Executions)
	}
	w.Failed = []string{}
	for n := range failed {
		w.Failed = append(w.Failed, n)
	}
	sort.Strings(w.Failed)
	w.ElapsedMS = summarize(ms)
	return w
}
