package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

const (
	// othEmployerSkillExclusion keeps "worked with <skill> before" away from
	// employer history. The technology names cover the common stacks a bare
	// "with" would otherwise confuse for an organisation. A body of material
	// worked with (AssemblyAI: "worked with financial or operational data") is
	// what the applicant handled, not who employed them.
	othEmployerSkillExclusion = `\b(?:experience\s+with|familiar(?:ity)?|knowledge\s+of|proficient|skills?|tools?|language|frameworks?|technolog(?:y|ies)|platforms?)\b|` +
		`\bworked\s+with\s+(?:[\w-]+\s+){0,4}(?:data(?:sets?)?|information|records)\b|` + othTechNames

	othTechNames = `\b(?:typescript|javascript|python|java|golang|rust|react|angular|node(?:\.js)?|sql|aws|azure|kubernetes|docker)\b`

	// othCompanyKind is a kind of company rather than an employer ("Have you
	// worked at a startup before?", Beacon AI): the applicant's experience of
	// that kind, never this employer's history.
	othCompanyKind = `|\b(?:at|for|with)\s+(?:an?|any)\s+(?:[\w-]+\s+){0,2}(?:start-?ups?|scale-?ups?|agenc(?:y|ies)|non-?profits?|consultanc(?:y|ies))\b`

	// othEmploymentRole is a role that makes "provided services" or "worked
	// as" an employment-history statement.
	othEmploymentRole = `(?:(?:regular|temporary|temp|contract)\s+employees?|vendors?|contractors?|consultants?|contingent\s+workers?|temps?|temporary)`

	// othAuditFirm is one of the large audit firms, any common spelling.
	othAuditFirm = `\b(?:pwc|pricewaterhouse(?:coopers)?|deloitte|ernst\s*(?:&|and)?\s*young|ey|kpmg|klynveld)\b`
)

var othAuditFirmRE = lazyregexp.New("(?i)" + othAuditFirm)

// othDistinctAuditFirms counts the different audit firms a text names: the
// spellings of one firm (PwC and PricewaterhouseCoopers, EY and Ernst & Young,
// KPMG and Klynveld) count once.
func othDistinctAuditFirms(text string) int {
	seen := map[string]bool{}
	for _, m := range othAuditFirmRE.FindAllString(text, -1) {
		seen[othAuditFirmKey(m)] = true
	}
	return len(seen)
}

// othAuditFirmKey is the one key of an audit firm's spelling.
func othAuditFirmKey(spelling string) string {
	k := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.ToLower(spelling))
	switch {
	case strings.HasPrefix(k, "pricewaterhouse"):
		k = "pwc"
	case strings.HasPrefix(k, "ernst"):
		k = "ey"
	case k == "klynveld":
		k = "kpmg"
	}
	return k
}

// othStatementsApply asks whether statements listed after it apply to the
// applicant.
const othStatementsApply = `^\W*(?:do|does)\s+(?:either|any|one|both|all)\s+(?:of\s+)?(?:the\s+)?(?:following\s+)?statements?\s+apply\s+to\s+you\b`

var othStatementsApplyRE = lazyregexp.New("(?i)" + othStatementsApply)

// othEmploymentStatementRE is a first-person statement of employment by an
// employer: "I am currently employed by", "I am/was employed by".
var othEmploymentStatementRE = lazyregexp.New(`(?i)^i\s+(?:am|was|have\s+been)(?:\s*/\s*(?:am|was|have\s+been))?\s+(?:currently\s+|previously\s+|formerly\s+)?(?:employed|engaged|contracted)\s+(?:by|at|with|for)\b`)

// othAllStatementsEmployment reports a list of statements after its question
// that are all statements of employment, at least one.
func othAllStatementsEmployment(text string) bool {
	sentences := sentencesOf(text)
	at := slices.IndexFunc(sentences, func(s string) bool { return strings.HasSuffix(s, "?") })
	if at < 0 || at == len(sentences)-1 {
		return false
	}
	for _, s := range sentences[at+1:] {
		if !othEmploymentStatementRE.MatchString(s) || relRelativeRE.MatchString(s) {
			return false
		}
	}
	return true
}

func othEmployerRoutes() []Route {
	const (
		workedAt   = `\bhave\s+you\s+worked\s+(?:for|at|with)\b`
		offerFrame = `\bif\s+offered\b|\bwhen\s+(?:would|can|could)\s+you\b|\bable\s+to\s+start\b|\bstart\s+working\b|\bstart\s+date\b|\bessential\s+function`
		// A government, a public agency, a party, a royal family or a
		// candidate is the public-employment entries' question.
		noGov = `|\bgovernment|\b(?:federal|state|local|county|municipal|public)\s+agenc(?:y|ies)\b|\b(?:` + publicEmployers + `)`
	)
	// Every route names the one employer-history entry; the old "first entry of
	// the kind" resolution is gone.
	const target = "employer-history"
	return []Route{
		// Capital One: "have you previously, worked at" and "have you in the past
		// two years worked for"; Motional: "worked within the Motional
		// ecosphere? (ex. Motional, Ottomatika, Aptiv, ...)".
		{
			Priority: 31, Target: target,
			Pattern: `\bhave\s+you\s+(?:ever|previously|in\s+the\s+past(?:\s+(?:\w+\s+)?(?:years?|months?))?),?\s+worked\s+(?:at|for|with|within\s+(?:the\s+)?\S+(?:\s+\S+){0,2}?\s+(?:ecosphere|ecosystem|family|group|affiliates))\b`,
			Exclude: othTechNames + othCompanyKind + noGov,
		},
		{
			Priority: 32, Target: target,
			Pattern: workedAt + `[^.?!]*\bbefore\b`,
			Exclude: othEmployerSkillExclusion + othCompanyKind + noGov,
		},
		// End-anchored: "in the past 5 years" stays available to the audit-firm
		// and external-auditor routes declared later.
		{
			Priority: 33, Target: target,
			Pattern: workedAt + `[^.?!]*\bin\s+the\s+past[?!.*\s]*$`,
			Exclude: othEmployerSkillExclusion + othCompanyKind + noGov,
		},
		{
			Priority: 34, Target: target,
			Pattern: `\bhave\s+you\s+(?:(?:ever|previously|in\s+the\s+past)\s+)?worked\s+as\s+(?:a|an)\b.{0,60}?\b(?:interns?|employees?|contractors?|contingent\s+workers?|consultants?|temps?|temporary|vendors?)\b.{0,40}?\b(?:at|for|with)\b`,
			Exclude: noGov[1:],
		},
		{
			Priority: 35, Target: target,
			// Asana: "been employed, or otherwise engaged, by".
			Pattern: `\bhave\s+you\s+(?:(?:ever|previously|in\s+the\s+past)\s+)?been\s+(?:(?:employed|engaged|contracted)(?:,?\s+or\s+(?:otherwise\s+)?(?:employed|engaged|contracted),?)?\s+(?:by|at|with|for)|an?\s+(?:employee|staff\s+member|team\s+member|contractor)\s+(?:of|at|with|for))\b`,
			Exclude: noGov[1:],
		},
		// The same asked as statements (Q2, Workday): "Do either of the
		// following statements apply to you? I am currently employed by Ernst &
		// Young at the partner level or above. I am/was employed by Ernst &
		// Young ...", every statement one of employment by the employer it
		// names.
		{
			Priority: 36, Target: target,
			Pattern: `\bwere\s+you\s+(?:(?:ever|previously)\s+)?(?:employed|working|a\s+contractor)\s+(?:at|by|for|with)\b|` + othStatementsApply,
			Exclude: noGov[1:],
			Require: func(text string) bool {
				return !othStatementsApplyRE.MatchString(text) || othAllStatementsEmployment(text)
			},
		},
		// Up to six words between the qualifier and "employee" absorb a
		// subsidiary parenthetical; a relative question belongs to the
		// relatives routes declared right after.
		{
			Priority: 37, Target: target,
			// Compeer Financial: "Are you a previous team member of ...?", the
			// employer's word for employee, asked of the applicant.
			Pattern: `\b(?:current|former|previous)(?:\s+or\s+(?:current|former|previous))?(?:\s+\S+){0,6}?\s+employee\b|` +
				`\bare\s+you\s+an?\s+(?:current|former|previous)(?:\s+or\s+(?:current|former|previous))?\s+team\s+member\b`,
			// Weave: "How did you hear about us? If you were introduced ... by a
			// current employee ..." asks the source, not the applicant's employment;
			// Babylist: "Were you referred to this role by a current Babylist
			// employee?" is the referral.
			// A relationship with an employee, or knowing one, is asked about
			// someone else.
			Exclude: `\b(?:relatives?|family|related|spouse|sibling|parent|friends?|relationships?)\b|\bdo\s+you\s+know\b|\bhow\s+did\s+you\s+(?:hear|learn|find\s+out)\s+about\b|\breferr(?:ed|al|ing)\b` + noGov,
			// Two different audit firms make it a firm-list ask for the audit-firm routes.
			Veto: func(text string) bool { return othDistinctAuditFirms(text) >= 2 },
		},
		{
			Priority: 38, Target: target,
			Pattern: `\b(?:former|previously|formerly)\s+(?:employed|work(?:ed|ing))\s+(?:by|at|for)\b`,
			Exclude: noGov[1:],
		},
		// A bare past-tense marker is not enough; an employment role must sit
		// in the same sentence.
		{
			Priority: 39, Target: target,
			Pattern: `\bprovided\s+services?\b[^.?!]*\b` + othEmploymentRole + `\b|\b` + othEmploymentRole + `\b[^.?!]*\bprovided\s+services?\b`,
			Exclude: noGov[1:],
		},
		// Disjoint from the history routes: the exclusion is the history
		// vocabulary plus offer and start framing.
		{
			Priority: 40, Target: "currently-employed-by-this-company",
			Pattern: `\b(?:do\s+you\s+(?:currently|presently|now)\s+(?:work|consult)|are\s+you\s+(?:currently|presently|now)\s+(?:employed|working))\s+(?:at|for|by|with)\b`,
			Exclude: guardPastTenseEmployment + `|` + offerFrame + noGov,
		},
	}
}

func othLanguageRoutes() []Route {
	const target = "language-proficiency"
	return []Route{
		{Priority: 198, Target: target, Pattern: `\blanguages?\b.*\b(?:speak|fluent|fluently)\b`},
		// A Workday languages row renders its aggregate rating as a bare label;
		// whether it is a languages row is for the field resolver to decide.
		{Priority: 199, Target: target, Pattern: `^\s*overall\s*\*?\s*$`},
		{Priority: 200, Target: target, Pattern: `^\s*(?:read(?:ing)?|speak(?:ing)?|writ(?:e|ing)|listen(?:ing)?)\s*\*?\s*$`},
		{
			Priority: 201, Target: target,
			Pattern: `\blanguages?\b`,
			// A question that names code or a programming language (Curai's
			// "code in Python (or a comparable language)") is not about speech.
			Exclude: `\b(?:programming|coding|computer|scripting|code)\b|\b(?:back|front)[- ]?end\s+languages?\b|\b(?:other\s+than|besides)\s+english\b|\bnon[- ]?english\b` +
				`|\b(?:python|java(?:script)?|typescript|perl|php|ruby|rust|golang|kotlin|scala|swift|node\.?js|c\+\+|c#)(?:\W|$)` +
				`|\b(?:first|last|middle|given|family|preferred|legal|maiden|sur)\s*name\b|\b(?:location|place|country)\s+of\s+residence\b` +
				`|\blarge\s+language\s+models?\b|\bllms?\b|\bfine[- ]?tun\w*|\btrain(?:ed|ing)?\b[^.?!]*\bmodels?\b`,
		},
		{
			Priority: 202, Target: target,
			Pattern: `\b(?:speaking|spoken|listening|reading|writing)\b.*\b(?:proficiency|fluency|fluent|level)\b` +
				`|\b(?:proficiency|fluency|fluent|level)\b.*\b(?:speaking|spoken|listening|reading|writing)\b`,
		},
	}
}

func othMultiSelectRoutes() []Route {
	return []Route{
		{
			// Tebra: "Which of the following data technologies have you
			// operated in a production environment?"
			Priority: 24, Target: "production-data-pipeline-tech",
			Pattern: `\b(?:which|what)\b[^.?!]*\b(?:technolog(?:y|ies)|tools?|frameworks?|orchestrat\w*)\b[^.?!]*\b(?:data\s+pipelines?|etl|elt|orchestration|data\s+engineering)\b` +
				`|\b(?:which|what)\b[^.?!]*\bdata\s+technolog(?:y|ies)\b[^.?!]*\bin\s+(?:a\s+)?production\b`,
		},
		{
			Priority: 25, Target: "production-data-platforms",
			Pattern: `\b(?:which|what)\b[^.?!]*\b(?:data\s+(?:platforms?|warehouses?|stores?)|databases?|warehouses?)\b[^.?!]*\b(?:used|worked|experience)\b`,
		},
		// Vola Dynamics: "Which of the following languages do you consider
		// yourself professionally proficient in?" over a list of programming
		// languages; the spoken-language entry takes no multi-select, and what
		// was used in production keeps the production-data lists' own answers.
		{
			Priority: 561, Target: idSkillChecklist, RequiresOptions: true,
			Pattern: `\b(?:which|what|select|check|tick)\b[^?]*\b(?:languages?|technolog(?:y|ies)|tools?|frameworks?|librar(?:y|ies)|skills?|platforms?|databases?)\b[^?]*\b(?:proficien\w*|experience[sd]?|familiar|comfortable|worked\s+with|used|expert\w*)\b`,
			Exclude: `\b(?:speak|spoken|speaking|native|mother\s+tongue|fluent\w*)\b|\bin\s+production\b|` + companyResearchAsk,
		},
		// Sphinx Defense: "Select what you have experience working in:" over
		// Python, Go and Rust; the options name what is asked.
		// Binti (Ashby): "Which of the following do you have experience working
		// with directly?" over Ruby, React and GraphQL.
		{
			Priority: 563, Target: idSkillChecklist, RequiresOptions: true,
			Pattern: `^\W*(?:(?:select|check|tick)\s+(?:what|which|all|any)|which\s+of\s+the\s+following(?:\s+do\s+you\s+have|\s+have\s+you)?)\b[^?.]*\bexperience\s+(?:working\s+)?(?:in|with)(?:\s+directly)?\W*$`,
		},
		// Workday's skills search ("Type to Add Skills" under My Experience):
		// the label alone; the resolver answers only a search prompt.
		{
			Priority: 573, Target: idSkillChecklist,
			Pattern: `^\W*(?:(?:type\s+to\s+)?add\s+)?skills\W*$`,
		},
		// Two different firms, in any order; a single-firm question, even one
		// that names the firm twice, belongs to the later per-firm routes.
		{
			Priority: 428, Target: "audit-firms-worked-at", Pattern: othAuditFirm + `.*` + othAuditFirm,
			Require: func(text string) bool { return othDistinctAuditFirms(text) >= 2 },
		},
		{
			Priority: 429, Target: "audit-firms-worked-at",
			Pattern: `\b(?:big\s*(?:4|four)|accounting\s+firms?)\b.*\b(?:work(?:ed)?|employ(?:ed|ment)?)\b`,
		},
		{
			Priority: 430, Target: "audit-firms-worked-at",
			Pattern: `\b(?:which|select|tick|check)\b.*\baccounting\s+firms?\b`,
		},
		// A shift word and an availability word must both appear, so a bare
		// start-date or overtime question does not route.
		{Priority: 431, Target: "shift-availability", Pattern: `\bshift\s+availab`},
		{Priority: 432, Target: "shift-availability", Pattern: `\bshifts?\b.*\bavailab(?:le|ility)\b`},
		{Priority: 433, Target: "shift-availability", Pattern: `\bavailab(?:le|ility)\b.*\bshifts?\b`},
	}
}
