// Clean-room: docs/provenance.md#CR-AGENT-TASKS

package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/store"
)

type rig struct {
	s   *Store
	ss  *sessions.Store
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := &rig{now: time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return r.now }
	r.s, r.ss = New(db, clock), sessions.New(db, clock)
	return r
}

func (r *rig) conversation(t *testing.T) string {
	t.Helper()
	sess, err := r.ss.Create(context.Background(), sessions.Dashboard, "")
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func (r *rig) start(t *testing.T, session string) Task {
	t.Helper()
	tk, err := r.s.Create(context.Background(), NewTask{SessionID: session, Title: "Apply to the strong matches", Mandate: "apply to every 4.0+ posting",
		Steps: []Step{{Text: "Read the apply list"}, {Text: "Start the runs"}}})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// A conversation holds one open task and the workspace MaxOpen, so the agent
// cannot pile up work that continues by itself.
func TestTasks_OneOpenTaskPerConversationAndAtMostMaxOpen(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	first := r.conversation(t)
	tk := r.start(t, first)
	if _, err := r.s.Create(ctx, NewTask{SessionID: first, Title: "Another", Mandate: "x"}); !errors.Is(err, ErrOpen) {
		t.Fatalf("a second open task in one conversation: %v", err)
	}
	for i := 1; i < MaxOpen; i++ {
		r.start(t, r.conversation(t))
	}
	if _, err := r.s.Create(ctx, NewTask{SessionID: r.conversation(t), Title: "One too many", Mandate: "x"}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("task %d: %v", MaxOpen+1, err)
	}
	if _, err := r.s.Control(ctx, tk.ID, ActionStop); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Create(ctx, NewTask{SessionID: first, Title: "Next", Mandate: "x"}); err != nil {
		t.Fatalf("a stopped task frees its conversation and a slot: %v", err)
	}
}

// An idea holds one open task, whichever conversation a second press would
// open, and finds it; a task delegated in a conversation belongs to no idea.
func TestTasks_OneOpenTaskPerIdea(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.start(t, r.conversation(t))
	if _, ok, err := r.s.OpenIdea(ctx, ""); ok || err != nil {
		t.Fatalf("a typed task found as an idea's: %v %v", ok, err)
	}
	inbox := NewTask{Idea: "evaluate_inbox", Title: "Evaluate the inbox", Mandate: "Evaluate the 3 newest postings."}
	inbox.SessionID = r.conversation(t)
	tk, err := r.s.Create(ctx, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := r.s.OpenIdea(ctx, "evaluate_inbox"); !ok || err != nil || got.ID != tk.ID {
		t.Fatalf("open idea = %+v %v %v", got, ok, err)
	}
	inbox.SessionID = r.conversation(t)
	if _, err := r.s.Create(ctx, inbox); !errors.Is(err, ErrOpen) {
		t.Fatalf("a second open task for one idea: %v", err)
	}
	if _, err := r.s.Control(ctx, tk.ID, ActionStop); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Create(ctx, inbox); err != nil {
		t.Fatalf("a stopped task frees its idea: %v", err)
	}
}

// The user's controls, from every state: the transitions the panel offers and
// the refusals it must show.
func TestTasks_ControlTransitions(t *testing.T) {
	ctx := context.Background()
	reach := map[string]func(r *rig, id string) error{
		Working:     func(*rig, string) error { return nil },
		WaitingRuns: func(r *rig, id string) error { _, err := r.s.Settle(ctx, id, WaitingRuns, "", true); return err },
		WaitingYou:  func(r *rig, id string) error { _, err := r.s.Ask(ctx, id, "Which city?"); return err },
		Paused:      func(r *rig, id string) error { _, err := r.s.Control(ctx, id, ActionPause); return err },
		Done:        func(r *rig, id string) error { _, err := r.s.Finish(ctx, id, "Applied to three."); return err },
		Stopped:     func(r *rig, id string) error { _, err := r.s.Control(ctx, id, ActionStop); return err },
	}
	want := map[string]map[string]string{ // from state, action -> new state; "" refused
		Working:     {ActionPause: Paused, ActionResume: "", ActionStop: Stopped},
		WaitingRuns: {ActionPause: Paused, ActionResume: "", ActionStop: Stopped},
		WaitingYou:  {ActionPause: Paused, ActionResume: Working, ActionStop: Stopped},
		Paused:      {ActionPause: "", ActionResume: Working, ActionStop: Stopped},
		Done:        {ActionPause: "", ActionResume: "", ActionStop: ""},
		Stopped:     {ActionPause: "", ActionResume: "", ActionStop: ""},
	}
	for from, actions := range want {
		for action, to := range actions {
			r := newRig(t)
			tk := r.start(t, r.conversation(t))
			if err := reach[from](r, tk.ID); err != nil {
				t.Fatalf("reach %s: %v", from, err)
			}
			got, err := r.s.Control(ctx, tk.ID, action)
			switch {
			case to == "" && !errors.Is(err, ErrConflict):
				t.Errorf("%s from %s: %v, want refused", action, from, err)
			case to != "" && (err != nil || got.State != to):
				t.Errorf("%s from %s: %s %v, want %s", action, from, got.State, err, to)
			case to == Working && (got.Question != "" || got.Turns != 0):
				t.Errorf("resume from %s keeps question %q and %d turns", from, got.Question, got.Turns)
			}
		}
	}
}

// Due names the tasks whose next turn may start; a task waiting on a question,
// paused or closed is never taken up by itself.
func TestTasks_DueLeavesQuestionsPausedAndClosedTasks(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		to   func(s *Store, id string) (Task, error)
		due  bool
	}{
		{"working", func(*Store, string) (Task, error) { return Task{}, nil }, true},
		{"waiting for runs", func(s *Store, id string) (Task, error) { return s.Settle(ctx, id, WaitingRuns, "", true) }, true},
		{"waiting on a card", func(s *Store, id string) (Task, error) { return s.Settle(ctx, id, WaitingYou, "", true) }, true},
		{"waiting on a question", func(s *Store, id string) (Task, error) { return s.Ask(ctx, id, "Which city?") }, false},
		{"paused", func(s *Store, id string) (Task, error) { return s.Control(ctx, id, ActionPause) }, false},
		{"done", func(s *Store, id string) (Task, error) { return s.Finish(ctx, id, "Applied to three.") }, false},
		{"stopped", func(s *Store, id string) (Task, error) { return s.Control(ctx, id, ActionStop) }, false},
	} {
		r := newRig(t)
		tk := r.start(t, r.conversation(t))
		if _, err := c.to(r.s, tk.ID); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		due, err := r.s.Due(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(due) == 1; got != c.due {
			t.Errorf("%s: due %v, want %v", c.name, got, c.due)
		}
	}
}

// A task asks the user once its own turns stop moving it or run out; the
// counts behind that are kept here: only the task's own turns are counted, and
// the user's message or any progress clears the idle count.
func TestTasks_TurnsAndIdleAreCountedForTheTasksOwnTurns(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	tk := r.start(t, r.conversation(t))
	step := func(name string, f func() (Task, error), turns, idle int) {
		t.Helper()
		got, err := f()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Turns != turns || got.Idle != idle {
			t.Fatalf("%s: turns %d idle %d, want %d and %d", name, got.Turns, got.Idle, turns, idle)
		}
	}
	step("own turn", func() (Task, error) { return r.s.Begin(ctx, tk.ID, false, 7) }, 1, 0)
	step("it moved nothing", func() (Task, error) { return r.s.Settle(ctx, tk.ID, Working, "", false) }, 1, 1)
	step("own turn", func() (Task, error) { return r.s.Begin(ctx, tk.ID, false, 3) }, 2, 1)
	step("a step changes", func() (Task, error) { return r.s.SetStep(ctx, tk.ID, 1, StepOK, "", nil) }, 2, 0)
	step("it moved nothing", func() (Task, error) { return r.s.Settle(ctx, tk.ID, Working, "", false) }, 2, 1)
	step("the user's message", func() (Task, error) { return r.s.Begin(ctx, tk.ID, true, 0) }, 2, 0)
	got, err := r.s.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SeenNote != 7 {
		t.Fatalf("the notes a turn was given never go back: %d", got.SeenNote)
	}
	if _, err := r.s.Ask(ctx, tk.ID, "Which city?"); err != nil {
		t.Fatal(err)
	}
	if got, err = r.s.Settle(ctx, tk.ID, Working, "", false); err != nil || got.State != WaitingYou || got.Question == "" {
		t.Fatalf("a turn's end leaves the question it asked: %s %q %v", got.State, got.Question, err)
	}
}
