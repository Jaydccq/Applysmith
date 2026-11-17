package discovery

import (
	"html"
	"net/url"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Markers the lists use, written as escapes so no source file holds emoji.
const (
	lockMarker  = "\U0001F512"
	arrowMarker = "↳"
)

// listRow is one row of a job table before its age is read.
type listRow struct {
	Company, Role, Location, Age string
	// Apply is the application URL, empty when the row has none.
	Apply string
	// Closed marks a row the list itself closed: a lock marker and no link.
	Closed bool
}

var (
	htmlTableRE = lazyregexp.New(`(?is)<table\b.*?</table>`)
	htmlRowRE   = lazyregexp.New(`(?is)<tr\b[^>]*>(.*?)</tr>`)
	htmlCellRE  = lazyregexp.New(`(?is)<t[dh]\b[^>]*>(.*?)</t[dh]>`)
	mdLinkRE    = lazyregexp.New(`\[[^\]]*\]\((https?://[^)\s]+)[^)]*\)`)
	hrefLinkRE  = lazyregexp.New(`(?is)\bhref\s*=\s*(?:"(https?://[^"]+)"|'(https?://[^']+)')`)
	mdSepRE     = lazyregexp.New(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	locCountRE  = lazyregexp.New(`(?is)<summary\b[^>]*>\s*(?:\*\*|<[^>]+>)*\s*\d+\s+locations?\s*(?:\*\*|<[^>]+>)*\s*</summary>`)
	lineBreakRE = lazyregexp.New(`(?i)<\s*/?\s*br\b[^>]*>`)
	mdMarkupRE  = lazyregexp.New("[*_`]|\\[([^\\]]*)\\]\\([^)]*\\)")
)

// tableColumns maps a normalized header cell to the column it names.
var tableColumns = map[string]string{
	"company": "company", "employer": "company", "company name": "company",
	"role": "role", "title": "role", "position": "role", "job title": "role", "job": "role",
	"location": "location", "locations": "location",
	"application": "apply", "apply": "apply", "link": "apply", "application link": "apply", "application/link": "apply", "links": "apply",
	"age": "age", "date posted": "age", "posted": "age", "date": "age", "date added": "age",
}

// parseListTables reads every job table of a README, Markdown pipe tables and
// HTML tables alike, with the same row semantics.
func parseListTables(readme string) []listRow {
	var rows []listRow
	rest := htmlTableRE.ReplaceAllStringFunc(readme, func(t string) string {
		var table [][]string
		for _, tr := range htmlRowRE.FindAllStringSubmatch(t, -1) {
			var cells []string
			for _, c := range htmlCellRE.FindAllStringSubmatch(tr[1], -1) {
				cells = append(cells, c[1])
			}
			table = append(table, cells)
		}
		rows = append(rows, rowsOfTable(table)...)
		return "\n"
	})
	var table [][]string
	flush := func() {
		rows = append(rows, rowsOfTable(table)...)
		table = nil
	}
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			flush()
			continue
		}
		if mdSepRE.MatchString(line) {
			continue
		}
		table = append(table, splitPipes(line))
	}
	flush()
	return rows
}

func splitPipes(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	return strings.Split(line, "|")
}

// rowsOfTable reads one table: the first row naming a company and a role is
// the header; later rows are data. A table without such a header is not a job
// table.
func rowsOfTable(table [][]string) []listRow {
	cols := map[string]int{}
	var out []listRow
	last := ""
	for _, cells := range table {
		if len(cols) == 0 {
			for i, c := range cells {
				if name, ok := tableColumns[strings.ToLower(strings.TrimSpace(plainCell(c)))]; ok {
					if _, dup := cols[name]; !dup {
						cols[name] = i
					}
				}
			}
			if _, ok := cols["company"]; !ok {
				cols = map[string]int{}
			} else if _, ok := cols["role"]; !ok {
				cols = map[string]int{}
			}
			continue
		}
		cell := func(name string) string {
			if i, ok := cols[name]; ok && i < len(cells) {
				return cells[i]
			}
			return ""
		}
		company := plainCell(cell("company"))
		if company == "" {
			company = last
		} else {
			last = company
		}
		row := listRow{Company: company, Role: plainCell(cell("role")), Location: listLocations(cell("location")), Age: plainCell(cell("age"))}
		apply := cell("apply")
		row.Apply = pickApplyURL(apply)
		if row.Apply == "" {
			// A company-profile link is not an application, so the company
			// cell never feeds the fallback.
			var rest []string
			for name, i := range cols {
				if name != "company" && name != "apply" && i < len(cells) {
					rest = append(rest, cells[i])
				}
			}
			row.Apply = pickApplyURL(strings.Join(rest, " "))
		}
		if row.Apply == "" && (strings.Contains(apply, lockMarker) || strings.Contains(strings.ToLower(plainCell(apply)), "closed")) {
			row.Closed = true
		}
		out = append(out, row)
	}
	return out
}

// listLocations reads a location cell. A cell with several places is a
// details block ("5 locations" summary, one place per line break); the count
// is dropped and the places become one clean list.
func listLocations(cell string) string {
	cell = locCountRE.ReplaceAllString(cell, " ")
	cell = lineBreakRE.ReplaceAllString(cell, ";")
	return cleanLocations(strings.Split(plainCell(cell), ";"))
}

// plainCell is a cell's text with markup, links and the lists' decorative
// markers removed. The markers (a flame, a lock, a flag, an arrow) are symbol
// characters, never letters, so a name in any script keeps every letter.
func plainCell(c string) string {
	c = textOf(c)
	c = mdMarkupRE.ReplaceAllString(c, "$1")
	c = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.So, r) || unicode.Is(unicode.Variation_Selector, r) || r == '\u200d' || r >= 0x1F3FB && r <= 0x1F3FF {
			return ' '
		}
		return r
	}, html.UnescapeString(c))
	return strings.Join(strings.Fields(c), " ")
}

// pickApplyURL is the first link of a cell that is not the list's own
// profile pages, else the first link.
func pickApplyURL(cell string) string {
	var links []string
	for _, m := range hrefLinkRE.FindAllStringSubmatch(cell, -1) {
		links = append(links, html.UnescapeString(m[1]+m[2]))
	}
	for _, m := range mdLinkRE.FindAllStringSubmatch(cell, -1) {
		links = append(links, m[1])
	}
	for _, l := range links {
		if u, err := url.Parse(l); err == nil && !strings.HasSuffix(strings.ToLower(u.Hostname()), "simplify.jobs") {
			return l
		}
	}
	return ""
}
