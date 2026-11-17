package match

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// eduOnly is the persona without work history, so a date control cannot be
// claimed by the employment lane.
func eduOnly() Facts {
	f := alexFacts()
	f.Employment = nil
	return f
}

func TestAnswerBank_Education_MostRecentAndEarliest(t *testing.T) {
	t.Run("PR-017 an in-progress degree is the most recent", func(t *testing.T) {
		f := Facts{Education: []EducationFact{
			{School: "Finished U", End: "2024-05"},
			{School: "Current U"},
		}}
		got, ok := mostRecentEducation(f)
		if !ok || got.School != "Current U" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("PR-017 the latest end wins whatever the profile order", func(t *testing.T) {
		got, _ := mostRecentEducation(alexFacts())
		if got.School != "Example State University" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("PR-017 a tie keeps the first listed", func(t *testing.T) {
		f := Facts{Education: []EducationFact{{School: "A", End: "2020-05"}, {School: "B", End: "2020-05"}}}
		if got, _ := mostRecentEducation(f); got.School != "A" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("PR-017 no entries", func(t *testing.T) {
		if _, ok := mostRecentEducation(Facts{}); ok {
			t.Fatal("found an entry")
		}
	})
}

func TestAnswerBank_EducationDate_Exclusions(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	for _, c := range []struct{ name, text string }{
		{"age eligibility", "Are you at least 18 years of age? You must be to apply."},
		{"how many", "How many years of relevant coursework to date"},
		{"typo in experience", "Do you have relevant experience TO you have of 3 years"},
		{"directionless month", "Month"},
		{"both directions", "Start date to end date year"},
		{"employment context", "Year you started this job from"},
		{"duration in days", "Can you start within 30 days?"},
		{"no component", "Graduation"},
		// Rain (Ashby, live): a four-year university is the program's length.
		{"a program length", "If you did graduate from a 4 year university, which one did you earn your degree?"},
		{"a program length, hyphenated", "Which 4-year degree did you complete?"},
		// Avride (Greenhouse, owner workspace): a year of study is a standing,
		// never a date; it took the degree's end year.
		{"a year of study", "Are you currently enrolled as a student at a college or university? If yes, please provide the name of your school and your current year/level of study (e.g., undergraduate freshman, sophomore, junior, senior, or graduate/master’s student)."},
		{"a year in school", "What year in school are you?"},
		// A short sentence is no bare date part: these took the school's dates.
		{"a start in the role with its format", "When can you start? (month and year)"},
		{"a move", "What year did you move to the US?"},
		{"a bar admission", "Year admitted to the bar"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pfAbstain(t, pfOne(t, m, f, pfToday, textQ("c", c.text)), ReasonNoEntry, false)
		})
	}
	t.Run("a genuine To month still fills", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "To month")), "May")
	})
	t.Run("a day is only a day inside an explicit date control", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "End date day")), "1")
	})
	t.Run("a checkbox group is never a date part", func(t *testing.T) {
		q := Question{Selector: "c", Text: "End year", Kind: KindCheckbox, Options: opts("2024", "2026")}
		pfAbstain(t, pfOne(t, m, f, pfToday, q), ReasonNoEntry, false)
	})
}

func TestAnswerBank_EducationDate_HighSchoolYear(t *testing.T) {
	m := pfMatcher()
	q := textQ("hs", "What year did you graduate high school?")
	t.Run("PR-021 no row names high school", func(t *testing.T) {
		f := Facts{Education: []EducationFact{{School: "A", Start: "2018-09", End: "2022-05"}, {School: "B", Start: "2022-09", End: "2024-05"}}}
		pfAbstain(t, pfOne(t, m, f, pfToday, q), ReasonPlaceholderEmpty, false)
	})
	t.Run("a row that names high school answers its year", func(t *testing.T) {
		f := Facts{Education: []EducationFact{
			{School: "Example University", Degree: "BS", Start: "2018-09", End: "2022-05"},
			{School: "Example High School", Degree: "Diploma", Start: "2014-09", End: "2018-06"},
		}}
		pfWant(t, pfOne(t, m, f, pfToday, q), "2018")
	})
	t.Run("a high-school month is never answered", func(t *testing.T) {
		f := Facts{Education: []EducationFact{{School: "Example High School", Start: "2014-09", End: "2018-06"}}}
		pfAbstain(t, pfOne(t, m, f, pfToday, textQ("hs", "In which month did you graduate high school?")), ReasonPlaceholderEmpty, false)
		// A date control holds a month too: the bare year went into it.
		pfAbstain(t, pfOne(t, m, f, pfToday, Question{Selector: "hs", Text: "High school graduation date", Kind: KindText, InputType: InputDate}), ReasonPlaceholderEmpty, false)
	})
}

func TestAnswerBank_EducationDate_MonthYearAndDay(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	t.Run("month and year on one control", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "Expected graduation month and year")), "May 2026")
	})
	t.Run("a profile date answer is a review fill at 0.85", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, textQ("c", "Expected graduation month and year"))
		if r.Outcome != OutcomeFilledReview || r.Confidence != 0.85 {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a start control reads the start", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "Start month / year")), "September 2024")
	})
	t.Run("a day control answers the day unpadded", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "End date day")), "1")
	})
	// Harvey's Ashby picker: a date control that asks only for the date.
	grad := Question{Selector: "c", Text: "Graduation Date", Kind: KindText, InputType: InputDate}
	t.Run("a graduation date control takes the first of the month", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, grad), "2026-05-01")
	})
	t.Run("a start date control is not an education date", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, Question{Selector: "c", Text: "Start Date", Kind: KindText, InputType: InputDate})
		if r.Answer == "September 2024" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("an anticipated start date is the start in the role, not at school", func(t *testing.T) {
		// Palantir: "August 2024", the master's start, went into it.
		r := pfOne(t, m, f, pfToday, textQ("c", "What is your anticipated start date? (Month/Year)"))
		if r.Answer == "September 2024" || strings.Contains(strings.Join(r.Trace, "\n"), "education-date") {
			t.Fatalf("result = %+v", r)
		}
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "Expected start month and year of your degree")), "September 2024")
	})
	t.Run("the user's saved expected graduation date stands", func(t *testing.T) {
		own := ent("expected-graduation-date", "fixed", withAnswer("2026-12-15"), withTypes(InputDate, InputText))
		r := pfOne(t, pfMatcher(own), f, pfToday, grad)
		if r.EntryID != "expected-graduation-date" {
			t.Fatalf("result = %+v", r)
		}
	})
	// Adobe (Workday, live): a text area that states the layout it wants.
	t.Run("a graduation date in a stated layout is written in it", func(t *testing.T) {
		for label, want := range map[string]string{
			"What is your current estimated graduation date (please enter in this format MM/YYYY)?": "05/2026",
			"Graduation date (YYYY-MM)": "2026-05",
			"Graduation date, MM-YY":    "05-26",
		} {
			q := Question{Selector: "c", Text: label, Kind: KindText, InputType: InputLongText}
			pfWant(t, pfOne(t, m, f, pfToday, q), want)
		}
	})
}

func TestAnswerBank_EducationDate_NoOptionForYearAbstains(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	text := "What is your expected graduation month and year?"
	t.Run("PR-023 the list offers only other years", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, pfSelect("c", text, "December 2027", "May 2028"))
		pfAbstain(t, r, ReasonInputMismatch, false)
		if !traceHas(r, "no option offers the year 2026") {
			t.Fatalf("trace = %v", r.Trace)
		}
	})
	t.Run("the option with the user's year is chosen", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, pfSelect("c", text, "December 2026", "May 2026", "May 2027"))
		pfWant(t, r, "May 2026")
		if r.Outcome != OutcomeFilledReview || r.Confidence != 0.85 {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a day list skips the year filter", func(t *testing.T) {
		days := make([]string, 31)
		for i := range days {
			days[i] = fmt.Sprint(i + 1)
		}
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("c", "End date day", days...)), "1")
	})
}

func TestAnswerBank_EducationDate_SingleComponent(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	long := []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	short := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	cases := []struct {
		name, text string
		options    []string
		want       string
	}{
		{"graduation month text", "Graduation month", nil, "May"},
		{"graduation year text", "Graduation year", nil, "2026"},
		{"expected graduation month", "Expected graduation month", nil, "May"},
		{"start month text", "Start date month*", nil, "September"},
		{"start year text", "Start year", nil, "2024"},
		{"end month select", "To month", long, "May"},
		{"start month abbreviations", "Start date month", short, "Sep"},
		{"end year select", "End year", []string{"2024", "2025", "2026"}, "2026"},
		// Chase (Oracle): "graduated from" names the end, never a start.
		{"graduated-from month sentence", "Please select your expected graduation month of your current studies, or if you have already graduated select the month you graduated from your most recent studies.", long, "May"},
		{"graduated-from year sentence", "Please select your expected graduation year of your current studies, or if you have already graduated select the year you graduated from your most recent studies.", []string{"2024", "2025", "2026", "2027"}, "2026"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := textQ("c", c.text)
			if c.options != nil {
				q = pfSelect("c", c.text, c.options...)
			}
			r := pfOne(t, m, f, pfToday, q)
			pfWant(t, r, c.want)
			if r.Source != SourceDerived || r.Confidence != 0.85 || r.Outcome != OutcomeFilledReview {
				t.Fatalf("result = %+v", r)
			}
		})
	}
	t.Run("a year list without the year falls through", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, pfSelect("c", "End year", "2020", "2021")), ReasonNoEntry, false)
	})
	t.Run("an in-progress degree with no end gives no end date", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Start: "2025-09"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, textQ("c", "Graduation year")), ReasonNoEntry, false)
	})
}

func TestAnswerBank_MonthNameNormalization(t *testing.T) {
	cases := map[string]string{"5": "May", "05": "May", "May": "May", "Sep": "September", "september": "September", "Dec.": "December"}
	for in, want := range cases {
		if got := MonthName(monthFromWord(in)); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "13", "0", "Ma", "soon", "2026"} {
		if got := monthFromWord(in); got != 0 {
			t.Errorf("%q -> %d, want no month", in, got)
		}
	}
}

func TestAnswerBank_Graduation_ParseSeasonBracket(t *testing.T) {
	k := monthKey
	cases := []struct {
		label    string
		ok       bool
		from, to int
	}{
		{"Winter 2025 - Spring 2026", true, k(2025, 12), k(2026, 5)},
		{"Summer 2026", true, k(2026, 6), k(2026, 8)},
		{"Fall 2026", true, k(2026, 9), k(2026, 11)},
		{"Autumn 2026", true, k(2026, 9), k(2026, 11)},
		{"December 2026 - November 2027", true, k(2026, 12), k(2027, 11)},
		{"Summer 2027 or later", true, k(2027, 6), math.MaxInt},
		// "After" and "before" leave the named month out; "or later" keeps it.
		{"I graduate after December 2027", true, k(2028, 1), math.MaxInt},
		{"Before May 2026", true, math.MinInt, k(2026, 4)},
		{"Later than Summer 2027", true, k(2027, 9), math.MaxInt},
		{"After May 2026 and before December 2026", false, 0, 0},
		// A term that states its own months is those months.
		{"Fall 2026 (September-December)", true, k(2026, 9), k(2026, 12)},
		{"Spring 2027 (January-May)", true, k(2027, 1), k(2027, 5)},
		{"Dec. 2026", true, k(2026, 12), k(2026, 12)},
		{"I have already graduated", false, 0, 0},
		{"Other", false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			r, ok := parseSeasonBracket(c.label)
			if ok != c.ok || (ok && (r.from != c.from || r.to != c.to)) {
				t.Fatalf("got %+v %t", r, ok)
			}
		})
	}
}

// Two Sigma (Avature, live): a graduate asked how likely the graduation date
// is to change; the model rungs declined it.
func TestAnswerBank_GraduationChange(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	q := func(text string, labels ...string) Question { return pfSelect("c", text, labels...) }
	r := pfOne(t, m, f, pfDate(2026, 11, 1), q("Likelihood of Graduation Change", "Likely", "Unlikely"))
	pfWant(t, r, "Unlikely")
	if r.AnswerKey != "graduation_change" {
		t.Fatalf("result = %+v", r)
	}
	pfWant(t, pfOne(t, m, f, pfDate(2026, 11, 1), q("How likely is your graduation date to change?", "Very likely", "Likely", "Unlikely", "Very unlikely")), "Very unlikely")
	// Before the graduation, and for another likelihood, it is not this lane's.
	for _, c := range []struct {
		at   Date
		text string
	}{{pfDate(2026, 1, 15), "Likelihood of Graduation Change"}, {pfDate(2026, 11, 1), "How likely are you to change teams within a year?"}} {
		if r := pfOne(t, m, f, c.at, q(c.text, "Likely", "Unlikely")); r.AnswerKey == "graduation_change" {
			t.Errorf("%q on %v: %+v", c.text, c.at, r)
		}
	}
}

func TestAnswerBank_GraduationTimeFrame(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	q := func(text string, labels ...string) Question { return pfSelect("c", text, labels...) }
	t.Run("PR-034 a graduation in the past picks already graduated", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 9, 30), q("When is your anticipated graduation date?", "I have already graduated", "I expect to graduate in 2027"))
		pfWant(t, r, "I have already graduated")
		if r.Confidence != 0.95 || r.Outcome != OutcomeFilled || r.AnswerKey != "graduation_time_frame" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("the graduation month itself counts as done", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 5, 3), q("Graduation time frame", "I have already graduated", "Winter 2026 - Spring 2027"))
		pfWant(t, r, "I have already graduated")
	})
	t.Run("never already graduated before the graduation", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 1, 15), q("Graduation time frame", "I have already graduated", "Winter 2025 - Spring 2026", "Summer 2026 or later"))
		pfWant(t, r, "Winter 2025 - Spring 2026")
	})
	t.Run("an open-ended bracket", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", End: "2028-05"}}}
		r := pfOne(t, m, g, pfDate(2026, 1, 15), q("Expected graduation term", "Before December 2027", "I graduate after December 2027"))
		pfWant(t, r, "I graduate after December 2027")
	})
	t.Run("nothing covers the date", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 1, 15), q("Graduation time frame", "Fall 2027", "Fall 2028"))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
	// Decagon (Ashby, live): a graduate asked only about terms still ahead.
	t.Run("graduated, every term ahead: the earliest", func(t *testing.T) {
		terms := q("What is your graduation date?", "Fall 2026 (September-December)", "Spring 2027 (January-May)", "Summer 2027 (June or later)", "Later than Summer 2027")
		pfWant(t, pfOne(t, m, f, pfDate(2026, 11, 1), terms), "Fall 2026 (September-December)")
		// A December graduation is in the fall term that says it runs to December.
		dec := Facts{Education: []EducationFact{{School: "A", End: "2026-12"}}}
		pfWant(t, pfOne(t, m, dec, pfDate(2026, 1, 15), terms), "Fall 2026 (September-December)")
		pfAbstain(t, pfOne(t, m, dec, pfDate(2026, 1, 15), q("Expected graduation term", "Before December 2026", "After December 2026")), ReasonInputMismatch, false)
		// Before the graduation the same terms are no answer, and a term that
		// reaches back past it leaves the question to the user.
		pfAbstain(t, pfOne(t, m, f, pfDate(2026, 1, 15), q("Graduation time frame", "Fall 2027", "Fall 2028")), ReasonInputMismatch, false)
		pfAbstain(t, pfOne(t, m, f, pfDate(2026, 11, 1), q("Graduation time frame", "Fall 2025", "Fall 2026", "Spring 2027")), ReasonInputMismatch, false)
	})
	// SeatGeek (Greenhouse, live): years alone, every one ahead, and "Other".
	t.Run("graduated, every year ahead: Other when offered, else the earliest", func(t *testing.T) {
		const yq = "What is your expected graduation year?"
		pfWant(t, pfOne(t, m, f, pfDate(2026, 11, 7), q(yq, "2027", "2028", "2029", "2030", "Other")), "Other")
		pfWant(t, pfOne(t, m, f, pfDate(2026, 11, 7), q("What is your graduation date?", "Fall 2026", "Spring 2027", "Other (please specify)")), "Other (please specify)")
		pfWant(t, pfOne(t, m, f, pfDate(2026, 11, 7), q(yq, "2028", "2027", "2029")), "2027")
		// A year that holds the graduation is the answer, Other or not.
		pfWant(t, pfOne(t, m, f, pfDate(2026, 11, 7), q(yq, "2025", "2026", "2027", "Other")), "2026")
		// Before the graduation, years still ahead are no answer.
		pfAbstain(t, pfOne(t, m, f, pfDate(2026, 1, 15), q(yq, "2027", "2028", "Other")), ReasonInputMismatch, false)
	})
	t.Run("no education dates", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A"}}}
		r := pfOne(t, m, g, pfToday, q("Graduation time frame", "Fall 2027", "Fall 2028"))
		pfAbstain(t, r, ReasonPlaceholderEmpty, false)
	})
	t.Run("an entry without an end month may be in progress: never already graduated", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Degree: "BS", End: "2024-05"}, {School: "B", Degree: "MS"}}}
		r := pfOne(t, m, g, pfDate(2026, 9, 30), q("When is your anticipated graduation date?", "I have already graduated", "I expect to graduate in 2027"))
		pfAbstain(t, r, ReasonPlaceholderEmpty, false)
		pfAbstain(t, pfOne(t, m, g, pfDate(2026, 11, 7), q("What is your expected graduation year?", "2027", "2028", "Other")), ReasonPlaceholderEmpty, false)
	})
	// General Mills (Workday, live): the same question in a text box.
	t.Run("asked in a text box, the latest end's month and year", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, textQ("c", "For the degree program for which you are currently enrolled, when do you anticipate you will graduate?")), "May 2026")
		for _, text := range []string{"When did you graduate from high school?", "When will you start the graduate program?", "When do you anticipate graduate school admission decisions?"} {
			if r := pfOne(t, m, f, pfToday, textQ("c", text)); r.Answer == "May 2026" {
				t.Errorf("%q answered with the degree's end: %+v", text, r)
			}
		}
	})
	t.Run("a picker without two options is not claimed", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, textQ("c", "Graduation time frame")), ReasonNoEntry, false)
	})
	t.Run("the latest end decides", func(t *testing.T) {
		r := pfOne(t, m, alexFacts(), pfDate(2025, 12, 1), q("Graduation time frame", "I have already graduated", "Spring 2026"))
		pfWant(t, r, "Spring 2026")
	})
	// P&G (Workday, live).
	t.Run("how far from graduating, after every end, is not studying", func(t *testing.T) {
		far := q("If currently studying, how far are you from graduating with the degree selected above?", "In final year of graduating", "Within 2 years of graduating",
			"I will graduate at the end of this semester", "Entering a Masters Degree program", "I am not currently studying")
		pfWant(t, pfOne(t, m, f, pfDate(2026, 10, 1), far), "I am not currently studying")
		g := Facts{Education: append([]EducationFact{{School: "B", Degree: "Doctor of Philosophy", Start: "2026-09"}}, f.Education...)}
		r := pfOne(t, m, g, pfDate(2026, 10, 1), far)
		pfAbstain(t, r, ReasonPlaceholderEmpty, false)
		if !traceHas(r, "has no end month") {
			t.Fatalf("a degree in progress is left for its missing end: %v", r.Trace)
		}
	})
	t.Run("a span of months names its year once", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 1, 15), q("What is your expected graduation date:", "January - March 2026", "April - June 2026", "July - September 2026", "I am currently not attending school"))
		pfWant(t, r, "April - June 2026")
	})
	t.Run("a span across the new year starts the year before", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", End: "2026-12"}}}
		r := pfOne(t, m, g, pfDate(2026, 1, 15), q("Expected graduation term", "November - February 2027", "March - May 2027"))
		pfWant(t, r, "November - February 2027")
	})
}

// Agave (Ashby, live): a graduation window asked as Yes or No is answered
// from the latest education end, inside or outside; Scale AI asks it of two
// terms.
func TestAnswerBank_GraduationWindow(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	for _, c := range []struct{ q, want string }{
		{"Is your graduation date between November 2026 and June 2027?", "No"},
		{"Is your graduation date between January 2026 and June 2026?", "Yes"},
		{"Will you graduate between Spring 2026 and Fall 2026?", "Yes"},
		// Scale AI (Greenhouse, queue186): two terms, the end in neither.
		{"I confirm that my graduation date will be either Fall 2026 or Spring 2027", "No"},
		{"I confirm that my graduation date will be either Spring 2026 or Fall 2026", "Yes"},
	} {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 10, 31), yesNoQ("c", c.q)), c.want)
	}
	open := Facts{Education: append([]EducationFact{{School: "B", Degree: "Doctor of Philosophy", Start: "2026-09"}}, f.Education...)}
	for _, r := range []Result{
		pfOne(t, m, open, pfDate(2026, 10, 31), yesNoQ("c", "Is your graduation date between November 2026 and June 2027?")),
		pfOne(t, m, f, pfDate(2026, 10, 31), yesNoQ("c", "Is your high school graduation date between May 2016 and June 2018?")),
	} {
		if r.Outcome != OutcomeAbstained {
			t.Errorf("answered: %+v", r)
		}
	}
}

func TestAnswerBank_CurrentlyPursuingDegree(t *testing.T) {
	m := pfMatcher()
	f := eduOnly()
	yn := func(text string) Question { return yesNoQ("c", text) }
	t.Run("PR-029 the master's ended in May", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 10, 1), yn("Are you currently pursuing a Master's degree?"))
		pfWant(t, r, "No")
		if r.AnswerKey != "currently_pursuing_degree" || r.Confidence != 0.95 {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("before the end it is Yes", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 1, 10), yn("Are you currently pursuing a Master's degree?")), "Yes")
	})
	t.Run("the end month counts as ended", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 5, 20), yn("Are you currently pursuing a Master's degree?")), "No")
	})
	t.Run("a bare degree matches any level", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 1, 10), yn("Are you currently pursuing a degree?")), "Yes")
	})
	t.Run("a level the user has no entry for is No", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 1, 10), yn("Are you currently pursuing a PhD?")), "No")
	})
	t.Run("every level the question names counts", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Degree: "Bachelor of Science", Start: "2023-09", End: "2027-05"}}}
		pfWant(t, pfOne(t, m, g, pfToday, yn("Are you currently pursuing a Bachelor's, Master's or PhD degree?")), "Yes")
		pfWant(t, pfOne(t, m, g, pfToday, yn("Are you currently pursuing a Master's or PhD degree?")), "No")
		pfWant(t, pfOne(t, m, g, pfToday, yn("Are you currently pursuing a graduate degree?")), "No")
	})
	t.Run("a field, a school or a second condition after the degree is more than the dates answer", func(t *testing.T) {
		for _, text := range []string{
			"Are you currently pursuing a degree in Mechanical Engineering?",
			"Are you currently pursuing a degree and graduating between December 2026 and June 2027?",
			"Are you currently pursuing a Master's degree at an accredited university?",
		} {
			if r := pfOne(t, m, f, pfDate(2026, 1, 10), yn(text)); r.AnswerKey == "currently_pursuing_degree" {
				t.Errorf("%q: %+v", text, r)
			}
		}
		pfWant(t, pfOne(t, m, f, pfDate(2026, 1, 10), yn("Are you currently pursuing a Master’s or PhD degree program?")), "Yes")
	})
	t.Run("an entry with no end cannot be dated", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Degree: "Master of Science", Start: "2025-09"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, yn("Are you currently pursuing a Master's degree?")), ReasonPlaceholderEmpty, false)
	})
	t.Run("a control without Yes and No is left alone", func(t *testing.T) {
		q := pfSelect("c", "Are you currently pursuing a Master's degree?", "Full time", "Part time")
		pfAbstain(t, pfOne(t, m, f, pfToday, q), ReasonNoEntry, false)
	})
	t.Run("currently enrolled stays with the user's own row", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, yn("Are you currently enrolled in a degree program?")), ReasonNoEntry, false)
	})
	amex := []string{"Master (MS/MA)", "Master/MBA Dual Degree", "MBA - Executive Program", "MBA - Part time", "MBA - Full time", "MFA", "Bachelor (BA/BS)", "Currently not pursuing a degree", "Juris Doctor", "Other - Not listed", "PhD/Doctorate"}
	pick := func(labels ...string) Question {
		return pfSelect("c", "Select the degree program you are currently pursuing:", labels...)
	}
	t.Run("a picker of levels takes the level in progress", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 1, 10), pick("Bachelor (BA/BS)", "Master (MS/MA)", "PhD/Doctorate", "Currently not pursuing a degree")), "Master (MS/MA)")
	})
	t.Run("a picker of levels after every end is not pursuing", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 10, 1), pick(amex...)), "Currently not pursuing a degree")
	})
	t.Run("a level several options share is left for review", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfDate(2026, 1, 10), pick(amex...)), ReasonAmbiguous, false)
	})
	// Decagon (Ashby, live): degree levels and "Other", nothing for not
	// studying. Owner decision: a graduate takes the degree just completed;
	// in progress, the degree being pursued.
	decagon := func() Question {
		return pfSelect("c", "What degree are you pursuing?", "Associate's Degree", "Bachelor's Degree", "Master's Degree", "Doctor of Philosophy (Ph.D.)", "Other")
	}
	t.Run("a graduate asked which degree is pursued, with no option for not studying, takes the degree just completed", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 11, 1), decagon()), "Master's Degree")
		pfWant(t, pfOne(t, m, f, pfDate(2026, 1, 10), decagon()), "Master's Degree")
	})
	twoSigma := func() Question {
		return pfSelect("c", "Please select your current level of education:", "I am a currently a first year undergrad student.", "I am currently a sophomore.",
			"I am currently a junior.", "I am currently a senior.", "I am currently in a graduate program (Master's or PhD)", "Other")
	}
	t.Run("after every end, the catch-all is the one option that states no enrollment", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 10, 1), twoSigma()), "Other")
	})
	// Zip (Ashby, live): the one option that states no enrollment states a
	// degree completed.
	zip := func() Question {
		return pfSelect("c", "What is your current education status?", "I have completed my undergraduate degree", "I am currently enrolled in an undergraduate program",
			"I am currently enrolled in a higher education program but can work a full time job", "I am currently enrolled in a higher education program and cannot work a full time job")
	}
	t.Run("after every end, the one option that states no enrollment may state a degree completed", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 10, 1), zip()), "I have completed my undergraduate degree")
		// A level the profile never completed is no such option, and a degree in progress has no option here.
		g := Facts{Education: []EducationFact{{School: "A", Degree: "Associate of Arts", Start: "2018-09", End: "2020-05"}}}
		pfAbstain(t, pfOne(t, m, g, pfDate(2026, 10, 1), zip()), ReasonAmbiguous, false)
		pfAbstain(t, pfOne(t, m, f, pfDate(2026, 1, 10), zip()), ReasonAmbiguous, false)
	})
	t.Run("a class year in progress is left for review", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "A", Degree: "Bachelor of Science", Start: "2024-09", End: "2028-05"}}}
		q := pfSelect("c", "Which degree program are you currently enrolled in?", "Undergraduate - senior year", "Graduate student (Master's)", "Other")
		pfAbstain(t, pfOne(t, m, g, pfDate(2026, 10, 1), q), ReasonAmbiguous, false)
	})
	t.Run("a current level over degree levels is the highest level attained, not this lane", func(t *testing.T) {
		r := pfOne(t, m, f, pfDate(2026, 10, 1), pfSelect("c", "What is your current level of education?", "High School", "Bachelor's Degree", "Master's Degree", "Other"))
		if strings.Contains(strings.Join(r.Trace, " "), "pursuing-level") {
			t.Fatalf("claimed by the pursuing lane: %+v", r)
		}
	})
	// P&G (Workday, live): the question asks for the highest degree when none is in progress.
	pg := func(text string) Question {
		return pfSelect("c", text, "Doctoral degree or other equivalent advanced degree", "Masters degree", "Bachelors degree", "Associates degree",
			"I do not have and am not working toward a degree")
	}
	const towards = "What degree are you working towards? (Or if not currently working towards a degree, what is your highest degree obtained?)"
	t.Run("after every end, the highest completed degree when the question asks for it then", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfDate(2026, 10, 1), pg(towards)), "Masters degree")
		// In progress, the degree being worked towards, not the bachelor's completed.
		pfWant(t, pfOne(t, m, f, pfDate(2026, 1, 10), pg(towards)), "Masters degree")
		if r := pfOne(t, m, f, pfDate(2026, 10, 1), pg("What degree are you currently working towards?")); r.Answer == "Masters degree" {
			t.Fatalf("no highest degree asked: %+v", r)
		}
	})
	t.Run("the user's own pick stands", func(t *testing.T) {
		own := pickMatcher(ent("current-level-education-pursuing", "pick-option", withPreferred("MBA - Part time"), withExamples("What level of education are you pursuing?"), withDescription("Level of the degree the applicant is currently pursuing"), withTypes(InputSingleSelect)))
		r := pfOne(t, own, f, pfDate(2026, 10, 1), pick(amex...))
		pfWant(t, r, "MBA - Part time")
	})
	t.Run("level words", func(t *testing.T) {
		for in, want := range map[string]string{"Master of Science": "Master", "Bachelor of Arts": "Bachelor", "PhD in Physics": "Doctorate",
			"Associate of Applied Science": "Associate", "High School Diploma": "High School", "Welding Certificate": "", "MBA": "Master", "B.S. Computer Science": "Bachelor"} {
			if got := degreeLevel(in); got != want {
				t.Errorf("%q -> %q, want %q", in, got, want)
			}
		}
		if strings.TrimSpace(degreeLevel("Master's Degree")) != "Master" {
			t.Error("master's")
		}
	})
	t.Run("a two-letter abbreviation is a degree only in capitals or dots, and only in the degree field", func(t *testing.T) {
		for in, want := range map[string]string{"MD": "Doctorate", "M.D.": "Doctorate", "B.E. Mechanical": "Bachelor", "md": "", "Me": "", "be": ""} {
			if got := degreeNameLevel(in); got != want {
				t.Errorf("degree %q -> %q, want %q", in, got, want)
			}
		}
		if got := degreeLevel("Are you located in Baltimore, MD, or willing to be?"); got != "" {
			t.Errorf("a state code read as %q", got)
		}
	})
}

// A co-op question that names no school is not about another school's
// program: the words before "co-op" were read as a school no education names.
func TestAnswerBank_SchoolCoop_NeedsASchoolName(t *testing.T) {
	m := pfMatcher()
	for _, text := range []string{"Are you a co-op student?", "Are you seeking a co-op?", "Are you currently enrolled in a co-op?", "Are You Seeking A Co-Op?"} {
		if r := pfOne(t, m, eduOnly(), pfToday, yesNoQ("c", text)); r.AnswerKey == "school_coop" {
			t.Errorf("%q: %+v", text, r)
		}
	}
	pfWant(t, pfOne(t, m, eduOnly(), pfToday, yesNoQ("c", "Are you a Northeastern University co-op student?")), "No")
}

// P&G (Workday, live): "graduated within the past N months, or an active
// student" is read from the education history.
func TestAnswerBank_RecentGraduateOrStudent_FromTheEducationHistory(t *testing.T) {
	m := pfMatcher()
	q := yesNoQ("g", "Have you either graduated within the past 6 months, or are you an active student?")
	for _, c := range []struct {
		name  string
		today Date
		edit  func(*Facts)
		want  string
	}{
		{"graduated five months ago", pfDate(2026, 10, 31), nil, "Yes"},
		{"graduated seven months ago", pfDate(2026, 12, 31), nil, "No"},
		{"still enrolled", pfDate(2026, 3, 1), nil, "Yes"},
		{"a degree in progress beside one with no end", pfDate(2026, 3, 1), func(f *Facts) { f.Education[1].End = "" }, "Yes"},
	} {
		f := alexFacts()
		if c.edit != nil {
			c.edit(&f)
		}
		if r := pfOne(t, m, f, c.today, q); r.Answer != c.want {
			t.Errorf("%s: %+v, want %s", c.name, r, c.want)
		}
	}
	// An entry without an end month cannot be dated, as in every education
	// lane: it proves neither a student nor a graduate of long ago.
	for _, end := range []string{"", "soon"} {
		f := alexFacts()
		f.Education[1].End = end
		if r := pfOne(t, m, f, pfDate(2027, 12, 1), q); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
			t.Errorf("an undated degree (%q): %+v, want an abstain", end, r)
		}
	}
}

// Adobe (Workday, live): "Which one of the following best describes your
// current status?" over enrolled / completed within 12 months (24 for a PhD) /
// an Adobe internship / none is read from the education dates.
func TestAnswerBank_StudentStatus_FromTheEducationDates(t *testing.T) {
	m := pfMatcher()
	enrolled := "I'm currently enrolled in an undergraduate or graduate degree program"
	recent := "I've completed an undergraduate or graduate degree program within the last 12 months (24 months for PhD graduates)"
	q := selectQ("Which one of the following best describes your current status?", enrolled, recent,
		"I am currently or have previously been an Adobe intern through the Adobe Digital Academy program in the last 12 months", "None of the above applies to me")
	for _, c := range []struct {
		name  string
		today Date
		edit  func(*Facts)
		want  string
	}{
		{"graduated five months ago", pfDate(2026, 10, 31), nil, recent},
		{"still enrolled", pfDate(2026, 3, 1), nil, enrolled},
		{"a PhD that ended twenty months ago", pfDate(2028, 1, 15), func(f *Facts) { f.Education[0].Degree = "PhD" }, recent},
	} {
		f := alexFacts()
		if c.edit != nil {
			c.edit(&f)
		}
		if r := pfOne(t, m, f, c.today, q); r.Answer != c.want {
			t.Errorf("%s: %+v, want %q", c.name, r, c.want)
		}
	}
	if r := pfOne(t, m, alexFacts(), pfDate(2028, 1, 15), q); r.Outcome == OutcomeFilled {
		t.Errorf("a master's that ended twenty months ago: %+v, want it left (the internship option is not the profile's)", r)
	}
	// One enrolled option in a list about something else is no study status.
	visa := selectQ("Which best describes your work authorization?", "U.S. citizen or permanent resident", "Currently enrolled student on F-1 (CPT/OPT)", "H-1B holder", "Other")
	if r := pfOne(t, m, alexFacts(), pfDate(2026, 3, 1), visa); r.AnswerKey == "student_status" {
		t.Errorf("a work-authorization list: %+v", r)
	}
}
