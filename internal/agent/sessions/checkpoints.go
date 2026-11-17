package sessions

import (
	"context"
	"database/sql"
	"time"
	"unicode"
	"unicode/utf8"
)

// Checkpoint is the summary that stands in for the messages of a conversation
// up to CoveredTo. Tainted is set when anything it covers, or any checkpoint
// it builds on, carried text from outside the product.
type Checkpoint struct {
	ID           int64
	SessionID    string
	CoveredTo    int64
	CoveredTurns int
	Summary      string
	Tainted      bool
	CreatedAt    time.Time
}

// AddCheckpoint stores a checkpoint and returns it with its id.
func (s *Store) AddCheckpoint(ctx context.Context, c Checkpoint) (Checkpoint, error) {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.now()
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO chat_checkpoints (session_id, covered_to, covered_turns, summary, tainted, created_at) VALUES (?,?,?,?,?,?)`,
			c.SessionID, c.CoveredTo, c.CoveredTurns, c.Summary, b2i(c.Tainted), stampOf(c.CreatedAt))
		if err != nil {
			return err
		}
		c.ID, err = res.LastInsertId()
		return err
	})
	return c, err
}

// LatestCheckpoint returns the newest checkpoint of a conversation, if any.
func (s *Store) LatestCheckpoint(ctx context.Context, sessionID string) (Checkpoint, bool, error) {
	cs, err := s.Checkpoints(ctx, sessionID, 1)
	if err != nil || len(cs) == 0 {
		return Checkpoint{}, false, err
	}
	return cs[0], true, nil
}

// Checkpoints returns a conversation's checkpoints, newest first; limit 0 is
// all of them.
func (s *Store) Checkpoints(ctx context.Context, sessionID string, limit int) ([]Checkpoint, error) {
	q := `SELECT id, session_id, covered_to, covered_turns, summary, tainted, created_at FROM chat_checkpoints WHERE session_id = ? ORDER BY id DESC`
	args := []any{sessionID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var tainted int
		var at string
		if err := rows.Scan(&c.ID, &c.SessionID, &c.CoveredTo, &c.CoveredTurns, &c.Summary, &tainted, &at); err != nil {
			return nil, err
		}
		c.Tainted, c.CreatedAt = tainted == 1, parseStamp(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// EndedBetween returns the conversations last updated in (after, before],
// oldest first. A conversation with no update since before has ended for the
// caller's purposes.
func (s *Store) EndedBetween(ctx context.Context, after, before time.Time, limit int) ([]Session, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+sessionColumns+` FROM chat_sessions WHERE updated_at > ? AND updated_at <= ? ORDER BY updated_at, id LIMIT ?`,
		stampOf(after), stampOf(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// EstimateTokens is the one token estimate the agent uses: four bytes to a
// token, rounded up, except that a character of a script written without
// spaces between words (Han, kana, Hangul, fullwidth forms) is one token, as
// tokenizers spend about that much on it.
func EstimateTokens(s string) int {
	wide, rest := 0, 0
	for _, r := range s {
		if isWide(r) {
			wide++
		} else {
			rest += utf8.RuneLen(r)
		}
	}
	return wide + (rest+3)/4
}

var wideScripts = []*unicode.RangeTable{unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, fullwidthForms}

// fullwidthForms is the Halfwidth and Fullwidth Forms block.
var fullwidthForms = &unicode.RangeTable{R16: []unicode.Range16{{Lo: 0xFF00, Hi: 0xFFEF, Stride: 1}}}

// isWide skips the table lookup for everything below the first of these scripts.
func isWide(r rune) bool { return r >= 0x1100 && unicode.In(r, wideScripts...) }
