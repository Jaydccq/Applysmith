package bind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// pageDriver is a tab whose engine is a script: when the host starts a run it
// asks the page's match binding the questions a form would raise, through the
// same JSON the in-page engine sends, and reports what came back.
type pageDriver struct {
	mu        sync.Mutex
	handlers  map[string]browser.BindingHandler
	questions []engine.WireQuestion
	posting   engine.PostingContext
	resp      engine.MatchResponse
	callErr   error
	present   bool
	runID     string
}

const pageOrigin = "https://jobs.example.com"

func (d *pageDriver) Frames(context.Context) ([]browser.Frame, error) {
	return []browser.Frame{{ID: "top", IsTop: true, Origin: pageOrigin, URL: pageOrigin + "/apply"}}, nil
}

func (d *pageDriver) AddBinding(_ string, name string, h browser.BindingHandler) error {
	d.handlers[name] = h
	return nil
}

func (d *pageDriver) SetFiles(context.Context, string, string, string, []string) error { return nil }
func (d *pageDriver) OnTabClosed(func())                                               {}

func (d *pageDriver) Eval(ctx context.Context, frame, _, expr string) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case len(expr) > 10000:
		d.present = true
		return json.RawMessage("null"), nil
	case strings.Contains(expr, "e.status().stamp"):
		if d.present {
			return json.RawMessage(fmt.Sprintf("%q", engine.Stamp())), nil
		}
		return json.RawMessage("null"), nil
	case strings.Contains(expr, "__applysmithEngine.start("):
		call := expr[strings.Index(expr, "__applysmithEngine.start("):]
		var args struct {
			RunID string `json:"runId"`
		}
		_ = json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(call, "__applysmithEngine.start("), ")")), &args)
		d.runID = args.RunID
		d.ask(ctx, frame, args.RunID)
		return json.RawMessage(fmt.Sprintf(`{"started":true,"runId":%q}`, args.RunID)), nil
	case strings.Contains(expr, "e.status():null"):
		return json.RawMessage(fmt.Sprintf(`{"state":"done","runId":%q,"stamp":%q,"summary":{"host":"h","url":"u","rows":[],"counts":{}}}`, d.runID, engine.Stamp())), nil
	case strings.Contains(expr, "e.stop("):
		return json.RawMessage("true"), nil
	case expr == "location.origin":
		return json.RawMessage(fmt.Sprintf("%q", pageOrigin)), nil
	case strings.Contains(expr, "location.href"):
		return json.RawMessage(fmt.Sprintf(`{"href":%q,"origin":%q}`, pageOrigin+"/apply", pageOrigin)), nil
	case expr == "document.readyState":
		return json.RawMessage(`"complete"`), nil
	}
	return nil, fmt.Errorf("unexpected expression %.60q", expr)
}

// ask sends the match call the way the engine does and keeps the reply.
func (d *pageDriver) ask(ctx context.Context, frame, runID string) {
	payload, _ := json.Marshal(engine.MatchRequest{RunID: runID, Host: "jobs.example.com", Posting: d.posting, Questions: d.questions})
	raw, err := d.handlers[engine.BindingMatch](ctx, browser.BindingCall{Frame: browser.Frame{ID: frame, Origin: pageOrigin}, Payload: string(payload)})
	if err != nil {
		d.callErr = err
		return
	}
	var env struct {
		OK     bool                 `json:"ok"`
		Error  string               `json:"error"`
		Result engine.MatchResponse `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		d.callErr = err
		return
	}
	if !env.OK {
		d.callErr = fmt.Errorf("%s", env.Error)
		return
	}
	d.resp = env.Result
}

// The engine's run reaches the real binding through the page's match binding
// (admission, run check, JSON in and out) and the default bank answers the
// form from the profile.
func TestBind_Page_FillReachesTheRealBinding(t *testing.T) {
	if len(engine.Bundle()) == 0 {
		t.Skip("the engine bundle is not generated (make generate)")
	}
	f := newFixture(t, alexSponsored)
	rec := &recorder{}
	b := f.binding(func(d *Deps) { d.Telemetry = rec; d.Writes = rec })
	d := &pageDriver{handlers: map[string]browser.BindingHandler{}, posting: engine.PostingContext{Company: "Example Corp", Title: "Engineer"}}
	d.questions = []engine.WireQuestion{
		{Selector: `[id="fn"]`, Text: "Full name", Kind: "text", InputType: "text", Options: []string{}, Required: true},
		{Selector: `[id="em"]`, Text: "Email address", Kind: "text", InputType: "email", Options: []string{}, Required: true},
		{Selector: `input[type="radio"][name="spons"]`, Text: "Will you now require visa sponsorship to work for us?", Kind: "radio-group", InputType: "yes_no", Options: []string{"Yes", "No"}, Required: true},
		{Selector: `[id="county_1"]`, Text: "County", Kind: "text", InputType: "text", Options: []string{}},
		{Selector: `[id="$$employerCounty_1_RTiCandidate"]`, Text: "County", Kind: "text", InputType: "text", Options: []string{}},
		{Selector: `[id="why"]`, Text: "Why do you want to work here?", Kind: "text", InputType: "long_text", Options: []string{}},
	}
	page, err := engine.NewPage(d, engine.Host{Matcher: b, StageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	applicant := func(context.Context) (json.RawMessage, error) {
		return json.RawMessage(`{"candidate":{"fullName":"Alex Example"}}`), nil
	}
	timing := engine.Timing{Poll: 5 * time.Millisecond, PollTimeout: time.Second, PageDeadline: 5 * time.Second, HungGrace: 50 * time.Millisecond, DocumentWait: 20 * time.Millisecond, ApplicantRetry: time.Millisecond}
	if _, err := page.Fill(context.Background(), engine.FillOptions{Applicant: applicant, Timing: timing}); err != nil {
		t.Fatal(err)
	}
	if d.callErr != nil {
		t.Fatal(d.callErr)
	}
	got := map[string]engine.Resolution{}
	for _, r := range d.resp.Results {
		got[r.Selector] = r
	}
	if len(got) != len(d.questions) {
		t.Fatalf("%d results for %d questions", len(got), len(d.questions))
	}
	if r := got[`[id="fn"]`]; r.Outcome != "filled" || r.Value != "Alex Example" {
		t.Fatalf("name %+v", r)
	}
	if r := got[`[id="em"]`]; r.Outcome != "filled" || r.Value != "alex@example.com" {
		t.Fatalf("email %+v", r)
	}
	if r := got[`input[type="radio"][name="spons"]`]; r.Outcome == "abstained" || r.OptionLabel != "No" {
		t.Fatalf("sponsorship %+v", r)
	}
	if r := got[`[id="county_1"]`]; r.Outcome != "filled" || r.Value != "Durham County" {
		t.Fatalf("county %+v", r)
	}
	if r := got[`[id="$$employerCounty_1_RTiCandidate"]`]; r.Outcome != "abstained" || r.Reason != "section-policy" || !r.Terminal {
		t.Fatalf("employer county %+v", r)
	}
	if r := got[`[id="why"]`]; r.Outcome != "abstained" {
		t.Fatalf("narrative %+v", r)
	}
	if len(rec.events) == 0 || len(rec.writes) < 4 {
		t.Fatalf("events %d writes %d", len(rec.events), len(rec.writes))
	}
	for _, w := range rec.writes {
		if w[0] == "" || w[0] == "run" {
			t.Fatalf("a system write is not tied to the page's run id: %v", w)
		}
	}
}
