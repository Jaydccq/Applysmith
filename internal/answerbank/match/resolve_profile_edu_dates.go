package match

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Education date components, the graduation clock, graduation time frames and
// the currently-pursuing question.

// graduated reports an education end at or before the current month: the
// current month counts as done, because by the time anyone asks the term is over.
func graduated(end yearMonth, today Date) bool {
	return end.key() <= monthKey(today.Year, today.Month)
}

var alreadyGraduatedRE = lazyregexp.New(`(?i)\balready\s+graduat|\bhave\s+graduated\b|^\s*graduated\s*\.?\s*$`)

// alreadyGraduatedLabel reports an option that states the applicant has
// graduated; "I have not graduated" does not match.
func alreadyGraduatedLabel(label string) bool { return alreadyGraduatedRE.MatchString(label) }

var (
	educationContextRE = lazyregexp.New(`(?i)\b(?:educat\w*|school|schooling|universit\w*|college|degree|diploma|graduat\w*|alma\s+mater|major|minor|gpa|study|studies|studied|coursework|academic)\b`)
	ageWordRE          = lazyregexp.New(`(?i)\bage\b`)
	eduHowManyRE       = lazyregexp.New(`(?i)\bhow\s+(?:many|much)\b`)
	experienceWordRE   = lazyregexp.New(`(?i)\bexperience\b`)
	monthWordRE        = lazyregexp.New(`(?i)\bmonths?\b`)
	yearWordRE         = lazyregexp.New(`(?i)\byears?\b`)
	// programLengthRE is a program's length ("a 4 year university", "a
	// four-year degree", Rain), never a date part.
	programLengthRE = lazyregexp.New(`(?i)\b(?:\d|two|three|four|five)[\s-]+years?\s+(universit\w*|college|degree|program\w*|school|institution)\b`)
	// yearOfStudyRE is a year of study, a standing ("your current year/level
	// of study", Avride), never a date part; a year of a degree can be its
	// start or end.
	yearOfStudyRE    = lazyregexp.New(`(?i)\byears?\s*(?:(?:/|\bor\b)\s*levels?\s+)?(?:of|in)\s+(?:your\s+)?(?:study|studies|school|college|universit\w*)\b`)
	dayWordRE        = lazyregexp.New(`(?i)\bdays?\b`)
	dateWordRE       = lazyregexp.New(`(?i)\bdate\b`)
	employmentWordRE = lazyregexp.New(`(?i)\b(?:employ\w*|work\w*|job|position|company|occupation)\b`)
	// availableStartRE is the applicant's own start in the role: "What is your
	// anticipated start date? (Month/Year)" (Palantir) took the school's start,
	// "Expected employment start year" the last job's.
	availableStartRE = lazyregexp.New(`(?i)\b(?:anticipated|expected|earliest|desired|preferred|possible|potential|available|ideal|target)\s+(?:employment\s+)?start\b`)
	eduStartRE       = lazyregexp.New(`(?i)\b(?:start|from|began|begin(?:ning)?|commenc\w*)\b`)
	eduEndRE         = lazyregexp.New(`(?i)\b(?:end|to|until|graduat\w*|completion|completed?|finish\w*|conferred|awarded)\b`)
	highSchoolRE     = lazyregexp.New(`(?i)\bhigh\s*school\b|\bsecondary\s*school\b`)
	// graduatedFromRE is "graduated from (a school)": the from names the
	// school, never a start date.
	graduatedFromRE = lazyregexp.New(`(?i)\bgraduat\w*\s+from\b`)
	// graduationDateRE is a label that asks for the date of graduation.
	graduationDateRE = lazyregexp.New(`(?i)\bgraduat\w*\b.*\bdate\b|\bdate\b.*\bgraduat\w*\b`)
	// monthYearLayoutRE is a month-year layout the label states: MM/YYYY,
	// MM-YY or YYYY-MM.
	monthYearLayoutRE = lazyregexp.New(`(?i)\b(?:mm\s*([/.-])\s*(yyyy|yy)|yyyy\s*([/.-])\s*mm)\b`)
	// schoolDateRE is the start or end date of the schooling the label names
	// (Jane Street: "Current degree start date"), never the start in the role.
	schoolDateRE = lazyregexp.New(`(?i)\b(?:degree|education|school|universit\w*|college|studies)\s+(?:start|end|completion)\s+date\b`)
	// dateLabelWordRE is the vocabulary of a bare date-part label.
	dateLabelWordRE = lazyregexp.New(`(?i)\b(?:start(?:ed|ing)?|from|began|begin(?:ning)?|commenc\w*|end(?:ed|ing)?|to|until|completion|completed?|finish\w*|conferred|awarded|attended|dates?|months?|years?|days?|mm|dd|yy|yyyy|please|enter|select|provide|choose|the|of|in|an?|and|or|if|applicable|optional|required|expected|actual)\b`)
)

// isBareControl reports a control that can hold a date component: a date part
// is never a checkbox or a radio group.
func isBareControl(q Question) bool {
	return q.Kind != KindCheckbox && q.Kind != KindRadioGroup
}

// educationDate answers a bare date-component control (start month, end
// year, graduation month and year) that lost its row index, from the
// education entry the question names (educationOfQuestion).
func educationDate(s *Step) (Draft, bool) {
	f := s.Req.Facts
	q := s.Q
	if len(f.Education) == 0 || !isBareControl(q) || isEducationRowControl(q) {
		return Draft{}, false
	}
	text := programLengthRE.ReplaceAllString(q.Text, "$1")
	// A label of date words alone ("End year", "Start date month") is a date
	// part; anything more must name schooling itself: "When can you start?
	// (month and year)" is the start in the role, "Year admitted to the bar"
	// no school date, and "attend" alone a conference or an event.
	if !educationContextRE.MatchString(text) && strings.IndexFunc(dateLabelWordRE.ReplaceAllString(text, ""), unicode.IsLetter) >= 0 {
		return Draft{}, false
	}
	if ageWordRE.MatchString(text) || eduHowManyRE.MatchString(text) || experienceWordRE.MatchString(text) || yearOfStudyRE.MatchString(text) {
		return Draft{}, false
	}
	if availableStartRE.MatchString(text) && !educationContextRE.MatchString(text) {
		return Draft{}, false
	}
	hasMonth, hasYear := monthWordRE.MatchString(text), yearWordRE.MatchString(text)
	// A day counts only inside an explicit date control; "within 30 days" is a
	// duration, never a day of the month.
	hasDay := dayWordRE.MatchString(text) && dateWordRE.MatchString(text)
	// A date control that asks for the graduation date (Ashby's "Graduation
	// Date" picker) takes a whole date; a date the user saved for it stands.
	// A school's start or end date is a whole date in a date control and the
	// month and year in a text box.
	schoolDate := !hasMonth && !hasYear && !hasDay && schoolDateRE.MatchString(text)
	wholeDate := !hasMonth && !hasYear && !hasDay && q.InputType == InputDate && (graduationDateRE.MatchString(text) || schoolDate)
	if schoolDate && !wholeDate {
		hasMonth, hasYear = true, true
	}
	// A graduation date asked in a month-year layout the label states
	// ("please enter in this format MM/YYYY", Adobe, in a text area) is the
	// graduation month written in it.
	layout := monthYearLayoutRE.FindStringSubmatch(text)
	stated := layout != nil && !hasDay && !dateLayoutRE.MatchString(text) && graduationDateRE.MatchString(text)
	if (wholeDate || stated) && ownText(entryNamed(s, "expected-graduation-date")) != "" {
		return Draft{}, false
	}
	if stated && !employmentWordRE.MatchString(text) {
		recent, which, ok := educationOfQuestion(f, text)
		ym, dated := parseYearMonth(recent.End)
		if !ok || !dated {
			return Draft{}, false
		}
		var v string
		switch {
		case layout[3] != "":
			v = fmt.Sprintf("%04d%s%02d", ym.Year, layout[3], ym.Month)
		case strings.EqualFold(layout[2], "yy"):
			v = fmt.Sprintf("%02d%s%02d", ym.Month, layout[1], ym.Year%100)
		default:
			v = fmt.Sprintf("%02d%s%04d", ym.Month, layout[1], ym.Year)
		}
		d := derivedDraft(v, "", startProfileDate, "education-date: "+which+" end in the stated layout -> "+strconv.Quote(v))
		d.ChosenLabel = v
		d.Deterministic = true
		return d, true
	}
	if !hasMonth && !hasYear && !hasDay && !wholeDate {
		return Draft{}, false
	}
	if employmentWordRE.MatchString(text) {
		return Draft{}, false
	}
	isStart, isEnd := eduStartRE.MatchString(graduatedFromRE.ReplaceAllString(text, "graduated")), eduEndRE.MatchString(text)
	if isStart == isEnd {
		return Draft{}, false
	}
	if highSchoolRE.MatchString(text) {
		return highSchoolDate(f, hasMonth || hasDay || wholeDate, isEnd), true
	}
	recent, which, ok := educationOfQuestion(f, text)
	if !ok {
		return Draft{}, false
	}
	stamp := recent.Start
	dir := "start"
	if isEnd {
		stamp, dir = recent.End, "end"
	}
	ym, hasStamp := parseYearMonth(stamp)
	if wholeDate {
		if !hasStamp {
			return Draft{}, false
		}
		// The profile records months, so the day is the first of the month.
		v := fmt.Sprintf("%04d-%02d-01", ym.Year, ym.Month)
		d := derivedDraft(v, "", startProfileDate, "education-date: "+which+" "+dir+" -> "+strconv.Quote(v))
		d.ChosenLabel = v
		d.Deterministic = true
		return d, true
	}
	if hasDay || (hasMonth && hasYear) {
		return educationMonthYear(s, ym, hasStamp, which+" "+dir, hasDay && !hasMonth && !hasYear)
	}
	if !hasStamp {
		return Draft{}, false
	}
	return educationComponent(s, ym, which+" "+dir, hasMonth)
}

// graduateVerbRE is "graduate" as a verb ("did you graduate", "expect to
// graduate"), which names no graduate degree.
var graduateVerbRE = lazyregexp.New(`(?i)\b(?:you|to|will|did|i)\s+graduate\b`)

// educationOfQuestion is the entry a date question names: the bachelor entry
// for undergraduate wording, the master or doctorate entry for graduate
// wording (Turbo AI: "What year did you graduate with your bachelor's
// degree?" is the bachelor's year), otherwise the most recent. A level no
// entry holds names none.
func educationOfQuestion(f Facts, text string) (EducationFact, string, bool) {
	rows := orderedEducation(f)
	if len(rows) == 0 {
		return EducationFact{}, "", false
	}
	under, grad := levelScope(text)
	switch {
	case under && !grad:
		e, ok := educationOfScope(rows, true, false)
		return e, "undergraduate entry", ok
	case grad && !under:
		e, ok := educationOfScope(rows, false, doctoralScope(text))
		return e, "graduate entry", ok
	}
	return rows[0], "most recent entry", true
}

// highSchoolDate answers a high-school graduation year only from an education
// row that names high school; a month (also inside a whole date or beside a
// day), or no such row, abstains.
func highSchoolDate(f Facts, month, end bool) Draft {
	if month {
		return abstainDraft(ReasonPlaceholderEmpty, "", "education-date: a high-school month is not recorded -> abstain")
	}
	for _, e := range f.Education {
		if !highSchoolRE.MatchString(e.School + " " + e.Degree + " " + e.Field) {
			continue
		}
		stamp := e.Start
		if end {
			stamp = e.End
		}
		if ym, ok := parseYearMonth(stamp); ok {
			d := derivedDraft(strconv.Itoa(ym.Year), "", startProfileDate, "education-date: high-school entry names the year")
			d.ChosenLabel = d.Answer
			d.Deterministic = true
			return d
		}
	}
	return abstainDraft(ReasonPlaceholderEmpty, "", "education-date: no education entry names high school -> abstain")
}

// educationMonthYear answers a control that holds more than one component: a
// month and year ("May 2026") or a day. The day is always the first of the
// month, because the profile records months. A month-and-year option list is
// narrowed to the options naming the user's year; a list without it abstains
// rather than commit the right month in a different year.
func educationMonthYear(s *Step, ym yearMonth, ok bool, dir string, dayOnly bool) (Draft, bool) {
	if !ok {
		return Draft{}, false
	}
	wanted := MonthName(ym.Month) + " " + strconv.Itoa(ym.Year)
	if dayOnly {
		wanted = "1"
	}
	trace := "education-date: " + dir + " -> " + strconv.Quote(wanted)
	opts := realOptions(s.Q.Options)
	if len(opts) == 0 {
		d := derivedDraft(wanted, "", startProfileDate, trace)
		d.ChosenLabel = wanted
		d.Deterministic = true
		return d, true
	}
	if !dayOnly {
		year := strconv.Itoa(ym.Year)
		var kept []Option
		for _, o := range opts {
			if containsWord(o.Label, year) {
				kept = append(kept, o)
			}
		}
		if len(kept) == 0 {
			return abstainDraft(ReasonInputMismatch, "", "%s; no option offers the year %s -> abstain rather than pick a different date", trace, year), true
		}
		opts = kept
	}
	b, found := bindOption(s.bareScorer(), opts, wanted)
	if !found || b.Score < rowMatchFloor {
		return Draft{}, false
	}
	d := derivedDraft(b.Label, "", startProfileDate, trace+"; chose option "+strconv.Quote(b.Label))
	d.ChosenLabel = b.Label
	d.Deterministic = true
	return d, true
}

// educationComponent answers a single month or year component.
func educationComponent(s *Step, ym yearMonth, dir string, month bool) (Draft, bool) {
	value := strconv.Itoa(ym.Year)
	if month {
		value = MonthName(ym.Month)
	}
	trace := "education-date: " + dir + " -> " + strconv.Quote(value)
	opts := realOptions(s.Q.Options)
	if len(opts) > 0 {
		var chosen string
		if month {
			if o, ok := sameMonthOption(opts, ym.Month); ok {
				chosen = o.Label
			}
		}
		if chosen == "" {
			if b, ok := bindOption(s.bareScorer(), opts, value); ok && b.Score >= rowMatchFloor {
				chosen = b.Label
			}
		}
		if chosen == "" {
			return Draft{}, false
		}
		value = chosen
	}
	d := derivedDraft(value, "", startProfileDate, trace)
	d.ChosenLabel = value
	d.Deterministic = true
	return d, true
}

// season ranges: winter runs from December of the year into February of the next.
var seasonMonths = map[string]struct{ from, to, toYearOffset int }{
	"winter": {12, 2, 1}, "spring": {3, 5, 0}, "summer": {6, 8, 0}, "fall": {9, 11, 0}, "autumn": {9, 11, 0},
}

var (
	// seasonTokenRE is a season and its year; groups 3 and 4 are the months the
	// label states for it ("Fall 2026 (September-December)").
	seasonTokenRE = lazyregexp.New(`(?i)\b(winter|spring|summer|fall|autumn)\s+(\d{4})\b(?:\s*\(\s*(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s*(?:-|\x{2013}|\x{2014}|to|through)\s*(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s*\))?`)
	monthTokenRE  = lazyregexp.New(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{4})\b`)
	// quarterTokenRE is a calendar quarter ("Q4 2026"): its three months.
	quarterTokenRE = lazyregexp.New(`(?i)\bQ([1-4])\s+(\d{4})\b`)
	// monthSpanRE is a span of months that names its year once ("April - June
	// 2026"): the first month takes the year, or the year before when it comes
	// later in the calendar than the last ("November - February 2027").
	monthSpanRE = lazyregexp.New(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s*(?:-|\x{2013}|\x{2014}|to|through)\s*(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{4})\b`)
	// "Before" and "after" leave the term they name out of the range; "or
	// earlier" and "or later" keep it.
	beforeRE    = lazyregexp.New(`(?i)\bbefore\b|\bprior\s+to\b`)
	afterRE     = lazyregexp.New(`(?i)\bafter\b|\bbeyond\b|\blater\s+than\b`)
	orEarlierRE = lazyregexp.New(`(?i)\bor\s+earlier\b`)
	orLaterRE   = lazyregexp.New(`(?i)\bor\s+later\b`)
	// graduationWhenRE asks in a text box when the applicant graduates or
	// graduated; graduateStudiesRE is graduate school, not graduating.
	graduationWhenRE  = lazyregexp.New(`(?i)\bwhen\s+(?:do|will|did)\s+you\s+(?:expect\s+to\s+|anticipate\s+(?:that\s+)?you\s+will\s+|plan\s+to\s+)?graduate\b|\bwhen\s+(?:do\s+you\s+)?(?:expect|anticipate)\s+(?:that\s+)?(?:you\s+will\s+|to\s+)?graduate\b|\b(?:expected|anticipated)\s+(?:date\s+of\s+)?graduation\b`)
	graduateStudiesRE = lazyregexp.New(`(?i)\bgraduate\s+(?:school|programs?|degrees?|studies|study|level|students?|courses?|assistants?)\b`)
	timeFrameRE       = lazyregexp.New(`(?i)\bgraduation\s+(?:time\s*-?\s*frames?|date|timeline|term|semester|period)\b|\b(?:when|expected|anticipated)\b[^?.]*\bgraduat|\bhow\s+(?:far|close|long)\b[^?.]*\bgraduat`)
)

// monthRange is an inclusive range of month keys.
type monthRange struct{ from, to int }

// parseSeasonBracket reads the season, month and quarter tokens of an option
// label into one inclusive month range; a season is the months the label
// states for it, else its own. "Or earlier" opens the low end and "or later"
// the high end; "before" and "prior to" are every month before the range,
// "after", "later than" and "beyond" every month after it. A label without a
// dated token, or with both a before and an after, is not a bracket.
func parseSeasonBracket(label string) (monthRange, bool) {
	var all []monthRange
	for _, m := range seasonTokenRE.FindAllStringSubmatch(label, -1) {
		year, _ := strconv.Atoi(m[2])
		sm := seasonMonths[strings.ToLower(m[1])]
		if m[3] != "" {
			sm.from, sm.to, sm.toYearOffset = monthFromWord(m[3]), monthFromWord(m[4]), 0
			if sm.from > sm.to {
				sm.toYearOffset = 1
			}
		}
		all = append(all, monthRange{monthKey(year, sm.from), monthKey(year+sm.toYearOffset, sm.to)})
	}
	for _, m := range monthTokenRE.FindAllStringSubmatch(label, -1) {
		year, _ := strconv.Atoi(m[2])
		k := monthKey(year, monthFromWord(m[1]))
		all = append(all, monthRange{k, k})
	}
	for _, m := range quarterTokenRE.FindAllStringSubmatch(label, -1) {
		year, _ := strconv.Atoi(m[2])
		q, _ := strconv.Atoi(m[1])
		all = append(all, monthRange{monthKey(year, 3*q-2), monthKey(year, 3*q)})
	}
	for _, m := range monthSpanRE.FindAllStringSubmatch(label, -1) {
		year, _ := strconv.Atoi(m[3])
		from, to := monthFromWord(m[1]), monthFromWord(m[2])
		if from > to {
			year--
		}
		k := monthKey(year, from)
		all = append(all, monthRange{k, k})
	}
	if len(all) == 0 {
		return monthRange{}, false
	}
	r := monthRange{from: math.MaxInt, to: math.MinInt}
	for _, t := range all {
		r.from, r.to = min(r.from, t.from), max(r.to, t.to)
	}
	switch before, after := beforeRE.MatchString(label), afterRE.MatchString(label); {
	case before && after:
		return monthRange{}, false
	case before:
		return monthRange{math.MinInt, r.from - 1}, true
	case after:
		return monthRange{r.to + 1, math.MaxInt}, true
	}
	if orEarlierRE.MatchString(label) {
		r.from = math.MinInt
	}
	if orLaterRE.MatchString(label) {
		r.to = math.MaxInt
	}
	return r, true
}

// bareYearRE is an option that names a year and nothing else ("2027", "2030
// or later"): a graduation-year picker's term.
var bareYearRE = lazyregexp.New(`(?i)^\W*((?:19|20)\d{2})(?:\s+or\s+(?:later|earlier))?\W*$`)

// graduationTerm is the month range of a graduation picker's option: a
// season, month or quarter bracket, or a year alone, January to December.
func graduationTerm(label string) (monthRange, bool) {
	if r, ok := parseSeasonBracket(label); ok {
		return r, true
	}
	m := bareYearRE.FindStringSubmatch(label)
	if m == nil {
		return monthRange{}, false
	}
	year, _ := strconv.Atoi(m[1])
	r := monthRange{monthKey(year, 1), monthKey(year, 12)}
	if orEarlierRE.MatchString(label) {
		r.from = math.MinInt
	}
	if orLaterRE.MatchString(label) {
		r.to = math.MaxInt
	}
	return r, true
}

// graduationTimeFrame answers a "when do you graduate" picker: the option that
// states the applicant has already graduated, or the one saying no study is in
// progress, once the latest education end is in the current month or earlier,
// otherwise the option whose season or month bracket contains the end. An
// entry without an end month may be in progress, so it abstains. Asked in a
// text box, it is the month and year the named degree ends
// (educationOfQuestion).
func graduationTimeFrame(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	if t := s.Q.Text; len(opts) == 0 && s.Q.Kind == KindText && graduationWhenRE.MatchString(t) && !highSchoolRE.MatchString(t) && !graduateStudiesRE.MatchString(t) {
		recent, which, ok := educationOfQuestion(s.Req.Facts, t)
		if !ok {
			return Draft{}, false
		}
		ym, ok := parseYearMonth(recent.End)
		return educationMonthYear(s, ym, ok, which+" end", false)
	}
	if len(opts) < 2 || !timeFrameRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	// A Yes and a No hold no time frame: the question states its own (degreeInField).
	if _, yes := ownYesNo(opts, true); yes && len(opts) == 2 {
		if _, no := ownYesNo(opts, false); no {
			return Draft{}, false
		}
	}
	var grad yearMonth
	for _, e := range s.Req.Facts.Education {
		ym, ok := parseYearMonth(e.End)
		if !ok {
			return abstainDraft(ReasonPlaceholderEmpty, "", "graduation-time-frame: %q has no end month, so it cannot be dated -> abstain", e.Degree), true
		}
		if ym.key() > grad.key() {
			grad = ym
		}
	}
	if grad.isZero() {
		return abstainDraft(ReasonPlaceholderEmpty, "", "graduation-time-frame: no education end date is recorded -> abstain"), true
	}
	emit := func(o Option, why string) (Draft, bool) {
		d := derivedDraft(o.Label, "graduation_time_frame", startProfileDerive, "graduation-time-frame: "+why+" -> "+strconv.Quote(o.Label))
		d.ChosenLabel = o.Label
		d.Deterministic = true
		return d, true
	}
	gradName := strconv.Itoa(grad.Year) + "-" + strconv.Itoa(grad.Month)
	if graduated(grad, s.Req.Today) {
		for _, o := range opts {
			if alreadyGraduatedLabel(o.Label) || notPursuingRE.MatchString(o.Label) {
				return emit(o, "graduation "+gradName+" is in the past")
			}
		}
	}
	for _, o := range opts {
		if r, ok := graduationTerm(o.Label); ok && grad.key() >= r.from && grad.key() <= r.to {
			return emit(o, "graduation "+gradName+" falls in the bracket")
		}
	}
	// The owner's rule: an applicant who has graduated, asked by a picker whose
	// every term is still ahead (Decagon: "Fall 2026" onwards for a May 2026
	// graduate; SeatGeek: 2027 to 2030), takes the list's "Other" when it has
	// one, else the earliest term. A term that reaches back to the graduation
	// leaves the question to the user.
	if graduated(grad, s.Req.Today) {
		best, from := -1, math.MaxInt
		for i, o := range opts {
			r, ok := graduationTerm(o.Label)
			if !ok {
				continue
			}
			if r.from <= grad.key() {
				best = -1
				break
			}
			if r.from < from {
				best, from = i, r.from
			}
		}
		if best >= 0 {
			if i := slices.IndexFunc(opts, func(o Option) bool { return otherOptionRE.MatchString(o.Label) }); i >= 0 {
				return emit(opts[i], "graduation "+gradName+" is past and every term is ahead: Other")
			}
			return emit(opts[best], "graduation "+gradName+" is past and every term is ahead: the earliest")
		}
	}
	return abstainDraft(ReasonInputMismatch, "", "graduation-time-frame: no option covers graduation %s -> abstain", gradName), true
}

// graduationChangeRE asks how likely the graduation date is to change (Two
// Sigma's "Likelihood of Graduation Change", Likely or Unlikely).
var (
	graduationChangeRE = lazyregexp.New(`(?i)\blikel(?:ihood|y)\b.{0,40}\bgraduat\w*.{0,40}\bchang|\bgraduat\w*.{0,40}\bchang\w*.{0,40}\blikel`)
	unlikelyRE         = lazyregexp.New(`(?i)^\W*(very\s+)?unlikely\W*$`)
)

// graduationChange answers how likely the graduation date is to change: once
// every education entry is dated and has ended, the graduation is a fact that
// cannot change, so the option reading Unlikely (Very unlikely when offered). A
// graduation still ahead is the user's to judge.
func graduationChange(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	ed := s.Req.Facts.Education
	if len(opts) < 2 || len(ed) == 0 || !graduationChangeRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	if slices.ContainsFunc(ed, func(e EducationFact) bool {
		end, ok := parseYearMonth(e.End)
		return !ok || !graduated(end, s.Req.Today)
	}) {
		return Draft{}, false
	}
	pick := -1
	for i, o := range opts {
		if m := unlikelyRE.FindStringSubmatch(o.Label); m != nil && (pick < 0 || m[1] != "") {
			pick = i
		}
	}
	if pick < 0 {
		return Draft{}, false
	}
	o := opts[pick]
	d := derivedDraft(o.Label, "graduation_change", startProfileDerive, "graduation-change: every education entry has ended -> "+strconv.Quote(o.Label))
	d.ChosenLabel = o.Label
	d.Deterministic = true
	return d, true
}

// graduationWindowRE asks whether the applicant graduates inside a window
// (Agave's "Is your graduation date between November 2026 and June 2027?");
// groups 1 and 2 are its ends. graduationTermsRE asks it of two terms (Scale
// AI: "I confirm that my graduation date will be either Fall 2026 or Spring
// 2027"); groups 1 and 2 are the terms.
var (
	graduationWindowRE = lazyregexp.New(`(?i)^\W*(?:is\s+your\s+(?:expected\s+|anticipated\s+)?graduation(?:\s+date)?|(?:will\s+you|do\s+you\s+expect\s+to)\s+graduate)\s+(?:between|from)\s+([a-z]+\.?\s+\d{4})\s+(?:and|to|through|-)\s+([a-z]+\.?\s+\d{4})\s*[?.!*]*$`)
	graduationTermsRE  = lazyregexp.New(`(?i)^\W*(?:i\s+confirm\s+that\s+)?(?:my\s+(?:expected\s+|anticipated\s+)?graduation(?:\s+date)?\s+(?:will\s+be|is)|is\s+your\s+(?:expected\s+|anticipated\s+)?graduation(?:\s+date)?|will\s+you\s+graduate)\s+(?:either\s+)?(?:in\s+)?((?:fall|autumn|spring|summer|winter)\s+\d{4})\s+or\s+(?:in\s+)?((?:fall|autumn|spring|summer|winter)\s+\d{4})\s*[?.!*]*$`)
)

// graduationWindow answers a Yes/No graduation-window question from the latest
// education end: Yes inside the window, or inside either term, No outside it.
// An entry still open with no end month leaves it to the user.
func graduationWindow(s *Step) (Draft, bool) {
	q := s.Q
	text := strings.TrimSpace(q.Text)
	m, terms := graduationWindowRE.FindStringSubmatch(text), false
	if m == nil {
		m, terms = graduationTermsRE.FindStringSubmatch(text), true
	}
	if m == nil || isEducationRowControl(q) {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	from, okF := parseSeasonBracket(m[1])
	to, okT := parseSeasonBracket(m[2])
	recent, okR := mostRecentEducation(s.Req.Facts)
	end, okE := parseYearMonth(recent.End)
	if !okY || !okN || !okF || !okT || !okR || !okE {
		return Draft{}, false
	}
	pick, where, span := no, "outside", m[1]+" to "+m[2]
	in := end.key() >= from.from && end.key() <= to.to
	if terms {
		in, span = end.key() >= from.from && end.key() <= from.to || end.key() >= to.from && end.key() <= to.to, m[1]+" or "+m[2]
	}
	if in {
		pick, where = yes, "inside"
	}
	d := derivedDraft(pick.Label, "", startEducation, "graduation window: the latest education end "+recent.End+" is "+where+" "+span+" -> "+quoteTerm(pick.Label))
	d.ChosenLabel = pick.Label
	d.Deterministic = true
	return d, true
}

var (
	// pursuingPickRE asks which degree is in progress with the degree first
	// ("Select the degree program you are currently pursuing").
	pursuingPickRE = lazyregexp.New(`(?i)\b(?:degree|program(?:me)?|level\s+of\s+(?:education|study))\b[^?!.]*\b(?:currently|presently|now)\s+(?:pursuing|enrolled\s+in|studying)\b|` +
		// Decagon: "What degree are you pursuing?"
		`\b(?:what|which)\s+(?:degree|program(?:me)?|level\s+of\s+(?:education|study))\s+are\s+you\s+pursuing\b`)
	nowWordRE = lazyregexp.New(`(?i)\b(?:currently|presently|now)\b`)
	// saysNoStudyRE is an option that speaks of not studying in any words
	// ("I do not have and am not working toward a degree").
	saysNoStudyRE = lazyregexp.New(`(?i)\bnot\b[^,.;]*\b(?:pursu\w*|enroll\w*|attend\w*|stud\w*|working\s+toward\w*|in\s+school)\b`)
	// currentLevelRE asks the applicant's level of education now; it often
	// means the highest level attained, so it is read here only over options
	// that state enrollment.
	currentLevelRE = lazyregexp.New(`(?i)\bcurrent\s+(?:level\s+of\s+(?:education|study)|education(?:al)?\s+(?:level|status))\b`)
	// notPursuingRE is the option saying no degree is in progress.
	notPursuingRE = lazyregexp.New(`(?i)\bnot\s+(?:currently\s+)?(?:pursuing|enrolled|attending|studying|in\s+school)\b`)
	// highestInsteadRE asks for the highest degree when none is in progress
	// (P&G: "(Or if not currently working towards a degree, what is your
	// highest degree obtained?)").
	highestInsteadRE = lazyregexp.New(`(?i)\bif\s+(?:you\s+are\s+)?not\b[^?]*\bhighest\s+(?:degree|level)\b`)
	// enrolledOptionRE is an option that states current enrollment: a class
	// year, or being in a program now.
	enrolledOptionRE = lazyregexp.New(`(?i)\b(?:currently|presently)\b|` + classYear)
	classYearRE      = lazyregexp.New(`(?i)` + classYear)
	// catchAllRE is the option for every case the list does not name.
	catchAllRE = lazyregexp.New(`(?i)^\s*(?:other|none(?:\s+of\s+the\s+above)?)\b`)
	// completedOptionRE is an option stating a degree completed ("I have
	// completed my undergraduate degree": Zip).
	completedOptionRE = lazyregexp.New(`(?i)\b(?:completed|graduated|finished|earned|obtained)\b`)
)

const classYear = `\b(?:freshman|sophomore|junior|senior)\b|\bfirst[\s-]year\b`

// completedLevelOption reports an option that states a degree completed at a
// level an ended entry of the profile holds. An option bounded by a window
// ("within the past 6 months") is the student-status lane's.
func completedLevelOption(f Facts, today Date, label string) bool {
	l := degreeLevel(label)
	if l == "" || !completedOptionRE.MatchString(label) || recentDegreeOptRE.MatchString(label) {
		return false
	}
	return slices.ContainsFunc(f.Education, func(e EducationFact) bool {
		end, ok := parseYearMonth(e.End)
		return ok && graduated(end, today) && degreeNameLevel(e.Degree) == l
	})
}

// pursuingLevel answers a picker asked about the degree in progress: the one
// option of the level of the entry that has not ended; when every entry has
// ended, the rung of the highest completed degree if the question asks for it
// then, else the one option saying no degree is pursued, or else the one
// catch-all ("Other") or the one option stating a degree the profile completed
// when every other option states enrollment. The user's own pick in
// current-level-education-pursuing stands; an entry without an end month, two
// levels in progress, a class year to work out, or not exactly one such
// option abstains.
func pursuingLevel(s *Step) (Draft, bool) {
	f := s.Req.Facts
	q := s.Q.Text
	if len(f.Education) == 0 || ownText(entryNamed(s, "current-level-education-pursuing")) != "" {
		return Draft{}, false
	}
	opts := realOptions(s.Q.Options)
	levels, enrolled, classYears := 0, 0, false
	for _, o := range opts {
		if degreeLevel(o.Label) != "" {
			levels++
		}
		if enrolledOptionRE.MatchString(o.Label) && !notPursuingRE.MatchString(o.Label) {
			enrolled++
		}
		classYears = classYears || classYearRE.MatchString(o.Label)
	}
	asksPursuit := currentlyPursuingRE.MatchString(q) || pursuingPickRE.MatchString(q)
	if !(asksPursuit && levels >= 2 || currentLevelRE.MatchString(q) && enrolled >= 2) {
		return Draft{}, false
	}
	level := ""
	for _, e := range f.Education {
		end, ok := parseYearMonth(e.End)
		if !ok {
			return abstainDraft(ReasonPlaceholderEmpty, "", "pursuing-level: %q has no end month, so it cannot be dated -> abstain", e.Degree), true
		}
		if graduated(end, s.Req.Today) {
			continue
		}
		l := degreeNameLevel(e.Degree)
		if l == "" || level != "" && l != level {
			return abstainDraft(ReasonAmbiguous, "", "pursuing-level: the degrees in progress have no single level -> abstain"), true
		}
		level = l
	}
	var picks []Option
	why := level + " is in progress"
	switch {
	case level == "" && highestInsteadRE.MatchString(q):
		user, ok := userEducationLevel(f, s.Req.Today)
		if !ok {
			return abstainDraft(ReasonPlaceholderEmpty, "", "pursuing-level: no degree is in progress and none is completed -> abstain"), true
		}
		rung, trace, ok := pickEducationRung(s.Q.Options, user, nil)
		if !ok {
			return abstainDraft(ReasonAmbiguous, "", "pursuing-level: no degree is in progress; %s -> abstain", trace), true
		}
		d := derivedDraft(rung.Label, "current_level_education_pursuing", startProfileDerive, "pursuing-level: no degree is in progress and the question asks the highest then; "+trace)
		d.ChosenLabel = rung.Label
		d.Deterministic = true
		return d, true
	case level == "":
		why = "no degree is in progress"
		for _, o := range opts {
			if notPursuingRE.MatchString(o.Label) {
				picks = append(picks, o)
			}
		}
		if len(picks) == 0 && enrolled == len(opts)-1 {
			for _, o := range opts {
				if catchAllRE.MatchString(o.Label) || completedLevelOption(f, s.Req.Today, o.Label) {
					picks = append(picks, o)
				}
			}
			why += " and every other option states enrollment"
		}
		// Owner decision, of the earliest graduation-term rule's class: a
		// list of degree levels with no option for not studying, asked of a
		// graduate, takes the degree just completed. A question that asks
		// about now ("currently") is not this.
		if recent, ok := mostRecentEducation(f); ok && len(picks) == 0 && enrolled == 0 && !nowWordRE.MatchString(q) &&
			!slices.ContainsFunc(opts, func(o Option) bool { return saysNoStudyRE.MatchString(o.Label) }) {
			if l := degreeNameLevel(recent.Degree); l != "" {
				for _, o := range opts {
					if degreeLevel(o.Label) == l {
						picks = append(picks, o)
					}
				}
				why += ", the list has no option saying so, and the degree just completed is " + strconv.Quote(recent.Degree)
			}
		}
	case classYears:
		return abstainDraft(ReasonAmbiguous, "", "pursuing-level: %s and the options ask the class year -> abstain", why), true
	default:
		for _, o := range opts {
			if degreeLevel(o.Label) == level {
				picks = append(picks, o)
			}
		}
	}
	if len(picks) != 1 {
		return abstainDraft(ReasonAmbiguous, "", "pursuing-level: %s and %d options say so -> abstain", why, len(picks)), true
	}
	d := derivedDraft(picks[0].Label, "current_level_education_pursuing", startProfileDerive, "pursuing-level: "+why+" -> "+strconv.Quote(picks[0].Label))
	d.ChosenLabel = picks[0].Label
	d.Deterministic = true
	return d, true
}

var currentlyPursuingRE = lazyregexp.New(`(?i)\b(?:currently|presently|now)\s+(?:pursuing|studying\s+(?:for|towards?)|working\s+(?:on|towards?)|completing)\b[^?!.]*?\b(?:degree|program(?:me)?|master'?s?|bachelor'?s?|ph\.?\s?d|doctorate)\b`)

// pursuingTailRE is what may follow the degree a currently-pursuing question
// names: more levels, "degree" or "program", and the end of the sentence.
var pursuingTailRE = lazyregexp.New(`(?i)^(?:['\x{2019}]s\b|[\s,/]+|\b(?:or|and|an?|degrees?|programs?|programmes?|masters?|bachelors?|ph\.?\s?d|doctorate|doctoral|associates?)\b\.?)*[\s?.!:*)]*$`)

// currentlyPursuing answers "Are you currently pursuing a Master's degree?" on
// a Yes/No control against the clock: Yes when an entry of a named level (any
// level for a bare "a degree") ends after the current month, No when every
// such entry has ended or none exists, and an abstain when an end is unknown.
// A question that goes on after the degree ("in Mechanical Engineering", "and
// graduating between ...") asks more than the dates hold and is left.
func currentlyPursuing(s *Step) (Draft, bool) {
	f := s.Req.Facts
	loc := currentlyPursuingRE.FindStringIndex(s.Q.Text)
	if len(f.Education) == 0 || loc == nil || !pursuingTailRE.MatchString(s.Q.Text[loc[1]:]) {
		return Draft{}, false
	}
	yes, okY := ownYesNo(s.Q.Options, true)
	no, okN := ownYesNo(s.Q.Options, false)
	if !okY || !okN {
		return Draft{}, false
	}
	levels := degreeLevels(s.Q.Text)
	var ongoing *EducationFact
	for i := range f.Education {
		e := &f.Education[i]
		if len(levels) > 0 && !slices.Contains(levels, degreeNameLevel(e.Degree)) {
			continue
		}
		end, ok := parseYearMonth(e.End)
		if !ok {
			return abstainDraft(ReasonPlaceholderEmpty, "", "currently-pursuing: %q has no end month, so it cannot be dated -> abstain", e.Degree), true
		}
		if !graduated(end, s.Req.Today) && ongoing == nil {
			ongoing = e
		}
	}
	chosen, why := no, "no matching degree is still in progress"
	if ongoing != nil {
		chosen, why = yes, strconv.Quote(ongoing.Degree)+" has not ended"
	}
	d := derivedDraft(chosen.Label, "currently_pursuing_degree", startProfileDerive, "currently-pursuing: "+why+" -> "+strconv.Quote(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}

// schoolCoopRE asks whether the applicant comes through a school's co-op
// program (Mirakl: "Are you applying to this opportunity as a Northeastern
// Co-op?"): group 1 or 2 the school's name, which opens with a capital and
// holds none of the words of coopNoSchoolRE ("seeking a", "enrolled in a").
var (
	schoolCoopRE   = lazyregexp.New(`(?i)^\W*(?:are\s+you\s+applying\b[^?]{0,40}?\bas\s+(?:an?\s+)?((?-i:[A-Z])[\w.&'-]*(?:\s+[a-z][\w.&'-]*){0,3}?)|are\s+you\s+(?:an?\s+)?((?-i:[A-Z])[\w.&'-]*(?:\s+[a-z][\w.&'-]*){0,3}?))\s+co-?op(?:\s+(?:student|candidate))?\W*$`)
	coopNoSchoolRE = lazyregexp.New(`(?i)\b(?:an?|the|this|that|your|our|in|on|for|to|seeking|looking|interested|currently|current|enrolled|eligible|available|participating|pursuing|completing)\b`)
)

// schoolCoop answers such a Yes/No No when no education in progress is at the
// named school: a co-op is a program of the school one attends. A school any
// education names, or an education without an end month, leaves it to the
// user.
func schoolCoop(s *Step) (Draft, bool) {
	f := s.Req.Facts
	m := schoolCoopRE.FindStringSubmatch(s.Q.Text)
	if len(f.Education) == 0 || m == nil {
		return Draft{}, false
	}
	school := strings.TrimSpace(m[1] + m[2])
	no, ok := ownYesNo(s.Q.Options, false)
	if school == "" || !ok || coopNoSchoolRE.MatchString(school) {
		return Draft{}, false
	}
	if _, okY := ownYesNo(s.Q.Options, true); !okY {
		return Draft{}, false
	}
	for _, e := range f.Education {
		if _, dated := parseYearMonth(e.End); !dated || containsWord(e.School, school) {
			return Draft{}, false
		}
	}
	d := derivedDraft(no.Label, "school_coop", startProfileDerive, "school co-op: no education in progress is at "+strconv.Quote(school)+" -> "+strconv.Quote(no.Label))
	d.ChosenLabel = no.Label
	d.Deterministic = true
	return d, true
}

var (
	// recentGraduateRE is "graduated within the past N months" (P&G).
	recentGraduateRE = lazyregexp.New(`(?i)\bgraduated\s+(?:with)?in\s+the\s+(?:past|last)\s+(\d{1,2})\s+months?\b`)
	// activeStudentRE is the other half of the disjunction, "an active student".
	activeStudentRE = lazyregexp.New(`(?i)\bor\b[^?]*\b(?:an?\s+)?(?:active|current|currently\s+enrolled|enrolled)\s+student\b`)
)

// recentGraduateOrStudent answers "Have you either graduated within the past N
// months, or are you an active student?" from the education history: Yes when
// a degree has not ended or ended within the N months, No when every degree
// ended earlier. An entry without an end month proves neither, so without
// such a Yes it abstains.
func recentGraduateOrStudent(s *Step) (Draft, bool) {
	m := recentGraduateRE.FindStringSubmatch(s.Q.Text)
	if m == nil || !activeStudentRE.MatchString(s.Q.Text) || len(s.Req.Facts.Education) == 0 {
		return Draft{}, false
	}
	yes, okY := ownYesNo(s.Q.Options, true)
	no, okN := ownYesNo(s.Q.Options, false)
	if !okY || !okN {
		return Draft{}, false
	}
	window, _ := strconv.Atoi(m[1])
	today := monthKey(s.Req.Today.Year, s.Req.Today.Month)
	chosen, why := no, "every degree ended more than "+m[1]+" months ago"
	var undated *EducationFact
	for i, e := range s.Req.Facts.Education {
		end, ok := parseYearMonth(e.End)
		if !ok {
			undated = &s.Req.Facts.Education[i]
			continue
		}
		if !graduated(end, s.Req.Today) || today-end.key() <= window {
			chosen, why, undated = yes, strconv.Quote(e.Degree)+" ends "+e.End, nil
			break
		}
	}
	if undated != nil {
		return abstainDraft(ReasonPlaceholderEmpty, "", "recent-graduate-or-student: %q has no end month, so it cannot be dated -> abstain", undated.Degree), true
	}
	d := derivedDraft(chosen.Label, "recent_graduate_or_student", startProfileDerive, "recent-graduate-or-student: "+why+" -> "+strconv.Quote(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}

var (
	// statusAskRE asks which statement describes the applicant's standing
	// (Adobe: "Which one of the following best describes your current status?").
	statusAskRE = lazyregexp.New(`(?i)\bbest\s+describes\s+(?:your|you)\b|\byour\s+current\s+(?:student\s+|academic\s+|education(?:al)?\s+)?status\b`)
	// recentDegreeOptRE is an option stating a degree completed within N months.
	recentDegreeOptRE = lazyregexp.New(`(?i)\b(?:completed|graduated|finished|earned)\b[^.;]*?\bwithin\s+the\s+(?:past|last)\s+(\d{1,2})\s+months?\b`)
	// doctoralWindowRE is the longer window the option grants a doctorate
	// ("(24 months for PhD graduates)").
	doctoralWindowRE = lazyregexp.New(`(?i)\b(\d{1,2})\s+months?\s+for\s+(?:ph\.?\s?d|doctoral)\b`)
	// enrolledNowOptRE is an option stating enrollment now.
	enrolledNowOptRE = lazyregexp.New(`(?i)\b(?:currently|presently)\s+(?:enrolled|pursuing|studying)\b`)
)

// studentStatus answers a pick of the applicant's standing whose options state
// enrollment or a degree completed within N months (Adobe): the one enrolled
// option of the study in progress while a degree has not ended, else the completed option when the most recent
// degree ended inside its window. Any other case is not claimed: the list's
// other options (an internship with the company) are facts the profile does
// not hold, and a list with fewer than two options about study (one enrolled
// option among visa statuses) asks something else. An undated entry abstains.
func studentStatus(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	if len(opts) < 2 || len(s.Req.Facts.Education) == 0 || !statusAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var enrolled []Option
	var recent *Option
	window, doctoral, study := 0, 0, 0
	for i := range opts {
		o := &opts[i]
		if notPursuingRE.MatchString(o.Label) || enrolledNowOptRE.MatchString(o.Label) || completedOptionRE.MatchString(o.Label) {
			study++
		}
		switch m := recentDegreeOptRE.FindStringSubmatch(o.Label); {
		case notPursuingRE.MatchString(o.Label):
		case m != nil && recent == nil:
			recent = o
			window, _ = strconv.Atoi(m[1])
			if d := doctoralWindowRE.FindStringSubmatch(o.Label); d != nil {
				doctoral, _ = strconv.Atoi(d[1])
			}
		case enrolledNowOptRE.MatchString(o.Label):
			enrolled = append(enrolled, *o)
		}
	}
	if len(enrolled) == 0 && recent == nil || study < 2 {
		return Draft{}, false
	}
	ongoing, latest := false, EducationFact{}
	inProgress := map[string]bool{}
	last := -1
	for _, e := range s.Req.Facts.Education {
		end, ok := parseYearMonth(e.End)
		if !ok {
			return abstainDraft(ReasonPlaceholderEmpty, "", "student-status: %q has no end month, so it cannot be dated -> abstain", e.Degree), true
		}
		if !graduated(end, s.Req.Today) {
			ongoing, inProgress[degreeNameLevel(e.Degree)] = true, true
		}
		if end.key() > last {
			last, latest = end.key(), e
		}
	}
	if doctoral > 0 && degreeNameLevel(latest.Degree) == "Doctorate" {
		window = doctoral
	}
	// An enrolled option scoped to undergraduate or to graduate study states
	// the level in progress (Zip: "enrolled in an undergraduate program" is not
	// a master's student's); one scoped to both or neither fits any.
	var fits []Option
	for _, o := range enrolled {
		switch under, grad := levelScope(o.Label); {
		case under && !grad && !inProgress["Bachelor"] && !inProgress["Associate"]:
		case grad && !under && !inProgress["Master"] && !inProgress["Doctorate"]:
		default:
			fits = append(fits, o)
		}
	}
	var pick *Option
	why := ""
	switch {
	case ongoing && len(fits) == 1:
		pick, why = &fits[0], "a degree has not ended"
	case !ongoing && recent != nil && monthKey(s.Req.Today.Year, s.Req.Today.Month)-last <= window:
		pick, why = recent, strconv.Quote(latest.Degree)+" ended "+latest.End+", within "+strconv.Itoa(window)+" months"
	default:
		return Draft{}, false
	}
	d := derivedDraft(pick.Label, "student_status", startProfileDerive, "student-status: "+why+" -> "+strconv.Quote(pick.Label))
	d.ChosenLabel = pick.Label
	d.Deterministic = true
	return d, true
}
