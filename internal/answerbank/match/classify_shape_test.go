package match

import "testing"

// Ashby draws "What are some AI specific technologies you are comfortable
// with?" as a short-text input: the user's one-paragraph written answer goes in
// when the box admits its length, never one longer than the box or one of
// several paragraphs.
func TestAnswerBank_Shape_AWrittenAnswerInASingleLineBoxThatAdmitsIt(t *testing.T) {
	const q = "What are some AI specific technologies you are comfortable with?"
	const answer = "PyTorch for training loops, retrieval-augmented generation, and agent workflows with Claude Code."
	box := Question{Selector: "a", Text: q, Kind: KindText, InputType: InputText}
	m := pickMatcher(fixedEnt("ai-technologies-comfort", q, answer, withTypes(InputLongText)))
	if r := matchReq(m, box); r.Outcome == OutcomeAbstained || r.Answer != answer {
		t.Fatalf("a box that admits it: %+v", r)
	}
	short := box
	short.MaxLength = 40
	wantAbstain(t, matchReq(m, short), ReasonInputMismatch, "")
	two := pickMatcher(fixedEnt("ai-technologies-comfort", q, answer+"\n\nAnd more.", withTypes(InputLongText)))
	wantAbstain(t, matchReq(two, box), ReasonInputMismatch, "")
}

// Black Canyon Consulting (Greenhouse, owner workspace): "Are you currently
// living and residing within the DC Metro Area? If not, are you willing to
// relocate at your own expense." in a text box took willing-to-relocate's bare
// "Yes", which reads as living there. A bare Yes or No never answers a text
// box whose second question hangs on the first answer; a plain Yes/No question
// still takes it.
func TestAnswerBank_Shape_ABareYesNeverAnswersAConditionalSecondQuestion(t *testing.T) {
	m := pickMatcher(fixedEnt("willing-to-relocate", "Are you willing to relocate?", "Yes", withTypes(InputYesNo)))
	box := func(text string) Question {
		return Question{Selector: "a", Text: text, Kind: KindText, InputType: InputText}
	}
	wantAbstain(t, matchReq(m, box("Are you currently living and residing within the DC Metro Area? If not, are you willing to relocate at your own expense.")), ReasonInputMismatch, "")
	if r := matchReq(m, box("Are you willing to relocate?")); r.Outcome == OutcomeAbstained || r.Answer != "Yes" {
		t.Fatalf("a plain Yes/No question in a text box: %+v", r)
	}
}

// Relativity Space (Greenhouse, owner workspace): "If your location differs
// from the location posted on the job description, are you willing to commute
// and/or relocate for this role? If not, please explain:" in a text box wants
// words only of a No. The policy's Yes answers it whole; a No is left for the
// explanation, never typed bare.
func TestAnswerBank_Shape_AYesAnswersABoxThatWantsWordsOnlyOfANo(t *testing.T) {
	const q = "If your location differs from the location posted on the job description, are you willing to commute and/or relocate for this role? If not, please explain:"
	box := Question{Selector: "a", Text: q, Kind: KindText, InputType: InputText}
	entry := func(pol *RelocationPolicy) Entry {
		e := relocEntry(pol)
		e.Answer, e.Placeholder = "Yes", false
		return e
	}
	r := matchReq(pickMatcher(entry(alexPolicy(RelocationOpen, nil, nil))), box, withPosting("Long Beach, CA"))
	if r.Outcome == OutcomeAbstained || r.Answer != "Yes" || r.EntryID != "willing-to-relocate" {
		t.Fatalf("open to relocation: %+v", r)
	}
	r = matchReq(pickMatcher(entry(alexPolicy(RelocationClosed, nil, []string{"Long Beach"}))), box, withPosting("Long Beach, CA"))
	if r.Outcome != OutcomeAbstained || r.Answer != "" || r.Reason != ReasonNarrativeDeferred {
		t.Fatalf("an excluded place: the No wants an explanation, %+v", r)
	}
}
