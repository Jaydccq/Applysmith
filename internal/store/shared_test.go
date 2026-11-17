package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenShared_DoesNotInterruptRunsOfALiveProcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), StateDir, DBFile)
	live, err := Open(ctx, path, Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	ref, err := live.CreateRun(ctx, RunRequest{Principal: "cli", Operation: "apply", Key: "k", Kind: "apply", Input: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := Open(ctx, path, Options{WorkspaceID: testWorkspaceID, Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	var status string
	if err := live.Read().QueryRow(`SELECT status FROM runs WHERE id = ?`, ref.RunID).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("run status = %q, %v; a shared open must not reconcile another process's runs", status, err)
	}
	if err := shared.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO workspace_meta (key, value) VALUES ('probe', 'v')`)
		return err
	}); err != nil {
		t.Errorf("shared write: %v", err)
	}
}

func TestOpenShared_RefusesToMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateDir, DBFile)
	_, err := Open(context.Background(), path, Options{WorkspaceID: testWorkspaceID, Shared: true})
	if !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("a refused shared open created the database")
	}
}

func TestOpenShared_ConcurrentWritersSerialize(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), StateDir, DBFile)
	a, err := Open(ctx, path, Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path, Options{WorkspaceID: testWorkspaceID, Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	errs := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		go func() {
			var err error
			for n := range 25 {
				key := string(rune('a'+i)) + string(rune('a'+n))
				if err = s.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO workspace_meta (key, value) VALUES (?, 'v')`, key)
					return err
				}); err != nil {
					break
				}
			}
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// A publish in flight in another process holds the write lock with its
// journal on disk; recovery must wait for its outcome instead of treating the
// uncommitted files as orphans.
func TestRecoverArtifacts_WaitsForAPublishInFlightElsewhere(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, StateDir, DBFile)
	server, err := Open(ctx, path, Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	cli, err := Open(ctx, path, Options{WorkspaceID: testWorkspaceID, Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	inFlight, release := make(chan struct{}), make(chan struct{})
	published := make(chan error, 1)
	go func() {
		_, err := server.PublishArtifactTx(ctx, root, "report", twoFiles, func(*sql.Tx, Artifact) error {
			close(inFlight)
			<-release
			return nil
		})
		published <- err
	}()
	<-inFlight
	recovered := make(chan error, 1)
	go func() { recovered <- cli.RecoverArtifacts(ctx, root) }()
	time.Sleep(200 * time.Millisecond) // time for recovery to reach the lock
	select {
	case err := <-recovered:
		t.Fatalf("recovery returned while a publish was in flight: %v", err)
	default:
	}
	close(release)
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if err := <-recovered; err != nil {
		t.Fatal(err)
	}
	if files := publishFilesOnly(t, root); len(files) != 2 {
		t.Errorf("recovery removed the files of a publish that committed: %v", files)
	}
}
