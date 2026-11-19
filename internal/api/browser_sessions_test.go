package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
)

const (
	checkinPath = "/api/browser/sessions/checkin"
	importsPath = "/api/browser/sessions/imports"
	pushesPath  = "/api/browser/sessions/pushes"
	// cookieSecret stands for a cookie value; no response, log or error may repeat it.
	cookieSecret = "cookie-value-for-tests-7f3a91"
)

// fakeBrowser records what the API hands the managed browser.
type fakeBrowser struct {
	mu      sync.Mutex
	sites   []string
	undone  int
	undoErr error
	domains [][]string
	cookies [][]wire.SessionCookie
	// checked and checkErr answer the sign-in probe; checks counts the calls.
	checked  []browser.SignInCheck
	checkErr error
	checks   int
	hold     chan struct{} // when set, Import waits for it
	entered  chan struct{}
	imported []browser.ImportedSite
	forgot   []string
	// signedOut names the sign-in sites whose cookies the profile lacks.
	signedOut map[string]bool
}

func (f *fakeBrowser) SignedOut(_ context.Context, host string) (browser.SignInSite, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	site, ok := browser.SiteOf(host)
	return site, ok && f.signedOut[site.Domain]
}

func (f *fakeBrowser) ImportedSites(context.Context) ([]browser.ImportedSite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.imported, nil
}

func (f *fakeBrowser) Forget(_ context.Context, host string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, host)
	return 1, nil
}

func (f *fakeBrowser) Sites(context.Context) ([]browser.SessionSite, error) {
	out := make([]browser.SessionSite, len(f.sites))
	for i, d := range f.sites {
		out[i] = browser.SessionSite{Domain: d, Label: d, Cookies: 2, SignIn: "unknown"}
	}
	return out, nil
}

func (f *fakeBrowser) Undo(context.Context) (wire.SessionUndoResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.undoErr != nil {
		return wire.SessionUndoResult{}, f.undoErr
	}
	f.undone++
	return wire.SessionUndoResult{Restored: 3}, nil
}

func (f *fakeBrowser) CheckSignIn(context.Context, ...string) ([]browser.SignInCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.checked, f.checkErr
}

func (f *fakeBrowser) UndoAvailable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.domains) > f.undone
}

func (f *fakeBrowser) Import(ctx context.Context, domains []string, cookies []wire.SessionCookie) (wire.SessionImportResult, error) {
	f.mu.Lock()
	f.domains, f.cookies = append(f.domains, domains), append(f.cookies, cookies)
	hold, entered := f.hold, f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return wire.SessionImportResult{}, ctx.Err()
		}
	}
	return wire.SessionImportResult{Imported: wire.SafeInt(len(cookies)), Replaced: 1}, nil
}

func (f *fakeBrowser) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.domains)
}

func sessionHarness(t *testing.T, b *fakeBrowser, live func() bool) *harness {
	t.Helper()
	return newDomainHarness(t, func(c *Config) {
		if b != nil {
			c.SessionBrowser = b
		}
		c.ApplyRunLive = live
	})
}

func cookie(domain, name string) wire.SessionCookie {
	return wire.SessionCookie{Name: name, Value: cookieSecret, Domain: domain, Path: "/", Secure: true, HTTPOnly: true, SameSite: "lax"}
}

// request queues a pending request for the domains and returns its id after the
// extension's check-in picked it up.
func (h *harness) request(domains ...string) string {
	h.t.Helper()
	req, e := h.srv.signin.queue(domains, nil)
	if e != nil {
		h.t.Fatal(e)
	}
	return req.id
}

func (h *harness) checkin(sites ...wire.SessionSiteCount) result {
	h.t.Helper()
	return h.post(checkinPath, wire.SessionCheckin{Sites: sites})
}

func TestSessionCheckinReportsCountsAndReceivesSitesAndPending(t *testing.T) {
	b := &fakeBrowser{sites: []string{"jobs.example.test", "accounts.google.com", "Example.org"}}
	h := sessionHarness(t, b, nil)
	r := h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 4})
	if r.StatusCode != 200 || r.data()["pending"] != nil {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got := fmt.Sprint(r.data()["sites"]); got != "[example.org jobs.example.test]" {
		t.Fatalf("sites %s", got)
	}
	id := h.request("careers.example.net")
	r = h.checkin()
	p := r.data()["pending"].(map[string]any)
	if p["requestId"] != id || fmt.Sprint(p["domains"]) != "[careers.example.net]" || p["expiresAt"] == nil {
		t.Fatalf("pending %v", p)
	}
	if got := fmt.Sprint(r.data()["sites"]); got != "[careers.example.net example.org jobs.example.test]" {
		t.Fatalf("a request's domains are counted too: %s", got)
	}
}

func TestSessionCheckinCarriesCountsOnly(t *testing.T) {
	h := sessionHarness(t, nil, nil)
	body := map[string]any{"sites": []map[string]any{{"domain": "jobs.example.test", "cookies": 3, "value": cookieSecret}}}
	if r := h.post(checkinPath, body); r.StatusCode != 422 || strings.Contains(string(r.body), cookieSecret) {
		t.Fatalf("a check-in with an extra field: %d %s", r.StatusCode, r.body)
	}
}

func TestSessionCheckinRefusals(t *testing.T) {
	h := sessionHarness(t, nil, nil)
	many := make([]wire.SessionSiteCount, maxSessionSites+1)
	for i := range many {
		many[i] = wire.SessionSiteCount{Domain: fmt.Sprintf("s%d.example.test", i)}
	}
	for _, c := range []struct {
		name   string
		sites  []wire.SessionSiteCount
		status int
		reason string
	}{
		{"google", []wire.SessionSiteCount{{Domain: "accounts.google.com"}}, 403, "not-allowed"},
		{"youtube", []wire.SessionSiteCount{{Domain: "youtube.com", Cookies: 2}}, 403, "not-allowed"},
		{"not a domain", []wire.SessionSiteCount{{Domain: "https://jobs.example.test/"}}, 422, ""},
		{"negative count", []wire.SessionSiteCount{{Domain: "jobs.example.test", Cookies: -1}}, 422, ""},
		{"too many sites", many, 422, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.checkin(c.sites...)
			if r.StatusCode != c.status || r.errReason() != c.reason {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
		})
	}
}

// A run asks for its own host only when the extension is present and the site
// holds cookies in the everyday Chrome.
func TestSessionOwnHostAvailability(t *testing.T) {
	h := sessionHarness(t, nil, nil)
	y := h.srv.signin
	if got := y.ownHostUnavailable("jobs.example.test"); got != reasonExtensionSilent {
		t.Fatalf("no check-in yet: %q", got)
	}
	h.checkin() // the first check-in after a restart carries no counts
	if got := y.ownHostUnavailable("jobs.example.test"); got != reasonExtensionSilent {
		t.Fatalf("a check-in without counts is no proof: %q", got)
	}
	h.checkin(wire.SessionSiteCount{Domain: "example.test", Cookies: 0}, wire.SessionSiteCount{Domain: "other.example.org", Cookies: 5})
	if got := y.ownHostUnavailable("jobs.example.test"); got != reasonNoCookies {
		t.Fatalf("a parent with no cookies: %q", got)
	}
	if got := y.ownHostUnavailable("other.example.org"); got != "" {
		t.Fatalf("a site with cookies: %q", got)
	}
	h.clock.advance(20 * time.Second)
	if got := y.ownHostUnavailable("other.example.org"); got != "" {
		t.Fatalf("a check-in 20 s old: %q", got)
	}
	h.clock.advance(3 * time.Minute)
	if got := y.ownHostUnavailable("other.example.org"); got != reasonExtensionSilent {
		t.Fatalf("a check-in 3 minutes old: %q", got)
	}
}

func TestSessionRequestQueue(t *testing.T) {
	h := sessionHarness(t, nil, nil)
	y := h.srv.signin
	if _, e := y.queue([]string{"accounts.google.com"}, nil); e == nil || e.Code != wire.CodeForbidden || e.Details.Reason != "not-allowed" {
		t.Fatalf("%v", e)
	}
	if _, e := y.queue([]string{"a.example.test", "b.youtube.com"}, nil); e == nil {
		t.Fatal("one forbidden domain refuses the request")
	}
	if y.pending != nil {
		t.Fatal("nothing is queued after a refusal")
	}
	first, _ := y.queue([]string{"jobs.example.test", "example.test", "JOBS.example.test", "other.example.org"}, nil)
	if fmt.Sprint(first.domains) != "[example.test other.example.org]" {
		t.Fatalf("collapsed %v", first.domains)
	}
	second, _ := y.queue([]string{"example.org"}, nil)
	if y.pending.id != second.id || second.id == first.id {
		t.Fatal("a newer request replaces the pending one")
	}
	h.clock.advance(sessionRequestTTL - time.Second)
	if y.checkin(nil) == nil {
		t.Fatal("the request waits for 120 s")
	}
	h.clock.advance(2 * time.Second)
	if y.checkin(nil) != nil {
		t.Fatal("the request expired")
	}
}

func TestSessionImportCopiesTheAnsweredRequest(t *testing.T) {
	b := &fakeBrowser{}
	h := sessionHarness(t, b, nil)
	id := h.request("example.test")
	body := wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie(".example.test", "sid"), cookie("jobs.example.test", "csrf")}, Skipped: []string{}}
	r := h.post(importsPath, body)
	if r.StatusCode != 200 || r.data()["imported"] != float64(2) || r.data()["replaced"] != float64(1) || r.data()["failed"] != float64(0) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if b.calls() != 1 || fmt.Sprint(b.domains[0]) != "[example.test]" || len(b.cookies[0]) != 2 || b.cookies[0][0].Value != cookieSecret {
		t.Fatalf("the browser got %v", b.domains)
	}
	if strings.Contains(string(r.body), cookieSecret) || strings.Contains(h.logText(), cookieSecret) {
		t.Fatal("a cookie value reached a response or the log")
	}
	again := h.post(importsPath, body)
	if again.StatusCode != 409 || again.errReason() != "stale-request" || b.calls() != 1 {
		t.Fatalf("the answered request is finished: %d %s", again.StatusCode, again.body)
	}
	if h.srv.signin.checkin(nil) != nil {
		t.Fatal("the answered request is no longer pending")
	}
}

func TestSessionImportStaleRequests(t *testing.T) {
	b := &fakeBrowser{}
	h := sessionHarness(t, b, nil)
	cookies := []wire.SessionCookie{cookie("example.test", "sid")}
	old := h.request("example.test")
	fresh := h.request("example.test")
	for name, id := range map[string]string{"replaced": old, "never issued": "sync_0000000000000000", "empty": ""} {
		r := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: cookies})
		want := 409
		if id == "" {
			want = 422
		}
		if r.StatusCode != want || (id != "" && r.errReason() != "stale-request") {
			t.Fatalf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	h.clock.advance(sessionRequestTTL + time.Second)
	if r := h.post(importsPath, wire.SessionImport{RequestID: fresh, Cookies: cookies}); r.StatusCode != 409 || r.errReason() != "stale-request" {
		t.Fatalf("expired: %d %s", r.StatusCode, r.body)
	}
	if b.calls() != 0 {
		t.Fatal("the browser was touched")
	}
}

func TestSessionImportScope(t *testing.T) {
	b := &fakeBrowser{}
	h := sessionHarness(t, b, nil)
	id := h.request("jobs.example.test")
	for _, c := range []struct {
		name    string
		body    wire.SessionImport
		status  int
		reason  string
		message string
	}{
		{"a cookie of another site", wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("jobs.example.test", "a"), cookie("other.example.test", "b")}}, 422, "cookie-out-of-scope", ""},
		{"the parent of the requested domain", wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("example.test", "a")}}, 422, "cookie-out-of-scope", ""},
		{"a suffix lookalike", wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("notjobs.example.test", "a")}}, 422, "cookie-out-of-scope", ""},
		{"a skipped domain nobody requested", wire.SessionImport{RequestID: id, Skipped: []string{"elsewhere.example.org"}}, 422, "outside-request", ""},
		{"a google cookie", wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie(".google.com", "SID")}}, 403, "not-allowed", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.post(importsPath, c.body)
			if r.StatusCode != c.status || r.errReason() != c.reason || strings.Contains(string(r.body), cookieSecret) {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
		})
	}
	if b.calls() != 0 {
		t.Fatal("the browser was touched")
	}
	// A refused payload leaves the request waiting for a correct answer.
	ok := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("jobs.example.test", "a")}, Skipped: []string{"jobs.example.test"}})
	if ok.StatusCode != 200 || b.calls() != 1 {
		t.Fatalf("%d %s", ok.StatusCode, ok.body)
	}
}

func TestSessionImportCookieShape(t *testing.T) {
	h := sessionHarness(t, &fakeBrowser{}, nil)
	id := h.request("example.test")
	mut := func(f func(*wire.SessionCookie)) wire.SessionCookie {
		c := cookie("example.test", "sid")
		f(&c)
		return c
	}
	negative := -1.0
	for name, c := range map[string]wire.SessionCookie{
		"no name":          mut(func(c *wire.SessionCookie) { c.Name = "" }),
		"control in name":  mut(func(c *wire.SessionCookie) { c.Name = "a\r\nb" }),
		"long value":       mut(func(c *wire.SessionCookie) { c.Value = strings.Repeat(cookieSecret, 200) }),
		"relative path":    mut(func(c *wire.SessionCookie) { c.Path = "x" }),
		"unknown samesite": mut(func(c *wire.SessionCookie) { c.SameSite = "none" }),
		"negative expiry":  mut(func(c *wire.SessionCookie) { c.ExpirationDate = &negative }),
		"bad domain":       mut(func(c *wire.SessionCookie) { c.Domain = "ex ample" }),
	} {
		r := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{c}})
		if r.StatusCode != 422 || strings.Contains(string(r.body), cookieSecret) {
			t.Errorf("%s: %d %.200s", name, r.StatusCode, r.body)
		}
	}
	// A partitioned cookie has no place in the wire shape: it is an unknown field.
	raw := map[string]any{"requestId": id, "cookies": []map[string]any{{"name": "a", "value": cookieSecret, "domain": "example.test", "path": "/",
		"secure": true, "httpOnly": false, "hostOnly": true, "session": true, "sameSite": "lax", "partitionKey": map[string]any{"topLevelSite": "https://example.test"}}},
		"skipped": []string{}, "truncated": false}
	if r := h.post(importsPath, raw); r.StatusCode != 422 || strings.Contains(string(r.body), cookieSecret) {
		t.Fatalf("partitioned: %d %.200s", r.StatusCode, r.body)
	}
}

func TestSessionImportSizeLimits(t *testing.T) {
	b := &fakeBrowser{}
	h := sessionHarness(t, b, nil)
	id := h.request("example.test")
	many := make([]wire.SessionCookie, maxSessionCookies+1)
	for i := range many {
		many[i] = cookie("example.test", fmt.Sprintf("c%d", i))
		many[i].Value = "v"
	}
	if r := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: many}); r.StatusCode != 413 || b.calls() != 0 {
		t.Fatalf("2001 cookies: %d %.200s", r.StatusCode, r.body)
	}
	if r := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: many[:maxSessionCookies]}); r.StatusCode != 200 {
		t.Fatalf("2000 cookies are accepted: %d %.200s", r.StatusCode, r.body)
	}
	id = h.request("example.test")
	bulky := make([]wire.SessionCookie, 300)
	for i := range bulky {
		bulky[i] = cookie("example.test", fmt.Sprintf("c%d", i))
		bulky[i].Value = strings.Repeat("v", 4000)
	}
	r := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: bulky})
	if r.StatusCode != 413 || r.errCode() != "payload_too_large" {
		t.Fatalf("over 1 MiB: %d %.200s", r.StatusCode, r.body)
	}
}

func TestSessionImportIsRefusedWhileAnotherRuns(t *testing.T) {
	b := &fakeBrowser{hold: make(chan struct{}), entered: make(chan struct{}, 2)}
	h := sessionHarness(t, b, nil)
	id := h.request("example.test")
	done := make(chan result, 1)
	go func() {
		done <- h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("example.test", "a")}})
	}()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the import never reached the browser")
	}
	push := h.post(pushesPath, wire.SessionPush{Site: "other.example.org", Cookies: []wire.SessionCookie{cookie("other.example.org", "a")}})
	if push.StatusCode != 409 || push.errReason() != "busy" {
		t.Fatalf("%d %s", push.StatusCode, push.body)
	}
	close(b.hold)
	select {
	case r := <-done:
		if r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not finish")
	}
	if after := h.post(pushesPath, wire.SessionPush{Site: "other.example.org"}); after.StatusCode != 200 {
		t.Fatalf("the slot is free again: %d %s", after.StatusCode, after.body)
	}
}

// A server without a managed browser stops at a typed error and keeps
// nothing: the request is finished, no value is stored or repeated.
func TestSessionImportWithoutAManagedBrowser(t *testing.T) {
	h := sessionHarness(t, nil, nil)
	id := h.request("example.test")
	r := h.post(importsPath, wire.SessionImport{RequestID: id, Cookies: []wire.SessionCookie{cookie("example.test", "a")}})
	if r.StatusCode != 503 || r.errCode() != "unavailable" || r.errReason() != "browser-unavailable" || r.json()["error"].(map[string]any)["retryable"] != false {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	p := h.post(pushesPath, wire.SessionPush{Site: "example.test", Cookies: []wire.SessionCookie{cookie("example.test", "a")}})
	if p.StatusCode != 503 || p.errReason() != "browser-unavailable" {
		t.Fatalf("%d %s", p.StatusCode, p.body)
	}
	if strings.Contains(string(r.body)+string(p.body)+h.logText(), cookieSecret) || h.dbHolds(cookieSecret) {
		t.Fatal("a cookie value was kept or repeated")
	}
}

func TestSessionPushCopiesTheChosenSite(t *testing.T) {
	b := &fakeBrowser{}
	h := sessionHarness(t, b, nil)
	pending := h.request("example.org")
	r := h.post(pushesPath, wire.SessionPush{Site: "Jobs.Example.test", Cookies: []wire.SessionCookie{cookie("jobs.example.test", "a"), cookie(".jobs.example.test", "b")}, Truncated: true})
	if r.StatusCode != 200 || r.data()["imported"] != float64(2) || r.data()["truncated"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if fmt.Sprint(b.domains[0]) != "[jobs.example.test]" {
		t.Fatalf("%v", b.domains)
	}
	if strings.Contains(string(r.body), cookieSecret) || strings.Contains(h.logText(), cookieSecret) {
		t.Fatal("a cookie value reached a response or the log")
	}
	if got := h.srv.signin.checkin(nil); got == nil || got.id != pending {
		t.Fatal("a push does not use the pending-request slot")
	}
	if empty := h.post(pushesPath, wire.SessionPush{Site: "jobs.example.test"}); empty.StatusCode != 200 {
		t.Fatalf("a site with no cookies: %d %s", empty.StatusCode, empty.body)
	}
}

func TestSessionPushRefusals(t *testing.T) {
	live := false
	b := &fakeBrowser{}
	h := sessionHarness(t, b, func() bool { return live })
	for _, c := range []struct {
		name   string
		body   wire.SessionPush
		status int
		reason string
	}{
		{"google", wire.SessionPush{Site: "accounts.google.com"}, 403, "not-allowed"},
		{"youtube", wire.SessionPush{Site: "m.youtube.com"}, 403, "not-allowed"},
		{"not a domain", wire.SessionPush{Site: "https://jobs.example.test"}, 422, ""},
		{"a cookie of another site", wire.SessionPush{Site: "jobs.example.test", Cookies: []wire.SessionCookie{cookie("example.test", "a")}}, 422, "cookie-out-of-scope"},
		{"a google cookie among the site's", wire.SessionPush{Site: "jobs.example.test", Cookies: []wire.SessionCookie{cookie("google.com", "a")}}, 403, "not-allowed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.post(pushesPath, c.body)
			if r.StatusCode != c.status || r.errReason() != c.reason {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
		})
	}
	live = true
	if r := h.post(pushesPath, wire.SessionPush{Site: "jobs.example.test", Cookies: []wire.SessionCookie{cookie("jobs.example.test", "a")}}); r.StatusCode != 409 || r.errReason() != "apply-run-live" {
		t.Fatalf("while a run drives the apply window: %d %s", r.StatusCode, r.body)
	}
	if b.calls() != 0 {
		t.Fatal("the browser was touched")
	}
}

func TestSessionPushSizeLimits(t *testing.T) {
	h := sessionHarness(t, &fakeBrowser{}, nil)
	many := make([]wire.SessionCookie, maxSessionCookies+1)
	for i := range many {
		many[i] = cookie("example.test", fmt.Sprintf("c%d", i))
	}
	if r := h.post(pushesPath, wire.SessionPush{Site: "example.test", Cookies: many}); r.StatusCode != 413 {
		t.Fatalf("%d %.200s", r.StatusCode, r.body)
	}
}
