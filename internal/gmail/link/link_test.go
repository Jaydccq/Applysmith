package link

import (
	"math"
	"slices"
	"testing"

	"applysmith/internal/gmail/classify"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLink_ScoreWeights(t *testing.T) {
	cand := Candidacy{ID: 1, Company: "Example Corp (Examplo)", Role: "Senior Backend Engineer, Payments"}
	for _, c := range []struct {
		name string
		sig  Signal
		want float64
	}{
		{"company only, no role", Signal{Company: "Example Corp", Role: classify.UnknownRole}, 0.5},
		{"alias honored", Signal{Company: "Examplo Inc.", Role: classify.UnknownRole}, 0.5},
		{"company and the whole signal role covered", Signal{Company: "Example Corp", Role: "Backend Engineer"}, 0.9},
		{"role conflict subtracts", Signal{Company: "Example Corp", Role: "Data Scientist"}, 0.2},
		{"another company", Signal{Company: "Other Co", Role: "Backend Engineer"}, 0.4},
		{"unknown company is not a match", Signal{Company: classify.UnknownCompany, Role: classify.UnknownRole}, 0},
	} {
		if got := Score(c.sig, cand); !near(got, c.want) {
			t.Errorf("%s: score %.3f, want %.3f", c.name, got, c.want)
		}
	}
}

func TestLink_DecisionGates(t *testing.T) {
	a := Candidacy{ID: 1, Company: "Example Corp", Role: "Backend Engineer"}
	b := Candidacy{ID: 2, Company: "Other Co", Role: "Backend Engineer"}
	for _, c := range []struct {
		name  string
		sig   Signal
		cands []Candidacy
		want  string
		id    int64
		why   string
	}{
		{"unique company and role", Signal{Company: "Example Corp", Role: "Backend Engineer"}, []Candidacy{a, b}, Auto, 1, ReasonClearMatch},
		{"no candidacy at all", Signal{Company: "Example Corp"}, nil, Unlinked, 0, ReasonNoCandidate},
		{"below the floor", Signal{Company: "Nobody Ltd", Role: "Pilot"}, []Candidacy{a}, Unlinked, 0, ReasonNoCandidate},
		{"between the floor and auto", Signal{Company: "Nobody Ltd", Role: classify.UnknownRole, Subject: "Baker news"}, []Candidacy{{ID: 3, Company: "Third Co", Role: "Baker"}}, Review, 0, ReasonBelowAuto},
	} {
		d := Decide(c.sig, NewPool(c.cands), nil)
		if d.Kind != c.want || d.CandidacyID != c.id || !slices.Contains(d.Reasons, c.why) {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestLink_SameCompanyTwoRolesIsReviewAndRolelessSingleIsAuto(t *testing.T) {
	a := Candidacy{ID: 1, Company: "Example Corp", Role: "Backend Engineer"}
	b := Candidacy{ID: 2, Company: "Example Corp", Role: "Data Scientist"}
	d := Decide(Signal{Company: "Example Corp", Role: classify.UnknownRole}, NewPool([]Candidacy{a, b}), nil)
	if d.Kind != Review || d.CandidacyID != 0 || !slices.Contains(d.Reasons, ReasonAmbiguous) {
		t.Errorf("two roles: %+v", d)
	}
	d = Decide(Signal{Company: "Example Corp", Role: classify.UnknownRole}, NewPool([]Candidacy{a}), nil)
	if d.Kind != Auto || d.CandidacyID != 1 {
		t.Errorf("roleless, one candidacy: %+v", d)
	}
	// A stated role picks between the two.
	d = Decide(Signal{Company: "Example Corp", Role: "Data Scientist"}, NewPool([]Candidacy{a, b}), nil)
	if d.Kind != Auto || d.CandidacyID != 2 {
		t.Errorf("stated role: %+v", d)
	}
}

// The word a stored decision is shown with agrees with what Decide did at the
// thresholds: it linked a lone 0.5 on its own, held a 0.5 tie and a 0.2 for
// review, and left a no-match unlinked.
func TestLink_StrengthWordsFollowDecide(t *testing.T) {
	a := Candidacy{ID: 1, Company: "Example Corp", Role: "Backend Engineer"}
	b := Candidacy{ID: 2, Company: "Example Corp", Role: "Data Scientist"}
	roleless := Signal{Company: "Example Corp", Role: classify.UnknownRole}
	for _, c := range []struct {
		d    Decision
		want string
		near bool
	}{
		{Decide(roleless, NewPool([]Candidacy{a}), nil), "close", false},
		{Decide(roleless, NewPool([]Candidacy{a, b}), nil), "close", true},
		{Decide(Signal{Company: "Example Corp", Role: "Data Scientist"}, NewPool([]Candidacy{a}), nil), "weak", false},
		{Decide(Signal{Company: "Nobody Ltd", Role: "Pilot"}, NewPool([]Candidacy{a}), nil), "none", false},
	} {
		if Strength(c.d.Score) != c.want || NearlyAsClose(c.d.Score, c.d.RunnerUp) != c.near {
			t.Errorf("%+v: %s, nearly as close %v", c.d, Strength(c.d.Score), NearlyAsClose(c.d.Score, c.d.RunnerUp))
		}
	}
	if NearlyAsClose(AutoScore+AutoLead, AutoScore) {
		t.Error("a runner-up exactly AutoLead behind is not nearly as close: Decide links the best on its own")
	}
}

func TestLink_UnknownRoleNotScored(t *testing.T) {
	c := Candidacy{ID: 1, Company: "Example Corp", Role: "Backend Engineer"}
	if got := Score(Signal{Company: "Example Corp", Role: classify.UnknownRole}, c); !near(got, 0.5) {
		t.Errorf("placeholder role scored %.2f, want a company-only 0.50 with no mismatch penalty", got)
	}
}

func TestLink_DuplicateClusterIsReview(t *testing.T) {
	cands := []Candidacy{{ID: 1, Company: "Example Corp", Role: "Backend Engineer"}, {ID: 2, Company: "Example Inc", Role: "backend engineer"}}
	d := Decide(Signal{Company: "Example Corp", Role: "Backend Engineer"}, NewPool(cands), nil)
	if d.Kind != Review || !slices.Contains(d.Reasons, ReasonDuplicateCluster) {
		t.Errorf("%+v", d)
	}
}

func TestLink_RoleQualifierMismatch(t *testing.T) {
	c := Candidacy{ID: 1, Company: "Example Corp", Role: "Data Scientist"}
	// A subject and a sender that name the company lift the score over the
	// auto line even with the mismatch penalty; the conflict then stops it.
	d := Decide(Signal{Company: "Example Corp", Role: "Backend Engineer", Subject: "Example Corp update", Sender: "Example <hr@example.org>"}, NewPool([]Candidacy{c}), nil)
	if d.Kind != Review || !slices.Contains(d.Reasons, ReasonRoleQualifier) {
		t.Errorf("auto-scoring conflict: %+v", d)
	}
	// Without that lift the same conflict is still named.
	d = Decide(Signal{Company: "Example Corp", Role: "Backend Engineer"}, NewPool([]Candidacy{c}), nil)
	if d.Kind != Review || !slices.Contains(d.Reasons, ReasonRoleQualifier) {
		t.Errorf("plain conflict: %+v", d)
	}
	// Generic nouns do not conflict.
	d = Decide(Signal{Company: "Example Corp", Role: "Senior Software Engineer"}, NewPool([]Candidacy{{ID: 1, Company: "Example Corp", Role: "Staff Engineer"}}), nil)
	if d.Kind != Auto {
		t.Errorf("generic nouns only: %+v", d)
	}
	// Another kind of job that shares only a seniority word conflicts.
	d = Decide(Signal{Company: "Example Corp", Role: "Entry Level Backend Engineer"}, NewPool([]Candidacy{{ID: 1, Company: "Example Corp", Role: "Entry Level Analyst"}}), nil)
	if d.Kind != Review || !slices.Contains(d.Reasons, ReasonRoleQualifier) {
		t.Errorf("title nouns differ: %+v", d)
	}
}

func TestLink_ThreadContinuityAndMultiLinkedThread(t *testing.T) {
	cands := []Candidacy{{ID: 1, Company: "Example Corp", Role: "A"}, {ID: 2, Company: "Example Corp", Role: "B"}}
	d := Decide(Signal{Company: "Unrelated"}, NewPool(cands), []int64{2})
	if d.Kind != Auto || d.CandidacyID != 2 || d.Reasons[0] != ReasonThreadContinuity {
		t.Errorf("one link: %+v", d)
	}
	d = Decide(Signal{Company: "Example Corp"}, NewPool(cands), []int64{1, 2})
	if d.Kind != Review || d.CandidacyID != 0 || d.Reasons[0] != ReasonThreadMultiLinked {
		t.Errorf("two links: %+v", d)
	}
}

// The task's rule: a signal whose review reasons include signup_window_unknown
// or sender_unverified is decided review, never auto, whatever its score and
// whatever the thread already holds.
func TestLink_ReviewReasonsDecideReviewNeverAuto(t *testing.T) {
	cand := []Candidacy{{ID: 1, Company: "Example Corp", Role: "Backend Engineer"}}
	for _, reason := range []string{classify.ReviewSignupWindowUnknown, classify.ReviewSenderUnverified} {
		for _, thread := range [][]int64{nil, {1}} {
			d := Decide(Signal{Company: "Example Corp", Role: "Backend Engineer", ReviewReasons: []string{classify.ReviewLowConfidence, reason}}, NewPool(cand), thread)
			if d.Kind != Review || d.CandidacyID != 0 || !slices.Contains(d.Reasons, reason) {
				t.Errorf("%s with thread %v: %+v", reason, thread, d)
			}
		}
	}
	// Other review reasons do not force it.
	d := Decide(Signal{Company: "Example Corp", Role: "Backend Engineer", ReviewReasons: []string{classify.ReviewLowConfidence}}, NewPool(cand), nil)
	if d.Kind != Auto {
		t.Errorf("low confidence alone: %+v", d)
	}
}
