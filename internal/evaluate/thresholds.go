// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import "math"

// Score thresholds shared with the tracker and the document capabilities.
const (
	SkipBelow      = 3.5
	ApplyFrom      = 4.2
	StrongFrom     = 4.5
	DocumentsFloor = StrongFrom
)

// Recommendation names.
const (
	RecSkip     = "skip"
	RecConsider = "consider"
	RecApply    = "apply"
	RecStrong   = "strong"
)

func tenths(v float64) int { return int(math.Round(v * 10)) }

// Recommend maps a one-decimal score to a recommendation.
func Recommend(score float64) string {
	switch t := tenths(score); {
	case t < tenths(SkipBelow):
		return RecSkip
	case t < tenths(ApplyFrom):
		return RecConsider
	case t < tenths(StrongFrom):
		return RecApply
	}
	return RecStrong
}

// DocumentsEligible says whether a final-kind score clears the document floor.
func DocumentsEligible(score float64, final bool) bool {
	return final && tenths(score) >= tenths(DocumentsFloor)
}

// round2 rounds half up to two decimals, with an epsilon against float noise.
func round2(v float64) float64 { return math.Floor(v*100+0.5+1e-9) / 100 }

// round1 rounds half up to one decimal.
func round1(v float64) float64 { return math.Floor(v*10+0.5+1e-9) / 10 }

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }
