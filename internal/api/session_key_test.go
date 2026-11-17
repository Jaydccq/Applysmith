package api

import (
	"strings"
	"testing"
)

func TestSessionKeyRedirectFragmentCarriesTheKeyNotTheQuery(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: h.launchPath()})
	loc := r.Header.Get("Location")
	path, frag, found := strings.Cut(loc, "#")
	if r.StatusCode != 303 || path != "/" || !found {
		t.Fatalf("%d Location %q", r.StatusCode, loc)
	}
	key := r.sessionKey()
	if len(key) != 43 {
		t.Fatalf("key %q from fragment %q", key, frag)
	}
	if strings.Contains(strings.Join(r.Header.Values("Set-Cookie"), ";"), key) {
		t.Error("the key travels in the cookie")
	}
	if other := h.do(call{path: h.launchPath()}); other.sessionKey() == key {
		t.Error("two sessions share a key")
	}
}

// A cookie another server on the same host received (cookies ignore ports) is
// not enough on its own: the key lives in the dashboard page's own storage.
func TestStolenCookieWithoutTheSessionKeyIsRefused(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	origin := map[string]string{"Origin": h.srv.Origin()}

	read := h.do(call{path: "/api/runs/none", cookie: cookie, noKey: true})
	if read.StatusCode != 401 || read.errCode() != "unauthenticated" {
		t.Fatalf("cookie only, read: %d %s", read.StatusCode, read.body)
	}
	admin := h.do(call{method: "POST", path: "/api/pairing/codes", cookie: cookie, noKey: true, headers: origin})
	if admin.StatusCode != 401 {
		t.Fatalf("cookie only with a forged Origin reached an admin route: %d %s", admin.StatusCode, admin.body)
	}
	wrong := h.do(call{path: "/api/runs/none", cookie: cookie, sessionKey: strings.Repeat("A", 43)})
	if wrong.StatusCode != 401 {
		t.Fatalf("wrong key: %d", wrong.StatusCode)
	}
	if ok := h.do(call{method: "POST", path: "/api/pairing/codes", cookie: cookie, headers: origin}); ok.StatusCode != 200 {
		t.Fatalf("cookie and key: %d %s", ok.StatusCode, ok.body)
	}
}

func TestSessionKeyBelongsToItsOwnSession(t *testing.T) {
	h := newHarness(t)
	a, b := h.session(), h.session()
	h.keyMu.Lock()
	keyA := h.keys[a]
	h.keyMu.Unlock()
	if r := h.do(call{path: "/api/runs/none", cookie: b, sessionKey: keyA}); r.StatusCode != 401 {
		t.Errorf("the key of one session opened another: %d", r.StatusCode)
	}
}

func TestSessionKeyIsRequiredOnTheBoardStreamToo(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	if r := h.do(call{path: "/api/board/events", cookie: cookie, noKey: true}); r.StatusCode != 401 {
		t.Fatalf("stream without the key: %d", r.StatusCode)
	}
}

// A browser that kept its cookie but lost the page's storage opens a fresh
// launch link: the same session gets a new key, and no second session exists.
func TestFreshLaunchLinkRecoversALostKeyOnTheSameSession(t *testing.T) {
	h := newHarness(t)
	cookie := h.session()
	h.keyMu.Lock()
	oldKey := h.keys[cookie]
	h.keyMu.Unlock()
	if oldKey == "" {
		t.Fatal("no key issued")
	}
	r := h.do(call{path: h.launchPath(), cookie: cookie})
	newKey := r.sessionKey()
	if r.StatusCode != 303 || len(newKey) != 43 || newKey == oldKey {
		t.Fatalf("%d, key %q", r.StatusCode, newKey)
	}
	if r.cookie(SessionCookie) != nil {
		t.Error("a second session was issued")
	}
	if r := h.do(call{path: "/api/runs/none", cookie: cookie, sessionKey: oldKey}); r.StatusCode != 401 {
		t.Errorf("the lost key still works: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/api/runs/none", cookie: cookie, sessionKey: newKey}); r.StatusCode != 404 {
		t.Errorf("the new key was refused: %d", r.StatusCode)
	}
	// Without a code the document is still served as before.
	if r := h.do(call{path: "/", cookie: cookie, noKey: true}); r.StatusCode != 200 {
		t.Errorf("document: %d", r.StatusCode)
	}
	// The new key, not the lost one, is what a restart keeps.
	h.restart()
	if r := h.do(call{path: "/api/runs/none", cookie: cookie, sessionKey: oldKey}); r.StatusCode != 401 {
		t.Errorf("the lost key works after a restart: %d", r.StatusCode)
	}
	if r := h.do(call{path: "/api/runs/none", cookie: cookie, sessionKey: newKey}); r.StatusCode != 404 {
		t.Errorf("the new key was lost by a restart: %d", r.StatusCode)
	}
}
