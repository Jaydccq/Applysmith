package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

type fakeApply struct {
	one    []wire.ApplyRunRequest
	lists  []wire.ApplyBatchRequest
	view   wire.ApplyRunView
	batchV wire.ApplyBatchView
	before []wire.ApplyRunView // answered before view
	// ahead queues the start; entries are the board entry answers, in turn.
	ahead   wire.SafeInt
	entries []wire.BoardEntry
	polls   int
}

func (f *fakeApply) runOne(_ context.Context, r wire.ApplyRunRequest) (wire.RunAccepted, error) {
	f.one = append(f.one, r)
	return wire.RunAccepted{RunID: "run_1", Ahead: f.ahead}, nil
}

func (f *fakeApply) entry(context.Context, string) (wire.RunView, error) {
	f.polls++
	e := f.entries[min(f.polls, len(f.entries))-1]
	return wire.RunView{RunID: "run_1", Entry: &e}, nil
}

func (f *fakeApply) runList(_ context.Context, r wire.ApplyBatchRequest) (wire.ApplyBatchAccepted, error) {
	f.lists = append(f.lists, r)
	return wire.ApplyBatchAccepted{RunID: "run_1", BatchID: "list_1", Rows: 1}, nil
}

func (f *fakeApply) run(context.Context, string) (wire.ApplyRunView, error) {
	if len(f.before) == 0 {
		return f.view, nil
	}
	v := f.before[0]
	f.before = f.before[1:]
	return v, nil
}

func (f *fakeApply) batch(context.Context, string) (wire.ApplyBatchView, error) { return f.batchV, nil }

func applyEnv(t *testing.T) (*env, *bytes.Buffer, *bytes.Buffer) {
	var out, errw bytes.Buffer
	setForTest(t, &applyPoll, time.Millisecond)
	return &env{stdout: &out, stderr: &errw}, &out, &errw
}

func finished() *wire.Time { t := wire.Time{Time: time.Now()}; return &t }

func TestApplyCommand_DefaultIsADryRunAndSubmitIsTheConfirmation(t *testing.T) {
	e, out, _ := applyEnv(t)
	f := &fakeApply{view: wire.ApplyRunView{RunID: "run_1", Mode: wire.ApplyModeDryRun, Host: "jobs.example.test", FinishedAt: finished(), Outcome: wire.ApplyOutcome{Kind: "filled"}}}
	if code := e.runApply(context.Background(), f, applyArgs{url: "https://jobs.example.test/apply/42"}, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if r := f.one[0]; r.Submit || r.Confirmed || !strings.Contains(out.String(), "Nothing was submitted") {
		t.Fatalf("request %+v, output %s", r, out)
	}
	f.view.Mode, f.view.Outcome = wire.ApplyModeSubmit, wire.ApplyOutcome{Kind: "submitted", Confirmed: true}
	if code := e.runApply(context.Background(), f, applyArgs{url: "https://jobs.example.test/apply/42", submit: true}, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if r := f.one[1]; !r.Submit || !r.Confirmed {
		t.Fatalf("a typed --submit must reach the server as a confirmed submit: %+v", r)
	}
}

func TestApplyCommand_AQueuedStartWaitsForTheWindowThenFollowsItsRun(t *testing.T) {
	queued := wire.BoardEntry{Stage: wire.BoardStageQueued, State: "working", Detail: "Queued (1 ahead): it starts when the apply window is free."}
	t.Run("the run takes the queued id", func(t *testing.T) {
		e, out, errw := applyEnv(t)
		f := &fakeApply{ahead: 1, entries: []wire.BoardEntry{queued, queued, {Stage: "preflight", State: "working"}},
			view: wire.ApplyRunView{RunID: "run_1", Mode: wire.ApplyModeDryRun, Host: "jobs.example.test", FinishedAt: finished(), Outcome: wire.ApplyOutcome{Kind: "filled"}}}
		if code := e.runApply(context.Background(), f, applyArgs{url: "https://jobs.example.test/apply/42"}, false); code != 0 {
			t.Fatalf("exit %d: %s", code, errw)
		}
		if !strings.Contains(errw.String(), "queued (1 ahead)") || !strings.Contains(out.String(), "filled") {
			t.Fatalf("stderr %q stdout %q", errw, out)
		}
	})
	t.Run("a start cancelled before it ran says so", func(t *testing.T) {
		e, _, errw := applyEnv(t)
		f := &fakeApply{ahead: 2, entries: []wire.BoardEntry{queued, {Stage: wire.BoardStageQueued, State: "done", Outcome: "cancelled", Detail: "Cancelled before it started."}}}
		if code := e.runApply(context.Background(), f, applyArgs{url: "https://jobs.example.test/apply/42"}, false); code == 0 || !strings.Contains(errw.String(), "Cancelled before it started") {
			t.Fatalf("exit %d, stderr %q", code, errw)
		}
	})
}

func TestApplyCommand_ResubmitWithAListIsAUsageErrorBeforeAnyServerContact(t *testing.T) {
	for _, args := range [][]string{
		{"--list", "apply-now", "--resubmit"},
		{"--list", "apply-now", "--resubmit", "--submit"},
		{"--list", "other-lane"},
		{"--list", "apply-now", "https://jobs.example.test/a"},
		{"https://jobs.example.test/a", "--resubmit"},
		{},
	} {
		e, _, errw := applyEnv(t)
		// A workspace that does not exist: a command that got as far as opening it would fail differently.
		if code := e.apply(context.Background(), append(args, "--workspace", "/nonexistent-workspace")); code != 2 || !strings.Contains(errw.String(), "usage: applysmith apply") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errw)
		}
	}
}

func TestApplyCommand_AListWaitsForTheBatchAndNamesWhoOwnsEachStop(t *testing.T) {
	e, out, _ := applyEnv(t)
	f := &fakeApply{batchV: wire.ApplyBatchView{State: wire.BatchClosed, Rows: []wire.ApplyBatchRow{
		{Index: 0, Company: "Example Corp", Host: "jobs.example.test", Outcome: &wire.ApplyOutcome{Kind: "stopped", Stage: "account", Class: "credential", Reason: "refused", Owner: wire.OwnerUser}},
		{Index: 1, Company: "Sample Labs", Host: "careers.example.test", Outcome: &wire.ApplyOutcome{Kind: "filled"}}},
		Owners: []wire.ApplyOwnerGroup{{Owner: wire.OwnerUser, Companies: []string{"Example Corp"}}}}}
	code := e.runApply(context.Background(), f, applyArgs{lanes: []string{wire.LaneApplyNow}, limit: 2}, false)
	if code != 1 || f.lists[0].Submit || f.lists[0].Limit != 2 {
		t.Fatalf("exit %d, request %+v", code, f.lists[0])
	}
	for _, want := range []string{"1. Example Corp", "stopped at account (credential)", "user: Example Corp", "2. Sample Labs"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestApplyCommand_ScorecardReadsTheDatabaseOnly(t *testing.T) {
	te := newTestEnv(t)
	te.addApplied("Alpha", daysAgo(1)) // creates the workspace database
	o := te.run(context.Background(), "apply", "scorecard", "--json", "--workspace", te.ws)
	if o.code != 0 || !strings.Contains(o.stdout, `"defectClusters"`) {
		t.Fatalf("exit %d: %s %s", o.code, o.stdout, o.stderr)
	}
	if o := te.run(context.Background(), "apply", "scorecard", "--days", "0", "--workspace", te.ws); o.code != 2 {
		t.Fatalf("a window of no days: exit %d", o.code)
	}
}

// The run id the server answers with is readable by the first poll, so the
// command reports how the run ended instead of "no application run is called".
func TestApplyCommand_AgainstARunningServerThePollFindsTheRunAtOnce(t *testing.T) {
	// A browser that cannot start keeps the run from opening any window; it
	// stops at its first stage.
	t.Setenv("APPLYSMITH_CHROME_BIN", filepath.Join(t.TempDir(), "no-chrome"))
	te := newTestEnv(t)
	te.startServe()
	setForTest(t, &applyPoll, time.Millisecond)
	o := te.run(context.Background(), "apply", "https://jobs.example.test/apply/42", "--workspace", te.ws)
	if strings.Contains(o.stderr, "no application run") || !strings.Contains(o.stdout, "jobs.example.test") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", o.code, o.stdout, o.stderr)
	}
}

// pollScript answers the polls of a run or batch one after another; the last
// answer repeats.
type pollScript struct {
	*fakeApply
	errs  []error
	polls int
}

func (p *pollScript) next() error {
	i := min(p.polls, len(p.errs)-1)
	p.polls++
	return p.errs[i]
}

func (p *pollScript) run(ctx context.Context, id string) (wire.ApplyRunView, error) {
	if err := p.next(); err != nil {
		return wire.ApplyRunView{}, err
	}
	return p.fakeApply.run(ctx, id)
}

func (p *pollScript) batch(ctx context.Context, id string) (wire.ApplyBatchView, error) {
	if err := p.next(); err != nil {
		return wire.ApplyBatchView{}, err
	}
	return p.fakeApply.batch(ctx, id)
}

var errNoAnswer = fmt.Errorf("%w: context deadline exceeded", errServerSilent)

func TestApplyCommand_AMissedPollIsAskedAgainOnTheNextTick(t *testing.T) {
	e, out, errw := applyEnv(t)
	f := &pollScript{fakeApply: &fakeApply{view: wire.ApplyRunView{RunID: "run_1", Host: "jobs.example.test", FinishedAt: finished(), Outcome: wire.ApplyOutcome{Kind: "filled"}}},
		errs: []error{errNoAnswer, errNoAnswer, nil}}
	if code := e.runApply(context.Background(), f, applyArgs{url: "https://jobs.example.test/apply/42"}, false); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errw)
	}
	if !strings.Contains(out.String(), "jobs.example.test") || strings.Count(errw.String(), "asking again") != 1 {
		t.Fatalf("stdout %q, stderr %q", out, errw)
	}
}

func TestApplyCommand_ABatchPollMissedOnceDoesNotAbortTheList(t *testing.T) {
	e, out, errw := applyEnv(t)
	f := &pollScript{fakeApply: &fakeApply{batchV: wire.ApplyBatchView{State: wire.BatchClosed, Rows: []wire.ApplyBatchRow{{Index: 0, Company: "Example Corp", Host: "jobs.example.test", Outcome: &wire.ApplyOutcome{Kind: "filled"}}}}},
		errs: []error{errNoAnswer, nil}}
	if code := e.runApply(context.Background(), f, applyArgs{lanes: []string{wire.LaneApplyNow}}, false); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errw)
	}
	if !strings.Contains(out.String(), "Example Corp") {
		t.Fatalf("stdout %q", out)
	}
}

func TestApplyCommand_ARefusedPollEndsTheWaitAtOnce(t *testing.T) {
	e, _, _ := applyEnv(t)
	f := &pollScript{fakeApply: &fakeApply{}, errs: []error{errors.New("the running server refused: no application run is called \"run_1\"")}}
	if _, err := e.awaitRun(context.Background(), f, "run_1"); err == nil || f.polls != 1 {
		t.Fatalf("err %v after %d polls", err, f.polls)
	}
}

func TestApplyCommand_AServerSilentForTooLongEndsTheWait(t *testing.T) {
	e, _, _ := applyEnv(t)
	old := pollDeafFor
	pollDeafFor = 30 * time.Millisecond
	t.Cleanup(func() { pollDeafFor = old })
	f := &pollScript{fakeApply: &fakeApply{}, errs: []error{errNoAnswer}}
	_, err := e.awaitRun(context.Background(), f, "run_1")
	if !errors.Is(err, errServerSilent) || f.polls < 2 {
		t.Fatalf("err %v after %d polls", err, f.polls)
	}
}
