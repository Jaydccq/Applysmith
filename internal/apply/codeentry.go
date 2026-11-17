package apply

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// emailCode is the emailed-code recovery after a generic sign-up that landed
// on a form that takes a code: it reopens the gate URL (waiting for the
// boxes), reads the code from mail newer than the sign-up, types it only when
// the number of boxes equals its length (one box takes the whole code),
// presses the page's own verify control and leaves the verdict to the sign-in
// that follows. A recovery that fails is noted and never hidden: the ordinary
// sign-in decides.
func (a *acct) emailCode(ctx context.Context, signedUpAt time.Time) {
	note := func(why string) {
		a.e.Run.Warnings = append(a.e.Run.Warnings, "email-code recovery on "+a.host+": "+why)
	}
	codes := a.r.d.Page.Codes
	if codes == nil {
		note("no mail lookup is configured")
		return
	}
	if _, err := a.s.tab.Navigate(ctx, a.s.entry.URL); err != nil {
		note("the gate page could not be reopened: " + err.Error())
		return
	}
	var boxes []*InputInfo
	deadline := time.Now().Add(a.tm.Boxes)
	for {
		if st, err := Probe(ctx, a.s.tab); err == nil {
			if boxes, _ = codeBoxes(st); len(boxes) > 0 {
				break
			}
		}
		if !time.Now().Before(deadline) || !sleepCtx(ctx, a.tm.Poll) {
			note("no code boxes stood on the reopened page")
			return
		}
	}
	if err := a.audit(gates.CodeRequest(gates.CodeRequestInput{})); err != nil {
		note(err.Error())
		return
	}
	tm := a.r.d.Page.Code
	tm.Poll, tm.GiveUp = a.tm.CodePoll, a.tm.MailWait
	code, _, err := a.e.codeLane().Lookup(ctx, codes, tm, a.r.d.Now, CodeAnchor(signedUpAt), "", fitsBoxes(len(boxes)))
	switch {
	case errors.Is(err, ErrCodeBoxes):
		note("the mailed code does not fit the boxes; the mail stays available")
		return
	case err != nil:
		if mailNotConnected(err) {
			a.rec.Mail = MailNotConnected
		}
		note(err.Error())
		return
	}
	if err := typeCode(ctx, a.s.tab, boxes, code); err != nil {
		note("the code could not be typed: " + err.Error())
		return
	}
	st, err := Probe(ctx, a.s.tab)
	if err != nil {
		note("the page could not be read after the code was typed")
		return
	}
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Disabled || c.Chat || c.Chrome {
			continue
		}
		for _, l := range c.Labels {
			if strings.EqualFold(l, "verify") || strings.EqualFold(l, "continue") {
				if stp := a.press(ctx, c); stp != nil {
					note(stp.reason)
				}
				return
			}
		}
	}
	note("the page has no verify control the stage may press")
}

// An email-first sign-in (Eightfold's sign-in dialog) asks for the email
// alone, then mails a code to an address it knows and offers to create an
// account for one it does not. It has no password box; one that appears after
// Continue is the password sign-in's.

var (
	emailNextRE = lazyregexp.New(`(?i)^(?:continue|next)$`)
	// dialogSendRE is the control that sends a later step of the dialog: its
	// review before a new account, its code.
	dialogSendRE = lazyregexp.New(`(?i)^(?:continue|next|submit|verify)$`)
	// unknownEmailRE is the dialog saying it holds no account for the address
	// ("We don't recognize this email").
	unknownEmailRE = lazyregexp.New(`(?i)\b(?:don['’]?t|do\s+not|can['’]?t|cannot|couldn['’]?t|could\s+not)\s+(?:recogni[sz]e|find)\b[^.]{0,40}\b(?:e-?mail|account|address)\b|\bno\s+account\s+(?:was\s+)?found\b`)
	// leastUseRE is the data-use option that consents least: the profile used
	// only for the role applied for.
	leastUseRE = lazyregexp.New(`(?i)\bonly\b[^.]{0,40}\b(?:role|job|position)\b`)
)

// emailStep is the step that takes the email: one email box and nothing else
// to fill, and its Continue, under a heading that signs in or creates an
// account.
type emailStep struct {
	email *InputInfo
	next  *Control
}

func readEmailStep(st *PageState) *emailStep {
	if !googleWallContext(st) && !bareSignInGate(st) {
		return nil
	}
	g := &emailStep{next: stepControl(st, emailNextRE)}
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		if g.email != nil || in.Type != "email" && !emailLabelRE.MatchString(in.Label+" "+in.Name+" "+in.Autocomplete) {
			return nil
		}
		g.email = in
	}
	if g.email == nil || g.next == nil {
		return nil
	}
	return g
}

// reviewStep is the step the dialog shows before it creates an account
// ("Please review before continuing"): boxes and choices in the dialog,
// nothing to type, and its Submit. A required box is the account's agreement;
// a radio group is the data-use choice; an optional box (job alerts) is never
// ticked. An application page of boxes alone stands in no dialog.
type reviewStep struct {
	agree, choices []*InputInfo
}

func readReview(st *PageState) *reviewStep {
	r := &reviewStep{}
	n := 0
	for i := range st.Inputs {
		in := &st.Inputs[i]
		switch {
		// A radio is a choice, never an invitation: the data-use options name
		// the talent community they decline, which reads as furniture.
		case in.Type == "hidden", in.Furniture && in.Type != "radio":
		case !in.Modal:
			return nil
		case in.Type == "checkbox":
			n++
			if in.Required {
				r.agree = append(r.agree, in)
			}
		case in.Type == "radio":
			n++
			r.choices = append(r.choices, in)
		default:
			return nil
		}
	}
	if n == 0 || stepControl(st, dialogSendRE) == nil {
		return nil
	}
	return r
}

// stepControl is the first control whose first label re matches, enabled or
// not (a step enables its control once it is answered), never one that hands
// the sign-in to another identity provider.
func stepControl(st *PageState, re *lazyregexp.Regexp) *Control {
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat || c.Chrome || len(c.Labels) == 0 {
			continue
		}
		if l := strings.TrimSpace(c.Labels[0]); re.MatchString(l) && !ssoRE.MatchString(l) {
			return c
		}
	}
	return nil
}

// atEmailGate works an email-first sign-in: the session from the user's
// everyday Chrome first, as at every wall, then the email-first sign-in.
func (a *acct) atEmailGate(ctx context.Context, st *PageState) (string, *stop) {
	imported, stp := a.importSession(ctx, st.URL)
	if stp != nil {
		return "", stp
	}
	if imported {
		if ok, _ := a.signedIn(ctx); ok {
			a.rec.Succeeded = "session"
			return "signed in by the imported session; no email was typed", nil
		}
		if p, err := Probe(ctx, a.s.tab); err == nil && readEmailStep(p) != nil {
			st = p
		}
	}
	detail, stp := a.emailFirst(ctx, st)
	if stp != nil {
		stp.at = StageAccount
	}
	return detail, stp
}

// emailFirst signs in through an email-first sign-in: the profile's email and
// Continue, then whatever the dialog asks next, each step at most once (the
// email twice: to sign in and to sign up). A password box is the password
// sign-in's. A code is read from mail newer than the press that led to it.
// An address the site does not know gets an account through the dialog's own
// steps, behind the account creation gate. The dialog gone for the stable
// interval is the verdict.
func (a *acct) emailFirst(ctx context.Context, st *PageState) (string, *stop) {
	if cr := a.r.d.Page.Credentials; cr != nil && a.cands == nil {
		a.cands, _ = cr.Candidates(ctx, a.host, a.system, a.tenant)
	}
	email := a.accountEmail()
	if email == "" {
		return "", a.fail(ClassCredential, "sign-in", "the sign-in on %s asks for an email and the profile holds none", a.host)
	}
	var sent time.Time
	emails, created, reviewed, coded, waited := 0, false, false, false, false
	for {
		var stp *stop
		next := st
		g, r := readEmailStep(st), readReview(st)
		boxes, _ := codeBoxes(st)
		switch w := findWall(st); {
		case w != nil && w.password != nil:
			return a.signIn(ctx, w)
		case len(boxes) > 0 && coded:
			return "", a.fail(ClassVerification, "one-time-code", "the code mailed to you did not sign in on %s: its code step stands again; type a new one in the browser, then resume", a.host)
		case len(boxes) > 0:
			coded = true
			if stp = a.dialogCode(ctx, boxes, sent); stp == nil {
				next, _, stp = a.pressStep(ctx, dialogSendRE, gates.StageSigninDialog)
			}
		case g != nil && !created && emails == 1 && unknownEmailRE.MatchString(st.Text+" "+strings.Join(st.Validation, " ")):
			created = true
			if stp = a.signupDecision(ctx, a.host+" does not know your email"); stp == nil {
				if err := a.recordAttempt(ctx); err != nil {
					return "", a.fail(ClassEnvironment, "", "the sign-up attempt could not be recorded: %v", err)
				}
				next, sent, stp = a.pressStep(ctx, signupBtnRE, gates.StageAccount)
			}
		case g != nil && (emails == 0 || created && emails == 1):
			emails++
			if stp = a.typeInto(ctx, g.email, email); stp == nil {
				next, sent, stp = a.pressStep(ctx, emailNextRE, gates.StageSigninDialog)
			}
		case g != nil && !waited:
			// The dialog can redraw before it says why it stands again ("We
			// don't recognize this email", Qualcomm): its words are waited for once.
			waited = true
			if p := a.waitFor(ctx, func(p *PageState) bool {
				return readEmailStep(p) == nil || unknownEmailRE.MatchString(p.Text+" "+strings.Join(p.Validation, " "))
			}); p != nil {
				next = p
			}
		case g != nil:
			return "", a.fail(ClassCredential, "sign-in", "the sign-in on %s still asks for the email after Continue%s; sign in in the browser, then resume", a.host, validationWords(st))
		case r != nil && created && !reviewed:
			reviewed = true
			if stp = a.review(ctx, r); stp == nil {
				next, sent, stp = a.pressStep(ctx, dialogSendRE, gates.StageSigninDialog)
			}
		case r != nil && reviewed:
			return "", a.fail(ClassCredential, "sign-in", "the review in the sign-in on %s is still shown after Submit%s; nothing more was pressed", a.host, validationWords(st))
		case r != nil:
			return "", a.fail(ClassCredential, "sign-in", "the sign-in on %s asks for a review before continuing for an account this run did not create; it is yours", a.host)
		default:
			if ok, _ := a.signedIn(ctx); !ok {
				return "", a.fail(ClassCredential, "sign-in", "the email-first sign-in on %s did not lead to an application page; sign in in the browser, then resume", a.host)
			}
			how := "with the code mailed to you"
			if !coded {
				how = "with the email alone"
			}
			if created {
				a.rec.Succeeded = "created-account"
				return "created an account in the sign-in dialog and signed in " + how, nil
			}
			a.rec.Succeeded = "email-code"
			return "signed in " + how, nil
		}
		if stp != nil {
			return "", stp
		}
		st = next
	}
}

// pressStep presses the control of the step the dialog shows now, read again
// (typing or ticking can redraw it), and returns the page that answered and
// the moment of the press: a recognised step, or the dialog closed. A step
// that does not move within the answer time is returned as it stands.
func (a *acct) pressStep(ctx context.Context, re *lazyregexp.Regexp, stage gates.ControlStage) (*PageState, time.Time, *stop) {
	cur, err := Probe(ctx, a.s.tab)
	if err != nil {
		return nil, time.Time{}, a.fail(ClassEnvironment, "", "the sign-in dialog could not be read: %v", err)
	}
	c := stepControl(cur, re)
	if c == nil {
		return nil, time.Time{}, a.fail(ClassCredential, "sign-in", "the sign-in on %s shows no control the stage may press; sign in in the browser, then resume", a.host)
	}
	pre, at := cur.Fingerprint(), a.r.d.Now()
	if stp := a.pressAs(ctx, c, stage); stp != nil {
		return nil, at, stp
	}
	// A spinner while the code is sent is no step yet.
	if p := a.waitFor(ctx, func(p *PageState) bool {
		boxes, _ := codeBoxes(p)
		step := findWall(p) != nil || readEmailStep(p) != nil || readReview(p) != nil || len(boxes) > 0
		return !p.Loading && p.Fingerprint() != pre && (step || !p.Modal)
	}); p != nil {
		return p, at, nil
	}
	if cur, err = Probe(ctx, a.s.tab); err != nil {
		return nil, at, a.fail(ClassEnvironment, "", "the sign-in dialog could not be read: %v", err)
	}
	return cur, at, nil
}

// dialogCode types the code mailed after the press that led to the code step:
// only mail newer than that press, from the site's sender where it is known,
// and only a code that fits the boxes. The code is never recorded.
func (a *acct) dialogCode(ctx context.Context, boxes []*InputInfo, sent time.Time) *stop {
	codes := a.r.d.Page.Codes
	if codes == nil {
		return a.fail(ClassVerification, "one-time-code", "the sign-in on %s asks for a code mailed to you and no mail lookup is configured; type it in the browser, then resume", a.host)
	}
	if st := a.audit(gates.CodeRequest(gates.CodeRequestInput{})); st != nil {
		return st
	}
	tm := a.r.d.Page.Code
	tm.Poll, tm.GiveUp = a.tm.CodePoll, a.tm.MailWait
	code, ev, err := a.e.codeLane().Lookup(ctx, codes, tm, a.r.d.Now, CodeAnchor(sent), SenderDomainFor(a.host), fitsBoxes(len(boxes)))
	switch {
	case ctx.Err() != nil:
		return cancelledStop(StageAccount)
	case errors.Is(err, ErrCodeBoxes):
		return a.fail(ClassVerification, "one-time-code", "the code mailed to you has %d characters and the sign-in on %s has %d boxes; type it in the browser, then resume", ev.Length, a.host, len(boxes))
	case err != nil:
		a.e.noMailbox(a.host, err)
		return a.fail(ClassVerification, "one-time-code", "the sign-in on %s asks for the code mailed to you: %v; type it in the browser, then resume", a.host, err)
	}
	if err := typeCode(ctx, a.s.tab, boxes, code); err != nil {
		return a.fail(ClassDefect, "", "the mailed code could not be typed: %v", err)
	}
	return nil
}

// review answers the step before the new account. The agreement is ticked
// only under the legal setting: the account creation gate decides with the
// form's own facts before anything is touched. Each data-use choice takes the
// option that consents least; an optional box is never ticked.
func (a *acct) review(ctx context.Context, r *reviewStep) *stop {
	if len(r.agree) > 0 {
		in := a.gateIn
		in.RequiredAgreementOnForm = true
		d := gates.AccountCreation(in)
		if st := a.audit(d); st != nil {
			return st
		}
		if !d.Allowed() {
			return a.fail(ClassCredential, "sign-in", "%s", d.Reason)
		}
	}
	for _, c := range r.choices {
		if st := a.leastConsent(ctx, c); st != nil {
			return st
		}
	}
	return a.agree(ctx, &signupForm{boxes: r.agree})
}

// leastConsent chooses, in a data-use choice, its one option limited to the
// role applied for. A choice with no such option, or several, is the user's.
func (a *acct) leastConsent(ctx context.Context, in *InputInfo) *stop {
	least := func() (*Choice, int) {
		cs, err := Choices(ctx, a.s.tab, in.Selector)
		if err != nil {
			return nil, 0
		}
		var pick *Choice
		n := 0
		for i := range cs {
			if leastUseRE.MatchString(cs[i].Name) {
				pick, n = &cs[i], n+1
			}
		}
		return pick, n
	}
	pick, n := least()
	if n != 1 {
		return a.fail(ClassCredential, "sign-in", "the data-use choice %q on %s offers no single option limited to this role; nothing was submitted", clip(in.Label, 80), a.host)
	}
	if pick.Checked {
		return nil
	}
	if st := a.clickInput(ctx, &InputInfo{I: pick.I, Label: pick.Name}); st != nil {
		return st
	}
	if now, _ := least(); now == nil || !now.Checked {
		return a.fail(ClassCredential, "sign-in", "the data-use choice on %s did not take %q; nothing was submitted", a.host, clip(pick.Name, 80))
	}
	return nil
}

// codeSignUp creates an account on a form that takes a code mailed to the
// address in place of a password (ByteDance: Email, a code box beside Get
// code, Continue). It types the applicant's email, presses Get code, reads the
// code from mail that arrived after the press, types it and presses Continue;
// the page that follows is read as a sign-up's next page. Without a mailbox to
// read nothing is typed.
func (a *acct) codeSignUp(ctx context.Context, st *PageState) *stop {
	codes := a.r.d.Page.Codes
	if codes == nil {
		return a.fail(ClassCredential, "sign-in", "the create-account form on %s asks for a code mailed to your email and no mailbox is connected; nothing was typed. Connect Gmail on the Gmail page, then retry", a.host)
	}
	var email, box *InputInfo
	for i := range st.Inputs {
		in := &st.Inputs[i]
		switch {
		case in.Furniture:
		case isCodeBox(in.Label + " " + in.Placeholder):
			box = in
		case email == nil && (in.Type == "email" || strings.Contains(strings.ToLower(in.Label+in.Name+in.Autocomplete+in.Placeholder), "mail")):
			email = in
		}
	}
	send, next := stepControl(st, getCodeRE), stepControl(st, emailNextRE)
	if email == nil || box == nil || send == nil || next == nil {
		return a.fail(ClassDefect, "sign-in", "the create-account form on %s takes a mailed code but its email box, code box, Get code or Continue could not be read", a.host)
	}
	address := a.accountEmail()
	if address == "" {
		return a.fail(ClassCredential, "sign-in", "the profile holds no email to register with")
	}
	if err := a.recordAttempt(ctx); err != nil {
		return a.fail(ClassEnvironment, "", "the sign-up attempt could not be recorded: %v", err)
	}
	if stp := a.typeInto(ctx, email, address); stp != nil {
		return stp
	}
	// Get code, and Continue after the code, are enabled once their box is filled.
	enabled := func(re *lazyregexp.Regexp) *Control {
		var c *Control
		a.waitFor(ctx, func(st *PageState) bool {
			c = stepControl(st, re)
			return c != nil && !c.Disabled
		})
		return c
	}
	if send = enabled(getCodeRE); send == nil || send.Disabled {
		return a.fail(ClassDefect, "sign-in", "Get code on %s stayed disabled after the email was typed", a.host)
	}
	pressed := a.r.d.Now()
	if stp := a.pressFlow(ctx, send); stp != nil {
		return stp
	}
	if stp := a.audit(gates.CodeRequest(gates.CodeRequestInput{})); stp != nil {
		return stp
	}
	tm := a.r.d.Page.Code
	tm.Poll, tm.GiveUp = a.tm.CodePoll, a.tm.MailWait
	code, _, err := a.e.codeLane().Lookup(ctx, codes, tm, a.r.d.Now, CodeAnchor(pressed), "", fitsBoxes(1))
	if err != nil {
		if mailNotConnected(err) {
			a.rec.Mail = MailNotConnected
		}
		return a.fail(ClassCredential, "one-time-code", "the code %s mailed for its new account could not be read: %v", a.host, err)
	}
	if err := typeCode(ctx, a.s.tab, []*InputInfo{box}, code); err != nil {
		return a.fail(ClassDefect, "one-time-code", "the mailed code could not be typed: %v", err)
	}
	a.rec.Tried = append(a.rec.Tried, "created-account")
	if next = enabled(emailNextRE); next == nil || next.Disabled {
		return a.fail(ClassDefect, "one-time-code", "Continue on %s stayed disabled after the code was typed", a.host)
	}
	return a.press(ctx, next)
}

// consoleSettle is how long a page opened for its console code is read after
// it loads.
const consoleSettle = 2 * time.Second

// ConsoleReader opens a page in a tab of its own and returns what it wrote to
// its console; the run's lane tab is one.
type ConsoleReader interface {
	ConsoleLines(ctx context.Context, rawURL string) ([]string, error)
}

var (
	// consoleCodeAskRE asks for a code the employer's page prints to the
	// browser console (Metabase: "What is the application code found in the
	// browser console at https://metabase.com/jobs (to help us filter out
	// robots)"); the code's name and the address are the captures.
	consoleCodeAskRE = lazyregexp.New(`(?i)\b((?:[a-z]+\s+){0,2}(?:code|token|key|password|phrase))\b[^?]{0,80}?\bconsole\b[^?]{0,40}?(https?://[^\s)\]"'<>]+)`)
	articleRE        = lazyregexp.New(`(?i)^(?:(?:what|which|is|are|the|a|an|your|enter|please|provide)\s+)+`)
	// companyNoiseWords are the words of a company's name that name no site.
	companyNoiseWords = map[string]bool{"inc": true, "corp": true, "llc": true, "ltd": true, "the": true, "labs": true, "company": true, "technologies": true, "group": true, "com": true}
)

// ConsoleCodeAsk reads a question that asks for a code printed to the browser
// console of a page: the code's name ("application code") and the page.
func ConsoleCodeAsk(label string) (name, page string, ok bool) {
	m := consoleCodeAskRE.FindStringSubmatch(label)
	if m == nil {
		return "", "", false
	}
	name = strings.TrimSpace(articleRE.ReplaceAllString(m[1], ""))
	page = strings.TrimRight(m[2], ".,;:")
	return name, page, name != ""
}

// CodeFromConsole is the one value the console lines print after the code's
// name ("application code 🌳🍀..."); none, or two different ones, is nothing.
func CodeFromConsole(lines []string, name string) (string, bool) {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(name) + `\s*[:=\-\x{2013}\x{2014}]?\s*(\S+)`)
	found := ""
	for _, l := range lines {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		v := strings.Trim(m[1], `"'`)
		if v == "" || found != "" && found != v {
			return "", false
		}
		found = v
	}
	return found, found != ""
}

// EmployerSite reports a page on the employer's own site: its host holds a word
// of the company's name, or the board tenant the posting is listed under.
func EmployerSite(page, company, tenant string) bool {
	u, err := url.Parse(page)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	words := strings.FieldsFunc(strings.ToLower(company+" "+tenant), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for _, w := range words {
		if len(w) >= 4 && !companyNoiseWords[w] && strings.Contains(host, w) {
			return true
		}
	}
	return false
}

// consoleCodes answers an open question that asks for a code the employer's
// page prints to its browser console: the page, on the employer's own site, is
// opened in a tab of its own and the code its console prints is typed (owner
// decision: the system finds such a code itself). A page elsewhere, or a
// console that prints no single code, leaves the question.
func (s *session) consoleCodes(ctx context.Context, res *engine.PageResult, ps *pageState) {
	cr, ok := s.tab.(ConsoleReader)
	if !ok {
		return
	}
	var ws []answerWrite
	for i, r := range ps.rows {
		if !open(r) || ps.replaced(i) {
			continue
		}
		name, page, ok := ConsoleCodeAsk(r.Label)
		if !ok {
			continue
		}
		if !EmployerSite(page, s.companyName(), s.e.Run.Tenant) {
			ps.setWhy(r.Selector, "console code: "+page+" is not the employer's own site")
			continue
		}
		lines, err := cr.ConsoleLines(ctx, page)
		if err != nil {
			ps.setWhy(r.Selector, "console code: "+page+" could not be read: "+err.Error())
			continue
		}
		code, ok := CodeFromConsole(lines, name)
		if !ok {
			ps.setWhy(r.Selector, "console code: the console of "+page+" printed no single "+name)
			continue
		}
		ws = append(ws, answerWrite{idx: i, value: code, rung: "console"})
	}
	if len(ws) > 0 {
		s.writeAnswers(ctx, res.RunID, ps, ws)
	}
}
