package api

import (
	"strings"
	"testing"

	"applysmith/internal/api/wire"
)

func TestDemoRoutes_TryTheDemoIsLocalSessionOnlyAndRemoteDenied(t *testing.T) {
	r := routeOf(t, "POST", "/api/setup/demo")
	if r.Access != AccessStandard || r.Remote != RemoteDenied || !r.SessionOnly || r.Body != BodyNone || r.Response != "DemoCreated" {
		t.Fatalf("route %+v", r)
	}
	f := &fakeSetup{}
	h := setupHarness(t, f)
	if res := h.post("/api/setup/demo", nil); res.StatusCode != 403 || res.errReason() != wire.ReasonSessionOnly {
		t.Fatalf("token: %d %s", res.StatusCode, res.body)
	}
	if len(f.hosts) != 0 {
		t.Fatal("a refused call reached the setup service")
	}
	got := decode[wire.DemoCreated](t, h.chat(h.session(), "POST", "/api/setup/demo", nil))
	if got.Postings != 3 || !strings.HasPrefix(got.Start, "applysmith serve --workspace") || len(f.hosts) != 1 || f.hosts[0].Workspace.Root != h.ws.Root {
		t.Fatalf("demo: %+v hosts %+v", got, f.hosts)
	}
}

func TestDemoRoutes_TheSampleFormIsServedOnlyByADemoWorkspace(t *testing.T) {
	r := routeOf(t, "GET", "/demo/sample-application")
	if r.Access != AccessPublic || r.Remote != RemoteDenied {
		t.Fatalf("route %+v", r)
	}
	h := setupHarness(t, &fakeSetup{})
	if res := h.do(call{method: "GET", path: "/demo/sample-application"}); res.StatusCode != 404 {
		t.Fatalf("outside the demo: %d", res.StatusCode)
	}
	if err := h.ws.MarkDemo(); err != nil {
		t.Fatal(err)
	}
	res := h.do(call{method: "GET", path: "/demo/sample-application"})
	if res.StatusCode != 200 || !strings.Contains(string(res.body), "sends nothing anywhere") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "form-action 'none'") {
		t.Fatalf("in the demo: %d %s", res.StatusCode, res.Header)
	}
}

// In a demo workspace the persona's document goes to any page, and each
// upload is audited as an allowed demo_workspace decision.
func TestDemoRoutes_TheResumeFileIsServedForAnyPageAndAudited(t *testing.T) {
	h := setupHarness(t, &fakeSetup{})
	if err := h.ws.MarkDemo(); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"", "https://boards.greenhouse.io/x/jobs/1", "http://127.0.0.1:47319"} {
		if res := h.get("/api/resumes/default/file?for=" + page); res.StatusCode == 403 {
			t.Fatalf("for %q: refused: %s", page, res.body)
		}
	}
	var allowed, refused int
	db := h.srv.store.Load().Read()
	if err := db.QueryRow(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'allow'`).Scan(&allowed); err != nil || allowed != 3 {
		t.Fatalf("%d audited uploads (%v), want 3", allowed, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'deny'`).Scan(&refused); err != nil || refused != 0 {
		t.Fatalf("%d audited refusals (%v), want 0", refused, err)
	}
}

// No route clears the marker: the config writers, the setup routes and the
// demo route itself leave it in place.
func TestDemoRoutes_NoRouteClearsTheMarker(t *testing.T) {
	h := setupHarness(t, &fakeSetup{})
	if err := h.ws.MarkDemo(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"profile", "portals", "cv", "narrative", "voice"} {
		doc := decode[wire.ConfigDocument](t, h.get("/api/config/"+f))
		h.put("/api/config/"+f, wire.ConfigSave{Content: doc.Content, ExpectedRevision: doc.Revision})
	}
	h.post("/api/setup/steps/workspace/apply", wire.SetupApplyRequest{Input: map[string]any{}})
	h.chat(h.session(), "POST", "/api/setup/demo", nil)
	if !decode[wire.SetupStatus](t, h.get("/api/setup/status")).Demo || !h.ws.IsDemo() {
		t.Fatal("a route cleared the demo marker")
	}
}
