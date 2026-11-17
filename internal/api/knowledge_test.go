package api

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	"applysmith/internal/kb"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writeKnowledge(t *testing.T, h *harness, rel, content string) {
	t.Helper()
	p := filepath.Join(h.ws.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// kbSearch asks the knowledge service the server holds, as the agent's tools
// and the answer pipeline do.
func (h *harness) kbSearch(q kb.Query) kb.Result {
	h.t.Helper()
	svc, err := h.srv.kb.Knowledge()
	if err != nil {
		h.t.Fatal(err)
	}
	res, err := svc.Search(h.t.Context(), q)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

// GET and HEAD change nothing: the story views read the index as it is and
// never wait for the database writer.
func TestKnowledgeReadsNeverWriteAndSurviveABusyWriter(t *testing.T) {
	h := newDomainHarness(t)
	writeKnowledge(t, h, "knowledge/behavioral/s1.md",
		"---\nid: s1\nprinciples: [ownership, delivery]\nstory_source_type: work\ndimensions: [metric]\nallowed_facts: [\"a\"]\n---\n\n"+
			"## Situation\nThe pager fired.\n\n## Task\nCut the noise.\n\n## Action\nI grouped alerts.\n\n## Result\nFewer pages.\n")
	h.srv.kb.Refresh(t.Context())

	held, free, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- h.srv.store.Load().Write(context.Background(), func(*sql.Tx) error {
			close(held)
			<-free
			return nil
		})
	}()
	<-held
	for path, want := range map[string]int{
		"/api/kb/stories/coverage": 200, "/api/kb/stories/mining/mine-missing": 404,
	} {
		got := make(chan result, 1)
		go func() { got <- h.get(path) }()
		select {
		case r := <-got:
			if r.StatusCode != want {
				t.Errorf("%s: %d %s", path, r.StatusCode, r.body)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("GET %s waited for the writer", path)
		}
	}
	close(free)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cov := h.get("/api/kb/stories/coverage")
	for _, p := range cov.data()["principles"].([]any) {
		if row := p.(map[string]any); row["id"] == "ownership" && row["grade"] != "green" {
			t.Errorf("ownership after the refresh: %v", row)
		}
	}
}

// The refresh runs on the server's own schedule, so a file the user edits
// reaches the views without any request having to write.
func TestKnowledgeIndexIsRefreshedInTheBackground(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) { c.knowledgeEvery = 10 * time.Millisecond })
	writeKnowledge(t, h, "knowledge/behavioral/s1.md",
		"---\nid: s1\nprinciples: [ownership, delivery]\nstory_source_type: work\ndimensions: [metric]\nallowed_facts: [\"a\"]\n---\n\n"+
			"## Situation\nThe pager fired.\n\n## Task\nCut the noise.\n\n## Action\nI grouped alerts.\n\n## Result\nFewer pages.\n")
	deadline := time.Now().Add(5 * time.Second)
	pause := newPollPause()
	for {
		cov := h.get("/api/kb/stories/coverage")
		for _, p := range cov.data()["principles"].([]any) {
			if row := p.(map[string]any); row["id"] == "ownership" && row["grade"] == "green" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background refresh never indexed the file: %s", cov.body)
		}
		pause()
	}
}

func (h *harness) mineReply(id, text string) map[string]any {
	h.t.Helper()
	cur := h.get("/api/kb/stories/mining/" + id).data()
	r := h.post("/api/kb/stories/mining/"+id+"/replies", map[string]any{"text": text, "expectedRevision": len(cur["log"].([]any))})
	if r.StatusCode != 200 {
		h.t.Fatalf("reply %d %s", r.StatusCode, r.body)
	}
	return r.data()
}

func TestKnowledgeMiningRoutesRunASessionToADraft(t *testing.T) {
	h := newDomainHarness(t)
	start := h.post("/api/kb/stories/mining", map[string]any{"quadrant": "team"})
	id, _ := start.data()["id"].(string)
	if start.StatusCode != 201 || id == "" || start.Header.Get("Location") != "/api/kb/stories/mining/"+id {
		t.Fatalf("%d %s", start.StatusCode, start.body)
	}
	if next := start.data()["next"].(map[string]any); next["field"] != "situation" {
		t.Fatalf("%s", start.body)
	}
	var last map[string]any
	for _, a := range []string{"The pager fired all night.", "I had to cut the noise.", "A lead wanted to silence it.", "I grouped the alerts.", "Pages fell from 200 to 15."} {
		last = h.mineReply(id, a)
	}
	if last["state"] != "ready" {
		t.Fatalf("%v", last)
	}
	at := len(last["log"].([]any))
	stale := h.post("/api/kb/stories/mining/"+id+"/replies", map[string]any{"field": "result", "text": "x", "expectedRevision": at - 1})
	if stale.StatusCode != 409 {
		t.Fatalf("%d %s", stale.StatusCode, stale.body)
	}
	a := h.post("/api/kb/stories/mining/"+id+"/save", nil)
	if a.StatusCode != 200 || a.data()["status"] != "draft" || a.data()["origin"] != "mining" {
		t.Fatalf("%d %s", a.StatusCode, a.body)
	}
	if del := h.send("DELETE", "/api/kb/stories/mining/"+id, nil); del.StatusCode != 200 || del.data()["undo"] == "" {
		t.Fatalf("%s", del.body)
	}
	if gone := h.get("/api/kb/stories/mining/" + id); gone.StatusCode != 404 {
		t.Fatalf("%d", gone.StatusCode)
	}
	if bad := h.post("/api/kb/stories/mining", map[string]any{"quadrant": "nope"}); bad.StatusCode != 422 {
		t.Fatalf("%d %s", bad.StatusCode, bad.body)
	}
}

func TestKnowledgeSourcesRoutesAddListRemove(t *testing.T) {
	h := newDomainHarness(t)
	add := h.post("/api/kb/sources", map[string]any{"repo": "https://github.com/alex-example/reef-tool"})
	if add.StatusCode != 201 || add.data()["owner"] != "alex-example" || add.data()["status"] != "added" {
		t.Fatalf("%d %s", add.StatusCode, add.body)
	}
	if bad := h.post("/api/kb/sources", map[string]any{"repo": "http://github.com/a/b"}); bad.StatusCode != 422 {
		t.Fatalf("%d %s", bad.StatusCode, bad.body)
	}
	if list := h.get("/api/kb/sources"); len(list.data()["repos"].([]any)) != 1 {
		t.Fatalf("%s", list.body)
	}
	if missing := h.send("POST", "/api/kb/sources/99/ingest", map[string]any{}, "Idempotency-Key", "key-ingest-0099"); missing.StatusCode != 404 {
		t.Fatalf("%d %s", missing.StatusCode, missing.body)
	}
	if del := h.send("DELETE", "/api/kb/sources/1", nil); del.StatusCode != 200 || del.data()["undo"] == "" {
		t.Fatalf("%s", del.body)
	}
	if list := h.get("/api/kb/sources"); len(list.data()["repos"].([]any)) != 0 {
		t.Fatalf("%s", list.body)
	}
}

// An ingestion reads a repository and drafts from it, so it is a background
// run: the request returns with the run id while the work is still going, a
// second press is refused and a cancel stops it.
func TestKnowledgeIngestIsABackgroundRun(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	reading := make(chan struct{})
	var once sync.Once
	svc, err := kb.New(h.srv.store.Load(), kb.Options{Workspace: h.ws, GitHubToken: func(ctx context.Context) string {
		once.Do(func() { close(reading) })
		<-ctx.Done()
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	d.kb = svc
	if add := h.post("/api/kb/sources", map[string]any{"repo": "alex-example/reef-tool"}); add.StatusCode != 201 {
		t.Fatalf("%d %s", add.StatusCode, add.body)
	}
	if r := h.post("/api/kb/sources/1/ingest", map[string]any{}); r.StatusCode != 400 {
		t.Fatalf("a run needs an Idempotency-Key: %d %s", r.StatusCode, r.body)
	}
	r := h.send("POST", "/api/kb/sources/1/ingest", map[string]any{}, "Idempotency-Key", "key-ingest-0001")
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	select {
	case <-reading:
	case <-time.After(5 * time.Second):
		t.Fatal("the read never started")
	}
	if run := h.get("/api/runs/" + id).data(); run["kind"] != "kb.ingest" || run["status"] != "running" {
		t.Errorf("run while it reads: %v", run)
	}
	readingRun := func() any {
		return h.get("/api/kb/sources").data()["repos"].([]any)[0].(map[string]any)["readingRunId"]
	}
	if got := readingRun(); got != id {
		t.Errorf("the listing while it reads names run %v, want %s", got, id)
	}
	if r := h.send("POST", "/api/kb/sources/1/ingest", map[string]any{}, "Idempotency-Key", "key-ingest-0002"); r.StatusCode != 409 {
		t.Errorf("a second read of the same repository while one runs: %d %s", r.StatusCode, r.body)
	}
	if r := h.send("DELETE", "/api/kb/sources/1", nil); r.StatusCode != 409 {
		t.Errorf("removing a repository while it is being read: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/runs/"+id+"/cancel", nil); r.StatusCode != 202 {
		t.Fatalf("cancel: %d %s", r.StatusCode, r.body)
	}
	if run := h.waitRun(id); run["status"] != "cancelled" {
		t.Fatalf("run %v", run)
	}
	if got := readingRun(); got != nil {
		t.Errorf("the listing after the read ended names run %v", got)
	}
}

func TestKnowledgeProductionSemanticOptIn(t *testing.T) {
	h := newDomainHarness(t)
	writeKnowledge(t, h, "knowledge/projects/pager.md", "---\nid: pager\n---\n\nHandled on-call rotations and incident response.")
	count := func() int {
		var n int
		if err := h.srv.store.Load().Read().QueryRow("SELECT count(*) FROM llm_ledger WHERE task='embedding'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	search := func(q string) { h.kbSearch(kb.Query{Query: q}) }
	search("How do you cope when the pager goes off at night?")
	if count() != 0 {
		t.Fatal("embedding called before opt-in")
	}
	on := h.send("PATCH", "/api/answer-bank/settings", map[string]any{"knowledgeSemanticRetrieval": true})
	if on.StatusCode != 200 || on.data()["knowledgeSemanticRetrieval"] != true {
		t.Fatalf("%d %s", on.StatusCode, on.body)
	}
	search("How do you cope when the pager goes off at night?")
	n := count()
	if n < 2 {
		t.Fatalf("default production embedder not called: %d", n)
	}
	off := h.send("PATCH", "/api/answer-bank/settings", map[string]any{"knowledgeSemanticRetrieval": false})
	if off.StatusCode != 200 {
		t.Fatal(string(off.body))
	}
	search("How do you respond to a page during the weekend?")
	if count() != n {
		t.Fatal("embedding called after opt-out")
	}
}

func TestKnowledgeProductionCuratedAndApprovedCorrections(t *testing.T) {
	h := newDomainHarness(t)
	writeKnowledge(t, h, "answer-bank.yml", `version: 1
entries:
  - id: berth
    description: Harbor berth allocation
    kind: fixed
    answer: Designed the harbor berth allocation solver.
    category: experience
  - id: private
    description: Harbor berth allocation name
    kind: fixed
    answer: Private identity fixture.
    category: identity
`)
	query := func(kind string) []kb.Fact {
		return h.kbSearch(kb.Query{Query: "harbor berth allocation", Kinds: []string{kind}}).Facts
	}
	if facts := query("answer-bank"); len(facts) != 1 {
		t.Fatalf("curated: %v", facts)
	}
	if facts := query("correction"); len(facts) != 0 {
		t.Fatalf("unapproved: %v", facts)
	}
	err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO answer_candidates(key,type,state,source,entry_id,seed_question,questions_json,hosts_json,proposed_kind,proposed_answer,support_count,first_seen_at,last_seen_at,created_at,updated_at) VALUES('fixture','new-entry','approved','manual','berth','Harbor berth allocation','[]','[]','fixed','Designed the harbor berth allocation solver.',1,'2026-01-01','2026-01-01','2026-01-01','2026-01-01')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := query("correction")
	if len(facts) != 1 || facts[0].Trust != "correction" {
		t.Fatalf("approved: %v", facts)
	}
	// A user edit invalidates the old correction instead of reviving its stale answer.
	p := h.ws.Root + "/answer-bank.yml"
	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(string(raw), "Designed the harbor berth allocation solver.", "Operated the harbor berth allocation service.")), 0600); err != nil {
		t.Fatal(err)
	}
	if facts := query("correction"); len(facts) != 0 {
		t.Fatalf("stale correction: %v", facts)
	}
}

// A knowledge search over the approved corrections leaves no goroutine behind.
func TestBankEvidence_ACorrectionSearchLeavesNoGoroutineRunning(t *testing.T) {
	h := newDomainHarness(t)
	src := bankEvidence{h.domainOf().bank, h.srv.store.Load(), kb.KindCorrection}
	before := runtime.NumGoroutine()
	for range 50 {
		if _, err := src.Search(context.Background(), kb.Query{Query: "why this company"}); err != nil {
			t.Fatal(err)
		}
	}
	if grown := runtime.NumGoroutine() - before; grown > 25 {
		t.Fatalf("50 searches left %d goroutines running", grown)
	}
}
