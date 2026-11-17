//go:build browser

package apply

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
	"applysmith/internal/tracker"
)

// listRig is one rig over several mock boards: the rows of a list on the
// systems the chain names, each with its own company.
type listRig struct {
	*scenario
	rows   []ListRow
	boards []*board
}

func newListRig(t *testing.T, kinds ...string) *listRig {
	t.Helper()
	s := newScenario(t, kinds[0], modeAccept, Hooks{})
	l := &listRig{scenario: s}
	for i, kind := range kinds {
		b := s.b
		if i > 0 {
			b = s.site.add(&board{kind: kind, marker: fmt.Sprintf("j%d%07d", 4, boardSeq.Add(1))})
			b.setMode(modeAccept)
		}
		url := s.site.postingURL(b)
		company := []string{"Example Corp", "Quartz Dynamics", "Harbor Robotics", "Meridian Systems", "Tundra Analytics"}[i]
		role := "Platform Engineer"
		if i == 0 {
			// The scenario seeded its own board's row.
			company, role = "Example Corp", "Platform Engineer"
		} else if _, err := s.g.tracker.Add(context.Background(), tracker.AddRow{Company: company, Role: role, URL: url, Status: tracker.Evaluated}); err != nil {
			t.Fatal(err)
		}
		l.rows = append(l.rows, ListRow{URL: url, Company: company, Role: role})
		l.boards = append(l.boards, b)
	}
	return l
}

func (l *listRig) opts(submit bool) ListOptions {
	return ListOptions{Submit: submit, ExplicitRequest: submit, Caller: gates.CallerCLI, Tool: gates.ToolApplyFromList}
}

func (l *listRig) run(opt ListOptions) []ListResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return l.g.runner.RunList(ctx, l.rows, opt)
}

// brief is a result in one line; a whole run record would bury the failure.
func brief(r ListResult) string {
	if r.Run == nil {
		return "no run: " + r.Note
	}
	return fmt.Sprintf("%s at %s (%s): %s", r.Run.Outcome.Kind, r.Run.Outcome.Stage, r.Run.Outcome.Class, r.Run.Outcome.Reason)
}

func (l *listRig) presses() int {
	n := 0
	for _, b := range l.boards {
		n += b.presses()
	}
	return n
}

// attachCounter counts how often a list asks for the browser.
type attachCounter struct {
	e2eEnv
	n int
}

func (a *attachCounter) Attach(ctx context.Context) (Window, error) {
	a.n++
	return a.e2eEnv.Attach(ctx)
}

func TestE2E_List_DryRunFillsEveryRowOnOneAttachAndPressesNothing(t *testing.T) {
	l := newListRig(t, "greenhouse", "lever", "ashby", "workday")
	env := &attachCounter{e2eEnv: l.g.runner.d.Env.(e2eEnv)}
	l.g.runner.d.Env = env
	j := NewBatchJournal(l.g.st, nil)
	if err := j.Create(context.Background(), "list_e2e", BatchMeta{Caller: gates.CallerCLI, Tool: gates.ToolApplyFromList, Deadline: time.Now().Add(time.Hour)}, l.rows); err != nil {
		t.Fatal(err)
	}
	opt := l.opts(false)
	opt.BatchID, opt.Journal = "list_e2e", j.For("list_e2e")
	res := l.run(opt)
	for i, r := range res {
		if r.Run == nil || r.Run.Outcome.Kind != OutcomeFilled {
			if r.Run != nil {
				dump(t, r.Run)
			}
			t.Fatalf("row %d: %s", i, brief(r))
		}
	}
	if env.n != 1 {
		t.Fatalf("the browser was attached %d times for one list", env.n)
	}
	if l.presses() != 0 || l.g.count(t, `SELECT count(*) FROM submissions`) != 0 {
		t.Fatalf("a dry run pressed %d times and wrote %d ledger rows", l.presses(), l.g.count(t, `SELECT count(*) FROM submissions`))
	}
	if n := l.g.count(t, `SELECT count(*) FROM tracker_entries WHERE status <> 'evaluated'`); n != 0 {
		t.Fatalf("a dry run changed %d tracker rows", n)
	}
	if n := l.g.count(t, `SELECT count(*) FROM apply_batch_rows WHERE batch_id = 'list_e2e' AND state = 'ended' AND run_id <> ''`); n != 4 {
		t.Fatalf("%d journal rows ended with a run", n)
	}
	if n := l.g.count(t, `SELECT count(*) FROM apply_runs WHERE batch_id = 'list_e2e' AND submit = 0`); n != 4 {
		t.Fatalf("%d run records carry the batch id and the dry-run mode", n)
	}
}

func TestE2E_List_SubmitPressesEachRowOnceAndAnotherListSkipsAsDuplicates(t *testing.T) {
	l := newListRig(t, "greenhouse", "lever", "ashby")
	res := l.run(l.opts(true))
	for i, r := range res {
		if r.Run == nil || r.Run.Outcome.Kind != OutcomeSubmitted || !r.Run.Outcome.Confirmed {
			if r.Run != nil {
				dump(t, r.Run)
			}
			t.Fatalf("row %d: %s", i, brief(r))
		}
	}
	for i, b := range l.boards {
		if b.presses() != 1 || b.recorded() != 1 {
			t.Fatalf("board %d saw %d presses and recorded %d", i, b.presses(), b.recorded())
		}
	}
	// The same list again: the duplicate guard stops every row before the form.
	for i, r := range l.run(l.opts(true)) {
		if r.Run == nil {
			t.Fatalf("row %d of the second list: %s; the duplicate guard stops a run", i, brief(r))
		}
		mustStop(t, r.Run, StageResolve, ClassPosting)
	}
	for i, b := range l.boards {
		if b.presses() != 1 || b.recorded() != 1 {
			t.Fatalf("board %d saw %d presses and recorded %d after the second list", i, b.presses(), b.recorded())
		}
	}
}

// A live non-submit list runs against the real sites with the configured
// provider and may sign in, upload and save a draft; it creates no account,
// buys no captcha solve and presses nothing. The same list as a submitting run
// shows the harness would have noticed.
func TestE2E_List_LiveNonSubmitCreatesNoAccountBuysNoSolveAndPressesNothing(t *testing.T) {
	l := newListRig(t, "greenhouse", "icims", "generic")
	login := l.boards[2]
	login.accept = "Site-Pw-0!s"
	l.g.seedPassword(t, "signin-default", "signin_default", "", "Site-Pw-0!s")
	svc := newTwoCaptcha(t)
	l.g.runner.d.Page.Solver = func(e *Exec, audit func(gates.Decision)) captcha.Solver {
		return svc.solver(solveRunKind(e), gates.SkipNotListed)(e, audit)
	}
	res := l.run(l.opts(false))
	if res[0].Run == nil || res[0].Run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("the plain row: %s", brief(res[0]))
	}
	if res[1].Run == nil || res[1].Run.Outcome.Kind != OutcomeStopped || !strings.Contains(res[1].Run.Outcome.Reason, "bot-challenge") {
		t.Fatalf("the puzzle row must end for the user: %s", brief(res[1]))
	}
	if res[2].Run == nil || res[2].Run.Outcome.Kind != OutcomeFilled || login.pwTries.Load() != 1 {
		t.Fatalf("a live non-submit row may sign in: %s, %d passwords", brief(res[2]), login.pwTries.Load())
	}
	if svc.calls != 0 {
		t.Fatalf("a live non-submit list reached the captcha service %d times", svc.calls)
	}
	if n := l.g.count(t, `SELECT count(*) FROM signup_attempts`); n != 0 {
		t.Fatalf("%d account creations in a non-submit list", n)
	}
	if l.presses() != 0 || len(l.boards[1].stepPosts()) != 0 || l.g.count(t, `SELECT count(*) FROM submissions`) != 0 {
		t.Fatalf("a non-submit list pressed %d times", l.presses())
	}
	if n := l.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'captcha_solve' AND verdict = 'allow'`); n != 0 {
		t.Fatalf("%d solve decisions were allowed", n)
	}

	// The puzzle row as a submitting list does buy its solve.
	puzzle := &listRig{scenario: l.scenario, rows: l.rows[1:2], boards: l.boards[1:2]}
	if r := puzzle.run(puzzle.opts(true)); r[0].Run == nil || r[0].Run.Outcome.Kind != OutcomeSubmitted || svc.tasks != 1 {
		t.Fatalf("the submitting list: %s, %d solves", brief(r[0]), svc.tasks)
	}
}

// recordedModel stands for the configured provider: it sits behind the same
// seam as the routed model and remembers what it was asked.
type recordedModel struct {
	inner Model
	tasks []string
}

func (m *recordedModel) Resolve(ctx context.Context, task string) (llm.Resolved, error) {
	return m.inner.Resolve(ctx, task)
}

func (m *recordedModel) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	m.tasks = append(m.tasks, req.Task)
	return m.inner.Complete(ctx, req)
}

// A non-submit run reaches the model the process is configured with; an
// explicitly selected offline simulation routes every call to the fake
// provider, which makes no network or subprocess call.
func TestE2E_List_OfflineSimulationRoutesEveryCallToTheFakeProvider(t *testing.T) {
	l := newListRig(t, "ladder")
	m := newModelRig(t, l.g.st, script(`[{"id":1,"answer":"Go and SQL","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"))
	rec := &recordedModel{inner: m.router}
	l.g.runner.d.Page.Model = rec
	l.g.runner.d.Page.Own = func(context.Context) (Own, error) { return ladderOwn, nil }
	res := l.run(l.opts(false))
	if res[0].Run == nil || res[0].Run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("outcome: %s", brief(res[0]))
	}
	if len(rec.tasks) == 0 {
		t.Fatal("a non-submit run did not use the model it was configured with")
	}
	for _, task := range rec.tasks {
		r, err := m.router.Resolve(context.Background(), task)
		if err != nil || r.Provider != llm.OfflineProviderID {
			t.Fatalf("task %s resolved to %q (%v): offline simulation must answer every task from the fake provider", task, r.Provider, err)
		}
	}
	if m.count(taskComplete) == 0 {
		t.Fatal("the fake provider answered nothing")
	}
	if l.presses() != 0 {
		t.Fatal("a simulated non-submit list pressed Submit")
	}
}
