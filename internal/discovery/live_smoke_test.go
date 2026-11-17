//go:build live && browser

package discovery_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/launch"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// The live smoke check runs every source of the catalog once against the real
// site, in a temporary store, with no model call (top-n 0) or signed-in
// session. API keys may be supplied through the vault environment override;
// missing keys and website challenges skip rather than pass acceptance.
// It only reads public pages. Browser sources use a
// headless Chrome by default; APPLYSMITH_LIVE_SMOKE_HEADED=1 opens a visible
// Chrome with the same throwaway profile, killed with its process group.
// It prints one SMOKE line per source: counts and statuses, never a
// full address.
//
//	APPLYSMITH_LIVE_SMOKE=1 go test -tags 'live browser' -count=1 -v -timeout 30m -run TestLiveSmoke ./internal/discovery/
//
// APPLYSMITH_LIVE_SMOKE_SOURCES=a,b limits the run. A source whose search is a
// POST (a16z) runs only with APPLYSMITH_LIVE_SMOKE_POST=1.

var queryRE = regexp.MustCompile(`\?[^\s"')]*`)

func redact(s string) string {
	s = queryRE.ReplaceAllString(s, "")
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return s
}

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("APPLYSMITH_LIVE_SMOKE") != "1" {
		t.Skip("set APPLYSMITH_LIVE_SMOKE=1 to read the real sites")
	}
	only := map[string]bool{}
	for _, s := range strings.Split(os.Getenv("APPLYSMITH_LIVE_SMOKE_SOURCES"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			if _, err := discovery.LookupSource(s); err != nil {
				t.Fatal(err)
			}
			only[s] = true
		}
	}

	root, err := os.MkdirTemp("", "applysmith-browser-test-live-")
	if err != nil {
		t.Fatal(err)
	}
	var inst *browser.Instance
	t.Cleanup(func() {
		if inst != nil {
			inst.Stop(context.Background())
			if chromeAlive(root) {
				t.Errorf("a Chrome of the smoke profile is still running after Stop")
			}
		}
		_ = os.RemoveAll(root)
	})
	headed := os.Getenv("APPLYSMITH_LIVE_SMOKE_HEADED") == "1"
	t.Logf("live smoke headed=%t; isolated signed-out browser profile", headed)
	window := launch.ChromeWindow(func(ctx context.Context) (*browser.Instance, error) {
		if inst != nil {
			return inst, nil
		}
		chrome, err := browser.DiscoverChrome(ctx, os.Getenv)
		if err != nil {
			return nil, err
		}
		opts := browser.Options{
			Chrome:       chrome,
			ProfileDir:   filepath.Join(root, "applysmith-browser-test-profile"),
			StateDir:     filepath.Join(root, "state"),
			DownloadsDir: filepath.Join(root, "downloads"),
			Environ:      os.Environ(),
		}
		if !headed {
			opts = opts.WithHeadless()
		}
		i, err := browser.Open(ctx, opts)
		if err != nil {
			return nil, err
		}
		inst = i
		watchdog(i.PID())
		return inst, nil
	}, nil)

	for _, info := range discovery.Sources {
		if len(only) > 0 && !only[info.Name] {
			continue
		}
		t.Run(info.Name, func(t *testing.T) {
			line := smoke(t, info, window)
			t.Logf("SMOKE %-14s %s", info.Name, line)
		})
	}
}

func smoke(t *testing.T, info discovery.SourceInfo, window discovery.Window) string {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	switch {
	case info.Needs == "session":
		t.Skip("needs a signed-in test session; the isolated smoke profile has none")
		return ""
	case info.Name == "a16z" && os.Getenv("APPLYSMITH_LIVE_SMOKE_POST") != "1":
		t.Skip("set APPLYSMITH_LIVE_SMOKE_POST=1 to allow the read-only a16z search POST")
		return ""
	}

	if info.Needs == "key" && os.Getenv(vault.EnvName(info.KeyName)) == "" {
		t.Skipf("%s needs %s for live acceptance", info.Name, vault.EnvName(info.KeyName))
	}

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	repo := discovery.NewRepo(st, nil)
	sc := &discovery.Scanner{Repo: repo, Client: discovery.NewClient(), Secret: func(name string) string { return os.Getenv(vault.EnvName(name)) },
		Browser: &discovery.BrowserEnv{Window: window}}

	zero := 0
	req := discovery.ScanRequest{Source: info.Name, TopN: &zero, Budget: 4 * time.Minute, Limit: 5, Pages: 1}
	req.Portals = workspace.Portals{Version: 1}
	req.Portals.Filters.Title.Positive = []string{"engineer"}
	switch info.Name {
	case "registry":
		for _, b := range []discovery.Board{{ATS: discovery.Greenhouse, Slug: "airbnb", Company: "Airbnb"}, {ATS: discovery.Lever, Slug: "palantir", Company: "Palantir"},
			{ATS: discovery.Ashby, Slug: "ramp", Company: "Ramp"}} {
			if _, err := repo.RegistryAdd(ctx, "live-smoke", b); err != nil {
				t.Fatal(err)
			}
		}
	case "tracked":
		req.Portals.Companies = []workspace.Company{{Name: "Airbnb", ATS: "greenhouse", Slug: "airbnb"}}
	case "amazon":
		req.Search = "software engineer"
	case "builtin":
		req.URL = "https://builtin.com/jobs/dev-engineering"
	case "indeed", "ziprecruiter", "google-jobs":
		req.Search, req.City = "software engineer", "Austin"
	}

	if info.Name == "a16z" && os.Getenv("APPLYSMITH_LIVE_SMOKE_POST") == "1" {
		req.Limit, req.Pages = 30, 2
	}
	rec, err := sc.Run(ctx, req)
	if err != nil {
		t.Errorf("scan could not run: %s", redact(err.Error()))
		return "failed: " + redact(err.Error())
	}
	if rec.Status == discovery.RunFailed || rec.Counts.BoardErrors > 0 {
		t.Errorf("source failed: %s %s", redact(rec.Error), redact(strings.Join(rec.Errors, "; ")))
	}
	c := rec.Counts
	out := fmt.Sprintf("%s discovered=%d inserted=%d filtered=%d duplicates=%d board_errors=%d", rec.Status, c.Discovered, c.Inserted, sumOf(c.Filtered), sumOf(c.Duplicates), c.BoardErrors)
	if rec.Reason != "" {
		out += " reason=" + redact(rec.Reason)
	}
	if rows, err := repo.InboxList(ctx, discovery.InboxQuery{Limit: 3}); err == nil {
		for _, r := range rows {
			out += fmt.Sprintf(" sample=%q", redact(r.Company+" | "+r.Role))
		}
	}
	if rec.Error != "" {
		out += " error=" + redact(rec.Error)
	}
	for _, e := range rec.Errors {
		out += " note=" + redact(e)
	}
	for _, a := range rec.Attempts {
		out += fmt.Sprintf(" attempt=%v", redact(fmt.Sprintf("%+v", a)))
	}
	if info.Needs == "key" {
		out += " | " + reach(ctx, map[string]string{"theirstack": "https://api.theirstack.com/", "exa": "https://api.exa.ai/"}[info.Name])
	}
	if rec.Status == discovery.RunCompleted && c.Discovered == 0 {
		t.Logf("SMOKE %-14s %s", info.Name, out)
		t.Skip("no postings observed; live extraction acceptance remains unproven")
	}
	if rec.Status == discovery.RunBlocked || rec.Status == discovery.RunNeedsSignIn {
		t.Logf("SMOKE %-14s %s", info.Name, out)
		t.Skipf("live scan stopped as %s: %s", rec.Status, rec.Reason)
	}
	return out
}

func sumOf(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// reach reports whether the host of an address resolves and what one GET of
// its front page answers; nothing is sent beyond that.
func reach(ctx context.Context, rawURL string) string {
	if rawURL == "" {
		return ""
	}
	host := strings.TrimSuffix(strings.TrimPrefix(rawURL, "https://"), "/")
	if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
		return "host " + host + " does not resolve"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "front page unreachable: " + redact(err.Error())
	}
	res.Body.Close()
	return fmt.Sprintf("host %s resolves, front page answers %d", host, res.StatusCode)
}
