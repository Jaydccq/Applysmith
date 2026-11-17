package classify

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

const roleChars = `[A-Za-z0-9,&/()' .+\-\x{2013}\x{2014}]`

func rr(p string) *lazyregexp.Regexp {
	p = strings.ReplaceAll(p, "RC", roleChars)
	p = strings.ReplaceAll(p, "DASH", dash)
	return lazyregexp.New("(?i)" + p)
}

var roleNounRE2 = lazyregexp.New(`(?i)(engineer|scientist|developer|builder|manager|analyst|researcher|intern|architect|consultant|designer|specialist|operations|\bstaff\b|\bsde\b)`)

// Prose that precedes the title in a subject or body capture.
var prosePrefixes = []*lazyregexp.Regexp{
	rr(`^thank\s+you\s+for\s+(?:taking\s+the\s+)?time\s+to\s+express\s+interest\s+in\s+the\s+`),
	rr(`^thank\s+you\s+for\s+(?:taking\s+the\s+)?time\s+(?:to\s+)?(?:express\s+)?interest\s+in\s+`),
	rr(`^thank\s+you\s+for\s+your\s+interest\s+in\s+(?:the\s+|joining\s+|working\s+(?:for|at)\s+)?`),
	rr(`^thanks?\s+for\s+applying\s+(?:to|for)\s+(?:the\s+|our\s+|your\s+)?`),
	rr(`^thanks?\s+for\s+(?:your|the)\s+interest\s+(?:in\s+)?`),
	rr(`^we\s+appreciate\s+your\s+interest\s+(?:in|for)\s+(?:the\s+|our\s+|joining\s+|working\s+(?:for|at)\s+)?`),
	rr(`^applying\s+(?:to|for)\s+(?:the\s+|our\s+|your\s+)?`),
	rr(`^applied\s+(?:to|for)\s+(?:the\s+|our\s+|your\s+)?`),
	rr(`^application\s+(?:to|for|with)\s+(?:the\s+|our\s+|your\s+)?`),
	rr(`^time\s+to\s+express\s+interest\s+in\s+(?:the\s+)?`),
	rr(`^time\s+(?:you'?ve\s+|you\s+have\s+)?invested\s+in\s+applying\s+(?:to|for)\s+(?:the\s+|our\s+|your\s+)?`),
	rr(`^interest\s+in\s+(?:the\s+)?`),
	rr(`^your\s+application\s+(?:to|for|with)\s+(?:the\s+|our\s+|your\s+)?`),
	rr(`^your\s+interest\s+(?:in|for)\s+(?:the\s+|our\s+|joining\s+|working\s+(?:for|at)\s+)?`),
	rr(`^application\s+seems\s+like\s+a\s+good\s+fit\s+for\s+(?:the\s+)?`),
	rr(`^.*?\bgood\s+fit\s+for\s+(?:the\s+)?`),
	rr(`^position\s+of\s+`),
	rr(`^role\s+of\s+`),
	rr(`^position:\s*`),
	rr(`^role:\s*`),
	rr(`^for\s+(?:the\s+|our\s+|your\s+)`),
	rr(`^to\s+(?:the\s+|our\s+|your\s+)`),
	rr(`^our\s+(?:open\s+)?`),
	rr(`^the\s+`),
	rr(`^engineer,\s+`),
}

var (
	roleTerminatorRE = rr(`\s+(?:role|position|job|opening|requisition|opportunity)\b[.,!?:;\s]*.*$`)
	roleContextCuts  = []*lazyregexp.Regexp{
		rr(`\s+here\s+at\s+.*$`), rr(`\s+with\s+us\b.*$`), rr(`\s+and\s+(?:we\s+|our\s+team\s+)?will\b.*$`),
		rr(`\s+(?:and|but)\s+(?:i|we|our|you|the)\b.*$`), rr(`\s+(?:please|our\s+team|we\s+will|you\s+will)\b.*$`),
		lazyregexp.New(`[.!]\s+.*$`), lazyregexp.New(`\s+/\s+#.*$`), lazyregexp.New(`\s+\([^)]*\).*$`),
	}
	sentenceEndRE   = lazyregexp.New(`[.!?]\s`)
	recommendFooter = lazyregexp.New(`(?i)(?:Now,?\s+take these next steps for more success|View similar jobs you may be interested in)`)
	sentToBodyRE    = lazyregexp.New(`(?i)your application was sent to`)
	salutationRE    = lazyregexp.New(`(?i)^(?:hi|hello|hey|dear|hi there|hello there|greetings|good (?:morning|afternoon|evening))\b[,\s]`)
	firstNameSalRE  = lazyregexp.New(`^(?:[A-Z][a-z]+,\s+)`)
	corpSuffixes    = map[string]bool{"inc": true, "llc": true, "ltd": true, "corp": true, "co": true, "company": true, "plc": true, "gmbh": true, "sa": true, "ag": true, "llp": true, "pllc": true}
)

var subjectRolePatterns = []*lazyregexp.Regexp{
	rr(`\b(?:first|initial|introductory)\s+call\s+with\s+[^,\x{2013}\x{2014}:]+?\s+for\s+([A-Z]RC{3,100}?)\s+Role\b`),
	rr(`\bInterview(?:\s+invitation)?\s*(?:DASH|:)\s*([A-Z]RC{2,90}?)\s+@\s+[A-Z]`),
	rr(`\(\s*[A-Z][^()\x{2013}\x{2014}-]{1,80}\s+DASH\s+([A-Z][^()]{2,90}?)\s*\)\s*@`),
	rr(`^We received your (.+?) (?:Assessment|Screener) submission$`),
	rr(`^Your\s+.+?\s+application\s+for\s+(.+)$`),
	rr(`\bInterview\s+Schedule\s*(?:DASH|:)\s*([A-Z]RC{3,90}?)(?:\s*DASH\s*[A-Z][A-Za-z]+(?:\s+[A-Z][A-Za-z]+){0,3})?\s*$`),
	rr(`\bJob\s+Opportunity\s*(?:DASH|:)\s*([A-Z]RC{3,90}?)(?:\s*DASH\s*(?:Remote|Hybrid|Onsite|[A-Z][A-Za-z]+,\s*[A-Z]{2}|[A-Z][A-Za-z]+))?\s*$`),
	rr(`^Indeed Application:\s+([A-Z]RC{3,90})$`),
	rr(`\bApplication Received for\s+([A-Z]RC{3,80})$`),
	rr(`\bapplication (?:to|for)\s+\d{3,}\s+([A-Z]RC{3,80}?)(?:\s+at\s+[A-Z]|\s*$)`),
	rr(`([A-Z][A-Za-z]+(?:\s+[A-Za-z0-9]+){0,4}?\s+(?:Engineer|Developer|Scientist|Manager|Analyst|Designer|Architect|Specialist|Consultant|Researcher|Intern)(?:\s+[A-Z0-9]{1,3})?)\s*\([A-Za-z]{2,3}\)\s*/\s*R[_-]?\d`),
	rr(`\bYour application to\s+([A-Z]RC{3,90}?)\s+at\s+[A-Z][A-Za-z0-9.&'-]+(?:\s+[A-Z0-9][A-Za-z0-9.&'-]+){0,5}\s*$`),
	rr(`\bfor\s+(?:the\s+)?([A-Z]RC{3,90}?)\s*/\s*#`),
	rr(`^([A-Z]RC{3,90}?)\s+(?:with|at)\s+[A-Z]`),
	rr(`(?:the|our|for)\s+([A-Z]RC{3,90}?)\s+(?:position|role|job|opening)`),
	rr(`^([A-Z]RC{3,90}?)\s*DASH\s*(?:First|Second|Third|Final|Onsite|Phone|Technical|Initial|Virtual)\s+Round\s*$`),
	rr(`\|\s*([A-Z]RC{1,90}?)\s*DASH\s*[A-Z][a-z'\x{2019}-]+(?:\s+[A-Z][a-z'\x{2019}-]+){1,3}\s*$`),
}

var bodyRolePatterns = []*lazyregexp.Regexp{
	rr(`\bAbout the role:\s*[-\s]*Title:\s*\[?([A-Z]RC{3,90}?)(?:\]|https?:|Location:|Salary:|Equity:|\n|$)`),
	rr(`\bPosition\s*(?:DASH|:)\s*([A-Z]RC{3,90}?)(?:\n|Location\s*(?:DASH|:)|What you'll need|$)`),
	rr(`\byour application to\s+([A-Z]RC{3,80}?)\s+at\s+[A-Z]`),
	rr(`\byour interest in\s+(?:the\s+)?([A-Z]RC{3,80}?)\s+position\s+at\s+[A-Z]`),
	rr(`\bapply\s+(?:to|for)\s+(?:the\s+|our\s+)?([A-Z]RC{3,80}?)\s+(?:position|role|job|opening)\b`),
	rr(`\byour application was sent to\s+[A-Za-z0-9.&'-]+\s+([A-Z]RC{3,80}?(?:engineer|scientist|developer|manager|analyst|researcher|intern|architect|consultant|designer|specialist)RC{0,40}?)\s+[A-Za-z0-9.&'-]+\s+\x{00b7}`),
	rr(`\bapplication\s+for\s+(?:our\s+|the\s+)?([A-Z]RC{3,80}?)\s+(?:at|with)\s+[A-Z]`),
	rr(`\b(?:our|the)\s+([A-Z]RC{3,80}?)\s+(?:position|role|job|opening)`),
	rr(`\bapplying\s+(?:to|for)\s+(?:the\s+|our\s+)?([A-Z]RC{3,80}?)\s+(?:at|with|role|position|job|opening)`),
	rr(`\bposition\s+of\s+([A-Z]RC{3,80}?)(?:\.|,|\n|\s+(?:at|with|here|in)\b|$)`),
	rr(`\bapplication\s+for\s+(?:our\s+|the\s+)?([A-Z]RC{3,80})`),
	rr(`\byour\s+interest\s+in\s+(?:the\s+)?([A-Z]RC{3,80}?)\s+(?:position|role|opportunity)`),
}

var keywordFallback = rr(`([A-Z][A-Za-z0-9,&/()' .+\-\x{2013}\x{2014}]{0,80}(?:engineer|scientist|developer|manager|analyst|researcher|intern|architect|consultant|designer|specialist)[A-Za-z0-9,&/()' .+\-\x{2013}\x{2014}]{0,60})`)

func normToken(t string) string {
	return strings.ToLower(strings.TrimFunc(t, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') }))
}

// stripCompanyPrefix removes leading company words and dropped corporate
// suffixes from a role captured out of a "sent to" body, stopping at the first
// role noun.
func stripCompanyPrefix(value, company string) string {
	if company == "" {
		return value
	}
	ct := map[string]bool{}
	for _, w := range strings.Fields(company) {
		if n := normToken(w); n != "" {
			ct[n] = true
		}
	}
	toks := strings.Fields(value)
	for len(toks) > 1 {
		if roleNounRE2.MatchString(toks[0]) {
			break
		}
		n := normToken(toks[0])
		if n != "" && (ct[n] || corpSuffixes[n]) {
			toks = toks[1:]
		} else {
			break
		}
	}
	return strings.Join(toks, " ")
}

var tldTail = lazyregexp.New(`\.[a-z]{2,}$`)
var dashTail = lazyregexp.New(`^(.*\S)\s*[-\x{2013}\x{2014}|]\s*([^-\x{2013}\x{2014}|]+)$`)

// stripCompanySuffix removes a trailing "- <company>" segment that names the
// resolved employer.
func stripCompanySuffix(value, company string) string {
	if company == "" {
		return value
	}
	ct := map[string]bool{}
	for _, w := range strings.Fields(company) {
		if n := tldTail.ReplaceAllString(normToken(w), ""); n != "" {
			ct[n] = true
		}
	}
	m := dashTail.FindStringSubmatch(value)
	if m == nil {
		return value
	}
	tail := strings.TrimSpace(m[2])
	if roleNounRE2.MatchString(tail) {
		return value
	}
	if n := tldTail.ReplaceAllString(normToken(tail), ""); n == "" || !ct[n] {
		return value
	}
	return strings.TrimSpace(m[1])
}

var trailingPunctRE = lazyregexp.New(`[,;:.!?\s-]+$`)

func trimProseSuffix(text string) string {
	cur := strings.TrimSpace(text)
	for _, re := range roleContextCuts {
		cur = strings.TrimSpace(re.ReplaceAllString(cur, ""))
	}
	if loc := sentenceEndRE.FindStringIndex(cur); loc != nil && loc[0] > 0 {
		rest := strings.TrimSpace(cur[loc[0]+1:])
		if rest != "" && rest[0] >= 'A' && rest[0] <= 'Z' {
			cur = strings.TrimSpace(cur[:loc[0]])
		}
	}
	if roleNounRE2.MatchString(cur) {
		cur = strings.TrimSpace(roleTerminatorRE.ReplaceAllString(cur, ""))
	}
	return strings.TrimSpace(trailingPunctRE.ReplaceAllString(cur, ""))
}

func stripProsePrefix(text string) (string, bool) {
	cur, stripped := strings.TrimSpace(text), false
	for changed := true; changed; {
		changed = false
		for _, re := range prosePrefixes {
			if re.MatchString(cur) {
				cur = strings.TrimSpace(re.ReplaceAllString(cur, ""))
				stripped, changed = true, true
				break
			}
		}
	}
	return cur, stripped
}

func refine(captured string) (string, bool) {
	v, stripped := stripProsePrefix(trimProseSuffix(captured))
	return trimProseSuffix(v), stripped
}

var (
	roleProseRE      = lazyregexp.New(`(?i)\b(application has been received|your application seems|we will contact you|requirements of the|needs of the team|role\. in the meantime|world who help|first 90 days|seems like a good fit|currently reviewing)\b`)
	possessiveInRE   = lazyregexp.New(`(?i)^[a-z]'?s\s+in\s+`)
	conjunctionIRE   = lazyregexp.New(`(?i)\b(?:and|but)\s+i\b`)
	sentToRE         = lazyregexp.New(`(?i)^your application was sent to\b`)
	inviteToRE       = lazyregexp.New(`(?i)^(?:i'|we'|you')?d?\s*like to invite you to\b`)
	engineerCommaRE  = lazyregexp.New(`(?i)^(?:engineer,)\b`)
	genericOpenerRE  = lazyregexp.New(`(?i)^(?:application|interest|time|thanks?|thank you|your interest|our|the)\b`)
	associateCommaRE = lazyregexp.New(`(?i)^associate,\s+`)
)

// isGenericRole reports a capture that is prose or a salutation, not a title.
func isGenericRole(v string) bool {
	t := strings.TrimSpace(v)
	switch {
	case t == "", len(t) > 110:
		return true
	case t[0] >= 'a' && t[0] <= 'z':
		return true
	case roleProseRE.MatchString(t),
		possessiveInRE.MatchString(t),
		conjunctionIRE.MatchString(t),
		sentToRE.MatchString(t),
		inviteToRE.MatchString(t),
		engineerCommaRE.MatchString(t),
		genericOpenerRE.MatchString(t),
		salutationRE.MatchString(t):
		return true
	}
	return firstNameSalRE.MatchString(t) && !associateCommaRE.MatchString(t)
}

type roleCapture struct {
	value  string
	source string
	span   *Span
}

func captureRole(patterns []*lazyregexp.Regexp, text, source string, needNoun bool) *roleCapture {
	for pass := 0; pass < 2; pass++ {
		for _, re := range patterns {
			m := re.FindStringSubmatch(text)
			if m == nil || len(m) < 2 || m[1] == "" {
				continue
			}
			if source == "subject" {
				return &roleCapture{strings.TrimSpace(m[1]), source, mkSpan(source, m[1], text)}
			}
			if pass == 0 {
				if !roleNounRE2.MatchString(m[1]) {
					continue
				}
				if v, _ := refine(m[1]); v != "" && roleNounRE2.MatchString(v) && !isGenericRole(v) {
					return &roleCapture{strings.TrimSpace(m[1]), source, mkSpan(source, m[1], text)}
				}
				continue
			}
			if v, _ := refine(m[1]); v != "" && !isGenericRole(v) {
				return &roleCapture{strings.TrimSpace(m[1]), source, mkSpan(source, m[1], text)}
			}
		}
		if source == "subject" {
			break
		}
	}
	return nil
}

// resolveRole reads the role from the subject, then the body, then a keyword
// fallback. company, when resolved, lets a company name that leaked into the
// title be stripped.
func resolveRole(subject, body, company string) RoleEvidence {
	if loc := recommendFooter.FindStringIndex(body); loc != nil && loc[0] > 0 {
		body = body[:loc[0]]
	}
	sent := sentToBodyRE.MatchString(body)
	deleak := func(c *roleCapture) string {
		v := c.value
		if c.source == "body" && sent {
			v = stripCompanyPrefix(v, company)
		}
		return stripCompanySuffix(v, company)
	}
	var caps []*roleCapture
	if c := captureRole(subjectRolePatterns, subject, "subject", true); c != nil {
		caps = append(caps, c)
	}
	if c := captureRole(bodyRolePatterns, body, "body", true); c != nil {
		caps = append(caps, c)
	}
	for _, c := range caps {
		v, stripped := refine(deleak(c))
		if v != "" && roleNounRE2.MatchString(v) && !isGenericRole(v) {
			return RoleEvidence{Value: v, Strategy: c.source, StrippedPrefix: stripped, Span: c.span}
		}
	}
	for _, src := range []struct{ name, text string }{{"subject", subject}, {"body", body}} {
		if src.text == "" {
			continue
		}
		m := keywordFallback.FindStringSubmatch(src.text)
		if m == nil {
			continue
		}
		raw := m[1]
		if src.name == "body" && sent {
			raw = stripCompanyPrefix(raw, company)
		}
		raw = stripCompanySuffix(raw, company)
		v, stripped := refine(raw)
		if v != "" && roleNounRE2.MatchString(v) && !isGenericRole(v) {
			return RoleEvidence{Value: v, Strategy: "fallback_keyword", StrippedPrefix: stripped, Span: mkSpan(src.name, m[1], src.text)}
		}
	}
	return RoleEvidence{Value: UnknownRole, Strategy: StratUnknown}
}
