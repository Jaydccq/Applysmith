package kb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBM25_SectionRecallBeatsWholeDoc(t *testing.T) {
	f := newFixture(t)
	long := "Intro about general harbor operations.\n\n## Berth allocation\nBuilt the berth allocation solver with constraint programming.\n\n"
	for i := 0; i < 12; i++ {
		long += fmt.Sprintf("## Filler %d\nCrane maintenance notes number %d and tidal tables.\n\n", i, i)
	}
	f.write("knowledge/projects/long.md", snippet("", long))
	f.write("knowledge/projects/short.md", snippet("", "Wrote unrelated documentation about tidal tables."))
	res := f.search("berth allocation solver")
	if len(res.Facts) == 0 || !strings.HasSuffix(res.Facts[0].SourceRef, "#Berth allocation") {
		t.Fatalf("facts = %v", sourceRefs(res))
	}
}

func TestBM25_RelevanceFloor_TwoTerms(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Migrated the billing database to a new cluster."))
	// Scenario "Single shared word".
	if res := f.search("billing strategy overview"); len(res.Facts) != 0 {
		t.Fatalf("one shared word returned %v", ids(res))
	}
	if res := f.search("billing database"); len(res.Facts) != 1 {
		t.Fatalf("two shared words returned %v", ids(res))
	}
}

func TestBM25_Boosts(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/plain.md", snippet("", "Rolled out the search ranking service."))
	f.write("knowledge/projects/tagged.md", snippet("tags: [search]", "Rolled out the search ranking service."))
	f.write("knowledge/projects/kinded.md", snippet("prefer_for_company_kinds: [startup]", "Rolled out the search ranking service."))
	res := f.search("search ranking service", func(q *Query) { q.Tags = []string{"search"}; q.CompanyKind = "startup" })
	got := snippetIDs(res)
	if len(got) != 3 || got[0] != "projects/kinded" || got[1] != "projects/tagged" || got[2] != "projects/plain" {
		t.Fatalf("order = %v", got)
	}
	by := map[string]float64{}
	for _, fact := range res.Facts {
		by[fact.SourceRef] = fact.Score
	}
	base := by["knowledge/projects/plain.md"]
	if d := by["knowledge/projects/tagged.md"] - base; d < 0.1499 || d > 0.1501 {
		t.Fatalf("tag boost = %v", d)
	}
	if d := by["knowledge/projects/kinded.md"] - base; d < 0.1999 || d > 0.2001 {
		t.Fatalf("company kind boost = %v", d)
	}
}

func TestBM25_TriggerOutranksText(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/behavioral/strong-text.md", snippet("", "Resolved a conflict between two teams over the shared schema conflict resolution process."))
	f.write("knowledge/behavioral/triggered.md", snippet("triggers: [\"tell me about a conflict\"]", "Mediated a roadmap disagreement."))
	res := f.search("Tell me about a conflict resolution you handled")
	got := snippetIDs(res)
	if len(got) < 2 || got[0] != "behavioral/triggered" {
		t.Fatalf("order = %v", got)
	}
	if !hasReason(res.Facts[0], "trigger") {
		t.Fatalf("reasons = %v", res.Facts[0].Reasons)
	}
}

func TestBM25_TriggerPatternRE2(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/behavioral/a.md", snippet("triggers: [\"/(?i)biggest\\\\s+weakness/\"]", "Learned to delegate code reviews."))
	if res := f.search("What is your BIGGEST   weakness"); len(res.Facts) != 1 {
		t.Fatalf("pattern trigger missed: %v", ids(res))
	}
}

func hasReason(f Fact, r string) bool {
	for _, x := range f.Reasons {
		if x == r {
			return true
		}
	}
	return false
}

func TestBM25_ArchetypeBoostAndNoMatchNoop(t *testing.T) {
	f := newFixture(t)
	body := "Dealt with a hard call about the pricing roadmap and the pricing model."
	f.write("knowledge/behavioral/aaa.md", snippet("tags: [ownership]", body))
	f.write("knowledge/behavioral/zzz.md", snippet("tags: [conflict]", "Handled a disagreement on the pricing roadmap."))
	f.write("knowledge/projects/side.md", snippet("", body))
	res := f.search("pricing roadmap model", func(q *Query) { q.ArchetypeTags = []string{"conflict"} })
	got := snippetIDs(res)
	if len(got) == 0 || got[0] != "behavioral/zzz" {
		t.Fatalf("archetype order = %v", got)
	}
	// The other story is demoted below the plain project snippet.
	idx := map[string]int{}
	for i, g := range got {
		idx[g] = i
	}
	if idx["projects/side"] > idx["behavioral/aaa"] {
		t.Fatalf("demotion missing: %v", got)
	}
	// No story carries the tag: the tags change nothing.
	plain := snippetIDs(f.search("pricing roadmap model"))
	noop := snippetIDs(f.search("pricing roadmap model", func(q *Query) { q.ArchetypeTags = []string{"mentoring"} }))
	if !equal(plain, noop) {
		t.Fatalf("a non-matching archetype changed the order: %v vs %v", plain, noop)
	}
}

func TestTokenizer_ScaffoldingAndFolding(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/reflective.md", snippet("", "Describe and explain your experience: I reflected on how the work went."))
	f.write("knowledge/projects/schema.md", snippet("", "Redesigned the database schema and added an index on the orders table."))
	res := f.search("Describe your experience with the schema indexes")
	got := snippetIDs(res)
	if len(got) != 1 || got[0] != "projects/schema" {
		t.Fatalf("results = %v", got)
	}
	// A snippet that shares only scaffolding words scores zero and is absent.
	if res := f.search("Describe and explain your experience"); len(res.Facts) != 0 {
		t.Fatalf("scaffolding-only query returned %v", ids(res))
	}
}

func TestBM25_KDefaultAndCap(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 25; i++ {
		f.write(fmt.Sprintf("knowledge/projects/s%02d.md", i), snippet("", "Operated the telemetry exporter fleet."))
	}
	if n := len(snippetIDs(f.search("telemetry exporter"))); n != 4 {
		t.Fatalf("default k returned %d", n)
	}
	if n := len(snippetIDs(f.search("telemetry exporter", func(q *Query) { q.K = 100 }))); n != 20 {
		t.Fatalf("capped k returned %d", n)
	}
}

func TestRetrieval_CategoryRestriction(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/answer-patterns/a.md", snippet("", "Standard answer about remote work arrangements."))
	f.write("knowledge/experience/e.md", snippet("", "Worked remotely on payment arrangements."))
	res := f.search("remote work arrangements", func(q *Query) { q.Kinds = []string{"projects", "experience", "behavioral"} })
	for _, p := range snippetIDs(res) {
		if strings.HasPrefix(p, "answer-patterns/") {
			t.Fatalf("answer-patterns returned: %v", snippetIDs(res))
		}
	}
	if len(res.Facts) == 0 {
		t.Fatal("restriction returned nothing")
	}
}

// OwnSections are every section of the user's own documents and none of a
// company brief; an unreadable source or a held-out document of the user's
// makes them incomplete (F-10).
func TestOwnSections_OwnDocumentsOnly(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/experience/club.md", snippet("title: League work\ndescription: Box office systems\ntags: [events]\ntriggers: [\"stadium crowds\"]\ndimensions: [metric]\nprinciples: [ownership]\nemployer: Northwind Example", "## Ticketing\nBuilt ticketing for a sports league."))
	f.write("cv.md", "## Experience\nShipped a managed services provider portal in C#.\n")
	f.write("narrative.md", "## Why\nI enjoy hackathon weekends.\n")
	f.write("knowledge/companies/northwind.md", snippet("", "Northwind runs a Kubernetes platform."))
	got, err := f.svc.OwnSections(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []OwnSection{
		{Ref: "cv.md#Experience", Text: "Experience\nShipped a managed services provider portal in C#."},
		{Ref: "knowledge/experience/club.md", Text: "League work\nBox office systems\nevents\nstadium crowds\nmetric\nownership\nNorthwind Example"},
		{Ref: "knowledge/experience/club.md#Ticketing", Text: "Ticketing\nBuilt ticketing for a sports league."},
		{Ref: "narrative.md#Why", Text: "Why\nI enjoy hackathon weekends."},
	}
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("sections = %q\nwant %q", got, want)
	}
	locked := filepath.Join(f.ws.Root, "knowledge", "experience")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if _, err := f.svc.OwnSections(f.ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unreadable folder: %v", err)
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	f.write("knowledge/projects/leak.md", snippet("", "Harbor notes.\n"+fakeCreds()["api key prefix"]))
	if _, err := f.svc.OwnSections(f.ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a held-out document: %v", err)
	}
}
