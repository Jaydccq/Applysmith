package match

import (
	"fmt"
	"strings"
)

// declineConfidence is the starting confidence of a decline chosen without a
// recorded answer: a review fill, never an automatic one, because the user did
// not choose to decline.
const declineConfidence = FuzzyConfidenceCap

// entryHoldsStance reports an entry that carries the user's answer: a pick-option
// entry with a preferred list (a fallback text alone does not count), a fixed
// entry with a non-blank answer, any other kind.
func entryHoldsStance(e *Entry) bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case KindPickOption:
		return len(e.Preferred) > 0
	case KindFixed:
		return strings.TrimSpace(e.Answer) != ""
	}
	return true
}

// trialResolve runs the resolver of the chosen entry's kind on a copy of the
// step, with the output guards and the confidence mapping, and reports whether
// the result is a fill. The question's own trace is untouched.
func (s *Step) trialResolve() bool {
	if s.kind == nil || s.Entry == nil {
		return false
	}
	t := *s
	t.trace = append([]string(nil), s.trace...)
	if v := acceptsControl(s.Entry, s.Q); !v.OK || s.Entry.BodyEmpty() && !s.Entry.derived() {
		return false
	}
	d, ok := s.kind(&t)
	if !ok {
		return false
	}
	if d.Abstain == nil {
		t.out = d
		if g, hit := outputGuards(&t); hit {
			d = g
		}
	}
	return t.finalize(d, s.Entry, s.Class.start).Outcome != OutcomeAbstained
}

// selfIDCheck is step 9: whatever entry was classified, an option list that
// names protected characteristics is answered only from a value the user
// recorded for that axis or by the page's own option that declines to answer. A
// recorded answer that binds is left to the entry's resolver; otherwise the set
// is declined, or ends in the terminal abstain. A fill is never altered: the
// policy stops invention, not the use of the user's own data.
func selfIDCheck(s *Step) (Draft, bool) {
	set := readSelfIDSetUsing(s.Q.Options, s.scorer())
	e := s.Entry
	if set == nil {
		return stanceDecline(s)
	}
	if e != nil && e.AsksMe() {
		return Draft{}, false
	}
	if e != nil && s.trialResolve() {
		return Draft{}, false
	}
	if d, ok := compoundRaceDraft(s); ok {
		return d, true
	}
	recorded := e != nil && strings.HasPrefix(e.ID, "eeo-") && entryHoldsStance(e)
	if i := set.declineIndex(); i >= 0 && !recorded {
		return declineDraft(s.Q.Options[i], "self-id option set unresolved from the recorded profile"), true
	}
	why := "self-id option set unresolved: terminal abstain (no lane may guess a protected characteristic)"
	if recorded {
		why = "self-id option set cannot express the recorded answer: terminal abstain (a recorded answer is never traded for a refusal)"
	}
	return Draft{Abstain: &Abstain{Reason: ReasonSelfIDPolicy}, Trace: []string{why}}, true
}

// stanceDecline is step 9 for a question whose options name no characteristic
// ("Do you identify as transgender?" with Yes, No and "Prefer not to say"): when
// the question names one, the control takes a single choice, and no answer the
// user recorded resolves it, the page's own decline option is chosen (owner
// decision F-10). A self-identification entry that holds the user's answer
// keeps the question even when the options cannot express it; a box is never
// ticked, and a control with no decline option is left as it was.
func stanceDecline(s *Step) (Draft, bool) {
	q, e := s.Q, s.Entry
	if q.Kind == KindCheckbox || selfIDAxisOfLabel(q.Text) == "" {
		return Draft{}, false
	}
	if e != nil && (e.Category == CategorySelfIdentification && entryHoldsStance(e) || s.trialResolve()) {
		return Draft{}, false
	}
	for _, o := range q.Options {
		if s.scorer().role(o.Label).kind == roleDecline {
			return declineDraft(o, "self-id question the recorded profile does not answer"), true
		}
	}
	return Draft{}, false
}

// declineDraft chooses the page's decline option at the review band.
func declineDraft(o Option, why string) Draft {
	return Draft{Entry: &Entry{}, Answer: o.Value, ChosenLabel: o.Label, Source: SourceSelfIDDecline, Start: declineConfidence,
		Trace: []string{fmt.Sprintf("%s: decline %q (asserts no characteristic)", why, o.Label)}}
}
