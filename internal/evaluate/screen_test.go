// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"testing"

	"applysmith/internal/gates"
)

func TestPrechecks(t *testing.T) {
	base := PrecheckInput{Stance: gates.StanceSilent, Need: NeedNeeded, Title: "Software Engineer"}
	for _, c := range []struct {
		name string
		mod  func(*PrecheckInput)
		want string
	}{
		{"refusal for a user who needs sponsorship", func(in *PrecheckInput) { in.Stance = gates.StanceRefuses }, BlockNoSponsorship},
		{"restricted for a user who needs sponsorship", func(in *PrecheckInput) { in.Stance = gates.StanceRestricted }, BlockRestrictedAuth},
		{"refusal for a citizen", func(in *PrecheckInput) { in.Stance, in.Need = gates.StanceRefuses, NeedNotNeeded }, ""},
		{"restricted with sponsorship unanswered", func(in *PrecheckInput) { in.Stance, in.Need = gates.StanceRestricted, NeedUnanswered }, ""},
		{"senior title for a mid profile", func(in *PrecheckInput) { in.Title, in.LimitsSeniority = "Senior Engineer", true }, BlockSeniority},
		{"senior title with no seniority target", func(in *PrecheckInput) { in.Title = "Senior Engineer" }, ""},
		{"early-career signal wins", func(in *PrecheckInput) { in.Title, in.LimitsSeniority = "Senior Intern Engineer", true }, ""},
		{"salary below the minimum", func(in *PrecheckInput) { in.Compensation, in.Minimum = "$70,000 - $80,000", 100000 }, BlockSalary},
		{"salary at the minimum", func(in *PrecheckInput) { in.Compensation, in.Minimum = "$90,000 - $100,000", 100000 }, ""},
		{"salary unstated", func(in *PrecheckInput) { in.Minimum = 100000 }, ""},
		{"placeholder salary", func(in *PrecheckInput) { in.Compensation, in.Minimum = "Salary Range: $1\u2014$2 USD", 100000 }, ""},
		{"salary in another currency", func(in *PrecheckInput) { in.Compensation, in.Minimum = "£50,000 - £60,000", 100000 }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mod(&in)
			got := Prechecks(in)
			if c.want == "" && len(got) != 0 || c.want != "" && (len(got) != 1 || got[0] != c.want) {
				t.Fatalf("Prechecks = %v, want %q", got, c.want)
			}
		})
	}
}

func TestPrecheckScore(t *testing.T) {
	for _, c := range []struct {
		blockers []string
		want     float64
	}{
		{[]string{BlockNoSponsorship, BlockSeniority}, 0.2},
		{[]string{BlockSalary}, 0.9},
		{[]string{BlockClearance}, 0.2},
		{[]string{BlockYears}, 0.9},
		{[]string{BlockNoSponsorship}, 0.2},
		{nil, 2.4},
	} {
		if got := PrecheckScore(c.blockers); !near(got, c.want) {
			t.Errorf("PrecheckScore(%v) = %v, want %v", c.blockers, got, c.want)
		}
	}
}

func TestDecide(t *testing.T) {
	for _, c := range []struct {
		name     string
		score    float64
		proposed string
		reason   string
		blockers []string
		want     string
	}{
		{"boundary 4.2 advances", 4.2, DecisionAdvance, "", nil, DecisionAdvance},
		{"3.4 with proposal advance drops", 3.4, DecisionAdvance, "", nil, DecisionDrop},
		{"3.5 advances", 3.5, DecisionAdvance, "", nil, DecisionAdvance},
		{"review keeps a high-fit posting", 3.9, DecisionReview, "legitimacy_unclear", nil, DecisionReview},
		{"unknown reason code advances", 3.9, DecisionReview, "looks_odd", nil, DecisionAdvance},
		{"review with no code advances", 3.9, DecisionReview, "", nil, DecisionAdvance},
		{"low score with an allowed review reason", 3.0, DecisionReview, "missing_jd_evidence", nil, DecisionReview},
		{"low score with an unknown reason", 3.0, DecisionReview, "looks_odd", nil, DecisionDrop},
		{"blocker beats everything", 4.9, DecisionAdvance, "", []string{BlockClearance}, DecisionDrop},
		{"blocker beats review", 3.9, DecisionReview, "seniority_unclear", []string{BlockSalary}, DecisionDrop},
		{"high score ignores a review proposal", 4.5, DecisionReview, "seniority_unclear", nil, DecisionAdvance},
		{"proposed drop in the middle band advances", 3.9, DecisionDrop, "", nil, DecisionAdvance},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.score, c.proposed, c.reason, c.blockers); got != c.want {
				t.Fatalf("Decide = %s, want %s", got, c.want)
			}
		})
	}
}

func TestSplitBlockers(t *testing.T) {
	hard, concerns := SplitBlockers([]string{"seems_junior_focused", BlockClearance, BlockClearance, "unknown_sponsorship", BlockNoSponsorship}, NeedNotNeeded)
	if len(hard) != 1 || hard[0] != BlockClearance {
		t.Fatalf("hard = %v", hard)
	}
	if len(concerns) != 3 || concerns[0] != "seems_junior_focused" {
		t.Fatalf("concerns = %v", concerns)
	}
	hard, _ = SplitBlockers([]string{BlockNoSponsorship, BlockRestrictedAuth}, NeedNeeded)
	if len(hard) != 2 {
		t.Fatalf("sponsorship blockers count when sponsorship is needed: %v", hard)
	}
	hard, _ = SplitBlockers([]string{BlockNoSponsorship}, NeedUnanswered)
	if len(hard) != 0 {
		t.Fatalf("unanswered is not a blocker: %v", hard)
	}
}
