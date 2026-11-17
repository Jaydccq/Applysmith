package kb

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGaps_QueryWithSecretIsNeverRecorded(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("id: a", "Inventory service uses Kafka."))
	key := "sk" + "-" + "ant-api03-" + strings.Repeat("Q9x", 9)
	if _, err := f.svc.Search(f.ctx, Query{Query: "why does " + key + " fail sometimes"}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := f.st.Read().QueryRowContext(f.ctx, `SELECT coalesce(group_concat(question || ' | ' || terms_key, ' ; '), '') FROM kb_gaps`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "" {
		t.Fatalf("a gap was recorded for a credential-shaped query: %q", stored)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := f.svc.Export(f.ctx, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(out, "gaps.json"))
	if strings.Contains(string(b), key) {
		t.Fatal("gaps.json holds the key")
	}
}

func TestStory_FollowupAnswer_SecretRejected(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "todo-story", map[string]string{"result": "Time fell to TODO minutes."})
	qs, _ := f.svc.FollowUps(f.ctx, d.ID)
	key := "sk" + "-" + "ant-api03-" + strings.Repeat("Q9x", 9)
	_, err := f.svc.RecordAnswer(f.ctx, d.ID, qs[0].ID, "about "+key)
	if !errors.Is(err, ErrSecret) || strings.Contains(err.Error(), key) {
		t.Fatalf("err = %v", err)
	}
	if answers, _ := f.svc.Answers(f.ctx, d.ID); len(answers) != 0 {
		t.Fatalf("answers = %+v", answers)
	}
}

func TestSecrets_LettersOnlyAndContinuationLines(t *testing.T) {
	flagged := map[string]string{
		"letters only":      "password=" + "xKqWbTzLmPvRfNcYhGjD",
		"lowercase random":  "secret: " + "xkqwbtzlmpvrfncyhgjdsq",
		"next line":         "password:\n  " + "Tr0ub4dor&3xyz",
		"next line letters": "api_key:\n  \"" + "xKqWbTzLmPvRfNcYhGjD" + "\"",
		"block scalar":      "token: |\n  " + "xKqWbTzLmPvRfNcYhGjD",
		"stripe test":       "key " + "sk_" + "test_" + "4eC39HqLyjWDarjtT1zdp7dc",
		"stripe live":       "key " + "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc",
		"after underscore":  "MYKEY_" + "sk-" + "abcdefghijklmnopqrstuvwx",
	}
	for name, text := range flagged {
		if err := CheckSecrets("x.md", text); !errors.Is(err, ErrSecret) {
			t.Errorf("%s not flagged", name)
		}
	}
	prose := []string{
		"password rotation is handled by the platform team every quarter",
		"token = getUserPasswordHashFromCache",
		"secret: internationalization",
		"password:\nis stored in the vault by the platform team",
		"the api_key: documentation lives in the wiki",
	}
	for _, text := range prose {
		if err := CheckSecrets("x.md", text); err != nil {
			t.Errorf("prose flagged: %q: %v", text, err)
		}
	}
}

func TestKB_Edit_TitleOnlyChangeIsAConflict(t *testing.T) {
	f := newFixture(t)
	orig := snippet("id: pay\ntitle: Original title\ndescription: first", "Migrated the payments ledger.")
	f.write("knowledge/projects/pay.md", orig)
	doc, err := f.svc.Show(f.ctx, "pay")
	if err != nil {
		t.Fatal(err)
	}
	f.write("knowledge/projects/pay.md", strings.Replace(orig, "Original title", "Title edited elsewhere", 1))
	disk := f.read("knowledge/projects/pay.md")
	if _, err := f.svc.Edit(f.ctx, "pay", orig, doc.FileHash); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if f.read("knowledge/projects/pay.md") != disk {
		t.Fatal("the stale save reverted the retitle")
	}
}

func TestBrief_OwnDomainAcceptedWhateverThePageHost(t *testing.T) {
	for _, c := range []struct{ declared, page string }{
		{"https://acme.test", "https://jobs.acme.test/req/1"},
		{"https://www.acme.test", "https://acme.test/careers/1"},
		{"https://acme.test", "https://acme.test/careers/1"},
	} {
		sf := &stubFetcher{pages: map[string]*Page{
			c.page:           ldPage(c.declared),
			c.declared + "/": sitePage("Acme builds fleet software."),
		}}
		f := briefFixture(t, sf)
		b, err := f.svc.EnsureBrief(f.ctx, BriefRequest{Company: "Acme", ApplicationURL: c.page})
		if err != nil || b.Domain != "acme.test" {
			t.Errorf("declared %s on %s: %+v, %v", c.declared, c.page, b, err)
		}
	}
	if _, ok := acceptSite("https://boards.greenhouse.io/acme"); ok {
		t.Error("an ATS host was accepted")
	}
}

func TestKB_SymlinkedKnowledgeRootIsWalked(t *testing.T) {
	f := newFixture(t)
	real := filepath.Join(f.ws.Root, "knowledge-real")
	if err := os.MkdirAll(filepath.Join(real, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(real, "projects", "a.md"), []byte(snippet("id: a", "Inventory service uses Kafka for events.")), 0o600)
	if err := os.Symlink(real, filepath.Join(f.ws.Root, "knowledge")); err != nil {
		t.Skip("symlinks unavailable")
	}
	list, err := f.svc.List(f.ctx)
	if err != nil || len(list) != 1 || list[0].Path != "knowledge/projects/a.md" || list[0].Category != "projects" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if r := f.search("inventory kafka events"); r.Status != StatusGrounded {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestKB_DanglingKnowledgeRootIsUnavailable(t *testing.T) {
	f := newFixture(t)
	if err := os.Symlink(filepath.Join(f.ws.Root, "missing-target"), filepath.Join(f.ws.Root, "knowledge")); err != nil {
		t.Skip("symlinks unavailable")
	}
	r := f.search("inventory kafka events")
	if r.Status != StatusSourcesUnavailable {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestFetch_PublicAddrEmbeddedAndReservedRanges(t *testing.T) {
	for a, want := range map[string]bool{
		"0.0.0.0": false, "0.0.0.1": false, "0.1.2.3": false, "240.0.0.1": false, "255.255.255.255": false,
		"::127.0.0.1": false, "::10.0.0.1": false, "64:ff9b::7f00:1": false, "64:ff9b::a00:1": false,
		"2002:7f00:1::1": false, "2002:a00:1::1": false, "fec0::1": false, "::": false,
		"64:ff9b::808:808": true, "2002:808:808::1": true, "8.8.8.8": true, "2606:4700::1111": true,
	} {
		if got := publicAddr(netip.MustParseAddr(a)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", a, got, want)
		}
	}
}

func TestTrigger_LimitsAndEmptyMatch(t *testing.T) {
	for _, bad := range []string{"/.*/", "/a*/", "/(?i)x?/", "/" + strings.Repeat("a", 600) + "/"} {
		if tr := compileTrigger(bad); !tr.invalid || tr.matches("anything") {
			t.Errorf("%.20s was accepted", bad)
		}
	}
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, fmt.Sprintf("trigger phrase %d", i))
	}
	front := "triggers:\n  - " + strings.Join(many, "\n  - ")
	if _, err := ParseSnippet("knowledge/x.md", []byte(snippet(front, "Body."))); !errors.Is(err, ErrFrontmatter) {
		t.Fatalf("too many triggers = %v", err)
	}
	if _, err := ParseSnippet("knowledge/x.md", []byte(snippet("triggers: ['"+strings.Repeat("p", 600)+"']", "Body."))); !errors.Is(err, ErrFrontmatter) {
		t.Fatalf("a long literal trigger = %v", err)
	}
}

func TestKB_ReadsRefuseSymlinks(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("id: a", "Harbor text about cranes and tides."))
	d, _ := f.svc.Show(f.ctx, "a")
	outside := filepath.Join(t.TempDir(), "outside.md")
	os.WriteFile(outside, []byte("OUTSIDE"), 0o600)
	p := filepath.Join(f.ws.Root, "knowledge", "projects", "a.md")
	os.Remove(p)
	if err := os.Symlink(outside, p); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := f.svc.Edit(f.ctx, "a", snippet("id: a", "Changed."), d.FileHash); err == nil {
		t.Fatal("edit followed a symlink")
	}
	if b, _ := os.ReadFile(outside); string(b) != "OUTSIDE" {
		t.Fatal("the outside file was changed")
	}
	if _, err := readRegular(p); err == nil {
		t.Fatal("readRegular followed a symlink")
	}
}

func TestDrafts_SnippetIDValidated(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"../../escape", "a/b", "..", ".", "a\\b", "a\x00b", "   ", strings.Repeat("x", 200)} {
		_, err := f.svc.CreateDraft(f.ctx, NewDraft{SnippetID: id, Markdown: snippet("title: x", "Body text here.")})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("id %q accepted: %v", id, err)
		}
	}
	// An id taken from the frontmatter follows the same rule.
	if _, err := f.svc.CreateDraft(f.ctx, NewDraft{Markdown: snippet("id: ../../x", "Body text here.")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("frontmatter id accepted: %v", err)
	}
	if _, err := f.svc.CreateDraft(f.ctx, NewDraft{SnippetID: "Fine Id", Markdown: snippet("title: x", "Body text here.")}); err != nil {
		t.Errorf("plain id refused: %v", err)
	}
}
