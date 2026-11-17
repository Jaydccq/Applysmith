// Clean-room: docs/provenance.md#CR-AGENT-LOOP

package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

const (
	// MaxStepRunes is the longest status phrase the stream shows.
	MaxStepRunes = 60
	// unsafeStepRunes is the length from which a phrase is not trusted to be a
	// phrase at all.
	unsafeStepRunes = 200
)

// protectedPath matches a local path into a home directory, a system
// directory or the product's own state and key files, none of which belongs in
// a progress line.
var protectedPath = lazyregexp.New(`(?i)(?:^|[\s"'(])(?:~|/(?:users|home|etc|var|private|root|opt)\b|[a-z]:\\)|\.applysmith\b|\.ssh\b|\.env\b|id_rsa|id_ed25519`)

// StepPhrase turns the model's optional step argument into the progress line
// of a call: control characters removed, cut to 60 characters, and replaced by
// the tool's fixed label when absent, long enough to be something else, or
// shaped like a credential or a protected path.
func StepPhrase(raw, label string) string {
	if utf8.RuneCountInString(raw) >= unsafeStepRunes {
		return label
	}
	clean := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	if clean == "" || hasCredentialShape(clean) || protectedPath.MatchString(clean) {
		return label
	}
	if r := []rune(clean); len(r) > MaxStepRunes {
		clean = strings.TrimSpace(string(r[:MaxStepRunes]))
	}
	return clean
}
