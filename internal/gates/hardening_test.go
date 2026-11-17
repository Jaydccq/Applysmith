// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"
)

// The strings below spell their lookalike characters as escapes so the source
// stays ASCII.
const (
	dotlessI    = "\u0131"
	fullwidthW  = "\uff37"
	zeroWidth   = "\u200b"
	softHyphen  = "\u00ad"
	cyrillicS   = "\u0405"
	eAcute      = "\u00e9"
	fullwidthQ  = "\uff1f"
	fullwidthEx = "\uff25\uff58\uff41\uff4d\uff50\uff4c\uff45" // "Example" in full-width letters
)

func walk(labels ...string) Decision {
	return PressControl(ControlInput{Stage: StageWalk, Found: true, Labels: labels})
}

func TestGates_Fold_LookalikesAccentsAndFormatCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"R" + eAcute + "sum" + eAcute: "resume",
		"W" + dotlessI + "thdraw":     "withdraw",
		fullwidthW + "ithdraw":        "withdraw",
		"Sub" + softHyphen + "mit":    "submit",
		"Sub" + zeroWidth + "mit":     "submit",
		cyrillicS + "ubmit":           "submit",
		"  Save\u00a0 and\tcontinue ": "save and continue",
		"don\u2019t":                  "don't",
		fullwidthEx + " Corp":         "example corp",
		"caf" + "e\u0301":             "cafe",
		"\xffbroken":                  "broken",
	} {
		if got := foldText(in); got != strings.TrimSpace(want) {
			t.Errorf("foldText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGates_Controls_WalkPressesOnlyRecognisedNavigation(t *testing.T) {
	for _, c := range []struct {
		name   string
		labels []string
		v      Verdict
		code   string
	}{
		{"continue", []string{"Continue"}, Allow, CodeOK},
		{"next with an arrow", []string{"Next \u2192"}, Allow, CodeOK},
		{"save and continue with an ampersand", []string{"Save & Continue"}, Allow, CodeOK},
		{"review", []string{"Review"}, Allow, CodeOK},
		{"apply opener", []string{"Apply now"}, Allow, CodeOK},
		{"label text and value agree", []string{"Continue", "continue", ""}, Allow, CodeOK},
		{"unknown label", []string{"Finalize my candidacy"}, Deny, CodeNotNavigation},
		{"terminal wording", []string{"Apply and save"}, Deny, CodeNotNavigation},
		{"navigation word plus more", []string{"Continue and delete everything"}, Deny, CodeNotNavigation},
		{"one label navigation, another not", []string{"Continue", "Go somewhere else"}, Deny, CodeNotNavigation},
		{"no label at all", nil, Deny, CodeNotNavigation},
		{"destructive: delete account", []string{"Delete account"}, Deny, CodeDiscardControl},
		{"destructive: close my account", []string{"Close my account"}, Deny, CodeDiscardControl},
		{"destructive: delete hidden behind continue", []string{"Continue", "Delete account"}, Deny, CodeDiscardControl},
		{"discard a draft", []string{"Discard draft"}, Deny, CodeDiscardControl},
		{"withdraw", []string{"Withdraw application"}, Deny, CodeWithdrawControl},
		{"withdraw beside continue", []string{"Continue", "Withdraw application"}, Deny, CodeWithdrawControl},
		{"submit in the walk", []string{"Submit application"}, Deny, CodeSubmitInWalk},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, walk(c.labels...), GateControlPress, c.v, c.code)
		})
	}
	// An unrecognised stage is read as the walk, never as the submit stage.
	got(t, PressControl(ControlInput{Stage: "other", Found: true, Labels: []string{"Submit"}, SubmitAuthorized: true, ClaimHeld: true}), GateControlPress, Deny, CodeSubmitInWalk)
}

func TestGates_Controls_WithdrawDeniedUnderEveryOption(t *testing.T) {
	for _, stage := range []ControlStage{StageWalk, StageSubmit, ""} {
		for _, auth := range []bool{false, true} {
			for _, claim := range []bool{false, true} {
				in := ControlInput{Stage: stage, Found: true, Labels: []string{"Withdraw application"}, SubmitAuthorized: auth, ClaimHeld: claim}
				got(t, PressControl(in), GateControlPress, Deny, CodeWithdrawControl)
			}
		}
	}
}

func TestGates_Controls_LookalikeSpellingsFoldBeforeClassifying(t *testing.T) {
	for _, l := range []string{
		"W" + dotlessI + "thdraw application",
		fullwidthW + "ithdraw application",
		"W" + zeroWidth + "ithdraw application",
		"With" + softHyphen + "draw application",
	} {
		got(t, walk(l), GateControlPress, Deny, CodeWithdrawControl)
	}
	for _, l := range []string{"Sub" + softHyphen + "mit application", cyrillicS + "ubmit application", "Sub" + zeroWidth + "mit application"} {
		got(t, walk(l), GateControlPress, Deny, CodeSubmitInWalk)
	}
	// The submit stage reads the same folded label, so a lookalike Submit is
	// still a Submit; even with the authorization and the claim it is not
	// pressed, because a label spelled with lookalikes is
	// not the plain Submit the user authorized.
	in := ControlInput{Stage: StageSubmit, Found: true, Labels: []string{cyrillicS + "ubmit"}}
	got(t, PressControl(in), GateControlPress, Deny, CodeMixedScriptLabel)
	in.SubmitAuthorized, in.ClaimHeld = true, true
	got(t, PressControl(in), GateControlPress, Deny, CodeMixedScriptLabel)
	in.Labels = []string{"Sub" + zeroWidth + "mit"}
	got(t, PressControl(in), GateControlPress, Deny, CodeSubmitLabelMismatch)
	in.Labels = []string{"Submit application"}
	got(t, PressControl(in), GateControlPress, Allow, CodeOK)
}

// A form whose final control reads Apply is pressed as its Submit by the
// submit stage alone, after the authorization and the claim, and only when the
// stage says the probe read it as the form's final control. Nothing else may
// press it as one, and a plain Apply is never a Submit.
func TestGates_Controls_FinalApplyIsASubmitOnlyForTheSubmitStage(t *testing.T) {
	submit := func(labels []string, final, auth, claim bool) Decision {
		return PressControl(ControlInput{Stage: StageSubmit, Found: true, Labels: labels, FinalApply: final, SubmitAuthorized: auth, ClaimHeld: claim})
	}
	apply := []string{"Apply"}
	got(t, submit(apply, true, false, false), GateControlPress, Deny, CodeSubmitNotAuthorized)
	got(t, submit(apply, true, true, false), GateControlPress, Deny, CodeSubmitClaimMissing)
	got(t, submit(apply, true, true, true), GateControlPress, Allow, CodeOK)
	got(t, submit([]string{"Apply now"}, true, true, true), GateControlPress, Allow, CodeOK)
	// Toast: one closing exclamation mark; two, or one inside, are no final Apply.
	got(t, submit([]string{"Apply now!"}, true, true, true), GateControlPress, Allow, CodeOK)
	got(t, submit([]string{"Apply now!!"}, true, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	got(t, submit([]string{"Apply! now"}, true, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	got(t, submit([]string{"Apply for this position", "apply for this position"}, true, true, true), GateControlPress, Allow, CodeOK)
	got(t, submit([]string{"Send"}, true, true, true), GateControlPress, Allow, CodeOK)
	got(t, submit([]string{"Send"}, false, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	got(t, submit([]string{"Send code"}, true, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	// Without the stage's flag a plain Apply is no Submit, even fully authorized.
	got(t, submit(apply, false, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	// The flag is not a licence: the label read at press time must still be a final Apply.
	got(t, submit([]string{"Continue"}, true, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	got(t, submit([]string{"Apply and save"}, true, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	got(t, submit([]string{"Apply", "Delete account"}, true, true, true), GateControlPress, Deny, CodeDiscardControl)
	got(t, submit([]string{"Apply", "Withdraw application"}, true, true, true), GateControlPress, Deny, CodeWithdrawControl)
	got(t, submit([]string{"Ap" + zeroWidth + "ply"}, true, true, true), GateControlPress, Deny, CodeSubmitLabelMismatch)
	got(t, submit([]string{cyrillicS + "pply"}, true, true, true), GateControlPress, Deny, CodeMixedScriptLabel)
	// No other stage presses a control marked as the final Apply, authorized or not.
	for _, stage := range []ControlStage{StageWalk, StageEditorSave, StageAccount, StageAccountFlow, StageReset, StageSigninDialog, StageProfileDialog, StageChallenge, StageDataConsent, "", "other"} {
		in := ControlInput{Stage: stage, Found: true, Labels: apply, FinalApply: true, SubmitAuthorized: true, ClaimHeld: true, InDialog: true}
		got(t, PressControl(in), GateControlPress, Deny, CodeSubmitInWalk)
	}
}

func TestGates_Statement_AccentsFoldAndFamiliesCovered(t *testing.T) {
	for _, c := range []struct {
		label string
		want  StatementKind
	}{
		{"Did you attach a r" + eAcute + "sum" + eAcute + "?", StatementObservable},
		{"Did you attach a resume?", StatementObservable},
		{"Have you uploaded your cover letter?", StatementObservable},
		{"Were you referred by an employee?", StatementOther},
		{"Did you use AI to help complete this application?", StatementOther},
		{"Did you use an AI tool to complete this application?", StatementOther},
		{"Did you use ChatGPT for any answer?", StatementOther},
		{"Did you complete this application yourself?", StatementOther},
		{"Have you applied to Example Corp before?", StatementOther},
		{"Did you apply through a recruiter?", StatementOther},
		{"Why do you want to work here?", StatementNone},
		{"Have you worked with Kubernetes?", StatementNone},
		{"Do you use AI tools in your daily work?", StatementNone},
		{"Have you used AI tools in your work?", StatementNone},
		// Cameo (Greenhouse): the applicant's AI-assisted work, not the application.
		{"How have you used AI-assisted development in the last 6+ months?", StatementNone},
		{"Have you used AI to help write your answers?", StatementOther},
		{"Did you use AI?", StatementOther},
		{"How many years of Go experience do you have?", StatementNone},
		// Authorship of the application: its content is the applicant's own,
		// whether or not the sentence also certifies that it is true.
		{"Can you confirm that everything in this application is true and your own, including your experience and identity?", StatementOther},
		{"Everything in this application is my own.", StatementOther},
		{"I confirm that my answers are in my own words", StatementOther},
		{"Are all of your answers your own?", StatementOther},
		{"Is this your own work?", StatementOther},
		{"Was this application written by you?", StatementOther},
		{"I certify that I completed this application without the use of AI", StatementOther},
		{"I confirm that my responses were not generated by AI", StatementOther},
		{"I confirm my answers are not AI-generated", StatementOther},
		{"I certify that the information I provided is true and complete", StatementNone},
		{"Please answer in your own words", StatementNone},
		{"Is this your own GitHub profile?", StatementNone},
		{"Is the vehicle you will use for deliveries your own?", StatementNone},
		{"I confirm my information is accurate and my own contact details are current", StatementNone},
	} {
		if got := ClassifyStatement(c.label); got != c.want {
			t.Errorf("ClassifyStatement(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}

func TestGates_Statement_ModelLadderNeverAnswersStatementShapedQuestions(t *testing.T) {
	for _, l := range []string{
		"Did you use an AI tool to complete this application?",
		"Did you attach a r" + eAcute + "sum" + eAcute + "?",
		"Have you applied to Example Corp before?",
		"Did you apply through a recruiter?",
	} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); !ex || code != CodeApplicationStatement {
			t.Errorf("%q -> %q %v", l, code, ex)
		}
	}
}

func TestGates_Lockout_HedgeIsBoundedToTheSameSentence(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"Maybe you mistyped it. Your account has been locked.", true},
		{"Maybe you mistyped it. Too many attempts, please wait.", true},
		{"Perhaps a typo!\nYour account is locked", true},
		{"Maybe your account is locked", false},
		{"Your account could be locked; try again", false},
		{"Y\u200bour account has been locked", true},
	} {
		if got := DefiniteLockout(c.text); got != c.want {
			t.Errorf("DefiniteLockout(%q) = %v", c.text, got)
		}
	}
}

func TestGates_Activation_SentPageNeedsAPressTimeAndAMailStrictlyAfterIt(t *testing.T) {
	sent := func(pressed, received int64) ActivationInput {
		return workdayMail(func(i *ActivationInput) {
			i.Precondition = PreconditionSentPage
			i.Mail.NotNewerThan, i.Mail.ReceivedAt = pressed, received
		})
	}
	got(t, ActivationLink(sent(0, t0-Hour)), GateActivationLink, Deny, CodeMailNotNewer)
	got(t, ActivationLink(sent(-1, t0-Hour)), GateActivationLink, Deny, CodeMailNotNewer)
	got(t, ActivationLink(sent(t0-Hour, t0-Hour)), GateActivationLink, Deny, CodeMailNotNewer)
	got(t, ActivationLink(sent(t0-Hour, t0-Hour+1)), GateActivationLink, Allow, CodeOK)
	// A banner needs no press time: the mail only has to be recent.
	got(t, ActivationLink(workdayMail(nil)), GateActivationLink, Allow, CodeOK)
}

func TestGates_Links_EmptyHostFailsLockOne(t *testing.T) {
	got(t, ActivationLink(workdayMail(func(i *ActivationInput) {
		i.TenantHost, i.Mail.Link = "", "https://:443/site/activate/a1b2c3d4e5"
	})), GateActivationLink, Deny, CodeLinkHostMismatch)
	got(t, ActivationLink(workdayMail(func(i *ActivationInput) { i.TenantHost = "  " })), GateActivationLink, Deny, CodeLinkHostMismatch)
	got(t, ActivationLink(workdayMail(func(i *ActivationInput) { i.Mail.Link = "https://:443/site/activate/a1b2c3d4e5" })), GateActivationLink, Deny, CodeLinkHostMismatch)
	got(t, ResetLink(ResetLinkInput{HostIsWorkday: true, TenantHost: "", Mail: MailFacts{
		Link: "https://:443/site/passwordreset/Zx9Yw8Vv7U", SenderDomain: "otp.workday.com", ReceivedAt: t0 - Minute, NotNewerThan: t0 - 2*Minute, Now: t0,
	}}), GateResetLink, Deny, CodeLinkHostMismatch)
}

func TestGates_Resubmit_NegatedOrAskedAsAQuestionIsNotARequest(t *testing.T) {
	door := func(msg string) Decision {
		return ResubmitDoor(ResubmitInput{Caller: CallerAskAgent, Message: msg, Company: "Example Corp"})
	}
	got(t, door("Please resubmit Example Corp"), GateResubmit, Allow, CodeOK)
	got(t, door("Nothing went wrong, so resubmit Example Corp now"), GateResubmit, Allow, CodeOK)
	got(t, door("Hold on, don't wait: resubmit Example Corp"), GateResubmit, Allow, CodeOK)
	for _, m := range []string{
		"Do not resubmit Example Corp",
		"Don\u2019t resubmit Example Corp",
		"never resubmit Example Corp",
		"There is no need to resubmit Example Corp",
		"stop, do not re-send Example Corp",
		"Example Corp: please do not send it again",
	} {
		got(t, door(m), GateResubmit, Deny, CodeResubmitNegated)
	}
	for _, m := range []string{"Should I resubmit Example Corp?", "Resubmit Example Corp" + fullwidthQ} {
		got(t, door(m), GateResubmit, Deny, CodeResubmitQuestion)
	}
	// A question mark inside a pasted link is not a question.
	got(t, door("resubmit Example Corp https://jobs.example.test/apply/42?ref=1"), GateResubmit, Allow, CodeOK)
	// A full-width spelling of the company still names it.
	got(t, ResubmitDoor(ResubmitInput{Caller: CallerAskAgent, Message: "resubmit " + fullwidthEx + " Corp", Company: "Example Corp"}), GateResubmit, Allow, CodeOK)
}

func TestGates_Correction_NegatedMessageIsNotARequest(t *testing.T) {
	door := func(msg string) CorrectionDecision {
		return CorrectionDoor(CorrectionInput{
			Caller: CallerAskAgent, SinglePosting: true, Message: msg, Submit: true, LedgerReadable: true,
			Rows: []CorrectionRow{{ID: "row1", At: t0, Company: "Example Corp"}},
		})
	}
	const link = " https://jobs.example.test/apply/42"
	got(t, door("correct the answers for Example Corp"+link).Decision, GateCorrection, Allow, CodeOK)
	for _, m := range []string{
		"Do not correct the answers for Example Corp" + link,
		"don't correct my application at Example Corp" + link,
		"never redo the answers for Example Corp" + link,
		"no need to correct the submission for Example Corp" + link,
		"stop - correct the answers for Example Corp" + link,
	} {
		got(t, door(m).Decision, GateCorrection, Deny, CodeCorrectionNegated)
	}
	got(t, door("Should I correct the answers for Example Corp?"+link).Decision, GateCorrection, Deny, CodeCorrectionQuestion)
	if d := door("do not correct the answers for Example Corp" + link); d.SupersedesRowID != "" || d.Class != ClassPosting {
		t.Errorf("negated refusal carries %+v", d)
	}
}

func TestGates_Consent_UnmatchedOptionalBoxNeedsTheUsersOwnBankEntry(t *testing.T) {
	for _, l := range []string{"I am a policyholder", "Available for midterms", "I am authorized to work in the United States", "Remote only"} {
		got(t, ConsentControl(ConsentInput{Label: l, SettingOn: true}), GateConsentControl, Deny, CodeOptionalNeedsUser)
		got(t, ConsentControl(ConsentInput{Label: l, SettingOn: true, Answer: AnswerUserBankExact}), GateConsentControl, Allow, CodeUserBankAnswer)
	}
	// The zero value attests nothing, and an unknown attestation is not one.
	got(t, ConsentControl(ConsentInput{Label: "Remote only", Answer: "model_chosen"}), GateConsentControl, Deny, CodeOptionalNeedsUser)
	// A required box that is no consent wording is not the gate's concern.
	got(t, ConsentControl(ConsentInput{Label: "I am a policyholder", PageMarksRequired: true}), GateConsentControl, Allow, CodeNotConsentControl)
}

func TestGates_Captcha_SkipStatusFailsClosedWhenUnsetOrUnknown(t *testing.T) {
	for _, s := range []SkipStatus{"", "unknown", "NOT_LISTED", SkipUnreadable} {
		got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Skip = s })), GateCaptchaSolve, Deny, CodeSkipListUnreadable)
	}
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Skip = SkipListed })), GateCaptchaSolve, Deny, CodeSkipListCompany)
	got(t, CaptchaSolve(solveOK(func(i *CaptchaInput) { i.Skip = SkipNotListed })), GateCaptchaSolve, Allow, CodeOK)
}

func TestGates_Ladder_SelfIdentificationWidenedToFreeTextAndMoreAxes(t *testing.T) {
	for _, l := range []string{
		"Describe your gender identity",
		"What is your nationality?",
		"What year were you born?",
		"Which best describes your orientation?",
		"Tell us your sexual orientation",
		"Country of citizenship",
		"What is your age?",
		"How old are you?",
		"Date of birth",
		"Describe your race",
		"Your pronouns",
		"R" + eAcute + "ligion",
	} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); !ex || code != ExcludedSelfID {
			t.Errorf("%q -> %q %v", l, code, ex)
		}
	}
	for _, l := range []string{"Marital status", "Why do you want to work here?", "Describe a project where you improved gender diversity in your team and what you learned"} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); ex {
			t.Errorf("%q excluded as %q", l, code)
		}
	}
	// Folded spellings do not slip past the vocabulary.
	if _, ex := LadderExclusion(LadderRow{Label: "G" + zeroWidth + "ender"}); !ex {
		t.Error("zero-width gender kept")
	}
}

func TestGates_Ladder_FactAllowlistDeniesUnknownKeysByDefault(t *testing.T) {
	for k, want := range map[string]bool{
		"skills": true, "skills-go": true, "years-of-go-experience": true, "degree": true, "education-level": true,
		"preferred-start-month": true, "relocation-willingness": true, "current-title": true, "timezone": true,
		"linkedin-url": false, "email-address": false, "phone-national": false, "applicant-full-name": false,
		"full-name": false, "github-url": false, "portfolio-site": false, "street-address": false,
		"work-authorization": false, "requires-sponsorship-now": false, "visa-status": false,
		"education-gender": false, "country-of-birth": false, "date_of_birth": false, "Race": false,
		"citizenship": false, "something-new": false, "": false, "fact:skills": false, "L\u0131nkedin": false,
	} {
		if got := LadderFactAllowed(k); got != want {
			t.Errorf("LadderFactAllowed(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestGates_AgentSubmit_DeniesTelegramAndScheduledChannelItself(t *testing.T) {
	const jobURL = "https://boards.example.test/acme/jobs/1"
	in := func(c Caller, tool string) AgentSubmitInput {
		return AgentSubmitInput{Caller: c, Tool: tool, PostingURL: jobURL, UserMessage: jobURL}
	}
	got(t, AgentSubmit(in(CallerAskAgent, ToolAutofillApplication)), GateAgentSubmit, Allow, CodeExactURLNamed)
	for _, tool := range []string{ToolAutofillApplication, ToolApplyFromList} {
		got(t, AgentSubmit(in(CallerTelegram, tool)), GateAgentSubmit, Deny, CodeSubmitDashboardOnly)
		got(t, AgentSubmit(in(CallerScheduler, tool)), GateAgentSubmit, Deny, CodeUnattendedCaller)
		got(t, AgentSubmit(in("", tool)), GateAgentSubmit, Deny, CodeUnattendedCaller)
		got(t, AgentSubmit(in("remote_device", tool)), GateAgentSubmit, Deny, CodeUnattendedCaller)
	}
}

func TestGates_SkipList_FullwidthAndAccentedNamesFold(t *testing.T) {
	for _, c := range []struct{ entry, company string }{
		{"\uff21\uff50\uff50\uff4c\uff45", "Apple"},
		{"Apple", "\uff21\uff50\uff50\uff4c\uff45 Inc."},
		{"Nestl" + eAcute, "Nestle"},
		{"Appl" + zeroWidth + "e", "Apple"},
	} {
		if !CoversCompany(c.entry, c.company) {
			t.Errorf("CoversCompany(%q, %q) = false", c.entry, c.company)
		}
	}
	got(t, SkipCompany(SkipInput{Company: "\uff2d\uff45\uff54\uff41", Entries: DefaultSkipList}).Decision, GateSkipList, Deny, CodeSkipListed)
	if CoversCompany("\uff2d\uff45\uff54\uff41", "Microsoft") {
		t.Error("a full-width name covers an unrelated company")
	}
}

func TestGuard_Duplicate_TrackerStatusReadsThroughMarkdownEmphasis(t *testing.T) {
	for _, s := range []string{"**Applied**", " Applied ", "_Interview_", "OFFER"} {
		if !AppliedOrLater(s) {
			t.Errorf("AppliedOrLater(%q) = false", s)
		}
	}
	for _, s := range []string{"**Evaluated**", "SKIP", "", "Applied soon"} {
		if AppliedOrLater(s) {
			t.Errorf("AppliedOrLater(%q) = true", s)
		}
	}
}

func TestGates_Controls_DataConsentPressesOnlyAnAccept(t *testing.T) {
	for _, c := range []struct {
		labels []string
		v      Verdict
		code   string
	}{
		{[]string{"I Accept"}, Allow, CodeOK},
		{[]string{"Agree and continue"}, Allow, CodeOK},
		{[]string{"I Acknowledge"}, Allow, CodeOK},
		{[]string{"I Decline"}, Deny, CodeNotNavigation},
		{[]string{"Accept all cookies"}, Deny, CodeNotNavigation},
		{[]string{"Accept and submit"}, Deny, CodeSubmitInWalk},
		{[]string{"I Accept", "Withdraw application"}, Deny, CodeWithdrawControl},
	} {
		got(t, PressControl(ControlInput{Stage: StageDataConsent, Found: true, Labels: c.labels}), GateControlPress, c.v, c.code)
	}
}

func TestGates_Controls_EditorSavePressesOnlyExactlySave(t *testing.T) {
	editor := func(labels ...string) Decision {
		return PressControl(ControlInput{Stage: StageEditorSave, Found: true, Labels: labels})
	}
	for _, c := range []struct {
		name   string
		labels []string
		v      Verdict
		code   string
	}{
		{"save", []string{"Save"}, Allow, CodeOK},
		{"label and value agree", []string{"Save", "save"}, Allow, CodeOK},
		{"save and continue is still navigation", []string{"Save and continue"}, Allow, CodeOK},
		{"the timeline add button", []string{"Add"}, Allow, CodeOK},
		{"add education", []string{"Add Education"}, Allow, CodeOK},
		{"add and delete", []string{"Add", "Delete application"}, Deny, CodeDiscardControl},
		{"save a draft is not Save", []string{"Save draft"}, Deny, CodeNotNavigation},
		{"save with a delete beside it", []string{"Save", "Delete account"}, Deny, CodeDiscardControl},
		{"submit is never an editor save", []string{"Submit application"}, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, Deny, CodeWithdrawControl},
		{"nothing at all", nil, Deny, CodeNotNavigation},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, editor(c.labels...), GateControlPress, c.v, c.code)
		})
	}
	// Plain Save is refused in the ordinary walk.
	got(t, walk("Save"), GateControlPress, Deny, CodeNotNavigation)
}

func TestGates_Controls_AccountStagePressesOnlySigninAndCreateAccountLabels(t *testing.T) {
	acct := func(labels ...string) Decision {
		return PressControl(ControlInput{Stage: StageAccount, Found: true, Labels: labels})
	}
	for _, c := range []struct {
		name   string
		labels []string
		v      Verdict
		code   string
	}{
		{"sign in", []string{"Sign in"}, Allow, CodeOK},
		{"log in with an arrow", []string{"Log in \u2192"}, Allow, CodeOK},
		{"create account", []string{"Create account"}, Allow, CodeOK},
		{"submit application", []string{"Submit application"}, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, Deny, CodeWithdrawControl},
		{"delete account", []string{"Delete account"}, Deny, CodeDiscardControl},
		{"sign in with google", []string{"Sign in with Google"}, Deny, CodeNotNavigation},
		{"sign in beside a stranger", []string{"Sign in", "Pay now"}, Deny, CodeNotNavigation},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, acct(c.labels...), GateControlPress, c.v, c.code)
		})
	}
	got(t, walk("Sign in"), GateControlPress, Deny, CodeNotNavigation)
}

func TestGates_Controls_AccountFlowStagePressesOnlyTheTermsRecoveryAndForgotLabels(t *testing.T) {
	flow := func(labels ...string) Decision {
		return PressControl(ControlInput{Stage: StageAccountFlow, Found: true, Labels: labels})
	}
	for _, c := range []struct {
		name   string
		labels []string
		v      Verdict
		code   string
	}{
		{"accept", []string{"Accept"}, Allow, CodeOK},
		{"i agree", []string{"I Agree"}, Allow, CodeOK},
		{"terms link", []string{"Terms and Conditions"}, Allow, CodeOK},
		{"privacy link", []string{"Privacy Policy"}, Allow, CodeOK},
		{"read-and-accept link under its caption (SuccessFactors)", []string{"Read and accept the data privacy statement.", "Terms of Use Read and accept the data privacy statement. Required"}, Allow, CodeOK},
		{"accept the terms and pay is not one", []string{"Accept the terms and pay"}, Deny, CodeNotNavigation},
		{"resend", []string{"Resend verification email"}, Allow, CodeOK},
		{"request", []string{"Request a new activation link"}, Allow, CodeOK},
		{"resend account verification (Workday's own tenant)", []string{"Resend Account Verification"}, Allow, CodeOK},
		{"send the passcode", []string{"Send passcode"}, Allow, CodeOK},
		{"send a code", []string{"Send code"}, Allow, CodeOK},
		{"get the code of a code-only sign-up (ByteDance)", []string{"Get code"}, Allow, CodeOK},
		{"get started is not a code", []string{"Get started"}, Deny, CodeNotNavigation},
		{"forgot", []string{"Forgot your password?"}, Allow, CodeOK},
		{"sign in with email", []string{"Sign in with email"}, Allow, CodeOK},
		{"without resume", []string{"Apply Without Resume"}, Allow, CodeOK},
		{"an upload method is not an account-flow control", []string{"Upload Resume"}, Deny, CodeNotNavigation},
		{"a third-party identity method is not one either", []string{"Sign in with LinkedIn"}, Deny, CodeNotNavigation},
		{"accept all cookies is not the dialog's accept", []string{"Accept all cookies"}, Deny, CodeNotNavigation},
		{"submit application", []string{"Submit application"}, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, Deny, CodeWithdrawControl},
		{"delete account", []string{"Delete account"}, Deny, CodeDiscardControl},
		{"a stranger beside an accept", []string{"Accept", "Pay now"}, Deny, CodeNotNavigation},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, flow(c.labels...), GateControlPress, c.v, c.code)
		})
	}
	// The other stages never press these.
	got(t, walk("Resend verification email"), GateControlPress, Deny, CodeNotNavigation)
	got(t, PressControl(ControlInput{Stage: StageAccount, Found: true, Labels: []string{"Forgot your password?"}}), GateControlPress, Deny, CodeNotNavigation)
}

// The computer-use rung (F-9) presses the controls of the form it writes and
// nothing else: the walk keeps its navigation, the submit stage its Submit.
func TestGates_Controls_ComputerUseStagePressesOnlyTheFormItWrites(t *testing.T) {
	for _, c := range []struct {
		name            string
		labels          []string
		submitter, link bool
		v               Verdict
		code            string
	}{
		{"an option", []string{"Yes"}, false, false, Allow, CodeOK},
		{"a listbox showing its prompt", []string{"Select...", "Work authorization"}, false, false, Allow, CodeOK},
		{"an option holding a Submit button", []string{"No Submit", "No"}, false, false, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, false, false, Deny, CodeWithdrawControl},
		{"delete the application", []string{"Delete application"}, false, false, Deny, CodeDiscardControl},
		{"a destructive label", []string{"Remove"}, false, false, Deny, CodeDestructiveLabel},
		{"pay", []string{"Pay now"}, false, false, Deny, CodeDestructiveLabel},
		{"the walk's next", []string{"Next"}, false, false, Deny, CodeNavigationInRung},
		{"a button that submits its form", []string{"Yes"}, true, false, Deny, CodeSubmitInWalk},
		{"an option that is a link out of the page", []string{"LinkedIn"}, false, true, Deny, CodeNavigationInRung},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, PressControl(ControlInput{Stage: StageComputerUse, Found: true, Labels: c.labels, FormSubmitter: c.submitter, Link: c.link}), GateControlPress, c.v, c.code)
		})
	}
}

func TestGates_Controls_ResetStagePressesOnlyTheResetFormButtons(t *testing.T) {
	reset := func(labels ...string) Decision {
		return PressControl(ControlInput{Stage: StageReset, Found: true, Labels: labels})
	}
	for _, c := range []struct {
		name   string
		labels []string
		v      Verdict
		code   string
	}{
		{"submit", []string{"Submit"}, Allow, CodeOK},
		{"reset password", []string{"Reset Password"}, Allow, CodeOK},
		{"send reset link", []string{"Send reset link"}, Allow, CodeOK},
		{"save", []string{"Save"}, Allow, CodeOK},
		{"submit application is never a reset button", []string{"Submit application"}, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, Deny, CodeWithdrawControl},
		{"delete account", []string{"Delete account"}, Deny, CodeDiscardControl},
		{"a stranger", []string{"Pay now"}, Deny, CodeNotNavigation},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, reset(c.labels...), GateControlPress, c.v, c.code)
		})
	}
	got(t, walk("Submit"), GateControlPress, Deny, CodeSubmitInWalk)
}

func TestGates_Controls_SigninDialogStagePressesOnlyTheDialogsOwnSteps(t *testing.T) {
	for _, c := range []struct {
		name     string
		labels   []string
		inDialog bool
		v        Verdict
		code     string
	}{
		{"the review's Submit", []string{"Submit"}, true, Allow, CodeOK},
		{"the code step's Verify", []string{"Verify"}, true, Allow, CodeOK},
		{"the email step's Continue", []string{"Continue"}, true, Allow, CodeOK},
		{"a Submit outside a dialog", []string{"Submit"}, false, Deny, CodeSubmitInWalk},
		{"submit application is never a dialog step", []string{"Submit application"}, true, Deny, CodeSubmitInWalk},
		{"a lookalike Submit", []string{cyrillicS + "ubmit"}, true, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, true, Deny, CodeWithdrawControl},
		{"a stranger", []string{"Pay now"}, true, Deny, CodeNotNavigation},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, PressControl(ControlInput{Stage: StageSigninDialog, Found: true, Labels: c.labels, InDialog: c.inDialog}), GateControlPress, c.v, c.code)
		})
	}
	// Every other stage refuses the dialog's Submit.
	got(t, PressControl(ControlInput{Stage: StageAccount, Found: true, Labels: []string{"Submit"}, InDialog: true}), GateControlPress, Deny, CodeSubmitInWalk)
}

// The profile stage presses only a dialog's plain Submit (Eightfold's "Create
// your profile"); the caller has read the dialog as identity boxes only.
func TestGates_Controls_ProfileDialogStagePressesOnlyTheDialogsSubmit(t *testing.T) {
	for _, c := range []struct {
		name     string
		labels   []string
		inDialog bool
		v        Verdict
		code     string
	}{
		{"the dialog's Submit", []string{"Submit"}, true, Allow, CodeOK},
		{"a Submit outside a dialog", []string{"Submit"}, false, Deny, CodeSubmitInWalk},
		{"submit application is never a profile step", []string{"Submit application"}, true, Deny, CodeSubmitInWalk},
		{"a lookalike Submit", []string{cyrillicS + "ubmit"}, true, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, true, Deny, CodeWithdrawControl},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, PressControl(ControlInput{Stage: StageProfileDialog, Found: true, Labels: c.labels, InDialog: c.inDialog}), GateControlPress, c.v, c.code)
		})
	}
}

func TestGates_Controls_ChallengeStagePressesOnlyThePlainButtonOfAChallengePage(t *testing.T) {
	chal := func(labels ...string) Decision {
		return PressControl(ControlInput{Stage: StageChallenge, Found: true, Labels: labels})
	}
	for _, c := range []struct {
		name   string
		labels []string
		v      Verdict
		code   string
	}{
		{"continue", []string{"Continue"}, Allow, CodeOK},
		{"verify you are human", []string{"Verify you are human"}, Allow, CodeOK},
		{"the application's submit stays refused", []string{"Submit application"}, Deny, CodeSubmitNotAuthorized},
		{"send application", []string{"Send my application"}, Deny, CodeSubmitNotAuthorized},
		{"withdraw", []string{"Withdraw application"}, Deny, CodeWithdrawControl},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, chal(c.labels...), GateControlPress, c.v, c.code)
		})
	}
	got(t, walk("Submit"), GateControlPress, Deny, CodeSubmitInWalk)
}

// A plain "Submit" on a challenge page can be the application's own final
// Submit: the challenge stage presses it only with the submit authorization and
// the press claim, which the walk never holds.
func TestGates_Controls_ChallengeStageNeverPressesASubmitWithoutAuthorizationAndClaim(t *testing.T) {
	chal := func(authorized, claim bool) Decision {
		return PressControl(ControlInput{Stage: StageChallenge, Found: true, Labels: []string{"Submit"}, SubmitAuthorized: authorized, ClaimHeld: claim})
	}
	got(t, chal(false, false), GateControlPress, Deny, CodeSubmitNotAuthorized)
	got(t, chal(false, true), GateControlPress, Deny, CodeSubmitNotAuthorized)
	got(t, chal(true, false), GateControlPress, Deny, CodeSubmitClaimMissing)
	got(t, chal(true, true), GateControlPress, Allow, CodeOK)
}

func TestGates_Controls_BotManagerHostsAreMatchedByEqualityOrSubdomainOnly(t *testing.T) {
	for host, want := range map[string]bool{
		"validate.perfdrive.com": true, "perfdrive.com": true, "a.validate.perfdrive.com": true, "VALIDATE.PERFDRIVE.COM": true,
		"perfdrive.com.evil.example": false, "evilperfdrive.com": false, "validate.perfdrive.com.example.test": false,
		"careers.example.test": false, "": false,
	} {
		if got := BotManagerHost(host); got != want {
			t.Errorf("BotManagerHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// A bot-manager validation page cannot be an application, so the challenge
// stage presses its plain Submit without the submit authorization; on any
// other host the same press still needs the authorization and the claim.
func TestGates_Controls_ChallengeStagePressesTheSubmitOfAListedBotManagerPageOnly(t *testing.T) {
	chal := func(host string, labels ...string) Decision {
		return PressControl(ControlInput{Stage: StageChallenge, Found: true, Labels: labels, TopHost: host})
	}
	d := chal("validate.perfdrive.com", "Submit")
	got(t, d, GateControlPress, Allow, CodeOK)
	if !strings.Contains(d.Reason, "validate.perfdrive.com") {
		t.Fatalf("reason %q does not name the host", d.Reason)
	}
	got(t, chal("validate.perfdrive.com", "Submit application"), GateControlPress, Deny, CodeSubmitNotAuthorized)
	got(t, chal("perfdrive.com.evil.example", "Submit"), GateControlPress, Deny, CodeSubmitNotAuthorized)
	got(t, chal("careers.example.test", "Submit"), GateControlPress, Deny, CodeSubmitNotAuthorized)
	got(t, PressControl(ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Submit"}, TopHost: "validate.perfdrive.com"}), GateControlPress, Deny, CodeSubmitInWalk)
}

// A control inside an embedded frame of a listed bot manager's page is not the
// bot manager's own button, so the exception does not cover it.
func TestGates_Controls_BotManagerExceptionNeverCoversAFrameControl(t *testing.T) {
	d := PressControl(ControlInput{Stage: StageChallenge, Found: true, Labels: []string{"Submit"}, TopHost: "validate.perfdrive.com", InFrame: true})
	got(t, d, GateControlPress, Deny, CodeSubmitNotAuthorized)
}

func TestGoogleSignInPressRequiresSettingAndExactLabel(t *testing.T) {
	for _, label := range []string{"Sign in with Google", "Continue with Google", "Allow", "Sign up with Google", "Register with Google", "Submit application", "Google", "Continue with Google and submit"} {
		for _, enabled := range []bool{false, true} {
			d := PressControl(ControlInput{Stage: StageGoogleSignIn, Found: true, Labels: []string{label}, AllowGoogleSignIn: enabled})
			want := enabled && (label == "Sign in with Google" || label == "Continue with Google" || label == "Sign up with Google" || label == "Register with Google")
			if d.Allowed() != want {
				t.Fatalf("label %q enabled %v: %+v", label, enabled, d)
			}
		}
	}
}

func TestGoogleProviderPressNeedsVerifiedGoogleContext(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, verified := range []bool{false, true} {
			for _, host := range []string{"accounts.google.com", "evil.test"} {
				d := PressControl(ControlInput{Stage: StageGoogleProvider, Found: true, Labels: []string{"Continue"}, TopHost: host, AllowGoogleSignIn: enabled, GoogleProviderControl: verified})
				if d.Allowed() != (enabled && verified && host == "accounts.google.com") {
					t.Fatalf("%+v", d)
				}
			}
		}
	}
}

func TestGates_Controls_WalkPressesATabOnlyWhenItNamesTheApplication(t *testing.T) {
	tab := func(stage ControlStage, labels ...string) Decision {
		return PressControl(ControlInput{Stage: stage, Found: true, Labels: labels, Tab: true})
	}
	for _, c := range []struct {
		name   string
		labels []string
		v      Verdict
		code   string
	}{
		{"application", []string{"Application"}, Allow, CodeOK},
		{"application form", []string{"Application Form"}, Allow, CodeOK},
		{"another tab", []string{"Role overview"}, Deny, CodeNotNavigation},
		{"a submit that names the application", []string{"Submit application"}, Deny, CodeSubmitInWalk},
		{"withdraw", []string{"Withdraw application"}, Deny, CodeWithdrawControl},
		{"nothing at all", nil, Deny, CodeNotNavigation},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, tab(StageWalk, c.labels...), GateControlPress, c.v, c.code)
		})
	}
	// The same label is no navigation on a control that is not a tab, nor in
	// a stage other than the walk.
	got(t, walk("Application"), GateControlPress, Deny, CodeNotNavigation)
	got(t, tab(StageDataConsent, "Application"), GateControlPress, Deny, CodeNotNavigation)
}
