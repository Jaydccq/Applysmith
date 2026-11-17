package apply

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// activeFake is a window with one scripted tab.
type activeFake struct {
	tab  *scriptTab
	none bool
}

func (w *activeFake) OpenTab(context.Context, string, engine.Host) (Tab, error) {
	panic("a fill of the active tab never opens a tab")
}

func (w *activeFake) ActiveTab(context.Context) (string, error) {
	if w.none {
		return "", ErrNoTab
	}
	return "script", nil
}

func (w *activeFake) Adopt(context.Context, string, engine.Host) (Tab, error) { return w.tab, nil }

func fillForm(fields int) *PageState {
	st := &PageState{URL: "https://jobs.example.test/apply/1?utm=x", Title: "Apply"}
	for i := 0; i < fields; i++ {
		st.Inputs = append(st.Inputs, InputInfo{I: i, Tag: "input", Type: "text"})
	}
	return st
}

func TestApply_FillTab_LooksLikeApplicationFollowsTheEngineRule(t *testing.T) {
	article := &PageState{Inputs: []InputInfo{{Type: "search"}, {Type: "hidden"}, {Type: "text", Furniture: true}}}
	cases := []struct {
		name string
		st   *PageState
		want bool
	}{
		{"a news article with a search box", article, false},
		{"two data-entry controls", fillForm(2), true},
		{"one control the page marks required", &PageState{Inputs: []InputInfo{{Type: "email", Required: true}}}, true},
		{"one optional control", fillForm(1), false},
		{"a form in a visible frame", &PageState{Frames: []FrameInfo{{Src: "https://boards.example.test/embed", Visible: true}}}, true},
	}
	for _, c := range cases {
		if got, why := LooksLikeApplication(c.st); got != c.want || (!got && why == "") {
			t.Errorf("%s: %v %q", c.name, got, why)
		}
	}
}

func summaryOf(rows ...engine.Row) *engine.PageResult {
	return &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{Site: "example", Rows: rows,
		Drafts: json.RawMessage(`[{"question":"Why us?"}]`)}}}
}

func TestApply_FillTab_FillOnceReportsTheGroupsAndNeverNavigatesOrPresses(t *testing.T) {
	g := newRig(t, nil)
	tab := newScriptTab(fillForm(3))
	tab.page = &fakePage{fills: []*engine.PageResult{summaryOf(
		engine.Row{Selector: "#a", Label: "First name", Outcome: "filled", Value: "Alex", Required: true},
		engine.Row{Selector: "#b", Label: "Phone", Outcome: "filled", Value: "5550100", Review: true},
		engine.Row{Selector: "#c", Label: "Pronouns", Outcome: "skipped", Reason: "sensitive"},
		engine.Row{Selector: "#d", Label: "Referral code", Outcome: "abstained", Required: true, Reason: "no-answer"},
	)}, rows: func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#a", Value: "Alex"}, {Selector: "#b", Value: "5550100"}}
	}}
	f, err := g.r.OpenFill(context.Background(), &activeFake{tab: tab}, "script")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.Run(context.Background(), "run_test")
	if err != nil {
		t.Fatal(err)
	}
	names := func(cs []FillControl) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Label)
		}
		return strings.Join(out, ",")
	}
	if names(res.Filled) != "First name" || names(res.Review) != "Phone" || names(res.Skipped) != "Pronouns" || names(res.Missing) != "Referral code" {
		t.Fatalf("groups = %+v", res)
	}
	if res.Missing[0].Reason == "" || res.Drafts != 1 || res.Site != "example" || res.URL != "https://jobs.example.test/apply/1" {
		t.Fatalf("result = %+v", res)
	}
	if len(tab.navs) != 0 || len(tab.clicks) != 0 {
		t.Fatalf("the fill navigated %v or pressed %v", tab.navs, tab.clicks)
	}
}

func TestApply_FillTab_AWrittenValueThePageDoesNotHoldIsMissing(t *testing.T) {
	g := newRig(t, nil)
	tab := newScriptTab(fillForm(2))
	tab.page = &fakePage{fills: []*engine.PageResult{summaryOf(engine.Row{Selector: "#a", Label: "Country", Outcome: "filled", Value: "Canada", Required: true})},
		rows: func() []engine.InspectRow { return []engine.InspectRow{{Selector: "#a", Value: "Select One"}} }}
	f, err := g.r.OpenFill(context.Background(), &activeFake{tab: tab}, "script")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.Run(context.Background(), "run_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Filled) != 0 || len(res.Missing) != 1 || res.Missing[0].Label != "Country" {
		t.Fatalf("result = %+v", res)
	}
}

func TestApply_FillTab_APageTheEngineReadsAsNoApplicationWritesNothing(t *testing.T) {
	g := newRig(t, nil)
	tab := newScriptTab(fillForm(2))
	tab.page = &fakePage{fills: []*engine.PageResult{{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{NotAnApplication: true}}}}}
	f, err := g.r.OpenFill(context.Background(), &activeFake{tab: tab}, "script")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.Run(context.Background(), "run_test")
	if err != nil || !res.NotAnApplication || len(res.Filled)+len(res.Missing) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// deadlinePage is an engine page that notes the deadline each fill is given.
type deadlinePage struct {
	*fakePage
	deadlines []time.Time
}

func (p *deadlinePage) Fill(ctx context.Context, o engine.FillOptions) (*engine.PageResult, error) {
	d, _ := ctx.Deadline()
	p.deadlines = append(p.deadlines, d)
	return p.fakePage.Fill(ctx, o)
}

// pageTab is a scripted tab with its own engine page.
type pageTab struct {
	*scriptTab
	page Filler
}

func (t *pageTab) Page() Filler { return t.page }

// pageWindow is an apply window whose one tab is a pageTab.
type pageWindow struct {
	activeFake
	tab *pageTab
}

func (w *pageWindow) Adopt(context.Context, string, engine.Host) (Tab, error) { return w.tab, nil }

func TestApply_FillTab_TheFillStopsAtTheEnginesPageDeadline(t *testing.T) {
	g := newRig(t, nil)
	page := &deadlinePage{fakePage: &fakePage{}}
	tab := &pageTab{scriptTab: newScriptTab(fillForm(2)), page: page}
	f, err := g.r.OpenFill(context.Background(), &pageWindow{tab: tab}, "script")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := f.Run(context.Background(), "run_test"); err != nil {
		t.Fatal(err)
	}
	end := time.Now()
	if len(page.deadlines) == 0 {
		t.Fatal("the engine was never asked to fill")
	}
	for _, d := range page.deadlines {
		if d.Before(start.Add(FillDeadline)) || d.After(end.Add(FillDeadline)) {
			t.Fatalf("a fill was given %v from the start, want the %v page deadline", d.Sub(start), FillDeadline)
		}
	}
}

// drainPage is the engine page of a tab the person goes on editing after the
// fill: each drain hands over the next scripted result, then nothing.
type drainPage struct {
	*fakePage
	mu     sync.Mutex
	drains []drained
}

type drained struct {
	events []engine.LearningEvent
	err    error
}

func (p *drainPage) DrainEvents(context.Context) ([]engine.LearningEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.drains) == 0 {
		return nil, nil
	}
	d := p.drains[0]
	p.drains = p.drains[1:]
	return d.events, d.err
}

// Once the fill has left the tab to the person, what they type or pick there
// is their own answer: it reaches the learning service under the fill's run
// as the engine collected it, until they leave the page or close the tab, and
// the next fill of the tab takes what is left before it starts.
func TestApply_FillTab_WhatYouChangeAfterTheFillIsPostedUntilYouLeaveThePage(t *testing.T) {
	const runID = "run_0123456789abcdef01234567"
	// A pick in a custom listbox inside a section, as the engine's watch reports it.
	pick := engine.LearningEvent{EventID: "0b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8", Kind: "manual-fill", Host: "jobs.example.test", URL: "https://jobs.example.test/apply/1",
		Selector: `[id="office"]`, Question: "Preferred office", Section: []string{"Application questions"}, ControlKind: "custom-listbox",
		Options: []string{"Durham", "Raleigh"}, Time: t0.UnixMilli(), LabelSource: "label", Required: true, After: "Raleigh", OptionLabel: "Raleigh",
		FillOutcome: "abstained", TrustedInput: true}
	closed := &browser.Error{Code: browser.CodeTabClosed, Message: "tab is closed"}
	cases := []struct {
		name   string
		drains []drained
		// next is the next fill of the tab, which ends the follow at once.
		next bool
		want string
	}{
		{name: "the person leaves the page", drains: []drained{{events: []engine.LearningEvent{pick}}, {err: engine.ErrNoEngine}}, want: "left"},
		{name: "the person closes the tab", drains: []drained{{events: []engine.LearningEvent{pick}}, {err: closed}}, want: "tab-closed"},
		{name: "the next fill of the tab", drains: []drained{{events: []engine.LearningEvent{pick}}}, next: true, want: "stopped"},
	}
	for _, c := range cases {
		p := &posts{}
		g := newRig(t, func(d *Deps) { d.Page.Poster = p })
		page := &drainPage{fakePage: &fakePage{}, drains: c.drains}
		tab := &pageTab{scriptTab: newScriptTab(fillForm(2)), page: page}
		f, err := g.r.OpenFill(context.Background(), &pageWindow{tab: tab}, "script")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Run(context.Background(), runID); err != nil {
			t.Fatal(err)
		}
		before := len(p.events())
		stop := make(chan struct{})
		f.every = time.Millisecond
		if c.next {
			f.every = time.Hour
			close(stop)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		why, n := f.Follow(ctx, stop)
		cancel()
		got := p.events()[before:]
		if why != c.want || n != 1 || len(got) != 1 || got[0].RunID != runID || !reflect.DeepEqual(got[0].LearningEvent, pick) {
			t.Errorf("%s: ended %q after posting %d %+v, want %q and the pick under the fill's run", c.name, why, n, got, c.want)
		}
	}
}
