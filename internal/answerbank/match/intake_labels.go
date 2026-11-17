package match

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"applysmith/internal/lazyregexp"
)

// normalizeLabel is the primitive every option, employer and token comparison
// uses: NFKC, the six entities decoded, lowercase, whitespace collapsed, then
// leading and trailing whitespace and punctuation stripped. Punctuation inside
// the string and symbols such as "$" and "+" stay.
func normalizeLabel(s string) string {
	s = foldText(decodeEntities(s))
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
}

// foldText is the comparison fold shared by captured-scope matching and label
// normalization: NFKC, whitespace collapsed to single spaces, lowercase.
func foldText(s string) string {
	return strings.ToLower(collapseSpaces(norm.NFKC.String(s)))
}

var placeholderOptionWords = setOf(
	"select", "choose", "pick", "select one", "select an option", "please select", "please choose",
	"choose one", "pick one", "make a selection", "none selected", "no selection", "not selected",
	"nothing selected", "no answer",
)

var dashWrapped = lazyregexp.New(`^[-\x{2013}\x{2014}]{2,}.*[-\x{2013}\x{2014}]{2,}$`)

// isPlaceholderOption reports whether an option label is the control's own
// prompt ("Select...", "-- No answer --") rather than a choice.
func isPlaceholderOption(label string) bool {
	l := strings.TrimSpace(label)
	if l == "" || IsPlaceholderValue(l) {
		return true
	}
	return dashWrapped.MatchString(l) && placeholderOptionWords[normalizeLabel(l)]
}

// realOptions returns the options that are not placeholders.
func realOptions(opts []Option) []Option {
	var out []Option
	for _, o := range opts {
		if !isPlaceholderOption(o.Label) {
			out = append(out, o)
		}
	}
	return out
}

// hasRenderedOptions reports whether the control renders at least one option
// that is not a placeholder.
func hasRenderedOptions(opts []Option) bool {
	for _, o := range opts {
		if !isPlaceholderOption(o.Label) {
			return true
		}
	}
	return false
}

var (
	positionCounter = lazyregexp.New(` (\d+) of (\d+)$`)
	yesLabelRE      = lazyregexp.New(`^(?:yes|yeah|yep|yup|y|true|i agree|i confirm|i acknowledge)\b`)
	noLabelRE       = lazyregexp.New(`^(?:no|nope|nah|n|false|i do not|i disagree|not to my knowledge)\b`)
)

// dedupeOptionLabels keeps the first occurrence of every label (trimmed,
// case-sensitive) in the original order. Option values are untouched.
func dedupeOptionLabels(opts []Option) []Option {
	if len(opts) < 2 {
		return opts
	}
	seen := make(map[string]bool, len(opts))
	out := make([]Option, 0, len(opts))
	for _, o := range opts {
		k := strings.TrimSpace(o.Label)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, o)
	}
	return out
}

// stripPositionCounters removes an assistive "i of N" counter from every label,
// only when every label carries the in-order counter for the option count.
func stripPositionCounters(opts []Option) []Option {
	if len(opts) < 2 {
		return opts
	}
	for i, o := range opts {
		m := positionCounter.FindStringSubmatch(o.Label)
		if m == nil || m[1] != strconv.Itoa(i+1) || m[2] != strconv.Itoa(len(opts)) {
			return opts
		}
	}
	out := make([]Option, len(opts))
	for i, o := range opts {
		o.Label = positionCounter.ReplaceAllString(o.Label, "")
		out[i] = o
	}
	return out
}

// spellLetterYesNo relabels a bare Y/N pair as Yes/No for matching only. The
// pair is the two real options whose labels are "y" and "n".
func spellLetterYesNo(opts []Option) ([]Option, bool) {
	real := realOptions(opts)
	if len(real) != 2 {
		return opts, false
	}
	hasY, hasN := false, false
	for _, o := range real {
		switch strings.ToLower(strings.TrimSpace(o.Label)) {
		case "y":
			hasY = true
		case "n":
			hasN = true
		}
	}
	if !hasY || !hasN {
		return opts, false
	}
	out := make([]Option, len(opts))
	for i, o := range opts {
		o.Label = letterYesNo(o.Label)
		out[i] = o
	}
	return out, true
}

// letterYesNo spells a bare "y" or "n" label as Yes or No.
func letterYesNo(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "y":
		return "Yes"
	case "n":
		return "No"
	}
	return label
}

var rungShape = lazyregexp.New(`^\s*([^:\d]{1,40}?)\s*:\s+((?:[^\s\d]+\s+){3,}[^\d]*)$`)

// scoreOptionsOnRung relabels "<rung>: <description>" options to their rung when
// every real option has that shape, the rungs are distinct and short, and each
// description has four or more words and no digit. A Yes and a No are not a
// ladder: their descriptions say when to choose them and can narrow the answer
// ("YES: If you are a U.S. citizen or permanent resident."), so they are read
// whole (a pair that only restates a work-permit question is permitPairOnRungs).
func scoreOptionsOnRung(opts []Option) ([]Option, bool) {
	real := realOptions(opts)
	if len(real) < 2 {
		return opts, false
	}
	rungs := make(map[string]string, len(real))
	seen := map[string]bool{}
	for _, o := range real {
		m := rungShape.FindStringSubmatch(o.Label)
		if m == nil {
			return opts, false
		}
		rung := strings.TrimSpace(m[1])
		key := strings.ToLower(rung)
		if rung == "" || len(strings.Fields(rung)) > 4 || seen[key] {
			return opts, false
		}
		seen[key] = true
		rungs[o.Label] = rung
	}
	bare := make([]Option, 0, len(real))
	for _, o := range real {
		bare = append(bare, Option{Label: rungs[o.Label]})
	}
	if isYesNoOptionSet(bare) {
		return opts, false
	}
	out := make([]Option, len(opts))
	for i, o := range opts {
		if r, ok := rungs[o.Label]; ok && !isPlaceholderOption(o.Label) {
			o.Label = r
		}
		out[i] = o
	}
	return out, true
}

var (
	yesNoConditionRE = lazyregexp.New(`(?i)^\W*(yes|no)\s*[:\x{2013}\x{2014}-]\s*(.{8,})$`)
	permitHeldRE     = lazyregexp.New(`(?i)\b(?:hold|have|possess)\b[^.;]{0,30}\b(?:valid|active|current|legal)\b[^.;]{0,20}\b(?:work\s+permit|work\s+authori[sz]ation|authori[sz]ation\s+to\s+work|employment\s+authori[sz]ation)\b`)
	permitLackedRE   = lazyregexp.New(`(?i)\b(?:do\s+not|don['\x{2019}]t|no|not)\b[^.;]{0,40}\bauthori[sz]`)
	permitNarrowRE   = lazyregexp.New(`(?i)\bcitizen|\bpermanent\s+residen|\bgreen\s+card\b|\bsponsor|\bwithout\b`)
)

// permitPairOnRungs relabels a Yes and a No whose descriptions only restate
// the work-authorization question to Yes and No (Etched: "YES: If you
// currently hold a valid work permit (such as OPT, CPT, or an existing H-1B
// transfer)." and "NO: If you do not have any active, legal authorization to
// work in the country right now."): the Yes is for anyone who holds a permit
// to work, so the recorded Yes or No answers it. A description that names
// citizenship, permanent residence or sponsorship narrows the answer and keeps
// the labels whole.
func permitPairOnRungs(opts []Option) ([]Option, bool) {
	real := realOptions(opts)
	if len(real) != 2 {
		return opts, false
	}
	desc := map[string]string{}
	rung := map[string]string{}
	for _, o := range real {
		m := yesNoConditionRE.FindStringSubmatch(o.Label)
		if m == nil {
			return opts, false
		}
		key := strings.ToLower(m[1])
		if _, dup := desc[key]; dup || permitNarrowRE.MatchString(m[2]) {
			return opts, false
		}
		desc[key], rung[o.Label] = m[2], strings.ToUpper(key[:1])+key[1:]
	}
	if !permitHeldRE.MatchString(desc["yes"]) || !permitLackedRE.MatchString(desc["no"]) {
		return opts, false
	}
	out := make([]Option, len(opts))
	for i, o := range opts {
		if r, ok := rung[o.Label]; ok {
			o.Label = r
		}
		out[i] = o
	}
	return out, true
}

// isYesNoOptionSet reports a two or three option set that holds a Yes-shaped and
// a No-shaped label (a placeholder plus Yes plus No counts as three).
func isYesNoOptionSet(opts []Option) bool {
	if len(opts) < 2 || len(opts) > 3 {
		return false
	}
	hasYes, hasNo := false, false
	for _, o := range opts {
		l := normalizeLabel(o.Label)
		if yesLabelRE.MatchString(l) {
			hasYes = true
		}
		if noLabelRE.MatchString(l) {
			hasNo = true
		}
	}
	return hasYes && hasNo
}

func regexpMust(p string) *lazyregexp.Regexp { return lazyregexp.New(p) }

var dateShapedRE = lazyregexp.New(`^(?:\d{4}-\d{2}(?:-\d{2})?|\d{1,2}/\d{1,2}/(?:\d{2}|\d{4}))$`)
