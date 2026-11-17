package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

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
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var epoch = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

type env struct {
	t   *testing.T
	st  *store.Store
	rp  *Repo
	clk *testClock
	srv *fakeATS
	sc  *Scanner
	ctx context.Context
	enq *fakeEnqueuer
}

type fakeEnqueuer struct {
	mu   sync.Mutex
	reqs []EvalRequest
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, r EvalRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	return "job-" + string(rune('a'+len(f.reqs)-1)), nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := &testClock{t: epoch}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := newFakeATS(t)
	rp := NewRepo(st, clk.now)
	client := NewClient()
	client.MinGap = 0
	client.Sleep = srv.sleep
	client.Now = clk.now
	enq := &fakeEnqueuer{}
	return &env{t: t, st: st, rp: rp, clk: clk, srv: srv, ctx: context.Background(), enq: enq,
		sc: &Scanner{Repo: rp, Client: client, Endpoints: srv.endpoints(), Enqueuer: enq}}
}

// budgetOnCue makes the next runs' budgets end only when fire is called, so a
// test reaches its budget at the step it names instead of waiting out a timer.
// The run's context then ends with errScanBudget as its cause.
func (e *env) budgetOnCue() (fire func()) {
	var mu sync.Mutex
	var live []context.CancelCauseFunc
	e.sc.budget = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancelCause(ctx)
		mu.Lock()
		live = append(live, cancel)
		mu.Unlock()
		return ctx, func() { cancel(nil) }
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		for _, cancel := range live {
			cancel(errScanBudget)
		}
	}
}

// fakeATS serves synthetic boards for all five ATS APIs.
type fakeATS struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]*route
	hits   map[string]int
	hook   func(path string)
	// methods counts requests by HTTP method.
	methods map[string]int
}

type route struct {
	status int
	body   string
	etag   string
}

func newFakeATS(t *testing.T) *fakeATS {
	f := &fakeATS{routes: map[string]*route{}, hits: map[string]int{}, methods: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeATS) sleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

func (f *fakeATS) serve(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path
	if r.URL.Query().Get("offset") != "" {
		key += "?offset=" + r.URL.Query().Get("offset")
	}
	if f.hook != nil {
		f.hook(key)
	}
	f.mu.Lock()
	rt := f.routes[key]
	f.hits[key]++
	f.methods[r.Method]++
	f.mu.Unlock()
	if rt == nil {
		http.NotFound(w, r)
		return
	}
	if rt.etag != "" {
		if r.Header.Get("If-None-Match") == rt.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", rt.etag)
	}
	status := rt.status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(rt.body))
}

func (f *fakeATS) set(path string, rt route) {
	f.mu.Lock()
	f.routes[path] = &rt
	f.mu.Unlock()
}

func (f *fakeATS) json(path string, v any) {
	b, _ := json.Marshal(v)
	f.set(path, route{body: string(b)})
}

func (f *fakeATS) hitCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakeATS) endpoints() Endpoints {
	e := Endpoints{}
	for _, a := range ATSNames {
		e[a] = f.URL
	}
	return e
}

// portals builds a portals document with the given filters.
func portals(positive, negative []string) workspace.Portals {
	var p workspace.Portals
	p.Version = 1
	p.Filters.Title.Positive = positive
	p.Filters.Title.Negative = negative
	return p
}

func (e *env) add(boards ...Board) {
	e.t.Helper()
	if _, err := e.rp.RegistryAdd(e.ctx, "test", boards...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) run(req ScanRequest) Record {
	e.t.Helper()
	if req.Source == "" {
		req.Source = "registry"
	}
	rec, err := e.sc.Run(e.ctx, req)
	if err != nil {
		e.t.Fatalf("run: %v", err)
	}
	return rec
}

func (e *env) registry(b Board) RegistryRow {
	e.t.Helper()
	row, err := e.rp.RegistryGet(e.ctx, b)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

func ghBoard(slug string) Board { return Board{ATS: Greenhouse, Slug: slug, Company: titleCase(slug)} }

func titleCase(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// iso formats a time for a vendor payload.
func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// goListImports returns the imports of the package's non-test files.
func goListImports(t *testing.T) ([]string, error) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, im := range f.Imports {
				out = append(out, strings.Trim(im.Path.Value, `"`))
			}
		}
	}
	return out, nil
}

// The readers below serve the tests only: nothing in the product reads a
// history row, the gate audit log or a location's single class back.

// HistoryRow is one canonical URL the scans have seen.
type HistoryRow struct {
	CanonicalURL string
	IdentityKey  string
	FirstSeen    time.Time
	LastSeen     time.Time
	Source       string
	Title        string
	Company      string
	Location     string
	Status       string
	Score        *int
	LastRunID    string
}

// historyUpsert writes or refreshes the row of a URL: a new URL gets the
// given status and score, a known one only moves its last-seen time and run.

// HistoryGet returns the row of a canonical URL.
func (r *Repo) HistoryGet(ctx context.Context, url string) (HistoryRow, error) {
	var h HistoryRow
	var first, last string
	var score sql.NullInt64
	err := r.s.Read().QueryRowContext(ctx, `SELECT canonical_url, identity_key, first_seen_at, last_seen_at, source, title, company, location, status, score, last_run_id
		FROM scan_history WHERE canonical_url = ?`, url).Scan(&h.CanonicalURL, &h.IdentityKey, &first, &last, &h.Source, &h.Title, &h.Company, &h.Location, &h.Status, &score, &h.LastRunID)
	if err == sql.ErrNoRows {
		return HistoryRow{}, ErrNotFound
	}
	if err != nil {
		return HistoryRow{}, err
	}
	h.FirstSeen, _ = time.Parse(timeLayout, first)
	h.LastSeen, _ = time.Parse(timeLayout, last)
	if score.Valid {
		v := int(score.Int64)
		h.Score = &v
	}
	return h, nil
}

// GateAudit is one rejected candidate with every rule it failed. The log is
// evidence only: no stage reads it to re-admit or retry a candidate.
type GateAudit struct {
	ID          int64
	At          time.Time
	Ingress     string
	IdentityKey string
	Title       string
	Location    string
	Company     string
	URL         string
	Rules       []string
}

// GateAuditList lists the audit rows of an ingress ("" for all), newest first.
func (r *Repo) GateAuditList(ctx context.Context, ingress string, limit int) ([]GateAudit, error) {
	q := `SELECT id, at, ingress, identity_key, title, location, company, url, rules_json FROM lead_gate_audit`
	var args []any
	if ingress != "" {
		q += ` WHERE ingress = ?`
		args = append(args, ingress)
	}
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT %d`, max(limit, 1))
	rows, err := r.s.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GateAudit
	for rows.Next() {
		var a GateAudit
		var at, rules string
		if err := rows.Scan(&a.ID, &at, &a.Ingress, &a.IdentityKey, &a.Title, &a.Location, &a.Company, &a.URL, &rules); err != nil {
			return nil, err
		}
		a.At, _ = time.Parse(timeLayout, at)
		_ = json.Unmarshal([]byte(rules), &a.Rules)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Classifications a test names besides the scopes and ClassElsewhere.
const (
	ClassMixed   = "mixed"
	ClassUnknown = "unknown"
)

// ClassOf names the classification of a location: one scope, elsewhere, mixed,
// or unknown.
func ClassOf(loc string) string {
	switch s := Classify(loc); len(s) {
	case 0:
		return ClassUnknown
	case 1:
		return s[0]
	}
	return ClassMixed
}

// queue sends a pending row to an evaluation the way every caller does, with
// a job that only has an id.
func (e *env) queue(id int64, jobID string) error {
	_, _, err := e.rp.QueueForEvaluation(e.ctx, InboxRow{ID: id}, nil, func() (string, error) { return jobID, nil })
	return err
}
