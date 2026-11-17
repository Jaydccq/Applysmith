package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// apiPollMax is the pace the general limit allows: one request per
// window/count. The harness clock is frozen, so the limit counts requests for
// the whole test and a 429 never clears; a wait that polls faster than this
// for long enough (a real browser under load) spends the budget and fails
// on its own polling.
var apiPollMax = rateWindow / time.Duration(limitCounts[LimitGeneral])

// newPollPause returns a function that sleeps between polls: it starts at
// 10 ms so quick waits stay quick and doubles up to apiPollMax.
func newPollPause() func() {
	d := 10 * time.Millisecond
	return func() {
		time.Sleep(d)
		d = min(2*d, apiPollMax)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// harness is a started server over a temporary workspace with an in-memory
// keychain.
type harness struct {
	t      *testing.T
	srv    *Server
	ws     *workspace.Workspace
	vault  *vault.Vault
	mem    *vault.Memory
	clock  *fakeClock
	mods   []func(*Config)
	client *http.Client
	bare   bool // no test-only run routes

	keyMu sync.Mutex
	keys  map[string]string // session cookie value -> the key the redirect fragment carried
}

func newVaultFor(t *testing.T, ws *workspace.Workspace, mem *vault.Memory) *vault.Vault {
	t.Helper()
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(id, mem, vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// newHarness starts a server that is ready to serve.
func newHarness(t *testing.T, mods ...func(*Config)) *harness {
	t.Helper()
	h := newUnreadyHarness(t, mods...)
	h.ready()
	return h
}

// newUnreadyHarness starts a server whose database is not yet handed over.
func newUnreadyHarness(t *testing.T, mods ...func(*Config)) *harness {
	t.Helper()
	return startHarness(t, false, mods...)
}

// newBareHarness is a server with only its production routes.
func newBareHarness(t *testing.T, mods ...func(*Config)) *harness {
	t.Helper()
	h := startHarness(t, true, mods...)
	h.ready()
	return h
}

func startHarness(t *testing.T, bare bool, mods ...func(*Config)) *harness {
	t.Helper()
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	mem := vault.NewMemory()
	h := &harness{t: t, ws: ws, mem: mem, vault: newVaultFor(t, ws, mem), clock: newClock(), mods: mods, bare: bare,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives:     true,
				ExpectContinueTimeout: time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}}
	h.boot()
	return h
}

func (h *harness) config() Config {
	cfg := Config{Workspace: h.ws, Vault: h.vault, Version: "0.0.0-test", BuildID: "test-build", Now: h.clock.now}
	// A test reaches Gmail only through a fake it wires itself, and starts
	// each sync itself.
	cfg.Domain.Gmail.Dial = func(context.Context) (net.Conn, error) { return nil, errors.New("this test wired no fake Gmail") }
	cfg.Domain.Gmail.NoWatch = true
	if !h.bare {
		cfg.extra = append(cfg.extra, h.runRoutes()...)
	}
	for _, m := range h.mods {
		m(&cfg)
	}
	return cfg
}

func (h *harness) boot() {
	h.t.Helper()
	srv, err := New(h.config())
	if err != nil {
		h.t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.srv = srv
	h.t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
}

func (h *harness) ready() {
	h.t.Helper()
	id, err := h.ws.ID()
	if err != nil {
		h.t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(h.ws.StateDir(), store.DBFile), store.Options{Now: h.clock.now, WorkspaceID: id})
	if err != nil {
		h.t.Fatal(err)
	}
	h.srv.Ready(st)
}

// restart stops the server and starts a new one over the same workspace and
// keychain, as a process restart would.
func (h *harness) restart() {
	h.t.Helper()
	if err := h.srv.Shutdown(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.boot()
	h.ready()
}

func (h *harness) token() string {
	h.t.Helper()
	tok, err := h.vault.Get(vault.APIToken)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

func (h *harness) runCount() int {
	h.t.Helper()
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM runs`).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) logText() string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.ws.StateDir(), "logs", logFileName))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// call describes one request.
type call struct {
	method, path string
	host         string // overrides the Host header
	bearer       string
	cookie       string
	contentType  string
	body         []byte
	headers      map[string]string
	useToken     bool   // authenticate with the server's own token
	noKey        bool   // send the cookie without the session key
	sessionKey   string // sent instead of the key issued with the cookie
}

type result struct {
	*http.Response
	body []byte
}

func (h *harness) do(c call) result {
	h.t.Helper()
	if c.method == "" {
		c.method = http.MethodGet
	}
	var rd io.Reader
	if c.body != nil {
		rd = bytes.NewReader(c.body)
	}
	req, err := http.NewRequest(c.method, h.srv.Origin()+c.path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if c.host != "" {
		req.Host = c.host
	}
	if c.useToken {
		c.bearer = h.token()
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: c.cookie})
		key := c.sessionKey
		if key == "" && !c.noKey {
			h.keyMu.Lock()
			key = h.keys[c.cookie]
			h.keyMu.Unlock()
		}
		if key != "" {
			req.Header.Set(SessionKeyHeader, key)
		}
	}
	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	res := result{Response: resp, body: b}
	if ck := res.cookie(SessionCookie); ck != nil {
		h.keyMu.Lock()
		if h.keys == nil {
			h.keys = map[string]string{}
		}
		h.keys[ck.Value] = res.sessionKey()
		h.keyMu.Unlock()
	}
	return res
}

// sessionKey is the key in the fragment of a redirect that issued a session.
func (r result) sessionKey() string {
	loc := r.Header.Get("Location")
	_, frag, _ := strings.Cut(loc, "#")
	v, _ := url.ParseQuery(frag)
	return v.Get(SessionKeyFragment)
}

func (r result) json() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		panic("response is not JSON: " + string(r.body))
	}
	return m
}

func (r result) data() map[string]any {
	d, _ := r.json()["data"].(map[string]any)
	return d
}

func (r result) errCode() string {
	e, _ := r.json()["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (r result) errReason() string {
	e, _ := r.json()["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	s, _ := d["reason"].(string)
	return s
}

func (r result) cookie(name string) *http.Cookie {
	for _, c := range r.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// launchPath is the dashboard address with the server's current launch code.
func (h *harness) launchPath() string {
	h.t.Helper()
	code, _, err := h.srv.launch.current()
	if err != nil {
		h.t.Fatal(err)
	}
	return "/?" + launchParam + "=" + url.QueryEscape(code)
}

// session opens the dashboard with the launch code and returns the issued
// cookie value.
func (h *harness) session() string {
	h.t.Helper()
	r := h.do(call{path: h.launchPath()})
	ck := r.cookie(SessionCookie)
	if ck == nil {
		h.t.Fatalf("the launch code issued no session cookie (status %d)", r.StatusCode)
	}
	return ck.Value
}

func jsonBody(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func itoa(n int) string { return strconv.Itoa(n) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// withRoutes adds in-package test routes to the server.
func withRoutes(routes ...boundRoute) func(*Config) {
	return func(c *Config) { c.extra = append(c.extra, routes...) }
}

func testRoute(method, path string, mod func(*Route), h apikit.Handler) boundRoute {
	r := Route{Method: method, Path: path, Access: AccessStandard, Remote: RemoteDenied, Limit: LimitGeneral, Body: BodyNone,
		Request: PayloadNone, Response: PayloadNone}
	if mod != nil {
		mod(&r)
	}
	return boundRoute{Route: r, Handler: h}
}

func ok(c *apikit.Call) (apikit.Reply, error) {
	return apikit.Reply{Data: map[string]string{"ok": "yes"}}, nil
}

func newVaultWithEnv(t *testing.T, ws *workspace.Workspace, mem *vault.Memory, name, value string) *vault.Vault {
	t.Helper()
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(id, mem, vault.WithGetenv(func(k string) string {
		if k == name {
			return value
		}
		return ""
	}))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (h *harness) newCode() string {
	h.t.Helper()
	r := h.do(call{method: "POST", path: "/api/pairing/codes", useToken: true})
	if r.StatusCode != 200 {
		h.t.Fatalf("pairing code: %d %s", r.StatusCode, r.body)
	}
	return r.data()["code"].(string)
}

func (h *harness) claim(code string) result {
	return h.do(call{method: "POST", path: "/api/pairing/claim", contentType: "application/json", body: jsonBody(map[string]string{"code": code})})
}

// flushRecorder is a ResponseWriter that keeps what is written and supports
// flushing, like a real connection.
type flushRecorder struct {
	header http.Header
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (f *flushRecorder) Header() http.Header { return f.header }
func (f *flushRecorder) WriteHeader(int)     {}
func (f *flushRecorder) Flush()              {}
func (f *flushRecorder) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(b)
}

func (f *flushRecorder) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.String()
}
