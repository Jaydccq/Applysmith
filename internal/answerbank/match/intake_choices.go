package match

import (
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

var (
	choiceMarker  = lazyregexp.New(`(\d{1,2})\s*(?:[.):]|\s-)\s*([A-Za-z])`)
	howManyRE     = lazyregexp.New(`(?i)\bhow (?:many|much|long)\b`)
	closeAfterEnu = lazyregexp.New(`^[\s;,]*\)`)
)

const (
	maxChoiceLabel   = 60
	lastChoiceWindow = 80
)

type choiceMark struct {
	num        int
	start      int // index of the first digit
	labelStart int // index of the first letter of the label
	paren      bool
}

// choiceMarkers scans for "N." / "N)" / "N:" / "N -" markers followed by a
// letter. The two checks RE2 cannot do inside a pattern are explicit: the
// character before the marker is not a word character or a period (so "3.5
// years" and "0-10" are not markers), and a "(N)" form numbers the parts of one
// request and is ignored.
func choiceMarkers(s string) []choiceMark {
	var out []choiceMark
	for _, m := range choiceMarker.FindAllStringSubmatchIndex(s, -1) {
		start := m[0]
		if start > 0 {
			c := s[start-1]
			if c == '.' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				continue
			}
		}
		// m[2]..m[3] is the number; the character after it tells the form.
		n, _ := strconv.Atoi(s[m[2]:m[3]])
		closeIsParen := m[3] < len(s) && s[m[3]] == ')'
		if closeIsParen && start > 0 && s[start-1] == '(' {
			continue
		}
		out = append(out, choiceMark{num: n, start: start, labelStart: m[4], paren: start > 0 && s[start-1] == '('})
	}
	return out
}

// lastChoiceLabel reads the label of the final choice: it ends at the first of
// ; , | ) ? newline or a period followed by whitespace.
func lastChoiceLabel(s string, from int) (label string, end int) {
	limit := from + lastChoiceWindow
	if limit > len(s) {
		limit = len(s)
	}
	seg := s[from:limit]
	cut := len(seg)
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if c == ';' || c == ',' || c == '|' || c == ')' || c == '?' || c == '\n' {
			cut = i
			break
		}
		if c == '.' && (i+1 == len(seg) || seg[i+1] == ' ') {
			cut = i
			break
		}
	}
	return seg[:cut], from + cut
}

// rewriteNumberedChoices turns an option-less text box whose label enumerates
// numbered choices into a single select whose option values are the numbers.
// It reports false when the question is not such a list.
func rewriteNumberedChoices(q Question) (Question, bool) {
	if q.Kind != KindText || len(q.Options) > 0 || howManyRE.MatchString(q.Text) {
		return q, false
	}
	s := q.Text
	marks := choiceMarkers(s)
	first := -1
	for i, m := range marks {
		if m.num == 1 {
			first = i
			break
		}
	}
	if first < 0 {
		return q, false
	}
	run := []choiceMark{marks[first]}
	for _, m := range marks[first+1:] {
		if m.num != len(run)+1 {
			break
		}
		run = append(run, m)
	}
	if len(run) < 2 {
		return q, false
	}
	var opts []Option
	for i, m := range run {
		var label string
		end := 0
		if i+1 < len(run) {
			label = s[m.labelStart:run[i+1].start]
			end = run[i+1].start
		} else {
			label, end = lastChoiceLabel(s, m.labelStart)
		}
		label = strings.TrimRight(label, " \t;,|/")
		label = strings.TrimSpace(label)
		if label == "" || len(label) > maxChoiceLabel {
			return q, false
		}
		opts = append(opts, Option{Value: strconv.Itoa(m.num), Label: label})
		if i+1 == len(run) {
			cutFrom := run[0].start
			tail := s[end:]
			if run[0].paren {
				cutFrom--
				if loc := closeAfterEnu.FindStringIndex(tail); loc != nil {
					tail = tail[loc[1]:]
				}
			}
			q.Text = strings.TrimSpace(strings.TrimRight(s[:cutFrom], " ") + " " + strings.TrimLeft(tail, " ;,"))
		}
	}
	q.Options = opts
	q.Kind = KindSelect
	q.InputType = InputSingleSelect
	return q, true
}

var (
	// legendKeyRE is an option whose label is a bare number; legendEntryRE one
	// entry of a legend in the question that says what each number stands for
	// (Tebra: "[1 = Spark; 2 = Airflow; 3 = Kafka or other Streaming platforms]").
	legendKeyRE   = lazyregexp.New(`^\s*(\d{1,2})\s*$`)
	legendEntryRE = lazyregexp.New(`(?:^|[\[(;,\s])(\d{1,2})\s*=\s*([^;\]\n]+)`)
	legendWrapRE  = lazyregexp.New(`\[\s*\]|\(\s*\)`)
)

// rewriteOptionLegend reads options labelled by bare numbers through the
// legend the question gives for them: each option is matched by what its
// number stands for, and the legend leaves the text. Every option's number must
// be in the legend; anything else is not a legend.
func rewriteOptionLegend(q Question) (Question, bool) {
	ms := legendEntryRE.FindAllStringSubmatchIndex(q.Text, -1)
	if len(ms) < 2 || len(realOptions(q.Options)) < 2 {
		return q, false
	}
	legend := map[string]string{}
	first, last := -1, -1
	for _, m := range ms {
		legend[q.Text[m[2]:m[3]]] = strings.TrimSpace(q.Text[m[4]:m[5]])
		if first < 0 {
			first = m[2]
		}
		last = m[5]
	}
	out := append([]Option(nil), q.Options...)
	for i, o := range out {
		if isPlaceholderOption(o.Label) {
			continue
		}
		m := legendKeyRE.FindStringSubmatch(o.Label)
		if m == nil || legend[m[1]] == "" {
			return q, false
		}
		out[i].Label = legend[m[1]]
	}
	q.Options = out
	q.Text = strings.TrimSpace(legendWrapRE.ReplaceAllString(q.Text[:first]+q.Text[last:], ""))
	return q, true
}

var (
	// letterKeyRE is an option whose label is a bare capital letter;
	// letterDefRE the head of one letter's definition in a description ("A .
	// U.S. Person, which is ...", "C. Not a U.S. Person ...", Oklo), and
	// ruleLineRE the drawn line that closes a definition.
	letterKeyRE = lazyregexp.New(`^\s*([A-H])\s*$`)
	letterDefRE = lazyregexp.New(`(?:^|[\s*_])([A-H])\s?[.)]\s+`)
	ruleLineRE  = lazyregexp.New(`_{5,}`)
)

// rewriteLetterDefinitions reads options labelled by bare letters through the
// definitions the description under the question gives them: each option is
// matched by its definition, which runs to the next letter's definition or a
// drawn line. Every option's letter must be defined exactly once, in the
// options' order; anything else is not a set of definitions.
func rewriteLetterDefinitions(q Question) (Question, bool) {
	d := q.Description
	if d == "" || len(realOptions(q.Options)) < 2 {
		return q, false
	}
	var order []string
	for _, o := range q.Options {
		if isPlaceholderOption(o.Label) {
			continue
		}
		m := letterKeyRE.FindStringSubmatch(o.Label)
		if m == nil {
			return q, false
		}
		order = append(order, m[1])
	}
	var heads [][]int
	for _, m := range letterDefRE.FindAllStringSubmatchIndex(d, -1) {
		if slices.Contains(order, d[m[2]:m[3]]) {
			heads = append(heads, m)
		}
	}
	if len(heads) != len(order) {
		return q, false
	}
	defs := map[string]string{}
	for i, m := range heads {
		if d[m[2]:m[3]] != order[i] {
			return q, false
		}
		end := len(d)
		if i+1 < len(heads) {
			end = heads[i+1][2]
		}
		body := d[m[1]:end]
		if loc := ruleLineRE.FindStringIndex(body); loc != nil {
			body = body[:loc[0]]
		}
		if body = strings.TrimSpace(body); body == "" {
			return q, false
		}
		defs[order[i]] = body
	}
	out := append([]Option(nil), q.Options...)
	for i, o := range out {
		if m := letterKeyRE.FindStringSubmatch(o.Label); m != nil {
			out[i].Label = defs[m[1]]
		}
	}
	q.Options = out
	return q, true
}
