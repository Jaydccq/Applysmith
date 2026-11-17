package gmail

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Stored text limits: the evidence snippet is kept for good, bodies are
// bounded before they reach the database.
const (
	MaxSnippetChars = 220
	maxBodyChars    = 12000
	maxRawChars     = 24000
)

func truncateChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// scoredMessage is a fetched message with its verdict, ready to store.
type scoredMessage struct {
	msg     *Message
	verdict Verdict
}

// existingDecisions returns which of the ids already have a decision under the
// classifier version.
func (r *Repo) existingDecisions(ctx context.Context, version string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		chunk := ids[start:end]
		args := []any{version}
		for _, id := range chunk {
			args = append(args, id)
		}
		q := `SELECT message_id FROM gmail_decisions WHERE classifier_version = ? AND message_id IN (` + placeholders(len(chunk)) + `)`
		rows, err := r.st.Read().QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

func (r *Repo) tombstoned(ctx context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(ids); start += 400 {
		chunk := ids[start:min(start+400, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := r.st.Read().QueryContext(ctx, `SELECT message_id FROM gmail_tombstones WHERE message_id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		rows.Close()
	}
	return out, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// storeBatch writes a batch of verdicts and tombstones in one transaction.
// Evidence is written once per message and decisions once per message and
// classifier version, so a repeated batch changes nothing. replace lets a
// reclassification overwrite its own decision. It returns how many decisions
// were new and how many of those were review-tier.
func (r *Repo) storeBatch(ctx context.Context, runID int64, version string, batch []scoredMessage, tombs []string, replace bool) (stored, review int, err error) {
	now := stamp(r.now())
	verb := "INSERT OR IGNORE"
	if replace {
		verb = "INSERT OR REPLACE"
	}
	err = r.st.Write(ctx, func(tx *sql.Tx) error {
		for _, sm := range batch {
			v, m := sm.verdict, sm.msg
			if v.Kind == VerdictSignal {
				snippet := truncateChars(v.Snippet, MaxSnippetChars)
				labels, _ := json.Marshal(m.Labels)
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_evidence (message_id, thread_id, received_ms,
					from_header, to_header, cc_header, subject, snippet, labels, first_run_id, first_seen_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.ThreadID, m.InternalMS,
					m.From, m.To, m.Cc, m.Subject, snippet, string(labels), runID, now); err != nil {
					return err
				}
				if v.Body != "" || v.Raw != "" {
					body, raw := truncateChars(v.Body, maxBodyChars), truncateChars(v.Raw, maxRawChars)
					if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_bodies (message_id, body_text, raw_text, bytes, stored_at)
						VALUES (?, ?, ?, ?, ?)`, m.ID, body, raw, len(body)+len(raw), now); err != nil {
						return err
					}
				}
			}
			var due any
			if v.DueMS != 0 {
				due = v.DueMS
			}
			detail := v.DetailJSON
			if detail == "" {
				detail = "{}"
			}
			res, err := tx.ExecContext(ctx, verb+` INTO gmail_decisions (message_id, classifier_version, outcome, reason, signal_type, tier, direction,
				employer, role, confidence, due_ms, rule_id, review_reasons, detail_json, run_id, decided_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, version, v.Kind, v.Reason, v.Type, v.Tier, v.Direction,
				v.Employer, v.Role, v.Confidence, due, v.RuleID, strings.Join(v.ReviewReasons, ","), detail, runID, now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				stored++
				if v.Tier == "review" {
					review++
				}
			}
			if err := recordSignalEvent(ctx, tx, v, m, now); err != nil {
				return err
			}
		}
		for _, id := range tombs {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_tombstones (message_id, tombstoned_at, run_id) VALUES (?, ?, ?)`, id, now, runID); err != nil {
				return err
			}
		}
		return nil
	})
	return stored, review, err
}

// SweepResult says what the retention sweep removed.
type SweepResult struct{ ByAge, BySize int }

// Retention limits for stored message bodies.
const (
	BodyMaxAge   = 30 * 24 * time.Hour
	BodyMaxBytes = 200 << 20
)

// Sweep removes bodies older than BodyMaxAge and then the oldest bodies until
// the total is under maxBytes. Signals, snippets and decisions stay.
func (r *Repo) Sweep(ctx context.Context, maxBytes int64) (SweepResult, error) {
	var res SweepResult
	cutoff := stamp(r.now().Add(-BodyMaxAge))
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		out, err := tx.ExecContext(ctx, `DELETE FROM gmail_bodies WHERE stored_at < ?`, cutoff)
		if err != nil {
			return err
		}
		n, _ := out.RowsAffected()
		res.ByAge = int(n)
		var total int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM gmail_bodies`).Scan(&total); err != nil {
			return err
		}
		for total > maxBytes {
			var id string
			var size int64
			if err := tx.QueryRowContext(ctx, `SELECT message_id, bytes FROM gmail_bodies ORDER BY stored_at ASC, rowid ASC LIMIT 1`).Scan(&id, &size); err != nil {
				if err == sql.ErrNoRows {
					break
				}
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM gmail_bodies WHERE message_id = ?`, id); err != nil {
				return err
			}
			total -= size
			res.BySize++
		}
		return nil
	})
	return res, err
}

// SignalRow is the stored view of one visible (non-tombstoned) signal at the
// newest classifier version of its message.
type SignalRow struct {
	MessageID, ThreadID   string
	Type, Tier, Direction string
	Employer, Role        string
	Confidence            float64
	DueMS                 int64
	RuleID                string
	ReviewReasons         string
	ReceivedMS            int64
	Subject, Snippet      string
	// From is the raw From header.
	From string
}

// Signals returns the visible signals: tombstoned messages and ignored
// messages are excluded, and each message appears once, at its newest
// classifier version.
func (r *Repo) Signals(ctx context.Context) ([]SignalRow, error) {
	return r.signalsWhere(ctx, "")
}

// signalsWhere is Signals narrowed by one more condition on the decision (d)
// or the evidence (e); the empty condition narrows nothing.
func (r *Repo) signalsWhere(ctx context.Context, cond string, args ...any) ([]SignalRow, error) {
	if cond != "" {
		cond = " AND " + cond
	}
	rows, err := r.st.Read().QueryContext(ctx, `SELECT d.message_id, e.thread_id, d.signal_type,
		CASE WHEN rj.message_id IS NULL THEN d.tier ELSE 'suppressed' END, d.direction, d.employer, d.role,
		d.confidence, COALESCE(d.due_ms, 0), d.rule_id, d.review_reasons, e.received_ms, e.subject, e.snippet, e.from_header
		FROM gmail_decisions d JOIN gmail_evidence e ON e.message_id = d.message_id
		LEFT JOIN gmail_tombstones t ON t.message_id = d.message_id
		LEFT JOIN gmail_link_rejections rj ON rj.message_id = d.message_id
		WHERE t.message_id IS NULL AND d.outcome = 'signal'
		AND d.rowid = (SELECT x.rowid FROM gmail_decisions x WHERE x.message_id = d.message_id
			ORDER BY x.decided_at DESC, x.rowid DESC LIMIT 1)`+cond+`
		ORDER BY e.received_ms, d.message_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignalRow
	for rows.Next() {
		var s SignalRow
		if err := rows.Scan(&s.MessageID, &s.ThreadID, &s.Type, &s.Tier, &s.Direction, &s.Employer, &s.Role,
			&s.Confidence, &s.DueMS, &s.RuleID, &s.ReviewReasons, &s.ReceivedMS, &s.Subject, &s.Snippet, &s.From); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
