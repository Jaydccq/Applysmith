package documents

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/toolchain"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// These tests kill a real process. The parent starts the test binary again; the
// child writes a draft, starts a PDF run that needs repair rounds and exits at
// the named point without running a deferred function, as a crash would.
const (
	envCrashRoot = "APPLYSMITH_TEST_CRASH_ROOT"
	envCrashAt   = "APPLYSMITH_TEST_CRASH_AT" // "<fake|real>-<rounds|publish>"
	crashExit    = 87
)

// crashBuilder ends the process at its at-th build.
type crashBuilder struct {
	inner pdfBuilder
	at, n int
}

func (c *crashBuilder) Build(ctx context.Context, in toolchain.BuildInput) (toolchain.BuildOutput, error) {
	if c.n++; c.n == c.at {
		os.Exit(crashExit)
	}
	return c.inner.Build(ctx, in)
}

func TestRepairCrashChild(t *testing.T) {
	root, at := os.Getenv(envCrashRoot), os.Getenv(envCrashAt)
	if root == "" {
		t.Skip("started by the crash tests")
	}
	kind, point, _ := strings.Cut(at, "-")
	opts, pages, crashAt := fixtureOpts{root: root, profile: leanProfile, cv: repairCV(5, 4, 3, 0)}, 2, 4 // the rebuild of round 2
	inner := func(*toolchain.Toolchain) pdfBuilder { return &fakeBuilder{resumeCap: 331, itemPt: 9} }
	if kind == "real" {
		opts.toolchn, opts.cv, pages, crashAt = realToolchain(t), repairCV(3, 3, 0, 430), 1, 3 // the rebuild of round 1
		inner = func(tc *toolchain.Toolchain) pdfBuilder { return tc }
	}
	opts.builder = inner
	if point == "rounds" {
		opts.builder = func(tc *toolchain.Toolchain) pdfBuilder { return &crashBuilder{inner: inner(tc), at: crashAt} }
	}
	f := newFixture(t, opts)
	draft := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Pages: pages}})
	if !draft.Published {
		t.Fatalf("draft = %+v", draft)
	}
	if point == "publish" {
		f.svc.beforeCommit = func() { os.Exit(crashExit) }
	}
	f.run(Request{Action: ActionPDF, DocumentID: draft.Documents[0]})
	t.Fatal("the process survived the crash point")
}

func filesUnder(t *testing.T, dir string) (out []string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return out
}

// versionDirs lists the bundle/version directories below documents/.
func versionDirs(t *testing.T, root string) (out []string) {
	for _, f := range filesUnder(t, filepath.Join(root, "documents")) {
		if parts := strings.Split(f, "/"); len(parts) >= 2 && !hasID(out, parts[0]+"/"+parts[1]) {
			out = append(out, parts[0]+"/"+parts[1])
		}
	}
	return out
}

func TestRepair_CrashBetweenRoundsOrDuringPublishLeavesNoHalfVersion(t *testing.T) {
	for _, at := range []string{"fake-rounds", "fake-publish"} {
		t.Run(at, func(t *testing.T) { checkCrash(t, at) })
	}
}

// The same crashes with the installed tools: the orphan version holds the
// repaired PDFs when the process dies, and recovery removes all of it.
func TestDocuments_RealRepairCrashLeavesNoHalfVersion(t *testing.T) {
	if os.Getenv(envRealTools) != "1" {
		t.Skipf("%s=1 runs the installed tools (make verify-documents)", envRealTools)
	}
	for _, at := range []string{"real-rounds", "real-publish"} {
		t.Run(at, func(t *testing.T) { checkCrash(t, at) })
	}
}

func checkCrash(t *testing.T, at string) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRepairCrashChild$", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), envCrashRoot+"="+root, envCrashAt+"="+at)
	if strings.HasPrefix(at, "real-") {
		// The child reuses the parent's prepared root: it dies before any
		// cleanup of its own would run, and a Setup of its own is slow.
		realToolchain(t)
		cmd.Env = append(cmd.Env, "APPLYSMITH_TEST_DOCUMENT_ROOT="+string(realCfg.Root))
	}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != crashExit {
		t.Fatalf("the child did not die at the crash point: %v\n%s", err, out)
	}
	ctx := t.Context()

	// What the dead process left, before anything recovers: only the draft when
	// it died between rounds; the new version's files, PDFs included, when it
	// died after they were linked.
	dirs, pdfs := versionDirs(t, root), 0
	for _, f := range filesUnder(t, filepath.Join(root, "documents")) {
		if strings.HasSuffix(f, ".pdf") {
			pdfs++
		}
	}
	if strings.HasSuffix(at, "rounds") && len(dirs) != 1 || strings.HasSuffix(at, "publish") && (len(dirs) != 2 || pdfs == 0) {
		t.Fatalf("version directories = %v, PDFs = %d", dirs, pdfs)
	}

	// Opening the workspace settles the publish journal.
	st, err := store.OpenWorkspace(ctx, root, store.Options{})
	must(t, err)
	defer st.Close()
	var rows, interrupted int
	must(t, st.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`).Scan(&rows))
	must(t, st.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE kind = 'documents' AND status <> 'succeeded' AND failure_reason = 'interrupted'`).Scan(&interrupted))
	if dirs := versionDirs(t, root); len(dirs) != 1 || !strings.HasSuffix(dirs[0], "/1") || rows != 2 || interrupted != 1 {
		t.Errorf("after recovery: directories %v, rows %d, interrupted runs %d; want the draft only and the PDF run interrupted", dirs, rows, interrupted)
	}
	for _, f := range append(filesUnder(t, filepath.Join(root, store.StateDir)), filesUnder(t, filepath.Join(root, "documents"))...) {
		if strings.Contains(f, "publish") && strings.HasSuffix(f, ".json") || strings.HasSuffix(f, ".tmp") {
			t.Errorf("leftover %s", f)
		}
	}

	// The draft is whole.
	tr, err := tracker.New(st, tracker.Options{LinkedTables: []tracker.LinkedTable{{Table: "documents", Column: "candidacy_id"}}})
	must(t, err)
	svc, err := New(Deps{Store: st, Tracker: tr, Workspace: workspace.Open(root)})
	must(t, err)
	must(t, svc.Start(ctx))
	defer svc.Close()
	docs, err := svc.List(ctx, 1)
	must(t, err)
	for _, d := range docs {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.SourcePath)))
		if err != nil || sha(raw) != d.ContentHash || d.Version != 1 || d.PDFState != PDFNone {
			t.Errorf("%+v is not the untouched draft (%v)", d, err)
		}
	}
	if len(docs) != 2 {
		t.Errorf("documents = %d, want 2", len(docs))
	}
}
