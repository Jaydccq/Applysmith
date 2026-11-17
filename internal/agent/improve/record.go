// Package improve is the Ask Agent's self-improvement: it replays recorded
// turns under candidate harness versions without side effects, compares the
// executions blind, adopts a version that does better and keeps the lessons
// the evidence supports. See docs/architecture/ask-agent-improve.md.
package improve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/store"
)

// Call is one tool call of a turn: as recorded, or as a probe made it.
type Call struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result"`
	OK     bool   `json:"ok"`
	// Source says where a probe's result came from: recorded, live, synthetic,
	// or guard when the guard chain answered without running the tool.
	Source string `json:"source,omitempty"`
}

// Recorded is a turn as the record keeps it, with the conversation before it.
type Recorded struct {
	TurnID    string           `json:"turnId"`
	SessionID string           `json:"sessionId"`
	Channel   sessions.Channel `json:"channel"`
	Message   string           `json:"message"`
	Calls     []Call           `json:"calls"`
	Answer    string           `json:"answer"`
	Status    string           `json:"status"`
	Reason    string           `json:"reason,omitempty"`
	Version   int64            `json:"version"`
	StartedAt time.Time        `json:"startedAt"`

	history    []sessions.Message
	checkpoint *sessions.Checkpoint
}

// ErrNotReplayable means the turn cannot be replayed: it was answered by the
// intent router, it has no user message, or it is still running.
var ErrNotReplayable = errors.New("improve: the turn cannot be replayed")

// Load reads a turn and the conversation before it.
func Load(ctx context.Context, db *store.Store, ss *sessions.Store, turnID string) (Recorded, error) {
	r := Recorded{TurnID: turnID}
	var origin, started string
	err := db.Read().QueryRowContext(ctx, `SELECT session_id, channel, origin, status, reason, harness_version, started_at FROM chat_turns WHERE id = ?`, turnID).
		Scan(&r.SessionID, &r.Channel, &origin, &r.Status, &r.Reason, &r.Version, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return Recorded{}, fmt.Errorf("%w: no turn %s", ErrNotReplayable, turnID)
	}
	if err != nil {
		return Recorded{}, err
	}
	if origin != sessions.OriginModel || r.Status == "running" {
		return Recorded{}, fmt.Errorf("%w: %s is %s/%s", ErrNotReplayable, turnID, origin, r.Status)
	}
	r.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	msgs, err := ss.Messages(ctx, r.SessionID)
	if err != nil {
		return Recorded{}, err
	}
	var first int64
	byID := map[string]int{}
	for _, m := range msgs {
		if m.TurnID != turnID {
			if first == 0 {
				r.history = append(r.history, m)
			}
			continue
		}
		if first == 0 {
			first = m.ID
		}
		switch m.Role {
		case sessions.RoleUser:
			if r.Message == "" {
				r.Message = m.Body
			}
		case sessions.RoleToolCall:
			byID[m.ToolCallID] = len(r.Calls)
			r.Calls = append(r.Calls, Call{ID: m.ToolCallID, Name: m.ToolName, Args: canonical(m.Body)})
		case sessions.RoleToolResult:
			if i, ok := byID[m.ToolCallID]; ok {
				r.Calls[i].Result, r.Calls[i].OK = m.Body, m.OK
			}
		case sessions.RoleAssistant:
			r.Answer = m.Body
		}
	}
	if r.Message == "" {
		return Recorded{}, fmt.Errorf("%w: %s has no user message", ErrNotReplayable, turnID)
	}
	cps, err := ss.Checkpoints(ctx, r.SessionID, 20)
	if err != nil {
		return Recorded{}, err
	}
	for _, cp := range cps {
		if cp.CoveredTo < first {
			r.checkpoint = &cp
			break
		}
	}
	return r, nil
}

// copyTo writes the conversation before the turn into another store and
// returns the new conversation's id. Message ids change, so the checkpoint's
// boundary is carried over to the copy of the message it ended at.
func (r Recorded) copyTo(ctx context.Context, dst *sessions.Store) (string, error) {
	sess, err := dst.Create(ctx, r.Channel, "replay")
	if err != nil {
		return "", err
	}
	ids := map[int64]int64{}
	for _, m := range r.history {
		old := m.ID
		m.ID, m.SessionID = 0, sess.ID
		got, err := dst.Append(ctx, m)
		if err != nil {
			return "", fmt.Errorf("copy message %d: %w", old, err)
		}
		ids[old] = got.ID
	}
	if cp := r.checkpoint; cp != nil {
		to, ok := ids[cp.CoveredTo]
		if ok {
			if _, err := dst.AddCheckpoint(ctx, sessions.Checkpoint{SessionID: sess.ID, CoveredTo: to, CoveredTurns: cp.CoveredTurns, Summary: cp.Summary, Tainted: cp.Tainted}); err != nil {
				return "", err
			}
		}
	}
	return sess.ID, nil
}

// canonical rewrites JSON arguments with sorted keys and no step, so a probe's
// call and a recorded one compare equal when they ask for the same thing.
func canonical(args string) string {
	var v map[string]any
	if json.Unmarshal([]byte(args), &v) != nil {
		return args
	}
	delete(v, "step")
	b, err := json.Marshal(v)
	if err != nil {
		return args
	}
	return string(b)
}
