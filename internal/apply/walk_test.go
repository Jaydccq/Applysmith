package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/credentials"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
)

type wenv struct {
	g    *rig
	tab  *scriptTab
	page *fakePage
	e    *Exec
	s    *session
	took time.Duration // the last walk's length on the bubble's clock
}

func newWalk(t *testing.T, in Input, first *PageState) *wenv {
	t.Helper()
	g := newRig(t, func(d *Deps) {
		// A scripted tab changes a page within 600 ms of the action that
		// changes it, so a 1 s look after a form page sees every change; a walk
		// that ends on a page with no form waits that look out. Saving stays
		// longer than AfterForm, as in production, at test scale.
		d.Page.Timing = WalkTiming{AfterForm: time.Second, First: 300 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 60 * time.Millisecond, Saving: 1500 * time.Millisecond}
		d.Page.Check = CheckTiming{Reread: time.Millisecond, Settle: time.Millisecond}
	})
	tab := newScriptTab(first)
	tab.page = &fakePage{}
	run := &Run{ID: "run_w", StartedAt: t0, Input: in, ATS: ATSGeneric, Tenant: "careers.example.test"}
	e := &Exec{r: g.r, Run: run, Ctx: context.Background()}
	s := &session{r: g.r, e: e, tab: tab, page: tab.page}
	s.docs = newDocResumes(g.r, e)
	return &wenv{g: g, tab: tab, page: tab.page, e: e, s: s}
}

// walk runs in a synctest bubble: its looks, polls and waits pass on the
// bubble's clock, so they take no real time and a read count fits its budget.
func (w *wenv) walk() (res *walkResult, stp *stop) {
	synctest.Test(w.g.t, func(*testing.T) {
		began := time.Now()
		res, stp = w.g.r.walk(context.Background(), w.e, w.s)
		w.took = time.Since(began)
	})
	return res, stp
}

// wizardStep is step n of a wizard whose Next button reads Submit on the last.
func wizardStep(n, total int) *PageState {
	label := "Next"
	if n == total {
		label = "Submit application"
	}
	return &PageState{
		URL: "https://careers.example.test/apply", Title: "Apply", Text: fmt.Sprintf("Step %d of %d", n, total), Lines: []string{fmt.Sprintf("Step %d of %d", n, total)},
		Forms:    []FormInfo{{Fields: 2}},
		Inputs:   []InputInfo{{I: 0, Tag: "input", Type: "text", Label: fmt.Sprintf("Question %d", n), Form: 0}},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{label}, Form: 0}},
		Step:     &StepInfo{Current: n, Total: total},
	}
}

func advanceOnNext(total int) func(*scriptTab, Control) {
	n := 1
	return func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" && n < total {
			n++
			t.set(wizardStep(n, total))
		}
	}
}

func TestApply_Walk_EleventhPageEndsMaxPagesAfterTheTenth(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 14))
	w.tab.onClick = advanceOnNext(14)
	res, st := w.walk()
	if st != nil || res.End != EndMaxPages {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if got := w.page.nFills; got != MaxPages {
		t.Fatalf("%d pages were filled, want %d", got, MaxPages)
	}
	if res.End.Class(false) != ClassDefect {
		t.Fatal("max-pages is a defect")
	}
}

func TestApply_Walk_ThreeStepWizardEndsAtSubmit(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 3))
	w.tab.onClick = advanceOnNext(3)
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 3 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
	for _, c := range w.tab.clicks {
		if strings.Contains(strings.ToLower(c), "submit") {
			t.Fatalf("the walk pressed %q", c)
		}
	}
}

func TestApply_Walk_NextThatAnswersLateIsReadFromThePage(t *testing.T) {
	// A busy page held the input dispatch past its bound after Next landed.
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 3))
	w.tab.clickErr = &browser.Error{Code: browser.CodeOperationTimeout, Message: "a click timed out after 10s"}
	w.tab.onClick = advanceOnNext(3)
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 3 || len(w.tab.clicks) != 2 {
		t.Fatalf("end %+v stop %+v fills %d clicks %v", res, st, w.page.nFills, w.tab.clicks)
	}
}

func TestApply_Walk_SameStepAfterNextStalledOnSecondVisit(t *testing.T) {
	first := wizardStep(1, 3)
	first.Validation = []string{"Last name is required"}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, first)
	res, st := w.walk() // the click changes nothing
	if st != nil || res.End != EndStalled || !strings.Contains(res.Detail, "Last name is required") {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.clicks) > 2 {
		t.Fatalf("%d presses before the stall was called", len(w.tab.clicks))
	}
}

// Workday's My Information page: the first pass writes the boxes and presses
// Next, the page answers with "Errors Found" naming a row only the person can
// answer, and the second pass writes nothing new. Pressing again would send
// what the page already refused, so the walk ends there.
func TestApply_Walk_RefusedStepReadAgainWithNothingNewEndsWithoutAnotherPress(t *testing.T) {
	first := wizardStep(1, 3)
	refused := wizardStep(1, 3)
	refused.Validation = []string{"The field How Did You Hear About Us? is required and must have a value."}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, first)
	city := engine.Row{Selector: "#city", Label: "City", Kind: "text", Outcome: "filled", Value: "Durham"}
	w.page.fills = []*engine.PageResult{doneResult(city), doneResult(scriptedRow)}
	w.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#city", Label: "City", Kind: "text", Value: "Durham"}}
	}
	w.tab.onClick = func(t *scriptTab, c Control) { t.set(refused) }
	res, st := w.walk()
	if st != nil || res.End != EndStalled || !strings.Contains(res.Detail, "How Did You Hear") {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.clicks) != 1 || w.page.nFills != 2 {
		t.Fatalf("%d presses and %d fills, want one press and two fills", len(w.tab.clicks), w.page.nFills)
	}
}

// A Phenom requisition that is gone sends its address to the site's localized
// home; that is the posting's fact, read without asking a model.
func TestApply_Walk_PostingAddressSentToTheLocalizedHomeIsClosed(t *testing.T) {
	home := &PageState{URL: "https://careers.example.test/us/en", Title: "Careers at Example", Text: "Elevate your career. Search job title or location",
		Lines: []string{"Elevate your career.", "Search job title or location"}}
	w := newWalk(t, Input{URL: "https://careers.example.test/global/en/job/JR114518", Caller: gates.CallerCLI}, home)
	res, st := w.walk()
	if st != nil || res.End != EndPostingClosed || !strings.Contains(res.Detail, "names no job") {
		t.Fatalf("end %+v stop %+v", res, st)
	}
}

// A closed Greenhouse posting sends its address to the employer's careers
// page (Nuro), whose job-search filters are a form: still the posting's fact,
// and nothing is written into the filters.
func TestApply_Walk_PostingAddressSentToACareersPageWithSearchFiltersIsClosed(t *testing.T) {
	inv := buildInventory([]engine.InspectRow{
		{Selector: `[name="Location/Office"]`, Label: "Location/Office", Kind: "select"},
		{Selector: `[name="Search"]`, Label: "Search", Kind: "text"},
	}, []placement{{Found: true, I: 1, Tag: "select", Form: 0}, {Found: true, I: 2, Tag: "input", Type: "text", Form: 0}}, nil)
	careers := &PageState{URL: "https://www.example.test/careers", Title: "Careers | Example", Text: "Open roles. Location/Office Type Search",
		Lines: []string{"Open roles", "Location/Office", "Type", "Search"}, Inputs: inv.Inputs}
	if !careers.HasForm() {
		t.Fatal("the fixture's filters must read as a form")
	}
	w := newWalk(t, Input{URL: "https://job-boards.greenhouse.io/example/jobs/8248317", Caller: gates.CallerCLI}, careers)
	res, st := w.walk()
	if st != nil || res.End != EndPostingClosed || !strings.Contains(res.Detail, "names no job") || w.page.nFills != 0 || len(w.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v fills %d clicks %v", res, st, w.page.nFills, w.tab.clicks)
	}
}

func TestApply_Walk_SlowRenderStepPolledUntilTheFormReads(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
	empty := &PageState{URL: "https://careers.example.test/apply", Title: "Apply", Text: "Loading", Lines: []string{"Loading"}, Loading: true}
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			// The next step renders its fields only after a number of reads.
			seq := []*PageState{}
			for i := 0; i < 12; i++ {
				seq = append(seq, empty)
			}
			seq = append(seq, wizardStep(2, 2))
			t.script(seq...)
			t.state = empty
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 2 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
}

func TestApply_Walk_OpenerFollowedOncePerControlThenNoNavigation(t *testing.T) {
	post := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Platform Engineer", Text: "Platform Engineer", Lines: []string{"Platform Engineer"},
		Controls: []Control{{I: 0, Tag: "button", Labels: []string{"Apply now"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	res, st := w.walk() // the opener changes nothing
	if st != nil || res.End != EndNoNavigation {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.clicks) != 1 {
		t.Fatalf("%d presses: an Apply control is followed once", len(w.tab.clicks))
	}
}

func TestApply_Run_ClosedAlreadyAppliedEmailOnlyAndBotEnds(t *testing.T) {
	for name, c := range map[string]struct {
		lines []string
		title string
		want  WalkEnd
		class Class
	}{
		"closed":          {[]string{"Platform Engineer", "This job is no longer available."}, "Platform Engineer", EndPostingClosed, ClassPosting},
		"already applied": {[]string{"You've already applied to this job."}, "Platform Engineer", EndAlreadyApplied, ClassPosting},
		"email only":      {[]string{"Please send your resume to jobs@example.test to apply."}, "Platform Engineer", EndEmailApplication, ClassPosting},
		"bot":             {[]string{"Verify you are human"}, "Just a moment...", EndBotChallenge, ClassVerification},
	} {
		t.Run(name, func(t *testing.T) {
			st0 := &PageState{URL: "https://careers.example.test/jobs/7", Title: c.title, Text: strings.Join(c.lines, " "), Lines: c.lines}
			w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st0)
			res, st := w.walk()
			if st != nil || res.End != c.want || res.End.Class(res.Offers) != c.class {
				t.Fatalf("end %+v stop %+v", res, st)
			}
			if name == "closed" && len(w.e.Run.PostingFacts) != 1 {
				t.Fatalf("no posting fact: %v", w.e.Run.PostingFacts)
			}
			if len(w.tab.clicks) != 0 {
				t.Fatalf("pressed %v", w.tab.clicks)
			}
		})
	}
}

func TestApply_Record_ClosedPostingIsFactNotTrackerWrite(t *testing.T) {
	g, tr := trackerRig(t)
	ent, _ := tr.Add(context.Background(), trackerAddRow())
	st0 := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Platform Engineer", Text: "closed", Lines: []string{"This position has been filled"}}
	tab := newScriptTab(st0)
	tab.page = &fakePage{}
	run := &Run{ID: "run_c", StartedAt: t0, Input: Input{URL: pageURL, Caller: gates.CallerCLI}, ATS: ATSGeneric}
	e := &Exec{r: g.r, Run: run, Ctx: context.Background()}
	s := &session{r: g.r, e: e, tab: tab, page: tab.page}
	res, _ := g.r.walk(context.Background(), e, s)
	if res.End != EndPostingClosed || len(run.PostingFacts) != 1 {
		t.Fatalf("end %+v facts %v", res, run.PostingFacts)
	}
	got, _ := tr.Get(context.Background(), ent.ID)
	if got.Entry.Status != "evaluated" {
		t.Fatalf("the tracker row changed to %s: the tracker grants the chain no transition for a closed posting", got.Entry.Status)
	}
}

// Rockwell (Workday, live): Apply leads to a social-first sign-in page, Google
// and "Sign in with email" under a Sign In heading, with no form.
func TestApply_Walk_SocialSignInWallAfterApplyIsLoginRequired(t *testing.T) {
	post := &PageState{URL: "https://careers.example.test/job/7", Title: "Engineer", Text: "Engineer", Lines: []string{"Engineer"},
		Controls: []Control{{I: 0, Tag: "a", Role: "button", Labels: []string{"Apply"}, Href: "#apply", Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	w.tab.onClick = func(t *scriptTab, c Control) {
		t.set(&PageState{URL: "https://careers.example.test/job/7/apply", Title: "Engineer", Headings: []string{"Engineer", "Sign In"}, Text: "Sign In", Lines: []string{"Sign In"},
			Controls: []Control{
				{I: 1, Tag: "button", Labels: []string{"Sign in with Google"}, Form: -1},
				{I: 2, Tag: "button", Labels: []string{"Sign in with email"}, Form: -1}}})
	}
	res, st := w.walk()
	if st != nil || res.End != EndLoginRequired || strings.Join(w.tab.clicks, ",") != "Apply" {
		t.Fatalf("end %+v stop %+v clicks %q", res, st, w.tab.clicks)
	}
}

// Rockwell (Workday, live): My Information drew "Something went wrong. Please
// refresh the page and then try again." A reload brings the step back; an error
// that stays is the site's.
func TestApply_Walk_SiteErrorPageIsReloadedOnceThenTheSites(t *testing.T) {
	errPage := &PageState{URL: "https://careers.example.test/apply", Title: "Apply", Text: "Something went wrong",
		Lines: []string{"My Information", "Something went wrong", "Please refresh the page and then try again.", "Error Code: VPS|0000"}}
	// Workday's step that never finished drawing: its progress bar, no field.
	stuckPage := &PageState{URL: "https://careers.example.test/apply", Title: "Apply", FlowLoading: true,
		Text: "Software Engineer Create Account/Sign In My Information Review", Lines: []string{"Software Engineer", "Create Account/Sign In", "My Information", "Review"}}
	// P&G (Workday, live): the error page keeps the title of the step it replaced.
	staleTitle := *errPage
	staleTitle.Title, staleTitle.Headings = "Create Account - Example", []string{"Something went wrong"}
	for _, c := range []struct {
		page  *PageState
		heals bool
	}{{errPage, true}, {errPage, false}, {stuckPage, true}, {stuckPage, false}, {&staleTitle, true}} {
		errPage, heals := c.page, c.heals
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, errPage)
		w.tab.onNav = func(t *scriptTab, url string) {
			if heals {
				t.set(wizardStep(2, 2))
			}
		}
		res, st := w.walk()
		if len(w.tab.domNavs) != 1 {
			t.Fatalf("heals=%v: %d reloads, want exactly one (end %+v stop %+v)", heals, len(w.tab.domNavs), res, st)
		}
		if heals && (st != nil || res.End != EndSubmit) {
			t.Fatalf("end %+v stop %+v: the reloaded step is walked", res, st)
		}
		if !heals && (st == nil || st.class != ClassUnavailable) {
			t.Fatalf("end %+v stop %+v: an error page that stays is the site's", res, st)
		}
	}
}

// NVIDIA (Workday, live): Save and Continue on step 2 drew the error page; the
// reload opens the flow at its first step, which the walk had already pressed.
func TestApply_Walk_AReloadBackToTheFirstStepWalksTheStepsAgain(t *testing.T) {
	errPage := &PageState{URL: "https://careers.example.test/apply", Title: "Apply", FlowLoading: true, Text: "Something went wrong",
		Lines: []string{"My Information", "Something went wrong", "Please refresh the page and then try again.", "Error Code: VPS|0000"}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 3))
	n, failed := 1, false
	w.tab.onClick = func(t *scriptTab, c Control) {
		switch {
		case n == 2 && !failed:
			failed = true
			t.set(errPage)
		case c.Labels[0] == "Next":
			n++
			t.set(wizardStep(n, 3))
		}
	}
	w.tab.onNav = func(t *scriptTab, url string) {
		n = 1
		t.set(wizardStep(1, 3))
	}
	start := time.Now()
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || len(w.tab.domNavs) != 1 {
		t.Fatalf("end %+v stop %+v reloads %d: the steps after the reload are walked again", res, st, len(w.tab.domNavs))
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("the error page was waited on for %v as a saving step", time.Since(start))
	}
}

// NVIDIA (Workday, live): the social-first sign-in page (Google, LinkedIn, "Sign
// in with email", no form) drew right after the flow's loading page; the wall is
// the account stage's at once, not after the saving wait.
func TestApply_Walk_ASignInWallAfterTheLoadingPageIsNotWaitedOn(t *testing.T) {
	url := "https://careers.example.test/job/7/apply/applyManually"
	loading := &PageState{URL: url, Title: "Apply", FlowLoading: true, Text: "Create Account/Sign In", Lines: []string{"Create Account/Sign In"}}
	wall := &PageState{URL: url, Title: "Sign In", Headings: []string{"Engineer", "Sign In"}, Text: "Sign In", Lines: []string{"Sign In"},
		Controls: []Control{
			{I: 1, Tag: "button", Labels: []string{"Sign in with Google"}, Form: -1},
			{I: 2, Tag: "button", Labels: []string{"Sign in with email"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, loading)
	w.tab.script(loading, wall)
	start := time.Now()
	res, st := w.walk()
	if st != nil || res.End != EndLoginRequired || time.Since(start) > 30*time.Second {
		t.Fatalf("end %+v stop %+v after %v", res, st, time.Since(start))
	}
}

// Netflix (Eightfold, live): after the résumé upload a "Candidate Privacy" modal
// names its statement in its text, not its heading, and offers "I Acknowledge";
// with the legal setting on it is acknowledged and the walk reaches Submit.
func TestApply_Walk_APolicyNamedInTheDialogTextIsAcknowledged(t *testing.T) {
	form := wizardStep(1, 1)
	shown := *form
	buttons := []DialogButton{{I: 28, Label: "Close"}, {I: 30, Label: "Cancel"}, {I: 31, Label: "I Acknowledge"}}
	shown.Dialogs = []DialogInfo{{Heading: "Candidate Privacy", Buttons: buttons,
		Text: "Candidate Privacy When you upload your resume/CV, we provide job recommendations to you. You acknowledge agreement with our Candidate Privacy Statement CANCELI ACKNOWLEDGE"}}
	shown.Controls = append([]Control{}, form.Controls...)
	for _, b := range buttons {
		shown.Controls = append(shown.Controls, Control{I: b.I, Tag: "button", Labels: []string{b.Label}, Modal: true, Form: -1})
	}
	for _, on := range []bool{true, false} {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, &shown)
		w.g.r.d.Page.Settings = func() Settings { return Settings{AutoAcceptLegalDeclarations: on} }
		w.tab.onClick = func(t *scriptTab, c Control) {
			if c.Labels[0] == "I Acknowledge" {
				t.set(form)
			}
		}
		res, st := w.walk()
		pressed := slices.Contains(w.tab.clicks, "I Acknowledge")
		if on && (st != nil || res.End != EndSubmit || !pressed) {
			t.Fatalf("legal on: end %+v stop %+v clicks %q", res, st, w.tab.clicks)
		}
		if !on && (pressed || res == nil || res.End != EndConsentGate) {
			t.Fatalf("legal off: end %+v stop %+v clicks %q: the acknowledgement is the person's", res, st, w.tab.clicks)
		}
	}
}

// Netflix (Eightfold, live): a required box the fill never reads stays unread
// on every refill; it is one growth of the page, not a new page each time.
func TestApply_Walk_ABoxTheFillNeverReadsIsOneGrowth(t *testing.T) {
	st0 := wizardStep(1, 1)
	st0.Controls = nil
	st0.Inputs = append(st0.Inputs, InputInfo{I: 5, Tag: "input", Type: "text", Label: "Phone Number", Selector: `[id="phone"]`, Required: true, InspectionOnly: true, Form: 0})
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st0)
	res, st := w.walk()
	if st != nil || res.End != EndNoNavigation || w.page.nFills > 2 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
}

func TestApply_Walk_LoginWallInTheWalkIsLoginRequiredCredential(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, loginPage(""))
	res, st := w.walk()
	if st != nil || res.End != EndLoginRequired || res.End.Class(false) != ClassCredential {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.typed) != 0 || len(w.tab.clicks) != 0 {
		t.Fatal("the walk typed or pressed on a sign-in wall")
	}
}

func TestApply_RoleGuard_AccountPageStaleLinkStopsAsPostingBeforeFilling(t *testing.T) {
	st0 := wizardStep(1, 2)
	st0.Title, st0.Headings = "Marketing Manager - Example Corp", []string{"Marketing Manager"}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Company: "Example Corp", Role: "Platform Engineer"}, st0)
	_, st := w.walk()
	if st == nil || st.class != ClassPosting || !strings.Contains(st.reason, "stale link") || w.page.nFills != 0 {
		t.Fatalf("stop %+v fills %d", st, w.page.nFills)
	}
}

func TestApply_Walk_DisabledSubmitIsDefectNotClosed(t *testing.T) {
	st0 := wizardStep(2, 2)
	st0.Controls[0].Disabled = true
	st0.Validation = []string{"Please fill in this field"}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st0)
	res, st := w.walk()
	if st != nil || res.End != EndSubmitDisabled || res.End.Class(false) != ClassDefect || !strings.Contains(res.Detail, "Please fill in this field") {
		t.Fatalf("end %+v stop %+v", res, st)
	}
}

func TestApply_Walk_SubmitEnabledAMomentAfterTheStepDrawsIsReady(t *testing.T) {
	disabled := func() *PageState {
		st := wizardStep(2, 2)
		st.Controls[0].Disabled = true
		return st
	}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, disabled())
	w.g.r.d.Page.Timing.PressWait = 2 * time.Second
	for range 100 {
		w.tab.queue = append(w.tab.queue, disabled())
	}
	w.tab.queue = append(w.tab.queue, wizardStep(2, 2))
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
}

// ADP's job page draws its text seconds before its buttons: the first look
// waits for them instead of reading the page as one without an Apply.
func TestApply_Walk_ApplyDrawnAfterThePostingTextIsFollowed(t *testing.T) {
	text := func() *PageState {
		return &PageState{URL: loginURL, Title: "Software Engineer", Text: "Software Engineer Job Description", Lines: []string{"Software Engineer", "Job Description"}}
	}
	posting := text()
	posting.Controls = []Control{{I: 1, Tag: "sdf-button", Role: "button", Labels: []string{"Apply"}, Form: -1}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, text())
	// The Apply is drawn after 100 reads, a second of looks at the test's
	// poll, so the look lasts three.
	w.g.r.d.Page.Timing.AfterForm, w.g.r.d.Page.Timing.Saving = 3*time.Second, 4*time.Second
	for range 100 {
		w.tab.queue = append(w.tab.queue, text())
	}
	w.tab.queue = append(w.tab.queue, posting)
	w.tab.onClick = func(tb *scriptTab, c Control) {
		if c.I == 1 {
			tb.set(wizardStep(1, 1))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || !slices.Contains(w.tab.clicks, "Apply") {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
	}
}

// An application started earlier and left unfinished is completed: ADP's
// posting shows "Applied" in Apply's place, and it continues the application.
func TestApply_Walk_AnUnfinishedApplicationIsContinuedFromApplied(t *testing.T) {
	posting := &PageState{URL: loginURL, Title: "Software Engineer", Text: "Software Engineer Job Description", Lines: []string{"Software Engineer", "Job Description"},
		Controls: []Control{{I: 1, Tag: "sdf-button", Role: "button", Labels: []string{"Applied"}, Form: -1}, {I: 2, Tag: "sdf-button", Role: "button", Labels: []string{"Refer this job"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, posting)
	w.tab.onClick = func(tb *scriptTab, c Control) {
		if c.I == 1 {
			tb.set(wizardStep(1, 1))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || !slices.Equal(w.tab.clicks, []string{"Applied"}) {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
	}
}

func TestApply_Walk_PressDeniedByTheGateIsAuditedAndStopsTheWalk(t *testing.T) {
	st0 := wizardStep(1, 3)
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st0)
	// The label read at press time is a Withdraw control: it is never pressed.
	w.tab.labelsNow = map[int][]string{1: {"Withdraw application"}}
	_, st := w.walk()
	if st == nil || len(w.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %v", st, w.tab.clicks)
	}
	if w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'deny' AND code = 'withdraw_control'`) != 1 {
		t.Fatal("the denied press was not audited")
	}
}

func TestApply_Walk_CoveredNavigationControlCountsAsAPressThatDidNotHappen(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 3))
	w.tab.covered = map[int]bool{1: true}
	res, st := w.walk()
	if st != nil || res.End != EndStalled || len(w.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
	}
}

func trackerAddRow() tracker.AddRow {
	return tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: pageURL, Status: tracker.Evaluated}
}

func TestApply_Workday_StepStillLoadingIsNotFilledAndTheActiveStepSeparatesSteps(t *testing.T) {
	step := func(name string) *string { s := "current step 2 of 5 " + name; return &s }
	prev := wizardStep(1, 2)
	prev.ActiveStep = step("My Experience")
	// The apply flow's loading page keeps the previous step's fields on screen.
	loading := wizardStep(1, 2)
	loading.ActiveStep, loading.FlowLoading = step("Application Questions"), true
	next := wizardStep(2, 2)
	next.ActiveStep = step("Application Questions")

	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, prev)
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			t.script(append(repeatState(loading, 6), next)...)
			t.state = loading
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	// One fill for the first step and one for the step after it: none while the
	// skeleton was over the old fields.
	if w.page.nFills != 2 {
		t.Fatalf("%d fills: the loading step must not be filled", w.page.nFills)
	}
	if stateKey(prev) == stateKey(next) {
		t.Fatal("two steps with different active steps share a state key")
	}
}

// A cold Workday apply flow draws only its header for longer than a first
// look (live: workday.wd5, nvidia.wd5); the look keeps reading it.
func TestApply_Workday_SlowApplyFlowGetsTheLongLook(t *testing.T) {
	flow := "https://example.wd5.myworkdayjobs.com/en-US/Ex/job/X/Engineer_R1/apply/applyManually"
	post := &PageState{URL: "https://example.wd5.myworkdayjobs.com/en-US/Ex/job/X/Engineer_R1", Title: "Engineer", Text: "Engineer", Lines: []string{"Engineer"},
		Controls: []Control{{I: 0, Tag: "a", Role: "button", Labels: []string{"Apply"}, Href: flow, Form: -1}}}
	shell := &PageState{URL: flow, Title: "Careers", Text: "Careers", Lines: []string{"Careers"}}
	form := wizardStep(1, 1)
	form.URL = flow
	w := newWalk(t, Input{URL: post.URL, Caller: gates.CallerCLI}, post)
	w.e.Run.ATS = ATSWorkday
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Apply" {
			t.script(append(repeatState(shell, 60), form)...)
			t.state = shell
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
}

// Jobvite's data-consent page (Optimizely): off, consent-gate; on, residence
// from the profile, I Accept, then the form. Alerts box and decline untouched.
func TestApply_Walk_DataConsentFollowsTheLegalSetting(t *testing.T) {
	const url = "https://jobs.jobvite.com/example/job/oAbc/apply"
	alerts := InputInfo{I: 2, Kind: "checkbox", Label: "Send me job alerts", Form: 0}
	residence := InputInfo{I: 0, Tag: "select", Kind: "select", Label: "Location of Residence", Form: 0, Required: true, Options: []string{"Select your location", "Germany", "Rest of World"}}
	gate := &PageState{URL: url, Lines: []string{"Data Consent"}, Inputs: []InputInfo{residence, alerts}, Controls: []Control{{I: 1, Tag: "a", Labels: []string{"Back"}, Href: "/j", Form: -1}}}
	for _, on := range []bool{false, true} {
		w := newWalk(t, Input{URL: url, Caller: gates.CallerCLI}, gate)
		w.g.r.d.Page.Settings = func() Settings { return Settings{AutoAcceptLegalDeclarations: on} }
		w.g.r.d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"candidate":{"address":{"country":"US"}}}`), nil
		}
		w.tab.onChoose = func(t *scriptTab, _, _ int) {
			picked := residence
			picked.Filled = true
			t.state = &PageState{URL: url, Lines: []string{"Data Consent"}, Inputs: []InputInfo{picked, alerts},
				Controls: []Control{{I: 3, Tag: "button", Labels: []string{"I Accept"}, Form: 0}, {I: 4, Tag: "a", Labels: []string{"I Decline"}, Href: "/j", Form: 0}}}
		}
		w.tab.onClick = func(t *scriptTab, c Control) {
			if c.Labels[0] == "I Accept" {
				t.set(wizardStep(2, 2))
			}
		}
		res, stp := w.walk()
		notes := strings.Join(w.s.walkNotes, ";")
		if stp != nil || len(w.tab.inputClicks) != 0 || (!on && (res.End != EndConsentGate || len(w.tab.chosen)+len(w.tab.clicks) != 0 || !strings.Contains(res.Detail, "autoAcceptLegalDeclarations is off"))) ||
			(on && (res.End != EndSubmit || w.tab.chosen[0] != 2 || strings.Join(w.tab.clicks, ",") != "I Accept" || !strings.Contains(notes, `"Rest of World"`))) {
			t.Fatalf("on=%v end %+v stop %+v clicks %v %v chosen %v notes %s", on, res, stp, w.tab.clicks, w.tab.inputClicks, w.tab.chosen, notes)
		}
	}
}

func repeatState(st *PageState, n int) []*PageState {
	out := make([]*PageState, n)
	for i := range out {
		out[i] = st
	}
	return out
}

// Workable draws a cookie dialog (aria-modal, "Cookie Consent") with a
// full-page backdrop over the posting: the Apply link is on the page and
// covered. The walk closes the notice with the least consent it offers
// (Decline all, not Accept all), finds the link again and presses it once.
// Channable (Recruitee): a scam-warning notice and a cookie dialog stand over
// Apply at once; each is closed in turn, then Apply is pressed.
func TestApply_Walk_TwoStackedGatesAreClosedBeforeOnePress(t *testing.T) {
	post := &PageState{URL: "https://jobs.example.test/o/engineer", Title: "Engineer", Text: "Engineer", Lines: []string{"Engineer"},
		Controls: []Control{
			{I: 0, Tag: "a", Role: "button", Labels: []string{"Apply"}, Href: "#apply", Form: -1},
			{I: 1, Tag: "button", Labels: []string{"\u00d7"}, Form: -1, Chrome: true},
			{I: 2, Tag: "button", Labels: []string{"Allow all"}, Form: -1, Chrome: true},
		}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	// The cookie dialog lies over the notice's close button.
	w.tab.covered = map[int]bool{0: true, 1: true}
	w.tab.consent = &ConsentGate{Found: true, Controls: []ConsentButton{{I: 2, Label: "Allow all"}, {I: 1, Label: "\u00d7"}}}
	w.tab.onClick = func(t *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Allow all":
			t.covered = map[int]bool{0: true}
			t.consent = &ConsentGate{Found: true, Controls: []ConsentButton{{I: 1, Label: "\u00d7"}}}
		case "\u00d7":
			t.covered, t.consent = nil, nil
		case "Apply":
			t.set(wizardStep(2, 2))
		}
	}
	res, st := w.walk()
	if got := strings.Join(w.tab.clicks, ","); st != nil || res.End != EndSubmit || got != "Allow all,\u00d7,Apply" {
		t.Fatalf("end %+v stop %+v clicks %q", res, st, got)
	}
}

func TestApply_Walk_CookieBackdropOverTheApplyLinkIsDeclinedThenApplyIsPressedOnce(t *testing.T) {
	post := &PageState{URL: "https://apply.workable.com/omilia/j/1F218B38D3", Title: "Computational Linguist", Text: "Computational Linguist", Lines: []string{"Computational Linguist"},
		Controls: []Control{
			{I: 0, Tag: "a", Role: "button", Labels: []string{"Apply for this job"}, Href: "/omilia/j/1F218B38D3/apply/", Form: -1},
			{I: 1, Tag: "button", Labels: []string{"Cookies settings"}, Form: -1, Chrome: true},
			{I: 2, Tag: "button", Labels: []string{"Accept all"}, Form: -1, Chrome: true},
			{I: 3, Tag: "button", Labels: []string{"Decline all"}, Form: -1, Chrome: true},
		}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	w.tab.covered = map[int]bool{0: true}
	w.tab.consent = &ConsentGate{Found: true, Text: "This website uses cookies", Controls: []ConsentButton{{I: 1, Label: "Cookies settings"}, {I: 2, Label: "Accept all"}, {I: 3, Label: "Decline all"}}}
	w.tab.onClick = func(t *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Decline all":
			t.covered, t.consent = nil, nil
		case "Apply for this job":
			t.set(wizardStep(2, 2))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if got := strings.Join(w.tab.clicks, ","); got != "Decline all,Apply for this job" {
		t.Fatalf("clicks %q: the notice is declined once, then the link is pressed once", got)
	}
	if n := w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'cookie_consent' AND verdict = 'allow' AND code = 'ok'`); n != 1 {
		t.Fatalf("%d cookie_consent decisions: the least-consent press is audited", n)
	}
}

// Workable's /apply/ page under its cookie dialog: the backdrop hides the form
// from the fill, which reads no control. The walk declines the notice and
// fills once more instead of ending on an unreadable page.
func TestApply_Walk_CookieDialogOverTheFormIsDeclinedThenTheFormIsFilled(t *testing.T) {
	form := wizardStep(1, 1)
	form.Controls = append(form.Controls, Control{I: 2, Tag: "button", Labels: []string{"Accept all"}, Form: -1, Chrome: true}, Control{I: 3, Tag: "button", Labels: []string{"Decline all"}, Form: -1, Chrome: true})
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, form)
	w.tab.consent = &ConsentGate{Found: true, Text: "This website uses cookies", Controls: []ConsentButton{{I: 2, Label: "Accept all"}, {I: 3, Label: "Decline all"}}}
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Decline all" {
			t.consent = nil
		}
	}
	q := engine.Row{Selector: "#q1", Label: "Question 1", Kind: "text", Outcome: "filled", Value: "Yes"}
	w.page.fills = []*engine.PageResult{doneResult(), doneResult(q)}
	w.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#q1", Label: "Question 1", Kind: "text", Value: "Yes"}}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 2 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
	if got := strings.Join(w.tab.clicks, ","); got != "Decline all" {
		t.Fatalf("clicks %q", got)
	}
}

// Oracle's contact step: the walk's inventory, read from the engine, held its
// fields, but the fill read no form on it, and Next was pressed over a first
// name the site had put there ("JordanAvery"). The fill looks once more, and a
// page it still cannot read is never walked past; a one-field page it reads as
// no form is walked past as before.
func TestApply_Walk_AFormTheFillCannotReadIsNotWalkedPast(t *testing.T) {
	notApp := &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{NotAnApplication: true}}}
	contact := func(n, total int) *PageState {
		st := wizardStep(n, total)
		st.Inputs = append(st.Inputs, InputInfo{I: 2, Tag: "input", Type: "text", Label: "Last Name", Form: 0})
		return st
	}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, contact(1, 2))
	w.tab.onClick = advanceOnNext(2)
	w.page.fills = []*engine.PageResult{notApp, notApp}
	res, st := w.walk()
	if res != nil || st == nil || st.class != ClassDefect || !strings.Contains(st.reason, "does not go past it") || w.page.nFills != 2 || len(w.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v fills %d clicks %v", res, st, w.page.nFills, w.tab.clicks)
	}
	// The second look reads the form: the walk goes on as on any page.
	w1 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, contact(1, 2))
	w1.tab.onClick = advanceOnNext(2)
	w1.page.fills = []*engine.PageResult{notApp, doneResult(scriptedRow)}
	if res, st := w1.walk(); st != nil || res.End != EndSubmit || w1.page.nFills != 3 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w1.page.nFills)
	}
	// One field read as no form is walked past, as before.
	w2 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
	w2.tab.onClick = advanceOnNext(2)
	w2.page.fills = []*engine.PageResult{notApp, doneResult(scriptedRow)}
	if res, st := w2.walk(); st != nil || res.End != EndSubmit || w2.page.nFills != 2 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w2.page.nFills)
	}
}

// With nothing the walk may close, a covered Apply control ends the walk
// honestly: it names the cover, not "every Apply control was already followed".
func TestApply_Walk_CoveredApplyLinkIsFollowedACoveredButtonNamesTheCover(t *testing.T) {
	post := &PageState{URL: "https://apply.workable.com/omilia/j/1F218B38D3", Title: "Computational Linguist", Text: "Computational Linguist", Lines: []string{"Computational Linguist"},
		Controls: []Control{{I: 0, Tag: "a", Labels: []string{"Apply for this job"}, Href: "/omilia/j/1F218B38D3/apply/", Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	w.tab.covered = map[int]bool{0: true}
	_, _ = w.walk()
	if len(w.tab.clicks) != 0 || !slices.Contains(w.tab.domNavs, "https://apply.workable.com/omilia/j/1F218B38D3/apply/") {
		t.Fatalf("clicks %v navs %v: the covered link is followed to its own address", w.tab.clicks, w.tab.domNavs)
	}
	button := &PageState{URL: post.URL, Title: post.Title, Text: post.Text, Lines: post.Lines,
		Controls: []Control{{I: 0, Tag: "button", Labels: []string{"Apply for this job"}, Form: -1}}}
	w1 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, button)
	w1.tab.covered = map[int]bool{0: true}
	res, st := w1.walk()
	if st != nil || res.End != EndNoNavigation || !strings.Contains(res.Detail, "is covered by") || strings.Contains(res.Detail, "already followed") || len(w1.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w1.tab.clicks)
	}
	// A dialog that asks for a password is no consent gate and is never closed.
	w2 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, button)
	w2.tab.covered = map[int]bool{0: true}
	w2.tab.consent = &ConsentGate{Found: true, Asks: true, Controls: []ConsentButton{{I: 9, Label: "Accept"}}}
	if res, st := w2.walk(); st != nil || res.End != EndNoNavigation || len(w2.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w2.tab.clicks)
	}
}

// SuccessFactors (HII): the page says the position is no longer posted and
// draws a job-alert widget whose frequency box is not a question.
func TestApply_Walk_ClosedNoticeBeatsAJobAlertWidgetsFrequencyBox(t *testing.T) {
	label := "Select how often (in days) to receive an alert:"
	inv := buildInventory([]engine.InspectRow{{Selector: `[id="j_idt195"]`, Label: label, Kind: "text", Required: true, Value: "7"}},
		[]placement{{Found: true, I: 4, Tag: "input", Type: "number", Form: -1}}, nil)
	if len(inv.Inputs) != 1 || !inv.Inputs[0].Furniture {
		t.Fatalf("inputs %+v: a box that sets up an alert is furniture", inv.Inputs)
	}
	if n := ClosedNotice([]string{"Thank you for your interest.  This position is no longer posted at this time."}); n == "" {
		t.Fatal("\"no longer posted\" is not read as closed")
	}
	st0 := &PageState{URL: "https://careers.example.test/job/ENGINEER-SOFTWARE-1/1425126300", Title: "ENGINEER SOFTWARE 1 Job Details", Text: "closed",
		Lines: []string{label, "Create Alert", "Thank you for your interest.  This position is no longer posted at this time."}, Inputs: inv.Inputs,
		Controls: []Control{ctl(1, "a", -1, "Create Alert")}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st0)
	res, st := w.walk()
	if st != nil || res.End != EndPostingClosed || len(w.e.Run.PostingFacts) != 1 || len(w.tab.clicks) != 0 || w.page.nFills != 0 {
		t.Fatalf("end %+v stop %+v facts %v clicks %v fills %d", res, st, w.e.Run.PostingFacts, w.tab.clicks, w.page.nFills)
	}
}

// A document that drew nothing (SmartRecruiters' one-click page in a browser
// that was refused) is the site or the browser, never a fact about the posting.
func TestApply_Walk_BlankDocumentIsUnavailableNotPosting(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, &PageState{URL: "https://jobs.smartrecruiters.com/oneclick-ui/company/CircleLogistics1/publication/d20c"})
	res, st := w.walk()
	if res != nil || st == nil || st.class != ClassUnavailable || !strings.Contains(st.reason, "loaded blank") {
		t.Fatalf("end %+v stop %+v", res, st)
	}
}

// A no-form stop with an Apply-shaped control the walk does not follow is a
// defect (a gap in the walk), not a posting fact that holds the row off lists.
func TestApply_Walk_NoFormWithAnUnrecognisedApplyControlIsADefectNotAPostingFact(t *testing.T) {
	post := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Platform Engineer", Text: "Platform Engineer", Lines: []string{"Platform Engineer"},
		Controls: []Control{ctl(0, "a", -1, "Apply with Indeed")}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	res, st := w.walk()
	if st != nil || res.End != EndNoForm || res.End.Class(res.Offers) != ClassDefect || !strings.Contains(res.Detail, "Apply with Indeed") || len(w.tab.clicks) != 0 {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	// The same page with nothing Apply-shaped is a plain posting stop.
	bare := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Platform Engineer", Text: "Platform Engineer", Lines: []string{"Platform Engineer"}}
	w2 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, bare)
	if res, st := w2.walk(); st != nil || res.End != EndNoForm || res.End.Class(res.Offers) != ClassPosting {
		t.Fatalf("end %+v stop %+v", res, st)
	}
}

// A bot check left to the person is theirs: the persisted outcome carries no
// board reason, so the owner and the learning lane read the run's own words.
func TestApply_Record_BotCheckLeftToTheUserIsOwnedByTheUserNotADefect(t *testing.T) {
	o := Outcome{Kind: OutcomeStopped, Stage: StageFill, Class: ClassDefect, Reason: "walk ended bot-challenge: bot check: a captcha stands on iCIMS's email step (hcaptcha) and is yours"}
	if got := OwnerOf(o); got != OwnerUser {
		t.Fatalf("owner %q", got)
	}
	run := &Run{Outcome: o, Snapshot: "/snap/run-stop.html"}
	if note := laneNote(run); run.LearningLane != LaneUserAction || strings.Contains(note, "work-item") {
		t.Fatalf("lane %q note %q: a challenge is no developer work item", run.LearningLane, note)
	}
	plain := Outcome{Kind: OutcomeStopped, Stage: StageFill, Class: ClassDefect, Reason: "walk ended stalled: the step returned with the same control and writes"}
	if OwnerOf(plain) != OwnerDefect {
		t.Fatal("an ordinary walk defect is no longer a defect")
	}
}

// Taleo redraws its job page right after it loads, so the handle the first read
// gave the Apply button points at a node that is gone when the press looks it
// up. The press reads the page again and finds the button by its labels, tag
// and address (the same occurrence of two alike), then presses it once.
func TestApply_Walk_ApplyButtonRedrawnAfterTheReadIsFoundAgainAndPressedOnce(t *testing.T) {
	post := &PageState{URL: "https://textron.taleo.example.test/careersection/jobdetail.ftl", Title: "Software Engineer I", Text: "Software Engineer I", Lines: []string{"Software Engineer I"},
		Controls: []Control{
			{I: 0, Tag: "input", Type: "button", Labels: []string{"Apply Online", "Apply for this position online"}, Form: -1},
			{I: 1, Tag: "input", Type: "button", Labels: []string{"Add to My Job Cart"}, Form: -1},
			{I: 2, Tag: "input", Type: "button", Labels: []string{"Apply Online", "Apply for this position online"}, Form: -1},
		}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	w.tab.gone = map[int]bool{0: true, 2: true}
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Apply Online" {
			t.set(wizardStep(2, 2))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if got := strings.Join(w.tab.clicks, ","); got != "Apply Online/Apply for this position online" {
		t.Fatalf("clicks %q: the Apply button is pressed once", got)
	}
	if n := w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'deny'`); n != 0 {
		t.Fatalf("%d denied presses: a redraw is not a missing control", n)
	}
}

// Capital One's careers page (TalentBrew) rewrites its Apply link after load,
// adding the landing page's tracking parameters to the address. The link is
// found again by its address without the query and pressed once.
func TestApply_Walk_ApplyLinkWhoseQueryThePageRewroteIsFoundAgain(t *testing.T) {
	const apply = "https://capitalone.wd12.example.test/Capital_One/job/Richmond-VA/Engineer_R1/apply"
	link := func(href string) *PageState {
		return &PageState{URL: "https://www.careers.example.test/job/richmond/engineer/1/2", Title: "Engineer at Example", Text: "Engineer", Lines: []string{"Engineer"},
			Controls: []Control{{I: 0, Tag: "a", Href: href, Labels: []string{"Apply"}, Form: -1}}}
	}
	post := link(apply)
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	w.tab.gone = map[int]bool{0: true}
	reads := 0
	w.tab.probeHook = func(t *scriptTab) {
		if reads++; reads == 2 {
			t.state = link(apply + "?p_sid=x&source=rd_linkedin")
		}
	}
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Apply" {
			t.set(wizardStep(2, 2))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || strings.Join(w.tab.clicks, ",") != "Apply" {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
	}
}

// A control that is really gone after the page was read is still refused as
// not found: finding it again never lets a press through on a guess.
func TestApply_Walk_ApplyButtonThatIsReallyGoneIsRefusedAsNotFound(t *testing.T) {
	post := &PageState{URL: "https://textron.taleo.example.test/careersection/jobdetail.ftl", Title: "Software Engineer I", Text: "Software Engineer I", Lines: []string{"Software Engineer I"},
		Controls: []Control{{I: 0, Tag: "input", Type: "button", Labels: []string{"Apply Online"}, Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	w.tab.gone = map[int]bool{0: true}
	reads := 0
	w.tab.probeHook = func(t *scriptTab) {
		if reads++; reads > 1 {
			t.state = &PageState{URL: post.URL, Title: post.Title, Text: "Software Engineer I", Lines: []string{"Software Engineer I"}}
		}
	}
	_, st := w.walk()
	if st == nil || !strings.Contains(st.reason, "no control matched at press time") || len(w.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %v", st, w.tab.clicks)
	}
	if w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'deny' AND code = 'control_not_found'`) != 1 {
		t.Fatal("the refused press was not audited")
	}
}

// iCIMS draws its posting in a frame whose share widget finishes loading about
// when the Apply link is pressed, so the page's signature changes before the
// frame leaves for the login step. A pressed opener is given time to take the
// page away before the walk reads the page again; reading it at once found the
// same link, already followed, and ended the walk on a page that was moving.
func TestApply_Walk_OpenerStillShownWhileThePageLoadsOtherThingsIsWaitedOut(t *testing.T) {
	post := &PageState{URL: "https://careers.example.test/jobs/7", Title: "Embedded Software Engineer", Text: "Embedded Software Engineer", Lines: []string{"Embedded Software Engineer"},
		Controls: []Control{{I: 0, Tag: "a", Labels: []string{"Apply for this job online"}, Href: "https://careers.example.test/jobs/7/job?mode=apply", Form: -1}}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, post)
	pressed, reads := false, 0
	w.tab.onClick = func(t *scriptTab, _ Control) {
		pressed = true
		loaded := *post
		loaded.Text = post.Text + " Share on your newsfeed"
		t.set(&loaded) // the signature moves; the opener is still there
	}
	w.tab.probeHook = func(t *scriptTab) {
		if pressed {
			if reads++; reads == 3 {
				t.state = wizardStep(2, 2)
			}
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || len(w.tab.clicks) != 1 {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
	}
}

func finalApplyPage() *PageState {
	return &PageState{
		URL: "https://ats.example.test/jobs/7/apply", Title: "Application", Text: "Application", Lines: []string{"Application"},
		Forms:    []FormInfo{{Fields: 2}},
		Inputs:   []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "First name", Form: 0}},
		Controls: []Control{{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply"}, Form: 0}},
	}
}

// A filled form whose only forward control is its own Apply button of type
// submit is at its Submit: the walk ends ready for Submit, names the control,
// and never presses it; only the submit stage may.
func TestApply_Walk_FormWhoseFinalControlReadsApplyEndsAtSubmitAndNeverPressesIt(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, finalApplyPage())
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || !strings.Contains(res.Detail, `its final control reads "Apply"`) {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.clicks) != 0 {
		t.Fatalf("clicks %v: the walk pressed the form's final Apply", w.tab.clicks)
	}
	// With a Next beside it the Apply is not the end of the form: the walk
	// presses Next and still never presses the Apply.
	two := finalApplyPage()
	two.Controls = append(two.Controls, Control{I: 2, Tag: "button", Labels: []string{"Next"}, Form: 0})
	w2 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, two)
	w2.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			t.set(wizardStep(2, 2))
		}
	}
	if res, st := w2.walk(); st != nil || res.End != EndSubmit || strings.Join(w2.tab.clicks, ",") != "Next" {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w2.tab.clicks)
	}
}

// Even if a caller handed the walk's press the control marked as the form's
// final Apply, the gate refuses it for every stage but the submit stage.
func TestApply_Walk_FinalApplyControlIsRefusedByThePressGate(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, finalApplyPage())
	st := finalApplyPage()
	cats := Categorize(st, true)
	if len(cats.Submit) != 1 || !cats.Submit[0].FinalApply {
		t.Fatalf("categories %+v", cats)
	}
	wk := &walker{r: w.g.r, e: w.e, s: w.s, tm: w.g.r.d.Page.Timing.withDefaults(), opened: map[string]bool{}, seen: map[string]int{}, decided: map[string]bool{}, revalidated: map[string]bool{}}
	_, stp := wk.press(context.Background(), st, cats.Submit[0])
	if stp == nil || len(w.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %v", stp, w.tab.clicks)
	}
	if w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'deny' AND code = 'submit_in_walk'`) != 1 {
		t.Fatal("the refused press was not audited")
	}
}

// A combined account and application form that ends in "Create Account"
// instead of a Next or a Submit: the walk types the account, fills the rest
// and presses Create Account once, as the account stage would.
func TestApply_Walk_CombinedFormEndingInCreateAccountIsPressedOnce(t *testing.T) {
	form := ebrdForm()
	form.Title, form.Lines = "Apply - Platform Engineer", []string{"Platform Engineer"}
	form.Inputs = append(form.Inputs, InputInfo{I: 13, Tag: "input", Type: "text", Label: "How did you hear about us?", Form: 0})
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, form)
	withApplicant("United States")(&w.g.r.d)
	v, err := vault.New("ws", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	w.g.r.d.Page.Credentials = credentials.New(w.g.st, v, w.g.r.d.Now)
	const host = "careers.example.test"
	w.e.combined = &combinedAccount{host: host, name: "signin-site-" + host, system: ATSGeneric, tenant: host}
	w.tab.onClick = func(tab *scriptTab, c Control) {
		if c.Labels[0] == "Create Account" {
			tab.set(appPage())
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if n := w.tab.clicksOf("Create Account"); n != 1 {
		t.Fatalf("clicks %v: Create Account is pressed once", w.tab.clicks)
	}
	if pw, err := v.Get("signin-site-" + host); err != nil || w.tab.typed[1] != pw {
		t.Fatalf("typed %v: the generated password was typed and kept (%v)", w.tab.typed, err)
	}
	if got := w.e.Run.Signins[0].Succeeded; got != "created-account" {
		t.Fatalf("record %+v", w.e.Run.Signins)
	}
}

// Node's multistep Workday review guard (Mizuho): a Review page whose recap
// still shows "No Response" under a required item never ends at Submit.
func TestApply_Walk_ReviewNoResponseEndsStalledNotAtSubmit(t *testing.T) {
	review := &PageState{
		URL: "https://careers.example.test/apply", Title: "Review", Text: "Review Work Experience No Response", Lines: []string{"Review", "Work Experience", "No Response"},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Submit"}, Form: -1}},
		Step:     &StepInfo{Current: 5, Total: 5},
	}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			t.set(review)
			t.prepress = []PrePress{{NoResponse: []string{"Work Experience"}}}
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndStalled || !strings.Contains(res.Detail, `"No Response" for: Work Experience`) {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	w2 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
	w2.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			t.set(review)
		}
	}
	if res, st := w2.walk(); st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v: a clean Review page ends at Submit", res, st)
	}
}

// ADP's Preliminary Questions draws its next question once one is answered,
// with nothing to press until the last: the page is filled again.
func TestApply_Walk_AQuestionDrawnAfterTheFillIsFilledToo(t *testing.T) {
	q := func(name string, filled bool) InputInfo {
		return InputInfo{I: len(name), Selector: `input[type="radio"][name="` + name + `"]`, Kind: "radio", Tag: "input", Type: "radio", Name: name, Label: name, Required: true, Filled: filled, Form: 0}
	}
	page := func(in ...InputInfo) *PageState {
		return &PageState{URL: loginURL, Title: "Preliminary Questions", Text: "Please answer all questions", Lines: []string{"Please answer all questions"}, Forms: []FormInfo{{Fields: len(in)}}, Inputs: in}
	}
	done := page(q("adult", true), q("sponsor", true))
	done.Controls = []Control{{I: 9, Tag: "button", Labels: []string{"Submit application"}, Form: 0}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, page(q("adult", false)))
	w.page.fills = []*engine.PageResult{
		doneResult(engine.Row{Selector: q("adult", false).Selector, Label: "adult", Kind: "radio-group", Outcome: "filled", Value: "Yes"}),
		doneResult(engine.Row{Selector: q("sponsor", false).Selector, Label: "sponsor", Kind: "radio-group", Outcome: "filled", Value: "No"}),
	}
	w.tab.probeHook = func(tb *scriptTab) {
		switch w.page.nFills {
		case 0:
			tb.state = page(q("adult", false))
		case 1:
			tb.state = page(q("adult", true), q("sponsor", false))
		default:
			tb.state = done
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 2 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
}

// Eightfold's profile dialog after sign-in: its Submit is pressed as the
// profile stage, and the walk goes on to the application, whose own Submit it
// never presses.
func TestApply_Walk_AProfileDialogIsSavedAndTheApplicationWalked(t *testing.T) {
	w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, profileDialogPage())
	w.tab.onClick = func(tab *scriptTab, c Control) {
		if c.Labels[0] == "Submit" {
			tab.set(formState())
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || strings.Join(w.tab.clicks, ",") != "Submit" {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
	}
}

// A choice inside an editor the walk fills (Oracle's pills, its Cancel) is a
// press like any other: the press gate reads its label and the decision is
// audited. A pill that reads as navigation or as deleting is not clicked.
func TestApply_Walk_AnEditorChoiceIsPressedThroughThePressGate(t *testing.T) {
	st := &PageState{URL: loginURL, Controls: []Control{
		{I: 1, Tag: "button", Labels: []string{"Bachelor's Degree"}, Pressed: "false", Form: -1},
		{I: 2, Tag: "button", Labels: []string{"Cancel"}, Form: -1},
		{I: 3, Tag: "button", Labels: []string{"Delete entry"}, Form: -1},
		{I: 4, Tag: "button", Labels: []string{"Next"}, Form: -1},
		{I: 5, Tag: "button", Labels: []string{"Submit"}, Form: -1},
	}}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st)
	for i, want := range map[int]bool{1: true, 2: true, 3: false, 4: false, 5: false} {
		if stp := w.s.clickChoice(context.Background(), i); (stp == nil) != want {
			t.Errorf("%v: stop %+v, want pressed %v", st.Controls[i-1].Labels, stp, want)
		}
	}
	if len(w.tab.clicks) != 2 || w.tab.clicksOf("Bachelor's Degree") != 1 || w.tab.clicksOf("Cancel") != 1 {
		t.Fatalf("clicked %v: only the choice and Cancel are pressed", w.tab.clicks)
	}
	if n := w.g.audits(gates.GateControlPress); n != 5 {
		t.Fatalf("%d press decisions audited, want one for each control asked", n)
	}
}
