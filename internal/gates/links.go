// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Gate names and reason codes of the mail-link gates.
const (
	GateMailLink         Gate = "mail_link"
	GateActivationLink   Gate = "activation_link"
	GateActivationResend Gate = "activation_resend"
	GateResetStart       Gate = "reset_start"
	GateResetLink        Gate = "reset_link"
	GateResetPassword    Gate = "reset_password"

	CodeSigninLinkUserOnly = "signin_link_user_only"
	CodeLinkUserOnly       = "link_user_only"

	CodeActivationUnsupported  = "activation_unsupported_system"
	CodeActivationPrecondition = "activation_precondition_missing"
	CodeLinkFromText           = "link_from_text"
	CodeLinkHostMismatch       = "link_host_mismatch"
	CodeLinkNotHTTPS           = "link_not_https"
	CodeLinkBadShape           = "link_bad_shape"
	CodeLinkTokenNotOpaque     = "link_token_not_opaque"
	CodeLinkSenderMismatch     = "link_sender_mismatch"
	CodeMailNotNewer           = "mail_not_newer"
	CodeMailTooOld             = "mail_too_old"

	CodeResendGenericHost = "resend_generic_host"
	CodeResendOncePerRun  = "resend_once_per_run"

	CodeResetAccountUnknown     = "reset_account_unknown"
	CodeResetPasswordNotRefused = "reset_password_not_refused"
	CodeResetNotWorkday         = "reset_not_workday"
	CodeResetAlreadyStarted     = "reset_already_started"
	CodeResetLockoutWording     = "reset_lockout_wording"
	CodeResetFailedWithin24h    = "reset_failed_within_24h"
	CodeResetPasswordNotSaved   = "reset_password_not_saved"
)

var (
	activationCodes = []string{CodeOK, CodeDemoWorkspace, CodeActivationUnsupported, CodeActivationPrecondition, CodeLinkHostMismatch,
		CodeLinkNotHTTPS, CodeLinkBadShape, CodeLinkTokenNotOpaque, CodeLinkSenderMismatch, CodeMailNotNewer, CodeMailTooOld}
	resetLinkCodes = []string{CodeOK, CodeResetNotWorkday, CodeLinkFromText, CodeLinkHostMismatch, CodeLinkNotHTTPS, CodeLinkBadShape,
		CodeLinkTokenNotOpaque, CodeLinkSenderMismatch, CodeMailNotNewer}
)

// Mail-link bounds. The account stage's default timing reads the four waits.
const (
	ActivationLookback = 30 * Day
	// ActivationSettleWait is the wait after an activation link is opened.
	ActivationSettleWait = 3 * Second
	// ActivationResendWait and ResetMailWait bound the wait for a mail that
	// the run asked for; both poll every MailPollInterval.
	ActivationResendWait = 90 * Second
	ResetMailWait        = 120 * Second
	MailPollInterval     = 5 * Second
	// ResetFailedHold is how long a failed reset blocks another on the host.
	ResetFailedHold = 24 * Hour
)

const (
	workdaySender        = "otp.workday.com"
	successFactorsSender = "successfactors.com"
	sfHashMin            = 20
	tokenMin             = 8
)

// MailLinkKind says what a link found in mail would do.
type MailLinkKind string

const (
	LinkSignin     MailLinkKind = "signin"
	LinkActivation MailLinkKind = "activation"
	LinkReset      MailLinkKind = "reset"
	LinkOther      MailLinkKind = "other"
)

// OpenMailLink decides, by kind, whether a run may open a link from mail. A
// sign-in link presents a session and is the user's to open in a new tab;
// only an activation or a reset link may be opened, and only through its own
// gate.
func OpenMailLink(kind MailLinkKind) Decision {
	const g = GateMailLink
	switch kind {
	case LinkActivation, LinkReset:
		return decide(g, Allow, CodeOK, "a %s link may be opened only after its own locks hold", kind)
	case LinkSignin:
		return decide(g, Deny, CodeSigninLinkUserOnly, "a sign-in link presents a session; open it yourself in a new tab").withClass(ClassCredential)
	}
	return decide(g, Deny, CodeLinkUserOnly, "this mail link is yours to open in a new tab").withClass(ClassCredential)
}

// ATS names a system with an activation shape.
type ATS string

const (
	ATSWorkday        ATS = "workday"
	ATSSuccessFactors ATS = "successfactors"
	ATSGeneric        ATS = "generic"
)

// Precondition is what the run itself just read. The zero value is none, on
// which no activation link is opened.
type Precondition string

const (
	// PreconditionBanner is a sign-in banner saying the account needs email
	// verification.
	PreconditionBanner Precondition = "banner"
	// PreconditionSentPage is the tenant's "activation email sent" page right
	// after the run's own sign-up.
	PreconditionSentPage Precondition = "sent_page"
)

// MailFacts are the facts about one mail and the link taken from it.
type MailFacts struct {
	// Link is the URL taken from the mail: an anchor's href, or for an
	// activation an address the mail prints (owner decision). FromText is true
	// when it was read from text, which a reset link refuses.
	Link         string
	FromText     bool
	SenderDomain string
	ReceivedAt   int64
	// NotNewerThan is the latest instant the mail must be strictly newer than:
	// the sign-up press, a link proven stale, or the resend press. Zero when
	// none applies; after a sign-up (the sent page) zero is a missing press
	// time and refuses.
	NotNewerThan int64
	Now          int64
}

// ActivationInput holds the facts for opening an activation link.
type ActivationInput struct {
	// Demo is the workspace's demo marker.
	Demo         bool
	ATS          ATS
	Precondition Precondition
	// TenantHost is the host the run is signing in to or registered on.
	TenantHost string
	Mail       MailFacts
}

// ActivationLink decides whether the activation link of a mail may be opened.
// All four locks must hold: the run just read that the account needs it, the
// host equals the tenant host, the URL has the system's activation shape with
// an opaque token, and the sender is the system's activation domain.
func ActivationLink(in ActivationInput) Decision {
	const g = GateActivationLink
	deny := func(code, why string) Decision {
		return decide(g, Deny, code, "activation link refused: %s", why).withClass(ClassCredential)
	}
	if in.Demo {
		return demoDeny(g, "opening an activation link").withClass(ClassCredential)
	}
	var sender string
	switch in.ATS {
	case ATSWorkday:
		sender = workdaySender
	case ATSSuccessFactors:
		sender = successFactorsSender
	default:
		return deny(CodeActivationUnsupported, "no activation shape is defined for this system")
	}
	switch {
	case in.Precondition != PreconditionBanner && in.Precondition != PreconditionSentPage:
		return deny(CodeActivationPrecondition, "the run has not just read that the account needs verification")
	case in.Mail.Now-in.Mail.ReceivedAt > ActivationLookback:
		return deny(CodeMailTooOld, "the mail is older than the 30-day lookup")
	case in.Precondition == PreconditionSentPage && in.Mail.NotNewerThan <= 0:
		return deny(CodeMailNotNewer, "the sign-up press time is unknown, so no mail can be shown to follow it")
	case in.Mail.NotNewerThan != 0 && in.Mail.ReceivedAt <= in.Mail.NotNewerThan:
		return deny(CodeMailNotNewer, "the mail is not strictly newer than the instant it must follow")
	}
	u, code, why := checkLinkURL(in.Mail.Link, in.TenantHost)
	if code != "" {
		return deny(code, why)
	}
	switch in.ATS {
	case ATSWorkday:
		if tok, ok := tokenAfter(u, "activate"); !ok {
			return deny(CodeLinkBadShape, "not a Workday /activate/<token> link")
		} else if !opaqueToken(tok) {
			return deny(CodeLinkTokenNotOpaque, "the token is not opaque")
		}
	case ATSSuccessFactors:
		q := u.Query()
		switch {
		case strings.TrimRight(u.Path, "/") != "/career" || q.Get("navBarLevel") != "EMAIL_VERIFICATION":
			return deny(CodeLinkBadShape, "not a SuccessFactors email-verification link")
		case !opaqueHash(q.Get("hash")):
			return deny(CodeLinkTokenNotOpaque, "the hash is not opaque")
		}
	}
	if !strings.EqualFold(strings.TrimSpace(in.Mail.SenderDomain), sender) {
		return deny(CodeLinkSenderMismatch, "the sender is not this system's activation domain")
	}
	return decide(g, Allow, CodeOK, "all four locks hold; the sign-in that follows is the verdict")
}

// checkLinkURL parses a link and checks the https and host-equality locks. It
// never puts the URL into a message.
func checkLinkURL(raw, tenantHost string) (*url.URL, string, string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil || u.Host == "" || u.User != nil:
		return nil, CodeLinkBadShape, "the link does not parse as a plain URL"
	case u.Scheme != "https":
		return nil, CodeLinkNotHTTPS, "the link is not https"
	case u.Hostname() == "" || strings.TrimSpace(tenantHost) == "" ||
		!strings.EqualFold(u.Hostname(), strings.TrimSpace(tenantHost)) || (u.Port() != "" && u.Port() != "443"):
		return nil, CodeLinkHostMismatch, "the link host is not the tenant host"
	}
	return u, "", ""
}

// tokenAfter returns the path segment that follows the named segment.
func tokenAfter(u *url.URL, names ...string) (string, bool) {
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(segs); i++ {
		for _, n := range names {
			if strings.EqualFold(segs[i], n) && segs[i+1] != "" {
				return segs[i+1], true
			}
		}
	}
	return "", false
}

// opaqueToken: at least 8 characters including a letter and a digit.
func opaqueToken(s string) bool {
	var letter, digit bool
	for _, r := range s {
		letter = letter || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit = digit || r >= '0' && r <= '9'
	}
	return len(s) >= tokenMin && letter && digit
}

var hashRE = lazyregexp.New(`^[A-Za-z0-9+/=_-]+$`)

// opaqueHash: at least 20 characters from the base64 or URL-safe alphabet.
func opaqueHash(s string) bool { return len(s) >= sfHashMin && hashRE.MatchString(s) }

// ResendInput holds the facts for the tenant's own verification-resend press.
type ResendInput struct {
	ATS            ATS
	PressesThisRun int
}

// ActivationResend allows the one press of the tenant's own "request a
// verification email" control per run; a generic host gets none.
func ActivationResend(in ResendInput) Decision {
	const g = GateActivationResend
	switch {
	case in.ATS != ATSWorkday && in.ATS != ATSSuccessFactors:
		return decide(g, Deny, CodeResendGenericHost, "a generic host gets no resend press")
	case in.PressesThisRun >= 1:
		return decide(g, Deny, CodeResendOncePerRun, "the run's one resend press is spent")
	}
	return decide(g, Allow, CodeOK, "one resend press; only mail strictly newer than it qualifies")
}

// ResetStartInput holds the facts for starting a password reset.
type ResetStartInput struct {
	// Demo is the workspace's demo marker.
	Demo bool
	Now  int64
	// AccountKnown: a tenant key is stored, or the run's own sign-up reported
	// that the account exists.
	AccountKnown bool
	// SavedPasswordRefused is true when the saved password was refused, or the
	// host is out of password budget or held, so the saved one cannot be tried.
	SavedPasswordRefused bool
	HostIsWorkday        bool
	// ResetsStartedThisRun counts resets already started on this host in this
	// run, including one that pressed nothing.
	ResetsStartedThisRun int
	LockoutWordingSeen   bool
	// LastFailedResetAt is the time of the host's last failed reset, zero when
	// none.
	LastFailedResetAt int64
}

// ResetStart decides whether a password reset may begin on a host.
func ResetStart(in ResetStartInput) Decision {
	const g = GateResetStart
	deny := func(code, why string) Decision {
		return decide(g, Deny, code, "password reset not started: %s", why).withClass(ClassCredential)
	}
	switch {
	case in.Demo:
		return demoDeny(g, "a password reset").withClass(ClassCredential)
	case !in.HostIsWorkday:
		return deny(CodeResetNotWorkday, "only a Workday tenant gets a reset")
	case !in.AccountKnown:
		return deny(CodeResetAccountUnknown, "the account is not known to exist")
	case !in.SavedPasswordRefused:
		return deny(CodeResetPasswordNotRefused, "the saved password was not refused")
	case in.LockoutWordingSeen:
		return deny(CodeResetLockoutWording, "the host answered with lockout wording")
	case in.ResetsStartedThisRun >= 1:
		return deny(CodeResetAlreadyStarted, "one reset per host per run")
	case in.LastFailedResetAt != 0 && in.Now-in.LastFailedResetAt < ResetFailedHold:
		return deny(CodeResetFailedWithin24h, "a reset on this host failed within the last 24 hours")
	}
	return decide(g, Allow, CodeOK, "reset may start; its sign-in is the verdict")
}

// PasswordSource is where a password about to be set came from.
type PasswordSource string

const (
	PasswordSiteOverride  PasswordSource = "site_override"
	PasswordSharedDefault PasswordSource = "shared_default"
	PasswordAlternate     PasswordSource = "alternate"
	PasswordTenantKey     PasswordSource = "tenant_key"
	PasswordGenerated     PasswordSource = "generated"
)

// ResetPassword allows only the user's saved password: this host's override,
// else the shared default.
func ResetPassword(src PasswordSource) Decision {
	const g = GateResetPassword
	if src == PasswordSiteOverride || src == PasswordSharedDefault {
		return decide(g, Allow, CodeOK, "the user's saved password")
	}
	return decide(g, Deny, CodeResetPasswordNotSaved, "a reset sets only the saved password, never an alternate, a tenant key or a generated one").withClass(ClassCredential)
}

var resetSegments = []string{"passwordreset", "resetpassword", "reset-password", "password-reset"}

// ResetLinkInput holds the facts for opening a reset link.
type ResetLinkInput struct {
	HostIsWorkday bool
	TenantHost    string
	Mail          MailFacts
}

// ResetLink applies the activation locks to a reset mail with these
// differences: the path carries a reset segment and an opaque token, and the
// mail was received strictly after the press (Mail.NotNewerThan is that press).
func ResetLink(in ResetLinkInput) Decision {
	const g = GateResetLink
	deny := func(code, why string) Decision {
		return decide(g, Deny, code, "reset link refused: %s", why).withClass(ClassCredential)
	}
	switch {
	case !in.HostIsWorkday:
		return deny(CodeResetNotWorkday, "a generic host gets no reset")
	case in.Mail.FromText:
		return deny(CodeLinkFromText, "nothing is read from anchor text or prose")
	case in.Mail.NotNewerThan == 0 || in.Mail.ReceivedAt <= in.Mail.NotNewerThan:
		return deny(CodeMailNotNewer, "the mail was not received strictly after the press")
	}
	u, code, why := checkLinkURL(in.Mail.Link, in.TenantHost)
	if code != "" {
		return deny(code, why)
	}
	if tok, ok := tokenAfter(u, resetSegments...); !ok {
		return deny(CodeLinkBadShape, "no reset path segment")
	} else if !opaqueToken(tok) {
		return deny(CodeLinkTokenNotOpaque, "the token is not opaque")
	}
	if !strings.EqualFold(strings.TrimSpace(in.Mail.SenderDomain), workdaySender) {
		return deny(CodeLinkSenderMismatch, "the sender is not the Workday sender")
	}
	return decide(g, Allow, CodeOK, "all four locks hold; the sign-in that follows is the verdict")
}

// ResetCodeMail decides whether a mailed reset code may be typed: the sender
// is the Workday sender and the mail arrived strictly after the press.
func ResetCodeMail(senderDomain string, receivedAt, pressedAt int64) Decision {
	const g = GateResetLink
	switch {
	case !strings.EqualFold(strings.TrimSpace(senderDomain), workdaySender):
		return decide(g, Deny, CodeLinkSenderMismatch, "reset code refused: the sender is not the Workday sender").withClass(ClassCredential)
	case pressedAt == 0 || receivedAt <= pressedAt:
		return decide(g, Deny, CodeMailNotNewer, "reset code refused: the mail was not received strictly after the press").withClass(ClassCredential)
	}
	return decide(g, Allow, CodeOK, "reset code from the Workday sender after the press")
}

// ResetOutcomeInput is how a reset ended.
type ResetOutcomeInput struct {
	// SignedIn is the verdict: the single sign-in after the reset worked.
	SignedIn bool
	// PressedNothing is true when the page had no forgot-password control.
	PressedNothing bool
	// PolicyRefused is true when the reset page refused the saved password.
	PolicyRefused bool
}

// ResetOutcome is what the caller records after a reset.
type ResetOutcome struct {
	Result string
	// Hold24h starts the 24-hour hold on resets and on the host's credential
	// cooldown.
	Hold24h bool
	// ClearsHolds clears the host's credential cooldown and same-day hold,
	// and StoreTenantKey stores the password as the tenant key.
	ClearsHolds, StoreTenantKey bool
	Class                       string
	Message                     string
}

// SettleReset maps how a reset ended to what is recorded. A reset that
// pressed nothing spends the run's one try without starting the hold.
func SettleReset(in ResetOutcomeInput) ResetOutcome {
	switch {
	case in.SignedIn:
		return ResetOutcome{Result: "succeeded", ClearsHolds: true, StoreTenantKey: true}
	case in.PressedNothing:
		return ResetOutcome{Result: "not_sent", Class: ClassCredential, Message: "no forgot-password control was found"}
	case in.PolicyRefused:
		return ResetOutcome{Result: "failed", Hold24h: true, Class: ClassCredential, Message: "the tenant refused the saved password; change the saved password"}
	}
	return ResetOutcome{Result: "failed", Hold24h: true, Class: ClassCredential, Message: "the reset did not restore sign-in"}
}
