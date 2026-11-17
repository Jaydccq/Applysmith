package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Open sets: a proper-noun list can omit a value, a closed demographic set
// cannot. "Other" is a list-gap choice only on a long proper-noun menu.

const openSetMinDistinct = 8

var (
	openSetIDs = setOf("most-recent-school", "undergraduate-school", "field-of-study-choice", "undergraduate-major", "current-company")
	otherLabel = lazyregexp.New(`(?i)^\s*other\s*(?:\((?:please\s+)?specify\)|[:\x{2026}]|-\s*please\s+specify)?\s*$`)
	// employerLabelRE is a question that is ENTIRELY an employer label: "What
	// notice must you give your current employer?" is not an employer picker.
	employerLabelRE = lazyregexp.New(`(?i)^(?:(?:what is|who is or was) your )?(?:current\s*(?:/\s*|or\s*)?(?:most recent|previous)?|most recent|present)\s+(?:employer|company)(?:\s*\(if applicable\))?\s*[*\x{2731}\s?]*$`)
)

// allowsEmployerSelection reports the stored employer name applying to its name
// picker: the current-company entry, fixed, allowing text, on a question that is
// entirely an employer label.
func allowsEmployerSelection(e *Entry, questionText string) bool {
	if e.ID != IDCurrentCompany || e.Kind != KindFixed {
		return false
	}
	if len(e.InputTypes) > 0 && !e.allowsInput(InputText) {
		return false
	}
	t := strings.TrimSpace(strings.TrimLeft(foldSpaces(questionText), "*\u2731 "))
	return employerLabelRE.MatchString(t)
}

// openSetOther returns the explicit "Other" option of a long proper-noun menu,
// or false. The list needs at least eight distinct real values, so a ten-row
// menu with two distinct values is not an open set.
func openSetOther(e *Entry, q Question) (Option, bool) {
	if !openSetIDs[e.ID] || e.ID == IDCurrentCompany && !allowsEmployerSelection(e, q.Text) {
		return Option{}, false
	}
	distinct := map[string]bool{}
	for _, o := range realOptions(q.Options) {
		v := o.Value
		if v == "" {
			v = o.Label
		}
		distinct[v] = true
	}
	if len(distinct) < openSetMinDistinct {
		return Option{}, false
	}
	for _, o := range realOptions(q.Options) {
		if otherLabel.MatchString(o.Label) {
			return o, true
		}
	}
	return Option{}, false
}

var (
	optionLetterPrefix = lazyregexp.New(`^\s*(?:\(?[A-Za-z]\)|[A-Za-z]\s*[-\x{2013}.:])\s+`)
	startsOtherRE      = lazyregexp.New(`(?i)^other\b`)
	notACategoryRE     = lazyregexp.New(`(?i)^(?:other|yes|no|none)\b`)
)

// categoryDichotomy reads a bare Yes/No fact on a two-option "<category> | Other"
// set: the category option means Yes and "Other" means No. Only a pair
// qualifies: exactly two real options, one starting with "Other" after an
// option-letter prefix, the other of two or more words that does not lead with
// other, yes, no or none.
func categoryDichotomy(answer string, opts []Option) (Option, bool) {
	yn := strings.ToLower(strings.TrimSpace(answer))
	if yn != "yes" && yn != "no" {
		return Option{}, false
	}
	real := realOptions(opts)
	if len(real) != 2 {
		return Option{}, false
	}
	bare := [2]string{}
	other := -1
	for i, o := range real {
		bare[i] = strings.TrimSpace(optionLetterPrefix.ReplaceAllString(o.Label, ""))
		if other < 0 && startsOtherRE.MatchString(bare[i]) {
			other = i
		}
	}
	if other < 0 {
		return Option{}, false
	}
	category := bare[1-other]
	if notACategoryRE.MatchString(category) || len(strings.Fields(category)) < 2 {
		return Option{}, false
	}
	if yn == "no" {
		return real[other], true
	}
	return real[1-other], true
}
