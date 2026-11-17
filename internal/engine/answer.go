package engine

import (
	"context"
	"encoding/json"
	"fmt"
)

// InspectRow is one control of the page as the engine reads it back: a
// read-only inventory with the value the page holds now. A control whose value
// the engine withholds (a password, a one-time code) carries none.
// InspectionOnly marks a control the engine never fills: an attachment slot or
// an authentication box, listed so the host sees the page's structure and
// never sends it to the bank. ParserOnly marks an upload that only feeds the
// page's own parser.
type InspectRow struct {
	Selector string   `json:"selector"`
	Label    string   `json:"label"`
	Section  []string `json:"section"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required"`
	Value    string   `json:"value,omitempty"`
	FileName string   `json:"fileName,omitempty"`

	InspectionOnly bool `json:"inspectionOnly,omitempty"`
	ParserOnly     bool `json:"parserOnly,omitempty"`
}

// AnswerResult is what the engine's writer did with one resolution.
type AnswerResult struct {
	Selector    string `json:"selector"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason,omitempty"`
	Value       string `json:"value,omitempty"`
	OptionLabel string `json:"optionLabel,omitempty"`
}

type answerReply struct {
	Results []AnswerResult `json:"results"`
	Handled int            `json:"handled"`
}

// maxAnswerRounds bounds the calls one Answer makes: the engine handles as
// many resolutions as fit its write window and the host sends the rest.
const maxAnswerRounds = 8

// prepare makes sure the top document, or the named sub-frame, holds the current engine build and
// returns the top frame's id. It refuses while a fill owns the page.
func (p *Page) prepare(ctx context.Context, frame string) (*filler, string, error) {
	rs := newRunState("inspect")
	if err := p.begin(rs); err != nil {
		return nil, "", err
	}
	p.end(rs)
	top, err := p.topFrame(ctx)
	if err != nil {
		return nil, "", pageError(err)
	}
	f := &filler{p: p, o: FillOptions{Timing: DefaultTiming()}, top: top.ID}
	target := top.ID
	if frame != "" {
		target = frame
	}
	if err := f.ensure(ctx, target); err != nil {
		return nil, "", pageError(err)
	}
	return f, target, nil
}

// Inspect reads every control of the top document with the value it holds
// now, without changing anything. The check stage compares the result with
// what the fill wrote.
func (p *Page) Inspect(ctx context.Context) ([]InspectRow, error) {
	return p.InspectIn(ctx, "")
}

// InspectIn is Inspect for the document of one frame (an application embedded
// in the page); an empty id is the top document.
func (p *Page) InspectIn(ctx context.Context, frame string) ([]InspectRow, error) {
	f, top, err := p.prepare(ctx, frame)
	if err != nil {
		return nil, err
	}
	raw, err := f.eval(ctx, top, `__applysmithEngine.inspect()`)
	if err != nil {
		return nil, pageError(err)
	}
	var rows []InspectRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("engine inspect: %w", err)
	}
	return rows, nil
}

// Log is the engine's diagnostic log of the last fill in the top document,
// applicant values already masked: what each writer tried and saw.
func (p *Page) Log(ctx context.Context) ([]string, error) {
	return p.LogIn(ctx, "")
}

// LogIn is Log for the document of one frame; an empty id is the top document.
func (p *Page) LogIn(ctx context.Context, frame string) ([]string, error) {
	f, target, err := p.prepare(ctx, frame)
	if err != nil {
		return nil, err
	}
	raw, err := f.eval(ctx, target, `__applysmithEngine.log()`)
	if err != nil {
		return nil, pageError(err)
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return nil, fmt.Errorf("engine log: %w", err)
	}
	return lines, nil
}

// Answer writes resolutions through the engine's own writer, for the run id
// of the fill that read the page (empty skips the staleness check). It never
// calls a model; the caller chose every value. The call is refused while a
// fill is running in the tab.
func (p *Page) Answer(ctx context.Context, runID string, res []Resolution) ([]AnswerResult, error) {
	return p.AnswerIn(ctx, "", runID, res)
}

// AnswerIn is Answer for the document of one frame; an empty id is the top
// document.
func (p *Page) AnswerIn(ctx context.Context, frame, runID string, res []Resolution) ([]AnswerResult, error) {
	f, top, err := p.prepare(ctx, frame)
	if err != nil {
		return nil, err
	}
	var out []AnswerResult
	for round := 0; len(res) > 0 && round < maxAnswerRounds; round++ {
		args, err := json.Marshal(res)
		if err != nil {
			return out, err
		}
		expr := `__applysmithEngine.answer(` + string(args) + `,` + runIDArg(runID) + `)`
		raw, err := f.eval(ctx, top, expr)
		if err != nil {
			// A reply lost after the page may have written ends the run's
			// remaining writers (its restore pass) before the failure is reported.
			if runID != "" && !isReplaced(err) {
				_ = f.stopRun(top, runID)
			}
			return out, pageError(err)
		}
		var rep answerReply
		if err := json.Unmarshal(raw, &rep); err != nil {
			return out, fmt.Errorf("engine answer: %w", err)
		}
		out = append(out, rep.Results...)
		if rep.Handled <= 0 || rep.Handled > len(res) {
			break
		}
		res = res[rep.Handled:]
	}
	return out, nil
}

// HoldInputIn tells the engine of one document (an empty id is the top one)
// that the host itself now drives trusted input there, as the apply chain's
// computer-use rung does (owner decision F-9): until ReleaseInputIn, native
// submissions are held and no event counts as the person's edit. It fails
// while a fill or an answer call owns the page.
func (p *Page) HoldInputIn(ctx context.Context, frame string) error {
	f, top, err := p.prepare(ctx, frame)
	if err != nil {
		return err
	}
	raw, err := f.eval(ctx, top, `__applysmithEngine.holdInput()`)
	if err != nil {
		return pageError(err)
	}
	var held bool
	if json.Unmarshal(raw, &held) != nil || !held {
		return fmt.Errorf("engine hold: a fill or an answer call owns the page")
	}
	return nil
}

// ReleaseInputIn ends the hold. writes are the values the host's input left in
// their controls: a later edit by the person is measured against them.
func (p *Page) ReleaseInputIn(ctx context.Context, frame string, writes []Resolution) error {
	f, top, err := p.prepare(ctx, frame)
	if err != nil {
		return err
	}
	args, err := json.Marshal(writes)
	if err != nil {
		return err
	}
	if writes == nil {
		args = []byte("[]")
	}
	if _, err := f.eval(ctx, top, `__applysmithEngine.releaseInput(`+string(args)+`)`); err != nil {
		return pageError(err)
	}
	return nil
}

func runIDArg(id string) string {
	if id == "" {
		return "undefined"
	}
	return jsString(id)
}

// BlockedSubmit is a native form submission the engine held while it wrote.
type BlockedSubmit struct {
	Form      string `json:"form"`
	Submitter string `json:"submitter"`
}

// BlockedSubmits lists the submissions the engine held in the top document
// since the last fill began. A held submission is a defect to find.
func (p *Page) BlockedSubmits(ctx context.Context) ([]BlockedSubmit, error) {
	return p.BlockedSubmitsIn(ctx, "")
}

// BlockedSubmitsIn is BlockedSubmits for the document of one frame.
func (p *Page) BlockedSubmitsIn(ctx context.Context, frame string) ([]BlockedSubmit, error) {
	f, top, err := p.prepare(ctx, frame)
	if err != nil {
		return nil, err
	}
	raw, err := f.eval(ctx, top, `__applysmithEngine.blockedSubmits()`)
	if err != nil {
		return nil, pageError(err)
	}
	var out []BlockedSubmit
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("engine blocked submits: %w", err)
	}
	return out, nil
}

// SavedFormGaps reads the site's saved copy of the form in the top document
// again and names the required questions it lacks. Only Ashby's driver can
// say; elsewhere the list is empty.
func (p *Page) SavedFormGaps(ctx context.Context) ([]string, error) {
	return p.savedForm(ctx, `__applysmithEngine.refreshSavedFormGaps()`)
}

// RepairSavedForm is Ashby's submit-repair round after a refused submit: a
// Yes/No the saved copy lacks is cleared and pressed again. It returns the
// gaps that remain.
func (p *Page) RepairSavedForm(ctx context.Context) ([]string, error) {
	return p.savedForm(ctx, `__applysmithEngine.repairSavedForm()`)
}

func (p *Page) savedForm(ctx context.Context, expr string) ([]string, error) {
	f, top, err := p.prepare(ctx, "")
	if err != nil {
		return nil, err
	}
	raw, err := f.eval(ctx, top, expr)
	if err != nil {
		return nil, pageError(err)
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("engine saved form: %w", err)
	}
	return out, nil
}
