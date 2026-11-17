// Clean-room: docs/provenance.md#CR-BROWSER-VIEW

package browser

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Lane names the owner of a page target.
type Lane string

// Lanes.
const (
	LaneApply Lane = "apply"
	LaneScan  Lane = "scan"
)

// Tab caps per lane, counting popups. Variables so tests can lower them.
var (
	applyTabCap = 24
	scanTabCap  = 32
)

// Popup budget: at most popupBudget popup targets per popupWindow in one window.
const (
	popupBudget = 4
	popupWindow = 2 * time.Second
)

// Operation deadlines. evalDeadline is a variable so a test can shorten it.
const (
	navigateDeadline = 30 * time.Second
	createDeadline   = 45 * time.Second
	inputDeadline    = 5 * time.Second
	shotDeadline     = 10 * time.Second
	// attachDeadline bounds attaching a session to a tab. The attach enables
	// the CSS domain, which Chrome answers only once the page's resources
	// have loaded, so attaching to a page that is loading takes the rest of
	// that load (measured up to 15 s for a one-line page on a busy machine):
	// it gets the bound of a page load.
	attachDeadline = navigateDeadline
)

var evalDeadline = 20 * time.Second

func laneCap(l Lane) int {
	if l == LaneScan {
		return scanTabCap
	}
	return applyTabCap
}

// tab is the server-side record of one page target.
type tab struct {
	id             target.ID
	lane           Lane
	owner          string // run that created it; empty for a user-owned or adopted tab
	opener         target.ID
	window         int64
	created        time.Time
	adopted        bool
	loadEverything bool   // scan-lane resource policy off
	lease          *Lease // the lease that owns the tab; nil for a user's or adopted tab
	reason         atomic.Pointer[string]

	// kept is read under the instance lock and by event handlers while mu is
	// held for a whole attach, so it is its own atomic and takes no lock.
	kept atomic.Bool

	// popupBinding names the scan stubs' binding on the tab's session.
	popupBinding atomic.Pointer[string]

	mu        sync.Mutex
	popupURLs []string
	ctx       context.Context // chromedp session; nil until the tab is attached
	cancel    context.CancelFunc

	hooks   *Hooks     // guarded by mu; at most one per tab
	hooksMu sync.Mutex // serializes creating the hooks, so a lost race leaves no listeners behind

	onClosed []func() // guarded by mu; run once when the tab is closed, kept or not

	observing bool // guarded by mu; at most one observer per kept tab

	networkMu    sync.Mutex
	networkUsers map[context.Context]int

	infoMu sync.Mutex
	shown  time.Time // when ShowWindow last activated the tab

	shotMu sync.Mutex
	shot   cachedShot

	viewMu sync.Mutex             // serializes attaching the viewer session
	view   atomic.Pointer[viewer] // the panel's own session; nil while nobody watches
	driver atomic.Pointer[Lease]  // the lease that last operated on the tab

	slow       atomic.Bool // the last operation timed out
	navigating atomic.Bool
	closed     atomic.Bool
	// settingUp holds from the tab's registration until its session is set
	// up. A loss in between leaves the browser's blank page and no tab of
	// ours, so the record leaves it out and the adopter closes it.
	settingUp atomic.Bool
}

// cachedShot is the last image taken of a tab, kept in memory for a second.
type cachedShot struct {
	key  string
	at   time.Time
	data []byte
}

// TabInfo describes one tab for status output. The URL is reduced to origin
// and path so no query or fragment leaves the package.
type TabInfo struct {
	ID     string
	Lane   Lane
	Owner  string
	Window int64
	Kept   bool
	Opener string
	// Created is when the server first saw the tab.
	Created time.Time
}

// setCloseReason records why the tab is closing; the first reason wins.
func (t *tab) setCloseReason(r string) {
	t.reason.CompareAndSwap(nil, &r)
}

func (t *tab) closeReason() string {
	if r := t.reason.Load(); r != nil {
		return *r
	}
	return "closed-by-page-or-system"
}

func (t *tab) isKept() bool { return t.kept.Load() }

// session returns the chromedp context for the tab, attaching on first use.
func (i *Instance) session(t *tab) (context.Context, error) {
	if i.deadInfo() != nil {
		return nil, newError(CodeBrowserDisconnected, "the browser exited")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed.Load() {
		return nil, newError(CodeTabClosed, "tab is closed")
	}
	if t.ctx != nil {
		return t.ctx, nil
	}
	ctx, cancel := chromedp.NewContext(i.root, chromedp.WithTargetID(t.id), chromedp.WithDetachOnCancel())
	if err := attachWithin(context.Background(), ctx, attachDeadline); err != nil {
		cancel()
		return nil, err
	}
	if err := i.setupTab(ctx, t); err != nil {
		cancel()
		return nil, err
	}
	t.ctx, t.cancel = ctx, cancel
	return ctx, nil
}

// session is the session of a tab an operation of the lane acts on. A tab kept
// for the user takes no command from the lane: only its observer reads it.
func (h *LaneHandle) session(t *tab) (context.Context, error) {
	if t.isKept() {
		return nil, newError(CodeNotAllowed, "the tab is kept for the user")
	}
	return h.in().session(t)
}

// attachWithin attaches the context's target. The session lives as long as
// the context of the first command, so that command must carry the long-lived
// tab context; the wait ends instead at d, or when the caller's context ends.
func attachWithin(caller, ctx context.Context, d time.Duration) error {
	done := make(chan error, 1)
	go func() {
		done <- chromedp.Do(ctx, chromedp.Func(func(context.Context, *chromedp.Target) error { return nil }))
	}()
	select {
	case err := <-done:
		if err != nil {
			return wrapError(CodeTabClosed, err, "cannot attach to the tab")
		}
		return nil
	case <-caller.Done():
		return wrapError(CodeOperationTimeout, caller.Err(), "attaching to the tab timed out")
	case <-time.After(d):
		return newError(CodeOperationTimeout, "attaching to the tab timed out")
	}
}

// runClosed runs the callbacks registered for the tab's close, once.
func (t *tab) runClosed() {
	t.mu.Lock()
	fns := t.onClosed
	t.onClosed = nil
	t.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// detach drops the session without closing the tab.
func (t *tab) detach() {
	t.mu.Lock()
	cancel := t.cancel
	hooks := t.hooks
	t.ctx, t.cancel, t.hooks = nil, nil, nil
	t.mu.Unlock()
	if hooks != nil {
		hooks.release()
	}
	if cancel != nil {
		cancel()
	}
}

// opCtx derives a context that carries the tab's session, ends at the
// deadline and ends early when the caller's context ends.
func opCtx(caller, session context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(session, d)
	stop := context.AfterFunc(caller, cancel)
	return ctx, func() { stop(); cancel() }
}
