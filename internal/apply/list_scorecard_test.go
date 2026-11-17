package apply

import (
	"context"
	"testing"
	"time"
)

func day(d int) time.Time { return time.Date(2027, 3, d, 10, 0, 0, 0, time.UTC) }

func scoreRun(id, url string, started time.Time, minutes int, mod func(*RunFact)) RunFact {
	r := RunFact{ID: id, URL: url, Started: started, Finished: started.Add(time.Duration(minutes) * time.Minute), Kind: OutcomeStopped, Stage: StageFill,
		Class: ClassDefect, Reason: "the walk reached 3 pages of 5 and stopped", ATS: "generic", Build: "build-a"}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestApply_Scorecard_LoadsTheWindowAndTheRunBeforeIt(t *testing.T) {
	st := lsStore(t)
	u := "https://careers.example.test/jobs/a"
	lsInsertRun(t, st, scoreRun("a0", u, day(2), 3, nil))
	lsInsertRun(t, st, scoreRun("a-1", u, day(1), 3, nil))
	lsInsertRun(t, st, scoreRun("a1", u, day(9), 2, func(r *RunFact) { r.Snapshot = "snapshots/a1.html" }))
	lsInsertRun(t, st, scoreRun("a2", u, day(10), 2, func(r *RunFact) {
		r.Kind, r.Stage, r.Class, r.Reason, r.Confirmed = OutcomeSubmitted, StageConfirm, "", "", true
	}))
	stamp := day(9).Add(30 * time.Second).UTC().Format(stampLayout)
	lsExec(t, st, `INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, latency_ms, cost_usd, billing)
		VALUES ('c1', ?, 'apply-check', 'provider-one', 'api', 'm', 1, 'primary', 'ok', 700, 0.01, 'metered')`, stamp)
	sc, err := LoadScorecard(context.Background(), st, 2, day(10), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Window of 2 days: a1 and a2 count; a0 is only the run before the window.
	if sc.Total.Runs != 2 || sc.Total.Submitted != 1 || len(sc.Days) != 2 {
		t.Fatalf("%+v", sc)
	}
	if len(sc.Total.Models) != 1 || sc.Total.Models[0].Calls != 1 || sc.Total.Models[0].CostUSD != 0.01 {
		t.Fatalf("models %+v", sc.Total.Models)
	}
	if sc.Total.RepeatRuns != 1 {
		t.Fatalf("a1 repeats a0, which is before the window: %d repeats", sc.Total.RepeatRuns)
	}
}
