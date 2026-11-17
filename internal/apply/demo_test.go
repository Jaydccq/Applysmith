package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// markDemo puts the demo marker beside the rig's database, which is where a
// demo workspace keeps it.
func markDemo(t *testing.T, st *store.Store) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(st.Path()), workspace.DemoMarkerName), []byte("demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !st.Demo() {
		t.Fatal("the store does not see the marker")
	}
}

// Scenarios "The demo never submits" and "Explicit submit in the demo".
func TestDemo_TheDemoNeverSubmitsEvenOnAnExplicitRequest(t *testing.T) {
	for _, caller := range []gates.Caller{gates.CallerAskAgent, gates.CallerCLI, gates.CallerDashboard} {
		t.Run(string(caller), func(t *testing.T) {
			x := newSubmit(t, nil)
			markDemo(t, x.g.st)
			x.e.Run.Input.Caller, x.e.Run.Input.Submit, x.e.Run.Input.ExplicitRequest = caller, true, true
			x.e.Run.Input.UserMessage = "apply to this posting and submit it"
			if stp := x.submit(); stp != nil {
				t.Fatalf("the demo stopped instead of ending as a dry run: %+v", stp)
			}
			if n := x.tab.clicksOf("Submit application"); n != 0 {
				t.Fatalf("Submit was pressed %d time(s)", n)
			}
			if n := x.g.count(`SELECT count(*) FROM submissions`); n != 0 {
				t.Fatalf("%d ledger rows in the demo", n)
			}
			if n := x.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'submit_authorization' AND verdict = 'deny' AND code = 'demo_workspace'`); n != 1 {
				t.Fatalf("%d submit authorization refusals with demo_workspace, want 1", n)
			}
			if l := stageOf(x.e.Run, StageSubmit); l.Status != StatusSkipped || !strings.HasPrefix(l.Detail, "dry run: demo_workspace") {
				t.Fatalf("submit stage %+v, want a dry run naming demo_workspace", l)
			}
		})
	}
}

// demoSolver is the test service's solver as the real factory builds it in a
// demo workspace.
func (f *twoCaptcha) demoSolver() func(*Exec, func(gates.Decision)) captcha.Solver {
	return func(e *Exec, audit func(gates.Decision)) captcha.Solver {
		var cm sync.Mutex
		clock := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
		return captcha.New(captcha.Config{
			Demo: true, Now: func() time.Time { cm.Lock(); defer cm.Unlock(); return clock },
			Run: solveRunKind(e), Switch: gates.SolverOn, Key: func() string { return "k" + strings.Repeat("x", 24) },
			Skip: func() gates.SkipStatus { return gates.SkipNotListed }, Audit: audit, BaseURL: f.srv.URL, HTTP: f.srv.Client(),
		})
	}
}

// Scenario "A listed bot-manager page in the demo".
func TestDemo_ABotManagerPageBuysNoSolveAndEndsAsABotCheck(t *testing.T) {
	svc := newTwoCaptcha(t)
	wall := &PageState{URL: "https://validate.perfdrive.com/?ssa=1", Title: "Checking", Text: "Please complete the check", Lines: []string{"Please complete the check"},
		Captcha: []CaptchaW{{Kind: "hcaptcha", SiteKey: "k", Visible: true, W: 304, H: 78}}, Controls: []Control{{I: 0, Tag: "button", Labels: []string{"Submit"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, wall)
	markDemo(t, w.g.st)
	w.g.r.d.Page.Solver = svc.demoSolver()
	res, st := w.walk()
	if st != nil || res == nil || res.End != EndBotChallenge {
		t.Fatalf("end %+v stop %+v, want a bot check", res, st)
	}
	if svc.calls != 0 || len(w.tab.clicks) != 0 || len(w.tab.delivered) != 0 {
		t.Fatalf("service calls %d, clicks %v, tokens %v: the demo reached the solver or pressed", svc.calls, w.tab.clicks, w.tab.delivered)
	}
	if !strings.Contains(res.Detail, "demo workspace") {
		t.Fatalf("the bot check reads %q, want the demo named", res.Detail)
	}
	if w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'captcha_solve' AND code = 'demo_workspace'`) == 0 {
		t.Fatal("the refused solve was not audited with demo_workspace")
	}
}

// The real factory reads the marker from the workspace itself.
func TestDemo_TheSolverFactoryReadsTheMarker(t *testing.T) {
	v, err := vault.New("solver-ws", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	ws := workspace.Open(t.TempDir())
	if err := ws.MarkDemo(); err != nil {
		t.Fatal(err)
	}
	src := SolverSource{Vault: v, Workspace: ws, Profile: func() (workspace.Profile, bool) { return workspace.Profile{}, true }}
	sv := SolverFactory(src)(&Exec{Run: &Run{Input: Input{Submit: true, ExplicitRequest: true, Company: "Example Corp"}}}, func(gates.Decision) {})
	_, err = sv.Solve(context.Background(), captcha.Challenge{Kind: gates.ChallengeRecaptchaV2, Point: gates.PointBeforePress, SiteKey: "k", PageURL: "https://jobs.example.test/p/1"})
	var ref *captcha.Refusal
	if !errors.As(err, &ref) || ref.Decision.Code != gates.CodeDemoWorkspace {
		t.Fatalf("err = %v, want a demo_workspace refusal", err)
	}
}

// Scenario "The demo uploads the persona's document", at the document
// binding: a page on another host and a page on this computer both get the
// fictional résumé, and each upload is audited.
func TestDemo_ThePersonasDocumentIsUploadedToAnyPageAndAudited(t *testing.T) {
	pdf := &engine.ResumePayload{FileName: "resume.pdf", MediaType: "application/pdf", ContentBase64: "JVBERg=="}
	g := newRig(t, func(d *Deps) {
		d.Page.ProfileResume = func(context.Context, *ReportBinding) (*engine.ResumePayload, string, error) {
			p := *pdf
			return &p, "the default", nil
		}
	})
	markDemo(t, g.st)
	e := &Exec{r: g.r, Run: &Run{ID: "run_u", StartedAt: t0, Input: Input{URL: pageURL, Caller: gates.CallerCLI}}, Ctx: context.Background()}
	docs := newDocResumes(g.r, e)
	for _, origin := range []string{"https://boards.greenhouse.io", "http://127.0.0.1:47319"} {
		got, err := docs.Resume(context.Background(), engine.ResumeRequest{RunID: "run_u", Document: "resume", Origin: origin})
		if err != nil || got == nil {
			t.Fatalf("%s got no document (%v)", origin, err)
		}
	}
	if n := g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'allow' AND target_host = 'boards.greenhouse.io' AND run_id = 'run_u'`); n != 1 {
		t.Fatalf("%d audited uploads to the real host, want 1", n)
	}
	if s := docs.skips(); len(s) != 0 {
		t.Fatalf("skips %v", s)
	}
}
