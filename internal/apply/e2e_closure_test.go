//go:build browser

package apply

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
)

const adpPagerPage = `<title>Apply</title><style>
.appGo{width:64px;height:64px;background:#ccc;margin:8px}
.srccarPager{position:relative}
</style>
<ol id="stepper">
 <li aria-current="page">Job</li><li>Details</li><li id="last" aria-current="true">Review and Submit</li>
</ol>
<input name="firstName" aria-label="First name"><input name="lastName" aria-label="Last name">
<div class="srccarPager">
 <div class="appGo" data-dojo-attach-point="thePagerDualNext" id="dual"></div>
 <div class="appGo" data-dojo-attach-point="thePagerSingleGo" id="single" style="display:none"></div>
 <div class="appGo" data-dojo-attach-point="thePagerOther" id="other"></div>
</div>
<div class="appGo" data-dojo-attach-point="thePagerDualNext" id="outside"></div>
<script>
window.__g=[];
for (const id of ['dual','single','other','outside']) {
  const el=document.getElementById(id);
  for (const t of ['mousedown','mouseup','click']) el.addEventListener(t,e=>window.__g.push(id+':'+t+':'+e.isTrusted));
}
</script>`

func TestE2E_ApplyChain_AdpPagerAndFinalStepMarker(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, adpPagerPage)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	// Only the shown advance control at an attach point inside the pager is a control.
	var pager []Control
	for _, c := range st.Controls {
		if c.Tag == "div" {
			pager = append(pager, c)
		}
	}
	if len(pager) != 1 || len(pager[0].Labels) != 1 || pager[0].Labels[0] != "Next" {
		t.Fatalf("controls %+v: one pager control, named Next, and no other appGo", st.Controls)
	}
	cats := Categorize(st, true)
	if len(cats.Nav) != 1 {
		t.Fatalf("nav %+v", cats.Nav)
	}
	loc, err := Locate(ctx, tab, pager[0].I)
	if err != nil || !loc.Found || !loc.Hit || len(loc.Labels) != 1 || loc.Labels[0] != "Next" {
		t.Fatalf("located %+v err %v", loc, err)
	}
	if err := tab.Click(ctx, loc.X, loc.Y); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := tab.Eval(ctx, `window.__g`, &got); err != nil {
		t.Fatal(err)
	}
	want := "dual:mousedown:true,dual:mouseup:true,dual:click:true"
	if strings.Join(got, ",") != want {
		t.Fatalf("gesture %v, want %s: Dojo ignores a bare click", got, want)
	}
	// The marker of the last step counts; aria-current page does not.
	if st.Step == nil || st.Step.Current != 3 || st.Step.Total != 3 || !st.Step.Last() {
		t.Fatalf("step %+v", st.Step)
	}
}

func TestE2E_ApplyChain_AdpStepMarkerHiddenByLayoutOrAriaHidden(t *testing.T) {
	read := func(markup string) *StepInfo {
		ctx, _, _, tab := htmlTab(t, "<title>Apply</title>"+markup)
		st, err := Probe(ctx, tab)
		if err != nil {
			t.Fatal(err)
		}
		return st.Step
	}
	// Hidden only by layout (a narrow-screen rule): it still counts when no shown marker exists.
	if s := read(`<ol><li>One</li><li style="display:none" aria-current="step">Two</li></ol>`); s == nil || !s.Last() {
		t.Fatalf("layout-hidden marker %+v", s)
	}
	// An aria-hidden marker never counts.
	if s := read(`<ol aria-hidden="true"><li>One</li><li aria-current="step">Two</li></ol>`); s != nil && s.Last() {
		t.Fatalf("aria-hidden marker %+v", s)
	}
	// A shown marker wins over a hidden one.
	if s := read(`<ol><li aria-current="true">One</li><li>Two</li></ol><ol><li>A</li><li style="display:none" aria-current="step">B</li></ol>`); s == nil || s.Last() {
		t.Fatalf("shown marker %+v", s)
	}
}

func TestApply_Walk_IgnoresHiddenStackedContinue(t *testing.T) {
	var panels []string
	for i := 0; i < 6; i++ {
		style := "display:none"
		if i == 4 {
			style = ""
		}
		panels = append(panels, `<div style="`+style+`"><button type="button">Continue</button></div>`)
	}
	ctx, _, _, tab := htmlTab(t, `<title>Apply</title><form><input name="q" aria-label="Question">`+strings.Join(panels, "")+`<button type="button" style="display:none">Submit application</button></form>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	cats := Categorize(st, false)
	if len(cats.Nav) != 1 || len(cats.Submit) != 0 {
		t.Fatalf("nav %d submit %d: only the rendered Continue is a control", len(cats.Nav), len(cats.Submit))
	}
}

func TestApply_RoleGuard_HiddenH1Ignored(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Careers</title><h1 style="display:none">Application Submitted</h1><h1 aria-hidden="true" style="height:0;overflow:hidden">Thank you</h1><h1>Platform Engineer</h1><form><input name="a"><input name="b"></form>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if role := PageRole(st.Title, st.Headings, ""); role != "Platform Engineer" {
		t.Fatalf("role %q from headings %v", role, st.Headings)
	}
}

const recorderPage = `<title>Apply</title><style>input{display:block;width:200px;height:20px;margin:2px}</style>
<div id="box"></div>
<script>
const box=document.getElementById('box');
for(let i=0;i<110;i++){const l=document.createElement('label');l.setAttribute('for','f'+i);l.textContent='Question '+i;const n=document.createElement('input');n.id='f'+i;box.append(l,n)}
</script>`

func typeIntoField(t *testing.T, tab Tab, ctx context.Context, id, text string) {
	t.Helper()
	if err := tab.Eval(ctx, `(function(){var e=document.getElementById(`+strconv.Quote(id)+`);e.focus();return 1})()`, nil); err != nil {
		t.Fatal(err)
	}
	if err := tab.TypeText(ctx, text); err != nil {
		t.Fatal(err)
	}
	if err := tab.Eval(ctx, `document.getElementById(`+strconv.Quote(id)+`).blur()`, nil); err != nil {
		t.Fatal(err)
	}
}

func TestE2E_Handoff_CursorReadSurvivesNavigationAndReturnsNothingTwice(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, recorderPage)
	var armed string
	if err := tab.Eval(ctx, recorderArmScript(testKey), &armed); err != nil || armed != "armed" {
		t.Fatalf("arm %q %v", armed, err)
	}
	for i := 0; i < 3; i++ {
		typeIntoField(t, tab, ctx, "f"+strconv.Itoa(i), "answer")
	}
	first := readBuffer(t, ctx, tab)
	if len(first.Entries) != 3 {
		t.Fatalf("%d entries", len(first.Entries))
	}
	var again sealedBuffer
	if err := tab.Eval(ctx, recorderReadScript(first.Epoch, first.Next), &again); err != nil || len(again.Entries) != 0 {
		t.Fatalf("a second read from the cursor returned %+v (%v)", again.Entries, err)
	}
	// A same-origin navigation drops the script and keeps the buffer; arming
	// again is idempotent and the cursor carries on.
	var cur string
	if err := tab.Eval(ctx, `location.href`, &cur); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.Navigate(ctx, cur+"?second=1#frag"); err != nil {
		t.Fatal(err)
	}
	after := readBuffer(t, ctx, tab)
	if after.Armed || len(after.Entries) != 3 {
		t.Fatalf("after the navigation: armed=%v entries=%d", after.Armed, len(after.Entries))
	}
	if err := tab.Eval(ctx, recorderArmScript(testKey), &armed); err != nil || armed != "armed" {
		t.Fatalf("re-arm %q %v", armed, err)
	}
	typeIntoField(t, tab, ctx, "f3", "late")
	var sealed sealedBuffer
	err := tab.Eval(ctx, recorderReadScript(first.Epoch, first.Next), &sealed)
	rest := unseal(testKey, sealed)
	if err != nil || len(rest.Entries) != 1 || rest.Entries[0].Question != "Question 3" {
		t.Fatalf("read from the cursor %+v (%v)", rest.Entries, err)
	}
	if strings.ContainsAny(rest.Entries[0].URL, "?#") {
		t.Fatalf("entry URL %q carries a query or fragment", rest.Entries[0].URL)
	}
}

func TestE2E_Handoff_RecorderOfAnotherSessionIsReplacedNotDoubled(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, recorderPage)
	// The tab outlived the session that armed it, whose recorder is still there.
	var armed string
	if err := tab.Eval(ctx, recorderArmScript(bytes.Repeat([]byte{9}, 32)), &armed); err != nil || armed != "armed" {
		t.Fatalf("arm %q %v", armed, err)
	}
	if err := tab.Eval(ctx, recorderArmScript(testKey), &armed); err != nil || armed != "armed" || !readBuffer(t, ctx, tab).Armed {
		t.Fatalf("re-arm %q %v", armed, err)
	}
	typeIntoField(t, tab, ctx, "f0", "once")
	var sealed sealedBuffer
	if err := tab.Eval(ctx, recorderReadScript("", 0), &sealed); err != nil {
		t.Fatal(err)
	}
	if n, mine := len(sealed.Entries), len(unseal(testKey, sealed).Entries); n != 1 || mine != 1 {
		t.Fatalf("%d entries for one edit, %d under this session's key: the replaced recorder still listens", n, mine)
	}
}
