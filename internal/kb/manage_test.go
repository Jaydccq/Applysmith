package kb

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestKB_Edit_ValidatesAndConflicts(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("id: a\ntitle: One", "Built the reef survey tool."))
	d, err := f.svc.Show(f.ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	// An unparseable body is refused and the file untouched.
	before := f.read("knowledge/projects/a.md")
	if _, err := f.svc.Edit(f.ctx, "a", "---\ntags: oops\n---\n\nText.", d.FileHash); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad frontmatter = %v", err)
	}
	// A changed id is refused.
	if _, err := f.svc.Edit(f.ctx, "a", snippet("id: renamed", "Text."), d.FileHash); !errors.Is(err, ErrConflict) {
		t.Fatalf("id change = %v", err)
	}
	if f.read("knowledge/projects/a.md") != before {
		t.Fatal("a refused edit changed the file")
	}
	// A missing hash is refused.
	if _, err := f.svc.Edit(f.ctx, "a", before, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing hash = %v", err)
	}
	// A good edit lands.
	out, err := f.svc.Edit(f.ctx, "a", snippet("id: a\ntitle: One", "Built the reef survey tool and its exporter."), d.FileHash)
	if err != nil || out.ContentHash == d.ContentHash {
		t.Fatalf("edit = %+v, %v", out.Entry, err)
	}
}

func TestKB_Edit_StaleHashRefused(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("id: a", "Built the reef survey tool."))
	opened, _ := f.svc.Show(f.ctx, "a")
	f.write("knowledge/projects/a.md", snippet("id: a", "Edited elsewhere: reef survey tool v2."))
	disk := f.read("knowledge/projects/a.md")
	_, err := f.svc.Edit(f.ctx, "a", snippet("id: a", "My edit of the reef survey tool."), opened.FileHash)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit = %v", err)
	}
	if f.read("knowledge/projects/a.md") != disk {
		t.Fatal("a stale edit changed the file")
	}
}

func TestKB_Edit_PreviousVersionRecoverableUntilNextEdit(t *testing.T) {
	f := newFixture(t)
	v1 := snippet("id: a", "First version of the reef survey tool.")
	f.write("knowledge/projects/a.md", v1)
	d, _ := f.svc.Show(f.ctx, "a")
	v2 := snippet("id: a", "Second version of the reef survey tool.")
	d2, err := f.svc.Edit(f.ctx, "a", v2, d.FileHash)
	if err != nil || !d2.HasPrevious {
		t.Fatalf("edit = %v, previous %v", err, d2.HasPrevious)
	}
	prev, err := f.svc.PreviousVersion(f.ctx, "a")
	if err != nil || prev.Markdown != v1 {
		t.Fatalf("previous = %q, %v", prev, err)
	}
	v3 := snippet("id: a", "Third version of the reef survey tool.")
	if _, err := f.svc.Edit(f.ctx, "a", v3, d2.FileHash); err != nil {
		t.Fatal(err)
	}
	prev, _ = f.svc.PreviousVersion(f.ctx, "a")
	if prev.Markdown != v2 {
		t.Fatalf("after the next edit the previous version is %q", prev)
	}
}

func TestKB_Edit_AFailedRecordLeavesTheFileAndItsPreviousVersion(t *testing.T) {
	f := newFixture(t)
	v1 := snippet("id: a", "First version of the reef survey tool.")
	f.write("knowledge/projects/a.md", v1)
	d, _ := f.svc.Show(f.ctx, "a")
	v2 := snippet("id: a", "Second version of the reef survey tool.")
	// The record is made and the transaction then fails to commit.
	drop := f.failCommitAfter("INSERT ON kb_previous")
	if _, err := f.svc.Edit(f.ctx, "a", v2, d.FileHash); err == nil {
		t.Fatal("the edit succeeded without its commit")
	}
	if got := f.read("knowledge/projects/a.md"); got != v1 {
		t.Fatalf("after a failed commit the file is %q", got)
	}
	f.exec(drop)
	d2, err := f.svc.Edit(f.ctx, "a", v2, d.FileHash)
	if err != nil {
		t.Fatalf("edit after the failure: %v", err)
	}
	// The database refuses the record.
	f.exec(`CREATE TRIGGER test_refuse BEFORE INSERT ON kb_previous BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	v3 := snippet("id: a", "Third version of the reef survey tool.")
	if _, err := f.svc.Edit(f.ctx, "a", v3, d2.FileHash); err == nil {
		t.Fatal("the edit succeeded without its record")
	}
	if got := f.read("knowledge/projects/a.md"); got != v2 {
		t.Fatalf("after a refused record the file is %q", got)
	}
	if prev, err := f.svc.PreviousVersion(f.ctx, "a"); err != nil || prev.Markdown != v1 {
		t.Fatalf("previous = %q, %v", prev.Markdown, err)
	}
}

func TestKB_Delete_CascadesToIndex(t *testing.T) {
	e := newEmbedder(map[string]float64{"reef": 0.5})
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	f.write("knowledge/projects/a.md", snippet("id: a", "Built the reef survey tool."))
	f.search(semQuery) // fills the embedding cache
	d, _ := f.svc.Show(f.ctx, "a")
	if _, err := f.svc.Edit(f.ctx, "a", snippet("id: a", "Built the reef survey tool, again."), d.FileHash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Delete(f.ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if f.exists("knowledge/projects/a.md") {
		t.Fatal("file survives")
	}
	for _, q := range []string{
		`SELECT count(*) FROM kb_docs`, `SELECT count(*) FROM kb_sections`,
		`SELECT count(*) FROM kb_embeddings`, `SELECT count(*) FROM kb_previous`,
	} {
		var n int
		if err := f.st.Read().QueryRowContext(f.ctx, q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s = %d, %v", q, n, err)
		}
	}
	if r := f.search("reef survey tool"); len(r.Facts) != 0 {
		t.Fatalf("deleted text still found: %v", ids(r))
	}
	if _, err := f.svc.Delete(f.ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// Undo puts a deleted snippet back as it was before the delete: the file, its
// previous version and its place in search. It never replaces a snippet made
// again since, and the window expires.
func TestKB_DeleteUndo(t *testing.T) {
	f := newFixture(t)
	const rel = "knowledge/projects/a.md"
	f.write(rel, snippet("id: a", "Built the reef survey tool."))
	d, _ := f.svc.Show(f.ctx, "a")
	if _, err := f.svc.Edit(f.ctx, "a", snippet("id: a", "Built the reef survey tool, again."), d.FileHash); err != nil {
		t.Fatal(err)
	}
	file := f.read(rel)
	prev, err := f.svc.PreviousVersion(f.ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.svc.Delete(f.ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Restore(f.ctx, token); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.PreviousVersion(f.ctx, "a"); f.read(rel) != file || !reflect.DeepEqual(got, prev) {
		t.Fatalf("restored %q, previous %+v", f.read(rel), got)
	}
	if r := f.search("reef survey tool"); len(r.Facts) == 0 {
		t.Fatal("the restored snippet is not found")
	}
	token, _ = f.svc.Delete(f.ctx, "a")
	f.write("knowledge/experience/a.md", snippet("id: a", "A newer harbor note."))
	if _, err := f.svc.Restore(f.ctx, token); !errors.Is(err, ErrConflict) || f.exists(rel) {
		t.Fatalf("restore over a newer snippet = %v", err)
	}
	token, _ = f.svc.Delete(f.ctx, "a")
	f.clk.advance(UndoWindow + time.Second)
	if _, err := f.svc.Restore(f.ctx, token); !errors.Is(err, ErrNotFound) || f.exists("knowledge/experience/a.md") {
		t.Fatalf("restore after the window = %v", err)
	}
}

// A delete whose file cannot be removed, or whose record cannot be committed,
// leaves the page and its previous version as they were.
func TestKB_Delete_FailureKeepsThePageAndItsPreviousVersion(t *testing.T) {
	f := newFixture(t)
	const rel = "knowledge/projects/a.md"
	f.write(rel, snippet("id: a", "Built the reef survey tool."))
	d, _ := f.svc.Show(f.ctx, "a")
	if _, err := f.svc.Edit(f.ctx, "a", snippet("id: a", "Built the reef survey tool, again."), d.FileHash); err != nil {
		t.Fatal(err)
	}
	file := f.read(rel)
	dir := filepath.Join(f.ws.Root, "knowledge", "projects")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Delete(f.ctx, "a")
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatal("a delete whose file stayed reported success")
	}
	if _, err := f.svc.PreviousVersion(f.ctx, "a"); err != nil || f.read(rel) != file {
		t.Fatalf("after a failed remove: previous %v, file %q", err, f.read(rel))
	}
	drop := f.failCommitAfter("DELETE ON kb_previous")
	if _, err := f.svc.Delete(f.ctx, "a"); err == nil {
		t.Fatal("a delete whose record failed reported success")
	}
	if _, err := f.svc.PreviousVersion(f.ctx, "a"); err != nil || !f.exists(rel) || f.read(rel) != file {
		t.Fatalf("after a failed commit: previous %v, exists %v", err, f.exists(rel))
	}
	f.exec(drop)
}

// A page named "gaps" or "embeddings" is deleted by its id; the gap list and
// the vector cache are cleared only through their typed refs.
func TestKB_Remove_PageIDsAreNotKeywords(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/embeddings.md", snippet("id: embeddings\ntitle: Embeddings", "Built the vector search."))
	if err := f.svc.Remove(f.ctx, "embeddings"); err != nil {
		t.Fatal(err)
	}
	if f.exists("knowledge/projects/embeddings.md") {
		t.Fatal("removing the id \"embeddings\" left the page")
	}
	for _, ref := range []string{"cache:gaps", "cache:embeddings"} {
		if err := f.svc.Remove(f.ctx, ref); err != nil {
			t.Fatalf("Remove(%q) = %v", ref, err)
		}
	}
}

func TestKB_Export(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("id: a", "Built the reef survey tool."))
	f.write("knowledge/companies/example.test.md", "---\nsource_url: https://example.test/\nfetched_at: 2027-03-01T00:00:00Z\n---\n\nExample Test makes reef sensors.\n")
	draftOf(t, f, "pending", "Pending text.")
	out := filepath.Join(t.TempDir(), "out")
	if err := f.svc.Export(f.ctx, out); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"projects/a.md", "companies/example.test.md", "drafts.json", "gaps.json"} {
		if _, err := os.Stat(filepath.Join(out, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(out, "drafts.json"))
	if !strings.Contains(string(b), "pending") {
		t.Fatalf("drafts.json = %s", b)
	}
}

func TestKB_Lint_FourFindings(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/behavioral/a.md", snippet("id: a\ntriggers: [\"tell me about a conflict\"]\nallowed_facts: [\"Cut 200 alerts to 15 alerts in 12 minutes\"]", "Cut noisy alerts."))
	f.write("knowledge/behavioral/b.md", snippet("id: b\ntriggers: [\"tell me about a conflict\"]\nallowed_facts: [\"Cut 200 alerts to 15 alerts in 12 minutes\"]", "Silenced paging noise."))
	f.write("knowledge/behavioral/c.md", snippet("id: c", "No facts at all here."))
	// The New snippet form cannot add facts, and a snippet outside the story
	// bank is used by its text, so it is not flagged for having none.
	if _, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: "Form", Category: CatProjects, Body: "Built the harbor crane scheduler."}); err != nil {
		t.Fatal(err)
	}
	list, _ := f.svc.List(f.ctx)
	by := map[string]map[string]string{}
	for _, e := range list {
		by[e.ID] = map[string]string{}
		for _, fd := range e.Lint {
			by[e.ID][fd.Code] = fd.Other
		}
	}
	if by["a"][LintTriggerCollision] != "b" || by["b"][LintTriggerCollision] != "a" {
		t.Fatalf("collisions = %v", by)
	}
	if by["a"][LintNearDuplicate] != "b" {
		t.Fatalf("near duplicate = %v", by)
	}
	if _, ok := by["c"][LintEmptyAllowedFacts]; !ok {
		t.Fatalf("empty facts = %v", by)
	}
	if _, ok := by["form"][LintEmptyAllowedFacts]; ok || by["form"] == nil {
		t.Fatalf("a form-made snippet = %v", by)
	}
}
