package browser

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Cloudflare's managed challenge ("Just a moment...", "Additional
// Verification Required") is a page with one "Verify you are human"
// checkbox. Turnstile ignores a click whose isTrusted flag is false, so the
// checkbox is pressed with trusted input at its MEASURED centre. A click at a
// guessed offset from the widget's box landed on the checkbox's border on the
// live Indeed challenge and read as Cloudflare refusing a real click, so an
// unmeasurable checkbox is never clicked. Press-and-hold, image and other
// escalated challenges are not touched.
const (
	checkboxBudget   = 25 * time.Second
	checkboxClicks   = 3
	checkboxPassWait = 8 * time.Second
	// A widget shows its checkbox before it takes a press: on the live Indeed
	// challenge presses 1.5 s after the frame appeared went unanswered, while
	// one about 7 s after the page loaded passed.
	checkboxSettle  = 4 * time.Second
	checkboxPoll    = 500 * time.Millisecond
	checkboxStateOp = 2 * time.Second
)

// CheckboxResult is what an attempt at a Cloudflare checkbox did.
type CheckboxResult struct {
	// Present is a Cloudflare challenge standing on the page.
	Present bool
	// Clicks is the trusted presses on the measured checkbox.
	Clicks int
	Solved bool
	// Reason is no-challenge, solved, target-not-resolved or clicked-no-pass.
	Reason string
}

// cfStateJS reads whether the page is a Cloudflare challenge and whether a
// Turnstile pass token is already on it.
const cfStateJS = `(() => {
  const title = document.title || '';
  const h1 = ((document.querySelector('h1') || {}).textContent || '').trim();
  const challenge = /just a moment|attention required/i.test(title) ||
    /additional verification|verify(ing)? you are human|performing security verification/i.test(h1) ||
    !!document.querySelector('#challenge-form, #challenge-stage');
  let token = '';
  try { token = (window.turnstile && window.turnstile.getResponse && window.turnstile.getResponse()) || ''; } catch (e) {}
  if (!token) { const el = document.querySelector('input[name="cf-turnstile-response"]'); token = (el && el.value) || ''; }
  return {challenge: challenge, token: !!token};
})()`

type cfState struct {
	Challenge bool `json:"challenge"`
	Token     bool `json:"token"`
}

// ClearCloudflareCheckbox presses the checkbox of a Cloudflare challenge on
// the tab, at most three times within 25 seconds, and reports whether the
// challenge went away. A page that is not a Cloudflare challenge returns at
// once with no-challenge. The error is only for a tab that cannot be used.
func (h *LaneHandle) ClearCloudflareCheckbox(ctx context.Context, tabID string) (CheckboxResult, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return CheckboxResult{}, err
	}
	sess, err := h.session(t)
	if err != nil {
		return CheckboxResult{}, err
	}
	st, ok := h.cfState(ctx, tabID)
	if !ok || !st.Challenge {
		return CheckboxResult{Reason: "no-challenge"}, ctx.Err()
	}
	res := CheckboxResult{Present: true}
	if st.Token {
		res.Solved, res.Reason = true, "solved"
		return res, nil
	}
	deadline := time.Now().Add(checkboxBudget)
	for res.Clicks < checkboxClicks {
		// Once the widget first shows it is given a moment to settle.
		if res.Clicks == 0 {
			if _, ok := h.checkboxPoint(ctx, sess, deadline); !ok {
				break
			}
			if !pause(ctx, checkboxSettle) {
				break
			}
		}
		// Measured just before the press: the widget re-renders.
		pt, ok := h.checkboxPoint(ctx, sess, deadline)
		if !ok {
			break
		}
		if err := h.pressCheckbox(ctx, sess, tabID, pt); err != nil {
			return res, err
		}
		res.Clicks++
		if h.passed(ctx, tabID, min(checkboxPassWait, time.Until(deadline))) {
			res.Solved, res.Reason = true, "solved"
			return res, nil
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if res.Clicks == 0 {
		res.Reason = "target-not-resolved"
	} else {
		res.Reason = "clicked-no-pass"
	}
	return res, ctx.Err()
}

// cfState reads the page's challenge state; false when the page did not
// answer (it is navigating).
func (h *LaneHandle) cfState(ctx context.Context, tabID string) (cfState, bool) {
	c, cancel := context.WithTimeout(ctx, checkboxStateOp)
	defer cancel()
	raw, err := h.Evaluate(c, tabID, cfStateJS)
	var st cfState
	if err != nil || json.Unmarshal(raw, &st) != nil {
		return st, false
	}
	return st, true
}

// passed polls for the challenge to go away, or a token to appear, for at
// most d. A page that does not answer is navigating to the real page and is
// asked again.
func (h *LaneHandle) passed(ctx context.Context, tabID string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !pause(ctx, time.Second) {
			return false
		}
		if st, ok := h.cfState(ctx, tabID); ok && (!st.Challenge || st.Token) {
			return true
		}
	}
	return false
}

type point struct{ x, y float64 }

// checkboxPoint measures the checkbox centre in viewport pixels, retrying
// every half second until deadline: the widget is injected late and its frame
// re-renders, so one pierce may land between two renders.
func (h *LaneHandle) checkboxPoint(ctx context.Context, sess context.Context, deadline time.Time) (point, bool) {
	for {
		if pt, ok := h.measureCheckbox(ctx, sess); ok {
			return pt, true
		}
		if time.Now().Add(checkboxPoll).After(deadline) || !pause(ctx, checkboxPoll) {
			return point{}, false
		}
	}
}

// measureCheckbox pierces the page for the Turnstile frame, through closed
// shadow roots. A same-process frame carries its document, checkbox included;
// an out-of-process one is attached as its own target and pierced there, and
// its box is moved by the frame's origin in the page.
func (h *LaneHandle) measureCheckbox(ctx context.Context, sess context.Context) (point, bool) {
	c, cancel := opCtx(ctx, sess, inputDeadline)
	defer cancel()
	doc, err := chromedp.Call(c, dom.GetDocument, dom.GetDocumentParams{Depth: ptr(int64(-1)), Pierce: ptr(true)})
	if err != nil || doc.Root == nil {
		return point{}, false
	}
	frame := findNode(doc.Root, isTurnstileFrame)
	if frame == nil {
		return point{}, false
	}
	if box := findNode(frame, isCheckbox); box != nil {
		return centre(c, box.NodeID, point{})
	}
	if frame.FrameID == "" {
		return point{}, false
	}
	origin, err := chromedp.Call(c, dom.GetBoxModel, dom.GetBoxModelParams{NodeID: frame.NodeID})
	if err != nil || origin.Model == nil || len(origin.Model.Content) < 8 {
		return point{}, false
	}
	sctx, scancel := chromedp.NewContext(h.in().root, chromedp.WithTargetID(target.ID(frame.FrameID)), chromedp.WithDetachOnCancel())
	defer scancel()
	if attachWithin(c, sctx, inputDeadline) != nil {
		return point{}, false
	}
	fc, fcancel := opCtx(c, sctx, inputDeadline)
	defer fcancel()
	fdoc, err := chromedp.Call(fc, dom.GetDocument, dom.GetDocumentParams{Depth: ptr(int64(-1)), Pierce: ptr(true)})
	if err != nil || fdoc.Root == nil {
		return point{}, false
	}
	box := findNode(fdoc.Root, isCheckbox)
	if box == nil {
		return point{}, false
	}
	return centre(fc, box.NodeID, point{origin.Model.Content[0], origin.Model.Content[1]})
}

// centre is the centre of a node's border box, moved by off. A box smaller
// than 8 px is not laid out yet.
func centre(ctx context.Context, id cdp.NodeID, off point) (point, bool) {
	m, err := chromedp.Call(ctx, dom.GetBoxModel, dom.GetBoxModelParams{NodeID: id})
	if err != nil || m.Model == nil || len(m.Model.Border) < 8 {
		return point{}, false
	}
	q := m.Model.Border
	x0, x1 := min(q[0], q[2], q[4], q[6]), max(q[0], q[2], q[4], q[6])
	y0, y1 := min(q[1], q[3], q[5], q[7]), max(q[1], q[3], q[5], q[7])
	if x1-x0 < 8 || y1-y0 < 8 {
		return point{}, false
	}
	return point{off.x + (x0+x1)/2, off.y + (y0+y1)/2}, true
}

// pressCheckbox moves the pointer in from the side, as a hand does, then
// clicks with trusted input.
func (h *LaneHandle) pressCheckbox(ctx context.Context, sess context.Context, tabID string, pt point) error {
	c, cancel := opCtx(ctx, sess, clickDeadline)
	from := point{pt.x - 60, pt.y + 24}
	for i := 0; i <= 8; i++ {
		f := float64(i) / 8
		p := input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseMoved, X: from.x + (pt.x-from.x)*f, Y: from.y + (pt.y-from.y)*f}
		if _, err := chromedp.Call(c, input.DispatchMouseEvent, p); err != nil {
			cancel()
			return err
		}
	}
	cancel()
	if !pause(ctx, 80*time.Millisecond) {
		return ctx.Err()
	}
	return h.Click(ctx, tabID, pt.x, pt.y)
}

// findNode searches depth first through children, shadow roots and frame
// documents, an ancestor before its descendants.
func findNode(n *cdp.Node, match func(*cdp.Node) bool) *cdp.Node {
	if match(n) {
		return n
	}
	kids := append(append([]*cdp.Node{}, n.Children...), n.ShadowRoots...)
	if n.ContentDocument != nil {
		kids = append(kids, n.ContentDocument)
	}
	for _, k := range kids {
		if hit := findNode(k, match); hit != nil {
			return hit
		}
	}
	return nil
}

func isTurnstileFrame(n *cdp.Node) bool {
	return n.NodeName == "IFRAME" && (strings.Contains(attr(n, "src"), "challenges.cloudflare.com") || strings.HasPrefix(attr(n, "id"), "cf-chl-widget"))
}

// isCheckbox matches on the type alone: the widget's label is in whatever
// language Cloudflare chose, and its document holds no other checkbox.
func isCheckbox(n *cdp.Node) bool {
	return n.NodeName == "INPUT" && attr(n, "type") == "checkbox"
}

// attr is the value of a node's attribute, or "".
func attr(n *cdp.Node, name string) string {
	for i := 0; i+1 < len(n.Attributes); i += 2 {
		if n.Attributes[i] == name {
			return n.Attributes[i+1]
		}
	}
	return ""
}

// pause waits d; false when ctx ended first.
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
