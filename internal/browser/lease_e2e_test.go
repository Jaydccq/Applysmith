//go:build browser

package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ownInstance starts a Chrome of its own, in a fresh directory, so a test can
// stop it, kill it or restart it. Whatever is left of it is ended at cleanup.
func ownInstance(t *testing.T, tune func(*Options)) (*Instance, Options) {
	t.Helper()
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	o.EventLogPath = filepath.Join(dir, "logs", "browser-events.jsonl")
	if tune != nil {
		tune(&o)
	}
	inst, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { endChrome(inst) })
	return inst, o
}

// endChrome stops the instance and kills what is left of its process group.
func endChrome(inst *Instance) {
	inst.Stop(context.Background())
	if processAlive(inst.PID()) {
		killGroup(inst.PID())
	}
}

// reopen returns the instance a later Open gives for the options, which adopts
// or launches, and ends it at cleanup.
func reopen(t *testing.T, o Options) *Instance {
	t.Helper()
	inst, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { endChrome(inst) })
	return inst
}

func hasEventWith(i *Instance, kind, sub string) bool {
	for _, e := range i.Events() {
		if e.Kind == kind && (sub == "" || strings.Contains(e.Detail, sub)) {
			return true
		}
	}
	return false
}

// Releasing a lease ends the run's hold. It closes the run's own tabs and
// leaves Chrome, the apply window and a tab kept for the user as they were.
func TestBrowser_ReleaseLease_DoesNotStopChrome(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	applyLease, err := inst.Acquire(LaneApply, "run-apply")
	ok(t, err)
	apply := applyLease.Lane()
	kept, err := apply.OpenTab(ctx30(t), "")
	ok(t, err)
	ok(t, apply.Keep(kept))
	applyLease.Release()

	scanLease, err := inst.Acquire(LaneScan, "run-scan")
	ok(t, err)
	scan := scanLease.Lane()
	own, err := scan.OpenTab(ctx30(t), "")
	ok(t, err)
	pid := inst.PID()
	applyWindow := windowOfTab(t, inst, kept)
	scanLease.Release()

	eventually(t, 5*time.Second, "the scan lease's tab closed with the run", func() bool {
		_, err := inst.Lane(LaneScan).Navigate(ctx30(t), own, "about:blank")
		code, _ := CodeOf(err)
		return code == CodeTabClosed
	})
	if !hasEventWith(inst, EventTabClosed, "run-ended") {
		t.Fatal("no tab_closed event with the reason run-ended")
	}
	time.Sleep(500 * time.Millisecond)
	if !processAlive(pid) || inst.isStopped() {
		t.Fatal("releasing a lease stopped Chrome")
	}
	if tabs := inst.Lane(LaneApply).Tabs(); len(tabs) != 1 || tabs[0].ID != kept || !tabs[0].Kept {
		t.Fatalf("the kept tab changed: %+v", tabs)
	}
	if got := windowOfTab(t, inst, kept); got != applyWindow {
		t.Fatalf("the apply window changed from %d to %d", applyWindow, got)
	}
	if inst.Leased() {
		t.Fatal("a released lease is still listed")
	}
}

// A tab creation the browser has not acknowledged still belongs to the lease
// that asked for it: the lease stays held across its release, and the tab does
// not outlive it.
func TestE2E_Browser_PendingCreationOutlivesRelease(t *testing.T) {
	inst, _ := sharedInstance(t)
	gate, arrived := make(chan struct{}), make(chan struct{})
	createGate = func(Lane) { close(arrived); <-gate }
	t.Cleanup(func() { createGate = nil })
	l, err := inst.Acquire(LaneScan, "run-pending")
	ok(t, err)
	before := len(inst.Lane(LaneScan).Tabs())
	var openErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, openErr = l.Lane().OpenTab(ctx30(t), "")
	}()
	<-arrived
	l.Release()
	if !inst.Leased() {
		t.Fatal("the lease ended while its tab creation was pending")
	}
	createGate = nil
	close(gate)
	wg.Wait()
	if code, _ := CodeOf(openErr); code != CodeLeaseReleased {
		t.Fatalf("code %q err %v, want lease-released", code, openErr)
	}
	eventually(t, 5*time.Second, "the tab made after the release is gone and the lease dropped", func() bool {
		return !inst.Leased() && len(inst.Lane(LaneScan).Tabs()) == before
	})
}

// Two scan tabs navigate and evaluate at once; the server sees both requests
// in flight together, which one serialized operation could never show.
func TestE2E_Browser_ConcurrentTabOperations(t *testing.T) {
	inst, fx := sharedInstance(t)
	fx.holdArrivals.Store(0)
	fx.holdAlone.Store(0)
	a, b := openTab(t, inst, LaneScan, "run-conc-a"), openTab(t, inst, LaneScan, "run-conc-b")
	h, ctx := inst.Lane(LaneScan), ctx30(t)
	var wg sync.WaitGroup
	for n, id := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Distinct addresses: the browser's cache coalesces two requests for one.
			if _, err := h.Navigate(ctx, id, fx.Loopback("/hold?tab="+string(rune('a'+n)))); err != nil {
				t.Error(err)
				return
			}
			if got, err := h.Evaluate(ctx, id, `document.getElementById('p').textContent`); err != nil || string(got) != `"held"` {
				t.Errorf("tab %s read %s (%v)", id, got, err)
			}
		}()
	}
	wg.Wait()
	// Each answer waits for the other request to arrive; of a serialized pair
	// the first would be answered alone, when the server gives up waiting.
	if n := fx.holdAlone.Load(); n != 0 {
		t.Fatalf("%d of the two navigations was answered before the other arrived: they waited for each other", n)
	}
}

// A scan tab has a fixed viewport of 1280 by 900.
func TestE2E_Browser_ScanViewportSize(t *testing.T) {
	inst, _ := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-viewport")
	size := eval[struct{ W, H float64 }](t, inst.Lane(LaneScan), tab, `({W: innerWidth, H: innerHeight})`)
	if size.W != 1280 || size.H > 900 || size.H < 600 {
		t.Fatalf("scan viewport %vx%v, want 1280 wide and about 900 tall", size.W, size.H)
	}
}

// A popup keeps the run of its opener, even after the opener is gone, and
// closes with the run.
func TestE2E_Browser_RunEndClosesPopupOfClosedOpener(t *testing.T) {
	inst, fx := sharedInstance(t)
	h := inst.Lane(LaneApply)
	opener, err := h.OpenTab(ctx30(t), "run-popup-end")
	ok(t, err)
	navigate(t, inst, LaneApply, opener, fx.Loopback("/opener"))
	gesture(t, inst, LaneApply, opener, `document.getElementById('b').click()`)
	var popup string
	eventually(t, 10*time.Second, "the popup labelled with the opener's run", func() bool {
		for _, ti := range h.Tabs() {
			if ti.Opener == opener && ti.Owner == "run-popup-end" {
				popup = ti.ID
				return true
			}
		}
		return false
	})
	ok(t, h.CloseTab(ctx30(t), opener))
	h.CloseOwned(ctx30(t), "run-popup-end")
	eventually(t, 5*time.Second, "the popup closed with the run", func() bool {
		for _, ti := range h.Tabs() {
			if ti.ID == popup {
				return false
			}
		}
		return true
	})
}

// A graceful stop sweeps cookies, closes Chrome, closes every tab, a tab kept
// for the user among them, and removes the instance record.
func TestE2E_Browser_GracefulStopClosesTabs(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	lease, err := inst.Acquire(LaneApply, "run-stop")
	ok(t, err)
	h := lease.Lane()
	kept, err := h.OpenTab(ctx30(t), "")
	ok(t, err)
	ok(t, h.Keep(kept))
	openTab(t, inst, LaneScan, "run-stop")
	ok(t, inst.SetCookie(ctx30(t), Cookie{Name: "sid", Value: "v", Domain: "jobs.example.test", Path: "/"}))
	closed := make(chan struct{})
	tb, err := inst.Lane(LaneApply).tab(kept)
	ok(t, err)
	tb.mu.Lock()
	tb.onClosed = append(tb.onClosed, func() { close(closed) })
	tb.mu.Unlock()
	pid := inst.PID()
	if _, err := os.Stat(inst.recordPath()); err != nil {
		t.Fatalf("no record while running: %v", err)
	}
	if out := inst.Stop(ctx30(t)); out != StopClosed {
		t.Fatalf("outcome %q", out)
	}
	if processAlive(pid) {
		t.Fatal("Chrome is still running")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the kept tab was not closed with the browser")
	}
	if n := len(inst.Lane(LaneApply).Tabs()) + len(inst.Lane(LaneScan).Tabs()); n != 0 {
		t.Fatalf("%d tabs after the stop", n)
	}
	if _, err := os.Stat(inst.recordPath()); !os.IsNotExist(err) {
		t.Fatalf("the instance record outlived the stop: %v", err)
	}
	if !hasEventWith(inst, EventCookieSweep, "1 cookies") {
		t.Fatal("cookies were not swept before the stop")
	}
}

// A stopping server leaves the record in place when it names another server's
// instance.
func TestE2E_Browser_StopLeavesAnotherServersRecord(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	openTab(t, inst, LaneApply, "run-record")
	openTab(t, inst, LaneScan, "run-record")
	eventually(t, 5*time.Second, "the record", func() bool { _, ok := ReadRecord(inst.recordPath()); return ok })
	rec, _ := ReadRecord(inst.recordPath())
	rec.PID = os.Getpid() // a newer server's instance
	ok(t, WriteRecord(inst.recordPath(), rec))
	inst.Stop(ctx30(t))
	got, found := ReadRecord(inst.recordPath())
	if !found || got.PID != os.Getpid() {
		t.Fatalf("the stop removed a record that named another instance: %+v %v", got, found)
	}
	// And one that names this instance is removed (see TestE2E_Browser_GracefulStopClosesTabs).
}

// Focus emulation keeps a tab that is not its window's active tab, and one in a
// covered window, focused and rendering.
func TestE2E_Browser_FocusEmulationKeepsACoveredTabFocused(t *testing.T) {
	inst, fx := sharedInstance(t)
	first := openTab(t, inst, LaneApply, "run-focus")
	second := openTab(t, inst, LaneApply, "run-focus") // the first is no longer the window's active tab
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, first, fx.Loopback("/render"))
	navigate(t, inst, LaneApply, second, fx.Loopback("/render"))
	for _, id := range []string{first, second} {
		if !eval[bool](t, h, id, `document.hasFocus()`) {
			t.Errorf("tab %s does not report focus", id)
		}
		if got := eval[string](t, h, id, `document.getElementById('t').textContent`); got != "updated" {
			eventually(t, 3*time.Second, "the DOM update on a covered tab", func() bool {
				return eval[string](t, h, id, `document.getElementById('t').textContent`) == "updated"
			})
		}
	}
	if hasEvent(inst, EventFocusEmulationFail, "") {
		t.Fatal("focus emulation failed on a tab")
	}
}
