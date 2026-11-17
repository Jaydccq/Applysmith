package browser

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Frame describes one frame of a tab. URL is reduced to origin and path.
type Frame struct {
	ID       string
	ParentID string
	Origin   string
	URL      string
	IsTop    bool
}

// BindingCall is one call of a page-side binding.
type BindingCall struct {
	Frame   Frame
	Payload string
}

// BindingHandler answers a binding call. Its error rejects the page-side
// promise with the error text.
type BindingHandler func(ctx context.Context, c BindingCall) (string, error)

// Binding limits.
const (
	MaxBindingBytes       = 10 << 20
	DefaultBindingTimeout = 120 * time.Second
)

// bindingTimeout bounds one binding call. A variable so a test can shorten it.
var bindingTimeout = DefaultBindingTimeout

// Hooks is the frame-aware injection surface of one tab: frames of every
// process, evaluation in named isolated worlds, bindings that exist only in
// those worlds, and native file selection. Everything is released when the
// tab closes or is detached.
type Hooks struct {
	inst *Instance
	tab  *tab

	ctx      context.Context
	cancel   context.CancelFunc
	top      *hookSession
	topFrame string

	mu       sync.Mutex
	sessions map[target.ID]*hookSession
	frames   map[string]hookFrame
	bindings map[string]*hookBinding
	// initScripts run at the start of every document, in the main world.
	initScripts []string
}

type hookFrame struct {
	Frame
	sess *hookSession
}

type hookBinding struct {
	world   string
	handler BindingHandler
}

// Hooks returns the tab's hooks, creating them on first use.
func (h *LaneHandle) Hooks(ctx context.Context, tabID string) (*Hooks, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return nil, err
	}
	sess, err := h.session(t)
	if err != nil {
		return nil, err
	}
	t.hooksMu.Lock()
	defer t.hooksMu.Unlock()
	t.mu.Lock()
	if t.hooks != nil {
		hk := t.hooks
		t.mu.Unlock()
		return hk, nil
	}
	t.mu.Unlock()

	root, err := frameTreeOf(ctx, sess)
	if err != nil {
		return nil, h.wrapOp(t, err, "reading the frames", inputDeadline)
	}
	hctx, hcancel := context.WithCancel(sess)
	hk := &Hooks{
		inst: h.in(), tab: t, ctx: hctx, cancel: hcancel,
		topFrame: string(root.Frame.ID),
		sessions: map[target.ID]*hookSession{}, frames: map[string]hookFrame{},
		bindings: map[string]*hookBinding{},
	}
	hk.top = hk.newSession(t.id, sess, nil, hk.topFrame)
	t.mu.Lock()
	if t.hooks != nil || t.ctx != sess {
		existing := t.hooks
		t.mu.Unlock()
		hcancel()
		if existing != nil {
			return existing, nil
		}
		return nil, newError(CodeTabClosed, "tab was detached")
	}
	t.hooks = hk
	t.mu.Unlock()
	hk.watchTargets()
	return hk, nil
}

// listTargets is chromedp.Targets; a variable so a test can stall it.
var listTargets = chromedp.Targets

// OnTabClosed registers fn to run once when the tab is closed, whether by the
// caller, by the user or because the browser stopped. It also runs for a tab
// that was kept for the user once that tab closes, which outlives the hooks.
// A tab that is already closed runs fn at once.
func (hk *Hooks) OnTabClosed(fn func()) {
	t := hk.tab
	t.mu.Lock()
	if t.closed.Load() {
		t.mu.Unlock()
		fn()
		return
	}
	t.onClosed = append(t.onClosed, fn)
	t.mu.Unlock()
}

func frameTreeOf(ctx, sess context.Context) (*page.FrameTree, error) {
	c, cancel := opCtx(ctx, sess, inputDeadline)
	defer cancel()
	r, err := chromedp.Call(c, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return nil, err
	}
	return r.FrameTree, nil
}

// release drops every hook. Sessions of out-of-process frames end with the
// hooks; the tab's own session is the tab's to end.
func (hk *Hooks) release() {
	hk.cancel()
	hk.mu.Lock()
	hk.bindings = map[string]*hookBinding{}
	for id, s := range hk.sessions {
		s.cancel()
		delete(hk.sessions, id)
	}
	hk.mu.Unlock()
}

// Released reports whether the hooks have been released.
func (hk *Hooks) Released() bool { return hk.ctx.Err() != nil }

// forgetFrame drops a frame that left the tab from the frames last listed.
func (hk *Hooks) forgetFrame(id string) {
	hk.mu.Lock()
	delete(hk.frames, id)
	hk.mu.Unlock()
}

// hookSession is one DevTools session the hooks use: the tab's own, or one
// attached to an out-of-process frame target.
type hookSession struct {
	id     target.ID
	ctx    context.Context
	cancel context.CancelFunc
	root   string // id of the frame whose main world is the session's default context

	mu       sync.Mutex
	byCtx    map[runtime.ExecutionContextID]*hookWorld
	defaults map[string]runtime.ExecutionContextID
	unique   map[string]runtime.ExecutionContextID // the destroyed event names a context by its unique id
}

// newSession adopts a session: it listens for the events the hooks need and
// registers the bindings that exist so far.
func (hk *Hooks) newSession(id target.ID, ctx context.Context, cancel context.CancelFunc, root string) *hookSession {
	if cancel == nil {
		cancel = func() {}
	}
	s := &hookSession{id: id, ctx: ctx, cancel: cancel, root: root,
		byCtx: map[runtime.ExecutionContextID]*hookWorld{}, defaults: map[string]runtime.ExecutionContextID{}, unique: map[string]runtime.ExecutionContextID{}}
	watch(ctx, runtime.BindingCalled, func(ev runtime.EventBindingCalled) { hk.onBinding(s, ev) })
	watch(ctx, runtime.ExecutionContextCreated, s.onContextCreated)
	watch(ctx, runtime.ExecutionContextDestroyed, s.onContextDestroyed)
	watch(ctx, runtime.ExecutionContextsCleared, func(runtime.EventExecutionContextsCleared) { s.forget(0, true) })
	watch(ctx, page.FrameDetached, func(ev page.EventFrameDetached) {
		// A frame that moves between processes is detached from the old
		// session and navigated in the new one; only a real removal counts.
		if ev.Reason == page.FrameDetachedReasonRemove {
			hk.forgetFrame(string(ev.FrameID))
		}
	})
	return s
}

func (hk *Hooks) frameOf(f *cdp.Frame) Frame {
	return Frame{ID: string(f.ID), ParentID: string(f.ParentID), Origin: f.SecurityOrigin, URL: reduceURL(f.URL), IsTop: string(f.ID) == hk.topFrame}
}

func (s *hookSession) onContextCreated(ev runtime.EventExecutionContextCreated) {
	var aux struct {
		FrameID   string `json:"frameId"`
		IsDefault bool   `json:"isDefault"`
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unique[ev.Context.UniqueID] = ev.Context.ID
	if json.Unmarshal([]byte(ev.Context.AuxData), &aux) == nil && aux.IsDefault {
		s.defaults[aux.FrameID] = ev.Context.ID
	}
}

func (s *hookSession) onContextDestroyed(ev runtime.EventExecutionContextDestroyed) {
	s.mu.Lock()
	id, ok := s.unique[ev.ExecutionContextUniqueID]
	delete(s.unique, ev.ExecutionContextUniqueID)
	s.mu.Unlock()
	if ok {
		s.forget(id, false)
	}
}

// forget drops a destroyed context, or every context after a clear.
func (s *hookSession) forget(id runtime.ExecutionContextID, all bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if all {
		s.byCtx = map[runtime.ExecutionContextID]*hookWorld{}
		s.defaults = map[string]runtime.ExecutionContextID{}
		s.unique = map[string]runtime.ExecutionContextID{}
		return
	}
	delete(s.byCtx, id)
	for f, c := range s.defaults {
		if c == id {
			delete(s.defaults, f)
		}
	}
}

// Frames lists the frames of the tab in every process, the top frame first.
func (hk *Hooks) Frames(ctx context.Context) ([]Frame, error) {
	if err := hk.refresh(ctx); err != nil {
		return nil, err
	}
	hk.mu.Lock()
	defer hk.mu.Unlock()
	out := make([]Frame, 0, len(hk.frames))
	if f, ok := hk.frames[hk.topFrame]; ok {
		out = append(out, f.Frame)
	}
	for _, f := range hk.order() {
		if f.ID != hk.topFrame {
			out = append(out, f.Frame)
		}
	}
	return out, nil
}

// order returns the known frames in a stable order (by id).
func (hk *Hooks) order() []hookFrame {
	out := make([]hookFrame, 0, len(hk.frames))
	for _, f := range hk.frames {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b hookFrame) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// refresh rebuilds the frame table: the tab's own frame tree plus the tree of
// every out-of-process frame target that descends from the tab.
func (hk *Hooks) refresh(ctx context.Context) error {
	if hk.Released() {
		return hk.closedError()
	}
	found := map[string]hookFrame{}
	add := func(tree *page.FrameTree, s *hookSession) {
		var walk func(*page.FrameTree)
		walk = func(n *page.FrameTree) {
			found[string(n.Frame.ID)] = hookFrame{Frame: hk.frameOf(n.Frame), sess: s}
			for _, c := range n.ChildFrames {
				walk(c)
			}
		}
		walk(tree)
	}
	tree, err := frameTreeOf(ctx, hk.top.ctx)
	if err != nil {
		return hk.mapErr(ctx, err)
	}
	add(tree, hk.top)
	tc, tcancel := opCtx(ctx, hk.inst.root, evalDeadline)
	infos, err := listTargets(tc)
	tcancel()
	if err != nil {
		return hk.mapErr(ctx, err)
	}
	owned := map[target.ID]bool{hk.tab.id: true}
	for changed := true; changed; {
		changed = false
		for _, in := range infos {
			if in.Type == "iframe" && owned[in.ParentID] && !owned[in.TargetID] {
				owned[in.TargetID] = true
				changed = true
			}
		}
	}
	for _, in := range infos {
		if in.Type != "iframe" || !owned[in.TargetID] {
			continue
		}
		s, err := hk.targetSession(ctx, in.TargetID)
		if err != nil {
			continue // a frame that cannot be attached is reported by the caller that needs it
		}
		sub, err := frameTreeOf(ctx, s.ctx)
		if err != nil {
			// A renderer busy past the deadline (a fill working through a long
			// list) cannot describe its tree, but its target is still there:
			// the frames it served before stay.
			hk.keepFramesOf(s, found)
			continue
		}
		add(sub, s)
	}
	hk.mu.Lock()
	hk.frames = found
	hk.mu.Unlock()
	return nil
}

// keepFramesOf copies into found the frames the table holds for a session.
func (hk *Hooks) keepFramesOf(s *hookSession, found map[string]hookFrame) {
	hk.mu.Lock()
	defer hk.mu.Unlock()
	for id, f := range hk.frames {
		if f.sess == s {
			found[id] = f
		}
	}
}

// targetSession attaches to an out-of-process frame target, once.
func (hk *Hooks) targetSession(ctx context.Context, id target.ID) (*hookSession, error) {
	hk.mu.Lock()
	if s, ok := hk.sessions[id]; ok && s.ctx.Err() == nil {
		hk.mu.Unlock()
		return s, nil
	}
	hk.mu.Unlock()
	sctx, cancel := chromedp.NewContext(hk.inst.root, chromedp.WithTargetID(id), chromedp.WithDetachOnCancel())
	stop := context.AfterFunc(hk.ctx, cancel)
	if err := attachWithin(ctx, sctx, attachDeadline); err != nil {
		stop()
		cancel()
		return nil, err
	}
	c, ccancel := context.WithTimeout(sctx, 5*time.Second)
	_, _ = chromedp.Call(c, network.Disable, cdp.Empty{})
	ccancel()
	hk.mu.Lock()
	if hk.Released() {
		hk.mu.Unlock()
		stop()
		cancel()
		return nil, hk.closedError()
	}
	if s, ok := hk.sessions[id]; ok && s.ctx.Err() == nil {
		hk.mu.Unlock()
		stop()
		cancel()
		return s, nil
	}
	s := hk.newSession(id, sctx, func() { stop(); cancel() }, string(id))
	hk.sessions[id] = s
	names := hk.bindingNames()
	scripts := append([]string(nil), hk.initScripts...)
	hk.mu.Unlock()
	for name, world := range names {
		_ = s.addNative(name, world)
	}
	for _, src := range scripts {
		_ = s.addInit(ctx, src)
	}
	return s, nil
}

// watchTargets ends the session of an out-of-process frame whose target is
// gone and forgets the frame.
func (hk *Hooks) watchTargets() {
	watchBrowser(hk.ctx, target.TargetDestroyed, func(ev target.EventTargetDestroyed) {
		hk.mu.Lock()
		s, adopted := hk.sessions[ev.TargetID]
		delete(hk.sessions, ev.TargetID)
		delete(hk.frames, string(ev.TargetID))
		hk.mu.Unlock()
		if adopted {
			s.cancel()
		}
	})
}

func (hk *Hooks) closedError() error {
	if hk.tab.closed.Load() {
		return newError(CodeTabClosed, "tab is closed")
	}
	return newError(CodeBrowserDisconnected, "the tab's hooks were released")
}
