//go:build browser

package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// A timeout on a live page and a call on a page the browser closed are two
// different errors, and only the second means the tab is gone.
func TestBrowser_Tab_ClosedErrorIsDistinct(t *testing.T) {
	inst, fx := sharedInstance(t)
	h := inst.Lane(LaneScan)
	slow, gone := openTab(t, inst, LaneScan, "run-closed-a"), openTab(t, inst, LaneScan, "run-closed-b")
	navigate(t, inst, LaneScan, slow, fx.Loopback("/probe"))
	navigate(t, inst, LaneScan, gone, fx.Loopback("/probe"))

	old := evalDeadline
	evalDeadline = 400 * time.Millisecond
	_, err := h.Evaluate(ctx30(t), slow, `new Promise(() => {})`)
	evalDeadline = old
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeOperationTimeout || typed.Op != "script evaluation" || typed.Bound != 400*time.Millisecond {
		t.Fatalf("a timeout on a live page: %v", err)
	}
	if got := eval[int](t, h, slow, "1+1"); got != 2 {
		t.Fatal("the tab did not survive the timed-out evaluation")
	}

	// The browser closes the page underneath; no close() of ours ever runs.
	_, err = chromedp.CallBrowser(inst.root, target.CloseTarget, target.CloseTargetParams{TargetID: target.ID(gone)})
	ok(t, err)
	eventually(t, 5*time.Second, "tab-closed on the page the browser closed", func() bool {
		_, err := h.Evaluate(ctx30(t), gone, "1")
		code, _ := CodeOf(err)
		return code == CodeTabClosed
	})
}

func TestE2E_Browser_NewTabStartsBlank(t *testing.T) {
	inst, _ := sharedInstance(t)
	for _, lane := range []Lane{LaneApply, LaneScan} {
		tab := openTab(t, inst, lane, "run-blank")
		if got := eval[string](t, inst.Lane(lane), tab, "location.href"); got != "about:blank" {
			t.Fatalf("%s tab starts at %q", lane, got)
		}
	}
}

func TestE2E_Browser_TabIDsAreTheSameEverywhere(t *testing.T) {
	inst, _ := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-ids")
	h := inst.Lane(LaneScan)
	has := func(ids []string) bool {
		for _, id := range ids {
			if id == tab {
				return true
			}
		}
		return false
	}
	var inTabs, inStatus []string
	for _, ti := range h.Tabs() {
		inTabs = append(inTabs, ti.ID)
	}
	for _, ts := range NewControl(inst.opts).Status(ctx30(t)).Scan.Tabs {
		inStatus = append(inStatus, ts.ID)
	}
	if !has(inTabs) || !has(inStatus) {
		t.Fatalf("tab %s: Tabs %v, Status %v", tab, has(inTabs), has(inStatus))
	}
}

// A blank tab that arrives after its creation was given up on is closed; a
// page that was open before, and one the user opens, are never touched.
func TestE2E_Browser_LateCreatedTabIsClosed(t *testing.T) {
	inst, _ := sharedInstance(t)
	existing := openTab(t, inst, LaneApply, "run-late")
	inst.noteAbandoned()
	res, err := chromedp.CallBrowser(inst.root, target.CreateTarget, target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(false), Focus: ptr(false)})
	ok(t, err)
	late := res.TargetID
	eventually(t, 10*time.Second, "the late tab closed", func() bool {
		pages, err := inst.listTargets(ctx30(t))
		if err != nil {
			return false
		}
		_, there := pages[late]
		return !there
	})
	if !hasEventWith(inst, EventTabClosed, "late-creation") {
		t.Fatal("no tab_closed event with the reason late-creation")
	}
	pages, err := inst.listTargets(ctx30(t))
	ok(t, err)
	if _, there := pages[target.ID(existing)]; !there {
		t.Fatal("an existing page was closed because of another operation's timeout")
	}
	// Nothing is abandoned now: a tab the user opens by hand stays.
	user, err := chromedp.CallBrowser(inst.root, target.CreateTarget, target.CreateTargetParams{URL: "about:blank", NewWindow: ptr(false), Focus: ptr(false)})
	ok(t, err)
	t.Cleanup(func() {
		_, _ = chromedp.CallBrowser(inst.root, target.CloseTarget, target.CloseTargetParams{TargetID: user.TargetID})
	})
	time.Sleep(time.Second)
	pages, err = inst.listTargets(ctx30(t))
	ok(t, err)
	if _, there := pages[user.TargetID]; !there {
		t.Fatal("a tab the user opened was closed")
	}
}

func TestBrowser_NavigateDOM_ReportsFinalStatusWithoutWaitingForLoad(t *testing.T) {
	inst, _ := sharedInstance(t)
	h := inst.Lane(LaneScan)
	tab := openTab(t, inst, LaneScan, "dom-status")
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/held-frame":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>held frame")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/child":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "child missing")
		default:
			w.Header().Set("Content-Type", "text/html")
			if r.URL.Path != "/ready" {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			fmt.Fprint(w, `<title>owned unavailable fixture</title><iframe src="/held-frame"></iframe><iframe src="/child"></iframe>`)
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready, err := h.NavigateDOM(ctx, tab, server.URL+"/ready")
	ok(t, err)
	if ready.Status != http.StatusOK || eval[string](t, h, tab, "document.readyState") == "complete" {
		t.Fatalf("DOM-ready navigation waited for the held frame: %+v", ready)
	}
	res, err := h.NavigateDOM(ctx, tab, server.URL+"/start")
	ok(t, err)
	if res.Status != http.StatusServiceUnavailable || res.URL != server.URL+"/final" || res.Title != "owned unavailable fixture" || res.LoadState != "domcontentloaded" {
		t.Fatalf("navigation result = %+v", res)
	}
}
