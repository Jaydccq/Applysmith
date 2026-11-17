package match

import (
	"fmt"
	"strconv"
	"strings"
)

// The matcher reads no clock, so calendar arithmetic runs on civil day numbers
// (days since the Unix epoch day), which need no time zone and no time package.

// monthNames are the English month names, January first.
var monthNames = [12]string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

// MonthName returns the English name of month 1..12, or "" outside it.
func MonthName(m int) string {
	if m < 1 || m > 12 {
		return ""
	}
	return monthNames[m-1]
}

// monthFromWord reads a month from its number ("5", "05"), its full English
// name or its first three letters ("Sep"), case-insensitively. A trailing
// period is ignored. It returns 0 when the word is no month.
func monthFromWord(w string) int {
	w = strings.ToLower(strings.TrimRight(strings.TrimSpace(w), "."))
	if w == "" {
		return 0
	}
	if n, err := strconv.Atoi(w); err == nil {
		if n >= 1 && n <= 12 && len(w) <= 2 {
			return n
		}
		return 0
	}
	for i, name := range monthNames {
		lower := strings.ToLower(name)
		if w == lower || (len(w) == 3 && strings.HasPrefix(lower, w)) {
			return i + 1
		}
	}
	return 0
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// daysInMonth is the length of month m of year y; 0 for an invalid month.
func daysInMonth(y, m int) int {
	switch m {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if isLeap(y) {
			return 29
		}
		return 28
	}
	return 0
}

// Valid reports a real calendar date.
func (d Date) Valid() bool {
	return d.Year >= 1 && d.Month >= 1 && d.Month <= 12 && d.Day >= 1 && d.Day <= daysInMonth(d.Year, d.Month)
}

// IsZero reports the unset date.
func (d Date) IsZero() bool { return d == Date{} }

// dayNumber is the number of days from the Unix epoch day to d (negative before it),
// by the civil-from-days algorithm.
func (d Date) dayNumber() int {
	y, m := d.Year, d.Month
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d.Day - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

// dateFromDayNumber is the inverse of dayNumber.
func dateFromDayNumber(n int) Date {
	n += 719468
	era := n / 146097
	if n < 0 {
		era = (n - 146096) / 146097
	}
	doe := n - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	day := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if m > 12 {
		m -= 12
	}
	if m <= 2 {
		y++
	}
	return Date{Year: y, Month: m, Day: day}
}

// AddDays returns d moved by n days (negative moves back).
func (d Date) AddDays(n int) Date { return dateFromDayNumber(d.dayNumber() + n) }

// DaysUntil is the number of whole days from d to o (negative when o is earlier).
func (d Date) DaysUntil(o Date) int { return o.dayNumber() - d.dayNumber() }

// Before reports d strictly earlier than o.
func (d Date) Before(o Date) bool { return d.dayNumber() < o.dayNumber() }

// ISO formats d in the ISO 8601 calendar-date layout.
func (d Date) ISO() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// US formats d as MM/DD/YYYY.
func (d Date) US() string { return fmt.Sprintf("%02d/%02d/%04d", d.Month, d.Day, d.Year) }

// Long formats d as "<Month> <day>, <year>" with an unpadded day.
func (d Date) Long() string { return fmt.Sprintf("%s %d, %d", MonthName(d.Month), d.Day, d.Year) }

// LastOfMonth is the last day of the month d falls in.
func (d Date) LastOfMonth() Date {
	return Date{Year: d.Year, Month: d.Month, Day: daysInMonth(d.Year, d.Month)}
}

// monthKey is year*12+month-1, so months compare and subtract as integers.
func monthKey(year, month int) int { return year*12 + month - 1 }

// parseCalendarDate accepts an ISO 8601 calendar date or a US month/day/year date and
// nothing else: the matcher never guesses a locale. An impossible date (a
// thirteenth month, a thirtieth of the second month) is a parse miss.
func parseCalendarDate(s string) (Date, bool) {
	s = strings.TrimSpace(s)
	var parts []string
	var y, m, d string
	switch {
	case strings.Count(s, "-") == 2 && !strings.Contains(s, "/"):
		parts = strings.Split(s, "-")
		y, m, d = parts[0], parts[1], parts[2]
		if len(y) != 4 || len(m) != 2 || len(d) != 2 {
			return Date{}, false
		}
	case strings.Count(s, "/") == 2 && !strings.Contains(s, "-"):
		parts = strings.Split(s, "/")
		m, d, y = parts[0], parts[1], parts[2]
		if len(y) != 4 || len(m) < 1 || len(m) > 2 || len(d) < 1 || len(d) > 2 {
			return Date{}, false
		}
	default:
		return Date{}, false
	}
	yi, e1 := strconv.Atoi(y)
	mi, e2 := strconv.Atoi(m)
	di, e3 := strconv.Atoi(d)
	if e1 != nil || e2 != nil || e3 != nil || !digitsOnly(y+m+d) {
		return Date{}, false
	}
	out := Date{Year: yi, Month: mi, Day: di}
	return out, out.Valid()
}

func digitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// yearMonth is a calendar month; the zero value is "no month".
type yearMonth struct{ Year, Month int }

func (a yearMonth) isZero() bool { return a.Year == 0 }
func (a yearMonth) key() int     { return monthKey(a.Year, a.Month) }

// parseYearMonth reads a profile month, YYYY-MM.
func parseYearMonth(s string) (yearMonth, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 7 || s[4] != '-' || !digitsOnly(s[:4]+s[5:]) {
		return yearMonth{}, false
	}
	y, _ := strconv.Atoi(s[:4])
	m, _ := strconv.Atoi(s[5:])
	if y < 1 || m < 1 || m > 12 {
		return yearMonth{}, false
	}
	return yearMonth{y, m}, true
}

// monthOf is the calendar month a date falls in.
func monthOf(d Date) yearMonth { return yearMonth{d.Year, d.Month} }
