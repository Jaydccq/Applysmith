// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import "testing"

func TestGates_Lockout_ThreeRefusalsPerHostPerRun(t *testing.T) {
	for _, c := range []struct {
		name string
		in   PasswordInput
		v    Verdict
		code string
	}{
		{"first password, known account", PasswordInput{AccountKnown: true}, Allow, CodeOK},
		{"two refused, known account", PasswordInput{AccountKnown: true, Refused: 2}, Allow, CodeOK},
		{"three refused", PasswordInput{AccountKnown: true, Refused: 3}, Deny, CodeRefusalsExhausted},
		{"four refused", PasswordInput{AccountKnown: true, Refused: 4}, Deny, CodeRefusalsExhausted},
		{"guess, none refused", PasswordInput{}, Allow, CodeOK},
		{"guess, one refused", PasswordInput{Refused: 1}, Allow, CodeOK},
		{"guess, two refused keeps headroom", PasswordInput{Refused: 2}, Deny, CodeGuessHeadroom},
		{"guess, three refused", PasswordInput{Refused: 3}, Deny, CodeRefusalsExhausted},
		{"same password again", PasswordInput{AccountKnown: true, AlreadyRefused: true}, Deny, CodePasswordRepeat},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.in.TodayLocal = "d1"
			got(t, PasswordSubmission(c.in), GatePasswordSubmission, c.v, c.code)
		})
	}
}

func TestGates_Lockout_DefiniteWordingStopsHedgedDoesNot(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"Your account has been locked", true},
		{"The account is locked.", true},
		{"This user was suspended", true},
		{"Your profile has been disabled", true},
		{"You are locked out", true},
		{"Too many attempts, try later", true},
		{"too many tries", true},
		{"Account locked", true},
		{"wrong email or password, or your account might be locked", false},
		{"your account may be locked after repeated failures", false},
		{"Incorrect password", false},
		{"The lock icon is shown", false},
		{"", false},
	} {
		if got := DefiniteLockout(c.text); got != c.want {
			t.Errorf("DefiniteLockout(%q) = %v", c.text, got)
		}
	}
	got(t, PasswordSubmission(PasswordInput{AccountKnown: true, LockoutSeen: true, TodayLocal: "d1"}), GatePasswordSubmission, Deny, CodeLockoutWording)
	// A hedged sentence is one ordinary refusal and does not lock the host.
	got(t, PasswordSubmission(PasswordInput{AccountKnown: true, Refused: 1, LockoutSeen: DefiniteLockout("wrong email or password, or your account might be locked"), TodayLocal: "d1"}), GatePasswordSubmission, Allow, CodeOK)
}

func TestGates_Lockout_ExhaustedHoldLiftedOnlyByChangedCredential(t *testing.T) {
	prior := Exhaustion{Recorded: true, At: t0, Day: "d1"}
	for _, c := range []struct {
		name    string
		today   string
		changed int64
		signed  int64
		named   bool
		prior   Exhaustion
		lifted  bool
	}{
		{"same day, nothing changed", "d1", 0, 0, false, prior, false},
		{"next day", "d2", 0, 0, false, prior, true},
		{"credential changed after", "d1", t0 + 1, 0, false, prior, true},
		{"credential changed at the same instant", "d1", t0, 0, false, prior, false},
		{"credential changed before", "d1", t0 - 1, 0, false, prior, false},
		{"later run signed in", "d1", 0, t0 + 1, false, prior, true},
		{"nothing recorded", "d1", 0, 0, false, Exhaustion{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := HoldLifted(c.prior, c.today, c.changed, c.signed, c.named); got != c.lifted {
				t.Fatalf("lifted = %v", got)
			}
			d := PasswordSubmission(PasswordInput{AccountKnown: true, Prior: c.prior, TodayLocal: c.today, CredentialsChangedAt: c.changed, SignedInAt: c.signed, NamedRetry: c.named})
			if c.lifted {
				got(t, d, GatePasswordSubmission, Allow, CodeOK)
			} else {
				got(t, d, GatePasswordSubmission, Deny, CodeHostExhaustedToday)
			}
		})
	}
}

func TestGates_Lockout_NamedRetryLiftsExceptLockoutWording(t *testing.T) {
	ordinary, locked := Exhaustion{Recorded: true, At: t0, Day: "d1"}, Exhaustion{Recorded: true, At: t0, Day: "d1", Lockout: true}
	if !HoldLifted(ordinary, "d1", 0, 0, true) {
		t.Fatal("naming the company must lift an ordinary hold")
	}
	if HoldLifted(locked, "d1", 0, 0, true) {
		t.Fatal("a hold whose run met lockout wording stands")
	}
	got(t, PasswordSubmission(PasswordInput{AccountKnown: true, Prior: ordinary, TodayLocal: "d1", NamedRetry: true}), GatePasswordSubmission, Allow, CodeOK)
	got(t, PasswordSubmission(PasswordInput{AccountKnown: true, Prior: locked, TodayLocal: "d1", NamedRetry: true}), GatePasswordSubmission, Deny, CodeHostExhaustedToday)
}

func TestGates_Lockout_BrowserFaultIsEnvironmentNoHold(t *testing.T) {
	for _, c := range []struct {
		name, class, host string
		evidence          bool
		hold              bool
	}{
		{"credential stop on an account host", ClassCredential, "acme.example.test", true, true},
		{"browser fault is environment", ClassEnvironment, "acme.example.test", true, false},
		{"shared board without account-host evidence", ClassCredential, "boards.example.test", false, false},
		{"no host", ClassCredential, "", true, false},
		{"posting stop", ClassPosting, "acme.example.test", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, ok := HoldAfterStop(c.class, c.host, c.evidence, t0)
			if ok != c.hold {
				t.Fatalf("hold = %v", ok)
			}
			if ok && h.End != t0+24*Hour {
				t.Fatalf("hold = %+v", h)
			}
		})
	}
}

func TestGates_Lockout_CredentialCooldownPerExactHost(t *testing.T) {
	holds := []StopHold{{Host: "acme.example.test", End: t0 + 1}, {Host: "old.example.test", End: t0}}
	for _, c := range []struct {
		host string
		want bool
	}{
		{"acme.example.test", true}, {"ACME.example.test", true}, {"other.example.test", false},
		{"acme.example.test.evil.test", false}, {"sub.acme.example.test", false}, {"old.example.test", false},
	} {
		if got := RowHeld(c.host, holds, t0); got != c.want {
			t.Errorf("RowHeld(%q) = %v", c.host, got)
		}
	}
}

func TestGates_Lockout_CodeRequestCap(t *testing.T) {
	for _, c := range []struct {
		name string
		in   CodeRequestInput
		v    Verdict
		code string
	}{
		{"first request", CodeRequestInput{}, Allow, CodeOK},
		{"second request (at the cap)", CodeRequestInput{RequestsThisHost: 1}, Allow, CodeOK},
		{"third request (above the cap)", CodeRequestInput{RequestsThisHost: 2}, Deny, CodeCodeRequestCap},
		{"fourth request", CodeRequestInput{RequestsThisHost: 3}, Deny, CodeCodeRequestCap},
		{"second request on one tab", CodeRequestInput{RequestedOnThisTab: true}, Deny, CodeCodeTabOnce},
		{"code mail typed twice", CodeRequestInput{CodeMailAlreadyTyped: true}, Deny, CodeCodeAlreadyTyped},
	} {
		t.Run(c.name, func(t *testing.T) { got(t, CodeRequest(c.in), GateCodeRequest, c.v, c.code) })
	}
}
