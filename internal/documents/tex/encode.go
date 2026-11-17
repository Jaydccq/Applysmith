// Package tex encodes plain text and URLs for the reviewed document
// templates. It is the only way content reaches a TeX engine: every character
// is either written as itself or as an escape that typesets that character,
// so no input can start a command, a comment or a file inclusion.
package tex

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// Text returns s as TeX source that typesets exactly s. Line breaks and tabs
// become spaces, control characters are dropped, and runs of hyphens, quotes
// and brackets are written so that no ligature, comment or optional argument
// can form.
func Text(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	prev := rune(0)
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			r = ' '
			b.WriteRune(r)
		case r == '\u00a0':
			b.WriteString("~")
		case r == '-':
			// A box between hyphens stops the font's dash ligature; an empty
			// group does not, because LuaTeX applies ligatures to the node list.
			if prev == '-' {
				b.WriteString(`\mbox{-}`)
			} else {
				b.WriteRune(r)
			}
		case unicode.IsControl(r) || r == '\ufeff' || r == '\u200b':
			continue
		default:
			if esc, ok := escapes[r]; ok {
				b.WriteString(esc)
			} else {
				b.WriteRune(r)
			}
		}
		prev = r
	}
	return b.String()
}

// escapes holds every character that TeX treats specially or that a font
// would typeset as something else under the default ligature rules.
var escapes = map[rune]string{
	'\\': `\textbackslash{}`,
	'&':  `\&`,
	'%':  `\%`,
	'$':  `\$`,
	'#':  `\#`,
	'_':  `\_`,
	'{':  `\{`,
	'}':  `\}`,
	'~':  `\textasciitilde{}`,
	'^':  `\textasciicircum{}`,
	'<':  `\textless{}`,
	'>':  `\textgreater{}`,
	'|':  `\textbar{}`,
	'[':  `{[}`,
	']':  `{]}`,
	'"':  `\textquotedbl{}`,
	'\'': `\textquotesingle{}`,
	'`':  `\textasciigrave{}`,
}

// ErrURL is returned for a link that is not an absolute http, https or mailto
// address.
var ErrURL = errors.New("tex: not an http, https or mailto link")

// URL returns raw as the first argument of \href. The address is parsed and
// re-serialised first, so what the PDF link annotation carries is the
// canonical form of raw, and every character TeX would read as markup is
// percent-encoded or backslash-escaped.
func URL(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || len(raw) > 2048 {
		return "", fmt.Errorf("%w: empty, padded or too long", ErrURL)
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f || r > unicode.MaxASCII {
			return "", fmt.Errorf("%w: control, space or non-ASCII character", ErrURL)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrURL, err)
	}
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return "", fmt.Errorf("%w: no host", ErrURL)
		}
	case "mailto":
		if u.Opaque == "" {
			return "", fmt.Errorf("%w: no address", ErrURL)
		}
	default:
		return "", fmt.Errorf("%w: scheme %q", ErrURL, u.Scheme)
	}
	var b strings.Builder
	for _, r := range u.String() {
		switch r {
		case '%', '#', '&', '_':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '~', '$', '^', '{', '}', '\\', '|', '"', '<', '>', '`':
			fmt.Fprintf(&b, `\%%%02X`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

// Rich is Text for a line that may bold spans with paired double asterisks.
// Every span is encoded as plain text; the only markup Rich writes is the
// bold command around a span, so the markers carry no other power. An
// unpaired marker is dropped.
func Rich(s string) string {
	parts := strings.Split(s, "**")
	if len(parts)%2 == 0 {
		return Text(strings.Join(parts, ""))
	}
	var b strings.Builder
	for i, p := range parts {
		switch {
		case p == "":
		case i%2 == 1:
			b.WriteString(`\textbf{` + Text(p) + `}`)
		default:
			b.WriteString(Text(p))
		}
	}
	return b.String()
}
