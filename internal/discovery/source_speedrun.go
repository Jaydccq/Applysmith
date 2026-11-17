package discovery

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/lazyregexp"
)

// Speedrun board bounds and defaults.
const (
	speedrunBase     = "https://speedrun-talent-network.com"
	speedrunMaxPages = 2
	speedrunRSSItems = 100
	speedrunWindow   = 24 * time.Hour
	// speedrunCraft and speedrunSeniority are the board filters used when none
	// is given. The employment type is deliberately not sent by default: the
	// board's filter drops every row whose type is empty, which is where the
	// new-grad postings sit.
	speedrunCraft     = "engineering"
	speedrunSeniority = "junior"
)

// The board embeds each listing as an encoded object on the page.
var speedrunObjectRE = lazyregexp.New(`\[0,\{"id":\[0,"([^"]+)"\],"title":\[0,"((?:\\.|[^"\\])*)"\],"company_key":\[0,"((?:\\.|[^"\\])*)"\][\s\S]*?"path":\[0,"(/jobs/[^"]+)"\]\}`)

// speedrunListURL is the list page of a query: an explicit URL wins outright;
// otherwise the board's own filters steer the scan, never free text alone.
func speedrunListURL(base string, q Query, req ScanRequest, page int) string {
	if explicit := firstNonEmpty(req.URL, q.Get("url")); explicit != "" {
		u, err := url.Parse(explicit)
		if err != nil {
			return explicit
		}
		if page > 1 {
			v := u.Query()
			v.Set("page", strconv.Itoa(page))
			u.RawQuery = v.Encode()
		}
		return u.String()
	}
	v := url.Values{}
	if q.Search != "" {
		v.Set("q", q.Search)
	}
	if fn := firstNonEmpty(req.Fn, q.Get("fn"), speedrunCraft); fn != "" {
		v.Set("fn", fn)
	}
	if sen := firstNonEmpty(req.Seniority, q.Get("sen"), q.Get("seniority"), speedrunSeniority); sen != "" {
		v.Set("sen", sen)
	}
	if emp := firstNonEmpty(req.Employment, q.Get("emp"), q.Get("employment")); emp != "" && emp != "any" {
		v.Set("emp", emp)
	}
	if req.Remote || q.Get("remote") == "true" || q.Get("remote") == "1" {
		v.Set("remote", "1")
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return base + "/jobs?" + v.Encode()
}

// flightREs keeps the expression of each job field read, compiled once.
var flightREs sync.Map

func flightString(block, key string) string {
	re, ok := flightREs.Load(key)
	if !ok {
		re, _ = flightREs.LoadOrStore(key, regexp.MustCompile(`"`+key+`":\[0,(?:null|"((?:\\.|[^"\\])*)")\]`))
	}
	m := re.(*regexp.Regexp).FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	return unescapeJSON(m[1])
}

func unescapeJSON(s string) string {
	if u, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return u
	}
	return s
}

// parseSpeedrunList reads the encoded job objects of a list page.
func parseSpeedrunList(page, base string) []speedrunRow {
	page = html.UnescapeString(page)
	var rows []speedrunRow
	for _, m := range speedrunObjectRE.FindAllStringSubmatch(page, -1) {
		row := speedrunRow{ID: m[1], Title: unescapeJSON(m[2]), Company: unescapeJSON(m[3]), URL: base + m[4]}
		row.Location = flightString(m[0], "location")
		row.Published = flightString(m[0], "published_at")
		row.Type = flightString(m[0], "employment_type")
		rows = append(rows, row)
	}
	return rows
}

type speedrunRow struct {
	ID, Title, Company, Location, Published, Type, URL string
}

// parseSpeedrunRSS reads the public feed. The item title reads
// "role at company".
func parseSpeedrunRSS(body []byte, max int) ([]speedrunRow, error) {
	var feed struct {
		Items []struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			Desc  string `xml:"description"`
			Date  string `xml:"pubDate"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	var rows []speedrunRow
	for _, it := range feed.Items {
		if len(rows) >= max {
			break
		}
		title, company := splitRoleAtCompany(strings.TrimSpace(it.Title))
		row := speedrunRow{Title: title, Company: company, URL: strings.TrimSpace(it.Link), ID: strings.TrimSpace(it.Link)}
		desc := textOf(it.Desc)
		row.Location = strings.TrimSpace(strings.SplitN(desc, "\u00b7", 2)[0])
		if t, err := time.Parse(time.RFC1123Z, strings.TrimSpace(it.Date)); err == nil {
			row.Published = t.UTC().Format(time.RFC3339)
		} else if t, err := time.Parse(time.RFC1123, strings.TrimSpace(it.Date)); err == nil {
			row.Published = t.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// splitRoleAtCompany splits "role at company" at its last " at ".
func splitRoleAtCompany(s string) (role, company string) {
	i := strings.LastIndex(strings.ToLower(s), " at ")
	if i < 0 {
		return s, ""
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+4:])
}

// readSpeedrun reads up to two list pages per query, newest first, and stops
// when a page adds no row inside the window.
func readSpeedrun(ctx context.Context, r *run) error {
	qs, err := r.queries()
	if err != nil {
		return err
	}
	if len(qs) == 0 {
		qs = []Query{{P: map[string]string{}}}
	}
	base := r.base("speedrun", speedrunBase)
	window := r.window(speedrunWindow)
	seen := map[string]bool{}
	for _, q := range qs {
		r.queryRan()
		label := q.label()
		var fresh []Posting
		failed := false
		for page := 1; page <= speedrunMaxPages; page++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			pg := r.readPage(ctx, speedrunListURL(base, q, r.req, page))
			if pg.Err != nil || pg.Res.Status >= 400 {
				if _, end := pg.settle(0); end != nil {
					return end
				}
				err := pg.Err
				if err == nil {
					err = fmt.Errorf("the list answered %d", pg.Res.Status)
				}
				// A page past the first comes only after rows were read:
				// they are kept.
				if page > 1 {
					r.partialRead(fmt.Sprintf("%s: page %d", label, page), err)
					break
				}
				r.queryFailed(label, err)
				failed = true
				break
			}
			rows := parseSpeedrunList(string(pg.Res.Body), base)
			if _, end := pg.settle(len(rows)); end != nil {
				return end
			}
			if len(rows) == 0 {
				if page == 1 && len(pg.Res.Body) > 0 {
					r.layoutChange(ReasonLayoutChanged, label)
				}
				break
			}
			got := r.speedrunPostings(rows, window, seen)
			fresh = append(fresh, got...)
			if len(got) == 0 {
				break
			}
		}
		if failed {
			continue
		}
		if q.Get("rss") == "true" {
			fresh = append(fresh, r.speedrunFeed(ctx, base, window, seen)...)
		}
		if r.req.Limit > 0 && len(fresh) > r.req.Limit {
			fresh = fresh[:r.req.Limit]
		}
		if len(fresh) == 0 {
			continue
		}
		if err := r.emit(ctx, label, fresh); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) speedrunFeed(ctx context.Context, base string, window time.Duration, seen map[string]bool) []Posting {
	res, err := r.client.Do(ctx, Request{URL: base + "/jobs.rss", NoRetry: true, Accept: "application/rss+xml,application/xml,text/xml"})
	if err != nil || res.Status != 200 {
		r.noteError("the Speedrun feed could not be read")
		return nil
	}
	rows, err := parseSpeedrunRSS(res.Body, speedrunRSSItems)
	if err != nil {
		r.noteError("the Speedrun feed is not valid XML")
		return nil
	}
	return r.speedrunPostings(rows, window, seen)
}

// speedrunPostings applies the window to a page of rows and counts what it
// drops; rows already taken this run (a feed item repeating a list row) are
// skipped without a count because they were counted once.
func (r *run) speedrunPostings(rows []speedrunRow, window time.Duration, seen map[string]bool) []Posting {
	now := r.repo.now()
	var out []Posting
	var undated, stale, incomplete int
	for _, row := range rows {
		if row.Title == "" || row.Company == "" || row.URL == "" {
			incomplete++
			continue
		}
		if seen[row.URL] {
			continue
		}
		seen[row.URL] = true
		t := parseTime(row.Published)
		switch {
		case t.IsZero():
			undated++
			continue
		case window > 0 && now.Sub(t) >= window:
			stale++
			continue
		}
		out = append(out, Posting{Company: row.Company, Title: row.Title, Location: row.Location, URL: row.URL, JobID: row.ID, PostedAt: t,
			EmploymentType: row.Type})
	}
	r.dropRows("incomplete", incomplete)
	r.dropRows("stale", stale)
	r.undatedRows(undated)
	return out
}
