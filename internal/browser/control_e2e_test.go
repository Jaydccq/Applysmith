//go:build browser

package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// controlRig is a Control over its own throwaway headless Chrome, stopped when
// the test ends.
func controlRig(t *testing.T) (*Control, *fixtures) {
	t.Helper()
	root, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	fx := newFixtures()
	c := NewControl(testOptions(t, root))
	t.Cleanup(func() {
		if inst := c.running(); inst != nil {
			inst.Stop(context.Background())
		}
		fx.close()
		_ = os.RemoveAll(root)
	})
	return c, fx
}

func TestE2E_Control_StatusScreenshotAndStop(t *testing.T) {
	c, fx := controlRig(t)
	ctx := t.Context() // each operation has its own bound; the steps together take longer on a busy machine

	if st := c.Status(ctx); st.State != InstanceStopped || st.Apply.Window || len(st.Apply.Tabs) != 0 || c.running() != nil {
		t.Fatalf("a status read starts nothing: %+v", st)
	}

	ok(t, c.Show(ctx, ""))
	inst := c.running()
	if inst == nil {
		t.Fatal("show did not start the browser")
	}
	st := c.Status(ctx)
	if st.State != InstanceRunning || st.Version == "" || !st.Apply.Window || len(st.Apply.Tabs) != 1 || !st.Apply.Tabs[0].Kept || !st.Apply.Tabs[0].Active {
		t.Fatalf("show opened a blank tab for the user: %+v", st)
	}

	lease, err := inst.Acquire(LaneApply, "run-1")
	ok(t, err)
	run := lease.Lane()
	id, err := run.OpenTab(ctx, "run-1")
	ok(t, err)
	navigate(t, inst, LaneApply, id, fx.A("/render?token=abc123#step2"))
	st = c.Status(ctx)
	var mine *TabStatus
	for i, tb := range st.Apply.Tabs {
		if tb.ID == id {
			mine = &st.Apply.Tabs[i]
		}
	}
	if mine == nil || mine.Title != "render" || mine.Owner != "run-1" || mine.Kept {
		t.Fatalf("the run's tab: %+v", st.Apply.Tabs)
	}
	if strings.Contains(mine.URL, "token") || strings.Contains(mine.URL, "step2") || !strings.HasSuffix(mine.URL, "/render") {
		t.Fatalf("a query or fragment reached the status: %q", mine.URL)
	}

	first, err := c.Capture(ctx, LaneApply, id, ShotOptions{Width: 400})
	ok(t, err)
	img, err := jpeg.Decode(bytes.NewReader(first.Data))
	if err != nil || first.ContentType != "image/jpeg" || img.Bounds().Dx() > 400 || img.Bounds().Dx() < 100 {
		t.Fatalf("a jpeg at most 400 wide: %v %s %v", err, first.ContentType, img)
	}
	again, err := c.Capture(ctx, LaneApply, id, ShotOptions{Width: 400})
	ok(t, err)
	if !bytes.Equal(first.Data, again.Data) {
		t.Error("a second capture within a second is the cached image")
	}
	png, err := c.Capture(ctx, LaneApply, id, ShotOptions{PNG: true})
	ok(t, err)
	if png.ContentType != "image/png" || !bytes.HasPrefix(png.Data, []byte("\x89PNG")) {
		t.Fatalf("png: %s", png.ContentType)
	}

	kept := st.Apply.Tabs[0].ID
	if _, err := c.Capture(ctx, LaneApply, kept, ShotOptions{}); err != nil {
		t.Fatalf("a tab kept for the user is captured without a lane session: %v", err)
	}
	if _, err := c.Capture(ctx, LaneApply, "no-such-tab", ShotOptions{}); !errorIs(err, CodeTabClosed) {
		t.Fatalf("unknown tab: %v", err)
	}
	if _, err := c.Capture(ctx, LaneScan, id, ShotOptions{}); !errorIs(err, CodeWrongLane) {
		t.Fatalf("an apply tab asked for as a scan tab: %v", err)
	}

	if _, err := c.Stop(ctx, false); !errorIs(err, CodeLaneLeased) {
		t.Fatalf("stop while a run holds a lane: %v", err)
	}
	if c.running() == nil {
		t.Fatal("the refused stop stopped the browser")
	}
	out, err := c.Stop(ctx, true)
	ok(t, err)
	if out != StopClosed && out != StopKilled {
		t.Fatalf("outcome %q", out)
	}
	if st := c.Status(ctx); st.State != InstanceStopped {
		t.Fatalf("after the stop: %+v", st)
	}
	if out, err := c.Stop(ctx, false); err != nil || out != "" {
		t.Fatalf("stopping a stopped browser: %q %v", out, err)
	}
}

// liveRecorder runs a live view in the background and hands its events over.
type liveRecorder struct {
	events chan LiveEvent
	done   chan error
	stop   context.CancelFunc
}

func startLive(t *testing.T, c *Control, tabID string) *liveRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &liveRecorder{events: make(chan LiveEvent, 256), done: make(chan error, 1), stop: cancel}
	go func() {
		r.done <- c.Live(ctx, tabID, LiveOptions{MaxWidth: 640, MaxHeight: 480}, func(ev LiveEvent) error {
			select {
			case r.events <- ev:
			default:
			}
			return nil
		})
	}()
	t.Cleanup(cancel)
	return r
}

// next waits for the first event that match accepts.
func (r *liveRecorder) next(t *testing.T, what string, match func(LiveEvent) bool) LiveEvent {
	t.Helper()
	deadline := time.After(attachDeadline + 15*time.Second) // a live view attaches to the tab first
	for {
		select {
		case ev := <-r.events:
			if match(ev) {
				return ev
			}
		case err := <-r.done:
			t.Fatalf("the live view ended before %s: %v", what, err)
		case <-deadline:
			t.Fatalf("no %s in time", what)
		}
	}
}

// A tab whose screencast never sends a frame gets a still every two seconds,
// not one that a failure would leave the panel waiting after.
func TestE2E_Control_LiveStillsUntilTheScreencastPaints(t *testing.T) {
	c, fx := controlRig(t)
	ctx := t.Context() // each operation has its own bound; the steps together take longer on a busy machine
	id, err := c.NewTab(ctx, fx.Loopback("/render"))
	ok(t, err)
	screencastMuted.Store(true)
	t.Cleanup(func() { screencastMuted.Store(false) })
	live := startLive(t, c, id)
	live.next(t, "a still", func(ev LiveEvent) bool { return ev.Frame != nil && ev.Frame.Width > 0 })
	live.next(t, "a second still", func(ev LiveEvent) bool { return ev.Frame != nil && ev.Frame.Width > 0 })
	live.stop()
	<-live.done
}

func TestE2E_Control_LiveViewFramesUserInputAndDisconnect(t *testing.T) {
	c, fx := controlRig(t)
	ctx := t.Context() // each operation has its own bound; the steps together take longer on a busy machine
	ok(t, c.Show(ctx, ""))
	inst := c.running()
	lease, err := inst.Acquire(LaneApply, "run-1")
	ok(t, err)
	run := lease.Lane()
	id, err := run.OpenTab(ctx, "run-1")
	ok(t, err)
	navigate(t, inst, LaneApply, id, fx.Loopback("/clicks"))

	live := startLive(t, c, id)
	tab := live.next(t, "the tab's state", func(ev LiveEvent) bool { return ev.Tab != nil }).Tab
	if !tab.Driven || tab.RunID != "run-1" || !strings.HasSuffix(tab.URL, "/clicks") || tab.Title != "clicks" {
		t.Fatalf("a run's tab: %+v", tab)
	}
	frame := live.next(t, "a frame", func(ev LiveEvent) bool { return ev.Frame != nil }).Frame
	img, err := jpeg.Decode(bytes.NewReader(frame.JPEG))
	if err != nil || frame.Width <= 0 || frame.Height <= 0 || img.Bounds().Dx() > 640 || img.Bounds().Dy() > 480 {
		t.Fatalf("a jpeg within the bounds and the page's viewport: %v %+v %v", err, frame, img.Bounds())
	}

	click := []InputEvent{{Kind: "mouse", Type: "down", X: 110, Y: 80, Button: "left", Buttons: 1, ClickCount: 1}, {Kind: "mouse", Type: "up", X: 110, Y: 80, Button: "left", ClickCount: 1}}
	if err := c.Input(ctx, id, click); !errorIs(err, CodeTabDriven) {
		t.Fatalf("a run's tab took the user's input: %v", err)
	}
	if err := c.Navigate(ctx, id, NavReload, ""); !errorIs(err, CodeTabDriven) {
		t.Fatalf("a run's tab took the user's navigation: %v", err)
	}

	ok(t, run.Click(ctx, id, 110, 80))
	cur := live.next(t, "the run's cursor", func(ev LiveEvent) bool { return ev.Cursor != nil }).Cursor
	if cur[0] != 110 || cur[1] != 80 {
		t.Fatalf("cursor %v", cur)
	}

	ok(t, run.Keep(id))
	live.next(t, "the hand-over", func(ev LiveEvent) bool { return ev.Tab != nil && !ev.Tab.Driven })
	ok(t, c.Input(ctx, id, click))
	ok(t, c.Input(ctx, id, []InputEvent{
		{Kind: "mouse", Type: "down", X: 20, Y: 18, Button: "left", Buttons: 1, ClickCount: 1},
		{Kind: "mouse", Type: "up", X: 20, Y: 18, Button: "left", ClickCount: 1},
		{Kind: "text", Text: "\u4f60"},
		{Kind: "key", Type: "down", Key: "a", Code: "KeyA", KeyCode: 65, Text: "a"},
		{Kind: "key", Type: "up", Key: "a", Code: "KeyA", KeyCode: 65},
		{Kind: "text", Text: "b"},
		{Kind: "key", Type: "down", Key: "Backspace", Code: "Backspace", KeyCode: 8},
	}))
	ok(t, run.Reclaim(id))
	raw, err := run.Evaluate(ctx, id, `JSON.stringify({e: window.__events, v: document.getElementById('t').value})`)
	ok(t, err)
	var text string
	ok(t, json.Unmarshal(raw, &text))
	var page struct {
		E []struct {
			Click, Trusted bool
			Key            string
		}
		V string
	}
	ok(t, json.Unmarshal([]byte(text), &page))
	clicks, keys := 0, 0
	for _, e := range page.E {
		if !e.Trusted {
			t.Errorf("an untrusted event reached the page: %+v", e)
		}
		if e.Click {
			clicks++
		}
		if e.Key == "a" {
			keys++
		}
	}
	// The run's click and the user's.
	if clicks != 2 || keys != 1 || page.V != "\u4f60a" {
		t.Fatalf("what the page saw: %s", text)
	}
	ok(t, run.Keep(id))

	live.stop()
	select {
	case <-live.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the live view did not end when its reader left")
	}
	inst.mu.Lock()
	tb := inst.tabs[target.ID(id)]
	inst.mu.Unlock()
	if tb == nil || tb.view.Load() != nil {
		t.Fatal("the viewer session outlived its last live view")
	}

	ok(t, c.Navigate(ctx, id, NavGo, fx.Loopback("/render")))
	again := startLive(t, c, id)
	again.next(t, "the new page", func(ev LiveEvent) bool { return ev.Tab != nil && strings.HasSuffix(ev.Tab.URL, "/render") })
	if err := c.Navigate(ctx, id, NavGo, "file:///etc/hosts"); !errorIs(err, CodeSchemeNotAllowed) {
		t.Fatalf("a file address: %v", err)
	}
	ok(t, inst.Lane(LaneApply).CloseTab(ctx, id))
	end := again.next(t, "the end", func(ev LiveEvent) bool { return ev.End != "" })
	if end.End != LiveEndTabClosed {
		t.Fatalf("end %q", end.End)
	}

	if err := c.Live(ctx, "no-such-tab", LiveOptions{}, func(LiveEvent) error { return nil }); !errorIs(err, CodeTabClosed) {
		t.Fatalf("unknown tab: %v", err)
	}
	mine, err := c.NewTab(ctx, fx.Loopback("/render"))
	ok(t, err)
	opened := startLive(t, c, mine)
	if tab := opened.next(t, "the new tab", func(ev LiveEvent) bool { return ev.Tab != nil && strings.HasSuffix(ev.Tab.URL, "/render") }).Tab; tab.Driven {
		t.Fatalf("a tab the user opened: %+v", tab)
	}
	scan, err := inst.Lane(LaneScan).OpenTab(ctx, "")
	ok(t, err)
	if err := c.Input(ctx, scan, click); !errorIs(err, CodeWrongLane) {
		t.Fatalf("a scan tab: %v", err)
	}

	// Fit: only while no lease holds the apply lane, undone when a run takes
	// it. Headless keeps a tab hidden at a stale size until it is activated.
	_, err = chromedp.CallBrowser(inst.root, target.ActivateTarget, target.ActivateTargetParams{TargetID: target.ID(mine)})
	ok(t, err)
	mt, _ := inst.Lane(LaneApply).tab(mine)
	v, rel, err := inst.openViewer(mt)
	ok(t, err)
	defer rel()
	size := func(fit bool) (wh [2]float64) {
		if fit {
			ok(t, c.Input(ctx, mine, []InputEvent{{Kind: "fit", X: 500, Y: 700}}))
		}
		time.Sleep(300 * time.Millisecond)
		wh[0], wh[1], err = inst.viewportSize(ctx, v.ctx)
		return
	}
	own := size(false)
	held := size(true)
	lease.Release()
	fitted := size(true)
	_, err = inst.Acquire(LaneApply, "run-2")
	if back := size(false); held != own || fitted != [2]float64{500, 700} || back != own || err != nil {
		t.Fatalf("own %v, under a lease %v, fitted %v, after a run took the lane %v: %v", own, held, fitted, back, err)
	}
}

func errorIs(err error, code Code) bool {
	got, ok := CodeOf(err)
	return ok && got == code
}
