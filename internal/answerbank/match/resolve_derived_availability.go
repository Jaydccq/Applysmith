package match

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Start availability is derived from earliest-start-date through StartDate,
// moved past unfinished education (AfterEducation): "Immediately" is not a
// fact, it is the answer only while the start is today.

var (
	availImmediateRE = lazyregexp.New(`\b(?:immediate(?:ly)?|as\s+soon\s+as\s+possible|asap|right\s+away|at\s+once|any\s?time|currently\s+available|available\s+now|start\s+now)\b`)
	availQuantityRE  = lazyregexp.New(`\b(\d{1,3}|` + countWords + `)\s*(day|week|month)s?\b`)
	availUnitDays    = map[string]int{"day": 1, "week": 7, "month": 30}
)

// availabilityLeadDays is the lead time an option label commits to, in days:
// zero for an immediate cue, otherwise the largest "<n> <unit>" it names, so a
// range commits to its upper bound. A label naming no lead time is not an
// availability option.
func availabilityLeadDays(label string) (int, bool) {
	t := strings.Join(strings.Fields(strings.ToLower(label)), " ")
	if t == "" {
		return 0, false
	}
	if availImmediateRE.MatchString(t) {
		return 0, true
	}
	largest, found := 0, false
	for _, m := range availQuantityRE.FindAllStringSubmatch(t, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			n = numberWords[m[1]]
		}
		if days := n * availUnitDays[m[2]]; !found || days > largest {
			largest, found = days, true
		}
	}
	return largest, found
}

// chooseAvailabilityOption is the offered option with the shortest lead time
// that is at least the days until availability; options naming no lead time
// are never chosen. None long enough gives no answer: picking the longest would
// still be a commitment that cannot be met.
func chooseAvailabilityOption(days int, opts []Option) (Option, bool) {
	best, bestLead, found := Option{}, 0, false
	for _, o := range opts {
		lead, ok := availabilityLeadDays(o.Label)
		if !ok || lead < days {
			continue
		}
		if !found || lead < bestLead {
			best, bestLead, found = o, lead, true
		}
	}
	return best, found
}

// availabilityOther is the one "Other" of a menu whose lead times all fall
// short of the start (Xometry: "Immediately", "2 Weeks", "Other" for a month's
// notice): the start is none of them, and Other says so.
func availabilityOther(days int, opts []Option) (Option, bool) {
	var other []Option
	timed := false
	for _, o := range opts {
		if otherLabel.MatchString(o.Label) {
			other = append(other, o)
			continue
		}
		lead, ok := availabilityLeadDays(o.Label)
		if !ok || lead >= days {
			return Option{}, false
		}
		timed = true
	}
	if !timed || len(other) != 1 {
		return Option{}, false
	}
	return other[0], true
}

// chooseAvailabilityTerm is the one offered option whose season or month
// bracket ("Fall 2026", "Spring 2027 or later") holds the month availability
// begins in. Roblox offers "Available to Start Immediately" beside terms, and a
// start a day away is no immediate start but is in "Fall 2026". Two such
// options, or none, give no answer.
func chooseAvailabilityTerm(from Date, opts []Option) (Option, bool) {
	k := monthKey(from.Year, from.Month)
	var hit []Option
	for _, o := range opts {
		if r, ok := parseSeasonBracket(o.Label); ok && r.from <= k && k <= r.to {
			hit = append(hit, o)
		}
	}
	if len(hit) != 1 {
		return Option{}, false
	}
	return hit[0], true
}

// deriveAvailability answers full-time-start-availability.
func deriveAvailability(s *Step, e *Entry) (Draft, bool) {
	q, today := s.Q, s.Req.Today
	start, ok := StartDate(storedStart(s), today)
	if !ok || !today.Valid() {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s is left for you or not a date: availability is unknown", IDEarliestStartDate), true
	}
	if start, ok = AfterEducation(start, today, s.Req.Facts.Education); !ok {
		return abstainDraft(ReasonPlaceholderEmpty, "", "an education entry has no end month: availability is unknown"), true
	}
	days := max(0, today.DaysUntil(start))
	if real := realOptions(q.Options); len(real) > 0 {
		o, ok := chooseAvailabilityOption(days, real)
		why := fmt.Sprintf("available in %d days -> shortest honourable option %q", days, o.Label)
		if !ok {
			if o, ok = chooseAvailabilityTerm(start, real); ok {
				why = fmt.Sprintf("available from %s -> the one option whose term holds it, %q", start.ISO(), o.Label)
			} else if o, ok = availabilityOther(days, real); ok {
				why = fmt.Sprintf("available in %d days, later than every offered lead time -> %q", days, o.Label)
			} else {
				return abstainDraft(ReasonInputMismatch, "", "no offered option reaches %d days", days), true
			}
		}
		d := entryDraft(o.Label, why)
		d.ChosenLabel, d.HasOption, d.OptionScore = o.Label, true, 1
		return d, true
	}
	answer := availabilityText(q, start, days)
	d := entryDraft(answer, fmt.Sprintf("available in %d days -> %q", days, answer))
	if q.Kind == KindCustomListbox {
		d.ChosenLabel = answer
	}
	return d, true
}

var (
	// nextOpportunityAskRE asks when the applicant would be open to a new
	// job (Crossnokaye: "When would you ideally be open to your next
	// opportunity?").
	nextOpportunityAskRE = lazyregexp.New(`(?i)\bwhen\b[^?]{0,40}\b(?:open|ready|looking|available)\b[^?]{0,30}\b(?:next|new)\s+(?:opportunity|role|job|position|move)\b`)
	// lookingNowOptionRE is the option of one looking now ("Actively looking
	// now").
	lookingNowOptionRE = lazyregexp.New(`(?i)\bactively\s+(?:looking|searching|interviewing)\b|\b(?:right\s+)?now\b|\bimmediately\b`)
	// windowMonthsRE opens a later window in months ("Open in the next 3-5
	// months").
	windowMonthsRE = lazyregexp.New(`(?i)\b(\d{1,2})\s*(?:[-\x{2013}]\s*\d{1,2}\s*|\+\s*)?months?\b`)
)

// nextOpportunityTiming answers when the applicant would be open to a next
// opportunity, over an option of looking now beside later windows in months,
// from the notice period (owner decision: the question asks the notice): the
// option of looking now when the notice ends before the soonest later window
// opens. A notice unrecorded or as long as a window leaves it.
func nextOpportunityTiming(s *Step) (Draft, bool) {
	if !nextOpportunityAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var now []Option
	soonest := -1
	for _, o := range realOptions(s.Q.Options) {
		if lookingNowOptionRE.MatchString(o.Label) {
			now = append(now, o)
			continue
		}
		if m := windowMonthsRE.FindStringSubmatch(o.Label); m != nil {
			n, _ := strconv.Atoi(m[1])
			if soonest < 0 || n*30 < soonest {
				soonest = n * 30
			}
		}
	}
	notice, ok := noticeDays(s)
	if len(now) != 1 || soonest < 0 || !ok || notice >= soonest {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: now[0].Label, Value: now[0].Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{fmt.Sprintf("open to a next opportunity: a notice of %d days ends before the soonest later window (%d days) -> %q", notice, soonest, now[0].Label)}
	return d, true
}

// noticeDays is the user's notice period in days, read from their
// availability-notice-period answer ("in 1 Month").
func noticeDays(s *Step) (int, bool) {
	e := entryNamed(s, "availability-notice-period")
	if e == nil {
		return 0, false
	}
	answer := e.FallbackText
	if len(e.Preferred) > 0 {
		answer = e.Preferred[0]
	}
	if strings.TrimSpace(answer) == "" || e.Placeholder && len(e.Preferred) == 0 {
		return 0, false
	}
	return parseLeadTime(answer)
}

// availabilityText is the answer for a control without options. A date control
// takes the start's ISO date; a month-and-year box takes the start's month and
// never "Immediately"; a plain box takes "Immediately" or the date in words,
// because "in 47 days" makes the reader do arithmetic.
func availabilityText(q Question, start Date, days int) string {
	switch {
	case q.InputType == InputDate:
		return start.ISO()
	case monthYearAskRE.MatchString(q.Text):
		return fmt.Sprintf("%s %d", MonthName(start.Month), start.Year)
	case days == 0:
		return "Immediately"
	}
	return start.Long()
}

// shiftKind is one shift "any shift" covers, with the words a recorded
// selection may use for it.
type shiftKind struct {
	name string
	re   *lazyregexp.Regexp
}

var (
	// anyShiftRE asks whether the applicant will work whatever shift is set
	// ("Are you willing to work any shift?", "Are you able to work all shifts?").
	anyShiftRE = lazyregexp.New(`(?i)\b(?:willing|able|available|open)\s+to\s+work\s+(?:any|all|every)\s+shifts?\b`)
	shiftKinds = []shiftKind{
		{"day", lazyregexp.New(`(?i)\b(?:days?|mornings?|first)\b`)},
		{"evening", lazyregexp.New(`(?i)\b(?:evenings?|afternoons?|swing|second)\b`)},
		{"night", lazyregexp.New(`(?i)\b(?:nights?|overnight|third|graveyard)\b`)},
	}
	// shiftWeekend is counted only when the question names weekends.
	shiftWeekend = shiftKind{"weekend", lazyregexp.New(`(?i)\bweekends?\b`)}
	// shiftAnyRE is a selection of any or all shifts, which covers every one.
	shiftAnyRE = lazyregexp.New(`(?i)\b(?:any|all|every|flexible)\b`)
)

// anyShift answers "Are you willing to work any shift?" from the shifts the
// user recorded in shift-availability: Yes when they cover day, evening and
// night (and weekends when the question names them), No when they leave one
// out, and nothing while no shift is recorded.
func anyShift(s *Step) (Draft, bool) {
	if !anyShiftRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	yes, okYes := ownYesNo(s.Q.Options, true)
	no, okNo := ownYesNo(s.Q.Options, false)
	if !okYes || !okNo {
		return Draft{}, false
	}
	var picked []string
	if e := entryNamed(s, "shift-availability"); e != nil && e.Kind == KindMultiSelect {
		for _, v := range e.Selected {
			if strings.TrimSpace(v) != "" && !IsAskMe(v) {
				picked = append(picked, v)
			}
		}
	}
	if len(picked) == 0 {
		return abstainDraft(ReasonPlaceholderEmpty, "", "any shift: shift-availability records no shift -> left for you"), true
	}
	kinds := shiftKinds
	if shiftWeekend.re.MatchString(s.Q.Text) {
		kinds = append(slices.Clone(kinds), shiftWeekend)
	}
	answer, why := yes, fmt.Sprintf("any shift: you can work %q -> %q", strings.Join(picked, ", "), yes.Label)
	for _, k := range kinds {
		if !slices.ContainsFunc(picked, func(v string) bool { return shiftAnyRE.MatchString(v) || k.re.MatchString(v) }) {
			answer, why = no, fmt.Sprintf("any shift: you can work %q, not the %s shift -> %q", strings.Join(picked, ", "), k.name, no.Label)
			break
		}
	}
	d := derivedDraft(answer.Label, "", startProfileDerive, why)
	d.ChosenLabel = answer.Label
	d.Deterministic = true
	return d, true
}

var (
	// coreHoursAskRE asks whether the applicant can work a team's core hours
	// (Enveritas: "Are you able to work during our core hours (10 am to 2 pm
	// United States Eastern Time), Monday through Friday?") or a U.S. zone's
	// business hours (FutureFit AI: "Are you able to work in EST/ EDT business
	// hours?").
	coreHoursAskRE = lazyregexp.New(`(?i)\b(?:able|willing|available|open)\s+to\s+work\s+(?:(?:during\s+|within\s+)?(?:our|the|these|its)\s+(?:team['\x{2019}]?s\s+)?core\s+(?:working\s+|business\s+)?hours|` +
		`(?:during|within|in)\s+(?:the\s+)?(?:(?:u\.?s\.?\s+)?(?:eastern|central|mountain|pacific)(?:\s+(?:standard|daylight))?(?:\s+time)?|[ecmp][sd]?t)(?:\s*/\s*[ecmp][sd]?t)?\s+(?:business|working|office|core)\s+hours)\b`)
	// usZoneRE names a U.S. time zone, written out or abbreviated.
	usZoneRE     = lazyregexp.New(`(?i)\b(?:eastern|central|mountain|pacific)\s+(?:standard\s+|daylight\s+)?time\b|\b(?:united\s+states|u\.?s\.?)\s+(?:eastern|central|mountain|pacific)\b`)
	usZoneAbbrRE = lazyregexp.New(`\b(?:E|C|M|P)[SD]?T\b`)
	// overlapCoreHoursRE is the user's own word that they work any U.S.
	// team's core hours.
	overlapCoreHoursRE = lazyregexp.New(`(?i)\boverlap\b[^.;]*\bcore\s+hours\b[^.;]*\bany\s+u\.?s\.?\s+time\s*zones?\b`)
)

// coreHours answers whether the applicant can work a team's core hours in a
// U.S. time zone from the user's own working-hours entry: Yes when it says
// they overlap with core hours in any U.S. time zone. An entry that says
// nothing of it, or hours in no U.S. zone, are left to the other lanes.
func coreHours(s *Step) (Draft, bool) {
	if !coreHoursAskRE.MatchString(s.Q.Text) || !usZoneRE.MatchString(s.Q.Text) && !usZoneAbbrRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	yes, okYes := ownYesNo(s.Q.Options, true)
	if _, okNo := ownYesNo(s.Q.Options, false); !okYes || !okNo {
		return Draft{}, false
	}
	own := ownText(entryNamed(s, "working-hours"))
	if !overlapCoreHoursRE.MatchString(own) {
		return Draft{}, false
	}
	d := derivedDraft(yes.Label, "", startProfileDerive, fmt.Sprintf("core hours: your working-hours entry says %q -> %q", clip(own, 160), yes.Label))
	d.ChosenLabel = yes.Label
	d.Deterministic = true
	return d, true
}
