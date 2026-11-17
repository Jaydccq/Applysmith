package match

import (
	"reflect"
	"testing"
)

func TestAnswerBank_NarrativeGate_PatternTable(t *testing.T) {
	for _, s := range []string{
		"Tell us about yourself", "Describe a time you led a team", "Explain your approach", "Walk us through your design",
		"Why are you interested in this role?", "Why do you want to work here?", "How would you handle a deadline?",
		"What motivates you?", "Please share details about a time you built a service",
		"Please provide three examples of accomplishments", "Attach a cover letter", "Additional comments", "In your own words",
		"Briefly describe your role",
		// Kinetic (Ashby, live): a manner asked of a past project is prose.
		"How did you use AI to ship this project?", "How have you approached on-call?",
	} {
		if !isNarrativeText(s) {
			t.Errorf("%q asks for prose", s)
		}
	}
	for _, s := range []string{"Share links to any personal projects", "What is your full name?", "How many years of Python?",
		"How did you hear about us?", "How did you find out about this role?", "How did you learn about Kinetic?"} {
		if isNarrativeText(s) {
			t.Errorf("%q must stay a short field", s)
		}
	}
}

func TestAnswerBank_NarrativeGate_SingleLineAbstains(t *testing.T) {
	years := ent("years-experience-python", "fixed", withAnswer("3"), withExamples("How many years of Python experience do you have?"))
	m := pickMatcher(years)
	// A text control, single-line or long_text, asking for prose is left to the composer.
	for _, q := range []Question{
		textQ("n", "Tell us why you want to work at Example Corp"),
		{Selector: "n", Text: "Tell us why you want to work at Example Corp", Kind: KindText, InputType: InputLongText},
	} {
		r := matchReq(m, q)
		wantAbstain(t, r, ReasonNarrativeDeferred, "")
		if r.Terminal {
			t.Errorf("narrative-deferred is not terminal: %+v", r)
		}
	}
	// A curated stage outranks the guard.
	own := ent("about-me", "fixed", withAnswer("I build things."), withExamples("Tell us about yourself"))
	if r := matchReq(pickMatcher(own), textQ("n", "Tell us about yourself")); r.Answer != "I build things." {
		t.Errorf("%+v", r)
	}
	// An unconfirmed entry's verbatim example does not: the essay-shaped ask is
	// left to the composer.
	unconfirmed := ent("about-me", "fixed", withAnswer("I build things."), withExamples("Tell us about yourself"), notCurated())
	wantAbstain(t, matchReq(pickMatcher(unconfirmed), textQ("n", "Tell us about yourself")), ReasonNarrativeDeferred, "")
}

func TestAnswerBank_NarrativeGate_ExamplesNotYearsEntry(t *testing.T) {
	years := ent("years-experience-python", "fixed", withAnswer("3"), withExamples("How many years of Python experience do you have?"))
	m := pickMatcher(years)
	wantAbstain(t, matchReq(m, textQ("n", "Please give examples that illustrate your proficiency in Python, including years of experience and areas used")), ReasonNarrativeDeferred, "")
	if r := matchReq(m, textQ("n", "How many years of Python experience do you have?")); r.Answer != "3" {
		t.Errorf("the plain quantity still answers: %+v", r)
	}
}

func TestAnswerBank_InputType_NarrativeInfersLongText(t *testing.T) {
	q := prepareQuestion(textQ("n", "Tell us about yourself")).Q
	if q.InputType != InputLongText {
		t.Fatalf("%s", q.InputType)
	}
	if q := prepareQuestion(textQ("n", "What is your start date?")).Q; q.InputType == InputLongText {
		t.Fatalf("%s", q.InputType)
	}
}

func TestAnswerBank_Signature_FullNameOnly(t *testing.T) {
	name := ent(IDApplicantFullName, "fixed", withAnswer("Alex Q Example"))
	name.Placeholder = false
	m := pickMatcher(name)
	for _, q := range []string{"Signature", "Sign your full name", "Electronic signature"} {
		if r := matchReq(m, textQ("s", q)); r.Answer != "Alex Q Example" {
			t.Errorf("%q: %+v", q, r)
		}
	}
	// A blank name abstains: no composed text, no inferred name.
	blank := blankEntry(IDApplicantFullName, KindFixed)
	for _, q := range []string{"Signature", "Initials"} {
		r := matchReq(pickMatcher(blank), textQ("s", q))
		if r.Outcome != OutcomeAbstained {
			t.Errorf("%q: %+v", q, r)
		}
	}
	// Initials come from the same entry; the date is the injected one.
	if r := matchReq(m, textQ("i", "Initials")); r.Answer != "AQE" {
		t.Errorf("%+v", r)
	}
	if r := matchReq(m, textQ("d", "Date*"), func(r *Request) { r.Today = Date{Year: 2031, Month: 3, Day: 4} }); r.Answer != "03/04/2031" {
		t.Errorf("%+v", r)
	}
}

func TestAnswerBank_Statements_CoverLetterAndResume(t *testing.T) {
	m := pickMatcher()
	q := radioQ("Did you submit a cover letter?", "Yes", "No")
	yes := func(r *Request) { r.CoverLetterAttached = boolP(true) }
	r := matchReq(m, q, yes)
	if r.ChosenLabel != "Yes" || r.Source != SourceObservation {
		t.Fatalf("observed: %+v", r)
	}
	for _, mod := range []func(*Request){func(*Request) {}, func(r *Request) { r.CoverLetterAttached = boolP(false) }} {
		r = matchReq(m, q, mod)
		wantAbstain(t, r, ReasonContextPolicy, DetailApplicationStatement)
		if !r.Terminal {
			t.Fatalf("%+v", r)
		}
	}
	// A future-tense intent, or the letter's content, is not a statement about this application.
	if r = matchReq(m, q, func(r *Request) { r.CoverLetterAttached = boolP(false) }); r.Detail != DetailApplicationStatement {
		t.Fatal("setup")
	}
	if r = matchReq(m, radioQ("Will you submit a cover letter?", "Yes", "No")); r.Detail == DetailApplicationStatement {
		t.Fatalf("%+v", r)
	}
	// A user entry that borrows the run-context id never answers it: the
	// observation decides, so an unobserved cover letter is not claimed.
	own := disclosureEntry("cover-letter-submitted", "Yes", "Did you submit a cover letter?")
	if r = matchReq(pickMatcher(own), q); r.Detail != DetailApplicationStatement || r.Outcome != OutcomeAbstained {
		t.Fatalf("same-id user entry: %+v", r)
	}
	if r = matchReq(pickMatcher(own), q, yes); r.Source != SourceObservation || r.ChosenLabel != "Yes" {
		t.Fatalf("same-id user entry, observed: %+v", r)
	}
	// The résumé is observed the same way, and the bank never answers it.
	bank := disclosureEntry("resume-claim", "Yes", "Did you attach a résumé?")
	r = matchReq(pickMatcher(bank), radioQ("Did you attach a résumé?", "Yes", "No"), func(r *Request) { r.ResumeAttached = boolP(false) })
	wantAbstain(t, r, ReasonContextPolicy, DetailApplicationStatement)
	r = matchReq(m, radioQ("Did you attach a resume?", "Yes", "No"), func(r *Request) { r.ResumeAttached = boolP(true) })
	if r.ChosenLabel != "Yes" || r.Source != SourceObservation {
		t.Fatalf("%+v", r)
	}
	// YipitData (Greenhouse, queue165): an instruction to submit a PDF over a
	// sole option saying it was done; every résumé the run attaches is a PDF.
	const pdfOnly = "Please only submit a PDF version of your resume above. Submitting .doc, .pages, or other file types may lead to rendering/formatting errors."
	done := selectQ(pdfOnly, "I have submitted a PDF version of my resume.")
	r = matchReq(m, done, func(r *Request) { r.ResumeAttached = boolP(true) })
	if r.ChosenLabel != "I have submitted a PDF version of my resume." || r.Source != SourceObservation {
		t.Fatalf("PDF statement, observed: %+v", r)
	}
	r = matchReq(m, done, func(r *Request) { r.ResumeAttached = boolP(false) })
	wantAbstain(t, r, ReasonContextPolicy, DetailApplicationStatement)
}

func TestAnswerBank_Statements_AIUsedFuzzyMisrouteNoFill(t *testing.T) {
	ai := ent("application-completed-by-ai", "fixed", withAnswer("No"), withExamples("Was this application completed by AI?"))
	ai.Placeholder = false
	// A user-written entry the curated stages classify answers the statement.
	own := disclosureEntry("ai-used", "No", "Did you use AI to help complete this application?")
	m := pickMatcher(own)
	wantFill(t, matchReq(m, radioQ("Did you use AI to help complete this application?", "Yes", "No")), "No", OutcomeFilled)
	// With no entry the user wrote, the terminal application_statement abstain.
	wantAbstain(t, matchReq(pickMatcher(), radioQ("Did you use AI to help complete this application?", "Yes", "No")), ReasonContextPolicy, DetailApplicationStatement)
	// The acknowledgement the entry's description fuzzily resembles is not a statement.
	r := matchReq(pickMatcher(ai), radioQ("Kindly ensure your application is complete. Please acknowledge.", "Yes", "No"))
	if r.Answer == "No" && r.EntryID == "application-completed-by-ai" {
		t.Fatalf("an acknowledgement is not answered from the AI statement entry: %+v", r)
	}
	// Referral is a statement too.
	wantAbstain(t, matchReq(pickMatcher(), radioQ("Were you referred?", "Yes", "No")), ReasonContextPolicy, DetailApplicationStatement)
	// LiveKit (Ashby, live): a request not to use AI on the application, which a
	// Yes acknowledges, is the applicant's statement; the company's own use is not.
	const pledge = "At LiveKit, we embrace the thoughtful use of AI tools to move faster and work smarter. However, for the application form and during interviews, we ask that you don’t use AI assistance. This allows us to get a clear sense of your genuine interest in LiveKit and how you communicate in your own voice. Please select \"yes\" below to acknowledge."
	wantAbstain(t, matchReq(pickMatcher(), radioQ(pledge, "Yes", "No")), ReasonContextPolicy, DetailApplicationStatement)
	if !IsApplicationStatement("Please do not use ChatGPT or other AI tools to complete this application.") || IsApplicationStatement("We do not use AI to screen your application.") {
		t.Fatal("the request to the applicant, and only it, is a statement")
	}
}

// Kobie (Lever, live): the page asks for the person-made option, which the
// submitter-identity entry's "Myself" would otherwise bind; whether the user
// completed the application is their own statement.
func TestAnswerBank_Statements_CompletingTheApplicationMyselfIsTheUsers(t *testing.T) {
	submitter := ent("application-submitter-identity", "pick-option", withPreferred("I'm the real candidate, submitting this myself", "Myself"), withExamples("Who is submitting this application?"))
	q := required(radioQ(`We include this question to ensure each application is completed by a person.  Please select "I confirm I am completing this application myself"`,
		"I used an AI tool or automated service to complete this application", "I confirm I am completing this application myself", "I had someone else complete this on my behalf"))
	r := matchReq(pickMatcher(submitter), q, withLegal(true))
	wantAbstain(t, r, ReasonContextPolicy, DetailApplicationStatement)
	if !r.Terminal {
		t.Fatalf("a later lane may still answer it: %+v", r)
	}
}

// IsUserStatement names the statements an entry the user wrote answers, so the
// Answer Bank page asks only a question the user's answer settles: given such
// an entry naming the question, the matcher answers from it exactly when
// IsUserStatement says so.
func TestAnswerBank_Statements_IsUserStatementIsWhatTheUsersEntryAnswers(t *testing.T) {
	for _, c := range []struct {
		q    string
		user bool
	}{
		{"Can you confirm that everything in this application is true and your own, including your experience and identity?", true},
		{"Did you use AI to help complete this application?", true},
		{"Were you referred by a current employee?", true},
		{"Did you submit a cover letter?", false},
		{"Did you submit a cover letter written by you?", false},
		{`Did you start your first written answer below with the exact phrase "Northwind Example"?`, false},
	} {
		r := matchReq(pickMatcher(disclosureEntry("mine", "Yes", c.q)), required(radioQ(c.q, "Yes", "No")))
		if answered := r.Outcome == OutcomeFilled && r.EntryID == "mine"; IsUserStatement(c.q) != c.user || answered != c.user {
			t.Errorf("%q: IsUserStatement %t, answered from the user's entry %t (%+v), want %t", c.q, IsUserStatement(c.q), answered, r, c.user)
		}
	}
}

func TestAnswerBank_Meta_VerificationAnswers(t *testing.T) {
	m := pickMatcher()
	for _, c := range []struct{ q, want string }{
		{"Anti-Spam: What is 6 x 7?", "42"},
		{"What is 12 + 30?", "42"},
		{"What is 50 - 8?", "42"},
		{"Human check: what is six times seven?", "42"},
		{"What is the sum of 20 and 22?", "42"},
		{"Which number is bigger, 17 or 42?", "42"},
		{"To verify you are human, type PROCEED", "PROCEED"},
		{`To confirm you are not a bot, enter "banana"`, "banana"},
		{"Anti-spam check: please type the word human", "human"},
		{"How many days are in a week?", "7"},
		{"What color is the sky?", "Blue"},
		{"Are you an LLM?", "No"},
		{"Are you a robot?", "No"},
		{"Are you a human?", "Yes"},
		// Mux (Ashby): a number to spell out in words.
		{"Spell the number '12'", "twelve"},
		{"Please spell out the number 47 in words.", "forty-seven"},
		{"Write the number 305 in words", "three hundred five"},
	} {
		r := matchReq(m, textQ("v", c.q))
		if r.Answer != c.want || r.Source != SourceFormText || r.Outcome != OutcomeFilled {
			t.Errorf("%q: %+v", c.q, r)
		}
	}
	// Not arithmetic, not a gate.
	for _, q := range []string{"Desired salary range, e.g. 100-120", "What is 7 / 2?", "How many days are in a leap february?", "To verify you are human, type something nice",
		"Are you an AI engineer?", "Are you a human resources professional?", "Spell the number of years you have worked", "Spell the number 12345"} {
		if r := matchReq(m, textQ("v", q)); r.Source == SourceFormText {
			t.Errorf("%q: %+v", q, r)
		}
	}
	// No gate applies to a disclosure or consent text.
	for _, q := range []string{`If you have a disability, please type "yes"`, `Type "I AGREE" to consent to electronic signature`} {
		if r := matchReq(m, textQ("v", q)); r.Source == SourceFormText {
			t.Errorf("%q: %+v", q, r)
		}
	}
}

func TestAnswerBank_Meta_VerificationOptions(t *testing.T) {
	m := pickMatcher()
	wantFill(t, matchReq(m, radioQ("What is 6 x 7?", "36", "42", "48", "54")), "42", OutcomeFilled)
	wantFill(t, matchReq(m, radioQ("To filter automated submissions, select option B if you are a human.", "Option A", "Option B", "Option C")), "Option B", OutcomeFilled)
	wantFill(t, matchReq(m, radioQ("To prove you are human, choose the second option", "Select...", "Red", "Green", "Blue")), "Green", OutcomeFilled)
	// Common-knowledge gates are computed from a closed table, not guessed.
	wantFill(t, matchReq(m, radioQ("To prove you are human: how many days are in a week?", "5", "6", "7", "8")), "7", OutcomeFilled)
	// A target not among the options is never guessed.
	if r := matchReq(m, radioQ("To prove you are human, choose the ninth option", "Red", "Green")); r.Source == SourceFormText {
		t.Errorf("%+v", r)
	}
	// An ordinal pick needs a verification phrase.
	if r := matchReq(m, radioQ("Choose the second option", "Red", "Green")); r.Source == SourceFormText {
		t.Errorf("%+v", r)
	}
	// Found Energy: the applicant is a person, whatever the other options are.
	fe := Question{Selector: "q", Kind: KindCheckbox, Text: "Are you an LLM, AI agent, or other associated agentic tool? We are surveying the applicant pool to understand the suite of AI tools that the applicant is using. Select all that apply",
		Options: []Option{{"a", "I am a web based AI agent"}, {"b", "I am a local instance"}, {"c", "I am a human"}, {"d", "I am a cat"}}}
	if r := matchReq(m, fe); r.Outcome != OutcomeFilled || r.Answer != "c" || !reflect.DeepEqual(r.ChosenLabels, []string{"I am a human"}) {
		t.Errorf("%+v", r)
	}
	if r := matchReq(m, radioQ("Are you a bot?", "Bot", "Human being")); r.ChosenLabel != "Human being" || r.Source != SourceFormText {
		t.Errorf("%+v", r)
	}
	// Two such options, or a question that does not ask, are no gate.
	fe.Options = append(fe.Options, Option{"e", "Human"})
	for _, q := range []Question{fe, radioQ("Which best describes you?", "I am a human", "I am a cat"), radioQ("Are you an AI engineer?", "Yes", "Person")} {
		if r := matchReq(m, q); r.Source == SourceFormText {
			t.Errorf("%q: %+v", q.Text, r)
		}
	}
	// The answer is the option's value; the label is what the page shows.
	q := Question{Selector: "q", Text: "What is 6 x 7?", Kind: KindRadioGroup, Options: []Option{{"a", "36"}, {"b", "42"}}}
	if r := matchReq(m, q); r.Answer != "b" || r.ChosenLabel != "42" {
		t.Errorf("%+v", r)
	}
}

func TestAnswerBank_Meta_ArithmeticHelpers(t *testing.T) {
	for in, want := range map[string]int{"7": 7, "twenty-one": 21, "ninety": 90, "hundred": 100, "sixteen": 16, "twenty one": 21} {
		if got, ok := parseNumber(in); !ok || got != want {
			t.Errorf("parseNumber(%q) = %d %t", in, got, ok)
		}
	}
	if _, ok := arithmeticAnswer("What is 3 - 8?"); ok {
		t.Error("a negative result is refused")
	}
	if _, ok := arithmeticAnswer("What is 7 / 2?"); ok {
		t.Error("a non-integer result is refused")
	}
	if n, ok := arithmeticAnswer("Solve: 9 minus 4"); !ok || n != 5 {
		t.Errorf("minus: %d %t", n, ok)
	}
	if len(triviaTable) != 23 {
		t.Errorf("23 common-knowledge gates: %d", len(triviaTable))
	}
}

func TestAnswerBank_Slot_ContextPoliciesGuardPickOptionToo(t *testing.T) {
	// A work-authorization entry held as a pick-option is guarded by the profile
	// family's context policies through the ContextPolicies slot.
	e := ent("work-auth-us", "pick-option", withPreferred("Yes"), withExamples("Are you authorized to work in Canada?"))
	e.Placeholder = false
	r := matchReq(pickMatcher(e), radioQ("Are you authorized to work in Canada?", "Yes", "No"))
	wantAbstain(t, r, ReasonContextPolicy, DetailPostingCountry)
}

// The role this application is for is the kind the posting title names
// (Two Sigma); a title or an option of no recognised kind leaves it.
func TestAnswerBank_Statement_RoleAppliedForIsThePostingsKind(t *testing.T) {
	const q = "Confirm which role you are interested in applying for"
	titled := func(title string) func(*Request) { return func(r *Request) { r.Posting.Title = title } }
	for _, c := range []struct {
		title string
		opts  []string
		want  string // empty: nothing is chosen
	}{
		{"Software Engineering Full-Time Campus Hire (NYC)", []string{"Select an option", "Internship", "Campus Fulltime Role"}, "Campus Fulltime Role"},
		{"Software Engineering Intern (Summer)", []string{"Select an option", "Internship", "Campus Fulltime Role"}, "Internship"},
		{"Software Engineer", []string{"Internship", "Campus Fulltime Role"}, ""},
		{"Software Engineering Full-Time Campus Hire (NYC)", []string{"Internship", "Full-time", "Part-time"}, ""},
	} {
		r := matchReq(pickMatcher(), selectQ(q, c.opts...), titled(c.title))
		if r.ChosenLabel != c.want || c.want != "" && (r.Outcome != OutcomeFilled || r.Source != SourceObservation) {
			t.Errorf("%q over %v: chose %q (%s %s), want %q", c.title, c.opts, r.ChosenLabel, r.Outcome, r.Source, c.want)
		}
	}
}
