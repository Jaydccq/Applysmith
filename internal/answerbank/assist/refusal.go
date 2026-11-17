package assist

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// NoAnswer is the sentinel the composer is told to emit when the facts do not
// cover the question. Whatever the model emits is typed as the applicant's own
// words, so a refusal must be recognized, not merely requested in prose.
const NoAnswer = "NO_ANSWER"

var (
	noAnswerLeadRE = lazyregexp.New(`^\W*NO_ANSWER\b`)
	asAIRE         = lazyregexp.New(`(?i)\bas an? (?:ai(?: language model)?|language model|large language model)\b`)
	deferralRE     = lazyregexp.New(`(?i)\b(?:can|could|will|'ll|happy to|glad to)\b[^.!?]{0,60}\b(?:share|provide|send|give)\b[^.!?]{0,60}\b(?:directly|upon request|on request|if you need|for your records)\b|\breach me (?:through|via) the (?:application )?(?:portal|platform|system|site)\b`)
	inabilityRE    = lazyregexp.New(`(?i)\bI (?:don'?t|do not) have\b|\bI (?:can'?t|cannot|can not)\b|\bI(?:'m| am) (?:unable|not able)\b`)
	metaObjectRE   = lazyregexp.New(`(?i)\b(?:enough (?:context|information|details?)|answer (?:this|that|the) (?:question|one)|previous answer|could you (?:please )?(?:provide|clarify|specify)|more (?:details|information|context) about what you(?:'re| are) looking for|please (?:provide|clarify|specify) (?:more|additional|further)|(?:details|information|info) (?:provided|given|available|shared)(?: here| to me| above)?|not (?:provided|included|listed|available|given)(?: in| here)?|reach me (?:through|via)|make (?:something|anything) up|no (?:job description|information|details?|context) (?:was|were|is|has been) (?:provided|given|shared|included))\b`)
	contrastRE     = lazyregexp.New(`(?i)\b(?:but|though|however|although)\b`)
)

func normalizeApostrophes(s string) string {
	s = strings.NewReplacer("\u2019", "'", "\u2018", "'").Replace(s)
	return collapse(s)
}

// isModelRefusal reports a completion that declines instead of answering. It
// runs on the final text in every grounding mode: a refusal asserts nothing, so
// the fact check can never catch it.
func isModelRefusal(text string) bool {
	t := normalizeApostrophes(text)
	switch {
	case noAnswerLeadRE.MatchString(t):
		return true
	case asAIRE.MatchString(t):
		return true
	case wordCount(t) <= 40 && deferralRE.MatchString(t):
		return true
	}
	inabilities := inabilityRE.FindAllStringIndex(t, -1)
	if len(inabilities) == 0 {
		return false
	}
	if metaObjectRE.MatchString(t) {
		return true
	}
	// Two or more inability clauses with no contrast that opens the answer or
	// fits inside forty words: a long answer with incidental hedges that pivots
	// to content is kept.
	if len(inabilities) >= 2 {
		if wordCount(t) <= 40 && !contrastRE.MatchString(t) {
			return true
		}
		first := inabilities[0][0] == 0 || wordCount(t[:inabilities[0][0]]) <= 3
		if first && !contrastBefore(t, 40) {
			return true
		}
	}
	return false
}

// contrastBefore reports a contrastive word within the first n words.
func contrastBefore(t string, n int) bool {
	words := strings.Fields(t)
	if len(words) > n {
		words = words[:n]
	}
	return contrastRE.MatchString(strings.Join(words, " "))
}
