// Clean-room: docs/provenance.md#CR-DOCTOR

// Package onboarding holds the setup flow and the doctor. This file is the
// doctor framework: a registry of leaf checks, a runner with per-check
// timeouts, and the report with its exit code. Capabilities contribute leaves
// by adding entries to the registry in checks.go or their own file.
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Status is the outcome of one check.
type Status string

const (
	Pass    Status = "pass"
	Warn    Status = "warn"
	Fail    Status = "fail"
	Skipped Status = "skipped"
)

// Exit codes of `applysmith doctor`.
const (
	ExitOK     = 0
	ExitFailed = 1
	ExitUnable = 4
)

const (
	leafTimeout     = 10 * time.Second
	liveLeafTimeout = 30 * time.Second
	// browserBudget is shared by the two leaves that open the browser windows.
	browserBudget = 45 * time.Second

	// PlatformStatement opens the human-readable report.
	PlatformStatement = "Applysmith supports macOS 13 or later. Other operating systems are unverified."
)

// Outcome is what a check function reports.
type Outcome struct {
	Status  Status
	Summary string
	Fix     string // one next action, the exact command when there is one
}

// Check is one leaf of the doctor. ID has the form <group>.<name>, where the
// group is a setup step id or "runtime".
type Check struct {
	ID       string
	Required bool
	// Budget replaces the leaf timeout for a check that drives a browser.
	Budget time.Duration
	Run    func(ctx context.Context, env *Env) Outcome
}

// Group is the part of the ID before the dot.
func (c Check) Group() string { return strings.SplitN(c.ID, ".", 2)[0] }

// Result is a check's outcome with its identity. A failing leaf that is not
// required is reported as a warning.
type Result struct {
	ID       string `json:"id"`
	Status   Status `json:"status"`
	Required bool   `json:"required"`
	Summary  string `json:"summary"`
	Fix      string `json:"fix,omitempty"`
	// Step is the setup step that fixes the check, when its group is one. Fix
	// is worded for the command line; the dashboard links to this step instead.
	Step string `json:"step,omitempty"`
}

// Report is the outcome of a doctor run.
type Report struct {
	Results []Result `json:"results"`
}

// ExitCode is 1 when a required check failed, 0 otherwise.
func (r Report) ExitCode() int {
	for _, res := range r.Results {
		if res.Status == Fail && res.Required {
			return ExitFailed
		}
	}
	return ExitOK
}

// Options selects and bounds a run.
type Options struct {
	// Only is a group ("workspace") or a leaf ("workspace.files"); empty runs all.
	Only string
	// Live permits checks that use the network and lengthens the leaf timeout.
	Live bool
	// Timeout overrides the leaf timeout (tests).
	Timeout time.Duration
}

// ErrUnknownCheck is returned when Only names no check.
var ErrUnknownCheck = errors.New("onboarding: unknown check")

// Select returns the checks an Only filter picks, in registry order.
func Select(checks []Check, only string) ([]Check, error) {
	if only == "" {
		return checks, nil
	}
	var out []Check
	for _, c := range checks {
		if c.ID == only || c.Group() == only {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w %q", ErrUnknownCheck, only)
	}
	return out, nil
}

// Run executes the selected checks at once and reports them in registry
// order. Each works on its own copy of the environment; most wait on a child
// process or the keychain, so together they take about as long as the slowest
// (the home page runs them on every visit, docs/architecture/performance.md).
// A check that panics or outlives its timeout becomes a failure, never a hang.
func Run(ctx context.Context, checks []Check, env *Env, opts Options) (Report, error) {
	picked, err := Select(checks, opts.Only)
	if err != nil {
		return Report{}, err
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = leafTimeout
		if opts.Live {
			timeout = liveLeafTimeout
		}
	}
	env.Live = opts.Live
	if opts.Live && env.BrowserCheck != nil {
		probe := env.BrowserCheck
		env.liveBrowser = sync.OnceValues(func() (BrowserReport, error) {
			bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), browserBudget)
			defer cancel()
			return probe(bctx)
		})
	}
	rep := Report{Results: make([]Result, len(picked))}
	var wg sync.WaitGroup
	for i, c := range picked {
		if skipped(ctx, c, env) {
			rep.Results[i] = Result{ID: c.ID, Status: Skipped, Required: false, Summary: "skipped by the user"}
			continue
		}
		wg.Go(func() { rep.Results[i] = runOne(ctx, c, env, timeout) })
	}
	wg.Wait()
	return rep, ctx.Err()
}

// skipped reports whether the user skipped the optional step a leaf belongs to.
func skipped(ctx context.Context, c Check, env *Env) bool {
	st, ok := StepByID(c.Group())
	if !ok || st.Required {
		return false
	}
	_, skip := env.decision(ctx, skipKey(st.ID))
	return skip
}

func runOne(ctx context.Context, c Check, env *Env, timeout time.Duration) Result {
	if c.Budget > 0 {
		timeout = c.Budget
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan Outcome, 1)
	// A check that outlives its timeout keeps running; it works on its own
	// copy, so a late write cannot change what the next check sees.
	own := *env
	env = &own
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- Outcome{Status: Fail, Summary: "the check crashed", Fix: crashFix(env)}
			}
		}()
		done <- c.Run(cctx, env)
	}()
	var out Outcome
	select {
	case out = <-done:
	case <-cctx.Done():
		out = Outcome{Status: Fail, Summary: fmt.Sprintf("did not finish within %s", timeout), Fix: rerunFix(env, c.ID)}
	}
	if out.Status == Fail && !c.Required {
		out.Status = Warn
	}
	res := Result{ID: c.ID, Status: out.Status, Required: c.Required, Summary: out.Summary, Fix: out.Fix}
	if _, ok := StepByID(c.Group()); ok {
		res.Step = c.Group()
	}
	return res
}

// rerunFix and crashFix are the doctor's own next actions. The dashboard
// (Env.InServer) runs the checks with its own button and shows the versions in
// Settings, Advanced, so only a terminal reader is given the commands.
func rerunFix(env *Env, id string) string {
	if env.InServer {
		return "run the checks again"
	}
	return "run `applysmith doctor --only " + id + "` again"
}

func crashFix(env *Env) string {
	if env.InServer {
		return "report this with the versions shown in Settings, Advanced"
	}
	return "report this with the output of `applysmith version`"
}

// WriteText prints the platform statement, one line per check, the fix
// under each check that did not pass and a closing count.
func WriteText(w io.Writer, rep Report) {
	fmt.Fprintln(w, PlatformStatement)
	fmt.Fprintln(w)
	width := 0
	for _, r := range rep.Results {
		width = max(width, len(r.ID))
	}
	failed, warned := 0, 0
	for _, r := range rep.Results {
		tag := strings.ToUpper(string(r.Status))
		switch {
		case r.Status == Fail && r.Required:
			failed++
		case r.Status == Fail || r.Status == Warn:
			warned++
		}
		fmt.Fprintf(w, "%-9s %-*s  %s\n", "["+tag+"]", width, r.ID, r.Summary)
		if r.Fix != "" && r.Status != Pass {
			fmt.Fprintf(w, "          fix: %s\n", r.Fix)
		}
	}
	if failed > 0 {
		fmt.Fprintf(w, "\nFAIL %d, WARN %d. Fix the FAIL lines first, then run `applysmith doctor` again.\n", failed, warned)
	} else {
		fmt.Fprintf(w, "\nEvery required check passed; WARN %d (optional setup).\n", warned)
	}
}
