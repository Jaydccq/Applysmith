package match

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// pfStep builds the step a kind resolver receives, for helpers called directly.
func pfStep(text string, f Facts) *Step {
	return &Step{Req: &Request{Facts: f}, Q: Question{Selector: "s", Text: text, Kind: KindRadioGroup, Options: opts("Yes", "No")}}
}

func screeningEntry(mode string, mods ...func(*Entry)) *Entry {
	e := ent("skill-experience-knowledge-gate", "fixed", withAnswer("Yes"))
	e.SkillMode = mode
	for _, m := range mods {
		m(&e)
	}
	return &e
}

// No evidence abstains while the search is incomplete (the knowledge base
// not read); once every source was read, a subject named nowhere is No.
func TestAnswerBank_SkillScreening_NoEvidenceAbstains(t *testing.T) {
	e := screeningEntry(SkillProfileSkill)
	f := alexFacts()
	f.KnowledgeRead = false
	d, ok := skillScreening(pfStep("Do you have experience with COBOL?", f), e)
	if !ok || d.Abstain == nil || d.Abstain.Reason != ReasonNoEntry || d.Answer != "" {
		t.Fatalf("draft = %+v", d)
	}
	f.KnowledgeRead = true
	if d, _ := skillScreening(pfStep("Do you have experience with COBOL?", f), e); d.Answer != "No" || d.Abstain != nil {
		t.Fatalf("a complete search that finds nothing: %+v", d)
	}
	t.Run("no skills and no terms at all asks for the profile", func(t *testing.T) {
		d, ok := skillScreening(pfStep("Do you have experience with COBOL?", Facts{}), e)
		if !ok || d.Abstain == nil || d.Abstain.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("draft = %+v", d)
		}
	})
	t.Run("a declared negative answer is No without a search", func(t *testing.T) {
		neg := screeningEntry(SkillProfileSkill, func(e *Entry) { e.SkillNegativeAnswer = "No, not yet" })
		d, _ := skillScreening(pfStep("Do you have experience with COBOL?", alexFacts()), neg)
		if d.Answer != "No, not yet" || d.Abstain != nil {
			t.Fatalf("draft = %+v", d)
		}
	})
	t.Run("not a screening entry", func(t *testing.T) {
		if _, ok := skillScreening(pfStep("x", alexFacts()), &Entry{Kind: KindFixed}); ok {
			t.Fatal("handled")
		}
	})
}

// F-10: with no declared negative, a question about the user's own experience
// is No only when none of their information mentions what it asks about, in
// either spelling of a common abbreviation, an acronym or a compound, and up
// to another form of a word; a mention anywhere leaves it to the evidence
// lanes, and so do a section the semantic search ranks near the question and
// a knowledge base not read whole.
func TestAnswerBank_SkillScreening_NoMentionAnywhereIsNo(t *testing.T) {
	const sports = "Do you have experience working in the sports industry?"
	const msp = "Do you have experience working within a Managed Service Provider environment?"
	const oss = "Do you have a public track record of open-source contributions?"
	const ossFull = "Do you have a public track record of open-source contributions, such as merged pull requests to libraries, or projects you've authored or actively maintain?"
	const riseUp = "Are you a graduate of the RiseUp with ServiceNow (formerly NextGen) Program?"
	cv := []string{"Built the billing service in Go"}
	// read is the user's information with the knowledge base read whole, and
	// near one whose semantic search ranks a section near question.
	read := func(f Facts, sections ...string) Facts {
		f.KnowledgeRead = true
		for i, text := range sections {
			f.Knowledge = append(f.Knowledge, KnowledgeSection{Ref: fmt.Sprintf("knowledge/experience/notes.md#Part %d", i+1), Text: text})
		}
		return f
	}
	near := func(f Facts, question string) Facts {
		f.KnowledgeNear = map[string][]string{question: {"knowledge/experience/cloud.md#Platform"}}
		return f
	}
	for _, c := range []struct {
		name, q string
		f       Facts
		entries []Entry
		want    string // "" is an abstain
	}{
		{"only in a knowledge section", sports, read(Facts{Evidence: cv}, "Built ticketing for a sports league."), nil, ""},
		{"only in the CV", sports, read(Facts{Evidence: append(cv, "Built ticketing for a sports league")}), nil, ""},
		{"only in a profile skill", "Do you have experience with Kubernetes?", read(Facts{Skills: []SkillFact{{Name: "Kubernetes operators"}}, Evidence: cv}), nil, ""},
		{"only in the employment history", sports, read(Facts{Evidence: cv, Employment: []EmploymentFact{{Employer: "Northwind Sports", Title: "Engineer"}}}), nil, ""},
		{"only in the education", "Do you have experience with bioinformatics?", read(Facts{Education: []EducationFact{{Field: "Bioinformatics"}}}), nil, ""},
		{"only in an answer the user wrote", sports, read(Facts{Evidence: cv}), []Entry{{ID: "league", Kind: KindFixed, Body: Body{Answer: "Volunteer coach in a youth sports league"}}}, ""},
		{"only in a language the user recorded", "Are you fluent in Spanish?", read(Facts{Evidence: cv}), []Entry{{ID: "second-language", Kind: KindLanguageProficiency, Body: Body{Language: "Spanish"}}}, ""},
		{"only in the skill terms of an entry the user wrote", "Have you used Terraform?", read(Facts{Evidence: cv}), []Entry{{ID: "iac", Kind: KindFixed, Meta: Meta{SkillTerms: []string{"Terraform"}}}}, ""},
		{"only in an employment summary", "Do you have experience with Kafka?", read(Facts{Evidence: cv, Employment: []EmploymentFact{{Employer: "Northwind Example", Summary: "Ran the Kafka event pipeline"}}}), nil, ""},
		{"only in an employment location", "Have you worked in Canada?", read(Facts{Evidence: cv, Employment: []EmploymentFact{{Employer: "Northwind Example", Location: "Toronto, Canada"}}}), nil, ""},
		{"only in the rest of the profile", "Do you have product management experience?", read(Facts{Evidence: cv, ProfileText: []string{"Product Manager"}}), nil, ""},
		{"a section the semantic search ranks near it", "Have you used GCP?", near(read(Facts{Evidence: cv}), "Have you used GCP?"), nil, ""},
		{"an acronym the CV spells out", "Have you used GCP?", read(Facts{Evidence: []string{"Moved the data platform to Google Cloud Platform"}}), nil, ""},
		{"a name the CV writes as an acronym", "Do you have Google Cloud Platform experience?", read(Facts{Evidence: []string{"Ran GCP projects"}}), nil, ""},
		{"an acronym whose letters the CV does not spell", "Have you used GCP?", read(Facts{Evidence: []string{"Built on Google Cloud Run"}}), nil, "No"},
		{"another form of the word, either way", "Have you ever worked remotely?", read(Facts{Evidence: []string{"Remote team lead"}}), nil, ""},
		{"nowhere", sports, read(Facts{Evidence: cv}, "Ran the billing migration."), nil, "No"},
		{"the knowledge base not read", sports, Facts{Evidence: cv}, nil, ""},
		{"an abbreviation the CV spells out", "Do you have MSP experience?", read(Facts{Evidence: []string{"Ran the help desk of a managed services provider"}}), nil, ""},
		{"an abbreviation two short words spell", "Do you have MSP experience?", read(Facts{Evidence: []string{"MS, PhD in Computer Science"}}), nil, "No"},
		{"a phrase the CV abbreviates", msp, read(Facts{Evidence: []string{"Ran the help desk of an MSP"}}), nil, ""},
		{"a phrase whose words the CV uses apart", msp, read(Facts{Evidence: []string{"Managed the provider integrations of the service desk"}}), nil, "No"},
		{"a phrase the question abbreviates", "Do you have experience in cybersecurity SaaS?", read(Facts{Evidence: []string{"Shipped a software-as-a-service billing product"}}), nil, ""},
		{"a compound spelt apart", "Do you have experience in cybersecurity?", read(Facts{Evidence: cv}, "Taught a cyber security course."), nil, ""},
		{"a compound the question spells apart", "Do you have full stack experience?", read(Facts{Evidence: []string{"Fullstack engineer"}}), nil, ""},
		{"a compound with a short part", "Do you have macOS experience?", read(Facts{Evidence: []string{"Shipped Mac OS tooling"}}), nil, ""},
		{"a compound in another form", "Do you have webhooks experience?", read(Facts{Evidence: []string{"Wrote web hook handlers"}}), nil, ""},
		{"a library without its js ending", "Do you have ReactJS experience?", read(Facts{Evidence: []string{"Built dashboards in React"}}), nil, ""},
		{"a language under its other name", "Do you have Golang experience?", read(Facts{Evidence: cv}), nil, ""},
		{"a language the CV abbreviates", "Do you have JavaScript experience?", read(Facts{Evidence: []string{"Wrote JS widgets"}}), nil, ""},
		{"a platform the question abbreviates", "Do you have K8s experience?", read(Facts{Skills: []SkillFact{{Name: "Kubernetes"}}}), nil, ""},
		{"a database under its short name", "Do you have PostgreSQL experience?", read(Facts{Evidence: []string{"Tuned Postgres queries"}}), nil, ""},
		{"open-source spelt as one word", oss, read(Facts{Evidence: []string{"Opensource maintainer of a parser"}}), nil, ""},
		{"open-source spelt with a hyphen in a note", oss, read(Facts{Evidence: cv}, "Open-source maintainer of a parser."), nil, ""},
		{"the examples a question lists", ossFull, read(Facts{Evidence: cv}, "I've kept the billing service running."), nil, "No"},
		{"open-source whose words the CV uses apart", oss, read(Facts{Evidence: []string{"Kept the source control tidy and stayed open to feedback"}}), nil, "No"},
		{"a longer word a shorter one does not name", riseUp, read(Facts{Evidence: []string{"Ran the service desk for the next release"}}), nil, "No"},
		{"a word with a few letters more", "Do you have experience with React?", read(Facts{Evidence: []string{"Built dashboards in ReactJS"}}), nil, ""},
		{"a word with three letters more", "Do you have experience with SQL?", read(Facts{Evidence: []string{"Shipped an offline cache on SQLite"}}), nil, ""},
		{"another form of the word", "Do you have hiring experience?", read(Facts{Evidence: []string{"Hire and mentor engineers"}}), nil, ""},
		{"a short word inside a longer one", "Do you have experience with Go?", read(Facts{Evidence: []string{"Set quarterly goals for the team"}}), nil, "No"},
		{"a word inside another skill", "Do you have experience with Java?", read(Facts{Evidence: []string{"Built frontends in JavaScript"}}), nil, "No"},
		{"a symbol the CV has", "Do you have experience with C#?", read(Facts{Evidence: []string{"Wrote services in C# and .NET"}}), nil, ""},
		{"a symbol the CV lacks", "Do you have experience with C#?", read(Facts{Evidence: []string{"Wrote embedded C"}}), nil, "No"},
		{"a joined skill nothing mentions", "Do you have experience with Python and Kubernetes?", read(Facts{Skills: []SkillFact{{Name: "Python"}}, Evidence: cv}), nil, "No"},
		{"a willingness beside the experience", "Do you have experience with Rust, and are you willing to learn it?", read(Facts{Evidence: cv}), nil, ""},
		{"a question the policies keep", "Do you have experience with criminal record checks?", read(Facts{Evidence: cv}), nil, ""},
		{"a time word of the question an entry also writes", "Have you used Kafka before?", read(Facts{Evidence: cv}),
			[]Entry{{ID: "employer-history", Kind: KindEmployerHistory, Meta: Meta{Examples: []string{"Have you ever worked for this company before?"}}}}, "No"},
	} {
		s := pfStep(c.q, c.f)
		s.Entries = c.entries
		d, ok := skillScreening(s, screeningEntry(SkillProfileSkill))
		if !ok || c.want == "" && (d.Abstain == nil || d.Answer != "") || c.want != "" && (d.Abstain != nil || d.Answer != c.want) {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

// A threshold the role sets, a number of years or the years, experience or
// skills it requires (the questions route 194 sends here), and whether the
// user meets or matches the role, are answered neither Yes from a recorded
// skill nor No from a declared negative answer or the absence of a mention:
// they stay the user's question (flow round 7, F-10).
func TestAnswerBank_SkillScreening_QuantifiedThresholdNeverYes(t *testing.T) {
	e := screeningEntry(SkillProfileSkill)
	declared := screeningEntry(SkillProfileSkill, func(e *Entry) { e.SkillNegativeAnswer = "No" })
	for _, ent := range []*Entry{e, declared} {
		for _, q := range []string{
			"Do you have 3+ years of professional Python experience?",
			"Do you have at least 5 years of Python experience?",
			"Do you have a minimum of 4 years with Python?",
			"Do you have 2 or more years of Python experience?",
			"Do you have 3-5 years of Python experience?",
			"Do you have more than 5 years of Python experience?",
			"Do you have the required years of relevant experience needed for this role?",
			"Do you have the required software language needed for this position?",
			"Do you have the required years of Python experience needed for this role?",
			"Do you have the experience required for this role?",
			"Do you have experience in a similar role?",
		} {
			d, ok := skillScreening(pfStep(q, alexFacts()), ent)
			if !ok || d.Abstain == nil || d.Answer != "" {
				t.Errorf("negative %q, %q: %+v", ent.SkillNegativeAnswer, q, d)
			}
		}
	}
	d, _ := skillScreening(pfStep("Do you have experience with Python?", alexFacts()), e)
	if d.Answer != "Yes" {
		t.Fatalf("the unquantified screener still answers: %+v", d)
	}
}

func TestAnswerBank_SkillScreening_SymbolExactTermsAndCompactMatch(t *testing.T) {
	cases := []struct {
		name, q, term string
		want          bool
	}{
		{"hash term vs C/C++", "Do you have experience with C/C++?", "C#", false},
		{"hash term vs C#", "Do you have experience with C# or Java?", "C#", true},
		{"plus term", "Do you have experience with C++?", "C++", true},
		{"plus term does not match C", "Do you have experience with C?", "C++", false},
		{"compact form", "Experience with nodejs required", "Node.js", true},
		{"compact form with a dot in the question", "Experience with Next.js", "NextJS", true},
		{"contiguous tokens", "Experience with machine learning models", "machine learning", true},
		{"tokens must be contiguous", "Learning machine tools", "machine learning", false},
		{"short compact form is not enough", "A big idea", "AI", false},
		{"letters inside another word", "Do you have experience with digital marketing?", "Git", false},
		{"letters inside a longer word", "Have you worked in a restaurant?", "REST", false},
		{"letters across two words", "Have you flown as a pilot?", "api", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := skillTermMatchesQuestion(c.q, c.term); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
	t.Run("blacklist answers No through the entry", func(t *testing.T) {
		e := screeningEntry(SkillProfileSkill, func(e *Entry) { e.SkillBlacklistTerms = []string{"C#"} })
		d, _ := skillScreening(pfStep("Do you have experience with C#?", alexFacts()), e)
		if d.Answer != "No" || d.Abstain != nil {
			t.Fatalf("blacklisted: %+v", d)
		}
		d, _ = skillScreening(pfStep("Do you have experience with C/C++ or Python?", alexFacts()), e)
		if d.Answer != "Yes" {
			t.Fatalf("C/C++ is not C#: %+v", d)
		}
		// Joined with "and", the unrecorded C/C++ keeps the answer from Yes, but
		// the blacklist still does not turn it into No.
		d, _ = skillScreening(pfStep("Do you have experience with C/C++ and Python?", alexFacts()), e)
		if d.Answer != "" || d.Abstain == nil {
			t.Fatalf("C/C++ and Python: %+v", d)
		}
	})
}

func TestAnswerBank_SkillScreening_SynonymGroupsExpandProfileSkills(t *testing.T) {
	e := screeningEntry(SkillProfileSkill)
	react := Facts{Skills: []SkillFact{{Name: "React"}}}
	if d, _ := skillScreening(pfStep("Do you have front-end development experience?", react), e); d.Answer != "Yes" {
		t.Fatalf("front-end via React: %+v", d)
	}
	py := Facts{Skills: []SkillFact{{Name: "Python"}}}
	if d, _ := skillScreening(pfStep("Do you have front-end development experience?", py), e); d.Abstain == nil {
		t.Fatalf("a group the user has no skill of adds nothing: %+v", d)
	}
	exp := expandSkillTerms([]string{"GitHub", "React"})
	for _, want := range []string{"version control", "frontend", "web development", "Git"} {
		if !slices.Contains(exp, want) {
			t.Errorf("expanded terms lack %q: %v", want, exp)
		}
	}
	if slices.Contains(expandSkillTerms([]string{"Python"}), "web") {
		t.Fatal("python is in no group")
	}
	// The broad members that turned unrelated questions into Yes are not
	// expansions: a bare "web" (it sits inside "webinar") and "coding samples"
	// (a request for work, not a skill).
	for _, broad := range []string{"web", "coding samples"} {
		if slices.Contains(expandSkillTerms([]string{"React", "JavaScript"}), broad) {
			t.Errorf("%q is an expansion of a front-end skill", broad)
		}
	}
	t.Run("equivalence", func(t *testing.T) {
		if !skillTermsEquivalent("Node.js", "NodeJS") || !skillTermsEquivalent("Spring Boot", "Spring") || skillTermsEquivalent("Java", "JavaScript") {
			t.Fatal("equivalence")
		}
	})
}

func TestAnswerBank_SkillScreening_TermCleaningAndProfileTerms(t *testing.T) {
	got := cleanSkillTerms([]string{"  Python   3 ", "python 3", "", "  ", "Go"})
	if len(got) != 2 || got[0] != "Python 3" || got[1] != "Go" {
		t.Fatalf("cleaned = %v", got)
	}
	e := screeningEntry(SkillProfileSkill)
	d, _ := skillScreening(pfStep("Do you have experience with Go?", alexFacts()), e)
	if d.Answer != "Yes" || !strings.Contains(d.Trace[0], "Go") {
		t.Fatalf("profile skill terms are read at match time: %+v", d)
	}
}

// Compensation, experience and proficiency ladders, "Skill screening": in
// profile_skill mode the answer is Yes only when X appears among the user's
// skills. A question that joins skills with a conjunction names every one of
// them as an X; a disjunction is satisfied by any one.
func TestAnswerBank_SkillScreening_ConjunctionNeedsEverySkill(t *testing.T) {
	pythonOnly := Facts{Skills: []SkillFact{{Name: "Python"}}}
	pythonAndSQL := Facts{Skills: []SkillFact{{Name: "Python"}, {Name: "SQL"}}}
	e := screeningEntry(SkillProfileSkill)
	run := func(t *testing.T, ent *Entry, f Facts, q, wantAnswer string) {
		t.Helper()
		d, ok := skillScreening(pfStep(q, f), ent)
		if !ok {
			t.Fatalf("%q: not handled", q)
		}
		if wantAnswer == "" && (d.Abstain == nil || d.Answer != "") {
			t.Errorf("%q: want an abstain, got %+v", q, d)
		}
		if wantAnswer != "" && (d.Abstain != nil || d.Answer != wantAnswer) {
			t.Errorf("%q: want %q, got %+v", q, wantAnswer, d)
		}
	}

	t.Run("one of the named skills is missing", func(t *testing.T) {
		for _, q := range []string{
			"Are you proficient in both Python and SQL?",
			"Do you have experience with Python and SQL?",
			"Do you have experience with SQL and Python?",
			"Do you have experience with Python as well as SQL?",
			"Do you have experience with Python & SQL?",
			"Do you have experience with Python, Java, and SQL?",
			"Are you proficient in both SQL and Python?",
		} {
			run(t, e, pythonOnly, q, "")
		}
	})
	t.Run("every named skill is recorded", func(t *testing.T) {
		for _, q := range []string{
			"Are you proficient in both Python and SQL?",
			"Do you have experience with Python and SQL?",
			"Do you have experience with Python as well as SQL?",
			"Do you have experience with Python & SQL in a production environment?",
		} {
			run(t, e, pythonAndSQL, q, "Yes")
		}
	})
	t.Run("a skill term of the entry counts as recorded", func(t *testing.T) {
		withTerm := screeningEntry(SkillProfileSkill, func(e *Entry) { e.SkillTerms = []string{"SQL"} })
		run(t, withTerm, pythonOnly, "Are you proficient in both Python and SQL?", "Yes")
	})
	t.Run("a disjunction needs any one", func(t *testing.T) {
		run(t, e, pythonOnly, "Do you have experience with Python or SQL?", "Yes")
		run(t, e, Facts{Skills: []SkillFact{{Name: "SQL"}}}, "Do you have experience with Python or SQL?", "Yes")
		run(t, e, Facts{Skills: []SkillFact{{Name: "Go"}}}, "Do you have experience with Python or SQL?", "")
	})
	t.Run("a conjunction that does not join skills does not block", func(t *testing.T) {
		run(t, e, pythonOnly, "Do you have experience with Python and the ability to work independently?", "Yes")
	})
	t.Run("a declared negative answer is the recorded negative", func(t *testing.T) {
		neg := screeningEntry(SkillProfileSkill, func(e *Entry) { e.SkillNegativeAnswer = "No" })
		run(t, neg, pythonOnly, "Are you proficient in both Python and SQL?", "No")
		run(t, neg, pythonAndSQL, "Are you proficient in both Python and SQL?", "Yes")
	})
	t.Run("manual yes is the user's own assertion", func(t *testing.T) {
		run(t, screeningEntry(SkillManualYes), pythonOnly, "Are you proficient in both Python and SQL?", "Yes")
	})
}
