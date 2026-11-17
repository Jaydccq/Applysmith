package browser

import (
	"context"
	"encoding/json"
	"time"
)

// WindowFingerprint is what one lane's window says about itself: whether a tab
// could be opened in it, whether it advertises automation and its user agent.
type WindowFingerprint struct {
	Lane      Lane
	Attached  bool
	Webdriver *bool
	UserAgent string
	Err       string
}

const fingerprintTabBudget = 15 * time.Second

// CheckFingerprint opens a blank tab in the apply window and in the scan
// window, reads navigator.webdriver and the user agent from each, and closes
// the tabs. It loads no page, so nothing is sent to any site.
func CheckFingerprint(ctx context.Context, inst *Instance) []WindowFingerprint {
	out := make([]WindowFingerprint, 0, 2)
	for _, lane := range []Lane{LaneApply, LaneScan} {
		out = append(out, checkLane(ctx, inst.Lane(lane), lane))
	}
	return out
}

func checkLane(ctx context.Context, h *LaneHandle, lane Lane) WindowFingerprint {
	w := WindowFingerprint{Lane: lane}
	ctx, cancel := context.WithTimeout(ctx, fingerprintTabBudget)
	defer cancel()
	id, err := h.OpenTab(ctx, "setup-check")
	if err != nil {
		w.Err = err.Error()
		return w
	}
	defer func() { _ = h.CloseTab(context.WithoutCancel(ctx), id) }()
	w.Attached = true
	raw, err := h.Evaluate(ctx, id, `({webdriver: navigator.webdriver, userAgent: navigator.userAgent})`)
	if err != nil {
		w.Err = err.Error()
		return w
	}
	var got struct {
		Webdriver *bool  `json:"webdriver"`
		UserAgent string `json:"userAgent"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		w.Err = "the window's answer was not readable"
		return w
	}
	w.Webdriver, w.UserAgent = got.Webdriver, got.UserAgent
	return w
}
