package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// orphanGrace is how long an adopting server waits before closing scan tabs
// that no live run owns.
const orphanGrace = 5 * time.Second

func watchBrowser[E any](ctx context.Context, ev cdp.Event[E], fn func(E)) {
	events := chromedp.BrowserEvents(ctx, ev)
	go func() {
		for e, err := range events {
			if err != nil {
				return
			}
			fn(e)
		}
	}()
}

func (i *Instance) watchTargets() {
	watchBrowser(i.root, target.TargetCreated, func(ev target.EventTargetCreated) {
		if ev.TargetInfo.Type == "page" {
			go i.onPageCreated(ev.TargetInfo)
		}
	})
	watchBrowser(i.root, target.TargetDestroyed, func(ev target.EventTargetDestroyed) {
		i.markClosed(ev.TargetID)
	})
	watchBrowser(i.root, target.TargetCrashed, func(ev target.EventTargetCrashed) {
		i.onCrashed(ev.TargetID)
	})
	watchBrowser(i.root, target.TargetInfoChanged, func(ev target.EventTargetInfoChanged) {
		if ev.TargetInfo.Type == "page" {
			go i.onPageChanged(ev.TargetInfo)
		}
	})
}

func (i *Instance) lookup(id target.ID) *tab {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.tabs[id]
}

// onPageCreated labels a page the instance did not create itself: a popup
// takes its opener's lane and run (inherit), any other page takes the lane of
// the window that holds it, and a window the instance did not create is the
// apply lane and user-owned.
func (i *Instance) onPageCreated(info *target.Info) {
	// A tab being created by this instance is registered before createMu is
	// released, so waiting here separates our tabs from foreign ones.
	i.createMu.Lock()
	i.createMu.Unlock() //nolint:staticcheck // empty critical section is the barrier
	if i.lookup(info.TargetID) != nil {
		return
	}
	// A tab whose creation was given up on arrives blank, with no opener and no
	// owner. Nothing a user opens looks like that: their new tab is the new-tab
	// page.
	if info.OpenerID == "" && info.URL == "about:blank" && i.takeAbandoned() {
		i.closeTarget(info.TargetID, "late-creation")
		i.logEvent(EventTabClosed, "", "late-creation")
		return
	}
	ctx, cancel := context.WithTimeout(i.root, 3*time.Second)
	defer cancel()
	win, err := i.windowOf(ctx, info.TargetID)
	if err != nil {
		// A page that cannot be placed is not listed. One a run's tab opened
		// is closed, so the run leaves no page behind that nothing counts.
		if op := i.lookup(info.OpenerID); op != nil && op.owner != "" && !op.isKept() {
			i.logEvent(EventPopupClosed, op.lane, "window-unknown")
			i.closeTarget(info.TargetID, "window-unknown")
		}
		return
	}
	t := &tab{id: info.TargetID, lane: LaneApply, window: win, opener: info.OpenerID, created: time.Now()}
	how := "user"
	if op := i.lookup(info.OpenerID); op != nil {
		how = t.inherit(op)
	} else {
		i.mu.Lock()
		if l, ok := i.windows[win]; ok {
			t.lane = l
		}
		i.mu.Unlock()
	}
	if info.OpenerID != "" && !t.isKept() {
		if reason := i.popupVerdict(t, info.URL); reason != "" {
			i.logEvent(EventPopupClosed, t.lane, reason)
			i.closeTarget(info.TargetID, reason)
			return
		}
	}
	i.mu.Lock()
	if _, dup := i.tabs[t.id]; dup {
		i.mu.Unlock()
		return
	}
	i.tabs[t.id] = t
	i.touchLocked()
	i.mu.Unlock()
	i.logTab(EventTabOpened, t, how)
	if t.owner != "" {
		if _, err := i.session(t); err != nil {
			i.logEvent("popup_attach_failed", t.lane, "")
		}
	}
}

// inherit labels a page by the tab that opened it and says how it came about.
// A run's popup takes the opener's lane, run and lease. A window opened from a
// tab kept for the user (a sign-in popup) is the user's own: it stays in the
// lane, kept like its opener, with no run and no lease on it.
func (t *tab) inherit(op *tab) string {
	t.lane = op.lane
	if op.isKept() {
		t.kept.Store(true)
		return "user"
	}
	t.owner, t.lease = op.owner, op.lease
	return "popup"
}

// beginCreation and endCreation count the tab creations under way.
func (i *Instance) beginCreation() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.creating++
}

func (i *Instance) endCreation() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.creating--
}

// noteAbandoned remembers a tab creation that was given up on while the browser
// may still make the tab.
func (i *Instance) noteAbandoned() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.abandoned = append(i.abandoned, i.clock())
}

// abandonedFor is how long after being given up on a creation may still arrive.
const abandonedFor = 2 * createDeadline

// liveAbandoned forgets the abandoned creations whose tab can no longer arrive
// and returns the others. i.mu must be held.
func (i *Instance) liveAbandoned() []time.Time {
	now := i.clock()
	live := i.abandoned[:0]
	for _, at := range i.abandoned {
		if now.Sub(at) < abandonedFor {
			live = append(live, at)
		}
	}
	i.abandoned = live
	return live
}

// takeAbandoned claims one abandoned creation for a blank tab that arrived.
func (i *Instance) takeAbandoned() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	live := i.liveAbandoned()
	if len(live) == 0 {
		return false
	}
	i.abandoned = live[1:]
	return true
}

// popupVerdict returns the reason a popup must be closed, or "".
func (i *Instance) popupVerdict(t *tab, rawURL string) string {
	if !schemeAllowed(rawURL) {
		return "not-web-url"
	}
	if i.laneCount(t.lane) >= laneCap(t.lane) {
		return "lane-tab-limit"
	}
	now := time.Now()
	i.mu.Lock()
	defer i.mu.Unlock()
	kept := i.popups[t.window][:0]
	for _, at := range i.popups[t.window] {
		if now.Sub(at) < popupWindow {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	i.popups[t.window] = kept
	if len(kept) > popupBudget {
		return "popup-budget"
	}
	return ""
}

// onPageChanged applies the scheme allowlist to the pages a run drives: a
// popup whose URL turns out to be outside it is closed, and any other such tab
// is sent back to about:blank. A tab kept for the user or opened by the user
// is theirs: its new-tab or settings page gets no session and no navigation.
func (i *Instance) onPageChanged(info *target.Info) {
	t := i.lookup(info.TargetID)
	if t == nil || schemeAllowed(info.URL) {
		return
	}
	if driven, _ := t.drivenBy(); !driven {
		return
	}
	if t.opener != "" && t.owner != "" {
		i.logEvent(EventPopupClosed, t.lane, "not-web-url")
		i.closeTarget(t.id, "not-web-url")
		return
	}
	i.logTab(EventNavigationBlocked, t, blockedURL(info.URL))
	ctx, cancel := context.WithTimeout(i.root, 2*time.Second)
	defer cancel()
	if sess, err := i.session(t); err == nil {
		_ = chromedp.Do(sessionWith(sess, ctx), chromedp.Navigate("about:blank"))
	}
}

// sessionWith carries sess's chromedp context under ctx's deadline.
func sessionWith(sess, ctx context.Context) context.Context {
	c, cancel := context.WithCancel(sess)
	context.AfterFunc(ctx, cancel)
	return c
}

// closeTarget closes a page on the instance's own account. The page leaves
// the listing at once; a close the browser did not perform is tried again in
// the background, as dropTab does.
func (i *Instance) closeTarget(id target.ID, reason string) {
	if t := i.lookup(id); t != nil {
		t.setCloseReason(reason)
	}
	ctx, cancel := context.WithTimeout(i.root, closeBound)
	err := i.closeCall(ctx, id)
	cancel()
	i.markClosed(id)
	if err != nil && i.deadInfo() == nil && !i.isStopped() {
		go i.retryClose(id, closeBound)
	}
}

func (i *Instance) onCrashed(id target.ID) {
	t := i.lookup(id)
	if t == nil {
		return
	}
	i.logTab(EventTabCrashed, t, "renderer-crashed")
	i.closeTarget(id, "tab-crashed")
}

// permissionNames are the permission descriptor names the installed Chrome
// accepts for Browser.setPermission; the protocol's own enumeration lists
// names that command rejects.
var permissionNames = []string{
	"geolocation", "notifications", "midi", "camera", "microphone", "background-fetch", "background-sync",
	"persistent-storage", "ambient-light-sensor", "accelerometer", "gyroscope", "magnetometer", "screen-wake-lock",
	"nfc", "display-capture", "idle-detection", "periodic-background-sync", "system-wake-lock", "storage-access",
	"window-management", "local-fonts", "top-level-storage-access", "captured-surface-control", "speaker-selection",
	"keyboard-lock", "pointer-lock", "local-network-access", "clipboard-read", "clipboard-write", "payment-handler",
	"web-app-installation", "loopback-network", "local-network",
}

// denyPermissions denies every permission in permissionNames except
// sanitized clipboard writes; fullscreen needs no permission. A name the
// installed Chrome rejects is recorded and skipped.
func (i *Instance) denyPermissions(ctx context.Context) {
	denied := 0
	for _, name := range permissionNames {
		setting := browser.PermissionSettingDenied
		if name == "clipboard-write" {
			setting = browser.PermissionSettingGranted
		}
		_, err := chromedp.CallBrowser(ctx, browser.SetPermission, browser.SetPermissionParams{
			Permission: &browser.PermissionDescriptor{Name: name}, Setting: setting,
		})
		switch {
		case err != nil:
			i.logEvent("permission_unsupported", "", name)
		case setting == browser.PermissionSettingDenied:
			denied++
		}
	}
	i.logEvent(EventPermissionDenied, "", fmt.Sprintf("%d permission kinds denied", denied))
}

// adoptExisting labels pages that exist when the instance attaches. Windows
// come from the record; a page in no recorded window is apply-lane and
// user-owned. Scan pages no run owns are closed after a grace period. When the
// record says the connection was lost during a tab creation or with one left
// unanswered (Record.Abandoned), a blank page with no opener that the lost
// instance did not know is the tab the browser made for such a creation, and
// it is closed, as onPageCreated closes one that arrives late. The record is
// then written again without that mark, so a later adoption closes nothing
// this instance holds.
func (i *Instance) adoptExisting(ctx context.Context) {
	abandoned, known := false, map[target.ID]bool{}
	if r, ok := ReadRecord(i.recordPath()); ok && r.PID == i.proc.PID {
		i.mu.Lock()
		for _, w := range r.ApplyWindows {
			i.windows[int64(w)] = LaneApply
		}
		for _, w := range r.ScanWindows {
			i.windows[int64(w)] = LaneScan
		}
		i.mu.Unlock()
		abandoned = r.Abandoned
		for _, id := range r.Tabs {
			known[target.ID(id)] = true
		}
	}
	infos, err := chromedp.Targets(ctx)
	if err != nil {
		return
	}
	orphans := false
	for _, in := range infos {
		if in.Type != "page" {
			continue
		}
		if abandoned && in.OpenerID == "" && in.URL == "about:blank" && !known[in.TargetID] {
			i.closeTarget(in.TargetID, "late-creation")
			i.logEvent(EventTabClosed, "", "late-creation")
			continue
		}
		win, err := i.windowOf(ctx, in.TargetID)
		if err != nil {
			continue
		}
		t := &tab{id: in.TargetID, lane: LaneApply, window: win, opener: in.OpenerID, created: time.Now(), adopted: true}
		i.mu.Lock()
		if l, ok := i.windows[win]; ok && l == LaneScan {
			t.lane = LaneScan
			orphans = true
		}
		i.tabs[t.id] = t
		i.touchLocked()
		i.mu.Unlock()
		i.logTab(EventTabOpened, t, "adopted")
	}
	if abandoned {
		i.saveRecord(false)
	}
	if orphans {
		time.AfterFunc(orphanGrace, i.closeOrphanScanTabs)
	}
}

func (i *Instance) closeOrphanScanTabs() {
	i.mu.Lock()
	var orphans []*tab
	for _, t := range i.tabs {
		if t.lane == LaneScan && t.adopted && t.owner == "" {
			orphans = append(orphans, t)
		}
	}
	i.mu.Unlock()
	for _, t := range orphans {
		i.closeTarget(t.id, "orphan-scan-tab")
	}
}
