//go:build browser

package browser

import (
	"strings"
	"testing"
	"time"
)

// A request the page made before the observation was switched on is never in
// the record: the submit stage turns the observation on right before its press,
// so a page-load request that failed for a signed-out probe is not evidence.
func TestE2E_ResponseWatch_RequestsBeforeTheWatchAreNotObserved(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-watch-order")
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/submit"))
	if st := eval[int](t, h, tab, `window.send('/api/refuse')`); st != 422 {
		t.Fatalf("pre-watch request status %d", st)
	}
	w, err := h.WatchResponses(ctx30(t), tab)
	ok(t, err)
	t.Cleanup(w.Stop)
	if st := eval[int](t, h, tab, `window.send('/api/apply')`); st != 200 {
		t.Fatalf("watched request status %d", st)
	}
	eventually(t, 5*time.Second, "the answer to the watched write", func() bool {
		for _, r := range w.Responses() {
			if strings.HasSuffix(r.URL, "/api/apply") && r.Status == 200 {
				return true
			}
		}
		return false
	})
	for _, r := range w.Responses() {
		if strings.HasSuffix(r.URL, "/api/refuse") {
			t.Fatalf("the record holds a request made before the watch began: %+v", r)
		}
	}
}

func TestBrowser_NavigateDOMPreservesActiveWatch(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "review-dom-watch")
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/submit"))
	w, err := h.WatchResponses(ctx30(t), tab)
	ok(t, err)
	defer w.Stop()
	res, err := h.NavigateDOM(ctx30(t), tab, fx.Loopback("/submit?afterdom=1"))
	ok(t, err)
	if res.Status != 200 {
		t.Fatalf("navigation=%+v", res)
	}
	if st := eval[int](t, h, tab, `window.send('/api/apply')`); st != 200 {
		t.Fatalf("request status=%d", st)
	}
	eventually(t, 5*time.Second, "the write after DOM-ready navigation", func() bool {
		for _, r := range w.Responses() {
			if strings.HasSuffix(r.URL, "/api/apply") && r.Status == 200 {
				return true
			}
		}
		return false
	})
}

func TestBrowser_NavigateDOMSchemeAndSameDocument(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "review-dom-scheme")
	h := inst.Lane(LaneScan)
	raw := strings.Replace(fx.Loopback("/submit"), "http:", "HTTP:", 1)
	res, err := h.NavigateDOM(ctx30(t), tab, raw)
	ok(t, err)
	if res.Status != 200 {
		t.Errorf("allowed uppercase HTTP scheme lost main-document status: %+v", res)
	}
	res, err = h.NavigateDOM(ctx30(t), tab, fx.Loopback("/submit")+"#same-document")
	ok(t, err)
	if res.Status != 0 {
		t.Errorf("same-document status=%+v", res)
	}
	res, err = h.NavigateDOM(ctx30(t), tab, "about:blank")
	ok(t, err)
	if res.Status != 0 {
		t.Errorf("non-HTTP status=%+v", res)
	}
}

func TestE2E_ResponseWatch_StopLeavesOtherWatchActive(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "shared-network-observation")
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/submit"))
	first, err := h.WatchResponses(ctx30(t), tab)
	ok(t, err)
	defer first.Stop()
	second, err := h.WatchResponses(ctx30(t), tab)
	ok(t, err)
	defer second.Stop()
	first.Stop()
	first.Stop()
	// Full-load navigation also releases only its own observation.
	_, err = h.Navigate(ctx30(t), tab, fx.Loopback("/submit?secondwatch=1"))
	ok(t, err)
	if st := eval[int](t, h, tab, `window.send('/api/apply')`); st != 200 {
		t.Fatalf("request status=%d", st)
	}
	eventually(t, 5*time.Second, "the remaining watch's write response", func() bool {
		for _, r := range second.Responses() {
			if strings.HasSuffix(r.URL, "/api/apply") && r.Status == 200 && r.Body != "" {
				return true
			}
		}
		return false
	})
	if len(first.Responses()) != 0 {
		t.Fatal("stopped watch retained responses")
	}
}
