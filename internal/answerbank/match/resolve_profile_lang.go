package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// langVocabulary is the vocabulary of the named-language guard; it is also a
// signal that a label is about languages.
var langVocabulary = []string{
	"English", "Spanish", "French", "German", "Italian", "Portuguese", "Dutch", "Swedish", "Norwegian", "Danish",
	"Finnish", "Polish", "Czech", "Slovak", "Hungarian", "Romanian", "Bulgarian", "Greek", "Turkish", "Russian",
	"Ukrainian", "Arabic", "Hebrew", "Persian", "Farsi", "Hindi", "Urdu", "Bengali", "Punjabi", "Tamil", "Telugu",
	"Marathi", "Gujarati", "Mandarin", "Cantonese", "Chinese", "Japanese", "Korean", "Vietnamese", "Thai",
	"Indonesian", "Malay", "Tagalog", "Filipino", "Swahili",
}

var (
	langNameRE        = lazyregexp.New(`(?i)\b(?:` + strings.Join(langVocabulary, "|") + `)\b`)
	langWordRE        = lazyregexp.New(`\blanguages?\b`)
	langCEFRRE        = lazyregexp.New(`(?i)\b[abc][12]\b`)
	langRowSelectorRE = lazyregexp.New(`^language-\d+--`)
	langProficiencyRE = lazyregexp.New(`\b(?:proficiency|fluency|fluent|level|overall|rate|rating)\b`)
	langDirectionRE   = lazyregexp.New(`\b(?:speaking|spoken|listening|reading|writing)\b`)
	langPersonNameRE  = lazyregexp.New(`(?i)\b(?:first|last|middle|given|family|sur|preferred|full|legal)\s*name\b`)
	langModelRE       = lazyregexp.New(`(?i)\blarge\s+language\s+models?\b|\bllms?\b`)
	langWrittenRE     = lazyregexp.New(`\bwritten\b`)
	langFluentAskRE   = lazyregexp.New(`(?i)\bfluen(?:t|tly|cy)\b|\bnative\b|\bproficien(?:t|tly|cy)\b`)
	// langFirstLanguageRE is a question on a first language, which a fluent
	// level does not answer.
	// langEitherRE joins languages as alternatives, any of which answers.
	langEitherRE        = lazyregexp.New(`(?i)\bor\b`)
	langAllRE           = lazyregexp.New(`(?i)\b(?:and|both|plus)\b|&`)
	langFirstLanguageRE = lazyregexp.New(`(?i)\bnative\b|\bfirst\s+language\b|\bmother\s+tongue\b`)
	langBareSkillLabel  = map[string]string{
		"read": "reading", "reading": "reading", "speak": "speaking", "speaking": "speaking",
		"write": "writing", "writing": "writing", "listen": "listening", "listening": "listening",
	}
	// langCEFRRungs lists, best first, the CEFR level code and label word each
	// plain proficiency word maps to. Fluent is C1, never C2: C2 means mastery
	// and claiming it would inflate a factual claim.
	langCEFRRungs = map[string][]string{
		"native": {"c2", "proficient"}, "bilingual": {"c2", "proficient"},
		"fluent": {"c1", "advanced"}, "professional": {"c1", "advanced"}, "advanced": {"c1", "advanced"},
		"intermediate": {"b1", "intermediate"},
		"elementary":   {"a2", "elementary"},
		"beginner":     {"a1", "beginner"}, "basic": {"a1", "beginner"},
	}
)

// languageEntry resolves a language-proficiency entry. A question that names
// another language than the entry's is not answered; the field it reads
// follows the label, and a CEFR ladder gets the ladder's own wording.
func languageEntry(s *Step) (Draft, bool) {
	e, q := s.Entry, s.Q
	if e == nil || e.Kind != KindLanguageProficiency {
		return Draft{}, false
	}
	if langPersonNameRE.MatchString(q.Text) || langModelRE.MatchString(q.Text) {
		return Draft{}, false
	}
	if langNamesOnlyOthers(q.Text, e.Language) || langPairRE.MatchString(q.Text) {
		return abstainDraft(ReasonNoEntry, "", "kind=language-proficiency but the question names another language than %q", e.Language), true
	}
	if isYesNoGate(q) {
		return languageYesNo(s, e), true
	}
	if q.InputType == InputMultiSelect && spokenLanguageList(q) {
		return languageList(s, e), true
	}
	field := languageField(q.Text, q.SectionPath, q.Options, s.Raw.Selector)
	if field == "" {
		return abstainDraft(ReasonNoEntry, "", "kind=language-proficiency but no language/proficiency field label detected"), true
	}
	answer := strings.TrimSpace(languageFieldValue(e, field))
	if answer == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "kind=language-proficiency field=%s has no configured answer", field), true
	}
	if field != "language" {
		if label, ok := langCEFROption(answer, q.Options); ok {
			d := bindEntryAnswer(s, e, label, "kind=language-proficiency field="+field+" "+quoteTerm(answer)+" -> CEFR option "+quoteTerm(label))
			return d, true
		}
	}
	return bindEntryAnswer(s, e, answer, "kind=language-proficiency field="+field+" -> "+quoteTerm(answer)), true
}

// languageYesNo answers a yes/no question on working fluency in the entry's
// language ("Are you fluent in English?"): Yes when every recorded skill is
// fluent or better (CEFR C1 or C2). A lower level, a question on a first
// language, and one that also names a language the entry does not record,
// unless as an alternative ("English or Spanish"), are left to the owner.
func languageYesNo(s *Step, e *Entry) Draft {
	if langFirstLanguageRE.MatchString(s.Q.Text) || !langNameRE.MatchString(s.Q.Text) {
		return abstainDraft(ReasonUnsupported, "", "kind=language-proficiency: a yes/no question that names no language or asks for a first language")
	}
	for _, n := range langNameRE.FindAllString(s.Q.Text, -1) {
		if langNamesOnlyOthers(n, e.Language) && (langAllRE.MatchString(s.Q.Text) || !langEitherRE.MatchString(s.Q.Text)) {
			return abstainDraft(ReasonUnsupported, "", "kind=language-proficiency: the yes/no question also asks about %s, which is not recorded", n)
		}
	}
	levels := 0
	for _, v := range []string{e.Speaking, e.Listening, e.Reading, e.Writing} {
		if v = normalizeLabel(v); v == "" {
			continue
		}
		if rungs, ok := langCEFRRungs[v]; !ok || rungs[0] != "c1" && rungs[0] != "c2" {
			return abstainDraft(ReasonUnsupported, "", "kind=language-proficiency: the recorded level %q is below fluent", v)
		}
		levels++
	}
	if levels == 0 {
		return abstainDraft(ReasonPlaceholderEmpty, "", "kind=language-proficiency has no recorded level")
	}
	return bindEntryAnswer(s, e, "Yes", "kind=language-proficiency every recorded skill is fluent or better -> Yes")
}

// spokenLanguageList reports a list asking for languages whose options are
// mostly spoken languages, not programming languages.
func spokenLanguageList(q Question) bool {
	if !langWordRE.MatchString(normalizeLabel(q.Text)) {
		return false
	}
	real := realOptions(q.Options)
	spoken := 0
	for _, o := range real {
		if langNameRE.MatchString(o.Label) {
			spoken++
		}
	}
	return spoken >= 2 && spoken*2 >= len(real)
}

// languageList answers a list of the languages one speaks with the entry's
// language. When the list asks for fluent ones, the recorded speaking level
// must be fluent or better. The record holds one language, so the choice is
// for review: another the applicant speaks is not recorded.
func languageList(s *Step, e *Entry) Draft {
	level := normalizeLabel(e.Speaking)
	if langFluentAskRE.MatchString(s.Q.Text) {
		if rungs, ok := langCEFRRungs[level]; !ok || rungs[0] != "c1" && rungs[0] != "c2" {
			return abstainDraft(ReasonUnsupported, "", "kind=language-proficiency: a list of fluent languages, and the recorded speaking level %q is not fluent", e.Speaking)
		}
	}
	if strings.TrimSpace(e.Language) == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "kind=language-proficiency has no recorded language")
	}
	d := bindEntryAnswer(s, e, e.Language, "kind=language-proficiency: a list of the languages spoken -> the recorded "+quoteTerm(e.Language)+"; any other language spoken is not recorded, for review")
	d.Start = min(s.Class.start, FuzzyConfidenceCap)
	return d
}

func languageFieldValue(e *Entry, field string) string {
	switch field {
	case "speaking":
		return e.Speaking
	case "listening":
		return e.Listening
	case "reading":
		return e.Reading
	case "writing":
		return e.Writing
	}
	return e.Language
}

// langPairRE is two languages asked together ("English and Spanish"): the
// entry holds one language's level, and one answer would claim both.
var langPairRE = lazyregexp.New(`(?i)\b(?:` + strings.Join(langVocabulary, "|") + `)\s*(?:and|&|as\s+well\s+as)\s+(?:` + strings.Join(langVocabulary, "|") + `)\b`)

// langNamesOnlyOthers reports a question that names at least one language and
// none of them is the entry's (containment either way, lowercase).
func langNamesOnlyOthers(text, own string) bool {
	named := langNameRE.FindAllString(text, -1)
	if len(named) == 0 {
		return false
	}
	own = strings.ToLower(own)
	for _, n := range named {
		n = strings.ToLower(n)
		if strings.Contains(own, n) || strings.Contains(n, own) {
			return false
		}
	}
	return true
}

// langCEFROption translates a plain proficiency word onto the picker's own
// CEFR ladder: the first rung hit in the ladder's labels. A picker without a
// CEFR-looking label is untouched.
func langCEFROption(answer string, opts []Option) (string, bool) {
	cefr := false
	for _, o := range opts {
		if langCEFRRE.MatchString(o.Label) {
			cefr = true
			break
		}
	}
	rungs, known := langCEFRRungs[normalizeLabel(answer)]
	if !cefr || !known {
		return "", false
	}
	for _, rung := range rungs {
		for _, o := range opts {
			if strings.Contains(normalizeLabel(o.Label), rung) {
				return o.Label, true
			}
		}
	}
	return "", false
}

// languageField decides which entry field a question asks for: "language"
// (the language name), a skill, or "" when the question is no language
// question. A bare skill label ("Read") counts only inside a languages
// section, a CEFR ladder or a Workday languages row; an overall rating
// without a direction is the speaking level.
func languageField(text string, section []string, opts []Option, selector string) string {
	t := langWrittenRE.ReplaceAllString(normalizeLabel(text), "writing")
	inSection := false
	for _, part := range section {
		if langWordRE.MatchString(normalizeLabel(part)) {
			inSection = true
		}
	}
	ladder := false
	for _, o := range opts {
		if langCEFRRE.MatchString(o.Label) {
			ladder = true
		}
	}
	row := langRowSelectorRE.MatchString(selectorID(selector))
	hasLanguage := langWordRE.MatchString(t) || langNameRE.MatchString(text) || inSection || ladder || row
	if skill, bare := langBareSkillLabel[t]; bare {
		if inSection || ladder || row {
			return skill
		}
		return ""
	}
	proficiency := langProficiencyRE.MatchString(t)
	direction := langDirectionRE.MatchString(t)
	switch {
	case hasLanguage && proficiency && !direction:
		return "speaking"
	case hasLanguage && !direction, hasLanguage && !proficiency:
		return "language"
	case strings.Contains(" "+t+" ", " listening "):
		return "listening"
	case strings.Contains(" "+t+" ", " reading "):
		return "reading"
	case strings.Contains(" "+t+" ", " writing "):
		return "writing"
	case (strings.Contains(" "+t+" ", " speaking ") || strings.Contains(" "+t+" ", " spoken ")) && proficiency:
		return "speaking"
	case hasLanguage:
		return "language"
	}
	return ""
}
