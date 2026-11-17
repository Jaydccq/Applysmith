package match

import (
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

var contentlessStrip = lazyregexp.New(`(?i)\(\s*(?:required|optional)\s*\)|\b(?:required|optional)\b`)

// Fold only the grammar's ASCII alphabet, after preserving Unicode separators.
func promptText(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, collapseSpaces(text))
}

// Control instructions name a choice action. Object-bearing instructions are
// furniture only when found in an option value, never in the question label.
func choicePrompt(text string, optionValue bool) bool {
	text = strings.Trim(text, "-–—.… ")
	switch text {
	case "", "make a selection", "none selected", "no selection", "not selected", "nothing selected":
		return true
	}
	words := strings.Fields(strings.TrimPrefix(text, "please "))
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "choose", "pick", "select":
	default:
		return false
	}
	tail := strings.Join(words[1:], " ")
	if tail == "" || tail == "one" || tail == "a option" || tail == "an option" {
		return true
	}
	if !optionValue || len(words) < 3 {
		return false
	}
	switch words[1] {
	case "a", "an", "one", "your", "the":
	default:
		return false
	}
	object := strings.Join(words[2:], " ")
	if len(object) > 24 {
		return false
	}
	for _, r := range object {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == ' ') {
			return false
		}
	}
	return true
}

// IsContentless reports whether a sanitized label is only the control's own
// prompt or format hint ("Choose...", "MM/DD/YYYY", "Select One Required", a bare
// star) and so names no question. A prompt with an object ("Select your gender")
// is a question.
func IsContentless(text string) bool {
	text = strings.NewReplacer("*", " ", "＊", " ").Replace(text)
	text = promptText(contentlessStrip.ReplaceAllString(promptText(text), " "))
	return choicePrompt(text, false) || controlFormatHint(text)
}

// IsPlaceholderValue reports whether a control value is the control's own prompt
// rather than a choice. Empty is false: callers decide what empty means.
func IsPlaceholderValue(value string) bool {
	return mayBePrompt(value) && placeholderValue(value)
}

// placeholderValue is IsPlaceholderValue without its cheap first test.
func placeholderValue(value string) bool {
	text := promptText(value)
	if text == "" {
		return false
	}
	if strings.Trim(text, "-–—._… ") == "" {
		return true
	}
	text = strings.TrimRight(text, "* ")
	return text != "" && choicePrompt(text, true)
}

// promptOpeners are the words a value IsPlaceholderValue accepts can open with
// once its leading punctuation is gone: choicePrompt's verbs, its "please", and
// the first words of its fixed phrases ("make a selection", "no", "none",
// "not" and "nothing selected").
var promptOpeners = []string{"choose", "pick", "select", "please", "make", "no"}

// mayBePrompt is a cheap necessary condition of IsPlaceholderValue: with its
// spaces folded as promptText folds them, and past its leading punctuation and
// spaces, a prompt is empty or opens with one of the promptOpeners in any case.
// Every option of every control is asked whether it is a placeholder, so a
// long label that fails this skips collapsing and lowercasing the whole text.
func mayBePrompt(value string) bool {
	rest := strings.TrimLeftFunc(foldSpaces(value), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("-–—._…*", r)
	})
	if rest == "" {
		return true
	}
	for _, w := range promptOpeners {
		if len(rest) >= len(w) && strings.EqualFold(rest[:len(w)], w) {
			return true
		}
	}
	return false
}

// Format hints describe the control, not the applicant. Dates accept only
// format symbols; a real date or a question naming a date keeps its meaning.
func controlFormatHint(text string) bool {
	text = strings.TrimSpace(strings.TrimRight(text, ".… "))
	for _, prefix := range []string{"e.g.", "ex.", "example:"} {
		if rest, ok := strings.CutPrefix(text, prefix); ok && strings.TrimSpace(rest) != "" {
			return true
		}
	}
	instruction := strings.TrimSuffix(text, " here")
	switch instruction {
	case "type", "type response", "type answer", "type your response", "type your answer", "enter text", "enter value":
		return instruction != "type" || strings.HasSuffix(text, " here")
	}
	verb, rest, _ := strings.Cut(text, " ")
	switch verb {
	case "choose", "enter", "pick", "select":
		if rest == "date" || rest == "a date" {
			return true
		}
	}
	parts := strings.Split(strings.NewReplacer("/", "|", ".", "|", "-", "|").Replace(text), "|")
	if len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		switch strings.TrimSpace(part) {
		case "dd", "mm", "mmm", "yy", "yyyy":
		default:
			return false
		}
	}
	return true
}
