// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

func ruleNames(fs []Failure) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return strings.Join(out, ",")
}

func settings(mod func(*GateSettings)) GateSettings {
	s := GateSettings{AllowRemote: true, RejectSeniority: []string{"manager", "director"},
		AllowedCountries: []string{"US", "CA"}, AllowedCurrencies: []string{"USD", "CAD"}}
	if mod != nil {
		mod(&s)
	}
	return s
}

// "Member of Technical Staff" names a title family, not the staff rung: a
// profile that rejects staff titles keeps it, unless a level beside it is one
// the profile rejects too.
func TestGate_MemberOfTechnicalStaffNamesNoLevel(t *testing.T) {
	s := settings(func(s *GateSettings) { s.RejectSeniority = []string{"staff", "principal"} })
	for title, want := range map[string]string{
		"Member of Technical Staff - New Grad (2027 Start)": "",
		"Staff Software Engineer":                           "seniority",
		"Principal Member of Technical Staff":               "seniority",
	} {
		if got := ruleNames(Gate(GateInput{Title: title, Location: "San Francisco, CA"}, s)); got != want {
			t.Errorf("%q: rules %q, want %q", title, got, want)
		}
	}
}

func TestGate_MultipleReasonsAreAllReported(t *testing.T) {
	got := Gate(GateInput{Title: "Engineering Manager", Location: "Berlin, Germany", Compensation: "€90,000 - €110,000"}, settings(nil))
	if ruleNames(got) != "seniority,geography,currency" {
		t.Fatalf("rules = %s", ruleNames(got))
	}
	for _, f := range got {
		if f.Detail == "" {
			t.Errorf("rule %s has no detail", f.Rule)
		}
	}
}

func TestGate_Geography(t *testing.T) {
	for _, c := range []struct {
		name, loc string
		remote    bool
		pass      bool
	}{
		{"allowed region wins over a disallowed country", "Toronto, ON or Berlin, Germany", true, true},
		{"allowed city beside a disallowed country", "Seattle / Dublin, Ireland", true, true},
		{"bare remote passes", "Remote", true, true},
		{"remote rejected when not allowed", "Remote", false, false},
		{"remote with allowed country passes when not allowed", "Remote - United States", false, true},
		{"ambiguous state code is Germany", "Exampleburg, DE", true, false},
		{"state with city and code", "Springfield, TX", true, true},
		{"ambiguous Indiana code is India", "Smallville, IN", true, false},
		{"Indiana by name is a state", "Smallville, Indiana", true, true},
		{"new mexico is not mexico", "Albuquerque, New Mexico", true, true},
		{"multi-country region", "EMEA", true, false},
		{"multi-country region with an allowed country", "EMEA or Canada", true, true},
		{"apac", "APAC remote", true, false},
		{"foreign country", "London, United Kingdom", true, false},
		{"a listed city alone names its country", "Bengaluru", true, false},
		{"an office in a listed foreign city", "Munich Office", true, false},
		{"unrecognizable passes", "Exampleville", true, true},
		{"blank passes", "", true, true},
		{"province", "Halifax, NS", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Geography(c.loc, []string{"US", "CA"}, c.remote)
			if got.Pass != c.pass {
				t.Fatalf("Geography(%q) = %+v", c.loc, got)
			}
		})
	}
}

func TestGate_GeographyRuleOffWithoutCountries(t *testing.T) {
	if !Geography("Berlin, Germany", nil, true).Pass {
		t.Fatal("with no allowed countries the rule must be off")
	}
}

func TestGate_Currency(t *testing.T) {
	allowed := []string{"USD", "CAD"}
	for _, c := range []struct {
		comp string
		bad  string
	}{
		{"$120,000", ""},
		{"US$120,000", ""},
		{"CA$120,000", ""},
		{"A$120,000", "AUD"},
		{"S$120,000", "SGD"},
		{"£70,000", "GBP"},
		{"EUR 70,000", "EUR"},
		{"USD 90000 - 110000", ""},
		{"", ""},
	} {
		if got := disallowedCurrency(c.comp, allowed); got != c.bad {
			t.Errorf("disallowedCurrency(%q) = %q, want %q", c.comp, got, c.bad)
		}
	}
	if got := disallowedCurrency("A$100", []string{"USD", "AUD"}); got != "" {
		t.Errorf("A$ with AUD allowed = %q", got)
	}
}

func TestGate_ClearanceUSPersonAndExperience(t *testing.T) {
	s := settings(func(s *GateSettings) { s.RejectClearance, s.RejectUSPerson, s.MaxRequiredYears = true, true, ptr(5) })
	got := Gate(GateInput{Title: "Engineer", Text: "Active TS/SCI clearance required. You must be a US person. 8+ years of experience."}, s)
	if ruleNames(got) != "clearance,us_person,experience" {
		t.Fatalf("rules = %s", ruleNames(got))
	}
	off := settings(nil)
	if got := Gate(GateInput{Title: "Engineer", Text: "Active TS/SCI clearance required. 8+ years of experience."}, off); len(got) != 0 {
		t.Fatalf("rules off: %s", ruleNames(got))
	}
}

func TestGate_BoardIndexVersusEmbedApplication(t *testing.T) {
	for _, c := range []struct {
		url   string
		board bool
	}{
		{"https://boards.greenhouse.io/exampleco", true},
		{"https://boards.greenhouse.io/embed/job_board?for=exampleco", true},
		{"https://boards.greenhouse.io/embed/job_app?for=exampleco&token=4012345", false},
		{"https://boards.greenhouse.io/exampleco/jobs/4012345", false},
		{"https://jobs.lever.co/exampleco", true},
		{"https://jobs.lever.co/exampleco/0a1b2c3d-1111-2222-3333-444455556666", false},
		{"https://jobs.ashbyhq.com/exampleco", true},
		{"https://jobs.example.test/p/1", false},
		{"", false},
	} {
		if got := IsBoardIndex(c.url); got != c.board {
			t.Errorf("IsBoardIndex(%q) = %v", c.url, got)
		}
	}
	if got := Gate(GateInput{Title: "x", URL: "https://boards.greenhouse.io/exampleco"}, settings(nil)); ruleNames(got) != "board_index" {
		t.Fatalf("rules = %s", ruleNames(got))
	}
}

func TestGate_BlockedEmployer(t *testing.T) {
	got := Gate(GateInput{Title: "Engineer", BlockedWhy: "the employer is on the blocked-companies list or in skip_companies"}, settings(nil))
	if ruleNames(got) != "blocked_employer" {
		t.Fatalf("rules = %s", ruleNames(got))
	}
}

func TestGate_NeutralDefaultsForANewProfile(t *testing.T) {
	s := ResolveGate(workspace.Profile{})
	in := GateInput{Title: "Principal Engineering Director", Location: "Tokyo, Japan", Compensation: "¥900,000",
		Text: "Active TS/SCI clearance required. US person only. 20+ years of experience.", URL: "https://jobs.example.test/p/1"}
	if got := Gate(in, s); len(got) != 0 {
		t.Fatalf("only board-index and blocked-employer may reject, got %s", ruleNames(got))
	}
	in.URL = "https://boards.greenhouse.io/exampleco"
	in.BlockedWhy = "blocked"
	if got := Gate(in, s); ruleNames(got) != "board_index,blocked_employer" {
		t.Fatalf("rules = %s", ruleNames(got))
	}
}

func TestGate_SeniorityDerivedFromTargets(t *testing.T) {
	var p workspace.Profile
	// Neither a list nor a target seniority: the rule is off, no title is rejected for rank.
	if got := ResolveGate(p).RejectSeniority; len(got) != 0 {
		t.Fatalf("a profile with no seniority signal rejects %v", got)
	}
	p.Targets.Roles = []workspace.Role{{Title: "Engineer", Seniority: "mid"}}
	s := ResolveGate(p)
	for _, c := range []struct {
		title string
		want  bool
	}{
		{"Senior Software Engineer", true}, {"Director of Engineering", true}, {"Software Engineer II", false},
		{"Staff Engineer", true}, {"VP Engineering", true}, {"Vice President, Engineering", true}, {"Software Engineer", false},
	} {
		got := len(Gate(GateInput{Title: c.title}, s)) > 0
		if got != c.want {
			t.Errorf("title %q rejected = %v", c.title, got)
		}
	}
	p.EligibilityGate.RejectSeniority = []string{"intern"}
	if got := Gate(GateInput{Title: "Senior Engineer"}, ResolveGate(p)); len(got) != 0 {
		t.Fatalf("an explicit list replaces the derived one: %s", ruleNames(got))
	}
	p.Targets.Roles = []workspace.Role{{Title: "Exec", Seniority: "executive"}}
	p.EligibilityGate.RejectSeniority = nil
	if got := ResolveGate(p).RejectSeniority; len(got) != 0 {
		t.Fatalf("nothing ranks above executive: %v", got)
	}
}

func TestGate_DerivedDefaults(t *testing.T) {
	var p workspace.Profile
	p.WorkAuthorization.AuthorizedCountries = []string{"US", "CA"}
	p.WorkAuthorization.RequiresSponsorshipNow = ptr(true)
	s := ResolveGate(p)
	if strings.Join(s.AllowedCurrencies, ",") != "USD,CAD" || !s.RejectClearance || !s.RejectUSPerson || !s.AllowRemote {
		t.Fatalf("settings = %+v", s)
	}
	p.WorkAuthorization.RequiresSponsorshipNow = ptr(false)
	p.WorkAuthorization.RequiresSponsorshipFuture = ptr(false)
	p.WorkAuthorization.SecurityClearance = "none"
	s = ResolveGate(p)
	if !s.RejectClearance || s.RejectUSPerson {
		t.Fatalf("settings = %+v", s)
	}
	p.EligibilityGate.RejectClearance = ptr(false)
	if ResolveGate(p).RejectClearance {
		t.Fatal("an explicit setting wins")
	}
}

func TestSponsorNeed(t *testing.T) {
	for _, c := range []struct {
		now, future *bool
		want        Need
	}{
		{ptr(true), nil, NeedNeeded}, {nil, ptr(true), NeedNeeded}, {ptr(false), ptr(true), NeedNeeded},
		{ptr(false), ptr(false), NeedNotNeeded}, {nil, nil, NeedUnanswered}, {ptr(false), nil, NeedUnanswered}, {nil, ptr(false), NeedUnanswered},
	} {
		if got := SponsorNeed(workspace.WorkAuth{RequiresSponsorshipNow: c.now, RequiresSponsorshipFuture: c.future}); got != c.want {
			t.Errorf("SponsorNeed = %s, want %s", got, c.want)
		}
	}
}

func TestGate_Clearance_And_USPerson_SeparateSwitches(t *testing.T) {
	both := "Active TS/SCI clearance required. US person status is required."
	for _, c := range []struct {
		name      string
		clearance bool
		usPerson  bool
		in        GateInput
		want      string
	}{
		{"clearance switch alone", true, false, GateInput{Title: "Engineer", Text: both}, "clearance"},
		{"us person switch alone", false, true, GateInput{Title: "Engineer", Text: both}, "us_person"},
		{"both switches", true, true, GateInput{Title: "Engineer", Text: both}, "clearance,us_person"},
		{"both off", false, false, GateInput{Title: "Engineer", Text: both}, ""},
		{"clearance named only in the title", true, false, GateInput{Title: "Systems Engineer (Secret clearance required)"}, "clearance"},
		{"eligible-to-obtain reading", true, false, GateInput{Title: "Engineer", Text: "Candidates must be eligible to obtain a security clearance."}, "clearance"},
		{"hold reading", true, false, GateInput{Title: "Engineer", Text: "Must hold an active Top Secret clearance."}, "clearance"},
		{"a clearance that is only a plus", true, false, GateInput{Title: "Engineer", Text: "An active security clearance is a plus."}, ""},
		{"us persons welcome is no requirement", false, true, GateInput{Title: "Engineer", Text: "US persons and non-US persons may apply."}, ""},
		{"us person with a requiring verb", false, true, GateInput{Title: "Engineer", Text: "You must be a U.S. person due to export controls."}, "us_person"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := settings(func(s *GateSettings) { s.RejectClearance, s.RejectUSPerson = c.clearance, c.usPerson })
			if got := ruleNames(Gate(c.in, s)); got != c.want {
				t.Fatalf("rules = %q, want %q", got, c.want)
			}
		})
	}

	// A permanent resident needs no sponsorship and is a US person, but is not
	// eligible for a clearance: the profile turns the two rules on separately.
	var p workspace.Profile
	p.WorkAuthorization.RequiresSponsorshipNow, p.WorkAuthorization.RequiresSponsorshipFuture = ptr(false), ptr(false)
	p.WorkAuthorization.SecurityClearance = "none"
	if s := ResolveGate(p); !s.RejectClearance || s.RejectUSPerson {
		t.Fatalf("settings = %+v", s)
	}
}

func TestGate_Experience_CutoffAbove(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		max  *int
		want string
	}{
		{"above the cutoff", "5+ years of experience in Go.", ptr(4), "experience"},
		{"at the cutoff", "4+ years of experience in Go.", ptr(4), ""},
		{"below the cutoff", "2 years of experience in Go.", ptr(4), ""},
		{"a range is read at its floor", "3-6 years experience with databases.", ptr(4), ""},
		{"a floor above the cutoff", "5-8 years experience with databases.", ptr(4), "experience"},
		{"preferred years are not a requirement", "10 years of experience is a plus.", ptr(4), ""},
		{"null disables the rule", "15+ years of experience in Go.", nil, ""},
		{"no stated years", "Build services in Go.", ptr(4), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := settings(func(s *GateSettings) { s.MaxRequiredYears = c.max })
			if got := ruleNames(Gate(GateInput{Title: "Engineer", Text: c.text}, s)); got != c.want {
				t.Fatalf("rules = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGate_BlankInputSkipsRule(t *testing.T) {
	s := settings(func(s *GateSettings) { s.RejectClearance, s.RejectUSPerson, s.MaxRequiredYears = true, true, ptr(1) })
	for name, in := range map[string]GateInput{
		"nothing":     {},
		"whitespace":  {Title: "  ", Seniority: " ", Location: "\t", Compensation: " ", Text: "\n", URL: " "},
		"title only":  {Title: "Engineer"},
		"text absent": {Title: "Engineer", Location: "", Compensation: ""},
	} {
		if got := Gate(in, s); len(got) != 0 {
			t.Errorf("%s: blank inputs failed %s", name, ruleNames(got))
		}
	}
}
