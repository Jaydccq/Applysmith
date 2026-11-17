package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"applysmith/internal/store"
)

// Settings are the three runtime switches of the notifications. Quiet hours
// are not among them: they stay in the profile.
type Settings struct {
	Desktop     bool
	Telegram    bool
	FailedScans bool
}

// DefaultSettings are what a workspace without a record reads as.
var DefaultSettings = Settings{Desktop: true, Telegram: true}

// SettingsPatch changes any subset of the settings.
type SettingsPatch struct {
	Desktop     *bool
	Telegram    *bool
	FailedScans *bool
}

// SettingsStore reads and writes the settings in the database.
type SettingsStore struct {
	st  *store.Store
	now func() time.Time
}

// NewSettingsStore returns the settings store.
func NewSettingsStore(st *store.Store, now func() time.Time) *SettingsStore {
	if now == nil {
		now = time.Now
	}
	return &SettingsStore{st: st, now: now}
}

// Get reads the settings. A record that cannot be read gives the defaults, so a
// damaged table never silences the notifications.
func (s *SettingsStore) Get(ctx context.Context) Settings {
	out := DefaultSettings
	rows, err := s.st.Read().QueryContext(ctx, `SELECT key, value FROM notify_settings`)
	if err != nil {
		return DefaultSettings
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			return DefaultSettings
		}
		on := v == "true"
		switch k {
		case "desktop":
			out.Desktop = on
		case "telegram":
			out.Telegram = on
		case "failed_scans":
			out.FailedScans = on
		}
	}
	if rows.Err() != nil {
		return DefaultSettings
	}
	return out
}

// Patch applies the given keys atomically.
func (s *SettingsStore) Patch(ctx context.Context, p SettingsPatch) (Settings, error) {
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		for k, v := range map[string]*bool{"desktop": p.Desktop, "telegram": p.Telegram, "failed_scans": p.FailedScans} {
			if v == nil {
				continue
			}
			val := "false"
			if *v {
				val = "true"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO notify_settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, k, val, ts(s.now())); err != nil {
				return fmt.Errorf("save %s: %w", k, err)
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return Settings{}, err
	}
	return s.Get(ctx), err
}
