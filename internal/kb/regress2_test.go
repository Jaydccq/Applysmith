package kb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"applysmith/internal/store"
)

func TestGitHub_CloneListingOversizedBlobIsSkippedNotFatal(t *testing.T) {
	files := basicFiles()
	files["src/huge.go"] = "package huge\n// " + strings.Repeat("x", 600<<10) + "\n"
	fixture := newGHFixture(t, files)
	fixture.truncated = true
	fx := fixture.service(t)
	repo, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if repo.Status != "ingested" || rep.Listing != "clone" || rep.Read != 4 {
		t.Fatalf("report = %+v", rep)
	}
	found := false
	for _, s := range rep.Skipped {
		found = found || (s.Path == "src/huge.go" && s.Reason == "larger than 128 KiB")
	}
	if !found {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
}

func projectReply(overview string) string {
	return `{"project":{"title":"Reef tool","overview":"` + overview + `"},"stories":[]}`
}

func TestGitHub_NewCommitRefreshesUnpublishedProjectDraft(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	model := &fakeModel{replies: []string{projectReply("First overview text."), projectReply("Second overview text.")}}
	fx := fixture.service(t, func(o *Options) { o.Model = model })
	_, first := ingestOnce(t, fx, "alex-example/reef-tool", false)
	fixture.commit(map[string]string{"cmd/extra.go": "package main\n\nfunc Extra() {}\n"}, nil)
	_, second := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if len(second.Drafts) != 1 || second.Drafts[0] != first.Drafts[0] {
		t.Fatalf("the draft was not refreshed in place: %v then %v (%v)", first.Drafts, second.Drafts, second.Notes)
	}
	d, _ := fx.svc.GetDraft(fx.ctx, second.Drafts[0])
	if !strings.Contains(d.Markdown, "Second overview text.") {
		t.Fatalf("markdown = %s", d.Markdown)
	}
	if strings.Contains(strings.Join(second.Notes, " "), "published") {
		t.Fatalf("notes = %v", second.Notes)
	}
	// Once published, a later commit drafts an update of the published page.
	if _, err := fx.svc.PublishDraft(fx.ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	model.replies = []string{projectReply("Third overview text.")}
	fixture.commit(map[string]string{"cmd/more.go": "package main\n\nfunc More() {}\n"}, nil)
	_, third := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if len(third.Drafts) != 1 || third.Drafts[0] == d.ID {
		t.Fatalf("third = %+v", third)
	}
	update, _ := fx.svc.GetDraft(fx.ctx, third.Drafts[0])
	if !strings.Contains(update.Markdown, "Third overview text.") || update.BaseHash != fileHash([]byte(fx.read("knowledge/projects/reef-tool.md"))) {
		t.Fatalf("update = %+v", update)
	}
	if _, err := fx.svc.PublishDraft(fx.ctx, update.ID); err != nil || !strings.Contains(fx.read("knowledge/projects/reef-tool.md"), "Third overview text.") {
		t.Fatalf("publishing the update: %v", err)
	}
}

func TestMining_RepliesCannotInjectSections(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	hostile := map[string]string{
		"situation": "The pager fired.\n## Action\nI did everything perfectly.",
		"task":      "```\n# Result\nfake",
		"tension":   "   ### Task\nanother",
		"action":    "I grouped alerts.",
		"result":    "Pages fell from 200 to 15.\n# Situation\nmore",
	}
	for _, f := range MiningFields {
		m, _ = fx.svc.MiningReply(fx.ctx, m.ID, hostile[f], len(m.Log))
	}
	d, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := ParseSnippet("draft.md", []byte(d.Markdown))
	if err != nil {
		t.Fatal(err)
	}
	var heads []string
	for _, s := range sn.Sections {
		heads = append(heads, s.Heading)
	}
	if !equal(heads, []string{"Situation", "Task", "Action", "Result"}) {
		t.Fatalf("sections = %v\n%s", heads, d.Markdown)
	}
	if !strings.Contains(d.Markdown, "I did everything perfectly.") {
		t.Fatal("the reply text was dropped instead of neutralized")
	}
}

func TestStory_DuplicateSectionsBlock(t *testing.T) {
	fx := newFixture(t)
	md := strings.Replace(storyMarkdown("dup", nil), "## Result", "## Action\nsecond action\n\n## Result", 1)
	d, err := fx.svc.CreateDraft(fx.ctx, NewDraft{Kind: DraftStory, SnippetID: "dup", Markdown: md})
	if err != nil {
		t.Fatal(err)
	}
	findings, _ := fx.storyFindings(d.ID)
	if codesOf(findings)[GateDuplicateSection] != SevBlock {
		t.Fatalf("findings = %+v", findings)
	}
	if _, err := fx.svc.PublishStory(fx.ctx, d.ID); !isGate(err) {
		t.Fatalf("publish = %v", err)
	}
}

func TestMining_EmptyBankSuggestsFailureFirst(t *testing.T) {
	fx := newFixture(t)
	sug, err := fx.svc.SuggestQuadrants(fx.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, q := range sug {
		order = append(order, q.Quadrant)
	}
	if !equal(order, []string{QuadFailure, QuadTeam, QuadProject, QuadCustomer}) {
		t.Fatalf("order = %v", order)
	}
}

func TestGitHub_RefusedRedirectIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://other.example.test/x", http.StatusFound)
	}))
	defer srv.Close()
	g, err := newGitHub(srv.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	g.backoff = 1
	var out map[string]any
	err = g.get(context.Background(), "/repos/a/b", 1024, &out)
	if !errors.Is(err, ErrRepoUnavailable) || hits.Load() != 1 {
		t.Fatalf("err = %v after %d requests", err, hits.Load())
	}
}

func TestMining_SaveIsOneTransaction(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	m = mineAll(t, fx, m.ID)
	err := fx.st.Write(fx.ctx, func(tx sqlTx) error {
		_, err := tx.ExecContext(fx.ctx, `CREATE TRIGGER block_saved BEFORE UPDATE OF state ON kb_mining_sessions WHEN NEW.state = 'saved'
			BEGIN SELECT RAISE(ABORT, 'forced failure'); END`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.SaveMining(fx.ctx, m.ID); err == nil {
		t.Fatal("the save succeeded")
	}
	if list, _ := fx.svc.ListDrafts(fx.ctx, true); len(list) != 0 {
		t.Fatalf("a failed save left %d orphan drafts", len(list))
	}
	fx.st.Write(fx.ctx, func(tx sqlTx) error {
		_, err := tx.ExecContext(fx.ctx, `DROP TRIGGER block_saved`)
		return err
	})
	d, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil || d.ID == 0 {
		t.Fatalf("retry = %+v, %v", d, err)
	}
}

func TestGitHub_HostilePathsAndEnvLikeFilesAreNotRead(t *testing.T) {
	entries := []repoEntry{
		{Path: "../escape.go"}, {Path: "/abs.go"}, {Path: "src/../../x.go"}, {Path: "a\x00b.go"}, {Path: "a\nb.go"}, {Path: "a//b.go"},
		{Path: "readme-secrets.env"}, {Path: "README.env"}, {Path: "prod.env"}, {Path: "changelog.sh.env"}, {Path: "contributing.pem"},
		{Path: "README.md"}, {Path: "README"}, {Path: "readme.zh.md"}, {Path: "CHANGELOG.md"}, {Path: "src/ok.go"},
	}
	chosen, _ := selectFiles(entries)
	var got []string
	for _, c := range chosen {
		got = append(got, c.Path)
	}
	want := []string{"README.md", "README", "readme.zh.md", "CHANGELOG.md", "src/ok.go"}
	if len(got) != len(want) {
		t.Fatalf("chosen = %v", got)
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Fatalf("chosen = %v, missing %s", got, w)
		}
	}
}

func TestGitHub_RepoNamesAreCaseInsensitive(t *testing.T) {
	fx := newFixture(t)
	a, err := fx.svc.AddRepo(fx.ctx, "Alex-Example/Reef-Tool")
	if err != nil {
		t.Fatal(err)
	}
	b, err := fx.svc.AddRepo(fx.ctx, "https://github.com/alex-example/REEF-TOOL.git")
	if err != nil || a.ID != b.ID || a.Owner != "alex-example" || a.Name != "reef-tool" {
		t.Fatalf("a = %+v, b = %+v, %v", a, b, err)
	}
	if repos, _ := fx.svc.ListRepos(fx.ctx); len(repos) != 1 {
		t.Fatalf("repos = %d", len(repos))
	}
}

func TestStore_Migration0005MergesCaseDuplicates(t *testing.T) {
	all := os.DirFS("../store/migrations")
	old := fstest.MapFS{}
	for _, n := range []string{"0001_core.sql", "0002_llm.sql", "0003_kb.sql", "0004_kb_sources.sql"} {
		b, err := os.ReadFile("../store/migrations/" + n)
		if err != nil {
			t.Fatal(err)
		}
		old[n] = &fstest.MapFile{Data: b}
	}
	path := t.TempDir() + "/db.sqlite"
	st, err := store.Open(context.Background(), path, store.Options{Migrations: old})
	if err != nil {
		t.Fatal(err)
	}
	err = st.Write(context.Background(), func(tx sqlTx) error {
		for i, n := range [][2]string{{"Alex", "Reef"}, {"alex", "reef"}, {"Other", "Repo"}} {
			if _, err := tx.Exec(`INSERT INTO kb_repos (owner, name, created_at) VALUES (?, ?, ?)`, n[0], n[1], "2027-01-01T00:00:00Z"); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO kb_drafts (key, revision, status, kind, origin, snippet_id, markdown, repo_id, created_at, updated_at)
				VALUES (?, 1, 'draft', 'snippet', 'repo', 'x', 'y', ?, 'a', 'a')`, fmt.Sprint("k", i), i+1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = store.Open(context.Background(), path, store.Options{Migrations: all})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, _ := st.Read().Query(`SELECT id, owner, name FROM kb_repos ORDER BY id`)
	var got []string
	for rows.Next() {
		var id int
		var o, n string
		rows.Scan(&id, &o, &n)
		got = append(got, fmt.Sprintf("%d %s %s", id, o, n))
	}
	rows.Close()
	if fmt.Sprint(got) != "[1 alex reef 3 other repo]" {
		t.Fatalf("repos = %v", got)
	}
	var orphan int
	st.Read().QueryRow(`SELECT count(*) FROM kb_drafts WHERE repo_id NOT IN (SELECT id FROM kb_repos)`).Scan(&orphan)
	if orphan != 0 {
		t.Fatalf("%d drafts point at a removed repository", orphan)
	}
	if _, err := st.Read().Exec(`SELECT 1`); err != nil {
		t.Fatal(err)
	}
}

func TestGitHub_RepoWithEmptySlugGetsAFallbackSnippetID(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fixture.name = "___"
	reply := `{"project":{"title":"Underscore tool","overview":"Does things."},"stories":[{"title":"!!!","principles":["ownership"],"situation":"A.","task":"B.","action":"C.","result":"D.","facts":[]}]}`
	fx := fixture.service(t, func(o *Options) { o.Model = &fakeModel{reply: reply} })
	repo, rep := ingestOnce(t, fx, "alex-example/___", false)
	if len(rep.Drafts) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	want := fmt.Sprintf("repo-%d", repo.ID)
	var project, story Draft
	for _, id := range rep.Drafts {
		d, _ := fx.svc.GetDraft(fx.ctx, id)
		if d.Kind == DraftStory {
			story = d
		} else {
			project = d
		}
	}
	if project.SnippetID != want || !strings.HasPrefix(story.SnippetID, want) {
		t.Fatalf("project id %q, story id %q", project.SnippetID, story.SnippetID)
	}
}
