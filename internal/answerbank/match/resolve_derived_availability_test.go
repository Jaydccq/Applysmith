package match

import "testing"

func startDateEntry(answer string) Entry {
	e := ent(IDEarliestStartDate, "fixed", withAnswer(answer))
	if answer == "" {
		e = blankEntry(IDEarliestStartDate, KindFixed)
	}
	return e
}

// availabilityMatcher declares the input types the spec scenarios need; the
// shipped entry lists only single_select.
func availabilityMatcher(startDate string) *Matcher {
	avail := blankEntry(IDFullTimeStartAvailable, KindPickOption)
	avail.InputTypes = []string{InputSingleSelect, InputText, InputDate}
	return pfMatcher(avail, startDateEntry(startDate))
}

const availWhen = "When are you available to work full-time?"

// Every start lane reads one first day: the stored start moved past any
// education that has not finished, as the start window and period do, so two
// questions on one page never disagree.
func TestAnswerBank_Availability_AnUnfinishedDegreeMovesEveryStartLane(t *testing.T) {
	today := pfDate(2026, 10, 2)
	enrolled := alexFacts()
	enrolled.Education[0].End = "2027-05"
	labels := []string{"Immediately", "1 month", "3 months", "9 months"}
	if r := pfOne(t, availabilityMatcher("2026-06-15"), enrolled, today, pfSelect("avail", availWhen, labels...)); r.ChosenLabel != "9 months" {
		t.Fatalf("a lead time before the degree ends: %+v", r)
	}
	if r := pfOne(t, availabilityMatcher("2026-06-15"), enrolled, today, pfSelect("avail", availWhen, labels[:3]...)); r.Outcome != OutcomeAbstained {
		t.Fatalf("no lead time reaches the degree's end: %+v", r)
	}
	box := Question{Selector: "avail", Text: availWhen, Kind: KindText}
	if r := pfOne(t, availabilityMatcher("2026-06-15"), enrolled, today, box); r.Answer != "May 31, 2027" {
		t.Fatalf("the date in words: %+v", r)
	}
	stages := Stages{Fixed: func(s *Step) (Draft, bool) { return futureOnlyDate(s, s.Entry) }}
	when := Question{Selector: "d", Text: "When can you start?", Kind: KindText}
	if r := pfOne(t, New(bankOf(startDateEntry("2026-12-01")), stages), enrolled, today, when); r.Answer != "2027-05-31" {
		t.Fatalf("the typed start date: %+v", r)
	}
	// An education entry with no end month leaves the first day unknown.
	open := alexFacts()
	open.Education[0].End = ""
	if r := pfOne(t, availabilityMatcher("2026-06-15"), open, today, pfSelect("avail", availWhen, labels...)); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("an unknown end, lead times: %+v", r)
	}
	if r := pfOne(t, New(bankOf(startDateEntry("2026-12-01")), stages), open, today, when); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("an unknown end, typed date: %+v", r)
	}
}

func TestAnswerBank_Availability_ParseLeadDaysTable(t *testing.T) {
	for _, c := range []struct {
		label string
		days  int
		ok    bool
	}{
		{"Immediately", 0, true}, {"As soon as possible", 0, true}, {"ASAP", 0, true}, {"Right away", 0, true},
		{"At once", 0, true}, {"Anytime", 0, true}, {"Any time", 0, true}, {"Currently available", 0, true},
		{"Available now", 0, true}, {"Start now", 0, true},
		{"2 weeks", 14, true}, {"Two weeks notice", 14, true}, {"1-2 weeks", 14, true}, {"2 to 4 weeks", 28, true},
		{"30 days", 30, true}, {"1 month", 30, true}, {"Within six months", 180, true}, {"3months", 90, true},
		{"Other", 0, false}, {"Fall 2026", 0, false}, {"Prefer not to say", 0, false}, {"", 0, false},
	} {
		if got, ok := availabilityLeadDays(c.label); got != c.days || ok != c.ok {
			t.Errorf("%q = %d,%v; want %d,%v", c.label, got, ok, c.days, c.ok)
		}
	}
}

func TestAnswerBank_Availability_TermOptionsHoldTheStart(t *testing.T) {
	today := pfDate(2031, 11, 2)
	labels := []string{"Available to Start Immediately", "Fall 2031", "Winter 2031", "Spring 2032", "Summer 2032"}
	for _, c := range []struct{ name, start, want string }{
		{"a day away is no immediate start but is in the fall term", "2031-11-03", "Fall 2031"},
		{"today is immediate", "2031-11-02", "Available to Start Immediately"},
		{"winter runs from December", "2031-12-15", "Winter 2031"},
		{"a later term", "2032-04-01", "Spring 2032"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, availabilityMatcher(c.start), alexFacts(), today, pfSelect("avail", availWhen, labels...))
			if r.ChosenLabel != c.want {
				t.Fatalf("got %+v", r)
			}
		})
	}
	// Calendar quarters are terms too (Stripe).
	quarters := []string{"Q4 2031 (October - December)", "Q1 2032 (January - March)", "Q2 2032 (April - June)", "Q3 2032 (July - September)"}
	for start, want := range map[string]string{"2031-11-02": quarters[0], "2032-03-31": quarters[1], "2032-07-01": quarters[3]} {
		if r := pfOne(t, availabilityMatcher(start), alexFacts(), today, pfSelect("avail", availWhen, quarters...)); r.ChosenLabel != want {
			t.Fatalf("start %s: got %+v", start, r)
		}
	}
	// Two terms holding the start are no answer.
	r := pfOne(t, availabilityMatcher("2031-11-03"), alexFacts(), today, pfSelect("avail", availWhen, "Fall 2031", "Summer 2031 or later"))
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("got %+v", r)
	}
}

func TestAnswerBank_Availability_TextBoxForms(t *testing.T) {
	today := pfDate(2031, 7, 4)
	box := Question{Selector: "avail", Text: availWhen, Kind: KindText}
	for _, c := range []struct{ name, start, want string }{
		{"thirty days ahead", "2031-08-03", "August 3, 2031"},
		{"a passed date is thirty days from today", "2031-01-02", "August 3, 2031"},
		{"today", "2031-07-04", "Immediately"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, availabilityMatcher(c.start), alexFacts(), today, box)
			if r.Answer != c.want {
				t.Fatalf("got %+v", r)
			}
		})
	}
	t.Run("date control gets an ISO date and never a past one", func(t *testing.T) {
		q := box
		q.InputType = InputDate
		if r := pfOne(t, availabilityMatcher("2031-08-03"), alexFacts(), today, q); r.Answer != "2031-08-03" {
			t.Fatalf("got %+v", r)
		}
		if r := pfOne(t, availabilityMatcher("2031-01-02"), alexFacts(), today, q); r.Answer != "2031-08-03" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("a blank start date is thirty days from today", func(t *testing.T) {
		r := pfOne(t, availabilityMatcher(""), alexFacts(), today, box)
		if r.Answer != "August 3, 2031" {
			t.Fatalf("got %+v", r)
		}
		r = pfOne(t, availabilityMatcher(""), alexFacts(), today, pfSelect("avail", availWhen, "Immediately", "2 weeks notice"))
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonInputMismatch {
			t.Fatalf("no lead-time option reaches thirty days: %+v", r)
		}
	})
	t.Run("the ask-me marker is left for the user", func(t *testing.T) {
		if r := pfOne(t, availabilityMatcher(AskMe), alexFacts(), today, box); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("unparseable start date", func(t *testing.T) {
		if r := pfOne(t, availabilityMatcher("next spring"), alexFacts(), today, box); r.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestAnswerBank_Availability_MonthYearBoxNeverPastJobMonth(t *testing.T) {
	today := pfDate(2031, 7, 4)
	q := Question{Selector: "avail", Kind: KindText,
		Text: "When is the earliest you would be available to work a full-time role? Please input month and year."}
	for _, c := range []struct{ name, start, want string }{
		{"stored month still ahead", "2031-10-15", "October 2031"},
		{"date passed: thirty days from today", "2031-02-01", "August 2031"},
		{"today: the current month", "2031-07-04", "July 2031"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, availabilityMatcher(c.start), alexFacts(), today, q)
			if r.Answer != c.want {
				t.Fatalf("got %+v", r)
			}
		})
	}
	if q.InputType = inferInputType(q); q.InputType != InputText {
		t.Fatalf("the word year must not make a number box: %s", q.InputType)
	}
}

func TestAnswerBank_Availability_AbstainsInsideEntitySection(t *testing.T) {
	m := availabilityMatcher("2031-07-04")
	today := pfDate(2031, 7, 4)
	q := Question{Selector: "avail", Text: availWhen, Kind: KindText}
	if r := pfOne(t, m, alexFacts(), today, q); r.Outcome != OutcomeFilled {
		t.Fatalf("outside a section the entry answers: %+v", r)
	}
	for _, path := range [][]string{{"Work Experience"}, {"Education", "Education 1"}} {
		q.SectionPath = path
		r := pfOne(t, m, alexFacts(), today, q)
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonSectionPolicy || !r.Terminal {
			t.Fatalf("%v: %+v", path, r)
		}
	}
	q.Text = "Are you available to start on a date within 2 weeks?"
	q.SectionPath = []string{"Work Experience"}
	if r := pfOne(t, pfMatcher(startDateEntry("2031-07-04")), alexFacts(), today, q); r.Outcome == OutcomeFilled {
		t.Fatalf("the start-date entry is refused in an entity section too: %+v", r)
	}
}

func TestAnswerBank_AnyShift_FromTheShiftsYouRecorded(t *testing.T) {
	// Huntington Ingalls (SuccessFactors) doubles the label and adds "is required".
	ask := "Are you willing to work any shift? Are you willing to work any shift? is required"
	for _, c := range []struct {
		text   string
		picked []string
		want   string
	}{
		{ask, []string{"Day"}, "No"},
		{ask, []string{"Day", "Evening", "Night"}, "Yes"},
		{ask, []string{"Any shift"}, "Yes"},
		{"Are you able to work all shifts, including weekends?", []string{"Day", "Evening", "Night"}, "No"},
		{ask, nil, ""},
	} {
		e := ent("shift-availability", "multi-select", withTypes(InputMultiSelect))
		e.Selected = c.picked
		q := selectQ(c.text, "No", "Yes")
		q.Kind = KindCustomListbox
		r := matchReq(pickMatcher(e), q)
		if c.want == "" {
			if r.Outcome != OutcomeAbstained {
				t.Fatalf("no shift recorded was answered: %+v", r)
			}
			continue
		}
		wantFill(t, r, c.want, OutcomeFilled)
	}
}
