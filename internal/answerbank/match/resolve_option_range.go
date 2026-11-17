package match

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// numberTokenRE is a number with its k or m suffix, which counts only when no
// letter follows it: "in 1 Month" is one, not one million (Automox).
var numberTokenRE = lazyregexp.New(`\$?\s*([0-9]{1,3}(?:,[0-9]{3})+|[0-9]+(?:\.[0-9]+)?)\s*(?:([kKmM])\b)?\s*(%)?`)

// codeTokenRE is a code whose digits name it, not a quantity: a visa or tax
// form (F-1, H-1B, H1-B1, E-3, W-2) or a grade band (K-12). Two Sigma's
// "H1-B1" had read as the range 1..1 and taken "F-1 OPT".
var codeTokenRE = lazyregexp.New(`(?i)\b[a-z]{1,3}-?[0-9]+[a-z]*[0-9]*\b`)

var utcOffsetRE = lazyregexp.New(`(?i)\b(?:utc|gmt)\s*[+\-\x{2212}]\s*\d`)

type numToken struct {
	value   float64
	percent bool
}

// numericRange is an interval read from a label; an unbounded side is infinite.
// percent is set when any number of the label is a percentage.
type numericRange struct {
	min, max float64
	percent  bool
}

func numericTokens(s string) []numToken {
	if !strings.ContainsAny(s, "0123456789") {
		return nil
	}
	s = codeTokenRE.ReplaceAllString(s, " ")
	var out []numToken
	for _, m := range numberTokenRE.FindAllStringSubmatch(s, -1) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(m[2]) {
		case "k":
			v *= 1_000
		case "m":
			v *= 1_000_000
		}
		out = append(out, numToken{v, m[3] == "%"})
	}
	return out
}

var (
	rangeAtLeastRE = lazyregexp.New(`\b(?:plus|or\s+more|and\s+(?:above|over|up)|or\s+(?:above|over|greater)|above|over|greater\s+than|at\s+least|minimum|min)\b`)
	// openPlusRE is the "N+" shorthand for "N or more": the plus sign closes
	// the number, so a leading sign ("UTC+5") is not read as one.
	openPlusRE    = lazyregexp.New(`[0-9]\s*[kKmM]?\s*%?\s*\+`)
	rangeAtMostRE = lazyregexp.New(`\b(?:up\s+to|less\s+than|under|below|at\s+most|maximum|max|no\s+more\s+than)\b`)
	// moreThanRE is "more than N" (ZipRecruiter's "More than 8 years"); "no
	// more than N" is a ceiling.
	moreThanRE    = lazyregexp.New(`\bmore\s+than\b`)
	notMoreThanRE = lazyregexp.New(`\b(?:no|not)\s+more\s+than\b`)
	// strictBelowRE is an at-most word that leaves its bound out.
	strictBelowRE = lazyregexp.New(`\b(?:less\s+than|fewer\s+than|under|below)\b`)
)

// rangeOfTokens reads the interval a label states from its numericTokens: two
// numbers are its ends, one number with an at-least or at-most word is a
// half-open range, anything else is not a range.
func rangeOfTokens(label string, toks []numToken) (numericRange, bool) {
	if len(toks) == 0 {
		return numericRange{}, false
	}
	percent := false
	for _, t := range toks {
		percent = percent || t.percent
	}
	if len(toks) >= 2 {
		a, b := toks[0].value, toks[1].value
		return numericRange{math.Min(a, b), math.Max(a, b), percent}, true
	}
	norm := normalizeLabel(label)
	switch {
	case rangeAtLeastRE.MatchString(norm), openPlusRE.MatchString(label), moreThanRE.MatchString(norm) && !notMoreThanRE.MatchString(norm):
		return numericRange{toks[0].value, math.Inf(1), percent}, true
	case rangeAtMostRE.MatchString(norm):
		return numericRange{math.Inf(-1), toks[0].value, percent}, true
	}
	return numericRange{}, false
}

var rangeUnitRE = lazyregexp.New(`(?i)\b(hour|hr|day|week|wk|month|mo|year|yr)s?\b`)

// durationUnit is the first time unit a text names, folded to one spelling:
// hour, day, week, month or year; "" when it names none.
func durationUnit(s string) string {
	m := rangeUnitRE.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	switch u := strings.ToLower(m[1]); u {
	case "hr":
		return "hour"
	case "wk":
		return "week"
	case "mo":
		return "month"
	case "yr":
		return "year"
	default:
		return u
	}
}

// numericSide is what scoreNumericRange reads from one text alone, a label or
// an answer. A control's profiles keep it (pairReadings.numeric), so each text
// is read once and not once per pair.
type numericSide struct {
	digits bool // a text without a digit is neither a quantity nor a range
	utc    bool
	unit   string
	toks   []numToken
	rng    numericRange
	ranged bool
	// strictBelow is an at-most word that leaves its bound out; it is read
	// only for a range open below, the one place it is asked.
	strictBelow bool
}

// noNumbers is the reading of every text without a digit. It is shared, so it
// must not be modified.
var noNumbers = &numericSide{}

func readNumericSide(s string) *numericSide {
	if !strings.ContainsAny(s, "0123456789") {
		return noNumbers
	}
	n := &numericSide{digits: true, utc: utcOffsetRE.MatchString(s), unit: durationUnit(s), toks: numericTokens(s)}
	n.rng, n.ranged = rangeOfTokens(s, n.toks)
	n.strictBelow = n.ranged && math.IsInf(n.rng.min, -1) && strictBelowRE.MatchString(normalizeLabel(s))
	return n
}

// scoreNumericRange scores a numeric answer against an option whose label is a
// range containing it: 0.96, or 0.95 on the upper edge of a closed bucket so
// contiguous brackets resolve to the bucket where the value is the lower bound.
// An answer that is itself a ceiling flips the preference, so "Up to 25%" picks
// "0-25%" and not "25-50%".
//
// It reads the label and the answer through their readings. Every refusal is
// the same zero, so their order does not matter.
func scoreNumericRange(l, a *numericSide) (float64, string, bool) {
	// A label without a digit states no range.
	if !a.digits || !l.digits {
		return 0, "", false
	}
	// A UTC offset ("UTC-08:00") is an identifier, not a quantity: read as a
	// range it would make a Pacific label contain the Eastern offset.
	if l.utc || a.utc {
		return 0, "", false
	}
	// "3 weeks" is not in "More than 3 months": a duration is compared only in
	// the unit both name.
	if a.unit != "" && l.unit != "" && a.unit != l.unit {
		return 0, "", false
	}
	if len(a.toks) == 0 {
		return 0, "", false
	}
	first := a.toks[0]
	r := l.rng
	if !l.ranged || r.percent != first.percent {
		return 0, "", false
	}
	v := first.value
	if v < r.min || v > r.max {
		return 0, "", false
	}
	bounds := fmt.Sprintf("%s..%s", boundText(r.min, "-inf"), boundText(r.max, "+inf"))
	if ar := a.rng; a.ranged && math.IsInf(ar.min, -1) && !math.IsInf(ar.max, 1) {
		lowerEdge := !math.IsInf(r.min, 0) && v == r.min && !math.IsInf(r.max, 0) && v != r.max
		if lowerEdge {
			return 0.95, fmt.Sprintf("numeric-range lower-edge %v (%s): ceiling bounds the bucket below", v, bounds), true
		}
		return 0.96, fmt.Sprintf("numeric-range contains ceiling %v (%s)", v, bounds), true
	}
	upperEdge := !math.IsInf(r.max, 0) && v == r.max && !math.IsInf(r.min, 0) && v != r.min
	// "Less than 1 year" ends below its bound: a value on it is in the bucket
	// that starts there (ZipRecruiter's "More than 1 year ... but less than 2").
	if math.IsInf(r.min, -1) && v == r.max && l.strictBelow {
		upperEdge = true
	}
	if upperEdge {
		return 0.95, fmt.Sprintf("numeric-range upper-edge %v (%s): defer to the lower-bound bucket", v, bounds), true
	}
	return 0.96, fmt.Sprintf("numeric-range contains %v (%s)", v, bounds), true
}

func boundText(v float64, inf string) string {
	if math.IsInf(v, 0) {
		return inf
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
