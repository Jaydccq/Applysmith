package apply

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
)

func settingOn(d *Deps) {
	d.Page.Settings = func() Settings { return Settings{AutoAcceptLegalDeclarations: true} }
}

func signupMod(country string, extra ...func(*Deps)) func(*Deps) {
	return func(d *Deps) {
		withApplicant(country)(d)
		d.Page.Policy = fakePolicy{}
		for _, f := range extra {
			f(d)
		}
	}
}

// sfForm is the create-account form of a SuccessFactors tenant: a retype box
// for the email and the password, a policy sentence, a required country select
// and a terms link.
func sfForm(policy string, options []string, terms bool) *PageState {
	st := &PageState{
		URL: loginURL, Title: "Create account", Text: "Create account. Password must not be longer than " + policy + " characters. By creating an account you agree to the Terms and Conditions.",
		Lines: []string{"Create account"},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "email", Label: "Email", Form: -1},
			{I: 4, Tag: "input", Type: "email", Label: "Retype email", Form: -1},
			{I: 1, Tag: "input", Type: "password", Label: "Password", Form: -1},
			{I: 5, Tag: "input", Type: "password", Label: "Confirm password", Form: -1},
			{I: 6, Tag: "select", Type: "select", Label: "Country", Required: true, Options: options, Form: -1},
		},
		Controls: []Control{{I: 2, Tag: "button", Labels: []string{"Create account"}, Form: -1}},
	}
	if terms {
		st.Controls = append(st.Controls, Control{I: 7, Tag: "a", Labels: []string{"Terms and Conditions"}, Form: -1})
	}
	return st
}

// sfRig wires a sign-up whose default guess is refused, then the form f, then
// a verifying sign-in that accepts whatever was generated.
func sfRig(t *testing.T, country string, f *PageState, dialog bool, mod ...func(*Deps)) (*acctEnv, *[]string) {
	t.Helper()
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", signupMod(country, mod...))
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	events := &[]string{}
	a.tab.onClick = func(tab *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Sign in":
			pw := tab.typed[1]
			tab.typed = map[int]string{}
			if pw == "Default-Pw-1!x" {
				tab.set(loginPage("Invalid email or password"))
				return
			}
			tab.set(appPage())
		case "Create account":
			if tab.state.Title == "Create account" {
				*events = append(*events, "submit")
				tab.set(loginPage(""))
				return
			}
			tab.set(f)
		case "Terms and Conditions":
			*events = append(*events, "terms")
			if dialog {
				d := *tab.state
				d.Modal = true
				d.Controls = append(append([]Control(nil), d.Controls...), Control{I: 20, Tag: "button", Labels: []string{"Accept"}, Modal: true, Form: -1})
				tab.set(&d)
			}
		case "Accept":
			*events = append(*events, "accept")
		}
	}
	return a, events
}

var sfOptions = []string{"Canada (+1)", "United States (+1)", "United States Minor Outlying Islands"}

func TestApply_Signup_RetypeEmailMaxLengthExactCountryTermsDialog(t *testing.T) {
	t.Run("retype box, password cap, exact country and the terms dialog", func(t *testing.T) {
		a, events := sfRig(t, "United States", sfForm("20", sfOptions, true), true, settingOn)
		d, st := a.run()
		if st != nil || !strings.Contains(d, "created an account") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
		if a.tab.chosen[6] != 1 {
			t.Fatalf("country option %d: the exact name, ignoring the dialling code, is index 1", a.tab.chosen[6])
		}
		if got := strings.Join(*events, ","); got != "terms,accept,submit" {
			t.Fatalf("order %s: the dialog is accepted before the submit", got)
		}
		pw := vaultValue(t, a.v, "signin-site-careers.example.test")
		if len(pw) < 12 || len(pw) > 20 {
			t.Fatalf("password length %d under a stated maximum of 20", len(pw))
		}
	})
	t.Run("the profile's country code names the option; a select without the country is left alone", func(t *testing.T) {
		// Huntington Ingalls' SuccessFactors: the profile says "US".
		a, _ := sfRig(t, "US", sfForm("20", sfOptions, false), false)
		if _, st := a.run(); st != nil || a.tab.chosen[6] != 1 {
			t.Fatalf("chosen %v stop %+v: US is United States (+1), index 1", a.tab.chosen, st)
		}
		a, _ = sfRig(t, "United States", sfForm("20", []string{"Canada", "United States of America"}, false), false)
		if _, st := a.run(); st != nil || a.tab.chosen[6] != 1 {
			t.Fatalf("chosen %v stop %+v: the same country under another name", a.tab.chosen, st)
		}
		a, _ = sfRig(t, "United States", sfForm("20", []string{"Canada", "Mexico"}, false), false)
		if _, st := a.run(); st != nil {
			t.Fatalf("stop %+v", st)
		}
		if len(a.tab.chosen) != 0 {
			t.Fatalf("chosen %v: no option names the profile country", a.tab.chosen)
		}
	})
	t.Run("a stated maximum below the floor still gives 12 characters", func(t *testing.T) {
		if policyMaxLen("Your password must not be longer than 8 characters.") != 8 || policyMaxLen("no policy here") != 0 {
			t.Fatal("the policy sentence is not read")
		}
		a, _ := sfRig(t, "United States", sfForm("8", sfOptions, false), false)
		if _, st := a.run(); st != nil {
			t.Fatalf("stop %+v", st)
		}
		if n := len(vaultValue(t, a.v, "signin-site-careers.example.test")); n != 12 {
			t.Fatalf("password length %d, want the floor of 12", n)
		}
	})
	t.Run("the agreement needs the setting and nothing is typed without it", func(t *testing.T) {
		a, events := sfRig(t, "United States", sfForm("20", sfOptions, true), true)
		_, st := a.run()
		if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "autoAcceptLegalDeclarations") {
			t.Fatalf("stop %+v", st)
		}
		if len(*events) != 0 || a.tab.typed[0] != "" || a.tab.typed[4] != "" {
			t.Fatalf("events %v typed %v: nothing is typed or pressed once the gate refuses", *events, a.tab.typed)
		}
		if a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'account_creation' AND code = 'agreement_needs_setting'`) != 1 {
			t.Fatal("the refusal was not audited")
		}
		if _, err := a.v.Get("signin-site-careers.example.test"); err == nil {
			t.Fatal("a password was saved before the refusal")
		}
	})
	t.Run("no Accept control fails the submit and removes the saved password", func(t *testing.T) {
		a, events := sfRig(t, "United States", sfForm("20", sfOptions, true), false, settingOn)
		_, st := a.run()
		if st == nil || !strings.Contains(st.reason, "no Accept control") {
			t.Fatalf("stop %+v", st)
		}
		if strings.Contains(strings.Join(*events, ","), "submit") {
			t.Fatalf("events %v: the form was submitted", *events)
		}
		if _, err := a.v.Get("signin-site-careers.example.test"); err == nil {
			t.Fatal("the saved password was kept")
		}
	})
	t.Run("the retype boxes both receive the email", func(t *testing.T) {
		a, _ := sfRig(t, "United States", sfForm("20", sfOptions, false), false)
		// The sign-in that follows clears the typed map, so the boxes are read
		// when Create account is pressed on the form.
		base := a.tab.onClick
		var sent map[int]string
		a.tab.onClick = func(tab *scriptTab, c Control) {
			if c.Labels[0] == "Create account" && tab.state.Title == "Create account" {
				sent = map[int]string{0: tab.typed[0], 4: tab.typed[4]}
			}
			base(tab, c)
		}
		if _, st := a.run(); st != nil {
			t.Fatalf("stop %+v", st)
		}
		if sent[0] != "alex@example.com" || sent[4] != "alex@example.com" {
			t.Fatalf("email boxes at the press %v: Email and Retype email both hold the email", sent)
		}
	})
}

func TestApply_Signup_NewPasswordBoxIsRegistrationNeverTypesStored(t *testing.T) {
	reg := loginPage("")
	reg.Inputs[1].Autocomplete = "new-password"
	if w := findWall(reg); w == nil || !w.registration {
		t.Fatalf("wall %+v: a new-password box marks registration", w)
	}
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.tab.set(reg)
	a.s.entry = reg
	_, st := a.run()
	if st == nil || !strings.Contains(st.reason, "careers.example.test asks for a new account") {
		t.Fatalf("stop %+v", st)
	}
	if len(a.tab.typed) != 0 || len(a.tab.clicks) != 0 || len(a.e.Run.Signins[0].Tried) != 0 {
		t.Fatalf("typed %v clicked %v tried %v: a stored password goes into a registration box never", a.tab.typed, a.tab.clicks, a.e.Run.Signins[0].Tried)
	}
	// Generated passwords never put three identical characters in a row.
	for range 500 {
		pw, err := credentials.Generate(12)
		if err != nil {
			t.Fatal(err)
		}
		for i := 2; i < len(pw); i++ {
			if pw[i] == pw[i-1] && pw[i] == pw[i-2] {
				t.Fatalf("%q repeats a character three times", pw)
			}
		}
	}
}

// ByteDance marks its sign-in box new-password beside "Forgot password?", and
// its Create account view, drawn over the sign-in form whose boxes stay in the
// page, asks for an email and a code mailed to it: the box is signed in to, and
// the code form is the user's, untouched.
func TestApply_Signup_ForgotPasswordSignsInAndACodeOnlyFormIsLeft(t *testing.T) {
	bd := loginPage("")
	bd.Inputs[1].Autocomplete = "new-password"
	bd.Controls = append(bd.Controls, Control{I: 4, Tag: "button", Labels: []string{"Forgot password?"}, Form: -1})
	if w := findWall(bd); w == nil || w.registration || readSignupForm(bd) != nil {
		t.Fatalf("wall %+v: a new-password box beside Forgot password? signs in", w)
	}
	code := &PageState{URL: loginURL, Title: "Create account", Text: "Create account", Lines: []string{"Create account"},
		Inputs: []InputInfo{
			{I: 10, Tag: "input", Type: "text", Label: "Email", Placeholder: "Email", ID: "email", Form: -1},
			{I: 11, Tag: "input", Type: "text", Label: "Enter Email Verification Code", Placeholder: "Enter Email Verification Code", ID: "validCode", Form: -1},
		},
		Controls: []Control{{I: 12, Tag: "button", Labels: []string{"Get code"}, Disabled: true, Form: -1}, {I: 13, Tag: "button", Labels: []string{"Continue"}, Form: -1}},
	}
	code.Inputs = append(code.Inputs, bd.Inputs...)
	code.Controls = append(code.Controls, bd.Controls[0], bd.Controls[2])
	a, _ := sfRig(t, "United States", code, false)
	a.tab.set(bd)
	a.s.entry = bd
	_, st := a.run()
	if st == nil || !strings.Contains(st.reason, "asks for a code mailed to your email") {
		t.Fatalf("stop %+v", st)
	}
	if len(a.tab.typed) != 0 || len(a.e.Run.Signins[0].Tried) != 1 {
		t.Fatalf("typed %v tried %v: the saved password is tried on the sign-in, nothing typed into the code form", a.tab.typed, a.e.Run.Signins[0].Tried)
	}
}

// Deloitte's Avature: Create account leads to the application's method
// choice, Copy & Paste or From Device, and its Register step asks for the
// password beside the application's fields: the account is the walk's, on a
// run that submits, and nothing is typed or pressed for it here.
func TestApply_Avature_APasteMethodChoiceIsACombinedAccount(t *testing.T) {
	method := &PageState{URL: loginURL, Title: "Select your resume", Text: "Choose any option to start the application process", Lines: []string{"Choose any option to start the application process"},
		Controls: []Control{
			{I: 10, Tag: "a", Labels: []string{"Copy & Paste"}, Form: -1},
			{I: 11, Tag: "a", Labels: []string{"From Device"}, Form: -1},
			{I: 12, Tag: "a", Labels: []string{"Login"}, Form: -1},
		}}
	a, _ := sfRig(t, "United States", method, false)
	d, st := a.run()
	if st != nil || !strings.Contains(d, "the application walk registers on it") || a.e.combined == nil {
		t.Fatalf("detail %q stop %+v combined %+v", d, st, a.e.combined)
	}
	for _, c := range a.tab.clicks {
		if c == "Copy & Paste" || c == "From Device" {
			t.Fatalf("clicks %v: the method is the walk's", a.tab.clicks)
		}
	}
}

func TestApply_Avature_OnlyWithoutResumeMethodPressed(t *testing.T) {
	method := &PageState{
		URL: loginURL, Title: "How would you like to apply?", Text: "How would you like to apply?", Lines: []string{"How would you like to apply?"},
		Controls: []Control{
			{I: 10, Tag: "button", Labels: []string{"Upload Resume"}, Form: -1},
			{I: 11, Tag: "button", Labels: []string{"Apply Without Resume"}, Form: -1},
			{I: 12, Tag: "button", Labels: []string{"Sign in with LinkedIn"}, Form: -1},
		},
	}
	avatureWall := func(err string) *PageState {
		w := loginPage(err)
		w.Controls[1].Labels = []string{"Create an account"}
		return w
	}
	wall := avatureWall("")
	run := func(t *testing.T, afterMethod *PageState) (*acctEnv, *stop) {
		dir := t.TempDir()
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", func(d *Deps) {
			signupMod("United States")(d)
			d.Page.SnapshotDir = dir
		})
		a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
		a.tab.set(wall)
		a.s.entry = wall
		a.tab.onClick = func(tab *scriptTab, c Control) {
			switch c.Labels[0] {
			case "Sign in":
				tab.typed = map[int]string{}
				tab.set(avatureWall("Invalid email or password"))
			case "Create an account":
				tab.set(method)
			case "Apply Without Resume":
				tab.set(afterMethod)
			}
		}
		_, st := a.run()
		return a, st
	}
	form := sfForm("20", nil, false)
	form.Inputs = form.Inputs[:4]
	a, _ := run(t, form)
	for _, c := range a.tab.clicks {
		if c == "Upload Resume" || c == "Sign in with LinkedIn" {
			t.Fatalf("clicks %v: an upload or third-party method was pressed", a.tab.clicks)
		}
	}
	if !strings.Contains(strings.Join(a.tab.clicks, ","), "Apply Without Resume") {
		t.Fatalf("clicks %v: the method marked without resume was not pressed", a.tab.clicks)
	}
	// A destination page with no password box stops, with a snapshot of it.
	nopw := &PageState{URL: loginURL, Title: "Profile", Text: "Tell us about you", Lines: []string{"Tell us about you"}, Inputs: []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "Name", Form: -1}}}
	a, st := run(t, nopw)
	if st == nil || !strings.Contains(st.reason, "no create-account form appeared") {
		t.Fatalf("stop %+v", st)
	}
	files, _ := filepath.Glob(filepath.Join(a.g.r.d.Page.SnapshotDir, "*-pre-form.html"))
	if len(files) != 1 {
		t.Fatalf("snapshots %v: a pre-form snapshot is expected", files)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatal(err)
	}
}

// combinedPage is a create-account form that is also the application.
func combinedPage(url string) *PageState {
	return &PageState{
		URL: url, Title: "Apply", Text: "Create your account and apply", Lines: []string{"Create your account and apply"},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "email", Label: "Email", Form: -1},
			{I: 4, Tag: "input", Type: "email", Label: "Confirm email", Form: -1},
			{I: 1, Tag: "input", Type: "password", Label: "Password", Form: -1},
			{I: 5, Tag: "input", Type: "password", Label: "Confirm password", Form: -1},
			{I: 8, Tag: "input", Type: "text", Label: "First Name", Form: -1},
		},
		Controls: []Control{{I: 9, Tag: "button", Labels: []string{"Next"}, Form: -1}},
	}
}

func TestApply_CombinedAccount_HookTypesOnlyUnderThreeConditions(t *testing.T) {
	const host = "careers.example.test"
	newHook := func(t *testing.T) *acctEnv {
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, host, signupMod("United States"))
		a.e.combined = &combinedAccount{host: host, name: "signin-site-" + host, system: ATSGeneric, tenant: host}
		return a
	}
	ctx := context.Background()
	t.Run("the three conditions hold: the account fields are typed once", func(t *testing.T) {
		a := newHook(t)
		page := combinedPage(loginURL)
		a.tab.set(page)
		if stp := a.s.combinedType(ctx, page); stp != nil {
			t.Fatalf("stop %+v", stp)
		}
		pw := vaultValue(t, a.v, "signin-site-"+host)
		if a.tab.typed[0] != "alex@example.com" || a.tab.typed[4] != "alex@example.com" || a.tab.typed[1] != pw || a.tab.typed[5] != pw || a.tab.typed[8] != "" {
			t.Fatalf("typed %v: email, retype, password and confirm are typed, the application's own fields are not", a.tab.typed)
		}
		// At most one account per run: a later password page is a sign-in.
		a.tab.typed = map[int]string{}
		_ = a.s.combinedType(ctx, page)
		if len(a.tab.typed) != 0 {
			t.Fatalf("typed %v: a second account is never created", a.tab.typed)
		}
		// After the page was sent and the combined form is gone, the account is kept
		// and counted.
		a.tab.set(appPage())
		a.s.combinedSettled(ctx)
		if vaultValue(t, a.v, "signin-site-"+host) != pw || a.g.count(`SELECT count(*) FROM signup_attempts`) != 1 {
			t.Fatal("the kept account was not stored and counted against the quota")
		}
		if a.g.count(`SELECT count(*) FROM credential_index WHERE name = ? AND account_created_at IS NOT NULL`, "signin-site-"+host) != 1 {
			t.Fatal("the index does not record a created account")
		}
		if a.e.Run.Signins[0].Succeeded != "created-account" {
			t.Fatalf("record %+v", a.e.Run.Signins)
		}
	})
	t.Run("a combined form that is still shown after the press loses the password", func(t *testing.T) {
		a := newHook(t)
		page := combinedPage(loginURL)
		a.tab.set(page)
		_ = a.s.combinedType(ctx, page)
		a.s.combinedSettled(ctx) // the page still shows the combined form
		if _, err := a.v.Get("signin-site-" + host); err == nil {
			t.Fatal("the password of a refused combined form was kept")
		}
		if a.g.count(`SELECT count(*) FROM signup_attempts`) != 0 {
			t.Fatal("a refused form counted against the quota")
		}
	})
	t.Run("another host, a plain sign-up form, or a captcha types nothing", func(t *testing.T) {
		other := combinedPage("https://sso.example.org/apply")
		plain := combinedPage(loginURL)
		plain.Inputs = plain.Inputs[:4]
		captcha := combinedPage(loginURL)
		captcha.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, W: 304, H: 78}}
		captcha.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
		for name, page := range map[string]*PageState{"another host": other, "not an application": plain, "a captcha": captcha} {
			a := newHook(t)
			a.tab.set(page)
			if stp := a.s.combinedType(ctx, page); stp != nil {
				t.Fatalf("%s: stop %+v", name, stp)
			}
			if len(a.tab.typed) != 0 {
				t.Errorf("%s: typed %v", name, a.tab.typed)
			}
			if _, err := a.v.Get("signin-site-" + host); err == nil {
				t.Errorf("%s: a password was saved", name)
			}
		}
	})
}

// detailsPage is the account-details step of a UKG tenant.
func detailsPage(phoneRequired bool, alert string) *PageState {
	st := &PageState{
		URL: loginURL, Title: "Almost there!", Headings: []string{"Almost there!"}, Text: "Almost there! Tell us a bit more.", Lines: []string{"Almost there!"},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Label: "First Name", Form: -1},
			{I: 1, Tag: "input", Type: "text", Label: "Last Name", Form: -1},
			{I: 2, Tag: "input", Type: "tel", Label: "Phone", Required: phoneRequired, Form: -1},
			{I: 3, Tag: "input", Type: "checkbox", Label: "I consent to the processing of my data", Form: -1},
		},
		Controls: []Control{{I: 4, Tag: "button", Labels: []string{"Continue"}, Form: -1}},
	}
	if alert != "" {
		st.Validation = []string{alert}
	}
	return st
}

func detailsRig(t *testing.T, phoneRequired bool, mod ...func(*Deps)) *acctEnv {
	t.Helper()
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", signupMod("United States", mod...))
	step := detailsPage(phoneRequired, "")
	a.tab.set(step)
	a.s.entry = step
	consented := false
	a.tab.onClick = func(tab *scriptTab, c Control) {
		if len(c.Labels) == 0 {
			consented = true // the click landed on the consent box
			return
		}
		if c.Labels[0] == "Continue" {
			if consented {
				tab.set(appPage())
				return
			}
			tab.set(detailsPage(phoneRequired, "You must accept the consent to continue."))
		}
	}
	return a
}

func TestApply_AccountDetails_ConsentFollowsSetting(t *testing.T) {
	t.Run("setting on: names, no optional phone, the consent, one pointer click", func(t *testing.T) {
		a := detailsRig(t, false, settingOn)
		d, st := a.run()
		if st != nil || !strings.Contains(d, "completed the account-details step") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
		if a.tab.typed[0] != "Alex" || a.tab.typed[1] != "Example" || a.tab.typed[2] != "" {
			t.Fatalf("typed %v: first and last name only, the optional phone left empty", a.tab.typed)
		}
		if len(a.tab.inputClicks) != 1 || len(a.tab.clicks) != 2 {
			t.Fatalf("input clicks %v, clicks %v: the consent box and Continue are real pointer clicks", a.tab.inputClicks, a.tab.clicks)
		}
	})
	t.Run("setting off: the step is reported and nothing is typed or pressed", func(t *testing.T) {
		a := detailsRig(t, false)
		_, st := a.run()
		if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "autoAcceptLegalDeclarations is off") || !strings.Contains(st.reason, "nothing was pressed") {
			t.Fatalf("stop %+v", st)
		}
		if len(a.tab.inputClicks) != 0 || len(a.tab.clicks) != 0 || a.tab.typed[0] != "" || a.tab.typed[1] != "" {
			t.Fatalf("input clicks %v, clicks %v, typed %v: the setting is off", a.tab.inputClicks, a.tab.clicks, a.tab.typed)
		}
	})
	t.Run("a required phone is typed", func(t *testing.T) {
		a := detailsRig(t, true, settingOn)
		if _, st := a.run(); st != nil {
			t.Fatalf("stop %+v", st)
		}
		if a.tab.typed[2] != "+1 555 0100" {
			t.Fatalf("typed %v", a.tab.typed)
		}
	})
}

// The account-details step ticks only what the consent gate allows. With the
// setting on the step's consent is ticked; an opt-in and a self-identification
// box beside it are not; and each box's decision is on the audit.
func TestApply_AccountDetails_OnlyTheConsentIsTickedNeverAnOptInOrSelfIdentification(t *testing.T) {
	a := detailsRig(t, false, settingOn)
	a.s.entry.Inputs = append(a.s.entry.Inputs,
		InputInfo{I: 5, Tag: "input", Type: "checkbox", Label: "Send me job alerts", Form: -1},
		InputInfo{I: 6, Tag: "input", Type: "checkbox", Label: "I agree to receive marketing emails", Form: -1},
		InputInfo{I: 7, Tag: "input", Type: "checkbox", Label: "I am a protected veteran", Form: -1})
	d, st := a.run()
	if st != nil || !strings.Contains(d, "completed the account-details step") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if len(a.tab.inputClicks) != 1 || a.tab.inputClicks[0] != "I consent to the processing of my data" {
		t.Fatalf("ticked %v: only the step's consent", a.tab.inputClicks)
	}
	for code, want := range map[string]int{gates.CodeLegalDeclarationOn: 1, gates.CodeMarketingOptIn: 2, gates.CodeSelfIDCheckbox: 1} {
		if n := a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'consent_control' AND code = ?`, code); n != want {
			t.Errorf("%d audited %s decisions, want %d", n, code, want)
		}
	}
}

func TestApply_AccountDetails_WalkerGateStepCompletedByStage(t *testing.T) {
	t.Run("the gate itself is the details step", func(t *testing.T) {
		a := detailsRig(t, false, settingOn)
		d, st := a.g.r.accountOn(context.Background(), a.e, a.s, a.tab.state)
		if st != nil || !strings.Contains(d, "completed the account-details step") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
	})
	t.Run("the sign-up lane finds no create-account form and tries the step first", func(t *testing.T) {
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", signupMod("United States", settingOn))
		a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
		consented := false
		a.tab.onClick = func(tab *scriptTab, c Control) {
			switch {
			case len(c.Labels) == 0:
				consented = true
			case c.Labels[0] == "Sign in":
				tab.typed = map[int]string{}
				tab.set(loginPage("Invalid email or password"))
			case c.Labels[0] == "Create account":
				tab.set(detailsPage(false, ""))
			case c.Labels[0] == "Continue" && consented:
				tab.set(appPage())
			}
		}
		d, st := a.run()
		if st != nil || !strings.Contains(d, "completed the account-details step") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
	})
}

func sfLink(host string) gmail.LinkResult {
	return gmail.LinkResult{URL: "https://" + host + "/career?navBarLevel=EMAIL_VERIFICATION&hash=" + strings.Repeat("Ab3dE6", 4), From: mailFrom("noreply", "successfactors.com"), ReceivedAt: t0.Add(40 * time.Second)}
}

func TestApply_Activation_SuccessFactorsOpensOnlyItsOwnMailAfterTheSignupPress(t *testing.T) {
	const sfHost = "career5.successfactors.com"
	build := func(t *testing.T, fn func(gmail.LinkQuery, int) (gmail.LinkResult, error)) (*acctEnv, *scriptMail, *[]string) {
		m := &scriptMail{fn: fn}
		a := newAcct(t, Input{URL: "https://" + sfHost + "/x", Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, sfHost, func(d *Deps) {
			signupMod("United States")(d)
			d.Page.Mail = m
		})
		a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
		wall := loginPage("")
		wall.URL = "https://" + sfHost + "/x"
		a.tab.set(wall)
		a.s.entry = wall
		activated := false
		var log []string
		form := sfForm("20", sfOptions, false)
		form.URL = wall.URL
		a.tab.onNav = func(tab *scriptTab, url string) {
			if strings.Contains(url, "navBarLevel=EMAIL_VERIFICATION") {
				activated = true
				log = append(log, "activation-opened")
				return
			}
			w := loginPage("")
			w.URL = wall.URL
			tab.set(w)
		}
		a.tab.onClick = func(tab *scriptTab, c Control) {
			switch c.Labels[0] {
			case "Sign in":
				pw := tab.typed[1]
				tab.typed = map[int]string{}
				if pw == "Default-Pw-1!x" || !activated {
					tab.set(loginPage("Invalid email or password"))
					return
				}
				tab.set(appPage())
			case "Create account":
				if tab.state.Title == "Create account" {
					sent := &PageState{URL: wall.URL, Title: "Activation Email Sent", Text: "Activation Email Sent", Lines: []string{"Activation Email Sent", "We have sent you an activation email."},
						Controls: []Control{{I: 9, Tag: "button", Labels: []string{"Resend activation email"}, Form: -1}}}
					tab.set(sent)
					return
				}
				tab.set(form)
			case "Resend activation email":
				log = append(log, "resend")
			}
		}
		return a, m, &log
	}
	t.Run("a mail that arrives after the press is opened and the sign-in is the verdict", func(t *testing.T) {
		a, m, log := build(t, func(q gmail.LinkQuery, call int) (gmail.LinkResult, error) {
			if call < 4 {
				return noMailYet()
			}
			return sfLink(sfHost), nil
		})
		d, st := a.run()
		if st != nil || !strings.Contains(d, "activated and signed in") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
		if len(m.calls) < 4 || m.calls[0].After.IsZero() {
			t.Fatalf("lookups %+v: only mail newer than the sign-up press is asked for", m.calls)
		}
		if strings.Contains(strings.Join(*log, ","), "resend") {
			t.Fatalf("log %v: a generic host gets no resend press", *log)
		}
	})
	t.Run("another host's activation mail stops as credential and is not opened", func(t *testing.T) {
		a, _, log := build(t, func(gmail.LinkQuery, int) (gmail.LinkResult, error) { return sfLink("career9.successfactors.com"), nil })
		_, st := a.run()
		if st == nil || st.class != ClassCredential || strings.Contains(strings.Join(*log, ","), "activation-opened") {
			t.Fatalf("stop %+v log %v", st, *log)
		}
	})
	t.Run("a mail older than the press is not opened", func(t *testing.T) {
		a, _, log := build(t, func(gmail.LinkQuery, int) (gmail.LinkResult, error) {
			l := sfLink(sfHost)
			l.ReceivedAt = t0.Add(-time.Hour)
			return l, nil
		})
		_, st := a.run()
		if st == nil || st.class != ClassCredential || strings.Contains(strings.Join(*log, ","), "activation-opened") {
			t.Fatalf("stop %+v log %v", st, *log)
		}
	})
	t.Run("a sign-in page with a checkbox captcha is needs-login without typing a password", func(t *testing.T) {
		a := newAcct(t, Input{URL: "https://" + sfHost + "/x", Caller: gates.CallerCLI}, ATSGeneric, sfHost, nil)
		a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
		wall := loginPage("")
		wall.URL = "https://" + sfHost + "/x"
		wall.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, W: 304, H: 78}}
		wall.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
		a.tab.set(wall)
		a.s.entry = wall
		_, st := a.run()
		if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "captcha stands on the sign-in page") || len(a.tab.typed) != 0 {
			t.Fatalf("stop %+v typed %v", st, a.tab.typed)
		}
	})
}

// ebrdForm is SuccessFactors' "Create an Account" form as the Node fixture
// generic-signup.successfactors-create-account-1004 reduces it from EBRD's
// snapshot: name and phone boxes beside the credentials, an optional
// notification box already ticked, and a "Please sign in" link.
func ebrdForm() *PageState {
	return &PageState{
		URL: loginURL, Title: "Create account", Text: "Career Opportunities: Create an Account. Already a registered user? Please sign in. Password must not be longer than 20 characters.",
		Lines: []string{"Career Opportunities: Create an Account"},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Name: "fbclc_userName", Label: "Email Address:*", Form: 0},
			{I: 4, Tag: "input", Type: "text", Name: "fbclc_emailConf", Label: "Retype Email Address:*", Form: 0},
			{I: 1, Tag: "input", Type: "password", Name: "fbclc_pwd", Label: "Choose Password:*", Form: 0},
			{I: 5, Tag: "input", Type: "password", Name: "fbclc_pwdConf", Label: "Retype Password:*", Form: 0},
			{I: 8, Tag: "input", Type: "text", Name: "fbclc_fName", Label: "First Name:*", Form: 0},
			{I: 9, Tag: "input", Type: "text", Name: "fbclc_lName", Label: "Last Name:*", Form: 0},
			{I: 10, Tag: "input", Type: "text", Name: "fbclc_phoneNumber", Label: "Phone Number:*", Form: 0},
			{I: 11, Tag: "input", Type: "checkbox", Name: "fbclc_emailEnabled", Label: "Notification:", Checked: true, Form: 0},
		},
		Controls: []Control{
			{I: 12, Tag: "a", Labels: []string{"Please sign in"}, Form: 0},
			{I: 2, Tag: "button", Labels: []string{"Create Account"}, Form: 0},
		},
		Forms: []FormInfo{{Fields: 8}},
	}
}

func TestApply_Signup_ProfileBoxesOnCreateAccountFormAreTyped(t *testing.T) {
	t.Run("SuccessFactors: names and phone from the profile, the form stays a sign-up", func(t *testing.T) {
		a, _ := sfRig(t, "United States", ebrdForm(), false)
		base := a.tab.onClick
		var sent map[int]string
		a.tab.onClick = func(tab *scriptTab, c Control) {
			if c.Labels[0] == "Create Account" {
				sent = map[int]string{}
				for k, v := range tab.typed {
					sent[k] = v
				}
				tab.set(loginPage(""))
				return
			}
			base(tab, c)
		}
		d, st := a.run()
		if st != nil || !strings.Contains(d, "created an account") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
		pw := vaultValue(t, a.v, "signin-site-careers.example.test")
		want := map[int]string{0: "alex@example.com", 4: "alex@example.com", 1: pw, 5: pw, 8: "Alex", 9: "Example", 10: "+1 555 0100"}
		for k, v := range want {
			if sent[k] != v {
				t.Fatalf("box %d holds %q at the submit, want %q (all %v)", k, sent[k], v, sent)
			}
		}
		if len(a.tab.inputClicks) != 0 || sent[11] != "" {
			t.Fatalf("clicked %v typed %q: the ticked notification box is the site's own default", a.tab.inputClicks, sent[11])
		}
	})
	t.Run("YC: a username beside the email is the email's local part", func(t *testing.T) {
		yc := &PageState{URL: loginURL, Title: "Account", Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Label: "First Name", Form: 0},
			{I: 1, Tag: "input", Type: "text", Label: "Last Name", Form: 0},
			{I: 2, Tag: "input", Type: "email", Label: "Email", Form: 0},
			{I: 3, Tag: "input", Type: "text", ID: "ycid-input", Name: "username", Autocomplete: "username", Label: "Username", Form: 0},
			{I: 4, Tag: "input", Type: "password", Name: "password", Autocomplete: "new-password", Form: 0},
		}, Controls: []Control{
			{I: 5, Tag: "button", Type: "submit", Labels: []string{"Sign up"}, Form: 0},
			{I: 6, Tag: "button", Type: "button", Labels: []string{"Log in"}, Form: 0},
		}}
		f := readSignupForm(yc)
		if f == nil || f.combined || f.submit == nil || f.submit.I != 5 {
			t.Fatalf("form %+v: the sign-up view with its own Log in toggle is a create-account form", f)
		}
		got := map[gates.IdentityField]int{}
		for _, b := range f.profile {
			got[b.field] = b.in.I
		}
		if got[fieldUsername] != 3 || got[gates.FieldFirstName] != 0 || got[gates.FieldLastName] != 1 || len(f.emails) != 1 || f.emails[0].I != 2 {
			t.Fatalf("boxes %v emails %v", got, f.emails)
		}
		if u := usernameFrom("Jane.Doe+jobs@example.com"); u != "jane.doejobs" {
			t.Fatalf("username %q", u)
		}
	})
	t.Run("UKG Auth0: email, a new password and Continue", func(t *testing.T) {
		auth0 := &PageState{URL: loginURL, Title: "Sign up", Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Name: "email", Autocomplete: "email", Label: "Email address", Required: true, Form: 0},
			{I: 1, Tag: "input", Type: "password", Name: "password", Autocomplete: "new-password", Label: "Password", Required: true, Form: 0},
		}, Controls: []Control{
			{I: 2, Tag: "button", Type: "button", Labels: []string{"Show password"}, Form: 0},
			{I: 3, Tag: "button", Type: "submit", Labels: []string{"Continue"}, Form: 0},
			{I: 4, Tag: "a", Labels: []string{"Log in"}, Form: -1},
		}}
		if f := readSignupForm(auth0); f == nil || f.submit == nil || f.submit.I != 3 || f.combined {
			t.Fatalf("form %+v: Auth0's sign-up submits with Continue", f)
		}
		spa := *auth0
		spa.Inputs = append([]InputInfo(nil), auth0.Inputs...)
		spa.Controls = append([]Control(nil), auth0.Controls...)
		for i := range spa.Inputs {
			spa.Inputs[i].Form = -1
		}
		for i := range spa.Controls {
			spa.Controls[i].Form = -1
		}
		if f := readSignupForm(&spa); f == nil || f.submit == nil || f.submit.I != 3 {
			t.Fatalf("form %+v: a new-password box registers with no <form> around it too", f)
		}
		login := *auth0
		login.Inputs = append([]InputInfo(nil), auth0.Inputs...)
		login.Inputs[1].Autocomplete = "current-password"
		if f := readSignupForm(&login); f != nil {
			t.Fatalf("form %+v: the same page with current-password is a sign-in", f)
		}
	})
	t.Run("an application that carries account fields is combined", func(t *testing.T) {
		wellfound := &PageState{URL: loginURL, Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Name: "name", Label: "Full Name *", Form: 0},
			{I: 1, Tag: "input", Type: "email", Name: "email", Label: "Email *", Form: 0},
			{I: 2, Tag: "input", Type: "password", Label: "Set a Password *", Form: 0},
			{I: 3, Tag: "input", Type: "password", Label: "Confirm Password *", Form: 0},
			{I: 4, Tag: "input", Type: "text", Label: "Set Your Location *", Form: 0},
			{I: 5, Tag: "input", Type: "number", Label: "Desired Salary *", Form: 0},
		}, Controls: []Control{{I: 6, Tag: "button", Type: "submit", Labels: []string{"Submit application"}, Form: 0}}}
		sfQuick := &PageState{URL: loginURL, Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Label: "Email Address:*", Form: 0},
			{I: 1, Tag: "input", Type: "password", Label: "Choose Password:*", Form: 0},
			{I: 2, Tag: "input", Type: "password", Label: "Retype Password:*", Form: 0},
			{I: 3, Tag: "input", Type: "text", Label: "First Name:*", Form: 0},
		}, Controls: []Control{
			{I: 4, Tag: "button", Type: "submit", Labels: []string{"Apply"}, Form: 0},
			{I: 5, Tag: "button", Labels: []string{"Create Account"}, Form: -1},
		}}
		quick := ebrdForm()
		quick.Inputs = append(quick.Inputs, InputInfo{I: 30, Tag: "input", Type: "file", Label: "Resume / CV", Form: 0})
		if f := readCombined(quick); f == nil || len(f.emails) != 2 {
			t.Fatalf("combined %+v: the two email boxes, never the job-alert box named emailEnabled", f)
		}
		for name, st := range map[string]*PageState{"Wellfound": wellfound, "SuccessFactors Quick Apply": sfQuick} {
			if f := readSignupForm(st); f != nil && !f.combined {
				t.Errorf("%s: form %+v is not combined", name, f)
			}
		}
	})
	t.Run("an SSO button is never the submit", func(t *testing.T) {
		st := ebrdForm()
		st.Controls = append([]Control{{I: 20, Tag: "button", Labels: []string{"Sign up with LinkedIn"}, Form: 0}}, st.Controls...)
		if f := readSignupForm(st); f == nil || f.submit == nil || f.submit.I != 2 {
			t.Fatalf("form %+v: the plain Create Account is the submit", f)
		}
		w := loginPage("")
		w.Controls[1].Labels = []string{"Sign up with LinkedIn"}
		if fw := findWall(w); fw == nil || fw.create != nil {
			t.Fatalf("wall %+v: an SSO control is no create-account control", fw)
		}
	})
	t.Run("RedBalloon: one password and a Create Account submit is a registration", func(t *testing.T) {
		rb := &PageState{URL: loginURL, Title: "Create Account", Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "email", Label: "Email", Form: 0},
			{I: 1, Tag: "input", Type: "password", Label: "Password", Form: 0},
		}, Controls: []Control{
			{I: 2, Tag: "button", Type: "submit", Labels: []string{"Create Account"}, Form: 0},
			{I: 3, Tag: "a", Labels: []string{"Log in"}, Form: -1},
		}, Forms: []FormInfo{{Fields: 2}}}
		if w := findWall(rb); w == nil || !w.registration {
			t.Fatalf("wall %+v: the submit's own words make it a registration", w)
		}
		if f := readSignupForm(rb); f == nil || f.submit == nil || f.submit.I != 2 {
			t.Fatalf("form %+v", f)
		}
		if w := findWall(loginPage("")); w == nil || w.registration {
			t.Fatalf("wall %+v: a Sign in submit beside a Create account link is a sign-in", w)
		}
		if readSignupForm(loginPage("")) != nil {
			t.Fatal("the sign-in wall read as a create-account form")
		}
	})
	t.Run("IBM: an account named for its site is still created through its link", func(t *testing.T) {
		for _, l := range []string{"Create an IBMid", "Create a Workday account", "Create your Oracle ID"} {
			w := loginPage("")
			w.Controls[1].Labels = []string{l}
			if fw := findWall(w); fw == nil || fw.create == nil {
				t.Errorf("%q: wall %+v has no create-account control", l, fw)
			}
		}
		// The probe keeps a link only by these words: its copy is the same pattern.
		if !strings.Contains(probeJS, strings.TrimPrefix(signupBtnRE.String(), "(?i)")+"/i") {
			t.Error("probe.js no longer keeps the links signupBtnRE reads as create-account controls")
		}
	})
	t.Run("Workday: an unmarked Yes, I agree under a by-creating statement is the agreement", func(t *testing.T) {
		form := func(text, label string) *PageState {
			return &PageState{URL: loginURL, Title: "Create Account", Text: text, Inputs: []InputInfo{
				{I: 0, Tag: "input", Type: "text", Label: "Email Address", Autocomplete: "email", Form: 0},
				{I: 1, Tag: "input", Type: "password", Label: "Password", Autocomplete: "new-password", Form: 0},
				{I: 2, Tag: "input", Type: "password", Label: "Verify New Password", Autocomplete: "new-password", Form: 0},
				{I: 3, Tag: "input", Type: "checkbox", Label: label, Form: 0},
			}, Controls: []Control{{I: 4, Tag: "button", Labels: []string{"Create Account"}, Form: 0}}}
		}
		bound := "By creating this account, you agree that we may collect and use the information submitted by you."
		if f := readSignupForm(form(bound, "Yes, I agree.")); f == nil || len(f.boxes) != 1 {
			t.Fatalf("form %+v: the box is the account's agreement", f)
		}
		if f := readSignupForm(form(bound, "I agree to receive job alerts")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: an opt-in is never the agreement", f)
		}
		if f := readSignupForm(form("Create your account.", "Yes, I agree.")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: no statement binds the account to the box", f)
		}
		// Workday's own tenant: the box's own words bind it to creating the
		// account, with no statement around it and no required mark.
		own := "For details on how Workday processes your personal data, please see our Recruitment Privacy Statement."
		if f := readSignupForm(form(own, "I agree to creating this account to allow me to apply for positions with Workday.")); f == nil || len(f.boxes) != 1 {
			t.Fatalf("form %+v: a box that agrees to creating this account is the agreement", f)
		}
		if f := readSignupForm(form(own, "I agree to be contacted about future roles")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: an opt-in is never the agreement", f)
		}
		// CVS: the policy is a link to another page above a bare I Agree; the
		// link opens no terms dialog.
		cvs := form("Candidate Privacy Policy - https://www.cvshealth.com/privacy-policy/workforce.html I Agree Create Account Already have an account? Sign In Privacy Policy", "I Agree")
		cvs.Controls = append(cvs.Controls, Control{I: 5, Tag: "a", Labels: []string{"https://www.cvshealth.com/privacy-policy/workforce.html"}, Href: "https://www.cvshealth.com/privacy-policy/workforce.html", Form: 0})
		if f := readSignupForm(cvs); f == nil || len(f.boxes) != 1 || f.terms != nil {
			t.Fatalf("form %+v: the I Agree box is the agreement and the policy link is no terms dialog", f)
		}
		// Boeing's Taleo Business Edition (live): the box's caption carries the
		// policy link, which opens the statement in a window of its own.
		boeing := form("Applicant Privacy Policy. This notice pertains to the personal information of applicants. I agree to Boeing Applicant Privacy Policy", "I agree to Boeing Applicant Privacy Policy")
		boeing.Inputs[3].Required = true
		boeing.Controls = append(boeing.Controls, Control{I: 5, Tag: "a", Labels: []string{"Boeing Applicant Privacy Policy"}, Href: "#", Form: 0})
		if f := readSignupForm(boeing); f == nil || len(f.boxes) != 1 || f.terms != nil {
			t.Fatalf("form %+v: the box is the agreement and its caption's link is no terms dialog", f)
		}
		boeing.Inputs[3].Label = "I agree to the Terms of Use"
		if f := readSignupForm(boeing); f == nil || f.terms == nil {
			t.Fatalf("form %+v: a terms link outside the box's caption still opens the terms dialog", f)
		}
		// Visa (live): "I consent to the above." under a by-creating-a-profile
		// statement, and the cookie notice's "Read Full Privacy Message" toggle,
		// which opens no terms dialog.
		visa := form("By creating a profile and applying for a Visa role, I acknowledge that I have read and understood the Visa Candidate Privacy Notice. Read More I consent to the above.", "I consent to the above.")
		visa.Controls = append(visa.Controls, Control{I: 5, Tag: "button", Labels: []string{"Read Full Privacy Message"}, Form: -1})
		if f := readSignupForm(visa); f == nil || len(f.boxes) != 1 || f.terms != nil {
			t.Fatalf("form %+v: the box is the agreement and the notice's toggle is no terms dialog", f)
		}
		if f := readSignupForm(form("Create your profile.", "I consent to the above.")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: no statement binds the profile to the box", f)
		}
		// Trend Micro's Workday (live): the create-account statement names a
		// privacy notice above an unmarked box; the page refuses the account without it.
		trend := "By clicking the \u201cCreate Account\u201d button, you are agreeing to Trend Micro\u2019s Recruitment Privacy Notice. Read More"
		if f := readSignupForm(form(trend, "Yes, I have read and consent to the above notices.")); f == nil || len(f.boxes) != 1 {
			t.Fatalf("form %+v: a box that has read and consents to the notices above is the agreement", f)
		}
		if f := readSignupForm(form("Create your account.", "Yes, I have read and consent to the above notices.")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: no statement binds the account to the box", f)
		}
		// Expedia's Workday (live): an unmarked box that says the applicant has
		// read the named privacy policy; the page refuses the account without it.
		expedia := "When you create an Expedia Group Candidate Home account, the personal information you provide to us may be used across multiple Expedia Group recruiting systems."
		if f := readSignupForm(form(expedia, "I have read the Expedia Group Global Staffing Privacy Policy.")); f == nil || len(f.boxes) != 1 {
			t.Fatalf("form %+v: a box that has read the named policy is the agreement", f)
		}
		if f := readSignupForm(form(expedia, "I have read the job description")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: having read anything else is no agreement", f)
		}
		// Red Hat's Workday (live): the box's own words say ticking it consents
		// to the terms above; unmarked, and the page refuses the account without it.
		redhat := "When you apply for a position at Red Hat, we use the personal data you provide for the purpose of assessing your application."
		if f := readSignupForm(form(redhat, "By clicking the checkbox I consent to the above terms including the cross border transfer of data.")); f == nil || len(f.boxes) != 1 {
			t.Fatalf("form %+v: a box whose ticking consents to the terms above is the agreement", f)
		}
		if f := readSignupForm(form(redhat, "By checking this box, I agree to receive job alerts")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: an opt-in is never the agreement", f)
		}
		// P&G's Workday (live): the box accepts the terms described above,
		// unmarked; the page refuses the account without it.
		pg := "The Procter & Gamble Company processes your personal information in accordance with P&G's Global Candidate Privacy Notice."
		if f := readSignupForm(form(pg, "I UNDERSTAND AND ACCEPT THE TERMS DESCRIBED ABOVE AND IN P&G\u2019s GLOBAL CANDIDATE PRIVACY NOTICE.")); f == nil || len(f.boxes) != 1 {
			t.Fatalf("form %+v: a box that accepts the terms above is the agreement", f)
		}
		if f := readSignupForm(form(pg, "I accept the terms of the talent community newsletter")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: an opt-in is never the agreement", f)
		}
		// Target's Workday (live): the box agrees, by continuing, to the
		// privacy notice above it; unmarked, and the page refuses the account
		// without it.
		target := "Target\u2019s Career Privacy Notice."
		if f := readSignupForm(form(target, "By continuing, you agree to the above Career Privacy Notice")); f == nil || len(f.boxes) != 1 {
			t.Fatalf("form %+v: a box that agrees to the notice above by continuing is the agreement", f)
		}
		if f := readSignupForm(form(target, "By continuing, you agree to receive job alerts")); f == nil || len(f.boxes) != 0 {
			t.Fatalf("form %+v: an opt-in is never the agreement", f)
		}
	})
}

// Node's generic-signup test (emploive): a sign-up press answered by a swap
// back to the sign-in tab headed "Welcome back", with no error text, means the
// account exists. The password this attempt generated is removed and no
// verifying sign-in is made with it.
func TestApply_Signup_WelcomeBackRevertIsAnExistingAccount(t *testing.T) {
	a, events := sfRig(t, "United States", sfForm("20", sfOptions, false), false)
	base := a.tab.onClick
	a.tab.onClick = func(tab *scriptTab, c Control) {
		if c.Labels[0] == "Create account" && tab.state.Title == "Create account" {
			*events = append(*events, "submit")
			back := loginPage("")
			back.Text = "Sign in Sign up Welcome back Sign in to keep tracking the roles you want."
			tab.set(back)
			return
		}
		base(tab, c)
	}
	_, st := a.run()
	if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "already exists") {
		t.Fatalf("stop %+v", st)
	}
	if _, err := a.v.Get("signin-site-careers.example.test"); err == nil {
		t.Fatal("the generated password was kept for an account that was never created with it")
	}
	if a.tab.clicksOf("Sign in") != 1 {
		t.Fatalf("clicks %v: only the shared default's sign-in, never one with the generated password", a.tab.clicks)
	}
}

// TikTok's create-account page (live) carries the address over from its
// sign-in, checks it as it is drawn ("This email account is already in use")
// and creates with a bare "Create"; the press changes nothing. The account
// exists, and the password this attempt generated is not kept.
func TestApply_Signup_AnAddressRefusedBeforeThePressIsAnExistingAccount(t *testing.T) {
	tiktok := &PageState{URL: loginURL, Title: "Create Account", Text: "Create Account This email account is already in use. Sign in now Create",
		Lines:      []string{"Create Account", "This email account is already in use. Sign in now", "Create"},
		Validation: []string{"This email account is already in use. Sign in now"},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", ID: "email", Label: "Enter your email", Placeholder: "Enter your email", Form: 0},
			{I: 1, Tag: "input", Type: "password", ID: "password", Label: "Set your password", Autocomplete: "new-password", Form: 0},
		},
		Controls: []Control{{I: 2, Tag: "button", Type: "button", Labels: []string{"Create"}, Form: 0}},
	}
	a, events := sfRig(t, "United States", tiktok, false)
	_, st := a.run()
	if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "already exists") {
		t.Fatalf("stop %+v events %v", st, *events)
	}
	if a.tab.clicksOf("Create") != 1 {
		t.Fatalf("clicks %v: the bare Create is the form's submit", a.tab.clicks)
	}
	if _, err := a.v.Get("signin-site-careers.example.test"); err == nil {
		t.Fatal("the generated password was kept for an account that was never created with it")
	}
}

// Node's auto-signup PHONE_VERIFICATION_PATTERNS: a sign-up that asks to verify
// a phone by text message stops as the user's, before anything is typed when
// the form says so, and with the generated password kept when the page after
// the press does.
func TestApply_Signup_PhoneVerificationIsTheUsers(t *testing.T) {
	t.Run("on the form", func(t *testing.T) {
		f := sfForm("20", sfOptions, false)
		f.Lines = append(f.Lines, "SMS verification is required to finish creating your account.")
		a, events := sfRig(t, "United States", f, false)
		_, st := a.run()
		if st == nil || !strings.Contains(st.reason, "verify a phone") || len(*events) != 0 || a.tab.typed[4] != "" {
			t.Fatalf("stop %+v events %v typed %v", st, *events, a.tab.typed)
		}
	})
	t.Run("after the press", func(t *testing.T) {
		a, _ := sfRig(t, "United States", sfForm("20", sfOptions, false), false)
		base := a.tab.onClick
		a.tab.onClick = func(tab *scriptTab, c Control) {
			if c.Labels[0] == "Create account" && tab.state.Title == "Create account" {
				tab.set(&PageState{URL: loginURL, Title: "Verify", Lines: []string{"Enter the code we sent to your phone"}, Inputs: []InputInfo{{I: 30, Tag: "input", Type: "text", Label: "Code", Form: -1}}})
				return
			}
			base(tab, c)
		}
		_, st := a.run()
		if st == nil || !strings.Contains(st.reason, "verify a phone") {
			t.Fatalf("stop %+v", st)
		}
		if _, err := a.v.Get("signin-site-careers.example.test"); err != nil {
			t.Fatal("the password of an account that may exist was removed")
		}
	})
}
