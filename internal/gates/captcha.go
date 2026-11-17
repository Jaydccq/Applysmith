// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"fmt"
	"strings"
)

// Gate name and reason codes of the captcha solver gate.
const (
	GateCaptchaSolve Gate = "captcha_solve"

	CodeRunNotSubmitting     = "run_not_submitting"
	CodeSolverOff            = "solver_off"
	CodeKeyMissing           = "key_missing"
	CodeUnsupportedChallenge = "unsupported_challenge"
	CodeEvidencePointUnknown = "evidence_point_unknown"
	CodeCombinedFormUserOnly = "combined_form_user_only"
	CodeSkipListCompany      = "skip_list_company"
	CodeSkipListUnreadable   = "skip_list_unreadable"
	CodeSolveCap             = "solve_cap"
	CodeBalanceLow           = "balance_low"
	CodeBalanceImplausible   = "balance_implausible"
	CodeTurnstileFullPage    = "turnstile_full_page"
	CodeBalanceUnknown       = "balance_unknown"
)

// Captcha solver bounds, defined once for the solver, the tests and the text
// that names them.
const (
	CaptchaMaxSolves = 2
	// CaptchaBalanceFloorMicroUSD is USD 0.50 in millionths of a dollar.
	CaptchaBalanceFloorMicroUSD int64 = 500_000
	// CaptchaBalanceCeilingMicroUSD is USD 1,000,000: a larger figure is not a
	// real account balance, so it is not trusted.
	CaptchaBalanceCeilingMicroUSD int64 = 1_000_000_000_000
	CaptchaFirstWait                    = 5 * Second
	CaptchaPollEvery                    = 5 * Second
	CaptchaGiveUpAfter                  = 120 * Second
	CaptchaCreateRetryWait              = 3 * Second
)

// RunKind says what started the run.
type RunKind string

const (
	// RunApplySubmit is an explicit, user-started apply run that submits.
	RunApplySubmit RunKind = "apply_submit"
	RunDryRun      RunKind = "dry_run"
)

// SolverSwitch is the captcha_solver policy key as read.
type SolverSwitch string

const (
	SolverOn  SolverSwitch = "on"
	SolverOff SolverSwitch = "off"
)

// Challenge kinds the solver recognises.
const (
	ChallengeRecaptchaV2           = "recaptcha_v2"
	ChallengeRecaptchaV2Enterprise = "recaptcha_v2_enterprise"
	ChallengeHCaptcha              = "hcaptcha"
	ChallengeTurnstileWidget       = "turnstile_widget"
	ChallengeRecaptchaV3           = "recaptcha_v3"
	ChallengeInterstitial          = "cloudflare_interstitial"
	ChallengeImagePuzzle           = "image_puzzle"
	ChallengeArkose                = "arkose"
	ChallengeDataDome              = "datadome"
)

// Evidence points: where in a run a solve is allowed.
const (
	PointBeforePress   = "before_press"
	PointAfterPress    = "after_press"
	PointNextUnchanged = "next_unchanged"
	PointICIMSEmail    = "icims_email_step"
	PointGenericSignin = "generic_signin"
	PointGenericSignup = "generic_signup"
	PointChallengeOnly = "challenge_only_page"
)

var (
	solvable = []string{ChallengeRecaptchaV2, ChallengeRecaptchaV2Enterprise, ChallengeHCaptcha, ChallengeTurnstileWidget}
	points   = []string{PointBeforePress, PointAfterPress, PointNextUnchanged, PointICIMSEmail, PointGenericSignin, PointGenericSignup, PointChallengeOnly}
)

// SkipStatus is the batch skip list's verdict on the run's company.
type SkipStatus string

const (
	SkipNotListed  SkipStatus = "not_listed"
	SkipListed     SkipStatus = "listed"
	SkipUnreadable SkipStatus = "unreadable"
)

// CaptchaInput holds the facts for one solve.
type CaptchaInput struct {
	// Demo is the workspace's demo marker.
	Demo bool
	Run  RunKind
	// Switch is the captcha_solver policy key; an unreadable or invalid policy
	// is passed as "" and reads off. KeyConfigured is whether the keychain or
	// its environment override holds a 2Captcha key. No environment variable
	// enables or overrides the solver.
	Switch        SolverSwitch
	KeyConfigured bool
	Challenge     string
	Point         string
	// CombinedAccountAndApplication is true for a combined form with a
	// captcha, which is left to the user with nothing typed.
	CombinedAccountAndApplication bool
	Skip                          SkipStatus
	// SolvesThisRun counts task creation requests made or reserved in this run.
	SolvesThisRun int
	// BalanceKnown is false when the once-per-run balance read failed.
	BalanceKnown    bool
	BalanceMicroUSD int64
}

// CaptchaSolve decides whether the user's own 2Captcha account may be used for
// one challenge. The reason is the `captcha:` line for the stage detail; it
// never holds a token, a key or a URL.
func CaptchaSolve(in CaptchaInput) Decision {
	const g = GateCaptchaSolve
	no := func(code, why string) Decision {
		return decide(g, Deny, code, "captcha: not attempted — %s", why)
	}
	switch {
	case in.Demo:
		return demoDeny(g, "a captcha solve")
	case in.Run != RunApplySubmit:
		return no(CodeRunNotSubmitting, "only an explicit, user-started, submitting apply run may use the solver")
	case in.Switch != SolverOn:
		return no(CodeSolverOff, "captcha_solver is off")
	case !in.KeyConfigured:
		return no(CodeKeyMissing, "no 2Captcha key is configured")
	case !contains(solvable, in.Challenge):
		return no(CodeUnsupportedChallenge, "this challenge is not solved by this lane ("+knownKind(in.Challenge)+"); it is yours")
	case !contains(points, in.Point):
		return no(CodeEvidencePointUnknown, "no approved evidence point for this solve")
	case in.Challenge == ChallengeTurnstileWidget && in.Point == PointChallengeOnly:
		// A Turnstile challenge that is the whole page is Cloudflare's own
		// full-page interstitial, which this lane never touches. Turnstile is
		// solved only as a widget on a page with application controls.
		return no(CodeTurnstileFullPage, "a Turnstile challenge that is the whole page is a full-page Cloudflare interstitial; it is yours")
	case in.CombinedAccountAndApplication:
		return no(CodeCombinedFormUserOnly, "a combined account and application form is left to you with nothing typed")
	case in.Skip == SkipListed:
		return no(CodeSkipListCompany, "the company is on your batch skip list")
	case in.Skip != SkipNotListed:
		return no(CodeSkipListUnreadable, "the batch skip list was not read, or its verdict is unknown")
	case in.SolvesThisRun >= CaptchaMaxSolves:
		return no(CodeSolveCap, fmt.Sprintf("%d solves already used in this run", CaptchaMaxSolves))
	case !in.BalanceKnown:
		return no(CodeBalanceUnknown, "the balance could not be read")
	case in.BalanceMicroUSD > CaptchaBalanceCeilingMicroUSD:
		return no(CodeBalanceImplausible, "the balance is above any plausible figure, so it is not trusted")
	case in.BalanceMicroUSD < CaptchaBalanceFloorMicroUSD:
		return no(CodeBalanceLow, "the balance is under the USD 0.50 floor")
	}
	return decide(g, Allow, CodeOK, "captcha: %s solve %d of %d at %s", in.Challenge, in.SolvesThisRun+1, CaptchaMaxSolves, in.Point)
}

// knownKind echoes only a name from the closed vocabulary, so a reason never
// carries free text such as a page URL.
func knownKind(kind string) string {
	switch kind {
	case ChallengeRecaptchaV3, ChallengeInterstitial, ChallengeImagePuzzle, ChallengeArkose, ChallengeDataDome:
		return kind
	}
	return "unrecognised kind"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// PollAction is what the solver does next while a task is pending.
type PollAction string

const (
	PollWait   PollAction = "wait"
	PollNow    PollAction = "poll"
	PollGiveUp PollAction = "give_up"
)

// CaptchaPollStep reads the polling schedule: wait 5 seconds first, then poll
// every 5 seconds, and give up 120 seconds after task creation.
func CaptchaPollStep(elapsed int64) PollAction {
	switch {
	case elapsed >= CaptchaGiveUpAfter:
		return PollGiveUp
	case elapsed < CaptchaFirstWait:
		return PollWait
	}
	return PollNow
}

// CaptchaCreateRetry reports whether a task creation that failed with an HTTP
// status is retried once after a short wait: a server error, first time only.
func CaptchaCreateRetry(httpStatus, retriesDone int) (retry bool, wait int64) {
	if httpStatus >= 500 && httpStatus <= 599 && retriesDone == 0 {
		return true, CaptchaCreateRetryWait
	}
	return false, 0
}

// CaptchaErrorLine builds a failure line from the service's error code and
// the HTTP status only, so a key, a token or a page URL can never reach a log:
// the code is kept only if it is made of capitals, digits and underscores.
func CaptchaErrorLine(code string, httpStatus int) string {
	clean := strings.TrimSpace(code)
	for _, r := range clean {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			clean = ""
			break
		}
	}
	if clean == "" || len(clean) > 64 {
		clean = "ERROR_UNKNOWN"
	}
	return fmt.Sprintf("captcha: service error %s (HTTP %d)", clean, httpStatus)
}
