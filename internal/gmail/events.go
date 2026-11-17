package gmail

import (
	"context"
	"database/sql"
	"time"

	"applysmith/internal/gmail/classify"
)

// SignalEventRetention is how long a signal event is kept.
const SignalEventRetention = 30 * 24 * time.Hour

// SignalEvent is the record that a message newly got a classified signal. It
// holds identifiers and classification fields only: a trigger that needs the
// text reads the stored body, which the retention rule bounds.
type SignalEvent struct {
	Seq        int64
	MessageID  string
	SignalType string
	Tier       string
	Direction  string
	Employer   string
	Received   time.Time
	Recorded   time.Time
}

// recordSignalEvent writes the event of a message in the transaction that
// stores its decision. The unique message id makes it once per message, so a
// reclassification, a resync or a changed type or tier records no second one.
// Outgoing mail produces none.
func recordSignalEvent(ctx context.Context, tx *sql.Tx, v Verdict, m *Message, now string) error {
	if v.Kind != VerdictSignal || v.Direction == classify.DirOutgoing {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_signal_events (message_id, signal_type, tier, direction, employer, received_ms, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, m.ID, v.Type, v.Tier, v.Direction, v.Employer, m.InternalMS, now)
	return err
}

// SignalEvents returns up to limit events recorded after seq, oldest first.
// A trigger keeps the last seq it handled as its high-water mark.
func (r *Repo) SignalEvents(ctx context.Context, afterSeq int64, limit int) ([]SignalEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := r.st.Read().QueryContext(ctx, `SELECT seq, message_id, signal_type, tier, direction, employer, received_ms, recorded_at
		FROM gmail_signal_events WHERE seq > ? ORDER BY seq LIMIT ?`, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignalEvent
	for rows.Next() {
		var e SignalEvent
		var ms int64
		var rec string
		if err := rows.Scan(&e.Seq, &e.MessageID, &e.SignalType, &e.Tier, &e.Direction, &e.Employer, &ms, &rec); err != nil {
			return nil, err
		}
		e.Received = time.UnixMilli(ms).UTC()
		e.Recorded, _ = parseStamp(rec)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneSignalEvents deletes the events recorded more than 30 days ago.
func (r *Repo) PruneSignalEvents(ctx context.Context) (int, error) {
	n := 0
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM gmail_signal_events WHERE recorded_at < ?`, stamp(r.now().Add(-SignalEventRetention)))
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}
