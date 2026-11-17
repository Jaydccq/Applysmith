package agent

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// excludeClause finds what a message asks to leave out of a batch: "but not
// Example Corp", "except Acme and Beta", "do not apply to Gamma". The list
// tool reads it from the user's own message, so a model cannot widen a batch
// the user narrowed.
var (
	excludeClause = lazyregexp.New(`(?i)\b(?:but not|except(?: for)?|excluding|exclude|skip|leave out|other than|(?:do not|don't|dont|never) apply (?:to|for))\s+(.+?)(?:[.;!?]|\s(?:but|then)\s|$)`)
	nameSplit     = lazyregexp.New(`(?i)\s*(?:,|\band\b|\bor\b)\s*`)
)

// excludedCompanies lists the company names a message excludes, lower-cased.
func excludedCompanies(message string) []string {
	var out []string
	for _, m := range excludeClause.FindAllStringSubmatch(urlText.ReplaceAllString(message, " "), -1) {
		for _, name := range nameSplit.Split(m[1], -1) {
			name = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "the "))
			if len(name) >= 2 {
				out = append(out, name)
			}
		}
	}
	return out
}

// isExcluded reports whether a company is one the user left out.
func isExcluded(company string, excluded []string) bool {
	c := strings.ToLower(company)
	for _, name := range excluded {
		if c == name || strings.Contains(c, name) {
			return true
		}
	}
	return false
}
