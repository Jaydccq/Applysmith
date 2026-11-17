package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"applysmith/internal/api/apikit"
)

var securityHeaderNames = []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy", "Cross-Origin-Resource-Policy"}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Assets = fstest.MapFS{"index.html": {Data: []byte("<html></html>")}, "assets/app-1a2b3c.js": {Data: []byte("x")}}
	})
	for _, c := range []call{
		{path: "/api/health"}, {path: "/api/nothing"}, {path: "/"}, {path: "/assets/app-1a2b3c.js"},
		{path: "/api/runs/none"}, {method: "OPTIONS", path: "/api/health"}, {path: "/api/health", host: "attacker.example.com"},
	} {
		r := h.do(c)
		for _, name := range securityHeaderNames {
			if r.Header.Get(name) == "" {
				t.Errorf("%s %s (status %d) lacks %s", c.method, c.path, r.StatusCode, name)
			}
		}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s carries %s", c.path, k)
			}
		}
	}
	csp := h.do(call{path: "/api/health"}).Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("script policy: %s", csp)
	}
	// The document preview frames a blob URL the page made from a fetched PDF.
	if !strings.Contains(csp, "frame-src 'self' blob:;") || !strings.Contains(csp, "object-src 'none'") {
		t.Errorf("frame policy: %s", csp)
	}
	for _, d := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src") && strings.TrimSpace(d) != "script-src 'self'" {
			t.Errorf("script-src is %q", d)
		}
	}
	if h.do(call{path: "/api/health"}).Header.Get("Cache-Control") != "no-store" {
		t.Error("API response is cacheable")
	}
}

func TestPreflightIsRefused(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{method: "OPTIONS", path: "/api/apply/runs", headers: map[string]string{
		"Origin": "http://attacker.example.com", "Access-Control-Request-Method": "POST"}})
	if r.StatusCode != 405 || r.errCode() != "method_not_allowed" || r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestRouteTableValidates(t *testing.T) {
	if problems := validateRoutes(routeDefs); len(problems) != 0 {
		t.Fatalf("the route table has problems: %v", problems)
	}
	good := Route{Method: "POST", Path: "/api/x", Access: AccessStandard, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyJSON,
		Request: "ApplyRunRequest", Response: "RunAccepted"}
	mutations := map[string]func(*Route){
		"missing access":                  func(r *Route) { r.Access = "" },
		"unknown wire type":               func(r *Route) { r.Request = "NoSuchType" },
		"admin and remote":                func(r *Route) { r.Access = AccessAdmin; r.Remote = RemoteAllowed },
		"secret and remote":               func(r *Route) { r.Secret = true; r.Remote = RemoteAllowed },
		"session-only and remote":         func(r *Route) { r.SessionOnly = true; r.Remote = RemoteAllowed },
		"read route with body":            func(r *Route) { r.Method = "GET" },
		"json route without request type": func(r *Route) { r.Request = PayloadNone },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r := good
			mutate(&r)
			problems := validateRoutes([]Route{r})
			if len(problems) == 0 {
				t.Fatal("not detected")
			}
			if !strings.HasPrefix(problems[0], r.Pattern()) {
				t.Errorf("the problem does not name the route: %s", problems[0])
			}
		})
	}
	if problems := validateRoutes([]Route{good, good}); len(problems) == 0 {
		t.Error("a duplicate was not detected")
	}
}

// The router itself is asked, not the table: every table route must be what
// the router resolves its sample request to, and probe paths that are not in
// the table must resolve to the static catch-all or nothing.
func TestEveryRegisteredRouteIsInTheTable(t *testing.T) {
	h := newBareHarness(t)
	sample := strings.NewReplacer("{id}", "x1", "{number}", "1", "{provider}", "openai", "{path...}", "some/file")
	for _, r := range routeDefs {
		req, _ := http.NewRequest(r.Method, "http://127.0.0.1"+sample.Replace(r.Path), nil)
		if _, pat := h.srv.mux.Handler(req); pat != r.Pattern() {
			t.Errorf("%s resolves to %q in the router", r.Pattern(), pat)
		}
	}
	inTable := map[string]bool{}
	for _, r := range routeDefs {
		inTable[r.Pattern()] = true
	}
	for _, probe := range []struct{ method, path string }{
		{"GET", "/api/health"}, {"POST", "/api/apply/runs"}, {"GET", "/api/runs/abc"}, {"GET", "/api/board/events"},
		{"POST", "/api/pairing/codes"}, {"POST", "/api/pairing/claim"}, {"POST", "/api/token/rotate"}, {"POST", "/api/backups"},
		{"GET", "/api/unknown"}, {"POST", "/api/unknown"}, {"DELETE", "/api/runs/abc"}, {"PUT", "/api/health"}, {"GET", "/anything"},
	} {
		req, _ := http.NewRequest(probe.method, "http://127.0.0.1"+probe.path, nil)
		if _, pat := h.srv.mux.Handler(req); pat != "" && !inTable[pat] {
			t.Errorf("%s %s resolves to %q, which is not in the table", probe.method, probe.path, pat)
		}
	}
	for pat, b := range h.srv.routes {
		if !inTable[pat] || b.Handler == nil {
			t.Errorf("%s is registered but undeclared or unbound", pat)
		}
	}
}

func TestBindingsMustMatchTheTable(t *testing.T) {
	h := newHarness(t)
	good := h.srv.handlers()
	if err := checkBindings(routeDefs, good); err != nil {
		t.Fatalf("the real bindings: %v", err)
	}
	withExtra := map[string]apikit.Handler{"GET /api/secret-door": ok}
	for k, v := range good {
		withExtra[k] = v
	}
	if err := checkBindings(routeDefs, withExtra); err == nil || !strings.Contains(err.Error(), "secret-door") {
		t.Errorf("a handler outside the table was accepted: %v", err)
	}
	missing := map[string]apikit.Handler{}
	for k, v := range good {
		if k != patternHealth {
			missing[k] = v
		}
	}
	if err := checkBindings(routeDefs, missing); err == nil || !strings.Contains(err.Error(), "no handler") {
		t.Errorf("a route without a handler was accepted: %v", err)
	}
}

func TestUnregisteredPathAndWrongMethod(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: "/api/nothing", useToken: true})
	if r.StatusCode != 404 || r.errCode() != "not_found" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("unknown GET path: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{method: "POST", path: "/api/nothing", useToken: true}); r.StatusCode != 404 {
		t.Errorf("unknown POST path: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{method: "POST", path: "/api/health", useToken: true}); r.StatusCode != 405 || r.errCode() != "method_not_allowed" {
		t.Errorf("wrong method: %d %s", r.StatusCode, r.body)
	}
}

func TestEnvelopeShapes(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: "/api/runs/none"})
	j := r.json()
	e, _ := j["error"].(map[string]any)
	if r.StatusCode != 401 || j["ok"] != false || j["requestId"] == "" || e["code"] != "unauthenticated" || e["retryable"] != false || e["message"] == "" {
		t.Fatalf("error envelope: %d %s", r.StatusCode, r.body)
	}
	if j["requestId"] != r.Header.Get("X-Request-Id") {
		t.Error("body and header request identifiers differ")
	}
	ok := h.do(call{path: "/api/health"})
	if ok.json()["ok"] != true || ok.json()["data"] == nil {
		t.Errorf("success envelope: %s", ok.body)
	}
}

func TestClientRequestIdentifier(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{path: "/api/health", headers: map[string]string{"X-Request-Id": "run-check-0001"}})
	if r.Header.Get("X-Request-Id") != "run-check-0001" || r.json()["requestId"] != "run-check-0001" {
		t.Errorf("identifier not echoed: %v %s", r.Header, r.body)
	}
	for _, bad := range []string{"has space in it", strings.Repeat("a", 65)} {
		r := h.do(call{path: "/api/health", headers: map[string]string{"X-Request-Id": bad}})
		if got := r.Header.Get("X-Request-Id"); got == bad || !strings.HasPrefix(got, "req_") {
			t.Errorf("%q was accepted: %q", bad, got)
		}
	}
}

func TestPanicIsRecoveredAndLogged(t *testing.T) {
	boom := testRoute("GET", "/api/test/boom", nil, func(c *apikit.Call) (apikit.Reply, error) { panic("kaboom-internal-detail") })
	h := newHarness(t, withRoutes(boom))
	r := h.do(call{path: "/api/test/boom", useToken: true, headers: map[string]string{"X-Request-Id": "panic-check-01"}})
	if r.StatusCode != 500 || r.errCode() != "internal" || strings.Contains(string(r.body), "kaboom") || strings.Contains(string(r.body), "goroutine") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	log := h.logText()
	if !strings.Contains(log, "panic-check-01") || !strings.Contains(log, "kaboom-internal-detail") || !strings.Contains(log, "goroutine") {
		t.Errorf("the log lacks the stack:\n%s", log)
	}
}

func TestUnexpectedErrorsAreGenericToTheClient(t *testing.T) {
	bad := testRoute("GET", "/api/test/bad", nil, func(c *apikit.Call) (apikit.Reply, error) { return apikit.Reply{}, os.ErrPermission })
	h := newHarness(t, withRoutes(bad))
	r := h.do(call{path: "/api/test/bad", useToken: true})
	if r.StatusCode != 500 || strings.Contains(string(r.body), "permission") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSynchronousHandlerDeadline(t *testing.T) {
	slow := testRoute("GET", "/api/test/slow", nil, func(c *apikit.Call) (apikit.Reply, error) {
		<-c.R.Context().Done()
		return apikit.Reply{}, c.R.Context().Err()
	})
	h := newHarness(t, withRoutes(slow))
	h.srv.deadline = 50 * time.Millisecond
	r := h.do(call{path: "/api/test/slow", useToken: true})
	e, _ := r.json()["error"].(map[string]any)
	if r.StatusCode != 503 || e["code"] != "unavailable" || e["retryable"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// The dashboard keeps up to five reads in flight (a browser opens six
// connections to a host, and the board stream holds one); a read from the menu
// bar, the extension or a run still answers beside them.
func TestAReadAnswersBesideTheDashboardsReadsInFlight(t *testing.T) {
	h := newDomainHarness(t)
	pool := h.srv.store.Load().Read()
	ctx, cancel := context.WithTimeout(context.Background(), readBound)
	defer cancel()
	var held []*sql.Conn
	release := func() {
		for _, c := range held {
			c.Close()
		}
		held = nil
	}
	defer release()
	for range 5 {
		c, err := pool.Conn(ctx)
		if err != nil {
			t.Fatalf("the read pool does not hold the dashboard's five reads: %v", err)
		}
		held = append(held, c)
	}
	getWithin(t, h, "/api/runs/summary", release)
}

// The page gives up on a model call after MODEL_TIMEOUT_MS; the server must
// answer, or fall back, before that, or the page shows a timeout for work the
// server then throws away.
func TestPageWaitsLongerThanTheModelDeadline(t *testing.T) {
	src, err := os.ReadFile("../../web/src/api/client.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`MODEL_TIMEOUT_MS = ([\d_]+);`).FindSubmatch(src)
	if m == nil {
		t.Fatal("MODEL_TIMEOUT_MS is not declared in client.ts")
	}
	ms, err := strconv.Atoi(strings.ReplaceAll(string(m[1]), "_", ""))
	if err != nil || time.Duration(ms)*time.Millisecond <= modelDeadline {
		t.Fatalf("the page waits %s ms, the server %s", m[1], modelDeadline)
	}
}

func bodyRoutes() (boundRoute, boundRoute, *atomic.Int32) {
	var ran atomic.Int32
	type payload struct {
		Name string `json:"name"`
	}
	js := testRoute("POST", "/api/test/json", func(r *Route) { r.Body = BodyJSON; r.Request = "ApplyRunRequest" }, func(c *apikit.Call) (apikit.Reply, error) {
		ran.Add(1)
		var p payload
		if e := apikit.Decode(c.Body, &p); e != nil {
			return apikit.Reply{}, e
		}
		return apikit.Reply{Data: map[string]string{"name": p.Name}}, nil
	})
	up := testRoute("POST", "/api/test/upload", func(r *Route) { r.Body = BodyUpload }, func(c *apikit.Call) (apikit.Reply, error) {
		ran.Add(1)
		_, err := io.Copy(io.Discard, c.R.Body)
		return apikit.Reply{}, err
	})
	return js, up, &ran
}

func TestBodyLimitsAndStrictDecoding(t *testing.T) {
	js, up, ran := bodyRoutes()
	h := newHarness(t, withRoutes(js, up))
	post := func(path, ct string, body []byte) result {
		return h.do(call{method: "POST", path: path, useToken: true, contentType: ct, body: body})
	}
	deep := strings.Repeat(`{"a":`, 40) + "1" + strings.Repeat("}", 40)
	cases := []struct {
		name      string
		path, ct  string
		body      []byte
		status    int
		code      string
		handlerRn bool
	}{
		{"valid", "/api/test/json", "application/json", []byte(`{"name":"x"}`), 200, "", true},
		{"form encoded", "/api/test/json", "application/x-www-form-urlencoded", []byte("name=x"), 415, "unsupported_media_type", false},
		{"unknown field", "/api/test/json", "application/json", []byte(`{"name":"x","extra":1}`), 422, "validation_failed", true},
		{"too deep", "/api/test/json", "application/json", []byte(deep), 422, "validation_failed", true},
		{"not json", "/api/test/json", "application/json", []byte(`{name`), 400, "bad_request", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := ran.Load()
			r := post(tc.path, tc.ct, tc.body)
			if r.StatusCode != tc.status || (tc.code != "" && r.errCode() != tc.code) {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if ran := ran.Load() != before; ran != tc.handlerRn {
				t.Errorf("handler ran = %v, want %v", ran, tc.handlerRn)
			}
		})
	}
	r := post("/api/test/json", "application/json", []byte(`{"name":"x","extra":1}`))
	e, _ := r.json()["error"].(map[string]any)
	if d, _ := e["details"].(map[string]any); d["field"] != "extra" {
		t.Errorf("the unknown field is not named: %s", r.body)
	}
}

// expectClient sends the headers first and waits for the server's verdict, so
// a rejected body is never written and the 413 cannot be lost to a reset.
func expectClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, ExpectContinueTimeout: 5 * time.Second}}
}

func (h *harness) postExpecting(path, contentType string, body []byte) (*http.Response, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.srv.Origin()+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.token())
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Expect", "100-continue")
	resp, err := expectClient().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestOversizedJSONBodyNeverReachesTheHandler(t *testing.T) {
	js, _, ran := bodyRoutes()
	h := newHarness(t, withRoutes(js))
	body := []byte(`{"name":"` + strings.Repeat("a", 300000) + `"}`)
	resp, b := h.postExpecting("/api/test/json", "application/json", body)
	if resp.StatusCode != 413 || !strings.Contains(string(b), "payload_too_large") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if ran.Load() != 0 {
		t.Error("the handler ran")
	}
}

func TestUploadLimit(t *testing.T) {
	_, up, ran := bodyRoutes()
	h := newHarness(t, withRoutes(up))
	resp, b := h.postExpecting("/api/test/upload", "application/octet-stream", bytes.Repeat([]byte("a"), 12<<20))
	if resp.StatusCode != 413 || !strings.Contains(string(b), "payload_too_large") || ran.Load() != 0 {
		t.Fatalf("12 MiB upload: %d %s (handler ran %d times)", resp.StatusCode, b, ran.Load())
	}
	resp, _ = h.postExpecting("/api/test/upload", "application/octet-stream", bytes.Repeat([]byte("a"), 1<<20))
	if resp.StatusCode != 200 || ran.Load() != 1 {
		t.Errorf("1 MiB upload: %d (handler ran %d times)", resp.StatusCode, ran.Load())
	}
}

// Without a Content-Length the limit is enforced while reading. The body is
// only slightly over the limit, so the server drains the rest and closes
// cleanly; the client reads the response from a raw connection.
func TestChunkedOversizeBodyIsRefusedBeforeTheHandler(t *testing.T) {
	js, _, ran := bodyRoutes()
	h := newHarness(t, withRoutes(js))
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.srv.Origin(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	chunk := []byte(`{"name":"` + strings.Repeat("a", 270000) + `"}`)
	fmt.Fprintf(conn, "POST /api/test/json HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n",
		strings.TrimPrefix(h.srv.Origin(), "http://"), h.token(), len(chunk))
	conn.Write(chunk)
	fmt.Fprint(conn, "\r\n0\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 413 || !strings.Contains(string(b), "payload_too_large") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if ran.Load() != 0 {
		t.Fatalf("the handler ran %d times", ran.Load())
	}
}

func TestNoneRoutesRejectABody(t *testing.T) {
	h := newHarness(t)
	r := h.do(call{method: "POST", path: "/api/pairing/codes", useToken: true, contentType: "application/json", body: []byte(`{}`)})
	if r.StatusCode != 400 || r.errCode() != "bad_request" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestStaticDashboard(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>dash</title>")},
		"assets/app-1a2b3c.js": {Data: []byte("console.log(1)")},
		"favicon.svg":          {Data: []byte("<svg/>")},
	}
	h := newHarness(t, func(c *Config) { c.Assets = assets })
	r := h.do(call{path: "/tracker"})
	if r.StatusCode != 200 || !strings.Contains(string(r.body), "<title>dash</title>") || r.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("single-page fallback: %d %q %v", r.StatusCode, r.body, r.Header)
	}
	if r := h.do(call{path: "/", cookie: h.session()}); r.StatusCode != 200 || !strings.Contains(string(r.body), "<title>dash</title>") {
		t.Errorf("index: %d", r.StatusCode)
	}
	r = h.do(call{path: "/assets/app-1a2b3c.js"})
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset: %d %v", r.StatusCode, r.Header)
	}
	if r := h.do(call{path: "/favicon.svg"}); r.Header.Get("Cache-Control") == "public, max-age=31536000, immutable" {
		t.Error("an unhashed file is cached forever")
	}
	// A text asset goes out in gzip to a client that accepts it, cached as
	// immutably as before; a client that does not gets the bytes as they are.
	big := strings.Repeat("export const answer = 42;\n", 200)
	assets["assets/big-4d5e6f.js"] = &fstest.MapFile{Data: []byte(big)}
	r = h.do(call{path: "/assets/big-4d5e6f.js", headers: map[string]string{"Accept-Encoding": "br, gzip;q=0.8"}})
	if r.StatusCode != 200 || r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("Vary") != "Accept-Encoding" ||
		r.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || len(r.body) >= len(big)/4 {
		t.Fatalf("gzip asset: %d %v %d bytes", r.StatusCode, r.Header, len(r.body))
	}
	if zr, err := gzip.NewReader(bytes.NewReader(r.body)); err != nil {
		t.Fatal(err)
	} else if plain, err := io.ReadAll(zr); err != nil || string(plain) != big {
		t.Fatalf("the gzip body does not inflate to the file: %v", err)
	}
	if r := h.do(call{path: "/assets/big-4d5e6f.js", headers: map[string]string{"Accept-Encoding": "identity"}}); r.Header.Get("Content-Encoding") != "" || string(r.body) != big {
		t.Fatalf("Accept-Encoding identity: %v %d bytes", r.Header, len(r.body))
	}
	for _, p := range []string{"/missing.js", "/assets/gone.css"} {
		if r := h.do(call{path: p}); r.StatusCode != 404 || r.errCode() != "not_found" {
			t.Errorf("%s: %d %s", p, r.StatusCode, r.body)
		}
	}
	for _, p := range []string{"/api/nothing", "/api"} {
		r := h.do(call{path: p})
		if r.StatusCode != 404 || r.errCode() != "not_found" || strings.Contains(string(r.body), "<html") {
			t.Errorf("%s: %d %s", p, r.StatusCode, r.body)
		}
	}
}

func TestWorkspaceFilesAreNotServed(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.ws.Root, "cv.md"), []byte("Alex Example"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/cv.md", "/.applysmith/applysmith.db", "/reports/1.html"} {
		if r := h.do(call{path: p}); strings.Contains(string(r.body), "Alex Example") || r.StatusCode == 200 && p != "/reports/1.html" {
			t.Errorf("%s: %d %s", p, r.StatusCode, r.body)
		}
	}
}

func TestRequestLogLineHasRouteAndNoQuery(t *testing.T) {
	h := newHarness(t)
	h.do(call{path: "/api/runs/run_abc?cursor=private-cursor-value", useToken: true})
	log := h.logText()
	if strings.Contains(log, "private-cursor-value") || strings.Contains(log, "run_abc") {
		t.Errorf("the log has the raw path or query:\n%s", log)
	}
	for _, want := range []string{"method=GET", `route="GET /api/runs/{id}"`, "status=404", "principal=local-token", "request_id=req_", "ms="} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %s:\n%s", want, log)
		}
	}
}

func TestDebugLevelStillExcludesBodiesAndCredentials(t *testing.T) {
	js, _, _ := bodyRoutes()
	h := newHarness(t, withRoutes(js), func(c *Config) { c.LogLevel = "debug" })
	h.do(call{method: "POST", path: "/api/test/json", useToken: true, contentType: "application/json", body: []byte(`{"name":"body-marker-value"}`)})
	log := h.logText()
	if strings.Contains(log, "body-marker-value") || strings.Contains(log, h.token()) {
		t.Errorf("debug log leaks:\n%s", log)
	}
}

func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	rf, err := openRotating(path, 1000, logKeepFiles)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 99) + "\n")
	for range 200 {
		if _, err := rf.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	rf.Close()
	entries, _ := os.ReadDir(dir)
	if len(entries) != logKeepFiles {
		t.Errorf("%d files kept, want %d", len(entries), logKeepFiles)
	}
	for _, e := range entries {
		if info, _ := e.Info(); info.Size() > 1000 {
			t.Errorf("%s is %d bytes", e.Name(), info.Size())
		}
	}
}

// A rotation that cannot move the live file keeps logging to it, and rotates
// once the way is clear.
func TestLogRotation_AFailedRotationKeepsLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	rf, err := openRotating(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	blocker := path + ".1"
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"first--\n", "second-\n", "third--\n"} {
		if _, err := rf.Write([]byte(line)); err != nil {
			t.Fatalf("write %q: %v", line, err)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != "first--\nsecond-\nthird--\n" {
		t.Fatalf("the live file holds %q", got)
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := rf.Write([]byte("fourth-\n")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "fourth-\n" {
		t.Errorf("after the rotation the live file holds %q", got)
	}
	if got, _ := os.ReadFile(blocker); string(got) != "first--\nsecond-\nthird--\n" {
		t.Errorf("the rotated file holds %q", got)
	}
}

func TestCredentialInQueryInAnySeparatorForm(t *testing.T) {
	h := newHarness(t)
	tok := h.token()
	for _, q := range []string{
		"x=1;token=" + tok, "token=" + tok + ";x=1", "x=1&access_token=" + tok, "%74oken=" + tok,
		"TOKEN=" + tok, "a=1;b=2;api_key=" + tok, "token", "x=1&;Authorization=Bearer+" + tok,
	} {
		r := h.do(call{path: "/api/health?" + q})
		if r.StatusCode != 400 || r.errCode() != "bad_request" {
			t.Errorf("?%s: %d %s", q[:min(len(q), 24)], r.StatusCode, r.body)
		}
	}
	for _, q := range []string{"x=1;y=2", "tokens=abc", "cursor=a;b"} {
		if r := h.do(call{path: "/api/health?" + q}); r.StatusCode != 200 {
			t.Errorf("?%s: %d", q, r.StatusCode)
		}
	}
	if log := h.logText(); strings.Contains(log, tok) {
		t.Error("the credential reached the log")
	}
}

// The Ask Agent page names the open conversation in its address
// (/ask?conversation=<id>), and notifications link to it, so a document load of
// that address must serve the dashboard. The id is not a credential; a real
// credential parameter, including `session`, is still refused on the same path.
func TestAskConversationLinkIsADocumentLoadAndCredentialsStayRefused(t *testing.T) {
	assets := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>dash</title>")}}
	h := newHarness(t, func(c *Config) { c.Assets = assets })
	tok := h.token()
	for _, q := range []string{"conversation=sess_1", "tab=chat&conversation=sess%2F2"} {
		r := h.do(call{path: "/ask?" + q})
		if r.StatusCode != 200 || !strings.Contains(string(r.body), "<title>dash</title>") || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
			t.Errorf("/ask?%s: %d %q %v", q, r.StatusCode, r.body, r.Header.Get("Content-Type"))
		}
	}
	for _, q := range []string{"session=" + tok, "conversation=sess_1&token=" + tok, "conversation=sess_1;applysmith_session=" + tok} {
		r := h.do(call{path: "/ask?" + q})
		if r.StatusCode != 400 || r.errCode() != "bad_request" {
			t.Errorf("/ask?%s: %d %s", q[:min(len(q), 24)], r.StatusCode, r.body)
		}
	}
	if log := h.logText(); strings.Contains(log, tok) {
		t.Error("the credential reached the log")
	}
}

func TestNonStreamRoutesHaveAReadDeadline(t *testing.T) {
	js, _, ran := bodyRoutes()
	h := newHarness(t, withRoutes(js))
	h.srv.readTimeout = 200 * time.Millisecond
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.srv.Origin(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	// The body never completes: 5 of 100 promised bytes arrive.
	fmt.Fprintf(conn, "POST /api/test/json HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"na",
		strings.TrimPrefix(h.srv.Origin(), "http://"), h.token())
	start := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 || time.Since(start) > 5*time.Second || ran.Load() != 0 {
		t.Errorf("status %d after %v (handler ran %d times)", resp.StatusCode, time.Since(start), ran.Load())
	}
}

// The read deadline bounds the reading of the request, not the handler: a
// route with a longer deadline of its own works past it, with or without a
// body.
func TestReadDeadlineEndsOnceTheRequestIsRead(t *testing.T) {
	slow := func(c *apikit.Call) (apikit.Reply, error) {
		select {
		case <-c.R.Context().Done():
			return apikit.Reply{}, apikit.Unavailable("the request was cut short: " + context.Cause(c.R.Context()).Error())
		case <-time.After(500 * time.Millisecond):
			return ok(c)
		}
	}
	long := func(r *Route) { r.Deadline = 10 * time.Second }
	h := newHarness(t, withRoutes(testRoute("GET", "/api/test/slow", long, slow),
		testRoute("POST", "/api/test/slow", func(r *Route) { long(r); r.Body, r.Request = BodyJSON, "ApplyRunRequest" }, slow)))
	h.srv.readTimeout = 100 * time.Millisecond
	if r := h.do(call{path: "/api/test/slow", useToken: true}); r.StatusCode != 200 {
		t.Errorf("without a body: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{method: "POST", path: "/api/test/slow", useToken: true, contentType: "application/json", body: []byte(`{}`)}); r.StatusCode != 200 {
		t.Errorf("with a body: %d %s", r.StatusCode, r.body)
	}
}

func TestStreamsHaveNoReadDeadline(t *testing.T) {
	h := newHarness(t)
	h.srv.readTimeout = 50 * time.Millisecond
	h.srv.keepAlive = 300 * time.Millisecond
	s := h.openStream("/api/board/events", h.token(), "")
	s.next()
	if m := s.next(); m.comment != "keep-alive" {
		t.Fatalf("the stream did not survive past the read timeout: %+v", m)
	}
}

// An application run lives on the runs board and in apply_runs; starting one
// from the dashboard creates no row of the generic runs table, which is the
// table of the idempotent long operations.
func TestProductionApplyStartPersistsNoQueuedRun(t *testing.T) {
	h := newBareHarness(t)
	c := call{method: "POST", path: "/api/apply/runs", contentType: "application/json", body: []byte(`{"url":"https://jobs.example.com/openings/1234"}`), useToken: true}
	if r := h.do(c); r.StatusCode != 202 {
		t.Errorf("%d %s", r.StatusCode, r.body)
	}
	if h.runCount() != 0 {
		t.Errorf("%d runs persisted", h.runCount())
	}
}
