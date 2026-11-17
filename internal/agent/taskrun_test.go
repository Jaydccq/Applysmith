// Clean-room: docs/provenance.md#CR-AGENT-TASKS

package agent

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/agent/tasks"
)

type taskRig struct {
	a        *Agent
	store    *tasks.Store
	session  string
	going    int
	cards    bool
	announce []tasks.Task
}

func newTaskRig(t *testing.T) *taskRig {
	t.Helper()
	f := newFixture(t)
	sess, err := f.sessions.Create(context.Background(), sessions.Dashboard, "")
	if err != nil {
		t.Fatal(err)
	}
	r := &taskRig{store: tasks.New(f.st, nil), session: sess.ID}
	r.a = New(Config{Sessions: f.sessions, Tasks: &TaskConfig{Store: r.store,
		RunsOf:       func(context.Context, string) ([]string, error) { return nil, nil },
		Going:        func(context.Context, string) (int, error) { return r.going, nil },
		CardsWaiting: func(context.Context, string) (bool, error) { return r.cards, nil },
		Announce:     func(_ context.Context, tk tasks.Task) { r.announce = append(r.announce, tk) }}})
	return r
}

// Where a task stands once a turn is over: the lifecycle the task panel and
// the loop rely on. A task waits for the runs its conversation started and
// for its cards, works on while its turns move it, asks the user once its own
// turns stop moving it or a turn fails, and a cancelled turn pauses it. The
// user hears of a question or an end only from the task's own turns: in their
// own turn they are watching.
func TestTasks_WhereATaskStandsAfterATurn(t *testing.T) {
	ctx := context.Background()
	final := TurnResult{Status: sessions.TurnFinal}
	for _, c := range []struct {
		name      string
		own       bool // the user typed the turn's message
		idle      int  // idle own turns before this one
		going     int
		cards     bool
		during    func(s *tasks.Store, id string) // what the turn's tools did
		res       TurnResult
		state     string
		asks      bool
		announced bool
	}{
		{name: "its runs are going", going: 1, res: final, state: tasks.WaitingRuns},
		{name: "a card waits", cards: true, res: final, state: tasks.WaitingYou},
		{name: "it moved", during: func(s *tasks.Store, id string) { _, _ = s.SetStep(ctx, id, 1, tasks.StepOK, "", nil) }, idle: 1, res: final, state: tasks.Working},
		{name: "a first own turn that moved nothing", res: final, state: tasks.Working},
		{name: "a second own turn that moved nothing", idle: 1, res: final, state: tasks.WaitingYou, asks: true, announced: true},
		{name: "the user's own turn moves it", own: true, idle: 1, res: final, state: tasks.Working},
		{name: "it asked", during: func(s *tasks.Store, id string) { _, _ = s.Ask(ctx, id, "Which city?") }, res: final, state: tasks.WaitingYou, asks: true, announced: true},
		{name: "it asked in the user's turn", own: true, during: func(s *tasks.Store, id string) { _, _ = s.Ask(ctx, id, "Which city?") }, res: final, state: tasks.WaitingYou, asks: true},
		{name: "it finished", during: func(s *tasks.Store, id string) { _, _ = s.Finish(ctx, id, "Applied to two.") }, res: final, state: tasks.Done, announced: true},
		{name: "the turn failed", res: TurnResult{Status: sessions.TurnError, Reason: pirpc.ReasonProvider, Detail: "the provider refused"}, state: tasks.WaitingYou, asks: true, announced: true},
		{name: "the user cancelled the turn", res: TurnResult{Status: sessions.TurnError, Reason: pirpc.ReasonCancelled}, state: tasks.Paused},
		{name: "the server stopped", res: TurnResult{Status: sessions.TurnError, Reason: pirpc.ReasonUnavailable, Detail: DetailServerStopped}, state: tasks.Working},
		{name: "pi could not start", res: TurnResult{Status: sessions.TurnError, Reason: pirpc.ReasonUnavailable, Detail: "pi did not start: no such file"}, state: tasks.WaitingYou, asks: true, announced: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newTaskRig(t)
			r.going, r.cards = c.going, c.cards
			tk, err := r.store.Create(ctx, tasks.NewTask{SessionID: r.session, Title: "Apply to the list", Mandate: "apply to my list", Steps: []tasks.Step{{Text: "Start the runs"}}})
			if err != nil {
				t.Fatal(err)
			}
			for range c.idle {
				if _, err := r.store.Settle(ctx, tk.ID, tasks.Working, "", false); err != nil {
					t.Fatal(err)
				}
			}
			turn := &Turn{ID: "turn_1", SessionID: r.session, Channel: ChannelDashboard, UserMessage: "apply to my list"}
			if c.own {
				err = r.a.beginOwnTurn(ctx, turn)
			} else {
				tk, err = r.store.Begin(ctx, tk.ID, false, 0)
				turn.Task, turn.Continues, turn.taskMark = tk.ID, true, tk.ProgressAt
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.during != nil {
				c.during(r.store, tk.ID)
			}
			if err := r.a.settleTask(ctx, turn, c.res); err != nil {
				t.Fatal(err)
			}
			got, err := r.store.Get(ctx, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.state || (got.Question != "") != c.asks || (len(r.announce) > 0) != c.announced {
				t.Fatalf("state %s, question %q, announced %d; want %s, asks %v, announced %v", got.State, got.Question, len(r.announce), c.state, c.asks, c.announced)
			}
			if c.asks && c.res.Detail != "" && !strings.Contains(got.Question, c.res.Detail) {
				t.Fatalf("the question does not say why the turn stopped: %q", got.Question)
			}
		})
	}
}

// A task that has taken MaxTurns turns by itself asks before it takes another.
func TestTasks_ATaskAsksBeforeItsTurnBudgetRunsOut(t *testing.T) {
	ctx := context.Background()
	r := newTaskRig(t)
	tk, err := r.store.Create(ctx, tasks.NewTask{SessionID: r.session, Title: "Apply to the list", Mandate: "apply to my list"})
	if err != nil {
		t.Fatal(err)
	}
	for range tasks.MaxTurns {
		if tk, err = r.store.Begin(ctx, tk.ID, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.a.advance(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, _ := r.store.Get(ctx, tk.ID)
	if got.State != tasks.WaitingYou || got.Question == "" || len(r.announce) != 1 {
		t.Fatalf("after %d turns: %s %q, announced %d", tasks.MaxTurns, got.State, got.Question, len(r.announce))
	}
}

// A "Let the agent finish" turn is bound to its run, not to the conversation's
// task: however it ends, the task stands where it stood.
func TestTasks_AFinishTurnsEndLeavesTheConversationsTaskAlone(t *testing.T) {
	ctx := context.Background()
	r := newTaskRig(t)
	tk, err := r.store.Create(ctx, tasks.NewTask{SessionID: r.session, Title: "Apply to the list", Mandate: "apply to my list"})
	if err != nil {
		t.Fatal(err)
	}
	turn := &Turn{ID: "turn_1", SessionID: r.session, Channel: ChannelDashboard, UserMessage: "finish the run", FinishRun: "run_1"}
	if err := r.a.beginOwnTurn(ctx, turn); err != nil || turn.Task != "" {
		t.Fatalf("the finish turn was bound to task %q, %v", turn.Task, err)
	}
	r.a.afterTurn(turn, TurnResult{Status: sessions.TurnError, Reason: pirpc.ReasonCancelled})
	got, err := r.store.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != tk.State || got.Question != "" || len(r.announce) != 0 {
		t.Fatalf("the task moved from %s to %s (question %q, announced %d)", tk.State, got.State, got.Question, len(r.announce))
	}
}
