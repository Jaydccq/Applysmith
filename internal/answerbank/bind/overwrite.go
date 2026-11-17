package bind

import "applysmith/internal/answerbank/match"

// ClientMayOverwrite reports whether the profile pass may still fill a control
// the matcher left. It is a pure function of the outcome: a fill is final, a
// deliberate blank (a policy abstain, the ask-me marker, a deferred narrative,
// a model-lane refusal) stays blank, and only the three abstains that mean the
// matcher found no usable entry leave the control to the profile pass.
func ClientMayOverwrite(r match.Result) bool {
	if r.Outcome != match.OutcomeAbstained || r.Terminal {
		return false
	}
	switch r.Reason {
	case match.ReasonNoEntry, match.ReasonLowConfidence, match.ReasonAmbiguous:
		return true
	}
	return false
}
