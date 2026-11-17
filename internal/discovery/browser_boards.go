package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

const (
	indeedBase       = "https://www.indeed.com"
	zipRecruiterBase = "https://www.ziprecruiter.com"
	googleBase       = "https://www.google.com"
	glassdoorBase    = "https://www.glassdoor.com"
)

func hostIn(domain string) func(string) bool {
	return func(h string) bool { return h == domain || strings.HasSuffix(h, "."+domain) }
}

// absQuery resolves a saved URL against the board's base and returns it with
// a mutable query.
func absQuery(base, raw string) (*url.URL, url.Values, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	if !u.IsAbs() {
		bu, err := url.Parse(base)
		if err != nil {
			return nil, nil, err
		}
		u = bu.ResolveReference(u)
	}
	return u, u.Query(), nil
}

// ---- Indeed ----

var indeedJobTypes = map[string]string{"full-time": "fulltime", "part-time": "parttime", "contract": "contract", "temporary": "temporary", "internship": "internship"}
var indeedLevels = map[string]string{"entry": "entry_level", "mid": "mid_level", "senior": "senior_level"}

var indeedListJS = script("indeed-list", `const cs=OUTER([...document.querySelectorAll('div.job_seen_beacon,td.resultContent,[data-jk]')]);
const rows=cs.map(c=>{const k=c.getAttribute('data-jk')||QA(c,'[data-jk]','data-jk');
const a=Q(c,'a.jcs-JobTitle|h2.jobTitle a|a[data-jk]');
return {id:k,title:QT(c,'h2.jobTitle span[title]|h2.jobTitle|a.jcs-JobTitle')||T(a),company:QT(c,'[data-testid="company-name"]|.companyName'),location:QT(c,'[data-testid="text-location"]|.companyLocation'),
href:a?ABS(a.getAttribute('href')||''):'',age:QT(c,'[data-testid="myJobsStateDate"]|span.date'),salary:QT(c,'[data-testid="attribute_snippet_testid"]|.salary-snippet-container'),easy:/easily apply/i.test(T(c))};
}).filter(r=>r.id);
return {url:location.href,cards:cs.length,rows};`)

func indeedURL(base string, q Query, n int, window time.Duration) (string, bool, error) {
	var u *url.URL
	var qs url.Values
	var err error
	if raw := q.Get("url"); raw != "" {
		if u, qs, err = absQuery(base, raw); err != nil {
			return "", false, err
		}
	} else {
		if u, qs, err = absQuery(base, "/jobs"); err != nil {
			return "", false, err
		}
		qs.Set("q", q.Search)
		if v := q.place(); v != "" {
			qs.Set("l", v)
		}
		if v := indeedJobTypes[q.Get("job_type")]; v != "" {
			qs.Set("jt", v)
		}
		if v := indeedLevels[q.Get("experience_level")]; v != "" {
			qs.Set("explvl", v)
		}
		if window > 0 {
			if d, ok := pickMenu([]int{1, 3, 7, 14}, window); ok {
				qs.Set("fromage", strconv.Itoa(d))
			}
		}
	}
	start, _ := strconv.Atoi(qs.Get("start"))
	if start+n*10 > 0 {
		qs.Set("start", strconv.Itoa(start+n*10))
	}
	u.RawQuery = qs.Encode()
	return u.String(), qs.Get("fromage") != "", nil
}

// indeedGuard accepts a live URL that still names the requested search. The
// query, the location and the offset compare strictly. A filter the site
// normalizes away and drops from the live URL is accepted, because a strict
// comparison of it would request the page forever; one the live URL keeps
// with another value is a contradiction.
func indeedGuard(requested string) func(string) bool {
	ru, err := url.Parse(requested)
	if err != nil {
		return nil
	}
	rq := ru.Query()
	return func(live string) bool {
		lu, err := url.Parse(live)
		if err != nil {
			return true
		}
		lq := lu.Query()
		if !sameParam(rq, lq, "q") || !sameParam(rq, lq, "l") {
			return false
		}
		rs, _ := strconv.Atoi(rq.Get("start"))
		ls, _ := strconv.Atoi(lq.Get("start"))
		if rs != ls {
			return false
		}
		for _, k := range []string{"jt", "explvl", "fromage", "sc", "remotejob"} {
			if lq.Get(k) != "" && rq.Get(k) != "" && !sameParam(rq, lq, k) {
				return false
			}
		}
		return true
	}
}

func indeedPosting(base string, c rawCard, now time.Time) (Posting, bool) {
	if c.ID == "" || strings.TrimSpace(c.Title) == "" {
		return Posting{}, false
	}
	p := Posting{Title: c.Title, Company: c.Company, Location: c.Location, JobID: c.ID, Compensation: c.Salary,
		URL: strings.TrimRight(base, "/") + "/viewjob?jk=" + url.QueryEscape(c.ID), PostedAt: cardTime(c, now)}
	p.addFlag(FlagMirror)
	if c.Easy {
		p.addFlag(FlagEasyApply)
	}
	return p, true
}

var jkRE = lazyregexp.New(`(?i)[?&]jk=([0-9a-z]+)`)

// otherIndeedKey reports a destination that names another Indeed posting.
func otherIndeedKey(landing, own string) bool {
	m := jkRE.FindStringSubmatch(landing)
	if m == nil {
		return false
	}
	o := jkRE.FindStringSubmatch(own)
	return o == nil || !strings.EqualFold(o[1], m[1])
}

var indeedSpec = &boardSpec{
	source: "indeed", defBase: indeedBase, pageSize: 10, defPages: 1, warmUp: true,
	extract: indeedListJS, build: indeedURL, guard: indeedGuard, posting: indeedPosting,
	detail: &detailSpec{
		sel: detailSel{Title: "h1.jobsearch-JobInfoHeader-title|h1", Company: "[data-testid=\"inlineHeader-companyName\"]|[data-company-name]",
			Location: "[data-testid=\"inlineHeader-companyLocation\"]", Desc: "#jobDescriptionText|.jobsearch-JobComponent-description"},
		boardHost: hostIn("indeed.com"), otherKey: otherIndeedKey,
	},
}

// ---- ZipRecruiter ----

// The live results page links no posting: the results page shows a posting
// in its pane when its address names the card's id as lk, so that address is
// the row's. Rows are one per id. The current layout is a list item per
// posting whose article carries the id in its test id (job-card-<id>), the
// company a /co/ link and the place a location= link; the earlier one
// rendered each card twice (the desktop list and a hidden mobile copy) as
// article#job-card-<id>.
var zipListJS = script("ziprecruiter-list", `const cs=OUTER([...document.querySelectorAll('article[data-testid^="job-card-"],article[id^="job-card"],[data-testid="job-card"],div.job_result_two_pane')]);
const seen=new Set();
const rows=cs.map(c=>{const a=Q(c,'a[data-testid="job-card-link"]|a[href*="/c/"]|h2 a|a.job_link');const href=a?(a.getAttribute('href')||''):'';
const m=href.match(/[?&]jid=([^&]+)/);const t=Q(c,'time');const id=c.getAttribute('data-job-id')||(m?m[1]:'')||(c.getAttribute('data-testid')||'').replace(/^job-card-?/,'')||(c.id||'').replace(/^job-card-?/,'');
let to=href?ABS(href):'';if(!to&&id){const u=new URL(location.href);u.searchParams.set('lk',id);to=u.href}
return {id,title:QT(c,'[data-testid="job-card-title"]|[data-testid="serp-job-card-title"]|h2')||T(a),company:QT(c,'[data-testid="job-card-company"]|a.company_name|a[href^="/co/"]'),
location:QT(c,'[data-testid="job-card-location"]|.location|a[href*="location="]'),href:to,age:QT(c,'[data-testid="job-card-posted"]|[data-testid="new-label-badge"]')||(t?T(t):''),datetime:t?(t.getAttribute('datetime')||''):'',salary:QT(c,'[data-testid="job-card-salary"]')};
}).filter(r=>r.href&&!(r.id&&seen.has(r.id))&&(!r.id||seen.add(r.id)));
return {url:location.href,cards:cs.length,rows};`)

func zipURL(base string, q Query, n int, window time.Duration) (string, bool, error) {
	var u *url.URL
	var qs url.Values
	var err error
	if raw := q.Get("url"); raw != "" {
		if u, qs, err = absQuery(base, raw); err != nil {
			return "", false, err
		}
	} else {
		if u, qs, err = absQuery(base, "/jobs-search"); err != nil {
			return "", false, err
		}
		qs.Set("search", q.Search)
		// With no place ZipRecruiter searches 25 miles around the place it
		// guesses from the IP (live: 0 results around Myrtle Beach, SC, 116 in
		// the United States), so the whole country stands in for no place.
		place := q.place()
		if place == "" {
			place = "United States"
		}
		qs.Set("location", place)
		if window > 0 {
			if d, ok := pickMenu([]int{1, 5, 10, 30}, window); ok {
				qs.Set("days", strconv.Itoa(d))
			}
		}
	}
	page, _ := strconv.Atoi(qs.Get("page"))
	if p := max(page, 1) + n; p > 1 {
		qs.Set("page", strconv.Itoa(p))
	}
	u.RawQuery = qs.Encode()
	return u.String(), qs.Get("days") != "", nil
}

func zipPosting(base string, c rawCard, now time.Time) (Posting, bool) {
	if c.Href == "" || strings.TrimSpace(c.Title) == "" {
		return Posting{}, false
	}
	p := Posting{Title: c.Title, Company: c.Company, Location: c.Location, JobID: c.ID, Compensation: c.Salary, URL: c.Href, PostedAt: cardTime(c, now)}
	p.addFlag(FlagMirror)
	return p, true
}

var zipSpec = &boardSpec{
	source: "ziprecruiter", defBase: zipRecruiterBase, pageSize: 20, defPages: 1, warmUp: true,
	extract: zipListJS, build: zipURL, posting: zipPosting,
	detail: &detailSpec{
		// The results page's pane: its h1 is the result count, so the pane's
		// title comes first.
		sel: detailSel{Title: "[data-testid=\"serp-job-details-title\"]|h1", Company: "[data-testid=\"job-company\"]|a.company_name|[data-testid=\"employer-details-header\"] span",
			Location: "[data-testid=\"job-location\"]|[data-testid=\"serp-job-details-title\"]+div",
			Desc:     "[data-testid=\"job-description\"]|.job_description|.jobDescriptionSection|[data-testid=\"job-details-scroll-container\"] .whitespace-pre-line"},
		boardHost: hostIn("ziprecruiter.com"),
	},
}

// ---- Google Jobs ----

// The live list (udm=8) is a div per posting carrying data-share-url; its
// place line ends with the source ("North Carolina • via Cisco Careers") and
// its age is an unclassed span ("3 days ago").
var googleListJS = script("google-jobs-list", `const cs=OUTER([...document.querySelectorAll('li[data-share-url],div[data-share-url],li.iFjolb')]);
const age=c=>QT(c,'.LL4CDc|span[aria-label^="Posted"]')||T([...c.querySelectorAll('span')].find(s=>!s.children.length&&/^\d+\+? (minute|hour|day|week|month)s? ago$/i.test(T(s))));
const rows=cs.map(c=>({id:c.getAttribute('data-share-url')||'',title:QT(c,'.BjJfJf|.tNxQIb|[role="heading"]'),company:QT(c,'.vNEEBe|.a3jPc'),location:QT(c,'.Qk80Jf|.FqK3wc').replace(/\s*\u2022\s*via .*$/,''),href:ABS(c.getAttribute('data-share-url')||''),age:age(c)})).filter(r=>r.href);
return {url:location.href,cards:cs.length,rows};`)

// googleChip is the nearest freshness chip the board honors that is at least
// as wide as the request: week, month, any; anything tighter than a week is a
// 3-day window, and the local filter enforces the ask.
func googleChip(window time.Duration) string {
	switch {
	case window <= 0:
		return ""
	case window <= 3*day:
		return "date_posted:3days"
	case window <= 7*day:
		return "date_posted:week"
	}
	return "date_posted:month"
}

func googleURL(base string, q Query, n int, window time.Duration) (string, bool, error) {
	if raw := q.Get("url"); raw != "" {
		u, qs, err := absQuery(base, raw)
		if err != nil {
			return "", false, err
		}
		return u.String(), strings.Contains(qs.Get("htichips"), "date_posted"), nil
	}
	u, qs, err := absQuery(base, "/search")
	if err != nil {
		return "", false, err
	}
	text := q.Search
	if loc := q.place(); loc != "" {
		text += " " + loc
	}
	qs.Set("q", strings.TrimSpace(text))
	qs.Set("udm", "8")
	chip := googleChip(window)
	if chip != "" {
		qs.Set("htichips", chip)
	}
	u.RawQuery = qs.Encode()
	return u.String(), chip != "", nil
}

func googlePosting(base string, c rawCard, now time.Time) (Posting, bool) {
	if c.Href == "" || strings.TrimSpace(c.Title) == "" {
		return Posting{}, false
	}
	p := Posting{Title: c.Title, Company: c.Company, Location: c.Location, URL: c.Href, PostedAt: cardTime(c, now)}
	p.addFlag(FlagMirror)
	return p, true
}

var googleSpec = &boardSpec{
	source: "google-jobs", defBase: googleBase, pageSize: 10, defPages: 1, maxPages: 1,
	extract: googleListJS, build: googleURL, posting: googlePosting, enforceWindow: true, deferURLCheck: true,
	detail: &detailSpec{
		// The live detail viewer is a group labelled "Job details for ...",
		// beside hidden previews of the neighbouring postings
		// (data-is-preview="true"); the page's first h1 is a feedback dialog's.
		sel: detailSel{Title: "[data-is-preview=\"false\"][aria-label^=\"Job details for\"] [role=\"heading\"]|h1,[role=\"heading\"]",
			Company: ".nJlQNd|[data-is-preview=\"false\"][aria-label^=\"Job details for\"] .knLYHc", Location: ".sMzDkb",
			Desc: ".HBvzbc|[jsname=\"HBvzbc\"]|#gws-plugins-horizon-jobs__job_details_page|[data-is-preview=\"false\"][aria-label^=\"Job details for\"]"},
		gapLo: 1500 * time.Millisecond, gapHi: 4 * time.Second,
		boardHost: hostIn("google.com"),
	},
}

// ---- Glassdoor ----

// The cards print no age, but the page carries each listing's ageInDays in
// its Next.js data (jobview.header.ageInDays, keyed by jobview.job.listingId,
// the card's data-jobid). The array is read out of the flight chunks and a
// failure to read it leaves the cards undated, as before.
var glassdoorListJS = script("glassdoor-list", `const cs=OUTER([...document.querySelectorAll('li[data-test="jobListing"],li.JobsList_jobListItem__wjTHv')]);
const ages={};try{const re=/self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)/g;let f='';for(const s of document.querySelectorAll('script')){const t=s.textContent;if(t.indexOf('__next_f')<0)continue;let m;while((m=re.exec(t))){try{f+=JSON.parse(m[1])}catch(e){}}}
const i=f.indexOf('"jobListings":[');if(i>=0){let d=0,q=false,x=false,end=-1;const j=i+14;for(let k=j;k<f.length;k++){const c=f[k];if(q){if(x)x=false;else if(c==='\\')x=true;else if(c==='"')q=false;continue}if(c==='"')q=true;else if(c==='['||c==='{')d++;else if(c===']'||c==='}'){d--;if(d===0){end=k;break}}}
if(end>0)for(const o of JSON.parse(f.slice(j,end+1))){const v=o&&o.jobview;if(v&&v.job&&v.header&&typeof v.header.ageInDays==='number')ages[String(v.job.listingId)]=v.header.ageInDays}}}catch(e){}
const rows=cs.map(c=>{const a=Q(c,'a[data-test="job-title"]|a[href*="/job-listing/"]|a[data-test="job-link"]');const href=a?(a.getAttribute('href')||''):'';
const m=href.match(/[?&]jl=(\d+)/);
return {id:c.getAttribute('data-jobid')||(m?m[1]:''),title:QT(c,'a[data-test="job-title"]')||T(a),company:QT(c,'[class*="compactEmployerName"]|[data-test="employer-short-name"]|[data-test="emp-name"]'),location:QT(c,'[data-test="emp-location"]'),href:ABS(href),age:QT(c,'[data-test="job-age"]')||(ages[c.getAttribute('data-jobid')||(m?m[1]:'')]!=null?ages[c.getAttribute('data-jobid')||(m?m[1]:'')]+'d':''),salary:QT(c,'[data-test="detailSalary"]')};
}).filter(r=>r.href);
return {url:location.href,cards:cs.length,rows};`)

// glassdoorLocateJS asks the site's own location typeahead, from inside a
// Glassdoor page, which places a term names (the call the search box makes on
// every keystroke; best match first).
func glassdoorLocateJS(term string) string {
	return script("glassdoor-locate", `return fetch('/autocomplete/location?locationTypeFilters=CITY,STATE,COUNTRY&caller=site-header&term='+encodeURIComponent(`+jsString(term)+`),{credentials:'include',headers:{'x-requested-with':'XMLHttpRequest'}})
.then(async r=>{let l=null;try{l=JSON.parse(await r.text())}catch(e){}
return {status:r.status,places:Array.isArray(l)?l.slice(0,5).map(x=>({id:String((x&&x.locationId)||''),type:String((x&&x.locationType)||''),name:String((x&&(x.locationName||x.label))||''),label:String((x&&(x.longName||x.label))||'')})):null}})
.catch(e=>({status:0,places:null}));`)
}

type glassdoorPlace struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Name  string `json:"name"`
	Label string `json:"label"`
}

type glassdoorPlaces struct {
	Status int              `json:"status"`
	Places []glassdoorPlace `json:"places"`
}

// pick is the place the term names exactly ("Austin, TX"), else the site's
// best match. Places without an id or a type are not places.
func (g glassdoorPlaces) pick(term string) (glassdoorPlace, bool) {
	var usable []glassdoorPlace
	for _, p := range g.Places {
		if p.ID != "" && p.Type != "" {
			usable = append(usable, p)
		}
	}
	for _, p := range usable {
		if strings.EqualFold(strings.TrimSpace(term), p.Name) || strings.EqualFold(strings.TrimSpace(term), strings.TrimSuffix(p.Label, " (US)")) {
			return p, true
		}
	}
	if len(usable) == 0 {
		return glassdoorPlace{}, false
	}
	return usable[0], true
}

// glassdoorLocate resolves a search's place to Glassdoor's location id and
// type (locT, locId): the results page applies those and ignores a location
// keyword. The lookup runs in a Glassdoor page of the scan tab, with the
// session the tab already has. A saved search with its own URL is left as
// it is.
func glassdoorLocate(ctx context.Context, b *bscan, base string, q Query) (Query, error) {
	place := q.place()
	if place == "" || q.Get("url") != "" {
		return q, nil
	}
	if err := b.ensureTab(ctx); err != nil {
		return q, err
	}
	b.r.addDetail("list-opens", 1)
	home := strings.TrimRight(base, "/") + "/Job/index.htm"
	nav, err := b.tab.Goto(ctx, home)
	if err != nil {
		return q, err
	}
	b.warmed = true
	b.reads++
	if nav.Failed != "" {
		return q, fmt.Errorf("the page did not load: %s", nav.Failed)
	}
	var pr pageProbe
	if nav.Status != 403 && nav.Status != 429 {
		if err := b.tab.Eval(ctx, probeJS, &pr); err != nil {
			return q, err
		}
	}
	if block, signal := classifyPage(nav, pr, glassdoorSignIn); block != blockNone {
		return q, b.pageEnds(listRead{Nav: nav, Block: block, Signal: signal}, home)
	}
	var res glassdoorPlaces
	if err := b.tab.Eval(ctx, glassdoorLocateJS(place), &res); err != nil {
		return q, err
	}
	if res.Status != http.StatusOK {
		return q, fmt.Errorf("glassdoor: the location lookup for %q answered %d", place, res.Status)
	}
	pick, ok := res.pick(place)
	if !ok {
		return q, fmt.Errorf("glassdoor did not recognise the location %q; use a city it lists or a saved search url", place)
	}
	out := Query{Search: q.Search, P: map[string]string{}}
	for k, v := range q.P {
		out.P[k] = v
	}
	out.P["locT"], out.P["locId"] = pick.Type, pick.ID
	b.r.detail("location-resolved", 1)
	return out, nil
}

// glassdoorURL addresses the results the way the site does, by its search
// path ("<place>-<words>-jobs-SRCH_IL.0,9_IC1139761_KO10,27.htm", the
// offsets naming the place and the words in the slug, the place by its id
// and type). /Job/jobs.htm redirects there and drops fromAge, and no address
// turns a page (p= and _IP2 serve the first page again).
func glassdoorURL(base string, q Query, n int, window time.Duration) (string, bool, error) {
	if raw := q.Get("url"); raw != "" {
		u, qs, err := absQuery(base, raw)
		if err != nil {
			return "", false, err
		}
		return u.String(), qs.Get("fromAge") != "", nil
	}
	words := glassdoorSlug(q.Search)
	if words == "" {
		return "", false, errors.New("glassdoor: a search needs words")
	}
	path := fmt.Sprintf("/Job/%s-jobs-SRCH_KO0,%d.htm", words, len(words))
	if id := q.Get("locId"); id != "" {
		place := glassdoorSlug(q.place())
		path = fmt.Sprintf("/Job/%s-%s-jobs-SRCH_IL.0,%d_I%s%s_KO%d,%d.htm", place, words, len(place), q.Get("locT"), id, len(place)+1, len(place)+1+len(words))
	}
	u, qs, err := absQuery(base, path)
	if err != nil {
		return "", false, err
	}
	if window > 0 {
		if d, ok := pickMenu([]int{1, 3, 7, 14, 30}, window); ok {
			qs.Set("fromAge", strconv.Itoa(d))
		}
	}
	u.RawQuery = qs.Encode()
	return u.String(), qs.Get("fromAge") != "", nil
}

var glassdoorSlugRE = lazyregexp.New(`[^a-z0-9]+`)

// glassdoorSlug is the site's slug of a phrase: lower case, every run of other
// characters one hyphen ("Austin, TX" is "austin-tx").
func glassdoorSlug(s string) string {
	return strings.Trim(glassdoorSlugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func glassdoorPosting(base string, c rawCard, now time.Time) (Posting, bool) {
	if c.Href == "" || strings.TrimSpace(c.Title) == "" {
		return Posting{}, false
	}
	p := Posting{Title: c.Title, Company: c.Company, Location: c.Location, JobID: c.ID, Compensation: c.Salary, URL: c.Href, PostedAt: cardTime(c, now)}
	p.addFlag(FlagMirror)
	return p, true
}

var glassdoorSignIn = lazyregexp.New(`(?i)sign in to (see|view|continue)|create (a )?free account to`)

var glassdoorSpec = &boardSpec{
	source: "glassdoor", defBase: glassdoorBase, pageSize: 30, defPages: 1, maxPages: 1, warmUp: true,
	signInText: glassdoorSignIn,
	extract:    glassdoorListJS, build: glassdoorURL, locate: glassdoorLocate, posting: glassdoorPosting,
	detail: &detailSpec{
		sel:       detailSel{Title: "h1", Company: "[data-test=\"employer-name\"]|[class*=\"EmployerProfile_employerNameHeading\"]", Location: "[data-test=\"location\"]", Desc: "[class*=\"JobDetails_jobDescription\"]|.jobDescriptionContent"},
		boardHost: hostIn("glassdoor.com"),
	},
}
