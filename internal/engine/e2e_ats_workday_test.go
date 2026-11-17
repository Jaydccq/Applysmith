//go:build browser

package engine_test

import (
	"testing"

	"applysmith/internal/engine"
)

// A search prompt: Enter runs the search, rows arrive after a delay, a click on the leaf makes a pill.
const promptJS = `function prompt(id,catalog){const inp=document.getElementById(id),w=inp.dataset.uxiMultiselectId,list=inp.parentElement.querySelector('ul');
inp.addEventListener('keydown',e=>{if(e.key!=='Enter')return;const q=inp.value.toLowerCase().split(/\s+/);setTimeout(()=>{let root=document.querySelector('[data-associated-widget="'+w+'"]');
if(!root){root=document.createElement('div');root.setAttribute('data-associated-widget',w);root.innerHTML='<ul role=listbox></ul>';document.body.append(root)}
const ul=root.querySelector('ul');ul.innerHTML='';for(const c of catalog.filter(c=>q.some(t=>c.toLowerCase().includes(t)))){const li=document.createElement('li');li.setAttribute('role','option');
li.setAttribute('data-automation-id','promptOption');li.setAttribute('data-automation-label',c);li.innerHTML='<div data-automation-id=promptLeafNode>'+c+'</div>';
li.addEventListener('click',()=>{const p=document.createElement('li');p.setAttribute('data-automation-id','selectedItem');p.textContent=c;list.append(p);inp.value='';root.remove()});ul.append(li)}},40)})}`

const workdayPage = `<h1>Apply</h1>
<label for="firstName">Legal First Name *</label><input id="firstName"><label for="lastName">Legal Last Name *</label><input id="lastName">
<label for="email">Email *</label><input id="email" type="email">
<div><label for="phone-number">Phone Number *</label><input id="phone-number" type="tel">
<div data-automation-id="formField-cc"><label for="cc">Country Phone Code</label><div data-automation-id="multiSelectContainer"><ul data-automation-id="selectedItemList"></ul><input id="cc" data-uxi-widget-type="selectinput" data-uxi-multiselect-id="W-cc"></div></div></div>
<h3>Education 1</h3>
<div data-automation-id="formField-school"><label for="education-1--school">School or University</label><div data-automation-id="multiSelectContainer"><ul data-automation-id="selectedItemList"></ul><input id="education-1--school" data-uxi-widget-type="selectinput" data-uxi-multiselect-id="W-school"></div></div>
<label for="education-1--major">Field of Study</label><input id="education-1--major">
<script>` + promptJS + `
prompt('cc',['United States Minor Outlying Islands (+1)','United States of America (+1)','Antigua and Barbuda (+1264)']);
prompt('education-1--school',['Example University','Example College']);</script>`

// The pills of a Workday prompt, by its input id.
func pills(t *testing.T, e interface {
	MainEval(string, any)
}, id string) []string {
	t.Helper()
	var out []string
	e.MainEval(`Array.from(document.getElementById(`+jsq(id)+`).parentElement.querySelectorAll('[data-automation-id=selectedItem]')).map(p=>p.textContent)`, &out)
	return out
}

func TestE2E_Workday_SearchPromptsAndPhoneCode(t *testing.T) {
	runATS(t, atsCase{
		name: "workday", host: "example.wd5.myworkdayjobs.com", path: "/en-US/site/job/x/apply/e2e-workday", body: workdayPage,
		want: map[string]string{"Legal First Name": "Alex", "Legal Last Name": "Example", "Email": "alex@example.com", "Field of Study": "Computer Science"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			if got := pills(t, e, "education-1--school"); len(got) != 1 || got[0] != "Example University" {
				t.Errorf("school pills %v", got)
			}
			if got := pills(t, e, "cc"); len(got) != 1 || got[0] != "United States of America (+1)" {
				t.Errorf("phone code pills %v", got)
			}
			if res.Top.Summary.Site != "workday" {
				t.Errorf("site %q", res.Top.Summary.Site)
			}
		},
	})
}
