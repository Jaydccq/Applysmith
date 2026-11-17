//go:build browser

package engine_test

import (
	"testing"

	"applysmith/internal/engine"
)

// --- ADP recruiting (Dojo) ---------------------------------------------------------------

// A dijit select creates its popup on the first open, wants the pointer
// sequence on the option, and gives the form its code in a hidden input.
const adpDijitPage = names + `
<div class="element"><label id="st-lbl">` + sponsorQ + `</label>
<table id="st" role="listbox" widgetid="dijit_form_Select_7" aria-haspopup="true" aria-labelledby="st-lbl" class="dijit dijitSelect"><tbody><tr><td class="dijitButtonText">Please Specify</td></tr></tbody></table>
<input type="hidden" name="state" value=" "></div>
<script>
const trigger=document.getElementById('st'),hidden=document.querySelector('input[name=state]');
const states=[['Yes','Y'],['No','N']];
trigger.addEventListener('click',()=>{
  if(document.getElementById('dijit_form_Select_7_dropdown'))return;
  const pop=document.createElement('div');pop.id='dijit_form_Select_7_dropdown';
  pop.innerHTML=states.map(s=>'<div class="dijitMenuItem" role="option">'+s[0]+'</div>').join('');
  document.body.appendChild(pop);
  pop.querySelectorAll('.dijitMenuItem').forEach((row,i)=>{let down=false;row.addEventListener('mousedown',()=>down=true);
    row.addEventListener('click',()=>{if(!down)return;hidden.value=states[i][1];trigger.querySelector('.dijitButtonText').textContent=states[i][0];pop.remove()})});
});
</script>`

func TestE2E_Adp_DijitSelectCommitsHiddenCode(t *testing.T) {
	runATS(t, atsCase{
		name: "adp-dojo", host: "recruiting.adp.com", path: "/srccar/public/RT", body: adpDijitPage,
		want: map[string]string{"First Name": "Alex", sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var code string
			e.MainEval(`document.querySelector('input[name=state]').value`, &code)
			if code != "N" {
				t.Errorf("the hidden input holds %q, want the option's code N", code)
			}
		},
	})
}

// A places autocomplete reads the legacy key codes and fills its dependent
// fields from its own Enter handler; a click on a suggestion breaks it.
const adpPlacesPage = names + `
<label for="addr">Address Line 1</label><input id="addr" class="pac-target-input" autocomplete="off">
<label for="city">City</label><input id="city"><label for="zip">Postal Code</label><input id="zip">
<div class="pac-container" style="display:none"></div>
<script>
window.__clicked=false;
const addr=document.getElementById('addr'),box=document.querySelector('.pac-container');let active=-1;
addr.addEventListener('input',()=>{
  if(!addr.value){box.style.display='none';box.innerHTML='';return}
  box.style.display='block';active=-1;
  box.innerHTML='<div class="pac-item">1 Example Street, Durham, NC 27701, USA</div><div class="pac-item">1 Example Street, Houston, TX 77001, USA</div>';
  box.querySelectorAll('.pac-item').forEach(i=>i.addEventListener('click',()=>{window.__clicked=true;addr.value=''}));
});
addr.addEventListener('keydown',e=>{
  if(e.key==='ArrowDown'||e.keyCode===40){active=0;box.querySelectorAll('.pac-item')[0].classList.add('pac-item-selected')}
  if((e.key==='Enter'||e.keyCode===13)&&active===0){const p=box.querySelectorAll('.pac-item')[0].textContent.split(',').map(s=>s.trim());
    addr.value=p[0];document.getElementById('city').value=p[1];document.getElementById('zip').value=/\d{5}/.exec(p[2])[0];box.style.display='none'}
});
</script>`

func TestE2E_Adp_PlacesMasterArrowDownEnterNeverClick(t *testing.T) {
	runATS(t, atsCase{
		name: "adp-places", host: "recruiting.adp.com", path: "/srccar/public/RT", body: adpPlacesPage,
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			if got := val(t, e, "city"); got != "Durham" {
				t.Errorf("city %q: the page's own Enter handler should have filled it", got)
			}
			var clicked bool
			e.MainEval(`window.__clicked`, &clicked)
			if clicked {
				t.Error("a suggestion was clicked")
			}
		},
	})
}

// An sdf-select-simple keeps its pane either portalled to the document body or
// inside its own shadow root.
const adpSdfPage = names + `
<label for="auth">` + sponsorQ + `</label><sdf-select-simple id="auth"></sdf-select-simple>
<label for="auth2">Are you a current or former employee of Example Corp?</label><sdf-select-simple id="auth2"></sdf-select-simple>
<script>
function wire(host,portal){
  const root=host.attachShadow({mode:'open'});
  root.innerHTML='<button class="trigger-button" type="button">Choose an option</button><div class="pane"></div>';
  const trigger=root.querySelector('.trigger-button');let pane=null;
  const close=()=>{if(!pane)return;if(portal)pane.remove();else pane.innerHTML='';pane=null};
  trigger.addEventListener('keydown',e=>{if(e.key==='Escape')close()});
  trigger.addEventListener('click',()=>{
    close();pane=portal?document.body.appendChild(document.createElement('div')):root.querySelector('.pane');
    pane.innerHTML=['Yes','No'].map(l=>'<sdf-select-item role="option">'+l+'</sdf-select-item>').join('');
    pane.querySelectorAll('sdf-select-item').forEach(item=>item.addEventListener('click',()=>{host.value=item.textContent;trigger.textContent=item.textContent;close()}));
  });
}
wire(document.getElementById('auth'),true);wire(document.getElementById('auth2'),false);
</script>`

func TestE2E_Adp_SdfSelectPortalledAndInShadowPane(t *testing.T) {
	runATS(t, atsCase{
		name: "adp-sdf", host: "recruiting.adp.com", path: "/srccar/public/RT", body: adpSdfPage,
		want: map[string]string{sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var shown []string
			e.MainEval(`['auth','auth2'].map(id=>document.getElementById(id).shadowRoot.querySelector('.trigger-button').textContent)`, &shown)
			if len(shown) != 2 || shown[0] != "No" {
				t.Errorf("the portalled select shows %v", shown)
			}
		},
	})
}

// An sdf-select-simple whose pane options take no click: the component keeps
// its own items, and only its change event, with the item as detail, shows the
// choice. The page sets its items from its own script, which the isolated
// world cannot read.
const adpSdfClicklessPage = names + `
<label for="auth">` + sponsorQ + `</label><sdf-select-simple id="auth"></sdf-select-simple>
<script>
window.__clicks=0;window.__changes=[];
const host=document.getElementById('auth');
host.items=[{value:'y',label:'Yes'},{value:'n',label:'No'}];
const root=host.attachShadow({mode:'open'});
root.innerHTML='<button class="trigger-button" type="button">Choose an option</button><div class="pane"></div>';
const trigger=root.querySelector('.trigger-button');
host.addEventListener('sdfChange',e=>{window.__changes.push(e.detail.value);trigger.textContent=e.detail.label});
trigger.addEventListener('click',()=>{
  const pane=root.querySelector('.pane');
  pane.innerHTML=host.items.map(i=>'<sdf-select-item role="option" value="'+i.value+'">'+i.label+'</sdf-select-item>').join('');
  pane.querySelectorAll('sdf-select-item').forEach(item=>item.addEventListener('click',()=>{window.__clicks++}));
});
</script>`

func TestE2E_Adp_SdfSelectClicklessOptionCommitsThroughComponent(t *testing.T) {
	runATS(t, atsCase{
		name: "adp-sdf-clickless", host: "recruiting.adp.com", path: "/srccar/public/RT", body: adpSdfClicklessPage, helper: true,
		want: map[string]string{sponsorQ: "No"},
		check: func(t *testing.T, e *engine.E2E, res *engine.PageResult) {
			var got struct {
				Shown   string
				Clicks  int
				Changes []string
			}
			e.MainEval(`({shown:document.getElementById('auth').shadowRoot.querySelector('.trigger-button').textContent,clicks:window.__clicks,changes:window.__changes})`, &got)
			if got.Shown != "No" || got.Clicks == 0 || len(got.Changes) != 1 || got.Changes[0] != "n" {
				t.Errorf("shown %q, option clicks %d, sdfChange values %v", got.Shown, got.Clicks, got.Changes)
			}
		},
	})
}
