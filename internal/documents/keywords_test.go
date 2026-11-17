package documents

import (
	"slices"
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

// The opening names what the posting asks for and the candidate's own lines
// hold: tools, languages and listed skills, spelled as a person writes them.
// A function word, a common word or a lower-cased acronym reads as machine
// output, so a posting that names nothing the candidate has gets no list.
func TestDeterministicLetter_NamesKeywordsAsThePostingWritesThem(t *testing.T) {
	p, _ := workspace.ParseProfile([]byte(testProfile))
	cv := func(bullets, skills string) string {
		return "# Alex Example\n\n## Experience\n\n### Senior Engineer, Example Systems (2021-03 to present)\n\n" + bullets + "\n\n## Skills\n\n" + skills + "\n"
	}
	marketing := cv("- Ran email campaigns in HubSpot that lifted repeat purchases by 18%\n- Managed a $400K paid social budget across Meta and TikTok",
		"- Tools: HubSpot, Google Analytics, Excel\n- Channels: email, paid social, SEO")
	data := cv("- Built Airflow pipelines that load 2 TB a day into Snowflake\n- Rewrote dbt models and cut warehouse cost by 30%", "- Languages: Python, SQL\n- Tools: Airflow, dbt, Snowflake, Kafka")
	for _, c := range []struct {
		name, cv, posting string
		want, never       []string // want empty: no emphasis sentence at all
	}{
		{"named tools", testCV, testPosting, []string{"Go", "PostgreSQL", "SQL"}, []string{"services", "mentor", "postgresql"}},
		{"lower-case posting", strings.Replace(testCV, "Docker", "Docker, AWS", 1),
			"We build internal services in go. Our internal services run on aws.\nRequirements:\n- Experience building internal services in go\n- Experience with postgres and aws\n",
			[]string{"PostgreSQL", "AWS"}, []string{"go", "internal", "services", "aws", "postgres"}},
		{"nothing named", testCV, "Our team keeps the services used by dispatch running.\n\nYou will\n- Be on call for a 24/7 control room.\n\nYou have\n- 10+ years in operations or site reliability engineering.\n- 3+ years managing engineers.\n- Experience with safety-critical systems.\n", nil, nil},
		{"non-tech", marketing, "What we're looking for\n- 4+ years of experience in marketing; you go the extra mile\n- Hands-on experience with HubSpot\n- Experience managing paid social budgets on Meta and TikTok\n- Excellent writing skills\n",
			[]string{"HubSpot", "Meta"}, []string{"Go", "go", "paid", "social", "experience", "marketing"}},
		{"data", data, "We own the warehouse and the pipelines that feed it; pipelines run hourly into the warehouse.\n\nWhat we look for\n- 5+ years of experience in data engineering\n- Expert SQL and Python\n- Experience with Airflow and dbt for our warehouse pipelines\n",
			[]string{"SQL", "Python", "Airflow"}, []string{"warehouse", "pipelines", "data"}},
	} {
		m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(testProfile), CV: c.cv, Posting: c.posting, Company: "Example Corp", Role: "Backend Engineer"}, ModeTailored, 2)
		must(t, err)
		d, ok := deterministicLetter(m, ResumeDoc{}, "en")
		_, list, said := strings.Cut(d.Opening, " Your posting emphasizes ")
		items := splitItems(list)
		switch {
		case !ok:
			t.Errorf("%s: no letter", c.name)
		case said != (len(c.want) > 0):
			t.Errorf("%s: opening %q; want an emphasis list only of %v", c.name, d.Opening, c.want)
		}
		for _, w := range c.want {
			if !slices.Contains(items, w) {
				t.Errorf("%s: emphasis %q lacks %q as written", c.name, list, w)
			}
		}
		for _, w := range c.never {
			if slices.Contains(items, w) {
				t.Errorf("%s: emphasis %q names %q", c.name, list, w)
			}
		}
	}
}

// A tailored run without a pasted posting reads the candidacy's report. Read
// raw, a live report gave the core keywords "CV, evidence, level, id, label,
// hit" and requirements that were rows of its evidence table.
func TestPostingFromReport_RequirementsAndKeywordsOfTheJob(t *testing.T) {
	md := "report: 0004\nscore: 3.4\n\n# Mercury - Senior Backend Engineer\n\n## Fit\n\n| Group | Requirement | Hit | Evidence |\n|---|---|---|---|\n" +
		"| must-have | SQL | 1.0 | CV: lists SQL |\n\n```scorecard\n" +
		`{"items":{"must_have":[{"id":"m1","label":"Proficiency in SQL","source":"CV","hit_level":1,"evidence":"CV: lists SQL"},` +
		`{"id":"m2","label":"Haskell","source":"Knowledge of or excitement to learn Haskell","hit_level":0,"evidence":"CV: none"},` +
		`{"id":"m3","label":"Five years of backend work","source":"\u201c5+ years building backend systems in Go\u201d","hit_level":1,"evidence":"CV: nine years"},` +
		`{"id":"m4","label":"Go","source":"\u201c5+ years building backend systems in Go\u201d","hit_level":1,"evidence":"CV: Go"}],` +
		`"nice_to_have":[{"id":"n1","label":"Fintech domain","source":"","hit_level":1,"evidence":"CV: billing"}]}}` +
		"\n```\n\n## Pay\n\nStated range.\n\n## Keywords\n\nHaskell, SQL, backend, fintech\n"
	reqs, keywords, nice, _ := successProfile(postingFromReport(md))
	// m3 and m4 quote one posting line (demo report 0001 listed it twice, in
	// quote marks): one requirement, in the posting's words only.
	if len(reqs) != 3 || reqs[0].Text != "Proficiency in SQL" || reqs[1].Text != "Knowledge of or excitement to learn Haskell" ||
		reqs[2].Text != "5+ years building backend systems in Go" || len(nice) != 1 {
		t.Fatalf("requirements %+v nice %v", reqs, nice)
	}
	for _, k := range keywords {
		switch strings.ToLower(k) {
		case "cv", "label", "hit", "evidence", "scorecard", "keywords":
			t.Fatalf("report vocabulary %q in keywords %v", k, keywords)
		}
	}
	lower := strings.ToLower(strings.Join(keywords, " "))
	if !strings.Contains(lower, "haskell") || !slices.Contains(keywords, "SQL") {
		t.Fatalf("keywords %v", keywords)
	}
	if got := postingFromReport("# A screening report\n\nNo scorecard."); got != "# A screening report\n\nNo scorecard." {
		t.Fatalf("a report without a scorecard changed: %q", got)
	}
}
