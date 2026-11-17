//go:build browser

package browser

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// A click on an aggregator's apply button opens the employer posting in a new
// tab: the capture returns its address and closes that tab, and leaves a tab
// the user opened meanwhile alone.
func TestE2E_Browser_PopupURLCapture(t *testing.T) {
	inst, fx := sharedInstance(t)

	apply := openTab(t, inst, LaneApply, "run-popup-capture")
	ha := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, apply, fx.Loopback("/opener"))
	var user target.ID
	url, err := ha.CapturePopup(ctx30(t), apply, func(ctx context.Context) error {
		res, err := chromedp.CallBrowser(inst.root, target.CreateTarget, target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(false), Focus: ptr(false)})
		if err != nil {
			return err
		}
		user = res.TargetID
		gesture(t, inst, LaneApply, apply, `document.getElementById('b').click()`)
		return nil
	})
	ok(t, err)
	t.Cleanup(func() {
		_, _ = chromedp.CallBrowser(inst.root, target.CloseTarget, target.CloseTargetParams{TargetID: user})
	})
	if url != fx.B("/ran") {
		t.Fatalf("captured %q, want %q", url, fx.B("/ran"))
	}
	eventually(t, 5*time.Second, "the popup closed", func() bool {
		for _, ti := range ha.Tabs() {
			if ti.Opener == apply {
				return false
			}
		}
		return true
	})
	pages, err := inst.listTargets(ctx30(t))
	ok(t, err)
	if _, there := pages[user]; !there {
		t.Fatal("a tab the user opened was reported or closed")
	}

	// In the scan lane the page asks for a window and gets none; the address is
	// the one recorded and no tab is made.
	scan := openTab(t, inst, LaneScan, "run-popup-capture")
	hs := inst.Lane(LaneScan)
	navigate(t, inst, LaneScan, scan, fx.Loopback("/opener"))
	tabs := len(hs.Tabs()) + len(ha.Tabs())
	got, err := hs.CapturePopup(ctx30(t), scan, func(context.Context) error {
		eval[any](t, hs, scan, `document.getElementById('b').click()`)
		return nil
	})
	ok(t, err)
	if got != fx.B("/ran") {
		t.Fatalf("scan capture %q", got)
	}
	if now := len(hs.Tabs()) + len(ha.Tabs()); now != tabs {
		t.Fatalf("a tab was created: %d -> %d", tabs, now)
	}
	// A relative argument is recorded as the address the window would load.
	rel, err := hs.CapturePopup(ctx30(t), scan, func(context.Context) error {
		eval[any](t, hs, scan, `void window.open('ran?from=opener')`)
		return nil
	})
	if err != nil || rel != fx.Loopback("/ran?from=opener") {
		t.Fatalf("a relative popup address was recorded as %q (%v)", rel, err)
	}
	// An action that opens nothing returns no address.
	none, err := hs.CapturePopup(ctx30(t), scan, func(context.Context) error { return nil })
	if err != nil || none != "" {
		t.Fatalf("%q %v", none, err)
	}
}
