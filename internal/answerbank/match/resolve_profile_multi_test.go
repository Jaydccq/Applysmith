package match

import (
	"reflect"
	"testing"
)

func dMulti(question string, selected []string, mods ...func(*Entry)) Entry {
	e := Entry{ID: "work-mode-preference", Kind: KindMultiSelect, Body: Body{Selected: selected},
		Meta: Meta{Examples: []string{question}, InputTypes: []string{InputMultiSelect, InputSingleSelect, InputYesNo, InputCheckbox, InputText}}}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func multiAnswer(t *testing.T, e Entry, q Question) Result {
	t.Helper()
	return pfOne(t, pfMatcher(e), Facts{}, Date{}, q)
}

func checkboxGroup(text string, labels ...string) Question {
	return Question{Selector: "s", Text: text, Kind: KindCheckbox, InputType: InputSingleSelect, Options: opts(labels...)}
}

func TestAnswerBank_MultiSelect_CheckboxGroupBindsEverySelection(t *testing.T) {
	const q = "Which work settings are you open to?"
	e := dMulti(q, []string{"Hybrid", "Onsite", "Remote"})
	// The extension labels a checkbox group of non-Yes/No options single_select.
	r := multiAnswer(t, e, checkboxGroup(q, "Onsite", "Remote", "Hybrid"))
	if r.Outcome != OutcomeFilled || !reflect.DeepEqual(r.ChosenLabels, []string{"Hybrid", "Onsite", "Remote"}) || r.ChosenLabel != "Hybrid" {
		t.Fatalf("%+v", r)
	}
	if r.Answer != "Hybrid, Onsite, Remote" {
		t.Fatalf("answer %q", r.Answer)
	}
	multi := Question{Selector: "s", Text: q, Kind: KindSelect, InputType: InputMultiSelect, Options: opts("Onsite", "Remote", "Hybrid")}
	if r := multiAnswer(t, e, multi); len(r.ChosenLabels) != 3 {
		t.Fatalf("a multi_select control binds all: %+v", r)
	}
	t.Run("a selection with no option is dropped, not guessed", func(t *testing.T) {
		r := multiAnswer(t, e, checkboxGroup(q, "Onsite", "Remote", "Travel"))
		if !reflect.DeepEqual(r.ChosenLabels, []string{"Onsite", "Remote"}) || !traceHas(r, "dropped, not guessed") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("nothing binds abstains low-confidence", func(t *testing.T) {
		r := multiAnswer(t, e, checkboxGroup(q, "Travel", "Nights"))
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonLowConfidence {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a standalone consent checkbox is not multi-bound", func(t *testing.T) {
		single := Question{Selector: "s", Text: q, Kind: KindCheckbox, InputType: InputCheckbox, Options: opts("Remote")}
		r := multiAnswer(t, e, single)
		if len(r.ChosenLabels) > 1 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("the outcome follows the band", func(t *testing.T) {
		capped := dMulti(q, []string{"Hybrid", "Onsite"}, func(e *Entry) { c := 0.80; e.Confidence = &c })
		r := multiAnswer(t, capped, checkboxGroup(q, "Onsite", "Hybrid"))
		if r.Outcome != OutcomeFilledReview {
			t.Fatalf("an entry cap under 0.90 is a review fill: %+v", r)
		}
		review := dMulti(q, []string{"Hybrid"}, func(e *Entry) { e.NeedsReview = true })
		r = multiAnswer(t, review, checkboxGroup(q, "Onsite", "Hybrid"))
		if r.Outcome == OutcomeFilled {
			t.Fatalf("needs_review is never an automatic fill: %+v", r)
		}
	})
}

func TestAnswerBank_MultiSelect_SingleAnswerControlTakesFirstBoundSelection(t *testing.T) {
	const q = "What is your preferred work mode?"
	e := dMulti(q, []string{"Hybrid", "Onsite", "Remote"})
	dropdown := func(labels ...string) Question {
		return Question{Selector: "s", Text: q, Kind: KindSelect, InputType: InputSingleSelect, Options: opts(labels...)}
	}
	if r := multiAnswer(t, e, dropdown("Remote", "Onsite", "Hybrid")); r.ChosenLabel != "Hybrid" || r.Outcome == OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
	if r := multiAnswer(t, e, dropdown("Remote", "Onsite")); r.ChosenLabel != "Onsite" {
		t.Fatalf("the next selection in the user's order: %+v", r)
	}
	if r := multiAnswer(t, e, dropdown("Nights", "Weekends")); r.Outcome != OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
	// Selections unrelated to a Yes/No control do not answer it.
	firms := dMulti("Are you a current or former employee of an audit firm?", []string{"Example Audit LLP", "Acme Audit"})
	if r := multiAnswer(t, firms, yesNoQ("s", "Are you a current or former employee of an audit firm?")); r.Outcome != OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerBank_MultiSelect_SelectUpToCapShapes(t *testing.T) {
	tests := []struct {
		text string
		n    int
		ok   bool
	}{
		{"Select up to 2 technologies", 2, true},
		{"Please choose at most 3", 3, true},
		{"Pick no more than 4 options", 4, true},
		{"Select a maximum of 5", 5, true},
		{"Maximum of 3 answers", 3, true},
		{"You may give up to 2 options", 2, true},
		{"Select up to 0", 0, false},
		{"Select all that apply", 0, false},
		{"We have 3 offices", 0, false},
	}
	for _, tc := range tests {
		n, ok := selectionCap(tc.text)
		if n != tc.n || ok != tc.ok {
			t.Errorf("%q = %d,%v", tc.text, n, ok)
		}
	}
	const q = "Select up to 2 languages you use"
	e := dMulti(q, []string{"Go", "Python", "Rust", "Zig"})
	r := multiAnswer(t, e, checkboxGroup(q, "Zig", "Rust", "Python", "Go"))
	if !reflect.DeepEqual(r.ChosenLabels, []string{"Go", "Python"}) || !traceHas(r, "cap of 2") {
		t.Fatalf("exactly the first two selections: %+v", r)
	}
	txt := multiAnswer(t, e, textQ("s", q))
	if txt.Answer != "Go, Python" {
		t.Fatalf("the cap also trims a text answer: %+v", txt)
	}
}

func TestAnswerBank_MultiSelect_EmptyYesNoNoneOptionAndAllowedOptionsIntersection(t *testing.T) {
	const q = "Are you a current or former employee of an audit firm?"
	none := dMulti(q, []string{})
	if r := multiAnswer(t, none, yesNoQ("s", q)); r.Answer != "No" || r.ChosenLabel != "No" {
		t.Fatalf("empty selection on a Yes/No control: %+v", r)
	}
	const g = "Which audit firms have you worked at?"
	e := dMulti(g, []string{})
	if r := multiAnswer(t, e, checkboxGroup(g, "Firm A", "None of the above", "Firm B")); r.ChosenLabel != "None of the above" {
		t.Fatalf("an option that means none: %+v", r)
	}
	if r := multiAnswer(t, e, checkboxGroup(g, "Firm A", "N/A")); r.ChosenLabel != "N/A" {
		t.Fatalf("%+v", r)
	}
	for _, c := range []Question{checkboxGroup(g, "Firm A", "Firm B"), textQ("s", g)} {
		if r := multiAnswer(t, e, c); r.Outcome != OutcomeAbstained {
			t.Fatalf("no none option and a text box abstain, the literal N.A is never typed: %+v", r)
		}
	}
	t.Run("allowed options named in the question intersect the selections", func(t *testing.T) {
		const tq = "Which of Go or Rust do you use?"
		e := dMulti(tq, []string{"Python", "Go", "Rust"}, func(e *Entry) { e.AllowedOptions = []string{"Go", "Rust", "Python"} })
		if r := multiAnswer(t, e, textQ("s", tq)); r.Answer != "Go, Rust" {
			t.Fatalf("%+v", r)
		}
		const none = "Which of Zig or Nim do you use?"
		e2 := dMulti(none, []string{"Python"}, func(e *Entry) { e.AllowedOptions = []string{"Zig", "Nim", "Python"} })
		if r := multiAnswer(t, e2, textQ("s", none)); r.Outcome != OutcomeAbstained {
			t.Fatalf("an empty intersection abstains: %+v", r)
		}
		const all = "Which languages do you use?"
		e3 := dMulti(all, []string{"Python", "Go"}, func(e *Entry) { e.AllowedOptions = []string{"Go", "Python", "Rust"} })
		if r := multiAnswer(t, e3, textQ("s", all)); r.Answer != "Python, Go" {
			t.Fatalf("nothing named: every selection, in order: %+v", r)
		}
	})
	t.Run("an option ending in a symbol is a whole word", func(t *testing.T) {
		const cq = "Do you know C++ or Go?"
		e := dMulti(cq, []string{"C++", "Python"}, func(e *Entry) { e.AllowedOptions = []string{"C++", "Python"} })
		if r := multiAnswer(t, e, textQ("s", cq)); r.Answer != "C++" {
			t.Fatalf("%+v", r)
		}
	})
}
