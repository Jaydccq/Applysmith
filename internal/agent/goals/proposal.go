// Clean-room: docs/provenance.md#CR-AGENT-GOALS

package goals

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// Audit gate and codes of the proposal changes.
const (
	GateProposal gates.Gate = "goal_proposal"

	CodeProposed  = "goal_proposed"
	CodeReplaced  = "goal_proposal_replaced"
	CodeAccepted  = "goal_proposal_accepted"
	CodeDismissed = "goal_proposal_dismissed"
)

// Actor says who made a proposal decision, for the audit row.
type Actor string

// The callers that decide a proposal. The agent can only propose.
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

// Propose stores a plan change for the user to accept and changes nothing
// else. A goal has at most one pending proposal: a newer one replaces it.
func (s *Store) Propose(ctx context.Context, goalID, reason string, ch Change) (Proposal, error) {
	trim(&reason)
	if err := checkText("a reason", reason, MaxReasonChars, true); err != nil {
		return Proposal{}, err
	}
	for _, st := range ch.Steps {
		if st.Status == Done {
			return Proposal{}, invalid("a proposal revises the steps that are not done")
		}
	}
	if err := checkSteps(ch.Steps, false); err != nil {
		return Proposal{}, err
	}
	if ch.DueDate != nil {
		if err := checkDue(ch.DueDate); err != nil {
			return Proposal{}, err
		}
	}
	if ch.Cadence != nil {
		c, err := ch.Cadence.clean()
		if err != nil {
			return Proposal{}, err
		}
		ch.Cadence = &c
	}
	raw, err := json.Marshal(ch)
	if err != nil {
		return Proposal{}, err
	}
	p := Proposal{ID: store.NewID("gprop"), GoalID: goalID, Reason: reason, Change: ch, CreatedAt: s.now()}
	code := CodeProposed
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		g, err := s.openGoal(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if done, _ := g.Progress(); done+len(ch.Steps) > MaxSteps {
			return invalid("the done steps and the proposed ones are over %d", MaxSteps)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM agent_goal_proposals WHERE goal_id = ?`, goalID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			code = CodeReplaced
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_goal_proposals (id, goal_id, reason, change, created_at) VALUES (?,?,?,?,?)`, p.ID, goalID, reason, string(raw), stamp(p.CreatedAt))
		return err
	})
	if err != nil {
		return Proposal{}, err
	}
	s.record(ctx, code, goalID, p.ID, ActorAgent)
	return p, nil
}

// openGoal loads a goal that can still be planned: active or paused.
func (s *Store) openGoal(ctx context.Context, tx *sql.Tx, id string) (Goal, error) {
	list, err := s.load(ctx, tx, `WHERE id = ?`, id)
	if err != nil {
		return Goal{}, err
	}
	if len(list) == 0 {
		return Goal{}, ErrNotFound
	}
	if st := list[0].State; st != Active && st != Paused {
		return Goal{}, fmt.Errorf("%w: the goal is %s", ErrConflict, st)
	}
	return list[0], nil
}

// Accept applies a pending proposal on the user's word: the steps that are not
// done are replaced by the proposed ones, the other proposed fields are set,
// and the goal is a new version. Dashboard or CLI only: the agent has no path
// here, and the caller names who decided.
func (s *Store) Accept(ctx context.Context, goalID, proposalID string, by Actor) (Goal, error) {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		g, err := s.openGoal(ctx, tx, goalID)
		if err != nil {
			return err
		}
		if g.Proposal == nil || g.Proposal.ID != proposalID {
			return ErrNotFound
		}
		ch := g.Proposal.Change
		var steps []Step
		for _, st := range g.Steps {
			if st.Status == Done {
				steps = append(steps, st)
			}
		}
		if steps = append(steps, ch.Steps...); len(steps) > MaxSteps {
			return fmt.Errorf("%w: the goal now has %d done steps, so the proposed steps do not fit in %d", ErrConflict, len(steps)-len(ch.Steps), MaxSteps)
		}
		due, cad := g.DueDate, g.Cadence
		if ch.DueDate != nil {
			due = *ch.DueDate
		}
		if ch.Cadence != nil {
			cad = *ch.Cadence
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_goals SET due_date=?, checkin_days=?, checkin_time=?, version=version+1, updated_at=? WHERE id=?`,
			due, joinDays(cad), cad.Time, stamp(s.now()), goalID); err != nil {
			return err
		}
		if err := writeSteps(ctx, tx, goalID, steps); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM agent_goal_proposals WHERE id = ?`, proposalID)
		return err
	})
	if err != nil {
		return Goal{}, err
	}
	s.record(ctx, CodeAccepted, goalID, proposalID, by)
	return s.Get(ctx, goalID)
}

// Dismiss discards a pending proposal.
func (s *Store) Dismiss(ctx context.Context, goalID, proposalID string, by Actor) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM agent_goal_proposals WHERE id = ? AND goal_id = ?`, proposalID, goalID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err == nil {
		s.record(ctx, CodeDismissed, goalID, proposalID, by)
	}
	return err
}

// record writes the audit row of a proposal change. The change has already
// committed, so a failure is logged and not returned.
func (s *Store) record(ctx context.Context, code, goalID, proposalID string, by Actor) {
	if s.audit == nil {
		return
	}
	row := gates.NewAuditRow(s.now().UnixMilli(), gates.Decision{Gate: GateProposal, Verdict: gates.AllowWithTrace, Code: code,
		Reason: fmt.Sprintf("%s changed the proposal %s of goal %s", by, proposalID, goalID)}, gates.AuditContext{Caller: by.caller()})
	if err := s.audit.AppendDecision(context.WithoutCancel(ctx), row); err != nil && s.Log != nil {
		s.Log.Error("goals: audit write failed", "code", code, "error", err.Error())
	}
}
