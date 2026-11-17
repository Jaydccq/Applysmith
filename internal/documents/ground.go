package documents

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Finding is one problem a gate found.
type Finding struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Gate is the result of the deterministic checks on structured content.
type Gate struct {
	Errors   []Finding `json:"errors"`
	Warnings []Finding `json:"warnings"`
	Lines    int       `json:"lines"`
	Chars    int       `json:"chars"`
}

// OK is true when nothing failed the draft.
func (g Gate) OK() bool { return len(g.Errors) == 0 }

func (g *Gate) fail(code, format string, args ...any) {
	g.Errors = append(g.Errors, Finding{code, fmt.Sprintf(format, args...)})
}

func (g *Gate) warn(code, format string, args ...any) {
	g.Warnings = append(g.Warnings, Finding{code, fmt.Sprintf(format, args...)})
}

// MaxResumeChars is the input and output safety bound of a résumé's content.
const MaxResumeChars = 14000

var (
	boldRE = lazyregexp.New(`\*\*([^*]+)\*\*`)

	labelRE = lazyregexp.New(`(?im)^\s*(?:executive overview|situation|task|action|result|interview stories|source references|reusable answer patterns|implementation evidence|target:.*|design direction.*)\s*:?\s*$|\b\d/5\b`)
	codeRE  = lazyregexp.New(`(?i)(?:[\w.-]+/)+[\w.-]+\.(?:go|py|ts|tsx|jsx|java|rb|rs|c|cpp|h|cs|php|sh|js)\b|\b[a-z][a-z0-9_-]*\.(?:go|py|rs|java|rb|php|sh|tsx|jsx|cpp)\b|\b(?:src|internal|pkg|cmd|node_modules)/`)
	tableRE = lazyregexp.New(`\|\s*-{3,}|<table|<tr|\t|\S {3,}\S`)

	boilerRE = lazyregexp.New(`(?i)strong interest|excited by your mission|excited about the opportunity|excited to apply|passionate about|perfect fit|dream job|innovative company|leading company`)
	ellipsis = lazyregexp.New(`\.\.\.|\x{2026}`)
	relocRE  = lazyregexp.New(`(?i)relocat|reubica|umzug|mobilit|trasferiment|mudan`)
	// authRE finds a statement of the right to work. Like relocation, it is
	// the user's to make, never the draft's: no source line a letter cites
	// says it, and the never-invent rule covers it.
	authRE = lazyregexp.New(`(?i)\b(?:eligible|authori[sz]ed|permitted|entitled|cleared) to work\b|\bwork(?:ing)? (?:authori[sz]ation|permit|visa)\b|\bright to work\b|\b(?:visa )?sponsorship\b|\bgreen card\b|\bcitizenship\b|\bpermanent resident\b`)
)

func checkBold(g *Gate, where, text string) {
	n := 0
	for _, m := range boldRE.FindAllStringSubmatch(text, -1) {
		if strings.ContainsAny(m[1], "0123456789%$") {
			n++
		}
	}
	if n > 1 {
		g.fail("two-bold-metrics", "%s bolds %d metrics; at most one is allowed", where, n)
	}
}

// checkShape applies the rules that need no evidence: internal-artifact
// labels, code and repository paths, tables.
func checkShape(g *Gate, where, text string) {
	switch {
	case labelRE.MatchString(text):
		g.fail("artifact-label", "%s holds a heading or label that belongs to an internal artifact", where)
	case holdsSourcePath(text):
		g.fail("raw-code", "%s holds a source path or repository evidence", where)
	case tableRE.MatchString(text):
		g.fail("table", "%s holds table-like content", where)
	}
}

// codeDirs are the directory names a repository path starts or passes
// through; a name before ".js" that is none of them is a product, as in
// "React/Next.js".
var codeDirs = map[string]bool{
	"src": true, "lib": true, "dist": true, "build": true, "public": true, "app": true, "assets": true, "scripts": true,
	"test": true, "tests": true, "internal": true, "pkg": true, "cmd": true, "node_modules": true, "static": true,
	"utils": true, "components": true, "pages": true, "vendor": true, "bin": true,
}

// holdsSourcePath reports whether the text names a source file or repository
// directory. A framework or product name that merely ends in ".js" is not one.
func holdsSourcePath(text string) bool {
	for _, m := range codeRE.FindAllString(text, -1) {
		if !frameworkName(m) {
			return true
		}
	}
	return false
}

func frameworkName(match string) bool {
	lower := strings.ToLower(match)
	if !strings.HasSuffix(lower, ".js") {
		return false
	}
	parts := strings.Split(lower, "/")
	for _, dir := range parts[:len(parts)-1] {
		if codeDirs[dir] {
			return false
		}
	}
	return true
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// checkDuplicates fails a long line repeated within five lines and warns on a
// repeat further apart.
func checkDuplicates(g *Gate, lines []string) {
	last := map[string]int{}
	for i, l := range lines {
		n := norm(l)
		if len([]rune(n)) < 60 {
			continue
		}
		if j, ok := last[n]; ok {
			if i-j <= 5 {
				g.fail("duplicated-line", "a line of %d characters repeats within 5 lines", len([]rune(n)))
			} else {
				g.warn("repeated-line", "a line of %d characters repeats", len([]rune(n)))
			}
		}
		last[n] = i
	}
}

func (d ResumeDoc) lines() []string {
	var out []string
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	add(d.Profile)
	for _, c := range d.Competencies {
		add(c.Label + ": " + c.Text)
	}
	for _, list := range [][]EntryDraft{d.Experience, d.Projects} {
		for _, e := range list {
			for _, b := range e.Bullets {
				add(b.Text)
			}
		}
	}
	return out
}

// checkEvidenceIDs fails a citation that is not a selected evidence line and,
// for a bullet of an entry, one that belongs to another entry.
func checkEvidenceIDs(g *Gate, m Manifest, where, entry string, ids []string) {
	for _, id := range ids {
		ev, ok := m.evidence(id)
		switch {
		case !ok:
			g.fail("unknown-evidence", "%s cites %s, which is not in the manifest", where, id)
		case !m.isSelected(id):
			g.fail("unselected-evidence", "%s cites %s, which the manifest did not select", where, id)
		case entry != "" && ev.Entry != entry && ev.Entry != "":
			g.fail("moved-evidence", "%s cites %s, which belongs to entry %s", where, id, ev.Entry)
		}
	}
}

// resumeGround holds what every part of a résumé draft is checked against, so
// the gate of the whole draft and the repair of one part use the same rules.
type resumeGround struct {
	m       Manifest
	names   names
	summary names
	user    []string
	whole   corpus
	// loose is whole with the summary's relaxed subject.
	loose corpus
	held  []string
}

func newResumeGround(m Manifest) resumeGround {
	r := resumeGround{m: m, names: m.names(), summary: m.summaryNames(), user: m.userTexts(), held: phraseKeys(m.corpus("", true))}
	r.whole = corpusQuantities(m.corpus("", true), r.user)
	r.loose = r.whole
	r.loose.summary = true
	return r
}

// entryCorpus is what a bullet of one entry may rest on.
func (r resumeGround) entryCorpus(entry string) corpus {
	return corpusQuantities(r.m.corpus(entry, false), r.user)
}

// checkSummary gates the profile text and its citations.
func (r resumeGround) checkSummary(g *Gate, text string, ids []string) {
	checkClaims(g, "profile", text, r.loose)
	checkBold(g, "profile", text)
	checkShape(g, "profile", text)
	checkNames(g, "profile", text, r.summary)
	checkEvidenceIDs(g, r.m, "profile", "", ids)
	if relocRE.MatchString(text) && !r.m.Contact.Relocation {
		g.fail("header-grounding", "the profile text claims relocation that the profile does not state")
	}
}

// checkCompetency gates one skills line.
func (r resumeGround) checkCompetency(g *Gate, where string, c Competency) {
	checkEvidenceIDs(g, r.m, where, "", c.Evidence)
	checkShape(g, where, c.Label+": "+c.Text)
	checkClaims(g, where, c.Text, r.whole)
	for _, item := range splitItems(c.Text) {
		if !hasPhrase(r.held, item) {
			g.fail("unsupported-technology", "%s lists %q, which no selected evidence mentions", where, item)
		}
	}
}

// checkBullet gates one bullet of an entry against that entry's corpus.
func (r resumeGround) checkBullet(g *Gate, where, entry string, b Bullet, c corpus) {
	checkEvidenceIDs(g, r.m, where, entry, b.Evidence)
	checkClaims(g, where, b.Text, c)
	checkBold(g, where, b.Text)
	checkShape(g, where, b.Text)
	checkNames(g, where, b.Text, r.names)
}

// checkResume gates a résumé draft against the frozen manifest.
func checkResume(m Manifest, d ResumeDoc) Gate {
	var g Gate
	if err := m.Verify(); err != nil {
		g.fail("manifest-changed", "%v", err)
		return g
	}
	if d.ManifestHash != m.Hash {
		g.fail("manifest-mismatch", "the draft was written for another manifest")
	}
	r := newResumeGround(m)
	r.checkSummary(&g, d.Profile, d.ProfileEvidence)
	for i, c := range d.Competencies {
		r.checkCompetency(&g, fmt.Sprintf("competency %d", i+1), c)
	}
	seen := map[string]bool{}
	for _, group := range []struct {
		name string
		list []EntryDraft
		proj bool
	}{{"experience", d.Experience, false}, {"project", d.Projects, true}} {
		for _, e := range group.list {
			en, ok := m.entry(e.Entry)
			switch {
			case !ok || en.Kind == EntryEducation || (en.Kind == EntryProject) != group.proj:
				g.fail("unknown-entry", "%s entry %q is not in the manifest", group.name, e.Entry)
				continue
			case !m.entrySelected(e.Entry):
				if group.proj {
					g.fail("unselected-project", "unselected projects included: %s", e.Entry)
				} else {
					g.fail("unselected-entry", "entry %s was not selected", e.Entry)
				}
				continue
			case seen[e.Entry]:
				g.fail("duplicate-entry", "entry %s appears twice", e.Entry)
				continue
			}
			seen[e.Entry] = true
			corpus := r.entryCorpus(e.Entry)
			for i, b := range e.Bullets {
				r.checkBullet(&g, fmt.Sprintf("%s %s bullet %d", group.name, e.Entry, i+1), e.Entry, b, corpus)
			}
		}
	}
	for _, k := range m.SelectedProjects {
		if !seen[k] {
			g.fail("missing-project", "selected project %s is absent without a recorded compaction decision", k)
		}
	}
	lines := d.lines()
	checkDuplicates(&g, lines)
	for _, l := range lines {
		g.Chars += len([]rune(l))
	}
	g.Lines = len(lines)
	if g.Chars > MaxResumeChars {
		g.fail("too-long", "%d characters exceed the %d character bound", g.Chars, MaxResumeChars)
	}
	if len(d.Experience) == 0 && len(d.Projects) == 0 {
		g.warn("structure", "no Experience or Projects section")
	}
	if len(d.Competencies) == 0 {
		g.warn("structure", "no Skills or Core Competencies section")
	}
	if strings.TrimSpace(d.Profile) == "" {
		g.warn("structure", "no summary at the top")
	}
	g.keywordWarning(m, strings.ToLower(strings.Join(lines, "\n")))
	g.reflectionWarning(m, lines)
	g.linkWarning(m)
	return g
}

// linkWarning notes a profile link cv.md states that the contact block, which
// is rendered from profile.yml, does not carry.
func (g *Gate) linkWarning(m Manifest) {
	carried := map[string]bool{}
	for _, l := range m.Contact.Links {
		carried[linkKey(l.URL)] = true
	}
	for _, raw := range m.CVLinks {
		if !carried[linkKey(raw)] {
			g.warn("dropped-link", "cv.md links to %s, which the contact line does not carry; add it under links in profile.yml", raw)
		}
	}
}

func (g *Gate) keywordWarning(m Manifest, lower string) {
	var missing []string
	for _, k := range m.Keywords {
		if !strings.Contains(lower, strings.ToLower(k)) && len(missing) < 5 {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		g.warn("keywords", "core keywords not covered: %s", strings.Join(missing, ", "))
	}
}

var itemSepRE = lazyregexp.New(`[,;/]|\band\b|&`)

func splitItems(s string) []string {
	var out []string
	for _, p := range itemSepRE.Split(s, -1) {
		if p = strings.TrimSpace(strings.Trim(p, ". ")); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// sentences splits letter prose at terminal punctuation.
func sentences(s string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range s {
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			if t := strings.TrimSpace(cur.String()); t != "" {
				out = append(out, t)
			}
			cur.Reset()
		}
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

func terminal(s string) bool {
	s = strings.TrimRight(strings.TrimSpace(s), `"')]`)
	return strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?")
}

// words counts the words of the body as printed.
func (m Manifest) words(d LetterDoc) int {
	n := 0
	for _, p := range m.printedParagraphs(d) {
		n += len(strings.Fields(p))
	}
	for _, b := range d.Bullets {
		n += len(strings.Fields(b.Text))
	}
	return n
}

// checkLetter gates a letter draft: the shape rules of a cover letter and the
// grounding rules it shares with the résumé. Company facts appear only as
// slots; everything else is the applicant's text.
func checkLetter(m Manifest, d LetterDoc) Gate {
	var g Gate
	if err := m.Verify(); err != nil {
		g.fail("manifest-changed", "%v", err)
		return g
	}
	if d.ManifestHash != m.Hash {
		g.fail("manifest-mismatch", "the letter was written for another manifest")
	}
	quoted := m.checkCompanyFacts(&g, "letter", d.CompanyFacts, d.own())
	printed := m.printedParagraphs(d)
	all := strings.Join(printed, "\n")
	for _, b := range d.Bullets {
		all += "\n" + b.Text
	}
	g.Chars, g.Lines = len([]rune(all)), len(printed)+len(d.Bullets)
	if g.Chars < 120 {
		g.fail("too-short", "the letter has %d characters; the minimum is 120", g.Chars)
	}
	checkShape(&g, "letter", all)
	if ellipsis.MatchString(all) {
		g.fail("cut-off", "the letter holds a truncation ellipsis")
	}
	for i, p := range printed {
		// A body that ends in a colon introduces the bullets printed under it.
		if !terminal(p) && !(i == 1 && p == d.Body && len(d.Bullets) > 0 && strings.HasSuffix(strings.TrimSpace(p), ":")) {
			g.fail("cut-off", "paragraph %d does not end in terminal punctuation", i+1)
		}
	}
	// A fact the system quotes is the company's sentence, not a claim of the
	// applicant: the checks that read the applicant's words skip it.
	own := strings.Join(d.own(), "\n")
	if relocRE.MatchString(own) {
		g.fail("relocation-claim", "the system, not the draft, states relocation")
	}
	if authRE.MatchString(own) {
		g.fail("work-authorization-claim", "the letter states the right to work, which only the user may state")
	}
	m.checkLetterOwn(&g, "letter", own, corpusQuantities(m.corpus("", true), m.userTexts()), m.names())
	if len(quoted) > 0 {
		checkBeforeFacts(&g, d.evidenceParts())
		m.checkAfterFacts(&g, "closing", d.Closing, quoted)
	}
	checkEvidenceIDs(&g, m, "opening", "", d.OpeningEvidence)
	for i, b := range d.Bullets {
		checkEvidenceIDs(&g, m, fmt.Sprintf("bullet %d", i+1), "", b.Evidence)
		if b.Text != "" && !terminal(b.Text) {
			g.warn("bullet-punctuation", "bullet %d has no terminal punctuation", i+1)
		}
	}
	if boilerRE.MatchString(own) && len(quoted) == 0 && !companySpecific(m, own) {
		g.fail("generic", "the letter uses stock enthusiasm with nothing specific to the company or role")
	}
	for _, s := range sentences(own) {
		phrases := stackedPhrases(m, s)
		if phrases >= 4 || (phrases >= 3 && strings.Count(s, ",") >= 2) {
			g.fail("stuffed-sentence", "one sentence stacks %d company-research phrases", phrases)
		}
	}
	if d.Language == "en" {
		if w := m.words(d); w < letterMinWords || w > letterMaxWords {
			g.warn("length", "the body has %d words; the English target is %s and thin evidence is not padded", w, letterWordsText)
		}
	}
	return g
}

// companySpecific is true when the letter names the company or shares a
// three-word phrase with the posting requirements.
func companySpecific(m Manifest, letter string) bool {
	if m.Company != "" && lowerContains(letter, m.Company) {
		return true
	}
	lw := words3(letter)
	for _, src := range requirementTexts(m) {
		for p := range words3(src) {
			if lw[p] {
				return true
			}
		}
	}
	return false
}

func requirementTexts(m Manifest) []string {
	var out []string
	for _, r := range m.Requirements {
		out = append(out, r.Text)
	}
	return out
}

func words3(s string) map[string]bool {
	var toks []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 4 {
			toks = append(toks, w)
		}
	}
	out := map[string]bool{}
	for i := 0; i+2 < len(toks); i++ {
		out[toks[i]+" "+toks[i+1]+" "+toks[i+2]] = true
	}
	return out
}

// stackedPhrases counts the company facts a sentence echoes.
func stackedPhrases(m Manifest, sentence string) int {
	sw := words3(sentence)
	n := 0
	for _, f := range m.CompanyFacts {
		for p := range words3(f.Text) {
			if sw[p] {
				n++
				break
			}
		}
	}
	return n
}
