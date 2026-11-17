// Clean-room: docs/provenance.md#CR-AGENT-GOALS

package goals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// Auditor receives the audit rows of proposal changes; the conversation
// record's store is one.
type Auditor interface {
	AppendDecision(ctx context.Context, r gates.AuditRow) error
}

// Store keeps the goals over the workspace database. loc is the zone of the
// due dates and check-in times.
type Store struct {
	db    *store.Store
	audit Auditor
	now   func() time.Time
	loc   *time.Location
	// Log receives audit-write failures; nil discards them.
	Log *slog.Logger
}

// New returns a store. A nil clock reads time.Now and a nil zone is the
// machine's local one.
func New(db *store.Store, audit Auditor, now func() time.Time, loc *time.Location) *Store {
	if now == nil {
		now = time.Now
	}
	if loc == nil {
		loc = time.Local
	}
	return &Store{db: db, audit: audit, now: now, loc: loc}
}

// Zone is where due dates end and check-in times are read.
func (s *Store) Zone() *time.Location { return s.loc }

const layout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(layout) }

func parse(v string) time.Time { t, _ := time.Parse(layout, v); return t }

func parseOrZero(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	return parse(v)
}

// NewGoal is a goal to create. A created goal is active.
type NewGoal struct {
	Title   string
	DueDate string
	Steps   []Step
	Cadence Cadence
}

// Create stores an active goal. Five active goals is the limit.
func (s *Store) Create(ctx context.Context, ng NewGoal) (Goal, error) {
	if err := checkTitle(&ng.Title); err != nil {
		return Goal{}, err
	}
	if err := checkDue(&ng.DueDate); err != nil {
		return Goal{}, err
	}
	if err := checkSteps(ng.Steps, true); err != nil {
		return Goal{}, err
	}
	cad, err := ng.Cadence.clean()
	if err != nil {
		return Goal{}, err
	}
	now := s.now()
	g := Goal{ID: store.NewID("goal"), Title: ng.Title, DueDate: ng.DueDate, State: Active, Version: 1, Cadence: cad, Steps: ng.Steps,
		ActiveSince: now, CreatedAt: now, UpdatedAt: now}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if n, err := countActive(ctx, tx); err != nil || n >= MaxActive {
			if err == nil {
				err = ErrTooManyActive
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_goals (id, title, due_date, state, version, checkin_days, checkin_time, active_since, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, g.ID, g.Title, g.DueDate, g.State, g.Version, strings.Join(cad.Days, ","), cad.Time, stamp(now), stamp(now), stamp(now)); err != nil {
			return err
		}
		return writeSteps(ctx, tx, g.ID, g.Steps)
	})
	return g, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func countActive(ctx context.Context, q queryer) (int, error) {
	var n int
	return n, q.QueryRowContext(ctx, `SELECT count(*) FROM agent_goals WHERE state = 'active'`).Scan(&n)
}

func writeSteps(ctx context.Context, q queryer, goalID string, steps []Step) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM agent_goal_steps WHERE goal_id = ?`, goalID); err != nil {
		return err
	}
	for i, st := range steps {
		if _, err := q.ExecContext(ctx, `INSERT INTO agent_goal_steps (goal_id, position, text, status, note, application_id, company) VALUES (?,?,?,?,?,?,?)`,
			goalID, i+1, st.Text, st.Status, st.Note, st.ApplicationID, st.Company); err != nil {
			return err
		}
	}
	return nil
}

const goalColumns = `id, title, due_date, state, version, checkin_days, checkin_time, active_since, last_slot, created_at, updated_at`

func scanGoal(r interface{ Scan(...any) error }) (Goal, error) {
	var g Goal
	var days, since, slot, created, updated string
	if err := r.Scan(&g.ID, &g.Title, &g.DueDate, &g.State, &g.Version, &days, &g.Cadence.Time, &since, &slot, &created, &updated); err != nil {
		return Goal{}, err
	}
	if days != "" {
		g.Cadence.Days = strings.Split(days, ",")
	}
	g.ActiveSince, g.LastSlot, g.CreatedAt, g.UpdatedAt = parse(since), parseOrZero(slot), parse(created), parse(updated)
	return g, nil
}

// fill loads the steps and the pending proposal of each goal.
func fill(ctx context.Context, q queryer, list []Goal) error {
	for i := range list {
		g := &list[i]
		rows, err := q.QueryContext(ctx, `SELECT text, status, note, application_id, company FROM agent_goal_steps WHERE goal_id = ? ORDER BY position`, g.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st Step
			if err := rows.Scan(&st.Text, &st.Status, &st.Note, &st.ApplicationID, &st.Company); err != nil {
				_ = rows.Close()
				return err
			}
			g.Steps = append(g.Steps, st)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		var p Proposal
		var change, created string
		err = q.QueryRowContext(ctx, `SELECT id, goal_id, reason, change, created_at FROM agent_goal_proposals WHERE goal_id = ?`, g.ID).
			Scan(&p.ID, &p.GoalID, &p.Reason, &change, &created)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			if err := json.Unmarshal([]byte(change), &p.Change); err != nil {
				return err
			}
			p.CreatedAt = parse(created)
			g.Proposal = &p
		}
	}
	return nil
}

func (s *Store) load(ctx context.Context, q queryer, where string, args ...any) ([]Goal, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+goalColumns+` FROM agent_goals `+where, args...)
	if err != nil {
		return nil, err
	}
	var out []Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, fill(ctx, q, out)
}

// Get returns one goal with its steps and pending proposal.
func (s *Store) Get(ctx context.Context, id string) (Goal, error) {
	list, err := s.load(ctx, s.db.Read(), `WHERE id = ?`, id)
	if err != nil {
		return Goal{}, err
	}
	if len(list) == 0 {
		return Goal{}, ErrNotFound
	}
	return list[0], nil
}

// List returns every goal, active ones first, then by due date and age.
func (s *Store) List(ctx context.Context) ([]Goal, error) {
	return s.load(ctx, s.db.Read(), `ORDER BY state <> 'active', due_date = '', due_date, created_at, id`)
}

// ListActive returns the active goals in the same order.
func (s *Store) ListActive(ctx context.Context) ([]Goal, error) {
	return s.load(ctx, s.db.Read(), `WHERE state = 'active' ORDER BY due_date = '', due_date, created_at, id`)
}

// Patch is the user's edit of a goal; a nil field is left alone. Changing the
// title, the due date, the cadence or the steps is a new version of the plan.
type Patch struct {
	Title   *string
	DueDate *string
	Cadence *Cadence
	State   *string
	Steps   *[]Step
}

// Update applies the user's edit.
func (s *Store) Update(ctx context.Context, id string, p Patch) (Goal, error) {
	if p.Title != nil {
		if err := checkTitle(p.Title); err != nil {
			return Goal{}, err
		}
	}
	if p.DueDate != nil {
		if err := checkDue(p.DueDate); err != nil {
			return Goal{}, err
		}
	}
	var cad Cadence
	if p.Cadence != nil {
		var err error
		if cad, err = p.Cadence.clean(); err != nil {
			return Goal{}, err
		}
	}
	if p.Steps != nil {
		if err := checkSteps(*p.Steps, true); err != nil {
			return Goal{}, err
		}
	}
	if p.State != nil && !validState(*p.State) {
		return Goal{}, invalid("%q is not a goal state", *p.State)
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		list, err := s.load(ctx, tx, `WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return ErrNotFound
		}
		g := list[0]
		plan := false
		if p.Title != nil && *p.Title != g.Title {
			g.Title, plan = *p.Title, true
		}
		if p.DueDate != nil && *p.DueDate != g.DueDate {
			g.DueDate, plan = *p.DueDate, true
		}
		if p.Cadence != nil {
			g.Cadence, plan = cad, true
		}
		if p.Steps != nil {
			g.Steps, plan = *p.Steps, true
		}
		now := s.now()
		if p.State != nil && *p.State != g.State {
			if *p.State == Active {
				if n, err := countActive(ctx, tx); err != nil || n >= MaxActive {
					if err == nil {
						err = ErrTooManyActive
					}
					return err
				}
				g.ActiveSince = now
			}
			g.State = *p.State
		}
		if plan {
			g.Version++
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_goals SET title=?, due_date=?, state=?, version=?, checkin_days=?, checkin_time=?, active_since=?, updated_at=? WHERE id=?`,
			g.Title, g.DueDate, g.State, g.Version, strings.Join(g.Cadence.Days, ","), g.Cadence.Time, stamp(g.ActiveSince), stamp(now), g.ID); err != nil {
			return err
		}
		if p.Steps != nil {
			return writeSteps(ctx, tx, g.ID, g.Steps)
		}
		return nil
	})
	if err != nil {
		return Goal{}, err
	}
	return s.Get(ctx, id)
}

func joinDays(c Cadence) string { return strings.Join(c.Days, ",") }

func validState(v string) bool { return v == Active || v == Paused || v == Completed || v == Archived }

// Delete removes a goal with its steps and proposal.
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM agent_goals WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SetStep changes the status or the note of one step, by its 1-based
// position. Besides a state change and a deletion, it is the only change the
// agent may make to an existing goal, and it is not a new version of the plan.
func (s *Store) SetStep(ctx context.Context, goalID string, position int, status, note *string) (Goal, error) {
	if status != nil && !(*status == Pending || *status == InProgress || *status == Done || *status == Blocked) {
		return Goal{}, invalid("%q is not a step status", *status)
	}
	if note != nil {
		trim(note)
		if err := checkText("a step note", *note, MaxNoteChars, false); err != nil {
			return Goal{}, err
		}
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM agent_goals WHERE id = ?`, goalID).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if state != Active && state != Paused {
			return ErrConflict
		}
		var cur Step
		err := tx.QueryRowContext(ctx, `SELECT status, note FROM agent_goal_steps WHERE goal_id = ? AND position = ?`, goalID, position).Scan(&cur.Status, &cur.Note)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if status != nil {
			cur.Status = *status
		}
		if note != nil {
			cur.Note = *note
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_goal_steps SET status = ?, note = ? WHERE goal_id = ? AND position = ?`, cur.Status, cur.Note, goalID, position); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_goals SET updated_at = ? WHERE id = ?`, stamp(s.now()), goalID)
		return err
	})
	if err != nil {
		return Goal{}, err
	}
	return s.Get(ctx, goalID)
}
