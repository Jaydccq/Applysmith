package match

import (
	"applysmith/internal/lazyregexp"
)

var (
	stateWordRE    = lazyregexp.New(`(?i)\bstate\b`)
	statePrefixRE  = lazyregexp.New(`(?i)\b(?:please|kindly)\s$`)
	stateSuffixRE  = lazyregexp.New(`(?i)^\s+(?:your|the|which|whether|why|how|what|if|type|reasons?)\b`)
	addressWordsRE = lazyregexp.New(`(?i)\b(?:address|street|city|province|zip|postal|country)\b`)
	dateWordsRE    = lazyregexp.New(`(?i)\b(?:start date|date available|available date)\b`)
	numberWordsRE  = lazyregexp.New(`(?i)\b(?:salary|compensation|pay|rate|wage|years?|number of)\b`)
	monthYearAskRE = lazyregexp.New(`(?i)\bmonths?\s*(?:and|&|/|,)\s*years?\b|\bmm\s*/\s*yyyy\b`)
	urlWordsRE     = lazyregexp.New(`(?i)\b(?:urls?|links?|websites?|linkedin|github|portfolio)\b`)
	// linkBoxRE is a label that names only a link ("Other URL", "GitHub URL",
	// "Personal Website URL", "LinkedIn profile"): up to three words before a
	// link word, no question.
	linkBoxRE        = lazyregexp.New(`(?i)^[\s*]*(?:[\w.+-]+\s+){0,3}(?:urls?|links?|websites?|web\s+site|linkedin|github|portfolio)(?:\s+(?:url|link|profile))?[\s*:]*(?:\(optional\))?[\s*]*$`)
	questionLeadRE   = lazyregexp.New(`(?i)^\W*(?:are|do|does|did|have|has|is|was|were|will|would|can|could|should|what|which|how|why|where|when|who|please|share|provide|enter|include|list|describe|tell)\b`)
	citizenshipWords = lazyregexp.New(`(?i)\b(?:citizenship|nationality)\b`)
	emailWordRE      = lazyregexp.New(`(?i)\be-?mail\b`)
)

// isLinkBox reports whether a text box's label names only a link, which makes
// it a URL box whatever its HTML type: Lever draws its link fields as plain
// text inputs (the Institute of Foundation Models' required "Other URL").
func isLinkBox(text string) bool {
	return linkBoxRE.MatchString(text) && !questionLeadRE.MatchString(text)
}

// namesAddressPart reports whether the text names an address part. The word
// "state" is a verb after "please" or "kindly" or before your, the, which,
// whether, why, how, what, if, type or reason(s), and is then not an address.
func namesAddressPart(text string) bool {
	if addressWordsRE.MatchString(text) {
		return true
	}
	for _, loc := range stateWordRE.FindAllStringIndex(text, -1) {
		if statePrefixRE.MatchString(text[:loc[0]]) || stateSuffixRE.MatchString(text[loc[1]:]) {
			continue
		}
		return true
	}
	return false
}

// inferInputType gives a control without an extractor-supplied input type one
// from its sanitized text and options: explicit type, checkbox, yes/no option
// set or single select, narrative wording as long text, date, number, url,
// citizenship or nationality as text, an e-mail address as text (the engine's
// email type is text too), address words, otherwise text.
func inferInputType(q Question) InputType {
	if q.InputType != "" {
		return q.InputType
	}
	switch q.Kind {
	case KindCheckbox:
		return InputCheckbox
	case KindSelect, KindRadioGroup, KindCustomListbox:
		if isYesNoOptionSet(q.Options) {
			return InputYesNo
		}
		return InputSingleSelect
	}
	t := q.Text
	switch {
	case isNarrativeText(t) || isCareerGoalQuestion(t):
		return InputLongText
	// A school's start date in a text box is no date picker (Jane Street:
	// "Current degree start date"): it takes the month and year as its end
	// date does.
	case dateWordsRE.MatchString(t) && !schoolDateRE.MatchString(t):
		return InputDate
	case numberWordsRE.MatchString(t) && !monthYearAskRE.MatchString(t):
		return InputNumber
	case urlWordsRE.MatchString(t):
		return InputURL
	case citizenshipWords.MatchString(t), emailWordRE.MatchString(t):
		return InputText
	case namesAddressPart(t):
		return InputAddress
	}
	return InputText
}
