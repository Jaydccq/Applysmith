//go:build browser

package apply

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// handoffForm is a page with every control the recorder reads, laid out at
// fixed positions so a trusted click lands on the intended control.
const handoffForm = `<title>Apply</title><style>
body{margin:0} .c{position:absolute;left:20px;width:300px;height:30px}
#city{top:20px} #pw{top:70px} #dob{top:120px} #auth{top:170px;margin:0;border:0;padding:0}
#grpq{top:260px} #grp{top:290px;left:20px;position:absolute} #notice{top:360px} #blank{top:400px;height:100px}
.r{position:absolute;left:360px;width:300px;height:30px}
</style>
<label for=city class=c style="top:0">City</label><input id=city class=c>
<label for=pw class=c style="top:50px">Account password</label><input id=pw type=password class=c>
<label for=dob class=c style="top:100px">Date of birth</label><input id=dob class=c>
<label for=trans class=r style="top:0">I identify as transgender:</label><input id=trans class=r style="top:20px">
<label for=hisp class=r style="top:50px">Are you Hispanic or Latino?</label><input id=hisp class=r style="top:70px">
<label for=ethn class=r style="top:100px">I identify my ethnicity as:</label><input id=ethn class=r style="top:120px">
<label for=disab class=r style="top:150px">I have a physical disability:</label><input id=disab class=r style="top:170px">
<fieldset id=auth class=c style="height:80px"><legend>Do you hold a work authorization for the US?</legend>
 <label><input type=radio name=auth value=yes id=ay> Yes</label> <label><input type=radio name=auth value=no id=an> No</label></fieldset>
<div id=grpq class=c>Do you require sponsorship?</div>
<div id=grp><button type=button id=by aria-pressed=false style="width:80px;height:30px">Yes</button><button type=button id=bn aria-pressed=true style="width:80px;height:30px">No</button></div>
<label for=notice class=c style="top:330px">Notice period</label><select id=notice class=c><option value="">Select...</option><option>Immediately</option><option>2 weeks</option><option>1 month</option></select>
<div id=blank class=c></div>
<script>
document.querySelectorAll('#grp button').forEach(function(b){b.addEventListener('click',function(){
 document.querySelectorAll('#grp button').forEach(function(o){o.setAttribute('aria-pressed',String(o===b))})})});
</script>`

func centre(t *testing.T, ctx context.Context, tab Tab, sel string) (x, y float64) {
	t.Helper()
	var r struct{ X, Y float64 }
	expr := fmt.Sprintf(`(function(){var b=document.querySelector(%q).getBoundingClientRect();return {X:b.left+b.width/2,Y:b.top+b.height/2}})()`, sel)
	if err := tab.Eval(ctx, expr, &r); err != nil {
		t.Fatal(err)
	}
	return r.X, r.Y
}

func clickOn(t *testing.T, ctx context.Context, tab Tab, sel string) {
	t.Helper()
	x, y := centre(t, ctx, tab, sel)
	if err := tab.Click(ctx, x, y); err != nil {
		t.Fatal(err)
	}
}

// testKey seals what a recorder armed through the lane, in the page's world, records.
var testKey = make([]byte, 32)

func readBuffer(t *testing.T, ctx context.Context, tab Tab) recorderBuffer {
	t.Helper()
	var b sealedBuffer
	if err := tab.Eval(ctx, recorderReadScript("", 0), &b); err != nil {
		t.Fatal(err)
	}
	return unseal(testKey, b)
}

// armObserver tells when the watch has armed the recorder, so the person's
// first edit is not raced.
type armObserver struct {
	TabObserver
	armed chan struct{}
}

func (o armObserver) Install(ctx context.Context, script string) error {
	err := o.TabObserver.Install(ctx, script)
	if err == nil && len(o.armed) == 0 {
		o.armed <- struct{}{}
	}
	return err
}

func watchArmed(t *testing.T, w *Watcher, spec WatchSpec) {
	t.Helper()
	armed, tab := make(chan struct{}, 1), spec.Tab
	spec.Tab = observeFunc(func(ctx context.Context) (TabObserver, error) {
		o, err := tab.Observe(ctx)
		return armObserver{o, armed}, err
	})
	if !w.Start(spec) {
		t.Fatal("the watch did not start")
	}
	select {
	case <-armed:
	case <-time.After(15 * time.Second):
		t.Fatal("the watch never armed the recorder")
	}
}

func byQuestion(b recorderBuffer, q string) (recorderEntry, bool) {
	for _, e := range b.Entries {
		if e.Question == q {
			return e, true
		}
	}
	return recorderEntry{}, false
}

// armedFormTab is the form after the chain filled it (synthetic writes) with
// the recorder installed, ready for the person to edit.
func armedFormTab(t *testing.T) (context.Context, *browser.LaneHandle, Tab) {
	ctx, lane, _, tab := htmlTab(t, handoffForm)
	chain := `(function(){
 var c=document.getElementById('city');c.value='Durham';c.dispatchEvent(new Event('input',{bubbles:true}));c.dispatchEvent(new Event('change',{bubbles:true}));
 var y=document.getElementById('ay');y.checked=true;y.dispatchEvent(new Event('change',{bubbles:true}));
 return 1})()`
	if err := tab.Eval(ctx, chain, nil); err != nil {
		t.Fatal(err)
	}
	var armed string
	if err := tab.Eval(ctx, recorderArmScript(testKey), &armed); err != nil || armed != "armed" {
		t.Fatalf("arming: %q %v", armed, err)
	}
	return ctx, lane, tab
}

func TestHandoff_Recorder_TrustedChangeAndPressedButtonOnly(t *testing.T) {
	ctx, _, tab := armedFormTab(t)

	// The chain's own writes after arming are synthetic and never count.
	synthetic := `(function(){var c=document.getElementById('city');c.value='Raleigh';c.dispatchEvent(new Event('change',{bubbles:true}));
 document.getElementById('by').click();document.getElementById('bn').click();return 1})()`
	if err := tab.Eval(ctx, synthetic, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if b := readBuffer(t, ctx, tab); len(b.Entries) != 0 {
		t.Fatalf("synthetic events were recorded: %+v", b.Entries)
	}

	// Chain write is not recorded: the person changes "Raleigh" by typing.
	clickOn(t, ctx, tab, "#city")
	if err := tab.Eval(ctx, `document.getElementById('city').select()`, nil); err != nil {
		t.Fatal(err)
	}
	if err := tab.TypeText(ctx, "Springfield"); err != nil {
		t.Fatal(err)
	}
	clickOn(t, ctx, tab, "#blank") // commit by leaving the box
	clickOn(t, ctx, tab, "#an")
	clickOn(t, ctx, tab, "#by")
	// The label puts the focus on its menu, and a typed letter picks in it.
	clickOn(t, ctx, tab, "label[for=notice]")
	if err := tab.TypeText(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	// The pressed state is read after the click settles; a loaded machine settles late.
	var b recorderBuffer
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if b = readBuffer(t, ctx, tab); len(b.Entries) >= 4 {
			break
		}
	}
	city, ok := byQuestion(b, "City")
	if !ok || city.Value != "Springfield" || city.Prior != "Raleigh" || city.Kind != "text" || city.Skipped {
		t.Fatalf("city %+v (all %+v)", city, b.Entries)
	}
	// A choice carries its options; a menu's placeholder is none, and the
	// legend that asks the question is not a section of its own.
	yesNo := []string{"Yes", "No"}
	auth, ok := byQuestion(b, "Do you hold a work authorization for the US?")
	if !ok || auth.Kind != "radio-group" || auth.OptionLabel != "No" || auth.Prior != "Yes" || !slices.Equal(auth.Options, yesNo) || len(auth.Section) != 0 {
		t.Fatalf("radio %+v", auth)
	}
	spons, ok := byQuestion(b, "Do you require sponsorship?")
	if !ok || spons.Kind != "radio-group" || spons.Value != "Yes" || spons.Prior != "No" || !slices.Equal(spons.Options, yesNo) {
		t.Fatalf("button group %+v", spons)
	}
	notice, ok := byQuestion(b, "Notice period")
	if !ok || notice.Kind != "select" || notice.Value != "2 weeks" || notice.OptionLabel != "2 weeks" || !slices.Equal(notice.Options, []string{"Immediately", "2 weeks", "1 month"}) {
		t.Fatalf("menu %+v", notice)
	}
	if len(b.Entries) != 4 {
		t.Fatalf("%d entries: %+v", len(b.Entries), b.Entries)
	}
	for _, e := range b.Entries {
		if e.URL == "" || e.Seq == 0 {
			t.Errorf("entry %+v", e)
		}
	}
	// A page that is read again after a reload-less navigation keeps its buffer
	// and a second install does not double the listeners.
	var again string
	if err := tab.Eval(ctx, recorderArmScript(testKey), &again); err != nil || again != "armed" {
		t.Fatalf("arming twice: %q %v", again, err)
	}
	if n := len(readBuffer(t, ctx, tab).Entries); n != 4 {
		t.Fatalf("%d entries after arming twice", n)
	}
}

func TestE2E_Handoff_Recorder_SensitiveControlsNeverLeaveThePage(t *testing.T) {
	ctx, _, tab := armedFormTab(t)
	// A password, a date of birth, and self-identification questions in the
	// words forms use, each whole.
	typed := []struct{ sel, value string }{
		{"#pw", "correct horse"}, {"#dob", "1990-01-02"},
		{"#trans", "answer one"}, {"#hisp", "answer two"}, {"#ethn", "answer three"}, {"#disab", "answer four"},
	}
	for _, c := range typed {
		clickOn(t, ctx, tab, c.sel) // and so leave the box before
		if err := tab.TypeText(ctx, c.value); err != nil {
			t.Fatal(err)
		}
	}
	clickOn(t, ctx, tab, "#blank")
	b := readBuffer(t, ctx, tab)
	if len(b.Entries) != len(typed) {
		t.Fatalf("%d entries: the edit of each sensitive control is noted", len(b.Entries))
	}
	raw, _ := json.Marshal(b)
	for _, c := range typed {
		if strings.Contains(string(raw), c.value) {
			t.Fatalf("the value typed into %s sits in the buffer: %s", c.sel, raw)
		}
	}
	for _, e := range b.Entries {
		if !e.Skipped || e.Value != "" || e.Prior != "" {
			t.Errorf("entry %+v", e)
		}
	}
}

// listboxForm holds custom listboxes as React-Select 5.8 draws them on a
// Greenhouse application: the markup and emotion classes (key remix-css) the
// board serves, the menu and options the library adds when the list opens,
// and the ARIA a Mac browser gets (no aria-selected; aria-activedescendant
// stays as served). The script does what the library does on a press of the
// control, a press in the menu, a click on an option, a typed search, the
// arrow keys, Enter, Tab and leaving the box. City is a box whose suggestions
// are a listbox and which keeps what is typed. Each widget sits in a section
// of another kind, all in one named for the job.
const listboxForm = `<title>Apply</title><style>
body{margin:0;font:14px sans-serif} h1,h2,p,legend{font-size:14px;margin:2px 0;padding:0}
section,fieldset,[role=group]{width:360px;margin:0;padding:0 8px;border:0} .field-wrapper{margin:4px 0}
#blank{position:absolute;left:440px;top:0;width:200px;height:500px}
.remix-css-b62m3t-container{position:relative;box-sizing:border-box}
.remix-css-7pg0cj-a11yText{z-index:9999;border:0;clip:rect(1px,1px,1px,1px);height:1px;width:1px;position:absolute;overflow:hidden;padding:0;white-space:nowrap}
.remix-css-13cymwt-control,.remix-css-t3ipsp-control{display:flex;flex-wrap:wrap;justify-content:space-between;align-items:center;min-height:38px;border:1px solid #ccc;border-radius:4px;box-sizing:border-box;background:#fff}
.remix-css-hlgwow{display:grid;flex:1;flex-wrap:wrap;align-items:center;position:relative;overflow:hidden;padding:2px 8px;box-sizing:border-box}
.remix-css-1jqq78o-placeholder,.remix-css-1dimb5e-singleValue{grid-area:1/1/2/3;margin-left:2px;margin-right:2px;box-sizing:border-box}
.remix-css-19bb58m{visibility:visible;flex:1 1 auto;display:inline-grid;grid-area:1/1/2/3;grid-template-columns:0 min-content;margin:2px;padding-bottom:2px;padding-top:2px;box-sizing:border-box}
.remix-css-1wy0on6{align-items:center;align-self:stretch;display:flex;flex-shrink:0;box-sizing:border-box}
.remix-css-1a0ro4n-requiredInput{opacity:0;pointer-events:none;position:absolute;bottom:0;left:0;right:0;width:100%}
.remix-css-1nmdiq5-menu{top:100%;position:absolute;width:100%;z-index:1;background:#fff;border-radius:4px;margin:8px 0;box-shadow:0 0 0 1px rgba(0,0,0,.1);box-sizing:border-box}
.remix-css-1n6sfyn-MenuList{max-height:300px;overflow-y:auto;position:relative;padding:4px 0;box-sizing:border-box}
.remix-css-10wo9uf-option,.remix-css-d7l1ni-option,.remix-css-tr4s17-option{display:block;width:100%;padding:8px 12px;box-sizing:border-box;user-select:none}
.remix-css-d7l1ni-option{background:#deebff} .remix-css-tr4s17-option{background:#2684ff;color:#fff}
.icon-button{border:0;background:none;padding:6px}
</style>
<section aria-labelledby=job><h1 id=job>Platform Engineer, Northwind Example</h1>
<section><h2>Personal information</h2>
<div class=field-wrapper><label for=city>City</label> <input id=city type=text role=combobox aria-autocomplete=list aria-expanded=false aria-controls=city-list autocomplete=off><ul id=city-list role=listbox hidden></ul></div>
</section>
<div role=group aria-labelledby=questions><p id=questions>Application questions</p>
<div class=field-wrapper><div class=select><div class=select__container><label id=question_101-label for=question_101 class="label select__label">How did you hear about this job?<span aria-hidden=true>*</span></label><div class=rs data-id=question_101 data-options="LinkedIn|Referral|Job board|Company website|Other"></div></div></div></div>
</div>
<fieldset><legend>Additional information</legend>
<div class=field-wrapper><div class=select><div class=select__container><label id=question_102-label for=question_102 class="label select__label">Preferred office<span aria-hidden=true>*</span></label><div class=rs data-id=question_102 data-options="Austin|Berlin|Lisbon" data-more=30></div></div></div></div>
</fieldset>
<div class=field-wrapper><div class=select><div class=select__container><label id=131-label for=131 class="label select__label">I identify my ethnicity as:<span aria-hidden=true>*</span></label><div class=rs data-id=131 data-options="Hispanic or Latino|White|Black or African American|Asian|Two or more races|I don't wish to answer"></div></div></div></div>
</section>
<div id=blank></div>
<script>
var longOffice='Remote, anywhere in the European Union'+', with quarterly travel to Lisbon'.repeat(6);
document.querySelectorAll('.rs').forEach(function(root){
 var id=root.dataset.id,opts=root.dataset.options.split('|'),pre='react-select-'+id;
 if(root.dataset.more){opts.push(longOffice);for(var n=opts.length+1;n<=+root.dataset.more;n++)opts.push('Office '+n)}
 var t=document.createElement('template');
 t.innerHTML='<div class="select-shell remix-css-b62m3t-container"><span id="'+pre+'-live-region" class="remix-css-7pg0cj-a11yText"></span>'+
  '<span aria-live="polite" aria-atomic="false" aria-relevant="additions text" role="log" class="remix-css-7pg0cj-a11yText"></span>'+
  '<div><div class="select__control remix-css-13cymwt-control"><div class="select__value-container remix-css-hlgwow">'+
  '<div class="select__placeholder remix-css-1jqq78o-placeholder" id="'+pre+'-placeholder">Select...</div>'+
  '<div class="select__input-container remix-css-19bb58m" data-value=""><input class="select__input" style="label:input;color:inherit;background:0;opacity:1;width:100%;grid-area:1 / 2;font:inherit;min-width:2px;border:0;margin:0;outline:0;padding:0" autocapitalize="none" autocomplete="off" autocorrect="off" id="'+id+'" spellcheck="false" tabindex="0" type="text" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-errormessage="'+id+'-error" aria-invalid="false" aria-labelledby="'+id+'-label" aria-required="true" role="combobox" aria-activedescendant="" aria-describedby="'+pre+'-placeholder" enterkeyhint="done" value=""></div></div>'+
  '<div class="select__indicators remix-css-1wy0on6"><button type="button" class="icon-button icon-button--sm" aria-label="Toggle flyout" tabindex="-1"><svg class="svg-icon" fill="none" height="20" width="20" viewBox="0 0 24 24"><path d="M6 9l6 6 6-6" stroke="currentColor"></path></svg></button></div></div></div>'+
  '<input required="" tabindex="-1" aria-hidden="true" class="remix-css-1a0ro4n-requiredInput" value=""></div>';
 var box=t.content.firstChild;root.replaceWith(box);
 var live=box.children[1],control=box.querySelector('.select__control'),vc=control.firstChild,input=box.querySelector('input[role=combobox]'),ic=input.parentNode,flyout=box.querySelector('.icon-button');
 var chosen=-1,focus=-1,shown=[],menu=null;
 function paint(){
  var f=document.activeElement===input,old=vc.querySelector('.select__placeholder,.select__single-value'),req=box.querySelector('.remix-css-1a0ro4n-requiredInput');
  control.className='select__control'+(f?' select__control--is-focused':'')+(menu?' select__control--menu-is-open':'')+(f?' remix-css-t3ipsp-control':' remix-css-13cymwt-control');
  vc.className='select__value-container'+(chosen>=0?' select__value-container--has-value':'')+' remix-css-hlgwow';
  if(old)old.remove();
  if(!input.value){var n=document.createElement('div');
   if(chosen<0){n.className='select__placeholder remix-css-1jqq78o-placeholder';n.id=pre+'-placeholder';n.textContent='Select...';input.setAttribute('aria-describedby',n.id)}
   else{n.className='select__single-value remix-css-1dimb5e-singleValue';n.textContent=opts[chosen];input.removeAttribute('aria-describedby')}
   vc.insertBefore(n,ic)}
  if(chosen>=0&&req)req.remove();
  input.setAttribute('aria-expanded',String(!!menu));
  if(menu)input.setAttribute('aria-controls',pre+'-listbox');else input.removeAttribute('aria-controls');
 }
 function draw(){
  var list=menu.firstChild;list.textContent='';
  shown.forEach(function(i){var o=document.createElement('div');
   o.setAttribute('aria-disabled','false');o.id=pre+'-option-'+i;o.tabIndex=-1;o.setAttribute('role','option');o.textContent=opts[i];
   o.addEventListener('mouseover',function(){if(focus!==i){focus=i;mark()}});o.addEventListener('click',function(){choose(i)});list.appendChild(o)});
  mark();
 }
 // A render that only moves the focus updates the options in place, as React does.
 function mark(){
  shown.forEach(function(i){document.getElementById(pre+'-option-'+i).className='select__option'+(i===focus?' select__option--is-focused':'')+(i===chosen?' select__option--is-selected':'')+(i===chosen?' remix-css-tr4s17-option':i===focus?' remix-css-d7l1ni-option':' remix-css-10wo9uf-option')});
  live.textContent=focus<0?'':'option '+opts[focus]+' focused, '+(shown.indexOf(focus)+1)+' of '+shown.length+'. '+shown.length+' results available. Use Up and Down to choose options, press Enter to select the currently focused option, press Escape to exit the menu, press Tab to select the option and exit the menu.';
 }
 function open(){
  shown=opts.map(function(_,i){return i}).filter(function(i){return opts[i].toLowerCase().indexOf(input.value.toLowerCase())>=0});
  focus=!input.value&&chosen>=0?chosen:shown.length?shown[0]:-1;
  if(!menu){menu=document.createElement('div');menu.className='select__menu remix-css-1nmdiq5-menu';
   menu.innerHTML='<div class="select__menu-list remix-css-1n6sfyn-MenuList" role="listbox" aria-multiselectable="false" id="'+pre+'-listbox"></div>';
   menu.addEventListener('mousedown',function(e){if(e.button!==0)return;e.stopPropagation();e.preventDefault();input.focus()});
   control.parentNode.after(menu)}
  draw();paint();
 }
 function close(){if(menu){menu.remove();menu=null}focus=-1;paint()}
 function choose(i){chosen=i;input.value='';ic.dataset.value='';close();live.textContent='option '+opts[i]+', selected.'}
 control.addEventListener('mousedown',function(e){if(e.button!==0||e.defaultPrevented)return;
  if(document.activeElement!==input){input.focus();open()}else if(!menu)open();else if(e.target!==input)close();
  if(e.target!==input)e.preventDefault()});
 flyout.addEventListener('mousedown',function(e){if(e.button!==0)return;input.focus();if(menu)close();else open();e.preventDefault()});
 input.addEventListener('input',function(){ic.dataset.value=input.value;open()});
 input.addEventListener('keydown',function(e){
  if(e.key==='ArrowDown'||e.key==='ArrowUp'){if(!menu)open();else if(shown.length){var k=shown.indexOf(focus)+(e.key==='ArrowDown'?1:shown.length-1);focus=shown[k%shown.length];mark()}}
  else if(e.key==='Enter'||(e.key==='Tab'&&!e.shiftKey)){if(!menu||focus<0)return;choose(focus)}
  else if(e.key==='Escape'){if(!menu)return;input.value='';ic.dataset.value='';close()}
  else return;
  e.preventDefault()});
 input.addEventListener('focus',paint);
 input.addEventListener('blur',function(){input.value='';ic.dataset.value='';live.textContent='';close()});
});
(function(){var i=document.getElementById('city'),l=document.getElementById('city-list'),all=['Springfield, IL','Springfield, MO','Spring Hill, TN'];
 function hide(){l.hidden=true;i.setAttribute('aria-expanded','false')}
 i.addEventListener('input',function(){var m=all.filter(function(c){return i.value&&c.toLowerCase().indexOf(i.value.toLowerCase())===0});
  l.innerHTML=m.map(function(c){return '<li role=option>'+c+'</li>'}).join('');l.hidden=!m.length;i.setAttribute('aria-expanded',String(!!m.length))});
 l.addEventListener('mousedown',function(e){e.preventDefault()});
 l.addEventListener('click',function(e){var o=e.target.closest('[role=option]');if(o){i.value=o.textContent;hide()}});
 i.addEventListener('blur',hide);
})();
</script>`

// A pick in a custom listbox changes no form control: the recorder reads what
// the widget shows after the person's own click on an option or Enter or Tab
// in its open list, and records the pick under the widget's question with the
// chosen label, the list's options and the section around it. The search
// typed into it, a key that picked nothing, the chain's own picks and the
// value of a self-identification widget are not recorded.
func TestE2E_Handoff_Recorder_APickInACustomListboxIsRecordedWithItsOptionsAndSection(t *testing.T) {
	ctx, lane, id, tab := htmlTab(t, listboxForm)
	type pt struct{ X, Y float64 }
	var at struct {
		Heard, Referral, Office, Lisbon, Ethnicity, City, Blank pt
		Height                                                  float64
	}
	// Where things are, each list open in turn, before anything is armed.
	measure := `(function(){function c(el){var r=el.getBoundingClientRect();return {X:r.left+r.width/2,Y:r.top+r.height/2}}
 function control(id){return c(document.getElementById(id).closest('.select__control'))}
 function option(id,n){var i=document.getElementById(id);i.closest('.select__control').dispatchEvent(new MouseEvent('mousedown',{bubbles:true,cancelable:true,button:0}));
  var o=c(document.getElementById('react-select-'+id+'-option-'+n));i.blur();return o}
 return {Heard:control('question_101'),Referral:option('question_101',1),Office:control('question_102'),Lisbon:option('question_102',2),
  Ethnicity:control('131'),City:c(document.getElementById('city')),Blank:c(document.getElementById('blank')),Height:innerHeight}})()`
	// A press below the viewport reaches nothing: every point must be in it.
	if err := tab.Eval(ctx, measure, &at); err != nil || at.Referral.Y <= at.Heard.Y || at.Lisbon.Y <= at.Office.Y || at.Lisbon.Y >= at.Height || at.Ethnicity.Y >= at.Height {
		t.Fatalf("layout %+v (%v)", at, err)
	}
	// The chain picks in the first widget before the recorder is armed, and in
	// the second, by a click and by keys, after it is: the person's pick there
	// then replaces what the widget showed when they came to it.
	chainPick := func(widget string, after string) {
		t.Helper()
		var shown string
		expr := fmt.Sprintf(`(function(){var i=document.getElementById(%q),c=i.closest('.select__control');
 c.dispatchEvent(new MouseEvent('mousedown',{bubbles:true,cancelable:true,button:0}));
 document.getElementById('react-select-'+i.id+'-option-0').click();
 var k=function(key){i.dispatchEvent(new KeyboardEvent('keydown',{key:key,bubbles:true,cancelable:true}))};%s
 i.blur();return c.querySelector('.select__single-value').textContent})()`, widget, after)
		if err := tab.Eval(ctx, expr, &shown); err != nil || shown == "" {
			t.Fatalf("the chain's pick in %s: %q %v", widget, shown, err)
		}
	}
	chainPick("question_101", "")
	var armed string
	if err := tab.Eval(ctx, recorderArmScript(testKey), &armed); err != nil || armed != "armed" {
		t.Fatalf("arming: %q %v", armed, err)
	}
	chainPick("question_102", "k('ArrowDown');k('ArrowDown');k('Enter');")
	time.Sleep(300 * time.Millisecond)
	if err := tab.Keep(); err != nil {
		t.Fatal(err)
	}

	// The person works through the app's browser panel.
	in := func(ev ...browser.InputEvent) {
		t.Helper()
		if err := panelControl().Input(ctx, id, ev); err != nil {
			t.Fatal(err)
		}
		time.Sleep(400 * time.Millisecond) // a person's pace, past the recorder's settle
	}
	click := func(p pt) []browser.InputEvent {
		return []browser.InputEvent{{Kind: "mouse", Type: "down", X: p.X, Y: p.Y, Button: "left", Buttons: 1, ClickCount: 1},
			{Kind: "mouse", Type: "up", X: p.X, Y: p.Y, Button: "left", ClickCount: 1}}
	}
	key := func(name string, code int) []browser.InputEvent {
		return []browser.InputEvent{{Kind: "key", Type: "down", Key: name, Code: name, KeyCode: code}, {Kind: "key", Type: "up", Key: name, Code: name, KeyCode: code}}
	}
	text := func(s string) browser.InputEvent { return browser.InputEvent{Kind: "text", Text: s} }
	down, enter, tabKey := key("ArrowDown", 40), key("Enter", 13), key("Tab", 9)

	in(click(at.Heard)...)
	in(text("Ref")) // a search, then the person leaves the box
	in(click(at.Blank)...)
	in(click(at.Heard)...)
	in(click(at.Referral)...) // a click on an option
	in(append(down, down...)...)
	in(enter...) // Enter on the next option
	in(click(at.Office)...)
	in(click(at.Lisbon)...)
	in(click(at.Ethnicity)...)
	in(append(down, tabKey...)...) // Tab on a self-identification option
	in(click(at.City)...)
	in(text("Spring"))
	in(enter...)           // Enter where no option is chosen
	in(click(at.Blank)...) // the city keeps what was typed

	if err := lane.Reclaim(id); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	var b recorderBuffer
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		var err error
		if raw, err = lane.Evaluate(ctx, id, recorderReadScript("", 0)); err != nil {
			t.Fatal(err)
		}
		var sealed sealedBuffer
		if err := json.Unmarshal(raw, &sealed); err != nil {
			t.Fatal(err)
		}
		if b = unseal(testKey, sealed); len(b.Entries) >= 5 {
			break
		}
	}
	type row struct {
		Question, Kind, InputType, Autocomplete, Value, OptionLabel, Prior string
		Skipped                                                            bool
		Options, Section                                                   string
	}
	var got []row
	for _, e := range b.Entries {
		got = append(got, row{e.Question, e.Kind, e.InputType, e.Autocomplete, e.Value, e.OptionLabel, e.Prior, e.Skipped, strings.Join(e.Options, "|"), strings.Join(e.Section, "|")})
	}
	const heard, offered = "How did you hear about this job?", "LinkedIn|Referral|Job board|Company website|Other"
	// The first 25 of the 30 offices, each at most 200 characters, as the
	// learning service takes them.
	offices := []string{"Austin", "Berlin", "Lisbon", ("Remote, anywhere in the European Union" + strings.Repeat(", with quarterly travel to Lisbon", 6))[:200]}
	for n := 5; n <= 25; n++ {
		offices = append(offices, fmt.Sprintf("Office %d", n))
	}
	want := []row{
		{heard, "custom-listbox", "text", "off", "Referral", "Referral", "LinkedIn", false, offered, "Application questions"},
		{heard, "custom-listbox", "text", "off", "Job board", "Job board", "Referral", false, offered, "Application questions"},
		{"Preferred office", "custom-listbox", "text", "off", "Lisbon", "Lisbon", "Berlin", false, strings.Join(offices, "|"), "Additional information"},
		{"I identify my ethnicity as", "custom-listbox", "text", "off", "", "", "", true, "", ""},
		{"City", "text", "text", "off", "Spring", "", "", false, "", "Personal information"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries\n got %+v\nwant %+v", got, want)
	}
	if strings.Contains(string(raw), "White") {
		t.Fatalf("a self-identification value sits in the buffer: %s", raw)
	}
}

// forgeForm's script replaces JSON.stringify, which a recorder in the page's
// world would call, to make City Shelbyville; once the recorder has written
// Team's entry, it changes that entry and appends one under its seal.
const forgeForm = `<input id=city aria-label=City value=Durham><input id=team aria-label=Team><div id=blank style="height:60px"></div><script>
var str=JSON.stringify;
JSON.stringify=function(v,r,i){if(v&&v.question==='City')v=Object.assign({},v,{value:'Shelbyville'});return str.call(JSON,v,r,i)};
document.addEventListener('change',function(e){
 if(!e.isTrusted||e.target.id!=='team')return;
 var s=JSON.parse(sessionStorage.getItem('__ajhr')),x=s.e[s.e.length-1];
 x.d=x.d.replace('Platform','Billing');
 s.e.push({n:++s.n,d:str({epoch:s.epoch,seq:s.n,question:'Desired salary',kind:'text',value:'999999',url:location.href}),m:x.m});
 sessionStorage.setItem('__ajhr',str(s))});
</script>`

// The whole path on a real tab: the tab is kept, the watch arms the recorder
// in its own world, the person edits through the app's panel, only the
// entries the recorder sealed are posted, once, and closing the tab ends the
// session. An edit in a same-origin frame, where the arming runs too, is one.
func TestE2E_Handoff_Watcher_PostsOnlyTheRecordersOwnEntriesAndEndsWhenTheTabCloses(t *testing.T) {
	_, st := sharedBrowser(t)
	frame := st.postingURL(st.add(&board{kind: "html", html: `<input aria-label=Department style="width:90%;height:60px">`, marker: fmt.Sprintf("h%d%07d", 6, boardSeq.Add(1))}))
	ctx, lane, id, tab := htmlTab(t, forgeForm+`<iframe src="`+frame+`" style="height:80px"></iframe>`)
	var ev []browser.InputEvent
	in := func(sel, text string) {
		x, y := centre(t, ctx, tab, sel)
		ev = append(ev, browser.InputEvent{Kind: "mouse", Type: "down", X: x, Y: y, Button: "left", Buttons: 1, ClickCount: 1},
			browser.InputEvent{Kind: "mouse", Type: "up", X: x, Y: y, Button: "left", ClickCount: 1}, browser.InputEvent{Kind: "text", Text: text})
	}
	in("#team", "Platform")
	in("iframe", "Research")
	in("#city", " NC")
	in("#blank", "")
	if err := tab.Keep(); err != nil {
		t.Fatal(err)
	}
	p := &posts{}
	w := &Watcher{Ctx: context.Background(), Poster: p, Every: 100 * time.Millisecond}
	ended := make(chan string, 1)
	watchArmed(t, w, WatchSpec{TabID: id, RunID: "run_20270310T090000.000_abc123", Report: 5, Tab: tab.(Observable), Ended: func(r string) { ended <- r }})
	if err := panelControl().Input(ctx, id, ev); err != nil {
		t.Fatal(err)
	}
	// City is the last entry: once it is posted, every entry was read.
	eventually(t, 15*time.Second, "the person's City", func() bool {
		return slices.ContainsFunc(p.events(), func(e LearnEvent) bool { return e.Question == "City" })
	})
	time.Sleep(400 * time.Millisecond)
	got := p.events()
	if len(got) != 2 || got[0].Question != "Department" || got[0].After != "Research" || got[0].FrameURL == "" ||
		got[1].After != "Durham NC" || got[1].Before != "Durham" || got[1].ReportNumber != 5 || got[1].Host == "" {
		t.Fatalf("posted %+v; want only the person's Department and City, once each, as typed", got)
	}
	// The lane does not evaluate on a kept tab.
	if _, err := lane.Evaluate(ctx, tab.ID(), "document.getElementById('city').value"); err == nil {
		t.Fatal("the lane evaluated on a kept tab")
	}
	if err := lane.CloseTab(ctx, tab.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-ended:
		if r != "tab-closed" {
			t.Fatalf("ended by %q", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session outlived its tab")
	}
	w.Wait()
}

// keptForm is a form the run fills from the profile. When the person changes
// City, the page's own script empties Phone, as a form empties a box that
// depends on another: the blank the restore pass puts back while a run owns
// the tab.
const keptForm = `<title>Apply</title><style>
body{margin:0} .c{position:absolute;left:20px;width:300px;height:30px}
#city{top:20px} #phone{top:70px} #blank{top:120px;height:100px}
</style>
<label for=city class=c style="top:0">City</label><input id=city class=c>
<label for=phone class=c style="top:50px">Phone</label><input id=phone type=tel class=c>
<div id=blank class=c></div>
<script>
document.getElementById('city').addEventListener('change',function(e){if(e.isTrusted)document.getElementById('phone').value=''});
</script>`

// A tab handed to the person runs none of the run's scripts: the restore pass,
// its tail guard and the engine's listeners end with the hand-over, so a box
// the page empties after the person's edit stays as the page left it, and the
// recorder still records what the person typed. A run hands a tab over in two
// ways: a stop keeps the run's tab, and a resumed run that ends lets go of the
// tab it took back.
func TestE2E_Handoff_AKeptTabRunsNoneOfTheRunsScriptsAndTheRecorderStillRecords(t *testing.T) {
	// page reads the form and whether the page calls its own fetch or the
	// main-world helper's.
	const page = `({City:document.getElementById('city').value,Phone:document.getElementById('phone').value,OwnFetch:String(window.fetch).includes('[native code]')})`
	type held struct {
		City, Phone string
		OwnFetch    bool
	}
	// fill fills the form as the apply stack does, helper included.
	fill := func(t *testing.T, ctx context.Context, tab Tab) {
		t.Helper()
		opts := engine.FillOptions{Applicant: func(context.Context) (json.RawMessage, error) { return json.RawMessage(applicantJSON), nil }, MainWorld: true, Timing: fastEngine()}
		if _, err := tab.Page().Fill(ctx, opts); err != nil {
			t.Fatal(err)
		}
		var got held
		if err := tab.Eval(ctx, page, &got); err != nil || got.Phone == "" || got.OwnFetch {
			t.Fatalf("after the fill %+v (%v), want Phone filled and the helper in the page", got, err)
		}
	}
	for _, c := range []struct {
		name     string
		handOver func(t *testing.T, ctx context.Context, lane *browser.LaneHandle, tab Tab)
	}{
		{"a stop keeps the run's tab", func(t *testing.T, ctx context.Context, _ *browser.LaneHandle, tab Tab) {
			fill(t, ctx, tab)
			if err := tab.Keep(); err != nil {
				t.Fatal(err)
			}
		}},
		{"a resumed run lets go of the tab it took back", func(t *testing.T, ctx context.Context, lane *browser.LaneHandle, tab Tab) {
			if err := tab.Keep(); err != nil {
				t.Fatal(err)
			}
			back, err := LaneWindow{Lane: lane}.Reclaim(ctx, tab.ID(), engine.Host{})
			if err != nil {
				t.Fatal(err)
			}
			fill(t, ctx, back)
			if err := back.Close(ctx); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, lane, id, tab := htmlTab(t, keptForm)
			cx, cy := centre(t, ctx, tab, "#city")
			bx, by := centre(t, ctx, tab, "#blank")
			c.handOver(t, ctx, lane, tab)
			// The test reads the kept tab, not the run: the lane attaches for
			// one read and lets go again.
			peek := func(expr string, out any) {
				t.Helper()
				if err := lane.Reclaim(id); err != nil {
					t.Fatal(err)
				}
				raw, err := lane.Evaluate(ctx, id, expr)
				if kerr := lane.Keep(id); kerr != nil {
					t.Fatal(kerr)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, out); err != nil {
					t.Fatal(err)
				}
			}
			p := &posts{}
			w := &Watcher{Ctx: context.Background(), Poster: p, Every: 100 * time.Millisecond}
			t.Cleanup(func() { w.Stop(id); w.Wait() })
			watchArmed(t, w, WatchSpec{TabID: id, RunID: "run_20270310T090000.000_abc123", Tab: tab.(Observable)})

			// The person types a new City through the app's browser panel and
			// leaves the box; the page empties Phone as they do.
			click := func(x, y float64) []browser.InputEvent {
				return []browser.InputEvent{{Kind: "mouse", Type: "down", X: x, Y: y, Button: "left", Buttons: 1, ClickCount: 1},
					{Kind: "mouse", Type: "up", X: x, Y: y, Button: "left", ClickCount: 1}}
			}
			ev := append(click(cx, cy), browser.InputEvent{Kind: "key", Type: "down", Key: "End", Code: "End", KeyCode: 35})
			for range 20 {
				ev = append(ev, browser.InputEvent{Kind: "key", Type: "down", Key: "Backspace", Code: "Backspace", KeyCode: 8},
					browser.InputEvent{Kind: "key", Type: "up", Key: "Backspace", Code: "Backspace", KeyCode: 8})
			}
			ev = append(append(ev, browser.InputEvent{Kind: "text", Text: "Raleigh"}), click(bx, by)...)
			if err := panelControl().Input(ctx, id, ev); err != nil {
				t.Fatal(err)
			}
			eventually(t, 15*time.Second, "the person's City in the manual-fill corpus", func() bool {
				for _, e := range p.events() {
					if e.Question == "City" && e.After == "Raleigh" {
						return true
					}
				}
				return false
			})
			// Five looks of the restore pass later, nothing of the run has put
			// Phone back, and the page calls its own fetch again.
			time.Sleep(2 * time.Second)
			var got held
			peek(page, &got)
			if got != (held{City: "Raleigh", OwnFetch: true}) {
				t.Fatalf("the kept tab holds %+v; the person wrote Raleigh, the page emptied Phone, and no helper of the run stays", got)
			}
		})
	}
}

// uploadForm is an application whose résumé box takes only a file the browser
// itself sets: one a script assigns is discarded, so the run stages the
// document on disk and the browser reads it from there when the form is sent.
const uploadForm = `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` + fieldNames +
	`<label for="resume">Resume/CV *</label><input id="resume" type="file" name="resume" required></form>
<script>document.getElementById('resume').addEventListener('change',function(e){if(!e.isTrusted)e.target.value=''})</script>`

// oneResume serves the same résumé for every request.
type oneResume engine.ResumePayload

func (r oneResume) Resume(context.Context, engine.ResumeRequest) (*engine.ResumePayload, error) {
	p := engine.ResumePayload(r)
	return &p, nil
}

func stagedFiles(t *testing.T, root string) (files []string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func TestE2E_Handoff_AStagedResumeStaysWhileTheTabIsTheUsersAndGoesWithTheTab(t *testing.T) {
	pdf, err := os.ReadFile(resumePath)
	if err != nil {
		t.Fatal(err)
	}
	doc := oneResume{FileName: "resume-alex-example.pdf", MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(pdf)}
	for _, c := range []struct {
		name string
		take func(ctx context.Context, w LaneWindow, id string, h engine.Host) (Tab, error)
	}{
		{"a resumed run hands back the tab it took", func(ctx context.Context, w LaneWindow, id string, h engine.Host) (Tab, error) {
			return w.Reclaim(ctx, id, h)
		}},
		{"a fill of the user's own tab lets go of it", func(ctx context.Context, w LaneWindow, id string, h engine.Host) (Tab, error) {
			// The tab is the user's with no run attached, as one they opened is.
			if err := w.Lane.Reclaim(id); err != nil {
				return nil, err
			}
			return w.Adopt(ctx, id, h)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, lane, id, opened := htmlTab(t, uploadForm)
			if err := opened.Keep(); err != nil {
				t.Fatal(err)
			}
			stage := t.TempDir()
			tab, err := c.take(ctx, LaneWindow{Lane: lane}, id, engine.Host{Resumes: doc, StageDir: stage})
			if err != nil {
				t.Fatal(err)
			}
			res, err := tab.Page().Fill(ctx, engine.FillOptions{Applicant: func(context.Context) (json.RawMessage, error) { return json.RawMessage(applicantJSON), nil }, Timing: fastEngine()})
			if err != nil || len(res.NativeUploads) != 1 {
				t.Fatalf("the fill staged %v (%v), want the résumé", res, err)
			}
			if err := tab.Close(ctx); err != nil {
				t.Fatal(err)
			}
			// The tab stays with the user, whose own Submit reads the résumé from disk.
			if got := stagedFiles(t, stage); len(got) != 1 || filepath.Base(got[0]) != "resume-alex-example.pdf" {
				t.Fatalf("staged after the run let go of the tab: %v", got)
			}
			if err := lane.CloseTab(ctx, id); err != nil {
				t.Fatal(err)
			}
			eventually(t, 15*time.Second, "the staged résumé to go with the tab", func() bool { return len(stagedFiles(t, stage)) == 0 })
		})
	}
}

// Placer's Greenhouse frame carries a 344-character validityToken before the
// token that names the job: the probe reads a frame's address whole, so the
// embedded application is recognised.
func TestE2E_Probe_AFramesLongAddressIsReadWhole(t *testing.T) {
	src := "about:blank#for=example-corp&validityToken=" + strings.Repeat("x", 344) + "&token=1234567"
	ctx, _, _, tab := htmlTab(t, `<title>Careers</title><iframe src="`+src+`" style="width:600px;height:400px"></iframe>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Frames) != 1 || !strings.HasSuffix(st.Frames[0].Src, "&token=1234567") {
		t.Fatalf("frames %+v", st.Frames)
	}
}
