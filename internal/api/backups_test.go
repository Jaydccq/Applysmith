package api

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

func (h *harness) postBackup(out string) result {
	return h.do(call{method: "POST", path: "/api/backups", useToken: true, contentType: "application/json", body: jsonBody(map[string]string{"out": out})})
}

// pollBackup follows the Location of a 202 until the operation leaves running.
func (h *harness) pollBackup(loc string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	pause := newPollPause()
	for time.Now().Before(deadline) {
		r := h.do(call{path: loc, useToken: true})
		if r.StatusCode != 200 {
			h.t.Fatalf("poll %s: %d %s", loc, r.StatusCode, r.body)
		}
		if d := r.data(); d["state"] != "running" {
			return d
		}
		pause()
	}
	h.t.Fatal("the backup stayed running")
	return nil
}

func TestLiveBackupThroughTheOwningServer(t *testing.T) {
	h := newHarness(t)
	h.startApply("key-0000-0001", postingA)
	out := filepath.Join(t.TempDir(), "backup")
	r := h.postBackup(out)
	id, _ := r.data()["id"].(string)
	if r.StatusCode != 202 || id == "" || r.Header.Get("Location") != "/api/backups/"+id {
		t.Fatalf("%d %v %s", r.StatusCode, r.Header, r.body)
	}
	done := h.pollBackup(r.Header.Get("Location"))
	res, _ := done["result"].(map[string]any)
	if done["state"] != "succeeded" || res["out"] != out || res["workspaceId"] == "" {
		t.Fatalf("%v", done)
	}
	m, err := store.VerifyBackup(context.Background(), out)
	if err != nil || m.SchemaVersion != store.SchemaVersion() {
		t.Fatalf("verify: %v %+v", err, m)
	}
	if r := h.do(call{method: "POST", path: "/api/backups", contentType: "application/json", body: []byte(`{}`)}); r.StatusCode != 401 {
		t.Errorf("anonymous backup: %d", r.StatusCode)
	}
}

func TestLiveBackupRejectsBadOutputUpFront(t *testing.T) {
	h := newHarness(t)
	if r := h.postBackup("relative/dir"); r.StatusCode != 422 {
		t.Errorf("relative output: %d %s", r.StatusCode, r.body)
	}
	// The path rules of the store are reported by the operation.
	inside := h.postBackup(filepath.Join(h.ws.Root, "inside"))
	done := h.pollBackup(inside.Header.Get("Location"))
	e, _ := done["error"].(map[string]any)
	if done["state"] != "failed" || e["code"] != "validation_failed" {
		t.Errorf("output inside the workspace: %v", done)
	}
	out := filepath.Join(t.TempDir(), "backup")
	h.pollBackup(h.postBackup(out).Header.Get("Location"))
	again := h.pollBackup(h.postBackup(out).Header.Get("Location"))
	if e, _ := again["error"].(map[string]any); again["state"] != "failed" || e["code"] != "conflict" {
		t.Errorf("existing output: %v", again)
	} else if _, has := e["details"]; has {
		t.Errorf("an error without details sends the key, unlike every other error body: %v", e)
	}
}

// A backup that waits for the writer must not hold the request open: the
// route answers at once and the caller polls.
func TestLiveBackupDoesNotHoldTheRequest(t *testing.T) {
	h := newHarness(t)
	hold, release := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- h.srv.store.Load().Write(context.Background(), func(*sql.Tx) error {
			close(hold)
			<-release
			return nil
		})
	}()
	<-hold
	out := filepath.Join(t.TempDir(), "backup")
	start := time.Now()
	r := h.postBackup(out)
	if r.StatusCode != 202 || time.Since(start) > 2*time.Second {
		t.Fatalf("%d after %s: %s", r.StatusCode, time.Since(start), r.body)
	}
	loc := r.Header.Get("Location")
	if d := h.do(call{path: loc, useToken: true}).data(); d["state"] != "running" {
		t.Errorf("while the writer is held: %v", d)
	}
	if second := h.postBackup(filepath.Join(t.TempDir(), "other")); second.StatusCode != 409 || second.errCode() != "conflict" {
		t.Errorf("a second backup while one runs: %d %s", second.StatusCode, second.body)
	}
	close(release)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if d := h.pollBackup(loc); d["state"] != "succeeded" {
		t.Errorf("%v", d)
	}
}

func TestUnknownBackupIsNotFound(t *testing.T) {
	h := newHarness(t)
	if r := h.do(call{path: "/api/backups/bak_missing", useToken: true}); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{path: "/api/backups/bak_missing"}); r.StatusCode != 401 {
		t.Fatalf("anonymous poll: %d", r.StatusCode)
	}
}

// Shutdown waits for a backup as long as it takes: closing the database under
// a running copy would corrupt it.
func TestShutdownDoesNotCloseTheDatabaseUnderARunningBackup(t *testing.T) {
	h := newHarness(t)
	h.srv.shutdownWait = 20 * time.Millisecond
	release, running := make(chan struct{}), make(chan struct{})
	if _, e := h.srv.backups.start(func() (*wire.BackupResult, *apikit.Error) {
		close(running)
		<-release
		return nil, apikit.NewErr(wire.CodeInternal, "stopped")
	}); e != nil {
		t.Fatal(e)
	}
	<-running
	done := make(chan error, 1)
	go func() { done <- h.srv.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("shutdown finished under a running backup: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if h.srv.store.Load() == nil {
		t.Error("the database was closed while the backup ran")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
