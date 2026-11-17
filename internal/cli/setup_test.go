package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/api"
	"applysmith/internal/browser"
	"applysmith/internal/onboarding"
	"applysmith/internal/workspace"
)

// machineProbes stands in for Chrome and the disk, which differ per machine.
func machineProbes(oe *onboarding.Env) {
	oe.Chrome = func(context.Context) (browser.Chrome, error) {
		return browser.Chrome{Path: "/chrome", Version: "155.0.1.0", Major: 155}, nil
	}
	oe.DiskFree = func(string) (uint64, error) { return 50 << 30, nil }
	no := false
	oe.BrowserCheck = func(context.Context) (onboarding.BrowserReport, error) {
		return onboarding.BrowserReport{Windows: []onboarding.WindowReport{
			{Lane: "apply", Attached: true, Webdriver: &no, UserAgent: "Mozilla/5.0 Chrome/155.0.0.0"},
			{Lane: "scan", Attached: true, Webdriver: &no, UserAgent: "Mozilla/5.0 Chrome/155.0.0.0"},
		}}, nil
	}
}

// Test values are assembled at run time so no contact detail sits in the source.
func testEmail() string { return "riley.tester" + "@" + "mailbox-test.dev" }
func testPhone() string { return "+" + "1415" + "5552671" }

const cvText = `# Riley Tester

## Summary

Platform engineer who builds reliable internal tooling and data pipelines for teams of every size.

## Experience

### Platform Engineer, Northwind Systems (2019-03 to present)

- Built the deployment pipeline that cut release time from a day to ten minutes.

### Developer, Harbour Software (2015-06 to 2019-02)

- Maintained the billing service and its nightly reconciliation jobs.

## Education

### BSc Computer Science, Northern College (2011-09 to 2015-06)

## Skills

- Languages: Go, Python, SQL
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const narrativeText = `# Riley Tester

Headline: a platform engineer who makes release work boring and fast.

Strengths: I cut a day-long release to ten minutes at Northwind Systems by rebuilding the deployment pipeline, and I kept the billing reconciliation jobs at Harbour Software running through three platform migrations without a missed night.

Next: a senior platform role where reliability is the product and the team reviews its own incidents in public.

I will not take: roles that are on call every night with no engineering time to fix what pages people, or roles where tooling is an afterthought.

Describe me as: practical, calm in incidents, and direct in writing.
`

// fullScript answers every question of a full run: the required steps in
// order, a skip for every optional step, and the orientation.
func fullScript(cvPath, narrativePath, pdfPath string) string {
	return strings.Join([]string{
		"# step 1",
		"",
		"# step 2",
		cvPath, "yes", "yes",
		"# step 3",
		"Riley Tester", testEmail(), testPhone(), "Leeds", "West Yorkshire", "GB", "@ask-me", "@ask-me", "no",
		"# step 4",
		"citizen", "GB", "no", "no", "none", "open", "", "", "remote,hybrid",
		"# step 5",
		"Platform Engineer", "senior", "", "no", "London, anywhere or remote", "GBP", "year", "70000", "90000", "yes",
		"# step 6",
		narrativePath, "",
		"# step 7",
		pdfPath, "", "",
		"# step 8: leave every entry the profile cannot fill to yourself, then confirm the defaults",
		"no", "yes",
		"# step 9",
		"offline",
		"# step 10",
		"# optional steps: gmail, telegram, credentials, captcha, discovery, extension",
		"", "", "", "", "skip", "",
		"# step 17",
		"yes",
	}, "\n") + "\n"
}

func scriptedEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	te := newTestEnv(t)
	te.hook = machineProbes
	dir := t.TempDir()
	cv := writeFile(t, dir, "cv.md", cvText)
	narrative := writeFile(t, dir, "narrative.md", narrativeText)
	pdf := writeFile(t, dir, "riley-tester.pdf", "%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n")
	answers := writeFile(t, t.TempDir(), "answers.txt", fullScript(cv, narrative, pdf))
	return te, answers
}

type statusJSON struct {
	Progress int `json:"progress"`
	Steps    []struct {
		N        int    `json:"n"`
		ID       string `json:"id"`
		Title    string `json:"title"`
		Required bool   `json:"required"`
		Status   string `json:"status"`
		Reasons  []struct{ Text string }
	} `json:"steps"`
}

func (te *testEnv) status() statusJSON {
	te.t.Helper()
	out := te.run(context.Background(), "setup", "status", "--json", "--workspace", te.ws)
	if out.code != 0 {
		te.t.Fatalf("status: %+v", out)
	}
	var s statusJSON
	if err := json.Unmarshal([]byte(out.stdout), &s); err != nil {
		te.t.Fatalf("status json: %v\n%s", err, out.stdout)
	}
	return s
}

func (s statusJSON) of(id string) string {
	for _, st := range s.Steps {
		if st.ID == id {
			return st.Status
		}
	}
	return ""
}

// userFiles snapshots the workspace without the state directory.
func userFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range snapshot(t, root) {
		if !strings.HasPrefix(k, ".applysmith") {
			out[k] = v
		}
	}
	return out
}

func TestSetupScriptedRunCompletesEveryRequiredStepWithTheFakeProvider(t *testing.T) {
	te, answers := scriptedEnv(t)
	out := te.run(context.Background(), "setup", "--workspace", te.ws, "--answers", answers)
	if out.code != 0 {
		t.Fatalf("setup exit %d\nstdout:\n%s\nstderr:\n%s", out.code, out.stdout, out.stderr)
	}
	st := te.status()
	for _, c := range onboarding.Catalog() {
		switch {
		case c.Required && st.of(c.ID) != "complete":
			t.Errorf("required step %s is %s", c.ID, st.of(c.ID))
		case !c.Required && st.of(c.ID) != "skipped":
			t.Errorf("optional step %s is %s, want skipped", c.ID, st.of(c.ID))
		}
	}
	if st.Progress != 100 {
		t.Errorf("progress = %d, want 100", st.Progress)
	}
	checkStepsFromTheCatalog(t, out.stdout)
	// No step of setup turns remote access on.
	if got := remoteMode(t, te); got != "off" {
		t.Errorf("policy.yml holds remote access mode %q after setup", got)
	}
}

func TestSetupRerunIsIdempotent(t *testing.T) {
	te, answers := scriptedEnv(t)
	if out := te.run(context.Background(), "setup", "--workspace", te.ws, "--answers", answers); out.code != 0 {
		t.Fatalf("first run: %+v", out)
	}
	before := userFiles(t, te.ws)
	out := te.run(context.Background(), "setup", "--workspace", te.ws, "--answers", answers)
	if out.code != 0 || !strings.Contains(out.stdout, "Progress: 100%") {
		t.Fatalf("second run: %+v", out)
	}
	if strings.Contains(out.stdout, "Step ") {
		t.Errorf("a complete step was asked again:\n%s", out.stdout)
	}
	after := userFiles(t, te.ws)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed on the second run", k)
		}
	}
	// Answering the same values again through the writers changes nothing either.
	in := `{"full_name":"Riley Tester","city":"Leeds"}`
	te.stdin = in
	if o := te.run(context.Background(), "setup", "apply", "identity", "--file", "-", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("re-apply: %+v", o)
	}
	for k, v := range userFiles(t, te.ws) {
		if before[k] != v {
			t.Errorf("%s changed after re-applying the same values", k)
		}
	}
}

func TestSetupInteractiveWithoutATerminalExitsTwo(t *testing.T) {
	te := newTestEnv(t)
	out := te.run(context.Background(), "setup", "--workspace", te.ws)
	if out.code != 2 || !strings.Contains(out.stderr, "not a terminal") {
		t.Fatalf("out = %+v", out)
	}
}

func TestSetupDryRunRejectsAMalformedEmailWithoutWriting(t *testing.T) {
	te := newTestEnv(t)
	te.hook = machineProbes
	if out := te.run(context.Background(), "init", "--workspace", te.ws); out.code != 0 {
		t.Fatal(out)
	}
	before := userFiles(t, te.ws)
	te.stdin = `{"email":"not-an-email"}`
	out := te.run(context.Background(), "setup", "apply", "identity", "--file", "-", "--dry-run", "--workspace", te.ws)
	if out.code != 2 || !strings.Contains(out.stderr, "email") {
		t.Fatalf("out = %+v", out)
	}
	for k, v := range userFiles(t, te.ws) {
		if before[k] != v {
			t.Errorf("%s changed", k)
		}
	}
	te.stdin = `{"nickname":"x"}`
	if out := te.run(context.Background(), "setup", "apply", "identity", "--file", "-", "--workspace", te.ws); out.code != 2 || !strings.Contains(out.stderr, "nickname: unknown field") {
		t.Fatalf("unknown field: %+v", out)
	}
}

func TestSetupCreatesNothingInTheDefaultLocationWhenAnotherIsChosen(t *testing.T) {
	te := newBareTestEnv(t)
	te.hook = machineProbes
	other := filepath.Join(t.TempDir(), "elsewhere")
	answers := writeFile(t, t.TempDir(), "answers.txt", other+"\n")
	out := te.run(context.Background(), "setup", "--workspace", te.ws, "--answers", answers)
	if out.code != 0 {
		t.Fatalf("setup: %+v", out)
	}
	if _, err := os.Stat(te.ws); !os.IsNotExist(err) {
		t.Fatalf("the location that was not chosen exists (stat err = %v)", err)
	}
	for _, name := range []string{"id", "lock", "applysmith.db"} {
		if _, err := os.Stat(filepath.Join(other, ".applysmith", name)); err == nil {
			t.Errorf("%s was created before the run restarted in the chosen workspace", name)
		}
	}
}

func TestSetupApplyBeforeTheWorkspaceStepCreatesNothing(t *testing.T) {
	te := newBareTestEnv(t)
	te.hook = machineProbes
	out := te.run(context.Background(), "setup", "apply", "identity", "--workspace", te.ws, "--file", "-")
	if out.code == 0 {
		t.Fatalf("identity applied without a workspace: %+v", out)
	}
	if _, err := os.Stat(te.ws); !os.IsNotExist(err) {
		t.Fatalf("a refused apply created %s (stat err = %v)", te.ws, err)
	}
}

func TestSetupBesideARunningServerNamesTheWizardAddress(t *testing.T) {
	te := newTestEnv(t)
	te.hook = machineProbes
	if out := te.run(context.Background(), "init", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("init: %+v", out)
	}
	srv := te.startServe()
	out := te.run(context.Background(), "setup", "apply", "identity", "--workspace", te.ws, "--file", "-")
	if out.code != 4 || !strings.Contains(out.stderr, srv.origin+"/setup") {
		t.Fatalf("setup beside a server: %+v, want the wizard address %s", out, srv.origin+"/setup")
	}
}

func TestCVImportCommandConfirmsTextAndLeavesProfileUntouched(t *testing.T) {
	for _, answer := range []string{"yes", "no"} {
		t.Run(fmt.Sprintf("answer-%q", answer), func(t *testing.T) {
			te := newTestEnv(t)
			if got := te.run(context.Background(), "init", "--workspace", te.ws); got.code != 0 {
				t.Fatal(got)
			}
			before, _ := os.ReadFile(filepath.Join(te.ws, "cv.md"))
			profile, _ := os.ReadFile(filepath.Join(te.ws, "profile.yml"))
			path := writeFile(t, t.TempDir(), "cv.txt", cvText)
			te.stdin = answer
			got := te.run(context.Background(), "cv", "import", path, "--workspace", te.ws, "--answers", "-")
			want := 2
			if strings.TrimSpace(answer) == "yes" {
				want = 0
			}
			if got.code != want || !strings.Contains(got.stdout, cvText) {
				t.Fatalf("result=%+v want=%d", got, want)
			}
			after, _ := os.ReadFile(filepath.Join(te.ws, "cv.md"))
			if want == 0 {
				if string(after) != cvText {
					t.Fatal("confirmed text missing")
				}
			} else if string(after) != string(before) {
				t.Fatal("refusal changed CV")
			}
			pafter, _ := os.ReadFile(filepath.Join(te.ws, "profile.yml"))
			if string(pafter) != string(profile) {
				t.Fatal("import changed profile without fact review")
			}
		})
	}
}

type cvConfirmReader struct {
	io.Reader
	before func()
}

func (r *cvConfirmReader) Read(b []byte) (int, error) {
	if r.before != nil {
		f := r.before
		r.before = nil
		f()
	}
	return r.Reader.Read(b)
}

func TestCVImportCommandRefusesLockedOrChangedWorkspace(t *testing.T) {
	te := newTestEnv(t)
	if got := te.run(context.Background(), "init", "--workspace", te.ws); got.code != 0 {
		t.Fatal(got)
	}
	path := writeFile(t, t.TempDir(), "cv.md", cvText)
	ws := workspace.Open(te.ws)
	lock, err := api.AcquireLock(ws.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	te.stdin = "yes"
	got := te.run(context.Background(), "cv", "import", path, "--workspace", te.ws, "--answers", "-")
	lock.Release()
	if got.code != 4 || !strings.Contains(got.stderr, "workspace is in use") {
		t.Fatalf("locked=%+v", got)
	}
	var out, errw syncBuffer
	e := te.env(&out, &errw)
	newer := "# A newer edit written during review\n"
	e.stdin = &cvConfirmReader{Reader: strings.NewReader("yes"), before: func() {
		if err := os.WriteFile(ws.Path(workspace.FileCV), []byte(newer), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	if code := e.run(context.Background(), []string{"cv", "import", path, "--workspace", te.ws, "--answers", "-"}); code == 0 {
		t.Fatal("overwrote a newer revision")
	}
	cv, _ := os.ReadFile(ws.Path(workspace.FileCV))
	if string(cv) != newer {
		t.Fatal("newer edit was lost")
	}
}
