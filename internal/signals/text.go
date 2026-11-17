package signals

import (
	"html"
	"net/url"
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

var (
	tagRE       = lazyregexp.New(`(?s)<[^>]*>`)
	spaceRE     = lazyregexp.New(`[ \t\r\f\v]+`)
	blankRunRE  = lazyregexp.New(`\n{3,}`)
	breakTagRE  = lazyregexp.New(`(?i)<\s*br\s*/?\s*>`)
	paraTagRE   = lazyregexp.New(`(?i)<\s*/?\s*p\s*[^>]*>`)
	liTagRE     = lazyregexp.New(`(?i)<\s*li\b[^>]*>`)
	blockEndRE  = lazyregexp.New(`(?i)<\s*/\s*(?:li|ul|ol|div|h[1-6])\s*>`)
	scriptRE    = lazyregexp.New(`(?is)<(script|style)\b.*?</\s*(?:script|style)\s*>`)
	collapseWSP = lazyregexp.New(`\s+`)
)

// stripTags removes markup and decodes entities twice, so a feed that escaped
// its HTML once still ends as plain text: unescape, strip tags, unescape.
func stripTags(s string) string {
	s = html.UnescapeString(s)
	s = scriptRE.ReplaceAllString(s, " ")
	s = tagRE.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

// cleanSpace collapses every run of whitespace to one space.
func cleanSpace(s string) string {
	return strings.TrimSpace(collapseWSP.ReplaceAllString(s, " "))
}

// htmlToText turns an HTML fragment into text and keeps paragraph breaks and
// list bullets, so a post still reads as lines.
func htmlToText(s string) string {
	s = html.UnescapeString(s)
	s = scriptRE.ReplaceAllString(s, " ")
	s = breakTagRE.ReplaceAllString(s, "\n")
	s = paraTagRE.ReplaceAllString(s, "\n\n")
	s = liTagRE.ReplaceAllString(s, "\n- ")
	s = blockEndRE.ReplaceAllString(s, "\n")
	s = tagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaceRE.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(blankRunRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// firstLine is the first non-empty line of text, capped at n runes.
func firstLine(s string, n int) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return capRunes(l, n)
		}
	}
	return ""
}

func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// capBytes cuts s to at most n bytes on a rune boundary.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// CanonicalURL lowercases the scheme and host, removes a leading www., the
// trailing slash, the default port and the fragment, so one article reached
// through two adapters is one document.
func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errNotWebURL
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	u.Host = host
	u.Fragment = ""
	u.RawFragment = ""
	if p := strings.TrimRight(u.EscapedPath(), "/"); p != u.EscapedPath() {
		u.RawPath = ""
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String(), nil
}

type simpleError string

func (e simpleError) Error() string { return string(e) }

const errNotWebURL = simpleError("not an http(s) URL")

// verbatimForm is the text a quote is compared in: entities decoded, curly
// quotes and dashes turned to ASCII, whitespace collapsed. Nothing looser.
func verbatimForm(s string) string {
	s = html.UnescapeString(s)
	s = verbatimReplacer.Replace(s)
	return strings.TrimSpace(collapseWSP.ReplaceAllString(s, " "))
}

var verbatimReplacer = strings.NewReplacer(
	"‘", "'", "’", "'", "‚", "'", "‛", "'",
	"“", `"`, "”", `"`, "„", `"`,
	"–", "-", "—", "-", "‒", "-", "−", "-",
	" ", " ",
)
