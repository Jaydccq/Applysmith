// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/llm"
	"applysmith/internal/prompts"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const testWorkspaceID = "0123456789abcdef0123456789abcdef"

var testNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

type fakeInbox struct {
	mu       sync.Mutex
	released map[int64]string
	done     map[int64]Result
	// queued is the set of rows the fake holds in the queued state.
	queued map[int64]bool
}

func (f *fakeInbox) ReleaseQueued(_ context.Context, note string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.queued)
	for id := range f.queued {
		f.released[id] = note
		delete(f.queued, id)
	}
	return n, nil
}

func (f *fakeInbox) Release(_ context.Context, id int64, note string, _ discovery.Charge) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released[id] = note
	return false, nil
}

func (f *fakeInbox) Done(_ context.Context, id int64, r Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done[id] = r
	return nil
}

type tenv struct {
	t       *testing.T
	st      *store.Store
	tr      *tracker.Tracker
	root    string
	ev      *Evaluator
	router  *llm.Router
	fake    *llm.Fake
	prof    Profile
	inbox   *fakeInbox
	mu      sync.Mutex
	events  []Event
	blocked map[string]bool
	reader  Reader
	psetDir string
	model   Model
	closed  func(ctx context.Context, url string, l discovery.Liveness, inboxRowID int64) error
	// capped is what CapReached answers: the day's spend is at the daily cap.
	capped bool
}

func ptr[T any](v T) *T { return &v }

func testProfile() Profile {
	var p workspace.Profile
	p.Version = 1
	p.WorkAuthorization = workspace.WorkAuth{AuthorizedCountries: []string{"US", "CA"}, RequiresSponsorshipNow: ptr(false), RequiresSponsorshipFuture: ptr(false)}
	p.Targets.Roles = []workspace.Role{{Title: "Software Engineer", Seniority: "mid"}}
	p.Targets.Compensation = workspace.Compensation{Currency: "USD", Period: "year", Minimum: ptr(100000), Target: ptr(130000)}
	return Profile{Profile: p, CV: "Alex Example. Built Go services for six years.", Narrative: "Alex Example prefers backend work."}
}

func newTenv(t *testing.T, fixtures ...llm.Fixture) *tenv {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(root, store.StateDir, store.DBFile), store.Options{WorkspaceID: testWorkspaceID, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tr, err := tracker.New(st, tracker.Options{Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(testWorkspaceID, vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	fake := llm.NewFake(fixtures...)
	fake.SetGenerator(FakeGenerator)
	router := llm.New(st, v, llm.WithFake(fake), llm.WithOfflineSimulation(), llm.WithClock(func() time.Time { return testNow }))
	e := &tenv{t: t, st: st, tr: tr, root: root, router: router, fake: fake, prof: testProfile(),
		inbox: &fakeInbox{released: map[int64]string{}, done: map[int64]Result{}, queued: map[int64]bool{}}, blocked: map[string]bool{}}
	e.rebuild()
	return e
}

// rebuild recreates the evaluator after a field of the environment changed.
func (e *tenv) rebuild() {
	ev, err := New(Deps{
		Store: e.st, Tracker: e.tr, Model: e.modelOrRouter(), Prompts: prompts.New(e.psetDir),
		Profile: func(context.Context) (Profile, error) { return e.prof, nil },
		Reader:  e.reader, Inbox: e.inbox, Root: e.root, Closed: e.closed,
		Now:        func() time.Time { return testNow },
		Blocked:    func(c string) bool { return e.blocked[NormalizeCompany(c)] },
		Emit:       func(ev Event) { e.mu.Lock(); e.events = append(e.events, ev); e.mu.Unlock() },
		CapReached: func(context.Context) bool { return e.capped },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.ev = ev
}

// run creates a job and runs it to its outcome in the calling goroutine.
func (e *tenv) run(req Request) Result {
	e.t.Helper()
	req, err := NormalizeRequest(req)
	if err != nil {
		e.t.Fatal(err)
	}
	key := requestKey(req)
	id, err := e.ev.createJob(context.Background(), req, key, newIdempotencyKey())
	if err != nil {
		e.t.Fatal(err)
	}
	return e.ev.Run(context.Background(), id, req, key)
}

func (e *tenv) readIndex(number int64) (kind, rec, legit, verification string, score *float64) {
	e.t.Helper()
	var s *float64
	err := e.st.Read().QueryRow(`SELECT kind, recommendation, legitimacy, verification, score FROM report_index WHERE report_number = ?`, number).Scan(&kind, &rec, &legit, &verification, &s)
	if err != nil {
		e.t.Fatal(err)
	}
	return kind, rec, legit, verification, s
}

func (e *tenv) readMarkdown(number int64) string {
	e.t.Helper()
	var p string
	if err := e.st.Read().QueryRow(`SELECT markdown_path FROM report_index WHERE report_number = ?`, number).Scan(&p); err != nil {
		e.t.Fatal(err)
	}
	b, err := readFile(filepath.Join(e.root, filepath.FromSlash(p)))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *tenv) count(table string) int {
	e.t.Helper()
	var n int
	if err := e.st.Read().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *tenv) ledgerCalls(task string) int {
	e.t.Helper()
	var n int
	if err := e.st.Read().QueryRow(`SELECT count(*) FROM llm_ledger WHERE task = ?`, task).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// postingText returns synthetic posting text of at least 200 characters.
func postingText(extra string) string {
	return strings.TrimSpace(`Title: Software Engineer
Company: Example Systems
Location: Remote - United States

Example Systems builds scheduling software for clinics. You will design and build backend services in Go, own their reliability in production, and work with a small team on a shared roadmap. We value clear writing, careful design, and steady delivery.

Requirements: three or more years building production backend services; experience with SQL databases; comfort reading other people's code.
` + extra)
}

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func splitLines(s string) []string { return strings.Split(s, "\n") }

func (e *tenv) modelOrRouter() Model {
	if e.model != nil {
		return e.model
	}
	return e.router
}

// recorder wraps a Model and keeps every prompt it was asked.
type recorder struct {
	inner   Model
	mu      sync.Mutex
	prompts []string
	tasks   []string
	reqs    []llm.Request
}

func (r *recorder) Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, req.Prompt)
	r.tasks = append(r.tasks, req.Task)
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return r.inner.Structured(ctx, req, schema, out)
}

type deepOpts struct {
	must, nice, hidden, align []float64
	flags                     []float64
	snapshot                  string
	extra                     map[string]any
	gates                     []string
}

func items(prefix string, levels []float64, gates []string) []map[string]any {
	out := make([]map[string]any, len(levels))
	for i, l := range levels {
		ev := ""
		if l > 0 {
			ev = "CV: matches"
		}
		it := map[string]any{"id": fmt.Sprintf("%s%d", prefix, i+1), "label": fmt.Sprintf("%s item %d", prefix, i+1), "source": "posting", "hit_level": l, "evidence": ev}
		if gates != nil {
			g := "none"
			if i < len(gates) && gates[i] != "" {
				g = gates[i]
			}
			it["gate_class"] = g
		}
		out[i] = it
	}
	return out
}

// alignment is the four alignment dimensions. The given levels repeat in turn
// (one or two levels keep their mean); none gives four 0.5s, the neutral 3.0
// an empty list used to score.
func alignment(levels []float64) []map[string]any {
	if len(levels) == 0 {
		levels = []float64{0.5}
	}
	four := make([]float64, len(AlignmentIDs))
	for i := range four {
		four[i] = levels[i%len(levels)]
	}
	out := items("a", four, nil)
	for i, it := range out {
		it["id"] = AlignmentIDs[i]
	}
	return out
}

// deepReply builds a schema-valid deep-eval reply.
func deepReply(o deepOpts) string {
	if o.must == nil {
		o.must = []float64{1, 1, 1}
	}
	if o.snapshot == "" {
		o.snapshot = "A backend role."
	}
	flags := []map[string]any{}
	for i, l := range o.flags {
		flags = append(flags, map[string]any{"id": fmt.Sprintf("f%d", i+1), "label": "flag", "level": l, "evidence": "posting"})
	}
	m := map[string]any{
		"posting": map[string]any{"title": "Software Engineer", "company": "Example Systems", "location": "Remote", "work_model": "remote",
			"compensation": map[string]any{"text": "", "minimum": nil, "maximum": nil, "currency": "", "period": ""}, "employment_type": "full-time", "company_size": "51-200", "role_family": "backend"},
		"must_have": items("m", o.must, append([]string{}, o.gates...)), "nice_to_have": items("n", o.nice, nil), "hidden_signals": items("h", o.hidden, nil),
		"profile_alignment": alignment(o.align), "red_flags": flags,
		"legitimacy": map[string]any{"level": "credible", "signals": []string{}},
		"sections":   map[string]any{"snapshot": o.snapshot, "level_trajectory": "Level.", "pay_notes": "Pay.", "tailoring_plan": []string{"Plan.", "Second edit."}, "interview_prep": []string{"Prep."}, "keywords": []string{"go", "sql", "Go"}},
	}
	for k, v := range o.extra {
		m[k] = v
	}
	return jsonOf(m)
}

const answersReply = `{"answers":[{"question":"Why this company?","answer":"Because of the clinics.","status":"answered"},{"question":"Why this role?","answer":"","status":"unanswered"}]}`

func screenReply(score float64, decision, reason string, blockers ...string) string {
	if blockers == nil {
		blockers = []string{}
	}
	m := map[string]any{"score": score, "decision": decision, "legitimacy": "credible", "blockers": blockers, "reasons": []string{"fit"}, "concerns": []string{}, "summary": "Screen summary."}
	if reason != "" {
		m["review_reason"] = reason
	}
	return jsonOf(m)
}

func steps(texts ...string) []llm.Step {
	out := make([]llm.Step, len(texts))
	for i, t := range texts {
		out[i] = llm.Step{Text: t}
	}
	return out
}

func (e *tenv) entries() []tracker.Entry {
	e.t.Helper()
	p, err := e.tr.List(context.Background(), tracker.ListQuery{PageSize: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return p.Entries
}

func (e *tenv) phases() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		if ev.Final {
			out = append(out, "final:"+string(ev.Outcome))
		} else {
			out = append(out, string(ev.Phase))
		}
	}
	return out
}

type workspaceTier = workspace.TierPolicy

func fmtFloat(v float64) string { return fmt.Sprintf("%.1f", v) }
