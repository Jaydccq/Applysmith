package apply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// journalKeep is how long a closed batch stays in the journal.
const journalKeep = 7 * 24 * time.Hour

// Journal records the state of the rows of one batch. A row is marked started
// before its chain runs, so a process that dies mid-row leaves it started.
type Journal interface {
	Started(ctx context.Context, idx int) error
	Attach(ctx context.Context, idx int, runID string)
	Ended(ctx context.Context, idx int, note string)
}

// BatchMeta is what a batch is started from.
type BatchMeta struct {
	Caller   gates.Caller
	Tool     string
	Submit   bool
	Explicit bool
	Deadline time.Time
	// ConversationID and MessageHash name the Ask Agent turn the batch came
	// from; the hash is salted and the message text is never kept.
	ConversationID, MessageHash string
	// Request is the JSON of the lanes, limit and options the batch was asked
	// for; it holds no form value.
	Request json.RawMessage
}

// BatchJournal is the SQLite journal of apply lists.
type BatchJournal struct {
	st  *store.Store
	now func() time.Time
}

// NewBatchJournal returns the journal over the workspace database.
func NewBatchJournal(st *store.Store, now func() time.Time) *BatchJournal {
	if now == nil {
		now = time.Now
	}
	return &BatchJournal{st: st, now: now}
}

func (j *BatchJournal) stamp() string { return j.now().UTC().Format(stampLayout) }

// Create journals a batch with its rows, all pending.
func (j *BatchJournal) Create(ctx context.Context, id string, m BatchMeta, rows []ListRow) error {
	req := m.Request
	if len(req) == 0 {
		req = json.RawMessage(`{}`)
	}
	return j.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO apply_batches (batch_id, created_at, deadline_at, caller, tool, submit, explicit, request, trigger_conversation_id, trigger_message_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, j.stamp(), m.Deadline.UTC().Format(stampLayout), string(m.Caller), m.Tool, m.Submit, m.Explicit, string(req), m.ConversationID, m.MessageHash); err != nil {
			return err
		}
		for i, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO apply_batch_rows (batch_id, idx, url, employer_url, company, role, named, mirrors, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, i, r.URL, r.EmployerURL, r.Company, r.Role, r.Named, r.Mirrors, j.stamp()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Close ends a batch and drops the closed batches older than a week.
func (j *BatchJournal) Close(ctx context.Context, id string) error {
	return j.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE apply_batches SET closed_at = ? WHERE batch_id = ? AND closed_at IS NULL`, j.stamp(), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM apply_batches WHERE closed_at IS NOT NULL AND closed_at < ?`, j.now().Add(-journalKeep).UTC().Format(stampLayout))
		return err
	})
}

// For binds the journal to one batch.
func (j *BatchJournal) For(id string) Journal { return boundJournal{j, id} }

type boundJournal struct {
	j  *BatchJournal
	id string
}

func (b boundJournal) Started(ctx context.Context, idx int) error {
	return b.j.setRow(ctx, b.id, idx, `state = 'started'`, nil, `AND state <> 'started'`)
}

func (b boundJournal) Attach(ctx context.Context, idx int, runID string) {
	_ = b.j.setRow(ctx, b.id, idx, `run_id = ?`, []any{runID}, "")
}

func (b boundJournal) Ended(ctx context.Context, idx int, note string) {
	_ = b.j.setRow(ctx, b.id, idx, `state = 'ended', note = ?`, []any{note}, "")
}

var errRowMoved = errors.New("the journal row is not pending")

func (j *BatchJournal) setRow(ctx context.Context, id string, idx int, set string, args []any, guard string) error {
	ctx = context.WithoutCancel(ctx)
	return j.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE apply_batch_rows SET `+set+`, updated_at = ? WHERE batch_id = ? AND idx = ? `+guard,
			append(args, j.stamp(), id, idx)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errRowMoved
		}
		return nil
	})
}

// OpenBatch is a batch that did not close: the rows to resume and the request.
type OpenBatch struct {
	ID    string
	Meta  BatchMeta
	Rows  []ListRow
	Index []int
}

// Recover reads the batches a dead process left open. A started row stays
// started, which reads as possibly submitted and is re-run by nothing; the
// pending rows come back with their original positions. A batch past its deadline, or with
// nothing pending, is closed.
func (j *BatchJournal) Recover(ctx context.Context) ([]OpenBatch, error) {
	var out []OpenBatch
	err := j.st.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT batch_id, caller, tool, submit, explicit, deadline_at, request, trigger_conversation_id, trigger_message_hash FROM apply_batches WHERE closed_at IS NULL ORDER BY created_at`)
		if err != nil {
			return err
		}
		var batches []OpenBatch
		for rows.Next() {
			var b OpenBatch
			var caller, deadline, req string
			if err := rows.Scan(&b.ID, &caller, &b.Meta.Tool, &b.Meta.Submit, &b.Meta.Explicit, &deadline, &req, &b.Meta.ConversationID, &b.Meta.MessageHash); err != nil {
				rows.Close()
				return err
			}
			b.Meta.Caller, b.Meta.Request = gates.Caller(caller), json.RawMessage(req)
			b.Meta.Deadline, _ = time.Parse(stampLayout, deadline)
			batches = append(batches, b)
		}
		rows.Close()
		for _, b := range batches {
			pr, err := tx.QueryContext(ctx, `SELECT idx, url, employer_url, company, role, named, mirrors FROM apply_batch_rows WHERE batch_id = ? AND state = 'pending' ORDER BY idx`, b.ID)
			if err != nil {
				return err
			}
			for pr.Next() {
				var idx int
				var r ListRow
				if err := pr.Scan(&idx, &r.URL, &r.EmployerURL, &r.Company, &r.Role, &r.Named, &r.Mirrors); err != nil {
					pr.Close()
					return err
				}
				b.Rows, b.Index = append(b.Rows, r), append(b.Index, idx)
			}
			pr.Close()
			if len(b.Rows) == 0 || !j.now().Before(b.Meta.Deadline) {
				if _, err := tx.ExecContext(ctx, `UPDATE apply_batch_rows SET state = 'ended', note = ? WHERE batch_id = ? AND state = 'pending'`, NoteDeadline, b.ID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE apply_batches SET closed_at = ? WHERE batch_id = ?`, j.stamp(), b.ID); err != nil {
					return err
				}
				continue
			}
			out = append(out, b)
		}
		return nil
	})
	return out, err
}

// Row notes the journal and the driver share.
const (
	NoteDeadline = "skipped: time budget exhausted"
)
