package apply

import (
	"context"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
)

type senv struct {
	g    *rig
	tab  *scriptTab
	page *fakePage
	e    *Exec
	s    *session
	wr   *walkResult
}

func formState() *PageState {
	return &PageState{
		URL: "https://careers.example.test/apply", Title: "Apply", Text: "Platform Engineer application", Lines: []string{"Platform Engineer"},
		Forms:    []FormInfo{{Fields: 3}},
		Inputs:   []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "First Name", Form: 0}},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Submit application"}, Form: 0}},
	}
}

func newSubmit(t *testing.T, mod func(*Deps)) *senv {
	t.Helper()
	g := newRig(t, func(d *Deps) {
		d.Page.Timing = WalkTiming{AfterForm: 100 * time.Millisecond, First: 50 * time.Millisecond, Poll: 10 * time.Millisecond, PressWait: 120 * time.Millisecond}
		if mod != nil {
			mod(d)
		}
	})
	form := formState()
	tab := newScriptTab(form)
	tab.page = &fakePage{}
	tab.watch = &fakeWatch{}
	in := Input{URL: pageURL, Submit: true, ExplicitRequest: true, Caller: gates.CallerCLI, Tool: ToolAutofill, Company: "Example Corp", Role: "Platform Engineer"}
	run := &Run{ID: "run_s", StartedAt: t0, Input: in, ATS: ATSGeneric, Tenant: "careers.example.test", Identity: "x"}
	e := &Exec{r: g.r, Run: run, Ctx: context.Background()}
	s := &session{r: g.r, e: e, tab: tab, page: tab.page, last: form}
	s.docs = newDocResumes(g.r, e)
	s.pages = []*pageState{{report: PageReport{URL: form.URL}}}
	return &senv{g: g, tab: tab, page: tab.page, e: e, s: s, wr: &walkResult{End: EndSubmit, Last: form}}
}

func (x *senv) submit() *stop {
	return x.g.r.submitStage(context.Background(), x.e, x.s, x.wr)
}

func thanks() *PageState {
	return &PageState{URL: "https://careers.example.test/apply", Title: "Thanks", Text: "Thank you for applying", Lines: []string{"Thank you for applying", "Your application has been submitted."}}
}

func post(status int, body string) browser.Response {
	return browser.Response{Method: "POST", URL: "https://careers.example.test/apply/write", Status: status, Body: body}
}

func TestApply_Submit_PressOrderGatesThenClaimThenSignal(t *testing.T) {
	x := newSubmit(t, nil)
	var claimedAtClick int
	x.tab.onClick = func(t *scriptTab, c Control) {
		claimedAtClick = x.g.count(`SELECT count(*) FROM submissions WHERE state = 'submitting'`)
		t.watch.add(post(200, `{"ok":true}`))
		t.set(thanks())
	}
	if st := x.submit(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if claimedAtClick != 1 {
		t.Fatal("the press went out before the claim was held")
	}
	rows, err := x.g.st.Read().Query(`SELECT gate FROM gate_decisions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for rows.Next() {
		var g string
		_ = rows.Scan(&g)
		order = append(order, g)
	}
	rows.Close()
	pos := map[string]int{}
	for i, g := range order {
		if _, ok := pos[g]; !ok {
			pos[g] = i
		}
	}
	if !(pos["submit_authorization"] < pos["duplicate_guard"] && pos["duplicate_guard"] < pos["press_claim"] && pos["press_claim"] < pos["control_press"]) {
		t.Fatalf("gate order %v", order)
	}
	if x.e.Run.Outcome.Kind != OutcomeSubmitted || !x.e.Run.Outcome.Confirmed || x.e.Run.Outcome.ConfirmSource != ConfirmATSResponse {
		t.Fatalf("outcome %+v", x.e.Run.Outcome)
	}
}

func TestApply_Submit_LabelClassifiedAtPressTimeTrustedInput(t *testing.T) {
	x := newSubmit(t, nil)
	x.tab.labelsNow = map[int][]string{1: {"Next"}}
	st := x.submit()
	if st == nil || st.class != ClassDefect || !strings.Contains(st.reason, "no submit control was pressed") {
		t.Fatalf("stop %+v", st)
	}
	if len(x.tab.clicks) != 0 {
		t.Fatalf("pressed %v", x.tab.clicks)
	}
	if state, _, _ := ledgerState(x.g); state != "failed" {
		t.Fatalf("ledger %s: a claim with no press must not stand", state)
	}
}

func ledgerState(g *rig) (state string, confirmed bool, source string) {
	var c int
	var src *string
	if err := g.st.Read().QueryRow(`SELECT state, confirmed, confirm_source FROM submissions ORDER BY id DESC LIMIT 1`).Scan(&state, &c, &src); err != nil {
		return "none", false, ""
	}
	if src != nil {
		source = *src
	}
	return state, c == 1, source
}

func TestApply_Submit_CoveredControlIsNotPressedAndTheClaimFails(t *testing.T) {
	x := newSubmit(t, nil)
	x.tab.covered = map[int]bool{1: true}
	st := x.submit()
	if st == nil || !strings.Contains(st.reason, "covered by") || len(x.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %v", st, x.tab.clicks)
	}
	if state, _, _ := ledgerState(x.g); state != "failed" {
		t.Fatalf("ledger %s", state)
	}
}

func TestApply_Submit_UnsolvedVisibleCaptchaStopsAsBotCheck(t *testing.T) {
	x := newSubmit(t, nil)
	form := formState()
	form.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, W: 304, H: 78}}
	form.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
	x.tab.set(form)
	st := x.submit()
	if st == nil || st.class != ClassVerification || st.board != "challenge" || !strings.Contains(st.reason, "bot check") {
		t.Fatalf("stop %+v", st)
	}
	if len(x.tab.clicks) != 0 || x.g.count(`SELECT count(*) FROM submissions`) != 0 {
		t.Fatalf("clicks %v, ledger rows %d: nothing is pressed and no claim row is written", x.tab.clicks, x.g.count(`SELECT count(*) FROM submissions`))
	}
	// An invisible widget does not count.
	form2 := formState()
	form2.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, Invisible: true, W: 1, H: 1}}
	if unsolvedWidget(form2) != "" {
		t.Fatal("an invisible widget read as a challenge")
	}
}

func TestApply_Submit_DryRunNeverPressesAndRecordsNothing(t *testing.T) {
	x := newSubmit(t, nil)
	x.e.Run.Input.Submit = false
	if st := x.submit(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if len(x.tab.clicks) != 0 || x.g.count(`SELECT count(*) FROM submissions`) != 0 {
		t.Fatal("a dry run pressed or wrote a claim")
	}
	if l := stageOf(x.e.Run, StageSubmit); l.Status != StatusSkipped {
		t.Fatalf("submit line %+v", l)
	}
}

func TestApply_Submit_ModelChosenSubmitIsRefusedWithoutAnExplicitRequest(t *testing.T) {
	x := newSubmit(t, nil)
	x.e.Run.Input.ExplicitRequest = false
	st := x.submit()
	if st == nil || len(x.tab.clicks) != 0 || x.g.count(`SELECT count(*) FROM submissions`) != 0 {
		t.Fatalf("stop %+v", st)
	}
	x2 := newSubmit(t, nil)
	x2.e.Run.Input.Caller = gates.CallerTelegram
	if st := x2.submit(); st == nil || len(x2.tab.clicks) != 0 {
		t.Fatalf("a Telegram caller submitted: %+v", st)
	}
}

func TestApply_Submit_PostPressClassificationOrder(t *testing.T) {
	validation := func() *PageState {
		f := formState()
		f.Validation = []string{"Email is invalid"}
		return f
	}
	challenge := func() *PageState {
		f := formState()
		f.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, W: 304, H: 78}}
		f.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
		return f
	}
	for _, c := range []struct {
		name  string
		resp  []browser.Response
		page  *PageState
		kind  string
		class Class
	}{
		{"service refuses and the page names a problem", []browser.Response{post(422, "bad")}, validation(), "refused", ClassDefect},
		{"validation without a refusal", nil, validation(), "validation", ClassDefect},
		{"challenge in place of the confirmation", nil, challenge(), "challenge", ClassVerification},
		{"a failing read is ignored", []browser.Response{{Method: "GET", URL: "https://x.test/badge", Status: 401}}, thanks(), "pressed", ""},
		{"pixels and analytics writes are ignored", []browser.Response{{Method: "POST", URL: "https://x.test/analytics/collect", Status: 500}}, thanks(), "pressed", ""},
		// Node's challenge-rejection tests: Greenhouse's 428 with no code
		// recipient, and a body in a verification wall's words, are a challenge;
		// a 422 that names a field and a 429 are refusals.
		{"428 captcha-failed is a challenge", []browser.Response{post(428, `{"code":"captcha-failed","message":"Oops! We were unable to verify your Captcha response."}`)}, thanks(), "challenge", ClassVerification},
		{"a body that asks for the reCAPTCHA is a challenge", []browser.Response{post(403, "Please complete the reCAPTCHA")}, thanks(), "challenge", ClassVerification},
		{"422 naming a field is a refusal", []browser.Response{post(422, `{"errors":["First name is required"]}`)}, thanks(), "refused", ClassDefect},
		{"429 is a refusal", []browser.Response{post(429, "Too many requests")}, thanks(), "refused", ClassDefect},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newSubmit(t, nil)
			x.tab.onClick = func(t *scriptTab, _ Control) {
				t.watch.add(c.resp...)
				t.set(c.page)
			}
			st := x.submit()
			if c.kind == "pressed" {
				if st != nil || x.e.Run.Outcome.Kind != OutcomeSubmitted {
					t.Fatalf("stop %+v outcome %+v", st, x.e.Run.Outcome)
				}
				return
			}
			if st == nil || st.class != c.class {
				t.Fatalf("stop %+v", st)
			}
			if c.kind == "refused" && !strings.Contains(st.reason, "HTTP ") {
				t.Fatalf("reason %q names no status", st.reason)
			}
			if state, _, _ := ledgerState(x.g); state != "failed" {
				t.Fatalf("ledger %s: a refused press is not a submission", state)
			}
			if len(x.tab.clicks) != 1 {
				t.Fatalf("%d presses", len(x.tab.clicks))
			}
		})
	}
}

func TestApply_Confirm_EvidenceOrderAndObservationAloneIsNotConfirmation(t *testing.T) {
	cases := []struct {
		name    string
		resp    []browser.Response
		page    *PageState
		obs     bool
		receipt bool
		src     string
		conf    bool
	}{
		{"service acceptance", []browser.Response{post(200, "ok")}, formState(), false, false, ConfirmATSResponse, true},
		{"page confirms", nil, thanks(), false, false, ConfirmPage, true},
		{"receipt only", nil, formState(), false, true, ConfirmReceipt, true},
		{"page confirms and a receipt arrives", nil, thanks(), false, true, ConfirmPage, true},
		{"observation only", nil, formState(), true, false, ConfirmObserved, false},
		{"nothing at all", nil, formState(), false, false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rc *fakeReceipts
			x := newSubmit(t, func(d *Deps) {
				if c.receipt {
					rc = &fakeReceipts{r: &gmail.Receipt{MessageID: "m-1", ReceivedAt: t0}}
					d.Page.Receipts = rc
				} else {
					rc = &fakeReceipts{err: context.DeadlineExceeded}
					d.Page.Receipts = rc
				}
			})
			if c.obs {
				x.page.obs = []engine.SubmitObservation{{Kind: "fetch", Method: "POST", Status: 200}}
			}
			x.tab.onClick = func(t *scriptTab, _ Control) {
				t.watch.add(c.resp...)
				t.set(c.page)
			}
			if st := x.submit(); st != nil {
				t.Fatalf("stop %+v", st)
			}
			o := x.e.Run.Outcome
			if o.Kind != OutcomeSubmitted || o.Confirmed != c.conf || o.ConfirmSource != c.src {
				t.Fatalf("outcome %+v", o)
			}
			state, conf, src := ledgerState(x.g)
			if state != "submitted" || conf != c.conf {
				t.Fatalf("ledger %s confirmed=%v source=%q", state, conf, src)
			}
			if src != c.src && c.src != ConfirmObserved {
				t.Fatalf("ledger source %q, want %q", src, c.src)
			}
			var receipt string
			_ = x.g.st.Read().QueryRow(`SELECT COALESCE(receipt_message_id, '') FROM submissions ORDER BY id DESC LIMIT 1`).Scan(&receipt)
			if want := map[bool]string{true: "m-1"}[c.receipt]; receipt != want {
				t.Fatalf("receipt id %q, want %q: a receipt found beside a confirmation is kept", receipt, want)
			}
		})
	}
}

func TestApply_Confirm_LookupFailureNeverTurnsASubmitIntoAnError(t *testing.T) {
	x := newSubmit(t, func(d *Deps) { d.Page.Receipts = &fakeReceipts{err: context.Canceled} })
	x.tab.onClick = func(t *scriptTab, _ Control) { t.set(thanks()) }
	if st := x.submit(); st != nil || x.e.Run.Outcome.ConfirmSource != ConfirmPage {
		t.Fatalf("stop %+v outcome %+v", st, x.e.Run.Outcome)
	}
	if len(x.e.Run.Warnings) == 0 {
		t.Fatal("the failed lookup left no trace")
	}
}

type fakeReceipts struct {
	r   *gmail.Receipt
	err error
}

func (f *fakeReceipts) AwaitReceipt(context.Context, gmail.ReceiptLookup, gmail.AwaitOptions) (*gmail.Receipt, error) {
	return f.r, f.err
}

func TestApply_Submit_ClickLostAfterDispatchLeavesMayHaveSubmittedAndNeverRetries(t *testing.T) {
	x := newSubmit(t, nil)
	x.tab.clickErr = context.DeadlineExceeded
	st := x.submit()
	if st == nil || !strings.Contains(st.reason, "no verdict") {
		t.Fatalf("stop %+v", st)
	}
	if state, _, _ := ledgerState(x.g); state != "may-have-submitted" {
		t.Fatalf("ledger %s: the press may have gone out", state)
	}
	if !x.e.Run.MayHaveSubmitted {
		t.Fatal("run not marked")
	}
	if n := x.tab.clicksOf("Submit application"); n != 1 || x.e.Run.Presses != 1 {
		t.Fatalf("Submit pressed %d times, run counts %d: a lost press is never retried", n, x.e.Run.Presses)
	}
}

func TestApply_Submit_FormStaysFilledAfterRefusalAndTabIsKept(t *testing.T) {
	x := newSubmit(t, func(d *Deps) { d.Page.Keep = &Keeper{} })
	x.tab.onClick = func(t *scriptTab, _ Control) { t.watch.add(post(500, "boom")) }
	st := x.submit()
	if st == nil || !strings.Contains(st.reason, "left open") {
		t.Fatalf("stop %+v", st)
	}
	x.e.Run.Outcome = Outcome{Kind: OutcomeStopped, Class: st.class, Stage: StageSubmit, Reason: st.reason}
	if !humanCanFinish(x.e.Run.Outcome) {
		t.Fatal("a refused submit is for a person to finish")
	}
	x.s.release(context.Background())
	if !x.tab.kept || x.tab.closed || len(x.tab.navs) != 0 {
		t.Fatalf("kept %v closed %v navigations %v: the filled form stays open for the person", x.tab.kept, x.tab.closed, x.tab.navs)
	}
	if humanCanFinish(Outcome{Kind: OutcomeStopped, Class: ClassPosting}) {
		t.Fatal("a closed posting needs no tab")
	}
}

func TestApply_Submit_ChallengeStopNamesDetectionSignal(t *testing.T) {
	widget := func() *PageState {
		f := formState()
		f.Captcha = []CaptchaW{{Kind: "hcaptcha", Visible: true, W: 304, H: 78}}
		f.Resp = []RespField{{Name: "h-captcha-response", Empty: true}}
		return f
	}
	wording := func() *PageState {
		f := formState()
		f.Title = "Just a moment..."
		f.Lines = []string{"Verify you are human by completing the action below"}
		return f
	}
	for _, c := range []struct {
		name string
		page *PageState
		from string
	}{
		{"a visible widget", widget(), "detected from: a visible hcaptcha widget"},
		{"challenge wording", wording(), "detected from: challenge wording: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newSubmit(t, nil)
			x.e.Run.ATS = ATSLever
			x.tab.onClick = func(t *scriptTab, _ Control) { t.set(c.page) }
			st := x.submit()
			if st == nil || st.class != ClassVerification || st.board != "challenge" || !strings.Contains(st.reason, "the page served a verification challenge") || !strings.Contains(st.reason, c.from) {
				t.Fatalf("stop %+v", st)
			}
			// The puzzle is the user's: nothing pressed it again and the filled form stays open.
			if x.tab.clicksOf("Submit application") != 1 || !strings.Contains(st.reason, "the filled form is left open") {
				t.Fatalf("presses %d, reason %q", x.tab.clicksOf("Submit application"), st.reason)
			}
		})
	}
}

func TestApply_Submit_SecurityCodeStopNamesWhichHalfFailed(t *testing.T) {
	refusal := post(428, `{"code":"captcha-failed","security_code_recipient":"a***@example.com"}`)
	// The read half: no mail lookup is configured, so no code was read.
	x := newSubmit(t, nil)
	x.e.Run.ATS = ATSGreenhouse
	x.tab.onClick = func(t *scriptTab, _ Control) { t.watch.add(refusal) }
	st := x.submit()
	if st == nil || !strings.Contains(st.reason, "read the emailed security code and retried once: the code was not read or typed: ") {
		t.Fatalf("stop %+v", st)
	}
	if x.tab.clicksOf("Submit application") != 1 {
		t.Fatalf("%d presses: with no code read there is nothing to retry", x.tab.clicksOf("Submit application"))
	}
}

// finalApplyForm is a form whose own final control reads Apply (type submit).
func finalApplyForm() *PageState {
	st := formState()
	st.Controls = []Control{{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply"}, Form: 0}}
	return st
}

func newFinalApplySubmit(t *testing.T) *senv {
	t.Helper()
	x := newSubmit(t, nil)
	form := finalApplyForm()
	x.tab.set(form)
	x.s.last, x.wr.Last = form, form
	return x
}

// The submit stage treats a form's final Apply exactly like a Submit: the same
// authorization, duplicate guard, claim and press gate, in that order, and one
// press.
func TestApply_Submit_FinalApplyIsPressedOnceThroughTheSubmitGates(t *testing.T) {
	x := newFinalApplySubmit(t)
	var claimedAtClick int
	x.tab.onClick = func(t *scriptTab, c Control) {
		claimedAtClick = x.g.count(`SELECT count(*) FROM submissions WHERE state = 'submitting'`)
		t.watch.add(post(200, `{"ok":true}`))
		t.set(thanks())
	}
	if st := x.submit(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if claimedAtClick != 1 || x.tab.clicksOf("Apply") != 1 || len(x.tab.clicks) != 1 {
		t.Fatalf("claim at click %d, clicks %v", claimedAtClick, x.tab.clicks)
	}
	if n := x.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'allow'`); n != 1 {
		t.Fatalf("%d allowed control presses", n)
	}
	if x.e.Run.Outcome.Kind != OutcomeSubmitted {
		t.Fatalf("outcome %+v", x.e.Run.Outcome)
	}
}

// The press gate reads the control again at press time: an Apply that has
// become anything else is not pressed, and the claim is released.
func TestApply_Submit_FinalApplyThatChangedLabelIsNotPressed(t *testing.T) {
	x := newFinalApplySubmit(t)
	x.tab.labelsNow = map[int][]string{1: {"Continue"}}
	st := x.submit()
	if st == nil || !strings.Contains(st.reason, "no submit control was pressed") || len(x.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %v", st, x.tab.clicks)
	}
	if x.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'deny' AND code = 'submit_label_mismatch'`) != 1 {
		t.Fatal("the refused press was not audited")
	}
}

// An in-form Apply that the probe did not read as the form's final control
// (a plain button, not of type submit) is no Submit for the stage either.
func TestApply_Submit_PlainInFormApplyIsNoSubmit(t *testing.T) {
	x := newSubmit(t, nil)
	form := finalApplyForm()
	form.Controls[0].Type = "button"
	x.tab.set(form)
	x.s.last, x.wr.Last = form, form
	st := x.submit()
	if st == nil || !strings.Contains(st.reason, "no Submit control stands on the form") || len(x.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %v", st, x.tab.clicks)
	}
}

// Node's submit-generic tests (OpenAI on Ashby): a required Yes/No group still
// unanswered on the second read blocks the press; one that recovers on the
// re-read does not.
func TestApply_Submit_RequiredToggleStillUnansweredBlocksThePress(t *testing.T) {
	const q = "Are you authorized to work in the country where the job is located?"
	t.Run("still unanswered on the second read", func(t *testing.T) {
		x := newSubmit(t, nil)
		x.tab.prepress = []PrePress{{Toggles: []string{q}}}
		st := x.submit()
		if st == nil || st.class != ClassAnswerGap || !strings.Contains(st.reason, q) {
			t.Fatalf("stop %+v", st)
		}
		if len(x.tab.clicks) != 0 || x.g.count(`SELECT count(*) FROM submissions`) != 0 {
			t.Fatalf("pressed %v: nothing is claimed or pressed", x.tab.clicks)
		}
	})
	t.Run("recovers on the re-read", func(t *testing.T) {
		x := newSubmit(t, nil)
		x.tab.prepress = []PrePress{{Toggles: []string{q}}, {}}
		x.tab.onClick = func(t *scriptTab, _ Control) {
			t.watch.add(post(200, `{"ok":true}`))
			t.set(thanks())
		}
		if st := x.submit(); st != nil || x.tab.clicksOf("Submit application") != 1 {
			t.Fatalf("stop %+v clicks %v", st, x.tab.clicks)
		}
	})
}
