package answerbank

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// newService opens a service over an initialised-but-unseeded workspace: the
// bank file does not exist until a test writes or seeds it.
func newService(t *testing.T) (*Service, *workspace.Workspace) {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	if err := os.MkdirAll(ws.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(ws.StateDir(), store.DBFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(ws, st), ws
}

func writeFile(t *testing.T, ws *workspace.Workspace, f workspace.File, text string) {
	t.Helper()
	if err := os.WriteFile(ws.Path(f), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, ws *workspace.Workspace, f workspace.File) string {
	t.Helper()
	b, err := os.ReadFile(ws.Path(f))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bankText wraps entry YAML in a version-1 file.
func bankText(entries ...string) string {
	return "version: 1\nemployers: []\nentries:\n" + strings.Join(entries, "") + "overrides: {}\n"
}

func fixedYAML(id, answer string, extra ...string) string {
	s := "  - id: " + id + "\n    description: d\n    kind: fixed\n    answer: " + answer + "\n"
	for _, e := range extra {
		s += "    " + e + "\n"
	}
	return s
}

func mustParse(t *testing.T, text string) Result {
	t.Helper()
	r, err := Parse("answer-bank.yml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hasWarning(r Result, parts ...string) bool {
	for _, w := range r.Warnings {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(w, p)
		}
		if ok {
			return true
		}
	}
	return false
}
