package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/toolchain"
)

// noTools points the executable search at an empty directory, which is what a
// machine without TeX, Python and Poppler looks like.
func noTools(te *testEnv) {
	te.vars[toolchain.EnvToolPath] = te.t.TempDir()
}

func TestDocumentsToolchainCheck_ReportsMissingToolsWithoutChangingTheMachine(t *testing.T) {
	te := newTestEnv(t)
	noTools(te)
	o := te.run(context.Background(), "documents", "toolchain", "check", "--json")
	if o.code != 1 {
		t.Fatalf("exit = %d, want 1: %+v", o.code, o)
	}
	var rep toolchain.Report
	if err := json.Unmarshal([]byte(o.stdout), &rep); err != nil {
		t.Fatalf("json: %v\n%s", err, o.stdout)
	}
	if rep.State != toolchain.StateUnavailable || rep.Code != "document-toolchain-unavailable" || len(rep.Missing) == 0 || rep.Fix == "" {
		t.Fatalf("report = %+v", rep)
	}
	if entries, _ := os.ReadDir(te.config); len(entries) != 0 {
		t.Errorf("check created %d entries in the config directory", len(entries))
	}
}

func TestDocumentsToolchainSetup_ShowsTheBudgetAndWaitsForConsent(t *testing.T) {
	te := newTestEnv(t)
	noTools(te)
	o := te.run(context.Background(), "documents", "toolchain", "setup")
	if o.code != 3 {
		t.Fatalf("exit = %d, want 3 (a person must accept the budget): %+v", o.code, o)
	}
	for _, frag := range []string{"pypdf", "MiB", "brew install poppler", "does not"} {
		if !strings.Contains(o.stdout, frag) {
			t.Errorf("budget lacks %q:\n%s", frag, o.stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(te.config, "applysmith")); !os.IsNotExist(err) {
		t.Error("setup wrote state before consent")
	}
}

func TestDocumentsToolchainSetup_WithoutPythonStops(t *testing.T) {
	te := newTestEnv(t)
	noTools(te)
	o := te.run(context.Background(), "documents", "toolchain", "setup", "--yes")
	if o.code != 1 || !strings.Contains(o.stderr, "Python") {
		t.Fatalf("exit = %d stderr = %q", o.code, o.stderr)
	}
}

func TestDocumentsToolchainSmoke_NeedsEveryComponent(t *testing.T) {
	te := newTestEnv(t)
	noTools(te)
	o := te.run(context.Background(), "documents", "toolchain", "smoke")
	if o.code != 3 || !strings.Contains(o.stderr, "document-toolchain-unavailable") {
		t.Fatalf("exit = %d stderr = %q", o.code, o.stderr)
	}
}

func TestDoctorResumesIncludesTheToolchainLeaf(t *testing.T) {
	te := newTestEnv(t)
	noTools(te)
	o := te.run(context.Background(), "doctor", "--workspace", te.ws, "--port", "0", "--json", "--only", "resumes.toolchain")
	var rep struct {
		Results []struct {
			ID, Status, Summary, Fix string
			Required                 bool
		}
	}
	if err := json.Unmarshal([]byte(o.stdout), &rep); err != nil || len(rep.Results) != 1 {
		t.Fatalf("report: %+v (%v)", o, err)
	}
	r := rep.Results[0]
	if r.ID != "resumes.toolchain" || r.Status != "warn" || r.Required || !strings.Contains(r.Summary, "document-toolchain-unavailable") || r.Fix == "" {
		t.Fatalf("leaf = %+v", r)
	}
	if o.code != 0 {
		t.Errorf("missing tools must not fail the doctor, exit = %d", o.code)
	}
	if entries, _ := os.ReadDir(te.config); len(entries) != 0 {
		t.Errorf("doctor created %d entries in the config directory", len(entries))
	}
}
