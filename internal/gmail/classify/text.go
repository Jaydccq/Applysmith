package classify

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Matching text is bounded so a hostile message cannot make a rule slow.
const (
	maxMatchChars = 12000
	maxRawChars   = 24000
	maxSnippet    = 220
)

var invisibleRE = lazyregexp.New(`[\x{034f}\x{200b}\x{200c}\x{200d}\x{2060}\x{feff}]`)

var quoteReplacer = strings.NewReplacer(
	string(rune(0x2018)), "'", string(rune(0x2019)), "'", string(rune(0x201c)), `"`, string(rune(0x201d)), `"`,
)

// sanitize removes zero-width and invisible characters and straightens curly
// quotes, so phrase rules match what a person reads.
func sanitize(s string) string {
	return quoteReplacer.Replace(invisibleRE.ReplaceAllString(s, ""))
}

// compact folds whitespace to single spaces and truncates to max runes.
func compact(s string, max int) string {
	clean := strings.Join(strings.Fields(s), " ")
	r := []rune(clean)
	if len(r) > max {
		return strings.TrimSpace(string(r[:max-3])) + "..."
	}
	return clean
}

var addrRE = lazyregexp.New(`^\s*"?([^"<]*)"?\s*<([^>]+)>`)

var angleAddrRE = lazyregexp.New(`<[^>]+>`)

func parseAddress(raw string) Sender {
	if m := addrRE.FindStringSubmatch(raw); m != nil {
		return Sender{Name: strings.TrimSpace(m[1]), Email: strings.TrimSpace(m[2])}
	}
	name := strings.TrimSpace(angleAddrRE.ReplaceAllString(raw, ""))
	email := ""
	if strings.Contains(raw, "@") {
		email = strings.TrimSpace(raw)
	}
	return Sender{Name: name, Email: email}
}

func emailDomain(email string) string {
	_, d, _ := strings.Cut(email, "@")
	return strings.ToLower(strings.TrimSpace(d))
}

func emailLocal(email string) string {
	l, _, _ := strings.Cut(email, "@")
	return strings.ToLower(strings.TrimSpace(l))
}

// splitAddresses splits a To or Cc header on commas outside quotes and angle
// brackets.
func splitAddresses(h string) []string {
	var out []string
	depth, quoted, start := 0, false, 0
	for i, r := range h {
		switch {
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '<':
			depth++
		case r == '>':
			depth--
		case r == ',' && depth <= 0:
			out = append(out, h[start:i])
			start = i + 1
		}
	}
	if start < len(h) {
		out = append(out, h[start:])
	}
	return out
}

// snippetOf cuts a span snippet to the evidence limit.
func snippetOf(s string) string {
	r := []rune(s)
	if len(r) <= 120 {
		return s
	}
	return string(r[:117]) + "..."
}

func mkSpan(source, text, full string) *Span {
	return &Span{Source: source, Snippet: snippetOf(text), Start: strings.Index(full, text)}
}

func titleCase(v string) string {
	words := strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == '_' || r == '-' })
	for i, w := range words {
		if len(w) <= 3 && w == strings.ToUpper(w) {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}
