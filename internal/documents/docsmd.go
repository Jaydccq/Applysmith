package documents

import (
	"fmt"
	"strings"
)

func postingMarkdown(v *version) string {
	if v.posting == "" {
		return "# Posting\n\nNo job description was supplied; this is a baseline run with limited context.\n"
	}
	return "# Posting\n\n" + v.posting + "\n"
}

func companyMarkdown(m Manifest) string {
	var b strings.Builder
	b.WriteString("# Company research\n\n")
	if len(m.CompanyFacts) == 0 {
		b.WriteString("No verified company context was available. The documents make no company assertion beyond the posting.\n")
		return b.String()
	}
	for _, c := range m.CompanyFacts {
		fmt.Fprintf(&b, "- %s: %s (source: %s)\n", c.ID, c.Text, c.Source)
	}
	return b.String()
}

func successMarkdown(m Manifest) string {
	var b strings.Builder
	b.WriteString("# Success profile\n\nGeneration: deterministic extraction from the posting.\n\n")
	fmt.Fprintf(&b, "Role: %s\n\n## Top requirements\n\n", m.Role)
	for _, r := range m.Requirements {
		fmt.Fprintf(&b, "- %s: %s\n", r.ID, r.Text)
	}
	b.WriteString("\n## Core keywords\n\n" + strings.Join(m.Keywords, ", ") + "\n\n## Nice to have\n\n")
	for _, n := range m.NiceToHave {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	return b.String()
}

func strategyMarkdown(m Manifest, gen Generation, repair *RepairRecord) string {
	var b strings.Builder
	b.WriteString("# Application strategy\n\n")
	if m.Limited {
		b.WriteString("Limited context: no job description was available, so nothing here claims tailoring.\n\n")
	}
	fmt.Fprintf(&b, "Content generation: %s.\n\n## Themes to lead with\n\n", gen)
	coverage := m.Coverage
	if repair != nil && len(repair.Coverage.After) > 0 {
		coverage = repair.Coverage.After // the themes follow what the document still shows
	}
	for _, c := range coverage {
		if len(c.Evidence) > 0 {
			fmt.Fprintf(&b, "- %s is supported by %s\n", c.Requirement, strings.Join(c.Evidence, ", "))
		}
	}
	b.WriteString("\n## Gaps\n\n")
	lost := repair.lostRequirements()
	for _, r := range lost {
		fmt.Fprintf(&b, "- %s: layout repair removed the last line that backed it\n", r)
	}
	if len(m.Gaps) == 0 && len(lost) == 0 {
		b.WriteString("None found.\n")
	}
	for _, g := range m.Gaps {
		fmt.Fprintf(&b, "- %s\n", g)
	}
	return b.String()
}

func selectionMarkdown(m Manifest) string {
	var b strings.Builder
	b.WriteString("# Selection manifest\n\nLater stages use only the entries and evidence below.\n\n")
	fmt.Fprintf(&b, "Manifest hash: %s\n\n", m.Hash)
	for _, k := range append(append([]string(nil), m.SelectedEntries...), m.SelectedProjects...) {
		e, ok := m.entry(k)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "## %s: %s %s\n\n", e.Key, e.Title, e.Organization)
		for _, id := range e.Evidence {
			if ev, ok := m.evidence(id); ok && m.isSelected(id) {
				fmt.Fprintf(&b, "- %s (%s): %s\n", ev.ID, ev.Source, ev.Text)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("## Excluded\n\n")
	for _, x := range m.Excluded {
		fmt.Fprintf(&b, "- %s: %s\n", x.Key, x.Reason)
	}
	return b.String()
}

func materialsMarkdown(m Manifest) string {
	var b strings.Builder
	b.WriteString("# Materials\n\n")
	fmt.Fprintf(&b, "Query: %s\n\nRetrieved snippet sections: %d\n\n", m.Retrieval.Query, m.Retrieval.Retrieved)
	for _, it := range m.Retrieval.Items {
		fmt.Fprintf(&b, "- %s (score %.2f; %s)\n", it.Source, it.Score, strings.Join(it.Reasons, ", "))
	}
	for _, w := range m.Warnings {
		fmt.Fprintf(&b, "\nWarning: %s\n", w)
	}
	b.WriteString("\n## Retrieved text\n\n")
	for _, e := range m.Evidence {
		if e.Kind == "snippet" {
			fmt.Fprintf(&b, "- %s (%s): %s\n", e.ID, e.Source, e.Text)
		}
	}
	return b.String()
}

func gateMarkdown(title string, m Manifest, g Gate) string {
	var b strings.Builder
	result := "pass"
	if !g.OK() {
		result = "fail"
	}
	projects := "none"
	if len(m.SelectedProjects) > 0 {
		projects = strings.Join(m.SelectedProjects, ", ")
	}
	fmt.Fprintf(&b, "# %s\n\nMode: %s\nResult: %s\nlimited_context: %t\nLines: %d\nCharacters: %d\nSelected projects: %s\n\n## Errors\n\n",
		title, m.Mode, result, m.Limited, g.Lines, g.Chars, projects)
	for _, f := range g.Errors {
		fmt.Fprintf(&b, "- %s: %s\n", f.Code, f.Detail)
	}
	b.WriteString("\n## Warnings\n\n")
	for _, f := range g.Warnings {
		fmt.Fprintf(&b, "- %s: %s\n", f.Code, f.Detail)
	}
	return b.String()
}
