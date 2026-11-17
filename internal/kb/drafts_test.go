package kb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func draftOf(t *testing.T, f *fixture, id, body string, opts ...func(*NewDraft)) Draft {
	t.Helper()
	nd := NewDraft{Kind: DraftSnippet, Origin: OriginUser, Category: CatProjects, SnippetID: id, Markdown: snippet("id: "+id+"\nallowed_facts: [\"Scale: 3 regions\"]", body)}
	for _, o := range opts {
		o(&nd)
	}
	d, err := f.svc.CreateDraft(f.ctx, nd)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDrafts_Invisible(t *testing.T) {
	f := newFixture(t)
	draftOf(t, f, "ferry", "Operated the ferry dispatch console for coastal routes.")
	if r := f.search("ferry dispatch console coastal routes"); len(r.Facts) != 0 {
		t.Fatalf("a draft was returned: %v", ids(r))
	}
}

func TestDrafts_NothingPublishedAutomatically(t *testing.T) {
	f := newFixture(t)
	d := draftOf(t, f, "ferry", "Operated the ferry dispatch console for coastal routes.")
	if _, err := f.svc.SetDraftStatus(f.ctx, d.ID, StatusReady); err != nil {
		t.Fatal(err)
	}
	f.svc.Sync(f.ctx)
	if f.exists("knowledge/projects/ferry.md") {
		t.Fatal("a ready draft was written to knowledge/")
	}
	got, _ := f.svc.GetDraft(f.ctx, d.ID)
	if got.Status != StatusReady {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestDrafts_PublishWritesFile(t *testing.T) {
	f := newFixture(t)
	d := draftOf(t, f, "ferry", "Operated the ferry dispatch console for coastal routes.")
	out, err := f.svc.PublishDraft(f.ctx, d.ID)
	if err != nil || out.Path != "knowledge/projects/ferry.md" {
		t.Fatalf("publish = %+v, %v", out, err)
	}
	if !f.exists(out.Path) {
		t.Fatal("no file written")
	}
	if r := f.search("ferry dispatch console coastal routes"); len(r.Facts) == 0 {
		t.Fatal("the published snippet is not retrievable")
	}
	got, _ := f.svc.GetDraft(f.ctx, d.ID)
	if got.Status != StatusPublished || got.PublishedPath != out.Path {
		t.Fatalf("draft = %+v", got)
	}
}

func TestPublish_AFailedRecordLeavesThePageItsPreviousVersionAndTheDraft(t *testing.T) {
	f := newFixture(t)
	const page = "knowledge/projects/ferry.md"
	old := snippet("id: ferry", "Operated the ferry dispatch console.")
	f.write(page, old)
	f.svc.Sync(f.ctx)
	d := draftOf(t, f, "ferry", "Rebuilt the ferry dispatch console for coastal routes.", func(nd *NewDraft) { nd.BaseHash = fileHash([]byte(old)) })
	check := func(when string) {
		t.Helper()
		if got := f.read(page); got != old {
			t.Fatalf("%s: the page was replaced though no record of it was kept:\n%s", when, got)
		}
		if got, _ := f.svc.GetDraft(f.ctx, d.ID); got.Status == StatusPublished {
			t.Fatalf("%s: the draft is published", when)
		}
	}
	// The database refuses the record.
	f.exec(`CREATE TRIGGER test_refuse BEFORE UPDATE ON kb_drafts BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	if _, err := f.svc.PublishDraft(f.ctx, d.ID); err == nil {
		t.Fatal("the publish succeeded without its record")
	}
	check("a refused record")
	f.exec(`DROP TRIGGER test_refuse`)
	// The record is made and the transaction then fails to commit.
	drop := f.failCommitAfter("UPDATE ON kb_drafts")
	if _, err := f.svc.PublishDraft(f.ctx, d.ID); err == nil {
		t.Fatal("the publish succeeded without its commit")
	}
	check("a failed commit")
	f.exec(drop)
	// The draft is still the newest revision of an unchanged page.
	if _, err := f.svc.PublishDraft(f.ctx, d.ID); err != nil {
		t.Fatalf("publish after the failures: %v", err)
	}
	if prev, err := f.svc.PreviousVersion(f.ctx, "ferry"); err != nil || prev.Markdown != old {
		t.Fatalf("previous = %q, %v", prev.Markdown, err)
	}
	// A page that did not exist is taken away again.
	fresh := draftOf(t, f, "harbor", "Ran the harbor pilot schedule for two seasons.")
	drop = f.failCommitAfter("UPDATE ON kb_drafts")
	if _, err := f.svc.PublishDraft(f.ctx, fresh.ID); err == nil || f.exists("knowledge/projects/harbor.md") {
		t.Fatalf("a publish that failed to commit left its page: %v", err)
	}
	f.exec(drop)
}

func TestPublish_DefaultCategoryAnswerPatterns(t *testing.T) {
	f := newFixture(t)
	d := draftOf(t, f, "Remote Work!", "Open to remote and hybrid arrangements.", func(n *NewDraft) { n.Category = "" })
	out, err := f.svc.PublishDraft(f.ctx, d.ID)
	if err != nil || out.Path != "knowledge/answer-patterns/remote-work.md" {
		t.Fatalf("publish = %+v, %v", out, err)
	}
}

func TestDrafts_SupersededPublishConflict(t *testing.T) {
	f := newFixture(t)
	r1 := draftOf(t, f, "ferry", "Operated the ferry dispatch console.")
	r2, err := f.svc.RefineDraft(f.ctx, r1.ID, snippet("id: ferry", "Operated the ferry dispatch console and the radio desk."), nil)
	if err != nil || r2.Revision != 2 {
		t.Fatalf("refine = %+v, %v", r2, err)
	}
	_, err = f.svc.PublishDraft(f.ctx, r1.ID)
	var se *SupersededError
	if !errors.Is(err, ErrConflict) || !errors.As(err, &se) || se.Newer != 2 || se.NewerID != r2.ID {
		t.Fatalf("publish of revision 1 = %v", err)
	}
	if f.exists("knowledge/projects/ferry.md") || f.exists("knowledge/answer-patterns/ferry.md") {
		t.Fatal("a superseded revision wrote a file")
	}
	old, _ := f.svc.GetDraft(f.ctx, r1.ID)
	if old.Status != StatusSuperseded {
		t.Fatalf("status = %s", old.Status)
	}
}

func TestDrafts_StoryRefusedByGenericPublish(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.CreateDraft(f.ctx, NewDraft{Kind: DraftStory, SnippetID: "story-a", Markdown: storyMarkdown("story-a", nil)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.PublishDraft(f.ctx, d.ID)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "story publish action") {
		t.Fatalf("generic publish = %v", err)
	}
}

func TestDrafts_OnePerRun_RefusedAfterPublish(t *testing.T) {
	f := newFixture(t)
	nd := NewDraft{SourceRun: "run-1", Origin: OriginRepo, Category: CatProjects, SnippetID: "repo-a", Markdown: snippet("id: repo-a", "First pass about lock-free queues.")}
	a, err := f.svc.UpsertSourceDraft(f.ctx, nd)
	if err != nil {
		t.Fatal(err)
	}
	nd.Markdown = snippet("id: repo-a", "Second pass about lock-free queues and ring buffers.")
	b, err := f.svc.UpsertSourceDraft(f.ctx, nd)
	if err != nil || b.ID != a.ID {
		t.Fatalf("second build = %+v, %v", b, err)
	}
	list, _ := f.svc.ListDrafts(f.ctx, true)
	if len(list) != 1 || !strings.Contains(list[0].Markdown, "ring buffers") {
		t.Fatalf("drafts = %+v", list)
	}
	if _, err := f.svc.PublishDraft(f.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpsertSourceDraft(f.ctx, nd); !errors.Is(err, ErrConflict) {
		t.Fatalf("build after publish = %v", err)
	}
	// Once the user deletes the page, the next build drafts it again, and the
	// build after that refreshes the new draft in place.
	if _, err := f.svc.Delete(f.ctx, "repo-a"); err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.UpsertSourceDraft(f.ctx, nd)
	if err != nil || c.ID == a.ID || c.Status != StatusDraft {
		t.Fatalf("build after the page was deleted = %+v, %v", c, err)
	}
	if d, err := f.svc.UpsertSourceDraft(f.ctx, nd); err != nil || d.ID != c.ID {
		t.Fatalf("rebuild of the new draft = %+v, %v", d, err)
	}
}

func TestImport_SafeIDAndNoOverwrite(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "../My Notes (v2).md", Data: []byte("# Warehouse notes\n\nMapped the pick path.")})
	if err != nil || d.SnippetID != "my-notes-v2" || d.Origin != OriginImport || d.Status != StatusDraft {
		t.Fatalf("import = %+v, %v", d, err)
	}
	if !strings.Contains(d.Markdown, "allowed_facts: []") {
		t.Fatalf("allowed_facts not empty: %s", d.Markdown)
	}
	if f.exists("knowledge/answer-patterns/my-notes-v2.md") {
		t.Fatal("an import wrote a published file")
	}
	if _, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "my-notes-v2.md", Data: []byte("Other text here.")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second import = %v", err)
	}
	if _, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "my-notes-v2.md", Data: []byte("Other text here."), Replace: true}); err != nil {
		t.Fatalf("replace import = %v", err)
	}
	if _, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "x.exe", Data: []byte("MZ")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("extension = %v", err)
	}
	if _, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "big.txt", Data: make([]byte, 6<<20)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("size = %v", err)
	}
	if _, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "doc.pdf", Data: []byte("%PDF")}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("pdf without extractor = %v", err)
	}
}

// The index skips a knowledge file over 1 MiB, so a draft that large is
// refused rather than published as a page no search finds.
func TestImport_TextOverTheIndexCapIsRefused(t *testing.T) {
	f := newFixture(t)
	big := "# Field log\n\n" + strings.Repeat("Logged a reef survey at dawn. ", (maxFileBytes/30)+10)
	if _, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "field-log.md", Data: []byte(big)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("import of %d bytes = %v", len(big), err)
	}
}

func TestImport_PDFUsesExtractor(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.PDFText = func(context.Context, []byte) (string, error) { return "Extracted text about boat hulls.", nil }
	})
	d, err := f.svc.ImportFile(f.ctx, ImportInput{Name: "Hulls.PDF", Data: []byte("%PDF-1.4")})
	if err != nil || !strings.Contains(d.Markdown, "boat hulls") {
		t.Fatalf("pdf import = %+v, %v", d, err)
	}
}

func TestPublish_RunsLintWithoutBlocking(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("Sentence about the harbor crane. ", 80)
	d, err := f.svc.CreateDraft(f.ctx, NewDraft{Category: CatProjects, SnippetID: "big", Markdown: snippet("id: big", long)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.PublishDraft(f.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, fd := range out.Lint {
		codes[fd.Code] = true
	}
	if !codes[LintSectionOverBudget] {
		t.Fatalf("lint = %+v", out.Lint)
	}
}

func TestPublish_RefusesIDOwnedByAnotherFile(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/experience/other.md", snippet("id: ferry", "Existing snippet about ferries."))
	d := draftOf(t, f, "ferry", "Operated the ferry dispatch console.")
	if _, err := f.svc.PublishDraft(f.ctx, d.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("publish = %v", err)
	}
}

func TestDrafts_DeleteRemovesRevisions(t *testing.T) {
	f := newFixture(t)
	d := draftOf(t, f, "ferry", "Operated the ferry dispatch console.")
	n, _ := f.svc.RefineDraft(f.ctx, d.ID, snippet("id: ferry", "More text."), nil)
	before, _ := f.svc.ListDrafts(f.ctx, true)
	token, err := f.svc.DeleteDraft(f.ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := f.svc.ListDrafts(f.ctx, true); len(list) != 0 {
		t.Fatalf("drafts left: %+v", list)
	}
	// Undo brings every revision back; a draft made again since is never replaced.
	if _, err := f.svc.Restore(f.ctx, token); err != nil {
		t.Fatal(err)
	}
	if after, _ := f.svc.ListDrafts(f.ctx, true); !reflect.DeepEqual(after, before) {
		t.Fatalf("restored %+v, want %+v", after, before)
	}
	token, _ = f.svc.DeleteDraft(f.ctx, n.ID)
	again := draftOf(t, f, "ferry", "A newer ferry draft.")
	if _, err := f.svc.Restore(f.ctx, token); !errors.Is(err, ErrConflict) {
		t.Fatalf("restore over a newer draft = %v", err)
	}
	if list, _ := f.svc.ListDrafts(f.ctx, true); len(list) != 1 || list[0].ID != again.ID {
		t.Fatalf("drafts = %+v", list)
	}
}

func TestPublish_UnknownFrontmatterKeysSurvive(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.CreateDraft(f.ctx, NewDraft{Category: CatProjects, SnippetID: "keep", Markdown: snippet("id: keep\nreviewed_by: nobody\nupdated_at: 2027-01-02", "Kept body text about harbors.")})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.PublishDraft(f.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	file := f.read(out.Path)
	if !strings.Contains(file, "reviewed_by: nobody") || !strings.Contains(file, "updated_at: 2027-01-02") {
		t.Fatalf("unknown keys lost:\n%s", file)
	}
}
