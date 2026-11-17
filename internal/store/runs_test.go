package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestIdempotentRunCreation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), DBFile)
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	req := RunRequest{Principal: "dashboard", Operation: "apply.start", Key: "key-1", Kind: "apply", Input: []byte(`{"url":"https://example.com/jobs/1"}`)}

	first, err := s.CreateRun(ctx, req)
	if err != nil || first.Replayed {
		t.Fatalf("first = %+v, %v", first, err)
	}
	again, err := s.CreateRun(ctx, req)
	if err != nil || !again.Replayed || again.RunID != first.RunID {
		t.Fatalf("replay = %+v, %v; want run %s", again, err, first.RunID)
	}

	changed := req
	changed.Input = []byte(`{"url":"https://example.com/jobs/2"}`)
	if _, err := s.CreateRun(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("different input under same key: %v", err)
	}

	otherPrincipal := req
	otherPrincipal.Principal = "cli"
	other, err := s.CreateRun(ctx, otherPrincipal)
	if err != nil || other.RunID == first.RunID {
		t.Fatalf("key is scoped to the principal: %+v, %v", other, err)
	}

	// Lost response plus restart: the original run id comes back.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	afterRestart, err := s.CreateRun(ctx, req)
	if err != nil || !afterRestart.Replayed || afterRestart.RunID != first.RunID {
		t.Fatalf("after restart = %+v, %v", afterRestart, err)
	}
	var runs int
	_ = s.Read().QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
}

func TestScheduledOccurrenceIsUnique(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	if _, err := s.rw.Exec(`INSERT INTO schedules (id, name, job_kind, dtstart, created_at, updated_at) VALUES (1, 'daily', 'scan', 'x', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	ins := func(id string) error {
		_, err := s.rw.ExecContext(ctx, `INSERT INTO runs (id, kind, status, schedule_id, scheduled_for, created_at, updated_at) VALUES (?, 'scan', 'queued', 1, '2026-01-01T07:30:00Z', 'x', 'x')`, id)
		return err
	}
	if err := ins("a"); err != nil {
		t.Fatal(err)
	}
	if err := ins("b"); err == nil {
		t.Fatal("same occurrence inserted twice")
	}
}

func TestGetRunAndSetRun(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), DBFile), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.GetRun(ctx, "run_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}
	ref, err := s.CreateRun(ctx, RunRequest{Principal: "p", Operation: "scan", Key: "key-1", Kind: "scan", Input: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun(ctx, ref.RunID)
	if err != nil || run.Kind != "scan" || run.Status != "queued" || run.FinishedAt != "" {
		t.Fatalf("new run = %+v, %v", run, err)
	}
	if err := s.SetRun(ctx, ref.RunID, "running", "sweep", ""); err != nil {
		t.Fatal(err)
	}
	if run, _ = s.GetRun(ctx, ref.RunID); run.Status != "running" || run.Stage != "sweep" || run.FinishedAt != "" {
		t.Fatalf("running run = %+v", run)
	}
	if err := s.SetRun(ctx, ref.RunID, "failed", "", "boom"); err != nil {
		t.Fatal(err)
	}
	if run, _ = s.GetRun(ctx, ref.RunID); run.Status != "failed" || run.FailureReason != "boom" || run.FinishedAt == "" {
		t.Fatalf("failed run = %+v", run)
	}
	// An ended run stays as it ended.
	if err := s.SetRun(ctx, ref.RunID, "succeeded", "", ""); err != nil {
		t.Fatal(err)
	}
	if run, _ = s.GetRun(ctx, ref.RunID); run.Status != "failed" {
		t.Fatalf("an ended run was changed: %+v", run)
	}
}

func TestCreateRunCanAttachAKeyToAnExistingRun(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), DBFile), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	first, err := s.CreateRun(ctx, RunRequest{Principal: "p", Operation: "evaluate", Key: "key-1", Kind: "evaluate", Input: []byte(`{"a":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	joined := RunRequest{Principal: "p", Operation: "evaluate", Key: "key-2", Kind: "evaluate", Input: []byte(`{"a":2}`), Attach: first.RunID}
	ref, err := s.CreateRun(ctx, joined)
	if err != nil || ref.RunID != first.RunID || ref.Replayed {
		t.Fatalf("attached = %+v, %v", ref, err)
	}
	var runs int
	if err := s.Read().QueryRow(`SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("%d runs, %v; attaching must not create one", runs, err)
	}
	again, err := s.CreateRun(ctx, joined)
	if err != nil || !again.Replayed || again.RunID != first.RunID {
		t.Fatalf("replay of an attached key = %+v, %v", again, err)
	}
	joined.Input = []byte(`{"a":3}`)
	if _, err := s.CreateRun(ctx, joined); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed input under an attached key: %v", err)
	}
}

func TestCreateRunAdmitRunsOnlyForANewClaim(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), DBFile), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	refuse := errors.New("not now")
	calls := 0
	req := RunRequest{Principal: "p", Operation: "scan", Key: "key-1", Kind: "scan", Input: []byte(`{}`),
		Admit: func(context.Context) error { calls++; return refuse }}
	if _, err := s.CreateRun(ctx, req); !errors.Is(err, refuse) || calls != 1 {
		t.Fatalf("refused claim: %v after %d calls", err, calls)
	}
	var runs int
	if err := s.Read().QueryRow(`SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("%d runs after a refusal, %v", runs, err)
	}
	req.Admit = func(context.Context) error { calls++; return nil }
	first, err := s.CreateRun(ctx, req)
	if err != nil || calls != 2 {
		t.Fatalf("admitted claim: %v after %d calls", err, calls)
	}
	req.Admit = func(context.Context) error { calls++; return refuse }
	again, err := s.CreateRun(ctx, req)
	if err != nil || !again.Replayed || again.RunID != first.RunID || calls != 2 {
		t.Fatalf("replay = %+v, %v after %d calls; a replay must not be asked again", again, err, calls)
	}
}

func TestCreateRun_CreatedHookCommitsWithTheClaim(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	var seen string
	req := RunRequest{Principal: "dashboard", Operation: "evaluate", Key: "key-hook-1", Kind: "evaluate", Input: []byte(`{}`),
		Created: func(tx *sql.Tx, runID string) error {
			seen = runID
			_, err := tx.Exec(`INSERT INTO schedules (id, name, job_kind, enabled, dtstart, created_at, updated_at) VALUES (77, 'hook', 'scan', 1, 'x', 'x', 'x')`)
			return err
		}}
	ref, err := s.CreateRun(ctx, req)
	if err != nil || seen != ref.RunID {
		t.Fatalf("ref %+v hook saw %q err %v", ref, seen, err)
	}
	if _, err := s.CreateRun(ctx, req); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.ro.QueryRow(`SELECT count(*) FROM schedules WHERE id = 77`).Scan(&n)
	if n != 1 {
		t.Fatalf("a replay must not run the hook again: %d rows", n)
	}
}

func TestCreateRun_CreatedHookFailureLeavesNoClaimAndNoRun(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	boom := errors.New("injected")
	req := RunRequest{Principal: "dashboard", Operation: "evaluate", Key: "key-hook-2", Kind: "evaluate", Input: []byte(`{}`),
		Created: func(*sql.Tx, string) error { return boom }}
	if _, err := s.CreateRun(ctx, req); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	var runs, keys int
	_ = s.ro.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
	_ = s.ro.QueryRow(`SELECT count(*) FROM idempotency_keys`).Scan(&keys)
	if runs != 0 || keys != 0 {
		t.Fatalf("%d runs and %d claims survive a failed hook; a retry would replay a run that never executes", runs, keys)
	}
	req.Created = nil
	if ref, err := s.CreateRun(ctx, req); err != nil || ref.Replayed {
		t.Fatalf("retry = %+v, %v", ref, err)
	}
}

func TestReconcileStartup_InterruptedRunsGetAFinishTime(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	for _, kind := range []string{"evaluate", "apply"} {
		if _, err := s.CreateRun(ctx, RunRequest{Principal: "p", Operation: kind, Key: "key-" + kind, Kind: kind, Input: []byte(kind)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReconcileStartup(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ro.Query(`SELECT kind, status, finished_at FROM runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, status string
		var finished sql.NullString
		if err := rows.Scan(&kind, &status, &finished); err != nil {
			t.Fatal(err)
		}
		if !finished.Valid || finished.String == "" {
			t.Errorf("%s run %s has no finished_at", kind, status)
		}
	}
}
