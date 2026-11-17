package apply

import (
	"html"
	"strings"
	"unicode"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// Page-state statements are read narrowly: line by line, as a statement about
// this posting, and a line that ends with a question mark is never the site's
// statement.

var (
	closedRE = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\b(?:this|the)\s+(?:job|position|role|posting|opening|vacancy|requisition|listing)(?:\s+posting)?\s+(?:is|has\s+been|was|has)\s+(?:no\s+longer\s+(?:available|accepting|open|active|taking|posted)|closed|filled|removed|expired|withdrawn|deactivated)\b`),
		lazyregexp.New(`(?i)\b(?:this|the)\s+(?:job|position|role|posting|opening|vacancy|requisition|listing)(?:\s+(?:listing|posting))?\s+(?:no\s+longer\s+(?:exists|available|open|active)|does(?:\s+not|n['\x{2019}]t)\s+exist(?:\s+anymore)?)\b`),
		lazyregexp.New(`(?i)\bno\s+longer\s+accepting\s+(?:job\s+)?applications\b`),
		lazyregexp.New(`(?i)\b(?:job|position|posting|role)\s+(?:you(?:'re|\s+are)\s+looking\s+for\s+)?(?:is|has)\s+(?:no\s+longer|expired)\b`),
		lazyregexp.New(`(?i)\bwe(?:'re|\s+are)\s+no\s+longer\s+(?:accepting|hiring|taking)\b`),
		// Phenom (Chewy): "We're sorry… the job you are trying to apply for has
		// been filled."
		lazyregexp.New(`(?i)\bthe\s+(?:job|position|role)\s+(?:you\s+are|you(?:'|\x{2019})re)\s+trying\s+to\s+apply\s+(?:for|to)\s+(?:has\s+been|is)\s+(?:filled|closed)\b`),
		lazyregexp.New(`(?i)\bapplications?\s+(?:for\s+this\s+(?:job|position|role|posting)\s+)?(?:are|is|has\s+been|have\s+been)\s+closed\b`),
		lazyregexp.New(`(?i)\bthe\s+(?:job|position|role)\s+you\s+(?:are\s+)?(?:looking\s+for|requested|tried\s+to\s+view)\s+(?:is|was|has)\b.*\b(?:unavailable|closed|removed|expired|not\s+found)\b`),
		// Workday's page for a requisition that is gone; the whole line, so a
		// sentence that only mentions such a page is not read as the notice.
		lazyregexp.New(`(?i)^\s*the\s+page\s+you(?:'re|\s+are)\s+looking\s+for\s+does(?:\s+not|n['\x{2019}]t)\s+exist\.?\s*$`),
	}
	alreadyRE = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\byou(?:'ve|\s+have)\s+already\s+applied\b`),
		lazyregexp.New(`(?i)\byou\s+(?:have\s+)?applied\s+(?:to|for|on)\s+this\s+(?:job|position|role|posting)\b`),
		lazyregexp.New(`(?i)\byou(?:'ve|\s+have)\s+(?:already\s+)?submitted\s+(?:an\s+|your\s+)?application\s+(?:to|for)\s+this\b`),
	}
	emailOnlyRE  = lazyregexp.New(`(?i)\b(?:send|email|e-mail|forward|submit)\s+(?:your\s+|a\s+|an\s+)?(?:updated\s+)?(?:resume|résumé|cv|curriculum\s+vitae|application)s?(?:\s+(?:and|with)\s+(?:a\s+)?(?:cover\s+letter|resume|cv))?\s+(?:directly\s+)?(?:to|at)\s*:?\s*[\w.+-]+@[\w-]+(?:\.[\w-]+)+`)
	emailApplyRE = lazyregexp.New(`(?i)\bto\s+apply,?\s+(?:please\s+)?(?:send|email|e-mail)\b.*[\w.+-]+@[\w-]+(?:\.[\w-]+)+`)
	botRE        = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)^just\s+a\s+moment\.{0,3}$`),
		lazyregexp.New(`(?i)\bchecking\s+(?:if\s+the\s+site\s+connection\s+is\s+secure|your\s+browser\s+before\s+accessing)\b`),
		lazyregexp.New(`(?i)\bverify\s+(?:you\s+are|that\s+you\s+are)\s+(?:a\s+)?human\b`),
		lazyregexp.New(`(?i)\bpress\s*(?:&|and)\s*hold\s+to\s+confirm\s+you\s+are\s+(?:a\s+)?human\b`),
		lazyregexp.New(`(?i)\bpardon\s+our\s+interruption\b`),
		lazyregexp.New(`(?i)\battention\s+required!?\s*\|?\s*cloudflare\b`),
		lazyregexp.New(`(?i)\bour\s+systems\s+have\s+detected\s+unusual\s+traffic\b`),
		lazyregexp.New(`(?i)\bradware\s+bot\s+manager\b`),
		lazyregexp.New(`(?i)\bincapsula\s+incident\s+id\b`),
		lazyregexp.New(`(?i)\benable\s+javascript\s+and\s+cookies\s+to\s+continue\b`),
	}
)

// StatementLines returns the lines that can be a statement: not empty and not
// a question.
func statementLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasSuffix(l, "?") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func firstMatch(lines []string, res []*lazyregexp.Regexp) string {
	for _, l := range statementLines(lines) {
		for _, re := range res {
			if re.MatchString(l) {
				return l
			}
		}
	}
	return ""
}

// ClosedNotice returns the page's own notice that the posting is closed, or "".
// "Closed" alone, "applications close on a date" and "open enrolment is
// closed" do not match: only verb phrases a withdrawn posting produces do. A
// phrase inside a time, condition or relative clause ("open until the
// position is filled", "the level at which the role is filled") is not a
// notice.
func ClosedNotice(lines []string) string {
	for _, l := range statementLines(lines) {
		for _, re := range closedRE {
			if loc := re.FindStringIndex(l); loc != nil && !clauseLeadRE.MatchString(l[:loc[0]]) {
				return l
			}
		}
	}
	return ""
}

var clauseLeadRE = lazyregexp.New(`(?i)\b(?:until|till|unless|once|when|whenever|if|after|before|which|where)\s*$`)

// AlreadyAppliedNotice reads second-person perfect-tense sentences about this
// job; a form's question about earlier applications never matches.
func AlreadyAppliedNotice(lines []string) string { return firstMatch(lines, alreadyRE) }

// EmailOnlyNotice reads "send your resume to <address>" shapes. The caller
// asks only where the walk found no application form.
func EmailOnlyNotice(lines []string) string {
	for _, l := range statementLines(lines) {
		if emailOnlyRE.MatchString(l) || emailApplyRE.MatchString(l) {
			return l
		}
	}
	return ""
}

// BotInterstitial recognises an anti-bot interstitial by its own copy only; a
// job description about security never matches. It names what was seen and
// solves nothing.
func BotInterstitial(title string, lines []string) string {
	if firstLine := strings.TrimSpace(title); firstLine != "" {
		for _, re := range botRE {
			if re.MatchString(firstLine) {
				return firstLine
			}
		}
	}
	if len(lines) > 40 {
		lines = lines[:40] // an interstitial is a short page
	}
	return firstMatch(lines, botRE)
}

var jobNounRE = lazyregexp.New(`(?i)\b(?:engineer|developer|manager|analyst|scientist|designer|specialist|director|architect|consultant|intern|internship|associate|lead|administrator|coordinator|representative|technician|officer|researcher|recruiter|accountant|assistant|advisor|writer|editor|producer|nurse|operator|supervisor|strategist|programmer|owner|partner|president|vp|head)\b`)

var landmarkRE = lazyregexp.New(`(?i)^(?:job\s+)?application(?:\s+form)?$|^apply(?:\s+(?:now|for\s+this\s+job))?$|^(?:sign\s*in|log\s*in|register|create\s+(?:an\s+)?account|sign\s*up)\b`)

// PageRole returns the role a page names for itself: the first heading that
// reads as a job title, else the page title. A title that is only the site's
// own name, an apply-flow landmark heading ("job application form") and a
// title naming sign-in or register with no job noun read as no role ("").
func PageRole(title string, headings []string, siteName string) string {
	cands := append(append([]string{}, headings...), title)
	for _, c := range cands {
		c = strings.TrimSpace(c)
		if c == "" || landmarkRE.MatchString(c) {
			continue
		}
		if siteName != "" && strings.EqualFold(c, siteName) {
			continue
		}
		if jobNounRE.MatchString(c) {
			return c
		}
	}
	return ""
}

var markupRE = lazyregexp.New(`<[^>]*>`)

// pageName makes text a page states fit a title on the board: character
// references decoded once, tags and the characters that open one gone, control
// and invisible formatting characters gone, whitespace one space, at most n
// characters. What it cannot make plain it drops.
func pageName(s string, n int) string {
	s = markupRE.ReplaceAllString(html.UnescapeString(s), " ")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case r == '<' || r == '>' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// cohortLabelRE is the new-grad cohort a list files a row under ("New Grad
// 2027: "), which the employer's own page does not carry.
var cohortLabelRE = lazyregexp.New(`(?i)^\s*(?:new|university|college|campus)\s+grad(?:uate)?s?(?:\s+\d{4})?\s*:\s*`)

// StaleLink reports whether the page's own role names a different job than the
// evaluated report. A role of the same words with other punctuation, a level
// suffix or a list's new-grad cohort label is the same job. No role on the
// page, or no wanted role, is not stale.
func StaleLink(pageRole, wantRole, company string) bool {
	if pageRole == "" || strings.TrimSpace(wantRole) == "" {
		return false
	}
	pageRole, wantRole = cohortLabelRE.ReplaceAllString(pageRole, ""), cohortLabelRE.ReplaceAllString(wantRole, "")
	pageRole, wantRole = identity.RoleWithoutCompany(pageRole, company), identity.RoleWithoutCompany(wantRole, company)
	a := identity.Posting{Company: orDefault(company, "x"), Role: pageRole}
	b := identity.Posting{Company: orDefault(company, "x"), Role: wantRole}
	return !identity.SameCandidacyStrict(a, b) && !strings.Contains(strings.ToLower(pageRole), strings.ToLower(strings.TrimSpace(wantRole)))
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
