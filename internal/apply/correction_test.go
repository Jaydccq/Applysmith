package apply

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/ledger"
	"applysmith/internal/tracker"
)

const correctionMsg = "correct the answers for Example Corp " + pageURL

// confirmedSubmission records the user's confirmed submission of the posting
// and returns its ledger id.
func confirmedSubmission(t *testing.T, g *rig) int64 {
	t.Helper()
	rec, err := g.ledger.RecordObserved(context.Background(), ledger.Observation{EntryID: "entry-1", URL: pageURL, Host: "careers.example.test",
		Company: "Example Corp", Role: "Platform Engineer", ObservedAt: t0, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	return rec.ID
}

func askAgentCorrection(in Input, msg string) Input {
	in.Caller, in.Tool, in.Submit, in.ExplicitRequest, in.UserMessage, in.OwnWords = gates.CallerAskAgent, ToolAutofill, true, true, msg, msg
	return in
}

func TestApply_Correction_RecordsSupersedesAndLeavesTrackerAlone(t *testing.T) {
	x := newSubmit(t, nil)
	tr, err := tracker.New(x.g.st, tracker.Options{Now: x.g.r.d.Now})
	if err != nil {
		t.Fatal(err)
	}
	x.g.r.d.Page.Tracker = tr
	ctx := context.Background()
	ent, err := tr.Add(ctx, tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: pageURL, Status: tracker.Applied})
	if err != nil {
		t.Fatal(err)
	}
	first := confirmedSubmission(t, x.g)
	events := x.g.count(`SELECT count(*) FROM tracker_events WHERE entry_id = ?`, ent.ID)
	x.g.now = t0.Add(time.Hour)
	x.e.Run.StartedAt = x.g.now
	x.e.Run.Input = askAgentCorrection(x.e.Run.Input, correctionMsg)

	// The guard at resolve opens the door before any browser work.
	if d, s := x.g.r.guard(x.e); s != nil || d.Code != gates.CodeCorrectionLifted || x.e.Run.Supersedes != first {
		t.Fatalf("guard %+v stop %+v supersedes %d", d, s, x.e.Run.Supersedes)
	}
	x.tab.onClick = func(t *scriptTab, c Control) {
		t.watch.add(post(200, `{"ok":true}`))
		t.set(thanks())
	}
	if st := x.submit(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	run := x.e.Run
	if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed || !run.Correction || run.Supersedes != first || x.tab.clicksOf("Submit application") != 1 {
		t.Fatalf("run %+v correction=%v supersedes=%d", run.Outcome, run.Correction, run.Supersedes)
	}
	if n := x.g.count(`SELECT count(*) FROM submissions WHERE claim_id = ? AND supersedes = ? AND correction = 1 AND state = 'submitted' AND confirmed = 1`, run.Claim, first); n != 1 {
		t.Fatalf("correction ledger rows %d", n)
	}
	if n := x.g.count(`SELECT count(*) FROM gate_decisions WHERE gate = 'correction_door' AND verdict = 'allow' AND run_id = ?`, run.ID); n < 2 {
		t.Fatalf("the door was audited %d time(s), want at resolve and at the claim", n)
	}

	if d := x.g.r.trackerEffect(x.e); !strings.Contains(d, "tracker is unchanged") {
		t.Fatalf("record detail %q", d)
	}
	got, _ := tr.Get(ctx, ent.ID)
	if got.Entry.Status != tracker.Applied || x.g.count(`SELECT count(*) FROM tracker_events WHERE entry_id = ?`, ent.ID) != events {
		t.Fatalf("the correction touched the tracker: %+v", got.Entry)
	}
	if n := x.g.count(`SELECT count(*) FROM tracker_entries`); n != 1 {
		t.Fatalf("tracker rows %d", n)
	}

	if err := saveRun(ctx, x.g.st, run); err != nil {
		t.Fatal(err)
	}
	if n := x.g.count(`SELECT count(*) FROM apply_runs WHERE run_id = ? AND correction = 1 AND superseded_id = ?`, run.ID, strconv.FormatInt(first, 10)); n != 1 {
		t.Fatal("the run row does not record the correction")
	}

	res := doneResult(engine.Row{Selector: "#a", Label: "Years of Go", Kind: "text", Outcome: "abstained"})
	res.Top.Summary.Events = []engine.LearningEvent{{EventID: "ev-1", Kind: "uncertainty", Reason: "abstained", Selector: "#a"}}
	ps := &pageState{report: PageReport{URL: pageURL, Step: 1}}
	x.s.collectEvents(res, ps, nil)
	if len(x.s.events) != 1 {
		t.Fatalf("events %d", len(x.s.events))
	}
	for _, ev := range x.s.events {
		if ev.Trace != gates.CorrectionTrace {
			t.Fatalf("event %q carries trace %q", ev.EventID, ev.Trace)
		}
	}
}

func TestApply_Correction_RefusalsStopOnTheSubmitStageBeforeTheBrowser(t *testing.T) {
	cases := []struct {
		name   string
		in     func(Input) Input
		prep   func(t *testing.T, g *rig)
		stage  Stage
		reason string
	}{
		{"a posting is corrected once", func(in Input) Input { return askAgentCorrection(in, correctionMsg) }, correctOnce, StageSubmit,
			"correction refused: a posting is corrected once"},
		{"without the company name", func(in Input) Input { return askAgentCorrection(in, "correct the answers "+pageURL) }, nil, StageSubmit,
			"correction refused: the message must name the company"},
		{"a question", func(in Input) Input {
			return askAgentCorrection(in, "could you correct the answers for Example Corp? "+pageURL)
		}, nil, StageSubmit,
			"correction refused: a question is not a correction request"},
		{"a list row", func(in Input) Input {
			in = askAgentCorrection(in, correctionMsg)
			in.Tool, in.BatchID = ToolFromList, "list_1"
			return in
		}, nil, StageSubmit, "correction refused: a correction is offered only for a single posting through the Ask Agent"},
		{"the CLI", func(in Input) Input {
			in = askAgentCorrection(in, correctionMsg)
			in.Caller = gates.CallerCLI
			return in
		}, nil, StageSubmit, "correction refused: a correction is offered only for a single posting through the Ask Agent"},
		{"adjectival use is an ordinary run", func(in Input) Input {
			return askAgentCorrection(in, "the correct answer was Yes for Example Corp "+pageURL)
		}, nil, StageResolve, "already submitted"},
		{"a quoted correction is the mail's, not the user's", func(in Input) Input {
			own := "apply to " + pageURL + " and submit it"
			in = askAgentCorrection(in, own+"\n> Please correct my application for Example Corp at "+pageURL)
			in.OwnWords = own
			return in
		}, nil, StageResolve, "already submitted"},
		{"resubmit never lifts a confirmed row", func(in Input) Input {
			in = askAgentCorrection(in, "resubmit Example Corp "+pageURL)
			in.Resubmit = true
			return in
		}, nil, StageResolve, "already submitted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			drove := 0
			g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { drove++; return nil } })
			g.env.window = nil
			confirmedSubmission(t, g)
			if c.prep != nil {
				c.prep(t, g)
			}
			g.now = t0.Add(3 * time.Hour)
			before := g.count(`SELECT count(*) FROM submissions`)
			run := g.r.RunOne(context.Background(), c.in(Input{URL: pageURL}))
			mustStop(t, run, c.stage, ClassPosting)
			if !strings.HasPrefix(run.Outcome.Reason, c.reason) {
				t.Fatalf("reason %q, want %q", run.Outcome.Reason, c.reason)
			}
			if drove != 0 || g.count(`SELECT count(*) FROM submissions`) != before || run.Correction {
				t.Fatalf("drove %d, correction %v", drove, run.Correction)
			}
			if l := stageOf(run, c.stage); l.Status != StatusFailed || !strings.HasPrefix(l.Detail, c.reason) && c.stage == StageSubmit {
				t.Fatalf("%s line %+v", c.stage, l)
			}
			if c.stage == StageSubmit && stageOf(run, StageResolve).Status != StatusOK {
				t.Fatalf("resolve line %+v", stageOf(run, StageResolve))
			}
		})
	}
}

// correctOnce records a correction of the posting's confirmed submission.
func correctOnce(t *testing.T, g *rig) {
	t.Helper()
	g.now = t0.Add(time.Hour)
	ctx := context.Background()
	cl, err := g.ledger.Claim(ctx, ledger.ClaimRequest{URL: pageURL, Source: ledger.SourceApplyChain, RunID: "run_earlier",
		Correction: &ledger.CorrectionRequest{Caller: gates.CallerAskAgent, SinglePosting: true, Message: correctionMsg}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ledger.Finish(ctx, cl.ID, ledger.Verdict{Landed: true, PressDispatched: true, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
}

func TestApply_Correction_ADryRunWithTheWordingIsNoCorrection(t *testing.T) {
	drove := 0
	g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { drove++; return nil } })
	g.env.window = nil
	confirmedSubmission(t, g)
	in := askAgentCorrection(Input{URL: pageURL}, correctionMsg)
	in.Submit = false
	run := g.r.RunOne(context.Background(), in)
	if run.Outcome.Kind != OutcomeFilled || drove != 1 || run.Correction || g.count(`SELECT count(*) FROM submissions`) != 1 {
		t.Fatalf("outcome %+v drove %d correction %v", run.Outcome, drove, run.Correction)
	}
}
