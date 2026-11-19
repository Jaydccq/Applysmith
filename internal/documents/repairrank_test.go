package documents

import (
	"slices"
	"strings"
	"testing"

	"applysmith/internal/documents/assets"
)

// rankManifest has two jobs, a project and two skill lines. R1 is backed by E4
// and E7, R2 by E5, R3 by E3; E1, E2, E6 and the skill lines back none.
func rankManifest(t *testing.T) Manifest {
	t.Helper()
	ev := func(id, entry, text string) Evidence {
		return Evidence{ID: id, Source: "cv.md#" + entry, Kind: "cv", Entry: entry, Text: text}
	}
	m := Manifest{Version: ManifestVersion, Mode: ModeTailored, Company: "Example Corp", Role: "Backend Engineer",
		Contact:      Contact{FullName: "Alex Example", Email: "alex.example@example.com"},
		Requirements: []Requirement{{ID: "R1", Text: "Build backend services in Go"}, {ID: "R2", Text: "Model data in PostgreSQL"}, {ID: "R3", Text: "Mentor engineers"}},
		Evidence: []Evidence{
			ev("E1", "X1", "Wrote weekly status notes for the planning group"), ev("E2", "X1", "Reviewed pull requests from the platform team"),
			ev("E3", "X1", "Mentored four engineers in code review"), ev("E4", "X2", "Built backend services in Go used by twelve teams"),
			ev("E5", "X2", "Modelled reporting data in PostgreSQL for planners"), ev("E6", "X2", "Organized the quarterly planning calendar"),
			ev("E7", "P1", "Parsed survey files with a small Go service"),
			{ID: "E8", Source: "cv.md#Skills", Kind: "cv", Text: "Languages: Go, SQL"}, {ID: "E9", Source: "cv.md#Skills", Kind: "cv", Text: "Tools: PostgreSQL, Docker"},
		},
		Entries: []Entry{
			{Key: "X1", Kind: EntryExperience, Title: "Senior Engineer", Organization: "Example Systems", Start: "2021-03", End: "present", Evidence: []string{"E1", "E2", "E3"}},
			{Key: "X2", Kind: EntryExperience, Title: "Engineer", Organization: "Sample Works", Start: "2018-01", End: "2021-02", Evidence: []string{"E4", "E5", "E6"}},
			{Key: "P1", Kind: EntryProject, Title: "Reef Survey Tool", Evidence: []string{"E7"}},
		},
		SelectedEntries: []string{"X1", "X2"}, SelectedProjects: []string{"P1"},
		Selected: []string{"E1", "E2", "E3", "E4", "E5", "E6", "E7", "E8", "E9"},
		Coverage: []Coverage{{Requirement: "R1", Evidence: []string{"E4", "E7"}}, {Requirement: "R2", Evidence: []string{"E5"}}, {Requirement: "R3", Evidence: []string{"E3"}}},
	}
	if err := m.Freeze(); err != nil {
		t.Fatal(err)
	}
	return m
}

func bulletOf(m Manifest, id string) Bullet {
	e, _ := m.evidence(id)
	return Bullet{Text: e.Text, Evidence: []string{id}}
}

func rankResume(m Manifest) ResumeDoc {
	return ResumeDoc{Schema: docSchema, Generation: GenDeterministic, ManifestHash: m.Hash, Language: "en",
		ResumeDraft: ResumeDraft{
			Competencies: []Competency{{Label: "Languages", Text: "Go, SQL", Evidence: []string{"E8"}}, {Label: "Tools", Text: "PostgreSQL, Docker", Evidence: []string{"E9"}}},
			Experience: []EntryDraft{{Entry: "X1", Bullets: []Bullet{bulletOf(m, "E1"), bulletOf(m, "E2"), bulletOf(m, "E3")}},
				{Entry: "X2", Bullets: []Bullet{bulletOf(m, "E4"), bulletOf(m, "E5"), bulletOf(m, "E6")}}},
			Projects: []EntryDraft{{Entry: "P1", Bullets: []Bullet{bulletOf(m, "E7")}}},
		}}
}

func rankLetter(m Manifest, opening []string, bullets ...Bullet) *LetterDoc {
	return &LetterDoc{Schema: docSchema, Generation: GenDeterministic, ManifestHash: m.Hash, Language: "en", Salutation: "Dear Hiring Team,",
		LetterDraft: LetterDraft{Opening: "I am writing to apply for the Backend Engineer role at Example Corp.", OpeningEvidence: opening,
			Body: "The work most relevant to this role is below.", Bullets: bullets,
			Closing: "I would welcome the opportunity to discuss how this experience can support Example Corp. Thank you for your time and consideration."}}
}

// ranked lists the cuttable lines in removal order, by the evidence they cite.
func ranked(m Manifest, d ResumeDoc, l *LetterDoc) (cs []candidate, ids []string) {
	cs = resumeCandidates(m, d, l)
	rankOrder(cs)
	for _, c := range cs {
		ids = append(ids, strings.Join(c.evidence, "+"))
	}
	return
}

func TestRank_OrdersByRequirementsThenUniquenessThenLetterDependency(t *testing.T) {
	m := rankManifest(t)
	d := rankResume(m)
	// Lines that back no requirement go first, the later printed one first among equals.
	if _, got := ranked(m, d, nil); !slices.Equal(got, []string{"E6", "E2", "E1", "E9", "E8", "E5", "E4", "E3"}) {
		t.Fatalf("removal order = %v", got)
	}
	// A line whose evidence another line repeats carries nothing unique.
	d.Experience[0].Bullets = append(d.Experience[0].Bullets, Bullet{Text: "Reviewed pull requests again", Evidence: []string{"E2"}})
	if cs, _ := ranked(m, d, nil); cs[0].rank.Unique != 0 || cs[0].evidence[0] != "E2" {
		t.Fatalf("first = %+v, want a duplicate of E2", cs[0])
	}
	// A line the letter relies on goes after an otherwise equal one.
	cs, ids := ranked(m, rankResume(m), rankLetter(m, nil, bulletOf(m, "E6")))
	e6, e2 := slices.Index(ids, "E6"), slices.Index(ids, "E2")
	if !(e2 < e6) || !cs[e6].rank.Dependent || cs[e2].rank.Dependent {
		t.Fatalf("E2 at %d, E6 at %d (dependent %v)", e2, e6, cs[e6].rank.Dependent)
	}
}

// A requirement's proof outranks a figure that backs no requirement; among
// lines that back as many requirements, an entry's only quantified result goes
// last. (A filler bullet with a figure had cut a job's only SQL proof.)
func TestRank_RequirementsBeforeTheOnlyQuantifiedResult(t *testing.T) {
	m := rankManifest(t)
	d := rankResume(m)
	d.Experience[0].Bullets = []Bullet{{Text: "Wrote 52 weekly status notes for the planning group", Evidence: []string{"E1"}}, bulletOf(m, "E2")}
	d.Experience[1].Bullets = []Bullet{bulletOf(m, "E5"), {Text: "Organized 40 planning sessions a year", Evidence: []string{"E6"}}}
	cs, ids := ranked(m, d, nil)
	e1, e5, e6 := slices.Index(ids, "E1"), slices.Index(ids, "E5"), slices.Index(ids, "E6")
	if !cs[e6].rank.OnlyQuantified || cs[e5].rank.Requirements != 1 || !(e6 < e5) {
		t.Fatalf("removal order = %v; the requirement-free figure must go before the proof", ids)
	}
	if !cs[e1].rank.OnlyQuantified || cs[e1].rank.Requirements != 0 {
		t.Fatalf("E1 rank = %+v", cs[e1].rank)
	}
	for i, c := range cs {
		if c.rank.Requirements == 0 && !c.rank.OnlyQuantified && i > e1 {
			t.Fatalf("removal order = %v; X1's only figure must outlast the lines that back no requirement", ids)
		}
	}
}

// Cutting to exhaustion leaves each entry's last bullet and the last
// competency; contact values are not content and nothing is invented.
func TestRepairCuts_NeverRemoveRequiredLabelsOrContactValues(t *testing.T) {
	m := rankManifest(t)
	d := rankResume(m)
	for n := 0; n < 50; n++ {
		cs, _ := ranked(m, d, nil)
		if len(cs) == 0 {
			break
		}
		d = removeFromResume(d, cs[0])
		if g := checkResume(m, d); !g.OK() {
			t.Fatalf("after %d cuts the gate fails: %v", n+1, g.Errors)
		}
	}
	if len(d.Competencies) != 1 {
		t.Errorf("competencies left = %d", len(d.Competencies))
	}
	for _, list := range [][]EntryDraft{d.Experience, d.Projects} {
		for _, e := range list {
			if len(e.Bullets) != 1 {
				t.Errorf("entry %s keeps %d bullets, want 1", e.Entry, len(e.Bullets))
			}
		}
	}
	r, err := buildResume(m, d, assets.PaperA4)
	if err != nil || r.Email != m.Contact.Email || r.LastName != "Example" || len(r.Experience) != 2 || len(r.Projects) != 1 {
		t.Errorf("rendered data = %+v, %v", r, err)
	}
	have := map[string]bool{}
	for _, l := range rankResume(m).lines() {
		have[l] = true
	}
	for _, l := range d.lines() {
		if !have[l] {
			t.Errorf("line %q was not in the draft", l)
		}
	}
}

func TestLetterEffect_KeepsAnEquivalentReferenceOrRemovesTheClaim(t *testing.T) {
	m := rankManifest(t)
	d := rankResume(m)
	d.Experience[1].Bullets = append(d.Experience[1].Bullets, Bullet{Text: "Planned data models in PostgreSQL", Evidence: []string{"E5"}})
	l := rankLetter(m, []string{"E4"}, bulletOf(m, "E5"), bulletOf(m, "E3"))
	cut := func(id string, index int) letterEffect {
		for _, c := range resumeCandidates(m, d, l) {
			if c.evidence[0] == id && (index < 0 || c.index == index) {
				return effectOnLetter(d, removeFromResume(d, c), l)
			}
		}
		t.Fatalf("no candidate %s", id)
		return letterEffect{}
	}
	// E5 is cited twice: the letter bullet keeps the other citation.
	if eff := cut("E5", 1); !eff.acceptable || len(eff.letter.Bullets) != 2 || !strings.Contains(strings.Join(eff.notes, "|"), "keeps its reference E5") {
		t.Fatalf("equivalent reference: %+v", eff)
	}
	// E3 is the only line behind a letter bullet: the claim goes with it.
	eff := cut("E3", -1)
	if !eff.acceptable || len(eff.letter.Bullets) != 1 || eff.letter.Bullets[0].Evidence[0] != "E5" || !strings.Contains(strings.Join(eff.notes, "|"), "was removed") {
		t.Fatalf("lost claim: %+v", eff)
	}
	if g := checkLetter(m, *eff.letter); !g.OK() {
		t.Fatalf("letter gate: %v", g.Errors)
	}
	// The opening cannot be rewritten without a model: stripping its reference is refused.
	if eff := cut("E4", -1); eff.acceptable {
		t.Fatalf("a cut that strips the opening's only reference was accepted: %+v", eff)
	}
}
