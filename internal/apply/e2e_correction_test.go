//go:build browser

package apply

import (
	"testing"

	"applysmith/internal/gates"
)

// A confirmed submission is re-sent once as a correction the user asked for by
// name, recorded as superseding the first row, and never counted again.
func TestE2E_Apply_CorrectionReSendsOnceAndLeavesTheTrackerAlone(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "greenhouse", modeAccept, Hooks{})
	first := s.run(submitInput(s.url))
	defer dump(t, first)
	if first.Outcome.Kind != OutcomeSubmitted || !first.Outcome.Confirmed || s.b.presses() != 1 {
		t.Fatalf("first submit %+v, %d writes", first.Outcome, s.b.presses())
	}
	events := s.g.count(t, `SELECT count(*) FROM tracker_events`)

	ask := func(msg string) Input {
		return Input{URL: s.url, Submit: true, ExplicitRequest: true, Caller: gates.CallerAskAgent, Tool: ToolAutofill, UserMessage: msg, OwnWords: msg}
	}
	corr := s.run(ask(gates.CorrectionMessage("Example Corp", s.url)))
	defer dump(t, corr)
	if corr.Outcome.Kind != OutcomeSubmitted || !corr.Correction || corr.Supersedes == 0 || s.b.presses() != 2 {
		t.Fatalf("correction %+v correction=%v, %d writes", corr.Outcome, corr.Correction, s.b.presses())
	}
	if n := s.g.count(t, `SELECT count(*) FROM submissions WHERE correction = 1 AND supersedes = ? AND state = 'submitted'`, corr.Supersedes); n != 1 {
		t.Fatalf("correction rows %d", n)
	}
	if st, _ := s.trackerStatus(); st != "applied" || s.g.count(t, `SELECT count(*) FROM tracker_events`) != events {
		t.Fatalf("the correction touched the tracker: %s", st)
	}

	again := s.run(ask(gates.CorrectionMessage("Example Corp", s.url)))
	defer dump(t, again)
	mustStop(t, again, StageSubmit, ClassPosting)
	if again.Outcome.Reason != "correction refused: a posting is corrected once" || s.b.presses() != 2 {
		t.Fatalf("second correction %+v, %d writes", again.Outcome, s.b.presses())
	}
}
