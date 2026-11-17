package signals

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/discovery"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

var epoch = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

const testWorkspaceID = "0123456789abcdef0123456789abcdef"

type clock struct {
	mu    sync.Mutex
	t     time.Time
	timed []clockDeadline
}

type clockDeadline struct {
	at  time.Time
	end context.CancelCauseFunc
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// advance moves the clock and ends every tick share whose deadline it reached.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	var due []clockDeadline
	c.timed = slices.DeleteFunc(c.timed, func(x clockDeadline) bool {
		if c.t.Before(x.at) {
			return false
		}
		due = append(due, x)
		return true
	})
	c.mu.Unlock()
	for _, x := range due {
		x.end(errTickBudget)
	}
}

// deadline is the service's deadline seam on this clock: the context ends
// with errTickBudget when advance reaches at, never on the wall clock.
func (c *clock) deadline(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
	ctx, end := context.WithCancelCause(ctx)
	c.mu.Lock()
	if c.t.Before(at) {
		c.timed = append(c.timed, clockDeadline{at: at, end: end})
		c.mu.Unlock()
	} else {
		c.mu.Unlock()
		end(errTickBudget)
	}
	return ctx, func() { end(context.Canceled) }
}

// web is the fake internet: every host maps to one local server, and the
// handler sees the host the client asked for. Every request is recorded.
type web struct {
	t      *testing.T
	mu     sync.Mutex
	routes []route
	seen   []seenReq
	srv    *httptest.Server
	clk    *clock
}

type route struct {
	host, prefix string
	h            func(w http.ResponseWriter, r *http.Request)
}

type seenReq struct {
	Host, Path, Query string
	UA                string
	At                time.Time
}

func newWeb(t *testing.T, clk *clock) *web {
	w := &web{t: t, clk: clk}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.seen = append(w.seen, seenReq{Host: r.Host, Path: r.URL.Path, Query: r.URL.RawQuery, UA: r.UserAgent(), At: clk.now()})
		routes := append([]route(nil), w.routes...)
		w.mu.Unlock()
		for i := len(routes) - 1; i >= 0; i-- {
			rt := routes[i]
			if (rt.host == "" || rt.host == r.Host) && strings.HasPrefix(r.URL.Path, rt.prefix) {
				rt.h(rw, r)
				return
			}
		}
		http.NotFound(rw, r)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

// on routes requests for host (empty: any) whose path starts with prefix.
func (w *web) on(host, prefix string, h func(http.ResponseWriter, *http.Request)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.routes = append(w.routes, route{host, prefix, h})
}

func (w *web) body(host, prefix, contentType, body string) {
	w.on(host, prefix, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", contentType)
		fmt.Fprint(rw, body)
	})
}

func (w *web) status(host, prefix string, code int) {
	w.on(host, prefix, func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(code) })
}

func (w *web) requests(host string) []seenReq {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []seenReq
	for _, s := range w.seen {
		if host == "" || s.Host == host {
			out = append(out, s)
		}
	}
	return out
}

func (w *web) client() *http.Client {
	target, _ := url.Parse(w.srv.URL)
	return &http.Client{Transport: rewrite{target: target}}
}

type rewrite struct{ target *url.URL }

func (rw rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Host = req.URL.Host
	r.URL.Scheme, r.URL.Host = rw.target.Scheme, rw.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// brain is the scripted model behind the fake provider: it answers a task from
// a function of the prompt and counts the calls.
type brain struct {
	mu    sync.Mutex
	calls map[string]int
	fn    func(task, prompt string) (string, bool)
}

func (b *brain) count(task string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[task]
}

func (b *brain) total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		n += c
	}
	return n
}

type env struct {
	t      *testing.T
	st     *store.Store
	svc    *Service
	clk    *clock
	web    *web
	brain  *brain
	sets   Settings
	vault  *vault.Memory
	keys   map[string]string
	enq    *fakeEnqueuer
	leads  *fakeLeads
	note   *fakeNotifier
	ctx    context.Context
	disc   *discovery.Repo
	router *llm.Router
}

type fakeEnqueuer struct {
	mu   sync.Mutex
	reqs []discovery.EvalRequest
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, r discovery.EvalRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	return fmt.Sprintf("job-%d", len(f.reqs)), nil
}

type fakeLeads struct {
	mu    sync.Mutex
	leads []Lead
	err   error
}

func (f *fakeLeads) AddLead(_ context.Context, l Lead) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.leads = append(f.leads, l)
	return nil
}

func (f *fakeLeads) byCategory(c string) []Lead {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Lead
	for _, l := range f.leads {
		if l.Category == c {
			out = append(out, l)
		}
	}
	return out
}

type fakeNotifier struct {
	mu       sync.Mutex
	notices  []Notice
	alerts   []Alert
	delivery Delivery
	fail     error
}

func (f *fakeNotifier) Announce(_ context.Context, n Notice) (Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return Delivery{}, f.fail
	}
	if f.delivery.Queued {
		f.notices = append(f.notices, n)
	}
	return f.delivery, nil
}

func (f *fakeNotifier) NeedsYou(_ context.Context, a Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, a)
	return nil
}

func (f *fakeNotifier) sent(kind string) []Notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Notice
	for _, n := range f.notices {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

func boolp(b bool) *bool { return &b }

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	clk := &clock{t: epoch}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.StateDir, store.DBFile), store.Options{WorkspaceID: testWorkspaceID, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := newWeb(t, clk)
	br := &brain{calls: map[string]int{}}
	fake := llm.NewFake()
	fake.SetGenerator(func(task, prompt string) (string, bool) {
		br.mu.Lock()
		br.calls[task]++
		fn := br.fn
		br.mu.Unlock()
		if fn == nil {
			return "", false
		}
		return fn(task, prompt)
	})
	mem := vault.NewMemory()
	v, err := vault.New(testWorkspaceID, mem)
	if err != nil {
		t.Fatal(err)
	}
	router := llm.New(st, v, llm.WithFake(fake), llm.WithOfflineSimulation(), llm.WithClock(clk.now))
	e := &env{t: t, st: st, clk: clk, web: w, brain: br, vault: mem, keys: map[string]string{}, ctx: ctx,
		enq: &fakeEnqueuer{}, leads: &fakeLeads{}, note: &fakeNotifier{delivery: Delivery{Queued: true}}}
	e.router = router
	e.disc = discovery.NewRepo(st, clk.now)
	e.sets = Settings{Portals: workspace.Portals{Version: 1}}
	e.sets.Profile.Candidate.Email = "alex@example.org"
	svc, err := New(Deps{Store: st, Discovery: e.disc, Model: router, Settings: func(context.Context) (Settings, error) { return e.sets, nil },
		Secret: func(n string) string { return e.keys[n] }, HTTP: w.client(), Leads: e.leads, Notifier: e.note, Enqueuer: e.enq, Now: clk.now,
		Sleep: func(_ context.Context, d time.Duration) error { clk.advance(d); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	svc.deadline = clk.deadline
	e.svc = svc
	return e
}

// enable turns a source on, with its queries.
func (e *env) enable(id string, queries ...string) {
	if e.sets.Portals.Sources == nil {
		e.sets.Portals.Sources = map[string]workspace.Source{}
	}
	var nodes []yaml.Node
	for _, q := range queries {
		nodes = append(nodes, yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: q})
	}
	e.sets.Portals.Sources[id] = workspace.Source{Enabled: boolp(true), Queries: nodes}
}

func (e *env) disable(id string) {
	if e.sets.Portals.Sources == nil {
		e.sets.Portals.Sources = map[string]workspace.Source{}
	}
	e.sets.Portals.Sources[id] = workspace.Source{Enabled: boolp(false)}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if err := e.st.Write(e.ctx, func(tx *sqlTx) error { _, err := tx.ExecContext(e.ctx, q, args...); return err }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Read().QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) tick(req ...TickRequest) TickSummary {
	e.t.Helper()
	var r TickRequest
	if len(req) > 0 {
		r = req[0]
	}
	sum, err := e.svc.Tick(e.ctx, r)
	if err != nil {
		e.t.Fatalf("tick: %v (summary %+v)", err, sum)
	}
	return sum
}

// doc stores a document directly and returns its id.
func (e *env) doc(d Doc) int64 {
	e.t.Helper()
	if d.FetchedAt.IsZero() {
		d.FetchedAt = e.clk.now()
	}
	if d.Adapter == "" {
		d.Adapter = adapterAlerts
	}
	id, _, err := e.svc.repo.InsertDocument(e.ctx, d)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// company makes a company with one funding event and its document.
func (e *env) company(name, body string, eventAt time.Time, adapter string) (Company, Event, int64) {
	e.t.Helper()
	canon := RuleCanon(name)
	c, err := e.svc.repo.UpsertCompany(e.ctx, name, canon)
	if err != nil {
		e.t.Fatal(err)
	}
	d := e.doc(Doc{SourceID: "signals-" + adapter, Adapter: adapter, URL: "https://techcrunch.com/2026/10/" + strings.ReplaceAll(canon, " ", "-"), PublishedAt: eventAt, Title: name + " raises", Body: body})
	id, _, err := e.svc.repo.AttachEvent(e.ctx, d, c.ID, TypeFunding, eventAt, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.repo.MarkIdentified(e.ctx, d); err != nil {
		e.t.Fatal(err)
	}
	ev, err := e.svc.repo.Event(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c, ev, d
}

// assessment stores an ok assessment for an event.
func (e *env) assessment(ev Event, a Assessment) int64 {
	e.t.Helper()
	a.EventID, a.Rubric = ev.ID, RubricVersion
	if a.Status == "" {
		a.Status = StatusOK
	}
	if a.Type == "" {
		a.Type = TypeFunding
	}
	if a.Scale == "" {
		a.Scale = ScaleHeadcount
	}
	if a.Intent == "" {
		a.Intent = IntentStrong
	}
	if a.Confidence == 0 {
		a.Confidence = 0.9
	}
	if a.Strength == 0 {
		a.Strength = 10
	}
	if err := e.svc.repo.StoreAssessment(e.ctx, a, "", "", false); err != nil {
		e.t.Fatal(err)
	}
	var id int64
	if err := e.st.Read().QueryRowContext(e.ctx, `SELECT id FROM signal_assessments WHERE event_id = ?`, ev.ID).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func atoi(s string) int {
	var n int
	fmt.Sscan(s, &n)
	return n
}

type sqlTx = sql.Tx

// profile gives the user target roles in the United States.
func (e *env) profile() {
	e.sets.Profile.Targets.Roles = []workspace.Role{{Title: "Software Engineer"}}
	e.sets.Profile.Targets.Locations = []string{"United States"}
	e.sets.Profile.WorkAuthorization.AuthorizedCountries = []string{"US"}
	e.sets.Profile.Preferences.WorkModes = []string{"remote"}
}

// hot makes a company with a strong, recent, ok assessment, scored.
func (e *env) hot(name string) Company {
	e.t.Helper()
	c, ev, doc := e.company(name, name+" raised $20M and will hire 30 engineers.", e.clk.now().Add(-24*time.Hour), adapterAlerts)
	e.assessment(ev, Assessment{Teams: []string{"engineering"}, Quotes: []Quote{{DocumentID: doc, Quote: "will hire 30 engineers"}}})
	if err := e.svc.Rescore(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) identify(c Company, ats, slug string) {
	e.t.Helper()
	if err := e.svc.repo.PutIdentity(e.ctx, Identity{CompanyID: c.ID, Status: IdentityResolved, ATS: ats, Slug: slug, ResolverVersion: ResolverVersion}); err != nil {
		e.t.Fatal(err)
	}
}

type ghJob struct {
	ID       int
	Title    string
	Location string
	Content  string
}

func (e *env) greenhouse(slug string, jobs ...ghJob) {
	var list []map[string]any
	for _, j := range jobs {
		list = append(list, map[string]any{"id": j.ID, "title": j.Title, "absolute_url": fmt.Sprintf("https://boards.greenhouse.io/%s/jobs/%d", slug, j.ID),
			"location": map[string]string{"name": j.Location}, "content": j.Content})
	}
	b, _ := json.Marshal(map[string]any{"jobs": list})
	e.web.body("boards-api.greenhouse.io", "/v1/boards/"+slug+"/jobs", "application/json", string(b))
}
