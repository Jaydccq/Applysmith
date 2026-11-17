package match

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Confidence a profile-grounded resolver starts from. A row of the profile and
// an answer read straight from a stored fact are certain; a date or a figure
// computed from the clock and a fact lands in the review band.
const (
	startProfileRow    = 0.90
	startProfileDerive = 0.95
	startProfileDate   = 0.85
)

// chain runs stages in order and returns the first that handles the question.
func chain(stages ...StageFunc) StageFunc {
	return func(s *Step) (Draft, bool) {
		for _, f := range stages {
			if d, ok := f(s); ok {
				return d, true
			}
		}
		return Draft{}, false
	}
}

// guarded runs the profile context policies on the classified entry before a
// kind resolver, so a country, currency or held-compensation block ends the
// walk with the terminal context-policy abstain whichever resolver the entry
// would have reached.
func guarded(f StageFunc) StageFunc {
	return func(s *Step) (Draft, bool) {
		if s.Entry != nil {
			if a, line, hit := profileContextGuards(s, s.Entry); hit {
				d := Draft{Abstain: &a}
				if line != "" {
					d.Trace = []string{line}
				}
				return d, true
			}
		}
		return f(s)
	}
}

// profileContextGuards are the context policies this family owns, in the order
// of the resolver order: posting country, a U.S. status asked of another
// country (no-entry, the one guard that is not terminal), local currency, held
// compensation, the employer-detail policy, the relocation list's open shared
// code, then another person's education.
func profileContextGuards(s *Step, e *Entry) (Abstain, string, bool) {
	for _, g := range []func(*Step, *Entry) (Abstain, string, bool){
		postingCountryGuard, foreignStatusGuard, currencyGuard, heldCompensationGuard, employerDetailGuard, sharedCodeGuard, othersEducationGuard,
	} {
		if a, line, hit := g(s, e); hit {
			return a, line, true
		}
	}
	return Abstain{}, "", false
}

// derivedDraft is the draft of an answer computed from profile facts. The
// synthetic entry carries only the answer key, so the result reports no entry
// id and the source is derived. start is the starting confidence.
func derivedDraft(answer, answerKey string, start float64, trace string) Draft {
	d := Draft{
		Entry:  &Entry{Meta: Meta{AnswerKey: answerKey}},
		Answer: answer,
		Source: SourceDerived,
		Start:  start,
	}
	if trace != "" {
		d.Trace = []string{trace}
	}
	return d
}

// abstainDraft ends the walk with an abstain and a trace line.
func abstainDraft(reason Reason, detail Detail, format string, args ...any) Draft {
	d := Draft{Abstain: &Abstain{Reason: reason, Detail: detail}}
	if format != "" {
		d.Trace = []string{fmt.Sprintf(format, args...)}
	}
	return d
}

// curatedVerbatimHit reports that a curated entry's example equals the question,
// which outranks every derived resolver of this family.
func curatedVerbatimHit(s *Step) bool {
	_, hit := newClassifier(s.Index, s.Q, s.scorer()).curated()
	return hit
}

// entryNamed finds an entry of the effective set by id.
func entryNamed(s *Step, id string) *Entry {
	for i := range s.Entries {
		if s.Entries[i].ID == id && !s.Entries[i].RunContext {
			return &s.Entries[i]
		}
	}
	return nil
}

// ownText is the single text value an entry stores: a fixed answer, the first
// preferred value of a pick-option entry or its fallback text. A placeholder,
// a blank value and the ask-me marker are not values.
func ownText(e *Entry) string {
	if e == nil {
		return ""
	}
	var v string
	switch e.Kind {
	case KindFixed:
		v = e.Answer
	case KindPickOption:
		for _, p := range e.Preferred {
			if strings.TrimSpace(p) != "" {
				v = p
				break
			}
		}
		if strings.TrimSpace(v) == "" {
			v = e.FallbackText
		}
	}
	v = strings.TrimSpace(v)
	if v == AskMe {
		return ""
	}
	return v
}

// entryText is ownText of the entry with the id, or "".
func entryText(s *Step, id string) string { return ownText(entryNamed(s, id)) }

// phraseAt is the byte offset of the first occurrence of phrase in text that
// fits its neighbours, or -1. fits gets the rune before and the rune after the
// occurrence; either end of the text is utf8.RuneError.
func phraseAt(text, phrase string, fits func(before, after rune) bool) int {
	if phrase == "" {
		return -1
	}
	for from := 0; ; {
		i := strings.Index(text[from:], phrase)
		if i < 0 {
			return -1
		}
		start, end := from+i, from+i+len(phrase)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if fits(before, after) {
			return start
		}
		from = start + 1
	}
}

// asciiWordRune is a rune of RE2's \b word class.
func asciiWordRune(r rune) bool {
	return r == '_' || r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// wordIndex is the byte offset of the first whole occurrence of phrase in
// text, or -1; both are compared as given. A letter, a digit or an underscore
// on either side, or a + or # after it, makes an occurrence part of a longer
// name ("c" in "c++", "cole" in "école"). Unlike \b, the boundary holds for a
// phrase that ends in a symbol and for letters outside ASCII.
func wordIndex(text, phrase string) int {
	inWord := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	return phraseAt(text, phrase, func(b, a rune) bool { return !inWord(b) && !inWord(a) && a != '+' && a != '#' })
}

// containsWord reports the phrase as whole words, case-insensitively: no
// letter, number or underscore touches it on either side, so a phrase that
// ends in a symbol still counts.
func containsWord(text, phrase string) bool {
	inWord := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }
	return phraseAt(strings.ToLower(text), strings.ToLower(strings.TrimSpace(phrase)), func(b, a rune) bool { return !inWord(b) && !inWord(a) }) >= 0
}

// chosenOption is a derived answer that chooses one rendered option, the one
// selection of a box group or multi-select.
func chosenOption(s *Step, o Option, trace string) Draft {
	d := derivedDraft(o.Value, "", startProfileDerive, trace+" -> "+quoteTerm(o.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore, d.Deterministic = o.Label, true, 1, true
	if s.Q.Kind == KindCheckbox || mselMultiAnswer(s.Q, len(s.Q.Options)) {
		d.ChosenLabels = []string{o.Label}
	}
	return d
}
