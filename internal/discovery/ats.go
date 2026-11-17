package discovery

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// The five supported ATSs.
const (
	Greenhouse      = identity.Greenhouse
	Lever           = identity.Lever
	Ashby           = identity.Ashby
	SmartRecruiters = identity.SmartRecruiters
	Workable        = identity.Workable
)

// ATSNames lists the supported ATSs.
var ATSNames = []string{Greenhouse, Lever, Ashby, SmartRecruiters, Workable}

// Board is one company's job board on an ATS.
type Board struct {
	ATS     string
	Slug    string // lowercase
	Company string // display name when known
}

// Key is the registry key `ats:slug`.
func (b Board) Key() string { return b.ATS + ":" + b.Slug }

func (b Board) company() string {
	if b.Company != "" {
		return b.Company
	}
	return humanSlug(b.Slug)
}

var slugPattern = lazyregexp.New(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidSlug reports whether s may be a registry slug.
func ValidSlug(s string) bool { return slugPattern.MatchString(s) }

func validATS(a string) bool {
	for _, n := range ATSNames {
		if n == a {
			return true
		}
	}
	return false
}

// ParseBoardKey reads `ats:slug`.
func ParseBoardKey(s string) (Board, error) {
	ats, slug, ok := strings.Cut(strings.TrimSpace(s), ":")
	ats = strings.ToLower(ats)
	if !ok || !validATS(ats) || !ValidSlug(slug) {
		return Board{}, fmt.Errorf("%q is not an ats:slug pair; ats is one of %s", s, strings.Join(ATSNames, ", "))
	}
	return Board{ATS: ats, Slug: strings.ToLower(slug)}, nil
}

// LeverEU is the Endpoints key of Lever's EU API, which alone serves the
// boards of jobs.eu.lever.co.
const LeverEU = "lever-eu"

// Endpoints holds the API base URL of each ATS. The zero value uses the
// vendors' public hosts; tests point it at local servers.
type Endpoints map[string]string

var defaultEndpoints = Endpoints{
	Greenhouse:      "https://boards-api.greenhouse.io",
	Lever:           "https://api.lever.co",
	LeverEU:         "https://api.eu.lever.co",
	Ashby:           "https://api.ashbyhq.com",
	SmartRecruiters: "https://api.smartrecruiters.com",
	Workable:        "https://apply.workable.com",
}

func (e Endpoints) base(ats string) string {
	if v := e[ats]; v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultEndpoints[ats]
}

// ListURL is the listing endpoint of a board. page is the zero-based page of
// a paged ATS.
func (e Endpoints) ListURL(b Board, page int) string {
	base, slug := e.base(b.ATS), url.PathEscape(b.Slug)
	switch b.ATS {
	case Greenhouse:
		return base + "/v1/boards/" + slug + "/jobs"
	case Lever:
		return base + "/v0/postings/" + slug
	case Ashby:
		return base + "/posting-api/job-board/" + slug + "?includeCompensation=true"
	case SmartRecruiters:
		return fmt.Sprintf("%s/v1/companies/%s/postings?limit=%d&offset=%d", base, slug, srPageSize, page*srPageSize)
	case Workable:
		return base + "/api/v1/widget/accounts/" + slug + "?details=true"
	}
	return ""
}

// leverEUListURL is the listing of a Lever board on the EU API, "" for a board
// of another ATS. A registry row keeps no region, so a board the global API
// does not know is asked for here. With the global endpoint pointed elsewhere
// and no EU one named there is none.
func (e Endpoints) leverEUListURL(b Board) string {
	if b.ATS != Lever || (e[Lever] != "" && e[LeverEU] == "") {
		return ""
	}
	return e.base(LeverEU) + "/v0/postings/" + url.PathEscape(b.Slug)
}

// ErrNoBoard means a URL names no board.
var ErrNoBoard = errors.New("discovery: url names no supported board")

var staticExt = lazyregexp.New(`(?i)\.(js|mjs|css|png|jpe?g|gif|svg|ico|txt|xml|json|map|woff2?|ttf|webp|html?|pdf)$`)

var infraSubdomains = map[string]bool{
	"api": true, "www": true, "support": true, "status": true, "app": true, "apply": true, "help": true,
	"docs": true, "developers": true, "resources": true, "boards": true, "job-boards": true, "boards-api": true,
	"careers": true, "jobs": true,
}

// ResolveBoard reads a board slug from a careers or posting URL.
func ResolveBoard(raw string) (Board, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Board{}, ErrNoBoard
	}
	host := strings.ToLower(u.Hostname())
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	first := func() string {
		if len(segs) > 0 {
			return segs[0]
		}
		return ""
	}
	var ats, slug string
	fromHost := false
	switch {
	case host == "jobs.ashbyhq.com" || host == "jobs.eu.ashbyhq.com":
		ats, slug = Ashby, first()
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co":
		ats, slug = Lever, first()
	case host == "jobs.smartrecruiters.com" || host == "careers.smartrecruiters.com":
		ats, slug = SmartRecruiters, first()
		if strings.EqualFold(slug, "oneclick-ui") {
			slug = ""
			if len(segs) >= 3 && strings.EqualFold(segs[1], "company") {
				slug = segs[2]
			}
		}
	case host == "apply.workable.com":
		ats, slug = Workable, first()
		if strings.EqualFold(slug, "j") || strings.EqualFold(slug, "api") {
			slug = ""
		}
	case strings.HasSuffix(host, ".workable.com"):
		ats, slug, fromHost = Workable, strings.TrimSuffix(host, ".workable.com"), true
	case host == "boards.greenhouse.io" || host == "job-boards.greenhouse.io" || host == "boards.eu.greenhouse.io" || host == "job-boards.eu.greenhouse.io":
		ats, slug = Greenhouse, first()
		if strings.EqualFold(slug, "embed") {
			slug = u.Query().Get("for")
		}
	case strings.HasSuffix(host, ".greenhouse.io"):
		ats, slug, fromHost = Greenhouse, strings.TrimSuffix(host, ".greenhouse.io"), true
	default:
		return Board{}, ErrNoBoard
	}
	if slug == "" || !ValidSlug(slug) || staticExt.MatchString(slug) || (fromHost && infraSubdomains[strings.ToLower(slug)]) {
		return Board{}, ErrNoBoard
	}
	return Board{ATS: ats, Slug: strings.ToLower(slug)}, nil
}

// parseTime reads the date forms the vendors, the pages' JobPosting data and
// the boards' cards use, in UTC; the zero time means none. RFC 3339 covers a
// time with a fraction of a second too.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// joinNonEmpty joins the non-blank parts with ", ".
func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// AshbyEmbedURL is the embedded application form of an Ashby posting address
// (https://jobs.ashbyhq.com/<board>/<id>), "" for any other address. An
// employer whose own careers page embeds the form turns Ashby's posting pages
// off, so they answer 404, while the form itself is still served (PostHog); a
// closed posting's form answers 404 too.
func AshbyEmbedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "jobs.ashbyhq.com" && host != "jobs.eu.ashbyhq.com" {
		return ""
	}
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) < 2 {
		return ""
	}
	return "https://" + host + "/" + segs[0] + "/" + segs[1] + "/application?embed=js"
}
