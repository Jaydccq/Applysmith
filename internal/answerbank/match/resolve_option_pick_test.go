package match

import (
	"testing"
)

// Citadel asks its "top two preferred coding languages for interviews" as two
// lists, "Preference 1" and "Preference 2": the second takes the next language
// the user ranks, never the first again, and a place past the user's ranked
// languages is no answer.
func TestPickOption_ALaterPreferenceTakesTheNextRankedOption(t *testing.T) {
	const ask = "Please select your top two preferred coding languages for interviews: "
	e := ent("preferred-coding-language", "pick-option", withExamples("Preferred coding language"))
	e.Preferred = []string{"Python 3", "Python", "Python 2", "Java"}
	m := pickMatcher(e)
	langs := []string{"C++", "Python", "Java", "JavaScript", "TypeScript"}
	wantFill(t, matchReq(m, selectQ(ask+"Preference 1", langs...)), "Python", OutcomeFilled)
	wantFill(t, matchReq(m, selectQ(ask+"Preference 2", langs...)), "Java", OutcomeFilled)
	if r := matchReq(m, selectQ(ask+"Preference 3", langs...)); r.Outcome == OutcomeFilled || r.Outcome == OutcomeFilledReview {
		t.Fatalf("a third place past the two ranked languages was answered: %+v", r)
	}
}

func TestPickOption_ImplicitFallbackTextIsTheSinglePreferred(t *testing.T) {
	const q = "What is your gender?"
	e := ent("eeo-gender", "pick-option", withExamples(q))
	e.Preferred, e.FallbackText = nil, "Male"
	r := matchReq(pickMatcher(e), selectQ(q, "Man", "Woman", "Non-Binary", "Decline to self-identify"))
	wantFill(t, r, "Man", OutcomeFilled)
	if !traceHas(r, "fallback_text") {
		t.Fatalf("trace names the implicit preferred: %v", r.Trace)
	}
}

func TestPickOption_EmptyOptionsDefersToClient(t *testing.T) {
	const q = "Phone device type"
	e := curatedPick("phone-type", q, "+1 US - United States", "+1")
	lb := Question{Selector: "s", Text: q, Kind: KindCustomListbox, InputType: InputSingleSelect}
	r := matchReq(pickMatcher(e), lb)
	if r.Outcome != OutcomeFilled || r.Answer != "+1" || r.ChosenLabel != "+1" {
		t.Fatalf("the bare dial code is the atomic value: %+v", r)
	}
	e2 := curatedPick("phone-type", q, "Mobile", "Home")
	r = matchReq(pickMatcher(e2), lb)
	if r.Answer != "Mobile" || r.ChosenLabel != "Mobile" {
		t.Fatalf("first value otherwise: %+v", r)
	}
	// The outcome follows the bands: a fuzzy route never defers at the automatic band.
	f := ent("phone-type", "pick-option", withPreferred("Mobile"))
	f.Description = "phone device type"
	r = matchReq(pickMatcher(f), Question{Selector: "s", Text: "Device type of the phone", Kind: KindCustomListbox, InputType: InputSingleSelect})
	if r.Outcome == OutcomeFilled {
		t.Fatalf("band-derived outcome: %+v", r)
	}
}

func TestPickOption_RaceSubGranularVeto(t *testing.T) {
	const q = "Race / ethnicity"
	broad := ent("eeo-race", "pick-option", withPreferred("Asian"), withExamples(q))
	sub := selectQ(q, "Asian: Chinese (United States)", "Asian: Indian (United States)", "Asian: Japanese (United States)", "White")
	r := matchReq(pickMatcher(broad), sub)
	wantAbstain(t, r, ReasonSelfIDPolicy, "")
	if !r.Terminal {
		t.Fatal("the veto is terminal")
	}
	// A plain list still fills at full confidence.
	r = matchReq(pickMatcher(broad), selectQ(q, "Asian", "Black", "White"))
	wantFill(t, r, "Asian", OutcomeFilled)
	// When the bank carries the sub-category ranked first it resolves exactly.
	exact := ent("eeo-race", "pick-option", withPreferred("Asian: Chinese (United States)", "Asian"), withExamples(q))
	r = matchReq(pickMatcher(exact), sub)
	wantFill(t, r, "Asian: Chinese (United States)", OutcomeFilled)
	// Not a general ties-abstain rule: a near-tied non-demographic entry keeps DOM order.
	src := curatedPick("job-source", "Where did you find us?", "LinkedIn")
	r = matchReq(pickMatcher(src), selectQ("Where did you find us?", "LinkedIn Jobs", "LinkedIn Premium"))
	wantFill(t, r, "LinkedIn Jobs", OutcomeFilledReview)
}

func TestPickOption_RaceEthnicitySiblingSwap(t *testing.T) {
	const q = "Ethnicity"
	eth := ent("eeo-ethnicity", "pick-option", withPreferred("Not Hispanic or Latino"), withExamples(q))
	race := ent("eeo-race", "pick-option", withPreferred("Asian"))
	r := matchReq(pickMatcher(eth, race), selectQ(q, "Asian", "Black", "White", "Decline to self-identify"))
	wantFill(t, r, "Asian", OutcomeFilled)
	if r.EntryID != "eeo-race" || !traceHas(r, "race/ethnicity label conflation") {
		t.Fatalf("sibling swap: %+v", r)
	}
	// No other pair is swapped.
	g := ent("eeo-gender", "pick-option", withPreferred("Male"), withExamples("Gender"))
	v := ent("eeo-veteran", "pick-option", withPreferred("I am not a protected veteran"))
	r = matchReq(pickMatcher(g, v), selectQ("Gender", "Asian", "Black", "White"))
	if r.Outcome != OutcomeAbstained || r.EntryID == "eeo-veteran" {
		t.Fatalf("only the race and ethnicity pair swaps: %+v", r)
	}
}

// HelloFresh's race follow-up names the choice above that asks it and the
// answer otherwise: with one race chosen it takes "Does not apply", not the
// bank's race; with the named choice held it is left to its own lane.
func TestPickOption_FollowUpNamingTheChoiceAboveTakesItsOtherwiseAnswer(t *testing.T) {
	const followUp = "If you selected, “Two or More Race/Ethnicities” above, please select which races/ethnicities you identify with. (select all that apply) If you did not select, “Two or More Race/Ethnicities”, please answer “Does not apply”."
	races := []string{"Black or of African descent", "East Asian", "White or European", "Other", "I don't wish to answer"}
	parent := selectQ("How would you describe your racial/ethnic background? (select only one)", append(races, "Two or More Races/Ethnicities")...)
	child := checkboxQ(followUp, append(races, "Does not apply")...)
	child.InputType = InputMultiSelect
	one := ent("eeo-race", "pick-option", withPreferred("East Asian"))
	rs := matchAll(pickMatcher(one), parent, child)
	wantFill(t, rs[0], "East Asian", OutcomeFilled)
	if resultLabel(rs[1]) != "Does not apply" || rs[1].Source != SourceConditional || rs[1].Outcome != rs[0].Outcome {
		t.Fatalf("follow-up: %+v", rs[1])
	}
	several := ent("eeo-race", "pick-option", withPreferred("Two or More Races/Ethnicities"))
	if rs := matchAll(pickMatcher(several), parent, child); resultLabel(rs[1]) == "Does not apply" {
		t.Fatalf("the named choice is held: %+v", rs[1])
	}
	if rs := matchAll(pickMatcher(one), child); resultLabel(rs[0]) == "Does not apply" {
		t.Fatalf("no question before it: %+v", rs[0])
	}
}

// Squarespace's text follow-up names the choice that asks it in one sentence
// and what to put otherwise in another: with another choice held it takes the
// page's "N/A"; with the named choice held, or nothing held, it is the user's.
func TestPickOption_TextFollowUpNamingTheChoiceAboveTakesItsOtherwiseAnswer(t *testing.T) {
	const parentQ = "Have you previously been employed by or worked directly for Squarespace in any capacity? Please choose all that apply."
	const followUp = "If you answered ‘Yes - Consultant or Vendor for Squarespace', please elaborate below, provide the name of the employer (including if a sole proprietor/freelancer), dates of your engagement with Squarespace, and briefly describe the nature of your work with Squarespace. If you did not answer this way, please put ‘N/A’."
	choices := []string{"No - I have not worked at Squarespace before", "Yes - Full Time Employee at Squarespace", "Yes - Intern at Squarespace", "Yes - Consultant or Vendor for Squarespace"}
	parent := selectQ(parentQ, choices...)
	child := required(Question{Text: followUp, Kind: KindText, InputType: InputLongText})
	never := ent("worked-here", "pick-option", withPreferred(choices[0]), withExamples(parentQ))
	rs := matchAll(pickMatcher(never), parent, child)
	wantFill(t, rs[0], choices[0], OutcomeFilled)
	if rs[1].Answer != "N/A" || rs[1].Source != SourceConditional || rs[1].Outcome != rs[0].Outcome {
		t.Fatalf("follow-up: %+v", rs[1])
	}
	vendor := ent("worked-here", "pick-option", withPreferred(choices[3]), withExamples(parentQ))
	if rs := matchAll(pickMatcher(vendor), parent, child); rs[1].Answer == "N/A" {
		t.Fatalf("the named choice is held: %+v", rs[1])
	}
	if rs := matchAll(pickMatcher(), parent, child); rs[1].Answer == "N/A" {
		t.Fatalf("nothing is held: %+v", rs[1])
	}
}

// Squarespace's follow-up under a Yes / No / Not Sure question names the
// answer that takes its value: "No" held gives the page's "N/A"; "Not Sure"
// or "Yes" asks the user to elaborate.
func TestPickOption_TextFollowUpNamingTheAnswerThatTakesItsValue(t *testing.T) {
	const parentQ = "Are you subject to any contractual obligations or post-employment restrictions that may apply to or would limit your employment at Squarespace? (e.g., non-compete)?"
	const followUp = "If Yes, or if you are not sure, please elaborate below. If you answered no, please input ‘N/A’."
	parent := selectQ(parentQ, "Yes", "No", "Not Sure")
	child := required(Question{Text: followUp, Kind: KindText, InputType: InputLongText})
	for _, c := range []struct {
		held string
		na   bool
	}{{"No", true}, {"Not Sure", false}, {"Yes", false}} {
		e := ent("restrictions", "pick-option", withPreferred(c.held), withExamples(parentQ))
		rs := matchAll(pickMatcher(e), parent, child)
		wantFill(t, rs[0], c.held, OutcomeFilled)
		if got := rs[1].Answer == "N/A" && rs[1].Source == SourceConditional; got != c.na {
			t.Fatalf("%s held: follow-up %+v", c.held, rs[1])
		}
	}
	// A text box is never given a statement the page dictates.
	declare := required(Question{Text: "If you answered yes, please type ‘I certify the above’.", Kind: KindText, InputType: InputLongText})
	yes := ent("restrictions", "pick-option", withPreferred("Yes"), withExamples(parentQ))
	if rs := matchAll(pickMatcher(yes), parent, declare); rs[1].Source == SourceConditional {
		t.Fatalf("a dictated statement was typed: %+v", rs[1])
	}
}

func TestPickOption_SourceChannelSocialCategory(t *testing.T) {
	const q = "How did you hear about this job?"
	e := ent("job-source", "pick-option", withPreferred("LinkedIn Jobs"), withExamples(q))
	r := matchReq(pickMatcher(e), selectQ(q, "Company website", "Referral", "Other Social Networking Sites", "Other"))
	wantFill(t, r, "Other Social Networking Sites", OutcomeFilledReview)
	// Only for a social platform preferred value.
	f := ent("job-source", "pick-option", withPreferred("Career fair"), withExamples(q))
	r = matchReq(pickMatcher(f), selectQ(q, "Company website", "Other Social Networking Sites"))
	if r.ChosenLabel == "Other Social Networking Sites" {
		t.Fatalf("not a social platform: %+v", r)
	}
}

func TestPickOption_FallbackAnyOptionGuards(t *testing.T) {
	const q = "How did you hear about us?"
	any := ent("referral-source", "pick-option", withPreferred("Employee referral"), withExamples(q))
	any.FallbackAnyOption = true
	r := matchReq(pickMatcher(any), selectQ(q, "Select One", "Job fair", "Newspaper"))
	wantFill(t, r, "Job fair", OutcomeFilled) // placeholder skipped
	if !traceHas(r, "fallback_any_option") {
		t.Fatalf("trace: %v", r.Trace)
	}
	r = matchReq(pickMatcher(any), selectQ(q, "Select One", "None"))
	if r.Outcome == OutcomeFilled && r.ChosenLabel == "Select One" {
		t.Fatalf("a placeholder is never committed: %+v", r)
	}
	// A self-identification set is refused whatever the route.
	set := selectQ(q, "Person with disability", "Veteran", "Parent or caregiver", "I prefer not to answer")
	r = matchReq(pickMatcher(any), set)
	if r.ChosenLabel == "Person with disability" || r.ChosenLabel == "Veteran" {
		t.Fatalf("self-id first option: %+v", r)
	}
	// A fuzzy route is refused.
	fz := ent("referral-source", "pick-option", withPreferred("Employee referral"))
	fz.FallbackAnyOption = true
	fz.Description = "how did you hear about the referral"
	r = matchReq(pickMatcher(fz), selectQ("how did you feel about using AI tools", "Haven't tried it", "Use daily"))
	if r.ChosenLabel == "Haven't tried it" {
		t.Fatalf("a fuzzy route never uses the first-option net: %+v", r)
	}
	// A work-authorization entry never takes the net.
	wa := ent("immigration-pick", "pick-option", withPreferred("Citizen"), withExamples("Your status"))
	wa.FallbackAnyOption, wa.Category = true, CategoryWorkAuthorization
	r = matchReq(pickMatcher(wa), selectQ("Your status", "Visa holder", "Student"))
	if r.Outcome == OutcomeFilled {
		t.Fatalf("work-authorization first option: %+v", r)
	}
}

func TestPickOption_TextControlTypesFallbackText(t *testing.T) {
	e := ent("work-pref", "pick-option", withPreferred("Remote"), withExamples("Work preference"))
	e.FallbackText = "Remote"
	r := matchReq(pickMatcher(e), textQ("s", "Work preference"))
	if r.Answer != "Remote" || r.Outcome == OutcomeAbstained {
		t.Fatalf("text control: %+v", r)
	}
	// An empty fallback text types the one preferred value; of several, none.
	e.FallbackText = ""
	if r := matchReq(pickMatcher(e), textQ("s", "Work preference")); r.Answer != "Remote" {
		t.Fatalf("one preferred value: %+v", r)
	}
	e.Preferred = []string{"Remote", "Hybrid"}
	if r := matchReq(pickMatcher(e), textQ("s", "Work preference")); r.Outcome != OutcomeAbstained {
		t.Fatalf("two preferred values and no fallback text abstain: %+v", r)
	}
}

func TestPickOption_EducationLadderAndLevelGuards(t *testing.T) {
	const q = "Highest level of education completed"
	e := ent("highest-level-education", "pick-option", withPreferred("Master's degree"), withExamples(q))
	ladder := selectQ(q, "High School", "Associate Degree", "Bachelor's Degree", "Post Graduate Degree", "Doctorate")
	r := matchReq(pickMatcher(e), ladder)
	wantFill(t, r, "Post Graduate Degree", OutcomeFilled)
	// The ladder never goes above the user's level: the highest rung below it.
	r = matchReq(pickMatcher(e), selectQ(q, "High School", "Bachelors Degree", "Doctorate"))
	wantFill(t, r, "Bachelors Degree", OutcomeFilled)
	if !traceHas(r, "education ladder") {
		t.Fatalf("chosen by the ladder: %v", r.Trace)
	}
	// A claimed Master's never scores a Bachelor's option when no ladder reads.
	r = matchReq(pickMatcher(e), selectQ(q, "Bachelors Degree", "Doctorate"))
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("level contradiction: %+v", r)
	}
	// A degree-name catalog never qualifies as a ladder.
	cat := selectQ(q, "Bachelor of Science in Biology", "Bachelor of Arts in History", "Bachelor of Engineering", "Doctor of Law")
	if r := matchReq(pickMatcher(e), cat); r.ChosenLabel == "Bachelor of Science in Biology" {
		t.Fatalf("catalog: %+v", r)
	}
}

func TestPickOption_OptionsNeverChosenFromPlaceholdersOrWithdrawals(t *testing.T) {
	const q = "Are you able to continue?"
	e := curatedPick("continue", q, "No")
	r := matchReq(pickMatcher(e), radioQ(q, "Yes, continue the process", "No thank you - I withdraw my interest in the role"))
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("a withdrawal option is chosen only by a verbatim answer: %+v", r)
	}
	v := curatedPick("continue", q, "No thank you - I withdraw my interest in the role")
	wantFill(t, matchReq(pickMatcher(v), radioQ(q, "Yes, continue the process", "No thank you - I withdraw my interest in the role")),
		"No thank you - I withdraw my interest in the role", OutcomeFilled)
}

func TestPickOption_WithdrawalBestOptionAbstainsDestructive(t *testing.T) {
	const q = "Are you able to continue?"
	opts := []string{"Yes, continue the process", "No thank you - I withdraw my interest in the role"}
	// The inferred best option is the withdrawal: the walk names the guard.
	r := matchReq(pickMatcher(curatedPick("continue", q, "No")), radioQ(q, opts...))
	wantAbstain(t, r, ReasonDestructiveOption, "")
	if !traceHas(r, "destructive-option guard") {
		t.Fatalf("the rejection is traced: %v", r.Trace)
	}
	// An ordinary miss keeps the ordinary reason.
	wantAbstain(t, matchReq(pickMatcher(curatedPick("continue", q, "Maybe later")), radioQ(q, opts...)), ReasonLowConfidence, "")
}

// Nominal asks "What is your preferred programming language for interviews?"
// in a text box: the entry that picks the language from a list types its own
// text there; a question that asks for a story does not take it.
func TestPickOption_TypedTextInAShortTextBox(t *testing.T) {
	e := ent("preferred-coding-language", "pick-option", withPreferred("Python 3", "Java"), withTypes(InputSingleSelect),
		withExamples("Preferred programming language"), func(e *Entry) { e.FallbackText = "Python" })
	r := matchReq(pickMatcher(e), Question{Selector: "s", Kind: KindText, InputType: InputText, Text: "What is your preferred programming language for interviews?"})
	if r.Answer != "Python" || r.EntryID != "preferred-coding-language" {
		t.Fatalf("typed text: %+v", r)
	}
	r = matchReq(pickMatcher(e), Question{Selector: "s", Kind: KindText, InputType: InputLongText, Text: "New Grad 2027 Application Questions : What is your preferred programming language for interviews?"})
	if r.Answer != "Python" {
		t.Fatalf("typed text in a textarea behind a section title: %+v", r)
	}
	r = matchReq(pickMatcher(e), Question{Selector: "s", Kind: KindText, InputType: InputText, Text: "Describe a project you built in your preferred programming language"})
	if r.Answer == "Python" {
		t.Fatalf("a story ask took the typed text: %+v", r)
	}
}

// Tenex (Ashby, live): with no preferred source among "Current Employee",
// "Social Media", ..., the first-option net took "Current Employee", which
// claims a referral no record holds. A source the net picks is never a
// person's referral or outreach; the next real option is taken.
func TestAnswerBank_FirstOptionNet_NeverClaimsAPersonsReferral(t *testing.T) {
	const q = "How did you find out about this position?"
	src := ent("referral-source", "pick-option", withPreferred("LinkedIn", "Job Board"), withExamples(q))
	src.FallbackAnyOption, src.Category = true, CategorySource
	m := pickMatcher(src)
	for _, c := range []struct {
		opts []string
		want string
	}{
		{[]string{"Current Employee", "Social Media", "Search Engine", "Indeed"}, "Social Media"},
		{[]string{"Employee Referral", "Friend or family", "Recruiter reached out", "Career Fair"}, "Career Fair"},
		{[]string{"Referral", "Employee"}, ""},
	} {
		r := matchReq(m, selectQ(q, c.opts...))
		if c.want == "" {
			if r.Outcome != OutcomeAbstained {
				t.Errorf("%v: want left, got %q", c.opts, r.ChosenLabel)
			}
			continue
		}
		if r.ChosenLabel != c.want {
			t.Errorf("%v: want %q, got %q (%s) %v", c.opts, c.want, r.ChosenLabel, r.Outcome, r.Trace)
		}
	}
}

// Observe.AI (Greenhouse, live): a required text follow-up quotes the choices
// that ask it and says nothing of the others. With another source held it
// takes "N/A", as under a No gate; with a quoted choice held, or nothing held,
// it is the user's.
func TestPickOption_TextFollowUpQuotingChoicesNotHeldIsNotApplicable(t *testing.T) {
	const parentQ = "How did you hear about this opportunity?"
	const followUp = `Please type in your answer if you selected "Current Observe.AI employee" or "Other"`
	parent := selectQ(parentQ, "LinkedIn", "Indeed", "Current Observe.AI employee", "Other")
	child := required(Question{Text: followUp, Kind: KindText, InputType: InputText})
	linkedin := ent("referral-source", "pick-option", withPreferred("LinkedIn"), withExamples(parentQ))
	rs := matchAll(pickMatcher(linkedin), parent, child)
	wantFill(t, rs[0], "LinkedIn", OutcomeFilled)
	if rs[1].Answer != "N/A" || rs[1].Source != SourceConditional {
		t.Fatalf("follow-up: %+v", rs[1])
	}
	other := ent("referral-source", "pick-option", withPreferred("Other"), withExamples(parentQ))
	if rs := matchAll(pickMatcher(other), parent, child); rs[1].Answer == "N/A" {
		t.Fatalf("a quoted choice is held: %+v", rs[1])
	}
	if rs := matchAll(pickMatcher(), parent, child); rs[1].Answer == "N/A" {
		t.Fatalf("nothing is held: %+v", rs[1])
	}
}

// A channel the user prefers is not read into an option that names a person
// who referred or reached them: "Saw on personal LinkedIn feed" asks whose
// post it was (The Clearing House; the owner: avoid it, pick Other). A
// preference that names a referral still takes one.
func TestAnswerBank_JobSource_APreferredChannelSkipsAPersonOption(t *testing.T) {
	src := ent("job-source", "pick-option", withPreferred("LinkedIn", "Other"), withExamples("How did you hear about us?"))
	src.Placeholder = false
	src.Category = CategorySource
	q := Question{Text: "How Did You Hear About Us?", Kind: KindSelect, InputType: InputSingleSelect,
		Options: opts("Agency", "Current Worker", "Employee Referral", "Indeed", "Other", "Saw on personal LinkedIn feed")}
	if r := matchAll(pickMatcher(src), q)[0]; r.Answer != "Other" {
		t.Fatalf("picked %q (%s), want Other", r.Answer, r.Outcome)
	}
	ref := ent("referral-source", "pick-option", withPreferred("Employee Referral"), withExamples("How did you hear about us?"))
	ref.Placeholder = false
	if r := matchAll(pickMatcher(ref), q)[0]; r.Answer != "Employee Referral" {
		t.Fatalf("a referral preference picked %q", r.Answer)
	}
}
