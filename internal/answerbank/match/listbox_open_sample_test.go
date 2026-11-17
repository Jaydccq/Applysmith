package match

import "testing"

func TestMatcher_CustomListboxIsNotAClosedSet(t *testing.T) {
	const q = "What is your current school?"
	e := fixedEnt("school", q, "Example State University")
	sample := []string{"Alpha College", "Beta Institute", "Gamma School"}
	closed := matchReq(pickMatcher(e), Question{Selector: "s", Text: q, Kind: KindSelect, Options: opts(sample...)})
	if closed.Outcome != OutcomeAbstained || closed.Reason != ReasonLowConfidence {
		t.Fatalf("a native select is the whole list, so an answer outside it is refused: %+v", closed)
	}
	open := matchReq(pickMatcher(e), Question{Selector: "s", Text: q, Kind: KindCustomListbox, Options: opts(sample...)})
	if open.Outcome == OutcomeAbstained || open.Answer != "Example State University" || open.ChosenLabel != "Example State University" {
		t.Fatalf("a custom listbox shows a sample: the answer goes to the engine's search: %+v", open)
	}
	// An option the sample does hold is still bound to that option.
	held := matchReq(pickMatcher(fixedEnt("school", q, "Beta Institute")), Question{Selector: "s", Text: q, Kind: KindCustomListbox, Options: opts(sample...)})
	if held.Outcome == OutcomeAbstained || held.ChosenLabel != "Beta Institute" {
		t.Fatalf("held option: %+v", held)
	}
}
