// Package accounts holds the cooldowns that risk signals start on the sites the
// application chain creates accounts on. It answers the account creation
// gate's cooldown input and records the signals; the gate itself lives in
// internal/gates.
package accounts

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

const stampLayout = "2006-01-02T15:04:05.000Z"

// Service reads and writes the signals.
type Service struct {
	st  *store.Store
	ws  *workspace.Workspace
	now func() time.Time
}

// New returns a service over the open store and workspace. A nil now uses the
// wall clock.
func New(st *store.Store, ws *workspace.Workspace, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, ws: ws, now: now}
}

// Cooldowns lists the cooldowns still running, the account creation gate's
// input that lives outside the chain.
func (s *Service) Cooldowns(ctx context.Context) ([]gates.Cooldown, error) {
	return s.cooldowns(ctx)
}

func (s *Service) cooldowns(ctx context.Context) ([]gates.Cooldown, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT system, ends_at FROM risk_signals WHERE ends_at > ?`, s.now().UTC().Format(stampLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gates.Cooldown
	for rows.Next() {
		var sys, end string
		if err := rows.Scan(&sys, &end); err != nil {
			return nil, err
		}
		t, err := time.Parse(stampLayout, end)
		if err != nil {
			return nil, err
		}
		out = append(out, gates.Cooldown{System: sys, End: t.UnixMilli()})
	}
	return out, rows.Err()
}

// RecordSignal stores a detection signal for a system and starts the cooldown
// the policy's table gives it. A configured 0 hours stores the signal and
// starts none. It reports the cooldown's end, or the zero time when none
// started.
func (s *Service) RecordSignal(ctx context.Context, system, signal, runID string) (time.Time, error) {
	pol, _ := s.ws.LoadPolicy()
	c := pol.Cooldowns
	hours := map[string]int{
		gates.SignalCaptcha: c.Captcha, gates.SignalRateLimited: c.RateLimited, gates.SignalAccountLocked: c.AccountLocked,
		gates.SignalVerificationRequired: c.VerificationRequired, gates.SignalLoginRedirect: c.LoginRedirect, gates.SignalSilentDegradation: c.SilentDegradation,
	}
	if _, known := hours[signal]; !known || system == "" {
		return time.Time{}, fmt.Errorf("accounts: %q is not a detection signal for %q", signal, system)
	}
	now := s.now().UTC()
	ends := now
	cd, started := gates.CooldownFor(signal, system, now.UnixMilli(), hours)
	if started {
		ends = time.UnixMilli(cd.End).UTC()
	}
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO risk_signals (system, signal, run_id, at, ends_at) VALUES (?, ?, ?, ?, ?)`,
			system, signal, runID, now.Format(stampLayout), ends.Format(stampLayout))
		return err
	})
	if err != nil || !started {
		return time.Time{}, err
	}
	return ends, nil
}

// Signal is one recorded detection signal. EndsAt is the end of the cooldown it
// started, equal to At when the policy gave its signal no hours.
type Signal struct {
	System, Signal, RunID string
	At, EndsAt            time.Time
}

// Started reports whether the signal started a cooldown.
func (g Signal) Started() bool { return g.EndsAt.After(g.At) }

// RecentSignals lists the signals recorded since the given time, newest first.
func (s *Service) RecentSignals(ctx context.Context, since time.Time, limit int) ([]Signal, error) {
	rows, err := s.st.Read().QueryContext(ctx,
		`SELECT system, signal, run_id, at, ends_at FROM risk_signals WHERE at >= ? ORDER BY at DESC, id DESC LIMIT ?`,
		since.UTC().Format(stampLayout), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signal
	for rows.Next() {
		var g Signal
		var at, end string
		if err := rows.Scan(&g.System, &g.Signal, &g.RunID, &at, &end); err != nil {
			return nil, err
		}
		if g.At, err = time.Parse(stampLayout, at); err != nil {
			return nil, err
		}
		if g.EndsAt, err = time.Parse(stampLayout, end); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ActiveCooldowns lists, for each system whose cooldown has not ended, the
// signal that started it last: that one supplies the reason shown. A system
// is active while any of its entries ends in the future.
func (s *Service) ActiveCooldowns(ctx context.Context) ([]Signal, error) {
	rows, err := s.st.Read().QueryContext(ctx,
		`SELECT system, signal, run_id, at, ends_at FROM risk_signals WHERE ends_at > ? AND ends_at > at ORDER BY at DESC, id DESC`,
		s.now().UTC().Format(stampLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signal
	at := map[string]int{}
	for rows.Next() {
		var g Signal
		var started, end string
		if err := rows.Scan(&g.System, &g.Signal, &g.RunID, &started, &end); err != nil {
			return nil, err
		}
		if g.At, err = time.Parse(stampLayout, started); err != nil {
			return nil, err
		}
		if g.EndsAt, err = time.Parse(stampLayout, end); err != nil {
			return nil, err
		}
		if i, ok := at[g.System]; ok {
			out[i].EndsAt = latest(out[i].EndsAt, g.EndsAt)
			continue
		}
		at[g.System] = len(out)
		out = append(out, g)
	}
	return out, rows.Err()
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
