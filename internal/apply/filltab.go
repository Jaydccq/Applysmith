package apply

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// ErrNoTab is the apply window showing no tab.
var ErrNoTab = errors.New("the apply window has no tab")

// FillDeadline bounds one fill of the active tab, the engine's page deadline.
const FillDeadline = 300 * time.Second

// ActiveWindow is an apply window that can name the tab it shows and take an
// existing tab over for one fill. A tab taken over is the user's: closing the
// fill leaves it open.
type ActiveWindow interface {
	Window
	// ActiveTab names the tab the window shows, or returns ErrNoTab.
	ActiveTab(ctx context.Context) (string, error)
	Adopt(ctx context.Context, id string, h engine.Host) (Tab, error)
}

// FillControl is one control of a fill's result.
type FillControl struct {
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
}

// FillResult is what one fill of the active tab did. It holds labels and
// reasons, never the values written.
type FillResult struct {
	URL              string        `json:"url"`
	Site             string        `json:"site,omitempty"`
	Filled           []FillControl `json:"filled"`
	Review           []FillControl `json:"review"`
	Missing          []FillControl `json:"missing"`
	Skipped          []FillControl `json:"skipped"`
	Drafts           int           `json:"drafts"`
	Flagged          int           `json:"flagged"`
	Events           int           `json:"events"`
	NotAnApplication bool          `json:"notAnApplication,omitempty"`
}

// Fill is one fill of a tab: the page step and the page check, once, with no
// walk, no navigation and no press.
type Fill struct {
	r   *Runner
	e   *Exec
	s   *session
	tab Tab
	// every is how often Follow drains the tab; zero is handoffEvery.
	every time.Duration
}

// OpenFill takes tab id of the window over for one fill.
func (r *Runner) OpenFill(ctx context.Context, win ActiveWindow, id string) (*Fill, error) {
	run := &Run{StartedAt: r.d.Now(), Build: r.d.Build, Outcome: Outcome{Kind: outcomeRunning},
		Input: Input{Caller: gates.CallerDashboard, Tool: defaultTool, ExplicitRequest: true}}
	e := &Exec{r: r, Run: run, Ctx: ctx}
	s := &session{r: r, e: e}
	s.docs = newDocResumes(r, e)
	tab, err := win.Adopt(ctx, id, engine.Host{Matcher: r.matcherFor(run.ID), Resumes: s.docs, StageDir: r.d.Page.StageDir})
	if err != nil {
		return nil, err
	}
	s.tab, s.page = tab, tab.Page()
	e.sess = s
	return &Fill{r: r, e: e, s: s, tab: tab}, nil
}

// Close lets go of the tab without closing it.
func (f *Fill) Close(ctx context.Context) { _ = f.tab.Close(ctx) }

// LooksLikeApplication applies the engine's detection rule to a probe: at
// least two visible data-entry controls, or one the page marks required. The
// engine decides again when it fills; this answers before a run is started.
func LooksLikeApplication(st *PageState) (bool, string) {
	controls, required := 0, 0
	for _, in := range st.Inputs {
		if in.Furniture || in.Disabled {
			continue
		}
		switch strings.ToLower(in.Type) {
		case "hidden", "submit", "reset", "button", "image", "search":
			continue
		}
		controls++
		if in.Required {
			required++
		}
	}
	if controls >= 2 || required >= 1 {
		return true, ""
	}
	if slices.ContainsFunc(st.Frames, func(f FrameInfo) bool { return f.Visible && f.Src != "" }) {
		return true, ""
	}
	return false, fmt.Sprintf("the page shows %d data-entry controls; an application form has at least 2, or one the page marks required", controls)
}

// Probe reads the tab for the check that comes before a run.
func (f *Fill) Probe(ctx context.Context) (*PageState, error) { return Probe(ctx, f.tab) }

// Run fills the tab once as the run runID, an id the server issued. A tab the
// engine reads as not an application ends with NotAnApplication set and
// nothing written.
func (f *Fill) Run(ctx context.Context, runID string) (*FillResult, error) {
	ctx, cancel := context.WithTimeout(ctx, FillDeadline)
	defer cancel()
	f.e.Ctx = ctx
	f.e.Run.ID = runID
	s := f.s
	st, err := Probe(ctx, f.tab)
	if err != nil {
		return nil, fmt.Errorf("the tab could not be read: %w", err)
	}
	f.e.Run.Input.URL = st.URL
	cls, _ := Classify(st.URL)
	s.jd, _ = s.postingText(ctx, st, cls.ATS)
	// A native submission the writes provoke is held, as in a run.
	_ = s.page.ObserveSubmissions(ctx)
	res, err := s.page.Fill(ctx, s.fillOptions(st))
	if err != nil {
		return nil, fillStop(err)
	}
	switch res.Top.State {
	case engine.StateStopped, engine.StateError:
		return nil, fmt.Errorf("fill: %s", firstNonEmpty(res.Top.Reason, res.Top.Message, res.Top.State))
	}
	out := &FillResult{URL: redactURL(st.URL), Filled: []FillControl{}, Review: []FillControl{}, Missing: []FillControl{}, Skipped: []FillControl{}}
	if sum := res.Top.Summary; sum != nil {
		out.Site = sum.Site
		if sum.NotAnApplication {
			out.NotAnApplication = true
			return out, nil
		}
	}
	ps := &pageState{report: PageReport{URL: out.URL, Step: 1}, runID: res.RunID}
	s.pages = append(s.pages, ps)
	s.check(ctx, res, ps)
	s.fix(ctx, res, ps)
	s.collectEvents(res, ps, ps.answered)
	f.collect(out, res, ps)
	if p := f.r.d.Page.Poster; p != nil && len(s.events) > 0 {
		posted, _, _ := PostBatches(context.WithoutCancel(ctx), p, s.events)
		out.Events = posted
	}
	return out, nil
}

// CheckedRow is one control a fill read and checked, without the value it
// holds or was given.
type CheckedRow struct {
	Label, Kind string
	Required    bool
	// Outcome is the engine's outcome for the row; Why is the code of the
	// reason it was not written (reasonCode), empty for a row it wrote.
	Outcome, Why string
	// Options counts the options the engine read for a choice it left (never
	// their labels); zero when the list was never read.
	Options int
}

// Checked is what the page check of Run concluded: every control the fill
// read, without values, and the readiness verdict the check stage gives a walk
// that ended on this page, so its required leftovers count.
func (f *Fill) Checked() ([]CheckedRow, gates.ReadinessResult) {
	var rows []CheckedRow
	for _, ps := range f.s.pages {
		for _, r := range ps.rows {
			c := CheckedRow{Label: r.Label, Kind: r.Kind, Required: r.Required, Outcome: r.Outcome, Options: len(r.Options)}
			if r.Outcome != "filled" && r.Outcome != "prefilled" {
				c.Why = reasonCode(r)
			}
			rows = append(rows, c)
		}
	}
	return rows, f.s.finalVerdict()
}

// collect sorts the page's rows into the four groups of the result.
func (f *Fill) collect(out *FillResult, res *engine.PageResult, ps *pageState) {
	seen := map[string]bool{}
	for _, r := range ps.rows {
		seen[r.Selector] = true
		c := FillControl{Label: r.Label}
		switch {
		case ps.answered[r.Selector] != "":
			out.Filled = append(out.Filled, c)
		case r.Review && (r.Outcome == "filled" || r.Outcome == "prefilled"):
			out.Review = append(out.Review, c)
		case r.Outcome == "filled" || r.Outcome == "prefilled":
			out.Filled = append(out.Filled, c)
		case r.Outcome == "skipped":
			c.Reason = firstNonEmpty(r.Reason, r.MatcherReason)
			out.Skipped = append(out.Skipped, c)
		default:
			c.Reason = reasonOf(r)
			if w := ps.why[r.Selector]; w != "" {
				c.Reason = w
			}
			out.Missing = append(out.Missing, c)
		}
	}
	// A value the write did not leave on the page is missing, whatever the
	// fill reported.
	for _, l := range slices.Concat(ps.notHeld, ps.unreadable) {
		if i := slices.IndexFunc(out.Filled, func(c FillControl) bool { return c.Label == l.Label }); i >= 0 {
			out.Filled = slices.Delete(out.Filled, i, i+1)
		}
		out.Missing = append(out.Missing, FillControl{Label: l.Label, Reason: l.Reason})
	}
	out.Flagged = len(ps.flags)
	out.Drafts = draftCount(res)
}

// draftCount counts the composed drafts the engine held back for review.
func draftCount(res *engine.PageResult) int {
	var sums []*engine.Summary
	if res.Top.Summary != nil {
		sums = append(sums, res.Top.Summary)
	}
	for _, f := range res.Frames {
		if f.Status != nil && f.Status.Summary != nil {
			sums = append(sums, f.Status.Summary)
		}
	}
	n := 0
	for _, sum := range sums {
		var drafts []json.RawMessage
		if json.Unmarshal(sum.Drafts, &drafts) == nil {
			n += len(drafts)
		}
	}
	return n
}

// drainer is a page that hands over what its engines collected after a fill.
type drainer interface {
	DrainEvents(ctx context.Context) ([]engine.LearningEvent, error)
}

// Follow posts, under the fill's run, what the person changes in the tab once
// the fill has left it to them: every handoffEvery it drains the engine of each
// document of the tab and hands the events to the learning service, the bounds
// the hand-off recorder of a kept tab works within. It returns why it ended and
// how many events it posted: the tab closed ("tab-closed"), no document holds
// the engine any more because the person left the page ("left"), stop closed,
// after one last drain ("stopped"), ctx ended ("cancelled"), handoffLifetime
// passed ("expired"), or handoffFailures drains in a row failed ("failures").
// It holds no lease and writes nothing to the page: the tab stays the
// person's to use.
func (f *Fill) Follow(ctx context.Context, stop <-chan struct{}) (string, int) {
	d, ok := f.s.page.(drainer)
	if !ok || f.r.d.Page.Poster == nil {
		return "unsupported", 0
	}
	every := f.every
	if every <= 0 {
		every = handoffEvery
	}
	deadline := f.r.d.Now().Add(handoffLifetime)
	tick := time.NewTicker(every)
	defer tick.Stop()
	w := &follower{f: f, d: d, names: entryNames{writes: f.s.bankWrites()}}
	for {
		why := ""
		select {
		case <-ctx.Done():
			return "cancelled", w.posted
		case <-stop:
			why = cmp.Or(w.drain(ctx), "stopped")
		case <-tick.C:
			why = w.drain(ctx)
			if why == "" && !f.r.d.Now().Before(deadline) {
				why = "expired"
			}
		}
		if why != "" {
			w.post(ctx)
			return why, w.posted
		}
	}
}

// follower is the state of one Follow.
type follower struct {
	f *Fill
	d drainer
	// held are events drained and not yet delivered: the receiver is
	// idempotent by event id, so a failed post is simply posted again.
	held   []LearnEvent
	fails  int
	posted int
	// names are the entries behind the values the fill wrote from the bank.
	names entryNames
}

// drain takes what the engines collected and posts it with whatever an earlier
// post did not deliver. It names why the follow ends, or returns "".
func (w *follower) drain(ctx context.Context) string {
	evs, err := w.d.DrainEvents(ctx)
	switch {
	case errors.Is(err, engine.ErrNoEngine):
		return "left"
	case isTabClosed(err):
		return "tab-closed"
	case err != nil:
		if w.fails++; w.fails >= handoffFailures {
			return "failures"
		}
		return ""
	}
	w.fails = 0
	for _, ev := range evs {
		le := LearnEvent{LearningEvent: capOptions(ev), RunID: w.f.e.Run.ID}
		le.EntryID = w.names.entryOf(le.LearningEvent, func(b bankWrite) bool { return b.selector == ev.Selector })
		w.held = append(w.held, le)
	}
	w.post(ctx)
	return ""
}

func (w *follower) post(ctx context.Context) {
	if len(w.held) == 0 {
		return
	}
	n, _, err := PostBatches(context.WithoutCancel(ctx), w.f.r.d.Page.Poster, w.held)
	if err != nil {
		w.held = w.held[max(0, len(w.held)-maxEventBatch):]
		return
	}
	w.posted += n
	w.held = nil
}
