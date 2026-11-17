package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// urlStop is what ends a URL in a message: whitespace, and Chinese, Japanese
// or Korean text or full-width punctuation glued to it, as the submit gate's
// URL tokens are split (gates.isTokenBreak).
const urlStop = `\s\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}\x{3000}-\x{303F}\x{FF00}-\x{FFEF}`

var urlText = lazyregexp.New(`(?i)\b[a-z][a-z0-9+.-]*://[^` + urlStop + `]+`)

// Verbs that express an intent to change local data (tier 2) and to start an
// action (tier 3). The user's own words are the only input. Chinese verbs are
// written as escapes (tracked files carry English text only); each line's
// comment glosses them.
var (
	writeVerbs = verbs("remember", "save", "store", "note", "change", "update", "add", "forget", "delete", "remove", "record", "edit", "amend", "set", "tailor", "rewrite", "skip", "block", "unblock", "create", "manage", "pause", "resume", "mark", "complete", "discard",
		"\u8bb0\u4f4f", "\u8bb0\u4e0b", "\u4fdd\u5b58", "\u6dfb\u52a0", "\u52a0\u5230", "\u4fee\u6539", "\u66f4\u65b0", // remember, note down, save, add, add to, change, update
		"\u5220\u9664", "\u5220\u6389", "\u5fd8\u8bb0", "\u5fd8\u6389", "\u8df3\u8fc7", "\u5c4f\u853d", "\u521b\u5efa", // delete, delete, forget, forget, skip, block, create
		"\u5b89\u6392", "\u6682\u505c", "\u6062\u590d", "\u6807\u8bb0", "\u5b9a\u5236", "\u6539\u5199", "\u653e\u5f03", // schedule, pause, resume, mark, tailor, rewrite, discard
		"dismiss", "snooze", "undo", "reopen", "restore", "remind", "postpone", // a job mail thread's actions
		"\u5b8c\u6210", "\u5ffd\u7565", "\u64a4\u9500", "\u63d0\u9192", "\u63a8\u8fdf") // complete, ignore, undo, remind, postpone
	actionVerbs = verbs("scan", "evaluate", "score", "assess", "screen", "fill", "autofill", "apply", "register", "prepare", "research",
		"\u8bc4\u4f30", "\u8bc4\u4ef7", "\u5206\u6790", "\u6253\u5206", "\u626b\u63cf", "\u641c\u7d22", // evaluate, assess, analyse, score, scan, search
		"\u7533\u8bf7", "\u6295\u9012", "\u586b", "\u6ce8\u518c") // apply, apply (send an application), fill, register
	// applyVerbs are the explicit request a submit needs; fill and autofill
	// are action verbs only, so they start a fill that never submits.
	applyVerbs = verbs("apply", "submit",
		"\u7533\u8bf7", "\u6295\u9012", "\u63d0\u4ea4") // apply, apply (send an application), submit
	// operateVerbs ask the agent to work a window or a form by hand: the
	// desktop actions and the answers to a stopped run need one of them.
	operateVerbs = verbs("click", "press", "type", "fill", "select", "choose", "scroll", "open", "check",
		"\u70b9\u51fb", "\u5355\u51fb", "\u6309\u4e0b", "\u8f93\u5165", "\u586b", // click, click, press, type, fill
		"\u9009\u62e9", "\u6eda\u52a8", "\u6253\u5f00", "\u52fe\u9009") // select or choose, scroll, open, check
	// finishVerbs ask the agent to carry a stopped application run on; with an
	// operate verb they let the apply tools work its kept tab (the desktop
	// tools still need an operate verb).
	finishVerbs = verbs("finish", "complete", "continue", "resume",
		"\u5b8c\u6210", "\u7ee7\u7eed", "\u586b\u5199") // finish or complete, continue, fill in
	// delegateVerbs hand an outcome to the agent to carry on; beside an action,
	// write or finish verb they let delegate_task start a task.
	delegateVerbs = verbs("handle", "delegate", "until", "keep", "follow",
		"\u5904\u7406", "\u641e\u5b9a", "\u76f4\u5230", "\u4e00\u76f4", // handle, get it done, until, keep on
		"\u8d1f\u8d23", "\u4ea4\u7ed9", "\u8ddf\u8fdb") // take charge of, hand to, follow up
	// searchVerbs ask for a search of the job sources. They name scan_portal and
	// delegate_task only, being too wide to start a fill or an evaluation: live,
	// "Find me new senior platform engineer jobs posted this week" held both.
	searchVerbs = verbs("search", "find", "discover",
		"\u627e") // find
)

// verbSet holds English verbs, matched as whole words, and Chinese verbs,
// matched inside a run of Han characters because Chinese puts no space
// between words.
type verbSet struct {
	words map[string]bool
	han   []string
}

func verbs(list ...string) verbSet {
	s := verbSet{words: map[string]bool{}}
	for _, w := range list {
		if hasHan(w) {
			s.han = append(s.han, w)
		} else {
			s.words[w] = true
		}
	}
	return s
}

func wordSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

func hasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

// pastAux before a verb makes it a question about what already happened
// ("what did I submit"), and a negation before it cancels the request.
var (
	pastAux  = wordSet("did", "have", "has", "had", "was", "were")
	negators = wordSet("dont", "not", "never", "no", "without", "cant", "wont")
)

// The Chinese forms of the same: a negation (not, do not, have not, not yet,
// must not) or a past marker (already, once) among the four characters before
// a verb in its clause, or a completion marker right after it (the aspect
// particles of "applied", "have applied").
var (
	hanNegators = []rune{'\u4e0d', '\u522b', '\u6ca1', '\u672a', '\u52ff'}
	hanPastAux  = []rune{'\u5df2', '\u66fe'}
	hanPastTail = []string{"\u4e86", "\u8fc7"}
)

// namesVerb reports whether the user's own words ask for one of the verbs: the
// words are not a question (the router's predicate, message.asks), and the verb
// appears outside every URL, is not part of a question about the past, and is
// not negated within the three words before it. A request put as a question
// ("can you scan linkedin?") names only the verb it starts with.
func namesVerb(message string, vs verbSet) bool {
	for _, own := range ownWords(message) {
		if namesIn(own, vs) {
			return true
		}
	}
	return false
}

func namesIn(own string, vs verbSet) bool {
	if m := prepare(own); m.asks() {
		return false
	} else if m.question {
		return vs.words[m.words[0]]
	}
	text := urlText.ReplaceAllString(own, " ")
	words := wordsOf(text)
	for i, w := range words {
		if !vs.words[w] {
			continue
		}
		blocked := false
		for j := max(0, i-4); j < i; j++ {
			if pastAux[words[j]] {
				blocked = true
			}
		}
		for j := max(0, i-3); j < i; j++ {
			if negators[words[j]] {
				blocked = true
			}
		}
		if !blocked {
			return true
		}
	}
	return hanUse(text, vs.han, func(before []rune, after string) bool {
		if containsAny(before, hanNegators) || containsAny(before, hanPastAux) {
			return false
		}
		for _, p := range hanPastTail {
			if strings.HasPrefix(after, p) {
				return false
			}
		}
		return true
	})
}

// hanUse reports whether f accepts one use of a Chinese verb in text. f gets
// the at most four letters before the use, back to the start of its clause,
// and the text after it.
func hanUse(text string, list []string, f func(before []rune, after string) bool) bool {
	for _, v := range list {
		for from := 0; ; {
			i := strings.Index(text[from:], v)
			if i < 0 {
				break
			}
			at := from + i
			var before []rune
			for j := at; j > 0 && len(before) < 4; {
				r, n := utf8.DecodeLastRuneInString(text[:j])
				if !unicode.IsLetter(r) {
					break
				}
				before = append(before, r)
				j -= n
			}
			if f(before, text[at+len(v):]) {
				return true
			}
			from = at + len(v)
		}
	}
	return false
}

func containsAny(rs, set []rune) bool {
	for _, r := range rs {
		for _, s := range set {
			if r == s {
				return true
			}
		}
	}
	return false
}

// Bounds of the user's own words in a long message.
const (
	pastedLines = 3
	pastedRunes = 600
	ownRunes    = 300
)

var (
	fencedBlock = lazyregexp.New("(?s)```.*?(```|$)")
	quotedLine  = lazyregexp.New(`(?m)^[ \t]*>.*$`)
)

// ownWords are the parts of a message the user wrote, which alone can name an
// action, each read on its own: a fenced block and a quoted line are pasted,
// and a message of more than three lines or 600 characters is read as a
// request around a pasted block (a job description), so only its first
// non-empty line counts, and its last when that line starts with a request
// and is not a posting's own call to apply ("Apply now: <url>"); each is cut
// to 300 characters. A restriction ("do not submit") is read from the whole
// message.
func ownWords(message string) []string {
	s := quotedLine.ReplaceAllString(fencedBlock.ReplaceAllString(message, " "), " ")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) <= pastedLines && utf8.RuneCountInString(s) <= pastedRunes {
		return []string{strings.Join(lines, "\n")}
	}
	out := []string{headRunes(lines[0], ownRunes)}
	if last := headRunes(lines[len(lines)-1], ownRunes); len(lines) > 1 && ownRequest(last) {
		out = append(out, last)
	}
	return out
}

// OwnText is the user's own words of a message on one string, for the URL
// tokens and the correction wording the gates read.
func OwnText(message string) string { return strings.Join(ownWords(message), "\n") }

func headRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// postingCall are the words after the verb of a posting's own call to apply.
var postingCall = wordSet("now", "here", "today", "online", "url")

// ownRequest reports whether a line starts with a verb of the intent tables,
// after its filler ("please", "can you"), in English or Chinese, and is more
// than a verb with postingCall words.
func ownRequest(line string) bool {
	m := prepare(line)
	if len(m.words) == 0 {
		return false
	}
	if !m.request {
		return hanRequest.MatchString(m.words[0])
	}
	for _, w := range m.words[1:] {
		if !postingCall[w] {
			return true
		}
	}
	return false
}

// hanRequest is a Chinese line that starts with a verb of the intent tables,
// after an optional "please" and "help me".
var hanRequest = lazyregexp.New("^(\u8bf7|\u9ebb\u70e6\u4f60?)?(\u5e2e\u6211|\u7ed9\u6211|\u5e2e\u5fd9)?(" +
	strings.Join(append(append(append([]string{}, writeVerbs.han...), actionVerbs.han...), applyVerbs.han...), "|") + ")")

// wordsOf folds a text to lower-case words. A run of Han characters is a word
// of its own, apart from Latin letters glued to it (a source name written
// right after a Chinese verb).
func wordsOf(s string) []string {
	s = strings.ToLower(strings.NewReplacer("’", "'", "n't", "nt").Replace(s))
	var b strings.Builder
	prevHan, prevWord := false, false
	for _, r := range s {
		han := unicode.Is(unicode.Han, r)
		word := unicode.IsLetter(r) || unicode.IsDigit(r)
		if word && prevWord && han != prevHan {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
		prevHan, prevWord = han, word
	}
	return strings.FieldsFunc(b.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// forbidsSubmit reports whether the message tells the agent not to submit
// ("fill it but do not submit"): a submit word with a negation among the three
// words before it. Such a message may start a fill, never a submit. It reads
// the whole message, pasted text included: a restriction only narrows.
func forbidsSubmit(message string) bool {
	text := urlText.ReplaceAllString(message, " ")
	words := wordsOf(text)
	for i, w := range words {
		if !submitWords.words[w] {
			continue
		}
		for j := max(0, i-3); j < i; j++ {
			if negators[words[j]] {
				return true
			}
		}
	}
	return hanUse(text, submitWords.han, func(before []rune, _ string) bool { return containsAny(before, hanNegators) })
}

var submitWords = verbs("submit", "submitting", "send", "sending",
	"\u63d0\u4ea4", "\u6295\u9012", "\u53d1\u9001") // submit, send an application, send
