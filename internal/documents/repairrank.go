package documents

import (
	"fmt"
	"sort"
	"strings"
)

// Groups of removable content, as recorded with each cut.
const (
	groupCompetency     = "competency"
	groupExperience     = "experience-bullet"
	groupProject        = "project-bullet"
	groupLetterBullet   = "letter-bullet"
	groupLetterSentence = "letter-sentence"
)

// Rank is the ascending tuple removable content is ordered by: whether the
// line is the only quantified result its entry still has, distinct explicit
// requirements its evidence supports, evidence only it carries, letter
// dependency (false first) and strength. Strength decides among lines the
// requirement coverage cannot tell apart, so a nearly empty coverage no longer
// falls back to the printed tail. Age plays no part, except that the printed
// order (the manifest's, newest first) is the last tie-breaker.
type Rank struct {
	// OnlyQuantified marks a bullet that carries the only quantified result its
	// entry has left while the entry still has unquantified bullets: those go
	// first.
	OnlyQuantified bool `json:"onlyQuantified"`
	Requirements   int  `json:"requirements"`
	Unique         int  `json:"uniqueEvidence"`
	Dependent      bool `json:"letterDependent"`
	// Strength is 4 for a line that states a quantified result, plus one for
	// each of up to three posting keywords the line names; a competency line,
	// which states no result but is what a screening reads, starts at 2.
	Strength int `json:"strength"`
}

func (a Rank) less(b Rank) bool {
	switch {
	case a.OnlyQuantified != b.OnlyQuantified:
		return !a.OnlyQuantified
	case a.Requirements != b.Requirements:
		return a.Requirements < b.Requirements
	case a.Unique != b.Unique:
		return a.Unique < b.Unique
	case a.Dependent != b.Dependent:
		return !a.Dependent
	}
	return a.Strength < b.Strength
}

// quantifiedStrength and maxKeywordStrength are the parts of Rank.Strength.
const (
	quantifiedStrength = 4
	maxKeywordStrength = 3
	competencyStrength = 2
)

// quantified reports whether a line states a number (a figure, a count, a
// percentage); a magnitude word with no number ("significantly") does not count.
func quantified(text string) bool {
	for _, q := range quantities(text) {
		if q.vague == "" {
			return true
		}
	}
	return false
}

// postingKeywords are the words the posting asks for: its core keywords and
// the keywords of each requirement.
func postingKeywords(m Manifest) []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, k := range m.Keywords {
		add(k)
	}
	for _, r := range m.Requirements {
		for _, k := range r.Keywords {
			add(k)
		}
	}
	return out
}

// strengthOf scores a line independently of the rest of the document.
func strengthOf(text string, keywords []string) int {
	n := 0
	if quantified(text) {
		n = quantifiedStrength
	}
	padded := " " + strings.Join(rankWords(text), " ") + " "
	hits := 0
	for _, k := range keywords {
		if hits < maxKeywordStrength && strings.Contains(padded, " "+strings.Join(rankWords(k), " ")+" ") {
			hits++
		}
	}
	return n + hits
}

// candidate is one piece of approved content a repair may remove; pos is the
// printed order, the tie-breaker among equal ranks (the tail goes first).
type candidate struct {
	group    string
	entry    string
	index    int
	pos      int
	text     string
	evidence []string
	rank     Rank
}

func cloneResume(d ResumeDoc) ResumeDoc {
	d.ProfileEvidence = append([]string(nil), d.ProfileEvidence...)
	d.Competencies = append([]Competency(nil), d.Competencies...)
	clone := func(in []EntryDraft) []EntryDraft {
		out := make([]EntryDraft, len(in))
		for i, e := range in {
			out[i] = EntryDraft{Entry: e.Entry, Bullets: append([]Bullet(nil), e.Bullets...)}
		}
		return out
	}
	d.Experience, d.Projects = clone(d.Experience), clone(d.Projects)
	return d
}

func cloneLetter(d LetterDoc) LetterDoc {
	d.OpeningEvidence = append([]string(nil), d.OpeningEvidence...)
	d.Bullets = append([]Bullet(nil), d.Bullets...)
	d.CompanyFacts = append([]string(nil), d.CompanyFacts...)
	return d
}

// citedIDs counts how many pieces of a résumé cite each evidence id.
func citedIDs(d ResumeDoc) map[string]int {
	n := map[string]int{}
	for _, id := range d.ProfileEvidence {
		n[id]++
	}
	for _, c := range d.Competencies {
		for _, id := range sortedUnique(c.Evidence) {
			n[id]++
		}
	}
	for _, list := range [][]EntryDraft{d.Experience, d.Projects} {
		for _, e := range list {
			for _, b := range e.Bullets {
				for _, id := range sortedUnique(b.Evidence) {
					n[id]++
				}
			}
		}
	}
	return n
}

func hasID(set []string, id string) bool {
	for _, s := range set {
		if s == id {
			return true
		}
	}
	return false
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if hasID(b, x) {
			return true
		}
	}
	return false
}

// requirementsSupported counts the distinct explicit requirements whose
// coverage lists any of the evidence.
func requirementsSupported(m Manifest, evidence []string) int {
	n := 0
	for _, c := range m.Coverage {
		if overlaps(evidence, c.Evidence) {
			n++
		}
	}
	return n
}

// letterClaims lists the evidence sets of the claims a letter makes: its
// opening, then each bullet. A claim with no evidence makes no dependency.
func letterClaims(l *LetterDoc) [][]string {
	if l == nil {
		return nil
	}
	claims := [][]string{l.OpeningEvidence}
	for _, b := range l.Bullets {
		claims = append(claims, b.Evidence)
	}
	return claims
}

func letterDepends(l *LetterDoc, evidence []string) bool {
	for _, claim := range letterClaims(l) {
		if overlaps(claim, evidence) {
			return true
		}
	}
	return false
}

// resumeCandidates lists the removable lines of a résumé with their ranks. The
// last line of each group (the competencies, an entry's bullets) stays: it
// carries the label the template prints.
func resumeCandidates(m Manifest, d ResumeDoc, l *LetterDoc) []candidate {
	cited := citedIDs(d)
	keywords := postingKeywords(m)
	base := map[string]int{groupCompetency: competencyStrength}
	var out []candidate
	pos := 0
	add := func(group, entry string, index int, text string, evidence []string, last, onlyQuantified bool) {
		pos++
		if last {
			return
		}
		ev := sortedUnique(evidence)
		unique := 0
		for _, id := range ev {
			if cited[id] == 1 {
				unique++
			}
		}
		out = append(out, candidate{group: group, entry: entry, index: index, pos: pos, text: text, evidence: ev,
			rank: Rank{OnlyQuantified: onlyQuantified, Requirements: requirementsSupported(m, ev), Unique: unique, Dependent: letterDepends(l, ev),
				Strength: strengthOf(text, keywords) + base[group]}})
	}
	for i, c := range d.Competencies {
		add(groupCompetency, "", i, c.Label+": "+c.Text, c.Evidence, len(d.Competencies) == 1, false)
	}
	walk := func(group string, keys []string, list []EntryDraft) {
		for _, key := range keys {
			for _, e := range list {
				if e.Entry != key {
					continue
				}
				nq := 0
				for _, b := range e.Bullets {
					if quantified(b.Text) {
						nq++
					}
				}
				for i, b := range e.Bullets {
					// The only quantified result of an entry that also has
					// unquantified bullets is the last to go.
					only := nq == 1 && len(e.Bullets) > 1 && quantified(b.Text)
					add(group, key, i, b.Text, b.Evidence, len(e.Bullets) == 1, only)
				}
			}
		}
	}
	experience := make([]string, 0, len(m.SelectedEntries))
	for _, k := range m.SelectedEntries {
		if e, ok := m.entry(k); ok && e.Kind == EntryExperience {
			experience = append(experience, k)
		}
	}
	walk(groupExperience, experience, d.Experience)
	walk(groupProject, m.SelectedProjects, d.Projects)
	return out
}

// rankOrder sorts candidates for removal: lowest rank first, the later
// printed line first among equals.
func rankOrder(cs []candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].rank != cs[j].rank {
			return cs[i].rank.less(cs[j].rank)
		}
		return cs[i].pos > cs[j].pos
	})
}

// removeFromResume returns a copy of d without the candidate.
func removeFromResume(d ResumeDoc, c candidate) ResumeDoc {
	d = cloneResume(d)
	if c.group == groupCompetency {
		d.Competencies = append(d.Competencies[:c.index:c.index], d.Competencies[c.index+1:]...)
		return d
	}
	list := d.Experience
	if c.group == groupProject {
		list = d.Projects
	}
	for i := range list {
		if list[i].Entry == c.entry {
			b := list[i].Bullets
			list[i].Bullets = append(b[:c.index:c.index], b[c.index+1:]...)
		}
	}
	return d
}

// letterEffect says what a résumé cut does to the paired letter. A claim the
// résumé backed keeps an equivalent reference while a résumé line still cites
// one of its lines; otherwise a letter bullet goes with the cut. The opening and
// the last bullet cannot be rewritten without a model, so such a cut is refused.
type letterEffect struct {
	letter     *LetterDoc
	notes      []string
	acceptable bool
}

func effectOnLetter(before, after ResumeDoc, l *LetterDoc) letterEffect {
	if l == nil {
		return letterEffect{acceptable: true}
	}
	was, now := citedIDs(before), citedIDs(after)
	backed := func(ev []string) bool {
		for _, id := range ev {
			if was[id] > 0 {
				return true
			}
		}
		return false
	}
	remaining := func(ev []string) (string, bool) {
		for _, id := range sortedUnique(ev) {
			if now[id] > 0 {
				return id, true
			}
		}
		return "", false
	}
	out := cloneLetter(*l)
	eff := letterEffect{letter: &out, acceptable: true}
	if backed(out.OpeningEvidence) {
		id, ok := remaining(out.OpeningEvidence)
		if !ok {
			return letterEffect{}
		}
		if reduced(was, now, out.OpeningEvidence) {
			eff.notes = append(eff.notes, "the letter opening keeps its reference "+id)
		}
	}
	var kept []Bullet
	for i, b := range out.Bullets {
		if !backed(b.Evidence) {
			kept = append(kept, b)
			continue
		}
		id, ok := remaining(b.Evidence)
		switch {
		case ok:
			kept = append(kept, b)
			if reduced(was, now, b.Evidence) {
				eff.notes = append(eff.notes, fmt.Sprintf("letter bullet %d keeps its reference %s", i+1, id))
			}
		default:
			eff.notes = append(eff.notes, fmt.Sprintf("letter bullet %d rested only on removed evidence and was removed", i+1))
		}
	}
	if len(kept) == 0 && len(out.Bullets) > 0 {
		// The paragraph introduces the list and cannot be rewritten here.
		return letterEffect{}
	}
	out.Bullets = kept
	return eff
}

// reduced reports whether the cut took away a citation of any of the ids.
func reduced(was, now map[string]int, evidence []string) bool {
	for _, id := range evidence {
		if now[id] < was[id] {
			return true
		}
	}
	return false
}

// uncovered lists requirements that had résumé-cited evidence before a cut and
// have none after it.
func uncovered(m Manifest, before, after ResumeDoc) []string {
	was, now := citedIDs(before), citedIDs(after)
	var out []string
	for _, c := range m.Coverage {
		had, has := false, false
		for _, id := range c.Evidence {
			had = had || was[id] > 0
			has = has || now[id] > 0
		}
		if had && !has {
			out = append(out, c.Requirement)
		}
	}
	return out
}

// coverageOf maps each requirement to the evidence the résumé still cites.
func coverageOf(m Manifest, d ResumeDoc) []Coverage {
	cited := citedIDs(d)
	out := make([]Coverage, 0, len(m.Coverage))
	for _, c := range m.Coverage {
		row := Coverage{Requirement: c.Requirement, Evidence: []string{}}
		for _, id := range c.Evidence {
			if cited[id] > 0 {
				row.Evidence = append(row.Evidence, id)
			}
		}
		out = append(out, row)
	}
	return out
}

// letterCandidates lists a letter's removable parts: ranked bullets, then the
// sentences of the evidence paragraph from the end. The last bullet and the
// first sentence stay; the paragraph introduces the list.
func letterCandidates(m Manifest, d LetterDoc) (bullets, sentencesOut []candidate) {
	cited := map[string]int{}
	for _, id := range sortedUnique(d.OpeningEvidence) {
		cited[id]++
	}
	for _, b := range d.Bullets {
		for _, id := range sortedUnique(b.Evidence) {
			cited[id]++
		}
	}
	if len(d.Bullets) > 1 {
		keywords := postingKeywords(m)
		for i, b := range d.Bullets {
			ev := sortedUnique(b.Evidence)
			unique := 0
			for _, id := range ev {
				if cited[id] == 1 {
					unique++
				}
			}
			bullets = append(bullets, candidate{group: groupLetterBullet, index: i, pos: i + 1, text: b.Text, evidence: ev,
				rank: Rank{Requirements: requirementsSupported(m, ev), Unique: unique, Strength: strengthOf(b.Text, keywords)}})
		}
		rankOrder(bullets)
	}
	if ss := sentences(d.Body); len(ss) > 1 {
		for i := len(ss) - 1; i >= 1; i-- {
			// The cut ends the paragraph at sentence i, so it removes every
			// sentence from there on.
			sentencesOut = append(sentencesOut, candidate{group: groupLetterSentence, index: i, pos: i + 1, text: strings.Join(ss[i:], " ")})
		}
	}
	return bullets, sentencesOut
}

// removeFromLetter returns a copy of d without the candidate.
func removeFromLetter(d LetterDoc, c candidate) LetterDoc {
	d = cloneLetter(d)
	if c.group == groupLetterBullet {
		d.Bullets = append(d.Bullets[:c.index:c.index], d.Bullets[c.index+1:]...)
		return d
	}
	ss := sentences(d.Body)
	d.Body = strings.Join(ss[:c.index], " ")
	return d
}
