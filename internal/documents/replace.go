package documents

import (
	"fmt"
	"strings"
)

// replaceRejected keeps every part of a model draft that passes the gate and
// swaps each part that fails for text the user's own sources already hold: a
// bullet becomes the evidence line it cites, a summary the CV's own summary, a
// skills line is dropped. The gate itself is not relaxed: the result is gated
// again as a whole by the caller. notes name each swap, and kept counts the
// parts of the model's draft that survived unchanged.
func replaceRejected(m Manifest, d ResumeDoc) (out ResumeDoc, notes []string, kept int) {
	r := newResumeGround(m)
	out = d
	det := deterministicResume(m, d.Language)

	if strings.TrimSpace(d.Profile) != "" {
		var g Gate
		r.checkSummary(&g, d.Profile, d.ProfileEvidence)
		if g.OK() {
			kept++
		} else {
			out.Profile, out.ProfileEvidence = det.Profile, det.ProfileEvidence
			var dg Gate
			if r.checkSummary(&dg, out.Profile, out.ProfileEvidence); !dg.OK() {
				out.Profile, out.ProfileEvidence = "", nil
			}
			notes = append(notes, fmt.Sprintf("the model summary was replaced by the summary of your sources (%s)", codes(g)))
		}
	}

	out.Competencies = nil
	for i, c := range d.Competencies {
		var g Gate
		r.checkCompetency(&g, "competency", c)
		if g.OK() {
			out.Competencies = append(out.Competencies, c)
			kept++
			continue
		}
		notes = append(notes, fmt.Sprintf("model skills line %d (%s) was dropped (%s)", i+1, c.Label, codes(g)))
	}
	if len(out.Competencies) == 0 && len(d.Competencies) > 0 {
		for _, c := range det.Competencies {
			var g Gate
			if r.checkCompetency(&g, "competency", c); g.OK() {
				out.Competencies = append(out.Competencies, c)
			}
		}
	}

	var n int
	out.Experience, n = replaceEntries(m, r, d.Experience, false, &notes)
	kept += n
	out.Projects, n = replaceEntries(m, r, d.Projects, true, &notes)
	kept += n
	present := map[string]bool{}
	for _, e := range out.Projects {
		present[e.Entry] = true
	}
	for _, k := range m.SelectedProjects {
		if en, ok := m.entry(k); ok && !present[k] {
			out.Projects = append(out.Projects, entryBullets(m, en))
			notes = append(notes, fmt.Sprintf("the model draft left out the selected project %s; its source lines were used", k))
		}
	}
	return out, notes, kept
}

func replaceEntries(m Manifest, r resumeGround, in []EntryDraft, proj bool, notes *[]string) (out []EntryDraft, kept int) {
	for _, e := range in {
		en, ok := m.entry(e.Entry)
		if !ok || en.Kind == EntryEducation || (en.Kind == EntryProject) != proj || !m.entrySelected(e.Entry) {
			// A structural fault is left for the whole-draft gate to refuse.
			out = append(out, e)
			continue
		}
		c := r.entryCorpus(e.Entry)
		fixed := EntryDraft{Entry: e.Entry}
		have := map[string]bool{}
		for i, b := range e.Bullets {
			var g Gate
			r.checkBullet(&g, "bullet", e.Entry, b, c)
			if g.OK() {
				fixed.Bullets = append(fixed.Bullets, b)
				have[norm(b.Text)] = true
				kept++
				continue
			}
			if src, ok := sourceBullet(r, e.Entry, b, c, have); ok {
				fixed.Bullets = append(fixed.Bullets, src)
				have[norm(src.Text)] = true
				*notes = append(*notes, fmt.Sprintf("model bullet %d of %s was replaced by its source line %s (%s)", i+1, e.Entry, src.Evidence[0], codes(g)))
				continue
			}
			*notes = append(*notes, fmt.Sprintf("model bullet %d of %s was dropped, its source line is unusable (%s)", i+1, e.Entry, codes(g)))
		}
		fixed.Bullets = withoutRepeats(fixed.Bullets)
		out = append(out, fixed)
	}
	return out, kept
}

// sourceBullet is the first line the bullet cites that may stand in for it:
// selected, of the same entry, clean in shape and not already in the entry.
func sourceBullet(r resumeGround, entry string, b Bullet, c corpus, have map[string]bool) (Bullet, bool) {
	for _, id := range b.Evidence {
		ev, ok := r.m.evidence(id)
		if !ok || !r.m.isSelected(id) || (ev.Entry != entry && ev.Entry != "") || have[norm(ev.Text)] {
			continue
		}
		cand := Bullet{Text: ev.Text, Evidence: []string{id}}
		var g Gate
		if r.checkBullet(&g, "bullet", entry, cand, c); g.OK() {
			return cand, true
		}
	}
	return Bullet{}, false
}

// withoutRepeats drops a bullet that repeats an earlier one, which a swap for a
// source line can produce.
func withoutRepeats(in []Bullet) []Bullet {
	seen := map[string]bool{}
	var out []Bullet
	for _, b := range in {
		if k := norm(b.Text); !seen[k] {
			seen[k] = true
			out = append(out, b)
		}
	}
	return out
}
