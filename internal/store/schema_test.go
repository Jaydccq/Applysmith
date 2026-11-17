package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func exec(t *testing.T, s *Store, q string, args ...any) error {
	t.Helper()
	return s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	})
}

func TestEmptyIdentityKeyIsRefused(t *testing.T) {
	s := openT(t, Options{})
	err := exec(t, s, `INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2026-01-01', 'Example Corp', 'Engineer', 'evaluated', '', 'x', 'x')`)
	if err == nil {
		t.Error("tracker row with an empty identity key accepted")
	}
	err = exec(t, s, `INSERT INTO tracker_entries (date, company, role, status, created_at, updated_at) VALUES ('2026-01-01', 'Example Corp', 'Engineer', 'evaluated', 'x', 'x')`)
	if err == nil {
		t.Error("tracker row without an identity key accepted")
	}
}

func TestTrackerStatusMustBeCanonical(t *testing.T) {
	s := openT(t, Options{})
	insert := func(status string) error {
		return exec(t, s, `INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2026-01-01', 'Example Corp', 'Engineer', ?1, 'greenhouse:example/' || ?1, 'x', 'x')`, status)
	}
	for _, ok := range []string{"evaluated", "skipped", "applied", "responded", "interview", "offer", "rejected", "discarded"} {
		if err := insert(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"Evaluated", "maybe", "applied (agent)", "SKIP", ""} {
		if err := insert(bad); err == nil {
			t.Errorf("status %q accepted", bad)
		}
	}
}

func TestDeviceSessionNetworkIsTunnelOnly(t *testing.T) {
	s := openT(t, Options{})
	insert := func(network any) error {
		return exec(t, s, `INSERT INTO device_sessions (id, label, token_sha256, last_network, created_at) VALUES (lower(hex(randomblob(4))), 'phone', lower(hex(randomblob(8))), ?, 'x')`, network)
	}
	if err := insert("tunnel"); err != nil {
		t.Errorf("tunnel: %v", err)
	}
	if err := insert(nil); err != nil {
		t.Errorf("unset: %v", err)
	}
	if err := insert("lan"); err == nil {
		t.Error("a lan session was accepted")
	}
}

func TestLeaseNamesItsOwner(t *testing.T) {
	s := openT(t, Options{})
	if err := exec(t, s, `INSERT INTO resource_leases (name, holder, acquired_at) VALUES ('browser', 'run', 'x')`); err == nil {
		t.Error("a lease without an owner or expiry was accepted")
	}
}

func TestStartupReconcilesInterruptedRunsAndLeases(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx
	path := f.s.Path()
	if err := exec(t, f.s, `INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES ('queued-run', 'apply', 'queued', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil { // the process died with all of this in flight
		t.Fatal(err)
	}
	s, err := Open(ctx, path, Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := s.Read()
	for id, want := range map[string]string{"apply-run": "review_required", "queued-run": "review_required", "sched-run": "failed", "eval-run": "failed", "done-run": "succeeded"} {
		var status string
		if err := q.QueryRow(`SELECT status FROM runs WHERE id = ?`, id).Scan(&status); err != nil || status != want {
			t.Errorf("run %s = %q, %v; want %s", id, status, err, want)
		}
	}
	var reason string
	_ = q.QueryRow(`SELECT failure_reason FROM runs WHERE id = 'apply-run'`).Scan(&reason)
	if reason != "interrupted" {
		t.Errorf("failure_reason = %q", reason)
	}
	_ = q.QueryRow(`SELECT failure_reason FROM runs WHERE id = 'eval-run'`).Scan(&reason)
	if reason != "interrupted" {
		t.Errorf("an evaluation run must end failed with reason interrupted, got %q", reason)
	}
	if n := count(t, q, `SELECT count(*) FROM schedules WHERE active_run_id IS NOT NULL`); n != 0 {
		t.Errorf("a schedule still points at an interrupted run")
	}
	if n := count(t, q, `SELECT count(*) FROM resource_leases`); n != 0 {
		t.Errorf("leases of the dead process survived: %d", n)
	}
}

func TestReconcileKeepsLiveLeasesAndClearsExpiredOnes(t *testing.T) {
	s := openT(t, Options{})
	future := s.now().Add(time.Hour).UTC().Format(timeLayout)
	past := s.now().Add(-time.Hour).UTC().Format(timeLayout)
	for name, expires := range map[string]string{"browser": future, "gmail:read": past} {
		if err := exec(t, s, `INSERT INTO resource_leases (name, holder, owner_pid, owner_instance, acquired_at, expires_at) VALUES (?, 'run', 1, ?, 'x', ?)`, name, s.instance, expires); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReconcileStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s.Read(), `SELECT count(*) FROM resource_leases WHERE name = 'browser'`); n != 1 {
		t.Error("a live lease of this start was cleared")
	}
	if n := count(t, s.Read(), `SELECT count(*) FROM resource_leases WHERE name = 'gmail:read'`); n != 0 {
		t.Error("an expired lease survived")
	}
}

func TestOpenRefusesADatabaseOfAnotherWorkspace(t *testing.T) {
	s := openT(t, Options{})
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path, Options{WorkspaceID: strings.Repeat("a", 32)})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	again, err := Open(context.Background(), path, Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
}

func TestReportNumberIsNeverReused(t *testing.T) {
	s := openT(t, Options{})
	ctx := context.Background()
	root := t.TempDir()
	name := func(n int64) string { return "reports/r" + string(rune('0'+n)) + ".md" }
	a, err := publishOne(ctx, s, root, name, []byte("one"))
	if err != nil || a.Number != 1 {
		t.Fatalf("%+v %v", a, err)
	}
	if err := exec(t, s, `DELETE FROM report_artifacts WHERE report_number = 1`); err != nil {
		t.Fatal(err)
	}
	b, err := publishOne(ctx, s, root, name, []byte("two"))
	if err != nil || b.Number != 2 {
		t.Fatalf("number after delete = %d, %v; want 2", b.Number, err)
	}
}
