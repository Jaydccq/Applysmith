package browser

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// nativeSuffix names the raw DevTools binding behind a public binding. The raw
// function only takes a string and returns nothing; the world-side wrapper
// turns it into the promise-returning function the engine calls.
const nativeSuffix = "$n"

// hookWorld is one isolated world of one frame document.
type hookWorld struct {
	ctxID  runtime.ExecutionContextID
	frame  string
	origin string

	mu        sync.Mutex
	installed map[string]bool
}

// wrapperScript turns each raw binding of the world into an async function
// and keeps the pending calls in the world, out of the page's reach.
const wrapperScript = `(names=>{
const g=globalThis;
if(!g.__ajSettle$){
 const pending=new Map();let seq=0;
 Object.defineProperty(g,'__ajPending$',{value:{pending,next:()=>++seq}});
 Object.defineProperty(g,'__ajSettle$',{value:(id,ok,v)=>{const p=pending.get(id);pending.delete(id);if(p)ok?p.res(v):p.rej(new Error(v))}});
}
const {pending,next}=g.__ajPending$;
for(const n of names){
 const raw=g[n+'$n'];
 if(typeof raw!=='function')continue;
 try{delete g[n+'$n']}catch(e){}
 Object.defineProperty(g,n,{configurable:true,writable:true,value:payload=>new Promise((res,rej)=>{const id=next();pending.set(id,{res,rej});raw(JSON.stringify({id,payload}))})});
}
return location.origin;
})(%s)`

// AddBinding registers a binding that exists only in the named isolated world
// of this tab's frames. Registering a name again replaces its handler, so the
// newest registration answers (a tab has one owner of a binding at a time);
// the same name for another world is refused.
func (hk *Hooks) AddBinding(world, name string, h BindingHandler) error {
	hk.mu.Lock()
	if hk.Released() {
		hk.mu.Unlock()
		return hk.closedError()
	}
	if old, dup := hk.bindings[name]; dup {
		defer hk.mu.Unlock()
		if old.world != world {
			return newError(CodeBindingConflict, "binding %s is registered for world %s, not %s", name, old.world, world)
		}
		hk.bindings[name] = &hookBinding{world: world, handler: h}
		return nil
	}
	b := &hookBinding{world: world, handler: h}
	hk.bindings[name] = b
	sessions := []*hookSession{hk.top}
	for _, s := range hk.sessions {
		sessions = append(sessions, s)
	}
	hk.mu.Unlock()
	for _, s := range sessions {
		if err := s.addNative(name, world); err != nil {
			// The name is forgotten, so the next registration adds it again
			// instead of being answered as a duplicate.
			hk.mu.Lock()
			if hk.bindings[name] == b {
				delete(hk.bindings, name)
			}
			hk.mu.Unlock()
			return hk.mapErr(context.Background(), err)
		}
	}
	return nil
}

// The Runtime commands that keep a session's bindings. Variables so a test can
// script the browser.
var (
	addBinding = func(ctx context.Context, p runtime.AddBindingParams) error {
		_, err := chromedp.Call(ctx, runtime.AddBinding, p)
		return err
	}
	removeBinding = func(ctx context.Context, name string) error {
		_, err := chromedp.Call(ctx, runtime.RemoveBinding, runtime.RemoveBindingParams{Name: name})
		return err
	}
	// announceContexts switches context reporting off and on, which makes the
	// browser announce every context of the session again.
	announceContexts = func(ctx context.Context) error {
		if _, err := chromedp.Call(ctx, runtime.Disable, cdp.Empty{}); err != nil {
			return err
		}
		_, err := chromedp.Call(ctx, runtime.Enable, cdp.Empty{})
		return err
	}
)

func (s *hookSession) addNative(name, world string) error {
	c, cancel := context.WithTimeout(s.ctx, inputDeadline)
	defer cancel()
	return addBinding(c, runtime.AddBindingParams{Name: name + nativeSuffix, ExecutionContextName: world})
}

// restoreBindings registers every binding of a session afresh. The browser
// answers a name it still knows without doing anything, so each is removed
// first; the wrappers in the documents that are loaded keep working, because a
// call is matched by name.
func (hk *Hooks) restoreBindings(s *hookSession) error {
	hk.mu.Lock()
	names := hk.bindingNames()
	hk.mu.Unlock()
	c, cancel := context.WithTimeout(s.ctx, inputDeadline)
	defer cancel()
	for name, world := range names {
		if err := removeBinding(c, name+nativeSuffix); err != nil {
			return err
		}
		if err := addBinding(c, runtime.AddBindingParams{Name: name + nativeSuffix, ExecutionContextName: world}); err != nil {
			return err
		}
	}
	if s != hk.top {
		return nil
	}
	return hk.tab.restorePopupBinding(c)
}

func (hk *Hooks) bindingNames() map[string]string {
	out := make(map[string]string, len(hk.bindings))
	for n, b := range hk.bindings {
		out[n] = b.world
	}
	return out
}

// ensureWorld returns the frame's isolated world, creating it and loading the
// binding wrappers when this document does not have them yet.
func (hk *Hooks) ensureWorld(ctx context.Context, s *hookSession, frame, world string) (*hookWorld, error) {
	r, err := chromedp.Call(ctx, page.CreateIsolatedWorld, page.CreateIsolatedWorldParams{FrameID: cdp.FrameID(frame), WorldName: world})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	w := s.byCtx[r.ExecutionContextID]
	if w == nil {
		w = &hookWorld{ctxID: r.ExecutionContextID, frame: frame, installed: map[string]bool{}}
		s.byCtx[w.ctxID] = w
	}
	s.mu.Unlock()
	hk.mu.Lock()
	var names []string
	for n, b := range hk.bindings {
		if b.world == world {
			names = append(names, n)
		}
	}
	hk.mu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	todo := []string{}
	for _, n := range names {
		if !w.installed[n] {
			todo = append(todo, n)
		}
	}
	if len(todo) == 0 && w.origin != "" {
		return w, nil
	}
	list, _ := json.Marshal(todo)
	res, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: fmt.Sprintf(wrapperScript, list), ContextID: w.ctxID, ReturnByValue: ptr(true),
	})
	if err != nil {
		return nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, newError(CodeEvalFailed, "cannot load the binding wrappers: %s", res.ExceptionDetails.Text)
	}
	_ = json.Unmarshal(res.Result.Value, &w.origin)
	for _, n := range todo {
		w.installed[n] = true
	}
	return w, nil
}

// mainContext returns the default context of a frame in a session; zero means
// the session's own default, which is the main world of its root frame.
func (hk *Hooks) mainContext(ctx context.Context, s *hookSession, frame string) (runtime.ExecutionContextID, error) {
	if frame == s.root {
		return 0, nil
	}
	lookup := func() (runtime.ExecutionContextID, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id, ok := s.defaults[frame]
		return id, ok
	}
	if id, ok := lookup(); ok {
		return id, nil
	}
	// Contexts created before the hooks listened are announced again when
	// reporting is switched off and on. Switching it off makes the browser
	// drop the session's bindings, so they are registered afresh whether or
	// not reporting came back on: a document made without them fails silently.
	aerr := announceContexts(ctx)
	if s == hk.top {
		defer hk.tab.hidePopupBinding(ctx, s)
	}
	if err := hk.restoreBindings(s); err != nil || aerr != nil {
		return 0, cmp.Or(aerr, err)
	}
	for range 20 {
		if id, ok := lookup(); ok {
			return id, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return 0, newError(CodeFrameGone, "frame %s has no main-world context", frame)
}

// locate finds the frame and the session that serves it.
func (hk *Hooks) locate(ctx context.Context, frame string) (hookFrame, error) {
	hk.mu.Lock()
	f, ok := hk.frames[frame]
	hk.mu.Unlock()
	if ok && f.sess.ctx.Err() == nil {
		return f, nil
	}
	if err := hk.refresh(ctx); err != nil {
		return hookFrame{}, err
	}
	hk.mu.Lock()
	f, ok = hk.frames[frame]
	hk.mu.Unlock()
	if !ok {
		return hookFrame{}, newError(CodeFrameGone, "no frame %s in the tab", frame)
	}
	return f, nil
}

// context resolves the execution context for a call and returns the context
// that carries the call's deadline.
func (hk *Hooks) prepare(ctx context.Context, frame, world string) (hookFrame, runtime.ExecutionContextID, context.Context, context.CancelFunc, error) {
	if hk.Released() {
		return hookFrame{}, 0, nil, nil, hk.closedError()
	}
	f, err := hk.locate(ctx, frame)
	if err != nil {
		return hookFrame{}, 0, nil, nil, err
	}
	octx, cancel := opCtx(ctx, f.sess.ctx, evalDeadline)
	var id runtime.ExecutionContextID
	if world == "" {
		id, err = hk.mainContext(octx, f.sess, frame)
	} else {
		var w *hookWorld
		if w, err = hk.ensureWorld(octx, f.sess, frame, world); w != nil {
			id = w.ctxID
		}
	}
	if err != nil {
		cancel()
		return hookFrame{}, 0, nil, nil, hk.mapErr(ctx, err)
	}
	return f, id, octx, cancel, nil
}

// Eval evaluates expr in the named isolated world of a frame, or in the page's
// main world when world is empty, awaits a promise result and returns the
// value as JSON.
func (hk *Hooks) Eval(ctx context.Context, frame, world, expr string) (json.RawMessage, error) {
	_, id, octx, cancel, err := hk.prepare(ctx, frame, world)
	if err != nil {
		return nil, err
	}
	defer cancel()
	res, err := chromedp.Call(octx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: expr, ContextID: id, AwaitPromise: ptr(true), ReturnByValue: ptr(true),
	})
	if err != nil {
		return nil, hk.mapErr(ctx, err)
	}
	if res.ExceptionDetails != nil {
		return nil, newError(CodeEvalFailed, "%s", exceptionText(res.ExceptionDetails))
	}
	if res.Result == nil || len(res.Result.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(res.Result.Value), nil
}

func exceptionText(e *runtime.ExceptionDetails) string {
	if e.Exception != nil && e.Exception.Description != "" {
		return e.Exception.Description
	}
	return e.Text
}

var setFilesGroups atomic.Uint64

// setFilesGroup names the object group of one SetFiles call, so concurrent
// calls never release each other's file input.
func setFilesGroup() string {
	return fmt.Sprintf("applysmith-setfiles-%d", setFilesGroups.Add(1))
}

// SetFiles sets files on the file input that locator (an expression evaluated
// in the given world) returns, with DOM.setFileInputFiles semantics: the
// input reports the files and fires a trusted change event.
func (hk *Hooks) SetFiles(ctx context.Context, frame, world, locator string, paths []string) error {
	f, id, octx, cancel, err := hk.prepare(ctx, frame, world)
	if err != nil {
		return err
	}
	defer cancel()
	group := setFilesGroup()
	res, err := chromedp.Call(octx, runtime.Evaluate, runtime.EvaluateParams{Expression: locator, ContextID: id, ObjectGroup: group})
	if err != nil {
		return hk.mapErr(ctx, err)
	}
	defer func() {
		// The file input's remote object lives until its group is released.
		rc, rcancel := context.WithTimeout(f.sess.ctx, inputDeadline)
		defer rcancel()
		_, _ = chromedp.Call(rc, runtime.ReleaseObjectGroup, runtime.ReleaseObjectGroupParams{ObjectGroup: group})
	}()
	if res.ExceptionDetails != nil {
		return newError(CodeEvalFailed, "%s", exceptionText(res.ExceptionDetails))
	}
	if res.Result == nil || res.Result.ObjectID == "" {
		return newError(CodeEvalFailed, "the locator found no file input")
	}
	if _, err := chromedp.Call(octx, dom.SetFileInputFiles, dom.SetFileInputFilesParams{Files: paths, ObjectID: res.Result.ObjectID}); err != nil {
		return hk.mapErr(ctx, err)
	}
	return nil
}

// mapErr turns a DevTools failure into a typed error. As in wrapOp, a browser
// that is gone is browser-disconnected, and any other failure no code names,
// with the browser and the tab still there, is operation-failed.
func (hk *Hooks) mapErr(caller context.Context, err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	msg := err.Error()
	switch {
	case errors.Is(caller.Err(), context.DeadlineExceeded):
		return wrapError(CodeOperationTimeout, err, "operation timed out")
	case caller.Err() != nil && !hk.tab.closed.Load():
		return caller.Err()
	case hk.tab.closed.Load():
		return newError(CodeTabClosed, "tab is closed")
	case strings.Contains(msg, "Cannot find context with specified id"),
		strings.Contains(msg, "Execution context was destroyed"),
		strings.Contains(msg, "Inspected target navigated or closed"),
		strings.Contains(msg, "context was destroyed"):
		return wrapError(CodeDocumentReplaced, err, "the frame document was replaced")
	case strings.Contains(msg, "No frame for given id"), strings.Contains(msg, "Session with given id not found"),
		strings.Contains(msg, "No target with given id"):
		return wrapError(CodeFrameGone, err, "the frame is gone")
	case errors.Is(err, context.DeadlineExceeded):
		return wrapError(CodeOperationTimeout, err, "operation timed out")
	case hk.Released():
		return hk.closedError()
	case hk.inst.confirmGone():
		return wrapError(CodeBrowserDisconnected, err, "the browser is gone")
	}
	return wrapError(CodeOperationFailed, err, "DevTools operation failed")
}

// onBinding answers one raw binding call: it finds the calling frame, runs the
// handler under the binding timeout and settles the page-side promise.
func (hk *Hooks) onBinding(s *hookSession, ev runtime.EventBindingCalled) {
	name, found := strings.CutSuffix(ev.Name, nativeSuffix)
	if !found {
		return
	}
	hk.mu.Lock()
	b := hk.bindings[name]
	hk.mu.Unlock()
	s.mu.Lock()
	w := s.byCtx[ev.ExecutionContextID]
	s.mu.Unlock()
	if b == nil || w == nil {
		return
	}
	var env struct {
		ID      int    `json:"id"`
		Payload string `json:"payload"`
	}
	if json.Unmarshal([]byte(ev.Payload), &env) != nil {
		return
	}
	go func() {
		reply, err := hk.serve(b, w, env.Payload)
		hk.settle(s, w, env.ID, reply, err)
	}()
}

func (hk *Hooks) serve(b *hookBinding, w *hookWorld, payload string) (string, error) {
	if len(payload) > MaxBindingBytes {
		return "", fmt.Errorf("payload of %d bytes exceeds the %d byte limit", len(payload), MaxBindingBytes)
	}
	call := BindingCall{Frame: hk.frameInfo(w), Payload: payload}
	c, cancel := context.WithTimeout(hk.ctx, bindingTimeout)
	defer cancel()
	type result struct {
		reply string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := b.handler(c, call)
		ch <- result{r, err}
	}()
	select {
	case r := <-ch:
		if r.err == nil && len(r.reply) > MaxBindingBytes {
			return "", fmt.Errorf("reply of %d bytes exceeds the %d byte limit", len(r.reply), MaxBindingBytes)
		}
		return r.reply, r.err
	case <-c.Done():
		// The handler's late reply lands in the buffered channel and is dropped.
		return "", fmt.Errorf("binding timeout after %v", bindingTimeout)
	}
}

func (hk *Hooks) frameInfo(w *hookWorld) Frame {
	hk.mu.Lock()
	f, ok := hk.frames[w.frame]
	hk.mu.Unlock()
	if !ok {
		f.Frame = Frame{ID: w.frame}
	}
	f.IsTop = w.frame == hk.topFrame
	w.mu.Lock()
	f.Origin = w.origin
	w.mu.Unlock()
	return f.Frame
}

func (hk *Hooks) settle(s *hookSession, w *hookWorld, id int, reply string, err error) {
	val := reply
	if err != nil {
		val = err.Error()
	}
	lit, _ := json.Marshal(val)
	c, cancel := context.WithTimeout(s.ctx, evalDeadline)
	defer cancel()
	// A context that is gone (the page navigated) has nobody left to settle.
	_, _ = chromedp.Call(c, runtime.Evaluate, runtime.EvaluateParams{
		Expression: fmt.Sprintf("__ajSettle$(%d,%t,%s)", id, err == nil, lit), ContextID: w.ctxID,
	})
}
