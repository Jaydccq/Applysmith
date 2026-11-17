// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"strings"

	"applysmith/internal/gates"
)

// Cap names. A demotion is listed with value 5.0, which has no effect.
const (
	CapThin           = "thin_posting"
	CapWorkAuth       = "work_authorization_gate"
	CapHonoredGate    = "eligibility_gate"
	CapTwoMisses      = "two_or_more_must_have_misses"
	CapOneMiss        = "one_must_have_miss"
	CapGateDemoted    = "gate_class_demoted"
	CapUnsupportedHit = "unsupported-hit"
)

// Cap is one rule that applied, with its value and reason.
type Cap struct {
	Rule   string  `json:"rule"`
	Value  float64 `json:"value"`
	Reason string  `json:"reason"`
}

// ScoreInput is everything the arithmetic reads. It holds no clock, no I/O and
// no randomness: the same input always gives the same output.
type ScoreInput struct {
	Must     []float64 `json:"must"`
	Nice     []float64 `json:"nice"`
	Hidden   []float64 `json:"hidden"`
	Align    []float64 `json:"alignment"`
	RedFlags []float64 `json:"red_flags"`
	Comp     float64   `json:"comp"`
	// MustMisses counts must-haves with hit level 0, demoted gates included.
	MustMisses int `json:"must_misses"`
	// HonoredGate is a must-have miss with a triggered gate class.
	HonoredGate bool `json:"honored_gate"`
	// WorkAuthGate is the deterministic sponsorship stance blocking the user.
	WorkAuthGate bool `json:"work_auth_gate"`
	// TierDelta is the employer-tier penalty, subtracted once after the caps.
	TierDelta float64 `json:"tier_delta"`
	// Notes are caps already recorded (demotions), listed first.
	Notes []Cap `json:"notes"`
}

// Scorecard is the computed result, serialized into the report.
type Scorecard struct {
	Must      float64 `json:"must_score"`
	Nice      float64 `json:"nice_score"`
	Match     float64 `json:"match"`
	Alignment float64 `json:"alignment"`
	Hidden    float64 `json:"hidden"`
	Comp      float64 `json:"comp"`
	Penalty   float64 `json:"penalty"`
	Raw       float64 `json:"raw"`
	Caps      []Cap   `json:"caps"`
	Midpoint  float64 `json:"midpoint"`
	TierDelta float64 `json:"tier_delta"`
	Low       float64 `json:"interval_low"`
	High      float64 `json:"interval_high"`
	Thin      bool    `json:"thin"`
	// Score is the tracker score: the final midpoint to one decimal.
	Score float64 `json:"score"`
}

// HitScore is 1 + 4 x the mean hit level; an empty list is 3.0.
func HitScore(levels []float64) float64 {
	if len(levels) == 0 {
		return 3.0
	}
	var sum float64
	for _, l := range levels {
		sum += l
	}
	return round2(1 + 4*sum/float64(len(levels)))
}

// Compute applies the arithmetic, the caps, the tier penalty and the interval.
func Compute(in ScoreInput) Scorecard {
	sc := Scorecard{Thin: len(in.Must) < 3, TierDelta: in.TierDelta, Comp: in.Comp}
	sc.Must = HitScore(in.Must)
	sc.Match = sc.Must
	if len(in.Nice) > 0 {
		sc.Nice = HitScore(in.Nice)
		sc.Match = round2(0.75*sc.Must + 0.25*sc.Nice)
	}
	sc.Alignment = HitScore(in.Align)
	if in.WorkAuthGate {
		sc.Alignment = min(sc.Alignment, 1.5)
	}
	sc.Hidden = HitScore(in.Hidden)
	var red float64
	for _, l := range in.RedFlags {
		red += l
	}
	sc.Penalty = round2(min(1.5, 0.5*red))
	sc.Raw = round2(0.45*sc.Match + 0.25*sc.Alignment + 0.15*sc.Hidden + 0.15*in.Comp - sc.Penalty)

	sc.Caps = append([]Cap(nil), in.Notes...)
	limit := 5.0
	add := func(c Cap) {
		sc.Caps = append(sc.Caps, c)
		limit = min(limit, c.Value)
	}
	if sc.Thin {
		add(Cap{CapThin, 3.7, "fewer than 3 must-haves"})
	}
	if in.WorkAuthGate {
		add(Cap{CapWorkAuth, 2.5, "the posting's sponsorship stance blocks the profile"})
	}
	switch {
	case in.HonoredGate:
		add(Cap{CapHonoredGate, 1.5, "a must-have eligibility requirement is missed"})
	case in.MustMisses >= 2:
		add(Cap{CapTwoMisses, 2.7, "two or more must-haves missed"})
	case in.MustMisses == 1:
		add(Cap{CapOneMiss, 3.7, "one must-have missed"})
	}
	mid := clamp(min(sc.Raw, limit), 1, 5)
	if in.TierDelta > 0 {
		mid = clamp(round2(mid-in.TierDelta), 1, 5)
	}
	sc.Midpoint = mid
	width := 0.25
	if sc.Thin {
		width = 0.5
	}
	sc.Low, sc.High = clamp(round2(mid-width), 1, 5), clamp(round2(mid+width), 1, 5)
	if in.HonoredGate && limit <= 1.5 {
		sc.Low, sc.High = 1.2, 1.8
	}
	sc.Score = round1(mid)
	return sc
}

// CompensationScore prices the posting's upper bound against the profile's
// minimum and target. A missing range, a missing minimum or a different
// currency scores a neutral 3.
func CompensationScore(r *gates.Range, p CompTarget) float64 {
	currency := p.Currency
	if currency == "" {
		currency = "USD"
	}
	if r == nil || p.Minimum <= 0 || !strings.EqualFold(r.Currency, currency) {
		return 3
	}
	lowest, target := p.Minimum, p.Target
	if target <= 0 {
		target = lowest * 11 / 10
	}
	switch hi := r.High; {
	case hi >= target:
		return 5
	case hi >= lowest:
		return 4
	case hi >= 0.9*lowest:
		return 3
	case hi >= 0.75*lowest:
		return 2
	}
	return 1
}

// CompTarget is the profile's compensation target, annualized.
type CompTarget struct {
	Currency string
	Minimum  float64
	Target   float64
}
