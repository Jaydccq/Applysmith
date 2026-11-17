package apply

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

const (
	wdPostURL = "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/Durham-NC/Platform-Engineer_R1/apply/applyManually"
)

// driveEnv is a run driven end to end over a scripted tab: preflight is done,
// the first page is first.
func newDrive(t *testing.T, in Input, first *PageState, mod func(*Deps)) *acctEnv {
	t.Helper()
	a := newAcct(t, in, ATSGeneric, "careers.example.test", func(d *Deps) {
		d.Page.Timing = WalkTiming{AfterForm: 200 * time.Millisecond, First: 80 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 150 * time.Millisecond}
		d.Page.Check = CheckTiming{Reread: time.Millisecond, Settle: time.Millisecond}
		d.Page.Keep = &Keeper{}
		if mod != nil {
			mod(d)
		}
	})
	a.tab.set(first)
	a.tab.page = &fakePage{}
	a.e.Ver = &Verdict{Window: scriptWindow{a.tab}}
	a.e.Cls = Classification{ATS: ATSGeneric, Tenant: "careers.example.test", ApplyURL: first.URL, Posting: first.URL}
	return a
}

func (a *acctEnv) drive() *stop { return a.g.r.drivePages(context.Background(), a.e) }

func stagesOf(r *Run, st Stage) []StageLine {
	var out []StageLine
	for _, l := range r.Stages {
		if l.Stage == st {
			out = append(out, l)
		}
	}
	return out
}

func postingPage(controls ...Control) *PageState {
	return &PageState{URL: pageURL, Title: "Platform Engineer - Example Corp", Headings: []string{"Platform Engineer"}, Text: "Platform Engineer", Lines: []string{"Platform Engineer"}, Controls: controls}
}

func answeredRows() *engine.PageResult {
	return doneResult(engine.Row{Selector: "#q", Label: "Will you require visa sponsorship?", Kind: "radio", Outcome: "filled", Value: "No"})
}

func TestApply_Handoff_LandingOnOtherATSAddsResolveAndAccountLines(t *testing.T) {
	a := newDrive(t, Input{URL: pageURL, Caller: gates.CallerCLI, Tool: ToolAutofill}, postingPage(ctl(0, "button", -1, "Apply now")), nil)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	wd := loginPage("")
	wd.URL = wdPostURL
	a.tab.page.fills = []*engine.PageResult{answeredRows()}
	a.tab.onClick = func(t *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Apply now":
			t.set(wd)
		case "Sign in":
			t.typed = map[int]string{}
			t.set(appPage())
		}
	}
	if st := a.drive(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	run := a.e.Run
	var seq []string
	for _, l := range run.Stages {
		if l.Stage == StageResolve || l.Stage == StageAccount {
			seq = append(seq, string(l.Stage)+": "+l.Detail)
		}
	}
	if len(seq) != 3 || !strings.HasPrefix(seq[0], "account") || seq[1] != "resolve: landed on ats=workday tenant=example" || !strings.HasPrefix(seq[2], "account: re-entry at the login gate") {
		t.Fatalf("timeline %q", seq)
	}
	if run.ATS != ATSWorkday || run.Tenant != "example" || run.Input.URL != pageURL {
		t.Fatalf("ats %s tenant %s: the run keeps the posting URL and takes the landing's system (%s)", run.ATS, run.Tenant, run.Input.URL)
	}
	if run.Stages[len(run.Stages)-1].Stage == StageAccount && a.tab.page.nFills == 0 {
		t.Fatal("the walk did not repeat from the landing")
	}
}

func TestApply_RoleGuard_AccountPageReentersAccountOnce(t *testing.T) {
	reg := &PageState{
		URL: "https://careers.example.test/account/register", Title: "Example Corp | Register", Text: "Almost there", Lines: []string{"Almost there"},
		Inputs:   []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "First name", Form: -1}, {I: 1, Tag: "input", Type: "email", Label: "Email", Form: -1}},
		Controls: []Control{ctl(2, "button", -1, "Continue")},
	}
	// The walk ends at it as a sign-in, not as a stale link, and fills nothing.
	w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI, Company: "Example Corp", Role: "Platform Engineer"}, reg)
	res, st := w.walk()
	if st != nil || res.End != EndLoginRequired || res.End.Class(false) != ClassCredential || w.page.nFills != 0 || len(w.tab.typed) != 0 || len(w.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v fills %d: an account page is a credential stop, never a different role", res, st, w.page.nFills)
	}
	// The account stage is entered once more at that address; what remains is a
	// credential stop.
	a, wr := gateRig(t, reg, func(d *Deps) { d.Page.Sessions = &fakeSync{res: OwnHostResult{Reason: "no-cookies"}} })
	for i := 0; i < 2; i++ {
		if again, stp := a.g.r.reenterGate(context.Background(), a.e, a.s, wr); again || stp != nil {
			t.Fatalf("again %v stop %+v", again, stp)
		}
	}
	stp := a.g.r.afterWalk(context.Background(), a.e, a.s, t0, wr, nil)
	if stp == nil || stp.class != ClassCredential || stp.board != "sign-in" || strings.Contains(stp.reason, "stale link") {
		t.Fatalf("stop %+v", stp)
	}
	t.Run("a page named for a role that merely contains the word is a role", func(t *testing.T) {
		st := reg
		st.Title, st.Headings = "Registered Nurse", []string{"Registered Nurse"}
		if accountPageFirst(st) {
			t.Fatal("Registered Nurse is a job")
		}
		for _, title := range []string{"Sign In | Acme Corp", "Acme - Log in", "Create an Account", "Acme Login"} {
			st.Title, st.Headings = title, nil
			if !accountPageFirst(st) {
				t.Errorf("%q names an account page", title)
			}
		}
	})
}

func TestApply_Account_GateReentryBounds(t *testing.T) {
	ctx := context.Background()
	gateAt := func(path string) *PageState {
		w := loginPage("")
		w.URL = "https://careers.example.test" + path
		return w
	}
	a, _ := gateRig(t, gateAt("/signin/a"), nil)
	enter := func(path string) (*walkResult, bool, *stop) {
		pg := gateAt(path)
		a.tab.set(pg)
		wr := &walkResult{End: EndLoginRequired, Detail: "the page asks for a sign-in", Last: pg}
		again, st := a.g.r.reenterGate(ctx, a.e, a.s, wr)
		return wr, again, st
	}
	if _, again, st := enter("/signin/a"); !again || st != nil {
		t.Fatalf("first gate: again %v stop %+v", again, st)
	}
	if wr, again, _ := enter("/signin/a"); again || !strings.Contains(wr.Detail, "already worked at this address") {
		t.Fatalf("an address seen twice ends the stage: again %v detail %q", again, wr.Detail)
	}
	if a.e.reentries != 1 {
		t.Fatalf("%d re-entries after one gate and a repeat", a.e.reentries)
	}
	a.e.reentries = MaxGateReentries
	if wr, again, _ := enter("/signin/c"); again || !strings.Contains(wr.Detail, "re-entered 2 times") {
		t.Fatalf("the second re-entry spent: again %v detail %q", again, wr.Detail)
	}
	// What remains is a credential stop with the evidence.
	wr := &walkResult{End: EndLoginRequired, Detail: "the page asks for a sign-in; the account stage was re-entered 2 times already", Last: gateAt("/signin/c")}
	if st := a.g.r.afterWalk(ctx, a.e, a.s, t0, wr, nil); st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "re-entered 2 times") {
		t.Fatalf("stop %+v", st)
	}
}

func TestApply_Handoff_GateTabBecomesTheOneKeptTab(t *testing.T) {
	t.Run("a sign-in stop keeps the run's tab as the one hand-off tab", func(t *testing.T) {
		reg := &PageState{URL: "https://careers.example.test/account/login", Title: "Sign In | Acme Corp", Inputs: []InputInfo{{I: 0, Tag: "input", Type: "email", Label: "Email", Form: -1}}, Controls: []Control{ctl(1, "button", -1, "Continue")}}
		a := newDrive(t, Input{URL: pageURL, Caller: gates.CallerCLI}, reg, nil)
		a.e.Cls.ApplyURL = pageURL
		// The page is an email-first sign-in and the profile holds no email:
		// the account stage stops there.
		if st := a.drive(); a.e.Run.Outcome.Class != ClassCredential {
			t.Fatalf("stop %+v outcome %+v", st, a.e.Run.Outcome)
		}
		if !a.tab.kept || a.tab.closed || a.g.r.d.Page.Keep.Count() != 1 {
			t.Fatalf("kept %v closed %v count %d", a.tab.kept, a.tab.closed, a.g.r.d.Page.Keep.Count())
		}
	})
	t.Run("a stop that is not a sign-in closes the tab", func(t *testing.T) {
		gone := &PageState{URL: pageURL, Title: "Platform Engineer", Text: "This job is no longer available", Lines: []string{"This job is no longer available."}}
		a := newDrive(t, Input{URL: pageURL, Caller: gates.CallerCLI}, gone, nil)
		st := a.drive()
		if st == nil || st.class != ClassPosting {
			t.Fatalf("stop %+v: a closed posting is a posting stop", st)
		}
		if a.tab.kept || !a.tab.closed || a.g.r.d.Page.Keep.Count() != 0 {
			t.Fatalf("kept %v closed %v count %d", a.tab.kept, a.tab.closed, a.g.r.d.Page.Keep.Count())
		}
	})
	t.Run("a second kept tab of one run replaces the first", func(t *testing.T) {
		k := &Keeper{}
		first, second, other := newScriptTab(appPage()), newScriptTab(appPage()), newScriptTab(appPage())
		k.Admit(context.Background(), first, "run_1")
		k.Admit(context.Background(), other, "run_2")
		k.Admit(context.Background(), second, "run_1")
		if k.Count() != 2 {
			t.Fatalf("count %d", k.Count())
		}
		if first.closed == second.closed || other.closed {
			t.Fatalf("first closed %v second closed %v other closed %v: only the run's own earlier tab is closed", first.closed, second.closed, other.closed)
		}
	})
	t.Run("an unreachable app browser stops the run at account as environment, blocked on the board", func(t *testing.T) {
		a := newDrive(t, Input{URL: pageURL, Caller: gates.CallerCLI}, postingPage(), nil)
		a.e.Ver = &Verdict{}
		st := a.drive()
		if st == nil || st.class != ClassEnvironment || st.at != StageAccount || st.board != "lane-unavailable" {
			t.Fatalf("stop %+v", st)
		}
	})
}

// refusingWindow is an apply window whose tab does not open.
type refusingWindow struct{ err error }

func (w refusingWindow) OpenTab(context.Context, string, engine.Host) (Tab, error) { return nil, w.err }

func TestApply_Walk_ChatWidgetUploadIsFurniture(t *testing.T) {
	chat := postingPage(ctl(1, "a", -1, "Apply for this position"))
	chat.Inputs = []InputInfo{{I: 0, Tag: "input", Type: "file", Label: "candidate_upload_resume", Form: -1, Furniture: true}}
	chat.Controls[0].Href = "https://careers.example.test/apply/7"
	if chat.HasForm() {
		t.Fatal("a chat widget's résumé input made the posting read as an application")
	}
	w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, chat)
	w.tab.onClick = func(tab *scriptTab, c Control) { tab.set(appPage()) }
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || len(w.tab.clicks) != 1 || w.tab.clicks[0] != "Apply for this position" {
		t.Fatalf("end %+v stop %+v clicks %v: the real Apply link is followed", res, st, w.tab.clicks)
	}
	// The same input on a page without the widget is an application's.
	chat.Inputs[0].Furniture = false
	if !chat.HasForm() {
		t.Fatal("an ordinary résumé input is a form")
	}
}

func TestApply_Walk_BoardSignInToApplyKeepsTabNoSignup(t *testing.T) {
	board := &PageState{URL: "https://www.glassdoor.com/job-listing/platform-engineer-JV7", Title: "Platform Engineer", Text: "Platform Engineer", Lines: []string{"Platform Engineer"},
		Controls: []Control{ctl(0, "button", -1, "Sign in to apply")}}
	a := newDrive(t, Input{URL: board.URL, Caller: gates.CallerCLI}, board, nil)
	a.e.Cls = Classification{ATS: ATSGeneric, Tenant: "www.glassdoor.com", ApplyURL: board.URL, Posting: board.URL}
	st := a.drive()
	if st == nil || st.class != ClassCredential || st.board != "sign-in" || !strings.Contains(st.reason, "login-required") {
		t.Fatalf("stop %+v", st)
	}
	if n := len(stagesOf(a.e.Run, StageAccount)); n != 1 || len(a.tab.typed) != 0 || len(a.tab.clicks) != 0 {
		t.Fatalf("account lines %d typed %v clicks %v: no account is created on a board", n, a.tab.typed, a.tab.clicks)
	}
	if !a.tab.kept {
		t.Fatal("the tab was not kept for the user")
	}
	t.Run("a list of other jobs' sign-in buttons is not this posting's wall", func(t *testing.T) {
		list := postingPage(ctl(0, "button", -1, "Sign in to apply"), ctl(1, "button", -1, "Sign in to apply"), ctl(2, "button", -1, "Sign in to apply"))
		if signInWall(list) != "" || signInWall(board) != "Sign in to apply" {
			t.Fatal("one control is the wall, several are other jobs'")
		}
	})
	t.Run("an employer's own sign-in wall is not a board's", func(t *testing.T) {
		if isBoardURL("https://careers.example.test/jobs/7") || !isBoardURL("https://www.glassdoor.com/x") || !isBoardURL("https://www.indeed.com/viewjob?jk=1") || !isBoardURL("https://smartapply.indeed.com/x") {
			t.Fatal("boards: the mirrors and Indeed")
		}
	})
}

func TestApply_Walk_StallHintDoesNotFeedRefusalReader(t *testing.T) {
	st := wizardStep(1, 3)
	st.Stall = StallHints{Messages: []string{"Please select a state"}, Empty: []string{"State"}}
	if validationWords(st) != "" {
		t.Fatal("the hints reached the refusal reader")
	}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st)
	res, stp := w.walk()
	if stp != nil || res.End != EndStalled || !strings.Contains(res.Detail, "the page says: Please select a state") || !strings.Contains(res.Detail, `1 required field(s) still empty: "State"`) {
		t.Fatalf("end %+v stop %+v", res, stp)
	}
	// A disabled Submit with no marked refusal still ends without the hints:
	// the repair and disabled-submit paths read "no refusal" as no refusal.
	last := wizardStep(2, 2)
	last.Controls[0].Disabled = true
	last.Stall = st.Stall
	w = newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, last)
	res, _ = w.walk()
	if res.End != EndSubmitDisabled || strings.Contains(res.Detail, "Please select") {
		t.Fatalf("end %+v", res)
	}
	// A page that marked its refusal keeps its own words.
	st.Validation = []string{"Phone is required"}
	if got := stallWords(st); !strings.Contains(got, "Phone is required") || strings.Contains(got, "Please select") {
		t.Fatalf("stall words %q", got)
	}
}

func TestApply_Walk_FormValidationRevalidateOncePerPageNeverSubmits(t *testing.T) {
	disabledLast := func() *PageState {
		st := wizardStep(2, 2)
		st.Controls[0].Disabled = true
		return st
	}
	t.Run("a refusal nobody can see is released and the Submit control re-read", func(t *testing.T) {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, disabledLast())
		w.tab.revalidate = &Revalidation{HiddenReleased: []string{"followUp"}, Revalidated: []string{"email"}}
		w.tab.onRevalidate = func(tab *scriptTab) { tab.state.Controls[0].Disabled = false }
		res, st := w.walk()
		if st != nil || res.End != EndSubmit || w.tab.revalidated != 1 || len(w.tab.clicks) != 0 {
			t.Fatalf("end %+v stop %+v asked %d clicks %v", res, st, w.tab.revalidated, w.tab.clicks)
		}
	})
	t.Run("what the library still refuses is named, by field name only", func(t *testing.T) {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, disabledLast())
		w.tab.revalidate = &Revalidation{Invalid: []string{"phone", "city"}}
		res, _ := w.walk()
		if res.End != EndSubmitDisabled || !strings.Contains(res.Detail, "the form library still refuses: phone, city") || w.tab.revalidated != 1 {
			t.Fatalf("end %+v asked %d", res, w.tab.revalidated)
		}
	})
	t.Run("a page with a validation message, or with no such form, is not re-validated into success", func(t *testing.T) {
		msg := disabledLast()
		msg.Validation = []string{"Phone is required"}
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, msg)
		res, _ := w.walk()
		if res.End != EndSubmitDisabled || w.tab.revalidated != 0 {
			t.Fatalf("end %+v asked %d", res, w.tab.revalidated)
		}
		w = newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, disabledLast())
		res, _ = w.walk()
		if res.End != EndSubmitDisabled || w.tab.revalidated != 1 {
			t.Fatalf("end %+v asked %d", res, w.tab.revalidated)
		}
	})
}

func TestApply_Walk_RedirectToSiteErrorPageIsClosedPostingFact(t *testing.T) {
	cases := []struct {
		name, final, text, want string
	}{
		{"the site's error page", "https://careers.example.test/errors/404", "Page not found", "redirected to the site's error page"},
		{"an error page that says so on a neutral path", "https://careers.example.test/oops", "Page not found", "redirected to the site's error page"},
		{"the careers home", "https://careers.example.test/careers", "Open roles", "redirected to the careers home"},
		{"the root", "https://careers.example.test/", "Welcome", "redirected to the careers home"},
		{"a sign-in page is not a closed posting", "https://careers.example.test/login", "Sign in", ""},
		{"another site is not the site's error page", "https://elsewhere.example.org/404", "Page not found", ""},
		{"the same place", pageURL, "Platform Engineer", ""},
	}
	for _, c := range cases {
		a := newDrive(t, Input{URL: pageURL, Caller: gates.CallerCLI}, &PageState{URL: pageURL, Title: "Page", Text: c.text, Lines: []string{c.text}}, nil)
		var told []discovery.Liveness
		a.g.r.d.PostingClosed = func(_ context.Context, url string, l discovery.Liveness) {
			if url == "https://careers.example.test/jobs/7/apply" {
				told = append(told, l)
			}
		}
		a.e.Cls.ApplyURL = "https://careers.example.test/jobs/7/apply"
		a.e.Cls.Posting = a.e.Cls.ApplyURL
		a.tab.redirects = map[string]string{a.e.Cls.ApplyURL: c.final}
		st := a.g.r.openApplication(context.Background(), a.e, &session{r: a.g.r, e: a.e, tab: a.tab, page: a.tab.page})
		closed := st != nil && st.class == ClassPosting && strings.Contains(st.reason, c.want) && c.want != ""
		if (c.want != "") != closed {
			t.Errorf("%s: stop %+v", c.name, st)
			continue
		}
		if want := map[bool]int{true: 1, false: 0}[closed]; len(told) != want || closed && (told[0].Verdict != discovery.LiveExpired || told[0].Reason != "redirect-lost-job") {
			t.Errorf("%s: liveness told %+v", c.name, told)
		}
		if c.want != "" && (len(a.e.Run.PostingFacts) != 1 || a.e.Run.PostingFacts[0] != "closed: "+c.want) {
			t.Errorf("%s: facts %v", c.name, a.e.Run.PostingFacts)
		}
		if c.want == "" && len(a.e.Run.PostingFacts) != 0 {
			t.Errorf("%s: facts %v", c.name, a.e.Run.PostingFacts)
		}
	}
}

func TestApply_Walk_OpenerVocabularyApplyForPosition(t *testing.T) {
	for label, want := range map[string]bool{
		"Apply for Position": true, "apply for position": true, "Apply for this position": true, "Apply Now": true,
		"Apply for Job": false, "Apply for Role": false, "Apply for Position now": false,
	} {
		st := &PageState{URL: pageURL, Controls: []Control{{I: 0, Tag: "a", Labels: []string{label}, Form: -1, Href: "https://careers.example.test/apply"}}}
		if got := len(Categorize(st, false).Openers) == 1; got != want {
			t.Errorf("%q: opener=%v want %v", label, got, want)
		}
	}
}

func TestApply_Walk_StopsRatherThanGuessOnUnsupportedControls(t *testing.T) {
	t.Run("a Next control is pressed", func(t *testing.T) {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
		w.tab.onClick = advanceOnNext(2)
		if res, st := w.walk(); st != nil || res.End != EndSubmit || len(w.tab.clicks) != 1 {
			t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
		}
	})
	t.Run("a control the walk does not know is never pressed", func(t *testing.T) {
		st := wizardStep(1, 2)
		st.Controls = []Control{ctl(1, "button", 0, "Save draft"), ctl(2, "button", 0, "Continue with LinkedIn"), ctl(3, "button", 0, "Autofill with my profile")}
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st)
		if res, stp := w.walk(); stp != nil || res.End != EndNoNavigation || len(w.tab.clicks) != 0 {
			t.Fatalf("end %+v stop %+v clicks %v", res, stp, w.tab.clicks)
		}
	})
	t.Run("a challenge page the solver cannot clear ends the walk", func(t *testing.T) {
		st := &PageState{URL: pageURL, Title: "Just a moment...", Text: "Verify you are human", Lines: []string{"Verify you are human"}}
		w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, st)
		if res, stp := w.walk(); stp != nil || res.End != EndBotChallenge || len(w.tab.clicks) != 0 {
			t.Fatalf("end %+v stop %+v clicks %v", res, stp, w.tab.clicks)
		}
	})
	t.Run("a required field the run could not answer blocks the verdict", func(t *testing.T) {
		st := wizardStep(2, 2)
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st)
		w.tab.page.fills = []*engine.PageResult{doneResult(engine.Row{Selector: "#x", Label: "Describe your favourite language", Kind: "text", Required: true, Outcome: "abstained", Reason: "no-answer"})}
		if _, stp := w.walk(); stp != nil {
			t.Fatalf("stop %+v", stp)
		}
		if v := w.s.finalVerdict(); v.Allowed() {
			t.Fatalf("verdict %+v: a required leftover must not be guessed", v)
		}
	})
}

func TestApply_Walk_TalentCommunityHomeIsFurnitureRedirectMeansGone(t *testing.T) {
	box := []InputInfo{{I: 0, Tag: "input", Type: "email", Label: "Email address", Form: -1, Furniture: true}}
	home := &PageState{URL: "https://careers.example.test/", Title: "Careers at Example Corp", Text: "Join our talent community", Lines: []string{"Join our talent community"},
		Inputs: box, Controls: []Control{ctl(1, "button", -1, "Join our talent community"), ctl(2, "button", -1, "Get job alerts"), ctl(3, "button", -1, "Sign up for job alerts"), ctl(4, "button", -1, "Sign up now"), ctl(5, "a", -1, "Search jobs")}}
	if home.HasForm() {
		t.Fatal("the talent-community box is a schema")
	}
	c := Categorize(home, false)
	if len(c.Submit)+len(c.Nav)+len(c.Openers) != 0 {
		t.Fatalf("categories %+v: none of these is a control the walk may press", c)
	}
	if res, st := func() (*walkResult, *stop) {
		w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, home)
		return w.walk()
	}(); st != nil || res.End != EndNoForm || res.End.Class(res.Offers) != ClassPosting {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	t.Run("a real application whose class merely says join is kept", func(t *testing.T) {
		app := formPage(ctl(0, "button", 0, "Submit application"))
		app.Forms[0].Text = "Join the team: tell us about yourself"
		if !app.HasForm() {
			t.Fatal("an application form was taken for furniture")
		}
	})
	t.Run("a redirect to the careers home names the posting as gone", func(t *testing.T) {
		if why := redirectedAway("https://careers.example.test/jobs/platform-engineer/7", "https://careers.example.test/", false); why != "redirected to the careers home" {
			t.Fatalf("why %q", why)
		}
		if why := redirectedAway("https://careers.example.test/jobs/7", "https://careers.example.test/jobs/7?utm=1", false); why != "" {
			t.Fatalf("why %q", why)
		}
	})
}

func TestApply_Submit_ContactBoilerplateOnlyNeverPressed(t *testing.T) {
	rows := func(rs ...engine.Row) []*pageState {
		ps := &pageState{report: PageReport{URL: pageURL, Step: 1}, rows: rs}
		return []*pageState{ps}
	}
	contact := []engine.Row{
		{Selector: "#loc", Label: "Location", Kind: "text", Outcome: "filled", Value: "Durham"},
		{Selector: "#em", Label: "Email address", Kind: "text", Outcome: "filled", Value: "alex@example.com"},
	}
	run := func(pages []*pageState) (*senv, *stop) {
		x := newSubmit(t, nil)
		x.s.pages = pages
		st := x.g.r.afterWalk(context.Background(), x.e, x.s, t0, x.wr, nil)
		return x, st
	}
	x, st := run(rows(contact...))
	if st == nil || st.class != ClassPosting || st.at != StageCheck || !strings.Contains(st.reason, "contact details") || x.tab.clicksOf("Submit application") != 0 || x.e.Run.Pressed {
		t.Fatalf("stop %+v clicks %v", st, x.tab.clicks)
	}
	for name, extra := range map[string]engine.Row{
		"a résumé":             {Selector: "#cv", Label: "Resume", Kind: "file", Outcome: "filled", FileName: "cv.pdf"},
		"an answered question": {Selector: "#q", Label: "Do you need sponsorship?", Kind: "radio", Outcome: "filled", Value: "No"},
		"a prefilled control":  {Selector: "#p", Label: "Phone", Kind: "text", Outcome: "prefilled"},
	} {
		_, st := run(rows(append(append([]engine.Row{}, contact...), extra)...))
		if st != nil && st.at == StageCheck && strings.Contains(st.reason, "contact details") {
			t.Errorf("%s is evidence of an application: %+v", name, st)
		}
	}
}

// A sentence that names a place, a question or a statement, is answered, so a
// form whose other rows are contact fields is an application; a label that
// names a place field stays contact data, even with help text after it. The
// wordings are those of real forms: a checkbox statement and a Yes/No without a
// question mark among them.
func TestApply_ContactLabel_ASentenceThatNamesAPlaceIsNotContactData(t *testing.T) {
	for label, contact := range map[string]bool{
		"Are you legally authorized to work in the country in which you are applying for a position?":                           false,
		"Do you now, or will you in the future, require visa sponsorship to work for Northwind Example in the country of hire?": false,
		"Are you legally authorized to work in the country in which you are applying":                                           false,
		"I am authorized to work in this country":                                                                               false,
		"I am legally eligible to work in the United States":                                                                    false,
		"Country":              true,
		"Country of residence": true,
		"Current location":     true,
		"State":                true,
		"State / Province":     true,
		"Phone country code":   true,
		"Current Location We use this to determine whether we can legally employ you in the entity where you are located.": true,
	} {
		if got := contactLabel(label); got != contact {
			t.Errorf("contactLabel(%q) = %v, want %v", label, got, contact)
		}
	}
}

// A page that will not advance while required questions stand unanswered is
// the user's: the stop names those questions as an answer gap at the check,
// whatever the walk's own end was. A stall with nothing left unanswered, or
// with an identity mismatch beside the gap, keeps the walk's class.
func TestApply_Submit_StalledOnRequiredQuestionsIsAnAnswerGap(t *testing.T) {
	gap := func(ps *pageState) *pageState {
		ps.leftovers = []Leftover{
			{Selector: "#q0", Label: "Are you legally authorized to work in the country you are applying to?", Reason: ReasonNeedsUser, Detail: "abstained:no-match"},
			{Selector: "#q2", Label: "Have you ever been employed by this company before?", Reason: ReasonNeedsUser, Detail: "abstained:no-match"},
		}
		return ps
	}
	run := func(end WalkEnd, ps *pageState) (*senv, *stop) {
		x := newSubmit(t, nil)
		x.s.pages = []*pageState{ps}
		x.wr = &walkResult{End: end, Detail: "the step returned with the same control and writes; the page says: Correct the information in highlighted fields.", Last: formState()}
		return x, x.g.r.afterWalk(context.Background(), x.e, x.s, t0, x.wr, nil)
	}
	for _, end := range []WalkEnd{EndStalled, EndNoNavigation, EndSubmitDisabled} {
		x, st := run(end, gap(&pageState{report: PageReport{URL: pageURL, Step: 1}}))
		if st == nil || st.class != ClassAnswerGap || st.at != StageCheck || st.board != "answer" ||
			!strings.Contains(st.reason, "legally authorized to work") || !strings.Contains(st.reason, "employed by this company") || !strings.Contains(st.reason, "the walk ended "+string(end)) {
			t.Fatalf("%s: stop %+v", end, st)
		}
		if got := x.e.Run.Stages; got[len(got)-2].Stage != StageFill || got[len(got)-2].Status != StatusOK || got[len(got)-1].Stage != StageCheck || got[len(got)-1].Status != StatusFailed {
			t.Errorf("%s: stage lines %+v", end, got)
		}
	}
	if _, st := run(EndStalled, &pageState{report: PageReport{URL: pageURL, Step: 1}}); st == nil || st.class != ClassDefect || st.at != StageFill {
		t.Errorf("a stall with nothing unanswered: stop %+v", st)
	}
	mismatched := gap(&pageState{report: PageReport{URL: pageURL, Step: 1}, mismatch: []gates.IdentityMismatch{{Label: "Email"}}})
	if _, st := run(EndStalled, mismatched); st == nil || st.class != ClassDefect {
		t.Errorf("a stall with an identity mismatch: stop %+v", st)
	}
	if _, st := run(EndBotChallenge, gap(&pageState{report: PageReport{URL: pageURL, Step: 1}})); st == nil || st.class != ClassVerification || st.at != StageFill {
		t.Errorf("a bot challenge keeps its own end: stop %+v", st)
	}
}

// A final Submit the page keeps disabled while the last page holds questions
// the run left blank (none marked required) is the page's own evidence that it
// wants them: the stop is an answer gap that names them, and nothing is
// answered or ticked because of it. With no blank row the disabled Submit keeps
// its class, and only the submit-disabled end is read this way.
func TestApply_Submit_DisabledSubmitWithBlankRowsIsAnAnswerGap(t *testing.T) {
	blank := func(rows ...engine.Row) *pageState {
		return &pageState{report: PageReport{URL: pageURL, Step: 1}, rows: rows}
	}
	row := func(label, outcome, reason string) engine.Row {
		return engine.Row{Selector: "#" + label, Label: label, Kind: "text", Outcome: outcome, Reason: reason}
	}
	run := func(end WalkEnd, detail string, ps *pageState) (*senv, *stop) {
		x := newSubmit(t, nil)
		x.s.pages = []*pageState{ps}
		x.wr = &walkResult{End: end, Detail: detail, Last: formState()}
		return x, x.g.r.afterWalk(context.Background(), x.e, x.s, t0, x.wr, nil)
	}
	const disabled = "the Submit control is disabled"
	x, st := run(EndSubmitDisabled, disabled, blank(
		row("Résumé", "filled", ""),
		row("Phone", "prefilled", ""),
		row("What excites you about this opportunity?", "abstained", "no-match"),
		row("Check Yes or No to indicate your agreement to receive text message updates", "abstained", "no-match"),
		row("Cover letter", "skipped", "no-lane-claimed"),
		row("Hidden", "skipped", "hidden"),
		row("Locked", "skipped", "readonly"),
		row("Edited", "skipped", "user-edited"),
	))
	if st == nil || st.class != ClassAnswerGap || st.at != StageCheck || st.board != "answer" ||
		!strings.Contains(st.reason, "What excites you") || !strings.Contains(st.reason, "text message updates") || !strings.Contains(st.reason, "Cover letter") ||
		!strings.Contains(st.reason, "the walk ended submit-disabled: "+disabled) {
		t.Fatalf("stop %+v", st)
	}
	for _, left := range []string{"Résumé", "Phone", "Honeypot", "Hidden", "Locked", "Edited"} {
		if strings.Contains(st.reason, left) {
			t.Errorf("%s is not a question left blank: %s", left, st.reason)
		}
	}
	if got := x.e.Run.Stages; got[len(got)-2].Stage != StageFill || got[len(got)-2].Status != StatusOK || got[len(got)-1].Stage != StageCheck || got[len(got)-1].Status != StatusFailed {
		t.Errorf("stage lines %+v", got)
	}
	if x.e.Run.Pressed || len(x.tab.clicks) != 0 {
		t.Errorf("the stop pressed or ticked something: %v", x.tab.clicks)
	}

	// The list is capped.
	var many []engine.Row
	for i := 0; i < maxBlankRowsNamed+3; i++ {
		many = append(many, row(fmt.Sprintf("Question %d", i), "abstained", "no-match"))
	}
	if _, st := run(EndSubmitDisabled, disabled, blank(many...)); st == nil || st.class != ClassAnswerGap ||
		!strings.Contains(st.reason, "Question 4") || strings.Contains(st.reason, "Question 5") || !strings.Contains(st.reason, "(and 3 more)") {
		t.Errorf("the capped list: stop %+v", st)
	}

	// Nothing left blank: the disabled Submit keeps its class.
	if _, st := run(EndSubmitDisabled, disabled, blank(row("Name", "filled", ""), row("Hidden", "skipped", "hidden"))); st == nil || st.class != ClassDefect || st.at != StageFill {
		t.Errorf("no blank row: stop %+v", st)
	}
	// Another finding beside the blanks keeps the walk's class.
	mismatched := blank(row("Q", "abstained", "no-match"))
	mismatched.mismatch = []gates.IdentityMismatch{{Label: "Email"}}
	if _, st := run(EndSubmitDisabled, disabled, mismatched); st == nil || st.class != ClassDefect {
		t.Errorf("an identity mismatch: stop %+v", st)
	}
	// Only the submit-disabled end reads blank rows this way.
	for _, end := range []WalkEnd{EndStalled, EndNoNavigation} {
		if _, st := run(end, "the step returned with the same control and writes", blank(row("Q", "abstained", "no-match"))); st == nil || st.class != ClassDefect || st.at != StageFill {
			t.Errorf("%s: stop %+v", end, st)
		}
	}
	// A rejected saved password keeps its own class.
	if _, st := run(EndSubmitDisabled, "an existing record with that email", blank(row("Q", "abstained", "no-match"))); st == nil || st.class == ClassAnswerGap {
		t.Errorf("an existing-account page: stop %+v", st)
	}
}

func TestApply_Walk_MatchWidgetUploadNotApplicationWhenOpenerPresent(t *testing.T) {
	widget := func(withOpener bool) *PageState {
		st := postingPage(ctl(1, "button", -1, "Get my score"))
		st.Inputs = []InputInfo{{I: 0, Tag: "input", Type: "file", Label: "Upload a file", Form: -1}}
		if withOpener {
			st.Controls = append(st.Controls, ctl(2, "button", -1, "Apply Now"))
		}
		return st
	}
	st := widget(true)
	st.dropMatchWidget()
	if st.HasForm() || len(Categorize(st, false).Openers) != 1 || len(Categorize(st, false).Submit) != 0 {
		t.Fatalf("with an opener the upload is the widget's: %+v", Categorize(st, false))
	}
	// With no opener on the page it is a step of the application.
	st = widget(false)
	st.dropMatchWidget()
	if !st.HasForm() {
		t.Fatal("a lone upload with no opener is an application step")
	}
	// A named résumé input is an application's, opener or not.
	st = widget(true)
	st.Inputs[0].Label = "Resume/CV"
	st.dropMatchWidget()
	if !st.HasForm() {
		t.Fatal("a résumé input is an application's")
	}
}

func TestApply_Walk_LocationFilterMenuIsNotTheFormWhenOpenerPresent(t *testing.T) {
	page := func(label string, withOpener bool) *PageState {
		st := postingPage(ctl(1, "button", -1, "Share page"))
		st.Inputs = []InputInfo{{I: 0, Tag: "select", Kind: "select", Type: "select", Label: label, Form: -1}}
		if withOpener {
			st.Controls = append(st.Controls, ctl(2, "button", -1, "Apply for this job"))
		}
		return st
	}
	// Pinterest's posting page (its Greenhouse form is in a frame): "Location:".
	for _, label := range []string{"Select location", "Location:"} {
		st := page(label, true)
		st.dropMatchWidget()
		if st.HasForm() || len(Categorize(st, false).Openers) != 1 || len(Categorize(st, false).InFormApply) != 0 {
			t.Fatalf("%s: the location filter is the page's, the Apply control opens the application: %+v", label, Categorize(st, false))
		}
	}
	// With no opener the same menu is all there is; a real question is never dropped.
	for name, st := range map[string]*PageState{"no opener": page("Select location", false), "a question": page("Which location do you prefer?", true)} {
		st.dropMatchWidget()
		if !st.HasForm() {
			t.Errorf("%s: the menu stays a field", name)
		}
	}
	// A menu with no blank choice reads as required and filled; it is still the filter.
	flagged := page("Select location", true)
	flagged.Inputs[0].Required, flagged.Inputs[0].Filled = true, true
	flagged.dropMatchWidget()
	if flagged.HasForm() {
		t.Error("a required-looking filter menu is still the filter")
	}
}

// Rippling's posting page: a language picker (a combobox named only
// "combobox", outside any form) beside "Apply now".
func TestApply_Walk_RoleNamedPickerOutsideAFormIsNotTheForm(t *testing.T) {
	page := func(label string, required bool) *PageState {
		st := postingPage(ctl(1, "button", -1, "Apply now"))
		st.Inputs = []InputInfo{{I: 0, Tag: "input", Kind: "text", Type: "text", Label: label, Form: -1, Required: required, Filled: true}}
		return st
	}
	st := page("combobox", false)
	st.dropMatchWidget()
	if st.HasForm() || len(Categorize(st, false).Openers) != 1 {
		t.Fatalf("the picker is the page's, Apply now opens the application: %+v", Categorize(st, false))
	}
	for name, st := range map[string]*PageState{"a question": page("Preferred language", false), "required": page("combobox", true)} {
		st.dropMatchWidget()
		if !st.HasForm() {
			t.Errorf("%s: the control stays a field", name)
		}
	}
}

func TestApply_Walk_BareSigninGateLoginRequiredRealApplicationFilled(t *testing.T) {
	gate := &PageState{
		URL: "https://login.example.test/auth", Title: "Log in or create account", Headings: []string{"Log in or create account"},
		Inputs: []InputInfo{{I: 0, Tag: "input", Type: "email", Label: "Email", Form: -1}}, Controls: []Control{ctl(1, "button", -1, "Continue")},
	}
	w := newWalk(t, Input{URL: gate.URL, Caller: gates.CallerCLI}, gate)
	res, st := w.walk()
	if st != nil || res.End != EndLoginRequired || res.End.Class(false) != ClassCredential || w.page.nFills != 0 || len(w.tab.typed) != 0 || len(w.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
	spa := &PageState{
		URL: "https://login.example.test/apply", Title: "Apply", Text: "Platform Engineer", Lines: []string{"Platform Engineer"},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Label: "Full name", Form: -1}, {I: 1, Tag: "input", Type: "email", Label: "Email", Form: -1},
			{I: 2, Tag: "input", Type: "file", Label: "Resume", Form: -1},
		},
		Controls: []Control{ctl(3, "button", -1, "Submit application")},
	}
	w = newWalk(t, Input{URL: spa.URL, Caller: gates.CallerCLI}, spa)
	res, st = w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 1 {
		t.Fatalf("end %+v stop %+v fills %d: a real application on a login-looking host is filled", res, st, w.page.nFills)
	}
}

func TestApply_Walk_OpenerEnumerationBoardSigninUKGAndDropdown(t *testing.T) {
	t.Run("the board's sign-in button is the wall and not an opener", func(t *testing.T) {
		st := postingPage(ctl(0, "button", -1, "Sign in to apply"))
		if len(Categorize(st, false).Openers) != 0 || signInWall(st) == "" {
			t.Fatal("sign in to apply is the wall")
		}
	})
	t.Run("a social-apply dropdown's toggle is followed once and the menu's Apply Now comes next", func(t *testing.T) {
		toggle := Control{I: 0, Tag: "button", Labels: []string{"Apply now"}, Form: -1, Order: 0}
		first := postingPage(toggle)
		menu := postingPage(toggle,
			Control{I: 1, Tag: "a", Role: "menuitem", Labels: []string{"Start applying with LinkedIn"}, Form: -1, Order: 1, Href: "https://www.linkedin.com/oauth/x"},
			Control{I: 2, Tag: "a", Role: "menuitem", Labels: []string{"Apply Now"}, Form: -1, Order: 2, Href: "https://careers.example.test/cas/sign-in"})
		w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, first)
		w.tab.onClick = func(tab *scriptTab, c Control) {
			switch {
			case c.Tag == "button":
				tab.set(menu)
			case c.Role == "menuitem" && c.Labels[0] == "Apply Now":
				tab.set(appPage())
			}
		}
		res, st := w.walk()
		if st != nil || res.End != EndSubmit || strings.Join(w.tab.clicks, ",") != "Apply now,Apply Now" {
			t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
		}
	})
}
