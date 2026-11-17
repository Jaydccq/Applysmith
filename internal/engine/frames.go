package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"applysmith/internal/browser"
)

// walker considers every sub-frame of the page while the top frame fills: it
// records one decision per frame (the latest replaces earlier ones), starts
// the engine in each admitted frame once, and follows those fills.
type walker struct {
	f        *filler
	top      browser.Frame
	deadline time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu sync.Mutex
	st map[string]*frameState
}

type frameState struct {
	frame  browser.Frame
	dec    FrameDecision
	origin string // the document origin the decision was made at
	live   bool
	done   bool
	status *Status
	err    error
}

func newWalker(f *filler, top browser.Frame, deadline time.Time) *walker {
	return &walker{f: f, top: top, deadline: deadline, st: map[string]*frameState{}}
}

// followTop polls the top frame to its end, stepping the walk on every tick.
func (w *walker) followTop(ctx context.Context) (Status, error) {
	w.ctx, w.cancel = context.WithCancel(ctx)
	return w.f.pollWith(ctx, w.top.ID, w.deadline, w.step)
}

// finish does a last sweep and waits for the frames that are still filling.
func (w *walker) finish(ctx context.Context) error {
	for {
		if err := w.step(ctx); err != nil {
			return err
		}
		w.mu.Lock()
		live := 0
		for _, s := range w.st {
			if s.live {
				live++
			}
		}
		w.mu.Unlock()
		if live == 0 {
			w.wg.Wait()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.f.o.Timing.Poll):
		}
	}
}

// awaitTerminals holds the result back until every frame that ended its fill
// has delivered its terminal notice. The engine settles its status first and
// sends the notice from a later task, so a poll can see the end state before
// the binding call reaches Go. The wait is bounded by HungGrace; a sub-frame
// whose notice is still missing then is reported like an unreachable one, and
// a missing notice from the top frame is logged (the top has no decision).
func (w *walker) awaitTerminals(ctx context.Context) {
	rs := w.f.rs
	missing := func() []string {
		w.mu.Lock()
		defer w.mu.Unlock()
		var out []string
		if !rs.terminalFrom(w.top.ID) {
			out = append(out, w.top.ID)
		}
		for id, s := range w.st {
			if s.status != nil && !rs.terminalFrom(id) {
				out = append(out, id)
			}
		}
		return out
	}
	timer := time.NewTimer(w.f.o.Timing.HungGrace)
	defer timer.Stop()
	for len(missing()) > 0 {
		select {
		case <-rs.arrived:
		case <-timer.C:
			w.reportMissing(missing())
			return
		case <-ctx.Done():
			return
		}
	}
}

func (w *walker) reportMissing(ids []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range ids {
		s := w.st[id]
		if s == nil {
			w.f.o.logf("terminal-missing frame=%s", id)
			continue
		}
		s.dec = FrameDecision{FrameID: s.dec.FrameID, URL: s.dec.URL, Outcome: FailedNoListener, Reason: "the frame's terminal notice did not arrive"}
		w.f.o.logf("frame-failed frame=%s reason=%s", id, s.dec.Reason)
	}
}

// abandon ends the frame fills of a failed attempt.
func (w *walker) abandon() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

// step decides frames not decided yet, re-probes frames that held no controls
// (a script may have built them since), and starts the admitted ones.
func (w *walker) step(ctx context.Context) error {
	frames, err := w.f.p.d.Frames(ctx)
	if err != nil {
		if isReplaced(err) || isFatal(err) {
			return err
		}
		return nil
	}
	for _, fr := range frames {
		if fr.IsTop {
			continue
		}
		w.mu.Lock()
		s := w.st[fr.ID]
		again := s == nil || s.dec.Outcome == SkippedZeroControl
		w.mu.Unlock()
		if again {
			dec, origin, err := w.decide(ctx, fr)
			if err != nil {
				return err
			}
			if dec == nil {
				continue
			}
			s = w.record(fr, *dec, origin)
		}
		w.mu.Lock()
		start := s.dec.Admitted() && !s.live && !s.done
		if start {
			s.live = true
		}
		w.mu.Unlock()
		if start {
			w.launch(s)
		}
	}
	return nil
}

func isFatal(err error) bool {
	_, ok := CodeOf(pageError(err))
	return ok
}

func (w *walker) record(fr browser.Frame, d FrameDecision, origin string) *frameState {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.st[fr.ID]
	if s == nil {
		s = &frameState{frame: fr}
		w.st[fr.ID] = s
	}
	if s.dec != d {
		w.f.o.logf("frame-decision frame=%s url=%s outcome=%s controls=%d reason=%s", d.FrameID, d.URL, d.Outcome, d.Controls, d.Reason)
	}
	s.dec, s.origin = d, origin
	return s
}

type decisionRequest struct {
	FrameID string        `json:"frameId"`
	URL     string        `json:"url"`
	Probe   *probeResult  `json:"probe,omitempty"`
	Failure *failureInput `json:"failure,omitempty"`
}

type failureInput struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// ask puts a decision to the top frame's engine, which holds the registry and
// the deny-list; nil means the frame has to be probed first.
func (w *walker) ask(ctx context.Context, req decisionRequest) (*FrameDecision, error) {
	b, _ := json.Marshal(req)
	raw, err := w.f.eval(ctx, w.top.ID, `__applysmithEngine.decideFrame(`+string(b)+`)`)
	if err != nil {
		return nil, err
	}
	var d *FrameDecision
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, newError(CodePollFailed, err, "the frame decision is not readable")
	}
	return d, nil
}

func (w *walker) failed(ctx context.Context, fr browser.Frame, href, outcome, reason string) (*FrameDecision, error) {
	return w.ask(ctx, decisionRequest{FrameID: fr.ID, URL: href, Failure: &failureInput{Outcome: outcome, Reason: reason}})
}

// frameLocation reads where a frame's document is: the page it shows and the
// origin the binding wrappers will report for it.
const frameLocation = `({href:location.href,origin:location.origin})`

type frameLoc struct {
	Href   string `json:"href"`
	Origin string `json:"origin"`
}

// decide returns the frame's decision and the origin its document had when it
// was decided, or a nil decision when the frame is mid-navigation and is
// looked at again on the next tick.
func (w *walker) decide(ctx context.Context, fr browser.Frame) (*FrameDecision, string, error) {
	d, origin, err := w.look(ctx, fr)
	return unpinnable(d, origin), origin, err
}

// look reads where the frame is and decides it, admitted or not.
func (w *walker) look(ctx context.Context, fr browser.Frame) (*FrameDecision, string, error) {
	raw, err := w.f.eval(ctx, fr.ID, frameLocation)
	if err != nil {
		d, err := w.frameFailure(ctx, fr, fr.URL, FailedProbe, err)
		return d, "", err
	}
	var loc frameLoc
	if err := json.Unmarshal(raw, &loc); err != nil {
		d, err := w.frameFailure(ctx, fr, fr.URL, FailedProbe, err)
		return d, "", err
	}
	failed := func(outcome string, cause error) (*FrameDecision, string, error) {
		d, err := w.frameFailure(ctx, fr, loc.Href, outcome, cause)
		return d, loc.Origin, err
	}
	d, err := w.ask(ctx, decisionRequest{FrameID: fr.ID, URL: loc.Href})
	if err != nil || d != nil {
		return d, loc.Origin, err
	}
	if err := w.f.ensure(ctx, fr.ID); err != nil {
		return failed(FailedInject, err)
	}
	raw, err = w.f.eval(ctx, fr.ID, `__applysmithEngine.probe()`)
	if err != nil {
		return failed(FailedProbe, err)
	}
	var pr probeResult
	if err := json.Unmarshal(raw, &pr); err != nil {
		return failed(FailedNoListener, err)
	}
	d, err = w.ask(ctx, decisionRequest{FrameID: fr.ID, URL: loc.Href, Probe: &pr})
	return d, loc.Origin, err
}

// unpinnable turns the admission of a frame whose origin cannot be pinned (an
// opaque origin, or none) into a failure: the start guard would pass for any
// other document of that kind.
func unpinnable(d *FrameDecision, origin string) *FrameDecision {
	if d == nil || !d.Admitted() || pinnable(origin) {
		return d
	}
	return &FrameDecision{FrameID: d.FrameID, URL: d.URL, Outcome: FailedInject, Reason: "the frame has no origin to pin"}
}

// frameFailure turns an error in one frame into a failed decision, so one bad
// frame never stops the others. An error that ends the whole fill propagates;
// a frame that went away mid-look is retried on the next tick.
func (w *walker) frameFailure(ctx context.Context, fr browser.Frame, href, outcome string, cause error) (*FrameDecision, error) {
	if c, ok := browser.CodeOf(cause); ok && (c == browser.CodeTabClosed || c == browser.CodeBrowserDisconnected) {
		return nil, cause
	}
	if isReplaced(cause) {
		return nil, nil
	}
	if c, ok := browser.CodeOf(cause); ok && c == browser.CodeFrameGone {
		return nil, nil
	}
	return w.failed(ctx, fr, href, outcome, cause.Error())
}

func (w *walker) launch(s *frameState) {
	w.mu.Lock()
	w.f.rs.admit(s.frame.ID, s.origin)
	w.mu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		var st Status
		var err error
		notAdmitted := false
		for again := 0; ; again++ {
			w.mu.Lock()
			origin := s.origin
			w.mu.Unlock()
			st, err = w.f.runFrame(w.ctx, s.frame.ID, origin, w.deadline)
			if !isReplaced(err) || again >= MaxReinjections || !w.isRegistry(s) {
				break
			}
			w.f.o.logf("frame-replaced frame=%s attempt=%d reason=%v", s.frame.ID, again+1, err)
			// A registry frame whose document is replaced is considered again
			// from where it now is: the new document may not be a form the
			// registry admits, or may belong to another site altogether.
			w.f.waitDocument(w.ctx, s.frame.ID)
			var ok bool
			if ok, err = w.readmit(w.ctx, s); err != nil || !ok {
				notAdmitted = err == nil
				break
			}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		s.live, s.done = false, true
		if notAdmitted {
			return // readmit recorded the new decision; nothing was started
		}
		if err != nil {
			s.err = err
			s.dec = FrameDecision{FrameID: s.dec.FrameID, URL: s.dec.URL, Outcome: FailedInject, Reason: err.Error()}
			w.f.o.logf("frame-failed frame=%s reason=%v", s.frame.ID, err)
			return
		}
		s.status = &st
	}()
}

func (w *walker) isRegistry(s *frameState) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return s.dec.Outcome == AdmittedRegistry
}

// readmit decides a registry frame's replaced document afresh. It reports true
// when the registry admits the new document too (the frame is admitted again at
// its new origin) and false, with the new decision recorded, when it does not.
// A document the registry does not know is never filled on the strength of the
// earlier admission, even if a probe would admit it: the embed has gone
// somewhere nobody decided on, so the frame is reported as unreachable.
func (w *walker) readmit(ctx context.Context, s *frameState) (bool, error) {
	dec, origin, err := w.decide(ctx, s.frame)
	if err != nil {
		return false, err
	}
	if dec == nil {
		return false, newError(CodeInjectFailed, nil, "the frame is still loading after its document was replaced")
	}
	if dec.Admitted() && dec.Outcome != AdmittedRegistry {
		dec = &FrameDecision{FrameID: dec.FrameID, URL: dec.URL, Outcome: FailedInject, Reason: "the embed moved to a page the registry does not admit"}
	}
	w.record(s.frame, *dec, origin)
	if dec.Outcome != AdmittedRegistry {
		return false, nil
	}
	w.f.rs.admit(s.frame.ID, origin)
	return true, nil
}

// result assembles the page result from the top frame's end state.
func (w *walker) result(top Status) *PageResult {
	res := &PageResult{RunID: w.f.rs.id, Stamp: Stamp(), Top: top}
	w.mu.Lock()
	for _, s := range w.st {
		res.Frames = append(res.Frames, FrameResult{Frame: s.frame, Decision: s.dec, Status: s.status, Err: errText(s.err)})
	}
	w.mu.Unlock()
	sortFrameResults(res.Frames)
	for _, fr := range res.Frames {
		res.Decisions = append(res.Decisions, fr.Decision)
		if fr.Decision.Failed() && w.f.o.OnUnreached != nil {
			w.f.o.OnUnreached(fr.Decision)
		}
	}
	w.f.rs.mu.Lock()
	res.Terminals = append(res.Terminals, w.f.rs.terminals...)
	w.f.rs.mu.Unlock()
	return res
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
