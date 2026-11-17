package match

import (
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Variants a bare Yes or No answer is scored against. Matched as whole
// contiguous tokens, the longest variant first.
var (
	yesVariants = sortedVariants("yes", "yeah", "yep", "yup", "i certify", "i confirm", "i agree", "i consent", "i acknowledge", "i understand", "i meet", "i have", "i did", "i do",
		"i am", "i was", "i was a", "i have been", "accept", "agree", "confirm", "acknowledge", "true")
	// "will not require" is scoped to "require": a combined status ("authorized
	// to work and will not require sponsorship") must not read as a bare Yes.
	// "Not to my knowledge" is the No of a "To the best of your knowledge"
	// question (Booz Allen).
	noVariants = sortedVariants("no", "nope", "nah", "i have not", "i do not", "i don't", "i am not", "i did not", "i was not", "i have never",
		"will not require", "does not require", "decline", "false", "none of the above", "not to my knowledge")
	maybeVariants = sortedVariants("maybe", "perhaps", "possibly", "it depends", "depends", "unsure", "not sure")
	// hedgeVariants are the soft negatives of a graded ladder; only a LEADING one
	// disqualifies, since mid-sentence they are prose.
	hedgeVariants = sortedVariants("unlikely", "doubtful", "probably not", "likely not", "rarely")
)

// A "Yes" that is put off or made conditional answers a different question than
// a now-question asks, and "Not yet" is a soft no whatever follows it.
var (
	softNoLeadRE = lazyregexp.New(`^(?:not yet|not currently|not right now|not now|not at the moment|not at this time|no longer)\b`)
	deferralRE   = lazyregexp.New(`\b(?:but|however|except|unless|once|until|later|eventually|in the future|next year|after|when|if)\b|\bin 20\d\d\b`)
	// futureQuestionRE marks a question that itself asks about willingness, a
	// plan or a later time, which a deferral can answer.
	futureQuestionRE = lazyregexp.New(`(?i)\b(?:will|would|willing|able|can|could|ever|future|later|eventually|if|plan|intend|going to|wish|want|prefer|open to|interested)\b`)
	// pickGuidanceRE is a parenthesis that says when to pick the option, not a
	// condition on the answer (Zania: "Yes (Select this if you are a citizen,
	// permanent resident, or hold an open work permit like an EAD)."). Guidance
	// that picks it "only if" restricts the answer and stays. The label is
	// normalized, so the closing parenthesis may be trimmed.
	pickGuidanceRE = lazyregexp.New(`\(\s*(?:please\s+)?(?:select|choose|pick|check|tick|mark)\s+this(?:\s+option)?\s+if\b[^)]*(?:\)|$)`)
)

// conditionalDemoted keeps a deferred or conditional "Yes" below the clear-winner
// floor, so no lane picks it for the user.
const conditionalDemoted = 0.4

var wordRE = lazyregexp.New(`[\p{L}\p{N}]+`)

// variant is one entry of a variant list with its tokens.
type variant struct {
	text string
	toks []string
}

// sortedVariants tokenizes a variant list once, the longest variant first.
func sortedVariants(texts ...string) []variant {
	slices.SortStableFunc(texts, func(a, b string) int { return len(b) - len(a) })
	out := make([]variant, len(texts))
	for i, t := range texts {
		out[i] = variant{t, tokenize(t)}
	}
	return out
}

// variantHit is the first variant found in a label and the token index where
// it starts.
type variantHit struct {
	variant string
	start   int
}

// findVariantHit returns the longest variant occurring as whole tokens. A
// one-word variant glued to a hyphen ("no-code") is not a hit.
func findVariantHit(l *labelProfile, variants []variant) (variantHit, bool) {
	toks, lower := l.toks, strings.ToLower(l.norm)
	var offsets [][]int
	for _, v := range variants {
		need := v.toks
		if optRunStart(toks, need) < 0 {
			continue
		}
		if len(need) == 1 {
			saw, unglued := false, -1
			for i, t := range toks {
				if t != need[0] {
					continue
				}
				saw = true
				if offsets == nil {
					offsets = wordRE.FindAllStringIndex(lower, -1)
				}
				if i < len(offsets) && !hyphenGlued(lower, offsets[i][0], offsets[i][1]) {
					unglued = i
					break
				}
			}
			if unglued >= 0 {
				return variantHit{v.text, unglued}, true
			}
			if saw {
				continue
			}
		}
		return variantHit{v.text, optRunStart(toks, need)}, true
	}
	return variantHit{}, false
}

// hyphenGlued reports a word directly against a hyphen with no whitespace.
func hyphenGlued(label string, start, end int) bool {
	return (start > 0 && label[start-1] == '-') || (end < len(label) && label[end] == '-')
}

// scoreYesNo scores an option against a bare Yes or No answer. It reports false
// when the option holds no variant of either kind, so the containment rules run.
func scoreYesNo(l *labelProfile, yes bool) (optScore, bool) {
	label := l.norm
	if l.decline != "" {
		return optScore{0, fmt.Sprintf("rejected: decline synonym %q is not a yes/no variant", l.decline)}, true
	}
	if h, ok := findVariantHit(l, maybeVariants); ok && h.start == 0 {
		return optScore{0, fmt.Sprintf("rejected: option leads with MAYBE variant %q", h.variant)}, true
	}
	if h, ok := findVariantHit(l, hedgeVariants); ok && h.start == 0 {
		return optScore{0, fmt.Sprintf("rejected: option leads with negative-hedge variant %q", h.variant)}, true
	}
	yesHit, hasYes := findVariantHit(l, yesVariants)
	noHit, hasNo := findVariantHit(l, noVariants)
	leadsYes := len(l.toks) > 0 && slices.Contains([]string{"yes", "yeah", "yep", "yup"}, l.toks[0])
	if yes {
		switch {
		case softNoLeadRE.MatchString(label):
			return optScore{0, "rejected: option leads with a soft no (not yet)"}, true
		case (leadsYes || hasYes) && l.deferredOf():
			return optScore{conditionalDemoted, "deferred or conditional yes to a now-question: left for review"}, true
		case hasNo && !leadsYes:
			return optScore{0, fmt.Sprintf("rejected: option contains NO variant %q", noHit.variant)}, true
		case leadsYes && hasNo:
			return optScore{0.85, fmt.Sprintf("option leads with \"yes\" (later %q qualifies it)", noHit.variant)}, true
		case hasYes:
			return optScore{exactOr(label, yesHit.variant), fmt.Sprintf("yes-variant %q matched", yesHit.variant)}, true
		}
		return optScore{}, false
	}
	switch {
	case leadsYes:
		return optScore{0, "rejected: option leads with \"yes\""}, true
	case hasYes && !hasNo:
		return optScore{0, fmt.Sprintf("rejected: option contains YES variant %q", yesHit.variant)}, true
	case hasNo:
		return optScore{exactOr(label, noHit.variant), fmt.Sprintf("no-variant %q matched", noHit.variant)}, true
	}
	return optScore{}, false
}

func exactOr(label, variant string) float64 {
	if label == variant {
		return 0.95
	}
	return 0.85
}
