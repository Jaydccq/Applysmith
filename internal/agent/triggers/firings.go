// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package triggers

import (
	"context"
	"database/sql"
	"strconv"
	"time"
	"unicode/utf8"

	"applysmith/internal/agent/goals"
)

// Time rules of the firing.
const (
	// SlotWindow is how long a cadence slot stays due, so a machine that was
	// asleep at the slot still runs it on waking.
	SlotWindow = 3 * time.Hour
	// MailWindow is how old a message may be and still fire a trigger.
	MailWindow = 24 * time.Hour
	// PerTick is how many firings start in one tick.
	PerTick = 5
	// FiringRetention is how long a firing row is kept.
	FiringRetention = 90 * 24 * time.Hour

	highWaterKey = "agent.triggers.high_water"
	maxSummary   = 300
)

// Firing states.
const (
	FiringClaimed  = "claimed"
	FiringSkipped  = "skipped"
	FiringAnswered = "answered"
	FiringFailed   = "failed"
	FiringTimedOut = "timed_out"
)

// Firing is the record of one trigger and one Gmail message.
type Firing struct {
	PromptID       string
	MessageID      string
	Seq            int64
	SignalType     string
	Employer       string
	State          string
	Detail         string
	SessionID      string
	Draft          string
	ProposedStatus string
	ProposedReason string
	ClaimedAt      time.Time
	FinishedAt     time.Time
}

// DueCadence claims and returns the cadence prompts whose slot is due at now.
// Only the latest slot of a prompt is due, only inside SlotWindow, never one
// before the prompt became active, and never one already claimed. The claim is
// written before the caller starts the turn, so a crash cannot rerun a slot.
func (s *Store) DueCadence(ctx context.Context, now time.Time) ([]Prompt, error) {
	list, err := s.query(ctx, s.db.Read(), `WHERE kind = 'cadence' AND state = 'active' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var out []Prompt
	for _, p := range list {
		slot := goals.LatestSlot(p.Cadence, now, s.loc)
		if slot.IsZero() || now.Sub(slot) > SlotWindow || slot.Before(p.ActiveSince) || !slot.After(p.LastSlot) {
			continue
		}
		var claimed bool
		err := s.db.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE agent_prompts SET last_slot = ? WHERE id = ? AND state = 'active' AND last_slot < ?`, stamp(slot), p.ID, stamp(slot))
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			claimed = n == 1
			return nil
		})
		if err != nil {
			return out, err
		}
		if claimed {
			p.LastSlot = slot
			out = append(out, p)
		}
	}
	return out, nil
}

// ActiveTriggers lists the triggers that may fire.
func (s *Store) ActiveTriggers(ctx context.Context) ([]Prompt, error) {
	return s.query(ctx, s.db.Read(), `WHERE kind = 'trigger' AND state = 'active' ORDER BY created_at, id`)
}

// HighWater is the last signal event sequence the triggers have handled.
func (s *Store) HighWater(ctx context.Context) (int64, error) {
	var v string
	err := s.db.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, highWaterKey).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n, nil
}

// SetHighWater moves the mark forward; it never moves back.
func (s *Store) SetHighWater(ctx context.Context, seq int64) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value WHERE CAST(workspace_meta.value AS INTEGER) < CAST(excluded.value AS INTEGER)`,
			highWaterKey, strconv.FormatInt(seq, 10))
		return err
	})
}

// Claim writes the claim of the pair (trigger, message) and reports whether
// this call made it. The primary key keeps it to one, across restarts,
// re-classification and resync.
func (s *Store) Claim(ctx context.Context, p Prompt, e Event) (bool, error) {
	return s.insertFiring(ctx, p, e, FiringClaimed, "")
}

// Skip records that a matching message was too old to fire.
func (s *Store) Skip(ctx context.Context, p Prompt, e Event, why string) (bool, error) {
	return s.insertFiring(ctx, p, e, FiringSkipped, why)
}

func (s *Store) insertFiring(ctx context.Context, p Prompt, e Event, state, detail string) (bool, error) {
	var made bool
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_trigger_firings (prompt_id, message_id, seq, signal_type, employer, state, detail, claimed_at, finished_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, p.ID, e.MessageID, e.Seq, e.SignalType, e.Employer, state, detail, stamp(s.now()), map[bool]string{true: stamp(s.now())}[state == FiringSkipped])
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		made = n == 1
		return nil
	})
	return made, err
}

// Result is how a firing or a cadence run ended.
type Result struct {
	State          string // answered, failed or timed_out
	Detail         string
	SessionID      string
	Draft          string
	ProposedStatus string
	ProposedReason string
}

// Finish stores the result of a claimed firing and the prompt's last run.
func (s *Store) Finish(ctx context.Context, promptID, messageID string, r Result) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		now := stamp(s.now())
		if messageID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_trigger_firings SET state = ?, detail = ?, session_id = ?, draft = ?, proposed_status = ?, proposed_reason = ?, finished_at = ?
				WHERE prompt_id = ? AND message_id = ? AND state = 'claimed'`, r.State, r.Detail, r.SessionID, r.Draft, r.ProposedStatus, r.ProposedReason, now, promptID, messageID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE agent_prompts SET last_at = ?, last_status = ?, last_session_id = ?, last_summary = ? WHERE id = ?`,
			now, r.State, r.SessionID, summary(r), promptID)
		return err
	})
}

func summary(r Result) string {
	text := r.Draft
	if text == "" {
		text = r.Detail
	}
	return cutRunes(oneLine(text), maxSummary)
}

// SettleInterrupted ends the firings a stopped server left claimed. They are
// not run again: the claim stands.
func (s *Store) SettleInterrupted(ctx context.Context) (int, error) {
	var n int
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE agent_trigger_firings SET state = 'failed', detail = 'the server stopped during the firing', finished_at = ? WHERE state = 'claimed'`, stamp(s.now()))
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}

// Firings returns the latest firings of a trigger, newest first.
func (s *Store) Firings(ctx context.Context, promptID string, limit int) ([]Firing, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT prompt_id, message_id, seq, signal_type, employer, state, detail, session_id, draft, proposed_status, proposed_reason, claimed_at, finished_at
		FROM agent_trigger_firings WHERE prompt_id = ? AND state <> 'skipped' ORDER BY claimed_at DESC, seq DESC LIMIT ?`, promptID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Firing
	for rows.Next() {
		var f Firing
		var claimed, finished string
		if err := rows.Scan(&f.PromptID, &f.MessageID, &f.Seq, &f.SignalType, &f.Employer, &f.State, &f.Detail, &f.SessionID, &f.Draft, &f.ProposedStatus, &f.ProposedReason, &claimed, &finished); err != nil {
			return nil, err
		}
		f.ClaimedAt, f.FinishedAt = parse(claimed), parse(finished)
		out = append(out, f)
	}
	return out, rows.Err()
}

// FiringOf returns the record of one pair.
func (s *Store) FiringOf(ctx context.Context, promptID, messageID string) (Firing, bool, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT prompt_id, message_id, seq, signal_type, employer, state, detail, session_id, draft, proposed_status, proposed_reason, claimed_at, finished_at
		FROM agent_trigger_firings WHERE prompt_id = ? AND message_id = ?`, promptID, messageID)
	if err != nil {
		return Firing{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Firing{}, false, rows.Err()
	}
	var f Firing
	var claimed, finished string
	if err := rows.Scan(&f.PromptID, &f.MessageID, &f.Seq, &f.SignalType, &f.Employer, &f.State, &f.Detail, &f.SessionID, &f.Draft, &f.ProposedStatus, &f.ProposedReason, &claimed, &finished); err != nil {
		return Firing{}, false, err
	}
	f.ClaimedAt, f.FinishedAt = parse(claimed), parse(finished)
	return f, true, nil
}

// Prune deletes the firings older than FiringRetention.
func (s *Store) Prune(ctx context.Context) (int, error) {
	var n int
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM agent_trigger_firings WHERE claimed_at < ?`, stamp(s.now().Add(-FiringRetention)))
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}

func oneLine(s string) string {
	out := make([]rune, 0, len(s))
	space := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			space = true
			continue
		}
		if space && len(out) > 0 {
			out = append(out, ' ')
		}
		space = false
		out = append(out, r)
	}
	return string(out)
}

// cutRunes shortens s to at most n characters, the last an ellipsis when
// something was left out.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
