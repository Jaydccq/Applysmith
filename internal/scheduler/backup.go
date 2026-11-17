package scheduler

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// The daily snapshot timer writes the same verified backup `applysmith backup
// create` does, into a directory outside the workspace, and keeps a bounded
// number of the ones it wrote.
const (
	// SnapshotHour is the local hour of the daily snapshot.
	SnapshotHour = 2
	// SnapshotKeep is how many snapshots are kept, and SnapshotBudget the
	// soft size limit above which older ones go too, always leaving two.
	SnapshotKeep   = 14
	SnapshotBudget = 2 << 30

	snapshotMeta  = "scheduler.snapshot"
	snapshotRetry = time.Hour
	snapshotLimit = 10 * time.Minute
)

var snapshotName = lazyregexp.New(`^snapshot-\d{8}-\d{6}$`)

// SnapshotOptions configure the timer.
type SnapshotOptions struct {
	// Root is the workspace whose files are copied; Dir receives the
	// snapshots and must lie outside Root.
	Root, Dir, Version string
	// Keep, Budget and Hour replace the defaults above when set.
	Keep   int
	Budget int64
	Hour   int
}

// snapshotState is the record of the last outcome, kept in workspace_meta.
type snapshotState struct {
	Slot  string `json:"slot"`  // the 02:00 slot last completed
	At    string `json:"at"`    // when that snapshot finished
	Path  string `json:"path"`  // where it is
	Error string `json:"error"` // why the last attempt failed
	Retry string `json:"retry"` // no attempt before this instant
}

type snapshotTimer struct {
	s  *Scheduler
	o  SnapshotOptions
	mu sync.Mutex
}

// AddSnapshots registers the daily user-data snapshot timer.
func (s *Scheduler) AddSnapshots(o SnapshotOptions) {
	o.Keep, o.Budget, o.Hour = cmp.Or(o.Keep, SnapshotKeep), cmp.Or(o.Budget, SnapshotBudget), cmp.Or(o.Hour, SnapshotHour)
	s.AddTimer(&snapshotTimer{s: s, o: o})
}

func (t *snapshotTimer) Name() string { return "user-data-snapshot" }
func (t *snapshotTimer) Describe() string {
	return fmt.Sprintf("Once a local day, from %02d:00: writes a verified backup of the database and your files to %s, keeping the last %d",
		t.o.Hour, t.o.Dir, t.o.Keep)
}
func (t *snapshotTimer) Status(context.Context, time.Time) TimerStatus {
	if t.o.Dir == "" {
		return TimerStatus{Reason: "no snapshot directory"}
	}
	return TimerStatus{Active: true}
}
func (t *snapshotTimer) Limit() time.Duration { return snapshotLimit }

// Tick takes one snapshot per slot. A machine asleep at the slot takes it at
// its first tick after waking; a failure is retried after an hour and the
// slot stays open until one succeeds. Stopping the scheduler cancels a
// snapshot in progress and removes its partial output.
func (t *snapshotTimer) Tick(ctx context.Context, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	slot := dailySlot(t.s.o.Zone, now, t.o.Hour)
	st, err := t.load(ctx)
	if err != nil {
		return err
	}
	if (st.Slot != "" && !parseTS(st.Slot).Before(slot)) || now.Before(parseTS(st.Retry)) {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(t.s.runCtx, cancel)()

	out := filepath.Join(t.o.Dir, "snapshot-"+now.In(t.s.o.Zone).Format("20060102-150405"))
	err = os.MkdirAll(t.o.Dir, 0o700)
	if err == nil {
		_, err = t.s.o.Store.Backup(ctx, store.BackupOptions{Out: out, WorkspaceRoot: t.o.Root, ProductVersion: t.o.Version, Now: t.s.clock.Now})
	}
	if err != nil {
		st.Error, st.Retry = err.Error(), ts(now.Add(snapshotRetry))
		return errors.Join(fmt.Errorf("the daily snapshot failed; retrying in an hour: %w", err), t.save(ctx, st))
	}
	st = snapshotState{Slot: ts(slot), At: ts(t.s.clock.Now()), Path: out}
	if err := t.save(ctx, st); err != nil {
		return err
	}
	if _, err := PruneSnapshots(t.o.Dir, t.o.Keep, t.o.Budget); err != nil {
		return fmt.Errorf("pruning old snapshots: %w", err)
	}
	return nil
}

func (t *snapshotTimer) load(ctx context.Context) (snapshotState, error) {
	var raw string
	err := t.s.o.Store.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, snapshotMeta).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotState{}, nil
	}
	var st snapshotState
	if err == nil && json.Unmarshal([]byte(raw), &st) != nil {
		st = snapshotState{} // an unreadable record is a snapshot that never happened
	}
	return st, err
}

func (t *snapshotTimer) save(ctx context.Context, st snapshotState) error {
	return t.s.setMeta(context.WithoutCancel(ctx), snapshotMeta, marshal(st))
}

// PruneSnapshots removes the oldest snapshot directories of dir beyond keep,
// and beyond the soft size budget while more than two remain. It touches only
// directories named like the timer's own that hold a manifest, never anything
// the user put there. It returns how many it removed.
func PruneSnapshots(dir string, keep int, budget int64) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	type snap struct {
		path string
		size int64
	}
	var snaps []snap // ascending by name, so oldest first
	var total int64
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !e.IsDir() || !snapshotName.MatchString(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, "manifest.json")); err != nil {
			continue
		}
		var size int64
		_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if info, err := d.Info(); err == nil {
					size += info.Size()
				}
			}
			return nil
		})
		snaps, total = append(snaps, snap{p, size}), total+size
	}
	slices.SortFunc(snaps, func(a, b snap) int { return cmp.Compare(a.path, b.path) })
	floor := min(keep, 2)
	removed := 0
	for len(snaps) > keep || (len(snaps) > floor && total > budget) {
		if err := os.RemoveAll(snaps[0].path); err != nil {
			return removed, err
		}
		total -= snaps[0].size
		snaps = snaps[1:]
		removed++
	}
	return removed, nil
}
