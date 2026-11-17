package match

import "testing"

func windowMatcher(startDate string) *Matcher {
	return pfMatcher(startDateEntry(startDate))
}

func TestAnswerBank_StartWindow_YesWhenAvailableBeforeWindowCloses(t *testing.T) {
	today := pfDate(2026, 10, 2)
	ask := func(m *Matcher, f Facts, text string, labels ...string) Result {
		q := Question{Selector: "w", Text: text, Kind: KindRadioGroup, Options: opts("Yes", "No")}
		if len(labels) > 0 {
			q.Options = opts(labels...)
		}
		return pfOne(t, m, f, today, q)
	}
	jan := "Are you able to start between January 2027 and September 2027?"
	t.Run("a past start date does not make the answer No", func(t *testing.T) {
		r := ask(windowMatcher("2026-06-15"), alexFacts(), jan)
		if r.Answer != "Yes" || r.ChosenLabel != "Yes" || r.Source != SourceDerived || r.Confidence != startProfileDerive {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("a window that closed before availability is never answered No", func(t *testing.T) {
		r := ask(windowMatcher("2026-12-01"), alexFacts(), "Are you able to start between July 2026 and September 2026?")
		if r.Outcome != OutcomeAbstained || r.Answer == "No" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("an unfinished degree moves availability later", func(t *testing.T) {
		f := alexFacts()
		f.Education[0].End = "2027-05"
		if r := ask(windowMatcher("2026-06-15"), f, jan); r.Answer != "Yes" {
			t.Fatalf("window ends after the degree: %+v", r)
		}
		if r := ask(windowMatcher("2026-06-15"), f, "Are you able to start between January 2027 and March 2027?"); r.Outcome == OutcomeFilled {
			t.Fatalf("the degree finishes after the window: %+v", r)
		}
	})
	t.Run("an education entry with no end month makes availability unknown", func(t *testing.T) {
		f := alexFacts()
		f.Education[0].End = ""
		if r := ask(windowMatcher("2026-06-15"), f, jan); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("a blank start date is thirty days from today for windows too", func(t *testing.T) {
		if r := ask(windowMatcher(""), alexFacts(), jan); r.Answer != "Yes" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("a span from today is a window too", func(t *testing.T) {
		within := "Are you able to start working within 30-60 days of applying? (If you are close to completing your degree and can start within 60 days we can accommodate)"
		if r := ask(windowMatcher("2026-11-15"), alexFacts(), within); r.Answer != "Yes" || r.Source != SourceDerived {
			t.Fatalf("available inside the span: %+v", r)
		}
		if r := ask(windowMatcher("2027-03-01"), alexFacts(), within); r.Outcome == OutcomeFilled {
			t.Fatalf("available after the span: %+v", r)
		}
	})
	t.Run("an option-less box receives the literal Yes", func(t *testing.T) {
		q := Question{Selector: "w", Text: jan, Kind: KindText}
		if r := pfOne(t, windowMatcher("2026-06-15"), alexFacts(), today, q); r.Answer != "Yes" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("no start-date entry stands the lane down", func(t *testing.T) {
		if r := ask(pfMatcher(), alexFacts(), jan); r.Source == SourceDerived {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("a select without a Yes option is not claimed", func(t *testing.T) {
		if r := ask(windowMatcher("2026-06-15"), alexFacts(), jan, "Maybe", "Unsure"); r.Source == SourceDerived {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestAnswerBank_StartWindow_DateSpanGrammar(t *testing.T) {
	ends := []struct {
		text string
		end  Date
		ok   bool
	}{
		{"Are you able to start between January 2027 and September 2027?", pfDate(2027, 9, 30), true},
		{"Can you start from 03/01/2027 to 06/15/2027?", pfDate(2027, 6, 15), true},
		{"Would you be available to begin from May 2027 through June 2027?", pfDate(2027, 6, 30), true},
		{"Are you able to start by June 15, 2027?", pfDate(2027, 6, 15), true},
		{"Can you start no later than 2027-02-10?", pfDate(2027, 2, 10), true},
		{"Are you able to start on or before March 2027?", pfDate(2027, 3, 31), true},
		{"Could you start before September 2027?", pfDate(2027, 8, 31), true},
		{"Are you ready to start before January 1, 2028?", pfDate(2027, 12, 31), true},
		{"Are you able to start in 2027?", pfDate(2027, 12, 31), true},
		{"Can you start during Fall 2027?", Date{}, false},
		{"Are you able to start in February 30, 2027?", Date{}, false},
		{"What is the role's start date in 2027?", Date{}, false},
		{"Are you able to start immediately?", Date{}, false},
		{"Are you able to start between January 2027 and September 2027? Our offices close in 2030.", pfDate(2027, 9, 30), true},
		// A span from today ends at its longer bound.
		{"Are you able to start working within 30-60 days of applying? (If you are close to completing your degree and can start within 60 days we can accommodate)", pfDate(2027, 3, 2), true},
		{"Can you start within 2 weeks of an offer?", pfDate(2027, 1, 15), true},
		{"Can you start within a reasonable time?", Date{}, false},
	}
	for _, c := range ends {
		got, ok := startWindowEnd(c.text, pfDate(2027, 1, 1))
		if ok != c.ok || (ok && got != c.end) {
			t.Errorf("%q = %v,%v; want %v,%v", c.text, got, ok, c.end, c.ok)
		}
	}
	spans := []struct {
		text        string
		first, last Date
		ok          bool
	}{
		{"May 2027", pfDate(2027, 5, 1), pfDate(2027, 5, 31), true},
		{"Feb 2028", pfDate(2028, 2, 1), pfDate(2028, 2, 29), true},
		{"Sept. 2027", pfDate(2027, 9, 1), pfDate(2027, 9, 30), true},
		{"June 3rd, 2027", pfDate(2027, 6, 3), pfDate(2027, 6, 3), true},
		{"2027", pfDate(2027, 1, 1), pfDate(2027, 12, 31), true},
		{"1/5/2027", pfDate(2027, 1, 5), pfDate(2027, 1, 5), true},
		{"2027-04-09.", pfDate(2027, 4, 9), pfDate(2027, 4, 9), true},
		{"April 31, 2027", Date{}, Date{}, false},
		{"Fall 2027", Date{}, Date{}, false},
	}
	for _, c := range spans {
		f, l, ok := dateSpan(c.text)
		if ok != c.ok || f != c.first || l != c.last {
			t.Errorf("%q = %v %v %v", c.text, f, l, ok)
		}
	}
}

func TestAnswerBank_StartPeriod_EarliestReachableQuarter(t *testing.T) {
	q4 := "Please indicate which quarter you would be able to start work for this position."
	quarters := []string{"Q1: January 2027 - March 2027", "Q2: April 2027 - June 2027", "Q3: July 2027 - September 2027"}
	today := pfDate(2026, 10, 2)
	run := func(start string, f Facts, labels ...string) Result {
		return pfOne(t, windowMatcher(start), f, today, pfSelect("p", q4, labels...))
	}
	f := alexFacts()
	f.Education[0].End = "2027-02"
	if r := run("2026-06-15", f, quarters...); r.ChosenLabel != quarters[0] || r.Source != SourceDerived || r.Confidence != startProfileDerive {
		t.Fatalf("degree finishing in February 2027: %+v", r)
	}
	f.Education[0].End = "2027-05"
	if r := run("2026-06-15", f, quarters...); r.ChosenLabel != quarters[1] {
		t.Fatalf("a period that ends before availability is skipped: %+v", r)
	}
	f.Education[0].End = "2028-05"
	if r := run("2026-06-15", f, quarters...); r.Outcome != OutcomeAbstained || r.Reason != ReasonInputMismatch {
		t.Fatalf("no reachable period: %+v", r)
	}
	f = alexFacts()
	if r := run("2026-06-15", f, "Other", quarters[1], "Q1: January 2027 - March 2027"); r.ChosenLabel != "Q1: January 2027 - March 2027" {
		t.Fatalf("the earliest period wins whatever the page order: %+v", r)
	}
	if r := run("2026-06-15", f, "Other", "As soon as possible"); r.Outcome != OutcomeAbstained {
		t.Fatalf("options naming no date are skipped: %+v", r)
	}
	if r, want := run("", f, quarters...), run(today.AddDays(30).ISO(), f, quarters...); r.ChosenLabel == "" || r.ChosenLabel != want.ChosenLabel {
		t.Fatalf("a blank start date is thirty days from today: %+v, want %+v", r, want)
	}
	// Whether-you-can-start is not a which-period question.
	if r := pfOne(t, windowMatcher("2026-06-15"), f, today, pfSelect("p", "Are you able to start in the first quarter?", quarters...)); r.Source == SourceDerived {
		t.Fatalf("got %+v", r)
	}
}

// The date inside an option label is found without lookaround: a date glued to
// a word character, slash or hyphen is part of a longer token.
func TestAnswerBank_StartPeriod_DateBoundaryWithoutLookaround(t *testing.T) {
	cases := []struct {
		label string
		want  []string
	}{
		{"Q1: January 2027 - March 2027", []string{"January 2027", "March 2027"}},
		{"May 2027 June 2027", []string{"May 2027", "June 2027"}},
		{"(2027-03-01)", []string{"2027-03-01"}},
		{"2027", []string{"2027"}},
		{"Spring term 2027", []string{"2027"}},
		{"ref A2027", nil},
		{"2027x", nil},
		{"Q1-2027", nil},
		{"12/05/2027/b", nil},
		{"2027-03-01-extra", nil},
		{"through 03/31/2027.", []string{"03/31/2027"}},
		{"No dates here", nil},
	}
	for _, c := range cases {
		got := scanDates(c.label)
		if len(got) != len(c.want) {
			t.Errorf("%q = %v, want %v", c.label, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q = %v, want %v", c.label, got, c.want)
			}
		}
	}
	if end, ok := periodEnd("Q2: April 2027 - June 2027"); !ok || end != pfDate(2027, 6, 30) {
		t.Fatalf("period end = %v %v", end, ok)
	}
	if _, ok := periodEnd("Other"); ok {
		t.Fatal("a label with no date has no period")
	}
}

func TestAnswerBank_Derived_StoredDateAsSpanOption(t *testing.T) {
	expiry := ent(idWorkAuthorizationExpiry, "fixed", withAnswer("2029-03-01"), withTypes(InputText, InputDate))
	m := pfMatcher(expiry)
	today := pfDate(2026, 10, 1)
	text := "What is your employment authorization end date?"
	labels := []string{"In 1 to 3 years.", "In 4 to 10 years.", "No end date", "Other"}
	r := pfOne(t, m, alexFacts(), today, pfSelect("s", text, labels...))
	if r.ChosenLabel != "In 1 to 3 years." || r.EntryID != idWorkAuthorizationExpiry || r.Source != SourceDerived {
		t.Fatalf("got %+v", r)
	}
	later := pfMatcher(ent(idWorkAuthorizationExpiry, "fixed", withAnswer("2033-03-01"), withTypes(InputText, InputDate)))
	if r := pfOne(t, later, alexFacts(), today, pfSelect("s", text, labels...)); r.ChosenLabel != "In 4 to 10 years." {
		t.Fatalf("got %+v", r)
	}
	past := pfMatcher(ent(idWorkAuthorizationExpiry, "fixed", withAnswer("2025-03-01"), withTypes(InputText, InputDate)))
	if r := pfOne(t, past, alexFacts(), today, pfSelect("s", text, labels...)); r.Source == SourceDerived {
		t.Fatalf("a past date yields no span answer: %+v", r)
	}
	if r := pfOne(t, m, alexFacts(), today, pfSelect("s", text, "In 1 to 3 years.", "No end date")); r.Source == SourceDerived {
		t.Fatalf("fewer than two span options: %+v", r)
	}
	if r := pfOne(t, m, alexFacts(), today, pfSelect("s", text, "In 1 to 3 years.", "In 2 to 4 years.")); r.Source == SourceDerived {
		t.Fatalf("overlapping spans are ambiguous: %+v", r)
	}
	// A control the entry accepts is left to the ordinary path.
	if r := pfOne(t, m, alexFacts(), today, Question{Selector: "s", Text: text, Kind: KindText}); r.Source == SourceDerived {
		t.Fatalf("got %+v", r)
	}
	for _, c := range []struct {
		label    string
		from, to float64
		ok       bool
	}{
		{"In 1 to 3 years", 1, 4, true},
		{"Within 6 to 12 months", 0.5, 13.0 / 12, true},
		{"Less than 2 years", 0, 2, true},
		{"Under 6 months", 0, 0.5, true},
		{"More than 5 years", 5, openUpper, true},
		{"10+ years", 10, openUpper, true},
		{"No end date", 0, 0, false},
	} {
		from, to, ok := spanOfLabel(c.label)
		if ok != c.ok || (ok && (from != c.from || to != c.to)) {
			t.Errorf("%q = %v %v %v", c.label, from, to, ok)
		}
	}
}

func TestAnswerBank_CalendarDate_ParseAndClamp(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
		d  Date
	}{
		{"2031-03-04", true, pfDate(2031, 3, 4)}, {"3/4/2031", true, pfDate(2031, 3, 4)}, {"03/04/2031", true, pfDate(2031, 3, 4)},
		{" 2031-03-04 ", true, pfDate(2031, 3, 4)}, {"2028-02-29", true, pfDate(2028, 2, 29)},
		{"2027-02-29", false, Date{}}, {"2031-13-01", false, Date{}}, {"03-04-2031", false, Date{}},
		{"2031/03/04", false, Date{}}, {"March 4, 2031", false, Date{}}, {"31-03-2031", false, Date{}}, {"", false, Date{}},
		{"3/4/31", false, Date{}}, {"2031-3-4", false, Date{}},
	} {
		if d, ok := parseCalendarDate(c.in); ok != c.ok || (ok && d != c.d) {
			t.Errorf("%q = %v %v", c.in, d, ok)
		}
	}
	// Dates move across month, year and leap boundaries by day count.
	if got := pfDate(2027, 12, 31).AddDays(1); got != pfDate(2028, 1, 1) {
		t.Errorf("year rollover %v", got)
	}
	if got := pfDate(2028, 3, 1).AddDays(-1); got != pfDate(2028, 2, 29) {
		t.Errorf("leap day %v", got)
	}
	if n := pfDate(2031, 7, 4).DaysUntil(pfDate(2031, 8, 3)); n != 30 {
		t.Errorf("days until = %d", n)
	}
	// A stale future-only date is typed as thirty days from today.
	stages := Stages{Fixed: func(s *Step) (Draft, bool) { return futureOnlyDate(s, s.Entry) }}
	m := New(bankOf(startDateEntry("2031-06-20")), stages)
	r := pfOne(t, m, alexFacts(), pfDate(2031, 7, 4), Question{Selector: "d", Text: "When can you start?", Kind: KindText})
	if r.Answer != "2031-08-03" {
		t.Fatalf("got %+v", r)
	}
}
