// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"fmt"
	"testing"

	"applysmith/internal/gates"
)

func must(label, class string) Item {
	return Item{ID: "m1", Label: label, Source: "posting", Hit: 0, GateClass: class}
}

func checkOne(it Item, c CheckContext) Checked {
	return Check(DeepOutput{Must: []Item{it}}, c)
}

func TestCheck_UnsupportedHit(t *testing.T) {
	ck := Check(DeepOutput{
		Must:      []Item{{ID: "m1", Label: "Go", Hit: 1, Evidence: ""}, {ID: "m2", Label: "SQL", Hit: 1, Evidence: "CV: six years of SQL"}},
		Nice:      []Item{{ID: "n1", Label: "Rust", Hit: 0.5, Evidence: "seems like a quick learner"}},
		Alignment: []Item{{ID: "domain", Label: "Domain", Hit: 1, Evidence: "knowledge:story-12 covers clinics"}},
	}, CheckContext{})
	if ck.Must[0].Hit != 0 || ck.Must[1].Hit != 1 || ck.Nice[0].Hit != 0 || ck.Align[0].Hit != 1 {
		t.Fatalf("checked = %+v", ck)
	}
	if len(ck.Unsupported) != 2 || ck.Unsupported[0] != "m1" || ck.MustMisses != 1 {
		t.Fatalf("unsupported = %v misses = %d", ck.Unsupported, ck.MustMisses)
	}
}

// A live deep-eval reply cited the profile in the source field ("source":
// "CV") and wrote alignment evidence such as "Alex holds a Senior Platform
// Engineer title"; those hits are cited. A posting quote in the source field,
// even one that mentions a résumé, is not a citation.
func TestCheck_ProfileSourceFieldIsACitation(t *testing.T) {
	ck := Check(DeepOutput{
		Alignment: []Item{
			{ID: "seniority", Label: "Seniority fit", Source: "CV", Hit: 1, Evidence: "Alex holds a Senior Platform Engineer title."},
			{ID: "domain", Label: "Domain", Source: " narrative ", Hit: 0.5, Evidence: "Adjacent payments work."},
			{ID: "location", Label: "Location", Source: "Please attach your resume and cover letter", Hit: 1, Evidence: "Lives nearby."},
		},
	}, CheckContext{})
	if ck.Align[0].Hit != 1 || ck.Align[1].Hit != 0.5 || ck.Align[2].Hit != 0 {
		t.Fatalf("checked = %+v", ck.Align)
	}
	if len(ck.Unsupported) != 1 || ck.Unsupported[0] != "location" {
		t.Fatalf("unsupported = %v", ck.Unsupported)
	}
}

// Alignment always averages the same four dimensions: a repeat, an unknown id
// and the reply's order change nothing. "targets:" names the profile's
// targets as the source.
func TestCheck_AlignmentDimensions(t *testing.T) {
	ck := Check(DeepOutput{Alignment: []Item{
		{ID: "role", Label: "Role", Hit: 1, Evidence: "targets: Backend Engineer"},
		{ID: "compensation_fit", Label: "Pay", Hit: 1, Evidence: "CV"},
		{ID: "seniority", Label: "Level", Hit: 0.5, Evidence: "CV: nine years"},
		{ID: "role", Label: "Role again", Hit: 0, Evidence: ""},
		{ID: "location", Label: "Location", Hit: 1, Evidence: "targets: remote"},
	}}, CheckContext{})
	var ids []string
	for _, it := range ck.Align {
		ids = append(ids, it.ID)
	}
	if got := fmt.Sprint(ids); got != "[seniority location role]" || ck.Align[2].Hit != 1 || len(ck.Unsupported) != 0 {
		t.Fatalf("alignment = %+v unsupported = %v", ck.Align, ck.Unsupported)
	}
	if NamesProfileSource("the posting targets senior engineers") {
		t.Fatal("the word targets alone is not a source")
	}
}

func TestCheck_GateClasses(t *testing.T) {
	five := ptr(3.0)
	for _, c := range []struct {
		name  string
		item  Item
		ctx   CheckContext
		honor bool
	}{
		{"skill gap mislabeled as education", must("Go proficiency", GateEducation), CheckContext{}, false},
		{"degree label", must("Bachelor's degree in computer science", GateEducation), CheckContext{}, true},
		{"noun-form work authorization", must("Legal authority to work in the country", GateWorkAuth), CheckContext{}, true},
		{"citizenship label", must("US citizenship", GateWorkAuth), CheckContext{}, true},
		{"acronym matched case-sensitively", must("Eligible for OPT", GateWorkAuth), CheckContext{}, true},
		{"lowercase opt is not the acronym", must("Opt-in to on-call rotation", GateWorkAuth), CheckContext{}, false},
		{"itar inside military is not the acronym", must("Military experience", GateWorkAuth), CheckContext{}, false},
		{"restricted stance counts", must("Eligibility to be hired", GateWorkAuth), CheckContext{Stance: gates.StanceRestricted}, true},
		{"refuses stance counts", must("Candidate profile", GateWorkAuth), CheckContext{Stance: gates.StanceRefuses}, true},
		{"certification example is not a license", must("Security certifications (for example CEH)", GateLicense), CheckContext{}, false},
		{"license wording", must("Active professional engineer license", GateLicense), CheckContext{}, true},
		{"clearance", must("Active TS/SCI clearance", GateClearance), CheckContext{}, true},
		{"clearance class without clearance wording", must("Backend experience", GateClearance), CheckContext{}, false},
		{"experience above the profile", must("10+ years of backend experience", GateExperience), CheckContext{ProfileYears: five}, true},
		{"experience within the profile", must("2 years of backend experience", GateExperience), CheckContext{ProfileYears: five}, false},
		{"experience with unknown profile years", must("10+ years of backend experience", GateExperience), CheckContext{}, false},
		{"program eligibility", must("Veteran status", GateProgram), CheckContext{}, true},
		{"program without wording", must("Strong writing", GateProgram), CheckContext{}, false},
		{"language fluency", must("Fluent Japanese", GateLanguage), CheckContext{}, true},
		{"language without wording", must("Japanese market knowledge", GateLanguage), CheckContext{}, false},
		{"onsite", must("Willing to relocate to Austin", GateOnsite), CheckContext{}, true},
		{"onsite without wording", must("Team player", GateOnsite), CheckContext{}, false},
		{"class none", must("Go proficiency", GateNone), CheckContext{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ck := checkOne(c.item, c.ctx)
			if ck.HonoredGate != c.honor {
				t.Fatalf("honored = %v, demotions = %+v", ck.HonoredGate, ck.Demotions)
			}
			if c.item.GateClass != GateNone && !c.honor {
				if len(ck.Demotions) != 1 || ck.Demotions[0].Rule != CapGateDemoted || ck.Demotions[0].Value != 5.0 {
					t.Fatalf("a demotion must be recorded with cap 5.0: %+v", ck.Demotions)
				}
			}
			if ck.MustMisses != 1 {
				t.Fatalf("a demoted gate still counts as an ordinary miss, misses = %d", ck.MustMisses)
			}
		})
	}
}

func TestCheck_DegreeWordingElsewhereDoesNotHonorTheGate(t *testing.T) {
	// The posting says a bachelor's degree is preferred; the requirement's own
	// label has no degree wording, so the gate is still demoted.
	it := must("Go proficiency", GateEducation)
	it.Source = "Go proficiency is required"
	if ck := checkOne(it, CheckContext{}); ck.HonoredGate {
		t.Fatal("the posting's other sentences must not trigger the class")
	}
}

func TestCheck_MetMustHaveCarriesNoGate(t *testing.T) {
	it := must("US citizenship", GateWorkAuth)
	it.Hit, it.Evidence = 1, "CV: US citizen"
	ck := checkOne(it, CheckContext{})
	if ck.HonoredGate || ck.MustMisses != 0 || len(ck.Demotions) != 0 {
		t.Fatalf("checked = %+v", ck)
	}
}

func TestCheck_WorkAuthMustHaveFollowsSponsorNeed(t *testing.T) {
	workAuth := func(hit float64, evidence string) Item {
		return Item{ID: "m1", Label: "Authorized to work in the United States", Source: "posting", Hit: hit, Evidence: evidence, GateClass: GateWorkAuth}
	}
	for _, c := range []struct {
		name       string
		item       Item
		need       Need
		stance     gates.Stance
		wantHit    float64
		wantMisses int
		wantGate   bool
		wantUnsup  int
	}{
		{"not needed: a citation of the profile block is met", workAuth(1, "Profile work authorization: authorized in US"), NeedNotNeeded, gates.StanceSilent, 1, 0, false, 0},
		{"not needed: a miss reported by the model is still met", workAuth(0, ""), NeedNotNeeded, gates.StanceRefuses, 1, 0, false, 0},
		{"not needed: a restricted stance does not gate", workAuth(0, "none"), NeedNotNeeded, gates.StanceRestricted, 1, 0, false, 0},
		{"unanswered: no gate, no miss, an unknown", workAuth(0, ""), NeedUnanswered, gates.StanceRestricted, 0.5, 0, false, 0},
		{"unanswered: a profile citation is not zeroed as unsupported", workAuth(1, "Profile work authorization: unanswered"), NeedUnanswered, gates.StanceSilent, 0.5, 0, false, 0},
		{"needed: the stance still gates", workAuth(0, ""), NeedNeeded, gates.StanceRestricted, 0, 1, true, 0},
		{"needed: an uncited hit is still unsupported", workAuth(1, "Profile work authorization: needs sponsorship"), NeedNeeded, gates.StanceSilent, 0, 1, true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			ck := Check(DeepOutput{Must: []Item{c.item}}, CheckContext{Stance: c.stance, Need: c.need})
			if ck.Must[0].Hit != c.wantHit || ck.MustMisses != c.wantMisses || ck.HonoredGate != c.wantGate || len(ck.Unsupported) != c.wantUnsup {
				t.Fatalf("hit %v misses %d gate %v unsupported %v", ck.Must[0].Hit, ck.MustMisses, ck.HonoredGate, ck.Unsupported)
			}
		})
	}
}

func TestCheck_AuthorizedUsersStrongMatchIsNotCapped(t *testing.T) {
	out := DeepOutput{Must: []Item{
		{ID: "m1", Label: "Authorized to work in the United States", Hit: 1, Evidence: "Profile work authorization: authorized in US, no sponsorship needed", GateClass: GateWorkAuth},
		{ID: "m2", Label: "5 years of Go", Hit: 1, Evidence: "CV: six years of Go"},
		{ID: "m3", Label: "Distributed systems", Hit: 1, Evidence: "CV: built distributed services"},
	}}
	ck := Check(out, CheckContext{Stance: gates.StanceSilent, Need: NeedNotNeeded})
	sc := Compute(ScoreInput{Must: Levels(ck.Must), Align: []float64{1, 1}, Hidden: []float64{1}, Comp: 5, MustMisses: ck.MustMisses, HonoredGate: ck.HonoredGate, Notes: ck.Demotions})
	if sc.Score <= 1.5 || len(sc.Caps) != 0 {
		t.Fatalf("score %.1f caps %+v", sc.Score, sc.Caps)
	}
}

// Lever and Ashby readers know only the board slug; the report shows the name
// the posting states (live: "palantir" -> "Palantir").
func TestDisplayCompany_SlugGivesWayToStatedName(t *testing.T) {
	for _, c := range []struct{ read, stated, want string }{
		{"palantir", "Palantir", "Palantir"},
		{"1password", "1Password", "1Password"},
		{"base-power", "Base Power Company", "Base Power Company"},
		{"Mercury", "Mercury Technologies", "Mercury"},
		{"acme", "Globex", "acme"},
		{"", "Globex", "Globex"},
		{"palantir", "", "palantir"},
	} {
		if got := displayCompany(c.read, c.stated); got != c.want {
			t.Errorf("displayCompany(%q, %q) = %q, want %q", c.read, c.stated, got, c.want)
		}
	}
}
