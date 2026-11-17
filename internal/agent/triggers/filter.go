// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package triggers

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"applysmith/internal/gmail/extract"
	"applysmith/internal/lazyregexp"
)

// MaxMailChars is how much of a message the model is given.
const MaxMailChars = 4000

// Placeholders that stand where material was removed.
const (
	CodePlaceholder = "[verification code removed]"
	LinkPlaceholder = "[link removed]"
)

var (
	urlRE = lazyregexp.New(`(?i)\bhttps?://[^\s<>"'\])]+`)
	// sensitiveURL words: a link that signs in, activates, resets or verifies.
	sensitiveURLRE = lazyregexp.New(`(?i)activat|password|reset|sign[-_]?in|log[-_]?in|magic|verif|confirm|token|otp|\bauth|sso|invite|passcode`)
	trimURLRE      = lazyregexp.New(`[.,;:!?]+$`)
	// codeNounRE finds the words a one-time code stands beside.
	codeNounRE = lazyregexp.New(`(?i)\b(?:pass\s?codes?|otp|pin|codes?)\b`)
	// codeTokenRE is a digit run of 4 to 10 digits, spaces or hyphens allowed
	// between them, or a 6 to 10 character token mixing letters and digits.
	codeTokenRE = lazyregexp.New(`\b\d(?:[ -]?\d){3,9}\b|\b(?:[A-Za-z]+\d|\d+[A-Za-z])[A-Za-z0-9]{4,9}\b`)
)

const codeWindow = 40

// FilterMail returns the text of a message as the model may see it: the
// sender, the subject and the body with verification codes and sign-in,
// activation and reset links replaced by placeholders, cut at MaxMailChars.
// It is conservative on purpose: a link that carries an opaque credential and
// a number beside a code word go too, because a firing's input is nothing
// the model needs a secret from.
func FilterMail(from, subject, body string) string {
	code := extract.Code(body, subject)
	text := "From: " + oneLine(from) + "\nSubject: " + oneLine(subject) + "\n\n" + body
	signin := ""
	if m := extract.SigninLink(extract.SigninInput{Text: body, Subject: subject}); m != nil {
		signin = m.URL
	}
	text = stripLinks(text, signin)
	text = stripCode(text, code)
	return cutPlain(text, MaxMailChars)
}

func stripLinks(text, signin string) string {
	return urlRE.ReplaceAllStringFunc(text, func(raw string) string {
		u := trimURLRE.ReplaceAllString(raw, "")
		tail := raw[len(u):]
		if (signin != "" && strings.HasPrefix(u, signin)) || sensitiveLink(u) {
			return LinkPlaceholder + tail
		}
		return raw
	})
}

func sensitiveLink(raw string) bool {
	if sensitiveURLRE.MatchString(raw) {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	if len(u.Fragment) >= 11 {
		return true
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			if len(v) >= 10 && opaque(v) {
				return true
			}
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if len(seg) >= 8 && opaque(seg) {
			return true
		}
	}
	return false
}

// opaque is a value that cannot be a word: letters and digits mixed, or very
// long.
func opaque(v string) bool {
	var letter, digit bool
	n := 0
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letter = true
		default:
			continue
		}
		n++
	}
	return n >= 8 && ((letter && digit) || n >= 24)
}

func stripCode(text, code string) string {
	if code != "" {
		parts := make([]string, 0, len(code))
		for _, r := range code {
			parts = append(parts, regexp.QuoteMeta(string(r)))
		}
		text = regexp.MustCompile(`(?i)`+strings.Join(parts, `[ -]?`)).ReplaceAllString(text, CodePlaceholder)
	}
	nouns := codeNounRE.FindAllStringIndex(text, -1)
	if len(nouns) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range codeTokenRE.FindAllStringIndex(text, -1) {
		if m[0] < last || !near(nouns, m[0], m[1]) {
			continue
		}
		b.WriteString(text[last:m[0]])
		b.WriteString(CodePlaceholder)
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// near reports whether a token lies within codeWindow bytes of a code word.
func near(nouns [][]int, start, end int) bool {
	for _, n := range nouns {
		if start < n[1]+codeWindow && end > n[0]-codeWindow {
			return true
		}
	}
	return false
}

func cutPlain(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
