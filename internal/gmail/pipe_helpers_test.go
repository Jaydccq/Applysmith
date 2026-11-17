package gmail

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/tracker"
)

// pipeEnv is a stack over a real store and tracker with signals seeded
// straight into storage, the path the sync's store stage takes.
type pipeEnv struct {
	*env
	tr     *tracker.Tracker
	stack  *Stack
	model  *scriptedModel
	alerts []Alert
	run    int64
}

type scriptedModel struct {
	mu    sync.Mutex
	calls map[string]int
	fn    func(task, system, prompt string, n int) (Completion, error)
}

func (m *scriptedModel) Complete(ctx context.Context, task, system, prompt string) (Completion, error) {
	m.mu.Lock()
	m.calls[task]++
	n := m.calls[task]
	fn := m.fn
	m.mu.Unlock()
	if fn == nil {
		return Completion{}, fmt.Errorf("no route")
	}
	return fn(task, system, prompt, n)
}

func (m *scriptedModel) count(task string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[task]
}

func newPipeEnv(t *testing.T) *pipeEnv {
	t.Helper()
	e := newEnv(t)
	tr, err := tracker.New(e.st, tracker.Options{Now: e.clk.now})
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeEnv{env: e, tr: tr, model: &scriptedModel{calls: map[string]int{}}}
	p.stack = Open(e.st, e.vault, Options{Dial: e.imap.Dial, Now: e.clk.now, Tracker: tr, Model: p.model,
		Alerts: func(_ context.Context, a Alert) error { p.alerts = append(p.alerts, a); return nil }})
	e.connect()
	rec, err := e.repo.beginRun(e.ctx, TriggerCLI, ModeBootstrap, classify.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.run = rec.ID
	// A finished sync: alerts and digests wait for one.
	if err := e.repo.SetState(e.ctx, map[string]string{keyCursor: "7:1", keyCursorUpdated: stamp(e.clk.now())}); err != nil {
		t.Fatal(err)
	}
	return p
}

// sig describes one stored signal.
type sig struct {
	id, thread, typ, tier, dir string
	employer, role, subject    string
	rule, from, reasons, body  string
	at, due                    time.Time
}

func (p *pipeEnv) seed(sigs ...sig) {
	p.t.Helper()
	var batch []scoredMessage
	for _, s := range sigs {
		if s.thread == "" {
			s.thread = "t-" + s.id
		}
		if s.tier == "" {
			s.tier = classify.TierActionable
		}
		if s.dir == "" {
			s.dir = classify.DirIncoming
		}
		if s.employer == "" {
			s.employer = "Example Corp"
		}
		if s.subject == "" {
			s.subject = "Update from " + s.employer
		}
		if s.from == "" {
			s.from = "Recruiter <" + addr("jobs", "example.org") + ">"
		}
		if s.at.IsZero() {
			s.at = p.clk.now()
		}
		if s.rule == "" {
			s.rule = "test.rule"
		}
		if s.body == "" {
			s.body = "body of " + s.id
		}
		v := Verdict{Kind: VerdictSignal, Type: s.typ, Tier: s.tier, Direction: s.dir, Employer: s.employer, Role: s.role, Confidence: 0.9,
			RuleID: s.rule, Snippet: "snippet of " + s.id, Body: s.body}
		if s.reasons != "" {
			v.ReviewReasons = strings.Split(s.reasons, ",")
		}
		if !s.due.IsZero() {
			v.DueMS = s.due.UnixMilli()
		}
		batch = append(batch, scoredMessage{msg: &Message{ID: s.id, ThreadID: s.thread, InternalMS: s.at.UnixMilli(), From: s.from, Subject: s.subject}, verdict: v})
	}
	if _, _, err := p.repo.storeBatch(p.ctx, p.run, classify.Version, batch, nil, false); err != nil {
		p.t.Fatal(err)
	}
}

// add creates a tracker row as the user.
func (p *pipeEnv) add(company, role string, status tracker.Status) int64 {
	p.t.Helper()
	e, err := p.tr.Add(p.ctx, tracker.AddRow{Company: company, Role: role, Status: status})
	if err != nil {
		p.t.Fatal(err)
	}
	return e.ID
}

func (p *pipeEnv) status(id int64) tracker.Status {
	p.t.Helper()
	d, err := p.tr.Get(p.ctx, id)
	if err != nil {
		p.t.Fatal(err)
	}
	return d.Entry.Status
}

// sync runs the link stage as a sync would.
func (p *pipeEnv) link() {
	p.t.Helper()
	if _, err := p.stack.Pipeline.Link(p.ctx, p.run); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pipeEnv) ago(d time.Duration) time.Time { return p.clk.now().Add(-d) }

const hour = time.Hour
const day = 24 * time.Hour

// exec runs one statement against the store.
func (p *pipeEnv) exec(q string, args ...any) {
	p.t.Helper()
	err := p.st.Write(p.ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(p.ctx, q, args...); return err })
	if err != nil {
		p.t.Fatal(err)
	}
}
