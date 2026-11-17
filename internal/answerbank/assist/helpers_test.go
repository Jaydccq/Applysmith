package assist

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

const testWorkspaceID = "0123456789abcdef0123456789abcdef"

var testNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

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

// promptRec is one prompt the fake provider received.
type promptRec struct{ Task, Prompt string }

// env wires a real router (so the ledger is real) to the fake provider, and a
// Service over it with a stub knowledge base.
type env struct {
	t      *testing.T
	st     *store.Store
	router *llm.Router
	fake   *llm.Fake
	svc    *Service
	clock  *testClock
	know   *stubKnowledge

	mu      sync.Mutex
	prompts []promptRec
	script  func(task, prompt string) string
	explain func(prompt string) string
}

func newEnv(t *testing.T, fixtures ...llm.Fixture) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.StateDir, store.DBFile), store.Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	v, err := vault.New(testWorkspaceID, vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, clock: &testClock{t: testNow}, know: &stubKnowledge{}}
	e.fake = llm.NewFake(fixtures...)
	e.fake.SetGenerator(func(task, prompt string) (string, bool) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.prompts = append(e.prompts, promptRec{Task: task, Prompt: prompt})
		if e.script == nil {
			return "", false
		}
		if task == TaskExplainer && e.explain == nil {
			return "", false // no scripted explainer: the raw brief is used
		}
		if task == TaskExplainer {
			return e.explain(prompt), true
		}
		out := e.script(task, prompt)
		return out, out != ""
	})
	e.router = llm.New(st, v, llm.WithFake(e.fake), llm.WithOfflineSimulation(), llm.WithClock(e.clock.now))
	e.svc = New(Deps{Model: e.router, Knowledge: e.know, Now: e.clock.now, Store: st})
	return e
}

// respond installs the scripted model: it receives the task and the whole
// prompt and returns the completion text.
func (e *env) respond(f func(task, prompt string) string) { e.script = f }

// ledgerCalls counts the model calls recorded for a task ("" counts all).
func (e *env) ledgerCalls(task string) int {
	e.t.Helper()
	q, args := `SELECT count(DISTINCT call_id) FROM llm_ledger`, []any{}
	if task != "" {
		q += ` WHERE task = ?`
		args = append(args, task)
	}
	var n int
	if err := e.st.Read().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) ledgerFailures(task string) int {
	e.t.Helper()
	var n int
	if err := e.st.Read().QueryRow(`SELECT count(*) FROM llm_ledger WHERE task = ? AND outcome != 'ok'`, task).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// seen returns the prompts of a task, in order.
func (e *env) seen(task string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, p := range e.prompts {
		if p.Task == task {
			out = append(out, p.Prompt)
		}
	}
	return out
}

// stubKnowledge stands in for the knowledge base: fixed snippets and a brief.
type stubKnowledge struct {
	mu       sync.Mutex
	snippets []kb.Fact
	brief    *kb.Brief
	briefErr error
	searches int
	queries  []kb.Query
	briefReq []kb.BriefRequest
}

func (k *stubKnowledge) Search(_ context.Context, q kb.Query) (kb.Result, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.searches++
	k.queries = append(k.queries, q)
	return kb.Result{Facts: k.snippets, Status: kb.StatusGrounded}, nil
}

func (k *stubKnowledge) EnsureBrief(_ context.Context, req kb.BriefRequest) (*kb.Brief, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.briefReq = append(k.briefReq, req)
	if k.briefErr != nil {
		return nil, k.briefErr
	}
	if k.brief == nil {
		return nil, kb.ErrNoSite
	}
	return k.brief, nil
}

func snippet(ref, text string) kb.Fact {
	return kb.Fact{ID: "snippet:" + ref, Text: text, SourceKind: kb.KindSnippet, SourceRef: ref}
}

var errBoom = errors.New("boom")

// ---- matcher fixtures -------------------------------------------------

func opts(labels ...string) []match.Option {
	out := make([]match.Option, len(labels))
	for i, l := range labels {
		out[i] = match.Option{Value: strings.ToLower(strings.ReplaceAll(l, " ", "-")), Label: l}
	}
	return out
}

func selectQ(sel, text string, labels ...string) match.Question {
	return match.Question{Selector: sel, Text: text, Kind: match.KindSelect, Options: opts(labels...)}
}

func textQ(sel, text string, long bool) match.Question {
	q := match.Question{Selector: sel, Text: text, Kind: match.KindText}
	if long {
		q.InputType = match.InputLongText
	}
	return q
}

// matchAll runs the real matcher over a bank so the lanes see real results.
func matchAll(t *testing.T, bank match.Bank, req match.Request) (match.Request, match.Response) {
	t.Helper()
	if req.Today.IsZero() {
		req.Today = match.Date{Year: 2026, Month: 10, Day: 11}
	}
	resp, err := match.New(bank, match.DefaultStages()).Match(req)
	if err != nil {
		t.Fatal(err)
	}
	return req, resp
}

func fixedEntry(id, desc, answer string) match.Entry {
	return match.Entry{ID: id, Description: desc, Kind: match.KindFixed, Body: match.Body{Answer: answer}}
}

var enabled = Settings{LLMFallbackEnabled: true, Mode: ModeStrict}

// profileSources is a small fictional profile.
func profileSources() Sources {
	return Sources{
		Profile: []ProfileField{
			{Ref: "profile#city", Label: "City", Value: "Springfield", Category: CatCity},
			{Ref: "profile#country", Label: "Country", Value: "US", Category: CatCountry},
			{Ref: "profile#degree", Label: "Highest degree", Value: "Bachelor of Science in Computer Science", Category: CatDegree},
			{Ref: "profile#school", Label: "School", Value: "Example University", Category: CatSchool},
		},
		Identity:  []string{"Name: Alex Example", "School: Example University"},
		DegreeEnd: "2022-05",
		Degree:    "Bachelor of Science in Computer Science",
	}
}

// hand builds a request and a matcher response by hand: every question
// abstained with the given reason, prepared with its text as sent. It gives a
// test full control of what the lanes are offered.
func hand(reason match.Reason, qs ...match.Question) (match.Request, match.Response) {
	req := match.Request{Host: "jobs.example.com", Posting: match.Posting{Company: "Example Corp"}, Questions: qs,
		Today: match.Date{Year: 2026, Month: 10, Day: 11}}
	resp := match.Response{}
	for _, q := range qs {
		p := q
		p.Text = match.Sanitize(q.Text, q.Options)
		if kept, ok := match.StripQuestionDirectives(p.Text); ok {
			p.Text = kept
		}
		if p.InputType == "" {
			if q.Kind == match.KindText {
				p.InputType = match.InputText
			} else {
				p.InputType = match.InputSingleSelect
			}
		}
		resp.Prepared = append(resp.Prepared, p)
		resp.Results = append(resp.Results, match.Result{Selector: q.Selector, Outcome: match.OutcomeAbstained, Reason: reason,
			Trace: []string{"matcher: " + string(reason)}})
	}
	return req, resp
}

func (e *env) runHand(reason match.Reason, set Settings, src Sources, qs ...match.Question) []match.Result {
	req, resp := hand(reason, qs...)
	return e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: set, Sources: src})
}

// roleQ is an option question the role-fit policy takes: its reply needs no
// citation, so a test can exercise the budgets without arranging evidence.
func roleQ(n int) match.Question {
	return selectQ(fmt.Sprintf("#role%d", n), fmt.Sprintf("Which team number %d would you prefer to join?", n), "Alpha", "Beta", "Gamma")
}

func replyOption(label string) string { return `{"option":"` + label + `","facts":["f1"]}` }

// noRouteModel is a model with no route for any task.
type noRouteModel struct{ calls int }

func (m *noRouteModel) Resolve(_ context.Context, task string) (llm.Resolved, error) {
	return llm.Resolved{}, &llm.Error{Kind: llm.KindNoRoute, Task: task}
}

func (m *noRouteModel) Complete(context.Context, llm.Request) (*llm.Response, error) {
	m.calls++
	return nil, errBoom
}

// Sessions is the service's session table, for tests that read the budget.
func (s *Service) Sessions() *Sessions { return s.sessions }

// Count is the calls spent in the live session. Like any check it does not
// extend the idle window.
func (s *Sessions) Count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fresh(key).count
}
