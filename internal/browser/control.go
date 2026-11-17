package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// CodeLaneLeased reports a stop while a run holds a tab.
const CodeLaneLeased Code = "lane-leased"

// CodeScreenshotUnavailable reports a tab the browser could not render.
const CodeScreenshotUnavailable Code = "screenshot-unavailable"

// InstanceState is the state of the managed browser as the dashboard shows it.
type InstanceState string

// Instance states.
const (
	InstanceStopped  InstanceState = "stopped"
	InstanceStarting InstanceState = "starting"
	InstanceRunning  InstanceState = "running"
	InstanceStopping InstanceState = "stopping"
	InstanceFailed   InstanceState = "failed"
)

const (
	statusEvents   = 100
	statusProbe    = time.Second
	shotCacheAge   = time.Second
	shotQuality    = 70
	shotDefaultW   = 1024
	shotMaxW       = 1600
	shotMinW       = 64
	maxStatusTabs  = 64
	statusInfoWait = 2 * time.Second
)

// TabStatus describes one tab. URL holds origin and path only. Active is the
// window's visible tab when the page could be asked, otherwise the tab shown
// last, otherwise the newest.
type TabStatus struct {
	ID      string
	Title   string
	URL     string
	Loading bool
	Active  bool
	Created time.Time
	Owner   string
	Kept    bool
}

// LaneStatus is one lane's window and tabs.
type LaneStatus struct {
	Window bool
	Cap    int
	Tabs   []TabStatus
}

// Status is the state of the managed browser. Events are the newest first.
type Status struct {
	State   InstanceState
	Version string
	Error   string
	Apply   LaneStatus
	Scan    LaneStatus
	Events  []Event
}

// ShotOptions selects the image format and width. Width 0 is the default.
type ShotOptions struct {
	PNG   bool
	Width int
}

// Shot is an image of a tab. It lives in memory only.
type Shot struct {
	Data        []byte
	ContentType string
}

// Control serves the dashboard's view of the managed browser and its window
// controls. It starts nothing for a status or a screenshot of a browser that
// is not running; show starts it.
type Control struct {
	opts Options

	mu       sync.Mutex
	chrome   bool // opts.Chrome is discovered
	starting int
	stopping int
	failure  string
}

// NewControl returns the control of the browser that o describes. An empty
// o.Chrome is discovered on the first start.
func NewControl(o Options) *Control { return &Control{opts: o, chrome: o.Chrome.Path != ""} }

func (c *Control) running() *Instance { return running(c.opts.ProfileDir) }

func (c *Control) open(ctx context.Context) (*Instance, error) {
	c.mu.Lock()
	c.starting++
	c.mu.Unlock()
	inst, err := c.openOnce(ctx)
	c.mu.Lock()
	c.starting--
	if err != nil {
		c.failure = err.Error()
	} else {
		c.failure = ""
	}
	c.mu.Unlock()
	return inst, err
}

func (c *Control) openOnce(ctx context.Context) (*Instance, error) {
	c.mu.Lock()
	known := c.chrome
	c.mu.Unlock()
	if !known {
		// Discovery runs Chrome for its version, for up to ten seconds. The
		// lock is not held meanwhile: the status reads the state under it.
		ch, err := DiscoverChrome(ctx, os.Getenv)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.opts.Chrome, c.chrome = ch, true
		c.mu.Unlock()
	}
	c.mu.Lock()
	opts := c.opts
	c.mu.Unlock()
	return Open(ctx, opts)
}

func (c *Control) state(inst *Instance) (InstanceState, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.stopping > 0:
		return InstanceStopping, ""
	case inst != nil:
		return InstanceRunning, ""
	case c.starting > 0 || openPending(c.opts.ProfileDir):
		return InstanceStarting, ""
	case c.failure != "":
		return InstanceFailed, c.failure
	}
	return InstanceStopped, ""
}

// State is the browser's state and failure, without asking Chrome.
func (c *Control) State() (InstanceState, string) { return c.state(c.running()) }

// Survives reports whether a browser may still hold the tabs of an earlier
// process: one this process runs or is opening, or one that the instance record
// or the profile's lock names a live process for, which the next lane use
// adopts. A graceful stop closes the browser and removes its record, so then
// none survives. It starts nothing and asks the browser nothing.
func (c *Control) Survives() bool {
	if c.running() != nil || openPending(c.opts.ProfileDir) {
		return true
	}
	if r, ok := ReadRecord(filepath.Join(c.opts.StateDir, "instance.json")); ok && processAlive(r.PID) {
		return true
	}
	pid := lockHolder(c.opts.ProfileDir)
	return pid > 0 && processAlive(pid)
}

// Status describes the browser without starting it.
func (c *Control) Status(ctx context.Context) Status {
	inst := c.running()
	st := Status{Apply: LaneStatus{Cap: laneCap(LaneApply)}, Scan: LaneStatus{Cap: laneCap(LaneScan)}}
	st.State, st.Error = c.state(inst)
	if inst == nil {
		return st
	}
	st.Version = inst.Version()
	inst.fillStatus(ctx, &st)
	return st
}

// tabSnapshot is what Status reads from a tab under the instance lock.
type tabSnapshot struct {
	t      *tab
	lane   Lane
	window int64
}

func (i *Instance) fillStatus(ctx context.Context, st *Status) {
	i.mu.Lock()
	var snaps []tabSnapshot
	for _, t := range i.tabs {
		if t.closed.Load() {
			continue
		}
		snaps = append(snaps, tabSnapshot{t: t, lane: t.lane, window: t.window})
	}
	for _, l := range i.windows {
		if l == LaneApply {
			st.Apply.Window = true
		} else {
			st.Scan.Window = true
		}
	}
	i.mu.Unlock()
	sort.Slice(snaps, func(a, b int) bool { return snaps[a].t.created.Before(snaps[b].t.created) })
	if len(snaps) > maxStatusTabs {
		snaps = snaps[:maxStatusTabs]
	}

	infos := i.pageInfos(ctx)
	visible := i.probeTabs(ctx, snaps)
	byWindow := map[int64][]int{}
	rows := make([]TabStatus, len(snaps))
	for n, s := range snaps {
		in := infos[s.t.id]
		rows[n] = TabStatus{ID: string(s.t.id), Title: truncate(in.title, 200), URL: reduceURL(in.url), Created: s.t.created, Owner: s.t.owner, Kept: s.t.isKept()}
		if p, ok := visible[s.t.id]; ok {
			rows[n].Loading = p.loading
		}
		byWindow[s.window] = append(byWindow[s.window], n)
	}
	for _, idx := range byWindow {
		active := -1
		for _, n := range idx {
			if visible[snaps[n].t.id].visible {
				active = n
				break
			}
		}
		if active < 0 {
			var latest time.Time
			for _, n := range idx {
				if sh := snaps[n].t.shownAt(); sh.After(latest) {
					latest, active = sh, n
				}
			}
		}
		if active < 0 {
			active = idx[len(idx)-1]
		}
		rows[active].Active = true
	}
	for n, s := range snaps {
		if s.lane == LaneScan {
			st.Scan.Tabs = append(st.Scan.Tabs, rows[n])
		} else {
			st.Apply.Tabs = append(st.Apply.Tabs, rows[n])
		}
	}
	ev := i.Events()
	for n := len(ev) - 1; n >= 0 && len(st.Events) < statusEvents; n-- {
		st.Events = append(st.Events, ev[n])
	}
}

type pageInfo struct{ title, url string }

// pageInfos reads the title and address of every page in one call. A browser
// that does not answer leaves them empty.
func (i *Instance) pageInfos(ctx context.Context) map[target.ID]pageInfo {
	bc, cancel := i.bctx(ctx)
	defer cancel()
	bc, cancel2 := context.WithTimeout(bc, statusInfoWait)
	defer cancel2()
	out := map[target.ID]pageInfo{}
	infos, err := chromedp.Targets(bc)
	if err != nil {
		return out
	}
	for _, in := range infos {
		if in.Type == "page" {
			out[in.TargetID] = pageInfo{title: in.Title, url: in.URL}
		}
	}
	return out
}

type probe struct{ visible, loading bool }

// probeTabs asks each tab that already has a session whether it is visible and
// still loading. A tab kept for the user has no session and is never asked.
func (i *Instance) probeTabs(ctx context.Context, snaps []tabSnapshot) map[target.ID]probe {
	out := map[target.ID]probe{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range snaps {
		s.t.mu.Lock()
		sess := s.t.ctx
		s.t.mu.Unlock()
		if sess == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			octx, cancel := opCtx(ctx, sess, statusProbe)
			defer cancel()
			type state struct {
				Visible string `json:"v"`
				Ready   string `json:"r"`
			}
			got, err := chromedp.Run(octx, chromedp.Evaluate[state](`({v: document.visibilityState, r: document.readyState})`))
			if err != nil {
				return
			}
			mu.Lock()
			out[s.t.id] = probe{visible: got.Visible == "visible", loading: got.Ready != "complete"}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func (t *tab) shownAt() time.Time {
	t.infoMu.Lock()
	defer t.infoMu.Unlock()
	return t.shown
}

func (t *tab) markShown() {
	t.infoMu.Lock()
	t.shown = time.Now()
	t.infoMu.Unlock()
}

// Capture returns an image of a tab, at most 1600 pixels wide. A tab captured
// less than a second ago with the same options is served from memory.
func (c *Control) Capture(ctx context.Context, lane Lane, tabID string, o ShotOptions) (Shot, error) {
	inst := c.running()
	if inst == nil {
		return Shot{}, newError(CodeTabClosed, "no such tab")
	}
	h := inst.Lane(lane)
	t, err := h.tab(tabID)
	if err != nil {
		return Shot{}, err
	}
	width := o.Width
	if width == 0 {
		width = shotDefaultW
	}
	width = min(max(width, shotMinW), shotMaxW)
	key := shotKey(o.PNG, width)

	t.shotMu.Lock()
	defer t.shotMu.Unlock()
	if t.shot.key == key && time.Since(t.shot.at) < shotCacheAge && len(t.shot.data) > 0 {
		return Shot{Data: t.shot.data, ContentType: shotType(o.PNG)}, nil
	}
	defer inst.Begin()()
	data, err := inst.capture(ctx, t, o.PNG, width)
	if err != nil {
		return Shot{}, err
	}
	t.shot.key, t.shot.at, t.shot.data = key, time.Now(), data
	return Shot{Data: data, ContentType: shotType(o.PNG)}, nil
}

func shotKey(png bool, width int) string { return fmt.Sprintf("%t/%d", png, width) }

func shotType(png bool) string {
	if png {
		return "image/png"
	}
	return "image/jpeg"
}

// capture renders the tab's viewport. A tab kept for the user takes no lane
// command and no session that enables a domain, so it is read through a quiet
// session (attachQuiet) that lasts only for the capture.
func (i *Instance) capture(ctx context.Context, t *tab, png bool, width int) ([]byte, error) {
	var quiet *Observer
	var octx context.Context
	var cancel context.CancelFunc
	if t.isKept() {
		o, err := i.Lane(t.lane).attachQuiet(ctx, t, ObserverScripts{})
		if err != nil {
			return nil, newError(CodeScreenshotUnavailable, "the tab could not be read")
		}
		defer o.detach()
		quiet = o
		octx, cancel = context.WithTimeout(ctx, shotDeadline)
	} else {
		s, err := i.session(t)
		if err != nil {
			return nil, newError(CodeScreenshotUnavailable, "the tab could not be read")
		}
		octx, cancel = opCtx(ctx, s, shotDeadline)
	}
	defer cancel()
	m, err := shotCall(octx, quiet, page.GetLayoutMetrics, cdp.Empty{})
	if err != nil || m.CSSVisualViewport == nil || m.CSSVisualViewport.ClientWidth <= 0 || m.CSSVisualViewport.ClientHeight <= 0 {
		return nil, newError(CodeScreenshotUnavailable, "the browser cannot render this tab")
	}
	vw, vh := m.CSSVisualViewport.ClientWidth, m.CSSVisualViewport.ClientHeight
	scale := min(1.0, float64(width)/vw)
	p := page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatJpeg, Quality: ptr(int64(shotQuality)),
		Clip: &page.Viewport{Width: vw, Height: vh, Scale: scale}}
	if png {
		p.Format, p.Quality = page.CaptureScreenshotFormatPng, nil
	}
	r, err := shotCall(octx, quiet, page.CaptureScreenshot, p)
	if err != nil || len(r.Data) == 0 {
		return nil, newError(CodeScreenshotUnavailable, "the browser cannot render this tab")
	}
	return r.Data, nil
}

// shotCall runs one command of a capture: on the quiet session of a kept tab,
// else on the lane session ctx carries.
func shotCall[P, R any](ctx context.Context, quiet *Observer, cmd cdp.Command[P, R], params P) (R, error) {
	if quiet == nil {
		return chromedp.Call(ctx, cmd, params)
	}
	var res R
	err := quiet.do(ctx, "a screenshot", cmd.Method, params, &res)
	return res, err
}

// Show starts the browser when it is not running, brings the apply window to
// the front and makes a tab its active tab: the one named, else the one shown
// last, else the newest. With no apply tab it opens a blank one for the user.
func (c *Control) Show(ctx context.Context, tabID string) error {
	ctx, cancel := context.WithTimeout(ctx, firstUseBudget)
	defer cancel()
	inst, err := c.open(ctx)
	if err != nil {
		return err
	}
	h := inst.Lane(LaneApply)
	if tabID == "" {
		tabID = inst.pickApplyTab()
	}
	if tabID == "" {
		if tabID, err = h.OpenTab(ctx, ""); err != nil {
			return err
		}
		if err := h.Keep(tabID); err != nil {
			return err
		}
	}
	return h.ShowWindow(ctx, tabID)
}

func (i *Instance) pickApplyTab() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	var best *tab
	for _, t := range i.tabs {
		if t.lane != LaneApply || t.closed.Load() {
			continue
		}
		switch {
		case best == nil:
			best = t
		case t.shownAt().After(best.shownAt()):
			best = t
		case t.shownAt().Equal(best.shownAt()) && t.created.After(best.created):
			best = t
		}
	}
	if best == nil {
		return ""
	}
	return string(best.id)
}

// Stop stops the browser gracefully. It refuses with lane-leased while a run,
// a list or a scan holds a lane unless force is set. A browser that is not
// running is not an error: the outcome is empty. One whose connection this
// process lost is stopped too (stopLost).
func (c *Control) Stop(ctx context.Context, force bool) (StopOutcome, error) {
	inst := c.running()
	if inst == nil {
		return stopLost(ctx, c.opts.ProfileDir), nil
	}
	if !force && inst.Leased() {
		return "", newError(CodeLaneLeased, "a run holds a lane; stop it from the run or force the stop")
	}
	c.mu.Lock()
	c.stopping++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.stopping--
		c.mu.Unlock()
	}()
	return inst.Stop(ctx), nil
}

// unminimize restores the window of a tab when it is minimized.
func (i *Instance) unminimize(ctx context.Context, t *tab) {
	bc, cancel := i.bctx(ctx)
	defer cancel()
	bc, cancel2 := context.WithTimeout(bc, inputDeadline)
	defer cancel2()
	r, err := chromedp.CallBrowser(bc, browser.GetWindowBounds, browser.GetWindowBoundsParams{WindowID: browser.WindowID(t.window)})
	if err != nil || r.Bounds.WindowState != browser.WindowStateMinimized {
		return
	}
	_, _ = chromedp.CallBrowser(bc, browser.SetWindowBounds, browser.SetWindowBoundsParams{
		WindowID: browser.WindowID(t.window), Bounds: &browser.Bounds{WindowState: browser.WindowStateNormal}})
}
