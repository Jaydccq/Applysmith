package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/store"
	"applysmith/internal/vault"
)

const testWorkspaceID = "0123456789abcdef0123456789abcdef"

type env struct {
	t     *testing.T
	st    *store.Store
	mem   *vault.Memory
	vault *vault.Vault
	envs  map[string]string
	r     *Router
	clock *testClock
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeKey assembles a credential-shaped value at run time; no key literal lives
// in the source.
func fakeKey(tag string) string {
	return "sk-" + strings.Repeat("a", 8) + tag + strings.Repeat("b", 24)
}

func newEnv(t *testing.T, opts ...Option) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.StateDir, store.DBFile), store.Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{t: t, st: st, mem: vault.NewMemory(), envs: map[string]string{},
		clock: &testClock{t: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}}
	e.vault, err = vault.New(testWorkspaceID, e.mem, vault.WithGetenv(func(k string) string { return e.envs[k] }))
	if err != nil {
		t.Fatal(err)
	}
	all := append([]Option{WithGetenv(func(k string) string { return e.envs[k] }), WithClock(e.clock.now)}, opts...)
	e.r = New(st, e.vault, all...)
	e.r.sleep = func(context.Context, time.Duration) error { return nil }
	return e
}

func (e *env) provider(p ProviderConfig, key string) {
	e.t.Helper()
	if err := e.r.SetProvider(context.Background(), p); err != nil {
		e.t.Fatal(err)
	}
	if key != "" {
		if err := e.r.SetKey(p.ID, key); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) route(task string, row Route) {
	e.t.Helper()
	if err := e.r.PatchRoutes(context.Background(), map[string]*Route{task: &row}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) setting(fn func(*Settings)) {
	e.t.Helper()
	if err := UpdateSettings(context.Background(), e.st, e.clock.now(), func(s *Settings) error { fn(s); return nil }); err != nil {
		e.t.Fatal(err)
	}
}

type ledgerRow struct {
	Task, Provider, Model, Outcome, Billing, Source, Replaced string
	Attempt, Status                                           int
	Cost                                                      *float64
	TTFT                                                      *int
	Clamped, Downgraded                                       bool
	Input, Output, Cached                                     int
	Finish                                                    string
}

func (e *env) ledger() []ledgerRow {
	e.t.Helper()
	rows, err := e.st.Read().Query(`SELECT task, provider_id, model, outcome, billing, candidate_source, COALESCE(replaced,''), attempt,
		COALESCE(http_status,0), cost_usd, ttft_ms, clamped, downgraded, input_tokens, output_tokens, cached_input_tokens, finish_reason
		FROM llm_ledger ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []ledgerRow
	for rows.Next() {
		var r ledgerRow
		var cl, dg int
		if err := rows.Scan(&r.Task, &r.Provider, &r.Model, &r.Outcome, &r.Billing, &r.Source, &r.Replaced, &r.Attempt,
			&r.Status, &r.Cost, &r.TTFT, &cl, &dg, &r.Input, &r.Output, &r.Cached, &r.Finish); err != nil {
			e.t.Fatal(err)
		}
		r.Clamped, r.Downgraded = cl == 1, dg == 1
		out = append(out, r)
	}
	return out
}

func outcomes(rows []ledgerRow) string {
	var s []string
	for _, r := range rows {
		s = append(s, r.Provider+":"+r.Outcome)
	}
	return strings.Join(s, ",")
}

// oaServer serves an OpenAI-compatible streaming endpoint. handler may write
// its own response; calls counts requests.
type oaServer struct {
	*httptest.Server
	mu     sync.Mutex
	calls  int
	last   map[string]any
	auth   string
	models []string
}

func newOAServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) *oaServer {
	t.Helper()
	s := &oaServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls++
		n := s.calls
		s.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.last = body
		if m, ok := body["model"].(string); ok {
			s.models = append(s.models, m)
		}
		s.mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *oaServer) lastModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.models[len(s.models)-1]
}

func (s *oaServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// oaStream writes a streaming reply with the given text pieces.
func oaStream(w http.ResponseWriter, finish string, in, out, cached int, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	for _, p := range pieces {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", b)
		f.Flush()
	}
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\n", finish)
	fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"prompt_tokens_details\":{\"cached_tokens\":%d}}}\n\n", in, out, cached)
	fmt.Fprint(w, "data: [DONE]\n\n")
	f.Flush()
}

func oaFail(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

func oaProvider(id string, s *oaServer) ProviderConfig {
	return ProviderConfig{ID: id, Kind: KindOpenAI, BaseURL: s.URL, StructuredOutput: true, Embeddings: true}
}

func (e *env) resolve(task string) Resolved {
	e.t.Helper()
	res, err := e.r.Resolve(bg(), task)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) show() RoutingTable {
	e.t.Helper()
	tab, err := e.r.Show(bg())
	if err != nil {
		e.t.Fatal(err)
	}
	return tab
}

func (e *env) load() Settings {
	e.t.Helper()
	s, err := LoadSettings(bg(), e.st)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func wantKind(t *testing.T, err error, k Kind) *Error {
	t.Helper()
	var e *Error
	if err == nil {
		t.Fatalf("want error of kind %s, got nil", k)
	}
	if got := KindOf(err); got != k {
		t.Fatalf("kind = %s (%v), want %s", got, err, k)
	}
	_ = asErr(err, &e)
	return e
}

func asErr(err error, target **Error) bool {
	e, ok := err.(*Error)
	*target = e
	return ok
}

func bg() context.Context { return context.Background() }

// waitWaiters waits until n callers are queued on the limiter.
func waitWaiters(t *testing.T, l *limiter, n int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		l.mu.Lock()
		got := l.waiters.Len()
		l.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiters = %d, want %d", got, n)
		}
	}
}

func decodeBody(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// reasons lists the ledger reasons in order, comma separated.
func (e *env) reasons() string {
	e.t.Helper()
	rows, err := e.st.Read().Query(`SELECT reason FROM llm_ledger ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}
