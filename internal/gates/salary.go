// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Range is a stated compensation range, annualized, with its currency.
type Range struct {
	Low, High float64
	Currency  string
}

// HoursPerYear and MonthsPerYear annualize hourly and monthly figures.
const (
	HoursPerYear  = 2080
	MonthsPerYear = 12
)

// LowestPay is the US federal minimum wage for a full-time year.
const LowestPay = 7.25 * HoursPerYear

// Placeholder reports a dollar range whose top is under LowestPay: no job pays
// that, so the figures stand where the pay was never filled in ("$1—$2 USD")
// and say nothing about it.
func (r Range) Placeholder() bool {
	return strings.EqualFold(r.Currency, "USD") && r.High < LowestPay
}

const num = `(\d[\d,]*(?:\.\d+)?)`

var (
	retirement  = lazyregexp.New(`(?i)401\s*\(?k\)?`)
	nonDollar   = lazyregexp.New(`[\x{00A3}\x{20AC}\x{00A5}\x{20B9}\x{20A9}]|(?:^|[^A-Za-z])(?:CA|C|A|AU|S|NZ|HK)\$`)
	otherCode   = lazyregexp.New(`(?i)\b(gbp|eur|cad|aud|chf|inr|jpy|sgd|cny|nzd)\b`)
	usdMark     = lazyregexp.New(`(?i)\bUSD\b|\bUS\$|\bU\.?S\.? dollars?\b`)
	rangeFigure = lazyregexp.New(`(?i)\$\s*` + num + `\s*(k)?(?:\s*usd)?\s*(?:-|\x{2013}|\x{2014}|to)\s*\$?\s*` + num + `\s*(k)?`)
	hourlyUnit  = lazyregexp.New(`(?i)/\s*h(ou)?r|per\s+hour|an\s+hour|hourly`)
	monthlyUnit = lazyregexp.New(`(?i)/\s*mo(nth)?\b|per\s+month|a\s+month|monthly`)
	annualUnit  = lazyregexp.New(`(?i)/\s*y(ea)?r|per\s+year|annual|a\s+year|yearly|per\s+annum`)
)

// betweenFigure is "between $230,000 USD and $334,000 USD"; a bare "and"
// elsewhere could join two unrelated figures. Same groups as rangeFigure.
var betweenFigure = lazyregexp.New(`(?i)\bbetween\s+\$\s*` + num + `\s*(k)?(?:\s*usd)?\s+and\s+\$?\s*` + num + `\s*(k)?`)

// unitWindow is how far after a range its unit may appear.
const unitWindow = 40

// ParseCompensation reads a stated range from text. A range needs two
// figures. A 401(k) reference and any figure followed by a percent sign are
// ignored; hourly figures are multiplied by 2,080 and monthly figures by 12
// unless an annual unit is also stated; figures under 1,000 are thousands;
// text with a non-dollar currency gives no range; a placeholder range such as
// "$1-$1/hr" or "$1—$2 USD" is ignored. When the text also names another currency by its
// code (a US and a Canadian range side by side), a dollar range counts only
// when it is marked USD, because a bare "$" could be either.
func ParseCompensation(text string) (Range, bool) {
	text = retirement.ReplaceAllString(text, " ")
	if nonDollar.MatchString(text) {
		return Range{}, false
	}
	mixed := otherCode.MatchString(text)
	for _, m := range append(rangeFigure.FindAllStringSubmatchIndex(text, -1), betweenFigure.FindAllStringSubmatchIndex(text, -1)...) {
		end := m[1]
		if strings.HasPrefix(strings.TrimSpace(text[end:]), "%") {
			continue
		}
		if mixed {
			after := text[end:min(len(text), end+unitWindow)]
			if i := strings.Index(after, "$"); i >= 0 {
				after = after[:i]
			}
			if !usdMark.MatchString(after) && !usdMark.MatchString(text[max(0, m[0]-6):m[0]+1]) {
				continue
			}
		}
		window := text[end:min(len(text), end+unitWindow)]
		lo, hi := applyUnit(figure(text, m[2], m[3]), m[4] >= 0, window), applyUnit(figure(text, m[6], m[7]), m[8] >= 0, window)
		r := Range{Low: lo, High: hi, Currency: "USD"}
		if lo <= 0 || hi < lo || r.Placeholder() {
			continue
		}
		return r, true
	}
	return Range{}, false
}

func figure(text string, from, to int) float64 {
	v, _ := strconv.ParseFloat(strings.ReplaceAll(text[from:to], ",", ""), 64)
	return v
}

func applyUnit(v float64, hasK bool, window string) float64 {
	switch {
	case hourlyUnit.MatchString(window) && !annualUnit.MatchString(window):
		return v * HoursPerYear
	case hasK:
		v *= 1000
	case v < 1000:
		v *= 1000
	}
	if monthlyUnit.MatchString(window) && !annualUnit.MatchString(window) {
		v *= MonthsPerYear
	}
	return v
}
