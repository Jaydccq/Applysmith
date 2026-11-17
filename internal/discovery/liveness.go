// Clean-room: docs/provenance.md#CR-LIVENESS

package discovery

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// Liveness verdicts.
const (
	LiveActive    = "active"
	LiveExpired   = "expired"
	LiveUncertain = "uncertain"
	LiveMismatch  = "mismatch"
)

// Liveness bounds from the requirement.
const (
	LivenessNavTimeout  = 30 * time.Second
	LivenessReadWait    = 8 * time.Second
	livenessMinText     = 200
	livenessClosureHead = 4000
)

// PageRead is what a browser read of a posting URL reported: the main
// navigation and the rendered page, with navigation, footer, related-jobs
// blocks, cookie or consent notices and other postings' cards already left out
// of Text, Banner and HasApply.
type PageRead struct {
	// Status is the HTTP status of the document that was read, and FinalURL
	// its address: where redirects, script redirects and a challenge's reload
	// left the page.
	Status   int
	FinalURL string
	Title    string
	Heading  string
	// PostingTitle is the title of the page's JobPosting structured data,
	// when it has one; a site banner can be the first heading.
	PostingTitle string
	// Banner is the text of a banner at the top of the page.
	Banner string
	Text   string
	// Description is the posting's own description where the page marks it:
	// a job board's description block (ListingDescription) or the
	// description of the page's JobPosting data. Text can open with a site's
	// search form; this cannot.
	Description string
	// ApplyLinks are the addresses the page's apply links name, in page order.
	ApplyLinks []string
	// Canonical is the page's canonical link.
	Canonical string
	// HasApply is true when an enabled apply control exists outside the
	// related-jobs blocks.
	HasApply bool
	// PasswordForm is true when the page's only content is a sign-in form.
	PasswordForm bool
	// NoPosting is true when the page is a script-rendered job shell (Workday)
	// whose posting data had not appeared when the read ended: its text and
	// title are the site's, not the posting's.
	NoPosting bool
	// LoadFailed and TimedOut report a navigation that did not finish.
	LoadFailed, TimedOut bool
	// Throttled says the site answered with a throttle status (ThrottleStatus)
	// on the way, though a script may have moved the page on since: LinkedIn's
	// 999 page sends the browser to its sign-in wall.
	Throttled bool
}

// PageReader reads a posting URL in a browser, rendered, so a page built by
// script is judged as a person would see it.
type PageReader interface {
	Read(ctx context.Context, rawURL string) (PageRead, error)
}

// Expect is the identity the page should show; both parts are optional.
type Expect struct{ Company, Role string }

// Liveness is the verdict on one URL.
type Liveness struct {
	Verdict string `json:"verdict"`
	// Reason is a code: http-404, http-410, redirect-lost-job, login, challenge,
	// closure-wording, closure-with-apply, mismatch, apply-control,
	// no-apply-control, unreadable, posting-not-rendered, navigation-failed, timeout.
	Reason string `json:"reason"`
}

var (
	closureRE    = lazyregexp.New(`(?i)no longer (?:available|accepting applications|open)|(?:position|job|role|vacancy|opening) (?:has been|is|was) (?:filled|closed|removed|taken down)|has been (?:filled|removed|closed)|applications? (?:are|is|have been)(?: now)? closed|(?:listing|posting|job|page) (?:has )?expired|job (?:not found|does not exist)|this (?:job|position|posting) (?:is )?(?:closed|unavailable)|no longer exists|the page you(?:'re| are) looking for (?:does(?: not|n['\x{2019}]t) exist|could not be found|can(?:no|')t be found)`)
	loginRE      = lazyregexp.New(`(?i)/(?:login|log-in|signin|sign-in|authwall|uas/login|accounts/login|session/new)(?:/|$|\?)`)
	idTokenRE    = lazyregexp.New(`^(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|\d{3,}|[a-z]*\d{4,}[a-z0-9]*)$`)
	wordSplit    = lazyregexp.New(`[a-z0-9]+`)
	clauseLeadRE = lazyregexp.New(`(?i)\b(?:until|till|unless|once|when|whenever|if|after|before|which|where)\s+(?:(?:the|this|a|an|our)\s+)?$`)
)

// livenessStop are the words a role title shares with every other title.
var livenessStop = map[string]bool{"and": true, "the": true, "of": true, "for": true, "a": true, "an": true, "in": true, "at": true, "to": true,
	"with": true, "remote": true, "hybrid": true, "onsite": true, "full": true, "time": true, "part": true, "i": true, "ii": true, "iii": true}

// ClassifyLiveness applies the rules in their order.
func ClassifyLiveness(requested string, pg PageRead, exp Expect) Liveness {
	if pg.TimedOut {
		return Liveness{LiveUncertain, "timeout"}
	}
	if pg.LoadFailed {
		return Liveness{LiveUncertain, "navigation-failed"}
	}
	// 1. Gone, or sent to a page that no longer names the job.
	switch pg.Status {
	case 404:
		return Liveness{LiveExpired, "http-404"}
	case 410:
		return Liveness{LiveExpired, "http-410"}
	}
	if lostJob(requested, pg.FinalURL) {
		return Liveness{LiveExpired, "redirect-lost-job"}
	}
	// 2. A wall says nothing about the job.
	if w := wallOf(pg); w != "" {
		return Liveness{LiveUncertain, w}
	}
	if pg.Status >= 400 {
		return Liveness{LiveUncertain, "http-" + strconv.Itoa(pg.Status)}
	}
	// 3. Closure wording.
	if StatesClosure(pg) {
		if pg.HasApply {
			return Liveness{LiveUncertain, "closure-with-apply"}
		}
		return Liveness{LiveExpired, "closure-wording"}
	}
	// A job shell that never rendered its posting says nothing about the job.
	if pg.NoPosting {
		return Liveness{LiveUncertain, "posting-not-rendered"}
	}
	// 4. Another posting behind the URL.
	if exp.Role != "" && exp.Company != "" && roleOverlap(exp.Role, pg.Title+" "+pg.Heading) < 1.0/3 && !mentions(pg, exp.Company) {
		return Liveness{LiveMismatch, "mismatch"}
	}
	// 5 and 6.
	switch {
	case len([]rune(pg.Text)) >= livenessMinText && pg.HasApply:
		return Liveness{LiveActive, "apply-control"}
	case len([]rune(pg.Text)) < livenessMinText:
		return Liveness{LiveUncertain, "unreadable"}
	}
	return Liveness{LiveUncertain, "no-apply-control"}
}

func wallOf(pg PageRead) string {
	if challengeRE.MatchString(pg.Title) || challengeRE.MatchString(truncateRunes(pg.Text, 2000)) {
		return "challenge"
	}
	if u, err := url.Parse(pg.FinalURL); err == nil && loginRE.MatchString(u.Path) {
		return "login"
	}
	if pg.PasswordForm {
		return "login"
	}
	return ""
}

// StatesClosure reports whether the page says its posting is closed: closure
// wording in the title, the main heading, the head of the posting's text, or a
// banner sentence about a posting. A banner speaks for the whole site, so its
// words count only in a sentence that names a job, a position, a posting, the
// applications or the page, and never in a sentence about cookies.
func StatesClosure(pg PageRead) bool {
	for _, s := range []string{pg.Title, pg.Heading, truncateRunes(pg.Text, livenessClosureHead)} {
		if closureIn(s, false) {
			return true
		}
	}
	return closureIn(pg.Banner, true)
}

var (
	postingNounRE = lazyregexp.New(`(?i)\b(?:jobs?|positions?|roles?|postings?|listings?|vacanc(?:y|ies)|openings?|requisitions?|applications?|page)\b`)
	sentenceEndRE = lazyregexp.New(`[.!?]`)
)

func closureIn(s string, banner bool) bool {
	for _, loc := range closureRE.FindAllStringIndex(s, -1) {
		// "open until the position is filled" is a condition and "the level at
		// which the role is filled" a relative clause, not a notice.
		if clauseLeadRE.MatchString(s[:loc[0]]) {
			continue
		}
		if banner {
			start, end := 0, len(s)
			if ends := sentenceEndRE.FindAllStringIndex(s[:loc[0]], -1); len(ends) > 0 {
				start = ends[len(ends)-1][1]
			}
			if e := sentenceEndRE.FindStringIndex(s[loc[1]:]); e != nil {
				end = loc[1] + e[1]
			}
			if sentence := s[start:end]; !postingNounRE.MatchString(sentence) || strings.Contains(strings.ToLower(sentence), "cookie") {
				continue
			}
		}
		return true
	}
	return false
}

// PostingAddress is the address of the posting a page read shows: the page's
// canonical link when it is on the page's own host and keeps an id of the
// address the read ended on (a career site names its posting by its own path,
// without the tracking parameters a redirect added), else that address. A
// canonical link that drops a posting-id parameter the address has is not
// taken: Datadog's careers page names /detail/8052118/ but loads its embedded
// Greenhouse form only from /detail/8052118/?gh_jid=8052118.
func PostingAddress(pg PageRead) string {
	final, canon := pg.FinalURL, strings.TrimSpace(pg.Canonical)
	fu, err := url.Parse(final)
	cu, cerr := url.Parse(canon)
	if canon == "" || err != nil || cerr != nil || (cu.Scheme != "http" && cu.Scheme != "https") || !strings.EqualFold(fu.Hostname(), cu.Hostname()) {
		return final
	}
	if namesPosting(fu.Query()) && !namesPosting(cu.Query()) {
		return final
	}
	for _, id := range idTokens(canon) {
		for _, want := range idTokens(final) {
			if id == want {
				return canon
			}
		}
	}
	return final
}

func sigWords(s string) []string {
	var out []string
	for _, w := range wordSplit.FindAllString(strings.ToLower(s), -1) {
		if !livenessStop[w] {
			out = append(out, w)
		}
	}
	return out
}

// roleOverlap is the share of the expected role's significant words that the
// page's title and heading carry.
func roleOverlap(role, page string) float64 {
	want := sigWords(role)
	if len(want) == 0 {
		return 1
	}
	have := map[string]bool{}
	for _, w := range sigWords(page) {
		have[w] = true
	}
	hit := 0
	for _, w := range want {
		if have[w] {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

func mentions(pg PageRead, company string) bool {
	hay := strings.ToLower(pg.Title + " " + pg.Heading + " " + pg.Banner + " " + pg.Text)
	return strings.Contains(hay, strings.ToLower(strings.TrimSpace(company)))
}

// LostJob reports whether a posting address that ended at final has lost
// its job: a redirect to a page that no longer names it.
func LostJob(requested, final string) bool { return lostJob(requested, final) }

var localeSegRE = lazyregexp.New(`^(?:[a-z]{2}(?:[-_][a-z]{2})?|global)$`)

// localeOnly reports whether every path segment is a country or language code.
func localeOnly(segs []string) bool {
	for _, s := range segs {
		if !localeSegRE.MatchString(s) {
			return false
		}
	}
	return len(segs) > 0 && segs[0] != ""
}

// lostJob reports whether a redirect left the posting for a page that no
// longer names it: a board index, a search page or a careers home.
func lostJob(requested, final string) bool {
	if final == "" || final == requested {
		return false
	}
	ids := idTokens(requested)
	if len(ids) == 0 {
		return false
	}
	for _, id := range idTokens(final) {
		for _, want := range ids {
			if id == want {
				return false
			}
		}
	}
	switch LinkKind(final) {
	case LinkCareersIndex, LinkCompanyHome:
		return true
	}
	fu, err := url.Parse(final)
	if err != nil {
		return false
	}
	// The company's careers page behind its own path (/about/careers).
	segs := strings.Split(strings.Trim(strings.ToLower(fu.Path), "/"), "/")
	if careersWords[segs[len(segs)-1]] {
		return true
	}
	// The site's localized home (/us/en, /global/en, /en-us): a Phenom career
	// site sends a requisition that is gone there.
	if localeOnly(segs) {
		return true
	}
	return NonPostingURL(fu.Scheme + "://" + fu.Host + fu.Path)
}

// idTokens are the parts of a URL that identify one posting: numeric and
// uuid-like path segments and the common id query parameters.
func idTokens(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	var out []string
	for _, seg := range strings.Split(strings.ToLower(u.Path), "/") {
		seg = strings.TrimSpace(seg)
		if idTokenRE.MatchString(seg) {
			out = append(out, seg)
		}
		// A slug that ends in an id: software-engineer-12345, or Workday's
		// Senior-Engineer_JR2026916.
		for _, sep := range []string{"-", "_"} {
			if i := strings.LastIndex(seg, sep); i >= 0 && idTokenRE.MatchString(seg[i+1:]) {
				out = append(out, seg[i+1:])
			}
		}
	}
	q := u.Query()
	// token is the posting id of Greenhouse's embedded application (embed/job_app?for=<board>&token=<id>).
	for _, k := range []string{"jk", "gh_jid", "id", "jobid", "job_id", "currentjobid", "jid", "token"} {
		for qk, vs := range q {
			if strings.EqualFold(qk, k) {
				for _, v := range vs {
					out = append(out, strings.ToLower(v))
				}
			}
		}
	}
	return out
}
