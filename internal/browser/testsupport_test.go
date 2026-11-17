package browser

import (
	"context"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// The helpers below drive or read the instance the way no product path does:
// they stand in for a server that dies, and read state a test asserts on.

// Detach drops the connection and leaves Chrome running, as when the server
// dies abruptly; the next Open adopts it.
func (i *Instance) Detach() {
	i.mu.Lock()
	i.detached = true
	i.mu.Unlock()
	i.unregister()
	i.proc.stopWatching()
	i.cancelRoot()
	i.cancelAll()
	i.markGone()
}

// Lost lists the tabs the lease had open when the browser went away.
func (l *Lease) Lost() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.lost))
	for id := range l.lost {
		out = append(out, id)
	}
	return out
}

// Recovered reports whether the lease used its automatic recovery.
func (l *Lease) Recovered() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recovered
}

// CloseOwned closes every unkept tab of the lane owned by owner.
func (h *LaneHandle) CloseOwned(ctx context.Context, owner string) {
	for _, t := range h.Tabs() {
		if t.Owner == owner && !t.Kept {
			_ = h.CloseTab(ctx, t.ID)
		}
	}
}

// WindowState returns the state ("normal", "minimized") of the window that
// holds the tab.
func (h *LaneHandle) WindowState(ctx context.Context, tabID string) (string, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return "", err
	}
	bc, cancel := h.in().bctx(ctx)
	defer cancel()
	r, err := chromedp.CallBrowser(bc, browser.GetWindowBounds, browser.GetWindowBoundsParams{WindowID: browser.WindowID(t.window)})
	if err != nil {
		return "", err
	}
	return string(r.Bounds.WindowState), nil
}

// listTargets reads the browser's pages: id and address.
func (i *Instance) listTargets(ctx context.Context) (map[target.ID]string, error) {
	bc, cancel := i.bctx(ctx)
	defer cancel()
	bc, cancel2 := context.WithTimeout(bc, 2*time.Second)
	defer cancel2()
	infos, err := chromedp.Targets(bc)
	if err != nil {
		return nil, err
	}
	out := make(map[target.ID]string, len(infos))
	for _, in := range infos {
		if in.Type == "page" {
			out[in.TargetID] = in.URL
		}
	}
	return out, nil
}
