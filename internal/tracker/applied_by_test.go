package tracker

import "testing"

func TestTracker_UserCorrectionKeepsTheAgentMark(t *testing.T) {
	f := newFixture(t)
	e := f.upsert(report(1, "Example Corp", "Engineer", "https://example.com/1", 4.0)).Entry
	f.setStatus(e.ID, Applied, ActorApplyChain)
	f.setStatus(e.ID, Rejected, ActorGmail)
	res := f.setStatus(e.ID, Applied, ActorUser)
	if !res.Correction {
		t.Fatalf("expected a correction: %+v", res)
	}
	if got := res.Entry; got.AppliedBy != ByAgent || got.DisplayStatus() != "applied (agent)" {
		t.Errorf("applied_by = %q after a user correction, want agent", got.AppliedBy)
	}
}
