package apply

import (
	"net/url"
	"strings"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// Applicant-tracking systems the chain names. Any other http(s) host is
// generic and is not a refusal.
const (
	ATSGreenhouse = "greenhouse"
	ATSLever      = "lever"
	ATSAshby      = "ashby"
	ATSWorkday    = "workday"
	ATSLinkedIn   = "linkedin"
	ATSGeneric    = "generic"
)

const notAnApplication = "not an application URL"

// AccountRequired says whether an application on a system needs an account
// before its form, and whether the system alone tells: Workday registers every
// applicant, the public boards (Greenhouse, Lever, Ashby) take the form signed
// out, and on LinkedIn or a generic site only the page tells.
func AccountRequired(ats string) (required, known bool) {
	switch ats {
	case ATSWorkday:
		return true, true
	case ATSGreenhouse, ATSLever, ATSAshby:
		return false, true
	}
	return false, false
}

// Classification is the posting URL read without touching the browser.
type Classification struct {
	ATS    string
	Tenant string
	// ApplyURL is where the walk opens: the adapter's application URL.
	ApplyURL string
	// Posting is the URL the classification was made from, the page to go back
	// to when ApplyURL, an address built from it, is not served.
	Posting string
}

var (
	pressWireHosts = []string{"prnewswire.com", "businesswire.com", "globenewswire.com", "einpresswire.com", "accesswire.com", "newswire.com", "prweb.com"}
	trackingHosts  = []string{"doubleclick.net", "googleadservices.com", "googlesyndication.com", "google-analytics.com", "googletagmanager.com", "adservice.google.com"}
	smartAPIRE     = lazyregexp.New(`^/v\d+/companies/([^/]+)/postings/([^/]+)/?$`)
	workdayJobRE   = lazyregexp.New(`(?i)/job/`)
)

func hostIs(h, domain string) bool { return h == domain || strings.HasSuffix(h, "."+domain) }

func anyHost(h string, domains []string) bool {
	for _, d := range domains {
		if hostIs(h, d) {
			return true
		}
	}
	return false
}

func segments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Classify reads a posting URL into one system and tenant. It stops with
// class posting and "not an application URL" for a non-http(s) URL, a URL a
// named adapter owns but judges not to be a posting (a board index, a LinkedIn
// feed, profile or search page), an advertising or analytics tracking URL and
// a press-release wire host. A SmartRecruiters posting-API URL is rewritten to
// the human page; the run's identity stays the original URL.
func Classify(raw string) (Classification, *stop) {
	cls, s := classifyURL(raw)
	cls.Posting = strings.TrimSpace(raw)
	return cls, s
}

func classifyURL(raw string) (Classification, *stop) {
	not := &stop{class: ClassPosting, reason: notAnApplication}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return Classification{}, not
	}
	host := strings.ToLower(u.Hostname())
	if anyHost(host, pressWireHosts) || anyHost(host, trackingHosts) {
		return Classification{}, not
	}
	segs := segments(u.Path)
	q := u.Query()
	switch {
	case host == "api.smartrecruiters.com":
		if m := smartAPIRE.FindStringSubmatch(u.Path); m != nil {
			page := "https://jobs.smartrecruiters.com/" + m[1] + "/" + m[2]
			return Classification{ATS: ATSGeneric, Tenant: "jobs.smartrecruiters.com", ApplyURL: page}, nil
		}
		return Classification{}, not
	case hostIs(host, "greenhouse.io"):
		return classifyGreenhouse(u, host, segs, q)
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co":
		if len(segs) < 2 {
			return Classification{}, not
		}
		base := *u
		base.RawQuery, base.Fragment = "", ""
		base.Path = "/" + strings.Join(segs[:2], "/")
		return Classification{ATS: ATSLever, Tenant: segs[0], ApplyURL: base.String() + "/apply"}, nil
	case host == "jobs.ashbyhq.com" || host == "jobs.eu.ashbyhq.com":
		if n := len(segs); n > 0 && strings.EqualFold(segs[n-1], "application") {
			segs = segs[:n-1]
		}
		if len(segs) < 2 {
			return Classification{}, not
		}
		base := *u
		base.RawQuery, base.Fragment = "", ""
		base.Path = "/" + strings.Join(segs[:2], "/") + "/application"
		return Classification{ATS: ATSAshby, Tenant: segs[0], ApplyURL: base.String()}, nil
	case hostIs(host, "myworkdayjobs.com") || hostIs(host, "myworkdaysite.com"):
		if !workdayJobRE.MatchString(u.Path) {
			return Classification{}, not
		}
		p := strings.TrimRight(u.Path, "/")
		p = strings.TrimSuffix(p, "/applyManually")
		p = strings.TrimSuffix(p, "/apply")
		base := *u
		base.RawQuery, base.Fragment = "", ""
		base.Path = p + "/apply/applyManually"
		return Classification{ATS: ATSWorkday, Tenant: workdayTenant(host, segs), ApplyURL: base.String()}, nil
	case hostIs(host, "linkedin.com"):
		if len(segs) >= 3 && segs[0] == "jobs" && segs[1] == "view" {
			apply := LinkedInFlowURL(raw)
			if apply == "" {
				apply = strings.TrimSpace(raw)
			}
			return Classification{ATS: ATSLinkedIn, Tenant: "linkedin", ApplyURL: apply}, nil
		}
		if q.Get("currentJobId") != "" && len(segs) >= 1 && segs[0] == "jobs" {
			return Classification{ATS: ATSLinkedIn, Tenant: "linkedin", ApplyURL: strings.TrimSpace(raw)}, nil
		}
		return Classification{}, not
	}
	return Classification{ATS: ATSGeneric, Tenant: strings.TrimPrefix(host, "www."), ApplyURL: strings.TrimSpace(raw)}, nil
}

// workdayTenant is the employer a Workday address names: the host's first
// label on myworkdayjobs.com ("example.wd5.myworkdayjobs.com"), and on
// myworkdaysite.com, whose host is the data centre every employer there
// shares, the path segment after "recruiting"
// ("wd5.myworkdaysite.com/en-US/recruiting/microchiphr/External/...").
func workdayTenant(host string, segs []string) string {
	if hostIs(host, "myworkdaysite.com") {
		for i, s := range segs[:max(len(segs)-1, 0)] {
			if strings.EqualFold(s, "recruiting") {
				return strings.ToLower(segs[i+1])
			}
		}
	}
	return strings.Split(host, ".")[0]
}

func classifyGreenhouse(u *url.URL, host string, segs []string, q url.Values) (Classification, *stop) {
	not := &stop{class: ClassPosting, reason: notAnApplication}
	switch {
	case len(segs) > 0 && segs[0] == "embed":
		// Only the application page of the embed is an application; its board
		// index (job_board) lists postings.
		if len(segs) < 2 || segs[1] != "job_app" || q.Get("for") == "" || q.Get("token") == "" {
			return Classification{}, not
		}
		return Classification{ATS: ATSGreenhouse, Tenant: q.Get("for"), ApplyURL: u.String()}, nil
	case len(segs) >= 3 && segs[1] == "jobs":
		board, id := segs[0], segs[2]
		if host == "boards.greenhouse.io" || host == "boards.eu.greenhouse.io" {
			return Classification{ATS: ATSGreenhouse, Tenant: board, ApplyURL: greenhouseEmbedURL(u, board, id)}, nil
		}
		return Classification{ATS: ATSGreenhouse, Tenant: board, ApplyURL: u.String()}, nil
	case len(segs) == 1 && q.Get("gh_jid") != "" && host != "boards.greenhouse.io":
		return Classification{ATS: ATSGreenhouse, Tenant: segs[0], ApplyURL: u.String()}, nil
	}
	return Classification{}, not
}

// greenhouseEmbedURL is Greenhouse's own application form for a board's job,
// on the address's port when it has one.
func greenhouseEmbedURL(u *url.URL, board, id string) string {
	h := "job-boards.greenhouse.io"
	if p := u.Port(); p != "" {
		h += ":" + p
	}
	return "https://" + h + "/embed/job_app?" + url.Values{"for": {board}, "token": {id}}.Encode()
}

// greenhouseBoardEmbed is the embed form of a job-boards.greenhouse.io job
// address, and "" for any other address.
func greenhouseBoardEmbed(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if h := u.Hostname(); h != "job-boards.greenhouse.io" && h != "job-boards.eu.greenhouse.io" || len(segs) < 3 || segs[1] != "jobs" {
		return ""
	}
	return greenhouseEmbedURL(u, segs[0], segs[2])
}

// IdentityKey is the run's posting identity: the original URL, whatever was
// rewritten for the walk.
func IdentityKey(raw string) (string, *stop) {
	k, err := identity.URLKey(raw)
	if err != nil {
		return "", &stop{class: ClassPosting, reason: notAnApplication}
	}
	return k, nil
}
