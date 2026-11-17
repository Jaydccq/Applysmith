package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/documents/toolchain"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

const testProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex.example@example.com
  phone: "+1 555 0100"
  address:
    city: Sampleville
    region: Exampleland
links:
  linkedin: https://www.example.com/in/alex-example
preferences:
  relocation:
    default: open
history:
  employment:
    - employer: Example Systems
      title: Senior Engineer
      start: 2021-03
      end: present
      location: Sampleville, Exampleland
    - employer: Sample Works
      title: Engineer
      start: 2018-01
      end: 2021-02
      location: Exampleland
  education:
    - school: Example University
      degree: BSc
      field: Mathematics
      start: 2011-09
      end: 2015-06
skills:
  - name: Go
  - name: SQL
`

const testCV = `# Alex Example

## Summary

Backend engineer focused on reliable data platforms.

## Experience

### Senior Engineer, Example Systems (2021-03 to present)

- Led the platform migration and cut incidents by 40% across 12 services
- Cut report time from 5 h to 20 min using a 3-step pipeline in Go & SQL
- Mentored four engineers in code review and design

### Engineer, Sample Works (2018-01 to 2021-02)

- Built the ingestion service used by twelve teams
- Owned on-call for a 99.9% availability target

## Projects

### Reef Survey Tool

- Built a Go service that parsed 5K+ survey files per day

## Skills

- Languages: Go, SQL, Python
- Tools: PostgreSQL, Docker
`

const testPosting = `Backend Engineer at Example Corp

Requirements:
- 3+ years of experience building backend services in Go
- Experience with PostgreSQL and SQL data modelling
- Ability to mentor engineers and lead code review

Nice to have:
- Experience with Docker
`

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// scriptModel answers structured calls from a function and records the tasks.
type scriptModel struct {
	mu    sync.Mutex
	tasks []string
	reqs  []llm.Request
	fn    func(task, prompt string) (any, error)
}

// callsTo counts the recorded calls of a task.
func (m *scriptModel) callsTo(task string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.tasks {
		if t == task {
			n++
		}
	}
	return n
}

// requestsTo returns the recorded requests of a task.
func (m *scriptModel) requestsTo(task string) []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []llm.Request
	for _, r := range m.reqs {
		if r.Task == task {
			out = append(out, r)
		}
	}
	return out
}

func (m *scriptModel) Structured(ctx context.Context, req llm.Request, _ json.RawMessage, out any) (*llm.Response, error) {
	m.mu.Lock()
	m.tasks = append(m.tasks, req.Task)
	m.reqs = append(m.reqs, req)
	id := "call-" + itoa(len(m.tasks))
	m.mu.Unlock()
	v, err := m.fn(req.Task, req.Prompt)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(v)
	return &llm.Response{Text: string(b), CallID: id}, json.Unmarshal(b, out)
}

type fixture struct {
	t     *testing.T
	ws    *workspace.Workspace
	st    *store.Store
	tr    *tracker.Tracker
	svc   *Service
	clk   *clock
	cand  int64
	model *scriptModel
}

type fixtureOpts struct {
	model    *scriptModel
	router   func(*store.Store) Model // builds a real router over the fixture store; used when model is nil
	toolchn  func(ctx context.Context) (*toolchain.Toolchain, error)
	profile  string
	cv       string
	noReport bool
	score    bool // leave the report below the floor
	emit     func(Event)
	// root is an existing directory to use as the workspace root.
	root string
	// builder scripts the toolchain's builds; the service then runs on a
	// toolchain that is ready but never started.
	builder func(*toolchain.Toolchain) pdfBuilder
	// retriever answers the knowledge-base queries; nil leaves the CV the only evidence.
	retriever Retriever
	// pairBudget shortens the compile, verify and repair budget.
	pairBudget time.Duration
}

func newFixture(t *testing.T, o fixtureOpts) *fixture {
	t.Helper()
	ctx := context.Background()
	root := o.root
	if root == "" {
		root = t.TempDir()
	}
	ws := workspace.Open(root)
	if o.profile == "" {
		o.profile = testProfile
	}
	if o.cv == "" {
		o.cv = testCV
	}
	must(t, os.WriteFile(filepath.Join(root, "profile.yml"), []byte(o.profile), 0o600))
	must(t, os.WriteFile(filepath.Join(root, "cv.md"), []byte(o.cv), 0o600))
	must(t, os.MkdirAll(ws.StateDir(), 0o700))
	clk := &clock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	st, err := store.Open(ctx, filepath.Join(ws.StateDir(), "applysmith.db"), store.Options{Now: clk.now})
	must(t, err)
	t.Cleanup(func() { st.Close() })
	tr, err := tracker.New(st, tracker.Options{Now: clk.now, LinkedTables: []tracker.LinkedTable{{Table: "documents", Column: "candidacy_id"}}})
	must(t, err)
	e, err := tr.Add(ctx, tracker.AddRow{Company: "Example Corp", Role: "Backend Engineer", URL: "https://jobs.example.com/backend-1"})
	must(t, err)
	f := &fixture{t: t, ws: ws, st: st, tr: tr, clk: clk, cand: e.ID, model: o.model}
	if !o.noReport {
		f.addReport(e.ID, !o.score)
	}
	if o.builder != nil && o.toolchn == nil {
		o.toolchn = func(context.Context) (*toolchain.Toolchain, error) { return &toolchain.Toolchain{}, nil }
	}
	d := Deps{Store: st, Tracker: tr, Workspace: ws, Now: clk.now, Toolchain: o.toolchn, Emit: o.emit, Retriever: o.retriever}
	switch {
	case o.model != nil:
		d.Model = o.model
	case o.router != nil:
		d.Model = o.router(st)
	}
	f.svc, err = New(d)
	must(t, err)
	f.svc.builderFor = o.builder
	if o.pairBudget > 0 {
		f.svc.pairBudget = o.pairBudget
	}
	must(t, f.svc.Start(ctx))
	t.Cleanup(f.svc.Close)
	return f
}

func (f *fixture) addReport(cand int64, eligible bool) {
	f.t.Helper()
	ctx := context.Background()
	err := f.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at)
			VALUES (7, 'report', 'reports/0007-example-corp.md', 'ready', 'h', 'x', 'x')`); err != nil {
			return err
		}
		el := 0
		if eligible {
			el = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, documents_eligible, created_at)
			VALUES (7, 'final', 'Example Corp', 'Backend Engineer', 'https://jobs.example.com/backend-1', 'k', 4.6, 'final', 'strong', 'credible', 'pasted unverified', 'reports/0007-example-corp.md', 'reports/0007-example-corp.html', 'j', ?, 'x')`, el); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET report_number = 7 WHERE id = ?`, cand)
		return err
	})
	must(f.t, err)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// run submits a request and waits for its result.
func (f *fixture) run(req Request) Result {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h, err := f.svc.Submit(ctx, Claim{Principal: "test", Key: "key-" + randKey()}, req)
	must(f.t, err)
	res, err := f.svc.Wait(ctx, h.RunID)
	must(f.t, err)
	return res
}

var keyMu sync.Mutex
var keyN int

func randKey() string {
	keyMu.Lock()
	defer keyMu.Unlock()
	keyN++
	return strings.Repeat("0", 6) + string(rune('a'+keyN%26)) + itoa(keyN)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.ws.Root, filepath.FromSlash(rel)))
	must(f.t, err)
	return string(b)
}

// briefRetriever answers a company-brief query with the given facts and every
// other query with nothing.
type briefRetriever []string

func (b briefRetriever) Search(_ context.Context, q kb.Query) (kb.Result, error) {
	var out kb.Result
	for _, k := range q.Kinds {
		if k != kb.KindBrief {
			continue
		}
		for i, text := range b {
			out.Facts = append(out.Facts, kb.Fact{ID: fmt.Sprintf("brief:%d", i+1), Text: text, SourceKind: kb.KindBrief, SourceRef: "https://example.com/about"})
		}
	}
	return out, nil
}
