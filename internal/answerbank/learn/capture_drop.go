package learn

import (
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// CaptureCandidate is what the capture rules look at: the engine runs the same
// rules before it keeps a capture, and ingest runs them again, over the shared
// vectors in engine/test/capture-vectors.json. An upload carries neither the
// control's own label nor its value when the watch attached, so the two rules
// that read them ("unchanged", "equals-label") run in the engine only.
type CaptureCandidate struct {
	Question    string
	Value       string
	ControlKind string
}

const (
	maxCaptureQuestion = 2000
	duplicateShare     = 0.35
)

var (
	opaqueID  = lazyregexp.New(`\b(?:question|field|input|q|id)[_-]?\d{5,}\b|\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b[a-z]+_[0-9a-f]{10,}\b|\b\d{9,}\b`)
	seamRE    = lazyregexp.New(`[a-z][A-Z]`)
	fragmentL = lazyregexp.New(`^[a-z]{1,3}$`)
)

func foldCapture(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// segmentsOf cuts text into the sentences, lines and bars a page glues together.
func segmentsOf(text string) []string {
	var out []string
	var cur []rune
	rs := []rune(text)
	for i, c := range rs {
		switch {
		case c == '\n' || c == '|':
			out = append(out, string(cur))
			cur = nil
		default:
			cur = append(cur, c)
			if (c == '.' || c == '?' || c == '!') && (i+1 >= len(rs) || unicode.IsSpace(rs[i+1])) {
				out = append(out, string(cur))
				cur = nil
			}
		}
	}
	out = append(out, string(cur))
	segs := out[:0]
	for _, s := range out {
		if f := foldCapture(s); f != "" {
			segs = append(segs, f)
		}
	}
	return segs
}

// repeatsPrefix reports a text that opens with a string and, after a few
// separator characters, the same string again ("Male--MaleFemale").
func repeatsPrefix(t string) bool {
	rs := []rune(t)
	for k := 3; k*2 <= len(rs); k++ {
		head := string(rs[:k])
		rest := rs[k:]
		sep := 0
		for len(rest) > 0 && !unicode.IsLetter(rest[0]) && !unicode.IsNumber(rest[0]) && sep < 4 {
			rest = rest[1:]
			sep++
		}
		if strings.HasPrefix(string(rest), head) {
			return true
		}
	}
	return false
}

func doubled(text string, prefix bool) bool {
	t := foldCapture(text)
	if prefix && repeatsPrefix(t) {
		return true
	}
	rs := []rune(t)
	if len(rs) >= 2 && len(rs)%2 == 0 && string(rs[:len(rs)/2]) == string(rs[len(rs)/2:]) {
		return true
	}
	w := strings.Fields(t)
	if len(w) >= 2 && len(w)%2 == 0 {
		h := len(w) / 2
		return strings.Join(w[:h], " ") == strings.Join(w[h:], " ")
	}
	return false
}

var scripts = []struct {
	name string
	has  func(rune) bool
}{
	{"latin", func(r rune) bool { return unicode.Is(unicode.Latin, r) }},
	{"cyrillic", func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }},
	{"greek", func(r rune) bool { return unicode.Is(unicode.Greek, r) }},
	{"arabic", func(r rune) bool { return unicode.Is(unicode.Arabic, r) }},
	{"hebrew", func(r rune) bool { return unicode.Is(unicode.Hebrew, r) }},
	{"han", func(r rune) bool { return unicode.Is(unicode.Han, r) }},
	{"kana", func(r rune) bool { return unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) }},
	{"hangul", func(r rune) bool { return unicode.Is(unicode.Hangul, r) }},
	{"devanagari", func(r rune) bool { return unicode.Is(unicode.Devanagari, r) }},
	{"thai", func(r rune) bool { return unicode.Is(unicode.Thai, r) }},
}

// scriptOf is the one script a text's letters are written in, "" when it has
// no letters or mixes scripts.
func scriptOf(text string) string {
	seen := map[string]bool{}
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		name := "other"
		for _, s := range scripts {
			if s.has(r) {
				name = s.name
				break
			}
		}
		seen[name] = true
	}
	if len(seen) != 1 {
		return ""
	}
	for k := range seen {
		return k
	}
	return ""
}

// CaptureDrop says why a capture is dropped instead of stored, or "" when it
// is kept.
func CaptureDrop(c CaptureCandidate) string {
	q := c.Question
	if len([]rune(q)) > maxCaptureQuestion {
		return "question-too-long"
	}
	segs := segmentsOf(q)
	if len(segs) >= 2 {
		distinct := map[string]bool{}
		for _, s := range segs {
			distinct[s] = true
		}
		if float64(len(segs)-len(distinct))/float64(len(segs)) >= duplicateShare {
			return "repeated-segment"
		}
	}
	if doubled(q, false) {
		return "repeated-segment"
	}
	if opaqueID.MatchString(q) {
		return "opaque-id"
	}
	if len(seamRE.FindAllString(q, -1)) >= 2 {
		return "seams"
	}
	if strings.TrimSpace(q) != "" && IsNothingChosen(q) {
		return "placeholder-question"
	}
	v := strings.TrimSpace(c.Value)
	switch {
	case v == "":
		return "empty-value"
	case doubled(v, true):
		return "doubled-value"
	case IsNothingChosen(v):
		return "placeholder"
	}
	if qs, vs := scriptOf(q), scriptOf(v); qs != "" && vs != "" && qs != vs {
		return "script-mismatch"
	}
	switch {
	case foldCapture(q) == foldCapture(v):
		return "equals-question"
	case c.ControlKind == "custom-listbox" && fragmentL.MatchString(v):
		return "listbox-fragment"
	}
	return ""
}
