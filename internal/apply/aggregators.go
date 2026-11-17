package apply

import (
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// aggregatorHosts are job boards that re-post other employers' jobs, or whose
// pages put an account or a bot wall of their own in front of the application.
// A run never walks them when the employer's own posting is known.
var aggregatorHosts = []string{
	"ziprecruiter.com", "jobright.ai", "ihire.com", "ihiretechnology.com", "remotejobs.dedyn.io", "remoshift.com",
	"emploive.com", "talenthop.com", "localjobs.com", "recentlypostedjobs.com", "jaabz.com", "christran.gg",
	"topsail.partners", "pragatishilclasses.org", "mysmartpros.com", "glassdoor.com", "tealhq.com",
	// Google Jobs and LinkedIn hand these over for DoorDash, Color, Loop, NVIDIA and others.
	"monster.com", "newgrad.ai", "bestjobtool.com", "remotehunter.com", "digitalhire.com",
	// Vaia re-posts listings behind its own multi-step form (a date of birth, a finance-advice offer).
	"vaia.com",
	// LinkedIn feed reposts link through lnkd.in to Hirecept, whose page leads on to other boards and a sign-up form.
	"hirecept.com",
}

var (
	// Indeed's job-view pages are a mirror; its own apply hosts are forms.
	indeedViewRE = lazyregexp.New(`(?i)^/(?:m/)?viewjob\b|^/(?:rc|pagead)/clk\b`)
	// A university career centre re-posting a job: /jobs/<company>-<role>.
	eduMirrorRE = lazyregexp.New(`(?i)^/jobs/[a-z0-9]+(?:-[a-z0-9]+)+/?$`)
)

// IsAggregatorURL reports whether the URL is a job-board mirror and not the
// employer's own posting.
func IsAggregatorURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if anyHost(host, aggregatorHosts) {
		return true
	}
	if hostIs(host, "indeed.com") && indeedViewRE.MatchString(u.Path) {
		return true
	}
	return strings.HasSuffix(host, ".edu") && eduMirrorRE.MatchString(u.Path)
}

// AggregatorHosts lists the mirror hosts by domain, for a web search that
// should leave them out.
func AggregatorHosts() []string {
	return append([]string{"indeed.com", "linkedin.com"}, aggregatorHosts...)
}
