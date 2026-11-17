package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ModelResultLimit is how many characters of a tool result the model sees.
const ModelResultLimit = 12000

// cutRunes shortens s to n characters and says how much was left out.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + fmt.Sprintf("\n[truncated: %d more characters]", len(r)-n)
}

// clipRunes shortens s to at most n characters, the last being an ellipsis when
// something was left out. It is for text a person reads (a conversation title,
// a card summary, a status line); text the model reads uses cutRunes, whose
// marker tells the model how much it did not get.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

// cutLines shortens s to n characters at the end of a whole line, so a result
// is never cut in the middle of a row. A first line longer than the limit, or a
// line break so early that most of the result would be lost, is cut where the
// limit falls.
func cutLines(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	kept := r[:n]
	if r[n] != '\n' {
		for i := n - 1; i >= n/2; i-- {
			if r[i] == '\n' {
				kept = r[:i]
				break
			}
		}
	}
	return string(kept) + fmt.Sprintf("\n[truncated: %d more characters]", len(r)-len(kept))
}

// cutBytes keeps at most n bytes of s without splitting a character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// oneLine collapses whitespace so a result fits a summary line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
