package apply

import (
	"context"
	"errors"
	"testing"

	"applysmith/internal/captcha"
	"applysmith/internal/gates"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

func solverFor(t *testing.T, in Input, withProfile bool) captcha.Solver {
	t.Helper()
	v, err := vault.New("solver-ws", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	src := SolverSource{Vault: v, Workspace: workspace.Open(t.TempDir()), Profile: func() (workspace.Profile, bool) { return workspace.Profile{}, withProfile }}
	return SolverFactory(src)(&Exec{Run: &Run{Input: in}}, func(gates.Decision) {})
}

func TestSolverFactory_NoKeyAndNoPolicyLeaveTheChallengeToTheUser(t *testing.T) {
	sv := solverFor(t, Input{Submit: true, ExplicitRequest: true, Company: "Example Corp"}, true)
	_, err := sv.Solve(context.Background(), captcha.Challenge{Kind: gates.ChallengeRecaptchaV2, Point: gates.PointBeforePress, SiteKey: "k", PageURL: "https://jobs.example.test/p/1"})
	var ref *captcha.Refusal
	if !errors.As(err, &ref) || ref.Decision.Allowed() {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

func TestSolverFactory_ADryRunIsNeverASolvingRun(t *testing.T) {
	sv := solverFor(t, Input{Submit: false, Company: "Example Corp"}, true)
	_, err := sv.Solve(context.Background(), captcha.Challenge{Kind: gates.ChallengeRecaptchaV2, Point: gates.PointBeforePress, SiteKey: "k", PageURL: "https://jobs.example.test/p/1"})
	var ref *captcha.Refusal
	if !errors.As(err, &ref) || ref.Decision.Allowed() {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// A Done resume that holds the final Submit (pressed from Telegram) is not a
// submitting run, so it buys no solve for the challenge beside that Submit.
func TestSolverFactory_AResumeThatHoldsSubmitIsNeverASolvingRun(t *testing.T) {
	in := Input{Submit: true, ExplicitRequest: true, Caller: gates.CallerAskAgent, Company: "Example Corp"}
	if got := solveRunKind(&Exec{Run: &Run{Input: in}}); got != gates.RunApplySubmit {
		t.Fatalf("an explicit submitting run reads as %v", got)
	}
	held := &Exec{Run: &Run{Input: in}, resume: &ResumeInput{Input: in, HoldSubmit: "resumed from Telegram, so Submit is confirmed in the dashboard"}}
	if got := solveRunKind(held); got != gates.RunDryRun {
		t.Fatalf("a resume that holds Submit reads as %v, want a dry run", got)
	}
	if got := solveRunKind(&Exec{Run: &Run{Input: in}, resume: &ResumeInput{Input: in}}); got != gates.RunApplySubmit {
		t.Fatalf("a dashboard resume reads as %v", got)
	}
}

// A run whose company is not known cannot be checked against the skip list, so
// the verdict is unreadable and no solve is bought. The company is read when
// the solve is asked, so a report bound after the solver was built counts.
func TestSolverFactory_UnknownCompanyReadsAsAnUnreadableSkipList(t *testing.T) {
	profile := func() (workspace.Profile, bool) { return workspace.Profile{}, true }
	e := &Exec{Run: &Run{Input: Input{Submit: true, ExplicitRequest: true}}}
	if got := skipStatus(e, profile); got != gates.SkipUnreadable {
		t.Fatalf("no company: %v, want %v", got, gates.SkipUnreadable)
	}
	e.Run.Input.Company = "  "
	if got := skipStatus(e, profile); got != gates.SkipUnreadable {
		t.Fatalf("a blank company: %v, want %v", got, gates.SkipUnreadable)
	}
	e.Run.Input.Company = "Example Corp"
	if got := skipStatus(e, profile); got != gates.SkipNotListed {
		t.Fatalf("a company off the list: %v", got)
	}
	e.Run.Report = &ReportBinding{Company: "Microsoft"}
	if got := skipStatus(e, profile); got != gates.SkipListed {
		t.Fatalf("the bound report's company on the default list: %v", got)
	}
	if got := skipStatus(e, func() (workspace.Profile, bool) { return workspace.Profile{}, false }); got != gates.SkipUnreadable {
		t.Fatalf("an unreadable profile: %v", got)
	}
}

// The solves an earlier leg of a resumed run made count against the cap of
// the solver the factory builds for the next leg.
func TestSolverFactory_AResumedRunStartsFromTheSolvesItAlreadyMade(t *testing.T) {
	v, err := vault.New("solver-ws", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	src := SolverSource{Vault: v, Workspace: workspace.Open(t.TempDir()), Profile: func() (workspace.Profile, bool) { return workspace.Profile{}, true }}
	sv := SolverFactory(src)(&Exec{Run: &Run{Input: Input{Submit: true, ExplicitRequest: true}}, spent: 2}, func(gates.Decision) {})
	if got := sv.Creations(); got != 2 {
		t.Fatalf("the resumed leg's solver starts at %d solves, want 2", got)
	}
}
