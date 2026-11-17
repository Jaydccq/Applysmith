package match

// Roles that make "related to ... <role>" a relatives-at-employer question.
const (
	rxbStaffRole = `(?:employee|associate|colleague|staff|team\s+member|teammate|worker|personnel|contractor|consultant|partner|director|officer|executive)s?`
	rxbYears     = `(?:\d+|one|two|three|four|five)`
	// rxbProgramStart is the start of a degree program, not of the job.
	rxbProgramStart = `\b(?:start|begin|commence)\w*\s+(?:the\s+|your\s+|a\s+)?(?:graduate|master'?s?|phd|doctoral|degree)\s+(?:program(?:me)?|school|studies|degree)\b|` +
		`\b(?:graduate|master'?s?|phd|doctoral|degree)\s+program(?:me)?\s+start\b`
)

// rxbListedSituations is an ask about any of a list of conflict situations.
const rxbListedSituations = `\bany\s+of\s+the\s+(?:above|following|below)\s+(?:listed\s+)?(?:situations|activities|relationships)\b`

// rxbEmploymentRoutes: employment history, availability, military and
// clearance screens, and the conflict-of-interest family.
func rxbEmploymentRoutes() []Route {
	return []Route{
		{Priority: 30, Target: "previously-interviewed-company", Exclude: `\bcandidates?\b|\bconduct(?:ed|ing)?\s+interviews?\b`,
			Pattern: `\bhave\s+you\s+(?:ever\s+|previously\s+)?(?:been\s+)?interview(?:ed)?\b[^.?!]*\b(?:with|at|for|by|here|us)\b`},
		// Adjacent only: "Salary Expectation - Currency" is the amount field.
		{Priority: 41, Target: "salary-currency",
			Pattern: `^\s*currency\s*$|\b(?:pay|compensation|salary)\s+currency\b|\bpreferred\s+currency\b|\bwhat\s+currency\b|\bcurrency\s+type\b|\bselect\s+currency\b`},

		// Openly's bracketed examples close on "etc.)" before "who is currently
		// employed".
		{Priority: 44, Target: "relatives-at-employer", Pattern: `\brelatives?\b(?:[^.?!]|\betc\.\))*\b(?:employed|employees?|work\w*)\b`},
		{Priority: 45, Target: "relatives-at-employer",
			Pattern: `\b(?:are|do)\s+you\s+(?:related\s+to|have\s+(?:any\s+)?(?:relatives|family\s+members))\b[^.?!]*\b` + rxbStaffRole + `\b`},
		{Priority: 46, Target: "relatives-at-employer",
			Pattern: `\brelated\s+to\s+(?:any\s+|an?\s+)?[a-z .,'-]{0,40}\b` + rxbStaffRole + `\b`},
		// The company name stays unconstrained on purpose.
		{Priority: 47, Target: "relatives-at-employer",
			Pattern: `\brelated\s+to\s+(?:anyone|anybody|someone|somebody|any\s+person|individual|employee|staff|persons|individuals)\b[^.?!]*\b(?:work\w*|employ\w*)\b`},
		// The applicant as someone's kin: "a family member or significant other of
		// a current employee" (Coterie).
		{Priority: 48, Target: "relatives-at-employer",
			Pattern: `\b(?:partner|spouse|family\s+member)\b[^.?!]*\b(?:currently\s+)?(?:employed|employee|work\w*)\b[^.?!]*\b(?:for|at|with|by)\b` +
				`|\b(?:family\s+member|relative|significant\s+other|spouse|partner)(?:\s+or\s+(?:an?\s+)?(?:family\s+member|relative|significant\s+other|spouse|partner))?\s+of\s+(?:a|an|any)\s+(?:current\s+)?(?:employee|staff\s+member|team\s+member)s?\b`,
			// A government, a party, a royal family or a candidate is the
			// public-employment entries' question.
			Exclude: `\bgovern|\b(?:` + publicEmployers + `)\b`},
		// A conflict of interest is a relatives question only when the text ties it to
		// family or to working for the employer; a bare one is a disclosure for the user,
		// and so is a family member in government (government-position-family) or a list
		// of situations of which relatives are one (HP).
		{Priority: 49, Target: "relatives-at-employer", Pattern: `\bconflicts?\s+of\s+interest\b`, Exclude: `\bgovernment\s+officials?\b|` + rxbListedSituations,
			AllOf: []string{`\b(?:relatives?|family|related|spouse|partner|employees?|employed|employer|work(?:s|ed|ing)?\s+(?:for|at|with))\b`}},
		// A contact at the company's auditor is the external-auditor entry's.
		{Priority: 50, Target: "relatives-at-employer", Exclude: `\bauditors?\b|\bsenior\s+(?:government\s+official|executive)\b|` + rxbListedSituations,
			Pattern: `\b(?:family\s+relationship|close\s+personal\s+contact)\b[^.?!]*\b(?:employee|associate|colleague|staff|team\s+member|teammate|worker|personnel|contractor|consultant|partner|director|officer|executive|board\s+member|contingent\s+resource)s?\b`},

		// An acknowledgement opener, a notice-period ask and a non-compete
		// question each mention an employer without asking for its name; so
		// does GitLab's "employment agreements and/or post-employment
		// restrictions with your current employer". Plaid asks "Current/Last
		// Company". A title, a role, pay, a reason for leaving, the industry,
		// the size or a manager there is asked of that employer, not its name.
		{Priority: 57, Target: "current-company",
			Exclude: `\b(?:titles?|salary|compensation|pay|reason|leaving|why|industry|size|manager|supervisor)\b|\b(?:role|position)\s+(?:at|with|in)\b|non[- ]?(?:compete|solicit\w*)|\bpost[- ]employment\b|\brestrictive\s+covenants?\b|\bemployment\s+agreements?\b|\b(?:customer|client|vendor|supplier|reseller)\s+of\b|\b(?:business\s+)?relationship\s+with\b|\bcurrently\s+(?:work|employed|working)|\bwork\s+(?:at|for|with)\s+this\s+company|\bnotice\s+period\b|\bhow\s+much\s+notice\b|\bweeks?\s+(?:of\s+)?notice\b|\bavailab(?:le|ility)\s+to\s+start\b|^\s*i\s+(?:hereby\s+)?(?:understand|acknowledge|affirm|agree|certify|attest|consent)`,
			Pattern: `\b(?:current|present)(?:\s*\(?\s*(?:/|or)\s*(?:most\s+recent|previous|former|last)\s*\)?)?\s+(?:company|employer|organi[sz]ation)(?:\s+name)?\b|\bmost\s+recent\s+(?:company|employer|organi[sz]ation)\b`},
		{Priority: 58, Target: "current-job-title", Exclude: `\b(?:company|employer|organi[sz]ation)\b`,
			Pattern: `\b(?:current|present)(?:\s*\(?\s*(?:/|or)\s*(?:most\s+recent|previous|former|last)\s*\)?)?\s+(?:job|position|role)\s+title\b|\bmost\s+recent\s+(?:job|position|role)\s+title\b`},

		{Priority: 166, Target: "multi-year-commitment", Pattern: `\b` + rxbYears + `[ -]*year[ -]*commitment\b`},
		{Priority: 167, Target: "multi-year-commitment", Pattern: `\bcommit(?:ted|ment)?\b[^.?!]*\b` + rxbYears + `[ -]*years?\b`},

		// Trail: "Whats the earliest you can start?"
		{Priority: 184, Target: "earliest-start-date", Exclude: rxbProgramStart, Pattern: `\b(?:earliest|when)\s+(?:(?:can\s+you|you\s+can|could\s+you|you\s+could)\s+)?start\b`},
		{Priority: 185, Target: "earliest-start-date", Pattern: `\bdate\s+available\b`},
		// Fireworks AI: "If offered a position, what is your ideal start-date?"
		{Priority: 186, Target: "earliest-start-date", Exclude: rxbProgramStart, Pattern: `\bstart(?:\s+|-)date\b`},
		// Prior Labs: "When are you looking to start?"
		{Priority: 187, Target: "earliest-start-date", Exclude: rxbProgramStart, Pattern: `\b(?:when|how\s+soon)\s+(?:can|could|would|will)\s+you\s+(?:be\s+able\s+to\s+)?(?:start|begin|join|come|commence)\b` +
			`|\bwhen\s+(?:are|would|will)\s+you\s+(?:be\s+)?(?:looking|hoping|planning|aiming|wanting|ready)\s+to\s+(?:start|begin|join|commence)\b` +
			// Anthropic: "When is the earliest you would want to start working with us?"
			`|\bwhen\s+is\s+the\s+earliest\s+(?:date\s+)?(?:that\s+)?you\s+(?:would|could|can|will)\s+(?:want\s+to\s+|like\s+to\s+|be\s+able\s+to\s+)?(?:start|begin|join|commence)\b`},
		// Offer-framed phrasing belongs to offer-start-availability, declared later.
		{Priority: 188, Target: "earliest-start-date", Exclude: `\boffer(?:s|ed|ing)?\b`,
			Pattern: `\b(?:able|available)\s+to\s+(?:start|begin|join|come|commence)(?:\s+(?:working|work))?\b`},
		{Priority: 189, Target: "earliest-start-date", Pattern: `\bif\s+offered\b[^.?!]*\b(?:when|able)\b[^.?!]*\b(?:start|begin|join)\b`},
		// Offer-framed and job-history wording belongs to other entries.
		{Priority: 190, Target: "earliest-start-date", Exclude: `\boffer(?:s|ed|ing)?\b|\b(?:work|employment|education|school|job|employer|position|company)\b`,
			Pattern: `\bavailable\s+to\s+start\b|\bavailab(?:le|ility)\s+to\s+(?:start|begin|join|come)\b|\bdate\s+you\s+can\s+(?:start|begin|join)\b|\bearliest\s+(?:(?:possible|available)\s+)?start(?:\s+date)?\b|\bavailability\s+date\b|\bavailable\s+start\s+date\b`},

		{Priority: 214, Target: "available-full-time", Pattern: `\bavailable\s+(?:to\s+work\s+)?(?:full[- ]time|40\s+hours)\b`},

		// Declared above every amount rule so an alignment ask never becomes
		// a salary figure.
		{Priority: 223, Target: "salary-aligns", Pattern: `\b(?:salary|wage|comp(?:ensation)?)\s+expectations?\b[^?]*\balign\w*`},
		{Priority: 225, Target: "salary-aligns", Pattern: `\b(?:salary|wage|pay|comp(?:ensation)?)\b[^?]*\bwithin\s+(?:your|their)\s+(?:expectations?|range|target)\b`},

		{Priority: 359, Target: "previously-applied-to-company",
			Pattern: `\bappl(?:ied|y|ication)\b[^.?!]*\b(?:previous\w*|prior|before|past|ever|occasion)\b[^.?!]*\b(?:employment|position|job|role|work|here)\b`},
		// Kin (Ashby): "Before we proceed with your application, ... potential
		// employment" opens a privacy consent; "before" there is no past.
		{Priority: 360, Target: "previously-applied-to-company", Exclude: `^\W*before\s+(?:we|you|proceeding|continuing|submitting|starting|moving)\b`,
			Pattern: `\b(?:previous\w*|prior|before|past|ever|occasion)\b[^.?!]*\bappl(?:ied|y|ication)\b[^.?!]*\b(?:employment|position|job|role|work|here)\b`},
		{Priority: 361, Target: "previously-applied-to-company",
			Pattern: `\bhave\s+you\s+(?:ever\s+|previously\s+)?applied\b[^.?!]*\b(?:here|with\s+us|to\s+this\s+(?:company|organi[sz]ation|firm|employer)|for\s+(?:a\s+)?(?:position|employment))\b`},
		{Priority: 362, Target: "previously-applied-to-company",
			Pattern: `\bhave\s+you\s+(?:ever\s+|previously\s+)?applied\b[^.?!]*\b(?:within|in|during|over)\s+the\s+(?:past|last)\b`},
		{Priority: 363, Target: "prior-company-interaction",
			Pattern: `\b(?:prior|previous|past|any)\s+(?:interactions?|engagements?|involvements?|contacts?|touchpoints?)\s+with\b`},
		{Priority: 364, Target: "prior-company-interaction",
			Pattern: `\b(?:have|had)\s+you\s+(?:ever\s+|previously\s+)?(?:had\s+)?(?:any\s+)?(?:interactions?|engagements?)\s+with\b`},

		{Priority: 368, Target: "overtime-able", Pattern: `\b(?:perform|work|do)\s+overtime\b`},
		{Priority: 369, Target: "overtime-able", Pattern: `\b(?:able|willing|available)\s+to\s+(?:(?:perform|work|do)\s+)?overtime\b`},
		{Priority: 370, Target: "overtime-able", Pattern: `\bovertime\s+(?:work|hours?|required|when\s+needed|necessary)\b`},

		{Priority: 393, Target: "commitments-to-another-employer",
			Pattern: `\bcommitments?\b[^.?!]*\b(?:another|other|any|previous|current|existing|former)\b[^.?!]*\b(?:employers?|organi[sz]ations?|compan(?:y|ies)|part(?:y|ies)|entit(?:y|ies)|business(?:es)?)\b`},
		{Priority: 394, Target: "commitments-to-another-employer",
			Pattern: `\bcommitments?\b[^.?!]*\b(?:interfere|conflict)\w*\b[^.?!]*\b(?:employment|employer|work|position|duties)\b`},
		{Priority: 395, Target: "outside-employment",
			Pattern: `\b(?:plan\s+to|intend\s+to|do\s+you)\s+(?:have|continue)\b[^.?!]*\b(?:other\s+employment|outside\s+employment|outside\s+work|independent\s+contractor|moonlighting)\b`},
		{Priority: 396, Target: "outside-employment", Pattern: `\bemployment\s+or\s+other\s+work\s+that\s+you\s+intend\s+to\s+continue\b`},
		{Priority: 408, Target: "influence-decisions-employer", Pattern: `\bability\s+to\s+influence\s+decisions\b`},

		// The question is about the hiring employer's own auditor, so the
		// company-relationship framing is required.
		{Priority: 426, Target: "external-auditor-employer",
			Pattern: `\b(?:worked?|employ(?:ed|ment))\b[^.?!]*\bfor\b[^.?!]*\b(?:serves?|serving)\s+as\b[^.?!]*\bauditors?\b`},
		{Priority: 427, Target: "external-auditor-employer",
			Pattern: `\b(?:work\w*|employ\w*)\b[^.?!]*\bfor\b[^.?!]*\bauditors?\b[^.?!]*\bwithin\s+the\s+(?:last|past)\b`},
		{Priority: 434, Target: "audit-firm-employment", Pattern: `\bkpmg\b|\bklynveld\s+peat\s+marwick\s+goerdeler\b`},
		{Priority: 435, Target: "audit-firm-employment",
			Pattern: `\b(?:work(?:ed)?|employ\w*)\s+for\s+(?:the\s+(?:bank's|company's|firm's)\s+)?(?:external\s+)?auditors?\b`},

		{Priority: 436, Target: "military-spouse", Pattern: `\bmilitary\s+spouse\b`},
		{Priority: 437, Target: "military-spouse", Pattern: `\bmilitary\s+(?:spouse|domestic\s+partner)\b`},
		{Priority: 438, Target: "military-spouse", Pattern: `\bspouse\b[^.?!]*\b(?:served|serving|armed\s+forces|military|national\s+guard|reserves?)\b`},
		{Priority: 446, Target: "security-clearance-history", Pattern: `\bever\s+(?:had|held)\b[^.?!]*\b(?:security\s+)?clearance\b`},
		{Priority: 447, Target: "security-clearance-history", Pattern: `\binvestigated\s+your\s+background\b`},
		{Priority: 448, Target: "security-clearance-history", Pattern: `\bsecurity\s+incident\s+report\b`},
	}
}

// rxbEducationRoutes: school, graduation and enrollment questions.
func rxbEducationRoutes() []Route {
	return []Route{
		// A school NAME field, declared before did-you-graduate, which would
		// claim it and then reject the text control.
		// An undergraduate school is its own entry (route 579), never the most recent school.
		{Priority: 173, Target: "most-recent-school", Exclude: `\bundergrad(?:uate)?\b`,
			// Rogo (Ashby): "Where did you go to University?".
			Pattern: `\b(?:what|which|name\s+of)\b[^?]*\b(?:institutions?|schools?|universit(?:y|ies)|colleges?)\b[^?]*\b(?:did|have|will)\s+you\s+(?:graduate|attend|receive|be\s+graduating)\b` +
				`|\bwhere\s+did\s+you\s+(?:go\s+to|attend|study\s+at)\s+(?:a\s+|the\s+)?(?:universit(?:y|ies)|colleges?|school)\b` +
				// Valkyrie Trading (Lever): "Which institution granted (or will
				// grant) this degree?" after the highest degree.
				`|\b(?:what|which)\s+(?:institution|school|universit(?:y|ies)|college)\s+(?:granted|awarded|conferred)\s+(?:\(or\s+will\s+(?:grant|award|confer)\)\s+)?(?:this|that|the|your)\s+degree\b`},
		// A yes/no entry never answers a question that asks for the month, year
		// or date; the exclusion also keeps it out of the fuzzy stage.
		{Priority: 174, Target: "did-you-graduate", Pattern: `\bdid\s+you\s+graduate\b`,
			Exclude: `\b(?:what|which)\b[^?]{0,40}?\b(?:months?|years?|dates?|days?|semester|term)\b|\bwhen\b|\b(?:month|year)s?\s*(?:and|&|/)\s*(?:year|month)s?\b`},
		{Priority: 205, Target: "latest-gpa-score", Pattern: `\bgpa\b|\bgrade\s+point\s+average\b`},

		{Priority: 310, Target: "most-recent-school",
			Pattern: `\b(?:most\s+recent|latest|last)\s+(?:school|university|college|institution)\b|\b(?:school|university|college|institution)\s+you\s+(?:have\s+)?most\s+recently\s+attended\b`},
		// Whole-label only: an unanchored "university" would take enrollment
		// questions and every sentence of a posting.
		// Veeva (Lever): "Please select your college or University:".
		// Harvey (Ashby): "University or School Attended".
		{Priority: 311, Target: "most-recent-school",
			Pattern: `^\W*(?:(?:please\s+)?(?:select|choose|enter|provide)\s+(?:the\s+name\s+of\s+)?your\s+|name\s+of\s+(?:your\s+)?)?(?:university|school|college|institution)(?:\s*(?:/|\bor\b)\s*(?:university|school|college))?(?:\s+(?:name|attended))?\s*[:*]*\s*$`},
		// Free-text half of the picker pair ("select Other and type it").
		{Priority: 312, Target: "most-recent-school",
			Pattern: `\b(?:if\s+you\s+don't\s+see|if\s+your)\b[^.?!]*\b(?:university|school|college|institution)\b[^.?!]*\b(?:listed|isn't\s+listed|not\s+listed|enter|type|specify)\b`},
		{Priority: 318, Target: "degree-focus-pursuing", Pattern: `\bdegree\s+focus\b`},
		// Reducto: "currently enrolled in Full time Education? Like a
		// Bachelor's, Master's, or PhD program"; ElevenLabs: "Are you
		// currently in full-time education?". A second ask joined to it ("in
		// school and willing to work hybrid") is another question.
		{Priority: 319, Target: "currently-enrolled-student", Exclude: `\b(?:and|but)\s+(?:are\s+you\s+)?(?:willing|able|open|available|comfortable)\b`,
			Pattern: `\bstudent\s+enrolled\b|\bcurrently\s+(?:a\s+)?student\b|\benrolled\s+in\s+(?:a\s+)?(?:full[-\s]?time\s+)?(?:college|polytechnic|university|degree\s+program|education)\b|\bcurrently\s+in\s+(?:full[-\s]?time\s+)?(?:education|school|college|university)\b`},
		{Priority: 321, Target: "graduation-year-month", Pattern: `\b(?:month\s+and\s+year|year\s+and\s+month)\b[^?]*\bgraduat\w*`},
		// A day-precision date, not the term picker.
		{Priority: 332, Target: "expected-graduation-date", Exclude: `\bhigh\s*school\b|\bsecondary\s+school\b`, Pattern: `\bgraduation\s+date\b`},
	}
}
