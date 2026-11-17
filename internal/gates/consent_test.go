// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import "testing"

func TestGates_Legal_RequiredDeclarationsFollowSetting(t *testing.T) {
	labels := []string{
		"I certify that the information is true",
		"I have read and agree to the Privacy Policy",
		"I accept the Terms and Conditions",
		"I authorize a background check",
		"I understand that employment is at-will",
		"I consent to the use of an electronic signature",
		"I acknowledge the arbitration agreement",
	}
	for _, l := range labels {
		t.Run(l, func(t *testing.T) {
			got(t, ConsentControl(ConsentInput{Label: l, PageMarksRequired: true, SettingOn: true}), GateConsentControl, Allow, CodeLegalDeclarationOn)
			got(t, ConsentControl(ConsentInput{Label: l, PageMarksRequired: true, SettingOn: false}), GateConsentControl, Deny, CodeLegalDeclarationOff)
			// An optional acknowledgement is never ticked, either way.
			got(t, ConsentControl(ConsentInput{Label: l, SettingOn: true}), GateConsentControl, Deny, CodeMarketingOptIn)
		})
	}
	// Whole words only.
	for _, l := range []string{"Policyholder number", "Midterms completed", "Preferred name"} {
		got(t, ConsentControl(ConsentInput{Label: l, PageMarksRequired: true, SettingOn: false}), GateConsentControl, Allow, CodeNotConsentControl)
	}
}

func TestGates_Marketing_OptionalOptInNeverTicked(t *testing.T) {
	for _, l := range []string{
		"Send me job alerts", "Subscribe to our newsletter", "Receive promotional offers", "Text me via SMS",
		"Join our talent community", "Share my profile with portfolio companies", "Join the mailing list",
		"Contact me about future opportunities", "Keep me informed about other roles", "Receive exclusive partner offers",
		"Notify me of other openings", "Consider me for future positions",
		"Sign me up for emails from Example Corp", "Notify me when similar jobs open", "Keep me posted about new openings",
		"Keep me informed", "I'd like to hear about new roles", "Join our talent network", "Email me about future opportunities",
		"Receive updates", "Marketing communications", "Promotional offers", "Emails from Example Corp",
	} {
		t.Run(l, func(t *testing.T) {
			for _, setting := range []bool{true, false} {
				got(t, ConsentControl(ConsentInput{Label: l, SettingOn: setting}), GateConsentControl, Deny, CodeMarketingOptIn)
			}
		})
	}
}

func TestGates_Marketing_RequiredWordingFollowsSetting(t *testing.T) {
	l := "I allow Example Corp to contact me about future opportunities"
	got(t, ConsentControl(ConsentInput{Label: l, PageMarksRequired: true, SettingOn: true}), GateConsentControl, Allow, CodeLegalDeclarationOn)
	got(t, ConsentControl(ConsentInput{Label: l, PageMarksRequired: true, SettingOn: false}), GateConsentControl, Deny, CodeLegalDeclarationOff)
	// A truth certification that merely mentions the future is a declaration.
	cert := "I certify my answers are true; false answers could disqualify me from future opportunities"
	got(t, ConsentControl(ConsentInput{Label: cert, PageMarksRequired: true, SettingOn: true}), GateConsentControl, Allow, CodeLegalDeclarationOn)
	got(t, ConsentControl(ConsentInput{Label: cert, PageMarksRequired: true, SettingOn: false}), GateConsentControl, Deny, CodeLegalDeclarationOff)
}

func TestGates_Marketing_LabelSaysOptionalOverridesFlag(t *testing.T) {
	d := ConsentControl(ConsentInput{Label: "(optional) keep me in the talent network", PageMarksRequired: true, SettingOn: true})
	got(t, d, GateConsentControl, Deny, CodeMarketingOptIn)
}

func TestGates_Legal_BankFixedAnswerAppliesWhateverTheSetting(t *testing.T) {
	for _, setting := range []bool{true, false} {
		d := ConsentControl(ConsentInput{Label: "I certify that the information is true", PageMarksRequired: true, SettingOn: setting, Answer: AnswerUserBankExact})
		got(t, d, GateConsentControl, Allow, CodeUserBankAnswer)
	}
	got(t, ConsentControl(ConsentInput{Label: "Send me job alerts", Answer: AnswerUserBankExact}), GateConsentControl, Allow, CodeUserBankAnswer)
}

func TestGates_Legal_SignupAgreementWithSettingOffRefuses(t *testing.T) {
	d := ConsentControl(ConsentInput{Label: "I agree to the Terms of Service", PageMarksRequired: true, Surface: SurfaceSignup})
	got(t, d, GateConsentControl, Deny, CodeLegalDeclarationOff)
	if d.Class != ClassCredential {
		t.Fatalf("class = %q", d.Class)
	}
	// The account creation gate refuses before any password is saved.
	a := AccountCreation(acctAllowed(func(i *AccountInput) { i.RequiredAgreementOnForm = true }))
	got(t, a, GateAccountCreation, Deny, CodeAgreementNeedsSetting)
}

func TestGates_Legal_AccountDetailsOffLeavesBox(t *testing.T) {
	d := ConsentControl(ConsentInput{Label: "I have read and agree (Declaration of Applicant, background check, at-will)", PageMarksRequired: true, Surface: SurfaceAccountDetails})
	got(t, d, GateConsentControl, Deny, CodeLegalDeclarationOff)
	if d.Class != "" {
		t.Fatalf("class = %q", d.Class)
	}
}

func TestGates_Statement_ObservationOnlyElseUserEntryElseBlank(t *testing.T) {
	for _, c := range []struct {
		name  string
		label string
		in    StatementInput
		v     Verdict
		code  string
		ans   string
	}{
		{"cover letter attached by this run", "Did you submit a cover letter?", StatementInput{Observed: true}, Allow, CodeOK, "Yes"},
		{"nothing observed", "Did you submit a cover letter?", StatementInput{}, Deny, CodeApplicationStatement, ""},
		{"observable never from the bank", "Did you attach a resume?", StatementInput{UserEntry: "Yes", HasUserEntry: true}, Deny, CodeApplicationStatement, ""},
		{"referral from the user's entry", "Were you referred by an employee?", StatementInput{UserEntry: "No", HasUserEntry: true}, Allow, CodeOK, "No"},
		{"AI use from the user's entry", "Did you use AI to help complete this application?", StatementInput{UserEntry: "No", HasUserEntry: true}, Allow, CodeOK, "No"},
		{"AI use without an entry", "Did you use AI to help complete this application?", StatementInput{}, Deny, CodeApplicationStatement, ""},
		{"observed does not answer a referral", "Were you referred?", StatementInput{Observed: true}, Deny, CodeApplicationStatement, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.in.Kind = ClassifyStatement(c.label)
			if c.in.Kind == StatementNone {
				t.Fatalf("%q is not classified as a statement", c.label)
			}
			r := AnswerStatement(c.in)
			got(t, r.Decision, GateStatement, c.v, c.code)
			if r.Answer != c.ans {
				t.Fatalf("answer = %q", r.Answer)
			}
		})
	}
	if ClassifyStatement("Do you have experience with Go?") != StatementNone {
		t.Fatal("an ordinary question is not a statement")
	}
	// LiveKit (Ashby, live): a request not to use AI on the application,
	// acknowledged with a Yes, is the applicant's statement; the company's own
	// use of AI is not.
	for label, want := range map[string]bool{
		"At LiveKit, we embrace the thoughtful use of AI tools to move faster and work smarter. However, for the application form and during interviews, we ask that you don’t use AI assistance. This allows us to get a clear sense of your genuine interest in LiveKit and how you communicate in your own voice. Please select \"yes\" below to acknowledge.": true,
		"Please do not use ChatGPT or other AI tools to complete this application.":                   true,
		"We do not use AI to screen your application. Do you consent to the processing of your data?": false,
		"Do you have experience building AI tools?":                                                   false,
	} {
		if got := ClassifyStatement(label) == StatementOther; got != want {
			t.Errorf("%q: statement %t, want %t", label, got, want)
		}
	}
	if code, ex := LadderExclusion(LadderRow{Label: "Did you submit a cover letter?"}); !ex || code != CodeApplicationStatement {
		t.Fatalf("ladder: %q %v", code, ex)
	}
	if len(SelectReviewRows([]ReviewRow{{Value: "Yes", WrittenByRun: true, ApplicationStatement: true}})) != 0 {
		t.Fatal("the review must not receive a statement row")
	}
}

func TestGates_Consent_LeastConsentCloseNeverAcceptAllFirst(t *testing.T) {
	for _, c := range []struct {
		name string
		in   CookieInput
		v    Verdict
		code string
		want int
	}{
		{"banner offers reject and accept", CookieInput{Controls: []string{"Accept all", "Reject all"}}, Allow, CodeOK, 1},
		{"accept listed last", CookieInput{Controls: []string{"Reject all", "Accept all"}}, Allow, CodeOK, 0},
		{"necessary only", CookieInput{Controls: []string{"Accept all cookies", "Necessary only"}}, Allow, CodeOK, 1},
		{"preference center saves untouched defaults", CookieInput{Controls: []string{"Select All", "Unselect All", "Save Changes"}, PreferenceCenter: true, AllOptionalOff: true}, Allow, CodeOK, 2},
		{"save is not least consent when a category is on", CookieInput{Controls: []string{"Select All", "Unselect All", "Save Changes"}, PreferenceCenter: true}, Deny, CodeNoClosingControl, -1},
		{"close button", CookieInput{Controls: []string{"Accept", "Close"}}, Allow, CodeOK, 1},
		{"x button", CookieInput{Controls: []string{"x"}}, Allow, CodeOK, 0},
		{"accept all is the last resort", CookieInput{Controls: []string{"Accept all"}}, Allow, CodeAcceptAllLastResort, 0},
		{"only toggles", CookieInput{Controls: []string{"Select All", "Unselect All"}, PreferenceCenter: true, AllOptionalOff: true}, Deny, CodeNoClosingControl, -1},
		{"dialog asking for a signature", CookieInput{Controls: []string{"Reject all", "Sign"}, AsksAboutApplicant: true}, Deny, CodeNotConsentGate, -1},
		{"disagree is a reject", CookieInput{Controls: []string{"I agree", "I disagree"}}, Allow, CodeOK, 1},
		// Toast: a negated accept is a reject, never an accept-all.
		{"do not accept is a reject", CookieInput{Controls: []string{"Manage cookies", "I do not accept", "I accept"}}, Allow, CodeOK, 1},
		{"don't allow is a reject", CookieInput{Controls: []string{"Allow all", "Don't allow"}}, Allow, CodeOK, 1},
		{"do not agree is a reject", CookieInput{Controls: []string{"I agree", "I do not agree"}}, Allow, CodeOK, 1},
		{"no controls", CookieInput{}, Deny, CodeNoClosingControl, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := CloseConsentGate(c.in)
			got(t, r.Decision, GateCookieConsent, c.v, c.code)
			if r.Press != c.want {
				t.Fatalf("press = %d", r.Press)
			}
		})
	}
}

func TestGates_Legal_ASettingChangeIsAuditable(t *testing.T) {
	got(t, LegalSettingChange(true), GateLegalSetting, Allow, CodeLegalSettingOn)
	got(t, LegalSettingChange(false), GateLegalSetting, Allow, CodeLegalSettingOff)
	if !LegalSettingChange(true).Valid() || !LegalSettingChange(false).Valid() {
		t.Fatal("a setting change is a well formed decision")
	}
}

func TestGates_CLIInstall_ChangesAndRefusalsAreAuditable(t *testing.T) {
	got(t, CLIInstallChange(true), GateCLIInstall, Allow, CodeCLILinkCreated)
	got(t, CLIInstallChange(false), GateCLIInstall, Allow, CodeCLILinkRemoved)
	got(t, CLIInstallRefused(), GateCLIInstall, Deny, CodeCLILinkForeign)
}
