package match

import "slices"

// kindFields lists the answer-field keys of each kind in file order.
var kindFields = map[Kind][]string{
	KindFixed:               {"answer"},
	KindPickOption:          {"preferred", "fallback_text", "fallback_any_option"},
	KindCompensationRange:   {"minimum", "maximum", "default_answer", "currency"},
	KindLanguageProficiency: {"language", "speaking", "listening", "reading", "writing", "proficiency_options"},
	KindEmployerHistory:     {"answer_if_match", "answer_if_no_match", "abstain_if_ambiguous"},
	KindAutoAcknowledge:     {},
	KindMultiSelect:         {"selected"},
}

// kindRequired lists the keys a file must state for the kind. A multi-select
// states `selected` only when it is answered.
var kindRequired = map[Kind][]string{
	KindFixed:               {"answer"},
	KindPickOption:          {"preferred", "fallback_text"},
	KindCompensationRange:   {"minimum", "maximum", "default_answer", "currency"},
	KindLanguageProficiency: {"language", "speaking", "listening", "reading", "writing"},
	KindEmployerHistory:     {"answer_if_match", "answer_if_no_match"},
}

// BodyKeys returns the answer-field keys of kind k in file order.
func BodyKeys(k Kind) []string { return append([]string(nil), kindFields[k]...) }

// RequiredKeys returns the keys a file must state for kind k.
func RequiredKeys(k Kind) []string { return append([]string(nil), kindRequired[k]...) }

// copyField copies the answer field named key from src to dst. Unknown keys
// are ignored.
func copyField(dst *Body, src Body, key string) {
	switch key {
	case "answer":
		dst.Answer = src.Answer
	case "preferred":
		dst.Preferred = entryCloneStrings(src.Preferred)
	case "fallback_text":
		dst.FallbackText = src.FallbackText
	case "fallback_any_option":
		dst.FallbackAnyOption = src.FallbackAnyOption
	case "minimum":
		dst.Minimum = src.Minimum
	case "maximum":
		dst.Maximum = src.Maximum
	case "default_answer":
		dst.DefaultAnswer = src.DefaultAnswer
	case "currency":
		dst.Currency = src.Currency
	case "language":
		dst.Language = src.Language
	case "speaking":
		dst.Speaking = src.Speaking
	case "listening":
		dst.Listening = src.Listening
	case "reading":
		dst.Reading = src.Reading
	case "writing":
		dst.Writing = src.Writing
	case "proficiency_options":
		dst.ProficiencyOptions = entryCloneStrings(src.ProficiencyOptions)
	case "answer_if_match":
		dst.AnswerIfMatch = src.AnswerIfMatch
	case "answer_if_no_match":
		dst.AnswerIfNoMatch = src.AnswerIfNoMatch
	case "abstain_if_ambiguous":
		if src.AbstainIfAmbiguous != nil {
			v := *src.AbstainIfAmbiguous
			dst.AbstainIfAmbiguous = &v
		} else {
			dst.AbstainIfAmbiguous = nil
		}
	case "selected":
		dst.Selected = entryCloneStrings(src.Selected)
	}
}

// cloneStrings copies a slice, keeping nil and empty distinct.
func entryCloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string{}, s...)
}

// allBodyKeys lists every answer-field key of every kind, in a fixed order.
var allBodyKeys = []string{
	"answer", "preferred", "fallback_text", "fallback_any_option", "minimum", "maximum", "default_answer",
	"currency", "language", "speaking", "listening", "reading", "writing", "proficiency_options",
	"answer_if_match", "answer_if_no_match", "abstain_if_ambiguous", "selected",
}

// fieldSet reports whether the answer field named key holds a value. A list
// that is present counts, even when empty.
func (b Body) fieldSet(key string) bool {
	switch key {
	case "answer":
		return b.Answer != ""
	case "preferred":
		return b.Preferred != nil
	case "fallback_text":
		return b.FallbackText != ""
	case "fallback_any_option":
		return b.FallbackAnyOption
	case "minimum":
		return b.Minimum != ""
	case "maximum":
		return b.Maximum != ""
	case "default_answer":
		return b.DefaultAnswer != ""
	case "currency":
		return b.Currency != ""
	case "language":
		return b.Language != ""
	case "speaking":
		return b.Speaking != ""
	case "listening":
		return b.Listening != ""
	case "reading":
		return b.Reading != ""
	case "writing":
		return b.Writing != ""
	case "proficiency_options":
		return b.ProficiencyOptions != nil
	case "answer_if_match":
		return b.AnswerIfMatch != ""
	case "answer_if_no_match":
		return b.AnswerIfNoMatch != ""
	case "abstain_if_ambiguous":
		return b.AbstainIfAmbiguous != nil
	case "selected":
		return b.Selected != nil
	}
	return false
}

// Stray lists the answer fields that hold a value although kind k does not use
// them, in file order.
func (b Body) Stray(k Kind) []string {
	var out []string
	for _, key := range allBodyKeys {
		if b.fieldSet(key) && !slices.Contains(kindFields[k], key) {
			out = append(out, key)
		}
	}
	return out
}

// KeepOnly returns the body with every field kind k does not use cleared.
func (b Body) KeepOnly(k Kind) Body {
	out := b
	for _, key := range allBodyKeys {
		if !slices.Contains(kindFields[k], key) {
			copyField(&out, Body{}, key)
		}
	}
	return out
}
