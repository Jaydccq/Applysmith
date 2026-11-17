//go:build browser

package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"applysmith/internal/engine"
)

// --- Ashby -----------------------------------------------------------------------------------

const ashbyPage = `<div data-field-path="firstName"><label for="firstName">First Name *</label><input id="firstName" name="firstName"></div>
<div data-field-path="lastName"><label for="lastName">Last Name *</label><input id="lastName" name="lastName"></div>
<div data-field-path="email"><label for="email">Email *</label><input id="email" type="email" name="email"></div>
<div data-field-path="sponsor" class="_fieldEntry_x1"><fieldset><legend>` + sponsorQ + ` *</legend><div class="_yesno_a1"><button type="button" aria-pressed="false">Yes</button><button type="button" aria-pressed="false">No</button></div></fieldset></div>
<script>
const server={};let inflight=0,maxIn=0,n=0;const yn={};window.__server=server;
function whole(){const f={};for(const i of document.querySelectorAll('input'))if(i.value)f[i.id]=i.value;for(const k in yn)if(yn[k])f[k]=yn[k];return f}
function save(){const body=JSON.stringify({fields:whole()});fetch('/api/non-user-graphql?op=ApiSetFormValue',{method:'POST',body})}
window.fetch=async(u,init)=>{const b=JSON.parse(init.body);const k=n++;inflight++;maxIn=Math.max(maxIn,inflight);window.__maxIn=maxIn;await new Promise(r=>setTimeout(r,70-Math.min(k,3)*20));inflight--;
for(const key of Object.keys(server))delete server[key];Object.assign(server,b.fields);
return new Response(JSON.stringify({data:{form:{sections:[{fieldEntries:['firstName','lastName','email','sponsor'].map(p=>({path:p,value:server[p]||null,required:true}))}]}}}),{status:200})};
document.addEventListener('input',save);
document.querySelectorAll('[data-field-path=sponsor] button').forEach(b=>b.addEventListener('click',()=>{const was=b.getAttribute('aria-pressed')==='true';
document.querySelectorAll('[data-field-path=sponsor] button').forEach(x=>x.setAttribute('aria-pressed',String(x===b&&!was)));yn.sponsor=was?'':b.textContent;save()}));
</script>`

func TestE2E_Ashby_SavesGoOutOneAtATimeAndTheServerCopyHoldsEveryAnswer(t *testing.T) {
	runATS(t, atsCase{
		name: "ashby", host: "jobs.ashbyhq.com", path: "/example/abc/application", body: ashbyPage, helper: true,
		want: map[string]string{"First Name": "Alex", "Last Name": "Example", sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var server map[string]string
			e.MainEval(`window.__server`, &server)
			for k, want := range map[string]string{"firstName": "Alex", "lastName": "Example", "email": "alex@example.com", "sponsor": "No"} {
				if server[k] != want {
					t.Errorf("the server's copy holds %s = %q, want %q (copy %v)", k, server[k], want, server)
				}
			}
			var max int
			e.MainEval(`window.__maxIn`, &max)
			if max != 1 {
				t.Errorf("%d saves were in flight at once", max)
			}
		},
	})
}

// --- Greenhouse ---------------------------------------------------------------------------------

const greenhousePage = names + `<label for="q1">` + sponsorQ + `</label>
<div class="select-container"><div class="select__control"><div class="select__value-container"><div class="select__placeholder">Select...</div><div class="select__input-container">
<input id="q1" role="combobox" aria-haspopup="true" aria-autocomplete="list" aria-expanded="false"></div></div></div></div>
<script>
const control=document.querySelector('.select__control'),input=document.getElementById('q1'),box=document.querySelector('.select__value-container');
control.addEventListener('mousedown',()=>{if(document.getElementById('react-select-q1-listbox'))return;const m=document.createElement('div');m.id='react-select-q1-listbox';
m.innerHTML='<div class="select__menu">'+['Yes','No'].map(t=>'<div role=option class="select__option" aria-selected=false>'+t+'</div>').join('')+'</div>';
control.parentElement.append(m);m.querySelectorAll('[role=option]').forEach(o=>o.addEventListener('mousedown',()=>{
box.querySelector('.select__placeholder').hidden=true;const s=document.createElement('div');s.className='select__single-value';s.textContent=o.textContent;box.prepend(s);m.remove()}))});
</script>`

func TestE2E_Greenhouse_ReactSelectIsOpenedWithItsOwnGesturesAndTheOptionIsChosen(t *testing.T) {
	runATS(t, atsCase{
		name: "greenhouse", host: "job-boards.greenhouse.io", path: "/example/jobs/4001", body: greenhousePage,
		want: map[string]string{"First Name": "Alex", sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var shown string
			e.MainEval(`document.querySelector('.select__single-value')?.textContent||''`, &shown)
			if shown != "No" {
				t.Errorf("the select shows %q", shown)
			}
		},
	})
}

// --- Lever --------------------------------------------------------------------------------------

var leverPage = `<script type="application/ld+json">{"@context":"https://schema.org","@type":"JobPosting","title":"Engineer","hiringOrganization":{"name":"Example Corp"},
"jobLocation":{"address":{"addressLocality":"Durham","addressRegion":"NC","addressCountry":"United States"}},"description":"` + strings.Repeat("We build small reliable tools. ", 10) + `"}</script>` + names + `<label for="location-input">Location</label><input id="location-input" name="location" type="text" autocomplete="off"><input type="hidden" name="selectedLocation" value="">
<div class="dropdown-container"></div>
<script>
const input=document.getElementById('location-input'),hidden=document.querySelector('[name=selectedLocation]'),box=document.querySelector('.dropdown-container');
const places=['Durham, England, UK','Durham, NC, USA','Raleigh, NC, USA'];
input.addEventListener('keydown',()=>setTimeout(()=>{const q=input.value.toLowerCase();box.innerHTML='';if(!q)return;
for(const p of places.filter(p=>p.toLowerCase().includes(q))){const r=document.createElement('div');r.className='dropdown-location';r.textContent=p;let d=false,u=false;
r.addEventListener('mousedown',()=>d=true);r.addEventListener('mouseup',()=>u=d);
r.addEventListener('click',()=>{if(!(d&&u)){input.value='';return}input.value=p;hidden.value=JSON.stringify({name:p});box.innerHTML=''});box.append(r)}},20));
</script>`

func TestE2E_Lever_LocationIsFilledOnlyWhenTheHiddenFieldHoldsIt(t *testing.T) {
	runATS(t, atsCase{
		name: "lever", host: "jobs.lever.co", path: "/example/abc/apply", body: leverPage,
		want: map[string]string{"First Name": "Alex"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var hidden string
			e.MainEval(`document.querySelector('[name=selectedLocation]').value`, &hidden)
			if !strings.Contains(hidden, "Durham, NC") {
				t.Errorf("the hidden location holds %q", hidden)
			}
		},
	})
}

// --- Rippling -------------------------------------------------------------------------------------

func TestE2E_Rippling_TheApplicantsValueWinsOverTheParsersGuess(t *testing.T) {
	runATS(t, atsCase{
		name: "rippling", host: "ats.rippling.com", path: "/example/jobs/abc/apply",
		body: names + `<label for="curco">Current company</label><input id="curco" value="Wrong Parse Inc">`,
		want: map[string]string{"Current company": "Example Labs"},
	})
}

// --- Workable -------------------------------------------------------------------------------------

func TestE2E_Workable_ChooseFileIsUploadStateNotAQuestion(t *testing.T) {
	runATS(t, atsCase{
		name: "workable", host: "apply.workable.com", path: "/example/j/ABC/apply/",
		body: names + `<label for="cv" role="button">Choose file</label><input id="cv" type="file">`,
		want: map[string]string{"First Name": "Alex"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			for _, r := range res.Top.Summary.Rows {
				if strings.EqualFold(r.Label, "Choose file") {
					t.Errorf("Choose file became a question: %+v", r)
				}
				if r.Kind == "file" && (r.Outcome != "filled" || r.FileName != "resume-alex-example.pdf") {
					t.Errorf("file row %+v", r)
				}
			}
		},
	})
}

// --- Breezy ---------------------------------------------------------------------------------------

const breezyPage = `<h1 style="display:none">Application Submitted</h1><h1>Staff Engineer</h1>` + names + `<label for="cv">Resume</label><input id="cv" type="file">
<div id="hist"><h3>Work History 1</h3><div class="row"><label for="c1">Company</label><input id="c1"><label for="t1">Job Title</label><input id="t1"><button type="button" class="del" aria-label="Remove work history 1">Remove</button></div></div>
<button type="button" id="add">Add work history</button>
<script>
let n=1;const hist=document.getElementById('hist');
function row(c,t){n++;const d=document.createElement('div');d.innerHTML='<h3>Work History '+n+'</h3><div class="row"><label for="c'+n+'">Company</label><input id="c'+n+'" value="'+c+'"><label for="t'+n+'">Job Title</label><input id="t'+n+'" value="'+t+'">'+
'<button type="button" class="del" aria-label="Remove work history '+n+'">Remove</button></div>';hist.append(d);d.querySelector('.del').addEventListener('click',()=>d.remove())}
document.querySelector('.del').addEventListener('click',e=>e.target.closest('.row').parentElement.remove());
document.getElementById('add').addEventListener('click',()=>row('',''));
new MutationObserver(()=>{let i=0;hist.querySelectorAll('h3').forEach(h=>h.textContent='Work History '+(++i))}).observe(hist,{childList:true});
document.getElementById('cv').addEventListener('change',()=>{row('Project Alpha','');row('Project Beta','')});
</script>`

func TestE2E_Breezy_ParserRowsAreReconciledToTheProfile(t *testing.T) {
	runATS(t, atsCase{
		name: "breezy", host: "example.breezy.hr", path: "/p/abc-engineer/apply", body: breezyPage,
		want: map[string]string{"First Name": "Alex"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var companies []string
			e.MainEval(`Array.from(document.querySelectorAll('#hist .row input[id^=c]')).map(i=>i.value)`, &companies)
			b, _ := json.Marshal(companies)
			if len(companies) != 2 || !strings.Contains(string(b), "Example Labs") || !strings.Contains(string(b), "Sample Works") || strings.Contains(string(b), "Project") {
				t.Errorf("work history companies %s", b)
			}
			var titles []string
			e.MainEval(`Array.from(document.querySelectorAll('#hist .row input[id^=t]')).map(i=>i.value)`, &titles)
			if len(titles) != 2 || titles[0] == "" || titles[1] == "" {
				t.Errorf("titles %v", titles)
			}
		},
	})
}

// --- JazzHR ---------------------------------------------------------------------------------------

func TestE2E_JazzHR_CheckboxPairIsOneQuestion(t *testing.T) {
	runATS(t, atsCase{
		name: "jazzhr", host: "example.applytojob.com", path: "/apply/abc/Engineer",
		body: names + `<div class="form-group"><label class="question">` + sponsorQ + `</label><label><input type="checkbox" name="q1-1" class="resumator-questionnaire-checkbox" id="y"> Yes</label>
<label><input type="checkbox" name="q1-2" class="resumator-questionnaire-checkbox" id="n"> No</label></div>`,
		want: map[string]string{sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var checked []bool
			e.MainEval(`[document.getElementById('y').checked,document.getElementById('n').checked]`, &checked)
			if len(checked) != 2 || checked[0] || !checked[1] {
				t.Errorf("boxes %v", checked)
			}
		},
	})
}

// --- BambooHR ---------------------------------------------------------------------------------------

const bambooPage = names + `<label for="auth">` + sponsorQ + `</label>
<div class="fab-SelectField"><button id="auth" type="button" class="fab-SelectToggle" role="combobox" aria-haspopup="listbox" data-menu-id="m1">Select...</button>
<select aria-hidden="true" style="opacity:0;width:0;height:0"><option value=""></option></select></div>
<script>
const b=document.getElementById('auth');
b.addEventListener('click',e=>{if(e.detail<1||document.getElementById('m1'))return;const m=document.createElement('ul');m.id='m1';m.setAttribute('role','menu');
m.innerHTML='<li role=menuitem>Yes</li><li role=menuitem>No</li>';document.body.append(m);m.querySelectorAll('li').forEach(li=>li.addEventListener('click',()=>{b.textContent=li.textContent;m.remove()}))});
</script>`

func TestE2E_BambooHR_FabricSelectOpensOnARealClickAndTakesTheOption(t *testing.T) {
	runATS(t, atsCase{
		name: "bamboohr", host: "example.bamboohr.com", path: "/careers/12", body: bambooPage,
		want: map[string]string{"First Name": "Alex", sponsorQ: "No"},
	})
}

// --- Jobvite ------------------------------------------------------------------------------------------

func TestE2E_Jobvite_FrameWithOneRequiredFieldIsAdmitted(t *testing.T) {
	e := engine.NewE2E(t)
	e.Serve("/example/job/o1/apply", `<h1>Apply</h1><iframe id="f" src="/jobvite-step" width="500" height="200"></iframe>`)
	e.Serve("/jobvite-step", `<label for="first">First Name *</label><input id="first" required>`)
	p := e.Page(engine.Host{Matcher: matcher(t), Resumes: newResumes(t)})
	e.Navigate(e.URL("jobs.jobvite.com", "/example/job/o1/apply"))
	res, err := e.Fill(p, engine.FillOptions{Applicant: applicantFn, Timing: fastTiming()})
	if err != nil {
		t.Fatal(err)
	}
	admitted := 0
	for _, d := range res.Decisions {
		if d.Admitted() && d.Controls == 1 {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("decisions %+v", res.Decisions)
	}
	for _, f := range res.Frames {
		if f.Status != nil && f.Status.Summary != nil && len(f.Status.Summary.Rows) == 1 && f.Status.Summary.Rows[0].Value == "Alex" {
			return
		}
	}
	t.Fatalf("the framed field was not filled: %+v", res.Frames)
}
