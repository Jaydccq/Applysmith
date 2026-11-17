package kb

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

	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

type fixture struct {
	t   *testing.T
	svc *Service
	ws  *workspace.Workspace
	st  *store.Store
	ctx context.Context
	clk *testClock
}

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

func newFixture(t *testing.T, mut ...func(*Options)) *fixture {
	t.Helper()
	root := t.TempDir()
	ws := workspace.Open(root)
	clk := &testClock{t: time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)}
	st, err := store.Open(context.Background(), filepath.Join(root, ".applysmith", "applysmith.db"), store.Options{Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	opts := Options{Workspace: ws, Now: clk.now}
	for _, m := range mut {
		m(&opts)
	}
	svc, err := New(st, opts)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, svc: svc, ws: ws, st: st, ctx: context.Background(), clk: clk}
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.ws.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.ws.Root, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(f.ws.Root, filepath.FromSlash(rel)))
	return err == nil
}

func (f *fixture) search(query string, mut ...func(*Query)) Result {
	f.t.Helper()
	q := Query{Query: query}
	for _, m := range mut {
		m(&q)
	}
	res, err := f.svc.Search(f.ctx, q)
	if err != nil {
		f.t.Fatalf("search %q: %v", query, err)
	}
	return res
}

// snippet renders a snippet file from a frontmatter body and a Markdown body.
func snippet(front, body string) string {
	if front == "" {
		return body + "\n"
	}
	return "---\n" + strings.TrimSpace(front) + "\n---\n\n" + strings.TrimSpace(body) + "\n"
}

func ids(r Result) []string {
	var out []string
	for _, f := range r.Facts {
		out = append(out, f.ID)
	}
	return out
}

func sourceRefs(r Result) []string {
	var out []string
	for _, f := range r.Facts {
		out = append(out, f.SourceRef)
	}
	return out
}

func equal(a, b []string) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// snippetIDs returns the ids of the snippet facts of r, in order; the
// "semantic:" facts of a snippet are left out.
func snippetIDs(r Result) []string {
	var out []string
	for _, f := range r.Facts {
		if f.SourceKind == KindSnippet && !strings.HasPrefix(f.ID, "semantic:") {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(f.SourceRef, "#", 2)[0], "knowledge/"), ".md"))
		}
	}
	return out
}

// exec runs statements against the store, one write each.
func (f *fixture) exec(stmts ...string) {
	f.t.Helper()
	for _, q := range stmts {
		if err := f.st.Write(f.ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(f.ctx, q); return err }); err != nil {
			f.t.Fatalf("%s: %v", q, err)
		}
	}
}

// failCommitAfter makes every transaction that fires the trigger event fail
// when it commits, through a deferred foreign key; the returned statement
// takes the trigger away.
func (f *fixture) failCommitAfter(event string) (drop string) {
	f.t.Helper()
	f.exec(`CREATE TABLE IF NOT EXISTS test_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS test_child (parent INTEGER REFERENCES test_parent (id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER test_fail_commit AFTER `+event+` BEGIN INSERT INTO test_child VALUES (1); END`)
	return `DROP TRIGGER test_fail_commit`
}

type sqlTx = *sql.Tx

type ctxT = context.Context

func marshalForTest(v any) ([]byte, error) { return json.Marshal(v) }
