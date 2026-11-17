package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// LaneWindow is a browser lane as the apply window: each tab it opens carries
// the engine's page on the tab's hooks.
type LaneWindow struct {
	Lane *browser.LaneHandle
	// Leased makes every tab the window hands out hold a lease on the lane,
	// which admits one run at a time. The product sets it; the end-to-end rigs
	// that drive several pages of one browser at once leave it off.
	Leased bool
}

// acquire takes the lease for run when the window is leased, and returns the
// handle the run works through. Without leases it is the window's own handle
// and the lease is nil, whose Release does nothing.
func (w LaneWindow) acquire(run string) (*browser.Lease, *browser.LaneHandle, error) {
	if !w.Leased {
		return nil, w.Lane, nil
	}
	lease, err := w.Lane.Acquire(run)
	if err != nil {
		return nil, nil, err
	}
	return lease, lease.Lane(), nil
}

// OpenTab opens a tab owned by owner and puts the engine page on it. The run
// holds a lease on the apply lane from before the tab exists until the tab is
// closed or kept for the user; a second run asking for the lane meanwhile is
// refused as lane-busy.
func (w LaneWindow) OpenTab(ctx context.Context, owner string, h engine.Host) (Tab, error) {
	lease, lane, err := w.acquire(owner)
	if err != nil {
		return nil, err
	}
	fail := func(err error, id string) (Tab, error) {
		if id != "" {
			_ = lane.CloseTab(context.WithoutCancel(ctx), id)
		}
		lease.Release()
		return nil, err
	}
	id, err := lane.OpenTab(ctx, owner)
	if err != nil {
		return fail(err, "")
	}
	hooks, err := lane.Hooks(ctx, id)
	if err != nil {
		return fail(err, id)
	}
	page, err := engine.NewPage(hooks, h)
	if err != nil {
		return fail(err, id)
	}
	return &laneTab{lane: lane, lease: lease, id: id, page: page, hooks: hooks}, nil
}

var _ ActiveWindow = LaneWindow{}

// ActiveTab names the lane's newest open tab: the apply window opens tabs in
// the background and never activates one, so the newest is the one the user
// last brought up, or a tab a stopped run kept for them.
func (w LaneWindow) ActiveTab(context.Context) (string, error) {
	tabs := w.Lane.Tabs()
	if len(tabs) == 0 {
		return "", ErrNoTab
	}
	newest := slices.MaxFunc(tabs, func(a, b browser.TabInfo) int { return a.Created.Compare(b.Created) })
	return newest.ID, nil
}

// Adopt puts the engine page on a tab that is already open. The tab stays
// open when the returned Tab is closed. The fill holds a lease on the apply
// lane while it works on the tab.
func (w LaneWindow) Adopt(ctx context.Context, id string, h engine.Host) (Tab, error) {
	lease, lane, err := w.acquire("fill-tab")
	if err != nil {
		return nil, err
	}
	hooks, err := lane.Hooks(ctx, id)
	if err != nil {
		lease.Release()
		return nil, err
	}
	page, err := engine.NewPage(hooks, h)
	if err != nil {
		lease.Release()
		return nil, err
	}
	return &adoptedTab{laneTab{lane: lane, lease: lease, id: id, page: page, hooks: hooks}}, nil
}

// ResumableWindow is an apply window that can take back a tab a run kept for the
// user, which a Done does.
type ResumableWindow interface {
	Window
	// Reclaim attaches the engine to a kept tab. Keeping the returned tab
	// detaches it again; closing it leaves the tab open, because it is the
	// user's.
	Reclaim(ctx context.Context, id string, h engine.Host) (Tab, error)
}

var _ ResumableWindow = LaneWindow{}

// Reclaim takes a tab kept for the user back and puts the engine page on it. A
// tab that cannot be taken is kept as it was.
func (w LaneWindow) Reclaim(ctx context.Context, id string, h engine.Host) (Tab, error) {
	lease, lane, err := w.acquire("resume")
	if err != nil {
		return nil, err
	}
	if err := lane.Reclaim(id); err != nil {
		lease.Release()
		return nil, err
	}
	keep := func(err error) (Tab, error) {
		_ = lane.Keep(id)
		lease.Release()
		return nil, err
	}
	hooks, err := lane.Hooks(ctx, id)
	if err != nil {
		return keep(err)
	}
	page, err := engine.NewPage(hooks, h)
	if err != nil {
		return keep(err)
	}
	return &reclaimedTab{laneTab{lane: lane, lease: lease, id: id, page: page, hooks: hooks}}, nil
}

// reclaimedTab is a tab a run took back from the user. Keeping it detaches it
// again, and so does closing it: the run never closes the tab, which stays the
// user's, but it does not stay attached to automation. A document the run
// staged for one of its file inputs stays on disk with it, for the user's own
// Submit, and goes when the tab closes.
type reclaimedTab struct{ laneTab }

func (t *reclaimedTab) Close(context.Context) error {
	t.leave()
	defer t.lease.Release()
	return t.lane.Keep(t.id)
}

// adoptedTab is a tab the user owns: it is never closed by the fill, and what
// the fill staged for it goes when the tab closes.
type adoptedTab struct{ laneTab }

func (t *adoptedTab) Keep() error {
	t.lease.Release()
	return nil
}

func (t *adoptedTab) Close(context.Context) error {
	t.lease.Release()
	return nil
}

type laneTab struct {
	lane  *browser.LaneHandle
	lease *browser.Lease
	id    string
	page  *engine.Page
	hooks *browser.Hooks
}

func (t *laneTab) ID() string { return t.id }

func (t *laneTab) Navigate(ctx context.Context, url string) (browser.NavResult, error) {
	return t.lane.Navigate(ctx, t.id, url)
}

func (t *laneTab) NavigateDOM(ctx context.Context, url string) (browser.NavResult, error) {
	return t.lane.NavigateDOM(ctx, t.id, url)
}

func (t *laneTab) Frames(ctx context.Context) ([]browser.Frame, error) { return t.hooks.Frames(ctx) }

func (t *laneTab) EvalFrame(ctx context.Context, frame, expr string, out any) error {
	raw, err := t.hooks.Eval(ctx, frame, "", expr)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (t *laneTab) Eval(ctx context.Context, expr string, out any) error {
	raw, err := t.lane.Evaluate(ctx, t.id, expr)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (t *laneTab) Inventory(ctx context.Context, frame string, forms []FormInfo) (Inventory, error) {
	return readInventory(ctx, t.page, t, frame, forms)
}

func (t *laneTab) Click(ctx context.Context, x, y float64) error {
	return t.lane.Click(ctx, t.id, x, y)
}

func (t *laneTab) TypeText(ctx context.Context, text string) error {
	return t.lane.TypeText(ctx, t.id, text)
}

func (t *laneTab) PressKey(ctx context.Context, key string) error {
	return t.lane.PressKey(ctx, t.id, key)
}

func (t *laneTab) Screenshot(ctx context.Context) ([]byte, error) {
	return t.lane.Screenshot(ctx, t.id)
}

func (t *laneTab) WatchResponses(ctx context.Context) (ResponseWatch, error) {
	return t.lane.WatchResponses(ctx, t.id)
}

// Keep hands the tab to the user and ends the run's hold on the lane; the tab
// and Chrome stay as they are.
func (t *laneTab) Keep() error {
	t.leave()
	if err := t.lane.Keep(t.id); err != nil {
		return err
	}
	t.lease.Release()
	return nil
}

// disposeBound bounds taking the run's scripts out of a tab it hands over, so
// the hand-over stays within the 2 seconds the browser layer has for it.
const disposeBound = 1500 * time.Millisecond

// leave takes the engine's scripts out of the tab before the lane lets go of
// it: once the tab is the user's, nothing of the run may write into the page.
func (t *laneTab) leave() {
	ctx, cancel := context.WithTimeout(context.Background(), disposeBound)
	defer cancel()
	t.page.Dispose(ctx)
}

func (t *laneTab) Close(ctx context.Context) error {
	defer t.lease.Release()
	return t.lane.CloseTab(ctx, t.id)
}

func (t *laneTab) Page() Filler { return t.page }

// ConsoleLines opens rawURL in a background tab of the run's lane and returns
// what the page wrote to its console (consoleCodes).
func (t *laneTab) ConsoleLines(ctx context.Context, rawURL string) ([]string, error) {
	return t.lane.ConsoleLines(ctx, "console-read", rawURL, consoleSettle)
}

// Closed reports whether the lane no longer lists the tab: the user or the
// site closed it.
func (t *laneTab) Closed() bool {
	for _, x := range t.lane.Tabs() {
		if x.ID == t.id {
			return false
		}
	}
	return true
}

// laneOps is the apply lane as the keeper uses it.
type laneOps struct{ lane *browser.LaneHandle }

func (o laneOps) LiveTabs(context.Context) ([]string, error) {
	tabs := o.lane.Tabs()
	ids := make([]string, len(tabs))
	for i, t := range tabs {
		ids[i] = t.ID
	}
	return ids, nil
}

func (o laneOps) CloseTabID(ctx context.Context, id string) error { return o.lane.CloseTab(ctx, id) }

func (o laneOps) ShowTab(ctx context.Context, id string) error { return o.lane.ShowWindow(ctx, id) }

func (o laneOps) KeepTab(id string) error { return o.lane.Keep(id) }

func (o laneOps) ObserveTab(ctx context.Context, id string) (TabObserver, error) {
	obs, err := o.lane.Observe(ctx, id, RecorderScripts())
	if err != nil {
		return nil, err
	}
	return obs, nil
}

var (
	_ Laned      = LaneWindow{}
	_ Laned      = (*laneTab)(nil)
	_ Observable = (*laneTab)(nil)
)

// Handle is the window's lane, for the keeper and the jump to a kept tab.
func (w LaneWindow) Handle() TabLane { return laneOps{w.Lane} }

// Handle is the tab's lane.
func (t *laneTab) Handle() TabLane { return laneOps{t.lane} }

// Observe opens the one session the browser layer permits on a kept tab, for
// the handoff recorder.
func (t *laneTab) Observe(ctx context.Context) (TabObserver, error) {
	o, err := t.lane.Observe(ctx, t.id, RecorderScripts())
	if err != nil {
		return nil, err
	}
	return o, nil
}

// CapturePopup runs act and reports the address of a window the tab opened
// during it; the window is closed.
func (t *laneTab) CapturePopup(ctx context.Context, act func(context.Context) error) (string, error) {
	return t.lane.CapturePopup(ctx, t.id, act)
}

// GooglePopups returns only new direct children owned by this run. An existing
// Google tab or an unrelated browser window is never an authentication target.
func (t *laneTab) GooglePopups(since time.Time) []string {
	owner := ""
	for _, info := range t.lane.Tabs() {
		if info.ID == t.id {
			owner = info.Owner
		}
	}
	if owner == "" {
		return nil
	}
	var out []string
	for _, info := range t.lane.Tabs() {
		if info.Opener == t.id && info.Owner == owner && !info.Created.Before(since) {
			out = append(out, info.ID)
		}
	}
	return out
}
func (t *laneTab) GooglePopup(ctx context.Context, id string) (Tab, func(), error) {
	owner := ""
	for _, info := range t.lane.Tabs() {
		if info.ID == t.id {
			owner = info.Owner
		}
	}
	found := false
	for _, info := range t.lane.Tabs() {
		if info.ID == id && info.Opener == t.id && info.Owner == owner && owner != "" {
			found = true
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("not a popup of this run")
	}
	hooks, err := t.lane.Hooks(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	page, err := engine.NewPage(hooks, engine.Host{})
	if err != nil {
		return nil, nil, err
	}
	return &laneTab{lane: t.lane, id: id, page: page, hooks: hooks}, func() { page.Close() }, nil
}
