package cvparse

import (
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

var (
	// nameWordRE is one word of a name as a CV heads it: capitalised letters,
	// with an apostrophe, a hyphen or an initial's dot.
	nameWordRE = lazyregexp.New(`^\p{Lu}[\p{L}'’.-]*$`)
	// bulletMarks lead a list item in the text of a PDF or DOCX (U+F0B7 is
	// Word's Symbol-font bullet).
	bulletMarks = "•●▪◦‣■·\uf0b7"
	// entryLineRE is an entry as cv.md heads one: "Title, Organisation (2021-03
	// to present)", the brackets holding a date range with a year.
	entryLineRE = lazyregexp.New(`(?i)^[^,()]+,[^()]+\(\s*(?:[a-z]+\.?\s+)?(?:19|20)\d{2}(?:-\d{1,2})?\s*(?:to|-|–|—)\s*(?:(?:[a-z]+\.?\s+)?(?:19|20)\d{2}(?:-\d{1,2})?|present|current|now)\s*\)$`)
)

// Markdown marks up a plain-text CV, such as the text of a PDF or DOCX, the
// way cv.md is read: its first line, when it reads as a name, becomes the name
// heading; a whole-line section label a section heading; under a section, a
// line shaped "Title, Organisation (2021-03 to present)" an entry heading; a
// line led by a bullet character a list item. The words and their order are unchanged, and a
// text that already has a Markdown heading is returned as it is.
func Markdown(text string) string {
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		if headingRE.MatchString(strings.TrimRight(l, " \t\r")) {
			return text
		}
	}
	first, inSection := true, false
	for i, raw := range lines {
		trim := strings.TrimSpace(raw)
		if trim == "" {
			continue
		}
		if first {
			first = false
			if isName(trim) {
				lines[i] = "# " + trim
				continue
			}
		}
		if _, ok := sectionHeading(trim); ok {
			lines[i] = "## " + strings.TrimSpace(strings.TrimSuffix(trim, ":"))
			inSection = true
			continue
		}
		if inSection && entryLineRE.MatchString(trim) {
			// A blank line closes the heading, as cv.md writes one: the lines
			// under it are what the entry did, not its role, place or dates.
			lines[i] = "### " + trim
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
				lines[i] += "\n"
			}
			continue
		}
		if r, size := utf8.DecodeRuneInString(trim); strings.ContainsRune(bulletMarks, r) && strings.TrimSpace(trim[size:]) != "" {
			lines[i] = "- " + strings.TrimSpace(trim[size:])
		}
	}
	return strings.Join(lines, "\n")
}

// isName reports whether a CV's first line is a person's name: two to five
// capitalised words and nothing else, so a contact line, a title such as
// "Curriculum Vitae" or a running header is left as it is.
func isName(line string) bool {
	words := strings.Fields(line)
	if len(words) < 2 || len(words) > 5 || cvTitle.MatchString(strings.Join(words, " ")) {
		return false
	}
	for _, w := range words {
		if !nameWordRE.MatchString(w) {
			return false
		}
	}
	return true
}
