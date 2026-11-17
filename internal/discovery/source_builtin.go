package discovery

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

const builtInBase = "https://builtin.com"

// builtInMenu is the site's own recency menu, in days.
var builtInMenu = []int{1, 3, 7, 30}

var (
	cardSplitRE  = lazyregexp.New(`(?i)data-id\s*=\s*["']job-card["']`)
	cardTitleRE  = lazyregexp.New(`(?is)<a\b([^>]*data-id\s*=\s*["']job-card-title["'][^>]*)>(.*?)</a>`)
	cardCompanyR = lazyregexp.New(`(?is)<a\b[^>]*data-id\s*=\s*["']company-title["'][^>]*>(.*?)</a>`)
	// The location is the first text after the card's location-pin icon.
	cardLocRE = lazyregexp.New(`(?is)fa-location-dot\b[^>]*>.*?<span\b([^>]*)>(.*?)</span>`)
	// A card with several locations says "5 Locations" and lists them in the
	// tooltip attribute of that text.
	cardLocTipRE = lazyregexp.New(`(?is)\bdata-bs-title\s*=\s*"([^"]*)"`)
	cardLocDivRE = lazyregexp.New(`(?is)<div\b[^>]*>(.*?)</div>`)
	cardAgeRE    = lazyregexp.New(`(?i)(?:reposted\s+)?(?:an?|\d+)\s+(?:minute|hour|day|week|month)s?\s+ago|yesterday|today`)
	emptyPageRE  = lazyregexp.New(`(?i)no (?:matching )?jobs|0 jobs|no results|didn['’]t find any`)
)

// builtInDays is the smallest value of the site's menu that covers the window.
func builtInDays(window time.Duration) int {
	for _, d := range builtInMenu {
		if time.Duration(d)*day >= window {
			return d
		}
	}
	return builtInMenu[len(builtInMenu)-1]
}

// builtInURL is the page of one saved search. A path keeps its segments in
// the order stored, takes the recency parameter from the window and a search
// parameter only when the search has a keyword; a full URL is requested as
// stored. A keyword without a path or URL (the ad-hoc `scan builtin --search`)
// searches the site's all-jobs page. hasWindow tells whether the effective URL carries the recency
// parameter.
func builtInURL(base string, q Query, window time.Duration) (u string, hasWindow bool, err error) {
	raw := q.Get("url")
	explicit := raw != ""
	if !explicit {
		raw = q.Get("path")
	}
	if raw == "" && q.Search != "" {
		raw = "/jobs"
	}
	if raw == "" {
		return "", false, fmt.Errorf("a Built In search needs a keyword, a path or a url")
	}
	pu, err := url.Parse(raw)
	if err != nil {
		return "", false, err
	}
	if !pu.IsAbs() {
		bu, err := url.Parse(base)
		if err != nil {
			return "", false, err
		}
		pu = bu.ResolveReference(&url.URL{Path: "/" + strings.TrimLeft(pu.Path, "/"), RawQuery: pu.RawQuery})
	}
	qs := pu.Query()
	if !explicit {
		if q.Search != "" {
			qs.Set("search", q.Search)
		}
		for _, k := range []string{"country", "allLocations"} {
			if v := q.Get(k); v != "" {
				qs.Set(k, v)
			}
		}
		if window > 0 && qs.Get("daysSinceUpdated") == "" {
			qs.Set("daysSinceUpdated", strconv.Itoa(builtInDays(window)))
		}
	}
	pu.RawQuery = qs.Encode()
	return pu.String(), qs.Get("daysSinceUpdated") != "", nil
}

// parseBuiltIn reads a listing page: the embedded JobPosting objects when
// there are any, else the visible cards. cards is the number of card
// containers seen, parsed or not.
func parseBuiltIn(page, base string, now time.Time) (posts []Posting, cards int) {
	if lds := jsonLDPostings(page); len(lds) > 0 {
		for _, o := range lds {
			p := postingFromLD(o)
			p.URL = absURL(base, p.URL)
			if p.Title != "" && p.URL != "" {
				posts = append(posts, p)
			}
		}
		return posts, len(lds)
	}
	parts := cardSplitRE.Split(page, -1)
	cards = len(parts) - 1
	for _, part := range parts[1:] {
		a := cardTitleRE.FindStringSubmatch(part)
		if a == nil {
			continue
		}
		p := Posting{Title: textOf(a[2]), URL: absURL(base, attr(a[1], "href"))}
		if m := cardCompanyR.FindStringSubmatch(part); m != nil {
			p.Company = textOf(m[1])
		}
		if m := cardLocRE.FindStringSubmatch(part); m != nil {
			p.Location = textOf(m[2])
			if tip := cardLocTipRE.FindStringSubmatch(m[1]); tip != nil {
				var locs []string
				for _, d := range cardLocDivRE.FindAllStringSubmatch(html.UnescapeString(tip[1]), -1) {
					if l := textOf(d[1]); l != "" {
						locs = append(locs, l)
					}
				}
				if len(locs) > 0 {
					p.Location = strings.Join(locs, "; ")
				}
			}
		}
		if age := cardAgeRE.FindString(textOf(part)); age != "" {
			if d, ok := RelativeAge(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(age), "reposted "))); ok {
				p.PostedAt = now.Add(-d)
			}
		}
		if p.Title != "" && p.URL != "" {
			posts = append(posts, p)
		}
	}
	return posts, cards
}

func absURL(base, ref string) string {
	ref = html.UnescapeString(strings.TrimSpace(ref))
	if ref == "" {
		return ""
	}
	bu, err := url.Parse(base)
	if err != nil {
		return ""
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return bu.ResolveReference(ru).String()
}

// readBuiltIn reads the saved Built In searches over HTTP.
func readBuiltIn(ctx context.Context, r *run) error {
	qs, err := r.queriesOrSkip()
	if err != nil {
		return err
	}
	base := r.base("builtin", builtInBase)
	window := r.window(0)
	for _, q := range qs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.queryRan()
		label := q.label()
		target, hasWindow, err := builtInURL(base, q, window)
		if err != nil {
			r.queryFailed(label, err)
			continue
		}
		pg := r.readPage(ctx, target)
		if pg.Err != nil {
			if _, end := pg.settle(0); end != nil {
				return end
			}
			r.queryFailed(label, pg.Err)
			continue
		}
		if pg.Res.Status >= 400 {
			if _, end := pg.settle(0); end != nil {
				return end
			}
			r.queryFailed(label, fmt.Errorf("the page answered %d", pg.Res.Status))
			continue
		}
		page := string(pg.Res.Body)
		posts, cards := parseBuiltIn(page, base, r.repo.now())
		if _, end := pg.settle(len(posts)); end != nil {
			return end
		}
		switch {
		case len(posts) > 0:
		case cards > 0:
			r.layoutChange(ReasonEmptyExtract, label)
			continue
		case emptyPageRE.MatchString(page):
			if hasWindow {
				r.endReason = ReasonEmptyWindow
				continue
			}
			r.layoutChange(ReasonNoCards, label)
			continue
		default:
			r.layoutChange(ReasonLayoutChanged, label)
			continue
		}
		posts = r.freshOnly(posts, window)
		if r.req.Limit > 0 && len(posts) > r.req.Limit {
			posts = posts[:r.req.Limit]
		}
		if err := r.emit(ctx, label, posts); err != nil {
			return err
		}
	}
	return nil
}

// freshOnly drops rows older than the window, counted `stale`: the site's
// recency menu is coarser than the window, so the local filter enforces the
// rest. Rows without a posted time are kept.
func (r *run) freshOnly(posts []Posting, window time.Duration) []Posting {
	if window <= 0 {
		return posts
	}
	cutoff := r.repo.now().Add(-window)
	var out []Posting
	stale := 0
	for _, p := range posts {
		if !p.PostedAt.IsZero() && p.PostedAt.Before(cutoff) {
			stale++
			continue
		}
		out = append(out, p)
	}
	r.dropRows("stale", stale)
	return out
}
