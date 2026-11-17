//go:build browser

package apply

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/browser"
	"applysmith/internal/demo"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
	"applysmith/internal/research"
	"applysmith/internal/signals"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// usePersonaAnswers writes the demo persona's answers into the rig's bank, as
// the demo init does: a fixed entry takes the answer, a pick-option entry takes
// it as its preferred option.
func usePersonaAnswers(t *testing.T, g *e2e) {
	t.Helper()
	bank := answerbank.New(workspace.Open(filepath.Dir(filepath.Dir(g.db))), g.st)
	loaded, err := bank.File()
	if err != nil {
		t.Fatal(err)
	}
	after := loaded.Bank.Clone()
	for id, v := range demo.Answers() {
		for i := range after.Entries {
			e := &after.Entries[i]
			if e.ID != id || !e.BodyEmpty() {
				continue
			}
			switch e.Kind {
			case match.KindFixed:
				e.Answer, e.NeedsReview = v, false
			case match.KindPickOption:
				e.Preferred, e.NeedsReview = []string{v}, false
			}
		}
	}
	if _, err := bank.SaveChange(loaded.Bank, after, loaded.Revision); err != nil {
		t.Fatal(err)
	}
}

// Keep a fixture's bank and applicant snapshot on the same persona: the bank
// answers its identity entries from the profile at every load.
func useProfile(t *testing.T, g *e2e, profile []byte) {
	t.Helper()
	ws := workspace.Open(filepath.Dir(filepath.Dir(g.db)))
	if err := os.WriteFile(ws.Path(workspace.FileProfile), profile, 0o600); err != nil {
		t.Fatal(err)
	}
}

// sampleFormServer serves the demo's sample application form on loopback, as
// a server running a demo workspace does.
func sampleFormServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != demo.SampleApplicationPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", demo.SampleApplicationCSP)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(demo.SampleApplication())
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL + demo.SampleApplicationPath
}

// A demo workspace dry-runs the local sample form: the page is filled, the
// résumé goes to the page on this computer, and an explicit submit request
// still presses nothing.
func TestE2E_Demo_TheSampleFormIsDryRunWithTheResumeAttached(t *testing.T) {
	inst, _ := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	slots <- struct{}{}
	defer func() { <-slots }()
	url := sampleFormServer(t)
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	markDemo(t, g.st)
	usePersonaAnswers(t, g)
	run := (&scenario{t: t, g: g}).run(Input{URL: url, Caller: gates.CallerAskAgent, Tool: ToolAutofill, Submit: true, ExplicitRequest: true,
		UserMessage: "apply to " + url + " and submit"})
	defer dump(t, run)
	for _, ti := range lane.Tabs() {
		if ti.Owner == run.ID {
			defer lane.CloseTab(t.Context(), ti.ID)
		}
	}
	if run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("outcome %+v, want a dry run that filled the form", run.Outcome)
	}
	if len(run.Pages) == 0 || run.Pages[0].Filled < 5 {
		t.Fatalf("pages %+v, want the sample form filled", run.Pages)
	}
	if n := g.count(t, `SELECT count(*) FROM submissions`); n != 0 {
		t.Fatalf("%d ledger rows", n)
	}
	if n := g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'submit_authorization' AND code = 'demo_workspace' AND run_id = ?`, run.ID); n != 1 {
		t.Fatalf("%d submit refusals with demo_workspace, want 1", n)
	}
	if n := g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'allow' AND target_host LIKE '127.0.0.1%' AND run_id = ?`, run.ID); n == 0 {
		t.Fatal("the résumé upload to the loopback page was not decided by the demo gate")
	}
}

// A demo dry run on a posting served under a real host name uploads the
// fictional résumé, fills the rest of the page and never presses Submit.
func TestE2E_Demo_ThePersonasResumeIsUploadedToARealHostAndSubmitIsNeverPressed(t *testing.T) {
	s := newScenario(t, "greenhouse", modeAccept, Hooks{})
	markDemo(t, s.g.st)
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if s.b.presses() != 0 {
		t.Fatalf("the board saw %d writes in a demo", s.b.presses())
	}
	if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'allow' AND target_host LIKE '%greenhouse.io%' AND run_id = ?`, run.ID); n == 0 {
		t.Fatal("no audited upload decision for the real host")
	}
	for _, w := range run.Warnings {
		if strings.HasPrefix(w, "upload skipped") {
			t.Fatalf("warnings %v: the upload was skipped", run.Warnings)
		}
	}
	if len(run.Pages) == 0 || run.Pages[0].Filled < 3 {
		t.Fatalf("pages %+v, want the other fields filled", run.Pages)
	}
	if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE code = 'demo_workspace' AND verdict = 'deny' AND run_id = ?`, run.ID); n == 0 {
		t.Fatal("the submit was not refused with demo_workspace")
	}
	if n := s.g.count(t, `SELECT count(*) FROM submissions`); n != 0 {
		t.Fatalf("%d ledger rows", n)
	}
}

// The live-fact lane end to end, on a page of a fictional company and with the
// demo persona Alex Example. Nothing on the page is real: the company, the
// role and every applicant value are invented, and the page sends nothing.
const (
	liveTextQuestion   = "What is the capital of Australia?"
	liveOptionQuestion = "How many continents are there?"
	personalQuestion   = "What is your favorite board game?"
)

const northwindForm = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Platform Engineer - Northwind Example Labs (fictional)</title>
<style>
body { font: 16px/1.5 system-ui, sans-serif; max-width: 42rem; margin: 2rem auto; padding: 0 1rem; }
label { display: block; font-weight: 600; margin-top: 1rem; }
input[type=text], input[type=email], input[type=tel], select { width: 100%; padding: .4rem; font: inherit; box-sizing: border-box; }
button { margin-top: 1.5rem; padding: .5rem 1.25rem; font: inherit; }
</style>
</head>
<body>
<h1>Platform Engineer</h1>
<p>Northwind Example Labs (a fictional company) - Remote, United States</p>
<form id="application" novalidate>
  <label for="first_name">First name *</label>
  <input id="first_name" name="first_name" type="text" autocomplete="given-name" required>
  <label for="last_name">Last name *</label>
  <input id="last_name" name="last_name" type="text" autocomplete="family-name" required>
  <label for="email">Email *</label>
  <input id="email" name="email" type="email" autocomplete="email" required>
  <label for="phone">Phone</label>
  <input id="phone" name="phone" type="tel" autocomplete="tel">
  <label for="city">City *</label>
  <input id="city" name="city" type="text" autocomplete="address-level2" required>
  <label for="capital">` + liveTextQuestion + `</label>
  <input id="capital" name="capital" type="text">
  <label for="continents">` + liveOptionQuestion + `</label>
  <select id="continents" name="continents">
    <option value="">Select</option>
    <option>5</option>
    <option>6</option>
    <option>7</option>
    <option>8</option>
  </select>
  <label for="boardgame">` + personalQuestion + `</label>
  <input id="boardgame" name="boardgame" type="text">
  <button type="submit">Submit application</button>
</form>
<script>
document.getElementById('application').addEventListener('submit', function (e) {
  e.preventDefault();
  fetch('/submitted', {method: 'POST'});
});
var last = '';
setInterval(function () {
  var v = JSON.stringify(Object.fromEntries(Array.from(document.querySelectorAll('#application input, #application select')).map(function (e) { return [e.id, e.value]; })));
  if (v !== last) { last = v; fetch('/values', {method: 'POST', body: v}); }
}, 100);
</script>
</body>
</html>`

// northwind is the fictional page on loopback: it counts the submissions that
// reach it and keeps the values the page last reported for its controls.
type northwind struct {
	url       string
	submitted atomic.Int32
	mu        sync.Mutex
	held      map[string]string
}

func (n *northwind) values() map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return maps.Clone(n.held)
}

func northwindServer(t *testing.T) *northwind {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	n := &northwind{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jobs/platform-engineer/apply":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(northwindForm))
		case "/values":
			var v map[string]string
			if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&v) == nil {
				n.mu.Lock()
				n.held = v
				n.mu.Unlock()
			}
		case "/submitted":
			n.submitted.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	n.url = srv.URL + "/jobs/platform-engineer/apply"
	return n
}

// researchFacts serves the answer bank's live-fact lane from a research
// service, as the server's own adapter does.
type researchFacts struct{ svc *research.Service }

func (l researchFacts) Available(ctx context.Context) bool { return l.svc.Available(ctx) }

func (l researchFacts) Lookup(ctx context.Context, question string) (assist.LiveFact, error) {
	ans, err := l.svc.Research(ctx, research.Request{Question: question, Brief: true})
	var uncited *research.UncitedError
	switch {
	case errors.Is(err, research.ErrNoSource):
		return assist.LiveFact{}, assist.ErrLiveFactNoSource
	case errors.As(err, &uncited), errors.Is(err, research.ErrNoResults):
		return assist.LiveFact{}, fmt.Errorf("%w: %v", assist.ErrLiveFactRefused, err)
	case err != nil:
		return assist.LiveFact{}, err
	case ans.Unanswered || strings.TrimSpace(ans.Text) == "":
		return assist.LiveFact{}, fmt.Errorf("%w: the sources do not answer", assist.ErrLiveFactRefused)
	}
	out := assist.LiveFact{Text: ans.Text, Via: ans.Via}
	for _, s := range ans.Sources {
		out.Sources = append(out.Sources, assist.LiveFactSource{N: s.N, URL: s.URL})
	}
	return out, nil
}

// useLiveFactLane gives the rig's matcher the model lanes with the given
// research service as the web search. The answer bank's settings stay at their
// defaults: the grounded option fallback is off.
func useLiveFactLane(t *testing.T, g *e2e, router *llm.Router, svc *research.Service) {
	t.Helper()
	ws := workspace.Open(filepath.Dir(filepath.Dir(g.db)))
	bank := answerbank.New(ws, g.st)
	lanes := assist.New(assist.Deps{Model: router, LiveFacts: researchFacts{svc}, Store: g.st})
	g.runner.d.Page.Matcher = bind.New(bind.Deps{Bank: bank, Workspace: ws, Assist: lanes, Observe: g.matches.Observe,
		Now: func() time.Time { return time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC) }})
}

// exaHits serves the research service the Exa client's results.
type exaHits struct{ p *signals.ExaSearch }

func (e exaHits) Search(ctx context.Context, query string, n int) ([]research.Hit, error) {
	found, err := e.p.Search(ctx, query, n, signals.Scope{})
	out := make([]research.Hit, len(found))
	for i, h := range found {
		out[i] = research.Hit{URL: h.URL, Title: h.Title, Text: cmp.Or(h.Text, h.Snippet)}
	}
	return out, err
}

// factSearch is the deterministic search source of the end-to-end run: two
// pages about the two public facts, and the queries it was asked.
type factSearch struct {
	mu      sync.Mutex
	queries []string
}

func (f *factSearch) Search(_ context.Context, query string, _ int) ([]research.Hit, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	switch {
	case strings.Contains(query, "Australia"):
		return []research.Hit{{URL: "https://atlas.example/geo/australia", Title: "Australia", Text: "Canberra is the capital of Australia."}}, nil
	case strings.Contains(query, "continents"):
		return []research.Hit{{URL: "https://atlas.example/geo/continents", Title: "Continents", Text: "By the usual convention there are seven continents."}}, nil
	}
	return nil, nil
}

func (f *factSearch) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Compact(slices.Sorted(slices.Values(f.queries)))
}

// factModel is the deterministic model of the research service: it answers from
// the numbered results the prompt carries, with a citation.
type factModel struct{}

func (factModel) NativeSearch(context.Context, string) bool { return false }

func (factModel) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	switch {
	case strings.Contains(req.Prompt, "Australia"):
		return &llm.Response{Text: "Canberra [1]"}, nil
	case strings.Contains(req.Prompt, "continents"):
		return &llm.Response{Text: "There are 7 continents [1]"}, nil
	}
	return &llm.Response{Text: research.Unanswerable}, nil
}

// northwindRun walks the fictional page with the persona and returns the run
// and the values the page held when it ended. The run is a dry run in a demo
// workspace; Submit is asked for and refused.
func northwindRun(t *testing.T, g *e2e, lane *browser.LaneHandle, pg *northwind) (*Run, map[string]string) {
	t.Helper()
	run := (&scenario{t: t, g: g}).run(Input{URL: pg.url, Caller: gates.CallerAskAgent, Tool: ToolAutofill, Submit: true, ExplicitRequest: true,
		UserMessage: "apply to " + pg.url + " and submit"})
	t.Cleanup(func() {
		for _, ti := range lane.Tabs() {
			if ti.Owner == run.ID {
				_ = lane.CloseTab(context.Background(), ti.ID)
			}
		}
	})
	if run.Outcome.Kind != OutcomeFilled {
		dump(t, run)
		t.Fatalf("outcome %+v, want a dry run that filled the form", run.Outcome)
	}
	return run, pg.values()
}

// recordOf is the matcher's record of the control with the given id: its
// outcome, the value it wrote and the trace, which names the pages a live fact
// rests on. The run's matcher calls are filed under the engine's own run id,
// the only one in the rig's log.
func recordOf(t *testing.T, g *e2e, id string) match.Result {
	t.Helper()
	if len(g.matches.byID) != 1 {
		t.Fatalf("the matcher log holds %d runs, want 1", len(g.matches.byID))
	}
	for runID := range g.matches.byID {
		if m, ok := g.matches.get(runID, `[id="`+id+`"]`); ok {
			return m.R
		}
	}
	t.Fatalf("the matcher has no record for the control %s", id)
	return match.Result{}
}

// A live-fact text question and a live-fact option question on a fictional
// company's page are answered from the cited search, a personal question is
// never searched, the ordinary fields come from the profile, and nothing is
// submitted. The search and its model are deterministic fakes behind the real
// research service, so the citation rules are the product's own.
func TestE2E_Demo_LiveFactQuestionsOnAFictionalPageAreAnsweredFromTheCitedSearch(t *testing.T) {
	inst, _ := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	slots <- struct{}{}
	defer func() { <-slots }()
	pg := northwindServer(t)
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	markDemo(t, g.st)
	usePersonaAnswers(t, g)
	search := &factSearch{}
	rig := newModelRig(t, g.st, nil)
	useLiveFactLane(t, g, rig.router, &research.Service{Model: factModel{}, Search: func() research.Searcher { return search }})

	run, page := northwindRun(t, g, lane, pg)

	// The text fact is typed as the cited sentence, without its marker or any
	// address; the option is the one the finding names; the personal question
	// and the controls the profile does not cover are blank.
	if page["capital"] != "Canberra" || page["continents"] != "7" || page["boardgame"] != "" {
		t.Fatalf("page values %v", page)
	}
	if page["first_name"] != "Alex" || page["last_name"] != "Example" || page["email"] != "alex@example.com" {
		t.Fatalf("the profile fields were not filled: %v", page)
	}
	for _, v := range page {
		if strings.Contains(v, "[") || strings.Contains(v, "http") {
			t.Fatalf("a marker or an address was typed: %v", page)
		}
	}
	// The run record names the pages each fact rests on, and only for them.
	text, option, personal := recordOf(t, g, "capital"), recordOf(t, g, "continents"), recordOf(t, g, "boardgame")
	if text.Outcome != match.OutcomeFilledModel || text.Answer != "Canberra" || !strings.Contains(strings.Join(text.Trace, "\n"), "https://atlas.example/geo/australia") {
		t.Fatalf("text record %+v", text)
	}
	if option.Outcome != match.OutcomeFilledModel || option.ChosenLabel != "7" || !strings.Contains(strings.Join(option.Trace, "\n"), "https://atlas.example/geo/continents") {
		t.Fatalf("option record %+v", option)
	}
	if personal.Outcome != match.OutcomeAbstained || personal.Answer != "" || strings.Contains(strings.Join(personal.Trace, "\n"), "https://") {
		t.Fatalf("personal record %+v", personal)
	}
	// Only the two impersonal questions were searched, and the query held none
	// of the persona's data.
	if got := search.asked(); !slices.Equal(got, []string{liveOptionQuestion, liveTextQuestion}) {
		t.Fatalf("searched %q", got)
	}
	// No model picked or composed anything: the router saw no call at all.
	if len(rig.calls) != 0 {
		t.Fatalf("the router saw %d calls: %+v", len(rig.calls), rig.calls)
	}
	// Nothing was submitted: the page saw no submission, the ledger no row, and
	// the demo gate refused the explicit Submit.
	if pg.submitted.Load() != 0 || g.count(t, `SELECT count(*) FROM submissions`) != 0 {
		t.Fatalf("the page saw %d submissions", pg.submitted.Load())
	}
	if n := g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'submit_authorization' AND code = 'demo_workspace' AND run_id = ?`, run.ID); n != 1 {
		t.Fatalf("%d submit refusals with demo_workspace, want 1", n)
	}
}

// The same page against the real router of a demo workspace: the routed
// provider's own web search answers two stable public facts. It costs a few
// cents, so it runs only when APPLYSMITH_LIVE_FACT_E2E=1; the workspace is
// APPLYSMITH_LIVE_FACT_WORKSPACE or ~/Applysmith-Demo. Its database is copied, the
// key is read from the keychain in process and never printed or stored.
func TestE2E_Demo_LiveFactQuestionsAgainstTheRealRouter(t *testing.T) {
	if os.Getenv("APPLYSMITH_LIVE_FACT_E2E") != "1" {
		t.Skip("set APPLYSMITH_LIVE_FACT_E2E=1 to spend a few cents on real searches")
	}
	root := os.Getenv("APPLYSMITH_LIVE_FACT_WORKSPACE")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		root = filepath.Join(home, demo.DefaultDir)
	}
	if !workspaceIsDemo(root) {
		t.Fatalf("%s is not a demo workspace", root)
	}
	id, err := os.ReadFile(filepath.Join(root, ".applysmith", "workspace-id"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(filepath.Join(root, ".applysmith", store.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), store.DBFile)
	if err := os.WriteFile(copyPath, db, 0o600); err != nil {
		t.Fatal(err)
	}
	routerStore, err := store.Open(context.Background(), copyPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer routerStore.Close()
	keys, err := vault.New(strings.TrimSpace(string(id)), vault.NewKeychain())
	if err != nil {
		t.Fatal(err)
	}
	router := llm.New(routerStore, keys)
	// The search source the product would use: the routed provider's own search,
	// else the workspace's Exa key, as internal/api wires it.
	svc := &research.Service{Model: router}
	if key, err := keys.Get(signals.KeyExa); err == nil && key != "" {
		exa := &signals.ExaSearch{Fetch: signals.NewFetcher(nil), Base: "https://api.exa.ai", Key: key}
		svc.Search = func() research.Searcher { return exaHits{exa} }
	}

	inst, _ := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	slots <- struct{}{}
	defer func() { <-slots }()
	pg := northwindServer(t)
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	markDemo(t, g.st)
	usePersonaAnswers(t, g)
	useLiveFactLane(t, g, router, svc)
	var before int64
	_ = routerStore.Read().QueryRow(`SELECT COALESCE(max(id), 0) FROM llm_ledger`).Scan(&before)

	run, page := northwindRun(t, g, lane, pg)
	t.Logf("run %s outcome=%s pages=%d filled=%d", run.ID, run.Outcome.Kind, len(run.Pages), run.Pages[0].Filled)
	text, option, personal := recordOf(t, g, "capital"), recordOf(t, g, "continents"), recordOf(t, g, "boardgame")
	for name, r := range map[string]match.Result{"capital": text, "continents": option, "boardgame": personal} {
		t.Logf("%-10s outcome=%s source=%s answer=%q chosen=%q reason=%s", name, r.Outcome, r.Source, r.Answer, r.ChosenLabel, r.Reason)
		for _, l := range r.Trace {
			t.Logf("%-10s   trace: %s", name, l)
		}
	}
	t.Logf("page values: capital=%q continents=%q boardgame=%q", page["capital"], page["continents"], page["boardgame"])
	var calls int
	var cost float64
	_ = routerStore.Read().QueryRow(`SELECT count(*), COALESCE(sum(cost_usd), 0) FROM llm_ledger WHERE id > ? AND task = ?`, before, research.Task).Scan(&calls, &cost)
	t.Logf("this run on the copy's ledger: %d %s calls, USD %.4f", calls, research.Task, cost)
	if !strings.Contains(page["capital"], "Canberra") || page["continents"] != "7" || page["boardgame"] != "" {
		t.Fatalf("page values %v", page)
	}
	if !strings.Contains(strings.Join(text.Trace, "\n"), "http") || !strings.Contains(strings.Join(option.Trace, "\n"), "http") {
		t.Fatalf("the traces name no page: %v / %v", text.Trace, option.Trace)
	}
	if pg.submitted.Load() != 0 || g.count(t, `SELECT count(*) FROM submissions`) != 0 {
		t.Fatal("something was submitted")
	}
}

// workspaceIsDemo reads the demo marker a workspace keeps in its state folder.
func workspaceIsDemo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".applysmith", workspace.DemoMarkerName))
	return err == nil
}

// adpCodePage is an ADP-shaped application: a first page of contact boxes
// whose Continue raises a verification-code dialog in a custom element (a
// heading attribute, one box, a Verify button disabled until six characters
// stand in the box, a countdown for Resend) over the page, and a second page
// that holds a question and the application's Submit. decoy adds a
// Submit-looking button to the dialog that would write the application;
// confirm names the dialog's confirm control. The page reports its steps
// without the code.
func adpCodePage(confirm string, decoy bool) func(*board, *http.Request) string {
	return func(b *board, r *http.Request) string {
		questions, _ := json.Marshal(fieldQuestions)
		extra := ""
		if decoy {
			extra = `<button type="button" id="decoy" onclick="sendApplication()">Submit application</button>`
		}
		return `<title>Apply - Example Corp (fictional)</title><h1>Platform Engineer</h1><div id="page">
<form id="f1" onsubmit="return false">` + fieldNames + `<button type="button" id="next" onclick="openOtp()">Continue</button></form></div>
<sdf-focus-pane heading="Enter the Verification Code" id="otp" style="display:none;position:fixed;left:15%;top:8%;width:70%;background:#fff;border:2px solid #333;padding:16px;z-index:1000">
<div class="otpDialogContent"><label class="otpInstructionText">Verification Code sent to your email address</label>
<div><input id="oneTimePassWord" aria-label="Enter the Verification Code" maxlength="6" type="text" oninput="gate()"></div>
<div><button type="button" id="verify" disabled onclick="verify()">` + confirm + `</button>` + extra + `</div>
<div id="timer">Did not receive code? Resend code in 59 seconds.</div><div>Powered by ADP</div></div></sdf-focus-pane>
<script>
function ev(e) { fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e: e}), keepalive: true}); }
function openOtp() { document.getElementById('otp').style.display = 'block'; ev('otp-opened'); }
function gate() { document.getElementById('verify').disabled = document.getElementById('oneTimePassWord').value.length !== 6; }
function verify() {
  ev('verify:' + (document.getElementById('oneTimePassWord').value === '482916' ? 'ok' : 'bad'));
  document.getElementById('otp').style.display = 'none';
  document.getElementById('page').innerHTML = '<form id="f2" onsubmit="return false">' + ` + string(questions) + ` + '<button type="button" onclick="sendApplication()">Submit application</button></form>';
}
</script>` + submitScript(b, successHTML)
	}
}

// A demo workspace reads the one code only when the persona's address is a
// plus alias of the account the lane reads: the lookup is bounded to the
// sender and to mail after the dialog opened, the code is typed and Verify is
// pressed; with any other address the run stops on the dialog with nothing
// read, typed or pressed.
func TestE2E_CodeDialog_ADemoReadsTheCodeOnlyForAPlusAliasOfTheConnectedAccount(t *testing.T) {
	codeSenders = append(codeSenders, struct{ host, sender string }{"careers.example.test", SenderADP})
	defer func() { codeSenders = codeSenders[:len(codeSenders)-1] }()
	owner, alias := "owner@example.test", "owner+applysmithdemo@example.test"
	persona := func(email string) string { return strings.Replace(personaApplicantJSON, "alex@example.com", email, 1) }
	for name, c := range map[string]struct {
		email string
		reads bool
	}{"a plus alias of the account": {alias, true}, "an address that is not an alias": {"alex@example.com", false}, "an alias of another account": {"other+applysmithdemo@example.test", false}} {
		t.Run(name, func(t *testing.T) {
			s := newScenario(t, "custom", modeAccept, Hooks{})
			s.b.render = adpCodePage("Verify", true)
			s.g.applicant = persona(c.email)
			fc := &fakeCodes{account: owner, replies: []codeReply{notYet(), mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", time.Now())}}
			markDemo(t, s.g.st)
			s.g.runner.d.Page.Codes, s.g.runner.d.Page.Code = fc, CodeTiming{Poll: 50 * time.Millisecond, GiveUp: 20 * time.Second}
			run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
			defer dump(t, run)
			ev := strings.Join(s.b.eventList(), ",")
			if c.reads {
				if run.Outcome.Kind != OutcomeFilled || ev != "otp-opened,verify:ok" {
					t.Fatalf("outcome %+v events %q, want the code typed and Verify pressed", run.Outcome, ev)
				}
				for _, g := range fc.grants {
					if g == "" || !strings.HasPrefix(g, SenderADP+"|") {
						t.Fatalf("lookup grants %q: every lookup carries the gate's bound", fc.grants)
					}
				}
				if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND verdict = 'allow' AND reason LIKE '%verification code%' AND run_id = ?`, run.ID); n != 1 {
					t.Fatalf("%d audited allows, want one", n)
				}
				if strings.Contains(fmt.Sprintf("%+v", run), "482916") {
					t.Fatal("the code reached the run record")
				}
			} else {
				o := run.Outcome
				if o.Kind != OutcomeStopped || o.Class != ClassVerification || !strings.Contains(o.Reason, "this run cannot read mail (demo workspace: ") {
					t.Fatalf("outcome %+v, want a verification stop that names the demo", o)
				}
				if ev != "otp-opened" || len(fc.since) != 0 {
					t.Fatalf("events %q lookups %d: nothing is pressed, typed or read", ev, len(fc.since))
				}
			}
			if s.b.presses() != 0 || s.g.count(t, `SELECT count(*) FROM submissions`) != 0 {
				t.Fatalf("presses %d: a demo never submits", s.b.presses())
			}
		})
	}
}

// With a mail lane the dialog is answered inside the run: the newest code
// after the dialog opened is typed, the dialog's Verify (never its decoy
// Submit) is pressed once it enables, and the walk goes on to the next page.
func TestE2E_CodeDialog_ACodeFromMailIsTypedVerifyPressedAndTheWalkContinues(t *testing.T) {
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = adpCodePage("Verify", true)
	fc := &fakeCodes{replies: []codeReply{notYet(), mailed("482916", mailFrom("SecurityServices_NoReply", "adp.com"), "Your verification code", time.Now())}}
	s.g.runner.d.Page.Codes, s.g.runner.d.Page.Code = fc, CodeTiming{Poll: 50 * time.Millisecond, GiveUp: 20 * time.Second}
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("outcome %+v, want a dry run that walked past the dialog", run.Outcome)
	}
	if ev := strings.Join(s.b.eventList(), ","); ev != "otp-opened,verify:ok" {
		t.Fatalf("page events %q, want the dialog opened and the right code verified once", ev)
	}
	if s.b.presses() != 0 || s.g.count(t, `SELECT count(*) FROM submissions`) != 0 {
		t.Fatalf("presses %d: the dialog's Submit-looking button was pressed", s.b.presses())
	}
	if len(run.Pages) < 2 {
		t.Fatalf("pages %+v, want the walk to reach the page after the dialog", run.Pages)
	}
	if strings.Contains(fmt.Sprintf("%+v", run), "482916") {
		t.Fatal("the code reached the run record")
	}
	notes := fmt.Sprint(run.Pages[0].Notes)
	if !strings.Contains(notes, "code dialog: a code was typed") || !strings.Contains(notes, "code dialog: Verify pressed") {
		t.Fatalf("page notes %q: each decision of the dialog is traced", notes)
	}
}
