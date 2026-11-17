package assist

import (
	"math"
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// Length model: the word budget is the real controller; the character ceiling
// is twice what the budget implies, so it is a runaway net and never a silent
// trimmer.
const (
	charsPerWord      = 6.2
	minWords          = 20
	maxWords          = 500
	companyWords      = 90
	narrativeWords    = 200
	defaultWords      = 100
	maxLengthFraction = 0.85
)

var (
	explicitWordsRE      = lazyregexp.New(`(?i)\b(\d{1,4})\s*(?:-|to|\x{2013})?\s*(\d{1,4})?\s*words?\b`)
	explicitSentencesRE  = lazyregexp.New(`(?i)\b(\d{1,2})(?:\s*(?:-|to|\x{2013})\s*(\d{1,2}))?\s*sentences?\b`)
	explicitParagraphsRE = lazyregexp.New(`(?i)\b(\d{1,2})(?:\s*(?:-|to|\x{2013})\s*(\d{1,2}))?\s*paragraphs?\b`)
)

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func upper(a, b string) int {
	if b != "" {
		return atoi(b)
	}
	return atoi(a)
}

func clampWords(n int) int { return max(minWords, min(maxWords, n)) }

var spelledCountRE = lazyregexp.New(`\b(one|two|three|four|five|six|seven|eight|nine|ten)\b`)

func spelledCount(s string) string {
	return spelledCountRE.ReplaceAllStringFunc(s, func(w string) string {
		if d, ok := spelledNumbers[w]; ok {
			return d
		}
		return "10"
	})
}

// explicitLength reads a length the question states.
func explicitLength(q string) (int, bool) {
	q = spelledCount(strings.ToLower(q))
	if m := explicitWordsRE.FindStringSubmatch(q); m != nil {
		return clampWords(upper(m[1], m[2])), true
	}
	if m := explicitSentencesRE.FindStringSubmatch(q); m != nil {
		return clampWords(upper(m[1], m[2]) * 18), true
	}
	if m := explicitParagraphsRE.FindStringSubmatch(q); m != nil {
		return clampWords(upper(m[1], m[2]) * 80), true
	}
	l := strings.ToLower(q)
	switch {
	case strings.Contains(l, "cover letter"):
		return 300, true
	case strings.Contains(l, "additional comments"):
		return 200, true
	}
	return 0, false
}

// lengthPlan is the word budget and the character ceiling of one answer.
type lengthPlan struct {
	Words, MaxChars int
}

func planLength(question string, companyMotivation, narrative bool, maxLength int) lengthPlan {
	words, ok := explicitLength(question)
	if !ok {
		switch {
		case companyMotivation:
			words = companyWords
		case narrative:
			words = narrativeWords
		default:
			words = defaultWords
		}
	}
	chars := int(math.Ceil(float64(words) * charsPerWord * 2))
	if maxLength > 0 {
		if int(math.Ceil(float64(words)*charsPerWord)) > maxLength {
			words = max(minWords, int(math.Floor(float64(maxLength)/charsPerWord*maxLengthFraction)))
		}
		chars = min(chars, maxLength)
	}
	return lengthPlan{Words: words, MaxChars: chars}
}

// cutAtSentence shortens text to at most n characters at a real sentence
// boundary (abbreviations do not end one), else at the last space, else hard.
func cutAtSentence(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	slice := string([]rune(text)[:n])
	ss := sentences(slice)
	// Every sentence but the last ends inside the slice; the last ends there only
	// when it carries a terminator the original text also had.
	if len(ss) > 0 {
		last := ss[len(ss)-1]
		complete := strings.HasSuffix(last, ".") || strings.HasSuffix(last, "!") || strings.HasSuffix(last, "?")
		if complete && !continuesAfterAbbreviation(last, 0) {
			return strings.TrimSpace(strings.Join(ss, " "))
		}
		if len(ss) > 1 {
			return strings.TrimSpace(strings.Join(ss[:len(ss)-1], " "))
		}
	}
	if i := strings.LastIndexAny(slice, ".!?"); i > 0 {
		return strings.TrimSpace(slice[:i+1])
	}
	if i := strings.LastIndex(slice, " "); i > 0 {
		return strings.TrimSpace(slice[:i])
	}
	return strings.TrimSpace(slice)
}

var (
	dashRE       = lazyregexp.New("[\u2014\u2013]")
	boldRE       = lazyregexp.New(`\*\*|__`)
	headingRE    = lazyregexp.New(`(?m)^\s{0,3}#{1,6}\s+`)
	backtickRE   = lazyregexp.New("`+")
	commaRunRE   = lazyregexp.New(`\s+,|,\s*,+`)
	curlyQuoteRE = strings.NewReplacer("\u201c", `"`, "\u201d", `"`, "\u2018", "'", "\u2019", "'")
	answerLabel  = lazyregexp.New(`(?im)^\s*>?\s*\*{0,2}answer\*{0,2}\s*:\s*\*{0,2}\s*`)
	nounPhrasesH = lazyregexp.New(`(?im)^\s*noun phrases[^\n]*:\s*\n(?:\s*[-*\x{2022}][^\n]*\n?)*`)
)

func hasDash(s string) bool { return dashRE.MatchString(s) }

// mechanicalClean applies the substitutions that never need a model: an em dash
// becomes a comma, an en dash a hyphen, curly quotes and markdown go. Paragraph
// breaks stay, because a counted answer is judged by its paragraphs.
func mechanicalClean(s string) string {
	s = strings.ReplaceAll(s, "\u2014", ", ")
	s = strings.ReplaceAll(s, "\u2013", "-")
	s = curlyQuoteRE.Replace(s)
	s = boldRE.ReplaceAllString(s, "")
	s = headingRE.ReplaceAllString(s, "")
	s = backtickRE.ReplaceAllString(s, "")
	s = commaRunRE.ReplaceAllString(s, ",")
	s = strings.ReplaceAll(s, " ,", ",")
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = collapse(l)
	}
	return strings.TrimSpace(blankRunRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var blankRunRE = lazyregexp.New(`\n{3,}`)

// stripScaffolding keeps only the prose after the last line-leading "Answer:"
// label; with no label it drops a leading noun-phrase list.
func stripScaffolding(s string) string {
	if locs := answerLabel.FindAllStringIndex(s, -1); len(locs) > 0 {
		return strings.TrimSpace(s[locs[len(locs)-1][1]:])
	}
	return strings.TrimSpace(nounPhrasesH.ReplaceAllString(s, ""))
}
