//go:build browser

package apply

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/documents"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// resumeFixture is a run that stopped at a sign-in wall and left its tab for
// the user, who then signs in by hand.
type resumeFixture struct {
	l     *loginScenario
	first *Run
	tab   string
	in    Input
}

func agentInput(url string, submit bool) Input {
	return Input{URL: url, Submit: submit, ExplicitRequest: true, Caller: gates.CallerAskAgent, Tool: ToolAutofill, ConversationID: "chat_1",
		UserMessage: "apply to " + url}
}

// typeInto focuses a control of a tab with a real click and types as a person does.
func typeInto(t *testing.T, ctx context.Context, tab Tab, sel, text string) {
	t.Helper()
	clickOn(t, ctx, tab, sel)
	if err := tab.Eval(ctx, `document.querySelector(`+jsString(sel)+`).select()`, nil); err != nil {
		t.Fatal(err)
	}
	if err := tab.TypeText(ctx, text); err != nil {
		t.Fatal(err)
	}
}

func jsString(s string) string { return "'" + strings.ReplaceAll(s, "'", `\'`) + "'" }

// panelControl is the app's browser panel on the shared test browser: the
// person's own clicks and keys reach a kept tab through it.
func panelControl() *browser.Control {
	return browser.NewControl(browser.Options{ProfileDir: filepath.Join(shared.root, "applysmith-apply-e2e-profile")})
}

// signedInByHand runs the chain to its stop at the sign-in wall of a board no
// stored credential opens, then signs in inside the kept tab with real input.
func signedInByHand(t *testing.T, submit bool) *resumeFixture {
	t.Helper()
	const password = "By-Hand-1!x"
	l := newLogin(t, password)
	l.g.seedPosting(t, l.url)
	in := agentInput(l.url, submit)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	first := l.g.runner.RunOne(ctx, in)
	t.Cleanup(func() { dump(t, first) })
	if first.Outcome.Kind != OutcomeStopped || first.Outcome.Board != "sign-in" || first.Outcome.Class != ClassCredential {
		t.Fatalf("the first run %+v, want a stop for a sign-in", first.Outcome)
	}
	tabID, ok := l.g.runner.Keeper().TabOf(first.ID)
	if !ok {
		t.Fatal("the stop kept no tab for the user")
	}
	if n := l.b.pwTries.Load(); n != 0 {
		t.Fatalf("the chain submitted %d passwords with none stored", n)
	}
	// The user takes the tab over: real clicks and real typing.
	tab, err := LaneWindow{Lane: l.lane}.Reclaim(ctx, tabID, engine.Host{})
	if err != nil {
		t.Fatal(err)
	}
	typeInto(t, ctx, tab, "#em", "alex@example.com")
	typeInto(t, ctx, tab, "#pw", password)
	clickOn(t, ctx, tab, "button")
	eventually(t, 15*time.Second, "the page to show the application", func() bool {
		var has bool
		return tab.Eval(ctx, `!document.getElementById('pw')`, &has) == nil && has
	})
	if n := l.b.pwTries.Load(); n != 1 {
		t.Fatalf("the server saw %d sign-ins, want the user's one", n)
	}
	// The user leaves the tab as it was: detached from automation again.
	if err := tab.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return &resumeFixture{l: l, first: first, tab: tabID, in: in}
}

func (f *resumeFixture) resume(t *testing.T, mod func(*ResumeInput)) (*Run, PageGlance) {
	t.Helper()
	var glance PageGlance
	in := ResumeInput{RunID: f.first.ID, TabID: f.tab, Input: f.in, Read: func(g PageGlance) { glance = g }}
	if mod != nil {
		mod(&in)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	run := f.l.g.runner.Resume(ctx, in)
	t.Cleanup(func() { dump(t, run) })
	return run, glance
}

func TestE2E_Resume_SignedInByHandThenDoneReadsThePageAgainAndFillsTheForm(t *testing.T) {
	f := signedInByHand(t, false)
	run, glance := f.resume(t, nil)
	if run.ID != f.first.ID || run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("run %s outcome %+v", run.ID, run.Outcome)
	}
	if glance.SignIn || glance.Controls < 3 || !strings.Contains(glance.URL, "/jobs/") || strings.Contains(glance.URL, "?") {
		t.Fatalf("the fresh read: %+v", glance)
	}
	if n := f.l.b.pwTries.Load(); n != 1 {
		t.Fatalf("the resumed run submitted a password: the server saw %d sign-ins", n)
	}
	if len(run.Pages) == 0 || run.Pages[len(run.Pages)-1].Filled == 0 {
		t.Fatalf("nothing was filled after the resume: %+v", run.Pages)
	}
	var kinds []string
	for _, l := range run.Stages {
		kinds = append(kinds, string(l.Stage)+":"+string(l.Status))
	}
	if stageOf(run, StageAccount).Status != StatusFailed && !strings.Contains(strings.Join(kinds, " "), "account:failed") {
		t.Fatalf("the earlier stop was not kept in the record: %v", kinds)
	}
	if n := f.l.g.count(t, `SELECT count(*) FROM apply_runs WHERE run_id = ?`, run.ID); n != 1 {
		t.Fatalf("%d rows for the run", n)
	}
	// The tab is the user's and is still open, with the form the run filled.
	if _, ok := f.l.g.runner.Keeper().TabOf(run.ID); ok {
		t.Error("a finished run still offers its tab")
	}
	if f.l.b.presses() != 0 {
		t.Fatalf("Submit was pressed on a dry run: %d", f.l.b.presses())
	}
}

func TestE2E_Resume_AResumedSubmitPassesTheSubmitGateAgainAndIsPressedOnce(t *testing.T) {
	f := signedInByHand(t, true)
	var rechecks int
	run, _ := f.resume(t, func(in *ResumeInput) {
		in.Recheck = func(context.Context) gates.Decision {
			rechecks++
			return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.Allow, Code: "exact_url_named", Reason: "the user's message names this exact posting URL"}
		}
	})
	if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if rechecks != 1 || f.l.b.presses() != 1 || f.l.b.recorded() != 1 {
		t.Fatalf("rechecked %d times, %d presses, %d recorded", rechecks, f.l.b.presses(), f.l.b.recorded())
	}
	// Both the Ask Agent's gate and the submit authorization decided again, and
	// both are in the audit under the run's own id.
	for _, gate := range []string{string(gates.GateAgentSubmit), string(gates.GateSubmitAuthorization)} {
		if n := f.l.g.count(t, `SELECT count(*) FROM gate_decisions WHERE run_id = ? AND gate = ?`, run.ID, gate); n < 1 {
			t.Errorf("no %s decision is audited for the resumed run", gate)
		}
	}
	if st, confirmed, _ := f.l.g.ledgerState(t); st == "none" || !confirmed {
		t.Fatalf("ledger %s confirmed %v", st, confirmed)
	}
}

// ---- a field the user corrected stays as they left it

func hobbyPage(b *board, r *http.Request) string {
	return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` + fieldNames +
		`<label for="city">City *</label><input id="city" name="city" required>
<label for="why">Describe a project you are proud of *</label><textarea id="why" name="why" required></textarea>
<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
}

// hobbyFramed is the same application inside a same-origin frame of the
// careers page, as an embedded system shows it.
func hobbyFramed(b *board, r *http.Request) string {
	if r.URL.Query().Get("in_frame") != "" {
		return hobbyPage(b, r)
	}
	return `<title>Careers - Example Corp</title><iframe id="app" src="?in_frame=1" style="width:720px;height:900px;border:0"></iframe>`
}

func TestE2E_Resume_AFieldTheUserCorrectedIsLeftAsTheyLeftItAndTheirAnswerCompletesTheForm(t *testing.T) {
	for _, c := range []struct {
		name   string
		render func(*board, *http.Request) string
		doc    string // the document that holds the form, from the top one
	}{
		{"the form is the page", hobbyPage, "document"},
		{"the form is in a same-origin frame", hobbyFramed, "document.getElementById('app').contentDocument"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newScenario(t, "custom", modeAccept, Hooks{})
			s.b.render = c.render
			in := agentInput(s.url, false)
			first := s.run(in)
			t.Cleanup(func() { dump(t, first) })
			if first.Outcome.Kind != OutcomeStopped || first.Outcome.Board != "answer" {
				t.Fatalf("the first run %+v, want a stop for an answer", first.Outcome)
			}
			tabID, ok := s.g.runner.Keeper().TabOf(first.ID)
			if !ok {
				t.Fatal("no tab was kept")
			}
			ctx := context.Background()
			// The lane attaches to the tab for the test only, to type as a person would,
			// and lets go again.
			asUser := func(f func(Tab)) {
				t.Helper()
				tab, err := LaneWindow{Lane: s.lane}.Reclaim(ctx, tabID, engine.Host{})
				if err != nil {
					t.Fatal(err)
				}
				defer tab.Close(ctx)
				f(tab)
			}
			control := func(sel string) string { return c.doc + `.querySelector(` + jsString(sel) + `)` }
			read := func(sel string) (v string) {
				t.Helper()
				asUser(func(tab Tab) {
					if err := tab.Eval(ctx, control(sel)+`.value`, &v); err != nil {
						t.Fatal(err)
					}
				})
				return v
			}
			// at is the centre of a control in the tab's viewport, its frame's offset added.
			at := func(tab Tab, sel string) (x, y float64) {
				t.Helper()
				var r struct{ X, Y float64 }
				expr := `(function(){var f=` + c.doc + `.defaultView.frameElement,o=f?f.getBoundingClientRect():{left:0,top:0},b=` + control(sel) +
					`.getBoundingClientRect();return {X:o.left+b.left+b.width/2,Y:o.top+b.top+b.height/2}})()`
				if err := tab.Eval(ctx, expr, &r); err != nil {
					t.Fatal(err)
				}
				return r.X, r.Y
			}
			click := func(tab Tab, sel string) {
				t.Helper()
				x, y := at(tab, sel)
				if err := tab.Click(ctx, x, y); err != nil {
					t.Fatal(err)
				}
			}
			typeIn := func(tab Tab, sel, text string) {
				t.Helper()
				click(tab, sel)
				if err := tab.Eval(ctx, control(sel)+`.select()`, nil); err != nil {
					t.Fatal(err)
				}
				if err := tab.TypeText(ctx, text); err != nil {
					t.Fatal(err)
				}
			}
			if got := read("#city"); got != "Durham" {
				t.Fatalf("the run left %q in City, want Durham", got)
			}
			if got := read("#phone"); got == "" {
				t.Fatal("the run left Phone empty; the user has nothing to remove")
			}
			asUser(func(tab Tab) {
				typeIn(tab, "#city", "Raleigh")
				typeIn(tab, "#why", "A scheduler for the neighbourhood chess club.")
				click(tab, "h1")
			})
			// The user empties the optional Phone the run filled from the profile, with
			// the keys of the app's browser panel on the kept tab.
			var px, py float64
			asUser(func(tab Tab) { px, py = at(tab, "#phone") })
			keys := []browser.InputEvent{{Kind: "mouse", Type: "down", X: px, Y: py, Button: "left", Buttons: 1, ClickCount: 1},
				{Kind: "mouse", Type: "up", X: px, Y: py, Button: "left", ClickCount: 1}, {Kind: "key", Type: "down", Key: "End", Code: "End", KeyCode: 35}}
			for range 20 {
				keys = append(keys, browser.InputEvent{Kind: "key", Type: "down", Key: "Backspace", Code: "Backspace", KeyCode: 8},
					browser.InputEvent{Kind: "key", Type: "up", Key: "Backspace", Code: "Backspace", KeyCode: 8})
			}
			if err := panelControl().Input(ctx, tabID, keys); err != nil {
				t.Fatal(err)
			}
			if got := read("#phone"); got != "" {
				t.Fatalf("Phone reads %q after the user's keys", got)
			}
			// The engine left the tab with the hand-over, so the resumed run puts a new
			// one in, which knows nothing of what the user did; only what the run
			// recorded as it handed the tab over does.
			rctx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			run := s.g.runner.Resume(rctx, ResumeInput{RunID: first.ID, TabID: tabID, Input: in})
			t.Cleanup(func() { dump(t, run) })
			if run.Outcome.Kind != OutcomeFilled {
				t.Fatalf("outcome %+v", run.Outcome)
			}
			if got := read("#city"); got != "Raleigh" {
				t.Fatalf("City reads %q after the resume: the user's correction was overwritten", got)
			}
			if got := read("#why"); !strings.HasPrefix(got, "A scheduler") {
				t.Fatalf("the user's answer is gone: %q", got)
			}
			if got := read("#phone"); got != "" {
				t.Fatalf("Phone reads %q after the resume: the box the user emptied was filled again", got)
			}
			if got := read("#firstName"); got != "Alex" {
				t.Fatalf("First Name reads %q: the run's own fill did not go on", got)
			}
		})
	}
}

// ---- what a run types in a tab it took back is not the user's

func TestE2E_Resume_WhatTheRunTypesInATabItTookBackIsNeverRecordedAsTheUsers(t *testing.T) {
	var chainTypes func()
	s := newScenario(t, "custom", modeRefuse, Hooks{AfterClaim: func() {
		if chainTypes != nil {
			chainTypes()
		}
	}})
	s.b.render = hobbyPage
	p := &posts{}
	wctx, stopWatches := context.WithCancel(context.Background())
	// A watch reads when it starts and when it is stopped, and never between:
	// a read between could not tell the user's edits from the run's.
	w := &Watcher{Ctx: wctx, Poster: p, Every: time.Hour}
	s.g.runner.d.Page.Keep.Watch = w
	t.Cleanup(func() { stopWatches(); w.Wait() })

	in := agentInput(s.url, true)
	first := s.run(in)
	t.Cleanup(func() { dump(t, first) })
	tabID, ok := s.g.runner.Keeper().TabOf(first.ID)
	if first.Outcome.Board != "answer" || !ok || w.Active() != 1 {
		t.Fatalf("the first run %+v kept a tab %v with %d watches, want a watched stop for an answer", first.Outcome, ok, w.Active())
	}
	ctx := context.Background()
	asUser := func(f func(Tab)) {
		t.Helper()
		tab, err := LaneWindow{Lane: s.lane}.Reclaim(ctx, tabID, engine.Host{})
		if err != nil {
			t.Fatal(err)
		}
		defer tab.Close(ctx)
		f(tab)
	}
	armed := func() bool {
		var b sealedBuffer
		asUser(func(tab Tab) { _ = tab.Eval(ctx, recorderReadScript("", 0), &b) })
		return b.Armed
	}
	// The user types through the app's browser panel: a click into the box, its
	// text removed, the new text, and a click on the heading to leave it.
	panelType := func(sel, text string) {
		t.Helper()
		var x, y, hx, hy float64
		asUser(func(tab Tab) { x, y = centre(t, ctx, tab, sel); hx, hy = centre(t, ctx, tab, "h1") })
		click := func(cx, cy float64) []browser.InputEvent {
			return []browser.InputEvent{{Kind: "mouse", Type: "down", X: cx, Y: cy, Button: "left", Buttons: 1, ClickCount: 1},
				{Kind: "mouse", Type: "up", X: cx, Y: cy, Button: "left", ClickCount: 1}}
		}
		ev := append(click(x, y), browser.InputEvent{Kind: "key", Type: "down", Key: "End", Code: "End", KeyCode: 35})
		for range 30 {
			ev = append(ev, browser.InputEvent{Kind: "key", Type: "down", Key: "Backspace", Code: "Backspace", KeyCode: 8},
				browser.InputEvent{Kind: "key", Type: "up", Key: "Backspace", Code: "Backspace", KeyCode: 8})
		}
		ev = append(append(ev, browser.InputEvent{Kind: "text", Text: text}), click(hx, hy)...)
		if err := panelControl().Input(ctx, tabID, ev); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 15*time.Second, "the recorder in the kept tab", armed)
	panelType("#city", "Raleigh")
	panelType("#why", "A scheduler for the neighbourhood chess club.")

	// Once the run has the tab back it types into City with its own DevTools
	// input, as an editor's typing or a box the walk ticks does.
	chainTypes = func() {
		if _, err := s.lane.Evaluate(ctx, tabID, `(function(){var c=document.getElementById('city');c.focus();c.select();return 1})()`); err != nil {
			t.Error(err)
			return
		}
		if err := s.lane.TypeText(ctx, tabID, "Chapel Hill"); err != nil {
			t.Error(err)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	run := s.g.runner.Resume(rctx, ResumeInput{RunID: first.ID, TabID: tabID, Input: in, Recheck: func(context.Context) gates.Decision {
		return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.Allow, Code: "exact_url_named", Reason: "the user's message names this exact posting URL"}
	}})
	t.Cleanup(func() { dump(t, run) })
	if again, _ := s.g.runner.Keeper().TabOf(run.ID); run.Outcome.Class != ClassDefect || s.b.presses() != 1 || again != tabID {
		t.Fatalf("outcome %+v after %d presses, tab %q: the refused press keeps the tab again", run.Outcome, s.b.presses(), again)
	}
	// Kept again, the tab is watched again, and the user's next edit is theirs.
	eventually(t, 15*time.Second, "the recorder in the tab kept again", armed)
	panelType("#city", "Cary")
	w.Stop(tabID)

	var city []string
	answers := 0
	for _, ev := range p.events() {
		switch ev.Question {
		case "City":
			city = append(city, ev.After)
		case "Describe a project you are proud of":
			answers++
		}
	}
	if strings.Join(city, ",") != "Raleigh,Cary" || answers != 1 {
		t.Fatalf("City was recorded as %q and the answer %d times; the user wrote Raleigh, the answer and then Cary, once each", city, answers)
	}
}

// coverLetterForm is an application with a required cover-letter upload beside
// the résumé.
func coverLetterForm(b *board, _ *http.Request) string {
	return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` +
		fieldNames + fieldResume +
		`<label for="cover_letter">Cover Letter *</label><input id="cover_letter" type="file" name="cover_letter" required>` +
		fieldQuestions + `<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
}

// A form that asks for a cover letter, for a posting with no report: the run
// evaluates the posting in full first, then writes the letter from that
// report, then attaches it, and the application carries it.
func TestE2E_CoverLetter_FormSlotWithoutReportEvaluatesFirst(t *testing.T) {
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = coverLetterForm
	var tracker int64
	if err := s.g.st.Read().QueryRow(`SELECT id FROM tracker_entries ORDER BY id LIMIT 1`).Scan(&tracker); err != nil {
		t.Fatal(err)
	}
	fd := &fakeDocs{verify: map[int64]*documents.Verification{}}
	fl := &fakeLetters{docs: fd, report: &ReportBinding{Number: 31, Kind: ReportFinal, TrackerID: tracker, Company: "Example Corp", Role: "Platform Engineer"}}
	s.g.runner.d.Page.Documents, s.g.runner.d.Page.Letters = fd, fl

	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeSubmitted {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if got := strings.Join(fl.log(), ","); got != "evaluate "+s.url+",write "+strconv.FormatInt(tracker, 10) {
		t.Fatalf("calls %q: the posting is evaluated first, then the letter is written for its candidacy", got)
	}
	body, _ := s.b.lastBody.Load().(string)
	if !strings.Contains(body, `"cover_letter":"cover-letter.pdf"`) || !strings.Contains(body, `"resume":"resume-alex-example.pdf"`) {
		t.Fatalf("application body %s: the written letter went into its slot and the résumé into its own", body)
	}
	if len(run.Pages) == 0 || !strings.Contains(strings.Join(run.Pages[0].Notes, "\n"), "evaluated first (report #031)") {
		t.Fatalf("page notes %v", run.Pages)
	}
}

// Teamtailor's letter is a text box: it gets the letter's text, "Why us?" gets
// none, and the statement reads Yes from what the run did.
func TestE2E_CoverLetter_TextBoxGetsTheLettersText(t *testing.T) {
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = func(b *board, _ *http.Request) string {
		return `<title>Platform Engineer - Example Corp</title><form id="f" onsubmit="return false">` + fieldNames + fieldResume +
			`<label for="cl">Cover letter<sup>*</sup><span>Required</span></label><textarea id="cl" required></textarea>` +
			`<label for="why">Why do you want to work here?</label><textarea id="why"></textarea><fieldset><legend>Did you submit a cover letter? *</legend><label><input type="radio" name="sent" value="Yes" required> Yes</label><label><input type="radio" name="sent" value="No"> No</label></fieldset>` +
			fieldQuestions + `<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
	}
	var tracker int64
	if err := s.g.st.Read().QueryRow(`SELECT id FROM tracker_entries ORDER BY id LIMIT 1`).Scan(&tracker); err != nil {
		t.Fatal(err)
	}
	fd := &fakeDocs{verify: map[int64]*documents.Verification{50: {State: documents.PDFReady}}}
	s.g.runner.d.Page.Documents, s.g.runner.d.Page.Letters = fd, &fakeLetters{docs: fd, report: &ReportBinding{Number: 31, Kind: ReportFinal, TrackerID: tracker}}
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	var sent map[string]string
	body, _ := s.b.lastBody.Load().(string)
	if err := json.Unmarshal([]byte(body), &sent); err != nil || run.Outcome.Kind != OutcomeSubmitted || sent["cl"] != fixtureLetter || sent["why"] != "" || sent["sent"] != "Yes" {
		t.Fatalf("outcome %+v, body %s", run.Outcome, body)
	}
}
