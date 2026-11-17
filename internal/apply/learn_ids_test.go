package apply

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// An event from a form inside a blank or srcdoc frame names no host (its
// location is about:) and takes the page's, or the learning service refuses
// it; an event from a frame with its own address keeps that host.
func TestApply_Learn_AFrameEventWithoutAHostTakesThePagesHost(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	res := doneResult()
	frame := func(ev engine.LearningEvent) engine.FrameResult {
		return engine.FrameResult{Status: &engine.Status{State: engine.StateDone, Summary: &engine.Summary{Events: []engine.LearningEvent{ev}}}}
	}
	res.Frames = []engine.FrameResult{
		frame(engine.LearningEvent{EventID: "srcdoc", Kind: "uncertainty", Question: "Why this team?", ControlKind: "text", Reason: "no-entry"}),
		frame(engine.LearningEvent{EventID: "own", Kind: "uncertainty", Host: "forms.example.test", Question: "Portfolio link", ControlKind: "text", Reason: "no-entry"}),
	}
	l.s.collectEvents(res, &pageState{report: PageReport{URL: "https://careers.example.test/apply?step=2", Step: 2}}, nil)
	hosts := map[string]string{}
	for _, ev := range l.s.events {
		hosts[ev.EventID] = ev.Host
	}
	if len(hosts) != 2 || hosts["srcdoc"] != "careers.example.test" || hosts["own"] != "forms.example.test" {
		t.Errorf("hosts = %v", hosts)
	}
}

// A list the computer-use rung opened names, on its learning event, the
// options the rung read, as the engine's event names those of a list it opened
// (CO-1); never options the rung did not read, nor ones the learning service
// withholds.
func TestApply_Learn_AListTheRungOpenedNamesTheOptionsItRead(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	res := doneResult()
	for _, c := range []struct{ sel, question string }{
		{"#sp", "Will you now or in the future require visa sponsorship?"},
		{"#who", "Which of these describes you?"},
		{"#src", "How did you hear about us?"},
	} {
		l.g.r.d.Page.Matches.Observe(bind.Request{RunID: res.RunID}, bind.Outcome{
			Results:  []match.Result{{Selector: c.sel, Outcome: match.OutcomeAbstained}},
			Prepared: []match.Question{{Selector: c.sel, Options: []match.Option{{Label: "LinkedIn"}}}},
		})
		res.Top.Summary.Events = append(res.Top.Summary.Events, engine.LearningEvent{Kind: "uncertainty", Selector: c.sel, Question: c.question, ControlKind: "custom-listbox"})
	}
	l.g.r.d.Page.Matches.list(res.RunID, "#sp", []string{"Yes", "No"})
	l.g.r.d.Page.Matches.list(res.RunID, "#who", []string{"Male", "Female", "I decline to self-identify"})
	l.s.collectEvents(res, &pageState{}, nil)
	want := map[string][]string{"#sp": {"Yes", "No"}, "#who": nil, "#src": nil}
	for _, ev := range l.s.events {
		if !slices.Equal(ev.Options, want[ev.Selector]) {
			t.Errorf("event of %s names %q, want %q", ev.Selector, ev.Options, want[ev.Selector])
		}
	}
	if len(l.s.events) != len(want) {
		t.Fatalf("%d events, want %d", len(l.s.events), len(want))
	}
}

// A person's edit after a run names the answer-bank entry whose answer the run
// wrote into that control, so the learning service counts it against that
// entry. The follow after a fill tells the control by the selector the engine
// names; the kept-tab recorder, whose entries name none, by its page and
// question.
func TestApply_Learn_AHandEditNamesTheEntryWhoseAnswerTheRunWrote(t *testing.T) {
	const page = "https://apply.example.test/form"
	text := func(sel, q, v string) engine.Row {
		return engine.Row{Selector: sel, Label: q, Kind: "text", Outcome: "filled", Value: v}
	}
	bank := func(sel, entry, answer string) match.Result {
		return match.Result{Selector: sel, Outcome: match.OutcomeFilled, Answer: answer, EntryID: entry, Source: match.SourceEntry}
	}
	// The page as the run left it, and the matcher's last answer for each of
	// its controls.
	rows := []engine.Row{
		text(`[id="go"]`, "Years of experience with Go", "3"),
		// A model rung of the check wrote it: the bank holds no entry.
		text(`[id="py"]`, "Years of experience with Python", "3"),
		// The check's first rung wrote the bank's answer, as the option it
		// picked, after the engine skipped the control.
		{Selector: `[id="start"]`, Label: "Earliest start date", Kind: "select", Outcome: "filled", Value: "2 weeks", OptionLabel: "In two weeks"},
		// The bank's pick did not commit; a model rung of the check picked another.
		{Selector: `[id="office"]`, Label: "Office location", Kind: "select", Outcome: "filled", Value: "Durham", OptionLabel: "Durham"},
		// The matcher's match to an entry was too weak; the profile wrote it.
		text(`[id="city"]`, "City", "Durham"),
		// The page held the bank's answer already.
		{Selector: `[id="employer"]`, Label: "Current employer", Kind: "text", Outcome: "prefilled", Value: "Northwind Example"},
		text(`[id="job-start"]`, "Start date", "2019-09"),
		text(`[id="school-start"]`, "Start date", "2019-09"),
		{Selector: `[id="relocate"]`, Label: "Willing to relocate?", Kind: "select", Outcome: "filled", Value: "Yes", OptionLabel: "Yes"},
	}
	answered := map[string]string{`[id="py"]`: ProvenanceCheck, `[id="start"]`: ProvenanceCheck, `[id="office"]`: ProvenanceCheck}
	results := []match.Result{
		bank(`[id="go"]`, "years-go", "3"),
		{Selector: `[id="py"]`, Outcome: match.OutcomeAbstained, Reason: match.ReasonNoEntry},
		{Selector: `[id="start"]`, Outcome: match.OutcomeFilled, Answer: "2 weeks", ChosenLabel: "In two weeks", EntryID: "start-date", Source: match.SourceEntry},
		{Selector: `[id="office"]`, Outcome: match.OutcomeFilled, Answer: "Raleigh", ChosenLabel: "Raleigh", EntryID: "office", Source: match.SourceEntry},
		{Selector: `[id="city"]`, Outcome: match.OutcomeAbstained, Reason: match.ReasonLowConfidence, EntryID: "city"},
		bank(`[id="employer"]`, "employer", "Northwind Example"),
		bank(`[id="job-start"]`, "job-start", "2019-09"),
		bank(`[id="school-start"]`, "school-start", "2019-09"),
		{Selector: `[id="relocate"]`, Outcome: match.OutcomeFilled, Answer: "Yes", ChosenLabel: "Yes", EntryID: "relocate", Source: match.SourceEntry},
	}
	// edit is a change the engine's watch drained, as it reports one.
	edit := func(id, sel, before, after string) engine.LearningEvent {
		r := rowFor(rows, sel)
		return engine.LearningEvent{EventID: id, Kind: manualFillKind, Host: "apply.example.test", URL: page, Selector: sel, Question: r.Label, ControlKind: r.Kind,
			Time: t0.UnixMilli(), LabelSource: "label", Before: before, After: after, PriorValue: before, FillOutcome: "filled", TrustedInput: true}
	}
	relocate := edit("4d5e6f70-8192-4a3b-8c4d-5e6f70819203", `[id="relocate"]`, "Yes", "Prefer not to say")
	relocate.InputType, relocate.OptionLabel, relocate.Options = "select-one", "Prefer not to say", []string{"Yes", "No", "Prefer not to say"}
	pick := func(q, value, prior string, options ...string) recorderEntry {
		e := entry(1, q, "select", value, prior)
		e.InputType, e.OptionLabel, e.Options = "select-one", value, options
		return e
	}
	elsewhere := entry(1, "Years of experience with Go", "text", "5", "3")
	elsewhere.URL = "https://apply.example.test/other"
	cases := []struct {
		name string
		// drains are what the engine's watch hands over after a toolbar fill,
		// kept what the recorder of a kept tab buffered.
		drains [][]engine.LearningEvent
		kept   []recorderEntry
		want   []string
	}{
		{name: "a value an entry wrote, changed after a fill and again in a later drain",
			drains: [][]engine.LearningEvent{{edit("0a1b2c3d-4e5f-4a6b-8c7d-8e9fa0b1c2d3", `[id="go"]`, "3", "5")}, {edit("1a1b2c3d-4e5f-4a6b-8c7d-8e9fa0b1c2d3", `[id="go"]`, "3", "6")}},
			want:   []string{"years-go", ""}},
		{name: "a value the profile wrote after a weak match to an entry",
			drains: [][]engine.LearningEvent{{edit("2a1b2c3d-4e5f-4a6b-8c7d-8e9fa0b1c2d3", `[id="city"]`, "Durham", "Raleigh")}}, want: []string{""}},
		{name: "a new answer the learning service withholds", drains: [][]engine.LearningEvent{{relocate}}, want: []string{""}},
		{name: "one of two controls with the same question and answer, told by its selector",
			drains: [][]engine.LearningEvent{{edit("3a1b2c3d-4e5f-4a6b-8c7d-8e9fa0b1c2d3", `[id="school-start"]`, "2019-09", "2019-08")}}, want: []string{"school-start"}},
		{name: "a value the page held before the run",
			drains: [][]engine.LearningEvent{{edit("5a1b2c3d-4e5f-4a6b-8c7d-8e9fa0b1c2d3", `[id="employer"]`, "Northwind Example", "Contoso Example")}}, want: []string{""}},
		{name: "a kept tab told by its page and question", kept: []recorderEntry{entry(1, "Years of experience with Go", "text", "5", "3")}, want: []string{"years-go"}},
		{name: "the bank's answer the check wrote", kept: []recorderEntry{pick("Earliest start date", "In a month", "In two weeks", "In two weeks", "In a month")}, want: []string{"start-date"}},
		{name: "a value the check wrote that is not the bank's", kept: []recorderEntry{pick("Office location", "Raleigh", "Durham", "Durham", "Raleigh")}, want: []string{""}},
		{name: "a value the run did not leave there", kept: []recorderEntry{entry(1, "Years of experience with Go", "text", "5", "4")}, want: []string{""}},
		{name: "a control the page emptied after the run", kept: []recorderEntry{entry(1, "Years of experience with Go", "text", "5", "")}, want: []string{""}},
		{name: "another question holding the same value", kept: []recorderEntry{entry(1, "Years of experience with Python", "text", "4", "3")}, want: []string{""}},
		{name: "another page", kept: []recorderEntry{elsewhere}, want: []string{""}},
		{name: "two entries that wrote the same answer to the question", kept: []recorderEntry{entry(1, "Start date", "text", "2020-01", "2019-09")}, want: []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &posts{}
			m := &MatchLog{}
			m.Observe(bind.Request{RunID: "er1"}, bind.Outcome{Results: results, Prepared: make([]match.Question, len(results))})
			w := &Watcher{Ctx: context.Background(), Poster: p, Every: 2 * time.Millisecond}
			g := newRig(t, func(d *Deps) { d.Page.Poster, d.Page.Matches, d.Page.Keep = p, m, &Keeper{Watch: w} })
			ps := &pageState{report: PageReport{URL: page, Step: 1}, runID: "er1", rows: rows, answered: answered}
			run := &Run{ID: "run_20270310T090000.000_abc123", Input: Input{URL: page, Caller: gates.CallerCLI}}
			if c.drains != nil {
				var ds []drained
				for _, evs := range c.drains {
					ds = append(ds, drained{events: evs})
				}
				dp := &drainPage{fakePage: &fakePage{}, drains: append(ds, drained{err: engine.ErrNoEngine})}
				f := &Fill{r: g.r, e: &Exec{r: g.r, Run: run, Ctx: context.Background()}, s: &session{r: g.r, page: dp, pages: []*pageState{ps}}, every: time.Millisecond}
				if why, _ := f.Follow(context.Background(), make(chan struct{})); why != "left" {
					t.Fatalf("the follow ended %q", why)
				}
			} else {
				obs := &scriptedObserver{onRead: func(n int) (json.RawMessage, error) {
					if n == 1 {
						return buffer(true, "e1", c.kept...), nil
					}
					return nil, browserErr(browser.CodeTabClosed)
				}}
				tab := &watchTab{scriptTab: newScriptTab(appPage()), id: "tab-k", obs: obs, asked: make(chan struct{}, 1)}
				run.Outcome = Outcome{Kind: OutcomeStopped, Stage: StageCheck, Class: ClassAnswerGap, Reason: "a required answer"}
				(&session{r: g.r, e: &Exec{r: g.r, Run: run, Ctx: context.Background()}, tab: tab, pages: []*pageState{ps}}).release(context.Background())
				w.Wait()
			}
			var got []string
			for _, ev := range p.events() {
				got = append(got, ev.EntryID)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("posted naming entries %q, want %q", got, c.want)
			}
		})
	}
}
