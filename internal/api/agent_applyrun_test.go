package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/answerbank"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/workspace"
)

// e2eTab is a kept tab showing one form page, scripted: the page the runner
// reads, the controls it clicks and the rows the engine fills.
type e2eTab struct {
	apply.Tab
	mu     sync.Mutex
	vals   map[string]string
	clicks []string
	kept   bool
	// answers are the resolutions the engine's writer was handed.
	answers []engine.Resolution
}

const (
	e2eWhy    = "#why"
	e2eGender = "#gender"
)

func newE2ETab() *e2eTab { return &e2eTab{vals: map[string]string{}} }

func (t *e2eTab) ID() string { return "tab-1" }

func (t *e2eTab) state() *apply.PageState {
	return &apply.PageState{
		URL: "https://jobs.example.test/apply", Title: "Apply", Text: "Platform Engineer application, based in Raleigh.", Lines: []string{"Platform Engineer"},
		Forms: []apply.FormInfo{{Fields: 2}},
		Inputs: []apply.InputInfo{
			{I: 0, Selector: e2eWhy, Tag: "input", Type: "text", Kind: "text", Label: "Why do you want to work here?", Required: true, Form: 0, Filled: t.vals[e2eWhy] != ""},
			{I: 1, Selector: e2eGender, Tag: "select", Kind: "select", Label: "What is your gender?", Required: true, Form: 0, Options: []string{"Male", "Female", "Decline to answer"}},
		},
		Controls: []apply.Control{
			{I: 100, Tag: "button", Labels: []string{"Next"}, Form: 0},
			{I: 101, Tag: "button", Labels: []string{"Submit application"}, Form: 0},
		},
	}
}

func (t *e2eTab) Inventory(context.Context, string, []apply.FormInfo) (apply.Inventory, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state()
	return apply.Inventory{Inputs: st.Inputs, Forms: st.Forms}, nil
}

func (t *e2eTab) Eval(_ context.Context, expr string, out any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	set := func(v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
	switch {
	case strings.HasSuffix(expr, `("probe",null)`):
		return set(t.state())
	case strings.Contains(expr, `("locate",`):
		var i int
		fmt.Sscanf(expr[strings.LastIndex(expr, `("locate",`)+len(`("locate",`):], "%d", &i)
		for _, c := range t.state().Controls {
			if c.I == i {
				return set(apply.Located{Found: true, Labels: c.Labels, X: float64(i*10 + 1), Y: 1, Hit: true, Visible: true})
			}
		}
		return set(apply.Located{})
	case strings.HasSuffix(expr, `("signature",null)`):
		return set(fmt.Sprintf("%v|%d", t.vals, len(t.clicks)))
	case strings.HasSuffix(expr, `("consent",null)`):
		return set(apply.ConsentGate{})
	}
	return nil
}

func (t *e2eTab) Click(_ context.Context, x, _ float64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.state().Controls {
		if c.I == int(x)/10 {
			t.clicks = append(t.clicks, strings.Join(c.Labels, "/"))
		}
	}
	return nil
}

func (t *e2eTab) Keep() error                                     { t.mu.Lock(); t.kept = true; t.mu.Unlock(); return nil }
func (t *e2eTab) Close(context.Context) error                     { return nil }
func (t *e2eTab) Screenshot(context.Context) ([]byte, error)      { return []byte("png"), nil }
func (t *e2eTab) Frames(context.Context) ([]browser.Frame, error) { return nil, nil }
func (t *e2eTab) Page() apply.Filler                              { return e2ePage{t} }

// e2ePage is the engine's page surface over the tab.
type e2ePage struct{ t *e2eTab }

func (p e2ePage) rows() []engine.Row {
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	why := engine.Row{Selector: e2eWhy, Label: "Why do you want to work here?", Kind: "text", Required: true, Outcome: "abstained", MatcherReason: "no-entry", Reason: "abstained"}
	if v := p.t.vals[e2eWhy]; v != "" {
		why.Outcome, why.Value = "filled", v
	}
	gender := engine.Row{Selector: e2eGender, Label: "What is your gender?", Kind: "select", Required: true, Outcome: "abstained", MatcherReason: "no-entry", Reason: "abstained",
		Options: []string{"Male", "Female", "Decline to answer"}}
	return []engine.Row{why, gender}
}

func (p e2ePage) Fill(context.Context, engine.FillOptions) (*engine.PageResult, error) {
	return &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{Rows: p.rows()}}}, nil
}

func (p e2ePage) Inspect(context.Context) ([]engine.InspectRow, error) {
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	return []engine.InspectRow{
		{Selector: e2eWhy, Label: "Why do you want to work here?", Kind: "text", Required: true, Value: p.t.vals[e2eWhy]},
		{Selector: e2eGender, Label: "What is your gender?", Kind: "select", Required: true},
	}, nil
}

func (p e2ePage) Answer(_ context.Context, _ string, res []engine.Resolution) ([]engine.AnswerResult, error) {
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	var out []engine.AnswerResult
	for _, r := range res {
		p.t.answers = append(p.t.answers, r)
		p.t.vals[r.Selector] = r.Value
		out = append(out, engine.AnswerResult{Selector: r.Selector, Outcome: "filled", Value: r.Value})
	}
	return out, nil
}

func (e2ePage) ObserveSubmissions(context.Context) error { return nil }
func (e2ePage) SubmitObservations(context.Context, time.Duration) ([]engine.SubmitObservation, error) {
	return nil, nil
}
func (e2ePage) BlockedSubmits(context.Context) ([]engine.BlockedSubmit, error) { return nil, nil }

// e2eWindow hands the scripted tab back to a run that takes its kept tab over.
type e2eWindow struct {
	tab       *e2eTab
	mu        sync.Mutex
	reclaimed int
}

func (w *e2eWindow) OpenTab(context.Context, string, engine.Host) (apply.Tab, error) {
	return nil, errors.New("a run that finishes a kept tab opens none")
}

func (w *e2eWindow) Reclaim(context.Context, string, engine.Host) (apply.Tab, error) {
	w.mu.Lock()
	w.reclaimed++
	w.mu.Unlock()
	return w.tab, nil
}

// "Let the agent finish" with the apply tools, end to end (task 1.5): the
// button's turn starts under the apply skill with the skill's tools alone, reads
// the stopped run's page, fills it, answers the one field the checks let it
// answer and is refused for the one they keep for the user, is refused when it
// names Submit, and continues the run, which walks on from the page in its kept
// tab by the Done door and does not submit. The tab is the scripted one, the
// model is a scripted pi, and everything between is the product's own code.
func TestApplyRunTools_AFinishTurnWorksAStoppedRunThroughTheApplyTools(t *testing.T) {
	tab := newE2ETab()
	win := &e2eWindow{tab: tab}
	var mu sync.Mutex
	var turnCtx map[string]any
	clicksBefore := -1
	texts := map[string]string{}
	script := func(p *scriptedPi) {
		status, cat := p.bridge("GET", "/internal/agent/catalog", nil)
		tools, ok := cat["tools"].([]any)
		if status != 200 || !ok {
			return
		}
		var names []string
		for _, tl := range tools {
			names = append(names, tl.(map[string]any)["name"].(string))
		}
		p.bridge("POST", "/internal/agent/hello", map[string]any{"tools": names})
		<-p.prompt
		_, tc := p.bridge("GET", "/internal/agent/turn-context", nil)
		mu.Lock()
		turnCtx = tc
		mu.Unlock()
		callTool := func(id, name string, args map[string]any) {
			args["step"] = "Working the application"
			p.emit(assistantLine([]map[string]any{{"type": "toolCall", "id": id, "name": name, "arguments": args}}, "toolUse"))
			p.emit(map[string]any{"type": "tool_execution_start", "toolCallId": id, "toolName": name, "args": args})
			_, res := p.bridge("POST", "/internal/agent/tool-call", map[string]any{"toolCallId": id, "name": name, "args": args})
			text, _ := res["text"].(string)
			mu.Lock()
			texts[name+"/"+id] = text
			mu.Unlock()
			p.emit(map[string]any{"type": "tool_execution_end", "toolCallId": id, "toolName": name,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, "isError": res["isError"]})
		}
		run := map[string]any{"run_id": "run_kept"}
		with := func(extra map[string]any) map[string]any {
			m := map[string]any{"run_id": "run_kept"}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		callTool("c1", "apply_page", with(nil))
		callTool("c2", "apply_fill", with(nil))
		callTool("c3", "apply_answer", with(map[string]any{"rows": []map[string]any{
			{"id": e2eWhy, "value": "I like small teams that ship."},
			{"id": e2eGender, "value": "Decline to answer", "cite": []string{"jd:based in Raleigh"}},
		}}))
		callTool("c4", "apply_press", with(map[string]any{"label": "Submit application"}))
		tab.mu.Lock()
		mu.Lock()
		clicksBefore = len(tab.clicks)
		mu.Unlock()
		tab.mu.Unlock()
		callTool("c5", "apply_continue", run)
		p.emit(assistantLine([]map[string]any{{"type": "text", "text": "I filled what I could."}}, "stop"))
		p.emit(map[string]any{"type": "agent_settled"})
	}
	h, l := chatHarness(t, script, func(c *Config) {
		c.ApplyWindow = func(context.Context) (apply.Window, error) { return win, nil }
	})
	// The environment the continued run's preflight asks for.
	if _, err := answerbank.New(h.ws, nil).Seed(); err != nil {
		t.Fatal(err)
	}
	profile := strings.Replace(applicantTestProfile, "linkedin.com/in/", "https://linkedin.com/in/", 1) + "resumes:\n  - {id: main, label: Main, file: resumes/main.pdf, default: true}\n"
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.ws.Root, "resumes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws.Root, "resumes", "main.pdf"), []byte(testPDF), 0o600); err != nil {
		t.Fatal(err)
	}
	answeredRun(t, h, "run_kept", "tab-1", "Notice period")

	r := h.chat(h.session(), "POST", "/api/apply/runs/run_kept/takeover", nil)
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	started := decode[wire.TakeoverStarted](t, r)
	readStream(t, h, started.SessionID, "", terminalItem, l.release)
	h.waitIdle(started.SessionID)

	mu.Lock()
	defer mu.Unlock()
	// The turn starts under the apply skill: its block in the system prompt, and
	// the skill's tools alone active.
	prompt, _ := turnCtx["systemPrompt"].(string)
	active, _ := turnCtx["activeTools"].([]any)
	if !strings.Contains(prompt, "Skill loaded: apply") || len(active) == 0 {
		t.Fatalf("turn context: %v", turnCtx)
	}
	for _, a := range active {
		if n := a.(string); n == "save_fact" || n == "evaluate_job" || n == "autofill_application" {
			t.Errorf("%s is active in the finish turn", n)
		}
	}
	for name, want := range map[string][]string{
		"apply_page/c1":     {"Why do you want to work here?", `Controls apply_press may press: "Next"`},
		"apply_fill/c2":     {"2 required still open", "#gender", "left to the user (excluded_self_identification)"},
		"apply_answer/c3":   {"- #why: written", "- #gender: refused: left to the user (excluded_self_identification)", "1 of 2 answer(s) written"},
		"apply_press/c4":    {"Nothing was pressed", "Submit control is pressed only by the submit stage"},
		"apply_continue/c5": {"goes on in its kept tab"},
	} {
		got := texts[name]
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", name, w, got)
			}
		}
	}

	// Before the run goes on, the page holds the one answer the checks let
	// through and nothing was pressed: the Submit the agent named never reached
	// the browser.
	tab.mu.Lock()
	vals := map[string]string{}
	for k, v := range tab.vals {
		vals[k] = v
	}
	tab.mu.Unlock()
	if vals[e2eWhy] != "I like small teams that ship." || vals[e2eGender] != "" {
		t.Errorf("the page holds %v", vals)
	}
	if clicksBefore != 0 {
		t.Errorf("the tab was clicked %d time(s) before the run went on", clicksBefore)
	}

	// The continued run walked on from the kept tab by the Done door, as an
	// agent's press, and ended without pressing Submit.
	waitLong(t, "the run to walk on from its kept tab", func() bool {
		var stages string
		err := h.srv.store.Load().Read().QueryRow(`SELECT stages_json FROM apply_runs WHERE run_id = 'run_kept'`).Scan(&stages)
		return err == nil && strings.Contains(stages, "resumed in the kept tab")
	})
	end := waitEntry(t, h, "run_kept", func(e notify.Entry) bool { return e.State != notify.Working })
	if end.State != notify.NeedsYou || end.Reason != "answer" || !strings.Contains(end.Detail, `"What is your gender?": required and not answered`) {
		t.Errorf("the continued run did not stop on the one field the checks keep for the user: %+v", end)
	}
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if slices.Contains(tab.clicks, "Submit application") {
		t.Errorf("the continued run pressed Submit: %v", tab.clicks)
	}
	if !tab.kept {
		t.Error("the tab was not kept again")
	}
}

// A turn working a run in its kept tab keeps the apply window between its
// calls: each call took the window for itself, so a start confirmed meanwhile
// took it as soon as one call ended, and every later call of the turn was
// refused for that run's whole length. The start waits instead, and begins
// once the turn lets the run go.
func TestApplyRunTools_ATurnWorkingARunKeepsTheWindowBetweenItsCalls(t *testing.T) {
	tab := newE2ETab()
	win := &e2eWindow{tab: tab}
	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(context.Context) (apply.Window, error) { return win, nil }
	})
	answeredRun(t, h, "run_kept", "tab-1", "Notice period")
	ctx := t.Context()
	ag := agentApply{h.srv}
	if text, err := ag.Page(ctx, "run_kept", false); err != nil || strings.HasPrefix(text, "Not done") {
		t.Fatalf("first call: %q %v", text, err)
	}
	a := h.domainOf().apply
	l, err := a.launch(ctx, agent.ApplyRequest{Tool: gates.ToolAutofillApplication, Rows: []agent.ApplyRow{{URL: "https://jobs.example.test/p/9"}},
		ExplicitRequest: true, Caller: gates.CallerDashboard}, true)
	if err != nil || l.ahead != 1 {
		t.Fatalf("a start between two calls of the turn: ahead %d, %v; want it queued", l.ahead, err)
	}
	if text, err := ag.Page(ctx, "run_kept", false); err != nil || strings.HasPrefix(text, "Not done") {
		t.Fatalf("second call: %q %v", text, err)
	}
	if text, err := ag.Leave(ctx, "run_kept", "the notice period is yours to answer"); err != nil || !strings.Contains(text, "handed back") {
		t.Fatalf("leave: %q %v", text, err)
	}
	waitLong(t, "the queued start to begin", func() bool { return len(a.queuedPostings()) == 0 })
}
