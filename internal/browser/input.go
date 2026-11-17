package browser

import (
	"context"
	"net/url"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// Input deadlines.
const (
	clickDeadline = 10 * time.Second
	typeDeadline  = 30 * time.Second
	bodyDeadline  = 3 * time.Second
)

// Click presses the left mouse button at viewport coordinates with trusted
// input: the page sees events whose isTrusted flag is true. The caller checks
// what stands at the point first; this does not.
func (h *LaneHandle) Click(ctx context.Context, tabID string, x, y float64) error {
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	sess, err := h.session(t)
	if err != nil {
		return err
	}
	octx, cancel := opCtx(ctx, sess, clickDeadline)
	defer cancel()
	if err := h.ensureViewport(octx, t, sess); err != nil {
		return err
	}
	t.noteCursor(x, y)
	for _, p := range []input.DispatchMouseEventParams{
		{Type: input.DispatchMouseEventTypeMouseMoved, X: x, Y: y},
		{Type: input.DispatchMouseEventTypeMousePressed, X: x, Y: y, Button: input.MouseButtonLeft, Buttons: 1, ClickCount: 1},
		{Type: input.DispatchMouseEventTypeMouseReleased, X: x, Y: y, Button: input.MouseButtonLeft, ClickCount: 1},
	} {
		if _, err := chromedp.Call(octx, input.DispatchMouseEvent, p); err != nil {
			return h.wrapOp(t, err, "a click", clickDeadline)
		}
	}
	return nil
}

// TypeText types text into the focused element one key at a time with trusted
// key events, as a person would: each key goes down with its text and comes
// up, so a widget that keeps its own model from keydown (ADP's phone box) sees
// the key, not only the character. Nothing about the text is logged.
func (h *LaneHandle) TypeText(ctx context.Context, tabID, text string) error {
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	sess, err := h.session(t)
	if err != nil {
		return err
	}
	octx, cancel := opCtx(ctx, sess, typeDeadline)
	defer cancel()
	if err := h.ensureViewport(octx, t, sess); err != nil {
		return err
	}
	for _, r := range text {
		for _, p := range keyStroke(r) {
			if _, err := chromedp.Call(octx, input.DispatchKeyEvent, p); err != nil {
				return h.wrapOp(t, err, "typing", typeDeadline)
			}
		}
	}
	return nil
}

// keyStroke is one typed character as a keyboard reports it: down with the
// text it inserts, then up. A line break is the Enter key.
func keyStroke(r rune) []input.DispatchKeyEventParams {
	key, text, vk := string(r), string(r), int64(0)
	if r == '\n' {
		key, text, vk = "Enter", "\r", 13
	}
	return []input.DispatchKeyEventParams{
		{Type: input.DispatchKeyEventTypeKeyDown, Key: key, Text: text, UnmodifiedText: text, WindowsVirtualKeyCode: vk},
		{Type: input.DispatchKeyEventTypeKeyUp, Key: key, WindowsVirtualKeyCode: vk},
	}
}

// namedKeys are the keys PressKey sends, with the codes a keyboard reports.
// namedKeys are the keys a lane may press: none of them submits a form.
var namedKeys = map[string]int64{"Tab": 9, "Escape": 27, "ArrowDown": 40}

// PressKey presses one named key with trusted key events, down then up, so the
// browser does what the key does for a person: Tab moves the focus on, Escape
// closes an open list, ArrowDown opens an autocomplete box's list.
func (h *LaneHandle) PressKey(ctx context.Context, tabID, key string) error {
	vk, ok := namedKeys[key]
	if !ok {
		return newError(CodeNotAllowed, "no key named %q", key)
	}
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	sess, err := h.session(t)
	if err != nil {
		return err
	}
	octx, cancel := opCtx(ctx, sess, clickDeadline)
	defer cancel()
	if err := h.ensureViewport(octx, t, sess); err != nil {
		return err
	}
	for _, typ := range []input.DispatchKeyEventType{input.DispatchKeyEventTypeRawKeyDown, input.DispatchKeyEventTypeKeyUp} {
		p := input.DispatchKeyEventParams{Type: typ, Key: key, Code: key, WindowsVirtualKeyCode: vk, NativeVirtualKeyCode: vk}
		if _, err := chromedp.Call(octx, input.DispatchKeyEvent, p); err != nil {
			return h.wrapOp(t, err, "a key press", clickDeadline)
		}
	}
	return nil
}

// reduceURL drops the query and fragment of an address, which can carry
// personal data. An about: address is kept whole; any other address without
// a hierarchy (data:, blob:, javascript:) is its bare scheme, since the rest
// is content or code. What is not an absolute address is "".
func reduceURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Scheme == "":
		return ""
	case u.Opaque != "" && u.Scheme == "about":
		return "about:" + u.Opaque
	case u.Opaque != "":
		return u.Scheme + ":"
	}
	return u.Scheme + "://" + u.Host + u.Path
}
