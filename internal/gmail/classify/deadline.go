package classify

import (
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

func nextWeekday(ref time.Time, target time.Weekday, advanceWeek bool) time.Time {
	delta := (int(target) - int(ref.Weekday()) + 7) % 7
	if advanceWeek {
		delta += 7
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day()+delta, 0, 0, 0, 0, time.UTC)
}

var (
	isoDateRE   = lazyregexp.New(`\b(20\d{2})[-/](\d{1,2})[-/](\d{1,2})\b`)
	inDaysRE    = ci(`\b(?:due|complete|finish|submit|respond)\s+(?:[a-z]+\s+){0,2}?(\d+)\s+(day|days|week|weeks)\b`)
	monthDayRE  = ci(`\b(?:by|before|until|due|complete)\s+(?:end\s+of\s+)?([A-Za-z]+\.?)\s+(\d{1,2})(?:,?\s+(20\d{2}))?\b`)
	tomorrowRE  = ci(`\b(?:by|before|until|due|complete|respond|submit)\s+(?:[a-z]+\s+){0,2}?tomorrow\b`)
	endOfWeekRE = ci(`\b(?:by|before|until|due|complete)\s+(?:the\s+)?end\s+of\s+(?:the\s+)?week\b`)
	weekdayRE   = ci(`\b(?:by|before|until|due|complete|respond|submit)\s+(?:[a-z]+\s+){0,3}?(?:end\s+of\s+(?:the\s+)?day\s+|eod\s+|cob\s+)?(this\s+|next\s+)?([A-Za-z]+day)\b`)
)

// parseDeadline reads a due time out of text, relative to the received time.
// It returns the zero time when the text states none.
func parseDeadline(text string, ref time.Time) time.Time {
	if text == "" || ref.IsZero() {
		return time.Time{}
	}
	ref = ref.UTC()
	if m := isoDateRE.FindStringSubmatch(text); m != nil {
		due, _ := time.Parse("2006-1-2", strings.Join(m[1:], "-"))
		return due
	}
	if m := inDaysRE.FindStringSubmatch(text); m != nil {
		n, err := strconv.ParseUint(m[1], 10, 64)
		unit := 24 * time.Hour
		if strings.HasPrefix(strings.ToLower(m[2]), "week") {
			unit *= 7
		}
		if err != nil || n > uint64((1<<63-1)/int64(unit)) {
			return time.Time{}
		}
		return ref.Add(time.Duration(n) * unit)
	}
	if m := monthDayRE.FindStringSubmatch(text); m != nil {
		// Parse a complete date so invalid days cannot silently become a
		// different month. A yearless date must also be valid after rollover.
		// A word that is not a month ("by Friday 5 PM") leaves the rules below
		// to read the text.
		month := strings.TrimSuffix(m[1], ".")
		if strings.EqualFold(month, "Sept") {
			month = "Sep"
		}
		parse := func(year int) time.Time {
			value := month + " " + m[2] + " " + strconv.Itoa(year)
			for _, layout := range []string{"January 2 2006", "Jan 2 2006"} {
				if due, err := time.Parse(layout, value); err == nil {
					return due
				}
			}
			return time.Time{}
		}
		year := ref.Year()
		if m[3] != "" {
			year, _ = strconv.Atoi(m[3])
		}
		due := parse(year)
		if !due.IsZero() && m[3] == "" && due.Before(ref.Add(-30*24*time.Hour)) {
			due = parse(year + 1)
		}
		if !due.IsZero() {
			return due
		}
	}
	if tomorrowRE.MatchString(text) {
		return time.Date(ref.Year(), ref.Month(), ref.Day()+1, 0, 0, 0, 0, time.UTC)
	}
	if endOfWeekRE.MatchString(text) {
		return nextWeekday(ref, time.Friday, false)
	}
	if m := weekdayRE.FindStringSubmatch(text); m != nil {
		for day := time.Sunday; day <= time.Saturday; day++ {
			if strings.EqualFold(m[2], day.String()) {
				return nextWeekday(ref, day, strings.HasPrefix(strings.ToLower(strings.TrimSpace(m[1])), "next"))
			}
		}
	}
	return time.Time{}
}
