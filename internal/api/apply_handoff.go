package api

import (
	"context"
	"errors"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/notify"
)

// boardRows is the runs board as the keeper of kept tabs reads it: the rows that
// offer a jump to a tab, kept by this process or an earlier one.
type boardRows struct{ a *applyStack }

var _ apply.KeptBoard = boardRows{}

func (b boardRows) Rows() []apply.KeptRow {
	ctx := context.WithoutCancel(b.a.s.bg)
	entries, err := b.a.d.notify.board.List(ctx, notify.Filter{Kinds: []notify.Kind{notify.KindApply}, Tabbed: true, Limit: notify.MaxLimit,
		States: []notify.State{notify.NeedsYou, notify.Blocked, notify.Done}})
	if err != nil {
		return nil
	}
	var out []apply.KeptRow
	for _, e := range entries {
		if e.FocusTab != "" {
			out = append(out, apply.KeptRow{RunID: e.ID, TabID: e.FocusTab, Since: e.StateAt, Waiting: e.Waiting()})
		}
	}
	return out
}

// Cleared takes the jump off a board row whose tab is gone. A filled form that
// waited at Submit there is gone with it, so that wait leaves the inbox: the
// posting is filled again from Apply Next.
func (b boardRows) Cleared(runID string) {
	ctx := context.WithoutCancel(b.a.s.bg)
	board := b.a.d.notify.board
	empty := ""
	e, err := board.Update(ctx, runID, notify.Patch{FocusTab: &empty})
	if err != nil {
		if !errors.Is(err, notify.ErrNotFound) {
			b.a.s.log.Warn("a kept tab's board row could not stop offering its jump", "run", runID, "error", err.Error())
		}
		return
	}
	b.settleUntabbed(ctx, e)
}

// settleUntabbed acknowledges a filled form that waits at Submit in no tab.
func (b boardRows) settleUntabbed(ctx context.Context, e notify.Entry) bool {
	if e.State != notify.NeedsYou || e.Reason != "confirmation" || e.FocusTab != "" || e.Acked() {
		return false
	}
	if _, err := b.a.d.notify.board.Ack(ctx, e.ID); err != nil {
		b.a.s.log.Warn("a filled form whose tab is gone stayed in the inbox", "run", e.ID, "error", err.Error())
		return false
	}
	return true
}

// dropClosedTabs takes the jump off every board row when no browser survived
// the last process: its graceful stop closed the browser with every tab a run
// kept. A browser an abrupt stop left running keeps its rows for the next lane
// use to adopt. A filled form an earlier process lost the tab of leaves the
// inbox either way.
func (a *applyStack) dropClosedTabs() {
	b := boardRows{a}
	if bc := a.s.cfg.BrowserControl; bc != nil && !bc.Survives() {
		rows := b.Rows()
		for _, r := range rows {
			b.Cleared(r.RunID)
		}
		if len(rows) > 0 {
			a.s.log.Info("the board stopped offering tabs a closed browser held", "count", len(rows))
		}
	}
	ctx := context.WithoutCancel(a.s.bg)
	no := false
	waits, err := a.d.notify.board.List(ctx, notify.Filter{States: []notify.State{notify.NeedsYou}, Kinds: []notify.Kind{notify.KindApply}, Acked: &no, Limit: notify.MaxLimit})
	if err != nil {
		a.s.log.Warn("the filled forms waiting in no tab could not be read", "error", err.Error())
		return
	}
	n := 0
	for _, e := range waits {
		if b.settleUntabbed(ctx, e) {
			n++
		}
	}
	if n > 0 {
		a.s.log.Info("filled forms whose tab is gone left the inbox", "count", n)
	}
}

// newKeeper builds the keeper of the tabs runs hand to the user. The recorder of
// how the user finishes a tab exists only when the launcher gave the server a
// browser, so a test server or a command-line run never opens a session.
func (a *applyStack) newKeeper() *apply.Keeper {
	k := &apply.Keeper{Board: boardRows{a}}
	if a.s.cfg.ApplyWindow != nil {
		k.Watch = &apply.Watcher{Ctx: a.s.bg, Poster: learnPoster{a.d.learn, a.s.log},
			Log: func(msg string, args ...any) { a.s.log.Info(msg, args...) }}
	}
	return k
}

// resumeWatches starts the recorder again on the kept tabs of the board that an
// earlier process was watching, the first time the apply window is reached.
func (a *applyStack) resumeWatches(ctx context.Context, win apply.Window) {
	laned, ok := win.(apply.Laned)
	if !ok {
		return
	}
	lane, ok := laned.Handle().(apply.ResumableLane)
	if !ok {
		return
	}
	a.resumed.Do(func() { a.runner.Keeper().ResumeWatches(context.WithoutCancel(ctx), lane) })
}

// focusTab is the handle the board row of a run offers: the tab it kept.
func (a *applyStack) focusTab(runID string) string {
	id, _ := a.runner.Keeper().TabOf(runID)
	return id
}

// tabOf names the tab kept for a run: the one this process kept, else the one
// its board row still offers after a restart.
func (a *applyStack) tabOf(ctx context.Context, runID string) string {
	if id := a.focusTab(runID); id != "" {
		return id
	}
	e, err := a.d.notify.board.Get(ctx, runID)
	if err != nil {
		return ""
	}
	return e.FocusTab
}

// keptTab reports whether a live tab was kept for a run. A lane that has not
// been read answers yes; the tab list refreshes in the background.
func (a *applyStack) keptTab(runID string) bool {
	id := a.tabOf(context.WithoutCancel(a.s.bg), runID)
	return id != "" && a.runner.Keeper().IsOpen(id)
}

// retireEarlier acknowledges the earlier stops of the posting a new run started
// on, waiting or blocked, and closes the tabs they kept: the new run is the one
// that goes on, so the old stops are stale (notify.LaterRunRetires). A possible
// submission stays for the user to check. The tabs are closed beside the run,
// which never waits on the browser for them.
func (a *applyStack) retireEarlier(ctx context.Context, id, postingURL string) {
	ctx = context.WithoutCancel(ctx)
	key, stp := apply.IdentityKey(postingURL)
	if stp != nil {
		return
	}
	rows, err := a.s.store.Load().Read().QueryContext(ctx, `SELECT run_id FROM apply_runs WHERE identity_key = ? AND run_id <> ?`, key, id)
	if err != nil {
		a.s.log.Warn("the earlier runs of a posting could not be read", "run", id, "error", err.Error())
		return
	}
	var earlier []string
	for rows.Next() {
		var rid string
		if rows.Scan(&rid) == nil {
			earlier = append(earlier, rid)
		}
	}
	rows.Close()
	board := a.d.notify.board
	var stale []notify.Entry
	for _, rid := range earlier {
		e, err := board.Get(ctx, rid)
		if err != nil || !notify.LaterRunRetires(e) {
			continue
		}
		if _, err := board.Ack(ctx, rid); err != nil {
			a.s.log.Warn("a stale entry of a posting could not be acknowledged", "run", rid, "error", err.Error())
			continue
		}
		stale = append(stale, e)
	}
	if len(stale) > 0 {
		a.wg.Go(func() {
			for _, e := range stale {
				a.closeKept(ctx, e)
			}
		})
	}
}

// closeKept closes the tab a stale entry kept, reaching the apply lane when this
// process has not used it yet.
func (a *applyStack) closeKept(ctx context.Context, e notify.Entry) {
	k := a.runner.Keeper()
	tab, kept := k.TabOf(e.ID)
	if !kept {
		tab = e.FocusTab
	}
	if tab == "" {
		return
	}
	if !kept && a.s.cfg.ApplyWindow != nil {
		if win, err := a.s.cfg.ApplyWindow(ctx); err == nil {
			if l, ok := win.(apply.Laned); ok {
				k.UseLane(l.Handle())
			}
		}
	}
	if err := k.Close(ctx, e.ID, tab); err != nil {
		if code, _ := browser.CodeOf(err); code != browser.CodeTabClosed {
			a.s.log.Warn("the tab of a stale entry could not be closed", "run", e.ID, "error", err.Error())
			return
		}
		k.Forget(tab)
		boardRows{a}.Cleared(e.ID)
	}
}

// formWaits reports whether a run's filled form waits at Submit in the tab it
// kept, for "Submit this form".
func (a *applyStack) formWaits(ctx context.Context, runID string) bool {
	e, err := a.d.notify.board.Get(ctx, runID)
	return err == nil && notify.CanSubmitKept(e)
}

// showApplyTab brings the tab a run kept for the user to the front.
func (s *Server) showApplyTab(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	a := d.apply
	ctx := c.R.Context()
	runID := c.R.PathValue("id")
	noTab := func() (apikit.Reply, error) {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeNotFound, "no tab was kept for this run"), wire.ReasonNoTab)
	}
	tab := a.tabOf(ctx, runID)
	if tab == "" {
		return noTab()
	}
	if s.cfg.ApplyWindow == nil {
		return apikit.Reply{}, apikit.Unavailable("the application browser is not available in this build")
	}
	win, err := s.cfg.ApplyWindow(ctx)
	if err != nil {
		return apikit.Reply{}, laneError(err)
	}
	laned, ok := win.(apply.Laned)
	if !ok {
		return apikit.Reply{}, apikit.Unavailable("the application browser cannot show a tab in this build")
	}
	lane := laned.Handle()
	a.runner.Keeper().UseLane(lane)
	a.resumeWatches(ctx, win)
	if err := lane.ShowTab(ctx, tab); err != nil {
		if code, _ := browser.CodeOf(err); code == browser.CodeTabClosed {
			a.runner.Keeper().Forget(tab)
			boardRows{a}.Cleared(runID)
			return noTab()
		}
		return apikit.Reply{}, laneError(err)
	}
	return apikit.Reply{Data: wire.ApplyTabShown{RunID: runID}}, nil
}
