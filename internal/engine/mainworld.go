package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// InitScripter is the part of a tab's hooks that registers code to run at the
// start of every document. *browser.Hooks implements it; a driver that does
// not simply cannot observe submissions across a navigation.
type InitScripter interface {
	AddInitScript(ctx context.Context, source string) error
}

// helperHolder is the page-side slot the helper records itself in.
const helperHolder = `globalThis[Symbol.for('applysmith.mainworld')]`

func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// helperSource is the main-world helper for this page, with the tab's nonce
// and the helper's own stamp in place of the two placeholders.
func (p *Page) helperSource() (string, error) {
	b := MainWorldBundle()
	if len(b) == 0 {
		return "", newError(CodeBundleMissing, nil, "the main-world helper is not generated; run make generate")
	}
	b = bytes.Replace(b, []byte("__AJ_NONCE__"), []byte(p.nonce), 1)
	b = bytes.Replace(b, []byte("__AJ_STAMP__"), []byte(MainWorldStamp()), 1)
	return string(b), nil
}

// helperCurrent asks the page whether its helper is this build's and answers
// under this page's nonce. A helper another Page installed in the same
// document has the stamp but listens on that Page's nonce.
func (p *Page) helperCurrent() string {
	return `(()=>{const h=` + helperHolder + `;return !!h&&h.stamp===` + jsString(MainWorldStamp()) + `&&typeof h.listens==="function"&&h.listens(` + jsString(p.nonce) + `)})()`
}

// installHelper makes sure the frame's current document holds the current
// main-world helper under this tab's nonce, and tells the engine the nonce.
// The engine must already be present in the frame.
func (f *filler) installHelper(ctx context.Context, frame string) error {
	c, cancel := context.WithTimeout(ctx, f.o.Timing.PollTimeout)
	defer cancel()
	raw, err := f.p.d.Eval(c, frame, "", f.p.helperCurrent())
	if err != nil {
		return err
	}
	var current bool
	_ = json.Unmarshal(raw, &current)
	if !current {
		src, err := f.p.helperSource()
		if err != nil {
			return err
		}
		if _, err := f.p.d.Eval(c, frame, "", src); err != nil {
			return err
		}
	}
	_, err = f.eval(ctx, frame, `__applysmithEngine.useHelper(`+jsString(f.p.nonce)+`)`)
	return err
}

// ObserveSubmissions installs the pass-through submit observer at the start of
// every document of the tab, so a submission that navigates away is still
// recorded. The observer never blocks, alters or delays a request.
func (p *Page) ObserveSubmissions(ctx context.Context) error {
	is, ok := p.d.(InitScripter)
	if !ok {
		return newError(CodeInjectFailed, nil, "the tab cannot run code at document start")
	}
	src, err := p.helperSource()
	if err != nil {
		return err
	}
	return is.AddInitScript(ctx, src)
}

// SubmitObservations reads what the observer saw in the top frame in the last
// since: evidence that the page's submit path fired, never a confirmation.
func (p *Page) SubmitObservations(ctx context.Context, since time.Duration) ([]SubmitObservation, error) {
	top, err := p.topFrame(ctx)
	if err != nil {
		return nil, err
	}
	f := &filler{p: p, o: FillOptions{Timing: DefaultTiming()}}
	if err := f.ensure(ctx, top.ID); err != nil {
		return nil, err
	}
	if err := f.installHelper(ctx, top.ID); err != nil {
		return nil, err
	}
	raw, err := f.eval(ctx, top.ID, `JSON.stringify(__applysmithEngine.submitObservations(`+jsInt(since.Milliseconds())+`))`)
	if err != nil {
		return nil, err
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, err
	}
	var out []SubmitObservation
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func jsInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// drainJS hands over, once, the events a document's engine has collected, or
// answers null for a document that holds no engine.
const drainJS = `(()=>{const e=globalThis.__applysmithEngine;return e?JSON.stringify(e.drainEvents()):null})()`

// ErrNoEngine is a drain of a tab none of whose documents holds the engine any
// more: the person has left the page the fill worked on.
var ErrNoEngine = errors.New("no document of the tab holds the engine")

// DrainEvents takes the events the engine of every document of the tab (the
// top one and each frame the fill entered) has collected since its fill: what
// the person changed, as manual fills, and scrubbed notes about controls the
// engine never touches. They are handed over once. A document that cannot be
// read keeps its events for a later drain; when none of them can be read the
// error is returned, and ErrNoEngine when none holds the engine.
func (p *Page) DrainEvents(ctx context.Context) ([]LearningEvent, error) {
	frames, err := p.d.Frames(ctx)
	if err != nil {
		return nil, err
	}
	f := &filler{p: p, o: FillOptions{Timing: DefaultTiming()}}
	var out []LearningEvent
	var unread error
	held := false
	for _, fr := range frames {
		raw, err := f.eval(ctx, fr.ID, drainJS)
		if err != nil {
			if unread == nil {
				unread = err
			}
			continue
		}
		var text *string
		if json.Unmarshal(raw, &text) != nil || text == nil {
			continue
		}
		held = true
		var evs []LearningEvent
		if json.Unmarshal([]byte(*text), &evs) == nil {
			out = append(out, evs...)
		}
	}
	switch {
	case held:
		return out, nil
	case unread != nil:
		return nil, unread
	}
	return nil, ErrNoEngine
}
