package interview

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

var update = flag.Bool("update", false, "rewrite the golden files")

type fixture struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	svc  *Service
	kb   *kb.Service
	tr   *tracker.Tracker
	cand int64
	now  time.Time
}

type fixtureOpts struct {
	model   Model
	resume  string
	stories bool
}

func newFixture(t *testing.T, o fixtureOpts) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	ws := workspace.Open(root)
	if err := os.MkdirAll(ws.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return f.now }
	st, err := store.Open(ctx, filepath.Join(ws.StateDir(), "applysmith.db"), store.Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f.st = st
	tr, err := tracker.New(st, tracker.Options{Now: clock, LinkedTables: []tracker.LinkedTable{{Table: "interview_events", Column: "candidacy_id"}, {Table: "interview_packs", Column: "candidacy_id"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.tr = tr
	e, err := tr.Add(ctx, tracker.AddRow{Company: "Example Corp", Role: "Backend Engineer", URL: "https://jobs.example.com/backend-1"})
	if err != nil {
		t.Fatal(err)
	}
	f.cand = e.ID
	f.kb, err = kb.New(st, kb.Options{Workspace: ws, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: st, Model: o.model, Now: clock}
	if o.stories {
		copyTree(t, "testdata/knowledge", filepath.Join(root, "knowledge"))
		deps.Stories = f.kb
	}
	if o.resume != "" {
		deps.Resume = func(context.Context, int64) (string, error) { return o.resume, nil }
	}
	if f.svc, err = New(deps); err != nil {
		t.Fatal(err)
	}
	return f
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) stage(id, typ string, confirmed bool) Stage {
	f.t.Helper()
	st, err := f.svc.RecordStage(f.ctx, f.cand, StageInput{ID: id, Type: typ, Format: "video", Interviewers: []string{"Sam Sample"}, UserConfirmed: confirmed})
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// input loads the checked-in synthetic fixture for this fixture's candidacy.
func (f *fixture) input() Input {
	f.t.Helper()
	b, err := os.ReadFile("testdata/input.json")
	if err != nil {
		f.t.Fatal(err)
	}
	var in Input
	if err := json.Unmarshal(b, &in); err != nil {
		f.t.Fatal(err)
	}
	in.Candidacy = f.cand
	return in
}

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file; run with -update after checking the change.\ngot:\n%s", name, got)
	}
}

// scriptModel answers structured calls from a function and records the tasks.
type scriptModel struct {
	mu    sync.Mutex
	tasks []string
	fn    func(prompt string) (any, error)
}

func (m *scriptModel) Structured(_ context.Context, req llm.Request, _ json.RawMessage, out any) (*llm.Response, error) {
	m.mu.Lock()
	m.tasks = append(m.tasks, req.Task)
	m.mu.Unlock()
	v, err := m.fn(req.Prompt)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(v)
	return &llm.Response{Text: string(b)}, json.Unmarshal(b, out)
}

func step(list []Step, sub string) *Step {
	for i := range list {
		if strings.Contains(list[i].Text, sub) {
			return &list[i]
		}
	}
	return nil
}
