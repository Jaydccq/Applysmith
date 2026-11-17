package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// compensationEntry resolves a compensation-range entry: a pay-period picker
// takes its annual option, an open-ended floor menu is answered from the
// minimum, a text box gets the field's figure (with the currency when the
// label asks for both, as plain digits when it asks for no commas) and a
// bracket menu gets the bracket that contains the figure. No containing
// bracket abstains.
func compensationEntry(s *Step) (Draft, bool) {
	e, q := s.Entry, s.Q
	if e == nil || e.Kind != KindCompensationRange {
		return Draft{}, false
	}
	opts := realOptions(q.Options)
	if label, ok := compAnnualPeriodOption(opts); ok {
		if strings.TrimSpace(e.DefaultAnswer) == "" {
			return abstainDraft(ReasonPlaceholderEmpty, "", "kind=compensation-range pay-period picker but no configured amount to describe"), true
		}
		b := optionBinding{Label: label.Label, Value: label.Value, Score: 1}
		d := optionDraft(b, e, SourceEntry, false)
		d.Trace = []string{"kind=compensation-range pay-period picker -> " + quoteTerm(label.Label) + " (the entry's amount is annual)"}
		return d, true
	}
	field := compensationField(q.Text, q.Kind)
	if floors, ok := compFloorMenu(opts); ok && field != "currency" {
		return compAnswerFromFloors(e, field, floors), true
	}
	value := strings.TrimSpace(fieldOfCompensation(e, field))
	if field != "currency" {
		v, ok := compForPeriod(value, q.Text)
		if !ok && value != "" {
			return abstainDraft(ReasonPlaceholderEmpty, "", "kind=compensation-range field=%s has no monthly form for %q", field, value), true
		}
		value = v
	}
	if value == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "kind=compensation-range field=%s has no configured answer", field), true
	}
	if !hasRenderedOptions(q.Options) {
		if field != "currency" && compNoCommasRE.MatchString(q.Text) {
			digits, ok := compDigits(value)
			if !ok {
				return abstainDraft(ReasonPlaceholderEmpty, "", "kind=compensation-range field=%s has no plain-digit form for %q", field, value), true
			}
			value = compExampleSign(q.Text, e.Currency) + digits
		}
		return compTextAnswer(s, e, field, value), true
	}
	if field == "currency" {
		return compBindPlain(s, e, value), true
	}
	return compBindBracket(s, e, value), true
}

func fieldOfCompensation(e *Entry, field string) string {
	switch field {
	case "minimum":
		return e.Minimum
	case "maximum":
		return e.Maximum
	case "currency":
		return e.Currency
	}
	return e.DefaultAnswer
}

// compOfTheRange is "of your desired annual base salary range": the range a
// "beginning", "start" or "end" cue points at.
const compOfTheRange = `\s+of\s+(?:(?:your|the)\s+)?(?:(?:desired|expected|target|preferred|annual|yearly|base|total|salary|compensation|comp|pay)\s+){0,5}range\b`

var (
	compCurrencyWordRE = lazyregexp.New(`\bcurrenc(?:y|ies)\b`)
	// compInLocalCurrencyRE is the amount's unit ("salary expectations in your
	// local currency", Mindbody), not a request for the currency.
	compInLocalCurrencyRE = lazyregexp.New(`\bin\s+(?:(?:your|the)\s+)?local\s+currenc(?:y|ies)\b`)
	compAmountWordRE      = lazyregexp.New(`\b(?:amount|expectations?|expected|desired|requirements?|target|seeking|bonus|range)\b`)
	compMinCueRE          = lazyregexp.New(`\b(?:minimum|min|lowest|least|floor)\b|\bwalk\s+away\b|\bat\s+least\b|\blower\s+end\b|\b(?:beginning|start)` + compOfTheRange)
	// "The end of" the range, but never the "lower end".
	compMaxCueRE = lazyregexp.New(`\b(?:maximum|max|highest|most|upper|ceiling)\b|\bno\s+more\s+than\b|(?:^|\bthe\s+)end` + compOfTheRange)
)

// compensationField picks the sub-field a question asks for. A currency
// mention means the currency unless the control is a text box whose label also
// asks for an amount, which wants both.
func compensationField(text string, kind ControlKind) string {
	t := compInLocalCurrencyRE.ReplaceAllString(normalizeLabel(text), " ")
	if compCurrencyWordRE.MatchString(t) && (kind != KindText || !compAmountWordRE.MatchString(t)) {
		return "currency"
	}
	lo, hi := compMinCueRE.MatchString(t), compMaxCueRE.MatchString(t)
	switch {
	case lo && !hi:
		return "minimum"
	case hi && !lo:
		return "maximum"
	}
	return "default_answer"
}

var compPayPeriodRE = lazyregexp.New(`(?i)^(?:hourly|daily|weekly|bi-?weekly|semi-?monthly|monthly|quarterly|yearly|annual(?:ly)?|per\s+(?:hour|day|week|month|year|annum))$`)
var compAnnualWordRE = lazyregexp.New(`(?i)\b(?:year(?:ly)?|annual(?:ly)?|annum)\b`)

// compAnnualPeriodOption is the annual option of a pay-period picker: two or
// more options, every one a period word. The stored amount is annual by
// contract, so the picker is answered with the period, not the amount.
func compAnnualPeriodOption(opts []Option) (Option, bool) {
	if len(opts) < 2 {
		return Option{}, false
	}
	for _, o := range opts {
		if !compPayPeriodRE.MatchString(strings.TrimSpace(o.Label)) {
			return Option{}, false
		}
	}
	for _, o := range opts {
		if compAnnualWordRE.MatchString(o.Label) {
			return o, true
		}
	}
	return Option{}, false
}

// compNoCommasRE is a question that asks for the figure without commas.
var compNoCommasRE = lazyregexp.New(`(?i)\b(?:no|without)\s+(?:any\s+)?commas?\b`)

// compDollarCurrencies are the currencies written with a bare "$".
var compDollarCurrencies = []string{"USD", "CAD", "AUD", "NZD", "SGD", "HKD", "MXN"}

// compExampleSign is the one sign the question's figures are all written with
// ("e.g. $50000"), when the entry's currency is written with it. Figures in
// several currencies show no one sign, and a code is not a sign.
func compExampleSign(text, currency string) string {
	sign := ""
	for _, m := range compMarkedAmountRE.FindAllStringSubmatch(text, -1) {
		mark := m[1] + m[2]
		if sign != "" && mark != sign {
			return ""
		}
		sign = mark
	}
	have := strings.ToUpper(strings.TrimSpace(currency))
	if sign == "$" && slices.Contains(compDollarCurrencies, have) {
		return sign
	}
	for _, sym := range compSymbolCurrencies {
		if sym.sign == sign && slices.Contains(sym.currencies, have) {
			return sign
		}
	}
	return ""
}

// compTextAnswer answers a text box. An amount label that mentions currency
// gets "<amount> <currency>", never a bare amount to re-denominate.
func compTextAnswer(s *Step, e *Entry, field, value string) Draft {
	d := Draft{Entry: e, Source: SourceEntry}
	currency := strings.TrimSpace(e.Currency)
	if field != "currency" && s.Q.Kind == KindText && currency != "" && compCurrencyWordRE.MatchString(normalizeLabel(s.Q.Text)) {
		value += " " + currency
		d.Trace = []string{"kind=compensation-range field=" + field + " + currency -> " + quoteTerm(value)}
	} else {
		d.Trace = []string{"kind=compensation-range field=" + field + " -> " + quoteTerm(value)}
	}
	d.Answer = value
	if s.Q.Kind != KindText {
		d.ChosenLabel = value
	}
	return d
}

// compBindPlain binds the value to the options by the option scorer, as a
// currency dropdown ranks plain currency options.
func compBindPlain(s *Step, e *Entry, value string) Draft {
	b, ok := bindOption(s.bareScorer(), s.Q.Options, value)
	switch {
	case !ok:
		return Draft{Entry: e, Abstain: &Abstain{Reason: ReasonInputMismatch}, Trace: []string{"no rendered option matches " + quoteTerm(value)}}
	case b.Tie:
		return Draft{Entry: e, Abstain: &Abstain{Reason: ReasonAmbiguous}, Trace: []string{"options tie for " + quoteTerm(value)}}
	}
	d := optionDraft(b, e, SourceEntry, false)
	d.Trace = []string{"kind=compensation-range -> " + quoteTerm(b.Label) + " score=" + scoreText(b.Score)}
	return d
}

// compBindBracket picks the bracket option that contains the figure. The
// figure on the shared edge of two closed brackets belongs to the one where it
// is the lower bound; a ceiling answer ("Up to 25%") bounds its own bracket
// instead. A bracket priced in another currency than the entry's ("BRL $100,000
// - $150,000" beside USD ones) never holds the figure. Options that are no
// brackets fall back to the option scorer.
func compBindBracket(s *Step, e *Entry, value string) Draft {
	fig, ok := compFirstNumber(value)
	if !ok {
		return compBindPlain(s, e, value)
	}
	ans, _ := compRangeOf(value)
	ceiling := ans.Min == nil && ans.Max != nil
	have := strings.ToUpper(strings.TrimSpace(e.Currency))
	type cand struct {
		opt   Option
		score float64
	}
	var cands []cand
	brackets := 0
	for _, o := range s.Q.Options {
		if isPlaceholderOption(o.Label) {
			continue
		}
		r, ok := compRangeOf(o.Label)
		if !ok {
			continue
		}
		brackets++
		if r.Percent != nil && *r.Percent != fig.Percent {
			continue
		}
		if named := compNamedCurrencies(o.Label); have != "" && len(named) > 0 && !slices.Contains(named, have) {
			continue
		}
		if (r.Min != nil && fig.Value < *r.Min) || (r.Max != nil && fig.Value > *r.Max) {
			continue
		}
		closed := r.Min != nil && r.Max != nil && *r.Min != *r.Max
		score := 0.96
		switch {
		case ceiling && closed && fig.Value == *r.Min:
			score = 0.95
		case !ceiling && closed && fig.Value == *r.Max:
			score = 0.95
		}
		cands = append(cands, cand{o, score})
	}
	if brackets == 0 {
		return compBindPlain(s, e, value)
	}
	if len(cands) == 0 {
		return Draft{Entry: e, Abstain: &Abstain{Reason: ReasonInputMismatch},
			Trace: []string{"no bracket contains " + quoteTerm(value) + " -> abstain"}}
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.score > best.score {
			best = c
		}
	}
	other := 0.0
	for _, c := range cands {
		if normalizeLabel(c.opt.Label) == normalizeLabel(best.opt.Label) {
			continue
		}
		if c.score == best.score {
			return Draft{Entry: e, Abstain: &Abstain{Reason: ReasonAmbiguous},
				Trace: []string{"two brackets contain " + quoteTerm(value)}}
		}
		other = max(other, c.score)
	}
	d := optionDraft(optionBinding{Label: best.opt.Label, Value: best.opt.Value, Score: best.score, Other: other}, e, SourceEntry, false)
	d.Trace = []string{"kind=compensation-range bracket " + quoteTerm(best.opt.Label) + " contains " + quoteTerm(value)}
	return d
}

// compFloor is an option that claims only a lower bound.
type compFloor struct {
	Opt   Option
	Floor float64
}

var compOpenSuffixRE = lazyregexp.New(`(?i)(?:\+|\bplus\b|\bor\s+(?:more|above|over|greater|higher)\b|\band\s+(?:above|over|up|higher)\b)\s*$`)

// compFloorMenu reads the options as a menu of open-ended floors ("$90,000+"):
// at least two floors, and every option that carries a number is a floor, so
// one closed band disqualifies the menu. Options without a number are ignored.
func compFloorMenu(opts []Option) ([]compFloor, bool) {
	var out []compFloor
	for _, o := range opts {
		nums := compNumbers(o.Label)
		if len(nums) == 0 {
			continue
		}
		if len(nums) != 1 || nums[0].Percent || !compOpenSuffixRE.MatchString(strings.TrimSpace(o.Label)) {
			return nil, false
		}
		out = append(out, compFloor{o, nums[0].Value})
	}
	return out, len(out) >= 2
}

// compAnswerFromFloors answers a floor menu from the minimum (the maximum for a
// question that names one): the highest floor that does not exceed it. A figure
// below every floor abstains, because choosing a floor above the stored figure
// claims a walk-away the user never set. The fill is for review.
func compAnswerFromFloors(e *Entry, field string, floors []compFloor) Draft {
	src := e.Minimum
	if field == "maximum" {
		src = e.Maximum
	}
	fig, ok := compFirstNumber(src)
	if !ok {
		return abstainDraft(ReasonPlaceholderEmpty, "", "kind=compensation-range floor menu but field %s has no figure", field)
	}
	var best *compFloor
	for i := range floors {
		if floors[i].Floor <= fig.Value && (best == nil || floors[i].Floor > best.Floor) {
			best = &floors[i]
		}
	}
	if best == nil {
		return abstainDraft(ReasonInputMismatch, "", "kind=compensation-range every floor exceeds %q -> abstain", src)
	}
	d := optionDraft(optionBinding{Label: best.Opt.Label, Value: best.Opt.Value, Score: 1}, e, SourceEntry, false)
	d.Start = ReviewFillConfidence
	d.Trace = []string{"kind=compensation-range floor menu: highest floor not above " + quoteTerm(src) + " is " + quoteTerm(best.Opt.Label)}
	return d
}
