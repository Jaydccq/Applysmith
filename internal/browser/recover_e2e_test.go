//go:build browser

package browser

import (
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// killBrowser ends the browser process the way a crash does.
func killBrowser(t *testing.T, inst *Instance) {
	t.Helper()
	ok(t, syscall.Kill(inst.PID(), syscall.SIGKILL))
}

// A killed browser fails the call in flight with browser-disconnected at once
// and records who went first and with what status. The lease's next call
// performs its one recovery, and the run is told which tabs are gone.
func TestBrowser_Disconnect_RecordsCause(t *testing.T) {
	inst, o := ownInstance(t, nil)
	lease, err := inst.Acquire(LaneScan, "run-crash")
	ok(t, err)
	h := lease.Lane()
	tab, err := h.OpenTab(ctx30(t), "")
	ok(t, err)
	oldPID := inst.PID()

	var wg sync.WaitGroup
	var inFlight error
	started := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(started)
		_, inFlight = h.Evaluate(ctx30(t), tab, `new Promise(r => setTimeout(r, 60000))`)
	}()
	<-started
	time.Sleep(300 * time.Millisecond)
	killBrowser(t, inst)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the call in flight did not fail after the browser died")
	}
	if code, _ := CodeOf(inFlight); code != CodeBrowserDisconnected {
		t.Fatalf("in-flight call: code %q err %v, want browser-disconnected", code, inFlight)
	}
	t.Cleanup(func() {
		if t.Failed() {
			for _, e := range inst.Events() {
				t.Logf("event %s %s %q", e.Kind, e.Lane, e.Detail)
			}
		}
	})
	eventually(t, 5*time.Second, "browser_exited with its cause", func() bool {
		return hasEventWith(inst, EventBrowserExited, "signal: killed") && hasEventWith(inst, EventBrowserExited, "clean=false")
	})

	// The next call recovers: a new instance, a new window, the old tabs gone.
	fresh, err := h.OpenTab(ctx30(t), "")
	ok(t, err)
	t.Cleanup(func() {
		if cur := lease.current(); cur != inst {
			endChrome(cur)
		}
	})
	cur := lease.current()
	if cur == inst || cur.PID() == oldPID {
		t.Fatal("the lease still works on the dead instance")
	}
	if !lease.Recovered() {
		t.Fatal("the lease does not know it recovered")
	}
	if lost := lease.Lost(); len(lost) != 1 || lost[0] != tab {
		t.Fatalf("lost tabs %v, want [%s]", lost, tab)
	}
	if _, err := h.Evaluate(ctx30(t), tab, "1"); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatalf("an operation on a lost tab: %v", err)
	}
	if _, err := h.Evaluate(ctx30(t), fresh, "1+1"); err != nil {
		t.Fatalf("the recovered lane does not work: %v", err)
	}
	if !hasEventWith(cur, EventRecoveryAttempted, "recovered") {
		t.Fatal("no recovery_attempted event on the new instance")
	}
	_ = o
}

// A second failure under the same lease ends the run with browser-crashed and
// no further relaunch; a new lease can recover once again.
func TestE2E_Browser_SecondFailureEndsTheLeaseButNotTheNextOne(t *testing.T) {
	inst, o := ownInstance(t, nil)
	lease, err := inst.Acquire(LaneScan, "run-twice")
	ok(t, err)
	h := lease.Lane()
	_, err = h.OpenTab(ctx30(t), "")
	ok(t, err)
	killBrowser(t, inst)
	eventually(t, 10*time.Second, "the first death noticed", func() bool { return inst.deadInfo() != nil })
	_, err = h.OpenTab(ctx30(t), "") // the one recovery
	ok(t, err)
	second := lease.current()
	t.Cleanup(func() { endChrome(second) })
	killBrowser(t, second)
	eventually(t, 10*time.Second, "the second death noticed", func() bool { return second.deadInfo() != nil })
	_, err = h.OpenTab(ctx30(t), "")
	if code, _ := CodeOf(err); code != CodeBrowserCrashed {
		t.Fatalf("code %q err %v, want browser-crashed", code, err)
	}
	if !strings.Contains(err.Error(), "event log") {
		t.Fatalf("the message does not name the browser log: %v", err)
	}
	if n := len(chromeMainProcesses(second.proc.Profile)); n != 0 {
		t.Fatalf("%d Chrome processes after the second failure: no relaunch may happen", n)
	}
	if _, err := h.OpenTab(ctx30(t), ""); err == nil {
		t.Fatal("a lease that ended with browser-crashed works again")
	}

	// A new lease gets a new allowance.
	next, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { endChrome(next) })
	lease2, err := next.Acquire(LaneScan, "run-again")
	ok(t, err)
	h2 := lease2.Lane()
	_, err = h2.OpenTab(ctx30(t), "")
	ok(t, err)
	killBrowser(t, next)
	eventually(t, 10*time.Second, "the death noticed", func() bool { return next.deadInfo() != nil })
	_, err = h2.OpenTab(ctx30(t), "")
	ok(t, err)
	t.Cleanup(func() { endChrome(lease2.current()) })
	if !lease2.Recovered() {
		t.Fatal("the new lease did not recover")
	}
}

// A lease its run released is never recovered. The keeper still reaches a kept
// tab through the handle of the run that kept it; when the browser has gone
// since, a recovery through that handle would hold the apply lane again for a
// run that ended, and every later run would be refused as lane-busy (a kept
// ADP tab's lease came back after a disconnect and held the lane).
func TestE2E_Browser_ReleasedLeaseIsNotRecovered(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	lease, err := inst.Acquire(LaneApply, "run-kept")
	ok(t, err)
	h := lease.Lane()
	tab, err := h.OpenTab(ctx30(t), "")
	ok(t, err)
	ok(t, h.Keep(tab))
	lease.Release()
	killBrowser(t, inst)
	eventually(t, 10*time.Second, "the death noticed", func() bool { return inst.deadInfo() != nil })

	err = h.CloseTab(ctx30(t), tab)
	if cur := lease.current(); cur != inst {
		t.Cleanup(func() { endChrome(cur) })
		if _, err := cur.Acquire(LaneApply, "run-next"); err != nil {
			t.Fatalf("the next run cannot take the apply lane: %v", err)
		}
		t.Fatal("a released lease brought a browser back")
	}
	if code, _ := CodeOf(err); code != CodeBrowserDisconnected {
		t.Fatalf("code %q err %v, want browser-disconnected", code, err)
	}
	if n := len(chromeMainProcesses(inst.proc.Profile)); n != 0 {
		t.Fatalf("%d Chrome processes: a released lease may not relaunch the browser", n)
	}
}

// A browser the user quit exits cleanly: the run holding a lane fails with
// browser-closed-by-user and nothing relaunches it.
func TestE2E_Browser_UserQuitIsNotRelaunched(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	lease, err := inst.Acquire(LaneApply, "run-quit")
	ok(t, err)
	h := lease.Lane()
	tab, err := h.OpenTab(ctx30(t), "")
	ok(t, err)
	askToQuit(inst.PID())
	eventually(t, 15*time.Second, "the quit noticed", func() bool { return inst.deadInfo() != nil })
	if info := inst.deadInfo(); !info.Clean {
		t.Fatalf("a quit must read as a clean exit: %s", info.Cause)
	}
	_, err = h.Evaluate(ctx30(t), tab, "1")
	if code, _ := CodeOf(err); code != CodeBrowserDisconnected && code != CodeBrowserClosedByUser {
		t.Fatalf("code %q err %v", code, err)
	}
	_, err = h.OpenTab(ctx30(t), "")
	if code, _ := CodeOf(err); code != CodeBrowserClosedByUser {
		t.Fatalf("code %q err %v, want browser-closed-by-user", code, err)
	}
	if n := len(chromeMainProcesses(inst.proc.Profile)); n != 0 {
		t.Fatalf("Chrome was relaunched after the user quit it (%d processes)", n)
	}
}

// One renderer dying closes that tab and reports tab-crashed; the other tabs
// of the lane keep working.
func TestE2E_Browser_TabCrashIsolated(t *testing.T) {
	inst, fx := sharedInstance(t)
	h := inst.Lane(LaneScan)
	doomed, healthy := openTab(t, inst, LaneScan, "run-crash-a"), openTab(t, inst, LaneScan, "run-crash-b")
	navigate(t, inst, LaneScan, doomed, fx.A("/probe"))
	navigate(t, inst, LaneScan, healthy, fx.B("/ran"))
	sess := tabSession(t, inst, LaneScan, doomed)
	// The crash command never answers; the call is allowed to fail.
	go func() { _, _ = chromedp.Call(sess, page.Crash, struct{}{}) }()
	eventually(t, 10*time.Second, "tab_crashed", func() bool { return hasEvent(inst, EventTabCrashed, "") })
	eventually(t, 10*time.Second, "the crashed tab closed", func() bool {
		_, err := h.Evaluate(ctx30(t), doomed, "1")
		code, _ := CodeOf(err)
		return code == CodeTabClosed
	})
	if got := eval[string](t, h, healthy, `document.title`); got != "ran" {
		t.Fatalf("the other tab reads %q", got)
	}
}
