package credentials

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const resetFailedPrefix = "reset_failed:"

// ResetFailedAt returns when a password reset last failed on a host, the zero
// time when none did. A failed reset blocks another on the host for 24 hours.
func (c *Service) ResetFailedAt(ctx context.Context, host string) (time.Time, error) {
	var v string
	err := c.s.Read().QueryRowContext(ctx, `SELECT value FROM credential_meta WHERE key = ?`, resetFailedPrefix+strings.ToLower(host)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(stampLayout, v)
}

// RecordResetFailed notes that a reset failed on the host now.
func (c *Service) RecordResetFailed(ctx context.Context, host string) error {
	return c.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO credential_meta (key, value) VALUES (?1, ?2) ON CONFLICT (key) DO UPDATE SET value = ?2`,
			resetFailedPrefix+strings.ToLower(host), c.now().UTC().Format(stampLayout))
		return err
	})
}

// ClearHold lifts the host's same-day hold and its failed-reset mark: a
// reset that signed in proved the saved password.
func (c *Service) ClearHold(ctx context.Context, host string) error {
	host = strings.ToLower(host)
	return c.s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM signin_holds WHERE host = ?`, host); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM credential_meta WHERE key = ?`, resetFailedPrefix+host)
		return err
	})
}
