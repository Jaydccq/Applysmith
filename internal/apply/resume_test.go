package apply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/ledger"
)

// adoptWindow is an apply window that takes over a kept tab.
type adoptWindow struct {
	tab     Tab
	err     error
	adopted []string
}

func (w *adoptWindow) OpenTab(context.Context, string, engine.Host) (Tab, error) {
	return nil, errors.New("a resumed run opens no tab")
}
func (w *adoptWindow) Reclaim(_ context.Context, id string, _ engine.Host) (Tab, error) {
	w.adopted = append(w.adopted, id)
	return w.tab, w.err
}

// plainWindow cannot take a tab over.
type plainWindow struct{}

func (plainWindow) OpenTab(context.Context, string, engine.Host) (Tab, error) { return nil, nil }

func resumeIn(id string) ResumeInput {
	return ResumeInput{RunID: id, TabID: "kept", Input: Input{URL: pageURL, Caller: gates.CallerAskAgent, Tool: ToolAutofill, ExplicitRequest: true, ConversationID: "chat_1"}}
}

// A kept tab that is gone, or a window that cannot take a tab over, blocks
// the resumed run before anything is read.
func TestApply_Resume_ATabThatCannotBeTakenOverBlocksTheRunAsLaneUnavailable(t *testing.T) {
	gone := &adoptWindow{err: errors.New("no such tab")}
	for _, c := range []struct {
		name   string
		window Window
	}{{"the tab is gone", gone}, {"the window cannot take a tab over", plainWindow{}}} {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, nil)
			g.env.window = c.window
			read := 0
			in := resumeIn("run_1")
			in.Read = func(PageGlance) { read++ }
			run := g.r.Resume(context.Background(), in)
			if run.ID != "run_1" || run.Outcome.Kind != OutcomeStopped || run.Outcome.Board != "lane-unavailable" || run.Outcome.Class != ClassEnvironment || read != 0 {
				t.Fatalf("outcome %+v read %d", run.Outcome, read)
			}
		})
	}
	if len(gone.adopted) != 1 || gone.adopted[0] != "kept" {
		t.Fatalf("adopted %v", gone.adopted)
	}
}

func TestApply_Resume_ARunKeepsItsIdAndItsEarlierStagesAndRecordsOneRow(t *testing.T) {
	g := newRig(t, nil)
	first := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
	// The first run stopped at the account stage for want of a window.
	if first.Outcome.Kind != OutcomeStopped {
		t.Fatalf("first run %+v", first.Outcome)
	}
	g.env.window = &adoptWindow{err: errors.New("no such tab")}
	g.now = t0.Add(time.Hour) // the resume comes later; the run keeps its first start
	run := g.r.Resume(context.Background(), resumeIn(first.ID))
	if run.ID != first.ID || !run.StartedAt.Equal(first.StartedAt) {
		t.Fatalf("id %s started %v, want %s %v", run.ID, run.StartedAt, first.ID, first.StartedAt)
	}
	if n := g.count(`SELECT count(*) FROM apply_runs WHERE run_id = ?`, first.ID); n != 1 {
		t.Fatalf("%d rows for the run", n)
	}
	preflights := 0
	for _, l := range run.Stages {
		if l.Stage == StagePreflight {
			preflights++
		}
		if l.Status == StatusSkipped && (l.Stage == StageRecord || l.Stage == StageLearn) {
			t.Errorf("a stale %s line survived: %+v", l.Stage, l)
		}
	}
	if preflights != 2 {
		t.Fatalf("%d preflight lines; the earlier and the resumed one should both be there", preflights)
	}
}

// linesSolver is a solver that only reports its detail lines.
type linesSolver struct {
	captcha.Solver
	lines []string
}

func (l linesSolver) Detail() []string { return l.lines }

// A resumed run is the same run: what its earlier leg recorded of sign-ins,
// pages and captcha solves stays in its row, so the account it created is
// still on record.
func TestApply_Resume_ARunKeepsTheSignInsPagesAndCaptchaLinesItsEarlierLegRecorded(t *testing.T) {
	g := newRig(t, nil)
	first := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
	if err := g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE apply_runs SET signins_json = ?, pages_json = ?, captcha_json = ? WHERE run_id = ?`,
			`[{"host":"careers.example.test","tried":["created-account"],"succeeded":"created-account"}]`,
			`[{"url":"https://careers.example.test/apply/1","step":1,"filled":4},{"url":"https://careers.example.test/apply/2","step":2,"filled":2,"unresolved":["Visa status: no data"]}]`,
			`["captcha: solved recaptcha_v2 before the press"]`, first.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g.env.window = &adoptWindow{err: errors.New("no such tab")}
	run := g.r.Resume(context.Background(), resumeIn(first.ID))
	if len(run.Signins) != 1 || run.Signins[0].Succeeded != "created-account" || len(run.Pages) != 2 || len(run.Captcha) != 1 {
		t.Fatalf("signins %+v pages %d captcha %v: the earlier leg's record is gone", run.Signins, len(run.Pages), run.Captcha)
	}
	if n := g.count(`SELECT count(*) FROM apply_runs WHERE run_id = ? AND signins_json LIKE '%"created-account"%' AND pages_json LIKE '%Visa status%' AND captcha_json LIKE '%recaptcha_v2%'`, first.ID); n != 1 {
		t.Fatal("the resumed run's row lost what the earlier leg recorded")
	}

	// The first page the resumed leg reads is the one the run stopped on: its
	// fresh report takes the stale one's place, and the pages before it stay.
	e := &Exec{r: g.r, Run: run, Ctx: context.Background(), reread: len(run.Pages), solved: run.Captcha}
	s := &session{r: g.r, e: e}
	s.addReport(PageReport{URL: "https://careers.example.test/apply/2", Step: 1, Filled: 3})
	s.addReport(PageReport{URL: "https://careers.example.test/apply/2", Step: 2})
	if len(run.Pages) != 3 || run.Pages[0].Filled != 4 || run.Pages[1].Filled != 3 || len(run.Pages[1].Unresolved) != 0 {
		t.Fatalf("pages %+v", run.Pages)
	}
	// A solve of the resumed leg is recorded after the earlier leg's.
	e.solver = linesSolver{lines: []string{"captcha: solved turnstile at the gate"}}
	g.r.captchaLines(e)
	if len(run.Captcha) != 2 || !strings.Contains(run.Captcha[0], "recaptcha_v2") || !strings.Contains(run.Captcha[1], "turnstile") {
		t.Fatalf("captcha %v", run.Captcha)
	}
}

func TestApply_Resume_ASignInThatStillStandsIsNeverSignedInAgain(t *testing.T) {
	g := newRig(t, nil)
	tab := newScriptTab(loginPage(""))
	g.env.window = &adoptWindow{tab: tab}
	var seen PageGlance
	in := resumeIn("run_1")
	in.Read = func(p PageGlance) { seen = p }
	run := g.r.Resume(context.Background(), in)
	if run.Outcome.Kind != OutcomeStopped || run.Outcome.Class != ClassCredential || run.Outcome.Board != "sign-in" || run.Outcome.Stage != StageAccount {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if !seen.SignIn || len(tab.typed) != 0 || len(tab.clicks) != 0 {
		t.Fatalf("glance %+v typed %v clicked %v: nothing may be typed or pressed", seen, tab.typed, tab.clicks)
	}
	if !tab.kept {
		t.Error("the tab was not kept for the user")
	}
	if run.Signins != nil {
		t.Errorf("a sign-in attempt was recorded: %+v", run.Signins)
	}
}

// A panic in a resumed run ends it as a defect, as one in a first run does:
// the server goes on and the run record does not stay "running".
func TestApply_Resume_APanicEndsTheRunAsADefect(t *testing.T) {
	g := newRig(t, nil)
	g.env.window = &adoptWindow{tab: newScriptTab(appPage())}
	in := resumeIn("run_1")
	in.Read = func(PageGlance) { panic("boom") }
	run := g.r.Resume(context.Background(), in)
	if run.Outcome.Kind != OutcomeStopped || run.Outcome.Class != ClassDefect || !strings.Contains(run.Outcome.Reason, "internal error: boom") {
		t.Fatalf("outcome %+v", run.Outcome)
	}
}

func TestApply_Resume_ThePageIsReadFreshAndSummarisedWithoutAValue(t *testing.T) {
	st := appPage()
	st.URL = "https://careers.example.test/jobs/7?token=SECRET-TOKEN#frag"
	st.Headings = []string{"Platform Engineer", "Your details", "Questions", "Fourth"}
	g := glanceOf(st)
	sum := g.Summary()
	if strings.Contains(sum, "SECRET-TOKEN") || strings.Contains(sum, "frag") {
		t.Errorf("the summary carries the query: %q", sum)
	}
	if len(g.Headings) != 3 || g.Controls != 1 || g.SignIn || !strings.Contains(sum, "Platform Engineer") || !strings.Contains(sum, "1 form control") {
		t.Errorf("glance %+v summary %q", g, sum)
	}
	if got := glanceOf(loginPage("")).Summary(); !strings.Contains(got, "still shows a sign-in") {
		t.Errorf("a sign-in page: %q", got)
	}
}

func TestApply_Resume_TheSubmitGateIsDecidedAgainAndAnythingButAnAllowHoldsThePress(t *testing.T) {
	cases := []struct {
		name  string
		rs    *ResumeInput
		hold  string
		audit int
	}{
		{"no resume", nil, "", 0},
		{"the gate allows", &ResumeInput{Recheck: func(context.Context) gates.Decision {
			return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.Allow, Code: "ok"}
		}}, "", 1},
		{"the gate allows with a trace", &ResumeInput{Recheck: func(context.Context) gates.Decision {
			return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.AllowWithTrace, Code: "confirmed:card"}
		}}, "", 1},
		{"the gate holds", &ResumeInput{Recheck: func(context.Context) gates.Decision {
			return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.Hold, Code: "host_not_named", Reason: "the posting was not named"}
		}}, "the posting was not named", 1},
		{"the gate denies", &ResumeInput{Recheck: func(context.Context) gates.Decision {
			return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.Deny, Code: "x", Reason: "denied"}
		}}, "denied", 1},
		{"Telegram", &ResumeInput{HoldSubmit: "resumed from Telegram"}, "resumed from Telegram", 0},
		{"the gate allows but Telegram resumed", &ResumeInput{HoldSubmit: "resumed from Telegram", Recheck: func(context.Context) gates.Decision {
			return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.Allow, Code: "ok"}
		}}, "resumed from Telegram", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, nil)
			e := &Exec{r: g.r, Run: &Run{ID: "run_1", Input: Input{URL: pageURL}}, Ctx: context.Background(), resume: c.rs}
			if got := g.r.resumeHold(context.Background(), e); got != c.hold {
				t.Fatalf("hold %q, want %q", got, c.hold)
			}
			if n := g.count(`SELECT count(*) FROM gate_decisions WHERE run_id = 'run_1'`); n != c.audit {
				t.Fatalf("%d audit rows, want %d", n, c.audit)
			}
		})
	}
}

func TestApply_Resume_AHeldRunEndsFilledWithTheConfirmationReasonAndKeepsTheTab(t *testing.T) {
	g := newRig(t, nil)
	e := &Exec{r: g.r, Run: &Run{ID: "run_1", Input: Input{URL: pageURL}, Outcome: Outcome{Kind: outcomeRunning}}, Ctx: context.Background()}
	e.held = "resumed from Telegram, so Submit is confirmed in the dashboard"
	g.r.finish(e)
	o := e.Run.Outcome
	if o.Kind != OutcomeFilled || o.Board != HoldConfirmation || !strings.Contains(o.Reason, "dashboard") {
		t.Fatalf("outcome %+v", o)
	}
}

// ---- what the user changed by hand

func handoffRows(vals map[string]string) func() []engine.InspectRow {
	return func() []engine.InspectRow {
		var out []engine.InspectRow
		for _, sel := range []string{"#city", "#state", "#note", "#zip"} {
			if v, ok := vals[sel]; ok {
				out = append(out, engine.InspectRow{Selector: sel, Label: sel, Kind: "text", Value: v})
			}
		}
		return out
	}
}

func keptSession(t *testing.T, vals map[string]string) (*rig, *session, *fakePage) {
	t.Helper()
	g := newRig(t, nil)
	page := &fakePage{rows: handoffRows(vals)}
	e := &Exec{r: g.r, Run: &Run{ID: "run_1", Input: Input{URL: pageURL}}, Ctx: context.Background()}
	return g, &session{r: g.r, e: e, page: page}, page
}

func TestApply_Resume_AFieldTheUserChangedIsKeptAndTheRestIsStillTheRuns(t *testing.T) {
	for _, c := range []struct{ name, frame string }{
		{"the form is the page", ""},
		{"the form is in a frame of an empty page", "app"},
	} {
		t.Run(c.name, func(t *testing.T) {
			vals := map[string]string{"#city": "Durham", "#state": "NC", "#note": ""}
			g, s, page := keptSession(t, vals)
			if c.frame != "" {
				page.rows = func() []engine.InspectRow { return nil }
				page.frames = map[string]func() []engine.InspectRow{c.frame: handoffRows(vals)}
				s.covered, s.frame = []string{c.frame}, c.frame
			}
			s.saveHandoff(context.Background())
			var stored string
			if err := g.st.Read().QueryRow(`SELECT fields FROM apply_handoffs WHERE run_id = 'run_1'`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stored, "Durham") || strings.Contains(stored, "NC") {
				t.Fatalf("the snapshot stores a value: %s", stored)
			}

			// The user changed the city, typed a note and the site prefilled a zip.
			vals["#city"], vals["#note"], vals["#zip"] = "Raleigh", "I can start in June", "27701"
			g.r.protectEdits(context.Background(), s.e, s)
			keep := s.keep[c.frame]
			if len(keep) != 3 || keep["#city"] != "Raleigh" || keep["#note"] != "I can start in June" || keep["#zip"] != "27701" {
				t.Fatalf("kept %v", s.keep)
			}
			if _, mine := keep["#state"]; mine {
				t.Fatal("a field the run left alone was taken for the user's")
			}

			got, err := s.answer(context.Background(), "er1", []engine.Resolution{
				{Selector: "#city", Outcome: "filled", Value: "Durham"}, {Selector: "#state", Outcome: "filled", Value: "NC"}})
			if err != nil {
				t.Fatal(err)
			}
			by := map[string]string{}
			for _, r := range got {
				by[r.Selector] = r.Outcome + "/" + r.Reason
			}
			if by["#city"] != "skipped/user-edited" || by["#state"] != "filled/" {
				t.Fatalf("outcomes %v", by)
			}
			if len(page.answers) != 1 || len(page.answers[0]) != 1 || page.answers[0][0].Selector != "#state" {
				t.Fatalf("the page was written: %+v", page.answers)
			}
		})
	}
}

func TestApply_Resume_AFieldTheUserChangedStaysTheirsWhenTheResumedRunIsKeptAgain(t *testing.T) {
	vals := map[string]string{"#city": "Durham", "#state": "NC"}
	g, s, page := keptSession(t, vals)
	s.saveHandoff(context.Background())
	// The user empties City and presses Done; the resumed run leaves it empty,
	// stops again before it fills a page and records the page as it hands the
	// tab over once more.
	vals["#city"] = ""
	next := &session{r: g.r, e: s.e, page: page}
	g.r.protectEdits(context.Background(), next.e, next)
	next.saveHandoff(context.Background())
	last := &session{r: g.r, e: s.e, page: page}
	g.r.protectEdits(context.Background(), last.e, last)
	if v, mine := last.keep[""]["#city"]; !mine || v != "" || len(last.keep[""]) != 1 {
		t.Fatalf("kept %v: the next leg takes City, and only City, for the user's", last.keep)
	}
}

func TestApply_Resume_WithoutASnapshotEveryFilledFieldIsTheUsers(t *testing.T) {
	g, s, _ := keptSession(t, map[string]string{"#city": "Raleigh", "#note": ""})
	g.r.protectEdits(context.Background(), s.e, s)
	keep := s.keep[""]
	if len(keep) != 1 || keep["#city"] != "Raleigh" {
		t.Fatalf("kept %v", keep)
	}
}

func TestApply_Resume_NothingIsKeptForARunThatWasNotResumed(t *testing.T) {
	_, s, page := keptSession(t, map[string]string{"#city": "Durham"})
	got, _ := s.answer(context.Background(), "er1", []engine.Resolution{{Selector: "#city", Outcome: "filled", Value: "Raleigh"}})
	if len(got) != 1 || got[0].Outcome != "filled" || len(page.answers) != 1 {
		t.Fatalf("got %+v answers %+v", got, page.answers)
	}
}

func TestApply_Resume_ARunHeldBeforeSubmitKeepsItsTabForTheUser(t *testing.T) {
	g := newRig(t, func(d *Deps) { d.Page.Keep = &Keeper{} })
	for _, c := range []struct {
		name string
		held string
		kept bool
	}{{"held", "the gate holds it", true}, {"finished", "", false}} {
		t.Run(c.name, func(t *testing.T) {
			tab := newScriptTab(appPage())
			e := &Exec{r: g.r, Run: &Run{ID: "run_1", Input: Input{URL: pageURL}, Outcome: Outcome{Kind: outcomeRunning}}, Ctx: context.Background(), held: c.held}
			s := &session{r: g.r, e: e, tab: tab, page: tab.Page()}
			s.release(context.Background())
			if tab.kept != c.kept || tab.closed == c.kept {
				t.Fatalf("kept %v closed %v, want kept %v", tab.kept, tab.closed, c.kept)
			}
		})
	}
}

// The per-run bounds hold across Done: a resumed run continues the solves, the
// verification-email presses and the password resets its earlier leg spent,
// and records them again when it ends.
func TestApply_Resume_PerRunBoundsCarryAcrossDone(t *testing.T) {
	g := newRig(t, nil)
	first := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
	const spent = `{"solves":2,"resends":1,"resets":{"example.wd5.myworkdayjobs.com":1}}`
	if err := g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE apply_runs SET bounds_json = ? WHERE run_id = ?`, spent, first.ID)
		return err
	}); err != nil {
		t.Fatalf("the earlier leg's bounds could not be recorded: %v", err)
	}
	var e *Exec
	svc := newTwoCaptcha(t)
	g.r.d.Page.Solver = func(x *Exec, audit func(gates.Decision)) captcha.Solver {
		e = x
		return svc.solver(gates.RunApplySubmit, gates.SkipNotListed)(x, audit)
	}
	g.env.window = &adoptWindow{err: errors.New("no such tab")}
	g.r.Resume(context.Background(), resumeIn(first.ID))
	var got string
	if err := g.st.Read().QueryRow(`SELECT bounds_json FROM apply_runs WHERE run_id = ?`, first.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var b runBounds
	if err := json.Unmarshal([]byte(got), &b); err != nil || b.Solves != 2 || b.Resends != 1 || b.Resets["example.wd5.myworkdayjobs.com"] != 1 {
		t.Fatalf("bounds after the resumed leg %s (%v): the counts started from zero", got, err)
	}

	// A solver built for a resumed leg that already spent the cap asks the
	// service for nothing.
	x := &Exec{r: g.r, Run: &Run{ID: first.ID, Input: Input{Submit: true, ExplicitRequest: true, Company: "Example Corp"}}, Ctx: context.Background(), spent: 2}
	sv := g.r.solver(x)
	if sv == nil || e != x {
		t.Fatal("no solver was built")
	}
	if _, err := sv.Solve(context.Background(), captcha.Challenge{Kind: gates.ChallengeRecaptchaV2, Point: gates.PointBeforePress, SiteKey: "k", PageURL: pageURL}); err == nil || svc.calls != 0 {
		t.Fatalf("a third solve in one run reached the service (%d calls, err %v)", svc.calls, err)
	}
}

func TestApply_StartRequest_IsRecordedReplacedReadBackAndPruned(t *testing.T) {
	g := newRig(t, nil)
	ctx := context.Background()
	q := StartRequest{RunID: "run_1", Caller: gates.CallerDashboard, Tool: ToolAutofill, Explicit: true, URL: pageURL + "?gh_jid=7",
		Company: "Example Corp", Role: "Platform Engineer", Named: true}
	if err := SaveRequest(ctx, g.st, q, t0); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadRequest(ctx, g.st, "run_1"); err != nil || got != q {
		t.Fatalf("read back %+v %v, want %+v", got, err, q)
	}
	// A confirmed "Submit this form" replaces the request; the record keeps its age.
	q.Submit, q.Caller = true, gates.CallerCLI
	if err := SaveRequest(ctx, g.st, q, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRequest(ctx, g.st, "run_1")
	if in := got.Input(); err != nil || got != q || !in.Submit || !in.ExplicitRequest || in.Caller != gates.CallerCLI || in.URL != q.URL || !in.NamedRetry {
		t.Fatalf("replaced %+v %v", got, err)
	}
	if err := PruneRequests(ctx, g.st, t0.Add(RequestRetention-time.Minute)); err != nil || g.count(`SELECT count(*) FROM apply_requests`) != 1 {
		t.Fatalf("a request inside the retention was pruned: %v", err)
	}
	if err := PruneRequests(ctx, g.st, t0.Add(RequestRetention+time.Minute)); err != nil || g.count(`SELECT count(*) FROM apply_requests`) != 0 {
		t.Fatalf("a request past the retention stayed: %v", err)
	}
}

// ---- a filled dry run of the user, then "Submit this form"

// keptFormWindow opens the scripted tab for a run and takes the same tab over
// when the run goes on.
type keptFormWindow struct{ tab *scriptTab }

func (w keptFormWindow) OpenTab(context.Context, string, engine.Host) (Tab, error) { return w.tab, nil }
func (w keptFormWindow) Reclaim(context.Context, string, engine.Host) (Tab, error) { return w.tab, nil }

const profileEmail = "alex@example.test"

// filledDryRun is a dry run on a one-page form that fills it and reaches its
// Submit, with the applicant's profile known to the identity review; batch
// names the list it is a row of.
func filledDryRun(t *testing.T, caller gates.Caller, batch string) (*rig, *scriptTab, *Run) {
	t.Helper()
	g := newRig(t, func(d *Deps) {
		d.Page.Keep = &Keeper{}
		d.Page.Timing = WalkTiming{AfterForm: 100 * time.Millisecond, First: 50 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 120 * time.Millisecond}
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"candidate":{"fullName":"Alex Example","email":"` + profileEmail + `"}}`), nil
		}
	})
	form := formState()
	form.URL = pageURL
	tab := newScriptTab(form)
	tab.page, tab.watch = &fakePage{}, &fakeWatch{}
	g.env.window = keptFormWindow{tab}
	run := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: caller, Tool: ToolAutofill, ExplicitRequest: true, Company: "Example Corp", Role: "Platform Engineer",
		BatchID: batch})
	return g, tab, run
}

// submitThisForm is the input a confirmed "Submit this form" resumes the run with.
func submitThisForm(run *Run, tab *scriptTab) ResumeInput {
	return ResumeInput{RunID: run.ID, TabID: tab.ID(), Input: Input{URL: pageURL, Submit: true, ExplicitRequest: true, Caller: gates.CallerDashboard, Tool: ToolAutofill,
		Company: "Example Corp", Role: "Platform Engineer"}}
}

func TestApply_Resume_AFilledDryRunTheUserStartedKeepsItsTabAtSubmitForTheirConfirmation(t *testing.T) {
	for _, c := range []struct {
		name   string
		caller gates.Caller
		batch  string
		kept   bool
	}{
		{"the dashboard", gates.CallerDashboard, "", true},
		{"the command line", gates.CallerCLI, "", true},
		{"a row of the user's list", gates.CallerDashboard, "batch_1", false},
		{"the Ask Agent", gates.CallerAskAgent, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, tab, run := filledDryRun(t, c.caller, c.batch)
			o := run.Outcome
			if o.Kind != OutcomeFilled || len(tab.clicks) != 0 || g.count(`SELECT count(*) FROM submissions`) != 0 {
				t.Fatalf("outcome %+v clicks %v: a dry run presses nothing", o, tab.clicks)
			}
			_, inKeeper := g.r.Keeper().TabOf(run.ID)
			held := o.Board == HoldConfirmation
			if held != c.kept || tab.kept != c.kept || tab.closed == c.kept || inKeeper != c.kept {
				t.Fatalf("outcome %+v, tab kept %v closed %v, in the keeper %v; want kept %v", o, tab.kept, tab.closed, inKeeper, c.kept)
			}
		})
	}
}

func TestApply_Resume_SubmitThisFormChecksTheFormAgainAndPressesSubmitOnce(t *testing.T) {
	g, tab, first := filledDryRun(t, gates.CallerDashboard, "")
	tab.onClick = func(t *scriptTab, c Control) {
		t.watch.add(post(200, `{"ok":true}`))
		t.set(thanks())
	}
	run := g.r.Resume(context.Background(), submitThisForm(first, tab))
	if run.ID != first.ID || run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed {
		t.Fatalf("outcome %+v; stages %+v", run.Outcome, run.Stages)
	}
	pressed := 0
	for _, c := range tab.clicks {
		if c == "Submit application" {
			pressed++
		}
	}
	if pressed != 1 || len(tab.clicks) != 1 {
		t.Fatalf("clicks %v", tab.clicks)
	}
	if n := g.count(`SELECT count(*) FROM apply_runs WHERE run_id = ? AND submit = 1 AND outcome_kind = 'submitted'`, first.ID); n != 1 {
		t.Fatal("the run's record does not show the leg that submitted")
	}
}

// Each pre-submit check is decided again on the resumed leg, and one that
// fails keeps the press from happening.
func TestApply_Resume_SubmitThisFormIsRefusedWhenAPreSubmitCheckFails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(g *rig, tab *scriptTab, in *ResumeInput)
		stage Stage
		words string
	}{
		{"the duplicate guard: the posting was submitted meanwhile", func(g *rig, _ *scriptTab, _ *ResumeInput) {
			if _, err := g.ledger.RecordObserved(context.Background(), ledger.Observation{EntryID: "e1", URL: pageURL, Host: "careers.example.test", Company: "Example Corp",
				Role: "Platform Engineer", ObservedAt: t0, Confirmed: true}); err != nil {
				t.Fatal(err)
			}
		}, StageResolve, "already submitted"},
		{"the duplicate guard: another run is submitting the posting", func(g *rig, _ *scriptTab, _ *ResumeInput) {
			if _, err := g.ledger.Claim(context.Background(), ledger.ClaimRequest{URL: pageURL, Host: "careers.example.test", Company: "Example Corp", Role: "Platform Engineer",
				Source: ledger.SourceApplyChain, RunID: "run_other", RunStartedAt: t0}); err != nil {
				t.Fatal(err)
			}
		}, StageResolve, "submitting this posting right now"},
		{"the press claim: another run claimed the posting after the guard", func(g *rig, tab *scriptTab, _ *ResumeInput) {
			claimed := false
			tab.probeHook = func(*scriptTab) {
				if !claimed {
					claimed = true
					_, _ = g.ledger.Claim(context.Background(), ledger.ClaimRequest{URL: pageURL, Host: "careers.example.test", Company: "Example Corp", Role: "Platform Engineer",
						Source: ledger.SourceApplyChain, RunID: "run_other", RunStartedAt: t0})
				}
			}
		}, StageSubmit, "submitting this posting right now"},
		{"the identity review: the page holds another email", func(_ *rig, tab *scriptTab, _ *ResumeInput) {
			const other = "someone@example.test"
			tab.page.fills = []*engine.PageResult{doneResult(engine.Row{Selector: "#email", Label: "Email", Kind: "text", Outcome: "prefilled", Value: other})}
			tab.page.rows = func() []engine.InspectRow {
				return []engine.InspectRow{{Selector: "#email", Label: "Email", Kind: "text", Value: other}}
			}
		}, StageCheck, "does not match your profile"},
		{"a required row: a question stands unanswered", func(_ *rig, tab *scriptTab, _ *ResumeInput) {
			tab.page.fills = []*engine.PageResult{doneResult(engine.Row{Selector: "#why", Label: "Why us?", Kind: "textarea", Required: true, Outcome: "abstained", Reason: "no-entry"})}
			tab.page.rows = func() []engine.InspectRow {
				return []engine.InspectRow{{Selector: "#why", Label: "Why us?", Kind: "textarea"}}
			}
		}, StageCheck, "required and not answered"},
		{"the submit authorization: Telegram never submits", func(_ *rig, _ *scriptTab, in *ResumeInput) { in.Input.Caller = gates.CallerTelegram }, StageSubmit, "Telegram cannot submit"},
		{"the submit authorization: no explicit request", func(_ *rig, _ *scriptTab, in *ResumeInput) { in.Input.ExplicitRequest = false }, StageSubmit, "did not explicitly ask"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, tab, first := filledDryRun(t, gates.CallerDashboard, "")
			in := submitThisForm(first, tab)
			c.setup(g, tab, &in)
			run := g.r.Resume(context.Background(), in)
			if run.Outcome.Kind == OutcomeSubmitted || len(tab.clicks) != 0 {
				t.Fatalf("outcome %+v clicks %v: a check failed and Submit was pressed anyway", run.Outcome, tab.clicks)
			}
			if run.Outcome.Stage != c.stage || !strings.Contains(run.Outcome.Reason, c.words) {
				t.Fatalf("stopped %+v, want at %s with %q", run.Outcome, c.stage, c.words)
			}
			if n := g.count(`SELECT count(*) FROM submissions WHERE run_id = ? AND state IN ('submitting', 'submitted')`, first.ID); n != 0 {
				t.Fatalf("%d claims of the refused leg stand", n)
			}
		})
	}
}
