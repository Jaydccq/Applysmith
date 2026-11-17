// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strconv"

	"applysmith/internal/lazyregexp"
)

var yearsFigure = lazyregexp.New(`(?i)\b(\d{1,2})\s*\+?\s*(?:(?:-|\x{2013}|to)\s*\d{1,2}\s*\+?\s*)?years?\b`)

// RequiredYears returns the largest stated years-of-experience floor among the
// sentences that state a requirement. A sentence that only prefers the
// experience, or describes the team's history, does not count, and an age
// floor ("at least 18 years of age") is not experience.
func RequiredYears(text string) (int, bool) {
	best, found := 0, false
	for _, s := range Sentences(text) {
		if !experienceWord.MatchString(s) || (softWording.MatchString(s) && !strongWording.MatchString(s)) || teamHistory.MatchString(s) {
			continue
		}
		for _, m := range yearsFigure.FindAllStringSubmatch(ageYears.ReplaceAllString(s, " "), -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && (!found || n > best) {
				best, found = n, true
			}
		}
	}
	return best, found
}

var (
	experienceWord = lazyregexp.New(`(?i)\bexperience\b`)
	ageYears       = lazyregexp.New(`(?i)\b\d{1,2}\s*\+?\s*years?\s+(?:of\s+age|old|or\s+older)\b`)
	teamHistory    = lazyregexp.New(`(?i)\b(?:on\s+(?:the|our)\s+(?:\w+\s+)?team|our\s+(?:team|founders?|leadership|engineers)|founding\s+team|we(?:'ve|\s+have)\s+been)\b`)
)

// YearsMentioned returns the largest years figure in text, whether or not the
// word "experience" is present; it reads a requirement's own label.
func YearsMentioned(text string) (int, bool) {
	best, found := 0, false
	for _, m := range yearsFigure.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && (!found || n > best) {
			best, found = n, true
		}
	}
	return best, found
}

var (
	softWording   = lazyregexp.New(`(?i)\b(preferred|a plus|nice to have|bonus|desirable|desired|advantage|helpful|beneficial)\b`)
	strongWording = lazyregexp.New(`(?i)\b(required|requires?|must|mandatory|requirement)\b`)
)

var (
	// The qualifiers name no skill: "hands-on engineering" (Curai) and "full-time,
	// professional" (Enveritas) are the whole career.
	overallYearsRE = lazyregexp.New(`(?i)\byears?\s+of\s+(?:(?:relevant|related|total|overall|professional|industry|full[\s-]?time|paid|work(?:ing)?|hands[\s-]?on|engineering|software|technical|practical|commercial)[\s,]+)*experience\b([^?.]*)`)
	namedSkillRE   = lazyregexp.New(`(?i)\b(?:with|in|using)\b`)
)

// OverallExperience reports a question for the applicant's years of
// experience as a whole, not with a named skill. Its answer rests on the
// employment dates, which the advisory review is never shown: shown only the
// skill years, it read a correct "Less than 1 year" as a contradiction
// (Campbell's).
func OverallExperience(label string) bool {
	m := overallYearsRE.FindStringSubmatch(label)
	return m != nil && !namedSkillRE.MatchString(m[1])
}

var (
	// thresholdYearsRE is a floor of whole years: "3+ years", "3 years+", "at
	// least 3 years", "a minimum of 3 years", "3 or more years", "3 years or
	// more", and the strict "more than 3 years" and "over 3 years".
	thresholdYearsRE = lazyregexp.New(`\b(?:(at least|(?:a )?minimum of|min\.?|more than|over|greater than) )?(\d{1,2}) ?(\+ ?)?((?:or more|or above|and above|plus) )?(?:years?|yrs?)\b( ?\+| or more)?`)
	// yearsCeilingRE, yearsRangeRE and ageYears read what is not a floor of
	// experience: a ceiling, a range of years, an age.
	yearsCeilingRE = lazyregexp.New(`\b(?:less than|fewer than|under|up to|no more than|maximum of|max\.?|at most|within|in the (?:last|past)) \d`)
	yearsRangeRE   = lazyregexp.New(`\d ?(?:-|\x{2013}|to) ?\d{1,2} ?(?:years?|yrs?)\b`)
	// yearsAlternativeRE is a second way to qualify or a second requirement: an
	// alternative ("or a degree"), a degree, a credential; yearsCredentialRE is
	// one of the latter, which a choice of fields never names.
	yearsAlternativeRE = lazyregexp.New(`\bor\b|` + yearsCredential)
	yearsCredentialRE  = lazyregexp.New(yearsCredential + `|\bequivalent\b`)
	// spelledYearsRE is a number written out before years ("more than three
	// years"), read as its figure.
	spelledYearsRE = lazyregexp.New(`\b(one|two|three|four|five|six|seven|eight|nine|ten)( ?\+? ?(?:years?|yrs?)\b)`)
	// yearsDoingRE is years spent doing something ("more than three years
	// deploying solutions at customers"): experience without the word. Years
	// spent living somewhere are not.
	yearsDoingRE  = lazyregexp.New(`\b(?:years?|yrs?) (?:of )?(?:[a-z]+ing)\b`)
	yearsLivingRE = lazyregexp.New(`\b(?:years?|yrs?) (?:of )?(?:living|residing|staying)\b`)
)

const yearsCredential = `\b(?:degrees?|bachelor\w*|masters?|ph\.?d\w*|diploma|certifi\w+|licen[cs]\w*|clearance)\b`

var spelledNumbers = map[string]string{"one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10"}

// YearsThreshold reads a question whether the applicant has at least a number
// of years of experience ("Do you have 3+ years of experience with Python?",
// "at least 5 years of professional Ruby on Rails experience"): the floor,
// whether it is strict ("more than 3 years"), and the question without it,
// which names what the experience is in. A question with a second figure, a
// range, a ceiling, an age, an alternative or a credential, or neither the
// word experience nor years spent doing something, is not one.
func YearsThreshold(label string) (floor int, strict bool, rest string, ok bool) {
	return yearsFloor(label, yearsAlternativeRE)
}

// YearsThresholdAnyOf reads a YearsThreshold question whose subject is a
// choice of fields joined by a bare "or" ("Do you have 2+ years of
// professional experience with Go or Python programming", New Relic): a
// degree, a credential or an equivalent still makes it no threshold. Only a
// bound that holds for every field answers it.
func YearsThresholdAnyOf(label string) (floor int, strict bool, rest string, ok bool) {
	return yearsFloor(label, yearsCredentialRE)
}

func yearsFloor(label string, alternative *lazyregexp.Regexp) (floor int, strict bool, rest string, ok bool) {
	f := spelledYearsRE.ReplaceAllStringFunc(foldText(label), func(m string) string {
		sub := spelledYearsRE.FindStringSubmatch(m)
		return spelledNumbers[sub[1]] + sub[2]
	})
	doing := yearsDoingRE.MatchString(f) && !yearsLivingRE.MatchString(f)
	if !(experienceWord.MatchString(f) || doing) || yearsCeilingRE.MatchString(f) || yearsRangeRE.MatchString(f) || ageYears.MatchString(f) {
		return 0, false, "", false
	}
	ms := thresholdYearsRE.FindAllStringSubmatchIndex(f, -1)
	if len(ms) != 1 {
		return 0, false, "", false
	}
	m := ms[0]
	group := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return f[m[2*i]:m[2*i+1]]
	}
	if group(1) == "" && group(3) == "" && group(4) == "" && group(5) == "" {
		return 0, false, "", false
	}
	n, err := strconv.Atoi(group(2))
	if err != nil || n == 0 {
		return 0, false, "", false
	}
	switch group(1) {
	case "more than", "over", "greater than":
		strict = true
	}
	rest = f[:m[0]] + " " + f[m[1]:]
	if alternative.MatchString(rest) {
		return 0, false, "", false
	}
	return n, strict, rest, true
}

// YearsThresholdAnswer reports a Yes or No to a YearsThreshold question. Written
// from the record, it rests on the years the user recorded (a skill's, the
// CV's skills at their default, the total), which the advisory review is never
// shown: shown none, it read a correct No to "a minimum of 3+ years of
// experience coding in Python" as a contradiction (Baton). A model's answer is
// still reviewed.
func YearsThresholdAnswer(label, value string) bool {
	switch foldText(value) {
	case "yes", "no":
		_, _, _, ok := YearsThresholdAnyOf(label)
		return ok
	}
	return false
}
