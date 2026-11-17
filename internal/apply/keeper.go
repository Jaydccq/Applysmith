package apply

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"applysmith/internal/tracker"
)

// TabLane is the apply lane as the keeper needs it: the ids of the tabs the
// lane lists now, closing one by id, and bringing one forward.
type TabLane interface {
	LiveTabs(ctx context.Context) ([]string, error)
	CloseTabID(ctx context.Context, id string) error
	ShowTab(ctx context.Context, id string) error
}

// Laned is a tab or a window that knows its lane.
type Laned interface{ Handle() TabLane }

// KeptRow is a tab the runs board still offers to jump to. Waiting is true
// while the run needs the user.
type KeptRow struct {
	RunID, TabID string
	Since        time.Time
	Waiting      bool
}

// KeptBoard is what the keeper reads of the runs board and the one thing it
// tells it: a row whose tab is gone stops offering the jump.
type KeptBoard interface {
	Rows() []KeptRow
	Cleared(runID string)
}

const (
	maxKept     = 3
	liveTabsTTL = 3 * time.Second
	laneReadMax = 5 * time.Second
)

// Keeper bounds the tabs kept for the user and answers whether one is still
// open. Everything it does is bookkeeping: a fault in it never reaches the
// run's decision, class or ledger row.
type Keeper struct {
	// Max is the cap of kept tabs; zero means 3.
	Max int
	// Board is the runs board; nil in a command-line run.
	Board KeptBoard
	// Watch learns how the user finishes a kept tab; nil never watches, which
	// is every test and every command-line run.
	Watch *Watcher
	// TTL is how long a read of the lane's tab list answers; zero means 3 s.
	TTL time.Duration
	Now func() time.Time

	// mu guards list, lane and view for a moment at a time: it is never held
	// across a call into the lane, a tab or the board, so a status read
	// (TabOf, IsOpen, Count) answers while a browser call is still pending.
	mu   sync.Mutex
	list []keptTab
	lane TabLane
	view liveView
	// admitMu lets one admission work at a time; only Admit takes it.
	admitMu sync.Mutex
}

type keptTab struct {
	tab   Tab
	id    string
	run   string
	since time.Time
}

// liveView is the last read of the lane's tab list.
type liveView struct {
	ids  map[string]bool
	read bool
	at   time.Time
	busy bool
}

func (k *Keeper) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *Keeper) max() int {
	if k.Max > 0 {
		return k.Max
	}
	return maxKept
}

func (k *Keeper) ttl() time.Duration {
	if k.TTL > 0 {
		return k.TTL
	}
	return liveTabsTTL
}

// UseLane tells the keeper which lane to read when no tab has been admitted
// yet in this process.
func (k *Keeper) UseLane(l TabLane) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.lane == nil {
		k.lane = l
	}
}

// Admit records a tab a run kept and closes the tabs beyond the cap: first the
// oldest whose run no longer needs the user, else the oldest, and never the new
// tab. The count is the board's rows and the tabs this process kept, filtered by
// the lane's live tab list; a lane that cannot be listed evicts nothing, and a
// close that fails leaves the tab counted. A run keeps one tab: a later kept tab
// replaces its earlier one. The lane, the tabs and the board are called with no
// lock held, so a slow browser never holds up a status read.
func (k *Keeper) Admit(ctx context.Context, t Tab, runID string) {
	if k == nil {
		return
	}
	k.admitMu.Lock()
	defer k.admitMu.Unlock()
	id := t.ID()
	var replaced []Tab
	k.mu.Lock()
	if l, ok := t.(Laned); ok {
		k.lane = l.Handle()
	}
	kept := make([]keptTab, 0, len(k.list)+1)
	for _, x := range k.list {
		if x.run == runID {
			if x.tab != t {
				replaced = append(replaced, x.tab)
			}
			continue
		}
		kept = append(kept, x)
	}
	k.list = append(kept, keptTab{tab: t, id: id, run: runID, since: k.now()})
	k.mu.Unlock()
	for _, old := range replaced {
		_ = old.Close(ctx)
	}
	k.evict(ctx, id)
}

type candidate struct {
	id, run string
	since   time.Time
	waiting bool
	own     Tab
}

// evict closes tabs until the live kept tabs fit the cap. admitMu is held, so
// no tab is added while it works; mu is taken only to read or change the list.
func (k *Keeper) evict(ctx context.Context, newID string) {
	k.mu.Lock()
	lane := k.lane
	k.mu.Unlock()
	if lane == nil {
		return
	}
	live, err := lane.LiveTabs(ctx)
	if err != nil {
		return
	}
	var rows []KeptRow
	if k.Board != nil {
		rows = k.Board.Rows()
	}
	waiting := func(run string) bool {
		for _, r := range rows {
			if r.RunID == run {
				return r.Waiting
			}
		}
		return true
	}
	var set []candidate
	have := map[string]bool{}
	k.mu.Lock()
	keep := make([]keptTab, 0, len(k.list))
	for _, x := range k.list {
		if !slices.Contains(live, x.id) {
			continue // the user closed it: it stops counting
		}
		keep = append(keep, x)
		have[x.id] = true
		set = append(set, candidate{id: x.id, run: x.run, since: x.since, waiting: waiting(x.run), own: x.tab})
	}
	k.list = keep
	k.mu.Unlock()
	for _, r := range rows {
		if !have[r.TabID] && r.TabID != "" && slices.Contains(live, r.TabID) {
			have[r.TabID] = true
			set = append(set, candidate{id: r.TabID, run: r.RunID, since: r.Since, waiting: r.Waiting})
		}
	}
	slices.SortStableFunc(set, func(a, b candidate) int { return a.since.Compare(b.since) })
	failed := map[string]bool{}
	for len(set) > k.max() {
		pick := -1
		for _, wantDone := range []bool{true, false} {
			for i, c := range set {
				if c.id != newID && !failed[c.id] && (!wantDone || !c.waiting) {
					pick = i
					break
				}
			}
			if pick >= 0 {
				break
			}
		}
		if pick < 0 {
			return
		}
		c := set[pick]
		var cerr error
		if c.own != nil {
			cerr = c.own.Close(ctx)
		} else {
			cerr = lane.CloseTabID(ctx, c.id)
		}
		if cerr != nil {
			failed[c.id] = true
			continue
		}
		set = slices.Delete(set, pick, pick+1)
		k.mu.Lock()
		k.list = slices.DeleteFunc(k.list, func(x keptTab) bool { return x.id == c.id })
		k.mu.Unlock()
		k.cleared(c.run)
	}
}

func (k *Keeper) cleared(run string) {
	if k.Board == nil {
		return
	}
	defer func() { _ = recover() }()
	k.Board.Cleared(run)
}

// Count is the number of tabs this process keeps.
func (k *Keeper) Count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.list)
}

// TabOf names the tab this process keeps for a run.
func (k *Keeper) TabOf(runID string) (string, bool) {
	if k == nil {
		return "", false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, x := range k.list {
		if x.run == runID {
			return x.id, true
		}
	}
	return "", false
}

// IsOpen answers whether to offer the jump to a kept tab. It answers at once
// from the last read of the lane's tab list: "open" until the lane has been
// read, and an unreadable lane never makes a tab read as closed. A read that
// is older than the TTL is refreshed in the background, one at a time.
func (k *Keeper) IsOpen(tabID string) bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.lane != nil && !k.view.busy && (!k.view.read || k.now().Sub(k.view.at) > k.ttl()) {
		k.view.busy = true
		go k.refresh(k.lane)
	}
	return !k.view.read || k.view.ids[tabID]
}

func (k *Keeper) refresh(lane TabLane) {
	ctx, cancel := context.WithTimeout(context.Background(), laneReadMax)
	defer cancel()
	var ids []string
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = context.Canceled
			}
		}()
		ids, err = lane.LiveTabs(ctx)
	}()
	k.mu.Lock()
	k.view.busy = false
	k.view.at = k.now()
	if err != nil {
		k.mu.Unlock()
		return // the last answer stands
	}
	k.view.read = true
	k.view.ids = make(map[string]bool, len(ids))
	for _, id := range ids {
		k.view.ids[id] = true
	}
	var gone []string
	k.list = slices.DeleteFunc(k.list, func(x keptTab) bool {
		if k.view.ids[x.id] {
			return false
		}
		gone = append(gone, x.run)
		return true
	})
	k.mu.Unlock()
	for _, run := range gone {
		k.cleared(run)
	}
}

// Forget drops a tab that is gone and lets the board stop offering it.
func (k *Keeper) Forget(tabID string) {
	if k == nil {
		return
	}
	var gone []string
	k.mu.Lock()
	k.list = slices.DeleteFunc(k.list, func(x keptTab) bool {
		if x.id != tabID {
			return false
		}
		gone = append(gone, x.run)
		return true
	})
	k.mu.Unlock()
	for _, run := range gone {
		k.cleared(run)
	}
}

// errNoLane is a close of a tab this process never kept, before it has read the
// apply lane.
var errNoLane = errors.New("apply: the apply lane has not been reached, so the kept tab cannot be closed")

// Close closes the tab kept for a run that no longer goes on: the one this
// process keeps, else tabID, which the run's board row names after a restart.
// The board row then stops offering it.
func (k *Keeper) Close(ctx context.Context, runID, tabID string) error {
	if k == nil {
		return nil
	}
	var own Tab
	k.mu.Lock()
	for _, x := range k.list {
		if x.run == runID {
			own, tabID = x.tab, x.id
		}
	}
	lane := k.lane
	k.mu.Unlock()
	var err error
	switch {
	case own != nil:
		err = own.Close(ctx)
	case tabID == "":
		return nil
	case lane == nil:
		return errNoLane
	default:
		err = lane.CloseTabID(ctx, tabID)
	}
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.list = slices.DeleteFunc(k.list, func(x keptTab) bool { return x.run == runID })
	k.mu.Unlock()
	k.cleared(runID)
	return nil
}

// handoff keeps the tab for the user: it counts the tab against the cap and, on
// the server, starts learning how the user finishes it. The stop is already
// decided; nothing here changes it, and a fault is dropped.
func (s *session) handoff(ctx context.Context) {
	defer func() { _ = recover() }()
	k := s.r.d.Page.Keep
	if k == nil {
		return
	}
	run := s.e.Run
	if s.e.resume != nil {
		// The tab was kept before and is the same browser tab: the earlier
		// record of it goes without closing it.
		k.Forget(s.tab.ID())
	}
	k.Admit(ctx, s.tab, run.ID)
	obs, ok := s.tab.(Observable)
	if k.Watch == nil || !ok {
		return
	}
	id := s.tab.ID()
	var applied func(context.Context) (bool, error)
	if run.Report != nil {
		applied = func(ctx context.Context) (bool, error) { return reportApplied(ctx, s.r.d.Page.Tracker, *run.Report) }
	}
	var number int64
	if run.Report != nil {
		number = run.Report.Number
	}
	k.Watch.Start(WatchSpec{TabID: id, RunID: run.ID, Report: number, Tab: obs, Applied: applied, writes: s.bankWrites(),
		Ended: func(reason string) {
			if reason == "tab-closed" {
				k.Forget(id)
			}
		}})
}

// takeBack ends the recording of a kept tab the run has just taken over again:
// the watch reads what the user did one last time and ends, and the recorder
// leaves the page, because the run's own input is as trusted to the page as
// the user's. Recording starts again only if the run keeps the tab again.
func (s *session) takeBack(ctx context.Context) error {
	if k := s.r.d.Page.Keep; k != nil {
		k.Watch.Stop(s.tab.ID())
	}
	return s.tab.Eval(ctx, recorderDisarmJS, nil)
}

// reportApplied reports whether the tracker row of an evaluated report has
// become applied. The row is found by its id and accepted only when it carries
// this report's number: the tracker's row number and the report's number are
// different id spaces.
func reportApplied(ctx context.Context, tr *tracker.Tracker, rb ReportBinding) (bool, error) {
	if tr == nil || rb.TrackerID == 0 {
		return false, nil
	}
	d, err := tr.Get(ctx, rb.TrackerID)
	if err != nil {
		return false, err
	}
	if d.Entry.ReportNumber == nil || *d.Entry.ReportNumber != rb.Number {
		return false, nil
	}
	return d.Entry.Status.Submitted(), nil
}

// closeUnusedGate closes the walker's gate tab when no stage used it and it is
// not the run's own tab. A tab the run did not open (an adopted one) is never
// closed.
func (s *session) closeUnusedGate(ctx context.Context) {
	defer func() { _ = recover() }()
	g := s.e.gate
	if g == nil || g.tab == s.tab || g.adopted > 0 {
		return
	}
	if _, adopted := g.tab.(*adoptedTab); adopted || !g.alive() {
		return
	}
	_ = g.tab.Close(ctx)
}

// Keeper is the runner's keeper of kept tabs; nil when it has none.
func (r *Runner) Keeper() *Keeper { return r.d.Page.Keep }

// ResumableLane is the lane as a restarted process resumes watches on it: the
// tabs it lists, marking one kept, and the one observer a kept tab may have.
type ResumableLane interface {
	LiveTabs(ctx context.Context) ([]string, error)
	KeepTab(id string) error
	ObserveTab(ctx context.Context, id string) (TabObserver, error)
}

type observeFunc func(ctx context.Context) (TabObserver, error)

func (f observeFunc) Observe(ctx context.Context) (TabObserver, error) { return f(ctx) }

// ResumeWatches starts the recorder again, after a restart, on every row of
// the board that still waits for the user and still keeps an open tab. The
// newest rows are started first, so when more tabs wait than the watcher's cap
// allows it is the oldest that go unwatched, and a deploy does not end the
// watch on a tab the person is part-way through. It returns the number of
// sessions started; a fault in one tab never stops the others.
func (k *Keeper) ResumeWatches(ctx context.Context, lane ResumableLane) int {
	if k == nil || k.Watch == nil || k.Board == nil || lane == nil {
		return 0
	}
	live, err := lane.LiveTabs(ctx)
	if err != nil {
		return 0
	}
	var rows []KeptRow
	for _, r := range k.Board.Rows() {
		if r.Waiting && r.TabID != "" && slices.Contains(live, r.TabID) {
			rows = append(rows, r)
		}
	}
	slices.SortStableFunc(rows, func(a, b KeptRow) int { return b.Since.Compare(a.Since) })
	started := 0
	for _, r := range rows {
		id := r.TabID
		if err := lane.KeepTab(id); err != nil {
			continue
		}
		spec := WatchSpec{TabID: id, RunID: r.RunID, Tab: observeFunc(func(c context.Context) (TabObserver, error) { return lane.ObserveTab(c, id) }),
			Ended: func(reason string) {
				if reason == "tab-closed" {
					k.Forget(id)
				}
			}}
		if k.Watch.Start(spec) {
			started++
		}
	}
	return started
}
