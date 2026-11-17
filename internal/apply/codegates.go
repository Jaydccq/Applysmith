package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/lazyregexp"
)

// The two dialogs of an emailed-code step that are not native fields: the
// delivery choice ("Where would you like to receive a verification code?")
// and the code entry that follows it. Both stand in custom elements, so the
// page's field list never shows them and the walk would read the page under
// them as unchanged.

var (
	codeHeadingRE  = lazyregexp.New(`(?i)verification code`)
	emailTokenRE   = lazyregexp.New(`(?i)^email$`)
	sendRE         = lazyregexp.New(`(?i)^send(?: code| verification code)?$`)
	verifyBtnRE    = lazyregexp.New(`(?i)^verify(?: code)?$`)
	segSubmitRE    = lazyregexp.New(`(?i)^(?:verify(?: code)?|next|continue)$`)
	resendRE       = lazyregexp.New(`(?i)^(?:resend(?:\s+(?:the\s+)?code)?|send\s+(?:a\s+)?new\s+code)$`)
	segBoxLabelRE  = lazyregexp.New(`(?i)\b(?:otp|one[- ]?time)\b|\bdigit\b|\bcharacter\b`)
	otpLabelRE     = lazyregexp.New(`(?i)otp|one[- ]?time|verification\s*code|passcode|security\s*code|\bpin\b|(?:sms|text(?:\s+message)?|phone|mobile)[\s-]*(?:verification[\s-]*)?code|(?:verification[\s-]*)?code[\s\S]{0,24}(?:sms|text(?:\s+message)?|phone|mobile)`)
	codeRequestRE  = lazyregexp.New(`(?i)\bone[- ]?time\b|\botp\b|\bpasscode\b|\b(?:security|verification|confirmation|access|login|sign[- ]?in|authentication)\s+code\b|\bcode\s+(?:sent|from|we|you|that|to)\b|\benter\s+(?:the\s+)?code\b`)
	codeCreationRE = lazyregexp.New(`(?i)\b(?:create|choose|set\s*up|set|new|reset)\s+(?:a\s+|an\s+|your\s+|new\s+)*(?:pass\s?code|password|pin\b|code)`)
	separatorsRE   = lazyregexp.New(`[_\-.]+`)
)

// isCodeStepText reads text as the prompt of a code step; text that asks the
// user to create a code is not one.
func isCodeStepText(text string) bool {
	if strings.TrimSpace(text) == "" || codeCreationRE.MatchString(text) {
		return false
	}
	return otpLabelRE.MatchString(text) || codeRequestRE.MatchString(separatorsRE.ReplaceAllString(text, " "))
}

// deliveryChoice is a dialog that offers to send the code by email.
type deliveryChoice struct {
	email DialogOption
	send  DialogButton
}

// classifyDelivery finds the delivery-choice dialog: a heading (or the start
// of its text) naming a verification code, an EMAIL choice and a Send button,
// and nothing that asks for a password or a signature. The phone choice is
// never taken; the mailbox is the only source a run can read.
func classifyDelivery(ds []DialogInfo) (deliveryChoice, bool) {
	for _, d := range ds {
		if d.Blocked || !(codeHeadingRE.MatchString(d.Heading) || codeHeadingRE.MatchString(clipRunes(d.Text, 200))) {
			continue
		}
		var ch deliveryChoice
		found := false
		for _, o := range d.Options {
			if emailTokenRE.MatchString(strings.TrimSpace(o.Value)) || emailTokenRE.MatchString(strings.TrimSpace(o.Label)) {
				ch.email, found = o, true
				break
			}
		}
		if !found {
			continue
		}
		for _, b := range d.Buttons {
			if sendRE.MatchString(strings.TrimSpace(b.Label)) {
				ch.send = b
				return ch, true
			}
		}
	}
	return deliveryChoice{}, false
}

// codeEntry is a dialog (or a segmented form) that takes the emailed code.
type codeEntry struct {
	d         DialogInfo
	boxes     []DialogBox
	verify    DialogButton
	resend    *DialogButton
	segmented bool
	// hasVerify is false for a pane whose confirm control is not Verify-shaped
	// (a "Submit code" button, a missing one): the run types nothing into it
	// and presses nothing on it.
	hasVerify bool
}

// classifyCodeEntry finds the code-entry dialog: a code prompt over exactly
// one visible text box (or over four to eight boxes that each read as a code
// digit). A dialog that also asks for a password, a signature or a captcha is
// not one.
func classifyCodeEntry(ds []DialogInfo) (codeEntry, bool) {
	for _, d := range ds {
		if d.Blocked {
			continue
		}
		seg := len(d.Boxes) >= 4 && len(d.Boxes) <= 8
		for _, b := range d.Boxes {
			seg = seg && segBoxLabelRE.MatchString(b.Label)
		}
		if !seg && (len(d.Boxes) != 1 || d.Form) {
			continue
		}
		if !isCodeStepText(d.Heading) && !isCodeStepText(clipRunes(d.Text, 200)) {
			continue
		}
		submit := verifyBtnRE
		if seg {
			submit = segSubmitRE
		}
		ce := codeEntry{d: d, boxes: d.Boxes, segmented: seg}
		for i, b := range d.Buttons {
			switch {
			case !ce.hasVerify && submit.MatchString(strings.TrimSpace(b.Label)):
				ce.verify, ce.hasVerify = b, true
			case resendRE.MatchString(strings.TrimSpace(b.Label)):
				ce.resend = &d.Buttons[i]
			}
		}
		return ce, true
	}
	return codeEntry{}, false
}

// codeDialogSubmits are the probe handles of the controls inside a code-entry
// dialog that read as a Submit. They confirm the code, never the application:
// the walk's categories leave them out so a "Submit code" button can neither
// end the walk as the form's Submit nor be pressed by the submit stage.
func codeDialogSubmits(st *PageState) map[int]bool {
	ce, ok := classifyCodeEntry(st.Dialogs)
	if !ok {
		return nil
	}
	out := map[int]bool{}
	for _, b := range ce.d.Buttons {
		if gates.ClassifyControl(b.Label) == gates.ControlSubmit {
			out[b.I] = true
		}
	}
	return out
}

// maxCodeEntries bounds the code panes one walk completes: the first code and
// the one typed after a Resend.
const maxCodeEntries = 2

// codeDialogs answers the emailed-code dialogs a page shows: the delivery
// choice first, then the code entry. It reports whether it acted, so the walk
// reads the page again instead of pressing on. A run that cannot read mail
// ends on the dialog instead of reading the page under it as unchanged.
func (w *walker) codeDialogs(ctx context.Context, st *PageState) (bool, *stop) {
	if len(st.Dialogs) == 0 {
		return false, nil
	}
	if why, deny := w.noMailWhy(ctx, st); why != "" {
		return false, w.noMailStop(ctx, st, why, deny)
	}
	if acted, s := w.deliveryGate(ctx, st); acted || s != nil {
		return acted, s
	}
	return w.codeEntryGate(ctx, st)
}

// verificationStop ends the run on a code dialog a person finishes: the tab
// stays open for them.
func (w *walker) verificationStop(reason string) *stop {
	return &stop{class: ClassVerification, board: "one-time-code", reason: reason, at: StageFill}
}

// noMailWhy says why this run cannot read the mailbox, or "" when it can. A
// demo workspace can only when the demo gate allows the code lookup at this
// dialog; its refusal is returned with the reason so the stop can audit it.
func (w *walker) noMailWhy(ctx context.Context, st *PageState) (string, *gates.Decision) {
	switch {
	case w.r.demo():
		d := w.demoCodeDecision(ctx, st, false)
		if d.Allowed() {
			return "", nil
		}
		return d.Reason, &d
	case w.r.d.Page.Codes == nil:
		return "no mail lane connected", nil
	}
	return "", nil
}

// demoCodeDecision asks the demo gate whether this run may read the emailed
// code of the dialog on st. At the dialog's sight (lookup false) the lookup is
// the one that would start now; at the lookup itself it is the real one.
func (w *walker) demoCodeDecision(ctx context.Context, st *PageState, lookup bool) gates.Decision {
	ce, entry := classifyCodeEntry(st.Dialogs)
	_, delivery := classifyDelivery(st.Dialogs)
	now := w.r.d.Now()
	if (entry || delivery) && w.e.codeSeenAt.IsZero() {
		w.e.codeSeenAt = now
	}
	f := gates.DemoCodeFacts{
		DryRun: !w.e.Run.Input.Submit, InWalk: true, AtCodeDialog: entry || delivery,
		PersonaEmail: w.personaEmail(ctx), SenderDomain: w.s.codeDomain(),
		OpenedMS: unixMS(w.codeOpened()), SinceMS: unixMS(now),
	}
	if m, ok := w.r.d.Page.Codes.(Mailboxed); ok {
		f.Account = m.Mailbox(ctx)
	}
	if entry {
		f.PageAddress = addressRE.FindString(ce.d.Text)
	}
	if lookup {
		f.SinceMS, f.CodesRead, f.Resent = unixMS(w.codeAnchor()), w.codeEntries, w.e.codeResent > 0
	}
	host, _ := parseHost(st.URL)
	return gates.DemoWorkspace(gates.DemoInput{Demo: true, Action: gates.DemoMailCode, Host: host, Code: f})
}

// unixMS is t in Unix milliseconds, 0 for the zero time.
func unixMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// codeOpened is when the code dialog opened: the last Send or Resend press of
// the walk, else the walk's first sight of the dialog.
func (w *walker) codeOpened() time.Time {
	if w.e.codeSentAt.After(w.e.codeSeenAt) {
		return w.e.codeSentAt
	}
	return w.e.codeSeenAt
}

// personaEmail is the address the applicant snapshot names, or "".
func (w *walker) personaEmail(ctx context.Context) string {
	snap, _ := w.r.applicant(ctx)
	return snap.Candidate.Email
}

// noMailStop is the end of a walk that meets an emailed-code dialog it cannot
// answer: it reads no mail, presses nothing in the dialog and names the
// address the site wrote to. A demo workspace's refusal is audited as the
// demo gate's decision.
func (w *walker) noMailStop(ctx context.Context, st *PageState, why string, deny *gates.Decision) *stop {
	var what string
	switch ce, entry := classifyCodeEntry(st.Dialogs); {
	case entry && w.e.Run.ATS == ATSGreenhouse && !w.e.Run.Pressed:
		return nil // a code box before any Submit is the check lane's note
	case entry:
		what = "the site emailed a verification code to " + w.codeAddress(ctx, ce.d)
	default:
		if _, ok := classifyDelivery(st.Dialogs); !ok {
			return nil
		}
		what = "the site offers to email a verification code"
	}
	if deny != nil {
		if aerr := w.r.audit(w.e, *deny, st.URL, false); aerr != nil {
			return &stop{class: ClassEnvironment, reason: aerr.Error()}
		}
	}
	return w.verificationStop(what + "; this run cannot read mail (" + why + ")")
}

var addressRE = lazyregexp.New(`[A-Za-z0-9._%+*-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// maskAddress keeps the first character of the name and the whole domain.
func maskAddress(a string) string {
	name, domain, ok := strings.Cut(a, "@")
	if !ok || name == "" || strings.Contains(name, "*") {
		return a
	}
	return string([]rune(name)[:1]) + "***@" + domain
}

// codeAddress is the masked address a code was sent to: the one the dialog
// names, else the applicant's own, else the words the dialog uses.
func (w *walker) codeAddress(ctx context.Context, d DialogInfo) string {
	if m := addressRE.FindString(d.Text); m != "" {
		return maskAddress(m)
	}
	if e := w.personaEmail(ctx); e != "" {
		return maskAddress(e)
	}
	return "your email address"
}

// codeNote records one decision of the code dialog on the run's latest page
// report, never the code.
func (w *walker) codeNote(format string, a ...any) {
	if n := len(w.e.Run.Pages); n > 0 {
		p := &w.e.Run.Pages[n-1]
		p.Notes = append(p.Notes, "code dialog: "+fmt.Sprintf(format, a...))
	}
}

// pressInDialog presses one control of a dialog by its probe handle. It
// reports "pressed", "disabled" (a disabled control swallows the click, so it
// is not pressed) or "absent". Withdraw, Discard and Submit controls are never
// pressed here.
func (w *walker) pressInDialog(ctx context.Context, handle int) (string, *stop) {
	loc, err := Locate(ctx, w.s.tab, handle)
	if err != nil {
		return "", w.browserStop(err)
	}
	if !loc.Found || !loc.Visible {
		return "absent", nil
	}
	if loc.Disabled {
		return "disabled", nil
	}
	switch gates.ClassifyControl(loc.Labels...) {
	case gates.ControlWithdraw, gates.ControlDiscard, gates.ControlSubmit:
		return "", &stop{class: ClassDefect, reason: "a code dialog control reads as " + strings.Join(loc.Labels, "/") + " and is not pressed"}
	}
	if !loc.Hit {
		return "absent", nil
	}
	if err := w.s.tab.Click(ctx, loc.X, loc.Y); err != nil {
		return "", w.browserStop(err)
	}
	return "pressed", nil
}

// requestCode takes the code-request gate for a Send or Resend press on the
// page's host and counts it when allowed. sentOnTab says the delivery choice's
// Send was already pressed on the run's tab: the gate refuses a second one.
func (w *walker) requestCode(st *PageState, sentOnTab bool) (bool, *stop) {
	host, _ := parseHost(st.URL)
	d := gates.CodeRequest(gates.CodeRequestInput{RequestsThisHost: w.e.codeRequests[host], RequestedOnThisTab: sentOnTab})
	if aerr := w.r.audit(w.e, d, st.URL, false); aerr != nil {
		return false, &stop{class: ClassEnvironment, reason: aerr.Error()}
	}
	if !d.Allowed() {
		return false, nil
	}
	if w.e.codeRequests == nil {
		w.e.codeRequests = map[string]int{}
	}
	w.e.codeRequests[host]++
	return true, nil
}

// deliveryGate chooses EMAIL and presses Send, then waits for the dialog to
// close. The code-request gate bounds it: one Send on the run's tab, inside
// the host's quota. The Send instant anchors the mail lookup that follows.
func (w *walker) deliveryGate(ctx context.Context, st *PageState) (bool, *stop) {
	ch, ok := classifyDelivery(st.Dialogs)
	if !ok {
		return false, nil
	}
	if allowed, s := w.requestCode(st, w.e.deliverySent); s != nil || !allowed {
		return false, s
	}
	if res, s := w.pressInDialog(ctx, ch.email.I); s != nil || res != "pressed" {
		return false, s
	}
	// The dialog is read again for the Send button: choosing a radio redraws it.
	st2, err := Probe(ctx, w.s.tab)
	if err != nil {
		return false, w.browserStop(err)
	}
	ch2, ok := classifyDelivery(st2.Dialogs)
	if !ok {
		return false, nil
	}
	pressed := w.r.d.Now()
	res, s := w.pressInDialog(ctx, ch2.send.I)
	if s != nil || res != "pressed" {
		return false, s
	}
	w.e.deliverySent, w.e.codeSentAt = true, pressed
	return true, w.waitDialogGone(ctx, func(p *PageState) bool { _, open := classifyDelivery(p.Dialogs); return open })
}

// waitDialogGone polls until open reports false, for at most the close wait.
// A dialog that stays is not an error: the walk reads the page again.
func (w *walker) waitDialogGone(ctx context.Context, open func(*PageState) bool) *stop {
	deadline := time.Now().Add(w.r.d.Page.Code.withDefaults().Close)
	for {
		p, err := Probe(ctx, w.s.tab)
		if err != nil {
			return w.browserStop(err)
		}
		if !open(p) || !time.Now().Before(deadline) {
			return nil
		}
		if !w.sleep(ctx, 250*time.Millisecond) {
			return cancelledStop(StageFill)
		}
	}
}

// codeAnchor is where this run's lookup starts: the ordinary lookback, or the
// moment of the last Send or Resend press when that is later.
func (w *walker) codeAnchor() time.Time {
	if w.r.demo() {
		// A demo reads only mail that arrived after the dialog opened.
		return CodeAnchor(w.codeOpened())
	}
	tm := w.r.d.Page.Code.withDefaults()
	since := w.r.d.Now().Add(-tm.Lookback)
	// The dialog's code was mailed after the press that brought it, the walk's
	// own or a Send / Resend: an earlier mail is another attempt's, and a site
	// that mails a new code refuses the old one.
	for _, t := range []time.Time{w.e.codeSentAt, w.e.walkPressedAt} {
		if a := CodeAnchor(t); !t.IsZero() && a.After(since) {
			since = a
		}
	}
	return since
}

// clickPage presses a page control for the walk and records when, for the
// code anchor.
func (w *walker) clickPage(ctx context.Context, loc Located) error {
	w.e.walkPressedAt = w.r.d.Now()
	return w.s.tab.Click(ctx, loc.X, loc.Y)
}

// codeEntryGate completes a code-entry dialog: it reads the emailed code
// (waiting for the lane's whole code window, which outlasts the stall grace of
// the walk), types it key by key, waits for Verify to enable and never
// presses it while disabled, and waits for the dialog to close. A pane that
// stands again after this run typed a code waits out the countdown of its own
// Resend control, presses it once inside the code-request quota, and reads
// only mail after that press; a second pane that still stands ends the walk.
// Every way this does not finish the pane ends the walk as a verification
// stop that keeps the tab for the person, with a reason that names the cause.
func (w *walker) codeEntryGate(ctx context.Context, st *PageState) (bool, *stop) {
	ce, ok := classifyCodeEntry(st.Dialogs)
	if !ok || (w.e.Run.ATS == ATSGreenhouse && !w.e.Run.Pressed) {
		return false, nil
	}
	// A pane with no control at all submits a whole code itself (ADP's
	// career-site passcode pane); one whose confirm control is not a Verify is
	// left to the person, since that control may be the application's Submit.
	if !ce.hasVerify && len(ce.d.Buttons) > 0 {
		return false, w.verificationStop("the code dialog has no Verify control the run may press; it is left to you")
	}
	if w.codeEntries >= maxCodeEntries {
		return false, w.verificationStop(fmt.Sprintf("the code dialog still stands after %d codes (one Resend); it is left to you", w.codeEntries))
	}
	if w.codeEntries > 0 || w.e.codeLane().spent() {
		pressed, s := w.resendCode(ctx, ce, st)
		if s != nil {
			return false, s
		}
		if !pressed {
			// The pane closed while its Resend was awaited: no mail was asked
			// for, so none is waited for and the walk reads the page again.
			w.e.codeSeenAt = time.Time{}
			return true, nil
		}
	}
	var fits func(string) bool
	if ce.segmented {
		fits = fitsBoxes(len(ce.boxes))
	}
	domain := w.s.codeDomain()
	code, ev, s, err := w.lookupCode(ctx, st, domain, fits)
	if s != nil {
		return false, s
	}
	if errors.Is(err, errNoCode) && w.e.codeResent == 0 && ctx.Err() == nil {
		// No mail in the window: the pane's own Resend, when it stands enabled
		// now, is pressed once and only mail after the press is read.
		if cur, perr := Probe(ctx, w.s.tab); perr == nil {
			if again, ok := classifyCodeEntry(cur.Dialogs); ok && again.resend != nil && !again.resend.Disabled {
				pressed, s := w.resendCode(ctx, again, cur)
				if s != nil {
					return false, s
				}
				if !pressed {
					w.e.codeSeenAt = time.Time{}
					return true, nil
				}
				ce = again
				if code, ev, s, err = w.lookupCode(ctx, cur, domain, fits); s != nil {
					return false, s
				}
			}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, cancelledStop(StageFill)
		}
		var cf *CodeFailure
		switch {
		case errors.As(err, &cf):
			w.e.noMailbox(hostOf(st.URL), err)
			return false, w.verificationStop(cf.Error())
		case errors.Is(err, ErrCodeBoxes):
			return false, w.verificationStop(fmt.Sprintf("the mailed code of %d characters does not fit the dialog's %d boxes; the mail stays available", ev.Length, len(ce.boxes)))
		}
		from := "any sender"
		if domain != "" {
			from = domain
		}
		return false, w.verificationStop(fmt.Sprintf("no verification code from %s arrived within %s", from, w.r.d.Page.Code.withDefaults().GiveUp))
	}
	w.codeEntries++
	for i, b := range ce.boxes {
		text := code
		if ce.segmented {
			text = string([]rune(code)[i])
		}
		landed, err := TypeKeys(ctx, w.s.tab, b.I, text)
		if err != nil {
			return false, w.browserStop(err)
		}
		if !landed {
			return false, w.verificationStop("the mailed code did not land in the dialog's box; it is left to you")
		}
	}
	w.codeNote("a code was typed (sender %s, subject %q, received %s)", ev.Sender, ev.Subject, ev.At.UTC().Format("15:04:05"))
	if !ce.hasVerify {
		// A pane that stands is met again by the walk, as a rejected code.
		if s := w.waitDialogGone(ctx, func(p *PageState) bool { _, open := classifyCodeEntry(p.Dialogs); return open }); s != nil {
			return false, s
		}
		w.e.codeSeenAt = time.Time{}
		return true, nil
	}
	return w.verifyCode(ctx)
}

// lookupCode reads the code mailed after the last Send or Resend press; a demo
// workspace reads it only under an audited grant. A refused grant is a stop.
func (w *walker) lookupCode(ctx context.Context, st *PageState, domain string, fits func(string) bool) (string, CodeEvidence, *stop, error) {
	lookupCtx := ctx
	if w.r.demo() {
		d := w.demoCodeDecision(ctx, st, true)
		if aerr := w.r.audit(w.e, d, st.URL, false); aerr != nil {
			return "", CodeEvidence{}, &stop{class: ClassEnvironment, reason: aerr.Error()}, nil
		}
		if !d.Allowed() {
			return "", CodeEvidence{}, w.verificationStop("this run cannot read mail (" + d.Reason + ")"), nil
		}
		lookupCtx = gmail.DemoCodeGrant(ctx, d, domain, w.codeAnchor())
	}
	code, ev, err := w.e.codeLane().Lookup(lookupCtx, w.r.d.Page.Codes, w.r.d.Page.Code, w.r.d.Now, w.codeAnchor(), domain, fits)
	return code, ev, nil, err
}

// resendCode presses the pane's own Resend control once it is there and
// enabled, waiting out its countdown for at most the resend wait. The press
// takes the code-request gate; the lookup that follows reads only mail after
// it. It reports whether Resend was pressed: false with no stop is a pane that
// went away meanwhile, for which no mail was asked.
func (w *walker) resendCode(ctx context.Context, ce codeEntry, st *PageState) (bool, *stop) {
	wait := w.r.d.Page.Code.withDefaults().Resend
	deadline := time.Now().Add(wait)
	for {
		if ce.resend != nil && !ce.resend.Disabled {
			allowed, s := w.requestCode(st, false)
			if s != nil {
				return false, s
			}
			if !allowed {
				return false, w.verificationStop("the code-request quota is spent, so the dialog's Resend is not pressed; it is left to you")
			}
			pressed := w.r.d.Now()
			res, s := w.pressInDialog(ctx, ce.resend.I)
			if s != nil {
				return false, s
			}
			if res == "pressed" {
				w.e.codeSentAt = pressed
				w.e.codeResent++
				w.codeNote("Resend pressed after the countdown")
				return true, nil
			}
		}
		if !time.Now().Before(deadline) {
			return false, w.verificationStop(fmt.Sprintf("the code dialog stands again and its Resend control did not become available within %s; it is left to you", wait))
		}
		if !w.sleep(ctx, 250*time.Millisecond) {
			return false, cancelledStop(StageFill)
		}
		cur, err := Probe(ctx, w.s.tab)
		if err != nil {
			return false, w.browserStop(err)
		}
		next, ok := classifyCodeEntry(cur.Dialogs)
		if !ok {
			return false, nil
		}
		ce, st = next, cur
	}
}

// verifyCode presses the pane's Verify once it is enabled (waiting for up to
// the verify wait) and waits for the pane to close.
func (w *walker) verifyCode(ctx context.Context) (bool, *stop) {
	deadline := time.Now().Add(w.r.d.Page.Code.withDefaults().Verify)
	for {
		cur, err := Probe(ctx, w.s.tab)
		if err != nil {
			return false, w.browserStop(err)
		}
		ce, ok := classifyCodeEntry(cur.Dialogs)
		if !ok {
			return true, nil
		}
		if !ce.hasVerify {
			return false, w.verificationStop("the code dialog lost its Verify control after the code was typed; it is left to you")
		}
		res, s := w.pressInDialog(ctx, ce.verify.I)
		if s != nil {
			return false, s
		}
		if res == "pressed" {
			w.codeNote("Verify pressed")
			break
		}
		if res == "absent" {
			return false, w.verificationStop("the code dialog's Verify control went away; it is left to you")
		}
		if !time.Now().Before(deadline) {
			return false, w.verificationStop("the code was typed but Verify stayed disabled; it is left to you")
		}
		if !w.sleep(ctx, 250*time.Millisecond) {
			return false, cancelledStop(StageFill)
		}
	}
	if s := w.waitDialogGone(ctx, func(p *PageState) bool { _, open := classifyCodeEntry(p.Dialogs); return open }); s != nil {
		return false, s
	}
	w.e.codeSeenAt = time.Time{}
	return true, nil
}

// lateCodeDialogs is the one more look at the end of the ordinary stall
// grace: ADP draws its code-entry pane seconds after Send, a mail-server round
// trip rather than a screen transition.
func (w *walker) lateCodeDialogs(ctx context.Context) (bool, *stop) {
	st, err := Probe(ctx, w.s.tab)
	if err != nil {
		return false, w.browserStop(err)
	}
	return w.codeDialogs(ctx, st)
}

// awaitCodeDialogs polls for an emailed-code dialog for at most the press
// wait, the same grace a pressed control is given to move the page, and
// answers the first one that stands. It is the walk's look at a form page that
// offers no control to press: the pane that explains why is still on its way.
func (w *walker) awaitCodeDialogs(ctx context.Context) (bool, *stop) {
	deadline := time.Now().Add(w.tm.PressWait)
	for {
		st, err := Probe(ctx, w.s.tab)
		if err != nil {
			return false, w.browserStop(err)
		}
		if acted, s := w.codeDialogs(ctx, st); acted || s != nil {
			return acted, s
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		if !w.sleep(ctx, w.tm.Poll) {
			return false, cancelledStop(StageFill)
		}
	}
}

// ADP's career site signs an applicant in by email first: its own components
// (an sdf-input with the email box in its shadow root, an sdf-button
// Continue), then a code mailed to that address, which the code dialogs read.
// The page asks for nothing but the email, so it is no form the engine fills.

// adpAuthExpr finds the step, the centres of its email box and Continue, and
// the text Continue shows.
const adpAuthExpr = `(() => {
  const host = document.querySelector('sdf-input#userEmail');
  const box = host && host.shadowRoot && host.shadowRoot.querySelector('input');
  const go = Array.from(document.querySelectorAll('sdf-button')).find((b) => /^continue$/i.test((b.textContent || '').trim()));
  const at = (e) => { if (!e) return null; const r = e.getBoundingClientRect(); return r.width && r.height ? [r.x + r.width / 2, r.y + r.height / 2] : null; };
  const b = at(box), c = at(go);
  return b && c ? { found: true, box: b, go: c, label: (go.textContent || '').trim() } : { found: false };
})()`

type adpAuthState struct {
	Found bool      `json:"found"`
	Box   []float64 `json:"box"`
	Go    []float64 `json:"go"`
	Label string    `json:"label"`
}

func adpAuthFind(ctx context.Context, t Tab) (adpAuthState, error) {
	var a adpAuthState
	err := t.Eval(ctx, adpAuthExpr, &a)
	return a, err
}

// adpAuthStep types the profile's email into ADP's sign-in step and presses
// Continue, once per walk. acted is true when the step left the page.
func (w *walker) adpAuthStep(ctx context.Context, st *PageState) (res *walkResult, stp *stop, acted bool) {
	if w.adpAuthDone {
		return nil, nil, false
	}
	a, err := adpAuthFind(ctx, w.s.tab)
	if err != nil || !a.Found {
		return nil, nil, false
	}
	w.adpAuthDone = true
	email, _ := w.s.icimsApplicant(ctx)
	if email == "" {
		return nil, &stop{class: ClassAnswerGap, reason: "ADP's sign-in step asks for an email and the profile holds none"}, false
	}
	d := gates.PressControl(gates.ControlInput{Unattended: w.e.unattended(), Stage: gates.StageWalk, Found: true, Labels: []string{a.Label}})
	if aerr := w.r.audit(w.e, d, st.URL, false); aerr != nil {
		return nil, &stop{class: ClassEnvironment, reason: aerr.Error()}, false
	}
	if !d.Allowed() {
		return nil, &stop{class: ClassDefect, reason: "control not pressed: " + d.Reason}, false
	}
	if err := w.s.tab.Click(ctx, a.Box[0], a.Box[1]); err != nil {
		return nil, w.browserStop(err), false
	}
	if err := w.s.tab.TypeText(ctx, email); err != nil {
		return nil, w.browserStop(err), false
	}
	// The button is found again: typing can redraw the step.
	if again, err := adpAuthFind(ctx, w.s.tab); err == nil && again.Found {
		a = again
	}
	if err := w.s.tab.Click(ctx, a.Go[0], a.Go[1]); err != nil && !lateClick(err) {
		return nil, w.browserStop(err), false
	}
	deadline := time.Now().Add(w.tm.PressWait)
	for time.Now().Before(deadline) {
		if !w.sleep(ctx, w.tm.Poll) {
			return nil, cancelledStop(StageFill), false
		}
		if now, err := adpAuthFind(ctx, w.s.tab); err == nil && !now.Found {
			return nil, nil, true
		}
	}
	return &walkResult{End: EndStalled, Detail: "ADP's sign-in step was still showing after Continue" + stallWords(st), Last: st}, nil, false
}

// A new ADP account then asks where to send its passcode: a text to the phone
// or a mail to the address. The run reads mail only, so it picks the mail.

// adpMethodExpr finds the step, the centres of its email choice and Send, and
// the text Send shows.
const adpMethodExpr = `(() => {
  const pane = document.querySelector('cx-verification-method');
  const mail = pane && pane.querySelector('sdf-radio-button[value="email"]');
  const send = pane && Array.from(pane.querySelectorAll('sdf-button')).find((b) => /^send passcode$/i.test((b.textContent || '').trim()));
  const at = (e) => { if (!e) return null; const r = e.getBoundingClientRect(); return r.width && r.height ? [r.x + r.width / 2, r.y + r.height / 2] : null; };
  const m = at(mail), s = at(send);
  return m && s ? { found: true, mail: m, send: s, label: (send.textContent || '').trim(), ready: send.getAttribute('aria-disabled') !== 'true' && !send.hasAttribute('disabled') } : { found: false };
})()`

type adpMethodState struct {
	Found bool      `json:"found"`
	Mail  []float64 `json:"mail"`
	Send  []float64 `json:"send"`
	Label string    `json:"label"`
	Ready bool      `json:"ready"`
}

func adpMethodFind(ctx context.Context, t Tab) (adpMethodState, error) {
	var a adpMethodState
	err := t.Eval(ctx, adpMethodExpr, &a)
	return a, err
}

// adpMethodStep picks the mail on ADP's verification-method step and presses
// Send passcode, once per walk; the code dialogs then read the mailed code.
// acted is true when the step left the page.
func (w *walker) adpMethodStep(ctx context.Context, st *PageState) (res *walkResult, stp *stop, acted bool) {
	if w.adpMethodDone {
		return nil, nil, false
	}
	a, err := adpMethodFind(ctx, w.s.tab)
	if err != nil || !a.Found {
		return nil, nil, false
	}
	w.adpMethodDone = true
	d := gates.PressControl(gates.ControlInput{Unattended: w.e.unattended(), Stage: gates.StageAccountFlow, Found: true, Labels: []string{a.Label}})
	if aerr := w.r.audit(w.e, d, st.URL, false); aerr != nil {
		return nil, &stop{class: ClassEnvironment, reason: aerr.Error()}, false
	}
	if !d.Allowed() {
		return nil, &stop{class: ClassDefect, reason: "control not pressed: " + d.Reason}, false
	}
	if err := w.s.tab.Click(ctx, a.Mail[0], a.Mail[1]); err != nil {
		return nil, w.browserStop(err), false
	}
	deadline := time.Now().Add(w.tm.PressWait)
	for !a.Ready {
		if !time.Now().Before(deadline) {
			return &walkResult{End: EndStalled, Detail: "ADP's Send passcode stayed disabled after the email was chosen" + stallWords(st), Last: st}, nil, false
		}
		if !w.sleep(ctx, w.tm.Poll) {
			return nil, cancelledStop(StageFill), false
		}
		if a, err = adpMethodFind(ctx, w.s.tab); err != nil || !a.Found {
			return nil, nil, true
		}
	}
	if err := w.s.tab.Click(ctx, a.Send[0], a.Send[1]); err != nil && !lateClick(err) {
		return nil, w.browserStop(err), false
	}
	deadline = time.Now().Add(w.tm.PressWait)
	for time.Now().Before(deadline) {
		if !w.sleep(ctx, w.tm.Poll) {
			return nil, cancelledStop(StageFill), false
		}
		if now, err := adpMethodFind(ctx, w.s.tab); err == nil && !now.Found {
			return nil, nil, true
		}
	}
	return &walkResult{End: EndStalled, Detail: "ADP's verification-method step was still showing after Send passcode" + stallWords(st), Last: st}, nil, false
}

// policyTitleRE is a modal that asks the applicant to agree to the
// employer's privacy policy or terms before going on (ADP shows one right
// after the passcode).
var policyTitleRE = lazyregexp.New(`(?i)\b(?:privacy\s+(?:policy|notice|statement)|terms\s+(?:and|&)\s+conditions|terms\s+of\s+(?:use|service))\b`)

// policyDialog presses the Agree of an employer-policy modal when the
// consent-control gate allows it (the legal setting). It reports whether it
// pressed, else the modal's title and why it stays the person's.
func (w *walker) policyDialog(ctx context.Context, st *PageState) (bool, string, string, *stop) {
	for _, d := range st.Dialogs {
		title := firstNonEmpty(strings.TrimSpace(d.Heading), clip(strings.TrimSpace(d.Text), 120))
		// The policy is named by the title, else by a sentence of the dialog's
		// text (Netflix's "Candidate Privacy": "You acknowledge agreement with
		// our Candidate Privacy Statement"), which is what the gate reads.
		policy := title
		if !policyTitleRE.MatchString(policy) {
			policy = policySentence(d.Text)
		}
		// A cookie notice is closed with the least consent instead.
		if d.Blocked || d.Form || len(d.Boxes) > 0 || policy == "" || strings.Contains(strings.ToLower(d.Text), "cookie") {
			continue
		}
		for _, b := range d.Buttons {
			if b.Disabled || !dataAcceptRE.MatchString(b.Label) {
				continue
			}
			if w.policyAgreed {
				return false, title, "the dialog stayed after its Agree was pressed", nil
			}
			on := w.r.d.Page.Settings != nil && w.r.d.Page.Settings().AutoAcceptLegalDeclarations
			g := gates.ConsentControl(gates.ConsentInput{Label: policy, PageMarksRequired: true, SettingOn: on, Surface: gates.SurfaceForm})
			if err := w.r.audit(w.e, g, st.URL, false); err != nil {
				return false, title, "", &stop{class: ClassEnvironment, reason: err.Error()}
			}
			if !g.Allowed() {
				return false, title, g.Reason, nil
			}
			w.policyAgreed = true
			if _, s := w.pressAs(ctx, st, Control{I: b.I, Labels: []string{b.Label}, Modal: true}, gates.StageDataConsent); s != nil {
				return false, title, "", s
			}
			w.s.walkNotes = append(w.s.walkNotes, fmt.Sprintf("policy dialog: %q agreed (%s)", title, g.Reason))
			return true, title, "", nil
		}
	}
	return false, "", "", nil
}

// sentenceSplitRE ends a sentence of a dialog's text.
var sentenceSplitRE = lazyregexp.New(`[.?!]+(?:\s+|$)`)

// policySentence is the first sentence of text that names a policy, "" when
// none does.
func policySentence(text string) string {
	for _, part := range sentenceSplitRE.Split(text, -1) {
		if policyTitleRE.MatchString(part) {
			return strings.TrimSpace(part)
		}
	}
	return ""
}

// maxNotices bounds the notice dialogs one walk closes.
const maxNotices = 3

// closeLabelRE is a dialog button that only closes it.
var closeLabelRE = lazyregexp.New(`(?i)^\s*(?:[×✕x]|ok(?:ay)?|close|dismiss|got it)?\s*$`)

// pageDialogs answers the dialogs standing over the page that are no code
// pane: an employer-policy modal, then a notice that only closes.
func (w *walker) pageDialogs(ctx context.Context, st *PageState) (bool, string, string, *stop) {
	if acted, title, why, s := w.policyDialog(ctx, st); acted || why != "" || s != nil {
		return acted, title, why, s
	}
	acted, s := w.closeNotice(ctx, st)
	return acted, "", "", s
}

// closeNotice closes a dialog that offers nothing but its own close control:
// ADP's "Errors Found" after a refused step stands over the whole form, so
// the page under it is read and filled again only once it is closed.
func (w *walker) closeNotice(ctx context.Context, st *PageState) (bool, *stop) {
	for _, d := range st.Dialogs {
		if d.Closer == nil || d.Blocked || d.Form || len(d.Boxes) > 0 || len(d.Options) > 0 || w.seen["notice-dialog"] >= maxNotices {
			continue
		}
		only := true
		for _, b := range d.Buttons {
			only = only && closeLabelRE.MatchString(b.Label)
		}
		if !only {
			continue
		}
		res, s := w.pressInDialog(ctx, *d.Closer)
		if s != nil || res != "pressed" {
			return false, s
		}
		w.seen["notice-dialog"]++
		w.s.walkNotes = append(w.s.walkNotes, fmt.Sprintf("notice dialog closed: %q", clip(strings.TrimSpace(d.Heading+" "+d.Text), 120)))
		if !w.sleep(ctx, 500*time.Millisecond) {
			return false, cancelledStop(StageFill)
		}
		return true, nil
	}
	return false, nil
}
