// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"

	"applysmith/internal/identity"
)

func TestGates_SkipList_DefaultAbsentWholeWhenPresentMalformedStops(t *testing.T) {
	long := strings.Repeat("x", MaxSkipEntryLength+1)
	many := make([]string, MaxSkipEntries+1)
	for _, c := range []struct {
		name string
		cfg  SkipListConfig
		want []string
		v    Verdict
		code string
	}{
		{"absent is the default", SkipListConfig{}, DefaultSkipList, Allow, CodeOK},
		{"present is the whole list", SkipListConfig{Present: true, Entries: []string{"Tesla"}}, []string{"Tesla"}, Allow, CodeOK},
		{"empty list skips nothing", SkipListConfig{Present: true}, []string{}, Allow, CodeOK},
		{"not a list", SkipListConfig{Present: true, NotAStringList: true}, nil, Deny, CodeSkipListMalformed},
		{"500 entries", SkipListConfig{Present: true, Entries: many[:MaxSkipEntries]}, many[:MaxSkipEntries], Allow, CodeOK},
		{"501 entries", SkipListConfig{Present: true, Entries: many}, nil, Deny, CodeSkipListTooLong},
		{"120 characters", SkipListConfig{Present: true, Entries: []string{long[:MaxSkipEntryLength]}}, []string{long[:MaxSkipEntryLength]}, Allow, CodeOK},
		{"121 characters", SkipListConfig{Present: true, Entries: []string{long}}, nil, Deny, CodeSkipListEntryLong},
	} {
		t.Run(c.name, func(t *testing.T) {
			list, d := ResolveSkipList(c.cfg)
			got(t, d, GateSkipListConfig, c.v, c.code)
			if len(list) != len(c.want) {
				t.Fatalf("list = %v", list)
			}
			if c.v == Deny && d.Class != ClassEnvironment {
				t.Fatalf("class = %q", d.Class)
			}
		})
	}
	if DefaultSkipList[0] != "Apple" || len(DefaultSkipList) != 7 {
		t.Fatalf("defaults = %v", DefaultSkipList)
	}
}

func TestGates_SkipList_TokenSetAndAliasGroupsNeverSubstring(t *testing.T) {
	for _, c := range []struct {
		entry, company string
		want           bool
	}{
		{"Amazon", "Amazon Web Services (AWS)", true},
		{"Amazon", "Amazonia Example Labs", false},
		{"Meta", "Metabolic Example Inc", false},
		{"Meta", "Facebook", true},
		{"Meta", "WhatsApp", true},
		{"Alphabet", "Google DeepMind", true},
		{"Google", "YouTube", true},
		{"Alphabet", "Alphabet Inc.", true},
		{"Amazon", "AWS", true},
		{"Rocket", "NewRocket", false},
		{"apple", "APPLE", true},
		{"Tesla", "Teslas Example", false},
		{"Apple", "Apple Bank", true},
		{"Microsoft", "Meta", false},
	} {
		if got := CoversCompany(c.entry, c.company); got != c.want {
			t.Errorf("CoversCompany(%q, %q) = %v, want %v", c.entry, c.company, got, c.want)
		}
	}
}

// The word-set comparison is the one same-candidacy-strict uses; this keeps
// the local copy from drifting away from the identity package.
func TestGates_SkipList_WordSetAgreesWithSameCandidacyStrict(t *testing.T) {
	pairs := [][2]string{
		{"Amazon", "Amazon Web Services (AWS)"}, {"Example Corp", "Example Corp UK"}, {"Rocket", "NewRocket"},
		{"Example", "The Example Group"}, {"Example Labs", "Example"}, {"Alpha Beta", "Beta Gamma"}, {"Example Inc", "Sample Inc"},
	}
	for _, p := range pairs {
		want := identity.SameCandidacyStrict(identity.Posting{Company: p[0], Role: "Engineer"}, identity.Posting{Company: p[1], Role: "Engineer"})
		if got := wordSetsMatch(nameSets(p[0]), nameSets(p[1])); got != want {
			t.Errorf("%v: local %v, identity %v", p, got, want)
		}
	}
}

func TestGates_SkipList_CriterionIsTheListNotTheSite(t *testing.T) {
	// The input has no site or ATS field: a listed company is skipped on any
	// applicant-tracking system, and an unlisted one on a custom site is not.
	list := []string{"NVIDIA"}
	got(t, SkipCompany(SkipInput{Company: "NVIDIA", Entries: list}).Decision, GateSkipList, Deny, CodeSkipListed)
	got(t, SkipCompany(SkipInput{Company: "Example Small Co", Entries: list}).Decision, GateSkipList, Allow, CodeOK)
}

func TestGates_SkipList_SkipEvenWhenNamedDuplicateFactWinsNotCounted(t *testing.T) {
	// Naming the company or retrying today's stops adds no input that lifts the
	// skip, so the Meta row is still left to the user.
	d := SkipCompany(SkipInput{Company: "Meta", Entries: DefaultSkipList})
	got(t, d.Decision, GateSkipList, Deny, CodeSkipListed)
	if d.Entry != "Meta" || d.Reason != `Meta is on your batch skip list (as "Meta") `+"—"+` apply there yourself` {
		t.Fatalf("decision = %+v", d)
	}
	d = SkipCompany(SkipInput{Company: "Meta", Entries: DefaultSkipList, DuplicateFact: true})
	got(t, d.Decision, GateSkipList, Deny, CodeDuplicateBeforeSkip)
	got(t, SkipCompany(SkipInput{Company: "Facebook", Entries: DefaultSkipList}).Decision, GateSkipList, Deny, CodeSkipListed)
}

func TestGates_SkipList_RemovedDefaultStaysRemoved(t *testing.T) {
	got(t, SkipCompany(SkipInput{Company: "Apple", Entries: []string{"Tesla"}}).Decision, GateSkipList, Allow, CodeOK)
}

func TestGates_SkipList_SingleExplicitURLNotBlockedButNoSolve(t *testing.T) {
	got(t, SkipCompany(SkipInput{Company: "Apple", Entries: DefaultSkipList, SingleExplicitURL: true}).Decision, GateSkipList, Allow, CodeSkipSingleURL)
	// The solver still refuses for that company.
	d := CaptchaSolve(CaptchaInput{Run: RunApplySubmit, Switch: SolverOn, KeyConfigured: true, Challenge: ChallengeHCaptcha, Point: PointBeforePress, Skip: SkipListed, BalanceKnown: true, BalanceMicroUSD: 1_000_000})
	got(t, d, GateCaptchaSolve, Deny, CodeSkipListCompany)
}
