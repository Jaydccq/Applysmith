// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"fmt"
	"slices"
)

// Verdict is what a gate decides about one action.
type Verdict string

const (
	// Allow lets the action proceed.
	Allow Verdict = "allow"
	// Hold parks the action until the user confirms it.
	Hold Verdict = "hold"
	// Deny refuses the action.
	Deny Verdict = "deny"
	// AllowWithTrace is written by the confirmation step for an allow that
	// carries a trace; no gate returns it.
	AllowWithTrace Verdict = "allow-with-trace"
)

// Gate names one pure decision function; it is the audit table's gate column.
type Gate string

// Time is injected as Unix milliseconds because a pure package cannot read a
// clock or import the time package.
const (
	Second int64 = 1000
	Minute       = 60 * Second
	Hour         = 60 * Minute
	Day          = 24 * Hour
)

// Stop classes a decision can carry when the run ends on it.
const (
	ClassPosting     = "posting"
	ClassEnvironment = "environment"
	ClassDefect      = "defect"
	ClassCredential  = "credential"
	ClassAnswerGap   = "answer-gap"
)

// MaxReasonRunes is the audit table's cap on a human-readable reason.
const MaxReasonRunes = 300

// Decision is the result of a gate. Reason never carries a secret, token or
// link; the caller persists the decision as one gate_decisions row.
type Decision struct {
	Gate    Gate
	Verdict Verdict
	// Code is stable and machine-readable, drawn from the gate's vocabulary.
	Code   string
	Reason string
	// Class is the stop class when a deny ends the run; empty otherwise.
	Class string
}

// Allowed reports whether the action may proceed.
func (d Decision) Allowed() bool { return d.Verdict == Allow }

func decide(g Gate, v Verdict, code, format string, args ...any) Decision {
	return Decision{Gate: g, Verdict: v, Code: code, Reason: capReason(fmt.Sprintf(format, args...))}
}

func (d Decision) withClass(c string) Decision { d.Class = c; return d }

func capReason(s string) string {
	r := []rune(s)
	if len(r) <= MaxReasonRunes {
		return s
	}
	return string(r[:MaxReasonRunes-3]) + "..."
}

// Valid reports whether d is well formed: a known gate and verdict, a code
// from the gate's closed vocabulary and a reason within the cap.
func (d Decision) Valid() bool {
	codes, ok := vocabulary[d.Gate]
	return ok && slices.Contains(codes, d.Code) &&
		(d.Verdict == Allow || d.Verdict == Hold || d.Verdict == Deny) &&
		d.Reason != "" && len([]rune(d.Reason)) <= MaxReasonRunes
}

// vocabulary is each gate's closed set of reason codes. The constants are
// declared beside the gate that returns them.
var vocabulary = map[Gate][]string{
	GateSubmitRegistration:     {CodeOK, CodeToolNotInSubmitLane},
	GateSubmitAuthorization:    {CodeDemoWorkspace, CodeSubmitNotRequested, CodeNoExplicitUserRequest, CodeUnattendedCaller, CodeSubmitDashboardOnly, CodeToolNotInSubmitLane, CodeApplyDashboardOnly, CodeOK},
	GateAgentSubmit:            {CodeURLNotHTTP, CodeExactURLNamed, CodeOutsideContentRead, CodeHostNotNamedNotListed, CodeHostNamedOrListed, CodeBatchOverUserList, CodeToolNotInSubmitLane, CodeSubmitDashboardOnly, CodeUnattendedCaller},
	GateHeldSubmitConfirmation: {CodeOK, CodeRemoteCannotConfirm, CodeConfirmationNotUserMessage, CodeSubmitDashboardOnly},
	GateControlPress:           {CodeOK, CodeUnattendedCaller, CodeWithdrawControl, CodeDiscardControl, CodeSubmitInWalk, CodeSubmitNotAuthorized, CodeSubmitClaimMissing, CodeControlNotFound, CodeSubmitLabelMismatch, CodeNotNavigation, CodeMixedScriptLabel, CodeNavigationInRung, CodeDestructiveLabel},
	GateDuplicateGuard:         {CodeOK, CodeNotSubmitting, CodeEvidenceUnreadable, CodeLiveSubmitting, CodeAlreadySubmitted, CodeTrackerApplied, CodeUnconfirmedSubmission, CodeMayHaveSubmitted, CodeResubmitLifted, CodeCorrectionLifted},
	GatePressClaim:             {CodeOK, CodeClaimLedgerUnreadable, CodeClaimUnwritable, CodeClaimLost},
	GateResubmit:               {CodeOK, CodeResubmitNotRequested, CodeResubmitCompanyNotNamed, CodeResubmitWithList, CodeResubmitCallerRefused, CodeResubmitQuestion, CodeResubmitNegated},
	GateCorrection:             {CodeOK, CodeCorrectionSingleOnly, CodeCorrectionNotRequested, CodeCorrectionQuestion, CodeCorrectionNoCompany, CodeCorrectionNoRow, CodeCorrectionOnce, CodeCorrectionLedgerUnreadable, CodeCorrectionNoSubmit, CodeCorrectionNegated},
	GateSkipListConfig:         {CodeOK, CodeSkipListMalformed, CodeSkipListTooLong, CodeSkipListEntryLong},
	GateSkipList:               {CodeOK, CodeSkipListed, CodeSkipSingleURL, CodeDuplicateBeforeSkip},
	GateIdentityReview:         {CodeOK, CodeIdentityMismatch},
	GateReviewFlag:             {CodeFlagBlocks, CodeFlagAdvice, CodeFlagNoProbability, CodeFlagDropped, CodeFlagNotListed, CodeFlagFallback, CodeFlagSelfCleared, CodeFlagRelocationNotPresence, CodeFlagOwnerDefaultAbsence, CodeFlagRecordAbsence, CodeFlagPreferenceNotPast, CodeFlagSoleOption},
	GateReadiness:              {CodeOK, CodePageUnreadable, CodeIdentityMismatch, CodeRequiredUnanswered, CodeValueNotHeld, CodeReviewContradiction},
	GateConsentControl:         {CodeNotConsentControl, CodeLegalDeclarationOn, CodeLegalDeclarationOff, CodeMarketingOptIn, CodeUserBankAnswer, CodeOptionalNeedsUser, CodeSelfIDCheckbox},
	GateLegalSetting:           {CodeLegalSettingOn, CodeLegalSettingOff},
	GateCLIInstall:             {CodeCLILinkCreated, CodeCLILinkRemoved, CodeCLILinkForeign},
	GateStatement:              {CodeOK, CodeApplicationStatement},
	GateMailLink:               {CodeOK, CodeSigninLinkUserOnly, CodeLinkUserOnly},
	GateActivationResend:       {CodeOK, CodeResendGenericHost, CodeResendOncePerRun},
	GateActivationLink:         activationCodes,
	GateResetStart:             {CodeOK, CodeDemoWorkspace, CodeResetAccountUnknown, CodeResetPasswordNotRefused, CodeResetNotWorkday, CodeResetAlreadyStarted, CodeResetLockoutWording, CodeResetFailedWithin24h},
	GateResetLink:              resetLinkCodes,
	GateResetPassword:          {CodeOK, CodeResetPasswordNotSaved},
	GateAccountCreation:        {CodeDemoWorkspace, CodeCooldownActive, CodeAgreementNeedsSetting, CodeOK, CodeNotUserStarted, CodeSystemUnsupported},
	GatePasswordSubmission:     {CodeOK, CodePasswordRepeat, CodeRefusalsExhausted, CodeGuessHeadroom, CodeLockoutWording, CodeHostExhaustedToday},
	GateCodeRequest:            {CodeOK, CodeCodeRequestCap, CodeCodeTabOnce, CodeCodeAlreadyTyped},
	GateCaptchaSolve:           {CodeOK, CodeDemoWorkspace, CodeRunNotSubmitting, CodeSolverOff, CodeKeyMissing, CodeUnsupportedChallenge, CodeEvidencePointUnknown, CodeCombinedFormUserOnly, CodeSkipListCompany, CodeSkipListUnreadable, CodeSolveCap, CodeBalanceLow, CodeBalanceUnknown, CodeBalanceImplausible, CodeTurnstileFullPage},
	GateDemoWorkspace:          {CodeOK, CodeDemoWorkspace},
	GateCookieConsent:          {CodeOK, CodeNotConsentGate, CodeNoClosingControl, CodeAcceptAllLastResort},
	GateDesktopAction:          {CodeOK, CodeDesktopPaused, CodeDesktopAppRefused, CodeDesktopAppNotNamed, CodeDesktopNoControl, CodeDesktopControl, CodeDesktopWalkOnly, CodeDesktopTypingRefused, CodeDesktopNotTextField, CodeDesktopKeyRefused, CodeDesktopTextTooLong, CodeDesktopUnknownAction, CodeMixedScriptLabel},
}

// Code constants shared by several gates.
const (
	CodeOK                  = "ok"
	CodeToolNotInSubmitLane = "tool_not_in_submit_lane"
)

// Tools that may carry the submit capability.
const (
	ToolAutofillApplication = "autofill_application"
	ToolApplyFromList       = "apply_from_list"
)

// Caller is the audit table's caller column.
type Caller string

const (
	CallerAskAgent  Caller = "ask_agent"
	CallerApplyList Caller = "apply_list"
	CallerCLI       Caller = "cli"
	CallerDashboard Caller = "dashboard"
	CallerScheduler Caller = "scheduler"
	CallerTelegram  Caller = "telegram"
)

func inSubmitLane(tool string) bool {
	return tool == ToolAutofillApplication || tool == ToolApplyFromList
}
