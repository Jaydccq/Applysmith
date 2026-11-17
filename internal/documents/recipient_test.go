package documents

import "testing"

// Scenario: Named recipient. Only a person's name in a labelled pattern on one
// line names the recipient; anything else keeps the default salutation.
func TestRecipient_OnlyAPersonOnOneLine(t *testing.T) {
	for _, c := range []struct{ name, posting, want string }{
		{"labelled person", "Hiring manager: Jane Doe\nBackend Engineer", "Jane Doe"},
		{"recruiter with a dash", "Recruiter - Sam Rivera", "Sam Rivera"},
		{"name then a comma", "Contact: Jane Doe, Talent Partner", "Jane Doe"},
		{"name from a later line", "About us\nIntro text\nTalent partner: Sam Rivera.", "Sam Rivera"},
		{"heading on the next line", "Reports to the hiring manager -\nBuild Great Products", ""},
		{"team name", "Please contact - Talent Acquisition", ""},
		{"company", "Contact: Example Corp", ""},
		{"no label", "Jane Doe leads the team", ""},
		{"team before a person", "Contact: Talent Team\nHiring manager: Jane Doe", "Jane Doe"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := recipientIn(c.posting); got != c.want {
				t.Errorf("recipientIn(%q) = %q, want %q", c.posting, got, c.want)
			}
		})
	}
}
