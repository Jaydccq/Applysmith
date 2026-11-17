package discovery

import (
	"testing"

	"applysmith/internal/workspace"
)

func rule(pos, neg []string, boosts ...workspace.Boost) TitleRule {
	return NewTitleRule(workspace.TitleFilter{Positive: pos, Negative: neg, SeniorityBoost: boosts})
}

func TestFilter_Keyword_WholeWordWithPluralGerund(t *testing.T) {
	cases := []struct {
		name       string
		pos, neg   []string
		title      string
		wantPass   bool
		wantReason string
	}{
		{"negative inside a longer word", nil, []string{"intern"}, "Software Engineer, International Payments", true, ""},
		{"negative wins", []string{"engineer"}, []string{"intern"}, "Senior Software Engineer Intern", false, "negative:intern"},
		{"negative plural", nil, []string{"intern"}, "Software Engineering Interns", false, "negative:intern"},
		{"positive inside a longer word", []string{"ai"}, nil, "Retail Operations Analyst", false, "no-positive"},
		{"plural form", []string{"agent"}, nil, "Agents Platform Engineer", true, ""},
		{"gerund form", []string{"engineer"}, nil, "Engineering Manager", true, ""},
		{"phrase flexible whitespace", []string{"machine learning"}, nil, "Machine   Learning Engineer", true, ""},
		{"positive words in another order", []string{"backend engineer"}, nil, "Software Engineer, Backend", true, ""},
		{"positive words split by punctuation", []string{"backend engineer"}, nil, "Backend/API Engineer", true, ""},
		{"positive needs every word", []string{"backend engineer"}, nil, "Frontend Engineer", false, "no-positive"},
		{"negative stays a phrase", nil, []string{"sales engineer"}, "Engineer, Sales Platform", true, ""},
		{"case insensitive", []string{"GO"}, nil, "go developer", true, ""},
		{"empty positive passes", nil, nil, "Anything", true, ""},
		{"punctuation boundary", []string{"c++"}, nil, "C++ Developer", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rule(c.pos, c.neg).Check(c.title)
			if got.Pass != c.wantPass || got.Reason != c.wantReason {
				t.Fatalf("Check(%q) = %+v", c.title, got)
			}
		})
	}
}

func TestFilter_BoostOnlyRanks(t *testing.T) {
	r := rule([]string{"engineer"}, nil, workspace.Boost{Term: "senior", Weight: -3}, workspace.Boost{Term: "staff", Weight: -2})
	got := r.Check("Senior Software Engineer")
	if !got.Pass || got.Boost != -3 || !got.Boosted {
		t.Fatalf("got %+v", got)
	}
	// Member of Technical Staff is a title family, not the staff rung.
	if got := r.Check("Member of Technical Staff, Software Engineer"); got.Boost != 0 {
		t.Fatalf("Member of Technical Staff weighed %+v", got)
	}
}

func TestFilter_ExcludeBeatsInclude(t *testing.T) {
	var p workspace.Portals
	p.Filters.Locations.Include = []string{"United States", "Remote"}
	p.Filters.Locations.Exclude = []string{"Remote - Brazil"}
	f := NewFilters(p, nil)
	for loc, want := range map[string]bool{"Remote - Brazil": false, "Remote - US": true, "Berlin": false, "United States": true} {
		if got := f.LocationOK(loc); got != want {
			t.Errorf("LocationOK(%q) = %v, want %v", loc, got, want)
		}
	}
	empty := NewFilters(workspace.Portals{}, nil)
	if !empty.LocationOK("Anywhere") {
		t.Error("empty include must pass")
	}
}

func TestBlocked_SkipListWholeWord(t *testing.T) {
	var p workspace.Portals
	p.Filters.SkipCompanies = []string{"Meta"}
	f := NewFilters(p, nil)
	if f.CompanyBlocked("Metabolic Example Inc") {
		t.Error("Metabolic must not match Meta")
	}
	if !f.CompanyBlocked("Meta Platforms Example") || !f.CompanyBlocked("meta") {
		t.Error("whole-word match missing")
	}
}

func TestBlocked_NormalizedExactMatchOnly(t *testing.T) {
	f := NewFilters(workspace.Portals{}, []string{NormalizeCompany("Acme Corp")})
	for company, want := range map[string]bool{"ACME, Inc.": true, "Acme": true, "Acme Labs": false, "Unknown": false} {
		if got := f.CompanyBlocked(company); got != want {
			t.Errorf("CompanyBlocked(%q) = %v, want %v", company, got, want)
		}
	}
}

func TestBlockedStore_AddRemoveList(t *testing.T) {
	e := newEnv(t)
	b, err := e.rp.BlockCompany(e.ctx, "Acme Corp", "no thanks", "user")
	if err != nil || b.Normalized == "" {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err := e.rp.BlockCompany(e.ctx, "ACME, Inc.", "", "user"); err != nil {
		t.Fatal(err)
	}
	list, _ := e.rp.BlockedCompanies(e.ctx)
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if _, err := e.rp.BlockCompany(e.ctx, "Unknown", "", "user"); err == nil {
		t.Fatal("a placeholder names no employer")
	}
	if err := e.rp.UnblockCompany(e.ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := e.rp.UnblockCompany(e.ctx, "acme"); err == nil {
		t.Fatal("second remove must fail")
	}
}

func TestScope_Classify_UnknownMixedInvalid(t *testing.T) {
	cases := []struct {
		loc, class string
		scope      string
		pass       bool
		unverified bool
	}{
		{"Toronto, ON; London, UK", ClassMixed, ScopeCanada, true, false},
		{"Remote", ClassUnknown, ScopeUS, true, true},
		{"", ClassUnknown, ScopeUS, true, true},
		{"Exampleburg, DE", ClassUnknown, ScopeUS, true, true},
		{"Austin, TX", ScopeUS, ScopeUS, true, false},
		{"Remote - US", ScopeUS, ScopeUS, true, false},
		{"Berlin, Germany", ScopeEurope, ScopeUS, false, false},
		{"Ukulele Studio Campus", ClassUnknown, ScopeUK, true, true},
		{"Tukwila", ClassUnknown, ScopeUK, true, true},
		{"London, UK", ScopeUK, ScopeUK, true, false},
		{"Sydney, Australia", ScopeAsiaPacific, ScopeUS, false, false},
		{"Anywhere", ClassUnknown, ScopeWorldwide, true, false},
		{"Berlin", ScopeEurope, ScopeWorldwide, true, false},
		{"Ho Chi Minh City", ScopeAsiaPacific, ScopeUS, false, false},
		{"Mohali Office", ScopeAsiaPacific, ScopeUS, false, false},
		{"Remote (EMEA)", ScopeEurope, ScopeUS, false, false},
		{"Santiago", ClassElsewhere, ScopeUS, false, false},
		{"São Paulo, Brazil", ClassElsewhere, ScopeEurope, false, false},
		{"Exampleton, MX", ClassElsewhere, ScopeUS, false, false},
		{"Tel Aviv", ClassElsewhere, ScopeWorldwide, true, false},
	}
	for _, c := range cases {
		if got := ClassOf(c.loc); got != c.class {
			t.Errorf("ClassOf(%q) = %s, want %s", c.loc, got, c.class)
		}
		got := ApplyScope(c.scope, c.loc)
		if got.Pass != c.pass || got.Unverified != c.unverified {
			t.Errorf("ApplyScope(%s, %q) = %+v", c.scope, c.loc, got)
		}
	}
}

// A code that is both a US state and a country names neither: "Wilmington, DE"
// is Delaware and "Pune, IN" is named by its city.
func TestScope_USStateCode_AmbiguousCodesExcluded(t *testing.T) {
	for _, code := range []string{"DE", "IN", "ID", "AR", "IL"} {
		if got := ClassOf("Exampleton, " + code); got != ClassUnknown {
			t.Errorf("%s classified %s", code, got)
		}
	}
	if ClassOf("Exampleton, CA") != ScopeUS {
		t.Error("CA must classify us")
	}
}

func TestScope_Invalid(t *testing.T) {
	err := ValidateScope("moon")
	if err == nil || !contains(err.Error(), "asia-pacific") {
		t.Fatalf("err = %v", err)
	}
	if ScopeFromCountries([]string{"US"}) != ScopeUS || ScopeFromCountries([]string{"US", "CA"}) != ScopeWorldwide || ScopeFromCountries(nil) != ScopeWorldwide {
		t.Error("scope derivation wrong")
	}
	if ScopeFromCountries([]string{"DE", "FR"}) != ScopeEurope {
		t.Error("two european countries are one scope")
	}
}
