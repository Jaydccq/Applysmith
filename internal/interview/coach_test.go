package interview

import (
	"strings"
	"testing"

	"applysmith/internal/llm"
)

const coachResume = `# Alex Example

- Led a team of 5 engineers on the platform migration and cut incidents by 40% across 12 services
- Built the ingestion service used by twelve teams
`

func mock(t *testing.T, f *fixture, stageType, question, answer string) MockTurn {
	t.Helper()
	st := f.stage("coach-"+strings.ReplaceAll(stageType, "_", "-"), stageType, true)
	turn, err := f.svc.RecordMockTurn(f.ctx, f.cand, MockInput{Stage: st.ID, Question: question, Answer: answer})
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestInterview_Coaching_FlagsDifferingNumber(t *testing.T) {
	f := newFixture(t, fixtureOpts{resume: coachResume})
	turn := mock(t, f, TypeTechnical, "Tell me about leading a team.", "I led a team of 12 engineers on the platform migration.")
	if !strings.Contains(turn.Coaching, "12 engineers") || !strings.Contains(turn.Coaching, "5 engineers") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
}

func TestInterview_Coaching_AcceptsFiguresFromSources(t *testing.T) {
	f := newFixture(t, fixtureOpts{resume: coachResume})
	turn := mock(t, f, TypeTechnical, "Tell me about leading a team.", "I led a team of 5 engineers on the platform migration and cut incidents by 40% across 12 services.")
	if !strings.HasPrefix(turn.Coaching, "No problems found") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
}

func TestInterview_Coaching_FlagsNumberNotInSources(t *testing.T) {
	f := newFixture(t, fixtureOpts{resume: coachResume})
	turn := mock(t, f, TypeTechnical, "q", "I led the platform migration that saved 700 hours.")
	if !strings.Contains(turn.Coaching, "No story of yours backs the number 700") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
}

func TestInterview_Coaching_FlagsUnsupportedClaim(t *testing.T) {
	f := newFixture(t, fixtureOpts{resume: coachResume})
	turn := mock(t, f, TypeTechnical, "q", "I architected a payments gateway with fraud scoring for the bank.")
	if !strings.Contains(turn.Coaching, "No support in your submitted résumé or story bank") || !strings.Contains(turn.Coaching, "payments gateway") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
}

func TestInterview_Coaching_MissingResultFigureAndCollectiveLanguage(t *testing.T) {
	f := newFixture(t, fixtureOpts{resume: coachResume})
	answer := "We rebuilt the ingestion service together. Our team chose a queue and we split the work between us. " +
		"We reviewed every change as a group and we shipped it after a long period of testing with our users."
	turn := mock(t, f, TypeBehavioral, "Tell me about a time you shipped something.", answer)
	if !strings.Contains(turn.Coaching, "no result figure") || !strings.Contains(turn.Coaching, "Heavy collective language") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
}

func TestInterview_Coaching_ShortBehavioralAnswerIsNotFlaggedForFigures(t *testing.T) {
	f := newFixture(t, fixtureOpts{resume: coachResume})
	turn := mock(t, f, TypeBehavioral, "Tell me about a time.", "I led the migration.")
	if strings.Contains(turn.Coaching, "states no result figure") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
}

// The coaching names a story only when the bank's own lines back the answer;
// the bank also holds "Mentoring through code review", which this answer does
// not tell.
func TestInterview_Coaching_UsesStoryBank(t *testing.T) {
	f := newFixture(t, fixtureOpts{stories: true})
	turn := mock(t, f, TypeTechnical, "q", "I cut 200 alerts to 15 alerts and the median page time fell to 12 minutes.")
	if !strings.HasPrefix(turn.Coaching, "No problems found") {
		t.Errorf("a figure from a story was flagged: %q", turn.Coaching)
	}
	if !strings.Contains(turn.Coaching, `Your answer draws on your story "Cutting alert noise before launch".`) {
		t.Errorf("coaching = %q", turn.Coaching)
	}
	turn = mock(t, f, TypeTechnical, "q", "I saved 700 hours on the payroll export.")
	if !strings.Contains(turn.Coaching, "No story of yours backs the number 700") || !strings.Contains(turn.Coaching, "draws on no story in your story bank") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
}

func TestInterview_Coaching_ModelNoteIsAdditiveAndOptional(t *testing.T) {
	m := &scriptModel{fn: func(prompt string) (any, error) {
		return map[string]string{"note": "Open with the result, then explain how you got there."}, nil
	}}
	f := newFixture(t, fixtureOpts{resume: coachResume, model: m})
	turn := mock(t, f, TypeTechnical, "q", "I led a team of 12 engineers.")
	if !strings.Contains(turn.Coaching, "12 engineers") || !strings.Contains(turn.Coaching, "Coach: Open with the result") {
		t.Errorf("coaching = %q", turn.Coaching)
	}
	if len(m.tasks) != 1 || m.tasks[0] != "interview-prep" {
		t.Errorf("tasks = %v", m.tasks)
	}
	failing := &scriptModel{fn: func(string) (any, error) { return nil, errTest }}
	g := newFixture(t, fixtureOpts{resume: coachResume, model: failing})
	turn = mock(t, g, TypeTechnical, "q", "I led a team of 12 engineers.")
	if !strings.Contains(turn.Coaching, "12 engineers") || strings.Contains(turn.Coaching, "Coach:") || !strings.Contains(turn.Coaching, "the model did not answer. Get coaching again") {
		t.Errorf("a failing model changed the coaching: %q", turn.Coaching)
	}
	// With no model for the task, the coaching says the note is missing and
	// where a model is chosen.
	unrouted := &scriptModel{fn: func(string) (any, error) { return nil, &llm.Error{Kind: llm.KindNoRoute} }}
	h := newFixture(t, fixtureOpts{resume: coachResume, model: unrouted})
	turn = mock(t, h, TypeTechnical, "q", "I led a team of 12 engineers.")
	if !strings.Contains(turn.Coaching, "12 engineers") || !strings.Contains(turn.Coaching, "No note on how the answer is delivered: interview prep has no model it can use; choose one under Settings, Models.") {
		t.Errorf("an unrouted model's coaching = %q", turn.Coaching)
	}
}
