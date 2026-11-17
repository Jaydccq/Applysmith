//go:build browser

package apply

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"applysmith/internal/gates"
)

// A mock iCIMS tenant: the portal's email-first login step (form id
// enterEmailForm, action with step=email, a Next that stays disabled until an
// email is typed, an EU-resident box that reveals a consent box), alone or
// inside the tenant's content frame. The puzzle variant raises an hCaptcha
// from the form's submit handler and accepts the step only when it arrives
// with a token and the behavior-type field the tenant's own callback adds.

func icimsStepHTML(b *board, puzzle bool) string {
	return fmt.Sprintf(`<title>Log in</title><form id="enterEmailForm" action="/jobs/%[1]s/engineer/login?step=email" method="post">
<label for="loginName">Email</label><input id="loginName" type="email" name="css_loginName">
<label><input type="checkbox" id="eu_resident" name="eu_resident"> If you are a European Union resident, please check this box</label>
<div id="gdprBox" style="display:none"><label><input type="checkbox" id="accept_gdpr" name="accept_gdpr"> I accept</label></div>
<button id="enterEmailSubmitButton" type="submit" disabled>Next</button></form>
<script>
const f = document.getElementById('enterEmailForm'), btn = document.getElementById('enterEmailSubmitButton');
f.css_loginName.addEventListener('input', () => { btn.disabled = !f.css_loginName.value.includes('@'); });
document.getElementById('eu_resident').addEventListener('change', (e) => { document.getElementById('gdprBox').style.display = e.target.checked ? '' : 'none'; });
f.addEventListener('submit', (ev) => {
  if (!%[2]t) return;
  const t = f.querySelector('[name=h-captcha-response]');
  if (t && t.value) return;
  ev.preventDefault();
  if (t) return;
  const w = document.createElement('div'); w.className = 'h-captcha'; w.setAttribute('data-sitekey', 'site-key-ic');
  w.style.cssText = 'width:304px;height:78px;border:1px solid #888';
  const r = document.createElement('textarea'); r.name = 'h-captcha-response'; r.style.display = 'none';
  f.appendChild(w); f.appendChild(r);
});
</script>`, b.marker, puzzle)
}

func icimsApplicationHTML(b *board) string {
	return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` +
		fieldNames + fieldQuestions + `<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
}

// icims answers the tenant's pages; it reports whether it did.
func (s *site) icims(b *board, w http.ResponseWriter, r *http.Request) bool {
	puzzle := b.kind == "icims"
	html := func(body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" + body))
	}
	switch {
	case strings.Contains(r.URL.Path, "/apply-form"):
		html(icimsApplicationHTML(b))
	case r.Method == http.MethodPost && r.URL.Query().Get("step") == "email":
		_ = r.ParseForm()
		token, behavior := r.PostForm.Get("h-captcha-response"), r.PostForm.Get("behavior-type")
		b.mu.Lock()
		b.events = append(b.events, fmt.Sprintf("step:email=%s:eu=%t:behavior=%s:token=%t", r.PostForm.Get("css_loginName"), r.PostForm.Get("eu_resident") != "", behavior, token != ""))
		b.mu.Unlock()
		switch {
		case puzzle && (token == "" || behavior != "other"):
			html(icimsStepHTML(b, puzzle))
		case b.kind == "icims-framed":
			html(fmt.Sprintf(`<script>top.location.href = '/jobs/%s/apply-form';</script>`, b.marker))
		default:
			html(icimsApplicationHTML(b))
		}
	case b.kind == "icims-framed" && r.URL.Query().Get("in_iframe") == "":
		html(fmt.Sprintf(`<title>Careers</title><h1>Careers</h1><iframe id="icims_content_iframe" src="/jobs/%s/engineer/login?in_iframe=1" style="width:900px;height:700px;border:0"></iframe>`, b.marker))
	default:
		html(icimsStepHTML(b, puzzle))
	}
	return true
}

func (b *board) stepPosts() (posts []string) {
	for _, e := range b.eventList() {
		if strings.HasPrefix(e, "step:") {
			posts = append(posts, e)
		}
	}
	return
}

func TestGates_Captcha_ICIMSStepSentLikeTenantCallback(t *testing.T) {
	s := newScenario(t, "icims", modeAccept, Hooks{})
	svc := newTwoCaptcha(t)
	s.g.runner.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeSubmitted || s.b.recorded() != 1 {
		t.Fatalf("outcome %+v, recorded %d", run.Outcome, s.b.recorded())
	}
	// The tenant accepts the step only with a token and the behavior-type field
	// its own callback adds; the page's own Next would have raised the puzzle
	// again, so exactly one request carried both.
	posts := s.b.stepPosts()
	if len(posts) != 1 || posts[0] != "step:email=alex@example.com:eu=false:behavior=other:token=true" || svc.tasks != 1 {
		t.Fatalf("step requests %v, solves %d", posts, svc.tasks)
	}
	if !strings.Contains(strings.Join(run.Captcha, " "), gates.PointICIMSEmail) {
		t.Fatalf("captcha lines %v", run.Captcha)
	}
}

func TestE2E_Apply_ICIMSFramedEmailStepSubmittedOnce(t *testing.T) {
	s := newScenario(t, "icims-framed", modeAccept, Hooks{})
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeSubmitted || s.b.recorded() != 1 {
		t.Fatalf("outcome %+v, recorded %d", run.Outcome, s.b.recorded())
	}
	if posts := s.b.stepPosts(); len(posts) != 1 || posts[0] != "step:email=alex@example.com:eu=false:behavior=:token=false" {
		t.Fatalf("step requests %v", posts)
	}
	if d := stageOf(run, StageAccount).Detail; !strings.Contains(d, "no login needed") {
		t.Fatalf("account stage: %q", d)
	}
}

// Rippling's form ends in an Apply button of type submit. The walk reaches it
// with the form filled and presses nothing; a dry run stops there, and a run
// that was asked to submit presses it once, through the submit gates.
func finalApplyBoard(t *testing.T, mode mockMode) *scenario {
	s := newScenario(t, "html", mode, Hooks{})
	page := strings.Replace(icimsApplicationHTML(s.b), `<button type="button" onclick="sendApplication()">Submit application</button>`, `<button type="submit" onclick="sendApplication()">Apply</button>`, 1)
	if !strings.Contains(page, `>Apply</button>`) {
		t.Fatal("the fixture has no final Apply button")
	}
	s.b.html = page
	return s
}

func TestE2E_Apply_FinalApplyButtonIsPressedOnceByTheSubmitStage(t *testing.T) {
	s := finalApplyBoard(t, modeAccept)
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeSubmitted || s.b.presses() != 1 || s.b.recorded() != 1 {
		t.Fatalf("outcome %+v, %d presses, %d recorded", run.Outcome, s.b.presses(), s.b.recorded())
	}
}

// A mock of ADP's career-site sign-in: its own components hold the email box
// in a shadow root, and Continue refuses an empty one with ADP's own words.
func adpAuthHTML(b *board) string {
	return fmt.Sprintf(`<title>Career Site</title><h1>Sign in</h1><p>Welcome! Let's find your dream job!</p>
<sdf-input id="userEmail"></sdf-input><div id="err"></div><sdf-button id="go">Continue</sdf-button><sdf-button>Use phone number</sdf-button>
<script>
customElements.define('sdf-input', class extends HTMLElement { constructor() { super(); this.attachShadow({mode: 'open'}).innerHTML = '<label part="label" for="input">Email Address</label><input id="input" type="text" autocomplete="email" style="width:240px;height:24px">'; } });
customElements.define('sdf-button', class extends HTMLElement { connectedCallback() { this.style.cssText = 'display:inline-block;padding:6px 12px;border:1px solid #333;cursor:pointer'; } });
const tell = (e) => fetch('/__telemetry/%[1]s', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
document.getElementById('go').addEventListener('click', async () => {
  const v = document.getElementById('userEmail').shadowRoot.querySelector('input').value;
  if (!v.includes('@')) { document.getElementById('err').textContent = 'Email Address required.'; return; }
  await tell('auth:email=' + v);
  location.href = '/jobs/%[1]s/adp-method';
});
</script>`, b.marker)
}

// A mock of ADP's verification-method step: Send passcode stays disabled until
// a way to send the code is chosen.
func adpMethodHTML(b *board) string {
	return fmt.Sprintf(`<title>Career Site</title><div role="heading" aria-level="1">Select your verification method</div>
<cx-verification-method><div role="radiogroup"><sdf-radio-button value="phone" role="radio" aria-checked="false">Text Message +1 555 555 0100</sdf-radio-button><sdf-radio-button value="email" role="radio" aria-checked="false">Email Message alex@example.com</sdf-radio-button></div>
<sdf-button id="send" aria-disabled="true" disabled>Send passcode</sdf-button></cx-verification-method>
<script>
const tell = (e) => fetch('/__telemetry/%[1]s', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
for (const r of document.querySelectorAll('sdf-radio-button')) {
  r.style.cssText = 'display:block;padding:8px;border:1px solid #333;margin:4px';
  r.addEventListener('click', () => { for (const o of document.querySelectorAll('sdf-radio-button')) o.setAttribute('aria-checked', String(o === r)); const s = document.getElementById('send'); s.removeAttribute('disabled'); s.setAttribute('aria-disabled', 'false'); });
}
const send = document.getElementById('send');
send.style.cssText = 'display:inline-block;padding:6px 12px;border:1px solid #333';
send.addEventListener('click', async () => {
  if (send.hasAttribute('disabled')) return;
  await tell('auth:method=' + document.querySelector('sdf-radio-button[aria-checked="true"]').getAttribute('value'));
  location.href = '/jobs/%[1]s/adp-apply';
});
</script>`, b.marker)
}

// adpAuth answers the mock's pages; it reports whether it did.
func (s *site) adpAuth(b *board, w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := adpAuthHTML(b)
	switch {
	case strings.Contains(r.URL.Path, "/adp-method"):
		body = adpMethodHTML(b)
	case strings.Contains(r.URL.Path, "/adp-apply"):
		body = icimsApplicationHTML(b)
	}
	_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" + body))
	return true
}

func TestE2E_Apply_ADPSignInTakesTheProfileEmailAndHasThePasscodeMailed(t *testing.T) {
	s := newScenario(t, "adp-auth", modeAccept, Hooks{})
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	var auth []string
	for _, e := range s.b.eventList() {
		if strings.HasPrefix(e, "auth:") {
			auth = append(auth, e)
		}
	}
	if len(auth) != 2 || auth[0] != "auth:email=alex@example.com" || auth[1] != "auth:method=email" {
		t.Fatalf("sign-in step requests %v (outcome %+v)", auth, run.Outcome)
	}
	if run.Outcome.Kind != OutcomeSubmitted || s.b.recorded() != 1 {
		t.Fatalf("outcome %+v, recorded %d", run.Outcome, s.b.recorded())
	}
}
