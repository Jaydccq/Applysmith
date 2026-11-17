//go:build browser

package apply

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A mock applicant-tracking system: small hand-written pages per named
// system, served over TLS from loopback under the real host names (the
// browser maps them). Each board counts what reached it, so a test can assert
// "at most one press" from the server's side.

type mockMode int32

const (
	modeAccept         mockMode = iota // record the application and answer 200
	modeRefuse                         // answer 422 and record nothing
	modeLost                           // record the application, then drop the connection
	modeSlow                           // record after a delay, then answer 200
	modeCovered                        // draw an overlay over the Submit control
	modeValidateOnce                   // the form refuses the first press with a validation message
	modeValidateAlways                 // the form refuses every press with a validation message
	modeSilent                         // the Submit control does nothing at all
)

type board struct {
	kind   string // greenhouse, lever, ashby, workday, linkedin
	html   string // the whole page of a board of kind "html"
	marker string // unique job id inside every URL of this board
	mode   atomic.Int32

	attempts atomic.Int32 // writes that reached the server, retries included
	loads    atomic.Int32

	// A board with a sign-in wall accepts exactly one password; pwTries counts
	// the passwords submitted to it, whatever they were.
	accept  string
	pwTries atomic.Int32
	// hangsOut counts /hang/ resources that were held to their end: a run
	// that waited for the page's load event outlasts one.
	hangsOut atomic.Int32
	seen     []string // what the sign-in endpoint received, for the test's diagnosis
	// redraw makes the sign-in page draw its button anew on every keystroke,
	// the way Workday re-renders it once the boxes hold text; proxy hides the
	// button behind an empty role=button overlay that presses it (Workday).
	redraw, proxy bool

	// dest and decoy are the addresses a LinkedIn posting's payload names: the
	// apply destination of this posting and of a similar-job card.
	dest, decoy string
	hits        []string // the paths that reached the board

	// render, when set, serves the board's pages (kind "custom").
	render func(b *board, r *http.Request) string

	mu       sync.Mutex
	lastBody atomic.Value    // the last application body the write endpoint received
	events   []string        // what the page reported through /__telemetry/
	press    map[string]bool // distinct presses of the Submit control that reached the server
	taken    map[string]bool // distinct presses the server recorded as applications
}

func (b *board) setMode(m mockMode) { b.mode.Store(int32(m)) }

func (b *board) hit(p string) {
	b.mu.Lock()
	b.hits = append(b.hits, p)
	b.mu.Unlock()
}

// hitsOn counts the requests whose path contains frag.
func (b *board) hitsOn(frag string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, h := range b.hits {
		if strings.Contains(h, frag) {
			n++
		}
	}
	return n
}

// presses is how many times the Submit control was pressed, as the server saw
// it: a browser that retries one dropped request is still one press.
func (b *board) presses() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.press)
}

// recorded is how many distinct applications the server recorded.
func (b *board) recorded() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.taken)
}

func (b *board) note(id string, recorded bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.press == nil {
		b.press, b.taken = map[string]bool{}, map[string]bool{}
	}
	b.press[id] = true
	if recorded {
		b.taken[id] = true
	}
}

type site struct {
	srv *httptest.Server
	mu  sync.Mutex
	bs  []*board
}

func newSite() *site {
	s := &site{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.srv.Listener = l
	s.srv.StartTLS()
	return s
}

func (s *site) port() int { return s.srv.Listener.Addr().(*net.TCPAddr).Port }

func (s *site) close() { s.srv.Close() }

func (s *site) add(b *board) *board {
	s.mu.Lock()
	s.bs = append(s.bs, b)
	s.mu.Unlock()
	return b
}

func (s *site) find(r *http.Request) *board {
	s.mu.Lock()
	defer s.mu.Unlock()
	whole := r.URL.Path + "?" + r.URL.RawQuery
	for _, b := range s.bs {
		if strings.Contains(whole, b.marker) {
			return b
		}
	}
	return nil
}

// URL builders: what the user passes as the posting URL.
func (s *site) postingURL(b *board) string {
	p := s.port()
	switch b.kind {
	case "greenhouse":
		return fmt.Sprintf("https://boards.greenhouse.io:%d/example-corp/jobs/%s", p, b.marker)
	case "greenhouse-board":
		return fmt.Sprintf("https://job-boards.greenhouse.io:%d/example-corp/jobs/%s", p, b.marker)
	case "lever-apply":
		return fmt.Sprintf("https://jobs.lever.co:%d/example-corp/%s/apply", p, b.marker)
	case "lever":
		return fmt.Sprintf("https://jobs.lever.co:%d/example-corp/%s", p, b.marker)
	case "ashby", "ashby-boxes":
		return fmt.Sprintf("https://jobs.ashbyhq.com:%d/example-corp/%s", p, b.marker)
	case "workday":
		return fmt.Sprintf("https://example.wd5.myworkdayjobs.com:%d/en-US/Careers/job/Durham-NC/Platform-Engineer_%s", p, b.marker)
	case "linkedin", "linkedin-late", "linkedin-404flow", "linkedin-sdui", "linkedin-gone", "linkedin-offsite", "linkedin-decoyonly", "linkedin-guest",
		"linkedin-closed", "linkedin-applied", "linkedin-challenge-next", "linkedin-challenge-submit", "linkedin-safety":
		return fmt.Sprintf("https://www.linkedin.com:%d/jobs/view/%s", p, b.marker)
	case "jobvite-embed":
		return fmt.Sprintf("https://careers.example.test:%d/jobs/%s", p, b.marker)
	case "oracle":
		return fmt.Sprintf("https://example.fa.us2.oraclecloud.com:%d/hcmUI/CandidateExperience/en/sites/CX/job/%s", p, b.marker)
	case "generic", "widgets", "repair", "ladder", "html", "custom":
		return fmt.Sprintf("https://careers.example.test:%d/jobs/%s", p, b.marker)
	case "icims", "icims-framed":
		return fmt.Sprintf("https://careers.example.test:%d/jobs/%s/engineer/login", p, b.marker)
	case "adp-auth":
		return fmt.Sprintf("https://careers.example.test:%d/jobs/%s/auth", p, b.marker)
	}
	return ""
}

// hostRules maps every host the mocks use to the loopback listener.
const mockHostRules = "MAP accounts.google.com 127.0.0.1, MAP boards.greenhouse.io 127.0.0.1, MAP job-boards.greenhouse.io 127.0.0.1, MAP jobs.lever.co 127.0.0.1, MAP jobs.ashbyhq.com 127.0.0.1, " +
	"MAP example.wd5.myworkdayjobs.com 127.0.0.1, MAP www.linkedin.com 127.0.0.1, MAP example.fa.us2.oraclecloud.com 127.0.0.1, MAP careers.example.test 127.0.0.1, MAP jobs.jobvite.com 127.0.0.1, MAP validate.perfdrive.com 127.0.0.1"

const fieldNames = `<label for="firstName">First Name *</label><input id="firstName" name="firstName" required>
<label for="lastName">Last Name *</label><input id="lastName" name="lastName" required>
<label for="email">Email *</label><input id="email" type="email" name="email" required>
<label for="phone">Phone</label><input id="phone" type="tel" name="phone">`

const fieldResume = `<label for="resume">Resume/CV *</label><input id="resume" type="file" name="resume">`

const fieldQuestions = `<fieldset><legend>Will you now require visa sponsorship to work for us? *</legend>
<label><input type="radio" name="sp" value="Yes"> Yes</label><label><input type="radio" name="sp" value="No"> No</label></fieldset>`

// submitScript wires the Submit control: it posts the answers to the board's
// write endpoint and, when the server accepts, replaces the page with the
// confirmation.
func submitScript(b *board, success string) string {
	return submitScriptAt(b, success, "/apply-write/")
}

func submitScriptAt(b *board, success, endpoint string) string {
	return fmt.Sprintf(`<script>
window.__presses = 0;
window.__page = Math.random().toString(36).slice(2);
async function sendApplication() {
  window.__presses++;
  const data = {_press: window.__page + ':' + window.__presses};
  for (const i of document.querySelectorAll('input,select,textarea')) {
    if (i.type === 'radio') { if (i.checked) data[i.name] = i.value; }
    else if (i.type === 'file') { data[i.name || i.id] = i.files && i.files[0] ? i.files[0].name : ''; }
    else data[i.name || i.id] = i.value;
  }
  let res;
  try { res = await fetch('%s%s', {method: 'POST', headers: {'content-type': 'application/json'}, body: JSON.stringify(data)}); }
  catch (e) { return; }
  if (res.ok) { document.body.innerHTML = %q; return; }
  const al = document.createElement('div'); al.setAttribute('role', 'alert'); al.textContent = 'The server refused the application: ' + res.status;
  document.body.prepend(al);
}
</script>`, endpoint, b.marker, success)
}

const successHTML = `<h1>Thank you for applying</h1><p>Your application has been submitted.</p>`

func (s *site) handle(w http.ResponseWriter, r *http.Request) {
	b := s.find(r)
	if b == nil {
		http.NotFound(w, r)
		return
	}
	b.hit(r.URL.Path)
	if strings.HasPrefix(r.URL.Path, "/hang/") {
		// A resource that never finishes: the page's load event never fires.
		select {
		case <-r.Context().Done():
		case <-time.After(25 * time.Second):
			b.hangsOut.Add(1)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/apply-write/") || strings.HasPrefix(r.URL.Path, "/voyager/api/jobs/applications/") {
		s.write(b, w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/__login/") {
		b.pwTries.Add(1)
		var cred struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&cred)
		b.mu.Lock()
		b.seen = append(b.seen, cred.Email+"|"+cred.Password)
		b.mu.Unlock()
		if b.accept != "" && cred.Password == b.accept {
			http.SetCookie(w, &http.Cookie{Name: "sess", Value: "ok", Path: "/"})
			return
		}
		http.Error(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}
	if b.accept != "" {
		if c, err := r.Cookie("sess"); err != nil || c.Value != "ok" {
			b.loads.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" + loginPageHTML(b)))
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/__telemetry/") {
		var ev struct {
			E string `json:"e"`
		}
		_ = json.NewDecoder(r.Body).Decode(&ev)
		b.mu.Lock()
		b.events = append(b.events, ev.E)
		b.mu.Unlock()
		return
	}
	b.loads.Add(1)
	if strings.HasPrefix(b.kind, "icims") && s.icims(b, w, r) {
		return
	}
	if b.kind == "adp-auth" && s.adpAuth(b, w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" + s.page(b, r)))
}

// write is the application write: the one request that must happen at most
// once.
func (s *site) write(b *board, w http.ResponseWriter, r *http.Request) {
	b.attempts.Add(1)
	var body struct {
		Press string `json:"_press"`
	}
	raw, _ := io.ReadAll(r.Body)
	b.lastBody.Store(string(raw))
	_ = json.Unmarshal(raw, &body)
	switch mockMode(b.mode.Load()) {
	case modeRefuse:
		b.note(body.Press, false)
		http.Error(w, `{"errors":["Missing entry for required field"]}`, http.StatusUnprocessableEntity)
	case modeLost:
		b.note(body.Press, true)
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	case modeSlow:
		time.Sleep(1500 * time.Millisecond)
		b.note(body.Press, true)
		_, _ = w.Write([]byte(acceptBody(b)))
	default:
		b.note(body.Press, true)
		_, _ = w.Write([]byte(acceptBody(b)))
	}
}

// acceptBody is the answer to an accepted application. Ashby answers 200 to a
// bounce too, so its success is the FormSubmitSuccess object, as on the real
// service.
func acceptBody(b *board) string {
	if strings.HasPrefix(b.kind, "ashby") {
		return `{"data":{"submitSingleApplicationForm":{"__typename":"FormSubmitSuccess"}}}`
	}
	return `{"ok":true}`
}

func (s *site) page(b *board, r *http.Request) string {
	if b.render != nil {
		return b.render(b, r)
	}
	cover := ""
	if mockMode(b.mode.Load()) == modeCovered {
		cover = `<div style="position:fixed;left:0;top:0;width:100%;height:100%;background:#fff;z-index:99999;opacity:.99">Cookie settings</div>`
	}
	switch b.kind {
	case "html":
		return b.html
	case "greenhouse":
		return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1>` + cover + `<form id="application_form" onsubmit="return false">` +
			fieldNames + fieldResume + fieldQuestions + `<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
	case "lever":
		return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1>` + cover + `<form id="application-form" onsubmit="return false">` +
			fieldNames + fieldQuestions + `<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, `<h1>Application submitted!</h1><p>Thanks for applying.</p>`)
	case "ashby":
		return `<title>Platform Engineer @ Example Corp</title><h1>Platform Engineer</h1>` + cover + `<div data-field-path="root">` +
			fieldNames + fieldQuestions + `<button type="button" onclick="sendApplication()">Submit Application</button></div>` + submitScript(b, `<h1>Your application was successfully submitted.</h1>`)
	case "ashby-boxes":
		return `<title>Platform Engineer @ Example Corp</title><h1>Platform Engineer</h1><div data-field-path="root">` + fieldNames + ashbyBoxGroup +
			`<button type="button" onclick="sendApplication()">Submit Application</button></div>` + submitScript(b, `<h1>Your application was successfully submitted.</h1>`)
	case "workday":
		return workdayPage(b, cover)
	case "linkedin", "linkedin-late", "linkedin-404flow", "linkedin-sdui", "linkedin-gone", "linkedin-offsite", "linkedin-decoyonly", "linkedin-guest",
		"linkedin-closed", "linkedin-applied", "linkedin-challenge-next", "linkedin-challenge-submit", "linkedin-safety":
		return linkedinPage(b, cover, r)
	case "jobvite-embed":
		return s.jobvitePage(b, r)
	case "oracle":
		return oraclePage(b)
	case "widgets":
		return widgetsPage(b)
	case "repair":
		return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` +
			fieldNames + `<label for="city">City *</label><input id="city" name="city" required>` + fieldQuestions +
			`<button type="button" onclick="press()">Submit application</button></form>` + submitScript(b, successHTML) + repairScript(b)
	case "ladder":
		return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` +
			fieldNames + `<label for="langs">Which programming languages do you use? *</label><input id="langs" name="langs" required>` + fieldQuestions +
			`<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
	case "generic":
		return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1>` + cover + `<form id="f" onsubmit="return false">` +
			fieldNames + fieldQuestions + `<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
	}
	return ""
}

// ashbyBoxGroup is a required checkbox question the way Ashby renders it: every
// box is named after its own option and the title's label points at an id
// nothing carries.
const ashbyBoxGroup = `<fieldset class="ashby-application-form-input-checkbox-group">
<label for="absent" class="ashby-application-form-question-title _required_a1b2c">Which of these have you personally shipped?</label>
<div><input type="checkbox" id="ab1" name="Desktop app"><label for="ab1">Desktop app</label></div>
<div><input type="checkbox" id="ab2" name="Kernel module"><label for="ab2">Kernel module</label></div>
<div><input type="checkbox" id="ab3" name="Game engine"><label for="ab3">Game engine</label></div></fieldset>`

func (b *board) eventList() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.events...)
}

// oraclePage is an Oracle Recruiting Cloud candidate page in its live shape:
// the Work and Education History block (apply-flow-block) with its timeline
// add buttons, each opening an inline editor (no dialog) that hides the add
// buttons while it is open. The editor's ids carry a volatile suffix; its
// month and year pickers, Major and School are cx-select grid comboboxes that
// commit a row on a hover followed by a click on its grid cell; School is a
// server search that answers a second after the last key with its ten closest
// names; a commit resets the Degree pills; the footer button (save-btn) reads
// Add Experience or Add Education. The page reports what happens in the
// editors through /__telemetry/ so the test can read the order.
func oraclePage(b *board) string {
	return `<title>Platform Engineer</title><h1>Platform Engineer</h1>` + fieldNames + `
<div class="apply-flow-block"><h3 class="apply-flow-block__title">Work and Education History</h3>
<div id="cards"><div class="timeline-card">Engineer \u2014 Sample Works</div></div>
<button id="timeline-add-experience-button" class="timeline-work-add-button" type="button" onclick="openEd('exp')">Add Experience</button>
<button id="timeline-add-education-button" class="timeline-education-add-button" type="button" onclick="openEd('edu')">Add Education</button>
<div id="ed"></div></div>
<button type="button" onclick="sendApplication()">Submit application</button>
<script>
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
const MONTHS = ['January','February','March','April','May','June','July','August','September','October','November','December'];
const YEARS = Array.from({length: 67}, (_, i) => String(2026 - i));
const SCHOOLS = Array.from({length: 12}, (_, i) => 'Example College ' + i).concat(['Example University of Arts', 'Example University']);
const MAJORS = ['Computer Engineering', 'Computer Science', 'Mathematics'];
let kind = '';
const n = () => Math.floor(Math.random() * 90 + 10);
function cx(id, label, list, search) {
  return '<div class="cx-select-container"><label for="' + id + '">' + label + '</label><input class="cx-select-input" id="' + id + '" role="combobox" aria-haspopup="grid" aria-expanded="false" aria-controls="' + id + '-grid" autocomplete="off" data-list="' + list + '"' + (search ? ' data-search="1" placeholder="Start typing to see suggestions"' : '') + '><div id="' + id + '-grid" role="grid" class="cx-select-dropdown" hidden></div></div>';
}
function pills(names) {
  return '<ul class="cx-select-pills-container">' + names.map((p) => '<li><button type="button" class="cx-select-pill-section" aria-pressed="false" onclick="pick(this)">' + p + '</button></li>').join('') + '</ul>';
}
function dates(k) {
  return cx('month-startDate-' + k, 'Start Date Month', 'MONTHS') + cx('year-startDate-' + k, 'Start Date Year', 'YEARS') +
    '<div id="end">' + cx('month-endDate-' + k, 'End Date Month', 'MONTHS') + cx('year-endDate-' + k, 'End Date Year', 'YEARS') + '</div>';
}
function openEd(k) {
  kind = k;
  const s = n();
  let html = '';
  if (k === 'exp') {
    html = '<label for="employerName-' + s + '">Employer Name</label><input id="employerName-' + s + '" name="employerName">' +
      '<label for="jobTitle-' + s + '">Job Title</label><input id="jobTitle-' + s + '" name="jobTitle">' + dates(s) +
      '<input type="checkbox" id="af-checkbox-currentJobFlag-' + s + '" onchange="document.getElementById(\'end\').hidden = this.checked"><label for="af-checkbox-currentJobFlag-' + s + '">Current Job</label>' +
      '<label for="responsibilities-' + s + '">Responsibilities</label><textarea id="responsibilities-' + s + '" name="responsibilities"></textarea>' +
      pills(['Internal to Example Co', 'External (Outside Example Co)']);
  } else {
    html = '<span>Degree</span>' + pills(["Associate Degree", "Bachelor's Degree", "Doctoral Degree", "Master's Degree", 'Unknown']) +
      cx('major-' + s, 'Major', 'MAJORS') + cx('educationalEstablishment-' + s, 'School', 'SCHOOLS', true) + dates(s) +
      '<label for="areaOfStudy-' + s + '">Area of Study</label><input id="areaOfStudy-' + s + '" name="areaOfStudy">';
  }
  const ed = document.getElementById('ed');
  ed.innerHTML = html + '<div class="footer"><button type="button" class="oj-button save-btn" onclick="saveEd()">' + (k === 'exp' ? 'Add Experience' : 'Add Education') + '</button><button type="button" class="oj-button cancel-btn" onclick="closeEd()">Cancel</button></div>';
  for (const i of ed.querySelectorAll('.cx-select-input')) wire(i);
  for (const id of ['timeline-add-experience-button', 'timeline-add-education-button']) document.getElementById(id).hidden = true;
  tell('open:' + k);
}
function closeEd() {
  document.getElementById('ed').innerHTML = '';
  for (const id of ['timeline-add-experience-button', 'timeline-add-education-button']) document.getElementById(id).hidden = false;
}
function pick(b) { for (const x of b.closest('ul').querySelectorAll('button')) x.setAttribute('aria-pressed', String(x === b)); tell('pill:' + b.textContent); }
function wire(input) {
  const grid = document.getElementById(input.id + '-grid');
  const all = {MONTHS, YEARS, SCHOOLS, MAJORS}[input.dataset.list];
  let timer;
  const show = (rows) => {
    grid.hidden = false; grid.classList.add('cx-select-dropdown--open'); input.setAttribute('aria-expanded', 'true');
    grid.innerHTML = rows.length ? rows.map((t) => '<div role="row"><div role="gridcell"><span class="cx-select__list-item--content">' + t + '</span></div></div>').join('') : '<div role="row"><div role="gridcell"><span class="cx-select__list-item--content">No results found</span></div></div>';
    for (const cell of grid.querySelectorAll('[role=gridcell]')) {
      let hovered = false;
      cell.addEventListener('mouseover', () => (hovered = true));
      cell.addEventListener('click', () => {
        if (!hovered || /^no results/i.test(cell.textContent)) return;
        input.value = cell.textContent; input.dataset.committed = cell.textContent;
        grid.hidden = true; grid.classList.remove('cx-select-dropdown--open'); input.setAttribute('aria-expanded', 'false');
        // A commit re-renders the editor, which drops the Degree choice.
        for (const x of document.querySelectorAll('#ed .cx-select-pill-section')) if (/Degree|Unknown/.test(x.textContent)) x.setAttribute('aria-pressed', 'false');
        if (input.dataset.search) tell('school:' + cell.textContent);
      });
    }
  };
  const filtered = () => {
    const q = input.value.trim().toLowerCase();
    const rows = q ? all.filter((x) => x.toLowerCase().includes(q)) : (input.dataset.search ? [] : all);
    return input.dataset.search ? rows.slice(0, 10) : rows;
  };
  input.addEventListener('mousedown', () => show(input.dataset.search ? [] : all));
  input.addEventListener('input', () => {
    if (!input.dataset.search) { show(filtered()); return; }
    clearTimeout(timer);
    timer = setTimeout(() => show(filtered()), 1000);
  });
  input.addEventListener('keydown', (e) => { if (e.key === 'Escape') { grid.hidden = true; grid.classList.remove('cx-select-dropdown--open'); } });
}
const ed = () => document.getElementById('ed');
const field = (root) => ed().querySelector('[name=' + root + '],[id^=' + root + '-]');
const held = (root) => (field(root) && field(root).dataset.committed) || '';
function saveEd() {
  if (kind === 'exp') {
    const current = ed().querySelector('[id^=af-checkbox-currentJobFlag]').checked;
    const end = current ? 'current' : held('month-endDate') + ' ' + held('year-endDate');
    if (!field('employerName').value || !held('month-startDate') || !held('year-startDate') || end === ' ') return;
    const card = document.createElement('div'); card.className = 'timeline-card'; card.textContent = field('jobTitle').value;
    document.getElementById('cards').appendChild(card);
    tell('save:exp:' + field('employerName').value + ':' + field('jobTitle').value + ':' + held('month-startDate') + ' ' + held('year-startDate') + ':' + end + ':' + field('responsibilities').value);
  } else {
    const degree = ed().querySelector('.cx-select-pill-section[aria-pressed=true]');
    if (!held('educationalEstablishment') || !degree) return;
    const card = document.createElement('div'); card.className = 'timeline-card'; card.textContent = held('educationalEstablishment');
    document.getElementById('cards').appendChild(card);
    tell('save:edu:' + held('educationalEstablishment') + ':' + held('major') + ':' + field('areaOfStudy').value + ':' + held('month-startDate') + ' ' + held('year-startDate') + '-' + held('month-endDate') + ' ' + held('year-endDate'));
  }
  closeEd();
}
</script>` + submitScript(b, successHTML)
}

func workdayPage(b *board, cover string) string {
	return `<title>Platform Engineer - Workday</title><h1>Platform Engineer</h1>` + cover + `
<div id="s1">` + fieldNames + `</div>
<div id="s2" hidden>` + fieldQuestions + `</div>
<div id="s3" hidden><p>Review your information and submit.</p></div>
<div id="marker">Step <span id="cur">1</span> of 3</div>
<button id="nav" type="button" data-automation-id="bottom-navigation-next-button" onclick="advance()">Next</button>
<script>
let cur = 1;
function advance() {
  if (cur === 3) { sendApplication(); return; }
  document.getElementById('s' + cur).hidden = true;
  cur++;
  document.getElementById('s' + cur).hidden = false;
  document.getElementById('cur').textContent = cur;
  document.getElementById('nav').textContent = cur === 3 ? 'Submit' : 'Next';
}
</script>` + submitScript(b, `<h1>Application Submitted</h1><p>Thank you for applying.</p>`)
}

// loginPageHTML is a sign-in wall in front of the application.
func loginPageHTML(b *board) string {
	button := `<button type="button" onclick="signin()">Sign in</button>`
	if b.proxy {
		button = `<div style="position:relative;width:300px;height:40px"><div aria-label="Submit" role="button" tabindex="0" style="position:absolute;inset:0;z-index:1" onclick="signin()"></div><button type="button" tabindex="-2" aria-hidden="true" style="width:300px;height:40px">Sign in</button></div>`
	}
	return `<title>Sign in</title><h1>Sign in</h1>
<label for="em">Email</label><input id="em" name="email" type="email">
<label for="pw">Password</label><input id="pw" name="password" type="password">
` + button + `
<script>
if (` + fmt.Sprint(b.redraw) + `) document.addEventListener('input', () => { const old = document.querySelector('button'); const fresh = old.cloneNode(true); fresh.onclick = signin; old.replaceWith(fresh); });
async function signin() {
  const res = await fetch('/__login/` + b.marker + `', {method: 'POST', body: JSON.stringify({email: document.getElementById('em').value, password: document.getElementById('pw').value})});
  if (res.ok) { location.reload(); return; }
  const al = document.createElement('div'); al.setAttribute('role', 'alert'); al.textContent = 'Invalid email or password (' + Date.now() + ')';
  document.body.appendChild(al);
}
</script>`
}

// widgetsPage carries the three widget families the token delivery serves:
// a reCAPTCHA with a data-callback and no response field of its own, an hCaptcha
// with both response fields, and a Turnstile with its own field.
func widgetsPage(b *board) string {
	return `<title>Check</title><h1>Platform Engineer</h1>
<form id="a"><label for="email">Email</label><input id="email" name="email">
<div class="g-recaptcha" data-sitekey="site-key-9" data-callback="onTok" style="width:304px;height:78px;border:1px solid #888"></div></form>
<form id="b"><div class="h-captcha" data-sitekey="site-key-8" style="width:304px;height:78px;border:1px solid #888"></div>
<textarea name="h-captcha-response" style="display:none"></textarea><textarea name="g-recaptcha-response" style="display:none"></textarea></form>
<form id="c"><div class="cf-turnstile" data-sitekey="site-key-7" style="width:300px;height:65px;border:1px solid #888"></div>
<input type="hidden" name="cf-turnstile-response"></form>
<script>window.__cb = []; function onTok(t) { window.__cb.push(t); }</script>`
}

// repairScript makes the Submit control refuse the way a client-validated form
// does: it empties the City box and names it in an alert, once or every time,
// and reports each press through /__telemetry/.
func repairScript(b *board) string {
	return fmt.Sprintf(`<script>
const mode = %d; let tries = 0;
const tellp = (e) => fetch('/__telemetry/%s', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
async function press() {
  tries++; await tellp('press:' + tries);
  const old = document.getElementById('err'); if (old) old.remove();
  if ((mode === %d && tries === 1) || mode === %d) {
    document.getElementById('city').value = '';
    const al = document.createElement('div'); al.id = 'err'; al.setAttribute('role', 'alert'); al.textContent = 'City: Missing entry for required field';
    document.body.prepend(al); return;
  }
  return sendApplication();
}
</script>`, b.mode.Load(), b.marker, modeValidateOnce, modeValidateAlways)
}
