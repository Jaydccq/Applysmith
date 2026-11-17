package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
)

// The fixture profile is the spec persona; the matcher is deterministic and
// answers from it, so no real answer bank is involved.

const applicantJSON = `{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example",
"email":"alex@example.com","phone":"+1 555 0100","phoneCountryCode":"+1","phoneNational":"5550100",
"address":{"line1":"1 Example Street","city":"Durham","state":"NC","postalCode":"27701","country":"United States"}},
"links":{"linkedin":"https://www.linkedin.com/in/alex-example"}}`

func fixtureApplicant(context.Context) (json.RawMessage, error) {
	return json.RawMessage(applicantJSON), nil
}

// bankAnswers maps a lower-case question to the matcher's resolution.
var bankAnswers = map[string]Resolution{
	"are you legally authorized to work in the united states?": {Outcome: "filled", Value: "Yes"},
	"how did you hear about us?":                               {Outcome: "filled", Value: "Job board"},
	"which languages do you use? select all that apply":        {Outcome: "filled", Value: "Go", OptionLabels: []string{"Go", "Rust"}},
	"years of experience with go":                              {Outcome: "filled", Value: "6"},
	"highest education level":                                  {Outcome: "filled", Value: "Bachelor's degree"},
	"do you require visa sponsorship?":                         {Outcome: "filled", Value: "No"},
	"linkedin profile url":                                     {Outcome: "filled", Value: "https://www.linkedin.com/in/alex-example"},
}

// fakeMatcher records every request and answers from bankAnswers.
type fakeMatcher struct {
	mu       sync.Mutex
	requests []MatchRequest
	// before runs at the start of each call; a test blocks or acts there.
	before func(ctx context.Context, req MatchRequest) error
}

func (m *fakeMatcher) Match(ctx context.Context, req MatchRequest) (MatchResponse, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	before := m.before
	m.mu.Unlock()
	if before != nil {
		if err := before(ctx, req); err != nil {
			return MatchResponse{}, err
		}
	}
	var out MatchResponse
	for _, q := range req.Questions {
		res, ok := bankAnswers[strings.ToLower(q.Text)]
		if !ok {
			res = Resolution{Outcome: "abstained", Reason: "no-entry", ClientMayOverwrite: true}
		}
		res.Selector = q.Selector
		out.Results = append(out.Results, res)
	}
	return out, nil
}

func (m *fakeMatcher) calls() []MatchRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MatchRequest(nil), m.requests...)
}

// fakeResumes serves the synthetic PDF under testdata.
type fakeResumes struct {
	mu       sync.Mutex
	requests []ResumeRequest
}

func (r *fakeResumes) Resume(_ context.Context, req ResumeRequest) (*ResumePayload, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	if req.Document != "resume" {
		return nil, nil
	}
	b, err := os.ReadFile("testdata/resume-alex-example.pdf")
	if err != nil {
		return nil, err
	}
	return &ResumePayload{FileName: "resume-alex-example.pdf", MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(b)}, nil
}

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
