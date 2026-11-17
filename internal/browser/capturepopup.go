package browser

import (
	"context"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Popup URL capture polls every 100 ms for up to 4 s: a popup attached while it
// is still navigating does not reliably fire load events.
const (
	popupPoll  = 100 * time.Millisecond
	popupWait  = 4 * time.Second
	blankPopup = "about:blank"
)

// CapturePopup runs act in the tab and reports the address the action asked a
// new window for.
//
// In the scan lane no window ever opens: the scan stubs record the address the
// page asked for, and the first one recorded during act is returned. In the apply
// lane the window really opens; the address of the new page whose opener is
// this tab is read and the page is closed. A page opened by the user or by
// another tab is neither reported nor closed. A popup that stays blank returns
// no address.
func (h *LaneHandle) CapturePopup(ctx context.Context, tabID string, act func(context.Context) error) (string, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return "", err
	}
	i := h.in()
	known := i.popupsOf(t.id)
	t.mu.Lock()
	seen := len(t.popupURLs)
	t.mu.Unlock()
	if err := act(ctx); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, popupWait)
	defer cancel()
	tick := time.NewTicker(popupPoll)
	defer tick.Stop()
	for {
		if h.lane == LaneScan {
			t.mu.Lock()
			if len(t.popupURLs) > seen {
				u := t.popupURLs[seen]
				t.mu.Unlock()
				return u, nil
			}
			t.mu.Unlock()
		} else {
			for id, p := range i.popupsOf(t.id) {
				if known[id] != nil {
					continue
				}
				u := i.liveURL(ctx, id)
				if u != "" && u != blankPopup {
					i.dropTab(context.WithoutCancel(ctx), p, "popup-captured")
					return u, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			if h.lane == LaneApply {
				for id, p := range i.popupsOf(t.id) {
					if known[id] == nil {
						i.dropTab(context.Background(), p, "popup-captured")
					}
				}
			}
			return "", nil
		case <-tick.C:
		}
	}
}

// popupsOf lists the pages whose opener is the tab.
func (i *Instance) popupsOf(opener target.ID) map[target.ID]*tab {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := map[target.ID]*tab{}
	for id, p := range i.tabs {
		if p.opener == opener && !p.closed.Load() {
			out[id] = p
		}
	}
	return out
}

// liveURL reads a page's current address from the browser.
func (i *Instance) liveURL(ctx context.Context, id target.ID) string {
	bc, cancel := i.bctx(ctx)
	defer cancel()
	r, err := chromedp.CallBrowser(bc, target.GetTargetInfo, target.GetTargetInfoParams{TargetID: id})
	if err != nil || r.TargetInfo == nil {
		return ""
	}
	return r.TargetInfo.URL
}
