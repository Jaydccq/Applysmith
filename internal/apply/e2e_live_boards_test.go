//go:build browser

package apply

import (
	"net/http"
	"strings"
	"testing"

	"applysmith/internal/gates"
)

// Synthetic copies of the markup the live dry run with the demo identity met
// on two hosted boards. Names and places are fictional; only the shape is
// kept: Lever's cookie notice marked up as a dialog, its type="button" Submit
// with a hidden captcha submit beside it, its wrapping labels with status
// lines and its location search; Greenhouse's phone fieldset whose dial-code
// picker is labelled only "Country" and its asynchronous city search.

func leverApplyPage(b *board) string {
	return `<title>Platform Engineer - Example Corp</title>
<style>.template-btn-submit{text-transform:uppercase}.hidden{display:none}.status{display:none}
.cc-window{position:fixed;left:0;right:0;bottom:0;height:60px;background:#333;color:#fff;z-index:10}</style>
<div role="dialog" aria-live="polite" aria-label="cookieconsent" class="cc-window cc-banner"><h4>Privacy Notice</h4>
<p>This website uses cookies to improve your web experience.</p><button class="cc-btn cc-deny">Deny</button><button class="cc-btn cc-allow">Accept</button></div>
<h2>Platform Engineer</h2>
<form id="application-form" onsubmit="return false"><h4>Submit your application</h4><ul>
<li class="application-question"><label><div class="application-label">Full name<span class="required">&#x2731;</span></div><div class="application-field"><input type="text" name="name" required></div></label></li>
<li class="application-question"><label><div class="application-label">Email<span class="required">&#x2731;</span></div><div class="application-field"><input type="email" name="email" required></div></label></li>
<li class="application-question"><label><div class="application-label">Current location <span class="required">&#x2731;</span></div>
 <div class="application-field"><input class="location-input" id="location-input" type="text" maxlength="100" name="location" required>
 <input id="selected-location" type="hidden" name="selectedLocation">
 <div class="dropdown-container"><div class="dropdown-results"></div>
 <div class="dropdown-no-results status">No location found. Try entering a different location</div>
 <div class="dropdown-loading-results status"><span>Loading</span></div></div></div></label></li>
</ul>` + fieldQuestions + `
<input id="hcaptchaResponseInput" type="hidden" name="h-captcha-response" value="">
<button id="hcaptchaSubmitBtn" type="submit" class="hidden" onclick="tell('hidden-press')"></button>
<div class="section last-section-apply"><button id="btn-submit" type="button" class="postings-btn template-btn-submit" data-qa="btn-submit" href="#" onclick="sendApplication()">Submit application</button></div>
</form>
<script>
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
const places = ['Durham, England, GBR', 'Durham, NC, USA', 'Durham, NH, USA'];
const box = document.getElementById('location-input');
const results = document.querySelector('.dropdown-results');
box.addEventListener('keydown', () => setTimeout(() => {
  const q = box.value.toLowerCase();
  results.innerHTML = '';
  if (!q) return;
  for (const place of places.filter((p) => p.toLowerCase().startsWith(q))) {
    const row = document.createElement('div');
    row.className = 'dropdown-location';
    row.textContent = place;
    row.addEventListener('click', () => {
      box.value = place;
      document.getElementById('selected-location').value = JSON.stringify({name: place});
      results.innerHTML = '';
      tell('location:' + place);
    });
    results.append(row);
  }
}, 40));
</script>` + submitScript(b, `<h1>Application submitted!</h1>`)
}

func greenhouseBoardPage(b *board) string {
	return `<title>Platform Engineer at Example Corp</title><h1>Platform Engineer</h1>
<form id="application-form" onsubmit="return false">
<div><label id="first_name-label" for="first_name">First Name<span aria-hidden="true">*</span></label><input id="first_name" aria-required="true" autocomplete="given-name"></div>
<div><label id="last_name-label" for="last_name">Last Name<span aria-hidden="true">*</span></label><input id="last_name" aria-required="true" autocomplete="family-name"></div>
<div><label id="email-label" for="email">Email<span aria-hidden="true">*</span></label><input id="email" aria-required="true" autocomplete="email"></div>
<fieldset class="phone-input"><legend class="visually-hidden">Phone</legend>
 <div class="phone-input__country"><div class="select"><div class="select__container">
 <label id="country-label" for="country" class="select__label">Country<span aria-hidden="true">*</span></label>
 <div class="select-shell"><div class="select__control" id="country-control"><div class="select__value-container"><div class="select__placeholder"></div>
 <div class="select__input-container"><input class="select__input" id="country" type="text" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-labelledby="country-label" aria-required="true" value=""></div></div></div></div></div></div></div>
 <div class="phone-input__phone"><label id="phone-label" for="phone">Phone<span aria-hidden="true">*</span></label><input id="phone" type="tel" aria-label="Phone" aria-required="true"></div></fieldset>
<div class="field-wrapper"><div class="select"><div class="select__container">
 <label id="candidate-location-label" for="candidate-location" class="select__label">Location (City)<span aria-hidden="true">*</span></label>
 <div class="select-shell"><span aria-live="polite" role="log"></span><div class="select__control" id="location-control"><div class="select__value-container"><div class="select__placeholder"></div>
 <div class="select__input-container"><input class="select__input" id="candidate-location" type="text" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-labelledby="candidate-location-label" aria-required="true" value=""></div></div></div></div></div></div></div>
` + fieldQuestions + `
<button type="button" onclick="sendApplication()">Submit application</button>
</form>
<script>
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
function reactSelect(inputId, controlId, opts) {
  const input = document.getElementById(inputId);
  const control = document.getElementById(controlId);
  const values = control.querySelector('.select__value-container');
  let menu = null, timer;
  const close = () => { if (menu) menu.remove(); menu = null; input.setAttribute('aria-expanded', 'false'); input.removeAttribute('aria-controls'); };
  const commit = (row) => {
    input.value = '';
    const old = values.querySelector('.select__single-value'); if (old) old.remove();
    const single = document.createElement('div'); single.className = 'select__single-value';
    opts.show(single, row);
    values.prepend(single);
    close();
    tell(inputId + ':' + row.text);
  };
  const list = (rows) => {
    const l = menu.querySelector('[role=listbox]'); l.innerHTML = '';
    rows.forEach((row, i) => {
      const o = document.createElement('div'); o.className = 'select__option'; o.setAttribute('role', 'option');
      o.id = 'react-select-' + inputId + '-option-' + i;
      o.innerHTML = row.flag ? '<div class="iti__flag iti__' + row.flag + '"></div>' : '';
      o.append(row.text);
      o.addEventListener('click', () => commit(row));
      l.append(o);
    });
  };
  const open = () => {
    if (menu) return;
    menu = document.createElement('div'); menu.className = 'select__menu';
    menu.innerHTML = '<div class="select__menu-list" role="listbox" id="react-select-' + inputId + '-listbox"></div>';
    control.after(menu);
    input.setAttribute('aria-expanded', 'true');
    input.setAttribute('aria-controls', 'react-select-' + inputId + '-listbox');
    if (!opts.search) list(opts.rows);
  };
  control.addEventListener('mousedown', open);
  input.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(); if (e.key === 'ArrowDown') open(); });
  input.addEventListener('input', () => {
    open();
    if (!opts.search) { list(opts.rows.filter((r) => r.text.toLowerCase().includes(input.value.toLowerCase()))); return; }
    const l = menu.querySelector('[role=listbox]');
    l.innerHTML = input.value ? '<div class="select__menu-notice select__menu-notice--loading">Loading...</div>' : '';
    clearTimeout(timer);
    const q = input.value.toLowerCase();
    if (q) timer = setTimeout(() => { if (menu) list(opts.search(q)); }, 150);
  });
}
reactSelect('country', 'country-control', {
  rows: [['ca', 'Canada +1'], ['um', 'United States Minor Outlying Islands +1'], ['gb', 'United Kingdom +44'], ['us', 'United States +1']].map(([flag, text]) => ({flag, text})),
  show: (el, row) => { el.innerHTML = '<div class="iti__flag iti__' + row.flag + '"></div><span></span>'; el.querySelector('span').append('+', /\+(\d+)/.exec(row.text)[1]); },
});
const places = ['Durham, North Carolina, United States', 'Durham, England, United Kingdom', 'Durham, New Hampshire, United States'];
reactSelect('candidate-location', 'location-control', {
  search: (q) => places.filter((p) => p.toLowerCase().includes(q)).map((text) => ({text})),
  show: (el, row) => { el.textContent = row.text; },
});
</script>` + submitScript(b, successHTML)
}

func liveBoard(t *testing.T, kind string, page func(*board) string) *scenario {
	t.Helper()
	s := newScenario(t, kind, modeAccept, Hooks{})
	s.b.render = func(b *board, _ *http.Request) string { return page(b) }
	// The demo identity's bank answers, as the live dry run used them.
	usePersonaAnswers(t, s.g)
	return s
}

func eventsWith(b *board, prefix string) []string {
	var out []string
	for _, e := range b.eventList() {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// Lever: a cookie notice marked up as a dialog no longer hides the form's own
// type="button" Submit, so a dry run ends ready for Submit without pressing it;
// the location search is answered from the clean label "Current location".
func TestE2E_LiveBoards_LeverDryRunStopsAtTheButtonSubmitBesideACookieDialog(t *testing.T) {
	t.Parallel()
	s := liveBoard(t, "lever-apply", leverApplyPage)
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("outcome %+v, want a dry run that reached the Submit control", run.Outcome)
	}
	if s.b.presses() != 0 || len(eventsWith(s.b, "hidden-press")) != 0 || run.Pressed {
		t.Fatalf("a dry run pressed: %d writes, events %v", s.b.presses(), s.b.eventList())
	}
	if got := eventsWith(s.b, "location:"); len(got) != 1 || got[0] != "location:Durham, NC, USA" {
		t.Fatalf("location events %v, want the North Carolina suggestion", got)
	}
}

// The Submit the walk recognised is the visible button, never the hidden
// captcha submit: an explicit submitting run presses it exactly once.
func TestE2E_LiveBoards_LeverSubmitPressesTheVisibleButtonOnce(t *testing.T) {
	t.Parallel()
	s := liveBoard(t, "lever-apply", leverApplyPage)
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeSubmitted || s.b.presses() != 1 {
		t.Fatalf("outcome %+v, %d writes", run.Outcome, s.b.presses())
	}
	if n := len(eventsWith(s.b, "hidden-press")); n != 0 {
		t.Fatalf("the hidden captcha submit was pressed %d times", n)
	}
}

// Greenhouse: the dial-code picker labelled "Country" commits the country's
// own row, and the city search that lists nothing until typed is asked,
// typed and committed with the suggestion that names the region.
func TestE2E_LiveBoards_GreenhouseCountryAndCitySearchFill(t *testing.T) {
	t.Parallel()
	s := liveBoard(t, "greenhouse-board", greenhouseBoardPage)
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled || s.b.presses() != 0 {
		t.Fatalf("outcome %+v, %d writes", run.Outcome, s.b.presses())
	}
	if got := eventsWith(s.b, "country:"); len(got) == 0 || got[len(got)-1] != "country:United States +1" {
		t.Fatalf("country events %v", got)
	}
	if got := eventsWith(s.b, "candidate-location:"); len(got) != 1 || got[0] != "candidate-location:Durham, North Carolina, United States" {
		t.Fatalf("location events %v", got)
	}
}

// Avature (Two Sigma): the posting page sets scroll-behavior: smooth and its
// Apply link sits below the fold. The link is located after an instant scroll,
// so the press lands on it and the run reaches the form.
func TestE2E_LiveBoards_ApplyLinkBelowTheFoldOnASmoothScrollingPage(t *testing.T) {
	t.Parallel()
	s := liveBoard(t, "custom", leverApplyPage)
	s.b.render = func(b *board, r *http.Request) string {
		if r.URL.Query().Get("step") == "apply" {
			return leverApplyPage(b)
		}
		return `<title>Software Engineer - Example Corp</title><style>html{scroll-behavior:smooth}</style>
<h1>Software Engineer</h1><div style="height:2600px">You will build services for our platform.</div>
<a class="button button--primary" href="?step=apply">Apply</a><div style="height:1400px"></div>`
	}
	run := s.run(Input{URL: s.url, Caller: gates.CallerCLI})
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled || s.b.presses() != 0 {
		t.Fatalf("outcome %+v, %d writes: the Apply link below the fold was not followed", run.Outcome, s.b.presses())
	}
}
