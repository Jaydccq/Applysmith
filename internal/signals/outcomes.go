package signals

import (
	"context"
	"database/sql"
	"sort"
)

// OutcomeStats is the hit rate of the ledger: overall, by the type of the
// assessment, and the median lead time of the hits.
type OutcomeStats struct {
	Hits       int                `json:"hits"`
	Misses     int                `json:"misses"`
	Pending    int                `json:"pending"`
	HitRate    float64            `json:"hitRate"`
	MedianLead float64            `json:"medianLeadDays"`
	ByType     map[string]TypeHit `json:"byType"`
}

// TypeHit is the hit rate of one assessed event type.
type TypeHit struct {
	Hits    int     `json:"hits"`
	Misses  int     `json:"misses"`
	Pending int     `json:"pending"`
	HitRate float64 `json:"hitRate"`
}

// UpdateOutcomes opens a 90-day window for every ok assessment and settles the
// open ones: a hit with the lead time in days when a gate-passing posting was
// first seen inside the window, a miss when the window closed with none. An
// open window stays pending and is checked again.
func (r *Repo) UpdateOutcomes(ctx context.Context) error {
	now := r.now()
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO signal_outcomes (company_id, event_id, event_type, window_start, window_end)
			SELECT e.company_id, e.id, a.event_type, e.event_time, strftime('%Y-%m-%dT%H:%M:%SZ', e.event_time, '+90 days')
			FROM signal_assessments a JOIN signal_events e ON e.id = a.event_id WHERE a.status = 'ok' AND a.rubric_version = ?
			ON CONFLICT (company_id, event_id) DO NOTHING`, RubricVersion); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT company_id, event_id, window_start, window_end FROM signal_outcomes WHERE status = 'pending'`)
		if err != nil {
			return err
		}
		type open struct {
			company, event int64
			start, end     string
		}
		var list []open
		for rows.Next() {
			var o open
			if err := rows.Scan(&o.company, &o.event, &o.start, &o.end); err != nil {
				rows.Close()
				return err
			}
			list = append(list, o)
		}
		rows.Close()
		for _, o := range list {
			var pid int64
			var seen string
			err := tx.QueryRowContext(ctx, `SELECT id, first_seen_at FROM signal_postings WHERE company_id = ? AND verdict = 'pass' AND first_seen_at >= ? AND first_seen_at <= ?
				ORDER BY first_seen_at, id LIMIT 1`, o.company, o.start, o.end).Scan(&pid, &seen)
			switch {
			case err == nil:
				lead := int(parseTS(seen).Sub(parseTS(o.start)).Hours() / 24)
				if _, err := tx.ExecContext(ctx, `UPDATE signal_outcomes SET status = 'hit', lead_days = ?, posting_id = ?, resolved_at = ? WHERE company_id = ? AND event_id = ?`,
					lead, pid, fmtTS(now), o.company, o.event); err != nil {
					return err
				}
			case err == sql.ErrNoRows:
				if now.After(parseTS(o.end)) {
					if _, err := tx.ExecContext(ctx, `UPDATE signal_outcomes SET status = 'miss', resolved_at = ? WHERE company_id = ? AND event_id = ?`, fmtTS(now), o.company, o.event); err != nil {
						return err
					}
				}
			default:
				return err
			}
		}
		return nil
	})
}

// OutcomeStats reads the ledger.
func (r *Repo) OutcomeStats(ctx context.Context) (OutcomeStats, error) {
	st := OutcomeStats{ByType: map[string]TypeHit{}}
	rows, err := r.s.Read().QueryContext(ctx, `SELECT event_type, status, COALESCE(lead_days, 0) FROM signal_outcomes`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	var leads []float64
	for rows.Next() {
		var typ, status string
		var lead int
		if err := rows.Scan(&typ, &status, &lead); err != nil {
			return st, err
		}
		t := st.ByType[typ]
		switch status {
		case "hit":
			st.Hits++
			t.Hits++
			leads = append(leads, float64(lead))
		case "miss":
			st.Misses++
			t.Misses++
		default:
			st.Pending++
			t.Pending++
		}
		st.ByType[typ] = t
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.HitRate = rate(st.Hits, st.Misses)
	for k, t := range st.ByType {
		t.HitRate = rate(t.Hits, t.Misses)
		st.ByType[k] = t
	}
	if n := len(leads); n > 0 {
		sort.Float64s(leads)
		st.MedianLead = leads[n/2]
		if n%2 == 0 {
			st.MedianLead = (leads[n/2-1] + leads[n/2]) / 2
		}
	}
	return st, nil
}

func rate(hits, misses int) float64 {
	if hits+misses == 0 {
		return 0
	}
	return float64(hits) / float64(hits+misses)
}
