package match

import "testing"

func TestAnswerBank_Derive_OrderAgeAvailabilityWorkAuthEmployerThenKind(t *testing.T) {
	today := pfDate(2026, 10, 11)
	t.Run("a derived id decides before the stored body and an abstain is final", func(t *testing.T) {
		stored := ent(IDCurrentAgeBracket, "pick-option", withPreferred("99"))
		m := pfMatcher(stored)
		f := alexFacts()
		q := Question{Selector: "age", Text: "What is your age?", Kind: KindText}
		if r := pfOne(t, m, f, today, q); r.Answer != "26" {
			t.Fatalf("the date of birth wins over the stored body: %+v", r)
		}
		f.DateOfBirth = ""
		if r := pfOne(t, m, f, today, q); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("an abstain never falls through to the literal body: %+v", r)
		}
	})
	t.Run("dispatch by entry id", func(t *testing.T) {
		step := func(id string) *Step {
			e := ent(id, "fixed")
			return &Step{Entry: &e, Entries: []Entry{e}, Req: &Request{Facts: alexFacts(), Today: today}, Q: Question{Kind: KindText}}
		}
		for _, id := range []string{IDCurrentAgeBracket, IDAgeMinimumThreshold, IDFullTimeStartAvailable, IDWorkAuthorizationDetail} {
			if _, ok := derivedEntry(step(id)); !ok {
				t.Errorf("%s is not handled", id)
			}
		}
		if _, ok := derivedEntry(step("some-other-entry")); ok {
			t.Error("an unknown id is not handled")
		}
	})
}

func TestAnswerBank_Derive_EmptyBodyDerivedEntriesAreNotPlaceholderAbstains(t *testing.T) {
	today := pfDate(2026, 10, 11)
	for _, id := range []string{IDCurrentAgeBracket, IDAgeMinimumThreshold, IDFullTimeStartAvailable, IDWorkAuthorizationDetail, IDEarliestStartDate} {
		if !BlankByDesign(id) {
			t.Errorf("%s must be blank by design", id)
		}
	}
	f := alexFacts()
	m := pfMatcher(blankEntry(IDCurrentAgeBracket, KindPickOption), blankEntry(IDEarliestStartDate, KindFixed))
	if r := pfOne(t, m, f, today, Question{Selector: "a", Text: "What is your age?", Kind: KindText}); r.Answer != "26" {
		t.Fatalf("derived entry with a blank body answers: %+v", r)
	}
	// A blank earliest-start-date is thirty days from today (owner's rule).
	r := pfOne(t, m, f, today, Question{Selector: "s", Text: "Earliest start date", Kind: KindText})
	if r.Answer != "2026-11-10" {
		t.Fatalf("got %+v", r)
	}
}

func TestAnswerBank_StartDate_BlankOrPassedDateIsThirtyDaysFromToday(t *testing.T) {
	stages := Stages{Fixed: func(s *Step) (Draft, bool) { return futureOnlyDate(s, s.Entry) }}
	today := pfDate(2031, 7, 4)
	box := Question{Selector: "d", Text: "When can you start?", Kind: KindText}
	for _, c := range []struct{ name, stored, want string }{
		{"two weeks stale", "2031-06-20", "2031-08-03"},
		{"US spelling, stale", "6/20/2031", "2031-08-03"},
		{"blank", "", "2031-08-03"},
		{"today unchanged", "2031-07-04", "2031-07-04"},
		{"future unchanged", "2031-08-15", "2031-08-15"},
		{"future US spelling unchanged", "8/15/2031", "8/15/2031"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, New(bankOf(startDateEntry(c.stored)), stages), alexFacts(), today, box)
			if r.Answer != c.want {
				t.Fatalf("got %+v", r)
			}
		})
	}
	t.Run("not a date abstains", func(t *testing.T) {
		r := pfOne(t, New(bankOf(startDateEntry("after graduation")), stages), alexFacts(), today, box)
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("the ask-me marker is left for the user", func(t *testing.T) {
		r := pfOne(t, New(bankOf(startDateEntry(AskMe)), stages), alexFacts(), today, box)
		if r.Outcome != OutcomeAbstained {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("an option list is left to the availability entry", func(t *testing.T) {
		r := pfOne(t, New(bankOf(startDateEntry("2031-08-15")), stages), alexFacts(), today,
			pfSelect("d", "Earliest start date", "Immediately", "2 weeks notice"))
		if r.Outcome != OutcomeAbstained {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestAnswerBank_ContextPolicy_GuardOrder(t *testing.T) {
	step := func(location, text string) *Step {
		return &Step{Req: &Request{Posting: Posting{Location: location}, Facts: alexFacts()}, Q: Question{Text: text}}
	}
	t.Run("jurisdiction answers first for a work-authorization entry", func(t *testing.T) {
		e := ent("work-auth-us", "fixed", withAnswer("Yes"))
		a, _, hit := profileContextGuards(step("Toronto, Canada", "Are you authorized to work in the country where this job is located?"), &e)
		if !hit || a.Detail != DetailPostingCountry || !a.Terminal() {
			t.Fatalf("got %+v %v", a, hit)
		}
	})
	t.Run("an unguarded entry passes every policy", func(t *testing.T) {
		e := ent("preferred-name", "fixed", withAnswer("Alex"))
		if _, _, hit := profileContextGuards(step("Toronto, Canada", "Preferred name"), &e); hit {
			t.Fatal("no policy applies")
		}
	})
	t.Run("currency is judged before held compensation", func(t *testing.T) {
		comp := Entry{ID: "salary-expectation", Kind: KindCompensationRange, Body: Body{DefaultAnswer: "150000", Currency: "USD"}}
		held := "What deferred compensation do you currently hold, in local currency?"
		held += " Please state the amount in EUR."
		a, _, hit := profileContextGuards(step("Berlin, Germany", held), &comp)
		if !hit || a.Reason != ReasonContextPolicy || a.Detail != DetailLocalCurrency {
			t.Fatalf("the currency block comes before the held-compensation block: %+v %v", a, hit)
		}
	})
}

func TestAnswerBank_ConflictSituations_NoOnlyWhenEverySituationIsNo(t *testing.T) {
	fixed := func(id, answer string) Entry {
		e := blankEntry(id, KindFixed)
		e.InputTypes = []string{"yes_no"}
		if answer != "" {
			e.Answer, e.Placeholder = answer, false
		}
		return e
	}
	bank := func(own string, facts map[string]string) *Matcher {
		es := []Entry{fixed(idConflictSituations, own)}
		for _, id := range conflictSituationFacts {
			es = append(es, fixed(id, facts[id]))
		}
		return pickMatcher(es...)
	}
	allNo := map[string]string{}
	for _, id := range conflictSituationFacts {
		allNo[id] = "No"
	}
	q := selectQ("To identify situations that may pose a conflict, please advise if you would be engaging in any of the above listed situations or activities during your employment.", "Yes", "No")
	wantFill(t, matchReq(bank("", allNo), q), "No", OutcomeFilled)
	for _, id := range []string{"relatives-at-employer", "financial-interests-conflict"} {
		for _, v := range []string{"", "Yes"} {
			facts := map[string]string{}
			for k, x := range allNo {
				facts[k] = x
			}
			facts[id] = v
			if r := matchReq(bank("", facts), q); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
				t.Errorf("%s=%q: %+v, want the question left to the user", id, v, r)
			}
		}
	}
	// The user's own answer is the answer.
	wantFill(t, matchReq(bank("Yes", allNo), q), "Yes", OutcomeFilled)
}
