// Package schedprop keeps the schedule changes the Ask Agent proposes. A
// proposal changes nothing: it is a row the user accepts or dismisses in the
// dashboard, and only the accept route applies it to the scheduler. The agent
// has a way to propose and none to accept.
package schedprop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// MaxReasonChars bounds the reason a proposal gives.
const MaxReasonChars = 300

// Audit gate and codes of the proposal changes.
const (
	Gate          gates.Gate = "schedule_proposal"
	CodeProposed             = "schedule_proposed"
	CodeReplaced             = "schedule_proposal_replaced"
	CodeAccepted             = "schedule_proposal_accepted"
	CodeDismissed            = "schedule_proposal_dismissed"
	CodeRestored             = "schedule_proposal_restored"
)

// Errors a caller branches on.
var (
	ErrNotFound = errors.New("schedprop: no such proposal")
	ErrInvalid  = errors.New("schedprop: invalid proposal")
	// ErrReplaced refuses to restore a proposal whose schedule has a newer one.
	ErrReplaced = errors.New("schedprop: a newer proposal replaced it")
	// ErrScheduleGone refuses to restore a proposal whose schedule was deleted.
	ErrScheduleGone = errors.New("schedprop: its schedule was deleted")
)

// Preset is the proposed cadence, in the scheduler's own terms.
type Preset struct {
	Kind     string   `json:"kind"`
	Time     string   `json:"time,omitempty"`
	Weekdays []string `json:"weekdays,omitempty"`
	RRule    string   `json:"rrule,omitempty"`
	Timezone string   `json:"timezone,omitempty"`
}

// Change is what the user would get on accepting: a new enabled flag, a new
// cadence, or both.
type Change struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Preset  *Preset `json:"preset,omitempty"`
}

func (c Change) empty() bool { return c.Enabled == nil && c.Preset == nil }

// Proposal is one pending change to one schedule.
type Proposal struct {
	ID         string
	ScheduleID int64
	Reason     string
	Change     Change
	CreatedAt  time.Time
}

// Actor says who decided a proposal; the agent only proposes.
type Actor string

// The callers that decide a proposal.
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

// Auditor receives the audit row of each change.
type Auditor interface {
	AppendDecision(ctx context.Context, r gates.AuditRow) error
}

// Store keeps the proposals over the workspace database.
type Store struct {
	db    *store.Store
	audit Auditor
	now   func() time.Time
	// Log receives audit-write failures; nil discards them.
	Log *slog.Logger

	mu sync.Mutex
	// dismissed is the proposal dismissed last, kept for its Undo, which the
	// dashboard offers right after the press. It is kept in memory alone: the
	// table has no state column, so a restart forgets it.
	dismissed Proposal
}

// New returns a store. A nil clock reads time.Now.
func New(db *store.Store, audit Auditor, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, audit: audit, now: now}
}

const layout = "2006-01-02T15:04:05.000Z"

// Propose stores a change for the user to accept and changes nothing else. A
// schedule has at most one pending proposal: a newer one replaces it.
func (s *Store) Propose(ctx context.Context, scheduleID int64, reason string, ch Change) (Proposal, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case scheduleID < 1:
		return Proposal{}, fmt.Errorf("%w: a proposal names a schedule", ErrInvalid)
	case reason == "":
		return Proposal{}, fmt.Errorf("%w: a proposal gives a reason", ErrInvalid)
	case utf8.RuneCountInString(reason) > MaxReasonChars:
		return Proposal{}, fmt.Errorf("%w: the reason holds at most %d characters", ErrInvalid, MaxReasonChars)
	case ch.empty():
		return Proposal{}, fmt.Errorf("%w: a proposal changes the enabled flag, the cadence or both", ErrInvalid)
	}
	raw, err := json.Marshal(ch)
	if err != nil {
		return Proposal{}, err
	}
	p := Proposal{ID: store.NewID("sprop"), ScheduleID: scheduleID, Reason: reason, Change: ch, CreatedAt: s.now()}
	code := CodeProposed
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM agent_schedule_proposals WHERE schedule_id = ?`, scheduleID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			code = CodeReplaced
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_schedule_proposals (id, schedule_id, reason, change, created_at) VALUES (?,?,?,?,?)`,
			p.ID, scheduleID, reason, string(raw), p.CreatedAt.UTC().Format(layout))
		return err
	})
	if err != nil {
		return Proposal{}, err
	}
	s.record(ctx, code, p.ID, scheduleID, ActorAgent)
	return p, nil
}

// List returns the pending proposals, newest first.
func (s *Store) List(ctx context.Context) ([]Proposal, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+columns+` FROM agent_schedule_proposals ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const columns = `id, schedule_id, reason, change, created_at`

func scanProposal(r interface{ Scan(...any) error }) (Proposal, error) {
	var p Proposal
	var raw, at string
	if err := r.Scan(&p.ID, &p.ScheduleID, &p.Reason, &raw, &at); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(raw), &p.Change); err != nil {
		return p, fmt.Errorf("schedprop: proposal %s: %w", p.ID, err)
	}
	p.CreatedAt, _ = time.Parse(layout, at)
	return p, nil
}

// Get returns one pending proposal.
func (s *Store) Get(ctx context.Context, id string) (Proposal, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Proposal{}, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	return Proposal{}, ErrNotFound
}

// Resolve ends a pending proposal: the user accepted it (the caller has
// already applied the change to the scheduler) or dismissed it. Dashboard or
// CLI only: the agent has no path here.
func (s *Store) Resolve(ctx context.Context, id string, accepted bool, by Actor) error {
	var p Proposal
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = scanProposal(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM agent_schedule_proposals WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM agent_schedule_proposals WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	code := CodeDismissed
	if accepted {
		code = CodeAccepted
	} else {
		s.mu.Lock()
		s.dismissed = p
		s.mu.Unlock()
	}
	s.record(ctx, code, id, p.ScheduleID, by)
	return nil
}

// Restore puts the proposal dismissed last back as it was, the undo of the
// dismissal. One whose schedule has a newer proposal since stays dismissed:
// a schedule has one pending proposal, and the newer one stands. One whose
// schedule was deleted since stays dismissed too, as the schedule's deletion
// removes its pending proposal.
func (s *Store) Restore(ctx context.Context, id string, by Actor) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.dismissed
	if id == "" || p.ID != id {
		return Proposal{}, ErrNotFound
	}
	raw, err := json.Marshal(p.Change)
	if err != nil {
		return Proposal{}, err
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_schedule_proposals WHERE schedule_id = ?`, p.ScheduleID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrReplaced
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO agent_schedule_proposals (id, schedule_id, reason, change, created_at)
			SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM schedules WHERE id = ?)`,
			p.ID, p.ScheduleID, p.Reason, string(raw), p.CreatedAt.UTC().Format(layout), p.ScheduleID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrScheduleGone
		}
		return nil
	})
	if err != nil {
		return Proposal{}, err
	}
	s.dismissed = Proposal{}
	s.record(ctx, CodeRestored, id, p.ScheduleID, by)
	return p, nil
}

// record writes the audit row of a change that has already committed.
func (s *Store) record(ctx context.Context, code, id string, scheduleID int64, by Actor) {
	if s.audit == nil {
		return
	}
	row := gates.NewAuditRow(s.now().UnixMilli(), gates.Decision{Gate: Gate, Verdict: gates.AllowWithTrace, Code: code,
		Reason: fmt.Sprintf("%s changed the proposal %s of schedule %d", by, id, scheduleID)}, gates.AuditContext{Caller: by.caller()})
	if err := s.audit.AppendDecision(context.WithoutCancel(ctx), row); err != nil && s.Log != nil {
		s.Log.Error("schedprop: audit write failed", "code", code, "error", err.Error())
	}
}
