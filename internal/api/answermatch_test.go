package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"applysmith/internal/answerbank/assist"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/research"
	"applysmith/internal/workspace"
)

const matchProfile = "version: 1\ncandidate:\n  full_name: Alex Example\n  email: alex@example.com\n  address:\n    city: Durham\n    county: Durham County\n    country: US\n"

func matchHarness(t *testing.T) *harness {
	t.Helper()
	h := seededBankHarness(t)
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(matchProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAnswerMatchAnswersFromProfileAndKeepsEmployerRowsBlank(t *testing.T) {
	h := matchHarness(t)
	before := bankDigest(t, h)
	r := h.post("/api/answer-bank/match", wire.AnswerMatchRequest{Host: "jobs.example.com", RunID: h.issueRun(), Questions: []wire.AnswerMatchQuestion{
		{Selector: `[id="fn"]`, Text: "Full name", Kind: "text", InputType: "text", Required: true},
		{Selector: `[id="county"]`, Text: "County", Kind: "text"},
		{Selector: `[id="$$employerCounty_1_RTiCandidate"]`, Text: "County", Kind: "text"},
		{Selector: `[id="why"]`, Text: "Why do you want to work here?", Kind: "text", InputType: "long_text"},
	}})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	res := r.data()["results"].([]any)
	if len(res) != 4 {
		t.Fatalf("%s", r.body)
	}
	item := func(i int) map[string]any { return res[i].(map[string]any) }
	if item(0)["outcome"] != "filled" || item(0)["answer"] != "Alex Example" || item(0)["entryId"] != "applicant-full-name" || item(0)["clientMayOverwrite"] != false {
		t.Fatalf("name %v", item(0))
	}
	if item(1)["answer"] != "Durham County" {
		t.Fatalf("county %v", item(1))
	}
	// The name comes from the identity step, stamped at load: it is not an
	// answer the user wrote into the bank, and neither is an abstain.
	if item(0)["ownAnswer"] != false || item(3)["ownAnswer"] != false {
		t.Fatalf("only the user's own bank answer is marked: %v %v", item(0), item(3))
	}
	if e := item(2); e["outcome"] != "abstained" || e["reason"] != "section-policy" || e["terminal"] != true || e["clientMayOverwrite"] != false {
		t.Fatalf("employer county %v", e)
	}
	if w := item(3); w["outcome"] != "abstained" || w["clientMayOverwrite"] != false {
		t.Fatalf("narrative %v", w)
	}
	if tr, _ := item(0)["trace"].([]any); len(tr) == 0 {
		t.Fatalf("no trace: %v", item(0))
	}
	if bankDigest(t, h) != before {
		t.Fatal("a match changed the bank")
	}
}

// The page scan calls a radio group yes/no only when its labels are exactly
// Yes and No; a sentence-spelled pair arrives as single_select and is still the
// work-authorization question.
func TestAnswerMatchSentenceYesNoRadiosRouteToTheirEntry(t *testing.T) {
	h := matchHarness(t)
	r := h.post("/api/answer-bank/match", wire.AnswerMatchRequest{Host: "jobs.lever.co", RunID: h.issueRun(),
		Posting: wire.AnswerMatchPosting{Location: "Remote, United States"},
		Questions: []wire.AnswerMatchQuestion{{Selector: `input[type="radio"][name="cards[x][field3]"]`, Text: "Work Authorization", Kind: "radio-group",
			InputType: "single_select", Required: true, Options: []wire.AnswerMatchOption{
				{Value: "Yes, I am authorized to work in the United States", Label: "Yes, I am authorized to work in the United States"},
				{Value: "No, I am not authorized to work in the United States", Label: "No, I am not authorized to work in the United States"}}}}})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	item := r.data()["results"].([]any)[0].(map[string]any)
	// The harness profile does not cover the United States, so the posting-country
	// rule rightly holds the answer; the route must still be the work-auth entry
	// rather than an ambiguous abstain.
	if item["entryId"] != "work-auth-us" || item["reason"] == "ambiguous" {
		t.Fatalf("%v", item)
	}
}

func TestAnswerMatchRefusals(t *testing.T) {
	h := matchHarness(t)
	many := make([]wire.AnswerMatchQuestion, 301)
	for i := range many {
		many[i] = wire.AnswerMatchQuestion{Selector: "#q", Text: "Full name", Kind: "text"}
	}
	run := h.issueRun()
	one := []wire.AnswerMatchQuestion{{Selector: "#q", Text: "Full name", Kind: "text"}}
	cases := []struct {
		name, field string
		req         wire.AnswerMatchRequest
	}{
		{"no run id", "runId", wire.AnswerMatchRequest{Host: "jobs.example.com", Questions: one}},
		{"a run id the server never issued", "runId", wire.AnswerMatchRequest{Host: "jobs.example.com", RunID: "run_0123456789abcdef01234567", Questions: one}},
		{"301 questions", "", wire.AnswerMatchRequest{Host: "jobs.example.com", RunID: run, Questions: many}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.post("/api/answer-bank/match", c.req)
			if r.StatusCode != 422 || r.errCode() != "validation_failed" {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if c.field != "" && !strings.Contains(string(r.body), `"field":"`+c.field+`"`) {
				t.Fatalf("the refusal names the field %q: %s", c.field, r.body)
			}
		})
	}
}

func TestAnswerMatchBrokenBankIsConfigInvalid(t *testing.T) {
	h := matchHarness(t)
	if err := os.WriteFile(h.ws.Path(workspace.FileAnswerBank), []byte(strings.Repeat("version: 7\n", 1)+"entries: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.post("/api/answer-bank/match", wire.AnswerMatchRequest{Host: "jobs.example.com", RunID: h.issueRun(), Questions: []wire.AnswerMatchQuestion{{Selector: "#q", Text: "Full name", Kind: "text"}}})
	if r.StatusCode != 422 || r.errCode() != "config_invalid" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

type liveModel struct {
	reply string
	calls int
}

func (m *liveModel) NativeSearch(context.Context, string) bool { return false }

func (m *liveModel) Complete(context.Context, llm.Request) (*llm.Response, error) {
	m.calls++
	return &llm.Response{Text: m.reply}, nil
}

type liveSearch struct{}

func (liveSearch) Search(context.Context, string, int) ([]research.Hit, error) {
	return []research.Hit{{URL: "https://puzzle.example/today", Title: "Today", Text: "The puzzle answer is CRANE."}}, nil
}

func TestAnswerMatchLiveFactsComeFromTheResearchServiceUnderItsCitationRules(t *testing.T) {
	m := &liveModel{}
	svc := &research.Service{Model: m, Search: func() research.Searcher { return liveSearch{} }}
	lf := liveFacts{svc}
	ctx := context.Background()
	m.reply = "The answer is CRANE [1]."
	got, err := lf.Lookup(ctx, "What is today's puzzle answer?")
	if err != nil || got.Text != m.reply || len(got.Sources) != 1 || got.Sources[0].URL != "https://puzzle.example/today" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, reply := range []string{"The answer is CRANE and the clue is a bird", "NO_ANSWER", "CRANE [4]"} {
		m.reply = reply
		if _, err := lf.Lookup(ctx, "What is today's puzzle answer?"); !errors.Is(err, assist.ErrLiveFactRefused) {
			t.Errorf("%q: %v", reply, err)
		}
	}
	none := liveFacts{&research.Service{Model: m, Search: func() research.Searcher { return nil }}}
	before := m.calls
	if _, err := none.Lookup(ctx, "What is today's puzzle answer?"); !errors.Is(err, assist.ErrLiveFactNoSource) || none.Available(ctx) || m.calls != before {
		t.Fatalf("no source: %v, %d calls", err, m.calls-before)
	}
}
