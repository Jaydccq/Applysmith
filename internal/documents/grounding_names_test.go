package documents

import (
	"testing"

	"applysmith/internal/demo"
)

// Scenario: A technology name that is also a plain word is watched only in
// its technology spelling.
func TestGrounding_PlainWordTechnologyNames(t *testing.T) {
	n := names{known: map[string]bool{}, watch: map[string]bool{"next": true, "react": true, "node": true}}
	cases := []struct {
		text string
		fail bool
	}{
		{"I want to help the team ship before the next release.", false},
		{"I help on-call react faster.", false},
		{"React faster to incidents.", false},
		{"I built dashboards in React.", true},
		{"I built a gateway on Node.js.", true},
		{"I built a gateway on Node.", true},
	}
	for _, c := range cases {
		var g Gate
		checkNames(&g, "letter", c.text, n)
		if got := len(g.Errors) > 0; got != c.fail {
			t.Errorf("%q: failed = %v, want %v (%+v)", c.text, got, c.fail, g.Errors)
		}
	}
}

// A lower-case "react" in the user's own sources does not make the framework
// known; the spelled technology does.
func TestGrounding_PlainWordTechnologyKnownOnlyInTechSpelling(t *testing.T) {
	m := factManifest(t)
	m.Evidence = append([]Evidence(nil), m.Evidence...)
	n := m.names("We react to incidents and ship the next release.")
	if n.known["react"] || n.known["next"] {
		t.Errorf("plain words made a technology known: %+v", n.known)
	}
	n = m.names("Built the dashboard in React with Next.js.")
	if !n.known["react"] || !n.known["next"] {
		t.Errorf("technology spellings are not known: %+v", n.known)
	}
}

// A model letter that writes its evidence ids into the text ("[E12]"), opens a
// sentence with "Alongside", or uses a bare success-profile keyword ("impact")
// is not refused as naming things no source holds; a listed product of the
// posting is still watched.
func TestGrounding_InlineIDsAndBareKeywordsAreNotNames(t *testing.T) {
	m := factManifest(t)
	m.Keywords = append(m.Keywords, "impact", "startup")
	m.Requirements = append(m.Requirements, Requirement{Text: "Experience with clickhouse and dagster"})
	n := m.names()
	if n.watch["impact"] || n.watch["startup"] {
		t.Errorf("a bare keyword is watched as a technology: %+v", n.watch)
	}
	if !n.watch["clickhouse"] || !n.watch["dagster"] {
		t.Errorf("a listed product of the posting is not watched: %+v", n.watch)
	}
	d := normalizeLetter(LetterDraft{Opening: "Alongside that, I care about impact [E1, E2].", Body: "Pages fell (E12).", Closing: "I would like to talk [E3 and E4]."})
	if d.Opening != "Alongside that, I care about impact." || d.Body != "Pages fell." || d.Closing != "I would like to talk." {
		t.Errorf("inline ids kept: %+v", d)
	}
	var g Gate
	checkNames(&g, "letter", d.Opening, n)
	if len(g.Errors) > 0 {
		t.Errorf("letter refused: %+v", g.Errors)
	}
	g = Gate{}
	checkNames(&g, "letter", "I ran dagster in production.", n)
	if len(g.Errors) == 0 {
		t.Error("an unbacked listed product passed")
	}
}

// A summary in the third person opens sentences with "Works", "Leads" or
// "Manages"; those are verbs, not names. A product no source holds still fails.
func TestGrounding_ThirdPersonSummaryVerbsAreNotNames(t *testing.T) {
	n := factManifest(t).summaryNames()
	var g Gate
	checkNames(&g, "profile", "Works across teams. Leads the review. Manages the rota.", n)
	if len(g.Errors) > 0 {
		t.Errorf("a third-person verb was read as a name: %+v", g.Errors)
	}
	g = Gate{}
	checkNames(&g, "profile", "Works with Oracle.", n)
	if len(g.Errors) == 0 {
		t.Error("an unbacked product passed")
	}
}

// demoPosting holds the requirement lines of the letter
// measurement (demo persona, thinking off), in the forms the postings used.
const demoPosting = `Senior Platform Engineer at Example Corp

Requirements:
- "5+ years building backend or platform systems in Go or Python"
- "Production experience with Kubernetes and Terraform"
- Track record of shipping internal tools
- Proficiency in JavaScript and React

Nice to have:
- Our stack involves TypeScript and Vite
`

func demoManifest(t *testing.T, company, role string) Manifest {
	t.Helper()
	m := manifestWith(t, string(demo.File("profile.yml")), string(demo.File("cv.md")), demoPosting)
	m.Company, m.Role = company, role
	must(t, m.Freeze())
	return m
}

// ownGate runs the checks a letter's own text gets.
func ownGate(m Manifest, text string) Gate {
	var g Gate
	m.checkLetterOwn(&g, "letter", text, corpusQuantities(m.corpus("", true), m.userTexts()), m.names())
	return g
}

// Scenario: the rejections of the letter measurement that were not
// fabrications. Each sentence is quoted from a refused model draft; the
// fabrications of the same drafts must still fail.
func TestGrounding_MeasuredLetterFalsePositives(t *testing.T) {
	m := demoManifest(t, "Example Corp", "Senior Platform Engineer")
	flexi := demoManifest(t, "Flexi", "Flexi - Junior Full-Stack Software Engineer Application")
	kafka := demoManifest(t, "Example Corp", "Platform Engineer - Kafka")
	remote := demoManifest(t, "TixTrack", "Software Engineer (Remote - US)")
	for _, r := range []string{"Candidates must be located in Texas, New Jersey or New York.", "Bachelor's degree in Computer Science, Engineering, or a related field.",
		"Knowledge of SQL and relational databases.", "Curiosity, including picking up new languages and tools."} {
		remote.Requirements = append(remote.Requirements, Requirement{Text: r})
	}
	remote.NiceToHave = append(remote.NiceToHave, "Payments integrations such as Stripe")
	for _, c := range []struct {
		m    Manifest
		text string
		ok   bool
	}{
		// "Production" opened a requirement line, so the word was watched as a product.
		{m, "Across my career I have run internal deployment tooling for product teams and built production backend services, and I would bring that mix of platform ownership and on-call discipline to this position.", true},
		{m, "I have a track record of shipping internal tools.", true},
		// The role named without the company and the page title's "Application".
		{flexi, "I am applying for the Junior Full-Stack Software Engineer role at Flexi.", true},
		// A role that ends in a bracket is still the role ("US" is no claim).
		{remote, "I am writing to apply for the Software Engineer (Remote - US) role at TixTrack.", true},
		{remote, "I have shipped software for US customers.", false},
		// The TixTrack drafts: a posting's capital ("New Jersey", "Engineering")
		// does not watch the lower-case word, and a sentence may open with a
		// word the posting writes in lower case.
		{remote, "A few results from that work show how I approach engineering problems.", true},
		{remote, "I pick up new languages and tools by shipping with them.", true},
		{remote, "Relational data modeling is the part of the work I enjoy most.", true},
		{m, "Relational data modeling is the part of the work I enjoy most.", false},
		{remote, "Most recently at Stripe, I built payment services.", false},
		{remote, "I built payment flows on stripe.", false},
		{remote, "Relativity Space taught me to ship.", false},
		{remote, "I have worked in New York.", false},
		{remote, "I hold a degree in Computer Engineering.", false},
		{remote, "I cut latency 47% on the order service.", false},
		// Fabrications in the same drafts.
		{m, "My work has centered on JavaScript, React, and interfaces that people depend on every day.", false},
		{m, "Over five years of backend and platform work in Go and Python, I have run production systems.", false},
		{flexi, "I bring full-stack experience across Go, Python and PostgreSQL.", false},
		{flexi, "I am based in the United States and glad to work from Atlanta, GA.", false},
		{kafka, "I have built Kafka pipelines.", false},
	} {
		if g := ownGate(c.m, c.text); g.OK() != c.ok {
			t.Errorf("%q: ok = %v, want %v (%s)", c.text, g.OK(), c.ok, gateDetail(g))
		}
	}
	n := m.names()
	if n.watch["production"] || n.watch["track"] || !n.watch["typescript"] || !n.watch["vite"] {
		t.Errorf("watch list: production %v, track %v, typescript %v, vite %v", n.watch["production"], n.watch["track"], n.watch["typescript"], n.watch["vite"])
	}
}
