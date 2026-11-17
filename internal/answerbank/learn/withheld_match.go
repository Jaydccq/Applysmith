package learn

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// foldedText is a piece of page text prepared for term matching. Two readings
// are kept because an invisible format character may separate words or hide
// inside one: one drops such characters, one turns them into spaces.
type foldedText struct {
	padded [2]string // words joined by single spaces, with a space at each end
	tight  [2]string // the same with every space removed
}

func newFoldedText(s string) foldedText {
	var f foldedText
	for i, invisibleAsSpace := range []bool{false, true} {
		w := fold(s, invisibleAsSpace)
		f.padded[i] = " " + w + " "
		f.tight[i] = strings.ReplaceAll(w, " ", "")
	}
	return f
}

// fold lowercases, strips accents and width (Hangul is left whole), maps the
// Latin letters that have no decomposition to their base letters, turns
// everything that is not a letter or digit into a space, and joins a run of
// single letters ("p a s s w o r d") back into one word.
func fold(s string, invisibleAsSpace bool) string {
	var b strings.Builder
	space := true
	put := func(r rune) {
		// A spacing combining mark is part of a Devanagari word.
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mc, r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	for _, r := range strings.ToLower(strings.ToValidUTF8(s, " ")) {
		switch {
		case unicode.Is(unicode.Hangul, r):
			put(r)
		case latinBase[r] != "":
			for _, d := range latinBase[r] {
				put(d)
			}
		case isInvisible(r):
			if invisibleAsSpace {
				put(' ')
			}
		default:
			for _, d := range norm.NFKD.String(string(r)) {
				if !unicode.Is(unicode.Mn, d) {
					put(unicode.ToLower(d))
				}
			}
		}
	}
	return joinLetterRuns(strings.TrimSpace(b.String()))
}

// latinBase maps the letters Unicode does not decompose to what a reader
// without the keyboard would type.
var latinBase = map[rune]string{'ß': "ss", 'ł': "l", 'ø': "o", 'đ': "d", 'ı': "i", 'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d"}

// isInvisible reports a format character: a zero-width space or joiner, a
// byte-order mark, a soft hyphen or a bidi mark. It carries nothing a reader
// sees, so every comparison drops it.
func isInvisible(r rune) bool { return unicode.Is(unicode.Cf, r) }

// minLetterRun is how many single-letter words in a row are read as one word
// spelled out.
const minLetterRun = 3

// joinLetterRuns joins a run of single-letter words, so "p a s s w o r d" and
// "p.a.s.s" read as the word they spell. Two single letters stay apart.
func joinLetterRuns(w string) string {
	words := strings.Fields(w)
	var out []string
	for i := 0; i < len(words); {
		j := i
		for j < len(words) && utf8.RuneCountInString(words[j]) == 1 && unicode.IsLetter([]rune(words[j])[0]) {
			j++
		}
		if j-i >= minLetterRun {
			out = append(out, strings.Join(words[i:j], ""))
			i = j
			continue
		}
		out = append(out, words[i])
		i++
	}
	return strings.Join(out, " ")
}

// foldAnswer is the form two answers are compared in: format characters
// dropped, width and compatibility forms unified, whitespace (a no-break space
// included) collapsed, lowercased. It keeps accents, because "Sao" and "São"
// are different answers.
func foldAnswer(s string) string {
	s = strings.Map(func(r rune) rune {
		if isInvisible(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, " "))
	return strings.ToLower(strings.Join(strings.Fields(norm.NFKC.String(s)), " "))
}

// term is a folded vocabulary entry.
type term struct {
	text string
	// kind is how the text must sit in the question.
	kind termKind
}

type termKind int

const (
	termWhole termKind = iota // whole words
	termStem                  // the last word may continue
	termSub                   // a substring of the space-free text (CJK scripts)
	termLabel                 // the whole question, the last word free to continue
)

// minStemRunes is the length from which a Latin last word may continue.
const minStemRunes = 5

func compileTerm(raw string) term {
	if label, ok := strings.CutPrefix(raw, "^"); ok {
		return term{text: fold(strings.TrimSuffix(label, "*"), false), kind: termLabel}
	}
	whole, stem := strings.HasPrefix(raw, "="), strings.HasSuffix(raw, "*")
	text := fold(strings.TrimSuffix(strings.TrimPrefix(raw, "="), "*"), false)
	for _, r := range text {
		if r > 0x2E7F {
			return term{text: strings.ReplaceAll(text, " ", ""), kind: termSub}
		}
	}
	last := text[strings.LastIndexByte(text, ' ')+1:]
	switch {
	case stem, !whole && utf8.RuneCountInString(last) >= minStemRunes:
		return term{text: text, kind: termStem}
	}
	return term{text: text, kind: termWhole}
}

func (f foldedText) has(t term) bool {
	for i := range f.padded {
		var ok bool
		switch t.kind {
		case termSub:
			ok = strings.Contains(f.tight[i], t.text)
		case termStem:
			ok = strings.Contains(f.padded[i], " "+t.text)
		case termLabel:
			rest, found := strings.CutPrefix(f.padded[i], " "+t.text)
			ok = found && !strings.Contains(strings.TrimSuffix(rest, " "), " ")
		default:
			ok = strings.Contains(f.padded[i], " "+t.text+" ")
		}
		if ok {
			return true
		}
	}
	return false
}

func (f foldedText) hasAny(ts []term) bool { return slices.ContainsFunc(ts, f.has) }

type compiledGroup struct {
	termGroup
	all []term
}

var (
	groups      []compiledGroup
	optionTable []term
)

func init() {
	for _, g := range termGroups {
		cg := compiledGroup{termGroup: g}
		for _, lang := range languages {
			for _, raw := range g.terms[lang] {
				cg.all = append(cg.all, compileTerm(raw))
			}
		}
		groups = append(groups, cg)
	}
	for _, lang := range languages {
		for _, raw := range optionTerms[lang] {
			optionTable = append(optionTable, compileTerm(raw))
		}
	}
}

// termGroupOf returns the first group whose vocabulary the question uses.
func termGroupOf(question string) (termGroup, bool) {
	f := newFoldedText(question)
	for _, g := range groups {
		if f.hasAny(g.all) {
			return g.termGroup, true
		}
	}
	return termGroup{}, false
}

// minSelfIDOptions is how many options must carry a self-identification label
// for the list to be read as a self-identification set.
const minSelfIDOptions = 2

// optionsLookSelfID reports an option list whose labels read as the answers of
// a gender, orientation, race, veteran or disability question in any of the
// supported languages.
func optionsLookSelfID(options []string) bool {
	hits := 0
	for _, o := range options {
		if newFoldedText(o).hasAny(optionTable) {
			hits++
		}
	}
	return hits >= minSelfIDOptions
}

// valueIsSelfIDOption reports a value or chosen label that is itself a
// self-identification answer, on a control that has options. One such label is
// enough: the options a client sends may be cut down to the chosen one.
func valueIsSelfIDOption(options []string, values ...string) bool {
	if len(options) == 0 {
		return false
	}
	return slices.ContainsFunc(values, func(v string) bool {
		return v != "" && newFoldedText(v).hasAny(optionTable)
	})
}
