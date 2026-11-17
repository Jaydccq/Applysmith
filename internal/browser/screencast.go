// Clean-room: docs/provenance.md#CR-BROWSER-VIEW

package browser

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// CodeTabDriven reports user input or navigation for an apply tab a run is
// driving: the panel only watches it.
const CodeTabDriven Code = "tab-driven"

// Live view bounds. A frame is never wider or taller than liveMaxSide, and a
// tab whose screencast has sent no frame yet (a page or window that does not
// repaint) gets a still every liveStillAfter until it does.
const (
	liveQuality    = 70
	liveMaxSide    = 2400
	liveMinSide    = 64
	liveStillAfter = 2 * time.Second
	liveTick       = time.Second
	chooserArmed   = time.Second
)

// LiveOptions bounds the frames of a live view.
type LiveOptions struct {
	MaxWidth, MaxHeight int
}

// LiveFrame is one JPEG frame. Width and Height are the page's viewport in
// CSS pixels, the space input coordinates are given in; Scale is the page's
// zoom and OffsetTop the space above the page that the browser reserves.
type LiveFrame struct {
	JPEG          []byte
	Width, Height float64
	OffsetTop     float64
	Scale         float64
}

// LiveTab is what the panel's toolbar shows. Driven is true while a run drives
// the tab; RunID names the run when the tab is one a run opened.
type LiveTab struct {
	URL, Title   string
	Loading      bool
	CanGoBack    bool
	CanGoForward bool
	Driven       bool
	RunID        string
}

// LiveEvent is one event of a live view; exactly one field is set.
type LiveEvent struct {
	Frame       *LiveFrame
	Tab         *LiveTab
	Cursor      *[2]float64
	FileChooser bool
	End         string
}

// Reasons a live view ends.
const (
	LiveEndTabClosed      = "tab-closed"
	LiveEndBrowserStopped = "browser-stopped"
)

// InputEvent is one input event the user made in the panel. Coordinates are
// the page's CSS pixels. Kind is mouse, wheel, key, text or fit (to X by Y).
type InputEvent struct {
	Kind       string
	Type       string // mouse: move, down, up; key: down, up
	X, Y       float64
	Button     string // left, middle, right or none
	Buttons    int
	ClickCount int
	Modifiers  int // Alt 1, Ctrl 2, Meta 4, Shift 8
	DeltaX     float64
	DeltaY     float64
	Key, Code  string
	KeyCode    int
	Text       string
}

// screencastMuted, when set, drops every screencast frame, so a test sees a
// tab that never repaints.
var screencastMuted atomic.Bool

// viewer is the panel's own DevTools session on a tab. It is apart from the
// session a run drives the tab through, so watching never touches the run's
// state, and it is shared by the live views of the tab.
type viewer struct {
	ctx    context.Context
	cancel context.CancelFunc
	refs   int // guarded by tab.viewMu

	castMu sync.Mutex // serializes starting and stopping the screencast
	casts  int        // live views the session's one screencast serves

	mu     sync.Mutex
	subs   map[chan viewNote]struct{}
	armed  bool
	disarm *time.Timer
}

// viewNote is a cursor position of a run's click or a file chooser the page
// asked for.
type viewNote struct {
	cursor  *[2]float64
	chooser bool
}

func (v *viewer) notify(n viewNote) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for ch := range v.subs {
		select {
		case ch <- n:
		default:
		}
	}
}

func (v *viewer) subscribe() chan viewNote {
	ch := make(chan viewNote, 8)
	v.mu.Lock()
	v.subs[ch] = struct{}{}
	v.mu.Unlock()
	return ch
}

func (v *viewer) unsubscribe(ch chan viewNote) {
	v.mu.Lock()
	delete(v.subs, ch)
	v.mu.Unlock()
}

// openViewer returns the tab's viewer session, attaching it on first use. The
// release ends the session with its last user.
func (i *Instance) openViewer(t *tab) (*viewer, func(), error) {
	t.viewMu.Lock()
	defer t.viewMu.Unlock()
	if t.closed.Load() {
		return nil, nil, newError(CodeTabClosed, "no such tab")
	}
	v := t.view.Load()
	if v == nil {
		ctx, cancel := chromedp.NewContext(i.root, chromedp.WithTargetID(t.id), chromedp.WithDetachOnCancel())
		if err := attachWithin(context.Background(), ctx, attachDeadline); err != nil {
			cancel()
			return nil, nil, err
		}
		// The viewer reads no network traffic; chromedp turns it on at attach.
		dctx, dcancel := context.WithTimeout(ctx, inputDeadline)
		_, _ = chromedp.Call(dctx, network.Disable, cdp.Empty{})
		dcancel()
		v = &viewer{ctx: ctx, cancel: cancel, subs: map[chan viewNote]struct{}{}}
		watch(ctx, page.FileChooserOpened, func(page.EventFileChooserOpened) { v.notify(viewNote{chooser: true}) })
		t.view.Store(v)
	}
	v.refs++
	var once sync.Once
	return v, func() {
		once.Do(func() {
			t.viewMu.Lock()
			defer t.viewMu.Unlock()
			if v.refs--; v.refs > 0 {
				return
			}
			t.view.CompareAndSwap(v, nil)
			v.mu.Lock()
			if v.disarm != nil {
				v.disarm.Stop()
			}
			v.mu.Unlock()
			v.cancel()
		})
	}, nil
}

// endCast ends one live view's use of the screencast. The session has one
// screencast, which every live view of the tab reads, so stop runs only when
// the view that ends is the last.
func (v *viewer) endCast(stop func()) {
	v.castMu.Lock()
	defer v.castMu.Unlock()
	if v.casts--; v.casts == 0 {
		stop()
	}
}

// noteCursor tells the tab's live views where a run is about to click.
func (t *tab) noteCursor(x, y float64) {
	if v := t.view.Load(); v != nil {
		v.notify(viewNote{cursor: &[2]float64{x, y}})
	}
}

// drivenBy reports whether a run drives the tab and the run that opened it: a
// run's tab it has not kept for the user, or a tab a held lease works on (a
// fill of the user's tab, a resumed run).
func (t *tab) drivenBy() (bool, string) {
	if t.isKept() {
		return false, ""
	}
	if t.owner != "" {
		return true, t.owner
	}
	if l := t.driver.Load(); l != nil && !l.isReleased() {
		return true, ""
	}
	return false, ""
}

// liveTab finds the apply tab a live view, input or navigation names. It
// starts nothing: a browser that is not running has no such tab.
func (c *Control) liveTab(tabID string) (*Instance, *tab, error) {
	inst := c.running()
	if inst == nil {
		return nil, nil, newError(CodeTabClosed, "no such tab")
	}
	t, err := inst.Lane(LaneApply).tab(tabID)
	if err != nil {
		return nil, nil, err
	}
	return inst, t, nil
}

// Live streams an apply tab to emit until ctx ends, the tab closes or the
// browser stops: frames, toolbar state on change, the cursor of a run's
// clicks and file choosers. Each frame is acknowledged only after emit
// returns, so a slow reader slows the frames instead of queueing them.
func (c *Control) Live(ctx context.Context, tabID string, o LiveOptions, emit func(LiveEvent) error) error {
	inst, t, err := c.liveTab(tabID)
	if err != nil {
		return err
	}
	v, release, err := inst.openViewer(t)
	if err != nil {
		return err
	}
	defer release()
	notes := v.subscribe()
	defer v.unsubscribe(notes)

	sctx, stop := context.WithCancel(v.ctx)
	defer stop()
	frames := make(chan page.EventScreencastFrame, 1)
	changed := make(chan struct{}, 1)
	var loading atomic.Bool
	poke := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	main := cdp.FrameID(t.id)
	watch(sctx, page.ScreencastFrame, func(ev page.EventScreencastFrame) {
		if screencastMuted.Load() {
			return
		}
		select {
		case frames <- ev:
		case <-sctx.Done():
		}
	})
	watch(sctx, page.FrameStartedLoading, func(ev page.EventFrameStartedLoading) {
		if ev.FrameID == main {
			loading.Store(true)
			poke()
		}
	})
	watch(sctx, page.FrameStoppedLoading, func(ev page.EventFrameStoppedLoading) {
		if ev.FrameID == main {
			loading.Store(false)
			poke()
		}
	})
	watch(sctx, page.FrameNavigated, func(ev page.EventFrameNavigated) {
		if ev.Frame != nil && ev.Frame.ParentID == "" {
			poke()
		}
	})
	watch(sctx, page.NavigatedWithinDocument, func(page.EventNavigatedWithinDocument) { poke() })

	p := page.StartScreencastParams{Format: page.StartScreencastFormatJpeg, Quality: ptr(int64(liveQuality)), EveryNthFrame: 1, MaxFramesInFlight: 1}
	if o.MaxWidth > 0 {
		p.MaxWidth = int64(min(max(o.MaxWidth, liveMinSide), liveMaxSide))
	}
	if o.MaxHeight > 0 {
		p.MaxHeight = int64(min(max(o.MaxHeight, liveMinSide), liveMaxSide))
	}
	v.castMu.Lock()
	octx, cancel := opCtx(ctx, sctx, inputDeadline)
	_, err = chromedp.Call(octx, page.StartScreencast, p)
	cancel()
	if err == nil {
		v.casts++
	}
	v.castMu.Unlock()
	if err != nil {
		return newError(CodeScreenshotUnavailable, "the browser cannot show this tab")
	}
	defer v.endCast(func() {
		qctx, qcancel := context.WithTimeout(v.ctx, inputDeadline)
		_, _ = chromedp.Call(qctx, page.StopScreencast, cdp.Empty{})
		qcancel()
	})

	var last LiveTab
	sent := false
	sendTab := func() error {
		cur := inst.liveState(ctx, v.ctx, t, loading.Load())
		if sent && cur == last {
			return nil
		}
		last, sent = cur, true
		return emit(LiveEvent{Tab: &cur})
	}
	if err := sendTab(); err != nil {
		return nil
	}
	tick := time.NewTicker(liveTick)
	defer tick.Stop()
	still := time.NewTimer(liveStillAfter)
	defer still.Stop()
	framed := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-inst.root.Done():
			_ = emit(LiveEvent{End: LiveEndBrowserStopped})
			return nil
		case <-v.ctx.Done():
			_ = emit(LiveEvent{End: LiveEndTabClosed})
			return nil
		case ev := <-frames:
			framed = true
			if err := emit(LiveEvent{Frame: frameOf(ev)}); err != nil {
				return nil
			}
			actx, acancel := opCtx(ctx, sctx, inputDeadline)
			_, _ = chromedp.Call(actx, page.ScreencastFrameAck, page.ScreencastFrameAckParams{SessionID: ev.SessionID})
			acancel()
		case n := <-notes:
			e := LiveEvent{Cursor: n.cursor, FileChooser: n.chooser}
			if err := emit(e); err != nil {
				return nil
			}
		case <-changed:
			if err := sendTab(); err != nil {
				return nil
			}
		case <-tick.C:
			if t.closed.Load() {
				_ = emit(LiveEvent{End: LiveEndTabClosed})
				return nil
			}
			if err := sendTab(); err != nil {
				return nil
			}
		case <-still.C:
			if framed {
				continue
			}
			// Stills repeat until the screencast sends a frame: one that failed
			// (a page mid-navigation) is tried again, and a window that does
			// not paint stays current.
			if f := inst.liveStill(ctx, v.ctx, o); f != nil {
				if err := emit(LiveEvent{Frame: f}); err != nil {
					return nil
				}
			}
			still.Reset(liveStillAfter)
		}
	}
}

func frameOf(ev page.EventScreencastFrame) *LiveFrame {
	f := &LiveFrame{JPEG: ev.Data, Scale: 1}
	if m := ev.Metadata; m != nil {
		f.Width, f.Height, f.OffsetTop = m.DeviceWidth, m.DeviceHeight, m.OffsetTop
		if m.PageScaleFactor > 0 {
			f.Scale = m.PageScaleFactor
		}
	}
	return f
}

// liveStill renders one frame of a tab that sent none, as a page that does not
// repaint never does.
func (i *Instance) liveStill(ctx, sess context.Context, o LiveOptions) *LiveFrame {
	octx, cancel := opCtx(ctx, sess, shotDeadline)
	defer cancel()
	m, err := chromedp.Call(octx, page.GetLayoutMetrics, struct{}{})
	if err != nil || m.CSSVisualViewport == nil || m.CSSVisualViewport.ClientWidth <= 0 || m.CSSVisualViewport.ClientHeight <= 0 {
		return nil
	}
	vw, vh := m.CSSVisualViewport.ClientWidth, m.CSSVisualViewport.ClientHeight
	scale := 1.0
	if o.MaxWidth > 0 {
		scale = min(scale, float64(o.MaxWidth)/vw)
	}
	if o.MaxHeight > 0 {
		scale = min(scale, float64(o.MaxHeight)/vh)
	}
	r, err := chromedp.Call(octx, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatJpeg, Quality: ptr(int64(liveQuality)),
		Clip: &page.Viewport{Width: vw, Height: vh, Scale: max(scale, 0.05)}})
	if err != nil || len(r.Data) == 0 {
		return nil
	}
	return &LiveFrame{JPEG: r.Data, Width: vw, Height: vh, Scale: 1}
}

// liveState reads the toolbar state of a tab. A history that cannot be read
// leaves the address and the buttons empty.
func (i *Instance) liveState(ctx, sess context.Context, t *tab, loading bool) LiveTab {
	out := LiveTab{Loading: loading}
	out.Driven, out.RunID = t.drivenBy()
	octx, cancel := opCtx(ctx, sess, inputDeadline)
	defer cancel()
	h, err := chromedp.Call(octx, page.GetNavigationHistory, cdp.Empty{})
	if err != nil || h.CurrentIndex < 0 || int(h.CurrentIndex) >= len(h.Entries) {
		return out
	}
	cur := h.Entries[h.CurrentIndex]
	out.URL, out.Title = cur.URL, truncate(cur.Title, 200)
	out.CanGoBack = h.CurrentIndex > 0
	out.CanGoForward = int(h.CurrentIndex) < len(h.Entries)-1
	return out
}

// userTab is the tab and viewer session for the user's own input. A tab a run
// drives takes none.
func (c *Control) userTab(tabID string) (*viewer, func(), error) {
	inst, t, err := c.liveTab(tabID)
	if err != nil {
		return nil, nil, err
	}
	if driven, _ := t.drivenBy(); driven {
		return nil, nil, newError(CodeTabDriven, "Applysmith is using this tab; it takes no input until the run ends or hands it to you")
	}
	return inst.openViewer(t)
}

// Input dispatches the user's input to an apply tab no run drives, as trusted
// events. A press arms file-chooser interception for a second, so a file
// picker the press opens is announced to the panel instead of opening in the
// Chrome window where nobody sees it. Each event has its own bound: Chrome
// answers an event once the page has handled it, which on a busy machine took
// up to a second an event, so a fixed bound for the whole batch failed a long
// one.
func (c *Control) Input(ctx context.Context, tabID string, events []InputEvent) error {
	v, release, err := c.userTab(tabID)
	if err != nil {
		return err
	}
	defer release()
	for _, e := range events {
		octx, cancel := opCtx(ctx, v.ctx, clickDeadline)
		err := c.input(octx, v, tabID, e)
		cancel()
		if err != nil {
			return wrapError(CodeOperationTimeout, err, "the tab did not take the input")
		}
	}
	return nil
}

func (c *Control) input(ctx context.Context, v *viewer, tabID string, e InputEvent) error {
	if e.Kind == "fit" {
		if inst, t, err := c.liveTab(tabID); err == nil {
			inst.fit(ctx, t, e.X, e.Y)
		}
		return nil
	}
	if (e.Kind == "mouse" || e.Kind == "key") && e.Type == "down" {
		v.armChooser(ctx)
	}
	return dispatchInput(ctx, e)
}

// armChooser turns file-chooser interception on in the viewer session and off
// again after a quiet second, so a picker the user opens in the Chrome window
// itself still appears there.
func (v *viewer) armChooser(ctx context.Context) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.armed {
		if _, err := chromedp.Call(ctx, page.SetInterceptFileChooserDialog, page.SetInterceptFileChooserDialogParams{Enabled: true}); err != nil {
			return
		}
		v.armed = true
	}
	if v.disarm != nil {
		v.disarm.Stop()
	}
	v.disarm = time.AfterFunc(chooserArmed, func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		dctx, cancel := context.WithTimeout(v.ctx, inputDeadline)
		defer cancel()
		_, _ = chromedp.Call(dctx, page.SetInterceptFileChooserDialog, page.SetInterceptFileChooserDialogParams{Enabled: false})
		v.armed = false
	})
}

var mouseTypes = map[string]input.DispatchMouseEventType{
	"move": input.DispatchMouseEventTypeMouseMoved,
	"down": input.DispatchMouseEventTypeMousePressed,
	"up":   input.DispatchMouseEventTypeMouseReleased,
}

// wheelParams is Input.dispatchMouseEvent for a wheel. Chrome refuses a wheel
// without both deltas, and cdproto's params drop a zero one, as a straight
// vertical scroll has.
type wheelParams struct {
	Type      input.DispatchMouseEventType `json:"type"`
	X         float64                      `json:"x"`
	Y         float64                      `json:"y"`
	Modifiers int64                        `json:"modifiers"`
	DeltaX    float64                      `json:"deltaX"`
	DeltaY    float64                      `json:"deltaY"`
}

var dispatchWheel = cdp.Command[wheelParams, cdp.Empty]{Method: input.CommandDispatchMouseEvent}

func dispatchInput(ctx context.Context, e InputEvent) error {
	mods := int64(e.Modifiers)
	switch e.Kind {
	case "mouse":
		_, err := chromedp.Call(ctx, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: mouseTypes[e.Type], X: e.X, Y: e.Y,
			Modifiers: mods, Button: input.MouseButton(e.Button), Buttons: int64(e.Buttons), ClickCount: int64(e.ClickCount)})
		return err
	case "wheel":
		_, err := chromedp.Call(ctx, dispatchWheel, wheelParams{Type: input.DispatchMouseEventTypeMouseWheel,
			X: e.X, Y: e.Y, Modifiers: mods, DeltaX: e.DeltaX, DeltaY: e.DeltaY})
		return err
	case "key":
		p := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Modifiers: mods, Key: e.Key, Code: e.Code,
			WindowsVirtualKeyCode: int64(e.KeyCode), NativeVirtualKeyCode: int64(e.KeyCode)}
		if e.Type == "down" {
			// A key that types text is a keyDown carrying it; any other is a raw
			// key down, which types nothing.
			p.Type, p.Text, p.UnmodifiedText = input.DispatchKeyEventTypeRawKeyDown, "", ""
			if e.Text != "" {
				p.Type, p.Text, p.UnmodifiedText = input.DispatchKeyEventTypeKeyDown, e.Text, e.Text
			}
		}
		_, err := chromedp.Call(ctx, input.DispatchKeyEvent, p)
		return err
	case "text":
		_, err := chromedp.Call(ctx, input.InsertText, input.InsertTextParams{Text: e.Text})
		return err
	}
	return newError(CodeNotAllowed, "unknown input kind %q", e.Kind)
}

// Navigation actions of the panel's toolbar.
const (
	NavBack    = "back"
	NavForward = "forward"
	NavReload  = "reload"
	NavStop    = "stop"
	NavGo      = "go"
)

// Navigate moves an apply tab no run drives back, forward, reloads it, stops
// its load or loads rawURL (go), and returns without waiting for the load.
func (c *Control) Navigate(ctx context.Context, tabID, action, rawURL string) error {
	v, release, err := c.userTab(tabID)
	if err != nil {
		return err
	}
	defer release()
	octx, cancel := opCtx(ctx, v.ctx, inputDeadline)
	defer cancel()
	switch action {
	case NavBack, NavForward:
		h, err := chromedp.Call(octx, page.GetNavigationHistory, cdp.Empty{})
		if err != nil {
			return wrapError(CodeOperationTimeout, err, "the tab's history could not be read")
		}
		to := int(h.CurrentIndex) - 1
		if action == NavForward {
			to = int(h.CurrentIndex) + 1
		}
		if to < 0 || to >= len(h.Entries) {
			return nil
		}
		_, err = chromedp.Call(octx, page.NavigateToHistoryEntry, page.NavigateToHistoryEntryParams{EntryID: h.Entries[to].ID})
		return err
	case NavReload:
		_, err = chromedp.Call(octx, page.Reload, page.ReloadParams{})
	case NavStop:
		_, err = chromedp.Call(octx, page.StopLoading, cdp.Empty{})
	case NavGo:
		if err := checkNavigationURL(rawURL); err != nil {
			return err
		}
		_, err = chromedp.Call(octx, page.Navigate, page.NavigateParams{URL: rawURL})
	default:
		return newError(CodeNotAllowed, "unknown navigation %q", action)
	}
	if err != nil {
		return wrapError(CodeOperationTimeout, err, "the tab did not take the navigation")
	}
	return nil
}

// NewTab opens a tab for the user in the apply window, starting the browser
// when it is not running, and loads rawURL in it when one is given. Unlike
// Show it raises nothing: the panel shows the tab.
func (c *Control) NewTab(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, firstUseBudget)
	defer cancel()
	inst, err := c.open(ctx)
	if err != nil {
		return "", err
	}
	h := inst.Lane(LaneApply)
	id, err := h.OpenTab(ctx, "")
	if err != nil {
		return "", err
	}
	if err := h.Keep(id); err != nil {
		return "", err
	}
	if rawURL == "" {
		return id, nil
	}
	return id, c.Navigate(ctx, id, NavGo, rawURL)
}
