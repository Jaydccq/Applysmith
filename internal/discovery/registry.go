package discovery

import (
	"context"
	"database/sql"

	"fmt"
	"strings"
	"time"
)

// Registry statuses.
const (
	StatusActive  = "active"
	StatusDead    = "dead"
	StatusBlocked = "blocked"
)

const (
	deadStrikes = 3
	probeEvery  = 30 * 24 * time.Hour
	pruneAfter  = 180 * 24 * time.Hour
)

// RegistryRow is one (ATS, slug) board.
type RegistryRow struct {
	Board
	Source       string
	FirstAdded   time.Time
	LastScanned  time.Time // zero when never scanned
	LastJobCount int
	ETag         string
	Status       string
	Strikes      int
	DeadSince    time.Time
}

// RegistryAdd inserts boards that are not in the registry and leaves existing
// rows unchanged. It returns how many rows were new.
func (r *Repo) RegistryAdd(ctx context.Context, source string, boards ...Board) (int, error) {
	added := 0
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		for _, b := range boards {
			if !validATS(b.ATS) || !ValidSlug(b.Slug) {
				return fmt.Errorf("registry: %q is not a valid board", b.Key())
			}
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ats_registry (ats, slug, company, source, first_added_at) VALUES (?, ?, ?, ?, ?)`,
				b.ATS, strings.ToLower(b.Slug), b.Company, source, r.stamp())
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			added += int(n)
		}
		return nil
	})
	return added, err
}

// RegistryHarvest adds the boards the URLs name, if any, in one write, without
// touching an existing row.
func (r *Repo) RegistryHarvest(ctx context.Context, source string, rawURLs ...string) error {
	seen := map[string]bool{}
	var boards []Board
	for _, raw := range rawURLs {
		if b, err := ResolveBoard(raw); err == nil && !seen[b.Key()] {
			seen[b.Key()] = true
			boards = append(boards, b)
		}
	}
	if len(boards) == 0 {
		return nil
	}
	_, err := r.RegistryAdd(ctx, source, boards...)
	return err
}

const registryCols = `ats, slug, company, source, first_added_at, last_scanned_at, last_job_count, etag, status, strikes, dead_since`

func scanRegistry(rows *sql.Rows) ([]RegistryRow, error) {
	defer rows.Close()
	var out []RegistryRow
	for rows.Next() {
		var x RegistryRow
		var added string
		var scanned, dead sql.NullString
		if err := rows.Scan(&x.ATS, &x.Slug, &x.Company, &x.Source, &added, &scanned, &x.LastJobCount, &x.ETag, &x.Status, &x.Strikes, &dead); err != nil {
			return nil, err
		}
		x.FirstAdded, _ = time.Parse(timeLayout, added)
		x.LastScanned, x.DeadSince = parseStamp(scanned), parseStamp(dead)
		out = append(out, x)
	}
	return out, rows.Err()
}

// RegistryActive lists active boards, least recently scanned first.
func (r *Repo) RegistryActive(ctx context.Context) ([]RegistryRow, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+registryCols+` FROM ats_registry WHERE status = 'active'
		ORDER BY last_scanned_at IS NOT NULL, last_scanned_at, ats, slug`)
	if err != nil {
		return nil, err
	}
	return scanRegistry(rows)
}

// RegistryProbeDue lists dead boards whose 30-day probe is due.
func (r *Repo) RegistryProbeDue(ctx context.Context) ([]RegistryRow, error) {
	cutoff := r.now().Add(-probeEvery).UTC().Format(timeLayout)
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+registryCols+` FROM ats_registry WHERE status = 'dead'
		AND (last_scanned_at IS NULL OR last_scanned_at <= ?) ORDER BY ats, slug`, cutoff)
	if err != nil {
		return nil, err
	}
	return scanRegistry(rows)
}

// RegistryGet returns one row.
func (r *Repo) RegistryGet(ctx context.Context, b Board) (RegistryRow, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+registryCols+` FROM ats_registry WHERE ats = ? AND slug = ?`, b.ATS, b.Slug)
	if err != nil {
		return RegistryRow{}, err
	}
	list, err := scanRegistry(rows)
	if err != nil {
		return RegistryRow{}, err
	}
	if len(list) == 0 {
		return RegistryRow{}, sql.ErrNoRows
	}
	return list[0], nil
}

// RegistryVisit is the outcome of one visit to a board.
type RegistryVisit int

// The visit outcomes that change a row. A transient error changes nothing.
const (
	VisitOK RegistryVisit = iota
	VisitNotModified
	VisitFailed
)

// RegistryRecord applies a visit: a successful non-empty visit resets the
// strikes and revives a dead row, a 304 only stamps the scan time, a failure
// adds a strike and retires the row at three. A blocked row never changes.
func (r *Repo) RegistryRecord(ctx context.Context, b Board, v RegistryVisit, jobs int, etag string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		now := r.stamp()
		var q string
		var args []any
		switch v {
		case VisitOK:
			q = `UPDATE ats_registry SET strikes = 0, status = 'active', dead_since = NULL, last_scanned_at = ?, last_job_count = ?, etag = ? WHERE ats = ? AND slug = ? AND status <> 'blocked'`
			args = []any{now, jobs, etag, b.ATS, b.Slug}
		case VisitNotModified:
			q = `UPDATE ats_registry SET last_scanned_at = ? WHERE ats = ? AND slug = ? AND status <> 'blocked'`
			args = []any{now, b.ATS, b.Slug}
		case VisitFailed:
			q = `UPDATE ats_registry SET strikes = strikes + 1, last_scanned_at = ?,
				dead_since = CASE WHEN status = 'active' AND strikes + 1 >= ? THEN ? ELSE dead_since END,
				status = CASE WHEN status = 'active' AND strikes + 1 >= ? THEN 'dead' ELSE status END
				WHERE ats = ? AND slug = ? AND status <> 'blocked'`
			args = []any{now, deadStrikes, now, deadStrikes, b.ATS, b.Slug}
		}
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	})
}

// RegistryStats counts rows by status.
func (r *Repo) RegistryStats(ctx context.Context) (map[string]int, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT status, COUNT(*) FROM ats_registry GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{StatusActive: 0, StatusDead: 0, StatusBlocked: 0}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// RegistryPrune deletes dead rows older than 180 days that were never revived.
func (r *Repo) RegistryPrune(ctx context.Context) (int, error) {
	n := 0
	cutoff := r.now().Add(-pruneAfter).UTC().Format(timeLayout)
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM ats_registry WHERE status = 'dead' AND dead_since IS NOT NULL AND dead_since <= ?`, cutoff)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}

// RegistrySetStatus revives a dead row or blocks a row.
func (r *Repo) RegistrySetStatus(ctx context.Context, b Board, status string) error {
	if status != StatusActive && status != StatusBlocked {
		return fmt.Errorf("registry: cannot set status %q", status)
	}
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE ats_registry SET status = ?, strikes = 0, dead_since = NULL WHERE ats = ? AND slug = ?`, status, b.ATS, b.Slug)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("registry: %s is not in the registry", b.Key())
		}
		return nil
	})
}

// RegistryLastSeeded is the time of the newest seed download, zero when the
// registry was never seeded.
func (r *Repo) RegistryLastSeeded(ctx context.Context) (time.Time, error) {
	var at sql.NullString
	err := r.s.Read().QueryRowContext(ctx, `SELECT MAX(retrieved_at) FROM data_sources WHERE kind = 'registry'`).Scan(&at)
	return parseStamp(at), err
}
