package answerbank

import "testing"

// A US state as the region gives the country; a code that also names a
// country does not, and a country the CV writes stays.
func TestIdentityFromCV_AUSStateGivesTheCountry(t *testing.T) {
	for cv, want := range map[string]string{
		"# Morgan Sample\n\nSeattle, WA | morgan@mailbox.test\n":  "US",
		"# Kai Sample\n\nBerlin, DE | kai@mailbox.test\n":         "",
		"# Ana Sample\n\nAustin, TX, Mexico | ana@mailbox.test\n": "Mexico",
	} {
		if got := IdentityFromCV(cv)["country"]; got != want {
			t.Errorf("%q: country %q, want %q", cv, got, want)
		}
	}
}
