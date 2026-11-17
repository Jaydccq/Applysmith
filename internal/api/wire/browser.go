package wire

// BrowserStateStopped is the managed browser's state when no instance runs.
const BrowserStateStopped = "stopped"

// BrowserTab is one tab of a lane. URL holds origin and path only, never a
// query or fragment. Owner is the run that opened the tab, empty for a tab the
// user owns.
type BrowserTab struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Loading bool   `json:"loading"`
	Active  bool   `json:"active"`
	Owner   string `json:"owner,omitempty"`
	Kept    bool   `json:"kept"`
}

// BrowserLane is one lane's window and tabs, with the lane's tab cap.
type BrowserLane struct {
	Window bool         `json:"window"`
	TabCap SafeInt      `json:"tabCap"`
	Tabs   []BrowserTab `json:"tabs"`
}

// BrowserEvent is one entry of the browser's event log.
type BrowserEvent struct {
	At     Time   `json:"at"`
	Kind   string `json:"kind"`
	Lane   string `json:"lane,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// BrowserStatus is the state of the managed browser. Events are the newest
// 100, newest first. Reading it never starts the browser.
type BrowserStatus struct {
	State         string         `json:"state"`
	ChromeVersion string         `json:"chromeVersion,omitempty"`
	Error         string         `json:"error,omitempty"`
	Apply         BrowserLane    `json:"apply"`
	Scan          BrowserLane    `json:"scan"`
	Events        []BrowserEvent `json:"events"`
}

// BrowserShowRequest names the apply tab to activate; empty keeps the one the
// window shows.
type BrowserShowRequest struct {
	TabID string `json:"tabId,omitempty"`
}

// BrowserShown says the apply window is in front.
type BrowserShown struct {
	Shown bool `json:"shown"`
}

// BrowserStopRequest stops the browser; Force stops it even while a run holds
// a tab.
type BrowserStopRequest struct {
	Force bool `json:"force,omitempty"`
}

// BrowserNotRunning is the outcome of a stop when there was nothing to stop.
const BrowserNotRunning = "not-running"

// BrowserStopped is how a stop ended: closed, killed, detached or not-running.
type BrowserStopped struct {
	Outcome string `json:"outcome"`
}

// BrowserInputEvent is one input event the user made in the browser panel.
// Kind is mouse, wheel, key, text or fit. Coordinates are the page's CSS pixels.
// A mouse event has Type move, down or up, Button left, middle, right or
// none, the pressed Buttons (left 1, right 2, middle 4) and ClickCount; a
// wheel event has DeltaX and DeltaY; a key event has Type down or up, Key,
// Code, KeyCode and the Text a down types; a text event inserts Text as an
// input method does; fit sets the window's contents to X by Y. Modifiers:
// Alt 1, Ctrl 2, Meta 4, Shift 8.
type BrowserInputEvent struct {
	Kind       string  `json:"kind"`
	Type       string  `json:"type,omitempty"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	Button     string  `json:"button,omitempty"`
	Buttons    SafeInt `json:"buttons,omitempty"`
	ClickCount SafeInt `json:"clickCount,omitempty"`
	Modifiers  SafeInt `json:"modifiers,omitempty"`
	DeltaX     float64 `json:"deltaX,omitempty"`
	DeltaY     float64 `json:"deltaY,omitempty"`
	Key        string  `json:"key,omitempty"`
	Code       string  `json:"code,omitempty"`
	KeyCode    SafeInt `json:"keyCode,omitempty"`
	Text       string  `json:"text,omitempty"`
}

// BrowserInputRequest carries the panel's input events in the order made, at
// most 64 per request.
type BrowserInputRequest struct {
	Events []BrowserInputEvent `json:"events"`
}

// BrowserNavigateRequest moves a tab: Action is back, forward, reload, stop
// or go; go loads URL, an http or https address without credentials.
type BrowserNavigateRequest struct {
	Action string `json:"action"`
	URL    string `json:"url,omitempty"`
}

// BrowserNewTabRequest opens a tab for the user in the apply window from the
// browser panel; URL, when given, is an http or https address without
// credentials.
type BrowserNewTabRequest struct {
	URL string `json:"url,omitempty"`
}

// BrowserNewTab names the tab opened for the user.
type BrowserNewTab struct {
	TabID string `json:"tabId"`
}

// BrowserTabSent says the input or the navigation reached the tab.
type BrowserTabSent struct {
	Sent bool `json:"sent"`
}

// BrowserLiveFrame is one frame of the live view: a base64 JPEG, the page's
// viewport in CSS pixels (the space of input coordinates), the space the
// browser reserves above the page and the page's zoom.
type BrowserLiveFrame struct {
	JPEG      string  `json:"jpeg"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	OffsetTop float64 `json:"offsetTop"`
	Scale     float64 `json:"scale"`
}

// BrowserLiveTab is the toolbar state of the tab in view. URL is the full
// address, which the live view serves to this computer only. Driven is true
// while a run drives the tab, and RunID names the run that opened it.
type BrowserLiveTab struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Loading      bool   `json:"loading"`
	CanGoBack    bool   `json:"canGoBack"`
	CanGoForward bool   `json:"canGoForward"`
	Driven       bool   `json:"driven"`
	RunID        string `json:"runId,omitempty"`
}

// BrowserLiveCursor is where a run is about to click, in CSS pixels.
type BrowserLiveCursor struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// BrowserLiveFileChooser says a press of the user's asked for a file, which
// the panel cannot provide.
type BrowserLiveFileChooser struct{}

// BrowserLiveEnd ends the live view: tab-closed or browser-stopped.
type BrowserLiveEnd struct {
	Reason string `json:"reason"`
}
