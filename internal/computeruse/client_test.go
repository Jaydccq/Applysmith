//go:build darwin

package computeruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeCU answers as PersonalCU's Swift source does: the tool and argument
// names of ToolCatalog.swift, whose schemas refuse any other argument; the
// replies of MCPHost.swift (the JSON text, then the PNG), WindowService.swift,
// DesktopController.swift, Observation.swift and Accessibility.swift; and the
// strict server of the Swift MCP SDK, which never answers a request that comes
// before initialize.
type fakeCU struct {
	ln net.Listener

	mu       sync.Mutex
	conns    int
	sessions []*mcp.ServerSession
	doctor   map[string]any
	refusal  string                    // the error text of the next action
	args     map[string]map[string]any // the last arguments of each tool
}

var fakePNG = []byte("\x89PNG\r\n\x1a\nfake")

// Arguments as ToolCatalog.swift declares them.
type (
	noArgs   struct{}
	appsArgs struct {
		RunningOnly *bool `json:"running_only,omitempty"`
	}
	windowsArgs struct {
		PID      *int    `json:"pid,omitempty"`
		BundleID *string `json:"bundle_id,omitempty"`
	}
	stateArgs struct {
		WindowID          int     `json:"window_id"`
		PID               *int    `json:"pid,omitempty"`
		BundleID          *string `json:"bundle_id,omitempty"`
		Mode              *string `json:"mode,omitempty"`
		Rect              any     `json:"rect,omitempty"`
		AXFilter          *string `json:"ax_filter,omitempty"`
		Offset            *int    `json:"offset,omitempty"`
		Limit             *int    `json:"limit,omitempty"`
		SnapshotID        *string `json:"snapshot_id,omitempty"`
		DisableDiff       *bool   `json:"disable_diff,omitempty"`
		AdditionalWindows []int   `json:"additional_window_ids,omitempty"`
		IncludeScreenshot *bool   `json:"include_screenshot,omitempty"`
		IncludeText       *bool   `json:"include_text,omitempty"`
	}
	boundArgs struct {
		PID          int     `json:"pid"`
		SnapshotID   string  `json:"snapshot_id"`
		ScreenshotID *string `json:"screenshot_id,omitempty"`
		InputMode    *string `json:"input_mode,omitempty"`
	}
	pointArgs struct {
		Index *int     `json:"index,omitempty"`
		X     *float64 `json:"x,omitempty"`
		Y     *float64 `json:"y,omitempty"`
	}
	clickArgs struct {
		boundArgs
		pointArgs
		Button      *string `json:"button,omitempty"`
		Count       *int    `json:"count,omitempty"`
		HoldMS      *int    `json:"hold_ms,omitempty"`
		HoverMS     *int    `json:"hover_ms,omitempty"`
		VerifyAfter *bool   `json:"verify_after,omitempty"`
	}
	typeArgs struct {
		boundArgs
		pointArgs
		Text   string `json:"text"`
		Clear  *bool  `json:"clear,omitempty"`
		Submit *bool  `json:"submit,omitempty"`
	}
	keyArgs struct {
		boundArgs
		Keys string `json:"keys"`
	}
	scrollArgs struct {
		boundArgs
		pointArgs
		DeltaY *int `json:"delta_y,omitempty"`
		DeltaX *int `json:"delta_x,omitempty"`
		Page   *int `json:"page,omitempty"`
	}
)

// fakeDir is a private folder with a short path: a socket path holds at most
// 104 bytes on macOS.
func fakeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pcu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func rect(x, y, w, h float64) map[string]any {
	return map[string]any{"x": x, "y": y, "width": w, "height": h}
}

// doctorReply is a ready service's doctor: DesktopController's fields with the
// ones MCPHost adds.
func doctorReply() map[string]any {
	return map[string]any{
		"accessibility": true, "screen_recording": true, "post_events": true, "permission_prompts_requested": false,
		"platform": "macOS", "snapshot_ttl_seconds": 30, "default_input_mode": "background",
		"background_input": map[string]any{"mouse_symbols_available": true, "keyboard_symbols_available": true, "keyboard_layout_supported": true,
			"uses_private_api": true, "delivery_verified": false, "foreground_fallback": false},
		"supported_keys": []string{"down", "end", "escape", "home", "pagedown", "tab"},
		"service":        map[string]any{"running": true, "paused": false, "background_enabled": true, "clients": 1, "socket": "runtime.sock"},
		"tool_count":     22, "input_monitoring": false,
		"desktop_session": map[string]any{"available": true, "locked": false, "on_console": true},
		"frontmost_pid":   501, "pointer": map[string]any{"x": 10.0, "y": 20.0},
	}
}

func serveFake(t *testing.T, dir string) *fakeCU {
	t.Helper()
	sock := filepath.Join(dir, "runtime.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	// The service makes its socket 0600 once bound (ResidentService.swift).
	if err := os.Chmod(sock, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeCU{ln: ln, doctor: doctorReply(), args: map[string]map[string]any{}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "personal-cu", Version: "0.2.0"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			// The one request a Go client sends before initialize.
			if method == "server/discover" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return next(ctx, method, req)
		}
	})
	addTool(f, srv, "doctor", func(noArgs) (any, []byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.doctor, nil, nil
	})
	addTool(f, srv, "list_apps", func(appsArgs) (any, []byte, error) {
		return map[string]any{"running_only": true, "apps": []map[string]any{
			{"name": "Google Chrome", "pid": 4242, "running": true, "frontmost": false, "bundle_id": "com.google.Chrome", "path": "/Applications/Google Chrome.app"},
		}}, nil, nil
	})
	addTool(f, srv, "list_windows", func(windowsArgs) (any, []byte, error) {
		return map[string]any{"windows": []map[string]any{
			{"window_id": 77, "pid": 4242, "title": "Apply - Example", "rect": rect(100, 50, 800, 600), "on_screen": true, "layer": 0},
			{"window_id": 78, "pid": 4242, "title": "", "rect": rect(0, 0, 1512, 24), "on_screen": true, "layer": 25},
			{"window_id": 90, "pid": 88, "title": "Banner", "rect": rect(1200, 30, 300, 80), "on_screen": true, "layer": 0},
		}}, nil, nil
	})
	addTool(f, srv, "get_window_state", func(stateArgs) (any, []byte, error) { return windowState(), fakePNG, nil })
	addTool(f, srv, "click", func(clickArgs) (any, []byte, error) { return f.act("click", nil) })
	addTool(f, srv, "type_text", func(in typeArgs) (any, []byte, error) {
		// A field that formats what it receives reads back other text.
		if in.Text == "2 weeks" {
			return f.act("type_text", map[string]any{"status": "readback_mismatch", "readback": "2 Weeks", "matches_expected": false})
		}
		return f.act("type_text", map[string]any{"status": "verified", "readback": in.Text, "matches_expected": true})
	})
	addTool(f, srv, "press_key", func(keyArgs) (any, []byte, error) {
		return f.act("press_key", map[string]any{"chords": []map[string]any{{"route": "key_event", "status": "posted_unverified"}}})
	})
	addTool(f, srv, "scroll", func(scrollArgs) (any, []byte, error) { return f.act("scroll", nil) })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			ss, err := srv.Connect(context.Background(), &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
			f.mu.Lock()
			f.conns++
			if err == nil {
				f.sessions = append(f.sessions, ss)
			}
			f.mu.Unlock()
		}
	}()
	t.Cleanup(f.stop)
	return f
}

// addTool registers a tool whose arguments must fit In, records them, and
// answers with the JSON and, when there is one, the PNG.
func addTool[In any](f *fakeCU, srv *mcp.Server, name string, answer func(In) (any, []byte, error)) {
	mcp.AddTool(srv, &mcp.Tool{Name: name}, func(_ context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		var raw map[string]any
		_ = json.Unmarshal(req.Params.Arguments, &raw)
		f.mu.Lock()
		f.args[name] = raw
		f.mu.Unlock()
		reply, png, err := answer(in)
		if err != nil {
			return nil, nil, err
		}
		text, _ := json.Marshal(reply)
		res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}
		if png != nil {
			res.Content = append(res.Content, &mcp.ImageContent{Data: png, MIMEType: "image/png"})
		}
		return res, nil, nil
	})
}

// act answers an action as DesktopController's posted() does, or refuses it
// with the words of the service.
func (f *fakeCU) act(operation string, extra map[string]any) (any, []byte, error) {
	f.mu.Lock()
	refusal := f.refusal
	f.mu.Unlock()
	if refusal != "" {
		return nil, nil, errors.New(refusal)
	}
	reply := map[string]any{"status": "posted_unverified", "operation": operation, "pid": 4242,
		"verification": "Events were posted to the target process; request get_app_state to inspect the result."}
	maps.Copy(reply, extra)
	return reply, nil, nil
}

// windowState is get_window_state of a 800 by 600 point window whose
// screenshot is 1600 by 900 pixels, so the two axes scale apart.
func windowState() map[string]any {
	elements := []map[string]any{
		{"index": 0, "depth": 0, "role": "AXWindow", "title": "Apply - Example", "secure": false, "rect": rect(100, 50, 800, 600), "enabled": true, "actions": []string{"AXRaise"}, "value_writable": false},
		{"index": 1, "depth": 1, "parent": 0, "role": "AXButton", "title": "Next", "secure": false, "rect": rect(300, 150, 100, 40), "enabled": true, "actions": []string{"AXPress"}, "value_writable": false},
		{"index": 2, "depth": 1, "parent": 0, "role": "AXTextField", "description": "Cover note", "secure": false, "value": strings.Repeat("é", 300), "rect": rect(120, 300, 400, 30), "enabled": true, "actions": []string{}, "value_writable": true},
		{"index": 3, "depth": 1, "parent": 0, "role": "AXTextField", "subrole": "AXSecureTextField", "title": "Password", "secure": true, "rect": rect(120, 340, 400, 30), "enabled": true, "actions": []string{}, "value_writable": false},
		{"index": 4, "depth": 1, "parent": 0, "role": "AXCheckBox", "title": "Remember me", "secure": false, "value": 1, "rect": rect(120, 380, 20, 20), "enabled": false, "actions": []string{"AXPress"}, "value_writable": false},
	}
	return map[string]any{
		"snapshot_id": "snap-1", "pid": 4242, "expires_in_seconds": 30,
		"window":   map[string]any{"title": "Apply - Example", "rect": rect(100, 50, 800, 600), "ax_index": 0, "window_id": 77},
		"elements": elements, "tree_truncated": false, "ax_scope": "window", "has_ax_window": true,
		"offset": 0, "limit": 4000, "total": len(elements), "has_more": false, "diff": false, "removed_indices": []int{}, "frontmost": false,
		"limits":    map[string]any{"nodes": 4000, "depth": 64, "seconds": 5, "text_bytes": 2 * 1024 * 1024},
		"window_id": 77, "screenshot_id": "shot-1",
		"screenshot": map[string]any{"screenshot_id": "shot-1", "pixel_width": 1600, "pixel_height": 900, "coordinate_space": "screenshot_pixels",
			"origin": "top_left", "window_rect_points": rect(100, 50, 800, 600)},
		"mode": "full", "window_count": 1,
		"windows": []map[string]any{{"window_id": 77, "pid": 4242, "title": "Apply - Example", "rect": rect(100, 50, 800, 600), "on_screen": true, "layer": 0}},
	}
}

func (f *fakeCU) argsOf(tool string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.args[tool]
}

func (f *fakeCU) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// stop quits the service: its socket goes and every connection closes.
func (f *fakeCU) stop() {
	_ = f.ln.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ss := range f.sessions {
		_ = ss.Close()
	}
	f.sessions = nil
}

func TestClient_StatusReadsTheDoctorInPriorityOrder(t *testing.T) {
	dir := fakeDir(t)
	f := serveFake(t, dir)
	c := &Client{dir: dir, uid: os.Getuid()}
	set := func(d map[string]any, path string, v any) {
		parent, key, nested := strings.Cut(path, ".")
		if nested {
			d[parent].(map[string]any)[key] = v
			return
		}
		d[path] = v
	}
	for _, row := range []struct {
		name   string
		doctor map[string]any // changes to a ready service's doctor
		want   Health
	}{
		{"ready", nil, HealthReady},
		{"stopping, without permissions", map[string]any{"service.running": false, "accessibility": false}, HealthNotRunning},
		{"no Accessibility", map[string]any{"accessibility": false}, HealthPermissions},
		{"no Screen Recording, locked and paused", map[string]any{"screen_recording": false, "desktop_session.locked": true, "service.paused": true}, HealthPermissions},
		{"locked and paused", map[string]any{"desktop_session.locked": true, "service.paused": true}, HealthLocked},
		{"paused in PersonalCU", map[string]any{"service.paused": true}, HealthPaused},
		{"background control off", map[string]any{"service.background_enabled": false}, HealthPaused},
		// Background keys and wheel need macOS 26 on arm64 (BackgroundInput.swift).
		{"no background keyboard on this Mac", map[string]any{"background_input.keyboard_layout_supported": false}, HealthUnsupported},
		{"no keyboard symbols", map[string]any{"background_input.keyboard_symbols_available": false}, HealthUnsupported},
		{"paused and no background keyboard", map[string]any{"service.paused": true, "background_input.keyboard_layout_supported": false}, HealthPaused},
	} {
		d := doctorReply()
		for path, v := range row.doctor {
			set(d, path, v)
		}
		f.mu.Lock()
		f.doctor = d
		f.mu.Unlock()
		st := c.Status(context.Background())
		if st.Health != row.want || (st.Reason == "") != (row.want == HealthReady) {
			t.Errorf("%s: %+v, want %s with a reason unless ready", row.name, st, row.want)
		}
		// Agent control tells the switched-off background control from a pause.
		if off := row.name == "background control off"; st.BackgroundOff != off {
			t.Errorf("%s: background off reads %v", row.name, st.BackgroundOff)
		}
	}
}

func TestClient_WindowsLookAndActSpeakTheServiceProtocol(t *testing.T) {
	dir := fakeDir(t)
	f := serveFake(t, dir)
	c := &Client{dir: dir, uid: os.Getuid()}
	ctx := context.Background()

	ws, err := c.Windows(ctx)
	want := []Window{{ID: 77, PID: 4242, App: "Google Chrome", BundleID: "com.google.Chrome", Title: "Apply - Example", Rect: Rect{100, 50, 800, 600}, OnScreen: true}}
	if err != nil || !slices.Equal(ws, want) {
		t.Fatalf("windows %+v, %v; want only the app's normal window", ws, err)
	}
	if f.argsOf("list_apps")["running_only"] != true {
		t.Errorf("list_apps %v", f.argsOf("list_apps"))
	}

	look, err := c.Look(ctx, ws[0])
	if err != nil {
		t.Fatal(err)
	}
	if a := f.argsOf("get_window_state"); !maps.Equal(a, map[string]any{"window_id": 77.0, "pid": 4242.0, "include_screenshot": true, "include_text": true, "disable_diff": true}) {
		t.Errorf("get_window_state %v: a look reads the whole tree and the screenshot of that window", a)
	}
	if look.SnapshotID != "snap-1" || look.ScreenshotID != "shot-1" || !bytes.Equal(look.PNG, fakePNG) || look.PixelWidth != 1600 || look.PixelHeight != 900 {
		t.Errorf("look %q %q %q %dx%d", look.SnapshotID, look.ScreenshotID, look.PNG, look.PixelWidth, look.PixelHeight)
	}
	// ScreenshotGeometry.globalPoint of Models.swift, which the service applies
	// to the pixel an action names.
	globalPoint := func(x, y float64) [2]float64 { return [2]float64{100 + x*800/1600, 50 + y*600/900} }
	next, _ := look.Element(1)
	r := next.Rect
	if got := [2][2]float64{globalPoint(r.X, r.Y), globalPoint(r.X+r.Width, r.Y+r.Height)}; got != [2][2]float64{{300, 150}, {400, 190}} {
		t.Errorf("Next at %+v pixels maps back to %v, not its corners in points", r, got)
	}
	if note, _ := look.Element(2); note.Value != strings.Repeat("é", MaxValueRunes) {
		t.Errorf("a long value keeps %d runes", len([]rune(note.Value)))
	}
	if box, _ := look.Element(4); box.Value != "1" || box.Enabled {
		t.Errorf("checkbox %+v", box)
	}

	// Every optional argument changes what the service does (submit presses
	// Return, verify_after captures again), so an action sends exactly these.
	for _, row := range []struct {
		name   string
		act    Action
		tool   string
		args   map[string]any
		status string
	}{
		{"press a control", Action{Kind: ActClick, Index: 1}, "click", map[string]any{"index": 1.0}, "posted_unverified"},
		{"press a point", Action{Kind: ActClick, Point: true, X: 412.5, Y: 230}, "click", map[string]any{"x": 412.5, "y": 230.0}, "posted_unverified"},
		{"type into a field", Action{Kind: ActType, Index: 2, Text: "two weeks"}, "type_text", map[string]any{"index": 2.0, "text": "two weeks"}, "verified"},
		{"type text the field reads back otherwise", Action{Kind: ActType, Index: 2, Text: "2 weeks"}, "type_text", map[string]any{"index": 2.0, "text": "2 weeks"}, StatusReadbackMismatch},
		{"a key as the gate spells it", Action{Kind: ActKeys, Keys: "Page Down"}, "press_key", map[string]any{"keys": "pagedown"}, "posted_unverified"},
		{"scroll down", Action{Kind: ActScroll, Index: 0, Down: true, Pages: 2}, "scroll", map[string]any{"index": 0.0, "page": -2.0}, "posted_unverified"},
	} {
		row.act.Look = look
		out, err := c.Act(ctx, row.act)
		args := map[string]any{"pid": 4242.0, "snapshot_id": "snap-1", "screenshot_id": "shot-1", "input_mode": "background"}
		maps.Copy(args, row.args)
		if got := f.argsOf(row.tool); err != nil || !maps.Equal(got, args) {
			t.Errorf("%s: %s %v, %v; want %v", row.name, row.tool, got, err, args)
		}
		// The detail is for the agent, which has none of the service's own
		// tools, and never repeats the typed text or the field's readback.
		if out.Status != row.status || out.Detail == "" || strings.Contains(out.Detail, "get_app_state") ||
			strings.Contains(strings.ToLower(out.Detail), "weeks") {
			t.Errorf("%s: outcome %+v, want status %s", row.name, out, row.status)
		}
	}

	f.mu.Lock()
	f.refusal = "stale_snapshot: Unknown or expired snapshot_id; request get_app_state"
	f.mu.Unlock()
	_, err = c.Act(ctx, Action{Kind: ActClick, Look: look, Index: 1})
	var se *ServiceError
	if !errors.As(err, &se) || !se.Stale() || se.Message != "Unknown or expired snapshot_id; request get_app_state" {
		t.Errorf("a stale snapshot is %#v", err)
	}
}

func TestClient_ConnectsOnlyToAPrivateSocketOfThisUser(t *testing.T) {
	for _, row := range []struct {
		name  string
		setup func(t *testing.T, f *fakeCU, dir string) (clientDir string, uid int)
		ready bool
	}{
		{"private and yours", func(t *testing.T, f *fakeCU, dir string) (string, int) { return dir, os.Getuid() }, true},
		{"the socket open to others", func(t *testing.T, f *fakeCU, dir string) (string, int) {
			chmod(t, filepath.Join(dir, "runtime.sock"), 0o666)
			return dir, os.Getuid()
		}, false},
		{"the folder open to others", func(t *testing.T, f *fakeCU, dir string) (string, int) {
			chmod(t, dir, 0o755)
			return dir, os.Getuid()
		}, false},
		{"the folder a link", func(t *testing.T, f *fakeCU, dir string) (string, int) {
			link := dir + "-link"
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(link) })
			return link, os.Getuid()
		}, false},
		{"another user's", func(t *testing.T, f *fakeCU, dir string) (string, int) { return dir, os.Getuid() + 1 }, false},
		{"a socket nobody listens on", func(t *testing.T, f *fakeCU, dir string) (string, int) {
			f.ln.(*net.UnixListener).SetUnlinkOnClose(false)
			f.stop()
			return dir, os.Getuid()
		}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			dir := fakeDir(t)
			f := serveFake(t, dir)
			cdir, uid := row.setup(t, f, dir)
			c := &Client{dir: cdir, uid: uid}
			st := c.Status(context.Background())
			_, err := c.Windows(context.Background())
			if row.ready {
				if st.Health != HealthReady || err != nil || f.connections() != 1 {
					t.Fatalf("%+v, %v, %d connections", st, err, f.connections())
				}
				return
			}
			if st.Health != HealthNotRunning || st.Reason == "" || !errors.Is(err, ErrNotRunning) || f.connections() != 0 {
				t.Errorf("%+v, %v, %d connections: the client connected", st, err, f.connections())
			}
		})
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// One connection per process, made on first use and made again after it
// drops (computer-use spec, "Computer-use service").
func TestClient_KeepsOneConnectionAndMakesItAgainAfterARestart(t *testing.T) {
	dir := fakeDir(t)
	first := serveFake(t, dir)
	c := &Client{dir: dir, uid: os.Getuid()}
	ctx := context.Background()
	for range 2 {
		if st := c.Status(ctx); st.Health != HealthReady {
			t.Fatalf("%+v", st)
		}
	}
	if n := first.connections(); n != 1 {
		t.Fatalf("%d connections for two reads", n)
	}
	first.stop()
	second := serveFake(t, dir)
	// The client learns of the drop when its reader of the old connection
	// ends, which may come after the service is back.
	var st Status
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if st = c.Status(ctx); st.Health == HealthReady {
			break
		}
	}
	if st.Health != HealthReady || second.connections() != 1 {
		t.Fatalf("after PersonalCU restarted: %+v, %d connections", st, second.connections())
	}
}
