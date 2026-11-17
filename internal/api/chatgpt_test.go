package api

import (
	"net/url"
	"strings"
	"testing"
)

// The sign-in itself is proved against a fake issuer in internal/llm; these
// routes only start it, report it and end it.
func TestLLM_ChatGPTRoutesStartReportAndEndASignIn(t *testing.T) {
	h := newRoutingHarness(t)
	if d := h.get("/api/llm/chatgpt").data(); d["signedIn"] != false || d["pending"] != false {
		t.Fatalf("before sign-in: %v", d)
	}
	start := h.post("/api/llm/chatgpt/signin", nil)
	raw, _ := start.data()["url"].(string)
	u, err := url.Parse(raw)
	if start.StatusCode != 200 || err != nil || u.Host != "auth.openai.com" || !strings.HasPrefix(u.Query().Get("redirect_uri"), "http://127.0.0.1:") {
		t.Fatalf("sign-in: %d %s", start.StatusCode, start.body)
	}
	if d := h.get("/api/llm/chatgpt").data(); d["pending"] != true {
		t.Fatalf("a started sign-in is not pending: %v", d)
	}
	if r := h.get("/api/llm/chatgpt/models"); r.StatusCode != 409 {
		t.Errorf("models while signed out: %d %s", r.StatusCode, r.body)
	}
	out := h.post("/api/llm/chatgpt/signout", map[string]any{"forget": true})
	if out.StatusCode != 200 || out.data()["revoked"] != true {
		t.Fatalf("sign-out: %d %s", out.StatusCode, out.body)
	}
	if d := h.get("/api/llm/chatgpt").data(); d["pending"] != false {
		t.Errorf("sign-out left the attempt waiting: %v", d)
	}
	if r := h.post("/api/llm/chatgpt/signout", map[string]any{"unknown": 1}); r.StatusCode != 422 {
		t.Errorf("unknown field: %d", r.StatusCode)
	}
}

func TestLLM_ChatGPTSignInAndOutAreAdminRoutes(t *testing.T) {
	seen := 0
	for _, r := range Routes() {
		switch r.Path {
		case "/api/llm/chatgpt/signin", "/api/llm/chatgpt/signout":
			seen++
			if r.Access != AccessAdmin || r.Remote != RemoteDenied {
				t.Errorf("%s: access %s remote %s", r.Pattern(), r.Access, r.Remote)
			}
			if r.Path == "/api/llm/chatgpt/signin" && !r.Secret {
				t.Errorf("%s returns a sign-in address and is not marked secret", r.Pattern())
			}
		}
	}
	if seen != 2 {
		t.Fatalf("%d of the two sign-in and sign-out routes are registered", seen)
	}
}
