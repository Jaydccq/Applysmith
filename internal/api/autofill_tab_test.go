package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// fakeFiller is the engine page of a fake tab: one scripted fill result, with
// an optional gate that holds the fill so a second press can arrive.
type fakeFiller struct {
	hold    chan struct{}
	started chan struct{}
	result  *engine.PageResult
}

func (f *fakeFiller) Fill(ctx context.Context, _ engine.FillOptions) (*engine.PageResult, error) {
	if f.started != nil {
		close(f.started)
	}
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.result, nil
}

func (f *fakeFiller) Inspect(context.Context) ([]engine.InspectRow, error) {
	return []engine.InspectRow{{Selector: "#name", Value: "Alex Example"}}, nil
}

func (f *fakeFiller) Answer(context.Context, string, []engine.Resolution) ([]engine.AnswerResult, error) {
	return nil, nil
}
func (f *fakeFiller) ObserveSubmissions(context.Context) error { return nil }
func (f *fakeFiller) SubmitObservations(context.Context, time.Duration) ([]engine.SubmitObservation, error) {
	return nil, nil
}
func (f *fakeFiller) BlockedSubmits(context.Context) ([]engine.BlockedSubmit, error) { return nil, nil }

// fakeTab answers the page probe with a fixed state and records every call
// that would move the page.
type fakeTab struct {
	apply.Tab
	state  apply.PageState
	filler *fakeFiller
	mu     sync.Mutex
	moved  []string
	closed bool
}

func (t *fakeTab) ID() string         { return "tab-1" }
func (t *fakeTab) Keep() error        { return nil }
func (t *fakeTab) Page() apply.Filler { return t.filler }
func (t *fakeTab) Close(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

func (t *fakeTab) Inventory(context.Context, string, []apply.FormInfo) (apply.Inventory, error) {
	return apply.Inventory{Inputs: t.state.Inputs, Forms: t.state.Forms}, nil
}

func (t *fakeTab) Eval(_ context.Context, _ string, out any) error {
	raw, _ := json.Marshal(t.state)
	return json.Unmarshal(raw, out)
}

func (t *fakeTab) Navigate(_ context.Context, url string) (browser.NavResult, error) {
	t.note("navigate " + url)
	return browser.NavResult{}, nil
}

func (t *fakeTab) Click(context.Context, float64, float64) error {
	t.note("click")
	return nil
}

func (t *fakeTab) note(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.moved = append(t.moved, s)
}

func (t *fakeTab) presses() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.moved...)
}

// fakeActive is an apply window with at most one tab.
type fakeActive struct {
	tab   *fakeTab
	err   error
	adopt func() (apply.Tab, error) // nil hands out the tab
}

func (w *fakeActive) OpenTab(context.Context, string, engine.Host) (apply.Tab, error) {
	return nil, errors.New("a fill of the active tab never opens a tab")
}

func (w *fakeActive) ActiveTab(context.Context) (string, error) {
	if w.err != nil {
		return "", w.err
	}
	if w.tab == nil {
		return "", apply.ErrNoTab
	}
	return w.tab.ID(), nil
}

func (w *fakeActive) Adopt(context.Context, string, engine.Host) (apply.Tab, error) {
	if w.adopt != nil {
		return w.adopt()
	}
	return w.tab, nil
}

func formPage(fields int) apply.PageState {
	st := apply.PageState{URL: "https://jobs.example.test/apply/1?utm=x", Title: "Apply"}
	for i := 0; i < fields; i++ {
		st.Inputs = append(st.Inputs, apply.InputInfo{I: i, Tag: "input", Type: "text"})
	}
	return st
}

func fillResult() *engine.PageResult {
	return &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{Site: "example", Rows: []engine.Row{
		{Selector: "#name", Label: "Full name", Outcome: "filled", Value: "Alex Example", Required: true},
		{Selector: "#why", Label: "Why us", Outcome: "abstained", Required: true, Reason: "no-answer"},
		{Selector: "#pro", Label: "Pronouns", Outcome: "skipped", Reason: "sensitive"},
	}}}}
}

func withWindow(w *fakeActive) func(*Config) {
	return func(c *Config) {
		c.ApplyWindow = func(context.Context) (apply.Window, error) { return w, nil }
	}
}

func fillStatus(t *testing.T, h *harness, id string) wire.AutofillTabResult {
	t.Helper()
	r := h.get("/api/autofill/active-tab/" + id)
	if r.StatusCode != 200 {
		t.Fatalf("result: %d %s", r.StatusCode, r.body)
	}
	return decode[wire.AutofillTabResult](t, r)
}

func waitFill(t *testing.T, h *harness, id string) wire.AutofillTabResult {
	t.Helper()
	var got wire.AutofillTabResult
	waitLong(t, "the fill to end", func() bool {
		got = fillStatus(t, h, id)
		return got.Status != wire.RunRunning
	})
	return got
}

func TestAutofillTab_FillOnce(t *testing.T) {
	tab := &fakeTab{state: formPage(8), filler: &fakeFiller{result: fillResult()}}
	h := newDomainHarness(t, withWindow(&fakeActive{tab: tab}))
	r := h.post("/api/autofill/active-tab", nil)
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	id := decode[wire.RunAccepted](t, r).RunID
	got := waitFill(t, h, id)
	if got.Status != wire.RunSucceeded || len(got.Filled) != 1 || got.Filled[0].Label != "Full name" || len(got.Missing) != 1 || got.Missing[0].Label != "Why us" ||
		len(got.Skipped) != 1 || got.Skipped[0].Label != "Pronouns" || got.URL != "https://jobs.example.test/apply/1" || got.Site != "example" {
		t.Fatalf("result = %+v", got)
	}
	if moved := tab.presses(); len(moved) != 0 {
		t.Fatalf("the fill moved the page: %v", moved)
	}
	run := h.get("/api/runs/" + id)
	if run.StatusCode != 200 || run.data()["kind"] != wire.RunKindAutofillTab || run.data()["status"] != wire.RunSucceeded {
		t.Fatalf("the run record: %d %s", run.StatusCode, run.body)
	}
	waitLong(t, "the tab to be let go", func() bool { return !h.domainOf().apply.fills.any() })
}

func TestAutofillTab_NotAnApplicationWritesNothing(t *testing.T) {
	article := apply.PageState{URL: "https://news.example.test/story", Title: "Story", Inputs: []apply.InputInfo{{Tag: "input", Type: "search"}}}
	tab := &fakeTab{state: article, filler: &fakeFiller{result: fillResult()}}
	h := newDomainHarness(t, withWindow(&fakeActive{tab: tab}))
	r := h.post("/api/autofill/active-tab", nil)
	if r.StatusCode != 422 || r.errReason() != wire.ReasonNotAnApp {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM runs WHERE kind = ?`, wire.RunKindAutofillTab).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused press left %d runs (%v)", n, err)
	}
	if h.domainOf().apply.fills.any() {
		t.Fatal("the tab stayed reserved")
	}
}

func TestAutofillTab_RunInProgress(t *testing.T) {
	tab := &fakeTab{state: formPage(8), filler: &fakeFiller{result: fillResult()}}
	h := newDomainHarness(t, withWindow(&fakeActive{tab: tab}))
	h.domainOf().apply.busy.Store(true)
	r := h.post("/api/autofill/active-tab", nil)
	if r.StatusCode != 409 || r.errReason() != wire.ReasonLaneBusy {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if len(tab.presses()) != 0 || tab.closed {
		t.Fatal("the tab was touched")
	}
}

func TestAutofillTab_NoTab(t *testing.T) {
	h := newDomainHarness(t, withWindow(&fakeActive{}))
	if r := h.post("/api/autofill/active-tab", nil); r.StatusCode != 404 || r.errReason() != wire.ReasonNoTab {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestAutofillTab_ChromeCannotBeUsed(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(context.Context) (apply.Window, error) {
			return nil, &browser.Error{Code: browser.CodeChromeMissing, Message: "no Chrome"}
		}
	})
	if r := h.post("/api/autofill/active-tab", nil); r.StatusCode != 503 || r.errReason() != string(browser.CodeChromeMissing) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestAutofillTab_SecondPressWhileTheFirstRuns(t *testing.T) {
	filler := &fakeFiller{result: fillResult(), hold: make(chan struct{}), started: make(chan struct{})}
	tab := &fakeTab{state: formPage(8), filler: filler}
	h := newDomainHarness(t, withWindow(&fakeActive{tab: tab}))
	first := h.post("/api/autofill/active-tab", nil)
	if first.StatusCode != 202 {
		t.Fatalf("%d %s", first.StatusCode, first.body)
	}
	<-filler.started
	if r := h.post("/api/autofill/active-tab", nil); r.StatusCode != 409 || r.errReason() != wire.ReasonAlreadyRunning {
		t.Fatalf("second press: %d %s", r.StatusCode, r.body)
	}
	// An application started while the window is being filled waits for the
	// fill, then starts.
	queued := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.test/apply/2"})
	run := decode[wire.RunAccepted](t, queued)
	if queued.StatusCode != 202 || run.Ahead != 1 {
		t.Fatalf("run during a fill: %d %s", queued.StatusCode, queued.body)
	}
	close(filler.hold)
	id := decode[wire.RunAccepted](t, first).RunID
	if got := waitFill(t, h, id); got.Status != wire.RunSucceeded {
		t.Fatalf("%+v", got)
	}
	waitLong(t, "the queued run to start once the fill ended", func() bool {
		v := decode[wire.RunView](t, h.get("/api/runs/"+run.RunID))
		return v.Entry != nil && v.Entry.Stage != wire.BoardStageQueued
	})
	h.post("/api/runs/"+run.RunID+"/cancel", nil)
}

// A fill that could not take its tab lets go of the window, so an application
// that queued behind it starts.
func TestAutofillTab_ATabThatCannotBeTakenLetsAQueuedStartRun(t *testing.T) {
	taking, hold := make(chan struct{}), make(chan struct{})
	w := &fakeActive{tab: &fakeTab{state: formPage(8), filler: &fakeFiller{result: fillResult()}}}
	w.adopt = func() (apply.Tab, error) {
		close(taking)
		<-hold
		return nil, errors.New("the tab is gone")
	}
	h := newDomainHarness(t, withWindow(w))
	press := make(chan result, 1)
	go func() { press <- h.post("/api/autofill/active-tab", nil) }()
	<-taking
	queued := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.test/apply/2"})
	run := decode[wire.RunAccepted](t, queued)
	if queued.StatusCode != 202 || run.Ahead != 1 {
		t.Fatalf("run during a fill: %d %s", queued.StatusCode, queued.body)
	}
	close(hold)
	if r := <-press; r.StatusCode != 503 {
		t.Fatalf("the fill: %d %s", r.StatusCode, r.body)
	}
	waitLong(t, "the queued run to start once the fill gave up", func() bool {
		v := decode[wire.RunView](t, h.get("/api/runs/"+run.RunID))
		return v.Entry != nil && v.Entry.Stage != wire.BoardStageQueued
	})
	h.post("/api/runs/"+run.RunID+"/cancel", nil)
}

func TestAutofillTab_StoppingTheServerCancelsAFillThatIsWorking(t *testing.T) {
	filler := &fakeFiller{result: fillResult(), hold: make(chan struct{}), started: make(chan struct{})}
	tab := &fakeTab{state: formPage(8), filler: filler}
	h := newDomainHarness(t, withWindow(&fakeActive{tab: tab}))
	first := h.post("/api/autofill/active-tab", nil)
	<-filler.started
	// Stopping the server ends a fill that is still working.
	h.srv.stopBG()
	id := decode[wire.RunAccepted](t, first).RunID
	if got := waitFill(t, h, id); got.Status != wire.RunCancelled {
		t.Fatalf("%+v", got)
	}
}
