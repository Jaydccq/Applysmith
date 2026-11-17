// Package learn turns what happens on application forms into proposals and
// evidence for the answer bank: the manual-fill corpus, learning candidates
// the user approves, the stale-entry lifecycle and usage telemetry. Nothing
// here writes the bank except through the answerbank service, and nothing
// approves a candidate without an explicit call.
package learn

import (
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// MinPostingSupport is the recurrence floor: a question needs to be asked on
// this many different postings before a new-entry candidate exists. A bank
// entry is an answer reused across applications; a question one posting asks,
// however often that posting is retried, is that application's own.
const MinPostingSupport = 3

// Normalize is the form every grouping, dedup key and collision test uses:
// lowercase, every character that is not a letter, digit or whitespace turned
// into a space, runs of whitespace collapsed, trimmed. It is for dedup only and
// never for matching. strings.Fields splits on every Unicode space, so a
// non-breaking space collapses like a plain one.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Key prefixes of a candidate.
const (
	newEntryPrefix = "new-entry:"
	extendPrefix   = "extend-examples:"
)

// NewEntryKey is the dedup key of a new-entry proposal.
func NewEntryKey(question string) string { return newEntryPrefix + Normalize(question) }

// ExtendExamplesKey is the dedup key of an example addition to one entry.
func ExtendExamplesKey(entryID, question string) string {
	return extendPrefix + entryID + ":" + Normalize(question)
}

// Placeholder parsing follows the learning contract, using the same Unicode
// whitespace and scalar-count rules as the browser's shared capture vectors.
func placeholderSpace(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }
func placeholderDash(r rune) bool  { return r == '-' || r == '\u2013' || r == '\u2014' }

// IsNothingChosen recognizes only control prompts and bounded dash wrappers.
// A prompt word inside an actual answer does not make that answer a prompt.
func IsNothingChosen(label string) bool {
	text := strings.TrimFunc(label, placeholderSpace)
	if strings.TrimFunc(text, func(r rune) bool {
		return placeholderSpace(r) || placeholderDash(r) || r == '.' || r == '\u2026'
	}) == "" {
		return true
	}

	chars := []rune(text)
	left, right := 0, len(chars)
	for left < right && placeholderDash(chars[left]) {
		left++
	}
	for right > left && placeholderDash(chars[right-1]) {
		right--
	}
	if left >= 2 && len(chars)-right >= 2 {
		body := []rune(strings.TrimFunc(string(chars[left:right]), placeholderSpace))
		if len(body) == 0 || len(body) > 40 {
			return false
		}
		for _, r := range body {
			if placeholderDash(r) {
				return false
			}
		}
		return true
	}

	// The prompt vocabulary is ASCII; Unicode case mappings differ between
	// Go and JavaScript (for example capital dotted I).
	text = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, text)
	text = strings.TrimRightFunc(text, func(r rune) bool {
		return placeholderSpace(r) || r == '.' || r == '\u2026'
	})
	words := strings.FieldsFunc(text, placeholderSpace)
	if len(words) == 0 {
		return false
	}
	if words[0] == "please" {
		if len(words) < 2 || (words[1] != "select" && words[1] != "choose") {
			return false
		}
		words = words[1:]
	}
	tail := strings.Join(words[1:], " ")
	switch words[0] {
	case "choose", "select":
		return tail == "" || tail == "one" || tail == "an option"
	case "make":
		return tail == "a selection" || tail == "a selection here"
	case "none":
		return tail == "" || tail == "selected"
	case "no":
		return tail == "selection"
	case "pick":
		return tail == "one"
	case "na", "n/a":
		return tail == ""
	}
	return false
}

// LongFormWords is the word count from which an answer with a sentence break
// is prose. The match-time long-form gate and this guard share the definition.
const LongFormWords = 30

// sentenceBreak needs something after the whitespace that opens a sentence; it
// consumes that character, which is harmless because the test is only "at
// least one".
var sentenceBreak = lazyregexp.New(`[.!?]+\s+[A-Z(0-9]`)

// IsLongFormProse reports an answer of at least LongFormWords words with a
// sentence break that has more content after it. A single long run-on clause,
// such as an address, does not trip it.
func IsLongFormProse(s string) bool {
	return len(strings.Fields(s)) >= LongFormWords && sentenceBreak.MatchString(s)
}

var (
	ratingShorthand = lazyregexp.New(`\(\s*\d\s*/\s*\d\s*\)`)
	markdownPaste   = lazyregexp.New(`(?m)(\*\*[^*]+\*\*|^#{1,6}\s|^[-*]\s)`)
)

// ReviewFlags are the warnings a reviewer sees beside a seeded answer. They
// never alter or reject the user's content.
func ReviewFlags(answer string) []string {
	var out []string
	if ratingShorthand.MatchString(answer) {
		out = append(out, "REVIEW: the answer holds rating shorthand such as (3/4)")
	}
	if markdownPaste.MatchString(answer) {
		out = append(out, "REVIEW: the answer holds markdown")
	}
	return out
}
