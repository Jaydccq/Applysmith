//go:build browser

package engine_test

import (
	"testing"

	"applysmith/internal/engine"
)

const names = `<label for="firstName">First Name *</label><input id="firstName" name="firstName"><label for="lastName">Last Name *</label><input id="lastName" name="lastName">
<label for="email">Email *</label><input id="email" type="email">`

const sponsorQ = "Will you now require visa sponsorship to work for us?"

// --- Oracle Recruiting Cloud -------------------------------------------------------------

const oraclePage = names + `
<div class="cx-select-container"><label for="country">Country</label>
<input class="cx-select-input" id="country" name="country" role="combobox" aria-haspopup="grid" aria-expanded="false" aria-controls="country-grid" autocomplete="off">
<div id="country-grid" role="grid" class="cx-select-dropdown" hidden></div></div>
<div id="block"></div>
<script>
const row=t=>'<div role=row><div role=gridcell><span class="cx-select__list-item--content">'+t+'</span></div></div>';
const input=document.getElementById('country'),grid=document.getElementById('country-grid'),all=['United States Minor Outlying Islands','United States','Canada'];
function show(rows){grid.hidden=false;grid.classList.add('cx-select-dropdown--open');grid.innerHTML=rows.length?rows.map(row).join(''):row('No results found');
grid.querySelectorAll('[role=gridcell]').forEach(c=>{let h=false;c.addEventListener('mouseover',()=>h=true);c.addEventListener('click',()=>{if(!h)return;input.value=c.textContent;grid.hidden=true;grid.classList.remove('cx-select-dropdown--open');mountAddress()})})}
input.addEventListener('mousedown',()=>show([]));
input.addEventListener('input',()=>{const q=input.value.trim().toLowerCase();show(q?all.filter(x=>x.toLowerCase().includes(q)):[])});
function mountAddress(){setTimeout(()=>{document.getElementById('block').innerHTML='<label for=addressLine1>Address Line 1</label><input id=addressLine1 name=addressLine1 class="cx-select-input cx-select-input--auto-suggest" role=combobox aria-autocomplete=list aria-haspopup=grid aria-controls=addr-grid autocomplete=off><div id=addr-grid role=grid></div>'+
'<label for=city>City</label><input id=city name=city><label for=region1>State</label><input id=region1 name=region1><label for=postalCode>Postal Code</label><input id=postalCode name=postalCode>';
const a=document.getElementById('addressLine1'),g=document.getElementById('addr-grid');let t;
a.addEventListener('input',()=>{clearTimeout(t);t=setTimeout(()=>{g.innerHTML=row('1 Example Street, Houston, TX 77001');setTimeout(()=>{g.innerHTML=row('1 Example Street, Houston, TX 77001')+row('1 Example Street, Durham, NC 27701');
g.querySelectorAll('[role=gridcell]').forEach(c=>c.addEventListener('click',()=>{const p=c.textContent.split(',').map(s=>s.trim());a.value=c.textContent;setTimeout(()=>{document.getElementById('city').value=p[1];
const m=/([A-Z]{2}) (\d{5})/.exec(p[2]);document.getElementById('region1').value=m[1];document.getElementById('postalCode').value=m[2]},60)}))},300)},120)})},100)}
</script>`

func TestE2E_Oracle_CountryFirstThenAddressMasterWithCascade(t *testing.T) {
	runATS(t, atsCase{
		name: "oracle", host: "example.fa.us2.oraclecloud.com", path: "/hcmUI/CandidateExperience/en/sites/CX/apply", body: oraclePage,
		want: map[string]string{"First Name": "Alex", "Last Name": "Example", "Email": "alex@example.com", "Country": "United States"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			for id, want := range map[string]string{"addressLine1": "1 Example Street, Durham, NC 27701", "city": "Durham", "region1": "NC", "postalCode": "27701"} {
				if got := val(t, e, id); got != want {
					t.Errorf("%s = %q, want %q", id, got, want)
				}
			}
		},
	})
}

// --- SuccessFactors ------------------------------------------------------------------------

const successFactorsPage = `<a id="app_expandAllSections" href="#">Expand all sections</a><a id="app_collapseAllSections" href="#">Collapse all sections</a>
<section id="personal" style="display:none">` + names + `</section>
<section><a id="r_attachIcon" href="#">+</a><form id="form_upload" style="display:none"><input type="file" name="fileData" class="fileUpload" id="sf_file"></form><span id="chip"></span></section>
<script>
document.getElementById('app_expandAllSections').addEventListener('click',e=>{e.preventDefault();setTimeout(()=>document.getElementById('personal').style.display='',40)});
const f=document.getElementById('sf_file');let asked=false;
document.getElementById('r_attachIcon').addEventListener('click',e=>{e.preventDefault();asked=true;f.click()});
f.addEventListener('change',()=>{if(!asked){f.value='';return}asked=false;document.getElementById('chip').textContent=f.files[0].name});
</script>`

func TestE2E_SuccessFactors_ExpandAllAndUploadThroughTheHelper(t *testing.T) {
	runATS(t, atsCase{
		name: "successfactors", host: "career5.successfactors.com", path: "/career", body: successFactorsPage, helper: true,
		want: map[string]string{"First Name": "Alex", "Last Name": "Example", "Email": "alex@example.com"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var chip string
			e.MainEval(`document.getElementById('chip').textContent`, &chip)
			if chip != "resume-alex-example.pdf" {
				t.Errorf("the page's uploader holds %q", chip)
			}
		},
	})
}

// --- Taleo ---------------------------------------------------------------------------------

func TestE2E_Taleo_GenericPassesListWhatTheyCouldNotFill(t *testing.T) {
	runATS(t, atsCase{
		name: "taleo", host: "example.taleo.net", path: "/careersection/ex/apply.ftl",
		body: names + `<div id="rating" role="slider" tabindex="0" aria-label="Rate your skill" aria-valuenow="0"></div>`,
		want: map[string]string{"First Name": "Alex", "Email": "alex@example.com"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			if res.Top.Summary.Site != "taleo" {
				t.Errorf("site %q", res.Top.Summary.Site)
			}
			for _, r := range res.Top.Summary.Rows {
				if r.Label == "Rate your skill" && r.Outcome == "skipped" && r.Reason == "driver-unavailable" {
					return
				}
			}
			t.Errorf("the unknown widget is not listed as skipped: %+v", res.Top.Summary.Rows)
		},
	})
}

// --- iCIMS ---------------------------------------------------------------------------------

const icimsPage = names + `
<div><label for="spons">` + sponsorQ + `</label><select id="spons" name="spons" icimsdropdown-enabled="1"><option value="">Make a Selection</option></select>
<a class="dropdown-select" href="#" role="button"><span class="dropdown-text">Make a Selection</span></a>
<div class="dropdown-container"><ul class="dropdown-results" style="display:none"><li class="result-selectable" id="o1">Yes</li><li class="result-selectable" id="o2">No</li></ul></div></div>
<script>
const sel=document.getElementById('spons'),list=document.querySelector('.dropdown-results'),text=document.querySelector('.dropdown-text');
document.querySelector('.dropdown-select').addEventListener('mousedown',e=>{e.preventDefault();list.style.display=list.style.display==='none'?'block':'none'});
list.querySelectorAll('li').forEach(li=>li.addEventListener('click',()=>{sel.innerHTML='<option value="'+li.textContent+'" selected>'+li.textContent+'</option>';sel.value=li.textContent;text.textContent=li.textContent;list.style.display='none'}));
</script>`

func TestE2E_Icims_PlaceholderSelectChoosesFromTheCustomList(t *testing.T) {
	runATS(t, atsCase{
		name: "icims", host: "careers-example.icims.com", path: "/jobs/1/engineer/job", body: icimsPage,
		want: map[string]string{"First Name": "Alex", sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			if res.Top.Summary.Site != "icims" {
				t.Errorf("site %q", res.Top.Summary.Site)
			}
			if got := val(t, e, "spons"); got != "No" {
				t.Errorf("the hidden select holds %q", got)
			}
		},
	})
}

// --- ADP WorkforceNow ------------------------------------------------------------------------

func TestE2E_Adp_MobileAndHomePhoneBothTakeTheApplicantsPhone(t *testing.T) {
	runATS(t, atsCase{
		name: "adp", host: "workforcenow.adp.com", path: "/mascsr/default/mdf/recruitment/recruitment.html",
		body: names + `<label for="mob">Phone Number *</label><input id="mob" type="tel" required><label for="home">Phone Number *</label><input id="home" type="tel" required value="+1">
<label for="emp">Employer phone</label><input id="emp" type="tel">`,
		want: map[string]string{"First Name": "Alex"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			for _, id := range []string{"mob", "home"} {
				if got := val(t, e, id); got == "" || got == "+1" {
					t.Errorf("%s = %q", id, got)
				}
			}
			if got := val(t, e, "emp"); got != "" {
				t.Errorf("the employer phone box holds %q", got)
			}
		},
	})
}

// --- Dayforce --------------------------------------------------------------------------------

const dayforcePage = `<h1>Apply</h1><div id="rows"></div><button type="button" id="add">Add Employment History</button><button type="button" id="cancel">Cancel</button><button type="button" id="next">Next</button>
<script>
let n=0;const rows=document.getElementById('rows');
function add(){const i=n++;const f=document.createElement('form');f.id='workHistory-'+i;
f.innerHTML='<label for="jobPostingApplication_workHistory_'+i+'_company">Company</label><input id="jobPostingApplication_workHistory_'+i+'_company">'+
'<label for="jobPostingApplication_workHistory_'+i+'_jobTitle">Job Title</label><input id="jobPostingApplication_workHistory_'+i+'_jobTitle">';rows.append(f)}
add();document.getElementById('add').addEventListener('click',add);
document.getElementById('cancel').addEventListener('click',()=>window.__cancel=true);document.getElementById('next').addEventListener('click',()=>window.__next=true);
</script>`

func TestE2E_Dayforce_RowsAreAddedAndFilledFromTheirOwnEntries(t *testing.T) {
	runATS(t, atsCase{
		name: "dayforce", host: "jobs.dayforcehcm.com", path: "/en-US/example/CANDIDATEPORTAL/jobs/1", body: dayforcePage,
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			for id, want := range map[string]string{
				"jobPostingApplication_workHistory_0_company": "Example Labs", "jobPostingApplication_workHistory_0_jobTitle": "Staff Engineer",
				"jobPostingApplication_workHistory_1_company": "Sample Works", "jobPostingApplication_workHistory_1_jobTitle": "Engineer",
			} {
				if got := val(t, e, id); got != want {
					t.Errorf("%s = %q, want %q", id, got, want)
				}
			}
			var pressed bool
			e.MainEval(`!!(window.__cancel||window.__next)`, &pressed)
			if pressed {
				t.Error("the page-level Cancel or Next was pressed")
			}
		},
	})
}
