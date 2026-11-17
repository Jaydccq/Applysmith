package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func reportName(n int64) string { return fmt.Sprintf("reports/%03d-example-corp.md", n) }

// publishOne publishes a single file as a report artifact.
func publishOne(ctx context.Context, s *Store, root string, name func(number int64) string, content []byte) (Artifact, error) {
	return s.PublishArtifactTx(ctx, root, "report", func(n int64) ([]PublishFile, error) {
		return []PublishFile{{Path: name(n), Content: content}}, nil
	}, noRows)
}

func TestPublishArtifactCommitsFileAndRow(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	a, err := publishOne(ctx, s, root, reportName, []byte("# Example\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Number != 1 || a.Status != ArtifactReady || a.ContentHash != sha256Hex([]byte("# Example\n")) {
		t.Fatalf("artifact = %+v", a)
	}
	b, err := os.ReadFile(filepath.Join(root, "reports", "001-example-corp.md"))
	if err != nil || string(b) != "# Example\n" {
		t.Fatalf("file: %q %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "reports", "001-example-corp.md")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(tempSibling(filepath.Join(root, "reports", "001-example-corp.md"))); !os.IsNotExist(err) {
		t.Error("temp sibling left behind")
	}
	if n := count(t, s.ro, `SELECT count(*) FROM report_artifacts WHERE status = 'ready'`); n != 1 {
		t.Errorf("ready artifacts = %d", n)
	}
}

func TestReportNumbersAreUniqueUnderConcurrency(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	ctx := context.Background()
	const n = 12
	var wg sync.WaitGroup
	numbers := make(chan int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := publishOne(ctx, s, root, reportName, []byte(fmt.Sprint("body ", i)))
			if err != nil {
				t.Error(err)
				return
			}
			numbers <- a.Number
		}()
	}
	wg.Wait()
	close(numbers)
	seen := map[int64]bool{}
	for k := range numbers {
		if seen[k] {
			t.Fatalf("number %d allocated twice", k)
		}
		seen[k] = true
	}
	if len(seen) != n {
		t.Fatalf("allocated %d numbers", len(seen))
	}
}

func TestPublishNeverOverwritesExistingBytes(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	abs := filepath.Join(root, "reports", "001-example-corp.md")
	_ = os.MkdirAll(filepath.Dir(abs), 0o700)
	_ = os.WriteFile(abs, []byte("user file"), 0o600)
	if _, err := publishOne(context.Background(), s, root, reportName, []byte("new")); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if b, _ := os.ReadFile(abs); string(b) != "user file" {
		t.Fatalf("bytes changed: %q", b)
	}
	if n := count(t, s.ro, `SELECT count(*) FROM report_artifacts`); n != 0 {
		t.Fatalf("%d artifact rows after a refused publish", n)
	}
}

func TestPublishRejectsEscapingPath(t *testing.T) {
	s := openT(t, Options{})
	_, err := publishOne(context.Background(), s, t.TempDir(), func(int64) string { return "../outside.md" }, []byte("x"))
	if err == nil {
		t.Fatal("escaping path accepted")
	}
	if n := count(t, s.ro, `SELECT count(*) FROM report_artifacts`); n != 0 {
		t.Fatal("row written for rejected path")
	}
}

func TestTargetAppearingBeforePublishIsAConflictAndPreserved(t *testing.T) {
	s := openT(t, Options{})
	root := t.TempDir()
	abs := filepath.Join(root, "reports", "001-example-corp.md")
	publishGap = func(target string) {
		if filepath.Ext(target) == ".md" {
			_ = os.WriteFile(target, []byte("user file"), 0o600)
		}
	}
	defer func() { publishGap = nil }()

	_, err := publishOne(context.Background(), s, root, reportName, []byte("generated"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if b, _ := os.ReadFile(abs); string(b) != "user file" {
		t.Fatalf("bytes overwritten: %q", b)
	}
	if _, err := os.Stat(tempSibling(abs)); !os.IsNotExist(err) {
		t.Error("temp sibling left behind")
	}
}
