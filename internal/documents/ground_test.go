package documents

import (
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

// testManifest builds the frozen manifest of the synthetic persona.
func testManifest(t *testing.T) Manifest {
	t.Helper()
	p, rep := workspace.ParseProfile([]byte(testProfile))
	if !rep.OK() {
		t.Fatalf("profile: %+v", rep)
	}
	m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(testProfile), CV: testCV, Posting: testPosting, Company: "Example Corp", Role: "Backend Engineer"}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func evID(t *testing.T, m Manifest, contains string) string {
	t.Helper()
	for _, e := range m.Evidence {
		if strings.Contains(e.Text, contains) {
			return e.ID
		}
	}
	t.Fatalf("no evidence containing %q", contains)
	return ""
}

func docWith(m Manifest, entry, text string, ids ...string) ResumeDoc {
	d := ResumeDoc{Schema: docSchema, ManifestHash: m.Hash, Language: "en", ResumeDraft: ResumeDraft{
		Experience: []EntryDraft{{Entry: entry, Bullets: []Bullet{{Text: text, Evidence: ids}}}}}}
	for _, k := range m.SelectedProjects {
		e, _ := m.entry(k)
		d.Projects = append(d.Projects, EntryDraft{Entry: k, Bullets: []Bullet{{Text: "Built a Go service that parsed 5K+ survey files per day", Evidence: e.Evidence[:1]}}})
	}
	return d
}

func hasCode(g Gate, code string) bool {
	for _, f := range g.Errors {
		if f.Code == code {
			return true
		}
	}
	return false
}

func hasWarning(g Gate, code string) bool {
	for _, w := range g.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestGroundingQuantifiedClaims(t *testing.T) {
	m := testManifest(t)
	mig := evID(t, m, "platform migration")
	ingest := evID(t, m, "ingestion")
	cases := []struct {
		name, text string
		ids        []string
		entry      string
		code       string // empty: must pass
	}{
		{"same figure", "Led the platform migration and cut incidents by 40% across 12 services", []string{mig}, "X1", ""},
		{"altered figure", "Led the platform migration and cut incidents by 28% across 12 services", []string{mig}, "X1", "unsupported-figure"},
		{"altered unit", "Cut incidents by 40 teams across 12 services", []string{mig}, "X1", "unsupported-figure"},
		{"moved evidence", "Cut incidents by 40%", []string{ingest}, "X1", "moved-evidence"},
		{"figure of another entry", "Owned on-call for a 99.9% availability target", nil, "X1", "unsupported-figure"},
		// The measured résumés bolded only the number; the noun after it is still its unit.
		{"bold number before its noun", "Led the platform migration and cut incidents by 40% across **12** services", []string{mig}, "X1", ""},
		{"bold number with another noun", "Led the platform migration and cut incidents by 40% across **12** teams", []string{mig}, "X1", "unsupported-figure"},
		{"unknown technology name", "Led the platform migration onto Kubernetes", []string{mig}, "X1", "unsupported-name"},
		{"unknown evidence", "Led the migration", []string{"E999"}, "X1", "unknown-evidence"},
		{"internal label", "Result:\nit worked", []string{mig}, "X1", "artifact-label"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := checkResume(m, docWith(m, c.entry, c.text, c.ids...))
			if c.code == "" {
				if !g.OK() {
					t.Errorf("errors = %+v", g.Errors)
				}
				return
			}
			if !hasCode(g, c.code) {
				t.Errorf("errors = %+v, want %s", g.Errors, c.code)
			}
		})
	}
}

func TestGroundingBindsTheManifest(t *testing.T) {
	m := testManifest(t)
	bad := docWith(m, "P9", "x")
	if g := checkResume(m, bad); !hasCode(g, "unknown-entry") {
		t.Errorf("unknown entry: %+v", g.Errors)
	}
	d := docWith(m, "X1", "Led the platform migration", evID(t, m, "platform migration"))
	d.ManifestHash = "stale"
	if g := checkResume(m, d); !hasCode(g, "manifest-mismatch") {
		t.Errorf("stale hash: %+v", g.Errors)
	}
}

func TestGroundingDuplicateAndSize(t *testing.T) {
	m := testManifest(t)
	long := "Led the platform migration and cut incidents by 40% across 12 services in the data platform group"
	id := evID(t, m, "platform migration")
	d := ResumeDoc{Schema: docSchema, ManifestHash: m.Hash, Language: "en", ResumeDraft: ResumeDraft{Experience: []EntryDraft{{Entry: "X1", Bullets: []Bullet{
		{Text: long, Evidence: []string{id}}, {Text: "Mentored four engineers", Evidence: []string{evID(t, m, "Mentored")}}, {Text: long, Evidence: []string{id}}}}}}}
	if g := checkResume(m, d); !hasCode(g, "duplicated-line") {
		t.Errorf("duplicate: %+v", g.Errors)
	}
	d.Experience[0].Bullets = []Bullet{{Text: strings.Repeat("Mentored four engineers. ", 700), Evidence: []string{evID(t, m, "Mentored")}}}
	if g := checkResume(m, d); !hasCode(g, "too-long") {
		t.Errorf("size: %+v", g.Errors)
	}
}

func letterDoc(m Manifest, opening, closing string) LetterDoc {
	return LetterDoc{Schema: docSchema, ManifestHash: m.Hash, Language: "en", Salutation: "Dear Hiring Team,",
		LetterDraft: LetterDraft{Opening: opening, Body: "", Closing: closing, OpeningEvidence: []string{}, CompanyFacts: []string{}, Bullets: []Bullet{}}}
}

func TestLetterGate(t *testing.T) {
	m := testManifest(t)
	good := "I am writing to apply for the Backend Engineer role at Example Corp, where backend services in Go are central to the work."
	cases := []struct{ name, opening, closing, code string }{
		{"passes", good, "I would welcome a conversation. Thank you for reading.", ""},
		{"too short", "Hello.", "Bye.", "too-short"},
		{"cut off", good, "I have supported 5K+ users and 12M+", "cut-off"},
		{"ellipsis", good + " And more...", "Thank you.", "cut-off"},
		{"generic", "I have a strong interest in this opportunity and I am excited by your mission to improve everything for everybody.", "I would be glad to talk about it soon. Thank you for reading.", "generic"},
		{"invented figure", good + " I doubled revenue by 300%.", "Thank you for reading.", "unsupported-figure"},
		{"relocation by draft", good + " I will relocate next month.", "Thank you for reading.", "relocation-claim"},
		// From a refused draft of the letter measurement: the posting asked for it, no source says it.
		{"right to work by draft", good, "I am eligible to work in the United States and based in Atlanta, Georgia. Thank you for reading.", "work-authorization-claim"},
		{"sponsorship by draft", good, "I am authorized to work here and need no sponsorship. Thank you for reading.", "work-authorization-claim"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := checkLetter(m, letterDoc(m, c.opening, c.closing))
			if c.code == "" && !g.OK() {
				t.Fatalf("errors = %+v", g.Errors)
			}
			if c.code != "" && !hasCode(g, c.code) {
				t.Errorf("errors = %+v, want %s", g.Errors, c.code)
			}
		})
	}
}

// A body that ends in a colon introduces its bullets (a refused draft of
// the letter measurement); without bullets under it, the colon is a cut-off paragraph.
func TestLetterGate_BodyColonIntroducesTheBullets(t *testing.T) {
	m := testManifest(t)
	l := letterDoc(m, "I am writing to apply for the Backend Engineer role at Example Corp, where backend services in Go are central to the work.", "I would welcome a conversation. Thank you for reading.")
	l.Body = "My work has centred on the parts of a system that quietly carry the most weight, from release pipelines to billing. A few results from that work:"
	if g := checkLetter(m, l); !hasCode(g, "cut-off") {
		t.Errorf("a colon with no bullets under it passed: %+v", g.Errors)
	}
	l.Bullets = []Bullet{{Text: "Mentored four engineers in code review and design.", Evidence: []string{evID(t, m, "Mentored four")}}}
	if g := checkLetter(m, l); !g.OK() {
		t.Errorf("errors = %+v", g.Errors)
	}
}

func TestLetterGateRejectsStuffedSentence(t *testing.T) {
	m := testManifest(t)
	m.CompanyFacts = []CompanyFact{
		{ID: "C1", Text: "Example Corp builds reef survey drones for coastal agencies", Source: "https://example.com/about"},
		{ID: "C2", Text: "Example Corp operates regional data centres across Exampleland", Source: "https://example.com/about"},
		{ID: "C3", Text: "Example Corp published open sensor standards last winter", Source: "https://example.com/blog"},
		{ID: "C4", Text: "Example Corp employs hardware engineers near Sampleville", Source: "https://example.com/jobs"},
	}
	if err := m.Freeze(); err != nil {
		t.Fatal(err)
	}
	stuffed := "I admire that Example Corp builds reef survey drones for coastal agencies, operates regional data centres across Exampleland, published open sensor standards last winter and employs hardware engineers near Sampleville."
	g := checkLetter(m, letterDoc(m, stuffed, "Thank you for reading this letter."))
	if !hasCode(g, "stuffed-sentence") {
		t.Errorf("errors = %+v", g.Errors)
	}
}

func TestManifestFreezeAndBinding(t *testing.T) {
	m := testManifest(t)
	if err := m.Verify(); err != nil {
		t.Fatal(err)
	}
	m.Selected = append(m.Selected, "E999")
	if err := m.Verify(); err == nil {
		t.Fatal("a changed manifest must not verify")
	}
	a, b := testManifest(t), testManifest(t)
	if a.Hash != b.Hash {
		t.Error("the same inputs must freeze to the same manifest")
	}
	for _, id := range a.Selected {
		e, _ := a.evidence(id)
		if e.Source == "" {
			t.Errorf("selected evidence %s has no source", id)
		}
	}
	if len(a.SelectedProjects) > 3 {
		t.Errorf("selected projects = %d", len(a.SelectedProjects))
	}
}

// Alnylam (Greenhouse, owner workspace): a general-pool title, "Submit Your
// Resume for Future Opportunities at ...", is ordinary words. The stock
// "welcome the opportunity" of a closing names nothing, while a word of the
// title that claims experience, such as "manufacturing", stays watched.
func TestLetterGate_AGeneralPoolTitleLeavesTheStockClosingAlone(t *testing.T) {
	p, rep := workspace.ParseProfile([]byte(testProfile))
	if !rep.OK() {
		t.Fatalf("profile: %+v", rep)
	}
	role := "Submit Your Resume for Future Opportunities at Example Corp's Norton Manufacturing Site"
	m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(testProfile), CV: testCV, Posting: testPosting, Company: "Example Corp", Role: role}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	opening := "I am writing to apply for the " + role + " role at Example Corp."
	if g := checkLetter(m, letterDoc(m, opening, "I would welcome the opportunity to discuss how my experience can support Example Corp. Thank you for your time and consideration.")); !g.OK() {
		t.Fatalf("the stock closing was refused: %+v", g.Errors)
	}
	if g := checkLetter(m, letterDoc(m, opening, "My manufacturing experience would support Example Corp. Thank you for your time and consideration.")); !hasCode(g, "unsupported-name") {
		t.Fatalf("a title word claimed as experience passed: %+v", g.Errors)
	}
}

// Relativity Space (Greenhouse, owner workspace): the posting's "AM"
// (additive manufacturing) is a name only in capitals, so a letter's "I am
// writing" is the verb, while "my AM experience" still claims the process no
// user source holds.
func TestLetterGate_AnAcronymInCapitalsIsNotItsLowerCaseWord(t *testing.T) {
	p, rep := workspace.ParseProfile([]byte(testProfile))
	if !rep.OK() {
		t.Fatalf("profile: %+v", rep)
	}
	posting := testPosting + "- Experience with AM processes and NDE methods\n"
	m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(testProfile), CV: testCV, Posting: posting, Company: "Example Corp", Role: "Backend Engineer"}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	const opening = "I am writing to apply for the Backend Engineer role at Example Corp, where backend services in Go are central to the work."
	if g := checkLetter(m, letterDoc(m, opening, "I would welcome a conversation. Thank you for reading.")); !g.OK() {
		t.Fatalf("the verb was read as the acronym: %+v", g.Errors)
	}
	if g := checkLetter(m, letterDoc(m, opening, "My AM experience would help the team. Thank you for reading.")); !hasCode(g, "unsupported-name") {
		t.Fatalf("a claimed acronym passed: %+v", g.Errors)
	}
}
