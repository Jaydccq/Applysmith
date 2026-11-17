package onboarding

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/browser"
	"applysmith/internal/demo"
	"applysmith/internal/discovery"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// demoEnv is a demo workspace created by the demo init, with the database open
// for writing and the machine probes replaced.
func demoEnv(t *testing.T) *Env {
	t.Helper()
	root := filepath.Join(t.TempDir(), demo.DefaultDir)
	ws, _, err := PrepareDemo(root)
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{Workspace: ws, Backend: vault.NewMemory(), HomeDir: t.TempDir(), ConfigDir: t.TempDir(),
		Getenv: func(string) string { return "" },
		Chrome: func(context.Context) (browser.Chrome, error) {
			return browser.Chrome{Version: "155.0.1", Major: 155}, nil
		},
		DiskFree: func(string) (uint64, error) { return 50 << 30, nil }}
	closeFn, err := env.Open(bg(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	if _, err := SeedDemo(bg(), ws, env.Store, time.Now); err != nil {
		t.Fatal(err)
	}
	return env
}

// Scenario "The demo persona completes setup only in the demo".
func TestDemo_ThePersonaCompletesSetupOnlyInTheDemo(t *testing.T) {
	env := setupEnv(t)
	profile := string(demo.File(demo.FileProfile))
	if err := os.WriteFile(env.Workspace.Path(workspace.FileProfile), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	d := status(t, env, "identity")
	if d.Status != StepTemplate || !hasReason(d, "alex@example.com") {
		t.Fatalf("outside a demo the persona's identity is %s %v, want template naming the email", d.Status, d.Texts())
	}
	if err := env.Workspace.MarkDemo(); err != nil {
		t.Fatal(err)
	}
	d = status(t, env, "identity")
	if d.Status != StepComplete || len(d.Reasons) != 1 || d.Reasons[0].Text != "demo persona" {
		t.Fatalf("in a demo the persona's identity is %s %v, want complete with the reason demo persona", d.Status, d.Texts())
	}
	// A placeholder is still not a value, demo or not.
	profile = strings.Replace(profile, "line1: 1 Example Street", "line1: <your street>", 1)
	if err := os.WriteFile(env.Workspace.Path(workspace.FileProfile), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if d = status(t, env, "identity"); d.Status != StepTemplate {
		t.Fatalf("a placeholder in the demo reads %s, want template", d.Status)
	}
}

func TestDemo_InitSeedsThePersonaThePostingsAndTheReviewedBank(t *testing.T) {
	env := demoEnv(t)
	if !env.Workspace.IsDemo() {
		t.Fatal("the demo workspace carries no marker")
	}
	for _, id := range []string{"workspace", "identity", "cv", "work-authorization", "targets", "story", "resumes", "answer-bank"} {
		if d := status(t, env, id); d.Status != StepComplete {
			t.Errorf("step %s is %s: %v", id, d.Status, d.Texts())
		}
	}
	if d := status(t, env, "llm"); d.Status == StepComplete {
		t.Error("the provider step is complete without a provider; the demo cannot seed it")
	}
	rows, err := discovery.NewRepo(env.Store, nil).InboxList(bg(), discovery.InboxQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(demo.Postings()) {
		t.Fatalf("%d inbox rows, want the %d sample postings", len(rows), len(demo.Postings()))
	}
	for _, r := range rows {
		if r.Source != demo.Source || !strings.Contains(r.URL, ".example/") || r.Description == "" {
			t.Errorf("inbox row %+v is not a sample posting on a reserved domain", r)
		}
	}
	// A second init admits nothing twice and keeps what the user changed.
	if err := os.WriteFile(env.Workspace.Path(workspace.FileNarrative), []byte("# Narrative\n\nmine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareDemo(env.Workspace.Root); err != nil {
		t.Fatal(err)
	}
	res, err := SeedDemo(bg(), env.Workspace, env.Store, time.Now)
	if err != nil || res.Postings != 0 {
		t.Fatalf("a repeat admitted %d postings (%v)", res.Postings, err)
	}
	if b, _ := os.ReadFile(env.Workspace.Path(workspace.FileNarrative)); string(b) != "# Narrative\n\nmine\n" {
		t.Fatal("a repeat overwrote the user's edit")
	}
}

func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			info, _ := d.Info()
			out = append(out, p+" "+info.ModTime().String())
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// Scenario "The demo is never written over a real workspace".
func TestDemo_IsNeverWrittenOverOrInsideARealWorkspace(t *testing.T) {
	real := workspace.Open(filepath.Join(t.TempDir(), "Applysmith"))
	if _, err := real.Init(); err != nil {
		t.Fatal(err)
	}
	before := treeOf(t, real.Root)
	for _, dir := range []string{real.Root, filepath.Join(real.Root, "demo")} {
		if _, _, err := PrepareDemo(dir); !errors.Is(err, ErrNotDemoWorkspace) {
			t.Fatalf("PrepareDemo(%s) = %v, want a refusal", dir, err)
		}
	}
	if after := treeOf(t, real.Root); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("the refused init wrote into the workspace:\n%v\n%v", before, after)
	}
	if real.IsDemo() {
		t.Fatal("the real workspace was marked demo")
	}
}

func TestDemo_TheWorkspaceStepNeverRecordsTheDemoAsYourWorkspace(t *testing.T) {
	env := demoEnv(t)
	mustApply(t, env, "workspace", `{}`)
	if _, err := os.Stat(filepath.Join(env.ConfigDir, "applysmith", "workspace.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the demo was recorded as the user's workspace: %v", err)
	}
}

// "Try the demo" creates the demo beside the server's own workspace and never
// writes into that workspace.
func TestDemo_TheSetupServiceCreatesTheDemoInItsOwnFolder(t *testing.T) {
	env := setupEnv(t)
	before := treeOf(t, env.Workspace.Root)
	dir := filepath.Join(t.TempDir(), demo.DefaultDir)
	svc := &Service{DemoDir: dir, HomeDir: env.HomeDir}
	got, err := svc.Demo(bg(), api.SetupHost{Workspace: env.Workspace, Store: env.Store, Vault: env.Vault})
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != dir || got.Postings != len(demo.Postings()) || !strings.Contains(got.Start, dir) {
		t.Fatalf("result %+v", got)
	}
	// Under the home folder the page names the folder the way its own text does.
	inHome := &Service{HomeDir: t.TempDir()}
	got, err = inHome.Demo(bg(), api.SetupHost{Workspace: env.Workspace, Store: env.Store, Vault: env.Vault})
	if err != nil {
		t.Fatal(err)
	}
	if want := "~/" + demo.DefaultDir; got.Workspace != want || got.Start != "applysmith serve --workspace "+want {
		t.Fatalf("home demo shown as %q, start %q", got.Workspace, got.Start)
	}
	if !workspace.Open(dir).IsDemo() || env.Workspace.IsDemo() {
		t.Fatal("the marker is on the wrong workspace")
	}
	if after := treeOf(t, env.Workspace.Root); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatal("Try the demo wrote into the server's own workspace")
	}
	svc.DemoDir = env.Workspace.Root
	if _, err := svc.Demo(bg(), api.SetupHost{Workspace: env.Workspace, Store: env.Store}); err == nil {
		t.Fatal("Try the demo accepted the user's own workspace as its folder")
	}
}
