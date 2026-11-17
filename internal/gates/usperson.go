// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"applysmith/internal/lazyregexp"
)

var (
	usPersonRequired = compileAll(
		`\b(must|required to|need to|has to|have to|shall)\s+be\s+(an?\s+)?(us|united states)\s+persons?\b`,
		`\b(requires?|requirement:?|only|restricted to|limited to|open to)\s+(for\s+|of\s+)?(an?\s+)?(us|united states)\s+persons?\b`,
		`\b(us|united states)\s+persons?\s+(status\s+)?(is\s+|are\s+)?(required|mandatory|needed)\b`,
	)
	requirementNegated = lazyregexp.New(`(?i)\b(not|no)\b[^.]{0,25}\b(required|require|requirement|necessary|needed)\b`)
)

// RequiresUSPerson reports whether the text states, as a requirement, that
// US-person status is needed (export-controlled work). The requiring verb must
// sit next to the phrase; a mention of the term alone does not count.
func RequiresUSPerson(text string) bool {
	for _, s := range Sentences(text) {
		if anyMatch(usPersonRequired, s) && !requirementNegated.MatchString(s) {
			return true
		}
	}
	return false
}
