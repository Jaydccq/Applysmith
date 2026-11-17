// Package computeruse is the client of PersonalCU, the owner's macOS
// computer-use service (owner decisions AC-1 to AC-5). The service owns the
// macOS permissions and speaks MCP on a same-user Unix socket; this package
// connects to it, reads windows and their controls, and forwards the actions
// the Ask Agent's desktop tools decided. It decides nothing itself: the
// desktop_action gate does.
package computeruse

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Health is the state of the service as the dashboard shows it.
type Health string

const (
	HealthReady       Health = "ready"
	HealthNotRunning  Health = "not-running"
	HealthPermissions Health = "permissions"
	HealthLocked      Health = "locked"
	HealthPaused      Health = "paused"
	// HealthUnsupported is a Mac where PersonalCU cannot send background
	// keys or scrolls, so typing, keys and scrolling all fail.
	HealthUnsupported Health = "unsupported"
)

// Status is what the service's doctor tool reports.
type Status struct {
	Health Health
	// Reason is one sentence for the user that says what to do, when the
	// health is not ready.
	Reason          string
	Accessibility   bool
	ScreenRecording bool
	// ServicePaused is PersonalCU's own pause, set from its menu.
	ServicePaused bool
	// BackgroundOff is PersonalCU's Allow background control switched off:
	// it then refuses every action the agent sends.
	BackgroundOff bool
}

// Rect is a rectangle. On a Window it is in global screen points; on an
// Element it is in screenshot pixels with the origin at the screenshot's top
// left, the space a point action names.
type Rect struct {
	X, Y, Width, Height float64
}

// SameFrame reports whether a window with this rectangle is where one with o
// was. PersonalCU reports a window one point larger on every side while it is
// key than while it is not (its own sameWindowFrame, Models.swift), so a
// window that only gained or lost the keyboard focus has not moved. Each axis
// is judged alone, as there: unchanged, or grown or shrunk by one border about
// its centre.
func (r Rect) SameFrame(o Rect) bool {
	axis := func(origin, size float64) bool {
		return (math.Abs(origin) <= 0.5 && math.Abs(size) <= 0.5) || (math.Abs(origin) <= 1 && math.Abs(size+2*origin) <= 0.5)
	}
	return axis(o.X-r.X, o.Width-r.Width) && axis(o.Y-r.Y, o.Height-r.Height)
}

// Contains reports whether the point lies inside the rectangle.
func (r Rect) Contains(x, y float64) bool {
	return r.Width > 0 && r.Height > 0 && x >= r.X && y >= r.Y && x < r.X+r.Width && y < r.Y+r.Height
}

// Window is one window of a running app.
type Window struct {
	ID       int64
	PID      int
	App      string // the app's name as the system shows it
	BundleID string
	Title    string
	Rect     Rect
	OnScreen bool
}

// Element is one control of a look.
type Element struct {
	Index       int
	Role        string
	Title       string
	Description string
	// Value is cut to MaxValueRunes; a secure field's value is never read.
	Value   string
	Enabled bool
	Secure  bool
	Rect    Rect
}

// MaxValueRunes is the longest control value a look keeps.
const MaxValueRunes = 200

// Labels are the texts the desktop_action gate reads a control by.
func (e Element) Labels() []string {
	var out []string
	for _, s := range []string{e.Title, e.Description, e.Value} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Look is one observation of a window. Its snapshot authorizes one action,
// for 30 seconds, while the window keeps its place and size (the service's
// contract).
type Look struct {
	Window       Window
	SnapshotID   string
	ScreenshotID string
	Elements     []Element
	// Truncated is set when the service cut the control tree at its bounds.
	Truncated bool
	// PNG is the window's screenshot; PixelWidth and PixelHeight are its size.
	PNG                     []byte
	PixelWidth, PixelHeight int
	At                      time.Time
}

// LookLifetime is how long a look authorizes an action.
const LookLifetime = 30 * time.Second

// Element finds a control by its index.
func (l Look) Element(index int) (Element, bool) {
	for _, e := range l.Elements {
		if e.Index == index {
			return e, true
		}
	}
	return Element{}, false
}

// Fraction is a control's rectangle as fractions of the screenshot, cut to
// it, for drawing the control on the picture at any size. It is nil when the
// look has no screenshot or the control lies outside it.
func (l Look) Fraction(r Rect) *Rect {
	if l.PixelWidth <= 0 || l.PixelHeight <= 0 {
		return nil
	}
	w, h := float64(l.PixelWidth), float64(l.PixelHeight)
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.Width, w), min(r.Y+r.Height, h)
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	return &Rect{X: x0 / w, Y: y0 / h, Width: (x1 - x0) / w, Height: (y1 - y0) / h}
}

// ElementAt finds the smallest control whose rectangle holds a point in
// screenshot pixels.
func (l Look) ElementAt(x, y float64) (Element, bool) {
	var best Element
	found := false
	for _, e := range l.Elements {
		if e.Rect.Contains(x, y) && (!found || e.Rect.Width*e.Rect.Height < best.Rect.Width*best.Rect.Height) {
			best, found = e, true
		}
	}
	return best, found
}

// Action kinds, as the desktop tools name them.
const (
	ActClick  = "click"
	ActType   = "type"
	ActKeys   = "keys"
	ActScroll = "scroll"
)

// Action is one decided action, bound to the look it was decided on.
type Action struct {
	Kind string
	Look Look
	// Index names the control; when Point is set the action goes to (X, Y)
	// in screenshot pixels instead.
	Index int
	Point bool
	X, Y  float64
	Text  string // type
	Keys  string // keys
	// Down scrolls down when true and up otherwise, by Pages pages (1 to 3).
	Down  bool
	Pages int
}

// Outcome is what the service says it did. Status is verified, performed,
// posted_unverified or, after typing, StatusReadbackMismatch; Detail says what
// the status means for the agent, never typed text.
type Outcome struct {
	Status string
	Detail string
}

// StatusReadbackMismatch is a type action whose field, read back after
// typing, holds other text than the text expected.
const StatusReadbackMismatch = "readback_mismatch"

// Service is the running computer-use service.
type Service interface {
	// Status never fails: a service that cannot be reached is not-running.
	Status(ctx context.Context) Status
	Windows(ctx context.Context) ([]Window, error)
	Look(ctx context.Context, w Window) (Look, error)
	Act(ctx context.Context, a Action) (Outcome, error)
}

// ErrNotRunning is returned when no service answers on the socket.
var ErrNotRunning = errors.New("PersonalCU is not running; open PersonalCU from your Applications folder")

// unreachable is why the service cannot be used, in words that say what to
// do; errors.Is matches it to ErrNotRunning.
type unreachable string

func (u unreachable) Error() string        { return string(u) }
func (u unreachable) Is(target error) bool { return target == ErrNotRunning }

// RuntimeDir is the folder of the service's socket: PERSONAL_CU_RUNTIME_DIR
// when set, else Library/Application Support/PersonalCU in the home folder
// (RuntimeEndpoint in the service's source). It is empty when HOME is unset.
func RuntimeDir(getenv func(string) string) string {
	if d := getenv("PERSONAL_CU_RUNTIME_DIR"); d != "" {
		return d
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, "Library", "Application Support", "PersonalCU")
	}
	return ""
}

// Open returns this process's client of the service whose socket is in dir;
// every server the process starts shares its one connection. PersonalCU runs
// on macOS only, so elsewhere, as without a dir, the service is Absent.
func Open(dir string) Service {
	switch {
	case dir == "":
		return Absent("PersonalCU's folder is unknown because HOME is not set.")
	case openClient == nil:
		return Absent("PersonalCU runs on macOS only.")
	}
	return openClient(dir)
}

// openClient is the macOS client; client.go sets it, because the socket
// checks it makes exist on macOS alone.
var openClient func(dir string) Service

// Absent is a service that is not there: Status is not-running with the
// reason, and every call fails with it.
func Absent(reason string) Service { return absent(reason) }

// Present reports whether a service can be there at all: a client, even of a
// service that is not running now, and not one that is Absent.
func Present(s Service) bool {
	_, gone := s.(absent)
	return s != nil && !gone
}

type absent string

func (a absent) Status(context.Context) Status {
	return Status{Health: HealthNotRunning, Reason: string(a)}
}
func (a absent) Windows(context.Context) ([]Window, error)    { return nil, unreachable(a) }
func (a absent) Look(context.Context, Window) (Look, error)   { return Look{}, unreachable(a) }
func (a absent) Act(context.Context, Action) (Outcome, error) { return Outcome{}, unreachable(a) }

// ServiceError is a refusal the service returned, such as a stale snapshot.
type ServiceError struct {
	Code    string // invalid_argument, permission_required, target_error, stale_snapshot, accessibility_error, operation_failed
	Message string
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("PersonalCU refused: %s: %s", e.Code, e.Message)
}

// Stale reports whether the error asks for a new look.
func (e *ServiceError) Stale() bool { return e.Code == "stale_snapshot" }

// Pause is the user's switch that refuses every desktop action (AC-5). It
// is kept across restarts.
type Pause interface {
	Paused(ctx context.Context) (bool, error)
	SetPaused(ctx context.Context, on bool) error
}

// Event is one desktop decision or action, as Agent control lists it. It
// never holds typed text.
type Event struct {
	ID        string
	At        time.Time
	SessionID string
	TurnID    string
	Tool      string
	App       string
	Window    string
	Role      string
	Control   string // the label of the control acted on
	Verdict   string // allow, deny or failed
	Reason    string
	// Detail is the key the gate allowed, or a scroll's direction and pages.
	Detail string
	// ShotID is the screenshot of a look or, on an action, of the look the
	// action was decided on, with Target the control's place in it.
	ShotID string
	Target *Rect
}

// Activity keeps the latest desktop events and screenshots in memory only.
type Activity struct {
	mu        sync.Mutex
	seq       int
	events    []Event
	shots     map[string][]byte
	shotOrder []string
	maxEvents int
	maxShots  int
}

// NewActivity keeps up to maxEvents events and maxShots screenshots.
func NewActivity(maxEvents, maxShots int) *Activity {
	return &Activity{shots: map[string][]byte{}, maxEvents: maxEvents, maxShots: maxShots}
}

// Record adds an event, with the screenshot of a look when png is set, and
// returns it with its id and screenshot id filled in. An event without a
// screenshot of its own keeps the ShotID it names.
func (a *Activity) Record(e Event, png []byte) Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	e.ID = fmt.Sprintf("e%d", a.seq)
	if len(png) > 0 && a.maxShots > 0 {
		e.ShotID = fmt.Sprintf("s%d", a.seq)
		a.shots[e.ShotID] = png
		a.shotOrder = append(a.shotOrder, e.ShotID)
		for len(a.shotOrder) > a.maxShots {
			delete(a.shots, a.shotOrder[0])
			a.shotOrder = a.shotOrder[1:]
		}
	}
	a.events = append(a.events, e)
	if len(a.events) > a.maxEvents {
		a.events = slices.Clone(a.events[len(a.events)-a.maxEvents:])
	}
	return e
}

// Recent lists the kept events, newest first.
func (a *Activity) Recent() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := slices.Clone(a.events)
	slices.Reverse(out)
	return out
}

// Shot returns a kept screenshot.
func (a *Activity) Shot(id string) ([]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	png, ok := a.shots[id]
	return png, ok
}
