package match

import (
	"strings"
	"testing"
)

func TestAnswerBank_USPerson_OwnAnswerElseTheSponsorshipNeed(t *testing.T) {
	need := func(now, fut bool) func(*Request) {
		return func(r *Request) {
			r.Facts.RequiresSponsorshipNow, r.Facts.RequiresSponsorshipFuture = boolP(now), boolP(fut)
		}
	}
	blank := ent(IDUSPerson, "fixed", withTypes(InputYesNo))
	blank.Placeholder = true
	own := ent(IDUSPerson, "fixed", withTypes(InputYesNo))
	own.Answer = "Yes"
	statements := selectQ("EXPORT COMPLIANCE",
		"I am a U.S. Person (U.S. citizen, lawful permanent resident, refugee or asylee)",
		"I am not a U.S. Person",
		"Prefer not to say")
	// Live shape (Relativity, Greenhouse): the label is the section title and
	// the options state the status.
	wantFill(t, matchReq(pickMatcher(blank), statements, need(false, true)), "I am not a U.S. Person", OutcomeFilled)
	wantFill(t, matchReq(pickMatcher(), statements, need(true, true)), "I am not a U.S. Person", OutcomeFilled)
	yesNo := radioQ("Are you a U.S. person as defined by ITAR?", "Yes", "No")
	wantFill(t, matchReq(pickMatcher(blank), yesNo, need(true, true)), "No", OutcomeFilled)
	// The user's own answer wins over the derivation.
	wantFill(t, matchReq(pickMatcher(own), yesNo, need(true, true)), "Yes", OutcomeFilled)
	wantFill(t, matchReq(pickMatcher(own), statements, need(true, true)), "I am a U.S. Person (U.S. citizen, lawful permanent resident, refugee or asylee)", OutcomeFilled)
	// No sponsorship need does not make a citizen: never answered from it.
	wantAbstain(t, matchReq(pickMatcher(blank), yesNo, need(false, false)), ReasonPlaceholderEmpty, "")
	if r := matchReq(pickMatcher(blank), statements, need(false, false)); r.Outcome == OutcomeFilled {
		t.Fatalf("no sponsorship need answered %q", r.Answer)
	}
	// Two "not a U.S. person" statements (license variants) are the user's.
	licence := selectQ("Export control status", "I am a U.S. person", "I am not a U.S. person and hold an export license", "I am not a U.S. person and do not hold an export license")
	wantAbstain(t, matchReq(pickMatcher(blank), licence, need(true, true)), ReasonAmbiguous, "")
	// Hadrian (Ashby, live): each option leads with its status; the U.S. person
	// definition's "granted, not pending" negates no status.
	hadrian := radioQ("ITAR status",
		"U.S. person. This ITAR/EAR status includes U.S. citizens, U.S. nationals, lawful permanent residents (green card holders), and asylees and refugees with such status granted, not pending",
		"Foreign person. This ITAR/EAR status includes anyone who is not a U.S. person (see above).")
	wantFill(t, matchReq(pickMatcher(blank), hadrian, need(false, true)), hadrian.Options[1].Label, OutcomeFilled)
	wantFill(t, matchReq(pickMatcher(own), hadrian, need(true, true)), hadrian.Options[0].Label, OutcomeFilled)
}

func TestAnswerBank_USPerson_ABareYesOrNoFollowsTheQuestionsPolarity(t *testing.T) {
	need := func(r *Request) {
		r.Facts.RequiresSponsorshipNow, r.Facts.RequiresSponsorshipFuture = boolP(true), boolP(true)
	}
	blank := ent(IDUSPerson, "fixed", withTypes(InputYesNo))
	blank.Placeholder = true
	own := ent(IDUSPerson, "fixed", withTypes(InputYesNo))
	own.Answer = "Yes"
	// A question that asks the opposite status takes the complement.
	for _, text := range []string{
		"Are you a non-U.S. person?",
		"Are you a Non-U.S. Person under ITAR?",
		"Are you a foreign person as defined by ITAR (i.e. not a U.S. person)?",
		"For export control purposes, are you a foreign national? U.S. persons are citizens, lawful permanent residents, refugees and asylees.",
	} {
		wantFill(t, matchReq(pickMatcher(own), radioQ(text, "Yes", "No")), "No", OutcomeFilled)
		wantFill(t, matchReq(pickMatcher(blank), radioQ(text, "Yes", "No"), need), "Yes", OutcomeFilled)
	}
	if r := matchReq(pickMatcher(own), textQ("t", "Are you a non-U.S. person?")); r.Outcome != OutcomeFilled || r.Answer != "No" {
		t.Fatalf("a typed answer to the opposite status: %+v", r)
	}
	// A note beside the question that says who is not a U.S. person (Okta)
	// leaves the question as asked.
	okta := "Are you a U.S. Person (U.S. Citizen, National, Lawful Permanent Resident, Refugee, or Asylee), and can you provide documentation establishing U.S. Person status upon hire? Please note: Candidates who require visa sponsorship (e.g. H-1B, F-1, O-1, L-1, etc.) are not U.S. Persons."
	wantFill(t, matchReq(pickMatcher(own), radioQ(okta, "Yes", "No")), "Yes", OutcomeFilled)
	wantFill(t, matchReq(pickMatcher(blank), radioQ(okta, "Yes", "No"), need), "No", OutcomeFilled)
	// A question that names both statuses, or negates before it names one, is
	// not read.
	for _, text := range []string{
		"Are you a U.S. person or a foreign person under ITAR?",
		"Is there any reason you would not be considered a U.S. person under ITAR?",
	} {
		if r := matchReq(pickMatcher(own), radioQ(text, "Yes", "No")); r.Outcome == OutcomeFilled {
			t.Errorf("%q answered %q", text, r.ChosenLabel)
		}
	}
	// Options that state the status need no reading of the question.
	statements := selectQ("We cannot share controlled technology with non-U.S. persons. Select your status:", "I am a U.S. Person", "I am not a U.S. Person")
	wantFill(t, matchReq(pickMatcher(own), statements), "I am a U.S. Person", OutcomeFilled)
}

func TestAnswerBank_USPerson_ClassListNoneOfTheAbove(t *testing.T) {
	need := func(now, fut bool) func(*Request) {
		return func(r *Request) {
			r.Facts.RequiresSponsorshipNow, r.Facts.RequiresSponsorshipFuture = boolP(now), boolP(fut)
		}
	}
	blank := ent(IDUSPerson, "fixed", withTypes(InputYesNo))
	blank.Placeholder = true
	state := ent("address-state", "pick-option", withPreferred("Example State"))
	// Astranis (Greenhouse): the classes of U.S. person, and none of them; the
	// verb "state" in the question is not the address state.
	q := selectQ("Astranis complies with U.S. Government space technology export regulations, therefore will you state which of the following applies to you:",
		"I am a U.S. Citizen.", "I am a lawful permanent resident of the U.S. and Green Card Holder.",
		"I am a refugee under 8 U.S.C. 1157.", "I am an asylee under 8 U.S.C. 1158.", "None of the above.")
	q.Kind = KindCustomListbox
	m := pickMatcher(blank, state)
	wantFill(t, matchReq(m, q, need(false, true)), "None of the above.", OutcomeFilled)
	// Huntington Ingalls (SuccessFactors) adds the lawful temporary resident, a protected individual.
	hii := selectQ("For purposes of complying with federal export control laws, please select one of the following:",
		"A citizen of the U.S", "A Lawful Permanent Resident of the U.S., i.e., Green Card holder", "A U.S. asylee", "A U.S. refugee", "A U.S. temporary resident", "None of the above")
	hii.Kind = KindCustomListbox
	wantFill(t, matchReq(m, hii, need(false, true)), "None of the above", OutcomeFilled)
	// Which class a U.S. person belongs to is not in the facts, and no
	// sponsorship need does not make one: neither is guessed.
	own := ent(IDUSPerson, "fixed", withTypes(InputYesNo))
	own.Answer = "Yes"
	for name, r := range map[string]Result{
		"own yes": matchReq(pickMatcher(own, state), q, need(false, false)),
		"no need": matchReq(m, q, need(false, false)),
	} {
		if r.Outcome != OutcomeAbstained {
			t.Errorf("%s: answered %q", name, r.ChosenLabel)
		}
	}
}

func TestAnswerBank_Clearance_NeverHeldFromTheHistory(t *testing.T) {
	level := ent("security-clearance", "pick-option", withTypes(InputSingleSelect))
	level.Preferred = nil
	level.Placeholder = true
	history := ent("security-clearance-history", "fixed", withTypes(InputYesNo), withAnswer("No"))
	// Astranis (Greenhouse) on the engine's wire: a checkbox group.
	group := Question{Selector: "c", Kind: KindCheckbox, InputType: InputMultiSelect, Required: true,
		Text: "While a security clearance is not required, please let us know if you currently hold one or would be open to obtaining one:",
		Options: opts("Top Secret SCI with Polygraph", "Top Secret SCI/SAP", "Top Secret", "DOE level Q", "Secret",
			"Expired Clearance", "Open to obtaining", "Never held a clearance", "Do not wish to disclose")}
	r := matchReq(pickMatcher(level, history), group)
	if r.Outcome == OutcomeAbstained || r.ChosenLabel != "Never held a clearance" || len(r.ChosenLabels) != 1 {
		t.Fatalf("the user's No to ever holding one is the one box: %+v", r)
	}
	// A single select with a bare None.
	if r := matchReq(pickMatcher(level, history), selectQ("Security clearance level", "Secret", "Top Secret", "None")); r.ChosenLabel != "None" {
		t.Fatalf("select: %+v", r)
	}
	// CACI words the no-clearance option as possessing no active clearance, and asks the sponsoring agency the same way.
	caci := []string{"No, I do not possess an active security clearance.", "Secret", "Top Secret", "Top Secret/SCI", "Confidential", "MBI"}
	if r := matchReq(pickMatcher(level, history), selectQ("Do you currently possess an active security clearance?", caci...)); r.ChosenLabel != caci[0] {
		t.Fatalf("CACI level: %+v", r)
	}
	agency := []string{"I do not possess an active security clearance.", "DOD", "CIA", "DIA", "DOJ", "NRO", "NSA", "OPM"}
	if r := matchReq(pickMatcher(level, history), selectQ("If you currently possess an active security clearance, which agency sponsored the clearance?", agency...)); r.ChosenLabel != agency[0] {
		t.Fatalf("CACI agency: %+v", r)
	}
	// Onebrief (Ashby, live): the no-clearance statement says "currently"
	// between "I" and "do not hold".
	onebrief := []string{"NO, I currently do not hold active security clearance.", "YES, I currently hold an active security clearance."}
	ob := radioQ("Do you currently hold an active security clearance? This role does not require an active security clearance, but knowing your current clearance status is helpful to our team.", onebrief...)
	if r := matchReq(pickMatcher(level, history), ob); r.ChosenLabel != onebrief[0] {
		t.Fatalf("Onebrief: %+v", r)
	}
	// OpenAI (Ashby, live): a polygraph clearance is a clearance; never held answers No.
	poly := radioQ("Do you have an active Full Scope Polygraph clearance? If not applicable, select No.", "Yes", "No")
	if r := matchReq(pickMatcher(level, history), poly); r.ChosenLabel != "No" {
		t.Fatalf("polygraph: %+v", r)
	}
	if r := matchReq(pickMatcher(level, yesHistoryEntry()), poly); r.Outcome == OutcomeFilled {
		t.Fatalf("a held clearance answered the polygraph: %+v", r)
	}
	// A recorded level of None (the real bank's shape) beside the No.
	none := ent("security-clearance", "pick-option", withTypes(InputSingleSelect), withPreferred("None"))
	if r := matchReq(pickMatcher(none, history), group); r.ChosenLabel != "Never held a clearance" {
		t.Fatalf("None beside the No: %+v", r)
	}
	// None alone says nothing about the past, but answers a bare None.
	if r := matchReq(pickMatcher(none), group); r.ChosenLabel == "Never held a clearance" {
		t.Fatalf("None alone answered never held: %+v", r)
	}
	if r := matchReq(pickMatcher(none), selectQ("Security clearance level", "Secret", "Top Secret", "None")); r.ChosenLabel != "None" {
		t.Fatalf("None alone, select: %+v", r)
	}
	// A recorded level is the entry's, whatever the history says.
	secret := ent("security-clearance", "pick-option", withTypes(InputSingleSelect), withPreferred("Secret"))
	if r := matchReq(pickMatcher(secret, history), group); r.ChosenLabel == "Never held a clearance" {
		t.Fatalf("a recorded level was overridden: %+v", r)
	}
	// No recorded history, or a Yes, is never read as never held.
	blankHistory := ent("security-clearance-history", "fixed", withTypes(InputYesNo), withAnswer(""))
	blankHistory.Placeholder = true
	yesHistory := ent("security-clearance-history", "fixed", withTypes(InputYesNo), withAnswer("Yes"))
	for name, h := range map[string]Entry{"blank": blankHistory, "yes": yesHistory} {
		if r := matchReq(pickMatcher(level, h), group); r.ChosenLabel == "Never held a clearance" {
			t.Errorf("%s history answered %q", name, r.ChosenLabel)
		}
	}
}

// Being eligible for, or able to obtain, a clearance is not having held one:
// the history never answers it. A U.S. security clearance is granted to U.S.
// citizens only, so a recorded status that is not a citizen's answers No
// (Manifold Industries, Ashby, live, with its own spelling); a citizen's
// eligibility, and a clearance the question does not place in the U.S., stay
// with the user.
func TestAnswerBank_Clearance_EligibilityIsNotTheHistory(t *testing.T) {
	history := ent("security-clearance-history", "fixed", withTypes(InputYesNo), withAnswer("No"))
	status := func(p string) Entry {
		e := ent(idUSCitizenStatus, "pick-option", withPreferred(p), withTypes(InputSingleSelect))
		e.Placeholder = false
		return e
	}
	const manifold = "Are you elegible for a U.S. Department of Defense security clearence or currently hold one?"
	for _, c := range []struct {
		name, text string
		es         []Entry
		want       string // empty: not answered from the history or the status
	}{
		{"not a citizen", manifold, []Entry{history, status("Lawful permanent resident")}, "No"},
		{"not a citizen, able to obtain", "Are you able to obtain a U.S. security clearance?", []Entry{history, status("Neither")}, "No"},
		{"a citizen", manifold, []Entry{history, status("U.S. citizen")}, ""},
		{"no recorded status", "Are you eligible to obtain a U.S. security clearance?", []Entry{history}, ""},
		{"a clearance placed nowhere", "Would you be able to obtain a government security clearance?", []Entry{history, status("Neither")}, ""},
		{"holding one now is the history's", "Do you currently hold an active security clearence?", []Entry{history, status("U.S. citizen")}, "No"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(pickMatcher(c.es...), radioQ(c.text, "Yes", "No"))
			if c.want == "" {
				if r.Outcome != OutcomeAbstained {
					t.Fatalf("answered: %+v", r)
				}
				return
			}
			if r.ChosenLabel != c.want || r.Source != SourceDerived {
				t.Fatalf("want %q, got %+v", c.want, r)
			}
		})
	}
}

// Booz Allen (Workday): the federal employment status list sits under a note
// to ask an agency ethics official; the user's No to ever being a government
// employee is its "never" option, and "Not to my knowledge" is the No of a
// "To the best of your knowledge" question. The list that also names a party,
// a royal family and a candidate needs the user's No to those as well.
func TestAnswerBank_Government_NeverEmployedFromTheHistory(t *testing.T) {
	history := ent("government-employee-history", "fixed", withTypes(InputYesNo), withAnswer("No"))
	party := ent("party-royal-candidate-employment", "fixed", withTypes(InputYesNo), withAnswer("No"))
	note := "Please check with your agency ethics official for advice if you are participating in any government matters or a procurement related to Booz Allen Hamilton or if you are uncertain if your position is considered federal employment subject to these restrictions."
	fed := []string{"I have never been employed by the U.S. Federal Government.", "I am currently employed by the U.S. Federal Government.",
		"I am a former employee of U.S. Federal Government that left service in the last 2 years.", "I am a former employee of U.S. Federal Government that left service more than 2 years ago."}
	q := Question{Selector: "s", Text: note, Kind: KindCustomListbox, InputType: InputSingleSelect, Required: true, Options: opts(fed...)}
	if r := matchReq(pickMatcher(history), q); r.ChosenLabel != fed[0] || r.EntryID != "government-employee-history" {
		t.Fatalf("the user's No is the never option: %+v", r)
	}
	entity := []string{"I have never been employed by a ‘Government Entity’, or political party, a member of a royal family, or a candidate for political office.",
		"I am currently employed by a ‘Government Entity’.", "I am currently employed by a political party, a member of a royal family, or a candidate for political office.",
		"I am a former employee of a ‘Government Entity’ that left service more than 5 years ago."}
	entityQ := selectQ("Please select your state, local, non-U.S. government or other public entity (‘Government Entity’) or political party, a member of a royal family, or a candidate for political office employment status?", entity...)
	if r := matchReq(pickMatcher(history, party), entityQ); r.ChosenLabel != entity[0] {
		t.Fatalf("both Nos are the never option: %+v", r)
	}
	plain := []string{"I have never been employed by the federal government or a political party.", "I am currently employed by the federal government."}
	contractor := []string{"I have never been employed by a government or a government contractor.", "I am currently employed by a government."}
	for name, c := range map[string]struct {
		bank []Entry
		list []string
	}{"no party No": {[]Entry{history}, entity}, "no party No, plain": {[]Entry{history}, plain}, "another employer": {[]Entry{history, party}, contractor}} {
		if r := matchReq(pickMatcher(c.bank...), selectQ("Please select your government employment status?", c.list...)); r.ChosenLabel == c.list[0] {
			t.Errorf("%s: a never option the Nos do not cover was picked: %+v", name, r)
		}
	}
	// A Yes, or nothing recorded, picks nothing.
	blank := ent("government-employee-history", "fixed", withTypes(InputYesNo), withAnswer(""))
	blank.Placeholder = true
	for name, h := range map[string]Entry{"yes": ent("government-employee-history", "fixed", withTypes(InputYesNo), withAnswer("Yes")), "blank": blank} {
		if r := matchReq(pickMatcher(h), q); r.ChosenLabel == fed[0] {
			t.Errorf("%s history answered %q", name, r.ChosenLabel)
		}
	}
	yn := selectQ("Have you ever been a government employee?", "Yes", "Not to my knowledge")
	if r := matchReq(pickMatcher(history), yn); r.ChosenLabel != "Not to my knowledge" {
		t.Fatalf("Not to my knowledge is the No: %+v", r)
	}
	yes := ent("government-employee-history", "fixed", withTypes(InputYesNo), withAnswer("Yes"))
	if r := matchReq(pickMatcher(yes), yn); r.ChosenLabel != "Yes" {
		t.Fatalf("a Yes stays Yes: %+v", r)
	}
}

// Dimensional (Workday, live): whether the user's current firm works with the
// employer is the firm's tie, not the user's employment there; an employment
// list confirmed complete never answers it No, and the research reads the
// employers the user works for now, every one for "current or former".
func TestAnswerBank_FirmRelationshipIsTheFirmsTie(t *testing.T) {
	const dfa = "Does your current firm have a professional working relationship with Dimensional Fund Advisors?"
	f := relFacts("2026-10-01")
	f.Employment = append(f.Employment, EmploymentFact{Employer: "Initech", Title: "Analyst", Start: "2019-01", End: "2020-12"})
	q := selectQ(dfa, "Yes", "No")
	if r := relMatch(t, f, "Dimensional Fund Advisors", q); r.Outcome != OutcomeAbstained || r.Detail != DetailCounterpartyEmployer {
		t.Fatalf("%s %s %q from %s", r.Outcome, r.Source, r.ChosenLabel, r.Trace)
	}
	for text, want := range map[string]string{
		dfa: "Globex Inc.",
		"7. To the best of your knowledge, are you a current or former employee of an organization that is a Booz Allen Hamilton client?": "Globex Inc.,Initech",
	} {
		a, ok := ReadCounterparty(f, selectQ(text, "Yes", "No"), Date{})
		if got := strings.Join(a.Companies, ","); !ok || got != want {
			t.Errorf("%q researches %q, want %q", text[:40], got, want)
		}
	}
}

func yesHistoryEntry() Entry {
	return ent("security-clearance-history", "fixed", withTypes(InputYesNo), withAnswer("Yes"))
}
