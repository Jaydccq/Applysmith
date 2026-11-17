package bind

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

// FBA-075: the overwrite flag is a pure function of the outcome and reason.
func TestAnswerBank_Fallback_ClientMayOverwriteByOutcome(t *testing.T) {
	abstain := func(r match.Reason, terminal bool) match.Result {
		return match.Result{Outcome: match.OutcomeAbstained, Reason: r, Terminal: terminal}
	}
	cases := []struct {
		name string
		r    match.Result
		want bool
	}{
		{"filled", match.Result{Outcome: match.OutcomeFilled}, false},
		{"filled for review", match.Result{Outcome: match.OutcomeFilledReview}, false},
		{"filled by a model", match.Result{Outcome: match.OutcomeFilledModel}, false},
		{"no entry", abstain(match.ReasonNoEntry, false), true},
		{"low confidence", abstain(match.ReasonLowConfidence, false), true},
		{"ambiguous", abstain(match.ReasonAmbiguous, false), true},
		{"narrative deferred", abstain(match.ReasonNarrativeDeferred, false), false},
		{"input mismatch", abstain(match.ReasonInputMismatch, false), false},
		{"placeholder empty", abstain(match.ReasonPlaceholderEmpty, false), false},
		{"section policy", abstain(match.ReasonSectionPolicy, true), false},
		{"context policy", abstain(match.ReasonContextPolicy, true), false},
		{"duplicate link", abstain(match.ReasonDuplicateLink, true), false},
		{"self-id policy", abstain(match.ReasonSelfIDPolicy, true), false},
		{"ask me", abstain(match.ReasonAskMe, true), false},
		{"upstream error", abstain(match.ReasonUpstreamError, false), false},
		{"sensitive category", abstain(match.ReasonSensitiveCategory, false), false},
		{"a terminal flag overrides the reason", abstain(match.ReasonNoEntry, true), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClientMayOverwrite(c.r); got != c.want {
				t.Fatalf("ClientMayOverwrite = %v, want %v", got, c.want)
			}
		})
	}
}
