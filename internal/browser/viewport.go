package browser

import (
	"context"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

// CodeViewportEmpty reports a tab whose viewport still has no area after it
// was resized, so no input could land on anything.
const CodeViewportEmpty Code = "viewport-empty"

// Usable viewport sizes: the scan window is created at this size and a tab
// found without area is parked at it.
const (
	scanViewportW = 1280
	scanViewportH = 900
)

// viewportSize reads the tab's inner size.
func (i *Instance) viewportSize(ctx, sess context.Context) (w, h float64, err error) {
	if i.viewportFn != nil {
		return i.viewportFn()
	}
	got, err := chromedp.Run(sessionWith(sess, ctx), chromedp.Evaluate[struct {
		W float64 `json:"w"`
		H float64 `json:"h"`
	}](`({w: innerWidth, h: innerHeight})`))
	return got.W, got.H, err
}

// resizeWindow sets the size of the window that holds the tab.
func (i *Instance) resizeWindow(ctx context.Context, t *tab, w, h int64) error {
	if i.resizeFn != nil {
		return i.resizeFn(w, h)
	}
	bc, cancel := i.bctx(ctx)
	defer cancel()
	bc, cancel2 := context.WithTimeout(bc, inputDeadline)
	defer cancel2()
	_, err := chromedp.CallBrowser(bc, browser.SetWindowBounds, browser.SetWindowBoundsParams{
		WindowID: browser.WindowID(t.window), Bounds: &browser.Bounds{Width: w, Height: h, WindowState: browser.WindowStateNormal}})
	return err
}

// fit sets the tab's window contents to w by h, the panel's size: a real
// window, as emulated metrics disagree with it and bot checks see that. Not
// while a lease holds the apply lane or the window is not normal.
func (i *Instance) fit(ctx context.Context, t *tab, w, h float64) {
	i.fitMu.Lock()
	defer i.fitMu.Unlock()
	bc, cancel := i.bctx(ctx)
	defer cancel()
	id := browser.WindowID(t.window)
	r, err := chromedp.CallBrowser(bc, browser.GetWindowBounds, browser.GetWindowBoundsParams{WindowID: id})
	i.mu.Lock()
	held := i.applyHolder() != nil
	i.mu.Unlock()
	if held || err != nil || r.Bounds.WindowState != browser.WindowStateNormal {
		return
	}
	if i.unfitted[0] != t.window {
		i.unfitted = [3]int64{t.window, r.Bounds.Width, r.Bounds.Height}
	}
	_, _ = chromedp.CallBrowser(bc, browser.SetContentsSize, browser.SetContentsSizeParams{WindowID: id, Width: int64(w), Height: int64(h)})
}

// unfit puts the size back when a run takes the apply lane or the browser
// stops, so the run gets its own size and Chrome does not keep the panel's.
func (i *Instance) unfit() {
	i.fitMu.Lock()
	defer i.fitMu.Unlock()
	if f := i.unfitted; f[0] != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), inputDeadline)
		defer cancel()
		_ = i.resizeWindow(ctx, &tab{window: f[0]}, f[1], f[2])
		i.unfitted = [3]int64{}
	}
}

// ensureViewport makes sure input can land: a tab whose viewport has no area is
// resized first (1280 by 900), and the operation fails with viewport-empty when
// the area stays zero.
func (h *LaneHandle) ensureViewport(ctx context.Context, t *tab, sess context.Context) error {
	i := h.in()
	c, cancel := context.WithTimeout(ctx, inputDeadline)
	defer cancel()
	w, hgt, err := i.viewportSize(c, sess)
	if err != nil {
		return h.wrapOp(t, err, "reading the viewport", inputDeadline)
	}
	if w > 0 && hgt > 0 {
		return nil
	}
	if err := i.resizeWindow(c, t, scanViewportW, scanViewportH); err != nil {
		return h.wrapOp(t, err, "resizing the window", inputDeadline)
	}
	select {
	case <-time.After(resizeSettle):
	case <-c.Done():
	}
	if w, hgt, err = i.viewportSize(c, sess); err != nil {
		return h.wrapOp(t, err, "reading the viewport", inputDeadline)
	}
	if w <= 0 || hgt <= 0 {
		return newError(CodeViewportEmpty, "the tab's viewport has no area")
	}
	return nil
}

// resizeSettle is how long a resized window gets to apply the new size.
var resizeSettle = 100 * time.Millisecond
