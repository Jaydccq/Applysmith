package apply

import (
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Deterministic posting facts: answers the check stage gives before any model
// call, each narrowly and never outside its shape.

var (
	internshipQuestionRE = lazyregexp.New(`(?i)\b(?:are\s+you\s+applying|do\s+you\s+want\s+to\s+apply|is\s+this)\b[^?]*\b(?:intern(?:ship)?|co-?op)\b`)
	internshipPastRE     = lazyregexp.New(`(?i)\b(?:previous|prior|past|completed|have\s+you|did\s+you|where\s+was|last|most\s+recent)\b`)
	internRE             = lazyregexp.New(`(?i)\b(?:intern(?:ship)?s?|co-?op|student|apprentice(?:ship)?)\b`)
	fullTimeRE           = lazyregexp.New(`(?i)\b(?:full[-\s]?time|regular|new[-\s]?grad(?:uate)?|permanent)\b`)
)

// InternshipAnswer answers "Are you applying for an internship or co-op?" from
// the evaluated report's role title: No only for an explicit full-time,
// regular or new-grad title with no intern, co-op, student or apprentice
// wording; Yes only for an explicit intern or co-op title with no full-time
// wording. Anything else (no title, both markers, no Yes/No option, a question
// about past internships) is not answered here. The posting body is never read.
func InternshipAnswer(label, roleTitle string, options []string) (string, bool) {
	if !internshipQuestionRE.MatchString(label) || internshipPastRE.MatchString(strings.ReplaceAll(strings.ToLower(label), "are you applying", "")) {
		return "", false
	}
	title := strings.TrimSpace(roleTitle)
	if title == "" {
		return "", false
	}
	intern, full := internRE.MatchString(title), fullTimeRE.MatchString(title)
	var want string
	switch {
	case full && !intern:
		want = "No"
	case intern && !full:
		want = "Yes"
	default:
		return "", false
	}
	if len(options) == 0 {
		return want, true
	}
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(o), want) {
			return o, true
		}
	}
	return "", false
}

var maintenanceRE = lazyregexp.New(`(?i)\b(?:currently\s+(?:undergoing|down\s+for)\s+(?:scheduled\s+)?maintenance|scheduled\s+maintenance|temporarily\s+unavailable|service\s+is\s+unavailable|down\s+for\s+maintenance)\b`)

// WorkdayUnavailable reads where a Workday run's entry page ended while
// Workday was not serving the tenant: its maintenance page on
// community.workday.com, or the employer's own site the tenant redirects to
// in a maintenance window (invesco.wd1 to careers.invesco.com). Either page
// shows no sign-in wall and reads as signed in. "" while the run stays on
// Workday.
func WorkdayUnavailable(ats, pageURL string) string {
	u, err := url.Parse(pageURL)
	if ats != ATSWorkday || err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "community.workday.com" && strings.HasPrefix(u.Path, "/maintenance-page"):
		return "Workday is down for scheduled maintenance (" + redactURL(pageURL) + ")"
	case hostIs(host, "myworkdayjobs.com") || hostIs(host, "myworkdaysite.com") || hostIs(host, "myworkday.com"):
		return ""
	}
	return "Workday sent the tenant off its own site to " + host + " instead of the posting, its usual maintenance redirect"
}

// MaintenanceNotice returns the page's own notice that the site is down for a
// while: the stop is class unavailable and the row is not held back for the
// rest of the day.
func MaintenanceNotice(lines []string) string {
	return firstMatch(lines, []*lazyregexp.Regexp{maintenanceRE})
}
