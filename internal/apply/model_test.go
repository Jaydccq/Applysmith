package apply

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

const modelWorkspaceID = "0123456789abcdef0123456789abcdef"

// modelRig is a real router over the rig's database, answering from the fake
// provider with a scripted function, and the prompts it received.
type modelRig struct {
	router *llm.Router
	mu     sync.Mutex
	calls  []modelCall
	script func(task, prompt string) string
}

type modelCall struct{ Task, Prompt string }

func newModelRig(t *testing.T, st *store.Store, script func(task, prompt string) string) *modelRig {
	t.Helper()
	v, err := vault.New(modelWorkspaceID, vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	m := &modelRig{script: script}
	fake := llm.NewFake()
	fake.SetGenerator(func(task, prompt string) (string, bool) {
		m.mu.Lock()
		m.calls = append(m.calls, modelCall{task, prompt})
		fn := m.script
		m.mu.Unlock()
		if fn == nil {
			return "", false
		}
		out := fn(task, prompt)
		return out, out != ""
	})
	m.router = llm.New(st, v, llm.WithFake(fake), llm.WithOfflineSimulation())
	return m
}

func (m *modelRig) count(task string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.Task == task {
			n++
		}
	}
	return n
}

func (m *modelRig) prompts(task string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.calls {
		if c.Task == task {
			out = append(out, c.Prompt)
		}
	}
	return out
}

// ladderEnv is a submit rig whose runner has a routed model and the user's
// own facts.
type ladderEnv struct {
	*senv
	model *modelRig
	own   Own
}

func newLadderEnv(t *testing.T, own Own, script func(task, prompt string) string, mod func(*Deps)) *ladderEnv {
	t.Helper()
	l := &ladderEnv{own: own}
	x := newSubmit(t, func(d *Deps) {
		d.Page.Check = CheckTiming{Reread: time.Millisecond, Settle: time.Millisecond}
		d.Page.Matches = &MatchLog{}
		d.Page.Own = func(context.Context) (Own, error) { return l.own, nil }
		if mod != nil {
			mod(d)
		}
	})
	l.senv = x
	l.model = newModelRig(t, x.g.st, script)
	x.g.r.d.Page.Model = l.model.router
	if x.g.r.d.Page.Declines == nil {
		x.g.r.d.Page.Declines = NewDeclines(x.g.r.d.Store, x.g.r.d.Now)
	}
	return l
}

// observe records the matcher's own question for a selector, so the ladder
// reads its options and control kind.
func (l *ladderEnv) observe(sel, text string, kind match.ControlKind, input match.InputType, options ...string) {
	var opts []match.Option
	for _, o := range options {
		opts = append(opts, match.Option{Value: o, Label: o})
	}
	l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
		Results:  []match.Result{{Selector: sel, Outcome: match.OutcomeAbstained}},
		Prepared: []match.Question{{Selector: sel, Text: text, Kind: kind, InputType: input, Options: opts}},
	})
}

// abstained is a required row the matcher abstained on.
func abstained(sel, label, kind string) engine.Row {
	return engine.Row{Selector: sel, Label: label, Required: true, Outcome: "abstained", MatcherReason: "no-entry", Reason: "abstained", Kind: kind}
}

// run checks a page of rows and returns its state.
func (l *ladderEnv) run(rows ...engine.Row) *pageState {
	l.t().Helper()
	ps := &pageState{}
	l.s.check(context.Background(), doneResult(rows...), ps)
	return ps
}

func (l *ladderEnv) t() *testing.T { return l.g.t }

func answeredValue(x *senv, sel string) (string, bool) {
	for _, call := range x.page.answers {
		for _, r := range call {
			if r.Selector == sel {
				return firstNonEmpty(r.OptionLabel, r.Value), true
			}
		}
	}
	return "", false
}

func contains2(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

// Harvey's Ashby form: "What type of work are you most interested in? Select
// 2." is a checkbox group. The grounded rung chooses exactly that many of its
// options and the engine ticks them; a count the label does not ask for and an
// option it does not offer stay open. A group that names no number (8vc's
// "Area of Expertise") takes every option the user's information supports, and
// the Ask Agent never takes such a group.
// Reddit (Greenhouse, live): a multi-value list that asks for "up to three"
// takes its options as a checkbox group does, rather than one joined value
// that is no option. A reply that writes the array as a string is read as the
// array: the model wrote "[\"Compute platforms: ...\", ...]" and the row was
// left to the user.
func TestApply_Ladder_AMultiValueListThatAsksForUpToThreeTakesThem(t *testing.T) {
	opts := []string{"Messaging: event streaming, or asynchronous processing", "Compute platforms: Kubernetes, or infrastructure orchestration",
		"Developer Experience: Build systems, deployment platforms, or developer tooling", "Authentication, authorization, or account infrastructure",
		"Shared backend services, frameworks, libraries, or APIs", "Infrastructure security, security automation, or protective controls"}
	const ask = "Which areas best describe your hands-on engineering experience? Please select up to three options."
	for name, answer := range map[string]string{
		"an array":             `["Compute platforms: Kubernetes, or infrastructure orchestration","Shared backend services, frameworks, libraries, or APIs"]`,
		"an array in a string": `"[\"Compute platforms: Kubernetes, or infrastructure orchestration\",\"Shared backend services, frameworks, libraries, or APIs\"]"`,
	} {
		l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":`+answer+`,"cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
		l.observe("#r", ask, match.KindCustomListbox, match.InputMultiSelect, opts...)
		row := abstained("#r", ask, "custom-listbox")
		row.Options = opts
		l.run(row)
		if p := l.model.prompts(taskComplete); len(p) == 0 || !strings.Contains(p[0], "choose 1 to 3 of the options") {
			t.Fatalf("%s: prompt %q", name, p)
		}
		var got []string
		for _, call := range l.page.answers {
			for _, r := range call {
				if r.Selector == "#r" {
					got = r.OptionLabels
				}
			}
		}
		if !slices.Equal(got, []string{opts[1], opts[4]}) {
			t.Errorf("%s: chose %v", name, got)
		}
	}
}

func TestApply_Ladder_ACheckboxGroupThatAsksForANumberTakesThatMany(t *testing.T) {
	opts := []string{"Backend (SF)", "Frontend (NY)", "Infrastructure (SF)", "Open to Anything"}
	group := func(sel, label string) engine.Row {
		r := abstained(sel, label, "checkbox")
		r.Options = opts
		return r
	}
	const ask = "What type of work are you most interested in? Select 2. This helps us route your application."
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":["Backend (SF)","4"],"cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	l.run(group("#w", ask))
	if p := l.model.prompts(taskComplete); len(p) == 0 || !strings.Contains(p[0], "choose exactly 2 of the options") {
		t.Fatalf("prompt %q", p)
	}
	var got []string
	for _, call := range l.page.answers {
		for _, r := range call {
			if r.Selector == "#w" {
				got = r.OptionLabels
			}
		}
	}
	if !slices.Equal(got, []string{"Backend (SF)", "Open to Anything"}) {
		t.Fatalf("ticked %v", got)
	}
	for name, reply := range map[string]string{
		"one of two":         `[{"id":1,"answer":["Backend (SF)"],"cite":["fact:skills"]}]`,
		"three of two":       `[{"id":1,"answer":["Backend (SF)","Frontend (NY)","Open to Anything"],"cite":["fact:skills"]}]`,
		"not an option":      `[{"id":1,"answer":["Backend (SF)","Mobile (LA)"],"cite":["fact:skills"]}]`,
		"the same one twice": `[{"id":1,"answer":["Backend (SF)","1"],"cite":["fact:skills"]}]`,
	} {
		e := newLadderEnv(t, ladderOwn, script(reply, `[{"id":1,"p":0.9}]`, "[]"), nil)
		e.run(group("#w", ask))
		if _, ok := answeredValue(e.senv, "#w"); ok {
			t.Errorf("%s: written %+v", name, e.page.answers)
		}
	}
	if n, upTo := pickCount("Which teams interest you? Select up to three.", 5); n != 3 || !upTo {
		t.Errorf("up to three: %d %v", n, upTo)
	}
	if n, _ := pickCount("Select all that apply", 5); n != 0 {
		t.Errorf("select all that apply: %d", n)
	}
	all := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":["Backend (SF)","Open to Anything"],"cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	all.run(group("#w", "Area of Expertise"))
	if p := all.model.prompts(taskComplete); len(p) == 0 || !strings.Contains(p[0], "choose every option your information supports") {
		t.Fatalf("prompt %q", p)
	}
	got = nil
	for _, call := range all.page.answers {
		for _, r := range call {
			if r.Selector == "#w" {
				got = r.OptionLabels
			}
		}
	}
	if !slices.Equal(got, []string{"Backend (SF)", "Open to Anything"}) {
		t.Errorf("a group that names no number ticked %v", got)
	}
}
