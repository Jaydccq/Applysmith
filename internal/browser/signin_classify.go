package browser

import (
	"net/url"
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// SignInState is what the sign-in probe concluded about a site.
type SignInState string

// Sign-in states.
const (
	StateSignedIn    SignInState = "signed_in"
	StateSignedOut   SignInState = "signed_out"
	StateCheckpoint  SignInState = "checkpoint"
	StateCaptcha     SignInState = "captcha"
	StateRateLimited SignInState = "rate_limited"
	StateUnknown     SignInState = "unknown"
)

// PageReading is what the probe read from a loaded account page. SignInForm is
// true when the page shows a password box or a sign-in call-to-action. Frames
// are the sources of the page's frames. Cookies are the names present in the
// profile for the site.
type PageReading struct {
	Status     int
	URL        string
	Title      string
	Text       string
	SignInForm bool
	Frames     []string
	Cookies    []string
	LoadFailed bool
}

var (
	rateLimitRE   = lazyregexp.New(`(?i)\b(?:too many requests|rate[- ]limit(?:ed)?|request limit|slow down)\b`)
	challengeRE   = lazyregexp.New(`(?i)\b(?:are you (?:a )?human|verify (?:that )?you are (?:a )?human|additional verification required|checking (?:if|your) (?:the )?(?:site )?connection is secure|unusual traffic|access denied|you have been blocked|press (?:and|&) hold)\b`)
	cloudflareRE  = lazyregexp.New(`(?i)^\s*just a moment`)
	checkpointURL = lazyregexp.New(`(?i)/(?:checkpoint|authwall|uas/login-submit|security-verification|identity-verification)\b`)
	checkpointTTL = lazyregexp.New(`(?i)\b(?:security verification|security check|verify your identity|identity verification)\b`)
	identityRE    = lazyregexp.New(`(?i)verify your identity`)
	closedRE      = lazyregexp.New(`(?i)\b(?:no longer available|page (?:was )?not found|job (?:has )?(?:expired|been removed)|no longer accepting)\b`)
	challengeHost = []string{"challenges.cloudflare.com", "captcha-delivery.com", "hcaptcha.com", "recaptcha", "arkoselabs.com", "funcaptcha.com", "perimeterx.net"}
)

// wallTextLimit is how much of a page's text the wording rules read. A wall
// says what it is at the top; further down is the page's own content (a feed,
// a list of jobs), where the same words are somebody's post.
const wallTextLimit = 400

// ClassifySignIn applies the probe's rules in order and returns the first
// that matches. The wording rules read the head of the text only. A challenge
// frame beside a sign-in form is the form's own widget, so that page is
// signed_out. A site that names sign-in cookies needs one of them for
// signed_in.
func ClassifySignIn(site SignInSite, r PageReading) SignInState {
	if r.LoadFailed {
		return StateUnknown
	}
	text := truncate(r.Text, wallTextLimit)
	signIn := onSignInPage(site, r.URL) || r.SignInForm
	switch {
	case r.Status == 429 || rateLimitRE.MatchString(text):
		return StateRateLimited
	case r.Status == 403 || challengeRE.MatchString(text) || cloudflareRE.MatchString(r.Title) || challengeFrame(r.Frames) && !signIn || challengeFrame([]string{r.URL}):
		return StateCaptcha
	case checkpointURL.MatchString(pathOf(r.URL)) || checkpointTTL.MatchString(r.Title) || identityRE.MatchString(text):
		return StateCheckpoint
	case r.Status == 404 || closedRE.MatchString(r.Title) || closedRE.MatchString(text):
		return StateUnknown
	case signIn:
		return StateSignedOut
	case !atAccountURL(site, r.URL):
		return StateUnknown
	}
	if len(site.Cookies) > 0 && !slices.ContainsFunc(site.Cookies, func(n string) bool { return slices.Contains(r.Cookies, n) }) {
		return StateUnknown
	}
	return StateSignedIn
}

// NextAction projects a state to what the caller should do: proceed, retry
// later, ask the user, or stop because a safety check is in the way. It is
// pause evidence, never an instruction to bypass a check.
func NextAction(s SignInState) string {
	switch s {
	case StateSignedIn, StateUnknown:
		return "proceed"
	case StateRateLimited:
		return "retry-later"
	case StateSignedOut, StateCheckpoint:
		return "needs-user"
	case StateCaptcha:
		return "blocked-by-safety"
	}
	return "proceed"
}

func challengeFrame(frames []string) bool {
	for _, f := range frames {
		low := strings.ToLower(f)
		for _, h := range challengeHost {
			if strings.Contains(low, h) {
				return true
			}
		}
	}
	return false
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

func trimPath(p string) string { return strings.TrimRight(p, "/") }

// onSignInPage reports that the page landed on the site's sign-in address.
func onSignInPage(site SignInSite, raw string) bool {
	want, err := url.Parse(site.SignInURL)
	if err != nil || site.SignInURL == "" {
		return false
	}
	got, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(got.Hostname(), want.Hostname()) && trimPath(got.Path) == trimPath(want.Path)
}

// atAccountURL reports that the page stayed on the account page it asked for:
// same host, and the requested path or a page below it.
func atAccountURL(site SignInSite, raw string) bool {
	want, err := url.Parse(site.AccountURL)
	if err != nil || site.AccountURL == "" {
		return false
	}
	got, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(got.Hostname(), want.Hostname()) {
		return false
	}
	w, g := trimPath(want.Path), trimPath(got.Path)
	return g == w || strings.HasPrefix(g, w+"/")
}
