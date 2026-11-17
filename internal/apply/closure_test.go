package apply

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// downPoster is a learning receiver that is not there.
type downPoster struct{ calls int }

func (p *downPoster) PostEvents(context.Context, []LearnEvent) error {
	p.calls++
	return errors.New("the learning route is down")
}

func eventResult() *engine.PageResult {
	row := engine.Row{Selector: "#a", Label: "Referral code", Outcome: "abstained", Required: true, Reason: "no-answer", Kind: "text"}
	ev := engine.LearningEvent{EventID: "6c6f6f70-0000-5000-8000-000000000001", Kind: "uncertainty", Reason: "abstained", Question: "Referral code", Selector: "#a", URL: "https://jobs.example.test/apply/1", Time: 1}
	return &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{Rows: []engine.Row{row}, Events: []engine.LearningEvent{ev}}}}
}

func TestAPI_AutofillTab_LearningDownDoesNotFail(t *testing.T) {
	run := func(p Poster) (*FillResult, error) {
		g := newRig(t, func(d *Deps) { d.Page.Poster = p })
		tab := newScriptTab(fillForm(2))
		tab.page = &fakePage{fills: []*engine.PageResult{eventResult()}, rows: func() []engine.InspectRow { return nil }}
		f, err := g.r.OpenFill(context.Background(), &activeFake{tab: tab}, "script")
		if err != nil {
			t.Fatal(err)
		}
		return f.Run(context.Background(), "run_test")
	}
	down := &downPoster{}
	res, err := run(down)
	if err != nil || down.calls == 0 {
		t.Fatalf("err=%v calls=%d: the events were not offered or the fill failed on them", err, down.calls)
	}
	if res.Events != 0 || len(res.Missing) != 1 || res.Missing[0].Label != "Referral code" {
		t.Fatalf("result %+v: a dead learning route changes only the number of accepted events", res)
	}
	// The same fill with a working receiver delivers the unanswered control as
	// an event of the fill, with no report number.
	ok := &fakePoster{limit: 10}
	res, err = run(ok)
	if err != nil || res.Events != 1 || len(ok.got) != 1 {
		t.Fatalf("err=%v events=%d posts=%d", err, res.Events, len(ok.got))
	}
	if ev := ok.got[0][0]; ev.ReportNumber != 0 || ev.RunID != "run_test" || ev.Question != "Referral code" {
		t.Fatalf("event %+v", ev)
	}
}

func TestApply_Gate_LeftoverBecomesRequiredSkip(t *testing.T) {
	x := newSubmit(t, func(d *Deps) { d.Page.Check = CheckTiming{Reread: time.Millisecond, Settle: time.Millisecond} })
	// The pipeline reports this row filled; the page does not keep the value.
	row := engine.Row{Selector: "#d", Label: "Work authorization", Required: true, Outcome: "filled", Value: "No", OptionLabel: "No", Kind: "select"}
	x.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#d", Label: "Work authorization", Required: true, Kind: "select", Value: "Select One"}}
	}
	ps := &pageState{}
	x.s.check(context.Background(), doneResult(row), ps)
	if len(ps.notHeld) != 1 {
		t.Fatalf("notHeld %+v", ps.notHeld)
	}
	// The submit gate reads the same findings the check counted: the row the
	// pipeline called filled blocks the press.
	verdict := gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{ps.gatesReport(1)}, Final: true})
	if verdict.Allowed() {
		t.Fatalf("verdict %+v: a required write the page did not keep passed the submit gate", verdict)
	}
	// A required row nobody answered is counted the same way.
	open := engine.Row{Selector: "#e", Label: "Referral code", Required: true, Outcome: "abstained", Reason: "no-answer", Kind: "text"}
	y := newSubmit(t, nil)
	ps2 := &pageState{}
	y.s.check(context.Background(), doneResult(open), ps2)
	if len(ps2.leftovers) != 1 || gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{ps2.gatesReport(1)}, Final: true}).Allowed() {
		t.Fatalf("leftovers %+v", ps2.leftovers)
	}
}

func TestApply_Page_LegalSettingIsReadAtEveryPage(t *testing.T) {
	on := false
	x := newSubmit(t, func(d *Deps) { d.Page.Settings = func() Settings { return Settings{AutoAcceptLegalDeclarations: on} } })
	if x.s.fillOptions(formState()).AcceptLegalDeclarations {
		t.Fatal("the setting is off by default")
	}
	on = true
	if !x.s.fillOptions(formState()).AcceptLegalDeclarations {
		t.Fatal("a toggle between two pages did not reach the next page")
	}
	y := newSubmit(t, nil)
	if y.s.fillOptions(formState()).AcceptLegalDeclarations {
		t.Fatal("an absent setting means no")
	}
}

func TestApplyChain_GreenhouseEmbedBoardIndexIsNotAnApplication(t *testing.T) {
	for _, u := range []string{
		"https://job-boards.greenhouse.io/embed/job_board?for=example-corp",
		"https://job-boards.greenhouse.io/embed/job_board?for=example-corp&token=1234567",
		"https://job-boards.greenhouse.io/embed/job_app?for=example-corp",
		"https://job-boards.greenhouse.io/embed/job_app?token=1234567",
		"https://job-boards.greenhouse.io/embed",
	} {
		if _, s := Classify(u); s == nil || s.class != ClassPosting || s.reason != "not an application URL" {
			t.Errorf("%s: stop %+v", u, s)
		}
	}
	cls, s := Classify("https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567")
	if s != nil || cls.ATS != ATSGreenhouse || cls.Tenant != "example-corp" {
		t.Fatalf("the application page of the embed: %+v %+v", cls, s)
	}
}

const ashbyBouncedBody = `{"data":{"submitSingleApplicationForm":{"__typename":"FormRender","formRender":{"sections":[]},"errorMessages":["Missing entry for required field: Why do you want to work here?","Missing entry for required field: Notice period","Missing entry for required field: Expected sal`

const ashbyAcceptedBody = `{"data":{"submitSingleApplicationForm":{"__typename":"FormSubmitSuccess","applicationFormResult":{"id":"x"}}}}`

func ashbyGraphql(status int, body string) browser.Response {
	return browser.Response{Method: "POST", URL: "https://jobs.ashbyhq.com/api/non-user-graphql", Status: status, Body: body}
}

func ashbySubmit(t *testing.T, resp browser.Response, page *PageState) (*senv, *stop) {
	t.Helper()
	x := newSubmit(t, nil)
	x.e.Run.ATS = ATSAshby
	x.s.pages[0].rows = nil
	x.tab.onClick = func(t *scriptTab, _ Control) {
		t.watch.add(resp)
		t.set(page)
	}
	return x, x.submit()
}

func TestApplyChain_AshbyBouncedSubmit200IsNotAcceptance(t *testing.T) {
	// HTTP 200 with the form returned: nothing was accepted, whatever the status says.
	x, st := ashbySubmit(t, ashbyGraphql(200, ashbyBouncedBody), formState())
	if st == nil || st.class != ClassDefect || !strings.Contains(st.reason, "returned the form again, so nothing was accepted") {
		t.Fatalf("stop %+v", st)
	}
	if state, _, _ := ledgerState(x.g); state != "failed" || x.e.Run.Outcome.Kind == OutcomeSubmitted {
		t.Fatalf("ledger %s outcome %+v: a bounce is not a submission", state, x.e.Run.Outcome)
	}
	// The success type is acceptance, with nothing else on the page.
	x, st = ashbySubmit(t, ashbyGraphql(200, ashbyAcceptedBody), formState())
	if st != nil || x.e.Run.Outcome.Kind != OutcomeSubmitted || !x.e.Run.Outcome.Confirmed || x.e.Run.Outcome.ConfirmSource != ConfirmATSResponse {
		t.Fatalf("stop %+v outcome %+v", st, x.e.Run.Outcome)
	}
	// Another write that answers 200 without either type is neither a bounce nor an acceptance.
	x, st = ashbySubmit(t, ashbyGraphql(200, `{"data":{"updateForm":{"__typename":"FormSaved"}}}`), thanks())
	if st != nil || x.e.Run.Outcome.ConfirmSource != ConfirmPage {
		t.Fatalf("stop %+v outcome %+v: only the page's own confirmation decides here", st, x.e.Run.Outcome)
	}
	// A bounce that the page also words as validation still gets its one repair round.
	invalid := formState()
	invalid.Validation = []string{"Notice period: Missing entry for required field"}
	_, st = ashbySubmit(t, ashbyGraphql(200, ashbyBouncedBody), invalid)
	if st == nil || !strings.Contains(st.reason, "returned the form again") || !strings.Contains(st.reason, "repair round") {
		t.Fatalf("stop %+v", st)
	}
}

func TestApplyChain_AshbyRequiredLeftoversFromTruncatedBody(t *testing.T) {
	got := ashbyMissing(ashbyBouncedBody)
	want := []string{"Why do you want to work here?", "Notice period"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("questions %q: a body cut inside the third message names the first two and no cut one", got)
	}
	if len(ashbyMissing(`{"errorMessages":["Something else","Missing entry for required field: Phone"]}`)) != 1 || ashbyMissing(`{"data":{}}`) != nil {
		t.Fatal("only the missing-entry messages of the errorMessages array count")
	}
	x, st := ashbySubmit(t, ashbyGraphql(200, ashbyBouncedBody), formState())
	if st == nil {
		t.Fatal("no stop")
	}
	var labels []string
	for _, l := range x.s.pages[0].leftovers {
		labels = append(labels, l.Label)
		if l.Reason != ReasonNeedsUser {
			t.Errorf("leftover %+v", l)
		}
	}
	if len(labels) != 2 || !strings.Contains(st.reason, "Notice period") {
		t.Fatalf("leftovers %v reason %q: each named question is a required leftover", labels, st.reason)
	}
}

func TestApplyChain_AshbyRefusedSubmitRecordsTheRunQuestions(t *testing.T) {
	// The fill wrote "Notice period" (the page showed it, Ashby had not taken
	// it) and never saw "Why do you want to work here?"; the bounce names both.
	x := newSubmit(t, nil)
	x.e.Run.ATS = ATSAshby
	x.e.Run.Pages = []PageReport{{URL: pageURL}}
	notice := engine.Row{Selector: "#notice", Label: "Notice period *", Kind: "select", Required: true, Outcome: "filled", OptionLabel: "2 weeks", Options: []string{"Immediately", "2 weeks", "1 month"}}
	x.s.pages[0].rows = []engine.Row{{Selector: "#first", Label: "First Name", Kind: "text", Required: true, Outcome: "filled", Value: "Ada"}, notice}
	x.tab.onClick = func(t *scriptTab, _ Control) {
		t.watch.add(ashbyGraphql(200, ashbyBouncedBody))
		t.set(formState())
	}
	if st := x.submit(); st == nil || st.at != StageSubmit {
		t.Fatalf("stop %+v", st)
	}
	got := x.e.Run.Pages[0].Leftovers
	if len(got) != 2 {
		t.Fatalf("recorded leftovers %+v: the run record names both refused questions", got)
	}
	n, w := got[0], got[1]
	if n.Label != "Notice period *" || n.Selector != "#notice" || n.Draft == nil || n.Draft.Kind != "select" || strings.Join(n.Draft.Options, "|") != "Immediately|2 weeks|1 month" {
		t.Errorf("notice period %+v %+v: the walk's row gives the kind and options", n, n.Draft)
	}
	if w.Label != "Why do you want to work here?" || w.Reason != ReasonNeedsUser || w.Draft == nil || w.Draft.Kind != "text" {
		t.Errorf("why here %+v %+v: a question no row carries is a text question", w, w.Draft)
	}
	if len(x.e.Run.Pages[0].Unresolved) != 2 {
		t.Errorf("unresolved %q", x.e.Run.Pages[0].Unresolved)
	}
	// The page's own validation words name a row too, and a question is recorded once.
	x = newSubmit(t, nil)
	x.e.Run.ATS = ATSAshby
	x.e.Run.Pages = []PageReport{{URL: pageURL}}
	x.s.pages[0].rows = []engine.Row{notice}
	invalid := formState()
	invalid.Validation = []string{"Notice period: Missing entry for required field"}
	x.tab.onClick = func(t *scriptTab, _ Control) {
		t.watch.add(ashbyGraphql(200, ashbyBouncedBody))
		t.set(invalid)
	}
	_ = x.submit()
	if got := x.e.Run.Pages[0].Leftovers; len(got) != 2 || got[0].Selector != "#notice" {
		t.Fatalf("recorded leftovers %+v", got)
	}
}

// watchSpy records what stood when the network observation was switched on.
type watchSpy struct {
	*scriptTab
	onWatch func()
}

func (w *watchSpy) WatchResponses(ctx context.Context) (ResponseWatch, error) {
	w.onWatch()
	return w.scriptTab.WatchResponses(ctx)
}

func TestApplyChain_PrePressFailuresNeverBecomeVerdict(t *testing.T) {
	t.Run("the observation starts after the claim and before the press", func(t *testing.T) {
		x := newSubmit(t, nil)
		var claims, clicks = -1, -1
		x.s.tab = &watchSpy{scriptTab: x.tab, onWatch: func() {
			claims = x.g.count(`SELECT count(*) FROM submissions WHERE state = 'submitting'`)
			clicks = len(x.tab.clicks)
		}}
		x.tab.onClick = func(t *scriptTab, _ Control) {
			t.watch.add(post(200, "ok"))
			t.set(thanks())
		}
		if st := x.submit(); st != nil {
			t.Fatalf("stop %+v", st)
		}
		// A request the page made while loading, before this point, is never seen.
		if claims != 1 || clicks != 0 {
			t.Fatalf("observation began with %d claim rows and %d presses made", claims, clicks)
		}
	})
	t.Run("an invisible widget's corner badge is not a challenge", func(t *testing.T) {
		x := newSubmit(t, nil)
		done := thanks()
		done.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, Invisible: true, W: 256, H: 60}}
		done.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
		x.tab.onClick = func(t *scriptTab, _ Control) { t.set(done) }
		if st := x.submit(); st != nil || x.e.Run.Outcome.Kind != OutcomeSubmitted {
			t.Fatalf("stop %+v outcome %+v", st, x.e.Run.Outcome)
		}
	})
	t.Run("a later failing read does not hide the application's own refusal", func(t *testing.T) {
		x := newSubmit(t, nil)
		x.tab.onClick = func(t *scriptTab, _ Control) {
			t.watch.add(post(428, `{"code":"captcha-failed"}`), browser.Response{Method: "GET", URL: "https://careers.example.test/session", Status: 401})
			t.set(formState())
		}
		st := x.submit()
		if st == nil || !strings.Contains(st.reason, "HTTP 428") || strings.Contains(st.reason, "HTTP 401") {
			t.Fatalf("stop %+v", st)
		}
	})
}

func TestApply_Greenhouse_SecurityInputBoxesAreTypedOneCharacterEach(t *testing.T) {
	refusal := post(428, `{"code":"captcha-failed","security_code_recipient":"a***@example.com"}`)
	fc := &fakeCodes{replies: []codeReply{mailed("ABCD1234", mailFrom("no-reply", "greenhouse-mail.io"), "Security code for your application", time.Now().Add(time.Minute))}}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
	x.e.Run.ATS = ATSGreenhouse
	presses := 0
	x.tab.onClick = func(t *scriptTab, _ Control) {
		presses++
		if presses > 1 {
			t.watch.add(post(200, "ok"))
			t.set(thanks())
			return
		}
		t.watch.add(refusal)
		f := formState()
		for i := 0; i < 8; i++ {
			// Marked aria-required and not required, with an id and no label of its own.
			f.Inputs = append(f.Inputs, InputInfo{I: 10 + i, Tag: "input", Type: "text", ID: "security-input-" + string(rune('0'+i)), MaxLength: 1, Form: 0})
		}
		t.set(f)
	}
	if st := x.submit(); st != nil || x.e.Run.Outcome.Kind != OutcomeSubmitted {
		t.Fatalf("stop %+v outcome %+v", st, x.e.Run.Outcome)
	}
	var typed string
	for i := 0; i < 8; i++ {
		typed += x.tab.typed[10+i]
	}
	if typed != "ABCD1234" || x.tab.clicksOf("Submit application") != 2 {
		t.Fatalf("typed %q presses %d", typed, x.tab.clicksOf("Submit application"))
	}
}

func TestApplyChain_OracleStepRendersLate(t *testing.T) {
	// The rule is relative (a step after a form gets the long look, a first
	// page the short one), so the looks are scaled down to keep the test quick.
	timing := WalkTiming{AfterForm: 500 * time.Millisecond, First: 100 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 60 * time.Millisecond}
	empty := &PageState{URL: "https://careers.example.test/apply", Title: "Apply", Text: "Loading", Lines: []string{"Loading"}, Loading: true}
	later := func(n int) []*PageState {
		var seq []*PageState
		for i := 0; i < n; i++ {
			seq = append(seq, empty)
		}
		return seq
	}
	t.Run("every step after a form page gets its own allowance", func(t *testing.T) {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 3))
		w.g.r.d.Page.Timing = timing
		step := 1
		w.tab.onClick = func(t *scriptTab, c Control) {
			if c.Labels[0] == "Next" && step < 3 {
				step++
				// 30 empty looks (300 ms) each: together longer than one allowance.
				t.script(append(later(30), wizardStep(step, 3))...)
				t.state = empty
			}
		}
		res, st := w.walk()
		if st != nil || res.End != EndSubmit || w.page.nFills != 3 {
			t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
		}
	})
	t.Run("a step empty on the second look past the allowance is no-form", func(t *testing.T) {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
		w.g.r.d.Page.Timing = timing
		w.tab.onClick = func(t *scriptTab, c Control) {
			if c.Labels[0] == "Next" {
				t.state = empty
			}
		}
		res, st := w.walk()
		if st != nil && res == nil {
			t.Fatalf("stop %+v", st)
		}
		if res.End != EndNoForm {
			t.Fatalf("end %+v", res)
		}
		if took := w.took; took < 450*time.Millisecond {
			t.Fatalf("gave up after %v, before the 500 ms allowance", took)
		}
	})
	t.Run("a first page that drew a control but no opener keeps a single short look", func(t *testing.T) {
		// A page with no control at all is still drawing and gets the long look
		// (TestApply_Walk_ApplyDrawnAfterThePostingTextIsFollowed).
		drawn := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Engineer", Text: "Engineer", Lines: []string{"Engineer"},
			Controls: []Control{{I: 5, Tag: "button", Labels: []string{"Share"}, Form: -1}}}
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, drawn)
		w.g.r.d.Page.Timing = timing
		// Probes are counted, not wall time: the short look reads about
		// First/Poll times, the long one AfterForm/Poll (50); a slow machine
		// only lowers the count.
		probes := 0
		w.tab.probeHook = func(*scriptTab) { probes++ }
		res, _ := w.walk()
		if res == nil || res.End != EndNoForm {
			t.Fatalf("end %+v", res)
		}
		if probes > 25 {
			t.Fatalf("a first page was read %d times: it got the long look", probes)
		}
	})
}

// resumeLane is a lane after a restart: its tabs are listed, none is kept yet,
// and each kept tab gets an observer that waits to be released.
type resumeLane struct {
	*fakeLane
	mu       sync.Mutex
	kept     []string
	observed []string
	block    chan struct{}
}

func (l *resumeLane) KeepTab(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kept = append(l.kept, id)
	return nil
}

func (l *resumeLane) ObserveTab(_ context.Context, id string) (TabObserver, error) {
	l.mu.Lock()
	l.observed = append(l.observed, id)
	l.mu.Unlock()
	return &scriptedObserver{onRead: func(int) (json.RawMessage, error) {
		<-l.block
		return nil, browserErr(browser.CodeTabClosed)
	}}, nil
}

func TestHandoff_WatchResumesAfterRestartKeepingTheNewestTabs(t *testing.T) {
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	board := &fakeBoard{rows: []KeptRow{
		{RunID: "run_old", TabID: "tab-old", Since: at(1), Waiting: true},
		{RunID: "run_mid", TabID: "tab-mid", Since: at(2), Waiting: true},
		{RunID: "run_gone", TabID: "tab-gone", Since: at(3), Waiting: true}, // the user closed it
		{RunID: "run_new", TabID: "tab-new", Since: at(4), Waiting: true},
		{RunID: "run_done", TabID: "tab-done", Since: at(5), Waiting: false}, // needs no one
		{RunID: "run_newest", TabID: "tab-newest", Since: at(6), Waiting: true},
		{RunID: "run_none", TabID: "", Since: at(7), Waiting: true},
	}}
	lane := &resumeLane{fakeLane: &fakeLane{live: []string{"tab-old", "tab-mid", "tab-new", "tab-done", "tab-newest"}}, block: make(chan struct{})}
	w := &Watcher{Ctx: context.Background(), Poster: &posts{}, Every: 2 * time.Millisecond}
	k := &Keeper{Board: board, Watch: w}
	n := k.ResumeWatches(context.Background(), lane)
	// Three sessions are the cap: the three newest waiting rows keep their watch.
	if n != handoffMaxTabs || w.Active() != handoffMaxTabs {
		t.Fatalf("started %d, active %d", n, w.Active())
	}
	eventually(t, 3*time.Second, "the observers to attach", func() bool {
		lane.mu.Lock()
		defer lane.mu.Unlock()
		return len(lane.observed) == handoffMaxTabs
	})
	lane.mu.Lock()
	observed := slices.Clone(lane.observed)
	lane.mu.Unlock()
	slices.Sort(observed)
	if !slices.Equal(observed, []string{"tab-mid", "tab-new", "tab-newest"}) {
		t.Fatalf("observed %v", observed)
	}
	close(lane.block)
	w.Wait()
	if k.ResumeWatches(context.Background(), nil) != 0 || (*Keeper)(nil).ResumeWatches(context.Background(), lane) != 0 {
		t.Fatal("no lane or no keeper resumes nothing")
	}
}
