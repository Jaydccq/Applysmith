package browser

import "strings"

// SignInSite is one entry of the site list the sign-in probe and the sign-in
// sync share. Cookies names the cookies that prove a sign-in, where the site
// is known to define them. SignInURL is where the site asks for a sign-in and
// AccountURL a page that only a signed-in profile can load at its own address.
type SignInSite struct {
	ID         string
	Label      string
	Domain     string
	SignInURL  string
	AccountURL string
	Cookies    []string
}

// SignInSites lists the job sites whose sign-in is worth carrying over: the
// sites the scanners read through the browser.
var SignInSites = []SignInSite{
	{ID: "linkedin", Label: "LinkedIn", Domain: "linkedin.com", SignInURL: "https://www.linkedin.com/login", AccountURL: "https://www.linkedin.com/feed/", Cookies: []string{"li_at"}},
	{ID: "indeed", Label: "Indeed", Domain: "indeed.com", SignInURL: "https://secure.indeed.com/auth", AccountURL: "https://www.indeed.com/account/view"},
	{ID: "jobright", Label: "Jobright", Domain: "jobright.ai", SignInURL: "https://jobright.ai/", AccountURL: "https://jobright.ai/jobs/recommend"},
	{ID: "ziprecruiter", Label: "ZipRecruiter", Domain: "ziprecruiter.com", SignInURL: "https://www.ziprecruiter.com/authn/login", AccountURL: "https://www.ziprecruiter.com/candidate/profile"},
	{ID: "glassdoor", Label: "Glassdoor", Domain: "glassdoor.com", SignInURL: "https://www.glassdoor.com/profile/login_input.htm", AccountURL: "https://www.glassdoor.com/member/profile/"},
}

// SiteOf returns the listed site host belongs to.
func SiteOf(host string) (SignInSite, bool) {
	host = strings.ToLower(host)
	for _, s := range SignInSites {
		if scoped(host, s.Domain) {
			return s, true
		}
	}
	return SignInSite{}, false
}

// proofCookies returns the names that prove a sign-in on host, or nil when no
// listed site above it defines any.
func proofCookies(host string) []string {
	var names []string
	for _, s := range SignInSites {
		if scoped(host, s.Domain) {
			names = append(names, s.Cookies...)
		}
	}
	return names
}
