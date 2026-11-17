//go:build browser

package apply

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/gates"
	"applysmith/internal/workspace"
)

func pressEvents(b *board) int {
	n := 0
	for _, e := range b.eventList() {
		if strings.HasPrefix(e, "press:") {
			n++
		}
	}
	return n
}

func TestE2E_Apply_RepairRoundResubmitsOnTheSameClaim(t *testing.T) {
	s := newScenario(t, "repair", modeValidateOnce, Hooks{})
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if pressEvents(s.b) != 2 || s.b.recorded() != 1 || run.Presses != 2 {
		t.Fatalf("page saw %d presses, server recorded %d, run counts %d", pressEvents(s.b), s.b.recorded(), run.Presses)
	}
	if !strings.Contains(stageOf(run, StageSubmit).Detail, "repair round: fields=City") {
		t.Fatalf("submit line %q", stageOf(run, StageSubmit).Detail)
	}
	if n := s.g.count(t, `SELECT count(*) FROM submissions`); n != 1 {
		t.Fatalf("%d ledger rows", n)
	}
	if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = ? AND run_id = ?`, string(gates.GatePressClaim), run.ID); n != 1 {
		t.Fatalf("%d press claims", n)
	}
	if body, _ := s.b.lastBody.Load().(string); !strings.Contains(body, `"city":"Durham"`) {
		t.Fatalf("application body %s: the repair re-wrote the named field", body)
	}
}

func TestE2E_Apply_LadderGroundedRungAnswersTheOpenQuestionFromTheProfile(t *testing.T) {
	s := newScenario(t, "ladder", modeAccept, Hooks{})
	m := newModelRig(t, s.g.st, script(`[{"id":1,"answer":"Go and SQL","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"))
	s.g.runner.d.Page.Model = m.router
	s.g.runner.d.Page.Own = func(context.Context) (Own, error) { return ladderOwn, nil }
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeSubmitted {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if body, _ := s.b.lastBody.Load().(string); !strings.Contains(body, `"langs":"Go and SQL"`) {
		t.Fatalf("application body %s", body)
	}
	if run.ModelCalls != 2 || m.count(taskComplete) != 2 {
		t.Fatalf("run counts %d model calls, router saw %d: one grounded call and one support check", run.ModelCalls, m.count(taskComplete))
	}
	var rung2 int
	for _, p := range run.Pages {
		rung2 += p.Ladder.Rung2
	}
	if rung2 != 1 {
		t.Fatalf("pages %+v", run.Pages)
	}
}

// Ashby's checkbox group, every box named after its own option, is one
// question: an answer the profile cannot give stops the run at check as a
// required leftover that names the question, before any press.
func TestApply_Ashby_CheckboxGroupIsOneQuestion(t *testing.T) {
	s := newScenario(t, "ashby-boxes", modeAccept, Hooks{})
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeStopped || run.Outcome.Stage != StageCheck || run.Outcome.Class != ClassAnswerGap {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	if !strings.Contains(run.Outcome.Reason, "shipped") {
		t.Fatalf("the stop does not name the question: %q", run.Outcome.Reason)
	}
	var unresolved []string
	for _, p := range run.Pages {
		unresolved = append(unresolved, p.Unresolved...)
	}
	if n := strings.Count(strings.Join(unresolved, "|"), "shipped"); n != 1 {
		t.Fatalf("the group is %d leftovers, want one question: %v", n, unresolved)
	}
	// The run record keeps the question with its options, so the user can answer it from there.
	if l := run.Pages[len(run.Pages)-1].Leftovers; len(l) != 1 || l[0].Draft == nil || !strings.Contains(l[0].Label, "shipped") || len(l[0].Draft.Options) != 3 {
		t.Fatalf("recorded leftovers %+v", l)
	}
	if s.b.presses() != 0 {
		t.Fatalf("%d presses before the check stop", s.b.presses())
	}
}

// The real matcher and DOM engine must agree on a negative option before a
// final press, even when the page encodes the contraction's apostrophe.
func TestE2E_Apply_AcknowledgementNegationBeforeSubmit(t *testing.T) {
	for _, tc := range []struct {
		name, options        string
		legal, submit, radio bool
	}{
		{"checkbox refusal", `<label><input required type="checkbox" name="consent" value="refused">I don&apos;t agree</label>`, true, false, false},
		{"checkbox affirmative lead", `<label><input required type="checkbox" name="consent" value="accepted">Yes, I will not accept outside assistance</label>`, true, true, false},
		{"checkbox setting off", `<label><input required type="checkbox" name="consent" value="accepted">I agree</label>`, false, false, false},
		{"radio refusal", `<label><input required type="radio" name="consent" value="refused">I don&apos;t agree</label>`, true, false, true},
		{"radio affirmative pair", `<label><input required type="radio" name="consent" value="refused">I don&apos;t agree</label><label><input required type="radio" name="consent" value="accepted">I agree</label>`, true, true, true},
		{"radio affirmative lead", `<label><input required type="radio" name="consent" value="accepted">Yes, I will not accept outside assistance</label>`, true, true, true},
		{"radio setting off", `<label><input required type="radio" name="consent" value="accepted">I agree</label>`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t, "html", modeAccept, Hooks{})
			bank := answerbank.New(workspace.Open(filepath.Dir(filepath.Dir(s.g.db))), s.g.st)
			if tc.radio {
				// The embedded privacy entry explicitly supports checkbox only;
				// curate this fictional workspace for the supported radio shape.
				loaded, err := bank.File()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for i := range loaded.Bank.Entries {
					if loaded.Bank.Entries[i].ID == "privacy-policy-consent" {
						loaded.Bank.Entries[i].InputTypes = []string{"checkbox", "yes_no", "single_select"}
						found = true
					}
				}
				if !found {
					t.Fatal("fixture privacy entry missing")
				}
				if _, err := bank.Save(loaded.Bank, loaded.Revision); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := bank.PatchSettings(context.Background(), answerbank.SettingsPatch{AutoAcceptLegalDeclarations: &tc.legal}); err != nil {
				t.Fatal(err)
			}
			s.g.runner.d.Page.Settings = func() Settings { return Settings{AutoAcceptLegalDeclarations: tc.legal} }
			s.b.html = `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` + fieldNames +
				`<fieldset><legend>I acknowledge the recruitment privacy notice *</legend>` + tc.options + `</fieldset><button type="button" onclick="if(document.querySelector('input[name=consent]:checked')) sendApplication()">Submit application</button></form>` + submitScript(s.b, successHTML)
			run := s.run(submitInput(s.url))
			defer dump(t, run)
			if !tc.submit {
				mustStop(t, run, StageCheck, ClassAnswerGap)
				if s.b.presses() != 0 || s.b.recorded() != 0 || run.Presses != 0 {
					t.Fatalf("refusal/disabled setting reached submit: server=%d recorded=%d run=%d", s.b.presses(), s.b.recorded(), run.Presses)
				}
				if tc.radio {
					seen := false
					s.g.matches.mu.Lock()
					defer s.g.matches.mu.Unlock()
					for _, bySelector := range s.g.matches.byID {
						for _, got := range bySelector {
							if got.R.EntryID != "privacy-policy-consent" {
								continue
							}
							trace := strings.Join(got.R.Trace, "\n")
							if strings.Contains(trace, "entry input_types reject") {
								t.Fatalf("fixture never reached acknowledgement: %s", trace)
							}
							if tc.legal && strings.Contains(trace, "negation before") || !tc.legal && string(got.R.Detail) == "legal_declaration_off" {
								seen = true
							}
						}
					}
					if !seen {
						t.Fatal("fixture did not exercise the expected acknowledgement refusal")
					}
				}
				return
			}
			if run.Outcome.Kind != OutcomeSubmitted || !run.Outcome.Confirmed || s.b.presses() != 1 || s.b.recorded() != 1 || run.Presses != 1 {
				t.Fatalf("outcome %+v, server=%d recorded=%d run=%d", run.Outcome, s.b.presses(), s.b.recorded(), run.Presses)
			}
			var body map[string]string
			raw, _ := s.b.lastBody.Load().(string)
			if err := json.Unmarshal([]byte(raw), &body); err != nil || body["consent"] != "accepted" {
				t.Fatalf("posted choice %q, error %v", body["consent"], err)
			}
		})
	}
}

// A careers site has no board to say where its posting is, so the run takes
// the place the first page's JobPosting data states. The form on the page after
// it, which states nothing, asks about "this country": the bank answers when
// that place is in the profile's country, and a place elsewhere keeps the
// question the terminal posting_country abstain (flow round 4). A posting
// placed nowhere is presumed in the United States: the bank answers and the
// fill stage says so (owner decision, flow round 8).
func TestE2E_Apply_TheFirstPagesJobPostingPlaceAnswersAThisCountryQuestion(t *testing.T) {
	const (
		question = "Are you legally authorized to work in this country?"
		presumed = "answered as if the posting is in the United States: its location could not be read"
	)
	place := func(locality, region, country string) string {
		return `,"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"` + locality + `","addressRegion":"` + region + `","addressCountry":"` + country + `"}}`
	}
	for _, c := range []struct {
		name, place        string
		answered, presumed bool
	}{
		{"placed in the profile's country", place("Durham", "North Carolina", "United States"), true, false},
		{"placed in another country", place("Toronto", "Ontario", "Canada"), false, false},
		{"placed nowhere", "", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newScenario(t, "custom", modeAccept, Hooks{})
			s.b.render = func(b *board, r *http.Request) string {
				if strings.Contains(r.URL.Path, "/apply/") {
					return `<title>Platform Engineer - Northwind Example</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` + fieldNames + fieldResume +
						`<fieldset><legend>` + question + ` *</legend><label><input type="radio" name="auth" value="Yes"> Yes</label><label><input type="radio" name="auth" value="No"> No</label></fieldset>` +
						`<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
				}
				return `<title>Platform Engineer - Northwind Example</title><script type="application/ld+json">{"@context":"https://schema.org","@type":"JobPosting","title":"Platform Engineer",` +
					`"hiringOrganization":{"@type":"Organization","name":"Northwind Example"}` + c.place + `}</script><h1>Platform Engineer</h1><p>Build the platform every team ships on.</p>` +
					`<a href="/jobs/` + b.marker + `/apply/">Apply for this job</a>`
			}
			run := s.run(submitInput(s.url))
			defer dump(t, run)
			if c.answered {
				var body map[string]string
				raw, _ := s.b.lastBody.Load().(string)
				if err := json.Unmarshal([]byte(raw), &body); err != nil || run.Outcome.Kind != OutcomeSubmitted || body["auth"] != "Yes" {
					t.Fatalf("outcome %+v, posted %q (%v)", run.Outcome, body["auth"], err)
				}
				fill := ""
				for _, st := range run.Stages {
					if st.Stage == StageFill {
						fill = st.Detail
					}
				}
				if stated := strings.Contains(fill, question) && strings.Contains(fill, presumed); stated != c.presumed {
					t.Fatalf("fill stage detail %q, want the presumption stated: %t", fill, c.presumed)
				}
				return
			}
			mustStop(t, run, StageCheck, ClassAnswerGap)
			s.g.matches.mu.Lock()
			defer s.g.matches.mu.Unlock()
			held := false
			for _, bySelector := range s.g.matches.byID {
				for _, got := range bySelector {
					held = held || strings.Contains(got.Q.Text, question) && got.R.Outcome == match.OutcomeAbstained && got.R.Detail == match.DetailPostingCountry
				}
			}
			if !held || s.b.presses() != 0 {
				t.Fatalf("the question is not the posting_country abstain (%v) or Submit was pressed %d time(s)", held, s.b.presses())
			}
		})
	}
}

// computerUsePage is a form whose widgets take only a person's input, as some
// hosted forms do: each list opens on any click but takes an option only from
// a person's click and closes on one it does not take, and when a person opens
// the authorization list its Yes option carries an icon button labelled Submit
// over its middle. The languages box, the last control, drops a value a script
// sets or inserts, takes keys only once a person pressed into it, and commits
// what was typed when it is left. The page reports what it takes, and any
// press of a Submit, through /__telemetry/.
func computerUsePage(b *board) string {
	return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1>
<style>.combo{display:inline-block;min-width:260px;padding:6px;border:1px solid #888;cursor:pointer}
.pop{position:absolute;margin:0;padding:4px;list-style:none;background:#fff;border:1px solid #888;z-index:5}
.pop li{position:relative;min-width:240px;padding:8px 12px;cursor:pointer}.go{position:absolute;left:30%;top:0;width:40%;height:100%}</style>
<form id="f" onsubmit="return false">` + fieldNames + `
<p><span id="spl">Will you now or in the future require visa sponsorship? *</span>
<span class="combo" id="sp" role="combobox" tabindex="0" aria-haspopup="listbox" aria-expanded="false" aria-controls="sp-pop" aria-labelledby="spl" aria-required="true">Select...</span></p>
<p><span id="aul">Are you legally authorized to work in the United States? *</span>
<span class="combo" id="au" role="combobox" tabindex="0" aria-haspopup="listbox" aria-expanded="false" aria-controls="au-pop" aria-labelledby="aul" aria-required="true">Select...</span></p>
<label for="langs">Which programming languages do you use? *</label><input id="langs" name="langs" required>
<button type="button" onclick="tell('submit-press'); sendApplication()">Submit application</button></form>
<script>
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
const langs = document.getElementById('langs');
let pressed = false, keyed = false;
langs.addEventListener('mousedown', (e) => { pressed = pressed || e.isTrusted; });
langs.addEventListener('keypress', (e) => { keyed = e.isTrusted && pressed; });
langs.addEventListener('beforeinput', (e) => { if (!keyed) e.preventDefault(); });
langs.addEventListener('input', (e) => { if (!e.isTrusted || !keyed) langs.value = ''; keyed = false; });
langs.addEventListener('change', (e) => { if (e.isTrusted) tell('langs:' + langs.value); });
function list(trigger, name, options, covered) {
  const close = () => { const p = document.getElementById(trigger.getAttribute('aria-controls')); if (p) p.remove(); trigger.setAttribute('aria-expanded', 'false'); };
  trigger.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(); });
  trigger.addEventListener('click', (e) => {
    if (document.getElementById(trigger.getAttribute('aria-controls'))) { close(); return; }
    const pop = document.createElement('ul');
    pop.id = trigger.getAttribute('aria-controls');
    pop.className = 'pop';
    pop.setAttribute('role', 'listbox');
    const r = trigger.getBoundingClientRect();
    pop.style.left = (r.left + scrollX) + 'px';
    pop.style.top = (r.bottom + scrollY) + 'px';
    for (const o of options) {
      const li = document.createElement('li');
      li.setAttribute('role', 'option');
      li.setAttribute('aria-label', o);
      li.textContent = o;
      if (o === covered && e.isTrusted) {
        const go = document.createElement('button');
        go.type = 'button';
        go.className = 'go';
        go.setAttribute('aria-label', 'Submit');
        go.addEventListener('click', (ev) => { ev.stopPropagation(); tell('widget-submit'); });
        li.append(go);
      }
      li.addEventListener('click', (ev) => {
        if (ev.isTrusted) { trigger.textContent = o; tell(name + ':' + o); }
        close();
      });
      pop.append(li);
    }
    document.body.append(pop);
    trigger.setAttribute('aria-expanded', 'true');
  });
}
list(document.getElementById('sp'), 'sponsorship', ['Yes', 'No']);
list(document.getElementById('au'), 'authorized', ['Yes', 'No'], 'Yes');
</script>` + submitScript(b, successHTML)
}

// The fill fills and computer use writes what it could not, with the answer
// the chain already chose (owner decision F-9): a list that took no option
// from the engine is opened and its option pressed, the box that drops a
// script's value is typed into, and the press gate keeps the Submit drawn over
// an option unpressed, leaving that row to the owner with the step that
// stopped it.
func TestE2E_Apply_ComputerUseWritesWhatTheFillCouldNot(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = func(b *board, _ *http.Request) string { return computerUsePage(b) }
	m := newModelRig(t, s.g.st, script(`[{"id":1,"answer":"Go and SQL","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"))
	s.g.runner.d.Page.Model = m.router
	s.g.runner.d.Page.Own = func(context.Context) (Own, error) { return ladderOwn, nil }
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	events := s.b.eventList()
	for _, want := range []string{"langs:Go and SQL", "sponsorship:No"} {
		if !slices.Contains(events, want) {
			t.Fatalf("events %v lack %q: the rung did not write what the fill could not", events, want)
		}
	}
	for _, bad := range []string{"widget-submit", "submit-press", "authorized:Yes"} {
		if slices.Contains(events, bad) {
			t.Fatalf("events %v: %s", events, bad)
		}
	}
	if s.b.presses() != 0 || run.Pressed {
		t.Fatalf("a dry run pressed: %d writes", s.b.presses())
	}
	var wrote int
	var left []string
	for _, p := range run.Pages {
		wrote += p.Ladder.ComputerUse
		left = append(left, p.Unresolved...)
	}
	const auth = "Are you legally authorized to work in the United States?"
	if wrote != 2 || len(left) != 1 || !strings.HasPrefix(left[0], auth) || !strings.Contains(left[0], "computer use: the option was refused by the press gate (submit_in_walk)") {
		t.Fatalf("the rung wrote %d rows; left %q", wrote, left)
	}
	if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND code = 'submit_in_walk' AND run_id = ?`, run.ID); n != 1 {
		t.Fatalf("%d audited refusals of the Submit drawn over the option, want 1", n)
	}
	var fill string
	for _, l := range run.Stages {
		if l.Stage == StageFill {
			fill = l.Detail
		}
	}
	if !strings.Contains(fill, `computer use wrote "Will you now or in the future require visa sponsorship?" (entry `) || !strings.Contains(fill, `, "Which programming languages do you use?" (rung2)`) {
		t.Fatalf("the fill stage names no write of the rung with its source: %s", fill)
	}
}

// lateListPage is a form of React-Select lists that open only on a person's
// press, as a Greenhouse list that hydrates late does: the fill reads no
// options and asks without them, and the matcher names the entry and
// abstains. The sponsorship list offers Yes and No; the authorization list
// offers no option its entry can bind. A list takes an option only from a
// person's click and closes on a person's Escape. The page reports what it
// takes, the Escape and any Submit press through /__telemetry/.
func lateListPage(b *board) string {
	list := func(id, label string) string {
		return `<div><label id="` + id + `-label" for="` + id + `">` + label + `*</label><div class="select__control"><div class="select__value-container">` +
			`<div class="select__placeholder">Select...</div><input class="select__input" id="` + id + `" role="combobox" aria-expanded="false" aria-haspopup="true" aria-labelledby="` + id + `-label" aria-required="true" autocomplete="off"></div></div></div>`
	}
	return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1>
<style>.select__control{width:320px;border:1px solid #888}.select__value-container{display:grid;padding:6px}.select__value-container>*{grid-area:1/1}
.select__input{border:0;background:transparent}.select__menu{width:320px;border:1px solid #888}.select__option{padding:8px 12px}</style>
<form id="f" onsubmit="return false">` + fieldNames + list("sp", "Will you now or in the future require visa sponsorship?") + list("au", "Are you legally authorized to work in the United States?") + `
<button type="button" onclick="tell('submit-press'); sendApplication()">Submit application</button></form>
<script>
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
function list(input, name, options) {
  const control = input.closest('.select__control'), id = 'react-select-' + input.id + '-listbox';
  const close = () => { const m = document.getElementById(id); if (m) m.parentElement.remove(); input.setAttribute('aria-expanded', 'false'); input.removeAttribute('aria-controls'); };
  control.addEventListener('mousedown', (e) => {
    if (!e.isTrusted || document.getElementById(id)) return;
    const menu = document.createElement('div'), rows = document.createElement('div');
    menu.className = 'select__menu';
    rows.id = id;
    rows.setAttribute('role', 'listbox');
    for (const o of options) {
      const row = document.createElement('div');
      row.className = 'select__option';
      row.setAttribute('role', 'option');
      row.textContent = o;
      row.addEventListener('click', (ev) => {
        if (!ev.isTrusted) return;
        input.previousElementSibling.outerHTML = '<div class="select__single-value">' + o + '</div>';
        close();
        tell(name + ':' + o);
      });
      rows.append(row);
    }
    menu.append(rows);
    control.after(menu);
    input.setAttribute('aria-expanded', 'true');
    input.setAttribute('aria-controls', id);
  });
  input.addEventListener('keydown', (e) => { if (e.key === 'Escape' && e.isTrusted && document.getElementById(id)) { close(); tell(name + ':escape'); } });
}
list(document.getElementById('sp'), 'sponsorship', ['Yes', 'No']);
list(document.getElementById('au'), 'authorized', ['U.S. citizen or permanent resident', 'Visa holder']);
</script>` + submitScript(b, successHTML)
}

// Computer use opens a list the fill could not read and writes the option the
// pinned matcher picks for the entry the fill's abstain named (CO-1): the
// sponsorship list takes No. The authorization list, whose options its entry
// cannot bind, is closed with Escape and left to the owner with the options it
// shows, which the row's draft keeps.
func TestE2E_Apply_ComputerUseOpensAListTheFillCouldNotRead(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = func(b *board, _ *http.Request) string { return lateListPage(b) }
	s.g.runner.d.Page.Pin = s.g.runner.d.Page.Matcher.(*bind.Binding).Pinned
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	events := s.b.eventList()
	for _, want := range []string{"sponsorship:No", "authorized:escape"} {
		if !slices.Contains(events, want) {
			t.Fatalf("events %v lack %q", events, want)
		}
	}
	if slices.Contains(events, "submit-press") || s.b.presses() != 0 || run.Pressed {
		t.Fatalf("events %v: a dry run pressed", events)
	}
	shown := []string{"U.S. citizen or permanent resident", "Visa holder"}
	var wrote int
	var drafts [][]string
	for _, p := range run.Pages {
		wrote += p.Ladder.ComputerUse
		for _, l := range p.Leftovers {
			drafts = append(drafts, l.Draft.Options)
		}
	}
	if wrote != 1 || len(drafts) != 1 || !slices.Equal(drafts[0], shown) {
		t.Fatalf("the rung wrote %d rows; the drafts offer %q, want one offering %q", wrote, drafts, shown)
	}
	s.g.matches.mu.Lock()
	defer s.g.matches.mu.Unlock()
	abstained := 0
	for _, bySelector := range s.g.matches.byID {
		for _, m := range bySelector {
			if strings.Contains(m.Q.Text, "sponsorship") && m.R.Outcome == match.OutcomeAbstained && m.R.EntryID == "sponsorship-now-or-future" {
				abstained++
			}
		}
	}
	if abstained != 1 {
		t.Fatalf("%d abstains of the fill named the sponsorship entry, want 1: the rung did not choose the answer", abstained)
	}
	if fill := stageOf(run, StageFill).Detail; !strings.Contains(fill, `(entry sponsorship-now-or-future)`) {
		t.Fatalf("the fill stage names no entry for the rung's write: %s", fill)
	}
}

// stateSearchPage is n8n's Ashby state list: a search box that lists the
// states matching what is typed ("No results" otherwise) and the whole list on
// ArrowDown, never on a press; it holds no North Carolina, the persona's
// state, but an "Other state". A row is chosen by trusted input only.
func stateSearchPage(b *board) string {
	return `<title>Forward Deployed Engineer - Example Corp</title><h1>Forward Deployed Engineer</h1>
<style>#st{width:320px}[role=listbox]{width:320px;border:1px solid #888}[role=option]{padding:6px 12px}</style>
<form id="f" onsubmit="return false">` + fieldNames + `
<div><label id="st-label" for="st">Which U.S. state would you primarily work from if hired?*</label>
<div><input id="st" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-haspopup="listbox" aria-labelledby="st-label" aria-required="true" placeholder="Start typing..." autocomplete="off"></div></div>
<button type="button" onclick="tell('submit-press'); sendApplication()">Submit application</button></form>
<script>
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
const states = ['California', 'Colorado', 'Florida', 'Georgia', 'Illinois', 'Massachusetts', 'New Jersey', 'New York', 'Other state', 'Pennsylvania', 'Texas', 'Virginia', 'Washington'];
const input = document.getElementById('st'), id = 'st-listbox';
const close = () => { const m = document.getElementById(id); if (m) m.remove(); input.setAttribute('aria-expanded', 'false'); input.removeAttribute('aria-controls'); };
function show(filter) {
  close();
  const m = document.createElement('div');
  m.id = id;
  m.setAttribute('role', 'listbox');
  const rows = states.filter((s) => s.toLowerCase().includes(filter.toLowerCase()));
  if (!rows.length) { const n = document.createElement('div'); n.textContent = 'No results'; m.append(n); }
  for (const o of rows) {
    const row = document.createElement('div');
    row.setAttribute('role', 'option');
    row.textContent = o;
    row.addEventListener('click', (ev) => { if (!ev.isTrusted) return; input.value = o; close(); tell('state:' + o); });
    m.append(row);
  }
  input.parentElement.after(m);
  input.setAttribute('aria-expanded', 'true');
  input.setAttribute('aria-controls', id);
}
input.addEventListener('input', () => (input.value ? show(input.value) : close()));
input.addEventListener('keydown', (e) => {
  if (e.key === 'ArrowDown' && e.isTrusted && !document.getElementById(id)) show('');
  if (e.key === 'Escape') close();
});
</script>` + submitScript(b, successHTML)
}

// A search box the fill typed the recorded state into and found nothing in
// (n8n, live: "South Carolina" in an Ashby list of fourteen states) is opened
// by the computer-use rung with ArrowDown, and the matcher, given the list,
// picks its "Other state".
func TestE2E_Apply_ComputerUseListsASearchBoxWithoutTheStateAndTakesItsCatchAll(t *testing.T) {
	t.Parallel()
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = func(b *board, _ *http.Request) string { return stateSearchPage(b) }
	s.g.runner.d.Page.Pin = s.g.runner.d.Page.Matcher.(*bind.Binding).Pinned
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	events := s.b.eventList()
	if !slices.Contains(events, "state:Other state") {
		t.Fatalf("events %v: the rung did not take the list's catch-all", events)
	}
	if slices.Contains(events, "submit-press") || s.b.presses() != 0 || run.Pressed {
		t.Fatalf("events %v: a dry run pressed", events)
	}
	wrote := 0
	for _, p := range run.Pages {
		wrote += p.Ladder.ComputerUse
	}
	if wrote != 1 {
		t.Fatalf("the rung wrote %d rows, want 1", wrote)
	}
}
