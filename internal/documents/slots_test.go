package documents

import (
	"strings"
	"testing"
	"time"

	"applysmith/internal/documents/assets"
)

const (
	slotApply   = "I am applying for the Backend Engineer role at Example Corp."
	slotClosing = "I would welcome a conversation about the role at Example Corp."
	fact1       = "Example Corp runs its event platform on Cassandra and grew revenue 40% in 2025."
)

// factManifest holds one eligible fact that names a technology and a figure,
// one that speaks as "we", two plain third-person facts, one about the
// engineers, and one that describes the ideal candidate.
func factManifest(t *testing.T) Manifest {
	t.Helper()
	m := manifestFor(t, testCV, testPosting)
	m.CompanyFacts = []CompanyFact{
		{ID: "C1", Text: "Example Corp runs its event platform on Cassandra and grew revenue 40% in 2025", Source: "https://example.com/about"},
		{ID: "C2", Text: "We run our event platform on Cassandra", Source: "https://example.com/about"},
		{ID: "C3", Text: "Example Corp builds reliable tools for coastal agencies.", Source: "https://example.com/about"},
		{ID: "C4", Text: "Example Corp publishes its sensor standards in the open", Source: "https://example.com/blog"},
		{ID: "C5", Text: "Example Corp engineers scaled Cassandra to 2 million writes per second", Source: "https://example.com/blog"},
		{ID: "C6", Text: "The ideal candidate has scaled Cassandra to 2 million writes per second", Source: "https://example.com/jobs"},
	}
	must(t, m.Freeze())
	return m
}

// letterWith builds a letter: the body follows the opening, then the bullets,
// then the company paragraph for facts, then the closing.
func letterWith(m Manifest, opening, body, closing string, facts ...string) LetterDoc {
	l := letterDoc(m, opening, closing)
	l.Body = body
	l.CompanyFacts = facts
	return l
}

func withBullet(t *testing.T, m Manifest, l LetterDoc, text string) LetterDoc {
	t.Helper()
	l.Bullets = append(l.Bullets, Bullet{Text: text, Evidence: []string{evID(t, m, "platform migration")}})
	return l
}

// Scenario: Company facts enter a letter in one place, verbatim.
func TestSlots_FactsArePrintedInTheCompanyParagraph(t *testing.T) {
	m := factManifest(t)
	l := letterWith(m, slotApply, "I enjoy backend work in Go.", "I want to help the team ship dependable software.", "C1")
	if g := checkLetter(m, l); !g.OK() {
		t.Fatalf("errors = %+v", g.Errors)
	}
	want := "What draws me to Example Corp: " + fact1
	if got := m.companyParagraph(l); got != want {
		t.Errorf("company paragraph = %q", got)
	}
	c, err := buildCover(m, l, assets.PaperA4, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	if err != nil || len(c.Blocks) != 4 || c.Blocks[2].Text != want {
		t.Errorf("blocks = %+v, %v", c.Blocks, err)
	}
	md := letterMarkdown(m, l)
	if i, j, k := strings.Index(md, "I enjoy backend"), strings.Index(md, want), strings.Index(md, "ship dependable"); !(i < j && j < k) {
		t.Errorf("markdown order wrong: %q", md)
	}
	if got := m.companyParagraph(letterWith(m, slotApply, "", slotClosing)); got != "" {
		t.Errorf("no facts chosen, paragraph = %q", got)
	}
}

func TestSlots_UnknownIneligibleAndSurplusFacts(t *testing.T) {
	m := factManifest(t)
	cases := []struct {
		name  string
		facts []string
		code  string
	}{
		{"unknown id", []string{"C9"}, "unknown-company-fact"},
		{"a fact that says we", []string{"C2"}, "ineligible-company-fact"},
		{"a fact about the ideal candidate", []string{"C6"}, "ineligible-company-fact"},
		{"three facts", []string{"C1", "C3", "C4"}, "bad-slot"},
		{"the same fact twice", []string{"C3", "C3"}, "bad-slot"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if g := checkLetter(m, letterWith(m, slotApply, "", slotClosing, c.facts...)); !hasCode(g, c.code) {
				t.Errorf("errors = %+v, want %s", g.Errors, c.code)
			}
		})
	}
	if g := checkLetter(m, letterWith(m, slotApply, "", slotClosing, "C3", "C4")); !g.OK() {
		t.Errorf("two facts: errors = %+v", g.Errors)
	}
	for _, text := range []string{"Applicants run Cassandra", "Example Corp is looking for engineers", "A successful engineer owns the platform", "Example Corp hires engineers", "Requirements include Go", "You will join us"} {
		if slotEligible(text) {
			t.Errorf("%q is slot-eligible", text)
		}
	}
}

// Slots outside the company paragraph: a marker typed anywhere in the model's
// text is refused, so a fact has exactly one place.
func TestSlots_AMarkerInTheTextIsRefused(t *testing.T) {
	m := factManifest(t)
	for name, l := range map[string]LetterDoc{
		"opening": letterWith(m, slotApply+" [[fact:C3]]", "", slotClosing),
		"body":    letterWith(m, slotApply, "[[fact:C3]]", slotClosing),
		"closing": letterWith(m, slotApply, "", slotClosing+" [[fact:C3]]"),
		"bullet":  withBullet(t, m, letterWith(m, slotApply, "", slotClosing), "[[fact:C3]]"),
	} {
		t.Run(name, func(t *testing.T) {
			if g := checkLetter(m, l); !hasCode(g, "bad-slot") {
				t.Errorf("errors = %+v", g.Errors)
			}
		})
	}
}

// Scenario: All model-written text is the applicant's. The attack sentences of
// the earlier designs fail because the model's own words name Cassandra or a
// figure that no source of the user holds, wherever they stand and whether or
// not the fact is chosen.
func TestSlots_ModelTextIsTheApplicantsAndNeedsUserSources(t *testing.T) {
	m := factManifest(t)
	cases := []struct{ name, text, code string }{
		{"person noun, technology", "You would gain an engineer who has run Cassandra clusters in production.", "unsupported-name"},
		{"person noun, figure", "Your team would gain someone who grew revenue 40% in 2025.", "unsupported-figure"},
		{"person noun, plural", "You would gain engineers who have run Cassandra clusters.", "unsupported-name"},
		{"next hire", "Your next hire should have run Cassandra at scale.", "unsupported-name"},
		{"participle opener", "Having run Cassandra clusters in production, your event platform is where I want to contribute.", "unsupported-name"},
		{"-ing opener", "After running Cassandra clusters in production, I want to work on your event platform.", "unsupported-name"},
		{"like your engineers", "Like your engineers, I scaled Cassandra for years.", "unsupported-name"},
		{"bare noun-phrase opener", "Cassandra experience: your platform runs on it, and so does my last system.", "unsupported-name"},
		{"work I did", "Your event platform on Cassandra matches the Cassandra work I did.", "unsupported-name"},
		{"relative clause", "Your event platform runs on Cassandra, which I have run in production.", "unsupported-name"},
		{"so does mine", "Your event platform runs on Cassandra, and so does my last system.", "unsupported-name"},
		{"the same", "Your revenue grew 40% in 2025, and I did the same.", "unsupported-figure"},
		{"a stack I know", "Your event platform uses Cassandra, a stack I know well.", "unsupported-name"},
		{"yours truly", "Yours truly has run Cassandra clusters in production.", "unsupported-name"},
		{"the undersigned", "Example Corp runs its event platform on Cassandra, which the undersigned has operated at scale.", "unsupported-name"},
		{"this author", "Example Corp runs its event platform on Cassandra, familiar ground for this author.", "unsupported-name"},
		{"passive", "Cassandra clusters were scaled by me.", "unsupported-name"},
		{"we, a former team", "Example Corp runs on Cassandra, and we did too.", "unsupported-name"},
		{"figure anaphor", "Example Corp grew revenue 40% in 2025. I doubled that.", "unsupported-figure"},
		{"demonstrative and a noun", "Example Corp grew revenue 40% in 2025. I drove that strong growth.", "unsupported-figure"},
		{"object gap", "Your platform runs on Cassandra, and I know inside out.", "unsupported-name"},
		{"which is why I applied", "Your platform runs on Cassandra, which is why I applied.", "unsupported-name"},
		{"fronted figure", "With revenue up 40% in 2025, I would like to help.", "unsupported-figure"},
		{"first-person figure", "I grew revenue 40% in 2025.", "unsupported-figure"},
		{"we on my team", "On my team we ran Cassandra and grew revenue 40%.", "unsupported-figure"},
	}
	for _, c := range cases {
		for _, facts := range [][]string{nil, {"C1"}} {
			name := c.name
			if facts != nil {
				name += " beside a chosen fact"
			}
			t.Run(name, func(t *testing.T) {
				g := checkLetter(m, letterWith(m, slotApply, c.text, slotClosing, facts...))
				if !hasCode(g, c.code) {
					t.Errorf("errors = %+v, want %s", g.Errors, c.code)
				}
				g = checkLetter(m, letterWith(m, slotApply, "", c.text, facts...))
				if !hasCode(g, c.code) {
					t.Errorf("closing: errors = %+v, want %s", g.Errors, c.code)
				}
			})
		}
	}
}

// A fact pasted into the text is the applicant's text; the message says how to
// choose it.
func TestSlots_PastedFactFailsWithAHint(t *testing.T) {
	m := factManifest(t)
	g := checkLetter(m, letterWith(m, slotApply, "Example Corp runs its event platform on Cassandra and grew revenue 40% in 2025.", slotClosing))
	if !hasCode(g, "unsupported-name") || !hasCode(g, "company-fact-without-slot") {
		t.Fatalf("errors = %+v", g.Errors)
	}
	for _, f := range g.Errors {
		if f.Code == "company-fact-without-slot" && !strings.Contains(f.Detail, "companyFacts") {
			t.Errorf("detail = %q", f.Detail)
		}
	}
	if g := checkLetter(m, letterWith(m, slotApply, "", slotClosing, "C1")); !g.OK() {
		t.Errorf("chosen fact: errors = %+v", g.Errors)
	}
	// Words the user's own sources hold are the user's text and pass.
	if g := checkLetter(m, letterWith(m, slotApply, "I cut incidents by 40% across 12 services.", slotClosing)); !g.OK() {
		t.Errorf("errors = %+v", g.Errors)
	}
}

// Scenario: Back-references. Only the closing follows the facts; an own
// sentence there may not point back at them or reuse their words.
func TestSlots_NoReferenceBackFromTheClosing(t *testing.T) {
	m := factManifest(t)
	cases := []struct {
		name, closing, fact string
		reject              bool
	}{
		{"distant database", "I enjoy backend work. I like clear code. I have tuned that database for years.", "C1", true},
		{"it", "I have run it for years. Thank you for reading.", "C1", true},
		{"so did this engineer", "Many engineers like that platform, and so did this engineer.", "C1", true},
		{"so do I", "So do I.", "C1", true},
		{"neither", "Neither have I.", "C1", true},
		{"did too", "I did too.", "C1", true},
		{"the same", "I have done the same.", "C1", true},
		{"such", "I have operated such systems.", "C1", true},
		{"likewise", "Likewise, I enjoy backend work.", "C1", true},
		{"them", "I know them well.", "C1", true},
		{"their", "I admire their engineers.", "C1", true},
		{"subject-less after and", "The team ships quickly and keeps it stable.", "C1", true},
		{"subject-less, it", "Tuned it for production workloads.", "C1", true},
		{"subject-less, it, at scale", "Operated it at scale for 3 product teams.", "C1", true},
		{"this engineer", "This engineer enjoys backend work.", "C1", true},
		{"that team", "I was part of that team.", "C5", true},
		{"that company", "I worked with that company on the scaling.", "C5", true},
		{"this team", "This team interests me.", "C5", true},
		{"there", "I helped get there, cutting incidents by 40% across 12 services.", "C1", true},
		{"so", "I am so glad.", "C1", true},
		{"fact name reused", "I know Cassandra inside out.", "C1", true},
		{"fact name reused, other ending", "I have run Cassandra clusters for years.", "C1", true},
		{"fact name, no subject", "Cassandra for years.", "C5", true},
		{"fact name, lower case", "I have run cassandra for years.", "C1", true},
		{"ordinary fact words are free", "I would help keep the event platform reliable.", "C1", false},
		{"a plain noun of the fact is no claim", "I know the platform inside out.", "C1", false},
		{"a verb of the fact is free", "Scaling for years.", "C5", false},
		{"title word alone is not a posting noun", "This Backend Engineer enjoys Go.", "C1", true},
		{"title words are no exemption", "I want this Backend Engineer role.", "C1", true},
		{"that role stays free", "I want that role.", "C5", false},
		{"this role", "I want this role. I like this opportunity and the team.", "C1", false},
		{"a synonym is a known residual", "I know the database inside out.", "C1", false},
		{"the same stays a pointer", "I value the same cost discipline.", "C1", true},
		{"fact word, third-person sentence", "The platform matters to the team.", "C1", false},
		{"company name and posting nouns are no reuse", "I want to help Example Corp as a Backend Engineer on the team.", "C1", false},
		{"complementizer that", "I believe that dependable software matters.", "C1", false},
		{"it without the applicant", "The role matters because it keeps users safe.", "C1", false},
		{"plain", "I would welcome a conversation. Thank you for your time.", "C1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := checkLetter(m, letterWith(m, slotApply, "", c.closing, c.fact))
			if got := hasCode(g, "slot-reference"); got != c.reject {
				t.Errorf("slot-reference = %v, want %v; errors = %+v", got, c.reject, g.Errors)
			}
		})
	}
}

// The opening and the evidence paragraph come before any fact, so pointers
// and fact words there refer to nothing.
func TestSlots_TextBeforeTheCompanyParagraphIsFree(t *testing.T) {
	m := factManifest(t)
	l := letterWith(m, "I have run it for years on the platform. "+slotApply, "I know the platform inside out, so I did.", slotClosing, "C1")
	l = withBullet(t, m, l, "Tuned it for production workloads.")
	if g := checkLetter(m, l); hasCode(g, "slot-reference") || hasCode(g, "forward-reference") {
		t.Errorf("errors = %+v", g.Errors)
	}
	// With no fact chosen the closing is free as well.
	if g := checkLetter(m, letterWith(m, slotApply, "", "I have run it for years.")); hasCode(g, "slot-reference") {
		t.Errorf("errors = %+v", g.Errors)
	}
}

// Cataphora: the sentence before the company paragraph may not point ahead at
// it together with a first person.
func TestSlots_ForwardPointerBeforeTheCompanyParagraph(t *testing.T) {
	m := factManifest(t)
	cases := []struct {
		name, body, bullet string
		facts              []string
		reject             bool
	}{
		{"below, first person", "Here is my work below.", "", []string{"C1"}, true},
		{"last bullet", "", "My work is described as follows.", []string{"C1"}, true},
		{"no first person", "Examples are below.", "", []string{"C1"}, false},
		{"no fact chosen", "Here is my work below.", "", nil, false},
		{"next, after a participle", "I have tuned the database described next for years.", "", []string{"C1"}, true},
		{"after this, in a bullet", "", "Ran the database named after this list for years.", []string{"C1"}, true},
		{"next at the end of an intention", "The kind of system I want to work on next.", "", []string{"C1"}, false},
		{"a bullet that says next as an adjective", "", "Planned the next release of the ingestion service.", []string{"C1"}, false},
		{"a bullet with below as a bound", "", "Kept the p99 latency below 200 ms.", []string{"C1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := letterWith(m, slotApply, c.body, slotClosing, c.facts...)
			if c.bullet != "" {
				l = withBullet(t, m, l, c.bullet)
			}
			if got := hasCode(checkLetter(m, l), "forward-reference"); got != c.reject {
				t.Errorf("forward-reference = %v, want %v; errors = %+v", got, c.reject, checkLetter(m, l).Errors)
			}
		})
	}
}

func TestSlots_DeterministicLetterQuotesTheFirstEligibleFact(t *testing.T) {
	m := factManifest(t)
	d, ok := deterministicLetter(m, ResumeDoc{}, "en")
	if !ok || len(d.CompanyFacts) != 1 || d.CompanyFacts[0] != "C1" {
		t.Fatalf("company facts = %v", d.CompanyFacts)
	}
	if g := checkLetter(m, d); !g.OK() {
		t.Errorf("errors = %+v", g.Errors)
	}
	m.CompanyFacts = m.CompanyFacts[1:2] // only the "we" fact
	must(t, m.Freeze())
	if d, _ := deterministicLetter(m, ResumeDoc{}, "en"); len(d.CompanyFacts) != 0 {
		t.Errorf("an ineligible fact was quoted: %v", d.CompanyFacts)
	}
}

// Résumé text never quotes a company fact.
func TestSlots_ResumeBulletsAreUnaffected(t *testing.T) {
	m := factManifest(t)
	mig := evID(t, m, "platform migration")
	if g := checkResume(m, docWith(m, "X1", "Led the platform migration. [[fact:C1]]", mig)); g.OK() {
		t.Error("a marker in a résumé bullet passed")
	}
	if p := resumePrompt(m, "en", 1); strings.Contains(p, "Cassandra") || strings.Contains(p, "companyFacts") {
		t.Error("the résumé prompt carries a company fact")
	}
}

// A review edit is held to the same rules.
func TestSlots_ReviewEdit(t *testing.T) {
	m := factManifest(t)
	byAddr := map[string]span{"letter.opening": {text: slotApply}, "letter.body": {text: "I enjoy Go."}, "letter.closing": {text: slotClosing}, "letter.bullet.1": {text: "Ran the platform migration."}}
	f := reviewFinding{Evidence: []string{evID(t, m, "platform migration")}}
	cases := []struct {
		name, span, edit, code string
	}{
		{"plain opening", "letter.opening", slotApply + " I enjoy Go.", ""},
		{"pasted fact", "letter.opening", slotApply + " Example Corp runs its event platform on Cassandra and grew revenue 40% in 2025.", "company-fact-without-slot"},
		{"typed marker", "letter.opening", slotApply + " [[fact:C3]]", "bad-slot"},
		{"forward pointer", "letter.body", "Here is my work below.", "forward-reference"},
		{"fact name in the closing", "letter.closing", "I know Cassandra inside out.", "slot-reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.Span, f.Edit = c.span, c.edit
			g := screenFinding(m, byAddr, f)
			switch {
			case c.code == "" && !g.OK():
				t.Fatalf("errors = %+v", g.Errors)
			case c.code != "" && !hasCode(g, c.code):
				t.Errorf("errors = %+v, want %s", g.Errors, c.code)
			}
		})
	}
}
