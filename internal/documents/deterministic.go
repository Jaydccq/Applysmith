package documents

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// shapeOK reports whether a source line may appear in a document at all.
func shapeOK(text string) bool {
	var g Gate
	checkShape(&g, "line", text)
	checkBold(&g, "line", text)
	return g.OK()
}

func isSummarySource(src string) bool {
	_, h, _ := strings.Cut(strings.ToLower(src), "#")
	return isSummaryGroup(h)
}

func isSkillSource(e Evidence) bool {
	_, h, _ := strings.Cut(strings.ToLower(e.Source), "#")
	return isSkillGroup(h) || strings.HasPrefix(e.Text, "Skills:")
}

// deterministicResume selects and formats approved source text without a
// model: summary lines, skill lines and the selected bullets, verbatim.
func deterministicResume(m Manifest, lang string) ResumeDoc {
	l, _ := lookupLanguage(lang)
	d := ResumeDoc{Schema: docSchema, Generation: GenDeterministic, ManifestHash: m.Hash, Language: lang, Limited: m.Limited}
	var summary []string
	// A skill line whose every item another kept skill line lists ("Skills:
	// Go, SQL" from the profile beside "Languages: Go, SQL, Python") repeats it.
	var lines []Evidence
	for _, e := range m.Evidence {
		if e.Entry == "" && m.isSelected(e.ID) && shapeOK(e.Text) && isSkillSource(e) {
			lines = append(lines, e)
		}
	}
	repeat := map[string]bool{}
	for _, e := range lines {
		rest := map[string]string{}
		for _, o := range lines {
			if o.ID != e.ID && !repeat[o.ID] {
				maps.Copy(rest, skillItems(o.Text))
			}
		}
		items := skillItems(e.Text)
		repeat[e.ID] = len(items) > 0
		for k := range items {
			repeat[e.ID] = repeat[e.ID] && rest[k] != ""
		}
	}
	for _, e := range m.Evidence {
		if e.Entry != "" || !m.isSelected(e.ID) || !shapeOK(e.Text) || repeat[e.ID] {
			continue
		}
		switch {
		case isSummarySource(e.Source) && len(d.ProfileEvidence) < 2:
			summary = append(summary, e.Text)
			d.ProfileEvidence = append(d.ProfileEvidence, e.ID)
		case isSkillSource(e) && len(d.Competencies) < 5:
			label, text, ok := strings.Cut(e.Text, ":")
			if !ok || len(strings.Fields(label)) > 4 {
				label, text = l.skills, e.Text
			}
			d.Competencies = append(d.Competencies, Competency{Label: strings.TrimSpace(label), Text: strings.TrimSpace(text), Evidence: []string{e.ID}})
		}
	}
	d.Profile = strings.Join(summary, " ")
	for _, k := range m.SelectedEntries {
		if e, ok := m.entry(k); ok && e.Kind == EntryExperience {
			d.Experience = append(d.Experience, entryBullets(m, e))
		}
	}
	for _, k := range m.SelectedProjects {
		if e, ok := m.entry(k); ok {
			d.Projects = append(d.Projects, entryBullets(m, e))
		}
	}
	return d
}

// skillItems are the items of a skill line after its label, by folded key.
func skillItems(text string) map[string]string {
	if _, list, ok := strings.Cut(text, ":"); ok {
		text = list
	}
	out := map[string]string{}
	for _, item := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' }) {
		if k := strings.Join(keysOf(nameTokens(item)), " "); k != "" {
			out[k] = strings.TrimSpace(item)
		}
	}
	return out
}

func entryBullets(m Manifest, e Entry) EntryDraft {
	out := EntryDraft{Entry: e.Key}
	for _, id := range e.Evidence {
		if ev, ok := m.evidence(id); ok && m.isSelected(id) && shapeOK(ev.Text) {
			out.Bullets = append(out.Bullets, Bullet{Text: ev.Text, Evidence: []string{id}})
		}
	}
	return out
}

// wordTerms are the terms of a posting line without its number fragments: the
// "000" of "$400,000" says nothing a "3,000 robots" line shares.
func wordTerms(s string) map[string]bool {
	out := termSet(s)
	for t := range out {
		if strings.Trim(t, "0123456789") == "" {
			delete(out, t)
		}
	}
	return out
}

// deterministicLetter writes a letter with the structure of a model draft from
// the posting terms and the strongest selected evidence: the lines that share
// the most terms with the requirements, then with the responsibilities; on a
// tie a CV line (an achievement) goes before a profile summary (a role), then
// source order decides. It is English only:
// phrasing in another language needs a model. Its bullets come from the
// evidence the final résumé cites. ok is false when the manifest
// has nothing to write a company-specific letter from.
func deterministicLetter(m Manifest, r ResumeDoc, lang string) (LetterDoc, bool) {
	if lang != "en" || m.Mode != ModeTailored || (m.Company == "" && m.Role == "") {
		return LetterDoc{}, false
	}
	d := LetterDoc{Schema: docSchema, Generation: GenDeterministic, ManifestHash: m.Hash, Language: lang}
	role := m.roleTitle()
	if role == "" {
		role = "open"
	}
	if m.Company != "" {
		d.Opening = fmt.Sprintf("I am writing to apply for the %s role at %s.", role, m.Company)
	} else {
		d.Opening = fmt.Sprintf("I am writing to apply for the %s role.", role)
	}
	if kw := letterKeywords(m); len(kw) > 0 {
		d.Opening += " Your posting emphasizes " + joinList(kw) + "."
	}
	type scored struct {
		id    string
		score int
	}
	var pool []scored
	req, duty := map[string]bool{}, wordTerms(strings.Join(m.Responsibilities, "\n"))
	for _, q := range m.Requirements {
		for t := range wordTerms(q.Text) {
			req[t] = true
		}
	}
	allowed := map[string]bool{}
	for _, id := range letterEvidence(m, r) {
		allowed[id] = true
	}
	for pass := 0; pass < 2 && len(pool) == 0; pass++ {
		for i, e := range m.Evidence {
			if e.Entry != "" && (allowed[e.ID] || pass == 1) && m.isSelected(e.ID) && shapeOK(e.Text) {
				terms, achievement := termSet(e.Text), 0
				if e.Kind != "profile" {
					achievement = 1
				}
				pool = append(pool, scored{e.ID, ((overlap(terms, req)*1000+overlap(terms, duty))*2+achievement)*1000 - i})
			}
		}
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].score > pool[j].score })
	for _, p := range pool {
		if len(d.Bullets) == 3 {
			break
		}
		e, _ := m.evidence(p.id)
		t := strings.TrimSpace(e.Text)
		if !terminal(t) {
			t += "."
		}
		// A line that says what another chosen line says, or less, is the same
		// achievement: the fuller one stays.
		b := Bullet{Text: t, Evidence: []string{e.ID}}
		same := slices.IndexFunc(d.Bullets, func(x Bullet) bool { return sameAchievement(x.Text, t) })
		switch {
		case same < 0:
			d.Bullets = append(d.Bullets, b)
		case len(t) > len(d.Bullets[same].Text):
			d.Bullets[same] = b
		}
	}
	if len(d.Bullets) > 0 {
		d.Body = "Examples of the work most relevant to this role are below."
		if len(d.Bullets) < 3 {
			d.Body = "The work most relevant to this role is below."
		}
	}
	target := m.Company
	if target == "" {
		target = "your team"
	}
	d.Closing = fmt.Sprintf("I would welcome the opportunity to discuss how my experience can support %s. Thank you for your time and consideration.", target)
	// The first eligible company fact is quoted in the company paragraph. If
	// the gate then refuses the text around it (a bullet points ahead, the
	// closing points back), the letter goes without it.
	if facts := m.slotFacts(); len(facts) > 0 {
		d.CompanyFacts = []string{facts[0].ID}
		for _, f := range checkLetter(m, d).Errors {
			if f.Code == "slot-reference" || f.Code == "forward-reference" {
				d.CompanyFacts = nil
				break
			}
		}
	}
	return d, true
}

// letterKeywords are the tools, languages and listed skills the posting asks
// for that the candidate's selected evidence also names, so the opening names
// only what the letter can back up: the requirements in the posting's order,
// then the nice-to-haves, then the core keywords. A name is a word the posting
// writes as one (Go, AWS, HubSpot, Next.js, a run such as Google Analytics) or
// a technology on the floor list; a plain word counts only as an item of the
// candidate's own skill lines ("message queues"). A name the posting writes in
// lower case is printed as the candidate spells it, and one the candidate
// capitalizes but the posting does not ("in go") is not taken for it. Nothing
// found leaves the opening without the sentence rather than with common words.
func letterKeywords(m Manifest) []string {
	skip := termSet(m.Role + " " + m.Company)
	var have [][]string
	cased := map[string]string{}
	var skills [][]nameTok
	for _, e := range m.Evidence {
		if !m.isSelected(e.ID) {
			continue
		}
		toks := nameTokens(e.Text)
		have = append(have, keysOf(toks))
		for _, w := range toks {
			if w.text != strings.ToLower(w.text) {
				cased[key(w.text)] = w.text
			}
		}
		if isSkillSource(e) {
			for _, item := range skillItems(e.Text) {
				if it := nameTokens(item); len(it) <= 4 {
					skills = append(skills, it)
				}
			}
		}
	}
	plainWord := func(w nameTok) bool {
		low := strings.ToLower(w.text)
		return isStop(low) || genericWords[low] || seniority[low] || roleNouns[low] || overlap(termSet(low), skip) > 0
	}
	isName := func(w nameTok) bool {
		return !plainWord(w) && (nameShaped(w) && !openingCapital(w) || builtinTech[strings.ToLower(w.text)] || builtinTech[key(w.text)])
	}
	var lines, out, picked []string
	for _, r := range m.Requirements {
		lines = append(lines, r.Text)
	}
	for _, line := range append(append(lines, m.NiceToHave...), m.Keywords...) {
		toks := nameTokens(line)
		ks := keysOf(toks)
		found := map[int][]nameTok{}
		for i := 0; i < len(toks); {
			j := i + 1
			for isName(toks[i]) && j < len(toks) && isName(toks[j]) && strings.Contains(line, toks[j-1].text+" "+toks[j].text) {
				j++
			}
			if isName(toks[i]) && slices.ContainsFunc(have, func(h []string) bool { return seqIndex(h, ks[i:j]) >= 0 }) {
				found[i] = toks[i:j]
			}
			i = j
		}
		for _, it := range skills {
			if p := seqIndex(ks, keysOf(it)); p >= 0 && len(it) > len(found[p]) && slices.ContainsFunc(it, func(w nameTok) bool { return !plainWord(w) }) {
				found[p] = it
			}
		}
		for _, p := range slices.Sorted(maps.Keys(found)) {
			it, k := found[p], strings.Join(keysOf(found[p]), " ")
			span := joinToks(toks[p : p+len(it)])
			text := span
			switch c, ok := cased[k]; {
			case span != strings.ToLower(span):
			case builtinTech[k] && ok:
				text = c
			case joinToks(it) != span:
				// The candidate capitalizes it and the posting does not:
				// "in go" is the verb, not the language.
				continue
			}
			if len(out) < 3 && !slices.ContainsFunc(picked, func(o string) bool {
				return strings.Contains(" "+o+" ", " "+k+" ") || strings.Contains(" "+k+" ", " "+o+" ")
			}) {
				out, picked = append(out, text), append(picked, k)
			}
		}
	}
	return out
}

func keysOf(toks []nameTok) []string {
	out := make([]string, len(toks))
	for i, w := range toks {
		out[i] = key(w.text)
	}
	return out
}

func joinToks(toks []nameTok) string {
	parts := make([]string, len(toks))
	for i, w := range toks {
		parts[i] = w.text
	}
	return strings.Join(parts, " ")
}

// seqIndex is where needle starts as a run of hay, or -1.
func seqIndex(hay, needle []string) int {
	for i := 0; len(needle) > 0 && i+len(needle) <= len(hay); i++ {
		if slices.Equal(hay[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

// sameAchievement is true when one line says what the other says, or less:
// the shorter line's terms are all the longer one's, or all but one of six or
// more ("for a fleet of warehouse robots" beside "for 3,000 warehouse robots
// in Python"). Two lines of one length that differ in a word ("checkout"
// against "search") are two achievements.
func sameAchievement(a, b string) bool {
	ta, tb := termSet(a), termSet(b)
	if len(ta) > len(tb) {
		ta, tb = tb, ta
	}
	n := overlap(ta, tb)
	return len(ta) > 0 && (n == len(ta) || len(ta) >= 6 && n == len(ta)-1 && len(tb) > len(ta))
}

func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
