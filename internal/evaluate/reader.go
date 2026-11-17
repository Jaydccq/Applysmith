// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/discovery"
	"applysmith/internal/flight"
	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// ErrNeedsBrowser is returned for a URL no public ATS API serves; a caller
// with a browser reads the page instead.
var ErrNeedsBrowser = errors.New("this server has no managed browser reader to read this posting; start the desktop app or paste the posting text")

// ErrPageUnsure matches a browser read that failed without showing the
// posting gone: a wall, a throttle (Glassdoor answers 429 right after a
// scan's detail reads) or a page that never rendered. The posting may still
// be open, so the text a scan already read from it can stand in.
var ErrPageUnsure = errors.New("the posting page could not be read")

type unsureRead struct{ reason string }

func (e unsureRead) Error() string      { return "the posting could not be read: " + e.reason }
func (unsureRead) Is(target error) bool { return target == ErrPageUnsure }

// ClosedError is a browser read that found the posting closed: a liveness
// result of the evaluation's own page read, which the job keeps (Deps.Closed).
type ClosedError struct {
	URL      string
	Liveness discovery.Liveness
}

func (e *ClosedError) Error() string { return "the posting could not be read: " + e.Liveness.Reason }

// ATSReader reads a posting from the public API of a supported ATS: any of
// the five whose URLs yield a native identity key.
type ATSReader struct {
	Client *http.Client
	// Hosts overrides the API origins by ATS name (tests).
	Hosts map[string]string
	// Boards keeps the Ashby boards this reader fetched for a short while;
	// nil fetches the board for every posting.
	Boards *BoardCache
}

// boardCacheLife is how long a fetched Ashby board answers for its postings:
// long enough for one batch of evaluations, short enough that a posting
// closed since is read again within minutes.
const boardCacheLife = 2 * time.Minute

// boardFetchLimit bounds one board fetch, which runs detached from the
// callers that wait for it.
const boardFetchLimit = time.Minute

// BoardCache shares Ashby board fetches among the evaluations of one board.
// Ashby answers a posting only with its whole board, so without it every
// posting of a board fetched the board again. Concurrent reads of a board wait
// for the one fetch in flight, and a fetched board answers for boardCacheLife.
// A failed fetch is not kept. The zero BoardCache is ready to use.
type BoardCache struct {
	flight flight.Group
	now    func() time.Time // tests

	mu     sync.Mutex
	boards map[string]cachedBoard
}

type cachedBoard struct {
	board   ashbyBoard
	fetched time.Time
}

func (c *BoardCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the board at u, from the cache when it is fresh, else from
// fetch, shared with every caller that asks while it runs.
func (c *BoardCache) get(ctx context.Context, u string, fetch func(context.Context) (ashbyBoard, error)) (ashbyBoard, error) {
	c.mu.Lock()
	if b, ok := c.boards[u]; ok && c.clock().Sub(b.fetched) < boardCacheLife {
		c.mu.Unlock()
		return b.board, nil
	}
	c.mu.Unlock()
	v, err := c.flight.Do(ctx, u, func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, boardFetchLimit)
		defer cancel()
		b, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		now := c.clock()
		if c.boards == nil {
			c.boards = map[string]cachedBoard{}
		}
		for k, old := range c.boards {
			if now.Sub(old.fetched) >= boardCacheLife {
				delete(c.boards, k)
			}
		}
		c.boards[u] = cachedBoard{board: b, fetched: now}
		return b, nil
	})
	if err != nil {
		return ashbyBoard{}, err
	}
	return v.(ashbyBoard), nil
}

// ashbyBoard is the part of an Ashby job board a posting read uses.
type ashbyBoard struct {
	Jobs []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Location string `json:"location"`
		Address  struct {
			Postal struct {
				Country string `json:"addressCountry"`
			} `json:"postalAddress"`
		} `json:"address"`
		Plain        string `json:"descriptionPlain"`
		Compensation struct {
			Summary string `json:"compensationTierSummary"`
		} `json:"compensation"`
	} `json:"jobs"`
}

// ashbyBoard fetches the board at u, through the cache when the reader has one.
func (r ATSReader) ashbyBoard(ctx context.Context, u string) (ashbyBoard, error) {
	fetch := func(ctx context.Context) (ashbyBoard, error) {
		var v ashbyBoard
		return v, r.get(ctx, u, &v)
	}
	if r.Boards == nil {
		return fetch(ctx)
	}
	return r.Boards.get(ctx, u, fetch)
}

// atsReadLimit bounds an API answer, as the scan bounds the same boards
// (discovery maxBody): Ashby answers with the whole job board, descriptions
// included, which passes 5 MB for a large company.
const atsReadLimit = 32 << 20

func (r ATSReader) origin(ats, def string) string {
	if h := r.Hosts[ats]; h != "" {
		return strings.TrimRight(h, "/")
	}
	return def
}

func (r ATSReader) get(ctx context.Context, u string, out any) error {
	c := r.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case discovery.ThrottleStatus(resp.StatusCode):
		// A throttled read says nothing about the posting, as a page's does.
		return unsureRead{"rate-limited"}
	case resp.StatusCode == http.StatusNotFound:
		// An API that does not know the board says nothing about the posting
		// either: Lime turned its Ashby posting API off while its posting
		// pages stay live, so the page is read and decides.
		return fmt.Errorf("the ATS API answered 404: %w", ErrNeedsBrowser)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the ATS API answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, atsReadLimit+1))
	if err != nil {
		return err
	}
	if len(body) > atsReadLimit {
		return fmt.Errorf("the ATS API answered more than %d MB", atsReadLimit>>20)
	}
	return json.Unmarshal(body, out)
}

// Read implements Reader.
func (r ATSReader) Read(ctx context.Context, rawURL string) (PageRead, error) {
	key, ok := identity.ATSKey(rawURL)
	if !ok {
		return PageRead{}, ErrNeedsBrowser
	}
	ats, rest, _ := strings.Cut(key, ":")
	slug, id, _ := strings.Cut(rest, "/")
	read := PageRead{Method: "http", Result: "live"}
	switch ats {
	case identity.Greenhouse:
		var v struct {
			Title    string `json:"title"`
			Company  string `json:"company_name"`
			Content  string `json:"content"`
			Location struct {
				Name string `json:"name"`
			} `json:"location"`
			Offices []discovery.GreenhouseOffice `json:"offices"`
		}
		if err := r.get(ctx, r.origin(ats, "https://boards-api.greenhouse.io")+"/v1/boards/"+url.PathEscape(slug)+"/jobs/"+url.PathEscape(id), &v); err != nil {
			return PageRead{}, err
		}
		// The API returns the description as escaped HTML.
		read.Title, read.Company, read.Text = v.Title, v.Company, html.UnescapeString(v.Content)
		read.Location = discovery.GreenhouseLocation(v.Location.Name, v.Offices)
	case identity.Lever:
		var v struct {
			Text  string `json:"text"`
			Plain string `json:"descriptionPlain"`
			// Description is the same text as HTML; Hivemapper's posting
			// states it only so, with descriptionPlain empty.
			Description string `json:"description"`
			Additional  string `json:"additionalPlain"`
			// Lists hold the posting's requirement and duty sections, as HTML
			// list items.
			Lists []struct {
				Text    string `json:"text"`
				Content string `json:"content"`
			} `json:"lists"`
			Categories struct {
				Location string `json:"location"`
			} `json:"categories"`
			// Country is the posting's ISO 3166-1 alpha-2 code.
			Country string `json:"country"`
		}
		// A board on Lever's EU host is served by the EU API only; the global
		// one answers 404 for it.
		api := "https://api.lever.co"
		if u, err := url.Parse(rawURL); err == nil {
			if strings.HasSuffix(strings.ToLower(u.Hostname()), ".eu.lever.co") {
				api = "https://api.eu.lever.co"
			}
			// The identity key lowercases the slug, and Lever's API is
			// case-sensitive: Hivemapper's board answers 404 as "hivemapper".
			for _, seg := range strings.Split(u.Path, "/") {
				if strings.EqualFold(seg, slug) {
					slug = seg
					break
				}
			}
		}
		if err := r.get(ctx, r.origin(ats, api)+"/v0/postings/"+url.PathEscape(slug)+"/"+url.PathEscape(id), &v); err != nil {
			return PageRead{}, err
		}
		if strings.TrimSpace(v.Plain) == "" {
			v.Plain = listItems(v.Description)
		}
		parts := []string{v.Plain}
		for _, l := range v.Lists {
			parts = append(parts, strings.TrimSpace(l.Text+"\n"+listItems(l.Content)))
		}
		// Lever's and Ashby's APIs state no employer name; the board's slug is no name either.
		read.Title, read.Location, read.Text = v.Text, placeWithCountry(v.Categories.Location, v.Country), strings.Join(append(parts, v.Additional), "\n")
	case identity.Ashby:
		v, err := r.ashbyBoard(ctx, r.origin(ats, "https://api.ashbyhq.com")+"/posting-api/job-board/"+url.PathEscape(slug)+"?includeCompensation=true")
		if err != nil {
			return PageRead{}, err
		}
		for _, j := range v.Jobs {
			if strings.EqualFold(j.ID, id) {
				read.Title, read.Location, read.Text, read.Compensation = j.Title, placeWithCountry(j.Location, j.Address.Postal.Country), j.Plain, j.Compensation.Summary
				return read, nil
			}
		}
		// A posting the board does not list may be unlisted and still live, or
		// gone: its page decides, as for a 404.
		return PageRead{}, fmt.Errorf("the posting is not on the ATS board: %w", ErrNeedsBrowser)
	case identity.SmartRecruiters:
		type section struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		}
		var v struct {
			Name string `json:"name"`
			// A posting taken down still answers, inactive and with no place.
			Active  bool `json:"active"`
			Company struct {
				Name string `json:"name"`
			} `json:"company"`
			Location struct {
				FullLocation string `json:"fullLocation"`
			} `json:"location"`
			JobAd struct {
				Sections struct {
					Company        section `json:"companyDescription"`
					Job            section `json:"jobDescription"`
					Qualifications section `json:"qualifications"`
					Additional     section `json:"additionalInformation"`
				} `json:"sections"`
			} `json:"jobAd"`
		}
		if err := r.get(ctx, r.origin(ats, "https://api.smartrecruiters.com")+"/v1/companies/"+url.PathEscape(slug)+"/postings/"+url.PathEscape(id), &v); err != nil {
			return PageRead{}, err
		}
		if !v.Active {
			return PageRead{}, fmt.Errorf("the posting is not active on the ATS board: %w", ErrNeedsBrowser)
		}
		// The sections are HTML, in the order the posting page shows them.
		var parts []string
		for _, s := range []section{v.JobAd.Sections.Company, v.JobAd.Sections.Job, v.JobAd.Sections.Qualifications, v.JobAd.Sections.Additional} {
			if strings.TrimSpace(s.Text) != "" {
				parts = append(parts, s.Title+"\n"+s.Text)
			}
		}
		read.Title, read.Company, read.Location, read.Text = v.Name, v.Company.Name, strings.TrimSpace(v.Location.FullLocation), strings.Join(parts, "\n")
	case identity.Workable:
		// The read the posting page makes; a closed posting answers 404.
		var v struct {
			Title string `json:"title"`
			// Locations are every place of the posting, the primary one first
			// and those the page hides included.
			Locations []struct {
				City    string `json:"city"`
				Region  string `json:"region"`
				Country string `json:"country"`
			} `json:"locations"`
			Description  string `json:"description"`
			Requirements string `json:"requirements"`
			Benefits     string `json:"benefits"`
		}
		if err := r.get(ctx, r.origin(ats, "https://apply.workable.com")+"/api/v2/accounts/"+url.PathEscape(slug)+"/jobs/"+url.PathEscape(id), &v); err != nil {
			return PageRead{}, err
		}
		var places []string
		for _, l := range v.Locations {
			var named []string
			for _, p := range []string{l.City, l.Region, l.Country} {
				if p = strings.TrimSpace(p); p != "" {
					named = append(named, p)
				}
			}
			if len(named) > 0 {
				places = append(places, strings.Join(named, ", "))
			}
		}
		text := v.Description
		if strings.TrimSpace(v.Requirements) != "" {
			text += "\nRequirements\n" + v.Requirements
		}
		if strings.TrimSpace(v.Benefits) != "" {
			text += "\nBenefits\n" + v.Benefits
		}
		// The job names no employer: an evaluation reads it from the text.
		read.Title, read.Location, read.Text = v.Title, strings.Join(places, "; "), text
	default:
		return PageRead{}, ErrNeedsBrowser
	}
	return read, nil
}

// placeWithCountry is a posting's location chip with the posting's country
// added when the chip names none: a "Remote" chip is "Remote, United States"
// for a posting Lever files under US or Ashby addresses in the United States
// (Acorns), so the work-authorization answers can tell which country the role
// is in. A chip with a country part keeps it.
func placeWithCountry(chip, country string) string {
	chip = strings.TrimSpace(chip)
	code, ok := match.CountryCode(country)
	if !ok {
		return chip
	}
	if chip == "" {
		return match.CountryName(code)
	}
	for _, part := range strings.FieldsFunc(chip, func(r rune) bool { return r == ',' || r == '(' || r == ')' || r == '/' || r == '-' }) {
		if _, named := match.CountryCode(part); named {
			return chip
		}
	}
	return chip + ", " + match.CountryName(code)
}

var (
	itemOpenRE = lazyregexp.New(`(?i)<\s*li\b[^>]*>`)
	anyTagRE   = lazyregexp.New(`(?s)<[^>]*>`)
)

// listItems is a Lever list's HTML as one "- item" line per entry.
func listItems(h string) string {
	h = anyTagRE.ReplaceAllString(itemOpenRE.ReplaceAllString(h, "\n- "), "")
	var out []string
	for _, l := range strings.Split(html.UnescapeString(h), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" && l != "-" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// ManagedReader uses public ATS APIs where supported, otherwise the managed
// scan browser. Browser observations alone determine the verification result.
type ManagedReader struct {
	ATS     ATSReader
	Browser discovery.PageReader
}

func (r ManagedReader) Read(ctx context.Context, raw string) (PageRead, error) {
	if !publicURL(raw) {
		return PageRead{}, errors.New("the posting URL must name a public http(s) host without credentials")
	}
	p, err := r.ATS.Read(ctx, raw)
	if !errors.Is(err, ErrNeedsBrowser) {
		return p, err
	}
	if r.Browser == nil {
		return PageRead{}, ErrNeedsBrowser
	}
	pg, verdict, err := r.browse(ctx, raw)
	if err != nil {
		return PageRead{}, err
	}
	if verdict.Verdict == discovery.LiveExpired {
		return PageRead{}, &ClosedError{URL: raw, Liveness: verdict}
	}
	if verdict.Verdict != discovery.LiveActive && (pg.Throttled || discovery.ThrottleStatus(pg.Status) || discovery.RateLimitPage(pg.Title, pg.Text)) {
		return PageRead{}, unsureRead{"rate-limited"}
	}
	if discovery.OnListingBoard(pg.FinalURL) {
		if read, ok, err := r.employer(ctx, pg); ok || err != nil {
			return read, err
		}
	}
	if verdict.Verdict == discovery.LiveUncertain && verdict.Reason != "no-apply-control" {
		return PageRead{}, unsureRead{verdict.Reason}
	}
	return browserRead(raw, pg, verdict), nil
}

func publicURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && identity.NonPublicHost(u.Hostname()) == ""
}

// browse reads the page, and reads it once more when the read failed or found
// no posting yet (a document in transition, a page still rendering); only the
// second read decides.
func (r ManagedReader) browse(ctx context.Context, raw string) (discovery.PageRead, discovery.Liveness, error) {
	var pg discovery.PageRead
	var v discovery.Liveness
	var err error
	for attempt := 0; attempt < 2 && ctx.Err() == nil; attempt++ {
		if pg, err = r.Browser.Read(ctx, raw); err == nil {
			v = discovery.ClassifyLiveness(raw, pg, discovery.Expect{})
		}
		if err == nil && !rereadReasons[v.Reason] {
			break
		}
	}
	return pg, v, err
}

// rereadReasons are the liveness reasons of a page that showed no posting yet.
var rereadReasons = map[string]bool{"unreadable": true, "posting-not-rendered": true, "navigation-failed": true, "timeout": true}

// employer reads the employer's posting that a job board's listing names in
// its apply links, at most two of them; nothing on the board is pressed. ok is
// false when no link led to a readable posting, and the listing stands. A
// posting the employer shows closed is a ClosedError.
func (r ManagedReader) employer(ctx context.Context, listing discovery.PageRead) (PageRead, bool, error) {
	tried := 0
	for _, href := range listing.ApplyLinks {
		target, ok := discovery.ApplyTarget(listing.FinalURL, href)
		if !ok || !publicURL(target) {
			continue
		}
		if tried++; tried > 2 {
			break
		}
		if p, err := r.ATS.Read(ctx, target); err == nil {
			p.URL = target
			return p, true, nil
		} else if !errors.Is(err, ErrNeedsBrowser) {
			continue
		}
		pg, v, err := r.browse(ctx, target)
		switch {
		case err != nil || discovery.OnListingBoard(pg.FinalURL):
			continue
		case v.Verdict == discovery.LiveExpired:
			return PageRead{}, true, &ClosedError{URL: target, Liveness: v}
		case v.Verdict == discovery.LiveUncertain && v.Reason != "no-apply-control":
			continue
		}
		if read := browserRead(listing.FinalURL, pg, v); read.URL != "" {
			return read, true, nil
		}
	}
	return PageRead{}, false, nil
}

// browserRead is the posting a page read of raw shows. Its text is the
// posting's own description where the page marks one (a job board's
// description block, the JobPosting data): the page's text can open with a
// site's search form and menus. URL is the page's posting address when the
// read ended on another posting than raw names (a redirect's landing page),
// and never a careers home, an index or a search.
func browserRead(raw string, pg discovery.PageRead, v discovery.Liveness) PageRead {
	result := "unverified"
	if v.Verdict == discovery.LiveActive {
		result = "live"
	}
	text := pg.Text
	if len([]rune(strings.TrimSpace(pg.Description))) >= MinPostingChars {
		text = pg.Description
	}
	read := PageRead{Title: firstNonEmpty(pg.PostingTitle, pg.Heading, pg.Title), Text: text, Method: "browser", Result: result}
	addr := discovery.PostingAddress(pg)
	canon, err := identity.CanonicalURL(addr)
	if err != nil || discovery.NonPostingURL(canon) {
		return read
	}
	switch discovery.LinkKind(addr) {
	case discovery.LinkCareersIndex, discovery.LinkCompanyHome, discovery.LinkShortener:
		return read
	}
	if k, err := identity.URLKey(addr); err == nil {
		if own, err := identity.URLKey(raw); err != nil || own != k {
			read.URL = addr
		}
	}
	return read
}
