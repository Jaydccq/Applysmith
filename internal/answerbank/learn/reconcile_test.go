package learn

import (
	"context"
	"strings"
	"testing"
	"time"
)

const failStateWrite = `CREATE TRIGGER fail_state_write BEFORE UPDATE ON answer_candidates BEGIN SELECT RAISE(ABORT, 'injected failure'); END`

// pendingCandidate generates the one candidate of the heard-about question.
func (e *env) pendingCandidate() Candidate {
	e.t.Helper()
	e.abstains(3, heardQ)
	e.generate()
	return e.only(StatePending)
}

func (e *env) bankHolds(entryID string) bool { return strings.Contains(e.bankText(), "id: "+entryID) }

// The bank write and the state write are two stores: a failure between them
// leaves the entry in the bank and the candidate pending, and the start-up
// reconciliation marks it approved without writing the bank again.
func TestAnswerBank_Candidates_ReconcileAfterInterruptedApproval(t *testing.T) {
	e := newEnv(t)
	c := e.pendingCandidate()
	e.exec(failStateWrite)
	if _, err := e.approve(c.ID); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("the state write fails: %v", err)
	}
	if !e.bankHolds(c.EntryID) || e.only(StatePending).ID != c.ID {
		t.Fatal("the bank holds the entry and the candidate is still pending")
	}
	e.exec(`DROP TRIGGER fail_state_write`)
	text := e.bankText()
	n, err := e.s.Reconcile(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("reconciled %d: %v", n, err)
	}
	got := e.candidates(StateApproved)
	if len(got) != 1 || got[0].ID != c.ID || got[0].DecidedAt == nil || len(e.candidates(StatePending)) != 0 {
		t.Fatalf("%+v", got)
	}
	if e.bankText() != text {
		t.Fatal("reconciliation never writes the bank")
	}
	e.s.Flush()
	if e.count("answer_events WHERE kind = 'candidate-approved' AND entry_id = ?", c.EntryID) != 1 {
		t.Fatal("the reconciled approval is recorded once")
	}
	if n, err := e.s.Reconcile(context.Background()); err != nil || n != 0 {
		t.Fatalf("a second pass finds nothing: %d %v", n, err)
	}
}

// A candidate the bank does not hold is left pending by reconciliation.
func TestAnswerBank_Candidates_ReconcileLeavesUnappliedCandidates(t *testing.T) {
	e := newEnv(t)
	c := e.pendingCandidate()
	if n, err := e.s.Reconcile(context.Background()); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if got := e.only(StatePending); got.ID != c.ID || e.bankHolds(c.EntryID) {
		t.Fatalf("%+v", got)
	}
}

// Approving again after the interruption is idempotent: the entry is in the
// bank once, and the candidate ends approved.
func TestAnswerBank_Candidates_RepeatedApprovalIsIdempotent(t *testing.T) {
	e := newEnv(t)
	c := e.pendingCandidate()
	e.exec(failStateWrite)
	if _, err := e.approve(c.ID); err == nil {
		t.Fatal("injected failure")
	}
	e.exec(`DROP TRIGGER fail_state_write`)
	d, err := e.approve(c.ID)
	if err != nil || d.Candidate.State != StateApproved || d.EntryID != c.EntryID {
		t.Fatalf("%+v %v", d, err)
	}
	if n := strings.Count(e.bankText(), "id: "+c.EntryID); n != 1 {
		t.Fatalf("the entry is in the bank %d times", n)
	}
	_, err = e.approve(c.ID)
	asConflict(t, err, ReasonNotPending)
}

// The same holds for an example appended to an existing entry.
func TestAnswerBank_Candidates_ReconcileExtendedExample(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("availability", "Flexible", []string{"What are your preferred working hours?"}))
	e.record(Event{Kind: KindMatch, At: baseTime.Add(-time.Hour), Host: "jobs.example.com", Question: "Which of these describes your preferred working hours?",
		ControlKind: "select", Options: []string{"Flexible", "Fixed"}, Lane: LaneModelOption, EntryID: "availability", Outcome: "filled-model", OptionLabel: "Flexible"})
	e.generate()
	c := e.only(StatePending)
	if c.Type != TypeExtendExamples {
		t.Fatalf("%+v", c)
	}
	e.exec(failStateWrite)
	if _, err := e.approve(c.ID); err == nil {
		t.Fatal("injected failure")
	}
	e.exec(`DROP TRIGGER fail_state_write`)
	if n, err := e.s.Reconcile(context.Background()); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if got := e.candidates(StateApproved); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestAnswerBank_Maintain_ReconcilesFirst(t *testing.T) {
	e := newEnv(t)
	c := e.pendingCandidate()
	e.exec(failStateWrite)
	if _, err := e.approve(c.ID); err == nil {
		t.Fatal("injected failure")
	}
	e.exec(`DROP TRIGGER fail_state_write`)
	if _, err := e.s.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.candidates(StateApproved); len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("%+v", got)
	}
}

// Decision telemetry is best effort: a store that refuses the event does not
// fail the decision.
func TestAnswerBank_Candidates_DecisionTelemetryFailureDoesNotFailTheDecision(t *testing.T) {
	e := newEnv(t)
	e.abstains(3, heardQ)
	e.abstains(3, "Another open question")
	e.generate()
	cs := e.candidates(StatePending)
	e.exec(`CREATE TRIGGER fail_decision_event BEFORE INSERT ON answer_events WHEN NEW.kind IN ('candidate-approved', 'candidate-rejected')
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	approved, err := e.approve(cs[0].ID)
	if err != nil || approved.Candidate.State != StateApproved || !e.bankHolds(approved.EntryID) {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	rejected, err := e.s.Reject(context.Background(), cs[1].ID)
	if err != nil || rejected.Candidate.State != StateRejected {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	e.s.Flush()
	if e.count("answer_events WHERE kind IN ('candidate-approved', 'candidate-rejected')") != 0 {
		t.Fatal("the injected failure stored an event")
	}
	if !strings.Contains(e.logged(), "telemetry write failed") {
		t.Fatalf("the failure is logged: %q", e.logged())
	}
}
