package bind

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/llm"
	"applysmith/internal/workspace"
)

// downModel has a route for every task and a provider that always fails.
type downModel struct{}

func (downModel) Resolve(context.Context, string) (llm.Resolved, error) { return llm.Resolved{}, nil }
func (downModel) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return nil, errors.New("provider down")
}

func TestBind_Run_OffersAbstainsToTheModelLanes(t *testing.T) {
	f := newFixture(t, alexProfile)
	q := match.Question{Selector: "#why", Text: "Why do you want to work at Example Corp?", Kind: match.KindText, InputType: match.InputLongText}
	req := Request{RunID: "run-a", Host: "jobs.example.com", Posting: Posting{Company: "Example Corp"}, Questions: []match.Question{q}}

	plain, err := f.binding(nil).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	withLanes, err := f.binding(func(d *Deps) { d.Assist = assist.New(assist.Deps{Model: downModel{}, Now: f.clock}) }).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	a, b := plain.Results[0], withLanes.Results[0]
	if a.Outcome != match.OutcomeAbstained || b.Outcome != match.OutcomeAbstained {
		t.Fatalf("both must abstain: %+v / %+v", a, b)
	}
	if a.Reason == b.Reason || b.Reason != match.ReasonUpstreamError {
		t.Fatalf("the lanes were not offered the abstain: matcher %q, with lanes %q", a.Reason, b.Reason)
	}
}

// countingModel has a route for every task and counts the calls it receives.
type countingModel struct{ calls int }

func (*countingModel) Resolve(context.Context, string) (llm.Resolved, error) {
	return llm.Resolved{}, nil
}

func (m *countingModel) Complete(context.Context, llm.Request) (*llm.Response, error) {
	m.calls++
	return nil, errors.New("provider down")
}

func TestBind_Preview_CallsNoModelAndRecordsNothing(t *testing.T) {
	f := newFixture(t, alexProfile)
	q := match.Question{Selector: "#why", Text: "Why do you want to work at Example Corp?", Kind: match.KindText, InputType: match.InputLongText}
	req := Request{RunID: "run-a", Host: "jobs.example.com", Posting: Posting{Company: "Example Corp"}, Questions: []match.Question{q}}
	model, rec := &countingModel{}, &recorder{}
	b := f.binding(func(d *Deps) {
		d.Assist = assist.New(assist.Deps{Model: model, Now: f.clock})
		d.Telemetry = rec
	})
	before, err := os.ReadFile(f.ws.Path(workspace.FileAnswerBank))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := b.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if model.calls == 0 || len(rec.events) == 0 {
		t.Fatalf("a run offers the abstain to the model lanes and records usage: %d calls, %d events", model.calls, len(rec.events))
	}
	model.calls, rec.events = 0, nil

	out, err := b.Preview(context.Background(), req)
	if err != nil || len(out.Results) != 1 || out.Results[0].Outcome != match.OutcomeAbstained {
		t.Fatalf("%+v %v", out, err)
	}
	if out.Results[0].Reason == match.ReasonUpstreamError || model.calls != 0 || len(rec.events) != 0 || len(rec.writes) != 0 {
		t.Fatalf("a preview ran a lane or recorded: reason %q, %d calls, %d events, %d writes", out.Results[0].Reason, model.calls, len(rec.events), len(rec.writes))
	}
	after, _ := os.ReadFile(f.ws.Path(workspace.FileAnswerBank))
	if string(before) != string(after) {
		t.Fatal("a preview changed the bank file")
	}
}

// recognizingModel names one entry for every recognition prompt and fails
// every other call.
type recognizingModel struct{ reply string }

func (recognizingModel) Resolve(context.Context, string) (llm.Resolved, error) {
	return llm.Resolved{}, nil
}
func (m recognizingModel) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	if strings.Contains(req.Prompt, "=== SAVED ANSWERS") {
		return &llm.Response{Text: m.reply}, nil
	}
	return nil, errors.New("provider down")
}

// The recognition lane runs inside Run with the matcher's own pinned
// resolution; its fill is never the user's own answer to the engine, and its
// usage event keeps the entry id on the model lane, which is what lets the
// candidates flow propose the question as that entry's example.
func TestBind_Run_RecognisedEntryIsPinnedThroughTheMatcher(t *testing.T) {
	f := newFixture(t, alexProfile)
	f.write(t, workspace.FileAnswerBank, `version: 1
entries:
  - id: relatives-at-employer
    description: "Whether the applicant has relatives employed by the hiring company"
    kind: fixed
    category: compliance
    input_types: [yes_no]
    examples:
      - "Do you have relatives working here?"
    answer: "No"
`)
	ctx := context.Background()
	on := true
	if _, err := f.bank.PatchSettings(ctx, answerbank.SettingsPatch{LLMFallbackEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	q := match.Question{Selector: "#rel", Text: "Are you a relative of any current Example Corp associate?", Kind: match.KindRadioGroup,
		Options: []match.Option{{Value: "y", Label: "Yes"}, {Value: "n", Label: "No"}}, Required: true}
	req := Request{RunID: "run-a", Host: "jobs.example.com", Posting: Posting{Company: "Example Corp"}, Questions: []match.Question{q}}
	rec := &recorder{}
	b := f.binding(func(d *Deps) {
		d.Assist = assist.New(assist.Deps{Model: recognizingModel{`{"entry": "relatives-at-employer", "option": "No"}`}, Now: f.clock})
		d.Telemetry = rec
	})
	out, err := b.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	r := out.Results[0]
	if r.Outcome != match.OutcomeFilledReview || r.Source != match.SourceModelEntry || r.ChosenLabel != "No" || r.EntryID != "relatives-at-employer" {
		t.Fatalf("recognised entry not resolved: %+v", r)
	}
	if out.Own[0] {
		t.Fatal("a recognised entry is flagged as the user's own answer")
	}
	found := false
	for _, ev := range rec.events {
		found = found || ev.Kind == learn.KindMatch && ev.Lane == learn.LaneModelOption && ev.EntryID == "relatives-at-employer"
	}
	if !found {
		t.Fatalf("no model-lane match event with the entry id: %+v", rec.events)
	}
}
