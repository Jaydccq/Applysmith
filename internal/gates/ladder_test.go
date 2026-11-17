// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import "testing"

func TestGates_Ladder_ExclusionTableInputFilterNotPrompt(t *testing.T) {
	for _, c := range []struct {
		name string
		row  LadderRow
		code string
	}{
		{"identity bound", LadderRow{Label: "First name", IdentityBound: true}, ExcludedIdentity},
		{"marketing opt-in", LadderRow{Label: "Send me job alerts"}, ExcludedMarketingOptIn},
		{"hear-about opt-in", LadderRow{Label: "I'd like to hear about future opportunities"}, ExcludedMarketingOptIn},
		{"legal declaration, setting off", LadderRow{Label: "I certify the information is true"}, ExcludedLegalDeclaration},
		// Greenlight (Lever): the applicant confirms what they provided is accurate.
		{"accuracy confirmed", LadderRow{Label: "Do you confirm that the information provided regarding your employment history, education, and qualifications is accurate and complete? Misrepresentation may result in withdrawal from hiring process."}, ExcludedLegalDeclaration},
		{"work history entry row", LadderRow{Label: "Company name", EntryRow: true}, ExcludedWorkHistory},
		{"short date label under an entry heading", LadderRow{Label: "From", UnderEntryHeading: true}, ExcludedWorkHistory},
		{"signature", LadderRow{Label: "Signature"}, ExcludedSignature},
		{"initials", LadderRow{Label: "Enter your initials"}, ExcludedSignature},
		{"statement about the application", LadderRow{Label: "Were you referred by an employee?"}, CodeApplicationStatement},
		{"work authorization", LadderRow{Label: "Are you legally authorized to work in the country?"}, ExcludedWorkAuth},
		{"sponsorship inflected", LadderRow{Label: "Will you now or in the future require sponsorship?"}, ExcludedWorkAuth},
		// n8n (Ashby, owner workspace): the label names no work authorization;
		// every option states a visa need beside a place.
		{"options that state a visa need", LadderRow{Label: "Tick all options relevant for the role that apply to you regarding the main location of your work contract",
			Options: []string{"I can be based in the UK and do not need visa support", "I will be based in the US and need visa support"}}, ExcludedWorkAuth},
		{"gender", LadderRow{Label: "Gender"}, ExcludedSelfID},
		{"disability", LadderRow{Label: "Do you have a disability?"}, ExcludedSelfID},
		{"veteran", LadderRow{Label: "Veteran status"}, ExcludedSelfID},
		{"bare legend with race options", LadderRow{Label: "", Legend: "Please select", Options: []string{"Hispanic or Latino", "Not Hispanic or Latino"}}, ExcludedSelfID},
		{"gender options with a neutral label", LadderRow{Label: "Select one", Options: []string{"Man", "Woman", "Prefer not to say"}}, ExcludedSelfID},
		{"veteran option", LadderRow{Label: "Select one", Options: []string{"I am a protected veteran", "I am not"}}, ExcludedSelfID},
		{"task at a link", LadderRow{Label: "Complete the assessment at https://tests.example.test/start"}, ExcludedTaskAtLink},
		{"a task after the question", LadderRow{Label: "Why this role? Complete the assessment at https://tests.example.test/start"}, ExcludedTaskAtLink},
		{"criminal history explanation", LadderRow{Label: "If yes, please explain the conviction"}, ExcludedCriminalHistory},
		{"third party contact details", LadderRow{Label: "Reference's email address"}, ExcludedThirdParty},
		{"a referrer's name or email (Adobe)", LadderRow{Label: "What's their name or email address?"}, ExcludedThirdParty},
		{"trap field", LadderRow{Label: "If you are a human, leave this field blank"}, ExcludedTrapField},
		{"follow-up detail slot without a parent answer", LadderRow{Label: "Please provide details", FollowUpDetailNoAnswer: true}, ExcludedFollowUpDetail},
		{"label is the control's own prompt", LadderRow{Label: "Enter text here", Placeholder: "enter text here"}, ExcludedPromptOnly},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, ex := LadderExclusion(c.row)
			if !ex || code != c.code {
				t.Fatalf("got %q %v, want %q", code, ex, c.code)
			}
		})
	}
	for _, c := range []struct {
		name string
		row  LadderRow
	}{
		{"declaration with the setting on", LadderRow{Label: "I certify the information is true", LegalSettingOn: true}},
		{"ordinary question", LadderRow{Label: "Why do you want to work here?"}},
		{"long question mentioning a category word", LadderRow{Label: "Describe a project where you improved gender diversity in your team and what you learned from it afterwards"}},
		{"experience question", LadderRow{Label: "How many years of Go experience do you have?"}},
		{"job-source question", LadderRow{Label: "How did you first hear about this opportunity?"}},
		{"country list with Isle of Man", LadderRow{Label: "Country", Options: []string{"Ireland", "Isle of Man", "Israel"}}},
		{"date label outside an entry", LadderRow{Label: "From"}},
		{"prompt differs from the label", LadderRow{Label: "Your favorite language", Placeholder: "Enter text here"}},
		{"a link aside after the question", LadderRow{Label: "What excited you about this role? You can check out our video about the team: https://video.example.test/1"}},
	} {
		t.Run("kept: "+c.name, func(t *testing.T) {
			if code, ex := LadderExclusion(c.row); ex {
				t.Fatalf("excluded as %q", code)
			}
		})
	}
}

// The owner's open rows (site wording) are of the shape F-10 answers from the
// user's information; the kinds the decision leaves out are not, nor is a
// question whose true Yes the user's information need not mention.
func TestGates_Ladder_OwnHistoryQuestionShape(t *testing.T) {
	for _, c := range []struct {
		label string
		want  bool
	}{
		{"Do you have experience with Kubernetes?", true},
		{"Have you used Terraform?", true},
		{"Are you certified in AWS?", true},
		{"Do you have a background in finance?", true},
		{"Do you have experience working in the sports industry?", true},
		{"Have you previously supported a SaaS company or a custom software application?", true},
		{"Do you have experience working within a Managed Service Provider environment?", true},
		{"Do you have experience in cybersecurity SaaS?", true},
		{"Do you have a public track record of open-source contributions, such as merged pull requests to libraries, or projects you've authored or actively maintain?", true},
		// Ambience (Ashby, live): "trackrecord" in one word.
		{"Do you have a trackrecord of outbound hunting net-new enterprise deals without reliance on inbound leads?", true},
		{"Are you a graduate of the RiseUp with ServiceNow (formerly NextGen) Program?", true},
		{"Have you participated in a hackathon?", true},
		{"Do you have experience with Go (3+ years)?", false},
		{"Do you have experience at the level the required skills of this role demand?", false},
		{"Do you have experience with Go and are you willing to learn Rust?", false},
		{"Have you ever worked in the United States without work authorization?", false},
		{"Have you ever worked at Northwind Example in any capacity?", false},
		{"Are you currently employed or have you ever been employed by Northwind Example?", false},
		{"Have you ever served in the armed forces as a veteran?", false},
		{"Which industries do you have experience in?", false},
		// Openers a disclosure takes, with and without a word of its family.
		{"Have you ever served in the military?", false},
		{"Have you ever served?", false},
		{"Have you ever been involved in a lawsuit?", false},
		{"Have you ever been part of a strike?", false},
		{"Are you a member of an underrepresented group?", false},
		{"Are you a member of a political party?", false},
		{"Do you have a criminal background?", false},
		{"Do you have a clean background?", false},
		// LiveKit (Ashby, live): a record of the applicant's own contributions.
		{"Do you contribute to open source repos?", true},
		// What the role requires.
		{"Do you have the experience required for this role?", false},
		{"Do you have similar experience?", false},
		// One disclosure of each family behind an opener.
		{"Did you attend a military academy?", false},
		{"Have you participated in a program for underrepresented groups?", false},
		{"Have you completed a drug test?", false},
		{"Have you completed probation?", false},
		{"Have you participated in a lawsuit?", false},
		{"Have you worked for a sanctioned entity?", false},
		{"Have you held a job from which you were fired?", false},
		{"Have you worked under a non-compete agreement?", false},
		{"Have you held a security clearance?", false},
		{"Have you held a US passport?", false},
		{"Have you ever worked under a different name?", false},
		{"Have you attended an interview with Northwind Example?", false},
		{"Have you worked with us before?", false},
	} {
		if got := OwnHistoryQuestion(c.label); got != c.want {
			t.Errorf("%q: %v, want %v", c.label, got, c.want)
		}
	}
}

func TestGates_Ladder_LegalDeclarationOffExcludedFromRungs(t *testing.T) {
	row := LadderRow{Label: "I agree to the Terms of Service"}
	if code, ex := LadderExclusion(row); !ex || code != ExcludedLegalDeclaration {
		t.Fatalf("setting off: %q %v", code, ex)
	}
	row.LegalSettingOn = true
	if _, ex := LadderExclusion(row); ex {
		t.Fatal("with the setting on the engine's own lane ticks it; the ladder has no part in it")
	}
}

// The computer-use rung writes a value the chain already chose (F-9): it leaves
// the owner self-identification, signatures and a legal declaration while the
// setting is off, and nothing that the model rungs exclude for the model's sake.
func TestGates_Ladder_ComputerUseLeavesSelfIDSignaturesAndDeclarationsOffToTheOwner(t *testing.T) {
	for _, c := range []struct {
		name string
		row  LadderRow
		code string
	}{
		{"signature", LadderRow{Label: "Signature"}, ExcludedSignature},
		{"legal declaration, setting off", LadderRow{Label: "I certify the information is true"}, ExcludedLegalDeclaration},
		{"legal declaration, setting on", LadderRow{Label: "I certify the information is true", LegalSettingOn: true}, ""},
		{"self-identification by its label", LadderRow{Label: "Gender"}, ExcludedSelfID},
		{"self-identification by its options", LadderRow{Label: "Select one", Options: []string{"Hispanic or Latino", "Not Hispanic or Latino"}}, ExcludedSelfID},
		{"immigration status, which the model rungs read as work authorization", LadderRow{Label: "What is your immigration status?"}, ExcludedSelfID},
		{"work authorization the bank answered", LadderRow{Label: "Are you legally authorized to work in the country?"}, ""},
		{"identity the profile answered", LadderRow{Label: "First name", IdentityBound: true}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code, _ := ComputerUseExclusion(c.row); code != c.code {
				t.Fatalf("got %q, want %q", code, c.code)
			}
		})
	}
}

func TestGates_Ladder_FactsWithholdIdentityAndDemographics(t *testing.T) {
	for k, want := range map[string]bool{
		"full-name": false, "Email": false, "phone": false, "date_of_birth": false, "Gender": false, "veteran": false,
		"work authorization": false, "citizenship": false,
		"years-of-go-experience": true, "preferred-start-month": true, "degree": true,
	} {
		if got := LadderFactAllowed(k); got != want {
			t.Errorf("LadderFactAllowed(%q) = %v", k, got)
		}
	}
}

func TestGates_Ladder_TrapFieldBlankNoGeneration(t *testing.T) {
	for _, l := range []string{"If you are a human, leave this field blank", "Leave this box empty", "leave blank"} {
		if code, ex := LadderExclusion(LadderRow{Label: l}); !ex || code != ExcludedTrapField {
			t.Errorf("%q -> %q %v", l, code, ex)
		}
	}
}
