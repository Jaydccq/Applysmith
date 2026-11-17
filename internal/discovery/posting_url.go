package discovery

import (
	"errors"
	"net/url"
	"strings"

	"applysmith/internal/identity"
)

// errUnsafeURL is a posting URL that is well-formed but points at the user's
// own machine or network.
var errUnsafeURL = errors.New("discovery: posting URL points at a local or private host")

// postingURL returns the canonical form of a vendor posting URL, or an error
// when it is not an absolute http(s) URL or names a host that is not public,
// by the predicate the Ask Agent's page reader shares. The evaluator opens
// these URLs and the dashboard links them, so a vendor payload must not be
// able to aim either at a local service.
func postingURL(raw string) (string, error) {
	canon, err := identity.CanonicalURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(canon)
	if err != nil {
		return "", err
	}
	if identity.NonPublicHost(u.Hostname()) != "" {
		return "", errUnsafeURL
	}
	return greenhouseBoardURL(u), nil
}

// greenhouseBoardURL writes a Greenhouse application-frame URL
// (job-boards.greenhouse.io/embed/job_app?for=X&token=N, what Jobright links)
// as the posting page it embeds, job-boards.greenhouse.io/X/jobs/N, so the
// stored address opens the posting. Any other URL is returned as it is.
func greenhouseBoardURL(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if (host != "job-boards.greenhouse.io" && host != "boards.greenhouse.io") || len(segs) != 2 || !strings.EqualFold(segs[0], "embed") || !strings.EqualFold(segs[1], "job_app") {
		return u.String()
	}
	q := u.Query()
	board, id := q.Get("for"), q.Get("token")
	if !isDigitString(id) {
		id = q.Get("gh_jid")
	}
	if board == "" || !isDigitString(id) || strings.ContainsAny(board, "/?#") {
		return u.String()
	}
	return "https://" + host + "/" + url.PathEscape(board) + "/jobs/" + id
}

func isDigitString(s string) bool {
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

// FilterNonPostingURL is the filter reason of a row whose URL is a listing,
// search or recommendation surface.
const FilterNonPostingURL = "non-posting-url"

// nonPostingPaths are the listing, search and recommendation paths of the
// deny-list. The list is the whole test: a shape it does not name is
// admitted, because an allow-list would drop the postings of every new board.
var nonPostingPaths = map[string]bool{
	"/jobs": true, "/careers": true, "/jobs/recommend": true,
	"/jobs/search": true, "/jobs/search-results": true, "/job-search": true, "/jobs-search": true, "/search": true,
}

// postingIDParams are query parameters that name one posting on a listing
// path: an employer's own careers page embedding Greenhouse (gh_jid), an
// Eightfold, iCIMS or Oracle board (pid, jid, jobId), ZipRecruiter's results
// page with its pane on one posting (lk) and the like.
var postingIDParams = []string{"gh_jid", "pid", "jid", "jobid", "job_id", "reqid", "req_id", "requisitionid", "id", "lk"}

// NonPostingURL reports whether a canonical http(s) URL names a listing, a
// search or a feed page and not one posting.
func NonPostingURL(canon string) bool {
	u, err := url.Parse(canon)
	if err != nil {
		return true
	}
	path := strings.TrimRight(strings.ToLower(u.Path), "/")
	if path == "" {
		return false
	}
	if nonPostingPaths[path] {
		return !namesPosting(u.Query())
	}
	host := strings.ToLower(u.Hostname())
	if host == "linkedin.com" || strings.HasSuffix(host, ".linkedin.com") {
		// A collections or feed page names a job only in currentJobId, which
		// canonicalization has already folded into /jobs/view/{id}.
		return strings.HasPrefix(path, "/jobs/collections") || path == "/feed" || strings.HasPrefix(path, "/feed/")
	}
	return false
}

// namesPosting reports whether the query carries a non-empty posting id.
func namesPosting(q url.Values) bool {
	for k, v := range q {
		for _, id := range postingIDParams {
			if strings.EqualFold(k, id) && len(v) > 0 && strings.TrimSpace(v[0]) != "" {
				return true
			}
		}
	}
	return false
}
