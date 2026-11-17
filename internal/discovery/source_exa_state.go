package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Exa state bounds.
const (
	exaSeenMax      = 5000
	exaSuspendAfter = 5
	exaSuspendFor   = 7 * day
)

// ExaState is what the Exa source remembers between runs.
type ExaState struct {
	// Watermark is the start of the last run whose queries all succeeded.
	Watermark time.Time `json:"watermark,omitempty"`
	// Seen holds the URLs already handled, oldest first.
	Seen []string `json:"seen,omitempty"`
	// Zero counts consecutive runs a query yielded nothing new.
	Zero map[string]int `json:"zero,omitempty"`
	// Suspended maps a query to the time its suspension ends.
	Suspended map[string]time.Time `json:"suspended,omitempty"`
	// Owed maps a query the spend cap or the row limit kept from running to
	// the time its next search starts from, so the window it missed is kept
	// when the watermark moves on. Owed queries run first.
	Owed map[string]time.Time `json:"owed,omitempty"`
}

// ExaStateGet reads the state; a workspace that never ran Exa has none.
func (r *Repo) ExaStateGet(ctx context.Context) (ExaState, error) {
	var body string
	err := r.s.Read().QueryRowContext(ctx, `SELECT state_json FROM exa_state WHERE id = 1`).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return ExaState{}, nil
	}
	if err != nil {
		return ExaState{}, err
	}
	var st ExaState
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		return ExaState{}, err
	}
	return st, nil
}

// ExaStateSave stores the state, keeping the newest 5,000 seen URLs.
func (r *Repo) ExaStateSave(ctx context.Context, st ExaState) error {
	if over := len(st.Seen) - exaSeenMax; over > 0 {
		st.Seen = append([]string(nil), st.Seen[over:]...)
	}
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO exa_state (id, state_json, updated_at) VALUES (1, ?, ?)
			ON CONFLICT (id) DO UPDATE SET state_json = excluded.state_json, updated_at = excluded.updated_at`, string(body), r.stamp())
		return err
	})
}

// ExaSeeds are the postings and companies the "similar" and "watch" queries
// start from.
type ExaSeeds struct {
	// Similar are posting URLs of reports scored 4.5 or higher in the last 30
	// days, at most 8.
	Similar []string
	// Watch are companies that answered an application, at most 8.
	Watch []string
}

// ExaSeedsGet reads the seeds from the tracker.
func (r *Repo) ExaSeedsGet(ctx context.Context) (ExaSeeds, error) {
	var seeds ExaSeeds
	since := r.now().Add(-30 * day).UTC().Format(timeLayout)
	rows, err := r.s.Read().QueryContext(ctx, `SELECT posting_url FROM tracker_entries
		WHERE score >= 4.5 AND posting_url <> '' AND archived_at IS NULL AND updated_at >= ? ORDER BY score DESC, updated_at DESC LIMIT 8`, since)
	if err != nil {
		return seeds, err
	}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return seeds, err
		}
		seeds.Similar = append(seeds.Similar, u)
	}
	if err := rows.Close(); err != nil {
		return seeds, err
	}
	rows, err = r.s.Read().QueryContext(ctx, `SELECT company FROM tracker_entries
		WHERE status IN ('responded', 'interview', 'offer') AND archived_at IS NULL GROUP BY lower(company) ORDER BY max(updated_at) DESC LIMIT 8`)
	if err != nil {
		return seeds, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return seeds, err
		}
		seeds.Watch = append(seeds.Watch, c)
	}
	return seeds, rows.Err()
}
