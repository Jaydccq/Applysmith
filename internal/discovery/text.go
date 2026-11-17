package discovery

import (
	"html"
	"strings"

	"applysmith/internal/lazyregexp"
)

var (
	blockTagRE = lazyregexp.New(`(?is)</?(p|div|br|ul|ol|h[1-6]|tr|section|table)\b[^>]*>`)
	liTagRE    = lazyregexp.New(`(?is)<li\b[^>]*>`)
	liEndRE    = lazyregexp.New(`(?is)</li\s*>`)
	dropRE     = lazyregexp.New(`(?is)<(script|style)\b.*?</(script|style)>`)
	tagRE      = lazyregexp.New(`(?s)<[^>]*>`)
	spacesRE   = lazyregexp.New(`[ \t\x{00a0}]+`)
	blankRE    = lazyregexp.New(`\n[ \t]*(\n[ \t]*)+`)
)

// plainText turns vendor HTML into plain text with one line per block, so the
// sentence-level signals can read it. Text that arrives entity-escaped
// (Greenhouse) is unescaped first.
func plainText(s string) string {
	if strings.Contains(s, "&lt;") && !strings.Contains(s, "<") {
		s = html.UnescapeString(s)
	}
	s = dropRE.ReplaceAllString(s, " ")
	s = liTagRE.ReplaceAllString(s, "\n- ")
	s = liEndRE.ReplaceAllString(s, "\n")
	s = blockTagRE.ReplaceAllString(s, "\n")
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spacesRE.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = blankRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(s)
}

// cleanLocations is a list's location text as one clean list: the pieces
// trimmed, repeats dropped, and a piece that only restates a place another
// piece spells out ("San Francisco" beside "San Francisco, California,
// United States", "Menlo Park, CA" beside "Menlo Park, California") replaced
// by the fuller one. The first place stays first, so it reads as the primary
// location. Pieces join with "; ", the separator every source uses.
func cleanLocations(parts []string) string {
	var out []string
	for _, raw := range parts {
		p := strings.Join(strings.Fields(raw), " ")
		if p == "" {
			continue
		}
		merged := false
		for i, q := range out {
			switch {
			case strings.EqualFold(p, q) || restates(p, q):
				merged = true
			case restates(q, p):
				out[i], merged = p, true
			}
			if merged {
				break
			}
		}
		if !merged {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

// restates reports whether short names the same place as long, said with
// fewer words: the same city, and either no further parts or a region that is
// the long form's region abbreviated.
func restates(short, long string) bool {
	s, l := strings.Split(short, ","), strings.Split(long, ",")
	if len(s) > len(l) || cityKey(s[0]) != cityKey(l[0]) {
		return false
	}
	if len(s) == 1 {
		return true
	}
	return abbreviates(strings.TrimSpace(s[1]), strings.TrimSpace(l[1]))
}

// abbreviates reports whether code is a short form of name: it starts with
// the same letter and its letters appear in order ("NY" in "New York", "DC"
// in "District of Columbia", "CA" in "California").
func abbreviates(code, name string) bool {
	code, name = strings.ToLower(strings.ReplaceAll(code, ".", "")), strings.ToLower(name)
	if len(code) < 2 || len(code) > 3 || !strings.HasPrefix(name, code[:1]) {
		return false
	}
	rest := name
	for _, c := range code {
		i := strings.IndexRune(rest, c)
		if i < 0 {
			return false
		}
		rest = rest[i+len(string(c)):]
	}
	return true
}

// cityKey is a city name as places compare it: case-folded, with the common
// name of New York City folded onto the city's.
func cityKey(c string) string {
	switch c = strings.ToLower(strings.TrimSpace(c)); c {
	case "new york city", "nyc":
		return "new york"
	}
	return c
}
