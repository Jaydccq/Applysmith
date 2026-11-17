package assist

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Work authorization is a structured fact, never composed. A composed answer
// says nothing about it unless the question asked, and never asserts a position
// the user's recorded status does not state.

var (
	// Acronyms are case-sensitive so "opt in" survives.
	visaAcronymRE = lazyregexp.New(`\b(?:F-?1|OPT|CPT|H-?1B|STEM OPT|TN|L-?1|O-?1)\b`)
	visaWordsRE   = lazyregexp.New(`(?i)\b(?:visa|sponsorship|sponsor(?:ed)?|work authori[sz]ation|authori[sz]ed to work|immigration)\b`)

	citizenClaimRE = lazyregexp.New(`(?i)\b(?:u\.?s\.?\s+citizen|citizen of(?: the)?\s+\w+|permanent resident|green card|naturali[sz]ed|lawful permanent)\b`)
	authClaimRE    = lazyregexp.New(`(?i)\b(?:(?:am|is|are|be|being)\s+)?(?:authori[sz]ed|eligible|permitted|legally able|legally allowed)\s+to\s+work\b`)
	needSponsorRE  = lazyregexp.New(`(?i)\b(?:(?:do|does|will|won'?t|currently|future)\s+(?:not\s+)?(?:need|require)|need|requires?|requiring|needing)\s+(?:\w+\s+)?sponsorship\b|\bno\s+(?:need\s+for\s+)?sponsorship\b|\bwithout\s+sponsorship\b`)
	negatedSponsor = lazyregexp.New(`(?i)\b(?:do(?:es)?\s+not|don'?t|won'?t|will\s+not|no\s+need|without|never)\b[^.!?]{0,30}\bsponsorship\b|\bno\s+sponsorship\b`)

	statusVocabRE = lazyregexp.New(`(?i)\b(?:authori[sz]\w*|eligible|permitted|sponsor\w*|visa|opt|cpt|ead|citizen\w*|resident|green card)\b`)
)

// sentenceMentionsWorkAuth reports a sentence about immigration or sponsorship.
func sentenceMentionsWorkAuth(s string) bool {
	return visaAcronymRE.MatchString(s) || visaWordsRE.MatchString(s)
}

// stripUnaskedWorkAuth removes the whole sentences that mention work
// authorization from an answer to a question that did not ask. The splitter is
// abbreviation-aware, so "I hold a B.S. and require sponsorship." is one
// sentence.
func stripUnaskedWorkAuth(answer string) string {
	var kept []string
	removed := false
	for _, s := range sentences(answer) {
		if sentenceMentionsWorkAuth(s) {
			removed = true
			continue
		}
		kept = append(kept, s)
	}
	if !removed {
		return answer
	}
	return joinSentences(kept)
}

// ungroundedImmigrationClaim reports an answer that asserts citizenship,
// residency, a work authorization or a sponsorship position the recorded status
// does not state. An empty status states nothing, so any such claim is one.
func ungroundedImmigrationClaim(answer, status string) bool {
	st := strings.ToLower(status)
	for _, m := range citizenClaimRE.FindAllString(answer, -1) {
		if !strings.Contains(st, strings.ToLower(m)) {
			return true
		}
	}
	claimsAuth := authClaimRE.MatchString(answer)
	claimsSponsor := needSponsorRE.MatchString(answer)
	if !claimsAuth && !claimsSponsor {
		return false
	}
	if !statusVocabRE.MatchString(status) {
		return true
	}
	// Polarity: a claim that negates sponsorship against a status that does not,
	// or the reverse.
	if claimsSponsor {
		answerNegates := negatedSponsor.MatchString(answer)
		statusNegates := negatedSponsor.MatchString(status)
		if answerNegates != statusNegates {
			return true
		}
	}
	return false
}
