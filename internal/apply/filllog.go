package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"applysmith/internal/engine"
)

// maxFillLog bounds the debug lines a run keeps; a stop snapshot is for a
// person reading why a page went wrong, not an archive.
const maxFillLog = 4000

// fillLine is one debug line of the page filler. It names controls and what
// happened to them and never holds a value the applicant gave.
type fillLine struct {
	Event    string `json:"event"`
	Page     int    `json:"page"`
	Pass     int    `json:"pass,omitempty"`
	Frame    string `json:"frame,omitempty"` // the document an engine line is from; empty for the top one
	Selector string `json:"selector,omitempty"`
	Label    string `json:"label,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// fillLog collects the filler's per-page lines of one run.
type fillLog struct {
	mu    sync.Mutex
	lines []fillLine
	page  int
	pass  int
	// frames are the frame documents the fills reached, in first-seen order.
	frames []string
}

func (l *fillLog) sawFrame(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.Contains(l.frames, id) {
		l.frames = append(l.frames, id)
	}
}

func (l *fillLog) frameIDs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.frames)
}

func (l *fillLog) add(ls ...fillLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range ls {
		if len(l.lines) < maxFillLog {
			l.lines = append(l.lines, x)
		}
	}
}

// engineLogger is a filler that reads the engine's own log of its last fill
// in a document: the top one for an empty frame id.
type engineLogger interface {
	LogIn(ctx context.Context, frame string) ([]string, error)
}

// writerMisses are the reasons a writer gives when it could not commit a
// value, and a write that ran out of its budget. The row never says what the
// writer searched and saw (a catalog's labels) or where it spent its time; the
// engine's log of that document does, and only the stop page's top log is
// otherwise kept.
var writerMisses = map[string]bool{"option-not-found": true, "listbox-not-opened": true, "write-not-committed": true, "verify-mismatch": true, "timeout": true}

// loggedFiller records what the engine reported for each fill and for each
// write the chain made afterwards (a refill pass per Answer call), and the
// engine's log of a page where a writer missed.
type loggedFiller struct {
	Filler
	log *fillLog
}

func (f *loggedFiller) Fill(ctx context.Context, o engine.FillOptions) (*engine.PageResult, error) {
	f.log.mu.Lock()
	f.log.page++
	f.log.pass = 0
	page := f.log.page
	f.log.mu.Unlock()
	// The host side's lines (frame decisions, a frame given up and why) are
	// kept for every pass: the engine's own log holds only the top document's
	// last fill.
	logf := o.Logf
	o.Logf = func(format string, args ...any) {
		f.log.add(fillLine{Event: "host", Page: page, Reason: fmt.Sprintf(format, args...)})
		if logf != nil {
			logf(format, args...)
		}
	}
	res, err := f.Filler.Fill(ctx, o)
	if err != nil {
		code, _ := engine.CodeOf(err)
		f.log.add(fillLine{Event: "fill-error", Page: page, Reason: string(code)})
		return res, err
	}
	f.log.add(fillLine{Event: "fill", Page: page, Outcome: res.Top.State, Reason: res.Top.Reason})
	type doc struct {
		frame string // empty for the top document
		sum   *engine.Summary
	}
	docs := []doc{{"", res.Top.Summary}}
	for _, fr := range res.Frames {
		if fr.Status != nil {
			docs = append(docs, doc{fr.Frame.ID, fr.Status.Summary})
		}
	}
	lg, logs := f.Filler.(engineLogger)
	for _, d := range docs {
		if d.frame != "" {
			f.log.sawFrame(d.frame)
		}
		if d.sum == nil {
			continue
		}
		missed := false
		for _, r := range d.sum.Rows {
			f.log.add(fillLine{Event: "row", Page: page, Selector: r.Selector, Label: r.Label, Kind: r.Kind, Outcome: r.Outcome, Reason: withDetail(r)})
			missed = missed || writerMisses[r.Reason]
		}
		if !logs || !missed {
			continue
		}
		// The log is the document's whose writer missed: a frame's own, not the top's.
		if lines, err := lg.LogIn(ctx, d.frame); err == nil {
			for _, l := range lines {
				f.log.add(fillLine{Event: "engine", Page: page, Frame: d.frame, Reason: l})
			}
		}
	}
	return res, nil
}

// fillRows are the rows a fill read in the top document and every frame.
func fillRows(res *engine.PageResult) []engine.Row {
	sums := []*engine.Summary{res.Top.Summary}
	for _, fr := range res.Frames {
		if fr.Status != nil {
			sums = append(sums, fr.Status.Summary)
		}
	}
	var rows []engine.Row
	for _, sum := range sums {
		if sum != nil {
			rows = append(rows, sum.Rows...)
		}
	}
	return rows
}

// readNoRow reports a fill that read no row in the top document or any frame:
// a layer hid the form from it. A form in a frame (Fivetran's careers page
// around its Greenhouse form) was read.
func readNoRow(res *engine.PageResult) bool {
	return res != nil && res.Top.Summary != nil && len(fillRows(res)) == 0
}

// writeMissed reports a fill in which a writer could not commit a value, in
// the top document or a frame.
func writeMissed(res *engine.PageResult) bool {
	if res == nil {
		return false
	}
	for _, r := range fillRows(res) {
		if r.Outcome == "failed" && writerMisses[r.Reason] {
			return true
		}
	}
	return false
}

// frameLogs is the engine's log of every frame document the fills reached,
// each under a line naming the frame. A value the page took back after its
// writer reported it (MongoDB's location search in the Greenhouse frame) is
// diagnosed only from the frame's own log, which no writer miss kept.
func (f *loggedFiller) frameLogs(ctx context.Context) []string {
	lg, ok := f.Filler.(engineLogger)
	if !ok {
		return nil
	}
	var out []string
	for _, id := range f.log.frameIDs() {
		if lines, err := lg.LogIn(ctx, id); err == nil && len(lines) > 0 {
			out = append(append(out, "== frame "+id), lines...)
		}
	}
	return out
}

func (f *loggedFiller) Answer(ctx context.Context, runID string, res []engine.Resolution) ([]engine.AnswerResult, error) {
	got, err := f.Filler.Answer(ctx, runID, res)
	return f.logAnswer(got, err)
}

func (f *loggedFiller) logAnswer(got []engine.AnswerResult, err error) ([]engine.AnswerResult, error) {
	f.log.mu.Lock()
	f.log.pass++
	page, pass := f.log.page, f.log.pass
	f.log.mu.Unlock()
	if err != nil {
		f.log.add(fillLine{Event: "answer-error", Page: page, Pass: pass})
		return got, err
	}
	for _, g := range got {
		f.log.add(fillLine{Event: "write", Page: page, Pass: pass, Selector: g.Selector, Outcome: g.Outcome, Reason: g.Reason})
	}
	return got, nil
}

// The frame variants pass through to the engine's page, so a logged filler is
// still a FrameFiller.
func (f *loggedFiller) inner() (FrameFiller, error) {
	ff, ok := f.Filler.(FrameFiller)
	if !ok {
		return nil, errNoFrameFiller
	}
	return ff, nil
}

func (f *loggedFiller) InspectIn(ctx context.Context, frame string) ([]engine.InspectRow, error) {
	ff, err := f.inner()
	if err != nil {
		return nil, err
	}
	return ff.InspectIn(ctx, frame)
}

func (f *loggedFiller) AnswerIn(ctx context.Context, frame, runID string, res []engine.Resolution) ([]engine.AnswerResult, error) {
	ff, err := f.inner()
	if err != nil {
		return nil, err
	}
	got, aerr := ff.AnswerIn(ctx, frame, runID, res)
	return f.logAnswer(got, aerr)
}

func (f *loggedFiller) BlockedSubmitsIn(ctx context.Context, frame string) ([]engine.BlockedSubmit, error) {
	ff, err := f.inner()
	if err != nil {
		return nil, err
	}
	return ff.BlockedSubmitsIn(ctx, frame)
}

// write saves the log as JSON lines next to a snapshot, privately.
func (l *fillLog) write(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return
	}
	var out []byte
	for _, x := range l.lines {
		b, err := json.Marshal(x)
		if err != nil {
			continue
		}
		out = append(append(out, b...), '\n')
	}
	_ = os.WriteFile(filepath.Clean(path), out, 0o600)
}

// withDetail is a row's reason followed by what the row says about it (the
// site's refusal, or why a file was not set).
func withDetail(r engine.Row) string {
	if r.Detail == "" {
		return r.Reason
	}
	return r.Reason + ": " + r.Detail
}
