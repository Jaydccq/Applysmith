// Clean-room: docs/provenance.md#CR-AGENT-TASKS

// Package tasks keeps the Ask Agent's delegated tasks: an outcome the user
// handed to the agent in one conversation, which the agent works over several
// turns until it is done, it asks the user, or the user stops it. The store
// holds the record and its state; the agent package decides when a task's next
// turn starts. No gate reads a task: the guard reads its mandate, which is the
// user's own message.
package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/agent/memory"
	"applysmith/internal/store"
)

// Task states. An open task is working, waiting for its runs, waiting for the
// user or paused; done and stopped are closed.
const (
	Working     = "working"
	WaitingRuns = "waiting_runs"
	WaitingYou  = "waiting_you"
	Paused      = "paused"
	Done        = "done"
	Stopped     = "stopped"
)

// Step statuses.
const (
	Pending = "pending"
	Doing   = "doing"
	StepOK  = "done"
	Skipped = "skipped"
)

// Limits.
const (
	MaxOpen          = 3
	MaxSteps         = 12
	MaxTitleChars    = 120
	MaxStepChars     = 200
	MaxNoteChars     = 300
	MaxQuestionChars = 1000
	MaxOutcomeChars  = 2000
	// MaxTurns is how many turns a task starts by itself before it asks the
	// user whether to go on; Resume gives it as many again.
	MaxTurns = 20
	// MaxIdle is how many of its own turns in a row may move nothing before
	// the task asks the user.
	MaxIdle = 2
)

// Errors.
var (
	ErrNotFound = store.ErrNotFound
	ErrInvalid  = errors.New("tasks: invalid")
	// ErrRefused is text that reads as an instruction to the assistant.
	ErrRefused = errors.New("tasks: the text reads as an instruction to the assistant, so it was refused")
	// ErrOpen is a conversation, or an idea, that already has an open task.
	ErrOpen = errors.New("tasks: this conversation already has an open task; finish or stop it first")
	// ErrTooMany is a start while MaxOpen tasks are open.
	ErrTooMany = fmt.Errorf("tasks: %d tasks are already open; finish, stop or pause one first", MaxOpen)
	// ErrConflict is a change the task's state does not allow.
	ErrConflict = errors.New("tasks: the task is not in a state that allows this")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Step is one step of a task's plan.
type Step struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// Task is a delegated task.
type Task struct {
	ID        string
	SessionID string
	Title     string
	// Mandate is the user's own message that delegated the task.
	Mandate string
	// Idea is the Home item the task was handed over from, empty for a task
	// delegated in a conversation.
	Idea       string
	State      string
	Steps      []Step
	Question   string
	Outcome    string
	Turns      int
	Idle       int
	ProgressAt time.Time
	SeenNote   int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Open reports whether the task is neither done nor stopped.
func (t Task) Open() bool { return t.State != Done && t.State != Stopped }

// Progress counts the steps that are done or skipped, and all steps.
func (t Task) Progress() (done, total int) {
	for _, s := range t.Steps {
		if s.Status == StepOK || s.Status == Skipped {
			done++
		}
	}
	return done, len(t.Steps)
}

// Store keeps the tasks over the workspace database.
type Store struct {
	db  *store.Store
	now func() time.Time
}

// New returns a store; a nil clock reads time.Now.
func New(db *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

const layout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(layout) }

func parse(v string) time.Time { t, _ := time.Parse(layout, v); return t }

func chars(s string) int { return utf8.RuneCountInString(s) }

// checkText applies a limit and the memory injection blocklist to text the
// model wrote.
func checkText(what, s string, limit int, required bool) error {
	switch {
	case required && s == "":
		return invalid("%s needs some text", what)
	case chars(s) > limit:
		return invalid("%s is at most %d characters", what, limit)
	case memory.InjectionShaped(s):
		return ErrRefused
	}
	return nil
}

func checkSteps(steps []Step) error {
	if len(steps) > MaxSteps {
		return invalid("a task has at most %d steps", MaxSteps)
	}
	for i := range steps {
		s := &steps[i]
		s.Text, s.Note = strings.TrimSpace(s.Text), strings.TrimSpace(s.Note)
		if s.Status == "" {
			s.Status = Pending
		}
		if !slices.Contains([]string{Pending, Doing, StepOK, Skipped}, s.Status) {
			return invalid("%q is not a step status", s.Status)
		}
		if err := checkText("a step", s.Text, MaxStepChars, true); err != nil {
			return err
		}
		if err := checkText("a step note", s.Note, MaxNoteChars, false); err != nil {
			return err
		}
	}
	return nil
}

// NewTask is a task to start. One delegated in a conversation starts in the
// user's own turn; one handed over from an idea takes its first turn by itself.
type NewTask struct {
	SessionID string
	Idea      string
	Title     string
	Mandate   string
	Steps     []Step
	// WaitsForRuns starts the task waiting for runs its conversation already
	// started, so its first turn comes once they have ended.
	WaitsForRuns bool
}

// Create starts a task in a conversation.
func (s *Store) Create(ctx context.Context, n NewTask) (Task, error) {
	n.Title, n.Mandate = strings.TrimSpace(n.Title), strings.TrimSpace(n.Mandate)
	if err := checkText("a title", n.Title, MaxTitleChars, true); err != nil {
		return Task{}, err
	}
	if n.Mandate == "" {
		return Task{}, invalid("a task needs the user's message")
	}
	if err := checkSteps(n.Steps); err != nil {
		return Task{}, err
	}
	steps, err := json.Marshal(stepsOrEmpty(n.Steps))
	if err != nil {
		return Task{}, err
	}
	now := stamp(s.now())
	t := Task{ID: store.NewID("task"), SessionID: n.SessionID, Title: n.Title, Mandate: n.Mandate, Idea: n.Idea, State: Working, Steps: n.Steps}
	if n.WaitsForRuns {
		t.State = WaitingRuns
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var open, here int
		if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(session_id = ? OR (idea <> '' AND idea = ?)), 0) FROM agent_tasks WHERE state NOT IN ('done', 'stopped')`,
			n.SessionID, n.Idea).Scan(&open, &here); err != nil {
			return err
		}
		switch {
		case here > 0:
			return ErrOpen
		case open >= MaxOpen:
			return ErrTooMany
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_tasks (id, session_id, idea, title, mandate, state, steps, progress_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, t.ID, t.SessionID, t.Idea, t.Title, t.Mandate, t.State, string(steps), now, now, now)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.Get(ctx, t.ID)
}

func stepsOrEmpty(steps []Step) []Step {
	if steps == nil {
		return []Step{}
	}
	return steps
}

const columns = `id, session_id, idea, title, mandate, state, steps, question, outcome, turns, idle, progress_at, seen_note, created_at, updated_at`

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) load(ctx context.Context, q queryer, where string, args ...any) ([]Task, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+columns+` FROM agent_tasks `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var steps, progress, created, updated string
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Idea, &t.Title, &t.Mandate, &t.State, &steps, &t.Question, &t.Outcome, &t.Turns, &t.Idle,
			&progress, &t.SeenNote, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(steps), &t.Steps); err != nil {
			return nil, fmt.Errorf("tasks: steps of %s: %w", t.ID, err)
		}
		t.ProgressAt, t.CreatedAt, t.UpdatedAt = parse(progress), parse(created), parse(updated)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) one(ctx context.Context, q queryer, where string, args ...any) (Task, error) {
	list, err := s.load(ctx, q, where, args...)
	if err != nil {
		return Task{}, err
	}
	if len(list) == 0 {
		return Task{}, ErrNotFound
	}
	return list[0], nil
}

// Get reads a task.
func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	return s.one(ctx, s.db.Read(), `WHERE id = ?`, id)
}

// OpenIn reads the open task of a conversation.
func (s *Store) OpenIn(ctx context.Context, sessionID string) (Task, bool, error) {
	t, err := s.one(ctx, s.db.Read(), `WHERE session_id = ? AND state NOT IN ('done', 'stopped')`, sessionID)
	if errors.Is(err, ErrNotFound) {
		return Task{}, false, nil
	}
	return t, err == nil, err
}

// OpenIdea reads the open task handed over from an idea.
func (s *Store) OpenIdea(ctx context.Context, idea string) (Task, bool, error) {
	t, err := s.one(ctx, s.db.Read(), `WHERE idea = ? AND idea <> '' AND state NOT IN ('done', 'stopped')`, idea)
	if errors.Is(err, ErrNotFound) {
		return Task{}, false, nil
	}
	return t, err == nil, err
}

// closedShown is how many closed tasks List returns after the open ones.
const closedShown = 20

// List returns the open tasks and the latest closed ones, newest first.
func (s *Store) List(ctx context.Context) ([]Task, error) {
	open, err := s.load(ctx, s.db.Read(), `WHERE state NOT IN ('done', 'stopped') ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	closed, err := s.load(ctx, s.db.Read(), `WHERE state IN ('done', 'stopped') ORDER BY updated_at DESC LIMIT ?`, closedShown)
	return append(open, closed...), err
}

// Due returns the tasks whose next turn may be due: those working, those
// waiting for runs, and those waiting for the user on a card rather than on a
// question. The caller checks the runs and the cards.
func (s *Store) Due(ctx context.Context) ([]Task, error) {
	return s.load(ctx, s.db.Read(), `WHERE state IN ('working', 'waiting_runs') OR (state = 'waiting_you' AND question = '') ORDER BY updated_at`)
}

// update changes an open task inside one write: f checks and edits it.
func (s *Store) update(ctx context.Context, id string, f func(t *Task) error) (Task, error) {
	var out Task
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		t, err := s.one(ctx, tx, `WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if err := f(&t); err != nil {
			return err
		}
		steps, err := json.Marshal(stepsOrEmpty(t.Steps))
		if err != nil {
			return err
		}
		t.UpdatedAt = s.now()
		_, err = tx.ExecContext(ctx, `UPDATE agent_tasks SET title = ?, state = ?, steps = ?, question = ?, outcome = ?, turns = ?, idle = ?,
			progress_at = ?, seen_note = ?, updated_at = ? WHERE id = ?`,
			t.Title, t.State, string(steps), t.Question, t.Outcome, t.Turns, t.Idle, stamp(t.ProgressAt), t.SeenNote, stamp(t.UpdatedAt), t.ID)
		out = t
		return err
	})
	return out, err
}

// active is a state in which the agent works the task.
func active(state string) bool {
	return state == Working || state == WaitingRuns || state == WaitingYou
}

// Begin records that a turn of the task starts: the user's own message in its
// conversation, or a turn the task started (counted), which was given the
// notes up to seenNote. The task works again: a question is answered by the
// user's message, or the turn the task started takes it up again.
func (s *Store) Begin(ctx context.Context, id string, own bool, seenNote int64) (Task, error) {
	return s.update(ctx, id, func(t *Task) error {
		if !active(t.State) {
			return ErrConflict
		}
		t.State, t.Question = Working, ""
		if own {
			t.Idle = 0
		} else {
			t.Turns++
		}
		t.SeenNote = max(t.SeenNote, seenNote)
		return nil
	})
}

// SetStep changes one step, by its 1-based position, or adds steps at the end
// when position is 0; either is progress.
func (s *Store) SetStep(ctx context.Context, id string, position int, status, note string, add []Step) (Task, error) {
	if err := checkSteps(add); err != nil {
		return Task{}, err
	}
	note = strings.TrimSpace(note)
	if err := checkText("a step note", note, MaxNoteChars, false); err != nil {
		return Task{}, err
	}
	return s.update(ctx, id, func(t *Task) error {
		if !active(t.State) {
			return ErrConflict
		}
		switch {
		case position == 0 && len(add) == 0:
			return invalid("name a step's position or give steps to add")
		case position == 0:
			if len(t.Steps)+len(add) > MaxSteps {
				return invalid("a task has at most %d steps", MaxSteps)
			}
			t.Steps = append(t.Steps, add...)
		case position < 0 || position > len(t.Steps):
			return invalid("the task has no step %d", position)
		default:
			st := &t.Steps[position-1]
			if status != "" {
				if !slices.Contains([]string{Pending, Doing, StepOK, Skipped}, status) {
					return invalid("%q is not a step status", status)
				}
				st.Status = status
			}
			if note != "" {
				st.Note = note
			}
		}
		t.ProgressAt, t.Idle = s.now(), 0
		return nil
	})
}

// Ask records the question the task waits on.
func (s *Store) Ask(ctx context.Context, id, question string) (Task, error) {
	question = strings.TrimSpace(question)
	if err := checkText("a question", question, MaxQuestionChars, true); err != nil {
		return Task{}, err
	}
	return s.update(ctx, id, func(t *Task) error {
		if !active(t.State) {
			return ErrConflict
		}
		t.State, t.Question = WaitingYou, question
		return nil
	})
}

// Finish closes the task as done with what it achieved.
func (s *Store) Finish(ctx context.Context, id, outcome string) (Task, error) {
	outcome = strings.TrimSpace(outcome)
	if err := checkText("an outcome", outcome, MaxOutcomeChars, true); err != nil {
		return Task{}, err
	}
	return s.update(ctx, id, func(t *Task) error {
		if !active(t.State) {
			return ErrConflict
		}
		t.State, t.Question, t.Outcome = Done, "", outcome
		return nil
	})
}

// Settle records where a working task stands after a turn: the next state, the
// question it waits on (empty for none) and whether the turn moved it, which
// clears or counts its idle turns. A task the turn closed, paused or made wait
// on a question is left as it is.
func (s *Store) Settle(ctx context.Context, id, state, question string, moved bool) (Task, error) {
	return s.update(ctx, id, func(t *Task) error {
		if t.State != Working {
			return nil
		}
		t.State, t.Question = state, clip(question, MaxQuestionChars)
		if moved {
			t.Idle = 0
		} else {
			t.Idle++
		}
		return nil
	})
}

// Release lets a task that waited on runs or on a card work again.
func (s *Store) Release(ctx context.Context, id string) (Task, error) {
	return s.update(ctx, id, func(t *Task) error {
		if t.State != WaitingRuns && (t.State != WaitingYou || t.Question != "") {
			return ErrConflict
		}
		t.State = Working
		return nil
	})
}

// Control actions the user takes.
const (
	ActionPause  = "pause"
	ActionResume = "resume"
	ActionStop   = "stop"
)

// Control pauses, resumes or stops a task. Resume also answers a question with
// "go on" and gives the task a fresh budget of turns.
func (s *Store) Control(ctx context.Context, id, action string) (Task, error) {
	return s.update(ctx, id, func(t *Task) error {
		switch {
		case action == ActionPause && active(t.State):
			t.State = Paused
		case action == ActionResume && (t.State == Paused || t.State == WaitingYou):
			t.State, t.Question, t.Turns, t.Idle = Working, "", 0, 0
		case action == ActionStop && t.Open():
			t.State, t.Question = Stopped, ""
		default:
			return ErrConflict
		}
		return nil
	})
}

func clip(s string, n int) string {
	if chars(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
