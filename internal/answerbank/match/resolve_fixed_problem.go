package match

import (
	"fmt"

	"applysmith/internal/lazyregexp"
)

// An arrangement question may ask whether the arrangement is a problem rather
// than whether it is accepted (Shield AI, Melbourne: "We are based in Port
// Melbourne, VIC and work on a hybrid model. Will this present any challenges
// for you?"). An accepted arrangement presents none, so the acceptance
// entry's Yes answers No there, and its No answers Yes.

const problemNoun = `(?:challenges?|problems?|issues?|difficult(?:y|ies)|obstacles?|concerns?|conflicts?|hardships?|barriers?)\b`

// problemAskRE asks whether something is a problem: "Will this present any
// challenges", "Would this be an issue", "Is this a problem", "Do you foresee
// any issues", "Is there any reason you would not be able to".
var problemAskRE = lazyregexp.New(`(?i)\b(?:be|present|pose|cause|create|raise)s?\s+(?:any|an?|some)\s+` + problemNoun +
	`|\b(?:is|are)\s+(?:this|that|it|these)\s+(?:an?|any)\s+` + problemNoun +
	`|\b(?:have|foresee|anticipate|expect|see|face)\s+(?:any|an?)\s+` + problemNoun +
	`|\b(?:is|are)\s+there\s+(?:anything|any\s+reasons?)\b.*\b(?:prevent|stop|keep|hinder|preclude|not|unable|cannot|can't)\b`)

// problemAsk reports a question whose ask, the sentence that ends at its first
// question mark (else its last), asks whether something is a problem. A
// problem named only before the ask ("Hybrid work can be a challenge for
// some. Are you comfortable ...?") is no such question.
func problemAsk(text string) bool {
	ask := sentencesOf(askedSentence(text))
	return len(ask) > 0 && problemAskRE.MatchString(ask[len(ask)-1])
}

// fixedEntry resolves a fixed entry (fixedAnswer); an arrangement question
// that asks for a problem takes the opposite of the acceptance.
func fixedEntry(s *Step) (Draft, bool) {
	d, ok := fixedAnswer(s)
	if !ok {
		return d, false
	}
	return oppositeOnAProblem(s, d), true
}

// oppositeOnAProblem answers a Yes/No about a problem with the arrangement
// with the opposite of the acceptance the draft holds. Options other than a
// Yes and a No keep the draft.
func oppositeOnAProblem(s *Step, d Draft) Draft {
	e := d.Entry
	if e == nil {
		e = s.Entry
	}
	if d.Abstain != nil || e == nil || !arrangementAcceptances[e.ID] || !problemAsk(s.Q.Text) {
		return d
	}
	if hasRenderedOptions(s.Q.Options) && !isYesNoOptionSet(s.Q.Options) {
		return d
	}
	accepted := firstNonEmpty(d.ChosenLabel, d.Answer)
	opposite := "Yes"
	switch {
	case isYesLabel(accepted):
		opposite = "No"
	case !isNoLabel(accepted):
		return d
	}
	out := bindEntryAnswer(s, e, opposite, fmt.Sprintf("the question asks whether the arrangement is a problem: %s's %q answers %q", e.ID, accepted, opposite))
	out.Trace = append(append([]string(nil), d.Trace...), out.Trace...)
	return out
}
