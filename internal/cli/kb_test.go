package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/kb"
	"applysmith/internal/llm"
)

func (te *testEnv) kb(args ...string) output {
	te.t.Helper()
	return te.run(context.Background(), append([]string{"kb"}, append(args, "--workspace", te.ws)...)...)
}

func (te *testEnv) mustKB(args ...string) output {
	te.t.Helper()
	o := te.kb(args...)
	if o.code != 0 {
		te.t.Fatalf("applysmith kb %v: code %d, stderr %q", args, o.code, o.stderr)
	}
	return o
}

func (te *testEnv) writeKB(rel, content string) {
	te.t.Helper()
	p := filepath.Join(te.ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		te.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		te.t.Fatal(err)
	}
}

func TestKBCLI_SearchListShowLintReindexDeleteExport(t *testing.T) {
	te := newTestEnv(t)
	te.writeKB("knowledge/projects/reef.md", "---\nid: reef\ntitle: Reef tool\n---\n\nBuilt the reef survey exporter for coastal teams.\n")

	o := te.mustKB("search", "reef", "survey", "exporter", "--json")
	var res kb.Result
	if err := json.Unmarshal([]byte(o.stdout), &res); err != nil || len(res.Facts) != 1 || res.Status != "grounded" {
		t.Fatalf("search = %+v, %v\n%s", res, err, o.stdout)
	}
	if o := te.mustKB("search", "quantum lattice research"); !strings.Contains(o.stdout, "nothing in the knowledge base matches") {
		t.Errorf("%s", o.stdout)
	}
	if o := te.mustKB("list"); !strings.Contains(o.stdout, "reef") || !strings.Contains(o.stdout, "1 documents") {
		t.Errorf("%s", o.stdout)
	}
	if o := te.mustKB("show", "reef"); !strings.Contains(o.stdout, "reef survey exporter") {
		t.Errorf("%s", o.stdout)
	}
	// Beside a running server the reads work and a write still refuses.
	rs := te.startServe()
	for _, args := range [][]string{{"search", "reef", "survey"}, {"list"}, {"show", "reef"}} {
		if o := te.kb(args...); o.code != 0 || !strings.Contains(o.stdout, "reef") {
			t.Errorf("kb %v beside a server: %+v", args, o)
		}
	}
	for args, want := range map[string]string{"lint": "0 warnings", "drafts": "DRAFT", "repo list": "0 repositories", "mine suggest": "score",
		"export " + filepath.Join(t.TempDir(), "out"): "exported to"} {
		if o := te.kb(strings.Fields(args)...); o.code != 0 || !strings.Contains(o.stdout, want) {
			t.Errorf("kb %s beside a server: %+v", args, o)
		}
	}
	if o := te.kb("mine", "show", "nope"); o.code == 0 || strings.Contains(o.stderr, "dashboard") {
		t.Errorf("kb mine show beside a server: %+v", o)
	}
	if o := te.kb("delete", "reef"); o.code == 0 || !strings.Contains(o.stderr, "dashboard") {
		t.Errorf("kb delete beside a server: %+v", o)
	}
	rs.stop()
	if o := te.mustKB("lint"); !strings.Contains(o.stdout, "0 warnings, 0 snippets with findings") {
		t.Errorf("%s", o.stdout)
	}
	if o := te.mustKB("lint", "--json"); strings.TrimSpace(o.stdout) != "{\n  \"snippets\": [],\n  \"warnings\": []\n}" {
		t.Errorf("lint --json with nothing to say:\n%s", o.stdout)
	}
	if o := te.mustKB("reindex"); !strings.Contains(o.stdout, "reindexed 1") {
		t.Errorf("%s", o.stdout)
	}
	out := filepath.Join(t.TempDir(), "out")
	te.mustKB("export", out)
	if _, err := os.Stat(filepath.Join(out, "projects", "reef.md")); err != nil {
		t.Errorf("export: %v", err)
	}
	te.mustKB("delete", "reef")
	if _, err := os.Stat(filepath.Join(te.ws, "knowledge", "projects", "reef.md")); !os.IsNotExist(err) {
		t.Errorf("file survives delete: %v", err)
	}
	if o := te.kb("show", "reef"); o.code == 0 {
		t.Errorf("show after delete succeeded")
	}
}

func TestKBCLI_ImportThenPublishNeverAutomatic(t *testing.T) {
	te := newTestEnv(t)
	src := filepath.Join(t.TempDir(), "Harbor Notes.md")
	if err := os.WriteFile(src, []byte("# Harbor notes\n\nDesigned the berth allocation solver."), 0o600); err != nil {
		t.Fatal(err)
	}
	o := te.mustKB("import", src, "--category", "projects")
	if !strings.Contains(o.stdout, "draft 1") {
		t.Fatalf("%s", o.stdout)
	}
	if o := te.mustKB("search", "berth allocation solver"); !strings.Contains(o.stdout, "nothing in the knowledge base matches") {
		t.Fatalf("a draft was retrievable: %s", o.stdout)
	}
	if o := te.mustKB("drafts"); !strings.Contains(o.stdout, "harbor-notes") {
		t.Fatalf("%s", o.stdout)
	}
	te.mustKB("publish", "1")
	if o := te.mustKB("search", "berth allocation solver"); !strings.Contains(o.stdout, "Designed the berth allocation solver") {
		t.Fatalf("%s", o.stdout)
	}
	if o := te.kb("publish", "1"); o.code == 0 {
		t.Errorf("publishing a published draft succeeded")
	}
}

func TestKBCLI_UsageErrors(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "kb"); o.code != 2 || !strings.Contains(o.stderr, "applysmith kb search") {
		t.Errorf("%d %s", o.code, o.stderr)
	}
	if o := te.kb("publish", "x"); o.code != 2 {
		t.Errorf("%d %s", o.code, o.stderr)
	}
}

var cliAnswers = []string{
	"Our on-call rotation was drowning in alerts.",
	"I had to make the pager sustainable before the freeze.",
	"The platform lead wanted to silence alerts and I feared missing an outage.",
	"I grouped alerts by cause and wrote the rules myself.",
	"Pages dropped from 200 a week to 15 a week.",
}

func TestKBCLI_MineRunSavesADraftNeverPublishes(t *testing.T) {
	te := newTestEnv(t)
	te.stdin = strings.Join(cliAnswers, "\n") + "\n"
	o := te.mustKB("mine", "run", "--quadrant", "team")
	if !strings.Contains(o.stdout, "saved as draft:1") || !strings.Contains(o.stdout, "What was the situation?") {
		t.Fatalf("%s", o.stdout)
	}
	if o := te.mustKB("search", "on-call rotation alerts pager sustainable"); !strings.Contains(o.stdout, "nothing in the knowledge base matches") {
		t.Fatalf("a mined draft is retrievable: %s", o.stdout)
	}
	if o := te.kb("publish", "1", "--story"); o.code == 0 || !strings.Contains(o.stderr, "at least two") {
		t.Fatalf("publish = %d %s", o.code, o.stderr)
	}
	o = te.mustKB("list")
	if !strings.Contains(o.stdout, "draft:1") || !strings.Contains(o.stdout, "session:") {
		t.Fatalf("%s", o.stdout)
	}
	o = te.mustKB("show", "draft:1")
	if !strings.Contains(o.stdout, "Pages dropped from 200") || !strings.Contains(o.stdout, `"pending": true`) {
		t.Fatalf("%s", o.stdout)
	}
	if o := te.mustKB("confirm", "1"); !strings.Contains(o.stdout, "revision 2") {
		t.Fatalf("%s", o.stdout)
	}
}

// A reply given with `mine reply` reaches the model as one given inside
// `mine run` does: a later part of the story the reply also states is filled
// from it and not asked again.
func TestKBCLI_MineReplyFillsWhatTheReplyAlsoStates(t *testing.T) {
	te := newTestEnv(t)
	fake := llm.NewFake()
	fake.SetGenerator(func(_, prompt string) (string, bool) {
		_, parts, _ := strings.Cut(prompt, "PARTS:\n- ")
		field, _, _ := strings.Cut(parts, ":")
		b, _ := json.Marshal(map[string]string{field: cliAnswers[1]})
		return string(b), true
	})
	te.llmOpts = []llm.Option{llm.WithFake(fake), llm.WithOfflineSimulation()}
	o := te.mustKB("mine", "start", "--quadrant", "failure", "--json")
	var m struct{ ID string }
	if err := json.Unmarshal([]byte(o.stdout), &m); err != nil || m.ID == "" {
		t.Fatalf("%s", o.stdout)
	}
	o = te.mustKB("mine", "reply", m.ID, cliAnswers[0]+" "+cliAnswers[1], "--json")
	if !strings.Contains(o.stdout, `"source": "reply"`) {
		t.Fatalf("the reply filled no other part: %s", o.stdout)
	}
}

func TestKBCLI_MineStepByStepAndDelete(t *testing.T) {
	te := newTestEnv(t)
	o := te.mustKB("mine", "start", "--quadrant", "failure", "--json")
	var m struct{ ID string }
	if err := json.Unmarshal([]byte(o.stdout), &m); err != nil || m.ID == "" {
		t.Fatalf("%s", o.stdout)
	}
	for _, a := range cliAnswers {
		te.mustKB("mine", "reply", m.ID, a)
	}
	late := te.mustKB("mine", "reply", m.ID, "one more thing")
	if !strings.Contains(late.stdout, "not recorded") {
		t.Fatalf("%s", late.stdout)
	}
	te.mustKB("mine", "save", m.ID)
	again := te.mustKB("mine", "save", m.ID, "--json")
	if !strings.Contains(again.stdout, `"id": 1`) {
		t.Fatalf("%s", again.stdout)
	}
	if o := te.mustKB("mine", "suggest"); !strings.Contains(o.stdout, "failure") {
		t.Fatalf("%s", o.stdout)
	}
	te.mustKB("delete", "session:"+m.ID)
	if o := te.kb("show", "session:"+m.ID); o.code == 0 {
		t.Fatal("the session survives")
	}
	if o := te.kb("mine", "start", "--quadrant", "nonsense"); o.code == 0 {
		t.Fatal("bad quadrant accepted")
	}
	o = te.mustKB("mine", "start", "--target", "Describe a vulnerability you found and how you validated the fix.")
	if !strings.Contains(o.stdout, "a security-finding story for: Describe a vulnerability") || !strings.Contains(o.stdout, "What was the vulnerability") {
		t.Fatalf("%s", o.stdout)
	}
	if o := te.kb("mine", "start", "--target", "Why are you interested in this role?"); o.code == 0 {
		t.Fatal("a target that asks for no story was accepted")
	}
}

func TestKBCLI_RepoAddListRemove(t *testing.T) {
	te := newTestEnv(t)
	te.mustKB("repo", "add", "https://github.com/alex-example/reef-tool.git")
	if o := te.mustKB("repo", "list"); !strings.Contains(o.stdout, "alex-example/reef-tool") || !strings.Contains(o.stdout, "added") {
		t.Fatalf("%s", o.stdout)
	}
	if o := te.mustKB("list"); !strings.Contains(o.stdout, "repo:1") {
		t.Fatalf("%s", o.stdout)
	}
	if o := te.kb("repo", "add", "http://github.com/a/b"); o.code == 0 {
		t.Fatal("plain http accepted")
	}
	if o := te.kb("repo", "ingest", "99"); o.code == 0 || !strings.Contains(o.stderr, "not found") {
		t.Fatalf("%d %s", o.code, o.stderr)
	}
	te.mustKB("repo", "remove", "1")
	if o := te.mustKB("repo", "list"); !strings.Contains(o.stdout, "0 repositories") {
		t.Fatalf("%s", o.stdout)
	}
}
