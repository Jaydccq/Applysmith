package apply

import (
	"testing"

	"applysmith/internal/gates"
)

func ctl(i int, tag string, form int, labels ...string) Control {
	return Control{I: i, Tag: tag, Labels: labels, Form: form, Order: i}
}

func formPage(controls ...Control) *PageState {
	return &PageState{
		URL:      "https://careers.example.test/jobs/7",
		Forms:    []FormInfo{{Fields: 3}},
		Inputs:   []InputInfo{{Type: "text", Form: 0}},
		Controls: controls,
	}
}

func TestApply_Walk_ApplyInsideTheFormIsNeverPressedByTheWalk(t *testing.T) {
	st := formPage(ctl(0, "button", 0, "Apply"), ctl(1, "button", 0, "Next"))
	c := Categorize(st, false)
	if len(c.InFormApply) != 1 || len(c.Nav) != 1 || c.Nav[0].Labels[0] != "Next" {
		t.Fatalf("categories %+v", c)
	}
}

func TestApply_Walk_ModalControlOutranksBackground(t *testing.T) {
	back := ctl(0, "button", 0, "Next")
	modal := ctl(1, "button", -1, "Continue")
	modal.Modal = true
	st := formPage(back, modal)
	st.Inputs = append(st.Inputs, InputInfo{Type: "text", Form: -1, Modal: true})
	c := Categorize(st, false)
	if len(c.Nav) != 1 || c.Nav[0].Labels[0] != "Continue" {
		t.Fatalf("nav %+v", c.Nav)
	}
}

// Eightfold's "Create your profile with existing information" dialog after
// sign-in: its manual method opens the form, its upload method is never pressed.
func TestApply_Walk_ProfileChooserOffersTheManualMethod(t *testing.T) {
	upload, manual := ctl(0, "button", -1, "Upload your resume"), ctl(1, "button", -1, "Enter information")
	upload.Modal, manual.Modal = true, true
	c := Categorize(&PageState{Modal: true, Controls: []Control{upload, manual}}, false)
	if len(c.Openers) != 1 || c.Openers[0].Labels[0] != "Enter information" || len(c.Nav)+len(c.Submit) != 0 {
		t.Fatalf("categories %+v", c)
	}
}

// profileDialogPage is Eightfold's "Create your profile" dialog after its
// manual method: names, the phone and its country code, each filled.
func profileDialogPage() *PageState {
	box := func(i int, label string) InputInfo {
		return InputInfo{I: i, Tag: "input", Type: "text", Label: label, Form: -1, Modal: true, Required: true, Filled: true}
	}
	back, submit := ctl(10, "button", -1, "Back"), ctl(11, "button", -1, "Submit")
	back.Modal, submit.Modal, submit.Type = true, true, "submit"
	return &PageState{URL: "https://careers.example.test/careers/apply?pid=7", Modal: true,
		Dialogs:  []DialogInfo{{Heading: "Create your profile"}},
		Inputs:   []InputInfo{box(0, "First Name"), box(1, "Last Name"), box(2, "Country code"), box(3, "Phone Number")},
		Controls: []Control{back, submit}}
}

// The Submit of a filled profile dialog saves the profile, not the
// application: it is a walk step pressed as the profile stage. A box that is
// no identity box, or one left empty, keeps it the application's Submit.
func TestApply_Walk_ProfileDialogSubmitIsAStepOnlyOverFilledIdentityBoxes(t *testing.T) {
	c := Categorize(profileDialogPage(), false)
	if len(c.Nav) != 1 || !c.Nav[0].ProfileStep || c.Nav[0].walkStage() != gates.StageProfileDialog || len(c.Submit) != 0 {
		t.Fatalf("categories %+v", c)
	}
	asks := profileDialogPage()
	asks.Inputs = append(asks.Inputs, InputInfo{I: 4, Tag: "textarea", Type: "textarea", Label: "Why do you want this job?", Form: -1, Modal: true, Filled: true})
	empty := profileDialogPage()
	empty.Inputs[3].Filled = false
	for name, st := range map[string]*PageState{"a question in the dialog": asks, "an empty box": empty} {
		if c := Categorize(st, false); len(c.Submit) != 1 || len(c.Nav) != 0 {
			t.Errorf("%s: categories %+v, want the Submit kept the application's", name, c)
		}
	}
}

func TestApply_Walk_SearchAlertNewsletterFormsAreFurniture(t *testing.T) {
	st := &PageState{
		URL:      "https://careers.example.test/jobs/7",
		Forms:    []FormInfo{{Fields: 2, Furniture: true}},
		Inputs:   []InputInfo{{Type: "search", Form: 0, Furniture: true}},
		Controls: []Control{ctl(0, "button", 0, "Apply"), ctl(1, "button", -1, "Join our talent community"), ctl(2, "button", -1, "Get job alerts")},
	}
	if st.HasForm() {
		t.Fatal("a search form read as the application")
	}
	c := Categorize(st, false)
	if len(c.Openers)+len(c.Nav)+len(c.Submit)+len(c.InFormApply) != 0 {
		t.Fatalf("furniture offered: %+v", c)
	}
}

func TestApply_Walk_ChatWidgetAndSiteChromeAreNeverTargets(t *testing.T) {
	chat := ctl(0, "button", -1, "Apply now")
	chat.Chat = true
	nav := ctl(1, "a", -1, "Apply")
	nav.Chrome = true
	st := &PageState{URL: "https://careers.example.test/", Controls: []Control{chat, nav}}
	c := Categorize(st, false)
	if len(c.Openers) != 0 {
		t.Fatalf("openers %+v", c.Openers)
	}
}

func TestApply_Walk_WithdrawAndDiscardControlsAreNeverOffered(t *testing.T) {
	st := formPage(ctl(0, "button", 0, "Withdraw application"), ctl(1, "button", 0, "Delete draft application"), ctl(2, "button", 0, "Continue"))
	c := Categorize(st, false)
	if len(c.Nav) != 1 || len(c.Submit) != 0 {
		t.Fatalf("categories %+v", c)
	}
}

func TestApply_Walk_DisabledControlStatingClosedIsPostingClosed(t *testing.T) {
	d := ctl(0, "button", 0, "Position closed")
	d.Disabled = true
	c := Categorize(formPage(d), false)
	if c.ClosedLabel != "Position closed" {
		t.Fatalf("categories %+v", c)
	}
}

func TestApply_Walk_LastWizardStepReviewEndsAtSubmitEvenWithNoField(t *testing.T) {
	st := &PageState{URL: "https://example.wd5.myworkdayjobs.com/x", Step: &StepInfo{Current: 3, Total: 3}, Controls: []Control{ctl(0, "button", -1, "Submit")}}
	if c := Categorize(st, true); len(c.Submit) != 1 {
		t.Fatalf("a review step after a form page: %+v", c)
	}
	if c := Categorize(st, false); len(c.Submit) != 0 {
		t.Fatalf("a field-less page with no form before it offered a Submit: %+v", c)
	}
	if !st.Step.Last() || (&StepInfo{Current: 2, Total: 3}).Last() {
		t.Fatal("step marker")
	}
}

// ADP's "Review Your Application" step: a read-only summary (Edit links), then
// the wizard footer Previous / Finish later / Next. No field, no <form>.
func TestApply_Walk_ReadOnlyReviewStepOffersNextAsNavigationNeverFinishLater(t *testing.T) {
	st := &PageState{URL: "https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html?jobId=1", Controls: []Control{
		ctl(0, "sdf-button", -1, "Edit Personal Information"),
		ctl(1, "button", -1, "Previous"),
		ctl(2, "button", -1, "Finish later"),
		ctl(3, "button", -1, "Next"),
	}}
	c := Categorize(st, true)
	if len(c.Nav) != 1 || c.Nav[0].Labels[0] != "Next" || len(c.Openers) != 0 || len(c.Submit) != 0 {
		t.Fatalf("a review step after a form page: %+v", c)
	}
	if c := Categorize(st, false); len(c.Nav) != 0 || len(c.Openers) != 1 {
		t.Fatalf("before any form page Next is an opener: %+v", c)
	}
	posting := &PageState{URL: "https://workforcenow.adp.com/x", Controls: []Control{ctl(0, "button", -1, "Complete Your Application"), ctl(1, "button", -1, "Apply")}}
	if c := Categorize(posting, true); len(c.Nav) != 0 || len(c.Openers) != 2 {
		t.Fatalf("an Apply or Complete Your Application control opens, whatever came before: %+v", c)
	}
}

func TestApply_Walk_OpenerAnchorsRankedOtherPostingCardsDropped(t *testing.T) {
	card := Control{I: 0, Tag: "a", Labels: []string{"Apply now"}, Href: "/jobs/other-role-99", Form: -1}
	apply := Control{I: 1, Tag: "a", Labels: []string{"Apply now"}, Href: "/jobs/7/apply", Form: -1}
	btn := Control{I: 2, Tag: "button", Labels: []string{"Apply now"}, Form: -1}
	st := &PageState{URL: "https://careers.example.test/jobs/7", Controls: []Control{card, apply, btn}}
	c := Categorize(st, false)
	if len(c.Openers) != 3 || c.Openers[0].I != 2 || c.Openers[1].I != 1 || c.Openers[2].I != 0 {
		t.Fatalf("order %+v", c.Openers)
	}
}

func TestApply_Walk_OneClickApplyIsReportedNeverPressed(t *testing.T) {
	st := &PageState{URL: "https://example.test/j", Controls: []Control{ctl(0, "button", -1, "1-click apply")}}
	c := Categorize(st, false)
	if c.OneClick == "" || len(c.Openers) != 0 {
		t.Fatalf("categories %+v", c)
	}
}

// The Apply controls of five systems that reached no form in a live survey:
// SmartRecruiters' "I'm interested" link, Breezy's "Apply To Position", the
// Taleo button (value "Apply Online", title "Apply for this position online")
// and the iCIMS link that stands in a same-origin frame.
func TestApply_Walk_SurveyedApplyWordingIsAnOpenerNotNavigationOrSubmit(t *testing.T) {
	for name, c := range map[string]Control{
		"smartrecruiters": {I: 0, Tag: "a", Labels: []string{"I'm interested"}, Href: "https://jobs.smartrecruiters.com/oneclick-ui/company/CircleLogistics1/publication/d20c0deb", Form: -1},
		"breezy":          {I: 0, Tag: "a", Labels: []string{"Apply To Position"}, Href: "/p/611f14be34db-ai-cloud-infra-software-engineer-fresh-grad/apply", Form: -1},
		"taleo":           {I: 0, Tag: "input", Type: "button", Labels: []string{"Apply Online", "Apply for this position online"}, Form: -1},
		"icims frame":     {I: 0, Tag: "a", Labels: []string{"Apply for this job online"}, Href: "https://studentopportunities-lutron.icims.com/jobs/5478/login", Form: -1},
	} {
		t.Run(name, func(t *testing.T) {
			st := &PageState{URL: "https://careers.example.test/jobs/7", Controls: []Control{c}}
			for _, afterForm := range []bool{false, true} {
				cats := Categorize(st, afterForm)
				if len(cats.Openers) != 1 || len(cats.Nav) != 0 || len(cats.Submit) != 0 {
					t.Fatalf("afterForm=%v: categories %+v", afterForm, cats)
				}
			}
			// Inside a form that holds fields the same wording is never pressed.
			in := formPage(c)
			in.Controls[0].Form = 0
			if cats := Categorize(in, false); len(cats.InFormApply) != 1 || len(cats.Openers) != 0 {
				t.Fatalf("in a form: %+v", cats)
			}
		})
	}
}

// A control that reads as Apply but that the walk does not follow is reported
// as a gap in the walk, never as a fact about the posting; filters, site
// chrome, furniture and a control the walk follows are not.
func TestApply_Walk_UnrecognisedApplyShapedControlIsNamedAndNothingElse(t *testing.T) {
	indeed := ctl(0, "a", -1, "Apply with Indeed")
	if got := unrecognisedApply(&PageState{Controls: []Control{ctl(1, "button", -1, "Apply filters"), indeed}}); got != "Apply with Indeed" {
		t.Fatalf("got %q", got)
	}
	chrome := ctl(2, "a", -1, "Apply with Indeed")
	chrome.Chrome = true
	disabled := ctl(3, "button", -1, "Apply with Indeed")
	disabled.Disabled = true
	for name, st := range map[string]*PageState{
		"a recognised opener":  {Controls: []Control{ctl(0, "button", -1, "Apply Online")}},
		"site chrome":          {Controls: []Control{chrome}},
		"disabled":             {Controls: []Control{disabled}},
		"furniture form":       {Forms: []FormInfo{{Furniture: true}}, Controls: []Control{ctl(0, "button", 0, "Apply with Indeed")}},
		"job alert":            {Controls: []Control{ctl(0, "button", -1, "Get job alerts")}},
		"no apply-shaped word": {Controls: []Control{ctl(0, "button", -1, "Save job")}},
	} {
		if got := unrecognisedApply(st); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestApply_Walk_BlankPageIsOneThatDrewNothing(t *testing.T) {
	if !blankPage(&PageState{URL: "https://jobs.smartrecruiters.com/oneclick-ui/company/x/publication/y"}) {
		t.Fatal("an empty document is blank")
	}
	for name, st := range map[string]*PageState{
		"text":    {Text: "Loading your application"},
		"control": {Controls: []Control{ctl(0, "button", -1, "Retry")}},
		"input":   {Inputs: []InputInfo{{Type: "text"}}},
		"frame":   {Frames: []FrameInfo{{Src: "https://x.example.test/"}}},
		"link":    {Links: []LinkInfo{{Text: "Home", Href: "https://x.example.test/"}}},
	} {
		if blankPage(st) {
			t.Errorf("%s: a page with something on it read as blank", name)
		}
	}
}

// A filled form whose only forward control is its own Apply button of type
// submit (Rippling) ends the form the way Submit does: Categorize lists it as
// the form's Submit with FinalApply set, and in no other case. A Next beside
// it, another type, another wording, a form with no field and a first page
// each keep it an in-form Apply that the walk never presses.
func TestApply_Walk_InFormApplyIsTheFormsFinalControlOnlyAsTheLastForwardButton(t *testing.T) {
	apply := Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply"}, Form: 0}
	c := Categorize(formPage(apply), true)
	if len(c.Submit) != 1 || !c.Submit[0].FinalApply || c.Submit[0].I != 1 || len(c.Nav) != 0 || len(c.Openers) != 0 || len(c.InFormApply) != 1 {
		t.Fatalf("the last forward button: %+v", c)
	}
	for name, st := range map[string]*PageState{
		"a Next beside it":     formPage(apply, ctl(2, "button", 0, "Next")),
		"a plain button":       formPage(Control{I: 1, Tag: "button", Type: "button", Labels: []string{"Apply"}, Form: 0}),
		"a link":               formPage(Control{I: 1, Tag: "a", Labels: []string{"Apply"}, Form: 0}),
		"another wording":      formPage(Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply filters"}, Form: 0}),
		"Start":                formPage(Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Start"}, Form: 0}),
		"a form with no field": {URL: "https://careers.example.test/jobs/7", Forms: []FormInfo{{}}, Controls: []Control{apply}},
	} {
		if c := Categorize(st, true); len(c.Submit) != 0 {
			t.Errorf("%s: %+v is read as the form's Submit", name, c.Submit)
		}
	}
	// Before any page of the application was walked the control is only ever an
	// in-form Apply, and a real Submit keeps its own place.
	if c := Categorize(formPage(apply), false); len(c.Submit) != 0 || len(c.InFormApply) != 1 {
		t.Errorf("first page: %+v", c)
	}
	// evlo's last step: "Apply to Job" beside its Back.
	toJob := Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply to Job"}, Form: 0}
	if c := Categorize(formPage(toJob, Control{I: 2, Tag: "button", Type: "button", Labels: []string{"Back"}, Form: 0}), true); len(c.Submit) != 1 || !c.Submit[0].FinalApply {
		t.Errorf("Apply to Job: %+v", c)
	}
	send := Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Send"}, Form: 0}
	if c := Categorize(formPage(send), true); len(c.Submit) != 1 || !c.Submit[0].FinalApply {
		t.Errorf("a form's own Send: %+v", c)
	}
	modal := send
	modal.Modal = true
	if c := Categorize(formPage(modal), true); len(c.Submit) != 0 {
		t.Errorf("a dialog's Send: %+v", c.Submit)
	}
	// Gem: "Apply without saving" sends this application alone and is the final
	// Apply; "Apply and save" beside it also keeps the applicant's profile at
	// Gem, which is never pressed. Gem's fields and buttons sit in no <form>.
	gem := Categorize(&PageState{URL: "https://jobs.gem.com/careers-acme/am9i", Inputs: []InputInfo{{Type: "text", Form: -1}}, Controls: []Control{
		{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply and save"}, Form: -1},
		{I: 2, Tag: "button", Type: "submit", Labels: []string{"Apply without saving"}, Form: -1},
	}}, true)
	if len(gem.Submit) != 1 || !gem.Submit[0].FinalApply || gem.Submit[0].I != 2 {
		t.Errorf("Gem: %+v", gem.Submit)
	}
	// SuccessFactors (Huntington Ingalls): a span with role=button whose id names
	// the submit; its Save beside it sends nothing.
	sf := Control{I: 1, Tag: "span", Role: "button", Labels: []string{"Apply"}, Form: 0, ID: "491:_submitBtn"}
	if c := Categorize(formPage(sf, Control{I: 2, Tag: "span", Role: "button", Labels: []string{"Save"}, Form: 0, ID: "491:_saveBtn"}), true); len(c.Submit) != 1 || !c.Submit[0].FinalApply || c.Submit[0].I != 1 {
		t.Errorf("SuccessFactors' submit span: %+v", c)
	}
	sf.ID = "491:_saveBtn"
	if c := Categorize(formPage(sf), true); len(c.Submit) != 0 {
		t.Errorf("an Apply span with no submit id: %+v", c.Submit)
	}
	both := Categorize(formPage(apply, ctl(2, "button", 0, "Submit application")), true)
	if len(both.Submit) != 1 || both.Submit[0].FinalApply || both.Submit[0].Labels[0] != "Submit application" {
		t.Errorf("a real Submit beside it: %+v", both.Submit)
	}
}

// A form's Apply button named "next_step" moves the application to its next
// step (Procore's name-and-email step before the questions): it is the page's
// Next on the first page and after one, never the form's final Submit.
func TestApply_Walk_InFormApplyNamedForTheNextStepIsANext(t *testing.T) {
	step := Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply"}, Form: 0, Name: "next_step"}
	for _, afterForm := range []bool{false, true} {
		c := Categorize(formPage(step), afterForm)
		if len(c.Nav) != 1 || c.Nav[0].I != 1 || len(c.Submit) != 0 || len(c.InFormApply) != 0 {
			t.Errorf("after a form %v: %+v", afterForm, c)
		}
	}
	other := step
	other.Name = "commit"
	if c := Categorize(formPage(other), true); len(c.Submit) != 1 || !c.Submit[0].FinalApply || len(c.Nav) != 0 {
		t.Errorf("another name: %+v", c)
	}
	// Toast's careers site (the same markup) holds the whole application in
	// one form, résumé upload and all, under "Apply now!" named next_step: that
	// Apply sends the application. It is the form's final Apply, never a Next.
	whole := formPage(Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply now!"}, Form: 0, Name: "next_step"})
	whole.Forms[0].Files, whole.Forms[0].HasResume = 1, true
	for _, afterForm := range []bool{false, true} {
		c := Categorize(whole, afterForm)
		if len(c.Nav) != 0 || len(c.Openers) != 0 {
			t.Errorf("a whole application after a form %v: %+v", afterForm, c)
		}
		if afterForm && (len(c.Submit) != 1 || !c.Submit[0].FinalApply) {
			t.Errorf("a whole application's Apply is its final Apply: %+v", c)
		}
	}
}

// Node's open-application tests (ZipRecruiter's rail, Dell's repeated Apply,
// a one-click Apply in the rail): an Apply label offered in two or more job
// cards is a list of other jobs, the same label twice in one card is the
// posting's own, and a control in an <aside> rail ranks after the posting's.
func TestApply_Walk_OpenersOfOtherJobCardsDroppedAndTheAsideRailRanksLast(t *testing.T) {
	card := func(i, n int, aside bool, labels ...string) Control {
		c := ctl(i, "button", -1, labels...)
		c.Card, c.Aside = n, aside
		return c
	}
	rail := []Control{card(1, 1, true, "Apply"), card(2, 2, true, "Apply"), card(3, 3, true, "Apply")}
	page := func(cs ...Control) *PageState {
		return &PageState{URL: "https://www.ziprecruiter.com/c/x/Job/Engineer", Controls: cs}
	}
	if c := Categorize(page(rail...), false); len(c.Openers) != 0 {
		t.Fatalf("openers %+v: the rail's Apply belongs to other jobs", c.Openers)
	}
	if c := Categorize(page(append(append([]Control(nil), rail...), ctl(4, "button", -1, "Apply now"))...), false); len(c.Openers) != 1 || c.Openers[0].I != 4 {
		t.Fatalf("openers %+v: the posting's own control is the one offered", c.Openers)
	}
	dell := page(card(1, 1, false, "Apply Now"), card(2, 1, false, "Apply Now"))
	if c := Categorize(dell, false); len(c.Openers) != 2 {
		t.Fatalf("openers %+v: one card's Apply at its top and bottom is the posting's", c.Openers)
	}
	// IBM draws the posting's summary twice, in its banner and its side rail,
	// each an <article> whose Apply opens the same address.
	link := func(i, n int, href string) Control {
		c := card(i, n, n == 2, "Apply now")
		c.Tag, c.Href = "a", href
		return c
	}
	ibm := page(link(1, 1, "https://careers.example.test/Login?jobId=7"), link(2, 2, "https://careers.example.test/Login?jobId=7"))
	if c := Categorize(ibm, false); len(c.Openers) != 2 {
		t.Fatalf("openers %+v: two cards whose Apply opens one address are the posting's", c.Openers)
	}
	others := page(link(1, 1, "https://careers.example.test/Login?jobId=7"), link(2, 2, "https://careers.example.test/Login?jobId=8"))
	if c := Categorize(others, false); len(c.Openers) != 0 {
		t.Fatalf("openers %+v: cards whose Apply opens different jobs are a list", c.Openers)
	}
	links := page(card(1, 0, true, "Apply now"), ctl(2, "button", -1, "Apply"))
	if c := Categorize(links, false); len(c.Openers) != 2 || c.Openers[0].I != 2 {
		t.Fatalf("openers %+v: a rail control ranks after the posting's own", c.Openers)
	}
	if c := Categorize(page(card(1, 1, true, "1-Click Apply")), false); c.OneClick != "" {
		t.Fatalf("one-click %q: a one-click Apply in the rail is another job's", c.OneClick)
	}
	if c := Categorize(page(ctl(1, "button", -1, "1-Click Apply")), false); c.OneClick == "" {
		t.Fatal("the posting's own one-click Apply is not reported")
	}
}
