// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Gate names and reason codes of the password and code gates.
const (
	GatePasswordSubmission Gate = "password_submission"
	GateCodeRequest        Gate = "code_request"

	CodePasswordRepeat     = "refused_password_repeat"
	CodeRefusalsExhausted  = "refusals_exhausted"
	CodeGuessHeadroom      = "guess_headroom"
	CodeLockoutWording     = "lockout_wording"
	CodeHostExhaustedToday = "host_exhausted_today"

	CodeCodeRequestCap   = "code_request_cap"
	CodeCodeTabOnce      = "code_request_once_per_tab"
	CodeCodeAlreadyTyped = "code_already_typed"
)

// Lockout bounds.
const (
	MaxRefusalsPerHost = 3
	MaxCodeRequests    = 2
	// CredentialCooldown is how long a credential-class stop holds a host.
	CredentialCooldown = 24 * Hour
)

var (
	definiteLockoutRE = lazyregexp.New(`(?i)\b(?:account|profile|user)\b[^.!?]{0,30}\b(?:is|has been|was|got|is now|has been temporarily)\s+(?:locked|disabled|suspended|blocked)\b|\blocked out\b|\btoo many (?:attempts|tries|failed)\b|\baccount locked\b`)
	hedgeRE           = lazyregexp.New(`(?i)\b(?:might|may|could|possibly|perhaps|maybe)\b`)
)

// DefiniteLockout reports whether sign-in text says the account is locked,
// disabled, suspended or blocked. A hedge ("or your account might be locked")
// in the same sentence makes it an ordinary wrong password; a hedge in an
// earlier sentence does not.
func DefiniteLockout(text string) bool {
	text = foldText(text)
	for _, loc := range definiteLockoutRE.FindAllStringIndex(text, -1) {
		start := strings.LastIndexAny(text[:loc[0]], ".!?;\n") + 1
		if !hedgeRE.MatchString(text[start:loc[1]]) {
			return true
		}
	}
	return false
}

// Exhaustion is the record of a host whose passwords ran out.
type Exhaustion struct {
	Recorded bool
	// At is when it was recorded and Day the local day it was recorded on.
	At  int64
	Day string
	// Lockout is true when the run met lockout wording.
	Lockout bool
}

// PasswordInput holds the facts for one password submission on a host.
type PasswordInput struct {
	// Refused counts refused submissions on the host in this run.
	Refused int
	// AlreadyRefused is true when this exact password was already refused.
	AlreadyRefused bool
	// AccountKnown is false when no tenant key (and on a generic host no site
	// override) exists, so the password is a guess.
	AccountKnown bool
	// LockoutSeen is true when lockout wording appeared in this run.
	LockoutSeen bool
	Prior       Exhaustion
	TodayLocal  string
	// CredentialsChangedAt is the latest change to the shared default, an
	// alternate or this host's override; SignedInAt the latest later sign-in
	// on the host.
	CredentialsChangedAt, SignedInAt int64
	// NamedRetry is true when the user named the company in the request and
	// the hold has not been lifted that way before.
	NamedRetry bool
}

// HoldLifted reports whether a recorded exhaustion no longer holds: it was
// on an earlier day, a credential changed or a later run signed in. Naming the
// company lifts it once, except after lockout wording. A new build never does.
func HoldLifted(p Exhaustion, today string, credentialsChangedAt, signedInAt int64, namedRetry bool) bool {
	switch {
	case !p.Recorded || p.Day != today:
		return true
	case credentialsChangedAt > p.At || signedInAt > p.At:
		return true
	}
	return namedRetry && !p.Lockout
}

// PasswordSubmission decides whether another password may be submitted on a
// host. A browser session already on the host is not a submission.
func PasswordSubmission(in PasswordInput) Decision {
	const g = GatePasswordSubmission
	deny := func(code, why string) Decision {
		return decide(g, Deny, code, "password not submitted: %s", why).withClass(ClassCredential)
	}
	limit := MaxRefusalsPerHost
	if !in.AccountKnown {
		limit-- // keep one submission for the sign-in that verifies a sign-up
	}
	switch {
	case in.LockoutSeen:
		return deny(CodeLockoutWording, "the host answered with lockout wording")
	case !HoldLifted(in.Prior, in.TodayLocal, in.CredentialsChangedAt, in.SignedInAt, in.NamedRetry):
		return deny(CodeHostExhaustedToday, "the host's passwords ran out earlier today")
	case in.AlreadyRefused:
		return deny(CodePasswordRepeat, "a refused password is never submitted again in the run")
	case in.Refused >= MaxRefusalsPerHost:
		return deny(CodeRefusalsExhausted, "3 refusals on this host in this run")
	case in.Refused >= limit:
		return deny(CodeGuessHeadroom, "one submission is kept for the sign-in after a sign-up")
	}
	return decide(g, Allow, CodeOK, "%d of %d refusals used", in.Refused, MaxRefusalsPerHost)
}

// StopHold is a credential-class stop's hold on a host.
type StopHold struct {
	Host string
	End  int64
}

// HoldAfterStop returns the 24-hour credential cooldown a stop puts on the
// exact account host. Only a credential-class stop with account-host
// evidence holds: a browser fault is class environment and holds nothing, and
// a failure on a shared board with no account-host evidence cools no
// employer.
func HoldAfterStop(class, accountHost string, haveAccountHostEvidence bool, now int64) (StopHold, bool) {
	if class != ClassCredential || !haveAccountHostEvidence || strings.TrimSpace(accountHost) == "" {
		return StopHold{}, false
	}
	return StopHold{Host: strings.ToLower(strings.TrimSpace(accountHost)), End: now + CredentialCooldown}, true
}

// RowHeld reports whether a list row on rowHost is skipped by an active
// credential cooldown. Hosts are compared whole, so Workday tenants stay
// separate.
func RowHeld(rowHost string, holds []StopHold, now int64) bool {
	h := strings.ToLower(strings.TrimSpace(rowHost))
	for _, c := range holds {
		if c.Host == h && c.End > now {
			return true
		}
	}
	return false
}

// CodeRequestInput holds the facts for a verification-code delivery request.
type CodeRequestInput struct {
	// RequestsThisHost counts requests on this host in this run.
	RequestsThisHost   int
	RequestedOnThisTab bool
	// CodeMailAlreadyTyped is true when this code mail was typed already.
	CodeMailAlreadyTyped bool
}

// CodeRequest allows a "send code" choice at most twice per host per run and
// once per tab, and each code mail to be typed once per run.
func CodeRequest(in CodeRequestInput) Decision {
	const g = GateCodeRequest
	switch {
	case in.CodeMailAlreadyTyped:
		return decide(g, Deny, CodeCodeAlreadyTyped, "each code mail is typed at most once per run")
	case in.RequestedOnThisTab:
		return decide(g, Deny, CodeCodeTabOnce, "a code was already requested on this tab")
	case in.RequestsThisHost >= MaxCodeRequests:
		return decide(g, Deny, CodeCodeRequestCap, "2 code requests on this host in this run; the page stays open for you")
	}
	return decide(g, Allow, CodeOK, "code request %d of %d", in.RequestsThisHost+1, MaxCodeRequests)
}
