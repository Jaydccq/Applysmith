package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/lazyregexp"
)

// fakeDriver stands in for a tab: it answers the protocol's expressions from a
// script, so the state machine is tested without a browser.
type fakeDriver struct {
	mu       sync.Mutex
	frames   []browser.Frame
	urls     map[string]string // frame -> location.href
	origins  map[string]string // frame -> location.origin; derived from the url when unset
	closed   []func()
	badLoc   map[string]bool // frames whose location reply is not readable
	startArg map[string][]startArgs
	probes   map[string]probeResult
	present  map[string]bool
	starts   map[string]int
	stops    []string
	setFiles []string
	setPaths [][]string
	setErr   error // what SetFiles returns
	handlers map[string]browser.BindingHandler
	runIDs   map[string]string

	// status returns the n-th status poll (from 1) of a frame: a JSON status or an error.
	status func(frame string, n int) (string, error)
	// onStart can fail the start expression.
	onStart func(frame string, n int) error
	// onEval sees every expression first; a non-nil error is returned as is.
	onEval func(frame, expr string) error
	// oldStamp makes a frame report that stamp until the bundle is evaluated in it.
	oldStamp map[string]string
	// helperNonce is, by frame, the nonce the frame's main-world helper of the
	// current build listens on; helperInstalls counts the helper sources a
	// frame was given, and usedHelper the nonces its engine was told.
	helperNonce    map[string]string
	helperInstalls map[string]int
	usedHelper     map[string][]string
	// onAnswer answers an __applysmithEngine.answer call; unset, the call handles nothing.
	onAnswer func(frame, expr string, n int) (string, error)
	answers  int
	// reports are the frame reports the top frame was given, as the engine
	// receives them; each failed frame comes back as one event.
	reports [][]FrameDecision
	polls   map[string]int
	// drains are, by frame, the events a frame's engine holds for the next
	// drain, as the engine serializes them; a frame it does not name holds no
	// engine.
	drains map[string]string

	// Like the engine, the fake reports a terminal status to the terminal
	// binding too, once per frame and run: at once, after terminalDelay, or
	// never (noTerminal).
	terminalDelay map[string]time.Duration
	noTerminal    map[string]bool
	notified      map[string]bool
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		frames: []browser.Frame{{ID: "top", IsTop: true, Origin: "https://jobs.example.com", URL: "https://jobs.example.com/apply"}},
		urls:   map[string]string{}, origins: map[string]string{}, startArg: map[string][]startArgs{}, probes: map[string]probeResult{}, present: map[string]bool{},
		starts: map[string]int{}, handlers: map[string]browser.BindingHandler{}, runIDs: map[string]string{}, polls: map[string]int{},
		terminalDelay: map[string]time.Duration{}, noTerminal: map[string]bool{}, notified: map[string]bool{},
		helperNonce: map[string]string{}, helperInstalls: map[string]int{}, usedHelper: map[string][]string{},
	}
}

// helperSourceNonce reports whether expr is the helper bundle (not the short
// question the host asks first) and the nonce it was built with: the bundle
// ends by installing itself under that nonce.
func helperSourceNonce(expr string) (string, bool) {
	if len(expr) < 5000 {
		return "", false
	}
	m := helperInstallRE.FindStringSubmatch(expr)
	if m == nil {
		return "", false
	}
	return m[1], true
}

var helperInstallRE = lazyregexp.New(`"([0-9a-f]{32})"`)

func (d *fakeDriver) addFrame(id, url string, probe probeResult) {
	d.frames = append(d.frames, browser.Frame{ID: id, ParentID: "top", URL: url, Origin: "https://x.example.com"})
	d.urls[id] = url
	d.probes[id] = probe
}

func (d *fakeDriver) Frames(context.Context) ([]browser.Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]browser.Frame(nil), d.frames...), nil
}

func (d *fakeDriver) AddBinding(_ string, name string, h browser.BindingHandler) error {
	d.handlers[name] = h
	return nil
}

func (d *fakeDriver) OnTabClosed(fn func()) { d.closed = append(d.closed, fn) }

// closeTab runs what the page registered for the tab's close.
func (d *fakeDriver) closeTab() {
	for _, fn := range d.closed {
		fn()
	}
}

// originOf is the origin the fake reports for a frame's document.
func (d *fakeDriver) originOf(frame string) string {
	if o, ok := d.origins[frame]; ok {
		return o
	}
	if d.urls[frame] == "" {
		for _, f := range d.frames {
			if f.ID == frame {
				return f.Origin
			}
		}
	}
	u, err := url.Parse(d.urls[frame])
	if err == nil && u.Scheme == "about" {
		return "https://jobs.example.com" // an about:blank frame inherits its parent's origin
	}
	if err != nil || u.Host == "" {
		return "null"
	}
	return u.Scheme + "://" + u.Host
}

func (d *fakeDriver) SetFiles(_ context.Context, frame, _, locator string, paths []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setFiles = append(d.setFiles, frame+" "+locator)
	d.setPaths = append(d.setPaths, paths)
	return d.setErr
}

func doneStatus(runID string) string {
	return `{"state":"done","runId":"` + runID + `","stamp":"` + Stamp() + `","summary":{"host":"h","url":"u","rows":[],"counts":{"filled":2}}}`
}

func runningStatus(runID string) string {
	return `{"state":"running","runId":"` + runID + `","stamp":"` + Stamp() + `"}`
}

func (d *fakeDriver) startCount(frame string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts[frame]
}

func (d *fakeDriver) Eval(_ context.Context, frame, _, expr string) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.onEval != nil {
		if err := d.onEval(frame, expr); err != nil {
			return nil, err
		}
	}
	switch {
	case strings.Contains(expr, ".listens("):
		if nonce, ok := helperSourceNonce(expr); ok {
			d.helperNonce[frame] = nonce
			d.helperInstalls[frame]++
			return json.RawMessage("null"), nil
		}
		held := d.helperNonce[frame]
		return json.RawMessage(fmt.Sprintf("%t", held != "" && strings.Contains(expr, ".listens("+jsString(held)+")"))), nil
	case strings.HasPrefix(expr, "__applysmithEngine.useHelper("):
		var nonce string
		_ = json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(expr, "__applysmithEngine.useHelper("), ")")), &nonce)
		d.usedHelper[frame] = append(d.usedHelper[frame], nonce)
		return json.RawMessage("null"), nil
	case len(expr) > 10000:
		d.present[frame] = true
		delete(d.oldStamp, frame)
		return json.RawMessage("null"), nil
	case strings.Contains(expr, "e.status().stamp"):
		if old := d.oldStamp[frame]; old != "" {
			return json.RawMessage(`"` + old + `"`), nil
		}
		if d.present[frame] {
			return json.RawMessage(`"` + Stamp() + `"`), nil
		}
		return json.RawMessage("null"), nil
	case strings.Contains(expr, "__applysmithEngine.start("):
		// The host guards the start with the origin the frame was admitted at,
		// in the same evaluation.
		call := expr
		if guard, ok := strings.CutPrefix(expr, "location.origin!=="); ok {
			var want string
			dec := json.NewDecoder(strings.NewReader(guard))
			_ = dec.Decode(&want)
			if d.originOf(frame) != want {
				return json.RawMessage(`{"started":false,"reason":"origin-changed"}`), nil
			}
			call = expr[strings.Index(expr, "__applysmithEngine.start("):]
		}
		d.starts[frame]++
		var args startArgs
		_ = json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(call, "__applysmithEngine.start("), ")")), &args)
		// The run id is the host's, sent with the start: it is known even when the start's answer is lost.
		d.runIDs[frame] = args.RunID
		if d.onStart != nil {
			if err := d.onStart(frame, d.starts[frame]); err != nil {
				return nil, err
			}
		}
		d.startArg[frame] = append(d.startArg[frame], args)
		d.polls[frame] = 0
		return json.RawMessage(`{"started":true,"runId":"` + args.RunID + `"}`), nil
	case strings.Contains(expr, "e.status():null"):
		d.polls[frame]++
		s := doneStatus(d.runIDs[frame])
		if d.status != nil {
			var err error
			if s, err = d.status(frame, d.polls[frame]); err != nil {
				return nil, err
			}
			s = strings.ReplaceAll(s, "RUN", d.runIDs[frame])
		}
		d.notifyTerminal(frame, s)
		return json.RawMessage(s), nil
	case strings.Contains(expr, "__applysmithEngine.answer("):
		d.answers++
		if d.onAnswer != nil {
			out, err := d.onAnswer(frame, expr, d.answers)
			return json.RawMessage(out), err
		}
		return json.RawMessage(`{"results":[],"handled":0}`), nil
	case strings.Contains(expr, "e.drainEvents()"):
		text, ok := d.drains[frame]
		if !ok {
			return json.RawMessage("null"), nil
		}
		d.drains[frame] = "[]" // handed over once
		b, _ := json.Marshal(text)
		return b, nil
	case strings.Contains(expr, "e.stop("):
		d.stops = append(d.stops, frame+" "+expr)
		return json.RawMessage("true"), nil
	case expr == frameLocation && d.badLoc[frame]:
		return json.RawMessage(`"not an object"`), nil
	case expr == frameLocation:
		return json.RawMessage(fmt.Sprintf("{\"href\":%q,\"origin\":%q}", d.urls[frame], d.originOf(frame))), nil
	case expr == "location.origin":
		return json.RawMessage(fmt.Sprintf("%q", d.originOf(frame))), nil
	case expr == "document.readyState":
		return json.RawMessage(`"complete"`), nil
	case strings.Contains(expr, "__applysmithEngine.probe()"):
		b, _ := json.Marshal(d.probes[frame])
		return b, nil
	case strings.Contains(expr, "__applysmithEngine.decideFrame("):
		return d.decide(expr)
	case strings.Contains(expr, "__applysmithEngine.frameReport("):
		var decs []FrameDecision
		_ = json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(expr, "__applysmithEngine.frameReport("), ")")), &decs)
		d.reports = append(d.reports, decs)
		events := []LearningEvent{}
		for _, dec := range decs {
			if dec.Failed() {
				events = append(events, LearningEvent{Kind: "frame-unreachable", ControlKind: "iframe", Reason: dec.Outcome, Question: "Application form frame could not be reached: " + dec.URL})
			}
		}
		b, _ := json.Marshal(events)
		return b, nil
	}
	return nil, fmt.Errorf("fake driver: unexpected expression %.60q", expr)
}

// notifyTerminal calls the terminal binding for a status that ends the run.
func (d *fakeDriver) notifyTerminal(frame, status string) {
	var st Status
	if json.Unmarshal([]byte(status), &st) != nil || !st.Terminal() || d.noTerminal[frame] || d.notified[frame+st.RunID] {
		return
	}
	d.notified[frame+st.RunID] = true
	h, origin := d.handlers[BindingTerminal], d.originOf(frame)
	if h == nil {
		return
	}
	call := func() {
		b, _ := json.Marshal(TerminalNotice{RunID: st.RunID, State: st.State})
		payload := string(b)
		_, _ = h(context.Background(), browser.BindingCall{Frame: browser.Frame{ID: frame, Origin: origin}, Payload: payload})
	}
	if delay := d.terminalDelay[frame]; delay > 0 {
		time.AfterFunc(delay, call)
		return
	}
	call()
}

// decide mirrors the engine's rules closely enough for the walk: registry
// hosts are admitted, ad hosts skipped, anything else needs a probe.
func (d *fakeDriver) decide(expr string) (json.RawMessage, error) {
	var req decisionRequest
	_ = json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(expr, "__applysmithEngine.decideFrame("), ")")), &req)
	dec := FrameDecision{FrameID: req.FrameID, URL: req.URL}
	switch {
	case req.Failure != nil:
		dec.Outcome, dec.Reason = req.Failure.Outcome, req.Failure.Reason
	case strings.Contains(req.URL, "greenhouse.io"):
		dec.Outcome = AdmittedRegistry
	case strings.Contains(req.URL, "doubleclick.net"):
		dec.Outcome = SkippedDenylist
	case req.Probe == nil:
		return json.RawMessage("null"), nil
	case req.Probe.Controls >= 2 || req.Probe.Required >= 1:
		dec.Outcome, dec.Controls = AdmittedProbe, req.Probe.Controls
	default:
		dec.Outcome = SkippedZeroControl
	}
	b, _ := json.Marshal(dec)
	return b, nil
}

func quick() Timing {
	return Timing{Poll: 5 * time.Millisecond, PollTimeout: time.Second, PageDeadline: 5 * time.Second, HungGrace: 50 * time.Millisecond, DocumentWait: 20 * time.Millisecond, ApplicantRetry: time.Millisecond, FrameGrace: 50 * time.Millisecond}
}

func quickOptions() FillOptions {
	return FillOptions{Applicant: fixtureApplicant, Timing: quick()}
}

func replacedErr() error {
	return &browser.Error{Code: browser.CodeDocumentReplaced, Message: "replaced"}
}
func timeoutErr() error { return &browser.Error{Code: browser.CodeOperationTimeout, Message: "slow"} }
func frameGoneErr() error {
	return &browser.Error{Code: browser.CodeFrameGone, Message: "no frame in the tab"}
}
