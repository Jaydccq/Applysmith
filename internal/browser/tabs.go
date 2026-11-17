package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	logdom "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// CodeTabMisplaced reports a new apply tab that Chrome put in another window.
const CodeTabMisplaced Code = "tab-misplaced"

func ptr[T any](v T) *T { return &v }

// LaneHandle is the only way to operate on tabs. Every operation checks the
// tab's lane label before any DevTools command is sent.
//
// A handle made by a lease follows the lease through an automatic recovery; a
// handle made by Instance.Lane has no lease and works on that instance only.
type LaneHandle struct {
	base  *Instance
	lane  Lane
	lease *Lease
}

// Lane returns the handle for one lane.
func (i *Instance) Lane(l Lane) *LaneHandle { return &LaneHandle{base: i, lane: l} }

// in is the instance the handle works on now.
func (h *LaneHandle) in() *Instance {
	if h.lease != nil {
		return h.lease.current()
	}
	return h.base
}

func (h *LaneHandle) tab(id string) (*tab, error) {
	i, err := h.ready(context.Background())
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	t := i.tabs[target.ID(id)]
	i.mu.Unlock()
	if t == nil || t.closed.Load() {
		if h.lease != nil && h.lease.wasLost(id) {
			return nil, newError(CodeTabClosed, "the tab was lost when the browser restarted")
		}
		return nil, newError(CodeTabClosed, "no such tab")
	}
	if t.lane != h.lane {
		i.logEvent(EventRequestRefused, h.lane, string(CodeWrongLane))
		return nil, newError(CodeWrongLane, "tab belongs to the %s lane, not the %s lane", t.lane, h.lane)
	}
	if h.lease != nil {
		t.driver.Store(h.lease)
	}
	return t, nil
}

func (i *Instance) laneCount(l Lane) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	n := 0
	for _, t := range i.tabs {
		if t.lane == l && !t.closed.Load() {
			n++
		}
	}
	return n
}

// bctx derives a browser-level context from the instance root that ends with
// ctx. A caller's plain context cannot carry the browser connection itself.
func (i *Instance) bctx(ctx context.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(i.root)
	if dl, ok := ctx.Deadline(); ok {
		c, cancel = context.WithDeadline(i.root, dl)
	}
	stop := context.AfterFunc(ctx, cancel)
	return c, func() { stop(); cancel() }
}

func (i *Instance) windowOf(ctx context.Context, id target.ID) (int64, error) {
	bc, cancel := i.bctx(ctx)
	defer cancel()
	r, err := chromedp.CallBrowser(bc, browser.GetWindowForTarget, browser.GetWindowForTargetParams{TargetID: id})
	if err != nil {
		return 0, err
	}
	return int64(r.WindowID), nil
}

// createGate, when set, runs once a tab creation is under way, before its
// command is sent. It lets a test hold a creation pending.
var createGate func(Lane)

// setupGate, when set, runs once a new tab is registered, before its session
// is set up. It lets a test lose the connection in between.
var setupGate func(target.ID)

// TabOptions tunes a new tab.
type TabOptions struct {
	// LoadEverything keeps the scan-lane resource policy off for the tab, as
	// when the user asked to watch a scan.
	LoadEverything bool
}

// OpenTab creates a blank tab for the lane. Windows are created and tabs
// opened without raising anything: a scan window opens in the background
// behind every other window, and the apply window does not take focus.
func (h *LaneHandle) OpenTab(ctx context.Context, owner string) (string, error) {
	return h.OpenTabWith(ctx, owner, TabOptions{})
}

// OpenTabWith is OpenTab with options. A tab of a lease belongs to the lease
// from the moment its creation starts, so a creation the browser has not yet
// acknowledged still holds the lease. A lease's creation that finds the
// browser gone acted on no page, so it is made once more on the browser the
// lease's one recovery brings.
func (h *LaneHandle) OpenTabWith(ctx context.Context, owner string, o TabOptions) (string, error) {
	i, err := h.ready(ctx)
	if err != nil {
		return "", err
	}
	if h.lease != nil {
		if err := h.lease.beginCreate(); err != nil {
			return "", err
		}
		if owner == "" {
			owner = h.lease.run
		}
	}
	id, err := i.openTab(ctx, h.lane, owner, h.lease, o)
	if err != nil && h.lease != nil && i.deadInfo() != nil && ctx.Err() == nil {
		if i, err = h.ready(ctx); err == nil {
			id, err = i.openTab(ctx, h.lane, owner, h.lease, o)
		}
	}
	if h.lease != nil && h.lease.endCreate() && err == nil {
		// The lease ended while the tab was being made; it must not outlive it.
		if t := i.lookup(target.ID(id)); t != nil {
			i.dropTab(context.WithoutCancel(ctx), t, "run-ended")
		}
		return "", newError(CodeLeaseReleased, "the lease on the %s lane ended while the tab was being created", h.lane)
	}
	return id, err
}

func (i *Instance) openTab(ctx context.Context, lane Lane, owner string, lease *Lease, o TabOptions) (string, error) {
	i.createMu.Lock()
	defer i.createMu.Unlock()
	cctx, cancel := context.WithTimeout(i.root, createDeadline)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	if n := i.laneCount(lane); n >= laneCap(lane) {
		return "", newError(CodeLaneTabLimit, "the %s lane already holds %d tabs, the cap", lane, n)
	}
	// The creation is counted until it returns: the browser may make its tab
	// although the connection drops before the answer, and the record written
	// at the loss hands it to the instance that adopts the browser.
	i.beginCreation()
	defer i.endCreation()
	if createGate != nil {
		createGate(lane)
	}
	id, win, err := i.createTarget(cctx, lane)
	if err != nil {
		// A lost browser or a timeout that create already named is named once.
		code, _ := CodeOf(err)
		if cctx.Err() != nil && code != CodeBrowserDisconnected {
			if i.confirmGone() {
				return "", wrapError(CodeBrowserDisconnected, err, "the browser exited during tab creation")
			}
			// The browser may still make the tab; it is closed when it arrives.
			i.noteAbandoned()
			if code != CodeOperationTimeout {
				err = timeoutError("tab creation", createDeadline, err)
			}
		}
		return "", err
	}
	t := &tab{id: id, lane: lane, owner: owner, window: win, created: time.Now(), lease: lease, loadEverything: o.LoadEverything}
	t.settingUp.Store(true)
	i.mu.Lock()
	i.tabs[id] = t
	i.touchLocked()
	i.mu.Unlock()
	if setupGate != nil {
		setupGate(id)
	}
	if _, err := i.session(t); err != nil {
		t.setCloseReason("setup-failed")
		i.dropTab(cctx, t, "setup-failed")
		return "", err
	}
	t.settingUp.Store(false)
	i.logTab(EventTabOpened, t, "created")
	return string(id), nil
}

// createTarget opens a blank page for lane without raising anything.
//
// Every scan tab gets its own window, created in the background so Chrome
// orders it behind every other window and leaves the foreground application
// alone. Opening a tab from a script, or activating a tab, brings Chrome to the
// front, so neither is used to place a tab. A minimized window is not used
// either: it renders but cannot be captured. An apply tab joins the apply
// window when one exists. Chrome opens such a tab in the most recently active
// window, which can be a scan window that a page popup raised, so the result
// is verified and a misplaced tab is replaced by one in a new apply window.
// The apply window can also be gone already: its last tab closed and the
// instance has not yet heard of it. Chrome then has no window to put the tab
// in, and the tab opens a new apply window instead.
func (i *Instance) createTarget(ctx context.Context, lane Lane) (target.ID, int64, error) {
	if lane == LaneApply && i.hasWindow(LaneApply) {
		id, err := i.create(ctx, target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(false), Focus: ptr(false)})
		var cerr *cdproto.Error
		if err != nil && !(errors.As(err, &cerr) && strings.Contains(cerr.Message, "no browser is open")) {
			return "", 0, err
		}
		if err == nil {
			win, err := i.windowOf(ctx, id)
			if err != nil {
				i.closeTarget(id, "setup-failed")
				return "", 0, wrapError(CodeTabClosed, err, "new tab disappeared")
			}
			i.mu.Lock()
			l := i.windows[win]
			i.mu.Unlock()
			if l == LaneApply {
				return id, win, nil
			}
			i.closeTarget(id, "setup-failed")
		}
	}
	params := target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(true), Focus: ptr(false)}
	if lane == LaneScan {
		params = target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(true), Background: ptr(true), Width: scanViewportW, Height: scanViewportH}
	}
	id, err := i.create(ctx, params)
	if err != nil {
		return "", 0, err
	}
	win, err := i.windowOf(ctx, id)
	if err != nil {
		i.closeTarget(id, "setup-failed")
		return "", 0, wrapError(CodeTabClosed, err, "new tab disappeared")
	}
	i.mu.Lock()
	i.windows[win] = lane
	i.mu.Unlock()
	i.writeRecord()
	return id, win, nil
}

func (i *Instance) hasWindow(lane Lane) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, l := range i.windows {
		if l == lane {
			return true
		}
	}
	return false
}

func (i *Instance) create(ctx context.Context, p target.CreateTargetParams) (target.ID, error) {
	r, err := chromedp.CallBrowser(ctx, target.CreateTarget, p)
	if err != nil {
		if i.root.Err() != nil && i.confirmGone() {
			return "", wrapError(CodeBrowserDisconnected, err, "lost the connection to the browser during tab creation")
		}
		if ctx.Err() != nil {
			return "", timeoutError("tab creation", createDeadline, err)
		}
		return "", wrapError(CodeBrowserDisconnected, err, "cannot create a tab")
	}
	return r.TargetID, nil
}

// setupTab installs the per-tab policy: Network events off until requested,
// file chooser interception, dialog and navigation guards, and focus
// emulation.
func (i *Instance) setupTab(ctx context.Context, t *tab) error {
	sctx, cancel := context.WithTimeout(ctx, setupDeadline)
	defer cancel()
	if _, err := chromedp.Call(sctx, network.Disable, cdp.Empty{}); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot attach to the tab")
	}
	if _, err := chromedp.Call(sctx, page.SetInterceptFileChooserDialog, page.SetInterceptFileChooserDialogParams{Enabled: true}); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot intercept the file chooser")
	}
	// A tab that is not its window's active tab, or whose window is covered,
	// still renders and reports focus.
	// The command can hang on a page that never loaded, so it has a short bound
	// of its own, and a failure is recorded and never raised.
	fctx, fcancel := context.WithTimeout(sctx, 5*time.Second)
	if _, err := chromedp.Call(fctx, emulation.SetFocusEmulationEnabled, emulation.SetFocusEmulationEnabledParams{Enabled: true}); err != nil {
		i.logTab(EventFocusEmulationFail, t, "cannot enable focus emulation")
	}
	fcancel()
	if t.lane == LaneScan {
		if err := i.installScanStubs(ctx, t); err != nil {
			return err
		}
		if err := i.installResourcePolicy(ctx, sctx, t); err != nil {
			return err
		}
	}
	watch(ctx, page.JavascriptDialogOpening, func(ev page.EventJavascriptDialogOpening) { i.onDialog(ctx, t, ev) })
	watch(ctx, page.FileChooserOpened, func(page.EventFileChooserOpened) {
		if !t.isKept() {
			i.logEvent("file_chooser_intercepted", t.lane, "")
		}
	})
	watch(ctx, page.FrameRequestedNavigation, func(ev page.EventFrameRequestedNavigation) { i.onNavigationRequest(ctx, t, ev) })
	watch(ctx, logdom.EntryAdded, func(ev logdom.EventEntryAdded) { i.onLogEntry(ctx, t, ev.Entry) })
	return nil
}

// watch runs fn for each event on the session until the session ends.
func watch[E any](ctx context.Context, ev cdp.Event[E], fn func(E)) {
	events := chromedp.Events(ctx, ev)
	go func() {
		for e, err := range events {
			if err != nil {
				return
			}
			fn(e)
		}
	}()
}

func (i *Instance) onDialog(ctx context.Context, t *tab, ev page.EventJavascriptDialogOpening) {
	if t.isKept() {
		return
	}
	accept := false
	switch ev.Type {
	case page.DialogTypeAlert:
		accept = true
	case page.DialogTypeBeforeunload:
		accept = t.navigating.Load()
	}
	msg := ev.Message
	if len(msg) > 400 {
		msg = msg[:400]
	}
	i.logTab(EventDialogHandled, t, string(ev.Type)+": "+msg)
	dctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := chromedp.Call(dctx, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: accept}); err != nil {
		i.logEvent("dialog_answer_failed", t.lane, "")
	}
}

// leavesAllowlist reports a frame navigation that takes the tab's own page to
// an address outside the scheme allowlist. A subframe's navigation (the hidden
// frame a tracking script gives `about:srcdoc`) is not the tab's page: the
// main frame of a page target carries the target's id.
func leavesAllowlist(tab target.ID, ev page.EventFrameRequestedNavigation) bool {
	return string(ev.FrameID) == string(tab) && !schemeAllowed(ev.URL)
}

func (i *Instance) onNavigationRequest(ctx context.Context, t *tab, ev page.EventFrameRequestedNavigation) {
	if t.isKept() || !leavesAllowlist(t.id, ev) {
		return
	}
	i.logTab(EventNavigationBlocked, t, blockedURL(ev.URL))
	nctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = chromedp.Call(nctx, page.Navigate, page.NavigateParams{URL: "about:blank"})
}

// blockedPrefix starts the console error Chrome raises when a web page tries
// to navigate to a local scheme. Chrome refuses such a navigation itself and
// emits no navigation event, so this message is the only signal.
const blockedPrefix = "Not allowed to load local resource: "

func (i *Instance) onLogEntry(ctx context.Context, t *tab, e *logdom.Entry) {
	if t.isKept() || e.Source != logdom.EntrySourceSecurity || !strings.HasPrefix(e.Text, blockedPrefix) {
		return
	}
	blocked := strings.TrimPrefix(e.Text, blockedPrefix)
	if schemeAllowed(blocked) {
		return
	}
	i.logTab(EventNavigationBlocked, t, blockedURL(blocked))
	nctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = chromedp.Call(nctx, page.Navigate, page.NavigateParams{URL: "about:blank"})
}

func urlScheme(raw string) string {
	if i := strings.Index(raw, ":"); i > 0 && i < 20 {
		return raw[:i+1]
	}
	return "?"
}

// NavResult is the outcome of a navigation. A page that fails to load is a
// result carrying the browser's error text, not a fault.
type NavResult struct {
	Status    int
	URL       string
	Title     string
	LoadState string
	Error     string
}

// Navigate loads rawURL in the tab. The URL scheme is checked before any
// DevTools command is sent.
func (h *LaneHandle) Navigate(ctx context.Context, tabID, rawURL string) (NavResult, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return NavResult{}, err
	}
	if err := checkNavigationURL(rawURL); err != nil {
		return NavResult{}, err
	}
	sess, err := h.session(t)
	if err != nil {
		return NavResult{}, err
	}
	octx, cancel := opCtx(ctx, sess, navigateDeadline)
	defer cancel()
	t.navigating.Store(true)
	defer t.navigating.Store(false)
	// Response status shares Network events with any active response watch.
	release, err := t.observeNetwork(octx, sess)
	if err != nil {
		return NavResult{}, h.wrapOp(t, err, "navigation", navigateDeadline)
	}
	defer release()
	resp, err := chromedp.Run(octx, chromedp.NavigateResponse(rawURL))
	var le *chromedp.LoadError
	switch {
	case errors.As(err, &le):
		if strings.Contains(le.ErrorText, "ERR_INTERNET_DISCONNECTED") {
			return NavResult{}, newError(CodeNetworkOffline, "the host has no network")
		}
		h.in().loadFailed(t, le.ErrorText)
		return NavResult{URL: rawURL, LoadState: "failed", Error: le.ErrorText}, nil
	case err != nil:
		return NavResult{}, h.wrapOp(t, err, "navigation", navigateDeadline)
	}
	res := NavResult{LoadState: "load"}
	if resp != nil {
		res.Status, res.URL = int(resp.Status), resp.URL
	}
	if loc, err := chromedp.Run(octx, chromedp.Location()); err == nil {
		res.URL = loc
	}
	res.Title, _ = chromedp.Run(octx, chromedp.Title())
	return res, nil
}

// NavigateDOM loads rawURL and returns once the new document has been parsed
// (DOMContentLoaded), without waiting for its load event. A single-page
// application keeps loading for as long as it likes; a caller that polls for
// the element it needs must not be held up by the load event. The HTTP status
// belongs to the new main document after redirects, never an iframe. A
// same-document or non-HTTP navigation has no new response and carries 0.
func (h *LaneHandle) NavigateDOM(ctx context.Context, tabID, rawURL string) (NavResult, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return NavResult{}, err
	}
	if err := checkNavigationURL(rawURL); err != nil {
		return NavResult{}, err
	}
	sess, err := h.session(t)
	if err != nil {
		return NavResult{}, err
	}
	octx, cancel := opCtx(ctx, sess, navigateDeadline)
	defer cancel()
	t.navigating.Store(true)
	defer t.navigating.Store(false)
	// Subscribe before navigation: the response can arrive before Page.navigate
	// returns. The iterator is scoped to this operation and never reads bodies.
	release, err := t.observeNetwork(octx, sess)
	if err != nil {
		return NavResult{}, h.wrapOp(t, err, "navigation", navigateDeadline)
	}
	defer release()
	responses := chromedp.Events(octx, network.ResponseReceived)
	const state = `({href: location.href, ready: document.readyState})`
	type docState struct {
		Href  string `json:"href"`
		Ready string `json:"ready"`
	}
	prev, _ := chromedp.Run(octx, chromedp.Evaluate[docState](state))
	res, err := chromedp.Call(octx, page.Navigate, page.NavigateParams{URL: rawURL})
	switch {
	case err != nil:
		return NavResult{}, h.wrapOp(t, err, "navigation", navigateDeadline)
	case res.ErrorText != "":
		if strings.Contains(res.ErrorText, "ERR_INTERNET_DISCONNECTED") {
			return NavResult{}, newError(CodeNetworkOffline, "the host has no network")
		}
		h.in().loadFailed(t, res.ErrorText)
		return NavResult{URL: rawURL, LoadState: "failed", Error: res.ErrorText}, nil
	}
	status := 0
	u, _ := url.Parse(rawURL)
	if res.LoaderID != "" && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) {
		for ev, err := range responses {
			if err != nil {
				return NavResult{}, h.wrapOp(t, err, "navigation", navigateDeadline)
			}
			if ev.Type == network.ResourceTypeDocument && ev.FrameID == res.FrameID && ev.LoaderID == res.LoaderID {
				status = int(ev.Response.Status)
				break
			}
		}
	}
	for {
		cur, err := chromedp.Run(octx, chromedp.Evaluate[docState](state))
		if err == nil && (cur.Href != prev.Href || prev.Href == rawURL) && (cur.Ready == "interactive" || cur.Ready == "complete") {
			title, _ := chromedp.Run(octx, chromedp.Title())
			return NavResult{Status: status, URL: cur.Href, Title: title, LoadState: "domcontentloaded"}, nil
		}
		select {
		case <-octx.Done():
			return NavResult{}, h.wrapOp(t, octx.Err(), "navigation", navigateDeadline)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// wrapOp maps a failed DevTools operation to a typed error. A page script that
// threw is eval-failed: the page answered, so neither the browser nor the tab
// is lost. A browser that is gone is browser-disconnected, whatever else the
// failure looked like; a page that is gone is tab-closed; a deadline is
// operation-timeout naming the operation and its bound, and never closes the
// tab. Any other failure, with the browser and the tab both still there, is
// operation-failed.
func (h *LaneHandle) wrapOp(t *tab, err error, op string, bound time.Duration) error {
	i := h.in()
	// A caller that gave up, or a tab handed to the user, cancels the call while
	// the connection stands: nothing says the browser is gone, and asking costs
	// a process listing and a request to the endpoint.
	gaveUp := errors.Is(err, context.Canceled) && i.root.Err() == nil
	var thrown *chromedp.ExceptionError
	switch {
	case errors.As(err, &thrown):
		return newError(CodeEvalFailed, "the page script threw: %s", exceptionText(thrown.ExceptionDetails))
	case i.deadInfo() != nil:
		return wrapError(CodeBrowserDisconnected, err, "the browser exited during %s", op)
	case t.closed.Load():
		return wrapError(CodeTabClosed, err, "the tab is closed")
	case errors.Is(err, context.DeadlineExceeded):
		t.noteSlow(i)
		return timeoutError(op, bound, err)
	case !gaveUp && i.confirmGone():
		return wrapError(CodeBrowserDisconnected, err, "the browser exited during %s", op)
	case i.targetMissing(t.id):
		// The browser closed the page underneath; no close() call ever ran.
		t.setCloseReason("closed-by-page-or-system")
		i.markClosed(t.id)
		return wrapError(CodeTabClosed, err, "the tab is closed")
	case documentReplaced(err):
		return wrapError(CodeDocumentReplaced, err, "a navigation replaced the document during %s", op)
	}
	return wrapError(CodeOperationFailed, err, "DevTools operation failed during %s", op)
}

// documentReplaced reports a DevTools failure that says the page navigated
// under the call. Checked after a closed tab and a lost browser, it means the
// browser and the tab are fine. Chrome says it as the inspector's "Inspected
// target navigated or closed" or as V8's words for the document's script
// context, which the navigation destroyed.
func documentReplaced(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Inspected target navigated or closed") || strings.Contains(msg, "Execution context was destroyed") ||
		strings.Contains(msg, "Cannot find context with specified id")
}

// loadFailed records a main-frame load that failed. A navigation superseded by
// another is routine and is not recorded.
func (i *Instance) loadFailed(t *tab, text string) {
	if strings.Contains(text, "ERR_ABORTED") {
		return
	}
	i.logTab(EventLoadFailed, t, text)
}

// targetMissing asks the browser whether it still has the page.
func (i *Instance) targetMissing(id target.ID) bool {
	ctx, cancel := context.WithTimeout(i.root, 2*time.Second)
	defer cancel()
	_, err := chromedp.CallBrowser(ctx, target.GetTargetInfo, target.GetTargetInfoParams{TargetID: id})
	return err != nil && strings.Contains(err.Error(), "No target with given id")
}

// noteSlow records, once, that a page stopped answering in time.
func (t *tab) noteSlow(i *Instance) {
	if !t.slow.Swap(true) {
		i.logTab(EventTabUnresponsive, t, "operation timed out")
	}
}

// noteResponsive records that a page that was slow answers again.
func (t *tab) noteResponsive(i *Instance) {
	if t.slow.Swap(false) {
		i.logTab(EventTabResponsive, t, "")
	}
}

// Evaluate runs expr in the page's main world and returns the JSON result. A
// timeout is reported as such and leaves the tab open. A script that threw is
// eval-failed (wrapOp): the page answered, so the browser is not lost.
func (h *LaneHandle) Evaluate(ctx context.Context, tabID, expr string) (json.RawMessage, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return nil, err
	}
	sess, err := h.session(t)
	if err != nil {
		return nil, err
	}
	octx, cancel := opCtx(ctx, sess, evalDeadline)
	defer cancel()
	out, err := chromedp.Run(octx, chromedp.Evaluate[[]byte](expr, chromedp.EvalAwaitPromise))
	if err != nil {
		return nil, h.wrapOp(t, err, "script evaluation", evalDeadline)
	}
	t.noteResponsive(h.in())
	if len(out) == 0 {
		out = []byte("null")
	}
	return out, nil
}

// Screenshot captures the visible viewport as PNG.
func (h *LaneHandle) Screenshot(ctx context.Context, tabID string) ([]byte, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return nil, err
	}
	sess, err := h.session(t)
	if err != nil {
		return nil, err
	}
	octx, cancel := opCtx(ctx, sess, shotDeadline)
	defer cancel()
	out, err := chromedp.Run(octx, chromedp.CaptureScreenshot())
	if err != nil {
		return nil, h.wrapOp(t, err, "screenshot", shotDeadline)
	}
	return out, nil
}

// CodeNotApplyLane reports a window-raising operation on a scan tab.
const CodeNotApplyLane Code = "not-apply-lane"

// ShowWindow makes the tab the active tab of the apply window and brings the
// window to the foreground. Chrome's activation command always takes the
// foreground, so this is only for an explicit user action (the show-window
// control) and never for a run; tabs are created in the background and render
// without activation.
func (h *LaneHandle) ShowWindow(ctx context.Context, tabID string) error {
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	if h.lane != LaneApply {
		return newError(CodeNotApplyLane, "only an apply tab can be shown")
	}
	h.in().unminimize(ctx, t)
	bc, cancel := h.in().bctx(ctx)
	defer cancel()
	bc, cancel2 := context.WithTimeout(bc, inputDeadline)
	defer cancel2()
	if _, err := chromedp.CallBrowser(bc, target.ActivateTarget, target.ActivateTargetParams{TargetID: t.id}); err != nil {
		return h.wrapOp(t, err, "showing the window", inputDeadline)
	}
	t.markShown()
	return nil
}

// CloseTab closes one tab of the lane.
func (h *LaneHandle) CloseTab(ctx context.Context, tabID string) error {
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	h.in().dropTab(ctx, t, "closed-by-client")
	return nil
}

// closeBound bounds one attempt to close a page. A variable so a test can
// shorten it.
var closeBound = 5 * time.Second

// closeRetries is how many more times a stuck close is tried in the
// background.
const closeRetries = 3

// closeCall asks the browser to close a page. A field of the instance seam in
// tests only.
func (i *Instance) closeCall(ctx context.Context, id target.ID) error {
	if i.closeFn != nil {
		return i.closeFn(ctx, id)
	}
	_, err := chromedp.CallBrowser(ctx, target.CloseTarget, target.CloseTargetParams{TargetID: id})
	return err
}

// dropTab closes a page. The wait is bounded: a page that does not close in
// time is forgotten here and its close is retried in the background, so a
// stuck page never hangs the caller.
func (i *Instance) dropTab(ctx context.Context, t *tab, reason string) {
	t.setCloseReason(reason)
	t.detach()
	cctx, cancel := context.WithTimeout(i.root, closeBound)
	stop := context.AfterFunc(ctx, cancel)
	err := i.closeCall(cctx, t.id)
	stop()
	cancel()
	i.markClosed(t.id)
	if err != nil && i.deadInfo() == nil && !i.isStopped() {
		go i.retryClose(t.id, closeBound)
	}
}

// retryClose tries again, in the background, to close a page that did not
// close in time. The bound is passed in, read by the caller, so the goroutine
// shares no variable with it.
func (i *Instance) retryClose(id target.ID, bound time.Duration) {
	for range closeRetries {
		select {
		case <-i.root.Done():
			return
		case <-time.After(bound / 2):
		}
		c, cancel := context.WithTimeout(i.root, bound)
		err := i.closeCall(c, id)
		cancel()
		if err == nil {
			return
		}
	}
	i.logEvent(EventRequestRefused, "", "a page would not close")
}

func (i *Instance) markClosed(id target.ID) {
	i.mu.Lock()
	t := i.tabs[id]
	delete(i.tabs, id)
	if t != nil {
		i.lastClosed = i.clock()
		empty := true
		for _, o := range i.tabs {
			if o.window == t.window {
				empty = false
			}
		}
		if empty {
			delete(i.windows, t.window)
		}
		i.touchLocked()
	}
	i.mu.Unlock()
	if t != nil {
		if !t.closed.Swap(true) {
			i.logTab(EventTabClosed, t, t.closeReason())
		}
		t.detach()
		t.runClosed()
	}
}

// Keep marks a tab as kept for the user: it survives CloseOwned and the
// instance stops answering its dialogs.
func (h *LaneHandle) Keep(tabID string) error {
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	t.kept.Store(true)
	t.detach()
	return nil
}

// Reclaim takes a tab kept for the user back for a run that goes on in it, which
// only the user's Done does. It is the inverse of Keep: the next command
// attaches a session as it would to any tab of the lane, and the page and
// everything the user typed in it stay as they are.
func (h *LaneHandle) Reclaim(tabID string) error {
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	t.kept.Store(false)
	return nil
}

// Tabs lists the lane's tabs.
func (h *LaneHandle) Tabs() []TabInfo {
	i := h.in()
	i.mu.Lock()
	defer i.mu.Unlock()
	var out []TabInfo
	for _, t := range i.tabs {
		if t.lane == h.lane && !t.closed.Load() {
			out = append(out, TabInfo{ID: string(t.id), Lane: t.lane, Owner: t.owner, Window: t.window, Kept: t.isKept(), Opener: string(t.opener), Created: t.created})
		}
	}
	return out
}
