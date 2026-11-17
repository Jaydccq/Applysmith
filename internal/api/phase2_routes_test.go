package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

var sampleValues = strings.NewReplacer("{id}", "1", "{number}", "1", "{provider}", "openai", "{companyId}", "1")

func routeOf(t *testing.T, method, path string) Route {
	t.Helper()
	for _, r := range Routes() {
		if r.Method == method && r.Path == path {
			return r
		}
	}
	t.Fatalf("%s %s is not in the table", method, path)
	return Route{}
}

// Every route the dashboard uses declares the class the specs give it.
func TestPhase2RouteClasses(t *testing.T) {
	if r := routeOf(t, "POST", "/api/candidacies/bulk-status"); !r.SessionOnly || r.Remote != RemoteDenied {
		t.Errorf("bulk status: %+v", r)
	}
	for _, r := range Routes() {
		if (r.Method == "GET" || r.Method == "HEAD") && r.Body != BodyNone {
			t.Errorf("%s takes a body", r.Pattern())
		}
		if r.Remote == RemoteAllowed && (r.Access == AccessAdmin || r.Access == AccessAgentProcess || r.Secret || r.SessionOnly ||
			r.Body == BodyUpload || r.Body == BodyJSONLarge) {
			t.Errorf("%s is remote: allowed, but it is admin, secret, session-only, a process route or takes an upload", r.Pattern())
		}
	}
}

func TestEveryAPIRouteRequiresACredentialUnlessPublic(t *testing.T) {
	h := newDomainHarness(t)
	for _, r := range routeDefs {
		if r.Access == AccessPublic {
			continue
		}
		c := call{method: r.Method, path: sampleValues.Replace(r.Path)}
		if r.Body == BodyJSON {
			c.contentType, c.body = "application/json", []byte("{}")
		}
		got := h.do(c)
		if got.StatusCode != http.StatusUnauthorized || got.errCode() != "unauthenticated" {
			t.Errorf("%s without a credential: %d %s", r.Pattern(), got.StatusCode, got.body)
		}
	}
}

func TestAdminRoutesAcceptTheSessionAndTheToken(t *testing.T) {
	h := newDomainHarness(t)
	cookie := h.session()
	r := h.do(call{method: "PUT", path: "/api/llm/keys/deepseek", cookie: cookie, contentType: "application/json",
		body: jsonBody(map[string]any{"key": testSecret}), headers: map[string]string{"Origin": h.srv.Origin()}})
	if r.StatusCode != 200 {
		t.Fatalf("session: %d %s", r.StatusCode, r.body)
	}
	noOrigin := h.do(call{method: "DELETE", path: "/api/llm/keys/deepseek", cookie: cookie})
	if noOrigin.StatusCode != 403 || noOrigin.errReason() != wire.ReasonOriginMismatch {
		t.Errorf("a cookie mutation without an Origin: %d %s", noOrigin.StatusCode, noOrigin.body)
	}
	if tok := h.do(call{method: "DELETE", path: "/api/llm/keys/deepseek", useToken: true}); tok.StatusCode != 200 {
		t.Errorf("token: %d %s", tok.StatusCode, tok.body)
	}
}

// Shutdown interrupts what is running, and the next start finds it ended.
func TestShutdownInterruptsRunningEvaluations(t *testing.T) {
	gate := newGateReader(postingBody("shutdown"))
	h := newDomainHarness(t, withReader(gate))
	r := h.startEvaluation(freshKey(), map[string]any{"url": "https://jobs.example.com/p/shutdown"})
	id := r.data()["runId"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for h.domainOf().pool.Stats().Active != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	h.restart()
	got := h.get("/api/evaluations/" + id).data()
	if got["status"] != wire.RunFailed || got["outcome"] != "failed" || got["reason"] != "interrupted" || got["reportNumber"] != nil {
		t.Fatalf("after a restart: %v", got)
	}
	if h.runCount() != 1 {
		t.Errorf("%d runs exist; a restart must not start another", h.runCount())
	}
}

func TestScanRunsCancelAndCloseWaitForTheScans(t *testing.T) {
	runs := newScanRuns()
	started, release := make(chan struct{}), make(chan struct{})
	var cause error
	err := runs.start(t.Context(), "run_a", "registry", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		cause = context.Cause(ctx)
		<-release
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if !runs.cancel("run_a") || runs.cancel("run_missing") {
		t.Fatal("cancel reported the wrong result")
	}
	closed := make(chan struct{})
	go func() { runs.close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close returned before the scan ended")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-closed
	if cause == nil || cause.Error() != "scan cancelled" {
		t.Errorf("the scan was cancelled with %v", cause)
	}
	if err := runs.start(t.Context(), "run_b", "registry", func(context.Context) {}); err == nil {
		t.Error("a scan started after close")
	}
}
