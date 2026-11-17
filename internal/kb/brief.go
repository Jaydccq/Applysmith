package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/lazyregexp"
)

// Company brief limits.
const (
	briefTextLimit   = 4000
	briefMinBody     = 200
	briefLifetime    = 90 * 24 * time.Hour
	briefMissMemo    = 24 * time.Hour
	declaredMemo     = 30 * time.Minute
	minRepeatedChars = 25
)

// Brief failures. ErrNoSite and ErrNoBrief are normal outcomes, not faults.
var (
	ErrNoSite  = errors.New("kb: the posting declares no company website")
	ErrNoBrief = errors.New("kb: no usable company page")
)

// BriefRequest names the company and the two pages that may declare its site.
type BriefRequest struct {
	Company        string
	ApplicationURL string
	JobURL         string
	// Refresh rebuilds a brief that has not expired. A brief the user wrote or
	// edited is never rebuilt.
	Refresh bool
	// Unsummarized keeps a new brief's page text as extracted, without the
	// summarizer: the caller condenses it itself under its own deadline.
	Unsummarized bool
}

// Brief is a cached company brief.
type Brief struct {
	Domain    string    `json:"domain"`
	Path      string    `json:"path"`
	SourceURL string    `json:"sourceUrl,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Manual    bool      `json:"manual"`
	Text      string    `json:"text"`
	// Task is the model task that condensed Text in this call; empty when
	// Text is the site's own words.
	Task string `json:"task,omitempty"`
}

// genericNames are page words that are never a company name.
var genericNames = setOf("jobs", "job", "careers", "career", "embed", "companies", "company", "apply", "hiring", "work", "unknown")

// notCompanySites are job boards, application systems, social profiles and
// directories. A page on one of them never declares the company's own site.
var notCompanySites = []string{
	"greenhouse.io", "lever.co", "ashbyhq.com", "myworkdayjobs.com", "workday.com", "smartrecruiters.com", "workable.com",
	"icims.com", "taleo.net", "jobvite.com", "bamboohr.com", "recruitee.com", "breezy.hr", "teamtailor.com",
	"successfactors.com", "successfactors.eu", "oraclecloud.com", "paylocity.com", "paycomonline.net", "ultipro.com",
	"dayforcehcm.com", "applytojob.com", "jazz.co", "rippling.com", "gem.com", "pinpointhq.com", "comeet.com", "personio.de",
	"linkedin.com", "indeed.com", "glassdoor.com", "ziprecruiter.com", "monster.com", "wellfound.com", "angel.co",
	"builtin.com", "dice.com", "simplyhired.com", "levels.fyi", "jobright.ai",
	"facebook.com", "twitter.com", "x.com", "instagram.com", "youtube.com", "tiktok.com", "github.com", "gitlab.com",
	"crunchbase.com", "wikipedia.org", "bloomberg.com", "zoominfo.com", "pitchbook.com", "dnb.com", "medium.com",
	"linktr.ee", "g2.com", "owler.com",
}

// placeholderHosts stand in for a real site in templates and test data.
var placeholderHosts = setOf("company.com", "yourcompany.com", "yourdomain.com", "domain.com", "website.com", "companyname.com", "sample.com", "test.com")

func hostMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// NotCompanySite reports a host on a job board, an application system, a social
// profile or a directory: a site that never stands for the employer itself,
// matched by domain suffix.
func NotCompanySite(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, d := range notCompanySites {
		if hostMatches(host, d) {
			return true
		}
	}
	return false
}

// acceptSite normalizes a declared URL into the site's origin, or reports why
// it can never be a company's own site. A company may host its careers page on
// its own domain, so the host of the page that declared the site is not
// compared; the job-board and ATS list does that work.
func acceptSite(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || !strings.Contains(host, ".") || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".invalid") || strings.HasSuffix(host, ".internal") {
		return nil, false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, false
	}
	if placeholderHosts[strings.TrimPrefix(host, "www.")] {
		return nil, false
	}
	for _, d := range notCompanySites {
		if hostMatches(host, d) {
			return nil, false
		}
	}
	return &url.URL{Scheme: u.Scheme, Host: host}, true
}

var ldJSONRE = lazyregexp.New(`(?is)<script[^>]*type\s*=\s*["']?application/ld\+json["']?[^>]*>(.*?)</script>`)

// publicWebsiteRE finds the public-website field an applicant-tracking system
// embeds in its page data.
var publicWebsiteRE = lazyregexp.New(`"publicWebsite"\s*:\s*"([^"\\]+(?:\\/[^"\\]*)*)"`)

// declaredCandidates lists, in order, the website URLs a page declares for the
// hiring organization: its structured data's url first, then its sameAs, and
// last the public-website field of the page's embedded data.
func declaredCandidates(body []byte) []string {
	var out []string
	for _, m := range ldJSONRE.FindAllSubmatch(body, -1) {
		// Script content is raw text, so entities in it are data; a block
		// escaped as a whole is read unescaped only when it does not parse.
		var v any
		block := strings.TrimSpace(string(m[1]))
		if err := json.Unmarshal([]byte(block), &v); err != nil {
			if err := json.Unmarshal([]byte(html.UnescapeString(block)), &v); err != nil {
				continue
			}
		}
		walkLD(v, &out)
	}
	for _, m := range publicWebsiteRE.FindAllSubmatch(body, -1) {
		out = append(out, strings.ReplaceAll(string(m[1]), `\/`, "/"))
	}
	return out
}

func walkLD(v any, out *[]string) {
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			walkLD(x, out)
		}
	case map[string]any:
		if org, ok := t["hiringOrganization"]; ok {
			collectOrg(org, out)
		}
		for _, k := range []string{"@graph", "mainEntity", "itemListElement"} {
			if sub, ok := t[k]; ok {
				walkLD(sub, out)
			}
		}
	}
}

func collectOrg(org any, out *[]string) {
	switch o := org.(type) {
	case []any:
		for _, x := range o {
			collectOrg(x, out)
		}
	case map[string]any:
		if u, ok := o["url"].(string); ok {
			*out = append(*out, u)
		}
		switch sa := o["sameAs"].(type) {
		case string:
			*out = append(*out, sa)
		case []any:
			for _, x := range sa {
				if str, ok := x.(string); ok {
					*out = append(*out, str)
				}
			}
		}
	}
}

// pageDeclaredSite fetches one page and returns the site it declares, or nil
// when it declares none. What a page declared, a site or nothing, is
// remembered for 30 minutes; a failed fetch is not remembered. Concurrent
// callers share a fetch.
func (s *Service) pageDeclaredSite(ctx context.Context, pageURL string) (*url.URL, error) {
	if pageURL == "" {
		return nil, nil
	}
	if site, ok := s.memo.lookup(pageURL, s.now()); ok {
		return site, nil
	}
	v, err := s.flights.Do(ctx, pageURL, func(ctx context.Context) (any, error) {
		page, err := s.opts.Fetcher.Fetch(ctx, pageURL)
		if err != nil {
			return nil, err
		}
		var site *url.URL
		for _, c := range declaredCandidates(page.Body) {
			if u, ok := acceptSite(c); ok {
				site = u
				break
			}
		}
		s.memo.remember(pageURL, site, s.now(), s.now().Add(declaredMemo))
		return site, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*url.URL), nil
}

// DeclaredSite reads the company's site from the application page first and
// then from the job page, both requested at once. The first that declares one
// wins. A name is never turned into a URL.
func (s *Service) DeclaredSite(ctx context.Context, req BriefRequest) (*url.URL, error) {
	if s.opts.Fetcher == nil {
		return nil, fmt.Errorf("%w: no page fetcher is configured", ErrUnavailable)
	}
	if genericNames[strings.ToLower(strings.TrimSpace(req.Company))] || strings.TrimSpace(req.Company) == "" {
		return nil, fmt.Errorf("%w: %q is not a company name", ErrInvalid, req.Company)
	}
	pages := []string{req.ApplicationURL, req.JobURL}
	sites := make([]*url.URL, len(pages))
	errs := make([]error, len(pages))
	var wg sync.WaitGroup
	for i, p := range pages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sites[i], errs[i] = s.pageDeclaredSite(ctx, p)
		}()
	}
	wg.Wait()
	for i := range pages {
		if sites[i] != nil {
			return sites[i], nil
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return nil, ErrNoSite
}

// EnsureBrief returns the company's brief, building it from the declared site
// when none is cached, when the cached one has expired, and never over a brief
// the user wrote or edited. An expired brief whose refetch finds nothing is left
// byte-for-byte as it was and is not returned.
func (s *Service) EnsureBrief(ctx context.Context, req BriefRequest) (*Brief, error) {
	site, err := s.DeclaredSite(ctx, req)
	if err != nil {
		return nil, err
	}
	domain := strings.TrimPrefix(site.Hostname(), "www.")
	if !safeDomain(domain) {
		return nil, fmt.Errorf("%w: %q is not a usable domain", ErrInvalid, domain)
	}
	rel := KnowledgeDir + "/" + CompaniesDir + "/" + domain + ".md"
	state, err := s.briefState(ctx, domain)
	if err != nil {
		return nil, err
	}
	existing, err := s.readBrief(rel, state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s cannot be read; fix or delete it: %v", ErrConflict, rel, err)
	}
	if existing != nil && (existing.Manual || (!req.Refresh && s.now().Before(existing.ExpiresAt))) {
		return existing, nil
	}
	if t, err := time.Parse(time.RFC3339, state.missUntil); err == nil && s.now().Before(t) {
		return nil, ErrNoBrief
	}
	text, from, err := s.fetchSiteText(ctx, site)
	if err != nil {
		if errors.Is(err, ErrNoBrief) && ctx.Err() == nil {
			if merr := s.recordMiss(ctx, domain); merr != nil {
				return nil, merr
			}
		}
		return nil, err
	}
	task := ""
	if s.opts.Summarizer != nil && !req.Unsummarized {
		if sum, err := s.opts.Summarizer.Summarize(ctx, text); err == nil && strings.TrimSpace(sum) != "" {
			text, task = clip(strings.TrimSpace(sum), briefTextLimit), BriefModelTask
		}
	}
	b, err := s.writeBrief(ctx, req.Company, domain, rel, from, text)
	if err != nil {
		return nil, err
	}
	b.Task = task
	return b, nil
}

func safeDomain(d string) bool {
	if d == "" || strings.HasPrefix(d, ".") || strings.Contains(d, "..") {
		return false
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// fetchSiteText tries the site's root, then /about, then the www. host, and
// returns the text of the first page that is HTML or plain text with a body of
// at least 200 characters.
func (s *Service) fetchSiteText(ctx context.Context, site *url.URL) (text, from string, err error) {
	cands := []string{site.Scheme + "://" + site.Host + "/", site.Scheme + "://" + site.Host + "/about"}
	if !strings.HasPrefix(site.Host, "www.") {
		cands = append(cands, site.Scheme+"://www."+site.Host+"/")
	}
	for _, c := range cands {
		page, ferr := s.opts.Fetcher.Fetch(ctx, c)
		if ferr != nil {
			if errors.Is(ferr, ErrRefused) {
				return "", "", ferr
			}
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			continue
		}
		if page.Status < 200 || page.Status > 299 || !textual(page.ContentType) {
			continue
		}
		t := ExtractText(string(page.Body), strings.HasPrefix(strings.ToLower(page.ContentType), "text/plain"))
		if runeLen(t) >= briefMinBody {
			return clip(t, briefTextLimit), c, nil
		}
	}
	return "", "", ErrNoBrief
}

var (
	blockRE   = lazyregexp.New(`(?is)<(script|style|noscript|svg|template|head)\b.*?</(script|style|noscript|svg|template|head)\s*>`)
	commentRE = lazyregexp.New(`(?s)<!--.*?-->`)
	tagRE     = lazyregexp.New(`(?s)<[^>]*>`)
	spaceRE   = lazyregexp.New(`\s+`)
)

// ExtractText turns a page into plain text: scripts, styles and tags removed,
// entities decoded, whitespace collapsed, and a sentence of 25 characters or
// more that repeats collapsed to its first occurrence.
func ExtractText(body string, plain bool) string {
	if !plain {
		body = blockRE.ReplaceAllString(body, " ")
		body = commentRE.ReplaceAllString(body, " ")
		body = tagRE.ReplaceAllString(body, " ")
		body = html.UnescapeString(body)
	}
	body = strings.TrimSpace(spaceRE.ReplaceAllString(body, " "))
	return dedupeSentences(body)
}

func dedupeSentences(text string) string {
	var out []string
	seen := map[string]bool{}
	rs := []rune(text)
	start := 0
	flush := func(end int) {
		sent := strings.TrimSpace(string(rs[start:end]))
		start = end
		if sent == "" {
			return
		}
		if runeLen(sent) >= minRepeatedChars {
			key := strings.ToLower(sent)
			if seen[key] {
				return
			}
			seen[key] = true
		}
		out = append(out, sent)
	}
	for i, r := range rs {
		if (r == '.' || r == '!' || r == '?') && (i+1 == len(rs) || rs[i+1] == ' ') {
			flush(i + 1)
		}
	}
	flush(len(rs))
	return strings.Join(out, " ")
}

type briefState struct {
	missUntil   string
	writtenHash string
}

func (s *Service) briefState(ctx context.Context, domain string) (briefState, error) {
	var st briefState
	err := s.st.Read().QueryRowContext(ctx, `SELECT miss_until, written_hash FROM kb_brief_state WHERE domain = ?`, domain).Scan(&st.missUntil, &st.writtenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	return st, err
}

func (s *Service) recordMiss(ctx context.Context, domain string) error {
	until := s.now().Add(briefMissMemo).UTC().Format(time.RFC3339)
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO kb_brief_state (domain, miss_until) VALUES (?, ?)
			ON CONFLICT (domain) DO UPDATE SET miss_until = excluded.miss_until`, domain, until)
		return err
	})
}

// readBrief parses a brief file. It reports a brief as manual when the file says
// so, names no source, or differs from what this product wrote.
func (s *Service) readBrief(rel string, st briefState) (*Brief, error) {
	data, err := readRegular(s.absPath(rel))
	if err != nil {
		return nil, err
	}
	sn, err := ParseSnippet(rel, data)
	if err != nil {
		return nil, err
	}
	return briefFrom(rel, sn, fileHash(data), st.writtenHash, sn.body), nil
}

func briefFrom(rel string, sn *Snippet, fh, written, body string) *Brief {
	b := &Brief{Domain: strings.TrimSuffix(filepath.Base(rel), ".md"), Path: rel, SourceURL: sn.Extra["source_url"], Text: body}
	b.Manual = sn.Extra["manual"] == "true" || b.SourceURL == "" || (written != "" && written != fh)
	b.FetchedAt, _ = time.Parse(time.RFC3339, sn.Extra["fetched_at"])
	b.ExpiresAt, _ = time.Parse(time.RFC3339, sn.Extra["expires_at"])
	if b.ExpiresAt.IsZero() && !b.FetchedAt.IsZero() {
		b.ExpiresAt = b.FetchedAt.Add(briefLifetime)
	}
	return b
}

// briefExpired reports whether an indexed brief is past its life. A manual
// brief never is.
func briefExpired(d *indexedDoc, written map[string]string, now time.Time) bool {
	domain := strings.TrimSuffix(filepath.Base(d.Path), ".md")
	b := briefFrom(d.Path, &d.Snippet, d.FileHash, written[domain], "")
	if b.Manual {
		return false
	}
	return b.ExpiresAt.IsZero() || !now.Before(b.ExpiresAt)
}

func (s *Service) writtenHashes(ctx context.Context) (map[string]string, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT domain, written_hash FROM kb_brief_state WHERE written_hash <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var d, h string
		if err := rows.Scan(&d, &h); err != nil {
			return nil, err
		}
		m[d] = h
	}
	return m, rows.Err()
}

func (s *Service) writeBrief(ctx context.Context, company, domain, rel, from, text string) (*Brief, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if err := CheckSecrets(rel, text); err != nil {
		return nil, err
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if c := strings.TrimSpace(company); c != "" {
		mapSet(node, "title", strNode(c+" company brief"))
	}
	mapSet(node, "source_url", strNode(from))
	mapSet(node, "fetched_at", strNode(now.Format(time.RFC3339)))
	mapSet(node, "expires_at", strNode(now.Add(briefLifetime).Format(time.RFC3339)))
	data, err := renderDocument(node, text)
	if err != nil {
		return nil, err
	}
	sum := fileHash(data)
	prev, rerr := readRegular(s.absPath(rel))
	err = s.writeRecorded(ctx, s.absPath(rel), data, prev, rerr == nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO kb_brief_state (domain, miss_until, written_hash) VALUES (?, '', ?)
			ON CONFLICT (domain) DO UPDATE SET miss_until = '', written_hash = excluded.written_hash`, domain, sum)
		return err
	})
	if err != nil {
		return nil, err
	}
	sn, err := ParseSnippet(rel, data)
	if err != nil {
		return nil, err
	}
	return briefFrom(rel, sn, sum, sum, text), nil
}

// renderDocument writes frontmatter and body as one Markdown document.
func renderDocument(fm *yaml.Node, body string) ([]byte, error) {
	var b strings.Builder
	if fm != nil && len(fm.Content) > 0 {
		out, err := yaml.Marshal(fm)
		if err != nil {
			return nil, err
		}
		b.WriteString("---\n")
		b.Write(out)
		b.WriteString("---\n\n")
	}
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return []byte(b.String()), nil
}
