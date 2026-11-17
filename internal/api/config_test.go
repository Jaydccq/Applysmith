package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

func (h *harness) put(path string, body any) result { return h.send("PUT", path, body) }

func (h *harness) patch(path string, body any) result { return h.send("PATCH", path, body) }

func TestConfigReadsAFileWithItsRevisionAndTemplateFlag(t *testing.T) {
	h := newDomainHarness(t)
	r := h.get("/api/config/cv")
	if r.StatusCode != 200 || r.data()["file"] != "cv" || r.data()["exists"] != true || r.data()["template"] != true || r.data()["revision"] == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if _, has := r.data()["values"]; has {
		t.Fatalf("a Markdown file has no values: %s", r.body)
	}
	if voice := h.get("/api/config/voice"); voice.StatusCode != 200 || voice.data()["exists"] != false {
		t.Fatalf("an optional file may be absent: %d %s", voice.StatusCode, voice.body)
	}
	for _, name := range []string{"answer-bank", "nothing"} {
		if bad := h.get("/api/config/" + name); bad.StatusCode != 404 {
			t.Fatalf("%s is not editable here: %d %s", name, bad.StatusCode, bad.body)
		}
	}
}

func TestConfigSaveIsCheckedAgainstTheLoadedRevision(t *testing.T) {
	h := newDomainHarness(t)
	rev := h.get("/api/config/narrative").data()["revision"].(string)
	ok := h.put("/api/config/narrative", map[string]any{"content": "I build careful software.\n", "expectedRevision": rev})
	if ok.StatusCode != 200 || ok.data()["changed"] != true {
		t.Fatalf("%d %s", ok.StatusCode, ok.body)
	}
	stale := h.put("/api/config/narrative", map[string]any{"content": "Another edit.\n", "expectedRevision": rev})
	if stale.StatusCode != 409 || stale.errCode() != "revision_conflict" {
		t.Fatalf("stale save: %d %s", stale.StatusCode, stale.body)
	}
	if got, _ := os.ReadFile(h.ws.Path(workspace.FileNarrative)); string(got) != "I build careful software.\n" {
		t.Fatalf("a refused save changed the file: %q", got)
	}
	if none := h.put("/api/config/narrative", map[string]any{"content": "x"}); none.StatusCode != 422 {
		t.Fatalf("a save without a revision: %d %s", none.StatusCode, none.body)
	}
}

// Write keeps what a save replaced (workspace.Write): the list is each earlier
// content, newest first, and a file never saved has none.
func TestConfigBackupsListWhatEachSaveReplacedNewestFirst(t *testing.T) {
	h := newDomainHarness(t)
	if none := h.get("/api/config/voice/backups"); none.StatusCode != 200 || len(none.data()["backups"].([]any)) != 0 {
		t.Fatalf("a file never saved: %d %s", none.StatusCode, none.body)
	}
	doc := h.get("/api/config/narrative").data()
	first, rev := doc["content"].(string), doc["revision"].(string)
	for _, text := range []string{"Draft one.\n", "Draft two.\n"} {
		r := h.put("/api/config/narrative", map[string]any{"content": text, "expectedRevision": rev})
		if r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		rev = r.data()["revision"].(string)
	}
	var got []string
	for _, b := range h.get("/api/config/narrative/backups").data()["backups"].([]any) {
		got = append(got, b.(map[string]any)["content"].(string))
	}
	if len(got) != 2 || got[0] != "Draft one.\n" || got[1] != first {
		t.Fatalf("backups %q, want the first draft then the original", got)
	}
	if bad := h.get("/api/config/answer-bank/backups"); bad.StatusCode != 404 {
		t.Fatalf("not an editable file: %d %s", bad.StatusCode, bad.body)
	}
}

// The page shows a refusal beside the field it names (details.fields, by
// profile path).
func TestConfigPatchRefusalNamesEachFieldInPlainWords(t *testing.T) {
	h := newDomainHarness(t)
	rev := h.get("/api/config/profile").data()["revision"].(string)
	r := h.patch("/api/config/profile", map[string]any{"expectedRevision": rev, "ops": []map[string]any{{"op": "set", "path": "preferences.max_days_in_office", "value": "9"}}})
	e, _ := r.json()["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	fields, _ := d["fields"].(map[string]any)
	got, _ := fields["preferences.max_days_in_office"].(string)
	if r.StatusCode != 422 || len(fields) != 1 || !strings.Contains(got, "0") || !strings.Contains(got, "7") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// A discarded draft entry stays discarded for every later read of the draft,
// until a restore (the Undo) puts it back.
func TestCVDraftDismissIsKeptUntilRestored(t *testing.T) {
	h := newDomainHarness(t)
	for range 2 {
		if r := h.post("/api/config/cv/draft/dismiss", map[string]any{"keys": []string{"skill|go", "skill|sql"}}); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	}
	if got := h.get("/api/config/cv/draft").data()["dismissed"]; fmt.Sprint(got) != "[skill|go skill|sql]" {
		t.Fatalf("dismissed: %v", got)
	}
	if r := h.post("/api/config/cv/draft/dismiss", map[string]any{"keys": []string{"skill|go"}, "restore": true}); r.StatusCode != 200 {
		t.Fatalf("restore: %d %s", r.StatusCode, r.body)
	}
	if got := h.get("/api/config/cv/draft").data()["dismissed"]; fmt.Sprint(got) != "[skill|sql]" {
		t.Fatalf("dismissed after the restore: %v", got)
	}
	if r := h.post("/api/config/cv/draft/dismiss", map[string]any{"keys": []string{}}); r.StatusCode != 422 {
		t.Fatalf("no keys: %d", r.StatusCode)
	}
}

func TestConfigPatchEditsTheProfileByKeyPathAgainstTheRevision(t *testing.T) {
	h := newDomainHarness(t)
	rev := h.get("/api/config/profile").data()["revision"].(string)
	r := h.patch("/api/config/profile", map[string]any{"expectedRevision": rev, "ops": []map[string]any{
		{"op": "set", "path": "candidate.full_name", "value": "Alex Example"},
		{"op": "append", "path": "preferences.relocation.places", "value": "Lisbon"},
	}})
	if r.StatusCode != 200 || r.data()["changed"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	values := h.get("/api/config/profile").data()["values"].(map[string]any)
	places := values["preferences"].(map[string]any)["relocation"].(map[string]any)["places"].([]any)
	if values["candidate"].(map[string]any)["full_name"] != "Alex Example" || len(places) != 1 || places[0] != "Lisbon" {
		t.Fatalf("%v", values)
	}
	if stale := h.patch("/api/config/profile", map[string]any{"expectedRevision": rev, "ops": []map[string]any{{"op": "unset", "path": "candidate.phone"}}}); stale.StatusCode != 409 {
		t.Fatalf("stale patch: %d %s", stale.StatusCode, stale.body)
	}
	if md := h.patch("/api/config/cv", map[string]any{"expectedRevision": "x", "ops": []map[string]any{{"op": "set", "path": "a", "value": "b"}}}); md.StatusCode != 422 {
		t.Fatalf("patching Markdown: %d %s", md.StatusCode, md.body)
	}
}

func (h *harness) uploadPDF(name string, body []byte) result {
	return h.do(call{method: "POST", path: "/api/resumes?name=" + name, useToken: true, contentType: "application/pdf", body: body})
}

func TestResumeUploadStoresAPDFOnceAndRefusesWhatIsNotOne(t *testing.T) {
	h := newDomainHarness(t)
	pdf := []byte("%PDF-1.4\nsynthetic\n%%EOF\n")
	r := h.uploadPDF("backend", pdf)
	if r.StatusCode != 201 || r.data()["file"] != "resumes/backend.pdf" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got, err := os.ReadFile(filepath.Join(h.ws.Root, "resumes", "backend.pdf")); err != nil || string(got) != string(pdf) {
		t.Fatalf("stored file: %v", err)
	}
	again := h.uploadPDF("backend", []byte("%PDF-1.4\nreplacement\n"))
	if again.StatusCode != 409 || again.errCode() != "conflict" || !strings.Contains(string(again.body), `"reason":"file_exists"`) {
		t.Fatalf("an upload that meets a stored file answers file_exists: %d %s", again.StatusCode, again.body)
	}
	if got, _ := os.ReadFile(filepath.Join(h.ws.Root, "resumes", "backend.pdf")); string(got) != string(pdf) {
		t.Fatalf("the stored file was replaced: %q", got)
	}
	if notPDF := h.uploadPDF("notes", []byte("plain text")); notPDF.StatusCode != 422 {
		t.Fatalf("not a PDF: %d %s", notPDF.StatusCode, notPDF.body)
	}
	for _, name := range []string{"..%2Fescape"} {
		if bad := h.uploadPDF(name, pdf); bad.StatusCode != 422 {
			t.Fatalf("name %q: %d %s", name, bad.StatusCode, bad.body)
		}
	}
	if _, err := os.Stat(filepath.Join(h.ws.Root, "escape.pdf")); err == nil {
		t.Fatal("an upload left the résumé directory")
	}
}

func TestCVDraftReadsTheParsedHistoryAndStoresNothing(t *testing.T) {
	h := newDomainHarness(t)
	cv := "# Alex Example\n\nalex@mailbox.test\n\n## Experience\n\n### Platform Engineer, Fictional Systems Inc. (2020-01 to present)\n\n## Skills\n\nGo, SQL\n"
	if err := os.WriteFile(h.ws.Path(workspace.FileCV), []byte(cv), 0o600); err != nil {
		t.Fatal(err)
	}
	profileBefore, _ := os.ReadFile(h.ws.Path(workspace.FileProfile))
	r := h.get("/api/config/cv/draft")
	d := r.data()
	jobs, _ := d["employment"].([]any)
	if r.StatusCode != 200 || len(jobs) != 1 || d["revision"] == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	job := jobs[0].(map[string]any)
	if job["employer"] != "Fictional Systems Inc." || job["title"] != "Platform Engineer" || job["start"] != "2020-01" || job["end"] != "present" {
		t.Fatalf("%v", job)
	}
	if skills, _ := d["skills"].([]any); len(skills) != 2 || len(d["education"].([]any)) != 0 {
		t.Fatalf("%s", r.body)
	}
	// The contact line's reading is cvparse's (TestIdentityReadsTheNameAndContactLine).
	if id, _ := d["identity"].(map[string]any); len(id) != 1 || id["email"] != "alex@mailbox.test" {
		t.Fatalf("identity: %s", r.body)
	}
	if after, _ := os.ReadFile(h.ws.Path(workspace.FileProfile)); string(after) != string(profileBefore) {
		t.Fatal("reading the draft changed the profile")
	}
}

func TestConfigPolicy_ReadAnyoneLocalWriteOnlyTheDashboardSession(t *testing.T) {
	h := newDomainHarness(t)
	doc := h.get("/api/config/policy")
	if doc.StatusCode != 200 || doc.data()["file"] != "policy" || doc.data()["revision"] == "" {
		t.Fatalf("read: %d %s", doc.StatusCode, doc.body)
	}
	rev := doc.data()["revision"].(string)
	vals := doc.data()["values"].(map[string]any)
	if vals["cooldowns"].(map[string]any)["captcha"] != float64(168) {
		t.Fatalf("effective values: %v", vals)
	}
	ops := []map[string]any{
		{"op": "set", "path": "cooldowns.captcha", "value": "48"},
		{"op": "set", "path": "cooldowns.rate_limited", "value": "3"},
	}
	// The local token the CLI and the extension hold is refused.
	if r := h.patch("/api/config/policy", map[string]any{"expectedRevision": rev, "ops": ops}); r.StatusCode != 403 || r.errCode() != "forbidden" {
		t.Fatalf("token: %d %s", r.StatusCode, r.body)
	}
	if p, _ := h.ws.LoadPolicy(); p.Cooldowns.Captcha != 168 {
		t.Fatal("a refused edit changed the file")
	}
	cookie := h.session()
	r := h.chat(cookie, "PATCH", "/api/config/policy", map[string]any{"expectedRevision": rev, "ops": ops})
	if r.StatusCode != 200 || r.data()["changed"] != true {
		t.Fatalf("session: %d %s", r.StatusCode, r.body)
	}
	if p, _ := h.ws.LoadPolicy(); p.Cooldowns.RateLimited != 3 || p.Cooldowns.Captcha != 48 {
		t.Fatalf("policy = %+v", p)
	}
	// A second edit against the old revision is a conflict.
	if r := h.chat(cookie, "PATCH", "/api/config/policy", map[string]any{"expectedRevision": rev, "ops": ops[:1]}); r.StatusCode != 409 {
		t.Fatalf("stale: %d %s", r.StatusCode, r.body)
	}
	fresh := h.get("/api/config/policy").data()["revision"].(string)
	// A value outside the policy's bounds is refused with the key named.
	bad := h.chat(cookie, "PATCH", "/api/config/policy", map[string]any{"expectedRevision": fresh, "ops": []map[string]any{{"op": "set", "path": "cooldowns.captcha", "value": "2161"}}})
	if bad.StatusCode != 422 || !strings.Contains(string(bad.body), "captcha") {
		t.Fatalf("bound: %d %s", bad.StatusCode, bad.body)
	}
}

func TestConfigPolicy_RawWriteIsTheDashboardSessions(t *testing.T) {
	h := newDomainHarness(t)
	doc := h.get("/api/config/policy").data()
	rev, content := doc["revision"].(string), doc["content"].(string)
	body := func(c, r string) map[string]any { return map[string]any{"content": c, "expectedRevision": r} }
	if r := h.put("/api/config/policy", body(content, rev)); r.StatusCode != 403 || r.errCode() != "forbidden" {
		t.Fatalf("the local token: %d %s", r.StatusCode, r.body)
	}
	cookie := h.session()
	if !strings.Contains(content, "captcha: 168") {
		t.Fatalf("the template: %s", content)
	}
	edited := strings.Replace(content, "captcha: 168", "captcha: 2", 1)
	r := h.chat(cookie, "PUT", "/api/config/policy", body(edited, rev))
	if r.StatusCode != 200 || r.data()["changed"] != true {
		t.Fatalf("session: %d %s", r.StatusCode, r.body)
	}
	if p, _ := h.ws.LoadPolicy(); p.Cooldowns.Captcha != 2 {
		t.Fatalf("policy = %+v", p)
	}
	if r := h.chat(cookie, "PUT", "/api/config/policy", body(content, rev)); r.StatusCode != 409 {
		t.Fatalf("a stale revision: %d %s", r.StatusCode, r.body)
	}
	fresh := h.get("/api/config/policy").data()["revision"].(string)
	if r := h.chat(cookie, "PUT", "/api/config/policy", body(strings.Replace(edited, "captcha: 2", "captcha: 2161", 1), fresh)); r.StatusCode != 422 || !strings.Contains(string(r.body), "captcha") {
		t.Fatalf("a value out of bounds: %d %s", r.StatusCode, r.body)
	}
}

func TestConfigPortalsAreEditedWithRevisionAndAnInvalidSaveNamesTheLineAndKey(t *testing.T) {
	h := newDomainHarness(t)
	doc := h.get("/api/config/portals")
	if doc.StatusCode != 200 || doc.data()["file"] != "portals" {
		t.Fatalf("%d %s", doc.StatusCode, doc.body)
	}
	rev := doc.data()["revision"].(string)
	bad := h.put("/api/config/portals", map[string]any{"content": "version: 1\nunknown_key: true\n", "expectedRevision": rev})
	if bad.StatusCode != 422 || !strings.Contains(string(bad.body), `"message":"portals.yml:2:1 unknown_key`) {
		t.Fatalf("an unknown key is refused with its line: %d %s", bad.StatusCode, bad.body)
	}
	good := h.put("/api/config/portals", map[string]any{"content": "version: 1\ncompanies:\n  - name: Example Corp\n    ats: greenhouse\n    slug: example-corp\n", "expectedRevision": rev})
	if good.StatusCode != 200 {
		t.Fatalf("%d %s", good.StatusCode, good.body)
	}
	values, _ := h.get("/api/config/portals").data()["values"].(map[string]any)
	if companies, _ := values["companies"].([]any); len(companies) != 1 {
		t.Fatalf("a valid portals file carries its parsed values: %v", values)
	}
}
