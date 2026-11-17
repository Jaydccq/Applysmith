//go:build dashboard

package cli

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"applysmith/web"
)

// session opens the printed link once and returns a client that carries the
// cookie and follows no redirects.
func (rs *runningServer) session() func(path string) (*http.Response, []byte) {
	rs.te.t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	opened, err := client.Get(rs.link())
	if err != nil {
		rs.te.t.Fatal(err)
	}
	opened.Body.Close()
	return func(path string) (*http.Response, []byte) {
		rs.te.t.Helper()
		req, _ := http.NewRequest("GET", rs.origin+path, nil)
		for _, c := range opened.Cookies() {
			req.AddCookie(c)
		}
		resp, err := client.Do(req)
		if err != nil {
			rs.te.t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
}

// Scenario "Reload on a deep link": every page route, with or without a
// query, answers with the app's index document so a reload opens that page.
func TestDeepLinksServeTheIndexDocument(t *testing.T) {
	dist, err := web.Dist()
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	get := newTestEnv(t).startServe().session()
	for _, path := range []string{"/tracker", "/tracker?status=applied&row=3", "/reports?id=7", "/apply-next", "/settings", "/discovery", "/guide?doc=tutorial", "/not-a-page"} {
		resp, body := get(path)
		if resp.StatusCode != 200 || string(body) != string(index) {
			t.Errorf("GET %s: %d, %d bytes (want the %d-byte index)", path, resp.StatusCode, len(body), len(index))
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q, want no-cache", path, got)
		}
	}
}

// Scenario "Unknown API path": a JSON 404, never the app shell. A missing file
// with an extension is a 404 too.
func TestUnknownPathsAreNotTheAppShell(t *testing.T) {
	get := newTestEnv(t).startServe().session()
	resp, body := get("/api/does-not-exist")
	if resp.StatusCode != 404 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(string(body), `"ok":false`) {
		t.Errorf("GET /api/does-not-exist: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, _ := get("/assets/missing.js"); resp.StatusCode != 404 {
		t.Errorf("GET /assets/missing.js: %d, want 404", resp.StatusCode)
	}
}

// The index loads its script and style from this server only, with no inline
// script, and the hashed assets it names are served immutable.
func TestIndexLoadsOnlyOwnHashedAssets(t *testing.T) {
	get := newTestEnv(t).startServe().session()
	resp, index := get("/tracker")
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("Content-Security-Policy %q", csp)
	}
	scripts := regexp.MustCompile(`<script[^>]*>`).FindAllString(string(index), -1)
	if len(scripts) == 0 {
		t.Fatalf("no script tag in the index:\n%s", index)
	}
	for _, tag := range scripts {
		if !strings.Contains(tag, ` src="/assets/`) {
			t.Errorf("script without a local src: %s", tag)
		}
	}
	assets := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindAllStringSubmatch(string(index), -1)
	if len(assets) < 2 {
		t.Fatalf("expected a script and a stylesheet, found %v", assets)
	}
	for _, a := range assets {
		r, body := get(a[1])
		if r.StatusCode != 200 || len(body) == 0 {
			t.Errorf("GET %s: %d, %d bytes", a[1], r.StatusCode, len(body))
		}
		if got := r.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s: Cache-Control %q", a[1], got)
		}
	}
}

// firstLoadBudget is the most script and style, gzipped, that the first page
// load may download.
const firstLoadBudget = 400 * 1024

func TestFirstLoadStaysWithinTheGzipBudget(t *testing.T) {
	dist, err := web.Dist()
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`(?:src|href)="/(assets/[^"]+\.(?:js|css))"`).FindAllStringSubmatch(string(index), -1)
	if len(refs) == 0 {
		t.Fatalf("the index names no script or style:\n%s", index)
	}
	total := 0
	for _, ref := range refs {
		raw, err := fs.ReadFile(dist, ref[1])
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		total += buf.Len()
	}
	if total > firstLoadBudget {
		t.Errorf("first load is %d bytes gzipped, budget %d", total, firstLoadBudget)
	}
	t.Logf("first load: %d bytes gzipped", total)
}
