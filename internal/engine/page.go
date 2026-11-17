package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"applysmith/internal/browser"
)

// World is the isolated world the engine and its bindings live in.
const World = "applysmith-engine"

// Driver is the part of a tab's browser hooks the protocol needs.
// *browser.Hooks implements it; tests replace it with a fake page.
type Driver interface {
	Frames(ctx context.Context) ([]browser.Frame, error)
	Eval(ctx context.Context, frame, world, expr string) (json.RawMessage, error)
	AddBinding(world, name string, h browser.BindingHandler) error
	SetFiles(ctx context.Context, frame, world, locator string, paths []string) error
	// OnTabClosed runs fn once when the tab closes, kept for the user or not.
	OnTabClosed(fn func())
}

// Matcher answers the engine's questions (POST /api/answer-bank/match semantics).
type Matcher interface {
	Match(ctx context.Context, req MatchRequest) (MatchResponse, error)
}

// Resumes supplies the document to attach; a nil payload means there is none.
type Resumes interface {
	Resume(ctx context.Context, req ResumeRequest) (*ResumePayload, error)
}

// Host is what the three bindings are served from.
type Host struct {
	Matcher Matcher
	Resumes Resumes
	// StageDir is a private directory (under the workspace state directory)
	// where documents the browser must read from disk are staged. A staged
	// document stays until the tab closes or the page is closed, because the
	// browser reads a file input's file lazily, when the page submits.
	StageDir string
}

// FrameNotice is a terminal notification and the frame it came from.
type FrameNotice struct {
	FrameID string
	Notice  TerminalNotice
}

// Page runs fills in one tab. Its bindings answer only the fill that is
// current for the tab and only from frames that fill admitted.
type Page struct {
	d    Driver
	host Host

	// nonce names this tab's channel to the main-world helper.
	nonce string

	mu     sync.Mutex
	cur    *runState
	staged []string // run directories under host.StageDir
	nstage int      // files staged so far, for unique names
	closed bool     // Close ran; nothing is staged any more
}

// pinnable reports whether an origin can pin a frame: an opaque origin
// ("null", as sandboxed and data: documents have) is shared by unrelated
// documents, so a comparison against it proves nothing.
func pinnable(origin string) bool { return origin != "" && origin != "null" }

// runState is what the bindings check a call against.
type runState struct {
	id string

	mu sync.Mutex
	// admitted maps each admitted frame to the origin its document had when it
	// was admitted; a frame whose document has since moved to another origin is
	// not the frame that was admitted. Every admission carries a usable origin
	// (see pinnable); a frame without one is never admitted.
	admitted  map[string]string
	terminals []FrameNotice
	// arrived wakes the fill that waits for terminal notices; one pending wake is enough.
	arrived chan struct{}
	served  map[string]map[string]ResumePayload // frame -> file name -> document
}

func newRunState(id string) *runState {
	return &runState{id: id, admitted: map[string]string{}, arrived: make(chan struct{}, 1), served: map[string]map[string]ResumePayload{}}
}

// terminalFrom reports whether the frame's terminal notice has arrived.
func (r *runState) terminalFrom(frame string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.terminals {
		if n.FrameID == frame {
			return true
		}
	}
	return false
}

func (r *runState) admit(frame, origin string) {
	r.mu.Lock()
	r.admitted[frame] = origin
	r.mu.Unlock()
}

// admission returns the origin a frame was admitted at and whether it was admitted.
func (r *runState) admission(frame string) (origin string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	origin, ok = r.admitted[frame]
	return origin, ok
}

// NewPage registers the three bindings on the tab's hooks.
func NewPage(d Driver, h Host) (*Page, error) {
	if len(Bundle()) == 0 {
		return nil, newError(CodeBundleMissing, nil, "the engine bundle is not generated; run make generate")
	}
	p := &Page{d: d, host: h, nonce: newNonce()}
	if h.StageDir != "" {
		sweepStaged(h.StageDir)
	}
	d.OnTabClosed(p.Close)
	for _, b := range []string{BindingMatch, BindingResume, BindingTerminal} {
		if err := d.AddBinding(World, b, p.handler(b)); err != nil {
			return nil, err
		}
	}
	return p, nil
}

var (
	errStaleRun    = errors.New("the call belongs to a run that is not current for this tab")
	errNotAdmitted = errors.New("the calling frame was not admitted for this run")
	// errOriginChanged rejects a call from a frame whose document moved to
	// another origin after the frame was admitted.
	errOriginChanged = errors.New("the calling frame is not at the origin it was admitted at")
)

func (p *Page) current() *runState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur
}

// begin makes rs the current run of the tab.
func (p *Page) begin(rs *runState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil {
		return newError(CodeBusy, nil, "a fill is already running in this tab")
	}
	p.cur = rs
	return nil
}

func (p *Page) end(rs *runState) {
	p.mu.Lock()
	if p.cur == rs {
		p.cur = nil
	}
	p.mu.Unlock()
}

func (p *Page) handler(binding string) browser.BindingHandler {
	return func(ctx context.Context, c browser.BindingCall) (string, error) {
		var head struct {
			RunID string `json:"runId"`
		}
		if err := json.Unmarshal([]byte(c.Payload), &head); err != nil {
			return "", errors.New("the request is not JSON")
		}
		rs := p.current()
		if rs == nil || head.RunID == "" || head.RunID != rs.id {
			return "", errStaleRun
		}
		origin, admitted := rs.admission(c.Frame.ID)
		if !admitted {
			return "", errNotAdmitted
		}
		if !pinnable(origin) || c.Frame.Origin != origin {
			return "", errOriginChanged
		}
		switch binding {
		case BindingMatch:
			return p.serveMatch(ctx, c.Payload)
		case BindingResume:
			return p.serveResume(ctx, rs, c)
		default:
			return serveTerminal(rs, c)
		}
	}
}

// reply wraps a result in the engine's binding envelope.
func reply(result any, err error) (string, error) {
	var env any
	if err != nil {
		env = map[string]any{"ok": false, "error": err.Error()}
	} else {
		env = map[string]any{"ok": true, "result": result}
	}
	b, merr := json.Marshal(env)
	return string(b), merr
}

func (p *Page) serveMatch(ctx context.Context, payload string) (string, error) {
	var req MatchRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return reply(nil, err)
	}
	if p.host.Matcher == nil {
		return reply(nil, errors.New("no matcher is available"))
	}
	res, err := p.host.Matcher.Match(ctx, req)
	if err == nil && res.Results == nil {
		res.Results = []Resolution{}
	}
	return reply(res, err)
}

func (p *Page) serveResume(ctx context.Context, rs *runState, c browser.BindingCall) (string, error) {
	var req ResumeRequest
	if err := json.Unmarshal([]byte(c.Payload), &req); err != nil {
		return reply(nil, err)
	}
	req.Origin = c.Frame.Origin
	if p.host.Resumes == nil {
		return reply(nil, nil)
	}
	doc, err := p.host.Resumes.Resume(ctx, req)
	if err != nil {
		return reply(nil, err)
	}
	if doc == nil {
		return reply(nil, nil)
	}
	rs.mu.Lock()
	if rs.served[c.Frame.ID] == nil {
		rs.served[c.Frame.ID] = map[string]ResumePayload{}
	}
	rs.served[c.Frame.ID][doc.FileName] = *doc
	rs.mu.Unlock()
	return reply(doc, nil)
}

func serveTerminal(rs *runState, c browser.BindingCall) (string, error) {
	var n TerminalNotice
	if err := json.Unmarshal([]byte(c.Payload), &n); err != nil {
		return reply(nil, err)
	}
	rs.mu.Lock()
	rs.terminals = append(rs.terminals, FrameNotice{FrameID: c.Frame.ID, Notice: n})
	rs.mu.Unlock()
	select {
	case rs.arrived <- struct{}{}:
	default:
	}
	return reply(nil, nil)
}
