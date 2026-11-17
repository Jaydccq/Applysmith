package wire

// ComputerUseStatus is the state of PersonalCU, the owner's computer-use
// service, and of the agent's desktop control (owner decisions AC-1, AC-5).
type ComputerUseStatus struct {
	// Health is ready, not-running, permissions, locked, paused or unsupported.
	Health string `json:"health"`
	// Reason says what to do when Health is not ready.
	Reason          string `json:"reason,omitempty"`
	Accessibility   bool   `json:"accessibility"`
	ScreenRecording bool   `json:"screenRecording"`
	// ServicePaused is PersonalCU's own pause, set from its menu.
	ServicePaused bool `json:"servicePaused"`
	// BackgroundOff is PersonalCU's Allow background control switched off.
	BackgroundOff bool `json:"backgroundOff"`
	// Paused is the Pause on the Agent control page: every desktop action is
	// refused while it is on.
	Paused bool `json:"paused"`
	// ImagesToModel is true when the agent's model takes images, so a look
	// sends the window's screenshot to it (AC-3).
	ImagesToModel bool `json:"imagesToModel"`
}

// ComputerUsePause turns the Pause of desktop control on or off.
type ComputerUsePause struct {
	Paused bool `json:"paused"`
}

// DesktopEvent is one desktop decision or action of the agent. It never
// holds text the agent typed.
type DesktopEvent struct {
	ID        string `json:"id"`
	At        Time   `json:"at"`
	SessionID string `json:"sessionId,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	// Tool is the desktop tool: desktop_windows, desktop_look, desktop_click,
	// desktop_type, desktop_keys or desktop_scroll.
	Tool   string `json:"tool"`
	App    string `json:"app,omitempty"`
	Window string `json:"window,omitempty"`
	Role   string `json:"role,omitempty"`
	// Control is the label of the control acted on.
	Control string `json:"control,omitempty"`
	// Verdict is allow, deny or failed.
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	// Detail is the key pressed, or a scroll's direction and pages.
	Detail string `json:"detail,omitempty"`
	// ShotID names the screenshot of a look or, on an action, of the look it
	// was decided on, served while it is kept in memory at
	// /api/computer-use/shots/{id}.
	ShotID string `json:"shotId,omitempty"`
	// Target is where in that screenshot the control acted on lies.
	Target *DesktopTarget `json:"target,omitempty"`
}

// DesktopTarget is a rectangle in a screenshot, each number a fraction (0 to
// 1) of the picture's width or height from its top left.
type DesktopTarget struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// DesktopActivity lists the latest desktop events, newest first.
type DesktopActivity struct {
	Events []DesktopEvent `json:"events"`
}

// TakeoverStarted answers "Let the agent finish": the conversation whose turn
// now works on the stopped run (AC-4).
type TakeoverStarted struct {
	SessionID string `json:"sessionId"`
}
