package tracker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Default ages for the archive sweep.
const (
	DefaultArchiveAge = 30 * 24 * time.Hour
	skippedArchiveAge = 60 * 24 * time.Hour
)

// lastChange is the time of a row's latest event that changed it. A refused
// automated attempt changed nothing, so it does not keep a closed row open.
const lastChange = `SELECT max(at) FROM tracker_events WHERE entry_id = e.id AND kind <> 'transition_refused'`

// ArchiveOptions selects the rows to archive.
type ArchiveOptions struct {
	// OlderThan is the age of the last event for rejected and discarded rows;
	// zero means 30 days.
	OlderThan time.Duration
	// IncludeSkipped adds skipped rows whose last event is over 60 days old.
	IncludeSkipped bool
	// DryRun returns the rows that would move and changes nothing.
	DryRun bool
}

func (a Actor) canArchive() bool { return a.valid() || a == ActorSystem }

// Archive sets archived_at on old terminal rows. Rows keep their ids, events
// and identity keys, so duplicate detection still sees them.
func (t *Tracker) Archive(ctx context.Context, actor Actor, opts ArchiveOptions) ([]Entry, error) {
	if opts.OlderThan == 0 {
		opts.OlderThan = DefaultArchiveAge
	}
	if opts.OlderThan < 0 {
		return nil, fmt.Errorf("%w: --older-than must be positive", ErrInvalid)
	}
	if !actor.canArchive() {
		return nil, fmt.Errorf("%w: unknown actor %q", ErrInvalid, actor)
	}
	rows := func(ctx context.Context, q querier) ([]Entry, error) {
		now := t.now()
		cut := func(age time.Duration) string { return now.Add(-age).UTC().Format(time.RFC3339) }
		text := `SELECT ` + entryColumns + ` FROM tracker_entries e WHERE archived_at IS NULL AND (
			(status IN ('rejected', 'discarded') AND (` + lastChange + `) < ?)`
		args := []any{cut(opts.OlderThan)}
		if opts.IncludeSkipped {
			text += ` OR (status = 'skipped' AND (` + lastChange + `) < ?)`
			args = append(args, cut(skippedArchiveAge))
		}
		return queryEntries(ctx, q, text+`) ORDER BY id`, args...)
	}
	if opts.DryRun {
		return rows(ctx, t.s.Read())
	}
	var out []Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if out, err = rows(ctx, tx); err != nil {
			return err
		}
		for i, e := range out {
			now := t.stamp()
			if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET archived_at = ?, version = version + 1, updated_at = ? WHERE id = ?`, now, now, e.ID); err != nil {
				return err
			}
			if err := t.addEvent(ctx, tx, e.ID, actor, EventArchived, "", "", nil); err != nil {
				return err
			}
			out[i].ArchivedAt = now
		}
		return nil
	})
	return out, err
}

// ArchiveRow archives one row the user picked. Only a closed or skipped row can
// be archived: a row still in flight stays on the lists until it is closed.
func (t *Tracker) ArchiveRow(ctx context.Context, id int64, actor Actor) (Entry, error) {
	return t.archiveOne(ctx, id, actor, true)
}

// Unarchive clears archived_at.
func (t *Tracker) Unarchive(ctx context.Context, id int64, actor Actor) (Entry, error) {
	return t.archiveOne(ctx, id, actor, false)
}

func (t *Tracker) archiveOne(ctx context.Context, id int64, actor Actor, archive bool) (Entry, error) {
	if !actor.canArchive() {
		return Entry{}, fmt.Errorf("%w: unknown actor %q", ErrInvalid, actor)
	}
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := t.setArchived(ctx, tx, id, actor, archive); err != nil {
			return err
		}
		var err error
		out, err = getEntry(ctx, tx, id)
		return err
	})
	return out, err
}

// BulkArchive archives (or with archive false unarchives) the ticked rows in
// one transaction. changed are the rows it moved; open are the rows still in
// flight it left, which only a close makes archivable. A row already where it
// should be is in neither.
func (t *Tracker) BulkArchive(ctx context.Context, ids []int64, actor Actor, archive bool) (changed, open []int64, err error) {
	if !actor.canArchive() {
		return nil, nil, fmt.Errorf("%w: unknown actor %q", ErrInvalid, actor)
	}
	err = t.s.Write(ctx, func(tx *sql.Tx) error {
		changed, open = nil, nil
		for _, id := range ids {
			moved, err := t.setArchived(ctx, tx, id, actor, archive)
			switch {
			case errors.Is(err, ErrInvalid):
				open = append(open, id)
			case err != nil:
				return err
			case moved:
				changed = append(changed, id)
			}
		}
		return nil
	})
	return changed, open, err
}

// setArchived moves one row in tx and says whether it moved. Archiving a row
// still in flight is ErrInvalid.
func (t *Tracker) setArchived(ctx context.Context, tx *sql.Tx, id int64, actor Actor, archive bool) (bool, error) {
	e, err := getEntry(ctx, tx, id)
	if err != nil || (e.ArchivedAt != "") == archive {
		return false, err
	}
	if archive && !e.Status.Terminal() && e.Status != Skipped {
		return false, fmt.Errorf("%w: row %d is %s; close or skip it before archiving", ErrInvalid, id, e.Status)
	}
	now, kind := any(t.stamp()), EventArchived
	if !archive {
		now, kind = nil, EventUnarchived
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET archived_at = ?, version = version + 1, updated_at = ? WHERE id = ?`, now, t.stamp(), id); err != nil {
		return false, err
	}
	return true, t.addEvent(ctx, tx, id, actor, kind, "", "", nil)
}
