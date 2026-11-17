package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"applysmith/internal/browser"
)

// FillOptions describes one page fill.
type FillOptions struct {
	// Applicant reads the applicant snapshot (GET /api/autofill/applicant).
	Applicant               func(ctx context.Context) (json.RawMessage, error)
	Posting                 PostingContext
	AcceptLegalDeclarations bool
	// Edited names, by document and selector, the controls the user changed
	// by hand before this fill: a frame's document by the frame's id, the top
	// document by "". The engine leaves them as they are, empty or not, in
	// every document it fills.
	Edited map[string][]string
	// MainWorld installs the main-world helper in every frame the fill enters.
	// Sites whose drivers need the page's own world (a save queue, an uploader
	// that overwrites assigned files, framework handlers) depend on it.
	MainWorld bool
	Timing    Timing
	// Logf receives the diagnostic lines of the host side, such as frame decisions.
	Logf func(format string, args ...any)
	// OnUnreached is called once for each frame that could not be reached.
	OnUnreached func(FrameDecision)
}

func (o *FillOptions) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// fetchApplicant reads the applicant snapshot, three attempts apart.
func fetchApplicant(ctx context.Context, o FillOptions) (json.RawMessage, error) {
	if o.Applicant == nil {
		return nil, newError(CodeApplicantUnavailable, nil, "no applicant source is configured")
	}
	var last error
	for i := range ApplicantAttempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(o.Timing.ApplicantRetry):
			}
		}
		raw, err := o.Applicant(ctx)
		if err == nil {
			var probe struct {
				Candidate json.RawMessage `json:"candidate"`
			}
			if json.Unmarshal(raw, &probe) == nil && len(probe.Candidate) > 0 {
				return raw, nil
			}
			err = errors.New("the snapshot has no candidate")
		}
		last = err
	}
	return nil, newError(CodeApplicantUnavailable, last, "the applicant data could not be read, so nothing was filled")
}

// Fill fills the tab's form: the engine runs in the top frame, which owns the
// result, and in every sub-frame the walk admits. A navigation that replaces
// the document restarts the fill, at most MaxReinjections times.
func (p *Page) Fill(ctx context.Context, o FillOptions) (*PageResult, error) {
	o.Timing = o.Timing.withDefaults()
	applicant, err := fetchApplicant(ctx, o)
	if err != nil {
		return nil, err
	}
	// The page deadline is counted from the start of the fill, not of each
	// reinjection, so a page that keeps replacing its document cannot extend it.
	deadline := time.Now().Add(o.Timing.PageDeadline)
	// The first attempt pins the top document's origin; later attempts, which
	// follow a replaced document, only start where the tab still is. The test
	// is the origin (scheme, host and port), not the site: a move between two
	// hosts of one company is a move the fill was not asked to follow.
	topOrigin := ""
	reinjections := 0
	for {
		res, err := p.attempt(ctx, o, applicant, deadline, &topOrigin)
		if err == nil {
			res.Reinjections = reinjections
			return res, nil
		}
		if !isReplaced(err) {
			return nil, pageError(err)
		}
		if reinjections >= MaxReinjections {
			return nil, newError(CodeNavigationLimit, nil, "the page replaced its document %d times during the fill", reinjections+1)
		}
		reinjections++
		o.logf("reinject attempt=%d", reinjections)
		top, terr := p.topFrame(ctx)
		if terr != nil {
			return nil, pageError(terr)
		}
		(&filler{p: p, o: o}).waitDocument(ctx, top.ID)
	}
}

func (p *Page) topFrame(ctx context.Context) (browser.Frame, error) {
	frames, err := p.d.Frames(ctx)
	if err != nil {
		return browser.Frame{}, err
	}
	for _, f := range frames {
		if f.IsTop {
			return f, nil
		}
	}
	return browser.Frame{}, newError(CodeInjectFailed, nil, "the tab has no top frame")
}

// attempt is one fill of the current document.
func (p *Page) attempt(ctx context.Context, o FillOptions, applicant json.RawMessage, deadline time.Time, topOrigin *string) (*PageResult, error) {
	top, err := p.topFrame(ctx)
	if err != nil {
		return nil, err
	}
	rs := newRunState(newRunID())
	if err := p.begin(rs); err != nil {
		return nil, err
	}
	defer p.end(rs)
	f := &filler{p: p, o: o, rs: rs, applicant: applicant, top: top.ID}
	if *topOrigin == "" {
		raw, err := f.eval(ctx, top.ID, `location.origin`)
		if err != nil {
			return nil, err
		}
		var origin string
		_ = json.Unmarshal(raw, &origin)
		if !pinnable(origin) {
			return nil, newError(CodeInjectFailed, nil, "the page has no origin to pin, so the applicant data is not handed to it")
		}
		*topOrigin = origin
	}
	rs.admit(top.ID, *topOrigin)

	if err := f.ensureFrame(ctx, top.ID); err != nil {
		return nil, err
	}
	if err := f.start(ctx, top.ID, *topOrigin, deadline); err != nil {
		return nil, err
	}
	w := newWalker(f, top, deadline)
	defer w.abandon()
	st, err := w.followTop(ctx)
	if err != nil {
		return nil, err
	}
	if err := w.finish(ctx); err != nil {
		return nil, err
	}
	w.awaitTerminals(ctx)
	res := w.result(st)
	f.frameReport(ctx, res)
	f.nativeUploads(ctx, res)
	res.Notice = res.message()
	return res, nil
}

// frameReport hands the walk's frame decisions to the top frame's engine,
// which writes the roll-up and one line per frame not reached into its fill
// log and returns one learning event per frame that could not be entered.
// Those events join the top frame's summary. A page with no sub-frame has
// nothing to report, and a report that fails is logged and costs the fill
// nothing.
func (f *filler) frameReport(ctx context.Context, res *PageResult) {
	if len(res.Decisions) == 0 {
		return
	}
	b, _ := json.Marshal(res.Decisions)
	raw, err := f.eval(ctx, f.top, `__applysmithEngine.frameReport(`+string(b)+`)`)
	var events []LearningEvent
	if err == nil {
		err = json.Unmarshal(raw, &events)
	}
	if err != nil {
		f.o.logf("frame-report failed reason=%v", err)
		return
	}
	if res.Top.Summary != nil {
		res.Top.Summary.Events = append(res.Top.Summary.Events, events...)
	}
}
