package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/computeruse"
	"applysmith/internal/llm"
)

// computerUseRoutes serve Agent control's view of PersonalCU (AC-1, AC-5).
// The Pause and the screenshots accept the dashboard session alone: one
// releases the agent's hands, the other shows the user's windows.
var computerUseRoutes = []Route{
	route("GET", "/api/computer-use", reads("ComputerUseStatus")),
	route("POST", "/api/computer-use/pause", func(r *Route) { sends("ComputerUsePause", "ComputerUseStatus")(r); r.SessionOnly = true }),
	route("GET", "/api/computer-use/activity", reads("DesktopActivity")),
	route("GET", "/api/computer-use/shots/{id}", func(r *Route) { reads(PayloadAsset)(r); r.SessionOnly = true }),
}

// computerUse is the computer-use service with the agent's record of what it
// did there and the user's Pause. The agent's desktop tools take all three:
// svc to list, look and act, activity to record each decision with the
// screenshot of each look, and pause for the desktop_action gate.
type computerUse struct {
	svc      computeruse.Service
	activity *computeruse.Activity
	pause    computeruse.Pause
}

// Agent control keeps the latest 200 desktop events and 20 screenshots.
const (
	desktopEventsKept = 200
	desktopShotsKept  = 20
)

func newComputerUse(svc computeruse.Service, sessions *chatsessions.Store) computerUse {
	if svc == nil {
		svc = computeruse.Absent("PersonalCU is not connected to this server.")
	}
	return computerUse{svc: svc, activity: computeruse.NewActivity(desktopEventsKept, desktopShotsKept), pause: desktopPause{sessions}}
}

// tools is the service the agent's desktop tools are given: none where
// PersonalCU cannot be (another system, no HOME), so the model is not offered
// tools that could never work there.
func (cu computerUse) tools() computeruse.Service {
	if !computeruse.Present(cu.svc) {
		return nil
	}
	return cu.svc
}

// desktopPause keeps the Pause in the agent's settings, so it survives a
// restart. A Pause that cannot be read counts as on.
type desktopPause struct{ s *chatsessions.Store }

func (p desktopPause) Paused(ctx context.Context) (bool, error) {
	set, err := p.s.Settings(ctx)
	if err != nil {
		return true, err
	}
	return set.DesktopPaused, nil
}

func (p desktopPause) SetPaused(ctx context.Context, on bool) error {
	return p.s.SetSwitch(ctx, chatsessions.SettingDesktopPaused, on)
}

// view is the service's status with the Pause over it: while the Pause is
// on, a ready service reads paused, because the gate refuses every action.
func (cu computerUse) view(ctx context.Context) (wire.ComputerUseStatus, error) {
	paused, err := cu.pause.Paused(ctx)
	if err != nil {
		return wire.ComputerUseStatus{}, err
	}
	st := cu.svc.Status(ctx)
	if paused && st.Health == computeruse.HealthReady {
		st.Health, st.Reason = computeruse.HealthPaused, "Desktop control is paused on the Agent control page; turn Pause off there to let the agent act."
	}
	return wire.ComputerUseStatus{Health: string(st.Health), Reason: st.Reason, Accessibility: st.Accessibility, ScreenRecording: st.ScreenRecording,
		ServicePaused: st.ServicePaused, BackgroundOff: st.BackgroundOff, Paused: paused}, nil
}

func (s *Server) computerUseStatus(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	v, err := d.computerUse.view(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	s.noteComputerUse(v)
	// A look's screenshot reaches the model only when the agent route's model
	// takes images (AC-3); a route that does not resolve takes none.
	if res, err := d.router.Resolve(c.R.Context(), llm.AgentTask); err == nil {
		v.ImagesToModel = llm.TakesImages(res.Model)
	}
	return apikit.Reply{Data: v}, nil
}

func (s *Server) computerUsePause(c *apikit.Call) (apikit.Reply, error) {
	var req wire.ComputerUsePause
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := d.computerUse.pause.SetPaused(c.R.Context(), req.Paused); err != nil {
		return apikit.Reply{}, err
	}
	return s.computerUseStatus(c)
}

func (s *Server) computerUseActivity(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	a := d.computerUse.activity
	out := wire.DesktopActivity{Events: []wire.DesktopEvent{}}
	for _, e := range a.Recent() {
		w := wire.DesktopEvent{ID: e.ID, At: wire.Time{Time: e.At}, SessionID: e.SessionID, TurnID: e.TurnID, Tool: e.Tool,
			App: e.App, Window: e.Window, Role: e.Role, Control: e.Control, Verdict: e.Verdict, Reason: e.Reason, Detail: e.Detail}
		// A screenshot no longer kept is not offered, nor a place in it.
		if _, ok := a.Shot(e.ShotID); ok {
			w.ShotID = e.ShotID
			if t := e.Target; t != nil {
				w.Target = &wire.DesktopTarget{X: t.X, Y: t.Y, Width: t.Width, Height: t.Height}
			}
		}
		out.Events = append(out.Events, w)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) computerUseShot(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	png, ok := d.computerUse.activity.Shot(c.R.PathValue("id"))
	if !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "the screenshot is no longer kept")
	}
	h := c.W.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", strconv.Itoa(len(png)))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(png)
	c.Handled = true
	return apikit.Reply{}, nil
}

const computerUseName = "computer-use"

// computerUseComponent is the health line of the service, read for the
// cached list; the doctor answers at once between actions.
func (s *Server) computerUseComponent() wire.Component {
	d := s.domain.Load()
	if d == nil {
		return wire.Component{Name: computerUseName, Status: wire.ComponentUnconfigured, Detail: "starting"}
	}
	ctx, cancel := context.WithTimeout(s.bg, probeTimeout)
	defer cancel()
	v, err := d.computerUse.view(ctx)
	if err != nil {
		return wire.Component{Name: computerUseName, Status: wire.ComponentDegraded, Detail: "the Pause cannot be read: " + err.Error()}
	}
	return computerUseLine(v)
}

// computerUseLine is ok when the service is ready, unconfigured when it is
// not running, and degraded otherwise, with the health and its reason.
func computerUseLine(v wire.ComputerUseStatus) wire.Component {
	c := wire.Component{Name: computerUseName, Status: wire.ComponentDegraded, Detail: strings.ReplaceAll(v.Health, "-", " ")}
	switch computeruse.Health(v.Health) {
	case computeruse.HealthReady:
		c.Status = wire.ComponentOK
	case computeruse.HealthNotRunning:
		c.Status = wire.ComponentUnconfigured
	}
	if v.Reason != "" {
		c.Detail += ": " + v.Reason
	}
	return c
}

// noteComputerUse puts a status Agent control just read into the cached
// health list, so Home agrees with it without waiting for the next refresh.
func (s *Server) noteComputerUse(v wire.ComputerUseStatus) {
	line := computerUseLine(v)
	for {
		old := s.components.Load()
		if old == nil {
			return
		}
		i := slices.IndexFunc(old.list, func(c wire.Component) bool { return c.Name == computerUseName })
		if i < 0 {
			return
		}
		next := *old
		next.list = slices.Clone(old.list)
		next.list[i] = line
		if s.components.CompareAndSwap(old, &next) {
			return
		}
	}
}
