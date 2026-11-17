package apply

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// Audit appends every gate decision as one gate_decisions row. There is no
// switch that turns it off: a decision always yields a row, and a write that
// fails is reported to the caller, never swallowed.
type Audit struct {
	st   *store.Store
	now  func() time.Time
	salt string
}

// NewAudit returns the audit writer over the workspace database.
func NewAudit(st *store.Store, salt string, now func() time.Time) *Audit {
	if now == nil {
		now = time.Now
	}
	return &Audit{st: st, now: now, salt: salt}
}

// Record writes one decision. It must not be called from inside a write
// transaction: the store has one writer.
func (a *Audit) Record(ctx context.Context, d gates.Decision, c gates.AuditContext) error {
	c.Salt = a.salt
	row := gates.NewAuditRow(a.now().UnixMilli(), d, c)
	return a.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gate_decisions
			(at, gate, verdict, code, reason, run_id, caller, tool, turn_id, conversation_id, risk_tier, arg_hash, user_message_hash, target_host, target_path, target_scheme)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`,
			time.UnixMilli(row.At).UTC().Format(stampLayout), string(row.Gate), string(row.Verdict), row.Code, row.Reason,
			row.RunID, string(row.Caller), row.Tool, row.TurnID, row.ConversationID, row.RiskTier, row.ArgHash, row.UserMessageHash,
			row.TargetHost, row.TargetPath, row.TargetScheme)
		return err
	})
}

const stampLayout = "2006-01-02T15:04:05.000Z"

// ErrAudit wraps a failed audit write. A run that cannot audit a side effect
// does not perform it.
type ErrAudit struct{ Err error }

func (e *ErrAudit) Error() string {
	return fmt.Sprintf("the gate audit could not be written: %v", e.Err)
}
func (e *ErrAudit) Unwrap() error { return e.Err }
