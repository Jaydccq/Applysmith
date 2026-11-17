package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Options configures the managed browser instance.
type Options struct {
	Chrome     Chrome
	ProfileDir string
	// StateDir holds the instance record and the download staging directory.
	StateDir string
	// DownloadsDir receives apply-lane downloads.
	DownloadsDir string
	// EventLogPath is the JSON-lines browser event log. Empty keeps events in
	// memory only.
	EventLogPath string
	// Environ is the caller's environment; Chrome sees an allowlisted subset.
	Environ   []string
	hostRules string // hostnames to loopback in Chrome; set by tests only
	headless  bool   // launch with --headless=new; set by tests only

	launchTimeout time.Duration // the launch deadline; set by tests only
	idleAfter     time.Duration // idle stop delay; set by tests only
	idleTick      time.Duration // how often the idle check runs; set by tests only
	sweepEvery    time.Duration // cookie sweep period; set by tests only
	markerEvery   time.Duration // marker cookie refresh period; set by tests only
	now           func() time.Time
}

// Timings of the instance's background work.
const (
	// idleStopAfter is how long the instance may sit with no lease, no tab and
	// no operation in flight before it stops itself.
	idleStopAfter  = 10 * time.Minute
	idleCheckEvery = 30 * time.Second
	// cookieSweepEvery is how often session cookies get an expiry.
	cookieSweepEvery = 60 * time.Second
	// markerEvery is how often the marker cookie is written again.
	markerRefreshEvery = 5 * time.Minute
)

// Instance is one managed Chrome process with two lane windows.
type Instance struct {
	opts Options
	proc *Process

	root       context.Context
	cancelRoot context.CancelFunc
	cancelAll  context.CancelFunc

	// createMu serializes tab creation so a created target's lane label is
	// recorded before its creation event is interpreted.
	createMu sync.Mutex

	mu        sync.Mutex
	tabs      map[target.ID]*tab
	windows   map[int64]Lane // browser window id -> lane
	events    []Event
	eventFile *eventFile
	popups    map[int64][]time.Time
	downloads map[string]*download
	leases    map[*Lease]struct{}
	busy      int // imports, probes, prints and captures in flight
	stopped   bool
	detached  bool      // the connection was dropped on purpose
	dead      *exitInfo // set once when the process or connection is lost
	key       string    // registry key, the canonical profile directory

	idleSince  time.Time // when the instance last became idle; zero while in use
	idleAfter  time.Duration
	idleTick   time.Duration
	sweepEvery time.Duration
	markEvery  time.Duration
	now        func() time.Time
	lastClosed time.Time // when a tab last closed, to tell a quit from a crash
	lostPages  map[string]bool

	ready      atomic.Bool                            // attached and serving
	closeFn    func(context.Context, target.ID) error // test seam
	viewportFn func() (float64, float64, error)
	resizeFn   func(w, h int64) error
	creating   int         // tab creations under way
	abandoned  []time.Time // tab creations given up on, whose tab may still arrive
	stopDone   chan struct{}
	doneOnce   sync.Once

	fitMu    sync.Mutex
	unfitted [3]int64 // the window fit sized and its size before
}

// Open starts, or adopts, the managed Chrome and attaches to it. Windows are
// created on demand by the first operation that needs a lane.
//
// Concurrent calls for one profile share a single launch: the later calls wait
// for the first and receive the same instance.
func Open(ctx context.Context, o Options) (*Instance, error) {
	key := profileKey(o.ProfileDir)
	for {
		registry.mu.Lock()
		c := registry.calls[key]
		if c == nil {
			c = &openCall{done: make(chan struct{})}
			registry.calls[key] = c
			registry.mu.Unlock()
			inst, err := openOnce(ctx, o)
			// The instance's own watchers may already be unregistering it, which
			// reads the call under the registry lock.
			registry.mu.Lock()
			c.inst, c.err = inst, err
			registry.mu.Unlock()
			if c.err != nil {
				registry.forget(key, c)
			}
			close(c.done)
			return c.inst, c.err
		}
		registry.mu.Unlock()
		select {
		case <-c.done:
		case <-ctx.Done():
			return nil, wrapError(CodeLaunchTimeout, ctx.Err(), "waiting for the browser launch")
		}
		if c.err == nil && !c.inst.isStopped() {
			return c.inst, nil
		}
		if c.err == nil {
			// A browser that is stopping still holds the profile; the next
			// launch waits for it to leave.
			select {
			case <-c.inst.stopDone:
			case <-ctx.Done():
				return nil, wrapError(CodeLaunchTimeout, ctx.Err(), "waiting for the browser to stop")
			}
		}
		registry.forget(key, c)
	}
}

// profileKey names a profile directory by where it really is, so two spellings
// of one directory (a link, a relative path) share a launch.
func profileKey(dir string) string {
	if c, err := canonical(dir); err == nil {
		return c
	}
	return filepath.Clean(dir)
}

type openCall struct {
	done chan struct{}
	inst *Instance
	err  error
}

type openRegistry struct {
	mu    sync.Mutex
	calls map[string]*openCall
	// lost holds, per profile, an instance whose connection was lost while
	// its browser still ran, until an open takes the browser back.
	lost map[string]lostBrowser
}

type lostBrowser struct {
	inst *Instance
	kept bool // a tab was kept for the user when the connection was lost
}

var registry = openRegistry{calls: map[string]*openCall{}, lost: map[string]lostBrowser{}}

// lose remembers an instance whose browser outlived its connection.
func (r *openRegistry) lose(i *Instance) {
	if i.key == "" {
		return
	}
	i.mu.Lock()
	kept := false
	for _, t := range i.tabs {
		kept = kept || t.isKept()
	}
	i.mu.Unlock()
	r.mu.Lock()
	r.lost[i.key] = lostBrowser{inst: i, kept: kept}
	r.mu.Unlock()
}

// found forgets a lost browser once an open has adopted or replaced it.
func (r *openRegistry) found(key string) {
	r.mu.Lock()
	delete(r.lost, key)
	r.mu.Unlock()
}

// stopLost stops the browser this process lost the connection to and has not
// opened again, as a server stop would have stopped it. One that held a tab
// kept for the user is left with its record for the next process to adopt.
func stopLost(ctx context.Context, profileDir string) StopOutcome {
	key := profileKey(profileDir)
	registry.mu.Lock()
	l, ok := registry.lost[key]
	delete(registry.lost, key)
	registry.mu.Unlock()
	if !ok || l.kept {
		return ""
	}
	p := l.inst.proc
	out := retire(ctx, Record{PID: p.PID, Start: p.Start})
	if r, ok := ReadRecord(l.inst.recordPath()); ok && r.PID == p.PID && out != StopDetached {
		_ = os.Remove(l.inst.recordPath())
	}
	l.inst.logEvent(EventBrowserExited, "", "lost browser stopped by the server: "+string(out))
	return out
}

func (r *openRegistry) forget(key string, c *openCall) {
	r.mu.Lock()
	if r.calls[key] == c {
		delete(r.calls, key)
	}
	r.mu.Unlock()
}

func (i *Instance) isStopped() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.stopped || i.dead != nil
}

// unregister lets the next Open for this profile start or adopt afresh.
func (i *Instance) unregister() {
	if i.key == "" {
		return
	}
	registry.mu.Lock()
	if c := registry.calls[i.key]; c != nil && c.inst == i {
		delete(registry.calls, i.key)
	}
	registry.mu.Unlock()
}

// attachBudget bounds attaching to a running browser: the connection and the
// first command. A variable so a test can shorten it.
var attachBudget = 15 * time.Second

// firstUseBudget bounds a launch together with the creation of the first
// window.
var firstUseBudget = 30 * time.Second

func openOnce(ctx context.Context, o Options) (*Instance, error) {
	if o.StateDir == "" {
		return nil, newError(CodeProfileUnsafe, "no state directory configured")
	}
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return nil, wrapError(CodeProfileUnsafe, err, "cannot create state directory")
	}
	var notes []Event
	proc, err := startOrAdopt(ctx, o, func(kind, detail string) {
		notes = append(notes, Event{Kind: kind, Detail: detail})
	})
	if err != nil {
		return nil, err
	}
	inst := &Instance{
		opts: o,
		proc: proc,
		// The key is set before any watcher starts: a browser lost in the first
		// moments is unregistered and remembered under it.
		key:        profileKey(o.ProfileDir),
		tabs:       map[target.ID]*tab{},
		windows:    map[int64]Lane{},
		popups:     map[int64][]time.Time{},
		downloads:  map[string]*download{},
		leases:     map[*Lease]struct{}{},
		eventFile:  newEventFile(o.EventLogPath),
		stopDone:   make(chan struct{}),
		idleAfter:  durationOr(o.idleAfter, idleStopAfter),
		idleTick:   durationOr(o.idleTick, idleCheckEvery),
		sweepEvery: durationOr(o.sweepEvery, cookieSweepEvery),
		markEvery:  durationOr(o.markerEvery, markerRefreshEvery),
		now:        o.now,
		lostPages:  map[string]bool{},
	}
	if inst.now == nil {
		inst.now = time.Now
	}
	inst.idleSince = inst.now()
	allocCtx, cancelAlloc := remote.NewAllocator(context.Background(), proc.wsURL, remote.NoModifyURL, remote.WithDialTimeout(attachBudget))
	root, cancelRoot := chromedp.NewContext(allocCtx)
	inst.root, inst.cancelRoot = root, cancelRoot
	inst.cancelAll = cancelAlloc
	if err := inst.attach(ctx); err != nil {
		inst.Stop(ctx)
		return nil, err
	}
	for _, n := range notes {
		inst.logEvent(n.Kind, "", n.Detail)
	}
	inst.ready.Store(true)
	// The marker is written at every launch and adoption.
	mctx, mcancel := context.WithTimeout(inst.root, cookieDeadline)
	_ = inst.writeMarker(mctx)
	mcancel()
	// This open adopted or replaced whatever browser was lost before; a loss
	// of this one, which the watchers below may report at once, is kept.
	registry.found(inst.key)
	go inst.closeTabsWhenGone(proc.done)
	go inst.watchIdle()
	go inst.watchCookies()
	return inst, nil
}

func durationOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (i *Instance) recordPath() string { return filepath.Join(i.opts.StateDir, "instance.json") }

// startOrAdopt starts or adopts the browser. note, when set, receives events
// that happened before an instance exists to hold them.
func startOrAdopt(ctx context.Context, o Options, note func(kind, detail string)) (*Process, error) {
	if note == nil {
		note = func(string, string) {}
	}
	rec := filepath.Join(o.StateDir, "instance.json")
	home, _ := os.UserHomeDir()
	profile, err := prepareProfile(o.ProfileDir, home, os.Getenv)
	if err != nil {
		return nil, err
	}
	r, ok := ReadRecord(rec)
	how := ""
	if !ok {
		r, ok = holderRecord(ctx, profile)
		how = ", found by the profile lock"
	}
	if ok {
		p, ours, err := adopt(ctx, r, o.Chrome.Major, profile)
		switch {
		case err == nil:
			note(EventBrowserAdopted, fmt.Sprintf("Chrome %s, pid %d%s", p.Version, p.PID, how))
			return p, nil
		case transient(err):
			// The browser may be fine and only slow to answer or to be inspected;
			// forgetting it would leave its profile locked with no way back.
			return nil, err
		case ours:
			// A browser that is verifiably ours but cannot be used (another
			// major version, an endpoint that is not its own) is stopped so the
			// profile is free for a fresh launch. If it will not leave, the
			// record is kept: it is the only trace of a browser that still
			// holds the profile.
			out := retire(ctx, r)
			note("browser_retired", fmt.Sprintf("pid %d: %v; stop outcome %s", r.PID, err, out))
			if out == StopDetached {
				return nil, wrapError(CodeProfileInUse, err, "the recorded browser (pid %d) did not quit", r.PID)
			}
		}
		// A record for another profile, or for a process that is gone or is
		// not the one recorded, is ignored and removed.
		_ = os.Remove(rec)
	}
	p, err := Launch(ctx, LaunchOptions{Chrome: o.Chrome, ProfileDir: o.ProfileDir, Environ: o.Environ, hostRules: o.hostRules, headless: o.headless, timeout: o.launchTimeout})
	if err == nil {
		note(EventBrowserLaunched, fmt.Sprintf("Chrome %s, pid %d", p.Version, p.PID))
	}
	return p, err
}

// holderRecord describes the live process named by the profile's SingletonLock,
// with the debugging port its command line passes right after this profile, so
// adoption verifies it like a record. Chrome writes no DevToolsActivePort file
// for an explicit port, so the command line is the only source of the port.
func holderRecord(ctx context.Context, profile string) (Record, bool) {
	pid := lockHolder(profile)
	if pid <= 0 || !processAlive(pid) {
		return Record{}, false
	}
	cmdline, err := commandOf(ctx, pid)
	if err != nil {
		return Record{}, false
	}
	_, rest, found := strings.Cut(cmdline+" ", " --user-data-dir="+profile+" --remote-debugging-port=")
	port, _ := strconv.Atoi(strings.SplitN(rest, " ", 2)[0])
	st, err := startOf(ctx, pid)
	if !found || port <= 0 || err != nil {
		return Record{}, false
	}
	return Record{PID: pid, Start: st, Profile: profile, Port: port}, true
}

// attach connects to the browser and installs the instance-wide policies:
// permission denial, download staging, target discovery and the event loops.
func (i *Instance) attach(ctx context.Context) error {
	if err := i.connect(ctx); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(i.root, attachBudget)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if _, err := chromedp.CallBrowser(cctx, target.SetDiscoverTargets, target.SetDiscoverTargetsParams{Discover: true}); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot attach to the browser")
	}
	i.denyPermissions(cctx)
	if err := i.setupDownloads(cctx); err != nil {
		return err
	}
	i.watchTargets()
	i.watchDownloads()
	i.adoptExisting(cctx)
	return nil
}

// connect dials the browser. The connection lives as long as the context of
// its first command, so that command must carry the long-lived root context;
// the wait is bounded here instead.
func (i *Instance) connect(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		_, err := chromedp.Targets(i.root)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return wrapError(CodeBrowserDisconnected, err, "cannot attach to the browser")
		}
		return nil
	case <-time.After(attachBudget):
	case <-ctx.Done():
	}
	i.cancelRoot()
	return newError(CodeBrowserDisconnected, "attaching to the browser timed out")
}

// writeRecord saves the instance record once both lanes have a window; before
// that the browser holds no state worth adopting.
func (i *Instance) writeRecord() {
	if !i.hasWindow(LaneApply) || !i.hasWindow(LaneScan) {
		return
	}
	i.saveRecord(false)
}

// saveRecord writes the instance record with the windows the lanes hold.
// abandoned says the connection was lost while a tab creation was under way or
// left unanswered: the record then says so, with the pages this instance knew,
// so that the adopter closes the blank tab the browser made for such a
// creation (Record.Abandoned). A tab whose session setup the loss cut off is
// such a blank tab, and is left out (flow round 9, E-2).
func (i *Instance) saveRecord(abandoned bool) {
	i.mu.Lock()
	r := Record{PID: i.proc.PID, Start: i.proc.Start, Profile: i.proc.Profile, Port: i.proc.Port, ChromeVersion: i.proc.Version}
	for w, l := range i.windows {
		if l == LaneScan {
			r.ScanWindows = append(r.ScanWindows, int(w))
		} else {
			r.ApplyWindows = append(r.ApplyWindows, int(w))
		}
	}
	if abandoned {
		r.Abandoned = true
		for id, t := range i.tabs {
			if !t.settingUp.Load() {
				r.Tabs = append(r.Tabs, string(id))
			}
		}
	}
	i.mu.Unlock()
	_ = WriteRecord(i.recordPath(), r)
}

// Version returns the Chrome version the instance runs.
func (i *Instance) Version() string { return i.proc.Version }

// Stop asks Chrome to quit, which closes every tab, waits for it to exit,
// escalates to a kill after 10 s (at once when ctx ends first), removes the
// instance record and releases every handle. Session cookies are swept first.
func (i *Instance) Stop(ctx context.Context) StopOutcome {
	out, _ := i.stopWith(ctx, nil, "")
	return out
}

// stopWith stops the instance unless cond, evaluated under the instance lock
// together with the claim on stopping, says it must keep running. The second
// result reports whether the instance was stopped by this call.
func (i *Instance) stopWith(ctx context.Context, cond func() bool, why string) (StopOutcome, bool) {
	i.mu.Lock()
	if i.stopped {
		i.mu.Unlock()
		return StopClosed, false
	}
	if cond != nil && !cond() {
		i.mu.Unlock()
		return "", false
	}
	i.stopped = true
	i.mu.Unlock()
	defer i.markGone()
	if why != "" {
		i.logEvent(EventIdleStop, "", why)
	}
	// Sweeping needs the connection, so it runs before anything is closed.
	i.unfit()
	i.sweepBeforeStop(ctx)
	out := i.proc.stop(ctx, i.askToClose)
	i.logEvent(EventBrowserExited, "", "stopped by the server: "+string(out))
	i.cancelRoot()
	i.cancelAll()
	i.closeAllTabs("browser-stopped")
	if r, ok := ReadRecord(i.recordPath()); ok && r.PID == i.proc.PID {
		_ = os.Remove(i.recordPath())
	}
	i.unregister()
	return out, true
}

// askToClose asks the browser to close. Browser.close is the graceful path: it
// closes the windows and commits the cookie store, which a termination signal
// does not reliably do. A browser that has not left after a few seconds, or
// that cannot be asked, gets the signal; the pid is re-verified first so a
// recycled pid is never signalled.
func (i *Instance) askToClose() {
	if i.root != nil && i.root.Err() == nil {
		// chromedp.Cancel on the first context sends Browser.close and waits.
		c, cancel := context.WithTimeout(i.root, closeSignalAfter)
		_ = chromedp.Cancel(c)
		cancel()
	}
	select {
	case <-i.proc.done:
		return
	case <-time.After(200 * time.Millisecond):
	}
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if st, err := processStart(c, i.proc.PID); err == nil && st == i.proc.Start {
		askToQuit(i.proc.PID)
	}
}

// closeSignalAfter is how long a browser asked to close may take before it is
// sent the termination signal as well. A variable so tests can shorten it.
var closeSignalAfter = 3 * time.Second

// markGone tells whoever waits for the browser to leave that it has.
func (i *Instance) markGone() {
	i.doneOnce.Do(func() {
		if i.stopDone != nil {
			close(i.stopDone)
		}
	})
}

// closeAllTabs marks every remaining tab closed once the browser is gone, so
// whatever was staged for a tab is released even when no close event arrives.
func (i *Instance) closeAllTabs(reason string) {
	i.mu.Lock()
	ids := make([]target.ID, 0, len(i.tabs))
	for id, t := range i.tabs {
		ids = append(ids, id)
		t.setCloseReason(reason)
		if reason == "browser-exited" && t.lease != nil {
			t.lease.markLost(string(id))
		}
	}
	i.mu.Unlock()
	for _, id := range ids {
		i.markClosed(id)
	}
}

// PID is the browser process id.
func (i *Instance) PID() int { return i.proc.PID }

// ManagedPID is the process id of the managed browser this process has open
// for the profile, or 0 when it has none open: a Chrome this process did not
// start or adopt is never the managed one.
func ManagedPID(profileDir string) int {
	if i := running(profileDir); i != nil {
		return i.PID()
	}
	return 0
}
