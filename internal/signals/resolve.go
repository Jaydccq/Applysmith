package signals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// ResolverVersion changes when the resolver learns a new lane; a negative
// recorded under an older version is probed once more.
const ResolverVersion = 1

// Resolution bounds.
const (
	negativeTTL       = 14 * 24 * time.Hour
	searchesPerTick   = 8
	expansionsPerCo   = 5
	slugGuessesPerCo  = 3
	minGuessSlugChars = 3
	genericATS        = "generic"
)

// Identity is a company's resolved ATS identity.
type Identity struct {
	CompanyID       int64
	Status          string
	ATS             string
	Slug            string
	CareersURL      string
	Lane            string
	ResolverVersion int
	ResolvedAt      time.Time
}

// Board is the board of a resolved identity.
func (i Identity) Board() discovery.Board { return discovery.Board{ATS: i.ATS, Slug: i.Slug} }

// Identity reads the cached identity of a company.
func (r *Repo) Identity(ctx context.Context, companyID int64) (Identity, bool, error) {
	var i Identity
	var at string
	err := r.s.Read().QueryRowContext(ctx, `SELECT company_id, status, ats, slug, careers_url, lane, resolver_version, resolved_at FROM signal_ats_identities WHERE company_id = ?`, companyID).
		Scan(&i.CompanyID, &i.Status, &i.ATS, &i.Slug, &i.CareersURL, &i.Lane, &i.ResolverVersion, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return i, false, nil
	}
	i.ResolvedAt = parseTS(at)
	return i, err == nil, err
}

// PutIdentity caches an identity, replacing any earlier one.
func (r *Repo) PutIdentity(ctx context.Context, i Identity) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO signal_ats_identities (company_id, status, ats, slug, careers_url, lane, resolver_version, resolved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (company_id) DO UPDATE SET status = excluded.status, ats = excluded.ats, slug = excluded.slug, careers_url = excluded.careers_url,
			lane = excluded.lane, resolver_version = excluded.resolver_version, resolved_at = excluded.resolved_at`,
			i.CompanyID, i.Status, i.ATS, i.Slug, i.CareersURL, i.Lane, i.ResolverVersion, fmtTS(r.now()))
		return err
	})
}

// ---- inputs

var (
	urlInTextRE = lazyregexp.New(`https?://[^\s<>"')\]]+`)
	bareHostRE  = lazyregexp.New(`(?i)\b((?:[a-z0-9][a-z0-9-]*\.)+(?:com|io|ai|co|net|org|dev|app|xyz|so|tech|test))\b`)
	hrefRE      = lazyregexp.New(`(?i)href\s*=\s*["']([^"']+)["']`)
	ldJSONRE    = lazyregexp.New(`(?is)<script[^>]+type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
)

// excludedHosts are aggregator, press, social and search hosts: a domain is
// never inferred from them.
var excludedHosts = []string{
	"google.com", "bing.com", "duckduckgo.com", "yahoo.com", "linkedin.com", "twitter.com", "x.com", "facebook.com", "instagram.com",
	"youtube.com", "reddit.com", "news.ycombinator.com", "ycombinator.com", "medium.com", "substack.com", "github.com", "sec.gov",
	"crunchbase.com", "pitchbook.com", "techcrunch.com", "reuters.com", "bloomberg.com", "businesswire.com", "prnewswire.com",
	"globenewswire.com", "venturebeat.com", "forbes.com", "wsj.com", "nytimes.com", "axios.com", "theverge.com", "wired.com",
	"businessinsider.com", "cnbc.com", "fortune.com", "finsmes.com", "tech.eu", "siliconangle.com", "zdnet.com", "bit.ly", "t.co",
	"glassdoor.com", "indeed.com", "ziprecruiter.com", "wellfound.com", "angel.co",
}

func excludedHost(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, h := range excludedHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// UnwrapRedirect returns the target of an alert-redirect wrapper such as a
// search engine's /url?url= link, or the URL itself.
func UnwrapRedirect(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if strings.Contains(u.Hostname(), "google.") && u.Path == "/url" {
		for _, k := range []string{"url", "q"} {
			if t := u.Query().Get(k); strings.HasPrefix(t, "http") {
				return t
			}
		}
	}
	return raw
}

// InferDomain picks the company's website domain from its evidence: the
// unwrapped evidence URL first, then URLs and bare domains in the decoded
// bodies, never an aggregator, press, social or search host.
func InferDomain(docs []StoredDoc, canon string) string {
	var cands []string
	add := func(host string) {
		host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
		if host != "" && !excludedHost(host) {
			cands = append(cands, host)
		}
	}
	for _, d := range docs {
		if u, err := url.Parse(UnwrapRedirect(d.URL)); err == nil {
			add(u.Hostname())
		}
		text := html.UnescapeString(d.Body)
		for _, m := range urlInTextRE.FindAllString(text, -1) {
			if u, err := url.Parse(UnwrapRedirect(m)); err == nil {
				add(u.Hostname())
			}
		}
		for _, m := range bareHostRE.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
	}
	compact := strings.ReplaceAll(canon, " ", "")
	if len(compact) >= minGuessSlugChars {
		for _, c := range cands {
			if strings.Contains(strings.ReplaceAll(c, "-", ""), compact) {
				return c
			}
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// ---- careers page

type parsedPosting struct {
	ID, URL, Title, Location, Description string
	Remote                                bool
}

// ReadCareers reads a careers page: a link to a supported board, else the
// JSON-LD job postings it carries. A page with neither is unstructured and is
// never read by a model.
func ReadCareers(page, pageURL string) (board *discovery.Board, posts []parsedPosting) {
	base, _ := url.Parse(pageURL)
	for _, m := range hrefRE.FindAllStringSubmatch(page, -1) {
		ref, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil || base == nil {
			continue
		}
		if b, err := discovery.ResolveBoard(base.ResolveReference(ref).String()); err == nil {
			return &b, nil
		}
	}
	for _, m := range ldJSONRE.FindAllStringSubmatch(page, -1) {
		posts = append(posts, parseJobPostings(m[1], pageURL)...)
	}
	return nil, posts
}

func parseJobPostings(raw, pageURL string) []parsedPosting {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return nil
	}
	var out []parsedPosting
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			if g, ok := t["@graph"]; ok {
				walk(g)
			}
			if typ, _ := t["@type"].(string); typ == "JobPosting" {
				out = append(out, jobPosting(t, pageURL))
			}
		}
	}
	walk(v)
	return out
}

func jobPosting(t map[string]any, pageURL string) parsedPosting {
	str := func(k string) string { s, _ := t[k].(string); return strings.TrimSpace(s) }
	p := parsedPosting{Title: str("title"), URL: str("url"), Description: cleanSpace(stripTags(str("description")))}
	if p.URL == "" {
		p.URL = pageURL
	}
	if id, ok := t["identifier"].(map[string]any); ok {
		p.ID, _ = id["value"].(string)
	} else if s, ok := t["identifier"].(string); ok {
		p.ID = s
	}
	if p.ID == "" {
		p.ID = p.URL + "#" + p.Title
	}
	if loc, _ := t["jobLocationType"].(string); strings.EqualFold(loc, "TELECOMMUTE") {
		p.Remote = true
	}
	var parts []string
	var locs []any
	switch l := t["jobLocation"].(type) {
	case []any:
		locs = l
	case map[string]any:
		locs = []any{l}
	}
	for _, l := range locs {
		if lm, ok := l.(map[string]any); ok {
			if a, ok := lm["address"].(map[string]any); ok {
				for _, k := range []string{"addressLocality", "addressRegion", "addressCountry"} {
					if s, ok := a[k].(string); ok && s != "" {
						parts = append(parts, s)
					}
				}
			}
		}
	}
	p.Location = strings.Join(parts, ", ")
	return p
}

func (s *Service) getPage(ctx context.Context, u string) (string, bool) {
	res, err := s.fetch.Do(ctx, Request{URL: u, Gap: gapRSS, Header: http.Header{"Accept": {"text/html"}}})
	if err != nil || res.Status != http.StatusOK {
		return "", false
	}
	return string(res.Body), true
}

// ---- resolution

type resolveInput struct {
	Company  Company
	Settings Settings
	Docs     []StoredDoc
	// Searches is the number of name-keyed searches left this tick.
	Searches *int
}

// Resolution is the outcome of one resolve; Ran counts the lanes that did
// work, and a resolution that ran none is not cached.
type Resolution struct {
	Identity Identity
	Ran      int
}

func slugGuesses(c Company) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if len(s) >= minGuessSlugChars && discovery.ValidSlug(s) && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	words := strings.Fields(c.Canon)
	add(strings.Join(words, ""))
	add(strings.Join(words, "-"))
	full := strings.Fields(strings.ToLower(nonWordRE.ReplaceAllString(c.Name, " ")))
	add(strings.Join(full, ""))
	add(strings.Join(full, "-"))
	if len(out) > slugGuessesPerCo {
		out = out[:slugGuessesPerCo]
	}
	return out
}

func boardMatches(posts []discovery.Posting, canon string) bool {
	any := false
	for _, p := range posts {
		if p.Company == "" {
			continue
		}
		any = true
		pc := RuleCanon(p.Company)
		if pc == canon || strings.Contains(strings.ReplaceAll(pc, " ", ""), strings.ReplaceAll(canon, " ", "")) {
			return true
		}
	}
	return !any
}

// override finds the user's explicit board or careers URL for the company in
// portals.yml.
func override(p workspace.Portals, c Company) (ats, slug, careers string) {
	for _, e := range p.Companies {
		if RuleCanon(e.Name) != c.Canon {
			continue
		}
		return strings.ToLower(e.ATS), strings.ToLower(e.Slug), e.CareersURL
	}
	return "", "", ""
}

func (s *Service) discoveryClient() *discovery.Client {
	base := &discovery.Client{HTTP: s.fetch.HTTP, MinGap: time.Millisecond, HostConcurrency: 4, Sleep: s.fetch.Sleep, Now: s.fetch.Now}
	return base.Session(0)
}

// Resolve finds a company's ATS identity. Lanes run in order and stop at the
// first success: the user's override, the board links in the signal's own
// text (shorteners expanded, at most 5 per company), the company website and
// its careers page, slug guesses against the five ATS APIs, and a name-keyed
// search.
func (s *Service) Resolve(ctx context.Context, in resolveInput) (Resolution, error) {
	c := in.Company
	res := Resolution{Identity: Identity{CompanyID: c.ID, ResolverVersion: ResolverVersion}}
	found := func(b discovery.Board, lane string) Resolution {
		res.Identity.Status, res.Identity.ATS, res.Identity.Slug, res.Identity.Lane = IdentityResolved, b.ATS, b.Slug, lane
		return res
	}
	ovATS, ovSlug, ovCareers := override(in.Settings.Portals, c)
	if ovATS != "" && ovSlug != "" {
		res.Ran++
		return found(discovery.Board{ATS: ovATS, Slug: ovSlug}, "override"), nil
	}

	// The signal's own text, before any guessed URL is probed.
	exp := &discovery.Expander{HTTP: s.fetch.HTTP, Limit: expansionsPerCo}
	harvested := false
	for _, d := range in.Docs {
		for _, raw := range urlInTextRE.FindAllString(html.UnescapeString(d.Title+"\n"+d.Body), -1) {
			raw = strings.TrimRight(raw, ".,;:")
			harvested = true
			if discovery.LinkKind(raw) == discovery.LinkShortener {
				final, ok := exp.Expand(ctx, raw)
				if !ok {
					continue
				}
				raw = final
			}
			if b, err := discovery.ResolveBoard(raw); err == nil {
				res.Ran++
				return found(b, "evidence-link"), nil
			}
		}
	}
	if harvested {
		res.Ran++
	}

	// The company website.
	domain := InferDomain(in.Docs, c.Canon)
	var tries []string
	if ovCareers != "" {
		tries = append(tries, ovCareers)
	}
	if domain != "" {
		for _, p := range []string{"/careers", "/jobs", "/"} {
			tries = append(tries, "https://"+domain+p)
		}
	}
	reached := ""
	for i, u := range tries {
		if i == 0 {
			res.Ran++
		}
		page, ok := s.getPage(ctx, u)
		if !ok {
			continue
		}
		if reached == "" {
			reached = u
		}
		board, posts := ReadCareers(page, u)
		if board != nil {
			res.Identity.CareersURL = u
			return found(*board, "careers-link"), nil
		}
		if len(posts) > 0 {
			res.Identity.CareersURL = u
			return found(discovery.Board{ATS: genericATS, Slug: u}, "careers-jsonld"), nil
		}
	}
	res.Identity.CareersURL = reached

	// Slug guesses against the five public ATS APIs.
	if guesses := slugGuesses(c); len(guesses) > 0 {
		res.Ran++
		client := s.discoveryClient()
		for _, g := range guesses {
			for _, ats := range discovery.ATSNames {
				b := discovery.Board{ATS: ats, Slug: g}
				br, err := client.FetchBoard(ctx, s.d.Endpoints.ATS, b, "")
				if ctx.Err() != nil {
					return res, ctx.Err()
				}
				if err == nil && len(br.Postings) > 0 && boardMatches(br.Postings, c.Canon) {
					return found(b, "slug-guess"), nil
				}
			}
		}
	}

	// A name-keyed search, within the per-tick budget.
	if prov := s.searchProvider(); prov != nil && in.Searches != nil && *in.Searches > 0 {
		*in.Searches--
		res.Ran++
		hits, err := prov.Search(ctx, c.Name+" careers jobs", 5, Scope{})
		if err == nil {
			for _, h := range hits {
				if b, err := discovery.ResolveBoard(h.URL); err == nil {
					return found(b, "search"), nil
				}
			}
		}
	}

	if reached != "" {
		res.Identity.Status = IdentityNoATS
	} else {
		res.Identity.Status = IdentityUnresolved
	}
	return res, nil
}

// needsResolve applies the cache rules: a resolved identity is never probed
// again; a negative is probed again once after the resolver learned something
// new and when it is older than 14 days.
func needsResolve(i Identity, have bool, now time.Time) bool {
	switch {
	case !have:
		return true
	case i.Status == IdentityResolved:
		return false
	case i.ResolverVersion < ResolverVersion:
		return true
	}
	return now.Sub(i.ResolvedAt) > negativeTTL
}

// ensureIdentity returns the identity to check a company with, resolving it
// when the cache rules ask for it. The user's override replaces a cached
// identity that differs.
func (s *Service) ensureIdentity(ctx context.Context, c Company, st Settings, searches *int) (Identity, error) {
	cached, have, err := s.repo.Identity(ctx, c.ID)
	if err != nil {
		return Identity{}, err
	}
	if ovATS, ovSlug, _ := override(st.Portals, c); ovATS != "" && ovSlug != "" {
		if !have || cached.Status != IdentityResolved || cached.ATS != ovATS || cached.Slug != ovSlug {
			cached = Identity{CompanyID: c.ID, Status: IdentityResolved, ATS: ovATS, Slug: ovSlug, Lane: "override", ResolverVersion: ResolverVersion}
			return cached, s.repo.PutIdentity(ctx, cached)
		}
		return cached, nil
	}
	if !needsResolve(cached, have, s.now()) {
		return cached, nil
	}
	docs, err := s.repo.CompanyDocs(ctx, c.ID, 5)
	if err != nil {
		return Identity{}, err
	}
	res, err := s.Resolve(ctx, resolveInput{Company: c, Settings: st, Docs: docs, Searches: searches})
	if err != nil {
		return Identity{}, err
	}
	if res.Ran == 0 && res.Identity.Status != IdentityResolved {
		// No lane ran (the search budget was spent): nothing is cached, so a
		// later tick tries again.
		if have {
			return cached, nil
		}
		return res.Identity, nil
	}
	return res.Identity, s.repo.PutIdentity(ctx, res.Identity)
}

// CompanyDocs lists the newest documents attached to the company's events.
func (r *Repo) CompanyDocs(ctx context.Context, companyID int64, limit int) ([]StoredDoc, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+docColumns+` FROM signal_documents WHERE id IN
		(SELECT ed.document_id FROM signal_event_docs ed WHERE ed.company_id = ?) ORDER BY id DESC LIMIT ?`, companyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredDoc
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
