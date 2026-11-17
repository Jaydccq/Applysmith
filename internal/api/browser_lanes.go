package api

import (
	"context"
	"encoding/base64"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
)

// BrowserControl is the managed browser as the Browser page and the browser
// panel see it. Status, Capture and the live view never start it; Show does.
// Input and Navigate act for the user on an apply tab no run drives.
type BrowserControl interface {
	Status(ctx context.Context) browser.Status
	State() (browser.InstanceState, string)
	Survives() bool
	Capture(ctx context.Context, lane browser.Lane, tabID string, o browser.ShotOptions) (browser.Shot, error)
	Show(ctx context.Context, tabID string) error
	Stop(ctx context.Context, force bool) (browser.StopOutcome, error)
	Live(ctx context.Context, tabID string, o browser.LiveOptions, emit func(browser.LiveEvent) error) error
	Input(ctx context.Context, tabID string, events []browser.InputEvent) error
	Navigate(ctx context.Context, tabID, action, rawURL string) error
	NewTab(ctx context.Context, rawURL string) (string, error)
}

// remoteShotGap is the least time between two screenshots a remote device may
// take.
const remoteShotGap = 2 * time.Second

// shotGate allows one screenshot per gap for each key.
type shotGate struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (g *shotGate) allow(key string, now time.Time) (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last == nil {
		g.last = map[string]time.Time{}
	}
	if at, ok := g.last[key]; ok && now.Sub(at) < remoteShotGap {
		return false, max(1, int((remoteShotGap-now.Sub(at)+time.Second-1)/time.Second))
	}
	g.last[key] = now
	if len(g.last) > 256 {
		for k, at := range g.last {
			if now.Sub(at) >= remoteShotGap {
				delete(g.last, k)
			}
		}
	}
	return true, 0
}

func (s *Server) browserControl() (BrowserControl, error) {
	if s.cfg.BrowserControl == nil {
		return nil, errNoBrowser()
	}
	return s.cfg.BrowserControl, nil
}

// browserComponent is the browser's state, read without asking Chrome.
func (s *Server) browserComponent() wire.Component {
	c := wire.Component{Name: "browser", Status: wire.ComponentUnconfigured, Detail: "not set up"}
	if s.cfg.BrowserControl != nil {
		st, msg := s.cfg.BrowserControl.State()
		c.Status, c.Detail = wire.ComponentOK, string(st)
		if st == browser.InstanceFailed {
			c.Status, c.Detail = wire.ComponentDown, msg
		} else if st == browser.InstanceStopped {
			c.Detail = "starts when needed"
		}
	}
	return c
}

// browserLaneError maps a lane failure to the envelope: state conflicts to
// conflict, a tab that is gone to not_found, the rest to unavailable with the
// code as the reason.
func browserLaneError(err error) error {
	code, ok := browser.CodeOf(err)
	if !ok {
		return err
	}
	switch code {
	case browser.CodeLaneLeased, browser.CodeLaneTabLimit, browser.CodeLaneBusy, browser.CodeTabDriven:
		return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", err.Error()), string(code))
	case browser.CodeTabClosed, browser.CodeWrongLane:
		return apikit.WithReason(apikit.NewErr(wire.CodeNotFound, "no such tab"), string(code))
	case browser.CodeNotAllowed, browser.CodeSchemeNotAllowed:
		return apikit.WithReason(apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error()), string(code))
	case browser.CodeScreenshotUnavailable:
		e := apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", err.Error()), string(code))
		e.Retryable = true
		return e
	}
	return sessionError(err)
}

func (s *Server) browserStatus(c *apikit.Call) (apikit.Reply, error) {
	out := wire.BrowserStatus{State: wire.BrowserStateStopped, Events: []wire.BrowserEvent{},
		Apply: wire.BrowserLane{Tabs: []wire.BrowserTab{}}, Scan: wire.BrowserLane{Tabs: []wire.BrowserTab{}}}
	if s.cfg.BrowserControl == nil {
		return apikit.Reply{Data: out}, nil
	}
	st := s.cfg.BrowserControl.Status(c.R.Context())
	lane := func(l browser.LaneStatus) wire.BrowserLane {
		o := wire.BrowserLane{Window: l.Window, TabCap: apikit.Safe(l.Cap), Tabs: make([]wire.BrowserTab, len(l.Tabs))}
		for i, t := range l.Tabs {
			o.Tabs[i] = wire.BrowserTab{ID: t.ID, Title: t.Title, URL: t.URL, Loading: t.Loading, Active: t.Active, Owner: t.Owner, Kept: t.Kept}
		}
		return o
	}
	out.State, out.ChromeVersion, out.Error = string(st.State), st.Version, st.Error
	out.Apply, out.Scan = lane(st.Apply), lane(st.Scan)
	for _, e := range st.Events {
		out.Events = append(out.Events, wire.BrowserEvent{At: wire.Time{Time: e.Time.UTC()}, Kind: e.Kind, Lane: string(e.Lane), Detail: e.Detail})
	}
	return apikit.Reply{Data: out}, nil
}

// allowScreenshots reads the live policy: a remote device gets stills only
// while remote_access.allow_screenshots is true.
func (r *remoteRT) allowScreenshots() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur.AllowScreenshots
}

// browserScreenshot serves a still of one tab. A remote device needs the
// policy switch and takes one still per two seconds.
func (s *Server) browserScreenshot(c *apikit.Call) (apikit.Reply, error) {
	if c.P.Kind == apikit.PrincipalRemoteDevice {
		if !s.remote.allowScreenshots() {
			return apikit.Reply{}, remoteForbidden()
		}
		if ok, wait := s.shots.allow(c.P.ID, s.now()); !ok {
			return apikit.Reply{}, errRateLimited(wait, "")
		}
	}
	ctrl, err := s.browserControl()
	if err != nil {
		return apikit.Reply{}, err
	}
	lane := browser.Lane(c.R.PathValue("lane"))
	if lane != browser.LaneApply && lane != browser.LaneScan {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such lane")
	}
	q := apikit.QueryOf(c)
	opts := browser.ShotOptions{Width: q.Integer("width", 0)}
	switch f := q.Str("format"); f {
	case "", "jpeg":
	case "png":
		opts.PNG = true
	default:
		q.Fail(apikit.InvalidField("format", "format must be jpeg or png"))
	}
	if q.Err == nil && (opts.Width < 0 || opts.Width > 1600) {
		q.Fail(apikit.InvalidField("width", "width must be between 1 and 1600"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	shot, err := ctrl.Capture(c.R.Context(), lane, c.R.PathValue("tabId"), opts)
	if err != nil {
		return apikit.Reply{}, browserLaneError(err)
	}
	h := c.W.Header()
	h.Set("Content-Type", shot.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(shot.Data)))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(shot.Data)
	c.Handled = true
	return apikit.Reply{}, nil
}

func (s *Server) browserShow(c *apikit.Call) (apikit.Reply, error) {
	var req wire.BrowserShowRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	ctrl, err := s.browserControl()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := ctrl.Show(c.R.Context(), req.TabID); err != nil {
		return apikit.Reply{}, browserLaneError(err)
	}
	s.notifyTopics("browser")
	return apikit.Reply{Data: wire.BrowserShown{Shown: true}}, nil
}

func (s *Server) browserStop(c *apikit.Call) (apikit.Reply, error) {
	var req wire.BrowserStopRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	ctrl, err := s.browserControl()
	if err != nil {
		return apikit.Reply{}, err
	}
	out, err := ctrl.Stop(c.R.Context(), req.Force)
	if err != nil {
		return apikit.Reply{}, browserLaneError(err)
	}
	s.notifyTopics("browser")
	if out == "" {
		return apikit.Reply{Data: wire.BrowserStopped{Outcome: wire.BrowserNotRunning}}, nil
	}
	return apikit.Reply{Data: wire.BrowserStopped{Outcome: string(out)}}, nil
}

// applyWindowOpen opens a page for the user in the apply window.
func (s *Server) applyWindowOpen(c *apikit.Call) (apikit.Reply, error) {
	var req wire.BrowserOpenRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	u, err := userPageURL(req.URL)
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := s.refuseSyncSiteDuringRun(c.R.Context(), u); err != nil {
		return apikit.Reply{}, err
	}
	if s.cfg.OpenInApplyWindow == nil {
		return apikit.Reply{}, errNoBrowser()
	}
	if err := s.cfg.OpenInApplyWindow(c.R.Context(), u.String()); err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	return apikit.Reply{Data: wire.BrowserOpened{Opened: true}}, nil
}

// userPageURL is an address the user asked to open: http or https, with a
// host and without embedded credentials.
func userPageURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, apikit.InvalidField("url", "url must be an http or https address without embedded credentials")
	}
	return u, nil
}

// refuseSyncSiteDuringRun keeps the user off a site on the sign-in sync's
// list while an apply run drives the apply window: signing in by hand there
// would change the cookies the run is using.
func (s *Server) refuseSyncSiteDuringRun(ctx context.Context, u *url.URL) error {
	if !s.applyRunLive() {
		return nil
	}
	listed, err := s.sessionBrowser().Sites(ctx)
	if err != nil {
		return sessionError(err)
	}
	host := strings.ToLower(u.Hostname())
	if slices.ContainsFunc(listed, func(site browser.SessionSite) bool { return site.Domain != "" && inScope(host, site.Domain) }) {
		return errApplyRunLive()
	}
	return nil
}

// browserNewTab opens a tab for the user from the browser panel. It starts
// the browser when needed and, unlike the apply-window routes, raises no
// window: the panel shows the tab.
func (s *Server) browserNewTab(c *apikit.Call) (apikit.Reply, error) {
	var req wire.BrowserNewTabRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if strings.TrimSpace(req.URL) != "" {
		u, err := userPageURL(req.URL)
		if err != nil {
			return apikit.Reply{}, err
		}
		if err := s.refuseSyncSiteDuringRun(c.R.Context(), u); err != nil {
			return apikit.Reply{}, err
		}
		req.URL = u.String()
	} else {
		req.URL = ""
	}
	ctrl, err := s.browserControl()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, err := ctrl.NewTab(c.R.Context(), req.URL)
	if id != "" {
		s.notifyTopics("browser")
	}
	if err != nil {
		return apikit.Reply{}, browserLaneError(err)
	}
	return apikit.Reply{Data: wire.BrowserNewTab{TabID: id}}, nil
}

// Live view bounds: the largest frame side a client may ask for, the most
// input events in one request and the longest text one event inserts.
const (
	liveMaxSide    = 2400
	inputMaxEvents = 64
	inputMaxText   = 4096
	inputMaxCoord  = 100000
)

// browserLive streams one apply tab to the browser panel as server-sent
// events: frame, tab, cursor, filechooser and end. A failure before the first
// event is an ordinary error answer.
func (s *Server) browserLive(c *apikit.Call) (apikit.Reply, error) {
	ctrl, err := s.browserControl()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	o := browser.LiveOptions{MaxWidth: q.Integer("maxWidth", 0), MaxHeight: q.Integer("maxHeight", 0)}
	if q.Err == nil && (o.MaxWidth < 0 || o.MaxWidth > liveMaxSide) {
		q.Fail(apikit.InvalidField("maxWidth", "maxWidth must be between 1 and %d", liveMaxSide))
	}
	if q.Err == nil && (o.MaxHeight < 0 || o.MaxHeight > liveMaxSide) {
		q.Fail(apikit.InvalidField("maxHeight", "maxHeight must be between 1 and %d", liveMaxSide))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	ctx, cancel := context.WithCancelCause(c.R.Context())
	defer cancel(nil)
	conn, e := s.streams.open(c.P, cancel, s.remote.pairedCount(c.P))
	if e != nil {
		return apikit.Reply{}, e
	}
	defer s.streams.close(conn)

	rc := http.NewResponseController(c.W)
	write := func(name string, payload any) error {
		if !c.Handled {
			h := c.W.Header()
			h.Set("Content-Type", "text/event-stream")
			h.Set("Cache-Control", "no-store")
			c.W.WriteHeader(http.StatusOK)
			c.Handled = true
		}
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTTL))
		if _, err := c.W.Write(newEvent(name, payload).bytes()); err != nil {
			return err
		}
		return rc.Flush()
	}
	err = ctrl.Live(ctx, c.R.PathValue("tabId"), o, func(ev browser.LiveEvent) error {
		switch {
		case ev.Frame != nil:
			f := ev.Frame
			return write("frame", wire.BrowserLiveFrame{JPEG: base64.StdEncoding.EncodeToString(f.JPEG), Width: f.Width, Height: f.Height, OffsetTop: f.OffsetTop, Scale: f.Scale})
		case ev.Tab != nil:
			t := ev.Tab
			return write("tab", wire.BrowserLiveTab{URL: t.URL, Title: t.Title, Loading: t.Loading, CanGoBack: t.CanGoBack, CanGoForward: t.CanGoForward, Driven: t.Driven, RunID: t.RunID})
		case ev.Cursor != nil:
			return write("cursor", wire.BrowserLiveCursor{X: ev.Cursor[0], Y: ev.Cursor[1]})
		case ev.FileChooser:
			return write("filechooser", wire.BrowserLiveFileChooser{})
		case ev.End != "":
			return write("end", wire.BrowserLiveEnd{Reason: ev.End})
		}
		return nil
	})
	if err != nil && !c.Handled {
		return apikit.Reply{}, browserLaneError(err)
	}
	c.Handled = true
	return apikit.Reply{}, nil
}

var (
	inputKinds   = []string{"mouse", "wheel", "key", "text", "fit"}
	mouseTypes   = []string{"move", "down", "up"}
	mouseButtons = []string{"", "none", "left", "middle", "right"}
	keyTypes     = []string{"down", "up"}
)

// inputEvents checks the panel's events and turns them into the browser's.
func inputEvents(in []wire.BrowserInputEvent) ([]browser.InputEvent, error) {
	if len(in) == 0 || len(in) > inputMaxEvents {
		return nil, apikit.InvalidField("events", "events must hold between 1 and %d events", inputMaxEvents)
	}
	coord := func(v float64) bool { return !math.IsNaN(v) && math.Abs(v) <= inputMaxCoord }
	out := make([]browser.InputEvent, len(in))
	for n, e := range in {
		field := "events[" + strconv.Itoa(n) + "]"
		ok := slices.Contains(inputKinds, e.Kind) && coord(e.X) && coord(e.Y) && coord(e.DeltaX) && coord(e.DeltaY) && len(e.Text) <= inputMaxText
		switch e.Kind {
		case "mouse":
			ok = ok && slices.Contains(mouseTypes, e.Type) && slices.Contains(mouseButtons, e.Button)
		case "key":
			ok = ok && slices.Contains(keyTypes, e.Type) && e.Key != ""
		case "text":
			ok = ok && e.Text != ""
		case "fit":
			ok = ok && e.X >= 1 && e.Y >= 1
		}
		if !ok {
			return nil, apikit.InvalidField(field, "%s is not an input event this route takes", field)
		}
		button := e.Button
		if button == "" {
			button = "none"
		}
		out[n] = browser.InputEvent{Kind: e.Kind, Type: e.Type, X: e.X, Y: e.Y, Button: button, Buttons: int(e.Buttons), ClickCount: int(e.ClickCount),
			Modifiers: int(e.Modifiers) & 15, DeltaX: e.DeltaX, DeltaY: e.DeltaY, Key: e.Key, Code: e.Code, KeyCode: int(e.KeyCode), Text: e.Text}
	}
	return out, nil
}

// browserInput forwards the user's own clicks, keys and text from the panel to
// an apply tab. A tab a run drives refuses with tab-driven.
func (s *Server) browserInput(c *apikit.Call) (apikit.Reply, error) {
	var req wire.BrowserInputRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	events, err := inputEvents(req.Events)
	if err != nil {
		return apikit.Reply{}, err
	}
	ctrl, err := s.browserControl()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := ctrl.Input(c.R.Context(), c.R.PathValue("tabId"), events); err != nil {
		// The panel shows the message; the log keeps the browser's own cause.
		if code, _ := browser.CodeOf(err); code != browser.CodeTabDriven {
			s.log.Warn("the panel's input was not taken", "tab", c.R.PathValue("tabId"), "error", err.Error())
		}
		return apikit.Reply{}, browserLaneError(err)
	}
	return apikit.Reply{Data: wire.BrowserTabSent{Sent: true}}, nil
}

// browserNavigate runs the panel toolbar's back, forward, reload, stop and
// address bar on an apply tab no run drives.
func (s *Server) browserNavigate(c *apikit.Call) (apikit.Reply, error) {
	var req wire.BrowserNavigateRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	switch req.Action {
	case browser.NavBack, browser.NavForward, browser.NavReload, browser.NavStop:
	case browser.NavGo:
		u, err := userPageURL(req.URL)
		if err != nil {
			return apikit.Reply{}, err
		}
		if err := s.refuseSyncSiteDuringRun(c.R.Context(), u); err != nil {
			return apikit.Reply{}, err
		}
		req.URL = u.String()
	default:
		return apikit.Reply{}, apikit.InvalidField("action", "action must be back, forward, reload, stop or go")
	}
	ctrl, err := s.browserControl()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := ctrl.Navigate(c.R.Context(), c.R.PathValue("tabId"), req.Action, req.URL); err != nil {
		return apikit.Reply{}, browserLaneError(err)
	}
	return apikit.Reply{Data: wire.BrowserTabSent{Sent: true}}, nil
}
