package match

import (
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The date grammar the start-window and start-period lanes read: a month name
// with an optional day and a year, MM/DD/YYYY, YYYY-MM-DD or a bare year.
// RE2 has no lookaround, so a date glued to a word character, a slash or a
// hyphen is rejected by an explicit boundary check on the match indices.

const dvMonthPattern = `(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sept?(?:ember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?`

// dvDateAlternatives are tried in this order at every position, the longest
// readings first, so "March 2027" is never read as the bare year 2027.
var dvDateAlternatives = []string{
	dvMonthPattern + `\s+(?:\d{1,2}(?:st|nd|rd|th)?,?\s+)?\d{4}`,
	`\d{1,2}/\d{1,2}/\d{4}`,
	`\d{4}-\d{2}-\d{2}`,
	`\d{4}`,
}

// dvDatePattern is the whole grammar as one non-capturing group.
var dvDatePattern = `(?:` + strings.Join(dvDateAlternatives, `|`) + `)`

var dvDateAnchored = func() []*lazyregexp.Regexp {
	out := make([]*lazyregexp.Regexp, len(dvDateAlternatives))
	for i, alt := range dvDateAlternatives {
		out[i] = lazyregexp.New(`(?i)^(?:` + alt + `)`)
	}
	return out
}()

var dvMonthDateRE = lazyregexp.New(`(?i)^(` + dvMonthPattern + `)\s+(?:(\d{1,2})(?:st|nd|rd|th)?,?\s+)?(\d{4})$`)

// dvDateGlue reports a byte that makes a neighbouring date part of a longer
// token: a word character, a slash or a hyphen.
func dvDateGlue(b byte) bool {
	return b == '_' || b == '/' || b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// scanDates returns every date expression in the label that is not glued to a
// neighbouring token, in order.
func scanDates(label string) []string {
	var out []string
	for i := 0; i < len(label); {
		if i > 0 && dvDateGlue(label[i-1]) {
			i++
			continue
		}
		matched := false
		for _, re := range dvDateAnchored {
			loc := re.FindStringIndex(label[i:])
			if loc == nil {
				continue
			}
			end := i + loc[1]
			if end < len(label) && dvDateGlue(label[end]) {
				continue
			}
			out = append(out, label[i:end])
			i, matched = end, true
			break
		}
		if !matched {
			i++
		}
	}
	return out
}

// dvMonthIndex reads a month from the first three letters of its name.
func dvMonthIndex(word string) int {
	w := strings.ToLower(strings.TrimRight(word, "."))
	if len(w) < 3 {
		return 0
	}
	for i, name := range monthNames {
		if strings.HasPrefix(strings.ToLower(name), w[:3]) {
			return i + 1
		}
	}
	return 0
}

// dateSpan returns the first and last day a date expression covers: an exact
// date, a bare year, or a month with an optional day ("May 2027" covers the
// whole month). A day beyond the month's length is no date.
func dateSpan(text string) (first, last Date, ok bool) {
	t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "."))
	if d, exact := parseCalendarDate(t); exact {
		return d, d, true
	}
	if len(t) == 4 && digitsOnly(t) {
		y, _ := strconv.Atoi(t)
		return Date{y, 1, 1}, Date{y, 12, 31}, true
	}
	m := dvMonthDateRE.FindStringSubmatch(t)
	if m == nil {
		return Date{}, Date{}, false
	}
	month := dvMonthIndex(m[1])
	year, _ := strconv.Atoi(m[3])
	if month == 0 {
		return Date{}, Date{}, false
	}
	if m[2] != "" {
		day, _ := strconv.Atoi(m[2])
		d := Date{year, month, day}
		if !d.Valid() {
			return Date{}, Date{}, false
		}
		return d, d, true
	}
	first = Date{year, month, 1}
	return first, first.LastOfMonth(), true
}
