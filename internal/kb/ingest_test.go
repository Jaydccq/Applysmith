package kb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func basicFiles() map[string]string {
	return map[string]string{
		"README.md":      "# Reef tool\n\nSurvey exporter for coastal research teams.\n",
		"go.mod":         "module example.com/reef\n\ngo 1.22\n",
		"cmd/main.go":    "package main\n\nfunc main() {}\n\nfunc Export() {}\n",
		"docs/design.md": "# Design\n\nThe exporter batches readings every 15 minutes.\n",
	}
}

func ingestOnce(t *testing.T, fx *fixture, ref string, force bool) (Repo, IngestReport) {
	t.Helper()
	repo, err := fx.svc.AddRepo(fx.ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := fx.svc.IngestRepo(fx.ctx, repo.ID, force)
	if err != nil {
		t.Fatalf("ingest: %v (%+v)", err, rep)
	}
	got, _ := fx.svc.GetRepo(fx.ctx, repo.ID)
	return got, rep
}

func TestGitHub_HTTPSOnly(t *testing.T) {
	if _, err := newGitHub("http://api.example.test", "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plain http accepted: %v", err)
	}
	if _, err := newGitHub("https://api.example.test", "", false); err != nil {
		t.Fatal(err)
	}
}

func TestGitHub_FileSelection_DenyWins(t *testing.T) {
	files := basicFiles()
	files["vendor/lib/README.md"] = "# vendored readme\n"
	files["package-lock.json"] = "{}"
	files["app.min.js"] = "var a=1;"
	files["app.js.map"] = "{}"
	files[".env"] = "SECRET_NAME=value\n"
	files[".env.example"] = "SERVICE_URL=https://example.test\n"
	files["assets/logo.png"] = "not really a png"
	files["notes.exe"] = "MZ"
	files["docs/binary.txt"] = "text\x00with a nul byte"
	files["node_modules/x/index.js"] = "module.exports = 1"
	files["src/big.go"] = "package big\n// " + strings.Repeat("x", 130<<10) + "\n"
	fixture := newGHFixture(t, files)
	fx := fixture.service(t)
	repo, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if repo.Status != "ingested" || rep.Listing != "api" {
		t.Fatalf("repo = %+v", repo)
	}
	blobs := map[string]string{}
	rows, _ := fx.st.Read().QueryContext(fx.ctx, `SELECT path, status FROM kb_repo_blobs`)
	for rows.Next() {
		var p, s string
		rows.Scan(&p, &s)
		blobs[p] = s
	}
	rows.Close()
	for _, p := range []string{"README.md", "go.mod", "cmd/main.go", "docs/design.md", ".env.example"} {
		if blobs[p] != "ok" {
			t.Errorf("%s status %q", p, blobs[p])
		}
	}
	for _, p := range []string{"vendor/lib/README.md", "package-lock.json", "app.min.js", "app.js.map", ".env", "assets/logo.png", "notes.exe", "node_modules/x/index.js"} {
		if _, ok := blobs[p]; ok {
			t.Errorf("%s was read", p)
		}
	}
	if blobs["docs/binary.txt"] != "binary" {
		t.Errorf("content-level binary check: %q", blobs["docs/binary.txt"])
	}
	big := false
	for _, s := range rep.Skipped {
		big = big || (s.Path == "src/big.go" && s.Reason == "larger than 128 KiB")
	}
	if !big {
		t.Errorf("skipped = %+v", rep.Skipped)
	}
	if fixture.count("/repos/alex-example/reef-tool/git/blobs/") > len(blobs)+1 {
		t.Errorf("blobs of denied files were fetched")
	}
}

func TestGitHub_Bounds_FileCapRanked(t *testing.T) {
	files := map[string]string{"README.md": "# Big repo\n"}
	for i := 0; i < 300; i++ {
		files[fmt.Sprintf("src/pkg%03d/file.go", i)] = fmt.Sprintf("package p%d\n\nfunc F%d() {}\n", i, i)
	}
	fixture := newGHFixture(t, files)
	fx := fixture.service(t)
	_, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if rep.Read+len(rep.Redacted) != 80 {
		t.Fatalf("read %d files", rep.Read)
	}
	capped := 0
	for _, s := range rep.Skipped {
		if s.Reason == "file cap" {
			capped++
		}
	}
	if capped != 221 {
		t.Fatalf("file-cap skips = %d", capped)
	}
	var readme int
	fx.st.Read().QueryRowContext(fx.ctx, `SELECT count(*) FROM kb_repo_blobs WHERE path = 'README.md'`).Scan(&readme)
	if readme != 1 {
		t.Fatal("the README was not ranked into the cap")
	}
	// The same input picks the same files.
	_, again := ingestOnce(t, fx, "alex-example/reef-tool", true)
	if fmt.Sprint(again.Skipped) != fmt.Sprint(rep.Skipped) {
		t.Fatal("the cap is not deterministic")
	}
}

func TestGitHub_RateLimitBackoff(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fixture.blobFail = 2
	fx := fixture.service(t)
	_, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if rep.Read != 4 {
		t.Fatalf("report = %+v", rep)
	}
	// A rate limit that never lifts ends the read with ErrRateLimited.
	fixture2 := newGHFixture(t, basicFiles())
	fixture2.blobFail = 100
	fx2 := fixture2.service(t)
	repo, _ := fx2.svc.AddRepo(fx2.ctx, "alex-example/reef-tool")
	// It is a kind of unavailability, so the API answers "unavailable, retry"
	// rather than an internal error.
	if _, err := fx2.svc.IngestRepo(fx2.ctx, repo.ID, false); !errors.Is(err, ErrRateLimited) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("persistent limit = %v", err)
	}
}

// A run that fails on one blob keeps the blobs it did fetch, so the next run
// asks only for the rest.
func TestGitHub_FailedBlobKeepsTheFetchedOnes(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fixture.blobGone = gitOut(t, fixture.work, "rev-parse", "HEAD:docs/design.md")
	fixture.goneAfter = len(basicFiles()) - 1
	fx := fixture.service(t)
	repo, err := fx.svc.AddRepo(fx.ctx, "alex-example/reef-tool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.IngestRepo(fx.ctx, repo.ID, false); err == nil {
		t.Fatal("a missing blob did not fail the run")
	}
	fixture.mu.Lock()
	fixture.blobGone = ""
	fixture.mu.Unlock()
	// The failure cancels the reads still in flight, so the first run kept at
	// least one blob and at most the three that were served.
	rep, err := fx.svc.IngestRepo(fx.ctx, repo.ID, false)
	if err != nil || rep.Reused < 1 || rep.Fetched < 1 || rep.Reused+rep.Fetched != 4 {
		t.Fatalf("second run = %+v, %v", rep, err)
	}
}

func TestGitHub_SecretInFile_Redacted(t *testing.T) {
	key := "-----" + "BEGIN RSA PRIVATE" + " KEY-----\nMIIBq7Zr4TbX9mLw2KdVn8Ys3HcFa5JgP6Ue\n-----" + "END RSA PRIVATE KEY-----\n"
	files := basicFiles()
	files["cmd/deploy.sh"] = "#!/bin/sh\n" + key
	fixture := newGHFixture(t, files)
	model := &fakeModel{reply: `{"project":{"title":"Reef tool","overview":"Exports survey data."},"stories":[]}`}
	fx := fixture.service(t, func(o *Options) { o.Model = model })
	_, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if len(rep.Redacted) != 1 || rep.Redacted[0].Path != "cmd/deploy.sh" || rep.Redacted[0].Reason == "" {
		t.Fatalf("redacted = %+v", rep.Redacted)
	}
	for _, p := range model.prompts {
		if strings.Contains(p, "MIIBq7Zr") || strings.Contains(p, "deploy.sh ===") {
			t.Fatal("the model saw the file with the key")
		}
	}
	assertNoText(t, fx, "MIIBq7Zr4TbX9mLw2KdVn8Ys3HcFa5JgP6Ue")
}

// assertNoText fails when the text appears in the database or the workspace.
func assertNoText(t *testing.T, fx *fixture, needle string) {
	t.Helper()
	fx.st.Close()
	err := filepath.Walk(fx.ws.Root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), needle) {
				t.Errorf("%s holds %q", p, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGitHub_TruncatedTree_FallsBackToClone(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fixture.truncated = true
	fx := fixture.service(t)
	_, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if rep.Listing != "clone" || rep.Read != 4 {
		t.Fatalf("report = %+v", rep)
	}
	// The truncated tree was asked for once; the clone listed the files.
	if got := fixture.count("/repos/alex-example/reef-tool/git/trees/"); got != 1 {
		t.Fatalf("tree requests = %d", got)
	}
}

func TestGitHub_TruncatedTree_NoCloneNoPartialListing(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fixture.truncated = true
	fx := fixture.service(t)
	fx.svc.cloneSeam = &cloner{url: func(string, string) string { return "file:///nonexistent/repo.git" }, protocols: "file"}
	repo, _ := fx.svc.AddRepo(fx.ctx, "alex-example/reef-tool")
	if _, err := fx.svc.IngestRepo(fx.ctx, repo.ID, false); err == nil {
		t.Fatal("a partial listing was used")
	}
	var n int
	fx.st.Read().QueryRowContext(fx.ctx, `SELECT count(*) FROM kb_repo_blobs`).Scan(&n)
	if n != 0 || fixture.count("/repos/alex-example/reef-tool/git/blobs/") != 0 {
		t.Fatal("files were chosen from the partial listing")
	}
}

func TestGitHub_UnchangedCommitSkipped_ForceReusesBlobs(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fx := fixture.service(t)
	repo, first := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if first.Fetched != 4 || repo.CommitSHA != fixture.sha {
		t.Fatalf("first = %+v", first)
	}
	trees, blobs := fixture.count("/repos/alex-example/reef-tool/git/trees/"), fixture.count("/repos/alex-example/reef-tool/git/blobs/")
	_, second := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if second.Status != IngestUpToDate || fixture.count("/repos/alex-example/reef-tool/git/trees/") != trees || fixture.count("/repos/alex-example/reef-tool/git/blobs/") != blobs {
		t.Fatalf("unchanged commit was fetched again: %+v", second)
	}
	_, forced := ingestOnce(t, fx, "alex-example/reef-tool", true)
	if forced.Fetched != 0 || forced.Reused != 4 {
		t.Fatalf("forced = %+v", forced)
	}
	fixture.commit(map[string]string{"cmd/main.go": "package main\n\nfunc main() { run() }\n"}, nil)
	_, third := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if third.Status != IngestDone || third.Fetched != 1 || third.Reused != 3 {
		t.Fatalf("changed commit = %+v", third)
	}
}

func TestGitHub_PinnedCommit(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fx := fixture.service(t)
	pinned := fixture.sha
	ingestOnce(t, fx, "alex-example/reef-tool", false)
	if fixture.count("/repos/alex-example/reef-tool/git/trees/"+pinned) != 1 {
		t.Fatalf("hits = %v", fixture.hits)
	}
	if fixture.count("/repos/alex-example/reef-tool/commits/") != 1 {
		t.Fatal("the branch tip was resolved more than once")
	}
}

func TestGitHub_TokenNeverPersisted(t *testing.T) {
	token := "ghp" + "_" + "q7Zr4TbX9mLw2KdVn8Ys3HcFa5JgP6UeAbCd"
	fixture := newGHFixture(t, basicFiles())
	fixture.truncated = true
	model := &fakeModel{reply: `{"project":{"title":"Reef tool","overview":"Exports survey data."},"stories":[]}`}
	fx := fixture.service(t, func(o *Options) {
		o.Model = model
		o.GitHubToken = func(context.Context) string { return token }
	})
	_, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	sawAuth := false
	for _, a := range fixture.auth {
		sawAuth = sawAuth || a == "Bearer "+token
	}
	if !sawAuth {
		t.Fatal("the token was not sent")
	}
	b, _ := marshalForTest(rep)
	if strings.Contains(string(b), token) {
		t.Fatal("the report holds the token")
	}
	assertNoText(t, fx, token)
}

func TestGitHub_MissingRepoIsUnavailable(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fx := fixture.service(t)
	repo, _ := fx.svc.AddRepo(fx.ctx, "alex-example/other-repo")
	if _, err := fx.svc.IngestRepo(fx.ctx, repo.ID, false); !errors.Is(err, ErrRepoUnavailable) {
		t.Fatalf("err = %v", err)
	}
	got, _ := fx.svc.GetRepo(fx.ctx, repo.ID)
	if got.Status != "failed" {
		t.Fatalf("status = %s", got.Status)
	}
}

const repoReply = `{"project":{"title":"Reef tool","description":"Survey exporter","overview":"Exports survey data for coastal teams.","architecture":"One binary.","tech":"Go"},
"stories":[
 {"title":"Batching the exporter","principles":["technical-depth","ownership","not-a-principle"],
  "situation":"Readings arrived faster than the exporter could write them.","task":"I had to keep the exporter from dropping readings under a deadline.",
  "action":"I decided to batch readings every 15 minutes because the risk of dropped data was higher than the delay.","result":"The exporter stopped dropping readings.",
  "facts":[{"text":"Readings are batched every 15 minutes","path":"docs/design.md","quote":"batches readings every 15 minutes","metric":true},
           {"text":"Owned 412 commits","path":"README.md","quote":"not in the file","metric":true}]},
 {"title":"","principles":[],"situation":"x","task":"x","action":"x","result":"x","facts":[]}
]}`

func TestGitHub_DraftsOnly_NothingPublishedAutomatically(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	model := &fakeModel{reply: repoReply}
	fx := fixture.service(t, func(o *Options) { o.Model = model })
	repo, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if len(rep.Drafts) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if r := fx.search("exporter batches readings coastal survey"); len(r.Facts) != 0 {
		t.Fatalf("a draft is retrievable: %v", ids(r))
	}
	drafts, _ := fx.svc.ListDrafts(fx.ctx, false)
	var story, project Draft
	for _, d := range drafts {
		if d.Kind == DraftStory {
			story = d
		} else {
			project = d
		}
		if d.Origin != OriginRepo || d.RepoID != repo.ID || d.Status != StatusDraft {
			t.Errorf("draft = %+v", d)
		}
	}
	if project.Category != CatProjects || !strings.Contains(project.Markdown, "Exports survey data") {
		t.Fatalf("project = %+v", project)
	}
	sn, err := ParseSnippet("draft.md", []byte(story.Markdown))
	if err != nil || sn.StorySourceType != "project" || !equal(sn.Principles, []string{"technical-depth", "ownership"}) {
		t.Fatalf("story = %v\n%s", err, story.Markdown)
	}
	// The cited metric is promotable; the unsupported one is inferred and is not.
	var measured, inferred bool
	for _, f := range story.Facts {
		measured = measured || (f.Origin == FactMeasured && f.promotable())
		inferred = inferred || (f.Origin == FactInferred && f.Metric && !f.promotable())
	}
	if !measured || !inferred {
		t.Fatalf("facts = %+v", story.Facts)
	}
	// Publishing the story promotes only what is supported.
	if _, err := fx.svc.PublishStory(fx.ctx, story.ID); err != nil {
		t.Fatalf("publish story: %v", err)
	}
	if file := fx.read("knowledge/behavioral/" + Slug(story.SnippetID) + ".md"); strings.Contains(file, "412") || !strings.Contains(file, "batched every 15 minutes") {
		t.Fatalf("published story:\n%s", file)
	}
}

func TestGitHub_StoryWithoutMeasuredMetricKeepsTODO(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	reply := `{"project":{},"stories":[{"title":"Quiet story","principles":["ownership","delivery"],"situation":"A.","task":"B.","action":"C.","result":"D.","facts":[]}]}`
	fx := fixture.service(t, func(o *Options) { o.Model = &fakeModel{reply: reply} })
	_, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	d, _ := fx.svc.GetDraft(fx.ctx, rep.Drafts[0])
	if !strings.Contains(d.Markdown, "TODO") {
		t.Fatalf("no TODO marker:\n%s", d.Markdown)
	}
	if _, err := fx.svc.PublishStory(fx.ctx, d.ID); !isGate(err) {
		t.Fatalf("publish = %v", err)
	}
}

// A metric is measured only when each of its numbers is a whole number of
// the quote: "5" is not in "250ms" or "2025".
func TestGitHub_MeasuredNeedsTheWholeNumberInTheQuote(t *testing.T) {
	files := map[string]string{"NOTES.md": "In 2025 the p95 dropped to 250ms after the cache landed; 1,200 jobs ran."}
	for _, c := range []struct {
		text, quote string
		want        string
	}{
		{"cut latency by 5%", "In 2025 the p95 dropped to 250ms", FactInferred},
		{"p95 dropped to 25ms", "the p95 dropped to 250ms", FactInferred},
		{"p95 dropped to 250ms", "the p95 dropped to 250ms", FactMeasured},
		{"ran 1,200 jobs.", "1,200 jobs ran.", FactMeasured},
	} {
		df, _ := gradeFact(modelFact{Text: c.text, Path: "NOTES.md", Quote: c.quote, Metric: true}, c.text, files)
		if df.Origin != c.want {
			t.Errorf("%q against %q = %s, want %s", c.text, c.quote, df.Origin, c.want)
		}
	}
}

// Stories whose titles have no letters or digits still get one id each, kept
// within 80 characters with the number intact.
func TestGitHub_UntitledStoriesGetDistinctIDs(t *testing.T) {
	st := modelStory{Title: "???", Situation: "A.", Task: "B.", Action: "C.", Result: "D."}
	long := strings.Repeat("reef", 20)
	seen := map[string]bool{}
	for _, base := range []string{"reef-tool", long} {
		for n := 1; n <= 2; n++ {
			nd, ok := storyDraftFrom(Repo{}, base, st, nil, nil, n)
			if !ok || seen[nd.SnippetID] || len(nd.SnippetID) > 80 || !strings.HasSuffix(nd.SnippetID, fmt.Sprintf("-story-%d", n)) {
				t.Fatalf("story %d of %q: id %q (seen before: %v)", n, base, nd.SnippetID, seen[nd.SnippetID])
			}
			seen[nd.SnippetID] = true
		}
	}
}

func TestDrafts_OnePerRun_RepoRebuildInPlace(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	model := &fakeModel{reply: repoReply}
	fx := fixture.service(t, func(o *Options) { o.Model = model })
	_, first := ingestOnce(t, fx, "alex-example/reef-tool", false)
	_, again := ingestOnce(t, fx, "alex-example/reef-tool", true)
	if len(again.Drafts) != 2 || again.Drafts[0] != first.Drafts[0] {
		t.Fatalf("project draft was not updated in place: %v vs %v", first.Drafts, again.Drafts)
	}
	list, _ := fx.svc.ListDrafts(fx.ctx, false)
	if len(list) != 2 {
		t.Fatalf("drafts = %d", len(list))
	}
	// After the project draft is published, rebuilding it drafts an update of
	// the published page that names the hash of the file it replaces.
	if _, err := fx.svc.PublishDraft(fx.ctx, first.Drafts[0]); err != nil {
		t.Fatal(err)
	}
	_, third := ingestOnce(t, fx, "alex-example/reef-tool", true)
	update, err := fx.svc.GetDraft(fx.ctx, third.Drafts[0])
	if err != nil || update.SnippetID != "reef-tool" || update.BaseHash != fileHash([]byte(fx.read("knowledge/projects/reef-tool.md"))) {
		t.Fatalf("update = %+v, %v (notes %v)", update, err, third.Notes)
	}
}

func TestKB_RepoRemoved_DraftsGoPublishedStay(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fx := fixture.service(t, func(o *Options) { o.Model = &fakeModel{reply: repoReply} })
	repo, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if _, err := fx.svc.PublishDraft(fx.ctx, rep.Drafts[0]); err != nil {
		t.Fatal(err)
	}
	repos, _ := fx.svc.ListRepos(fx.ctx)
	before, _ := fx.svc.ListDrafts(fx.ctx, true)
	var blobs int
	fx.st.Read().QueryRowContext(fx.ctx, `SELECT count(*) FROM kb_repo_blobs`).Scan(&blobs)
	token, err := fx.svc.RemoveRepo(fx.ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Undo brings the source, its cached files and its drafts back.
		if _, err := fx.svc.Restore(fx.ctx, token); err != nil {
			t.Fatal(err)
		}
		var n int
		fx.st.Read().QueryRowContext(fx.ctx, `SELECT count(*) FROM kb_repo_blobs`).Scan(&n)
		r, _ := fx.svc.ListRepos(fx.ctx)
		d, _ := fx.svc.ListDrafts(fx.ctx, true)
		if n != blobs || !reflect.DeepEqual(r, repos) || !reflect.DeepEqual(d, before) {
			t.Fatalf("restored %d files, %+v, %+v", n, r, d)
		}
	}()
	drafts, _ := fx.svc.ListDrafts(fx.ctx, true)
	for _, d := range drafts {
		if d.Status != StatusPublished {
			t.Errorf("unpublished draft survived: %+v", d)
		}
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts = %d", len(drafts))
	}
	if r := fx.search("exports survey data coastal teams"); len(r.Facts) == 0 {
		t.Fatal("the published snippet went with the repository")
	}
	var n int
	fx.st.Read().QueryRowContext(fx.ctx, `SELECT count(*) FROM kb_repo_blobs`).Scan(&n)
	if n != 0 {
		t.Fatal("cached files survive")
	}
	if _, err := fx.svc.RemoveRepo(fx.ctx, repo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove = %v", err)
	}
}

func TestGitHub_ModelFailureFailsTheIngestionAndTheNextReadDrafts(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	model := &fakeModel{err: errors.New("provider down")}
	fx := fixture.service(t, func(o *Options) { o.Model = model })
	repo, err := fx.svc.AddRepo(fx.ctx, "alex-example/reef-tool")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := fx.svc.IngestRepo(fx.ctx, repo.ID, false)
	if err == nil || rep.Status != "failed" || len(rep.Drafts) != 0 || !strings.Contains(strings.Join(rep.Notes, " "), "drafting failed") {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	if got, _ := fx.svc.GetRepo(fx.ctx, repo.ID); got.Status != "failed" || got.CommitSHA != "" {
		t.Fatalf("the repository is recorded as %s at %q", got.Status, got.CommitSHA)
	}
	// The same commit is read again, from the kept files, and drafted.
	model.err, model.reply = nil, repoReply
	blobs := fixture.count("/repos/alex-example/reef-tool/git/blobs/")
	got, again := ingestOnce(t, fx, "alex-example/reef-tool", false)
	if again.Status != IngestDone || len(again.Drafts) != 2 || got.Status != "ingested" || got.CommitSHA != fixture.sha {
		t.Fatalf("second read = %+v, repository %s at %q", again, got.Status, got.CommitSHA)
	}
	if fixture.count("/repos/alex-example/reef-tool/git/blobs/") != blobs || again.Reused != 4 {
		t.Fatalf("the second read fetched files again: %+v", again)
	}
}

func TestKB_InventoryExportAndRemoveCoverRepoAndSessions(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	fx := fixture.service(t, func(o *Options) { o.Model = &fakeModel{reply: repoReply} })
	repo, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	m = mineAll(t, fx, m.ID)
	fx.svc.SaveMining(fx.ctx, m.ID)
	fx.search("quantum lattice research")

	inv, err := fx.svc.Inventory(fx.ctx)
	if err != nil || len(inv.Repos) != 1 || inv.Repos[0].CommitSHA != fixture.sha || len(inv.Sessions) != 1 || len(inv.Drafts) != 3 || len(inv.Gaps) != 1 {
		t.Fatalf("inventory = %+v, %v", inv, err)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := fx.svc.Export(fx.ctx, out); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"repos.json": fixture.sha, "sessions.json": "on-call rotation", "drafts.json": "Reef tool", "answers.json": "[]", "gaps.json": "quantum"} {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s: %v\n%s", name, err, b)
		}
	}
	for _, ref := range []string{"session:" + m.ID, fmt.Sprintf("draft:%d", rep.Drafts[0]), "cache:gaps", "cache:embeddings", fmt.Sprintf("repo:%d", repo.ID)} {
		if err := fx.svc.Remove(fx.ctx, ref); err != nil {
			t.Errorf("remove %s: %v", ref, err)
		}
	}
	inv, _ = fx.svc.Inventory(fx.ctx)
	if len(inv.Repos) != 0 || len(inv.Sessions) != 0 || len(inv.Gaps) != 0 || len(inv.Drafts) != 0 {
		t.Fatalf("after remove = %+v", inv)
	}
	if err := fx.svc.Remove(fx.ctx, "repo:abc"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad ref = %v", err)
	}
}

const pagesReply = `{"project":{"title":"Reef tool","overview":"Exports survey data.",
  "points":[{"text":"Readings are batched every 15 minutes","path":"docs/design.md","quote":"batches readings every 15 minutes","metric":true},
            {"text":"Runs on 3000 sensors","path":"README.md","quote":"not in the file"}],
  "related":["example-corp","not-listed"]},
 "pages":[{"id":"example-corp","summary":"The exporter serves the survey data systems of this role.",
           "points":[{"text":"Built for coastal research teams","path":"README.md","quote":"Survey exporter for coastal research teams."}]},
          {"id":"not-listed","summary":"x","points":[{"text":"y","path":"README.md","quote":"Reef tool"}]}],
 "stories":[]}`

func TestGitHub_RepositoryReadEditsExistingPagesAndPromotesOnlyCitedPoints(t *testing.T) {
	fixture := newGHFixture(t, basicFiles())
	model := &fakeModel{reply: pagesReply}
	fx := fixture.service(t, func(o *Options) { o.Model = model })
	const page = "knowledge/experience/example-corp.md"
	fx.write(page, snippet("id: example-corp\ntitle: Example Corp\nallowed_facts:\n  - Backend engineer at Example Corp since 2021",
		"## Role\nBackend engineer at Example Corp, building survey and research data systems."))
	repo, rep := ingestOnce(t, fx, "alex-example/reef-tool", false)

	// The digest call was given the page the repository touches.
	if p := model.prompts[0]; !strings.Contains(p, "PAGE id=example-corp category=experience") || !strings.Contains(p, "building survey and research data systems") {
		t.Fatalf("the prompt lacks the existing page:\n%s", p)
	}
	notes := strings.Join(rep.Notes, " ")
	if !strings.Contains(notes, "1 key points of the project page were dropped") || !strings.Contains(notes, `"not-listed" was dropped`) {
		t.Fatalf("notes = %v", rep.Notes)
	}
	if len(rep.Drafts) != 2 {
		t.Fatalf("drafts = %v (%v)", rep.Drafts, rep.Notes)
	}
	project, _ := fx.svc.GetDraft(fx.ctx, rep.Drafts[0])
	update, _ := fx.svc.GetDraft(fx.ctx, rep.Drafts[1])
	if update.SnippetID != "example-corp" || update.Category != CatExperience || update.BaseHash != fileHash([]byte(fx.read(page))) {
		t.Fatalf("update = %+v", update)
	}
	for _, want := range []string{"Backend engineer at Example Corp, building survey", "## alex-example/reef-tool (repository)", "Built for coastal research teams (README.md)", "[Reef tool](/projects/reef-tool.md)"} {
		if !strings.Contains(update.Markdown, want) {
			t.Errorf("update lacks %q:\n%s", want, update.Markdown)
		}
	}
	if !strings.Contains(project.Markdown, "- Readings are batched every 15 minutes (docs/design.md)") || strings.Contains(project.Markdown, "3000") ||
		!strings.Contains(project.Markdown, "[Example Corp](/experience/example-corp.md)") || strings.Contains(project.Markdown, "not-listed") {
		t.Fatalf("project:\n%s", project.Markdown)
	}

	// Publishing promotes the cited point of the project and keeps the facts
	// the page had.
	if _, err := fx.svc.PublishDraft(fx.ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	sn, err := ParseSnippet("p.md", []byte(fx.read("knowledge/projects/reef-tool.md")))
	if err != nil || !equal(sn.AllowedFacts, []string{"Readings are batched every 15 minutes"}) {
		t.Fatalf("project facts = %v, %v", sn.AllowedFacts, err)
	}

	// The user edits the page before the update is published: publishing it is
	// a conflict and the user's text stays.
	edited := strings.Replace(fx.read(page), "building survey", "leading survey", 1)
	fx.write(page, edited)
	if _, err := fx.svc.PublishDraft(fx.ctx, update.ID); !errors.Is(err, ErrStale) || !errors.Is(err, ErrConflict) {
		t.Fatalf("publish over a changed page = %v", err)
	}
	if fx.read(page) != edited {
		t.Fatal("a refused publish changed the page")
	}

	// Reading the repository again remakes the update from the page as it is.
	again, err := fx.svc.IngestRepo(fx.ctx, repo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var redone Draft
	for _, id := range again.Drafts {
		if d, _ := fx.svc.GetDraft(fx.ctx, id); d.SnippetID == "example-corp" {
			redone = d
		}
	}
	if redone.ID == update.ID || redone.BaseHash != fileHash([]byte(edited)) || !strings.Contains(redone.Markdown, "leading survey") {
		t.Fatalf("redone = %+v", redone)
	}
	pub, err := fx.svc.PublishDraft(fx.ctx, redone.ID)
	if err != nil || len(pub.NotPromoted) != 0 {
		t.Fatalf("publish = %+v, %v", pub, err)
	}
	sn, err = ParseSnippet("p.md", []byte(fx.read(page)))
	if err != nil || !equal(sn.AllowedFacts, []string{"Backend engineer at Example Corp since 2021", "Built for coastal research teams"}) ||
		!strings.Contains(fx.read(page), "leading survey") || !strings.Contains(fx.read(page), "## alex-example/reef-tool (repository)") {
		t.Fatalf("page:\n%s\n%v", fx.read(page), err)
	}

	// A read whose model names the project differently replaces the section
	// instead of adding a second one.
	model.mu.Lock()
	model.reply = strings.Replace(pagesReply, `"title":"Reef tool"`, `"title":"Reef survey exporter"`, 1)
	model.mu.Unlock()
	third, err := fx.svc.IngestRepo(fx.ctx, repo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range third.Drafts {
		if d, _ := fx.svc.GetDraft(fx.ctx, id); d.SnippetID == "example-corp" {
			if _, err := fx.svc.PublishDraft(fx.ctx, d.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := strings.Count(fx.read(page), "(repository)\n"); n != 1 {
		t.Fatalf("%d repository sections:\n%s", n, fx.read(page))
	}
}
