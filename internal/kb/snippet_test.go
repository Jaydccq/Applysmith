package kb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const projectDoc = `
id: inventory-service
title: Inventory service
tags: [backend]
allowed_facts:
  - "Team size: 4 engineers"
`

func TestKB_Layout_CategoryFromDirectory(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/inventory-service.md", snippet(projectDoc, "Built the stock reservation flow for the warehouse."))
	f.write("knowledge/loose.md", snippet("", "A loose note about warehouse scanning."))
	list, err := f.svc.List(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	cats := map[string]string{}
	for _, e := range list {
		cats[e.ID] = e.Category
	}
	if cats["inventory-service"] != "projects" || cats["loose"] != "" || len(cats) != 2 {
		t.Fatalf("categories = %v", cats)
	}
}

func TestKB_Load_DuplicateIDFirstPathWins(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/b.md", snippet("id: payments", "Project text about ledgers and settlement."))
	f.write("knowledge/experience/a.md", snippet("id: payments", "Experience text about ledgers and settlement."))
	rep, err := f.svc.Sync(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "knowledge/projects/b.md") || !strings.Contains(rep.Warnings[0], "knowledge/experience/a.md") {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	d, err := f.svc.Show(f.ctx, "payments")
	if err != nil || d.Path != "knowledge/experience/a.md" {
		t.Fatalf("winner = %v, %v", d.Path, err)
	}
	res := f.search("ledgers settlement")
	if got := snippetIDs(res); len(got) != 1 || got[0] != "experience/a" {
		t.Fatalf("results = %v", got)
	}
}

func TestKB_Load_BrokenFrontmatterSkipped(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/bad.md", "---\ntitle: [unclosed\n---\n\nBody about turbines and blades.\n")
	f.write("knowledge/projects/good.md", snippet("title: Good", "Maintained the turbine blade inspection tool."))
	rep, err := f.svc.Sync(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "knowledge/projects/bad.md") {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	if got := snippetIDs(f.search("turbine blade inspection")); len(got) != 1 || got[0] != "projects/good" {
		t.Fatalf("results = %v", got)
	}
}

func TestKB_Parse_FieldTypeValidation(t *testing.T) {
	for name, front := range map[string]string{
		"tags not a list":     "tags: backend",
		"max_length zero":     "max_length: 0",
		"max_length negative": "max_length: -3",
		"max_length text":     "max_length: lots",
		"empty trigger":       "triggers: [\"\"]",
		"facts not a list":    "allowed_facts: some fact",
		"empty voice":         "voice: \"\"",
		"section_facts list":  "section_facts: [a, b]",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSnippet("knowledge/x.md", []byte(snippet(front, "Body text here."))); err == nil {
				t.Fatal("expected a frontmatter error")
			}
		})
	}
}

func TestKB_Load_DepthBoundAndNonMarkdownIgnored(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a/b/c/ok.md", snippet("", "Depth four is allowed for vendors."))
	f.write("knowledge/projects/a/b/c/d/too-deep.md", snippet("", "Depth five is too deep for vendors."))
	f.write("knowledge/projects/notes.txt", "not markdown about vendors")
	list, _ := f.svc.List(f.ctx)
	if len(list) != 1 || list[0].ID != "ok" {
		t.Fatalf("listed %v", list)
	}
}

func TestKB_Load_SymlinkCycleDoesNotHang(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/real.md", snippet("", "Real snippet about gardens."))
	if err := os.Symlink(filepath.Join(f.ws.Root, "knowledge"), filepath.Join(f.ws.Root, "knowledge", "projects", "loop")); err != nil {
		t.Skip("symlinks unavailable")
	}
	list, err := f.svc.List(f.ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
}

func TestKB_Sections_SplitRules(t *testing.T) {
	sn, err := ParseSnippet("knowledge/x.md", []byte(snippet("", "Intro paragraph.\n\n## Design\nDesign text.\n\n## Results\nResult text.")))
	if err != nil {
		t.Fatal(err)
	}
	if len(sn.Sections) != 3 || sn.Sections[0].Heading != "" || sn.Sections[1].Heading != "Design" {
		t.Fatalf("sections = %+v", sn.Sections)
	}
	one, _ := ParseSnippet("knowledge/y.md", []byte(snippet("", "No headings at all.")))
	if len(one.Sections) != 1 {
		t.Fatalf("heading-less body sections = %d", len(one.Sections))
	}
	fenced, _ := ParseSnippet("knowledge/z.md", []byte(snippet("", "Intro.\n\n```\n# not a heading\n```\n\n## Real\nText.")))
	if len(fenced.Sections) != 2 {
		t.Fatalf("a heading inside a code fence split the body: %+v", fenced.Sections)
	}
	// A closing run of "#" needs a space before it; "C#" keeps its sign.
	signs, _ := ParseSnippet("knowledge/w.md", []byte(snippet("", "## C#\nText.\n\n## F# ##\nMore.\n\n## Closed ##\nEnd.")))
	if len(signs.Sections) != 3 || signs.Sections[0].Heading != "C#" || signs.Sections[1].Heading != "F#" || signs.Sections[2].Heading != "Closed" {
		t.Fatalf("headings = %+v", signs.Sections)
	}
	if got := titleOf("x.md", "# C#\nText."); got != "C#" {
		t.Fatalf("first heading = %q", got)
	}
}

func TestKB_AllowedFacts_UnionFallback(t *testing.T) {
	front := "section_facts:\n  A:\n    - one\n  B:\n    - two\n"
	sn, _ := ParseSnippet("knowledge/x.md", []byte(snippet(front, "## A\nText.\n\n## B\nText.")))
	if got := sn.Facts(); len(got) != 2 {
		t.Fatalf("union = %v", got)
	}
	explicit, _ := ParseSnippet("knowledge/y.md", []byte(snippet(front+"allowed_facts: [only]\n", "## A\nText.")))
	if got := explicit.Facts(); len(got) != 1 || got[0] != "only" {
		t.Fatalf("explicit = %v", got)
	}
}

func TestKB_Trigger_InvalidPatternNeverMatches(t *testing.T) {
	tr := compileTrigger("/(unclosed/")
	if tr.matches("anything (unclosed") {
		t.Fatal("an invalid pattern matched")
	}
	sn, _ := ParseSnippet("knowledge/x.md", []byte(snippet("triggers: [\"/(unclosed/\"]\nallowed_facts: [a]", "Text body here.")))
	found := false
	for _, fd := range lintSnippet(sn, nil) {
		found = found || fd.Code == LintInvalidTrigger
	}
	if !found {
		t.Fatal("no lint warning for the invalid trigger")
	}
}

func TestKB_Hash_DescriptiveEditsKeepHash(t *testing.T) {
	base := snippet("id: a\ntitle: One\ndescription: first\nupdated_at: 2027-01-01\ntags: [x, y]", "Body sentence.")
	a, _ := ParseSnippet("knowledge/a.md", []byte(base))
	retitled, _ := ParseSnippet("knowledge/a.md", []byte(snippet("id: a\ntitle: Two\ndescription: other\nupdated_at: 2027-06-01\ntags: [y, x]", "Body sentence.")))
	if a.ContentHash != retitled.ContentHash {
		t.Fatal("title, description, timestamp or tag order changed the hash")
	}
	body, _ := ParseSnippet("knowledge/a.md", []byte(snippet("id: a\ntitle: One\ntags: [x, y]", "Body sentence changed.")))
	if a.ContentHash == body.ContentHash {
		t.Fatal("a body edit kept the hash")
	}
	tags, _ := ParseSnippet("knowledge/a.md", []byte(snippet("id: a\ntags: [x, z]", "Body sentence.")))
	if a.ContentHash == tags.ContentHash {
		t.Fatal("a tag edit kept the hash")
	}
}

func TestKB_Reindex_LostIndexRebuilds(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Designed the harbor scheduling algorithm."))
	before := f.search("harbor scheduling algorithm")
	if len(before.Facts) == 0 {
		t.Fatal("nothing found before")
	}
	if err := f.st.Write(f.ctx, func(tx sqlTx) error {
		_, err := tx.ExecContext(f.ctx, `DELETE FROM kb_docs`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after := f.search("harbor scheduling algorithm")
	if !equal(ids(before), ids(after)) || after.Status != StatusGrounded {
		t.Fatalf("before %v after %v", ids(before), ids(after))
	}
}

func TestKB_Reindex_EditThenAsk(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Designed the harbor scheduling algorithm."))
	f.search("harbor scheduling algorithm")
	f.write("knowledge/projects/a.md", snippet("", "Designed the lighthouse beacon firmware."))
	if res := f.search("lighthouse beacon firmware"); len(res.Facts) == 0 {
		t.Fatal("the edited text was not returned")
	}
	if res := f.search("harbor scheduling algorithm"); len(res.Facts) != 0 {
		t.Fatalf("the old text is still returned: %v", ids(res))
	}
}

func TestKB_Reindex_ExplicitRebuild(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Designed the harbor scheduling algorithm."))
	f.search("harbor scheduling algorithm")
	rep, err := f.svc.Reindex(f.ctx)
	if err != nil || rep.Changed != 1 {
		t.Fatalf("reindex = %+v, %v", rep, err)
	}
}
