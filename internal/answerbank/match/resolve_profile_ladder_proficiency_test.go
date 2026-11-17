package match

import "testing"

var pythonLadder = []string{
	"No experience (but willing to learn)",
	"Basic - I have written simple scripts",
	"I am using it in my day to day job",
	"I could teach others",
}

var ragLadder = []string{
	"No experience",
	"Basic - knowledge from classes",
	"Working - I have built RAG pipelines and deployed them into production",
	"Expert - I architect agentic workflows and fine-tuning for others",
}

func profQ(selector, text string, labels ...string) Question {
	return Question{Selector: selector, Text: text, Kind: KindRadioGroup, Options: opts(labels...)}
}

func TestAnswerBank_Proficiency_LadderDetection(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"a real ladder", pythonLadder, true},
		{"two options", []string{"Basic", "Advanced"}, false},
		{"yes no", []string{"Yes", "No"}, false},
		{"one option unranked", []string{"No experience", "Basic", "Somewhere between"}, false},
		{"ranks decrease", []string{"Basic", "Advanced", "Intermediate"}, false},
		{"fewer than three distinct ranks", []string{"No experience", "None", "Basic", "Basic - beginner"}, false},
		{"percentage ranges", []string{"0-25%", "25-50%", "50-75%"}, false},
		{"work settings", []string{"Remote", "Hybrid", "Onsite"}, false},
		// Rain (Ashby, live): mastery is the top rung.
		{"novice to mastery", []string{"Novice", "Intermediate", "Expert", "Mastery"}, true},
		// Dash0 (Ashby, live): the top rung first.
		{"expert down to beginner", []string{"Expert", "Advanced", "Beginner"}, true},
		{"beginner up to expert", []string{"Beginner", "Advanced", "Expert"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := readProficiencyLadder(opts(c.labels...)); ok != c.want {
				t.Fatalf("got %v", ok)
			}
		})
	}
}

func TestAnswerBank_Proficiency_SelectOneRung(t *testing.T) {
	today := pfDate(2026, 10, 1)
	t.Run("a rung that names a recorded skill", func(t *testing.T) {
		f := Facts{Skills: []SkillFact{{Name: "RAG"}}, Evidence: []string{"Built a RAG chatbot at Example Corp"}}
		r := pfOne(t, pfMatcher(), f, today, profQ("s", "How good are you with generative tooling?", ragLadder...))
		if r.Outcome != OutcomeFilled || r.ChosenLabel != ragLadder[2] || r.Source != SourceDerived {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("the top rung is reachable when it names a recorded skill whole", func(t *testing.T) {
		f := Facts{Skills: []SkillFact{{Name: "Agentic workflows"}}}
		r := pfOne(t, pfMatcher(), f, today, profQ("s", "How good are you with generative tooling?", ragLadder...))
		if r.ChosenLabel != ragLadder[3] {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("scale-only rungs: the subject decides, capped at the working rung", func(t *testing.T) {
		r := pfOne(t, pfMatcher(), alexFacts(), today, profQ("s", "How proficient are you in Python?", pythonLadder...))
		if r.ChosenLabel != pythonLadder[2] {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a stack of subjects on a novice-to-mastery scale, capped at the working rung", func(t *testing.T) {
		f := Facts{Skills: []SkillFact{{Name: "Node.js"}}}
		mastery := []string{"Novice", "Intermediate", "Expert", "Mastery"}
		r := pfOne(t, pfMatcher(), f, today, profQ("s", "What is your proficiency level with JS, Node, TypeScript, React, NextJS?", mastery...))
		if r.ChosenLabel != "Intermediate" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("nothing supported abstains", func(t *testing.T) {
		r := pfOne(t, pfMatcher(), alexFacts(), today, profQ("s", "How proficient are you in COBOL?", pythonLadder...))
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("ordinary words of a rung claim nothing", func(t *testing.T) {
		f := Facts{Evidence: []string{"Led large projects and shipped them", "Defined API contracts"}}
		r := pfOne(t, pfMatcher(), f, today, profQ("s", "How good are you with generative tooling?", ragLadder...))
		if r.Outcome != OutcomeAbstained {
			t.Fatalf("result = %+v", r)
		}
		led := []string{"No experience", "Basic", "Working: day to day", "Expert: led large projects"}
		if r = pfOne(t, pfMatcher(), f, today, profQ("s", "How proficient are you in COBOL?", led...)); r.Outcome != OutcomeAbstained {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a rung that repeats the subject is a scale-only rung", func(t *testing.T) {
		labels := []string{"No experience with Python", "Basic Python scripts", "I use Python in my day to day job", "Expert - I could teach Python to others"}
		r := pfOne(t, pfMatcher(), alexFacts(), today, profQ("s", "How proficient are you in Python?", labels...))
		if r.ChosenLabel != labels[2] {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("the words that frame the question are no subject", func(t *testing.T) {
		f := Facts{Evidence: []string{"Proficient in Go and Python", "Rated the best programming language skills of the team"}}
		for _, text := range []string{"How proficient are you in COBOL?", "How would you rate your COBOL programming language skills?"} {
			if r := pfOne(t, pfMatcher(), f, today, profQ("s", text, pythonLadder...)); r.Outcome != OutcomeAbstained {
				t.Fatalf("%q: %+v", text, r)
			}
		}
	})
	t.Run("short evidence matches a whole word only", func(t *testing.T) {
		f := Facts{Evidence: []string{"R"}}
		r := pfOne(t, pfMatcher(), f, today, profQ("s", "How proficient are you in COBOL?", pythonLadder...))
		if r.Outcome != OutcomeAbstained {
			t.Fatalf("result = %+v", r)
		}
		if !evidenceHas([]string{"Spring AI"}, "spring") || evidenceHas([]string{"r"}, "cobol") || !evidenceHas([]string{"PostgreSQL (pgvector)"}, "pgvector") {
			t.Fatal("evidence matching")
		}
	})
	t.Run("a skill is no evidence of a longer one that holds its letters", func(t *testing.T) {
		f := Facts{Skills: []SkillFact{{Name: "Java"}}, Evidence: []string{"Defined API contracts"}}
		r := pfOne(t, pfMatcher(), f, today, profQ("s", "How proficient are you in JavaScript?", pythonLadder...))
		if r.Outcome != OutcomeAbstained {
			t.Fatalf("result = %+v", r)
		}
		if evidenceHas([]string{"Java"}, "javascript") || evidenceHas([]string{"Defined API contracts"}, "fine") ||
			!evidenceHas([]string{"Node.js"}, "node") || !evidenceHas([]string{"Shipped microservices."}, "microservice") {
			t.Fatal("evidence matching")
		}
	})
	t.Run("at most one box of seven checkboxes", func(t *testing.T) {
		labels := []string{"No experience", "Basic", "Beginner level", "Intermediate", "Proficient", "Advanced", "Expert"}
		q := Question{Selector: "s", Kind: KindCheckbox, Text: "How proficient are you in Python?", Options: opts(labels...)}
		r := pfOne(t, pfMatcher(), alexFacts(), today, q)
		if r.ChosenLabel == "" || len(r.ChosenLabels) > 1 {
			t.Fatalf("result = %+v", r)
		}
	})
}

func TestAnswerBank_Proficiency_UserEntryWinsOverLadder(t *testing.T) {
	today := pfDate(2026, 10, 1)
	t.Run("a curated example names the question", func(t *testing.T) {
		q := profQ("s", "How proficient are you in Python?", pythonLadder...)
		e := ent("python-proficiency", "pick-option", withPreferred("Intermediate"), withExamples("How proficient are you in Python?"))
		r := pfOne(t, pfMatcher(e), alexFacts(), today, q)
		if r.Source == SourceDerived {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a pick-option entry with preferred values owns it", func(t *testing.T) {
		q := profQ("s", "Rate your Python proficiency level", pythonLadder...)
		e := ent("python-proficiency", "pick-option", withPreferred("Intermediate"),
			withDescription("Python proficiency level"), withExamples("Python proficiency level"))
		step := pfStep(q.Text, alexFacts())
		step.Q = q
		step.Entries = []Entry{e}
		step.Index = newClassIndex(step.Entries)
		if !userAnswerOwnsQuestion(step) {
			t.Fatal("a pick-option entry with preferred values must own the question")
		}
	})
	t.Run("a blank placeholder is superseded by the ladder", func(t *testing.T) {
		q := profQ("s", "How proficient are you in Python?", pythonLadder...)
		e := ent("python-proficiency", "pick-option", emptyBody(), withExamples("How proficient are you in Python?"))
		e.Placeholder = true
		r := pfOne(t, pfMatcher(e), alexFacts(), today, q)
		if r.ChosenLabel != pythonLadder[2] {
			t.Fatalf("result = %+v", r)
		}
	})
}

// Dash0 (Ashby, queue148): "What is your skill level in kubernetes?" over
// Expert, Advanced, Beginner. Kubernetes is a recorded skill, so the subject
// caps the rung at working, and the ladder's working rung or below is
// Beginner; a subject the evidence never names abstains for the assist lane.
func TestAnswerBank_Proficiency_DescendingLadder(t *testing.T) {
	m := pfMatcher()
	f := Facts{Skills: []SkillFact{{Name: "Kubernetes"}}}
	r := pfOne(t, m, f, pfToday, profQ("k", "What is your skill level in kubernetes?", "Expert", "Advanced", "Beginner"))
	if r.ChosenLabel != "Beginner" {
		t.Fatalf("kubernetes: %+v", r)
	}
	r = pfOne(t, m, f, pfToday, profQ("o", "What is your skill level in OpenTelemetry?", "Expert", "Advanced", "Beginner"))
	if r.Outcome != OutcomeAbstained || r.ChosenLabel != "" {
		t.Fatalf("opentelemetry: %+v", r)
	}
	if o, ok := LeastLadderOption(opts("Expert", "Advanced", "Beginner")); !ok || o.Label != "Beginner" {
		t.Fatalf("least rung %+v %v", o, ok)
	}
}
