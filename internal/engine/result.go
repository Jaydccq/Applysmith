package engine

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/browser"
)

// FrameResult is what became of one sub-frame.
type FrameResult struct {
	Frame    browser.Frame
	Decision FrameDecision
	Status   *Status // nil when the frame was not filled or failed
	Err      string
}

// PageResult is the outcome of a page fill. The top frame owns the run's
// result; sub-frame results fold by frame id.
type PageResult struct {
	RunID string
	Stamp string
	// Top is the top frame's terminal status: done, stopped (with a reason) or error.
	Top          Status
	Frames       []FrameResult
	Decisions    []FrameDecision
	Terminals    []FrameNotice
	Reinjections int
	// NativeUploads lists the file inputs the host set natively, as "frame selector".
	NativeUploads []string
	// Notice is the message for the user.
	Notice string
}

func sortFrameResults(fr []FrameResult) {
	slices.SortFunc(fr, func(a, b FrameResult) int { return strings.Compare(a.Frame.ID, b.Frame.ID) })
}

func (r *PageResult) summaries() []*Summary {
	var out []*Summary
	if r.Top.Summary != nil {
		out = append(out, r.Top.Summary)
	}
	for _, f := range r.Frames {
		if f.Status != nil && f.Status.Summary != nil {
			out = append(out, f.Status.Summary)
		}
	}
	return out
}

// message says what happened in words a user can act on: nothing is claimed
// that did not happen, an embedded form is mentioned only when one was found
// and filled, and a frame that could not be reached is the only reason to say
// that part of the form was out of reach (skipped frames never are).
func (r *PageResult) message() string {
	filled, prefilled := 0, 0
	for _, s := range r.summaries() {
		filled += s.Counts.Filled
		prefilled += s.Counts.Prefilled
	}
	var parts []string
	switch {
	case filled > 0:
		parts = append(parts, fmt.Sprintf("Filled %d field(s).", filled))
	case prefilled > 0:
		parts = append(parts, "The form is already filled.")
	default:
		parts = append(parts, "Nothing was filled.")
	}
	embedded, unreached := 0, 0
	for _, d := range r.Decisions {
		if d.Admitted() && (strings.HasPrefix(d.URL, "http://") || strings.HasPrefix(d.URL, "https://")) {
			embedded++
		}
		if d.Failed() {
			unreached++
		}
	}
	if embedded > 0 {
		parts = append(parts, fmt.Sprintf("%d embedded form frame(s) were filled separately.", embedded))
	}
	if unreached > 0 {
		parts = append(parts, fmt.Sprintf("Part of the form could not be reached (%d frame(s)).", unreached))
	}
	return strings.Join(parts, " ")
}

// nativeUploads sets the files the engine could not attach itself. The engine
// reports such a row with the file name; the document it served under that
// name is staged in a private directory for the browser to read, and stays
// there until the tab closes (see Page.Close).
func (f *filler) nativeUploads(ctx context.Context, res *PageResult) {
	apply := func(frame string, s *Summary) {
		if s == nil {
			return
		}
		for i := range s.Rows {
			row := &s.Rows[i]
			if row.Reason != "needs-native-upload" || row.FileName == "" {
				continue
			}
			// A failure is written on the row, so the run says why the slot
			// stayed empty.
			fail := func(what string, err error) {
				row.Detail = fmt.Sprintf("the file was not %s: %v", what, err)
				f.o.logf("native-upload %s frame=%s selector=%s reason=%v", what, frame, row.Selector, err)
			}
			f.rs.mu.Lock()
			doc, ok := f.rs.served[frame][row.FileName]
			f.rs.mu.Unlock()
			if !ok {
				fail("served", fmt.Errorf("no document named %q was served to this frame", row.FileName))
				continue
			}
			data, err := base64.StdEncoding.DecodeString(doc.ContentBase64)
			if err != nil {
				fail("decoded", err)
				continue
			}
			path, err := f.p.stage(f.rs.id, doc.FileName, data)
			if err != nil {
				fail("staged", err)
				continue
			}
			locator := `__applysmithEngine.locate(` + jsString(row.Selector) + `)`
			if err := f.p.d.SetFiles(ctx, frame, World, locator, []string{path}); err != nil {
				fail("set", err)
				continue
			}
			row.Outcome, row.Reason = "filled", ""
			s.Counts.Skipped--
			s.Counts.Filled++
			res.NativeUploads = append(res.NativeUploads, frame+" "+row.Selector)
		}
	}
	apply(f.top, res.Top.Summary)
	for _, fr := range res.Frames {
		if fr.Status != nil {
			apply(fr.Frame.ID, fr.Status.Summary)
		}
	}
}
