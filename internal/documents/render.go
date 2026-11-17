package documents

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/documents/assets"
	"applysmith/internal/lazyregexp"
)

func splitName(full string) (first, last string) {
	f := strings.Fields(full)
	switch len(f) {
	case 0:
		return "", ""
	case 1:
		return f[0], ""
	}
	return strings.Join(f[:len(f)-1], " "), f[len(f)-1]
}

func isCurrent(end string) bool {
	switch strings.ToLower(strings.TrimSpace(end)) {
	case "", "present", "current", "now":
		return true
	}
	return false
}

var (
	yearMonthRE = lazyregexp.New(`^(\d{4})(?:[-/.](\d{1,2})(?:[-/.]\d{1,2})?)?$`)
	monthYearRE = lazyregexp.New(`^(\d{1,2})[-/.](\d{4})$`)
)

// entryDate writes a numeric date ("2021-03", "03/2021", "2021") the way a
// résumé prints it: "Mar 2021" (the month in the language's words), or the
// year alone. Any other form is printed as written.
func entryDate(l language, s string) string {
	y, mo := "", ""
	if m := yearMonthRE.FindStringSubmatch(s); m != nil {
		y, mo = m[1], m[2]
	} else if m := monthYearRE.FindStringSubmatch(s); m != nil {
		y, mo = m[2], m[1]
	} else {
		return s
	}
	n, _ := strconv.Atoi(mo)
	switch {
	case mo == "":
		return y
	case n < 1 || n > 12:
		return s
	case l.abbrev:
		return string([]rune(l.months[n-1])[:3]) + " " + y
	}
	return l.months[n-1] + " " + y
}

// dateRange writes the dates of an entry, "Mar 2021 - Present" or
// "2019 - 2021", joined by one ASCII hyphen (an ATS reads it; the PDF check
// ascii-dates refuses a Unicode dash). An experience without an end date is
// current; an education without one shows its start only.
func dateRange(l language, e Entry) string {
	start, end := entryDate(l, e.Start), entryDate(l, e.End)
	switch {
	case e.Start == "" && e.End == "":
		return ""
	case e.Start == "":
		return end
	case isCurrent(e.End) && (e.End != "" || e.Kind == EntryExperience):
		return start + " - " + l.present
	case e.End == "" || e.End == e.Start:
		return start
	}
	return start + " - " + end
}

func contactLinks(c Contact) []assets.Link {
	var out []assets.Link
	for _, l := range c.Links {
		out = append(out, assets.Link{Label: l.Label, URL: l.URL})
	}
	return out
}

// buildResume assembles everything the résumé template prints. The contact
// block, titles, organizations, places and dates come from the manifest; the
// draft supplies only bullets, summary and skill lines.
func buildResume(m Manifest, d ResumeDoc, paper assets.Paper) (assets.Resume, error) {
	l, ok := lookupLanguage(d.Language)
	if !ok {
		return assets.Resume{}, fmt.Errorf("%w: unsupported language %q", ErrInvalid, d.Language)
	}
	first, last := splitName(m.Contact.FullName)
	r := assets.Resume{Paper: paper, Labels: l.labels, FirstName: first, LastName: last, Phone: m.Contact.Phone,
		Email: m.Contact.Email, Links: contactLinks(m.Contact), Profile: Normalize(d.Profile)}
	r.Address = m.Contact.Location
	if m.Contact.Relocation {
		if r.Address != "" {
			r.Address += " - "
		}
		r.Address += l.relocation
	}
	for _, c := range d.Competencies {
		r.Competencies = append(r.Competencies, assets.Competency{Label: Normalize(c.Label), Text: Normalize(c.Text)})
	}
	bullets := func(list []EntryDraft) map[string][]string {
		out := map[string][]string{}
		for _, e := range list {
			for _, b := range e.Bullets {
				out[e.Entry] = append(out[e.Entry], Normalize(b.Text))
			}
		}
		return out
	}
	exp, proj := bullets(d.Experience), bullets(d.Projects)
	for _, key := range m.SelectedEntries {
		e, ok := m.entry(key)
		if !ok {
			continue
		}
		a := assets.Entry{Dates: dateRange(l, e), Title: e.Title, Organization: e.Organization, Location: e.Location, Note: e.Note}
		switch e.Kind {
		case EntryExperience:
			a.Bullets = exp[key]
			r.Experience = append(r.Experience, a)
		case EntryEducation:
			r.Education = append(r.Education, a)
		}
	}
	for _, key := range m.SelectedProjects {
		if e, ok := m.entry(key); ok {
			r.Projects = append(r.Projects, assets.Entry{Title: e.Title, Bullets: proj[key]})
		}
	}
	return r, nil
}

// buildCover assembles the letter template data. The salutation is the
// system's, the contact line comes from the manifest, and a stated relocation
// is added to the closing once, in plain words.
func buildCover(m Manifest, d LetterDoc, paper assets.Paper, now time.Time) (assets.Cover, error) {
	l, ok := lookupLanguage(d.Language)
	if !ok {
		return assets.Cover{}, fmt.Errorf("%w: unsupported language %q", ErrInvalid, d.Language)
	}
	c := assets.Cover{Paper: paper, Name: m.Contact.FullName, Date: l.date(d.Language, now), Salutation: d.Salutation, Closing: l.closing}
	c.Contact = append(c.Contact, assets.Link{Label: m.Contact.Email, URL: "mailto:" + m.Contact.Email})
	if m.Contact.Phone != "" {
		c.Contact = append(c.Contact, assets.Link{Label: m.Contact.Phone})
	}
	c.Contact = append(c.Contact, contactLinks(m.Contact)...)
	c.Blocks = append(c.Blocks, assets.Block{Text: Normalize(d.Opening)})
	if strings.TrimSpace(d.Body) != "" || len(d.Bullets) > 0 {
		blk := assets.Block{Text: Normalize(d.Body)}
		for _, b := range d.Bullets {
			blk.Bullets = append(blk.Bullets, Normalize(b.Text))
		}
		// With no body the bullets follow the opening: an empty paragraph
		// is "\lettercontent{}", which XeLaTeX refuses ("There's no line
		// here to end").
		if blk.Text == "" {
			c.Blocks[0].Bullets = blk.Bullets
		} else {
			c.Blocks = append(c.Blocks, blk)
		}
	}
	if p := m.companyParagraph(d); p != "" {
		c.Blocks = append(c.Blocks, assets.Block{Text: Normalize(p)})
	}
	c.Blocks = append(c.Blocks, assets.Block{Text: closingWithRelocation(m, d)})
	return c, nil
}

// closingWithRelocation is the closing as printed: a stated openness to
// relocation is added once, in plain words, after the applicant's own text.
func closingWithRelocation(m Manifest, d LetterDoc) string {
	closing := Normalize(d.Closing)
	if m.Contact.Relocation && d.Language == "en" {
		closing += " I am open to relocation."
	}
	return closing
}

// resumeMarkdown is the readable projection of a résumé.
func resumeMarkdown(m Manifest, d ResumeDoc) string {
	l, _ := lookupLanguage(d.Language)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", m.Contact.FullName)
	if d.Profile != "" {
		fmt.Fprintf(&b, "%s\n\n", d.Profile)
	}
	if len(d.Competencies) > 0 {
		fmt.Fprintf(&b, "## %s\n\n", l.labels.Competencies)
		for _, c := range d.Competencies {
			fmt.Fprintf(&b, "- %s: %s\n", c.Label, c.Text)
		}
		b.WriteString("\n")
	}
	section := func(title string, keys []string, drafts []EntryDraft) {
		if len(keys) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n", title)
		for _, k := range keys {
			e, ok := m.entry(k)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "### %s", e.Title)
			if e.Organization != "" {
				fmt.Fprintf(&b, ", %s", e.Organization)
			}
			if dr := dateRange(l, e); dr != "" {
				fmt.Fprintf(&b, " (%s)", dr)
			}
			b.WriteString("\n\n")
			for _, ed := range drafts {
				if ed.Entry == k {
					for _, bl := range ed.Bullets {
						fmt.Fprintf(&b, "- %s\n", bl.Text)
					}
				}
			}
			b.WriteString("\n")
		}
	}
	var exp, edu []string
	for _, k := range m.SelectedEntries {
		if e, ok := m.entry(k); ok && e.Kind == EntryExperience {
			exp = append(exp, k)
		} else if ok {
			edu = append(edu, k)
		}
	}
	section(l.labels.Experience, exp, d.Experience)
	section(l.labels.Projects, m.SelectedProjects, d.Projects)
	section(l.labels.Education, edu, nil)
	return b.String()
}

// letterMarkdown is the readable projection of a letter, as printed.
func letterMarkdown(m Manifest, d LetterDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n\n", d.Salutation, d.Opening)
	if d.Body != "" {
		fmt.Fprintf(&b, "%s\n\n", d.Body)
	}
	for _, bl := range d.Bullets {
		fmt.Fprintf(&b, "- %s\n", bl.Text)
	}
	if len(d.Bullets) > 0 {
		b.WriteString("\n")
	}
	if p := m.companyParagraph(d); p != "" {
		fmt.Fprintf(&b, "%s\n\n", p)
	}
	fmt.Fprintf(&b, "%s\n", closingWithRelocation(m, d))
	return b.String()
}
