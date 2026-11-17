//go:build browser

package browser

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// These tests open popups or raise windows, which brings Chrome to the front
// of the user's applications. Chrome also activates the tab of any JavaScript
// dialog before DevTools can answer it, so every dialog test is here too. They
// are kept in the last file so every foreground check in the other tests runs
// first.

func TestE2E_Browser_PopupInheritsLane(t *testing.T) {
	inst, fx := sharedInstance(t)
	applyTab := openTab(t, inst, LaneApply, "run-pop")
	navigate(t, inst, LaneApply, applyTab, fx.Loopback("/geo"))
	t.Cleanup(func() { inst.Lane(LaneApply).CloseOwned(context.Background(), "run-pop") })
	gesture(t, inst, LaneApply, applyTab, `void window.open('about:blank#popup')`)
	eventually(t, 5*time.Second, "popup labelled apply lane in the opener's window", func() bool {
		for _, ti := range inst.Lane(LaneApply).Tabs() {
			if ti.Opener == applyTab && ti.Owner == "run-pop" && ti.Window == windowOfTab(t, inst, applyTab) {
				return true
			}
		}
		return false
	})
	for _, ti := range inst.Lane(LaneScan).Tabs() {
		if ti.Opener == applyTab {
			t.Fatal("popup of an apply tab appeared in the scan lane")
		}
	}
}

func TestE2E_Browser_PopupFlood(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-flood")
	navigate(t, inst, LaneApply, tab, fx.Loopback("/geo"))
	t.Cleanup(func() { inst.Lane(LaneApply).CloseOwned(context.Background(), "run-flood") })
	since := time.Now()
	// Each call is its own user gesture, so Chrome's own blocker lets them through.
	errs := make([]error, 10)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = tryGesture(inst, LaneApply, tab, `void window.open('about:blank#`+string(rune('a'+i))+`')`)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		ok(t, err)
	}
	// The budget is spent at the fifth popup of the window, so one denial shows
	// the flood went past it.
	eventually(t, 5*time.Second, "a popup denied with the reason popup-budget", func() bool {
		return hasEventSince(inst, EventPopupClosed, "popup-budget", since)
	})
	listed := 0
	for _, ti := range inst.Lane(LaneApply).Tabs() {
		if ti.Opener == tab {
			listed++
		}
	}
	if listed > popupBudget {
		t.Fatalf("%d popups of the tab are listed, want at most the budget of %d", listed, popupBudget)
	}
	// A denied popup is closed in Chrome, not only left out of the list.
	eventually(t, 5*time.Second, "at most the popup budget to remain open in Chrome", func() bool {
		infos, err := chromedp.Targets(inst.root)
		if err != nil {
			return false
		}
		open := 0
		for _, in := range infos {
			if in.Type == "page" && string(in.OpenerID) == tab {
				open++
			}
		}
		return open <= popupBudget
	})
}

func TestE2E_Browser_DialogAlreadyGoneIsSwallowed(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-gone")
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/dialog"))
	since := time.Now()
	// The alert is raised and the page is replaced before an answer can land.
	_, err := h.Evaluate(ctx30(t), tab, `setTimeout(()=>alert("gone"),0); setTimeout(()=>{location.href='about:blank'},0); 1`)
	ok(t, err)
	eventually(t, 5*time.Second, "the alert to reach the dialog handler", func() bool {
		return hasEventSince(inst, EventDialogHandled, "alert: gone", since)
	})
	navigate(t, inst, LaneApply, tab, fx.Loopback("/probe"))
	if got := eval[int](t, h, tab, "1+1"); got != 2 {
		t.Fatal("tab unusable after the dialog race")
	}
}

func TestE2E_Browser_KeptTabDialogLeftToUser(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-kept")
	navigate(t, inst, LaneApply, tab, fx.Loopback("/dialog"))
	ok(t, inst.Lane(LaneApply).Keep(tab))
	before := len(inst.Events())
	tb, _ := inst.Lane(LaneApply).tab(tab)
	if _, err := inst.session(tb); err != nil {
		t.Fatal(err)
	}
	// Raise the alert without waiting: nothing may answer it.
	go func() {
		c, cancel := context.WithTimeout(tb.ctx, 3*time.Second)
		defer cancel()
		_, _ = chromedp.Call(c, runtime.Evaluate, runtime.EvaluateParams{Expression: `alert("kept")`})
	}()
	time.Sleep(1500 * time.Millisecond)
	for _, e := range inst.Events()[before:] {
		if e.Kind == EventDialogHandled {
			t.Fatalf("dialog on a kept tab was handled: %+v", e)
		}
	}
	// Answer it ourselves so the page can close.
	c, cancel := context.WithTimeout(tb.ctx, 3*time.Second)
	defer cancel()
	if _, err := chromedp.Call(c, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: true}); err != nil {
		t.Fatalf("the kept tab's alert was not waiting for an answer: %v", err)
	}
}

func TestE2E_Browser_DialogPolicy(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-dialog")
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/dialog"))
	since := time.Now()
	if got := eval[bool](t, h, tab, `confirm("Leave?")`); got {
		t.Fatal("confirm returned true, want false (dismissed)")
	}
	if got := eval[any](t, h, tab, `prompt("name?")`); got != nil {
		t.Fatalf("prompt returned %v, want null", got)
	}
	eval[any](t, h, tab, `void alert("hi")`)
	for _, typ := range []string{"confirm: Leave?", "prompt: name?", "alert: hi"} {
		if !hasEventSince(inst, EventDialogHandled, typ, since) {
			t.Errorf("no dialog event %q", typ)
		}
	}
	// The handler gives each answer one second; an answer that took longer is
	// given up on and recorded.
	if hasEventSince(inst, "dialog_answer_failed", "", since) {
		t.Fatal("a dialog was not answered within the handler's one second")
	}
}

// A popup is needed here, so the test sits with the others that raise Chrome.
func TestSpike_H5_NoTargetLeftPausedForTheDebugger(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "spike-h5")
	sess := tabSession(t, inst, LaneScan, tab)
	var mu sync.Mutex
	var paused []string
	var attached int
	watch(sess, target.AttachedToTarget, func(ev target.EventAttachedToTarget) {
		mu.Lock()
		defer mu.Unlock()
		attached++
		if ev.WaitingForDebugger {
			paused = append(paused, ev.TargetInfo.URL)
		}
	})
	navigate(t, inst, LaneScan, tab, fx.A("/outer"))
	// A popup, an out-of-process iframe, and a tab of the other lane at once.
	apply, octx := inst.Lane(LaneApply), ctx30(t)
	t.Cleanup(func() { apply.CloseOwned(context.Background(), "spike-h5") })
	var otherErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		other, err := apply.OpenTab(octx, "spike-h5")
		if err != nil {
			otherErr = err
			return
		}
		r, err := apply.Navigate(octx, other, fx.B("/ran"))
		if err == nil && r.Error != "" {
			err = errors.New("load failed: " + r.Error)
		}
		otherErr = err
	}()
	nativeOpen(t, inst, LaneScan, tab, fx.B("/ran"))
	wg.Wait()
	ok(t, otherErr)
	deadline := time.Now().Add(2 * time.Second)
	var ran int
	for time.Now().Before(deadline) {
		ran = 0
		infos, _ := chromedp.Targets(inst.root)
		for _, in := range infos {
			if (in.Type == "page" || in.Type == "iframe") && strings.HasPrefix(in.URL, fx.B("/ran")) {
				c, cancel := chromedp.NewContext(inst.root, chromedp.WithTargetID(in.TargetID), chromedp.WithDetachOnCancel())
				if attachWithin(context.Background(), c, 2*time.Second) == nil {
					cc, cc2 := context.WithTimeout(c, 2*time.Second)
					if v, err := chromedp.Run(cc, chromedp.Evaluate[bool](`window.__ran===true`)); err == nil && v {
						ran++
					}
					cc2()
				}
				cancel()
			}
		}
		if ran >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ran < 2 {
		t.Fatalf("only %d of the new targets ran their first script within 2s", ran)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paused) != 0 {
		t.Fatalf("targets attached while waiting for the debugger: %v", paused)
	}
	t.Logf("auto-attached sessions seen: %d, paused: 0, new targets that ran their first script: %d", attached, ran)
	inst.Lane(LaneScan).CloseOwned(context.Background(), "spike-h5")
}

// nativeOpen opens a window from an isolated world, which the scan-lane stubs
// in the page's main world do not cover.
func nativeOpen(t *testing.T, inst *Instance, lane Lane, id, url string) {
	t.Helper()
	sess := tabSession(t, inst, lane, id)
	world, _ := isolatedWorld(t, sess, "applysmith-native-open")
	call(t, sess, runtime.Evaluate, runtime.EvaluateParams{Expression: `void window.open(` + jsonString(url) + `,'_blank','noopener')`, ContextID: world, UserGesture: ptr(true)})
}
