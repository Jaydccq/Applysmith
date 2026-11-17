package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func twoFiles(n int64) ([]PublishFile, error) {
	base := reportName(n)
	return []PublishFile{{Path: base, Content: []byte("# md\n")}, {Path: base[:len(base)-3] + ".html", Content: []byte("<p>html</p>\n")}}, nil
}

func noRows(*sql.Tx, Artifact) error { return nil }

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) != DBFile && !isDBSidecar(p) {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func isDBSidecar(p string) bool {
	ext := filepath.Ext(p)
	return ext == ".db-wal" || ext == ".db-shm" || filepath.Base(filepath.Dir(p)) == "backups"
}

func publishFilesOnly(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, f := range listFiles(t, root) {
		if filepath.Dir(f) == "reports" || filepath.Base(filepath.Dir(f)) == publishDir {
			out = append(out, f)
		}
	}
	return out
}

func TestPublishArtifactTx_CommitsFilesRowsAndCallbackTogether(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	var seen Artifact
	a, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, func(tx *sql.Tx, a Artifact) error {
		seen = a
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES ('probe', 'x')`)
		return err
	})
	if err != nil || a.Number != 1 || a.Status != ArtifactReady || seen.Number != 1 {
		t.Fatalf("artifact %+v err %v", a, err)
	}
	got := publishFilesOnly(t, root)
	if len(got) != 2 {
		t.Fatalf("files = %v (the journal must be gone)", got)
	}
	var v string
	if err := s.Read().QueryRow(`SELECT value FROM workspace_meta WHERE key = 'probe'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
}

func TestPublishArtifactTx_BlockedPathConsumesNoNumber(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "reports"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows); err == nil {
		t.Fatal("the publish must fail")
	}
	if err := os.Remove(filepath.Join(root, "reports")); err != nil {
		t.Fatal(err)
	}
	a, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows)
	if err != nil || a.Number != 1 {
		t.Fatalf("the next report must be 1, got %+v err %v", a, err)
	}
}

func TestPublishArtifactTx_SecondFileFailureLeavesNoFilesAndNoRows(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	html := filepath.Join(root, "reports", "001-example-corp.html")
	if err := os.MkdirAll(filepath.Dir(html), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(html, []byte("someone else's bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if got := publishFilesOnly(t, root); len(got) != 1 || got[0] != filepath.Join("reports", "001-example-corp.html") {
		t.Fatalf("only the pre-existing file may remain, got %v", got)
	}
	if b, _ := os.ReadFile(html); string(b) != "someone else's bytes" {
		t.Fatal("bytes that were not ours must be left alone")
	}
	if n := count(t, s.Read(), `SELECT count(*) FROM report_artifacts`); n != 0 {
		t.Fatalf("%d artifact rows", n)
	}
	_ = os.Remove(html)
	if a, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows); err != nil || a.Number != 1 {
		t.Fatalf("next = %+v %v", a, err)
	}
}

func TestPublishArtifactTx_CommitCallbackFailureRollsBackEverything(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	boom := errors.New("index insert failed")
	_, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, func(tx *sql.Tx, a Artifact) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES ('probe', 'x')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := publishFilesOnly(t, root); len(got) != 0 {
		t.Fatalf("files left: %v", got)
	}
	if n := count(t, s.Read(), `SELECT count(*) FROM workspace_meta WHERE key = 'probe'`); n != 0 {
		t.Fatal("the callback's rows must roll back")
	}
	a, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows)
	if err != nil || a.Number != 1 {
		t.Fatalf("next = %+v %v", a, err)
	}
}

func TestPublishArtifactTx_PlanErrorAndUnsafePath(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	if _, err := s.PublishArtifactTx(ctx, root, "report", func(int64) ([]PublishFile, error) { return nil, errors.New("render failed") }, noRows); err == nil {
		t.Fatal("a plan error must fail the publish")
	}
	_, err := s.PublishArtifactTx(ctx, root, "report", func(int64) ([]PublishFile, error) {
		return []PublishFile{{Path: "../escape.md", Content: []byte("x")}}, nil
	}, noRows)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v", err)
	}
	if a, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows); err != nil || a.Number != 1 {
		t.Fatalf("next = %+v %v", a, err)
	}
}

// A crash after the files were linked and before the commit leaves the files
// and a journal but no row. Recovery removes exactly those files.
func TestPublishArtifactTx_CrashBeforeCommitIsRecovered(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	files, _ := twoFiles(1)
	var entries []journalFile
	for _, f := range files {
		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := writeAndRename(abs, f.Content); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, journalFile{f.Path, sha256Hex(f.Content)})
	}
	if _, err := writeJournal(root, 1, entries); err != nil {
		t.Fatal(err)
	}
	// Bytes that are not the journal's stay untouched.
	other := filepath.Join(root, "reports", "002-other.md")
	if err := os.WriteFile(other, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverArtifacts(ctx, root); err != nil {
		t.Fatal(err)
	}
	if got := publishFilesOnly(t, root); len(got) != 1 || got[0] != filepath.Join("reports", "002-other.md") {
		t.Fatalf("after recovery: %v", got)
	}
	if a, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows); err != nil || a.Number != 1 {
		t.Fatalf("the name is free again: %+v %v", a, err)
	}
}

func TestPublishArtifactTx_CrashAfterCommitKeepsTheFiles(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	a, err := s.PublishArtifactTx(ctx, root, "report", twoFiles, noRows)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := twoFiles(a.Number)
	var entries []journalFile
	for _, f := range files {
		entries = append(entries, journalFile{f.Path, sha256Hex(f.Content)})
	}
	if _, err := writeJournal(root, a.Number, entries); err != nil { // the process died before removing it
		t.Fatal(err)
	}
	if err := s.RecoverArtifacts(ctx, root); err != nil {
		t.Fatal(err)
	}
	if got := publishFilesOnly(t, root); len(got) != 2 {
		t.Fatalf("a committed publish keeps its files and loses its journal: %v", got)
	}
}
