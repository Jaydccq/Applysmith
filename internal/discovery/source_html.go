package discovery

import (
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"applysmith/internal/lazyregexp"
)

var (
	ldScriptRE = lazyregexp.New(`(?is)<script\b[^>]*type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
	anchorRE   = lazyregexp.New(`(?is)<a\b([^>]*)>(.*?)</a>`)
)

// attrREs keeps the expression of each attribute name asked for, compiled once.
var attrREs sync.Map

// attr reads one attribute of a tag's attribute text.
func attr(attrs, name string) string {
	re, ok := attrREs.Load(name)
	if !ok {
		re, _ = attrREs.LoadOrStore(name, regexp.MustCompile(`(?is)\b`+regexp.QuoteMeta(name)+`\s*=\s*(?:"([^"]*)"|'([^']*)')`))
	}
	m := re.(*regexp.Regexp).FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1] + m[2])
}

// textOf is the visible text of an HTML fragment, whitespace collapsed.
func textOf(frag string) string {
	frag = dropRE.ReplaceAllString(frag, " ")
	frag = tagRE.ReplaceAllString(frag, " ")
	return strings.Join(strings.Fields(unescapeText(frag)), " ")
}

// unescapeText decodes character references, twice when the page encoded
// them twice (Built In titles arrive as "&amp;quot;"). A second pass cannot
// harm singly encoded text: what remains after one pass has no reference.
func unescapeText(s string) string {
	once := html.UnescapeString(s)
	if once == s || !strings.Contains(once, "&") {
		return once
	}
	return html.UnescapeString(once)
}

// jsonLDPostings returns the JobPosting objects embedded in a page, from
// plain objects, arrays, @graph lists and ItemList entries.
func jsonLDPostings(page string) []map[string]any {
	var out []map[string]any
	for _, m := range ldScriptRE.FindAllStringSubmatch(page, -1) {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v) != nil {
			continue
		}
		collectJobPostings(v, &out, 0)
	}
	return out
}

func collectJobPostings(v any, out *[]map[string]any, depth int) {
	if depth > 6 {
		return
	}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			collectJobPostings(e, out, depth+1)
		}
	case map[string]any:
		if isType(x["@type"], "JobPosting") {
			*out = append(*out, x)
			return
		}
		for _, k := range []string{"@graph", "itemListElement", "item", "mainEntity"} {
			if c, ok := x[k]; ok {
				collectJobPostings(c, out, depth+1)
			}
		}
	}
}

func isType(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return strings.EqualFold(t, want)
	case []any:
		for _, e := range t {
			if isType(e, want) {
				return true
			}
		}
	}
	return false
}

// str reads a JSON scalar as trimmed text.
func str(v any) string {
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	}
	return ""
}

func nested(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// ldLocation joins the places of a JobPosting's jobLocation.
func ldLocation(v any) string {
	var places []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if a, ok := x["address"].(map[string]any); ok {
				var parts []string
				for _, k := range []string{"addressLocality", "addressRegion", "addressCountry"} {
					if s := str(a[k]); s != "" {
						parts = append(parts, s)
					} else if c := str(nested(a, k, "name")); c != "" {
						parts = append(parts, c)
					}
				}
				if len(parts) > 0 {
					places = append(places, strings.Join(parts, ", "))
				}
			} else if s := str(x["address"]); s != "" {
				places = append(places, s)
			} else if s := str(x["name"]); s != "" {
				places = append(places, s)
			}
		case string:
			places = append(places, strings.TrimSpace(x))
		}
	}
	walk(v)
	return strings.Join(places, "; ")
}

// postingFromLD maps a JobPosting object to the common shape.
func postingFromLD(o map[string]any) Posting {
	p := Posting{
		Title:    unescapeText(str(o["title"])),
		URL:      str(o["url"]),
		Location: ldLocation(o["jobLocation"]),
		PostedAt: parseTime(str(o["datePosted"])),
		Company:  unescapeText(str(nested(o, "hiringOrganization", "name"))),
	}
	if p.Company == "" {
		p.Company = unescapeText(str(o["hiringOrganization"]))
	}
	if str(o["jobLocationType"]) == "TELECOMMUTE" {
		p.Remote = true
	}
	p.Description = truncateRunes(plainText(str(o["description"])), maxDescription)
	if id := str(nested(o, "identifier", "value")); id != "" {
		p.JobID = id
	} else if id := str(o["identifier"]); id != "" {
		p.JobID = id
	}
	switch e := o["employmentType"].(type) {
	case string:
		p.EmploymentType = e
	case []any:
		var parts []string
		for _, x := range e {
			parts = append(parts, str(x))
		}
		p.EmploymentType = strings.Join(parts, ", ")
	}
	return p
}
