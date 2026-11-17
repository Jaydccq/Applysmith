package interview

import (
	"errors"
	"strings"
	"testing"
)

var errTest = errors.New("provider unavailable")

func modelReply(prompts map[int]string, hooks []Hook) func(string) (any, error) {
	return func(string) (any, error) {
		type rp struct {
			Index  int    `json:"index"`
			Prompt string `json:"prompt"`
		}
		out := struct {
			RequirementPrompts []rp   `json:"requirementPrompts"`
			Hooks              []Hook `json:"hooks"`
		}{RequirementPrompts: []rp{}, Hooks: hooks}
		for i := 1; i <= 5; i++ {
			if p, ok := prompts[i]; ok {
				out.RequirementPrompts = append(out.RequirementPrompts, rp{i, p})
			}
		}
		if out.Hooks == nil {
			out.Hooks = []Hook{}
		}
		return out, nil
	}
}

func TestInterview_GoldenModelAssistedPack(t *testing.T) {
	m := &scriptModel{fn: modelReply(
		map[int]string{1: "Walk me through a backend service you owned from design to operation.", 3: "How have you helped other engineers get better at code review?"},
		[]Hook{{Text: "The platform team runs ingestion services in Go on PostgreSQL, which matches your stack.", Sources: []string{"knowledge/companies/example.com.md"}}})}
	f := newFixture(t, fixtureOpts{stories: true, model: m})
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	p.Candidacy = 1
	if len(m.tasks) != 1 || m.tasks[0] != "interview-prep" {
		t.Errorf("tasks = %v; one call under the interview-prep task", m.tasks)
	}
	golden(t, "pack_model.golden.json", p)
}

func TestInterview_UnsourcedHookDropped(t *testing.T) {
	hooks := []Hook{
		{Text: "The company just raised a large round and is hiring fast.", Sources: []string{}},
		{Text: "The company just raised a large round and is hiring fast.", Sources: []string{"https://news.example.com/unknown"}},
		{Text: "Example Corp builds scheduling software for marine survey teams.", Sources: []string{"knowledge/companies/example.com.md"}},
	}
	f := newFixture(t, fixtureOpts{model: &scriptModel{fn: modelReply(nil, hooks)}})
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hooks) != 1 || !strings.Contains(p.Hooks[0].Text, "scheduling software") {
		t.Errorf("hooks = %+v", p.Hooks)
	}
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
}

func TestInterview_HookNotRestatingItsSourceIsDropped(t *testing.T) {
	hooks := []Hook{{Text: "They value radical transparency and weekly offsites.", Sources: []string{"knowledge/companies/example.com.md"}}}
	f := newFixture(t, fixtureOpts{model: &scriptModel{fn: modelReply(nil, hooks)}})
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if p.Generation != GenDeterministic || len(p.Hooks) != 2 || strings.Contains(p.Hooks[0].Text, "radical") {
		t.Errorf("generation %s hooks %+v", p.Generation, p.Hooks)
	}
}

func TestInterview_ModelPromptsAreValidated(t *testing.T) {
	prompts := map[int]string{1: strings.Repeat("x", MaxLineChars+1), 2: "  ", 5: "Out of range index."}
	f := newFixture(t, fixtureOpts{model: &scriptModel{fn: modelReply(prompts, nil)}})
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if p.Generation != GenDeterministic {
		t.Errorf("generation = %s", p.Generation)
	}
	for _, q := range p.Questions {
		if q.Category == "requirement" && !strings.HasPrefix(q.Prompt, "Describe your experience") {
			t.Errorf("an invalid model prompt was used: %q", q.Prompt)
		}
	}
}

func TestInterview_ModelFailureFallsBackAndSaysSo(t *testing.T) {
	f := newFixture(t, fixtureOpts{model: &scriptModel{fn: func(string) (any, error) { return nil, errTest }}})
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if p.Generation != GenDeterministic || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "deterministic wording stands") || len(p.Hooks) != 2 {
		t.Errorf("pack = %+v", p)
	}
}
