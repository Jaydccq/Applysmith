package apply

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/engine"
)

// maxSubmitPresses is the most Submit presses one run may make: the first, and
// at most one more at any of the documented second-press points (the security
// code, the second captcha evidence point, the repair round).
const maxSubmitPresses = 2

// Board reason of a second refusal: the user finishes the form.
const boardNeedsUser = "needs-user"

// repairTarget is a field the refusal names and this run wrote.
type repairTarget struct {
	row     engine.Row
	written string
}

// namedFields maps the page's validation messages to the fields this run wrote:
// a message names a field when it carries the field's label. Identity,
// self-identification, attestation and signature rows are never returned, and a
// message that names a field the run did not write names nothing to repair.
func (s *session) namedFields(validation []string) []repairTarget {
	legalOn := s.r.d.Page.Settings != nil && s.r.d.Page.Settings().AutoAcceptLegalDeclarations
	var out []repairTarget
	seen := map[string]bool{}
	for i := len(s.pages) - 1; i >= 0; i-- {
		for _, r := range s.pages[i].rows {
			written := firstNonEmpty(r.OptionLabel, r.Value)
			if r.Outcome != "filled" || written == "" || seen[r.Selector] {
				continue
			}
			label := labelKey(r.Label)
			if len([]rune(label)) < 3 || !messagesName(validation, label) {
				continue
			}
			if _, excluded := ladderExclusion(r, nil, legalOn); excluded {
				continue
			}
			seen[r.Selector] = true
			out = append(out, repairTarget{row: r, written: written})
		}
	}
	return out
}

// recordRefusal records what a refused Submit names, the questions the
// application service listed as missing and the rows the page's validation
// words name, as needs-user leftovers of the submit page with an entry draft,
// so the run record offers them to answer. The draft takes the kind and
// options of the walk's row with that label; a question no row carries is a
// text question. A question already left is not added twice.
func (s *session) recordRefusal(missing, validation []string) {
	if len(s.pages) == 0 {
		return
	}
	ps := s.pages[len(s.pages)-1]
	var rep *PageReport
	if n := len(s.e.Run.Pages); n > 0 {
		rep = &s.e.Run.Pages[n-1]
	}
	seen := map[string]bool{}
	for _, l := range ps.leftovers {
		seen[labelKey(l.Label)] = true
	}
	add := func(l Leftover) {
		k := labelKey(l.Label)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		u := l.Label + " (named by the refused submit)"
		ps.leftovers, ps.report.Unresolved = append(ps.leftovers, l), append(ps.report.Unresolved, u)
		if rep != nil {
			rep.Leftovers, rep.Unresolved = append(rep.Leftovers, l), append(rep.Unresolved, u)
		}
	}
	want := map[string]bool{}
	for _, q := range missing {
		want[labelKey(q)] = true
	}
	for i := len(s.pages) - 1; i >= 0; i-- {
		p := s.pages[i]
		for j, r := range p.rows {
			k := labelKey(r.Label)
			if len([]rune(k)) < 3 || !want[k] && !messagesName(validation, k) {
				continue
			}
			delete(want, k)
			add(Leftover{Selector: r.Selector, Label: r.Label, Reason: ReasonNeedsUser, Detail: "named by the refused submit", Draft: s.draftOf(p.runID, j, r)})
		}
	}
	for _, q := range missing {
		if want[labelKey(q)] {
			add(Leftover{Label: q, Reason: ReasonNeedsUser, Detail: "named by the refused submit as a missing required entry", Draft: &EntryDraft{Pattern: normText(q), Kind: "text"}})
		}
	}
}

// labelKey is a label as validation words quote it.
func labelKey(l string) string {
	return normText(strings.Trim(strings.TrimSpace(dropRequired(l)), "*: "))
}

func dropRequired(l string) string { return strings.ReplaceAll(l, "(required)", "") }

func messagesName(validation []string, label string) bool {
	for _, v := range validation {
		if strings.Contains(normText(v), label) {
			return true
		}
	}
	return false
}

// repairRound is the one repair after a refused Submit. It runs only for a
// validation refusal on the form that is still open, re-writes the fields the
// refusal names that this run wrote, re-reads them and presses Submit once more
// on the claim the run already holds. It reports whether Submit was pressed
// again and the note for the stage line; with nothing to repair it presses
// nothing.
func (r *Runner) repairRound(ctx context.Context, e *Exec, s *session, evidence []string, again func(context.Context) *stop) (pressed bool, note string) {
	if e.Run.Repaired != "" {
		return false, ""
	}
	targets := s.namedFields(evidence)
	if len(targets) == 0 {
		return false, "repair round: fields=none result=nothing the run wrote is named"
	}
	var writes []engine.Resolution
	var names []string
	for _, t := range targets {
		writes = append(writes, engine.Resolution{Selector: t.row.Selector, Outcome: "filled", Value: t.row.Value, OptionLabel: t.row.OptionLabel})
		names = append(names, t.row.Label)
	}
	fields := strings.Join(names, ",")
	e.Run.Repaired = "fields=" + fields
	if _, err := s.answer(ctx, s.lastRunID(), writes); err != nil {
		return false, fmt.Sprintf("repair round: fields=%s result=rewrite failed", fields)
	}
	held := map[string]engine.InspectRow{}
	if rows, err := s.inspect(ctx); err == nil {
		held = indexRows(rows)
	}
	ok := 0
	for _, t := range targets {
		cur, found := held[t.row.Selector]
		if !found || verify(t.written, cur.Value) == writeKept {
			ok++
		}
	}
	if ok == 0 {
		return false, fmt.Sprintf("repair round: fields=%s result=not held after the rewrite", fields)
	}
	if st := again(ctx); st != nil {
		return false, fmt.Sprintf("repair round: fields=%s result=%s", fields, st.reason)
	}
	return true, fmt.Sprintf("repair round: fields=%s", fields)
}

// lastRunID is the engine run of the last page filled; the writer needs it.
func (s *session) lastRunID() string {
	for i := len(s.pages) - 1; i >= 0; i-- {
		if s.pages[i].runID != "" {
			return s.pages[i].runID
		}
	}
	return ""
}

// savedForm is Ashby's saved copy of the form, as the engine's driver reads
// it: the required questions it lacks, and the repair round that presses a
// lost Yes/No again.
type savedForm interface {
	SavedFormGaps(ctx context.Context) ([]string, error)
	RepairSavedForm(ctx context.Context) ([]string, error)
}

var _ savedForm = (*engine.Page)(nil)

// savedFormOf is the saved-form surface behind the page, when it has one. A
// run's page is wrapped by the fill log, which forwards fills only.
func savedFormOf(p Filler) (savedForm, bool) {
	if lf, ok := p.(*loggedFiller); ok {
		p = lf.Filler
	}
	sf, ok := p.(savedForm)
	return sf, ok
}

// ReasonSavedFormGap is a required question Ashby's saved copy of the form
// holds no answer for: Submit is validated against that copy, not the page.
const ReasonSavedFormGap = "ashby-unanswered"

// savedFormGaps adds a required leftover for each question Ashby's saved copy
// lacks that the page's own check did not already name (a datepicker that
// shows text it could not parse reads as filled).
func (s *session) savedFormGaps(ctx context.Context, ps *pageState) {
	sf, ok := savedFormOf(s.page)
	if s.e.Run.ATS != ATSAshby || !ok {
		return
	}
	gaps, err := sf.SavedFormGaps(ctx)
	if err != nil {
		return
	}
	for _, g := range gaps {
		key := labelKey(g)
		if slices.ContainsFunc(ps.leftovers, func(l Leftover) bool { return labelKey(l.Label) == key }) {
			continue
		}
		sel := ""
		for _, r := range ps.rows {
			if labelKey(r.Label) == key || strings.HasPrefix(labelKey(r.Label), key+" ") {
				sel = r.Selector
				break
			}
		}
		ps.leftovers = append(ps.leftovers, Leftover{Selector: sel, Label: g, Reason: ReasonSavedFormGap, Detail: "Ashby's saved copy of the form holds no answer"})
	}
}

// repairSaved runs Ashby's saved-form repair after a bounced submit and
// reports whether it closed every gap the saved copy had. No gap before the
// repair is no claim (the copy was never read), so it reports false.
func (s *session) repairSaved(ctx context.Context) bool {
	sf, ok := savedFormOf(s.page)
	if s.e.Run.ATS != ATSAshby || !ok {
		return false
	}
	if before, err := sf.SavedFormGaps(ctx); err != nil || len(before) == 0 {
		return false
	}
	gaps, err := sf.RepairSavedForm(ctx)
	return err == nil && len(gaps) == 0
}
