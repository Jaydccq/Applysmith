package match

import (
	"math"
	"slices"
	"strings"
)

// scoredOption is one option with its score against one answer.
type scoredOption struct {
	opt   Option
	index int
	optScore
}

// optionValue is the identity of an option for the "different value" tests: its
// value, else its label, trimmed and lower-cased. Options with an empty value
// compare by label, so two real choices are never one value.
func optionValue(o Option) string {
	v := o.Value
	if strings.TrimSpace(v) == "" {
		v = o.Label
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// scoreAll scores every option against one answer with the control's scorer.
func scoreAll(sc *optionScorer, opts []Option, answer string) []scoredOption {
	out := make([]scoredOption, len(opts))
	for i, o := range opts {
		out[i] = scoredOption{o, i, sc.score(o, answer)}
	}
	return out
}

// sortScored orders by score, highest first, page order breaking ties.
func sortScored(list []scoredOption) {
	slices.SortStableFunc(list, func(a, b scoredOption) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return a.index - b.index
	})
}

// scoreEpsilon absorbs the rounding of a difference of two scores: 1.0-0.9 is
// 0.09999999999999998, and a lead of exactly 0.10 is a lead of 0.10.
const scoreEpsilon = 1e-9

// leadsBy reports a lead that reaches the margin.
func leadsBy(lead, margin float64) bool { return lead >= margin-scoreEpsilon }

// clearWinnerMargin is the lead of the top option over the next option with a
// DIFFERENT value, so two "Yes" labels are not competitors. It is infinite when
// no distinct competitor exists and 0 for an empty list. The list must be
// sorted.
func clearWinnerMargin(sorted []scoredOption) float64 {
	if len(sorted) == 0 {
		return 0
	}
	best := optionValue(sorted[0].opt)
	for _, s := range sorted[1:] {
		if optionValue(s.opt) != best {
			return sorted[0].Score - s.Score
		}
	}
	return math.Inf(1)
}

// otherValueScore is the best score among options whose value differs from the
// top option's.
func otherValueScore(sorted []scoredOption) float64 {
	if len(sorted) == 0 {
		return 0
	}
	best := optionValue(sorted[0].opt)
	for _, s := range sorted[1:] {
		if optionValue(s.opt) != best {
			return s.Score
		}
	}
	return 0
}

// isClearWinner reports the margin-safe best option of a sorted list: it clears
// the value floor and leads the next distinct value by the margin.
func isClearWinner(sorted []scoredOption) bool {
	return len(sorted) > 0 && sorted[0].Score >= ClearWinnerFloor && leadsBy(clearWinnerMargin(sorted), ClearWinnerLead)
}

// raceEthnicityIDs are the two documented-confusable demographic entries.
var raceEthnicityIDs = setOf("eeo-race", "eeo-ethnicity")
