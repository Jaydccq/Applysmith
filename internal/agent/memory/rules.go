package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"applysmith/internal/store"
)

// Source of a rule proposal.
const (
	RuleFromAgent         = "agent"
	RuleFromConsolidation = "consolidation"
)

func checkRule(body string) (string, error) {
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return "", invalidf("memory: a rule needs some text")
	case utf8.RuneCountInString(body) > MaxRuleChars:
		return "", invalidf("memory: a rule is at most %d characters", MaxRuleChars)
	case ruleInjection(body):
		return "", ErrRefused
	case GrantsPermission(body):
		return "", ErrRuleGrantsPermission
	}
	return body, nil
}

// Propose stores a rule as proposed. A proposal never reaches a prompt; the
// user activates it with ApproveRule.
func (s *Store) Propose(ctx context.Context, body, source string) (Rule, error) {
	body, err := checkRule(body)
	if err != nil {
		return Rule{}, err
	}
	if source != RuleFromAgent && source != RuleFromConsolidation {
		return Rule{}, fmt.Errorf("memory: %q is not a proposer", source)
	}
	now := s.now()
	r := Rule{ID: store.NewID("rule"), Body: body, State: RuleProposed, Source: source, CreatedAt: now, UpdatedAt: now}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var dup, pending int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_rules WHERE state <> 'retired' AND lower(body) = lower(?)`, body).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return fmt.Errorf("memory: that rule already exists")
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_rules WHERE state = 'proposed'`).Scan(&pending); err != nil {
			return err
		}
		if pending >= MaxPendingRule {
			return fmt.Errorf("%w: %d proposals already wait for the user", ErrRulesFull, MaxPendingRule)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_rules (id, body, state, source, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			r.ID, r.Body, r.State, r.Source, stamp(now), stamp(now))
		return err
	})
	return r, err
}

// Rules lists rules, optionally of one state, oldest first.
func (s *Store) Rules(ctx context.Context, state string) ([]Rule, error) {
	q, args := `SELECT id, body, state, source, created_at, updated_at FROM memory_rules`, []any{}
	if state != "" {
		q, args = q+` WHERE state = ?`, append(args, state)
	}
	rows, err := s.db.Read().QueryContext(ctx, q+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var c, u string
		if err := rows.Scan(&r.ID, &r.Body, &r.State, &r.Source, &c, &u); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = parse(c), parse(u)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) setState(ctx context.Context, id, from, to string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if to == RuleActive {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_rules WHERE state = 'active'`).Scan(&n); err != nil {
				return err
			}
			if n >= MaxActiveRules {
				return fmt.Errorf("%w: %d rules are active; retire one first", ErrRulesFull, MaxActiveRules)
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE memory_rules SET state = ?, updated_at = ? WHERE id = ? AND state = ?`, to, stamp(s.now()), id, from)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: no %s rule %s", store.ErrNotFound, from, id)
		}
		return nil
	})
}

// ApproveRule activates a proposed rule. It is the user's act, from the
// dashboard or a Telegram button; no tool calls it.
func (s *Store) ApproveRule(ctx context.Context, id string) error {
	return s.setState(ctx, id, RuleProposed, RuleActive)
}

// RetireRule takes an active rule out of the prompt and keeps it.
func (s *Store) RetireRule(ctx context.Context, id string) error {
	return s.setState(ctx, id, RuleActive, RuleRetired)
}

// EditRule changes a rule's text under the same screening as a proposal.
func (s *Store) EditRule(ctx context.Context, id, body string) error {
	body, err := checkRule(body)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE memory_rules SET body = ?, updated_at = ? WHERE id = ?`, body, stamp(s.now()), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: no rule %s", store.ErrNotFound, id)
		}
		return nil
	})
}

// DeleteRule removes a rule in any state.
func (s *Store) DeleteRule(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM memory_rules WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: no rule %s", store.ErrNotFound, id)
		}
		return nil
	})
}
