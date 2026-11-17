package match

import (
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// compNum is one number read from a label: "$120k" is 120000, "25%" is a
// percent.
type compNum struct {
	Value   float64
	Percent bool
}

// compRange is the span a label claims. A nil bound is open. Percent is nil
// when the label carries no unit.
type compRange struct {
	Min, Max *float64
	Percent  *bool
}

var compNumRE = lazyregexp.New(`\$?\s*([0-9]{1,3}(?:,[0-9]{3})+|[0-9]+(?:\.[0-9]+)?)\s*([kKmM]?)\s*(%?)`)

// compNumbers lists the numbers in a text. A k or m suffix only counts when no
// letter follows it, so "100 months" is not 100 million.
func compNumbers(text string) []compNum {
	var out []compNum
	for _, m := range compNumRE.FindAllStringSubmatchIndex(text, -1) {
		raw := strings.ReplaceAll(text[m[2]:m[3]], ",", "")
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		suffix := text[m[4]:m[5]]
		if suffix != "" {
			if rest := text[m[5]:]; rest != "" && unicode.IsLetter([]rune(rest)[0]) {
				suffix = ""
			}
		}
		switch strings.ToLower(suffix) {
		case "k":
			v *= 1_000
		case "m":
			v *= 1_000_000
		}
		out = append(out, compNum{Value: v, Percent: text[m[6]:m[7]] == "%"})
	}
	return out
}

func compFirstNumber(text string) (compNum, bool) {
	if n := compNumbers(text); len(n) > 0 {
		return n[0], true
	}
	return compNum{}, false
}

var compBareMarkRE = lazyregexp.New(`^\s*(?:` + compSign + `|` + compCodes + `)?\s*$`)

// compDigits writes a stored figure as plain digits: "$120,000", "120,000 USD"
// and "120k" are all "120000". A value holding anything but one figure and its
// currency, or a percent, has no such form.
func compDigits(value string) (string, bool) {
	locs := compNumRE.FindAllStringIndex(value, -1)
	nums := compNumbers(value)
	if len(locs) != 1 || len(nums) != 1 || nums[0].Percent || !compBareMarkRE.MatchString(value[:locs[0][0]]+value[locs[0][1]:]) {
		return "", false
	}
	return strconv.FormatFloat(nums[0].Value, 'f', -1, 64), true
}

var (
	compLowerCue = lazyregexp.New(`\b(?:plus|or more|and (?:above|over|up)|or (?:above|over|greater)|above|over|greater than|at least|minimum|min)\b`)
	compUpperCue = lazyregexp.New(`\b(?:up to|less than|under|below|at most|maximum|max|no more than)\b`)
)

// compPairRE is one amount written for two periods ("120,000/57.69", annual
// and hourly).
var compPairRE = lazyregexp.New(`(\$?[0-9][0-9,]*(?:\.[0-9]+)?[kKmM]?)\s*/\s*(\$?[0-9][0-9,]*(?:\.[0-9]+)?[kKmM]?)`)

// compAnnualOfPairs keeps the larger figure of each pair: the stored amount is
// annual, and an annual figure is larger than its hourly one.
func compAnnualOfPairs(label string) string {
	return compPairRE.ReplaceAllStringFunc(label, func(p string) string {
		m := compPairRE.FindStringSubmatch(p)
		a, b := compNumbers(m[1]), compNumbers(m[2])
		if len(a) == 1 && len(b) == 1 && b[0].Value > a[0].Value {
			return m[2]
		}
		return m[1]
	})
}

// compRangeOf reads a label as a span: two numbers are a closed band, one
// number with a lower cue is open above, with an upper cue open below.
func compRangeOf(label string) (compRange, bool) {
	label = compAnnualOfPairs(label)
	toks := compNumbers(label)
	if len(toks) == 0 {
		return compRange{}, false
	}
	var percent *bool
	for _, t := range toks {
		if t.Percent {
			p := true
			percent = &p
			break
		}
	}
	if percent == nil {
		p := false
		percent = &p
	}
	if len(toks) >= 2 {
		lo, hi := toks[0].Value, toks[1].Value
		if lo > hi {
			lo, hi = hi, lo
		}
		return compRange{Min: &lo, Max: &hi, Percent: percent}, true
	}
	v := toks[0].Value
	n := normalizeLabel(label)
	// A sign is a bound too (Climate First Bank: "≤40,000", "≥100,001").
	lead := strings.TrimSpace(label)
	switch {
	case compLowerCue.MatchString(n) || strings.HasSuffix(lead, "+") || strings.HasPrefix(lead, "≥") || strings.HasPrefix(lead, ">"):
		return compRange{Min: &v, Percent: percent}, true
	case compUpperCue.MatchString(n) || strings.HasPrefix(lead, "≤") || strings.HasPrefix(lead, "<"):
		return compRange{Max: &v, Percent: percent}, true
	}
	return compRange{}, false
}

// compPeriodMonthly reports a question for a monthly figure.
var compMonthlyRE = lazyregexp.New(`(?i)\b(?:monthly|per\s+month|each\s+month)\b`)

var compPlainNumberRE = lazyregexp.New(`^\d+(?:\.\d+)?$`)

// compForPeriod returns the stored annual figure for a monthly question as
// annual/12 rounded half up to two decimals with trailing zeros removed, and
// the figure unchanged otherwise. A figure that is not a plain positive number
// has no monthly form. Rational arithmetic keeps the rounding exact.
func compForPeriod(annual, question string) (string, bool) {
	if !compMonthlyRE.MatchString(question) {
		return annual, true
	}
	plain := strings.ReplaceAll(strings.TrimSpace(annual), ",", "")
	if !compPlainNumberRE.MatchString(plain) {
		return "", false
	}
	r, ok := new(big.Rat).SetString(plain)
	if !ok || r.Sign() <= 0 {
		return "", false
	}
	r.Quo(r, big.NewRat(12, 1))
	r.Mul(r, big.NewRat(100, 1))
	r.Add(r, big.NewRat(1, 2))
	cents := new(big.Int).Div(r.Num(), r.Denom())
	whole, frac := new(big.Int).DivMod(cents, big.NewInt(100), new(big.Int))
	out := whole.String() + "." + twoDigits(int(frac.Int64()))
	switch {
	case strings.HasSuffix(out, ".00"):
		out = strings.TrimSuffix(out, ".00")
	case strings.HasSuffix(out, "0"):
		out = strings.TrimSuffix(out, "0")
	}
	return out, true
}

func twoDigits(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
