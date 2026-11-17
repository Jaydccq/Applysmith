package apply

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// cardsPage is a posting with an Apply button and three cards for other jobs.
func cardsPage() *PageState {
	return &PageState{URL: "https://careers.example.test/jobs/7", Title: "Platform Engineer", Text: "Platform Engineer. More jobs below.", Lines: []string{"Platform Engineer"},
		Controls: []Control{
			{I: 0, Tag: "button", Labels: []string{"Apply now"}, Form: -1},
			{I: 1, Tag: "a", Labels: []string{"Apply"}, Href: "https://careers.example.test/jobs/8", Form: -1},
			{I: 2, Tag: "a", Labels: []string{"Apply"}, Href: "https://careers.example.test/jobs/9", Form: -1},
			{I: 3, Tag: "a", Labels: []string{"Apply for this job"}, Href: "https://careers.example.test/jobs/7/apply", Form: -1},
		}}
}

func newDecide(t *testing.T, st *PageState, reply func(prompt string) string) (*wenv, *modelRig) {
	t.Helper()
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, st)
	m := newModelRig(t, w.g.st, func(task, prompt string) string {
		if task != taskPageDecision {
			return ""
		}
		return reply(prompt)
	})
	w.g.r.d.Page.Model = m.router
	return w, m
}

func TestApply_Walk_PageDecisionOnlyAmongNonSubmitAtProbabilityFloors(t *testing.T) {
	// Ambiguous Next: a choice at 0.69 is ignored and the first-in-page-order
	// rule decides; at 0.70 the choice overrides it.
	for _, c := range []struct {
		p    string
		want string
	}{{"0.69", "Apply now"}, {"0.70", "Apply for this job"}} {
		w, m := newDecide(t, cardsPage(), func(prompt string) string {
			return `{"choice":` + choiceOf(prompt, "/jobs/7/apply") + `,"p":` + c.p + `}`
		})
		w.walk()
		if len(w.tab.clicks) == 0 || w.tab.clicks[0] != c.want {
			t.Errorf("p=%s: pressed %v, want %q first", c.p, w.tab.clicks, c.want)
		}
		if m.count(taskPageDecision) != 1 || w.e.Run.ModelCalls != 1 {
			t.Errorf("p=%s: %d decision calls, %d in the run's budget", c.p, m.count(taskPageDecision), w.e.Run.ModelCalls)
		}
	}
	// One call per page: the same page read again is not asked again.
	w, m := newDecide(t, cardsPage(), func(string) string { return `{"choice":0,"p":1}` })
	w.walk()
	if m.count(taskPageDecision) != 1 {
		t.Fatalf("%d decision calls for one page", m.count(taskPageDecision))
	}
	// An unconfigured model leaves the deterministic rules in charge.
	n := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, cardsPage())
	n.walk()
	if len(n.tab.clicks) == 0 || n.tab.clicks[0] != "Apply now" || n.e.Run.ModelCalls != 0 {
		t.Fatalf("clicks %v", n.tab.clicks)
	}
	// So does a capped one.
	cw, cm := newDecide(t, cardsPage(), func(string) string { return `{"choice":4,"p":0.99}` })
	for i := 0; i < MaxModelCalls; i++ {
		cw.e.budget.take()
	}
	cw.walk()
	if cm.count(taskPageDecision) != 0 || len(cw.tab.clicks) == 0 || cw.tab.clicks[0] != "Apply now" {
		t.Fatalf("calls %d clicks %v", cm.count(taskPageDecision), cw.tab.clicks)
	}
}

func choiceOf(prompt, href string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.Contains(line, href) {
			return strings.SplitN(line, ".", 2)[0]
		}
	}
	return "0"
}

func TestApply_Walk_SubmitIsNeverOfferedToThePageDecision(t *testing.T) {
	w, m := newDecide(t, cardsPage(), func(string) string { return `{"choice":1,"p":0.99}` })
	cands := []Control{
		{I: 0, Tag: "button", Labels: []string{"Next"}, Form: 0},
		{I: 1, Tag: "button", Labels: []string{"Submit application"}, Form: 0},
		{I: 2, Tag: "button", Labels: []string{"Continue"}, Form: 0},
	}
	got := newWalker(w).decideNav(w.e.Ctx, cardsPage(), cands)
	if got != 0 {
		t.Fatalf("picked %d", got)
	}
	prompts := m.prompts(taskPageDecision)
	if len(prompts) != 1 || strings.Contains(strings.ToLower(prompts[0]), "submit") {
		t.Fatalf("prompts %v: the offered set holds no Submit control", prompts)
	}
	// With only one control left after the removal there is nothing to decide.
	w2, m2 := newDecide(t, cardsPage(), func(string) string { return `{"choice":1,"p":0.99}` })
	newWalker(w2).decideNav(w2.e.Ctx, cardsPage(), cands[:2])
	if m2.count(taskPageDecision) != 0 {
		t.Fatal("a single remaining control was put to the model")
	}
}

func TestApply_Walk_PageDecisionClosedEnumAndUnknownStops(t *testing.T) {
	blank := func() *PageState {
		return &PageState{URL: "https://careers.example.test/jobs/7", Title: "Platform Engineer", Text: "Thanks for your interest in this role.", Lines: []string{"Thanks for your interest in this role."}}
	}
	for _, c := range []struct {
		name, reply string
		end         WalkEnd
		detail      string
	}{
		{"closed at the floor", `{"kind":"closed","p":0.80}`, EndPostingClosed, ""},
		{"closed under the floor", `{"kind":"closed","p":0.79}`, EndNoForm, "under the floor"},
		{"sign-in over the floor", `{"kind":"sign-in","p":0.9}`, EndLoginRequired, ""},
		{"a word outside the enum", `{"kind":"application form","p":0.99}`, EndNoForm, "unknown answer"},
		{"unknown", `{"kind":"unknown","p":0.99}`, EndNoForm, "unknown answer"},
		{"not the contract", `closed`, EndNoForm, "not the contract"},
	} {
		w, m := newDecide(t, blank(), func(string) string { return c.reply })
		res, st := w.walk()
		if st != nil || res.End != c.end || !strings.Contains(res.Detail, c.detail) {
			t.Errorf("%s: end %+v stop %+v", c.name, res, st)
		}
		if m.count(taskPageDecision) != 1 {
			t.Errorf("%s: %d calls, want one per page", c.name, m.count(taskPageDecision))
		}
		if c.end == EndNoForm && res.End.Class(false) != ClassPosting {
			t.Errorf("%s: class %s", c.name, res.End.Class(false))
		}
	}
}

// A walk that ends on the same site's careers home, away from the posting it
// was given (RWE's Apply opened its job portal), says so in the no-form detail;
// the posting's own page says nothing of the kind.
func TestApply_Walk_NoFormOnTheCareersHomeSaysTheWalkWasSentThere(t *testing.T) {
	for _, c := range []struct {
		url  string
		home bool
	}{{"https://careers.example.test/", true}, {loginURL, false}} {
		page := &PageState{URL: c.url, Title: "Job portal", Text: "Find jobs", Lines: []string{"Find jobs"}}
		w, _ := newDecide(t, page, func(string) string { return `{"kind":"other","p":0.9}` })
		res, st := w.walk()
		if st != nil || res.End != EndNoForm || strings.Contains(res.Detail, "redirected to the careers home") != c.home {
			t.Errorf("%s: end %+v stop %+v", c.url, res, st)
		}
	}
}

func newWalker(w *wenv) *walker {
	return &walker{r: w.g.r, e: w.e, s: w.s, tm: w.g.r.d.Page.Timing.withDefaults(), opened: map[string]bool{}, seen: map[string]int{}, decided: map[string]bool{}}
}

func TestApply_Walk_RefusalCountsOnlySentStepInvalidMarks(t *testing.T) {
	st := wizardStep(1, 3)
	st.Inputs = []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "First name", Form: 0}}
	st.Validation = []string{"First name invalid", "Phone invalid", "Please fix the errors above"}
	got := validationWords(st)
	if !strings.Contains(got, `1 field(s) marked invalid: "First name"`) || strings.Contains(got, "Phone") || !strings.Contains(got, "Please fix the errors above") {
		t.Fatalf("words %q: only marks on the sent step's controls count, and they are named", got)
	}
	// A refusal that says an account exists is the user's credential.
	g := newRig(t, nil)
	e := &Exec{r: g.r, Run: &Run{ID: "run_c", StartedAt: t0, Input: Input{URL: loginURL, Caller: gates.CallerCLI}}, Ctx: context.Background()}
	s := &session{r: g.r, e: e}
	wr := &walkResult{End: EndStalled, Detail: "the step returned with the same control and writes; the page says: There's an existing record with that email", Last: st}
	stp := g.r.afterWalk(context.Background(), e, s, t0, wr, nil)
	if stp == nil || stp.class != ClassCredential || stp.board != "sign-in" {
		t.Fatalf("stop %+v", stp)
	}
}

// Boeing (Taleo): a step that would not go on while only the SSN, which no
// run types, stands empty waits for the user, not on a defect; any other empty
// box keeps the walk's own class.
func TestApply_Walk_AStallOnlyTheUsersIdentifierHoldsIsTheUsers(t *testing.T) {
	g := newRig(t, nil)
	stall := func(empty ...string) *stop {
		st := wizardStep(1, 3)
		st.Stall.Empty = empty
		e := &Exec{r: g.r, Run: &Run{ID: "run_s", StartedAt: t0, Input: Input{URL: loginURL, Caller: gates.CallerCLI}}, Ctx: context.Background()}
		wr := &walkResult{End: EndStalled, Detail: "the step returned with the same control and writes", Last: st}
		return g.r.afterWalk(context.Background(), e, &session{r: g.r, e: e}, t0, wr, nil)
	}
	if stp := stall("SSN"); stp == nil || stp.class != ClassAnswerGap || stp.board != boardNeedsUser || !strings.Contains(stp.reason, `requires "SSN", which a run never types`) {
		t.Fatalf("stop %+v", stp)
	}
	for _, empty := range [][]string{{"SSN", "Middle name"}, {"Branch"}, nil} {
		if stp := stall(empty...); stp == nil || stp.class != ClassDefect {
			t.Errorf("%v: stop %+v", empty, stp)
		}
	}
}

// A fill the tab left for another origin (Procore's name-and-email step
// redirecting into its Workday tenant) is a page that moved: the walk reads the
// page it moved to and goes on, rather than stopping on a fill defect.
func TestApply_Walk_AFillThePageMovedAwayFromReadsTheNewPage(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 3))
	w.tab.onClick = advanceOnNext(3)
	w.page.fillErr, w.page.fillErrOnce = &engine.Error{Code: engine.CodeTopOriginChanged, Message: "the tab moved to another origin during the fill"}, true
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 4 || len(w.tab.clicks) != 2 {
		t.Fatalf("end %+v stop %+v fills %d clicks %v", res, st, w.page.nFills, w.tab.clicks)
	}
	// Any other fill failure still ends the walk.
	w = newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 3))
	w.page.fillErr, w.page.fillErrOnce = &engine.Error{Code: engine.CodePollFailed, Message: "poll failed"}, true
	if _, st := w.walk(); st == nil || !strings.Contains(st.reason, "poll-failed") {
		t.Fatalf("stop %+v", st)
	}
}

// Boeing (Taleo Business Edition): Next posts the résumé while the form stays
// on screen, so the walk's look reads the old form; the fill then meets the
// wait page that reloads itself until the parse is done, and reads no form. The
// walk reads the tab again and fills the step after the wait. The two fills
// that read no form are what stopped the run when the fill looked once more at
// the page it had been given.
func TestApply_Walk_AFormLeftDuringTheFillIsReadAgain(t *testing.T) {
	notApp := &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{NotAnApplication: true}}}
	step := func(n int) *PageState {
		st := wizardStep(n, 2)
		st.Inputs = append(st.Inputs, InputInfo{I: 2, Tag: "input", Type: "text", Label: "Last Name", Form: 0})
		return st
	}
	wait := &PageState{URL: "https://careers.example.test/parsingProcess?attemptCount=1"}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, step(1))
	w.tab.onClick = advanceOnNext(2)
	w.page.fills = []*engine.PageResult{notApp, notApp, doneResult(scriptedRow)}
	w.page.onFill = func(n int) {
		if n == 1 {
			w.tab.script(wait, wait, step(2))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 3 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
}
