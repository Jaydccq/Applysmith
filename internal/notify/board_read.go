// Clean-room: docs/provenance.md#CR-RUNS-BOARD

package notify

import (
	"context"
	"strings"
	"time"
)

// Bounds of the list read.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// Liveness and retention windows of the board.
const (
	StaleAfter = 5 * time.Minute
	// sweepPause is the time between two sweeps that means the process did not
	// run (a host asleep or starved): sweeps come with every tick, at most a
	// minute apart.
	sweepPause      = 2 * time.Minute
	DoneRetention   = 14 * 24 * time.Hour
	WaitingLifetime = 30 * 24 * time.Hour
)

// Filter selects entries. With no state, acknowledgement or Since filter the
// default view applies: idle entries and acknowledged finished entries are
// hidden.
type Filter struct {
	States []State
	Kinds  []Kind
	Acked  *bool
	Batch  string
	// Since keeps the entries that ended or stopped to wait at or after it,
	// acknowledged ones too. The end is the run record's when there is one,
	// the time the day's counts read, else the entry's.
	Since time.Time
	// Tabbed keeps the entries that offer a kept tab.
	Tabbed bool
	Limit  int
}

// List returns entries, most recently updated first.
func (b *Board) List(ctx context.Context, f Filter) ([]Entry, error) {
	var where []string
	var args []any
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		where = append(where, col+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	states := make([]string, len(f.States))
	for i, s := range f.States {
		states[i] = string(s)
	}
	kinds := make([]string, len(f.Kinds))
	for i, k := range f.Kinds {
		kinds[i] = string(k)
	}
	in("state", states)
	in("kind", kinds)
	if f.Acked != nil {
		if *f.Acked {
			where = append(where, "acked_at IS NOT NULL")
		} else {
			where = append(where, "acked_at IS NULL")
		}
	}
	if f.Batch != "" {
		where = append(where, "batch_id = ?")
		args = append(args, f.Batch)
	}
	if f.Tabbed {
		where = append(where, "focus_tab <> ''")
	}
	if !f.Since.IsZero() {
		where = append(where, "state IN ('needs-you', 'blocked', 'done')",
			"COALESCE((SELECT r.finished_at FROM runs r WHERE r.id = board_entries.id), finished_at, state_at) >= ?")
		args = append(args, ts(f.Since))
	}
	if len(f.States) == 0 && f.Acked == nil && f.Since.IsZero() {
		where = append(where, "state <> 'idle'", "NOT (state = 'done' AND acked_at IS NOT NULL)")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	q := `SELECT ` + entryColumns + ` FROM board_entries`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY updated_at DESC, id LIMIT ?`
	args = append(args, limit)
	return b.listed(ctx, q, args...)
}

// listed is a read of entries shown by the names their postings are known by.
func (b *Board) listed(ctx context.Context, q string, args ...any) ([]Entry, error) {
	es, err := b.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return b.shown(ctx, es), nil
}

func (b *Board) query(ctx context.Context, q string, args ...any) ([]Entry, error) {
	rows, err := b.st.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Inbox lists the entries that wait for the user: needs-you before blocked, and
// within each group the one waiting longest first. An acknowledged entry is not
// in it.
func (b *Board) Inbox(ctx context.Context) ([]Entry, error) {
	return b.listed(ctx, `SELECT `+entryColumns+` FROM board_entries WHERE state IN ('needs-you', 'blocked') AND acked_at IS NULL
		ORDER BY CASE state WHEN 'needs-you' THEN 0 ELSE 1 END, state_at, id`)
}

// Summary counts the board for the navigation badge and the tray.
type Summary struct {
	Working   int
	NeedsYou  int
	Blocked   int
	DoneUnack int
	Unseen    int // unseen entries of the inbox
}

// Summary counts working entries, the inbox, unacknowledged finished entries
// and the unseen inbox entries.
func (b *Board) Summary(ctx context.Context) (Summary, error) {
	var s Summary
	err := b.st.Read().QueryRowContext(ctx, `SELECT
		COALESCE(SUM(state = 'working'), 0),
		COALESCE(SUM(state = 'needs-you' AND acked_at IS NULL), 0),
		COALESCE(SUM(state = 'blocked' AND acked_at IS NULL), 0),
		COALESCE(SUM(state = 'done' AND acked_at IS NULL), 0),
		COALESCE(SUM(state IN ('needs-you', 'blocked') AND acked_at IS NULL AND seen_at IS NULL), 0)
		FROM board_entries`).Scan(&s.Working, &s.NeedsYou, &s.Blocked, &s.DoneUnack, &s.Unseen)
	return s, err
}

// Members returns the entries of a batch.
func (b *Board) Members(ctx context.Context, batch string) ([]Entry, error) {
	return b.listed(ctx, `SELECT `+entryColumns+` FROM board_entries WHERE batch_id = ? ORDER BY started_at, id`, batch)
}

// Restart finalizes every entry a previous process left working as done with
// outcome interrupted. Waiting entries are kept so the user can still act.
func (b *Board) Restart(ctx context.Context) (int, error) {
	return b.finalize(ctx, `SELECT id FROM board_entries WHERE state = 'working'`)
}

// SweepStale finalizes working entries that no producer touched for five
// minutes. While the process did not run its producers could not touch
// either, so after such a pause nothing is finalized until five minutes of
// running time have passed.
func (b *Board) SweepStale(ctx context.Context) (int, error) {
	now := b.o.Now()
	b.sweepMu.Lock()
	if !b.swept.IsZero() && now.Sub(b.swept) > sweepPause {
		b.resumed = now
	}
	b.swept = now
	waiting := !b.resumed.IsZero() && now.Sub(b.resumed) < StaleAfter
	b.sweepMu.Unlock()
	if waiting {
		return 0, nil
	}
	return b.finalize(ctx, `SELECT id FROM board_entries WHERE state = 'working' AND updated_at < ?`, ts(now.Add(-StaleAfter)))
}

func (b *Board) finalize(ctx context.Context, q string, args ...any) (int, error) {
	ids, err := b.ids(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if _, err := b.Move(ctx, id, Done, MoveArgs{Outcome: OutcomeInterrupted, Detail: "The work stopped before it finished."}); err == nil {
			n++
		}
	}
	return n, nil
}

func (b *Board) ids(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := b.st.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Retain applies the retention rules: finished entries are deleted 14 days
// after they finished, acknowledged or not, and waiting entries older than 30
// days are finalized as interrupted.
func (b *Board) Retain(ctx context.Context) error {
	now := b.o.Now()
	stale, err := b.ids(ctx, `SELECT id FROM board_entries WHERE state IN ('needs-you', 'blocked') AND state_at < ?`, ts(now.Add(-WaitingLifetime)))
	if err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := b.Move(ctx, id, Done, MoveArgs{Outcome: OutcomeInterrupted, Detail: "No one acted on this for 30 days."}); err != nil {
			b.o.Log.Warn("retention could not finalize an entry", "entry", id, "error", err.Error())
		}
	}
	old, err := b.ids(ctx, `SELECT id FROM board_entries WHERE state = 'done' AND finished_at < ?`, ts(now.Add(-DoneRetention)))
	if err != nil {
		return err
	}
	for _, id := range old {
		if err := b.Delete(ctx, id); err != nil && err != ErrNotFound {
			return err
		}
	}
	return nil
}
