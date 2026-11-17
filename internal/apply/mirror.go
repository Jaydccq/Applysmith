package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"applysmith/internal/discovery"
	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// AggregatorOnlyPrefix starts the reason of a mirror whose employer posting was
// not found. The dashboard reads it as "no employer posting found, so nothing
// to apply to", not as a closed link; the verdict is the runtime's judgement
// and expires with the code.
const AggregatorOnlyPrefix = "aggregator-only posting; employer posting not found"

// MirrorSources are the lookups mirror resolution makes, all plain HTTP and
// none through a browser. A nil source is a step that is skipped.
type MirrorSources struct {
	// Page reads the mirror's own page: its HTML and the HTTP status, 0 when it
	// could not be read. A non-2xx page returns no HTML; a challenge is never
	// solved or retried.
	Page func(ctx context.Context, rawURL string) (html string, status int)
	// Registry lists the boards the ATS registry holds for a company, by exact
	// company name (not a substring).
	Registry func(ctx context.Context, company string) []discovery.Board
	// Board lists the postings of a board through its vendor's public listing.
	Board func(ctx context.Context, b discovery.Board) ([]discovery.Posting, error)
	// BoardName reads the name a Greenhouse board states for itself.
	BoardName func(ctx context.Context, slug string) string
	// Workday searches the Workday career sites the scans recorded for the
	// company (or whose tenant is employerLabel), and the sites of the pages in
	// sites, for the titles.
	Workday func(ctx context.Context, company, employerLabel string, titles, sites []string) []BoardMatch
	// Search is the open web: hits with their page title and address, only
	// from the hosts in include when it names any, with the hosts in exclude
	// left out. Nil, or ErrNoWebSearch, when no key is configured.
	Search func(ctx context.Context, query string, include, exclude []string) ([]WebHit, error)
	// Location is where the scans recorded the mirror posting itself ("Palo
	// Alto, CA"), "" when none did: it narrows several same-titled postings
	// when neither the mirror's address nor its page names a city.
	Location func(ctx context.Context, mirrorURL string) string
}

// atsSearchHosts are the applicant-tracking systems the resolver recognizes,
// searched on their own as well: the open web ranks a posting's copies, people
// and team pages above the employer's own posting, and not the same way twice
// (Caris on its Workday site, d66–d67).
var atsSearchHosts = []string{"myworkdayjobs.com", "myworkdaysite.com", "greenhouse.io", "lever.co", "ashbyhq.com", "icims.com", "smartrecruiters.com", "workable.com", "breezy.hr"}

// ErrNoWebSearch is a web search with no key to run it.
var ErrNoWebSearch = errors.New("no web search key is stored")

// BoardMatch is one posting a board search found.
type BoardMatch struct {
	URL string
	// Key is shared by two boards that list one posting (a Workday tenant's two
	// career sites), which count once.
	Key string
	// Location is the board's own location text for the posting.
	Location string
	Via      string
}

// WebHit is one web search result.
type WebHit struct{ Title, URL string }

// MirrorHint is the company and role a mirror row is looked up by: the
// evaluated report's, else the list row's own.
type MirrorHint struct{ Company, Role string }

// MirrorResolution is where a mirror resolved to, or why it did not.
type MirrorResolution struct {
	URL, Via string
	// Reason is set when the mirror did not resolve; it starts with
	// AggregatorOnlyPrefix.
	Reason string
}

var (
	aggregatorNameRE = lazyregexp.New(`(?i)talenthop|jobgrow|remoterush|emploive|jobright`)
	unnamedRE        = lazyregexp.New(`(?i)reputed|undisclosed|confidential|stealth|unnamed|anonymous|\bnot (?:disclosed|named)\b`)
	genericCompany   = map[string]bool{"inc": true, "llc": true, "ltd": true, "corp": true, "corporation": true, "co": true, "company": true, "the": true, "group": true, "gmbh": true, "plc": true}
	wordRE           = lazyregexp.New(`[a-z0-9]+`)
	anchorHrefRE     = lazyregexp.New(`(?is)<a\b[^>]*\bhref\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	ldJSONRE         = lazyregexp.New(`(?is)<script\b[^>]*type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
	tailSeparatorRE  = lazyregexp.New(`(?i)\s+(?:\||@|·|—|–|-|at)\s+`)
	zipCityRE        = lazyregexp.New(`(?i)/-in-([^/]+),[a-z]{2}/?$`)
	icimsHostRE      = lazyregexp.New(`(?i)(^|\.)icims\.com$`)
	breezyHostRE     = lazyregexp.New(`(?i)^[a-z0-9-]+\.breezy\.hr$`)
)

// titleKey is a title as its words in order, case and punctuation aside.
// Nothing looser counts: not a dropped level, not a subset, not a plural,
// because a wrong posting found here is an application to the wrong job.
func titleKey(title string) string {
	return strings.Join(wordRE.FindAllString(strings.ToLower(title), -1), " ")
}

func companyWords(company string) []string {
	var out []string
	for _, w := range wordRE.FindAllString(strings.ToLower(company), -1) {
		if !genericCompany[w] {
			out = append(out, w)
		}
	}
	return out
}

// SameCompany is the same name, spacing aside ("Stitch Fix" = "Stitchfix"); a
// name that merely contains the other does not match.
func SameCompany(a, b string) bool {
	ka, kb := strings.Join(companyWords(a), ""), strings.Join(companyWords(b), "")
	return ka != "" && ka == kb
}

// titleMatches reports whether a board's title is one of titles, the same
// words in order, with one allowance: an employer's own board may put its name
// in the title, which a mirror's title for the same job leaves out. Only the
// company's name is dropped, never a level or a qualifier.
func titleMatches(titles map[string]bool, boardTitle, company string) bool {
	key := titleKey(boardTitle)
	if titles[key] {
		return true
	}
	if name := strings.Join(companyWords(company), " "); name != "" {
		return titles[titleKey(strings.Replace(" "+key+" ", " "+name+" ", " ", 1))]
	}
	return false
}

// webTitleMatches reports whether a web page's title is one of titles, whole,
// or with a trailing part that names the company ("Platform Engineer at
// Example", "... - Careers | example.com"). A trailing part that does not name
// the company is not dropped: "Engineer in Test" is not "Engineer".
func webTitleMatches(titles map[string]bool, pageTitle, company string) bool {
	if titles[titleKey(pageTitle)] {
		return true
	}
	ck := strings.Join(companyWords(company), "")
	if ck == "" {
		return false
	}
	for _, loc := range tailSeparatorRE.FindAllStringIndex(pageTitle, -1) {
		tail := strings.Join(wordRE.FindAllString(strings.ToLower(pageTitle[loc[1]:]), -1), "")
		if strings.Contains(tail, ck) && titles[titleKey(pageTitle[:loc[0]])] {
			return true
		}
	}
	return false
}

// hostBelongsToCompany: a name label of the host is the company's name
// (`example.com`, `jobs.example.njoyn.com`), or the first word-run of a longer
// name of five letters or more.
func hostBelongsToCompany(raw, company string) bool {
	ck := strings.Join(companyWords(company), "")
	if ck == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	labels := strings.Split(strings.ToLower(u.Hostname()), ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels[:len(labels)-1] {
		if l == "www" {
			continue
		}
		a := strings.Join(wordRE.FindAllString(l, -1), "")
		if a == ck || (len(l) >= 5 && strings.HasPrefix(ck, a)) {
			return true
		}
	}
	return false
}

// employerNamed is the employer a company and role pair names. A label that is
// not a company (the mirror's own name, "Reputed Company", "Undisclosed ...")
// names no employer to search, so it yields nothing. "TalentHop (Quora)" names
// Quora; "Example Group (via TalentHop)" names Example Group.
func employerNamed(h MirrorHint) (MirrorHint, bool) {
	base, paren := strings.TrimSpace(h.Company), ""
	if i := strings.Index(base, "("); i >= 0 {
		if j := strings.Index(base[i:], ")"); j > 0 {
			paren = strings.TrimSpace(base[i+1 : i+j])
			base = strings.TrimSpace(base[:i])
		}
	}
	company := base
	if aggregatorNameRE.MatchString(base) && paren != "" {
		company = paren
	}
	role := strings.TrimSpace(h.Role)
	if company == "" || role == "" || aggregatorNameRE.MatchString(company) || unnamedRE.MatchString(company) {
		return MirrorHint{}, false
	}
	return MirrorHint{Company: company, Role: role}, true
}

// isRecognizedATSHref is an address on the employer's own system: a named
// adapter's host, any board the registry can address (SmartRecruiters,
// Workable), an iCIMS career site or a Breezy board. Another mirror or a
// marketing page is not a resolution.
func isRecognizedATSHref(raw string) bool {
	cls, stp := Classify(raw)
	if stp == nil && cls.ATS != ATSGeneric && cls.ATS != ATSLinkedIn {
		return true
	}
	if _, err := discovery.ResolveBoard(raw); err == nil {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (icimsHostRE.MatchString(u.Hostname()) || breezyHostRE.MatchString(u.Hostname()))
}

// outboundEmployerLink is branch one: the page's own outbound link to a
// recognized system.
func outboundEmployerLink(html, pageURL string) string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	for _, m := range anchorHrefRE.FindAllStringSubmatch(html, -1) {
		href := m[1]
		if href == "" {
			href = m[2]
		}
		ref, err := url.Parse(strings.TrimSpace(href))
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			continue
		}
		if isRecognizedATSHref(abs.String()) {
			return abs.String()
		}
	}
	return ""
}

type ldPosting struct{ title, employerHost, city string }

// postingJSONLD reads the schema.org JobPosting a mirror publishes for search
// engines: its title, the hiring organization's website and the city.
func postingJSONLD(html string) *ldPosting {
	for _, m := range ldJSONRE.FindAllStringSubmatch(html, -1) {
		var v any
		if json.Unmarshal([]byte(m[1]), &v) != nil {
			continue
		}
		nodes, ok := v.([]any)
		if !ok {
			nodes = []any{v}
		}
		for _, n := range nodes {
			o, _ := n.(map[string]any)
			if o == nil || o["@type"] != "JobPosting" {
				continue
			}
			out := &ldPosting{title: str(o["title"])}
			if org, _ := o["hiringOrganization"].(map[string]any); org != nil {
				if u, err := url.Parse(str(org["sameAs"])); err == nil {
					out.employerHost = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
				}
			}
			loc := o["jobLocation"]
			if arr, ok := loc.([]any); ok && len(arr) > 0 {
				loc = arr[0]
			}
			if lo, _ := loc.(map[string]any); lo != nil {
				if ad, _ := lo["address"].(map[string]any); ad != nil {
					out.city = str(ad["addressLocality"])
				}
			}
			return out
		}
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func cityFromMirrorURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if m := zipCityRE.FindStringSubmatch(u.Path); m != nil {
		return strings.ReplaceAll(m[1], "-", " ")
	}
	return ""
}

func locationHasCity(location, city string) bool {
	ck := titleKey(city)
	return ck != "" && strings.Contains(" "+titleKey(location)+" ", " "+ck+" ")
}

// Resolve finds the employer's own application for a mirror before the account
// and walk stages touch the mirror itself. Order: the page's own outbound link
// to a recognized system; then, for the company the hint names, the ATS
// registry, the Workday sites the scans recorded and boards guessed from the
// company's name, each accepted only when it proves it is that company's, the
// title is the same words in order and exactly one posting matches; several
// same-titled postings are narrowed by the city the mirror itself names, or
// the scans recorded for it; then the open web, only when the board searches
// found nothing; else a stop.
func (m *MirrorSources) Resolve(ctx context.Context, mirrorURL string, hint MirrorHint) MirrorResolution {
	var html string
	var status int
	// webNote says why the open web, when it was the next step, found nothing to go on.
	webNote := ""
	if m != nil && m.Page != nil {
		html, status = m.Page(ctx, mirrorURL)
	}
	if out := outboundEmployerLink(html, mirrorURL); out != "" {
		return MirrorResolution{URL: out, Via: "outbound-link"}
	}
	named, ok := employerNamed(hint)
	if ok && m != nil {
		posting := postingJSONLD(html)
		titles := map[string]bool{}
		for _, t := range []string{titleKey(named.Role), titleKey(ldTitle(posting))} {
			if t != "" {
				titles[t] = true
			}
		}
		matches := m.searchRegistry(ctx, named.Company, titles)
		if m.Workday != nil {
			employer := ""
			if posting != nil {
				employer = strings.SplitN(posting.employerHost, ".", 2)[0]
			}
			matches = append(matches, m.Workday(ctx, named.Company, employer, slices.Sorted(maps.Keys(titles)), nil)...)
		}
		matches = append(matches, m.probeBoards(ctx, named.Company, titles)...)
		if len(matches) == 0 {
			var hits []BoardMatch
			var sites []string
			hits, sites, webNote = m.searchWeb(ctx, named, titles)
			matches = append(matches, hits...)
			// A page of another posting on the company's own Workday site names
			// that site, whose own search finds the posting (Caris).
			if len(matches) == 0 && len(sites) > 0 && m.Workday != nil {
				matches = append(matches, m.Workday(ctx, named.Company, "", slices.Sorted(maps.Keys(titles)), sites)...)
			}
		}
		distinct := map[string]BoardMatch{}
		var order []string
		for _, bm := range matches {
			k := firstNonEmpty(bm.Key, bm.URL)
			if _, dup := distinct[k]; !dup {
				distinct[k] = bm
				order = append(order, k)
			}
		}
		switch {
		case len(order) == 1:
			return MirrorResolution{URL: distinct[order[0]].URL, Via: distinct[order[0]].Via}
		case len(order) > 1:
			city := cityFromMirrorURL(mirrorURL)
			if posting != nil && posting.city != "" {
				city = posting.city
			}
			if city == "" && m.Location != nil {
				city, _, _ = strings.Cut(m.Location(ctx, mirrorURL), ",")
			}
			var inCity []string
			for _, k := range order {
				if locationHasCity(distinct[k].Location, city) {
					inCity = append(inCity, k)
				}
			}
			if len(inCity) == 1 {
				return MirrorResolution{URL: distinct[inCity[0]].URL, Via: distinct[inCity[0]].Via}
			}
			var urls []string
			for _, k := range order {
				urls = append(urls, redactURL(distinct[k].URL))
			}
			return MirrorResolution{Reason: fmt.Sprintf("%s; %d employer postings share its title (%s); not guessing between them", AggregatorOnlyPrefix, len(order), strings.Join(urls, ", "))}
		}
	}
	var causes []string
	switch status {
	case 404, 410:
		causes = append(causes, fmt.Sprintf("the mirror's own copy is closed, HTTP %d", status))
	case 401, 403, 429, 503:
		causes = append(causes, fmt.Sprintf("the mirror page is behind a bot wall, HTTP %d", status))
	}
	if !ok {
		causes = append(causes, "no employer is named")
	}
	if webNote != "" {
		causes = append(causes, webNote)
	}
	reason := AggregatorOnlyPrefix
	if len(causes) > 0 {
		reason += " (" + strings.Join(causes, "; ") + ")"
	}
	return MirrorResolution{Reason: reason}
}

func ldTitle(p *ldPosting) string {
	if p == nil {
		return ""
	}
	return p.title
}

func (m *MirrorSources) searchRegistry(ctx context.Context, company string, titles map[string]bool) []BoardMatch {
	if m.Registry == nil || m.Board == nil {
		return nil
	}
	var out []BoardMatch
	for _, b := range m.Registry(ctx, company) {
		posts, err := m.Board(ctx, b)
		if err != nil {
			continue
		}
		for _, p := range posts {
			if p.URL != "" && titleMatches(titles, p.Title, company) {
				out = append(out, BoardMatch{URL: p.URL, Key: p.URL, Location: p.Location, Via: "registry-search"})
			}
		}
	}
	return out
}

// probeBoards guesses the company's board on each system the registry can
// address, from the slug its name makes. A slug another organisation holds
// must not pass: a board counts only when its own data says it is this
// company: Workable and SmartRecruiters name the company; Greenhouse names
// the board; Ashby and Lever name nobody, so the matching posting's text must
// say the company's name. The title rule is unchanged.
func (m *MirrorSources) probeBoards(ctx context.Context, company string, titles map[string]bool) []BoardMatch {
	words := companyWords(company)
	if m.Board == nil || len(words) == 0 {
		return nil
	}
	named := regexp.MustCompile(`(?i)\b` + strings.Join(quoteAll(words), `\W+`) + `\b`)
	var out []BoardMatch
	seen := map[string]bool{}
	// A board may carry the name's first word alone (Palantir Technologies'
	// Lever board is "palantir"); a posting there still has to name the whole
	// company to count.
	for _, slug := range []string{strings.Join(words, ""), strings.Join(words, "-"), words[0]} {
		if len(slug) < 2 || !discovery.ValidSlug(slug) {
			continue
		}
		for _, ats := range discovery.ATSNames {
			b := discovery.Board{ATS: ats, Slug: slug}
			if seen[b.Key()] {
				continue
			}
			seen[b.Key()] = true
			posts, err := m.Board(ctx, b)
			if err != nil {
				continue
			}
			var hits []discovery.Posting
			for _, p := range posts {
				if p.URL != "" && titleMatches(titles, p.Title, company) {
					hits = append(hits, p)
				}
			}
			if len(hits) == 0 {
				continue
			}
			owned := func(p discovery.Posting) bool {
				switch ats {
				case discovery.Greenhouse:
					if m.BoardName == nil {
						return false
					}
					// A board keyed by the whole name may go by its leading
					// words: Abnormal Security's "abnormalsecurity" is named
					// "Abnormal". The first-word guess must match whole.
					name := companyWords(m.BoardName(ctx, slug))
					whole := slug == strings.Join(words, "")
					return len(name) > 0 && (slices.Equal(name, words) || whole && len(name) < len(words) && slices.Equal(name, words[:len(name)]))
				case discovery.Ashby, discovery.Lever:
					return named.MatchString(p.Description)
				}
				return SameCompany(p.Company, company)
			}
			for _, p := range hits {
				if owned(p) {
					out = append(out, BoardMatch{URL: p.URL, Key: p.URL, Location: p.Location, Via: "board-probe"})
				}
			}
		}
	}
	return out
}

func quoteAll(ws []string) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = regexp.QuoteMeta(w)
	}
	return out
}

// searchWeb is the open web, for an employer whose boards the steps above
// could not find: a branded careers site or a system no registry lists. Two
// phrasings and the same search over the recognized systems alone are merged
// (a ranking moves with the wording), every mirror is excluded, and a hit counts only when it is not a mirror, press or LinkedIn
// page, its title is the mirror's title (webTitleMatches), and its host is the
// company's own or a recognized system whose title names the company. No key,
// a failed request: no matches, never an error; the note says that no key ran
// the search, or how every query of it failed. sites are the hits on the
// company's own Workday host, whatever posting they show.
func (m *MirrorSources) searchWeb(ctx context.Context, h MirrorHint, titles map[string]bool) (out []BoardMatch, sites []string, note string) {
	const noKey = "the open web was not searched: no web search key is stored (`applysmith secrets set api-key-exa`)"
	if m.Search == nil {
		return nil, nil, noKey
	}
	hits := map[string]WebHit{}
	var failed error
	ran := false
	queries := []struct {
		text             string
		include, exclude []string
	}{
		{h.Company + " careers " + h.Role + " apply", nil, aggregatorHosts},
		{h.Role + " " + h.Company, nil, aggregatorHosts},
		{h.Role + " " + h.Company, atsSearchHosts, nil},
	}
	for _, q := range queries {
		res, err := m.Search(ctx, q.text, q.include, q.exclude)
		if errors.Is(err, ErrNoWebSearch) {
			return nil, nil, noKey
		}
		if err != nil {
			failed = err
			continue
		}
		ran = true
		for _, hit := range res {
			hits[hit.URL] = hit
		}
	}
	ck := strings.Join(companyWords(h.Company), "")
	for _, u := range slices.Sorted(maps.Keys(hits)) {
		hit := hits[u]
		cls, stp := Classify(hit.URL)
		if stp == nil && cls.ATS == ATSWorkday && hostBelongsToCompany(hit.URL, h.Company) {
			sites = append(sites, hit.URL)
		}
		if stp != nil || cls.ATS == ATSLinkedIn || IsAggregatorURL(hit.URL) || !webTitleMatches(titles, hit.Title, h.Company) {
			continue
		}
		named := strings.Contains(strings.Join(wordRE.FindAllString(strings.ToLower(hit.Title), -1), ""), ck)
		if hostBelongsToCompany(hit.URL, h.Company) || (isRecognizedATSHref(hit.URL) && named) {
			// One posting reached by two addresses (Workday with and without the location segment) counts once.
			key := hit.URL
			if k, err := identity.URLKey(hit.URL); err == nil && k != "" {
				key = k
			}
			out = append(out, BoardMatch{URL: hit.URL, Key: key, Via: "web-search"})
		}
	}
	if !ran && failed != nil {
		return nil, nil, "the web search failed: " + failed.Error()
	}
	return out, sites, ""
}

// MatchesMirrorTitle applies the same exact title rule to external board adapters.
func MatchesMirrorTitle(titles []string, title, company string) bool {
	keys := map[string]bool{}
	for _, t := range titles {
		keys[titleKey(t)] = true
	}
	return titleMatches(keys, title, company)
}
