package match

import (
	"math"
	"slices"
	"testing"
)

func TestAnswerBank_FieldOfStudy_ByDegreeLevel(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	cases := []struct{ name, text, want string }{
		{"field of study defaults to the most recent", "What was your field of study?", "Computer Science"},
		{"undergraduate wording", "What was your undergraduate major?", "Mathematics"},
		{"bachelor wording", "Bachelor's degree discipline", "Mathematics"},
		{"graduate wording", "What was your graduate degree major?", "Computer Science"},
		{"degree initials", "Which discipline was your MS degree in?", "Computer Science"},
		{"course of study", "Course of study", "Computer Science"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, m, f, pfToday, textQ("c", c.text))
			pfWant(t, r, c.want)
			if r.Confidence != 0.90 || r.Source != SourceDerived {
				t.Fatalf("result = %+v", r)
			}
		})
	}
	t.Run("a bare major accomplishments prompt is not a field of study", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, textQ("c", "List your major accomplishments")), ReasonNarrativeDeferred, false)
	})
	t.Run("a major project of the college years is not a field of study", func(t *testing.T) {
		for _, text := range []string{"Describe a major project from your college coursework", "In college, what major challenge did you overcome?"} {
			if r := pfOne(t, m, f, pfToday, textQ("c", text)); traceHas(r, "field-of-study") {
				t.Errorf("%q: %+v", text, r)
			}
		}
	})
	t.Run("an option list binds the field", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, pfSelect("c", "Field of study", "Biology", "Computer Science", "History"))
		pfWant(t, r, "Computer Science")
	})
	t.Run("an option list without the field falls through", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, pfSelect("c", "Field of study", "Biology", "History")), ReasonNoEntry, false)
	})
	t.Run("an empty area falls through", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", End: "2020-05"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, textQ("c", "Field of study")), ReasonNoEntry, false)
	})
	t.Run("an indexed row is not a field-of-study question", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("discipline--1", "Field of study")), "Mathematics")
	})
	t.Run("a level no entry holds borrows no other degree's field", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Degree: "Master of Arts", Field: "History", End: "2020-05"}, {School: "B", Degree: "MBBS", Field: "Medicine", End: "2018-05"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, textQ("c", "Undergraduate major")), ReasonNoEntry, false)
		pfAbstain(t, pfOne(t, m, Facts{Education: g.Education[1:]}, pfToday, textQ("c", "What was your graduate degree major?")), ReasonNoEntry, false)
	})
}

func TestAnswerBank_FieldOfStudy_DescribeProgramSentence(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	q := textQ("c", "In 1-2 sentences, please describe your MS program, including your primary area of study.")
	want := "I completed my Master of Science at Example State University (September 2024 \u2013 May 2026), with Computer Science as my primary area of study."
	t.Run("after graduation", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 10, 2), q), want)
	})
	t.Run("before graduation the tense comes from the clock", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2025, 10, 2), q)
		pfWant(t, r, "I am pursuing my Master of Science at Example State University (September 2024 \u2013 May 2026), with Computer Science as my primary area of study.")
	})
	t.Run("missing parts are omitted, nothing is invented", func(t *testing.T) {
		got, ok := describeProgram("Tell us about your program", EducationFact{Field: "Physics"}, "Physics", pfToday)
		if !ok || got != "I am pursuing my degree, with Physics as my primary area of study." {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("a bare major question keeps the major", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "What is your field of study?")), "Computer Science")
	})
	t.Run("a control with options never gets a sentence", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, pfSelect("c", "Describe your field of study", "Computer Science", "History"))
		pfWant(t, r, "Computer Science")
	})
}

func TestAnswerBank_CurrentSchool_ResolvesFromHistory(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	t.Run("the in-progress entry", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 1, 10), textQ("c", "Which university do you currently attend?"))
		pfWant(t, r, "Example State University")
		if r.Confidence != 0.90 {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("re-confirm wording", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "Please re-confirm the university you attend")), "Example State University")
	})
	t.Run("current institution", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "Current institution")), "Example State University")
	})
	t.Run("a school list binds the option", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, pfSelect("c", "Which school do you currently attend?", "Example College", "Example State University"))
		pfWant(t, r, "Example State University")
	})
	t.Run("a Yes/No select is untouched", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, yesNoQ("c", "Are you currently attending school?")), ReasonNoEntry, false)
	})
	// The owner's rule (Equifax): a list of schools without the
	// applicant's own takes its none-of-these choice, else its "Other".
	equifax := []string{"Georgia State University", "Kennesaw State University", "Georgia Institute of Technology", "University of Georgia", "St. Louis University", "Washington University in St. Louis"}
	const attending = "Are you currently attending one of the following schools?"
	t.Run("a list without the school takes its none choice", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("c", attending, append(slices.Clone(equifax), "Other", "None of the above")...)), "None of the above")
	})
	t.Run("a list without the school or a none choice takes Other", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("c", attending, append(slices.Clone(equifax), "Other")...)), "Other")
	})
	t.Run("a list that holds the school takes it over Other", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("c", attending, "Georgia Institute of Technology", "Example State University", "Other")), "Example State University")
	})
	t.Run("an option that may spell the school otherwise is not guessed", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, pfSelect("c", attending, append(slices.Clone(equifax), "Example State University - Main Campus", "Other")...))
		if r.Outcome != OutcomeAbstained || r.ChosenLabel != "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a school written as an acronym of a listed one is not guessed", func(t *testing.T) {
		mit := Facts{Education: []EducationFact{{School: "MIT", End: "2026-05"}}}
		r := pfOne(t, m, mit, pfToday, pfSelect("c", attending, "Massachusetts Institute of Technology", "Harvard University", "Other"))
		if r.ChosenLabel != "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a box that asks only for a school's name", func(t *testing.T) {
		for _, label := range []string{"Enter school name below:", "School Name", "Please enter your university name"} {
			pfWant(t, pfOne(t, m, f, pfToday, textQ("c", label)), "Example State University")
		}
		for _, label := range []string{"High school name", "Name of your undergraduate school", "School name and location of your clinical rotation"} {
			if r := pfOne(t, m, f, pfToday, textQ("c", label)); r.Answer == "Example State University" && r.Source == SourceDerived {
				t.Errorf("%q: %+v", label, r)
			}
		}
	})
	t.Run("an Other beside no school list is untouched", func(t *testing.T) {
		if r := pfOne(t, m, f, pfToday, pfSelect("c", "Are you currently attending school?", "Yes", "No", "Other")); r.ChosenLabel == "Other" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("an indexed row is the row resolver's", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("school--1", "Which university do you currently attend?")), "Example College")
	})
	t.Run("no education", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, Facts{}, pfToday, textQ("c", "Which university do you currently attend?")), ReasonNoEntry, false)
	})
}

func TestAnswerBank_Gpa_ParseBracketRange(t *testing.T) {
	inf := math.Inf(1)
	cases := []struct {
		label    string
		ok       bool
		lo, high float64
	}{
		{"3.0-3.4", true, 3.0, 3.4},
		{"3.4 to 3.0", true, 3.0, 3.4},
		{"Less than 2.0", true, math.Inf(-1), 2.0},
		{"Below 2.5", true, math.Inf(-1), 2.5},
		{"Under 2.5", true, math.Inf(-1), 2.5},
		{"Lower than 2.5", true, math.Inf(-1), 2.5},
		{"<2.0", true, math.Inf(-1), 2.0},
		{"3.5+", true, 3.5, inf},
		{"3.5 or above", true, 3.5, inf},
		{"3.5 or higher", true, 3.5, inf},
		{"Above 3.5", true, 3.5, inf},
		{"At least 3.5", true, 3.5, inf},
		{"Greater than 3.5", true, 3.5, inf},
		{">=3.5", true, 3.5, inf},
		{"\u22653.5", true, 3.5, inf},
		{"3.7", true, 3.7, 3.7},
		{"Not applicable", false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			r, ok := parseGpaBracket(c.label)
			if ok != c.ok || (ok && (r.low != c.lo || r.high != c.high)) {
				t.Fatalf("got %+v %t", r, ok)
			}
		})
	}
}

func TestAnswerBank_Gpa_BracketPicker(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	brackets := []string{"Less than 2.0", "2.0-2.9", "3.0-3.4", "3.5-4.0"}
	ask := func(text string, labels ...string) Question { return pfSelect("c", text, labels...) }
	t.Run("an unscoped question reads the most recent entry", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, ask("What is your GPA?", brackets...))
		pfWant(t, r, "3.5-4.0")
		if r.Confidence != 0.90 {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("undergraduate wording reads the bachelor entry", func(t *testing.T) {
		g := eduOnly()
		g.Education[1].GradeAverage = "3.2"
		pfWant(t, pfOne(t, m, g, pfToday, ask("Undergraduate Academic Performance Rating", brackets...)), "3.0-3.4")
	})
	t.Run("graduate wording reads the master entry", func(t *testing.T) {
		g := eduOnly()
		g.Education[1].GradeAverage = "3.2"
		pfWant(t, pfOne(t, m, g, pfToday, ask("Postgraduate GPA", brackets...)), "3.5-4.0")
	})
	t.Run("a scoped question without a matching record does not borrow another degree", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Degree: "Master of Science", End: "2026-05", GradeAverage: "3.9"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, ask("Undergraduate GPA", brackets...)), ReasonNoEntry, false)
	})
	t.Run("a degree the profile does not hold takes the list's not-applicable option", func(t *testing.T) {
		// SpaceX and Freeform list GPA (Undergraduate), (Graduate) and (Doctorate)
		// for a bachelor and a master: the doctorate one took the master's GPA.
		g := eduOnly()
		g.Education[1].GradeAverage = "3.2"
		na := append([]string{"Not applicable/Do not recall"}, brackets...)
		pfWant(t, pfOne(t, m, g, pfToday, ask("GPA (Doctorate)", na...)), "Not applicable/Do not recall")
		pfWant(t, pfOne(t, m, g, pfToday, ask("GPA (Graduate)", na...)), "3.5-4.0")
		pfWant(t, pfOne(t, m, g, pfToday, ask("GPA (Undergraduate)", na...)), "3.0-3.4")
		bachelor := Facts{Education: []EducationFact{{School: "A", Degree: "Bachelor of Science", End: "2024-05", GradeAverage: "3.8"}}}
		pfWant(t, pfOne(t, m, bachelor, pfToday, ask("GPA (Graduate)", append([]string{"N/A"}, brackets...)...)), "N/A")
		pfAbstain(t, pfOne(t, m, g, pfToday, ask("GPA (Doctorate)", brackets...)), ReasonNoEntry, false)
		// A graduate is a person, not graduate study: the bachelor's GPA is asked.
		const person = "If you are a recent graduate, what was your cumulative GPA?"
		pfWant(t, pfOne(t, m, bachelor, pfToday, ask(person, append([]string{"N/A"}, brackets...)...)), "3.5-4.0")
		pfWant(t, pfOne(t, m, bachelor, pfToday, textQ("c", person)), "3.8")
		pfWant(t, pfOne(t, m, bachelor, pfToday, textQ("c", "As a college graduate, what was your GPA when you did graduate?")), "3.8")
		pfAbstain(t, pfOne(t, m, bachelor, pfToday, textQ("c", "GPA in your university graduate program")), ReasonNoEntry, false)
		if under, grad := levelScope("I am a graduate or undergraduate student"); !under || !grad {
			t.Errorf("both levels named: undergraduate %t, graduate %t", under, grad)
		}
	})
	t.Run("the tightest range wins on a shared boundary", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", End: "2026-05", GradeAverage: "3.5"}}}
		pfWant(t, pfOne(t, m, g, pfToday, ask("GPA", "3.0-3.5", "3.5+", "Below 3.0")), "3.0-3.5")
	})
	t.Run("below and above leave their bound out", func(t *testing.T) {
		at := func(gpa string) Facts {
			return Facts{Education: []EducationFact{{School: "A", End: "2026-05", GradeAverage: gpa}}}
		}
		pfWant(t, pfOne(t, m, at("3.0"), pfToday, ask("GPA", "Below 3.0", "3.0 or above")), "3.0 or above")
		pfWant(t, pfOne(t, m, at("3.8"), pfToday, ask("GPA", "< 3.5", "> 3.5")), "> 3.5")
		pfAbstain(t, pfOne(t, m, at("3.5"), pfToday, ask("GPA", "< 3.5", "> 3.5")), ReasonNoEntry, false)
	})
	t.Run("nothing contains the value", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, ask("GPA", "Less than 2.0", "2.0-3.0")), ReasonNoEntry, false)
	})
	t.Run("fewer than two parsed ranges is not a bracket list", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, ask("GPA", "3.0-4.0", "Prefer not to say")), ReasonNoEntry, false)
	})
	t.Run("no recorded grade average", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", End: "2026-05"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, ask("GPA", brackets...)), ReasonNoEntry, false)
	})
	t.Run("a scale picker is not a bracket list (Citadel's 4.0, 5.0, 0-100)", func(t *testing.T) {
		for _, q := range []string{"GPA Scale", "GPA out of", "Maximum GPA", "What scale is your GPA on?"} {
			pfAbstain(t, pfOne(t, m, f, pfToday, ask(q, "4.0", "5.0", "0-100")), ReasonNoEntry, false)
		}
		pfWant(t, pfOne(t, m, f, pfToday, ask("What is your cumulative GPA (on a 4.0 scale)?", brackets...)), "3.5-4.0")
	})
}

func TestAnswerBank_Gpa_ScopedText(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	cases := []struct{ name, text, want string }{
		{"undergraduate wording", "Undergraduate GPA", "3.7"},
		{"bachelor wording", "Bachelor's GPA", "3.7"},
		{"all levels listed is the undergraduate lane", "GPA (Undergraduate/Graduate/Doctorate)", "3.7"},
		{"graduate wording", "Graduate GPA", "3.9"},
		{"master wording", "Master's grade point average", "3.9"},
		{"a bare GPA is the most recent entry", "GPA", "3.9"},
		{"cumulative", "Cumulative GPA", "3.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, m, f, pfToday, textQ("c", c.text))
			pfWant(t, r, c.want)
			if r.Confidence != 0.90 {
				t.Fatalf("result = %+v", r)
			}
		})
	}
	t.Run("the first number of the recorded value", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", End: "2026-05", GradeAverage: "3.85 / 4.0"}}}
		pfWant(t, pfOne(t, m, g, pfToday, textQ("c", "GPA")), "3.85")
	})
	t.Run("a scoped question without a matching record", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Degree: "Master of Science", End: "2026-05", GradeAverage: "3.9"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, textQ("c", "Undergraduate GPA")), ReasonNoEntry, false)
	})
	t.Run("doctoral wording never reads the master entry", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, textQ("c", "GPA (Doctorate)")), ReasonNoEntry, false)
		g := eduOnly()
		g.Education = append(g.Education, EducationFact{School: "C", Degree: "PhD in Physics", End: "2020-05", GradeAverage: "4.0"})
		pfWant(t, pfOne(t, m, g, pfToday, textQ("c", "Doctoral GPA")), "4.0")
	})
	t.Run("both levels named is unscoped and answers nothing", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, textQ("c", "Undergraduate and graduate GPA")), ReasonNoEntry, false)
	})
	t.Run("only a text control", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, pfSelect("c", "GPA", "High", "Low")), ReasonNoEntry, false)
	})
	t.Run("a box for the scale or an explanation is not the grade average", func(t *testing.T) {
		for _, text := range []string{"GPA Scale", "If your GPA is below 3.0, please explain", "Why is your GPA not listed?"} {
			if r := pfOne(t, m, f, pfToday, textQ("c", text)); r.Answer == "3.9" {
				t.Errorf("%q: %+v", text, r)
			}
		}
	})
}

// Aven (Ashby, queue148): "Which university did you attend for undergrad?"
// over 28 schools and "Other", holding the master's school but not the
// bachelor's. The bachelor's school is read by the school-list rule; the
// master's school is never taken for it.
func TestAnswerBank_UndergradSchool_ListTakesTheBachelorsSchool(t *testing.T) {
	m := pfMatcher()
	f := Facts{Education: []EducationFact{
		{School: "Carver University", Degree: "Master of Science", Field: "Software Engineering", Start: "2024-08", End: "2026-05"},
		{School: "University of Westbridge", Degree: "Bachelor of Science", Field: "Applied Mathematics", Start: "2020-09", End: "2024-06"},
	}}
	aven := []string{"California Institute of Technology (Caltech)", "Carnegie Mellon University (CMU)", "Cornell University", "Carver University", "Harvard University", "Stanford University", "Tsinghua University", "University of Waterloo"}
	const ask = "Which university did you attend for undergrad?"
	t.Run("a list without the school takes Other", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("c", ask, append(slices.Clone(aven), "Other")...)), "Other")
	})
	t.Run("a list that holds the school takes it", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("c", "Undergraduate institution", append(slices.Clone(aven), "University of Westbridge", "Other")...)), "University of Westbridge")
	})
	t.Run("another question about the degree is not the school", func(t *testing.T) {
		if r := pfOne(t, m, f, pfToday, pfSelect("c", "What was your undergraduate major at your university?", append(slices.Clone(aven), "Other")...)); r.ChosenLabel == "Other" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("no bachelor's row", func(t *testing.T) {
		grad := Facts{Education: f.Education[:1]}
		if r := pfOne(t, m, grad, pfToday, pfSelect("c", ask, append(slices.Clone(aven), "Other")...)); r.ChosenLabel != "" {
			t.Fatalf("result = %+v", r)
		}
	})
}
