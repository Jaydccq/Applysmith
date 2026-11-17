package ledger

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/tracker"
)

const postURL = "https://jobs.example.com/acme/roles/42"

var t0 = time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)

// clocked opens a ledger whose clock the test sets.
func clocked(t *testing.T) (*Ledger, func(time.Time), *sql.DB) {
	t.Helper()
	l, st := open(t)
	cur := t0
	l.now = func() time.Time { return cur }
	return l, func(at time.Time) { cur = at }, st.Read()
}

func req(url string) ClaimRequest {
	return ClaimRequest{URL: url, Host: "jobs.example.com", Company: "Acme Labs", Role: "Platform Engineer"}
}

func blockedCode(t *testing.T, err error) string {
	t.Helper()
	var b *BlockedError
	if !errors.As(err, &b) {
		t.Fatalf("want a duplicate refusal, got %v", err)
	}
	return b.Decision.Code
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTwoWalksRaceForOnePosting(t *testing.T) {
	l, _, db := clocked(t)
	const walks = 8
	var wg sync.WaitGroup
	errs := make([]error, walks)
	grants := make([]Claim, walks)
	for i := range walks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grants[i], errs[i] = l.Claim(context.Background(), req(postURL))
		}()
	}
	wg.Wait()
	granted := 0
	for i, err := range errs {
		switch {
		case err == nil:
			granted++
			if grants[i].ID == "" || !grants[i].ExpiresAt.Equal(t0.Add(15*time.Minute)) {
				t.Errorf("grant %+v", grants[i])
			}
		case blockedCode(t, err) != gates.CodeLiveSubmitting:
			t.Errorf("walk %d: %v", i, err)
		}
	}
	if granted != 1 || count(t, db, `SELECT count(*) FROM submissions WHERE state = 'submitting'`) != 1 {
		t.Fatalf("granted %d", granted)
	}
}

func TestClaimRefusesADuplicateByKeyReportOrCandidacy(t *testing.T) {
	ctx := context.Background()
	t.Run("confirmed row, same posting under another URL shape", func(t *testing.T) {
		l, _, db := clocked(t)
		if _, err := l.RecordObserved(ctx, obs("a", "https://job-boards.greenhouse.io/example-corp/jobs/1234567", true)); err != nil {
			t.Fatal(err)
		}
		_, err := l.Claim(ctx, req("https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567"))
		if blockedCode(t, err) != gates.CodeAlreadySubmitted || count(t, db, `SELECT count(*) FROM submissions`) != 1 {
			t.Fatal(err)
		}
	})
	t.Run("an unconfirmed row blocks too, since the walker carries no resubmit", func(t *testing.T) {
		l, _, _ := clocked(t)
		if _, err := l.RecordObserved(ctx, obs("a", postURL, false)); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeUnconfirmedSubmission {
			t.Fatal(err)
		}
	})
	t.Run("same company and role under a new URL", func(t *testing.T) {
		l, _, _ := clocked(t)
		o := obs("a", "https://jobs.example.com/acme/roles/1", true)
		o.Company, o.Role = "Example Corp", "Junior Engineer"
		if _, err := l.RecordObserved(ctx, o); err != nil {
			t.Fatal(err)
		}
		r := req("https://careers.example.org/other/9")
		r.Company, r.Role = "Example Corp UK", "junior  engineer"
		if _, err := l.Claim(ctx, r); blockedCode(t, err) != gates.CodeAlreadySubmitted {
			t.Fatal(err)
		}
		if _, err := l.Claim(ctx, ClaimRequest{URL: "https://careers.example.org/other/10", Host: "careers.example.org", Company: "NewRocket", Role: "Junior Engineer"}); err != nil {
			t.Errorf("a different company is not blocked: %v", err)
		}
	})
	t.Run("a row without company and role matches by its report number", func(t *testing.T) {
		l, _, _ := clocked(t)
		tr := newTracker(t, l)
		e, err := tr.Add(ctx, tracker.AddRow{Company: "Acme Labs", Role: "Platform Engineer", URL: postURL})
		if err != nil {
			t.Fatal(err)
		}
		setReport(t, l, e.ID, 7)
		n := int64(7)
		o := obs("a", "https://other.example.net/jobs/5", true)
		o.ReportNumber = &n
		if _, err := l.RecordObserved(ctx, o); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Claim(ctx, ClaimRequest{URL: postURL, Host: "jobs.example.com"}); blockedCode(t, err) != gates.CodeAlreadySubmitted {
			t.Fatal(err)
		}
	})
	t.Run("another posting is granted", func(t *testing.T) {
		l, _, _ := clocked(t)
		if _, err := l.RecordObserved(ctx, obs("a", postURL, true)); err != nil {
			t.Fatal(err)
		}
		other := req("https://jobs.example.com/acme/roles/43")
		other.Role = "Data Engineer"
		if _, err := l.Claim(ctx, other); err != nil {
			t.Fatal(err)
		}
	})
}

func newTracker(t *testing.T, l *Ledger) *tracker.Tracker {
	t.Helper()
	tr, err := tracker.New(l.s, tracker.Options{Now: l.now})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func setReport(t *testing.T, l *Ledger, id, report int64) {
	t.Helper()
	err := l.s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE tracker_entries SET report_number = ? WHERE id = ?`, report, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClaimReadsTheTrackerAsEvidence(t *testing.T) {
	ctx := context.Background()
	t.Run("an applied row of the posting blocks and an evaluated one does not", func(t *testing.T) {
		l, _, _ := clocked(t)
		tr := newTracker(t, l)
		e, err := tr.Add(ctx, tracker.AddRow{Company: "Acme Labs", Role: "Platform Engineer", URL: postURL})
		if err != nil {
			t.Fatal(err)
		}
		// The guard takes no claim, so the ledger stays empty and only the
		// tracker row can refuse the claim below.
		if _, err := l.Guard(ctx, req(postURL)); err != nil {
			t.Fatalf("an evaluated row blocked: %v", err)
		}
		_, err = tr.SetStatus(ctx, tracker.StatusChange{ID: e.ID, To: tracker.Applied, Actor: tracker.ActorUser, On: "2027-03-10"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeTrackerApplied {
			t.Fatal(err)
		}
	})
	t.Run("an applied row with the same company and role under another URL blocks", func(t *testing.T) {
		l, _, _ := clocked(t)
		tr := newTracker(t, l)
		if _, err := tr.Add(ctx, tracker.AddRow{Company: "Acme Labs", Role: "Platform Engineer", URL: "https://other.example.net/j/1", Status: tracker.Applied}); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeTrackerApplied {
			t.Fatal(err)
		}
	})
}

func TestUnreadableEvidenceBlocksTheClaim(t *testing.T) {
	for name, table := range map[string]string{"ledger": "submissions", "tracker": "tracker_entries"} {
		t.Run(name, func(t *testing.T) {
			l, _, db := clocked(t)
			err := l.s.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`ALTER TABLE ` + table + ` RENAME TO gone`)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			g, err := l.Claim(context.Background(), req(postURL))
			var b *BlockedError
			if !errors.Is(err, ErrUnavailable) || errors.As(err, &b) || g.ID != "" {
				t.Fatalf("%+v %v", g, err)
			}
			if table == "tracker_entries" && count(t, db, `SELECT count(*) FROM submissions`) != 0 {
				t.Error("a claim was written without the tracker evidence")
			}
		})
	}
}

func TestALiveClaimBlocksUntilTheWindowEndsAndThenReadsAsUncertain(t *testing.T) {
	l, at, db := clocked(t)
	ctx := context.Background()
	if _, err := l.Claim(ctx, req(postURL)); err != nil {
		t.Fatal(err)
	}
	at(t0.Add(14 * time.Minute))
	if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeLiveSubmitting {
		t.Fatal(err)
	}
	at(t0.Add(16 * time.Minute))
	if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeMayHaveSubmitted {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions`); n != 1 {
		t.Errorf("rows %d: a refused claim wrote a row", n)
	}
}

func TestProcessDeathAfterTheClaimIsSweptAtStartAndNeverRetried(t *testing.T) {
	ctx := context.Background()
	l, at, db := clocked(t)
	if _, err := l.Claim(ctx, req(postURL)); err != nil {
		t.Fatal(err)
	}
	// Another claim by a process that is still running is left to its owner.
	other := req("https://jobs.example.com/acme/roles/77")
	other.Role = "Data Engineer"
	if _, err := l.Claim(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := l.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE submissions SET pid = 4242 WHERE url = ?`, postURL)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE submissions SET pid = 4243 WHERE url <> ?`, postURL)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	restarted := New(l.s, l.now)
	restarted.pid = 1
	swept, err := restarted.SweepStartup(ctx, func(pid int) bool { return pid == 4243 })
	if err != nil || swept != 1 {
		t.Fatalf("swept %d %v", swept, err)
	}
	if got := count(t, db, `SELECT count(*) FROM submissions WHERE state = 'may-have-submitted' AND pid = 4242`); got != 1 {
		t.Fatal("the dead owner's row is not uncertain")
	}
	if got := count(t, db, `SELECT count(*) FROM submissions WHERE state = 'submitting' AND pid = 4243`); got != 1 {
		t.Fatal("a running owner's row was taken from it")
	}
	// The uncertain outcome stays and a retry is blocked, within and after the window.
	for _, d := range []time.Duration{time.Minute, 2 * time.Hour} {
		at(t0.Add(d))
		if _, err := restarted.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeMayHaveSubmitted {
			t.Fatal(err)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE url = ?`, postURL); n != 1 {
		t.Errorf("the sweep or a retry added a row: %d", n)
	}
	if again, err := restarted.SweepStartup(ctx, func(int) bool { return false }); err != nil || again != 1 {
		t.Errorf("second sweep %d %v: only the row of the process that is gone remains", again, err)
	}
}

func TestSweepTreatsAMissingOrOwnProcessIdAsDead(t *testing.T) {
	ctx := context.Background()
	l, _, db := clocked(t)
	if _, err := l.Claim(ctx, req(postURL)); err != nil {
		t.Fatal(err)
	}
	other := req("https://jobs.example.com/acme/roles/78")
	other.Role = "Data Engineer"
	if _, err := l.Claim(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := l.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE submissions SET pid = NULL WHERE url = ?`, other.URL)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	swept, err := l.SweepStartup(ctx, func(int) bool { return true })
	if err != nil || swept != 2 || count(t, db, `SELECT count(*) FROM submissions WHERE state = 'submitting'`) != 0 {
		t.Fatalf("swept %d %v", swept, err)
	}
}

func TestProcessRunningSeesALiveAndAFinishedProcess(t *testing.T) {
	if !ProcessRunning(os.Getpid()) || ProcessRunning(0) {
		t.Fatal("this process, or pid 0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot run a child: %v", err)
	}
	if ProcessRunning(cmd.Process.Pid) {
		t.Fatal("a finished child reads as running")
	}
}

func TestALostGrantResponseLeavesAnUncertainClaimNobodyRetries(t *testing.T) {
	ctx := context.Background()
	l, at, db := clocked(t)
	// The grant is taken but its response never reaches the extension, so the
	// extension asks again.
	if _, err := l.Claim(ctx, req(postURL)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeLiveSubmitting {
		t.Fatal(err)
	}
	at(t0.Add(time.Hour))
	if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeMayHaveSubmitted {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions`); n != 1 {
		t.Errorf("rows %d", n)
	}
}

func TestALostSettleResponseIsRepeatedWithoutASecondRow(t *testing.T) {
	ctx := context.Background()
	l, _, db := clocked(t)
	g, err := l.Claim(ctx, req(postURL))
	if err != nil {
		t.Fatal(err)
	}
	score, n := 4.5, int64(9)
	o := Observation{EntryID: "e1", Title: "Platform Engineer", Score: &score, ReportNumber: &n, ObservedAt: t0.Add(time.Minute)}
	first, err := l.Settle(ctx, g.ID, o)
	if err != nil || first == 0 {
		t.Fatalf("%d %v", first, err)
	}
	again, err := l.Settle(ctx, g.ID, o)
	if err != nil || again != first {
		t.Fatalf("a repeat gave %d %v", again, err)
	}
	o.Confirmed = true
	if raised, err := l.Settle(ctx, g.ID, o); err != nil || raised != first {
		t.Fatalf("raise %d %v", raised, err)
	}
	o.Confirmed = false
	if _, err := l.Settle(ctx, g.ID, o); err != nil {
		t.Fatal(err)
	}
	var state, entry string
	var confirmed int
	if err := db.QueryRow(`SELECT state, confirmed, COALESCE(entry_id, '') FROM submissions`).Scan(&state, &confirmed, &entry); err != nil {
		t.Fatal(err)
	}
	if state != "submitted" || confirmed != 1 || entry != "e1" || count(t, db, `SELECT count(*) FROM submissions`) != 1 {
		t.Fatalf("%s %d %q", state, confirmed, entry)
	}
}

func TestOneClaimIsOnePress(t *testing.T) {
	ctx := context.Background()
	l, _, db := clocked(t)
	g, err := l.Claim(ctx, req(postURL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Settle(ctx, g.ID, Observation{ObservedAt: t0, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	// The settled posting grants nothing more, to this walk or another.
	if _, err := l.Claim(ctx, req(postURL)); blockedCode(t, err) != gates.CodeAlreadySubmitted {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions`); n != 1 {
		t.Errorf("rows %d", n)
	}
}

func TestSettleRefusesAnUnknownOrExpiredClaimAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	l, at, db := clocked(t)
	if _, err := l.Settle(ctx, "claim_never_issued", Observation{ObservedAt: t0}); !errors.Is(err, ErrUnknownClaim) {
		t.Fatal(err)
	}
	g, err := l.Claim(ctx, req(postURL))
	if err != nil {
		t.Fatal(err)
	}
	at(t0.Add(16 * time.Minute))
	if _, err := l.Settle(ctx, g.ID, Observation{ObservedAt: t0, Confirmed: true}); !errors.Is(err, ErrUnknownClaim) {
		t.Fatalf("expired: %v", err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE state = 'submitting' AND confirmed = 0`); n != 1 {
		t.Error("an expired settle changed the row")
	}
	if _, err := l.SweepStartup(ctx, func(int) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Settle(ctx, g.ID, Observation{ObservedAt: t0}); !errors.Is(err, ErrUnknownClaim) {
		t.Fatalf("swept: %v", err)
	}
}

func TestAnAddressThatCannotBeKeyedIsComparedByExactString(t *testing.T) {
	l, _, _ := clocked(t)
	ctx := context.Background()
	bad := "https://jobs.example.com/x%zz"
	if claimKey(bad) != bad {
		t.Fatal("the address can be keyed, so the test proves nothing")
	}
	g, err := l.Claim(ctx, req(bad))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Settle(ctx, g.ID, Observation{ObservedAt: t0, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Claim(ctx, req(bad)); blockedCode(t, err) != gates.CodeAlreadySubmitted {
		t.Fatal(err)
	}
}

// A run whose press was refused and that the user resumes with Done claims
// again under its own run id: its earlier failed row is its own, not another
// run's. The claim is still one at a time, and a different run that began
// before either row is still refused.
func TestARefusedPressResumedByTheSameRunClaimsAgainOnce(t *testing.T) {
	ctx := context.Background()
	l, set, db := clocked(t)
	own := req(postURL)
	own.RunID, own.RunStartedAt, own.Source = "run_a", t0, SourceApplyChain
	set(t0.Add(time.Minute))
	first, err := l.Claim(ctx, own)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := l.Finish(ctx, first.ID, Verdict{PressDispatched: true, RefusalProven: true}); err != nil || st != gates.StateFailed {
		t.Fatalf("settle %v %v", st, err)
	}
	set(t0.Add(5 * time.Minute))
	again, err := l.Claim(ctx, own)
	if err != nil {
		t.Fatalf("the resumed run lost its own claim: %v", err)
	}
	if _, err := l.Claim(ctx, own); blockedCode(t, err) != gates.CodeLiveSubmitting {
		t.Fatalf("a second claim while the first is live: %v", err)
	}
	if _, err := l.Finish(ctx, again.ID, Verdict{PressDispatched: true, RefusalProven: true}); err != nil {
		t.Fatal(err)
	}
	other := req(postURL)
	other.RunID, other.RunStartedAt, other.Source = "run_b", t0, SourceApplyChain
	set(t0.Add(6 * time.Minute))
	if _, err := l.Claim(ctx, other); blockedCode(t, err) != gates.CodeClaimLost {
		t.Fatalf("another run's claim: %v", err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE run_id = 'run_a'`); n != 2 {
		t.Fatalf("%d rows under the run id", n)
	}
}

// A later row of the same run ends its may-have-submitted row, as the
// specification says; rows of one run are grouped by the run id, not by claim.
func TestALaterRowOfTheSameRunEndsItsMayHaveSubmittedRow(t *testing.T) {
	ctx := context.Background()
	l, set, _ := clocked(t)
	own := req(postURL)
	own.RunID, own.RunStartedAt, own.Source = "run_a", t0, SourceApplyChain
	set(t0.Add(time.Minute))
	first, err := l.Claim(ctx, own)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := l.Finish(ctx, first.ID, Verdict{PressDispatched: true}); st != gates.StateMayHaveSubmitted {
		t.Fatalf("state %v", st)
	}
	set(t0.Add(3 * time.Minute))
	if _, err := l.Guard(ctx, req(postURL)); blockedCode(t, err) != gates.CodeMayHaveSubmitted {
		t.Fatalf("guard %v", err)
	}
	own.Resubmit = true
	second, err := l.Claim(ctx, own)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Finish(ctx, second.ID, Verdict{PressDispatched: true, RefusalProven: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Guard(ctx, req(postURL)); err != nil {
		t.Fatalf("the run's later row did not end its may-have-submitted row: %v", err)
	}
}
