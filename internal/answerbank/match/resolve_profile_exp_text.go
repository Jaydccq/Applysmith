package match

import (
	"fmt"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

var (
	firstDigitsRE = lazyregexp.New(`\d+`)
	stateCodeRE   = lazyregexp.New(`\(([A-Za-z]{2})\)\s*$`)
	// listCardinalityRE needs a list verb or a "top N" frame, so "3 years of
	// experience" or "Section 3" is never read as a cap.
	listCardinalityRE = lazyregexp.New(`(?i)\b(?:top|first|up\s+to|at\s+most|list|name|provide|share|give)\s+(?:your\s+|us\s+|the\s+)?(?:top\s+)?(\d{1,2}|one|two|three|four|five|six|seven|eight|nine|ten)\b`)
	// countedProseRE is what the number counts when it counts no items of a
	// list: a length of prose or a span of time ("2-3 sentences", "first 3
	// months").
	countedProseRE = lazyregexp.New(`(?i)^\s*(?:(?:-|\x{2013}|to|or)\s*\w+\s*)?(?:full\s+|short\s+|brief\s+)?(?:sentences?|paragraphs?|words?|lines?|characters?|pages?|minutes?|hours?|days?|weeks?|months?|years?)\b`)
	// sentenceMarkRE is punctuation that ends or joins sentences, which a list
	// of items does not hold.
	sentenceMarkRE = lazyregexp.New(`[.!?;:](?:\s|$)`)
)

var cardinalityWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
}

// maxListItemWords is the longest comma-separated part still read as an item
// of a list rather than a clause of a sentence.
const maxListItemWords = 4

// requestedListCardinality is the number of items a question asks for, 1 to 10,
// or 0 when it names none or counts prose or time (countedProseRE).
func requestedListCardinality(text string) int {
	m := listCardinalityRE.FindStringSubmatchIndex(text)
	if m == nil || countedProseRE.MatchString(text[m[1]:]) {
		return 0
	}
	token := strings.ToLower(text[m[2]:m[3]])
	n, ok := cardinalityWords[token]
	if !ok {
		n, _ = strconv.Atoi(token)
	}
	if n < 1 || n > 10 {
		return 0
	}
	return n
}

// trimListToCardinality keeps the first n comma-separated items; an answer that
// is not a list, or is already short enough, is returned unchanged. Prose is no
// list (a sentence mark, or a part longer than maxListItemWords), nor are
// places written "City, ST", whose commas do not separate items.
func trimListToCardinality(answer string, n int) string {
	if sentenceMarkRE.MatchString(answer) {
		return answer
	}
	var parts []string
	for _, p := range strings.Split(answer, ",") {
		if p = strings.TrimSpace(p); p != "" {
			if _, state := stateByAbbr[strings.ToLower(p)]; len(strings.Fields(p)) > maxListItemWords || state && p == strings.ToUpper(p) {
				return answer
			}
			parts = append(parts, p)
		}
	}
	if len(parts) <= n {
		return answer
	}
	return strings.Join(parts[:n], ", ")
}

// finishTextAnswer applies the text-control transformations to the answer of an
// entry, in order: a years entry (isYearsEntry) that stores a range types its
// lower-bound integer, a list answer is cut to the number of items the question
// asks for, and a state answer ending in a parenthesised code types the code.
// The caller applies it only to the text answer of a control that has no options.
func finishTextAnswer(e *Entry, q Question, answer string) (string, []string) {
	var trace []string
	if isYearsEntry(e) {
		if n, ok := yearsEntryNumber(answer); ok && strconv.Itoa(n) != answer {
			trace = append(trace, fmt.Sprintf("years coercion: %q -> \"%d\" for a text input", answer, n))
			answer = strconv.Itoa(n)
		}
	}
	if n := requestedListCardinality(q.Text); n > 0 {
		if trimmed := trimListToCardinality(answer, n); trimmed != answer {
			trace = append(trace, fmt.Sprintf("list cardinality: question asks for %d -> %q trimmed to %q", n, answer, trimmed))
			answer = trimmed
		}
	}
	// A No to a question that asks for a list of what the applicant holds says
	// the list is empty: "None" answers it, a bare No reads as a refusal.
	if bareNoRE.MatchString(answer) && listAskRE.MatchString(q.Text) {
		trace = append(trace, fmt.Sprintf("list ask: %q -> \"None\"", answer))
		answer = "None"
	}
	if e != nil && e.ID == "availability-notice-period" {
		if n, unit, ok := leadTimeIn(answer, q.Text); ok {
			trace = append(trace, fmt.Sprintf("notice in the box's unit: %q -> %d %s", answer, n, unit))
			answer = strconv.Itoa(n)
		}
	}
	if e != nil && e.ID == "address-state" {
		if m := stateCodeRE.FindStringSubmatch(answer); m != nil {
			code := strings.ToUpper(m[1])
			trace = append(trace, fmt.Sprintf("state text derivation: %q -> %q for text input", answer, code))
			answer = code
		}
	}
	return answer, trace
}

// leadUnitRE is the unit a box asks a lead time in: "Notice Period
// (months)", "Notice period (in weeks)".
var leadUnitRE = lazyregexp.New(`(?i)\(\s*(?:in\s+)?(days|weeks|months)\s*\)|\bin\s+(days|weeks|months)\b`)

// leadTimeIn is a recorded lead time ("in 1 Month") as a number of the unit
// the question names, when the unit divides it.
func leadTimeIn(answer, question string) (int, string, bool) {
	m := leadUnitRE.FindStringSubmatch(question)
	if m == nil {
		return 0, "", false
	}
	unit := strings.ToLower(m[1] + m[2])
	days, ok := parseLeadTime(answer)
	size := map[string]int{"days": 1, "weeks": 7, "months": 30}[unit]
	if !ok || days%size != 0 {
		return 0, "", false
	}
	return days / size, unit, true
}

var (
	bareNoRE       = lazyregexp.New(`(?i)^\s*no\s*\.?\s*$`)
	listAskRE      = lazyregexp.New(`(?i)\blist\s+(?:any|all|the|your|each)\b|\bwhich\b[^?]*\b(?:do|did)\s+you\s+(?:hold|have)\b`)
	daysFullTimeRE = lazyregexp.New(`\b(?:full[-\s]?time\s+onsite|in\s+the\s+office\s+(?:5|five)\s+days|monday\s*(?:-|\x{2013}|to)\s*friday)\b`)
	daysNumber     = `(\d+|one|two|three|four|five|six|seven)`
	daysRangeRE    = lazyregexp.New(`\b` + daysNumber + `\s*(?:-|\x{2013}|to)\s*` + daysNumber + `\s*[-/]?\s*day`)
	daysAtLeastRE  = lazyregexp.New(`\b(?:at\s+least|minimum\s+of?|no\s+less\s+than)\s+` + daysNumber + `\s*[-/]?\s*day`)
	daysPlusRE     = lazyregexp.New(`\b(\d+)\s*\+\s*days?\b`)
	daysUpToRE     = lazyregexp.New(`\b(?:up\s+to|maximum\s+of?|no\s+more\s+than)\s+` + daysNumber + `\s*[-/]?\s*day`)
	daysPerWeekRE  = lazyregexp.New(`\b` + daysNumber + `\s*[x\x{00d7}]\s*(?:/|per|a)?\s*week`)
	daysPlainRE    = lazyregexp.New(`\b` + daysNumber + `\s*[-/]?\s*day`)
)

var dayWords = map[string]int{"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7}

// dayCount reads a digit or a number word; a value outside 0..7 is a miss.
func dayCount(tok string) (int, bool) {
	if digitsOnly(tok) {
		n, err := strconv.Atoi(tok)
		return n, err == nil && n >= 0 && n <= 7
	}
	n, ok := dayWords[tok]
	return n, ok
}

// extractDaysPerWeek reads the days a week in the office a question asks about.
// The order is load-bearing: full-time forms first, a range gives its larger
// value (so "3-5 days" against a maximum of 4 reads as 5), then a floor, a
// ceiling, "N times a week", and the broadest plain "N days" last. Every
// pattern starts at a word boundary, so "stone to three days" is not "one to
// three". The second result names the rule for the trace.
func extractDaysPerWeek(text string) (value int, rule string, ok bool) {
	t := strings.ToLower(text)
	if daysFullTimeRE.MatchString(t) {
		return 5, "full-time", true
	}
	if m := daysRangeRE.FindStringSubmatch(t); m != nil {
		lo, ok1 := dayCount(m[1])
		hi, ok2 := dayCount(m[2])
		if ok1 && ok2 {
			return max(lo, hi), fmt.Sprintf("range %d-%d", lo, hi), true
		}
	}
	for _, p := range []struct {
		re   *lazyregexp.Regexp
		name string
	}{{daysAtLeastRE, "at-least"}, {daysPlusRE, "at-least"}, {daysUpToRE, "up-to"}, {daysPerWeekRE, "times-per-week"}, {daysPlainRE, "plain"}} {
		if m := p.re.FindStringSubmatch(t); m != nil {
			if n, ok := dayCount(m[1]); ok {
				return n, fmt.Sprintf("%s %d", p.name, n), true
			}
		}
	}
	return 0, "", false
}
