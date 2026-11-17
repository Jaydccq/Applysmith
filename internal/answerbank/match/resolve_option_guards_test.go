package match

import (
	"slices"
	"strings"
	"testing"
)

// guardMatcher answers a fixed entry with its literal body and applies only the
// output guards, so the guards are seen on their own.
func guardMatcher(e Entry) *Matcher {
	return New(bankOf(e), Stages{Fixed: fixedStage, OutputGuards: outputGuards})
}

func TestOutputGuard_OptionConstraint(t *testing.T) {
	const q = "Pick a month"
	e := fixedEnt("month", q, "2027")
	sel := selectQ(q, "January", "February")
	r := one(guardMatcher(e), sel)
	wantAbstain(t, r, ReasonAmbiguous, "")
	if !traceHas(r, "option-constraint") || !traceHas(r, "January, February") {
		t.Fatalf("the trace names the producer and the options: %v", r.Trace)
	}
	// A value or label the control holds passes, case and space aside.
	ok := fixedEnt("month", q, " february ")
	if r := one(guardMatcher(ok), sel); r.Outcome == OutcomeAbstained {
		t.Fatalf("a held value passes: %+v", r)
	}
	// Excluded on purpose: text, custom listbox, empty option lists, intentional blanks.
	if r := one(guardMatcher(e), textQ("s", q)); r.Outcome == OutcomeAbstained {
		t.Fatalf("text: %+v", r)
	}
	lb := sel
	lb.Kind = KindCustomListbox
	if r := one(guardMatcher(e), lb); r.Outcome == OutcomeAbstained {
		t.Fatalf("custom listbox: %+v", r)
	}
	none := selectQ(q)
	if r := one(guardMatcher(e), none); r.Outcome == OutcomeAbstained {
		t.Fatalf("empty option list: %+v", r)
	}
	if d := optionConstraintDraft(t, sel, Draft{}); d {
		t.Fatal("a blank fill passes")
	}
	// A multi-select list must hold only offered labels.
	cb := checkboxQ(q, "Remote", "Onsite")
	if _, hit := optionConstraint(&Step{Q: cb}, Draft{ChosenLabels: []string{"Remote", "Hybrid"}}); !hit {
		t.Fatal("an unoffered label in the list")
	}
	if _, hit := optionConstraint(&Step{Q: cb}, Draft{ChosenLabels: []string{"Remote", "Onsite"}}); hit {
		t.Fatal("offered labels pass")
	}
}

func optionConstraintDraft(t *testing.T, q Question, d Draft) bool {
	t.Helper()
	_, hit := optionConstraint(&Step{Q: q}, d)
	return hit
}

func TestOutputGuard_NumericBoxNeverGetsYesNo(t *testing.T) {
	const enc = "In your most recent role, did you primarily write code yourself? Use 1 for Yes and 0 for No."
	num := Question{Selector: "s", Text: enc, Kind: KindText, InputType: InputNumber}
	e := fixedEnt("coded", enc, "No")
	r := one(guardMatcher(e), num)
	if r.Answer != "0" || r.Outcome == OutcomeAbstained || !traceHas(r, "states its own encoding") {
		t.Fatalf("the stated encoding is written: %+v", r)
	}
	yes := fixedEnt("coded", enc, "Yes")
	if r := one(guardMatcher(yes), num); r.Answer != "1" {
		t.Fatalf("yes: %+v", r)
	}
	const bare = "Do you have experience with Go?"
	num2 := Question{Selector: "s", Text: bare, Kind: KindText, InputType: InputNumber}
	wantAbstain(t, one(guardMatcher(fixedEnt("go", bare, "No")), num2), ReasonAmbiguous, "")
	// An answer with no digit is never held by a number box; units keep their digits.
	wantAbstain(t, one(guardMatcher(fixedEnt("go", bare, "-- No answer --")), num2), ReasonAmbiguous, "")
	if r := one(guardMatcher(fixedEnt("go", bare, "130000 USD")), num2); r.Answer != "130000 USD" {
		t.Fatalf("units: %+v", r)
	}
	if n, ok := numeralForYesNo("Enter 1 for yes", "Yes"); ok || n != "" {
		t.Fatal("both encodings are required")
	}
	if n, ok := numeralForYesNo("Type 1.5 for Yes and -1 for No.", "no"); !ok || n != "-1" {
		t.Fatalf("signed and decimal numerals: %q %v", n, ok)
	}
}

func TestOutputGuard_WrapperOrderOptionBeforeNumeric(t *testing.T) {
	// Both guards apply to different controls, in this order: a select is held
	// to its options before the number rule is consulted.
	s := &Step{Q: Question{Kind: KindSelect, InputType: InputNumber, Options: opts("1", "2"), Text: "Count?"}, out: Draft{Answer: "No"}}
	g, hit := outputGuards(s)
	if !hit || !traceHasDraft(g, "option-constraint") {
		t.Fatalf("option constraint first: %+v", g)
	}
}

func traceHasDraft(d Draft, sub string) bool {
	for _, l := range d.Trace {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestOptionBinding_ClosedListboxDefer(t *testing.T) {
	const q = "Are you a citizen?"
	e := fixedEnt("citizen", q, "No")
	lb := Question{Selector: "s", Text: q, Kind: KindCustomListbox, InputType: InputYesNo}
	r := matchReq(pickMatcher(e), lb)
	if r.Outcome != OutcomeFilled || r.Answer != "No" || r.ChosenLabel != "No" {
		t.Fatalf("defers the option lookup: %+v", r)
	}
}

// A deferred pick carries the rest of the entry's ranking, once each, so the
// apply step can search for the next when a search holds none of the first
// (Applied Intuition's Ashby language search lists "Python", nothing for
// "Python 3"). A pick bound to a listed option carries none.
func TestOptionBinding_ClosedListboxDeferCarriesTheRanking(t *testing.T) {
	const q = "What is your preferred programming language for interviews?"
	e := curatedPick("preferred-coding-language", q, "Python 3", "Python", "Java", "TypeScript", "Typescript")
	lb := Question{Selector: "s", Text: q, Kind: KindCustomListbox, InputType: InputSingleSelect}
	r := matchReq(pickMatcher(e), lb)
	if r.Answer != "Python 3" || !slices.Equal(r.Alternatives, []string{"Python", "Java", "TypeScript"}) {
		t.Fatalf("the ranking after the answer, once each: %+v", r)
	}
	listed := matchReq(pickMatcher(e), Question{Selector: "s", Text: q, Kind: KindCustomListbox, InputType: InputSingleSelect, Options: opts("Bash", "Java", "Python")})
	if listed.ChosenLabel != "Python" || listed.Alternatives != nil {
		t.Fatalf("a listed option needs no ranking: %+v", listed)
	}
}
