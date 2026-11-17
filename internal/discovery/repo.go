package discovery

import (
	"database/sql"
	"time"

	"applysmith/internal/store"
)

// Repo reads and writes the discovery tables of one workspace database.
type Repo struct {
	s     *store.Store
	now   func() time.Time
	basis RankBasis
}

// NewRepo returns a Repo over an open store. now defaults to the wall clock.
func NewRepo(s *store.Store, now func() time.Time) *Repo {
	if now == nil {
		now = time.Now
	}
	return &Repo{s: s, now: now}
}

const timeLayout = time.RFC3339

func (r *Repo) stamp() string { return r.now().UTC().Format(timeLayout) }

func fmtTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func parseStamp(s sql.NullString) time.Time {
	if !s.Valid {
		return time.Time{}
	}
	t, _ := time.Parse(timeLayout, s.String)
	return t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
