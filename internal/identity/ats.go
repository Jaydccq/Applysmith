package identity

import (
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The five ATSs whose URLs yield a native key.
const (
	Greenhouse      = "greenhouse"
	Lever           = "lever"
	Ashby           = "ashby"
	SmartRecruiters = "smartrecruiters"
	Workable        = "workable"
)

var atsNames = map[string]bool{Greenhouse: true, Lever: true, Ashby: true, SmartRecruiters: true, Workable: true}

var (
	slugRE  = lazyregexp.New(`^[a-z0-9][a-z0-9._-]*$`)
	tokenRE = lazyregexp.New(`^[a-z0-9][a-z0-9-]{5,}$`)
)

// infraLabels are vendor subdomains that are not boards.
var infraLabels = map[string]bool{
	"api": true, "www": true, "support": true, "status": true, "app": true, "apply": true, "help": true,
	"developers": true, "docs": true, "resources": true, "boards": true, "job-boards": true, "boards-api": true,
}

// IsATSNative reports whether key is an ATS-native identity key
// `{ats}:{slug}/{job id}`. A `{source}:{source job id}` key is not.
func IsATSNative(key string) bool {
	name, rest, ok := strings.Cut(key, ":")
	if !ok || !atsNames[name] {
		return false
	}
	slug, id, ok := strings.Cut(rest, "/")
	return ok && slugRE.MatchString(slug) && id != ""
}

// ATSKey returns the ATS-native identity key `{ats}:{slug}/{job id}` (slug and
// id lowercased) when the URL, in any host, path or embed form, names a
// posting of a supported ATS.
func ATSKey(raw string) (string, bool) {
	u, err := parseWeb(raw)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	segs := pathSegments(u.Path)
	q := u.Query()
	var ats, slug, id string
	switch {
	case hostIs(host, "greenhouse.io"):
		ats = Greenhouse
		slug, id = greenhouseParts(host, segs, q)
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co":
		ats = Lever
		if len(segs) >= 2 {
			slug, id = segs[0], segs[1]
		}
	case host == "api.lever.co" || host == "api.eu.lever.co":
		ats = Lever
		if len(segs) >= 4 && segs[0] == "v0" && segs[1] == "postings" {
			slug, id = segs[2], segs[3]
		}
	case host == "jobs.ashbyhq.com" || host == "jobs.eu.ashbyhq.com":
		ats = Ashby
		if n := len(segs); n > 0 && strings.EqualFold(segs[n-1], "application") {
			segs = segs[:n-1]
		}
		if len(segs) >= 2 {
			slug, id = segs[0], segs[1]
		} else if len(segs) == 1 {
			slug, id = segs[0], q.Get("ashby_jid")
		}
	case host == "jobs.smartrecruiters.com" || host == "careers.smartrecruiters.com" || host == "api.smartrecruiters.com":
		ats = SmartRecruiters
		slug, id = smartRecruitersParts(host, segs, q)
	case host == "apply.workable.com":
		ats = Workable
		if len(segs) >= 3 && segs[0] != "j" && strings.EqualFold(segs[1], "j") {
			slug, id = segs[0], segs[2]
		}
	case strings.HasSuffix(host, ".workable.com"):
		ats = Workable
		slug = strings.TrimSuffix(host, ".workable.com")
		if len(segs) >= 2 && strings.EqualFold(segs[0], "jobs") {
			id = segs[1]
		}
	default:
		// An employer's page that embeds a Greenhouse board and names the board
		// in its own query (CoreWeave: "?board=coreweave&gh_jid=4709155006").
		// With no board named the posting stays the page's.
		id, slug = digitsOnly(q.Get("gh_jid")), q.Get("board")
		if slug == "" {
			slug = q.Get("for")
		}
		if id == "" || slug == "" {
			return "", false
		}
		ats = Greenhouse
	}
	return NativeKey(ats, slug, id)
}

// NativeKey builds the ATS-native identity key from a board and job id the
// caller already knows, for a posting whose own URL is on the employer's site.
// It applies the same checks as ATSKey, so the two always agree.
func NativeKey(ats, slug, id string) (string, bool) {
	slug, id = strings.ToLower(strings.TrimSpace(slug)), strings.ToLower(strings.TrimSpace(id))
	if !atsNames[ats] || !slugRE.MatchString(slug) || infraLabels[slug] || id == "" {
		return "", false
	}
	if ats == Lever || ats == Ashby {
		if !tokenRE.MatchString(id) {
			return "", false
		}
	} else if !tokenRE.MatchString(id) && !isDigits(id) {
		return "", false
	}
	return ats + ":" + slug + "/" + id, true
}

// greenhouseParts reads board and job id from the board, job-board, EU board,
// embed, API and per-company hosts, and from a gh_jid parameter.
func greenhouseParts(host string, segs []string, q url.Values) (slug, id string) {
	sub := strings.TrimSuffix(host, ".greenhouse.io")
	switch {
	case sub == "boards-api":
		if len(segs) >= 4 && segs[0] == "v1" && segs[1] == "boards" && segs[2] != "" && strings.EqualFold(segs[3], "jobs") && len(segs) >= 5 {
			return segs[2], segs[4]
		}
		return "", ""
	case sub == "boards" || sub == "job-boards" || sub == "boards.eu" || sub == "job-boards.eu":
		if len(segs) > 0 && strings.EqualFold(segs[0], "embed") {
			id = q.Get("token")
			if !isDigits(id) {
				id = q.Get("gh_jid")
			}
			return q.Get("for"), digitsOnly(id)
		}
		if len(segs) == 0 {
			return "", ""
		}
		if len(segs) >= 3 && strings.EqualFold(segs[1], "jobs") {
			return segs[0], digitsOnly(segs[2])
		}
		return segs[0], digitsOnly(q.Get("gh_jid"))
	case !strings.Contains(sub, ".") && sub != host:
		if len(segs) >= 2 && strings.EqualFold(segs[0], "jobs") {
			return sub, digitsOnly(segs[1])
		}
		return sub, digitsOnly(q.Get("gh_jid"))
	}
	return "", ""
}

// smartRecruitersParts reads the company and posting id from the job page, the
// API, the one-click publication path and the one-click job page, which
// carries the id in its query.
func smartRecruitersParts(host string, segs []string, q url.Values) (slug, id string) {
	switch {
	case host == "api.smartrecruiters.com":
		if len(segs) >= 5 && segs[0] == "v1" && segs[1] == "companies" && strings.EqualFold(segs[3], "postings") {
			return segs[2], leadingDigits(segs[4])
		}
	case len(segs) >= 5 && strings.EqualFold(segs[0], "oneclick-ui") && strings.EqualFold(segs[1], "company") && strings.EqualFold(segs[3], "publication"):
		return segs[2], segs[4]
	case len(segs) >= 4 && strings.EqualFold(segs[0], "oneclick-ui") && strings.EqualFold(segs[1], "company") && strings.EqualFold(segs[3], "job"):
		return segs[2], q.Get("id")
	case len(segs) >= 2 && !strings.EqualFold(segs[0], "oneclick-ui"):
		return segs[0], leadingDigits(segs[1])
	}
	return "", ""
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// digitsOnly returns s when it is all digits, else "".
func digitsOnly(s string) string {
	if isDigits(s) {
		return s
	}
	return ""
}

// URLKey is the identity key of a posting URL: the ATS-native key when the URL
// names a posting of a supported ATS, else the canonical URL.
func URLKey(raw string) (string, error) {
	if k, ok := ATSKey(raw); ok {
		return k, nil
	}
	return CanonicalURL(raw)
}
