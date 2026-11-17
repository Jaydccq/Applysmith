package apply

import (
	"context"

	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gates"
)

// closedTab is a tab the lane no longer lists.
type closedTab struct{ *scriptTab }

func (closedTab) Closed() bool { return true }

// gateRig is the account stage at a login gate the walk stopped on: the page in
// the walker's tab, a stored default that signs in.
func gateRig(t *testing.T, page *PageState, mod func(*Deps)) (*acctEnv, *walkResult) {
	t.Helper()
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", mod)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.tab.set(page)
	a.signInRight("Default-Pw-1!x")
	return a, &walkResult{End: EndLoginRequired, Detail: "the page asks for a sign-in the entry check did not see", Last: page}
}

func TestApply_Account_ExistingAccountVerifiesOnGateTab(t *testing.T) {
	// A gate that exists only in the session that clicked through to it: the
	// verifying sign-in types into the very page the walker stopped on, and a
	// fresh navigation (a cold load shows no form) is never made.
	a, wr := gateRig(t, loginPage(""), nil)
	rewalk, stp := a.g.r.reenterGate(context.Background(), a.e, a.s, wr)
	if stp != nil || !rewalk {
		t.Fatalf("rewalk %v stop %+v", rewalk, stp)
	}
	if len(a.tab.navs) != 0 {
		t.Fatalf("navigations %v: the gate tab is not reloaded", a.tab.navs)
	}
	if a.e.Run.Signins[0].Succeeded != "settings-default" {
		t.Fatalf("record %+v", a.e.Run.Signins)
	}
	// A checkbox captcha on that form is the user's: no password is typed.
	widget := loginPage("")
	widget.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, W: 304, H: 78}}
	widget.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
	b, wr := gateRig(t, widget, nil)
	_, stp = b.g.r.reenterGate(context.Background(), b.e, b.s, wr)
	if stp == nil || stp.class != ClassCredential || len(b.tab.typed) != 0 {
		t.Fatalf("stop %+v typed %v", stp, b.tab.typed)
	}
}

func ctaPage() *PageState {
	return &PageState{
		URL: loginURL, Title: "Careers", Text: "Join us", Lines: []string{"Join us"},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Sign in with SSO"}, Form: -1}},
	}
}

func TestApply_Account_GateReentryAsksTheSession(t *testing.T) {
	ctx := context.Background()
	t.Run("cookies landing re-run the walk without a re-entry", func(t *testing.T) {
		sy := &fakeSync{res: OwnHostResult{Imported: true}}
		a, wr := gateRig(t, ctaPage(), func(d *Deps) { d.Page.Sessions = sy })
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp != nil || !rewalk || len(sy.calls) != 1 || sy.calls[0] != "careers.example.test" {
			t.Fatalf("rewalk %v stop %+v calls %v", rewalk, stp, sy.calls)
		}
		if a.e.reentries != 0 || len(a.tab.typed) != 0 {
			t.Fatalf("re-entries %d typed %v: the session carries the walk, no password", a.e.reentries, a.tab.typed)
		}
	})
	t.Run("without cookies the stop carries the reason", func(t *testing.T) {
		sy := &fakeSync{res: OwnHostResult{Reason: "no-cookies"}}
		a, wr := gateRig(t, ctaPage(), func(d *Deps) { d.Page.Sessions = sy })
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp != nil || rewalk || !strings.Contains(wr.Detail, "your Chrome holds no sign-in for careers.example.test") {
			t.Fatalf("rewalk %v stop %+v detail %q", rewalk, stp, wr.Detail)
		}
		stop := a.g.r.afterWalk(ctx, a.e, a.s, t0, wr, nil)
		if stop == nil || stop.class != ClassCredential || stop.board != "sign-in" || !strings.Contains(stop.reason, "your Chrome holds no sign-in for careers.example.test") {
			t.Fatalf("stop %+v", stop)
		}
	})
	t.Run("not asked when the stage already signed in on an existing session", func(t *testing.T) {
		sy := &fakeSync{res: OwnHostResult{Imported: true}}
		a, wr := gateRig(t, ctaPage(), func(d *Deps) { d.Page.Sessions = sy })
		a.e.Run.Signins = []SigninRecord{{Host: "careers.example.test", Session: "imported", Succeeded: "session"}}
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp != nil || rewalk || len(sy.calls) != 0 {
			t.Fatalf("rewalk %v stop %+v calls %v: that session is the one the walker just refused", rewalk, stp, sy.calls)
		}
	})
}

// wallRun is what a run did at a sign-in wall: whether it went on past it,
// the stop it ended with there, and what it typed and pressed.
type wallRun struct {
	goesOn        bool
	stp           *stop
	typed, clicks int
}

// Every sign-in wall, Google-only walls, a job board's and LinkedIn's
// included, asks the user's everyday Chrome for exactly its host's session
// before the run presses anything or stops there. A session that takes
// carries the run on with nothing typed or pressed; otherwise the stop says
// what the import found. The password wall and a login control the probe could
// not see are TestApply_Account_SessionImportBeforePasswordsOncePerHost and
// TestApply_Account_GateReentryAsksTheSession.
func TestApply_Account_EveryWallAsksTheSessionBeforeItStops(t *testing.T) {
	ctx := context.Background()
	googleOnly := func(allowGoogle bool) func(*testing.T, *fakeSync) wallRun {
		return func(t *testing.T, sy *fakeSync) wallRun {
			a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) {
				d.Page.Sessions = sy
				d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: allowGoogle} }
			})
			w := loginPage("")
			w.Inputs = nil
			w.Controls = append(w.Controls, Control{I: 9, Tag: "button", Labels: []string{"Continue with Google"}, Form: -1})
			a.tab.set(w)
			a.s.entry = a.tab.state
			a.tab.onNav = func(t *scriptTab, _ string) { t.set(appPage()) } // the reload after the import
			_, st := a.run()
			return wallRun{goesOn: st == nil, stp: st, typed: len(a.tab.typed), clicks: len(a.tab.clicks)}
		}
	}
	board := func(t *testing.T, sy *fakeSync) wallRun {
		page := &PageState{URL: "https://www.indeed.com/viewjob?jk=0a1b2c3d", Title: "Platform Engineer", Text: "Sign in to apply", Lines: []string{"Sign in to apply"},
			Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Sign in to apply"}, Form: -1}}}
		a, _ := gateRig(t, page, func(d *Deps) { d.Page.Sessions = sy })
		wr := &walkResult{End: EndLoginRequired, NoAccount: true, Detail: `the page asks to sign in to apply ("Sign in to apply")`, Last: page}
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if !rewalk && stp == nil {
			stp = a.g.r.afterWalk(ctx, a.e, a.s, t0, wr, nil)
		}
		// A session that took reloads the page the walk runs again on.
		reloaded := len(a.tab.navs) == 1 && a.tab.navs[0] == page.URL
		return wallRun{goesOn: rewalk && reloaded, stp: stp, typed: len(a.tab.typed), clicks: len(a.tab.clicks)}
	}
	linkedIn := func(signedOut bool) func(*testing.T, *fakeSync) wallRun {
		return func(t *testing.T, sy *fakeSync) wallRun {
			run, tab, g := linkedInRun(t)
			g.r.d.Page.Sessions = sy
			e := &Exec{r: g.r, Run: run, Ctx: ctx, Cls: mustClassify(t, run.Input.URL)}
			s := &session{r: g.r, e: e, tab: tab, page: tab.page}
			// The Easy Apply flow opens once a session took.
			tab.onNav = func(tab *scriptTab, _ string) {
				if len(sy.calls) > 0 && sy.res.Imported {
					tab.mu.Lock()
					tab.lk["modal"] = true
					tab.mu.Unlock()
				}
			}
			if signedOut {
				tab.lk["affordance"] = map[string]any{"label": "Sign in", "signedOut": true}
				stp := g.r.openLinkedIn(ctx, e, s)
				return wallRun{goesOn: stp == nil, stp: stp, typed: len(tab.typed), clicks: len(tab.clicks)}
			}
			wr := &walkResult{End: EndLoginRequired, Detail: "the page asks for a sign-in", Last: &PageState{URL: "https://www.linkedin.com/login?session_redirect=jobs", Title: "LinkedIn Login"}}
			rewalk, stp := g.r.reenterGate(ctx, e, s, wr)
			if !rewalk && stp == nil {
				stp = g.r.afterWalk(ctx, e, s, t0, wr, nil)
			}
			return wallRun{goesOn: rewalk && len(tab.domNavs) == 1, stp: stp, typed: len(tab.typed), clicks: len(tab.clicks)}
		}
	}
	for _, w := range []struct {
		name, host string
		at         func(*testing.T, *fakeSync) wallRun
		// stops is false where the run goes on without a session (it presses
		// Google): only the session that took is this test's.
		stops bool
	}{
		{"a wall that offers only Google sign-in, Google sign-in off", "careers.example.test", googleOnly(false), true},
		{"a wall that offers only Google sign-in, Google sign-in on", "careers.example.test", googleOnly(true), false},
		{`a job board's "sign in to apply"`, "www.indeed.com", board, true},
		{"LinkedIn's login page", "www.linkedin.com", linkedIn(false), true},
		{"LinkedIn's signed-out posting", "www.linkedin.com", linkedIn(true), true},
	} {
		for _, took := range []bool{true, false} {
			if !took && !w.stops {
				continue
			}
			sy := &fakeSync{res: OwnHostResult{Imported: took}}
			if !took {
				sy.res.Reason = "no-cookies"
			}
			o := w.at(t, sy)
			switch {
			case len(sy.calls) != 1 || sy.calls[0] != w.host:
				t.Errorf("%s (session took %v): asked %v, want exactly %s once", w.name, took, sy.calls, w.host)
			case o.typed != 0:
				t.Errorf("%s (session took %v): typed %d value(s) at the wall", w.name, took, o.typed)
			case took && (!o.goesOn || o.stp != nil || o.clicks != 0):
				t.Errorf("%s: the session took, yet goes on %v stop %+v clicks %d", w.name, o.goesOn, o.stp, o.clicks)
			case !took && (o.stp == nil || o.stp.class != ClassCredential || o.stp.board != "sign-in" || !strings.Contains(o.stp.reason, "no sign-in for "+w.host)):
				t.Errorf("%s: without a session the stop is %+v", w.name, o.stp)
			}
		}
	}
}

func TestApply_Account_AdoptsGateTabAndReturnsUnusedOne(t *testing.T) {
	ctx := context.Background()
	a, wr := gateRig(t, loginPage(""), nil)
	rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
	if stp != nil || !rewalk {
		t.Fatalf("rewalk %v stop %+v", rewalk, stp)
	}
	if a.e.gate == nil || a.e.gate.tab != Tab(a.tab) || a.e.gate.adopted != 1 || len(a.tab.navs) != 0 {
		t.Fatalf("gate %+v navs %v: the stage works on the walker's own tab, with no fresh navigation", a.e.gate, a.tab.navs)
	}
	// The sign-up never touched it: the tab is still tagged and still open.
	if a.tab.closed || a.tab.kept {
		t.Fatal("an adopted tab that sign-up never used was closed or kept away from the gate tag")
	}
	// A tab closed under the run is left alone: nothing is typed, pressed or
	// kept in its place.
	b, wr := gateRig(t, loginPage(""), nil)
	b.s.tab = closedTab{b.tab}
	rewalk, stp = b.g.r.reenterGate(ctx, b.e, b.s, wr)
	if stp != nil || rewalk || len(b.tab.typed) != 0 || len(b.tab.clicks) != 0 || b.tab.kept {
		t.Fatalf("rewalk %v stop %+v typed %v clicks %v", rewalk, stp, b.tab.typed, b.tab.clicks)
	}
}

func TestApply_Account_ConfirmedLoginGateIsCredentialNotDefect(t *testing.T) {
	ctx := context.Background()
	t.Run("a gate the second look confirms is the user's credential", func(t *testing.T) {
		a, wr := gateRig(t, loginPage(""), nil)
		a.signInRight("never")
		_, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp == nil || stp.class != ClassCredential {
			t.Fatalf("stop %+v", stp)
		}
		// Re-entries are bounded.
		c, wr := gateRig(t, loginPage(""), nil)
		c.e.reentries = MaxGateReentries
		rewalk, stp := c.g.r.reenterGate(ctx, c.e, c.s, wr)
		if rewalk || stp != nil || len(c.tab.typed) != 0 {
			t.Fatalf("rewalk %v stop %+v typed %v: no third re-entry", rewalk, stp, c.tab.typed)
		}
		stop := c.g.r.afterWalk(ctx, c.e, c.s, t0, wr, nil)
		if stop == nil || stop.class != ClassCredential || stop.board != "sign-in" {
			t.Fatalf("stop %+v", stop)
		}
	})
	t.Run("a plain login control is not a password gate and triggers no account retry", func(t *testing.T) {
		a, wr := gateRig(t, ctaPage(), nil)
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if rewalk || stp != nil || a.e.reentries != 0 || len(a.tab.typed) != 0 || len(a.e.Run.Signins) != 1 || len(a.e.Run.Signins[0].Tried) != 0 {
			t.Fatalf("rewalk %v stop %+v re-entries %d typed %v", rewalk, stp, a.e.reentries, a.tab.typed)
		}
		if stop := a.g.r.afterWalk(ctx, a.e, a.s, t0, wr, nil); stop == nil || stop.class != ClassCredential {
			t.Fatalf("stop %+v", stop)
		}
	})
	t.Run("an employee-only sign-in is the posting's, not the user's credential", func(t *testing.T) {
		// Publicis Groupe's employees- iCIMS portal: an employee login and no
		// box; its candidates' portal answers 410 for the same posting.
		staff := func(more ...string) *PageState {
			p := &PageState{URL: loginURL, Title: "Login", Text: "Employee Login Log back in! In order to apply for this job, you must create a profile with us.",
				Controls: []Control{{I: 1, Tag: "a", Labels: []string{"Log back in!"}, Form: -1}}}
			for i, l := range more {
				p.Controls = append(p.Controls, Control{I: 2 + i, Tag: "a", Labels: []string{l}, Form: -1})
			}
			return p
		}
		a, wr := gateRig(t, staff(), nil)
		if _, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr); stp == nil || stp.class != ClassPosting || !strings.Contains(stp.reason, "employer's own staff") {
			t.Fatalf("stop %+v", stp)
		}
		// A page that also creates an account is the candidates' own sign-in.
		b, wr := gateRig(t, staff("Create an account"), nil)
		if _, stp := b.g.r.reenterGate(ctx, b.e, b.s, wr); stp != nil {
			t.Fatalf("stop %+v", stp)
		}
		if stop := b.g.r.afterWalk(ctx, b.e, b.s, t0, wr, nil); stop == nil || stop.class != ClassCredential {
			t.Fatalf("stop %+v", stop)
		}
	})
	t.Run("a gate the second look does not confirm is not the user's credential", func(t *testing.T) {
		a, wr := gateRig(t, formState(), nil)
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if rewalk || stp != nil {
			t.Fatalf("rewalk %v stop %+v", rewalk, stp)
		}
		stop := a.g.r.afterWalk(ctx, a.e, a.s, t0, wr, nil)
		if stop == nil || stop.class != ClassDefect {
			t.Fatalf("stop %+v", stop)
		}
	})
}

func TestApply_Run_AWalkThatMeetsALoginGateReentersTheAccountStageOnItsTabAndWalksAgain(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) {
		d.Page.Timing = WalkTiming{AfterForm: 200 * time.Millisecond, First: 100 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 100 * time.Millisecond}
	})
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.g.env.window = scriptWindow{a.tab}
	posting := &PageState{
		URL: loginURL, Title: "Platform Engineer - Example Corp", Text: "Platform Engineer", Lines: []string{"Platform Engineer"}, Headings: []string{"Platform Engineer"},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Apply"}, Form: -1}},
	}
	a.tab.set(posting)
	signedIn := false
	a.tab.onClick = func(tab *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Apply":
			tab.set(loginPage("")) // the gate exists only after the click
		case "Sign in":
			tab.typed = map[int]string{}
			signedIn = true
			f := formState()
			f.URL = loginURL
			tab.set(f)
		}
	}
	run := a.g.r.RunOne(context.Background(), Input{URL: loginURL, Caller: gates.CallerCLI})
	if run.Outcome.Kind != OutcomeFilled || !signedIn {
		t.Fatalf("outcome %+v signed in %v; stages %+v", run.Outcome, signedIn, run.Stages)
	}
	if len(a.tab.navs) != 1 {
		t.Fatalf("navigations %v: the posting is opened once and the gate tab is never reloaded", a.tab.navs)
	}
	if n := a.tab.page.nFills; n != 1 {
		t.Fatalf("%d fills: the application page is filled once, after the gate was cleared", n)
	}
	accountLines := 0
	for _, l := range run.Stages {
		if l.Stage == StageAccount {
			accountLines++
		}
	}
	if accountLines != 2 || !strings.Contains(run.Stages[2].Detail+run.Stages[3].Detail, "re-entry at the login gate") {
		t.Fatalf("stages %+v", run.Stages)
	}
}

// An email-first sign-in as the probe reads Eightfold's dialog: one email box
// (at handle at), its Continue, a Google button and Create an account, with a
// cookie banner's furniture box behind it.
func efEmail(heading, said string, at int) *PageState {
	return &PageState{
		URL: loginURL, Title: "Careers", Headings: []string{heading}, Modal: true,
		Text: heading + " Email Continue First time here? Create an account " + said, Lines: []string{heading, said},
		Inputs: []InputInfo{
			{I: at, Tag: "input", Type: "email", Label: "Email", ID: "auth-entry-email-input", Form: -1, Modal: true},
			{I: 90, Tag: "input", Type: "checkbox", Label: "Performance Cookies", Form: -1, Furniture: true},
		},
		Controls: []Control{
			{I: at + 1, Tag: "button", Labels: []string{"Continue"}, Form: -1, Modal: true},
			{I: at + 2, Tag: "button", Labels: []string{"Sign in using Google"}, Form: -1, Modal: true},
			{I: at + 3, Tag: "button", Labels: []string{"Create an account"}, Form: -1, Modal: true},
		},
	}
}

const (
	efChoice = "#qc_data_use-choice-0"
	efAgree  = "I have read and agree to the Privacy Policy and the Interview Policy"
	efAlerts = "I would like to receive AI-powered job alerts for roles that match my profile."
	efAny    = "Use my profile data for ANY ROLES GLOBALLY THAT MAY MATCH MY SKILLS, including those I haven't applied to"
	efOnly   = "Use my profile data for ONLY THE ROLE I AM APPLYING FOR. I will NOT be contacted about other roles"
)

// efReview is the step before a new account: the required agreement, the
// data-use choice and the optional job-alert box, and Submit.
func efReview() *PageState {
	return &PageState{
		URL: loginURL, Title: "Careers", Headings: []string{"Please review before continuing"}, Modal: true,
		Text: "Please review before continuing " + efAgree, Lines: []string{"Please review before continuing", efAgree},
		Inputs: []InputInfo{
			{I: 10, Tag: "input", Type: "checkbox", Label: efAgree, Name: "qc_privacy_and_interview_policy-consent-checkbox", Required: true, Form: -1, Modal: true},
			// Its options name the talent community, so the probe reads the radio as furniture.
			{I: 11, Tag: "input", Type: "radio", Selector: efChoice, Label: "Use my profile data", Form: -1, Modal: true, Furniture: true},
			{I: 12, Tag: "input", Type: "checkbox", Label: efAlerts, Form: -1, Modal: true},
		},
		Controls: []Control{{I: 13, Tag: "button", Labels: []string{"Submit"}, Form: -1, Modal: true}},
	}
}

// efCode is the step that takes the mailed code: six one-character boxes.
func efCode(said string) *PageState {
	st := &PageState{
		URL: loginURL, Title: "Careers", Headings: []string{"Verify your email"}, Modal: true,
		Text: "Verify your email. Enter the code sent to your email. " + said, Lines: []string{"Verify your email", said},
		Controls: []Control{{I: 46, Tag: "button", Labels: []string{"Submit"}, Form: -1, Modal: true}},
	}
	for k := 0; k < 6; k++ {
		st.Inputs = append(st.Inputs, InputInfo{I: 40 + k, Tag: "input", Type: "text", Label: fmt.Sprintf("Please enter verification code. Digit %d", k+1), MaxLength: 1, Form: -1, Modal: true})
	}
	return st
}

// efRig is the account stage at an email-first sign-in the walk stopped on:
// a run the user started, the profile's address, one code mail. Each press
// moves the clock a minute after it landed.
func efRig(t *testing.T, legal bool, mod func(*Deps)) (*acctEnv, *walkResult, *fakeCodes) {
	t.Helper()
	fc := &fakeCodes{replies: []codeReply{mailed("482916", mailFrom("no-reply", "eightfold.ai"), "Verify your email", t0.Add(10*time.Minute))}}
	mods := []func(*Deps){func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode }}
	if legal {
		mods = append(mods, settingOn)
	}
	if mod != nil {
		mods = append(mods, mod)
	}
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, UserStarted: true}, ATSGeneric, "careers.example.test", signupMod("United States", mods...))
	page := efEmail("Sign in", "", 0)
	a.tab.set(page)
	a.s.entry = page
	a.tab.choices = map[string][]Choice{efChoice: {{I: 20, Name: efAny}, {I: 21, Name: efOnly}}}
	return a, &walkResult{End: EndLoginRequired, Detail: "the page is a bare sign-in gate", Last: page}, fc
}

// script answers each press of the email-first sign-in with the page next.
func (a *acctEnv) script(next func(tab *scriptTab, label, heading string)) {
	a.tab.onClick = func(tab *scriptTab, c Control) {
		if len(c.Labels) > 0 {
			next(tab, c.Labels[0], tab.state.Headings[0])
			a.g.now = a.g.now.Add(time.Minute)
		}
	}
}

func typedCode(tab *scriptTab) string {
	code := ""
	for k := 40; k < 46; k++ {
		code += tab.typed[k]
	}
	return code
}

// The walk stops at Eightfold's sign-in dialog, which has no password box: the
// session is asked for first, then the stage types the profile's email and
// presses Continue, and types the code mailed after that press. A code the
// site refuses is tried once.
func TestApply_Account_EmailFirstSigninTypesTheCodeMailedAfterContinue(t *testing.T) {
	ctx := context.Background()
	t.Run("an address the site knows signs in with its code", func(t *testing.T) {
		sy := &fakeSync{res: OwnHostResult{Reason: "extension-silent"}}
		a, wr, fc := efRig(t, false, func(d *Deps) { d.Page.Sessions = sy })
		// The application's first page asks only for boxes: no review of the
		// dialog.
		boxes := &PageState{URL: loginURL, Title: "Apply", Headings: []string{"Application questions"}, Forms: []FormInfo{{Fields: 1}},
			Inputs:   []InputInfo{{I: 60, Tag: "input", Type: "checkbox", Label: "I am 18 or older", Required: true, Form: 0}},
			Controls: []Control{{I: 61, Tag: "button", Labels: []string{"Next"}, Form: 0}}}
		a.script(func(tab *scriptTab, label, heading string) {
			switch label {
			case "Continue":
				tab.set(efCode(""))
			case "Submit":
				tab.set(boxes)
			}
		})
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp != nil || !rewalk {
			t.Fatalf("rewalk %v stop %+v detail %q", rewalk, stp, wr.Detail)
		}
		if len(sy.calls) != 1 || a.tab.typed[0] != "alex@example.com" || typedCode(a.tab) != "482916" {
			t.Fatalf("sync %v typed %v: the session first, then the email and the code", sy.calls, a.tab.typed)
		}
		if got := strings.Join(a.tab.clicks, ","); got != "Continue,Submit" {
			t.Fatalf("pressed %s: no account is created and Google is never pressed", got)
		}
		// Continue was pressed at t0; the lookup ran a minute later and reads
		// only mail from the second before the press on.
		if len(fc.since) == 0 || !fc.since[0].Equal(t0.Add(-time.Second)) {
			t.Fatalf("lookup since %v", fc.since)
		}
		if a.e.Run.Signins[0].Succeeded != "email-code" || strings.Contains(stringify(a.e.Run), "482916") {
			t.Fatalf("record %+v", a.e.Run.Signins[0])
		}
	})
	t.Run("a page that reloads after the code reads as signed in", func(t *testing.T) {
		a, wr, _ := efRig(t, false, nil)
		// Qualcomm: the dialog closes, the page goes between documents (a read
		// fails), then the application page draws.
		a.script(func(tab *scriptTab, label, heading string) {
			switch label {
			case "Continue":
				tab.set(efCode(""))
			case "Submit":
				tab.script(&PageState{URL: loginURL, Title: "Apply"}, nil, appPage())
			}
		})
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp != nil || !rewalk || a.e.Run.Signins[0].Succeeded != "email-code" {
			t.Fatalf("rewalk %v stop %+v", rewalk, stp)
		}
	})
	t.Run("a code the site refuses stops as the user's code step", func(t *testing.T) {
		a, wr, fc := efRig(t, false, nil)
		a.script(func(tab *scriptTab, label, heading string) {
			tab.set(efCode("That code is not valid."))
		})
		_, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp == nil || stp.class != ClassVerification || len(fc.since) != 1 || strings.Contains(stp.reason, "482916") {
			t.Fatalf("stop %+v lookups %d", stp, len(fc.since))
		}
	})
}

// An address the site does not know gets an account through the dialog: the
// agreement only under the legal setting, the data-use choice limited to the
// role, never the job-alert box; then the sign-up's own email step, and the
// code mailed after that last Continue.
func TestApply_Account_EmailFirstUnknownAddressCreatesTheAccountInTheDialog(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, legal, signupEmail, late bool) (*acctEnv, *fakeCodes, *stop, []string) {
		a, wr, fc := efRig(t, legal, nil)
		var atSubmit []string
		a.script(func(tab *scriptTab, label, heading string) {
			switch {
			case label == "Continue" && heading == "Sign in" && late:
				// Qualcomm: the dialog redraws before it says why.
				tab.script(efEmail("Sign in", "Checking", 0), efEmail("Sign in", "We don’t recognize this email. Create a new account", 0))
			case label == "Continue" && heading == "Sign in":
				tab.set(efEmail("Sign in", "We don’t recognize this email. Create a new account", 0))
			case label == "Create an account":
				tab.set(efReview())
			case label == "Submit" && heading == "Please review before continuing":
				atSubmit = append([]string(nil), tab.inputClicks...)
				for _, c := range tab.choices[efChoice] {
					if c.Checked {
						atSubmit = append(atSubmit, "chosen: "+c.Name)
					}
				}
				if !signupEmail {
					tab.set(efCode(""))
					return
				}
				tab.set(efEmail("Sign Up", "", 30))
			case label == "Continue":
				tab.set(efCode(""))
			case label == "Submit":
				tab.set(appPage())
			}
		})
		rewalk, stp := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		if stp == nil && !rewalk {
			t.Fatalf("no stop and no walk again: %q", wr.Detail)
		}
		return a, fc, stp, atSubmit
	}
	t.Run("legal setting on", func(t *testing.T) {
		a, fc, stp, atSubmit := run(t, true, true, false)
		if stp != nil {
			t.Fatalf("stop %+v", stp)
		}
		want := []string{efOnly, efAgree, "chosen: " + efOnly}
		if strings.Join(atSubmit, "|") != strings.Join(want, "|") {
			t.Fatalf("at the review's Submit: %q, want %q", atSubmit, want)
		}
		if a.tab.typed[0] != "alex@example.com" || a.tab.typed[30] != "alex@example.com" || typedCode(a.tab) != "482916" {
			t.Fatalf("typed %v", a.tab.typed)
		}
		// Presses at t0 (Continue), +1 (Create), +2 (Submit), +3 (the sign-up's
		// Continue): the code is the one mailed after the last.
		if len(fc.since) != 1 || !fc.since[0].Equal(t0.Add(3*time.Minute-time.Second)) {
			t.Fatalf("lookup since %v", fc.since)
		}
		if a.e.Run.Signins[0].Succeeded != "created-account" || a.g.count(`SELECT count(*) FROM signup_attempts`) != 1 {
			t.Fatalf("record %+v", a.e.Run.Signins[0])
		}
	})
	t.Run("the review's Submit mails the code", func(t *testing.T) {
		a, fc, stp, _ := run(t, true, false, false)
		if stp != nil || typedCode(a.tab) != "482916" || len(fc.since) != 1 || !fc.since[0].Equal(t0.Add(2*time.Minute-time.Second)) {
			t.Fatalf("stop %+v lookup since %v", stp, fc.since)
		}
	})
	t.Run("the dialog's words come after it redraws", func(t *testing.T) {
		a, _, stp, _ := run(t, true, true, true)
		if stp != nil || a.e.Run.Signins[0].Succeeded != "created-account" {
			t.Fatalf("stop %+v record %+v", stp, a.e.Run.Signins)
		}
	})
	t.Run("legal setting off", func(t *testing.T) {
		a, fc, stp, _ := run(t, false, true, false)
		if stp == nil || stp.class != ClassCredential || !strings.Contains(stp.reason, "autoAcceptLegalDeclarations is off") {
			t.Fatalf("stop %+v", stp)
		}
		if len(a.tab.inputClicks) != 0 || len(fc.since) != 0 || strings.Count(strings.Join(a.tab.clicks, ","), "Submit") != 0 {
			t.Fatalf("ticked %v pressed %v lookups %d: nothing is ticked or submitted", a.tab.inputClicks, a.tab.clicks, len(fc.since))
		}
	})
}

// A tenant that asks for a password after Continue gets the password sign-in;
// no code is waited for.
func TestApply_Account_EmailFirstPasswordAfterContinueIsThePasswordSignin(t *testing.T) {
	a, wr, fc := efRig(t, false, nil)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.signInRight("Default-Pw-1!x")
	signIn := a.tab.onClick
	a.tab.onClick = func(tab *scriptTab, c Control) {
		if c.Labels[0] == "Continue" {
			tab.set(loginPage(""))
			return
		}
		signIn(tab, c)
	}
	rewalk, stp := a.g.r.reenterGate(context.Background(), a.e, a.s, wr)
	if stp != nil || !rewalk || a.e.Run.Signins[0].Succeeded != "settings-default" || a.wrong != 0 || len(fc.since) != 0 {
		t.Fatalf("rewalk %v stop %+v record %+v lookups %d", rewalk, stp, a.e.Run.Signins[0], len(fc.since))
	}
}

// At an entry page that is an email-first sign-in, the session from the
// user's Chrome comes first; a session that leaves the sign-in standing is no
// sign-in.
func TestApply_Account_EmailFirstEntryAsksTheSessionFirst(t *testing.T) {
	for _, c := range []struct {
		name      string
		stays     bool
		detail    string
		typedMail bool
	}{
		{"the session signs in", false, "imported session", false},
		{"the sign-in stays after the session", true, "code mailed to you", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, _, _ := efRig(t, false, func(d *Deps) { d.Page.Sessions = &fakeSync{res: OwnHostResult{Imported: true}} })
			a.tab.onNav = func(tab *scriptTab, _ string) {
				if c.stays {
					tab.set(efEmail("Sign in", "", 0))
					return
				}
				tab.set(appPage())
			}
			a.script(func(tab *scriptTab, label, heading string) {
				if label == "Continue" {
					tab.set(efCode(""))
					return
				}
				tab.set(appPage())
			})
			d, stp := a.run()
			if stp != nil || !strings.Contains(d, c.detail) || (a.tab.typed[0] != "") != c.typedMail {
				t.Fatalf("detail %q stop %+v typed %v", d, stp, a.tab.typed)
			}
		})
	}
}

func TestApply_Run_APanicInTheBrowserStagesEndsTheRunAsADefectWithItsRecordAndTab(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) {
		d.Page.Timing = WalkTiming{AfterForm: 200 * time.Millisecond, First: 100 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 100 * time.Millisecond}
	})
	a.g.env.window = scriptWindow{a.tab}
	a.tab.set(&PageState{
		URL: loginURL, Title: "Platform Engineer - Example Corp", Text: "Platform Engineer", Lines: []string{"Platform Engineer"}, Headings: []string{"Platform Engineer"},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Apply"}, Form: -1}},
	})
	a.tab.onClick = func(*scriptTab, Control) {
		var m map[string]int
		m["apply"]++
	}
	run := a.g.r.RunOne(context.Background(), Input{URL: loginURL, Caller: gates.CallerCLI})
	mustStop(t, run, StageFill, ClassDefect)
	if !strings.Contains(run.Outcome.Reason, "internal error: assignment to entry in nil map") || !strings.Contains(run.Outcome.Reason, "account_gate_test.go:") {
		t.Fatalf("reason %q: it names neither the panic nor where it came from", run.Outcome.Reason)
	}
	if n := a.g.count(`SELECT count(*) FROM apply_runs WHERE run_id = ? AND outcome_kind = 'stopped' AND finished_at IS NOT NULL`, run.ID); n != 1 {
		t.Fatal("the run record still reads as running")
	}
	if !a.tab.kept || a.tab.closed {
		t.Fatalf("kept %v closed %v: the page the run stopped on is the user's to finish", a.tab.kept, a.tab.closed)
	}
}
