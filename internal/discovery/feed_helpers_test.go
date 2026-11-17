package discovery

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/workspace"
)

// seenReq is one request a fake vendor received.
type seenReq struct {
	Method, Host, Path, RawQuery, Body string
	Header                             http.Header
	Deadline                           time.Duration // time left on the request context when it arrived
}

func (s seenReq) query() url.Values { v, _ := url.ParseQuery(s.RawQuery); return v }

// feedServer is a fake vendor: it records every request and answers through a
// handler. Requests that go to other hosts reach it through rewriteTransport.
type feedServer struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []seenReq
	handle func(w http.ResponseWriter, r *http.Request, body string)
}

func newFeedServer(t *testing.T) *feedServer {
	f := &feedServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seenReq{Method: r.Method, Host: r.Header.Get("X-Original-Host"), Path: r.URL.Path, RawQuery: r.URL.RawQuery, Body: string(b), Header: r.Header.Clone()})
		h := f.handle
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r, string(b))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *feedServer) requests() []seenReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenReq(nil), f.reqs...)
}

func (f *feedServer) count(path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

// rewriteTransport sends every request to the fake server, whatever host the
// code asked for, and records the context deadline the code set.
type rewriteTransport struct {
	target *url.URL
	mu     sync.Mutex
	last   map[string]time.Duration // path -> time left on the request context
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if dl, ok := req.Context().Deadline(); ok {
		rt.mu.Lock()
		if rt.last == nil {
			rt.last = map[string]time.Duration{}
		}
		rt.last[req.URL.Path] = time.Until(dl)
		rt.mu.Unlock()
	}
	c := req.Clone(req.Context())
	c.Header.Set("X-Original-Host", req.URL.Host)
	c.URL.Scheme, c.URL.Host = rt.target.Scheme, rt.target.Host
	c.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(c)
}

func (rt *rewriteTransport) deadline(path string) time.Duration {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.last[path]
}

// feedEnv is an env whose scanner sends all traffic to one fake vendor.
func newFeedEnv(t *testing.T) (*env, *feedServer, *rewriteTransport) {
	t.Helper()
	e := newEnv(t)
	fs := newFeedServer(t)
	target, _ := url.Parse(fs.URL)
	rt := &rewriteTransport{target: target}
	e.sc.Client.HTTP = &http.Client{Transport: rt}
	e.sc.Endpoints = Endpoints{}
	return e, fs, rt
}

// withKeys makes the scanner read these vault secrets.
func (e *env) withKeys(keys map[string]string) {
	e.sc.Secret = func(name string) string { return keys[name] }
}

// queriesOf builds portals.yml source queries from strings and mappings.
func queriesOf(t *testing.T, items ...any) []yaml.Node {
	t.Helper()
	var out []yaml.Node
	for _, it := range items {
		var n yaml.Node
		if err := n.Encode(it); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// portalsWith adds a source with saved queries to a portals document.
func portalsWith(t *testing.T, p workspace.Portals, source string, queries ...any) workspace.Portals {
	t.Helper()
	if p.Sources == nil {
		p.Sources = map[string]workspace.Source{}
	}
	p.Sources[source] = workspace.Source{Queries: queriesOf(t, queries...)}
	return p
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTML(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(s))
}

func mustContain(t *testing.T, what, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%s = %q, want it to contain %q", what, s, sub)
	}
}

// ago is a time before the test epoch.
func ago(d time.Duration) time.Time { return epoch.Add(-d) }
