package documents

import (
	"strings"
	"testing"
)

func TestGate_HeaderGrounding_SummaryNotMistakenForLocation(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	mig := evID(t, m, "platform migration")
	gate := func(summary string) Gate {
		d := docWith(m, "X1", "Led the platform migration", mig)
		d.Profile = summary
		return checkResume(m, d)
	}
	// A place named in a summary sentence is a name to ground, not a claim
	// about where the user lives or will move.
	for _, summary := range []string{
		"Backend engineer who worked with teams in Sampleville and Exampleland.",
		"Backend engineer based in Sampleville.",
	} {
		if g := gate(summary); hasCode(g, "header-grounding") {
			t.Errorf("%q: %+v", summary, g.Errors)
		}
	}
	closed := strings.Replace(testProfile, "default: open", "default: closed", 1)
	mc := manifestWith(t, closed, testCV, testPosting)
	d := docWith(mc, "X1", "Led the platform migration", evID(t, mc, "platform migration"))
	d.Profile = "Backend engineer ready to relocate to Sampleville."
	if g := checkResume(mc, d); !hasCode(g, "header-grounding") {
		t.Errorf("a relocation claim the profile does not state: %+v", g.Errors)
	}
	d.Profile = "Backend engineer based in Sampleville."
	if g := checkResume(mc, d); hasCode(g, "header-grounding") {
		t.Errorf("a location sentence is not a relocation claim: %+v", g.Errors)
	}
}

func TestGate_DuplicatedLine_DistanceDecidesSeverity(t *testing.T) {
	long := "Led the platform migration and cut incidents by 40% across 12 services in the data platform group"
	filler := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "Short unrelated line " + strings.Repeat("x", i+1)
		}
		return out
	}
	for _, c := range []struct {
		name      string
		between   int
		errCode   string
		warnCode  string
		shortLine bool
	}{
		{"five lines apart", 4, "duplicated-line", "", false},
		{"six lines apart", 5, "", "repeated-line", false},
		{"a short line repeated", 0, "", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			line := long
			if c.shortLine {
				line = "Mentored four engineers"
			}
			lines := append(append([]string{line}, filler(c.between)...), line)
			var g Gate
			checkDuplicates(&g, lines)
			gotErr, gotWarn := "", ""
			if len(g.Errors) > 0 {
				gotErr = g.Errors[0].Code
			}
			if len(g.Warnings) > 0 {
				gotWarn = g.Warnings[0].Code
			}
			if gotErr != c.errCode || gotWarn != c.warnCode {
				t.Fatalf("error %q warning %q, want %q and %q", gotErr, gotWarn, c.errCode, c.warnCode)
			}
		})
	}
}

func TestGate_InternalHeadingsAndWorkTraceLabels(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	mig := evID(t, m, "platform migration")
	for _, label := range []string{
		"Situation:", "ACTION:", "Implementation Evidence", "TARGET: Senior Backend Engineer", "Fit 4/5",
	} {
		t.Run(label, func(t *testing.T) {
			d := docWith(m, "X1", "Led the platform migration\n"+label, mig)
			if g := checkResume(m, d); !hasCode(g, "artifact-label") {
				t.Errorf("errors = %+v", g.Errors)
			}
		})
	}
	if g := checkResume(m, docWith(m, "X1", "Led the platform migration and took action on the task list", mig)); hasCode(g, "artifact-label") {
		t.Errorf("an ordinary sentence with the same words: %+v", g.Errors)
	}
}

func TestGate_RawCode_FrameworkNameNotAPath(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	mig := evID(t, m, "platform migration")
	for _, c := range []struct {
		text string
		raw  bool
	}{
		{"Fixed internal/store/migrate.go", true},
		{"Updated migrate.go", true},
		{"Led the platform migration of the React/Next.js front end", false},
		{"Led the platform migration of a Go and SQL service", false},
	} {
		g := checkResume(m, docWith(m, "X1", c.text, mig))
		if got := hasCode(g, "raw-code"); got != c.raw {
			t.Errorf("%q: raw-code = %v, want %v; errors = %+v", c.text, got, c.raw, g.Errors)
		}
	}
}

func TestGate_TableLikeAndStructure(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	mig := evID(t, m, "platform migration")
	for _, c := range []struct{ name, text string }{
		{"markdown table", "| Role | Years |\n| --- | --- |"},
		{"table tags", "<table><tr><td>Role</td></tr></table>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if g := checkResume(m, docWith(m, "X1", "Led the platform migration\n"+c.text, mig)); !hasCode(g, "table") {
				t.Errorf("errors = %+v", g.Errors)
			}
		})
	}
	dup := docWith(m, "X1", "Led the platform migration", mig)
	dup.Experience = append(dup.Experience, dup.Experience[0])
	if g := checkResume(m, dup); !hasCode(g, "duplicate-entry") {
		t.Errorf("a section identifier used twice: %+v", g.Errors)
	}
	unknown := docWith(m, "X1", "Led the platform migration", mig)
	unknown.Experience = append(unknown.Experience, EntryDraft{Entry: "X99"})
	if g := checkResume(m, unknown); !hasCode(g, "unknown-entry") {
		t.Errorf("an unknown section identifier: %+v", g.Errors)
	}
	empty := ResumeDoc{Schema: docSchema, ManifestHash: m.Hash, Language: "en"}
	g := checkResume(m, empty)
	var codes []string
	for _, w := range g.Warnings {
		codes = append(codes, w.Code+":"+w.Detail)
	}
	for _, want := range []string{"no Experience or Projects section", "no Skills or Core Competencies section", "no summary at the top"} {
		if !strings.Contains(strings.Join(codes, "|"), want) {
			t.Errorf("missing structure warning %q in %v", want, codes)
		}
	}
	for _, e := range g.Errors {
		if e.Code != "missing-project" {
			t.Errorf("only the missing projects fail an empty draft: %+v", g.Errors)
		}
	}
}

func TestGate_ContentSizeBound(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	mig := evID(t, m, "platform migration")
	sized := func(n int) Gate {
		d := docWith(m, "X1", "Led the platform migration", mig)
		d.Profile = strings.Repeat("a", n)
		g := checkResume(m, d)
		return g
	}
	base := checkResume(m, docWith(m, "X1", "Led the platform migration", mig)).Chars
	if g := sized(MaxResumeChars - base); hasCode(g, "too-long") {
		t.Errorf("exactly %d characters: %+v", g.Chars, g.Errors)
	}
	if g := sized(MaxResumeChars - base + 1); !hasCode(g, "too-long") {
		t.Errorf("%d characters pass", g.Chars)
	}
	if MaxResumeChars != 14000 {
		t.Errorf("the safety bound is %d, not the specified 14,000", MaxResumeChars)
	}
}

func TestGate_SelectedAndUnselectedProjects(t *testing.T) {
	m := testManifest(t)
	if len(m.SelectedProjects) == 0 {
		t.Fatal("the fixture selects no project")
	}
	only := ResumeDoc{Schema: docSchema, ManifestHash: m.Hash, Language: "en", ResumeDraft: ResumeDraft{
		Experience: []EntryDraft{{Entry: "X1", Bullets: []Bullet{{Text: "Led the platform migration", Evidence: []string{evID(t, m, "platform migration")}}}}}}}
	if g := checkResume(m, only); !hasCode(g, "missing-project") || !strings.Contains(findingDetail(g, "missing-project"), m.SelectedProjects[0]) {
		t.Errorf("a selected project the draft drops is named: %+v", g.Errors)
	}
	none := m
	none.SelectedProjects = nil
	must(t, none.Freeze())
	extra := ResumeDoc{Schema: docSchema, ManifestHash: none.Hash, Language: "en", ResumeDraft: ResumeDraft{Projects: []EntryDraft{{Entry: m.SelectedProjects[0]}}}}
	if g := checkResume(none, extra); !hasCode(g, "unselected-project") {
		t.Errorf("an unselected project present: %+v", g.Errors)
	}
}

func findingDetail(g Gate, code string) string {
	for _, f := range g.Errors {
		if f.Code == code {
			return f.Detail
		}
	}
	return ""
}

func TestGate_EvidenceNotReflected_ParaphraseDoesNotWarn(t *testing.T) {
	m := testManifest(t)
	mig := evID(t, m, "platform migration")
	reflected := func(text string) bool {
		g := checkResume(m, docWith(m, "X1", text, mig))
		for _, w := range g.Warnings {
			if w.Code == "evidence-not-reflected" && strings.Contains(w.Detail, mig) {
				return false
			}
		}
		return true
	}
	if !reflected("Led the platform migration and cut incidents by 40% across 12 services") {
		t.Error("the evidence line itself is reflected")
	}
	if !reflected("Drove a platform migration that reduced incidents 40% across 12 services") {
		t.Error("a paraphrase must not warn")
	}
	if reflected("Mentored four engineers") {
		t.Error("a bullet that echoes none of the selected line must warn")
	}
	g := checkResume(m, docWith(m, "X1", "Mentored four engineers", mig))
	n := 0
	for _, w := range g.Warnings {
		if w.Code == "evidence-not-reflected" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d reflection warnings; the lines are listed in one", n)
	}
}

func TestGate_OneBoldMetricPerBullet(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	mig := evID(t, m, "platform migration")
	for _, c := range []struct {
		text  string
		fails bool
	}{
		{"Cut incidents by **40%** across 12 services", false},
		{"Cut incidents by **40%** across **12 services**", true},
		{"Led the **platform** migration with **Go** and cut incidents by **40%**", false},
	} {
		g := checkResume(m, docWith(m, "X1", c.text, mig))
		if got := hasCode(g, "two-bold-metrics"); got != c.fails {
			t.Errorf("%q: two-bold-metrics = %v, want %v", c.text, got, c.fails)
		}
	}
}
