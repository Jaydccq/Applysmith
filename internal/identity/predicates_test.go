package identity

import "testing"

type pair struct {
	name    string
	a, b    Posting
	opening bool
	strict  bool
}

func pp(company, role, location string) Posting {
	return Posting{Company: company, Role: role, Location: location}
}

func TestPredicates_Table(t *testing.T) {
	tests := []pair{
		{"years and boilerplate ignored", pp("Example Corp", "Software Engineer (Remote)", ""), pp("Example Corp", "Software Engineer - Remote 2026", ""), true, false},
		{"level markers separate roles", pp("Example Corp", "Software Engineer I", ""), pp("Example Corp", "Software Engineer II", ""), false, false},
		{"digit levels separate roles", pp("Example Corp", "Software Engineer 2", ""), pp("Example Corp", "Software Engineer 3", ""), false, false},
		{"location conflict", pp("Example Corp", "Software Engineer", "Exampleton"), pp("Example Corp", "Software Engineer", "Sampleville"), false, true},
		{"location shares a token", pp("Example Corp", "Software Engineer", "Exampleton, EX"), pp("Example Corp", "Software Engineer", "Exampleton"), true, true},
		{"missing location is no conflict", pp("Example Corp", "Software Engineer", ""), pp("Example Corp", "Software Engineer", "Exampleton"), true, true},
		{"remote alone is no place", pp("Example Corp", "Software Engineer", "Remote"), pp("Example Corp", "Software Engineer", "Exampleton"), true, true},
		{"remote with different countries conflicts", pp("Example Corp", "Software Engineer", "Remote - US"), pp("Example Corp", "Software Engineer", "Remote - UK"), false, true},
		{"strict ignores location, opening does not", pp("Example Corp UK", "junior  engineer", "Sampleville"), pp("Example Corp", "Junior Engineer", "Exampleton"), false, true},
		{"qualifier separates roles", pp("Example Corp", "Software Engineer (iOS)", ""), pp("Example Corp", "Software Engineer", ""), false, false},
		{"parenthetical qualifier equal", pp("Example Corp", "Software Engineer (iOS)", ""), pp("Example Corp", "Software Engineer (iOS)", ""), true, true},
		{"differing requisition numbers", pp("Example Corp", "AI Analyst - 11444", ""), pp("Example Corp", "AI Analyst - 12125", ""), false, false},
		{"requisition number on one side only", pp("Example Corp", "AI Analyst", ""), pp("Example Corp", "AI Analyst - 12125", ""), true, false},
		{"same requisition number", pp("Example Corp", "AI Analyst - 12125", ""), pp("Example Corp", "AI Analyst (12125)", ""), true, true},
		{"a year is not a requisition number", pp("Example Corp", "AI Analyst 2025", ""), pp("Example Corp", "AI Analyst 2026", ""), true, false},
		{"punctuation in the role", pp("Example Corp", "Junior Engineer - Backend", ""), pp("Example Corp", "junior engineer backend", ""), true, true},
		{"jaccard below 0.8", pp("Example Corp", "Senior Backend Platform Engineer", ""), pp("Example Corp", "Senior Backend Platform Engineer Payments Team", ""), false, false},
		{"jaccard exactly 0.8", pp("Example Corp", "Senior Backend Platform Engineer", ""), pp("Example Corp", "Senior Backend Platform Engineer Payments", ""), true, false},
		{"boilerplate alone is not overlap", pp("Example Corp", "Remote Full Time Sales Manager", ""), pp("Example Corp", "Remote Full Time Data Scientist", ""), false, false},
		{"single shared word differs", pp("Example Corp", "Engineer", ""), pp("Example Corp", "Engineering Manager", ""), false, false},
		{"single word equal", pp("Example Corp", "Engineer", ""), pp("Example Corp", "The Engineer", ""), true, false},
		{"language markers kept", pp("Example Corp", "C++ Engineer", ""), pp("Example Corp", "C# Engineer", ""), false, false},
		{"role made of boilerplate only", pp("Example Corp", "Remote", ""), pp("Example Corp", "Remote", "Exampleton"), false, true},
		{"placeholder company unknown", pp("Unknown", "Software Engineer", ""), pp("Unknown", "Software Engineer", ""), false, false},
		{"placeholder n/a", pp("N/A", "Software Engineer", ""), pp("N/A", "Software Engineer", ""), false, false},
		{"placeholder stealth startup", pp("Stealth Startup", "Software Engineer", ""), pp("stealth  startup", "Software Engineer", ""), false, false},
		// What an evaluation files for a posting that names no employer or role.
		{"placeholder company the evaluation files", pp("Unknown company", "Software Engineer", ""), pp("Unknown company", "Software Engineer", ""), false, false},
		{"placeholder role the evaluation files", pp("Example Corp", "Unknown role", ""), pp("Example Corp", "Unknown role", ""), false, false},
		{"empty company", pp("", "Software Engineer", ""), pp("", "Software Engineer", ""), false, false},
		{"empty role", pp("Example Corp", "", ""), pp("Example Corp", "", ""), false, false},
		{"legal suffixes", pp("Example, Inc.", "Engineer", ""), pp("EXAMPLE LLC", "Engineer", ""), true, true},
		{"ampersand and accents", pp("Café & Sons Ltd", "Baker", ""), pp("Cafe and Sons", "Baker", ""), true, true},
		{"leading the", pp("The Example Company", "Engineer", ""), pp("Example", "Engineer", ""), true, true},
		{"parenthetical annotation ignored", pp("Example Corp (Hiring Now)", "Engineer", ""), pp("Example Corp", "Engineer", ""), true, true},
		{"parenthetical alias counts as a name", pp("Parent Holdings (Example Corp)", "Engineer", ""), pp("Example Corp", "Engineer", ""), true, true},
		{"company set never substring", pp("NewRocket", "Engineer", ""), pp("Rocket", "Engineer", ""), false, false},
		{"strict subset of words", pp("Example Group", "Engineer", ""), pp("Example Holdings Group", "Engineer", ""), false, true},
		{"different companies", pp("Example Corp", "Engineer", ""), pp("Sample Corp", "Engineer", ""), false, false},
		{"strict role is exact", pp("Example Corp", "Software Engineer", ""), pp("Example Corp", "Software Engineer (Remote)", ""), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, flip := range []bool{false, true} {
				a, b := tt.a, tt.b
				if flip {
					a, b = b, a
				}
				if got := SameOpening(a, b); got != tt.opening {
					t.Errorf("SameOpening(flip=%v) = %v, want %v", flip, got, tt.opening)
				}
				if got := SameCandidacyStrict(a, b); got != tt.strict {
					t.Errorf("SameCandidacyStrict(flip=%v) = %v, want %v", flip, got, tt.strict)
				}
			}
		})
	}
}

func TestSameOpening_UnequalATSNativeKeysAreDistinct(t *testing.T) {
	a := Posting{Key: "greenhouse:example-co/1001", Company: "Example Corp", Role: "Software Engineer"}
	b := Posting{Key: "greenhouse:example-co/2002", Company: "Example Corp", Role: "Software Engineer"}
	if SameOpening(a, b) {
		t.Error("same-opening held between two unequal ATS-native keys")
	}
	if !SameCandidacyStrict(a, b) {
		t.Error("the strict guard is not bound by the key rule and must match on company and role")
	}
	c := Posting{URL: "https://mirror.example.com/p/9", Company: "Example Corp", Role: "Software Engineer"}
	if !SameOpening(a, c) {
		t.Error("an ATS posting and a mirror of it must be the same opening")
	}
}

func TestPredicates_EqualKeysAlwaysMatch(t *testing.T) {
	a := Posting{URL: "https://boards.greenhouse.io/example-co/jobs/1001", Company: "Example Corp", Role: "Engineer"}
	b := Posting{URL: "https://job-boards.greenhouse.io/embed/job_app?for=example-co&token=1001", Company: "Other Name", Role: "Totally Different Title"}
	if !SameOpening(a, b) || !SameCandidacyStrict(a, b) {
		t.Error("equal identity keys are one posting whatever the other fields say")
	}
}

func TestSameCandidacyStrict_PlaceholderCompanyNeverMatches(t *testing.T) {
	a := pp("Unknown", "Engineer", "")
	if SameCandidacyStrict(a, a) || SameOpening(a, a) {
		t.Error("a placeholder company named an employer")
	}
}

func TestSameForDedup_Table(t *testing.T) {
	tests := []struct {
		name string
		a, b Posting
		want bool
	}{
		{"equal titles", pp("Example Corp", "Software Engineer", ""), pp("Example Corp", "software engineer", ""), true},
		{"same word set reordered", pp("Example Corp", "Backend Engineer", ""), pp("Example Corp", "Engineer, Backend", ""), true},
		{"filler words ignored", pp("Example Corp", "Director of Engineering", ""), pp("Example Corp", "Director, Engineering", ""), true},
		{"extra word separates", pp("Example Corp", "Software Development Engineer", ""), pp("Example Corp", "Software Development Engineer - Database 2026", ""), false},
		{"seniority counts", pp("Example Corp", "Software Engineer", ""), pp("Example Corp", "Senior Software Engineer", ""), false},
		{"employment type counts", pp("Example Corp", "Software Engineer", ""), pp("Example Corp", "Software Engineer Intern", ""), false},
		{"two letter words count", pp("Example Corp", "AI Engineer", ""), pp("Example Corp", "Engineer", ""), false},
		{"differing requisition numbers", pp("Example Corp", "AI Analyst - 11444", ""), pp("Example Corp", "AI Analyst - 12125", ""), false},
		{"level only role needs exact title", pp("Example Corp", "Senior II", ""), pp("Example Corp", "II Senior", ""), false},
		{"level only exact title", pp("Example Corp", "Senior", ""), pp("Example Corp", "senior", ""), true},
		{"placeholder groups nothing", pp("Unknown", "Engineer", ""), pp("Unknown", "Engineer", ""), false},
		{"location conflict", pp("Example Corp", "Engineer", "Exampleton"), pp("Example Corp", "Engineer", "Sampleville"), false},
		{"unequal ATS keys", Posting{Key: "lever:example-co/aaaaaaaa-1", Company: "Example Corp", Role: "Engineer"}, Posting{Key: "lever:example-co/bbbbbbbb-2", Company: "Example Corp", Role: "Engineer"}, false},
		{"different company", pp("Example Corp", "Engineer", ""), pp("Sample Corp", "Engineer", ""), false},
		{"equal keys", Posting{Key: "k:1", Company: "A", Role: "x"}, Posting{Key: "k:1", Company: "B", Role: "y"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameForDedup(tt.a, tt.b); got != tt.want {
				t.Errorf("SameForDedup = %v, want %v", got, tt.want)
			}
			if got := SameForDedup(tt.b, tt.a); got != tt.want {
				t.Errorf("SameForDedup flipped = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCompanyNames(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"Example Corp", []string{"Example Corp"}},
		{"Example Corp (Hiring)", []string{"Example Corp", "Hiring"}},
		{"Parent (Example Corp) Holdings", []string{"Parent Holdings", "Example Corp"}},
		{"Example Inc. is hiring", []string{"Example Inc. is hiring"}},
		{"Dr. Pepper", []string{"Dr. Pepper"}},
		{"(Only Alias)", []string{"Only Alias"}},
	}
	for _, tt := range tests {
		got := companyNames(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("companyNames(%q) = %q, want %q", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("companyNames(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	}
}

func TestSameCandidacyStrict_QualifiersKeptPunctuationCollapsed(t *testing.T) {
	differ := [][2]string{
		{"Software Engineer (iOS)", "Software Engineer"},
		{"Account Executive (Sr.)", "Account Executive"},
		{"Solutions Architect (Technical, Public Sector)", "Solutions Architect"},
	}
	for _, d := range differ {
		if SameCandidacyStrict(pp("Example Corp", d[0], ""), pp("Example Corp", d[1], "")) {
			t.Errorf("%q and %q are two openings", d[0], d[1])
		}
	}
	same := [][2]string{
		{"Engineer - Backend", "Engineer Backend"},
		{"  Software   Engineer ", "software engineer"},
		{"Software Engineer (iOS)", "Software Engineer - iOS"},
		{"Data/ML Engineer", "data ml engineer"},
	}
	for _, s := range same {
		if !SameCandidacyStrict(pp("Example Corp", s[0], ""), pp("Example Corp", s[1], "")) {
			t.Errorf("%q and %q differ by punctuation only", s[0], s[1])
		}
	}
}

func TestPredicates_EmptySideNeverMatches(t *testing.T) {
	full := pp("Example Corp", "Software Engineer", "")
	for name, empty := range map[string]Posting{
		"no company":   pp("", "Software Engineer", ""),
		"no role":      pp("Example Corp", "", ""),
		"nothing":      pp("", "", ""),
		"blank spaces": pp("  ", " ", ""),
	} {
		for _, other := range []Posting{full, empty} {
			if SameOpening(empty, other) || SameCandidacyStrict(empty, other) || SameForDedup(empty, other) {
				t.Errorf("%s matched %+v", name, other)
			}
		}
		if k := empty.IdentityKey(); k != "" {
			t.Errorf("%s has the identity key %q", name, k)
		}
	}
}
