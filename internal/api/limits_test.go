package api

import (
	"strconv"
	"testing"
	"testing/fstest"
	"time"
)

func TestGeneralLimit(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 240; i++ {
		if r := h.do(call{path: "/api/runs/none", useToken: true}); r.StatusCode != 404 {
			t.Fatalf("request %d: %d", i, r.StatusCode)
		}
	}
	r := h.do(call{path: "/api/runs/none", useToken: true})
	if r.StatusCode != 429 || r.errCode() != "rate_limited" {
		t.Fatalf("241st request: %d %s", r.StatusCode, r.body)
	}
	ra, err := strconv.Atoi(r.Header.Get("Retry-After"))
	if err != nil || ra < 1 || ra > 60 {
		t.Fatalf("Retry-After %q", r.Header.Get("Retry-After"))
	}
	e := r.json()["error"].(map[string]any)
	if e["retryable"] != true || e["details"].(map[string]any)["retryAfterSeconds"] != float64(ra) {
		t.Errorf("details %v", e)
	}
	h.clock.advance(time.Duration(ra) * time.Second)
	if r := h.do(call{path: "/api/runs/none", useToken: true}); r.StatusCode != 404 {
		t.Errorf("after the wait: %d", r.StatusCode)
	}
}

func TestLimitsAreCountedPerPrincipal(t *testing.T) {
	h := newHarness(t)
	for range 240 {
		h.do(call{path: "/api/runs/none", useToken: true})
	}
	cookie := h.session()
	if r := h.do(call{path: "/api/runs/none", cookie: cookie}); r.StatusCode != 404 {
		t.Errorf("the dashboard shares the extension's budget: %d", r.StatusCode)
	}
}

// Each caller kind meets the general budget api.md "Rate limits" gives it:
// the reads of the loopback dashboard session 1,200 a minute, its writes, the
// local token and a paired remote device 240.
func TestGeneralLimitPerCallerKind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget int
		caller func(r *remoteRig) func() int
	}{
		{"dashboard read", 1200, func(r *remoteRig) func() int {
			ck := r.session()
			return func() int { return r.do(call{path: "/api/runs/none", cookie: ck}).StatusCode }
		}},
		{"dashboard write", 240, func(r *remoteRig) func() int {
			ck := r.session()
			return func() int {
				return r.do(call{method: "POST", path: "/api/runs/none/cancel", cookie: ck, headers: map[string]string{"Origin": r.srv.Origin()}}).StatusCode
			}
		}},
		{"local token read", 240, func(r *remoteRig) func() int {
			return func() int { return r.do(call{path: "/api/runs/none", useToken: true}).StatusCode }
		}},
		{"remote device read", 240, func(r *remoteRig) func() int {
			r.online()
			dev := r.pair("Phone")
			return func() int { return r.tdo(asDevice(dev, "GET", "/api/runs/none")).status }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			send := tc.caller(newRemoteRig(t))
			for i := 1; i <= tc.budget; i++ {
				if got := send(); got == 429 {
					t.Fatalf("request %d of a budget of %d was refused", i, tc.budget)
				}
			}
			if got := send(); got != 429 {
				t.Errorf("the request past a budget of %d: %d, want 429", tc.budget, got)
			}
		})
	}
}

func TestHealthHasItsOwnBucket(t *testing.T) {
	h := newHarness(t)
	for range 240 {
		h.do(call{path: "/api/runs/none", useToken: true})
	}
	for i := 1; i <= 600; i++ {
		if r := h.do(call{path: "/api/health"}); r.StatusCode != 200 {
			t.Fatalf("health request %d: %d", i, r.StatusCode)
		}
	}
	if r := h.do(call{path: "/api/health"}); r.StatusCode != 429 {
		t.Fatalf("601st health request: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/api/runs/none", useToken: true}); r.StatusCode != 429 {
		t.Errorf("general bucket should still be full: %d", r.StatusCode)
	}
}

// A cold load of the dashboard fetches every page's chunks at once. The files
// have a bucket of their own: a burst of them is never refused for the general
// limit and leaves the API budget whole, which still applies.
func TestDashboardFilesHaveTheirOwnBucket(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Assets = fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}, "assets/page-1a2b.js": {Data: []byte("export {}")}}
	})
	for i := 1; i <= 4*80; i++ { // four cold loads of about eighty files
		if r := h.do(call{path: "/assets/page-1a2b.js"}); r.StatusCode != 200 {
			t.Fatalf("file request %d: %d", i, r.StatusCode)
		}
	}
	for i := 1; i <= 240; i++ {
		if r := h.do(call{path: "/api/runs/none", useToken: true}); r.StatusCode != 404 {
			t.Fatalf("API request %d after the files: %d", i, r.StatusCode)
		}
	}
	if r := h.do(call{path: "/api/runs/none", useToken: true}); r.StatusCode != 429 {
		t.Fatalf("241st API request: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/tracker"}); r.StatusCode != 200 {
		t.Fatalf("the page after a spent API budget: %d", r.StatusCode)
	}
	// The rest of the bucket is spent in memory, under the key a file request
	// from this host uses, rather than over thousands of connections.
	for range limitCounts[LimitAsset] - 4*80 - 2 {
		h.srv.limiter.allow("ip:127.0.0.1", LimitAsset)
	}
	if r := h.do(call{path: "/assets/page-1a2b.js"}); r.StatusCode != 200 {
		t.Fatalf("the last file inside the limit: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/assets/page-1a2b.js"}); r.StatusCode != 429 || r.errCode() != "rate_limited" {
		t.Fatalf("a file past the asset limit: %d %s", r.StatusCode, r.body)
	}
	if bad := validateRoutes([]Route{{Method: "GET", Path: "/api/x", Access: AccessStandard, Remote: RemoteDenied, Limit: LimitAsset, Body: BodyNone, Request: PayloadNone, Response: PayloadNone}}); len(bad) != 1 {
		t.Errorf("an API route in the asset class: %v", bad)
	}
}

func TestSlidingWindow(t *testing.T) {
	clock := newClock()
	l := newLimiter(clock.now)
	for range 5 {
		if ok, _ := l.allow("k", LimitPairing); !ok {
			t.Fatal("refused inside the limit")
		}
		clock.advance(10 * time.Second)
	}
	// Hits at 0,10,20,30,40 seconds; now 50 seconds in. The next must wait for the first to expire.
	ok, wait := l.allow("k", LimitPairing)
	if ok || wait != 10 {
		t.Fatalf("ok=%v wait=%d, want refusal and 10 seconds", ok, wait)
	}
	clock.advance(10 * time.Second)
	if ok, _ := l.allow("k", LimitPairing); !ok {
		t.Fatal("the oldest hit did not leave the window")
	}
	if ok, _ := l.allow("other", LimitPairing); !ok {
		t.Fatal("keys share a bucket")
	}
}
