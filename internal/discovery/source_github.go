package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/lazyregexp"
)

// GitHub list bounds.
const (
	githubWindow      = 24 * time.Hour
	githubPageTimeout = 15 * time.Second
	githubPageWorkers = 3

	// A redirect link is followed at most this many hops, each bounded by
	// githubPageTimeout, eight links at a time.
	redirectHops    = 4
	redirectWorkers = 8

	// Filter reasons of a redirect link that gives no employer posting.
	FilterDeadLink       = "dead-link"
	FilterUnresolvedLink = "unresolved-link"
)

// redirectLinkHosts are list hosts whose links are redirects to the employer's
// own posting (zapply.jobs/l/d/workday-..., go.vansh.nyc/15K?s=A for every
// row of the vanshb03 list), not postings themselves.
var redirectLinkHosts = []string{"zapply.jobs", "go.vansh.nyc"}

// DefaultGitHubLists are the public new-grad lists read when none is saved.
// They are addresses only; the lists themselves are fetched at run time.
var DefaultGitHubLists = []string{
	"https://raw.githubusercontent.com/SimplifyJobs/New-Grad-Positions/dev/README.md",
	"https://raw.githubusercontent.com/zapplyjobs/New-Grad-Software-Engineering-Positions/main/README.md",
	"https://raw.githubusercontent.com/vanshb03/New-Grad-2026/main/README.md",
}

// readGitHub reads the README of each list, keeps the rows inside the
// freshness window and hands them to the pipeline, which enriches the
// survivors from their application pages.
func readGitHub(ctx context.Context, r *run) error {
	qs, err := r.queries()
	if err != nil {
		return err
	}
	if len(qs) == 0 {
		for _, u := range DefaultGitHubLists {
			qs = append(qs, Query{P: map[string]string{"url": u}})
		}
	}
	window := r.window(githubWindow)
	links := &linkResolver{r: r, done: map[string]resolvedLink{}}
	fresh := 0
	for _, q := range qs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.queryRan()
		listURL := firstNonEmpty(q.Get("url"), q.Search)
		if listURL == "" {
			r.queryFailed(q.label(), fmt.Errorf("a list entry needs a url"))
			continue
		}
		pg := r.readPage(ctx, listURL)
		if pg.Err != nil || pg.Res.Status >= 400 {
			if _, end := pg.settle(0); end != nil {
				return end
			}
			if pg.Err != nil {
				r.queryFailed(listURL, pg.Err)
			} else {
				r.queryFailed(listURL, fmt.Errorf("the list answered %d", pg.Res.Status))
			}
			continue
		}
		rows := parseListTables(string(pg.Res.Body))
		if _, end := pg.settle(len(rows)); end != nil {
			return end
		}
		if len(rows) == 0 {
			r.layoutChange(ReasonLayoutChanged, listURL)
			continue
		}
		posts := links.resolve(ctx, r.listPostings(rows, window))
		fresh += len(posts)
		if r.req.Limit > 0 && len(posts) > r.req.Limit {
			r.dropRows("over-limit", len(posts)-r.req.Limit)
			posts = posts[:r.req.Limit]
		}
		if err := r.emit(ctx, listURL, posts); err != nil {
			return err
		}
	}
	r.mu.Lock()
	c := r.rec.Counts
	r.mu.Unlock()
	if fresh > 0 && c.Inserted == 0 && sumMap(c.Duplicates) == fresh {
		r.endReason = ReasonAllSeen
	}
	return nil
}

// listPostings turns table rows into postings, counting what it drops: rows
// the list closed, rows with no company, role or application link, rows whose
// age cannot be read, and rows outside the window.
func (r *run) listPostings(rows []listRow, window time.Duration) []Posting {
	now := r.repo.now()
	var out []Posting
	var closed, incomplete, undated, stale int
	for _, row := range rows {
		switch {
		case row.Closed:
			closed++
			continue
		case row.Company == "" || row.Role == "" || row.Apply == "":
			incomplete++
			continue
		}
		age, ok := AgeOf(row.Age, now)
		switch {
		case !ok:
			undated++
			continue
		case window > 0 && age >= window:
			stale++
			continue
		}
		out = append(out, Posting{Company: row.Company, Title: row.Role, Location: row.Location, URL: row.Apply, PostedAt: now.Add(-age)})
	}
	r.dropRows("closed", closed)
	r.dropRows("incomplete", incomplete)
	r.dropRows("stale", stale)
	r.undatedRows(undated)
	return out
}

// resolvedLink is what a redirect link led to: the employer's posting URL, or
// the reason it led to none.
type resolvedLink struct {
	url, drop string
}

// linkResolver turns a list's redirect links into the employer posting URLs
// before the pipeline derives an identity from them, so the same job listed by
// two lists is one posting. Each link is followed once per scan, by plain HTTP.
type linkResolver struct {
	r    *run
	mu   sync.Mutex
	done map[string]resolvedLink
}

// resolve returns the postings whose links resolved, with the employer URL in
// place of the redirect link. A link that lands on the redirector's own site
// (the posting is gone) or cannot be followed is dropped and counted; it is
// not stored with an address that names no posting.
func (l *linkResolver) resolve(ctx context.Context, posts []Posting) []Posting {
	var wg sync.WaitGroup
	sem := make(chan struct{}, redirectWorkers)
	drops := make([]string, len(posts))
	for i := range posts {
		if !isRedirectLink(posts[i].URL) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				drops[i] = FilterUnresolvedLink
				return
			}
			res := l.follow(ctx, posts[i].URL)
			posts[i].URL, drops[i] = res.url, res.drop
		}()
	}
	wg.Wait()
	out := posts[:0]
	var dead, unresolved int
	for i, p := range posts {
		switch {
		case drops[i] == FilterDeadLink:
			dead++
		case drops[i] != "" || p.URL == "":
			unresolved++
		default:
			out = append(out, p)
		}
	}
	l.r.dropRows(FilterDeadLink, dead)
	l.r.dropRows(FilterUnresolvedLink, unresolved)
	return out
}

func isRedirectLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && hostMatches(strings.ToLower(u.Hostname()), redirectLinkHosts)
}

func (l *linkResolver) follow(ctx context.Context, link string) resolvedLink {
	l.mu.Lock()
	res, ok := l.done[link]
	l.mu.Unlock()
	if ok {
		return res
	}
	res = l.walk(ctx, link)
	if ctx.Err() == nil {
		l.mu.Lock()
		l.done[link] = res
		l.mu.Unlock()
	}
	return res
}

// walk follows the redirect chain until it leaves the redirector's hosts. The
// client follows redirects within a host, so each request ends either at an
// off-host redirect (the employer's posting) or at a page of the redirector
// itself, which means it has no such posting.
func (l *linkResolver) walk(ctx context.Context, link string) resolvedLink {
	cur := link
	for hop := 0; hop < redirectHops; hop++ {
		res, err := l.r.client.Do(ctx, Request{URL: cur, NoRetry: true, Timeout: githubPageTimeout, Accept: "text/html,*/*;q=0.5"})
		if err != nil {
			return resolvedLink{drop: FilterUnresolvedLink}
		}
		loc := res.Header.Get("Location")
		if res.Status < 300 || res.Status >= 400 || loc == "" {
			return resolvedLink{drop: FilterDeadLink}
		}
		base, err := url.Parse(cur)
		if err != nil {
			return resolvedLink{drop: FilterUnresolvedLink}
		}
		cur = resolveRef(base, loc)
		if !isRedirectLink(cur) {
			if u, err := url.Parse(cur); err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
				return resolvedLink{drop: FilterUnresolvedLink}
			}
			return resolvedLink{url: cur}
		}
	}
	return resolvedLink{drop: FilterUnresolvedLink}
}

// enrichApplicationPages reads the application page of each candidate, three
// at a time, and keeps the page text as the description. A page that cannot
// be read leaves the row on its list text.
func (r *run) enrichApplicationPages(ctx context.Context, cands []candidate) (enriched, degraded, skipped int) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, githubPageWorkers)
	for i := range cands {
		p := &cands[i].p
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				skipped++
				mu.Unlock()
				return
			}
			text, err := r.applicationText(ctx, p.URL)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				degraded++
				return
			}
			p.Description = text
			enriched++
		}()
	}
	wg.Wait()
	// A page every company shares is a template (a maintenance notice, a
	// sign-in shell, a site's landing page), not any one posting's text.
	for _, drop := range sharedText(cands) {
		cands[drop].p.Description = ""
		enriched--
		degraded++
	}
	return
}

// sharedText returns the candidates whose description is word for word the
// description of a candidate of another company. One company repeating its
// own text across requisitions is real; unrelated employers never share a page.
func sharedText(cands []candidate) []int {
	companies := map[string]map[string]bool{}
	for _, c := range cands {
		if c.p.Description == "" {
			continue
		}
		set := companies[c.p.Description]
		if set == nil {
			set = map[string]bool{}
			companies[c.p.Description] = set
		}
		set[NormalizeCompany(c.p.Company)] = true
	}
	var out []int
	for i, c := range cands {
		if len(companies[c.p.Description]) > 1 && c.p.Description != "" {
			out = append(out, i)
		}
	}
	return out
}

// pageTitleRE reads a page's title.
var pageTitleRE = lazyregexp.New(`(?is)<title\b[^>]*>(.*?)</title>`)

// errorTitleRE names the titles of maintenance, outage, block and not-found
// pages, which an application link can land on instead of the posting.
var errorTitleRE = lazyregexp.New(`(?i)\b(?:currently|temporarily) unavailable\b|\b(?:under|scheduled) maintenance\b|\bmaintenance (?:page|mode|notice)\b|\bis unavailable\b|\bservice unavailable\b|\baccess denied\b|\bjust a moment\b|\battention required\b|\bpage not found\b|\bnot found\b|^\s*(?:404|403|500|503)\b|\bsite can.t be reached\b`)

func (r *run) applicationText(ctx context.Context, rawURL string) (string, error) {
	if api := oracleRequisitionURL(rawURL); api != "" {
		return r.oracleRequisitionText(ctx, api)
	}
	res, err := r.client.Do(ctx, Request{URL: rawURL, NoRetry: true, Timeout: githubPageTimeout, Accept: "text/html,*/*;q=0.5"})
	if err != nil {
		return "", err
	}
	if res.Status != http.StatusOK {
		return "", fmt.Errorf("the page answered %d", res.Status)
	}
	page := string(res.Body)
	if m := pageTitleRE.FindStringSubmatch(page); m != nil && errorTitleRE.MatchString(textOf(m[1])) {
		return "", fmt.Errorf("the page is %q, not the posting", textOf(m[1]))
	}
	if lds := jsonLDPostings(page); len(lds) > 0 {
		if d := postingFromLD(lds[0]).Description; d != "" {
			return d, nil
		}
	}
	text := strings.TrimSpace(plainText(page))
	if text == "" {
		return "", fmt.Errorf("the page has no text")
	}
	return truncateRunes(text, maxDescription), nil
}

// oracleJobRE reads the site (optional: a link may omit it) and requisition
// number of an Oracle Recruiting candidate-experience posting path.
var oracleJobRE = lazyregexp.New(`^/hcmUI/CandidateExperience/[^/]+/(?:sites/([A-Za-z0-9_-]+)/)?(?:job|requisitions/preview)/(\d+)`)

// oracleRequisitionURL is the public requisition endpoint of an Oracle
// Recruiting posting, or "" for any other address. The posting page is a
// script shell whose only text is the site's name, so it is never read as the
// description.
func oracleRequisitionURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !hostMatches(strings.ToLower(u.Hostname()), []string{"oraclecloud.com"}) {
		return ""
	}
	m := oracleJobRE.FindStringSubmatch(u.Path)
	if m == nil {
		return ""
	}
	finder := "ById%3BId%3D%22" + m[2] + "%22"
	if m[1] != "" {
		finder += "%2CsiteNumber%3D" + m[1]
	}
	return "https://" + u.Host + "/hcmRestApi/resources/latest/recruitingCEJobRequisitionDetails?expand=all&onlyData=true&finder=" + finder
}

// oracleRequisitionText reads the posting's own sections from the requisition
// endpoint. A requisition the endpoint does not list is closed or private.
func (r *run) oracleRequisitionText(ctx context.Context, api string) (string, error) {
	res, err := r.client.Do(ctx, Request{URL: api, NoRetry: true, Timeout: githubPageTimeout, Accept: "application/json"})
	if err != nil {
		return "", err
	}
	if res.Status != http.StatusOK {
		return "", fmt.Errorf("the requisition endpoint answered %d", res.Status)
	}
	var doc struct {
		Items []struct {
			ExternalDescriptionStr      string
			ExternalResponsibilitiesStr string
			ExternalQualificationsStr   string
		}
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		return "", fmt.Errorf("the requisition endpoint answered no JSON: %w", err)
	}
	if len(doc.Items) == 0 {
		return "", fmt.Errorf("the requisition is not listed")
	}
	it := doc.Items[0]
	var parts []string
	for _, frag := range []string{it.ExternalDescriptionStr, it.ExternalResponsibilitiesStr, it.ExternalQualificationsStr} {
		if t := strings.TrimSpace(plainText(frag)); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("the requisition has no text")
	}
	return truncateRunes(strings.Join(parts, "\n\n"), maxDescription), nil
}
