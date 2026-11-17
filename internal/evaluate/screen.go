// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"cmp"
	"slices"
	"strings"

	"applysmith/internal/gates"
)

// The closed set of hard-blocker codes. A model blocker outside it is a
// concern, never a blocker.
const (
	BlockNoSponsorship  = "no_sponsorship"
	BlockRestrictedAuth = "restricted_work_authorization"
	BlockClearance      = "clearance_required"
	BlockSeniority      = "seniority_too_high"
	BlockSalary         = "salary_below_minimum"
	BlockYears          = "years_above_limit"
)

var hardBlockers = []string{BlockNoSponsorship, BlockRestrictedAuth, BlockClearance, BlockSeniority, BlockSalary, BlockYears}

// blockerWords say the hard blockers in the tracker's notes, which people read.
var blockerWords = map[string]string{
	BlockNoSponsorship:  "no visa sponsorship",
	BlockRestrictedAuth: "restricted work authorization",
	BlockClearance:      "security clearance required",
	BlockSeniority:      "seniority too high",
	BlockSalary:         "salary below your minimum",
	BlockYears:          "more years of experience than your limit",
}

// BlockerNote is the tracker note of a screening that found hard blockers, or
// empty when it found none.
func BlockerNote(blockers []string) string {
	if len(blockers) == 0 {
		return ""
	}
	words := make([]string, len(blockers))
	for i, b := range blockers {
		words[i] = cmp.Or(blockerWords[b], strings.ReplaceAll(b, "_", " "))
	}
	return "Screening: " + strings.Join(words, ", ")
}

// reviewReasons is the closed set of codes that keep a posting for the user.
var reviewReasons = []string{"legitimacy_unclear", "duplicate_aggregator_listing", "seniority_unclear", "missing_jd_evidence"}

var reviewWords = map[string]string{
	"legitimacy_unclear": "The quick screen could not tell whether the posting is genuine.", "duplicate_aggregator_listing": "It may be a job board's copy of a posting you have.",
	"seniority_unclear": "The quick screen could not tell the seniority.", "missing_jd_evidence": "The posting says too little to judge.",
}

// ReviewNote says in words why a quick screen kept a posting for the user; ""
// for a code outside reviewReasons.
func ReviewNote(code string) string { return reviewWords[code] }

// Screening decisions.
const (
	DecisionAdvance = "advance"
	DecisionDrop    = "drop"
	DecisionReview  = "review"
)

// PrecheckInput is what the local prechecks read.
type PrecheckInput struct {
	Stance gates.Stance
	Need   Need
	Title  string
	Label  string
	// LimitsSeniority is true when the profile targets roles below senior.
	LimitsSeniority bool
	Compensation    string
	Text            string
	// Minimum is the profile's annual minimum in USD; zero when unset or not
	// in a comparable currency.
	Minimum float64
}

// Prechecks returns the hard blockers the gate does not cover. A hit ends the
// screen without a model call.
func Prechecks(in PrecheckInput) []string {
	var out []string
	if in.Need == NeedNeeded {
		switch in.Stance {
		case gates.StanceRefuses:
			out = append(out, BlockNoSponsorship)
		case gates.StanceRestricted:
			out = append(out, BlockRestrictedAuth)
		}
	}
	if in.LimitsSeniority && gates.ClearlySenior(in.Title, in.Label) {
		out = append(out, BlockSeniority)
	}
	if in.Minimum > 0 {
		if r, ok := gates.ParseCompensation(in.Compensation + " " + in.Text); ok && r.High < in.Minimum {
			out = append(out, BlockSalary)
		}
	}
	return out
}

var blockerPenalty = map[string]float64{
	BlockNoSponsorship: 2.2, BlockRestrictedAuth: 2.2, BlockClearance: 2.2,
	BlockSalary: 1.5, BlockYears: 1.5, BlockSeniority: 1.5,
}

// PrecheckScore is the deterministic screening score of a local skip: 2.4
// less the blocker penalties, clamped to [0.2, 4.8].
func PrecheckScore(blockers []string) float64 {
	s := 2.4
	for _, b := range blockers {
		s -= blockerPenalty[b]
	}
	return round1(clamp(s, 0.2, 4.8))
}

// SplitBlockers separates the model's blocker list into hard blockers and
// concerns. The two sponsorship blockers are deterministic: they are honored
// only when the profile needs sponsorship, otherwise they are concerns.
func SplitBlockers(model []string, need Need) (hard, concerns []string) {
	for _, b := range model {
		b = strings.TrimSpace(b)
		switch {
		case !slices.Contains(hardBlockers, b):
			if b != "" {
				concerns = append(concerns, b)
			}
		case (b == BlockNoSponsorship || b == BlockRestrictedAuth) && need != NeedNeeded:
			concerns = append(concerns, b)
		case !slices.Contains(hard, b):
			hard = append(hard, b)
		}
	}
	return hard, concerns
}

// Decide is the deterministic decision table; the first match wins.
func Decide(score float64, proposed, reason string, blockers []string) string {
	allowedReview := proposed == DecisionReview && slices.Contains(reviewReasons, reason)
	switch {
	case len(blockers) > 0:
		return DecisionDrop
	case score < SkipBelow:
		if allowedReview {
			return DecisionReview
		}
		return DecisionDrop
	case score >= ApplyFrom:
		return DecisionAdvance
	case allowedReview:
		return DecisionReview
	}
	return DecisionAdvance
}
