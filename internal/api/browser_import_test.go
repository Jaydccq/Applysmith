package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/browser/browserimport"
)

// fakeImporter is an installed-browser cookie reader for tests. Read hands back
// a scripted cookie whose value is cookieSecret, so a test can prove the value
// never reaches a response.
type fakeImporter struct {
	sources  []browserimport.Source
	sites    []browserimport.Site
	readID   string
	readHost []string
	readErr  error
}

func (f *fakeImporter) Sources() ([]browserimport.Source, error) { return f.sources, nil }

func (f *fakeImporter) Sites(context.Context, string) ([]browserimport.Site, error) {
	return f.sites, nil
}

func (f *fakeImporter) Read(_ context.Context, id string, hosts []string) ([]wire.SessionCookie, error) {
	f.readID, f.readHost = id, hosts
	if f.readErr != nil {
		return nil, f.readErr
	}
	return []wire.SessionCookie{{Name: "li_at", Value: cookieSecret, Domain: ".linkedin.com", Path: "/"}}, nil
}

func importHarness(t *testing.T, b *fakeBrowser, imp CookieImporter) *harness {
	t.Helper()
	return newDomainHarness(t, func(c *Config) {
		if b != nil {
			c.SessionBrowser = b
		}
		c.CookieImport = imp
	})
}

func TestBrowserImport_ListsSourcesAndSitesWithoutValues(t *testing.T) {
	imp := &fakeImporter{
		sources: []browserimport.Source{{ID: "chrome/Default", BrowserID: "chrome", BrowserLabel: "Google Chrome", Profile: "Default"}},
		sites:   []browserimport.Site{{Host: "linkedin.com", Cookies: 3}},
	}
	h := importHarness(t, &fakeBrowser{}, imp)

	r := h.get("/api/browser/import/sources")
	if r.StatusCode != 200 || r.data()["supported"] != true {
		t.Fatalf("sources: %d %s", r.StatusCode, r.body)
	}
	srcs := r.data()["sources"].([]any)
	if len(srcs) != 1 || srcs[0].(map[string]any)["id"] != "chrome/Default" {
		t.Fatalf("sources = %v", srcs)
	}

	r = h.get("/api/browser/import/sources/chrome%2FDefault/sites")
	if r.StatusCode != 200 {
		t.Fatalf("sites: %d %s", r.StatusCode, r.body)
	}
	if got := r.data()["sites"].([]any); len(got) != 1 || got[0].(map[string]any)["host"] != "linkedin.com" {
		t.Fatalf("sites = %v", got)
	}
}

func TestBrowserImport_ImportReadsThenWritesAndHidesTheValue(t *testing.T) {
	imp := &fakeImporter{}
	b := &fakeBrowser{}
	h := importHarness(t, b, imp)

	r := h.post("/api/browser/import", wire.ImportRequest{SourceID: "chrome/Default", Hosts: []string{"LinkedIn.com", "linkedin.com"}})
	if r.StatusCode != 200 {
		t.Fatalf("import: %d %s", r.StatusCode, r.body)
	}
	if strings.Contains(string(r.body), cookieSecret) {
		t.Fatal("the import response leaked a cookie value")
	}
	if imp.readID != "chrome/Default" || len(imp.readHost) != 1 || imp.readHost[0] != "linkedin.com" {
		t.Fatalf("Read got id=%q hosts=%v (hosts should be deduped and lowercased)", imp.readID, imp.readHost)
	}
	if b.calls() != 1 || len(b.cookies[0]) != 1 {
		t.Fatalf("Import calls=%d", b.calls())
	}
}

func TestBrowserImport_ImportIsRefusedWhileAnApplyRunIsLive(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) {
		c.SessionBrowser = &fakeBrowser{}
		c.CookieImport = &fakeImporter{}
		c.ApplyRunLive = func() bool { return true }
	})
	r := h.post("/api/browser/import", wire.ImportRequest{SourceID: "chrome/Default", Hosts: []string{"linkedin.com"}})
	if r.StatusCode != 409 {
		t.Fatalf("want 409 while a run is live, got %d %s", r.StatusCode, r.body)
	}
}

func TestBrowserImport_KeyErrorTellsTheUserWhatToDo(t *testing.T) {
	imp := &fakeImporter{readErr: browserimport.ErrNoKey}
	h := importHarness(t, &fakeBrowser{}, imp)
	r := h.post("/api/browser/import", wire.ImportRequest{SourceID: "chrome/Default", Hosts: []string{"linkedin.com"}})
	if r.StatusCode == 200 || !strings.Contains(string(r.body), "sign in to it once") {
		t.Fatalf("key error not surfaced: %d %s", r.StatusCode, r.body)
	}
}

func TestBrowserImport_ManageListAndForget(t *testing.T) {
	b := &fakeBrowser{imported: []browser.ImportedSite{{Host: "linkedin.com", At: time.Unix(1_800_000_000, 0), Cookies: 2}}}
	h := importHarness(t, b, &fakeImporter{})

	r := h.get("/api/browser/imported")
	if r.StatusCode != 200 {
		t.Fatalf("imported: %d %s", r.StatusCode, r.body)
	}
	if got := r.data()["sites"].([]any); len(got) != 1 || got[0].(map[string]any)["host"] != "linkedin.com" {
		t.Fatalf("imported sites = %v", got)
	}

	r = h.post("/api/browser/imported/forget", wire.ForgetRequest{Host: "linkedin.com"})
	if r.StatusCode != 200 || len(b.forgot) != 1 || b.forgot[0] != "linkedin.com" {
		t.Fatalf("forget: %d %s forgot=%v", r.StatusCode, r.body, b.forgot)
	}
}
