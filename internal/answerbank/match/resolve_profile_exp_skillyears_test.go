package match

import "testing"

func TestAnswerBank_SkillYears_ExtractWithTail(t *testing.T) {
	cases := []struct {
		name, q, want string
	}{
		{"with tail", "How many years of work experience do you have with Python?", "Python"},
		{"using tail", "How many months of hands-on experience do you have building applications using LLMs?", "LLMs"},
		{"plain total question", "How many years of experience do you have?", ""},
		{"not a years ask", "Do you have experience with Python?", ""},
		{"stopword only residue is still a tail", "Years of professional experience with the tools", "the tools"},
		// Nomic (Ashby): the skill ends with the question; the guidance after it is no part of it.
		{"the question ends the skill", "How many years of experience working full time with biological data and algorithms do you have? Please provide an accurate estimate based on projects that you have worked on that primarily revolved around building algorithms and pipelines.", "biological data and algorithms"},
		{"a later sentence names no skill", "How many years of Python experience do you have? Please share details with us.", ""},
		// Island (Greenhouse, queue161): the skill follows the work done, and a
		// short "in <skill>" ends it.
		{"the work done in a language", "How many years of hands-on professional software development experience do you have developing production software in Go?", "Go"},
		{"the work done", "How many years of hands-on professional experience do you have building large-scale distributed systems in a cloud environment (e.g., AWS, GCP, Azure)?", "large-scale distributed systems in a cloud environment (e.g., AWS, GCP, Azure)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := yearsSkillFromQuestion(c.q); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
	t.Run("PR-041 matcher: years with a skill answers a whole number", func(t *testing.T) {
		r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1),
			textQ("s", "How many years of work experience do you have with Python?"))
		if r.Outcome != OutcomeFilled || r.Answer != "3" || r.Source != SourceDerived {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("PR-041 matcher: a plain total question is not claimed", func(t *testing.T) {
		r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1),
			textQ("s", "How many years of experience do you have?"))
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonNoEntry {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("PR-041 matcher: other control kinds without options defer", func(t *testing.T) {
		q := Question{Selector: "s", Kind: KindCustomListbox, Text: "How many years of work experience do you have with Python?"}
		r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1), q)
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonNoEntry {
			t.Fatalf("result = %+v", r)
		}
	})
}

func TestAnswerBank_SkillYears_EmbeddedSubjectFallback(t *testing.T) {
	cases := []struct {
		name, q, want string
	}{
		{"embedded subject", "How many years of Python experience do you currently have?", "Python"},
		{"bare label", "Years of backend development*", "backend development"},
		{"needs the how many lead", "Do you have 3+ years of Java experience?", ""},
		{"stopword residue names no skill", "How many years of industry experience do you have?", ""},
		{"stopword residue hands-on", "How many years of hands-on experience do you have?", ""},
		{"with tail belongs to the primary lane", "How many years of Python experience do you have with Go?", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := embeddedYearsSubject(c.q); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
	t.Run("PR-042 matcher: no classified entry answers from the profile", func(t *testing.T) {
		r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1),
			textQ("s", "How many years of Python experience do you currently have?"))
		if r.Answer != "3" || r.Outcome != OutcomeFilled {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("PR-042 matcher: a classified entry keeps the question", func(t *testing.T) {
		total := ent("years-experience-total", "fixed", withAnswer("4"), withExamples("How many years of experience do you have"))
		r := pfOne(t, pfMatcher(total), alexFacts(), pfDate(2026, 10, 1),
			textQ("s", "How many years of Python experience do you currently have?"))
		if r.Source == SourceDerived {
			t.Fatalf("the embedded lane must stand down when an entry classifies: %+v", r)
		}
	})
}

func TestAnswerBank_SkillYears_MonthsUnit(t *testing.T) {
	q := textQ("s", "How many months of hands-on experience do you have using Python?")
	r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1), q)
	if r.Answer != "36" {
		t.Fatalf("result = %+v", r)
	}
	if experienceUnit("How many years of experience") != "years" || experienceUnit("How many Months") != "months" {
		t.Fatal("unit detection")
	}
	frac := alexFacts()
	two := 2.5
	frac.Skills = []SkillFact{{Name: "Rust", Years: &two}}
	if got := pfOne(t, pfMatcher(), frac, pfDate(2026, 10, 1), textQ("s", "How many months of experience do you have with Rust?")).Answer; got != "30" {
		t.Fatalf("2.5 years in months = %q", got)
	}
	if got := pfOne(t, pfMatcher(), frac, pfDate(2026, 10, 1), textQ("s", "How many years of experience do you have with Rust?")).Answer; got != "2" {
		t.Fatalf("2.5 years floors to %q", got)
	}
}

func TestAnswerBank_SkillYears_TokenMatching(t *testing.T) {
	if got := skillTokens("Node.js (Backend) / C++ and C#"); len(got) != 4 || got[0] != "node.js" || got[2] != "c++" || got[3] != "c#" {
		t.Fatalf("tokens = %v", got)
	}
	if got := skillTokens("the work experience years"); len(got) != 0 {
		t.Fatalf("stop words leaked: %v", got)
	}
	cases := []struct{ in, want string }{{"LLMs", "llm"}, {"iOS", "ios"}, {"AWS", "aws"}, {"JS", "js"}, {"class", "class"}}
	for _, c := range cases {
		if got := skillTokens(c.in); len(got) != 1 || got[0] != c.want {
			t.Errorf("fold %q = %v, want %s", c.in, got, c.want)
		}
	}
	y := func(v float64) *float64 { return &v }
	skills := []SkillFact{{Name: "LLM", Years: y(2)}, {Name: "Microsoft Excel", Years: y(5)}}
	if got, _, ok := profileSkillYears("hands-on LLMs such as GPT", skills); !ok || got != 2 {
		t.Fatalf("plural fold: %v %v", got, ok)
	}
	if _, _, ok := profileSkillYears("Microsoft Word", skills); ok {
		t.Fatal("an Excel row must not answer Word")
	}
	// A skill named in one or two letters is read only where the question
	// writes it as a name of its own.
	short := []SkillFact{{Name: "Go", Years: y(8)}, {Name: "R", Years: y(6)}, {Name: "C", Years: y(7)}}
	for asked, want := range map[string]float64{
		"Go": 8, "Go or Python": 8, "Python, R and SQL": 6, "C and C++": 7, "Go (Golang)": 8,
		"go-to-market strategy": 0, "R&D": 0, "Series C startups": 0, "a C-level audience": 0,
	} {
		if got, _, ok := profileSkillYears(asked, short); ok != (want > 0) || got != want {
			t.Errorf("%q: got %v %v, want %v", asked, got, ok, want)
		}
	}
	f := Facts{Skills: short}
	if r := pfOne(t, pfMatcher(), f, pfDate(2026, 10, 1), textQ("s", "How many years of experience do you have with go-to-market strategy?")); r.Answer == "8" {
		t.Fatalf("go-to-market answered from Go: %+v", r)
	}
	if r := pfOne(t, pfMatcher(), f, pfDate(2026, 10, 1), yesNoQ("s", "Do you have 3+ years of experience with go-to-market strategy?")); r.ChosenLabel == "Yes" {
		t.Fatalf("a go-to-market threshold met by Go: %+v", r)
	}
	if r := pfOne(t, pfMatcher(), f, pfDate(2026, 10, 1), yesNoQ("s", "Do you have 3+ years of experience with Go?")); r.ChosenLabel != "Yes" {
		t.Fatalf("a Go threshold: %+v", r)
	}
	if _, _, state := profileSkillLevel("Which best describes how you go about using AI coding assistants?", []SkillFact{{Name: "Go", Level: "advanced"}}); state == "ok" {
		t.Fatal("the verb go read as the skill Go")
	}
	if name, _, state := profileSkillLevel("Which best describes your experience with Go?", []SkillFact{{Name: "Go", Level: "advanced"}}); state != "ok" || name != "Go" {
		t.Fatalf("Go named: %q %q", name, state)
	}
}

// The owner's default: a skill with no recorded years, which the
// search cannot show absent, takes one year, for review.
func TestAnswerBank_SkillYears_UnlistedSkillTakesOneYear(t *testing.T) {
	for _, q := range []string{
		"How many years of work experience do you have with COBOL?",
		"How many years of work experience do you have with Go?", // listed, but no years recorded
	} {
		r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1), textQ("s", q))
		if r.Outcome != OutcomeFilledReview || r.Answer != "1" {
			t.Errorf("%q: result = %+v", q, r)
		}
	}
	// Island's buckets: Go is listed with no years, so the lowest bucket.
	island := Question{Selector: "s", Kind: KindCustomListbox, InputType: InputSingleSelect, Options: opts("1-2 years", "3-5 years", "5+ years"),
		Text: "How many years of hands-on professional software development experience do you have developing production software in Go?"}
	// It is the owner's default, so a review that finds the skill absent from
	// the profile advises rather than blocks.
	if r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1), island); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "1-2 years" || r.Source != SourceOwnerDefault {
		t.Errorf("Island: %+v", r)
	}
	empty := alexFacts()
	empty.Skills = nil
	r := pfOne(t, pfMatcher(), empty, pfDate(2026, 10, 1), textQ("s", "How many years of experience do you have using SQL?"))
	if r.Outcome != OutcomeFilledReview || r.Answer != "1" {
		t.Fatalf("no skills: %+v", r)
	}
	// A Yes/No is left non-terminal for the option lane's experience rule.
	if r := pfOne(t, pfMatcher(), alexFacts(), Date{}, yesNoQ("s", "Do you have 10+ years of experience with COBOL?")); r.Reason != ReasonNoEntry || r.Terminal {
		t.Fatalf("yes/no: %+v", r)
	}
}

func TestAnswerBank_SkillYears_BucketControl(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   string
	}{
		{"range ladder", []string{"Select...", "0", "1-3", "4-6", "7+"}, "1-3"},
		{"open top", []string{"0-1", "2-4", "5+ years"}, "2-4"},
		{"shared edge goes to the bucket it starts", []string{"0-3", "3-6", "6+"}, "3-6"},
		{"less than", []string{"Less than 1 year", "1 to 2 years", "3 or more years"}, "3 or more years"},
		{"bare integers", []string{"0", "1", "2", "3", "4", "5+"}, "3"},
		{"a label with both bounds", []string{"Less than 1 year", "More than 1 year but less than 3 years", "More than 3 years but less than 5 years", "More than 5 years"}, "More than 3 years but less than 5 years"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := pfSelect("s", "How many years of experience do you have with Python?", c.labels...)
			r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1), q)
			if r.Outcome != OutcomeFilled || r.ChosenLabel != c.want || r.Source != SourceDerived {
				t.Fatalf("result = %+v", r)
			}
		})
	}
	t.Run("a fraction above a strict bound is past it", func(t *testing.T) {
		f, y := alexFacts(), 5.5
		f.Skills[0].Years = &y
		for want, labels := range map[string][]string{
			"More than 5 years": {"0-2 years", "3-5 years", "More than 5 years"},
			"Over 5":            {"0-2", "3-5", "Over 5"},
			"3-5 years":         {"0-2 years", "3-5 years", "6+ years"},
		} {
			q := pfSelect("s", "How many years of experience do you have with Python?", labels...)
			if r := pfOne(t, pfMatcher(), f, pfDate(2026, 10, 1), q); r.ChosenLabel != want {
				t.Errorf("want %q: %+v", want, r)
			}
		}
	})
	t.Run("a bucket in months is read in months", func(t *testing.T) {
		mixed := []string{"Less than 6 months", "6 months to 1 year", "1-2 years", "3-5 years", "5+ years"}
		months := []string{"0-6 months", "6-12 months", "1-2 years", "2-5 years"}
		for _, c := range []struct {
			skill  string
			years  float64
			labels []string
			want   string
		}{
			{"Python", 4, mixed, "3-5 years"}, {"Python", 0.75, mixed, "6 months to 1 year"}, {"Python", 0.25, mixed, "Less than 6 months"},
			{"Python", 4, months, "2-5 years"}, {"Python", 0.75, months, "6-12 months"},
			// Go has no recorded years: the lowest bucket is the one in months.
			{"Go", -1, []string{"6-12 months", "1-2 years", "3+ years"}, "6-12 months"},
		} {
			f := alexFacts()
			if c.years >= 0 {
				f.Skills[0].Years = &c.years
			}
			q := pfSelect("s", "How many years of experience do you have with "+c.skill+"?", c.labels...)
			if r := pfOne(t, pfMatcher(), f, pfDate(2026, 10, 1), q); r.ChosenLabel != c.want {
				t.Errorf("%s %v over %v: want %q, got %+v", c.skill, c.years, c.labels, c.want, r)
			}
		}
	})
	t.Run("no bucket holds the figure", func(t *testing.T) {
		q := pfSelect("s", "How many years of experience do you have with Python?", "0", "1", "2")
		r := pfOne(t, pfMatcher(), alexFacts(), pfDate(2026, 10, 1), q)
		if r.Outcome != OutcomeAbstained || r.ChosenLabel != "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a profile link never answers a years bucket", func(t *testing.T) {
		gh := ent("github-url", "fixed", withAnswer("https://github.com/alex-example"))
		r := pfOne(t, pfMatcher(gh), alexFacts(), pfDate(2026, 10, 1),
			textQ("s", "How many years of work experience do you have with GitHub?"))
		if r.Answer == "https://github.com/alex-example" {
			t.Fatalf("result = %+v", r)
		}
	})
}

func TestAnswerBank_YearsLadder_BareIntegerRungs(t *testing.T) {
	ladder := opts("0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10+")
	cases := []struct {
		whole int
		want  string
		ok    bool
	}{{3, "3", true}, {0, "0", true}, {9, "9", true}, {14, "10+", true}}
	for _, c := range cases {
		got, ok := pickIntegerYearsRung(ladder, c.whole)
		if ok != c.ok || got.Label != c.want {
			t.Errorf("%d: got %q %v", c.whole, got.Label, ok)
		}
	}
	if _, ok := pickIntegerYearsRung(opts("0", "1", "2"), 5); ok {
		t.Fatal("a ladder that stops below the number is deferred, never rounded down")
	}
	if got, _ := pickIntegerYearsRung(opts("1 year", "2 years", "3+ years"), 2); got.Label != "2 years" {
		t.Fatalf("unit words: %q", got.Label)
	}
	if _, ok := pickIntegerYearsRung(opts("Select...", "5"), 3); ok {
		t.Fatal("a placeholder is not a rung")
	}
}

func TestAnswerBank_Experience_TotalYearsBucket(t *testing.T) {
	menu := opts("0-1 years", "2-3 years", "4-6 years", "7+ years")
	cases := []struct {
		stored string
		want   string
		ok     bool
	}{{"3", "2-3 years", true}, {"2-4 years", "2-3 years", true}, {"5+ years", "4-6 years", true}, {"0-1 years", "0-1 years", true}, {"", "", false}, {"several", "", false},
		{"Less than 1 year", "0-1 years", true}}
	for _, c := range cases {
		got, ok := yearsEntryBucket(c.stored, menu)
		if ok != c.ok || got.Label != c.want {
			t.Errorf("%q: got %q %v", c.stored, got.Label, ok)
		}
	}
	if _, ok := yearsEntryBucket("30", opts("0-1 years", "2-3 years")); ok {
		t.Fatal("defers instead of guessing when no bucket fits the total")
	}
	if got, _ := yearsEntryBucket("Less than 1 year", opts("Less than 1 year", "1-2 years", "3+ years")); got.Label != "Less than 1 year" {
		t.Fatalf("less than one year: %q", got.Label)
	}
	// The shipped total entry takes the bucket of the range it stores.
	total := fixedEnt(IDYearsFullTimeWork, "Years of full-time work experience", "5+")
	q := pfSelect("s", "Years of full-time work experience", "0-1 years", "2-4 years", "5-7 years", "8+ years")
	if r := pfOne(t, pfMatcher(total), alexFacts(), pfDate(2026, 10, 1), q); r.ChosenLabel != "5-7 years" {
		t.Fatalf("the shipped total entry over a menu: %+v", r)
	}
}

func TestAnswerBank_SkillYears_NeverReturnsProfileUrl(t *testing.T) {
	y := 4.0
	f := alexFacts()
	f.Skills = append(f.Skills, SkillFact{Name: "GitHub", Years: &y})
	gh := ent("github-url", "fixed", withAnswer("https://github.com/alex-example"), withExamples("GitHub"))
	t.Run("text", func(t *testing.T) {
		r := pfOne(t, pfMatcher(gh), f, pfDate(2026, 10, 1), textQ("s", "How many years of work experience do you have with GitHub?"))
		if r.Answer != "4" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("select", func(t *testing.T) {
		q := pfSelect("s", "How many years of work experience do you have with GitHub?", "0", "1-3", "4-6", "7+")
		r := pfOne(t, pfMatcher(gh), f, pfDate(2026, 10, 1), q)
		if r.ChosenLabel != "4-6" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a genuine profile question still gets the URL", func(t *testing.T) {
		if yearsSkillFromQuestion("What is your GitHub profile URL?") != "" {
			t.Fatal("the lane must not claim a profile-link question")
		}
	})
}

// F-10 in years: a skill none of the user's information names has no years
// with it (Huntress's Ruby on Rails box, InRule's C# and .NET menu), and a
// level-of-experience menu of years ranges is a years question (InRule's
// React). A skill the résumé names, or a search that could not read
// everything, takes the owner's default of one year, for review.
func TestAnswerBank_SkillYears_NoneOfYourInformationNamesIt(t *testing.T) {
	f := alexFacts()
	f.KnowledgeRead = true
	today := pfDate(2026, 10, 1)
	ruby := textQ("s", "How many years of experience do you have working with Ruby on Rails?")
	if r := pfOne(t, pfMatcher(), f, today, ruby); r.Outcome == OutcomeAbstained || r.Answer != "0" {
		t.Fatalf("absent skill: %+v", r)
	}
	menu := func(text string) Question {
		return pfSelect("s", text, "None", "Less than 2 years", "2 to 4 years", "5+ years")
	}
	if r := pfOne(t, pfMatcher(), f, today, menu("What is your level of professional experience with C# and .NET?")); r.ChosenLabel != "None" {
		t.Fatalf("absent skill on a menu: %+v", r)
	}
	if r := pfOne(t, pfMatcher(), f, today, menu("What is your level of professional experience with React?")); r.ChosenLabel != "2 to 4 years" {
		t.Fatalf("recorded skill on a menu: %+v", r)
	}
	named := f
	named.Evidence = []string{"Built a Ruby on Rails admin app"}
	if r := pfOne(t, pfMatcher(), named, today, ruby); r.Outcome != OutcomeFilledReview || r.Answer != "1" {
		t.Fatalf("the résumé names it: %+v", r)
	}
	if r := pfOne(t, pfMatcher(), alexFacts(), today, ruby); r.Outcome != OutcomeFilledReview || r.Answer != "1" {
		t.Fatalf("an incomplete search: %+v", r)
	}
}

// F-10 for events: a list of conferences or career fairs (P&G) is answered
// with its option for none when none of the user's information names a listed
// event by its whole name or its initials; one it names, or a search that could
// not read everything, answers nothing.
func TestAnswerBank_EventsAttended_NoneOfYourInformationNamesThem(t *testing.T) {
	f := alexFacts()
	f.KnowledgeRead = true
	today := pfDate(2026, 10, 1)
	q := pfSelect("s", "Did you attend or will you be attending any of the following conferences/career fairs?",
		"AISES", "Getting Hired Career Fair", "MBA Veterans Conference", "NSBE", "Out for Undergrad (O4U)", "SACC - Service Academy Career Conference", "SWE", "Not Applicable")
	if r := pfOne(t, pfMatcher(), f, today, q); r.ChosenLabel != "Not Applicable" {
		t.Fatalf("no event named: %+v", r)
	}
	for _, line := range []string{"Member of the National Society of Black Engineers", "Met recruiters at the Getting Hired Career Fair", "Volunteered at Out for Undergrad", "SACC 2025 attendee"} {
		named := f
		named.Evidence = []string{line}
		if r := pfOne(t, pfMatcher(), named, today, q); r.Outcome != OutcomeAbstained {
			t.Fatalf("%q: %+v", line, r)
		}
	}
	if r := pfOne(t, pfMatcher(), alexFacts(), today, q); r.Outcome != OutcomeAbstained {
		t.Fatalf("an incomplete search: %+v", r)
	}
}

func TestAnswerBank_YearsThreshold_AnswersFromTheRecordedYears(t *testing.T) {
	total := fixedEnt(IDYearsFullTimeWork, "Years of full-time work experience", "2")
	f := alexFacts() // Python 3 years, React 2, Go with no years
	f.KnowledgeRead = true
	cases := []struct {
		name, q, want string
		bank          []Entry
	}{
		{"a skill at the floor meets it", "Do you have a minimum of 3+ years of experience coding in Python?", "Yes", nil},
		{"a skill below the floor however the question narrows it (Baton)", "Do you have 4+ years of experience building and scaling backend Python-based services?", "No", nil},
		{"a strict floor the skill only reaches", "Do you have more than 2 years of experience with React?", "No", nil},
		{"a subject none of the information names (F-10)", "Do you have at least 5 years of professional Ruby on Rails experience?", "No", nil},
		{"experience the question does not narrow, from the total", "Do you have at least 2 years of professional experience?", "Yes", []Entry{total}},
		{"the total below the floor", "Do you have at least 10 years of professional development experience?", "No", []Entry{total}},
		{"a field the total is below", "Do you have 5+ years of software engineering experience?", "No", []Entry{total}},
		// H Company (Ashby, live): a spelled-out figure over years spent doing something nothing recorded names.
		{"years spent doing something none of the information names", "Do you have more than three years deploying solutions at customers ?", "No", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, pfMatcher(c.bank...), f, pfDate(2026, 11, 1), yesNoQ("s", c.q))
			if r.Outcome != OutcomeFilled || r.ChosenLabel != c.want || r.Source != SourceDerived {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}

// BitGo (Greenhouse, live): "Do you have 5+ years of backend software
// engineering experience?" with a total of one year and a CV that mentions
// backend work. A narrower subject inside the field, or narrower professional
// experience, is never more years than the total; experience the question
// does not place in the field or in work may come from anywhere.
func TestAnswerBank_YearsThreshold_TheTotalBoundsANarrowerSubject(t *testing.T) {
	total := fixedEnt(IDYearsFullTimeWork, "Years of full-time work experience", "2")
	f := alexFacts()
	f.KnowledgeRead = true
	f.Evidence = []string{"Built backend services and Kubernetes deployments for an internal platform"}
	cases := []struct {
		name, q, want string
	}{
		{"a narrower subject in a field the total is below", "Do you have 5+ years of backend software engineering experience?", "No"},
		{"narrower professional experience the total is below", "Do you have 3+ years of professional experience with Kubernetes?", "No"},
		{"a narrower subject in a field the total reaches", "Do you have 2+ years of backend software engineering experience?", ""},
		{"a subject the question does not place in the field or in work", "Do you have 3+ years of experience with Kubernetes?", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, pfMatcher(total), f, pfDate(2026, 11, 1), yesNoQ("s", c.q))
			if c.want == "" && r.Outcome == OutcomeFilled || c.want != "" && (r.Outcome != OutcomeFilled || r.ChosenLabel != c.want || r.Source != SourceDerived) {
				t.Fatalf("want %q, result = %+v", c.want, r)
			}
		})
	}
}

func TestAnswerBank_YearsThreshold_LeavesWhatTheYearsDoNotSettle(t *testing.T) {
	total := fixedEnt(IDYearsFullTimeWork, "Years of full-time work experience", "2")
	f := alexFacts()
	f.KnowledgeRead = true
	cases := []struct {
		name, q string
		bank    []Entry
	}{
		{"a skill that meets it inside a narrower subject", "Do you have 3+ years of experience building backend services in Python?", nil},
		{"a skill the profile names without years", "Do you have 2+ years of experience with Go?", nil},
		{"a field the total reaches", "Do you have 1+ years of software engineering experience?", []Entry{total}},
		{"no total recorded", "Do you have at least 10 years of professional development experience?", nil},
		{"an alternative way to qualify", "Do you have 5+ years of experience with Python or a master's degree?", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, pfMatcher(c.bank...), f, pfDate(2026, 11, 1), yesNoQ("s", c.q))
			if r.Outcome == OutcomeFilled {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}

// Initials spell a capitalised run of the question, never a word that is an
// acronym itself: Akuity's "B2B SaaS" is no "BS", so a BS degree does not
// name technical marketing; "National Society of Black
// Engineers" still spells the NSBE the user wrote.
func TestOwnMentions_InitialsSpellOnlyCapitalisedWords(t *testing.T) {
	f := Facts{KnowledgeRead: true, Education: []EducationFact{{School: "University of Westbridge", Degree: "BS", Field: "Applied Mathematics"}}, Evidence: []string{"NSBE member since 2021"}}
	if o := NewOwnSources(f, nil).Mentions("How many years of experience do you have in a technical marketing role at a B2B SaaS company?", "technical marketing"); !o.Absent() {
		t.Errorf("technical marketing named in %v", o.Found)
	}
	if o := NewOwnSources(f, nil).Mentions("Are you a member of the National Society of Black Engineers?", "National Society of Black Engineers"); o.Absent() {
		t.Errorf("NSBE not found: %+v", o)
	}
}

// Marketing is not its stem's market, and the field's "technical" names no
// subject beside another word: a market-data tool with technical indicators
// is no technical marketing, while marketing the user did is found.
func TestSubjectTerms_FieldWordsAndIngNouns(t *testing.T) {
	f := Facts{KnowledgeRead: true, Evidence: []string{"Tools include market data, technical indicators, news and research."}}
	const q = "How many years of experience do you have in a technical marketing role?"
	if o := NewOwnSources(f, nil).mentions(q, q, subjectTerms("technical marketing")); !o.Absent() {
		t.Errorf("technical marketing named in %v", o.Found)
	}
	f.Evidence = append(f.Evidence, "Ran the product marketing launch for the SDK.")
	if o := NewOwnSources(f, nil).mentions(q, q, subjectTerms("technical marketing")); o.Absent() {
		t.Errorf("the user's marketing not found: %+v", o)
	}
	// A consent says what the user allows, not what they did: an opt-in to
	// marketing email names no marketing role (Akuity, live).
	optIn := []Entry{{ID: "marketing-email-optin", Description: "Opt-in to marketing email", Kind: KindFixed, Answer: "Yes", Category: CategoryConsent, Examples: []string{"Send me job alerts by email"}}}
	f.Evidence = f.Evidence[:1]
	if o := NewOwnSources(f, optIn).mentions(q, q, subjectTerms("technical marketing")); !o.Absent() {
		t.Errorf("technical marketing named in %v", o.Found)
	}
	if got := subjectTerms("technical"); len(got) != 1 {
		t.Errorf("a subject of a field word alone keeps it: %v", got)
	}
}
