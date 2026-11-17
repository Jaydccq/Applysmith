// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"slices"
	"strings"
)

// Gate names and reason codes of the account creation gate.
const (
	GateAccountCreation Gate = "account_creation"

	CodeCooldownActive        = "cooldown_active"
	CodeAgreementNeedsSetting = "agreement_needs_setting"
	CodeNotUserStarted        = "not_user_started"
	CodeSystemUnsupported     = "system_unsupported"
)

// SignupSystems are the systems with a sign-up flow; "generic" is the key of
// the generic flow.
var SignupSystems = []string{"greenhouse", "lever", "ashby", "workday", "generic"}

// Cooldown holds a system until End. System is the cooldown's key
// (CooldownKey): a named system, or a generic host.
type Cooldown struct {
	System string
	End    int64
}

// genericKeyPrefix starts the cooldown key of a host the generic flow serves.
const genericKeyPrefix = "generic:"

// CooldownKey is the key a detection signal and the cooldown it starts are
// stored under: a named sign-up system's own name, and for the generic flow
// the host itself, so a signal on one site that is not a named system backs
// off that site only.
func CooldownKey(system, host string) string {
	if system != "generic" {
		return system
	}
	return genericKeyPrefix + strings.ToLower(strings.TrimSpace(host))
}

// SplitCooldownKey reads a cooldown key back: the system, and the host of a
// generic one ("" for a named system).
func SplitCooldownKey(key string) (system, host string) {
	if h, ok := strings.CutPrefix(key, genericKeyPrefix); ok {
		return "generic", h
	}
	return key, ""
}

// AccountInput holds the facts for creating an account.
type AccountInput struct {
	// Demo is the workspace's demo marker.
	Demo bool
	Now  int64
	// UserStarted is true for a run a user's own act started, whether it
	// submits or only fills (F-8): a start from the dashboard, a typed command
	// or the Ask Agent at the user's request, and its continuations. A batch
	// the app resumed after a restart is not one.
	UserStarted bool
	System      string
	// Host is the account host; a generic host's cooldowns are keyed by it.
	Host string
	// GenericOffersCreate is true when a generic host's gate offers to create
	// an account.
	GenericOffersCreate bool
	Cooldowns           []Cooldown
	// RequiredAgreementOnForm and LegalSettingOn decide check 4.
	RequiredAgreementOnForm, LegalSettingOn bool
}

// AccountCreation decides whether an account may be created. Nothing is saved,
// typed or submitted on a deny.
func AccountCreation(in AccountInput) Decision {
	const g = GateAccountCreation
	deny := func(code, why string) Decision {
		return decide(g, Deny, code, "no account created: %s", why).withClass(ClassCredential)
	}
	switch {
	case in.Demo:
		return demoDeny(g, "account creation").withClass(ClassCredential)
	case !in.UserStarted:
		return deny(CodeNotUserStarted, "accounts are created only inside a run you started")
	case !slices.Contains(SignupSystems, in.System) || in.System == "generic" && !in.GenericOffersCreate:
		return deny(CodeSystemUnsupported, "no sign-up flow for this system")
	}
	key := CooldownKey(in.System, in.Host)
	for _, c := range in.Cooldowns {
		if c.System == key && c.End > in.Now {
			on := in.System
			if _, host := SplitCooldownKey(key); host != "" {
				on = host
			}
			return deny(CodeCooldownActive, "a cooldown on "+on+" is active")
		}
	}
	if in.RequiredAgreementOnForm && !in.LegalSettingOn {
		return deny(CodeAgreementNeedsSetting, "the form requires an agreement and autoAcceptLegalDeclarations is off")
	}
	return decide(g, Allow, CodeOK, "cooldown and agreement checks passed")
}

// Detection signals.
const (
	SignalCaptcha              = "captcha"
	SignalRateLimited          = "rate_limited"
	SignalAccountLocked        = "account_locked"
	SignalVerificationRequired = "verification_required"
	SignalLoginRedirect        = "login_redirect"
	SignalSilentDegradation    = "silent_degradation"
)

// PageCapture is a captured page as the detection rules read it.
type PageCapture struct {
	// HTTPStatus is the main resource's status, zero when unknown.
	HTTPStatus int
	// CaptchaElement is true for a reCAPTCHA frame, an hCaptcha class or
	// identifier, a site-key attribute or a Turnstile widget.
	CaptchaElement bool
	Text           string
	FinalURL       string
	// ApplyPageExpected and ExpectedHost describe where the run expected to be.
	ApplyPageExpected bool
	ExpectedHost      string
	// StandardFields is what the form filler found; ExpectedFields what the
	// system normally exposes.
	StandardFields, ExpectedFields int
	// SignIn is true when the page is a sign-in response.
	SignIn bool
}

// DetectSignal returns the first detection rule that matches, or "" when the
// page matches none.
func DetectSignal(p PageCapture) string {
	text := strings.ToLower(p.Text)
	switch {
	case p.CaptchaElement || p.HTTPStatus == 403:
		return SignalCaptcha
	case p.HTTPStatus == 429:
		return SignalRateLimited
	case containsAny(text, "checkpoint", "verify your identity", "identity verification", "access denied", "are you human", "security check"):
		return SignalVerificationRequired
	case p.ApplyPageExpected && loginRedirect(p.FinalURL, p.ExpectedHost):
		return SignalLoginRedirect
	case p.ExpectedFields > 0 && p.StandardFields < p.ExpectedFields:
		return SignalSilentDegradation
	case p.SignIn && DefiniteLockout(p.Text):
		return SignalAccountLocked
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func loginRedirect(finalURL, expectedHost string) bool {
	r, ok := parseRef(finalURL)
	if !ok {
		return false
	}
	path := strings.ToLower(r.path)
	if !containsAny(path, "/login", "/signin", "/sign-in", "/log-in") {
		return false
	}
	want := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(expectedHost)), "www.")
	return want != "" && r.host != want && !strings.HasSuffix(r.host, "."+want) && !strings.HasSuffix(want, "."+r.host)
}

// CooldownFor creates the cooldown a recorded signal starts. Hours come from
// the policy's cooldowns table, which this package does not restate; a
// configured 0 creates none.
func CooldownFor(signal, system string, at int64, hours map[string]int) (Cooldown, bool) {
	h := hours[signal]
	if signal == "" || h <= 0 {
		return Cooldown{}, false
	}
	return Cooldown{System: system, End: at + int64(h)*Hour}, true
}
