package apply

import (
	"context"
	"strings"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/lazyregexp"
)

// An application embedded in an iframe has its own document: its fields and
// its Next control are invisible to a probe of the top frame. When the top
// frame shows no form, the probe reads the sub-frames and presents the first
// one that holds an application as the page, so every stage that reads the
// page sees the embedded form. The handle of an element of the n-th sub-frame
// is (n+1)*frameBase plus its handle in that frame, which lets Locate find the
// frame again without keeping state between calls.
const frameBase = 1_000_000

// frameSkip names the frames that are never an application: challenge
// widgets, analytics, advertising, video embeds and chat widgets (Breezy opens
// its messenger a few seconds after load, and its message box read as the page's
// form, which hid the posting's own Apply link).
var frameSkip = lazyregexp.New(`(?i)(?:hcaptcha|recaptcha|challenges\.cloudflare\.com|captcha-delivery\.com|doubleclick|googletagmanager|googleadservices|google-analytics|youtube|vimeo|facebook\.com/tr|breezy-widget|intercom|zdassets|zendesk|tidio|tawk\.to|crisp\.chat|livechatinc)`)

// subFrames lists the tab's candidate frames: http(s) frames below the top
// frame that are not a widget.
func subFrames(ctx context.Context, t Tab) []browser.Frame {
	all, err := t.Frames(ctx)
	if err != nil {
		return nil
	}
	var out []browser.Frame
	for _, f := range all {
		if f.IsTop || !(strings.HasPrefix(f.URL, "http://") || strings.HasPrefix(f.URL, "https://")) || frameSkip.MatchString(f.URL) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// embedFrames returns the top page with the first sub-frame that holds an
// application folded in, or the top page itself when none does. A frame the
// page shows but the browser has not listed yet, or one that cannot be read at
// this moment, is looked for again a few times: reading it as an empty page
// would end the walk on a form that is there.
func embedFrames(ctx context.Context, t Tab, top *PageState) *PageState {
	shown := 0
	for _, f := range top.Frames {
		if f.Visible && f.W > 0 && f.H > 0 && (strings.HasPrefix(f.Src, "http://") || strings.HasPrefix(f.Src, "https://")) && !frameSkip.MatchString(f.Src) {
			shown++
		}
	}
	// iCIMS draws the whole posting, and its "Apply for this job online" link,
	// in a same-origin frame under a header-only top document. A frame's Apply
	// control stands for the page only when the top document offers none of its
	// own, so a widget frame cannot take the page's opener away.
	framedOpener := len(Categorize(top, false).Openers) == 0
	for try := 0; try < 4; try++ {
		subs := subFrames(ctx, t)
		failed := 0
		for n, f := range subs {
			var fs PageState
			if err := t.EvalFrame(ctx, f.ID, probeExpr("probe", nil), &fs); err != nil {
				failed++
				continue
			}
			if err := fs.readInventory(ctx, t, f.ID); err != nil {
				failed++
				continue
			}
			c := Categorize(&fs, true)
			if !fs.HasForm() && len(c.Nav)+len(c.Submit) == 0 && !(framedOpener && len(c.Openers) > 0) {
				continue
			}
			return mergeFrame(top, &fs, (n+1)*frameBase)
		}
		if failed == 0 && len(subs) >= shown || !sleepCtx(ctx, 150*time.Millisecond) {
			break
		}
	}
	return top
}

// mergeFrame presents a frame's document as the page: the top frame keeps its
// address, title, headings and links, the frame contributes the form.
func mergeFrame(top, fs *PageState, base int) *PageState {
	out := *fs
	out.URL, out.Title, out.Headings, out.Links = top.URL, top.Title, top.Headings, top.Links
	out.Frames = top.Frames
	out.Text = top.Text + "\n" + fs.Text
	out.Lines = append(append([]string{}, top.Lines...), fs.Lines...)
	out.Loading = top.Loading || fs.Loading
	out.Inputs = append([]InputInfo(nil), fs.Inputs...)
	out.Controls = append([]Control(nil), fs.Controls...)
	for i := range out.Inputs {
		out.Inputs[i].I += base
	}
	for i := range out.Controls {
		out.Controls[i].I += base
	}
	return &out
}

// frameOf resolves a handle to its sub-frame and the handle inside it.
func frameOf(ctx context.Context, t Tab, handle int) (browser.Frame, int, bool) {
	n := handle/frameBase - 1
	subs := subFrames(ctx, t)
	if n < 0 || n >= len(subs) {
		return browser.Frame{}, 0, false
	}
	return subs[n], handle % frameBase, true
}

// locateInFrame locates a control of a sub-frame and returns where to press it
// in the page's own coordinates. The control is pressable when it is found,
// not covered and has stood still across a presented frame of the frame and of
// the page: input to an out-of-process frame is routed by what the compositor
// last presented, which trails the layout after any scroll or change. Waiting
// for a presented frame (and measuring again) replaces guessing a delay; a
// control that keeps moving after a few rounds is returned as last measured.
func locateInFrame(ctx context.Context, t Tab, handle int) (Located, error) {
	f, j, ok := frameOf(ctx, t, handle)
	if !ok {
		return Located{}, nil
	}
	var l Located
	if err := t.EvalFrame(ctx, f.ID, probeExpr("locateStill", j), &l); err != nil || !l.Found {
		return Located{}, err
	}
	if !l.InView {
		if err := t.EvalFrame(ctx, f.ID, probeExpr("locate", j), &l); err != nil {
			return l, err
		}
	}
	var at, prev *struct{ X, Y float64 }
	for round := 0; round < locateRounds; round++ {
		if err := t.Eval(ctx, probeExpr("frameRect", map[string]any{"src": f.URL, "dx": l.X, "dy": l.Y}), &at); err != nil {
			return l, err
		}
		if at == nil {
			return Located{}, nil
		}
		// A frame presented after the last scroll or change, in the frame and in
		// the page, before the control is measured for the last time.
		var shown bool
		if err := t.EvalFrame(ctx, f.ID, probeExpr("settle", nil), &shown); err != nil {
			return l, err
		}
		if err := t.Eval(ctx, probeExpr("settle", nil), &shown); err != nil {
			return l, err
		}
		var again Located
		if err := t.EvalFrame(ctx, f.ID, probeExpr("locateStill", j), &again); err != nil || !again.Found {
			return Located{}, err
		}
		steady := prev != nil && near(prev.X, at.X) && near(prev.Y, at.Y) && near(again.X, l.X) && near(again.Y, l.Y)
		l = again
		prev = at
		if steady {
			break
		}
	}
	l.X, l.Y = l.X+at.X, l.Y+at.Y
	return l, nil
}

// locateRounds bounds the measuring rounds of one frame control.
const locateRounds = 4

func near(a, b float64) bool { return a-b < 0.5 && b-a < 0.5 }

// frameSignature is the fingerprint of the sub-frames' content, appended to
// the top frame's when the top frame holds no form: a wizard that swaps its
// step inside the frame under one address moves only this part. A frame that
// cannot be read gives no fingerprint at all, so a failed read is never taken
// for a page that changed.
func frameSignature(ctx context.Context, t Tab) (string, error) {
	var b strings.Builder
	for _, f := range subFrames(ctx, t) {
		var s string
		if err := t.EvalFrame(ctx, f.ID, probeExpr("signature", nil), &s); err != nil {
			return "", err
		}
		b.WriteString("|frame:" + s)
	}
	return b.String(), nil
}
