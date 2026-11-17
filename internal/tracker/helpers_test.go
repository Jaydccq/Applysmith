package tracker

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	t   *testing.T
	st  *store.Store
	tr  *Tracker
	clk *clock
	ctx context.Context
}

func newFixture(t *testing.T, opts ...func(*Options)) *fixture {
	t.Helper()
	clk := &clock{t: time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	o := Options{Now: clk.now}
	for _, f := range opts {
		f(&o)
	}
	tr, err := New(st, o)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, st: st, tr: tr, clk: clk, ctx: context.Background()}
}

func fp(v float64) *float64 { return &v }

// report builds a final-scored report for a synthetic posting.
func report(n int64, company, role, url string, score float64) Report {
	return Report{
		Number: n, Date: "2027-03-10", Outcome: OutcomeRecommended, Kind: ScoreFinal, Score: fp(score),
		Posting: identity.Posting{Company: company, Role: role, URL: url},
	}
}

// upsertReport records a report in a transaction of its own, as the
// evaluation does in its.
func (f *fixture) upsertReport(r Report) (res UpsertResult, err error) {
	err = f.st.Write(f.ctx, func(tx *sql.Tx) (err error) {
		res, err = f.tr.UpsertReportIn(f.ctx, tx, r)
		return err
	})
	return res, err
}

func (f *fixture) upsert(r Report) UpsertResult {
	f.t.Helper()
	res, err := f.upsertReport(r)
	if err != nil {
		f.t.Fatalf("UpsertReportIn: %v", err)
	}
	return res
}

func (f *fixture) add(a AddRow) Entry {
	f.t.Helper()
	e, err := f.tr.Add(f.ctx, a)
	if err != nil {
		f.t.Fatalf("Add: %v", err)
	}
	return e
}

func (f *fixture) setStatus(id int64, to Status, actor Actor) StatusResult {
	f.t.Helper()
	c := StatusChange{ID: id, To: to, Actor: actor}
	if actor == ActorAgent {
		c.AppliedBy = ByAgent
	}
	res, err := f.tr.SetStatus(f.ctx, c)
	if err != nil {
		f.t.Fatalf("SetStatus(%d -> %s by %s): %v", id, to, actor, err)
	}
	return res
}

func (f *fixture) get(id int64) Detail {
	f.t.Helper()
	d, err := f.tr.Get(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *fixture) events(id int64, kind EventKind) []Event {
	f.t.Helper()
	var out []Event
	for _, ev := range f.get(id).Events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.st.Read().QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}
