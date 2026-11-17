package match

import "testing"

type textCase struct {
	text string
	want bool
}

func checkTexts(t *testing.T, name string, f func(string) bool, cases []textCase) {
	t.Helper()
	for _, c := range cases {
		if got := f(c.text); got != c.want {
			t.Errorf("%s(%q) = %v, want %v", name, c.text, got, c.want)
		}
	}
}

func TestAnswerBank_Unsafe_CriminalHistoryExplanation(t *testing.T) {
	checkTexts(t, "isCriminalHistoryExplanation", isCriminalHistoryExplanation, []textCase{
		{"If yes, please identify the offense and the circumstances", true},
		{"Describe any felony convictions", true},
		{"Please explain the misdemeanour", true},
		{"Give details of the offence", true},
		{"Explain the nature of your plea", true},
		{"Explain any guilty verdict or no contest plea", true},
		{"Identify any criminal matter", true},
		// A plain Yes/No question is answered from the bank, not refused.
		{"Have you ever been convicted of a felony?", false},
		{"Have you been convicted of a crime?", false},
		// An ask to explain without a criminal-history noun is an ordinary prompt.
		{"Please describe your experience", false},
		{"", false},
	})
}

func TestAnswerBank_Unsafe_ConditionalDetailSlot(t *testing.T) {
	checkTexts(t, "isConditionalDetailSlot", isConditionalDetailSlot, []textCase{
		{"If yes, please specify", true},
		{"IF NO, PLEASE EXPLAIN", true},
		{"If so, describe the role", true},
		{"If applicable, list the employee name", true},
		{"If you answered yes, provide the date", true},
		{"  If   yes,\n please   indicate the dates", true},
		{"If other, please specify", true},
		// A narrative prompt that merely opens with "If" is not swallowed.
		{"If you could change one thing about your last job, what would it be?", false},
		{"If you are passionate about data, tell us why", false},
		{"Please specify your answer if yes", false},
		// The verb must follow within 100 characters.
		{"If yes " + pad(120) + " specify", false},
	})
}

func pad(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestAnswerBank_Unsafe_ThirdPartyContactRequest(t *testing.T) {
	checkTexts(t, "isThirdPartyContactRequest", isThirdPartyContactRequest, []textCase{
		{"Please provide three professional references. (Provide name, phone, and or email contact)", true},
		{"Emergency contact", true},
		{"Emergency Contact name and phone", true},
		{"Reference 1: name, phone and e-mail", true},
		{"Phone number of your reference", true},
		{"Referee contact information", true},
		{"Please supply two referees", true},
		{"List your references", true},
		// A yes/no question about references keeps its curated route.
		{"May we contact your references?", false},
		// Consent to be contacted names the references one may give and asks
		// for none of their details.
		{"By submitting this application, I hereby provide consent to KnowBe4 to communicate directly with me, and any employment references I provide to KnowBe4, via phone call, text message, email, and/or LinkedIn regarding my application.", false},
		{"I consent to Acme contacting any references I provide. Reference 1: name, phone and e-mail", true},
		{"Are you willing to provide references upon request?", false},
		{"Do you have references?", false},
		// A question mark stops the detail word from reaching the reference word.
		{"Please list your skills. Do you have references? Phone preferred", false},
		// The imperative must sit within 60 characters of the word.
		{"Provide " + pad(70) + " references", false},
		{"Tell us about yourself", false},
		{"", false},
	})
}

func TestAnswerBank_Unsafe_CompositeGate(t *testing.T) {
	parts := map[string]string{
		"criminal-history explanation": "Identify the offense and explain the circumstances",
		"conditional detail slot":      "If yes, please describe",
		"third-party contact":          "Emergency contact",
		"leave-blank honeypot":         "If you're a human, leave this line blank",
	}
	for name, text := range parts {
		if !isGenerativelyUnsafeQuestion(text) {
			t.Errorf("%s: %q is unsafe", name, text)
		}
		if !isAttestationDefaultUnsafe(text) {
			t.Errorf("%s: the attestation gate includes the composite", name)
		}
	}
	if isGenerativelyUnsafeQuestion("Why do you want to work here?") {
		t.Error("a narrative prompt is composable")
	}
}

func TestAnswerBank_Unsafe_SensitiveCategory(t *testing.T) {
	checkTexts(t, "isSensitiveCategoryQuestion", isSensitiveCategoryQuestion, []textCase{
		{"Race", true}, {"Racial group", true}, {"Ethnicity", true}, {"Gender", true}, {"Gender identity", true},
		{"Sex", true}, {"Sexual orientation", true}, {"Disabled", true}, {"Disability status", true},
		{"Veteran status", true}, {"Military status", true}, {"Date of birth", true}, {"DOB", true}, {"Birth date", true},
		{"Your age", true}, {"Religion", true}, {"Religious affiliation", true}, {"Citizenship status", true},
		{"Immigration status", true}, {"National origin", true},
		// Whole words only.
		{"Essex county", false}, {"Do you like racing?", false},
		// Skills framing vetoes the word.
		{"Describe experience building accessibility features for users with disabilities", false},
		{"Knowledge of gender-inclusive design", false},
		{"Background in veteran hiring software", false},
		{"Passion for advocacy for disability rights", false},
		// A nonprofit exemption names religion without asking about it, in either order.
		{"Exclude activity that is exclusively charitable, civic, religious or fraternal", false},
		{"Exclude religious, charitable or civic activity", false},
		{"", false},
	})
	// The incident: the exemption wording dead-ended a required field.
	const incident = "Do you hold any non-employment positions such as an Officer of a Board? (Please exclude activity which is exclusively charitable, civic, religious or fraternal)"
	if isSensitiveCategoryQuestion(incident) {
		t.Error("the outside-activities question is not a self-identification question")
	}
}

func TestAnswerBank_Unsafe_DisqualifyingFactNoSkillsVeto(t *testing.T) {
	const driving = "I possess one year of verifiable driving experience and have no convictions of major traffic violations"
	if !isDisqualifyingFactDisclosure(driving) || !isAttestationDefaultUnsafe(driving) {
		t.Fatal("the word experience must not cancel a legal declaration")
	}
	checkTexts(t, "isDisqualifyingFactDisclosure", isDisqualifyingFactDisclosure, []textCase{
		{"Have you been convicted of a felony?", true}, {"Any misdemeanor charges", true}, {"Have you a criminal record?", true},
		{"I pleaded guilty", true}, {"I entered a plea of no contest", true}, {"Were you terminated for cause?", true},
		{"Have you been discharged?", true}, {"Were you ever asked to resign?", true}, {"Prior disciplinary action", true},
		{"Will you require visa sponsorship?", true}, {"Are you authorized to work in the United States?", true},
		{"Do you need immigration sponsorship?", true}, {"Work authorization status", true},
		// Consent to a check and a warning about consequences state no fact.
		{"I authorize a background check, including criminal records", false},
		{"Any false statement may result in discharge", false},
		{"I agree to the terms", false},
	})
}

func TestAnswerBank_Unsafe_AttestationDefaultGate(t *testing.T) {
	checkTexts(t, "isAttestationDefaultUnsafe", isAttestationDefaultUnsafe, []textCase{
		{"I acknowledge that I have been convicted of a felony", true}, // a first-person box about a disqualifying fact
		{"What is your religion?", true},
		{"Please provide three professional references", true},
		{"I certify that the information is true", false},
		{"I acknowledge the privacy notice", false},
		{"", false}, // empty text is safe
		// A requirement to show documents later states no fact (ASM).
		{"I understand that ASM will require me to present an original document issued by a government agency confirming my citizenship / residency status as indicated above.", false},
		{"I understand I will be required to provide documentation of identity and work authorization.", false},
		{"I am authorized to work in the United States and will be required to provide proof of it.", true},
		// The employer's own accommodation offer states no fact (Suno); a
		// sentence that asks the applicant still counts.
		{"Please confirm you read the policy. We will consider requests for accommodation to this policy and, upon request, will work with employees to explore reasonable accommodations for physical or mental disabilities.", false},
		{"We will work with you on accommodations. Do you have a disability?", true},
		{"Do you require a reasonable accommodation for a disability?", true},
		{"I confirm I do not need an accommodation for a disability.", true},
	})
}
