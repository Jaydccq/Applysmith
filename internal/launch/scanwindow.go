package launch

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/discovery"
)

// ChromeWindow is the scan window of the browser that open returns, started
// or adopted on first use. Tests give it a throwaway browser. watch reports
// the user's "watch scans" setting (nil never watches).
func ChromeWindow(open func(context.Context) (*browser.Instance, error), watch func(context.Context) bool) discovery.Window {
	return &chromeWindow{open: open, watch: watch}
}

// chromeWindow opens scan tabs of the browser that open returns, started or
// adopted on first use. A scan tab is a scan-lane tab unless the user asked to
// watch scans (browser.SelectScanLane), in which case it is an unfiltered tab
// of the apply window.
type chromeWindow struct {
	open  func(context.Context) (*browser.Instance, error)
	watch func(context.Context) bool

	// The watched scans of this process share one apply-lane lease, so a
	// scan's list tab and its detail tabs do not refuse each other.
	mu         sync.Mutex
	watchLease *browser.Lease
	watchTabs  int
}

// scanLease leases the scan lane of the instance open returns. The instance is
// asked for again once when it stopped between being opened and being leased,
// as it does after the idle period.
func scanLease(ctx context.Context, open func(context.Context) (*browser.Instance, error), run string) (*browser.Lease, error) {
	for try := 0; ; try++ {
		inst, err := open(ctx)
		if err != nil {
			return nil, err
		}
		lease, err := inst.Acquire(browser.LaneScan, run)
		if err == nil || try > 0 {
			return lease, err
		}
	}
}

// OpenTab opens a scan tab. Its lane comes from browser.SelectScanLane: the
// scan lane unless the user asked to watch scans and the board is not on the
// guarded list. A scan lease is released when the tab is closed.
func (w *chromeWindow) OpenTab(ctx context.Context, owner, host string) (discovery.Tab, error) {
	choice := browser.SelectScanLane(browser.LaneRequest{Host: host, Watch: w.watch != nil && w.watch(ctx)})
	if choice.Lane == browser.LaneApply {
		// A busy apply lane (a run holds it) keeps the scan hidden rather than
		// failing it: the setting is a convenience, never a reason to lose a scan.
		if t, err := w.openWatched(ctx, owner, choice.Tab); err == nil {
			return t, nil
		} else if code, _ := browser.CodeOf(err); code != browser.CodeLaneBusy {
			return nil, mapBrowserErr(err)
		}
	}
	lease, err := scanLease(ctx, w.open, owner)
	if err != nil {
		return nil, err
	}
	h := lease.Lane()
	id, err := h.OpenTab(ctx, owner)
	if err != nil {
		lease.Release()
		return nil, mapBrowserErr(err)
	}
	return &chromeTab{h: h, lease: lease, id: id}, nil
}

// openWatched opens a tab in the apply window under the shared watch lease.
func (w *chromeWindow) openWatched(ctx context.Context, owner string, o browser.TabOptions) (discovery.Tab, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watchLease == nil {
		inst, err := w.open(ctx)
		if err != nil {
			return nil, err
		}
		if w.watchLease, err = inst.Acquire(browser.LaneApply, "scan-watch"); err != nil {
			return nil, err
		}
	}
	h := w.watchLease.Lane()
	id, err := h.OpenTabWith(ctx, owner, o)
	if err != nil {
		if w.watchTabs == 0 {
			w.watchLease.Release()
			w.watchLease = nil
		}
		return nil, err
	}
	w.watchTabs++
	return &chromeTab{h: h, id: id, done: w.watchTabClosed}, nil
}

// watchTabClosed releases the shared lease with the last watched tab.
func (w *chromeWindow) watchTabClosed() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watchTabs--; w.watchTabs == 0 && w.watchLease != nil {
		w.watchLease.Release()
		w.watchLease = nil
	}
}

type chromeTab struct {
	h     *browser.LaneHandle
	lease *browser.Lease
	id    string
	// done runs after the tab is closed, in place of releasing a lease of its own.
	done func()
}

func mapBrowserErr(err error) error {
	if code, ok := browser.CodeOf(err); ok && code == browser.CodeNetworkOffline {
		return discovery.ErrOffline
	}
	return err
}

func (t *chromeTab) Goto(ctx context.Context, rawURL string) (discovery.Nav, error) {
	res, err := t.h.NavigateDOM(ctx, t.id, rawURL)
	if err != nil {
		return discovery.Nav{}, mapBrowserErr(err)
	}
	return discovery.Nav{URL: res.URL, Title: res.Title, Status: res.Status, Failed: res.Error}, nil
}

func (t *chromeTab) Eval(ctx context.Context, expr string, out any) error {
	raw, err := t.h.Evaluate(ctx, t.id, expr)
	if err != nil {
		return mapBrowserErr(err)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (t *chromeTab) ClearChallenge(ctx context.Context) (discovery.ChallengeClear, error) {
	res, err := t.h.ClearCloudflareCheckbox(ctx, t.id)
	return discovery.ChallengeClear{Present: res.Present, Solved: res.Solved, Clicks: res.Clicks, Reason: res.Reason}, mapBrowserErr(err)
}

func (t *chromeTab) PopupURLs() []string {
	urls, _ := t.h.PopupURLs(t.id)
	return urls
}

func (t *chromeTab) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.h.CloseTab(ctx, t.id)
	if t.done != nil {
		t.done()
		return
	}
	t.lease.Release()
}
