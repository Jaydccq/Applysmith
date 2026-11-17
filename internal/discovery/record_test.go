package discovery

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestRun_ConcurrentSameSourceRefused(t *testing.T) {
	e := newEnv(t)
	e.add(ghBoard("example-co"))
	e.stableBoard("example-co")
	held, err := e.rp.ScanStart(e.ctx, "registry", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.sc.Run(e.ctx, ScanRequest{Source: "registry", Portals: eng()})
	var re *RunningError
	if !errors.As(err, &re) || re.RunID != held.ID || !contains(err.Error(), held.ID) {
		t.Fatalf("err = %v", err)
	}
	recs, _ := e.rp.ScanList(e.ctx, "registry", 10)
	if len(recs) != 1 {
		t.Fatalf("a refused start created a record: %d", len(recs))
	}
	// A different source is not blocked.
	if _, err := e.rp.ScanStart(e.ctx, "tracked", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRun_ClosedOnEveryExitPath(t *testing.T) {
	cases := []struct {
		name   string
		cancel func(cancel context.CancelCauseFunc)
		want   string
	}{
		{"interrupted by a signal", func(c context.CancelCauseFunc) { c(context.Canceled) }, RunInterrupted},
		{"cancelled from the dashboard", func(c context.CancelCauseFunc) { c(ErrCancelled) }, RunCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			for _, slug := range []string{"a-co", "b-co", "c-co"} {
				e.stableBoard(slug)
				e.add(ghBoard(slug))
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			e.ctx = ctx
			e.srv.hook = func(path string) { c.cancel(cancel) }
			rec, err := e.sc.Run(ctx, ScanRequest{Source: "registry", Portals: eng()})
			if err != nil {
				t.Fatal(err)
			}
			if rec.Status != c.want || rec.Finished.IsZero() {
				t.Fatalf("status %s finished %v", rec.Status, rec.Finished)
			}
			saved, err := e.rp.ScanGet(context.Background(), rec.ID)
			if err != nil || saved.Status != c.want {
				t.Fatalf("saved = %+v %v", saved, err)
			}
			if err := saved.Counts.Validate(); err != nil {
				t.Fatalf("an interrupted run's counts must still balance: %v", err)
			}
		})
	}
}

// A running row whose document lost its id, or cannot be decoded at all, is
// still closed: it used to stop the server from starting with "sql: no rows".
func TestRun_DamagedOrphanIsClosedNotFatal(t *testing.T) {
	e := newEnv(t)
	noID, _ := e.rp.ScanStart(e.ctx, "registry", nil)
	if err := e.rp.s.Write(e.ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE scan_runs SET record_json = '{"source":"registry"}' WHERE id = ?`, noID.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO scan_runs (id, source, status, started_at, updated_at, record_json)
			VALUES ('run_broken', 'github', 'running', '2026-10-17T00:00:00.000Z', '2026-10-17T00:00:00.000Z', 'not json')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.rp.ScanCloseOrphans(e.ctx); err != nil {
		t.Fatalf("a damaged orphan must not fail the start: %v", err)
	}
	var running int
	if err := e.rp.s.Read().QueryRow(`SELECT count(*) FROM scan_runs WHERE status = 'running'`).Scan(&running); err != nil || running != 0 {
		t.Fatalf("running rows left = %d, %v", running, err)
	}
}
