package notify

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/vault"
)

// Limits of a notification's text.
const (
	MaxNoticeTitle = 80
	MaxNoticeBody  = 200
)

var (
	urlRE      = lazyregexp.New(`https?://[^\s<>"')]+`)
	codeRE     = lazyregexp.New(`(?i)\b(code|otp|passcode|pin)\b(\s+is)?\s*[:=]?\s*[A-Za-z0-9-]*\d[A-Za-z0-9-]*`)
	secretRE   = lazyregexp.New(`(?i)\b(password|passwd|token|secret|api[ _-]?key)\b\s*[:=]\s*\S+`)
	longTokRE  = lazyregexp.New(`[A-Za-z0-9_-]{28,}`)
	prefixedRE = lazyregexp.New(`\b(?:sk|pk|ghp|gho|xox[a-z])[-_][A-Za-z0-9_-]{12,}`)
	botTokenRE = lazyregexp.New(`\b\d{6,}:[A-Za-z0-9_-]{20,}`)
)

// Scrub removes what must never reach a notification: a verification code, a
// password or token, and a link that carries a secret (a query, a fragment or
// an opaque path segment).
func Scrub(s string) string {
	s = vault.Redact(s)
	s = urlRE.ReplaceAllStringFunc(s, func(raw string) string {
		u, err := url.Parse(strings.TrimRight(raw, ".,;"))
		if err != nil || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return "[link removed]"
		}
		for _, seg := range strings.Split(u.Path, "/") {
			if len(seg) >= 24 && longTokRE.MatchString(seg) {
				return "[link removed]"
			}
		}
		return raw
	})
	s = botTokenRE.ReplaceAllString(s, "[removed]")
	s = prefixedRE.ReplaceAllString(s, "[removed]")
	s = secretRE.ReplaceAllString(s, "$1 [removed]")
	s = codeRE.ReplaceAllString(s, "$1 [removed]")
	// Applied last so the placeholders above survive; links were handled first.
	return longTokRE.ReplaceAllStringFunc(s, func(m string) string {
		if strings.IndexFunc(m, unicode.IsDigit) < 0 || strings.IndexFunc(m, unicode.IsLetter) < 0 {
			return m
		}
		return "[removed]"
	})
}

// Compose bounds and scrubs the text of a notification.
func Compose(title, body string) (string, string) {
	return clipRunes(Scrub(title), MaxNoticeTitle), clipRunes(Scrub(body), MaxNoticeBody)
}

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
