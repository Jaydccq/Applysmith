package match

import "testing"

var communities = []string{"Person with a disability", "Veteran", "Parent or caregiver", "I prefer not to answer", "None of the above"}

const communitiesText = "Which of the following communities do you belong to? Please select all that apply."

func TestSelfID_DeclineFillWhenUnrecorded(t *testing.T) {
	// The question is misrouted onto a source entry that opts into the first-option net.
	src := ent("referral-source", "pick-option", withPreferred("Employee referral"), withExamples(communitiesText))
	src.FallbackAnyOption = true
	r := matchReq(pickMatcher(src), checkboxQ(communitiesText, communities...))
	if r.ChosenLabel != "I prefer not to answer" || r.Outcome != OutcomeFilledReview || r.Source != SourceSelfIDDecline ||
		r.Confidence != FuzzyConfidenceCap || r.AnswerKey != "" || r.EntryID != "" {
		t.Fatalf("decline fill: %+v", r)
	}
	if r.ChosenLabel == "None of the above" || len(r.ChosenLabels) != 0 {
		t.Fatalf("the aggregate negative is never ticked: %+v", r)
	}
	// Neither the first option nor an axis option is ever the answer.
	set := radioQ("Voluntary", "Person with disability", "Veteran", "I prefer not to answer")
	g := ent("eeo-gender", "pick-option")
	g.Preferred = nil
	g.Placeholder = true
	if r := matchReq(pickMatcher(g, src), set); r.ChosenLabel != "I prefer not to answer" {
		t.Fatalf("%+v", r)
	}
}

func TestSelfID_RecordedStanceNeverTradedForDecline(t *testing.T) {
	const q = "Race / ethnicity"
	race := ent("eeo-race", "pick-option", withPreferred("Asian"), withExamples(q))
	sub := radioQ(q, "Asian: Chinese (Example)", "Asian: Indian (Example)", "White", "I do not wish to answer")
	r := matchReq(pickMatcher(race), sub)
	wantAbstain(t, r, ReasonSelfIDPolicy, "")
	if !r.Terminal || r.ChosenLabel != "" {
		t.Fatalf("terminal and nothing chosen: %+v", r)
	}
	// A misrouted entry that is not a demographic one may still decline.
	other := ent("referral-source", "pick-option", withPreferred("Employee referral"), withExamples(q))
	if r := matchReq(pickMatcher(other), sub); r.ChosenLabel != "I do not wish to answer" {
		t.Fatalf("non-demographic misroute declines: %+v", r)
	}
}

func TestSelfID_EntryHoldsStance(t *testing.T) {
	for _, c := range []struct {
		name string
		e    *Entry
		want bool
	}{
		{"nil", nil, false},
		{"pick with preferred", &Entry{Kind: KindPickOption, Body: Body{Preferred: []string{"x"}}}, true},
		{"pick with fallback text alone", &Entry{Kind: KindPickOption, Body: Body{FallbackText: "x"}}, false},
		{"fixed with answer", &Entry{Kind: KindFixed, Body: Body{Answer: "No"}}, true},
		{"fixed blank", &Entry{Kind: KindFixed, Body: Body{Answer: "  "}}, false},
		{"other kind", &Entry{Kind: KindMultiSelect}, true},
	} {
		if got := entryHoldsStance(c.e); got != c.want {
			t.Errorf("%s = %v", c.name, got)
		}
	}
}

func TestSelfID_TerminalAbstain(t *testing.T) {
	// No decline option and no recorded answer: terminal, nothing chosen.
	q := radioQ("Voluntary", "Hispanic", "Female", "Veteran")
	g := ent("eeo-gender", "pick-option")
	g.Preferred, g.Placeholder = nil, true
	g.Examples = []string{"Voluntary"}
	r := matchReq(pickMatcher(g), q)
	wantAbstain(t, r, ReasonSelfIDPolicy, "")
	if !r.Terminal {
		t.Fatalf("terminal: %+v", r)
	}
	// No later pass changes a terminal abstain.
	if r.ChosenLabel != "" || r.Answer != "" {
		t.Fatalf("%+v", r)
	}
}

// Spec scenario "No decline option on the form": a race question with only
// specific categories and no recorded race ends in the terminal abstain.
func TestSelfID_RaceListWithoutADeclineIsTerminal(t *testing.T) {
	const q = "Race or ethnicity"
	race := ent("eeo-race", "pick-option")
	race.Preferred, race.Placeholder = nil, true
	race.Examples = []string{q}
	r := matchReq(pickMatcher(race), radioQ(q, "Hispanic or Latino", "Black or African American", "Asian", "White"))
	wantAbstain(t, r, ReasonSelfIDPolicy, "")
	if !r.Terminal || r.ChosenLabel != "" || r.Answer != "" {
		t.Fatalf("a race list without a decline chose or stayed open: %+v", r)
	}
	// A recorded race on the same list is the user's answer and fills.
	race = ent("eeo-race", "pick-option", withPreferred("Asian"), withExamples(q))
	r = matchReq(pickMatcher(race), radioQ(q, "Hispanic or Latino", "Black or African American", "Asian", "White"))
	if r.ChosenLabel != "Asian" {
		t.Fatalf("the recorded race is used: %+v", r)
	}
}

func TestSelfID_PolicyIsOutermostAndSparesFills(t *testing.T) {
	const q = "Disability status (Form CC-305, voluntary)"
	cc := []string{"Yes, I have a disability, or have had one in the past", "No, I do not have a disability and have not had one in the past", "I do not want to answer"}
	e := ent("eeo-disability", "pick-option", withPreferred("No, I do not have a disability"), withExamples(q))
	r := matchReq(pickMatcher(e), radioQ(q, cc...))
	wantFill(t, r, cc[1], OutcomeFilled)
	if r.Source == SourceSelfIDDecline || r.EntryID != "eeo-disability" {
		t.Fatalf("the recorded answer wins: %+v", r)
	}
	// With the default decline stance and needs_review the decline is a review fill.
	d := ent("eeo-disability", "pick-option", withPreferred("Decline to self-identify", "I do not want to answer"), withExamples(q), notCurated())
	r = matchReq(pickMatcher(d), radioQ(q, cc...))
	wantFill(t, r, cc[2], OutcomeFilledReview)
	if r.Confidence > FuzzyConfidenceCap {
		t.Fatalf("needs_review caps the confidence: %+v", r)
	}
}

func TestSelfID_RecordedNegativeNeverOpposite(t *testing.T) {
	dis := ent("eeo-disability", "pick-option", withPreferred("No, I do not have a disability"), withExamples("Disability"))
	r := matchReq(pickMatcher(dis), selectQ("Disability", "Yes", "No", "Prefer Not To Say"))
	wantFill(t, r, "No", OutcomeFilled)
	vet := ent("eeo-veteran", "pick-option", withPreferred("I am not a veteran"), withExamples("Veteran Status"))
	r = matchReq(pickMatcher(vet), selectQ("Veteran Status", "I Am A Veteran", "I Am Not A Veteran", "I Choose Not To Identify"))
	wantFill(t, r, "I Am Not A Veteran", OutcomeFilled)
	// The record is bare "No": the bare option inherits the question's scope.
	bare := ent("eeo-veteran", "pick-option", withPreferred("I am not a protected veteran"), withExamples("Are you a protected veteran?"))
	r = matchReq(pickMatcher(bare), radioQ("Are you a protected veteran?", "Yes", "No", "Prefer not to answer"))
	wantFill(t, r, "No", OutcomeFilled)
}

func TestSelfID_AskMeMarkerBeatsDecline(t *testing.T) {
	g := ent("eeo-gender", "pick-option", withPreferred(AskMe), withExamples("Gender"))
	r := matchReq(pickMatcher(g), radioQ("Gender", "Man", "Woman", "Prefer not to say"))
	wantAbstain(t, r, ReasonAskMe, "")
}

func TestSelfID_RouteSingleAxisByOptions(t *testing.T) {
	dis := ent("eeo-disability", "pick-option", withPreferred("No, I do not have a disability"))
	q := radioQ("Please check one of the boxes below:",
		"Yes, I have a disability, or have had one in the past", "No, I do not have a disability and have not had one in the past", "I do not want to answer")
	r := matchReq(pickMatcher(dis), required(q))
	wantFill(t, r, "No, I do not have a disability and have not had one in the past", OutcomeFilled)
	if r.EntryID != "eeo-disability" || !traceHas(r, "options are a single-axis self-ID set") {
		t.Fatalf("%+v", r)
	}
}

func TestSelfID_NeverInventNeverAGuess(t *testing.T) {
	// Nothing recorded and no decline: no lane fills a protected characteristic.
	empty := ent("eeo-race", "pick-option")
	empty.Preferred, empty.Placeholder = nil, true
	q := radioQ("Select all that apply", "Black", "Female", "Veteran")
	if r := matchReq(pickMatcher(empty), q); r.Outcome != OutcomeAbstained || !r.Terminal {
		t.Fatalf("%+v", r)
	}
	// A work-authorization pick entry with no answer abstains and never takes the first option.
	wa := ent("immigration-pick", "pick-option", withExamples("Your immigration status"))
	wa.Preferred, wa.FallbackAnyOption, wa.Category = nil, true, CategoryWorkAuthorization
	if r := matchReq(pickMatcher(wa), selectQ("Your immigration status", "Citizen", "Visa holder")); r.Outcome == OutcomeFilled {
		t.Fatalf("%+v", r)
	}
	// The setting never covers self-identification.
	g := ent("eeo-gender", "pick-option", withPreferred("Male"), withExamples("Gender"))
	if r := matchReq(pickMatcher(g), selectQ("Gender", "Man", "Woman"), withLegal(false)); r.ChosenLabel != "Man" {
		t.Fatalf("the user's own answer applies as written: %+v", r)
	}
	// 1Password's open share box names the survey's topics; a multi-line box never takes a self-ID answer,
	// and the reason says so rather than blaming the control.
	g.Category = CategorySelfIdentification
	box := textQ("s", "Helping you bring your best self to the recruitment process means a lot to us. Other than your ethnicity, gender, and disability status (survey below), is there anything you would like to share with us?")
	box.InputType = InputLongText
	if r := matchReq(pickMatcher(g), box); r.Reason != ReasonSelfIDPolicy || !r.Terminal {
		t.Fatalf("%+v", r)
	}
}

func TestSelfID_ExcludedFromEmbeddedDirective(t *testing.T) {
	// Page text never instructs the matcher: a question naming an option does not choose it.
	q := required(radioQ(`Which of these is not a cloud? (To confirm you read this, select "Lightning")`, "Stratus", "Cirrus", "Lightning", "Cumulus"))
	r := matchReq(pickMatcher(), q)
	if r.Outcome != OutcomeAbstained || r.ChosenLabel != "" {
		t.Fatalf("%+v", r)
	}
}

func TestClassify_ConsiderYourselfNeverHitsEEO(t *testing.T) {
	trans := ent("eeo-transgender", "pick-option", withPreferred("No"))
	trans.Description = "Do you consider yourself transgender?"
	cl, _ := classifyWith([]Entry{trans}, Question{Selector: "s", Kind: KindRadioGroup, Text: "Would you consider yourself a full-stack developer?", Options: opts("True", "False")}, nil)
	if cl.entry != nil {
		t.Fatalf("a generic frame is not a topic: %+v", cl)
	}
}

func TestClassify_CuratedExampleBeatsSelfIDRescue(t *testing.T) {
	const q = "Please check one of the boxes below:"
	mine := ent("my-legend", "pick-option", withPreferred("No, I do not have a disability"), withExamples(q))
	dis := ent("eeo-disability", "pick-option", withPreferred("Decline"))
	cl, _ := classifyWith([]Entry{mine, dis}, radioQ(q, "Yes, I have a disability", "No, I do not have a disability", "I do not want to answer"), nil)
	if entryID(cl) != "my-legend" || cl.stage != stageCuratedVerbatim {
		t.Fatalf("the user's own phrasing wins: %+v", cl)
	}
}

// TestSelfID_StanceQuestionTakesTheDeclineWhenUnanswered is the F-10 rule of
// "Self-identification and protected-category answers": a single-choice
// question that names a characteristic, over options that name none (the Ashby
// survey shape), takes the page's decline option when no recorded answer
// resolves it, at the review band.
func TestSelfID_StanceQuestionTakesTheDeclineWhenUnanswered(t *testing.T) {
	const q = "Do you identify as transgender?"
	const accommodation = "Will you need an accommodation for a disability during the interview process?"
	stance := []string{"No", "Yes", "Prefer not to say"}
	selfID := func(e *Entry) { e.Category = CategorySelfIdentification }
	blank := []Entry{ent("eeo-transgender", "pick-option", withExamples(q), selfID, func(e *Entry) { e.Preferred, e.Placeholder = nil, true })}
	cases := []struct {
		name string
		bank []Entry
		q    Question
		want string // the chosen label; empty: nothing is chosen
	}{
		{"no entry for the question", nil, radioQ(q, stance...), "Prefer not to say"},
		{"a blank self-identification entry", blank, radioQ(q, stance...), "Prefer not to say"},
		{"near miss: the user's own answer wins", []Entry{ent("interview-accommodation", "fixed", withAnswer("No"), withExamples(accommodation))}, radioQ(accommodation, stance...), "No"},
		{"near miss: a recorded answer the options cannot express", []Entry{ent("eeo-transgender", "pick-option", withPreferred("Questioning"), withExamples(q), selfID)}, radioQ(q, stance...), ""},
		{"near miss: no decline option", blank, radioQ(q, "No", "Yes"), ""},
		{"near miss: a box is never ticked", blank, checkboxQ(q, stance...), ""},
		{"near miss: a question that names no characteristic", nil, radioQ("Do you have a non-compete agreement?", stance...), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(pickMatcher(c.bank...), c.q)
			if r.ChosenLabel != c.want {
				t.Fatalf("chose %q, want %q: %s %s", r.ChosenLabel, c.want, r.Outcome, r.Reason)
			}
			if decline := c.want == "Prefer not to say"; decline != (r.Source == SourceSelfIDDecline) || decline && r.Outcome != OutcomeFilledReview {
				t.Fatalf("a decline is the page's own option at the review band, nothing else is: %s %s", r.Outcome, r.Source)
			}
		})
	}
}

// RTX (Workday) asks "What is your Sex?" with Female, Male and "I do not wish
// to share": the recorded gender answers it, and with none recorded the page's
// own refusal is the decline. A sex-offence question is never the gender one.
func TestSelfID_SexQuestionIsTheGenderQuestion(t *testing.T) {
	const q = "What is your Sex?"
	opts := []string{"Female", "I do not wish to share", "Male"}
	g := ent("eeo-gender", "pick-option", withPreferred("Male"), withExamples("Gender"))
	if r := matchReq(pickMatcher(g), selectQ(q, opts...)); r.ChosenLabel != "Male" || r.EntryID != "eeo-gender" {
		t.Fatalf("recorded gender: %+v", r)
	}
	blank := ent("eeo-gender", "pick-option", withExamples("Gender"))
	blank.Preferred, blank.Placeholder = nil, true
	if r := matchReq(pickMatcher(blank), selectQ(q, opts...)); r.ChosenLabel != "I do not wish to share" || r.Source != SourceSelfIDDecline {
		t.Fatalf("decline: %+v", r)
	}
	for _, other := range []string{"Have you ever been convicted of a sex offense?", "What sex were you assigned at birth?"} {
		if r := matchReq(pickMatcher(g), selectQ(other, "Yes", "No", "Female", "Male")); r.EntryID == "eeo-gender" {
			t.Fatalf("%q took the gender answer: %+v", other, r)
		}
	}
}
