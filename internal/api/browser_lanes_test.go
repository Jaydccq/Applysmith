package api

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
)

var stillBytes = []byte("\xff\xd8\xff\xe0 still image")

// fakeControl is a scripted managed browser.
type fakeControl struct {
	mu       sync.Mutex
	status   browser.Status
	shotErr  error
	showErr  error
	leased   bool
	stopOut  browser.StopOutcome
	shots    int
	lastOpts browser.ShotOptions
	shown    []string
	stops    []bool
	driven   bool
	inputErr error
	inputs   [][]browser.InputEvent
	navs     []string
	live     []browser.LiveEvent
	liveOpts browser.LiveOptions
	survives bool
}

func (f *fakeControl) Status(context.Context) browser.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeControl) State() (browser.InstanceState, string) { return f.status.State, "" }

func (f *fakeControl) Survives() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.survives
}

func (f *fakeControl) Capture(_ context.Context, lane browser.Lane, tabID string, o browser.ShotOptions) (browser.Shot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shots++
	f.lastOpts = o
	if f.shotErr != nil {
		return browser.Shot{}, f.shotErr
	}
	if lane != browser.LaneApply || tabID != "tab-1" {
		return browser.Shot{}, &browser.Error{Code: browser.CodeTabClosed, Message: "no such tab"}
	}
	ct := "image/jpeg"
	if o.PNG {
		ct = "image/png"
	}
	return browser.Shot{Data: stillBytes, ContentType: ct}, nil
}

func (f *fakeControl) Show(_ context.Context, tabID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shown = append(f.shown, tabID)
	return f.showErr
}

func (f *fakeControl) Stop(_ context.Context, force bool) (browser.StopOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, force)
	if f.leased && !force {
		return "", &browser.Error{Code: browser.CodeLaneLeased, Message: "a run holds a browser tab"}
	}
	return f.stopOut, nil
}

// liveTab answers as the browser does: tab-1 is the only apply tab, and a tab
// a run drives takes no input.
func (f *fakeControl) liveTab(tabID string, input bool) error {
	if tabID != "tab-1" {
		return &browser.Error{Code: browser.CodeTabClosed, Message: "no such tab"}
	}
	if input && f.driven {
		return &browser.Error{Code: browser.CodeTabDriven, Message: "Applysmith is using this tab"}
	}
	return nil
}

func (f *fakeControl) Live(_ context.Context, tabID string, o browser.LiveOptions, emit func(browser.LiveEvent) error) error {
	f.mu.Lock()
	f.liveOpts = o
	events := f.live
	f.mu.Unlock()
	if err := f.liveTab(tabID, false); err != nil {
		return err
	}
	for _, ev := range events {
		if err := emit(ev); err != nil {
			return nil
		}
	}
	return nil
}

func (f *fakeControl) Input(_ context.Context, tabID string, events []browser.InputEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.liveTab(tabID, true); err != nil {
		return err
	}
	if f.inputErr != nil {
		return f.inputErr
	}
	f.inputs = append(f.inputs, events)
	return nil
}

func (f *fakeControl) Navigate(_ context.Context, tabID, action, rawURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.liveTab(tabID, true); err != nil {
		return err
	}
	f.navs = append(f.navs, action+" "+rawURL)
	return nil
}

func (f *fakeControl) NewTab(_ context.Context, rawURL string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.navs = append(f.navs, "new "+rawURL)
	return "tab-new", nil
}

func runningStatus(now time.Time) browser.Status {
	created := now.Add(-90 * time.Second)
	return browser.Status{State: browser.InstanceRunning, Version: "Google Chrome 150.0.0.0",
		Apply: browser.LaneStatus{Window: true, Cap: 24, Tabs: []browser.TabStatus{
			{ID: "tab-1", Title: "Apply", URL: "https://jobs.example.test/apply", Active: true, Created: created, Kept: true}}},
		Scan:   browser.LaneStatus{Cap: 32},
		Events: []browser.Event{{Time: created, Kind: "dialog", Lane: browser.LaneApply, Detail: "alert: hello"}}}
}

func controlHarness(t *testing.T, f *fakeControl, live *atomic.Bool) *harness {
	t.Helper()
	return newDomainHarness(t, func(c *Config) {
		c.BrowserControl = f
		if live != nil {
			c.ApplyRunLive = live.Load
		}
	})
}

func TestBrowserStatus_NoBrowserIsStoppedWithBothLanesEmpty(t *testing.T) {
	h := newDomainHarness(t)
	d := h.get("/api/browser/status").data()
	apply, _ := d["apply"].(map[string]any)
	if d["state"] != "stopped" || apply["window"] != false || len(apply["tabs"].([]any)) != 0 || len(d["events"].([]any)) != 0 {
		t.Fatalf("%v", d)
	}
}

func TestBrowserStatus_ShowsLanesTabsAndNewestEvents(t *testing.T) {
	f := &fakeControl{}
	h := controlHarness(t, f, nil)
	f.status = runningStatus(h.srv.now())
	d := h.get("/api/browser/status").data()
	if d["state"] != "running" || d["chromeVersion"] != "Google Chrome 150.0.0.0" {
		t.Fatalf("%v", d)
	}
	apply := d["apply"].(map[string]any)
	tab := apply["tabs"].([]any)[0].(map[string]any)
	if apply["window"] != true || apply["tabCap"] != float64(24) || tab["id"] != "tab-1" || tab["url"] != "https://jobs.example.test/apply" || tab["active"] != true || tab["kept"] != true {
		t.Fatalf("%v", apply)
	}
	if ev := d["events"].([]any); len(ev) != 1 || ev[0].(map[string]any)["kind"] != "dialog" {
		t.Fatalf("%v", d["events"])
	}
	// Home's Server card agrees: running, not "not set up".
	if c := jsonBody(h.get("/api/health").data()["components"]); !strings.Contains(string(c), `"detail":"running","name":"browser","status":"ok"`) {
		t.Errorf("%s", c)
	}
}

func TestBrowserScreenshot_ServesTheImageAndRefusesWhatItCannotFind(t *testing.T) {
	f := &fakeControl{status: runningStatus(time.Now())}
	h := controlHarness(t, f, nil)
	r := h.get("/api/browser/lanes/apply/tabs/tab-1/screenshot")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/jpeg" || string(r.body) != string(stillBytes) {
		t.Fatalf("%d %q %q", r.StatusCode, r.Header.Get("Content-Type"), r.body)
	}
	if png := h.get("/api/browser/lanes/apply/tabs/tab-1/screenshot?format=png&width=800"); png.Header.Get("Content-Type") != "image/png" || !f.lastOpts.PNG || f.lastOpts.Width != 800 {
		t.Fatalf("%d %v", png.StatusCode, f.lastOpts)
	}
	for _, c := range []struct {
		path   string
		status int
		code   string
	}{
		{"/api/browser/lanes/apply/tabs/gone/screenshot", 404, "not_found"},
		{"/api/browser/lanes/apply/tabs/tab-1/screenshot?format=gif", 422, "validation_failed"},
	} {
		if r := h.get(c.path); r.StatusCode != c.status || r.errCode() != c.code {
			t.Errorf("%s: %d %s", c.path, r.StatusCode, r.body)
		}
	}
	f.shotErr = &browser.Error{Code: browser.CodeScreenshotUnavailable, Message: "the browser cannot render this tab"}
	if r := h.get("/api/browser/lanes/apply/tabs/tab-1/screenshot"); r.StatusCode != 503 || r.errReason() != "screenshot-unavailable" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestBrowserShow_ActivatesTheNamedTabAndStartsWithoutOne(t *testing.T) {
	f := &fakeControl{status: runningStatus(time.Now())}
	h := controlHarness(t, f, nil)
	if r := h.post("/api/browser/apply-window/show", map[string]string{"tabId": "tab-1"}); r.StatusCode != 200 || r.data()["shown"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/browser/apply-window/show", map[string]string{}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if len(f.shown) != 2 || f.shown[0] != "tab-1" || f.shown[1] != "" {
		t.Fatalf("%v", f.shown)
	}
	f.showErr = &browser.Error{Code: browser.CodeChromeMissing, Message: "Google Chrome is not installed"}
	if r := h.post("/api/browser/apply-window/show", map[string]string{}); r.StatusCode != 503 || r.errReason() != "chrome-missing" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if r := newDomainHarness(t).post("/api/browser/apply-window/show", map[string]string{}); r.StatusCode != 503 || r.errReason() != "browser-unavailable" {
		t.Fatalf("no browser: %d %s", r.StatusCode, r.body)
	}
}

func TestBrowserStop_RefusesWhileARunHoldsALaneUnlessForced(t *testing.T) {
	f := &fakeControl{status: runningStatus(time.Now()), stopOut: browser.StopClosed, leased: true}
	h := controlHarness(t, f, nil)
	if r := h.post("/api/browser/stop", map[string]any{}); r.StatusCode != 409 || r.errCode() != "conflict" || r.errReason() != "lane-leased" {
		t.Fatalf("a run holds a lane: %d %s", r.StatusCode, r.body)
	}
	if len(f.stops) != 1 || f.stops[0] {
		t.Fatalf("the refusal comes from the browser's leases, asked without force: %v", f.stops)
	}
	if r := h.post("/api/browser/stop", map[string]any{"force": true}); r.StatusCode != 200 || r.data()["outcome"] != "closed" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	f.leased, f.stopOut = false, ""
	if r := h.post("/api/browser/stop", map[string]any{}); r.StatusCode != 200 || r.data()["outcome"] != wire.BrowserNotRunning {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestBrowserLaneRoutes_TableClasses(t *testing.T) {
	want := map[string]struct {
		access Access
		remote Remote
	}{
		"GET /api/browser/status":                               {AccessStandard, RemoteDenied},
		"GET /api/browser/lanes/{lane}/tabs/{tabId}/screenshot": {AccessStandard, RemoteAllowed},
		"POST /api/browser/apply-window/show":                   {AccessStandard, RemoteDenied},
		"POST /api/browser/stop":                                {AccessAdmin, RemoteDenied},
	}
	for _, r := range Routes() {
		if w, ok := want[r.Pattern()]; ok {
			if r.Access != w.access || r.Remote != w.remote {
				t.Errorf("%s: %s %s", r.Pattern(), r.Access, r.Remote)
			}
			delete(want, r.Pattern())
		}
	}
	for p := range want {
		t.Errorf("missing route %s", p)
	}
}

// Every typed lane error crosses the boundary as the reason of the nearest
// envelope code: environment faults are unavailable, state conflicts are
// conflict and rejected input is validation_failed.
func TestAPI_BrowserErrors_UnavailableReason(t *testing.T) {
	want := map[wire.ErrorCode][]browser.Code{
		wire.CodeUnavailable:      {browser.CodeChromeMissing},
		wire.CodeConflict:         {browser.CodeLaneLeased},
		wire.CodeValidationFailed: {browser.CodeSchemeNotAllowed},
	}
	for code, reasons := range want {
		for _, reason := range reasons {
			got, ok := browserLaneError(&browser.Error{Code: reason, Message: "example"}).(*apikit.Error)
			if !ok || got.Code != code || got.Details.Reason != string(reason) {
				t.Errorf("%s: %+v, want %s with that reason", reason, got, code)
			}
		}
	}
}

// sessionCall sends a JSON request with the dashboard session, which the
// browser panel's routes require.
func sessionCall(h *harness, cookie, method, path string, body any) result {
	h.t.Helper()
	c := call{method: method, path: path, cookie: cookie, headers: map[string]string{"Origin": h.srv.Origin()}}
	if body != nil {
		c.contentType, c.body = "application/json", jsonBody(body)
	}
	return h.do(c)
}

const (
	inputPath = "/api/browser/lanes/apply/tabs/tab-1/input"
	navPath   = "/api/browser/lanes/apply/tabs/tab-1/navigate"
	livePath  = "/api/browser/lanes/apply/tabs/tab-1/live"
)

func TestBrowserPanel_RoutesTakeTheDashboardSessionOnlyAndStayLocal(t *testing.T) {
	f := &fakeControl{}
	h := controlHarness(t, f, nil)
	click := map[string]any{"events": []map[string]any{{"kind": "mouse", "type": "down", "x": 10, "y": 20, "button": "left", "clickCount": 1}}}
	for _, r := range []result{h.post(inputPath, click), h.post(navPath, map[string]string{"action": "reload"}), h.get(livePath)} {
		if r.StatusCode != 403 || r.errReason() != wire.ReasonSessionOnly {
			t.Errorf("the bearer token reached a panel route: %d %s", r.StatusCode, r.body)
		}
	}
	for _, p := range [][2]string{{"GET", "/api/browser/lanes/apply/tabs/{tabId}/live"}, {"POST", "/api/browser/lanes/apply/tabs/{tabId}/input"}, {"POST", "/api/browser/lanes/apply/tabs/{tabId}/navigate"}} {
		if r := routeOf(t, p[0], p[1]); r.Remote != RemoteDenied || !r.SessionOnly {
			t.Errorf("%s %s must be local and session-only: %+v", p[0], p[1], r)
		}
	}
	if r := routeOf(t, "POST", "/api/browser/lanes/apply/tabs/{tabId}/input"); r.Limit != LimitInput {
		t.Errorf("the input route counts in its own class: %s", r.Limit)
	}
	if len(f.inputs) != 0 || len(f.navs) != 0 {
		t.Fatalf("a refused call reached the browser: %v %v", f.inputs, f.navs)
	}
}

func TestBrowserPanel_InputReachesAUserTabAndADrivenTabRefuses(t *testing.T) {
	f := &fakeControl{}
	h := controlHarness(t, f, nil)
	cookie := h.session()
	body := map[string]any{"events": []map[string]any{
		{"kind": "mouse", "type": "move", "x": 10.5, "y": 20},
		{"kind": "mouse", "type": "down", "x": 10.5, "y": 20, "button": "left", "buttons": 1, "clickCount": 1, "modifiers": 8},
		{"kind": "wheel", "x": 10, "y": 20, "deltaY": 120},
		{"kind": "key", "type": "down", "key": "a", "code": "KeyA", "keyCode": 65, "text": "a"},
		{"kind": "text", "text": "\u4f60\u597d"},
	}}
	if r := sessionCall(h, cookie, "POST", inputPath, body); r.StatusCode != 200 || r.data()["sent"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	got := f.inputs[0]
	if len(got) != 5 || got[0].Button != "none" || got[1].Button != "left" || got[1].Modifiers != 8 || got[1].X != 10.5 || got[2].DeltaY != 120 || got[3].Text != "a" || got[4].Text != "\u4f60\u597d" {
		t.Fatalf("events as the browser got them: %+v", got)
	}

	f.driven = true
	if r := sessionCall(h, cookie, "POST", inputPath, body); r.StatusCode != 409 || r.errReason() != string(browser.CodeTabDriven) {
		t.Fatalf("a driven tab took input: %d %s", r.StatusCode, r.body)
	}
	if r := sessionCall(h, cookie, "POST", navPath, map[string]string{"action": "back"}); r.StatusCode != 409 || r.errReason() != string(browser.CodeTabDriven) {
		t.Fatalf("a driven tab took a navigation: %d %s", r.StatusCode, r.body)
	}
	f.driven = false
	if r := sessionCall(h, cookie, "POST", "/api/browser/lanes/apply/tabs/tab-9/input", body); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Fatalf("unknown tab: %d %s", r.StatusCode, r.body)
	}
	if len(f.inputs) != 1 {
		t.Fatalf("refused input reached the browser: %d", len(f.inputs))
	}
}

func TestBrowserPanel_InputRefusesMalformedEvents(t *testing.T) {
	f := &fakeControl{}
	h := controlHarness(t, f, nil)
	cookie := h.session()
	many := make([]map[string]any, 65)
	for i := range many {
		many[i] = map[string]any{"kind": "mouse", "type": "move"}
	}
	for name, events := range map[string]any{
		"too many":     many,
		"unknown kind": []map[string]any{{"kind": "touch"}},
	} {
		if r := sessionCall(h, cookie, "POST", inputPath, map[string]any{"events": events}); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if len(f.inputs) != 0 {
		t.Fatalf("malformed input reached the browser: %v", f.inputs)
	}
}

func TestBrowserPanel_NavigateChecksTheActionAndTheAddress(t *testing.T) {
	f := &fakeControl{}
	var live atomic.Bool
	h := newDomainHarness(t, func(c *Config) {
		c.BrowserControl = f
		c.ApplyRunLive = live.Load
		c.SessionBrowser = &fakeBrowser{sites: []string{"jobs.example.test"}}
	})
	cookie := h.session()
	for _, a := range []string{"back", "reload"} {
		if r := sessionCall(h, cookie, "POST", navPath, map[string]string{"action": a}); r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", a, r.StatusCode, r.body)
		}
	}
	if r := sessionCall(h, cookie, "POST", navPath, map[string]string{"action": "go", "url": " https://other.example.test/jobs "}); r.StatusCode != 200 {
		t.Fatalf("go: %d %s", r.StatusCode, r.body)
	}
	for _, bad := range []map[string]string{{"action": "go", "url": "javascript:alert(1)"}, {"action": "go", "url": "file:///etc/hosts"},
		{"action": "go", "url": "https://user:pw@jobs.example.test/"}, {"action": "go"}, {"action": "close"}} {
		if r := sessionCall(h, cookie, "POST", navPath, bad); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%v: %d %s", bad, r.StatusCode, r.body)
		}
	}
	live.Store(true)
	if r := sessionCall(h, cookie, "POST", navPath, map[string]string{"action": "go", "url": "https://jobs.example.test/login"}); r.StatusCode != 409 || r.errReason() != reasonApplyRunLive {
		t.Fatalf("a sign-in sync site during a run: %d %s", r.StatusCode, r.body)
	}
	want := []string{"back ", "reload ", "go https://other.example.test/jobs"}
	if strings.Join(f.navs, "|") != strings.Join(want, "|") {
		t.Fatalf("navigations: %q", f.navs)
	}
}

func TestBrowserPanel_LiveStreamsTheTabAndFailsBeforeTheFirstEvent(t *testing.T) {
	f := &fakeControl{live: []browser.LiveEvent{
		{Tab: &browser.LiveTab{URL: "https://jobs.example.test/apply?step=2", Title: "Apply", Driven: true, RunID: "run_a"}},
		{Frame: &browser.LiveFrame{JPEG: stillBytes, Width: 1280, Height: 800, Scale: 1}},
		{Cursor: &[2]float64{40, 60}},
		{FileChooser: true},
		{End: browser.LiveEndTabClosed},
	}}
	h := controlHarness(t, f, nil)
	cookie := h.session()
	r := sessionCall(h, cookie, "GET", livePath+"?maxWidth=800&maxHeight=600", nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	body := string(r.body)
	order := []string{"event: tab\n", `"url":"https://jobs.example.test/apply?step=2"`, `"runId":"run_a"`, "event: frame\n", `"width":1280`, "event: cursor\n", `"x":40`, "event: filechooser\n", "event: end\n", `"reason":"tab-closed"`}
	at := 0
	for _, part := range order {
		i := strings.Index(body[at:], part)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", part, body)
		}
		at += i
	}
	if !strings.Contains(body, base64.StdEncoding.EncodeToString(stillBytes)) {
		t.Errorf("the frame is the JPEG in base64: %s", body)
	}
	if f.liveOpts.MaxWidth != 800 || f.liveOpts.MaxHeight != 600 {
		t.Errorf("frame bounds: %+v", f.liveOpts)
	}
	if r := sessionCall(h, cookie, "GET", "/api/browser/lanes/apply/tabs/tab-9/live", nil); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Fatalf("unknown tab: %d %s", r.StatusCode, r.body)
	}
	if r := sessionCall(h, cookie, "GET", livePath+"?maxWidth=9000", nil); r.StatusCode != 422 {
		t.Fatalf("frame bound: %d %s", r.StatusCode, r.body)
	}
}

func TestBrowserPanel_NewTabOpensAUserTabWithoutRaisingTheWindow(t *testing.T) {
	f := &fakeControl{}
	h := controlHarness(t, f, nil)
	cookie := h.session()
	before := h.boardRevision("browser")
	for _, body := range []map[string]string{{}, {"url": "https://jobs.example.test/apply"}} {
		if r := sessionCall(h, cookie, "POST", "/api/browser/lanes/apply/tabs", body); r.StatusCode != 200 || r.data()["tabId"] != "tab-new" {
			t.Fatalf("%v: %d %s", body, r.StatusCode, r.body)
		}
	}
	if r := sessionCall(h, cookie, "POST", "/api/browser/lanes/apply/tabs", map[string]string{"url": "javascript:alert(1)"}); r.StatusCode != 422 {
		t.Fatalf("a script address: %d %s", r.StatusCode, r.body)
	}
	if strings.Join(f.navs, "|") != "new |new https://jobs.example.test/apply" || len(f.shown) != 0 {
		t.Fatalf("opened %q, shown %v", f.navs, f.shown)
	}
	if h.boardRevision("browser") == before {
		t.Error("the Browser page is not told about the new tab")
	}
}
