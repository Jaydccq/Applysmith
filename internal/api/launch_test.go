package api

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func setCookies(r result) []string { return r.Header.Values("Set-Cookie") }

func launchCodeOf(t *testing.T, path string) string {
	t.Helper()
	u, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(launchParam)
}

func TestDashboardWithoutLaunchCodeGetsNoCookieAndAnExplanation(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: "/"})
	if r.StatusCode != 401 || len(setCookies(r)) != 0 {
		t.Fatalf("%d, cookies %v", r.StatusCode, setCookies(r))
	}
	if !strings.Contains(string(r.body), "applysmith serve") || !strings.Contains(string(r.body), "one-time code") {
		t.Errorf("the page does not explain how to open the dashboard:\n%s", r.body)
	}
	if r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("headers %v", r.Header)
	}
}

func TestLaunchCodeIssuesTheCookieAndRedirectsWithoutTheCode(t *testing.T) {
	h := newHarness(t)
	path := h.launchPath()
	r := h.do(call{path: path})
	ck := r.cookie(SessionCookie)
	if r.StatusCode != 303 || !strings.HasPrefix(r.Header.Get("Location"), "/#"+SessionKeyFragment+"=") || ck == nil || !ck.HttpOnly {
		t.Fatalf("%d %v cookie %+v", r.StatusCode, r.Header, ck)
	}
	if strings.Contains(r.Header.Get("Location"), launchCodeOf(t, path)) {
		t.Error("the redirect carries the code")
	}
	if r := h.do(call{path: "/api/runs/none", cookie: ck.Value}); r.StatusCode != 404 {
		t.Errorf("the new session does not authenticate: %d", r.StatusCode)
	}
}

func TestLaunchCodeIsSingleUse(t *testing.T) {
	h := newHarness(t)
	path := h.launchPath()
	if r := h.do(call{path: path}); r.cookie(SessionCookie) == nil {
		t.Fatal("first use refused")
	}
	r := h.do(call{path: path})
	if r.StatusCode != 401 || len(setCookies(r)) != 0 {
		t.Errorf("second use: %d, cookies %v", r.StatusCode, setCookies(r))
	}
}

func TestLaunchCodeExpires(t *testing.T) {
	h := newHarness(t)
	path := h.launchPath()
	h.clock.advance(launchTTL + time.Second)
	r := h.do(call{path: path})
	if r.StatusCode != 401 || len(setCookies(r)) != 0 {
		t.Errorf("expired code: %d, cookies %v", r.StatusCode, setCookies(r))
	}
	// A fresh one is handed out on demand.
	if h.session() == "" {
		t.Error("no session from the replacement code")
	}
}

func TestLaunchCodeIsReplacedWhenUsed(t *testing.T) {
	h := newHarness(t)
	used := launchCodeOf(t, h.launchPath())
	h.do(call{path: "/?" + launchParam + "=" + used})
	info, err := ReadLockInfo(h.ws.StateDir())
	if err != nil || info.LaunchCode == "" || info.LaunchCode == used {
		t.Fatalf("lock document after use: %+v, %v", info, err)
	}
	if r := h.do(call{path: "/?" + launchParam + "=" + info.LaunchCode}); r.cookie(SessionCookie) == nil {
		t.Error("the code in the lock document does not open the dashboard")
	}
}

func TestLockDocumentCarriesTheLiveCodeAndClearsItOnShutdown(t *testing.T) {
	h := newHarness(t)
	first, err := ReadLockInfo(h.ws.StateDir())
	if err != nil || first.LaunchCode == "" {
		t.Fatalf("%+v, %v", first, err)
	}
	if err := h.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after, err := ReadLockInfo(h.ws.StateDir()); err != nil || after.LaunchCode != "" {
		t.Errorf("a stopped server left a code behind: %+v, %v", after, err)
	}
}

// A link that was printed stays good for its whole lifetime: the server does
// not swap the code for another one while nobody has used it.
func TestUnredeemedLaunchCodeIsNotReplacedBeforeItExpires(t *testing.T) {
	h := newHarness(t)
	first, err := ReadLockInfo(h.ws.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(launchTTL - time.Second)
	got, err := h.srv.launchCode()
	if err != nil || got != first.LaunchCode {
		t.Fatalf("code %q after %s, want the printed %q (%v)", got, launchTTL-time.Second, first.LaunchCode, err)
	}
	if next, _ := ReadLockInfo(h.ws.StateDir()); next.LaunchCode != first.LaunchCode {
		t.Error("the lock document swapped an unexpired code")
	}
	if !h.srv.launch.redeem(got) {
		t.Error("the printed code is no longer redeemable one second before its end")
	}
}

func TestExpiredLaunchCodeIsReplacedInTheLockDocument(t *testing.T) {
	h := newHarness(t)
	first, _ := ReadLockInfo(h.ws.StateDir())
	h.clock.advance(launchTTL + time.Second)
	if _, err := h.srv.launchCode(); err != nil {
		t.Fatal(err)
	}
	next, _ := ReadLockInfo(h.ws.StateDir())
	if next.LaunchCode == "" || next.LaunchCode == first.LaunchCode {
		t.Errorf("code not replaced after expiry: %q -> %q", first.LaunchCode, next.LaunchCode)
	}
}

func TestLaunchCodeRouteMintsAFreshCodeForTheBearerToken(t *testing.T) {
	h := newHarness(t)
	old := launchCodeOf(t, h.launchPath())
	r := h.do(call{method: "POST", path: "/api/launch-codes", useToken: true})
	code, _ := r.data()["code"].(string)
	if r.StatusCode != 200 || code == "" || code == old || r.data()["expiresAt"] == nil {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if info, _ := ReadLockInfo(h.ws.StateDir()); info.LaunchCode != code {
		t.Error("the lock document does not carry the minted code")
	}
	if r := h.do(call{path: "/?" + launchParam + "=" + old}); r.cookie(SessionCookie) != nil {
		t.Error("the replaced code still opens the dashboard")
	}
	if r := h.do(call{path: "/?" + launchParam + "=" + code}); r.cookie(SessionCookie) == nil {
		t.Errorf("the minted code does not open the dashboard: %d", r.StatusCode)
	}
	if r := h.do(call{method: "POST", path: "/api/launch-codes"}); r.StatusCode != 401 {
		t.Errorf("without a credential: %d", r.StatusCode)
	}
	if strings.Contains(h.logText(), code) {
		t.Error("the minted code reached the log")
	}
}

func TestLaunchURLOpensTheDashboard(t *testing.T) {
	h := newHarness(t)
	u, err := h.srv.LaunchURL()
	if err != nil || !strings.HasPrefix(u, h.srv.Origin()+"/?launch=") {
		t.Fatalf("%q, %v", u, err)
	}
	if r := h.do(call{path: strings.TrimPrefix(u, h.srv.Origin())}); r.cookie(SessionCookie) == nil {
		t.Errorf("the printed address does not open the dashboard: %d", r.StatusCode)
	}
}

func TestOtherHeadersAndPathsCannotBypassTheLaunchCode(t *testing.T) {
	h := newHarness(t)
	code := launchCodeOf(t, h.launchPath())
	for name, c := range map[string]call{
		"navigation":      {headers: map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}},
		"forwarded":       {headers: map[string]string{"X-Forwarded-For": "127.0.0.1", "X-Real-IP": "127.0.0.1", "Forwarded": "for=127.0.0.1"}},
		"code in header":  {headers: map[string]string{"X-Launch-Code": code, "Authorization-Launch": code}},
		"origin":          {headers: map[string]string{"Origin": h.srv.Origin(), "Referer": h.srv.Origin() + "/"}},
		"foreign cookie":  {cookie: "not-a-session"},
		"bearer token":    {useToken: true},
		"wrong bearer":    {bearer: code},
		"upgrade":         {headers: map[string]string{"Upgrade-Insecure-Requests": "1"}},
		"launch as token": {path: "/?token=" + code},
	} {
		if c.path == "" {
			c.path = "/"
		}
		r := h.do(c)
		if len(setCookies(r)) != 0 || (r.StatusCode != 401 && r.StatusCode != 400) {
			t.Errorf("%s: %d, cookies %v", name, r.StatusCode, setCookies(r))
		}
	}
	for name, path := range map[string]string{
		"other path":  "/index.html?" + launchParam + "=" + code,
		"sub path":    "/tracker?" + launchParam + "=" + code,
		"two codes":   "/?" + launchParam + "=" + code + "&" + launchParam + "=" + code,
		"wrong param": "/?code=" + code,
		"wrong value": "/?" + launchParam + "=" + code + "x",
		"empty value": "/?" + launchParam + "=",
	} {
		if r := h.do(call{path: path}); r.cookie(SessionCookie) != nil {
			t.Errorf("%s issued a session", name)
		}
	}
	// None of that spent the code.
	if r := h.do(call{path: "/?" + launchParam + "=" + code}); r.cookie(SessionCookie) == nil {
		t.Error("a refused request consumed the code")
	}
}

func TestLaunchCodeIsRedeemedOnlyByASameOriginGet(t *testing.T) {
	h := newHarness(t)
	code := launchCodeOf(t, h.launchPath())
	path := "/?" + launchParam + "=" + code
	for _, site := range []string{"cross-site", "same-site"} {
		if r := h.do(call{path: path, headers: map[string]string{"Sec-Fetch-Site": site}}); r.cookie(SessionCookie) != nil || r.StatusCode != 401 {
			t.Errorf("Sec-Fetch-Site %s: %d %v", site, r.StatusCode, setCookies(r))
		}
	}
	if r := h.do(call{method: "HEAD", path: path}); r.cookie(SessionCookie) != nil {
		t.Error("HEAD redeemed the code")
	}
	for _, site := range []string{"none", "same-origin", ""} {
		code = launchCodeOf(t, h.launchPath())
		r := h.do(call{path: "/?" + launchParam + "=" + code, headers: map[string]string{"Sec-Fetch-Site": site}})
		if r.cookie(SessionCookie) == nil {
			t.Errorf("Sec-Fetch-Site %q: no session (%d)", site, r.StatusCode)
		}
	}
}

func TestAnonymousRequestsDoNotEvictALiveSession(t *testing.T) {
	h := newHarness(t)
	live := h.session()
	for range 30 {
		h.do(call{path: "/"})
		h.do(call{path: "/", headers: map[string]string{"Sec-Fetch-Site": "none"}})
	}
	if r := h.do(call{path: "/api/runs/none", cookie: live}); r.StatusCode != 404 {
		t.Errorf("a live session was evicted: %d", r.StatusCode)
	}
}

func TestSessionStillOpensTheDashboardWithAUsedCodeInTheURL(t *testing.T) {
	h := newHarness(t)
	path := h.launchPath()
	cookie := h.do(call{path: path}).cookie(SessionCookie).Value
	r := h.do(call{path: path, cookie: cookie})
	if r.StatusCode != 200 || r.cookie(SessionCookie) != nil {
		t.Errorf("reload with a used code and a live session: %d", r.StatusCode)
	}
}

func TestConcurrentRedemptionsOfOneCodeSucceedOnce(t *testing.T) {
	h := newHarness(t)
	path := h.launchPath()
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.do(call{path: path}).cookie(SessionCookie) != nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Errorf("%d requests redeemed one code", won)
	}
}

func TestLoopbackOrigin(t *testing.T) {
	for origin, want := range map[string]bool{
		"http://127.0.0.1:47319": true, "http://[::1]:47319": true, "http://localhost:1": true,
		"http://203.0.113.9:1": false, "https://127.0.0.1:1": false, "": false, "127.0.0.1:1": false,
		"http://127.0.0.1:1/x": false, "http://127.0.0.1": false, "http://user@127.0.0.1:1": false,
	} {
		if got := LoopbackOrigin(origin); got != want {
			t.Errorf("%q: %v, want %v", origin, got, want)
		}
	}
}
