package apply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/vault"
)

type acctEnv struct {
	g     *rig
	tab   *scriptTab
	e     *Exec
	s     *session
	cs    *credentials.Service
	v     *vault.Vault
	wrong int
}

func fastAccount() AccountTiming {
	return AccountTiming{Stable: 40 * time.Millisecond, Google: 1500 * time.Millisecond, Answer: 1500 * time.Millisecond, Poll: 10 * time.Millisecond, MailWait: 300 * time.Millisecond, MailPoll: 10 * time.Millisecond, Settle: 5 * time.Millisecond, Boxes: 100 * time.Millisecond, CodePoll: 5 * time.Millisecond, Dwell: time.Millisecond}
}

func newAcct(t *testing.T, in Input, ats, tenant string, mod func(*Deps)) *acctEnv {
	t.Helper()
	g := newRig(t, func(d *Deps) {
		d.Page.Account = fastAccount()
		if mod != nil {
			mod(d)
		}
	})
	v, err := vault.New("ws", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	cs := credentials.New(g.st, v, g.r.d.Now)
	g.r.d.Page.Credentials = cs
	tab := newScriptTab(loginPage(""))
	run := &Run{ID: "run_acct", StartedAt: t0, Input: in, ATS: ats, Tenant: tenant}
	e := &Exec{r: g.r, Run: run, Ctx: context.Background()}
	s := &session{r: g.r, e: e, tab: tab, entry: tab.state}
	return &acctEnv{g: g, tab: tab, e: e, s: s, cs: cs, v: v}
}

func (a *acctEnv) seed(t *testing.T, name, kind, host, pw string) {
	t.Helper()
	if _, err := a.v.Set(name, pw); err != nil {
		t.Fatal(err)
	}
	err := a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO credential_index (name, kind, host, email, has_secret, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), 'alex@example.com', 1, '2027-03-01T00:00:00.000Z', '2027-03-01T00:00:00.000Z')`, name, kind, host)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// signInRight makes the sign-in control accept exactly one password.
func (a *acctEnv) signInRight(right string) {
	a.tab.onClick = func(t *scriptTab, c Control) {
		if len(c.Labels) == 0 || c.Labels[0] != "Sign in" {
			return
		}
		pw := t.typed[1]
		t.typed = map[int]string{}
		if pw == right {
			t.set(appPage())
			return
		}
		a.wrong++
		t.set(loginPage("Invalid email or password"))
	}
}

func (a *acctEnv) run() (string, *stop) { return a.g.r.account(context.Background(), a.e, a.s) }

func TestApply_Account_NoWallNoWork(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", nil)
	a.tab.set(appPage())
	a.s.entry = a.tab.state
	d, st := a.run()
	if st != nil || !strings.Contains(d, "no sign-in wall") || len(a.tab.typed) != 0 {
		t.Fatalf("detail %q stop %+v typed %v", d, st, a.tab.typed)
	}
}

func TestApply_Account_TryOrderStopsAtFirstSuccessAndAlternateBecomesTheSiteOverride(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.seed(t, "signin-alternate-0123456789ab", "signin_alternate", "", "Alt-One-2!y")
	a.seed(t, "signin-alternate-ba9876543210", "signin_alternate", "", "Alt-Two-3!z")
	// Nothing is known for the host yet, so the gate keeps one submission back:
	// two guesses, the second of which signs in.
	a.signInRight("Alt-One-2!y")
	d, st := a.run()
	if st != nil {
		t.Fatalf("stop %+v", st)
	}
	rec := a.e.Run.Signins[0]
	if got := strings.Join(rec.Tried, ","); got != "settings-default,alternate-1" || rec.Succeeded != "alternate-1" || rec.Refusals != 1 {
		t.Fatalf("record %+v (%s)", rec, d)
	}
	cands, _ := a.cs.Candidates(context.Background(), "careers.example.test", ATSGeneric, "careers.example.test")
	if cands[0].Kind != credentials.KindSite || cands[0].Password != "Alt-One-2!y" {
		t.Fatalf("the winning alternate was not kept as the site override: %+v", cands[0])
	}
	if cands[1].Kind != credentials.KindDefault || cands[1].Password != "Default-Pw-1!x" {
		t.Fatalf("the shared default changed: %+v", cands[1])
	}
	if a.g.audits(gates.GatePasswordSubmission) != 2 {
		t.Fatalf("password submission audit rows: %d", a.g.audits(gates.GatePasswordSubmission))
	}
	if strings.Contains(stringify(a.e.Run), "Alt-One-2!y") {
		t.Fatal("a password reached the run record")
	}
}

func stringify(r *Run) string {
	var b strings.Builder
	for _, l := range r.Stages {
		b.WriteString(l.Detail)
	}
	for _, s := range r.Signins {
		b.WriteString(strings.Join(s.Tried, ""))
		b.WriteString(s.Succeeded)
	}
	b.WriteString(r.Outcome.Reason)
	return b.String()
}

func TestApply_Account_ThreeRefusalsExhaustTheHostAndTheHoldBlocksTheNextRun(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	// The host's own override makes the account known, so all three refusals
	// the budget allows are spent before the host is exhausted.
	a.seed(t, "signin-site-careers.example.test", "signin_site", "careers.example.test", "Site-Pw-0!s")
	for i, id := range []string{"0123456789ab", "ba9876543210", "aaaaaaaaaaaa", "bbbbbbbbbbbb"} {
		a.seed(t, "signin-alternate-"+id, "signin_alternate", "", "Alt-Pw-"+string(rune('1'+i))+"!q")
	}
	a.signInRight("never")
	_, st := a.run()
	if st == nil || st.class != ClassCredential || st.board != "credential-refused" {
		t.Fatalf("stop %+v", st)
	}
	if a.wrong != 3 {
		t.Fatalf("%d passwords were submitted: the budget is 3 refusals per host per run", a.wrong)
	}
	if !a.e.Run.Signins[0].Exhausted {
		t.Fatal("the host was not recorded exhausted")
	}
	// The next run submits nothing on this host.
	b := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	b.g.st = a.g.st
	b.g.r.d.Page.Credentials = a.cs
	b.e.r = b.g.r
	b.g.r.d.Store = a.g.st
	b.g.r.d.Audit = a.g.r.d.Audit
	b.signInRight("never")
	_, st = b.run()
	if st == nil || !strings.Contains(st.reason, "ran out earlier today") || b.wrong != 0 || len(b.tab.typed) != 0 {
		t.Fatalf("stop %+v wrong %d typed %v", st, b.wrong, b.tab.typed)
	}
}

func TestApply_Account_LockoutWordingStopsAtOnceAndHoldsWithLockout(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.seed(t, "signin-alternate-0123456789ab", "signin_alternate", "", "Alt-Pw-1!q")
	a.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Sign in" {
			st := loginPage("Your account has been locked after too many attempts.")
			st.Text = "Your account has been locked after too many attempts."
			t.set(st)
		}
	}
	_, st := a.run()
	if st == nil || st.board != "lockout" || st.class != ClassCredential {
		t.Fatalf("stop %+v", st)
	}
	if n := len(a.e.Run.Signins[0].Tried); n != 1 {
		t.Fatalf("%d passwords tried after lockout wording", n)
	}
	var lock bool
	if err := a.g.st.Read().QueryRow(`SELECT lockout FROM signin_holds WHERE host = 'careers.example.test'`).Scan(&lock); err != nil || !lock {
		t.Fatalf("hold lockout=%v err=%v", lock, err)
	}
}

type fakeSync struct {
	res   OwnHostResult
	err   error
	calls []string
}

func (f *fakeSync) SyncOwnHost(_ context.Context, host string) (OwnHostResult, error) {
	f.calls = append(f.calls, host)
	return f.res, f.err
}

// A run cancelled while it records a sign-up attempt or a host's hold still
// writes the row: both are what the next run's limits read.
func TestApply_Account_CancelledRunStillRecordsItsAttemptAndItsHold(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	ac := a.s.newAcct("careers.example.test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ac.recordAttempt(ctx); err != nil || a.g.count(`SELECT count(*) FROM signup_attempts`) != 1 {
		t.Fatalf("attempt: err %v, rows %d", err, a.g.count(`SELECT count(*) FROM signup_attempts`))
	}
	ac.exhausted(ctx, "three passwords were refused")
	if n := a.g.count(`SELECT count(*) FROM signin_holds WHERE host = 'careers.example.test'`); n != 1 || len(a.e.Run.Warnings) != 0 {
		t.Fatalf("hold rows %d, warnings %v", n, a.e.Run.Warnings)
	}
}

func TestApply_Account_SessionImportBeforePasswordsOncePerHost(t *testing.T) {
	sy := &fakeSync{res: OwnHostResult{Imported: true}}
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Sessions = sy })
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.tab.onNav = func(t *scriptTab, url string) { t.set(appPage()) }
	d, st := a.run()
	if st != nil || !strings.Contains(d, "imported session") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if len(a.tab.typed) != 0 || a.e.Run.Signins[0].Session != "imported" || len(sy.calls) != 1 {
		t.Fatalf("a password was submitted though the session carried the sign-in: typed %v sync %v", a.tab.typed, sy.calls)
	}
	// Asked once per host per run: a second account stage on the same run asks no more.
	a.tab.set(loginPage(""))
	a.s.entry = a.tab.state
	a.signInRight("Default-Pw-1!x")
	if _, st := a.run(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if len(sy.calls) != 1 {
		t.Fatalf("session import asked %d times for one host", len(sy.calls))
	}
}

func TestApply_Account_SessionImportNeverAsksForGoogleAndUnavailableFallsToPasswords(t *testing.T) {
	sy := &fakeSync{res: OwnHostResult{Reason: "no-cookies"}}
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "accounts.google.com", func(d *Deps) { d.Page.Sessions = sy })
	st0 := loginPage("")
	st0.URL = "https://accounts.google.com/signin"
	a.tab.set(st0)
	a.s.entry = st0
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.signInRight("Default-Pw-1!x")
	if _, st := a.run(); st != nil || len(sy.calls) != 0 {
		t.Fatalf("stop %+v, sync calls %v: google.com is never asked for", st, sy.calls)
	}
	b := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Sessions = sy })
	b.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	b.signInRight("Default-Pw-1!x")
	if _, st := b.run(); st != nil || len(sy.calls) != 1 || !strings.HasPrefix(b.e.Run.Signins[0].Session, "not available") {
		t.Fatalf("stop %+v calls %v record %+v", st, sy.calls, b.e.Run.Signins)
	}
}

func TestApply_Account_ReloadMidPollIsNotSignedIn(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] != "Sign in" {
			return
		}
		// The form is absent for one poll, then comes back: a reload, not a sign-in.
		t.script(appPage(), loginPage(""), loginPage(""), loginPage(""), loginPage(""))
	}
	_, st := a.run()
	if st == nil || st.class != ClassCredential {
		t.Fatalf("a form that came back within the stable interval read as signed in: %+v", st)
	}
}

func TestApply_Account_NothingStoredIsACredentialStopAndARunNoUserStartedNeverCreatesAnAccount(t *testing.T) {
	// A row of a batch the app resumed after a restart (AP-1).
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerDashboard, Tool: gates.ToolApplyFromList, BatchID: "list_1"}, ATSGeneric, "careers.example.test", nil)
	_, st := a.run()
	if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "account creation needs a run you started") {
		t.Fatalf("stop %+v", st)
	}
	if len(a.tab.clicks) != 0 || len(a.tab.typed) != 0 {
		t.Fatalf("a run no user started pressed %v and typed %v", a.tab.clicks, a.tab.typed)
	}
	if a.g.audits(gates.GateAccountCreation) != 1 {
		t.Fatal("the account creation decision was not audited")
	}
}

type fakePolicy struct {
	cd  []gates.Cooldown
	err error
}

func (p fakePolicy) Cooldowns(context.Context) ([]gates.Cooldown, error) { return p.cd, p.err }

// coolingOn is a policy whose cooldown on key outlasts every test.
func coolingOn(key string) fakePolicy {
	return fakePolicy{cd: []gates.Cooldown{{System: key, End: math.MaxInt64}}}
}

func signupPages(a *acctEnv, refuse bool) {
	signupForm := func(err string) *PageState {
		st := &PageState{
			URL: loginURL, Title: "Create account", Text: "Create account " + err, Lines: []string{"Create account", err},
			Inputs: []InputInfo{
				{I: 0, Tag: "input", Type: "email", Label: "Email", Form: -1},
				{I: 4, Tag: "input", Type: "email", Label: "Confirm email", Form: -1},
				{I: 1, Tag: "input", Type: "password", Label: "Password", Form: -1, MaxLength: 20},
				{I: 5, Tag: "input", Type: "password", Label: "Confirm password", Form: -1, MaxLength: 20},
			},
			Controls: []Control{{I: 2, Tag: "button", Labels: []string{"Create account"}, Form: -1}},
		}
		if err != "" {
			st.Validation = []string{err}
		}
		return st
	}
	a.tab.onClick = func(t *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Create account":
			if t.state.Title == "Create account" {
				if refuse {
					t.set(signupForm("That email address is not allowed."))
					return
				}
				t.set(loginPage("")) // the account exists; the sign-in that follows is the verdict
				return
			}
			t.set(signupForm(""))
		case "Sign in":
			t.set(appPage())
		}
	}
}

func TestApply_Account_SignupBehindTheGateGeneratesAStoredPasswordAndSignsInAfterwards(t *testing.T) {
	// A run the user started creates the account whether it submits or only
	// fills (F-8), and the dry run's sign-up counts toward the quota too.
	for _, tc := range []struct {
		name string
		in   Input
	}{
		{"a run that submits", Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}},
		{"a dry run retried from the board", Input{URL: loginURL, Caller: gates.CallerDashboard, Tool: ToolAutofill, ExplicitRequest: true, UserStarted: true}},
		{"a dry run the Ask Agent started to fill the form", Input{URL: loginURL, Caller: gates.CallerAskAgent, Tool: ToolAutofill, UserStarted: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAcct(t, tc.in, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Policy = fakePolicy{} })
			a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
			// The shared default is the host's only guess and it is refused.
			signupPages(a, false)
			orig := a.tab.onClick
			var sent map[int]string
			a.tab.onClick = func(t *scriptTab, c Control) {
				if c.Labels[0] == "Sign in" && t.typed[1] == "Default-Pw-1!x" {
					t.typed = map[int]string{}
					t.set(loginPage("Invalid email or password"))
					return
				}
				if c.Labels[0] == "Create account" && t.state.Title == "Create account" {
					sent = map[int]string{0: t.typed[0], 4: t.typed[4]}
				}
				orig(t, c)
			}
			d, st := a.run()
			if st != nil {
				t.Fatalf("stop %+v", st)
			}
			if !strings.Contains(d, "created an account") {
				t.Fatalf("detail %q", d)
			}
			pw, err := a.v.Get("signin-site-careers.example.test")
			if err != nil || len(pw) == 0 || len(pw) > 20 {
				t.Fatalf("generated password: %q %v (the site states a 20 character maximum)", pw, err)
			}
			if a.g.count(`SELECT count(*) FROM signup_attempts WHERE system = 'generic'`) != 1 {
				t.Fatal("the attempt was not recorded for the quota")
			}
			if sent[0] != "alex@example.com" || sent[4] != "alex@example.com" {
				t.Fatalf("email boxes at the press %v: Email and Confirm email both hold the email", sent)
			}
		})
	}
}

func TestApply_Account_RefusedSharedPasswordsOnAnUnknownWorkdayAccountSignUp(t *testing.T) {
	// No tenant key and no site override: the account may not exist yet, so
	// the refused shared passwords lead to a sign-up, not to a held host. A
	// hold from an earlier run's refusals blocks guesses, never the sign-up.
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprintf("held=%v", held), func(t *testing.T) {
			a := wdAcct(t, Input{ExplicitRequest: true, UserStarted: true}, func(d *Deps) { d.Page.Policy = fakePolicy{} })
			a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
			a.seed(t, "signin-alternate-0123456789ab", "signin_alternate", "", "Alt-Pw-1!q")
			if held {
				if err := a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO signin_holds (host, recorded_at, day, lockout, run_id) VALUES (?, ?, ?, 0, 'run_old')`, wdHost, t0.Add(-time.Hour).UTC().Format(stampLayout), t0.Format("2006-01-02"))
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			signupPages(a, false)
			orig := a.tab.onClick
			guesses := 0
			a.tab.onClick = func(t *scriptTab, c Control) {
				if pw := t.typed[1]; c.Labels[0] == "Sign in" && (pw == "Default-Pw-1!x" || pw == "Alt-Pw-1!q") {
					guesses++
					t.typed = map[int]string{}
					t.set(loginPage("Wrong email or password"))
				} else {
					orig(t, c)
				}
				t.state.URL = a.s.entry.URL // every page stays on the tenant
			}
			d, st := a.run()
			if st != nil {
				t.Fatalf("stop %+v", st)
			}
			if !strings.Contains(d, "created an account") {
				t.Fatalf("detail %q", d)
			}
			if held && guesses != 0 {
				t.Fatalf("%d shared passwords were submitted on a held host", guesses)
			}
		})
	}
}

func TestApply_Signup_CreateControlDrawnAfterTheWallIsWaitedFor(t *testing.T) {
	// Workday draws "Create Account" under its sign-in form after the form.
	a := wdAcct(t, Input{ExplicitRequest: true, UserStarted: true}, func(d *Deps) { d.Page.Policy = fakePolicy{} })
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	if err := a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO signin_holds (host, recorded_at, day, lockout, run_id) VALUES (?, ?, ?, 0, 'run_old')`, wdHost, t0.Add(-time.Hour).UTC().Format(stampLayout), t0.Format("2006-01-02"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	signupPages(a, false)
	full := loginPage("")
	full.URL = a.s.entry.URL
	bare := loginPage("")
	bare.URL = a.s.entry.URL
	bare.Controls = bare.Controls[:1]
	early := make([]*PageState, 20)
	for i := range early {
		early[i] = bare
	}
	a.tab.set(bare)
	a.tab.script(append(early, full)...)
	orig := a.tab.onClick
	a.tab.onClick = func(t *scriptTab, c Control) {
		orig(t, c)
		t.state.URL = a.s.entry.URL
	}
	d, st := a.run()
	if st != nil || !strings.Contains(d, "created an account") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
}

func TestApply_Signup_ACreatePressThatAnswersLateIsReadFromThePage(t *testing.T) {
	// Workday's renderer was busy as "Create Account" was pressed: the input
	// dispatch timed out after the press landed, and the form it opened decides.
	a := wdAcct(t, Input{ExplicitRequest: true, UserStarted: true}, func(d *Deps) { d.Page.Policy = fakePolicy{} })
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	if err := a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO signin_holds (host, recorded_at, day, lockout, run_id) VALUES (?, ?, ?, 0, 'run_old')`, wdHost, t0.Add(-time.Hour).UTC().Format(stampLayout), t0.Format("2006-01-02"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	signupPages(a, false)
	orig := a.tab.onClick
	a.tab.clickErr = &browser.Error{Code: browser.CodeOperationTimeout, Message: "a click timed out after 10s"}
	a.tab.onClick = func(t *scriptTab, c Control) {
		t.clickErr = nil // only the first press answers late
		orig(t, c)
		t.state.URL = a.s.entry.URL
	}
	d, st := a.run()
	if st != nil || !strings.Contains(d, "created an account") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
}

func TestApply_Signup_PasswordSavedOnlyAfterFormFoundRemovedOnRefusal(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Policy = fakePolicy{} })
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	signupPages(a, true)
	orig := a.tab.onClick
	a.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Sign in" {
			t.typed = map[int]string{}
			t.set(loginPage("Invalid email or password"))
			return
		}
		orig(t, c)
	}
	_, st := a.run()
	if st == nil || !strings.Contains(st.reason, "no account was created") {
		t.Fatalf("stop %+v", st)
	}
	if _, err := a.v.Get("signin-site-careers.example.test"); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("the refused sign-up's password was kept: %v", err)
	}
}

// The sign-up asks the system's cooldowns before anything is typed: a running
// cooldown on this host's key refuses and is audited, another system's does
// not, and cooldowns that cannot be read may be running, so they refuse too.
func TestApply_Signup_TheCooldownIsAskedBeforeAnythingIsTyped(t *testing.T) {
	for _, c := range []struct {
		name   string
		policy fakePolicy
		class  Class
		reason string
	}{
		{"a cooldown on the host's own key", coolingOn("generic:careers.example.test"), ClassCredential, "careers.example.test"},
		{"another system's cooldown", coolingOn("workday"), "", ""},
		{"cooldowns that cannot be read", fakePolicy{err: errors.New("database is locked")}, ClassEnvironment, "cooldowns could not be read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Policy = c.policy })
			if c.reason == "" {
				// The shared default is refused, so the run reaches the sign-up.
				a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
				refuseDefault(a)
				if _, st := a.run(); st != nil {
					t.Fatalf("stop %+v", st)
				}
				return
			}
			signupPages(a, false)
			_, st := a.run()
			if st == nil || st.class != c.class || !strings.Contains(st.reason, c.reason) {
				t.Fatalf("stop %+v", st)
			}
			if len(a.tab.typed) != 0 || len(a.tab.clicks) != 0 || a.g.count(`SELECT count(*) FROM signup_attempts`) != 0 {
				t.Fatalf("typed %v clicked %v: the sign-up went on past its cooldowns", a.tab.typed, a.tab.clicks)
			}
			if c.class != ClassCredential {
				return
			}
			if a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'account_creation' AND code = 'cooldown_active'`) != 1 {
				t.Fatal("the refusal was not audited")
			}
			// The inbox item reads the refusing check from the host's sign-in record.
			if got := a.e.Run.Signins[0].Signup; got != gates.CodeCooldownActive {
				t.Fatalf("the record keeps %q", got)
			}
		})
	}
}

type fakeMail struct {
	link  gmail.LinkResult
	err   error
	calls int
	// down is how many first lookups find Gmail not answering.
	down int
}

func (f *fakeMail) ActivationLink(context.Context, gmail.LinkQuery) (gmail.LinkResult, error) {
	f.calls++
	if f.calls <= f.down {
		return gmail.LinkResult{}, &gmail.LookupError{Kind: gmail.KindUnavailable, Reason: gmail.ReasonUnreachable, Retryable: true, Message: "Gmail could not be reached; try again shortly"}
	}
	return f.link, f.err
}
func (f *fakeMail) ResetLink(context.Context, gmail.LinkQuery) (gmail.LinkResult, error) {
	return gmail.LinkResult{}, &gmail.LookupError{Kind: gmail.KindNotFound}
}

func TestApply_Activation_BannerOpensTheLinkSignsInAgainAndTheSecondSignInIsTheVerdict(t *testing.T) {
	host := "example.wd5.myworkdayjobs.com"
	m := &fakeMail{link: gmail.LinkResult{URL: "https://" + host + "/activate/Ab12Cd34Ef56", From: "Workday <" + mailFrom("noreply", "otp.workday.com") + ">", ReceivedAt: t0.Add(-time.Hour)}}
	a := newAcct(t, Input{URL: "https://" + host + "/en-US/Careers/job/X/Y_R1", Caller: gates.CallerCLI}, ATSWorkday, "example", func(d *Deps) { d.Page.Mail = m })
	st0 := loginPage("")
	st0.URL = "https://" + host + "/en-US/Careers/job/X/Y_R1/apply/applyManually"
	a.tab.set(st0)
	a.s.entry = st0
	a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Pw-1!x")
	activated := false
	a.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] != "Sign in" {
			return
		}
		t.typed = map[int]string{}
		if !activated {
			v := loginPage("")
			v.URL = st0.URL
			v.Inputs, v.Lines = nil, []string{"Please verify your email to continue", "We sent a verification email."}
			t.set(v)
			return
		}
		app := appPage()
		app.URL = st0.URL
		t.set(app)
	}
	// On the verification banner the form is not shown; after activation the
	// page is reopened and shows the wall again.
	a.tab.onNav = func(t *scriptTab, url string) {
		if strings.Contains(url, "/activate/") {
			activated = true
			return
		}
		if activated {
			l := loginPage("")
			l.URL = st0.URL
			t.set(l)
		}
	}
	d, st := a.run()
	if st != nil || !activated || !strings.Contains(d, "activated") {
		t.Fatalf("detail %q stop %+v activated=%v navs=%v", d, st, activated, a.tab.navs)
	}
	if a.g.audits(gates.GateActivationLink) != 1 || a.g.audits(gates.GateMailLink) != 1 {
		t.Fatalf("audit rows: activation %d mail-link %d", a.g.audits(gates.GateActivationLink), a.g.audits(gates.GateMailLink))
	}
}

// Workday answers a new account with a few seconds on the posting, its form
// gone and its header still offering Sign In, before it moves to the sign-in
// page that asks for the activation (ERCOT, Capital One, live). Those seconds
// are no sign-in: the stage waits for the sign-in page, activates, and only the
// page whose header shows the account menu signs it in.
func TestApply_Signup_WorkdaysWaitBeforeTheActivationPageIsNoSignIn(t *testing.T) {
	host := "example.wd5.myworkdayjobs.com"
	apply := "https://" + host + "/en-US/Careers/job/X/Y_R1/apply/applyManually"
	m := &fakeMail{link: gmail.LinkResult{URL: "https://" + host + "/activate/Ab12Cd34Ef56", From: "Workday <" + mailFrom("noreply", "otp.workday.com") + ">", ReceivedAt: t0.Add(-time.Hour)}}
	a := newAcct(t, Input{URL: apply, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, ATSWorkday, "example",
		func(d *Deps) { d.Page.Mail, d.Page.Policy = m, fakePolicy{} })
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	refuseDefault(a)
	login := func(lines ...string) *PageState {
		l := loginPage("")
		l.URL, l.Account = "https://"+host+"/en-US/Careers/login?redirect=x", accountSignedOut
		if len(lines) > 0 {
			l.Inputs, l.Lines = nil, lines
		}
		return l
	}
	// The shared sign-in and sign-up pages, on the tenant's own host.
	first := loginPage("")
	first.URL, first.Account = apply, accountSignedOut
	a.tab.set(first)
	a.s.entry = first
	activated := false
	base := a.tab.onClick
	onHost := func(tab *scriptTab) {
		if tab.state != nil && tab.state.URL == loginURL {
			tab.state.URL, tab.url = apply, apply
		}
	}
	a.tab.onClick = func(tab *scriptTab, c Control) {
		defer onHost(tab)
		switch {
		case c.Labels[0] == "Create account" && tab.state.Title == "Create account":
			posting := &PageState{URL: apply, Title: "Software Engineer", Text: "Software Engineer", Lines: []string{"Software Engineer"}, Account: accountSignedOut}
			waits := make([]*PageState, 0, 21)
			for range 20 {
				waits = append(waits, posting)
			}
			tab.script(append(waits, login())...)
			return
		case c.Labels[0] == "Sign in" && tab.typed[1] != "Default-Pw-1!x":
			tab.typed = map[int]string{}
			if !activated {
				tab.set(login("Please verify your email to continue", "We sent a verification email."))
				return
			}
			app := appPage()
			app.URL, app.Account = apply, accountSignedIn
			tab.set(app)
			return
		}
		base(tab, c)
	}
	a.tab.onNav = func(tab *scriptTab, url string) {
		if strings.Contains(url, "/activate/") {
			activated = true
			return
		}
		if activated {
			tab.set(login())
		}
	}
	d, st := a.run()
	if st != nil || !activated || !strings.Contains(d, "activated and signed in") {
		t.Fatalf("detail %q stop %+v activated=%v navs=%v", d, st, activated, a.tab.navs)
	}
}

// A verification wall whose mail never came stops saying the stored password
// is fine; Workday's own tenant prints its activation address as text, which
// the run never opens, so that stop says the mail came and sends the user to it.
func TestApply_Activation_AStopNamesTheMailItLacks(t *testing.T) {
	host := "example.wd5.myworkdayjobs.com"
	for _, c := range []struct {
		name      string
		err       *gmail.LookupError
		want, not string
	}{
		{"no mail within the wait", &gmail.LookupError{Kind: gmail.KindNotFound, Message: "no activation email found"}, "no activation email", ""},
		{"a mail whose link the run may not open", &gmail.LookupError{Kind: gmail.KindNotFound, Reason: gmail.ReasonLinkRefused, Message: "the activation email came with no link the run may open"}, "open it from your inbox", "no activation email"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &fakeMail{err: c.err}
			a := newAcct(t, Input{URL: "https://" + host + "/x", Caller: gates.CallerCLI}, ATSWorkday, "example", func(d *Deps) { d.Page.Mail = m })
			st0 := loginPage("")
			st0.URL = "https://" + host + "/x"
			a.tab.set(st0)
			a.s.entry = st0
			a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Pw-1!x")
			a.tab.onClick = func(t *scriptTab, c Control) {
				if c.Labels[0] == "Sign in" {
					v := loginPage("")
					v.Inputs, v.Lines = nil, []string{"Please verify your email to continue"}
					v.Controls = []Control{{I: 9, Tag: "button", Labels: []string{"Resend verification email"}, Form: -1}}
					t.set(v)
				}
			}
			_, st := a.run()
			if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, c.want) || (c.not != "" && strings.Contains(st.reason, c.not)) {
				t.Fatalf("stop %+v", st)
			}
			if c.not == "" && (m.calls < 2 || !strings.Contains(st.reason, "stored password is fine") && !strings.Contains(st.reason, "no mail newer than the request")) {
				t.Fatalf("stop %q after %d lookups: the stop does not say the credential is fine", st.reason, m.calls)
			}
		})
	}
}

func TestApply_Activation_WrongSenderOrHostIsRefusedByTheLocks(t *testing.T) {
	host := "example.wd5.myworkdayjobs.com"
	for name, link := range map[string]gmail.LinkResult{
		"sender":   {URL: "https://" + host + "/activate/Ab12Cd34Ef56", From: "x@evil.example.test", ReceivedAt: t0},
		"host":     {URL: "https://other.myworkdayjobs.com/activate/Ab12Cd34Ef56", From: mailFrom("n", "otp.workday.com"), ReceivedAt: t0},
		"not-http": {URL: "http://" + host + "/activate/Ab12Cd34Ef56", From: mailFrom("n", "otp.workday.com"), ReceivedAt: t0},
	} {
		t.Run(name, func(t *testing.T) {
			m := &fakeMail{link: link}
			a := newAcct(t, Input{URL: "https://" + host + "/x", Caller: gates.CallerCLI}, ATSWorkday, "example", func(d *Deps) { d.Page.Mail = m })
			st0 := loginPage("")
			st0.URL = "https://" + host + "/x"
			a.tab.set(st0)
			a.s.entry = st0
			a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Pw-1!x")
			a.tab.onClick = func(t *scriptTab, c Control) {
				if c.Labels[0] == "Sign in" {
					v := loginPage("")
					v.Inputs, v.Lines = nil, []string{"Please verify your email to continue"}
					t.set(v)
				}
			}
			_, st := a.run()
			if st == nil || len(a.tab.navs) != 0 {
				t.Fatalf("stop %+v navigations %v: a link that fails a lock must not be opened", st, a.tab.navs)
			}
			if m.calls < 1 || a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'activation_link' AND verdict = 'deny'`) != 1 {
				t.Fatalf("lookups %d: the mail was read and its link refused by a lock", m.calls)
			}
		})
	}
}

func TestApply_Signup_ApplicationFormStopsBeforeAnyTyping(t *testing.T) {
	st := &PageState{
		URL: loginURL, Title: "Create account", Text: "Create account", Lines: []string{"Create account"},
		Inputs: []InputInfo{
			{I: 0, Type: "email", Label: "Email", Form: -1}, {I: 1, Type: "password", Label: "Password", Form: -1},
			{I: 2, Type: "file", Label: "Resume", Form: -1},
		},
		Controls: []Control{{I: 3, Tag: "button", Labels: []string{"Create account"}, Form: -1}},
	}
	f := readSignupForm(st)
	if f == nil || !f.combined {
		t.Fatalf("a create-account form that also asks for a résumé reads as %+v", f)
	}
	// Sending the form is the application, so only a run that submits
	// registers on it; a dry run the user started stops before it.
	for _, tc := range []struct {
		name   string
		in     Input
		handed bool
	}{
		{"a run that submits", Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}, true},
		{"a dry run you started", Input{URL: loginURL, Caller: gates.CallerDashboard, Tool: ToolAutofill, ExplicitRequest: true, UserStarted: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAcct(t, tc.in, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Policy = fakePolicy{} })
			a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
			a.tab.onClick = func(t *scriptTab, c Control) {
				switch c.Labels[0] {
				case "Create account":
					if t.state.Title == "Create account" {
						return
					}
					t.set(st)
				case "Sign in":
					t.typed = map[int]string{}
					t.set(loginPage("Invalid email or password"))
				}
			}
			detail, stp := a.run()
			if tc.handed && (stp != nil || !strings.Contains(detail, "application walk registers on it") || a.e.combined == nil) {
				t.Fatalf("detail %q stop %+v combined %+v: the sign-up lane hands the page to the walk", detail, stp, a.e.combined)
			}
			if !tc.handed && (stp == nil || stp.class != ClassCredential || !strings.Contains(stp.reason, "only a run that submits") || a.e.combined != nil) {
				t.Fatalf("detail %q stop %+v combined %+v: a dry run leaves the combined form to a run that submits", detail, stp, a.e.combined)
			}
			if len(a.tab.typed) != 0 {
				t.Fatalf("typed %v: nothing is typed on a combined form by the account stage", a.tab.typed)
			}
			if a.g.count(`SELECT count(*) FROM signup_attempts`) != 0 {
				t.Fatal("an attempt was counted before any account existed")
			}
			if _, err := a.v.Get("signin-site-careers.example.test"); err == nil {
				t.Fatal("a password was saved for a combined form")
			}
		})
	}
}

func TestChain_Activation_SuccessFactorsHostsOnly(t *testing.T) {
	for host, want := range map[string]gates.ATS{
		"career5.successfactors.com": gates.ATSSuccessFactors, "career41.sapsf.eu": gates.ATSSuccessFactors, "career.sapsf.com": gates.ATSSuccessFactors,
		"www.successfactors.com": gates.ATSGeneric, "sapsf.com": gates.ATSGeneric, "career5.successfactors.com.evil.test": gates.ATSGeneric, "careers.example.test": gates.ATSGeneric,
	} {
		if got := activationATS(ATSGeneric, host); got != want {
			t.Errorf("activationATS(%q) = %v, want %v", host, got, want)
		}
	}
	// Another host's verification banner never looks up a mail and stops for the user.
	m := &fakeMail{link: gmail.LinkResult{URL: "https://careers.example.test/activate/Ab12Cd34Ef56", From: mailFrom("n", "otp.workday.com"), ReceivedAt: t0}}
	a := newAcct(t, Input{URL: "https://careers.example.test/x", Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Mail = m })
	st0 := loginPage("")
	st0.URL = "https://careers.example.test/x"
	a.tab.set(st0)
	a.s.entry = st0
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Sign in" {
			v := loginPage("")
			v.Inputs, v.Lines = nil, []string{"Please verify your email to continue"}
			t.set(v)
		}
	}
	_, st := a.run()
	if st == nil || st.class != ClassCredential || m.calls != 0 || len(a.tab.navs) != 0 {
		t.Fatalf("stop %+v after %d mail lookups and navigations %v", st, m.calls, a.tab.navs)
	}
}

func TestApply_Account_GoogleSignIn(t *testing.T) {
	for _, tc := range []struct {
		name                string
		enabled, onlyGoogle bool
		result              string
		wantSuccess         bool
		presses             int
	}{
		{"default off leaves Google unpressed", false, false, "success", false, 0},
		{"Google-only off hands off", false, true, "success", false, 0},
		{"existing session", true, true, "success", true, 1},
		{"before password", true, false, "success", true, 1},
		{"ordinary form candidate", true, true, "form", true, 1},
		{"authentication modal", true, true, "modal", false, 1},
		{"segmented verification form", true, true, "code", false, 1},
		{"consent", true, true, "external", false, 1},
		{"blank callback", true, true, "blank", false, 1},
		{"error feedback", true, true, "feedback", false, 1},
		{"no answer", true, true, "none", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", func(d *Deps) {
				d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: tc.enabled} }
				d.Page.Account.Answer = 90 * time.Millisecond
				d.Page.Account.Google = 90 * time.Millisecond
				d.Page.Account.Stable = 15 * time.Millisecond
			})
			wall := loginPage("")
			wall.Controls = append(wall.Controls, Control{I: 9, Tag: "button", Labels: []string{"Continue with Google"}, Form: -1})
			if tc.onlyGoogle {
				wall.Inputs = nil
			}
			a.tab.set(wall)
			presses := 0
			a.tab.onClick = func(tab *scriptTab, c Control) {
				if c.I != 9 {
					return
				}
				presses++
				switch tc.result {
				case "success":
					tab.set(appPage())
				case "form", "modal", "code":
					page := appPage()
					d := DialogInfo{Form: tc.result != "modal"}
					switch tc.result {
					case "code":
						d.Text = "Enter the verification code"
						for i := 0; i < 6; i++ {
							d.Boxes = append(d.Boxes, DialogBox{I: 30 + i, Label: "Code digit"})
						}
					}
					page.Dialogs = []DialogInfo{d}
					tab.set(page)
				case "external":
					tab.set(&PageState{URL: "https://accounts.google.com/consent", Text: "Allow access?"})
				case "feedback":
					tab.set(&PageState{URL: loginURL, Text: "Google sign-in failed", Inputs: []InputInfo{{Type: "textarea", Label: "Feedback", Form: 0}}, Forms: []FormInfo{{Fields: 1}}, Controls: []Control{{I: 10, Tag: "button", Labels: []string{"Send feedback"}, Form: 0}}})
				case "blank":
					tab.set(&PageState{URL: loginURL})
				}
			}
			_, st := a.run()
			if presses != tc.presses || len(a.tab.typed) != 0 {
				t.Fatalf("Google presses %d; typed %v", presses, a.tab.typed)
			}
			if tc.wantSuccess {
				if st != nil || a.e.Run.Signins[0].Succeeded != "google" {
					t.Fatalf("stop %+v signins %+v", st, a.e.Run.Signins)
				}
			} else if st == nil {
				t.Fatal("sign-in was not observed but was reported successful")
			}
			if tc.enabled && !tc.wantSuccess {
				_, _ = a.run()
				if presses != 1 {
					t.Fatal("re-entry pressed Google twice")
				}
			}
		})
	}
}

func TestApply_Account_GoogleButtonInApplicationIsNotALoginWall(t *testing.T) {
	st := appPage()
	st.Controls = []Control{{I: 9, Labels: []string{"Sign in with Google"}}}
	if findWall(st) != nil {
		t.Fatal("an application is not a Google login wall")
	}
}

func TestApply_Account_GoogleRegistrationHandsOff(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} } })
	a.tab.set(&PageState{URL: loginURL, Title: "Create account", Headings: []string{"Create account"}, Controls: []Control{{I: 9, Tag: "button", Labels: []string{"Continue with Google"}}}})
	_, st := a.run()
	if st == nil || len(a.tab.clicks) != 0 {
		t.Fatalf("stop %+v clicks %+v", st, a.tab.clicks)
	}
}

func TestApply_Account_GoogleHeaderOnPublicPostingIsNotALoginWall(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} } })
	a.tab.set(&PageState{URL: loginURL, Title: "Platform Engineer", Headings: []string{"Platform Engineer"}, Controls: []Control{{I: 7, Tag: "button", Labels: []string{"Sign in with Google"}}, {I: 8, Tag: "button", Labels: []string{"Apply"}}}})
	_, st := a.run()
	if st != nil || len(a.tab.clicks) != 0 {
		t.Fatalf("public posting stop %+v clicks %+v", st, a.tab.clicks)
	}
}

func TestApply_GoogleProvider_AccountAndBasicConsent(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", func(d *Deps) {
		d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} }
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"candidate":{"email":"alex@example.com"}}`), nil
		}
	})
	rec := &SigninRecord{}
	ac := &acct{r: a.g.r, e: a.e, s: a.s, host: "careers.example.test", rec: rec}
	flow := newGoogleFlow(ac)
	const address = "https://accounts.google.com/o/oauth2/v2/auth?scope=openid%20email%20profile&redirect_uri=https%3A%2F%2Fcareers.example.test%2Fcallback"
	chooser := &PageState{URL: address, Text: "Choose an account", Controls: []Control{{I: 20, GoogleAccount: "alex@example.com", Tag: "button", Labels: []string{"Alex alex@example.com"}}}}
	a.tab.set(chooser)
	a.tab.onClick = func(tab *scriptTab, c Control) {
		if c.I == 20 {
			tab.set(&PageState{URL: address + "&step=consent", Text: "alex@example.com Basic profile and email", Controls: []Control{{I: 21, Tag: "button", Labels: []string{"Continue"}}}})
		} else if c.I == 21 {
			tab.set(appPage())
		}
	}
	if acted, st := flow.step(context.Background(), a.tab, a.tab.state); !acted || st != nil {
		t.Fatalf("account %+v", st)
	}
	if acted, st := flow.step(context.Background(), a.tab, a.tab.state); !acted || st != nil {
		t.Fatalf("consent %+v", st)
	}
	if len(a.tab.clicks) != 2 || len(a.tab.typed) != 0 || a.tab.state.URL != loginURL {
		t.Fatalf("clicks %+v typed %+v", a.tab.clicks, a.tab.typed)
	}
}

func TestApply_GoogleProvider_UnknownAccountPermissionsAndVerificationStop(t *testing.T) {
	const basic = "https://accounts.google.com/o/oauth2/v2/auth?scope=openid%20email%20profile&redirect_uri=https%3A%2F%2Fcareers.example.test%2Fcallback"
	for _, tc := range []struct {
		name, address, text string
		input               bool
	}{
		{"wrong account", basic, "other@example.com", false},
		{"unknown scopes", "https://accounts.google.com/consent", "alex@example.com", false},
		{"extra scopes", strings.Replace(basic, "scope=openid", "scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fdrive%20openid", 1), "alex@example.com", false},
		{"wrong provider", "https://accounts.google.com.evil.test/consent", "alex@example.com", false},
		{"insecure provider", strings.Replace(basic, "https:", "http:", 1), "alex@example.com", false},
		{"verification", basic, "alex@example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", func(d *Deps) {
				d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} }
				d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
					return json.RawMessage(`{"candidate":{"email":"alex@example.com"}}`), nil
				}
			})
			ac := &acct{r: a.g.r, e: a.e, s: a.s, host: "careers.example.test", rec: &SigninRecord{}}
			page := &PageState{URL: tc.address, Text: tc.text, Controls: []Control{{I: 21, Tag: "button", Labels: []string{"Allow"}}}}
			if tc.input {
				page.Inputs = []InputInfo{{Type: "password", Label: "Password"}}
			}
			a.tab.set(page)
			if _, st := newGoogleFlow(ac).step(context.Background(), a.tab, page); st == nil || len(a.tab.clicks) != 0 {
				t.Fatalf("stop %+v clicks %+v", st, a.tab.clicks)
			}
		})
	}
}

func TestApply_Account_GoogleRegistrationUsesCreationPolicy(t *testing.T) {
	for _, n := range []int{0, 1} {
		for _, label := range []string{"Continue with Google", "Register with Google"} {
			t.Run(fmt.Sprint(n)+label, func(t *testing.T) {
				a := newAcct(t, Input{URL: loginURL, Submit: true, UserStarted: true}, ATSGeneric, "careers.example.test", func(d *Deps) {
					d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} }
					if n == 0 {
						d.Page.Policy = coolingOn("generic:careers.example.test")
					}
				})
				a.tab.set(&PageState{URL: loginURL, Title: "Create account", Controls: []Control{{I: 9, Tag: "button", Labels: []string{label}}}})
				a.tab.onClick = func(tab *scriptTab, c Control) { tab.set(appPage()) }
				_, st := a.run()
				if n == 0 {
					if st == nil || len(a.tab.clicks) != 0 {
						t.Fatalf("stop %+v clicks %+v", st, a.tab.clicks)
					}
				} else if st != nil || len(a.tab.clicks) != 1 || a.g.count(`SELECT count(*) FROM signup_attempts`) != 1 {
					t.Fatalf("stop %+v clicks %+v", st, a.tab.clicks)
				}
			})
		}
	}
}

func TestApply_GoogleProvider_CannotReuseStaleProof(t *testing.T) {
	for _, tc := range []struct{ name, address, text string }{
		{"account changed", "https://accounts.google.com/o/oauth2/v2/auth?scope=openid%20email%20profile&redirect_uri=https%3A%2F%2Fcareers.example.test%2Fcallback&step=consent", "other@example.com Basic email and profile"},
		{"unknown permission transaction", "https://accounts.google.com/consent?opaque_request=different", "alex@example.com View and manage your Drive files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", func(d *Deps) {
				d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} }
				d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
					return json.RawMessage(`{"candidate":{"email":"alex@example.com"}}`), nil
				}
			})
			ac := &acct{r: a.g.r, e: a.e, s: a.s, host: "careers.example.test", rec: &SigninRecord{}}
			flow := newGoogleFlow(ac)
			const basic = "https://accounts.google.com/o/oauth2/v2/auth?scope=openid%20email%20profile&redirect_uri=https%3A%2F%2Fcareers.example.test%2Fcallback"
			a.tab.set(&PageState{URL: basic, Text: "Choose an account", Controls: []Control{{I: 20, GoogleAccount: "alex@example.com", Labels: []string{"Alex alex@example.com"}}}})
			a.tab.onClick = func(tab *scriptTab, c Control) {
				if c.I == 20 {
					tab.set(&PageState{URL: tc.address, Text: tc.text, Controls: []Control{{I: 21, Labels: []string{"Allow"}}}})
				}
			}
			if _, st := flow.step(context.Background(), a.tab, a.tab.state); st != nil {
				t.Fatalf("initial account: %+v", st)
			}
			_, st := flow.step(context.Background(), a.tab, a.tab.state)
			if st == nil || len(a.tab.clicks) != 1 {
				t.Fatalf("stale proof permitted consent: stop=%+v clicks=%v", st, a.tab.clicks)
			}
		})
	}
}

func TestApply_GoogleProvider_EmailLabelIsNotAccountProof(t *testing.T) {
	a := newAcct(t, Input{URL: loginURL}, ATSGeneric, "careers.example.test", func(d *Deps) {
		d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} }
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"candidate":{"email":"alex@example.com"}}`), nil
		}
	})
	ac := &acct{r: a.g.r, e: a.e, s: a.s, host: "careers.example.test", rec: &SigninRecord{}}
	page := &PageState{URL: "https://accounts.google.com/consent?opaque_request=drive", Text: "alex@example.com View and manage your Drive files", Controls: []Control{{I: 21, Labels: []string{"Allow alex@example.com"}}}}
	a.tab.set(page)
	_, st := newGoogleFlow(ac).step(context.Background(), a.tab, page)
	if st == nil || len(a.tab.clicks) != 0 {
		t.Fatalf("unverified permission button treated as account choice: stop=%+v clicks=%v", st, a.tab.clicks)
	}
}

// Campbell's (Workday, live): Gmail did not answer one lookup during the
// activation and the stage stopped. A retryable failure is asked again while
// the wait lasts, and at least once; one that persists still stops.
func TestApply_Activation_AMomentWhenGmailDoesNotAnswerIsAskedAgain(t *testing.T) {
	link := gmail.LinkResult{URL: "https://example.wd5.myworkdayjobs.com/activate/Ab12Cd34Ef56"}
	for _, tc := range []struct {
		name  string
		down  int
		wait  time.Duration
		found bool
	}{
		{"no wait, one failure", 1, 0, true},
		{"within the wait, two failures", 2, time.Second, true},
		{"no wait, it persists", 2, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMail{link: link, down: tc.down}
			a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSWorkday, "example", func(d *Deps) { d.Page.Mail = m })
			ac := &acct{r: a.g.r, e: a.e, s: a.s, host: "example.wd5.myworkdayjobs.com", rec: &SigninRecord{}, tm: AccountTiming{MailPoll: time.Millisecond}.withDefaults()}
			res, found, st := ac.lookupMail(context.Background(), time.Time{}, tc.wait)
			if tc.found {
				if st != nil || !found || res.URL != link.URL {
					t.Fatalf("found %v stop %+v after %d lookups", found, st, m.calls)
				}
				return
			}
			if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "Gmail could not be reached") || m.calls != 2 {
				t.Fatalf("stop %+v after %d lookups", st, m.calls)
			}
		})
	}
}

// ByteDance's sign-in form holds an unmarked "I have read and agree to the User
// Agreement and Applicant Privacy Policy" box, and Sign In sends nothing until
// it is ticked: under the legal setting the box is ticked before the
// credentials are typed; with the setting off the box is the user's and
// nothing is typed.
func TestApply_Account_ASignInAgreementBoxIsTickedUnderTheLegalSetting(t *testing.T) {
	const agree = "I have read and agree to the User Agreement and Applicant Privacy Policy"
	page := func() *PageState {
		st := loginPage("")
		st.Inputs = append(st.Inputs, InputInfo{I: 4, Tag: "input", Type: "checkbox", Label: agree, Form: -1})
		return st
	}
	rig := func(mod func(*Deps)) *acctEnv {
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", mod)
		a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
		a.tab.set(page())
		a.s.entry = a.tab.state
		a.tab.onClick = func(tab *scriptTab, c Control) {
			if len(c.Labels) > 0 && c.Labels[0] == "Sign in" && slices.Contains(tab.inputClicks, agree) {
				tab.set(appPage())
			}
		}
		return a
	}
	a := rig(settingOn)
	if _, st := a.run(); st != nil || !slices.Contains(a.tab.inputClicks, agree) {
		t.Fatalf("stop %+v clicks %v", st, a.tab.inputClicks)
	}
	off := rig(nil)
	_, st := off.run()
	if st == nil || !strings.Contains(st.reason, "asks to agree") || len(off.tab.inputClicks) != 0 || len(off.tab.typed) != 0 {
		t.Fatalf("stop %+v clicks %v typed %d", st, off.tab.inputClicks, len(off.tab.typed))
	}
}
