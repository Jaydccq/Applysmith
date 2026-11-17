package match

import "testing"

// These pin the three matcher faults the default bank's self-example invariant
// (internal/answerbank) found: an entry has to be reachable from its own example.

func TestAnswerBank_NarrativeGate_DerivedEntryOwnsItsExample(t *testing.T) {
	// A derived entry has a blank body by design, so it is finished: its own
	// example is a curated hit that the narrative guard and the pattern routes
	// leave alone.
	const q = "Please describe your work authorization"
	detail := ent(IDWorkAuthorizationDetail, "fixed", emptyBody(), withExamples(q), withTypes(InputText))
	expiry := ent("work-authorization-expiry", "fixed", withAnswer("2027-01-15"), withExamples("Work authorization expiry"))
	m := pickMatcher(detail, expiry)
	with := func(r *Request) {
		r.Today = Date{Year: 2026, Month: 10, Day: 11}
		r.Facts.VisaStatus = "F-1 OPT"
	}
	r := matchReq(m, Question{Selector: "t", Text: q, Kind: KindText, InputType: InputText}, with)
	if r.Outcome != OutcomeFilled || r.EntryID != IDWorkAuthorizationDetail || r.Answer == "" {
		t.Fatalf("the derived entry answers its own example: %+v", r)
	}
	// A narrative prompt that is not the entry's example is still left to the composer.
	wantAbstain(t, matchReq(m, textQ("n", "Tell us about yourself"), with), ReasonNarrativeDeferred, "")
}
