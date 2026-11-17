package match

import "testing"

func TestAnswerBank_Experience_TotalYearsOnABucketMenuAndTextBox(t *testing.T) {
	const q = "How many years of professional experience do you have?"
	e := dFixed("years-experience-total", "0-1 years", q)
	m := pfMatcher(e)
	r := pfOne(t, m, alexFacts(), pfDate(2026, 10, 1), pfSelect("s", q, "0-1", "2-3", "4+"))
	if r.ChosenLabel != "0-1" || r.Outcome == OutcomeAbstained {
		t.Fatalf("the stored range lands on its bucket: %+v", r)
	}
	r = pfOne(t, m, alexFacts(), pfDate(2026, 10, 1), textQ("s", q))
	if r.Answer != "0" {
		t.Fatalf("a text box takes the lower bound: %+v", r)
	}
}

func TestAnswerBank_TextAnswer_StateCodeAndListTrimThroughFixedEntry(t *testing.T) {
	const sq = "State of residence"
	state := dFixed("address-state", "South Carolina (SC)", sq)
	if r := pfOne(t, pfMatcher(state), alexFacts(), Date{}, textQ("s", sq)); r.Answer != "SC" {
		t.Fatalf("state code: %+v", r)
	}
	const lq = "Please list your top two programming languages"
	langs := dFixed("programming-languages", "Go, Python, Rust, Java", lq)
	if r := pfOne(t, pfMatcher(langs), alexFacts(), Date{}, textQ("s", lq)); r.Answer != "Go, Python" {
		t.Fatalf("list trim: %+v", r)
	}
}
