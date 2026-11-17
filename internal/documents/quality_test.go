package documents

import (
	"strings"
	"testing"
	"time"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/kb"
	"applysmith/internal/workspace"
)

func projectTitles(m Manifest) []string {
	var out []string
	for _, e := range m.Entries {
		if e.Kind == EntryProject {
			out = append(out, e.Title)
		}
	}
	return out
}

// A CV that heads its projects "Project Experience" names both groups. Its
// sections are projects: read as roles they match no employer, and their
// lines belong to no entry a résumé can print.
func TestManifest_ProjectExperienceSectionsAreProjects(t *testing.T) {
	cv := strings.Replace(testCV, "## Projects", "## Project Experience", 1)
	m := manifestFor(t, cv, testPosting)
	if got := projectTitles(m); len(got) != 1 || got[0] != "Reef Survey Tool" {
		t.Fatalf("projects = %q, want the Reef Survey Tool", got)
	}
	for _, e := range m.Evidence {
		if strings.Contains(e.Text, "5K+ survey files") && !strings.HasPrefix(e.Entry, "P") {
			t.Errorf("the project line belongs to entry %q", e.Entry)
		}
	}
}

// Sections of one knowledge document are one project, named by the document:
// a section heading is not a project a résumé can list.
func TestManifest_KnowledgeProjectIsNamedByItsDocument(t *testing.T) {
	p, rep := workspace.ParseProfile([]byte(testProfile))
	if !rep.OK() {
		t.Fatalf("profile: %+v", rep)
	}
	fact := func(heading, text string) kb.Fact {
		return kb.Fact{ID: "snippet:" + heading, SourceKind: kb.KindSnippet, Title: "Ferry Scheduler",
			SourceRef: "knowledge/projects/ferry.md#" + heading, Text: text}
	}
	m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(testProfile), CV: testCV, Posting: testPosting, Company: "Example Corp", Role: "Backend Engineer",
		Snippets: []kb.Fact{fact("What it is", "- Built a Go scheduler for 30 ferry routes"), fact("Why I am proud of it", "- Cut missed sailings by 12% with PostgreSQL triggers")}}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, title := range projectTitles(m) {
		if title == "Ferry Scheduler" {
			n++
		} else if title != "Reef Survey Tool" {
			t.Errorf("project %q is a section heading", title)
		}
	}
	if n != 1 {
		t.Errorf("%d Ferry Scheduler projects, want one for the document", n)
	}
}

// Entries print newest first, whatever order the profile lists them in, and a
// numeric grade average says what it is.
func TestResume_NewestEntryFirstAndGradeLabelled(t *testing.T) {
	profile := strings.Replace(testProfile, "      start: 2011-09\n      end: 2015-06\n",
		"      start: 2011-09\n      end: 2015-06\n      grade_average: \"3.8\"\n    - school: Sample Institute\n      degree: MSc\n      field: Computing\n      start: 2015-09\n      end: 2017-06\n", 1)
	profile = strings.Replace(profile, "    - employer: Example Systems", "    - employer: Older Works\n      title: Intern\n      start: 2016-06\n      end: 2016-09\n    - employer: Example Systems", 1)
	m := manifestWith(t, profile, testCV, testPosting)
	r, err := buildResume(m, deterministicResume(m, "en"), assets.PaperA4)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Education) != 2 || r.Education[0].Organization != "Sample Institute" || r.Education[1].Note != "GPA 3.8" {
		t.Errorf("education = %+v", r.Education)
	}
	if len(r.Experience) == 0 || r.Experience[0].Organization != "Example Systems" {
		t.Errorf("the current role is not first: %+v", r.Experience)
	}
	for i := 1; i < len(r.Experience); i++ {
		if r.Experience[i].Organization == "Older Works" && i != len(r.Experience)-1 {
			t.Errorf("the oldest role is not last: %+v", r.Experience)
		}
	}
	if r.Title != "Alex Example - Resume" {
		t.Errorf("PDF title %q", r.Title)
	}
}

// The letter names the company and the role above the salutation and keeps
// the profile links off the email line.
func TestCover_RecipientLinesAndLinks(t *testing.T) {
	m := testManifest(t)
	d, ok := deterministicLetter(m, deterministicResume(m, "en"), "en")
	if !ok {
		t.Fatal("no deterministic letter")
	}
	d.Salutation = "Dear Hiring Team,"
	c, err := buildCover(m, d, assets.PaperA4, time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Recipient, "|") != "Example Corp|Re: Backend Engineer" || len(c.Contact) != 2 || len(c.Links) != 1 || c.Title != "Alex Example - Cover Letter" {
		t.Errorf("recipient %q, contact %+v, links %+v, title %q", c.Recipient, c.Contact, c.Links, c.Title)
	}
	e := expectCover(c)
	wantAll(t, FinalChecks(e, layerOutput(layer(e), fontsOf(e.Fonts))), toolchain.Pass)
}

// A word the text layer holds only with a space inside it fails, however many
// other words are whole: a program searching the text does not find it.
func TestFinalChecks_SplitWordFails(t *testing.T) {
	e := expectCover(fixtureCover())
	text := strings.Replace(layer(e), "Dear Hiring", "D ear Hiring", 1)
	if text == layer(e) {
		t.Fatal("the fixture has no salutation")
	}
	c := status(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), CheckWords)
	if c.Status != toolchain.Fail || !strings.Contains(c.Reason, "dear") {
		t.Errorf("words = %+v", c)
	}
}

// The second letter attempt is told why the first was refused.
func TestRetryNote_QuotesTheRefusal(t *testing.T) {
	var g Gate
	g.fail("unsupported-figure", "letter: %q is not in the selected evidence", "many")
	if n := retryNote(g); !strings.Contains(n, "unsupported-figure") || !strings.Contains(n, "many") || !strings.Contains(n, "<data kind=\"refused\">") {
		t.Errorf("note = %q", n)
	}
}
