package documents

import (
	"strings"

	"applysmith/internal/documents/assets"
)

// expectResume reads what the résumé template prints from the data it was
// rendered from. The section headings are listed in the template's own order.
func expectResume(r assets.Resume) Expectation {
	e := Expectation{Name: strings.TrimSpace(r.FirstName + " " + r.LastName), Email: r.Email, Phone: r.Phone, Fonts: resumeFonts}
	e.Order = []string{e.Name}
	var src []string
	add := func(s ...string) { src = append(src, s...) }
	add(e.Name, r.Address, r.Phone, r.Email, unbold(r.Profile))
	if r.Address != "" {
		e.Others = append(e.Others, r.Address)
	}
	for _, l := range r.Links {
		e.Others = append(e.Others, l.Label)
		add(l.Label)
	}
	heading := func(label string, present bool) {
		if present {
			e.Order = append(e.Order, label)
			add(label)
		}
	}
	heading(r.Labels.Competencies, len(r.Competencies) > 0)
	for _, c := range r.Competencies {
		add(c.Label, unbold(c.Text))
	}
	entries := func(list []assets.Entry) {
		for _, x := range list {
			add(x.Title, x.Organization, x.Location, x.Dates, x.Note)
			if x.Dates != "" {
				e.Dates = append(e.Dates, x.Dates)
			}
			for _, b := range x.Bullets {
				add(unbold(b))
			}
		}
	}
	heading(r.Labels.Experience, len(r.Experience) > 0)
	entries(r.Experience)
	heading(r.Labels.Projects, len(r.Projects) > 0)
	entries(r.Projects)
	heading(r.Labels.Education, len(r.Education) > 0)
	entries(r.Education)
	heading(r.Labels.Languages, r.Languages != "")
	add(r.Languages)
	return e.withSource(src)
}

// expectCover reads what the letter template prints. The paragraphs and list
// items are anchors in their printed order, between the name above the letter
// and the signature below it.
func expectCover(c assets.Cover) Expectation {
	e := Expectation{Name: c.Name, Fonts: coverFonts}
	e.Order = []string{c.Name, c.Date, c.Salutation}
	src := []string{c.Name}
	for i, l := range c.Contact {
		switch {
		case i == 0:
			e.Email = strings.TrimPrefix(l.URL, "mailto:")
		case l.URL == "" && looksLikePhone(l.Label):
			e.Phone = l.Label
		default:
			e.Others = append(e.Others, l.Label)
		}
		src = append(src, l.Label)
	}
	src = append(src, c.Date, c.Salutation)
	for _, b := range c.Blocks {
		if t := unbold(b.Text); t != "" {
			e.Order = append(e.Order, t)
			src = append(src, t)
		}
		for _, item := range b.Bullets {
			e.Order = append(e.Order, unbold(item))
			src = append(src, unbold(item))
		}
	}
	e.Order = append(e.Order, c.Closing, c.Name)
	return e.withSource(append(src, c.Closing, c.Name))
}

func looksLikePhone(s string) bool {
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case strings.ContainsRune("+-.() ", r):
		default:
			return false
		}
	}
	return digits >= 6
}

// withSource joins the printed strings and takes the percentages and the date
// ranges from them, so a figure inside a bullet is held to the same rule as a
// dated entry.
func (e Expectation) withSource(parts []string) Expectation {
	e.Source = strings.Join(parts, "\n")
	e.Percents = append(e.Percents, percentRE.FindAllString(e.Source, -1)...)
	e.Dates = append(e.Dates, dateRE.FindAllString(e.Source, -1)...)
	e.Dates, e.Percents = uniq(e.Dates), uniq(e.Percents)
	return e
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
