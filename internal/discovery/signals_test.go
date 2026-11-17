package discovery

import "testing"

func TestSignals_EarlyCareer_UnionRescueOnly(t *testing.T) {
	cases := map[string]bool{
		"Software Engineer I":         true,
		"Software Engineer II":        false,
		"Software Engineer 12":        false,
		"New Grad Software Engineer":  true,
		"Engineering Intern (Summer)": true,
		"IC1 Backend":                 true,
		"Level II Support Engineer":   false,
		"Senior Staff Engineer":       false,
	}
	for title, want := range cases {
		if got := EarlyCareer(title, ""); got != want {
			t.Errorf("EarlyCareer(%q) = %v, want %v", title, got, want)
		}
	}
	if !EarlyCareer("Software Engineer", "Our university graduate program welcomes you") {
		t.Error("text must count")
	}
}

func TestSignals_Clearance_SentenceScopeNegationEligibility(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"preferred plus", "An active security clearance is a plus.", false},
		{"obtain and maintain", "Must be able to obtain and maintain a security clearance.", true},
		{"eligible to obtain", "Candidates must be eligible to obtain a security clearance.", true},
		{"active top secret", "Requires an active Top Secret clearance.", true},
		{"TS/SCI dotted", "Must hold an active T.S./S.C.I. clearance", true},
		{"not required", "A security clearance is not required.", false},
		{"public trust only", "Must be able to obtain a public trust.", false},
		{"public trust clearance wording", "Must be able to obtain a public trust clearance.", false},
		{"preferred wording", "Secret clearance preferred.", false},
		{"clearable", "Applicants must be clearable.", false},
		{"after hire", "We will sponsor your clearance after hire.", false},
		{"no mention", "We value collaboration and craft.", false},
		{"mention without requirement", "Our offices have a secure clearance process for visitors", false},
		{"requirement above preferred heading", "Requirements:\n- Active secret clearance required\n- 3 years of Go\nPreferred qualifications:\n- Kubernetes", true},
		{"bulleted list", "- Must hold a TS/SCI clearance;\n- Nice to have: AWS", true},
	}
	for _, c := range cases {
		if got := ClearanceRequired(c.text); got != c.want {
			t.Errorf("%s: ClearanceRequired = %v, want %v\n%s", c.name, got, c.want, c.text)
		}
	}
}

func yearsOf(text string) string {
	if y := RequiredYears(text); y != nil {
		return itoa(*y)
	}
	return "none"
}

func TestSignals_RequiredYears_AnchoredHeadingsRanges(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"range lower bound", "0-2 years required.", "0"},
		{"colleagues tenure ignored", "0-2 years required.\nOur engineers on the team, many with 12+ years of experience, will mentor you.", "0"},
		{"plus", "You have 5+ years of experience building APIs.", "5"},
		{"preferred section ignored", "Required qualifications:\n- 2 years of experience in Python\nPreferred qualifications:\n- 8+ years of experience with Rust", "2"},
		{"preferred heading reopened", "Preferred qualifications:\n- 8+ years of experience with Rust\nMinimum qualifications:\n- 4 years of experience in Go", "4"},
		{"preferred sentence", "5+ years of experience is a plus.", "none"},
		{"requirement outranks incidental", "We have 15 years of experience serving customers.\nRequires 3 years of experience with Go.", "3"},
		{"word number", "Five years of experience is required.", "5"},
		{"cap at 30", "Requires 99+ years of experience.", "30"},
		{"none", "Join a friendly team.", "none"},
		{"max of requirements", "Must have 2 years of experience in Go.\nMust have 6+ years of experience in distributed systems.", "6"},
		{"age floor is not experience", "Are you at least 18 years of age? *\nRequires 2 years of experience.", "2"},
		{"age floor alone", "Applicants must be at least 21 years old and have customer experience.", "none"},
		{"founding team history", "Despite 30+ years of combined AI experience on the founding team, we keep learning.", "none"},
		{"decimal sentence boundary", "Requires 3 years of experience. Version 2.5 is used.", "3"},
	}
	for _, c := range cases {
		if got := yearsOf(c.text); got != c.want {
			t.Errorf("%s: RequiredYears = %s, want %s", c.name, got, c.want)
		}
	}
}
