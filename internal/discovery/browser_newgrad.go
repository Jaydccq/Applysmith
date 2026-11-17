package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

const (
	jobrightBase    = "https://jobright.ai"
	newgradCountry  = "us"
	newgradCategory = "swe"
)

var noSponsorRE = lazyregexp.New(`(?i)\bno h-?1b\b|no sponsorship|not (offer|provide|available).{0,20}sponsorship|sponsorship (is )?not (available|offered|provided)|(will|do|does|can) not sponsor|unable to sponsor`)

// ngListJS reads the newest jobs of one category from the data the minisite
// renders on the server. newgrad-jobs.com itself only frames this page, so
// this is the list a visitor sees there. It returns the shared extraction
// shape, with the posting time as an ISO date and the work model as extra.
var ngListJS = script("newgrad-list", `const n=document.getElementById('__NEXT_DATA__');let d=null;try{d=n?JSON.parse(n.textContent):null}catch(e){}
const pp=(d&&d.props&&d.props.pageProps)||{};const js=Array.isArray(pp.initialJobs)?pp.initialJobs:[];
const rows=js.filter(j=>j&&j.id&&j.title).map(j=>({id:String(j.id),title:String(j.title),company:String(j.company||''),location:String(j.location||''),salary:String(j.salary||''),extra:String(j.workModel||''),datetime:j.postedDate?new Date(j.postedDate).toISOString():''}));
return {url:location.href,cards:rows.length,rows};`)

// ngPick returns the first present string value among keys: the object's own,
// else that of a nested object at any depth, the nested objects taken in the
// order of their keys so two reads of one document agree.
func ngPick(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if sub, ok := m[k].(map[string]any); ok {
			if s := ngPick(sub, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

// ngSub returns the first nested object stored under key, in the same order.
func ngSub(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if sub, ok := m[k].(map[string]any); ok {
			if got := ngSub(sub, key); got != nil {
				return got
			}
		}
	}
	return nil
}

// newgradListURL names the minisite page of one country and category, the
// path newgrad-jobs.com embeds for its tabs. The site has no date filter and
// no further pages in the markup, so a page is the newest 50 jobs.
func newgradListURL(base string, q Query) (string, error) {
	if q.Get("url") != "" {
		return "", errors.New("a newgrad search takes a country and a category, not a url; newgrad-jobs.com only frames the Jobright list")
	}
	country := strings.ToLower(firstNonEmpty(q.Get("country"), newgradCountry))
	category := firstNonEmpty(q.Get("category"), newgradCategory)
	return strings.TrimRight(base, "/") + "/minisites-jobs/newgrad/" + url.PathEscape(country) + "/" + url.PathEscape(category), nil
}

// readNewGrad reads the new-grad list of each saved search and enriches each
// survivor from its Jobright page.
func readNewGrad(ctx context.Context, r *run) error {
	qs, err := r.queries()
	if err != nil {
		return err
	}
	if len(qs) == 0 {
		qs = []Query{{P: map[string]string{}}}
	}
	b, err := r.newBscan()
	if err != nil {
		return err
	}
	defer b.close()
	base := r.base("jobright", jobrightBase)
	for _, q := range qs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.queryRan()
		if err := b.readNewGradQuery(ctx, base, q); err != nil {
			var end *endRun
			if errors.As(err, &end) || ctx.Err() != nil || errors.Is(err, ErrOffline) {
				return err
			}
			r.queryFailed(q.label(), err)
		}
	}
	return nil
}

func (b *bscan) readNewGradQuery(ctx context.Context, base string, q Query) error {
	r := b.r
	listURL, err := newgradListURL(base, q)
	if err != nil {
		return err
	}
	window := r.browserWindow()
	r.recordSearch(q.label(), listURL, window, false)
	lr, err := b.readList(ctx, pageSpec{url: listURL, extract: ngListJS, once: true})
	if err != nil {
		return err
	}
	if end := b.pageEnds(lr, listURL); end != nil {
		return end
	}
	if lr.Cards == 0 {
		// The list is the newest jobs of a category, never empty by date: no
		// rows means a misspelled category or country, or a changed page.
		r.layoutChange(ReasonNoCards, q.label()+": the page lists no jobs; check the country and category, or the page may have changed")
		return nil
	}
	now := r.repo.now()
	var posts []Posting
	for _, c := range lr.cards() {
		if c.ID == "" || c.Title == "" {
			continue
		}
		p := Posting{Title: c.Title, Company: c.Company, Location: c.Location, JobID: c.ID, Compensation: c.Salary, Remote: strings.EqualFold(c.Extra, "remote"),
			URL: strings.TrimRight(base, "/") + "/jobs/info/" + url.PathEscape(c.ID), PostedAt: cardTime(c, now)}
		p.addFlag(FlagMirror)
		posts = append(posts, p)
	}
	if limit := r.rowLimit(0); limit > 0 && len(posts) > limit {
		posts = posts[:limit]
	}
	posts = r.freshOrDrop(posts, window, false, false)
	posts, err = r.listingRejects(ctx, posts, true)
	if err != nil {
		return err
	}
	return b.finishRows(ctx, jobrightDetail, q.label(), posts)
}

// finishRows runs the shared steps after the list: the detail reads, the
// sponsorship check and the pipeline.
func (b *bscan) finishRows(ctx context.Context, ds *detailSpec, label string, rows []Posting) error {
	r := b.r
	if len(rows) > 0 {
		var err error
		if rows, err = b.enrichRows(ctx, ds, rows); err != nil {
			return err
		}
		rows = r.sponsorshipCheck(rows)
	}
	if len(rows) > 0 {
		if err := r.emit(ctx, label, rows); err != nil {
			return err
		}
	}
	return b.pendingEnd()
}

// sponsorshipCheck drops, as `filtered:sponsorship`, the rows labelled
// no-sponsorship when the profile needs sponsorship.
func (r *run) sponsorshipCheck(rows []Posting) []Posting {
	wa := r.req.Profile.WorkAuthorization
	needs := (wa.RequiresSponsorshipNow != nil && *wa.RequiresSponsorshipNow) || (wa.RequiresSponsorshipFuture != nil && *wa.RequiresSponsorshipFuture)
	if !needs {
		return rows
	}
	var keep []Posting
	dropped := 0
	for _, p := range rows {
		if p.hasFlag(FlagNoSponsorship) {
			dropped++
			continue
		}
		keep = append(keep, p)
	}
	r.dropRows(FilterSponsorship, dropped)
	return keep
}

// ---- Jobright detail ----

var jobrightDetail = &detailSpec{
	sel: detailSel{Title: "h1", Company: "[class*=\"company-name\"]|[data-testid=\"company-name\"]", Location: "[class*=\"job-location\"]|[data-testid=\"job-location\"]",
		Desc: "[data-testid=\"job-description\"]|[class*=\"job-description\"]", Next: true},
	boardHost: hostIn("jobright.ai"),
	gapLo:     250 * time.Millisecond, gapHi: 750 * time.Millisecond,
}

func init() { jobrightDetail.read = readJobrightDetail }

// jrFacts are the structured facts of a Jobright posting page.
type jrFacts struct {
	Apply, Salary, Seniority, Sponsor, Summary, Published string
	Skills                                                []string
	Match                                                 string
	Clearance                                             bool
}

// jrSponsorship words the page's work-authorisation flags the way the
// description signals read them: a citizen-only posting sponsors nothing, and
// a flagged H-1B sponsor is stated. Unflagged says nothing; Jobright sets the
// flag only on evidence.
func jrSponsorship(job map[string]any) string {
	if job["isCitizenOnly"] == true {
		return "US citizens only, no sponsorship"
	}
	if job["isH1bSponsor"] == true {
		return "H1B Sponsor Likely"
	}
	return ""
}

func jrFactsOf(next json.RawMessage) jrFacts {
	var doc any
	if json.Unmarshal(next, &doc) != nil {
		return jrFacts{}
	}
	m, _ := doc.(map[string]any)
	// The flags sit on the page's jobResult object; a page without one has none.
	job := ngSub(m, "jobResult")
	// The posting's own fields come first: a company or a similar job on the
	// page carries a description, a salary and a link under the same keys.
	pick := func(keys ...string) string {
		if s := ngPick(job, keys...); s != "" {
			return s
		}
		return ngPick(m, keys...)
	}
	f := jrFacts{Apply: pick("applyLink", "applyUrl", "originalUrl"), Salary: pick("salaryDesc", "salary"),
		Seniority: pick("jobSeniority", "seniority"), Summary: pick("jobSummary", "jobDescription", "description"),
		Published: ngPick(job, "publishTime"), Sponsor: jrSponsorship(job), Match: pick("matchScore", "recommendationScore")}
	f.Clearance = job["isClearanceRequired"] == true
	var walk func(v any, d int)
	walk = func(v any, d int) {
		if d > 6 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if arr, ok := x["skills"].([]any); ok && len(f.Skills) == 0 {
				for _, s := range arr {
					switch sv := s.(type) {
					case string:
						f.Skills = append(f.Skills, sv)
					case map[string]any:
						if n := ngPick(sv, "skill", "name"); n != "" {
							f.Skills = append(f.Skills, n)
						}
					}
				}
			}
			for _, e := range x {
				walk(e, d+1)
			}
		case []any:
			for _, e := range x {
				walk(e, d+1)
			}
		}
	}
	walk(doc, 0)
	return f
}

// readJobrightDetail loads a Jobright posting page with a 30 second
// navigation and a 20 second extraction limit, and once more at 45 seconds
// after a one second settle. A second failure keeps the row on its list text.
func readJobrightDetail(ctx context.Context, b *bscan, tab Tab, p *Posting) error {
	nav, ext := orDur(b.env.DetailNav, 30*time.Second), orDur(b.env.DetailEval, 20*time.Second)
	var d detailRead
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			b.r.addDetail("retries", 1)
			nav, ext = orDur(b.env.DetailRetry, 45*time.Second), orDur(b.env.DetailRetry, 45*time.Second)
			if err := b.env.sleep(ctx, time.Second); err != nil {
				return err
			}
		}
		b.r.addDetail("detail-opens", 1)
		if d, err = jrAttempt(ctx, tab, firstNonEmpty(p.fetchURL, p.URL), nav, ext); err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return err
	}
	f := jrFactsOf(d.Next)
	if d.Desc == "" {
		d.Desc = f.Summary
	}
	var extra []string
	if len(f.Skills) > 0 {
		extra = append(extra, "Skills: "+strings.Join(f.Skills, ", "))
	}
	if f.Seniority != "" {
		extra = append(extra, "Seniority: "+f.Seniority)
	}
	if f.Salary != "" {
		extra = append(extra, "Salary: "+f.Salary)
		p.Compensation = f.Salary
	}
	if f.Clearance {
		extra = append(extra, "Security clearance required")
	}
	if f.Sponsor != "" {
		extra = append(extra, "Sponsorship: "+f.Sponsor)
		if noSponsorRE.MatchString(f.Sponsor) {
			p.addFlag(FlagNoSponsorship)
		}
	}
	if d.Desc != "" || len(extra) > 0 {
		d.Desc = strings.TrimSpace(d.Desc + "\n\n" + strings.Join(extra, "\n"))
	}
	if d.Apply == nil && f.Apply != "" {
		d.Apply = &applyControl{Href: f.Apply, Text: "apply"}
	} else if d.Apply != nil && f.Apply != "" && usefulApplyURL(f.Apply, jobrightDetail.boardHost) {
		d.Apply.Href = f.Apply
	}
	if f.Match != "" {
		p.addFlag("match=" + f.Match)
	}
	// The page states the exact publication time (UTC); the card only says
	// "3 hours ago" or "1 day ago".
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", f.Published, time.UTC); err == nil {
		p.PostedAt = t
	}
	return b.applyDetail(ctx, tab, jobrightDetail, p, d)
}

func jrAttempt(ctx context.Context, tab Tab, target string, navLimit, evalLimit time.Duration) (detailRead, error) {
	nctx, cancel := context.WithTimeout(ctx, navLimit)
	nav, err := tab.Goto(nctx, target)
	cancel()
	if err != nil {
		return detailRead{}, err
	}
	if nav.Failed != "" {
		return detailRead{}, fmt.Errorf("the page did not load: %s", nav.Failed)
	}
	ectx, cancel := context.WithTimeout(ctx, evalLimit)
	defer cancel()
	var d detailRead
	err = tab.Eval(ectx, detailJS(jobrightDetail.sel), &d)
	return d, err
}
