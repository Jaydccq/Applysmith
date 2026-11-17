//go:build darwin

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// SuiteSpec is a pinned go test invocation.
type SuiteSpec struct {
	Name string
	Args []string
	Env  []string
}

// TestOutcome is one finished test or subtest.
type TestOutcome struct {
	Package   string
	Test      string
	Result    string // pass, fail or skip
	ElapsedMS float64
}

// SuiteRun is one execution of a suite.
type SuiteRun struct {
	WallS      float64       `json:"wallSeconds"`
	PackageS   float64       `json:"packageSecondsSum"`
	Pass       int           `json:"pass"`
	Fail       int           `json:"fail"`
	Skip       int           `json:"skip"`
	PkgFail    int           `json:"failedPackages"`
	Failures   []Failure     `json:"failures,omitempty"`
	ExitCode   int           `json:"exitCode"`
	LoadStart  float64       `json:"load1Start"`
	LoadEnd    float64       `json:"load1End"`
	LoadMax    float64       `json:"load1Max"`
	Loaded     bool          `json:"loaded"`
	PeakMemory Memory        `json:"peakProcessTree"`
	PeakChrome float64       `json:"peakChromeMB"`
	Outcomes   []TestOutcome `json:"-"`
}

type event struct {
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

// Failure is a failed test or package with the last lines it printed, so a
// failure under load can be told from a failure of the product.
type Failure struct {
	Package string   `json:"package"`
	Test    string   `json:"test,omitempty"`
	Tail    []string `json:"tail"`
}

const tailLines = 12

func keepTail(lines []string, out string) []string {
	out = strings.TrimRight(out, "\n")
	if out == "" || strings.HasPrefix(out, "=== ") {
		return lines
	}
	if len(out) > 200 {
		out = out[:200]
	}
	lines = append(lines, out)
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return lines
}

// parseEvents folds `go test -json` output into one outcome per test and
// subtest, the sum of the packages' own elapsed seconds, and the failures with
// their last output lines (a package that failed as a whole, by a build error
// or a timeout, has an entry without a test name). A line that is not an event
// is skipped.
func parseEvents(r io.Reader) (outs []TestOutcome, packageS float64, fails []Failure) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	tails := map[string][]string{}
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		key := e.Package + "\x00" + e.Test
		switch e.Action {
		case "output":
			tails[key] = keepTail(tails[key], e.Output)
		case "pass", "skip":
			delete(tails, key)
			if e.Test == "" {
				packageS += e.Elapsed
				continue
			}
			outs = append(outs, TestOutcome{Package: e.Package, Test: e.Test, Result: e.Action, ElapsedMS: e.Elapsed * 1000})
		case "fail":
			fails = append(fails, Failure{Package: e.Package, Test: e.Test, Tail: tails[key]})
			delete(tails, key)
			if e.Test == "" {
				packageS += e.Elapsed
				continue
			}
			outs = append(outs, TestOutcome{Package: e.Package, Test: e.Test, Result: e.Action, ElapsedMS: e.Elapsed * 1000})
		}
	}
	return outs, packageS, fails
}

// runSuite runs one suite once in its own process group and samples the whole
// tree below it (the test binaries and every Chrome they start) every second.
// The one-minute load average is sampled with it.
func runSuite(ctx context.Context, repo string, spec SuiteSpec, load Load) (SuiteRun, error) {
	var run SuiteRun
	run.LoadStart, run.Loaded = load.Settle(ctx)
	cmd := exec.Command("go", append([]string{"test", "-count=1", "-json"}, spec.Args...)...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return run, err
	}
	begin := time.Now()
	if err := cmd.Start(); err != nil {
		return run, err
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
			}
			tree := descendants(processTable(), pid)
			m := memoryOf(tree)
			run.PeakMemory.peak(m)
			if c := m.ByKindMB["chrome"]; c > run.PeakChrome {
				run.PeakChrome = c
			}
			if l := load1(); l > run.LoadMax {
				run.LoadMax = l
			}
		}
	}()
	go func() {
		<-ctx.Done()
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}()
	run.Outcomes, run.PackageS, run.Failures = parseEvents(out)
	werr := cmd.Wait()
	close(done)
	<-sampled
	run.WallS = time.Since(begin).Seconds()
	run.LoadEnd = load1()
	if ee, ok := werr.(*exec.ExitError); ok {
		run.ExitCode = ee.ExitCode()
	}
	for _, f := range run.Failures {
		if f.Test == "" {
			run.PkgFail++
		}
	}
	for _, o := range run.Outcomes {
		if strings.Contains(o.Test, "/") {
			continue // subtests are counted with their own rates, not in the totals
		}
		switch o.Result {
		case "pass":
			run.Pass++
		case "fail":
			run.Fail++
		case "skip":
			run.Skip++
		}
	}
	return run, nil
}
