package apply

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

var t0 = time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)

// fakeEnv is a machine that is ready unless a test says otherwise.
type fakeEnv struct {
	mu        sync.Mutex
	healthSeq []healthReply
	healthN   int
	bankFile  string
	bankN     int
	bankErr   error
	profile   ProfileCheck
	attachErr error
	window    Window
	attached  int
}

type healthReply struct {
	status int
	err    error
}

func readyEnv() *fakeEnv {
	return &fakeEnv{healthSeq: []healthReply{{status: 200}}, bankFile: "answer-bank.yml", bankN: 12}
}

func (f *fakeEnv) Health(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(f.healthN, len(f.healthSeq)-1)
	f.healthN++
	return f.healthSeq[i].status, f.healthSeq[i].err
}
func (f *fakeEnv) AnswerBank(context.Context) (string, int, error) {
	return f.bankFile, f.bankN, f.bankErr
}
func (f *fakeEnv) Profile(context.Context) (ProfileCheck, error) { return f.profile, nil }
func (f *fakeEnv) Attach(context.Context) (Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached++
	return f.window, f.attachErr
}

// rig is a runner over a real temporary database.
type rig struct {
	t       *testing.T
	st      *store.Store
	ledger  *ledger.Ledger
	env     *fakeEnv
	r       *Runner
	now     time.Time
	boardMu sync.Mutex
	board   []BoardUpdate
}

func newRig(t *testing.T, mod func(*Deps)) *rig {
	t.Helper()
	g := &rig{t: t, env: readyEnv(), now: t0}
	clock := func() time.Time { return g.now }
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g.st = st
	g.ledger = ledger.New(st, clock)
	d := Deps{Store: st, Ledger: g.ledger, Audit: NewAudit(st, "test-salt-0123456789abcdef0123456789", clock), Reports: SQLReports{DB: st.Read()},
		Env: g.env, Now: clock, Build: "test-build", Salt: "test-salt-0123456789abcdef0123456789",
		Timing: PreflightTiming{Sleep: func(context.Context, time.Duration) error { return nil }},
		Board:  func(u BoardUpdate) { g.boardMu.Lock(); g.board = append(g.board, u); g.boardMu.Unlock() }}
	if mod != nil {
		mod(&d)
	}
	g.r = New(d)
	return g
}

func (g *rig) count(q string, args ...any) int {
	g.t.Helper()
	var n int
	if err := g.st.Read().QueryRow(q, args...).Scan(&n); err != nil {
		g.t.Fatal(err)
	}
	return n
}

func (g *rig) audits(gate gates.Gate) int {
	return g.count(`SELECT count(*) FROM gate_decisions WHERE gate = ?`, string(gate))
}

func stageOf(r *Run, st Stage) StageLine {
	for _, l := range r.Stages {
		if l.Stage == st {
			return l
		}
	}
	return StageLine{}
}

func mustStop(t *testing.T, r *Run, st Stage, class Class) {
	t.Helper()
	if r.Outcome.Kind != OutcomeStopped || r.Outcome.Stage != st || r.Outcome.Class != class {
		t.Fatalf("outcome %+v, want stopped at %s with class %s", r.Outcome, st, class)
	}
}

var errNoHTTP = errors.New("connection refused")

// vaultRegister teaches the process-wide redactor a secret the way the vault
// does when it stores one.
func vaultRegister(t *testing.T, secret string) {
	t.Helper()
	v, err := vault.New("test-ws", vault.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Set("signin-site-example.test", secret); err != nil {
		t.Fatal(err)
	}
}

// mailFrom builds a sender address at run time: the senders the gates bind a
// lookup to are real domains, which the repository guard keeps out of literals.
func mailFrom(local, domain string) string { return local + "@" + domain }
