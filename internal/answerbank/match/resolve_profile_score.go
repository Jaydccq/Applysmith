package match

import "strings"

// The profile-grounded resolvers bind an answer they computed to the options
// the control renders through the one option scorer (resolve_option_score.go).

// Answers that read as a bare Yes or No.
var (
	yesAnswers = setOf("yes", "y", "true", "yeah", "yep")
	noAnswers  = setOf("no", "n", "false", "nope")
)

// optionScore scores one rendered label against one answer, 0..1.
func optionScore(answer, label string) float64 {
	return scoreOption(Option{Label: label}, answer, "").Score
}

// optionBinding is the option an answer binds to.
type optionBinding struct {
	Label string
	Value string
	Index int
	// Score is the best score; Other the best score among options with a
	// different label.
	Score, Other float64
	// Tie marks options with different labels sharing the best score.
	Tie bool
}

// bindOption scores every rendered option against the answer. Placeholders and
// withdrawals score zero. It returns false when no option scores above zero.
func bindOption(sc *optionScorer, opts []Option, answer string) (optionBinding, bool) {
	best := optionBinding{Index: -1}
	scores := make([]float64, len(opts))
	for i, o := range opts {
		scores[i] = sc.score(o, answer).Score
		if scores[i] > best.Score {
			best.Score, best.Index, best.Label, best.Value = scores[i], i, o.Label, o.Value
		}
	}
	if best.Index < 0 {
		return optionBinding{}, false
	}
	bl := normalizeLabel(best.Label)
	for i, o := range opts {
		if normalizeLabel(o.Label) == bl {
			continue
		}
		if scores[i] > best.Other {
			best.Other = scores[i]
		}
		if scores[i] == best.Score {
			best.Tie = true
		}
	}
	return best, true
}

// optionDraft is the draft of a bound option; a fixed or single-valued source
// passes deterministic. It answers with the bound option's value beside its
// label; the derived lanes built on derivedDraft answer with the label. Every
// caller builds an option whose value is its label, and the page is driven
// from ChosenLabel.
func optionDraft(b optionBinding, e *Entry, source Source, deterministic bool) Draft {
	return Draft{Entry: e, Answer: b.Value, ChosenLabel: b.Label, Source: source,
		HasOption: true, OptionScore: b.Score, OtherValueScore: b.Other, Deterministic: deterministic}
}

// ownYesNo returns the control's own Yes or No option: the first real option
// whose label is exactly yes/true/y (or no/false/n), else the first that opens
// with the word.
func ownYesNo(opts []Option, yes bool) (Option, bool) {
	exact, lead := noAnswers, "no"
	if yes {
		exact, lead = yesAnswers, "yes"
	}
	real := realOptions(opts)
	for _, o := range real {
		if exact[normalizeLabel(o.Label)] {
			return o, true
		}
	}
	for _, o := range real {
		if l := normalizeLabel(o.Label); l == lead || strings.HasPrefix(l, lead+" ") || strings.HasPrefix(l, lead+",") {
			return o, true
		}
	}
	return Option{}, false
}
