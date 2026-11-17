package telegram

import (
	"context"
	"database/sql"
	"errors"
)

// Receipt states an update is left in. While it is handled its receipt is
// "processing", which Begin writes.
const (
	ReceiptProcessed = "processed"
	ReceiptRetryable = "failed-retryable"
	ReceiptTerminal  = "terminal"
)

// MaxAttempts is how many times an update is handled before it is given up.
const MaxAttempts = 3

// Begin records that an update is about to be handled. It reports skip when the
// update, or the callback it carries, already reached processed or terminal.
func (s *State) Begin(ctx context.Context, updateID int64, callbackID string) (skip bool, err error) {
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		var state string
		var cb sql.NullString
		qerr := tx.QueryRowContext(ctx, `SELECT state, callback_id FROM telegram_receipts WHERE update_id = ?`, updateID).Scan(&state, &cb)
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return qerr
		}
		if callbackID != "" {
			// The same callback under another update id is the same tap.
			var other string
			cerr := tx.QueryRowContext(ctx, `SELECT state FROM telegram_receipts WHERE callback_id = ? AND update_id <> ?`, callbackID, updateID).Scan(&other)
			if cerr == nil && (other == ReceiptProcessed || other == ReceiptTerminal) {
				skip = true
				return nil
			}
		}
		if qerr == nil && (state == ReceiptProcessed || state == ReceiptTerminal) {
			skip = true
			return nil
		}
		now := ts(s.now())
		var cbArg any
		if callbackID != "" {
			cbArg = callbackID
			var taken int
			_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM telegram_receipts WHERE callback_id = ? AND update_id <> ?`, callbackID, updateID).Scan(&taken)
			if taken > 0 {
				cbArg = nil
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_receipts (update_id, callback_id, state, attempts, created_at, updated_at) VALUES (?, ?, 'processing', 1, ?, ?)
			ON CONFLICT(update_id) DO UPDATE SET state = 'processing', attempts = attempts + 1, updated_at = excluded.updated_at`, updateID, cbArg, now, now)
		return err
	})
	return skip, err
}

// Complete marks an update processed and keeps the action name.
func (s *State) Complete(ctx context.Context, updateID int64, action string, chatID int64) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE telegram_receipts SET state = 'processed', action = ?, chat_id = ?, updated_at = ? WHERE update_id = ?`,
			action, chatID, ts(s.now()), updateID)
		return err
	})
}

// Fail records a failed attempt. After the third the receipt becomes terminal
// and terminal is true.
func (s *State) Fail(ctx context.Context, updateID int64, chatID int64) (terminal bool, err error) {
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		var attempts int
		if err := tx.QueryRowContext(ctx, `SELECT attempts FROM telegram_receipts WHERE update_id = ?`, updateID).Scan(&attempts); err != nil {
			return err
		}
		state := ReceiptRetryable
		if attempts >= MaxAttempts {
			state, terminal = ReceiptTerminal, true
		}
		_, err := tx.ExecContext(ctx, `UPDATE telegram_receipts SET state = ?, chat_id = ?, updated_at = ? WHERE update_id = ?`, state, chatID, ts(s.now()), updateID)
		return err
	})
	return terminal, err
}
