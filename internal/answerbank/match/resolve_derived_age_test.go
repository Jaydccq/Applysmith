package match

import "testing"

// blankEntry is a derived or placeholder entry with an empty body.
func blankEntry(id string, kind Kind) Entry {
	return Entry{ID: id, Kind: kind, Meta: Meta{Placeholder: true}}
}

func TestAnswerBank_Age_MissingFutureImplausibleDobAbstains(t *testing.T) {
	m := pfMatcher(blankEntry(IDCurrentAgeBracket, KindPickOption))
	q := Question{Selector: "age", Text: "What is your age?", Kind: KindText}
	today := pfDate(2031, 6, 14)
	for _, c := range []struct{ name, dob string }{
		{"unset", ""},
		{"unparseable", "June 15th 2000"},
		{"locale guess refused", "15.06.2000"},
		{"future", "2040-01-01"},
		{"implausible", "1890-01-01"},
		{"impossible month", "2000-13-01"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := alexFacts()
			f.DateOfBirth = c.dob
			r := pfOne(t, m, f, today, q)
			if r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestAnswerBank_Age_BirthdayRolloverAndDateSpellings(t *testing.T) {
	m := pfMatcher(blankEntry(IDCurrentAgeBracket, KindPickOption))
	q := Question{Selector: "age", Text: "What is your age?", Kind: KindText}
	for _, c := range []struct {
		name, dob string
		today     Date
		want      string
	}{
		{"day before the birthday", "2005-06-15", pfDate(2031, 6, 14), "25"},
		{"on the birthday", "2005-06-15", pfDate(2031, 6, 15), "26"},
		{"US spelling", "6/15/2005", pfDate(2031, 6, 15), "26"},
		{"US spelling padded", "06/15/2005", pfDate(2031, 6, 14), "25"},
		{"month later in the year", "2005-12-31", pfDate(2031, 6, 15), "25"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := alexFacts()
			f.DateOfBirth = c.dob
			r := pfOne(t, m, f, c.today, q)
			if r.Answer != c.want || r.Source != SourceDerived || r.EntryID != IDCurrentAgeBracket {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestAnswerBank_Age_MinimumGateShapesAndWhitelist(t *testing.T) {
	m := pfMatcher(blankEntry(IDAgeMinimumThreshold, KindFixed))
	today := pfDate(2026, 10, 11) // Alex is 26
	cases := []struct {
		text string
		want string // "" means abstain
	}{
		{"Are you at least 18 years of age?", "Yes"},
		{"Are you 18+ years of age?", "Yes"},
		{"Are you 18 years or older?", "Yes"},
		// Capital One (Workday, live).
		{"Are you age 18 or over?", "Yes"},
		{"Are you aged 21 or older?", "Yes"},
		{"Are you at least 21 years of age?", "Yes"},
		{"Are you at least 30 years of age?", ""},
		{"Are you at least 40 hours available per week?", ""},
		{"Are you at least 10 years experienced?", ""},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			r := pfOne(t, m, alexFacts(), today, yesNoQ("gate", c.text))
			if c.want == "" {
				if r.Outcome != OutcomeAbstained {
					t.Fatalf("wanted an abstain, got %+v", r)
				}
				return
			}
			if r.Answer != c.want || r.ChosenLabel != c.want {
				t.Fatalf("got %+v", r)
			}
		})
	}
	// Huntress (Greenhouse, live): a years-of-experience question the fuzzy
	// stage put on the age entry is unclaimed; an age it cannot settle stays the age entry's.
	for text, entry := range map[string]string{
		"Do you have at least 5 years of professional Ruby on Rails experience?": "",
		"Are you at least 10 years experienced?":                                 "",
		"Are you at least 30 years of age?":                                      IDAgeMinimumThreshold,
	} {
		r := pfOne(t, m, alexFacts(), today, yesNoQ("gate", text))
		if r.Outcome != OutcomeAbstained || r.EntryID != entry {
			t.Errorf("%q: %+v", text, r)
		}
	}
	t.Run("younger than the gate answers No", func(t *testing.T) {
		f := alexFacts()
		f.DateOfBirth = "2012-01-01"
		r := pfOne(t, m, f, today, yesNoQ("gate", "Are you at least 18 years of age?"))
		if r.Answer != "No" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("minimum age helper", func(t *testing.T) {
		for text, want := range map[string]int{"are you eighteen years of age or older": 18, "at least twenty one years old": 21, "twenty-one years or older": 21, "are you over the age of 21": 21, "16+ years of age": 16, "are you age 18 or over": 18, "at least 40 hours": 0, "a team aged 30 or more": 0,
			// UT Austin: an age the gate names itself counts from 14 to 21.
			"are you at least 15 years of age?": 15, "at least 20 hours a week": 0, "at least 14 years old": 14, "at least 25 years of age": 0} {
			if got, _ := minimumAgeAsked(text); got != want {
				t.Errorf("%q = %d, want %d", text, got, want)
			}
		}
	})
}

func TestAnswerBank_Age_BracketLabelShapesAndExactlyOne(t *testing.T) {
	labels := []struct {
		label string
		lo    int
		hi    int
		ok    bool
	}{
		{"18-24", 18, 24, true}, {"18 - 29", 18, 29, true}, {"25 to 34", 25, 34, true},
		{"Under 18", 0, 17, true}, {"24 and under", 0, 24, true}, {"65+", 65, 200, true},
		{"65 or older", 65, 200, true}, {"Prefer not to say", 0, 0, false}, {"30-20", 0, 0, false},
	}
	for _, c := range labels {
		r, ok := parseAgeRange(c.label)
		if ok != c.ok || (ok && (r.min != c.lo || r.max != c.hi)) {
			t.Errorf("%q = %+v %v", c.label, r, ok)
		}
	}
	m := pfMatcher(blankEntry(IDCurrentAgeBracket, KindPickOption))
	today := pfDate(2026, 10, 11)
	run := func(labels ...string) Result {
		return pfOne(t, m, alexFacts(), today, pfSelect("age", "What is your age range?", labels...))
	}
	if r := run("Under 18", "18-24", "25-34", "35+", "Prefer not to say"); r.ChosenLabel != "25-34" {
		t.Fatalf("got %+v", r)
	}
	if r := run("18-24", "18-29", "25-34", "35+"); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Fatalf("overlapping brackets: %+v", r)
	}
	if r := run("Under 18", "18-24", "Prefer not to say"); r.Outcome != OutcomeAbstained || r.Reason != ReasonInputMismatch {
		t.Fatalf("no containing bracket: %+v", r)
	}
}
