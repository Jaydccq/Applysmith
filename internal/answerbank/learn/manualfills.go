package learn

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound marks a row, candidate or entry that does not exist.
var ErrNotFound = errors.New("not found")

// ManualFill is what the user typed or chose themselves. Value and OptionLabel
// are nil for a value-withheld control: only the edit is recorded.
type ManualFill struct {
	ID           int64
	At           time.Time
	ReceivedAt   time.Time
	Host         string
	Question     string
	ControlKind  string
	Value        *string
	OptionLabel  *string
	Withheld     Withheld
	GroundTruth  bool
	PriorValue   *string
	EntryID      string
	RunID        string
	ReportNumber *int64
}

// ManualFills lists the records, newest first, with the total count.
func (s *Service) ManualFills(ctx context.Context, limit, offset int) ([]ManualFill, int, error) {
	var total int
	if err := s.st.Read().QueryRowContext(ctx, `SELECT count(*) FROM manual_fills`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.st.Read().QueryContext(ctx, `SELECT id, at, received_at, host, question, control_kind, value, option_label, withheld, ground_truth, prior_value, entry_id, run_id, report_number
		FROM manual_fills ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ManualFill{}
	for rows.Next() {
		var f ManualFill
		var at, recv, withheld string
		if err := rows.Scan(&f.ID, &at, &recv, &f.Host, &f.Question, &f.ControlKind, &f.Value, &f.OptionLabel, &withheld, &f.GroundTruth, &f.PriorValue, &f.EntryID, &f.RunID, &f.ReportNumber); err != nil {
			return nil, 0, err
		}
		f.At, f.ReceivedAt, f.Withheld = parseStamp(at), parseStamp(recv), Withheld(withheld)
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// DeleteManualFill removes a record. It leaves every candidate's evidence, so a
// pending candidate's support drops by one, and removes the correction the
// record produced and the upload record of its event, so the same event id is
// accepted again if it is re-posted.
func (s *Service) DeleteManualFill(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		var eventID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT event_id FROM manual_fills WHERE id = ?`, id).Scan(&eventID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM manual_fills WHERE id = ?`, id); err != nil {
			return err
		}
		if eventID.Valid {
			if _, err := tx.ExecContext(ctx, `DELETE FROM learning_uploads WHERE event_id = ?`, eventID.String); err != nil {
				return err
			}
		}
		return recountPending(ctx, tx)
	})
}

// recountPending sets the support of every pending candidate to the evidence
// rows it still has. A decided candidate keeps the support it was decided at.
func recountPending(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE answer_candidates SET support_count =
		(SELECT count(*) FROM answer_candidate_evidence e WHERE e.candidate_id = answer_candidates.id) WHERE state = 'pending'`)
	return err
}
