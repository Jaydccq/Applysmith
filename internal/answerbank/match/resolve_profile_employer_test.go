package match

import (
	"strings"
	"testing"
)

func dEmployer(question string, mods ...func(*Entry)) Entry {
	e := Entry{ID: "former-employer", Kind: KindEmployerHistory,
		Body: Body{AnswerIfMatch: "Yes", AnswerIfNoMatch: "No"},
		Meta: Meta{Examples: []string{question}}}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func employerMatcher(employers []Employer, e Entry) *Matcher {
	return New(Bank{Employers: employers, Entries: []Entry{e}}, pfStages())
}

func TestAnswerBank_EmployerHistory_BankAndProfileEmployersSentinelsSkipped(t *testing.T) {
	bankEmployers := []Employer{
		{Name: "Initech Example", Aliases: []string{"Initech", "N/A"}},
		{Name: "none"},
		{Name: ""},
	}
	f := alexFacts()
	f.Employment = append(f.Employment, EmploymentFact{Employer: "Not currently employed"}, EmploymentFact{Employer: "  "})
	tests := []struct {
		name, text, want string
	}{
		{"profile employer", "Are you a current or former employee of Example Corp or its subsidiaries?", "Yes"},
		{"another profile employer, case-insensitive", "Have you worked for ACME TEST before?", "Yes"},
		{"bank employer alias", "Have you worked for Initech?", "Yes"},
		{"unlisted company", "Have you worked for Globex Example before?", "No"},
		{"token inside a longer word is no match", "Have you worked for Example Corporation Holdings before?", "No"},
		{"sentinel values never match", "Have you been employed or currently unemployed, with none of the above or N/A?", "No"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := employerMatcher(bankEmployers, dEmployer(tc.text))
			r := pfOne(t, m, f, Date{}, yesNoQ("s", tc.text))
			if r.Outcome == OutcomeAbstained || r.Answer != tc.want {
				t.Fatalf("%+v", r)
			}
		})
	}
	for _, v := range []string{"", "N/A", "n a", "None", "not applicable", "Unemployed", "NULL", "  "} {
		if !emplIsNoCompany(v) {
			t.Errorf("%q should be a no-company value", v)
		}
	}
	if emplIsNoCompany("Example Corp") {
		t.Error("a real employer")
	}
}

// The bank's employers are the ones the user worked for at some time: a
// question about now is answered for one only by a job of the history, Yes
// when it goes on and No when it ended. Without such a job it is left.
func TestAnswerBank_EmployerHistory_ABankEmployerIsCurrentOnlyByTheHistory(t *testing.T) {
	const q = "Are you currently employed by Initech?"
	m := employerMatcher([]Employer{{Name: "Initech Example", Aliases: []string{"Initech"}}}, dEmployer(q))
	job := func(end string) Facts {
		return Facts{Employment: []EmploymentFact{{Employer: "Initech Example Inc.", Start: "2019-01", End: end}}}
	}
	if r := pfOne(t, m, Facts{}, pfToday, yesNoQ("s", q)); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Errorf("no job in the history: %+v", r)
	}
	for end, want := range map[string]string{"2020-06": "No", "present": "Yes"} {
		if r := pfOne(t, m, job(end), pfToday, yesNoQ("s", q)); r.Outcome == OutcomeAbstained || r.Answer != want {
			t.Errorf("a job that ends %s: %+v, want %s", end, r, want)
		}
	}
}

func TestAnswerBank_EmployerHistory_AbstainIfAmbiguousWhenNameOnlyInOption(t *testing.T) {
	const q = "Which company do you work for now?"
	employers := []Employer{{Name: "Example Corp"}}
	q1 := pfSelect("s", q, "Example Corp", "Other")
	m := employerMatcher(employers, dEmployer(q))
	r := pfOne(t, m, Facts{}, Date{}, q1)
	if r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Fatalf("%+v", r)
	}
	off := false
	m = employerMatcher(employers, dEmployer(q, func(e *Entry) { e.AbstainIfAmbiguous = &off }))
	if r := pfOne(t, m, Facts{}, Date{}, pfSelect("s", q, "Yes", "No")); r.Answer != "No" {
		t.Fatalf("no employer in the options: %+v", r)
	}
	// A name present in the question as well as the option is not ambiguous.
	const both = "Have you worked for Example Corp before?"
	m = employerMatcher(employers, dEmployer(both))
	if r := pfOne(t, m, Facts{}, Date{}, pfSelect("s", both, "Yes", "No")); r.Answer != "Yes" {
		t.Fatalf("%+v", r)
	}
}

// Owner decision: Yes when the named company ("this company": the posting's)
// is in the employers list or history, else No, also in a text box asking it.
func TestAnswerBank_EmployerHistory_AnswersFromTheHistory(t *testing.T) {
	yn := func(e *Entry) {
		e.AnswerIfMatch, e.AnswerIfNoMatch, e.InputTypes = "Yes", "No", []string{"yes_no", "single_select"}
	}
	m := New(bankOf(ent("employer-history", "employer-history", yn), ent("currently-employed-by-this-company", "employer-history", yn)), DefaultStages())
	jobs := []EmploymentFact{{Employer: "PwC", End: "2020-06"}, {Employer: "Globex Inc.", End: "present"}}
	for _, c := range []struct{ co, q, want string }{ // want "" = not answered
		{"SN", "Have you ever worked at SN in any capacity, e.g., full-time/part-time employee, independent contractor?", "No"},
		{"SN", "Are you currently employed or have you ever been employed by PwC?", "Yes"},
		{"", "Are you currently employed by Globex?", "Yes"}, {"", "Are you currently employed by PwC?", "No"},
		{"Globex", "Are you currently employed by this company?", "Yes"}, {"SN", "Are you currently employed by this company?", "No"},
		{"", "Are you currently employed by this company?", ""}, {"SN", "If you are a current employee, please provide your employee ID", ""},
		{"SN", "Only a current employee is sponsored. Do you require sponsorship?", ""},
		{"SN", "Within the past 5 years, have you ever been an employee of the U.S. Federal Government?", ""},
		{"SN", "Have you ever worked at SN?", "I have not worked at SN"},
		// Compeer Financial (Greenhouse, live): "team member" is the employer's word for employee.
		{"Compeer Financial", "Are you a previous team member of Compeer Financial?", "No"},
		{"", "Are you a previous team member of PwC?", "Yes"},
	} {
		q := textQ("s", c.q)
		if strings.HasPrefix(c.want, "I ") {
			q = pfSelect("s", c.q, "Yes", c.want)
		}
		resp, err := m.Match(Request{Host: "x.example", Posting: Posting{Company: c.co}, Facts: Facts{Employment: jobs}, Questions: []Question{q}})
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.Results[0]; (r.Outcome == OutcomeAbstained || !strings.Contains(r.EntryID, "employ")) != (c.want == "") || c.want != "" && r.Answer != c.want {
			t.Errorf("%q at %q: want %q: %+v", c.q, c.co, c.want, r)
		}
	}
}

// The company-relationship lane answers only from the relationships the user
// recorded and only under a current completeness statement; everything else
// falls through to the later lanes with the reason kept in the trace.

func relHistoryEntry() Entry {
	return ent("employer-history", "employer-history", func(e *Entry) { e.AnswerIfMatch, e.AnswerIfNoMatch = "Yes", "No" })
}

func relFacts(complete string, rels ...RelationshipFact) Facts {
	f := Facts{Employment: []EmploymentFact{{Employer: "Globex Inc.", Title: "Engineer", Start: "2021-01", End: "present"}}, Relationships: rels}
	if complete != "" {
		f.RelationshipsComplete = map[RelationshipFamily]string{FamilyEmployment: complete, FamilyRelative: complete, FamilyPriorApplication: complete}
	}
	return f
}

func relMatch(t *testing.T, f Facts, company string, q Question) Result {
	t.Helper()
	resp, err := New(bankOf(relHistoryEntry()), DefaultStages()).Match(Request{Host: "jobs.example.com", Posting: Posting{Company: company}, Facts: f, Questions: []Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0]
}

func TestMatcher_RelationshipLaneUnresolvedFallsThrough(t *testing.T) {
	q := yesNoQ("s", "Have you ever worked for Acme Corp before?")
	t.Run("no completeness statement: the reason is traced and the employer-history entry answers", func(t *testing.T) {
		r := relMatch(t, relFacts(""), "", q)
		if !traceHas(r, "relationship:absence-without-attestation") {
			t.Fatalf("the trace lost the lane's reason: %v", r.Trace)
		}
		if r.Terminal || r.Source == SourceRelationship || r.EntryID != "employer-history" || r.ChosenLabel != "No" {
			t.Fatalf("a later lane did not answer: %+v", r)
		}
	})
	t.Run("a recorded relationship without a statement is still unresolved", func(t *testing.T) {
		r := relMatch(t, relFacts("", RelationshipFact{Family: FamilyEmployment, Company: "Acme", Recorded: "2026-09-01"}), "", q)
		if !traceHas(r, "relationship:relationship-without-attestation") || r.Source == SourceRelationship {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a statement older than the newest relationship is stale", func(t *testing.T) {
		r := relMatch(t, relFacts("2026-08-01", RelationshipFact{Family: FamilyRelative, Company: "Initech", Recorded: "2026-09-01"}),
			"", yesNoQ("s", "Do you have any relatives employed at Initech?"))
		if !traceHas(r, "relationship:absence-without-attestation") && !traceHas(r, "relationship:relationship-without-attestation") {
			t.Fatalf("%+v", r)
		}
		if r.Source == SourceRelationship {
			t.Fatalf("a stale statement answered: %+v", r)
		}
	})
	t.Run("a pronoun with no application company is unresolved, never a guess", func(t *testing.T) {
		r := relMatch(t, relFacts("2026-10-01"), "", yesNoQ("s", "Have you previously worked for this company?"))
		if !traceHas(r, "relationship:pronoun-without-application-company") || r.Source == SourceRelationship {
			t.Fatalf("%+v", r)
		}
	})
}

func TestMatcher_RelationshipLaneResolvesUnderAttestation(t *testing.T) {
	f := relFacts("2026-10-01",
		RelationshipFact{Family: FamilyEmployment, Company: "Acme Corporation", Aliases: []string{"Acme"}, Recorded: "2026-09-01"},
		RelationshipFact{Family: FamilyRelative, Company: "Initech", Person: "Sam Example", Relation: "Sibling", Current: true, Recorded: "2026-09-01"})
	cases := []struct {
		name, company string
		q             Question
		label, answer string
	}{
		{"a recorded former employer", "", yesNoQ("s", "Have you ever worked for Acme before?"), "Yes", ""},
		{"absence under the statement", "", yesNoQ("s", "Have you ever worked for Umbrella before?"), "No", ""},
		{"a former employer is not a current one", "", yesNoQ("s", "Are you currently employed by Acme?"), "No", ""},
		// HP (Workday, live): "existing" is now, and the company comes before "employee".
		{"an existing employee is a current one", "", yesNoQ("s", "Are you an existing Acme employee?"), "No", ""},
		{"HP's wording behind a note on personal data", "", yesNoQ("s", "To help us understand your preferences related to personal data and uses for future recruitment processes, are you an existing Umbrella employee?"), "No", ""},
		{"the profile's current job, through the posting's company", "Globex", yesNoQ("s", "Do you currently work for this company?"), "Yes", ""},
		{"no literal No: Not Applicable", "", Question{Selector: "s", Kind: KindCheckbox, Text: "Have you previously worked at Umbrella in any capacity? (Select all that apply)",
			Options: opts("I currently work at Umbrella", "I previously worked at Umbrella", "Not Applicable")}, "Not Applicable", ""},
		{"a relative's name", "", textQ("s", "Please provide the name of your relative who works at Initech."), "", "Sam Example"},
		// Qualtrics (Greenhouse, live): the follow-up asks for the name and the relationship together.
		{"a relative's name and relationship", "", textQ("s", "If you answered yes to being related to a current Initech employee, please specify name and relationship (N/A if not applicable)."), "", "Sam Example (Sibling)"},
		{"a prior application to the posting's company", "Hooli", yesNoQ("s", "Have you applied here before?"), "No", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := relMatch(t, f, tc.company, tc.q)
			if r.Source != SourceRelationship || r.Outcome != OutcomeFilled || r.ChosenLabel != tc.label || (tc.answer != "" && r.Answer != tc.answer) {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// Qualtrics (Greenhouse, live): under a complete relatives list that holds no
// one at the company, the text box that follows "Are you related to a current
// employee?" got the gate's "No". A box that asks for the relative's name, or
// any "if yes" follow-up, is never answered Yes or No; the lane leaves it to
// the bank's relatives list.
func TestMatcher_RelationshipLaneNeverAnswersAFollowUpBoxYesOrNo(t *testing.T) {
	f := relFacts("2026-10-01")
	for _, text := range []string{
		"If you answered yes to being related to a current Qualtrics employee, please specify name and relationship (N/A if not applicable).",
		"If yes, please list the names of your relatives employed by Qualtrics.",
		"If you answered yes, please provide the name of the Qualtrics employee you are related to.",
	} {
		r := relMatch(t, f, "Qualtrics", textQ("s", text))
		if r.Source == SourceRelationship || r.Answer == "No" {
			t.Fatalf("%q was answered by the lane: %+v", text, r)
		}
	}
}

// Boeing (Taleo, live): "Internal Candidate" asks whether the user works for
// the posting's company now, and the BEMSID box says to type N/A when it does
// not apply. Both are read from the attested employment list; a company the
// user did work for leaves the ID to the user.
func TestMatcher_RelationshipLaneInternalCandidateAndTheCompanyID(t *testing.T) {
	const bemsid = "Boeing BEMSID (type N/A if not applicable)"
	internal := pfSelect("s", "Internal Candidate", "Please select", "No", "Yes")
	former := RelationshipFact{Family: FamilyEmployment, Company: "Boeing", Recorded: "2026-09-01"}
	for _, tc := range []struct {
		name string
		f    Facts
		q    Question
		want string // "" = not answered by the lane
	}{
		{"not an employee: the ID is N/A", relFacts("2026-10-01"), textQ("s", bemsid), "N/A"},
		{"not an employee: no internal candidate", relFacts("2026-10-01"), internal, "No"},
		{"a former employee is no internal candidate", relFacts("2026-10-01", former), internal, "No"},
		{"a current employee is one", relFacts("2026-10-01"), internal, "Yes"},
		{"a former employee's ID stays the user's", relFacts("2026-10-01", former), textQ("s", bemsid), ""},
		{"no completeness statement", relFacts(""), textQ("s", bemsid), ""},
		{"the ID of another company", relFacts("2026-10-01"), textQ("s", "Lockheed LMID (type N/A if not applicable)"), ""},
		{"no N/A instruction", relFacts("2026-10-01"), textQ("s", "Boeing BEMSID"), ""},
		{"an ID asked in a choice", relFacts("2026-10-01"), pfSelect("s", bemsid, "N/A", "Other"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			company := "Boeing"
			if tc.name == "a current employee is one" {
				company = "Globex"
			}
			r := relMatch(t, tc.f, company, tc.q)
			got := r.Answer
			if r.ChosenLabel != "" {
				got = r.ChosenLabel
			}
			if (r.Source == SourceRelationship) != (tc.want != "") || tc.want != "" && got != tc.want {
				t.Fatalf("want %q: %+v", tc.want, r)
			}
		})
	}
}

// SpaceX (Greenhouse, live): a label names the posting's company and the
// "never" option lists its affiliates; the never statement is chosen only when
// no company it lists is recorded.
func TestMatcher_RelationshipLaneLabelNamingThePostingCompany(t *testing.T) {
	q := pfSelect("s", "SpaceX & SpaceXAI Employment History",
		"I have never worked for SpaceX, SpaceXAI, xAI, X, or Twitter",
		"I am a former SpaceX, SpaceXAI, xAI, X, or Twitter employee",
		"I am a current SpaceX employee")
	never := "I have never worked for SpaceX, SpaceXAI, xAI, X, or Twitter"
	if r := relMatch(t, relFacts("2026-10-01", RelationshipFact{Family: FamilyEmployment, Company: "Acme Corporation", Recorded: "2026-09-01"}), "SpaceX", q); r.Source != SourceRelationship || r.ChosenLabel != never {
		t.Fatalf("never worked there: %+v", r)
	}
	if r := relMatch(t, relFacts("2026-10-01", RelationshipFact{Family: FamilyEmployment, Company: "Twitter", Recorded: "2026-09-01"}), "SpaceX", q); r.ChosenLabel == never {
		t.Fatalf("a recorded affiliate is in the never statement: %+v", r)
	}
	if r := relMatch(t, relFacts(""), "SpaceX", q); r.Source == SourceRelationship {
		t.Fatalf("no completeness statement: %+v", r)
	}
	// SoFi (Greenhouse, live): the company and its subsidiary named before
	// "employee", the subsidiary's former name in brackets.
	current := yesNoQ("s", "Are you currently a SoFi or SoFi Tech Solutions (formerly Galileo) employee?")
	if r := relMatch(t, relFacts("2026-10-01", RelationshipFact{Family: FamilyEmployment, Company: "Acme Corporation", Recorded: "2026-09-01"}), "SoFi", current); r.Source != SourceRelationship || r.ChosenLabel != "No" {
		t.Fatalf("not a current employee: %+v", r)
	}
	// Rebar (Ashby, live): naming the company's office asks about attendance.
	office := yesNoQ("s", "Are you capable - and excited - to work from Rebar's NYC office (Union Square area) 5 days/week? There are of course exceptions given travel, health, personal appointments, and such.")
	if r := relMatch(t, relFacts("2026-10-01"), "Rebar", office); r.Source == SourceRelationship {
		t.Fatalf("an office attendance question: %+v", r)
	}
}

func TestMatcher_RelationshipLaneNeverClaimsWorkEligibility(t *testing.T) {
	f := relFacts("2026-10-01")
	for _, text := range []string{
		"Are you legally authorized to work for Acme in the United States?",
		"Will you now or in the future require sponsorship to work for Acme?",
		"Are you eligible to work in the United States of America?",
		"Do you have working knowledge of Node.js?",
	} {
		r := relMatch(t, f, "Acme", yesNoQ("s", text))
		if r.Source == SourceRelationship || traceHas(r, "relationship:") || traceHas(r, "relationship: ") {
			t.Fatalf("%q was read as a relationship question: %+v", text, r)
		}
	}
	// Trend Micro (Workday, live): a capitalised label word after "of" names no company.
	if IsRelationshipQuestion("Desired Type of Employment") || !IsRelationshipQuestion("Have you previously worked at Acme?") {
		t.Fatal("a label word read as a company, or a company no longer read")
	}
}

// LiveKit (Ashby, live): a notice that names the company and its interviews
// and asks for a Yes to acknowledge it is no prior-interview question, even
// when the record holds an interview with the company.
func TestMatcher_RelationshipLaneLeavesAnAcknowledgementThatMentionsInterviews(t *testing.T) {
	const notice = "At Acme, we embrace the thoughtful use of AI tools to move faster and work smarter. However, for the application form and during interviews, we ask that you don't use AI assistance. This allows us to get a clear sense of your genuine interest in Acme and how you communicate in your own voice. Please select \"yes\" below to acknowledge."
	f := relFacts("2026-10-01", RelationshipFact{Family: FamilyPriorInterview, Company: "Acme", Recorded: "2026-09-01"})
	r := relMatch(t, f, "Acme", yesNoQ("s", notice))
	if r.Source == SourceRelationship || traceHas(r, "relationship:") || IsRelationshipQuestion(notice) {
		t.Fatalf("the notice was read as a prior-interview question: %+v", r)
	}
	if !IsRelationshipQuestion("Have you interviewed with Acme before?") {
		t.Fatal("the prior-interview question is no longer read")
	}
	// Oklo (Greenhouse, queue173): "If selected for ..." is a hypothetical
	// interview, as "should you be selected" is.
	if q := "If selected for a preliminary interview, you may be asked to work through a Python coding problem. Do you acknowledge this?"; IsRelationshipQuestion(q) {
		t.Fatalf("a hypothetical interview was read as a prior one: %q", q)
	}
}

// PathAI (Greenhouse): "If you do have any relatives currently employed by
// PathAI, please list them here:" applies only if there is one. Under an
// attested list without one, a required box says N/A and an optional one is
// left blank; never the gate's bare No. A recorded relative is listed by name.
func TestMatcher_RelationshipListBoxUnderItsOwnCondition(t *testing.T) {
	const text = "If you do have any relatives currently employed by PathAI, please list them here:"
	none := relFacts("2026-10-01")
	opt := textQ("s", text)
	if r := relMatch(t, none, "PathAI", opt); r.Outcome != OutcomeAbstained || r.Answer != "" {
		t.Fatalf("optional: left blank, got %+v", r)
	}
	req := textQ("s", text)
	req.Required = true
	if r := relMatch(t, none, "PathAI", req); r.Answer != "N/A" || r.Source != SourceRelationship {
		t.Fatalf("required: N/A, got %+v", r)
	}
	sib := relFacts("2026-10-01", RelationshipFact{Family: FamilyRelative, Company: "PathAI", Person: "Sam Example", Relation: "Sibling", Current: true, Recorded: "2026-09-01"})
	if r := relMatch(t, sib, "PathAI", opt); r.Answer != "Sam Example" {
		t.Fatalf("a recorded relative: listed by name, got %+v", r)
	}
}

// Snowflake (Ashby, live): "Have you ever been directly employed by (1) any
// government or military entity, state-owned enterprise, or publicly-funded
// institution, or (2) a government contractor in a role that recommended
// Snowflake as part of Government procurement?" asks about kinds of employer,
// not a company: the lane read "Government procurement" as one and answered
// No from the attested list, which names employers, never whether they are
// publicly funded. The lane leaves it; by the owner's word the bank's
// government employment answer is the answer to such a list of public
// employers (route 611).
func TestMatcher_RelationshipLaneLeavesKindsOfEmployer(t *testing.T) {
	const text = "Have you ever been directly employed by (1) any government or military entity, state-owned enterprise, or publicly-funded institution, or (2) a government contractor in a role that recommended Snowflake as part of Government procurement?"
	gov := ent("government-employee-history", "fixed", withAnswer("No"), withTypes(InputYesNo), withExamples("Have you ever been a government employee?"))
	resp, err := New(bankOf(relHistoryEntry(), gov), DefaultStages()).Match(Request{Host: "jobs.example.com", Posting: Posting{Company: "Snowflake"}, Facts: relFacts("2026-10-01"), Questions: []Question{yesNoQ("s", text)}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.EntryID != "government-employee-history" || r.ChosenLabel != "No" {
		t.Fatalf("a kind of employer was not the government employment answer: %+v", r)
	}
}
