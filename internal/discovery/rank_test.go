package discovery

import (
	"sort"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func TestRank_ScoreArithmetic(t *testing.T) {
	r := rule([]string{"software", "engineer", "rust"}, nil)
	title := "Junior Software Engineer"
	p := Posting{Title: title, Location: "Austin, TX", PostedAt: epoch.Add(-48 * time.Hour), novelty: 1}
	tr := r.Check(title)
	early := NewRankTargets([]workspace.Role{{Title: "Data Analyst", Seniority: "junior"}})
	score, reasons := Rank(p, RankInput{Title: tr, Targets: early, Scope: ScopeUS, InScope: true, Now: epoch})
	if score != 14 {
		t.Fatalf("score = %d, want 4+3+3+3+1; reasons %v", score, reasons)
	}
	if len(reasons) != 5 {
		t.Fatalf("reasons = %v", reasons)
	}
}

func TestRank_Seniority_ConfiguredWeightsReplaceBuiltins(t *testing.T) {
	r := rule(nil, nil, workspace.Boost{Term: "staff", Weight: -6})
	title := "Staff Senior Engineer"
	score, _ := Rank(Posting{Title: title}, RankInput{Title: r.Check(title), Scope: ScopeWorldwide, Now: epoch})
	if score != -6 {
		t.Fatalf("score = %d, want -6 and not the built-in -4 on top", score)
	}
	builtin := rule(nil, nil)
	title = "Senior Junior Engineer"
	early := NewRankTargets([]workspace.Role{{Title: "Data Analyst", Seniority: "junior"}})
	score, _ = Rank(Posting{Title: title}, RankInput{Title: builtin.Check(title), Targets: early, Scope: ScopeWorldwide, Now: epoch})
	if score != -1 {
		t.Fatalf("built-ins both apply for an early-career seeker: score = %d, want -4+3", score)
	}
}

// The demo persona targets senior Platform and Backend Engineer roles: its own
// target titles rank first (family words in any order), early-career wording
// ("Associate", "Engineer 1") is a penalty, and senior wording never is.
func TestRank_Seniority_FollowsTheTargetRoles(t *testing.T) {
	senior := NewRankTargets([]workspace.Role{{Title: "Platform Engineer", Seniority: "senior"}, {Title: "Backend Engineer", Seniority: "senior"}})
	builtin := rule(nil, nil)
	score := func(title string, tg RankTargets) int {
		s, _ := Rank(Posting{Title: title}, RankInput{Title: builtin.Check(title), Targets: tg, Scope: ScopeWorldwide, Now: epoch})
		return s
	}
	for title, want := range map[string]int{
		"Senior Platform Engineer":            4,
		"Software Engineer, Platform":         4,
		"Engineering Manager, Platform":       0,
		"Engineer, Backend Platforms":         4,
		"Sales Associate, Newbury":            -4,
		"Software Engineer 1":                 -4,
		"Staff Backend Engineer, Agent Infra": 4,
	} {
		if got := score(title, senior); got != want {
			t.Errorf("senior seeker, %q: %d, want %d", title, got, want)
		}
	}
	if score("Senior Platform Engineer", senior) <= score("Sales Associate, Newbury", senior) {
		t.Error("the target title must outrank an unrelated one")
	}
	early := NewRankTargets([]workspace.Role{{Title: "Software Engineer", Seniority: "junior"}})
	if got := score("Software Engineer 1", early); got != 7 {
		t.Errorf("early seeker, entry title: %d, want 4+3", got)
	}
	if got := score("Senior Software Engineer", early); got != 0 {
		t.Errorf("early seeker, senior title: %d, want 4-4", got)
	}
	if got := score("Member of Technical Staff, Software Engineer", early); got != 4 {
		t.Errorf("early seeker, Member of Technical Staff names no level: %d, want 4", got)
	}
	if got := score("Senior Software Engineer", NewRankTargets(nil)); got != 0 {
		t.Errorf("no targets: %d, want no adjustment", got)
	}
	mid := NewRankTargets([]workspace.Role{{Title: "Data Engineer", Seniority: "mid"}})
	if got := score("Junior Data Engineer", mid) + score("Senior Data Engineer", mid); got != 8 {
		t.Errorf("mid seeker: seniority wording is neutral both ways, got %d", got)
	}
}

func TestRank_Recency_Bands(t *testing.T) {
	tr := rule(nil, nil).Check("Engineer")
	cases := map[time.Duration]int{
		-24 * time.Hour:     3, // a future date counts as zero days
		7 * 24 * time.Hour:  3,
		8 * 24 * time.Hour:  1,
		31 * 24 * time.Hour: 0,
		91 * 24 * time.Hour: -2,
	}
	for age, want := range cases {
		p := Posting{Title: "Engineer", PostedAt: epoch.Add(-age)}
		if s, _ := Rank(p, RankInput{Title: tr, Scope: ScopeWorldwide, Now: epoch}); s != want {
			t.Errorf("age %v: score %d, want %d", age, s, want)
		}
	}
	if s, _ := Rank(Posting{Title: "Engineer"}, RankInput{Title: tr, Scope: ScopeWorldwide, Now: epoch}); s != 0 {
		t.Errorf("undated: %d", s)
	}
}

func TestRank_RecencySort_UndatedLast(t *testing.T) {
	ps := []Posting{
		{Title: "undated-low", Score: 1},
		{Title: "old", Score: 9, PostedAt: epoch.Add(-10 * 24 * time.Hour)},
		{Title: "undated-high", Score: 7},
		{Title: "new", Score: 2, PostedAt: epoch.Add(-1 * 24 * time.Hour)},
		{Title: "mid", Score: 5, PostedAt: epoch.Add(-5 * 24 * time.Hour)},
	}
	sortPostings(ps, SortRecency)
	var got []string
	for _, p := range ps {
		got = append(got, p.Title)
	}
	want := []string{"new", "mid", "old", "undated-high", "undated-low"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func sortPostings(ps []Posting, order string) {
	less := Less(order)
	sort.SliceStable(ps, func(i, j int) bool { return less(ps[i], ps[j]) })
}
