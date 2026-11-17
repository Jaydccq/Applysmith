// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"
)

const tenant = "acme.example.test"

func workdayMail(mut func(*ActivationInput)) ActivationInput {
	in := ActivationInput{
		ATS: ATSWorkday, Precondition: PreconditionBanner, TenantHost: tenant,
		Mail: MailFacts{Link: "https://acme.example.test/site/activate/a1b2c3d4e5", SenderDomain: "otp.workday.com", ReceivedAt: t0 - Hour, Now: t0},
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func sfMail(mut func(*ActivationInput)) ActivationInput {
	in := ActivationInput{
		ATS: ATSSuccessFactors, Precondition: PreconditionSentPage, TenantHost: tenant,
		Mail: MailFacts{
			Link:         "https://acme.example.test/career?navBarLevel=EMAIL_VERIFICATION&hash=" + strings.Repeat("Ab3_-", 5),
			SenderDomain: "successfactors.com", ReceivedAt: t0 - Minute, NotNewerThan: t0 - 2*Minute, Now: t0,
		},
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func TestGates_Activation_FourLocksAndSignInIsVerdict(t *testing.T) {
	for _, c := range []struct {
		name string
		in   ActivationInput
		v    Verdict
		code string
	}{
		{"all four locks hold (Workday)", workdayMail(nil), Allow, CodeOK},
		{"all four locks hold (SuccessFactors)", sfMail(nil), Allow, CodeOK},
		{"precondition missing", workdayMail(func(i *ActivationInput) { i.Precondition = "" }), Deny, CodeActivationPrecondition},
		{"look-alike tenant host", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://other-tenant.example.test/site/activate/a1b2c3d4e5" }), Deny, CodeLinkHostMismatch},
		{"suffix host", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test.evil.test/site/activate/a1b2c3d4e5" }), Deny, CodeLinkHostMismatch},
		{"userinfo host trick", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test@evil.test/site/activate/a1b2c3d4e5" }), Deny, CodeLinkBadShape},
		{"explicit odd port", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test:8443/site/activate/a1b2c3d4e5" }), Deny, CodeLinkHostMismatch},
		{"http, not https", workdayMail(func(i *ActivationInput) { i.Mail.Link = "http://acme.example.test/site/activate/a1b2c3d4e5" }), Deny, CodeLinkNotHTTPS},
		{"marketing link on the same path, no token", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test/site/activate/" }), Deny, CodeLinkBadShape},
		{"token without a digit", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test/site/activate/abcdefgh" }), Deny, CodeLinkTokenNotOpaque},
		{"token too short", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test/site/activate/a1b2c3d" }), Deny, CodeLinkTokenNotOpaque},
		{"token of eight", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test/site/activate/a1b2c3d4" }), Allow, CodeOK},
		{"wrong path", workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test/site/promo/a1b2c3d4e5" }), Deny, CodeLinkBadShape},
		{"SuccessFactors shape from the Workday sender", sfMail(func(i *ActivationInput) { i.Mail.SenderDomain = "otp.workday.com" }), Deny, CodeLinkSenderMismatch},
		{"Workday shape from the SuccessFactors sender", workdayMail(func(i *ActivationInput) { i.Mail.SenderDomain = "successfactors.com" }), Deny, CodeLinkSenderMismatch},
		{"sender subdomain", workdayMail(func(i *ActivationInput) { i.Mail.SenderDomain = "mail.otp.workday.com" }), Deny, CodeLinkSenderMismatch},
		// The owner allows an address the activation mail prints (Workday's own tenant).
		{"link printed in the mail's text", workdayMail(func(i *ActivationInput) { i.Mail.FromText = true }), Allow, CodeOK},
		{"Workday-shaped link under the SuccessFactors system", sfMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test/site/activate/a1b2c3d4e5" }), Deny, CodeLinkBadShape},
		{"thank-you link of the same sender", sfMail(func(i *ActivationInput) { i.Mail.Link = "https://acme.example.test/career?navBarLevel=JOB_MGMT" }), Deny, CodeLinkBadShape},
		{"verification link without a hash", sfMail(func(i *ActivationInput) {
			i.Mail.Link = "https://acme.example.test/career?navBarLevel=EMAIL_VERIFICATION"
		}), Deny, CodeLinkTokenNotOpaque},
		{"hash of 19", sfMail(func(i *ActivationInput) {
			i.Mail.Link = "https://acme.example.test/career?navBarLevel=EMAIL_VERIFICATION&hash=" + strings.Repeat("a", 19)
		}), Deny, CodeLinkTokenNotOpaque},
		{"hash of 20", sfMail(func(i *ActivationInput) {
			i.Mail.Link = "https://acme.example.test/career?navBarLevel=EMAIL_VERIFICATION&hash=" + strings.Repeat("a", 20)
		}), Allow, CodeOK},
		{"hash outside the alphabet", sfMail(func(i *ActivationInput) {
			i.Mail.Link = "https://acme.example.test/career?navBarLevel=EMAIL_VERIFICATION&hash=" + strings.Repeat("a!", 12)
		}), Deny, CodeLinkTokenNotOpaque},
		{"generic system", workdayMail(func(i *ActivationInput) { i.ATS = ATSGeneric }), Deny, CodeActivationUnsupported},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := ActivationLink(c.in)
			got(t, d, GateActivationLink, c.v, c.code)
			if strings.Contains(d.Reason, "a1b2c3d4e5") || strings.Contains(d.Reason, "https://") {
				t.Fatalf("reason %q leaks the link", d.Reason)
			}
			if c.v == Deny && d.Class != ClassCredential {
				t.Fatalf("class = %q", d.Class)
			}
		})
	}
}

func TestGates_Activation_ShapesPerATSAndJobMgmtLinkRejected(t *testing.T) {
	got(t, ActivationLink(sfMail(nil)), GateActivationLink, Allow, CodeOK)
	got(t, ActivationLink(sfMail(func(i *ActivationInput) {
		i.Mail.Link = "https://acme.example.test/career?navBarLevel=JOB_MGMT&hash=" + strings.Repeat("Ab3_-", 5)
	})), GateActivationLink, Deny, CodeLinkBadShape)
	got(t, ActivationLink(sfMail(func(i *ActivationInput) {
		i.Mail.Link = "https://acme.example.test/other?navBarLevel=EMAIL_VERIFICATION&hash=" + strings.Repeat("Ab3_-", 5)
	})), GateActivationLink, Deny, CodeLinkBadShape)
}

func TestGates_Activation_ResendOncePerRunStrictlyNewerNoGenericResend(t *testing.T) {
	for _, c := range []struct {
		name string
		in   ResendInput
		v    Verdict
		code string
	}{
		{"first press, Workday", ResendInput{ATS: ATSWorkday}, Allow, CodeOK},
		{"first press, SuccessFactors", ResendInput{ATS: ATSSuccessFactors}, Allow, CodeOK},
		{"second press", ResendInput{ATS: ATSWorkday, PressesThisRun: 1}, Deny, CodeResendOncePerRun},
		{"generic host", ResendInput{ATS: ATSGeneric}, Deny, CodeResendGenericHost},
	} {
		t.Run(c.name, func(t *testing.T) { got(t, ActivationResend(c.in), GateActivationResend, c.v, c.code) })
	}
	// Mail must be strictly newer than the press; stale mail is not reopened.
	pressed := t0 - 10*Minute
	for _, c := range []struct {
		name string
		recv int64
		code string
	}{
		{"older than the press", pressed - Minute, CodeMailNotNewer},
		{"at the press instant", pressed, CodeMailNotNewer},
		{"one millisecond after", pressed + 1, CodeOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := ActivationLink(workdayMail(func(i *ActivationInput) { i.Mail.ReceivedAt, i.Mail.NotNewerThan = c.recv, pressed }))
			if c.code == CodeOK {
				got(t, d, GateActivationLink, Allow, CodeOK)
			} else {
				got(t, d, GateActivationLink, Deny, c.code)
			}
		})
	}
	// Existing mail is looked up over 30 days.
	for _, c := range []struct {
		name string
		age  int64
		v    Verdict
		code string
	}{
		{"29 days", 29 * Day, Allow, CodeOK},
		{"30 days", 30 * Day, Allow, CodeOK},
		{"over 30 days", 30*Day + 1, Deny, CodeMailTooOld},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, ActivationLink(workdayMail(func(i *ActivationInput) { i.Mail.ReceivedAt = t0 - c.age })), GateActivationLink, c.v, c.code)
		})
	}
	// The wait for a requested mail is bounded: 90 s for activation, 120 s for
	// a reset, polled every 5 s, and the settle wait is 3 s. The account
	// stage's timing takes its defaults from these constants.
	if ActivationResendWait != 90*Second || ResetMailWait != 120*Second || MailPollInterval != 5*Second || ActivationSettleWait != 3*Second {
		t.Fatal("a mail wait, the poll or the settle bound changed")
	}
}

func TestGates_Activation_PreconditionSentPageNeedsMailAfterSignup(t *testing.T) {
	// The sent page requires a mail strictly newer than the sign-up press,
	// which is carried as NotNewerThan.
	got(t, ActivationLink(sfMail(func(i *ActivationInput) { i.Mail.ReceivedAt = i.Mail.NotNewerThan })), GateActivationLink, Deny, CodeMailNotNewer)
}

func TestGates_SigninLink_NeverOpenedByRun(t *testing.T) {
	d := OpenMailLink(LinkSignin)
	got(t, d, GateMailLink, Deny, CodeSigninLinkUserOnly)
	if d.Class != ClassCredential {
		t.Fatalf("class = %q", d.Class)
	}
	got(t, OpenMailLink(LinkOther), GateMailLink, Deny, CodeLinkUserOnly)
	got(t, OpenMailLink(LinkActivation), GateMailLink, Allow, CodeOK)
	got(t, OpenMailLink(LinkReset), GateMailLink, Allow, CodeOK)
	// Nothing about a setting can change the kind-level refusal.
	got(t, OpenMailLink("magic"), GateMailLink, Deny, CodeLinkUserOnly)
}

func TestGates_Reset_TriggersAndBounds(t *testing.T) {
	ok := ResetStartInput{Now: t0, AccountKnown: true, SavedPasswordRefused: true, HostIsWorkday: true}
	for _, c := range []struct {
		name string
		mut  func(*ResetStartInput)
		v    Verdict
		code string
	}{
		{"all conditions hold", func(*ResetStartInput) {}, Allow, CodeOK},
		{"generic host", func(i *ResetStartInput) { i.HostIsWorkday = false }, Deny, CodeResetNotWorkday},
		{"account unknown", func(i *ResetStartInput) { i.AccountKnown = false }, Deny, CodeResetAccountUnknown},
		{"saved password not refused", func(i *ResetStartInput) { i.SavedPasswordRefused = false }, Deny, CodeResetPasswordNotRefused},
		{"lockout wording", func(i *ResetStartInput) { i.LockoutWordingSeen = true }, Deny, CodeResetLockoutWording},
		{"one per host per run", func(i *ResetStartInput) { i.ResetsStartedThisRun = 1 }, Deny, CodeResetAlreadyStarted},
		{"failed 23 hours ago", func(i *ResetStartInput) { i.LastFailedResetAt = t0 - 23*Hour }, Deny, CodeResetFailedWithin24h},
		{"failed 24 hours ago", func(i *ResetStartInput) { i.LastFailedResetAt = t0 - 24*Hour }, Allow, CodeOK},
		{"failed 25 hours ago", func(i *ResetStartInput) { i.LastFailedResetAt = t0 - 25*Hour }, Allow, CodeOK},
		{"failed a moment ago", func(i *ResetStartInput) { i.LastFailedResetAt = t0 - 1 }, Deny, CodeResetFailedWithin24h},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := ok
			c.mut(&in)
			got(t, ResetStart(in), GateResetStart, c.v, c.code)
		})
	}
}

func TestGates_Reset_SetsOnlySavedPasswordNeverAlternate(t *testing.T) {
	for src, want := range map[PasswordSource]bool{PasswordSiteOverride: true, PasswordSharedDefault: true, PasswordAlternate: false, PasswordTenantKey: false, PasswordGenerated: false, "": false} {
		d := ResetPassword(src)
		if want {
			got(t, d, GateResetPassword, Allow, CodeOK)
		} else {
			got(t, d, GateResetPassword, Deny, CodeResetPasswordNotSaved)
		}
	}
}

func resetMail(mut func(*ResetLinkInput)) ResetLinkInput {
	in := ResetLinkInput{
		HostIsWorkday: true, TenantHost: tenant,
		Mail: MailFacts{Link: "https://acme.example.test/site/passwordreset/Zx9Yw8Vv7U", SenderDomain: "otp.workday.com", ReceivedAt: t0 - Minute, NotNewerThan: t0 - 2*Minute, Now: t0},
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func TestGates_ResetLink_FourLocksAndStrictlyAfterPress(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*ResetLinkInput)
		v    Verdict
		code string
	}{
		{"all locks hold", nil, Allow, CodeOK},
		{"reset-password segment", func(i *ResetLinkInput) { i.Mail.Link = "https://acme.example.test/x/reset-password/Zx9Yw8Vv7U" }, Allow, CodeOK},
		{"password-reset segment", func(i *ResetLinkInput) { i.Mail.Link = "https://acme.example.test/x/password-reset/Zx9Yw8Vv7U" }, Allow, CodeOK},
		{"resetpassword segment", func(i *ResetLinkInput) { i.Mail.Link = "https://acme.example.test/x/resetpassword/Zx9Yw8Vv7U" }, Allow, CodeOK},
		{"activation segment is not a reset", func(i *ResetLinkInput) { i.Mail.Link = "https://acme.example.test/x/activate/Zx9Yw8Vv7U" }, Deny, CodeLinkBadShape},
		{"token without a digit", func(i *ResetLinkInput) { i.Mail.Link = "https://acme.example.test/x/passwordreset/abcdefgh" }, Deny, CodeLinkTokenNotOpaque},
		{"other host", func(i *ResetLinkInput) { i.Mail.Link = "https://other.example.test/x/passwordreset/Zx9Yw8Vv7U" }, Deny, CodeLinkHostMismatch},
		{"not https", func(i *ResetLinkInput) { i.Mail.Link = "http://acme.example.test/x/passwordreset/Zx9Yw8Vv7U" }, Deny, CodeLinkNotHTTPS},
		{"other sender", func(i *ResetLinkInput) { i.Mail.SenderDomain = "mail.example.test" }, Deny, CodeLinkSenderMismatch},
		{"from prose", func(i *ResetLinkInput) { i.Mail.FromText = true }, Deny, CodeLinkFromText},
		{"before the press", func(i *ResetLinkInput) { i.Mail.ReceivedAt = i.Mail.NotNewerThan - 1 }, Deny, CodeMailNotNewer},
		{"at the press", func(i *ResetLinkInput) { i.Mail.ReceivedAt = i.Mail.NotNewerThan }, Deny, CodeMailNotNewer},
		{"press time unknown", func(i *ResetLinkInput) { i.Mail.NotNewerThan = 0 }, Deny, CodeMailNotNewer},
		{"generic host gets no reset", func(i *ResetLinkInput) { i.HostIsWorkday = false }, Deny, CodeResetNotWorkday},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := ResetLink(resetMail(c.mut))
			got(t, d, GateResetLink, c.v, c.code)
			if strings.Contains(d.Reason, "Zx9Yw8Vv7U") {
				t.Fatalf("reason %q leaks the token", d.Reason)
			}
		})
	}
}

func TestGates_Reset_FlowCodeBranchAndLinkBranch(t *testing.T) {
	// Code branch: typed only from the Workday sender, after the press.
	got(t, ResetCodeMail("otp.workday.com", t0, t0-1), GateResetLink, Allow, CodeOK)
	got(t, ResetCodeMail("otp.workday.com", t0, t0), GateResetLink, Deny, CodeMailNotNewer)
	got(t, ResetCodeMail("otp.workday.com", t0, 0), GateResetLink, Deny, CodeMailNotNewer)
	got(t, ResetCodeMail("example.test", t0, t0-1), GateResetLink, Deny, CodeLinkSenderMismatch)
	// Link branch: opened only after its locks.
	got(t, ResetLink(resetMail(nil)), GateResetLink, Allow, CodeOK)
}

func TestGates_Reset_OutcomesFailedHoldsNoControlDoesNot(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     ResetOutcomeInput
		result string
		hold   bool
		clears bool
		class  string
	}{
		{"sign-in works", ResetOutcomeInput{SignedIn: true}, "succeeded", false, true, ""},
		{"sign-in still fails", ResetOutcomeInput{}, "failed", true, false, ClassCredential},
		{"tenant policy refused the saved password", ResetOutcomeInput{PolicyRefused: true}, "failed", true, false, ClassCredential},
		{"nothing pressed", ResetOutcomeInput{PressedNothing: true}, "not_sent", false, false, ClassCredential},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := SettleReset(c.in)
			if o.Result != c.result || o.Hold24h != c.hold || o.ClearsHolds != c.clears || o.StoreTenantKey != c.clears || o.Class != c.class {
				t.Fatalf("outcome = %+v", o)
			}
		})
	}
	if o := SettleReset(ResetOutcomeInput{PolicyRefused: true}); !strings.Contains(o.Message, "change the saved password") {
		t.Fatalf("message = %q", o.Message)
	}
	// A reset that pressed nothing still spends the run's one try.
	got(t, ResetStart(ResetStartInput{Now: t0, AccountKnown: true, SavedPasswordRefused: true, HostIsWorkday: true, ResetsStartedThisRun: 1}), GateResetStart, Deny, CodeResetAlreadyStarted)
}

func TestGates_Reset_GenericHostNoResetUnmatchedMailFailsClosed(t *testing.T) {
	got(t, ResetStart(ResetStartInput{Now: t0, AccountKnown: true, SavedPasswordRefused: true}), GateResetStart, Deny, CodeResetNotWorkday)
	got(t, ResetLink(resetMail(func(i *ResetLinkInput) { i.Mail.SenderDomain = "" })), GateResetLink, Deny, CodeLinkSenderMismatch)
	got(t, ResetLink(resetMail(func(i *ResetLinkInput) { i.Mail.Link = "not a url" })), GateResetLink, Deny, CodeLinkBadShape)
}

func TestGates_Reset_PolicyRefusalStopsNoInventedPassword(t *testing.T) {
	o := SettleReset(ResetOutcomeInput{PolicyRefused: true})
	if o.Class != ClassCredential || !o.Hold24h {
		t.Fatalf("outcome = %+v", o)
	}
	got(t, ResetPassword(PasswordGenerated), GateResetPassword, Deny, CodeResetPasswordNotSaved)
}
