package api

import (
	"applysmith/internal/api/wire"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/kb"
	"applysmith/internal/workspace"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (h *harness) patchDoc(id string, body any) result {
	return h.send("PATCH", "/api/kb/documents/"+id, body)
}

func TestKnowledgeDocumentsCreateListReviseAndDelete(t *testing.T) {
	h := newDomainHarness(t)
	made := h.post("/api/kb/documents", map[string]any{"title": "Reef survey summary", "category": "projects", "tags": []string{"data"}, "body": "I led a small survey team."})
	if made.StatusCode != 201 {
		t.Fatalf("%d %s", made.StatusCode, made.body)
	}
	entry := made.data()["entry"].(map[string]any)
	id, hash := entry["id"].(string), entry["fileHash"].(string)
	list := h.get("/api/kb/documents").data()["entries"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["title"] != "Reef survey summary" {
		t.Fatalf("%v", list)
	}
	rev := h.patchDoc(id, map[string]any{"expectedHash": hash, "tags": []string{"data", "field"}, "body": "I led a survey team of four."})
	if rev.StatusCode != 200 || rev.data()["body"] != "I led a survey team of four." {
		t.Fatalf("%d %s", rev.StatusCode, rev.body)
	}
	if stale := h.patchDoc(id, map[string]any{"expectedHash": hash, "body": "late"}); stale.StatusCode != 409 || stale.errCode() != "revision_conflict" {
		t.Fatalf("stale edit: %d %s", stale.StatusCode, stale.body)
	}
	del := h.send("DELETE", "/api/kb/documents/"+id, nil)
	if del.StatusCode != 200 || len(h.get("/api/kb/documents").data()["entries"].([]any)) != 0 {
		t.Fatalf("%d %s", del.StatusCode, del.body)
	}
	// The delete's token undoes it once.
	undo := map[string]any{"undo": del.data()["undo"]}
	if back := h.post("/api/kb/restore", undo); back.StatusCode != 200 || h.get("/api/kb/documents/" + id).data()["body"] != "I led a survey team of four." {
		t.Fatalf("restore: %d %s", back.StatusCode, back.body)
	}
	if again := h.post("/api/kb/restore", undo); again.StatusCode != 404 {
		t.Fatalf("second restore: %d %s", again.StatusCode, again.body)
	}
}

// The KB keeps the text the last save replaced.
func TestKnowledgePreviousVersionIsWhatTheLastSaveReplaced(t *testing.T) {
	h := newDomainHarness(t)
	made := h.post("/api/kb/documents", map[string]any{"title": "Reef survey summary", "category": "projects", "tags": []string{"data"}, "body": "I led a small survey team."})
	entry := made.data()["entry"].(map[string]any)
	id, hash := entry["id"].(string), entry["fileHash"].(string)
	if none := h.get("/api/kb/documents/" + id + "/previous"); none.StatusCode != 404 {
		t.Fatalf("before any save: %d %s", none.StatusCode, none.body)
	}
	h.patchDoc(id, map[string]any{"expectedHash": hash, "title": "Reef survey", "body": "A bad save."})
	prev := h.get("/api/kb/documents/" + id + "/previous")
	got := prev.data()
	if prev.StatusCode != 200 || got["body"] != "I led a small survey team." || got["title"] != "Reef survey summary" || fmt.Sprint(got["tags"]) != "[data]" || got["savedAt"] == "" {
		t.Fatalf("previous: %d %s", prev.StatusCode, prev.body)
	}
}

// A brief on disk is listed under the kind the dashboard's Briefs tab keeps
// (wire.KBDocBrief) and a page under wire.KBDocSnippet, so neither tab lists
// nothing while the files are there.
func TestKnowledgeListingNamesBriefsAndPagesByTheWireKinds(t *testing.T) {
	h := newDomainHarness(t)
	dir := filepath.Join(h.ws.Root, "knowledge", "companies")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	brief := "---\ntitle: Example Systems company brief\nsource_url: https://www.example.com/\nfetched_at: \"2026-10-30T09:00:00Z\"\n---\n\nExample Systems builds ledgers.\n"
	if err := os.WriteFile(filepath.Join(dir, "example.com.md"), []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.post("/api/kb/documents", map[string]any{"title": "Ledger rebuild", "category": "projects", "body": "Rebuilt the ledger."}); r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	kinds := map[string]string{}
	for _, e := range h.get("/api/kb/documents").data()["entries"].([]any) {
		m := e.(map[string]any)
		kinds[m["id"].(string)] = m["kind"].(string)
	}
	if kinds["example.com"] != wire.KBDocBrief || kinds["ledger-rebuild"] != wire.KBDocSnippet {
		t.Fatalf("listed kinds %v", kinds)
	}
}

func TestKnowledgeBriefNeedsAPostingLinkAndNeverGuessesASite(t *testing.T) {
	h := newDomainHarness(t)
	r := h.send("POST", "/api/kb/briefs", map[string]any{"company": "Example Systems"}, "Idempotency-Key", "key-brief-0001")
	if r.StatusCode != 422 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestKnowledgeStoryIsStoredThroughTheGatesAndAnUnknownThemeIsRefused(t *testing.T) {
	h := newDomainHarness(t)
	story := map[string]any{"title": "Quieting the pager", "sourceType": "work", "principles": []string{"ownership"},
		"situation": "The pager fired all night.", "task": "I had to cut the noise before the launch deadline.",
		"action": "I decided to group alerts by cause because the risk of missing a real outage was higher than the cost of one tuning week.",
		"result": "I cut 200 alerts to 15 alerts and the median page time fell to 12 minutes."}
	r := h.post("/api/kb/stories", story)
	if r.StatusCode != 201 || r.data()["draftId"] == nil {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	story["title"], story["principles"] = "Another story", []string{"no-such-theme"}
	if bad := h.post("/api/kb/stories", story); bad.StatusCode != 422 {
		t.Fatalf("unknown theme: %d %s", bad.StatusCode, bad.body)
	}
}

// holdingModel answers a story request once released, or stops with its
// context.
type holdingModel struct {
	asked   chan struct{}
	release chan string
}

func (m holdingModel) Generate(ctx context.Context, _, _ string) (string, error) {
	close(m.asked)
	select {
	case reply := <-m.release:
		return reply, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Reading the CV for stories is a model call of minutes, so it is a background
// run: the request returns with the run id while the model is still asked, a
// second press is refused, and what the run found is read afterwards.
func TestKnowledgeStoryProposalsAreABackgroundRun(t *testing.T) {
	h := newDomainHarness(t)
	model := holdingModel{asked: make(chan struct{}), release: make(chan string)}
	svc, err := kb.New(h.srv.store.Load(), kb.Options{Workspace: h.ws, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	h.domainOf().kb = svc
	if err := os.WriteFile(h.ws.Path(workspace.FileCV), []byte("# Alex Example\n\nLed the reef survey team and quieted the pager.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.send("POST", "/api/kb/stories/proposals", nil, "Idempotency-Key", "key-propose-0002")
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	select {
	case <-model.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the model was never asked")
	}
	if run := h.get("/api/runs/" + id).data(); run["kind"] != "kb.propose" || run["status"] != "running" {
		t.Errorf("run while the model works: %v", run)
	}
	if r := h.send("POST", "/api/kb/stories/proposals", nil, "Idempotency-Key", "key-propose-0003"); r.StatusCode != 409 {
		t.Errorf("a second press: %d %s", r.StatusCode, r.body)
	}
	model.release <- `{"stories":[{"title":"Quieting the pager","principles":[],"situation":"The pager fired all night.","task":"Cut the noise.","action":"I grouped alerts by cause.","result":"Pages fell."}]}`
	if run := h.waitRun(id); run["status"] != "succeeded" {
		t.Fatalf("run %v", run)
	}
	got := h.get("/api/kb/stories/proposals").data()["proposals"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["title"] != "Quieting the pager" {
		t.Errorf("proposals after the run: %v", got)
	}
	// A dismissal holds on a later read until it is undone; a saved story leaves too.
	p := got[0].(map[string]any)
	p["sourceType"] = "work"
	for _, step := range []struct {
		path  string
		title any
		code  int
		n     int
	}{{"/proposals/dismiss", p["title"], 200, 0}, {"/proposals/restore", p["title"], 200, 1}, {"", nil, 201, 0}} {
		body := map[string]any{"title": step.title}
		if step.path == "" {
			body = p
		}
		r := h.post("/api/kb/stories"+step.path, body)
		if n := len(h.get("/api/kb/stories/proposals").data()["proposals"].([]any)); r.StatusCode != step.code || n != step.n {
			t.Errorf("%q: %d %s, %d shown", step.path, r.StatusCode, r.body, n)
		}
	}
}

// A task with no model routed is refused at once with the reason the page
// links to Settings, Models; no run is made.
func TestKnowledgeStoryProposalsWithoutAModelRouteAreRefusedAtOnce(t *testing.T) {
	h := newRoutingHarness(t)
	r := h.send("POST", "/api/kb/stories/proposals", nil, "Idempotency-Key", "key-propose-0004")
	if r.StatusCode != 503 || r.errReason() != wire.ReasonNoModelRoute {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// blockedFetcher holds every page read until the run is stopped.
type blockedFetcher struct{ asked chan struct{} }

func (f blockedFetcher) Fetch(ctx context.Context, _ string) (*kb.Page, error) {
	select {
	case f.asked <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// A brief reads pages and calls a model, so it is a background run too: the
// request returns at once and a cancel stops it.
func TestKnowledgeBriefIsABackgroundRun(t *testing.T) {
	h := newDomainHarness(t)
	fetch := blockedFetcher{asked: make(chan struct{}, 4)}
	svc, err := kb.New(h.srv.store.Load(), kb.Options{Workspace: h.ws, Fetcher: fetch})
	if err != nil {
		t.Fatal(err)
	}
	h.domainOf().kb = svc
	body := map[string]any{"company": "Example Systems", "jobUrl": "https://jobs.example.com/role"}
	r := h.send("POST", "/api/kb/briefs", body, "Idempotency-Key", "key-brief-0002")
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	select {
	case <-fetch.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("no page was read")
	}
	if run := h.get("/api/runs/" + id).data(); run["kind"] != "kb.brief" || run["status"] != "running" {
		t.Errorf("run while the site is read: %v", run)
	}
	if r := h.post("/api/runs/"+id+"/cancel", nil); r.StatusCode != 202 {
		t.Fatalf("cancel: %d %s", r.StatusCode, r.body)
	}
	if run := h.waitRun(id); run["status"] != "cancelled" {
		t.Fatalf("run %v", run)
	}
}

func TestKnowledgePDFImportUsesProductionExtractorAndRequiresPublish(t *testing.T) {
	bin := t.TempDir()
	t.Setenv(toolchain.EnvToolPath, bin)
	h := newDomainHarness(t)
	upload := func(name string, data []byte) result {
		return h.do(call{method: "POST", path: "/api/kb/imports?name=" + name, useToken: true, contentType: "application/pdf", body: data})
	}
	if got := upload("harbor.pdf", []byte("%PDF-1.4")); got.StatusCode != 503 {
		t.Fatalf("missing Poppler: %d %s", got.StatusCode, got.body)
	}
	if err := os.WriteFile(filepath.Join(bin, "pdftotext"), []byte("#!/bin/sh\n/bin/cat >/dev/null\nprintf 'Harbor berth allocation solver'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	made := upload("harbor.pdf", []byte("%PDF-1.4"))
	if made.StatusCode != 201 {
		t.Fatalf("%d %s", made.StatusCode, made.body)
	}
	if !strings.Contains(made.data()["markdown"].(string), "Harbor berth allocation") {
		t.Fatalf("%s", made.body)
	}
	pub := h.post(fmt.Sprintf("/api/kb/drafts/%.0f/publish", made.data()["id"].(float64)), nil)
	if pub.StatusCode != 200 {
		t.Fatalf("publish: %d %s", pub.StatusCode, pub.body)
	}
	if got := upload("large.pdf", make([]byte, (5<<20)+1)); got.StatusCode != 413 {
		t.Fatalf("large: %d %s", got.StatusCode, got.body)
	}
}

func TestKnowledgeDraftQueueListsEditsConfirmsAndRejects(t *testing.T) {
	h := newDomainHarness(t)
	made := h.do(call{method: "POST", path: "/api/kb/imports?name=harbor.md", useToken: true, contentType: "text/markdown", body: []byte("Harbor solver notes.")})
	pending := []kb.DraftFact{{Text: "The pager fired.", Pending: true}, {Text: "I cut 200 alerts.", Pending: true}}
	story, err := h.domainOf().kb.CreateDraft(t.Context(), kb.NewDraft{Kind: kb.DraftStory, SnippetID: "pager", Markdown: "---\nid: pager\n---\nThe pager fired.\n", Facts: pending})
	if made.StatusCode != 201 || err != nil {
		t.Fatalf("%d %s %v", made.StatusCode, made.body, err)
	}
	var list wire.Success[wire.KBDraftList]
	if strictInto(t, h.get("/api/kb/drafts").body, &list); len(list.Data.Drafts) != 2 {
		t.Fatalf("queue: %+v", list.Data)
	}
	// A draft is read as its text: the frontmatter shows only in the editor.
	for _, d := range list.Data.Drafts {
		if d.SnippetID == "pager" && d.Body != "The pager fired." {
			t.Errorf("story body = %q", d.Body)
		}
	}
	conf := h.post(fmt.Sprintf("/api/kb/drafts/%d/confirm", story.ID), map[string]any{"facts": []int{1}})
	if f := conf.data()["facts"].([]any); f[0].(map[string]any)["pending"] != true || f[1].(map[string]any)["pending"] != false {
		t.Fatalf("confirm: %d %s", conf.StatusCode, conf.body)
	}
	id := fmt.Sprintf("/api/kb/drafts/%.0f", made.data()["id"].(float64))
	if ed := h.send("PATCH", id, map[string]any{"markdown": "Revised."}); ed.data()["markdown"] != "Revised." {
		t.Fatalf("edit: %d %s", ed.StatusCode, ed.body)
	}
	if del := h.send("DELETE", id, nil); del.StatusCode != 200 || len(h.get("/api/kb/drafts").data()["drafts"].([]any)) != 1 || h.post(id+"/publish", nil).StatusCode != 404 {
		t.Fatalf("reject: %d %s", del.StatusCode, del.body)
	}
}
