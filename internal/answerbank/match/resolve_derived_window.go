package match

import (
	"fmt"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Start windows ("Are you able to start between X and Y?"), start periods
// ("Which quarter can you start?") and a stored date asked as a span from
// today. All three read availability: the first day the applicant can start.

var (
	ableToStartRE = lazyregexp.New(`(?i)\b(?:(?:are|would|will)\s+you\s+(?:be\s+)?(?:able|available|ready)\s+to|(?:can|could)\s+you)\s+(?:start|begin|commence|join)\b`)
	whichPeriodRE = lazyregexp.New(`(?i)\b(?:which|what)\s+(?:quarter|month|term|semester|season|cohort|start\s+date\s+window)\b[^?]*\b(?:start|begin|join|commence)\b`)
)

const dvDash = `\x{2013}\x{2014}`

// withinSpanRE is a window counted from now: "within 30-60 days", "within 2
// weeks". An offer or the application comes no earlier than today, so the
// window measured from today is the shortest one the question can mean.
var withinSpanRE = lazyregexp.New(`(?i)\bwithin\s+(\d+)(?:\s*(?:-|to|[` + dvDash + `])\s*(\d+))?\s+(days?|weeks?)\b`)

type windowPattern struct {
	re *lazyregexp.Regexp
	// before marks "before X": the window ends the day before X begins.
	before bool
}

var windowPatterns = []windowPattern{
	{lazyregexp.New(`(?i)\bbetween\s+` + dvDatePattern + `\s+(?:and|to|-|[` + dvDash + `])\s+(` + dvDatePattern + `)`), false},
	{lazyregexp.New(`(?i)\bfrom\s+` + dvDatePattern + `\s+(?:to|through|thru|until|-|[` + dvDash + `])\s+(` + dvDatePattern + `)`), false},
	{lazyregexp.New(`(?i)\b(?:by|no\s+later\s+than|on\s+or\s+before)\s+(` + dvDatePattern + `)`), false},
	{lazyregexp.New(`(?i)\bbefore\s+(` + dvDatePattern + `)`), true},
	{lazyregexp.New(`(?i)\b(?:in|during)\s+(` + dvDatePattern + `)`), false},
}

// startWindowEnd is the last day of the window a can-you-start question names,
// or false when the question is not that shape.
func startWindowEnd(text string, today Date) (Date, bool) {
	loc := ableToStartRE.FindStringIndex(text)
	if loc == nil {
		return Date{}, false
	}
	clause, _, _ := strings.Cut(text[loc[1]:], "?")
	if m := withinSpanRE.FindStringSubmatch(clause); m != nil {
		n, _ := strconv.Atoi(m[1])
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		if strings.HasPrefix(strings.ToLower(m[3]), "week") {
			n *= 7
		}
		return today.AddDays(n), true
	}
	for _, p := range windowPatterns {
		m := p.re.FindStringSubmatch(clause)
		if m == nil {
			continue
		}
		first, last, ok := dateSpan(m[1])
		if !ok {
			continue
		}
		if p.before {
			return first.AddDays(-1), true
		}
		return last, true
	}
	return Date{}, false
}

// availableFrom is the first day the applicant can start: StartDate of the
// stored start date, moved past unfinished education (AfterEducation). The
// returned draft is an abstain when a fact is missing; both results are empty
// when no start-date entry exists, so the lane stands down.
func availableFrom(s *Step) (Date, *Draft) {
	e := entryNamed(s, IDEarliestStartDate)
	if e == nil || e.Kind != KindFixed {
		return Date{}, nil
	}
	d, ok := StartDate(e.Answer, s.Req.Today)
	if !ok {
		a := abstainDraft(ReasonPlaceholderEmpty, "", "%s is left for you or not a date: availability is unknown", IDEarliestStartDate)
		return Date{}, &a
	}
	if d, ok = AfterEducation(d, s.Req.Today, s.Req.Facts.Education); !ok {
		a := abstainDraft(ReasonPlaceholderEmpty, "", "an education entry has no end month: availability is unknown")
		return Date{}, &a
	}
	return d, nil
}

// AfterEducation moves a start day later, to the last day of any education
// entry that has not finished; every start lane, and the apply check's
// month-and-year box, reads the day through it, so two questions on one page
// name the same day. It reports false when an entry has no end month, which
// leaves the day unknown.
func AfterEducation(d, today Date, education []EducationFact) (Date, bool) {
	for _, edu := range education {
		end, ok := parseYearMonth(edu.End)
		if !ok {
			return Date{}, false
		}
		if end.key() <= monthOf(today).key() {
			continue
		}
		if last := (Date{end.Year, end.Month, 1}).LastOfMonth(); d.Before(last) {
			d = last
		}
	}
	return d, true
}

// startWindow answers a can-you-start question that names a dated window with
// the control's own Yes when the applicant can start by the window's last day.
// It never answers No: a window that closes first is left to the user, and a
// question inside an entity section is not the applicant's own start.
func startWindow(s *Step) (Draft, bool) {
	q := s.Q
	end, ok := startWindowEnd(q.Text, s.Req.Today)
	if !ok || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	from, abstain := availableFrom(s)
	if abstain != nil {
		return *abstain, true
	}
	if from.IsZero() || end.Before(from) {
		return Draft{}, false
	}
	d := derivedDraft("Yes", "", startProfileDerive,
		fmt.Sprintf("start window: available from %s, window ends %s -> Yes", from.ISO(), end.ISO()))
	d.Deterministic = true
	if hasRenderedOptions(q.Options) {
		yes, ok := ownYesNo(q.Options, true)
		if !ok {
			return Draft{}, false
		}
		d.Answer, d.ChosenLabel, d.HasOption, d.OptionScore = yes.Label, yes.Label, true, 1
	}
	return d, true
}

// periodEnd is the last day an option's period covers: the latest date it
// names, or false when it names none.
func periodEnd(label string) (Date, bool) {
	var best Date
	found := false
	for _, text := range scanDates(label) {
		_, last, ok := dateSpan(text)
		if ok && (!found || best.Before(last)) {
			best, found = last, true
		}
	}
	return best, found
}

// startPeriod answers a which-quarter, month or term question with the earliest
// option whose period has not ended before the applicant can start.
func startPeriod(s *Step) (Draft, bool) {
	q := s.Q
	real := realOptions(q.Options)
	if len(real) < 2 || !whichPeriodRE.MatchString(q.Text) {
		return Draft{}, false
	}
	from, abstain := availableFrom(s)
	if abstain != nil {
		return *abstain, true
	}
	if from.IsZero() {
		return Draft{}, false
	}
	bestIdx, found := -1, false
	var bestEnd Date
	for i, o := range real {
		end, ok := periodEnd(o.Label)
		if !ok || end.Before(from) {
			continue
		}
		if !found || end.Before(bestEnd) {
			bestIdx, bestEnd, found = i, end, true
		}
	}
	if !found {
		return abstainDraft(ReasonInputMismatch, "", "start period: no offered period reaches %s", from.ISO()), true
	}
	o := real[bestIdx]
	d := derivedDraft(o.Label, "", startProfileDerive,
		fmt.Sprintf("start period: available from %s -> earliest period not yet ended: %q", from.ISO(), o.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore, d.Deterministic = o.Label, true, 1, true
	return d, true
}

// A span from today: "In 1 to 3 years", "Less than 2 years", "More than 5 years".
var (
	spanRangeRE = lazyregexp.New(`(?i)^\s*(?:in\s+|within\s+)?(\d+)\s*(?:to|-|[` + dvDash + `])\s*(\d+)\s+(years?|months?)\b`)
	spanBelowRE = lazyregexp.New(`(?i)^\s*(?:in\s+)?(?:less\s+than|under|fewer\s+than)\s+(\d+)\s+(years?|months?)\b`)
	spanAboveRE = lazyregexp.New(`(?i)^\s*(?:in\s+)?(?:(?:more\s+than|over)\s+(\d+)\s+(years?|months?)|(\d+)\s*\+\s*(years?|months?))\b`)
)

// openUpper stands for no upper bound.
const openUpper = 1e9

func spanUnit(u string) float64 {
	if strings.HasPrefix(strings.ToLower(u), "month") {
		return 1.0 / 12
	}
	return 1
}

// spanOfLabel is the [from, to) interval in years a label covers from today.
func spanOfLabel(label string) (from, to float64, ok bool) {
	num := func(s string) float64 { n, _ := strconv.Atoi(s); return float64(n) }
	if m := spanRangeRE.FindStringSubmatch(label); m != nil {
		u := spanUnit(m[3])
		return num(m[1]) * u, (num(m[2]) + 1) * u, true
	}
	if m := spanBelowRE.FindStringSubmatch(label); m != nil {
		return 0, num(m[1]) * spanUnit(m[2]), true
	}
	if m := spanAboveRE.FindStringSubmatch(label); m != nil {
		if m[1] != "" {
			return num(m[1]) * spanUnit(m[2]), openUpper, true
		}
		return num(m[3]) * spanUnit(m[4]), openUpper, true
	}
	return 0, 0, false
}

// storedDateSpan picks the span option that holds a stored future date when
// the classified entry stores a date but its input types reject the picker.
func storedDateSpan(s *Step) (Draft, bool) {
	q := s.Q
	real := realOptions(q.Options)
	if len(real) < 2 || s.Req.Today.IsZero() {
		return Draft{}, false
	}
	spans := 0
	for _, o := range real {
		if _, _, ok := spanOfLabel(o.Label); ok {
			spans++
		}
	}
	if spans < 2 {
		return Draft{}, false
	}
	cx := newClassifier(s.Index, q, newOptionScorer(q.Text, s.Req.bud))
	cl, ok := cx.curated()
	if !ok {
		cl = cx.general()
	}
	e := cl.entry
	if e == nil || !cl.rejected || !e.allowsInput(InputDate) || e.Kind != KindFixed {
		return Draft{}, false
	}
	stored, ok := parseCalendarDate(ownText(e))
	if !ok || !s.Req.Today.Before(stored) {
		return Draft{}, false
	}
	years := float64(s.Req.Today.DaysUntil(stored)) / 365.25
	var hit *Option
	for i, o := range real {
		from, to, ok := spanOfLabel(o.Label)
		if !ok || years < from || years >= to {
			continue
		}
		if hit != nil {
			return Draft{}, false
		}
		hit = &real[i]
	}
	if hit == nil {
		return Draft{}, false
	}
	d := derivedDraft(hit.Label, "", cl.start,
		fmt.Sprintf("stored date %s is %.2f years away -> %q", stored.ISO(), years, hit.Label))
	d.Entry, d.ChosenLabel, d.HasOption, d.OptionScore, d.Deterministic = e, hit.Label, true, 1, true
	return d, true
}
