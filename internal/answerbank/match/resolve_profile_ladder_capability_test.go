package match

import "testing"

var aiScale = []string{
	"No AI experience.",
	"I have experimented with AI tools but have not built applications",
	"I have built AI prototypes or side projects",
	"I have shipped AI features to customers in production",
	"I have led multiple production AI systems",
}

const aiScaleQuestion = "Which best describes your hands-on experience building AI-powered applications or solutions?"

func factsWithLevel(name, level string) Facts {
	return Facts{Skills: []SkillFact{{Name: name, Level: level}}}
}

// The four recorded skill levels map onto the five rungs of a scale; the bottom
// rung (no exposure) is never a recorded level, and an unknown word names none.
func TestAnswerBank_Capability_RecordedLevelsMapOntoRungs(t *testing.T) {
	for level, want := range map[string]capabilityRung{
		"beginner": rungExplored, "intermediate": rungProjects, "advanced": rungProduction, "expert": rungLed,
	} {
		if got, ok := levelRung(level); !ok || got != want {
			t.Errorf("%s: %v %v", level, got, ok)
		}
	}
	for _, level := range []string{"", "none", "Expert", "guru"} {
		if _, ok := levelRung(level); ok {
			t.Errorf("%q names a rung", level)
		}
	}
}

func TestAnswerBank_Capability_RungCues(t *testing.T) {
	cases := []struct {
		label string
		want  capabilityRung
		ok    bool
	}{
		{"No AI experience.", rungNone, true},
		{"None", rungNone, true},
		{"I have not used it", rungNone, true},
		{"I have experimented with AI tools", rungExplored, true},
		{"I am curious and have read about it", rungExplored, true},
		{"I use AI tools occasionally for personal or academic work", rungProjects, true},
		{"Occasionally", rungProjects, true},
		{"I built a side project", rungProjects, true},
		{"I use it daily in production", rungProduction, true},
		{"I shipped it to customers", rungProduction, true},
		{"I led the design of multiple production systems", rungLed, true},
		{"I mentor others", rungLed, true},
		{"Somewhat comfortable", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := capabilityRungOf(c.label)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got %v %v, want %v %v", c.label, got, ok, c.want, c.ok)
		}
	}
}

func TestAnswerBank_Capability_AscendingLadderNeverAbove(t *testing.T) {
	cases := []struct {
		level string
		want  string
	}{
		{"beginner", aiScale[1]},
		{"intermediate", aiScale[2]},
		{"advanced", aiScale[3]},
		{"expert", aiScale[4]},
	}
	for _, c := range cases {
		t.Run(c.level, func(t *testing.T) {
			q := pfSelect("s", aiScaleQuestion, aiScale...)
			r := pfOne(t, pfMatcher(), factsWithLevel("AI", c.level), pfDate(2026, 10, 1), q)
			if r.Outcome != OutcomeFilled || r.ChosenLabel != c.want || r.Source != SourceDerived || r.AnswerKey != "self_assessment" {
				t.Fatalf("result = %+v", r)
			}
		})
	}
	t.Run("the highest rung below when the exact rung is not rendered", func(t *testing.T) {
		q := pfSelect("s", aiScaleQuestion, aiScale[0], aiScale[2], aiScale[4])
		r := pfOne(t, pfMatcher(), factsWithLevel("AI", "advanced"), pfDate(2026, 10, 1), q)
		if r.ChosenLabel != aiScale[2] {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("every rung above the recorded level abstains", func(t *testing.T) {
		q := pfSelect("s", aiScaleQuestion, aiScale[2], aiScale[3], aiScale[4])
		r := pfOne(t, pfMatcher(), factsWithLevel("AI", "beginner"), pfDate(2026, 10, 1), q)
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonInputMismatch {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("no recorded level abstains", func(t *testing.T) {
		q := pfSelect("s", aiScaleQuestion, aiScale...)
		for _, f := range []Facts{factsWithLevel("AI", ""), factsWithLevel("Python", "expert"), {}} {
			r := pfOne(t, pfMatcher(), f, pfDate(2026, 10, 1), q)
			if r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
				t.Fatalf("facts %+v: result = %+v", f, r)
			}
		}
	})
	t.Run("a set that does not ascend is left to other lanes", func(t *testing.T) {
		q := pfSelect("s", aiScaleQuestion, aiScale[3], aiScale[1], aiScale[4])
		r := pfOne(t, pfMatcher(), factsWithLevel("AI", "expert"), pfDate(2026, 10, 1), q)
		if r.Source == SourceDerived {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("two skills with different levels are ambiguous", func(t *testing.T) {
		f := Facts{Skills: []SkillFact{{Name: "AI", Level: "advanced"}, {Name: "LLM", Level: "beginner"}}}
		q := pfSelect("s", "Which best describes your experience with AI and LLM tools?", aiScale...)
		r := pfOne(t, pfMatcher(), f, pfDate(2026, 10, 1), q)
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("an unrelated Yes/No entry never picks the rung", func(t *testing.T) {
		e := ent("application-completed-by-ai", "fixed", withAnswer("No"), withExamples("Was this application completed by AI?"))
		q := pfSelect("s", aiScaleQuestion, aiScale...)
		r := pfOne(t, pfMatcher(e), factsWithLevel("AI", "expert"), pfDate(2026, 10, 1), q)
		if r.ChosenLabel != aiScale[4] {
			t.Fatalf("result = %+v", r)
		}
	})
}

// A self-rating's least claimed statement is the first of a list that runs
// upwards and the last of one that runs from the strongest down; statements
// that run both ways are left.
func TestAnswerBank_LeastClaimedRating_ReadsTheOrder(t *testing.T) {
	down := []string{"I have led multiple production systems", "I have shipped features to customers in production",
		"I have built prototypes or side projects", "I have experimented but have not built applications"}
	up := []string{down[3], down[2], down[1], down[0]}
	mixed := []string{down[2], down[0], down[3]}
	for _, c := range []struct {
		want   string
		labels []string
	}{{down[3], down}, {up[0], up}, {"", mixed}} {
		q := Question{Selector: "s", Kind: KindRadioGroup, Text: "Rate your ability to build data systems:", Options: opts(c.labels...)}
		r := pfOne(t, pfMatcher(), Facts{}, pfDate(2026, 10, 1), q)
		if r.ChosenLabel != c.want || c.want != "" && r.Outcome != OutcomeFilledReview {
			t.Errorf("want %q over %v: %+v", c.want, c.labels, r)
		}
	}
}
