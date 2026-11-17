package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// scriptTab is a tab whose pages are scripted: Eval answers the probe
// commands from the current PageState, a click on a control runs a hook that
// may change the state, and typing is recorded per focused input. It lets the
// account stage be tested without a browser.
type scriptTab struct {
	Tab
	mu       sync.Mutex
	state    *PageState
	last     *PageState   // the state the last probe returned: its fields are the inventory
	queue    []*PageState // states the next probes return, then the last stays
	url      string
	focus    int
	typed    map[int]string
	clicks   []string
	navs     []string
	domNavs  []string // the navigations that waited for the document only
	loadNavs []string // the navigations that waited for the load event
	// lk answers the LinkedIn page reads by command; evalErr fails every read.
	lk      map[string]any
	evalErr error
	onClick func(t *scriptTab, c Control)
	onNav   func(t *scriptTab, url string)
	kept    bool
	closed  bool
	// labelsNow overrides what a control reads at press time, by handle.
	labelsNow map[int][]string
	// covered marks handles whose centre something else covers; coverLooks
	// covers a handle for that many locates only (a dialog still drawing).
	covered    map[int]bool
	coverLooks map[int]int
	watch      *fakeWatch
	// delivered records token deliveries by kind.
	delivered []string
	page      *fakePage
	// console is what each page prints to its console; consoleReads the pages read.
	console      map[string][]string
	consoleReads []string
	// clickErr makes the next click fail after it was dispatched.
	clickErr error
	// onDeliver runs when a token is delivered; blanked counts blanked kinds.
	// lost makes a delivery reach no response field and no callback.
	onDeliver func(t *scriptTab, kind string)
	blanked   int
	lost      bool
	// inputClicks are the labels of the boxes a click landed on.
	inputClicks []string
	// chosen is the option index picked per select handle; onChoose runs
	// under the tab's lock after a pick (it sets t.state directly).
	chosen   map[int]int
	onChoose func(t *scriptTab, i, index int)
	// company is what the page states as its employer.
	company string
	// posting is what probe.js `posting` answers (title, company) and
	// postingReads counts the times it was asked.
	posting      map[string]string
	postingReads int
	// keyed records the text typed key by key into each probed box.
	keyed map[int]string
	// onKeys runs when a box is typed key by key.
	onKeys func(t *scriptTab, i int, text string)
	// probeHook runs, under the tab's lock, before each probe and each locate;
	// it may change the state.
	probeHook func(t *scriptTab)
	// dropWrites makes the page keep nothing that is typed.
	dropWrites bool
	// revalidate answers the form re-validation command; onRevalidate runs when
	// it is asked. redirects maps a requested address to the one it ends on.
	revalidate   *Revalidation
	onRevalidate func(t *scriptTab)
	revalidated  int
	redirects    map[string]string
	// placeFn answers probe.js `place` for the selectors the engine named.
	placeFn func(sels []string) []placement
	// consent answers probe.js `consent`: the gate standing on the page now.
	consent *ConsentGate
	// gone marks handles the page dropped after they were read (a redraw): a
	// locate of one finds nothing, and the next probe hands out fresh handles.
	gone     map[int]bool
	goneSeen bool
	// prepress answers probe.js `prepress` in turn, the last one staying.
	prepress []PrePress
	// popups maps a pressed control's label to the address of the window the
	// press opens; captured lists the addresses CapturePopup reported.
	popups   map[string]string
	captured []string
	// popupLate makes the window appear only after the press returns, so a
	// wait the walk already cancelled never sees it (the real capturer
	// closes it unread).
	popupLate bool
	// choices answers probe.js `choices` by selector; a click on a member
	// chooses it and records its name in inputClicks.
	choices map[string][]Choice
}

// CapturePopup runs act and reports the window the press opened, by the
// label of the control the last click landed on.
func (t *scriptTab) CapturePopup(ctx context.Context, act func(context.Context) error) (string, error) {
	t.mu.Lock()
	n := len(t.clicks)
	t.mu.Unlock()
	if err := act(ctx); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.clicks) == n || t.popupLate && ctx.Err() != nil {
		return "", nil
	}
	u := t.popups[t.clicks[len(t.clicks)-1]]
	if u != "" {
		t.captured = append(t.captured, u)
	}
	return u, nil
}

func newScriptTab(st *PageState) *scriptTab {
	return &scriptTab{state: st, url: st.URL, typed: map[int]string{}, focus: -1}
}

func (t *scriptTab) ID() string { return "script" }

func (t *scriptTab) set(st *PageState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = st
	t.queue = nil
	if st.URL != "" {
		t.url = st.URL
	}
}

func (t *scriptTab) script(sts ...*PageState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queue = sts
}

func (t *scriptTab) current() *PageState {
	if len(t.queue) > 0 {
		st := t.queue[0]
		if len(t.queue) > 1 {
			t.queue = t.queue[1:]
		} else {
			t.state, t.queue = st, nil
		}
		t.last = st
		return st
	}
	t.last = t.state
	return t.state
}

// Inventory answers with the fields of the state the last probe returned, as
// the engine's inspect result would list them.
func (t *scriptTab) Inventory(_ context.Context, _ string, _ []FormInfo) (Inventory, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.last
	if st == nil {
		st = t.state
	}
	return Inventory{Inputs: st.Inputs, Forms: st.Forms, Empty: st.Stall.Empty}, nil
}

func (t *scriptTab) Navigate(_ context.Context, url string) (browser.NavResult, error) {
	t.mu.Lock()
	t.navs = append(t.navs, url)
	t.loadNavs = append(t.loadNavs, url)
	h := t.onNav
	t.mu.Unlock()
	if h != nil {
		h(t, url)
	}
	final := url
	if f, ok := t.redirects[url]; ok {
		final = f
	}
	return browser.NavResult{Status: 200, URL: final}, nil
}

// NavigateDOM records the navigation as one that waited for the document only.
func (t *scriptTab) NavigateDOM(_ context.Context, url string) (browser.NavResult, error) {
	t.mu.Lock()
	t.navs = append(t.navs, url)
	t.domNavs = append(t.domNavs, url)
	h := t.onNav
	t.mu.Unlock()
	if h != nil {
		h(t, url)
	}
	final := url
	if f, ok := t.redirects[url]; ok {
		final = f
	}
	return browser.NavResult{Status: 200, URL: final}, nil
}

func (t *scriptTab) Frames(context.Context) ([]browser.Frame, error) { return nil, nil }

func (t *scriptTab) EvalFrame(context.Context, string, string, any) error { return nil }

func (t *scriptTab) Eval(_ context.Context, expr string, out any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.evalErr != nil {
		return t.evalErr
	}
	for cmd, v := range t.lk {
		if strings.HasSuffix(expr, `)("`+cmd+`")`) {
			return setJSON(out, v)
		}
	}
	if t.probeHook != nil && (strings.HasSuffix(expr, `("probe",null)`) || strings.Contains(expr, `("locate",`)) {
		t.probeHook(t)
	}
	switch {
	case strings.Contains(expr, `("place",`):
		raw := expr[strings.LastIndex(expr, `("place",`)+len(`("place",`) : len(expr)-1]
		var sels []string
		if err := json.Unmarshal([]byte(raw), &sels); err != nil {
			return err
		}
		return setJSON(out, t.placeFn(sels))
	case strings.HasSuffix(expr, `("prepress",null)`):
		if len(t.prepress) == 0 {
			return setJSON(out, PrePress{})
		}
		p := t.prepress[0]
		if len(t.prepress) > 1 {
			t.prepress = t.prepress[1:]
		}
		return setJSON(out, p)
	case strings.HasSuffix(expr, `("consent",null)`):
		if t.consent == nil {
			return setJSON(out, ConsentGate{})
		}
		return setJSON(out, t.consent)
	case strings.HasSuffix(expr, `("revalidate",null)`):
		t.revalidated++
		if h := t.onRevalidate; h != nil {
			h(t)
		}
		return setJSON(out, t.revalidate)
	case strings.HasSuffix(expr, `("probe",null)`):
		if t.goneSeen {
			t.gone, t.goneSeen = nil, false
		}
		// A nil queued state is a page between documents: that read fails.
		if len(t.queue) > 1 && t.queue[0] == nil {
			t.queue = t.queue[1:]
			return errors.New("Execution context was destroyed")
		}
		return setJSON(out, t.current())
	case strings.HasSuffix(expr, `("signature",null)`):
		st := t.state
		return setJSON(out, fmt.Sprintf("%s|%d|%d|%v", st.URL, len(st.Text), len(st.Validation), t.typed))
	case strings.Contains(expr, `("focus",`):
		var i int
		fmt.Sscanf(expr[strings.LastIndex(expr, `("focus",`)+len(`("focus",`):], "%d", &i)
		t.focus = i
		return setJSON(out, true)
	case strings.Contains(expr, `("focusSecret",`):
		var i int
		fmt.Sscanf(expr[strings.LastIndex(expr, `("focusSecret",`)+len(`("focusSecret",`):], "%d", &i)
		for _, in := range t.state.Inputs {
			if in.I == i && in.Type == "password" {
				t.focus = i
				return setJSON(out, true)
			}
		}
		return setJSON(out, false)
	case strings.Contains(expr, `("locate",`):
		var i int
		fmt.Sscanf(expr[strings.LastIndex(expr, `("locate",`)+len(`("locate",`):], "%d", &i)
		if t.gone[i] {
			t.goneSeen = true
			return setJSON(out, Located{})
		}
		for _, c := range t.state.Controls {
			if c.I == i {
				labels := c.Labels
				if l, ok := t.labelsNow[i]; ok {
					labels = l
				}
				hit := !t.covered[i] && t.coverLooks[i] == 0
				if t.coverLooks[i] > 0 {
					t.coverLooks[i]--
				}
				return setJSON(out, Located{Found: true, Labels: labels, X: float64(i*10 + 1), Y: 1, Hit: hit, Blocker: "div cookie banner", Visible: true, Disabled: c.Disabled})
			}
		}
		for _, in := range t.state.Inputs {
			if in.I == i {
				return setJSON(out, Located{Found: true, Labels: []string{in.Label}, X: float64(i*10 + 1), Y: 1, Hit: !t.covered[i], Visible: true})
			}
		}
		for _, cs := range t.choices {
			for _, c := range cs {
				if c.I == i {
					return setJSON(out, Located{Found: true, Labels: []string{c.Name}, X: float64(i*10 + 1), Y: 1, Hit: true, Visible: true})
				}
			}
		}
		return setJSON(out, Located{})
	case strings.Contains(expr, `("choose",`):
		var arg struct{ I, Index int }
		raw := expr[strings.LastIndex(expr, `("choose",`)+len(`("choose",`):]
		raw = raw[:strings.LastIndex(raw, ")")]
		_ = json.Unmarshal([]byte(raw), &arg)
		if t.chosen == nil {
			t.chosen = map[int]int{}
		}
		t.chosen[arg.I] = arg.Index
		if t.onChoose != nil {
			t.onChoose(t, arg.I, arg.Index)
		}
		return setJSON(out, true)
	case strings.Contains(expr, `("deliver",`):
		kind := expr[strings.LastIndex(expr, `{"kind":"`)+9 : strings.LastIndex(expr, `","token"`)]
		t.delivered = append(t.delivered, kind)
		h := t.onDeliver
		if h != nil {
			t.mu.Unlock()
			h(t, kind)
			t.mu.Lock()
		}
		if t.lost {
			return setJSON(out, delivered{})
		}
		return setJSON(out, delivered{Fields: 1})
	case strings.Contains(expr, `("typekeys",`):
		var arg struct {
			I    int
			Text string
		}
		raw := expr[strings.LastIndex(expr, `("typekeys",`)+len(`("typekeys",`) : len(expr)-1]
		if err := json.Unmarshal([]byte(raw), &arg); err != nil {
			return err
		}
		if t.keyed == nil {
			t.keyed = map[int]string{}
		}
		t.keyed[arg.I] = arg.Text
		t.markFilled(arg.I)
		if h := t.onKeys; h != nil {
			t.mu.Unlock()
			h(t, arg.I, arg.Text)
			t.mu.Lock()
		}
		return setJSON(out, true)
	case strings.Contains(expr, `("choices",`):
		var sel string
		if err := json.Unmarshal([]byte(expr[strings.LastIndex(expr, `("choices",`)+len(`("choices",`):len(expr)-1]), &sel); err != nil {
			return err
		}
		return setJSON(out, t.choices[sel])
	case strings.HasSuffix(expr, `("company",null)`):
		return setJSON(out, t.company)
	case strings.HasSuffix(expr, `("posting",null)`):
		t.postingReads++
		return setJSON(out, t.posting)
	case strings.Contains(expr, `("blank",`):
		t.blanked++
		return setJSON(out, 1)
	case strings.Contains(expr, "outerHTML"):
		return setJSON(out, "<html></html>")
	}
	return nil
}

func (t *scriptTab) Click(_ context.Context, x, y float64) error {
	t.mu.Lock()
	i := int(x) / 10
	var hit Control
	for _, c := range t.state.Controls {
		if c.I == i {
			hit = c
		}
	}
	t.clicks = append(t.clicks, strings.Join(hit.Labels, "/"))
	if len(hit.Labels) == 0 {
		for _, in := range t.state.Inputs {
			if in.I == i {
				t.inputClicks = append(t.inputClicks, in.Label)
			}
		}
		for _, cs := range t.choices {
			for k := range cs {
				if cs[k].I == i {
					for j := range cs {
						cs[j].Checked = j == k
					}
					t.inputClicks = append(t.inputClicks, cs[k].Name)
				}
			}
		}
	}
	h := t.onClick
	cerr := t.clickErr
	t.mu.Unlock()
	if h != nil {
		h(t, hit)
	}
	return cerr
}

func (t *scriptTab) TypeText(_ context.Context, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.typed[t.focus] += text
	t.markFilled(t.focus)
	return nil
}

func (t *scriptTab) PressKey(context.Context, string) error { return nil }

// markFilled shows the box as holding text on the next probe.
func (t *scriptTab) markFilled(i int) {
	if t.dropWrites {
		return
	}
	for k := range t.state.Inputs {
		if t.state.Inputs[k].I == i {
			t.state.Inputs[k].Filled = true
		}
	}
}

func (t *scriptTab) WatchResponses(context.Context) (ResponseWatch, error) {
	if t.watch == nil {
		t.watch = &fakeWatch{}
	}
	return t.watch, nil
}

func (t *scriptTab) Page() Filler {
	if t.page == nil {
		t.page = &fakePage{}
	}
	return t.page
}

func (t *scriptTab) Screenshot(context.Context) ([]byte, error) { return []byte("png"), nil }
func (t *scriptTab) Keep() error                                { t.kept = true; return nil }

// ConsoleLines answers from console, by address; consoleReads counts the reads.
func (t *scriptTab) ConsoleLines(_ context.Context, rawURL string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consoleReads = append(t.consoleReads, rawURL)
	return t.console[rawURL], nil
}
func (t *scriptTab) Close(context.Context) error { t.closed = true; return nil }

func setJSON(out any, v any) error {
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// scriptWindow opens the one scripted tab.
type scriptWindow struct{ tab *scriptTab }

func (w scriptWindow) OpenTab(context.Context, string, engine.Host) (Tab, error) { return w.tab, nil }

// Page builders.

const loginURL = "https://careers.example.test/jobs/7"

func loginPage(errText string) *PageState {
	st := &PageState{
		URL: loginURL, Title: "Sign in", Text: "Sign in " + errText, Lines: []string{"Sign in", errText},
		Inputs: []InputInfo{
			{I: 0, Tag: "input", Type: "email", Label: "Email", Name: "email", Form: -1},
			{I: 1, Tag: "input", Type: "password", Label: "Password", Name: "password", Form: -1},
		},
		Controls: []Control{
			{I: 2, Tag: "button", Labels: []string{"Sign in"}, Form: -1},
			{I: 3, Tag: "a", Labels: []string{"Create account"}, Form: -1},
		},
	}
	if errText != "" {
		st.Validation = []string{errText}
	}
	return st
}

func appPage() *PageState {
	return &PageState{
		URL: loginURL, Title: "Apply - Platform Engineer", Text: "Platform Engineer apply", Lines: []string{"Platform Engineer"},
		Forms:    []FormInfo{{Fields: 3}},
		Inputs:   []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "First Name", Form: 0}},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Submit application"}, Form: 0}},
	}
}

// fakeWatch is a response watch the test fills.
type fakeWatch struct {
	mu   sync.Mutex
	rs   []browser.Response
	stop bool
}

func (w *fakeWatch) add(r ...browser.Response) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rs = append(w.rs, r...)
}
func (w *fakeWatch) Responses() []browser.Response {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]browser.Response(nil), w.rs...)
}
func (w *fakeWatch) Pending() int { return 0 }
func (w *fakeWatch) Stop()        { w.stop = true }

// fakePage is the engine's page surface, scripted.
type fakePage struct {
	mu      sync.Mutex
	fills   []*engine.PageResult
	nFills  int
	fillErr error
	// fillErrOnce fails only the first fill with fillErr.
	fillErrOnce bool
	// onFill runs at the start of each fill with its count.
	onFill   func(n int)
	rows     func() []engine.InspectRow
	answers  [][]engine.Resolution
	answerFn func([]engine.Resolution)
	obs      []engine.SubmitObservation
	blocked  []engine.BlockedSubmit
	// outcomes overrides what the writer reports for a selector ("filled"
	// otherwise).
	outcomes map[string]string
	// reasons is the stage the writer names for a selector it did not fill.
	reasons map[string]string
	// frames, when set, are the rows of the documents of the page's frames,
	// by frame id; a frame it does not name reads like the top document.
	frames map[string]func() []engine.InspectRow
	// gaps answers SavedFormGaps; repaired answers RepairSavedForm and repairs
	// counts its calls.
	gaps, repaired []string
	repairs        int
}

func (p *fakePage) SavedFormGaps(context.Context) ([]string, error) { return p.gaps, nil }

func (p *fakePage) RepairSavedForm(context.Context) ([]string, error) {
	p.repairs++
	return p.repaired, nil
}

func (p *fakePage) Fill(context.Context, engine.FillOptions) (*engine.PageResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nFills++
	if p.onFill != nil {
		p.onFill(p.nFills)
	}
	if err := p.fillErr; err != nil {
		if p.fillErrOnce {
			p.fillErr = nil
		}
		return nil, err
	}
	if len(p.fills) == 0 {
		return doneResult(scriptedRow), nil
	}
	r := p.fills[0]
	if len(p.fills) > 1 {
		p.fills = p.fills[1:]
	}
	return r, nil
}

// scriptedRow is the one control the scripted engine reads on a page a test
// gave no rows: a real page always reads at least one, and a page that reads
// none is never verified.
var scriptedRow = engine.Row{Selector: "#scripted", Label: "Scripted question", Kind: "text", Outcome: "skipped", Reason: "no-lane-claimed"}

func (p *fakePage) Inspect(context.Context) ([]engine.InspectRow, error) {
	if p.rows == nil {
		return []engine.InspectRow{{Selector: scriptedRow.Selector, Label: scriptedRow.Label, Kind: scriptedRow.Kind}}, nil
	}
	return p.rows(), nil
}
func (p *fakePage) Answer(_ context.Context, _ string, res []engine.Resolution) ([]engine.AnswerResult, error) {
	p.mu.Lock()
	p.answers = append(p.answers, res)
	fn := p.answerFn
	p.mu.Unlock()
	if fn != nil {
		fn(res)
	}
	var out []engine.AnswerResult
	for _, r := range res {
		oc := "filled"
		if o, ok := p.outcomes[r.Selector]; ok {
			oc = o
		}
		out = append(out, engine.AnswerResult{Selector: r.Selector, Outcome: oc, Reason: p.reasons[r.Selector], Value: r.Value})
	}
	return out, nil
}
func (p *fakePage) ObserveSubmissions(context.Context) error { return nil }
func (p *fakePage) SubmitObservations(context.Context, time.Duration) ([]engine.SubmitObservation, error) {
	return p.obs, nil
}
func (p *fakePage) BlockedSubmits(context.Context) ([]engine.BlockedSubmit, error) {
	return p.blocked, nil
}

func (p *fakePage) InspectIn(ctx context.Context, frame string) ([]engine.InspectRow, error) {
	if rows, ok := p.frames[frame]; ok {
		return rows(), nil
	}
	return p.Inspect(ctx)
}
func (p *fakePage) AnswerIn(ctx context.Context, frame, runID string, res []engine.Resolution) ([]engine.AnswerResult, error) {
	return p.Answer(ctx, runID, res)
}
func (p *fakePage) BlockedSubmitsIn(ctx context.Context, frame string) ([]engine.BlockedSubmit, error) {
	return p.BlockedSubmits(ctx)
}

func doneResult(rows ...engine.Row) *engine.PageResult {
	return &engine.PageResult{RunID: "er1", Top: engine.Status{State: engine.StateDone, Summary: &engine.Summary{Rows: rows}}}
}

// fakeMatcher is the answer-bank binding, scripted.
type fakeMatcher struct {
	mu    sync.Mutex
	reqs  []engine.MatchRequest
	reply func(engine.MatchRequest) engine.MatchResponse
}

func (f *fakeMatcher) Match(_ context.Context, req engine.MatchRequest) (engine.MatchResponse, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	fn := f.reply
	f.mu.Unlock()
	if fn == nil {
		return engine.MatchResponse{}, nil
	}
	return fn(req), nil
}
