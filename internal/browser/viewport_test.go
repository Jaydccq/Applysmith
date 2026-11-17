package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// A tab with no area is parked at a usable size before any input; one that
// stays empty fails with viewport-empty.
func TestBrowser_ZeroViewportIsResizedBeforeInput(t *testing.T) {
	old := resizeSettle
	resizeSettle = time.Millisecond
	t.Cleanup(func() { resizeSettle = old })
	i, h, _ := bareLane(t, LaneApply, 1)
	tb := i.tabs["tab-00"]
	w, hgt := 0.0, 0.0
	var resized [][2]int64
	i.viewportFn = func() (float64, float64, error) { return w, hgt, nil }
	i.resizeFn = func(rw, rh int64) error {
		resized = append(resized, [2]int64{rw, rh})
		w, hgt = float64(rw), float64(rh)
		return nil
	}
	if err := h.ensureViewport(context.Background(), tb, context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(resized) != 1 || resized[0] != [2]int64{1280, 900} {
		t.Fatalf("resized %v, want one resize to 1280 by 900", resized)
	}
	if err := h.ensureViewport(context.Background(), tb, context.Background()); err != nil || len(resized) != 1 {
		t.Fatalf("a tab with area is left alone: %v %v", err, resized)
	}
	// The area stays zero.
	w, hgt = 0, 0
	i.resizeFn = func(int64, int64) error { return nil }
	err := h.ensureViewport(context.Background(), tb, context.Background())
	if code, _ := CodeOf(err); code != CodeViewportEmpty {
		t.Fatalf("code %q err %v, want viewport-empty", code, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unexpected cause")
	}
}

// Chrome refuses a wheel without both deltas, so a straight vertical scroll
// sends its zero deltaX.
func TestBrowser_WheelSendsBothDeltas(t *testing.T) {
	raw, err := jsonv2.Marshal(wheelParams{Type: input.DispatchMouseEventTypeMouseWheel, X: 20, Y: 18, DeltaY: 40}, chromedp.DefaultMarshalOptions)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(raw); !strings.Contains(s, `"deltaX":0`) || !strings.Contains(s, `"deltaY":40`) {
		t.Fatalf("wheel params %s", s)
	}
}
