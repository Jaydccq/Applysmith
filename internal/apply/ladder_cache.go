package apply

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/store"
)

// declineTTL is how long a rung 2 decline is not paid for again.
const declineTTL = 30 * 24 * time.Hour

// Declines remembers the questions rung 2 declined. A decline is reused for the
// same question on the same system for 30 days, only for the same clean build,
// and only while none of the answer bank, the profile and the CV was saved
// since; a build with local modifications never vouches. A question declined
// by two distinct runs inside the window is a repeat, which the run reports so
// the user is asked once. The memory is kept in the store, so it survives a
// restart; Load reads it at the start of a run.
type Declines struct {
	mu  sync.Mutex
	st  *store.Store // nil keeps the memory for the process only
	now func() time.Time
	m   map[string]*declineRec
}

type declineRec struct {
	build, rev string
	runs       map[string]time.Time
}

// NewDeclines returns a memory backed by st; a nil store keeps it for the
// process only.
func NewDeclines(st *store.Store, now func() time.Time) *Declines {
	if now == nil {
		now = time.Now
	}
	return &Declines{st: st, now: now, m: map[string]*declineRec{}}
}

// Load replaces the in-memory view with the stored declines that are still
// inside the window and removes the expired rows.
func (d *Declines) Load(ctx context.Context) error {
	if d == nil || d.st == nil {
		return nil
	}
	cutoff := d.now().Add(-declineTTL).UTC().Format(stampLayout)
	if err := d.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM apply_declines WHERE declined_at < ?1`, cutoff)
		return err
	}); err != nil {
		return err
	}
	rows, err := d.st.Read().QueryContext(ctx, `SELECT decline_key, build, revision, run_id, declined_at FROM apply_declines ORDER BY declined_at, rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]*declineRec{}
	for rows.Next() {
		var key, build, rev, run, at string
		if err := rows.Scan(&key, &build, &rev, &run, &at); err != nil {
			return err
		}
		when, err := time.Parse(stampLayout, at)
		if err != nil {
			continue
		}
		rec := m[key]
		if rec == nil || rec.build != build || rec.rev != rev {
			rec = &declineRec{build: build, rev: rev, runs: map[string]time.Time{}}
			m[key] = rec
		}
		rec.runs[run] = when
	}
	if err := rows.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	d.m = m
	d.mu.Unlock()
	return nil
}

// cleanBuild reports a build id that vouches for a decline: a build with local
// modifications, or none, never does.
func cleanBuild(b string) bool {
	l := strings.ToLower(b)
	return b != "" && !strings.Contains(l, "dirty") && !strings.Contains(l, "modified") && !strings.Contains(l, "+")
}

// declineKey names a question on a system: the host, the normalized label, the
// control kind and the option set.
func declineKey(host, label, kind string, options []string) string {
	opts := make([]string, len(options))
	for i, o := range options {
		opts[i] = normText(o)
	}
	sort.Strings(opts)
	return strings.Join([]string{strings.ToLower(host), normText(match.Sanitize(label, nil)), kind, strings.Join(opts, "|")}, "\x1f")
}

// Held reports whether the question was declined recently enough, under this
// build and this revision, to skip paying for it again.
func (d *Declines) Held(key, build, rev string) bool {
	if d == nil || !cleanBuild(build) || rev == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rec := d.m[key]
	if rec == nil || rec.build != build || rec.rev != rev {
		return false
	}
	now := d.now()
	for _, at := range rec.runs {
		if now.Sub(at) <= declineTTL {
			return true
		}
	}
	return false
}

// Record remembers a decline by a run and reports whether it is the second
// distinct run to decline the question inside the window.
func (d *Declines) Record(ctx context.Context, key, build, rev, runID string) (repeat bool, err error) {
	if d == nil {
		return false, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	rec := d.m[key]
	if rec == nil || rec.build != build || rec.rev != rev {
		rec = &declineRec{build: build, rev: rev, runs: map[string]time.Time{}}
		d.m[key] = rec
	}
	for id, at := range rec.runs {
		if now.Sub(at) > declineTTL {
			delete(rec.runs, id)
		}
	}
	rec.runs[runID] = now
	repeat = len(rec.runs) >= 2
	if d.st == nil {
		return repeat, nil
	}
	err = d.st.Write(ctx, func(tx *sql.Tx) error {
		// A decline under another build or revision no longer vouches; it goes.
		if _, err := tx.ExecContext(ctx, `DELETE FROM apply_declines WHERE decline_key = ?1 AND (build <> ?2 OR revision <> ?3 OR declined_at < ?4)`,
			key, build, rev, now.Add(-declineTTL).UTC().Format(stampLayout)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO apply_declines (decline_key, run_id, build, revision, declined_at) VALUES (?1, ?2, ?3, ?4, ?5)
			ON CONFLICT (decline_key, run_id) DO UPDATE SET build = ?3, revision = ?4, declined_at = ?5`,
			key, runID, build, rev, now.UTC().Format(stampLayout))
		return err
	})
	return repeat, err
}
