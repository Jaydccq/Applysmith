// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"math"
	"strings"
	"testing"
)

func solveOK(mut func(*CaptchaInput)) CaptchaInput {
	in := CaptchaInput{
		Run: RunApplySubmit, Switch: SolverOn, KeyConfigured: true, Challenge: ChallengeHCaptcha, Point: PointBeforePress,
		Skip: SkipNotListed, BalanceKnown: true, BalanceMicroUSD: 1_000_000,
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func TestGates_Captcha_OnlySubmittingExplicitRunsGetSolver(t *testing.T) {
	for _, run := range []RunKind{RunDryRun, "scan", ""} {
		t.Run(string(run), func(t *testing.T) {
			got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Run = run })), GateCaptchaSolve, Deny, CodeRunNotSubmitting)
		})
	}
	got(t, CaptchaSolve(solveOK(nil)), GateCaptchaSolve, Allow, CodeOK)
}

func TestGates_Captcha_KeyFromSecretStoreSwitchFromPolicy(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*CaptchaInput)
		v    Verdict
		code string
	}{
		{"switch off with a stored key", func(i *CaptchaInput) { i.Switch = SolverOff }, Deny, CodeSolverOff},
		{"unreadable or invalid policy reads off", func(i *CaptchaInput) { i.Switch = "" }, Deny, CodeSolverOff},
		{"switch value other than on", func(i *CaptchaInput) { i.Switch = "yes" }, Deny, CodeSolverOff},
		{"no key with the switch on", func(i *CaptchaInput) { i.KeyConfigured = false }, Deny, CodeKeyMissing},
		{"switched on again, key still stored", nil, Allow, CodeOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			// No environment variable enables or overrides the solver: the
			// two set above change nothing.
			got(t, CaptchaSolve(solveOK(c.mut)), GateCaptchaSolve, c.v, c.code)
		})
	}
}

func TestGates_Captcha_SupportedKindsOnly(t *testing.T) {
	for _, k := range []string{ChallengeRecaptchaV2, ChallengeRecaptchaV2Enterprise, ChallengeHCaptcha, ChallengeTurnstileWidget} {
		got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Challenge = k })), GateCaptchaSolve, Allow, CodeOK)
	}
}

func TestGates_Captcha_UnsupportedChallengesNamedAndLeft(t *testing.T) {
	for _, k := range []string{ChallengeRecaptchaV3, ChallengeInterstitial, ChallengeImagePuzzle, ChallengeArkose, ChallengeDataDome} {
		t.Run(k, func(t *testing.T) {
			d := CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Challenge = k }))
			got(t, d, GateCaptchaSolve, Deny, CodeUnsupportedChallenge)
			if !strings.Contains(d.Reason, k) || !strings.HasPrefix(d.Reason, "captcha: ") {
				t.Fatalf("reason = %q", d.Reason)
			}
		})
	}
}

func TestGates_Captcha_WidgetOnlyPageAllowed(t *testing.T) {
	for _, k := range []string{ChallengeRecaptchaV2, ChallengeRecaptchaV2Enterprise, ChallengeHCaptcha} {
		got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Challenge, i.Point = k, PointChallengeOnly })), GateCaptchaSolve, Allow, CodeOK)
	}
}

func TestGates_Captcha_TurnstileOnChallengeOnlyPageIsFullPageInterstitial(t *testing.T) {
	d := CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Challenge, i.Point = ChallengeTurnstileWidget, PointChallengeOnly }))
	got(t, d, GateCaptchaSolve, Deny, CodeTurnstileFullPage)
	if !strings.HasPrefix(d.Reason, "captcha: not attempted") || !strings.Contains(d.Reason, "full-page Cloudflare interstitial") {
		t.Fatalf("reason = %q", d.Reason)
	}
	// Embedded in a page with application controls it is still solved, at
	// every other evidence point.
	for _, p := range []string{PointBeforePress, PointAfterPress, PointNextUnchanged, PointICIMSEmail, PointGenericSignin, PointGenericSignup} {
		got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Challenge, i.Point = ChallengeTurnstileWidget, p })), GateCaptchaSolve, Allow, CodeOK)
	}
}

func TestGates_Captcha_ImplausibleBalanceIsRefusedWithItsOwnReason(t *testing.T) {
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.BalanceMicroUSD = CaptchaBalanceCeilingMicroUSD })), GateCaptchaSolve, Allow, CodeOK)
	for _, b := range []int64{CaptchaBalanceCeilingMicroUSD + 1, math.MaxInt64} {
		got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.BalanceMicroUSD = b })), GateCaptchaSolve, Deny, CodeBalanceImplausible)
	}
}

func TestGates_Captcha_EvidencePointsAllowed(t *testing.T) {
	for _, p := range []string{PointBeforePress, PointAfterPress, PointNextUnchanged, PointICIMSEmail, PointGenericSignin, PointGenericSignup, PointChallengeOnly} {
		got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Point = p })), GateCaptchaSolve, Allow, CodeOK)
	}
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Point = "after_the_fact" })), GateCaptchaSolve, Deny, CodeEvidencePointUnknown)
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Point = "" })), GateCaptchaSolve, Deny, CodeEvidencePointUnknown)
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.CombinedAccountAndApplication = true })), GateCaptchaSolve, Deny, CodeCombinedFormUserOnly)
}

func TestGates_Captcha_BoundsTwoSolvesFloorPollingSkipList(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*CaptchaInput)
		v    Verdict
		code string
	}{
		{"no solve yet", func(i *CaptchaInput) { i.SolvesThisRun = 0 }, Allow, CodeOK},
		{"one solve used, the second allowed", func(i *CaptchaInput) { i.SolvesThisRun = 1 }, Allow, CodeOK},
		{"two used, the third refused", func(i *CaptchaInput) { i.SolvesThisRun = 2 }, Deny, CodeSolveCap},
		{"far above the cap", func(i *CaptchaInput) { i.SolvesThisRun = 9 }, Deny, CodeSolveCap},
		{"balance 0.49", func(i *CaptchaInput) { i.BalanceMicroUSD = 490_000 }, Deny, CodeBalanceLow},
		{"balance just under 0.50", func(i *CaptchaInput) { i.BalanceMicroUSD = CaptchaBalanceFloorMicroUSD - 1 }, Deny, CodeBalanceLow},
		{"balance exactly 0.50", func(i *CaptchaInput) { i.BalanceMicroUSD = CaptchaBalanceFloorMicroUSD }, Allow, CodeOK},
		{"balance 0.51", func(i *CaptchaInput) { i.BalanceMicroUSD = 510_000 }, Allow, CodeOK},
		{"balance unreadable", func(i *CaptchaInput) { i.BalanceKnown = false }, Deny, CodeBalanceUnknown},
		{"skip-list company", func(i *CaptchaInput) { i.Skip = SkipListed }, Deny, CodeSkipListCompany},
		{"skip list unreadable", func(i *CaptchaInput) { i.Skip = SkipUnreadable }, Deny, CodeSkipListUnreadable},
	} {
		t.Run(c.name, func(t *testing.T) { got(t, CaptchaSolve(solveOK(c.mut)), GateCaptchaSolve, c.v, c.code) })
	}
	d := CaptchaSolve(solveOK(func(i *CaptchaInput) { i.BalanceMicroUSD = 490_000 }))
	if d.Reason != "captcha: not attempted — the balance is under the USD 0.50 floor" {
		t.Fatalf("reason = %q", d.Reason)
	}
	// Polling: wait 5 s first, poll every 5 s, give up 120 s after creation.
	for _, c := range []struct {
		elapsed int64
		want    PollAction
	}{
		{0, PollWait}, {5*Second - 1, PollWait}, {5 * Second, PollNow}, {60 * Second, PollNow},
		{120*Second - 1, PollNow}, {120 * Second, PollGiveUp}, {121 * Second, PollGiveUp},
	} {
		if got := CaptchaPollStep(c.elapsed); got != c.want {
			t.Errorf("CaptchaPollStep(%d) = %s", c.elapsed, got)
		}
	}
	// One creation retry after a server error, three seconds later.
	for _, c := range []struct {
		status, done int
		retry        bool
		wait         int64
	}{
		{500, 0, true, 3 * Second}, {503, 0, true, 3 * Second}, {599, 0, true, 3 * Second}, {500, 1, false, 0},
		{499, 0, false, 0}, {600, 0, false, 0}, {200, 0, false, 0}, {429, 0, false, 0},
	} {
		if r, w := CaptchaCreateRetry(c.status, c.done); r != c.retry || w != c.wait {
			t.Errorf("CaptchaCreateRetry(%d, %d) = %v %d", c.status, c.done, r, w)
		}
	}
	if CaptchaMaxSolves != 2 || CaptchaBalanceFloorMicroUSD != 500_000 {
		t.Fatal("solver bounds changed")
	}
}

func TestGates_Captcha_RefusedTokenOneRetryInsideCap(t *testing.T) {
	// A refused token is one solve; one more fits inside the cap of two and a
	// further one does not.
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.SolvesThisRun = 1 })), GateCaptchaSolve, Allow, CodeOK)
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.SolvesThisRun = 2 })), GateCaptchaSolve, Deny, CodeSolveCap)
}

func TestGates_Captcha_TaskNotSupportedLeavesToUser(t *testing.T) {
	line := CaptchaErrorLine("ERROR_TASK_NOT_SUPPORTED", 200)
	if line != "captcha: service error ERROR_TASK_NOT_SUPPORTED (HTTP 200)" {
		t.Fatalf("line = %q", line)
	}
}

func TestGates_Captcha_NoSecretInDetailsErrorsFromCodeAndStatus(t *testing.T) {
	// Built at run time so no credential-shaped literal sits in the source.
	key := strings.Repeat("k", 8) + strings.Repeat("7", 24)
	token := "03AFcW" + strings.Repeat("t", 40)
	page := "https://apply.example.test/form?session=" + strings.Repeat("s", 20)
	for _, code := range []string{key, token, page, "ERROR WITH SPACES", "error_lower", "", strings.Repeat("A", 65), "ERROR_KEY_DOES_NOT_EXIST"} {
		line := CaptchaErrorLine(code, 403)
		for _, secret := range []string{key, token, page} {
			if strings.Contains(line, secret) {
				t.Fatalf("line %q leaks a secret", line)
			}
		}
		if !strings.HasPrefix(line, "captcha: service error ERROR_") || !strings.HasSuffix(line, "(HTTP 403)") {
			t.Fatalf("line = %q", line)
		}
	}
	// A refusal reason carries no key, token or URL either.
	d := CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Challenge = page }))
	got(t, d, GateCaptchaSolve, Deny, CodeUnsupportedChallenge)
	if strings.Contains(d.Reason, page) || strings.Contains(d.Reason, "session=") {
		t.Fatalf("reason %q echoes free text", d.Reason)
	}
}
