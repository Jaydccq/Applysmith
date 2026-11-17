package documents

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

// manifestFor builds the manifest of the synthetic persona from a chosen CV
// and posting.
func manifestFor(t *testing.T, cv, posting string) Manifest {
	t.Helper()
	return manifestWith(t, testProfile, cv, posting)
}

func manifestWith(t *testing.T, profile, cv, posting string) Manifest {
	t.Helper()
	p, rep := workspace.ParseProfile([]byte(profile))
	if !rep.OK() {
		t.Fatalf("profile: %+v", rep)
	}
	m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(profile), CV: cv, Posting: posting, Company: "Example Corp", Role: "Backend Engineer"}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

const jdWithTechTheCVLacks = `Backend Engineer at Example Corp

Requirements:
- Experience with Cassandra, Kafka and Terraform
- Experience with PostgreSQL and SQL data modelling
- Ability to mentor engineers and lead code review
`

// Scenario: Unsupported technology. A technology only the posting names never
// grounds a claim, in any case or position, in a bullet or in a letter.
func TestGrounding_PostingTextNeverGroundsATechnology(t *testing.T) {
	m := manifestFor(t, testCV, jdWithTechTheCVLacks)
	mig := evID(t, m, "platform migration")
	cases := []struct {
		name, text string
		code       string
	}{
		{"posting-only technology", "Led the platform migration onto Cassandra and Kafka", "unsupported-name"},
		{"technology outside the posting in lower case", "Led the platform migration onto kubernetes and docker", "unsupported-name"},
		{"first word of the sentence", "Kubernetes rollout: led the platform migration", "unsupported-name"},
		{"technology the CV holds", "Led the platform migration using Docker and PostgreSQL", ""},
		{"verbs open a bullet", "Mentored engineers and led the platform migration", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := checkResume(m, docWith(m, "X1", c.text, mig))
			if c.code == "" {
				if hasCode(g, "unsupported-name") {
					t.Errorf("errors = %+v", g.Errors)
				}
				return
			}
			if !hasCode(g, c.code) {
				t.Errorf("errors = %+v, want %s", g.Errors, c.code)
			}
		})
	}
}

func TestGrounding_LetterIsHeldToTheSameBoundary(t *testing.T) {
	m := manifestFor(t, testCV, jdWithTechTheCVLacks)
	const closing = "I would welcome a conversation. Thank you for reading."
	prefix := "I am applying for the Backend Engineer role at Example Corp because "
	for _, c := range []struct {
		name, text string
		fails      bool
	}{
		{"posting technology", prefix + "I have run Cassandra clusters in production for years.", true},
		{"technology outside the posting", prefix + "I have run kubernetes in production for years.", true},
		{"first word", "Kafka pipelines are what I know best. " + prefix + "backend services in Go matter to me.", true},
		{"company, role and own technology", prefix + "backend services in Go and PostgreSQL matter to me.", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := checkLetter(m, letterDoc(m, c.text, closing))
			if got := hasCode(g, "unsupported-name"); got != c.fails {
				t.Errorf("unsupported-name = %v, want %v; errors = %+v", got, c.fails, g.Errors)
			}
		})
	}
}

// A company fact the system quotes may name the company's own product; the same
// name still cannot be claimed as the user's technology.
func TestGrounding_CompanyFactsNameProductsOnlyInALetter(t *testing.T) {
	m := manifestFor(t, testCV, testPosting)
	m.CompanyFacts = []CompanyFact{{ID: "C1", Text: "Example Corp ships the Orbit reef platform", Source: "https://example.com/about"}}
	must(t, m.Freeze())
	l := letterDoc(m, "I am applying for the Backend Engineer role at Example Corp.", "Thank you for reading this letter.")
	l.CompanyFacts = []string{"C1"}
	if g := checkLetter(m, l); hasCode(g, "unsupported-name") {
		t.Errorf("letter: %+v", g.Errors)
	}
	g := checkResume(m, docWith(m, "X1", "Led the Orbit platform migration", evID(t, m, "platform migration")))
	if !hasCode(g, "unsupported-name") {
		t.Errorf("a résumé bullet may not claim a company's product: %+v", g.Errors)
	}
}

// Whole-token matching: a name is found only as a word of its own.
func TestGrounding_NamesMatchWholeTokens(t *testing.T) {
	for _, c := range []struct {
		name, cvTech, bullet string
		fails                bool
	}{
		{"Java is not JavaScript", "JavaScript", "Led the platform migration of the Java monolith", true},
		{"Rust is not trust", "trust", "Led the platform migration in Rust", true},
		{"C is not C++", "C++", "Led the platform migration in C", true},
		{"C# is not C", "C", "Led the platform migration in C#", true},
		{".NET held", ".NET", "Led the platform migration onto .NET", false},
		{"Node.js held", "Node.js", "Led the platform migration onto Node.js", false},
		{"Node.js absent", "Node", "Led the platform migration onto Node.js", false},
		{"plural held", "microservices", "Led the platform migration of a microservice fleet", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cv := strings.Replace(testCV, "Python", c.cvTech, 1)
			m := manifestFor(t, cv, testPosting)
			g := checkResume(m, docWith(m, "X1", c.bullet, evID(t, m, "platform migration")))
			if got := hasCode(g, "unsupported-name"); got != c.fails {
				t.Errorf("unsupported-name = %v, want %v; errors = %+v", got, c.fails, g.Errors)
			}
		})
	}
}

// The persona's own language is Go; with it removed, "good" holds no Go.
func TestGrounding_GoIsNotGood(t *testing.T) {
	goWord := regexp.MustCompile(`\bGo\b`)
	cv := goWord.ReplaceAllString(testCV, "good")
	profile := strings.Replace(testProfile, "- name: Go", "- name: good", 1)
	m := manifestWith(t, profile, cv, testPosting)
	inX1 := func(g Gate) bool {
		for _, f := range g.Errors {
			if f.Code == "unsupported-name" && strings.Contains(f.Detail, "experience X1") {
				return true
			}
		}
		return false
	}
	for _, text := range []string{"Led the platform migration in Go", "Led the platform migration and go-to-market work"} {
		if g := checkResume(m, docWith(m, "X1", text, evID(t, m, "platform migration"))); !inX1(g) {
			t.Errorf("%q: errors = %+v", text, g.Errors)
		}
	}
	if g := checkResume(m, docWith(m, "X1", "Led the platform migration in good order", evID(t, m, "platform migration"))); inX1(g) {
		t.Errorf("good is held: %+v", g.Errors)
	}
}

// A skills line lists a technology only when a selected evidence line holds it
// as a word of its own.
func TestGrounding_CompetencyItemsMatchWholeWords(t *testing.T) {
	m := manifestFor(t, strings.Replace(testCV, "Python", "JavaScript", 1), testPosting)
	skills := evID(t, m, "Languages:")
	for _, c := range []struct {
		text  string
		fails bool
	}{{"Go, SQL, JavaScript", false}, {"Go, Java", true}, {"Go, Script", true}} {
		d := ResumeDoc{Schema: docSchema, ManifestHash: m.Hash, Language: "en", ResumeDraft: ResumeDraft{
			Competencies: []Competency{{Label: "Languages", Text: c.text, Evidence: []string{skills}}}}}
		if got := hasCode(checkResume(m, d), "unsupported-technology"); got != c.fails {
			t.Errorf("%q: unsupported-technology = %v, want %v", c.text, got, c.fails)
		}
	}
}

// Scenario: Altered figure and Moved figure, with the subject and the count
// in words: a quantified claim keeps its unit and its relationship to its
// subject.
func TestGrounding_QuantifiedClaimKeepsItsSubject(t *testing.T) {
	m := testManifest(t)
	mig, ment := evID(t, m, "platform migration"), evID(t, m, "Mentored")
	cases := []struct {
		name, text string
		id         string
		fails      bool
	}{
		{"subject changed", "Cut cloud costs by 40% across 12 services", mig, true},
		{"modifier added to the subject", "Cut incident costs by 40%", mig, true},
		{"same subject", "Cut incidents by 40% across 12 services", mig, false},
		{"nominal form", "Delivered a 40% incident reduction across 12 services", mig, false},
		{"opposite direction", "Increased incidents by 40% across 12 services", mig, true},
		{"no subject at all", "Improved results by 40%", mig, true},
		{"count in words, other value", "Mentored twelve engineers in code review and design", ment, true},
		{"count in words, same value", "Mentored four engineers in code review and design", ment, false},
		{"count in words, other subject", "Mentored four managers in code review and design", ment, true},
		{"halved", "Led the platform migration and halved incidents across 12 services", mig, true},
		{"percent in words, same value", "Cut incidents by forty percent across 12 services", mig, false},
		{"percent in words, other value", "Cut incidents by thirty percent across 12 services", mig, true},
		{"hyphenated number word", "Cut incidents by forty-five percent across 12 services", mig, true},
		{"count of other things", "Cut incidents by 40% across 12 teams", mig, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := checkResume(m, docWith(m, "X1", c.text, c.id))
			if got := hasCode(g, "unsupported-figure"); got != c.fails {
				t.Errorf("unsupported-figure = %v, want %v; errors = %+v", got, c.fails, g.Errors)
			}
		})
	}
}

func TestQuantities_ExtractWordsRatiosAndPercentWords(t *testing.T) {
	type want struct {
		value float64
		unit  string
	}
	for _, c := range []struct {
		text string
		want []want
	}{
		{"Mentored twelve engineers", []want{{12, "engineer"}}},
		{"Cut costs by forty percent", []want{{40, "%"}}},
		{"cut by forty-five per cent", []want{{45, "%"}}},
		{"two hundred and fifty users", []want{{250, "user"}}},
		{"halved latency", []want{{0.5, "x"}}},
		{"cut the bill in half", []want{{0.5, "x"}}},
		{"a three-step pipeline", []want{{3, "step"}}},
		{"served one of the teams", nil},
		{"no one noticed", nil},
		{"Mentored one engineer", []want{{1, "engineer"}}},
		{"zero downtime", []want{{0, "downtime"}}},
	} {
		got := quantities(c.text)
		var have []want
		for _, q := range got {
			have = append(have, want{q.value, q.unit})
		}
		if !slices.Equal(have, c.want) {
			t.Errorf("%q: got %v, want %v", c.text, have, c.want)
		}
	}
}

func TestKeywords_DropStopWords(t *testing.T) {
	// A figure is not a word: demo posting 2207's salary made "000" its top
	// keyword, and a letter that named it failed its gate.
	posting := "Requirements:\n- Experience with the Go language and with PostgreSQL\n- Work with teams across the company, in the office or at home, for the best results\n" +
		"Salary $125,000 to $145,000; 400,000 reminders a week.\n"
	for _, k := range topKeywords(posting) {
		if isStop(k) || !hasLetter(k) {
			t.Errorf("keyword %q is a stop word or a figure", k)
		}
	}
}

// Scenario: The role grounds nothing about experience. The role and the
// company are named only where the letter refers to them.
func TestGrounding_TheRoleTitleNeverGroundsAnExperienceClaim(t *testing.T) {
	jd := strings.Replace(jdWithTechTheCVLacks, "Backend Engineer at Example Corp", "Senior Kafka Engineer at Example Corp", 1)
	p, rep := workspace.ParseProfile([]byte(testProfile))
	if !rep.OK() {
		t.Fatal(rep)
	}
	m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(testProfile), CV: testCV, Posting: jd, Company: "Example Corp", Role: "Senior Kafka Engineer"}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	const closing = "I would welcome a conversation. Thank you for reading."
	for _, c := range []struct {
		name, text string
		fails      bool
	}{
		{"experience with the role's technology", "I am applying for the Senior Kafka Engineer role at Example Corp because I have built Kafka pipelines in production for years.", true},
		{"role and company reference only", "I am applying for the Senior Kafka Engineer role at Example Corp because backend services in Go and PostgreSQL matter to me.", false},
		{"role in lower case", "I am applying for the senior kafka engineer role at example corp because backend services in Go matter to me.", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := checkLetter(m, letterDoc(m, c.text, closing))
			if got := hasCode(g, "unsupported-name"); got != c.fails {
				t.Errorf("unsupported-name = %v, want %v; errors = %+v", got, c.fails, g.Errors)
			}
		})
	}
}

// Scenario: Lower-case technologies. A name the posting lists, or that looks
// like a product, is held to the user's sources in any case.
func TestGrounding_LowerCaseTechnologiesAreChecked(t *testing.T) {
	jd := "Backend Engineer at Example Corp\n\nRequirements:\n- Experience with clickhouse, dagster and trino\n- Ability to mentor engineers\n"
	m := manifestFor(t, testCV, jd)
	mig := evID(t, m, "platform migration")
	bare := manifestFor(t, testCV, testPosting)
	for _, c := range []struct {
		name  string
		m     Manifest
		text  string
		fails bool
	}{
		{"listed by the posting in lower case", m, "Led the platform migration onto dagster and trino", true},
		{"outside the posting, in the floor", bare, "Led the platform migration onto clickhouse and supabase", true},
		{"outside the posting, with a digit", bare, "Led the platform migration onto s3 and ec2", true},
		{"ordinary words of the same posting", m, "Led the platform migration and mentored engineers", false},
		{"a technology the CV holds", bare, "Led the platform migration with docker and postgresql", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := checkResume(c.m, docWith(c.m, "X1", c.text, mig))
			if got := hasCode(g, "unsupported-name"); got != c.fails {
				t.Errorf("unsupported-name = %v, want %v; errors = %+v", got, c.fails, g.Errors)
			}
		})
	}
}
