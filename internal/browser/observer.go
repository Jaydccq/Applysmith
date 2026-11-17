package browser

import (
	"context"
	"encoding/json"
	"iter"
	"sync"
	"sync/atomic"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// ObserverScripts are the only scripts a kept tab's observer runs: IsArm
// recognises the arming script, the one write, which Install runs in the
// observer's isolated world; IsRead the read-only scripts Evaluate runs.
type ObserverScripts struct {
	IsArm  func(script string) bool
	IsRead func(expr string) bool
}

// observerWorld is the observer's isolated world: no page script reaches its
// globals or the builtins it calls.
const observerWorld = "applysmith-observer"

// Observer is the session the system itself holds on a tab kept for the user:
// one for the run's recorder, and one for the length of each screenshot
// (capture). The live view, which the user opens, attaches its own.
//
// chromedp enables the Runtime, Log, Network and Page domains on every session
// it attaches, and a page can see Runtime being enabled. The observer therefore
// attaches over the browser connection in non-flat mode and talks to the
// session through Target.sendMessageToTarget, which needs no domain: it sends
// Runtime.evaluate, the init-script commands of Install and the two commands
// of a screenshot, and nothing is ever subscribed to.
type Observer struct {
	h       *LaneHandle
	t       *tab
	inst    *Instance
	scripts ObserverScripts
	session target.SessionID
	ctx     context.Context
	cancel  context.CancelFunc

	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan observerReply
	dead    chan struct{}
	once    sync.Once
}

// sendMessageToTarget is a command the generated protocol no longer lists. It
// is the one way to reach a session over the browser connection without the
// connection library enabling domains on it.
var sendMessageToTarget = cdp.Command[sendMessageParams, cdp.Empty]{Method: "Target.sendMessageToTarget"}

type sendMessageParams struct {
	SessionID target.SessionID `json:"sessionId"`
	Message   string           `json:"message"`
}

type observerReply struct {
	Result json.RawMessage
	Err    string
}

// Observe attaches the observer to a tab that was kept. A tab that is not kept
// has the lane's own session, and a kept tab has at most one observer.
func (h *LaneHandle) Observe(ctx context.Context, tabID string, s ObserverScripts) (*Observer, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return nil, err
	}
	if !t.isKept() {
		return nil, newError(CodeNotAllowed, "only a tab kept for the user is observed")
	}
	t.mu.Lock()
	if t.observing {
		t.mu.Unlock()
		return nil, newError(CodeNotAllowed, "the tab already has an observer")
	}
	t.observing = true
	t.mu.Unlock()
	o, err := h.attachQuiet(ctx, t, s)
	if err != nil {
		t.mu.Lock()
		t.observing = false
		t.mu.Unlock()
		return nil, err
	}
	return o, nil
}

// attachQuiet attaches a session that enables no domain on the tab.
func (h *LaneHandle) attachQuiet(ctx context.Context, t *tab, s ObserverScripts) (*Observer, error) {
	octx, cancel := context.WithCancel(h.in().root)
	o := &Observer{h: h, t: t, inst: h.in(), scripts: s, ctx: octx, cancel: cancel, pending: map[int64]chan observerReply{}, dead: make(chan struct{})}
	// The subscription is made before the attach, so no reply can be missed.
	events := chromedp.BrowserEvents(octx, target.ReceivedMessageFromTarget)
	detached := chromedp.BrowserEvents(octx, target.DetachedFromTarget)

	bc, bcancel := h.in().bctx(ctx)
	defer bcancel()
	bc, tcancel := context.WithTimeout(bc, inputDeadline)
	defer tcancel()
	res, err := chromedp.CallBrowser(bc, target.AttachToTarget, target.AttachToTargetParams{TargetID: t.id, Flatten: ptr(false)})
	if err != nil {
		cancel()
		return nil, h.wrapOp(t, err, "attaching the observer", inputDeadline)
	}
	o.session = res.SessionID
	go o.route(events)
	go o.watchDetach(detached)
	return o, nil
}

// route hands each reply to the request that waits for it.
func (o *Observer) route(events iter.Seq2[target.EventReceivedMessageFromTarget, error]) {
	for ev, err := range events {
		if err != nil {
			return
		}
		if ev.SessionID != o.session {
			continue
		}
		var m struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(ev.Message), &m) != nil || m.ID == 0 {
			continue
		}
		r := observerReply{Result: m.Result}
		if m.Error != nil {
			r.Err = m.Error.Message
		}
		o.mu.Lock()
		ch := o.pending[m.ID]
		delete(o.pending, m.ID)
		o.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}

// watchDetach ends the observer when the browser drops its session, as it does
// when the tab closes.
func (o *Observer) watchDetach(events iter.Seq2[target.EventDetachedFromTarget, error]) {
	for ev, err := range events {
		if err != nil {
			return
		}
		if ev.SessionID == o.session {
			o.once.Do(func() { close(o.dead) })
			return
		}
	}
}

// Evaluate runs expr in the page's main world and returns the JSON value. Only
// the system's own read-only scripts are taken; any other expression is refused
// before anything is sent.
func (o *Observer) Evaluate(ctx context.Context, expr string) (json.RawMessage, error) {
	if o.scripts.IsRead == nil || !o.scripts.IsRead(expr) {
		return nil, newError(CodeNotAllowed, "a kept tab takes only the recorder's own scripts")
	}
	r, err := o.call(ctx, "an observer evaluation", "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	if err != nil {
		return nil, err
	}
	return decodeEvaluate(r)
}

// Install runs the arming script once, now, in the observer's isolated world of
// every document the tab holds; a later document is armed by a later call. A
// named world needs no context id, which a navigation could hand to another
// world. Any other script is refused before anything is sent.
func (o *Observer) Install(ctx context.Context, script string) error {
	if o.scripts.IsArm == nil || !o.scripts.IsArm(script) {
		return newError(CodeNotAllowed, "a kept tab takes only the recorder's own scripts")
	}
	const op = "installing the observer's script"
	r, err := o.call(ctx, op, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": script, "worldName": observerWorld, "runImmediately": true})
	if err != nil {
		return err
	}
	var res struct {
		Identifier string `json:"identifier"`
	}
	if r.Err != "" || json.Unmarshal(r.Result, &res) != nil || res.Identifier == "" {
		return newError(CodeEvalFailed, "the observer's script was not installed: %s", r.Err)
	}
	// With the Page domain off, a registered script never runs on a later
	// document; it is removed so the session holds none.
	_, err = o.call(ctx, op, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": res.Identifier})
	return err
}

// call sends one command to the observer's session and waits for its reply.
func (o *Observer) call(ctx context.Context, op, method string, params any) (observerReply, error) {
	select {
	case <-o.dead:
		return observerReply{}, newError(CodeTabClosed, "the observer's session ended")
	default:
	}
	if o.t.closed.Load() {
		return observerReply{}, newError(CodeTabClosed, "tab is closed")
	}
	id := o.nextID.Add(1)
	ch := make(chan observerReply, 1)
	o.mu.Lock()
	o.pending[id] = ch
	o.mu.Unlock()
	forget := func() { o.mu.Lock(); delete(o.pending, id); o.mu.Unlock() }

	msg, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	bc, cancel := o.inst.bctx(ctx)
	defer cancel()
	bc, tcancel := context.WithTimeout(bc, evalDeadline)
	defer tcancel()
	if _, err := chromedp.CallBrowser(bc, sendMessageToTarget, sendMessageParams{SessionID: o.session, Message: string(msg)}); err != nil {
		forget()
		return observerReply{}, o.h.wrapOp(o.t, err, op, evalDeadline)
	}
	select {
	case r := <-ch:
		return r, nil
	case <-o.dead:
		forget()
		return observerReply{}, newError(CodeTabClosed, "the observer's session ended")
	case <-bc.Done():
		forget()
		return observerReply{}, o.h.wrapOp(o.t, bc.Err(), op, evalDeadline)
	}
}

// do runs one protocol command on the session and decodes its result into
// res, both encoded as the connection library encodes them.
func (o *Observer) do(ctx context.Context, op, method string, params, res any) error {
	raw, err := jsonv2.Marshal(params, chromedp.DefaultMarshalOptions)
	if err != nil {
		return err
	}
	r, err := o.call(ctx, op, method, json.RawMessage(raw))
	switch {
	case err != nil:
		return err
	case r.Err != "":
		return newError(CodeEvalFailed, "%s", r.Err)
	}
	return jsonv2.Unmarshal(r.Result, res, chromedp.DefaultUnmarshalOptions)
}

func decodeEvaluate(r observerReply) (json.RawMessage, error) {
	if r.Err != "" {
		return nil, newError(CodeEvalFailed, "%s", r.Err)
	}
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		return nil, wrapError(CodeEvalFailed, err, "unreadable evaluation result")
	}
	if res.Exception != nil {
		return nil, newError(CodeEvalFailed, "the script threw: %s", res.Exception.Text)
	}
	if len(res.Result.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return res.Result.Value, nil
}

// Release detaches the observer's session. The tab and its page are untouched.
func (o *Observer) Release() {
	o.detach()
	o.t.mu.Lock()
	o.t.observing = false
	o.t.mu.Unlock()
}

func (o *Observer) detach() {
	o.once.Do(func() { close(o.dead) })
	ctx, cancel := context.WithTimeout(o.inst.root, inputDeadline)
	defer cancel()
	// A closed tab took the session with it.
	if !o.t.closed.Load() {
		_, _ = chromedp.CallBrowser(ctx, target.DetachFromTarget, target.DetachFromTargetParams{SessionID: o.session})
	}
	o.cancel()
}
