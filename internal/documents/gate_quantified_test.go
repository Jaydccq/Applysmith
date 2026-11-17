package documents

import (
	"strings"
	"testing"

	"applysmith/internal/kb"
	"applysmith/internal/workspace"
)

// Scenario: Altered figure, Moved figure. A quantified claim is a proposition:
// its unit and its subject are part of it, spelling is not, and a clause does
// not lend its figure to its neighbour.
func TestGate_Quantified_PropositionAndOwner(t *testing.T) {
	for _, c := range []struct {
		name, claim, evidence string
		ok                    bool
	}{
		{"thousands separator", "Processed 5000 survey files per day", "Processed 5,000 survey files per day", true},
		{"thousands separator the other way", "Processed 5,000 survey files per day", "Processed 5000 survey files per day", true},
		{"hyphen against space", "Built a 3 step pipeline", "Built a 3-step pipeline", true},
		{"article differs", "Built the 3-step pipeline", "Built a 3 step pipeline", true},
		{"unit is part of the claim", "Processed 5,000 users per day", "Processed 5,000 survey files per day", false},
		{"figure across a semicolon", "Mentored 12 engineers", "Led the migration; mentored four engineers; supported 12 services", false},
		{"figure across but", "Mentored 12 engineers", "Mentored four engineers but supported 12 services", false},
		{"figure across while", "Mentored 12 engineers", "Mentored four engineers while supporting 12 services", false},
		{"figure across whereas", "Mentored 12 engineers", "Mentored four engineers whereas the team supported 12 services", false},
		{"each clause keeps its own figure", "Supported 12 services", "Mentored four engineers; supported 12 services", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := claimOK(c.claim, c.evidence); got != c.ok {
				t.Errorf("claimOK(%q | %q) = %v, want %v", c.claim, c.evidence, got, c.ok)
			}
		})
	}

	// A figure moves only with its own project.
	m := manifestFor(t, testCV, testPosting)
	if len(m.SelectedProjects) == 0 {
		t.Fatal("the fixture selects no project")
	}
	project := m.SelectedProjects[0]
	d := docWith(m, "X1", "Led the platform migration and parsed 5K+ survey files per day", evID(t, m, "platform migration"))
	if g := checkResume(m, d); !hasCode(g, "unsupported-figure") {
		t.Errorf("a project's figure in an employment bullet: %+v", g.Errors)
	}
	own := checkBulletOnly(m, project, "Built a Go service that parsed 5K+ survey files per day", evID(t, m, "5K+ survey"))
	if !own.OK() {
		t.Errorf("a project keeps its own figure: %+v", own.Errors)
	}
}

// Scenario: Metadata is not exempt. The contact block comes from the profile
// and is never draft text, so the only lines a draft can put near it are its
// summary and skills lines, and those are held to the evidence like a bullet.
func TestGate_Quantified_MetadataLinesNotExempt(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	mig := evID(t, m, "platform migration")
	for _, c := range []struct {
		name, summary string
		code          string
	}{
		{"contact values do not launder a metric", "Reach me at alex.example@example.com or +1 555 0100; I cut costs by 90%.", "unsupported-figure"},
		{"education dates do not launder a metric", "BSc in Mathematics, Example University, 2011-2015, and 300% revenue growth.", "unsupported-figure"},
		{"achievement prose before the first heading", "Grew the business by 300% in one year.", "unsupported-figure"},
		{"a figure the evidence holds", "Backend engineer who cut incidents by 40% across 12 services.", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := docWith(m, "X1", "Led the platform migration and cut incidents by 40% across 12 services", mig)
			d.Profile = c.summary
			g := checkResume(m, d)
			if c.code != "" && !hasCode(g, c.code) {
				t.Errorf("errors = %+v, want %s", g.Errors, c.code)
			}
			if c.code == "" && hasCode(g, "unsupported-figure") {
				t.Errorf("errors = %+v", g.Errors)
			}
		})
	}
}

// Scenario: Repeated evidence keeps its owner. A knowledge snippet that repeats
// a project's bullet is evidence of its own and does not become the figure of
// an employment entry.
func TestGate_Quantified_RepeatedEvidenceKeepsOwner(t *testing.T) {
	p := testPosting
	snippet := kb.Fact{ID: "snippet:1", SourceKind: kb.KindSnippet, SourceRef: "knowledge/projects/reef-survey.md#Reef Survey Tool",
		Text: "- Built a Go service that parsed 5K+ survey files per day\n"}
	prof, rep := workspace.ParseProfile([]byte(testProfile))
	if !rep.OK() {
		t.Fatalf("profile: %+v", rep)
	}
	m, err := buildManifest(sourceSet{Profile: prof, ProfileBytes: []byte(testProfile), CV: testCV, Posting: p, Company: "Example Corp", Role: "Backend Engineer",
		Snippets: []kb.Fact{snippet}}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]bool{}
	for _, e := range m.Evidence {
		if strings.Contains(e.Text, "5K+ survey files") {
			owners[e.Entry] = true
		}
	}
	if len(owners) == 0 || owners[""] {
		t.Fatalf("the repeated line has no owner entry: %v", owners)
	}
	for owner := range owners {
		if en, ok := m.entry(owner); !ok || en.Kind != EntryProject {
			t.Errorf("the repeated line is owned by %q, not by a project", owner)
		}
	}
	d := docWith(m, "X1", "Led the platform migration and parsed 5K+ survey files per day", evID(t, m, "platform migration"))
	if g := checkResume(m, d); !hasCode(g, "unsupported-figure") {
		t.Errorf("the project's figure moved to an employment entry: %+v", g.Errors)
	}
}
