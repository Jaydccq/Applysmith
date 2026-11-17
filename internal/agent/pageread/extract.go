// Clean-room: docs/provenance.md#CR-AGENT-TAINT

package pageread

import (
	"html"
	"strings"

	"applysmith/internal/lazyregexp"
)

// skipped elements carry no readable page text.
var skipped = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "head": true, "iframe": true}

// lineTags start a new line where they open; blockTags set a block apart where
// they open and where they close. Other elements add no break, and the ones
// that separate words (table cells, buttons) add a space.
var (
	lineTags  = map[string]bool{"br": true, "li": true, "tr": true, "dt": true, "dd": true, "hr": true}
	blockTags = map[string]bool{
		"p": true, "div": true, "ul": true, "ol": true, "table": true, "section": true, "article": true, "header": true,
		"footer": true, "main": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"blockquote": true, "pre": true, "form": true, "nav": true, "aside": true,
	}
	spaceTags = map[string]bool{"td": true, "th": true, "button": true, "option": true, "label": true, "input": true, "select": true}
)

var (
	spaceRun = lazyregexp.New(`[ \t\f\r\x{a0}]+`)
	lineRun  = lazyregexp.New(`\n(?: *\n)+`)
	titleRE  = lazyregexp.New(`(?is)<title[^>]*>(.*?)</title>`)
)

// extractHTML returns the page title and its visible text. It is a scanner,
// not a parser: it drops comments and the elements that hold no text, turns
// block elements into line breaks and decodes entities.
func extractHTML(src string) (title, text string) {
	if m := titleRE.FindStringSubmatch(src); m != nil {
		title = strings.TrimSpace(spaceRun.ReplaceAllString(html.UnescapeString(m[1]), " "))
	}
	lower := asciiLower(src)
	var out strings.Builder
	for i := 0; i < len(src); {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			out.WriteString(html.UnescapeString(src[i:]))
			break
		}
		out.WriteString(html.UnescapeString(src[i : i+lt]))
		i += lt
		switch {
		case strings.HasPrefix(src[i:], "<!--"):
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				i = len(src)
			} else {
				i += 4 + end + 3
			}
			continue
		case strings.HasPrefix(src[i:], "<!") || strings.HasPrefix(src[i:], "<?"):
			i = skipTag(src, i)
			continue
		}
		end := skipTag(src, i)
		name, closing := tagName(src[i:end])
		i = end
		if name == "" {
			out.WriteByte('<')
			continue
		}
		if !closing && skipped[name] {
			i = skipElement(src, lower, i, name)
			continue
		}
		switch {
		case blockTags[name] || (lineTags[name] && !closing):
			out.WriteByte('\n')
		case spaceTags[name]:
			out.WriteByte(' ')
		}
	}
	text = spaceRun.ReplaceAllString(out.String(), " ")
	text = strings.Join(trimLines(strings.Split(text, "\n")), "\n")
	return title, lineRun.ReplaceAllString(text, "\n\n")
}

func trimLines(lines []string) []string {
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

// skipTag returns the index after the tag that starts at i, honouring quotes
// so a ">" inside an attribute value does not end it.
func skipTag(s string, i int) int {
	var quote byte
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return j + 1
		}
	}
	return len(s)
}

// tagName reads the lower-case element name of a tag, and whether it closes.
func tagName(tag string) (string, bool) {
	tag = strings.TrimPrefix(tag, "<")
	closing := strings.HasPrefix(tag, "/")
	tag = strings.TrimPrefix(tag, "/")
	n := 0
	for n < len(tag) && (tag[n] >= 'a' && tag[n] <= 'z' || tag[n] >= 'A' && tag[n] <= 'Z' || tag[n] >= '0' && tag[n] <= '9') {
		n++
	}
	return strings.ToLower(tag[:n]), closing
}

// skipElement returns the index after the closing tag of name, or the end of
// the input when it never closes.
func skipElement(s, lower string, i int, name string) int {
	if end := strings.Index(lower[i:], "</"+name); end >= 0 {
		return skipTag(s, i+end)
	}
	return len(s)
}

// asciiLower lowers ASCII letters only, so byte offsets stay valid for any
// UTF-8 text.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
