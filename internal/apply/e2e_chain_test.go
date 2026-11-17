//go:build browser

package apply

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/gates"
	"applysmith/internal/tracker"
)

var boardSeq atomic.Int32

// adapters are the named systems, each with its own mock.
var adapters = []string{"greenhouse", "lever", "ashby", "workday", "linkedin"}

// slots bounds how many scenarios drive the shared browser at once: the tests
// of one adapter run in parallel with those of the others, so the whole matrix
// fits the time a loaded machine allows.
var slots = make(chan struct{}, 3)

type scenario struct {
	runs []string
	t    *testing.T
	g    *e2e
	site *site
	b    *board
	url  string
	lane *browser.LaneHandle
}

func newScenario(t *testing.T, kind string, mode mockMode, hk Hooks) *scenario {
	t.Helper()
	inst, st := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	slots <- struct{}{}
	marker := fmt.Sprintf("j%d%07d", 4, boardSeq.Add(1))
	if strings.HasPrefix(kind, "linkedin") {
		marker = strings.TrimPrefix(marker, "j") + "00" // LinkedIn job ids are numbers
	}
	b := st.add(&board{kind: kind, marker: marker})
	b.setMode(mode)
	g := newE2E(t, lane, t.TempDir(), hk)
	url := st.postingURL(b)
	g.seedPosting(t, url)
	sc := &scenario{t: t, g: g, site: st, b: b, url: url, lane: lane}
	t.Cleanup(func() {
		// Tabs a stop kept for the user are closed so the shared browser stays
		// small; only this scenario's own runs' tabs, because others run beside it.
		own := map[string]bool{}
		for _, id := range sc.runIDs() {
			own[id] = true
		}
		for _, ti := range lane.Tabs() {
			if own[ti.Owner] {
				_ = lane.CloseTab(context.Background(), ti.ID)
			}
		}
		<-slots
	})
	return sc
}

func (s *scenario) runIDs() []string {
	rows, err := s.g.st.Read().Query(`SELECT run_id FROM apply_runs`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

func (s *scenario) run(in Input) *Run { return s.runCtx(context.Background(), in) }

func (s *scenario) runCtx(ctx context.Context, in Input) *Run {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	return s.g.runner.RunOne(ctx, in)
}

func (s *scenario) trackerStatus() (status, appliedBy string) {
	s.t.Helper()
	if err := s.g.st.Read().QueryRow(`SELECT status, COALESCE(applied_by, '') FROM tracker_entries ORDER BY id LIMIT 1`).Scan(&status, &appliedBy); err != nil {
		s.t.Fatal(err)
	}
	return
}

func dump(t *testing.T, r *Run) {
	t.Helper()
	if !t.Failed() {
		return
	}
	t.Logf("outcome: %+v", r.Outcome)
	for _, l := range r.Stages {
		t.Logf("  %-9s %-8s %s", l.Stage, l.Status, l.Detail)
	}
	for _, p := range r.Pages {
		t.Logf("  page %+v", p)
	}
	for _, w := range r.Warnings {
		t.Logf("  warning: %s", w)
	}
}

func forEachAdapter(t *testing.T, fn func(t *testing.T, kind string)) {
	for _, kind := range adapters {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fn(t, kind)
		})
	}
}

func TestE2E_Apply_DryRunThenSubmitSucceedsWithExactlyOnePress(t *testing.T) {
	t.Parallel()
	forEachAdapter(t, func(t *testing.T, kind string) {
		s := newScenario(t, kind, modeAccept, Hooks{})
		dry := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
		defer dump(t, dry)
		if dry.Outcome.Kind != OutcomeFilled || s.b.presses() != 0 {
			t.Fatalf("dry run: %+v, attempts %d", dry.Outcome, s.b.presses())
		}
		if state, _, _ := s.g.ledgerState(t); state != "none" {
			t.Fatalf("a dry run wrote a ledger row: %s", state)
		}
		if st, _ := s.trackerStatus(); st != "evaluated" {
			t.Fatalf("a dry run changed the tracker: %s", st)
		}

		run := s.run(submitInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed || run.Outcome.ConfirmSource != ConfirmATSResponse {
			t.Fatalf("outcome %+v", run.Outcome)
		}
		if s.b.presses() != 1 || s.b.recorded() != 1 {
			t.Fatalf("server saw %d writes, recorded %d", s.b.presses(), s.b.recorded())
		}
		if state, confirmed, src := s.g.ledgerState(t); state != "submitted" || !confirmed || src != ConfirmATSResponse {
			t.Fatalf("ledger %s confirmed=%v source=%s", state, confirmed, src)
		}
		if st, by := s.trackerStatus(); st != "applied" || by != string(tracker.ByAgent) {
			t.Fatalf("tracker %s applied_by %q", st, by)
		}
		for _, g := range []gates.Gate{gates.GateSubmitAuthorization, gates.GateDuplicateGuard, gates.GatePressClaim, gates.GateControlPress} {
			if s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = ? AND run_id = ?`, string(g), run.ID) == 0 {
				t.Errorf("no audit row for gate %s", g)
			}
		}
		if s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND run_id = ? AND verdict = 'allow' AND code = 'ok'`, run.ID) < 1 {
			t.Error("the Submit press was not audited as allowed")
		}
		// A second submitting run on the same posting is a duplicate and presses nothing.
		again := s.run(submitInput(s.url))
		mustStop(t, again, StageResolve, ClassPosting)
		if s.b.presses() != 1 {
			t.Fatalf("a duplicate run pressed: %d writes", s.b.presses())
		}
	})
}

// The Ask Agent's submit, as an approved submit card starts it (the input
// TestApplyStack_AnApprovedHeldSubmitReachesTheRunnerAndTheBoard sends the
// runner): one press on each mock board, the submission confirmed, the tracker
// applied by the agent, and every gate row naming the Ask Agent's turn.
func TestE2E_Apply_AnAskAgentSubmitPressesOnceAndNamesItsTurn(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"greenhouse", "lever", "ashby"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t, kind, modeAccept, Hooks{})
			msg := "Apply to " + s.url + " and submit it."
			run := s.run(Input{URL: s.url, Submit: true, ExplicitRequest: true, UserStarted: true, Caller: gates.CallerAskAgent,
				Tool: gates.ToolAutofillApplication, ConversationID: "ses_e2e_agent", UserMessage: msg, OwnWords: msg})
			defer dump(t, run)
			if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed {
				t.Fatalf("outcome %+v", run.Outcome)
			}
			if s.b.presses() != 1 || s.b.recorded() != 1 {
				t.Fatalf("server saw %d writes, recorded %d", s.b.presses(), s.b.recorded())
			}
			if st, by := s.trackerStatus(); st != "applied" || by != string(tracker.ByAgent) {
				t.Fatalf("tracker %s applied_by %q", st, by)
			}
			if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'submit_authorization' AND verdict = 'allow' AND run_id = ? AND caller = 'ask_agent'
				AND tool = ? AND conversation_id = 'ses_e2e_agent' AND user_message_hash IS NOT NULL`, run.ID, gates.ToolAutofillApplication); n != 1 {
				t.Fatalf("%d submit authorizations name the Ask Agent's turn", n)
			}
			if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE run_id = ? AND (caller IS NOT 'ask_agent' OR conversation_id IS NOT 'ses_e2e_agent')`, run.ID); n != 0 {
				t.Fatalf("%d gate rows of the run do not name the Ask Agent's turn", n)
			}
		})
	}
}

func TestE2E_Apply_ServiceRefusalStopsAsDefectAndLeavesNoSubmission(t *testing.T) {
	t.Parallel()
	forEachAdapter(t, func(t *testing.T, kind string) {
		s := newScenario(t, kind, modeRefuse, Hooks{})
		run := s.run(submitInput(s.url))
		defer dump(t, run)
		mustStop(t, run, StageSubmit, ClassDefect)
		if !strings.Contains(run.Outcome.Reason, "422") {
			t.Fatalf("reason %q", run.Outcome.Reason)
		}
		if s.b.presses() != 1 || s.b.recorded() != 0 {
			t.Fatalf("server saw %d writes, recorded %d", s.b.presses(), s.b.recorded())
		}
		if state, _, _ := s.g.ledgerState(t); state != "failed" {
			t.Fatalf("ledger %s", state)
		}
		if st, _ := s.trackerStatus(); st != "evaluated" {
			t.Fatalf("tracker %s", st)
		}
		// A refused press is not a submission: the next run may try again.
		s.b.setMode(modeAccept)
		next := s.run(submitInput(s.url))
		defer dump(t, next)
		if next.Outcome.Kind != OutcomeSubmitted || s.b.recorded() != 1 {
			t.Fatalf("retry after a refusal: %+v, recorded %d", next.Outcome, s.b.recorded())
		}
	})
}

func TestE2E_Apply_CancelBeforeDispatchPressesNothing(t *testing.T) {
	t.Parallel()
	forEachAdapter(t, func(t *testing.T, kind string) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := newScenario(t, kind, modeAccept, Hooks{AfterClaim: func() { cancel() }})
		run := s.runCtx(ctx, submitInput(s.url))
		defer dump(t, run)
		mustStop(t, run, StageSubmit, ClassEnvironment)
		if !strings.Contains(run.Outcome.Reason, "cancelled") {
			t.Fatalf("reason %q", run.Outcome.Reason)
		}
		if s.b.presses() != 0 {
			t.Fatalf("the press went out: %d writes", s.b.presses())
		}
		if state, _, _ := s.g.ledgerState(t); state != "failed" {
			t.Fatalf("ledger %s: a claim with no press must not stand", state)
		}
		if stageOf(run, StageRecord).Status != StatusOK {
			t.Fatal("record did not run")
		}
	})
}

func TestE2E_Apply_CancelAfterDispatchStillReachesVerdictAndRecord(t *testing.T) {
	t.Parallel()
	forEachAdapter(t, func(t *testing.T, kind string) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := newScenario(t, kind, modeAccept, Hooks{AfterPress: func() { cancel() }})
		run := s.runCtx(ctx, submitInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed {
			t.Fatalf("outcome %+v: a press cannot be undone by a cancel", run.Outcome)
		}
		if s.b.presses() != 1 {
			t.Fatalf("writes %d", s.b.presses())
		}
		if state, confirmed, _ := s.g.ledgerState(t); state != "submitted" || !confirmed {
			t.Fatalf("ledger %s confirmed=%v", state, confirmed)
		}
		if st, _ := s.trackerStatus(); st != "applied" {
			t.Fatalf("tracker %s", st)
		}
	})
}

func TestE2E_Apply_LostResponseIsRecordedUnconfirmedAndNeverRetried(t *testing.T) {
	t.Parallel()
	forEachAdapter(t, func(t *testing.T, kind string) {
		s := newScenario(t, kind, modeLost, Hooks{})
		run := s.run(submitInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeSubmitted || run.Outcome.Confirmed {
			t.Fatalf("outcome %+v: the server dropped the answer, so the run is submitted and unconfirmed", run.Outcome)
		}
		if l := stageOf(run, StageConfirm); l.Status != StatusFailed || !strings.Contains(l.Detail, "verify manually") {
			t.Fatalf("confirm line %+v", l)
		}
		if s.b.presses() != 1 {
			t.Fatalf("writes %d", s.b.presses())
		}
		if state, confirmed, _ := s.g.ledgerState(t); state != "submitted" || confirmed {
			t.Fatalf("ledger %s confirmed=%v", state, confirmed)
		}
		if st, _ := s.trackerStatus(); st != "evaluated" {
			t.Fatalf("an unconfirmed submit changed the tracker: %s", st)
		}
		// Not retried by anything; a later run is blocked until the user asks to resubmit.
		next := s.run(submitInput(s.url))
		mustStop(t, next, StageResolve, ClassPosting)
		if !strings.Contains(next.Outcome.Reason, "resubmit") {
			t.Fatalf("reason %q", next.Outcome.Reason)
		}
		if s.b.presses() != 1 {
			t.Fatalf("a blocked run pressed: %d writes", s.b.presses())
		}
	})
}

func TestE2E_Apply_TwoConcurrentRequestsForOnePostingPressOnce(t *testing.T) {
	t.Parallel()
	forEachAdapter(t, func(t *testing.T, kind string) {
		s := newScenario(t, kind, modeSlow, Hooks{})
		var wg sync.WaitGroup
		runs := make([]*Run, 2)
		for i := range runs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				runs[i] = s.run(submitInput(s.url))
			}()
		}
		wg.Wait()
		submitted, stopped := 0, 0
		for _, r := range runs {
			defer dump(t, r)
			switch r.Outcome.Kind {
			case OutcomeSubmitted:
				submitted++
			case OutcomeStopped:
				stopped++
				if r.Outcome.Class != ClassPosting {
					t.Errorf("the loser stopped with %+v", r.Outcome)
				}
			}
		}
		if submitted != 1 || stopped != 1 {
			t.Fatalf("submitted %d, stopped %d", submitted, stopped)
		}
		if s.b.presses() != 1 || s.b.recorded() != 1 {
			t.Fatalf("server saw %d writes, recorded %d", s.b.presses(), s.b.recorded())
		}
		if n := s.g.count(t, `SELECT count(*) FROM submissions WHERE state = 'submitted'`); n != 1 {
			t.Fatalf("%d submitted rows", n)
		}
	})
}

func TestE2E_Apply_CoveredSubmitControlIsNotPressed(t *testing.T) {
	s := newScenario(t, "greenhouse", modeCovered, Hooks{})
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeStopped {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if s.b.presses() != 0 {
		t.Fatalf("writes %d: a covered control was pressed", s.b.presses())
	}
	if state, _, _ := s.g.ledgerState(t); state != "failed" && state != "none" {
		t.Fatalf("ledger %s", state)
	}
}
