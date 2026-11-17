// Package identity is the one definition of posting identity: the key that
// names a posting whatever URL it was read from, and the two predicates that
// decide whether two postings or applications are the same. Discovery, the
// tracker, the report index and the apply gates all call it. It does no I/O.
package identity

import (
	"errors"
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// ErrNotURL reports input that is not an absolute http or https URL.
var ErrNotURL = errors.New("identity: not an absolute http(s) URL")

// stepSegments are trailing path segments that belong to the application flow
// of a posting, not to the posting itself.
var stepSegments = map[string]bool{
	"apply": true, "apply-manually": true, "autofill-with-resume": true, "login": true, "sign-in": true,
}

// trackingParams are removed from every URL; utm_* is matched by prefix.
var trackingParams = map[string]bool{
	"ref": true, "source": true, "gh_src": true, "lever-source": true, "jr_id": true, "cmpid": true, "ss": true,
}

// workdayLocaleRE is the locale prefix of a Workday path ("en-US").
var workdayLocaleRE = lazyregexp.New(`^[A-Za-z]{2}(?:-[A-Za-z]{2,4})?$`)

// gluedQueryRE is a query pasted onto the last path segment with "&" in place
// of "?" ("/jobs/4012345&gh_src=abc"): a name=value pair with no slash after.
var gluedQueryRE = lazyregexp.New(`&([A-Za-z_][A-Za-z0-9_.-]*=[^/]*)$`)

func parseWeb(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, ErrNotURL
	}
	// A glued query becomes the query it was meant to be, so the path names
	// the posting alone: Greenhouse otherwise reads "4012345&gh_src=abc" as
	// the job token.
	if m := gluedQueryRE.FindStringSubmatchIndex(u.Path); m != nil {
		glued := u.Path[m[2]:m[3]]
		u.Path, u.RawPath = u.Path[:m[0]], ""
		if u.RawQuery != "" {
			glued += "&" + u.RawQuery
		}
		u.RawQuery = glued
	}
	return u, nil
}

// pathSegments splits a path into its non-empty segments, which collapses
// repeated and trailing slashes.
func pathSegments(p string) []string {
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

func hostOf(u *url.URL) string {
	h := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		h += ":" + p
	}
	return h
}

func hostIs(h, domain string) bool { return h == domain || strings.HasSuffix(h, "."+domain) }

// isIndeed reports whether h is on one of Indeed's registrable domains:
// indeed.com, a country domain such as indeed.de or indeed.co.uk, or a
// subdomain of one. A host that only holds "indeed" elsewhere is not.
func isIndeed(h string) bool {
	labels := strings.Split(h, ".")
	for i, l := range labels {
		if l == "indeed" {
			rest := labels[i+1:]
			return len(rest) == 1 || len(rest) == 2 && (rest[0] == "co" || rest[0] == "com")
		}
	}
	return false
}

// CanonicalURL returns the stable form of a posting URL: lowercase host, https,
// no fragment, collapsed slashes, no trailing slash or application-step
// segment, no tracking parameters, and on known hosts only the parts that name
// the posting.
func CanonicalURL(raw string) (string, error) {
	u, err := parseWeb(raw)
	if err != nil {
		return "", err
	}
	host := hostOf(u)
	segs := pathSegments(u.Path)
	q := u.Query()
	keep := func(names ...string) url.Values {
		out := url.Values{}
		for _, n := range names {
			if v, ok := q[n]; ok {
				out[n] = v
			}
		}
		return out
	}
	var query url.Values

	switch {
	case hostIs(host, "linkedin.com"):
		if id := linkedInJobID(segs, q); id != "" {
			return "https://www.linkedin.com/jobs/view/" + id, nil
		}
		query = withoutTracking(q)
	case hostIs(host, "amazon.jobs"):
		for i := 0; i+1 < len(segs); i++ {
			if strings.EqualFold(segs[i], "jobs") && isDigits(segs[i+1]) {
				return "https://www.amazon.jobs/jobs/" + segs[i+1], nil
			}
		}
		query = withoutTracking(q)
	case isIndeed(host):
		query = keep("jk")
	case hostIs(host, "greenhouse.io"):
		switch {
		case len(segs) > 0 && strings.EqualFold(segs[0], "embed"):
			query = keep("for", "token", "gh_jid")
		case !pathNamesJob(segs):
			// A board index with gh_jid still names the posting; without the
			// parameter it would open the job list.
			query = keep("gh_jid")
		}
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co" || host == "jobs.ashbyhq.com" || host == "jobs.eu.ashbyhq.com",
		host == "jobs.smartrecruiters.com" || host == "careers.smartrecruiters.com":
		// Query-free hosts: the path names the posting.
		if strings.Contains(host, "ashbyhq.com") && len(segs) > 0 && strings.EqualFold(segs[len(segs)-1], "application") {
			segs = segs[:len(segs)-1]
		}
	case strings.HasSuffix(host, ".myworkdayjobs.com") || strings.HasSuffix(host, ".myworkdaysite.com"):
		segs = cutWorkdayApply(segs)
		if job, ok := workdayJob(segs); ok {
			segs = job
		} else {
			query = withoutTracking(q)
		}
	default:
		query = withoutTracking(q)
	}

	for len(segs) > 1 && stepSegments[strings.ToLower(segs[len(segs)-1])] {
		segs = segs[:len(segs)-1]
	}
	out := "https://" + host
	if len(segs) > 0 {
		out += "/" + strings.Join(segs, "/")
	}
	if enc := query.Encode(); enc != "" {
		out += "?" + enc
	}
	return out, nil
}

// pathNamesJob reports whether the path holds a /jobs/{id} pair.
func pathNamesJob(segs []string) bool {
	for i := 0; i+1 < len(segs); i++ {
		if strings.EqualFold(segs[i], "jobs") {
			return true
		}
	}
	return false
}

func cutWorkdayApply(segs []string) []string {
	jobAt := -1
	for i, s := range segs {
		if strings.EqualFold(s, "job") {
			jobAt = i
			break
		}
	}
	if jobAt < 0 {
		return segs
	}
	for i := jobAt + 1; i < len(segs); i++ {
		if strings.EqualFold(segs[i], "apply") {
			return segs[:i]
		}
	}
	return segs
}

// workdayJob reduces a Workday job path to the parts that name the posting:
// the site, lowercased (Workday reads it in any case), "job", and the title
// segment that ends in the requisition id. A locale prefix, a location segment
// before the title and the query name no other posting, so one requisition
// read through any of them is one URL ("/en-US/Careers_GM/job/X_JR-1" and
// "/careers_gm/job/Austin-Texas/X_JR-1?share_id=y" are both
// "/careers_gm/job/X_JR-1").
func workdayJob(segs []string) ([]string, bool) {
	at := -1
	for i, s := range segs {
		if strings.EqualFold(s, "job") {
			at = i
			break
		}
	}
	if at < 1 || at == len(segs)-1 {
		return nil, false
	}
	site := segs[:at]
	if len(site) > 1 && workdayLocaleRE.MatchString(site[0]) {
		site = site[1:]
	}
	out := make([]string, 0, len(site)+2)
	for _, s := range site {
		out = append(out, strings.ToLower(s))
	}
	return append(out, "job", segs[len(segs)-1]), true
}

func withoutTracking(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || trackingParams[lk] {
			continue
		}
		out[k] = v
	}
	return out
}

// linkedInJobID reads the numeric job id from /jobs/view/{slug-}{id} or from
// the currentJobId parameter of any /jobs/ page.
func linkedInJobID(segs []string, q url.Values) string {
	if len(segs) >= 3 && strings.EqualFold(segs[0], "jobs") && strings.EqualFold(segs[1], "view") {
		last := segs[2]
		if i := strings.LastIndex(last, "-"); i >= 0 {
			last = last[i+1:]
		}
		if isDigits(last) {
			return last
		}
	}
	if len(segs) >= 1 && strings.EqualFold(segs[0], "jobs") && isDigits(q.Get("currentJobId")) {
		return q.Get("currentJobId")
	}
	return ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
