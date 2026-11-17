package documents

import (
	"fmt"
	"strings"
)

// Company facts reach a letter in one fixed place: a company paragraph the
// system builds between the evidence paragraph and the closing. The model
// only chooses fact ids; the paragraph is a fixed lead-in followed by each
// fact's text word for word. Every word the model writes is the applicant's
// and rests on the applicant's sources alone, so no fact can become something
// the applicant did, and no check has to decide which model-written clauses
// speak for the company. The opening and the evidence paragraph come before
// any fact; only the closing follows one.
const maxCompanyFacts = 2

// slotEligible says whether a fact may stand in the letter: a third-person
// statement about the company. A fact in the first or second person would
// read as the applicant's own voice, and one that describes the person the
// company wants ("the ideal candidate has scaled ...") would read as a
// description of the applicant.
func slotEligible(text string) bool {
	return !firstPersonRE.MatchString(text) && !youRE.MatchString(text) && !candidateRE.MatchString(text)
}

// slotText is the fact as the letter prints it: one line with terminal
// punctuation.
func slotText(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if !terminal(t) {
		t += "."
	}
	return t
}

// slotFacts are the facts a letter may quote.
func (m Manifest) slotFacts() []CompanyFact {
	var out []CompanyFact
	for _, c := range m.CompanyFacts {
		if slotEligible(c.Text) {
			out = append(out, c)
		}
	}
	return out
}

func (m Manifest) companyFact(id string) (CompanyFact, bool) {
	for _, c := range m.CompanyFacts {
		if c.ID == id {
			return c, true
		}
	}
	return CompanyFact{}, false
}

// companyParagraph is the paragraph the system prints for the facts the letter
// chose, or "" when it chose none.
func (m Manifest) companyParagraph(d LetterDoc) string {
	var parts []string
	for _, id := range d.CompanyFacts {
		if c, ok := m.companyFact(id); ok && slotEligible(c.Text) {
			parts = append(parts, slotText(c.Text))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	l, ok := lookupLanguage(d.Language)
	if !ok {
		l = languages["en"]
	}
	who := l.thisTeam
	if m.Company != "" {
		who = m.Company
	}
	return fmt.Sprintf(l.leadIn, who) + " " + strings.Join(parts, " ")
}

// printedParagraphs are the paragraphs of the letter as printed, in order:
// opening, body, company, closing. The bullets belong to the body.
func (m Manifest) printedParagraphs(d LetterDoc) []string {
	out := []string{d.Opening}
	if strings.TrimSpace(d.Body) != "" {
		out = append(out, d.Body)
	}
	if c := m.companyParagraph(d); c != "" {
		out = append(out, c)
	}
	return append(out, d.Closing)
}

// letterOwn is the model's own text in reading order.
func (d LetterDoc) own() []string {
	out := []string{d.Opening, d.Body}
	for _, b := range d.Bullets {
		out = append(out, b.Text)
	}
	return append(out, d.Closing)
}

// checkCompanyFacts reports an unknown, ineligible, repeated or surplus fact id
// and a marker typed into the model's text, and returns the facts quoted.
func (m Manifest) checkCompanyFacts(g *Gate, where string, ids []string, texts []string) []CompanyFact {
	for _, t := range texts {
		if strings.Contains(t, "[[") || strings.Contains(t, "]]") {
			g.fail("bad-slot", "%s: a company fact is chosen by its id in companyFacts and printed by the system; it is never typed into the text", where)
			break
		}
	}
	var out []CompanyFact
	seen := map[string]bool{}
	for _, id := range ids {
		c, ok := m.companyFact(id)
		switch {
		case !ok:
			g.fail("unknown-company-fact", "%s is not a company fact of the manifest", id)
		case !slotEligible(c.Text):
			g.fail("ineligible-company-fact", "%s: %s speaks in the first or second person or describes a candidate and cannot be quoted", where, id)
		case seen[id]:
			g.fail("bad-slot", "%s: %s is chosen twice", where, id)
		case len(out) >= maxCompanyFacts:
			g.fail("bad-slot", "%s: a letter quotes at most %d company facts", where, maxCompanyFacts)
		default:
			seen[id] = true
			out = append(out, c)
		}
	}
	return out
}

// checkAfterFacts gates the text that follows the company paragraph. A
// sentence of the applicant's own (first person, no stated subject) there may
// not point back at a fact with a closed-class pointer or reuse one of its
// words.
func (m Manifest) checkAfterFacts(g *Gate, where, text string, facts []CompanyFact) {
	ref := m.referenceCheck()
	for _, c := range facts {
		ref.addFact(c.Text)
	}
	for _, sent := range splitSentences(text) {
		if ref.refersBack(sent) {
			g.fail("slot-reference", "%s: %q points back at a quoted company fact; say it in full, in other words, without a pronoun or \"so did\"", where, shorten(sent))
		}
	}
}

// aheadPart is a stretch of the evidence paragraph the forward-pointer check
// reads. A bullet has the applicant for its implied subject.
type aheadPart struct {
	where, text string
	bullet      bool
}

// evidenceParts are the opening, the body and the bullets, in reading order.
func (d LetterDoc) evidenceParts() []aheadPart {
	out := []aheadPart{{"opening", d.Opening, false}, {"body", d.Body, false}}
	for i, b := range d.Bullets {
		out = append(out, aheadPart{fmt.Sprintf("bullet %d", i+1), b.Text, true})
	}
	return out
}

// checkBeforeFacts gates every sentence before the company paragraph: a
// forward pointer ("the database described next", "as follows", "below") in a
// sentence of the applicant's own (first person, self-reference or a bullet)
// would let the applicant's claim ride on the facts that come later. A
// sentence with no one in it ("Examples are below.") claims nothing. A last
// sentence that ends in a colon introduces the facts.
func checkBeforeFacts(g *Gate, parts []aheadPart) {
	last := -1
	for i, p := range parts {
		if strings.TrimSpace(p.text) != "" {
			last = i
		}
	}
	for i, p := range parts {
		ss := splitSentences(p.text)
		for k, sent := range ss {
			own := p.bullet || firstPersonRE.MatchString(sent) || selfRefRE.MatchString(sent)
			colon := i == last && k == len(ss)-1 && strings.HasSuffix(strings.TrimSpace(sent), ":")
			if own && (pointsAhead(sent) || colon) {
				g.fail("forward-reference", "%s: %q points ahead at what follows; do not use \"below\", \"the following\", \"as follows\", \"next\", \"later\", \"after this\" or a closing colon in a sentence of your own", p.where, shorten(sent))
			}
		}
	}
}

func shorten(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "..."
	}
	return s
}

// checkLetterOwn gates the model's own letter text: names and figures rest on
// the applicant's sources. A company fact pasted into the text is the
// applicant's text and fails here like any other; the message says how to
// quote it.
func (m Manifest) checkLetterOwn(g *Gate, where string, own string, c corpus, n names) {
	before := len(g.Errors)
	flat := norm(own)
	own = m.withoutReferences(own)
	checkClaims(g, where, own, c)
	checkNames(g, where, own, n)
	if len(g.Errors) == before {
		return
	}
	for _, f := range m.CompanyFacts {
		if k := norm(strings.TrimRight(slotText(f.Text), ".!?")); k != "" && strings.Contains(flat, k) {
			g.fail("company-fact-without-slot", "%s repeats company fact %s as the applicant's own text, which no source backs; choose a company fact by listing %s in companyFacts and the system quotes it", where, f.ID, f.ID)
		}
	}
}

// factsForPrompt lists the facts a model may choose, one per line.
func (m Manifest) factsForPrompt() string {
	var b strings.Builder
	for _, c := range m.slotFacts() {
		fmt.Fprintf(&b, "%s %s (source %s)\n", c.ID, slotText(c.Text), c.Source)
	}
	return b.String()
}
