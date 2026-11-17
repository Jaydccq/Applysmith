package ledger

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/tracker"
)

const correctURL = "https://jobs.example.test/apply/42"

// submitted records a confirmed (or unconfirmed) apply-chain submission of the
// posting by run and returns its ledger id.
func submitted(t *testing.T, l *Ledger, url, company, run string, confirmed bool) int64 {
	t.Helper()
	r := ClaimRequest{URL: url, Host: "jobs.example.test", Company: company, Role: "Platform Engineer", Source: SourceApplyChain, RunID: run}
	cl, err := l.Claim(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Finish(context.Background(), cl.ID, Verdict{Landed: true, PressDispatched: true, Confirmed: confirmed}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := l.s.Read().QueryRow(`SELECT id FROM submissions WHERE claim_id = ?`, cl.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func correctionReq(url, msg, run string) ClaimRequest {
	return ClaimRequest{URL: url, Host: "jobs.example.test", Source: SourceApplyChain, RunID: run,
		Correction: &CorrectionRequest{Caller: gates.CallerAskAgent, SinglePosting: true, Message: msg}}
}

func refusedReason(t *testing.T, err error) string {
	t.Helper()
	d, ok := DecisionOf(err)
	if !ok {
		t.Fatalf("want a refusal, got %v", err)
	}
	return d.Reason
}

// Scenario: Correction of a confirmed submission.
func TestCorrection_OfAConfirmedSubmission(t *testing.T) {
	ctx := context.Background()
	l, set, db := clocked(t)
	first := submitted(t, l, correctURL, "Example Corp", "run-1", true)
	set(t0.Add(time.Hour))
	msg := "correct the answers for Example Corp " + correctURL

	j, err := l.Guard(ctx, correctionReq(correctURL, msg, "run-2"))
	if err != nil || j.Supersedes != first {
		t.Fatalf("guard %+v %v", j, err)
	}
	if len(j.Decisions) != 2 || j.Decisions[0].Gate != gates.GateCorrection || j.Last().Code != gates.CodeCorrectionLifted {
		t.Fatalf("decisions %+v", j.Decisions)
	}
	cl, err := l.Claim(ctx, correctionReq(correctURL, msg, "run-2"))
	if err != nil || cl.Supersedes != first {
		t.Fatalf("claim %+v %v", cl, err)
	}
	if _, err := l.Finish(ctx, cl.ID, Verdict{Landed: true, PressDispatched: true, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE claim_id = ? AND supersedes = ? AND correction = 1 AND state = 'submitted'`, cl.ID, first); n != 1 {
		t.Fatalf("correction rows %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE correction = 0`); n != 1 {
		t.Fatalf("the correction was counted as another application: %d ordinary rows", n)
	}

	set(t0.Add(2 * time.Hour))
	_, err = l.Claim(ctx, correctionReq(correctURL, msg, "run-3"))
	if blockedCode(t, err) != gates.CodeCorrectionOnce || !strings.Contains(refusedReason(t, err), "a posting is corrected once") {
		t.Fatalf("second correction: %v", err)
	}
}

func TestCorrection_RefusalsKeepTheEarlierSubmissionStanding(t *testing.T) {
	ctx := context.Background()
	msg := "correct the answers for Example Corp " + correctURL
	t.Run("no ledger row", func(t *testing.T) {
		l, _, db := clocked(t)
		_, err := l.Claim(ctx, correctionReq(correctURL, msg, "run-2"))
		if blockedCode(t, err) != gates.CodeCorrectionNoRow || count(t, db, `SELECT count(*) FROM submissions`) != 0 {
			t.Fatal(err)
		}
	})
	t.Run("a batch or another caller", func(t *testing.T) {
		l, set, _ := clocked(t)
		submitted(t, l, correctURL, "Example Corp", "run-1", true)
		set(t0.Add(time.Hour))
		for _, c := range []CorrectionRequest{
			{Caller: gates.CallerAskAgent, SinglePosting: false, Message: msg},
			{Caller: gates.CallerCLI, SinglePosting: true, Message: msg},
			{Caller: gates.CallerDashboard, SinglePosting: true, Message: msg},
			{Caller: gates.CallerScheduler, SinglePosting: true, Message: msg},
		} {
			r := correctionReq(correctURL, msg, "run-2")
			r.Correction = &c
			if _, err := l.Claim(ctx, r); blockedCode(t, err) != gates.CodeCorrectionSingleOnly {
				t.Fatalf("%+v: %v", c, err)
			}
		}
	})
	t.Run("a live submitting row is not overridden", func(t *testing.T) {
		l, set, _ := clocked(t)
		submitted(t, l, correctURL, "Example Corp", "run-1", true)
		set(t0.Add(time.Hour))
		insertLive(t, l, "run-x")
		_, err := l.Claim(ctx, correctionReq(correctURL, msg, "run-2"))
		if blockedCode(t, err) != gates.CodeLiveSubmitting {
			t.Fatal(err)
		}
	})
	t.Run("an unreadable ledger", func(t *testing.T) {
		l, set, _ := clocked(t)
		submitted(t, l, correctURL, "Example Corp", "run-1", true)
		set(t0.Add(time.Hour))
		err := l.s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE submissions RENAME TO gone`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = l.Guard(ctx, correctionReq(correctURL, msg, "run-2"))
		if blockedCode(t, err) != gates.CodeCorrectionLedgerUnreadable {
			t.Fatal(err)
		}
	})
	t.Run("an ordinary run is still refused by the confirmed row", func(t *testing.T) {
		l, set, _ := clocked(t)
		submitted(t, l, correctURL, "Example Corp", "run-1", true)
		set(t0.Add(time.Hour))
		r := correctionReq(correctURL, msg, "run-2")
		r.Correction, r.Resubmit = nil, true
		if _, err := l.Claim(ctx, r); blockedCode(t, err) != gates.CodeAlreadySubmitted {
			t.Fatal(err)
		}
	})
}

// insertLive writes another run's live submitting row for the posting, as a
// claim taken by a process the ledger cannot see.
func insertLive(t *testing.T, l *Ledger, run string) {
	t.Helper()
	at := l.now().UTC().Format(stampLayout)
	err := l.s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO submissions (identity_key, url, host, company, state, confirmed, source, observed_at, created_at, updated_at, claim_id, claimed_at, run_id)
			VALUES (?, ?, 'jobs.example.test', 'Example Corp', 'submitting', 0, 'apply-chain', ?, ?, ?, 'claim_live', ?, ?)`, claimKey(correctURL), correctURL, at, at, at, at, run)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCorrection_LiftsTheAppliedTrackerRowAndUnconfirmedRows(t *testing.T) {
	ctx := context.Background()
	l, set, _ := clocked(t)
	tr := newTracker(t, l)
	e, err := tr.Add(ctx, tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: correctURL})
	if err != nil {
		t.Fatal(err)
	}
	first := submitted(t, l, correctURL, "Example Corp", "run-1", false)
	if _, err := tr.SetStatus(ctx, tracker.StatusChange{ID: e.ID, To: tracker.Applied, Actor: tracker.ActorUser, On: "2027-03-10"}); err != nil {
		t.Fatal(err)
	}
	set(t0.Add(time.Hour))
	cl, err := l.Claim(ctx, correctionReq(correctURL, "Please correct my application for Example Corp "+correctURL, "run-2"))
	if err != nil || cl.Supersedes != first {
		t.Fatalf("%+v %v", cl, err)
	}
}

func TestCorrection_AFailedAttemptDoesNotUseTheCorrectionUp(t *testing.T) {
	ctx := context.Background()
	l, set, _ := clocked(t)
	first := submitted(t, l, correctURL, "Example Corp", "run-1", true)
	msg := "correct the answers for Example Corp " + correctURL
	set(t0.Add(time.Hour))
	cl, err := l.Claim(ctx, correctionReq(correctURL, msg, "run-2"))
	if err != nil {
		t.Fatal(err)
	}
	if st, err := l.Finish(ctx, cl.ID, Verdict{}); err != nil || st != gates.StateFailed {
		t.Fatalf("%s %v", st, err)
	}
	set(t0.Add(2 * time.Hour))
	again, err := l.Claim(ctx, correctionReq(correctURL, msg, "run-3"))
	if err != nil || again.Supersedes != first {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestCorrection_SchemaEnforcesTheLinkAndOnce(t *testing.T) {
	l, _, _ := clocked(t)
	first := submitted(t, l, correctURL, "Example Corp", "run-1", true)
	insert := func(supersedes any, correction int, state string) error {
		at := l.now().UTC().Format(stampLayout)
		return l.s.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO submissions (identity_key, url, host, state, confirmed, source, observed_at, created_at, updated_at, supersedes, correction)
				VALUES ('k', ?, 'jobs.example.test', ?, 1, 'apply-chain', ?, ?, ?, ?, ?)`, correctURL, state, at, at, at, supersedes, correction)
			return err
		})
	}
	if insert(nil, 1, "submitted") == nil {
		t.Error("a correction row without supersedes was written")
	}
	if insert(first, 0, "submitted") == nil {
		t.Error("a superseding row not marked as a correction was written")
	}
	if err := insert(first, 1, "failed"); err != nil {
		t.Fatal(err)
	}
	if err := insert(first, 1, "submitted"); err != nil {
		t.Fatal(err)
	}
	if insert(first, 1, "may-have-submitted") == nil {
		t.Error("a second correction of the same row was written")
	}
}

func TestCorrection_OfferOnlyWhenTheDoorAccepts(t *testing.T) {
	ctx := context.Background()
	l, set, _ := clocked(t)
	if _, ok, err := l.CorrectionOffer(ctx, correctURL); err != nil || ok {
		t.Fatalf("offered with no ledger row: %v", err)
	}
	first := submitted(t, l, correctURL, "Example Corp", "run-1", true)
	msg, ok, err := l.CorrectionOffer(ctx, correctURL)
	if err != nil || !ok || msg != gates.CorrectionMessage("Example Corp", correctURL) {
		t.Fatalf("%q %v %v", msg, ok, err)
	}
	set(t0.Add(time.Hour))
	cl, err := l.Claim(ctx, correctionReq(correctURL, msg, "run-2"))
	if err != nil || cl.Supersedes != first {
		t.Fatalf("the offered message did not open the door: %v", err)
	}
	if _, ok, _ := l.CorrectionOffer(ctx, correctURL); ok {
		t.Fatal("offered a second correction")
	}
	other := "https://jobs.example.test/apply/" + strconv.Itoa(7)
	submitted(t, l, other, "", "run-9", true)
	if _, ok, _ := l.CorrectionOffer(ctx, other); ok {
		t.Fatal("offered with no company to name")
	}
}
