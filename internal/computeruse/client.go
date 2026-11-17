//go:build darwin

package computeruse

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"

	"applysmith/internal/gates"
)

// How long each call may take. A doctor answers at once, even during an
// action; a look reads a whole accessibility tree and captures the window; an
// action waits in the service's one queue for every desktop client.
const (
	statusTimeout  = 3 * time.Second
	windowsTimeout = 5 * time.Second
	lookTimeout    = 15 * time.Second
	actTimeout     = 25 * time.Second
)

// protocolVersion is the version initialize asks for. The service's server is
// strict and leaves any request before initialize unanswered, and a client of
// a newer version would first probe with server/discover and wait on it.
const protocolVersion = "2025-11-25"

// Client is this process's connection to the service on its socket. It
// connects on first use and again after the connection drops.
type Client struct {
	dir string
	uid int // the user the folder, the socket and the peer must belong to

	mu sync.Mutex
	cs *mcp.ClientSession
}

var shared = struct {
	sync.Mutex
	clients map[string]*Client
}{clients: map[string]*Client{}}

func init() { openClient = openShared }

func openShared(dir string) Service {
	shared.Lock()
	defer shared.Unlock()
	c := shared.clients[dir]
	if c == nil {
		c = &Client{dir: dir, uid: os.Getuid()}
		shared.clients[dir] = c
	}
	return c
}

// Status reads the service's doctor. The order is what blocks the agent
// first: no service, missing permissions, a locked session, a pause.
func (c *Client) Status(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var d struct {
		Accessibility   bool `json:"accessibility"`
		ScreenRecording bool `json:"screen_recording"`
		Service         struct {
			Running           bool `json:"running"`
			Paused            bool `json:"paused"`
			BackgroundEnabled bool `json:"background_enabled"`
		} `json:"service"`
		Session struct {
			Locked bool `json:"locked"`
		} `json:"desktop_session"`
		Input struct {
			Symbols bool `json:"keyboard_symbols_available"`
			Layout  bool `json:"keyboard_layout_supported"`
		} `json:"background_input"`
	}
	if err := c.callJSON(ctx, "doctor", map[string]any{}, &d); err != nil {
		return Status{Health: HealthNotRunning, Reason: err.Error()}
	}
	s := Status{Health: HealthReady, Accessibility: d.Accessibility, ScreenRecording: d.ScreenRecording, ServicePaused: d.Service.Paused,
		BackgroundOff: d.Service.Running && !d.Service.BackgroundEnabled}
	var missing []string
	if !d.Accessibility {
		missing = append(missing, "Accessibility")
	}
	if !d.ScreenRecording {
		missing = append(missing, "Screen Recording")
	}
	switch {
	case !d.Service.Running:
		s.Health, s.Reason = HealthNotRunning, "PersonalCU is stopping; open PersonalCU.app again."
	case len(missing) > 0:
		s.Health, s.Reason = HealthPermissions, "Grant PersonalCU "+strings.Join(missing, " and ")+" in System Settings, Privacy & Security, then quit and reopen PersonalCU."
	case d.Session.Locked:
		s.Health, s.Reason = HealthLocked, "The Mac is locked; unlock it so the agent can act."
	case d.Service.Paused:
		s.Health, s.Reason = HealthPaused, "Control is paused in PersonalCU; resume it from PersonalCU's menu bar icon."
	case !d.Service.BackgroundEnabled:
		// The service then refuses every background action, the only kind the
		// agent sends.
		s.Health, s.Reason = HealthPaused, "Background control is off in PersonalCU; turn on Allow background control in its window."
	case !d.Input.Symbols || !d.Input.Layout:
		// Background keys and scrolls need macOS 26 on Apple silicon
		// (BackgroundInput.swift); without them only clicks work.
		s.Health, s.Reason = HealthUnsupported, "PersonalCU cannot type, press keys or scroll in the background on this Mac (it needs macOS 26 on Apple silicon); the agent can only click."
	}
	return s
}

// Windows lists the normal windows of running apps, with each app's name and
// bundle id. Menus, the menu bar, the Dock and overlays sit on other window
// layers; a process that is not an app has no bundle the gate could name.
func (c *Client) Windows(ctx context.Context) ([]Window, error) {
	ctx, cancel := context.WithTimeout(ctx, windowsTimeout)
	defer cancel()
	var ws struct {
		Windows []struct {
			ID       int64    `json:"window_id"`
			PID      int      `json:"pid"`
			Title    string   `json:"title"`
			Rect     rectJSON `json:"rect"`
			OnScreen bool     `json:"on_screen"`
			Layer    int      `json:"layer"`
		} `json:"windows"`
	}
	if err := c.callJSON(ctx, "list_windows", map[string]any{}, &ws); err != nil {
		return nil, err
	}
	type app struct {
		PID      int    `json:"pid"`
		Name     string `json:"name"`
		BundleID string `json:"bundle_id"`
	}
	var apps struct {
		Apps []app `json:"apps"`
	}
	if err := c.callJSON(ctx, "list_apps", map[string]any{"running_only": true}, &apps); err != nil {
		return nil, err
	}
	byPID := map[int]app{}
	for _, a := range apps.Apps {
		byPID[a.PID] = a
	}
	out := []Window{}
	for _, w := range ws.Windows {
		a, ok := byPID[w.PID]
		if !ok || w.Layer != 0 {
			continue
		}
		out = append(out, Window{ID: w.ID, PID: w.PID, App: a.Name, BundleID: a.BundleID, Title: w.Title, Rect: Rect(w.Rect), OnScreen: w.OnScreen})
	}
	return out, nil
}

// Look reads one window: its whole control tree (no diff against an earlier
// look) and its screenshot. Each control's rectangle arrives in global screen
// points and leaves in screenshot pixels.
func (c *Client) Look(ctx context.Context, w Window) (Look, error) {
	ctx, cancel := context.WithTimeout(ctx, lookTimeout)
	defer cancel()
	// Taken before the call, so the look expires no later than the snapshot
	// the service creates.
	at := time.Now()
	res, err := c.call(ctx, "get_window_state", map[string]any{"window_id": w.ID, "pid": w.PID,
		"include_screenshot": true, "include_text": true, "disable_diff": true})
	if err != nil {
		return Look{}, err
	}
	var st struct {
		SnapshotID   string `json:"snapshot_id"`
		ScreenshotID string `json:"screenshot_id"`
		PID          int    `json:"pid"`
		Window       struct {
			Title string   `json:"title"`
			Rect  rectJSON `json:"rect"`
		} `json:"window"`
		Elements []struct {
			Index       int             `json:"index"`
			Role        string          `json:"role"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			Value       json.RawMessage `json:"value"`
			Secure      bool            `json:"secure"`
			Enabled     *bool           `json:"enabled"`
			Rect        *rectJSON       `json:"rect"`
		} `json:"elements"`
		Truncated  bool `json:"tree_truncated"`
		Screenshot *struct {
			PixelWidth  int      `json:"pixel_width"`
			PixelHeight int      `json:"pixel_height"`
			Window      rectJSON `json:"window_rect_points"`
		} `json:"screenshot"`
	}
	if err := decodeText(res, "get_window_state", &st); err != nil {
		return Look{}, err
	}
	g := st.Screenshot
	if g == nil || g.PixelWidth <= 0 || g.PixelHeight <= 0 || g.Window.Width <= 0 || g.Window.Height <= 0 {
		return Look{}, errors.New("PersonalCU returned the window without its screenshot geometry")
	}
	// The inverse of ScreenshotGeometry.globalPoint (Models.swift), which
	// maps a pixel to window origin + pixel * window points / image pixels.
	sx, sy := float64(g.PixelWidth)/g.Window.Width, float64(g.PixelHeight)/g.Window.Height
	look := Look{Window: w, SnapshotID: st.SnapshotID, ScreenshotID: st.ScreenshotID, Truncated: st.Truncated,
		PixelWidth: g.PixelWidth, PixelHeight: g.PixelHeight, At: at}
	look.Window.Title, look.Window.Rect, look.Window.PID = st.Window.Title, Rect(st.Window.Rect), cmp.Or(st.PID, w.PID)
	for _, e := range st.Elements {
		el := Element{Index: e.Index, Role: e.Role, Title: e.Title, Description: e.Description, Enabled: e.Enabled == nil || *e.Enabled, Secure: e.Secure}
		if !e.Secure {
			el.Value = cut(valueText(e.Value), MaxValueRunes)
		}
		if r := e.Rect; r != nil {
			el.Rect = Rect{X: (r.X - g.Window.X) * sx, Y: (r.Y - g.Window.Y) * sy, Width: r.Width * sx, Height: r.Height * sy}
		}
		look.Elements = append(look.Elements, el)
	}
	for _, ct := range res.Content {
		if img, ok := ct.(*mcp.ImageContent); ok && img.MIMEType == "image/png" {
			look.PNG = img.Data
			break
		}
	}
	return look, nil
}

// Act forwards one action the gate allowed, bound to the snapshot and
// screenshot of its look. Every action is delivered in the background; none
// activates, raises or launches anything.
func (c *Client) Act(ctx context.Context, a Action) (Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, actTimeout)
	defer cancel()
	args := map[string]any{"pid": a.Look.Window.PID, "snapshot_id": a.Look.SnapshotID, "screenshot_id": a.Look.ScreenshotID, "input_mode": "background"}
	at := func() {
		if a.Point {
			args["x"], args["y"] = a.X, a.Y
		} else {
			args["index"] = a.Index
		}
	}
	var tool string
	switch a.Kind {
	case ActClick:
		tool = "click"
		at()
	case ActType:
		tool = "type_text"
		at()
		args["text"] = a.Text
	case ActKeys:
		// The gate decided on this spelling, which is also the key DSL's
		// (KeyDSL.swift): named keys joined by "+" with no space, which the
		// DSL would read as the start of a second chord.
		tool = "press_key"
		args["keys"] = gates.NormalizeChord(a.Keys)
	case ActScroll:
		tool = "scroll"
		at()
		// A positive page scrolls up (ToolCatalog.swift).
		args["page"] = a.Pages
		if a.Down {
			args["page"] = -a.Pages
		}
	default:
		return Outcome{}, fmt.Errorf("computeruse: %q is not a desktop action", a.Kind)
	}
	// The reply of type_text also reads the field back; only the status is
	// kept, never what was typed or read back. The service's own summary names
	// its tools, which the agent does not have, so the detail is said here.
	var r struct {
		Status string `json:"status"`
	}
	if err := c.callJSON(ctx, tool, args, &r); err != nil {
		return Outcome{}, err
	}
	return Outcome{Status: r.Status, Detail: cmp.Or(statusDetail[r.Status], "PersonalCU answered "+r.Status)}, nil
}

// statusDetail says what each status of an action means.
var statusDetail = map[string]string{
	"verified":             "PersonalCU read the result back and it matched",
	"performed":            "accessibility accepted the action; nothing read the result back",
	"posted_unverified":    "the input events were sent; nothing read the result back",
	StatusReadbackMismatch: "the text was typed, but the field reads back other text than expected",
}

type rectJSON struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// valueText is a control's value as text: the service sends a string or, for
// a checkbox or a slider, a number.
func valueText(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (c *Client) callJSON(ctx context.Context, tool string, args map[string]any, out any) error {
	res, err := c.call(ctx, tool, args)
	if err != nil {
		return err
	}
	return decodeText(res, tool, out)
}

// call runs one tool of the service. A reply the service marks as an error
// is a *ServiceError.
func (c *Client) call(ctx context.Context, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	cs, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	switch {
	case err != nil && ctx.Err() != nil:
		// Not ErrNotRunning: an action that timed out may still have happened.
		return nil, fmt.Errorf("PersonalCU did not answer %s in time; it may be busy with another action: %w", tool, ctx.Err())
	case err != nil:
		return nil, fmt.Errorf("PersonalCU %s: %w", tool, err)
	case res.IsError:
		return nil, serviceError(res)
	}
	return res, nil
}

// decodeText reads the JSON the service puts in the first text content of a
// reply (MCPHost.swift).
func decodeText(res *mcp.CallToolResult, tool string, out any) error {
	for _, ct := range res.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			if err := json.Unmarshal([]byte(t.Text), out); err != nil {
				return fmt.Errorf("PersonalCU %s answered %w", tool, err)
			}
			return nil
		}
	}
	return fmt.Errorf("PersonalCU %s answered no text", tool)
}

// serviceCodes prefix the service's refusals (LocalCUError in Models.swift).
var serviceCodes = []string{"invalid_argument", "permission_required", "target_error", "stale_snapshot", "accessibility_error", "operation_failed"}

func serviceError(res *mcp.CallToolResult) *ServiceError {
	var text string
	for _, ct := range res.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			text = t.Text
			break
		}
	}
	if code, msg, ok := strings.Cut(text, ": "); ok && slices.Contains(serviceCodes, code) {
		return &ServiceError{Code: code, Message: msg}
	}
	// The pause, the background switch and the argument checks of the service
	// answer in plain words.
	return &ServiceError{Code: "operation_failed", Message: text}
}

// session is the open session, or a new one.
func (c *Client) session(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cs != nil {
		return c.cs, nil
	}
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "applysmith", Version: "1"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: conn}, &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion})
	if err != nil {
		_ = conn.Close()
		return nil, unreachable("PersonalCU did not answer the connection (" + err.Error() + "); quit PersonalCU and open it again.")
	}
	c.cs = cs
	// Once the connection ends, as when the service quits, the next call
	// connects again.
	go func() {
		_ = cs.Wait()
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.cs == cs {
			c.cs = nil
		}
	}()
	return cs, nil
}

// connect dials the socket when it and its folder are this user's alone, and
// keeps the connection when the process that answers runs as this user: the
// service's own rule (RuntimeEndpoint, ResidentService.swift).
func (c *Client) connect(ctx context.Context) (net.Conn, error) {
	sock := filepath.Join(c.dir, "runtime.sock")
	if err := c.private(c.dir, fs.ModeDir, 0o700); err != nil {
		return nil, err
	}
	if err := c.private(sock, fs.ModeSocket, 0o600); err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		// A socket nobody listens on is left by a service that stopped.
		return nil, ErrNotRunning
	}
	var cred *unix.Xucred
	var credErr error
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err == nil {
		err = raw.Control(func(fd uintptr) {
			cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		})
	}
	if err != nil || credErr != nil || int(cred.Uid) != c.uid {
		_ = conn.Close()
		return nil, unreachable("The process behind PersonalCU's socket does not run as you, so Applysmith does not use it; quit it and open your own PersonalCU.app.")
	}
	return conn, nil
}

// private refuses a path that is missing, a link, of another kind, another
// user's, or open to others.
func (c *Client) private(path string, kind, perm fs.FileMode) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotRunning
	}
	if err != nil {
		return unreachable("PersonalCU's " + path + " cannot be read (" + err.Error() + ").")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if fi.Mode().Type() != kind || fi.Mode().Perm() != perm || !ok || int(st.Uid) != c.uid {
		return unreachable(fmt.Sprintf("%s is not yours alone (owner you, mode %#o), so Applysmith does not connect; quit PersonalCU, fix the permissions and open it again.", path, perm))
	}
	return nil
}
