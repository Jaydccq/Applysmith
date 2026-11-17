//go:build browser

package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Task 1.4 spike gate. Each test is one proof from the browser library spike
// requirement; the names carry the proof letter. Proofs (a), (b), (c) and (f)
// are the product tests of the hooks and the response watch.

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestSpike_D_TrustedMouseAndKeyInput(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "spike-d")
	navigate(t, inst, LaneScan, tab, fx.A("/clicks"))
	top := tabSession(t, inst, LaneScan, tab)
	t.Logf("scan viewport %vx%v", evalOn[float64](t, top, "innerWidth"), evalOn[float64](t, top, "innerHeight"))
	x, y := rectOf(t, top, "#b").center()
	click(t, top, x, y)
	type ev struct {
		Click   bool    `json:"click"`
		Key     string  `json:"key"`
		Trusted bool    `json:"trusted"`
		X       float64 `json:"x"`
	}
	events := evalOn[[]ev](t, top, `window.__events`)
	if len(events) != 1 || !events[0].Click || !events[0].Trusted {
		t.Fatalf("click events %+v", events)
	}
	x, y = rectOf(t, top, "#t").center()
	click(t, top, x, y)
	typeText(t, top, "Ada")
	events = evalOn[[]ev](t, top, `window.__events`)
	var keys string
	for _, e := range events[1:] {
		if e.Key != "" {
			if !e.Trusted {
				t.Fatalf("untrusted key event %+v", e)
			}
			keys += e.Key
		}
	}
	if keys != "Ada" || evalOn[string](t, top, `document.getElementById('t').value`) != "Ada" {
		t.Fatalf("keys %q", keys)
	}

	// Inside a cross-origin iframe: coordinates are computed through the
	// frame's offset and sent on the top-level session.
	navigate(t, inst, LaneScan, tab, fx.A("/outer"))
	frame, _ := iframeSession(t, inst, fx.B(""))
	outer := rectOf(t, top, "#f")
	inner := rectOf(t, frame, "#go")
	cx, cy := inner.center()
	// The frame's surface may not be ready for hit testing right after it
	// loads, so a click that did not arrive is repeated.
	for attempt := 0; attempt < 5 && evalOn[int](t, frame, `window.__events.length`) == 0; attempt++ {
		click(t, top, outer.X+cx, outer.Y+cy)
		time.Sleep(300 * time.Millisecond)
	}
	fevents := evalOn[[]ev](t, frame, `window.__events`)
	if len(fevents) != 1 || !fevents[0].Click || !fevents[0].Trusted {
		t.Fatalf("cross-origin click events %+v", fevents)
	}
	nx, ny := rectOf(t, frame, "#name").center()
	for attempt := 0; attempt < 5 && evalOn[bool](t, frame, `document.activeElement.id!=='name'`); attempt++ {
		click(t, top, outer.X+nx, outer.Y+ny)
		time.Sleep(300 * time.Millisecond)
	}
	typeText(t, top, "Ada")
	eventually(t, 3*time.Second, "the typed text to reach the frame", func() bool {
		return evalOn[string](t, frame, `document.getElementById('name').value`) == "Ada"
	})
}

// probe is the responsiveness probe of the background policy: a DOM update
// scheduled 100 ms after load, an extraction, and a non-blank screenshot, all
// within 5 s after load.
type probeResult struct {
	Tab        string
	LoadToDone time.Duration
	Focus      bool
	Visibility string
	Colors     int
	Err        error
}

func renderProbe(inst *Instance, fx *fixtures, lane Lane, id string) probeResult {
	r := probeResult{Tab: id}
	h := inst.Lane(lane)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := h.Navigate(ctx, id, fx.Loopback("/render")); err != nil {
		r.Err = err
		return r
	}
	loaded := time.Now()
	deadline := loaded.Add(5 * time.Second)
	for {
		raw, err := h.Evaluate(ctx, id, `document.getElementById('t').textContent`)
		if err == nil && strings.Contains(string(raw), "updated") {
			break
		}
		if time.Now().After(deadline) {
			r.Err = fmt.Errorf("DOM update not seen within 5s after load (err %v)", err)
			return r
		}
		time.Sleep(25 * time.Millisecond)
	}
	shot, err := h.Screenshot(ctx, id)
	if err != nil {
		r.Err = err
		return r
	}
	r.LoadToDone = time.Since(loaded)
	img, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		r.Err = err
		return r
	}
	seen := map[[3]uint32]bool{}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 20 {
		for x := b.Min.X; x < b.Max.X; x += 20 {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			seen[[3]uint32{cr >> 8, cg >> 8, cb >> 8}] = true
		}
	}
	r.Colors = len(seen)
	if seen[[3]uint32{255, 255, 255}] && len(seen) == 1 || len(seen) == 0 {
		r.Err = fmt.Errorf("blank screenshot")
	}
	raw, _ := h.Evaluate(ctx, id, `document.visibilityState`)
	_ = json.Unmarshal(raw, &r.Visibility)
	raw, _ = h.Evaluate(ctx, id, `document.hasFocus()`)
	_ = json.Unmarshal(raw, &r.Focus)
	return r
}

func TestSpike_G_BackgroundPolicyWithoutStealingFocus(t *testing.T) {
	inst, fx := headedInstance(t)
	locked := screenLocked()
	before := frontApp()
	tab := openTab(t, inst, LaneScan, "spike-g")
	st, err := inst.Lane(LaneScan).WindowState(ctx30(t), tab)
	ok(t, err)
	if st != "normal" {
		t.Fatalf("scan window is %q, want a normal (covered) window", st)
	}
	r := renderProbe(inst, fx, LaneScan, tab)
	if r.Err != nil {
		t.Fatalf("covered window probe failed: %v", r.Err)
	}
	if r.LoadToDone > 5*time.Second {
		t.Fatalf("probe took %v", r.LoadToDone)
	}
	after := frontApp()
	t.Logf("policy=covered-unminimized+focus-emulation: update+extract+screenshot %v after load, visibility=%s hasFocus=%v colors=%d locked=%v front before=%q after=%q",
		r.LoadToDone.Round(time.Millisecond), r.Visibility, r.Focus, r.Colors, locked, before, after)
	if locked {
		t.Log("screen locked: the frontmost-application check is vacuous and is not asserted")
		return
	}
	if before != after {
		t.Fatalf("foreground application changed from %q to %q", before, after)
	}
}

func TestSpike_H1_ConcurrentScanTabsAllRender(t *testing.T) {
	inst, fx := sharedInstance(t)
	var ids []string
	for range 4 {
		ids = append(ids, openTab(t, inst, LaneScan, "spike-h1"))
	}
	results := make([]probeResult, len(ids))
	var wg sync.WaitGroup
	start := time.Now()
	for i, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = renderProbe(inst, fx, LaneScan, id) }()
	}
	wg.Wait()
	for i, r := range results {
		if r.Err != nil || r.LoadToDone > 5*time.Second {
			t.Errorf("tab %d: err=%v done=%v", i, r.Err, r.LoadToDone)
		}
		t.Logf("tab %d: %v after load, visibility=%s", i, r.LoadToDone.Round(time.Millisecond), r.Visibility)
	}
	wins := map[int64]bool{}
	for _, id := range ids {
		wins[windowOfTab(t, inst, id)] = true
	}
	t.Logf("%d tabs in %d windows", len(ids), len(wins))
	t.Logf("four concurrent tabs finished in %v", time.Since(start).Round(time.Millisecond))
}

// Chrome's activation command takes the foreground, so a run never activates
// a tab. A second tab opens in the background; the first is no longer the
// active tab of its window and still renders, takes input and can be captured.
func TestSpike_H3_BackgroundTabCreationDoesNotRaiseAndInactiveTabsWork(t *testing.T) {
	inst, fx := headedInstance(t)
	first := openTab(t, inst, LaneApply, "spike-h3")
	navigate(t, inst, LaneApply, first, fx.Loopback("/clicks"))
	locked := screenLocked()
	before := frontApp()
	second := openTab(t, inst, LaneApply, "spike-h3")
	navigate(t, inst, LaneApply, second, fx.Loopback("/probe"))
	time.Sleep(500 * time.Millisecond)
	if after := frontApp(); !locked && after != before {
		t.Fatalf("foreground application changed from %q to %q when a second apply tab opened", before, after)
	}
	h := inst.Lane(LaneApply)
	if got := eval[string](t, h, first, `document.visibilityState`); got != "visible" {
		t.Fatalf("the inactive tab reports %q", got)
	}
	if _, err := h.Screenshot(ctx30(t), first); err != nil {
		t.Fatalf("capture of the inactive tab: %v", err)
	}
	sess := tabSession(t, inst, LaneApply, first)
	x, y := rectOf(t, sess, "#b").center()
	click(t, sess, x, y)
	if n := evalOn[int](t, sess, `window.__events.length`); n != 1 {
		t.Fatalf("a click on the inactive tab produced %d events", n)
	}
	t.Logf("locked=%v front before=%q after=%q", locked, before, frontApp())
}

func TestSpike_H4_WebPageCannotReachTheDevToolsEndpoint(t *testing.T) {
	inst, fx := sharedInstance(t)
	port := inst.proc.Port
	wsURL := inst.proc.wsURL
	tab := openTab(t, inst, LaneScan, "spike-h4")
	navigate(t, inst, LaneScan, tab, fx.Loopback("/geo"))
	sess := tabSession(t, inst, LaneScan, tab)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	res := evalOn[map[string]string](t, sess, `(async()=>{
	  const out={};
	  try{const r=await fetch("`+endpoint+`/json/version");out.cors="read:"+r.status}catch(e){out.cors="blocked"}
	  try{const r=await fetch("`+endpoint+`/json/version",{mode:"no-cors"});out.nocors=r.type+":"+r.status}catch(e){out.nocors="blocked"}
	  try{const r=await fetch("`+endpoint+`/json/list",{mode:"no-cors"});out.list=r.type+":"+r.status}catch(e){out.list="blocked"}
	  out.ws=await new Promise(res=>{try{const w=new WebSocket(`+jsonString(wsURL)+`);w.onopen=()=>{res("opened");w.close()};w.onerror=()=>res("refused");w.onclose=()=>res("refused")}catch(e){res("refused")}});
	  return out})()`)
	if res["cors"] != "blocked" {
		t.Errorf("a cross-origin read of /json/version was allowed: %s", res["cors"])
	}
	if res["nocors"] != "blocked" && res["nocors"] != "opaque:0" {
		t.Errorf("no-cors response is readable: %s", res["nocors"])
	}
	if res["ws"] != "refused" {
		t.Errorf("a page opened a WebSocket to the browser endpoint: %s", res["ws"])
	}
	t.Logf("page-side attempts: %v", res)

	// Raw requests with a foreign Origin or a non-local Host, and the
	// new-tab endpoint.
	raw := func(method, path, host, origin string) (int, string) {
		req, _ := http.NewRequest(method, endpoint+path, nil)
		if host != "" {
			req.Host = host
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, string(b)
	}
	st, body := raw("GET", "/json/version", "evil.example", "")
	if st == 200 || strings.Contains(body, "webSocketDebuggerUrl") {
		t.Errorf("non-local Host answered: %d %.80s", st, body)
	}
	t.Logf("GET /json/version with Host evil.example: %d %.70q", st, body)
	st, body = raw("GET", "/json/new?about:blank", "", "")
	if st == 200 || strings.Contains(body, "webSocketDebuggerUrl") {
		t.Errorf("GET /json/new created a tab: %d %.80s", st, body)
	}
	t.Logf("GET /json/new: %d %.70q", st, body)
	st, body = raw("GET", "/json/version", "", "http://evil.example")
	// Chrome answers a plain GET whatever the Origin; the browser's CORS rules,
	// not the endpoint, keep the body from the page (asserted above through
	// the page-side fetch).
	if st != 200 {
		t.Errorf("GET /json/version with a foreign Origin answered %d; the measured Chrome behaviour is 200", st)
	}
	t.Logf("GET /json/version with a foreign Origin: %d, body carries the address: %v", st, strings.Contains(body, "webSocketDebuggerUrl"))
	// The WebSocket upgrade with a foreign Origin is refused by Chrome.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	ok(t, err)
	defer conn.Close()
	u := strings.TrimPrefix(wsURL, "ws://")
	path := u[strings.Index(u, "/"):]
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\nSec-WebSocket-Version: 13\r\nOrigin: http://evil.example\r\n\r\n", path, port)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, 64)
	n, _ := conn.Read(head)
	if !strings.HasPrefix(string(head[:n]), "HTTP/1.1 403") {
		t.Errorf("upgrade with a foreign Origin answered %q, want 403", head[:n])
	}
	t.Logf("WebSocket upgrade with a foreign Origin: %q", strings.SplitN(string(head[:n]), "\r\n", 2)[0])
}
