package apply

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
)

const adpURL = "https://workforcenow.adp.com/mascsr/default/careers/apply"

var fastGate = CodeTiming{Poll: 5 * time.Millisecond, GiveUp: 400 * time.Millisecond, Settle: time.Millisecond, Verify: 300 * time.Millisecond, Close: 40 * time.Millisecond}

// The Codes seam is the in-process lookup of the Gmail stack, not an HTTP hop.
var _ Codes = (*gmail.Lookups)(nil)

func adpBase() *PageState {
	return &PageState{
		URL: adpURL, Title: "Apply", Text: "Guest information", Lines: []string{"Guest information"},
		Forms:    []FormInfo{{Fields: 2}},
		Inputs:   []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "First name", Form: 0}},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Submit application"}, Form: 0}},
	}
}

// withDialog stands a dialog over a page; its buttons and options are also
// controls, as the page's own control list shows them.
func withDialog(base *PageState, d DialogInfo) *PageState {
	st := *base
	st.Controls = append([]Control(nil), base.Controls...)
	st.Dialogs = []DialogInfo{d}
	for _, o := range d.Options {
		st.Controls = append(st.Controls, Control{I: o.I, Role: "radio", Tag: "wfn-radio-button", Labels: []string{o.Label}, Modal: true})
	}
	for _, b := range d.Buttons {
		st.Controls = append(st.Controls, Control{I: b.I, Tag: "sdf-button", Labels: []string{b.Label}, Disabled: b.Disabled, Modal: true})
	}
	return &st
}

func deliveryDialog() DialogInfo {
	return DialogInfo{
		Heading: "Verification code", Text: "Where would you like to receive a verification code?",
		Options: []DialogOption{{I: 20, Value: "PHONE", Label: "Phone"}, {I: 21, Value: "EMAIL", Label: "EMAIL"}},
		Buttons: []DialogButton{{I: 22, Label: "Send"}},
	}
}

func entryDialog(verifyDisabled bool) DialogInfo {
	return DialogInfo{
		Heading: "Enter the Verification Code", Text: "Verification Code sent to your email address",
		Boxes:   []DialogBox{{I: 30}},
		Buttons: []DialogButton{{I: 31, Label: "Verify", Disabled: verifyDisabled}, {I: 32, Label: "Resend Code"}},
	}
}

func setVerifyEnabled(t *scriptTab) {
	for i := range t.state.Controls {
		if t.state.Controls[i].I == 31 {
			t.state.Controls[i].Disabled = false
		}
	}
	for d := range t.state.Dialogs {
		for b := range t.state.Dialogs[d].Buttons {
			if t.state.Dialogs[d].Buttons[b].I == 31 {
				t.state.Dialogs[d].Buttons[b].Disabled = false
			}
		}
	}
}

type adpRun struct {
	t      *testing.T
	w      *wenv
	fc     *fakeCodes
	res    *walkResult
	stop   *stop
	waits  int
	sent   time.Time
	resent time.Time
}

// runADP walks an ADP-like application: the delivery choice stands first, the
// code pane follows the Send with Verify disabled until it has been looked at
// several times, and Verify closes the pane. rejectFirst leaves the pane
// standing after the first Verify.
func runADP(t *testing.T, replies []codeReply, rejectFirst bool) *adpRun {
	t.Helper()
	rejects := 0
	if rejectFirst {
		rejects = 1
	}
	return runADPWith(t, replies, rejects, entryDialog)
}

// runADPWith leaves the pane standing after the first `rejects` Verify
// presses and draws the pane with entry.
func runADPWith(t *testing.T, replies []codeReply, rejects int, entry func(bool) DialogInfo) *adpRun {
	t.Helper()
	return runADPIn(t, replies, rejects, entry, nil)
}

// runADPIn is runADPWith with a hook that prepares the walk (a demo workspace,
// a persona address) before it runs.
func runADPIn(t *testing.T, replies []codeReply, rejects int, entry func(bool) DialogInfo, setup func(*adpRun)) *adpRun {
	t.Helper()
	x := &adpRun{t: t, fc: &fakeCodes{replies: replies}}
	x.w = newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, withDialog(adpBase(), deliveryDialog()))
	x.w.g.r.d.Page.Codes, x.w.g.r.d.Page.Code = x.fc, fastGate
	if setup != nil {
		setup(x)
	}
	verifies, typed := 0, false
	x.w.tab.onKeys = func(*scriptTab, int, string) { typed = true }
	x.w.tab.probeHook = func(tb *scriptTab) {
		if !typed || len(tb.state.Dialogs) == 0 || len(tb.state.Dialogs[0].Buttons) < 1 || !tb.state.Dialogs[0].Buttons[0].Disabled {
			return
		}
		if x.waits++; x.waits >= 4 {
			setVerifyEnabled(tb)
		}
	}
	x.w.tab.onClick = func(tb *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Send":
			x.sent = x.w.g.now
			x.w.g.now = x.w.g.now.Add(time.Minute)
			tb.set(withDialog(adpBase(), entry(true)))
		case "Resend Code":
			x.resent = x.w.g.now
			x.w.g.now = x.w.g.now.Add(time.Minute)
		case "Verify":
			if c.Disabled {
				t.Error("a disabled Verify was clicked")
			}
			verifies++
			if verifies <= rejects {
				typed = false
				tb.set(withDialog(adpBase(), entry(false)))
				return
			}
			tb.set(adpBase())
		}
	}
	x.res, x.stop = x.w.walk()
	return x
}

func TestApply_Walk_CodeEntryModalExtendsStallGrace(t *testing.T) {
	waits := func(n int, mail codeReply) []codeReply {
		out := make([]codeReply, 0, n+1)
		for i := 0; i < n; i++ {
			out = append(out, notYet())
		}
		return append(out, mail)
	}
	adpMail := mailed("482916", mailFrom("SecurityServices_NoReply", "example.com"), "Your verification code", t0.Add(30*time.Second))
	page := func(d *DialogInfo) *PageState {
		st := wizardStep(1, 2)
		if d == nil {
			return st
		}
		return withDialog(st, *d)
	}
	run := func(t *testing.T, d DialogInfo, codes Codes, late bool) (*walkResult, *stop, *scriptTab) {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, page(nil))
		w.g.r.d.Page.Codes, w.g.r.d.Page.Code = codes, fastGate
		var clicked time.Time
		w.tab.onClick = func(tb *scriptTab, c Control) {
			switch c.Labels[0] {
			case "Next":
				clicked = time.Now()
				if !late {
					tb.set(page(&d))
				}
			case "Verify":
				tb.set(wizardStep(2, 2))
			}
		}
		if late {
			// The pane is drawn only after the walk's own press wait is over.
			w.tab.probeHook = func(tb *scriptTab) {
				if !clicked.IsZero() && time.Since(clicked) > 150*time.Millisecond && len(tb.state.Dialogs) == 0 && tb.state.Step.Current == 1 {
					tb.state = page(&d)
				}
			}
		}
		res, st := w.walk()
		return res, st, w.tab
	}
	for name, late := range map[string]bool{"pane drawn at once": false, "pane drawn after the grace": true} {
		t.Run(name, func(t *testing.T) {
			fc := &fakeCodes{replies: waits(30, adpMail)}
			res, st, tab := run(t, entryDialog(false), fc, late)
			if st != nil || res.End != EndSubmit {
				t.Fatalf("end %+v stop %+v: the code window outlasts the walk's stall grace", res, st)
			}
			if tab.keyed[30] != "482916" || len(fc.since) < 30 {
				t.Fatalf("typed %v after %d lookups", tab.keyed, len(fc.since))
			}
		})
	}
	other := entryDialog(false)
	other.Heading, other.Text = "Referral", "Enter your referral's name"
	withPassword := entryDialog(false)
	withPassword.Blocked = true
	for name, c := range map[string]struct {
		d     DialogInfo
		codes Codes
	}{
		"a dialog that does not ask for a code": {other, &fakeCodes{replies: []codeReply{adpMail}}},
		"a dialog that asks for a password":     {withPassword, &fakeCodes{replies: []codeReply{adpMail}}},
	} {
		t.Run("stall unchanged: "+name, func(t *testing.T) {
			res, st, tab := run(t, c.d, c.codes, false)
			// With the pane over it the page offers no control the walk may press,
			// so it ends where it always did: no code is read and none is typed.
			if st != nil || (res.End != EndStalled && res.End != EndNoNavigation) || len(tab.keyed) != 0 {
				t.Fatalf("end %+v stop %+v typed %v", res, st, tab.keyed)
			}
		})
	}
}

// ADP takes the Continue button away while it mails the code and draws the
// pane seconds later: the page after the press shows the same form with
// nothing to press and no dialog yet. The walk waits for the pane there
// instead of ending as no-navigation; with no mail lane it ends as a
// verification stop, never a defect.
func TestApply_Walk_CodePaneDrawnAfterTheContinueWentAwayIsWaitedFor(t *testing.T) {
	adpMail := mailed("482916", mailFrom("SecurityServices_NoReply", "example.com"), "Your verification code", t0.Add(30*time.Second))
	bare := func() *PageState {
		st := wizardStep(1, 2)
		st.Controls = nil
		return st
	}
	var nextAt time.Time
	run := func(t *testing.T, codes Codes) (*walkResult, *stop, *scriptTab) {
		w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
		w.g.r.d.Page.Codes, w.g.r.d.Page.Code = codes, fastGate
		w.g.r.d.Page.Timing.PressWait = 400 * time.Millisecond
		var clicked time.Time
		w.tab.onClick = func(tb *scriptTab, c Control) {
			switch c.Labels[0] {
			case "Next":
				clicked, nextAt = time.Now(), w.g.r.d.Now()
				tb.set(bare())
			case "Verify":
				tb.set(wizardStep(2, 2))
			}
		}
		w.tab.onKeys = func(tb *scriptTab, _ int, _ string) { setVerifyEnabled(tb) }
		w.tab.probeHook = func(tb *scriptTab) {
			if !clicked.IsZero() && time.Since(clicked) > 600*time.Millisecond && len(tb.state.Dialogs) == 0 && tb.state.Step.Current == 1 {
				tb.state = withDialog(bare(), liveADPPane())
			}
		}
		res, st := w.walk()
		return res, st, w.tab
	}
	t.Run("with a mail lane the code is typed and Verify pressed", func(t *testing.T) {
		fc := &fakeCodes{replies: []codeReply{adpMail}}
		res, st, tab := run(t, fc)
		if st != nil || res.End != EndSubmit || tab.keyed[30] != "482916" || tab.clicksOf("Verify") != 1 {
			t.Fatalf("end %+v stop %+v typed %v verifies %d", res, st, tab.keyed, tab.clicksOf("Verify"))
		}
		// The pane's code was mailed after the walk's own Next: an earlier mail
		// is another attempt's, and the site refuses its code (AmEx).
		if len(fc.since) == 0 || fc.since[0].Before(CodeAnchor(nextAt).Add(-100*time.Millisecond)) {
			t.Fatalf("lookups since %v, want none before the Next press %s", fc.since, nextAt)
		}
	})
	t.Run("without a mail lane the walk stops as a verification stop", func(t *testing.T) {
		res, st, tab := run(t, nil)
		if res != nil || st == nil || st.class != ClassVerification || len(tab.keyed) != 0 {
			t.Fatalf("end %+v stop %+v typed %v", res, st, tab.keyed)
		}
	})
}

func TestApply_Walk_DeliveryChoiceDialogChoosesEmailOnly(t *testing.T) {
	mail := mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(30*time.Second))
	x := runADP(t, []codeReply{mail}, false)
	if x.stop != nil || x.res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", x.res, x.stop)
	}
	got := strings.Join(x.w.tab.clicks, ",")
	if !strings.HasPrefix(got, "EMAIL,Send,") || strings.Contains(got, "Phone") {
		t.Fatalf("clicks %s: EMAIL is chosen, then Send, and the phone choice is never taken", got)
	}
	if n := x.w.g.audits(gates.GateCodeRequest); n != 1 {
		t.Fatalf("%d code-request decisions, want one", n)
	}
	if !x.fc.since[0].Equal(CodeAnchor(x.sent)) {
		t.Fatalf("lookup since %s, want one second before the Send press %s", x.fc.since[0], x.sent)
	}

	phoneOnly := deliveryDialog()
	phoneOnly.Options = phoneOnly.Options[:1]
	noSend := deliveryDialog()
	noSend.Buttons = nil
	notCode := deliveryDialog()
	notCode.Heading, notCode.Text = "Contact preference", "How may we reach you?"
	asksPassword := deliveryDialog()
	asksPassword.Blocked = true
	for name, d := range map[string]DialogInfo{"phone only": phoneOnly, "no Send button": noSend, "not about a code": notCode, "asks for a password or a signature": asksPassword} {
		if _, ok := classifyDelivery([]DialogInfo{d}); ok {
			t.Errorf("%s: read as the delivery choice", name)
		}
	}

	// One walk, one Send: the same dialog again is not answered a second time.
	w := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, withDialog(adpBase(), deliveryDialog()))
	w.g.r.d.Page.Codes, w.g.r.d.Page.Code = &fakeCodes{replies: []codeReply{mail}}, fastGate
	st := w.tab.state
	for i, want := range []bool{true, false} {
		if acted, s := newWalker(w).deliveryGate(context.Background(), st); s != nil || acted != want {
			t.Fatalf("delivery gate call %d: acted %v stop %+v", i+1, acted, s)
		}
	}
	if n := w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = ? AND code = ?`, string(gates.GateCodeRequest), gates.CodeCodeTabOnce); n != 1 {
		t.Fatalf("%d once-per-tab refusals audited: the code-request gate decides the second Send", n)
	}
	// The quota: two requests on the host in a run end it.
	q := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, withDialog(adpBase(), deliveryDialog()))
	q.g.r.d.Page.Codes = &fakeCodes{replies: []codeReply{mail}}
	q.e.codeRequests = map[string]int{"workforcenow.adp.com": 2}
	if acted, s := newWalker(q).deliveryGate(context.Background(), q.tab.state); s != nil || acted || len(q.tab.clicks) != 0 {
		t.Fatalf("acted %v stop %+v clicks %v: a third request on the host is refused", acted, s, q.tab.clicks)
	}
}

func TestApply_ADP_CodeTypedKeyByKeyVerifyNeverClickedDisabled(t *testing.T) {
	code := "482916"
	mail := func(at time.Duration, c string) codeReply {
		return mailed(c, mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(at))
	}
	t.Run("typed key by key, Verify waited for", func(t *testing.T) {
		x := runADP(t, []codeReply{notYet(), mail(30*time.Second, code)}, false)
		if x.stop != nil || x.res.End != EndSubmit {
			t.Fatalf("end %+v stop %+v", x.res, x.stop)
		}
		if x.w.tab.keyed[30] != code || len(x.w.tab.typed) != 0 {
			t.Fatalf("keyed %v typed %v: the pane takes the code with key events, not as plain text", x.w.tab.keyed, x.w.tab.typed)
		}
		if x.waits < 4 || x.w.tab.clicksOf("Verify") != 1 {
			t.Fatalf("%d looks while Verify was disabled, %d Verify clicks", x.waits, x.w.tab.clicksOf("Verify"))
		}
		for _, d := range x.fc.domain {
			if d != SenderADP {
				t.Fatalf("lookup domains %v: ADP mail only", x.fc.domain)
			}
		}
	})
	t.Run("a pane still standing is resent once and reads only newer mail", func(t *testing.T) {
		x := runADP(t, []codeReply{mail(30*time.Second, "111111"), mail(150*time.Second, "222222")}, true)
		if x.stop != nil || x.res.End != EndSubmit {
			t.Fatalf("end %+v stop %+v", x.res, x.stop)
		}
		if n := x.w.tab.clicksOf("Resend Code"); n != 1 || x.w.tab.clicksOf("Verify") != 2 || x.w.tab.keyed[30] != "222222" {
			t.Fatalf("resend %d, verify %d, keyed %v", n, x.w.tab.clicksOf("Verify"), x.w.tab.keyed)
		}
		if want := CodeAnchor(x.resent); !x.fc.since[len(x.fc.since)-1].Equal(want) {
			t.Fatalf("the lookup after the resend asked since %s, want %s", x.fc.since[len(x.fc.since)-1], want)
		}
		if n := x.w.g.audits(gates.GateCodeRequest); n != 2 {
			t.Fatalf("%d code-request decisions: the Send and the Resend", n)
		}
	})
}

func TestApply_CodeLane_InProcessLookupNeverOnURL(t *testing.T) {
	code := "482916"
	x := runADP(t, []codeReply{mailed(code, mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(30*time.Second))}, false)
	if x.stop != nil || x.res.End != EndSubmit || len(x.fc.since) == 0 {
		t.Fatalf("end %+v stop %+v lookups %d: the lookup is a call on the Codes seam", x.res, x.stop, len(x.fc.since))
	}
	if len(x.w.tab.navs) != 0 {
		t.Fatalf("navigations %v: the lookup travels by no page address", x.w.tab.navs)
	}
	record, err := json.Marshal(x.w.e.Run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record), code) {
		t.Fatal("the code reached the run record")
	}
	if n := x.w.g.count(`SELECT count(*) FROM gate_decisions WHERE target_path LIKE ?`, "%"+code+"%"); n != 0 {
		t.Fatalf("%d audit rows carry the code in an address", n)
	}
}

func (t *scriptTab) clicksOf(label string) int {
	n := 0
	for _, c := range t.clicks {
		if c == label {
			n++
		}
	}
	return n
}

// The pane ADP drew in the live dry run, as the probe reads it: the heading
// from the element's attribute, the text of the whole pane, one box labelled
// like the heading, Verify disabled and no Resend control while the countdown
// runs.
func liveADPPane() DialogInfo {
	return DialogInfo{
		Heading: "Enter the Verification Code",
		Text:    "Verification Code sent to your email address If you don't see the code in your inbox, please check your other folders like Spam, Social, Promotions, etc. Verify Did not receive code? Resend code in 59 seconds. Powered by ADP",
		Boxes:   []DialogBox{{I: 30, Label: "Enter the Verification Code", MaxLength: 6}},
		Buttons: []DialogButton{{I: 31, Label: "Verify", Disabled: true}},
	}
}

func TestApply_CodeEntry_RecognisesTheADPPaneAndOnlyACodePane(t *testing.T) {
	ce, ok := classifyCodeEntry([]DialogInfo{liveADPPane()})
	if !ok || !ce.hasVerify || ce.verify.I != 31 || ce.resend != nil || len(ce.boxes) != 1 {
		t.Fatalf("the live ADP pane read as %+v ok=%v", ce, ok)
	}
	referral := entryDialog(false)
	referral.Heading, referral.Text = "Referral", "Enter your referral's name"
	cookie := DialogInfo{Heading: "Cookie preferences", Text: "We use cookies to improve your experience", Boxes: []DialogBox{{I: 40, Label: "Search"}}, Buttons: []DialogButton{{I: 41, Label: "Save"}}}
	twoBoxes := liveADPPane()
	twoBoxes.Boxes = append(twoBoxes.Boxes, DialogBox{I: 32, Label: "Name"})
	createCode := liveADPPane()
	createCode.Heading, createCode.Text = "Create a new passcode", "Create a new passcode for your account"
	password := liveADPPane()
	password.Blocked = true
	for name, d := range map[string]DialogInfo{"a referral modal": referral, "a cookie modal": cookie, "two boxes": twoBoxes, "a passcode to create": createCode, "a password beside the box": password} {
		if _, ok := classifyCodeEntry([]DialogInfo{d}); ok {
			t.Errorf("%s read as a code pane", name)
		}
	}
	// Oracle's resend is "Send New Code".
	oracle := entryDialog(false)
	oracle.Buttons[1].Label = "Send New Code"
	if ce, ok := classifyCodeEntry([]DialogInfo{oracle}); !ok || ce.resend == nil || ce.resend.I != 32 {
		t.Fatalf("an Oracle pane read as %+v ok=%v", ce, ok)
	}
	// A pane whose only confirm control reads as Submit is still a code pane,
	// without a Verify control the run may press.
	submitOnly := liveADPPane()
	submitOnly.Buttons = []DialogButton{{I: 31, Label: "Submit code"}}
	if ce, ok := classifyCodeEntry([]DialogInfo{submitOnly}); !ok || ce.hasVerify {
		t.Fatalf("a Submit-code pane read as %+v ok=%v", ce, ok)
	}
}

func TestApply_Walk_CodePaneWithoutMailEndsAsAVerificationStop(t *testing.T) {
	page := func(d DialogInfo) *PageState {
		base := adpBase()
		base.Controls = []Control{{I: 1, Tag: "button", Labels: []string{"Next"}, Form: 0}}
		return withDialog(base, d)
	}
	mail := mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(30*time.Second))
	for name, c := range map[string]struct {
		demo  bool
		codes *fakeCodes
		tail  string
	}{
		"no mail lane":       {false, nil, "this run cannot read mail (no mail lane connected)"},
		"the demo workspace": {true, &fakeCodes{replies: []codeReply{mail}}, "this run cannot read mail (demo workspace: reading an emailed verification code is refused: no Gmail account is connected)"},
	} {
		for pane, d := range map[string]DialogInfo{"code entry": liveADPPane(), "delivery choice": deliveryDialog()} {
			t.Run(name+": "+pane, func(t *testing.T) {
				w := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, page(d))
				if c.demo {
					markDemo(t, w.g.st)
				}
				if c.codes != nil {
					w.g.r.d.Page.Codes = c.codes
				}
				w.g.r.d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
					return json.RawMessage(`{"candidate":{"email":"alex@example.com"}}`), nil
				}
				res, st := w.walk()
				if res != nil || st == nil || st.class != ClassVerification || st.board != "one-time-code" || st.at != StageFill {
					t.Fatalf("end %+v stop %+v, want a verification stop", res, st)
				}
				want := "the site emailed a verification code to a***@example.com; " + c.tail
				if pane == "delivery choice" {
					want = "the site offers to email a verification code; " + c.tail
				}
				if st.reason != want {
					t.Fatalf("reason %q, want %q", st.reason, want)
				}
				if strings.Contains(st.reason, "alex@") {
					t.Fatalf("reason %q carries the whole address", st.reason)
				}
				if len(w.tab.clicks) != 0 || len(w.tab.keyed) != 0 || len(w.tab.typed) != 0 {
					t.Fatalf("clicks %v keyed %v typed %v: nothing is pressed or typed", w.tab.clicks, w.tab.keyed, w.tab.typed)
				}
				if c.codes != nil && len(c.codes.since) != 0 {
					t.Fatalf("%d mail lookups in a run that cannot read mail", len(c.codes.since))
				}
				if n := w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'deny' AND code = 'demo_workspace'`); c.demo != (n == 1) {
					t.Fatalf("%d demo-gate refusals recorded in demo=%v", n, c.demo)
				}
				if humanCanFinish(Outcome{Kind: OutcomeStopped, Class: st.class, Reason: st.reason}) != true {
					t.Fatal("the tab is not kept for the person")
				}
			})
		}
	}
	if OwnerOf(Outcome{Kind: OutcomeStopped, Class: ClassVerification}) != OwnerUser {
		t.Fatal("a verification stop is the user's")
	}
}

func TestApply_Walk_CodePaneNoMailInTheBoundEndsTheWalk(t *testing.T) {
	w := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, withDialog(adpBase(), liveADPPane()))
	fc := &fakeCodes{replies: []codeReply{notYet()}}
	w.g.r.d.Page.Codes, w.g.r.d.Page.Code = fc, CodeTiming{Poll: 5 * time.Millisecond, GiveUp: 60 * time.Millisecond}
	res, st := w.walk()
	if res != nil || st == nil || st.class != ClassVerification || !strings.Contains(st.reason, "no verification code from adp.com arrived within 60ms") {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(fc.since) == 0 || len(w.tab.keyed) != 0 || len(w.tab.clicks) != 0 {
		t.Fatalf("lookups %d keyed %v clicks %v: the bound lookup ran and nothing was typed or pressed", len(fc.since), w.tab.keyed, w.tab.clicks)
	}
	for _, d := range fc.domain {
		if d != SenderADP {
			t.Fatalf("lookup domains %v: ADP mail only", fc.domain)
		}
	}
	// A failed lookup is not "no code yet": the reason is the lookup's own.
	w2 := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, withDialog(adpBase(), liveADPPane()))
	w2.g.r.d.Page.Codes = &fakeCodes{replies: []codeReply{{err: errors.New("the mail authorization expired")}}}
	w2.g.r.d.Page.Code = fastGate
	if _, st := w2.walk(); st == nil || st.class != ClassVerification || !strings.Contains(st.reason, "the mail authorization expired") {
		t.Fatalf("stop %+v", st)
	}
}

func TestApply_Walk_CodePaneOneResendThenTheWalkEnds(t *testing.T) {
	adpMail := func(at time.Duration, c string) codeReply {
		return mailed(c, mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(at))
	}
	t.Run("a second pane that still stands ends the walk", func(t *testing.T) {
		x := runADPWith(t, []codeReply{adpMail(30*time.Second, "111111"), adpMail(150*time.Second, "222222"), adpMail(300*time.Second, "333333")}, 2, entryDialog)
		if x.res != nil || x.stop == nil || x.stop.class != ClassVerification || !strings.Contains(x.stop.reason, "still stands after 2 codes (one Resend)") {
			t.Fatalf("end %+v stop %+v", x.res, x.stop)
		}
		if n, v := x.w.tab.clicksOf("Resend Code"), x.w.tab.clicksOf("Verify"); n != 1 || v != 2 {
			t.Fatalf("resend %d verify %d, want one Resend and two attempts", n, v)
		}
		if len(x.fc.since) < 2 || x.w.tab.keyed[30] != "222222" {
			t.Fatalf("lookups %d keyed %v: a third code is never read", len(x.fc.since), x.w.tab.keyed)
		}
	})
	t.Run("a Resend that never appears ends the walk", func(t *testing.T) {
		noResend := func(disabled bool) DialogInfo {
			d := entryDialog(disabled)
			d.Buttons = d.Buttons[:1]
			return d
		}
		fastGate.Resend = 100 * time.Millisecond
		defer func() { fastGate.Resend = 0 }()
		x := runADPWith(t, []codeReply{adpMail(30*time.Second, "111111"), adpMail(150*time.Second, "222222")}, 1, noResend)
		if x.res != nil || x.stop == nil || x.stop.class != ClassVerification || !strings.Contains(x.stop.reason, "Resend control did not become available within 100ms") {
			t.Fatalf("end %+v stop %+v", x.res, x.stop)
		}
		if x.w.tab.clicksOf("Resend Code") != 0 || len(x.fc.since) != 1 {
			t.Fatalf("resend clicks %d lookups %d: the second code is not read without a Resend", x.w.tab.clicksOf("Resend Code"), len(x.fc.since))
		}
	})
	t.Run("a pane that closes while its Resend is awaited is read again, with no wait for a mail nobody asked for", func(t *testing.T) {
		var run *adpRun
		closing := func(disabled bool) DialogInfo {
			d := entryDialog(disabled)
			if !disabled {
				// The pane drawn again after the first Verify has no Resend yet and
				// closes by itself once the walk has met it again (the close wait is
				// one 250 ms poll) and waits for its Resend.
				d.Buttons = d.Buttons[:1]
				go func() {
					time.Sleep(400 * time.Millisecond)
					run.w.tab.set(adpBase())
				}()
			}
			return d
		}
		fastGate.Resend = 5 * time.Second
		defer func() { fastGate.Resend = 0 }()
		x := runADPIn(t, []codeReply{adpMail(30*time.Second, "111111"), adpMail(150*time.Second, "222222")}, 1, closing, func(x *adpRun) { run = x })
		if x.stop != nil || len(x.fc.since) != 1 || x.w.tab.keyed[30] != "111111" {
			t.Fatalf("stop %+v lookups %d keyed %v: no Resend was pressed, so no second code is looked up or typed", x.stop, len(x.fc.since), x.w.tab.keyed)
		}
	})
}

// afterResend mails its code only once the pane's Resend was pressed.
type afterResend struct {
	x     *adpRun
	since []time.Time
}

func (a *afterResend) LatestCode(_ context.Context, since time.Time, _ string) (gmail.OTPResult, error) {
	a.since = append(a.since, since)
	if a.x.resent.IsZero() {
		return gmail.OTPResult{}, notYet().err
	}
	return mailed("222222", mailFrom("no-reply", "adp.com"), "Your verification code", a.x.resent.Add(30*time.Second)).res, nil
}

// Stanford (Oracle, live): no code mail came in the window, and the pane said
// "You can send a new code now". Its own Resend is pressed once and only mail
// after the press is read.
func TestApply_Walk_NoCodeInTheWindowPressesThePanesResendOnce(t *testing.T) {
	var codes *afterResend
	x := runADPIn(t, nil, 0, entryDialog, func(x *adpRun) {
		codes = &afterResend{x: x}
		x.w.g.r.d.Page.Codes = codes
	})
	if x.stop != nil || x.w.tab.clicksOf("Resend Code") != 1 || x.w.tab.keyed[30] != "222222" {
		t.Fatalf("stop %+v resend %d keyed %v", x.stop, x.w.tab.clicksOf("Resend Code"), x.w.tab.keyed)
	}
	if last := codes.since[len(codes.since)-1]; !last.Equal(CodeAnchor(x.resent)) {
		t.Fatalf("the lookup after the Resend asked since %s, want %s", last, CodeAnchor(x.resent))
	}
}

func TestApply_Walk_ASubmitButtonOnTheCodePaneIsNeverTheApplicationSubmit(t *testing.T) {
	// The pane's own confirm control reads as Submit: it is neither the form's
	// Submit nor pressed.
	submitOnly := liveADPPane()
	submitOnly.Buttons = []DialogButton{{I: 31, Label: "Submit code"}}
	st := withDialog(adpBase(), submitOnly)
	if c := Categorize(st, true); len(c.Submit) != 0 {
		t.Fatalf("Submit controls %+v: a code pane's Submit is not the form's", c.Submit)
	}
	for name, mailLane := range map[string]bool{"no mail lane": false, "a mail lane": true} {
		t.Run(name, func(t *testing.T) {
			w := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}, st)
			if mailLane {
				w.g.r.d.Page.Codes = &fakeCodes{replies: []codeReply{mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(30*time.Second))}}
				w.g.r.d.Page.Code = fastGate
			}
			res, stp := w.walk()
			if res != nil || stp == nil || stp.class != ClassVerification {
				t.Fatalf("end %+v stop %+v", res, stp)
			}
			if len(w.tab.clicks) != 0 || len(w.tab.keyed) != 0 || w.e.Run.Pressed {
				t.Fatalf("clicks %v keyed %v pressed %v: nothing on the pane is pressed", w.tab.clicks, w.tab.keyed, w.e.Run.Pressed)
			}
		})
	}
	// Beside a Verify control, a Submit-labelled one on the pane is left alone
	// and Verify is the control pressed.
	x := runADPWith(t, []codeReply{mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(30*time.Second))}, 0, func(d bool) DialogInfo {
		b := entryDialog(d)
		b.Buttons = append(b.Buttons, DialogButton{I: 33, Label: "Submit application"})
		return b
	})
	if x.stop != nil || x.res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", x.res, x.stop)
	}
	if x.w.tab.clicksOf("Submit application") != 0 || x.w.tab.clicksOf("Verify") != 1 {
		t.Fatalf("clicks %v: only Verify is pressed on the pane", x.w.tab.clicks)
	}
}

// Fixtures of the demo's mail opening: the persona's address is a plus alias
// of the account the lane reads. Both sit on a reserved domain; the gate
// compares addresses, not providers.
var (
	demoAccount = addr("owner", "example.test")
	demoAlias   = addr("owner+applysmithdemo", "example.test")
)

func addr(local, domain string) string { return local + "@" + domain }

func personaIs(email string) func(context.Context) (json.RawMessage, error) {
	return func(context.Context) (json.RawMessage, error) {
		return json.RawMessage(`{"candidate":{"email":"` + email + `"}}`), nil
	}
}

// demoSetup makes the walk a demo workspace whose lane reads account and whose
// persona applies with persona.
func demoSetup(account, persona string) func(*adpRun) {
	return func(x *adpRun) {
		markDemo(x.t, x.w.g.st)
		x.fc.account = account
		x.w.g.r.d.Page.Applicant = personaIs(persona)
	}
}

func demoAllows(t *testing.T, x *adpRun) int {
	return x.w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'allow' AND reason LIKE '%verification code%' AND run_id = '` + x.w.e.Run.ID + `'`)
}

// Scenario "A demo reads one emailed code for a plus alias of the connected
// account": the code lane of a demo workspace consults the gate, and on an
// allow the bounded lookup runs, the code is typed and Verify is pressed.
func TestApply_Walk_DemoCodeIsReadForAPlusAliasOfTheConnectedAccount(t *testing.T) {
	mail := func(at time.Duration, c string) codeReply {
		return mailed(c, mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(at))
	}
	x := runADPIn(t, []codeReply{mail(30*time.Second, "482916")}, 0, entryDialog, demoSetup(demoAccount, demoAlias))
	if x.stop != nil || x.res == nil || x.res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v: the demo walked past the code dialog", x.res, x.stop)
	}
	if x.w.tab.keyed[30] != "482916" || x.w.tab.clicksOf("Verify") != 1 || x.w.tab.clicksOf("Send") != 1 {
		t.Fatalf("keyed %v verify %d send %d", x.w.tab.keyed, x.w.tab.clicksOf("Verify"), x.w.tab.clicksOf("Send"))
	}
	if len(x.fc.since) != 1 || !x.fc.since[0].Equal(CodeAnchor(x.sent)) || x.fc.domain[0] != SenderADP {
		t.Fatalf("lookups since %v domains %v: one lookup, from the Send press, from adp.com", x.fc.since, x.fc.domain)
	}
	if want := SenderADP + "|" + CodeAnchor(x.sent).UTC().Format(time.RFC3339Nano); x.fc.grants[0] != want {
		t.Fatalf("the lookup carried the grant %q, want %q", x.fc.grants[0], want)
	}
	if n := demoAllows(t, x); n != 1 {
		t.Fatalf("%d audited allows, want one", n)
	}
	if n := x.w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'deny'`); n != 0 {
		t.Fatalf("%d refusals in an allowed lookup", n)
	}
	if n := x.w.g.count(`SELECT count(*) FROM gate_decisions WHERE reason LIKE '%482916%'`); n != 0 {
		t.Fatal("the code reached the audit rows")
	}
}

// Every condition of the gate ends the walk on the dialog with nothing read,
// nothing typed and nothing pressed, and records the refusal with its reason.
func TestApply_Walk_DemoCodeLookupIsRefusedWhenAnyConditionFails(t *testing.T) {
	mail := mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(30*time.Second))
	for name, c := range map[string]struct {
		setup func(*adpRun)
		why   string
	}{
		"a persona address that is not an alias": {demoSetup(demoAccount, addr("alex", "example.com")), "not a plus-address alias"},
		"an alias of another account":            {demoSetup(demoAccount, addr("someone+applysmithdemo", "example.test")), "not an alias of the connected"},
		"an alias on another domain":             {demoSetup(demoAccount, addr("owner+applysmithdemo", "example.com")), "not an alias of the connected"},
		"no Gmail connected":                     {demoSetup("", demoAlias), "no Gmail account is connected"},
		"no persona address":                     {func(x *adpRun) { demoSetup(demoAccount, demoAlias)(x); x.w.g.r.d.Page.Applicant = nil }, "not a plus-address alias"},
		"a lane that does not know its mailbox":  {func(x *adpRun) { demoSetup(demoAccount, demoAlias)(x); x.w.g.r.d.Page.Codes = codesOnly{x.fc} }, "no Gmail account is connected"},
		"a run that asks to submit":              {func(x *adpRun) { demoSetup(demoAccount, demoAlias)(x); x.w.e.Run.Input.Submit = true }, "not a dry run"},
		"a site whose sender is not known": {func(x *adpRun) {
			demoSetup(demoAccount, demoAlias)(x)
			x.w.e.Run.Input.URL = "https://careers.example.test/apply"
		}, "sender domain"},
	} {
		t.Run(name, func(t *testing.T) {
			x := runADPIn(t, []codeReply{mail}, 0, entryDialog, c.setup)
			if x.res != nil || x.stop == nil || x.stop.class != ClassVerification || x.stop.board != "one-time-code" {
				t.Fatalf("end %+v stop %+v, want a verification stop", x.res, x.stop)
			}
			if !strings.Contains(x.stop.reason, "this run cannot read mail (demo workspace: ") || !strings.Contains(x.stop.reason, c.why) {
				t.Fatalf("reason %q, want the demo refusal naming %q", x.stop.reason, c.why)
			}
			if len(x.fc.since) != 0 || len(x.w.tab.keyed) != 0 || len(x.w.tab.typed) != 0 || x.w.tab.clicksOf("Verify") != 0 {
				t.Fatalf("lookups %d keyed %v typed %v verify %d: nothing is read, typed or pressed", len(x.fc.since), x.w.tab.keyed, x.w.tab.typed, x.w.tab.clicksOf("Verify"))
			}
			if n := x.w.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'deny' AND code = 'demo_workspace' AND reason LIKE '%` + c.why + `%'`); n != 1 {
				t.Fatalf("%d audited refusals naming %q, want one", n, c.why)
			}
			if demoAllows(t, x) != 0 {
				t.Fatal("an allow was recorded")
			}
		})
	}
	t.Run("the dialog names another address", func(t *testing.T) {
		other := func(bool) DialogInfo {
			d := entryDialog(false)
			d.Text = "Verification Code sent to z***@example.test"
			return d
		}
		x := runADPIn(t, []codeReply{mail}, 0, other, demoSetup(demoAccount, demoAlias))
		if x.stop == nil || !strings.Contains(x.stop.reason, "names an address other than the persona's") || len(x.fc.since) != 0 {
			t.Fatalf("stop %+v lookups %d", x.stop, len(x.fc.since))
		}
	})
}

// codesOnly hides a lane's Mailbox method.
type codesOnly struct{ c Codes }

func (o codesOnly) LatestCode(ctx context.Context, since time.Time, domain string) (gmail.OTPResult, error) {
	return o.c.LatestCode(ctx, since, domain)
}

// One code per dialog and the one more after its own Resend: the demo reads
// two codes at most, each allow is audited, and a third is never read.
func TestApply_Walk_DemoReadsOneCodeAndOneMoreAfterTheDialogsOwnResend(t *testing.T) {
	adpMail := func(at time.Duration, c string) codeReply {
		return mailed(c, mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", t0.Add(at))
	}
	x := runADPIn(t, []codeReply{adpMail(30*time.Second, "111111"), adpMail(150*time.Second, "222222"), adpMail(300*time.Second, "333333")}, 2, entryDialog, demoSetup(demoAccount, demoAlias))
	if x.res != nil || x.stop == nil || !strings.Contains(x.stop.reason, "still stands after 2 codes (one Resend)") {
		t.Fatalf("end %+v stop %+v", x.res, x.stop)
	}
	if len(x.fc.since) != 2 || x.w.tab.clicksOf("Resend Code") != 1 || x.w.tab.keyed[30] != "222222" {
		t.Fatalf("lookups %d resend %d keyed %v: a third code is never read", len(x.fc.since), x.w.tab.clicksOf("Resend Code"), x.w.tab.keyed)
	}
	if want := CodeAnchor(x.resent); !x.fc.since[1].Equal(want) {
		t.Fatalf("the lookup after the Resend asked since %s, want %s", x.fc.since[1], want)
	}
	if n := demoAllows(t, x); n != 2 {
		t.Fatalf("%d audited allows, want two (the first code and the one after Resend)", n)
	}
}

// Node's adp-reopen-after-otp-login test: after ADP's code sign-in the posting
// re-offers only the opener the walk already followed; it is followed once
// more, never a third time, and never without a code step in the walk.
func TestApply_ADP_OpenerAlreadyFollowedIsReopenedOnceAfterTheCodeSignIn(t *testing.T) {
	posting := postingPage(Control{I: 1, Tag: "button", Labels: []string{"Complete Your Application"}, Form: -1})
	w := newWalker(newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, posting))
	w.opened[controlKey(posting.Controls[0])] = true
	if c := w.pickOpener(context.Background(), posting, posting.Controls); c != nil {
		t.Fatalf("opener %+v: without a code step a followed opener is not followed again", c)
	}
	w.codeEntries = 1
	if c := w.pickOpener(context.Background(), posting, posting.Controls); c == nil || c.I != 1 {
		t.Fatalf("opener %+v: after the code sign-in the opener is followed once more", c)
	}
	if c := w.pickOpener(context.Background(), posting, posting.Controls); c != nil {
		t.Fatalf("opener %+v: never a third time", c)
	}
}

// ADP's career-site passcode pane (cx-verification-otp) has no Verify: a whole
// code submits itself, so the code is typed and the pane waited out.
func TestApply_ADP_PasscodePaneWithoutVerifyTakesTheCodeAndCloses(t *testing.T) {
	pane := DialogInfo{
		Text:  "Please check your email for the security code a***@example.com Passcode Don't forget to check all of your inbox folders, including promotional, spam and junk. Resend Try a different method",
		Boxes: []DialogBox{{I: 30, Label: "Passcode"}},
	}
	w := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, withDialog(adpBase(), pane))
	fc := &fakeCodes{replies: []codeReply{mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your security code", t0)}}
	w.g.r.d.Page.Codes, w.g.r.d.Page.Code = fc, fastGate
	w.tab.onKeys = func(tb *scriptTab, _ int, _ string) { tb.set(adpBase()) }
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || w.tab.keyed[30] != "482916" {
		t.Fatalf("end %+v stop %+v keyed %v", res, st, w.tab.keyed)
	}
}

// ADP's employer privacy pane follows the passcode; its Agree follows the
// legal setting.
func TestApply_ADP_PrivacyPaneAfterThePasscodeFollowsTheLegalSetting(t *testing.T) {
	pane := DialogInfo{
		Text:  "Please check your email for the security code a***@example.com Passcode Resend Try a different method",
		Boxes: []DialogBox{{I: 30, Label: "Passcode"}},
	}
	policy := DialogInfo{
		Text:    "Employer Privacy Policy Applicant/Employee Privacy Notice This notice describes the categories of personal information",
		Buttons: []DialogButton{{I: 40, Label: "Disagree"}, {I: 41, Label: "Agree"}},
	}
	for _, on := range []bool{true, false} {
		w := newWalk(t, Input{URL: adpURL, Caller: gates.CallerCLI}, withDialog(adpBase(), pane))
		fc := &fakeCodes{replies: []codeReply{mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your security code", t0)}}
		w.g.r.d.Page.Codes, w.g.r.d.Page.Code = fc, fastGate
		w.g.r.d.Page.Settings = func() Settings { return Settings{AutoAcceptLegalDeclarations: on} }
		w.tab.onKeys = func(tb *scriptTab, _ int, _ string) { tb.set(withDialog(adpBase(), policy)) }
		w.tab.onClick = func(tb *scriptTab, c Control) {
			if c.I == 41 {
				tb.set(adpBase())
			}
		}
		res, st := w.walk()
		agreed := slices.Contains(w.tab.clicks, "Agree")
		switch {
		case st != nil || w.tab.keyed[30] != "482916" || slices.Contains(w.tab.clicks, "Disagree"):
			t.Fatalf("on=%v: end %+v stop %+v keyed %v clicks %v", on, res, st, w.tab.keyed, w.tab.clicks)
		case on && (res.End != EndSubmit || !agreed):
			t.Fatalf("setting on: end %+v clicks %v", res, w.tab.clicks)
		case !on && (res.End != EndConsentGate || agreed):
			t.Fatalf("setting off: end %+v clicks %v", res, w.tab.clicks)
		}
	}
}

// A step the site refuses with a notice that only closes (ADP's "Errors
// Found") is filled again and continued once the notice is closed.
func TestApply_Walk_ARefusedStepsNoticeIsClosedAndTheStepContinues(t *testing.T) {
	closer := 50
	notice := DialogInfo{Text: "Errors Found Before you continue, correct the indicated errors.", Closer: &closer}
	covered := func() *PageState {
		st := withDialog(wizardStep(1, 2), notice)
		st.Controls = append(st.Controls, Control{I: closer, Tag: "div", Modal: true})
		return st
	}
	w := newWalk(t, Input{URL: loginURL, Caller: gates.CallerCLI}, wizardStep(1, 2))
	// The first fill misses the source the step refuses; the fill after the
	// notice writes it.
	w.page.fills = []*engine.PageResult{doneResult(scriptedRow), doneResult(engine.Row{Selector: "#source", Label: "Candidate Source", Kind: "custom-listbox", Outcome: "filled", Value: "LinkedIn"})}
	refused := false
	w.tab.onClick = func(tb *scriptTab, c Control) {
		switch {
		case c.I == closer:
			tb.set(wizardStep(1, 2))
		case c.Labels[0] == "Next" && !refused:
			refused = true
			tb.set(covered())
		case c.Labels[0] == "Next":
			tb.set(wizardStep(2, 2))
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit || slices.Index(w.tab.clicks, "") < 0 {
		t.Fatalf("end %+v stop %+v clicks %q", res, st, w.tab.clicks)
	}
}
