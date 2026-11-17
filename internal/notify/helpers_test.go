package notify

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"applysmith/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeSink records what it is asked and fails on demand.
type fakeSink struct {
	mu        sync.Mutex
	name      string
	off       bool
	err       error
	announced []Announcement
	withdrawn []withdrawal
	notices   []Announcement
	forgotten []string
	seen      []string
	next      int
}

type withdrawal struct {
	A   Announcement
	Ext string
	Why Why
}

func (f *fakeSink) Name() string { return f.name }
func (f *fakeSink) Available(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.off
}

func (f *fakeSink) Announce(_ context.Context, a Announcement) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.announced = append(f.announced, a)
	f.next++
	return f.name + "-" + strconv.Itoa(f.next), nil
}

func (f *fakeSink) Withdraw(_ context.Context, a Announcement, ext string, why Why) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdrawn = append(f.withdrawn, withdrawal{a, ext, why})
	return nil
}

func (f *fakeSink) Notice(_ context.Context, a Announcement) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, a)
}

func (f *fakeSink) Forget(_ context.Context, entryID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, entryID)
}

func (f *fakeSink) Seen(_ context.Context, handle string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, handle)
}

func (f *fakeSink) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeSink) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.announced {
		out = append(out, a.Title)
	}
	return out
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.announced)
}

type rig struct {
	t       *testing.T
	st      *store.Store
	clk     *clock
	board   *Board
	policy  *Policy
	desktop *fakeSink
	tg      *fakeSink
	quiet   QuietHours
	topics  []string
	mu      sync.Mutex
	// desk holds the entries started at the desk; every other entry counts
	// as asked for from the phone, so the mechanics tests see both sinks.
	desk map[string]bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := &clock{t: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, st: st, clk: clk, desktop: &fakeSink{name: "desktop"}, tg: &fakeSink{name: "telegram"}, desk: map[string]bool{}}
	r.build()
	return r
}

// build (re)creates the board and the policy over the same database, which is
// what a restart does.
func (r *rig) build() {
	r.board = NewBoard(r.st, BoardOptions{Now: r.clk.Now, Changed: func(topics ...string) {
		r.mu.Lock()
		r.topics = append(r.topics, topics...)
		r.mu.Unlock()
	}})
	r.policy = NewPolicy(PolicyOptions{Store: r.st, Board: r.board, Now: r.clk.Now, Sinks: []Sink{r.desktop, r.tg},
		Quiet: func(context.Context) QuietHours { return r.quiet },
		Asked: func(_ context.Context, id string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return !r.desk[id]
		}})
	r.board.SetObserver(r.policy)
}

func (r *rig) open(id string, origin Origin, kind Kind) Entry {
	r.t.Helper()
	e, err := r.board.Open(context.Background(), OpenArgs{ID: id, Kind: kind, Origin: origin, Title: "Entry " + id})
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

func (r *rig) move(id string, to State, a MoveArgs) Entry {
	r.t.Helper()
	e, err := r.board.Move(context.Background(), id, to, a)
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

func (r *rig) process() {
	r.t.Helper()
	if err := r.policy.Process(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) outboxStates() map[string]int {
	r.t.Helper()
	rows, err := r.st.Read().Query(`SELECT state, count(*) FROM notify_outbox GROUP BY state`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		_ = rows.Scan(&s, &n)
		out[s] = n
	}
	return out
}

var errDown = errors.New("the sink is down")

func ptr[T any](v T) *T { return &v }
