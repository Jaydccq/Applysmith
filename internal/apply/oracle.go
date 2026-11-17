package apply

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// Oracle Recruiting Cloud's Work and Education History sections are known by
// the timeline add buttons. An add button opens an inline editor; the walk
// fills its fields, found by stable field name (the ids carry volatile
// suffixes): it types into the plain boxes and drives the pickers and the
// Current Job box through the engine's own writers, and presses the editor's footer
// button (class save-btn: "Add Experience" or "Add Education") through the
// gate, because the engine never presses it. The month and year pickers, Major
// and School are grid comboboxes the engine types into; School takes its exact
// row only. A row whose employer or job title is already on the timeline is
// skipped, and a profile row with no employer is skipped. The Degree pill is
// clicked last, because a later commit resets it.

var suffixRE = lazyregexp.New(`[-_0-9]+$`)

// fieldRoots are a control's stable names: its name and its id, lowercase,
// without the volatile numeric suffix.
func fieldRoots(in InputInfo) []string {
	var out []string
	for _, n := range []string{in.Name, in.ID} {
		if r := strings.ToLower(suffixRE.ReplaceAllString(strings.TrimSpace(n), "")); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// monthYear splits an ISO date (YYYY-MM, with or without a day) into the
// month name and the year the editor's pickers list.
func monthYear(iso string) (month, year string) {
	if len(iso) < 7 {
		return "", ""
	}
	m, err := strconv.Atoi(iso[5:7])
	if err != nil || m < 1 || m > 12 {
		return "", ""
	}
	return time.Month(m).String(), iso[:4]
}

func onTimeline(items []string, parts ...string) bool {
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		for _, it := range items {
			if strings.Contains(strings.ToLower(it), p) {
				return true
			}
		}
	}
	return false
}

// oracleHistory adds the applicant's history rows that the timelines lack.
// It reports what it did in the page report's notes; a row it cannot add is
// noted and left, never guessed.
func (s *session) oracleHistory(ctx context.Context, st *PageState, ps *pageState) {
	if st.Oracle == nil {
		return
	}
	a, ok := s.r.applicant(ctx)
	if !ok {
		return
	}
	note := func(f string, args ...any) { ps.report.Notes = append(ps.report.Notes, fmt.Sprintf(f, args...)) }
	cur := st
	for _, j := range a.History.Employment {
		if strings.TrimSpace(j.Employer) == "" {
			note("oracle: a history row with no employer was skipped")
			continue
		}
		if cur.Oracle == nil || cur.Oracle.AddExperience < 0 || onTimeline(cur.Oracle.Experience, j.Employer, j.Title) {
			continue
		}
		if stp := s.oracleEditor(ctx, cur.Oracle.AddExperience, "employername", func(ins []InputInfo, c []Control) []step {
			return experienceSteps(ins, c, j)
		}); stp != nil {
			note("oracle: the work history row %q was not added: %s", j.Employer, stp.reason)
			return
		}
		note("oracle: work history row added and saved")
		if next, err := Probe(ctx, s.tab); err == nil {
			cur = next
		}
	}
	for _, e := range a.History.Education {
		if strings.TrimSpace(e.School) == "" {
			continue
		}
		if cur.Oracle == nil || cur.Oracle.AddEducation < 0 || onTimeline(cur.Oracle.Education, e.School) {
			continue
		}
		if stp := s.oracleEditor(ctx, cur.Oracle.AddEducation, "educationalestablishment", func(ins []InputInfo, c []Control) []step {
			return educationSteps(ins, c, e)
		}); stp != nil {
			note("oracle: the education row %q was not added: %s", e.School, stp.reason)
			return
		}
		note("oracle: education row added and saved")
		if next, err := Probe(ctx, s.tab); err == nil {
			cur = next
		}
	}
}

// step is one action in an editor. It names its target instead of holding a
// probe handle, because handles are only good until the next probe and an
// editor is probed between steps.
type step struct {
	roots []string // the field-name roots of the control the engine writes
	value string
	pill  *lazyregexp.Regexp // the label of a pill to click
}

func find(ins []InputInfo, roots ...string) *InputInfo {
	for i := range ins {
		for _, r := range fieldRoots(ins[i]) {
			if slices.Contains(roots, r) {
				return &ins[i]
			}
		}
	}
	return nil
}

func pill(c []Control, label *lazyregexp.Regexp) *Control {
	for i := range c {
		if c[i].Role == "option" || c[i].Pressed == "" {
			continue
		}
		for _, l := range c[i].Labels {
			if label.MatchString(l) {
				return &c[i]
			}
		}
	}
	return nil
}

// plan reads the editor once to learn which steps apply to its fields.
type plan struct {
	steps []step
}

func (p *plan) write(ins []InputInfo, value string, roots ...string) {
	if value = strings.TrimSpace(value); value != "" && find(ins, roots...) != nil {
		p.steps = append(p.steps, step{roots: roots, value: value})
	}
}

func (p *plan) dates(ins []InputInfo, edge, iso string) {
	m, y := monthYear(iso)
	p.write(ins, m, "month-"+edge+"date")
	p.write(ins, y, "year-"+edge+"date")
}

// The Internal or External pill is chosen by keyword: its text names the
// tenant ("External (Outside Example Co)").
var externalPill = lazyregexp.New(`(?i)^\s*external\b`)

func experienceSteps(ins []InputInfo, c []Control, j wire.ApplicantJob) []step {
	var p plan
	p.write(ins, j.Employer, "employername")
	p.write(ins, j.Title, "jobtitle")
	p.write(ins, j.Summary, "responsibilities")
	p.dates(ins, "start", j.StartDate)
	if j.Current {
		p.write(ins, "Yes", "currentjobflag", "af-checkbox-currentjobflag")
	} else {
		p.dates(ins, "end", j.EndDate)
	}
	if pill(c, externalPill) != nil {
		p.steps = append(p.steps, step{pill: externalPill})
	}
	return p.steps
}

// oracleDegreePills are the Degree pills, each with the profile degrees that
// name it.
var oracleDegreePills = []struct {
	re   *lazyregexp.Regexp
	pill string
}{
	{lazyregexp.New(`\b(ph\.?d|doctor|doctoral|d\.?phil)\b`), "Doctoral Degree"},
	{lazyregexp.New(`\b(master|m\.?s|m\.?a|m\.?eng|mba|m\.?b\.?a|graduate)\b`), "Master's Degree"},
	{lazyregexp.New(`\b(bachelor|b\.?s|b\.?a|b\.?eng|undergrad(uate)?)\b`), "Bachelor's Degree"},
	{lazyregexp.New(`\bassociate\b`), "Associate Degree"},
	{lazyregexp.New(`\bged\b`), "GED"},
	{lazyregexp.New(`\b(high\s*school|secondary\s+school|h\.?s\.?\s*diploma)\b`), "High School Graduate"},
	{lazyregexp.New(`\b(some\s+college|college\s+coursework)\b`), "Some College"},
	{lazyregexp.New(`\b(technical|vocational|trade\s+school)\b`), "Technical/Vocational"},
	{lazyregexp.New(`\bminor\b`), "Minor"},
}

// oracleDegree is the Degree pill a profile degree names; Unknown is one of
// Oracle's own pills.
func oracleDegree(degree string) string {
	d := strings.ToLower(strings.TrimSpace(degree))
	for _, k := range oracleDegreePills {
		if d != "" && k.re.MatchString(d) {
			return k.pill
		}
	}
	return "Unknown"
}

func educationSteps(ins []InputInfo, c []Control, e wire.ApplicantStudy) []step {
	var p plan
	p.write(ins, e.Field, "major")
	p.write(ins, e.School, "educationalestablishment")
	p.dates(ins, "start", e.StartDate)
	p.dates(ins, "end", e.EndDate)
	p.write(ins, e.Field, "areaofstudy")
	// The Degree pill is clicked last: a later commit resets it.
	degree := lazyregexp.New(`(?i)^\s*` + regexp.QuoteMeta(oracleDegree(e.Degree)) + `\s*$`)
	if pill(c, degree) != nil {
		p.steps = append(p.steps, step{pill: degree})
	}
	return p.steps
}

// editorOpen reports whether an editor with the anchor field shows its footer
// Save.
func editorOpen(st *PageState, anchor string) bool {
	return st.Oracle != nil && st.Oracle.Save >= 0 && find(st.Inputs, anchor) != nil
}

// oracleEditor opens the editor behind a timeline add button, waits for its
// anchor field, runs the steps the plan produces and presses the editor's
// footer Save through the gate.
func (s *session) oracleEditor(ctx context.Context, add int, anchor string, mkPlan func([]InputInfo, []Control) []step) (res *stop) {
	tm := s.r.d.Page.Timing.withDefaults()
	defer func() {
		// An editor the walk could not finish is closed with its own Cancel, so
		// it does not cover the rest of the page.
		if res != nil {
			if cur, err := Probe(ctx, s.tab); err == nil && cur.Oracle != nil && cur.Oracle.Cancel >= 0 {
				_ = s.clickChoice(ctx, cur.Oracle.Cancel)
			}
		}
	}()
	if stp := s.pressEditorControl(ctx, add); stp != nil {
		return stp
	}
	var st *PageState
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cur, err := Probe(ctx, s.tab); err == nil && editorOpen(cur, anchor) {
			st = cur
			break
		}
		time.Sleep(tm.Poll / 5)
	}
	if st == nil {
		return &stop{class: ClassDefect, reason: "the editor did not open"}
	}
	for _, sp := range mkPlan(st.Inputs, st.Controls) {
		cur, err := Probe(ctx, s.tab)
		if err != nil {
			return &stop{class: ClassEnvironment, reason: err.Error()}
		}
		if sp.pill != nil {
			p := pill(cur.Controls, sp.pill)
			if p == nil {
				return &stop{class: ClassDefect, reason: "an editor choice disappeared"}
			}
			if stp := s.clickChoice(ctx, p.I); stp != nil {
				return stp
			}
			continue
		}
		in := find(cur.Inputs, sp.roots...)
		if in == nil {
			return &stop{class: ClassDefect, reason: "an editor field disappeared"}
		}
		if in.Type == "text" || in.Type == "textarea" {
			// A plain box is typed into: the engine's writer refuses the
			// applicant's own employer in a box labelled Employer, which reads
			// to it as a referee's.
			if ok, err := Focus(ctx, s.tab, in.I); err != nil || !ok {
				return &stop{class: ClassDefect, reason: "an editor field could not be focused"}
			}
			if err := s.tab.TypeText(ctx, sp.value); err != nil {
				return &stop{class: ClassEnvironment, reason: err.Error()}
			}
			continue
		}
		got, err := s.answer(ctx, "", []engine.Resolution{{Selector: in.Selector, Outcome: "filled", Value: sp.value}})
		if err != nil {
			return &stop{class: ClassEnvironment, reason: err.Error()}
		}
		if len(got) == 1 && (got[0].Outcome == "failed" || got[0].Outcome == "abstained") {
			if sp.roots[0] == "educationalestablishment" {
				return &stop{class: ClassAnswerGap, reason: "no exact match for the school in the search"}
			}
			return &stop{class: ClassDefect, reason: fmt.Sprintf("the editor field %s was not written: %s", sp.roots[0], got[0].Reason)}
		}
	}
	cur, err := Probe(ctx, s.tab)
	if err != nil {
		return &stop{class: ClassEnvironment, reason: err.Error()}
	}
	if cur.Oracle == nil || cur.Oracle.Save < 0 {
		return &stop{class: ClassDefect, reason: "the editor has no Save control"}
	}
	if stp := s.pressEditorControl(ctx, cur.Oracle.Save); stp != nil {
		return stp
	}
	for i := 0; i < 30; i++ {
		if after, err := Probe(ctx, s.tab); err == nil && !editorOpen(after, anchor) {
			return nil
		}
		time.Sleep(tm.Poll / 5)
	}
	return &stop{class: ClassDefect, reason: "the editor stayed open after Save"}
}

// pressEditorControl presses a timeline add button or an editor's Save through
// gates.PressControl at the editor stage.
func (s *session) pressEditorControl(ctx context.Context, handle int) *stop {
	loc, err := Locate(ctx, s.tab, handle)
	if err != nil {
		return &stop{class: ClassEnvironment, reason: err.Error()}
	}
	d := gates.PressControl(gates.ControlInput{Unattended: s.e.unattended(), Stage: gates.StageEditorSave, Found: loc.Found && loc.Visible, Labels: loc.Labels})
	if aerr := s.r.audit(s.e, d, s.e.Run.Input.URL, false); aerr != nil {
		return &stop{class: ClassEnvironment, reason: aerr.Error()}
	}
	if !d.Allowed() {
		return &stop{class: ClassDefect, reason: "the editor control was not pressed: " + d.Reason}
	}
	if !loc.Hit {
		return &stop{class: ClassDefect, reason: "the editor control is covered"}
	}
	if err := s.tab.Click(ctx, loc.X, loc.Y); err != nil {
		return &stop{class: ClassEnvironment, reason: err.Error()}
	}
	return nil
}

// clickChoice clicks a pill inside an editor, or the editor's Cancel. It is a
// choice in the form the walk writes, so the press gate decides it by the
// rules of the computer-use stage and the decision is audited: a control that
// reads as Withdraw, Discard, Submit, navigation or a destructive label, one
// that submits its form and a link out of the page are refused.
func (s *session) clickChoice(ctx context.Context, handle int) *stop {
	loc, err := Locate(ctx, s.tab, handle)
	if err != nil || !loc.Found {
		return &stop{class: ClassDefect, reason: "an editor choice could not be found"}
	}
	d := gates.PressControl(gates.ControlInput{Unattended: s.e.unattended(), Stage: gates.StageComputerUse, Found: loc.Visible, Labels: loc.Labels, FormSubmitter: loc.Submitter, Link: loc.Link})
	if aerr := s.r.audit(s.e, d, s.e.Run.Input.URL, false); aerr != nil {
		return &stop{class: ClassEnvironment, reason: aerr.Error()}
	}
	if !d.Allowed() {
		return &stop{class: ClassDefect, reason: "an editor choice was not pressed: " + d.Reason}
	}
	if !loc.Hit {
		return &stop{class: ClassDefect, reason: "an editor choice is covered"}
	}
	if err := s.tab.Click(ctx, loc.X, loc.Y); err != nil {
		return &stop{class: ClassEnvironment, reason: err.Error()}
	}
	return nil
}
