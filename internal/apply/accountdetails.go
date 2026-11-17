package apply

import (
	"context"
	"strings"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// detailsRE is how an account-details step introduces itself: the page that
// follows a sign-up and asks for the name, a phone and a consent.
var detailsRE = lazyregexp.New(`(?i)\b(?:almost there|account details|complete your (?:account|profile|registration)|just a few more details|finish (?:setting up|creating) your account)\b`)

// detailsIntroWords is the longest line, in words, that can introduce the step.
const detailsIntroWords = 12

// detailsStepBoxes reads the account-details step: its first and last name
// boxes, its phone box and its consent boxes. It is not a step when the page
// asks for a password or has no name box.
type detailsBoxes struct {
	first, last, full, phone *InputInfo
	consent                  []*InputInfo
	cont                     *Control
}

func readDetails(st *PageState) *detailsBoxes {
	// The step introduces itself in a heading, the title or a short line; a
	// notice's sentence naming "bank account details" in passing does not.
	intro := append([]string{st.Title}, st.Headings...)
	for _, l := range statementLines(st.Lines) {
		if len(strings.Fields(l)) <= detailsIntroWords {
			intro = append(intro, l)
		}
	}
	if !detailsRE.MatchString(strings.Join(intro, " ")) {
		return nil
	}
	d := &detailsBoxes{}
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		if in.Type == "password" {
			return nil
		}
		if in.Type == "checkbox" {
			d.consent = append(d.consent, in)
			continue
		}
		switch f, _ := identityFieldOf(in.Label + " " + in.Name); f {
		case gates.FieldFirstName:
			d.first = in
		case gates.FieldLastName:
			d.last = in
		case gates.FieldFullName:
			d.full = in
		case gates.FieldPhone:
			d.phone = in
		}
	}
	if d.first == nil && d.last == nil && d.full == nil {
		return nil
	}
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat || c.Chrome {
			continue
		}
		for _, l := range c.Labels {
			switch strings.ToLower(strings.TrimSpace(l)) {
			case "continue", "next":
				if d.cont == nil {
					d.cont = c
				}
			}
		}
	}
	return d
}

// detailsStep completes the account-details step when the page shows it: the
// profile's first and last name, the phone only when the step marks it
// required, the boxes the consent gate allows under the legal-declaration
// setting, and one real pointer click on Continue. A step still on screen
// afterwards is reported with the page's own alert text and is no success.
func (a *acct) detailsStep(ctx context.Context, st *PageState) (detail string, handled bool, stp *stop) {
	d := readDetails(st)
	if d == nil {
		return "", false, nil
	}
	snap, ok := a.r.applicant(ctx)
	if !ok {
		return "", true, a.fail(ClassEnvironment, "", "the account-details step needs the profile and it could not be read")
	}
	// The consent gate decides each unticked box before anything is typed, and
	// every decision is audited. The step's consent is a box the page marks
	// required, or one whose own words are a consent and no opt-in: with the
	// setting off it is the user's to tick, so nothing is typed or pressed and
	// the stop says why. An opt-in and a self-identification box are never
	// ticked.
	on := a.legalSettingOn()
	var tick []*InputInfo
	for _, c := range d.consent {
		if c.Checked {
			continue
		}
		optIn, _ := gates.LadderExclusion(gates.LadderRow{Label: c.Label})
		needed := c.Required || consentBoxRE.MatchString(c.Label) && optIn != gates.ExcludedMarketingOptIn
		g := gates.ConsentControl(gates.ConsentInput{Label: c.Label, PageMarksRequired: needed, SettingOn: on, Surface: gates.SurfaceAccountDetails})
		if s := a.audit(g); s != nil {
			return "", true, s
		}
		switch {
		case on && g.Allowed():
			tick = append(tick, c)
		case !on && needed:
			return "", true, a.fail(ClassCredential, "sign-in", "the account-details step on %s needs the consent box %q ticked and autoAcceptLegalDeclarations is off; nothing was pressed", a.host, clip(c.Label, 80))
		}
	}
	typeIf := func(in *InputInfo, v string) *stop {
		if in == nil || v == "" || in.Filled {
			return nil
		}
		return a.typeInto(ctx, in, v)
	}
	if s := typeIf(d.first, snap.Candidate.FirstName); s != nil {
		return "", true, s
	}
	if s := typeIf(d.last, snap.Candidate.LastName); s != nil {
		return "", true, s
	}
	if s := typeIf(d.full, snap.Candidate.FullName); s != nil {
		return "", true, s
	}
	if d.phone != nil && d.phone.Required {
		if s := typeIf(d.phone, snap.Candidate.Phone); s != nil {
			return "", true, s
		}
	}
	for _, c := range tick {
		if s := a.clickInput(ctx, c); s != nil {
			return "", true, s
		}
	}
	if d.cont == nil {
		return "", true, a.fail(ClassCredential, "sign-in", "the account-details step on %s has no Continue control the stage may press", a.host)
	}
	if s := a.press(ctx, d.cont); s != nil {
		return "", true, s
	}
	deadline := time.Now().Add(a.tm.Answer)
	for {
		if !sleepCtx(ctx, a.tm.Poll) {
			return "", true, cancelledStop(StageAccount)
		}
		after, err := Probe(ctx, a.s.tab)
		if err == nil && readDetails(after) == nil && !after.Loading {
			return "completed the account-details step", true, nil
		}
		if time.Now().After(deadline) {
			words := "no alert text"
			if err == nil && len(after.Validation) > 0 {
				words = strings.Join(after.Validation, " / ")
			}
			return "", true, a.fail(ClassCredential, "sign-in", "the account-details step on %s is still shown after Continue (%s); the sign-up is not a success", a.host, words)
		}
	}
}

func (a *acct) legalSettingOn() bool {
	if f := a.r.d.Page.Settings; f != nil {
		return f().AutoAcceptLegalDeclarations
	}
	return false
}

// signinAgreement ticks the box a sign-in form asks the applicant to agree to
// the site's terms or privacy policy with, which the site needs before it
// answers Sign In (ByteDance: "I have read and agree to the User Agreement
// and Applicant Privacy Policy", unmarked, and the press sent nothing). The
// consent gate decides it as a required declaration: ticked under the legal
// setting, else the stop says the box is the user's.
func (a *acct) signinAgreement(ctx context.Context) *stop {
	st, err := Probe(ctx, a.s.tab)
	if err != nil {
		return nil
	}
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if in.Furniture || in.Type != "checkbox" || in.Checked || !agreeTextRE.MatchString(in.Label) {
			continue
		}
		d := gates.ConsentControl(gates.ConsentInput{Label: in.Label, PageMarksRequired: true, SettingOn: a.legalSettingOn(), Surface: gates.SurfaceSigninWidget})
		if s := a.audit(d); s != nil {
			return s
		}
		if !d.Allowed() {
			return a.fail(ClassCredential, "sign-in", "the sign-in on %s asks to agree to %q first: %s; nothing was typed", a.host, clip(in.Label, 80), d.Reason)
		}
		if s := a.clickInput(ctx, in); s != nil {
			return s
		}
	}
	return nil
}

// clickInput presses a probed box with a real pointer click.
func (a *acct) clickInput(ctx context.Context, in *InputInfo) *stop {
	loc, err := Locate(ctx, a.s.tab, in.I)
	if err != nil || !loc.Found || !loc.Hit {
		return a.fail(ClassDefect, "", "the %q box could not be clicked", in.Label)
	}
	if err := a.s.tab.Click(ctx, loc.X, loc.Y); err != nil {
		return a.fail(ClassEnvironment, "", "the click failed: %v", err)
	}
	return nil
}
