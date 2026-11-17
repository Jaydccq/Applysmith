package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
)

const cliProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex.example@example.com
history:
  employment:
    - employer: Example Systems
      title: Senior Engineer
      start: 2021-03
      end: present
skills:
  - name: Go
`

const cliCV = `# Alex Example

## Experience

### Senior Engineer, Example Systems (2021-03 to present)

- Led the platform migration and cut incidents by 40% across 12 services

## Skills

- Languages: Go, SQL
`

// docsEnv is an initialised workspace with a profile and a CV.
func docsEnv(t *testing.T) *testEnv {
	t.Helper()
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	for name, body := range map[string]string{"profile.yml": cliProfile, "cv.md": cliCV} {
		if err := os.WriteFile(filepath.Join(te.ws, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return te
}

func TestResumeGenerateBaselineWritesDraftThenListShowDelete(t *testing.T) {
	te := docsEnv(t)
	ctx := context.Background()
	o := te.run(ctx, "resume", "generate", "--baseline", "--workspace", te.ws, "--json")
	if o.code != 0 {
		t.Fatalf("generate: %+v", o)
	}
	var res wire.DocumentRunView
	if err := json.Unmarshal([]byte(o.stdout), &res); err != nil || res.Status != wire.RunSucceeded || len(res.Documents) != 1 {
		t.Fatalf("result %+v err %v out %s", res, err, o.stdout)
	}
	assertAPIShape(t, o.stdout)
	o = te.run(ctx, "documents", "list", "--workspace", te.ws, "--json")
	var docs wire.DocumentList
	if err := json.Unmarshal([]byte(o.stdout), &docs); o.code != 0 || err != nil || len(docs.Documents) != 1 || docs.Documents[0].PDFState != documents.PDFNone {
		t.Fatalf("list %+v err %v", o, err)
	}
	assertAPIShape(t, o.stdout)
	id := int64(docs.Documents[0].ID)
	if o = te.run(ctx, "documents", "show", itoa(id), "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "pdf: none") {
		t.Fatalf("show: %+v", o)
	}
	if o = te.run(ctx, "documents", "delete", itoa(id), "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "deleted") {
		t.Fatalf("delete: %+v", o)
	}
	o = te.run(ctx, "documents", "list", "--workspace", te.ws, "--json")
	if err := json.Unmarshal([]byte(o.stdout), &docs); err != nil || len(docs.Documents) != 0 {
		t.Fatalf("after delete: %+v", o)
	}
	// An empty list says what it listed and how to list a tracker row's.
	if o = te.run(ctx, "documents", "list", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "--candidacy <id>") {
		t.Fatalf("empty list: %+v", o)
	}
}

// After cv.md changes, the list names the version made before it and a
// command that, run as printed, makes a current one again.
func TestDocumentsListNamesAVersionMadeBeforeItsCVChanged(t *testing.T) {
	te := docsEnv(t)
	ctx := context.Background()
	if o := te.run(ctx, "resume", "generate", "--baseline", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("generate: %+v", o)
	}
	if err := os.WriteFile(filepath.Join(te.ws, "cv.md"), []byte(cliCV+"- Mentoring\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := te.run(ctx, "documents", "list", "--workspace", te.ws)
	_, cmd, _ := strings.Cut(o.stdout, "before cv.md changed; `applysmith ")
	cmd, _, found := strings.Cut(cmd, "`")
	if !found {
		t.Fatalf("no stale note: %+v", o)
	}
	if o = te.run(ctx, append(strings.Fields(cmd), "--workspace", te.ws)...); o.code != 0 {
		t.Fatalf("%s: %+v", cmd, o)
	}
	if o = te.run(ctx, "documents", "list", "--workspace", te.ws); strings.Contains(o.stdout, "made before") {
		t.Fatalf("still stale after %s: %s", cmd, o.stdout)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestResumeGenerateRejectsAmbiguousOrMissingTarget(t *testing.T) {
	te := docsEnv(t)
	ctx := context.Background()
	for _, args := range [][]string{
		{"resume", "generate"},
		{"resume", "generate", "--baseline", "--candidacy", "3"},
		{"cover-letter"},
		{"resume", "nonsense"},
	} {
		if o := te.run(ctx, append(args, "--workspace", te.ws)...); o.code != 2 {
			t.Errorf("%v: %+v", args, o)
		}
	}
}

// A document id is a whole positive integer: "1abc" names no document, so
// nothing is shown, deleted or reviewed under its numeric prefix.
func TestDocumentCommandsRefuseAnIDWithTrailingText(t *testing.T) {
	te := docsEnv(t)
	ctx := context.Background()
	if o := te.run(ctx, "resume", "generate", "--baseline", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("generate: %+v", o)
	}
	for _, args := range [][]string{
		{"documents", "show", "1abc"},
		{"documents", "delete", "1abc"},
		{"documents", "review", "9999abc", "--performed", "--sha256", strings.Repeat("a", 64)},
		{"documents", "show", "0"},
	} {
		if o := te.run(ctx, append(args, "--workspace", te.ws)...); o.code != 2 || o.stdout != "" {
			t.Errorf("%v: %+v", args, o)
		}
	}
	if o := te.run(ctx, "documents", "show", "1", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("document 1 is gone: %+v", o)
	}
}

// A stray word stops flag parsing, so every flag after it would be dropped;
// the command refuses it instead of running without them.
func TestDocumentCommandsRefuseALeftoverArgument(t *testing.T) {
	te := docsEnv(t)
	ctx := context.Background()
	for _, args := range [][]string{
		{"resume", "generate", "--baseline", "--workspace", te.ws, "x", "--pdf"},
		{"cover-letter", "--candidacy", "1", "--workspace", te.ws, "x", "--pdf"},
		{"documents", "list", "--workspace", te.ws, "x", "--json"},
	} {
		if o := te.run(ctx, args...); o.code != 2 || !strings.Contains(o.stderr, `unexpected argument "x"`) {
			t.Errorf("%v: %+v", args, o)
		}
	}
	if o := te.run(ctx, "documents", "list", "--workspace", te.ws, "--json"); o.code != 0 || strings.Contains(o.stdout, `"kind"`) {
		t.Fatalf("a refused generate wrote a document: %+v", o)
	}
}

// Beside a running server documents list and show read; a write still
// refuses, naming the dashboard.
func TestDocumentReadsWorkBesideAServerWritesRefuse(t *testing.T) {
	te := docsEnv(t)
	ctx := context.Background()
	if o := te.run(ctx, "resume", "generate", "--baseline", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("generate: %+v", o)
	}
	te.startServe()
	if o := te.run(ctx, "documents", "list", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "resume") {
		t.Fatalf("list beside a server: %+v", o)
	}
	if o := te.run(ctx, "documents", "show", "1", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "pdf: none") {
		t.Fatalf("show beside a server: %+v", o)
	}
	if o := te.run(ctx, "documents", "delete", "1", "--workspace", te.ws); o.code == 0 || !strings.Contains(o.stderr, "dashboard") {
		t.Fatalf("delete beside a server: %+v", o)
	}
}

func TestDocumentsReviewNeedsTheHashAndAPDF(t *testing.T) {
	te := docsEnv(t)
	ctx := context.Background()
	o := te.run(ctx, "resume", "generate", "--baseline", "--workspace", te.ws, "--json")
	var res wire.DocumentRunView
	if err := json.Unmarshal([]byte(o.stdout), &res); o.code != 0 || err != nil || len(res.Documents) != 1 {
		t.Fatalf("generate: %+v err %v", o, err)
	}
	id := itoa(int64(res.Documents[0]))
	hash := strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		args []string
		code int
	}{
		"no --performed":    {[]string{"documents", "review", id, "--sha256", hash}, 2},
		"no --sha256":       {[]string{"documents", "review", id, "--performed"}, 2},
		"no id":             {[]string{"documents", "review", "--performed", "--sha256", hash}, 2},
		"a draft, no PDF":   {[]string{"documents", "review", id, "--performed", "--sha256", hash}, 2},
		"an unknown record": {[]string{"documents", "review", "9999", "--performed", "--sha256", hash}, 1},
	} {
		if o := te.run(ctx, append(tc.args, "--workspace", te.ws)...); o.code != tc.code {
			t.Errorf("%s: code %d, want %d: %+v", name, o.code, tc.code, o)
		}
	}
}
