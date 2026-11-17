package cvparse

import "testing"

func TestMarkdownMarksUpAPlainTextCVAndLeavesItsWords(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"a PDF's text",
			"Jane Sample\njane@mailbox.test · Oakland, CA\n\nSummary\nBackend engineer in Go.\n\nEXPERIENCE\nSenior Engineer, Contoso Sample (2021-03 to present)\n• Rebuilt the ledger service.\n\uf0b7 Led the move of 40 services\nto Kubernetes.\n\nSkills:\nGo, PostgreSQL\n",
			"# Jane Sample\njane@mailbox.test · Oakland, CA\n\n## Summary\nBackend engineer in Go.\n\n## EXPERIENCE\n### Senior Engineer, Contoso Sample (2021-03 to present)\n\n- Rebuilt the ledger service.\n- Led the move of 40 services\nto Kubernetes.\n\n## Skills\nGo, PostgreSQL\n"},
		// Only a first line that reads as a name is one; prose that holds a
		// section word stays prose, and a bullet glyph alone stays.
		{"a title, a contact line and prose",
			"Curriculum Vitae\nJane Sample\nExperience in payments and data.\n•\n",
			"Curriculum Vitae\nJane Sample\nExperience in payments and data.\n•\n"},
		// An entry line needs a section above it and a range of years: a
		// sentence that ends on a year in brackets stays a sentence.
		{"entries and sentences",
			"Engineer, Northwind (2019 to 2021)\nEducation\nBS Physics, Example University (Sep 2014 – May 2018)\nShipped search, used by 2M people (2020)\n",
			"Engineer, Northwind (2019 to 2021)\n## Education\n### BS Physics, Example University (Sep 2014 – May 2018)\n\nShipped search, used by 2M people (2020)\n"},
		{"a contact line first", "jane@mailbox.test | +1 415 555 0142\nExperience\n", "jane@mailbox.test | +1 415 555 0142\n## Experience\n"},
		{"lowercase words are no name", "Contact in header\nEducation\n", "Contact in header\n## Education\n"},
		{"one word is a running header, no name", "Confidential\nEducation\n", "Confidential\n## Education\n"},
		{"Markdown already", "# Jane Sample\n\nExperience\n• Built it.\n", "# Jane Sample\n\nExperience\n• Built it.\n"},
	} {
		if got := Markdown(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	// The name heading is what Identity reads the name and contact line from.
	got := Identity(Markdown("Jane Sample\njane@mailbox.test · Oakland, CA\n\nSummary\nBackend engineer in Go.\n"))
	if got["full_name"] != "Jane Sample" || got["email"] != "jane@mailbox.test" {
		t.Errorf("identity of the marked-up text: %v", got)
	}
}
