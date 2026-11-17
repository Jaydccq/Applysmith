//go:build browser

package apply

import (
	"fmt"
	"strings"
	"testing"

	"applysmith/internal/gates"
)

func dryInput(url string) Input { return Input{URL: url, Caller: gates.CallerCLI} }

func (s *scenario) extraBoard(kind string) *board {
	return s.site.add(&board{kind: kind, marker: fmt.Sprintf("j%d%07d", 4, boardSeq.Add(1))})
}

func TestApplyChain_LinkedInFlowUrlWithNotFoundFallback(t *testing.T) {
	t.Parallel()
	t.Run("the modal appears after a page that never finishes loading", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-late", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeFilled || len(run.Pages) != 2 {
			t.Fatalf("outcome %+v, %d pages", run.Outcome, len(run.Pages))
		}
		if s.b.hitsOn("/apply/") == 0 {
			t.Fatal("the flow URL was never opened")
		}
		if n := s.b.hangsOut.Load(); n != 0 {
			t.Fatalf("%d never-ending resources were held to their end: the run waited for a load event that never comes", n)
		}
	})
	t.Run("a not-found flow page sends the run to the posting page and its button", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-404flow", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeFilled || len(run.Pages) != 2 {
			t.Fatalf("outcome %+v, %d pages", run.Outcome, len(run.Pages))
		}
		if s.b.hitsOn("/apply/") == 0 || s.b.hitsOn("/jobs/view/") < 2 {
			t.Fatalf("paths %v", s.b.hits)
		}
	})
	t.Run("the server-driven layout: a not-found flow page, then the button's native dialog", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-sdui", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeFilled || len(run.Pages) != 2 {
			t.Fatalf("outcome %+v, %d pages", run.Outcome, len(run.Pages))
		}
		if s.b.hitsOn("/apply/") == 0 || s.b.hitsOn("/jobs/view/") < 2 {
			t.Fatalf("paths %v", s.b.hits)
		}
	})
	t.Run("LinkedIn's job search safety reminder is passed with Continue applying", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-safety", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeFilled || len(run.Pages) != 2 {
			t.Fatalf("outcome %+v, %d pages", run.Outcome, len(run.Pages))
		}
	})
	t.Run("a posting that is not found either is gone", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-gone", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		mustStop(t, run, StageAccount, ClassPosting)
		if !strings.Contains(run.Outcome.Reason, "the posting is gone") || len(run.PostingFacts) != 1 {
			t.Fatalf("reason %q facts %v", run.Outcome.Reason, run.PostingFacts)
		}
	})
}

func TestApplyChain_LinkedInOffsiteUrlFromOwnPostingPayloadOnly(t *testing.T) {
	t.Parallel()
	t.Run("the destination is this posting's own, never a similar-job card's", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-offsite", modeAccept, Hooks{})
		employer, decoy := s.extraBoard("ashby"), s.extraBoard("ashby")
		s.b.dest, s.b.decoy = s.site.postingURL(employer), s.site.postingURL(decoy)
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeFilled || run.ATS != ATSAshby {
			t.Fatalf("outcome %+v ats %s", run.Outcome, run.ATS)
		}
		if employer.loads.Load() == 0 || decoy.loads.Load() != 0 {
			t.Fatalf("the employer's board loaded %d times, the decoy's %d", employer.loads.Load(), decoy.loads.Load())
		}
		var lines []string
		for _, l := range run.Stages {
			if l.Stage == StageResolve {
				lines = append(lines, l.Detail)
			}
		}
		if len(lines) != 2 || !strings.Contains(lines[1], "applies on the employer's site") || run.Identity == "" {
			t.Fatalf("resolve lines %q", lines)
		}
	})
	t.Run("a payload that names another posting's destination only is no destination", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-decoyonly", modeAccept, Hooks{})
		decoy := s.extraBoard("ashby")
		s.b.decoy = s.site.postingURL(decoy)
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		mustStop(t, run, StageAccount, ClassPosting)
		if !strings.Contains(run.Outcome.Reason, `does not offer Easy Apply; it applies through "Apply"`) || decoy.loads.Load() != 0 {
			t.Fatalf("reason %q, decoy loads %d", run.Outcome.Reason, decoy.loads.Load())
		}
	})
	t.Run("a signed-out guest page means sign-in is needed", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-guest", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		mustStop(t, run, StageAccount, ClassCredential)
		if run.Outcome.Board != "sign-in" || !strings.Contains(run.Outcome.Reason, "signed-out") || strings.Contains(run.Outcome.Reason, "employer") {
			t.Fatalf("outcome %+v", run.Outcome)
		}
	})
	t.Run("a closed banner that appears late is found by polling", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-closed", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		mustStop(t, run, StageAccount, ClassPosting)
		if !strings.Contains(run.Outcome.Reason, "no longer accepting applications") || len(run.PostingFacts) != 1 {
			t.Fatalf("reason %q facts %v", run.Outcome.Reason, run.PostingFacts)
		}
	})
	t.Run("an applied receipt that appears late is found by polling", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-applied", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		mustStop(t, run, StageAccount, ClassPosting)
		if !strings.Contains(run.Outcome.Reason, "already applied outside Easy Apply") || !strings.Contains(run.Outcome.Reason, "You applied on company site") {
			t.Fatalf("reason %q", run.Outcome.Reason)
		}
	})
}

func TestApplyChain_LinkedInUnconfirmedSubmitReportsModalState(t *testing.T) {
	t.Parallel()
	t.Run("a press that nothing answers reports the modal still holding Submit", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin", modeSilent, Hooks{})
		run := s.run(submitInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeSubmitted || run.Outcome.Confirmed {
			t.Fatalf("outcome %+v", run.Outcome)
		}
		l := stageOf(run, StageConfirm)
		if l.Status != StatusFailed || !strings.Contains(l.Detail, "no confirmation within 3s") || !strings.Contains(l.Detail, "the Easy Apply modal still holds Submit") {
			t.Fatalf("confirm line %+v", l)
		}
		if s.b.presses() != 0 || run.Presses != 1 {
			t.Fatalf("server writes %d, presses %d", s.b.presses(), run.Presses)
		}
	})
	t.Run("a 200 on the applications endpoint confirms inside the window", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin", modeAccept, Hooks{})
		run := s.run(submitInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed || run.Outcome.ConfirmSource != ConfirmATSResponse {
			t.Fatalf("outcome %+v", run.Outcome)
		}
	})
	t.Run("a captcha challenge in the modal stops before the next step", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-challenge-next", modeAccept, Hooks{})
		run := s.run(dryInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeStopped || !strings.Contains(run.Outcome.Reason, "captcha") || len(run.Pages) != 1 {
			t.Fatalf("outcome %+v, %d pages", run.Outcome, len(run.Pages))
		}
	})
	t.Run("a captcha challenge on the review step presses nothing", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, "linkedin-challenge-submit", modeAccept, Hooks{})
		run := s.run(submitInput(s.url))
		defer dump(t, run)
		if run.Outcome.Kind != OutcomeStopped || !strings.Contains(run.Outcome.Reason, "captcha") {
			t.Fatalf("outcome %+v", run.Outcome)
		}
		if s.b.presses() != 0 || run.Pressed {
			t.Fatalf("server writes %d, pressed %v", s.b.presses(), run.Pressed)
		}
		if state, _, _ := s.g.ledgerState(t); state != "none" {
			t.Fatalf("ledger %s: a claim was taken before the challenge check", state)
		}
	})
}

func TestE2E_Jobvite_NextInEmbedFrameAdvanced(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "jobvite-embed", modeAccept, Hooks{})
	run := s.run(dryInput(s.url))
	defer dump(t, run)
	t.Logf("frame events %v", s.b.eventList())
	if run.Outcome.Kind != OutcomeFilled || len(run.Pages) != 2 {
		t.Fatalf("outcome %+v, %d pages", run.Outcome, len(run.Pages))
	}
	// The wizard swaps its step under one address: each Next reached the frame,
	// the values the engine wrote were in place when it was pressed, and the
	// walk went on to the next step instead of stalling on the first.
	events := s.b.eventList()
	t.Logf("frame events %v", events)
	if len(events) != 2 || events[0] != "next:1:Alex" || events[1] != "next:2:No" {
		t.Fatalf("the frame's Next was pressed as %v", events)
	}
	if s.b.presses() != 0 {
		t.Fatalf("a dry run wrote %d times", s.b.presses())
	}
	if l := stageOf(run, StageFill); l.Status != StatusOK || !strings.Contains(l.Detail, "ready for Submit") {
		t.Fatalf("fill line %+v", l)
	}
	// The check reads the frame, so a submitting run reaches the gated Submit and
	// presses it once.
	again := newScenario(t, "jobvite-embed", modeAccept, Hooks{})
	sub := again.run(submitInput(again.url))
	defer dump(t, sub)
	if sub.Outcome.Kind != OutcomeSubmitted || !sub.Pressed || again.b.presses() != 1 || again.b.recorded() != 1 {
		t.Fatalf("outcome %+v, presses %d recorded %d", sub.Outcome, again.b.presses(), again.b.recorded())
	}
	if l := stageOf(sub, StageCheck); l.Status != StatusOK {
		t.Fatalf("check line %+v: the framed pages were not verified", l)
	}
}
