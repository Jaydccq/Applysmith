package identity

import "testing"

func TestCompanyNormalization_AbbreviationPeriodIsNotASentenceEnd(t *testing.T) {
	tests := []struct{ a, b string }{
		{"Dr. Pepper", "Dr. Martens"},
		{"St. Jude Children's Research Hospital", "St. Louis Cardinals"},
	}
	for _, tt := range tests {
		a, b := pp(tt.a, "Software Engineer", ""), pp(tt.b, "Software Engineer", "")
		if SameCandidacyStrict(a, b) || SameForDedup(a, b) || SameOpening(a, b) {
			t.Errorf("%q and %q must be different companies", tt.a, tt.b)
		}
		if ka, kb := CompanyKeys(tt.a), CompanyKeys(tt.b); sharesName(ka, kb) {
			t.Errorf("CompanyKeys(%q) = %q shares a name with CompanyKeys(%q) = %q", tt.a, ka, tt.b, kb)
		}
	}
	if got := CompanyKeys("Dr. Pepper"); len(got) != 1 || got[0] != "dr pepper" {
		t.Errorf("CompanyKeys(Dr. Pepper) = %q", got)
	}
	if !SameOpening(pp("Example Inc.", "Software Engineer", ""), pp("Example", "Software Engineer", "")) {
		t.Error("a trailing legal suffix is still dropped")
	}
}

func TestCompanyKey_PlaceholderAliasAndSuffixes(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"Example Corp", []string{"example"}},
		{"Example, Inc.", []string{"example"}},
		{"Example Holdings LLC Ltd", []string{"example holdings"}},
		{"The Example Company", []string{"example"}},
		{"Example Corp (Hiring Now)", []string{"example", "hiring now"}},
		{"Parent Holdings (Example Corp)", []string{"parent holdings", "example"}},
		{"Café & Sons Ltd", []string{"cafe and sons"}},
		{"Unknown", nil},
		{"N/A", nil},
		{"Stealth Startup", nil},
		{"Confidential (Example Corp)", []string{"example"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := CompanyKeys(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("CompanyKeys(%q) = %q, want %q", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("CompanyKeys(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	}
	// Placeholders name no employer, whatever else matches.
	a := pp("Unknown", "Software Engineer", "")
	if SameOpening(a, a) || SameCandidacyStrict(a, a) || SameForDedup(a, a) {
		t.Error("a placeholder company grouped two postings")
	}
}
