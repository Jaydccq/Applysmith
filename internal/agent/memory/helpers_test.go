package memory

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

// clock is the fake clock every test runs on; it moves only when told to.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type rig struct {
	mem      *Store
	sessions *sessions.Store
	db       *store.Store
	clock    *clock
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &clock{t: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)}
	return &rig{mem: New(db, c.now), sessions: sessions.New(db, c.now), db: db, clock: c}
}

// scripted answers each model call from a queue and records the requests.
type scripted struct {
	mu      sync.Mutex
	answers []string
	err     error
	reqs    []llm.Request
}

func (s *scripted) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return nil, s.err
	}
	text := `{"facts":[],"rules":[]}`
	if len(s.answers) > 0 {
		text, s.answers = s.answers[0], s.answers[1:]
	}
	return &llm.Response{Text: text}, nil
}

// converse stores a conversation of user messages; the caller ages it.
func (r *rig) converse(t *testing.T, lines ...string) string {
	t.Helper()
	ctx := context.Background()
	s, err := r.sessions.Create(ctx, sessions.Dashboard, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if _, err := r.sessions.Append(ctx, sessions.Message{SessionID: s.ID, TurnID: "t1", Role: sessions.RoleUser, Body: l, OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	return s.ID
}

func sessionsMessage(session, role, body string) sessions.Message {
	return sessions.Message{SessionID: session, TurnID: "t1", Role: sessions.Role(role), Body: body, OK: true}
}
