package onboarding

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/documents/toolchain"
)

func TestDocumentToolchainLeaf_MissingToolsDegradeOnlyNewPDFs(t *testing.T) {
	env := &Env{ConfigDir: t.TempDir(), Getenv: func(k string) string {
		if k == toolchain.EnvToolPath {
			return t.TempDir()
		}
		return ""
	}}
	rep, err := Run(context.Background(), Checks(), env, Options{Only: "resumes.toolchain"})
	if err != nil || len(rep.Results) != 1 {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	r := rep.Results[0]
	if r.Status != Warn || !strings.Contains(r.Summary, toolchain.CodeUnavailable) || !strings.Contains(r.Summary, "keep working") || r.Fix == "" {
		t.Fatalf("result = %+v", r)
	}
	if rep.ExitCode() != ExitOK {
		t.Errorf("exit code = %d, want 0", rep.ExitCode())
	}
}

func TestDocumentToolchainOutcome_StatesMapToSeverity(t *testing.T) {
	ready := documentToolchainOutcome(toolchain.Report{State: toolchain.StateReady, Smoke: toolchain.SmokeCurrent})
	if ready.Status != Pass {
		t.Errorf("ready = %+v", ready)
	}
	needed := documentToolchainOutcome(toolchain.Report{State: toolchain.StateVerificationNeeded, Smoke: toolchain.SmokeStale, Fix: "run the smoke"})
	if needed.Status != Warn || needed.Fix != "run the smoke" || !strings.Contains(needed.Summary, "stale") {
		t.Errorf("verification needed = %+v", needed)
	}
	unavailable := documentToolchainOutcome(toolchain.Report{State: toolchain.StateUnavailable, Missing: []string{"moderncv", "pdftotext"}, Fix: "install them"})
	if unavailable.Status != Warn || !strings.Contains(unavailable.Summary, "moderncv, pdftotext") {
		t.Errorf("unavailable = %+v", unavailable)
	}
}
