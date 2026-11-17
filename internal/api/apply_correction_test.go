package api

import (
	"database/sql"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/gates"
)

// The runs board offers "Correct this application" on a submitted run's
// record with the exact message the correction door accepts, and only then.
func TestRuns_Board_CorrectionActionOnlyWhenGuardAccepts(t *testing.T) {
	h := newDomainHarness(t)
	const posting = "https://jobs.example.test/apply/42"
	key, stp := apply.IdentityKey(posting)
	if stp != nil {
		t.Fatal(stp)
	}
	const stamp = "2026-01-02T03:04:05.000Z"
	exec := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
			res, err := tx.Exec(q, args...)
			if err == nil {
				id, err = res.LastInsertId()
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	run := func(id, kind string) {
		exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, outcome_kind, submit) VALUES (?, ?, ?, 'ask-agent', ?, ?, ?, 1)`,
			id, stamp, stamp, posting, key, kind)
	}
	row := func(company string, supersedes any) int64 {
		correction := 0
		if supersedes != nil {
			correction = 1
		}
		return exec(`INSERT INTO submissions (identity_key, url, host, company, state, confirmed, source, observed_at, created_at, updated_at, supersedes, correction)
			VALUES (?, ?, 'jobs.example.test', ?, 'submitted', 1, 'apply-chain', ?, ?, ?, ?, ?)`, key, posting, company, stamp, stamp, stamp, supersedes, correction)
	}
	offer := func(id string) *wire.ApplyCorrection {
		t.Helper()
		return decode[wire.ApplyRunView](t, h.get("/api/apply/runs/"+id)).Correction
	}
	run("run_submitted", string(apply.OutcomeSubmitted))
	run("run_stopped", string(apply.OutcomeStopped))

	if c := offer("run_submitted"); c != nil {
		t.Fatalf("offered with no ledger row: %+v", c)
	}
	first := row("", nil)
	if c := offer("run_submitted"); c != nil {
		t.Fatalf("offered with no company to name: %+v", c)
	}
	exec(`UPDATE submissions SET company = 'Example Corp' WHERE id = ?`, first)
	c := offer("run_submitted")
	if c == nil || c.Message != gates.CorrectionMessage("Example Corp", posting) {
		t.Fatalf("offer %+v", c)
	}
	if c := offer("run_stopped"); c != nil {
		t.Fatalf("offered on a run that did not submit: %+v", c)
	}
	row("Example Corp", first)
	if c := offer("run_submitted"); c != nil {
		t.Fatalf("offered a second correction: %+v", c)
	}
}
