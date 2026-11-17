package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/gates"
)

// twoCaptcha is an httptest stand-in for the 2Captcha API. Each answer closes
// its connection, so a walk in a synctest bubble keeps no idle one waiting on
// the network.
type twoCaptcha struct {
	mu      sync.Mutex
	srv     *httptest.Server
	tasks   int
	calls   int // every request, balance reads included
	balance string
	tokens  []string
}

func newTwoCaptcha(t *testing.T) *twoCaptcha {
	f := &twoCaptcha{balance: "5.0", tokens: []string{"tok-one", "tok-two", "tok-three"}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")
		f.calls++
		switch r.URL.Path {
		case "/getBalance":
			fmt.Fprintf(w, `{"errorId":0,"balance":%s}`, f.balance)
		case "/createTask":
			f.tasks++
			fmt.Fprintf(w, `{"errorId":0,"taskId":%d}`, f.tasks)
		case "/getTaskResult":
			tok := f.tokens[0]
			if len(f.tokens) > 1 {
				f.tokens = f.tokens[1:]
			}
			b, _ := json.Marshal(map[string]any{"errorId": 0, "status": "ready", "solution": map[string]string{"gRecaptchaResponse": tok, "token": tok}})
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *twoCaptcha) solver(run gates.RunKind, skip gates.SkipStatus) func(*Exec, func(gates.Decision)) captcha.Solver {
	return func(e *Exec, audit func(gates.Decision)) captcha.Solver {
		// A clock that advances by what the solver sleeps, so polling takes no time.
		clock := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
		var cm sync.Mutex
		spent := 0
		if e != nil {
			spent = e.spent
		}
		return captcha.New(captcha.Config{
			Now: func() time.Time { cm.Lock(); defer cm.Unlock(); return clock },
			Run: run, Spent: spent, Switch: gates.SolverOn, Key: func() string { return "k" + strings.Repeat("x", 24) },
			Skip: func() gates.SkipStatus { return skip }, Audit: audit,
			BaseURL: f.srv.URL, HTTP: f.srv.Client(), Sleep: func(_ context.Context, d time.Duration) error {
				cm.Lock()
				clock = clock.Add(d)
				cm.Unlock()
				return nil
			},
		})
	}
}

func widgetForm(kind string) *PageState {
	f := formState()
	f.Captcha = []CaptchaW{{Kind: kind, SiteKey: "site-key-1", Visible: true, W: 304, H: 78}}
	f.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
	return f
}

func TestGates_Captcha_DetectionVisibilityBars(t *testing.T) {
	visible := formState()
	visible.Captcha = []CaptchaW{{Kind: "recaptcha_v2", SiteKey: "k", Visible: true, W: 304, H: 78}}
	dormant := formState()
	dormant.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: false}}
	invisible := formState()
	invisible.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, Invisible: true, W: 1, H: 1}}
	invisibleOpen := formState()
	invisibleOpen.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, Invisible: true, BframeOpen: true, W: 400, H: 500}}
	v3 := formState()
	v3.Captcha = []CaptchaW{{Kind: "recaptcha_v3", Visible: true, W: 100, H: 100}}
	for name, c := range map[string]struct {
		st   *PageState
		want string
	}{
		"visible widget": {visible, "recaptcha_v2"}, "dormant": {dormant, ""}, "invisible": {invisible, ""},
		"invisible with an open puzzle": {invisibleOpen, "recaptcha_v2"}, "v3 is never a challenge": {v3, ""},
	} {
		got := DetectChallenge(c.st)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: read as %+v", name, got)
		case c.want != "" && (got == nil || got.Kind != c.want):
			t.Errorf("%s: %+v, want %s", name, got, c.want)
		}
	}
	if d := DetectChallenge(visible); d.SiteKey != "k" {
		t.Errorf("sitekey %q", d.SiteKey)
	}
}

func TestGates_Captcha_TurnstileThatIsTheWholePageIsTheInterstitialNeverAWidget(t *testing.T) {
	whole := &PageState{URL: "https://careers.example.test/", Title: "Just a moment...", Text: "Verify you are human", Lines: []string{"Verify you are human"},
		Captcha: []CaptchaW{{Kind: "turnstile_widget", Visible: true, W: 300, H: 65}}, Controls: []Control{{I: 0, Labels: []string{"Submit"}}}}
	d := DetectChallenge(whole)
	if d == nil || d.Kind != gates.ChallengeInterstitial {
		t.Fatalf("detected %+v", d)
	}
	// The same widget on a page with application controls is a widget.
	form := formState()
	form.Captcha = []CaptchaW{{Kind: "turnstile_widget", Visible: true, W: 300, H: 65}}
	if d := DetectChallenge(form); d == nil || d.Kind != gates.ChallengeTurnstileWidget || d.ChallengeOnly {
		t.Fatalf("detected %+v", d)
	}
	// A bare Turnstile widget on a field-less page without the interstitial copy.
	bare := &PageState{URL: "https://careers.example.test/", Title: "Check", Text: "", Captcha: []CaptchaW{{Kind: "turnstile_widget", Visible: true, W: 300, H: 65}}}
	if d := DetectChallenge(bare); d == nil || d.Kind == gates.ChallengeTurnstileWidget {
		t.Fatalf("a Turnstile that is the whole page was labelled an embedded widget: %+v", d)
	}
}

func TestGates_Captcha_BeforePressTokenThenReprobeThenPress(t *testing.T) {
	svc := newTwoCaptcha(t)
	x := newSubmit(t, func(d *Deps) { d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed) })
	x.tab.set(widgetForm("recaptcha_v2"))
	x.tab.onClick = func(t *scriptTab, _ Control) {
		t.watch.add(post(200, "ok"))
		t.set(thanks())
	}
	// Delivering the token fills the response field: the re-probe reads it.
	x.tab.onDeliver = func(t *scriptTab, kind string) {
		f := widgetForm(kind)
		f.Resp = []RespField{{Name: "g-recaptcha-response", Empty: false}}
		t.set(f)
	}
	if st := x.submit(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if len(x.tab.delivered) != 1 || x.tab.delivered[0] != "recaptcha_v2" || len(x.tab.clicks) != 1 || svc.tasks != 1 {
		t.Fatalf("delivered %v clicks %v tasks %d", x.tab.delivered, x.tab.clicks, svc.tasks)
	}
	if len(x.e.Run.Captcha) == 0 || strings.Contains(strings.Join(x.e.Run.Captcha, " "), "tok-one") {
		t.Fatalf("captcha lines %v: a decision line is recorded and the token never is", x.e.Run.Captcha)
	}
	if x.g.audits(gates.GateCaptchaSolve) == 0 {
		t.Fatal("the solve decision was not audited")
	}
}

// A token that reached no response field and no callback solved nothing: the
// control is not pressed a second time on the strength of it.
func TestGates_Captcha_ATokenThatReachedNothingIsNoSolve(t *testing.T) {
	svc := newTwoCaptcha(t)
	x := newSubmit(t, func(d *Deps) { d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed) })
	x.tab.lost = true
	presses := 0
	x.tab.onClick = func(t *scriptTab, _ Control) {
		presses++
		t.set(widgetForm("hcaptcha")) // a puzzle stands where the confirmation should be
	}
	st := x.submit()
	if st == nil || presses != 1 || len(x.tab.delivered) != 1 {
		t.Fatalf("stop %+v presses %d delivered %v: the puzzle stays the user's and Submit is pressed once", st, presses, x.tab.delivered)
	}
	if _, ok := x.g.r.solveAt(context.Background(), x.e, x.s, &Detected{Kind: gates.ChallengeHCaptcha}, gates.PointAfterPress); ok {
		t.Fatal("a delivery that reached nothing read as a solve")
	}
}

// The waits after a token follow the run's context: a run cancelled while the
// page takes the token presses nothing more and does not sit the waits out.
func TestGates_Captcha_ACancelledRunPressesNothingAfterTheToken(t *testing.T) {
	svc := newTwoCaptcha(t)
	x := newSubmit(t, func(d *Deps) { d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed) })
	x.tab.set(widgetForm("hcaptcha"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x.tab.onDeliver = func(*scriptTab, string) { cancel() }
	presses, start := 0, time.Now()
	again := x.g.r.afterPress(ctx, x.e, x.s, x.tab.watch, func(context.Context) *stop { presses++; return nil })
	if again || presses != 0 || len(x.tab.delivered) != 1 {
		t.Fatalf("pressed again %v presses %d delivered %v: a cancelled run presses nothing", again, presses, x.tab.delivered)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("the cancelled run waited %s", waited)
	}
}

func TestGates_Captcha_DryRunAndSkipListedCompanyNeverReachTheService(t *testing.T) {
	svc := newTwoCaptcha(t)
	x := newSubmit(t, func(d *Deps) { d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipListed) })
	x.tab.set(widgetForm("recaptcha_v2"))
	if st := x.submit(); st == nil || svc.tasks != 0 || len(x.tab.clicks) != 0 {
		t.Fatalf("stop %+v tasks %d clicks %v: a skip-listed company is never solved for", st, svc.tasks, x.tab.clicks)
	}
	y := newSubmit(t, func(d *Deps) { d.Page.Solver = svc.solver(gates.RunDryRun, gates.SkipNotListed) })
	y.tab.set(widgetForm("recaptcha_v2"))
	if _, ok := y.g.r.beforePress(context.Background(), y.e, y.s, y.tab.state); ok || svc.tasks != 0 {
		t.Fatalf("a dry run asked for a solve (tasks %d)", svc.tasks)
	}
}

func TestGates_Captcha_AfterPressSecondPressWithoutSecondClaim(t *testing.T) {
	svc := newTwoCaptcha(t)
	x := newSubmit(t, func(d *Deps) { d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed) })
	presses := 0
	x.tab.onClick = func(t *scriptTab, _ Control) {
		presses++
		if presses == 1 {
			t.set(widgetForm("hcaptcha")) // a puzzle stands where the confirmation should be
			return
		}
		t.watch.add(post(200, "ok"))
		t.set(thanks())
	}
	if st := x.submit(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if presses != 2 || svc.tasks != 1 {
		t.Fatalf("presses %d tasks %d", presses, svc.tasks)
	}
	if n := x.g.count(`SELECT count(*) FROM submissions`); n != 1 {
		t.Fatalf("%d claim rows: the claim is not made twice", n)
	}
	if x.e.Run.Outcome.Kind != OutcomeSubmitted {
		t.Fatalf("outcome %+v", x.e.Run.Outcome)
	}
}

func TestGates_Captcha_RefusedTokenOneRetryInsideCapAndFieldBlanked(t *testing.T) {
	svc := newTwoCaptcha(t)
	x := newSubmit(t, func(d *Deps) { d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed) })
	x.tab.set(widgetForm("recaptcha_v2"))
	x.tab.onDeliver = func(t *scriptTab, kind string) {}
	d := DetectChallenge(x.tab.state)
	sol, ok := x.g.r.solveAt(context.Background(), x.e, x.s, d, gates.PointBeforePress)
	if !ok {
		t.Fatal("first solve failed")
	}
	if _, ok := x.g.r.retry(context.Background(), x.e, x.s, d, sol); !ok {
		t.Fatal("the one retry inside the cap was refused")
	}
	if x.tab.blanked == 0 {
		t.Fatal("the response field was not blanked after the refusal")
	}
	// The cap is two solves: a third is refused before the service is asked.
	sol2, _ := x.g.r.solveAt(context.Background(), x.e, x.s, d, gates.PointBeforePress)
	if _, ok := x.g.r.retry(context.Background(), x.e, x.s, d, sol2); ok {
		t.Fatal("a third solve went through")
	}
	if svc.tasks > 2 {
		t.Fatalf("%d tasks created: the cap is %d", svc.tasks, gates.CaptchaMaxSolves)
	}
}

// A challenge-only page on a listed bot manager's validation host in front of
// the tenant is cleared and its plain Submit pressed, then the walk goes on.
func TestGates_Captcha_WidgetOnlyPageSolvedThenWalkContinues(t *testing.T) {
	svc := newTwoCaptcha(t)
	wall := &PageState{URL: "https://validate.perfdrive.com/?ssa=1", Title: "Checking", Text: "Please complete the check", Lines: []string{"Please complete the check"},
		Captcha: []CaptchaW{{Kind: "hcaptcha", SiteKey: "k", Visible: true, W: 304, H: 78}}, Controls: []Control{{I: 0, Tag: "button", Labels: []string{"Submit"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, wall)
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Submit" && t.state == wall {
			t.set(wizardStep(2, 2))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.delivered) != 1 || svc.tasks != 1 {
		t.Fatalf("delivered %v tasks %d", w.tab.delivered, svc.tasks)
	}
	if w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND code = 'ok' AND reason LIKE '%validate.perfdrive.com%'`) != 1 {
		t.Fatal("the plain Submit was not pressed through the gate under the bot-manager host")
	}
	if n := w.g.count(`SELECT count(*) FROM submissions`); n != 0 {
		t.Fatalf("%d ledger rows for the interstitial press", n)
	}
}

// reviewWithWidget is a wizard's last step: no field, a checkbox widget and
// the form's own Submit.
func reviewWithWidget() *PageState {
	return &PageState{URL: "https://careers.example.test/apply", Title: "Review", Text: "Review your application", Lines: []string{"Review your application"},
		Captcha: []CaptchaW{{Kind: "recaptcha_v2", SiteKey: "k", Visible: true, W: 304, H: 78}}, Resp: []RespField{{Name: "g-recaptcha-response", Empty: true}},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Submit"}, Form: -1}}}
}

// A widget beside the final Submit after a form page is the submit stage's
// first evidence point: the walk neither buys a solve for it nor presses the
// Submit, and ends ready for the submit stage.
func TestGates_Captcha_FinalStepWidgetAndSubmitGoesToTheSubmitStage(t *testing.T) {
	svc := newTwoCaptcha(t)
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, wizardStep(1, 2))
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			t.set(reviewWithWidget())
		}
	}
	res, st := w.walk()
	if st != nil || res == nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	for _, c := range w.tab.clicks {
		if c == "Submit" {
			t.Fatalf("the walk pressed the final Submit: clicks %v", w.tab.clicks)
		}
	}
	if svc.tasks != 0 || len(w.tab.delivered) != 0 {
		t.Fatalf("the walk bought %d solve(s) for the submit page", svc.tasks)
	}
}

// Before any form page, a challenge page whose button reads Submit is left to
// the user: no solve is bought for a press the gate would refuse.
func TestGates_Captcha_ChallengePageWhoseButtonReadsSubmitIsNotSolvedBeforeTheForm(t *testing.T) {
	svc := newTwoCaptcha(t)
	wall := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Checking", Text: "Please complete the check", Lines: []string{"Please complete the check"},
		Captcha: []CaptchaW{{Kind: "recaptcha_v2", SiteKey: "k", Visible: true, W: 304, H: 78}}, Controls: []Control{{I: 0, Tag: "button", Labels: []string{"Submit"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, wall)
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	res, st := w.walk()
	if st != nil || res == nil || res.End != EndBotChallenge || svc.tasks != 0 || len(w.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v tasks %d clicks %v", res, st, svc.tasks, w.tab.clicks)
	}
}

func TestGates_Captcha_FullPageInterstitialIsNeverSolvedAndEndsAsBotCheck(t *testing.T) {
	svc := newTwoCaptcha(t)
	wall := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Just a moment...", Text: "Verify you are human", Lines: []string{"Verify you are human"},
		Captcha: []CaptchaW{{Kind: "turnstile_widget", Visible: true, W: 300, H: 65}}, Controls: []Control{{I: 0, Tag: "button", Labels: []string{"Submit"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, wall)
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	res, st := w.walk()
	if st != nil || res.End != EndBotChallenge || svc.tasks != 0 || len(w.tab.clicks) != 0 || len(w.tab.delivered) != 0 {
		t.Fatalf("end %+v stop %+v tasks %d clicks %v", res, st, svc.tasks, w.tab.clicks)
	}
}

// SmartRecruiters answers "I'm interested" with DataDome's interstitial: a top
// document that is only a captcha-delivery.com frame, its copy ("Verification
// Required", "Slide right to secure your access") inside that frame. The
// bot manager's challenge is the person's: never solved, never pressed, and the
// walk ends as a bot check of class verification.
func dataDomePage() *PageState {
	return &PageState{
		URL: "https://jobs.smartrecruiters.com/oneclick-ui/company/CircleLogistics1/publication/d20c0deb-3cf7-44d2-b521-9aa234660fa7?dcr_ci=CircleLogistics1", Title: "smartrecruiters.com",
		Captcha: []CaptchaW{{Kind: "datadome", Visible: true, W: 1280, H: 800}},
		Frames:  []FrameInfo{{Src: "https://geo.captcha-delivery.com/captcha/?initialCid=AHrlqAAAAAMAb1YCF9RfFYcAmCx3Tw%3D%3D&t=fe&dm=cd", Visible: true, W: 1280, H: 800}},
	}
}

func TestApply_Frames_ChatWidgetFramesAreNeverTheApplication(t *testing.T) {
	for _, src := range []string{
		"https://assets-cdn.breezy.hr/breezy-widget/index.html#/c29c0d524b5e01?page=https://vetsez.breezy.hr/p/18df3ec23bf901",
		"https://widget.intercom.io/widget/abc", "https://static.zdassets.com/web_widget/latest/web_widget.js",
	} {
		if !frameSkip.MatchString(src) {
			t.Errorf("frame %s would be read as an embedded application", src)
		}
	}
	if frameSkip.MatchString("https://careers.example.org/embedded/application") {
		t.Error("an embedded application frame must not be skipped")
	}
}

func TestGates_Captcha_DataDomeInterstitialIsAChallengeOnlyPageNeverSolved(t *testing.T) {
	// Its frame is a challenge widget, never an application folded in as the page:
	// the slider inside it reads as a control, which would make the page look like a form step.
	for _, f := range dataDomePage().Frames {
		if !frameSkip.MatchString(f.Src) {
			t.Errorf("frame %s would be read as an embedded application", f.Src)
		}
	}
	d := DetectChallenge(dataDomePage())
	if d == nil || d.Kind != gates.ChallengeDataDome || !d.ChallengeOnly {
		t.Fatalf("detected %+v", d)
	}
	// The same frame over a filled form is a challenge, not the whole page.
	over := formState()
	over.Captcha = dataDomePage().Captcha
	if d := DetectChallenge(over); d == nil || d.Kind != gates.ChallengeDataDome || d.ChallengeOnly {
		t.Fatalf("over a form: %+v", d)
	}
	svc := newTwoCaptcha(t)
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, dataDomePage())
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	res, st := w.walk()
	if st != nil || res == nil || res.End != EndBotChallenge || svc.tasks != 0 || len(w.tab.clicks) != 0 || len(w.tab.delivered) != 0 {
		t.Fatalf("end %+v stop %+v tasks %d clicks %v", res, st, svc.tasks, w.tab.clicks)
	}
	if res.End.Class(false) != ClassVerification {
		t.Fatalf("class %s", res.End.Class(false))
	}
	// One reload of the blocked address before the page is the person's, never a second.
	reloads := 0
	for _, u := range w.tab.navs {
		if u == dataDomePage().URL {
			reloads++
		}
	}
	if reloads != 1 {
		t.Fatalf("reloads %d, navs %v", reloads, w.tab.navs)
	}
}

func TestGates_Captcha_DataDomeLetsTheReloadThroughAndTheWalkGoesOn(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, dataDomePage())
	w.tab.onNav = func(t *scriptTab, url string) {
		if url == dataDomePage().URL {
			t.set(formState())
		}
	}
	res, st := w.walk()
	if st != nil || res == nil || res.End != EndSubmit {
		t.Fatalf("result %+v stop %+v: the reloaded form is walked to its submit", res, st)
	}
	if len(w.tab.navs) != 1 || w.tab.navs[0] != dataDomePage().URL {
		t.Fatalf("navigations %v: one reload of the challenged page", w.tab.navs)
	}
}

func TestGates_Captcha_NextRaisedPuzzleOneRepress(t *testing.T) {
	svc := newTwoCaptcha(t)
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, wizardStep(1, 2))
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	puzzle := wizardStep(1, 2)
	puzzle.Captcha = []CaptchaW{{Kind: "hcaptcha", SiteKey: "site-key-2", Visible: true, W: 304, H: 78}}
	puzzle.Resp = []RespField{{Name: "h-captcha-response", Empty: true}}
	nexts := 0
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] != "Next" {
			return
		}
		nexts++
		if nexts == 1 {
			t.set(puzzle) // the Next raised a puzzle and the page stayed put
			return
		}
		t.set(wizardStep(2, 2))
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || nexts != 2 || svc.tasks != 1 {
		t.Fatalf("end %+v stop %+v nexts %d tasks %d lines %v", res, st, nexts, svc.tasks, w.e.Run.Captcha)
	}
}

// Chase (Oracle): a dry run's Next opened an hCaptcha puzzle over the email
// page. Nothing may solve it, so the run stops as a bot check the person
// clears, never as a stalled step that blames a honeypot box.
func TestGates_Captcha_NextRaisedPuzzleTheRunMayNotSolveIsABotCheck(t *testing.T) {
	svc := newTwoCaptcha(t)
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, ExplicitRequest: true}, wizardStep(1, 2))
	w.g.r.d.Page.Solver = svc.solver(gates.RunDryRun, gates.SkipNotListed)
	puzzle := wizardStep(1, 2)
	puzzle.Captcha = []CaptchaW{{Kind: "hcaptcha", SiteKey: "site-key-2", Visible: true, W: 400, H: 600}}
	puzzle.Resp = []RespField{{Name: "h-captcha-response", Empty: true}}
	nexts := 0
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			nexts++
			t.set(puzzle)
		}
	}
	res, st := w.walk()
	if st == nil || st.class != ClassVerification || st.board != "challenge" || !strings.Contains(st.reason, "hcaptcha") || nexts != 1 || svc.tasks != 0 {
		t.Fatalf("end %+v stop %+v nexts %d tasks %d", res, st, nexts, svc.tasks)
	}
}

func TestGates_Captcha_SigninAndSignupSolvedBeforeCredentialOrSubmit(t *testing.T) {
	svc := newTwoCaptcha(t)
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed) })
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	wallWithWidget := loginPage("")
	wallWithWidget.Captcha = []CaptchaW{{Kind: "recaptcha_v2", SiteKey: "site-key-3", Visible: true, W: 304, H: 78}}
	wallWithWidget.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
	a.tab.set(wallWithWidget)
	a.s.entry = wallWithWidget
	order := []string{}
	a.tab.onDeliver = func(t *scriptTab, kind string) { order = append(order, "token") }
	a.tab.onClick = func(t *scriptTab, c Control) {
		order = append(order, "press")
		t.set(appPage())
	}
	if _, st := a.run(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if len(order) != 2 || order[0] != "token" || order[1] != "press" || a.tab.typed[1] != "Default-Pw-1!x" {
		t.Fatalf("order %v", order)
	}
	// Without a solver nothing is typed.
	b := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	b.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	b.tab.set(wallWithWidget)
	b.s.entry = wallWithWidget
	_, st := b.run()
	if st == nil || len(b.tab.typed) != 0 || st.board != "challenge" {
		t.Fatalf("stop %+v typed %v", st, b.tab.typed)
	}
	// A create-account form with a widget is solved before its submit press.
	c := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", func(d *Deps) {
		d.Page.Policy = fakePolicy{}
		d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	})
	c.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	signupPages(c, false)
	signup := c.tab.onClick
	order = nil
	c.tab.onDeliver = func(t *scriptTab, kind string) {
		if t.state.Title == "Create account" {
			order = append(order, "token")
		}
	}
	c.tab.onClick = func(t *scriptTab, ctl Control) {
		switch {
		case ctl.Labels[0] == "Sign in" && t.typed[1] == "Default-Pw-1!x":
			t.typed = map[int]string{}
			t.set(loginPage("Invalid email or password"))
			return
		case ctl.Labels[0] == "Create account" && t.state.Title == "Create account":
			order = append(order, "press")
		}
		signup(t, ctl)
		if t.state.Title == "Create account" && len(t.state.Captcha) == 0 {
			form := *t.state
			form.Captcha = []CaptchaW{{Kind: "recaptcha_v2", SiteKey: "site-key-4", Visible: true, W: 304, H: 78}}
			form.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
			t.set(&form)
		}
	}
	if d, st := c.run(); st != nil || !strings.Contains(d, "created an account") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if strings.Join(order, ",") != "token,press" {
		t.Fatalf("order %v: the sign-up form's widget is solved before its press", order)
	}
}

// The bot-manager exception covers the page's own top-document button only: a
// Submit that sits inside an embedded frame of a listed host's page may be any
// site's, so the page ends as a bot check with nothing solved or pressed.
func TestGates_Captcha_BotManagerPageWhoseSubmitIsInAFrameEndsAsABotCheck(t *testing.T) {
	svc := newTwoCaptcha(t)
	wall := &PageState{URL: "https://validate.perfdrive.com/?ssa=1", Title: "Checking", Text: "Please complete the check", Lines: []string{"Please complete the check"},
		Captcha: []CaptchaW{{Kind: "hcaptcha", SiteKey: "k", Visible: true, W: 304, H: 78}}, Controls: []Control{{I: frameBase + 3, Tag: "button", Labels: []string{"Submit"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, wall)
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	res, st := w.walk()
	if st != nil || res == nil || res.End != EndBotChallenge || svc.tasks != 0 || len(w.tab.clicks) != 0 || len(w.tab.delivered) != 0 {
		t.Fatalf("end %+v stop %+v tasks %d clicks %v", res, st, svc.tasks, w.tab.clicks)
	}
}
