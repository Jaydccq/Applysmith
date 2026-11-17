package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
)

const cliPosting = `Example Systems builds scheduling software for clinics. You will design and build backend services in Go, own their reliability in production, and work with a small team on a shared roadmap. We value clear writing, careful design, and steady delivery. Three or more years building production backend services are required.`

func (te *testEnv) evaluate(args ...string) output {
	te.t.Helper()
	var out, errw syncBuffer
	e := te.env(&out, &errw)
	e.livenessReader = evalBrowser(func(context.Context, string) (discovery.PageRead, error) {
		return discovery.PageRead{}, evaluate.ErrNeedsBrowser
	})
	fake := llm.NewFake()
	fake.SetGenerator(evaluate.FakeGenerator)
	e.llmOptions = []llm.Option{llm.WithFake(fake), llm.WithOfflineSimulation()}
	code := e.run(context.Background(), append(append([]string{}, args...), "--workspace", te.ws))
	return output{code, out.String(), errw.String()}
}

func TestEvaluateCLI_TextFileAndURL(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	o := te.evaluate("evaluate", "--text", cliPosting, "--url", "https://jobs.example.test/p/cli1", "--company", "Example Systems", "--title", "Software Engineer", "--json")
	if o.code != 0 {
		t.Fatalf("evaluate: %+v", o)
	}
	var res evaluate.Result
	if err := json.Unmarshal([]byte(o.stdout), &res); err != nil || res.Outcome != evaluate.OutcomeReported || res.ReportNumber == 0 || res.Score == nil {
		t.Fatalf("result %+v err %v out %s", res, err, o.stdout)
	}
	for _, ext := range []string{".md", ".html"} {
		matches, _ := filepath.Glob(filepath.Join(te.ws, "reports", "0001-example-systems-*"+ext))
		if len(matches) != 1 {
			t.Fatalf("report %s files = %v", ext, matches)
		}
	}
	if tr := te.mustTracker("list"); !strings.Contains(tr.stdout, "Software Engineer") {
		t.Fatalf("the tracker row must exist:\n%s", tr.stdout)
	}

	// The same posting again is a duplicate, in plain text.
	o = te.evaluate("evaluate", "--text", cliPosting, "--url", "https://jobs.example.test/p/cli1", "--company", "Example Systems", "--title", "Software Engineer")
	if o.code != 0 || !strings.HasPrefix(o.stdout, "duplicate report 0001") || !strings.Contains(o.stdout, "\nreport: "+filepath.Join(te.ws, "reports", "0001-")) {
		t.Fatalf("duplicate: %+v", o)
	}
	if !strings.Contains(o.stderr, "evaluation 1: reading\n") {
		t.Errorf("progress lines: %q", o.stderr)
	}
	// --file reads the text from a path; --mode screen stops after the screen.
	p := filepath.Join(t.TempDir(), "posting.txt")
	if err := os.WriteFile(p, []byte(cliPosting), 0o600); err != nil {
		t.Fatal(err)
	}
	o = te.evaluate("evaluate", "--file", p, "--url", "https://jobs.example.test/p/cli2", "--company", "Other Example", "--title", "Platform Engineer", "--mode", "screen")
	if o.code != 0 || !strings.HasPrefix(o.stdout, "screened report 0002") {
		t.Fatalf("screen: %+v", o)
	}
}

// The "no key yet" setup choice must be able to produce a report: the product
// router, not only the test routers, serves the evaluation fixtures.
func TestEvaluateCLI_OfflineSimulationReportsWithoutTestFixtures(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	choice := filepath.Join(t.TempDir(), "llm.json")
	if err := os.WriteFile(choice, []byte(`{"offline":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := te.run(context.Background(), "setup", "apply", "llm", "--file", choice, "--workspace", te.ws); o.code != 0 {
		t.Fatalf("setup llm: %+v", o)
	}
	o := te.run(context.Background(), "evaluate", "--text", cliPosting, "--url", "https://jobs.example.test/p/offline1",
		"--company", "Example Systems", "--title", "Software Engineer", "--json", "--workspace", te.ws)
	var res evaluate.Result
	if err := json.Unmarshal([]byte(o.stdout), &res); o.code != 0 || err != nil || res.Outcome != evaluate.OutcomeReported || res.ReportNumber == 0 {
		t.Fatalf("offline evaluation: %+v result %+v err %v", o, res, err)
	}
	md, _ := filepath.Glob(filepath.Join(te.ws, "reports", "0001-*.md"))
	if len(md) != 1 {
		t.Fatalf("report files = %v", md)
	}
	if b, _ := os.ReadFile(md[0]); !strings.Contains(string(b), evaluate.FakeNote) {
		t.Fatalf("the report must say its score is simulated:\n%s", b)
	}
}

func TestEvaluateCLI_UsageAndFailures(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if o := te.evaluate("evaluate"); o.code != 2 {
		t.Fatalf("no input is a usage error: %+v", o)
	}
	// Input the evaluation cannot work from is rejected (2) with the sentence
	// that says what to do, before any run exists.
	for name, tc := range map[string]struct {
		args []string
		want []string
	}{
		"a text under the minimum":         {[]string{"--text", "too short", "--url", "https://jobs.example.test/p/short"}, []string{"9 characters", "200"}},
		"pasted text given as the address": {[]string{cliPosting}, []string{"--text", "--file"}},
		"an address without its scheme":    {[]string{"jobs.example.test/p/1"}, []string{"--text", "--file"}},
	} {
		o := te.evaluate(append([]string{"evaluate"}, tc.args...)...)
		if o.code != 2 || o.stdout != "" || strings.Contains(o.stderr, "evaluation 1:") {
			t.Errorf("%s: %+v", name, o)
		}
		// The usage that may follow names the flags too; the problem line is the first.
		problem, _, _ := strings.Cut(o.stderr, "\n")
		for _, want := range tc.want {
			if !strings.Contains(problem, want) {
				t.Errorf("%s: the problem line lacks %q: %q", name, want, problem)
			}
		}
	}
	// No browser to read the page with is an environment failure (4).
	if o := te.evaluate("evaluate", "--url", "https://careers.example.com/jobs/search?gh_jid=1001"); o.code != 4 ||
		!strings.Contains(o.stderr, "browser reader") || !strings.Contains(o.stderr, "paste the posting text") {
		t.Fatalf("a branded careers URL must say what to do: %+v", o)
	}
	if o := te.evaluate("evaluate", "--text", cliPosting, "--mode", "nope"); o.code != 2 {
		t.Fatalf("bad mode: %+v", o)
	}
	// With no model provider a person must set one up (3, docs/setup/reference.md),
	// and the sentence names the command and the page that do.
	o := te.run(context.Background(), "evaluate", "--text", cliPosting, "--url", "https://jobs.example.test/p/nm", "--workspace", te.ws)
	if o.code != 3 || o.stdout != "" || !strings.Contains(o.stderr, "applysmith llm configure") || !strings.Contains(o.stderr, "Settings") || strings.Contains(o.stderr, "no-route") {
		t.Fatalf("no provider: %+v", o)
	}
	// --json keeps its result on standard output and exits the same way.
	o = te.run(context.Background(), "evaluate", "--text", cliPosting, "--url", "https://jobs.example.test/p/nm", "--json", "--workspace", te.ws)
	var res evaluate.Result
	if err := json.Unmarshal([]byte(o.stdout), &res); err != nil || o.code != 3 || res.Reason != "no-route" {
		t.Fatalf("no provider as JSON: %+v result %+v err %v", o, res, err)
	}
}

// With no server running, the command reads a page no ATS API serves in a
// browser it starts itself. A computer that cannot give it one (here no Chrome
// where APPLYSMITH_CHROME_BIN points, so none starts) is an environment failure
// (4, docs/setup/reference.md), and standard error names the way around it in
// place of the reason code.
func TestEvaluateCLI_ABrowserTheComputerCannotGiveIsAnEnvironmentFailure(t *testing.T) {
	t.Setenv("APPLYSMITH_CHROME_BIN", filepath.Join(t.TempDir(), "no-chrome"))
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	o := te.run(context.Background(), "evaluate", "https://careers.example.com/jobs/1001", "--workspace", te.ws)
	if o.code != 4 || strings.Contains(o.stdout, "posting-unreadable") || !strings.Contains(o.stderr, "--text") {
		t.Fatalf("%+v", o)
	}
}

func TestPromptsAndDataCLI(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if o := te.run(context.Background(), "prompts", "list", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "deep-eval\tembedded") {
		t.Fatalf("list: %+v", o)
	}
	dir := filepath.Join(te.ws, "prompts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deep-eval.tmpl"), []byte("no fields here"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := te.run(context.Background(), "prompts", "check", "--workspace", te.ws)
	if o.code != 1 || !strings.Contains(o.stderr, "prompts/deep-eval.tmpl") {
		t.Fatalf("check: %+v", o)
	}
	if o := te.run(context.Background(), "prompts", "diff", "deep-eval", "--workspace", te.ws); !strings.Contains(o.stdout, "+ no fields here") {
		t.Fatalf("diff: %+v", o)
	}
}

func TestEvaluateCLI_GapsReadsSavedEvidence(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if o := te.evaluate("evaluate", "--text", cliPosting, "--url", "https://jobs.example.test/p/gaps1", "--company", "Example Systems", "--title", "Software Engineer"); o.code != 0 {
		t.Fatalf("evaluate: %+v", o)
	}
	o := te.evaluate("evaluate", "gaps", "--report", "1,77", "--json")
	if o.code != 0 {
		t.Fatalf("gaps: %+v", o)
	}
	var rep evaluate.GapReport
	if err := json.Unmarshal([]byte(o.stdout), &rep); err != nil || len(rep.Sample) != 1 || len(rep.Excluded) != 1 || rep.Excluded[0].Reason != evaluate.ExcludedNotFound || len(rep.Gaps) == 0 {
		t.Fatalf("report %+v err %v out %s", rep, err, o.stdout)
	}
	for _, g := range rep.Gaps {
		if g.State == evaluate.GapConfirmed {
			t.Fatalf("a fresh analysis confirms nothing: %+v", g)
		}
	}
	text := te.evaluate("evaluate", "gaps", "--report", "1")
	if text.code != 0 || !strings.Contains(text.stdout, "1 report(s) analyzed") || !strings.Contains(text.stdout, "not-evidenced") && !strings.Contains(text.stdout, "needs-review") {
		t.Fatalf("text output: %+v", text)
	}
	if bad := te.evaluate("evaluate", "gaps", "--report", "one"); bad.code != 2 {
		t.Fatalf("a bad number: %+v", bad)
	}
	if none := te.evaluate("evaluate", "gaps"); none.code != 2 {
		t.Fatalf("no report: %+v", none)
	}
}

const inboxPostingURL = "https://jobs.example.test/p/inbox-row"

// inboxStatus is the status of inbox row 1, read through the command line.
func (te *testEnv) inboxStatus() string {
	te.t.Helper()
	o := te.run(context.Background(), "inbox", "list", "--json", "--workspace", te.ws)
	var page wire.InboxPage
	if err := json.Unmarshal([]byte(o.stdout), &page); err != nil || len(page.Rows) != 1 {
		te.t.Fatalf("inbox list: %+v err %v", o, err)
	}
	return page.Rows[0].Status
}

func (te *testEnv) workspaceWithInboxRow() {
	te.t.Helper()
	ctx := context.Background()
	if o := te.run(ctx, "init", "--workspace", te.ws); o.code != 0 {
		te.t.Fatalf("init: %+v", o)
	}
	if o := te.run(ctx, "inbox", "add", inboxPostingURL, "--workspace", te.ws); o.code != 0 {
		te.t.Fatalf("inbox add: %+v", o)
	}
}

// job-discovery, "Pipeline inbox": an evaluation of a pending row moves it
// through queued to evaluated.
func TestEvaluateCLI_AnInboxRowEvaluatedDirectlyEndsEvaluated(t *testing.T) {
	te := newTestEnv(t)
	te.workspaceWithInboxRow()
	if got := te.inboxStatus(); got != "pending" {
		t.Fatalf("a new row is %s", got)
	}
	if o := te.evaluate("evaluate", "1", "--text", cliPosting, "--company", "Example Systems", "--title", "Software Engineer"); o.code != 0 {
		t.Fatalf("evaluate: %+v", o)
	}
	if got := te.inboxStatus(); got != "evaluated" {
		t.Fatalf("the row is %s after its evaluation", got)
	}
}

// The gate a promotion runs also stands before a direct evaluation.
func TestEvaluateCLI_AnInboxRowTheGateRejectsIsRefusedAndStaysPending(t *testing.T) {
	te := newTestEnv(t)
	te.workspaceWithInboxRow()
	if o := te.run(context.Background(), "blocked", "add", "jobs.example.test", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("blocked add: %+v", o)
	}
	o := te.evaluate("evaluate", "1", "--text", cliPosting)
	if o.code == 0 || !strings.Contains(o.stderr, "eligibility gate") {
		t.Fatalf("evaluate: %+v", o)
	}
	if got := te.inboxStatus(); got != "pending" {
		t.Fatalf("the row is %s", got)
	}
}

// With a server running the command reaches it instead of asking for it to be
// stopped: an in-process pool would end the server's running jobs as
// interrupted at its start.
func TestEvaluateCLI_WhileAServerRunsTheEvaluationGoesThroughIt(t *testing.T) {
	te := newTestEnv(t)
	te.workspaceWithInboxRow()
	choice := filepath.Join(t.TempDir(), "llm.json")
	if err := os.WriteFile(choice, []byte(`{"offline":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := te.run(context.Background(), "setup", "apply", "llm", "--file", choice, "--workspace", te.ws); o.code != 0 {
		t.Fatalf("setup llm: %+v", o)
	}
	te.startServe()
	setForTest(t, &evaluatePoll, 20*time.Millisecond)

	o := te.evaluate("evaluate", "1", "--text", cliPosting, "--company", "Example Systems", "--title", "Software Engineer", "--json")
	var res evaluate.Result
	if err := json.Unmarshal([]byte(o.stdout), &res); o.code != 0 || err != nil || res.Outcome != evaluate.OutcomeReported || res.ReportNumber == 0 || res.Score == nil {
		t.Fatalf("evaluate: %+v result %+v err %v", o, res, err)
	}
	// Without a row id the plain-text output is the same as with no server.
	o = te.evaluate("evaluate", "--text", cliPosting, "--url", "https://jobs.example.test/p/live2", "--company", "Other Example", "--title", "Platform Engineer", "--mode", "screen")
	if o.code != 0 || !strings.HasPrefix(o.stdout, "screened report 0002") {
		t.Fatalf("screen: %+v", o)
	}
	// Listing the inbox does not need the workspace to itself.
	if got := te.inboxStatus(); got != "evaluated" {
		t.Fatalf("the row is %s", got)
	}
}

type evalBrowser func(context.Context, string) (discovery.PageRead, error)

func (f evalBrowser) Read(ctx context.Context, u string) (discovery.PageRead, error) {
	return f(ctx, u)
}

func TestEvaluateCLI_URLUsesManagedReader(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatal(o)
	}
	var out, errw syncBuffer
	e := te.env(&out, &errw)
	e.llmOptions = []llm.Option{llm.WithFake(evaluate.NewFake()), llm.WithOfflineSimulation()}
	reads := 0
	e.livenessReader = evalBrowser(func(_ context.Context, u string) (discovery.PageRead, error) {
		reads++
		return discovery.PageRead{Status: 200, FinalURL: u, Title: "Software Engineer", Heading: "Software Engineer", Text: cliPosting, HasApply: true}, nil
	})
	code := e.run(context.Background(), []string{"evaluate", "https://careers.example.test/jobs/12345", "--company", "Example Systems", "--json", "--workspace", te.ws})
	var res evaluate.Result
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil || code != 0 || reads != 1 || res.Score == nil || res.Outcome != evaluate.OutcomeReported {
		t.Fatalf("code=%d reads=%d result=%+v out=%s err=%s", code, reads, res, out.String(), errw.String())
	}
}
