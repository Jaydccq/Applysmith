//go:build browser

package apply

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/captcha"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

func widgetsTab(t *testing.T) (*e2e, Tab, *board) {
	t.Helper()
	inst, st := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	b := st.add(&board{kind: "widgets", marker: fmt.Sprintf("j%d%07d", 4, boardSeq.Add(1))})
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tab, err := LaneWindow{Lane: lane}.OpenTab(ctx, "widgets", engine.Host{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tab.Close(context.Background()) })
	if _, err := tab.Navigate(ctx, st.postingURL(b)); err != nil {
		t.Fatal(err)
	}
	return g, tab, b
}

func evalString(t *testing.T, tab Tab, expr string) string {
	t.Helper()
	var s string
	if err := tab.Eval(context.Background(), expr, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGates_Captcha_TokenDeliveryFieldsAndCallbacks(t *testing.T) {
	_, tab, _ := widgetsTab(t)
	ctx := context.Background()
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]CaptchaW{}
	for _, w := range st.Captcha {
		kinds[w.Kind] = w
	}
	if kinds["recaptcha_v2"].SiteKey != "site-key-9" || kinds["hcaptcha"].SiteKey != "site-key-8" || kinds["turnstile_widget"].SiteKey != "site-key-7" {
		t.Fatalf("widgets read as %+v", kinds)
	}
	// reCAPTCHA: the page has no response field, so one is created in the
	// widget's form, and the data-callback global is called with the token.
	d, err := deliverToken(ctx, tab, "recaptcha_v2", "token-r")
	if err != nil || !d.Created || d.Fields != 1 || d.Callback != "onTok" {
		t.Fatalf("delivery %+v err %v", d, err)
	}
	if got := evalString(t, tab, `document.querySelector('#a [name=g-recaptcha-response]').value + '|' + window.__cb.join(',')`); got != "token-r|token-r" {
		t.Fatalf("form field and callback: %q", got)
	}
	// hCaptcha: every response field of that kind, the g-recaptcha-response alias included.
	if d, err := deliverToken(ctx, tab, "hcaptcha", "token-h"); err != nil || d.Fields < 2 || d.Created {
		t.Fatalf("delivery %+v err %v", d, err)
	}
	if got := evalString(t, tab, `document.querySelector('#b [name=h-captcha-response]').value + '|' + document.querySelector('#b [name=g-recaptcha-response]').value`); got != "token-h|token-h" {
		t.Fatalf("fields %q", got)
	}
	// Turnstile: its own field; no callback was named, none is guessed at.
	if d, err := deliverToken(ctx, tab, "turnstile_widget", "token-t"); err != nil || d.Fields != 1 || d.Callback != "" {
		t.Fatalf("delivery %+v err %v", d, err)
	}
	if got := evalString(t, tab, `document.querySelector('#c [name=cf-turnstile-response]').value`); got != "token-t" {
		t.Fatalf("turnstile field %q", got)
	}
}

func TestGates_Captcha_RefusedTokenFieldBlanked(t *testing.T) {
	_, tab, _ := widgetsTab(t)
	ctx := context.Background()
	if _, err := deliverToken(ctx, tab, "hcaptcha", "token-h"); err != nil {
		t.Fatal(err)
	}
	blankFields(ctx, tab, "hcaptcha")
	if got := evalString(t, tab, `document.querySelector('#b [name=h-captcha-response]').value + document.querySelector('#b [name=g-recaptcha-response]').value`); got != "" {
		t.Fatalf("fields still hold %q after the page refused the token", got)
	}
	st, _ := Probe(ctx, tab)
	empty := 0
	for _, f := range st.Resp {
		if f.Empty {
			empty++
		}
	}
	if empty == 0 {
		t.Fatalf("the probe reads no empty response field: %+v", st.Resp)
	}
}

// botWall puts a challenge page in front of a one-page application. The
// posting sends the browser to the challenge page on wallHost; the page is an
// hCaptcha widget and a plain Submit under title and copy, and the Submit goes
// back to the posting with the form only when a token was put in. Every press
// of that Submit is reported as an event.
func botWall(wallHost, title, copy string) func(*board, *http.Request) string {
	return func(b *board, r *http.Request) string {
		_, port, _ := strings.Cut(r.Host, ":")
		back := fmt.Sprintf("https://careers.example.test:%s/jobs/%s?rdw=ok", port, b.marker)
		switch {
		case r.URL.Query().Get("ssa") == "1":
			return `<title>` + title + `</title><p>` + copy + `</p>
<form id="w" onsubmit="return false"><div class="h-captcha" data-sitekey="site-key-8" style="width:304px;height:78px;border:1px solid #888"></div>
<textarea name="h-captcha-response" style="display:none"></textarea>
<button type="button" onclick="pass()">Submit</button></form>
<script>
async function pass() {
  const tok = document.querySelector('[name=h-captcha-response]').value;
  await fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e: 'wall:' + (tok ? 'token' : 'empty')}), keepalive: true});
  if (tok) location.href = '` + back + `';
}
</script>`
		case r.URL.Query().Get("rdw") == "ok":
			return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` +
				fieldNames + fieldQuestions + `<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
		}
		return fmt.Sprintf(`<title>Redirecting</title><script>location.replace('https://%s:%s/jobs/%s?ssa=1')</script>`, wallHost, port, b.marker)
	}
}

// A challenge-only page on a listed bot manager's validation host, in front of
// the form, is solved once and its plain Submit pressed; the walk reaches the
// form, and the only claim and ledger row are the application's own.
func TestE2E_Captcha_BotManagerInterstitialSolvedThenTheWalkReachesTheForm(t *testing.T) {
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = botWall("validate.perfdrive.com", "Radware Bot Manager Captcha", "Please solve this CAPTCHA to request unblock to the website.")
	svc := newTwoCaptcha(t)
	s.g.runner.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if ev := strings.Join(s.b.eventList(), ","); ev != "wall:token" || svc.tasks != 1 {
		t.Fatalf("interstitial presses %q, solves %d", ev, svc.tasks)
	}
	if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND verdict = 'allow' AND reason LIKE '%validate.perfdrive.com%'`); n != 1 {
		t.Fatalf("%d bot-manager presses allowed", n)
	}
	if run.Outcome.Kind != OutcomeSubmitted || s.b.recorded() != 1 {
		t.Fatalf("outcome %+v, recorded %d: the walk did not reach the form", run.Outcome, s.b.recorded())
	}
	if n := s.g.count(t, `SELECT count(*) FROM submissions`); n != 1 {
		t.Fatalf("%d ledger rows; only the application's own press is claimed", n)
	}
	if n := s.g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'press_claim' AND verdict = 'allow'`); n != 1 {
		t.Fatalf("%d claims", n)
	}
}

// The same challenge page on the tenant's own host may be the application's
// last step in disguise: it is never solved or pressed and ends as a bot check.
func TestE2E_Captcha_TheSameInterstitialOnTheTenantHostEndsAsABotCheck(t *testing.T) {
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = botWall("careers.example.test", "One more step", "Please complete the check to send your request.")
	svc := newTwoCaptcha(t)
	s.g.runner.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	run := s.run(submitInput(s.url))
	defer dump(t, run)
	if svc.tasks != 0 || len(s.b.eventList()) != 0 || s.b.recorded() != 0 || s.g.count(t, `SELECT count(*) FROM submissions`) != 0 {
		t.Fatalf("solves %d, presses %v, recorded %d", svc.tasks, s.b.eventList(), s.b.recorded())
	}
	if run.Outcome.Kind != OutcomeStopped || !strings.Contains(run.Outcome.Reason, "bot-challenge") {
		t.Fatalf("outcome %+v", run.Outcome)
	}
}

// TestLiveAcceptance_CaptchaOfficialDemo is deliberately opt-in: it creates at
// most one paid task, only for the solver vendor's public verification form.
// It exercises production widget detection and token delivery, not an employer
// submission or the rest of the application chain.
func TestLiveAcceptance_CaptchaOfficialDemo(t *testing.T) {
	if os.Getenv("APPLYSMITH_LIVE_CAPTCHA_ACCEPTANCE") != "official-demo-confirmed" {
		t.Skip("requires action-time confirmation and a dedicated credential")
	}
	key := os.Getenv("APPLYSMITH_SECRET_TWOCAPTCHA_KEY")
	if key == "" {
		t.Fatal("live captcha key unavailable")
	}
	_, tab, _ := widgetsTab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	const pageURL = "https://2captcha.com/demo/recaptcha-v2"
	if _, err := tab.NavigateDOM(ctx, pageURL); err != nil {
		t.Fatal(err)
	}
	var challenge CaptchaW
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, err := Probe(ctx, tab)
		if err == nil {
			for _, w := range st.Captcha {
				if w.Kind == gates.ChallengeRecaptchaV2 && w.SiteKey != "" {
					challenge = w
				}
			}
		}
		if challenge.SiteKey != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if challenge.SiteKey == "" {
		t.Fatal("official demo widget was not detected")
	}
	// Reserve one of the production two-task budget slots: this explicitly
	// opted-in official-demo component test may create at most one paid task.
	sv := captcha.New(captcha.Config{Run: gates.RunApplySubmit, Switch: gates.SolverOn, Spent: 1, Key: func() string { return key }, Skip: func() gates.SkipStatus { return gates.SkipNotListed }})
	sol, err := sv.Solve(ctx, captcha.Challenge{Kind: challenge.Kind, Point: gates.PointBeforePress, SiteKey: challenge.SiteKey, PageURL: pageURL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deliverToken(ctx, tab, challenge.Kind, sol.Token()); err != nil {
		t.Fatal(err)
	}
	if err := tab.Eval(ctx, `[...document.querySelectorAll('button')].find(b => b.textContent.trim() === 'Check').click()`, nil); err != nil {
		t.Fatal(err)
	}
	accepted := false
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := tab.Eval(ctx, `document.body.innerText.includes('Captcha is passed successfully')`, &accepted); err != nil {
			t.Fatal(err)
		}
		if accepted {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if png, err := tab.Screenshot(ctx); err == nil {
		if out := os.Getenv("APPLYSMITH_LIVE_CAPTCHA_SCREENSHOT"); out != "" {
			if err := os.WriteFile(out, png, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !accepted {
		t.Fatal("token was returned, but the official form did not show successful verification")
	}
	t.Log("official reCAPTCHA form accepted the token; one paid task maximum; no employer submission")
}

// Node's captcha/page test: a reCAPTCHA rendered from script names no
// data-callback, and the solve reaches the page through the callback in
// ___grecaptcha_cfg.clients.
func TestGates_Captcha_ExplicitlyRenderedRecaptchaCallbackFromItsConfig(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Apply</title><form id="f"><div class="g-recaptcha" style="width:304px;height:78px"></div><textarea name="g-recaptcha-response" style="display:none"></textarea></form>
<script>window.__got = []; window.___grecaptcha_cfg = { clients: { 0: { Ab: { xy: { sitekey: "k", callback: function (t) { window.__got.push(t); } } } } } };</script>`)
	d, err := deliverToken(ctx, tab, "recaptcha_v2", "token-cfg")
	if err != nil || d.Callback != "___grecaptcha_cfg" {
		t.Fatalf("delivery %+v err %v", d, err)
	}
	if got := evalString(t, tab, `window.__got.join(",")`); got != "token-cfg" {
		t.Fatalf("callback received %q", got)
	}
}
