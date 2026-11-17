//go:build browser

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
)

// Requirement: Frame-aware injection hooks.

const testWorld = "applysmith-hooks-test"

func hookTab(t *testing.T) (*LaneHandle, string, *Hooks, *fixtures) {
	t.Helper()
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "hooks")
	h := inst.Lane(LaneScan)
	hk, err := h.Hooks(ctx30(t), tab)
	ok(t, err)
	return h, tab, hk, fx
}

func hookEval[T any](t *testing.T, hk *Hooks, frame, world, expr string) T {
	t.Helper()
	raw, err := hk.Eval(ctx30(t), frame, world, expr)
	if err != nil {
		t.Fatalf("eval %.60q: %v", expr, err)
	}
	var v T
	ok(t, json.Unmarshal(raw, &v))
	return v
}

func frameWhere(t *testing.T, hk *Hooks, match func(Frame) bool) Frame {
	t.Helper()
	var found Frame
	eventually(t, 8*time.Second, "a matching frame", func() bool {
		frames, err := hk.Frames(ctx30(t))
		if err != nil {
			return false
		}
		for _, f := range frames {
			if match(f) {
				found = f
				return true
			}
		}
		return false
	})
	return found
}

func TestHooks_CrossOriginIframeAndBindingRoundTrip(t *testing.T) {
	h, tab, hk, fx := hookTab(t)
	var mu sync.Mutex
	var calls []BindingCall
	big := strings.Repeat("p", 1<<20)
	reply := strings.Repeat("r", 1<<20)
	ok(t, hk.AddBinding(testWorld, "callGo", func(_ context.Context, c BindingCall) (string, error) {
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		if c.Payload == "boom" {
			return "", fmt.Errorf("refused")
		}
		return reply, nil
	}))
	navigate(t, h.base, LaneScan, tab, fx.A("/outer"))
	top := frameWhere(t, hk, func(f Frame) bool { return f.IsTop })
	child := frameWhere(t, hk, func(f Frame) bool { return !f.IsTop && strings.HasSuffix(f.URL, "/inner") })
	wantOrigin := strings.TrimSuffix(fx.B(""), "/")
	if child.Origin != wantOrigin || child.ParentID != top.ID || strings.Contains(child.URL, "?") {
		t.Fatalf("child frame %+v (top %+v)", child, top)
	}

	// The engine-side script reads and writes the iframe's inputs.
	got := hookEval[string](t, hk, child.ID, testWorld, `(()=>{const el=document.getElementById('name');el.value='Alex Example';return location.origin+'|'+(window===window.top)+'|'+el.value})()`)
	if got != wantOrigin+"|false|Alex Example" {
		t.Fatalf("iframe world saw %q", got)
	}

	// 1 MiB each way, from the top frame and from the cross-origin frame.
	if n := hookEval[int](t, hk, top.ID, testWorld, `callGo(`+jsonString(big)+`).then(r=>r.length)`); n != len(reply) {
		t.Fatalf("top reply length %d", n)
	}
	if n := hookEval[int](t, hk, child.ID, testWorld, `callGo("hi").then(r=>r.length)`); n != len(reply) {
		t.Fatalf("frame reply length %d", n)
	}
	mu.Lock()
	seen := append([]BindingCall(nil), calls...)
	mu.Unlock()
	if len(seen) != 2 || !seen[0].Frame.IsTop || seen[0].Frame.ID != top.ID || seen[1].Frame.IsTop || seen[1].Frame.ID != child.ID || seen[1].Frame.Origin != wantOrigin || len(seen[0].Payload) != len(big) {
		t.Fatalf("handler saw %+v", seen)
	}
	// A handler error rejects the page-side promise.
	if r := hookEval[string](t, hk, top.ID, testWorld, `callGo("boom").then(()=>"resolved",e=>e.message)`); r != "refused" {
		t.Fatalf("rejection %q", r)
	}

	// Page script cannot reach the binding, in either frame.
	for _, id := range []string{top.ID, child.ID} {
		found := hookEval[[]string](t, hk, id, "", `Object.getOwnPropertyNames(globalThis).filter(n=>/callGo/.test(n))`)
		if len(found) != 0 || hookEval[string](t, hk, id, "", `typeof callGo`) != "undefined" {
			t.Fatalf("main world of %s exposes %v", id, found)
		}
	}
	mu.Lock()
	n := len(calls)
	mu.Unlock()
	if n != 3 {
		t.Fatalf("handler ran %d times", n)
	}
}

func TestHooks_FirstScriptRunsInCrossProcessFrameAndSameOriginFrameMainWorld(t *testing.T) {
	h, tab, hk, fx := hookTab(t)
	navigate(t, h.base, LaneScan, tab, fx.A("/outer"))
	start := time.Now()
	child := frameWhere(t, hk, func(f Frame) bool { return !f.IsTop })
	eventually(t, 2*time.Second, "the frame's first script to run", func() bool {
		raw, err := hk.Eval(ctx30(t), child.ID, "", `Array.isArray(window.__events)`)
		return err == nil && string(raw) == "true"
	})
	t.Logf("cross-process frame ran its first script %v after the hooks looked", time.Since(start).Round(time.Millisecond))

	// A same-process child frame has its own main world.
	navigate(t, h.base, LaneScan, tab, fx.A("/outer-same"))
	same := frameWhere(t, hk, func(f Frame) bool { return !f.IsTop && strings.HasSuffix(f.URL, "/inner") })
	if hookEval[string](t, hk, same.ID, "", `location.pathname`) != "/inner" {
		t.Fatal("main-world evaluation reached the wrong frame")
	}
}

// Squarespace: a cross-process frame whose renderer is busy (the fill engine
// working through a 2,557-school list) cannot describe its frame tree within
// the deadline. It is still in the tab: a refresh keeps it, and a call that
// names it waits for the renderer instead of failing as a gone frame.
func TestHooks_ABusyCrossProcessFrameStaysInTheTab(t *testing.T) {
	h, tab, hk, fx := hookTab(t)
	navigate(t, h.base, LaneScan, tab, fx.A("/outer"))
	child := frameWhere(t, hk, func(f Frame) bool { return !f.IsTop })
	busy := (inputDeadline + 2*time.Second).Milliseconds()
	hookEval[bool](t, hk, child.ID, "", fmt.Sprintf(`setTimeout(()=>{const end=Date.now()+%d;while(Date.now()<end){}},0),true`, busy))
	time.Sleep(200 * time.Millisecond)
	frames, err := hk.Frames(ctx30(t))
	ok(t, err)
	if !slices.ContainsFunc(frames, func(f Frame) bool { return f.ID == child.ID }) {
		t.Fatalf("the busy frame dropped out of the tab: %+v", frames)
	}
	if got := hookEval[string](t, hk, child.ID, "", `location.pathname`); got != "/inner" {
		t.Fatalf("the busy frame answered %q", got)
	}
}

func TestHooks_SetFilesInCrossOriginFrame(t *testing.T) {
	h, tab, hk, fx := hookTab(t)
	navigate(t, h.base, LaneScan, tab, fx.A("/outer"))
	child := frameWhere(t, hk, func(f Frame) bool { return !f.IsTop })
	file := filepath.Join(t.TempDir(), "resume-example.txt")
	ok(t, os.WriteFile(file, []byte("Alex Example resume"), 0o600))
	ok(t, hk.SetFiles(ctx30(t), child.ID, testWorld, `document.getElementById('file')`, []string{file}))
	eventually(t, 3*time.Second, "the change event", func() bool { return hookEval[bool](t, hk, child.ID, "", `!!window.__file`) })
	got := hookEval[struct {
		Name    string
		Size    int
		Type    string
		Trusted bool
	}](t, hk, child.ID, "", `window.__file`)
	if got.Name != "resume-example.txt" || got.Size != 19 || got.Type != "text/plain" || !got.Trusted {
		t.Fatalf("input reports %+v", got)
	}
}

func TestHooks_ReleasedWhenTheTabCloses(t *testing.T) {
	h, tab, hk, fx := hookTab(t)
	navigate(t, h.base, LaneScan, tab, fx.A("/outer"))
	top := frameWhere(t, hk, func(f Frame) bool { return f.IsTop })
	ok(t, h.CloseTab(ctx30(t), tab))
	if !hk.Released() {
		t.Fatal("hooks were not released when the tab closed")
	}
	_, err := hk.Eval(ctx30(t), top.ID, testWorld, `1`)
	if c, _ := CodeOf(err); c != CodeTabClosed {
		t.Fatalf("eval after close: %v", err)
	}
	if _, err := h.Hooks(ctx30(t), tab); err == nil {
		t.Fatal("hooks were handed out for a closed tab")
	}
}

func TestHooks_SecondRegistrationOfABindingReplacesTheHandler(t *testing.T) {
	h, tab, hk, fx := hookTab(t)
	ok(t, hk.AddBinding(testWorld, "who", func(context.Context, BindingCall) (string, error) { return "first", nil }))
	ok(t, hk.AddBinding(testWorld, "who", func(context.Context, BindingCall) (string, error) { return "second", nil }))
	navigate(t, h.base, LaneScan, tab, fx.A("/inner"))
	top := frameWhere(t, hk, func(f Frame) bool { return f.IsTop })
	if got := hookEval[string](t, hk, top.ID, testWorld, `who("x")`); got != "second" {
		t.Fatalf("the newest registration did not answer: %q", got)
	}
	err := hk.AddBinding("another-world", "who", func(context.Context, BindingCall) (string, error) { return "", nil })
	if c, _ := CodeOf(err); c != CodeBindingConflict {
		t.Fatalf("a conflicting registration: %v", err)
	}
	if got := hookEval[string](t, hk, top.ID, testWorld, `who("x")`); got != "second" {
		t.Fatalf("a refused registration changed the handler: %q", got)
	}
}

func TestHooks_FramesIsBoundedWhenTheTargetListStalls(t *testing.T) {
	h, tab, hk, fx := hookTab(t)
	navigate(t, h.base, LaneScan, tab, fx.A("/outer"))
	old := listTargets
	listTargets = func(ctx context.Context) ([]*target.Info, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { listTargets = old })
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := hk.Frames(ctx)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("Frames returned %v after %v; a stalled connection must end with the caller's deadline", err, time.Since(start))
	}
}

func TestHooks_OnTabClosedRunsOnceForClosedAndForKeptTabs(t *testing.T) {
	h, tab, hk, _ := hookTab(t)
	var closed int32
	hk.OnTabClosed(func() { atomic.AddInt32(&closed, 1) })
	ok(t, h.Keep(tab))
	if atomic.LoadInt32(&closed) != 0 {
		t.Fatal("a tab kept for the user counts as closed")
	}
	ok(t, h.CloseTab(ctx30(t), tab))
	eventually(t, 5*time.Second, "the close callback", func() bool { return atomic.LoadInt32(&closed) == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := atomic.LoadInt32(&closed); n != 1 {
		t.Fatalf("the callback ran %d times", n)
	}
	late := make(chan struct{})
	hk.OnTabClosed(func() { close(late) })
	select {
	case <-late:
	case <-time.After(time.Second):
		t.Fatal("a registration on a closed tab never ran")
	}
}
