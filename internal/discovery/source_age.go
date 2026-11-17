package discovery

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"applysmith/internal/lazyregexp"
)

const (
	day   = 24 * time.Hour
	month = 30 * day
	year  = 365 * day
)

var (
	wordAgeRE  = lazyregexp.New(`(?i)^(?:reposted\s+|posted\s+|updated\s+)?(an?|\d+)\s*(second|minute|hour|day|week|month|year)s?\s+ago$`)
	shortAgeRE = lazyregexp.New(`(?i)^(\d+)\s*(mo|m|h|d|w|y)$`)
	slashRE    = lazyregexp.New(`^(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?$`)
)

// RelativeAge reads a vendor's age text: "16 hours ago", "2 days ago",
// "3 weeks ago", "4 months ago", "an hour ago", "yesterday", "today". ok is
// false when the phrasing is not one of these.
func RelativeAge(s string) (time.Duration, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "today", "just now", "just posted", "now":
		return 0, true
	case "yesterday":
		return day, true
	}
	m := wordAgeRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n := 1
	if m[1] != "a" && m[1] != "an" {
		n, _ = strconv.Atoi(m[1])
	}
	unit := map[string]time.Duration{"second": time.Second, "minute": time.Minute, "hour": time.Hour, "day": day, "week": 7 * day, "month": month, "year": year}[m[2]]
	return time.Duration(n) * unit, true
}

// ShortAge reads the compact ages of the list repositories: 10m, 23h, 3d, 2w,
// 3mo. Months are matched before minutes, so an archive row reads stale and
// not undated. A leading emoji or symbol marker is ignored.
func ShortAge(s string) (time.Duration, bool) {
	s = strings.TrimFunc(strings.TrimSpace(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	m := shortAgeRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"mo": month, "m": time.Minute, "h": time.Hour, "d": day, "w": 7 * day, "y": year}[strings.ToLower(m[2])]
	return time.Duration(n) * unit, true
}

var monthNames = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September, "sept": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

var textDateRE = lazyregexp.New(`(?i)^([a-z]{3,9})\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?$`)

// AbsoluteDate reads a month and day with or without a year, an ISO date and
// a month/day pair. A date without a year resolves to its most recent occurrence at or before
// now. The result is the start of that day in UTC.
func AbsoluteDate(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimFunc(strings.TrimSpace(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	var mon time.Month
	var dayN int
	var yr int
	if m := textDateRE.FindStringSubmatch(s); m != nil {
		var ok bool
		if mon, ok = monthNames[strings.ToLower(m[1])[:min(len(m[1]), 3)]]; !ok {
			return time.Time{}, false
		}
		dayN, _ = strconv.Atoi(m[2])
		yr, _ = strconv.Atoi(m[3])
	} else if m := slashRE.FindStringSubmatch(s); m != nil {
		a, _ := strconv.Atoi(m[1])
		mon, dayN = time.Month(a), 0
		dayN, _ = strconv.Atoi(m[2])
		if m[3] != "" {
			yr, _ = strconv.Atoi(m[3])
			if yr < 100 {
				yr += 2000
			}
		}
	} else {
		return time.Time{}, false
	}
	if mon < 1 || mon > 12 || dayN < 1 || dayN > 31 {
		return time.Time{}, false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if yr != 0 {
		t := time.Date(yr, mon, dayN, 0, 0, 0, 0, time.UTC)
		return t, t.Month() == mon
	}
	t := time.Date(now.Year(), mon, dayN, 0, 0, 0, 0, time.UTC)
	if t.Month() != mon {
		return time.Time{}, false
	}
	if t.After(today) {
		t = time.Date(now.Year()-1, mon, dayN, 0, 0, 0, 0, time.UTC)
	}
	return t, true
}

// AgeOf reads a list cell's age against now as a duration: a short age, a
// relative phrase, or a date counted in whole days. ok is false when the text
// is unreadable or a date lies more than one day in the future.
func AgeOf(cell string, now time.Time) (time.Duration, bool) {
	if d, ok := ShortAge(cell); ok {
		return d, true
	}
	if d, ok := RelativeAge(strings.TrimSpace(cell)); ok {
		return d, true
	}
	t, ok := AbsoluteDate(cell, now)
	if !ok {
		return 0, false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	age := today.Sub(t)
	if age < -day {
		return 0, false
	}
	return max(age, 0), true
}
