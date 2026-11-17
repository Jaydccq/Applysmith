package discovery

import (
	"context"
	"database/sql"
)

// History statuses.
const (
	HistoryInbox     = "inbox"
	HistoryDuplicate = "duplicate"
)

func (r *Repo) historyUpsert(ctx context.Context, tx *sql.Tx, p Posting, status string, score *int, runID string) error {
	now := r.stamp()
	var sc any
	if score != nil {
		sc = *score
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO scan_history (canonical_url, identity_key, first_seen_at, last_seen_at, source, title, company, location, status, score, last_run_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (canonical_url) DO UPDATE SET last_seen_at = excluded.last_seen_at, last_run_id = excluded.last_run_id,
			status = CASE WHEN excluded.status = 'inbox' THEN 'inbox' ELSE scan_history.status END,
			score = COALESCE(excluded.score, scan_history.score)`,
		historyURL(p), p.key(), now, now, p.Source, p.Title, p.Company, p.Location, status, sc, runID)
	return err
}

func historyURL(p Posting) string {
	if p.URL != "" {
		return p.URL
	}
	return p.key()
}
