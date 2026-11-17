package discovery

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// Link kinds, in rank order: a lead's apply link is its best-ranked link,
// with shorteners expanded first.
const (
	LinkShortener       = "shortener"
	LinkATSJob          = "ats-job"
	LinkJobDetail       = "job-detail"
	LinkApplicationForm = "application-form"
	LinkCareersIndex    = "careers-index"
	LinkCompanyHome     = "company-home"
	LinkOther           = "other"
)

var linkRank = map[string]int{LinkATSJob: 0, LinkJobDetail: 1, LinkApplicationForm: 2, LinkCareersIndex: 3, LinkCompanyHome: 4, LinkOther: 5, LinkShortener: 6}

var shortenerHosts = []string{"bit.ly", "t.co", "lnkd.in", "tinyurl.com", "ow.ly", "buff.ly", "is.gd", "rb.gy", "cutt.ly"}

// lnkd.in answers 200 with a "you are leaving LinkedIn" page whose
// external_url_click link is the destination, instead of redirecting.
var interstitialRE = lazyregexp.New(`(?is)<a\b[^>]*\bdata-tracking-control-name\s*=\s*["']external_url_click["'][^>]*\bhref\s*=\s*["']([^"']+)["']`)

var formHosts = []string{"forms.gle", "typeform.com", "jotform.com"}

var (
	careersWords   = map[string]bool{"careers": true, "career": true, "jobs": true, "join-us": true, "joinus": true, "open-positions": true, "work-with-us": true, "opportunities": true}
	jobDetailRE    = lazyregexp.New(`(?i)^/(?:[a-z]{2}(?:-[a-z]{2})?/)?(?:job|jobs|positions?|openings?|vacanc(?:y|ies)|careers/jobs?)/[^/]+`)
	emailRE        = lazyregexp.New(`(?i)[a-z0-9._%+\-]+@[a-z0-9\-]+(?:\.[a-z0-9\-]+)*\.[a-z]{2,}`)
	metaRefreshRE  = lazyregexp.New(`(?is)<meta[^>]+http-equiv\s*=\s*["']?refresh["']?[^>]*content\s*=\s*["']\s*\d*\s*;\s*url\s*=\s*([^"']+)["']`)
	scriptRedirRE  = lazyregexp.New(`(?is)(?:window\.|document\.)?location(?:\.href)?\s*=\s*["']([^"']+)["']|location\.(?:replace|assign)\(\s*["']([^"']+)["']\s*\)`)
	loginPathRE    = lazyregexp.New(`(?i)/(?:login|log-in|signin|sign-in|authwall|checkpoint|uas/login|accounts/login)(?:/|$|\?)`)
	maxShortenHops = 3
)

func hostMatches(host string, list []string) bool {
	for _, d := range list {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// LinkKind classifies one link. An unparseable or non-http(s) URL is other.
func LinkKind(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return LinkOther
	}
	host := strings.ToLower(u.Hostname())
	path := strings.TrimRight(u.Path, "/")
	switch {
	case hostMatches(host, shortenerHosts):
		return LinkShortener
	case func() bool { _, ok := identity.ATSKey(raw); return ok }():
		return LinkATSJob
	case hostMatches(host, formHosts) || (host == "docs.google.com" && strings.HasPrefix(path, "/forms")):
		return LinkApplicationForm
	}
	if _, err := ResolveBoard(raw); err == nil {
		return LinkCareersIndex
	}
	if path == "" {
		return LinkCompanyHome
	}
	segs := strings.Split(strings.Trim(strings.ToLower(path), "/"), "/")
	only := true
	for _, s := range segs {
		if !careersWords[s] {
			only = false
		}
	}
	switch {
	case only:
		return LinkCareersIndex
	case jobDetailRE.MatchString(path) && !NonPostingURL(u.Scheme+"://"+u.Host+path):
		return LinkJobDetail
	}
	return LinkOther
}

// SinglePostingLink reports whether a link kind names one posting.
func SinglePostingLink(kind string) bool {
	return kind == LinkATSJob || kind == LinkJobDetail || kind == LinkApplicationForm
}

// RankLinks orders links by kind, best first, with shorteners removed. The
// order within a kind is the order given.
func RankLinks(links []string) []string {
	type ranked struct {
		url  string
		rank int
	}
	var rs []ranked
	for _, l := range links {
		if k := LinkKind(l); k != LinkShortener {
			rs = append(rs, ranked{l, linkRank[k]})
		}
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.url
	}
	return out
}

// ExtractEmails returns the contact addresses found in text and mail links,
// lower-cased, without repeats.
func ExtractEmails(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range emailRE.FindAllString(strings.ReplaceAll(text, "mailto:", " "), -1) {
		m = strings.ToLower(strings.TrimRight(m, ".,;:"))
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// Expander expands link shorteners over plain HTTP: it follows redirects, one
// meta refresh, one script redirect and lnkd.in's leave-LinkedIn page per hop,
// for at most three hops with
// ten seconds each and no cookies. It never signs in and never goes past a
// login or challenge page.
type Expander struct {
	// HTTP supplies the transport; redirects are followed here, not by it.
	HTTP *http.Client
	// Limit is the number of expansions allowed per lead or signal; zero means five.
	Limit int
	used  int
}

const (
	expandHopTimeout = 10 * time.Second
	expandPerLead    = 5
)

// Expand returns the final URL of a shortener. ok is false when the link is
// not a shortener, the allowance is spent, or expansion stopped before a
// destination: past three hops, at a login or challenge page, or on an
// address that is not public.
func (e *Expander) Expand(ctx context.Context, raw string) (final string, ok bool) {
	if LinkKind(raw) != LinkShortener {
		return raw, false
	}
	limit := e.Limit
	if limit == 0 {
		limit = expandPerLead
	}
	if e.used >= limit {
		return raw, false
	}
	e.used++
	hc := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if e.HTTP != nil {
		hc.Transport = e.HTTP.Transport
	}
	cur := raw
	for hop := 0; hop < maxShortenHops; hop++ {
		next, stop := e.hop(ctx, &hc, cur)
		if stop {
			return raw, false
		}
		if next == "" {
			return cur, LinkKind(cur) != LinkShortener
		}
		cur = next
	}
	return raw, false
}

// hop makes one request. next is the redirect target, empty when this page is
// the destination; stop is true at a wall or an unusable address.
func (e *Expander) hop(ctx context.Context, hc *http.Client, cur string) (next string, stop bool) {
	u, err := url.Parse(cur)
	if err != nil || identity.NonPublicHost(u.Hostname()) != "" {
		return "", true
	}
	if loginPathRE.MatchString(u.Path) {
		return "", true
	}
	rctx, cancel := context.WithTimeout(ctx, expandHopTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, cur, nil)
	if err != nil {
		return "", true
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := hc.Do(req)
	if err != nil {
		return "", true
	}
	defer res.Body.Close()
	if loc := res.Header.Get("Location"); loc != "" && res.StatusCode >= 300 && res.StatusCode < 400 {
		return resolveRef(u, loc), false
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests {
		return "", true
	}
	page := string(body)
	// The leave page names its destination; its markup also carries a
	// recaptcha feature flag the challenge words would take for a wall.
	if m := interstitialRE.FindStringSubmatch(page); m != nil {
		return resolveRef(u, html.UnescapeString(strings.TrimSpace(m[1]))), false
	}
	if challengeRE.Match(head(body)) {
		return "", true
	}
	if m := metaRefreshRE.FindStringSubmatch(page); m != nil {
		return resolveRef(u, strings.TrimSpace(m[1])), false
	}
	if m := scriptRedirRE.FindStringSubmatch(page); m != nil {
		return resolveRef(u, strings.TrimSpace(m[1]+m[2])), false
	}
	return "", false
}

func resolveRef(base *url.URL, ref string) string {
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ""
	}
	return base.ResolveReference(r).String()
}
