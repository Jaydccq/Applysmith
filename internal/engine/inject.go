package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"applysmith/internal/browser"
)

// Protocol bounds.
const (
	// MaxReinjections is how often a page fill may restart on a replaced document.
	MaxReinjections = 3
	// ApplicantAttempts is how often the applicant snapshot is read.
	ApplicantAttempts = 3
)

// Timing holds the protocol's clocks; a zero field takes its default.
type Timing struct {
	Poll           time.Duration // status poll interval
	PollTimeout    time.Duration // one evaluation, inside the browser layer's deadline
	PageDeadline   time.Duration // per page fill, counted from start
	HungGrace      time.Duration // how long past the deadline a run may still be running
	DocumentWait   time.Duration // wait for a replaced document
	ApplicantRetry time.Duration // between applicant snapshot attempts
	FrameGrace     time.Duration // how long a frame may be missing from the tab while its fill is followed
}

// DefaultTiming is the specified timing.
func DefaultTiming() Timing {
	return Timing{
		Poll: time.Second, PollTimeout: 20 * time.Second, PageDeadline: 300 * time.Second,
		HungGrace: 10 * time.Second, DocumentWait: 2 * time.Second, ApplicantRetry: 1500 * time.Millisecond,
		FrameGrace: 20 * time.Second,
	}
}

func (t Timing) withDefaults() Timing {
	d := DefaultTiming()
	pick := func(v, def time.Duration) time.Duration {
		if v > 0 {
			return v
		}
		return def
	}
	return Timing{
		Poll: pick(t.Poll, d.Poll), PollTimeout: pick(t.PollTimeout, d.PollTimeout), PageDeadline: pick(t.PageDeadline, d.PageDeadline),
		HungGrace: pick(t.HungGrace, d.HungGrace), DocumentWait: pick(t.DocumentWait, d.DocumentWait),
		ApplicantRetry: pick(t.ApplicantRetry, d.ApplicantRetry), FrameGrace: pick(t.FrameGrace, d.FrameGrace),
	}
}

// errEngineMissing reports a document that no longer holds the engine, which
// after a successful injection means the document was replaced.
var errEngineMissing = errors.New("the engine is missing from the document")

func isReplaced(err error) bool {
	if errors.Is(err, errEngineMissing) {
		return true
	}
	c, ok := browser.CodeOf(err)
	return ok && c == browser.CodeDocumentReplaced
}

func isTimeout(err error) bool {
	c, ok := browser.CodeOf(err)
	return ok && c == browser.CodeOperationTimeout
}

func isFrameGone(err error) bool {
	c, ok := browser.CodeOf(err)
	return ok && c == browser.CodeFrameGone
}

// pageError maps a browser failure that ends a fill to its fill error.
func pageError(err error) error {
	if code, ok := browser.CodeOf(err); ok {
		switch code {
		case browser.CodeTabClosed:
			return newError(CodeTabClosed, err, "the tab was closed")
		case browser.CodeBrowserDisconnected:
			return newError(CodeBrowserLost, err, "the browser connection was lost")
		}
	}
	return err
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// filler holds one page fill's settings and state.
type filler struct {
	p         *Page
	o         FillOptions
	rs        *runState
	applicant json.RawMessage
	top       string // the top frame's id
}

// eval runs expr in the engine's world under the per-call deadline.
func (f *filler) eval(ctx context.Context, frame, expr string) (json.RawMessage, error) {
	c, cancel := context.WithTimeout(ctx, f.o.Timing.PollTimeout)
	defer cancel()
	return f.p.d.Eval(c, frame, World, expr)
}

// ensure makes sure the frame's current document holds the current engine:
// the stamp is compared, and the bundle is evaluated when it is missing or
// differs (the new instance releases the old one's listeners first).
func (f *filler) ensure(ctx context.Context, frame string) error {
	raw, err := f.eval(ctx, frame, `(()=>{const e=globalThis.__applysmithEngine;return e?e.status().stamp:null})()`)
	if err != nil {
		return err
	}
	var have *string
	_ = json.Unmarshal(raw, &have)
	if have != nil && *have == Stamp() {
		return nil
	}
	if _, err := f.eval(ctx, frame, string(Bundle())); err != nil {
		return err
	}
	raw, err = f.eval(ctx, frame, `(()=>{const e=globalThis.__applysmithEngine;return e?e.status().stamp:null})()`)
	if err != nil {
		return err
	}
	have = nil
	_ = json.Unmarshal(raw, &have)
	if have == nil || *have != Stamp() {
		return newError(CodeInjectFailed, nil, "the engine did not report the bundle's stamp after injection")
	}
	return nil
}

// ensureFrame injects the engine and, when the fill asked for it, the
// main-world helper.
func (f *filler) ensureFrame(ctx context.Context, frame string) error {
	if err := f.ensure(ctx, frame); err != nil {
		return err
	}
	if f.o.MainWorld {
		return f.installHelper(ctx, frame)
	}
	return nil
}

type startArgs struct {
	RunID                   string          `json:"runId"`
	Applicant               json.RawMessage `json:"applicant"`
	Posting                 PostingContext  `json:"posting"`
	AcceptLegalDeclarations bool            `json:"acceptLegalDeclarations"`
	Edited                  []string        `json:"edited,omitempty"`
	PageDeadlineMs          int64           `json:"pageDeadlineMs"`
}

// reasonOriginChanged is the start reply of a frame whose document is no longer
// at the origin it was admitted at.
const reasonOriginChanged = "origin-changed"

// start begins a fill in the frame. The origin is the one the frame was
// admitted at, and is required: it is compared with the document's own origin
// inside the same evaluation that starts the engine, so a document that was
// replaced between the decision and the start is never handed the applicant
// data. When the reply is lost after the page may have started, the run is
// stopped before the failure is reported.
func (f *filler) start(ctx context.Context, frame, origin string, deadline time.Time) error {
	if !pinnable(origin) {
		return newError(CodeStartRefused, nil, "the frame has no origin to pin, so the applicant data is not handed to it")
	}
	sa := startArgs{
		RunID: f.rs.id, Applicant: f.applicant, Posting: f.o.Posting,
		AcceptLegalDeclarations: f.o.AcceptLegalDeclarations,
		PageDeadlineMs:          max(1, time.Until(deadline).Milliseconds()),
	}
	doc := frame
	if frame == f.top {
		doc = ""
	}
	sa.Edited = f.o.Edited[doc]
	args, err := json.Marshal(sa)
	if err != nil {
		return err
	}
	expr := `location.origin!==` + jsString(origin) + `?{started:false,reason:` + jsString(reasonOriginChanged) + `}:__applysmithEngine.start(` + string(args) + `)`
	raw, err := f.eval(ctx, frame, expr)
	if err != nil {
		if isReplaced(err) {
			return err
		}
		f.stop(frame)
		return newError(CodeStartLost, pageError(err), "the start acknowledgement was lost")
	}
	var rep startReply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return newError(CodeStartLost, err, "the start reply is not readable")
	}
	if !rep.Started && rep.Reason == reasonOriginChanged && frame == f.top {
		return newError(CodeTopOriginChanged, nil, "the tab moved to another origin during the fill, so nothing was typed there")
	}
	if !rep.Started {
		return newError(CodeStartRefused, nil, "the engine refused to start: %s", rep.Reason)
	}
	return nil
}

// stop ends this run's fill in the frame, best effort and on a fresh deadline
// because the caller's context may be the thing that ended.
func (f *filler) stop(frame string) {
	if err := f.stopRun(frame, f.rs.id); err != nil {
		f.o.logf("stop-failed frame=%s reason=%v", frame, err)
	}
}

// stopRun is stop for a named run, which an answer call needs: the run it
// writes for is the fill that read the page, not the call's own bookkeeping.
func (f *filler) stopRun(frame, runID string) error {
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := f.p.d.Eval(c, frame, World, `(()=>{const e=globalThis.__applysmithEngine;return e?e.stop(`+jsString(runID)+`,"cancelled"):false})()`)
	return err
}

// disposeJS ends a document's engine (its restore pass, tail guard, watchers,
// timers and listeners) and takes it out of the page.
const disposeJS = `(()=>{const e=globalThis.__applysmithEngine;if(!e)return false;e.dispose();delete globalThis.__applysmithEngine;return true})()`

// helperDisposeJS takes the main-world helper out of a document: the page gets
// its own fetch, XMLHttpRequest and click back, and the helper's listeners go.
const helperDisposeJS = `(()=>{const h=` + helperHolder + `;if(!h)return false;h.dispose();return true})()`

// Dispose takes the engine and the main-world helper out of every document of
// the tab, for a tab handed to the user. An isolated world outlives the
// DevTools session that made it, so detaching alone would leave the restore
// pass writing the run's values into a page the user now owns. A run that
// takes the tab back injects both again. It is best effort within ctx: a frame
// it cannot reach keeps what it had.
func (p *Page) Dispose(ctx context.Context) {
	frames, err := p.d.Frames(ctx)
	if err != nil {
		return
	}
	for _, fr := range frames {
		_, _ = p.d.Eval(ctx, fr.ID, World, disposeJS)
		_, _ = p.d.Eval(ctx, fr.ID, "", helperDisposeJS)
	}
}

func (f *filler) status(ctx context.Context, frame string) (Status, error) {
	raw, err := f.eval(ctx, frame, `(()=>{const e=globalThis.__applysmithEngine;return e?e.status():null})()`)
	if err != nil {
		return Status{}, err
	}
	var st *Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return Status{}, newError(CodePollFailed, err, "the status is not readable")
	}
	if st == nil || st.State == StateIdle {
		return Status{}, errEngineMissing
	}
	if st.RunID != f.rs.id {
		// Another run owns this frame's engine now; its end state is not ours.
		return Status{}, newError(CodePollFailed, nil, "the frame reports run %q, not this fill's run", st.RunID)
	}
	return *st, nil
}

// poll follows a frame's run to its end; see pollWith.
func (f *filler) poll(ctx context.Context, frame string, deadline time.Time) (Status, error) {
	return f.pollWith(ctx, frame, deadline, nil)
}

// pollWith reads the frame's status once per interval until it is terminal,
// calling each after every poll that finds the run still going. A poll that
// times out is retried against the same run, because a starved host is slow
// and not finished; a run still running well past its deadline is stopped.
func (f *filler) pollWith(ctx context.Context, frame string, deadline time.Time, each func(context.Context) error) (Status, error) {
	tick := time.NewTicker(f.o.Timing.Poll)
	defer tick.Stop()
	var goneSince time.Time
	for {
		select {
		case <-ctx.Done():
			f.stop(frame)
			return Status{}, ctx.Err()
		case <-tick.C:
		}
		st, err := f.status(ctx, frame)
		if !isFrameGone(err) {
			goneSince = time.Time{}
		} else if goneSince.IsZero() {
			goneSince = time.Now()
			f.o.logf("frame-missing frame=%s reason=%v", frame, err)
		}
		switch {
		case err == nil && st.Terminal():
			return st, nil
		case err == nil, isTimeout(err):
		// An out-of-process frame whose renderer is busy with the fill can
		// drop out of the tab's frame table for a moment and come back.
		case isFrameGone(err) && time.Since(goneSince) < f.o.Timing.FrameGrace:
		case isReplaced(err):
			return Status{}, err
		default:
			// The run may still be going: left alone it would refuse the
			// next fill of this frame until its own deadline.
			f.stop(frame)
			return Status{}, pageError(err)
		}
		if err == nil && each != nil {
			if err := each(ctx); err != nil {
				f.stop(frame)
				return Status{}, err
			}
		}
		if time.Now().After(deadline.Add(f.o.Timing.HungGrace)) {
			f.stop(frame)
			return Status{}, newError(CodeEngineHung, nil, "the engine was still running %v after the page deadline", f.o.Timing.HungGrace)
		}
	}
}

// runFrame injects the engine into a frame, starts it and follows it to a
// terminal state.
func (f *filler) runFrame(ctx context.Context, frame, origin string, deadline time.Time) (Status, error) {
	if err := f.ensureFrame(ctx, frame); err != nil {
		return Status{}, err
	}
	if err := f.start(ctx, frame, origin, deadline); err != nil {
		return Status{}, err
	}
	return f.poll(ctx, frame, deadline)
}

// waitDocument gives a replaced document up to the configured wait to load.
func (f *filler) waitDocument(ctx context.Context, frame string) {
	end := time.Now().Add(f.o.Timing.DocumentWait)
	for time.Now().Before(end) {
		raw, err := f.eval(ctx, frame, `document.readyState`)
		var state string
		if err == nil && json.Unmarshal(raw, &state) == nil && state != "loading" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}
