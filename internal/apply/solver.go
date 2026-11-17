package apply

import (
	"log/slog"
	"strings"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/gates"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// SolverSource is where the per-run solver finds what lives outside the chain.
type SolverSource struct {
	// Vault holds the key; the policy file holds the switch; Profile gives the
	// batch skip list.
	Vault     *vault.Vault
	Workspace *workspace.Workspace
	Profile   func() (workspace.Profile, bool)
	Log       *slog.Logger
	Now       func() time.Time
}

// SolverFactory builds the run's captcha session for PageDeps.Solver. The key
// is read from the vault only, an unreadable policy reads as off, and an
// unreadable profile leaves the skip verdict unreadable; the gates inside the
// session do the rest.
func SolverFactory(src SolverSource) func(*Exec, func(gates.Decision)) captcha.Solver {
	return func(e *Exec, audit func(gates.Decision)) captcha.Solver {
		pol, rep := src.Workspace.LoadPolicy()
		sw := gates.SolverOff
		if rep.OK() && pol.CaptchaSolver == "on" {
			sw = gates.SolverOn
		}
		return captcha.New(captcha.Config{
			Demo: src.Workspace.IsDemo(), Run: solveRunKind(e), Spent: e.spent, Switch: sw, Audit: audit, Log: src.Log, Now: src.Now,
			Key:  func() string { k, _ := src.Vault.Get("twocaptcha-key"); return k },
			Skip: func() gates.SkipStatus { return skipStatus(e, src.Profile) },
		})
	}
}

// skipStatus is the batch skip list's verdict on the run's company, read when
// the solve is asked: the bound report's company, else the request's. A
// company that is not known cannot be checked, so it reads as unreadable, like
// a profile that cannot be read.
func skipStatus(e *Exec, profile func() (workspace.Profile, bool)) gates.SkipStatus {
	company := strings.TrimSpace(e.Run.company())
	if company == "" {
		return gates.SkipUnreadable
	}
	p, ok := profile()
	if !ok {
		return gates.SkipUnreadable
	}
	if gates.SkipCompany(gates.SkipInput{Company: company, Entries: p.EffectiveSkipCompanies()}).Allowed() {
		return gates.SkipNotListed
	}
	return gates.SkipListed
}

// solveRunKind is the kind of run the solver's gates judge: only an explicit
// run that submits may buy a solve; every other run, a live non-submit one and
// a resume that holds its Submit for the dashboard included, is a dry run.
func solveRunKind(e *Exec) gates.RunKind {
	in := e.Run.Input
	if e.resume != nil && e.resume.HoldSubmit != "" {
		return gates.RunDryRun
	}
	if in.Submit && in.ExplicitRequest {
		return gates.RunApplySubmit
	}
	return gates.RunDryRun
}
