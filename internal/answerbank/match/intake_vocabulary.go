package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// The route vocabulary is a second, route-only wording of a question. The walk
// tries the page's own text first and uses this form only for rules that did not
// match it, so no route that matches today can be displaced.

var (
	hedgeRE         = lazyregexp.New(`(?i)\(\s*or\s+(?:most recent|previous|former|last|prior)\s*\)`)
	emailSpellRE    = lazyregexp.New(`(?i)\be-mail\b`)
	usAbbrevRE      = lazyregexp.New(`\bU\.S\.(?:A\.)?`)
	listLetterRE    = lazyregexp.New(`^\s*(?:\(?[A-Ha-h]\)|[A-H]\.)\s+([A-Z])`)
	imperativeRE    = lazyregexp.New(`(?i)^\s*(?:set|enter|select|choose|add)\s+your\s+`)
	companyClauseRE = lazyregexp.New(`\s+(?:for|at|with)\s+([A-Z][\w&'.-]*(?:\s+(?:of\s+|&\s+|and\s+)?[A-Z][\w&'.-]*){0,3})`)
	trailingMarkRE  = lazyregexp.New(`[\s*]+$`)
	// immigrationLawRE names the law an authorization is under (Latitude AI:
	// "authorized under U.S. immigration laws to work"): it says nothing of the
	// answer, so the routes and the polarity readers read the text without it.
	immigrationLawRE = lazyregexp.New(`(?i)\s+under\s+(?:the\s+)?(?:u\.?s\.?(?:a\.?)?\s+|united\s+states\s+|federal\s+|applicable\s+)?immigration\s+laws?\b`)
	manySpacesRE     = lazyregexp.New(` {2,}`)
)

// notACompany are the capitalized words that open a place, a determiner or a
// generic noun rather than a company name.
var notACompany = setOf(
	"The", "A", "An", "Any", "All", "This", "That", "These", "Our", "Your", "Their", "Its",
	"Each", "Every", "US", "USA", "U.S.", "U.S.A.", "United", "America", "American", "Canada",
	"Canadian", "UK", "Britain", "Europe", "EU", "Mexico", "India", "China", "Federal", "State",
	"Local", "Government", "Yes", "No",
)

// stripCompanyClause removes the first "for/at/with <Company Name>" clause: the
// phrase must not open with a not-a-company word and must end at whitespace,
// ?.,;:*!) or the end of the text.
func stripCompanyClause(s string) string {
	for _, m := range companyClauseRE.FindAllStringSubmatchIndex(s, -1) {
		phrase := s[m[2]:m[3]]
		if notACompany[strings.Fields(phrase)[0]] {
			continue
		}
		if m[1] < len(s) && !strings.ContainsRune(" \t?.,;:*!)", rune(s[m[1]])) {
			continue
		}
		return s[:m[0]] + s[m[1]:]
	}
	return s
}

// routeVocabularyVariant returns the reworded form of a question for the second
// route sweep, and false when the rewording equals the input or is empty.
func routeVocabularyVariant(text string) (string, bool) {
	s := hedgeRE.ReplaceAllString(text, " ")
	s = emailSpellRE.ReplaceAllString(s, "email")
	s = usAbbrevRE.ReplaceAllString(s, "US")
	s = listLetterRE.ReplaceAllString(s, "$1")
	s = imperativeRE.ReplaceAllString(s, "")
	s = stripCompanyClause(immigrationLawRE.ReplaceAllString(s, ""))
	// The qualifiers of an indefinite work-authorization question; its resolver
	// reads them from the page's own text.
	s = fullTimeBasisRE.ReplaceAllString(indefiniteRE.ReplaceAllString(s, ""), "")
	s = trailingMarkRE.ReplaceAllString(s, "")
	s = strings.TrimSpace(manySpacesRE.ReplaceAllString(s, " "))
	if s == "" || s == strings.TrimSpace(text) {
		return "", false
	}
	return s, true
}
