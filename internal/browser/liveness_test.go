package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
)

// A DevTools failure while the browser and the tab are both still there is not
// a lost browser (flow round 10, EF-1). V8 words a navigation under the call as
// "Execution context was destroyed." (chromedp's own polling test meets it in
// turn with "Inspected target navigated or closed") or, for a context already
// gone, "Cannot find context with specified id"; any other answer is the
// operation's own failure.
func TestBrowser_WrapOp_ALiveBrowsersFailureIsNotALostBrowser(t *testing.T) {
	i, h, _ := bareLane(t, LaneScan, 1)
	for _, c := range []struct {
		msg  string
		want Code
	}{
		{"Execution context was destroyed.", CodeDocumentReplaced},
		{"Cannot find context with specified id", CodeDocumentReplaced},
		{"Object reference chain is too long", CodeOperationFailed},
	} {
		err := h.wrapOp(i.tabs["tab-00"], &cdproto.Error{Code: -32000, Message: c.msg}, "script evaluation", evalDeadline)
		if code, _ := CodeOf(err); code != c.want {
			t.Errorf("%q: code %q (%v), want %s", c.msg, code, err, c.want)
		}
	}
}

// The hooks a fill runs on name a failure as wrapOp does: one no code names,
// from a browser that still answers, is operation-failed, which a fill counts
// as its own defect; the same failure once the browser is gone stays
// browser-disconnected, the machine's (flow round 10).
func TestBrowser_Hooks_ALiveBrowsersFailureIsNotALostBrowser(t *testing.T) {
	i, _, _ := bareLane(t, LaneApply, 1)
	hk := &Hooks{inst: i, tab: i.tabs["tab-00"], ctx: context.Background()}
	fail := &cdproto.Error{Code: -32000, Message: "Object reference chain is too long"}
	if code, _ := CodeOf(hk.mapErr(context.Background(), fail)); code != CodeOperationFailed {
		t.Errorf("a live browser: code %q", code)
	}
	i.mu.Lock()
	i.dead = &exitInfo{Cause: "process ended"}
	i.mu.Unlock()
	if code, _ := CodeOf(hk.mapErr(context.Background(), fail)); code != CodeBrowserDisconnected {
		t.Errorf("a browser that is gone: code %q", code)
	}
}

// A caller that gives up while the connection stands says nothing about the
// browser: the operation fails as its own, and the lost-browser check (a
// process listing and a request to the endpoint) is not run for it.
func TestBrowser_WrapOp_ACancelledOperationDoesNotAskWhetherTheBrowserIsGone(t *testing.T) {
	i, h, _ := bareLane(t, LaneScan, 1)
	gone := make(chan struct{})
	close(gone)
	i.proc = &Process{PID: deadPID(t), done: gone}
	err := h.wrapOp(i.tabs["tab-00"], context.Canceled, "script evaluation", evalDeadline)
	if code, _ := CodeOf(err); code != CodeOperationFailed || i.deadInfo() != nil {
		t.Fatalf("code %q, lost-browser check ran: %v", code, i.deadInfo() != nil)
	}
}

// scriptedBindings swaps Runtime.addBinding for one that records what was sent
// and fails while *fail is set.
func scriptedBindings(t *testing.T, fail *bool) *[]string {
	t.Helper()
	var sent []string
	old := addBinding
	addBinding = func(_ context.Context, p runtime.AddBindingParams) error {
		sent = append(sent, "add "+p.Name+" "+p.ExecutionContextName)
		if fail != nil && *fail {
			return context.DeadlineExceeded
		}
		return nil
	}
	t.Cleanup(func() { addBinding = old })
	return &sent
}

func bareHooks(t *testing.T) (*Hooks, *hookSession) {
	t.Helper()
	i, _, _ := bareLane(t, LaneApply, 1)
	top := &hookSession{ctx: context.Background(), root: "top", byCtx: map[runtime.ExecutionContextID]*hookWorld{},
		defaults: map[string]runtime.ExecutionContextID{}, unique: map[string]runtime.ExecutionContextID{}}
	return &Hooks{inst: i, tab: i.tabs["tab-00"], ctx: context.Background(), top: top,
		sessions: map[target.ID]*hookSession{}, bindings: map[string]*hookBinding{}}, top
}

// A binding call is answered with a timeout at the bound, and what the handler
// returns afterwards goes nowhere.
func TestHooks_ABindingCallTimesOutAtTheBoundAndItsLateReplyIsDropped(t *testing.T) {
	bindingTimeout = 20 * time.Millisecond
	t.Cleanup(func() { bindingTimeout = DefaultBindingTimeout })
	hk, _ := bareHooks(t)
	release, returned := make(chan struct{}), make(chan struct{})
	slow := &hookBinding{handler: func(ctx context.Context, _ BindingCall) (string, error) {
		defer close(returned)
		<-ctx.Done() // the call's context ends at the bound
		<-release
		return "late", nil
	}}
	reply, err := hk.serve(slow, &hookWorld{frame: "top"}, "x")
	if err == nil || !strings.Contains(err.Error(), "binding timeout") || reply != "" {
		t.Fatalf("reply %q err %v, want a binding timeout", reply, err)
	}
	close(release)
	<-returned
	fast := &hookBinding{handler: func(context.Context, BindingCall) (string, error) { return "ok", nil }}
	if reply, err := hk.serve(fast, &hookWorld{frame: "top"}, "x"); err != nil || reply != "ok" {
		t.Fatalf("a call within the bound: %q %v", reply, err)
	}
}

// Learning a child frame's main world switches context reporting off and on,
// and switching it off makes the browser drop the session's bindings: every
// document made afterwards would lack them, and nothing would say so. Each
// binding of the session is therefore registered afresh, removed first because
// the browser answers a name it still knows without doing anything.
func TestHooks_BindingsAreRegisteredAfreshAfterContextsAreAnnouncedAgain(t *testing.T) {
	hk, top := bareHooks(t)
	sent := scriptedBindings(t, nil)
	oldAnnounce, oldRemove := announceContexts, removeBinding
	t.Cleanup(func() { announceContexts, removeBinding = oldAnnounce, oldRemove })
	announceContexts = func(context.Context) error {
		*sent = append(*sent, "announce")
		top.onContextCreated(runtime.EventExecutionContextCreated{Context: &runtime.ExecutionContextDescription{
			ID: 7, UniqueID: "u7", AuxData: []byte(`{"frameId":"child","isDefault":true}`)}})
		return nil
	}
	removeBinding = func(_ context.Context, name string) error { *sent = append(*sent, "remove "+name); return nil }
	answer := func(context.Context, BindingCall) (string, error) { return "", nil }
	if err := hk.AddBinding("w", "fill", answer); err != nil {
		t.Fatal(err)
	}
	popup := "aj_popup"
	hk.tab.popupBinding.Store(&popup)
	id, err := hk.mainContext(context.Background(), top, "child")
	if err != nil || id != 7 {
		t.Fatalf("context %d, %v", id, err)
	}
	want := []string{"add fill$n w", "announce", "remove fill$n", "add fill$n w", "remove aj_popup", "add aj_popup "}
	if fmt.Sprint(*sent) != fmt.Sprint(want) {
		t.Fatalf("sent %v\nwant %v", *sent, want)
	}
	// A frame whose context is already known asks the browser nothing.
	if _, err := hk.mainContext(context.Background(), top, "child"); err != nil || len(*sent) != len(want) {
		t.Fatalf("a known context: %v, sent %v", err, *sent)
	}
}

// A registration whose Runtime.addBinding failed (a busy renderer) leaves no
// entry behind: the next one sends the command again, where a kept entry
// answered it as a duplicate and the page never got the binding.
func TestHooks_ABindingThatCouldNotBeAddedIsAddedByTheNextRegistration(t *testing.T) {
	hk, _ := bareHooks(t)
	fail := true
	sent := scriptedBindings(t, &fail)
	answer := func(context.Context, BindingCall) (string, error) { return "", nil }
	if err := hk.AddBinding("w", "fill", answer); err == nil {
		t.Fatal("a registration the browser did not take reported success")
	}
	fail = false
	if err := hk.AddBinding("w", "fill", answer); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 2 {
		t.Fatalf("sent %v: the second registration did not add the binding", *sent)
	}
}

// bareLane is a lane handle over an instance with a scripted browser.
func bareLane(t *testing.T, lane Lane, n int) (*Instance, *LaneHandle, *clock) {
	t.Helper()
	i, clk := bareInstance(t)
	for k := range n {
		id := target.ID(fmt.Sprintf("tab-%02d", k))
		i.tabs[id] = &tab{id: id, lane: lane, created: clk.now().Add(-time.Hour)}
	}
	return i, i.Lane(lane), clk
}

func TestBrowser_Close_BoundedFiveSeconds(t *testing.T) {
	old := closeBound
	closeBound = 200 * time.Millisecond
	t.Cleanup(func() { closeBound = old })
	i, _, _ := bareLane(t, LaneScan, 1)
	var attempts atomic.Int64
	i.closeFn = func(ctx context.Context, id target.ID) error { attempts.Add(1); <-ctx.Done(); return ctx.Err() }
	tb := i.tabs["tab-00"]
	start := time.Now()
	i.dropTab(context.Background(), tb, "run-ended")
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a stuck close held the caller for %v", d)
	}
	if !tb.closed.Load() {
		t.Fatal("the page is still listed after a stuck close")
	}
	deadline := time.Now().Add(5 * time.Second)
	for attempts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatalf("%d close attempts: the stuck close was not retried in the background", attempts.Load())
	}
}

// A page the instance closes on its own account (a refused popup, a crashed
// tab) is tried again when the browser did not close it, and a run's popup
// whose window could not be read is closed instead of left open and unlisted.
func TestBrowser_ClosingAPageOnTheInstancesAccountIsTriedAgain(t *testing.T) {
	old := closeBound
	closeBound = 40 * time.Millisecond
	t.Cleanup(func() { closeBound = old })
	i, _, _ := bareLane(t, LaneApply, 1)
	i.tabs["tab-00"].owner = "run-1"
	closed := make(chan target.ID, 8)
	var attempts atomic.Int64
	i.closeFn = func(_ context.Context, id target.ID) error {
		closed <- id
		if attempts.Add(1) == 1 {
			return errors.New("the browser did not answer")
		}
		return nil
	}
	i.closeTarget("tab-00", "tab-crashed")
	if i.lookup("tab-00") != nil {
		t.Fatal("the page is still listed")
	}
	for range 2 {
		select {
		case id := <-closed:
			if id != "tab-00" {
				t.Fatalf("closed %s", id)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%d close attempts: a close the browser did not perform was not tried again", attempts.Load())
		}
	}
	// The bare instance has no browser, so reading the popup's window fails.
	i.tabs["tab-00"] = &tab{id: "tab-00", lane: LaneApply, owner: "run-1"}
	i.onPageCreated(&target.Info{TargetID: "popup", Type: "page", OpenerID: "tab-00", URL: "https://jobs.example.test/p"})
	select {
	case id := <-closed:
		if id != "popup" {
			t.Fatalf("closed %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a run's popup whose window could not be read was left open")
	}
	i.tabs["tab-00"].kept.Store(true)
	i.onPageCreated(&target.Info{TargetID: "users", Type: "page", OpenerID: "tab-00", URL: "https://jobs.example.test/p"})
	select {
	case id := <-closed:
		t.Fatalf("closed %s, a window opened from a tab kept for the user", id)
	case <-time.After(100 * time.Millisecond):
	}
}
