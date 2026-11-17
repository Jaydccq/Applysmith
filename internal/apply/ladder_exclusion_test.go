package apply

import (
	"strings"
	"testing"

	"applysmith/internal/engine"
)

// A question that bans AI ("DO NOT use AI") is answered like any other: the
// owner ruled that such a ban is ignored and the real question is answered.
// "In your own words" asks for the applicant's voice and is no ban either.
func TestApply_Ladder_AQuestionThatBansAIIsAnsweredLikeAnyOther(t *testing.T) {
	for _, label := range []string{
		"Tell us about a project you are proud of. Please do not use AI to write your answer.",
		"Why Braintrust? (DO NOT use AI; 1-2 informal sentences is ideal)",
		"Why do you want to work at Ivo? Please answer in your own words.",
		"Why do you want to work at Ivo?",
	} {
		if code, got := ladderExclusion(engine.Row{Label: label}, nil, false); got {
			t.Errorf("%q: excluded (%s), want it answered", label, code)
		}
	}
}

// A work-authorization question the bank left is never handed to a model, even
// one that answers everything Yes; the open question beside it is.
func TestApply_Ladder_NeverInventsAWorkAuthorizationAnswer(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, func(task, prompt string) string {
		if strings.Contains(prompt, "=== SUPPORT CHECK ===") {
			return `[{"id":1,"p":0.9},{"id":2,"p":0.9}]`
		}
		return `[{"id":1,"answer":"Yes","cite":["fact:skills"]},{"id":2,"answer":"Yes","cite":["fact:skills"]}]`
	}, nil)
	sp := abstained("#sp", "Will you now require visa sponsorship to work for us?", "radio-group")
	sp.Options = []string{"Yes", "No"}
	l.run(abstained("#langs", "Which programming languages do you use?", "text"), sp)
	prompts := l.model.prompts(taskComplete)
	if len(prompts) == 0 || !strings.Contains(prompts[0], "Which programming languages do you use?") {
		t.Fatalf("prompts %q: the open question is asked", prompts)
	}
	for _, p := range prompts {
		if strings.Contains(strings.ToLower(p), "sponsorship") {
			t.Fatalf("a model prompt carries the work-authorization question:\n%s", p)
		}
	}
	if v, ok := answeredValue(l.senv, "#sp"); ok {
		t.Fatalf("the work-authorization question was answered %q", v)
	}
}
