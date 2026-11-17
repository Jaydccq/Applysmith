// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import "testing"

// acctAllowed is an input that passes every check; cases change one thing.
func acctAllowed(mut func(*AccountInput)) AccountInput {
	in := AccountInput{Now: t0, UserStarted: true, System: "workday"}
	if mut != nil {
		mut(&in)
	}
	return in
}

func TestGates_AccountCreation_OrderCooldownAgreement(t *testing.T) {
	cooling := func(i *AccountInput) { i.Cooldowns = []Cooldown{{System: "workday", End: t0 + Hour}} }
	agree := func(i *AccountInput) { i.RequiredAgreementOnForm = true }
	both := func(fs ...func(*AccountInput)) func(*AccountInput) {
		return func(i *AccountInput) {
			for _, f := range fs {
				f(i)
			}
		}
	}
	for _, c := range []struct {
		name string
		mut  func(*AccountInput)
		v    Verdict
		code string
	}{
		{"all pass", nil, Allow, CodeOK},
		{"cooldown before agreement", both(cooling, agree), Deny, CodeCooldownActive},
		{"agreement last", agree, Deny, CodeAgreementNeedsSetting},
		{"agreement with the setting on", both(agree, func(i *AccountInput) { i.LegalSettingOn = true }), Allow, CodeOK},
		{"a run no user started", func(i *AccountInput) { i.UserStarted = false }, Deny, CodeNotUserStarted},
		{"unsupported system", func(i *AccountInput) { i.System = "icims" }, Deny, CodeSystemUnsupported},
		{"generic host that does not offer sign-up", func(i *AccountInput) { i.System = "generic" }, Deny, CodeSystemUnsupported},
		{"generic host that offers it", func(i *AccountInput) {
			i.System, i.GenericOffersCreate = "generic", true
		}, Allow, CodeOK},
		// A generic host's cooldown is keyed by the host, so a signal on one
		// unnamed site backs off that site only.
		{"a cooldown on the same generic host", func(i *AccountInput) {
			i.System, i.Host, i.GenericOffersCreate = "generic", "Careers.Example.test", true
			i.Cooldowns = []Cooldown{{System: "generic:careers.example.test", End: t0 + Hour}}
		}, Deny, CodeCooldownActive},
		{"a named system's cooldown holds each of its sites", both(cooling, func(i *AccountInput) { i.Host = "acme.wd5.myworkdayjobs.com" }), Deny, CodeCooldownActive},
		{"a cooldown on another generic host", func(i *AccountInput) {
			i.System, i.Host, i.GenericOffersCreate = "generic", "careers.example.test", true
			i.Cooldowns = []Cooldown{{System: "generic:jobs.other.test", End: t0 + Hour}, {System: "generic", End: t0 + Hour}}
		}, Allow, CodeOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := AccountCreation(acctAllowed(c.mut))
			got(t, d, GateAccountCreation, c.v, c.code)
			if c.v == Deny && d.Class != ClassCredential {
				t.Fatalf("class = %q", d.Class)
			}
		})
	}
}

func TestGates_Cooldown_ActiveWhileEndInFutureAutoOnSignal(t *testing.T) {
	hours := map[string]int{SignalCaptcha: 168, SignalRateLimited: 168, SignalAccountLocked: 720, SignalVerificationRequired: 72, SignalLoginRedirect: 168, SignalSilentDegradation: 24}
	// A captcha signal recorded 100 hours ago holds Greenhouse for 168 hours.
	cd, ok := CooldownFor(SignalCaptcha, "greenhouse", t0-100*Hour, hours)
	if !ok || cd.End != t0+68*Hour {
		t.Fatalf("cooldown = %+v %v", cd, ok)
	}
	for _, c := range []struct {
		name string
		end  int64
		v    Verdict
		code string
	}{
		{"ends in the future", t0 + 1, Deny, CodeCooldownActive},
		{"ends now", t0, Allow, CodeOK},
		{"ended", t0 - 1, Allow, CodeOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := acctAllowed(func(i *AccountInput) {
				i.System = "greenhouse"
				i.Cooldowns = []Cooldown{{System: "greenhouse", End: c.end}, {System: "workday", End: t0 + 100*Hour}}
			})
			got(t, AccountCreation(in), GateAccountCreation, c.v, c.code)
		})
	}
	// Another system's cooldown does not hold this one.
	got(t, AccountCreation(acctAllowed(func(i *AccountInput) { i.Cooldowns = []Cooldown{{System: "lever", End: t0 + Hour}} })), GateAccountCreation, Allow, CodeOK)
}

func TestGates_Cooldown_HoursFromTableZeroDisables(t *testing.T) {
	hours := map[string]int{SignalCaptcha: 168, SignalSilentDegradation: 0}
	if _, ok := CooldownFor(SignalSilentDegradation, "workday", t0, hours); ok {
		t.Fatal("a configured 0 creates no cooldown")
	}
	if _, ok := CooldownFor(SignalLoginRedirect, "workday", t0, hours); ok {
		t.Fatal("a signal missing from the table creates none")
	}
	if _, ok := CooldownFor("", "workday", t0, hours); ok {
		t.Fatal("an empty signal creates none")
	}
	cd, ok := CooldownFor(SignalCaptcha, "workday", t0, hours)
	if !ok || cd.System != "workday" || cd.End != t0+168*Hour {
		t.Fatalf("cooldown = %+v", cd)
	}
	for _, c := range []struct {
		h    int
		want int64
	}{{1, Hour}, {24, Day}} {
		if cd, _ := CooldownFor(SignalCaptcha, "x", 0, map[string]int{SignalCaptcha: c.h}); cd.End != c.want {
			t.Errorf("%d hours -> %d", c.h, cd.End)
		}
	}
}

func TestGates_Detection_RuleOrderFirstRuleWins(t *testing.T) {
	for _, c := range []struct {
		name string
		p    PageCapture
		want string
	}{
		{"captcha element", PageCapture{CaptchaElement: true}, SignalCaptcha},
		{"403", PageCapture{HTTPStatus: 403}, SignalCaptcha},
		{"429", PageCapture{HTTPStatus: 429}, SignalRateLimited},
		{"captcha element and 403 record one captcha", PageCapture{CaptchaElement: true, HTTPStatus: 403}, SignalCaptcha},
		{"captcha element beats 429", PageCapture{CaptchaElement: true, HTTPStatus: 429}, SignalCaptcha},
		{"checkpoint text", PageCapture{Text: "Security Checkpoint"}, SignalVerificationRequired},
		{"are you human", PageCapture{Text: "Are you human?"}, SignalVerificationRequired},
		{"access denied", PageCapture{Text: "Access Denied"}, SignalVerificationRequired},
		{"verify your identity", PageCapture{Text: "Please verify your identity"}, SignalVerificationRequired},
		{"429 beats text", PageCapture{HTTPStatus: 429, Text: "access denied"}, SignalRateLimited},
		{"login redirect", PageCapture{ApplyPageExpected: true, ExpectedHost: "jobs.example.test", FinalURL: "https://sso.other.example.test/login?next=x"}, SignalLoginRedirect},
		{"login path on the expected host", PageCapture{ApplyPageExpected: true, ExpectedHost: "jobs.example.test", FinalURL: "https://jobs.example.test/login"}, ""},
		{"login path on a subdomain of the expected host", PageCapture{ApplyPageExpected: true, ExpectedHost: "example.test", FinalURL: "https://auth.example.test/signin"}, ""},
		{"login path but no apply page expected", PageCapture{ExpectedHost: "jobs.example.test", FinalURL: "https://sso.other.example.test/login"}, ""},
		{"fewer standard fields", PageCapture{StandardFields: 3, ExpectedFields: 8}, SignalSilentDegradation},
		{"as many fields as expected", PageCapture{StandardFields: 8, ExpectedFields: 8}, ""},
		{"no expectation", PageCapture{StandardFields: 0}, ""},
		{"lockout wording at sign-in", PageCapture{SignIn: true, Text: "Your account has been locked"}, SignalAccountLocked},
		{"lockout wording off sign-in", PageCapture{Text: "Your account has been locked"}, ""},
		{"hedged wording at sign-in", PageCapture{SignIn: true, Text: "wrong email or password, or your account might be locked"}, ""},
		{"nothing", PageCapture{HTTPStatus: 200, Text: "Welcome"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if g := DetectSignal(c.p); g != c.want {
				t.Fatalf("signal = %q", g)
			}
		})
	}
}
