package discovery

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	linkedInBase     = "https://www.linkedin.com"
	linkedInPageSize = 25
)

// linkedInListJS reads the job cards of a results page from the cards
// themselves, with no click: each card carries its own job id. The signed-in
// results page (2026-10) renders a card as a componentkey'd button with no
// link or class to key on: the title is its first paragraph's aria-hidden
// text, the company and place the next two paragraphs, and the age a
// "Posted … ago" paragraph (a card already opened says "Viewed" instead).
var linkedInListJS = script("linkedin-list", `const cs=OUTER([...document.querySelectorAll('[componentkey^="job-card-component-ref-"],li[data-occludable-job-id],div.job-card-container[data-job-id],div.base-card[data-entity-urn],li.jobs-search-results__list-item')]);
const rows=cs.map(c=>{const ck=c.getAttribute('componentkey')||'';
if(ck.startsWith('job-card-component-ref-')){const id=ck.slice(23),ps=[...c.querySelectorAll('p')].filter(p=>T(p));
const posted=ps.find(p=>/^(re)?posted\b/i.test(T(p)));
return {id,title:QT(ps[0]||c,'span[aria-hidden="true"]')||T(ps[0]).replace(/\s*\(verified job\)\s*$/i,''),company:T(ps[1]),location:T(ps[2]),href:ABS('/jobs/view/'+id+'/'),age:posted?(QT(posted,'span[aria-hidden="true"]')||T(posted).replace(/^(re)?posted\s+/i,'')):'',datetime:'',easy:/easy apply/i.test(T(c))}}
const a=Q(c,'a.job-card-list__title|a.job-card-container__link|a.base-card__full-link|a[href*="/jobs/view/"]');
const href=a?(a.getAttribute('href')||''):'';
let id=c.getAttribute('data-occludable-job-id')||c.getAttribute('data-job-id')||'';
if(!id){const u=c.getAttribute('data-entity-urn')||'';id=u.split(':').pop()||''}
if(!id){const m=href.match(/\/jobs\/view\/(?:[^\/?]*-)?(\d+)/);if(m)id=m[1]}
const tm=Q(c,'time');
return {id,title:a?(a.getAttribute('aria-label')||T(a)):QT(c,'.base-search-card__title'),company:QT(c,'.job-card-container__primary-description|.artdeco-entity-lockup__subtitle|.base-search-card__subtitle'),location:QT(c,'.job-card-container__metadata-item|.artdeco-entity-lockup__caption|.job-search-card__location'),href:ABS(href),age:tm?T(tm):'',datetime:tm?(tm.getAttribute('datetime')||''):'',easy:/easy apply/i.test(T(c))};
}).filter(r=>r.id||r.href);
return {url:location.href,cards:cs.length,rows};`)

const linkedInCardSel = "[componentkey^=\"job-card-component-ref-\"],li[data-occludable-job-id],div.job-card-container[data-job-id],div.base-card[data-entity-urn]"

func linkedInHost(h string) bool { return h == "linkedin.com" || strings.HasSuffix(h, ".linkedin.com") }

// linkedInURL builds result page n of a saved search. A saved URL is used as
// stored, moved to the paginated results path so a page request returns the
// configured page size; its freshness and Easy Apply parameters are left
// alone. The results page drops location and f_WT from
// the address and searches the account's own region instead, so both are
// written into the keywords, which it does read: "remote backend engineer in
// Austin, TX".
func linkedInURL(base string, q Query, n int, window time.Duration) (string, bool, error) {
	var u *url.URL
	if raw := q.Get("url"); raw != "" {
		var err error
		if u, err = url.Parse(raw); err != nil {
			return "", false, err
		}
		if !u.IsAbs() {
			bu, _ := url.Parse(base)
			u = bu.ResolveReference(u)
		}
		if strings.HasPrefix(u.Path, "/jobs/search") && !strings.HasPrefix(u.Path, "/jobs/search-results") {
			u.Path = "/jobs/search-results/"
		}
	} else {
		bu, err := url.Parse(base)
		if err != nil {
			return "", false, err
		}
		u = bu.ResolveReference(&url.URL{Path: "/jobs/search-results/"})
		qs := u.Query()
		qs.Set("keywords", q.Search)
		if loc := q.place(); loc != "" {
			qs.Set("location", loc)
		}
		if q.Get("remote") == "true" {
			qs.Set("f_WT", "2")
		}
		if window > 0 {
			qs.Set("f_TPR", "r"+strconv.Itoa(int(window.Seconds())))
		}
		u.RawQuery = qs.Encode()
	}
	qs := u.Query()
	linkedInWords(qs)
	start, _ := strconv.Atoi(qs.Get("start"))
	qs.Set("start", strconv.Itoa(start+n*linkedInPageSize))
	u.RawQuery = qs.Encode()
	return u.String(), qs.Get("f_TPR") != "", nil
}

// linkedInWorkplace names the single workplace types of f_WT as words.
var linkedInWorkplace = map[string]string{"1": "on-site", "2": "remote", "3": "hybrid"}

// linkedInWords moves location and f_WT into the keywords.
func linkedInWords(qs url.Values) {
	k := strings.TrimSpace(qs.Get("keywords"))
	if w := linkedInWorkplace[qs.Get("f_WT")]; w != "" && !strings.HasPrefix(strings.ToLower(k), w+" ") {
		k = w + " " + k
	}
	if loc := strings.TrimSpace(qs.Get("location")); loc != "" {
		k += " in " + loc
	}
	qs.Set("keywords", strings.TrimSpace(k))
	qs.Del("location")
	qs.Del("f_WT")
}

func linkedInPosting(base string, c rawCard, now time.Time) (Posting, bool) {
	if c.ID == "" || strings.TrimSpace(c.Title) == "" {
		return Posting{}, false
	}
	p := Posting{Title: c.Title, Company: c.Company, Location: c.Location, JobID: c.ID,
		URL: strings.TrimRight(base, "/") + "/jobs/view/" + c.ID, PostedAt: cardTime(c, now)}
	p.addFlag(FlagMirror)
	if c.Easy {
		p.addFlag(FlagEasyApply)
	}
	return p, true
}

var linkedInDetail = &detailSpec{
	sel: detailSel{
		Title: ".top-card-layout__title|.job-details-jobs-unified-top-card__job-title|h1", Company: ".topcard__org-name-link|.job-details-jobs-unified-top-card__company-name|[aria-label^=\"Company, \"]",
		Location: ".topcard__flavor--bullet|.job-details-jobs-unified-top-card__primary-description-container", Posted: ".posted-time-ago__text|.job-details-jobs-unified-top-card__posted-date",
		Applicants: ".num-applicants__caption|.job-details-jobs-unified-top-card__applicant-count",
		Desc:       ".show-more-less-html__markup|.jobs-description__content|#job-details|.description__text|[componentkey^=\"JobDetails_AboutTheJob_\"] [data-testid=\"expandable-text-box\"]",
	},
	boardHost: linkedInHost,
}

var linkedInSpec = &boardSpec{
	source: "linkedin", defBase: linkedInBase, pageSize: linkedInPageSize, defPages: 6, rowCap: 250, frontier: true, rereadEmpty: true,
	extract: linkedInListJS, scrollCards: linkedInCardSel, build: linkedInURL, posting: linkedInPosting, detail: linkedInDetail,
}
