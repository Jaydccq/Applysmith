package match

import "testing"

// highestLevelBank is the shipped shape of the entry: a blank placeholder the
// profile answers for.
func highestLevelBank() *Matcher {
	return New(bankOf(Entry{ID: "highest-level-education", Kind: KindPickOption,
		Meta: Meta{Examples: []string{"Highest level of education"}, Placeholder: true}}), DefaultStages())
}

func TestAnswerBank_EducationLevel_DerivedLevelChoosesTheRung(t *testing.T) {
	const q = "What is your highest level of education?"
	ask := func(facts Facts, today Date, labels ...string) Result {
		return pfOne(t, highestLevelBank(), facts, today, pfSelect("s", q, labels...))
	}
	master := pfDate(2026, 10, 1)
	// The user's level is the highest completed degree in the history.
	r := ask(alexFacts(), master, "High School", "Bachelor's Degree", "Master's Degree", "Doctoral Degree")
	if r.ChosenLabel != "Master's Degree" || r.Source != SourceDerived {
		t.Fatalf("%+v", r)
	}
	// Never a rung above: no master's rung, so the highest below it.
	r = ask(alexFacts(), master, "High School", "4 Year Degree", "Doctoral Degree")
	if r.ChosenLabel != "4 Year Degree" {
		t.Fatalf("%+v", r)
	}
	// A degree still in progress is not the level.
	r = ask(alexFacts(), pfDate(2026, 4, 30), "High School", "Bachelor's Degree", "Master's Degree", "Doctoral Degree")
	if r.ChosenLabel != "Bachelor's Degree" {
		t.Fatalf("in-progress master's counted: %+v", r)
	}
	// An or-higher rung satisfies a master's holder, and a bachelor's holder is never offered it.
	or := []string{"High School Diploma", "Bachelors Degree", "Masters Degree or Higher"}
	if r = ask(alexFacts(), master, or...); r.ChosenLabel != "Masters Degree or Higher" {
		t.Fatalf("%+v", r)
	}
	bachelor := Facts{Education: []EducationFact{{School: "Example College", Degree: "Bachelor of Science", End: "2024-05"}}}
	if r = ask(bachelor, master, or...); r.ChosenLabel != "Bachelors Degree" {
		t.Fatalf("%+v", r)
	}
	// Justworks (Greenhouse, live): the master's holder takes the rung that
	// names a master's beside a doctorate.
	justworks := []string{"GED or high school diploma", "Apprenticeship, boot camp, and/or professional certificate", "Associate's", "Bachelor's", "Master's, JD, and/or PhD", "I don't wish to answer"}
	if r = ask(alexFacts(), master, justworks...); r.ChosenLabel != "Master's, JD, and/or PhD" {
		t.Fatalf("%+v", r)
	}
	if r = ask(bachelor, master, justworks...); r.ChosenLabel != "Bachelor's" {
		t.Fatalf("%+v", r)
	}
	// A census ladder opens with the rung below high school.
	census := []string{"Less than high school", "High school diploma or GED", "Some college, no degree", "Associate degree", "Bachelor's degree", "Graduate degree"}
	if r = ask(bachelor, master, census...); r.ChosenLabel != "Bachelor's degree" {
		t.Fatalf("%+v", r)
	}
	// Every rung above the user's level: nothing is chosen.
	if r = ask(bachelor, master, "Master's Degree", "Doctoral Degree", "Post Graduate Degree"); r.Outcome != OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
	// No completed degree: the blank entry abstains, nothing is invented.
	for _, f := range []Facts{{}, {Education: []EducationFact{{Degree: "Bachelor of Science"}}}} {
		r = ask(f, master, "High School", "Bachelor's Degree", "Master's Degree")
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("%+v", r)
		}
	}
	// Another person's education, or what the role requires, is not the
	// applicant's level.
	for _, text := range []string{
		"What is the highest level of education completed by either of your parents or guardians?",
		"What is the highest level of education required for this role?",
	} {
		if r = pfOne(t, highestLevelBank(), alexFacts(), master, pfSelect("s", text, "High School", "Bachelor's Degree", "Master's Degree")); r.ChosenLabel != "" {
			t.Fatalf("%q: %+v", text, r)
		}
		// Nor does the user's own filled entry answer it.
		own := Entry{ID: "highest-level-education", Kind: KindPickOption, Body: Body{Preferred: []string{"Bachelor's Degree"}},
			Meta: Meta{Examples: []string{"Highest level of education"}}}
		if r = pfOne(t, New(bankOf(own), DefaultStages()), alexFacts(), master, pfSelect("s", text, "High School", "Bachelor's Degree", "Master's Degree")); r.Outcome != OutcomeAbstained {
			t.Fatalf("%q answered from the user's entry: %+v", text, r)
		}
	}
	// Questions that name a more specific education ask are not this lane's.
	r = pfOne(t, highestLevelBank(), alexFacts(), master,
		pfSelect("s", "Are you currently pursuing a degree?", "High School", "Bachelor's Degree", "Master's Degree"))
	if r.Source == SourceDerived {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerBank_EducationLevel_LadderChoiceNeverAbove(t *testing.T) {
	nine := opts("Choose One", "Unknown", "High School/Equivalent", "Some College-No Degree", "2 Year Degree",
		"3 Year Degree", "4 Year University/College Degree", "Post Graduate Degree", "Doctoral Degree")
	got, _, ok := pickEducationRung(nine, levelPostGraduate, nil)
	if !ok || got.Label != "Post Graduate Degree" {
		t.Fatalf("got %q %v", got.Label, ok)
	}
	t.Run("master's holder, no master's rung", func(t *testing.T) {
		got, _, ok := pickEducationRung(opts("High School", "4 Year Degree", "Doctoral Degree"), levelPostGraduate, nil)
		if !ok || got.Label != "4 Year Degree" {
			t.Fatalf("got %q %v", got.Label, ok)
		}
	})
	t.Run("the level is derived from the completed degrees of the profile", func(t *testing.T) {
		r := pfOne(t, highestLevelBank(), alexFacts(), pfDate(2026, 10, 1),
			pfSelect("s", "Highest degree earned", "High School", "4 Year Degree", "Doctoral Degree"))
		if r.ChosenLabel != "4 Year Degree" || r.Source != SourceDerived {
			t.Fatalf("a master's holder is offered no rung above the master's: %+v", r)
		}
	})
	t.Run("every rung above", func(t *testing.T) {
		if _, note, ok := pickEducationRung(opts("Master's", "Doctoral", "Post Graduate"), levelFourYear, nil); ok || note == "" {
			t.Fatalf("ok=%v note=%q", ok, note)
		}
	})
	t.Run("a catalog repeating a level is not a ladder", func(t *testing.T) {
		catalog := opts("Bachelor of Arts", "Bachelor of Science", "Master of Arts", "Doctor of Philosophy")
		if _, ok := readEducationLadder(catalog); ok {
			t.Fatal("read as a ladder")
		}
		if _, _, ok := pickEducationRung(opts("Bachelor", "Master"), levelPostGraduate, nil); ok {
			t.Fatal("fewer than three levels")
		}
	})
	t.Run("an or-higher rung satisfies the level", func(t *testing.T) {
		ladder := opts("High School Diploma", "Bachelors Degree", "Masters Degree or Higher")
		for _, lv := range []educationLevel{levelPostGraduate, levelDoctorate} {
			got, _, ok := pickEducationRung(ladder, lv, []string{"Master of Science"})
			if !ok || got.Label != "Masters Degree or Higher" {
				t.Fatalf("level %v: %q %v", lv, got.Label, ok)
			}
		}
		got, _, _ := pickEducationRung(ladder, levelFourYear, []string{"Bachelor of Science"})
		if got.Label != "Bachelors Degree" {
			t.Fatalf("a bachelor's holder is never offered the higher rung: %q", got.Label)
		}
	})
}

func TestAnswerBank_EducationLevel_ContradictionScoresZero(t *testing.T) {
	cases := []struct {
		claimed educationLevel
		label   string
		want    bool
	}{
		{levelPostGraduate, "Bachelor's degree", true},
		{levelPostGraduate, "Doctoral Degree", true},
		{levelPostGraduate, "Master's degree", false},
		{levelPostGraduate, "Masters Degree or Higher", false},
		{levelPostGraduate, "Other", false},
		{levelPostGraduate, "Select...", false},
		{levelFourYear, "Master's degree", true},
	}
	for _, c := range cases {
		if got := contradictsEducationLevel(c.claimed, c.label); got != c.want {
			t.Errorf("%v vs %q: got %v", c.claimed, c.label, got)
		}
	}
}

func TestAnswerBank_EducationLevel_UnclaimedDisciplinePenalty(t *testing.T) {
	if degreeSpecialisationPenalty != 0.2 {
		t.Fatalf("penalty = %v", degreeSpecialisationPenalty)
	}
	cases := []struct {
		answer, label string
		want          bool
	}{
		{"Master of Science", "Master of Science, Electrical", true},
		{"Master of Science", "Master Of Science - M S", false},
		{"Master of Science", "Master of Business Administration", true},
		{"Master of Science", "Master of Science in Education", true},
		{"Master of Science in Computer Science", "Master of Science, Computer Science", false},
		{"Master's degree", "Masters Degree or Higher", false},
		{"Master's degree", "Master's degree", false},
		{"Master's degree", "Bachelor of Fine Arts", true},
		{"Master's degree", "Other", false},
	}
	for _, c := range cases {
		if got := namesUnclaimedDiscipline(c.answer, c.label); got != c.want {
			t.Errorf("%q vs %q: got %v", c.answer, c.label, got)
		}
	}
	t.Run("a ladder rung naming an unclaimed field abstains", func(t *testing.T) {
		ladder := opts("High School", "Bachelor's degree", "Master of Science, Electrical")
		if _, note, ok := pickEducationRung(ladder, levelPostGraduate, []string{"Master of Science"}); ok || note == "" {
			t.Fatalf("ok=%v note=%q", ok, note)
		}
	})
}

func TestAnswerBank_EducationLevel_UmbrellaAndNegation(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"Master's Degree (MS/MA/etc)", false},
		{"Bachelor's Degree (BA/BS /etc)", false},
		{"Master's degree (non-MBA)", false},
		{"Master's in Business Administration (MBA)", true},
		{"Master's (MA)", true},
		{"Master's degree (non-thesis, Electrical)", true},
	}
	for _, c := range cases {
		if got := namesUnclaimedDiscipline("Master's degree", c.label); got != c.want {
			t.Errorf("%q: got %v", c.label, got)
		}
	}
	if got := withoutInitialsUmbrella("master's degree (ms/ma/etc"); got == "master's degree (ms/ma/etc" {
		t.Fatalf("an unterminated umbrella is still an umbrella: %q", got)
	}
}

func TestAnswerBank_EducationLevel_DegreeHeldYesNo(t *testing.T) {
	m := New(Bank{}, DefaultStages())
	master := pfDate(2026, 10, 1)
	bachelor := Facts{Education: []EducationFact{{School: "Example College", Degree: "BSc", End: "2017-05"}}}
	ask := func(f Facts, q string) Result {
		return pfOne(t, m, f, master, Question{Selector: "s", Text: q, Kind: KindRadioGroup, Options: opts("Yes", "No")})
	}
	cases := []struct {
		facts Facts
		q     string
		want  string // "" stands aside
	}{
		{bachelor, "Do you have a bachelor's degree?", "Yes"},
		{bachelor, "Do you have a Bachelor's degree or higher?", "Yes"},
		{bachelor, "Have you completed a 4-year degree?", "Yes"},
		{bachelor, "Do you have a master's degree?", "No"},
		{alexFacts(), "Do you have a master's degree?", "Yes"},
		{alexFacts(), "Do you hold a PhD?", "No"},
		// A degree meets an alternative; without one the alternative is the user's.
		{bachelor, "Do you have a bachelor's degree or equivalent experience?", "Yes"},
		{bachelor, "Do you have a degree from an accredited institution of higher learning and/or evidence of work experience or knowledge to compensate for absence of higher education. A minimum of three (3) years of construction work experience may be substituted and will be reviewed for acceptability.", "Yes"},
		{bachelor, "Do you have a master's degree or equivalent experience?", ""},
		// A second level is part of the degree asked, never an alternative to it.
		{bachelor, "Do you have a Bachelor's or Master's degree?", "Yes"},
		{bachelor, "Do you have a Master's or PhD degree?", "No"},
		// A field, a school or another condition is not this lane's.
		{bachelor, "Do you have a bachelor's degree in Computer Science?", ""},
		{bachelor, "Do you have a Bachelor's or Master's degree in Computer Science, Computer Engineering, or a related field?", ""},
		{bachelor, "Do you have a Bachelor's, Master's, or Ph.D. in Computer Science or a related field?", ""},
		{bachelor, "Do you have a bachelor's degree from a top-50 university?", ""},
		// Lendbuzz (Lever, live): a level the user has not reached is missed in
		// every field; an alternative after the field stays the user's.
		{alexFacts(), "Do you have a PhD in Computer Science or similar?", "No"},
		{bachelor, "Do you have a master's degree in Computer Science?", "No"},
		{bachelor, "Do you have a master's degree in Computer Science or 5 years of industry experience?", ""},
		// A degree in progress is not held; no education recorded is not "No".
		{Facts{Education: []EducationFact{{Degree: "Bachelor of Science", End: "2027-05"}}}, "Do you have a bachelor's degree?", "No"},
		{Facts{}, "Do you have a bachelor's degree?", ""},
		// A completed degree whose level or end is not read is not "No" either;
		// a blank end is a degree in progress.
		{Facts{Education: []EducationFact{{Degree: "MBBS", End: "2018-06"}}}, "Do you have a bachelor's degree?", ""},
		{Facts{Education: []EducationFact{{Degree: "B.Tech Computer Science", End: "2018-06"}}}, "Do you have a bachelor's degree?", "Yes"},
		{Facts{Education: []EducationFact{{Degree: "Bachelor of Science", End: "June 2018"}}}, "Do you have a bachelor's degree?", ""},
		{Facts{Education: []EducationFact{{Degree: "BSc", End: "2017-05"}, {Degree: "MBBS", End: "2019-05"}}}, "Do you have a master's degree?", ""},
		{Facts{Education: []EducationFact{{Degree: "BSc", End: "2017-05"}, {Degree: "MBBS", End: "2019-05"}}}, "Do you have a bachelor's degree?", "Yes"},
		{Facts{Education: []EducationFact{{Degree: "Bachelor of Science"}}}, "Do you have a bachelor's degree?", "No"},
	}
	for _, c := range cases {
		r := ask(c.facts, c.q)
		derived := r.Source == SourceDerived
		if c.want == "" && derived || c.want != "" && (!derived || r.ChosenLabel != c.want) {
			t.Errorf("%q: want %q, got %+v", c.q, c.want, r)
		}
	}
}

// Gem (RRS Group): a degree in a listed field is Yes for an entry of a level
// asked in one of the fields listed and, when the question gives one, ending in
// its graduation window; anything else is left to the user, who judges what a
// "related field" is.
func TestAnswerBank_EducationLevel_DegreeInAListedField(t *testing.T) {
	m := New(Bank{}, DefaultStages())
	today := pfDate(2026, 10, 31)
	jordan := Facts{Education: []EducationFact{
		{School: "Example University", Degree: "Bachelor of Science (B.S)", Field: "Applied Mathematics", End: "2024-06"},
		{School: "Example Institute", Degree: "Master of Science (M.S)", Field: "Computer Engineering", End: "2026-05"},
	}}
	const listed = "Do you have a Bachelor's or Master's degree in Computer Science, Computer Engineering, CIS/MIS, Cybersecurity, Business, or a related field"
	cases := []struct {
		q    string
		want string // "" left to the user
	}{
		{listed + ", with a graduation date between January 2026 and November 2026?", "Yes"},
		{listed + "?", "Yes"},
		{listed + ", with a graduation date between January 2025 and November 2025?", ""},
		{"Do you have a Bachelor's degree in Computer Science, Computer Engineering, or a related field?", ""},
		{"Do you have a Master's degree in Physics or a related field?", ""},
	}
	for _, c := range cases {
		r := pfOne(t, m, jordan, today, Question{Selector: "s", Text: c.q, Kind: KindRadioGroup, Options: opts("Yes", "No")})
		derived := r.Source == SourceDerived
		if c.want == "" && derived || c.want != "" && (!derived || r.ChosenLabel != c.want) {
			t.Errorf("%q: want %q, got %+v", c.q, c.want, r)
		}
	}
}
