// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// ref is an http(s) URL reduced to the parts a mention is compared on.
type ref struct {
	host, port, path, query string
}

var bareHostRE = lazyregexp.New(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+(?::[0-9]+)?$`)

// isTokenBreak splits a message into tokens at whitespace, brackets, quotes
// and CJK text, so a URL glued to prose in those scripts is still isolated.
func isTokenBreak(r rune) bool {
	switch r {
	case '(', ')', '[', ']', '{', '}', '<', '>', '"', '\'', '`', '“', '”', '‘', '’':
		return true
	}
	return unicode.IsSpace(r) ||
		unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		r >= 0x3000 && r <= 0x303f || r >= 0xff00 && r <= 0xffef
}

// urlTokens returns the URL-shaped tokens of a user message: http(s) URLs and
// bare host/path tokens, with trailing sentence punctuation trimmed. A bare
// host without a path is prose, not a mention.
func urlTokens(msg string) []ref {
	var out []ref
	for _, tok := range strings.FieldsFunc(msg, isTokenBreak) {
		if r, ok := parseRef(strings.TrimRight(tok, ".,;:!?")); ok {
			out = append(out, r)
		}
	}
	return out
}

// parseRef reads an http(s) URL or a bare host/path. Userinfo names nothing.
func parseRef(tok string) (ref, bool) {
	low := strings.ToLower(tok)
	switch {
	case strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://"):
	case strings.Contains(tok, "/") && bareHostRE.MatchString(strings.ToLower(tok[:strings.Index(tok, "/")])):
		tok = "https://" + tok
	default:
		return ref{}, false
	}
	u, err := url.Parse(tok)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return ref{}, false
	}
	port := u.Port()
	if port == "80" && u.Scheme == "http" || port == "443" && u.Scheme == "https" {
		port = ""
	}
	return ref{
		host:  strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."),
		port:  port,
		path:  strings.TrimRight(u.Path, "/"),
		query: u.RawQuery,
	}, true
}

// sameURL is the exact-URL comparison: host, port, path (case-sensitive,
// trailing slash ignored) and query must be identical, and the path must be
// longer than the root so a site's front page vouches for nothing.
func sameURL(a, b ref) bool { return a == b && a.path != "" }

// URLs in a message are removed before wording and company names are read, so
// a slug or a question mark inside a pasted link counts for neither.
var linkRE = lazyregexp.New(`(?i)https?://[^\s<>"'` + "`" + `)\]]+|\b[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+(?::[0-9]+)?/[^\s<>"'` + "`" + `)\]]*`)

func stripURLs(msg string) string { return linkRE.ReplaceAllString(msg, " ") }

// namesWord reports whether text contains name as a whole word or phrase,
// ignoring case and runs of white space.
func namesWord(text, name string) bool {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return false
	}
	words := strings.Fields(regexp.QuoteMeta(name))
	re := regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])` + strings.Join(words, `\s+`) + `(?:[^\p{L}\p{N}]|$)`)
	return re.MatchString(text)
}
