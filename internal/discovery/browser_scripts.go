// Clean-room: docs/provenance.md#CR-EXTRACT

package discovery

import (
	"encoding/json"
	"fmt"
)

// The page scripts. Each is one expression that returns JSON and starts with
// a marker comment, so a test double can tell them apart. They read the page
// and never change it, apart from marking the one control a later click will
// press. Nothing here types, submits or answers anything.

// jsPrelude is the helpers every script shares. A selector argument is a list
// of alternatives separated by "|", tried in order.
const jsPrelude = `const T=e=>((e&&(e.innerText||e.textContent))||'').replace(/\s+/g,' ').trim();
const Q=(r,s)=>{for(const x of s.split('|')){if(!x)continue;const e=r.querySelector(x);if(e)return e}return null};
const QT=(r,s)=>{const e=Q(r,s);return e?T(e):''};
const QA=(r,s,a)=>{const e=Q(r,s);return e?(e.getAttribute(a)||''):''};
const ABS=h=>{try{return new URL(h,location.href).href}catch(e){return ''}};
const OUTER=els=>els.filter(e=>!els.some(o=>o!==e&&o.contains(e)));
const VIS=e=>{const r=e.getBoundingClientRect();return r.width>0&&r.height>0};
`

// script wraps a body in the expression form with its marker.
func script(name, body string) string {
	return "(()=>{/*aj:" + name + "*/" + jsPrelude + body + "})()"
}

// probeJS reads what a page with no job cards says about itself.
var probeJS = script("probe", `const t=((document.body&&document.body.innerText)||'').replace(/\s+/g,' ').trim();
return {url:location.href,title:document.title||'',text:t.slice(0,4000),password:!!document.querySelector('input[type=password]'),dialog:!!document.querySelector('[role=dialog],[aria-modal=true]')};`)

// scrollJS scrolls the page by dy pixels; the result says how far it got.
func scrollJS(dy int) string {
	return fmt.Sprintf("(()=>{/*aj:scroll*/window.scrollBy(0,%d);return {y:Math.round(window.scrollY),h:Math.round(window.innerHeight),total:document.documentElement.scrollHeight}})()", dy)
}

// scrollEndJS brings the last job card into view so a virtualised list renders
// the rest of its page.
func scrollEndJS(cardSel string) string {
	return script("scroll-end", `const c=document.querySelectorAll(`+jsString(cardSel)+`);if(c.length)c[c.length-1].scrollIntoView({block:'end'});return c.length;`)
}

// The kinds of click a scan makes besides the card, pagination and show-more
// controls of a board. Everything else is refused.
const (
	clickOverlay   = "overlay-close"
	clickApplyFlow = "apply-follow"
	clickComments  = "comments"
)

// clickJS presses one control, and refuses a control whose own text, label or
// class names a write action. The refusal is the last line of defense after
// the selectors, which already name only the allowed controls.
func clickJS(kind, sel string) string {
	return script("click", `const e=document.querySelector(`+jsString(sel)+`);if(!e)return {clicked:false,reason:'missing'};
const t=(T(e)+' '+(e.getAttribute('aria-label')||'')+' '+(e.className&&e.className.toString?e.className.toString():'')).toLowerCase();
if(/easy apply|easily apply|\bsave\b|dismiss|follow|connect|message|\blike\b|withdraw|submit|\bsend\b|repost/.test(t))return {clicked:false,reason:'write control'};
if(`+jsString(kind)+`!=='comments'&&/^comment$/.test(T(e).toLowerCase()))return {clicked:false,reason:'write control'};
e.scrollIntoView({block:'center'});e.click();return {clicked:true};`)
}

type clickResult struct {
	Clicked bool   `json:"clicked"`
	Reason  string `json:"reason"`
}

// detailSel names where one board keeps the parts of a posting page.
type detailSel struct {
	Title, Company, Location, Posted, Applicants, Desc string
	// Next also returns the page's embedded __NEXT_DATA__ document.
	Next bool
}

// detailJS reads a posting page: the description (a selector, else the
// structured data), the header facts and the apply control. The one control
// that is not a quick apply is marked, so a click can find it again. Only a label
// that starts with the quick-apply words counts: a whole similar-job card that
// is one link ends its text with "Easy Apply".
func detailJS(s detailSel) string {
	nextExpr := "null"
	if s.Next {
		nextExpr = "(()=>{const n=document.getElementById('__NEXT_DATA__');try{return n?JSON.parse(n.textContent):null}catch(e){return null}})()"
	}
	return script("detail", `const d=Q(document,`+jsString(s.Desc)+`);let desc=d?T(d):'';
let dp='';for(const s of document.querySelectorAll('script[type="application/ld+json"]')){try{const j=JSON.parse(s.textContent);const l=Array.isArray(j)?j:(j['@graph']||[j]);for(const o of l){if(o&&o['@type']==='JobPosting'){if(!dp&&o.datePosted)dp=String(o.datePosted);if(desc.length<200&&o.description){const h=document.createElement('div');h.innerHTML=o.description;const x=T(h);if(x.length>desc.length)desc=x}}}}catch(e){}}
let easy=false;const applies=[];
for(const e of [...document.querySelectorAll('a,button')].filter(VIS)){const t=T(e)||e.getAttribute('aria-label')||'';
if(/^(easy apply|easily apply|quick apply|apply with (linkedin|indeed))/i.test(t)){easy=true;continue}
if(/^(apply|apply now|apply (directly )?(on|at) [^|]{1,40}|apply externally)$/i.test(t)){e.setAttribute('data-aj-apply',String(applies.length));const h=e.tagName==='A'?(e.getAttribute('href')||''):(e.getAttribute('data-href')||'');applies.push({href:h?ABS(h):'',text:t})}}
const apply=applies[0]||null;
return {url:location.href,title:QT(document,`+jsString(s.Title)+`),company:QT(document,`+jsString(s.Company)+`),location:QT(document,`+jsString(s.Location)+`),posted:QT(document,`+jsString(s.Posted)+`),datePosted:dp,applicants:QT(document,`+jsString(s.Applicants)+`),desc:desc.slice(0,20000),easy,apply,applies,head:T(document.body).slice(0,500),next:`+nextExpr+`};`)
}

type applyControl struct {
	Href string `json:"href"`
	Text string `json:"text"`
}

type detailRead struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Company  string `json:"company"`
	Location string `json:"location"`
	Posted   string `json:"posted"`
	// DatePosted is the posting's structured data date, as the page wrote it.
	DatePosted string        `json:"datePosted"`
	Applicants string        `json:"applicants"`
	Desc       string        `json:"desc"`
	Easy       bool          `json:"easy"`
	Apply      *applyControl `json:"apply"`
	// Applies are every apply control in page order, Apply the first; the
	// control at index i carries data-aj-apply="i".
	Applies []applyControl  `json:"applies"`
	Head    string          `json:"head"`
	Next    json.RawMessage `json:"next"`
}

// pageTextJS reads the visible text of a throwaway page, capped.
var pageTextJS = script("page-text", `return {url:location.href,text:T(document.body).slice(0,20000),title:document.title||''};`)

type pageText struct {
	URL   string `json:"url"`
	Text  string `json:"text"`
	Title string `json:"title"`
}
