package sessions

import (
	"context"
	"database/sql"
	"time"

	"applysmith/internal/gates"
)

// AppendDecision writes one guard-chain decision to gate_decisions. The row
// type has no field for message text or a query string, so neither is stored.
func (s *Store) AppendDecision(ctx context.Context, r gates.AuditRow) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gate_decisions (at, gate, verdict, code, reason, run_id, caller, tool, turn_id, conversation_id,
			risk_tier, arg_hash, user_message_hash, target_host, target_path, target_scheme)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			stampOf(time.UnixMilli(r.At)), string(r.Gate), string(r.Verdict), r.Code, r.Reason, nullable(r.RunID), nullable(string(r.Caller)),
			nullable(r.Tool), nullable(r.TurnID), nullable(r.ConversationID), nullable(r.RiskTier), nullable(r.ArgHash),
			nullable(r.UserMessageHash), nullable(r.TargetHost), nullable(r.TargetPath), nullable(r.TargetScheme))
		return err
	})
}

// Decisions returns the rows of one turn in write order.
func (s *Store) Decisions(ctx context.Context, turnID string) ([]gates.AuditRow, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT at, gate, verdict, code, reason, COALESCE(caller, ''), COALESCE(tool, ''), COALESCE(turn_id, ''),
		COALESCE(conversation_id, ''), COALESCE(risk_tier, ''), COALESCE(arg_hash, ''), COALESCE(user_message_hash, ''),
		COALESCE(target_host, ''), COALESCE(target_path, ''), COALESCE(target_scheme, '')
		FROM gate_decisions WHERE turn_id = ? ORDER BY id`, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gates.AuditRow
	for rows.Next() {
		var r gates.AuditRow
		var at, gate, verdict, caller string
		if err := rows.Scan(&at, &gate, &verdict, &r.Code, &r.Reason, &caller, &r.Tool, &r.TurnID, &r.ConversationID, &r.RiskTier,
			&r.ArgHash, &r.UserMessageHash, &r.TargetHost, &r.TargetPath, &r.TargetScheme); err != nil {
			return nil, err
		}
		r.At = parseStamp(at).UnixMilli()
		r.Gate, r.Verdict, r.Caller = gates.Gate(gate), gates.Verdict(verdict), gates.Caller(caller)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneDecisions removes rows the retention period allows removing.
func (s *Store) PruneDecisions(ctx context.Context) error {
	cutoff := stampOf(time.UnixMilli(gates.AuditPruneBefore(s.now().UnixMilli())))
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM gate_decisions WHERE at < ?`, cutoff)
		return err
	})
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
