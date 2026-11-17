//go:build browser

package engine_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/engine"
)

// --- SmartRecruiters ---------------------------------------------------------------------------

const smartRecruitersPage = `<spl-input id="fn" label="First Name"></spl-input><spl-input id="ln" label="Last Name"></spl-input><spl-input id="office" label="Office location"></spl-input>
<script>
class Spl extends HTMLElement{connectedCallback(){if(this.shadowRoot)return;this.calls=[];const r=this.attachShadow({mode:'open'});
r.innerHTML='<label for=i>'+this.getAttribute('label')+'</label><input id=i>';r.querySelector('input').addEventListener('input',e=>{e.target.value=''})}
handleChange(v){this.calls.push(v);this.shadowRoot.querySelector('input').value=String(v);return Promise.resolve()}}
customElements.define('spl-input',Spl);
</script>`

func TestE2E_SmartRecruiters_ComponentsTakeTheirValueThroughTheirOwnMethod(t *testing.T) {
	runATS(t, atsCase{
		name: "smartrecruiters", host: "jobs.smartrecruiters.com", path: "/Example/743999-engineer", body: smartRecruitersPage, helper: true,
		want: map[string]string{"First Name": "Alex", "Last Name": "Example"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var calls []int
			e.MainEval(`['fn','ln','office'].map(id=>document.getElementById(id).calls.length)`, &calls)
			if len(calls) != 3 || calls[0] != 1 || calls[1] != 1 || calls[2] != 0 {
				t.Errorf("component method calls %v: the name boxes once each and Office location never", calls)
			}
		},
	})
}

// --- Paylocity ----------------------------------------------------------------------------------

const paylocityPage = `<h3>Work History</h3>
<label for="workHistory.0.company">Company</label><input id="workHistory.0.company">
<label id="cl" for="workHistory.0.country">Country</label>
<div id="workHistory.0.country" class="rw-dropdownlist rw-widget" role="combobox" aria-haspopup="listbox" aria-labelledby="cl" tabindex="0"><span class="rw-dropdownlist-value">Select...</span></div>
<script>
const P='__reactProps$e2e';
const company=document.getElementById('workHistory.0.company');let state='';
company[P]={onChange:e=>{e.persist();state=e.target.value},onBlur:e=>{e.persist()}};
company.addEventListener('input',()=>setTimeout(()=>{if(company.value!==state)company.value=state},1));
const root=document.getElementById('workHistory.0.country'),val=root.querySelector('.rw-dropdownlist-value');
root[P]={onClick:()=>{if(root.parentElement.querySelector('.rw-popup-container'))return;const pop=document.createElement('div');pop.className='rw-popup-container';
pop.innerHTML='<ul class="rw-list" role="listbox"><li class="rw-list-option" role="option">United States of America</li><li class="rw-list-option" role="option">Canada</li></ul>';
pop.querySelectorAll('li').forEach(li=>{li[P]={onClick:()=>{val.textContent=li.textContent;pop.remove()}}});root.after(pop)}};
</script>`

func TestE2E_Paylocity_RowsAreWrittenThroughTheComponentsHandlers(t *testing.T) {
	runATS(t, atsCase{
		name: "paylocity", host: "recruiting.paylocity.com", path: "/recruiting/jobs/Apply/1/example", body: names + paylocityPage, helper: true,
		want: map[string]string{"First Name": "Alex"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			if got := val(t, e, "workHistory.0.company"); got != "Example Labs" {
				t.Errorf("company %q", got)
			}
			var shown string
			e.MainEval(`document.querySelector('.rw-dropdownlist-value').textContent`, &shown)
			if shown != "United States of America" {
				t.Errorf("country %q", shown)
			}
		},
	})
}

// --- LinkedIn Easy Apply ---------------------------------------------------------------------

func TestE2E_LinkedIn_OnlyTheEasyApplyDialogIsScanned(t *testing.T) {
	feed := strings.Repeat(`<button type="button" role="combobox" aria-haspopup="listbox" aria-expanded="false" class="feed-filter">Filter</button>`, 30)
	body := `<main>` + feed + `</main><div role="dialog" class="jobs-easy-apply-modal"><h2>Apply to Example Corp</h2>` + names +
		`<fieldset><legend>` + sponsorQ + `</legend><label><input type="radio" name="sp" value="Yes"> Yes</label><label><input type="radio" name="sp" value="No"> No</label></fieldset>
<button type="button" id="next">Next</button></div>
<script>window.__opened=0;document.querySelectorAll('.feed-filter').forEach(b=>['mousedown','click','keydown'].forEach(t=>b.addEventListener(t,()=>window.__opened++)));
document.getElementById('next').addEventListener('click',()=>window.__next=true)</script>`
	runATS(t, atsCase{
		name: "linkedin", host: "www.linkedin.com", path: "/jobs/view/1234567890/", body: body,
		want: map[string]string{"First Name": "Alex", sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var opened int
			var next bool
			e.MainEval(`window.__opened`, &opened)
			e.MainEval(`!!window.__next`, &next)
			if opened != 0 || next {
				t.Errorf("feed comboboxes touched %d times, Next pressed %v", opened, next)
			}
			if n := len(res.Top.Summary.Rows); n != 4 {
				t.Errorf("%d rows, want the dialog's 4", n)
			}
		},
	})
}

// --- the submit observer ----------------------------------------------------------------------

func TestE2E_Observer_ASubmissionIsSeenAcrossTheNavigationItCauses(t *testing.T) {
	e := engine.NewE2E(t)
	e.Serve("/obs-form", `<h1>Apply</h1><button id="go" type="button">Send</button>
<script>document.getElementById('go').addEventListener('click',async()=>{const r=await fetch('/obs-submit?token=secret',{method:'POST',body:'a=1'});if(r.ok)location.href='/obs-thanks'})</script>`)
	e.Serve("/obs-submit", `ok`)
	e.Serve("/obs-thanks", `<h1>Thanks</h1>`)
	p := e.Page(engine.Host{Matcher: matcher(t), Resumes: newResumes(t)})
	e.Navigate(e.URL("jobs.example.test", "/obs-form"))
	e.Observe(p)
	e.Navigate(e.URL("jobs.example.test", "/obs-form"))
	var ignored any
	e.MainEval(`document.getElementById('go').click()`, &ignored)
	deadline := time.Now().Add(20 * time.Second)
	for {
		var path string
		e.MainEval(`location.pathname`, &path)
		if path == "/obs-thanks" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the page did not navigate: %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := e.Observations(p)
	if len(got) != 1 || got[0].Kind != "request" || got[0].Method != "POST" || got[0].Status != 200 || !strings.HasSuffix(got[0].URL, "/obs-submit") || strings.Contains(fmt.Sprint(got), "secret") {
		t.Fatalf("observations %+v", got)
	}
}
