package apply

import (
	"context"
	"database/sql"
	"errors"
)

// Reports binds a run to the evaluated report of its posting.
type Reports interface {
	// Bind returns the report whose posting identity key equals key, or nil.
	// A failure to read is an error; no report is not.
	Bind(ctx context.Context, key string) (*ReportBinding, error)
}

// SQLReports binds from the report index and the tracker.
type SQLReports struct{ DB *sql.DB }

// Bind prefers the report named by a live tracker row when two reports carry
// the posting. Company and role come from that tracker row, else from the
// report's own title, and never from the URL's host. With no report it
// returns nil and the run proceeds.
func (s SQLReports) Bind(ctx context.Context, key string) (*ReportBinding, error) {
	type rep struct {
		n                          int64
		company, role, mdown, kind string
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT report_number, company, role, markdown_path, kind FROM report_index WHERE identity_key = ? ORDER BY report_number DESC`, key)
	if err != nil {
		return nil, err
	}
	var reps []rep
	for rows.Next() {
		var r rep
		if err := rows.Scan(&r.n, &r.company, &r.role, &r.mdown, &r.kind); err != nil {
			rows.Close()
			return nil, err
		}
		reps = append(reps, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(reps) == 0 {
		return nil, nil
	}
	var tid, tnum sql.NullInt64
	var tcompany, trole string
	err = s.DB.QueryRowContext(ctx, `SELECT id, report_number, company, role FROM tracker_entries
		WHERE archived_at IS NULL AND (identity_key = ?1 OR id IN (SELECT entry_id FROM tracker_aliases WHERE identity_key = ?1))
		ORDER BY (report_number IS NOT NULL) DESC, id LIMIT 1`, key).Scan(&tid, &tnum, &tcompany, &trole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	pick := reps[0]
	b := &ReportBinding{}
	if err == nil && tnum.Valid {
		for _, r := range reps {
			if r.n == tnum.Int64 {
				pick = r
			}
		}
		b.TrackerID, b.Company, b.Role = tid.Int64, tcompany, trole
	}
	b.Number, b.Path, b.Kind = pick.n, pick.mdown, pick.kind
	if b.Company == "" {
		b.Company, b.Role = pick.company, pick.role
	}
	return b, nil
}
