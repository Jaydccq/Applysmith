package apply

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// signUpOrStop decides, when the sign-in could not be done with a stored
// password, whether an account is created. Always behind the account creation
// gate; a run no user started never creates one, and a form that is also the
// application waits for a run that submits. A site override that exists and
// failed is a credential stop, the sign-up lane not running.
func (a *acct) signUpOrStop(ctx context.Context, w *wall, why string) (string, *stop) {
	in := a.e.Run.Input
	if !in.UserStarted {
		d := gates.AccountCreation(gates.AccountInput{Demo: a.r.demo(), UserStarted: false, System: a.system})
		if st := a.audit(d); st != nil {
			return "", st
		}
		a.rec.Signup = d.Code
		return "", a.fail(ClassCredential, "sign-in", "%s; account creation needs a run you started", why)
	}
	if w == nil || (w.create == nil && !w.registration) {
		// A wall drawn in parts (Workday's "Create Account" link under its
		// sign-in form) may show the control after the first read.
		if st := a.waitFor(ctx, func(st *PageState) bool {
			nw := findWall(st)
			return nw != nil && (nw.create != nil || nw.registration)
		}); st != nil {
			w = findWall(st)
		}
	}
	if w == nil || (w.create == nil && !w.registration) {
		return "", a.fail(ClassCredential, "sign-in", "%s, and the page offers no way to create an account", why)
	}
	if st := a.signupDecision(ctx, why); st != nil {
		return "", st
	}
	detail, stp := a.signUp(ctx, w)
	if stp != nil && stp.board == boardCombined {
		// The form is also the application: nothing was saved, typed or pressed,
		// and the walk registers on it when the run submits. Sending its page is
		// what creates the account, so a dry run leaves it to a run that submits.
		if !in.Submit {
			return "", a.fail(ClassCredential, "sign-in", "the create-account form on %s is also the application, so only a run that submits creates the account on it; nothing was typed into it", a.host)
		}
		a.e.combined = &combinedAccount{host: a.host, name: a.secretName(), system: a.system, tenant: a.tenant}
		return "the create-account form is also the application; the application walk registers on it", nil
	}
	return detail, stp
}

// signupDecision is shared by password and Google registration. The Google
// preference does not erase the user-started run or a running cooldown.
// Cooldowns that cannot be read may be running, so no account is created.
func (a *acct) signupDecision(ctx context.Context, why string) *stop {
	var cooldowns []gates.Cooldown
	if pol := a.r.d.Page.Policy; pol != nil {
		var err error
		if cooldowns, err = pol.Cooldowns(ctx); err != nil {
			return a.fail(ClassEnvironment, "", "%s; the sign-up cooldowns could not be read (%v), so no account is created", why, err)
		}
	}
	a.gateIn = gates.AccountInput{
		Demo: a.r.demo(), Now: a.r.d.Now().UnixMilli(), UserStarted: a.e.Run.Input.UserStarted, System: signupKey(a.system), Host: a.host, GenericOffersCreate: true,
		Cooldowns: cooldowns, LegalSettingOn: a.legalSettingOn(),
	}
	d := gates.AccountCreation(a.gateIn)
	if st := a.audit(d); st != nil {
		return st
	}
	if !d.Allowed() {
		a.rec.Signup = d.Code
		return a.fail(ClassCredential, "sign-in", "%s; account creation is refused: %s", why, d.Reason)
	}
	return nil
}

// boardCombined marks the sign-up lane's hand-over to the application walk; it
// never reaches the runs board.
const boardCombined = "combined-account"

func signupKey(system string) string {
	for _, s := range gates.SignupSystems {
		if s == system {
			return s
		}
	}
	return "generic"
}

// secretName is where a new account's password is stored: the tenant key on a
// named system, the host's site override on a generic one.
func (a *acct) secretName() string {
	if credentials.IsNamed(a.system) && a.tenant != "" {
		return "tenant-" + strings.ToLower(a.system) + "-" + strings.ToLower(a.tenant)
	}
	return "signin-site-" + a.host
}

// signUp creates an account on the page's create-account form and signs in.
// The password is written to the credential store only after a create-account
// form has been found, and removed again when the form was never sent (a box
// could not be typed into, the create control was not pressed) or the site
// refuses the submit; it is kept when the form disappeared with no success
// marker, because the account possibly exists. The sign-in that follows is the verdict, never the
// sign-up's own success message.
func (a *acct) signUp(ctx context.Context, w *wall) (string, *stop) {
	creds := a.r.d.Page.Credentials
	// Open the create-account form when it is behind a control; a page that
	// already registers an account is the form.
	if !w.registration {
		if st := a.press(ctx, w.create); st != nil {
			return "", st
		}
	}
	form, done, stp := a.waitForm(ctx)
	if stp != nil || done != "" {
		return done, stp
	}
	if phoneVerifyRE.MatchString(strings.Join(statementLines(form.page.Lines), "\n")) {
		return "", a.fail(ClassCredential, "sign-in", "the create-account form on %s asks to verify a phone by text message, which is yours to do; nothing was typed", a.host)
	}
	if form.combined {
		return "", &stop{class: ClassCredential, board: boardCombined, reason: "the create-account form is also the application (it asks for more than an email and a password); nothing was typed", at: StageAccount}
	}
	if form.agreement() {
		in := a.gateIn
		in.RequiredAgreementOnForm = true
		d := gates.AccountCreation(in)
		if st := a.audit(d); st != nil {
			return "", st
		}
		if !d.Allowed() {
			return "", a.fail(ClassCredential, "sign-in", "%s", d.Reason)
		}
	}
	email := a.accountEmail()
	if email == "" {
		return "", a.fail(ClassCredential, "sign-in", "the profile holds no email to register with")
	}
	name := a.secretName()
	pw, created, err := creds.GenerateFor(name, form.maxLen)
	if err != nil {
		return "", a.fail(ClassEnvironment, "", "a password could not be prepared: %v", err)
	}
	// refuse removes a password this attempt created and ends the stage.
	refuse := func(st *stop) (string, *stop) {
		if created {
			_ = creds.Remove(ctx, name)
		}
		return "", st
	}
	if err := a.recordAttempt(ctx); err != nil {
		return refuse(a.fail(ClassEnvironment, "", "the sign-up attempt could not be recorded: %v", err))
	}
	start := time.Now()
	for _, in := range form.emails {
		if st := a.typeInto(ctx, in, email); st != nil {
			return refuse(st)
		}
	}
	for _, in := range form.passwords {
		if st := a.typeSecret(ctx, in, pw); st != nil {
			return refuse(st)
		}
	}
	if st := a.typeProfile(ctx, form, email); st != nil {
		return refuse(st)
	}
	a.chooseCountries(ctx, form)
	if st := a.agree(ctx, form); st != nil {
		return refuse(st)
	}
	submit := form.submit
	// Evidence point 6: a create-account form with a widget is solved before the
	// submit press.
	if st, err := Probe(ctx, a.s.tab); err == nil {
		if d := DetectChallenge(st); d != nil && unsolved(st, d) {
			if _, ok := a.r.solveAt(ctx, a.e, a.s, d, gates.PointGenericSignup); !ok {
				return refuse(a.fail(ClassCredential, "challenge", "a captcha stands on the create-account form and is yours; nothing was submitted"))
			}
		}
		// Typing can redraw the submit, and the handle read before typing then
		// names a detached element; the same form read again names the new one.
		if f := readSignupForm(st); f != nil && f.submit != nil && len(f.passwords) > 0 && f.passwords[0].I == form.passwords[0].I {
			submit = f.submit
		}
	}
	if !a.dwell(ctx, start) {
		return refuse(cancelledStop(StageAccount))
	}
	pre, _ := Signature(ctx, a.s.tab)
	if st := a.press(ctx, submit); st != nil {
		return refuse(st)
	}
	// A form still shown afterwards means the site refused the submit.
	after := a.afterSignup(ctx, pre)
	if after.phone {
		// The account may exist: the password stays saved for the user.
		return "", a.fail(ClassCredential, "sign-in", "the sign-up on %s asks to verify a phone by text message, which is yours to do; the generated password is saved for the site", a.host)
	}
	if after.formStill {
		if after.exists {
			// The tenant says the account exists: that is the stage's own verdict
			// that the stored password does not match, and a reset may follow.
			if created {
				_ = creds.Remove(ctx, name)
			}
			if detail, stp, ran := a.tryReset(ctx, true); ran {
				return detail, stp
			}
			return "", a.fail(ClassCredential, "sign-in", "an account already exists on %s and the stored password does not match it", a.host)
		}
		return refuse(a.fail(ClassCredential, "sign-in", "no account was created on %s: the create-account form is still shown%s", a.host, after.why))
	}
	_ = creds.Created(ctx, a.host, a.system, a.tenant, email, name)
	cand := credentials.Candidate{Label: "generated", Name: name, Kind: credentials.KindSite, Email: email, Password: pw}
	if credentials.IsNamed(a.system) {
		cand.Kind = credentials.KindTenant
	}
	a.rec.Tried = append(a.rec.Tried, "created-account")
	if after.sentPage {
		return a.verify(ctx, nil, cand, gates.PreconditionSentPage, after.pressedAt)
	}
	if after.detailsShown {
		st, perr := Probe(ctx, a.s.tab)
		if perr != nil {
			return "", a.fail(ClassEnvironment, "", "the page could not be read after the sign-up: %v", perr)
		}
		if _, _, stp := a.detailsStep(ctx, st); stp != nil {
			return "", stp
		}
	}
	if after.codeShown {
		a.emailCode(ctx, after.pressedAt)
	}
	// The verifying sign-in.
	noMarker := ""
	if !after.marker {
		noMarker = " showed no success marker and"
	}
	w2, signedIn, stp := a.settle(ctx)
	if stp != nil {
		return "", stp
	}
	if w2 != nil {
		res, stp := a.attempt(ctx, w2, cand)
		if stp != nil {
			return "", stp
		}
		switch res {
		case attemptSignedIn:
		case attemptVerify:
			return a.verify(ctx, w2, cand, gates.PreconditionBanner, time.Time{})
		default:
			return "", a.fail(ClassCredential, "sign-in", "the sign-up on %s%s the sign-in that followed was refused; the account is not known to work", a.host, noMarker)
		}
	} else if !signedIn {
		return "", a.fail(ClassCredential, "sign-in", "the sign-up on %s%s did not lead to an application page", a.host, noMarker)
	}
	a.rec.Succeeded = "created-account"
	return "created an account and signed in", nil
}

// accountEmail is the applicant's own email, from the candidate list of the
// store (every candidate types it) or the applicant snapshot.
func (a *acct) accountEmail() string {
	for _, c := range a.cands {
		if c.Email != "" {
			return c.Email
		}
	}
	snap, _ := a.r.applicant(context.Background())
	return snap.Candidate.Email
}

func (a *acct) recordAttempt(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	return a.r.d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO signup_attempts (system, host, run_id, at) VALUES (?, ?, ?, ?)`,
			signupKey(a.system), a.host, a.e.Run.ID, a.r.d.Now().UTC().Format(stampLayout))
		return err
	})
}

// signupForm is a create-account form read from a probe.
type signupForm struct {
	emails, passwords []*InputInfo
	// countries are the required country selects.
	countries []*InputInfo
	// terms is the link that opens the terms-and-privacy dialog; boxes are the
	// required agreement checkboxes.
	terms  *Control
	boxes  []*InputInfo
	submit *Control
	maxLen int
	// page is the probe the form was read from.
	page *PageState
	// profile are the name, phone and username boxes a create-account form
	// asks for beside the credentials; the profile answers them.
	profile []profileBox
	// combined is true when the form asks for more than the account needs, or
	// holds a résumé upload or an application Submit.
	combined bool
}

// profileBox is a create-account box the applicant's own profile answers.
type profileBox struct {
	in    *InputInfo
	field gates.IdentityField
}

// fieldUsername is a username the form asks for beside the email; it is the
// email's local part, never an invented name.
const fieldUsername gates.IdentityField = "username"

// agreement reports whether the form requires the applicant to agree to
// something.
func (f *signupForm) agreement() bool { return f.terms != nil || len(f.boxes) > 0 }

var (
	agreeTextRE = lazyregexp.New(`(?i)\b(?:agree|accept|consent|acknowledge)\b[^.]{0,80}\b(?:terms|privacy|conditions)\b`)
	// termsRE names the document a terms link opens; a notice's own toggle
	// ("Read Full Privacy Message") names none.
	termsRE       = lazyregexp.New(`(?i)\b(?:terms|conditions|privacy\s+(?:policy|notice|statement))\b`)
	agreeBoxRE    = lazyregexp.New(`(?i)\b(?:terms|privacy|conditions|agree|accept|acknowledge|consent)\b`)
	consentBoxRE  = lazyregexp.New(`(?i)\b(?:consent|agree|accept|acknowledge|declaration|certify|authori[sz]e)`)
	acceptRE      = lazyregexp.New(`(?i)^(?:i\s+)?(?:accept|agree)(?:\s+and\s+continue)?$`)
	countryRE     = lazyregexp.New(`(?i)\b(?:country|nation)\b`)
	lenSentence   = lazyregexp.New(`(?i)(?:not\s+(?:be\s+)?(?:longer|more)\s+than|no\s+(?:longer|more)\s+than|at\s+most|maximum(?:\s+of)?|max(?:imum)?\.?\s+length(?:\s+of)?|up\s+to)\s*:?\s*(\d{1,3})\s*(?:characters|chars)`)
	dialCodeRE    = lazyregexp.New(`\(?\+\d{1,4}\)?`)
	methodLaterRE = lazyregexp.New(`(?i)\bwithout\s+(?:a\s+)?(?:resume|cv)\b`)
	pasteMethodRE = lazyregexp.New(`(?i)^\s*copy\s*(?:&|and)\s*paste\s*$`)
	// getCodeRE is a create-account form's control that mails a code to the
	// address typed beside it.
	getCodeRE  = lazyregexp.New(`(?i)^(?:get|send|request)\s+(?:a\s+|the\s+)?(?:verification\s+|email\s+)?code$`)
	usernameRE = lazyregexp.New(`(?i)\buser\s*(?:name|id)\b`)
	// ssoRE is a control that hands the sign-up to another identity provider;
	// it is never a create-account form's submit.
	ssoRE = lazyregexp.New(`(?i)\b(?:google|facebook|apple|microsoft|linkedin|github|okta|sso|single[- ]sign[- ]on)\b|continue\s+with\b`)
	// regSubmitRE is a registration form's plain submit (Auth0's "Continue"),
	// taken only when the form itself shows it creates an account.
	regSubmitRE = lazyregexp.New(`(?i)^(?:continue|submit|next|create)$`)
	// A box that only says yes to a statement binding account creation to it, or
	// to the policy the form names, is the form's agreement even unmarked
	// (Workday's createAccountCheckbox: "By creating this account, you agree",
	// or "Candidate Privacy Policy" above a bare "I Agree"; Trend Micro's
	// "Yes, I have read and consent to the above notices.").
	bareAgreeRE = lazyregexp.New(`(?i)^(?:yes,?\s+)?i\s+(?:have\s+read\s+and\s+)?(?:agree|accept|consent)(?:\s+to\s+the\s+above(?:\s+(?:notices?|terms|polic(?:y|ies)|statements?))?)?\.?$`)
	// ownAgreeRE is a box whose own words agree to creating the account
	// (Workday's own tenant: "I agree to creating this account to allow me to
	// apply") or say the named policy was read: the account's agreement, with
	// or without a required mark.
	ownAgreeRE = lazyregexp.New(`(?i)\bi\s+(?:agree|consent)\s+to\s+(?:the\s+)?(?:creat(?:e|ing|ion\s+of))\s+(?:this|an|my)\s+account\b|` +
		// Expedia's Workday: "I have read the ... Privacy Policy." with no mark.
		`\bi\s+have\s+read\b[^.]{0,80}\b(?:privacy\s+(?:policy|notice|statement)|terms\s+(?:and\s+conditions|of\s+(?:use|service)))\b|` +
		// Red Hat's Workday: "By clicking the checkbox I consent to the above
		// terms ...", unmarked; the page refuses the account without it.
		`\bby\s+(?:clicking|checking|ticking|selecting)\s+(?:on\s+)?(?:this|the)\s+(?:check\s*)?box,?\s+i\s+(?:consent|agree|accept|acknowledge)\s+(?:to\s+)?(?:the\s+)?(?:above|terms|privacy|conditions)\b|` +
		// P&G's Workday: "I understand and accept the terms described above and
		// in P&G's Global Candidate Privacy Notice.", unmarked.
		`\bi\s+(?:understand\s+and\s+)?(?:accept|agree\s+to)\s+the\s+terms\s+(?:(?:described|set\s+out|stated|outlined)\s+)?above\b|` +
		// Target's Workday: "By continuing, you agree to the above Career
		// Privacy Notice", unmarked; the page refuses the account without it.
		`\bby\s+(?:continuing|proceeding),?\s+(?:you|i)\s+(?:agree|consent)\s+to\s+the\s+above\s+(?:[a-z]+\s+){0,3}(?:privacy\s+(?:policy|notice|statement)|terms(?:\s+(?:and\s+conditions|of\s+(?:use|service)))?)\b`)
	createAgreeRE = lazyregexp.New(`(?i)\bby\s+creating\s+(?:(?:this|an|your)\s+account\b[^.]{0,40}\byou|a\s+profile\b[^.]{0,60}\bi)\s+(?:agree|consent|acknowledge)|\b(?:privacy\s+(?:policy|notice|statement)|terms\s+(?:and\s+conditions|of\s+(?:use|service)))\b`)
)

// profileField reads a box the profile answers on a create-account form: a
// first, last or full name, a phone or a separate username.
func profileField(in *InputInfo) gates.IdentityField {
	if in.Tag != "input" || (in.Type != "" && in.Type != "text" && in.Type != "tel") {
		return ""
	}
	ac := strings.ToLower(in.Autocomplete)
	switch {
	case ac == "username" || usernameRE.MatchString(in.Label+" "+in.Name+" "+in.ID):
		return fieldUsername
	case in.Type == "tel" || ac == "tel":
		return gates.FieldPhone
	case ac == "given-name":
		return gates.FieldFirstName
	case ac == "family-name":
		return gates.FieldLastName
	case ac == "name":
		return gates.FieldFullName
	}
	switch f, _ := identityFieldOf(in.Label); f {
	case gates.FieldFirstName, gates.FieldLastName, gates.FieldFullName, gates.FieldPhone:
		return f
	}
	return ""
}

// submitsForm reports a control that submits its form: a button with no type
// inside a form, a button of type submit, or a submit input.
func submitsForm(c *Control) bool {
	return (c.Tag == "button" && (c.Type == "" && c.Form >= 0 || c.Type == "submit")) || (c.Tag == "input" && c.Type == "submit")
}

// policyMaxLen reads a password policy sentence such as "must not be longer
// than 20 characters"; 0 when the page states none.
func policyMaxLen(text string) int {
	if m := lenSentence.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// waitForm reads the create-account form, polling until it shows. On Avature's
// application-method choice it presses only the method marked "without
// resume". A page that is not a form but is the account-details step is
// completed instead, and a page that never becomes a form leaves a snapshot
// of what stood there.
func (a *acct) waitForm(ctx context.Context) (*signupForm, string, *stop) {
	deadline := time.Now().Add(a.tm.Answer)
	chose, coded, settled := false, false, 0
	for time.Now().Before(deadline) {
		st, err := Probe(ctx, a.s.tab)
		if err == nil {
			switch {
			case codeSignup(st) && !coded:
				if stp := a.codeSignUp(ctx, st); stp != nil {
					return nil, "", stp
				}
				coded, deadline = true, time.Now().Add(a.tm.Answer)
				continue
			case codeSignup(st):
				// The code form may stay a moment after Continue.
				settled = 0
				if !sleepCtx(ctx, a.tm.Poll) {
					return nil, "", cancelledStop(StageAccount)
				}
				continue
			}
			if f := readSignupForm(st); f != nil {
				return f, "", nil
			}
			if d, handled, stp := a.detailsStep(ctx, st); handled {
				return nil, d, stp
			}
			if coded {
				// The mailed code created the account once the page after it has
				// stood for two reads; the gate re-entry signs in.
				if settled++; settled >= 2 {
					return nil, "created an account with the code mailed to the address", nil
				}
			}
			if !chose {
				if c := methodLater(st); c != nil {
					chose = true
					if stp := a.pressFlow(ctx, c); stp != nil {
						return nil, "", stp
					}
				}
			}
			if !chose && pasteMethod(st) {
				// Avature's method choice with no method that skips the résumé
				// (Deloitte: Copy & Paste or From Device): its Register step asks
				// for the password beside the application's own fields.
				return nil, "", &stop{class: ClassCredential, board: boardCombined, reason: "the create-account fields sit inside the application, after its résumé step; nothing was typed", at: StageAccount}
			}
		}
		if !sleepCtx(ctx, a.tm.Poll) {
			return nil, "", cancelledStop(StageAccount)
		}
	}
	a.s.snapshot(ctx, "pre-form")
	if coded {
		return nil, "", a.fail(ClassCredential, "one-time-code", "the code mailed by %s was typed but its create-account form stayed", a.host)
	}
	return nil, "", a.fail(ClassCredential, "sign-in", "no create-account form appeared on %s", a.host)
}

// codeSignup reports a create-account form that asks for a code mailed to the
// address, with its own send-code control (ByteDance: Email, "Enter Email
// Verification Code" beside Get code, then Continue). A password box beside it
// changes nothing: ByteDance draws the view over its sign-in form, whose boxes
// stay in the page.
func codeSignup(st *PageState) bool {
	email, code := false, false
	for i := range st.Inputs {
		in := &st.Inputs[i]
		switch {
		case in.Furniture:
		case isCodeBox(in.Label + " " + in.Placeholder):
			code = true
		case in.Type == "email" || strings.Contains(strings.ToLower(in.Label+in.Name+in.Autocomplete), "mail"):
			email = true
		}
	}
	return email && code && slices.ContainsFunc(st.Controls, func(c Control) bool {
		return !c.Chat && slices.ContainsFunc(c.Labels, func(l string) bool { return getCodeRE.MatchString(strings.TrimSpace(l)) })
	})
}

// methodLater finds the application-method control marked as "without
// resume": the only method the stage presses, never an upload or a
// third-party identity method.
func methodLater(st *PageState) *Control {
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat || c.Chrome {
			continue
		}
		for _, l := range c.Labels {
			if methodLaterRE.MatchString(l) {
				return c
			}
		}
	}
	return nil
}

// pasteMethod reports an application-method choice that offers to paste the
// résumé ("Copy & Paste" beside "From Device").
func pasteMethod(st *PageState) bool {
	return slices.ContainsFunc(st.Controls, func(c Control) bool {
		return !c.Chat && !c.Chrome && slices.ContainsFunc(c.Labels, func(l string) bool { return pasteMethodRE.MatchString(l) })
	})
}

// readSignupForm reads email and password boxes (a second one of either is the
// retype box), the name, phone and username boxes the profile answers, and the
// control that creates the account. A form that shows it registers (a
// new-password box, a retype password box or a name box) is read even beside
// its own "Log in" toggle, and its submit may be a plain Continue.
func readSignupForm(st *PageState) *signupForm {
	f := &signupForm{maxLen: policyMaxLen(st.Text), page: st}
	other := 0
	newPassword := false
	taken := map[gates.IdentityField]bool{}
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if in.Furniture {
			continue
		}
		low := strings.ToLower(in.Label + in.Name + in.Autocomplete)
		switch {
		case in.Type == "password":
			f.passwords = append(f.passwords, in)
			if in.MaxLength > 0 && (f.maxLen == 0 || in.MaxLength < f.maxLen) {
				f.maxLen = in.MaxLength
			}
			newPassword = newPassword || strings.Contains(strings.ToLower(in.Autocomplete), "new-password")
		case in.Type == "checkbox":
			// Before the email case: SuccessFactors names its job-alert box
			// fbclc_emailEnabled.
			if agreeBoxRE.MatchString(in.Label) && (in.Required || ownAgreeRE.MatchString(in.Label) || bareAgreeRE.MatchString(strings.TrimSpace(in.Label)) && createAgreeRE.MatchString(st.Text)) {
				f.boxes = append(f.boxes, in)
			}
		case in.Type == "email" || strings.Contains(low, "mail"):
			f.emails = append(f.emails, in)
		case in.Type == "file":
			f.combined = true
		case profileField(in) != "" && !taken[profileField(in)]:
			taken[profileField(in)] = true
			f.profile = append(f.profile, profileBox{in: in, field: profileField(in)})
		case in.Tag == "select" && countryRE.MatchString(in.Label+" "+in.Name):
			if in.Required {
				f.countries = append(f.countries, in)
			}
		default:
			other++
		}
	}
	if len(f.passwords) == 0 || len(f.emails) == 0 {
		return nil
	}
	if other > 0 {
		f.combined = true
	}
	registers := newPassword && !offersForgot(st) || len(f.passwords) > 1 || taken[gates.FieldFirstName] || taken[gates.FieldLastName] || taken[gates.FieldFullName]
	var plain, signin *Control
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat {
			continue
		}
		if submitsForm(c) && gates.IsFinalApply(c.Labels) {
			f.combined = true // the form's own submit applies (SuccessFactors Quick Apply)
		}
		for _, l := range c.Labels {
			low := strings.ToLower(l)
			if signinBtnRE.MatchString(l) && signin == nil {
				signin = c
			}
			if strings.Contains(low, "submit application") {
				f.combined = true
			}
			switch {
			case ssoRE.MatchString(l):
			case signupBtnRE.MatchString(l) && f.submit == nil:
				f.submit = c
			case regSubmitRE.MatchString(strings.TrimSpace(l)) && plain == nil && !c.Chrome:
				plain = c
			case termsRE.MatchString(l) && f.terms == nil && !c.Chrome && len(l) < 60 && agreeTextRE.MatchString(st.Text) && !leavesPage(c):
				f.terms = c
			}
		}
	}
	// A terms link inside an agreement box's own caption ("I agree to <Boeing
	// Applicant Privacy Policy>", Taleo Business Edition) shows that box's
	// document, in a window of its own; ticking the box is the agreement.
	if f.terms != nil {
		for _, b := range f.boxes {
			for _, l := range f.terms.Labels {
				if l = strings.TrimSpace(l); l != "" && strings.Contains(strings.ToLower(b.Label), strings.ToLower(l)) {
					f.terms = nil
				}
			}
			if f.terms == nil {
				break
			}
		}
	}
	if f.submit == nil && registers {
		f.submit = plain
	}
	if f.submit == nil {
		return nil
	}
	// A page with a sign-in control is the sign-in wall, unless the form shows
	// it registers or its own submit creates the account while sign-in is
	// only a link outside it.
	if signin != nil && !registers {
		pw := f.passwords[0]
		if pw.Form < 0 || f.submit.Form != pw.Form || !submitsForm(f.submit) || (signin.Form == pw.Form && submitsForm(signin)) {
			return nil
		}
	}
	return f
}

// typeProfile types the profile's own name, phone and username into the boxes
// a create-account form asks for. A value the profile does not hold is left
// for the site to refuse.
func (a *acct) typeProfile(ctx context.Context, f *signupForm, email string) *stop {
	if len(f.profile) == 0 {
		return nil
	}
	snap, _ := a.r.applicant(ctx)
	c := snap.Candidate
	for _, b := range f.profile {
		v := ""
		switch b.field {
		case gates.FieldFirstName:
			v = c.FirstName
		case gates.FieldLastName:
			v = c.LastName
		case gates.FieldFullName:
			v = firstNonEmpty(c.FullName, strings.TrimSpace(c.FirstName+" "+c.LastName))
		case gates.FieldPhone:
			v = c.Phone
		case fieldUsername:
			v = usernameFrom(email)
		}
		if v == "" || b.in.Filled {
			continue
		}
		if st := a.typeInto(ctx, b.in, v); st != nil {
			return st
		}
	}
	return nil
}

var usernameDropRE = lazyregexp.New(`[^a-z0-9_.-]`)

// usernameFrom is the email's local part, lowercased and kept to the
// characters username boxes take.
func usernameFrom(email string) string {
	local, _, _ := strings.Cut(email, "@")
	return firstNonEmpty(usernameDropRE.ReplaceAllString(strings.ToLower(local), ""), "user")
}

// chooseCountries picks, in each required country select, the option that
// names the profile's country, by name or code and ignoring a dialling code:
// a profile's "US" is "United States (+1)", never "United States Minor
// Outlying Islands". A select with no such option is left alone.
func (a *acct) chooseCountries(ctx context.Context, f *signupForm) {
	snap, ok := a.r.applicant(ctx)
	if !ok {
		return
	}
	want, known := match.CountryCode(snap.Candidate.Address.Country)
	if !known {
		return
	}
	for _, sel := range f.countries {
		for i, o := range sel.Options {
			if code, ok := match.CountryCode(normCountry(o)); ok && code == want {
				_, _ = ChooseOption(ctx, a.s.tab, sel.I, i)
				break
			}
		}
	}
}

func normCountry(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(dialCodeRE.ReplaceAllString(s, " ")), " "))
}

// leavesPage reports a link to another document: it opens no terms dialog on
// this page (CVS's privacy-policy link opens a new tab).
func leavesPage(c *Control) bool {
	h := strings.ToLower(strings.TrimSpace(c.Href))
	return c.Tag == "a" && h != "" && !strings.HasPrefix(h, "#") && !strings.HasPrefix(h, "javascript:")
}

// agree handles the agreements a create-account form requires, only under the
// user's legal-declaration setting (the gate has already refused the sign-up
// without it): required agreement boxes are ticked, and the terms link opens a
// dialog whose Accept control, one that was not on screen before, is pressed.
// A dialog with no such control fails the submit.
func (a *acct) agree(ctx context.Context, f *signupForm) *stop {
	if !f.agreement() {
		return nil
	}
	for _, b := range f.boxes {
		if !b.Checked {
			if st := a.clickInput(ctx, b); st != nil {
				return st
			}
		}
	}
	if f.terms == nil {
		return nil
	}
	before, err := Probe(ctx, a.s.tab)
	if err != nil {
		return a.fail(ClassEnvironment, "", "the page could not be read before the terms dialog: %v", err)
	}
	was := map[string]bool{}
	for _, c := range before.Controls {
		was[strings.ToLower(strings.Join(c.Labels, "|"))] = true
	}
	if st := a.pressFlow(ctx, f.terms); st != nil {
		return st
	}
	deadline := time.Now().Add(a.tm.Answer)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, a.tm.Poll) {
			return cancelledStop(StageAccount)
		}
		st, err := Probe(ctx, a.s.tab)
		if err != nil {
			continue
		}
		for i := range st.Controls {
			c := &st.Controls[i]
			if was[strings.ToLower(strings.Join(c.Labels, "|"))] || c.Disabled {
				continue
			}
			for _, l := range c.Labels {
				if acceptRE.MatchString(strings.TrimSpace(l)) {
					return a.pressFlow(ctx, c)
				}
			}
		}
	}
	return a.fail(ClassCredential, "sign-in", "the terms dialog on %s shows no Accept control; the sign-up was not submitted", a.host)
}

type afterSignup struct {
	formStill bool
	// exists is true when the refusal says the account already exists.
	exists   bool
	sentPage bool
	// marker is true when the page said the account was created or its email
	// confirmation was sent.
	marker bool
	// detailsShown and codeShown say the sign-up led to an account-details step
	// or to a form that takes an emailed code.
	detailsShown, codeShown bool
	// phone is true when the page asks for a code sent to a phone.
	phone bool
	// why is what the page said when it kept the form, in the walk's words.
	why       string
	pressedAt time.Time
}

// phoneVerifyRE is a page that asks for a code sent to a phone by text
// message: the run has no lane for it.
var phoneVerifyRE = lazyregexp.New(`(?i)enter\s+(?:the\s+)?code\s+(?:we\s+)?sent\s+(?:to\s+)?your\s+phone|\bsms\s+verification\b|\btext\s+message\s+verification\b|\bverify\s+your\s+phone\b`)

var welcomeBackRE = lazyregexp.New(`(?i)\bwelcome\s+back\b`)

var createdRE = lazyregexp.New(`(?i)\b(?:account\s+(?:has\s+been\s+)?created|thank\s+you\s+for\s+(?:registering|signing\s+up)|registration\s+(?:is\s+)?complete|welcome\s+to)\b`)

// afterSignup reads the page the sign-up press led to.
func (a *acct) afterSignup(ctx context.Context, pre string) afterSignup {
	out := afterSignup{pressedAt: a.r.d.Now()}
	deadline := time.Now().Add(a.tm.Answer)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, a.tm.Poll) {
			break
		}
		st, err := Probe(ctx, a.s.tab)
		if err != nil {
			continue
		}
		text := strings.Join(statementLines(st.Lines), "\n")
		if phoneVerifyRE.MatchString(text) {
			out.phone = true
			return out
		}
		if sentPageRE.MatchString(text) {
			out.sentPage, out.marker = true, true
			return out
		}
		out.marker = createdRE.MatchString(text)
		if readDetails(st) != nil {
			out.detailsShown = true
			return out
		}
		if boxes, _ := codeBoxes(st); len(boxes) > 0 && verifyRE.MatchString(text+" "+strings.Join(st.Headings, " ")) {
			out.codeShown = true
			return out
		}
		changed := false
		if sig, _ := Signature(ctx, a.s.tab); sig != pre {
			changed = true
		}
		if readSignupForm(st) == nil && !st.Loading {
			// emploive answers an email it already holds with no error at all:
			// it swaps back to its sign-in tab headed "Welcome back".
			if w := findWall(st); w != nil && w.password != nil && !w.registration && welcomeBackRE.MatchString(st.Text) {
				out.formStill, out.exists = true, true
			}
			return out // the form is gone
		}
		if changed && (len(st.Validation) > 0 || badLoginRE.MatchString(text)) {
			out.formStill, out.why = true, validationWords(st)
			out.exists = existsRE.MatchString(text + " " + strings.Join(st.Validation, " "))
			return out
		}
	}
	// Nothing moved before the deadline: the form is still shown. A tenant that
	// checked the address before the press (TikTok: "This email account is
	// already in use") has answered already.
	if st, err := Probe(ctx, a.s.tab); err == nil && readSignupForm(st) != nil {
		out.formStill, out.why = true, validationWords(st)
		out.exists = existsRE.MatchString(strings.Join(st.Validation, " "))
	}
	return out
}
