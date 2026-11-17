package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/kb"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/signals"
)

// mirrorSources wires mirror resolution to the guarded page fetcher, the ATS
// registry, public boards, recorded Workday sites and configured web search.
func (s *Server) mirrorSources(d *domain) *apply.MirrorSources {
	fetch := kb.NewGuardedFetcher()
	client := discovery.NewClient()
	if s.cfg.Domain.ScanHTTP != nil {
		client.HTTP = s.cfg.Domain.ScanHTTP
		client = client.Session(0)
	}
	return &apply.MirrorSources{
		Page: func(ctx context.Context, raw string) (string, int) {
			p, err := fetch.Fetch(ctx, raw)
			if err != nil {
				return "", 0
			}
			if p.Status < 200 || p.Status > 299 {
				return "", p.Status
			}
			return string(p.Body), p.Status
		},
		Registry: func(ctx context.Context, company string) []discovery.Board {
			rows, err := d.repo.RegistryActive(ctx)
			if err != nil {
				return nil
			}
			var out []discovery.Board
			for _, r := range rows {
				if apply.SameCompany(strings.ReplaceAll(firstNonBlank(r.Company, r.Slug), "-", " "), company) {
					out = append(out, r.Board)
				}
			}
			return out
		},
		BoardName: func(ctx context.Context, slug string) string {
			base := firstNonBlank(s.cfg.Domain.ScanEndpoints["greenhouse"], "https://boards-api.greenhouse.io")
			res, err := client.Do(ctx, discovery.Request{URL: strings.TrimRight(base, "/") + "/v1/boards/" + url.PathEscape(slug), Timeout: 10 * time.Second})
			if err != nil || res.Status != 200 {
				return ""
			}
			var board struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(res.Body, &board) != nil {
				return ""
			}
			return board.Name
		},
		Workday: func(ctx context.Context, company, label string, titles, sites []string) []apply.BoardMatch {
			return s.mirrorWorkday(ctx, client, company, label, titles, sites)
		},
		Search: func(ctx context.Context, query string, include, exclude []string) ([]apply.WebHit, error) {
			if d.signals == nil {
				return nil, apply.ErrNoWebSearch
			}
			search := d.signals.SearchProvider()
			if search == nil {
				return nil, apply.ErrNoWebSearch
			}
			hits, err := search.Search(ctx, query, 10, signals.Scope{Include: include, Exclude: exclude})
			var out []apply.WebHit
			for _, h := range hits {
				u, e := url.Parse(h.URL)
				if e != nil {
					continue
				}
				blocked := false
				for _, host := range exclude {
					if strings.EqualFold(u.Hostname(), host) || strings.HasSuffix(strings.ToLower(u.Hostname()), "."+strings.ToLower(host)) {
						blocked = true
						break
					}
				}
				if !blocked {
					out = append(out, apply.WebHit{Title: h.Title, URL: h.URL})
				}
			}
			return out, err
		},
		Board: func(ctx context.Context, b discovery.Board) ([]discovery.Posting, error) {
			res, err := client.FetchBoard(ctx, s.cfg.Domain.ScanEndpoints, b, "")
			return res.Postings, err
		},
		Location: func(ctx context.Context, raw string) string {
			key, stop := apply.IdentityKey(raw)
			if stop != nil {
				return ""
			}
			loc, _ := d.repo.InboxLocation(ctx, key)
			return loc
		},
	}
}

// workdayPostingSuffixRE is the -N Workday adds to each further posting of a
// requisition ("R55738-2"); an id without a digit before the dash ("R-12345")
// keeps it.
var workdayPostingSuffixRE = lazyregexp.New(`^(.*\d)-\d{1,2}$`)

// workdayRequisition keys a Workday job path by its title and requisition:
// one requisition posted on two of a tenant's career sites (Cadence's
// Univ_Careers and University_Talent, "_R55738-1" and "_R55738-2") is one
// posting, wherever its location segment points.
func workdayRequisition(jobPath string) string {
	seg := jobPath[strings.LastIndex(jobPath, "/")+1:]
	i := strings.LastIndex(seg, "_")
	if i < 0 {
		return jobPath
	}
	req := seg[i+1:]
	if m := workdayPostingSuffixRE.FindStringSubmatch(req); m != nil {
		req = m[1]
	}
	return strings.ToLower(seg[:i]) + "_" + req
}

func firstNonBlank(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// mirrorWorkday searches only previously observed or explicitly configured
// career sites, and the sites of the pages in sites (the web search's hits on
// the company's own host). It never guesses a tenant or trusts the
// aggregator's prose.
func (s *Server) mirrorWorkday(ctx context.Context, client *discovery.Client, company, label string, titles, sites []string) []apply.BoardMatch {
	type candidate struct{ name, raw string }
	var candidates []candidate
	for _, raw := range sites {
		candidates = append(candidates, candidate{company, raw})
	}
	st, err := s.db()
	if err != nil {
		return nil
	}
	rows, err := st.Read().QueryContext(ctx, `SELECT company,url FROM inbox WHERE url LIKE '%myworkdayjobs.com/%' UNION SELECT company,posting_url FROM tracker_entries WHERE posting_url LIKE '%myworkdayjobs.com/%'`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.name, &c.raw) == nil {
			candidates = append(candidates, c)
		}
	}
	rows.Close()
	if portals, err := loadPortals(s.cfg.Workspace); err == nil {
		for _, c := range portals.Companies {
			if c.IsEnabled() {
				candidates = append(candidates, candidate{c.Name, c.CareersURL})
			}
		}
	}
	var out []apply.BoardMatch
	seen := map[string]bool{}
	for _, c := range candidates {
		u, err := url.Parse(c.raw)
		if err != nil || u.Scheme != "https" || u.User != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		parts := strings.Split(host, ".")
		if len(parts) != 4 || parts[2] != "myworkdayjobs" || parts[3] != "com" {
			continue
		}
		tenant := parts[0]
		if !apply.SameCompany(c.name, company) && !apply.SameCompany(tenant, company) && (label == "" || !strings.EqualFold(label, tenant)) {
			continue
		}
		segments := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segments) > 0 && len(segments[0]) == 5 && segments[0][2] == '-' {
			segments = segments[1:]
		}
		if len(segments) == 0 || !discovery.ValidSlug(segments[0]) || segments[0] == "job" {
			continue
		}
		site := segments[0]
		key := host + "/" + site
		if seen[key] || len(seen) >= 10 {
			continue
		}
		seen[key] = true
		base := firstNonBlank(s.cfg.Domain.ScanEndpoints["workday"], "https://"+host)
		for _, title := range titles {
			body, _ := json.Marshal(map[string]any{"appliedFacets": map[string]any{}, "limit": 20, "offset": 0, "searchText": title})
			res, err := client.Do(ctx, discovery.Request{Method: http.MethodPost, URL: strings.TrimRight(base, "/") + "/wday/cxs/" + tenant + "/" + site + "/jobs", Body: body, Header: http.Header{"Content-Type": {"application/json"}}, Timeout: 10 * time.Second})
			if err != nil || res.Status != 200 {
				continue
			}
			var doc struct {
				Total int `json:"total"`
				Jobs  []struct {
					Title    string `json:"title"`
					Path     string `json:"externalPath"`
					Location string `json:"locationsText"`
				} `json:"jobPostings"`
			}
			if json.Unmarshal(res.Body, &doc) != nil || doc.Total > len(doc.Jobs) {
				continue
			} // Incomplete results cannot prove uniqueness.
			for _, j := range doc.Jobs {
				if !apply.MatchesMirrorTitle(titles, j.Title, company) || !strings.HasPrefix(j.Path, "/job/") || strings.ContainsAny(j.Path, "?#\\") {
					continue
				}
				decoded, err := url.PathUnescape(j.Path)
				if err != nil || path.Clean(decoded) != decoded {
					continue
				}
				link := "https://" + host + "/" + site + j.Path
				out = append(out, apply.BoardMatch{URL: link, Key: host + "/" + workdayRequisition(j.Path), Location: j.Location, Via: "workday-search"})
			}
		}
	}
	return out
}
