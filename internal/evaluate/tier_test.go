// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"regexp"
	"strings"
	"testing"
)

func TestTier_NormalizeCompany(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Example Corp, Inc.", "example"},
		{"EXAMPLE CORPORATION", "example"},
		{"Café Partners LLC", "cafe partners"},
		{"Example Systems (formerly Old Name)", "example systems"},
		{"Inc", "inc"},
		{"Ernst & Young", "ernst and young"},
		{"A.B.C. Ltd.", "a b c"},
		{"  Spaced   Out  ", "spaced out"},
	} {
		if got := NormalizeCompany(c.in); got != c.want {
			t.Errorf("NormalizeCompany(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTier_RosterBeatsText(t *testing.T) {
	if got := ClassifyTier("Goldman Sachs", "Our clients value careful engineering. On behalf of our client."); got != TierDirect {
		t.Fatalf("tier = %s", got)
	}
	if got := ClassifyTier("Robert Half Inc.", "We build things."); got != TierStaffing {
		t.Fatalf("tier = %s", got)
	}
	if got := ClassifyTier("accenture", ""); got != TierConsultancy {
		t.Fatalf("tier = %s", got)
	}
	if got := ClassifyTier("Indeed", ""); got != TierAggregator {
		t.Fatalf("tier = %s", got)
	}
}

func TestTier_TextMarkers(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       Tier
	}{
		{"our client", "Our client, a fintech firm, is hiring.", TierStaffing},
		{"on behalf of our client", "We are recruiting on behalf of our client.", TierStaffing},
		{"contract to hire", "This is a contract-to-hire engagement.", TierStaffing},
		{"corp to corp", "Corp-to-corp or W2 considered.", TierStaffing},
		{"w2 only", "W2 only, no sponsorship.", TierStaffing},
		{"self description", "We are a staffing firm placing engineers.", TierStaffing},
		{"plural clients", "Our clients rely on our platform every day.", TierUnknown},
		{"possessive client", "We care about our client's success.", TierUnknown},
		{"no third party recruiters", "No third-party recruiters or agencies, please.", TierUnknown},
		{"silent", "Build things.", TierUnknown},
		{"hardware test bench", "You will work at a hardware test bench validating boards.", TierUnknown},
		{"evaluation bench", "Join our model evaluation bench team.", TierUnknown},
		{"direct employer recruiting notice", "We are a software company; we do not use recruiting agencies.", TierUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyTier("Unlisted Example Co", c.text); got != c.want {
				t.Fatalf("tier = %s, want %s", got, c.want)
			}
		})
	}
}

func TestTier_PluralClientsOnAnUnrosteredBank(t *testing.T) {
	if got := ClassifyTier("Example National Bank", "Our clients rely on our platform."); got != TierUnknown {
		t.Fatalf("tier = %s", got)
	}
}

func TestTier_NameThatContainsARosteredName(t *testing.T) {
	if _, ok := tierRoster["ust"]; !ok {
		t.Fatal("the test needs UST in the roster")
	}
	for _, name := range []string{"Example Trust Partners", "Just Example", "UST Holdings Group", "Gooogle"} {
		if got := ClassifyTier(name, ""); got != TierUnknown {
			t.Errorf("ClassifyTier(%q) = %s: matching must be exact on the normalized form", name, got)
		}
	}
	if got := ClassifyTier("UST", ""); got != TierConsultancy {
		t.Errorf("exact match failed: %s", got)
	}
}

func TestTier_Roster_NoPersonalSeeds(t *testing.T) {
	// The roster is public knowledge about staffing, consulting and aggregator
	// employers: two columns, a known tier, a plain company name, and no URL,
	// address or tracker figure that would show it was copied from a user's rows.
	known := map[Tier]bool{}
	for _, tier := range Tiers {
		known[tier] = true
	}
	personal := regexp.MustCompile(`(?i)https?:|www\.|@|\d{3,}|report|tracker|applied|interview`)
	for i, line := range splitLines(rosterData) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 2 || !known[Tier(cols[0])] || strings.TrimSpace(cols[1]) == "" {
			t.Errorf("line %d is not `tier<TAB>name`: %q", i+1, line)
			continue
		}
		if personal.MatchString(cols[1]) {
			t.Errorf("line %d looks like personal data: %q", i+1, cols[1])
		}
	}
	for name, want := range map[string]Tier{"Robert Half": TierStaffing, "Accenture": TierConsultancy, "Indeed": TierAggregator} {
		if got := ClassifyTier(name, ""); got != want {
			t.Errorf("%s is %s, want the public classification %s", name, got, want)
		}
	}
}
