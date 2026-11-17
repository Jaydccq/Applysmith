package apply

import (
	"slices"
	"strings"
	"testing"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// A posting whose application the site holds unfinished offers it as a link
// (Avature's "Complete Application" on Two Sigma, to ApplicationForm?jobId=):
// the walk opens the link's own address, a GET that never submits, instead of
// pressing a control whose words could be a final Submit elsewhere. A link to
// another site is not followed.
func TestApply_Walk_AResumeApplicationLinkIsOpenedByItsAddress(t *testing.T) {
	for _, c := range []struct {
		name, href string
		follows    bool
	}{
		{"same site", "https://careers.example.test/careers/ApplicationForm?jobId=7", true},
		{"another site", "https://elsewhere.example.org/careers/ApplicationForm?jobId=7", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			posting := postingPage(Control{I: 1, Tag: "a", Labels: []string{"— Complete Application"}, Href: c.href, Form: -1})
			posting.Text = "Finish your application Platform Engineer Complete Application Position Summary"
			posting.Links = []LinkInfo{{Text: "— Complete Application", Href: c.href}}
			w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, posting)
			w.tab.onNav = func(tab *scriptTab, url string) {
				if url == c.href {
					tab.set(formState())
				}
			}
			res, st := w.walk()
			if len(w.tab.clicks) != 0 {
				t.Fatalf("clicks %v: the link is never pressed", w.tab.clicks)
			}
			opened := slices.Contains(w.tab.navs, c.href)
			if opened != c.follows {
				t.Fatalf("navigations %v, want the link opened: %v", w.tab.navs, c.follows)
			}
			if c.follows && (st != nil || res.End != EndSubmit) {
				t.Fatalf("end %+v stop %+v: the opened form is walked to Submit", res, st)
			}
		})
	}
}

// Elastic's posting page: its menu's bare "Apply Now" leads to the careers
// home, and the posting's own "Apply Now for Agentic AI Engineer" to
// /form?gh_jid=8079636, the posting's number. The walk opens that link's
// address instead of pressing the bare one; a named link for another job, or
// one on another site, is not opened.
func TestApply_Walk_ThePostingsNamedApplyLinkIsOpenedByItsAddress(t *testing.T) {
	const posting = "https://careers.example.test/jobs/engineering/agentic-ai-engineer/8079636?gh_jid=8079636"
	const home = "https://careers.example.test/#/"
	for _, c := range []struct {
		name, label, href string
		opens             bool
	}{
		{"this posting", "Apply Now for Agentic AI Engineer", "https://careers.example.test/form?gh_jid=8079636", true},
		{"another job", "Apply Now for Data Engineer", "https://careers.example.test/form?gh_jid=8123456", false},
		{"another site", "Apply Now for Agentic AI Engineer", "https://elsewhere.example.org/form?gh_jid=8079636", false},
		{"an alert", "Apply for job alerts 8079636", "https://careers.example.test/alerts?gh_jid=8079636", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			page := postingPage(Control{I: 1, Tag: "a", Labels: []string{"Apply Now"}, Href: home, Form: -1},
				Control{I: 2, Tag: "a", Labels: []string{c.label}, Href: c.href, Form: -1})
			page.URL = posting
			page.Links = []LinkInfo{{Text: "Apply Now", Href: home}, {Text: c.label, Href: c.href}}
			w := newWalk(t, Input{URL: posting, Caller: gates.CallerCLI}, page)
			w.tab.onNav = func(tab *scriptTab, url string) {
				if url == c.href {
					tab.set(formState())
				}
			}
			w.tab.onClick = func(tab *scriptTab, _ Control) { tab.set(postingPage()) }
			res, st := w.walk()
			opened := slices.Contains(w.tab.navs, c.href)
			if opened != c.opens {
				t.Fatalf("navigations %v clicks %v, want the link opened: %v", w.tab.navs, w.tab.clicks, c.opens)
			}
			if c.opens && (len(w.tab.clicks) != 0 || st != nil || res.End != EndSubmit) {
				t.Fatalf("end %+v stop %+v clicks %v: the opened form is walked to Submit and nothing is pressed", res, st, w.tab.clicks)
			}
		})
	}
}

// Elastic draws its menu's bare "Apply Now", which links the careers home, before
// the posting's own Apply links: the first look waits for the posting's link
// instead of taking the menu's for the posting's Apply.
func TestApply_Walk_AnApplyLinkToTheSiteHomeIsNotThePostingsApply(t *testing.T) {
	const posting = "https://careers.example.test/jobs/engineering/agentic-ai-engineer/8079636?gh_jid=8079636"
	const form = "https://careers.example.test/form?gh_jid=8079636"
	for _, home := range []string{"https://careers.example.test/#/", "https://careers.example.test/", "https://careers.example.test"} {
		t.Run(home, func(t *testing.T) {
			menu := Control{I: 1, Tag: "a", Labels: []string{"Apply Now"}, Href: home, Form: -1}
			early := postingPage(menu)
			early.URL = posting
			early.Links = []LinkInfo{{Text: "Apply Now", Href: home}}
			drawn := postingPage(menu, Control{I: 2, Tag: "a", Labels: []string{"Apply Now for Agentic AI Engineer"}, Href: form, Form: -1})
			drawn.URL = posting
			drawn.Links = []LinkInfo{{Text: "Apply Now", Href: home}, {Text: "Apply Now for Agentic AI Engineer", Href: form}}
			w := newWalk(t, Input{URL: posting, Caller: gates.CallerCLI}, early)
			w.tab.script(early, early, early, drawn)
			w.tab.onNav = func(tab *scriptTab, url string) {
				if url == form {
					tab.set(formState())
				}
			}
			w.tab.onClick = func(tab *scriptTab, _ Control) { tab.set(postingPage()) }
			res, st := w.walk()
			if len(w.tab.clicks) != 0 || !slices.Contains(w.tab.navs, form) || st != nil || res.End != EndSubmit {
				t.Fatalf("end %+v stop %+v navigations %v clicks %v: the posting's link is opened, the menu's never pressed", res, st, w.tab.navs, w.tab.clicks)
			}
		})
	}
}

// Airbnb's posting page holds the Greenhouse application in its "Application"
// tab, behind "Role overview": the form reads, but its Submit is not drawn
// until the tab is pressed. The walk presses the tab and goes on to Submit; a
// tab already selected is never pressed.
func TestApply_Walk_TheApplicationTabIsOpened(t *testing.T) {
	tab := func(selected string) Control {
		return Control{I: 7, Tag: "button", Role: "tab", Labels: []string{"Application"}, Selected: selected, Form: -1}
	}
	hidden := formState()
	hidden.URL, hidden.Controls = pageURL, []Control{tab("false")}
	w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, hidden)
	w.tab.onClick = func(st *scriptTab, c Control) {
		if c.Labels[0] == "Application" {
			shown := formState()
			shown.URL, shown.Controls = pageURL, append(shown.Controls, tab("true"))
			st.set(shown)
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || !slices.Contains(w.tab.clicks, "Application") {
		t.Fatalf("end %+v stop %+v clicks %v: the application tab is pressed and the form walked to Submit", res, st, w.tab.clicks)
	}
	// Fivetran's careers page: the Greenhouse frame in the hidden panel already
	// reads as the form; the tab is shown first, so the form is filled once.
	if w.page.nFills != 1 {
		t.Fatalf("%d fills: the tab is shown before the fill", w.page.nFills)
	}

	open := formState()
	open.URL, open.Controls = pageURL, []Control{tab("true")}
	w = newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, open)
	if _, _ = w.walk(); slices.Contains(w.tab.clicks, "Application") {
		t.Fatalf("clicks %v: a selected tab is never pressed", w.tab.clicks)
	}
}

// Toast's careers site: the whole application in one form (a résumé upload
// among its fields) ends in "Apply now!" named next_step, under a cookie
// dialog whose buttons outrank the page behind it. The walk declines the
// dialog, then reads "Apply now!" as the form's final Apply and ends at
// Submit: it never presses it, and never ends on a form it could not read.
func TestApply_Walk_CookieDialogOverAWholeApplicationIsDeclinedAndItsApplyIsTheSubmit(t *testing.T) {
	page := func(dialog bool) *PageState {
		st := formState()
		st.Forms[0].Files, st.Forms[0].HasResume = 1, true
		st.Inputs = append(st.Inputs, InputInfo{I: 5, Tag: "input", Type: "file", Label: "Resume", Form: 0})
		st.Controls = []Control{{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply now!"}, Form: 0, Name: "next_step"}}
		if dialog {
			st.Controls = append(st.Controls,
				Control{I: 2, Tag: "button", Labels: []string{"Manage cookies"}, Form: -1, Modal: true},
				Control{I: 3, Tag: "button", Labels: []string{"I do not accept"}, Form: -1, Modal: true},
				Control{I: 4, Tag: "button", Labels: []string{"I accept"}, Form: -1, Modal: true})
		}
		return st
	}
	w := newWalk(t, Input{URL: pageURL, Caller: gates.CallerCLI}, page(true))
	w.tab.consent = &ConsentGate{Found: true, Text: "We use cookies", Controls: []ConsentButton{{I: 2, Label: "Manage cookies"}, {I: 3, Label: "I do not accept"}, {I: 4, Label: "I accept"}}}
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "I do not accept" {
			t.consent = nil
			t.set(page(false))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v clicks %v", res, st, w.tab.clicks)
	}
	if got := strings.Join(w.tab.clicks, ","); got != "I do not accept" {
		t.Fatalf("clicks %q: the dialog is declined and Apply now! is never pressed", got)
	}
}

// Toast's careers site: the fill reads the form under the cookie dialog, but
// the phone box's country picker takes real clicks, which land on the dialog,
// and the write is not committed. A write that missed under a consent gate
// closes the gate with the least consent and fills once more.
func TestApply_Walk_AWriteMissedUnderACookieDialogIsFilledAgainOnceTheDialogIsDeclined(t *testing.T) {
	form := wizardStep(1, 1)
	form.Controls = append(form.Controls, Control{I: 2, Tag: "button", Labels: []string{"I accept"}, Form: -1, Chrome: true}, Control{I: 3, Tag: "button", Labels: []string{"I do not accept"}, Form: -1, Chrome: true})
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, form)
	w.tab.consent = &ConsentGate{Found: true, Text: "We use cookies", Controls: []ConsentButton{{I: 2, Label: "I accept"}, {I: 3, Label: "I do not accept"}}}
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "I do not accept" {
			t.consent = nil
		}
	}
	missed := engine.Row{Selector: "#phone", Label: "Phone", Kind: "text", Outcome: "failed", Reason: "write-not-committed"}
	phone := engine.Row{Selector: "#phone", Label: "Phone", Kind: "text", Outcome: "filled", Value: "+1 555 0100"}
	w.page.fills = []*engine.PageResult{doneResult(missed), doneResult(phone)}
	w.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#phone", Label: "Phone", Kind: "text", Value: "+1 555 0100"}}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.page.nFills != 2 {
		t.Fatalf("end %+v stop %+v fills %d", res, st, w.page.nFills)
	}
	if got := strings.Join(w.tab.clicks, ","); got != "I do not accept" {
		t.Fatalf("clicks %q", got)
	}
	// With no consent gate on the page, a missed write is not filled again here.
	w2 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 1))
	w2.page.fills = []*engine.PageResult{doneResult(missed), doneResult(phone)}
	w2.walk()
	if w2.page.nFills != 1 || len(w2.tab.clicks) != 0 {
		t.Fatalf("no gate: fills %d clicks %v", w2.page.nFills, w2.tab.clicks)
	}
	// Fivetran's careers page: the Greenhouse form sits in a frame, so the top
	// document reads no row while the frame reads the whole form. The fill
	// reached the form; a second fill under the cookie dialog would only
	// re-read a location search it already chose.
	w3 := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, form)
	w3.tab.consent = &ConsentGate{Found: true, Text: "We use cookies", Controls: []ConsentButton{{I: 2, Label: "I accept"}, {I: 3, Label: "I do not accept"}}}
	w3.tab.onClick = w.tab.onClick
	framed := &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{}},
		Frames: []engine.FrameResult{{Status: &engine.Status{State: engine.StateDone, Summary: &engine.Summary{Rows: []engine.Row{phone}}}}}}
	w3.page.fills = []*engine.PageResult{framed, framed}
	w3.page.rows = w.page.rows
	w3.walk()
	if w3.page.nFills != 1 || len(w3.tab.clicks) != 0 {
		t.Fatalf("framed form: fills %d clicks %v", w3.page.nFills, w3.tab.clicks)
	}
}

// A run the scheduler started is watched by nobody: the press gate refuses its
// first control and nothing is clicked.
func TestApply_Walk_AnUnattendedRunPressesNoControl(t *testing.T) {
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerScheduler}, wizardStep(1, 3))
	w.tab.onClick = advanceOnNext(3)
	_, st := w.walk()
	if st == nil || !strings.Contains(st.reason, "unattended") || len(w.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %v: an unattended run presses nothing", st, w.tab.clicks)
	}
	if n := w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'deny' AND code = ?`, gates.CodeUnattendedCaller); n != 1 {
		t.Fatalf("%d audited refusals, want 1", n)
	}
}
