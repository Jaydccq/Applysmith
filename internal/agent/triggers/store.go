// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package triggers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"applysmith/internal/agent/goals"
	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// Audit gate and codes of the prompt state changes.
const (
	GatePrompt gates.Gate = "scheduled_prompt"

	CodeCreated   = "prompt_created"
	CodeProposed  = "prompt_proposed"
	CodeConfirmed = "prompt_confirmed"
	CodeEdited    = "prompt_edited"
	CodePaused    = "prompt_paused"
	CodeResumed   = "prompt_resumed"
	CodeDeleted   = "prompt_deleted"
)

// Actor says who changed a prompt, for the audit row. The agent can create a
// proposal, pause, resume and delete; confirming is the user's.
type Actor string

const (
	ActorAgent     Actor = "agent"
	ActorDashboard Actor = "dashboard"
	ActorCLI       Actor = "cli"
)

func (a Actor) caller() gates.Caller {
	switch a {
	case ActorAgent:
		return gates.CallerAskAgent
	case ActorCLI:
		return gates.CallerCLI
	}
	return gates.CallerDashboard
}

// Auditor receives the audit rows of prompt changes.
type Auditor interface {
	AppendDecision(ctx context.Context, r gates.AuditRow) error
}

// Store keeps the scheduled prompts and the firings of the triggers.
type Store struct {
	db    *store.Store
	audit Auditor
	now   func() time.Time
	loc   *time.Location
	// Log receives audit-write failures; nil discards them.
	Log *slog.Logger
}

// New returns a store. A nil clock reads time.Now and a nil zone is the
// machine's local one; the zone reads the cadence times.
func New(db *store.Store, audit Auditor, now func() time.Time, loc *time.Location) *Store {
	if now == nil {
		now = time.Now
	}
	if loc == nil {
		loc = time.Local
	}
	return &Store{db: db, audit: audit, now: now, loc: loc}
}

const layout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(layout) }

func parse(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, _ := time.Parse(layout, v)
	return t
}

const columns = `id, kind, text, state, origin, cadence_days, cadence_time, trigger_types, trigger_company, confirmed_at, active_since, last_slot,
	last_at, last_status, last_session_id, last_summary, created_at, updated_at`

func scan(r interface{ Scan(...any) error }) (Prompt, error) {
	var p Prompt
	var kind, state, origin, days, types, confirmed, since, slot, lastAt, lastStatus, created, updated string
	var last Last
	if err := r.Scan(&p.ID, &kind, &p.Text, &state, &origin, &days, &p.Cadence.Time, &types, &p.Trigger.Company, &confirmed, &since, &slot,
		&lastAt, &lastStatus, &last.SessionID, &last.Summary, &created, &updated); err != nil {
		return Prompt{}, err
	}
	p.Kind, p.State, p.Origin = Kind(kind), State(state), Origin(origin)
	if days != "" {
		p.Cadence.Days = strings.Split(days, ",")
	}
	if types != "" {
		p.Trigger.Types = strings.Split(types, ",")
	}
	p.ConfirmedAt, p.ActiveSince, p.LastSlot, p.CreatedAt, p.UpdatedAt = parse(confirmed), parse(since), parse(slot), parse(created), parse(updated)
	if lastStatus != "" {
		last.At, last.Status = parse(lastAt), lastStatus
		p.Last = &last
	}
	return p, nil
}

func (s *Store) query(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, where string, args ...any) ([]Prompt, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+columns+` FROM agent_prompts `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// List returns every prompt, oldest first; prompts created in the same
// millisecond keep the order they were written in (their ids are random).
func (s *Store) List(ctx context.Context) ([]Prompt, error) {
	return s.query(ctx, s.db.Read(), `ORDER BY created_at, rowid`)
}

// Get returns one prompt.
func (s *Store) Get(ctx context.Context, id string) (Prompt, error) {
	list, err := s.query(ctx, s.db.Read(), `WHERE id = ?`, id)
	if err != nil {
		return Prompt{}, err
	}
	if len(list) == 0 {
		return Prompt{}, ErrNotFound
	}
	return list[0], nil
}

// Create stores a prompt. A prompt the user creates is active at once; one
// the agent creates is only proposed and waits for Confirm. Ten prompts, of
// both kinds together, is the limit.
func (s *Store) Create(ctx context.Context, np NewPrompt, by Actor) (Prompt, error) {
	if err := clean(&np.Text); err != nil {
		return Prompt{}, err
	}
	p := Prompt{ID: store.NewID("prompt"), Text: np.Text, Origin: np.Origin, State: StateActive}
	switch {
	case !np.Cadence.Empty() && len(np.Trigger.Types) == 0 && np.Trigger.Company == "":
		cad, err := np.Cadence.Clean()
		if err != nil {
			return Prompt{}, cadenceError(err)
		}
		p.Kind, p.Cadence = KindCadence, cad
	case np.Cadence.Empty():
		if err := np.Trigger.clean(); err != nil {
			return Prompt{}, err
		}
		p.Kind, p.Trigger = KindTrigger, np.Trigger
	default:
		return Prompt{}, invalid("a prompt is on a cadence or on a trigger, not both")
	}
	if p.Origin == "" {
		p.Origin = OriginUser
	}
	now := s.now()
	p.CreatedAt, p.UpdatedAt = now, now
	if p.Origin == OriginAgent {
		p.State = StateProposed
	} else {
		p.ConfirmedAt, p.ActiveSince = now, now
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_prompts`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxPrompts {
			return ErrLimit
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_prompts (id, kind, text, state, origin, cadence_days, cadence_time, trigger_types, trigger_company,
			confirmed_at, active_since, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Kind, p.Text, p.State, p.Origin, strings.Join(p.Cadence.Days, ","), p.Cadence.Time, strings.Join(p.Trigger.Types, ","), p.Trigger.Company,
			opt(p.ConfirmedAt), opt(p.ActiveSince), stamp(now), stamp(now))
		return err
	})
	if err != nil {
		return Prompt{}, err
	}
	code := CodeCreated
	if p.State == StateProposed {
		code = CodeProposed
	}
	s.record(ctx, code, p.ID, by)
	return p, nil
}

func opt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return stamp(t)
}

// Confirm activates a proposed prompt on the user's word. No tool reaches it.
func (s *Store) Confirm(ctx context.Context, id string, by Actor) (Prompt, error) {
	now := s.now()
	err := s.transition(ctx, id, func(p Prompt) error {
		if p.State != StateProposed {
			return fmt.Errorf("%w: the prompt is %s", ErrConflict, p.State)
		}
		return nil
	}, `state = 'active', confirmed_at = ?, active_since = ?`, stamp(now), stamp(now))
	if err != nil {
		return Prompt{}, err
	}
	s.record(ctx, CodeConfirmed, id, by)
	return s.Get(ctx, id)
}

// Pause stops an active prompt.
func (s *Store) Pause(ctx context.Context, id string, by Actor) (Prompt, error) {
	err := s.transition(ctx, id, func(p Prompt) error {
		if p.State != StateActive {
			return fmt.Errorf("%w: the prompt is %s", ErrConflict, p.State)
		}
		return nil
	}, `state = 'paused'`)
	if err != nil {
		return Prompt{}, err
	}
	s.record(ctx, CodePaused, id, by)
	return s.Get(ctx, id)
}

// Resume restarts a paused prompt. What came due or arrived while it was
// paused never runs.
func (s *Store) Resume(ctx context.Context, id string, by Actor) (Prompt, error) {
	err := s.transition(ctx, id, func(p Prompt) error {
		if p.State != StatePaused {
			return fmt.Errorf("%w: the prompt is %s", ErrConflict, p.State)
		}
		return nil
	}, `state = 'active', active_since = ?`, stamp(s.now()))
	if err != nil {
		return Prompt{}, err
	}
	s.record(ctx, CodeResumed, id, by)
	return s.Get(ctx, id)
}

// transition applies a column change when check accepts the current row.
func (s *Store) transition(ctx context.Context, id string, check func(Prompt) error, set string, args ...any) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		list, err := s.query(ctx, tx, `WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return ErrNotFound
		}
		if err := check(list[0]); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_prompts SET `+set+`, updated_at = ? WHERE id = ?`, append(args, stamp(s.now()), id)...)
		return err
	})
}

// Update edits a prompt. A new schedule or trigger counts from now, so it
// never replays what came before the edit.
func (s *Store) Update(ctx context.Context, id string, pt Patch, by Actor) (Prompt, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Prompt{}, err
	}
	set, args := []string{}, []any{}
	if pt.Text != nil {
		text := *pt.Text
		if err := clean(&text); err != nil {
			return Prompt{}, err
		}
		set, args = append(set, "text = ?"), append(args, text)
	}
	reschedule := false
	switch {
	case pt.Cadence != nil && cur.Kind != KindCadence, pt.Trigger != nil && cur.Kind != KindTrigger:
		return Prompt{}, invalid("a %s prompt cannot take a %s", cur.Kind, map[Kind]Kind{KindCadence: KindTrigger, KindTrigger: KindCadence}[cur.Kind])
	case pt.Cadence != nil:
		cad, err := pt.Cadence.Clean()
		if err != nil {
			return Prompt{}, cadenceError(err)
		}
		if cad.Empty() {
			return Prompt{}, invalid("a cadence prompt needs weekdays and a time")
		}
		set, args, reschedule = append(set, "cadence_days = ?", "cadence_time = ?", "last_slot = ''"), append(args, strings.Join(cad.Days, ","), cad.Time), true
	case pt.Trigger != nil:
		tr := *pt.Trigger
		if err := tr.clean(); err != nil {
			return Prompt{}, err
		}
		set, args, reschedule = append(set, "trigger_types = ?", "trigger_company = ?"), append(args, strings.Join(tr.Types, ","), tr.Company), true
	}
	if len(set) == 0 {
		return cur, nil
	}
	if reschedule && cur.State == StateActive {
		set, args = append(set, "active_since = ?"), append(args, stamp(s.now()))
	}
	err = s.transition(ctx, id, func(Prompt) error { return nil }, strings.Join(set, ", "), args...)
	if err != nil {
		return Prompt{}, err
	}
	s.record(ctx, CodeEdited, id, by)
	return s.Get(ctx, id)
}

// Delete removes a prompt. The drafts of its past firings stay.
func (s *Store) Delete(ctx context.Context, id string, by Actor) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM agent_prompts WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err == nil {
		s.record(ctx, CodeDeleted, id, by)
	}
	return err
}

// record writes the audit row of a prompt change. The change has already
// committed, so a failure is logged and not returned.
func (s *Store) record(ctx context.Context, code, id string, by Actor) {
	if s.audit == nil {
		return
	}
	row := gates.NewAuditRow(s.now().UnixMilli(), gates.Decision{Gate: GatePrompt, Verdict: gates.AllowWithTrace, Code: code,
		Reason: fmt.Sprintf("%s changed the scheduled prompt %s", by, id)}, gates.AuditContext{Caller: by.caller()})
	if err := s.audit.AppendDecision(context.WithoutCancel(ctx), row); err != nil && s.Log != nil {
		s.Log.Error("triggers: audit write failed", "code", code, "error", err.Error())
	}
}

// NextSlot is the next time a cadence prompt is due, or zero.
func (s *Store) NextSlot(p Prompt) time.Time {
	if p.Kind != KindCadence || p.State != StateActive {
		return time.Time{}
	}
	return goals.NextSlot(p.Cadence, s.now(), s.loc)
}

// cadenceError presents the cadence check's refusal as this package's own.
func cadenceError(err error) error {
	return invalid("%s", strings.TrimPrefix(err.Error(), goals.ErrInvalid.Error()+": "))
}
