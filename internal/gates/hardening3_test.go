// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import "testing"

// u builds a letter from its code point, so the source stays ASCII even after
// gofmt rewrites escapes in string literals.
func u(cp rune) string { return string(cp) }

var (
	armenianAyb  = u(0x561) // looks like a
	armenianSeh  = u(0x57d) // looks like u
	armenianOh   = u(0x585) // looks like o
	greekOmicron = u(0x3bf)
	cherokeeUnk  = u(0x13ce) // a Cherokee letter the fold does not map
	hebrewMem    = u(0x5de)  // a Hebrew letter the fold does not map
	cyrillicI    = u(0x456)
	cyrillicDze  = u(0x455)
	thaiWord     = u(0xe44) + u(0xe21) + u(0xe48)
)

func TestGates_Ladder_WorkAuthorizationFamilyNeverReachesTheModel(t *testing.T) {
	for _, l := range []string{
		"Are you legally permitted to work in the United States?",
		"Can you legally work in the US?",
		"Are you allowed to work in the US?",
		"Are you authorised to be employed in the UK?",
		"Do you have the right to live and work in the UK?",
		"Are you authorized to work in the country in which this job is located?",
		"Do you have the right to work in Canada?",
		"Will you now or in the future require sponsorship?",
		"What is your current work permit type?",
		"Immigration status",
		"Do you hold a green card?",
		"Employment eligibility",
	} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); !ex || code != ExcludedWorkAuth {
			t.Errorf("%q -> %q %v", l, code, ex)
		}
	}
}

func TestGates_Ladder_JobSourceWithAVeteranChannelIsNoSelfIdentification(t *testing.T) {
	opts := []string{"Career Fair / Professional Organization Event", "Internet Job Board / Job Search", "Veteran Partner"}
	if code, ex := LadderExclusion(LadderRow{Label: "How Did You Hear About Us?", Options: opts}); ex {
		t.Fatalf("a job-source question was excluded as %s", code)
	}
	if _, ex := LadderExclusion(LadderRow{Label: "Status", Options: []string{"Protected veteran", "Not a protected veteran"}}); !ex {
		t.Fatal("a veteran-status question reached the ladder")
	}
}

func TestGates_Ladder_AgeAndBirthDateAreSelfIdentification(t *testing.T) {
	for _, l := range []string{"DOB (MM/DD/YYYY)", "Date of birth", "Are you over 18 (years old)?", "Are you over 18?", "Are you 18 or older?", "Are you at least 21 years of age?"} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); !ex || code != ExcludedSelfID {
			t.Errorf("%q -> %q %v", l, code, ex)
		}
	}
	for _, l := range []string{"Do you have at least 18 years of experience?", "Why do you want to work here?", "How many years of work experience do you have with Go?", "Describe the work you did in your last role"} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); ex {
			t.Errorf("%q excluded as %q", l, code)
		}
	}
}

func TestGates_Ladder_WorkFactKeysAreSafeOnlyInTheirExplicitFamilies(t *testing.T) {
	for k, want := range map[string]bool{
		"work_experience_years": true, "work-history": true, "work_experience": true,
		"work": false, "work_permit_type": false, "work_eligibility": false, "work_sponsor_required": false,
		"work_status_us": false, "work_authorization": false, "work_visa": false, "work_authorised_uk": false,
		"experience_citizenship_notes": false, "skills_immigration": false, "years_status": false,
		"work_experience_visa": false,
	} {
		if got := LadderFactAllowed(k); got != want {
			t.Errorf("LadderFactAllowed(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestGates_Consent_SelfIdentificationCheckboxIsNeverTicked(t *testing.T) {
	for _, l := range []string{
		"I identify as a protected veteran",
		"I am a veteran",
		"I have a disability",
		"I identify as female",
		"I am Hispanic or Latino",
		"My race is Asian",
		"I identify as LGBTQ",
		"My pronouns are she/her",
		"I am over the age of 40",
		"I identify as one or more of the following classifications of protected veteran: disabled veteran, recently separated veteran, active duty wartime or campaign badge veteran, or Armed Forces service medal veteran",
	} {
		for _, req := range []bool{false, true} {
			for _, setting := range []bool{false, true} {
				for _, ans := range []AnswerProvenance{"", AnswerUserBankExact} {
					in := ConsentInput{Label: l, PageMarksRequired: req, SettingOn: setting, Answer: ans}
					got(t, ConsentControl(in), GateConsentControl, Deny, CodeSelfIDCheckbox)
				}
			}
		}
	}
	// Folded spellings read the same.
	got(t, ConsentControl(ConsentInput{Label: "I am a v" + zeroWidth + "eteran", PageMarksRequired: true, SettingOn: true}), GateConsentControl, Deny, CodeSelfIDCheckbox)
	// A consent box that names no such axis is unaffected.
	got(t, ConsentControl(ConsentInput{Label: "I certify the information is true", PageMarksRequired: true, SettingOn: true}), GateConsentControl, Allow, CodeLegalDeclarationOn)
}

func TestGates_Consent_SelfIdentificationUsesTheLaddersDetector(t *testing.T) {
	// The one vocabulary behind both gates: whatever it reads as a category
	// word, the ladder excludes and the checkbox gate refuses.
	for _, l := range []string{"Gender", "Veteran status", "Pronouns", "Date of birth", "Disability status", "Sexual orientation"} {
		if _, ex := LadderExclusion(LadderRow{Label: l}); !ex {
			t.Errorf("ladder keeps %q", l)
		}
		if d := ConsentControl(ConsentInput{Label: l, PageMarksRequired: true}); d.Code != CodeSelfIDCheckbox {
			t.Errorf("consent gate %q -> %s", l, d.Code)
		}
	}
}

func TestGates_Fold_ArmenianCherokeeAndGreekLookalikesMapToLatin(t *testing.T) {
	for in, want := range map[string]string{
		"Withdr" + armenianAyb + "w":                        "withdraw",
		"Contin" + armenianSeh + "e":                        "continue",
		"C" + armenianOh + "ntinue":                         "continue",
		"C" + greekOmicron + "ntinue":                       "continue",
		u(0x13aa) + u(0x13e2) + u(0x13e2) + u(0x13de) + "y": "apply",
		u(0x3b7) + u(0x3b5) + "xt":                          "next",
		u(0x251) + u(0x261) + "ree":                         "agree",
		"Sub" + u(0x3bc) + "it":                             "subuit",
	} {
		if got := foldText(in); got != want {
			t.Errorf("foldText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGates_Controls_ArmenianSpelledWithdrawIsStillWithdraw(t *testing.T) {
	for _, stage := range []ControlStage{StageWalk, StageSubmit, ""} {
		for _, labels := range [][]string{
			{"Submit", "Withdr" + armenianAyb + "w"},
			{"Continue", "Withdr" + armenianAyb + "w"},
			{"Submit", "W" + cyrillicI + "thdraw"},
		} {
			in := ControlInput{Stage: stage, Found: true, Labels: labels, SubmitAuthorized: true, ClaimHeld: true}
			got(t, PressControl(in), GateControlPress, Deny, CodeWithdrawControl)
		}
	}
}

func TestGates_Controls_MixedScriptWordDeniesThePressInEveryStage(t *testing.T) {
	for _, labels := range [][]string{
		{"C" + armenianOh + "ntinue"},
		{"Submit" + armenianOh},
		{"Contin" + cherokeeUnk + "e"},
		{"N" + hebrewMem + "xt"},
		{"Next", "Contin" + cherokeeUnk + "e"},
		{"Review", "Rev" + greekOmicron + "ew"},
		{cyrillicDze + armenianSeh + "bm" + u(0x456) + "t"},
		{"Apply n" + cherokeeUnk + "w"},
	} {
		for _, stage := range []ControlStage{StageWalk, StageSubmit, ""} {
			for _, auth := range []bool{false, true} {
				in := ControlInput{Stage: stage, Found: true, Labels: labels, SubmitAuthorized: auth, ClaimHeld: auth}
				d := PressControl(in)
				record(d)
				if d.Verdict != Deny {
					t.Errorf("stage %q labels %q authorized=%v -> %s/%s", stage, labels, auth, d.Verdict, d.Code)
				}
			}
		}
	}
	got(t, walk("C"+armenianOh+"ntinue"), GateControlPress, Deny, CodeMixedScriptLabel)
	// A word in one alphabet, however unfamiliar, is not mixed.
	if hasMixedScriptWord("Next step: " + thaiWord + " Continue") {
		t.Error("separate words in separate alphabets read as mixed")
	}
}

func TestGates_Controls_ReviewApplicationIsNavigationButReviewAndSubmitIsNot(t *testing.T) {
	got(t, walk("Review application"), GateControlPress, Allow, CodeOK)
	got(t, walk("Review your application"), GateControlPress, Allow, CodeOK)
	got(t, walk("Review and submit"), GateControlPress, Deny, CodeSubmitInWalk)
	got(t, walk("Review and submit application"), GateControlPress, Deny, CodeSubmitInWalk)
}

// Suno (Ashby, queue161): a policy that its company may "update" is no
// opt-in to updates, nor is a question on looking for "a new role" (Databento).
// The opt-in wording is still refused.
func TestGates_Ladder_UpdatingAPolicyOrANewRoleIsNoMarketingOptIn(t *testing.T) {
	for _, l := range []string{
		"Suno fosters an in-office culture with reasonable flexibility. We expect teammates to work from one of our offices Monday through Friday. Suno reserves the right to update these expectations as needed.",
		"In a few words, why are you looking for a new role?",
		"Have you updated your resume?",
	} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); ex && code == ExcludedMarketingOptIn {
			t.Errorf("%q was excluded as a marketing opt-in", l)
		}
	}
	for _, l := range []string{"Receive updates", "Send me updates about new roles", "Notify me of new openings", "Subscribe to product updates", "Get job alerts for new jobs", "Updates from Example Corp"} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); !ex || code != ExcludedMarketingOptIn {
			t.Errorf("%q -> %q %v", l, code, ex)
		}
	}
}
