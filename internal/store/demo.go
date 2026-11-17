package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/workspace"
)

// Demo reports whether the workspace this database belongs to carries the
// demo marker. It is read from the state directory on every call, so every
// component that holds the database sees the marker without being told.
func (s *Store) Demo() bool { return s != nil && workspace.DemoMarked(filepath.Dir(s.path)) }

// DemoRefusal is the error of an outbound action the demo gate refused.
type DemoRefusal struct{ Decision gates.Decision }

func (e *DemoRefusal) Error() string { return e.Decision.Code + ": " + e.Decision.Reason }

// GuardDemo asks the demo gate about an outbound action that has no gate of
// its own. Outside a demo workspace it returns nil and records nothing; in one
// it records the decision as a gate_decisions row and returns a DemoRefusal
// for a denial. A refusal holds even when the row cannot be written.
func (s *Store) GuardDemo(ctx context.Context, action gates.DemoAction, host string) error {
	return s.GuardDemoInput(ctx, gates.DemoInput{Action: action, Host: host})
}

// GuardDemoInput is GuardDemo for an action whose decision needs facts (the
// marker is read here, so in.Demo is ignored).
func (s *Store) GuardDemoInput(ctx context.Context, in gates.DemoInput) error {
	if !s.Demo() {
		return nil
	}
	in.Demo = true
	host := in.Host
	d := gates.DemoWorkspace(in)
	if s.rw != nil {
		row := gates.NewAuditRow(s.now().UnixMilli(), d, gates.AuditContext{TargetURL: hostURL(host)})
		wctx := context.WithoutCancel(ctx)
		_ = s.Write(wctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(wctx, `INSERT INTO gate_decisions (at, gate, verdict, code, reason, target_host, target_scheme)
				VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
				time.UnixMilli(row.At).UTC().Format("2006-01-02T15:04:05.000Z"), string(row.Gate), string(row.Verdict), row.Code, row.Reason,
				row.TargetHost, row.TargetScheme)
			return err
		})
	}
	if !d.Allowed() {
		return &DemoRefusal{Decision: d}
	}
	return nil
}

func hostURL(host string) string {
	if host == "" {
		return ""
	}
	return "https://" + host + "/"
}
