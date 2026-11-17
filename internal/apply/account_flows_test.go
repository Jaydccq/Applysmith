package apply

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/vault"
)

const wdHost = "example.wd5.myworkdayjobs.com"

// wdAcct is the account stage on a Workday tenant's sign-in wall.
func wdAcct(t *testing.T, in Input, mod func(*Deps)) *acctEnv {
	t.Helper()
	if in.URL == "" {
		in.URL = "https://" + wdHost + "/x"
	}
	in.Caller = gates.CallerCLI
	a := newAcct(t, in, ATSWorkday, "example", mod)
	st0 := loginPage("")
	st0.URL = "https://" + wdHost + "/x"
	a.tab.set(st0)
	a.s.entry = st0
	return a
}

func vaultValue(t *testing.T, v *vault.Vault, name string) string {
	t.Helper()
	got, err := v.Get(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return got
}

// Workday's spam-bot filter drops a submit made within five seconds of its
// form mounting, with no request and no message (GM, CVS, Rockwell, live).
func TestApply_Account_WorkdaySignInWaitsOutTheSpamBotTimer(t *testing.T) {
	a := wdAcct(t, Input{}, func(d *Deps) { d.Page.Account.Dwell = 300 * time.Millisecond })
	a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Pw-1!x")
	a.signInRight("Tenant-Pw-1!x")
	right := a.tab.onClick
	var pressed time.Time
	a.tab.onClick = func(t *scriptTab, c Control) {
		if pressed.IsZero() {
			pressed = time.Now()
		}
		right(t, c)
	}
	start := time.Now()
	d, st := a.run()
	if st != nil || !strings.Contains(d, "signed in") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if got := pressed.Sub(start); got < 300*time.Millisecond {
		t.Fatalf("Sign In was pressed %v after the stage began; Workday drops it", got)
	}
}

// A refused tenant key is followed by the Settings credential once: the one
// that works becomes the tenant key, and when both are refused nothing stored
// changes.
func TestApply_Account_ARefusedTenantKeyTriesTheSettingsCredentialOnce(t *testing.T) {
	for _, c := range []struct {
		name, settings, tenantAfter string
	}{
		{"the Settings credential works", "Default-Pw-1!x", "Default-Pw-1!x"},
		{"both are refused", "Default-Bad-2!y", "Tenant-Bad-1!x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := wdAcct(t, Input{}, nil)
			a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Bad-1!x")
			a.seed(t, "signin-default", "signin_default", "", c.settings)
			a.signInRight("Default-Pw-1!x")
			d, st := a.run()
			works := c.settings == "Default-Pw-1!x"
			if works != (st == nil) || works && !strings.Contains(d, "settings-default") || !works && st.class != ClassCredential {
				t.Fatalf("detail %q stop %+v", d, st)
			}
			if want := map[bool]int{true: 1, false: 2}[works]; a.wrong != want {
				t.Fatalf("%d passwords refused, want %d: each credential is submitted once", a.wrong, want)
			}
			if got := vaultValue(t, a.v, "tenant-workday-example"); got != c.tenantAfter {
				t.Fatalf("the tenant key is %q, want %q", got, c.tenantAfter)
			}
			if got := vaultValue(t, a.v, "signin-default"); got != c.settings {
				t.Fatalf("the shared default changed: %q", got)
			}
		})
	}
}

// Workday's social-first wall (live, workday.wd5): Apple, Google and "Sign in
// with email"; the email form appears only after that press.
func TestApply_Account_SocialFirstWallOpensTheEmailForm(t *testing.T) {
	for _, stored := range []bool{true, false} {
		// Nothing stored: a running Workday cooldown holds the sign-up, so the
		// run stops at the account gate.
		a := wdAcct(t, Input{UserStarted: true}, func(d *Deps) {
			if !stored {
				d.Page.Policy = coolingOn("workday")
			}
		})
		social := &PageState{URL: "https://" + wdHost + "/x/apply/applyManually", Title: "Sign In", Headings: []string{"Sign In"}, Controls: []Control{
			{I: 7, Tag: "button", Labels: []string{"Sign in with Apple"}, Form: -1},
			{I: 8, Tag: "button", Labels: []string{"Sign in with Google"}, Form: -1},
			{I: 9, Tag: "button", Labels: []string{"Sign in with email"}, Form: -1}}}
		a.tab.set(social)
		a.s.entry = social
		if stored {
			a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Pw-1!x")
		}
		a.signInRight("Tenant-Pw-1!x")
		right := a.tab.onClick
		a.tab.onClick = func(t *scriptTab, c Control) {
			if c.I == 9 {
				t.set(loginPage(""))
			}
			right(t, c)
		}
		// The dialog is still drawing itself at the first look: its email
		// button is covered once (CVS Workday, live).
		a.tab.coverLooks = map[int]int{9: 1}
		d, st := a.run()
		if stored && (st != nil || !strings.Contains(d, "signed in")) {
			t.Fatalf("detail %q stop %+v", d, st)
		}
		// A dry run the user started with nothing stored stops at the account
		// gate, not at Google.
		if !stored && (st == nil || !strings.Contains(st.reason, "account creation is refused") || len(a.e.Run.Signins) == 0 || a.e.Run.Signins[0].Signup != gates.CodeCooldownActive) {
			t.Fatalf("stop %+v record %+v: a dry run with nothing stored is the account gate, not Google", st, a.e.Run.Signins)
		}
	}
}

// A refused password redraws Workday's social-first view (UT Austin, live):
// the next stored password is typed into the email form that view keeps behind
// "Sign in with email", not into a password box that is not there.
func TestApply_Account_RefusalRedrawsTheSocialViewNextPasswordOpensTheEmailForm(t *testing.T) {
	a := wdAcct(t, Input{}, nil)
	a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Bad-1!x")
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	social := &PageState{URL: "https://" + wdHost + "/x/login", Title: "Sign In", Headings: []string{"Sign In"}, Controls: []Control{
		{I: 8, Tag: "button", Labels: []string{"Sign in with Google"}, Form: -1},
		{I: 9, Tag: "button", Labels: []string{"Sign in with email"}, Form: -1}}}
	var seen []string
	a.tab.onClick = func(t *scriptTab, c Control) {
		switch {
		case c.I == 9:
			t.set(loginPage(""))
		case c.Labels[0] == "Sign in":
			pw := t.typed[1]
			t.typed = map[int]string{}
			seen = append(seen, pw)
			if pw == "Default-Pw-1!x" {
				t.set(appPage())
				return
			}
			t.script(loginPage("Invalid email or password"), social)
		}
	}
	d, st := a.run()
	if st != nil || !strings.Contains(d, "settings-default") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if len(seen) != 2 || seen[1] != "Default-Pw-1!x" {
		t.Fatalf("passwords submitted %v: the second goes into the email form", seen)
	}
}

func TestApply_Account_EmailButtonIgnoredOnceIsPressedAgain(t *testing.T) {
	a := wdAcct(t, Input{UserStarted: true}, nil)
	social := &PageState{URL: "https://" + wdHost + "/x/apply/applyManually", Title: "Sign In", Headings: []string{"Sign In"}, Controls: []Control{
		{I: 8, Tag: "button", Labels: []string{"Sign in with Google"}, Form: -1},
		{I: 9, Tag: "button", Labels: []string{"Sign in with email"}, Form: -1}}}
	a.tab.set(social)
	a.s.entry = social
	a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Pw-1!x")
	a.signInRight("Tenant-Pw-1!x")
	right := a.tab.onClick
	presses := 0
	a.tab.onClick = func(t *scriptTab, c Control) {
		// The page had not wired its buttons at the first press (CVS, live).
		if c.I == 9 {
			if presses++; presses == 2 {
				t.set(loginPage(""))
			}
		}
		right(t, c)
	}
	d, st := a.run()
	if st != nil || !strings.Contains(d, "signed in") || presses != 2 {
		t.Fatalf("detail %q stop %+v presses %d", d, st, presses)
	}
}

// withApplicant answers the profile read with a fictional applicant whose
// country is the given one.
func withApplicant(country string) func(*Deps) {
	return func(d *Deps) {
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example","email":"alex@example.com","phone":"+1 555 0100","address":{"country":"` + country + `"}}}`), nil
		}
	}
}

func TestApply_Account_SigninAfterUnmarkedSignupBothVerdictsReported(t *testing.T) {
	for _, c := range []struct {
		name    string
		signInP string
		ok      bool
	}{{"the sign-in works", "", true}, {"the sign-in is refused too", "Invalid email or password", false}} {
		t.Run(c.name, func(t *testing.T) {
			a := wdAcct(t, Input{Submit: true, ExplicitRequest: true, UserStarted: true}, func(d *Deps) { withApplicant("United States")(d); d.Page.Policy = fakePolicy{} })
			signupPages(a, false) // the form goes away with no success marker
			base := a.tab.onClick
			a.tab.onClick = func(t *scriptTab, ctl Control) {
				if ctl.Labels[0] == "Sign in" {
					t.typed = map[int]string{}
					if c.signInP != "" {
						t.set(loginPage(c.signInP))
						return
					}
				}
				base(t, ctl)
			}
			d, st := a.run()
			if got, err := a.v.Get("tenant-workday-example"); err != nil || got == "" {
				t.Fatalf("the generated password was removed though the account may exist: %v", err)
			}
			if c.ok {
				if st != nil || !strings.Contains(d, "created an account and signed in") {
					t.Fatalf("detail %q stop %+v", d, st)
				}
				return
			}
			if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "showed no success marker") || !strings.Contains(st.reason, "sign-in that followed was refused") {
				t.Fatalf("stop %+v: both verdicts are reported", st)
			}
		})
	}
}

// scriptMail answers the activation lookup from a function and keeps every
// query it saw.
type scriptMail struct {
	fn    func(q gmail.LinkQuery, call int) (gmail.LinkResult, error)
	calls []gmail.LinkQuery
}

func (m *scriptMail) ActivationLink(_ context.Context, q gmail.LinkQuery) (gmail.LinkResult, error) {
	m.calls = append(m.calls, q)
	return m.fn(q, len(m.calls))
}

func (m *scriptMail) ResetLink(context.Context, gmail.LinkQuery) (gmail.LinkResult, error) {
	return gmail.LinkResult{}, &gmail.LookupError{Kind: gmail.KindNotFound}
}

func wdLink(token string, at time.Time) gmail.LinkResult {
	return gmail.LinkResult{URL: "https://" + wdHost + "/activate/" + token, From: "Workday <" + mailFrom("noreply", "otp.workday.com") + ">", ReceivedAt: at}
}

func noMailYet() (gmail.LinkResult, error) {
	return gmail.LinkResult{}, &gmail.LookupError{Kind: gmail.KindNotFound, Message: "no activation email found"}
}

// verifyRig is a Workday account that needs its email confirmed: the sign-in
// shows the verification banner until okAfter activation links were opened.
type verifyRig struct {
	a           *acctEnv
	m           *scriptMail
	activations int
	resends     int
	okAfter     int
}

func newVerifyRig(t *testing.T, okAfter int, fn func(q gmail.LinkQuery, call int) (gmail.LinkResult, error)) *verifyRig {
	t.Helper()
	m := &scriptMail{fn: fn}
	v := &verifyRig{m: m, okAfter: okAfter}
	v.a = wdAcct(t, Input{Submit: true, ExplicitRequest: true, UserStarted: true}, func(d *Deps) { d.Page.Mail, d.Page.Policy = m, fakePolicy{} })
	v.a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Pw-1!x")
	entry := v.a.s.entry
	banner := func() *PageState {
		b := loginPage("")
		b.URL = entry.URL
		b.Inputs, b.Lines = nil, []string{"Please verify your email to continue"}
		b.Controls = []Control{{I: 9, Tag: "button", Labels: []string{"Resend verification email"}, Form: -1}}
		return b
	}
	v.a.tab.onNav = func(tab *scriptTab, url string) {
		if strings.Contains(url, "/activate/") {
			v.activations++
			return
		}
		w := loginPage("")
		w.URL = entry.URL
		tab.set(w)
	}
	v.a.tab.onClick = func(tab *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Sign in":
			tab.typed = map[int]string{}
			if v.activations >= v.okAfter {
				app := appPage()
				app.URL = entry.URL
				tab.set(app)
				return
			}
			tab.set(banner())
		case "Resend verification email":
			v.resends++
		}
	}
	return v
}

func (v *verifyRig) signups(t *testing.T) int {
	return v.a.g.count(`SELECT count(*) FROM signup_attempts`)
}

func TestApply_Account_VerificationOnlyNeverSignsUpResendsOnceWhenStale(t *testing.T) {
	old := t0.Add(-48 * time.Hour)
	t.Run("an existing mail opens and the retry sign-in is the verdict", func(t *testing.T) {
		v := newVerifyRig(t, 1, func(q gmail.LinkQuery, _ int) (gmail.LinkResult, error) {
			return wdLink("Ab12Cd34Ef56", old), nil
		})
		d, st := v.a.run()
		if st != nil || !strings.Contains(d, "activated and signed in") || v.activations != 1 || v.resends != 0 || v.signups(t) != 0 {
			t.Fatalf("detail %q stop %+v activations %d resends %d sign-ups %d", d, st, v.activations, v.resends, v.signups(t))
		}
	})
	t.Run("a stale first link forces one resend and only newer mail is taken", func(t *testing.T) {
		v := newVerifyRig(t, 2, func(q gmail.LinkQuery, call int) (gmail.LinkResult, error) {
			if q.After.IsZero() {
				return wdLink("Ab12Cd34Ef56", old), nil // the stale link
			}
			return wdLink("Zz99Yy88Xx77", q.After.Add(10*time.Second)), nil
		})
		d, st := v.a.run()
		if st != nil || !strings.Contains(d, "activated and signed in") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
		if v.activations != 2 || v.resends != 1 || v.signups(t) != 0 {
			t.Fatalf("activations %d resends %d sign-ups %d", v.activations, v.resends, v.signups(t))
		}
		last := v.m.calls[len(v.m.calls)-1]
		if last.After.IsZero() {
			t.Fatal("the second lookup does not exclude mail older than the resend press")
		}
	})
	t.Run("a mail that cannot be read for want of Gmail marks the host for the Gmail fix", func(t *testing.T) {
		v := newVerifyRig(t, 1, func(gmail.LinkQuery, int) (gmail.LinkResult, error) {
			return gmail.LinkResult{}, &gmail.LookupError{Kind: gmail.KindUnavailable, Reason: gmail.ReasonNotSignedIn, Message: gmail.ErrNotConnected.Error()}
		})
		_, st := v.a.run()
		if st == nil || st.class != ClassCredential || v.activations != 0 || v.a.e.Run.Signins[0].Mail != MailNotConnected {
			t.Fatalf("stop %+v activations %d records %+v", st, v.activations, v.a.e.Run.Signins)
		}
	})
	t.Run("the same stale mail on the second call is refused by the lock", func(t *testing.T) {
		v := newVerifyRig(t, 9, func(q gmail.LinkQuery, _ int) (gmail.LinkResult, error) {
			return wdLink("Ab12Cd34Ef56", old), nil // the lookup returns the old mail even after the press
		})
		_, st := v.a.run()
		if st == nil || st.class != ClassCredential || v.activations != 1 || v.resends != 1 {
			t.Fatalf("stop %+v activations %d resends %d: a stale mail is not opened twice", st, v.activations, v.resends)
		}
		if v.a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'activation_link' AND code = 'mail_not_newer'`) != 1 {
			t.Fatal("the refused stale mail was not audited")
		}
	})
	t.Run("a second stale link stays failed after the one resend", func(t *testing.T) {
		v := newVerifyRig(t, 9, func(q gmail.LinkQuery, _ int) (gmail.LinkResult, error) {
			if q.After.IsZero() {
				return wdLink("Ab12Cd34Ef56", old), nil
			}
			return wdLink("Zz99Yy88Xx77", q.After.Add(time.Minute)), nil
		})
		_, st := v.a.run()
		if st == nil || !strings.Contains(st.reason, "was not accepted") || v.resends != 1 || v.activations != 2 || v.signups(t) != 0 {
			t.Fatalf("stop %+v activations %d resends %d sign-ups %d", st, v.activations, v.resends, v.signups(t))
		}
	})
	t.Run("no mail at all says the credential is fine", func(t *testing.T) {
		v := newVerifyRig(t, 9, func(gmail.LinkQuery, int) (gmail.LinkResult, error) { return noMailYet() })
		_, st := v.a.run()
		if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "stored password is fine") || v.resends != 1 || v.activations != 0 {
			t.Fatalf("stop %+v resends %d", st, v.resends)
		}
	})
	t.Run("the class is the stage's even when the detail names the bridge", func(t *testing.T) {
		v := newVerifyRig(t, 9, func(gmail.LinkQuery, int) (gmail.LinkResult, error) {
			return gmail.LinkResult{}, &gmail.LookupError{Kind: gmail.KindUnavailable, Message: "the bridge is not connected"}
		})
		_, st := v.a.run()
		if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "bridge") {
			t.Fatalf("stop %+v", st)
		}
	})
}

func TestApply_Account_EmailCodeRecoveryBeforeVerifyingSignin(t *testing.T) {
	codePage := func() *PageState {
		return &PageState{
			URL: loginURL, Title: "Verify your email", Text: "Verify your email. Enter the 6-digit code we emailed you.", Lines: []string{"Verify your email", "Enter the code we emailed you."},
			Headings: []string{"Verify your email"},
			Inputs:   []InputInfo{{I: 7, Tag: "input", Type: "text", Label: "Verification code", Name: "otp", Form: -1}},
			Controls: []Control{{I: 8, Tag: "button", Labels: []string{"Verify"}, Form: -1}},
		}
	}
	codes := func() *fakeCodes {
		return &fakeCodes{replies: []codeReply{mailed("482916", mailFrom("no-reply", "careers.example.test"), "Your code", t0.Add(time.Minute))}}
	}
	build := func(t *testing.T, needsCode bool, fc *fakeCodes) (*acctEnv, *[]string) {
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", func(d *Deps) {
			d.Page.Policy, d.Page.Codes, d.Page.Code = fakePolicy{}, fc, fastCode
		})
		a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
		refuseDefault(a)
		base := a.tab.onClick
		events := &[]string{}
		a.tab.onClick = func(tab *scriptTab, c Control) {
			switch {
			case c.Labels[0] == "Create account" && tab.state.Title == "Create account":
				*events = append(*events, "create-account")
				if needsCode {
					tab.set(codePage())
					return
				}
			case c.Labels[0] == "Verify":
				*events = append(*events, "verify:"+tab.typed[7])
				tab.set(loginPage(""))
				return
			case c.Labels[0] == "Sign in" && tab.typed[1] != "Default-Pw-1!x":
				*events = append(*events, "sign-in")
			}
			base(tab, c)
		}
		a.tab.onNav = func(tab *scriptTab, url string) {
			if tab.state.Title == "Verify your email" {
				tab.set(codePage())
			}
		}
		return a, events
	}
	t.Run("the emailed code is typed and verified before the verifying sign-in", func(t *testing.T) {
		fc := codes()
		a, events := build(t, true, fc)
		d, st := a.run()
		if st != nil || !strings.Contains(d, "created an account and signed in") {
			t.Fatalf("detail %q stop %+v", d, st)
		}
		if got := strings.Join(*events, ","); got != "create-account,verify:482916,sign-in" {
			t.Fatalf("order %s", got)
		}
		if !fc.since[0].Before(t0) || fc.domain[0] != "" {
			t.Fatalf("the lookup anchor %v: it starts one second before the sign-up press", fc.since)
		}
	})
	t.Run("a sign-up that reports no verification runs no recovery", func(t *testing.T) {
		fc := codes()
		a, events := build(t, false, fc)
		if _, st := a.run(); st != nil {
			t.Fatalf("stop %+v", st)
		}
		if len(fc.since) != 0 || strings.Contains(strings.Join(*events, ","), "verify") {
			t.Fatalf("lookups %d events %v", len(fc.since), *events)
		}
	})
	t.Run("a failed recovery falls through to the ordinary verdict and is not hidden", func(t *testing.T) {
		a, _ := build(t, true, &fakeCodes{replies: []codeReply{notYet()}})
		_, st := a.run()
		warned := false
		for _, w := range a.e.Run.Warnings {
			warned = warned || strings.Contains(w, "email-code recovery")
		}
		if st == nil || st.class != ClassCredential || !warned || !strings.Contains(st.reason, "did not lead to an application page") {
			t.Fatalf("stop %+v warnings %v: the recovery failure is a warning, the verdict is the sign-in's", st, a.e.Run.Warnings)
		}
	})
}

func TestApply_Account_NeverSignedInOverrideReportedNotDeleted(t *testing.T) {
	hint := "the password saved for careers.example.test has never signed in there - delete or correct it in Settings"
	for _, c := range []struct {
		name   string
		proven bool
	}{{"never signed in", false}, {"signed in before", true}} {
		t.Run(c.name, func(t *testing.T) {
			a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
			a.seed(t, "signin-site-careers.example.test", "signin_site", "careers.example.test", "Site-Pw-0!s")
			if c.proven {
				if err := a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE credential_index SET last_signin_at = '2027-03-01T00:00:00.000Z' WHERE name = 'signin-site-careers.example.test'`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			a.signInRight("never")
			_, st := a.run()
			if st == nil || st.class != ClassCredential || strings.Contains(st.reason, hint) == c.proven {
				t.Fatalf("stop %+v: the hint appears exactly when the password never signed in", st)
			}
			if got := vaultValue(t, a.v, "signin-site-careers.example.test"); got != "Site-Pw-0!s" {
				t.Fatalf("the override changed: %q", got)
			}
			if a.g.count(`SELECT count(*) FROM credential_index WHERE name = 'signin-site-careers.example.test'`) != 1 {
				t.Fatal("the index row of the override was deleted")
			}
		})
	}
}

// Node's account tests (kla.wd1 and invesco.wd1 in one maintenance window): a
// Workday entry page on Workday's maintenance page, or on the employer's own
// site, stops as unavailable before any sign-in; a page on Workday does not.
func TestApply_Account_WorkdayMaintenanceOrOffTenantRedirectIsUnavailable(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"https://community.workday.com/maintenance-page?d=1&s=1&e=1&o=", "Workday is down for scheduled maintenance"},
		{"https://careers.example.com/", "off its own site to careers.example.com"},
		{"https://itron.wd5.myworkdayjobs.com/userHome", ""},
	} {
		a := wdAcct(t, Input{}, nil)
		page := appPage()
		page.URL = c.url
		a.tab.set(page)
		_, st := a.run()
		if c.want == "" {
			if st != nil {
				t.Errorf("%s: stop %+v", c.url, st)
			}
			continue
		}
		if st == nil || st.class != ClassUnavailable || !strings.Contains(st.reason, c.want) || len(a.tab.clicks) != 0 {
			t.Errorf("%s: stop %+v clicks %v", c.url, st, a.tab.clicks)
		}
	}
}

// Node's generic-signin signup-view-toggle test (YC's account gate opens on
// its sign-up view): for a host the store holds a sign-in for, the page's own
// "Log in" toggle is pressed and the stored password is tried on the sign-in
// view it shows; without one, the view is a sign-up.
func TestApply_Account_SignupViewLogInToggleForAKnownHost(t *testing.T) {
	signupView := &PageState{URL: loginURL, Title: "Account", Inputs: []InputInfo{
		{I: 0, Tag: "input", Type: "text", Label: "First Name", Form: 0},
		{I: 1, Tag: "input", Type: "text", Label: "Last Name", Form: 0},
		{I: 2, Tag: "input", Type: "email", Label: "Email", Form: 0},
		{I: 3, Tag: "input", Type: "text", ID: "ycid-input", Name: "username", Autocomplete: "username", Label: "Username", Form: 0},
		{I: 4, Tag: "input", Type: "password", ID: "password-input", Name: "password", Autocomplete: "current-password", Label: "Password", Form: 0},
	}, Controls: []Control{
		{I: 5, Tag: "button", Type: "submit", Labels: []string{"Sign up"}, Form: 0},
		{I: 6, Tag: "button", Type: "button", Labels: []string{"Log in"}, Form: 0},
	}, Forms: []FormInfo{{Fields: 5}}}
	loginView := &PageState{URL: loginURL, Title: "Log in", Inputs: []InputInfo{
		{I: 10, Tag: "input", Type: "text", ID: "ycid-input", Name: "username", Autocomplete: "username", Label: "Username", Form: 0},
		{I: 11, Tag: "input", Type: "password", ID: "password-input", Name: "password", Autocomplete: "current-password", Label: "Password", Form: 0},
	}, Controls: []Control{
		{I: 12, Tag: "button", Type: "submit", Labels: []string{"Log in"}, Form: 0},
		{I: 13, Tag: "button", Type: "button", Labels: []string{"Sign up"}, Form: 0},
	}, Forms: []FormInfo{{Fields: 2}}}
	rig := func(t *testing.T) *acctEnv {
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", signupMod("United States"))
		a.tab.set(signupView)
		a.s.entry = signupView
		a.tab.onClick = func(tab *scriptTab, c Control) {
			switch c.I {
			case 6:
				tab.set(loginView)
			case 12:
				if tab.typed[11] == "Site-Pw-1!x" {
					tab.set(appPage())
				}
			}
		}
		return a
	}
	a := rig(t)
	a.seed(t, "signin-site-careers.example.test", "signin_site", "careers.example.test", "Site-Pw-1!x")
	d, st := a.run()
	if st != nil || !strings.Contains(d, "signed in") || a.tab.clicksOf("Log in") != 2 || a.tab.clicksOf("Sign up") != 0 {
		t.Fatalf("detail %q stop %+v clicks %v", d, st, a.tab.clicks)
	}
	if w := findWall(signupView); w == nil || !w.registration {
		t.Fatalf("wall %+v: the sign-up view is a registration", w)
	}
	a = rig(t)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	_, _ = a.run()
	if a.tab.clicksOf("Log in") != 0 || len(a.e.Run.Signins[0].Tried) != 0 {
		t.Fatalf("clicks %v tried %v: with no sign-in for the host the view is a sign-up and no stored password is typed", a.tab.clicks, a.e.Run.Signins[0].Tried)
	}
	// Workday draws its Sign In toggle as an untyped button outside the form,
	// which submits nothing.
	signupView.Controls[1] = Control{I: 6, Tag: "button", Labels: []string{"Log in"}, Form: -1}
	a = rig(t)
	a.seed(t, "signin-site-careers.example.test", "signin_site", "careers.example.test", "Site-Pw-1!x")
	if d, st := a.run(); st != nil || !strings.Contains(d, "signed in") || a.tab.clicksOf("Sign up") != 0 {
		t.Fatalf("detail %q stop %+v clicks %v: the toggle outside the form leads to the sign-in", d, st, a.tab.clicks)
	}
}

// Campbell's (Workday, live): a re-read after a sign-in landed on the Create
// Account view Workday draws a moment before its Sign In view. An attempt with
// a stored password toggles to the sign-in form instead of stopping; a
// registration view with no toggle still gets no stored password.
func TestApply_Account_AnAttemptOnACreateAccountViewTogglesToSignIn(t *testing.T) {
	create := &PageState{URL: loginURL, Title: "Create Account", Inputs: []InputInfo{
		{I: 0, Tag: "input", Type: "email", Label: "Email Address", Form: 0},
		{I: 1, Tag: "input", Type: "password", Label: "Password", Autocomplete: "new-password", Form: 0},
		{I: 2, Tag: "input", Type: "password", Label: "Verify New Password", Autocomplete: "new-password", Form: 0},
	}, Controls: []Control{
		{I: 3, Tag: "button", Type: "submit", Labels: []string{"Create Account"}, Form: 0},
		{I: 4, Tag: "button", Labels: []string{"Sign In"}, Form: -1},
	}, Forms: []FormInfo{{Fields: 3}}}
	signin := &PageState{URL: loginURL, Title: "Sign In", Inputs: []InputInfo{
		{I: 10, Tag: "input", Type: "email", Label: "Email Address", Form: 0},
		{I: 11, Tag: "input", Type: "password", Label: "Password", Autocomplete: "current-password", Form: 0},
	}, Controls: []Control{{I: 12, Tag: "button", Type: "submit", Labels: []string{"Sign In"}, Form: 0}}, Forms: []FormInfo{{Fields: 2}}}
	c := credentials.Candidate{Label: "tenant-key", Email: "alex@example.com", Password: "Tenant-Pw-1!x"}
	for _, toggle := range []bool{true, false} {
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
		page := *create
		if !toggle {
			page.Controls = page.Controls[:1]
		}
		a.tab.set(&page)
		a.tab.onClick = func(tab *scriptTab, ctl Control) {
			switch ctl.I {
			case 4:
				tab.set(signin)
			case 12:
				if tab.typed[11] == c.Password {
					tab.set(appPage())
				}
			}
		}
		ac := &acct{r: a.g.r, e: a.e, s: a.s, host: "careers.example.test", rec: &SigninRecord{}, tm: fastAccount().withDefaults()}
		got, st := ac.attempt(context.Background(), findWall(&page), c)
		if toggle && (st != nil || got != attemptSignedIn) {
			t.Fatalf("result %v stop %+v clicks %v: the Sign In toggle leads to the form the password belongs on", got, st, a.tab.clicks)
		}
		if !toggle && (st == nil || st.class != ClassDefect || len(a.tab.typed) != 0) {
			t.Fatalf("stop %+v typed %v: a registration view with no toggle gets no stored password", st, a.tab.typed)
		}
	}
}

// Workday's Sign In dialog over its Create Account page: the header's own Sign
// In sits behind the dialog; the dialog's form holds the one to press.
func TestApply_Account_SignInOfThePasswordsFormIsTheWallsSignIn(t *testing.T) {
	st := &PageState{URL: loginURL, Title: "Create Account", Inputs: []InputInfo{
		{I: 1, Tag: "input", Type: "text", Label: "Email Address", Autocomplete: "email", Form: 0, Modal: true},
		{I: 2, Tag: "input", Type: "password", Label: "Password", Autocomplete: "current-password", Form: 0, Modal: true},
	}, Controls: []Control{
		{I: 0, Tag: "button", Labels: []string{"Sign In"}, Form: -1, Chrome: true},
		{I: 3, Tag: "div", Role: "button", Labels: []string{"Sign In"}, Form: 0, Modal: true},
		{I: 4, Tag: "button", Labels: []string{"Create Account"}, Form: -1, Modal: true},
	}, Forms: []FormInfo{{Fields: 2}}}
	if w := findWall(st); w == nil || w.signin == nil || w.signin.I != 3 {
		t.Fatalf("wall %+v: the dialog's Sign In is the one to press", w)
	}
}

// An application page whose fraud notice names "bank account details" in a
// sentence is no account-details step (Aptos Labs on Greenhouse); the step's
// own short line still is.
func TestApply_AccountDetails_ANoticeSentenceIsNoStep(t *testing.T) {
	page := &PageState{
		URL: "https://job-boards.greenhouse.io/acme/jobs/1", Title: "Job Application for Senior Software Engineer at Acme",
		Headings: []string{"Senior Software Engineer", "Apply for this job"},
		Lines: []string{
			"We will never request your personal financial information, such as your social security number or bank account details, during the initial application stages or via email or a video/voice call when onboarding.",
			"Apply for this job",
		},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "text", Label: "First Name", Required: true, Form: 0},
			{I: 1, Tag: "input", Type: "text", Label: "Last Name", Required: true, Form: 0},
			{I: 2, Tag: "input", Type: "file", Label: "Resume/CV", Required: true, Form: 0},
		},
		Controls: []Control{{I: 3, Tag: "button", Labels: []string{"Submit application"}, Form: 0}},
	}
	if d := readDetails(page); d != nil {
		t.Fatalf("the application page was read as an account-details step: %+v", d)
	}
	step := detailsPage(false, "")
	step.Title, step.Headings, step.Lines = "Careers", nil, []string{"Account details"}
	if readDetails(step) == nil {
		t.Fatal("the step's own short line no longer introduces it")
	}
}
