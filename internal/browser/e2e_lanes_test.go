//go:build browser

package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func openTab(t *testing.T, inst *Instance, lane Lane, owner string) string {
	t.Helper()
	h := inst.Lane(lane)
	id, err := h.OpenTab(ctx30(t), owner)
	ok(t, err)
	t.Cleanup(func() { _ = h.CloseTab(context.Background(), id) })
	return id
}

func navigate(t *testing.T, inst *Instance, lane Lane, id, url string) NavResult {
	t.Helper()
	r, err := inst.Lane(lane).Navigate(ctx30(t), id, url)
	ok(t, err)
	if r.Error != "" {
		t.Fatalf("load %s failed: %s", url, r.Error)
	}
	return r
}

// gesture evaluates expr with a user gesture, the way a click would.
func gesture(t *testing.T, inst *Instance, lane Lane, id, expr string) {
	t.Helper()
	ok(t, tryGesture(inst, lane, id, expr))
}

// tryGesture is gesture for a goroutine other than the test's own, which may
// not end the test: it returns the failure.
func tryGesture(inst *Instance, lane Lane, id, expr string) error {
	tb, err := inst.Lane(lane).tab(id)
	if err != nil {
		return err
	}
	sess, err := inst.session(tb)
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(sess, 10*time.Second)
	defer cancel()
	_, err = chromedp.Call(c, runtime.Evaluate, runtime.EvaluateParams{Expression: expr, UserGesture: ptr(true)})
	return err
}

func windowOfTab(t *testing.T, inst *Instance, id string) int64 {
	t.Helper()
	w, err := inst.windowOf(ctx30(t), target.ID(id))
	ok(t, err)
	return w
}

func TestE2E_Browser_TabPlacement(t *testing.T) {
	inst, _ := sharedInstance(t)
	applyTab := openTab(t, inst, LaneApply, "run-a")
	scanTab := openTab(t, inst, LaneScan, "run-s")
	scanTab2 := openTab(t, inst, LaneScan, "run-s")
	applyTab2 := openTab(t, inst, LaneApply, "run-a")
	aw, aw2 := windowOfTab(t, inst, applyTab), windowOfTab(t, inst, applyTab2)
	sw, sw2 := windowOfTab(t, inst, scanTab), windowOfTab(t, inst, scanTab2)
	if aw == sw || aw == sw2 {
		t.Fatalf("apply and scan tabs share a window")
	}
	if aw != aw2 {
		t.Fatalf("second apply tab opened in window %d, not the apply window %d", aw2, aw)
	}
	if sw == sw2 {
		t.Fatalf("scan tabs share window %d; each scan tab keeps its own window", sw)
	}
	for lane, ids := range map[Lane][]string{LaneScan: {scanTab, scanTab2}, LaneApply: {applyTab, applyTab2}} {
		for _, id := range ids {
			st, err := inst.Lane(lane).WindowState(ctx30(t), id)
			ok(t, err)
			if st != "normal" {
				t.Fatalf("window state %q, want normal (covered, not minimized)", st)
			}
		}
	}
	if err := inst.Lane(LaneScan).ShowWindow(ctx30(t), scanTab); err == nil {
		t.Fatal("showing a scan tab must be refused")
	}
}

func TestE2E_Browser_LaneHandle_WrongLane(t *testing.T) {
	inst, fx := sharedInstance(t)
	applyTab := openTab(t, inst, LaneApply, "run-a")
	navigate(t, inst, LaneApply, applyTab, fx.Loopback("/probe"))
	scan := inst.Lane(LaneScan)
	for name, call := range map[string]func() error{
		"navigate": func() error { _, err := scan.Navigate(ctx30(t), applyTab, fx.Loopback("/clicks")); return err },
		"evaluate": func() error { _, err := scan.Evaluate(ctx30(t), applyTab, "1"); return err },
		"capture":  func() error { _, err := scan.Screenshot(ctx30(t), applyTab); return err },
		"show":     func() error { return scan.ShowWindow(ctx30(t), applyTab) },
		"close":    func() error { return scan.CloseTab(ctx30(t), applyTab) },
	} {
		if code, _ := CodeOf(call()); code != CodeWrongLane {
			t.Errorf("%s from the scan lane: code %q, want wrong-lane", name, code)
		}
	}
	if got := eval[string](t, inst.Lane(LaneApply), applyTab, "location.pathname"); got != "/probe" {
		t.Fatalf("apply tab moved to %q", got)
	}
	if n := len(scan.Tabs()); n > 0 {
		for _, ti := range scan.Tabs() {
			if ti.ID == applyTab {
				t.Fatal("scan lane lists an apply tab")
			}
		}
	}
}

func TestE2E_Browser_UserOpenedTabIsApplyLaneAndSurvivesCleanup(t *testing.T) {
	inst, _ := sharedInstance(t)
	mine := openTab(t, inst, LaneApply, "run-clean")
	// A tab the user opens by hand has no opener and no owner.
	res, err := chromedp.CallBrowser(inst.root, target.CreateTarget, target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(false), Focus: ptr(false)})
	ok(t, err)
	user := string(res.TargetID)
	t.Cleanup(func() {
		_, _ = chromedp.CallBrowser(inst.root, target.CloseTarget, target.CloseTargetParams{TargetID: res.TargetID})
	})
	eventually(t, 5*time.Second, "user tab labelled apply with no owner", func() bool {
		for _, ti := range inst.Lane(LaneApply).Tabs() {
			if ti.ID == user && ti.Owner == "" {
				return true
			}
		}
		return false
	})
	kept := openTab(t, inst, LaneApply, "run-clean")
	ok(t, inst.Lane(LaneApply).Keep(kept))
	inst.Lane(LaneApply).CloseOwned(ctx30(t), "run-clean")
	live := map[string]bool{}
	for _, ti := range inst.Lane(LaneApply).Tabs() {
		live[ti.ID] = true
	}
	if live[mine] {
		t.Error("run-owned tab survived cleanup")
	}
	if !live[user] {
		t.Error("user-opened tab was closed by run cleanup")
	}
	if !live[kept] {
		t.Error("kept tab was closed by run cleanup")
	}
}

// The user closes the last tab of the apply window by hand, and a run opens an
// apply tab before the instance has heard of the close: the tab opens a new
// apply window instead of failing as a lost browser.
func TestE2E_Browser_ApplyTabAfterAnUnheardWindowClose(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	h := inst.Lane(LaneApply)
	id, err := h.OpenTab(ctx30(t), "")
	ok(t, err)
	gone := windowOfTab(t, inst, id)
	_, err = chromedp.CallBrowser(inst.root, target.CloseTarget, target.CloseTargetParams{TargetID: target.ID(id)})
	ok(t, err)
	eventually(t, 10*time.Second, "the close heard", func() bool { _, err := h.tab(id); return err != nil })
	inst.mu.Lock()
	inst.windows[gone] = LaneApply // as before the close is heard
	inst.mu.Unlock()
	next, err := h.OpenTab(ctx30(t), "run-1")
	ok(t, err)
	if w := windowOfTab(t, inst, next); w == gone {
		t.Fatalf("the tab is in the closed window %d", w)
	}
}

func TestE2E_Browser_TabCapReached(t *testing.T) {
	inst, _ := sharedInstance(t)
	old := scanTabCap
	t.Cleanup(func() { scanTabCap = old })
	scanTabCap = inst.laneCount(LaneScan) + 2
	openTab(t, inst, LaneScan, "run-cap")
	openTab(t, inst, LaneScan, "run-cap")
	before := inst.laneCount(LaneScan)
	_, err := inst.Lane(LaneScan).OpenTab(ctx30(t), "run-cap")
	if code, _ := CodeOf(err); code != CodeLaneTabLimit {
		t.Fatalf("code %q err %v, want lane-tab-limit", code, err)
	}
	if after := inst.laneCount(LaneScan); after != before {
		t.Fatalf("tab count changed from %d to %d", before, after)
	}
}

func TestE2E_Browser_URLSchemeAllowlist(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-scheme")
	h := inst.Lane(LaneScan)
	navigate(t, inst, LaneScan, tab, fx.Loopback("/probe"))
	for _, u := range []string{"file:///etc/hosts", "chrome://version", "devtools://devtools/bundled/inspector.html", "chrome-extension://abc/x.html", "view-source:http://example.com", "javascript:alert(1)", "ftp://example.com/x"} {
		_, err := h.Navigate(ctx30(t), tab, u)
		if code, _ := CodeOf(err); code != CodeSchemeNotAllowed {
			t.Errorf("%s: code %q, want scheme-not-allowed", u, code)
		}
	}
	if got := eval[string](t, h, tab, "location.pathname"); got != "/probe" {
		t.Fatalf("tab left its page: %s", got)
	}
	for _, u := range []string{fx.Loopback("/probe"), "data:text/html,<title>d</title>", "about:blank"} {
		if _, err := h.Navigate(ctx30(t), tab, u); err != nil {
			t.Errorf("allowed navigation to %.20s failed: %v", u, err)
		}
	}
}

func TestE2E_Browser_PageNavigationToBlockedSchemeReverted(t *testing.T) {
	inst, fx := sharedInstance(t)
	for _, path := range []string{"/chrome-nav", "/file-nav"} {
		tab := openTab(t, inst, LaneScan, "run-revert")
		h := inst.Lane(LaneScan)
		since := time.Now()
		_, _ = h.Navigate(ctx30(t), tab, fx.Loopback(path))
		eventually(t, 2*time.Second, "tab reverted to about:blank for "+path, func() bool {
			raw, err := h.Evaluate(ctx30(t), tab, "location.href")
			return err == nil && strings.Contains(string(raw), "about:blank")
		})
		if !hasEventSince(inst, "navigation_blocked", "", since) {
			t.Fatalf("no navigation_blocked event for %s", path)
		}
	}
}

// A hidden srcdoc frame is a subframe's navigation, not the tab's page: the
// tab keeps the page it loaded instead of being sent to about:blank.
func TestE2E_Browser_SrcdocSubframeLeavesTabAlone(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-srcdoc")
	h := inst.Lane(LaneScan)
	since := time.Now()
	navigate(t, inst, LaneScan, tab, fx.Loopback("/srcdoc-frame"))
	// A wrong revert follows the frame's event asynchronously, so the page is
	// read after the window in which the real revert is required to land.
	time.Sleep(time.Second)
	if got := eval[string](t, h, tab, "document.getElementById('p') ? document.getElementById('p').textContent : location.href"); got != "drawn" {
		t.Fatalf("tab left its page: %s", got)
	}
	if hasEventSince(inst, EventNavigationBlocked, "", since) {
		t.Fatal("the subframe's address was treated as the tab's")
	}
}

func TestE2E_Browser_PermissionsDeniedAndClipboardWriteAllowed(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-perm")
	h := inst.Lane(LaneScan)
	navigate(t, inst, LaneScan, tab, fx.Localhost("/geo"))
	code := eval[int](t, h, tab, `new Promise(r=>navigator.geolocation.getCurrentPosition(()=>r(0),e=>r(e.code),{timeout:5000}))`)
	if code != 1 {
		t.Fatalf("geolocation error code %d, want 1 (PERMISSION_DENIED)", code)
	}
	for _, name := range []string{"geolocation", "notifications", "midi", "camera", "microphone"} {
		state := eval[string](t, h, tab, `navigator.permissions.query({name:'`+name+`'}).then(s=>s.state,e=>'error')`)
		if state != "denied" {
			t.Errorf("permission %s reports %q, want denied", name, state)
		}
	}
	// Chrome ties a gesture-free write to the read permission, which stays
	// denied; a write inside a user gesture is allowed.
	if res := eval[string](t, h, tab, `navigator.clipboard.writeText('example').then(()=>'ok',e=>String(e))`); res == "ok" {
		t.Fatal("a gesture-free clipboard write succeeded while clipboard read is denied")
	}
	tb, err := h.tab(tab)
	ok(t, err)
	sess, err := inst.session(tb)
	ok(t, err)
	c, cancel := context.WithTimeout(sess, 5*time.Second)
	defer cancel()
	res, err := chromedp.Call(c, runtime.Evaluate, runtime.EvaluateParams{
		Expression: `navigator.clipboard.writeText('example').then(()=>'ok',e=>String(e))`, UserGesture: ptr(true), AwaitPromise: ptr(true), ReturnByValue: ptr(true)})
	ok(t, err)
	var got string
	_ = json.Unmarshal(res.Result.Value, &got)
	if got != "ok" {
		t.Fatalf("clipboard write inside a user gesture: %s", got)
	}
}

func TestE2E_Browser_FileChooserIntercepted(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-chooser")
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/chooser"))
	rect := eval[struct{ X, Y float64 }](t, h, tab, `(()=>{const r=document.getElementById('f').getBoundingClientRect();return {X:r.x+r.width/2,Y:r.y+r.height/2}})()`)
	tb, _ := h.tab(tab)
	sess, err := inst.session(tb)
	ok(t, err)
	c, cancel := context.WithTimeout(sess, 5*time.Second)
	defer cancel()
	for _, typ := range []input.DispatchMouseEventType{input.DispatchMouseEventTypeMousePressed, input.DispatchMouseEventTypeMouseReleased} {
		_, err := chromedp.Call(c, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: typ, X: rect.X, Y: rect.Y, Button: input.MouseButtonLeft, ClickCount: 1})
		ok(t, err)
	}
	eventually(t, 3*time.Second, "file_chooser_intercepted event", func() bool { return hasEvent(inst, "file_chooser_intercepted", "") })
}

func TestE2E_Browser_DownloadPolicyByLane(t *testing.T) {
	inst, fx := sharedInstance(t)
	dl := sharedDownloads()
	_ = os.RemoveAll(dl)
	apply := openTab(t, inst, LaneApply, "run-dl")
	for range 2 {
		navigate(t, inst, LaneApply, apply, fx.Loopback("/dl/link"))
		gesture(t, inst, LaneApply, apply, `document.getElementById('a').click()`)
		time.Sleep(700 * time.Millisecond)
	}
	eventually(t, 10*time.Second, "both downloads saved", func() bool {
		_, e1 := os.Stat(filepath.Join(dl, "offer-letter.pdf"))
		_, e2 := os.Stat(filepath.Join(dl, "offer-letter (1).pdf"))
		return e1 == nil && e2 == nil
	})
	// Hostile suggested name.
	navigate(t, inst, LaneApply, apply, fx.Loopback("/dl/link"))
	gesture(t, inst, LaneApply, apply, `const a=document.getElementById('a');a.href='/dl/hostile';a.click()`)
	eventually(t, 10*time.Second, "hostile download saved", func() bool { return hasEvent(inst, "download_saved", "") && len(dirNames(dl)) >= 3 })
	t.Logf("saved names: %v", dirNames(dl))
	for _, n := range dirNames(dl) {
		if strings.ContainsAny(n, `/\`) || strings.HasPrefix(n, ".") {
			t.Errorf("unsafe saved name %q", n)
		}
	}
	// Where the suggested name points from the Downloads directory, and from
	// one level up in case only part of it was honoured.
	for _, escaped := range []string{filepath.Join(dl, "..", "..", ".ssh", "config"), filepath.Join(dl, "..", ".ssh")} {
		if _, err := os.Stat(escaped); err == nil {
			t.Fatalf("download escaped the Downloads directory to %s", escaped)
		}
	}
	// Scan lane: cancelled, no file anywhere.
	scan := openTab(t, inst, LaneScan, "run-dl")
	before := dirNames(dl)
	_, _ = inst.Lane(LaneScan).Navigate(ctx30(t), scan, fx.Loopback("/dl/offer"))
	eventually(t, 10*time.Second, "download_cancelled event", func() bool { return hasEvent(inst, "download_cancelled", "") })
	time.Sleep(500 * time.Millisecond)
	if after := dirNames(dl); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("Downloads changed after a scan download: %v -> %v", before, after)
	}
	for end := time.Now().Add(5 * time.Second); len(dirNames(inst.stagingDir())) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			for _, e := range func() []os.DirEntry { es, _ := os.ReadDir(inst.stagingDir()); return es }() {
				info, _ := e.Info()
				t.Logf("left in staging: %s (%d bytes, modified %v ago)", e.Name(), info.Size(), time.Since(info.ModTime()))
			}
			t.Fatalf("staging directory not empty: %v", dirNames(inst.stagingDir()))
		}
	}
}

func dirNames(dir string) []string {
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestE2E_Browser_WindowClosedByUserIsRecreated(t *testing.T) {
	inst, _ := sharedInstance(t)
	h := inst.Lane(LaneScan)
	a, err := h.OpenTab(ctx30(t), "run-win")
	ok(t, err)
	b, err := h.OpenTab(ctx30(t), "run-win")
	ok(t, err)
	oldWin := windowOfTab(t, inst, a)
	// The user closes the window: every tab in it goes at once.
	for _, id := range []string{a, b} {
		_, _ = chromedp.CallBrowser(inst.root, target.CloseTarget, target.CloseTargetParams{TargetID: target.ID(id)})
	}
	eventually(t, 5*time.Second, "tabs reported closed", func() bool {
		_, e1 := h.Evaluate(ctx30(t), a, "1")
		_, e2 := h.Evaluate(ctx30(t), b, "1")
		c1, _ := CodeOf(e1)
		c2, _ := CodeOf(e2)
		return c1 == CodeTabClosed && c2 == CodeTabClosed
	})
	c, err := h.OpenTab(ctx30(t), "run-win")
	ok(t, err)
	t.Cleanup(func() { _ = h.CloseTab(context.Background(), c) })
	if newWin := windowOfTab(t, inst, c); newWin == oldWin {
		t.Fatalf("window %d was not recreated", newWin)
	}
	st, err := h.WindowState(ctx30(t), c)
	ok(t, err)
	if st != "normal" {
		t.Fatalf("recreated scan window is %q, want normal", st)
	}
}

func TestE2E_Browser_ConcurrentOpenAcrossLanes(t *testing.T) {
	inst, _ := sharedInstance(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for _, l := range []Lane{LaneApply, LaneScan, LaneScan, LaneApply} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := inst.Lane(l).OpenTab(ctx30(t), "run-conc")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	for id := range ids {
		_, a := inst.Lane(LaneApply).tab(id)
		_, s := inst.Lane(LaneScan).tab(id)
		if a != nil && s != nil {
			t.Errorf("tab %s belongs to neither lane", id)
		}
	}
	inst.Lane(LaneApply).CloseOwned(ctx30(t), "run-conc")
	inst.Lane(LaneScan).CloseOwned(ctx30(t), "run-conc")
}

// A scan page that opens windows and raises dialogs, on load and from a timer,
// opens nothing, shows nothing and has its popup URL recorded. Answers match
// the dialog policy. The window here is headless, so the foreground
// application is the headed tests' subject, not this one's.
func TestE2E_Browser_ScanStubsKeepDialogsAndPopupsQuiet(t *testing.T) {
	inst, fx := sharedInstance(t)
	since := time.Now()
	tab := openTab(t, inst, LaneScan, "run-quiet")
	h := inst.Lane(LaneScan)
	tabsBefore := len(h.Tabs()) + len(inst.Lane(LaneApply).Tabs())
	navigate(t, inst, LaneScan, tab, fx.Loopback("/scan-hostile"))
	eventually(t, 3*time.Second, "the timer round", func() bool { return eval[int](t, h, tab, `window.__log.length`) == 2 })
	log := eval[[][]string](t, h, tab, `window.__log`)
	for _, row := range log {
		// open -> null, alert -> undefined, confirm -> false, prompt -> null
		if row[1] != "null" || row[2] != "undefined" || row[3] != "false" || row[4] != "null" {
			t.Fatalf("stub answers %v", row)
		}
	}
	urls, err := h.PopupURLs(tab)
	ok(t, err)
	if len(urls) != 2 || urls[0] != fx.B("/ran") {
		t.Fatalf("popup URLs %v", urls)
	}
	if got := len(h.Tabs()) + len(inst.Lane(LaneApply).Tabs()); got != tabsBefore {
		t.Fatalf("tab count %d -> %d: a window opened", tabsBefore, got)
	}
	// Leaving a page that asked for a beforeunload prompt must not prompt either.
	navigate(t, inst, LaneScan, tab, fx.Loopback("/probe"))
	if hasEventSince(inst, EventDialogHandled, "", since) {
		t.Fatal("a native dialog reached the DevTools handler")
	}
	// Page code cannot find the binding that carries the popup URL.
	if n := eval[int](t, h, tab, `Object.getOwnPropertyNames(globalThis).filter(n=>/^aj_/.test(n)).length`); n != 0 {
		t.Fatalf("%d bridge globals visible to the page", n)
	}
}
