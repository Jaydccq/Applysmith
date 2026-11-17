package browser

import (
	"context"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

func TestImportedSitesListsBroughtOverSitesAndForgetClearsOne(t *testing.T) {
	jar := newJar()
	s, _ := newSessions(t, jar)
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { now = now.Add(time.Second); return now }
	ctx := context.Background()
	if _, err := s.Import(ctx, []string{"linkedin.com"}, []wire.SessionCookie{wc(".linkedin.com", "li_at"), wc("www.linkedin.com", "js")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, []string{"greenhouse.io"}, []wire.SessionCookie{wc("greenhouse.io", "g")}); err != nil {
		t.Fatal(err)
	}

	sites, err := s.ImportedSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first: greenhouse was imported after linkedin.
	if len(sites) != 2 || sites[0].Host != "greenhouse.io" || sites[1].Host != "linkedin.com" {
		t.Fatalf("imported sites = %+v", sites)
	}
	if sites[1].Cookies != 2 {
		t.Fatalf("linkedin cookie count = %d, want 2", sites[1].Cookies)
	}

	n, err := s.Forget(ctx, "linkedin.com")
	if err != nil || n != 2 {
		t.Fatalf("forget = %d, %v", n, err)
	}
	if got := jar.names("linkedin.com"); len(got) != 0 {
		t.Fatalf("linkedin cookies after forget: %v", got)
	}
	sites, _ = s.ImportedSites(ctx)
	if len(sites) != 1 || sites[0].Host != "greenhouse.io" {
		t.Fatalf("imported after forget = %+v", sites)
	}
}

func TestImportedSitesDropsASiteWhoseCookiesAreGone(t *testing.T) {
	jar := newJar()
	s, _ := newSessions(t, jar)
	ctx := context.Background()
	if _, err := s.Import(ctx, []string{"acme.test"}, []wire.SessionCookie{wc("acme.test", "a")}); err != nil {
		t.Fatal(err)
	}
	// Clear the profile out of band; the ledger row must not linger.
	for k := range jar.cookies {
		delete(jar.cookies, k)
	}
	sites, err := s.ImportedSites(ctx)
	if err != nil || len(sites) != 0 {
		t.Fatalf("imported = %+v, %v", sites, err)
	}
}
