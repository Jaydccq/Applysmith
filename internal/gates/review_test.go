// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"slices"
	"strings"
	"testing"
)

func TestGates_Identity_NormalizationTable(t *testing.T) {
	// Built at run time so no phone-shaped literal sits in the source.
	phone10 := "5550100123"
	for _, c := range []struct {
		name  string
		field IdentityField
		page  string
		prof  string
		match bool
	}{
		{"phone with +1, parentheses and dashes", FieldPhone, "+1 (" + phone10[:3] + ") " + phone10[3:6] + "-" + phone10[6:], phone10, true},
		{"phone with a leading 1", FieldPhone, "1" + phone10, phone10, true},
		{"phone with a different digit", FieldPhone, phone10[:9] + "4", phone10, false},
		{"national phone", FieldNationalPhone, phone10[:3] + "." + phone10[3:6] + "." + phone10[6:], phone10, true},
		{"email case", FieldEmail, "Alex@Example.com", "alex@example.com", true},
		{"email padded", FieldEmail, "  alex@example.com ", "alex@example.com", true},
		{"email differs", FieldEmail, "alexa@example.com", "alex@example.com", false},
		{"first name case matters", FieldFirstName, "alex", "Alex", false},
		{"first name whitespace", FieldFullName, "  Alex   Example ", "Alex Example", true},
		{"wrong first name", FieldFirstName, "Alexa", "Alex", false},
		{"linkedin spellings", FieldLinkedIn, "https://www.linkedin.com/in/alex-example/", "linkedin.com/in/alex-example", true},
		{"github scheme and case", FieldGitHub, "HTTP://GitHub.com/alex-example//", "github.com/alex-example", true},
		{"portfolio differs", FieldPortfolio, "https://alex.example.test", "https://other.example.test", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := ReviewIdentity([]IdentityControl{{Selector: "#f", Label: "Field", Field: c.field, PageValue: c.page, ProfileValue: c.prof, ReRead: true}})
			if c.match {
				got(t, r.Decision, GateIdentityReview, Allow, CodeOK)
			} else {
				got(t, r.Decision, GateIdentityReview, Deny, CodeIdentityMismatch)
			}
		})
	}
}

func TestGates_Identity_UnverifiedDoesNotBlockMismatchDoes(t *testing.T) {
	ctl := IdentityControl{Selector: "#first", Label: "First name", Field: FieldFirstName, PageValue: "Alexa", ProfileValue: "Alex", ReRead: true}
	r := ReviewIdentity([]IdentityControl{ctl})
	got(t, r.Decision, GateIdentityReview, Deny, CodeIdentityMismatch)
	for _, want := range []string{"#first", "First name", "first_name", "Alexa", "Alex"} {
		if !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q lacks %q", r.Reason, want)
		}
	}
	if r.Class != ClassDefect || len(r.Mismatches) != 1 {
		t.Fatalf("result = %+v", r)
	}
	ctl.ReRead = false
	r = ReviewIdentity([]IdentityControl{ctl})
	got(t, r.Decision, GateIdentityReview, Allow, CodeOK)
	if len(r.Unverified) != 1 {
		t.Fatalf("unverified = %v", r.Unverified)
	}
	// An empty page value is a required-leftover matter; an empty profile
	// value leaves nothing to compare.
	for _, c := range []IdentityControl{
		{Field: FieldEmail, PageValue: "", ProfileValue: "alex@example.com", ReRead: true},
		{Field: FieldEmail, PageValue: "alex@example.com", ProfileValue: "", ReRead: true},
		{Field: FieldPhone, PageValue: "n/a", ProfileValue: "", ReRead: true},
	} {
		got(t, ReviewIdentity([]IdentityControl{c}).Decision, GateIdentityReview, Allow, CodeOK)
	}
}

func TestGates_Identity_MismatchBlocksSubmitNoModelRungMayAnswer(t *testing.T) {
	r := ReviewIdentity([]IdentityControl{{Selector: "#e", Field: FieldEmail, PageValue: "x@example.com", ProfileValue: "y@example.com", ReRead: true}})
	got(t, r.Decision, GateIdentityReview, Deny, CodeIdentityMismatch)
	if code, ex := LadderExclusion(LadderRow{Label: "Email", IdentityBound: true}); !ex || code != ExcludedIdentity {
		t.Fatalf("an identity control must be excluded from the ladder, got %q %v", code, ex)
	}
}

func TestGates_Review_FlagFloorsBlockExactlyAtBound(t *testing.T) {
	for _, c := range []struct {
		name  string
		value string
		p     float64
		v     Verdict
		code  string
	}{
		{"below the floor", "Master's", 0.69, Allow, CodeFlagAdvice},
		{"at the floor", "Master's", 0.70, Deny, CodeFlagBlocks},
		{"above the floor", "Master's", 0.71, Deny, CodeFlagBlocks},
		{"phone code below", "+1", 0.84, Allow, CodeFlagAdvice},
		{"phone code at", "+1", 0.85, Deny, CodeFlagBlocks},
		{"phone code above", "+44", 0.86, Deny, CodeFlagBlocks},
		{"phone code between the floors", "+1", 0.70, Allow, CodeFlagAdvice},
		{"four digits is not a bare code", "+1234", 0.70, Deny, CodeFlagBlocks},
		{"plus with text is not a bare code", "+1 555", 0.70, Deny, CodeFlagBlocks},
		{"certain", "Yes", 1, Deny, CodeFlagBlocks},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, FlagGate(Flag{Value: c.value, Probability: c.p, HasProbability: true}, "", ""), GateReviewFlag, c.v, c.code)
		})
	}
	d := FlagGate(Flag{Value: "Master's", Probability: 0.70, HasProbability: true}, "", "")
	if d.Reason != "the review contradicts a value this run wrote" || d.Class != ClassDefect {
		t.Fatalf("decision = %+v", d)
	}
}

func TestGates_Review_NoSwitchDisablesBlocking(t *testing.T) {
	// FlagGate and FlagBlocks take no switch: the flag alone decides. That the
	// package reads no environment is the layering guard's rule for a pure
	// package, not something a run of this test could show.
	if !FlagBlocks(Flag{Value: "x", Probability: 0.9, HasProbability: true}) {
		t.Fatal("a blocking flag must block")
	}
	if FlagBlocks(Flag{Value: "x", Probability: 0.99}) {
		t.Fatal("a flag without a probability never blocks")
	}
	got(t, FlagGate(Flag{Value: "x", Probability: 0.99}, "", ""), GateReviewFlag, Allow, CodeFlagNoProbability)
}

func TestGates_Review_BareYesNoInSpecifyBoxBlocks(t *testing.T) {
	for _, c := range []struct {
		name string
		row  ReviewRow
		want bool
	}{
		{"specify box with Yes", ReviewRow{Label: "If you selected other, please specify which one", Value: "Yes", TextControl: true}, true},
		{"describe box with no", ReviewRow{Label: "Please describe", Value: " no. ", TextControl: true}, true},
		{"explain with TRUE", ReviewRow{Label: "Explain", Value: "TRUE", TextControl: true}, true},
		{"follow-up of a selection", ReviewRow{Label: "If you selected yes above, why?", Value: "N", TextControl: true}, true},
		{"own words", ReviewRow{Label: "In your own words", Value: "false", TextControl: true}, true},
		{"has options", ReviewRow{Label: "Please specify", Value: "Yes", TextControl: true, HasOptions: true}, false},
		{"not a text control", ReviewRow{Label: "Please specify", Value: "Yes"}, false},
		{"a real answer", ReviewRow{Label: "Please specify", Value: "Yes, the Example Referral Program", TextControl: true}, false},
		{"label asks nothing", ReviewRow{Label: "Are you a student", Value: "Yes", TextControl: true}, false},
		{"list asks whether", ReviewRow{Label: "Please list if you have ever worked under or earned degrees or professional certifications under another name (i.e., maiden name)", Value: "No", TextControl: true}, false},
		{"list asks for the names", ReviewRow{Label: "If yes, please list the names", Value: "No", TextControl: true}, true},
		// Maven Clinic (Greenhouse, live): the box asks its own question first; No answers it whole.
		{"a No to the box's own question with a detail asked only of a Yes", ReviewRow{Label: "Are you currently employed by Maven Clinic in a contracted capacity? If so, please list current role.", Value: "No", TextControl: true}, false},
		{"a Yes to the box's own question that asks for detail", ReviewRow{Label: "Are you currently employed by Maven Clinic in a contracted capacity? If so, please list current role.", Value: "Yes", TextControl: true}, true},
		// Relativity Space (Greenhouse, owner workspace): the explanation is asked only of a No.
		{"a Yes to the box's own question with an explanation asked only of a No", ReviewRow{Label: "If your location differs from the location posted on the job description, are you willing to commute and/or relocate for this role? If not, please explain:", Value: "Yes", TextControl: true}, false},
		{"a No to the box's own question that asks to explain a No", ReviewRow{Label: "If your location differs from the location posted on the job description, are you willing to commute and/or relocate for this role? If not, please explain:", Value: "No", TextControl: true}, true},
		// Scale AI (Greenhouse, queue192): the same with the detail in brackets.
		{"a No to the box's own question with a bracketed detail asked only of a Yes", ReviewRow{Label: "Do you have any restrictions on post-government employment? (If yes, please describe.)", Value: "No", TextControl: true}, false},
		{"a Yes to the box's own question with a bracketed detail", ReviewRow{Label: "Do you have any restrictions on post-government employment? (If yes, please describe.)", Value: "Yes", TextControl: true}, true},
		{"a bare number to why", ReviewRow{Label: "From the job description, which of the three possible projects resonates the most and why?", Value: "4", TextControl: true}, true},
		{"a count asked with why", ReviewRow{Label: "How many projects have you shipped, and why that many?", Value: "4", TextControl: true}, false},
		{"model talks about missing data", ReviewRow{Label: "Why us", Value: "No information was provided about the company.", TextControl: true}, true},
		{"as an AI", ReviewRow{Label: "Why us", Value: "As an AI I cannot say", TextControl: true}, true},
		// Evertune (Ashby, queue793): the answer told the form what its notes lacked.
		{"model talks about what its notes document", ReviewRow{Label: "Describe a backend system you built", Value: "I work backend-first. That said, the information available here does not document a specific system's architecture, so I cannot accurately provide those details.", TextControl: true}, true},
		{"prose about details that do not change", ReviewRow{Label: "When can you start?", Value: "The details below do not change my start date of next month.", TextControl: true}, false},
		{"normal prose", ReviewRow{Label: "Why us", Value: "I like the mission.", TextControl: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, ok := ShapeFlag(c.row)
			if ok != c.want {
				t.Fatalf("flag = %v", ok)
			}
			if ok {
				got(t, FlagGate(f, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
			}
		})
	}
}

func TestGates_Review_PostingOwnOfficeFlagDropped(t *testing.T) {
	loc := "Example City, EX; Sample Town, SX (hybrid)"
	for _, c := range []struct {
		value string
		drop  bool
	}{
		{"Example City, EX", true}, {"sample town", true}, {"Example City", true}, {"example   city", true},
		{"Other Place", false}, {"", false}, {"Example", false},
	} {
		d := FlagGate(Flag{Value: c.value, Probability: 0.9, HasProbability: true}, "", loc)
		if c.drop {
			got(t, d, GateReviewFlag, Allow, CodeFlagDropped)
		} else if d.Code == CodeFlagDropped {
			t.Errorf("%q must not be dropped", c.value)
		}
	}
}

func TestGates_Review_NotListedChoiceFlagIsAdvice(t *testing.T) {
	for _, v := range []string{"Other (School Not Listed)", "Other - School Not Listed", "other: employer not listed"} {
		got(t, FlagGate(Flag{Value: v, Probability: 0.95, HasProbability: true}, "", ""), GateReviewFlag, Allow, CodeFlagNotListed)
	}
	for _, v := range []string{"Example University", "Other", "Not listed", "Another school not listed"} {
		if d := FlagGate(Flag{Value: v, Probability: 0.95, HasProbability: true}, "", ""); d.Code == CodeFlagNotListed {
			t.Errorf("%q must not count as the not-listed choice", v)
		}
	}
	school := LabeledFlag{Label: "Which university did you last attend?", Flag: Flag{Value: "Other (School Not Listed)", Probability: 0.9, HasProbability: true}}
	got(t, SubmitReadiness(ReadinessInput{Final: true, Pages: []PageReport{{Number: 1, Flags: []LabeledFlag{school}}}}).Decision, GateReadiness, Allow, CodeOK)
	// A school list's bare "Other" (Ensemble's Workday catalog) is that choice too;
	// "Other" anywhere else is not.
	for _, label := range []string{"School or University", "Are you currently attending one of the following schools?"} {
		other := LabeledFlag{Label: label, Flag: Flag{Value: "Other", Probability: 0.9, HasProbability: true}}
		got(t, SubmitReadiness(ReadinessInput{Final: true, Pages: []PageReport{{Number: 1, Flags: []LabeledFlag{other}}}}).Decision, GateReadiness, Allow, CodeOK)
	}
	for _, label := range []string{"Gender", "How did you hear about us?", ""} {
		if d := FlagGate(Flag{Value: "Other", Probability: 0.95, HasProbability: true}, label, ""); d.Code == CodeFlagNotListed {
			t.Errorf("%q: a bare Other outside a school list must not count as the not-listed choice", label)
		}
	}
	// A state list's own catch-all names itself (n8n: fourteen states and "Other state" for an applicant in South Carolina).
	for _, v := range []string{"Other state", "Other States", "Other U.S. state"} {
		got(t, FlagGate(Flag{Value: v, Probability: 0.95, HasProbability: true}, "Which U.S. state would you primarily work from if hired?", ""), GateReviewFlag, Allow, CodeFlagNotListed)
	}
	for _, v := range []string{"South Carolina", "Other statement", "Another state"} {
		if d := FlagGate(Flag{Value: v, Probability: 0.95, HasProbability: true}, "Which U.S. state would you primarily work from if hired?", ""); d.Code == CodeFlagNotListed {
			t.Errorf("%q must not count as a state list's catch-all", v)
		}
	}
}

// The review model listed Runway's "Yes" to "Are you currently based in the
// U.S. or Canada?" at a blocking probability with a reason that cleared it.
func TestGates_Review_AFlagWhoseReasonClearsTheValueIsAdvice(t *testing.T) {
	cleared := Flag{Value: "Yes", Probability: 0.9, HasProbability: true,
		Reason: "The profile says the applicant is in Charleston, South Carolina, United States, so Yes is consistent; no values are contradicted."}
	got(t, FlagGate(cleared, "", ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	based := LabeledFlag{Label: "Are you currently based in the U.S. or Canada?", Flag: cleared}
	got(t, SubmitReadiness(ReadinessInput{Final: true, Pages: []PageReport{{Number: 1, Flags: []LabeledFlag{based}}}}).Decision, GateReadiness, Allow, CodeOK)
	// Verkada: the reason ends by putting the figure inside the very range it flagged.
	gpa := Flag{Value: "3.1 - 3.5", Probability: 0.9, HasProbability: true,
		Reason: "The Carver master's GPA is 3.4, which falls in the 3.0–3.4 range, not 3.1–3.5? Actually 3.4 is within 3.1–3.5."}
	got(t, FlagGate(gpa, "What is your GPA?", ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	wrong := Flag{Value: "3.1 - 3.5", Probability: 0.9, HasProbability: true, Reason: "The GPA is 3.8, which is within 3.6 - 4.0, not 3.1 - 3.5."}
	got(t, FlagGate(wrong, "What is your GPA?", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	// Applied Intuition: "Degree" = "Master's degree" flagged at 0.99 by a
	// reason whose last sentence clears it.
	degree := Flag{Value: "Master's degree", Probability: 0.99, HasProbability: true,
		Reason: "The profile lists an M.S. in Computer Engineering as completed; \u201cMaster's degree\u201d is not the contradiction? Actually, it matches the profile."}
	got(t, FlagGate(degree, "Degree", ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	// 10a Labs: the start year flagged by a reason whose closing clause clears it.
	year := Flag{Value: "2024", Probability: 0.9, HasProbability: true,
		Reason: "The master's degree started in 2024, not 2024? The profile lists 2024-08, so this value matches."}
	got(t, FlagGate(year, "Start date year", ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	for _, r := range []string{"The degree is consistent with the profile.", "The value is not the contradiction."} {
		got(t, FlagGate(Flag{Value: "Master's degree", Probability: 0.99, HasProbability: true, Reason: r}, "Degree", ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	}
	for _, r := range []string{"It matches the profile's bachelor's degree, not a master's.", "The profile matches a bachelor's; the value says master's.",
		"The value does not match the profile.", "The profile lists 2023, so this value no longer matches.", "The profile lists 2023-08, while this value matches only the end year."} {
		got(t, FlagGate(Flag{Value: "Master's degree", Probability: 0.99, HasProbability: true, Reason: r}, "Degree", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	}
	// Akuity: a No flagged by a reason that says the profile holds no such
	// thing is that No; a Yes so flagged is contradicted.
	absent := "The profile shows no work at a B2B SaaS startup; it lists software engineering at Northwind Labs and a research internship."
	got(t, FlagGate(Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: absent}, "", ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	got(t, FlagGate(Flag{Value: "Yes", Probability: 0.9, HasProbability: true, Reason: absent}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: "The profile lists two years at Acme, a Series B SaaS startup."}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	// Regard: a years bracket that starts at zero claims none of the
	// experience the reason says the profile lacks; one above zero does.
	none := "The profile lists no full-time data engineering or data-pipeline experience; its full-time software engineering role began in June 2026."
	for _, v := range []string{"0-2", "0 - 2 years", "0", "None", "Less than 1 year", "Under 2 years"} {
		got(t, FlagGate(Flag{Value: v, Probability: 0.9, HasProbability: true, Reason: none}, "", ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	}
	for _, v := range []string{"3-5", "1-2 years", "10+", "0-2 and 3-5"} {
		got(t, FlagGate(Flag{Value: v, Probability: 0.9, HasProbability: true, Reason: none}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	}
	for _, why := range []string{
		"The profile lists the Carver degree as Computer Engineering, not Engineering.",
		"The profile lists a B.S. and an M.S. in progress, so Other contradicts the listed education level.",
		"The start year is consistent but the profile's degree ended in 2026, not 2025.",
	} {
		f := Flag{Value: "x", Probability: 0.9, HasProbability: true, Reason: why}
		got(t, FlagGate(f, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	}
	// Wonderschool: "None" picked from a list whose reason names the list's
	// choices only as ones the profile lacks agrees with it, whatever it
	// concludes; a reason that names a choice the profile holds blocks.
	fields := []string{"Private equity", "Investment banking", "Consulting", "None"}
	const fieldsLabel = "Which of these fields have you worked in?"
	for _, why := range []string{
		"The profile shows software engineering and research experience, but no private equity, investment banking, or consulting experience; selecting None contradicts the listed work history.",
		"Neither private equity nor investment banking appears in the profile, and it lacks consulting work, so None contradicts the work history.",
	} {
		got(t, FlagGate(Flag{Value: "None", Probability: 0.9, HasProbability: true, Reason: why, Choices: fields}, fieldsLabel, ""), GateReviewFlag, Allow, CodeFlagSelfCleared)
	}
	for _, why := range []string{
		"The profile shows consulting work at Deloitte, so None is contradicted.",
		"The profile lists consulting at Deloitte though no private equity; None contradicts it.",
		"Neither private equity nor investment banking appears, but the profile lists consulting at Deloitte; None contradicts it.",
		"The profile shows software engineering experience, so None contradicts it.",
	} {
		got(t, FlagGate(Flag{Value: "None", Probability: 0.9, HasProbability: true, Reason: why, Choices: fields}, fieldsLabel, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	}
	agree := "The profile shows software engineering and research experience, but no private equity, investment banking, or consulting experience; selecting None contradicts it."
	got(t, FlagGate(Flag{Value: "Consulting", Probability: 0.9, HasProbability: true, Reason: agree, Choices: fields}, fieldsLabel, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "None", Probability: 0.9, HasProbability: true, Reason: agree}, fieldsLabel, ""), GateReviewFlag, Deny, CodeFlagBlocks)
}

// The floor is calibrated on the review task's own model; a fallback that
// answered while it was down read an applicant's own ZIP as a UK postcode at
// probability 1.0.
func TestGates_Review_AFallbackModelsFlagIsAdvice(t *testing.T) {
	own := Flag{Value: "29401", Probability: 1, HasProbability: true}
	got(t, FlagGate(own, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	fallback := own
	fallback.Fallback = true
	got(t, FlagGate(fallback, "", ""), GateReviewFlag, Allow, CodeFlagFallback)
	zip := LabeledFlag{Label: "Zip Code", Flag: fallback}
	got(t, SubmitReadiness(ReadinessInput{Final: true, Pages: []PageReport{{Number: 1, Flags: []LabeledFlag{zip}}}}).Decision, GateReadiness, Allow, CodeOK)
}

func TestGates_Review_RowSelectionAndBounds(t *testing.T) {
	rows := make([]ReviewRow, 0, 60)
	for i := 0; i < 60; i++ {
		rows = append(rows, ReviewRow{Label: "q", Value: strings.Repeat("v", 200), WrittenByRun: true})
	}
	sel := SelectReviewRows(rows)
	if len(sel) != ReviewMaxRows || len([]rune(sel[0].Value)) != ReviewValueMaxRunes {
		t.Fatalf("selected %d rows with %d characters", len(sel), len([]rune(sel[0].Value)))
	}
	for _, c := range []struct {
		name string
		row  ReviewRow
		keep bool
	}{
		{"written", ReviewRow{Value: "x", WrittenByRun: true}, true},
		{"not written by this run", ReviewRow{Value: "x"}, false},
		{"empty value", ReviewRow{Value: "  ", WrittenByRun: true}, false},
		{"attestation lane", ReviewRow{Value: "x", WrittenByRun: true, Attestation: true}, false},
		{"identity bound", ReviewRow{Value: "x", WrittenByRun: true, IdentityBound: true}, false},
		{"signature", ReviewRow{Value: "x", WrittenByRun: true, Signature: true}, false},
		{"statement about the application", ReviewRow{Value: "x", WrittenByRun: true, ApplicationStatement: true}, false},
		{"overall experience", ReviewRow{Label: "How many years of relevant experience do you possess?", Value: "Less than 1 year", WrittenByRun: true}, false},
		{"full-time experience", ReviewRow{Label: "Years of full-time work experience", Value: "0", WrittenByRun: true}, false},
		{"hands-on engineering experience", ReviewRow{Label: "How many years of hands-on engineering experience do you have?", Value: "0-2 years", WrittenByRun: true}, false},
		{"full-time, professional experience", ReviewRow{Label: "How many years of full-time, professional experience do you have as a software engineer?", Value: "Less than 4 years", WrittenByRun: true}, false},
		{"experience with a named skill", ReviewRow{Label: "How many years of experience do you have with Python?", Value: "3-5 years", WrittenByRun: true}, true},
		{"experience in a named field", ReviewRow{Label: "Years of professional experience in data engineering", Value: "1-2 years", WrittenByRun: true}, true},
		{"the applicant's country", ReviewRow{Label: "Country*", Value: "United States", WrittenByRun: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := len(SelectReviewRows([]ReviewRow{c.row})) == 1; got != c.keep {
				t.Fatalf("kept = %v", got)
			}
		})
	}
	for _, n := range []int{ReviewMaxRows - 1, ReviewMaxRows, ReviewMaxRows + 1} {
		if got := len(SelectReviewRows(rows[:n])); got != min(n, ReviewMaxRows) {
			t.Errorf("%d rows -> %d", n, got)
		}
	}
	if ReviewProfileMaxLines != 15 || ReviewMaxOutputTokens != 512 {
		t.Fatal("review bounds changed")
	}
}

// Black Canyon (Greenhouse, queue310): a 145-character answer ending "a
// benchmark demonstrated capacity exceeding 1,000 requests per minute." was
// shown to the review cut at 120 characters, "... capacity exceeding", and the
// review flagged it for leaving the number out. A cut value now ends with an
// ellipsis inside the bound; a value that fits is shown whole.
func TestGates_Review_ACutValueEndsWithAnEllipsis(t *testing.T) {
	fits := strings.Repeat("v", ReviewValueMaxRunes)
	sel := SelectReviewRows([]ReviewRow{{Value: strings.Repeat("word ", 40), WrittenByRun: true}, {Value: fits, WrittenByRun: true}})
	if got := []rune(sel[0].Value); len(got) != ReviewValueMaxRunes || got[len(got)-1] != '…' {
		t.Fatalf("a cut value reads %q", sel[0].Value)
	}
	if sel[1].Value != fits {
		t.Fatalf("a value that fits reads %q", sel[1].Value)
	}
}

func TestGates_Readiness_VerdictPrecedenceAndPerPageSubset(t *testing.T) {
	bad := PageReport{
		Number:             2,
		ReReadFailed:       true,
		IdentityMismatches: []Item{{Page: 2, Label: "Email"}},
		RequiredUnanswered: []Item{{Page: 2, Label: "Visa"}},
		NotHeld:            []Item{{Page: 2, Label: "Degree"}},
		Flags:              []LabeledFlag{{Label: "Phone", Flag: Flag{Value: "+1", Probability: 0.9, HasProbability: true}}},
	}
	one := func(p PageReport, final bool) ReadinessResult {
		return SubmitReadiness(ReadinessInput{Pages: []PageReport{p}, Final: final})
	}
	for _, c := range []struct {
		name  string
		page  PageReport
		final bool
		code  string
		class string
		n     int
	}{
		{"everything fails, unreadable first", bad, true, CodePageUnreadable, ClassDefect, 5},
		{"identity next", PageReport{Number: 1, IdentityMismatches: bad.IdentityMismatches, RequiredUnanswered: bad.RequiredUnanswered, NotHeld: bad.NotHeld}, true, CodeIdentityMismatch, ClassDefect, 3},
		{"required leftover is an answer gap", PageReport{Number: 1, RequiredUnanswered: bad.RequiredUnanswered, NotHeld: bad.NotHeld}, true, CodeRequiredUnanswered, ClassAnswerGap, 2},
		{"per page the walker's gate owns required leftovers", PageReport{Number: 1, RequiredUnanswered: bad.RequiredUnanswered}, false, "", "", 0},
		{"value not held", PageReport{Number: 1, NotHeld: bad.NotHeld}, false, CodeValueNotHeld, ClassDefect, 1},
		{"blocking flag", PageReport{Number: 1, Flags: bad.Flags}, false, CodeReviewContradiction, ClassDefect, 1},
		{"advice flag does not fail", PageReport{Number: 1, Flags: []LabeledFlag{{Label: "x", Flag: Flag{Value: "x", Probability: 0.5, HasProbability: true}}}, ReviewStatus: "failed"}, true, "", "", 0},
		{"clean", PageReport{Number: 1, ReviewStatus: "skipped"}, true, "", "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := one(c.page, c.final)
			if c.code == "" {
				got(t, r.Decision, GateReadiness, Allow, CodeOK)
			} else {
				got(t, r.Decision, GateReadiness, Deny, c.code)
				if r.Class != c.class {
					t.Fatalf("class = %q", r.Class)
				}
			}
			if len(r.Failures) != c.n {
				t.Fatalf("failures = %v", r.Failures)
			}
		})
	}
}

func TestGates_Readiness_WrongPolarityNeverRewritten(t *testing.T) {
	it := Item{Page: 3, Label: "Convicted of a felony", WrongPolarity: true}
	r := SubmitReadiness(ReadinessInput{Pages: []PageReport{{Number: 3, NotHeld: []Item{it}}}})
	got(t, r.Decision, GateReadiness, Deny, CodeValueNotHeld)
	if !r.Failures[0].WrongPolarity {
		t.Fatalf("failures = %+v", r.Failures)
	}
	if !strings.Contains(r.Reason, "page 3") || !strings.Contains(r.Reason, "Convicted of a felony") {
		t.Fatalf("reason = %q", r.Reason)
	}
}

func TestGates_Readiness_UnreadablePageReasonAndOwnOfficeFlag(t *testing.T) {
	r := SubmitReadiness(ReadinessInput{Pages: []PageReport{{Number: 1, ReReadFailed: true}}})
	if !strings.HasPrefix(r.Reason, "the page could not be re-read, so nothing on it is verified") {
		t.Fatalf("reason = %q", r.Reason)
	}
	office := LabeledFlag{Label: "Location", Flag: Flag{Value: "Example City", Probability: 0.95, HasProbability: true}}
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{{Number: 1, Flags: []LabeledFlag{office}}}, PostingLocation: "Example City, EX"}).Decision, GateReadiness, Allow, CodeOK)
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{{Number: 1, Flags: []LabeledFlag{office}}}}).Decision, GateReadiness, Deny, CodeReviewContradiction)
}

// The reason is shown to the user as the run's detail, so it names no code.
func TestGates_Readiness_ReasonIsPlainWords(t *testing.T) {
	r := SubmitReadiness(ReadinessInput{Final: true, Pages: []PageReport{{Number: 2,
		RequiredUnanswered: []Item{{Label: "First name"}},
		NotHeld:            []Item{{Label: "Work authorization", WrongPolarity: true}},
	}}})
	got(t, r.Decision, GateReadiness, Deny, CodeRequiredUnanswered)
	for _, code := range []string{CodeRequiredUnanswered, CodeValueNotHeld, CodeIdentityMismatch, CodeReviewContradiction} {
		if strings.Contains(r.Reason, code) {
			t.Errorf("reason %q carries the code %s", r.Reason, code)
		}
	}
	if !strings.Contains(r.Reason, `page 2 "First name": required and not answered`) || !strings.Contains(r.Reason, "opposite answer") {
		t.Errorf("reason = %q", r.Reason)
	}
}

// A blocking flag is said with its own reason, so the user knows what to fix:
// Expensify's composed coding history named the CV's "Software Engineering"
// master's, which the profile records as Computer Engineering.
func TestGates_Readiness_FlagSaysItsReason(t *testing.T) {
	model := LabeledFlag{Label: "What's your coding history?", Flag: Flag{Value: "I started coding ...", Probability: 0.9, HasProbability: true,
		Reason: "The profile lists a master's in Computer Engineering, not Software Engineering."}}
	shape, ok := ShapeFlag(ReviewRow{Label: "If yes, please specify", Value: "No", TextControl: true})
	if !ok {
		t.Fatal("a bare No under specify raises no flag")
	}
	r := SubmitReadiness(ReadinessInput{Pages: []PageReport{{Number: 1, Flags: []LabeledFlag{model, {Label: "If yes, please specify", Flag: shape}}}}})
	got(t, r.Decision, GateReadiness, Deny, CodeReviewContradiction)
	for _, want := range []string{
		`page 1 "What's your coding history?": looks wrong (The profile lists a master's in Computer Engineering, not Software Engineering)`,
		`page 1 "If yes, please specify": looks wrong (a bare yes or no where the question asks for detail)`,
	} {
		if !strings.Contains(r.Reason, want) {
			t.Errorf("reason = %q, want %q", r.Reason, want)
		}
	}
}

// A graduation question answered with a term or with the option for a
// finished degree is never reviewed: the review read Decagon's "Fall 2026", the
// earliest term a list offers a May 2026 graduate, and P&G's "I am not
// currently studying" as contradictions. A graduation date and a term written
// to another question are still reviewed.
func TestGates_Review_GraduationAnswer(t *testing.T) {
	rows := []ReviewRow{
		{Label: "What is your graduation date?", Value: "Fall 2026 (September-December)", WrittenByRun: true},
		{Label: "Expected graduation term", Value: "Later than Summer 2027", WrittenByRun: true},
		{Label: "If currently studying, how far are you from graduating with the degree selected above?", Value: "I am not currently studying", WrittenByRun: true},
		{Label: "Expected graduation", Value: "Already graduated", WrittenByRun: true},
		{Label: "Projected graduation date?", Value: "Before spring 2027", WrittenByRun: true},
		{Label: "Graduation year", Value: "Prior to 2025", WrittenByRun: true},
		{Label: "Graduation Date", Value: "2026-05-01", WrittenByRun: true},
		{Label: "When can you start?", Value: "Fall 2026", WrittenByRun: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0].Label != "Graduation Date" || sel[1].Label != "When can you start?" {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A grade-average question answered from the record with one of the form's
// brackets is not reviewed: shown both degrees' averages but not the rule
// (the most recent degree), the review read Verkada's "3.1 - 3.5" for a 3.4
// master's as a contradiction twice. A model's bracket, a bracket written to
// another question and a typed average are still reviewed.
func TestGates_Review_GPABracketFromTheRecord(t *testing.T) {
	rows := []ReviewRow{
		{Label: "What is your GPA?", Value: "3.1 - 3.5", WrittenByRun: true, FromRecord: true},
		{Label: "Cumulative grade point average", Value: "3.0 or under", WrittenByRun: true, FromRecord: true},
		{Label: "Undergraduate GPA", Value: "3.5+", WrittenByRun: true, FromRecord: true},
		{Label: "What is your GPA?", Value: "3.6 - 4.0", WrittenByRun: true},
		{Label: "How many years of experience with Python do you have?", Value: "3 - 5", WrittenByRun: true, FromRecord: true},
		{Label: "GPA", Value: "3.4", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 3 || sel[0].Value != "3.6 - 4.0" || sel[1].Label != rows[4].Label || sel[2].Value != "3.4" {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// Scale AI (Greenhouse, queue186): "I confirm that my graduation date will be
// either Fall 2026 or Spring 2027" = No, written from the dated education, was
// flagged by a review whose reason said the date is in neither term. A Yes or
// No to a graduation term or year written from the record is not reviewed; a
// model's is, and so is one that names no term.
func TestGates_Review_AGraduationTermYesNoFromTheRecord(t *testing.T) {
	const q = "I confirm that my graduation date will be either Fall 2026 or Spring 2027"
	rows := []ReviewRow{
		{Label: q, Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "No", WrittenByRun: true},
		{Label: "Have you graduated?", Value: "Yes", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0] != rows[1] || sel[1] != rows[2] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// Applied Intuition: "Degree" = "Master's degree", written from the record
// for an M.S. that ended in May, was flagged twice by a review that misread
// the date. A degree level written from the record is not reviewed; a model's
// is, and so is a degree in progress or expected, or one in a repeated
// education entry.
func TestGates_Review_ADegreeLevelFromTheRecord(t *testing.T) {
	rows := []ReviewRow{
		{Label: "Degree", Value: "Master's degree", WrittenByRun: true, FromRecord: true},
		{Label: "What is the highest level of education you have completed?", Value: "Master's", WrittenByRun: true, FromRecord: true},
		{Label: "Highest degree obtained*", Value: "Master's", WrittenByRun: true, FromRecord: true},
		{Label: "Degree", Value: "Master's degree", WrittenByRun: true},
		{Label: "Degree you are currently pursuing", Value: "Master's", WrittenByRun: true, FromRecord: true},
		{Label: "Expected degree", Value: "Master's", WrittenByRun: true, FromRecord: true},
		{Label: "Degree", Value: "Masters", WrittenByRun: true, FromRecord: true, InEntry: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 4 || sel[0] != rows[3] || sel[1] != rows[4] || sel[2] != rows[5] || sel[3] != rows[6] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A Yes or No to whether the applicant is enrolled, answered from the record,
// is not reviewed: Reducto's "currently enrolled in Full time Education" = No
// for a May 2026 graduate was flagged by a reason that itself said the
// applicant is not enrolled. A model's answer and another question are still
// reviewed.
func TestGates_Review_AnEnrollmentYesNoFromTheRecord(t *testing.T) {
	const q = "Are you currently enrolled in Full time Education? Like a Bachelor's, Master's, or PhD program"
	rows := []ReviewRow{
		{Label: q, Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: "Are you currently a student?", Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: "Are you currently in full-time education?", Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "No", WrittenByRun: true},
		{Label: "Are you willing to complete our onboarding program?", Value: "Yes", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0] != rows[3] || sel[1] != rows[4] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A Yes or No to living in the role's location, answered from the record, is
// not reviewed: shown the address and the posting's place, the review read
// Nebius's "Yes" for Charleston, SC against "United States" as a
// contradiction. A model's answer and another question are still reviewed.
func TestGates_Review_RoleResidenceFromTheRecord(t *testing.T) {
	const q = "Do you currently reside in the location specified for this role? *"
	rows := []ReviewRow{
		{Label: q, Value: "Yes", WrittenByRun: true, FromRecord: true},
		{Label: "Do you live in the area where this role is based?", Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "Yes", WrittenByRun: true},
		{Label: "Are you willing to relocate to the location specified for this role?", Value: "Yes", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0] != rows[2] || sel[1] != rows[3] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A Yes or No to living in places the question names, answered from the
// record, is not reviewed either: the review flagged Gigs's "No" to "Are you
// currently based in New York or San Francisco, and will to work hybrid?" for
// a Charleston address, its own reason saying the applicant lives elsewhere.
func TestGates_Review_NamedPlaceResidenceFromTheRecord(t *testing.T) {
	const q = "Are you currently based in New York or San Francisco, and will to work hybrid?"
	rows := []ReviewRow{
		{Label: q, Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: "Are you located in the New York or SF area?", Value: "Yes", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "No", WrittenByRun: true},
		{Label: "Are you willing to work from our New York office?", Value: "Yes", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0] != rows[2] || sel[1] != rows[3] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// Orum (Ashby, live): a Yes or No that also says whether the applicant would
// relocate ("Yes or willing to relocate"), answered from the record, is not
// reviewed either: the review read the Yes for a Charleston, SC address as a
// contradiction, willingness to relocate not making the applicant located
// there. A model's answer and another value are still reviewed.
func TestGates_Review_NamedPlaceResidenceOrRelocationFromTheRecord(t *testing.T) {
	const q = "Are you currently located in either the San Francisco Bay Area or Austin, Texas?"
	rows := []ReviewRow{
		{Label: q, Value: "Yes or willing to relocate", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "No and not willing to relocate ", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "Yes or willing to relocate", WrittenByRun: true},
		{Label: q, Value: "Yes, I live in Austin", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0] != rows[2] || sel[1] != rows[3] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A Yes or No to needing to relocate for the role, answered from the record,
// is not reviewed: shown the preferred destinations, the review read
// Lightfield's "Yes" to "Do you require relocation to the SF Bay Area?" for a
// Charleston, SC address as a contradiction because San Francisco is one of
// them. A model's answer and a relocation-benefit question are still reviewed.
func TestGates_Review_RelocationNeedFromTheRecord(t *testing.T) {
	const q = "Do you require relocation to the SF Bay Area?"
	rows := []ReviewRow{
		{Label: q, Value: "Yes", WrittenByRun: true, FromRecord: true},
		{Label: "Will you need to relocate for this role?", Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "Yes", WrittenByRun: true},
		{Label: "Do you require relocation assistance?", Value: "Yes", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0] != rows[2] || sel[1] != rows[3] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A Yes or No to a question stating the role's travel share, answered from the
// record, is not reviewed: shown the ceiling, the review read n8n's "No" to
// 30–50% against "up to 25%" as a contradiction. A model's answer, and a
// travel question with no share, are still reviewed.
func TestGates_Review_TravelShareFromTheRecord(t *testing.T) {
	const q = "This role involves travelling approximately 30–50% of the time, primarily across the US, including focused on-site customer engagements. Are you able and willing to meet this requirement?"
	rows := []ReviewRow{
		{Label: q, Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: "Are you willing to travel up to 20% of the time?", Value: "Yes", WrittenByRun: true, FromRecord: true},
		{Label: q, Value: "No", WrittenByRun: true},
		{Label: "Are you willing to travel?", Value: "Yes", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 2 || sel[0] != rows[2] || sel[1] != rows[3] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A security-clearance question is never reviewed: the review is shown no
// citizenship fact (Nominal's "No" for an F-1 student).
func TestGates_Review_Clearance(t *testing.T) {
	rows := []ReviewRow{
		{Label: "Clearance Eligibility - Do you presently hold an active U.S. security clearance, or are you eligible to obtain and maintain a U.S. security clearance?", Value: "No", WrittenByRun: true},
		{Label: "If you have held a U.S. security clearance in the past, what clearance level have you held?", Value: "N/A - have never held a U.S. security clearance", WrittenByRun: true},
		{Label: "Do you have an active TS/SCI?", Value: "No", WrittenByRun: true},
		{Label: "Do you have experience with customs clearance?", Value: "No", WrittenByRun: true},
	}
	if sel := SelectReviewRows(rows); len(sel) != 1 || sel[0].Label != rows[3].Label {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A government employment answer written from the record is never reviewed:
// whether a past employer counts is the user's own reading (Snowflake). The
// same question answered by a model still is.
func TestGates_Review_GovernmentEmploymentFromTheRecord(t *testing.T) {
	const snowflake = "Have you ever been directly employed by (1) any government or military entity, state-owned enterprise, or publicly-funded institution, or (2) a government contractor in a role that recommended Snowflake as part of Government procurement?"
	rows := []ReviewRow{
		{Label: snowflake, Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: "Have you ever been a government employee?", Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: snowflake, Value: "No", WrittenByRun: true},
		{Label: "Do you have experience selling to government agencies?", Value: "No", WrittenByRun: true, FromRecord: true},
	}
	if sel := SelectReviewRows(rows); len(sel) != 2 || sel[0] != rows[2] || sel[1] != rows[3] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// An export-control status is never reviewed: being a "U.S. person" rests on
// citizenship or permanent residence, which the review is not shown, and
// living in the U.S. is not it (Relativity: "I am not a U.S. Person" for an
// F-1 applicant flagged as contradicting a U.S. location).
func TestGates_Review_ExportControl(t *testing.T) {
	rows := []ReviewRow{
		{Label: "EXPORT COMPLIANCE", Value: "I am not a “U.S. Person\"", WrittenByRun: true},
		{Label: "Please identify which statement best applies to you", Value: "I am a U.S. person", WrittenByRun: true},
		{Label: "Are you subject to ITAR restrictions?", Value: "No", WrittenByRun: true},
		{Label: "Do you have experience with export documentation for shipping?", Value: "Yes", WrittenByRun: true},
	}
	if sel := SelectReviewRows(rows); len(sel) != 1 || sel[0].Label != rows[3].Label {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A citizenship question is never reviewed: the review is shown no
// citizenship fact (GRVTY: "Are you a U.S. citizen?" answered No from the
// recorded status for an applicant with a U.S. address).
func TestGates_Review_Citizenship(t *testing.T) {
	rows := []ReviewRow{
		{Label: "This position requires U.S. citizenship pursuant to a government contract. Are you a U.S. citizen?", Value: "No", WrittenByRun: true},
		{Label: "Country of citizenship", Value: "China", WrittenByRun: true},
		{Label: "What is your nationality?", Value: "Chinese", WrittenByRun: true},
		{Label: "Describe your civic engagement", Value: "Volunteer tutor", WrittenByRun: true},
	}
	if sel := SelectReviewRows(rows); len(sel) != 1 || sel[0].Label != rows[3].Label {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A label that asks only for a part of the applicant's own address is never
// reviewed: the review is shown no address fact (Ripple's "Country").
func TestGates_Review_Residence(t *testing.T) {
	for label, want := range map[string]bool{
		"Country": true, "Country / Region": true, "State/Province": true, "City": true, "Location (City)": true,
		"Current Location": true, "Zip Code": true, "Postal Code": true, "Address Line 1": true, "Home address (required)": true,
		// Infinitus and incident.io (Ashby): the applicant's place on a location typeahead.
		"Primary Location": true, "What is your location?": true, "Primary work location": false, "What is your location preference?": false,
		"Email address": false, "Which country would you like to work in?": false, "Country of citizenship": false,
		"Preferred office location": false, "Are you willing to relocate to another city?": false,
	} {
		if got := Residence(label); got != want {
			t.Errorf("%q: %v, want %v", label, got, want)
		}
	}
}

// An age question answered from the record is not reviewed: the answer is the
// date of birth's, which no model is shown, and the review read Sierra's "29"
// against the education dates as a contradiction. A model's age and another
// question are still reviewed.
func TestGates_Review_AgeFromTheRecord(t *testing.T) {
	rows := []ReviewRow{
		{Label: "What is your current age?", Value: "29", WrittenByRun: true, FromRecord: true},
		{Label: "Age range", Value: "25-34", WrittenByRun: true, FromRecord: true},
		{Label: "How old are you?", Value: "29", WrittenByRun: true, FromRecord: true},
		{Label: "What is your current age?", Value: "31", WrittenByRun: true},
		{Label: "How many years of experience do you have with Go?", Value: "3", WrittenByRun: true, FromRecord: true},
		{Label: "Message to the hiring manager", Value: "I have managed teams of all ages.", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 3 || sel[0].Value != "31" || sel[1].Label != rows[4].Label || sel[2].Label != rows[5].Label {
		t.Fatalf("reviewed = %+v", sel)
	}
}

func TestGates_Review_SchoolFromTheRecord(t *testing.T) {
	rows := []ReviewRow{
		{Label: "What college/university did you attend?", Value: "Carver University", WrittenByRun: true, FromRecord: true},
		{Label: "School", Value: "Carver University", WrittenByRun: true, FromRecord: true},
		{Label: "Name of your university", Value: "Carver University", WrittenByRun: true, FromRecord: true},
		// Reducto and Pogo: the schools whose education has ended.
		{Label: "What institution(s) did you graduate from?", Value: "Carver University; University of Westbridge", WrittenByRun: true, FromRecord: true},
		{Label: "What university did you attend for your undergraduate degree?", Value: "Carver University", WrittenByRun: true, FromRecord: true},
		{Label: "What college/university did you attend?", Value: "Carver University", WrittenByRun: true},
		{Label: "Why do you want to work at a school?", Value: "Teaching matters to me.", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 3 || sel[0].Label != rows[4].Label || sel[1].Label != rows[5].Label || sel[2].Label != rows[6].Label {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A major or field of study asked with no degree level, answered from the
// record, is the user's own field entry: the review read Pogo's "What was
// your major/minor?" = "Computer Engineering" (the M.S. field) as wrong because
// the bachelor's major differs, while Reducto's same row passed. A question
// that names a level, a pick from a list, a model's answer, and another
// question are reviewed.
func TestGates_Review_FieldOfStudyFromTheRecord(t *testing.T) {
	rows := []ReviewRow{
		{Label: "What was your major/minor?", Value: "Computer Engineering", TextControl: true, WrittenByRun: true, FromRecord: true},
		{Label: "Field of Study", Value: "Computer Engineering", TextControl: true, WrittenByRun: true, FromRecord: true},
		{Label: "What was your undergraduate major?", Value: "Computer Engineering", TextControl: true, WrittenByRun: true, FromRecord: true},
		{Label: "What was your major/minor?", Value: "Computer Engineering", TextControl: true, WrittenByRun: true},
		{Label: "What is your major accomplishment this year?", Value: "Shipped the search service.", TextControl: true, WrittenByRun: true, FromRecord: true},
		{Label: "Major", Value: "Engineering", WrittenByRun: true, FromRecord: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 4 || sel[0] != rows[2] || sel[1] != rows[3] || sel[2] != rows[4] || sel[3] != rows[5] {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// A start answered from the record is the earliest start date's, which no
// model is shown: the review read Babylist's "Immediately available" against
// the current employment as a contradiction, and Shield AI's "When are you
// available for full time work?" the same way, and Arsenal's bare "Available
// from" (the date thirty days from today) against a job begun in June, and
// n8n's "Notice period / availability details" (the owner's month) the same
// way. A model's start, a question that only mentions starting, and
// availability for an interview are still reviewed.
func TestGates_Review_StartAvailabilityFromTheRecord(t *testing.T) {
	rows := []ReviewRow{
		{Label: "When would you be able to start a new role?", Value: "Immediately available", WrittenByRun: true, FromRecord: true},
		{Label: "Earliest start date", Value: "2026-11-04", WrittenByRun: true, FromRecord: true},
		{Label: "How soon can you start?", Value: "Immediately", WrittenByRun: true, FromRecord: true},
		{Label: "When are you available for full time work?", Value: "Immediately", WrittenByRun: true, FromRecord: true},
		{Label: "When are you available to start your fellowship?", Value: "2026-11-07", WrittenByRun: true, FromRecord: true},
		{Label: "When are you able to start?", Value: "Immediately", WrittenByRun: true, FromRecord: true},
		{Label: "Available from", Value: "12/07/2026", WrittenByRun: true, FromRecord: true},
		{Label: "Availability date *", Value: "2026-12-07", WrittenByRun: true, FromRecord: true},
		{Label: "Date available", Value: "2026-12-07", WrittenByRun: true, FromRecord: true},
		{Label: "Notice period / availability details", Value: "in 1 Month", WrittenByRun: true, FromRecord: true},
		{Label: "What is your notice period?", Value: "in 1 Month", WrittenByRun: true, FromRecord: true},
		{Label: "When can you start full time?", Value: "Immediately", WrittenByRun: true},
		{Label: "What would you want to start building first?", Value: "A billing service.", WrittenByRun: true, FromRecord: true},
		{Label: "When are you available for an interview?", Value: "Weekdays after 5pm ET", WrittenByRun: true, FromRecord: true},
	}
	var reviewed []string
	for _, r := range SelectReviewRows(rows) {
		reviewed = append(reviewed, r.Label)
	}
	want := []string{rows[11].Label, rows[12].Label, rows[13].Label}
	if !slices.Equal(reviewed, want) {
		t.Fatalf("reviewed = %q, want %q", reviewed, want)
	}
}

// A question about the employer's eligible places is not reviewed: the review
// is shown no list of them, and read BILL's "Yes, I currently reside in one of
// the eligible states" for a Charleston, SC address as a contradiction.
func TestGates_Review_EligiblePlaces(t *testing.T) {
	rows := []ReviewRow{
		{Label: "Are you currently located in, or open to relocating to, one BILL's remote-eligible states?", Value: "Yes, I currently reside in one of the eligible states", WrittenByRun: true},
		{Label: "Are you eligible to work in the United States?", Value: "Yes", WrittenByRun: true},
	}
	sel := SelectReviewRows(rows)
	if len(sel) != 1 || sel[0].Label != rows[1].Label {
		t.Fatalf("reviewed = %+v", sel)
	}
}

// Collective (Ashby, live): a No to "Are you currently based in the San
// Francisco Bay Area and able to work from our San Francisco office at least 3
// days per week?" flagged because "the applicant is willing to relocate to San
// Francisco": a move later is no presence now, so the flag is advice. A reason
// that says the applicant lives there still blocks, and so does a Yes.
func TestGates_Review_RelocationIsNoPresence(t *testing.T) {
	const label = "Are you currently based in the San Francisco Bay Area and able to work from our San Francisco office at least 3 days per week?"
	move := Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: "The profile says the applicant is willing to relocate to San Francisco and accepts on-site work"}
	got(t, FlagGate(move, label, ""), GateReviewFlag, Allow, CodeFlagRelocationNotPresence)
	lives := move
	lives.Reason = "The profile address is in San Francisco, and the applicant is willing to relocate"
	got(t, FlagGate(lives, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	yes := move
	yes.Value = "Yes"
	got(t, FlagGate(yes, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(move, "Are you willing to work from our San Francisco office?", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	// Ivo (Ashby, queue198): the present tense alone asks where the applicant
	// is, a model's No for a Charleston applicant.
	const ivo = "Are you located near one of Ivo's US Offices?"
	near := Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: "The applicant is open to relocating to San Francisco, a preferred location, so saying they are not near any Ivo office is contradicted."}
	got(t, FlagGate(near, ivo, ""), GateReviewFlag, Allow, CodeFlagRelocationNotPresence)
	near.Reason = "The profile lists San Francisco as a preferred relocation destination, and the role is in San Francisco."
	got(t, FlagGate(near, ivo, ""), GateReviewFlag, Allow, CodeFlagRelocationNotPresence)
}

// Arketa (Ashby, queue155): the owner's default Yes to "Have you managed,
// operated or taught at a fitness/studio/wellness business?" flagged because
// "The profile does not indicate ...": the default exists because no fact
// settles the question, so the flag is advice. A reason that cites a fact, and
// the same reason on a value that is no default, still block.
func TestGates_Review_AbsenceOnTheOwnersDefaultIsAdvice(t *testing.T) {
	const label = "Have you managed, operated or taught at a fitness/studio/wellness business?"
	def := Flag{Value: "Yes", Probability: 0.9, HasProbability: true, OwnerDefault: true,
		Reason: "The profile does not indicate that the applicant has managed, operated, or taught at a fitness, studio, or wellness business."}
	got(t, FlagGate(def, label, ""), GateReviewFlag, Allow, CodeFlagOwnerDefaultAbsence)
	cited := def
	cited.Reason = "The applicant's only jobs are software engineering roles at Northwind Labs."
	got(t, FlagGate(cited, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	model := def
	model.OwnerDefault = false
	got(t, FlagGate(model, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	// Island (Greenhouse, queue163): "does not support" says the same absence.
	island := Flag{Value: "Yes", Probability: 0.9, HasProbability: true, OwnerDefault: true,
		Reason: "The profile does not support professional low-level systems development or network protocols/topologies experience"}
	got(t, FlagGate(island, "Do you have professional experience with low-level systems development and a strong understanding of network protocols and topologies?", ""), GateReviewFlag, Allow, CodeFlagOwnerDefaultAbsence)
}

// Lyft (Greenhouse, queue165): the required menu under "Please enter your
// relevant employment and military service above using the + Add Another
// Employment link." offers only "Thank you", and the review flagged it as no
// answer to an instruction. The control allows no other value, so the flag is
// advice; the same reason on a value with alternatives still blocks.
func TestGates_Review_AFlagOnTheOnlyOptionIsAdvice(t *testing.T) {
	const label = "Please enter your relevant employment and military service above using the + Add Another Employment link."
	sole := Flag{Value: "Thank you", Probability: 0.9, HasProbability: true, SoleOption: true,
		Reason: "This appears to be an instruction asking for employment and military service details, not a form question with \u201cThank you\u201d as a valid response."}
	got(t, FlagGate(sole, label, ""), GateReviewFlag, Allow, CodeFlagSoleOption)
	other := sole
	other.SoleOption = false
	got(t, FlagGate(other, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
}

// Gremlin (Greenhouse, queue147): the matcher wrote Yes to "Do you have
// professional (paid) experience writing Java?" from the profile's skills,
// and the review, shown a few profile lines without them, flagged it because
// "The profile does not mention Java experience". The review is told an
// unmentioned value is no contradiction; on a value written from the record
// such a flag is advice. A model's value so flagged, and a record-written
// value the reason contradicts, still block.
func TestGates_Review_AnAbsenceFlagOnARecordWrittenValueIsAdvice(t *testing.T) {
	const absent = "The profile does not mention Java experience"
	got(t, FlagGate(Flag{Value: "Yes", Probability: 0.9, HasProbability: true, Reason: absent, FromRecord: true}, "", ""), GateReviewFlag, Allow, CodeFlagRecordAbsence)
	got(t, FlagGate(Flag{Value: "Yes", Probability: 0.9, HasProbability: true, Reason: "No evidence of Java in the profile", FromRecord: true}, "", ""), GateReviewFlag, Allow, CodeFlagRecordAbsence)
	got(t, FlagGate(Flag{Value: "Yes", Probability: 0.9, HasProbability: true, Reason: absent}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "Master's", Probability: 0.9, HasProbability: true, Reason: "The profile lists a bachelor's degree only", FromRecord: true}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	// Footprint (Ashby, queue173): the absence said after what the profile
	// grants. A concession that turns into a contradiction still blocks.
	const granted = "The profile accepts on-site work and is open to relocating to New York, but does not say they can commit to five days a week in an office."
	got(t, FlagGate(Flag{Value: "New York City, San Francisco", Probability: 0.8, HasProbability: true, Reason: granted, FromRecord: true}, "", ""), GateReviewFlag, Allow, CodeFlagRecordAbsence)
	got(t, FlagGate(Flag{Value: "New York City, San Francisco", Probability: 0.8, HasProbability: true, Reason: granted}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "Master's", Probability: 0.9, HasProbability: true, Reason: "The profile lists a bachelor's degree, but does not show a master's; it contradicts the value", FromRecord: true}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "Master's", Probability: 0.9, HasProbability: true, Reason: "The profile lists only a bachelor's degree but does not show a master's", FromRecord: true}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	// Pogo (Ashby, queue194): the user's own startup-experience Yes.
	const startup = "The profile lists experience at Northwind Labs and the Example Institute of Sciences, but does not indicate either is a startup or quant firm."
	got(t, FlagGate(Flag{Value: "Yes", Probability: 0.8, HasProbability: true, Reason: startup, FromRecord: true}, "", ""), GateReviewFlag, Allow, CodeFlagRecordAbsence)
	// Pogo again (queue338): the same absence said as "but no".
	const butNo = "The profile lists software engineering and research experience, but no startup or quant-firm work."
	got(t, FlagGate(Flag{Value: "Yes", Probability: 0.98, HasProbability: true, Reason: butNo, FromRecord: true}, "", ""), GateReviewFlag, Allow, CodeFlagRecordAbsence)
	got(t, FlagGate(Flag{Value: "Yes", Probability: 0.98, HasProbability: true, Reason: butNo}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "Master's", Probability: 0.9, HasProbability: true, Reason: "The profile lists only a bachelor's degree, but no master's", FromRecord: true}, "", ""), GateReviewFlag, Deny, CodeFlagBlocks)
}

// TRM (Ashby, queue149): "Have you previously worked in a remote or hybrid
// environment? Select from the options below." = No, flagged because "The
// profile lists onsite, hybrid, and remote as preferred work modes". A
// preference now is no past arrangement; a reason that names a past remote
// job still blocks, and so does a flag on any other value.
func TestGates_Review_APreferenceIsNoPastArrangement(t *testing.T) {
	const label = "Have you previously worked in a remote or hybrid environment? Select from the options below."
	const pref = "The profile lists onsite, hybrid, and remote as preferred work modes, contradicting the answer that the applicant has never worked so"
	got(t, FlagGate(Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: pref}, label, ""), GateReviewFlag, Allow, CodeFlagPreferenceNotPast)
	got(t, FlagGate(Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: "The applicant worked remotely at Northwind Labs"}, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "Yes, hybrid", Probability: 0.9, HasProbability: true, Reason: pref}, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
	got(t, FlagGate(Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: pref}, "Are you open to hybrid work?", ""), GateReviewFlag, Deny, CodeFlagBlocks)
	// A reason that restates the No ("never worked remotely") names no past
	// job (TRM, a later run of the same posting).
	for _, reason := range []string{
		"The profile lists onsite, hybrid, and remote as preferred work modes, contradicting the answer that they have never worked remotely or hybrid.",
		"The profile prefers remote work, so saying the applicant has not worked remotely is doubtful.",
		"The profile is open to hybrid work, which conflicts with the claim the applicant hasn't ever worked hybrid.",
	} {
		got(t, FlagGate(Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: reason}, label, ""), GateReviewFlag, Allow, CodeFlagPreferenceNotPast)
	}
	got(t, FlagGate(Flag{Value: "No", Probability: 0.9, HasProbability: true, Reason: "The profile prefers remote work and the applicant never worked onsite; they worked remotely at Northwind Labs."}, label, ""), GateReviewFlag, Deny, CodeFlagBlocks)
}

// The owner's decision: a doubted value is marked for review and blocks
// nothing in a fill, while every other failure keeps its verdict.
func TestGates_Readiness_ReviewAdvisoryKeepsAFlagAsAMark(t *testing.T) {
	doubt := LabeledFlag{Label: "Preferred team", Flag: Flag{Value: "Infrastructure", Probability: 0.9, HasProbability: true}}
	page := PageReport{Number: 1, Flags: []LabeledFlag{doubt}}
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{page}, Final: true, ReviewAdvisory: true}).Decision, GateReadiness, Allow, CodeOK)
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{page}, Final: true}).Decision, GateReadiness, Deny, CodeReviewContradiction)
	page.NotHeld = []Item{{Label: "Work authorization", WrongPolarity: true}}
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{page}, Final: true, ReviewAdvisory: true}).Decision, GateReadiness, Deny, CodeValueNotHeld)
}

// A required statement only the user makes, left blank, is a mark in a fill
// and a failure for a run that presses Submit.
func TestGates_Readiness_AUserStatementLeftBlankIsAMarkInAFill(t *testing.T) {
	page := PageReport{Number: 1, RequiredUnanswered: []Item{{Page: 1, Label: "I confirm that I did not use AI.", UserStatement: true}}}
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{page}, Final: true, ReviewAdvisory: true}).Decision, GateReadiness, Allow, CodeOK)
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{page}, Final: true}).Decision, GateReadiness, Deny, CodeRequiredUnanswered)
	page.RequiredUnanswered = append(page.RequiredUnanswered, Item{Page: 1, Label: "Salary"})
	got(t, SubmitReadiness(ReadinessInput{Pages: []PageReport{page}, Final: true, ReviewAdvisory: true}).Decision, GateReadiness, Deny, CodeRequiredUnanswered)
}
