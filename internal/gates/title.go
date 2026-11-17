// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

var (
	earlyCareer = lazyregexp.New(`(?i)\b(interns?|internship|new[ -]grad(uate)?s?|entry[ -]level|junior|jr\.?|early[ -]career|campus|apprentice(ship)?|graduate (program|role|engineer|developer))\b|\b(engineer|developer|analyst) (i|1)\b`)
	seniorWord  = lazyregexp.New(`(?i)\b(senior|sr\.?|staff|principal|lead|manager|director|architect|head of|vp|vice president)\b`)
	seniorLabel = map[string]bool{"senior": true, "lead": true, "staff": true, "principal": true, "manager": true, "director": true, "vp": true, "executive": true}
	// technicalStaff is "Member of Technical Staff", a title family that runs
	// from new graduates to senior engineers: its "Staff" is not the staff rung.
	technicalStaff = lazyregexp.New(`(?i)\b(?:members?\s+of\s+(?:the\s+)?)?technical\s+staff\b`)
)

// SeniorityText is a title as seniority words are read in it: "Member of
// Technical Staff" is taken out, so a level beside it ("Senior Member of
// Technical Staff") still reads and the family alone names no level.
func SeniorityText(title string) string {
	return strings.TrimSpace(technicalStaff.ReplaceAllString(title, " "))
}

// ClearlySenior reports whether a title is clearly senior. An early-career
// signal wins first, then a senior word in the title, then the structured
// seniority label.
func ClearlySenior(title, label string) bool {
	switch {
	case earlyCareer.MatchString(title):
		return false
	case seniorWord.MatchString(SeniorityText(title)):
		return true
	}
	return seniorLabel[label]
}
