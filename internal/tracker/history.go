package tracker

import "context"

// History is what the tracker knows of the user's own history with companies,
// archived rows included: the companies of the rows applied to (an
// application date, or a status only an application leads to) and of the rows
// that reached an interview. The matcher reads them as the user's prior
// applications and interviews beside the ones the profile records, so a list
// the user marks complete means "nothing beyond the tracker".
type History struct{ Applied, Interviewed []string }

// History reads the applied and interviewed companies.
func (t *Tracker) History(ctx context.Context) (History, error) {
	var h History
	var err error
	if h.Applied, err = t.companies(ctx, `SELECT DISTINCT company FROM tracker_entries WHERE trim(company) <> ''
		AND (coalesce(applied_at, '') <> '' OR status IN ('applied', 'responded', 'interview', 'offer', 'rejected')) ORDER BY company`); err != nil {
		return History{}, err
	}
	if h.Interviewed, err = t.companies(ctx, `SELECT DISTINCT t.company FROM tracker_entries t WHERE trim(t.company) <> ''
		AND (t.status IN ('interview', 'offer') OR EXISTS (SELECT 1 FROM tracker_events e WHERE e.entry_id = t.id AND e.new_status IN ('interview', 'offer')))
		ORDER BY t.company`); err != nil {
		return History{}, err
	}
	return h, nil
}

func (t *Tracker) companies(ctx context.Context, query string) ([]string, error) {
	rows, err := t.s.Read().QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
