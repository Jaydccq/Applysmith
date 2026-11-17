package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// The two employer-board APIs that are not per-company ATS boards.
const (
	amazonBase     = "https://www.amazon.jobs"
	amazonMaxHits  = 100
	a16zBase       = "https://jobs.a16z.com"
	a16zTimeout    = 10 * time.Second
	a16zPageSize   = 25
	a16zMaxPages   = 5
	amazonSiteName = "Amazon"
)

// readAmazon reads the public Amazon jobs search for each saved query, at most
// 100 results per query per run.
func readAmazon(ctx context.Context, r *run) error {
	qs, err := r.queriesOrSkip()
	if err != nil {
		return err
	}
	base := r.base("amazon", amazonBase)
	for _, q := range qs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.queryRan()
		label := q.label()
		n := amazonMaxHits
		if r.req.Limit > 0 {
			n = min(n, r.req.Limit)
		}
		v := url.Values{"result_limit": {strconv.Itoa(n)}, "offset": {"0"}, "sort": {"recent"}}
		if q.Search != "" {
			v.Set("base_query", q.Search)
		}
		if loc := firstNonEmpty(q.Get("location"), q.Get("city")); loc != "" {
			v.Set("loc_query", loc)
		}
		res, err := r.client.Do(ctx, Request{URL: base + "/en/search.json?" + v.Encode()})
		if err != nil {
			r.queryFailed(label, err)
			continue
		}
		posts, err := parseAmazon(res, base, n)
		if err != nil {
			r.queryFailed(label, err)
			if isLayout(err) {
				r.layoutChange(ReasonLayoutChanged, label)
			}
			continue
		}
		if err := r.emit(ctx, label, posts); err != nil {
			return err
		}
	}
	return nil
}

// errLayout marks a response that had content and no parsable rows.
type errLayout struct{ what string }

func (e errLayout) Error() string { return e.what }

func isLayout(err error) bool { _, ok := err.(errLayout); return ok }

func parseAmazon(res Response, base string, limit int) ([]Posting, error) {
	if res.Status != http.StatusOK {
		return nil, fmt.Errorf("the search answered %d", res.Status)
	}
	var doc struct {
		Hits json.Number `json:"hits"`
		Jobs *[]struct {
			ID         string `json:"id_icims"`
			Title      string `json:"title"`
			Company    string `json:"company_name"`
			Path       string `json:"job_path"`
			Location   string `json:"location"`
			Normalized string `json:"normalized_location"`
			Posted     string `json:"posted_date"`
			Desc       string `json:"description"`
			Basic      string `json:"basic_qualifications"`
			Preferred  string `json:"preferred_qualifications"`
			Schedule   string `json:"job_schedule_type"`
			Category   string `json:"job_category"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil || doc.Jobs == nil {
		return nil, errLayout{"the response is not an Amazon jobs listing"}
	}
	hits, _ := doc.Hits.Int64()
	if len(*doc.Jobs) == 0 && hits > 0 {
		return nil, errLayout{"the listing reports hits but carries no jobs"}
	}
	var out []Posting
	for _, j := range *doc.Jobs {
		if j.Title == "" || j.Path == "" {
			continue
		}
		p := Posting{
			Company: firstNonEmpty(j.Company, amazonSiteName), Title: j.Title, URL: base + "/" + strings.TrimLeft(j.Path, "/"),
			Location: firstNonEmpty(j.Normalized, j.Location), JobID: j.ID, EmploymentType: j.Schedule, Department: j.Category,
		}
		if t, err := time.Parse("January 2, 2006", strings.Join(strings.Fields(j.Posted), " ")); err == nil {
			p.PostedAt = t
		}
		p.Description = truncateRunes(plainText(strings.Join(nonEmpty([]string{j.Desc, j.Basic, j.Preferred}), "\n\n")), maxDescription)
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// readA16Z reads the current public Next.js listing and its own load-more
// action. The action id is discovered from that deployment's scripts, never
// pinned across deployments. The removed Getro API is not retried.
func readA16Z(ctx context.Context, r *run) error {
	base := strings.TrimRight(r.base("a16z", a16zBase), "/")
	r.queryRan()
	home, err := r.client.Do(ctx, Request{URL: base + "/", Timeout: a16zTimeout})
	if err != nil {
		r.queryFailed("portfolio", err)
		return nil
	}
	page, err := parseA16Z(home)
	if err != nil {
		r.queryFailed("portfolio", err)
		if isLayout(err) {
			r.layoutChange(ReasonLayoutChanged, "portfolio")
		}
		return nil
	}
	all := []Posting{}
	seen := map[string]bool{}
	action := ""
	pages := a16zMaxPages
	if r.req.Pages > 0 {
		pages = min(pages, r.req.Pages)
	}
	for n := 0; n < pages; n++ {
		fresh := 0
		for _, p := range page.postings() {
			if !seen[p.URL] {
				seen[p.URL] = true
				all = append(all, p)
				fresh++
			}
		}
		if page.Done || fresh == 0 || (r.req.Limit > 0 && len(all) >= r.req.Limit) || n+1 >= pages {
			break
		}
		if action == "" {
			action, err = a16zAction(ctx, r, base, string(home.Body))
			if err != nil {
				r.queryFailed("portfolio pagination", err)
				break
			}
		}
		body, _ := json.Marshal([]any{map[string]any{"sort": "recent"}, map[string]any{"page": n + 1, "limit": a16zPageSize}})
		res, e := r.client.Do(ctx, Request{Method: http.MethodPost, URL: base + "/", Body: body, Header: http.Header{"Content-Type": {"text/plain;charset=UTF-8"}, "Next-Action": {action}}, Timeout: a16zTimeout})
		if e != nil {
			r.queryFailed("portfolio pagination", e)
			break
		}
		page, err = parseA16Z(res)
		if err != nil {
			r.queryFailed("portfolio pagination", err)
			if isLayout(err) {
				r.layoutChange(ReasonLayoutChanged, "portfolio pagination")
			}
			break
		}
		if page.Number != n+1 {
			r.queryFailed("portfolio pagination", errLayout{"the board returned a different page"})
			break
		}
	}
	if r.req.Limit > 0 && len(all) > r.req.Limit {
		all = all[:r.req.Limit]
	}
	return r.emit(ctx, "portfolio", all)
}

type a16zPage struct {
	Jobs *[]struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		URL         string `json:"apply_url"`
		Company     string `json:"company_name"`
		Location    string `json:"location"`
		Remote      bool   `json:"remote"`
		Posted      string `json:"posted_at"`
		Description string `json:"description_html"`
		Employment  string `json:"employment_type"`
		Department  string `json:"department"`
	} `json:"jobs"`
	Total  int  `json:"total"`
	Number int  `json:"page"`
	Done   bool `json:"isDone"`
}

func (d a16zPage) postings() []Posting {
	var out []Posting
	for _, j := range *d.Jobs {
		u, err := url.Parse(j.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || strings.TrimSpace(j.Title) == "" || strings.TrimSpace(j.Company) == "" {
			continue
		}
		out = append(out, Posting{Company: j.Company, Title: j.Title, URL: j.URL, JobID: j.ID, Location: cleanLocations(strings.Split(strings.ReplaceAll(j.Location, " · ", ";"), ";")), Remote: j.Remote, PostedAt: parseTime(j.Posted), EmploymentType: j.Employment, Department: j.Department, Description: truncateRunes(plainText(j.Description), maxDescription)})
	}
	return out
}

var a16zFlightRE = lazyregexp.New(`self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)`)
var a16zScriptRE = lazyregexp.New(`<script[^>]+src="([^"]+)"`)
var a16zActionRE = lazyregexp.New(`createServerReference\)\("([a-f0-9]{40,64})"[^;]{0,200}"loadMorePublicJobs"`)

func parseA16Z(res Response) (a16zPage, error) {
	if res.Status != http.StatusOK {
		return a16zPage{}, fmt.Errorf("the board answered %d", res.Status)
	}
	raw := string(res.Body)
	if strings.Contains(raw, "<script") {
		var chunks strings.Builder
		for _, m := range a16zFlightRE.FindAllStringSubmatch(raw, -1) {
			var text string
			if json.Unmarshal([]byte(m[1]), &text) == nil {
				chunks.WriteString(text)
			}
		}
		raw = chunks.String()
	}
	// First page is a component's initialData; load-more is a Flight JSON row.
	candidates := []string{}
	if i := strings.Index(raw, `"initialData":`); i >= 0 {
		candidates = append(candidates, raw[i+len(`"initialData":`):])
	}
	for _, line := range strings.Split(raw, "\n") {
		if _, v, ok := strings.Cut(line, ":"); ok {
			candidates = append(candidates, v)
		}
	}
	for _, candidate := range candidates {
		var doc a16zPage
		if json.NewDecoder(bytes.NewBufferString(candidate)).Decode(&doc) != nil || doc.Jobs == nil {
			continue
		}
		if (len(*doc.Jobs) == 0 && !doc.Done) || (len(*doc.Jobs) > 0 && len(doc.postings()) == 0) {
			return a16zPage{}, errLayout{"the board reports jobs but carries no usable postings"}
		}
		return doc, nil
	}
	return a16zPage{}, errLayout{"the public listing state is missing; the board layout changed"}
}

func a16zAction(ctx context.Context, r *run, base, home string) (string, error) {
	for i, m := range a16zScriptRE.FindAllStringSubmatch(home, -1) {
		if i >= 16 {
			break
		}
		// Only deployment scripts on the same public board are fetched.
		if !strings.HasPrefix(m[1], "/_next/static/chunks/") || strings.Contains(m[1], "..") || strings.ContainsAny(m[1], "?#\\") {
			continue
		}
		res, err := r.client.Do(ctx, Request{URL: base + m[1], Timeout: a16zTimeout})
		if err != nil || res.Status != 200 {
			continue
		}
		if match := a16zActionRE.FindStringSubmatch(string(res.Body)); match != nil {
			return match[1], nil
		}
	}
	return "", errLayout{"the public load-more action is missing; the board layout changed"}
}
