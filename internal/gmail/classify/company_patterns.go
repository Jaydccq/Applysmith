package classify

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

const (
	dash = `[-\x{2013}\x{2014}]`
	// tok is a capitalized run of up to six words, allowing a bare "&".
	tok = `[A-Z][A-Za-z0-9.&'-]+(?:\s+(?:&|[A-Z0-9][A-Za-z0-9.&'-]+)){0,5}`
	// tokPlain stops at sentence punctuation inside the run.
	tokPlain = `[A-Z][A-Za-z0-9&'-]+(?:\s+[A-Z0-9][A-Za-z0-9&'-]+){0,4}`
	// nameRun is a free-text name with spaces and ampersands.
	nameRun = `[A-Z][A-Za-z0-9&' -]`
)

// roleWordRE marks a word that begins a role title; a company run that reaches
// one is cut there.
var roleWordRE = lazyregexp.New(`(?i)^(?:senior|staff|principal|junior|lead|associate|entry|early|software|data|machine|ml|ai|backend|frontend|full|fullstack|full-stack|platform|systems?|security|devops|site|infrastructure|product|research|applied|technical|test|qa|sales|business|forward|cloud|application|automation|web|mobile|firmware|hardware|embedded|engineer|scientist|developer|manager|analyst|researcher|intern|architect|consultant|designer|specialist|operations)$`)

type subjectPattern struct {
	re     *lazyregexp.Regexp
	noRole bool // cut the capture at the first role word
}

func sp(noRole bool, pattern string) subjectPattern {
	pattern = strings.ReplaceAll(pattern, "TOK", tok)
	pattern = strings.ReplaceAll(pattern, "DASH", dash)
	return subjectPattern{lazyregexp.New("(?i)" + pattern), noRole}
}

var subjectCompanyPatterns = []subjectPattern{
	sp(false, `\bInterview(?:\s+invitation)?\s*DASH?:?\s*[^@\n]{1,100}\s+@\s+(TOK)(?:\s*\(|\s*$|\s*DASH)`),
	sp(false, `\(\s*(TOK)\s+DASH\s+[^)]{2,100}\)\s*@`),
	sp(true, `^(TOK)\s+Follow\s+Up$`),
	sp(false, `^(TOK)\s*(?:DASH|:)\s*Your Application\b`),
	sp(true, `^\[\s*(TOK)\s*\]\s+`),
	sp(true, `^(TOK)\s+decision\b`),
	sp(true, `^Interview\s+Schedule\s*(?:DASH|:).+?\b(TOK)\b\s*DASH\s*[A-Z][A-Za-z]+(?:\s+[A-Z][A-Za-z]+){0,3}\s*$`),
	sp(false, `^[A-Z][A-Za-z0-9.&'-]+(?:\s+[A-Z0-9][A-Za-z0-9.&'-]+){0,4}\s+from\s+(TOK)\s+sent\s+you\s+a\s+message\b`),
	sp(false, `^(TOK)\s+Intro\s+(?:Invite|Call)\b`),
	sp(false, `^[^<>]{1,80}<>\s*(TOK)\s*(?:DASH|:)\s*Next\s+Steps\b`),
	sp(false, `^(TOK)\s*(?:DASH|:)\s*Job\s+Opportunity\b`),
	sp(false, `\byour\s+application\s+was\s+sent\s+to\s+([A-Za-z0-9][A-Za-z0-9.&'-]+(?:\s+[A-Za-z0-9][A-Za-z0-9.&'-]+){0,3})\b`),
	sp(true, `\bYour\s+application\s+to\s+[^\n]+?\s+at\s+(TOK)\s*$`),
	sp(false, `\bYour\s+application\s+(?:with|to)\s+(TOK)\b`),
	sp(false, `\|\s*Your application to\s+(TOK)\s*\|`),
	sp(false, `\bYour\s+(TOK)\s+Careers?\b`),
	sp(false, `\bRegarding\s+your\s+(TOK)\s+Application\b`),
	sp(false, `^(TOK)\s*(?:DASH|:)\s*(?:Application|Interview|Offer)\b`),
	sp(false, `\bThank\s+you\s+for\s+your\s+interest\s+in\s+(?:joining\s+|working\s+(?:for|at)\s+)?(TOK)\b`),
	sp(false, `\b(?:application|applying)\s+(?:to|for)\s+[^,.\n]+?\s+at\s+(TOK)\s*$`),
	sp(false, `\b(?:thank\s+you|thanks)\s+for\s+applying\s+to\s+(TOK)(?:[,!.]|$)`),
	sp(false, `\b(TOK)\s+invites?\s+you\b`),
	sp(true, `\bProfile\s+submitted\s+to\s+(TOK)\b`),
	sp(true, `^(TOK)\s+(?:Technical\s+|Phone\s+|Video\s+|Coding\s+|Online\s+|Final\s+)?(?:Interview|Assessment|Coding\s+Challenge)\s+(?:Invitation|Invite|Request)\b`),
	sp(true, `^Next\s+Steps?\s+(?:with|at)\s+(TOK)\s*(?:[:|]|DASH)`),
	sp(true, `^Your\s+(TOK)\s+application\b`),
	sp(true, `^(TOK)\s+Job\s+Opportunity\b`),
	sp(true, `\bat\s+(TOK)\s*$`),
	sp(true, `^(?:Exploratory|Intro|Initial|Phone|Onsite|Technical|Final|Coffee|Quick)\s+(?:Call|Chat|Screen|Interview|Meeting|Check[- ]in)\s+(?:with|at)\s+(TOK)\s*(?:[!.,]|$)`),
	sp(false, `^([A-Z][A-Za-z0-9.&'-]+(?:\s+[A-Z0-9][A-Za-z0-9.&'-]+){0,4}\s+AI)\s*(?:DASH|:)\s*(?:Virtual\s+)?Interview\s+(?:Availability|Scheduling|Invitation|Invite)\b`),
	sp(true, `^(TOK)\s*(?:DASH|:)\s*(?:Virtual\s+)?Interview\s+(?:Availability|Scheduling|Invitation|Invite)\b`),
	sp(true, `^Update\s+from\s+(TOK)\s*$`),
	sp(true, `^Thanks?\s+from\s+(TOK)\s*$`),
	sp(true, `^Thank\s+you\s+from\s+(TOK)\s*$`),
	sp(false, `\bYour\s+application\s+(?:was|has been)\s+viewed\s+by\s+([A-Z][A-Za-z0-9.&'-]+(?:\s+(?:for|and|of|the|&|[A-Z0-9][A-Za-z0-9.&'-]+)){0,8})(?:\s+\([A-Z0-9&.-]{2,}\))?\s*$`),
	sp(true, `\b(?:Update on Your Application to|application to|Your application to)\s+(TOK)\s*$`),
	sp(true, `\bImportant information about your application to\s+(TOK)\s*$`),
	sp(false, `^Assessment\s+(?:completed|invitation|invite|received|submitted)\s*:\s*([A-Z][A-Za-z0-9&'.-]+(?:\s+[A-Z0-9][A-Za-z0-9&'.-]+){0,4}?)(?:\s+\d{4})?\s+(?:Coding\s+)?Assessment\b`),
	sp(true, `\bAssessment\s+invitation\s+from\s+(TOK)\b`),
}

// Bodies are matched case-sensitively: the prefix prose is in canonical case in
// real mail, and case-insensitive captures grabbed articles and fragments.
var bodyCompanyPatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`\bitems were sent to\s+([A-Z][A-Za-z0-9&'. -]{1,70}?)(?:[.!]|\s+Good luck\b)`),
	lazyregexp.New(`(?i)\binterest in\s+(?:employment\s+opportunities\s+at|opportunities\s+at|joining|working\s+(?:for|at))\s+(` + tok + `)\b`),
	lazyregexp.New(`(?i)\bThank you for taking the time to explore an opportunity (?:with|at)\s+(` + tok + `)\b`),
	lazyregexp.New(`(?i)\bThanks\s+(?:so much )?for applying\s+(?:to|for)\s+(?:the\s+|our\s+)?[^,.\n]{0,80}?\s+(?:role|position)\s+(?:at|with)\s+(` + tok + `)\b`),
	lazyregexp.New(`\bThank(?:s)?(?:\s+you)?\s+for\s+(?:your\s+)?interest\s+in\s+(?:the\s+)?[^,.\n]{0,80}?\s+(?:role|position)\s+(?:at|with)\s+(` + tokPlain + `)\b`),
	lazyregexp.New(`\b(?:applying|apply)\s+to\s+(` + tokPlain + `)\.`),
	lazyregexp.New(`(?:Best|Regards|Sincerely|Cheers|Thanks|Thank you|All the best|Kind regards|Warm regards),?\s+(?:[A-Z][A-Za-z.'-]+\s+){1,4}(?:Chief of Staff|Chief of [A-Za-z]+ Officer|Founder|Co-Founder|CEO|CTO|COO|CFO|VP of [A-Za-z]+|Head of [A-Za-z]+|Technical Recruiter|Recruiter)\s*(?:,\s*|@\s*|at\s+)(` + tokPlain + `)\b`),
}

var legacyBodyPatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`[Aa]pplication for\s+.+?\s+at\s+(` + nameRun + `{2,90}?)(?:\.|,|!|\n|\s{2,}|$)`),
	lazyregexp.New(`[Ii]nterest in\s+(?:a career with|joining)\s+(` + nameRun + `{2,90}?)(?:\.|,|!|\n|\s{2,}|$)`),
	lazyregexp.New(`(?i)(?:position|role|job|opening|interview)\s+(?:at|with)\s+(` + nameRun + `{2,90}?)(?:\.|,|!|\n|\s{2,}|$)`),
	lazyregexp.New(`[Ii]nterest in\s+(` + nameRun + `{2,70}?)(?:[.!?]|,)`),
	lazyregexp.New(`(?:[Tt]hank you|[Tt]hanks)\s+(?:so much\s+)?for\s+applying\s+to\s+(` + nameRun + `{1,70}?)(?:[!.,]|\s+for\b)`),
	lazyregexp.New(`(?:[Tt]hank you|[Tt]hanks)\s+(?:so much\s+)?for\s+applying\s+to\s+([a-z][A-Za-z]{0,3}[A-Z][A-Za-z0-9&' -]{0,40}|[a-z]{2,8})(?:[!.,]|\s+for\b)`),
}

var proseBodyPatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`applying to\s+(?:the\s+|our\s+|a\s+|an\s+)?(?:[A-Z][^,.\n]+?\s+)?(?:role|position|opportunity|opening)\s+(?:at|with)\s+(` + nameRun + `{2,70}?)(?:\.|,|\n|\s{2,}|$)`),
	lazyregexp.New(`application (?:to|with)\s+(` + nameRun + `{2,70}?)(?:\.|,|\n|\s{2,}|$)`),
	lazyregexp.New(`career opportunities at\s+(` + nameRun + `{2,70}?)(?:\.|,|\n|\s{2,}|$)`),
}

// Role-shaped and prose fragments that must never be an employer.
var (
	roleNounRE      = lazyregexp.New(`(?i)^(?:senior\s+|staff\s+|principal\s+|junior\s+|lead\s+)?(?:software|data|machine learning|ml|ai|backend|frontend|full[- ]?stack|platform|systems?|security|devops|site reliability|infrastructure|product|research|applied)\s+(?:engineer|scientist|developer|manager|analyst|researcher|intern)s?$`)
	prepFragmentRE  = lazyregexp.New(`(?i)^(?:this|that|an?|our|your|their|his|her|at|with|for|from|by|in|on|of|to)\b`)
	connectorRE     = lazyregexp.New(`(?i)^(?:recent|new|old|latest|current|previous|original|first|second|next|last|online|offline|job|jobs|application|applications|role|position|opening|posting|listing|opportunity|re|fwd|fw)$`)
	theFragmentRE   = lazyregexp.New(`^the\s+(?:[a-z]|ideal|next|right|best|perfect|first|last|new|same|only|role|position|opportunity|opening|candidate|team|company|hiring)\b`)
	theRoleRE       = lazyregexp.New(`(?i)^the\s+(?:[A-Z][a-z]+\s+){0,3}(?:engineer|scientist|developer|manager|analyst|researcher|intern|architect|consultant|designer|specialist|operations|associate)\b`)
	roleAtCompanyRE = lazyregexp.New(`(?i)\b(?:engineer|scientist|developer|manager|analyst|researcher|intern)\s+(?:at|with|for)\s+`)
	adminPhraseRE   = lazyregexp.New(`(?i)^(?:action required|reminder|urgent|important|final notice|update|fyi|update on your)$`)
	adminTagRE      = lazyregexp.New(`(?i)^\s*\[\s*(?:action required|reminder|urgent|important|final notice|update|fyi)\s*\]\s*`)
	vendorNameRE    = lazyregexp.New(`(?i)^(jobvite|ats|greenhouse|workday|myworkday|rippling|unknown company|dayforce|paylocity|hirebridge|dover|litmus|coderbyte)$`)
	applicantVerbRE = lazyregexp.New(`(?i)\b(?:building|working|applying|studying|majoring|pursuing|seeking|interning|graduating|looking|hoping)\b`)
	lowerCamelRE    = lazyregexp.New(`^[a-z][A-Za-z]{0,3}[A-Z]`)
	shortBrandRE    = lazyregexp.New(`^[a-z]{1,8}$`)
	lowerDomainRE   = lazyregexp.New(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+$`)
	liveGenericRes  = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)^us\s+soon$`), lazyregexp.New(`(?i)^and\b`), lazyregexp.New(`(?i)^seems\b`),
		lazyregexp.New(`(?i)^thanks?\s`), lazyregexp.New(`(?i)^please\b`), lazyregexp.New(`(?i)\bplease\b`),
		lazyregexp.New(`(?i)^talent acquisition team\b`), lazyregexp.New(`(?i)\b(team please|please respond)\b`),
		lazyregexp.New(`(?i)^(here|there)\b`),
	}
	titledPersonRE = lazyregexp.New(`(?i)^[A-Z][a-z]{1,20}(?:\s+[A-Z][a-z]{1,20}){1,3}\s+[-\x{2013}\x{2014}]\s+(?:vice president|director|associate director|recruiter|technical recruiter|talent acquisition)\b`)
	mailboxWordRE  = lazyregexp.New(`(?i)^(?:system|portal|notifications?|alerts?|inbox|mailbox)$`)
	positionAtRE   = lazyregexp.New(`(?i)\b(position|role|job|opening)\s+at\b`)
	unattendedRE   = lazyregexp.New(`(?i)^(?:an?\s+)?unattended mailbox\b`)
)

// Recruiting-desk labels glued onto an employer name. The sides differ on
// purpose: a bare Recruiting, Recruitment or Careers is never a suffix label
// because agencies are named that way, and as a prefix only before a
// capitalized name.
const (
	deskBoth   = `(?:hr|human resources|talent acquisition(?:\s+dept(?:artment)?)?(?:\s+team)?|talent team|recruit(?:ing|ment)\s+team|candidate experience|people\s+(?:team|operations)|hiring\s+team)`
	deskSuffix = `(?:career\s+opportunit(?:y|ies)|opportunit(?:y|ies))`
	deskPrefix = `(?:careers|recruit(?:ing|ment))`
)

var (
	deskPrefixRE = lazyregexp.New(`(?i)^(?:` + deskBoth + `|` + deskPrefix + `)\s*(?:[-\x{2013}\x{2014}:|]\s*|\s+)(?:at\s+)?`)
	deskSuffixRE = lazyregexp.New(`(?i)(?:\s*[-\x{2013}\x{2014}:|]\s*|\s+)(?:` + deskBoth + `|` + deskSuffix + `)\s*$`)
	deskOnlyRE   = lazyregexp.New(`(?i)^(?:` + deskBoth + `|` + deskSuffix + `|` + deskPrefix + `)$`)
	danglingRE   = lazyregexp.New(`(?i)\b(?:of|for|and|&|the|at|in)$`)

	atsNameSuffixRE = lazyregexp.New(`(?i)\s+(?:hiring team|recruiting team|recruiting|talent acquisition(?:\s+team)?|talent team|talent|careers|candidate experience|people team|hr team|human resources|hiring|notifications?|notification center|alerts?|recruitment(?:\s+team)?)\s*$`)
	entitySuffixRE  = lazyregexp.New(`(?i)\s+(?:llc|inc\.?|corp\.?|corporation|company|co\.?|ltd\.?|limited)\s*$`)
	trailingNoiseRE = lazyregexp.New(`(?i)\s+(?:Application(?:\s+(?:Update|Confirmation|Status|Received))?|Careers?|Update|Confirmation|Status)\s*$`)
	dashNoiseRE     = lazyregexp.New(`(?i)\s*[-\x{2013}\x{2014}:]\s*Application\b.*$`)
	noReplyTailRE   = lazyregexp.New(`(?i)[\s_.-]+(?:do[\s_-]?not[\s_-]?reply|donotreply|no[\s_-]?reply|noreply)\s*$`)
	noReplyLeadRE   = lazyregexp.New(`(?i)^(?:do[\s_-]?not[\s_-]?reply|donotreply|no[\s_-]?reply|noreply)\b[\s_.:-]*`)
	teamSuffixRE    = lazyregexp.New(`(?i)\s+team$`)
	regionGluedRE   = lazyregexp.New(`^([A-Z][a-z]{2,}(?:[A-Z][a-z]+)*)(ME|EU|US|UK|APAC|EMEA|LATAM|MENA)$`)

	personRE         = lazyregexp.New(`^[A-Z][a-z]{1,12}(?:\s+[A-Z]\.?)?(?:\s+[A-Z][a-z]{1,15}){1,3}$`)
	personLastFirst  = lazyregexp.New(`^[A-Z][A-Za-z'-]{1,20},\s*[A-Z][A-Za-z'-]{1,20}$`)
	nonNameTailRE    = lazyregexp.New(`(?i)^(?:Notifications?|Recruiting|Hiring|Talent|Careers|Recruiter|Recruitment|Alerts?|Team|Notification|Center|Updates?|Mail|News|Newsletter|Engineering|Operations|Bot)$`)
	personAtCompany  = lazyregexp.New(`^[A-Z][a-z]{1,20}(?:\s+[A-Z][a-z]{1,20}){1,3}\s+at\s+([A-Z][A-Za-z0-9&'-]+(?:\s+[A-Z0-9][A-Za-z0-9&'-]+){0,4})[.,;:!?]?$`)
	trailProseRE     = lazyregexp.New(`(?s)[.!?;:]\s+.*$`)
	trailClauseRE    = lazyregexp.New(`(?s)\s+(?:and|but|while|as)\s+(?:(?:the|our|your|we|i|you)\b|[a-z]).*$`)
	trailPronounRE   = lazyregexp.New(`(?is)\s+(?:we|our|you|please|will|has|have|is|are)\b.*$`)
	trailConnectorRE = lazyregexp.New(`(?is)\s+(?:and|or|for|regarding|about)\s+(?:the|our|your)\s+.*$`)
	trailAndForRE    = lazyregexp.New(`(?is)\s+and\s+for\s+.*$`)
	trailEarlyRE     = lazyregexp.New(`(?i)\s+-\s+(?:Early Career|New Grad)\s+job\s*$`)
	trailPunctRE     = lazyregexp.New(`[.,:;]+$`)
	markRE           = lazyregexp.New("[®™©]+")
)

// candidate is one employer proposal from one strategy.
type candidate struct {
	value    string
	strategy string
	span     *Span
}
