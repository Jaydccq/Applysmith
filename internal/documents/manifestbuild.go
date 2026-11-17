package documents

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"applysmith/internal/kb"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// sourceSet is everything the manifest is built from, already read.
type sourceSet struct {
	Profile      workspace.Profile
	ProfileBytes []byte
	CV           string
	Posting      string
	Company      string
	Role         string
	ReportNumber int64
	Snippets     []kb.Fact
	Brief        []kb.Fact
	Retrieval    Retrieval
	Warnings     []string
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func contactFrom(p workspace.Profile) (Contact, error) {
	c := p.Candidate
	name := Normalize(c.FullName)
	if name == "" {
		name = Normalize(c.PreferredName)
	}
	email := strings.TrimSpace(c.Email)
	if name == "" || email == "" {
		return Contact{}, ErrNoContact
	}
	out := Contact{FullName: name, Email: email, Phone: Normalize(c.Phone)}
	var loc []string
	for _, part := range []string{c.Address.City, c.Address.Region} {
		if part = Normalize(part); part != "" {
			loc = append(loc, part)
		}
	}
	out.Location = strings.Join(loc, ", ")
	out.Relocation = p.Preferences.Relocation.Default == "open"
	// A link prints its address: an applicant tracking system reads the text
	// layer, where a bare "LinkedIn" label carries no URL.
	for _, u := range []string{p.Links.LinkedIn, p.Links.GitHub, p.Links.Portfolio} {
		if u = strings.TrimSpace(u); u != "" {
			out.Links = append(out.Links, Link{Label: displayURL(u), URL: u})
		}
	}
	return out, nil
}

// displayURL is a link as a reader types it: no scheme, no "www.", no
// trailing slash.
func displayURL(u string) string {
	for _, p := range []string{"https://", "http://", "www."} {
		if len(u) >= len(p) && strings.EqualFold(u[:len(p)], p) {
			u = u[len(p):]
		}
	}
	return strings.TrimRight(u, "/")
}

// builder accumulates evidence and entries with positional ids.
type builder struct {
	m      *Manifest
	seen   map[string]string
	nx, np int
	ne     int
}

func (b *builder) addEvidence(source, kind, entry, text string) string {
	text = plain(text)
	if text == "" {
		return ""
	}
	k := entry + "|" + strings.ToLower(text)
	if id, ok := b.seen[k]; ok {
		return id
	}
	id := fmt.Sprintf("E%d", len(b.m.Evidence)+1)
	b.m.Evidence = append(b.m.Evidence, Evidence{ID: id, Source: source, Kind: kind, Entry: entry, Text: text})
	b.seen[k] = id
	if entry != "" {
		for i := range b.m.Entries {
			if b.m.Entries[i].Key == entry {
				b.m.Entries[i].Evidence = append(b.m.Entries[i].Evidence, id)
			}
		}
	}
	return id
}

func (b *builder) addEntry(kind, title, org, loc, start, end string) string {
	var key string
	switch kind {
	case EntryExperience:
		b.nx++
		key = fmt.Sprintf("X%d", b.nx)
	case EntryProject:
		b.np++
		key = fmt.Sprintf("P%d", b.np)
	default:
		b.ne++
		key = fmt.Sprintf("D%d", b.ne)
	}
	b.m.Entries = append(b.m.Entries, Entry{Key: key, Kind: kind, Title: plain(title), Organization: plain(org), Location: plain(loc),
		Start: strings.TrimSpace(start), End: strings.TrimSpace(strings.ToLower(end))})
	return key
}

func (b *builder) entryFor(heading string, kind string) string {
	h := strings.ToLower(heading)
	for _, e := range b.m.Entries {
		if e.Kind == kind && e.Organization != "" && strings.Contains(h, strings.ToLower(e.Organization)) {
			return e.Key
		}
	}
	return ""
}

// buildManifest reads the sources into a frozen manifest: the evidence lines,
// the dated entries, the requirement coverage and the bounded selection of
// what the drafts may use.
func buildManifest(s sourceSet, mode Mode, pages int) (Manifest, error) {
	contact, err := contactFrom(s.Profile)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Version: ManifestVersion, Mode: mode, Limited: mode == ModeBaseline || strings.TrimSpace(s.Posting) == "",
		Company: s.Company, Role: s.Role, Contact: contact, Retrieval: s.Retrieval, Warnings: append([]string(nil), s.Warnings...),
		Inputs: Inputs{CVSHA256: sha([]byte(s.CV)), ProfileSHA256: sha(s.ProfileBytes), ReportNumber: s.ReportNumber}}
	if strings.TrimSpace(s.Posting) != "" {
		m.Inputs.PostingSHA256 = sha([]byte(s.Posting))
		m.Requirements, m.Keywords, m.NiceToHave, m.Responsibilities = successProfile(s.Posting)
		m.Recipient = recipientIn(s.Posting)
	}
	for _, r := range s.Profile.Targets.Roles {
		if t := plain(r.Title); t != "" {
			m.TargetRoles = append(m.TargetRoles, t)
		}
	}
	m.CVLinks = profileLinksIn(s.CV)
	b := &builder{m: &m, seen: map[string]string{}}
	b.addProfile(s.Profile)
	b.addCV(s.CV)
	b.addSnippets(s.Snippets)
	for i, f := range s.Brief {
		m.CompanyFacts = append(m.CompanyFacts, CompanyFact{ID: fmt.Sprintf("C%d", i+1), Text: plain(f.Text), Source: f.SourceRef})
	}
	b.selectEvidence(pages)
	if err := m.Freeze(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (b *builder) addProfile(p workspace.Profile) {
	for _, e := range p.History.Employment {
		key := b.addEntry(EntryExperience, e.Title, e.Employer, e.Location, e.Start, e.End)
		if e.Summary != "" {
			b.addEvidence("profile.yml#history.employment", "profile", key, e.Summary)
		}
	}
	for _, e := range p.History.Education {
		title := strings.TrimSpace(strings.Join(nonEmpty(e.Degree, e.Field), " in "))
		key := b.addEntry(EntryEducation, title, e.School, "", e.Start, e.End)
		if e.GradeAverage != "" {
			for i := range b.m.Entries {
				if b.m.Entries[i].Key == key {
					b.m.Entries[i].Note = plain(e.GradeAverage)
				}
			}
		}
	}
	var names []string
	for _, sk := range p.Skills {
		if n := plain(sk.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		b.addEvidence("profile.yml#skills", "profile", "", "Skills: "+strings.Join(names, ", "))
	}
}

func nonEmpty(in ...string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func (b *builder) addCV(cv string) {
	hasEmployment := b.nx > 0
	hasEducation := b.ne > 0
	for _, sec := range parseCV(cv) {
		src := "cv.md#" + sec.Heading
		lines := append(append([]string(nil), sec.Bullets...), sec.Prose...)
		switch {
		case sec.Level >= 3 && isExperienceGroup(sec.Group):
			key := b.entryFor(sec.Heading, EntryExperience)
			if key == "" && !hasEmployment {
				title, org, start, end, ok := headingEntry(sec.Heading)
				if !ok {
					title = sec.Heading
				}
				key = b.addEntry(EntryExperience, title, org, "", start, end)
			}
			for _, l := range sec.Bullets {
				b.addEvidence(src, "cv", key, l)
			}
		case sec.Level >= 3 && isProjectGroup(sec.Group):
			title := sec.Heading
			if t, _, _, _, ok := headingEntry(sec.Heading); ok {
				title = t
			}
			key := b.addEntry(EntryProject, title, "", "", "", "")
			for _, l := range lines {
				b.addEvidence(src, "cv", key, l)
			}
		case sec.Level <= 2 && isProjectGroup(sec.Group):
			// "- deploykit: a command line tool that ..." items straight under
			// a Projects heading are one project each.
			for _, l := range sec.Bullets {
				name, desc, ok := strings.Cut(plain(l), ": ")
				desc = strings.TrimSpace(urlRE.ReplaceAllString(desc, ""))
				if !ok || name == "" || len(strings.Fields(name)) > 5 || desc == "" {
					continue
				}
				r := []rune(desc)
				key := b.addEntry(EntryProject, name, "", "", "", "")
				b.addEvidence(src, "cv", key, strings.ToUpper(string(r[:1]))+string(r[1:]))
			}
		case sec.Level >= 3 && isEducationGroup(sec.Group) && !hasEducation:
			title, org, start, end, ok := headingEntry(sec.Heading)
			if !ok {
				title = sec.Heading
			}
			b.addEntry(EntryEducation, title, org, "", start, end)
		case isSkillGroup(sec.Group):
			for _, l := range lines {
				b.addEvidence(src, "cv", "", l)
			}
		case isSummaryGroup(sec.Group):
			for _, l := range lines {
				b.addEvidence(src, "cv", "", l)
			}
		}
	}
}

// ApplicantLine is one evidence line of the user's own profile and CV, labelled
// with the dated entry or the project it belongs to.
type ApplicantLine struct {
	Source string // file#heading
	// Entry is "Title, Organization" for a role, with " (current role)" when it
	// has not ended, "Project: name" for a project, empty for a summary or
	// skills line. It carries no dates: a composed answer must not work out a
	// tenure from them.
	Entry string
	Text  string
}

// ApplicantEvidence lists the lines a tailored document may claim, read the
// way a manifest reads them (employment summaries and skills of profile.yml,
// then the role bullets, projects, summary and skills lines of cv.md), with no
// posting and no selection. The free-text composer selects from it per
// question, so an answer and a letter rest on the same evidence.
func ApplicantEvidence(p workspace.Profile, cv string) []ApplicantLine {
	m := Manifest{}
	b := &builder{m: &m, seen: map[string]string{}}
	b.addProfile(p)
	b.addCV(cv)
	labels := map[string]string{}
	for _, e := range m.Entries {
		switch e.Kind {
		case EntryExperience:
			l := strings.Join(nonEmpty(e.Title, e.Organization), ", ")
			if e.Start != "" && isCurrent(e.End) {
				l += " (current role)"
			}
			labels[e.Key] = l
		case EntryProject:
			labels[e.Key] = "Project: " + e.Title
		}
	}
	out := make([]ApplicantLine, 0, len(m.Evidence))
	for _, ev := range m.Evidence {
		out = append(out, ApplicantLine{Source: ev.Source, Entry: labels[ev.Entry], Text: ev.Text})
	}
	return out
}

// ApplicantRecords are the parts of cv.md a tailored document's manifest does
// not read but a composed answer may rest on.
type ApplicantRecords struct {
	// Sections are the lines of the licenses and certifications, publications,
	// languages, awards, research and volunteering sections, a level-3 heading
	// counted as a line, each labelled with its section heading.
	Sections []ApplicantLine
	// Education are the education headings and lines, the degrees a claim may
	// name.
	Education []string
}

// ReadApplicantRecords reads the ApplicantRecords of cv. The manifest builder
// never calls it, so no tailored document changes with these sections.
func ReadApplicantRecords(cv string) ApplicantRecords {
	var out ApplicantRecords
	label := ""
	for _, sec := range parseCV(cv) {
		if sec.Level <= 2 {
			label = plain(sec.Heading)
		}
		g := sec.Group
		lines := append(append([]string(nil), sec.Bullets...), sec.Prose...)
		if sec.Level >= 3 {
			lines = append([]string{sec.Heading}, lines...)
		}
		switch {
		case isExperienceGroup(g) || isProjectGroup(g) || isSkillGroup(g) || isSummaryGroup(g):
		case isEducationGroup(g):
			for _, l := range lines {
				if t := plain(l); t != "" {
					out.Education = append(out.Education, t)
				}
			}
		case isRecordGroup(g):
			for _, l := range lines {
				if t := plain(l); t != "" {
					out.Sections = append(out.Sections, ApplicantLine{Source: "cv.md#" + sec.Heading, Entry: label, Text: t})
				}
			}
		}
	}
	return out
}

// isRecordGroup names the sections ReadApplicantRecords takes as evidence.
func isRecordGroup(g string) bool {
	for _, w := range []string{"certif", "licen", "publication", "language", "award", "honor", "honour", "research", "volunteer", "patent"} {
		if strings.Contains(g, w) {
			return true
		}
	}
	return false
}

// snippetCategory reads the category directory of a knowledge path.
func snippetCategory(ref string) string {
	rest := strings.TrimPrefix(ref, kb.KnowledgeDir+"/")
	cat, _, _ := strings.Cut(rest, "/")
	return cat
}

func (b *builder) addSnippets(facts []kb.Fact) {
	for _, f := range facts {
		if f.SourceKind != kb.KindSnippet || strings.HasPrefix(f.ID, "semantic:") {
			continue
		}
		path, heading, _ := strings.Cut(f.SourceRef, "#")
		cat := snippetCategory(path)
		if cat == kb.CatAnswerPatterns {
			continue
		}
		var lines []string
		for _, l := range strings.Split(f.Text, "\n") {
			l = strings.TrimSpace(listMarkRE.ReplaceAllString(strings.TrimSpace(l), ""))
			if l != "" && !strings.HasPrefix(l, "#") {
				lines = append(lines, l)
			}
		}
		entry := ""
		switch cat {
		case kb.CatProjects:
			title := heading
			if title == "" {
				title = strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".md")
			}
			entry = b.addEntry(EntryProject, title, "", "", "", "")
		case kb.CatExperience:
			entry = b.entryFor(f.Text+" "+heading, EntryExperience)
		}
		for _, l := range lines {
			b.addEvidence(f.SourceRef, "snippet", entry, l)
		}
	}
}

// selectEvidence picks, within the page budget, the entries, projects and
// bullets the drafts may use, ranked by the requirement terms each supports,
// and records coverage, gaps and exclusions.
func (b *builder) selectEvidence(pages int) {
	m := b.m
	bud := budgetFor(pages)
	reqTerms := map[string]bool{}
	for _, r := range m.Requirements {
		for t := range termSet(r.Text) {
			reqTerms[t] = true
		}
	}
	for _, k := range m.Keywords {
		for t := range termSet(k) {
			reqTerms[t] = true
		}
	}
	hits := map[string]int{}
	order := map[string]int{}
	for i, e := range m.Evidence {
		hits[e.ID] = overlap(termSet(e.Text), reqTerms)
		order[e.ID] = i
	}
	rank := func(ids []string) []string {
		out := append([]string(nil), ids...)
		sort.SliceStable(out, func(i, j int) bool { return hits[out[i]] > hits[out[j]] })
		return out
	}
	entryScore := func(e Entry) int {
		n := 0
		for _, id := range e.Evidence {
			n += hits[id]
		}
		return n
	}
	pick := func(kind string, limit int) (chosen []string) {
		var all []Entry
		for _, e := range m.Entries {
			if e.Kind == kind {
				all = append(all, e)
			}
		}
		ranked := append([]Entry(nil), all...)
		sort.SliceStable(ranked, func(i, j int) bool { return entryScore(ranked[i]) > entryScore(ranked[j]) })
		keep := map[string]bool{}
		for i, e := range ranked {
			if i < limit {
				keep[e.Key] = true
			} else {
				m.Excluded = append(m.Excluded, Exclusion{Key: e.Key, Reason: "ranked below the page budget by requirement relevance"})
			}
		}
		for _, e := range all {
			if keep[e.Key] {
				chosen = append(chosen, e.Key)
			}
		}
		return chosen
	}
	m.SelectedEntries = pick(EntryExperience, bud.Experience)
	m.SelectedProjects = pick(EntryProject, bud.Projects)
	for _, e := range m.Entries {
		if e.Kind == EntryEducation {
			m.SelectedEntries = append(m.SelectedEntries, e.Key)
		}
	}
	chosen := map[string]bool{}
	for _, e := range m.Entries {
		if !m.entrySelected(e.Key) {
			continue
		}
		// A line that says what a chosen line says, or less (a profile
		// summary beside the CV bullet it shortens), is not a second bullet:
		// the fuller one stays.
		var kept []string
		for _, id := range rank(e.Evidence) {
			ev, _ := m.evidence(id)
			if j := slices.IndexFunc(kept, func(k string) bool { o, _ := m.evidence(k); return sameAchievement(o.Text, ev.Text) }); j >= 0 {
				if o, _ := m.evidence(kept[j]); len(ev.Text) > len(o.Text) {
					kept[j] = id
				}
				continue
			}
			if len(kept) < bud.Bullets {
				kept = append(kept, id)
			}
		}
		for _, id := range kept {
			chosen[id] = true
		}
	}
	// Entry-less lines (summary, skills, general snippets) are all eligible but
	// bounded, so a long CV cannot flood a draft.
	general := 0
	for _, e := range m.Evidence {
		if e.Entry == "" && general < 30 {
			chosen[e.ID] = true
			general++
		}
	}
	for _, e := range m.Evidence {
		if chosen[e.ID] {
			m.Selected = append(m.Selected, e.ID)
		}
	}
	for _, r := range m.Requirements {
		rt := termSet(r.Text)
		need := min(2, len(rt))
		// A name the requirement asks for ("proficiency in SQL") is
		// covered by a line that holds it, on its own.
		named := map[string]bool{}
		for _, w := range nameTokens(r.Text) {
			if nameShaped(w) && !genericWords[strings.ToLower(w.text)] {
				for t := range termSet(w.text) {
					named[t] = rt[t]
				}
			}
		}
		cov := Coverage{Requirement: r.ID}
		for _, e := range m.Evidence {
			et := termSet(e.Text)
			if chosen[e.ID] && need > 0 && (overlap(et, rt) >= need || overlap(et, named) > 0) {
				cov.Evidence = append(cov.Evidence, e.ID)
			}
		}
		m.Coverage = append(m.Coverage, cov)
		if len(cov.Evidence) == 0 {
			m.Gaps = append(m.Gaps, r.Text)
		}
	}
}

var (
	urlRE = lazyregexp.New(`https?://[^\s)>\]"'<]+`)
	// profileHosts are the addresses a résumé's contact line carries.
	profileHosts = map[string]bool{"github.com": true, "gitlab.com": true, "bitbucket.org": true, "linkedin.com": true}
)

// linkKey reduces an address to host and path, so www., the scheme, a trailing
// slash and case do not make two spellings of one link differ.
func linkKey(raw string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), ".,;"))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") + strings.TrimRight(strings.ToLower(u.EscapedPath()), "/")
}

// profileLinksIn lists the profile links cv.md states, in order, once each.
func profileLinksIn(cv string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range urlRE.FindAllString(cv, -1) {
		k := linkKey(raw)
		host, _, _ := strings.Cut(k, "/")
		if k == "" || seen[k] || !profileHosts[host] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimRight(raw, ".,;"))
	}
	return out
}
