package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/workspace"
)

// livenessReadJS reads the loaded document without touching it. Navigation,
// footer and related-jobs blocks, cookie or consent notices and other
// postings' cards (a short list item or article that links to a posting with
// none of this page's ids) are left out of the text, the banner and the
// search for an apply control. A page whose content sits in a same-origin
// frame (iCIMS) is read in that frame. The description is the job board's
// description block (desc, alternatives divided by "|"), else the description
// of the page's JobPosting data. shell says the page is a Workday app shell
// without its posting data or job header: the text of such a page is the
// site's chrome, and its title a tagline. A document with no body yet (a
// challenge page reloading) has no text.
func livenessReadJS(desc string) string {
	sel, _ := json.Marshal(desc)
	return `(()=>{const SEL=` + string(sel) + `;
const SKIP='nav,footer,aside,[role=navigation],[role=contentinfo],[class*=related i],[class*=similar i],[id*=related i],[id*=similar i],[class*=recommend i]';
const NOTICE='[id*=cookie i],[class*=cookie i],[id*=consent i],[class*=consent i],[aria-label*=cookie i],[aria-label*=consent i],[id*=onetrust i],[class*=onetrust i],[id*=gdpr i],[class*=gdpr i]';
const CARD='li,article,[data-job-id],[data-jobid],[data-occludable-job-id]';
const JOB=/\/(jobs?|job-listing|careers?|positions?|openings?|requisitions?|vacanc(y|ies))(\/|$)|viewjob|[?&](jk|jl|gh_jid|jobid|job_id|currentjobid)=/i;
const ne=performance.getEntriesByType('navigation')[0];const status=ne&&ne.responseStatus?ne.responseStatus:0;
const ids=h=>{try{const u=new URL(h,location.href),o=[];for(const s of u.pathname.toLowerCase().split('/')){if(/^(\d{3,}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[a-z]*\d{4,}[a-z0-9]*)$/.test(s))o.push(s);const m=s.match(/[-_]([a-z]*\d{4,}[a-z0-9]*|\d{3,})$/);if(m)o.push(m[1])}for(const [k,v] of u.searchParams)if(/^(jk|jl|gh_jid|id|jobid|job_id|currentjobid|jid)$/i.test(k))o.push(v.toLowerCase());return o}catch(e){return []}};
const own=ids(location.href);
const other=c=>(c.textContent||'').length<=600&&[...c.querySelectorAll('a[href]')].some(a=>{const i=ids(a.href);return JOB.test(a.href)&&i.length>0&&!i.some(x=>own.includes(x))});
const card=e=>{const c=e.closest(CARD);return !!c&&other(c)};
const skip=e=>!!e.closest(SKIP)||!!e.closest(NOTICE)||card(e);
const vis=e=>{const r=e.getBoundingClientRect();const s=(e.ownerDocument.defaultView||window).getComputedStyle(e);return r.width>0&&r.height>0&&s.visibility!=='hidden'&&s.display!=='none'};
const textOf=d=>{const root=d.querySelector('main,[role=main]')||d.body;if(!root)return '';const c=root.cloneNode(true);
c.querySelectorAll(SKIP+',script,style,noscript').forEach(e=>e.remove());c.querySelectorAll(NOTICE).forEach(e=>{if(!e.querySelector('h1'))e.remove()});c.querySelectorAll(CARD).forEach(e=>{if(other(e))e.remove()});
return (c.textContent||'').replace(/\s+/g,' ').trim().slice(0,30000)};
let d=document,text=textOf(document);
if(text.length<200)for(const f of document.querySelectorAll('iframe')){let fd=null;try{fd=f.contentDocument}catch(e){}if(!fd||!fd.body)continue;const t=textOf(fd);if(t.length>text.length){d=fd;text=t}}
const APPLY=/^\s*(apply|apply now|apply for this (job|role|position)|apply to this (job|role|position)|submit application|easy apply)\b/i;
const controls=[...d.querySelectorAll('a,button,input[type=submit]')].filter(e=>!skip(e)&&vis(e)&&!e.disabled&&e.getAttribute('aria-disabled')!=='true'&&APPLY.test((e.innerText||e.value||'').trim()));
const applyLinks=[...new Set(controls.filter(e=>!/^\s*(easy apply|submit application)/i.test(e.innerText||e.value||'')).map(e=>{const h=e.tagName==='A'?e.getAttribute('href'):e.getAttribute('data-href');try{return h?new URL(h,d.baseURI).href:''}catch(x){return ''}}).filter(h=>/^https?:/i.test(h)))].slice(0,5);
const pw=[...d.querySelectorAll('input[type=password]')].some(vis)&&text.length<600;
const banner=[...d.querySelectorAll('[role=alert],[class*=banner i],[class*=alert i],[class*=notice i]')].filter(e=>!skip(e)&&vis(e)).map(e=>(e.innerText||'').trim()).join(' ').slice(0,500);
const h1=d.querySelector('h1');
const docs=d===document?[document]:[document,d];
const ldPosting=o=>{if(!o||typeof o!=='object')return null;if(Array.isArray(o)){for(const x of o){const r=ldPosting(x);if(r)return r}return null}const ty=o['@type'];if(ty==='JobPosting'||(Array.isArray(ty)&&ty.includes('JobPosting')))return o;return o['@graph']?ldPosting(o['@graph']):null};
let lp=null;for(const x of docs)for(const s of x.querySelectorAll('script[type="application/ld+json"]')){if(!lp)try{lp=ldPosting(JSON.parse(s.textContent||''))}catch(e){}}
const plain=h=>{const x=new DOMParser().parseFromString(String(h),'text/html');x.querySelectorAll('br').forEach(b=>b.replaceWith('\n'));x.querySelectorAll('p,li,div,h1,h2,h3,h4,h5,h6,tr,ul,ol,section').forEach(b=>b.append('\n'));return ((x.body&&x.body.textContent)||'').replace(/[ \t ]+/g,' ').replace(/ ?\n ?/g,'\n').replace(/\n{3,}/g,'\n\n').trim()};
let ld=lp&&lp.description?plain(lp.description):'';if(/<(p|br|li|div|ul)\b/i.test(ld))ld=plain(ld);
let desc='';for(const s of SEL.split('|')){if(!s)continue;const e=d.querySelector(s);if(e){desc=(e.innerText||e.textContent||'').trim();break}}
if(desc.length<200&&ld.length>desc.length)desc=ld;
let postingTitle=lp&&typeof lp.title==='string'?lp.title.trim():'';
const wdHead=d.querySelector('[data-automation-id="jobPostingHeader"]');
if(!postingTitle&&wdHead)postingTitle=(wdHead.innerText||'').trim();
const cl=document.querySelector('link[rel=canonical]')||d.querySelector('link[rel=canonical]');
const shell=/\.(myworkdayjobs|myworkdaysite)\.com$/i.test(location.hostname)&&!postingTitle;
return {href:location.href,status,title:document.title||d.title||'',heading:h1?(h1.innerText||'').trim():'',postingTitle,banner,text,description:desc.slice(0,30000),applyLinks,canonical:cl?cl.href:'',hasApply:controls.length>0,passwordForm:pw,shell};
})()`
}

// livenessDoc is what livenessReadJS returns.
type livenessDoc struct {
	Href         string   `json:"href"`
	Status       int      `json:"status"`
	Title        string   `json:"title"`
	Heading      string   `json:"heading"`
	PostingTitle string   `json:"postingTitle"`
	Banner       string   `json:"banner"`
	Text         string   `json:"text"`
	Description  string   `json:"description"`
	ApplyLinks   []string `json:"applyLinks"`
	Canonical    string   `json:"canonical"`
	HasApply     bool     `json:"hasApply"`
	PasswordForm bool     `json:"passwordForm"`
	Shell        bool     `json:"shell"`
}

// fill puts the document into pg. The address and status are the document's
// own, so a script redirect or a challenge's reload is read where it ended,
// and a throttle status met on the way stays Throttled.
func (doc livenessDoc) fill(pg *discovery.PageRead) {
	if doc.Href != "" {
		pg.FinalURL = doc.Href
	}
	pg.Throttled = pg.Throttled || discovery.ThrottleStatus(pg.Status)
	if doc.Status > 0 {
		pg.Status = doc.Status
	}
	pg.Title = firstNonEmpty(doc.Title, pg.Title)
	pg.Heading, pg.PostingTitle, pg.Banner, pg.Text, pg.Description = doc.Heading, doc.PostingTitle, doc.Banner, doc.Text, doc.Description
	pg.ApplyLinks, pg.Canonical, pg.HasApply, pg.PasswordForm = doc.ApplyLinks, doc.Canonical, doc.HasApply, doc.PasswordForm
	pg.NoPosting = doc.Shell && !discovery.StatesClosure(*pg)
}

// browserLiveness reads posting pages in the scan window of the browser that
// open returns, started or adopted on first use. Each read opens a scan tab and
// closes it on every path; nothing is typed or clicked.
type browserLiveness struct {
	open func(context.Context) (*browser.Instance, error)
}

// chromeOpener starts or adopts the managed browser of a workspace.
type chromeOpener struct {
	opts browser.Options
	mu   sync.Mutex
}

// LivenessReader returns the liveness page reader of a workspace.
func LivenessReader(ws *workspace.Workspace) discovery.PageReader {
	o, _ := browserOptions(ws)
	return &browserLiveness{open: (&chromeOpener{opts: o}).instance}
}

// ChromeLiveness is the liveness page reader of the browser that open
// returns. Tests give it a throwaway browser.
func ChromeLiveness(open func(context.Context) (*browser.Instance, error)) discovery.PageReader {
	return &browserLiveness{open: open}
}

// ReleaseBrowser stops the managed browser this process launched for the
// workspace when nothing holds it (browser.StopIdleOnExit). A command-line
// command that may have read pages calls it before it exits.
func ReleaseBrowser(ctx context.Context, ws *workspace.Workspace) {
	o, _ := browserOptions(ws)
	browser.StopIdleOnExit(ctx, o.ProfileDir)
}

// ScanWindow is the scan window of a workspace's managed browser, which the
// browser scans open tabs in. The browser is started or adopted on first use.
// watch reports the "watch scans" runtime setting (nil never watches).
func ScanWindow(ws *workspace.Workspace, watch func(context.Context) bool) discovery.Window {
	o, _ := browserOptions(ws)
	return ChromeWindow((&chromeOpener{opts: o}).instance, watch)
}

func (b *chromeOpener) instance(ctx context.Context) (*browser.Instance, error) {
	b.mu.Lock()
	if b.opts.Chrome.Path == "" {
		c, err := browser.DiscoverChrome(ctx, os.Getenv)
		if err != nil {
			b.mu.Unlock()
			return nil, err
		}
		b.opts.Chrome = c
	}
	opts := b.opts
	b.mu.Unlock()
	return browser.Open(ctx, opts)
}

// Read reads the page until it shows its posting (text or an apply control,
// and a job board's description) or LivenessReadWait has passed; a document
// with no body yet is read again until then.
func (b *browserLiveness) Read(ctx context.Context, rawURL string) (discovery.PageRead, error) {
	lease, err := scanLease(ctx, b.open, "liveness")
	if err != nil {
		return discovery.PageRead{}, err
	}
	defer lease.Release()
	h := lease.Lane()
	id, err := h.OpenTab(ctx, "liveness")
	if err != nil {
		return discovery.PageRead{}, err
	}
	defer func() { _ = h.CloseTab(context.WithoutCancel(ctx), id) }()
	nctx, cancel := context.WithTimeout(ctx, discovery.LivenessNavTimeout)
	nav, err := h.Navigate(nctx, id, rawURL)
	cancel()
	switch {
	case ctx.Err() != nil:
		return discovery.PageRead{}, ctx.Err()
	case err != nil && nctx.Err() != nil:
		return discovery.PageRead{TimedOut: true}, nil
	case err != nil:
		return discovery.PageRead{}, err
	case nav.Error != "":
		return discovery.PageRead{LoadFailed: true, Status: nav.Status}, nil
	}
	pg := discovery.PageRead{Status: nav.Status, FinalURL: nav.URL, Title: nav.Title}
	desc := discovery.ListingDescription(pg.FinalURL)
	deadline := time.Now().Add(discovery.LivenessReadWait)
	for {
		raw, err := h.Evaluate(ctx, id, livenessReadJS(desc))
		if err != nil {
			return discovery.PageRead{}, err
		}
		var doc livenessDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return discovery.PageRead{}, fmt.Errorf("liveness: the page read has an unexpected shape: %w", err)
		}
		doc.fill(&pg)
		shown := (doc.HasApply || len([]rune(doc.Text)) >= 200) && !pg.NoPosting && (desc == "" || len([]rune(doc.Description)) >= 200)
		if shown || time.Now().After(deadline) {
			return pg, nil
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return discovery.PageRead{}, ctx.Err()
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
