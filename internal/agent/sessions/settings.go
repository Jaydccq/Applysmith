package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// Setting keys and the limits of the token budget.
const (
	SettingCompaction    = "compaction"
	SettingTokenBudget   = "token_budget"
	SettingConsolidation = "consolidation"
	SettingSelfImprove   = "self_improve"
	SettingDesktopPaused = "desktop_paused"

	DefaultTokenBudget = 24000
	MinTokenBudget     = 4000
	MaxTokenBudget     = 200000
)

// Settings are the agent's runtime switches.
type Settings struct {
	Compaction    bool
	TokenBudget   int
	Consolidation bool
	// SelfImprove lets the nightly round change the harness.
	SelfImprove bool
	// DesktopPaused is the Pause of Agent control: every desktop action is
	// refused while it is on (AC-5).
	DesktopPaused bool
}

// Settings returns the stored settings, with the defaults for any not set:
// compaction, consolidation and self-improvement on, a 24,000 token budget,
// desktop control not paused.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	out := Settings{Compaction: true, TokenBudget: DefaultTokenBudget, Consolidation: true, SelfImprove: true}
	rows, err := s.db.Read().QueryContext(ctx, `SELECT key, value FROM agent_settings`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		switch k {
		case SettingCompaction:
			out.Compaction = v == "on"
		case SettingConsolidation:
			out.Consolidation = v == "on"
		case SettingSelfImprove:
			out.SelfImprove = v == "on"
		case SettingDesktopPaused:
			out.DesktopPaused = v == "on"
		case SettingTokenBudget:
			if n, err := strconv.Atoi(v); err == nil && n >= MinTokenBudget && n <= MaxTokenBudget {
				out.TokenBudget = n
			}
		}
	}
	return out, rows.Err()
}

// SetSwitch turns compaction, consolidation, self-improvement or the desktop
// Pause on or off.
func (s *Store) SetSwitch(ctx context.Context, key string, on bool) error {
	if key != SettingCompaction && key != SettingConsolidation && key != SettingSelfImprove && key != SettingDesktopPaused {
		return fmt.Errorf("sessions: %q is not a switch", key)
	}
	v := "off"
	if on {
		v = "on"
	}
	return s.putSetting(ctx, key, v)
}

// SetTokenBudget stores the prompt budget, which must be within the limits.
func (s *Store) SetTokenBudget(ctx context.Context, n int) error {
	if n < MinTokenBudget || n > MaxTokenBudget {
		return fmt.Errorf("sessions: the token budget is between %d and %d", MinTokenBudget, MaxTokenBudget)
	}
	return s.putSetting(ctx, SettingTokenBudget, strconv.Itoa(n))
}

func (s *Store) putSetting(ctx context.Context, key, value string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_settings (key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, s.stamp())
		return err
	})
}
