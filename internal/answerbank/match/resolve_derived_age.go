package match

import (
	"fmt"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Age is derived from candidate.date_of_birth at answer time: a stored bracket
// is a fact with an expiry date.

const maxPlausibleAge = 120

// ageOn is the whole years elapsed from dob to today. A future or implausible
// date of birth is bad data, not a young applicant.
func ageOn(dob string, today Date) (int, bool) {
	d, ok := parseCalendarDate(dob)
	if !ok || !today.Valid() {
		return 0, false
	}
	age := today.Year - d.Year
	if today.Month < d.Month || (today.Month == d.Month && today.Day < d.Day) {
		age--
	}
	if age < 0 || age > maxPlausibleAge {
		return 0, false
	}
	return age, true
}

var ageGateRE = []*lazyregexp.Regexp{
	lazyregexp.New(`\b(\d{2})\s*\+\s*years?\s+(?:of\s+age|old)\b`),
	lazyregexp.New(`\bat\s+least\s+(\d{2})\s*(?:years?)?\s*(?:of\s+age|old)?\b`),
	lazyregexp.New(`\b(\d{2})\s+years?\s+(?:of\s+age\s+)?or\s+(?:older|above|over)\b`),
	lazyregexp.New(`\bover\s+(?:the\s+age\s+of\s+)?(\d{2})\b`),
	// Capital One's Workday: "Are you age 18 or over?"
	lazyregexp.New(`\baged?\s+(\d{2})\s+(?:or|and)\s+(?:older|above|over)\b`),
}

var ageWords = strings.NewReplacer("sixteen", "16", "eighteen", "18", "twenty one", "21", "twenty-one", "21")

// ageNamedRE is the age itself named beside the number ("15 years of age").
var ageNamedRE = lazyregexp.New(`\b(?:of\s+age|old|aged?)\b`)

// minimumAgeAsked is the threshold of an "are you at least N" gate. Only the
// employment-eligibility ages count, so "at least 40 hours" is never a gate:
// 16, 18 or 21 alone, and 14 to 21 when the gate names the age itself (UT
// Austin: "Are you at least 15 years of age?"), so "at least 20 hours" is not.
func minimumAgeAsked(text string) (int, bool) {
	t := ageWords.Replace(strings.ToLower(text))
	for _, re := range ageGateRE {
		m := re.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n == 16 || n == 18 || n == 21 || n >= 14 && n <= 21 && ageNamedRE.MatchString(m[0]) {
			return n, true
		}
	}
	return 0, false
}

type ageRange struct{ min, max int }

var (
	// ageStatementRE strips a first-person statement around an age ("I am 18
	// years of age or older" reads "18 or older").
	ageStatementRE = lazyregexp.New(`^(?:i\s+am|i'm)\s+|\s+years?(?:\s+of\s+age|\s+old)?\b`)
	ageUnderRE     = lazyregexp.New(`^(?:under|below|younger than|less than)\s+(\d{1,3})\b`)
	ageUpToRE      = lazyregexp.New(`^(\d{1,3})\s*(?:and|or)\s+(?:under|below|younger)\b`)
	ageOverRE      = lazyregexp.New(`^(\d{1,3})\s*(?:\+|(?:or|and)\s+(?:older|above|over)\b)`)
	ageSpanRE      = lazyregexp.New(`^(\d{1,3})\s*(?:-|to|[\x{2013}\x{2014}])\s*(\d{1,3})\b`)
)

// parseAgeRange reads a bracket label as an inclusive span. A label that names
// no range ("Prefer not to say") is not a bracket.
func parseAgeRange(label string) (ageRange, bool) {
	t := ageStatementRE.ReplaceAllString(strings.Join(strings.Fields(strings.ToLower(label)), " "), "")
	num := func(s string) int { n, _ := strconv.Atoi(s); return n }
	if m := ageUnderRE.FindStringSubmatch(t); m != nil {
		return ageRange{0, num(m[1]) - 1}, true
	}
	if m := ageUpToRE.FindStringSubmatch(t); m != nil {
		return ageRange{0, num(m[1])}, true
	}
	if m := ageOverRE.FindStringSubmatch(t); m != nil {
		return ageRange{num(m[1]), 200}, true
	}
	if m := ageSpanRE.FindStringSubmatch(t); m != nil {
		if lo, hi := num(m[1]), num(m[2]); hi >= lo {
			return ageRange{lo, hi}, true
		}
	}
	return ageRange{}, false
}

// allAgeRanges reports two or more options that each read as an age range.
func allAgeRanges(opts []Option) bool {
	for _, o := range opts {
		if _, ok := parseAgeRange(o.Label); !ok {
			return false
		}
	}
	return len(opts) >= 2
}

// chooseAgeBracket returns the one option whose range holds the age. Zero or
// several containing options give no answer, because picking one would guess
// the employer's taxonomy; several is reported through the second result.
func chooseAgeBracket(age int, opts []Option) (opt Option, several bool, ok bool) {
	var hits []Option
	for _, o := range opts {
		if r, ok := parseAgeRange(o.Label); ok && age >= r.min && age <= r.max {
			hits = append(hits, o)
		}
	}
	switch len(hits) {
	case 0:
		return Option{}, false, false
	case 1:
		return hits[0], false, true
	}
	return Option{}, true, false
}

// ageMentionRE is a question that speaks of age at all.
var ageMentionRE = lazyregexp.New(`(?i)\b(?:age|aged|old|older|younger)\b`)

// deriveAge answers current-age-bracket and age-minimum-threshold.
func deriveAge(s *Step, e *Entry) (Draft, bool) {
	q := s.Q
	age, ok := ageOn(s.Req.Facts.DateOfBirth, s.Req.Today)
	if !ok {
		return abstainDraft(ReasonPlaceholderEmpty, "", "date of birth is unset, unparseable or implausible: age is unknown"), true
	}
	real := realOptions(q.Options)
	if e.ID == IDAgeMinimumThreshold {
		minimum, ok := minimumAgeAsked(q.Text)
		if !ok && !ageMentionRE.MatchString(q.Text) {
			// Not the age question ("Do you have at least 5 years of Ruby on
			// Rails experience?", Huntress): no entry claims it, so the
			// user's own information decides it.
			d := abstainDraft(ReasonNoEntry, "", "no age in the question: not the age question")
			d.Entry = &Entry{}
			return d, true
		}
		if !ok {
			return abstainDraft(ReasonInputMismatch, "", "no age threshold of 16, 18 or 21 in the question"), true
		}
		answer := "No"
		if age >= minimum {
			answer = "Yes"
		}
		d := entryDraft(answer, fmt.Sprintf("age %d against a minimum of %d -> %s", age, minimum, answer))
		if len(real) == 0 {
			return d, true
		}
		o, ok := ownYesNo(real, answer == "Yes")
		if !ok && allAgeRanges(real) {
			if b, several, found := chooseAgeBracket(age, real); found && !several {
				o, ok = b, true
			}
		}
		if !ok {
			return abstainDraft(ReasonInputMismatch, "", "the control offers no %s option", answer), true
		}
		d.Answer, d.ChosenLabel, d.HasOption, d.OptionScore = o.Label, o.Label, true, 1
		return d, true
	}
	if len(real) == 0 {
		if q.Kind != KindText {
			return abstainDraft(ReasonInputMismatch, "", "the control cannot be read as an age box"), true
		}
		return entryDraft(strconv.Itoa(age), fmt.Sprintf("bare age box: %d", age)), true
	}
	o, several, ok := chooseAgeBracket(age, real)
	switch {
	case several:
		return abstainDraft(ReasonAmbiguous, "", "age %d lies in several overlapping brackets", age), true
	case !ok:
		return abstainDraft(ReasonInputMismatch, "", "age %d lies in no offered bracket", age), true
	}
	d := entryDraft(o.Label, fmt.Sprintf("age %d -> bracket %q", age, o.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore = o.Label, true, 1
	return d, true
}
