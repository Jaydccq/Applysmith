// Package cvparse extracts a draft of the profile history from a Markdown or plain-text CV.
package cvparse

import (
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Draft is what the CV parser found: entries for the profile's history and
// skills keys. Nothing here is stored until the user confirms it.
type Draft struct {
	Employment []Job    `json:"employment,omitempty"`
	Education  []Study  `json:"education,omitempty"`
	Skills     []string `json:"skills,omitempty"`
}

// Job is one extracted employment entry. Start and End are YYYY-MM, a bare
// YYYY when the CV gave no month, or (End only) "present".
type Job struct {
	Employer string `json:"employer"`
	Title    string `json:"title,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
}

// Study is one extracted education entry.
type Study struct {
	School string `json:"school"`
	Degree string `json:"degree,omitempty"`
	Field  string `json:"field,omitempty"`
	Start  string `json:"start,omitempty"`
	End    string `json:"end,omitempty"`
}

// degreeFieldREs read a degree written with its field: initials before the
// field ("B.S. Computer Science", "MS, Data Science", "Ph.D.: Physics"), or a
// spelled degree with a separator before it ("Bachelor of Science in Computer
// Science", "Master of Science, Statistics"). A spelled degree with none keeps
// its whole text: "Master of Business Administration" names no field apart.
var degreeFieldREs = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)^((?:bachelor|master|doctor|associate)(?:'?s)?(?:\s+of\s+.+?)?)(?:\s*[,:]\s*|\s+in\s+|\s+-\s+)([A-Za-z].*)$`),
	lazyregexp.New(`(?i)^([BM]\.?\s?(?:Sc|S|A|Eng)\.?|Ph\.?\s?D\.?|MBA)(?:\s*[,:]\s*|\s+in\s+|\s+-\s+|\s+)([A-Za-z].*)$`),
}

// study builds an education entry, its field split from the degree.
func study(school, degree, start, end string) Study {
	st := Study{School: school, Degree: degree, Start: start, End: end}
	for _, re := range degreeFieldREs {
		if m := re.FindStringSubmatch(degree); m != nil {
			st.Degree, st.Field = strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
			break
		}
	}
	return st
}

// Dated reports whether the draft holds at least one dated entry.
func (d Draft) Dated() bool {
	for _, j := range d.Employment {
		if j.Start != "" {
			return true
		}
	}
	for _, e := range d.Education {
		if e.Start != "" || e.End != "" {
			return true
		}
	}
	return false
}

// NeedsMonth counts the entries with a start or end the CV gave as a bare year.
func (d Draft) NeedsMonth() int {
	n := 0
	for _, j := range d.Employment {
		if YearOnly(j.Start) || YearOnly(j.End) {
			n++
		}
	}
	for _, e := range d.Education {
		if YearOnly(e.Start) || YearOnly(e.End) {
			n++
		}
	}
	return n
}

// Entries counts the extracted history and skills.
func (d Draft) Entries() int { return len(d.Employment) + len(d.Education) + len(d.Skills) }

// monthName is an English month, long or short, with an optional period
// ("Sep.", "Sept", "September").
const monthName = `(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?`

var (
	// dateRange groups: 1 start month name, 2 start year, 3 start month
	// number, 4 end month name, 5 end year, 6 end month number, 7 present.
	dateRange = lazyregexp.New(`(?i)\b(?:` + monthName + `\s+)?((?:19|20)\d{2})(?:[-/.](0?[1-9]|1[0-2]))?\s*(?:-|–|—|to)\s*(?:(?:` + monthName + `\s+)?((?:19|20)\d{2})(?:[-/.](0?[1-9]|1[0-2]))?|(present|current|now))\b`)
	// singleYear groups: 1 month name, 2 year.
	singleYear = lazyregexp.New(`(?i)\b(?:` + monthName + `\s+)?((?:19|20)\d{2})\b`)
	headingRE  = lazyregexp.New(`^(#{1,6})\s+(.*)$`)
	bulletRE   = lazyregexp.New(`^[-*+•]\s`)
	expSection = lazyregexp.New(`(?i)experience|employment|work history|career`)
	eduSection = lazyregexp.New(`(?i)education|studies|qualification`)
	skillsSect = lazyregexp.New(`(?i)skills|technologies|competenc`)
	splitSep   = lazyregexp.New(`\s+at\s+|\s+@\s+|,\s+|\s+[-–—|]\s+`)
)

// monthOf joins a year and an optional month. A CV that gives only a year stays
// year-only ("2019"): the month is never guessed, and YearOnly tells the
// caller it is still to be asked.
func monthOf(year, month string) string {
	if month == "" {
		return year
	}
	if len(month) == 1 {
		month = "0" + month
	}
	return year + "-" + month
}

var monthNumbers = map[string]string{"jan": "01", "feb": "02", "mar": "03", "apr": "04", "may": "05", "jun": "06",
	"jul": "07", "aug": "08", "sep": "09", "oct": "10", "nov": "11", "dec": "12"}

// monthNumber is the two-digit month of an English month name, or "".
func monthNumber(name string) string {
	if len(name) < 3 {
		return ""
	}
	return monthNumbers[strings.ToLower(name[:3])]
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// YearOnly reports a date the CV gave without a month. The profile only holds
// YYYY-MM, so the user supplies the month before such an entry is stored.
func YearOnly(date string) bool { return len(date) == 4 }

// Locator says where in the document an extracted entry came from.
type Locator struct {
	Line    int    `json:"line"`
	Section string `json:"section,omitempty"`
}

// Located holds one Locator per entry of a Draft, in the same order.
type Located struct {
	Employment []Locator
	Education  []Locator
	Skills     []Locator
}

// Parse extracts dated employment and education entries and skills from a
// Markdown or plain-text CV. It is deliberately conservative: an entry needs a date.
func Parse(md string) Draft {
	d, _ := ParseLocated(md)
	return d
}

// ParseLocated is Parse that also reports the line and heading of every entry,
// so a reviewer can find the source of a proposed fact.
func ParseLocated(md string) (Draft, Located) {
	var d Draft
	var loc Located
	section, heading := "", ""
	// ctx holds the last two undated lines since the section heading or the
	// last entry, places and bullets left out: the heading and role lines a
	// line of only dates belongs to.
	var ctx []string
	for i, line := range strings.Split(md, "\n") {
		trim := strings.TrimSpace(line)
		if h, ok := sectionHeading(trim); ok {
			heading, ctx = h, nil
			switch {
			case expSection.MatchString(h):
				section = "exp"
			case eduSection.MatchString(h):
				section = "edu"
			case skillsSect.MatchString(h):
				section = "skills"
			default:
				section = ""
			}
			continue
		}
		if trim == "" || strings.HasPrefix(trim, "<!--") {
			continue
		}
		at := Locator{Line: i + 1, Section: heading}
		switch section {
		case "exp", "edu":
			jobs, studies := len(d.Employment), len(d.Education)
			switch t := tidy(trim); {
			case entry(&d, section, trim, ctx):
				ctx = nil
			case t != "" && !isLocation(t) && !bulletRE.MatchString(trim):
				ctx = append(ctx, trim)
				if len(ctx) > 2 {
					ctx = ctx[1:]
				}
			}
			for ; jobs < len(d.Employment); jobs++ {
				loc.Employment = append(loc.Employment, at)
			}
			for ; studies < len(d.Education); studies++ {
				loc.Education = append(loc.Education, at)
			}
		case "skills":
			if headingRE.MatchString(trim) {
				continue // a sub-heading groups skills; it is none itself
			}
			for _, name := range skillsOf(trim) {
				d.Skills = append(d.Skills, name)
				loc.Skills = append(loc.Skills, at)
			}
		}
	}
	return d, loc
}

// sectionHeading accepts Markdown headings and whole-line conventional CV
// labels, so local PDF/DOCX text uses the same dated-entry parser. Ordinary
// prose containing a section word is never promoted to a heading.
func sectionHeading(line string) (string, bool) {
	if m := headingRE.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[2]), len(m[1]) <= 2
	}
	switch strings.ToLower(strings.Join(strings.Fields(strings.TrimSuffix(line, ":")), " ")) {
	case "experience", "work experience", "professional experience", "employment", "employment history", "work history", "career history",
		"education", "studies", "qualifications", "academic qualifications",
		"skills", "technical skills", "technologies", "competencies", "technical competencies",
		"summary", "professional summary", "profile", "projects", "certifications", "awards", "publications", "interests", "references", "languages":
		return line, true
	}
	return "", false
}

// entry records the dated entry on line, if it makes one, and reports whether
// the line carried a date. ctx is the undated lines before it: a dated line
// that holds only dates or a place takes its title and employer from there,
// from one line ("Engineer, Northwind") or from two (the organisation's
// heading, then the role).
func entry(d *Draft, section, line string, ctx []string) bool {
	start, end := "", ""
	text := line
	if m := dateRange.FindStringSubmatchIndex(line); m != nil {
		g := dateRange.FindStringSubmatch(line)
		start = monthOf(g[2], firstOf(g[3], monthNumber(g[1])))
		if g[7] != "" {
			end = "present"
		} else {
			end = monthOf(g[5], firstOf(g[6], monthNumber(g[4])))
		}
		text = line[:m[0]] + line[m[1]:]
	} else if section == "edu" {
		if m := singleYear.FindStringSubmatchIndex(line); m != nil {
			g := singleYear.FindStringSubmatch(line)
			end = monthOf(g[2], monthNumber(g[1]))
			text = line[:m[0]] + line[m[1]:]
		}
	}
	if start == "" && end == "" {
		return false
	}
	text = tidy(text)
	if text == "" || isLocation(text) {
		switch len(ctx) {
		case 0:
			text = ""
		case 1:
			text = tidy(ctx[0])
		default:
			org, role := tidy(ctx[0]), tidy(ctx[1])
			if section == "exp" {
				d.Employment = append(d.Employment, Job{Employer: org, Title: role, Start: start, End: end})
			} else {
				d.Education = append(d.Education, study(org, role, start, end))
			}
			return true
		}
	}
	first, rest := text, ""
	if loc := splitSep.FindStringIndex(text); loc != nil {
		first, rest = strings.TrimSpace(text[:loc[0]]), stripLocation(strings.TrimSpace(text[loc[1]:]))
	}
	if first == "" && rest == "" {
		return true
	}
	if section == "exp" {
		job := Job{Employer: rest, Title: first, Start: start, End: end}
		if rest == "" {
			job.Employer, job.Title = first, ""
		}
		d.Employment = append(d.Employment, job)
		return true
	}
	st := study(rest, first, start, end)
	if rest == "" {
		st = Study{School: first, Start: start, End: end}
	}
	d.Education = append(d.Education, st)
	return true
}

// tidy strips Markdown marks and the separators a CV puts between dates and
// a place ("2019-06 – 2022-02 · Remote").
func tidy(text string) string {
	text = strings.Trim(strings.NewReplacer("#", "", "*", "", "_", "", "()", "").Replace(text), " \t-–—|,()·•")
	return strings.TrimSpace(strings.TrimLeft(text, "-+ "))
}

var (
	remoteWord = lazyregexp.New(`(?i)^(remote|hybrid|on-?site)$`)
	stateCode  = lazyregexp.New(`^[A-Z]{2}(\s+\d{4,5})?$`)
	countries  = map[string]bool{
		"united states": true, "usa": true, "us": true, "united kingdom": true, "uk": true, "england": true, "scotland": true,
		"wales": true, "ireland": true, "canada": true, "germany": true, "france": true, "spain": true, "italy": true,
		"netherlands": true, "portugal": true, "sweden": true, "norway": true, "denmark": true, "finland": true, "poland": true,
		"switzerland": true, "austria": true, "belgium": true, "australia": true, "new zealand": true, "india": true,
		"china": true, "japan": true, "singapore": true, "brazil": true, "mexico": true, "israel": true, "romania": true,
	}
)

// isLocation reports whether text is only a place: "Remote", "City, ST",
// "City, Country" or "City, ST, Country".
func isLocation(text string) bool {
	if remoteWord.MatchString(text) {
		return true
	}
	parts := strings.Split(text, ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	switch len(parts) {
	case 2:
		return strings.TrimSpace(parts[0]) != "" && (stateCode.MatchString(last) || countries[strings.ToLower(last)])
	case 3: // "City, ST, Country"
		return strings.TrimSpace(parts[0]) != "" && stateCode.MatchString(strings.TrimSpace(parts[1])) && countries[strings.ToLower(last)]
	}
	return false
}

// stripLocation drops a trailing ", City, ST", ", City, Country" or ", Remote"
// from an employer.
func stripLocation(s string) string {
	parts := strings.Split(s, ",")
	for _, k := range []int{2, 1} {
		if len(parts) > k && (k == 2 && isLocation(strings.Join(parts[len(parts)-2:], ",")) || k == 1 && remoteWord.MatchString(strings.TrimSpace(parts[len(parts)-1]))) {
			return strings.TrimSpace(strings.Join(parts[:len(parts)-k], ","))
		}
	}
	return s
}

// skillsOf splits a skills line at commas, semicolons and bars outside
// parentheses, so "AWS (ECS, Lambda)" stays one skill. A parenthesised group
// too long for one skill becomes its head and each item inside.
func skillsOf(line string) []string {
	line = strings.TrimLeft(line, "-*+ ")
	if i := strings.Index(line, ":"); i > 0 && i < 30 {
		line = line[i+1:]
	}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(strings.NewReplacer("*", "", "_", "").Replace(p))
		if p != "" && len(p) <= 40 {
			out = append(out, p)
		}
	}
	for _, p := range splitOutsideParens(line) {
		head, inner, grouped := strings.Cut(p, "(")
		if !grouped || len(strings.TrimSpace(p)) <= 40 {
			add(p)
			continue
		}
		add(head)
		for _, q := range strings.FieldsFunc(strings.TrimSuffix(strings.TrimSpace(inner), ")"), func(r rune) bool { return r == ',' || r == ';' || r == '|' }) {
			add(q)
		}
	}
	return out
}

func splitOutsideParens(line string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range line {
		switch {
		case r == '(' || r == '[':
			depth++
		case (r == ')' || r == ']') && depth > 0:
			depth--
		case depth == 0 && (r == ',' || r == ';' || r == '|'):
			parts = append(parts, line[start:i])
			start = i + 1
		}
	}
	return append(parts, line[start:])
}

// Summary counts the entries by kind, for a one-line report.
func (d Draft) Summary() string {
	return count(len(d.Employment), "job", "jobs") + ", " + count(len(d.Education), "degree", "degrees") + ", " + count(len(d.Skills), "skill", "skills")
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

var (
	contactLabel = lazyregexp.New(`(?i)^(?:\*\*([^*]{1,30}):\*\*|\*\*([^*]{1,30})\*\*:|(e-?mail|phone|mobile|tel|github|linkedin|website|portfolio|location|address):)\s*`)
	contactSep   = lazyregexp.New(`\s+[|·•]\s+`)
	// contactish is a line under the name that reads as a contact detail (an
	// address, a link or a number) rather than a headline or a summary.
	contactish = lazyregexp.New(`(?i)@|https?://|www\.|\.[a-z]{2,}/|\d{3}`)
	// unreal is a value still holding a template's placeholder or the example
	// persona, which is not the person's own.
	unreal      = lazyregexp.New(`(?i)[\[\]<>{}]|\btodo\b|example`)
	cvTitle     = lazyregexp.New(`(?i)^(cv|r[eé]sum[eé]|curriculum vitae)$`)
	mdLink      = lazyregexp.New(`\]\(([^)\s]+)\)?$`)
	emailRE     = lazyregexp.New(`(?i)[^\s@<>()\[\]:;,]+@[^\s@<>()\[\]:;,]+\.[a-z]{2,}`)
	phoneRE     = lazyregexp.New(`^\+?[\d\s().-]+$`)
	siteRE      = lazyregexp.New(`(?i)^(https?://|www\.)\S+$|^[\w-]+(\.[\w-]+)+(/\S*)?$`)
	linkedInRE  = lazyregexp.New(`(?i)linkedin\.com/`)
	gitHubRE    = lazyregexp.New(`(?i)github\.com/`)
	placeLabel  = lazyregexp.New(`(?i)location|address`)
	notAPlaceRE = lazyregexp.New(`(?i)\d|remote`)
)

const contactEdges = " \t,|·•"

type contactPart struct{ label, value string }

// header reads the name a Markdown CV opens with (its first heading, at level
// one) and the contact parts of the lines under it, up to the first section.
func header(md string) (string, []contactPart) {
	name, fence := "", false
	var parts []contactPart
	for _, raw := range strings.Split(md, "\n") {
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "```") {
			fence = !fence
		}
		level := 0
		if m := headingRE.FindStringSubmatch(strings.TrimRight(raw, " \t\r")); m != nil && !fence {
			text := strings.TrimSpace(m[2])
			if t := strings.TrimRight(text, "#"); t == "" || strings.HasSuffix(t, " ") || strings.HasSuffix(t, "\t") {
				text = strings.TrimSpace(t)
			}
			if text != "" {
				level = len(m[1])
				if level == 1 && name == "" {
					name = text
					continue
				}
			}
		}
		switch {
		case level >= 1 && level <= 3:
			return name, parts
		case name == "" || level > 3 || trim == "" || strings.HasPrefix(trim, "<!--"):
		case contactSep.MatchString(trim) || contactLabel.MatchString(trim) || contactish.MatchString(trim):
			for _, p := range contactSep.Split(trim, -1) {
				c := contactPart{value: p}
				if m := contactLabel.FindStringSubmatch(p); m != nil {
					c = contactPart{label: strings.TrimSpace(m[1] + m[2] + m[3]), value: p[len(m[0]):]}
				}
				// A bracket at an edge goes unless it closes one in the part,
				// as in "(512) 555-0199".
				c.value = strings.Trim(c.value, contactEdges)
				if strings.Count(c.value, "(") != strings.Count(c.value, ")") || strings.HasPrefix(c.value, "(") && strings.HasSuffix(c.value, ")") {
					c.value = strings.Trim(c.value, contactEdges+"()")
				}
				if c.value != "" {
					parts = append(parts, c)
				}
			}
		}
	}
	return name, parts
}

func https(v string) string {
	if low := strings.ToLower(v); strings.HasPrefix(low, "http://") {
		v = v[len("http://"):]
	} else if strings.HasPrefix(low, "https://") {
		return v
	}
	return "https://" + v
}

// Identity reads the name over a Markdown CV and its contact line, and
// answers each part by the identity field it reads as: full_name, email,
// phone, city, region, country, linkedin, github or portfolio, the fields of
// the setup identity step. The first part that reads as a field wins; a part
// that reads as nothing in particular, or as a template's placeholder or the
// example persona, is left out.
func Identity(md string) map[string]string {
	name, parts := header(md)
	out := map[string]string{}
	put := func(k, v string) {
		if _, ok := out[k]; !ok && v != "" && !unreal.MatchString(v) {
			out[k] = v
		}
	}
	if !cvTitle.MatchString(name) {
		put("full_name", name)
	}
	for _, c := range parts {
		v := c.value
		if m := mdLink.FindStringSubmatch(v); m != nil {
			v = m[1]
		} else {
			v = strings.TrimSuffix(strings.TrimPrefix(v, "<"), ">")
		}
		if strings.HasPrefix(strings.ToLower(v), "mailto:") {
			v = v[len("mailto:"):]
		}
		v = strings.TrimSpace(v)
		digits := 0
		for _, r := range v {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		switch {
		case emailRE.MatchString(v):
			put("email", emailRE.FindString(v))
		case linkedInRE.MatchString(v):
			put("linkedin", https(v))
		case gitHubRE.MatchString(v):
			put("github", https(v))
		case phoneRE.MatchString(v) && digits >= 7 && digits <= 15:
			if strings.HasPrefix(v, "+") {
				v = strings.NewReplacer(" ", "", "(", "", ")", "", ".", "", "-", "", "\t", "").Replace(v)
			}
			put("phone", v)
		case siteRE.MatchString(v):
			put("portfolio", https(v))
		case !notAPlaceRE.MatchString(v) && (placeLabel.MatchString(c.label) || c.label == "" && strings.Contains(v, ",")):
			place := strings.Split(v, ",")
			for i, k := range []string{"city", "region", "country"} {
				if i < len(place) {
					put(k, strings.TrimSpace(place[i]))
				}
			}
		}
	}
	return out
}
