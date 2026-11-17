//go:build browser

package browser

import (
	"context"
	"errors"
	"testing"
	"time"
)

func requested(fx *fixtures, path string) bool { return len(fx.requests(path)) > 0 }

func TestE2E_Browser_ScanResourcePolicy(t *testing.T) {
	inst, fx := sharedInstance(t)
	scan := openTab(t, inst, LaneScan, "run-policy")
	navigate(t, inst, LaneScan, scan, fx.Loopback("/policy/scan"))
	eventually(t, 5*time.Second, "the scripts and the stylesheet", func() bool {
		return requested(fx, "/app-scan.js") && requested(fx, "/s-scan.css")
	})
	time.Sleep(500 * time.Millisecond)
	if requested(fx, "/pic-scan.png") || requested(fx, "/t-scan.js") {
		t.Fatalf("the scan lane loaded an image (%v) or a tracker script (%v)", requested(fx, "/pic-scan.png"), requested(fx, "/t-scan.js"))
	}
	h := inst.Lane(LaneScan)
	if got := eval[bool](t, h, scan, `document.getElementById('pic').complete && document.getElementById('pic').naturalWidth === 0`); !got {
		t.Fatal("the image did not fail")
	}
	if got := eval[int](t, h, scan, `window.__ran || 0`); got != 1 {
		t.Fatal("the page's own script did not run")
	}

	// The apply lane loads everything.
	apply := openTab(t, inst, LaneApply, "run-policy")
	navigate(t, inst, LaneApply, apply, fx.Loopback("/policy/apply"))
	eventually(t, 5*time.Second, "the apply lane's image and tracker script", func() bool {
		return requested(fx, "/pic-apply.png") && requested(fx, "/t-apply.js")
	})

	// A scan tab opened with the load-everything option blocks nothing.
	id, err := h.OpenTabWith(ctx30(t), "run-policy", TabOptions{LoadEverything: true})
	ok(t, err)
	t.Cleanup(func() { _ = h.CloseTab(context.Background(), id) })
	navigate(t, inst, LaneScan, id, fx.Loopback("/policy/all"))
	eventually(t, 5*time.Second, "everything on the load-everything tab", func() bool {
		return requested(fx, "/pic-all.png") && requested(fx, "/t-all.js")
	})
}

// A scan tab whose policy cannot be installed, or whose installation hangs, is
// closed and never handed out.
func TestE2E_Browser_PolicyInstallFailureClosesTab(t *testing.T) {
	inst, _ := sharedInstance(t)
	h := inst.Lane(LaneScan)
	before := len(h.Tabs())
	policyHook = func(context.Context) error { return errors.New("fetch domain refused") }
	t.Cleanup(func() { policyHook = nil })
	if _, err := h.OpenTab(ctx30(t), "run-policy-fail"); err == nil {
		t.Fatal("a tab without its policy was handed out")
	}
	oldDeadline := setupDeadline
	setupDeadline = 500 * time.Millisecond
	t.Cleanup(func() { setupDeadline = oldDeadline })
	policyHook = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	if _, err := h.OpenTab(ctx30(t), "run-policy-hang"); err == nil {
		t.Fatal("a tab whose policy hung was handed out")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the hang held the caller for %v", d)
	}
	policyHook = nil
	eventually(t, 5*time.Second, "the failed tabs gone", func() bool { return len(h.Tabs()) == before })
}
