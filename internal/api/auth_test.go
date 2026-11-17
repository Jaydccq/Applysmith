package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

func sessionOnlyRoute() boundRoute {
	return testRoute("PATCH", "/api/test/policy", func(r *Route) { r.Access = AccessAdmin; r.SessionOnly = true }, ok)
}

func principalRoute() boundRoute {
	return testRoute("GET", "/api/test/who", nil, func(c *apikit.Call) (apikit.Reply, error) {
		return apikit.Reply{Data: map[string]string{"kind": string(c.P.Kind), "id": c.P.ID}}, nil
	})
}

func TestCookieFlagsAndIssuance(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: h.launchPath()})
	ck := r.cookie(SessionCookie)
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
		t.Fatalf("cookie %+v", ck)
	}
	if n := len(ck.Value); n != 43 {
		t.Errorf("cookie value has %d characters", n)
	}
	// A browser that already holds a live session is not given another one.
	if again := h.do(call{path: h.launchPath(), cookie: ck.Value}); again.cookie(SessionCookie) != nil {
		t.Error("a live session was reissued")
	}
	// Only the document request redeems a code.
	if r := h.do(call{path: "/tracker" + h.launchPath()[1:]}); r.cookie(SessionCookie) != nil {
		t.Error("a non-document path issued a cookie")
	}
}

func TestCookieRefusedForForeignHost(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: "/", host: "attacker.example.com"})
	if r.StatusCode != 403 || r.errCode() != "forbidden" || r.cookie(SessionCookie) != nil {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSessionAuthenticatesAndSurvivesARestart(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	if r := h.do(call{path: "/api/runs/none", cookie: cookie}); r.StatusCode != 404 {
		t.Fatalf("session refused: %d %s", r.StatusCode, r.body)
	}
	h.restart()
	if r := h.do(call{path: "/api/runs/none", cookie: cookie}); r.StatusCode != 404 {
		t.Fatalf("old cookie after restart: %d %s", r.StatusCode, r.body)
	}
	// Reloading with a launch code of the new server keeps the session.
	if r := h.do(call{path: h.launchPath(), cookie: cookie}); r.cookie(SessionCookie) != nil {
		t.Error("a restart cost the browser its session")
	}
	// Only digests are stored: neither the cookie value nor the key is in the table.
	rows, err := h.srv.store.Load().Read().Query(`SELECT cookie_sha256 || key_sha256 || used_at FROM dashboard_sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	h.keyMu.Lock()
	key := h.keys[cookie]
	h.keyMu.Unlock()
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		if n++; strings.Contains(row, cookie) || strings.Contains(row, key) || len(row) < 128 {
			t.Errorf("stored session %q", row)
		}
	}
	if n != 1 {
		t.Errorf("%d stored sessions", n)
	}
}

func TestSessionLimitAndLifetimeHoldAcrossARestart(t *testing.T) {
	h := newHarness(t)
	first := h.session()
	var rest []string
	for range 19 {
		rest = append(rest, h.session())
	}
	h.session() // the 21st evicts the first, which nothing used since
	h.restart()
	if r := h.do(call{path: "/api/runs/none", cookie: first}); r.StatusCode != 401 {
		t.Errorf("an evicted session came back: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/api/runs/none", cookie: rest[0]}); r.StatusCode != 404 {
		t.Errorf("a live session was lost: %d", r.StatusCode)
	}
	// Idle for more than the lifetime, then a restart: the session is gone.
	h.clock.advance(31 * 24 * time.Hour)
	h.restart()
	if r := h.do(call{path: "/api/runs/none", cookie: rest[1]}); r.StatusCode != 401 {
		t.Errorf("an expired session came back: %d", r.StatusCode)
	}
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM dashboard_sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d expired rows left, %v", n, err)
	}
}

func TestSessionEvictionKeepsTheTwentyMostRecent(t *testing.T) {
	h := newHarness(t)
	first := h.session()
	var rest []string
	for range 19 {
		rest = append(rest, h.session())
	}
	// 20 live sessions: the first still works, and using it makes it recent.
	if r := h.do(call{path: "/api/runs/none", cookie: first}); r.StatusCode != 404 {
		t.Fatalf("first session: %d", r.StatusCode)
	}
	h.session() // the 21st evicts the least recently used, which is rest[0]
	if r := h.do(call{path: "/api/runs/none", cookie: rest[0]}); r.StatusCode != 401 {
		t.Errorf("least recently used session still works: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/api/runs/none", cookie: first}); r.StatusCode != 404 {
		t.Errorf("recently used session was evicted: %d", r.StatusCode)
	}
}

func TestSessionExpiresAfterThirtyDaysWithoutUse(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	h.clock.advance(29 * 24 * time.Hour)
	if r := h.do(call{path: "/api/runs/none", cookie: cookie}); r.StatusCode != 404 {
		t.Fatalf("session at day 29: %d", r.StatusCode)
	}
	h.clock.advance(31 * 24 * time.Hour)
	if r := h.do(call{path: "/api/runs/none", cookie: cookie}); r.StatusCode != 401 {
		t.Fatalf("session after 31 idle days: %d", r.StatusCode)
	}
}

func TestBearerAuthSuccessAndFailure(t *testing.T) {
	h := newHarness(t)
	tok := h.token()
	cases := []struct {
		name    string
		c       call
		wantHTT int
	}{
		{"valid", call{path: "/api/runs/none", bearer: tok}, 404},
		{"absent", call{path: "/api/runs/none"}, 401},
		{"wrong same length", call{path: "/api/runs/none", bearer: strings.Repeat("A", len(tok))}, 401},
		{"wrong short", call{path: "/api/runs/none", bearer: "short"}, 401},
		{"wrong scheme", call{path: "/api/runs/none", headers: map[string]string{"Authorization": "Basic " + tok}}, 401},
		{"cookie value as bearer", call{path: "/api/runs/none", bearer: h.session()}, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(tc.c)
			if r.StatusCode != tc.wantHTT {
				t.Fatalf("status %d: %s", r.StatusCode, r.body)
			}
			if tc.wantHTT == 401 && r.errCode() != "unauthenticated" {
				t.Errorf("code %s", r.errCode())
			}
		})
	}
}

func TestDeviceTokenAndForwardedHeaderOnLoopback(t *testing.T) {
	h := newHarness(t, withRoutes(principalRoute()))
	if r := h.do(call{path: "/api/runs/none", bearer: "a-device-token-from-another-listener"}); r.StatusCode != 401 {
		t.Errorf("device-shaped token on loopback: %d", r.StatusCode)
	}
	r := h.do(call{path: "/api/test/who", useToken: true, headers: map[string]string{
		"X-Forwarded-For": "198.51.100.7", "Forwarded": "for=198.51.100.7", "X-Real-Ip": "198.51.100.7"}})
	if r.StatusCode != 200 || r.data()["kind"] != "local-token" {
		t.Fatalf("forwarded request: %d %s", r.StatusCode, r.body)
	}
}

func TestPrincipalAvailableToHandlers(t *testing.T) {
	h := newHarness(t, withRoutes(principalRoute()))
	d := h.do(call{path: "/api/test/who", useToken: true}).data()
	if d["kind"] != "local-token" || d["id"] != "local-token" {
		t.Errorf("bearer principal %v", d)
	}
	d = h.do(call{path: "/api/test/who", cookie: h.session()}).data()
	if d["kind"] != "dashboard" || d["id"] != "dashboard" {
		t.Errorf("session principal %v", d)
	}
}

func TestBearerReachesAdminButNotSessionOnlyRoutes(t *testing.T) {
	h := newHarness(t, withRoutes(sessionOnlyRoute()))
	if r := h.do(call{method: "POST", path: "/api/pairing/codes", useToken: true}); r.StatusCode != 200 {
		t.Fatalf("admin route with bearer: %d %s", r.StatusCode, r.body)
	}
	r := h.do(call{method: "PATCH", path: "/api/test/policy", useToken: true})
	if r.StatusCode != 403 || r.errCode() != "forbidden" || r.errReason() != "session_only" {
		t.Fatalf("session-only route with bearer: %d %s", r.StatusCode, r.body)
	}
	cookie := h.session()
	r = h.do(call{method: "PATCH", path: "/api/test/policy", cookie: cookie, headers: map[string]string{"Origin": h.srv.Origin()}})
	if r.StatusCode != 200 {
		t.Fatalf("session-only route with the session: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{method: "PATCH", path: "/api/test/policy"}); r.StatusCode != 401 {
		t.Fatalf("session-only route without a credential: %d", r.StatusCode)
	}
}

func TestPairingCodeWorksOnce(t *testing.T) {
	h := newHarness(t)
	code := h.newCode()
	r := h.claim(code)
	if r.StatusCode != 200 || r.data()["token"] != h.token() {
		t.Fatalf("first claim: %d %s", r.StatusCode, r.body)
	}
	if r := h.claim(code); r.StatusCode != 401 || r.errCode() != "unauthenticated" {
		t.Fatalf("second claim: %d %s", r.StatusCode, r.body)
	}
}

func TestPairingCodeShapeAndExpiry(t *testing.T) {
	h := newHarness(t)
	code := h.newCode()
	if len(code) != 10 || strings.Trim(code, pairingAlpha) != "" {
		t.Fatalf("code %q", code)
	}
	h.clock.advance(121 * time.Second)
	if r := h.claim(code); r.StatusCode != 401 {
		t.Fatalf("expired code: %d", r.StatusCode)
	}
	fresh := h.newCode()
	h.clock.advance(119 * time.Second)
	if r := h.claim(fresh); r.StatusCode != 200 {
		t.Fatalf("code at 119 seconds: %d", r.StatusCode)
	}
}

func TestPairingGuessingIsBounded(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 5; i++ {
		if r := h.claim("WRONGCODE" + itoa(i)); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	r := h.claim("WRONGCODE6")
	if r.StatusCode != 429 || r.errCode() != "rate_limited" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("sixth attempt: %d %s", r.StatusCode, r.body)
	}
	// Even a correct code is refused while the limit holds.
	if r := h.claim(h.newCode()); r.StatusCode != 429 {
		t.Fatalf("valid code during the limit: %d", r.StatusCode)
	}
}

func TestTenFailedClaimsDiscardOutstandingCodes(t *testing.T) {
	h := newHarness(t)
	code := h.newCode()
	for i := range 10 {
		if i%5 == 0 {
			h.clock.advance(61 * time.Second)
		}
		if r := h.claim("WRONGCODE" + itoa(i)); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	h.clock.advance(61 * time.Second)
	if r := h.claim(code); r.StatusCode != 401 {
		t.Fatalf("a code that predates ten failures was accepted: %d", r.StatusCode)
	}
	if r := h.claim(h.newCode()); r.StatusCode != 200 {
		t.Fatalf("a new code after the reset: %d", r.StatusCode)
	}
}

// The failures that discard the codes are the ones made against the codes that
// are waiting: wrong claims from before a code was issued, or from before a
// claim succeeded, do not count against it.
func TestFailedClaimsCountFromTheLatestCodeAndTheLatestSuccess(t *testing.T) {
	p := &pairing{now: time.Now}
	wrong := func(n int) {
		for i := range n {
			if p.claim("WRONGCODE" + itoa(i)) {
				t.Fatal("a wrong code was accepted")
			}
		}
	}
	wrong(failedClaimCap - 1)
	first, _, err := p.create()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := p.create()
	if err != nil {
		t.Fatal(err)
	}
	wrong(failedClaimCap - 1)
	if !p.claim(first) {
		t.Fatal("one failure fewer than the cap after the code was issued discarded it")
	}
	wrong(failedClaimCap - 1)
	if !p.claim(second) {
		t.Fatal("failures from before a successful claim counted against the code left waiting")
	}
	third, _, err := p.create()
	if err != nil {
		t.Fatal(err)
	}
	wrong(failedClaimCap)
	if p.claim(third) {
		t.Fatal("a code outlived as many failures as the cap")
	}
}

func TestPairingClaimIsPublicButCodesNeedAnAdminCredential(t *testing.T) {
	h := newHarness(t)
	if r := h.do(call{method: "POST", path: "/api/pairing/codes"}); r.StatusCode != 401 {
		t.Fatalf("anonymous code request: %d", r.StatusCode)
	}
}

func TestTokenRotation(t *testing.T) {
	h := newHarness(t)
	old := h.token()
	r := h.do(call{method: "POST", path: "/api/token/rotate", bearer: old})
	if r.StatusCode != 200 {
		t.Fatalf("rotate: %d %s", r.StatusCode, r.body)
	}
	fresh, _ := r.data()["token"].(string)
	if fresh == "" || fresh == old || fresh != h.token() {
		t.Fatalf("rotated token %q (stored %q)", fresh, h.token())
	}
	if r := h.do(call{path: "/api/runs/none", bearer: old}); r.StatusCode != 401 {
		t.Errorf("old token: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/api/runs/none", bearer: fresh}); r.StatusCode != 404 {
		t.Errorf("new token: %d", r.StatusCode)
	}
	h.restart()
	if r := h.do(call{path: "/api/runs/none", bearer: fresh}); r.StatusCode != 404 {
		t.Errorf("new token after restart: %d", r.StatusCode)
	}
}

// Settings, Browser extension lists each install that used the token, and
// unpairing replaces the token they share. Only the dashboard session may
// unpair, and no reply carries the token.
func TestExtensionClientsAndUnpair(t *testing.T) {
	h := newHarness(t)
	old := h.token()
	seen := func(id, version string) {
		hd := map[string]string{extensionHeader: "id=" + id + "; version=" + version + "; build=b1; managed=0"}
		h.do(call{path: "/api/runs/none", bearer: old, headers: hd})
	}
	seen("everyday-0001", "0.1.0")
	h.clock.advance(time.Minute)
	seen("devchrome-0002", "0.1.0")
	h.do(call{path: "/api/runs/none", bearer: "not-the-token", headers: map[string]string{extensionHeader: "id=stranger-0003"}})
	r := h.do(call{path: "/api/extension/clients", cookie: h.session()})
	got := decode[wire.ExtensionClients](t, r)
	if r.StatusCode != 200 || len(got.Clients) != 2 || got.Clients[0].ID != "devchrome-0002" || got.Clients[1].Version != "0.1.0" || strings.Contains(string(r.body), old) {
		t.Fatalf("clients: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{method: "DELETE", path: "/api/extension/clients", bearer: old}); r.StatusCode != 403 {
		t.Fatalf("unpair with the token: %d", r.StatusCode)
	}
	r = h.do(call{method: "DELETE", path: "/api/extension/clients", cookie: h.session(), headers: map[string]string{"Origin": h.srv.Origin()}})
	if r.StatusCode != 200 || len(decode[wire.ExtensionClients](t, r).Clients) != 0 || strings.Contains(string(r.body), h.token()) {
		t.Fatalf("unpair: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{path: "/api/runs/none", bearer: old}); r.StatusCode != 401 {
		t.Errorf("old token after unpair: %d", r.StatusCode)
	}
}

func TestTokenRotationClosesStreamsOpenedWithTheOldToken(t *testing.T) {
	h := newHarness(t)
	old := h.token()
	s := h.openStream("/api/board/events", old, "")
	defer s.close()
	if ev := s.next(); ev.name != "snapshot" {
		t.Fatalf("first event %q", ev.name)
	}
	if r := h.do(call{method: "POST", path: "/api/token/rotate", bearer: old}); r.StatusCode != 200 {
		t.Fatalf("rotate: %d", r.StatusCode)
	}
	s.expectClosed()
}

func TestRotationRefusedWhenTheEnvironmentSuppliesTheToken(t *testing.T) {
	h := newHarness(t)
	h.vault = newVaultWithEnv(t, h.ws, h.mem, "APPLYSMITH_SECRET_API_TOKEN", "an-environment-token-of-at-least-32-bytes")
	h.restart()
	r := h.do(call{method: "POST", path: "/api/token/rotate", bearer: "an-environment-token-of-at-least-32-bytes"})
	if r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestCredentialInQueryStringIsRejectedAndNotLogged(t *testing.T) {
	h := newHarness(t)
	tok := h.token()
	for _, q := range []string{"token=" + tok, "access_token=" + tok, "api_key=" + tok, "Authorization=Bearer+" + tok, "applysmith_session=" + tok} {
		r := h.do(call{path: "/api/health?" + q})
		if r.StatusCode != 400 || r.errCode() != "bad_request" {
			t.Errorf("%s: %d %s", strings.SplitN(q, "=", 2)[0], r.StatusCode, r.body)
		}
	}
	if log := h.logText(); strings.Contains(log, tok) || strings.Contains(log, "access_token") {
		t.Error("the credential reached the log")
	}
}

func TestNoSecretsInLogs(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	bad := strings.Repeat("Z", 43)
	rid := "run-check-0001"
	h.do(call{path: "/api/runs/none", bearer: bad, headers: map[string]string{"X-Request-Id": rid}})
	h.do(call{path: "/api/runs/none?x=secret-query-value", useToken: true})
	h.do(call{method: "POST", path: "/api/pairing/claim", contentType: "application/json", body: jsonBody(map[string]string{"code": "SECRETCODE"})})
	log := h.logText()
	for _, secret := range []string{bad, h.token(), cookie, "secret-query-value", "SECRETCODE", "Bearer", "Authorization", "Cookie"} {
		if strings.Contains(log, secret) {
			t.Errorf("log contains %q", secret)
		}
	}
	if !strings.Contains(log, "request_id="+rid) || !strings.Contains(log, "route=\"GET /api/runs/{id}\"") {
		t.Errorf("the failed request is not identified in the log:\n%s", log)
	}
}

func TestOriginAndHostChecks(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	post := func(extra map[string]string, bearer bool) result {
		c := call{method: "POST", path: "/api/pairing/codes", headers: extra}
		if bearer {
			c.useToken = true
		} else {
			c.cookie = cookie
		}
		return h.do(c)
	}
	if r := post(nil, false); r.StatusCode != 403 || r.errReason() != "origin_mismatch" {
		t.Errorf("cookie POST without Origin: %d %s", r.StatusCode, r.body)
	}
	if r := post(map[string]string{"Origin": "http://attacker.example.com"}, false); r.errReason() != "origin_mismatch" {
		t.Errorf("foreign Origin: %s", r.body)
	}
	if r := post(map[string]string{"Origin": "http://localhost:" + itoa(h.srv.Port())}, false); r.errReason() != "origin_mismatch" {
		t.Errorf("an Origin that differs from the Host: %s", r.body)
	}
	if r := post(map[string]string{"Origin": h.srv.Origin()}, false); r.StatusCode != 200 {
		t.Errorf("matching Origin: %d %s", r.StatusCode, r.body)
	}
	if r := post(nil, true); r.StatusCode != 200 {
		t.Errorf("bearer POST without Origin: %d %s", r.StatusCode, r.body)
	}
	r := h.do(call{path: "/api/health", host: "attacker.example.com"})
	if r.StatusCode != 403 || r.errCode() != "forbidden" || r.errReason() != "host_not_allowed" {
		t.Errorf("DNS rebinding: %d %s", r.StatusCode, r.body)
	}
	r = h.do(call{path: "/api/health", host: "127.0.0.1:1"})
	if r.errReason() != "host_not_allowed" {
		t.Errorf("a loopback host on the wrong port: %s", r.body)
	}
}

func TestBearerTokenComparisonUsesFixedLengthDigests(t *testing.T) {
	var g tokenGuard
	g.set("correct-token-value")
	if !g.valid("correct-token-value") || g.valid("correct-token-valuf") || g.valid("") || g.valid("x") {
		t.Fatal("token guard accepts or rejects the wrong values")
	}
	if a, b := digest("a"), digest("b"); matches(a, b) || !matches(a, a) {
		t.Fatal("digest comparison is wrong")
	}
}

func TestSameSiteCookieRequestsAreRefused(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	for _, site := range []string{"cross-site", "same-site", "Same-Site"} {
		r := h.do(call{path: "/api/runs/none", cookie: cookie, headers: map[string]string{"Sec-Fetch-Site": site}})
		if r.StatusCode != 403 || r.errReason() != "cross_site" {
			t.Errorf("Sec-Fetch-Site %s: %d %s", site, r.StatusCode, r.body)
		}
	}
	for _, site := range []string{"same-origin", "none", ""} {
		if r := h.do(call{path: "/api/runs/none", cookie: cookie, headers: map[string]string{"Sec-Fetch-Site": site}}); r.StatusCode != 404 {
			t.Errorf("Sec-Fetch-Site %q: %d", site, r.StatusCode)
		}
	}
	// The bearer token is not a browser credential.
	if r := h.do(call{path: "/api/runs/none", useToken: true, headers: map[string]string{"Sec-Fetch-Site": "same-site"}}); r.StatusCode != 404 {
		t.Errorf("bearer with same-site: %d", r.StatusCode)
	}
}
