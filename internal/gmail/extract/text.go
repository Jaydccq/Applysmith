package extract

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

var (
	styleRE  = lazyregexp.New(`(?is)<style\b.*?</style>`)
	scriptRE = lazyregexp.New(`(?is)<script\b.*?</script>`)
	tagRE    = lazyregexp.New(`<[^>]*>`)
)

var entityReplacer = strings.NewReplacer(
	"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'", "&quot;", `"`,
)

// StripHTML removes style and script blocks and tags and decodes the handful
// of entities mail bodies use. Unknown entities stay as they are; the code
// extractor blanks them so they cannot supply a candidate.
func StripHTML(html string) string {
	s := styleRE.ReplaceAllString(html, " ")
	s = scriptRE.ReplaceAllString(s, " ")
	s = tagRE.ReplaceAllString(s, " ")
	return entityReplacer.Replace(s)
}

var hrefReplacer = strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">")

// DecodeEntities decodes the entities an href attribute carries.
func DecodeEntities(s string) string { return hrefReplacer.Replace(s) }

// compact folds every run of whitespace to one space.
func compact(s string) string { return strings.Join(strings.Fields(s), " ") }

var commentRE = lazyregexp.New(`(?s)<!--.*?-->`)

// AnchorHrefs returns the href attribute values of the anchors of an HTML
// part, in document order and with entities decoded. It reads attribute values
// only: anchor text, comments, style and script blocks, and every other
// attribute are never looked at, and a quote that closes an attribute cannot be
// talked into containing another one.
func AnchorHrefs(html string) []string {
	html = commentRE.ReplaceAllString(styleRE.ReplaceAllString(scriptRE.ReplaceAllString(html, " "), " "), " ")
	var out []string
	for i := 0; i < len(html); {
		j := strings.IndexByte(html[i:], '<')
		if j < 0 {
			break
		}
		i += j + 1
		if i+1 >= len(html) || html[i]|0x20 != 'a' || !isTagSpace(html[i+1]) {
			continue
		}
		var href string
		var found bool
		i, href, found = scanAnchorTag(html, i+1)
		if found {
			out = append(out, DecodeEntities(href))
		}
	}
	return out
}

func isTagSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }

// scanAnchorTag reads the attributes of a tag from i to its closing '>' and
// returns the position after it and the first href value.
func scanAnchorTag(s string, i int) (next int, href string, found bool) {
	for i < len(s) {
		for i < len(s) && (isTagSpace(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '>' {
			return i + 1, href, found
		}
		start := i
		for i < len(s) && !isTagSpace(s[i]) && s[i] != '=' && s[i] != '>' && s[i] != '/' {
			i++
		}
		name := strings.ToLower(s[start:i])
		for i < len(s) && isTagSpace(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			continue
		}
		i++
		for i < len(s) && isTagSpace(s[i]) {
			i++
		}
		var val string
		switch {
		case i < len(s) && (s[i] == '"' || s[i] == '\''):
			end := strings.IndexByte(s[i+1:], s[i])
			if end < 0 {
				return len(s), href, found
			}
			val = s[i+1 : i+1+end]
			i += end + 2
		default:
			start := i
			for i < len(s) && !isTagSpace(s[i]) && s[i] != '>' {
				i++
			}
			val = s[start:i]
		}
		if name == "href" && !found {
			href, found = strings.TrimSpace(val), true
		}
	}
	return i, href, found
}

// printedURLRE is an https address as a mail prints it, up to whitespace, a
// quote, an angle bracket or a bracket.
var printedURLRE = lazyregexp.New(`https://[^\s"'<>()\[\]]+`)

// PrintedURLs returns the https addresses a mail prints as visible text: the
// text part and the HTML part's text. Comments, script and style blocks and
// every attribute are never read.
func PrintedURLs(html, text string) []string {
	out := printedURLRE.FindAllString(StripHTML(commentRE.ReplaceAllString(html, " "))+"\n"+text, -1)
	for i := range out {
		out[i] = strings.TrimRight(out[i], ".,;:!?")
	}
	return out
}
