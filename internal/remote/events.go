package remote

import (
	"context"
	"database/sql"
	"time"
)

// Event kinds of the audit.
const (
	EventModeChange  = "mode_change"
	EventPairOffer   = "pair_offer"
	EventPairClaim   = "pair_claim"
	EventPairFailed  = "pair_failed"
	EventRevoke      = "revoke"
	EventDeviceLimit = "device_limit"
	EventAuthBlock   = "auth_block"
)

// EventRetention is how long an event is kept.
const EventRetention = 90 * 24 * time.Hour

// Event is one audit row. It holds no code, token or cookie value.
type Event struct {
	At       time.Time
	Kind     string
	DeviceID string
	Source   string
	Detail   string
}

// Record writes one event.
func (d *Devices) Record(ctx context.Context, kind, deviceID, source, detail string) error {
	return d.db.Write(ctx, func(tx *sql.Tx) error {
		var dev any
		if deviceID != "" {
			dev = deviceID
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO remote_events (at, kind, device_id, source, detail) VALUES (?, ?, ?, ?, ?)`,
			stamp(d.now()), kind, dev, source, detail)
		return err
	})
}

// Events returns the latest events, newest first.
func (d *Devices) Events(ctx context.Context, limit int) ([]Event, error) {
	rows, err := d.db.Read().QueryContext(ctx, `SELECT at, kind, device_id, source, detail FROM remote_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var at, kind, source, detail string
		var dev sql.NullString
		if err := rows.Scan(&at, &kind, &dev, &source, &detail); err != nil {
			return nil, err
		}
		out = append(out, Event{At: parseStamp(at), Kind: kind, DeviceID: dev.String, Source: source, Detail: detail})
	}
	return out, rows.Err()
}

// Sweep deletes the events older than the retention period.
func (d *Devices) Sweep(ctx context.Context) error {
	cutoff := stamp(d.now().Add(-EventRetention))
	return d.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM remote_events WHERE at < ?`, cutoff)
		return err
	})
}
