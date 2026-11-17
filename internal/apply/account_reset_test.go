package apply

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
)

// resetMail answers the reset lookup.
type resetMail struct {
	scriptMail
	reset func(q gmail.LinkQuery, call int) (gmail.LinkResult, error)
	rcall []gmail.LinkQuery
}

func (m *resetMail) ResetLink(_ context.Context, q gmail.LinkQuery) (gmail.LinkResult, error) {
	m.rcall = append(m.rcall, q)
	return m.reset(q, len(m.rcall))
}

func wdReset(token string, at time.Time) gmail.LinkResult {
	return gmail.LinkResult{URL: "https://" + wdHost + "/passwordreset/" + token, From: "Workday <" + mailFrom("noreply", "otp.workday.com") + ">", ReceivedAt: at}
}

type resetCfg struct {
	forgot  bool // the wall offers "Forgot your password?"
	code    bool // the reset asks for a mailed code instead of sending a link
	mail    func(q gmail.LinkQuery, call int) (gmail.LinkResult, error)
	codes   *fakeCodes
	policy  bool // the reset page refuses the new password
	unsaved bool // no shared default is saved
}

// resetRig is a Workday tenant whose account exists: the tenant key and the
// saved default are refused until the reset sets the default.
type resetRig struct {
	a          *acctEnv
	mail       *resetMail
	setTo      []string
	resetDone  bool
	forgotHits int
}

func newResetRig(t *testing.T, cfg resetCfg) *resetRig {
	t.Helper()
	rr := &resetRig{mail: &resetMail{reset: cfg.mail}}
	rr.mail.fn = func(gmail.LinkQuery, int) (gmail.LinkResult, error) { return noMailYet() }
	if rr.mail.reset == nil {
		rr.mail.reset = func(q gmail.LinkQuery, _ int) (gmail.LinkResult, error) {
			return wdReset("Tok12345abcd", q.After.Add(30*time.Second)), nil
		}
	}
	rr.a = wdAcct(t, Input{}, func(d *Deps) {
		d.Page.Account.Answer = 400 * time.Millisecond // each page wait of the reset is short in a scripted tab
		d.Page.Mail = rr.mail
		if cfg.codes != nil {
			d.Page.Codes, d.Page.Code = cfg.codes, fastCode
		}
	})
	a := rr.a
	a.seed(t, "tenant-workday-example", "tenant", "", "Tenant-Bad-1!x")
	if !cfg.unsaved {
		a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	}
	a.seed(t, "signin-alternate-0123456789ab", "signin_alternate", "", "Alt-One-2!y")
	entry := a.s.entry
	wall := func(err string) *PageState {
		w := loginPage(err)
		w.URL = entry.URL
		if cfg.forgot {
			w.Controls = append(w.Controls, Control{I: 10, Tag: "a", Labels: []string{"Forgot your password?"}, Form: -1})
		}
		return w
	}
	page := func(title string, in []InputInfo, ctl ...Control) *PageState {
		return &PageState{URL: entry.URL, Title: title, Text: title, Lines: []string{title}, Inputs: in, Controls: ctl}
	}
	emailForm := page("Reset your password", []InputInfo{{I: 20, Tag: "input", Type: "email", Label: "Email", Form: -1}}, Control{I: 21, Tag: "button", Labels: []string{"Submit"}, Form: -1})
	codeForm := page("Enter the code", []InputInfo{{I: 22, Tag: "input", Type: "text", Label: "Verification code", Name: "otp", Form: -1}}, Control{I: 23, Tag: "button", Labels: []string{"Verify"}, Form: -1})
	sent := page("Check your email", nil)
	newPw := page("Choose a new password", []InputInfo{
		{I: 30, Tag: "input", Type: "password", Label: "New password", Form: -1},
		{I: 31, Tag: "input", Type: "password", Label: "Confirm password", Form: -1},
	}, Control{I: 32, Tag: "button", Labels: []string{"Reset Password"}, Form: -1})
	done := page("Your password was reset", nil)
	a.tab.set(wall(""))
	a.tab.onNav = func(tab *scriptTab, url string) {
		switch {
		case strings.Contains(url, "/passwordreset/"):
			tab.set(newPw)
		default:
			tab.set(wall(""))
		}
	}
	a.tab.onClick = func(tab *scriptTab, c Control) {
		switch c.Labels[0] {
		case "Sign in":
			pw := tab.typed[1]
			tab.typed = map[int]string{}
			if rr.resetDone && pw == "Default-Pw-1!x" {
				app := appPage()
				app.URL = entry.URL
				tab.set(app)
				return
			}
			tab.set(wall("Invalid email or password"))
		case "Forgot your password?":
			rr.forgotHits++
			tab.set(emailForm)
		case "Submit":
			if cfg.code {
				tab.set(codeForm)
			} else {
				tab.set(sent)
			}
		case "Verify":
			tab.set(newPw)
		case "Reset Password":
			rr.setTo = append(rr.setTo, tab.typed[30], tab.typed[31])
			if cfg.policy {
				refused := *newPw
				refused.Validation = []string{"Your password must not match a recently used password."}
				tab.set(&refused)
				return
			}
			rr.resetDone = true
			tab.set(done)
		}
	}
	return rr
}

func (rr *resetRig) held(t *testing.T) bool {
	return rr.a.g.count(`SELECT count(*) FROM signin_holds WHERE host = ?`, wdHost) == 1
}

func (rr *resetRig) failedMark(t *testing.T) bool {
	return rr.a.g.count(`SELECT count(*) FROM credential_meta WHERE key = ?`, "reset_failed:"+wdHost) == 1
}

func TestApply_Reset_SavedPasswordRefusedOnAKnownWorkdayAccountResetsAndSignsIn(t *testing.T) {
	rr := newResetRig(t, resetCfg{forgot: true})
	d, st := rr.a.run()
	if st != nil || !strings.Contains(d, "signed in after a password reset") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	// Only the user's saved password is set: never the alternate, never the
	// refused tenant key.
	if len(rr.setTo) != 2 || rr.setTo[0] != "Default-Pw-1!x" || rr.setTo[1] != "Default-Pw-1!x" {
		t.Fatalf("passwords set %v", rr.setTo)
	}
	if got := vaultValue(t, rr.a.v, "tenant-workday-example"); got != "Default-Pw-1!x" {
		t.Fatalf("the password that signed in is not the tenant key: %q", got)
	}
	if rr.a.e.Run.Signins[0].ResetState != "succeeded" || rr.held(t) || rr.failedMark(t) {
		t.Fatalf("record %+v held %v", rr.a.e.Run.Signins[0], rr.held(t))
	}
	if len(rr.mail.rcall) == 0 || rr.mail.rcall[0].After.IsZero() {
		t.Fatalf("reset lookups %+v: only mail after the press qualifies", rr.mail.rcall)
	}
	for _, g := range []gates.Gate{gates.GateResetStart, gates.GateResetPassword, gates.GateResetLink, gates.GateMailLink} {
		if rr.a.g.audits(g) != 1 {
			t.Fatalf("gate %s audited %d times", g, rr.a.g.audits(g))
		}
	}
	if strings.Contains(stringify(rr.a.e.Run), "Tok12345abcd") {
		t.Fatal("the reset token reached the run record")
	}
}

func TestApply_Reset_CodeFieldReadsTheCodeFromTheWorkdaySenderAfterThePress(t *testing.T) {
	fc := &fakeCodes{replies: []codeReply{mailed("739104", mailFrom("no-reply", "otp.workday.com"), "Your code", t0.Add(20*time.Second))}}
	rr := newResetRig(t, resetCfg{forgot: true, code: true, codes: fc})
	d, st := rr.a.run()
	if st != nil || !strings.Contains(d, "password reset") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if len(fc.domain) == 0 || fc.domain[0] != "otp.workday.com" || len(rr.mail.rcall) != 0 {
		t.Fatalf("code lookups %v, link lookups %d", fc.domain, len(rr.mail.rcall))
	}
	// A code mail from another sender is refused by the reset gate.
	bad := &fakeCodes{replies: []codeReply{mailed("739104", mailFrom("no-reply", "evil.example.test"), "Your code", t0.Add(20*time.Second))}}
	rr2 := newResetRig(t, resetCfg{forgot: true, code: true, codes: bad})
	if _, st := rr2.a.run(); st == nil || st.class != ClassCredential || len(rr2.setTo) != 0 {
		t.Fatalf("stop %+v set %v", st, rr2.setTo)
	}
}

func TestApply_Reset_BoundsOnePerHostPerRunNeverAfterLockoutNoneWithin24HoursGenericGetsNone(t *testing.T) {
	deny := func(t *testing.T, rr *resetRig, code string) {
		t.Helper()
		_, st := rr.a.run()
		if st == nil || st.class != ClassCredential {
			t.Fatalf("stop %+v", st)
		}
		if rr.forgotHits != 0 || len(rr.setTo) != 0 {
			t.Fatalf("a reset ran though %s: forgot presses %d, passwords set %v", code, rr.forgotHits, rr.setTo)
		}
		if rr.a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'reset_start' AND code = ?`, code) != 1 {
			t.Fatalf("the refusal %q was not audited", code)
		}
	}
	t.Run("no saved password to set", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true, unsaved: true})
		_, st := rr.a.run()
		if st == nil || st.class != ClassCredential || !strings.Contains(st.reason, "sets only a saved password") || rr.forgotHits != 0 {
			t.Fatalf("stop %+v forgot %d: the stop names the saved password a reset needs", st, rr.forgotHits)
		}
		if rr.a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'reset_password' AND verdict = 'deny' AND code = ?`, gates.CodeResetPasswordNotSaved) != 1 {
			t.Fatal("the reset-password gate did not decide and audit the missing saved password")
		}
	})
	t.Run("a wall that returns after a password signed in gets no reset", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true})
		cands, err := rr.a.cs.Candidates(context.Background(), wdHost, ATSWorkday, "example")
		if err != nil || len(cands) == 0 {
			t.Fatalf("candidates %v %v", cands, err)
		}
		rec := &rr.a.e.Run.Signins[rr.a.e.signinIndex(wdHost)]
		for _, c := range cands {
			rec.Tried = append(rec.Tried, c.Label) // tried earlier in the run, none refused
		}
		deny(t, rr, gates.CodeResetPasswordNotRefused)
	})
	t.Run("a second reset in one run", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true})
		rr.a.e.resets = map[string]int{wdHost: 1}
		deny(t, rr, gates.CodeResetAlreadyStarted)
	})
	t.Run("lockout wording", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true})
		rr.a.tab.onClick = func(tab *scriptTab, c Control) {
			if c.Labels[0] == "Sign in" {
				w := loginPage("Your account has been locked after too many attempts.")
				w.Text = "Your account has been locked after too many attempts."
				w.URL = rr.a.s.entry.URL
				tab.set(w)
			}
		}
		_, st := rr.a.run()
		if st == nil || st.board != "lockout" || rr.forgotHits != 0 {
			t.Fatalf("stop %+v forgot %d: lockout wording never leads to a reset", st, rr.forgotHits)
		}
	})
	t.Run("a failed reset within 24 hours", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true})
		at := t0.Add(-23 * time.Hour).UTC().Format(stampLayout)
		if err := rr.a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO credential_meta (key, value) VALUES (?, ?)`, "reset_failed:"+wdHost, at)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		deny(t, rr, gates.CodeResetFailedWithin24h)
	})
	t.Run("a failed reset 25 hours ago does not block", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true})
		at := t0.Add(-25 * time.Hour).UTC().Format(stampLayout)
		if err := rr.a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO credential_meta (key, value) VALUES (?, ?)`, "reset_failed:"+wdHost, at)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, st := rr.a.run(); st != nil {
			t.Fatalf("stop %+v", st)
		}
	})
	t.Run("a generic host gets no reset", func(t *testing.T) {
		a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) {
			d.Page.Mail = &resetMail{reset: func(q gmail.LinkQuery, _ int) (gmail.LinkResult, error) {
				return wdReset("Tok12345abcd", q.After.Add(time.Minute)), nil
			}}
		})
		a.seed(t, "signin-site-careers.example.test", "signin_site", "careers.example.test", "Site-Pw-1!x") // a known account, so only the system refuses
		w := loginPage("")
		w.Controls = append(w.Controls, Control{I: 10, Tag: "a", Labels: []string{"Forgot your password?"}, Form: -1})
		a.tab.set(w)
		a.signInRight("never")
		_, st := a.run()
		if st == nil || a.tab.clicksOf("Forgot your password?") != 0 {
			t.Fatalf("stop %+v", st)
		}
		if a.g.audits(gates.GateResetStart) != 1 || a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'reset_start' AND verdict = 'deny' AND code = ?`, gates.CodeResetNotWorkday) != 1 {
			t.Fatalf("%d reset decisions: the reset gate decides a generic host and audits the one refusal", a.g.audits(gates.GateResetStart))
		}
	})
	t.Run("a hold earlier today does not block a reset, which submits no guess", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true})
		day := t0.Format("2006-01-02")
		if err := rr.a.g.st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO signin_holds (host, recorded_at, day, lockout, run_id) VALUES (?, ?, ?, 0, 'run_old')`, wdHost, t0.Add(-time.Hour).UTC().Format(stampLayout), day)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		d, st := rr.a.run()
		if st != nil || !strings.Contains(d, "password reset") || rr.held(t) {
			t.Fatalf("detail %q stop %+v held %v: the successful reset clears the hold", d, st, rr.held(t))
		}
	})
}

func TestApply_Reset_OutcomesFailedHoldsNoControlDoesNot(t *testing.T) {
	t.Run("a mail that fails the locks fails closed and starts the 24-hour hold", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true, mail: func(q gmail.LinkQuery, _ int) (gmail.LinkResult, error) {
			l := wdReset("Tok12345abcd", q.After.Add(time.Minute))
			l.URL = "https://other.myworkdayjobs.com/passwordreset/Tok12345abcd"
			return l, nil
		}})
		_, st := rr.a.run()
		if st == nil || st.class != ClassCredential || len(rr.a.tab.navs) > 1 {
			t.Fatalf("stop %+v navigations %v: a link that fails a lock is never opened", st, rr.a.tab.navs)
		}
		if rr.a.e.Run.Signins[0].ResetState != "failed" || !rr.failedMark(t) || !rr.held(t) {
			t.Fatalf("record %+v mark %v held %v", rr.a.e.Run.Signins[0], rr.failedMark(t), rr.held(t))
		}
	})
	t.Run("a reset that fails as the run is cancelled still leaves its mark and its hold", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rr := newResetRig(t, resetCfg{forgot: true, mail: func(q gmail.LinkQuery, _ int) (gmail.LinkResult, error) {
			cancel()
			l := wdReset("Tok12345abcd", q.After.Add(time.Minute))
			l.URL = "https://other.myworkdayjobs.com/passwordreset/Tok12345abcd"
			return l, nil
		}})
		if _, st := rr.a.g.r.account(ctx, rr.a.e, rr.a.s); st == nil {
			t.Fatal("the failed reset did not stop the stage")
		}
		if !rr.failedMark(t) || !rr.held(t) {
			t.Fatalf("mark %v held %v: the reset was sent, so the next run must not start another", rr.failedMark(t), rr.held(t))
		}
	})
	t.Run("a tenant that refuses the saved password stops and tells the user", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: true, policy: true})
		_, st := rr.a.run()
		if st == nil || !strings.Contains(st.reason, "change the saved password") || !rr.failedMark(t) {
			t.Fatalf("stop %+v", st)
		}
	})
	t.Run("no forgot control spends the try but starts no hold", func(t *testing.T) {
		rr := newResetRig(t, resetCfg{forgot: false})
		_, st := rr.a.run()
		if st == nil || rr.a.e.Run.Signins[0].ResetState != "not_sent" || rr.failedMark(t) {
			t.Fatalf("stop %+v record %+v mark %v", st, rr.a.e.Run.Signins[0], rr.failedMark(t))
		}
		if rr.a.e.resets[wdHost] != 1 {
			t.Fatalf("resets %v: the try is spent", rr.a.e.resets)
		}
	})
}

func TestChain_Reset_CodeOrLinkNeverInDetail(t *testing.T) {
	if got := (AccountTiming{}).withDefaults().ResetWait; got != 120*time.Second {
		t.Fatalf("reset wait %s, want 120 s", got)
	}
	fc := &fakeCodes{replies: []codeReply{mailed("739104", mailFrom("no-reply", "otp.workday.com"), "Your code 739104", t0.Add(20*time.Second))}}
	byCode := newResetRig(t, resetCfg{forgot: true, code: true, codes: fc})
	byLink := newResetRig(t, resetCfg{forgot: true})
	for name, c := range map[string]struct {
		rr    *resetRig
		token string
	}{"code": {byCode, "739104"}, "link": {byLink, "Tok12345abcd"}} {
		d, st := c.rr.a.run()
		if st != nil {
			t.Fatalf("%s: stop %+v", name, st)
		}
		if strings.Contains(d, c.token) || strings.Contains(stringify(c.rr.a.e.Run), c.token) {
			t.Fatalf("%s: the secret reached the detail or the run record: %q", name, d)
		}
	}
	if len(fc.domain) == 0 || fc.domain[0] != "otp.workday.com" || fc.since[0].IsZero() {
		t.Fatalf("code lookups %v since %v: bound to the Workday sender and the press", fc.domain, fc.since)
	}
}

// heldOnce answers the first hold read and fails every later one, as a store
// that stops answering between the sign-in and the reset would.
type heldOnce struct {
	Credentials
	reads int
}

func (h *heldOnce) Held(ctx context.Context, host string) (credentials.Hold, error) {
	h.reads++
	if h.reads > 1 {
		return credentials.Hold{}, errors.New("the hold record cannot be read")
	}
	return h.Credentials.Held(ctx, host)
}

// A hold record the reset cannot read is never read as "no hold": the reset
// does not start.
func TestApply_Reset_AnUnreadableHoldRecordStartsNoReset(t *testing.T) {
	rr := newResetRig(t, resetCfg{forgot: true})
	h := &heldOnce{Credentials: rr.a.g.r.d.Page.Credentials}
	rr.a.g.r.d.Page.Credentials = h
	_, st := rr.a.run()
	if h.reads < 2 {
		t.Fatalf("the reset never read the hold record (%d reads)", h.reads)
	}
	if st == nil || rr.forgotHits != 0 || len(rr.setTo) != 0 {
		t.Fatalf("stop %+v: a reset ran on an unreadable hold record (forgot presses %d, passwords set %v)", st, rr.forgotHits, rr.setTo)
	}
	if rr.a.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'reset_start'`) != 0 {
		t.Fatal("the reset gate was asked on an unreadable hold record")
	}
}
