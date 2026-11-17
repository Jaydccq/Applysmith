//go:build browser

package apply

import (
	"fmt"
	"net/http"
	"strings"
)

// The LinkedIn mocks. The flow URL (/jobs/view/<id>/apply/) renders the Easy
// Apply modal a moment after the document is parsed, the posting page carries a
// button that opens the same modal, and each variant takes one thing away or
// puts one thing in the way.

const lkNotFound = `<title>Page not found | LinkedIn</title><h1>Seite nicht gefunden</h1><h1>Page not found</h1>`

// lkSDUINotFound is the not-found page of the server-driven layout: one h2,
// no title, and the flow URL answers with it for a live posting.
const lkSDUINotFound = `<main><h2>0 notifications</h2><h2>This page doesn’t exist</h2><p>Please check your URL or return to LinkedIn home.</p></main>`

func lkPayload(b *board, id string) string {
	return fmt.Sprintf(`<code style="display:none">{"included":[{"entityUrn":"urn:li:fsd_jobPosting:4000000099","companyApplyUrl":%q},{"entityUrn":"urn:li:fsd_jobPosting:%s","companyApplyUrl":%q}]}</code>`, b.decoy, id, b.dest)
}

func linkedinPage(b *board, cover string, r *http.Request) string {
	flow := strings.Contains(r.URL.Path, "/apply")
	head := `<title>Platform Engineer | Example Corp | LinkedIn</title>`
	switch b.kind {
	case "linkedin-gone":
		return lkNotFound
	case "linkedin-404flow":
		if flow {
			return lkNotFound
		}
	case "linkedin-sdui":
		if flow {
			return lkSDUINotFound
		}
	case "linkedin-offsite":
		return head + `<main><h1>Platform Engineer</h1><p>Example Corp - Durham, NC</p><button type="button" role="link">Apply</button>` +
			`<section><h2>Similar jobs</h2><button type="button" role="link">Apply</button></section></main>` + lkPayload(b, b.marker)
	case "linkedin-decoyonly":
		// The payload names a destination for another posting only.
		return head + `<main><h1>Platform Engineer</h1><button type="button" role="link">Apply</button></main>` +
			fmt.Sprintf(`<code style="display:none">{"included":[{"entityUrn":"urn:li:fsd_jobPosting:4000000099","companyApplyUrl":%q}]}</code>`, b.decoy)
	case "linkedin-guest":
		return head + `<main><h1>Platform Engineer</h1><a href="/signup?m=` + b.marker + `" data-tracking-control-name="public_jobs_apply-link-onsite">Apply</a></main>`
	case "linkedin-closed", "linkedin-applied":
		note := "No longer accepting applications"
		if b.kind == "linkedin-applied" {
			note = "You applied on company site 9/24/2026"
		}
		// The notice appears seconds after the page: it is polled for, not read once.
		return head + `<main><h1>Platform Engineer</h1><p>Example Corp</p><div id="slot"></div></main>` +
			`<script>setTimeout(() => { document.getElementById('slot').textContent = ` + fmt.Sprintf("%q", note) + `; }, 1200);</script>`
	}
	late, hang := "", ""
	if b.kind == "linkedin-late" {
		late = "setTimeout(openModal, 1500);"
		hang = `<img src="/hang/` + b.marker + `" alt="">`
	} else if flow {
		late = "setTimeout(openModal, 400);"
	}
	captcha := `<iframe src="https://www.google.com/recaptcha/api2/anchor?k=site-key" width="300" height="80" title="reCAPTCHA"></iframe>`
	step1, step3 := "", ""
	switch b.kind {
	case "linkedin-challenge-next":
		step1 = captcha
	case "linkedin-challenge-submit":
		step3 = captcha
	}
	silent := mockMode(b.mode.Load()) == modeSilent
	send := "sendApplication();"
	if silent {
		send = "window.__silentPress = true;"
	}
	// The server-driven layout opens a native <dialog> headed "Apply to
	// <company>", with no role and no Easy Apply class.
	modal, open, endModal := `<div id="modal" class="jobs-easy-apply-modal" role="dialog" aria-modal="true" hidden style="position:fixed;left:10%;top:10%;width:80%;background:#fff;border:1px solid #000;padding:20px;z-index:1000">`,
		`document.getElementById('modal').hidden = false;`, `</div>`
	if b.kind == "linkedin-sdui" {
		modal, open, endModal = `<dialog id="modal" data-testid="dialog" style="width:80%"><h2>Apply to Example Corp</h2><p>1/3 pages</p>`,
			`document.getElementById('modal').showModal();`, `</dialog>`
	}
	// LinkedIn's "Job search safety reminder" stands between Easy Apply and
	// the modal, a native <dialog> as on the live page; its "Continue
	// applying" is a link that opens the modal, "Review job post" a button
	// that closes it.
	reminder, opener := "", "openModal()"
	if b.kind == "linkedin-safety" {
		reminder = `<dialog id="safety" data-testid="dialog" style="width:60%">
<h2>Job search safety reminder</h2><p>Research the company. Before you apply, review the employer's official website.</p>
<button type="button" onclick="document.getElementById('safety').close()">Review job post</button>
<a href="#continue" onclick="event.preventDefault(); document.getElementById('safety').close(); openModal()">Continue applying</a></dialog>`
		opener = "document.getElementById('safety').showModal()"
		late = strings.Replace(late, "openModal", "() => { "+opener+" }", 1)
	}
	return head + `<main><h1>Platform Engineer</h1><p>Example Corp - Durham, NC</p></main>` + cover + hang + reminder + `
<button id="open" type="button" aria-label="Easy Apply to this job" onclick="` + opener + `">Easy Apply</button>
` + modal + `
<div id="s1">` + fieldNames + step1 + `</div>
<div id="s2" hidden>` + fieldQuestions + `</div>
<div id="s3" hidden><p>Review your application</p>` + step3 + `</div>
<button id="nav" type="button" onclick="advance()">Next</button>
` + endModal + `
<script>
let cur = 1;
function openModal() { ` + open + ` }
function advance() {
  if (cur === 3) { ` + send + ` return; }
  document.getElementById('s' + cur).hidden = true;
  cur++;
  document.getElementById('s' + cur).hidden = false;
  document.getElementById('nav').textContent = cur === 2 ? 'Review' : 'Submit application';
}
` + late + `
</script>` + submitScriptAt(b, `<h1>Application sent</h1><p>Your application was sent to Example Corp.</p>`, "/voyager/api/jobs/applications/")
}

// jobvitePage serves a careers page that embeds Jobvite's wizard in a frame.
// The wizard swaps its step under one address and reports each Next to the
// board, so a test can tell an advance from a press that moved nothing.
func (s *site) jobvitePage(b *board, r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/example/apply/") {
		return `<title>Apply</title>
<div id="s1" class="jv-apply-step"><h2>Contact</h2>` + fieldNames + `</div>
<div id="s2" class="jv-apply-step" hidden><h2>Questions</h2>` + fieldQuestions + `</div>
<div id="s3" class="jv-apply-step" hidden><h2>Review</h2><p>Check your answers.</p></div>
<button id="nav" type="button" onclick="advance()">Next</button>
<script>
let cur = 1;
const tell = (e) => fetch('/__telemetry/` + b.marker + `', {method: 'POST', body: JSON.stringify({e}), keepalive: true});
function advance() {
  if (cur === 3) { sendApplication(); return; }
  document.getElementById('s' + cur).hidden = true;
  tell('next:' + cur + ':' + (cur === 1 ? document.getElementById('firstName').value : (document.querySelector('input[name=sp]:checked') || {}).value));
  cur++;
  document.getElementById('s' + cur).hidden = false;
  document.getElementById('nav').textContent = cur === 3 ? 'Submit application' : 'Next';
}
</script>` + submitScript(b, successHTML)
	}
	return fmt.Sprintf(`<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><p>Build platform tools.</p>`+
		`<iframe id="jv" src="https://jobs.jobvite.com:%d/example/apply/%s" width="700" height="520"></iframe>`, s.port(), b.marker)
}
