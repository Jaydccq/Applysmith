package documents

import (
	"strings"
	"testing"
)

func TestReplaceRejected(t *testing.T) {
	m := manifestFor(t, summaryCV, testPosting)
	mig, cloud := evID(t, m, "platform migration"), evID(t, m, "cloud spend")
	ev, _ := m.evidence(mig)
	d := docWith(m, ev.Entry, "Led the platform migration", mig)
	d.Experience[0].Bullets = []Bullet{
		{Text: "Led the platform migration and cut incidents by 90% across 12 services", Evidence: []string{mig}},
		{Text: "Led the platform migration and cut incidents by 40% across 12 services", Evidence: []string{mig}},
		{Text: "Cut costs by 90% across the board", Evidence: []string{cloud}},
		{Text: "Cut costs by 90% across the board", Evidence: []string{"E99"}},
	}
	out, notes, kept := replaceRejected(m, d)
	if kept < 1 {
		t.Fatalf("kept = %d", kept)
	}
	var texts []string
	for _, b := range out.Experience[0].Bullets {
		texts = append(texts, b.Text)
	}
	joined := strings.Join(texts, "|")
	if strings.Contains(joined, "90%") {
		t.Errorf("a rejected claim stayed: %s", joined)
	}
	if !strings.Contains(joined, ev.Text) {
		t.Errorf("the source line did not replace the bullet: %s", joined)
	}
	if got := strings.Count(strings.Join(notes, "|"), "dropped"); got < 1 {
		t.Errorf("a bullet with no usable source was not dropped and noted: %v", notes)
	}
	if strings.Count(joined, ev.Text) != 1 {
		t.Errorf("the source line repeats: %s", joined)
	}
	if g := checkResume(m, out); hasCode(g, "unsupported-figure") || hasCode(g, "unknown-evidence") || hasCode(g, "duplicated-line") {
		t.Errorf("the repaired draft fails the gate: %+v", g.Errors)
	}
}

// A rejected summary is replaced by the CV's own summary when there is one;
// with none, the note says the summary was dropped, not replaced.
func TestReplaceRejected_SummaryNoteSaysWhatHappened(t *testing.T) {
	noSummary := strings.Replace(summaryCV, "## Summary\n\nFull-stack engineer working across Go and SQL services.\n\n", "", 1)
	for _, tc := range []struct {
		cv, note string
		kept     bool
	}{{summaryCV, "was replaced by the summary of your sources", true}, {noSummary, "was dropped", false}} {
		m := manifestFor(t, tc.cv, testPosting)
		mig := evID(t, m, "platform migration")
		ev, _ := m.evidence(mig)
		d := docWith(m, ev.Entry, ev.Text, mig)
		d.Profile = "Award-winning engineer who led 40 teams to record revenue."
		out, notes, _ := replaceRejected(m, d)
		if !strings.Contains(strings.Join(notes, "|"), tc.note) || (out.Profile != "") != tc.kept {
			t.Errorf("summary %q, notes %v; want a note with %q", out.Profile, notes, tc.note)
		}
	}
}
