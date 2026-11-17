package match

import (
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// minSelectionBind is the score a selection needs to bind to an option.
const minSelectionBind = 0.70

var mselCapRE = lazyregexp.New(`(?i)\b(?:select|choose|pick)\s+(?:up\s+to|at\s+most|no\s+more\s+than|a\s+maximum\s+of)\s+(\d+)\b|\bmaximum\s+of\s+(\d+)\b|\bup\s+to\s+(\d+)\s+(?:options?|choices?|answers?)\b`)

// selectionCap reads a "select up to N" cap from the question. A cap is part
// of the answer: a tick over it is a validation error on submit.
func selectionCap(text string) (int, bool) {
	m := mselCapRE.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	for _, g := range m[1:] {
		if g == "" {
			continue
		}
		n := 0
		for _, r := range g {
			n = n*10 + int(r-'0')
			if n > 1<<20 {
				break
			}
		}
		return n, n > 0
	}
	return 0, false
}

var noneOptions = setOf("none", "none of the above", "n/a", "na", "not applicable")

// multiSelectEntry resolves a multi-select entry. A checkbox group or a
// multi-select control takes every selection that binds, in the entry's order,
// up to the question's cap; a control that accepts one answer takes the first
// selection that binds; a text box takes the selections the question names.
func multiSelectEntry(s *Step) (Draft, bool) {
	e, q := s.Entry, s.Q
	if e == nil || e.Kind != KindMultiSelect {
		return Draft{}, false
	}
	if e.Selected == nil {
		return abstainDraft(ReasonPlaceholderEmpty, "", "kind=multi-select: no selections recorded"), true
	}
	if len(e.Selected) == 0 {
		return mselNone(s, e), true
	}
	selected := mselNamed(e, q.Text)
	if len(selected) == 0 {
		return abstainDraft(ReasonInputMismatch, "", "kind=multi-select: none of the selections is among the options the question names"), true
	}
	real := realOptions(q.Options)
	switch {
	case len(real) == 0:
		return mselText(s, e, selected), true
	case mselMultiAnswer(q, len(real)):
		return mselBindAll(s, e, selected), true
	}
	return mselFirstBound(s, e, selected), true
}

// mselNamed keeps the selections the question names when it names some of the
// entry's allowed options, and all of them otherwise.
func mselNamed(e *Entry, text string) []string {
	var mentioned []string
	for _, opt := range e.AllowedOptions {
		if containsWord(text, opt) {
			mentioned = append(mentioned, opt)
		}
	}
	if len(mentioned) == 0 {
		return e.Selected
	}
	var out []string
	for _, sel := range e.Selected {
		for _, m := range mentioned {
			if sel == m {
				out = append(out, sel)
				break
			}
		}
	}
	return out
}

// mselMultiAnswer reports a control that takes several answers. The page's
// rendering decides: a group of checkboxes (more than one option) is multi-answer
// whatever input type the extension reported, and a lone consent checkbox is not.
func mselMultiAnswer(q Question, options int) bool {
	if q.InputType == InputMultiSelect {
		return true
	}
	return (q.InputType == InputCheckbox || q.Kind == KindCheckbox) && options > 1
}

// mselNone answers an empty selection: "No" on a Yes/No control, the option
// that means none when one is offered, nothing otherwise.
func mselNone(s *Step, e *Entry) Draft {
	q := s.Q
	if isYesNoOptionSet(q.Options) {
		return bindEntryAnswer(s, e, "No", "kind=multi-select: empty selection on a Yes/No control -> No")
	}
	for _, o := range realOptions(q.Options) {
		if noneOptions[normalizeLabel(o.Label)] {
			d := optionDraft(optionBinding{Label: o.Label, Value: o.Value, Score: 1}, e, SourceEntry, true)
			d.Trace = []string{"kind=multi-select: empty selection -> " + quoteTerm(o.Label)}
			return d
		}
	}
	return abstainDraft(ReasonInputMismatch, "", "kind=multi-select: empty selection and no option means none -> abstain")
}

func mselText(s *Step, e *Entry, selected []string) Draft {
	d := Draft{Entry: e, Source: SourceEntry, Deterministic: true, Answer: strings.Join(capSelections(s.Q.Text, selected, &s.trace), ", ")}
	d.Trace = []string{"kind=multi-select: text answer " + quoteTerm(d.Answer)}
	if s.Q.Kind != KindText {
		d.ChosenLabel = d.Answer
	}
	return d
}

// capSelections truncates to the question's cap, keeping the user's order.
func capSelections(text string, in []string, trace *[]string) []string {
	n, ok := selectionCap(text)
	if !ok || len(in) <= n {
		return in
	}
	*trace = append(*trace, "multi-select: question declares a cap of "+strconv.Itoa(n)+"; dropped "+strings.Join(in[n:], ", "))
	return in[:n]
}

// entryCapped applies the entry's own confidence cap to a score.
func entryCapped(e *Entry, score float64) float64 {
	if e.Confidence != nil && *e.Confidence < score {
		return *e.Confidence
	}
	return score
}

func mselBindAll(s *Step, e *Entry, selected []string) Draft {
	var labels []string
	var dropped []string
	low := 1.0
	for _, sel := range selected {
		b, ok := bindOption(s.bareScorer(), s.Q.Options, sel)
		if !ok || entryCapped(e, b.Score) < minSelectionBind {
			b, ok = bindAlternative(s.Q.Options, sel)
		}
		if !ok || entryCapped(e, b.Score) < minSelectionBind {
			dropped = append(dropped, sel)
			continue
		}
		dup := false
		for _, l := range labels {
			if l == b.Label {
				dup = true
			}
		}
		if dup {
			continue
		}
		labels = append(labels, b.Label)
		low = min(low, b.Score)
	}
	if len(labels) == 0 {
		return Draft{Entry: e, Abstain: &Abstain{Reason: ReasonLowConfidence},
			Trace: []string{"multi-select: none of [" + strings.Join(selected, ", ") + "] bound to an option at 0.70 -> abstain"}}
	}
	trace := []string{}
	labels = capSelections(s.Q.Text, labels, &trace)
	if len(dropped) > 0 {
		trace = append(trace, "multi-select: no option offered for ["+strings.Join(dropped, ", ")+"] - dropped, not guessed")
	}
	return Draft{Entry: e, Source: SourceEntry, Deterministic: true,
		Answer: strings.Join(labels, ", "), ChosenLabel: labels[0], ChosenLabels: labels,
		HasOption: true, OptionScore: low, Trace: trace}
}

func mselFirstBound(s *Step, e *Entry, selected []string) Draft {
	for i, sel := range selected {
		b, ok := bindOption(s.bareScorer(), s.Q.Options, sel)
		if !ok || b.Tie || entryCapped(e, b.Score) < minSelectionBind {
			continue
		}
		d := optionDraft(b, e, SourceEntry, true)
		d.Trace = []string{"multi-select on a single-answer control: rank " + strconv.Itoa(i+1) + " " + quoteTerm(sel) + " -> " + quoteTerm(b.Label)}
		return d
	}
	return abstainDraft(ReasonInputMismatch, "", "kind=multi-select: no selection binds to an option of the single-answer control")
}

var (
	// optionAltSplitRE splits an option that lists alternatives ("Snowflake,
	// Databricks, or Delta Lake"); otherAltRE is the catch-all among them
	// ("other Streaming platforms"), which names nothing.
	optionAltSplitRE = lazyregexp.New(`(?i)\s*,\s*(?:or\s+|and\s+)?|\s+or\s+|\s*/\s*`)
	otherAltRE       = lazyregexp.New(`(?i)^(?:any\s+)?others?\b`)
)

// bindAlternative binds a selection naming alternatives ("Apache
// Kafka/Kinesis") to the one option that lists one of them among its own
// (Tebra: "Kafka or other Streaming platforms"): an option's alternative read
// as whole words in a part of the selection. Two options so read are no
// binding.
func bindAlternative(opts []Option, sel string) (optionBinding, bool) {
	parts := strings.Split(parenRE.ReplaceAllString(sel, " "), "/")
	found := -1
	for i, o := range opts {
		for _, alt := range optionAltSplitRE.Split(strings.TrimSpace(parenRE.ReplaceAllString(o.Label, " ")), -1) {
			if alt = strings.TrimSpace(alt); len(alt) < 3 || otherAltRE.MatchString(alt) {
				continue
			}
			if slices.ContainsFunc(parts, func(p string) bool { return containsWord(p, alt) }) {
				if found >= 0 && found != i {
					return optionBinding{}, false
				}
				found = i
			}
		}
	}
	if found < 0 {
		return optionBinding{}, false
	}
	return optionBinding{Index: found, Label: opts[found].Label, Value: opts[found].Value, Score: minSelectionBind}, true
}
