//go:build browser

package browser

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// screenState reports whether the login screen is up. A locked screen makes
// the frontmost-application checks vacuous, so those checks are skipped.
func screenLocked() bool {
	out, err := exec.Command("ioreg", "-n", "Root", "-d1", "-a").Output()
	if err != nil {
		return false
	}
	return regexp.MustCompile(`CGSSessionScreenIsLocked</key>\s*<true/>`).Match(out)
}

func frontApp() string {
	out, err := exec.Command("lsappinfo", "front").Output()
	if err != nil {
		return ""
	}
	asn := strings.TrimSpace(string(out))
	name, _ := exec.Command("lsappinfo", "info", "-only", "name", asn).Output()
	return strings.TrimSpace(string(name))
}

// tabSession returns the chromedp session of a managed tab.
func tabSession(t *testing.T, inst *Instance, lane Lane, id string) context.Context {
	t.Helper()
	tb, err := inst.Lane(lane).tab(id)
	ok(t, err)
	sess, err := inst.session(tb)
	ok(t, err)
	return sess
}

// iframeSession attaches to the out-of-process iframe target whose URL starts
// with prefix, waiting for it to appear.
func iframeSession(t *testing.T, inst *Instance, prefix string) (context.Context, *target.Info) {
	t.Helper()
	var info *target.Info
	eventually(t, 8*time.Second, "an iframe target for "+prefix, func() bool {
		infos, err := chromedp.Targets(inst.root)
		if err != nil {
			return false
		}
		for _, in := range infos {
			if in.Type == "iframe" && strings.HasPrefix(in.URL, prefix) {
				info = in
				return true
			}
		}
		return false
	})
	ctx, cancel := chromedp.NewContext(inst.root, chromedp.WithTargetID(info.TargetID), chromedp.WithDetachOnCancel())
	t.Cleanup(cancel)
	ok(t, attachWithin(context.Background(), ctx, attachDeadline))
	return ctx, info
}

func call[P, R any](t *testing.T, sess context.Context, cmd cdp.Command[P, R], p P) R {
	t.Helper()
	c, cancel := context.WithTimeout(sess, 10*time.Second)
	defer cancel()
	r, err := chromedp.Call(c, cmd, p)
	if err != nil {
		t.Fatalf("%s: %v", cmd.Method, err)
	}
	return r
}

func evalOn[T any](t *testing.T, sess context.Context, expr string) T {
	t.Helper()
	c, cancel := context.WithTimeout(sess, 15*time.Second)
	defer cancel()
	v, err := chromedp.Run(c, chromedp.Evaluate[T](expr, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("evaluate %.60q: %v", expr, err)
	}
	return v
}

func isolatedWorld(t *testing.T, sess context.Context, name string) (runtime.ExecutionContextID, cdp.FrameID) {
	t.Helper()
	tree := call(t, sess, page.GetFrameTree, cdp.Empty{})
	w := call(t, sess, page.CreateIsolatedWorld, page.CreateIsolatedWorldParams{FrameID: tree.FrameTree.Frame.ID, WorldName: name})
	return w.ExecutionContextID, tree.FrameTree.Frame.ID
}

// click performs a trusted press and release with a short hold.
func click(t *testing.T, sess context.Context, x, y float64) {
	t.Helper()
	call(t, sess, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseMoved, X: x, Y: y})
	call(t, sess, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMousePressed, X: x, Y: y, Button: input.MouseButtonLeft, ClickCount: 1})
	time.Sleep(60 * time.Millisecond)
	call(t, sess, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseReleased, X: x, Y: y, Button: input.MouseButtonLeft, ClickCount: 1})
}

func typeText(t *testing.T, sess context.Context, s string) {
	t.Helper()
	for _, r := range s {
		k := string(r)
		call(t, sess, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Key: k, Text: k, UnmodifiedText: k})
		call(t, sess, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: k})
		time.Sleep(30 * time.Millisecond)
	}
}

type rect struct{ X, Y, W, H float64 }

func (r rect) center() (float64, float64) { return r.X + r.W/2, r.Y + r.H/2 }

func rectOf(t *testing.T, sess context.Context, sel string) rect {
	t.Helper()
	return evalOn[rect](t, sess, `(()=>{const r=document.querySelector('`+sel+`').getBoundingClientRect();return {X:r.x,Y:r.y,W:r.width,H:r.height}})()`)
}
