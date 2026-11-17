package match

import (
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// rxRelocationAssistanceAsk asks whether the applicant needs help to move
// (Scopely: "Will you require relocation support?"), whatever the options say
// about where they live.
const rxRelocationAssistanceAsk = `\b(?:need|require|requiring|seek(?:ing)?|request(?:ing)?|expect(?:ing)?)\b[^?]{0,20}?\brelocation\s+(?:assistance|support|package|stipend|reimbursement)\b`

// otherKindRoutes holds the acknowledge, compensation, employer-history,
// language and multi-select families of the route catalog.
// offerDeadlineAsk asks the deadline of an offer held ("If yes, when is your
// offer deadline?"), competing-offer-deadline's; whether one is held is
// competing-offers'.
const offerDeadlineAsk = `\b(?:when|what)\b[^?]{0,40}\b(?:offer|decision)\s+deadlines?\b|\bdeadlines?\s+(?:of|for|on)\s+(?:your|the|that|this|those|these|any)\s+(?:other\s+|competing\s+)?offers?\b`

func otherKindRoutes() []Route {
	var all []Route
	for _, part := range [][]Route{
		othAcknowledgeRoutes(),
		othCompensationRoutes(),
		othEmployerRoutes(),
		othLanguageRoutes(),
		othMultiSelectRoutes(),
	} {
		all = append(all, part...)
	}
	return all
}

const (
	// othPrivacyNoun names a privacy or data-protection document.
	othPrivacyNoun = `(?:privacy\s+(?:policy|notice|statement)|data\s+(?:processing|protection))`

	// othConsentVerb is an election verb in any common inflection.
	othConsentVerb = `\b(?:consent(?:s|ed|ing)?|agree(?:s|d|ing)?|acknowledg(?:e|es|ed|ing)|accept(?:s|ed|ing)?)\b`

	// othEEOSelfID keeps the "by checking" routes off self-identification prose.
	othEEOSelfID = `protected\s+veteran|veteran\s+status|disability\s+status|gender\s+identity|\brace\b|ethnicity|hispanic|self[- ]?identify|self[- ]?id\b`

	// othByCheckingTrigger and othByCheckingContext are the two halves of the
	// "by checking ... agree" routes; either may come first.
	othByCheckingTrigger = `(?:\bby\s+checking\b|\bi\s+acknowledge\b|\bi\s+certify\b)`
	othByCheckingContext = `(?:\baccept|\bagree|\bconsent|\bpolic(?:y|ies)\b|\bterms\b|\belectronically\b|\btruth\w*|\baccurate\b|\bcertif\w*)`

	// othFirstPersonVerb follows the pronoun "I" in the catch-all route.
	othFirstPersonVerb = `(?:acknowledge|certify|attest|confirm|agree|consent|have\s+read|understand\s+and\s+agree)`

	// othPolicyStem is the bare label naming a policy or agreement. A bare
	// "Terms and Conditions" box reaches it only while the user keeps no
	// terms-and-conditions-consent entry of their own (CACI).
	othPolicyStem = `(?:polic(?:y|ies)|agreement|terms\s+(?:of\s+use|of\s+service|(?:and|&)\s+conditions)|acknowledg(?:e)?ments?|consent|code\s+of\s+conduct|disclosure\s+statement|certificate|certification|attestation)`
)

// othMaxRunes caps the folded question text. RE2 has no lookahead for a
// length bound, so label-only routes check it in code.
func othMaxRunes(n int) func(string) bool {
	return func(s string) bool { return utf8.RuneCountInString(s) <= n }
}

func othAcknowledgeRoutes() []Route {
	return []Route{
		{
			Priority: 270, Target: "privacy-policy-consent",
			Pattern: `\b` + othPrivacyNoun + `\b.*` + othConsentVerb +
				`|` + altGroup(`\breview\b`, othConsentVerb) + `.*\b` + othPrivacyNoun + `\b`,
		},
		{
			Priority: 271, Target: "privacy-policy-consent",
			Pattern: `^[^?]*\b(?:privacy\s+(?:policy|notice|statement)|data\s+(?:processing|protection)\s+notice)\b[^?]*$`,
			Exclude: `\b(?:do|did|have|are|will|would|can)\s+you\b|\bopt[- ]?(?:in|out)\b|\?`,
			Require: othMaxRunes(140),
		},
		{
			Priority: 275, Target: "truth-certification",
			Pattern: `\b(?:certify|certifies|attest|attests|confirm|confirms|affirm|affirms|declare|declares|represent|represents|swear)\b.*\b(?:true|accurate|truthful|honest|complete|provided|information|representations?)\b`,
		},
		// Vercel (Greenhouse): "Please double-check all the information
		// provided above. Ensuring accuracy is crucial, ..." over the one option
		// "I have reviewed and confirmed that all the information provided is
		// accurate and complete."
		{
			Priority: 586, Target: "truth-certification",
			Pattern: `\b(?:double[- ]check|review|verify)\s+(?:all\s+)?(?:of\s+)?(?:the\s+)?information\s+(?:(?:you(?:['\x{2019}]ve|\s+have)\s+)?(?:provided|entered|given|submitted)|above)\b.*\b(?:accura(?:te|cy)|errors?|omissions?)\b`,
		},
		// Guidepoint (Greenhouse): "If any information I have provided is
		// untrue, or if I have concealed material information, I understand
		// that this will constitute cause for the denial of employment or
		// immediate dismissal." over one "Yes": the consequence of untrue
		// answers, acknowledged.
		{
			Priority: 632, Target: "truth-certification",
			Pattern: `\b(?:understand|acknowledge|agree)\b[^?]*` + othUntrue + `[^?]*` + othConsequence + `|` + othUntrue + `[^?]*\b(?:understand|acknowledge|agree)\b[^?]*` + othConsequence,
		},
		{
			Priority: 276, Target: "truth-certification",
			Pattern: `(?:^|[^0-9_])i\s+(?:hereby\s+)?acknowledge\b[^.]*\b(?:truthful(?:ly)?|accurate(?:ly)?)\b`,
		},
		{Priority: 494, Target: "mandatory-acknowledge", Pattern: `\bconsent\b`},
		// UT Austin: "Do you agree to notify the university of any future
		// personal travel to a foreign-adversary nation ...?"
		{Priority: 495, Target: "mandatory-acknowledge", Pattern: `\b(?:willing|agree)\s+to\s+(?:submit|provide|sign)\b|\bagree\s+to\s+(?:notify|inform)\b`},
		// onX: "If extended an offer of employment, would you be willing to
		// provide professional references?" asks a Yes or No the
		// acknowledgement above cannot take.
		{Priority: 638, Target: "references-on-request", Pattern: `\b(?:willing|able|agree)\s+to\s+(?:provide|share|supply)\b[^?.]{0,30}\breferences\b`},
		// Conduct: "How do you rate your coding skills out of 10 without AI
		// assistance?"; a scale of another length is not this entry's.
		{Priority: 640, Target: "coding-skill-rating", Pattern: `\b(?:coding|programming)\s+(?:skills?|abilit(?:y|ies))\b[^?]*\b(?:out\s+of\s+(?:10|ten)|on\s+a\s+scale\s+(?:of|from)\s+(?:1|one)\s*(?:-|–|to)\s*(?:10|ten))\b`},
		{Priority: 496, Target: "mandatory-acknowledge", Pattern: `\bsubmit\b.*\belectronically\b`},
		{Priority: 497, Target: "mandatory-acknowledge", Pattern: `\belectronic\s+signature\b`},
		{
			Priority: 498, Target: "mandatory-acknowledge",
			Pattern: othByCheckingTrigger + `.{0,80}` + othByCheckingContext,
			Exclude: othEEOSelfID,
		},
		{
			Priority: 499, Target: "mandatory-acknowledge",
			Pattern: othByCheckingContext + `.{0,80}` + othByCheckingTrigger,
			Exclude: othEEOSelfID,
		},
		{
			Priority: 554, Target: "mandatory-acknowledge",
			Pattern: `(?:(?:^|[^0-9_a-z])i|(?-i:[a-z]I))\s+(?:hereby\s+)?` + othFirstPersonVerb + `\b`,
		},
		{
			Priority: 555, Target: "mandatory-acknowledge",
			Pattern: `\b(?:please|kindly)\s+acknowledge\b|\b(?:please|kindly)\s+confirm\s+your\s+acknowledg(?:e)?ment\b|\backnowledge\s+(?:the\s+)?(?:above|following|statement)\b`,
		},
		// DNSFilter (Ashby): "Please confirm you have read DNSFilter's AI Usage
		// in Hiring Practices notice (see the "Learn more" link ...)" over the
		// one box "I have read DNSFilter's AI Usage in Hiring Practices notice."
		{
			Priority: 602, Target: "mandatory-acknowledge",
			Pattern: `\bconfirm\s+(?:that\s+)?you\s+have\s+read\b[^?.]{0,120}?\b(?:notice|polic(?:y|ies)|statement|disclosure|practices)\b`,
			Exclude: `\bopt[- ]?(?:in|out)\b`,
		},
		// Kin (Ashby): "... comfortable with us collecting and processing your
		// personal information for the purpose of this application ... Your
		// data will be handled in accordance with Kin's applicant privacy
		// policy here ." over an agree-or-decline pair.
		{
			Priority: 603, Target: "privacy-policy-consent",
			Pattern: `\b(?:collect(?:ing|ion)?|process(?:ing)?|stor(?:e|ing|age)|us(?:e|ing))\b[^.?!]{0,60}\b(?:your\s+)?personal\s+(?:information|data)\b.*\b` + othPrivacyNoun + `\b`,
			Exclude: `\bopt[- ]?(?:in|out)\b|\b(?:marketing|newsletters?|future\s+(?:job\s+)?opportunit(?:y|ies))\b`,
		},
		{
			Priority: 556, Target: "privacy-policy-consent",
			Pattern: `\b(?:gdpr|general\s+data\s+protection\s+regulation|ccpa|california\s+consumer\s+privacy\s+act)\b`,
		},
		{
			Priority: 557, Target: "mandatory-acknowledge",
			Pattern: `^[^.?!]*\b` + othPolicyStem + `\b[^.?!]*$`,
			Exclude: `non[- ]?(?:compete|solicit\w*)|\bprevious\b|\bprior\b|\bdo\s+you\b|\bhave\s+you\b|\bare\s+you\b`,
			Require: othMaxRunes(60),
		},
	}
}

// annualPayRE names a yearly figure; hourlyPayAskRE asks a pay per hour.
const annualPayRE = `\b(?:annual(?:ly)?|yearly|per\s+(?:year|annum))\b`

var (
	hourlyPayAskRE = lazyregexp.New(`(?i)\b(?:hourly|per\s+hour)\b`)
	annualPayAskRE = lazyregexp.New(`(?i)` + annualPayRE)
)

// hourlyPayOnly is an ask of a pay per hour that names no yearly figure: the
// hourly rate's (routes 537 and 538), never the annual salary's (Avride:
// "Please share your hourly salary requirements for this role." took the
// yearly 120000).
func hourlyPayOnly(text string) bool {
	return hourlyPayAskRE.MatchString(text) && !annualPayAskRE.MatchString(text)
}

func othCompensationRoutes() []Route {
	rs := othCompensationRouteList()
	for i := range rs {
		if rs[i].Target == "salary-expectation" && rs[i].Veto == nil {
			rs[i].Veto = hourlyPayOnly
		}
	}
	return rs
}

func othCompensationRouteList() []Route {
	const (
		amountModifier = `(?:(?:yearly|annual|gross)\s+){0,2}`
		totalCompAsk   = `\b(?:desired|expected|target)\s+` + amountModifier + `total\s+` + amountModifier + `comp(?:ensation)?\b`
		payNoun        = `(?:salary|comp(?:ensation)?|pay)`
		wantedAdj      = `(?:expected|desired|ideal|target|preferred|minimum|maximum)`
	)
	return []Route{
		{
			Priority: 219, Target: "total-cash-compensation",
			Pattern: `\btotal\s+(?:cash\s+)?comp(?:ensation)?\b[^.?!]*\b(?:expectations?|requirements?|targets?)\b`,
		},
		{
			Priority: 220, Target: "total-cash-compensation",
			Pattern: `\b(?:total\s+cash|on[- ]target\s+earnings|ote)\b[^.?!]*\bexpectations?\b`,
		},
		{Priority: 221, Target: "total-cash-compensation", Pattern: totalCompAsk},
		// Twin of 221 for a bank without total-cash-compensation; the walk
		// skips an absent entry.
		{Priority: 222, Target: "salary-expectation", Pattern: totalCompAsk},
		{Priority: 227, Target: "salary-expectation", Pattern: `\bsalary\s+expectations?\b.*\bcurrency\b`},
		{
			Priority: 228, Target: "salary-expectation",
			Pattern: `\b(?:minimum|maximum|min|max)\s+(?:annual\s+)?` + payNoun + `\s+(?:expectations?|requirements?)\b`,
		},
		// No trailing word boundary: "expectations" and "requirements" count.
		// CharterUP writes "compensations expectations".
		{
			Priority: 229, Target: "salary-expectation",
			Pattern: `\b(?:salary|comp(?:ensations?)?|base\s+salary|base\s+pay|pay)\s+(?:expectation|requirement)|\b(?:salary|compensation)\s+preference`,
		},
		// The salary held now or before, and its history, are not the salary
		// wanted (current-salary, route 620).
		{
			Priority: 231, Target: "salary-expectation",
			Pattern: `\b(?:base\s+salary|salary|base\s+pay|pay)\b[^.?!]{0,25}\bseek(?:ing)?\b` +
				`|\b(?:indicate|provide|list|specify|enter|state|share)\b[^.?!]{0,25}\b(?:base\s+)?salary\b`,
			Exclude: `\b(?:current|present|most\s+recent|previous|prior|last)\b[^.?!]{0,25}\bsalary\b|\bsalary\s+history\b`,
		},
		{Priority: 232, Target: "salary-expectation", Pattern: `\bwhat\s+is\s+your\s+base\s+pay\s+expectation\b`},
		{
			Priority: 233, Target: "salary-expectation",
			Pattern: `\bwhat\s+(?:are|is)\s+your\s+(?:salary|comp(?:ensation)?)\s+(?:expectations?|requirements?)\b`,
		},
		{Priority: 234, Target: "salary-expectation", Pattern: `\bwhat\s+is\s+your\s+desired\s+(?:annual\s+)?salary\b`},
		{Priority: 235, Target: "salary-expectation", Pattern: `\bwhat\s+is\s+your\s+expected\s+(?:annual\s+)?salary\b`},
		{
			Priority: 236, Target: "salary-expectation",
			Pattern: `\bexpect(?:ing)?\s+to\s+(?:earn|make|be\s+paid)\b`,
		},
		{
			Priority: 237, Target: "salary-expectation",
			Pattern: `\b(?:expected|desired|ideal|target(?:ed)?|preferred|minimum|maximum|requested|asking)\s+(?:required\s+)?(?:(?:annual|monthly|yearly)\s+)?(?:base\s+)?` + payNoun + `\b` +
				`|\b(?:salary|compensation|pay)\b.{0,50}\b(?:targeting|seeking|looking\s+for)\b` +
				// Windfall (Lever): "What is your required base salary per the
				// range listed (and OTE for sales roles)?"
				`|\byour\s+required\s+(?:(?:annual|monthly|yearly)\s+)?(?:base\s+)?` + payNoun + `\b`,
		},
		{
			Priority: 238, Target: "salary-expectation",
			Pattern: `\bwhat\s+is\s+your\s+` + wantedAdj + `\s+(?:base\s+)?` + payNoun + `\b`,
		},
		// End-anchored so a sentence that merely mentions salary before another
		// ask is not claimed.
		{
			Priority: 239, Target: "salary-expectation",
			Pattern: `\b` + payNoun + `\s+(?:desired|expected|required|requested|sought|wanted|preference|range)(?:\s*\(required\))?\s*:?\s*\*?\s*$`,
		},
		{Priority: 240, Target: "salary-expectation", Pattern: `\b` + payNoun + `\s+requirements?\b`},
		// iCapital: "What is your desired bonus expectation? Please specify the
		// amount and currency." is no currency question.
		{Priority: 533, Target: "bonus-expectation", Exclude: `\b(?:sign[- ]?on|signing|referral|retention|relocation)\s+bonus\b`,
			Pattern: `\bbonus\s+(?:expectations?|target|potential)\b|\b(?:desired|expected|target(?:ed)?|anticipated)\s+(?:annual\s+)?bonus\b|\bbonus\b[^.?!]{0,30}\b(?:you\s+)?expect\b`},
		// Adobe: "Do you have any competing offer deadlines?"; Nominal: "Do you
		// have any pending offers or deadlines we should know about?"; Semgrep:
		// "Do you have any deadlines we should be aware of?".
		{
			Priority: 572, Target: "competing-offers", Exclude: `\b(?:receive|newsletters?|promotions?|marketing|special)\b|` + offerDeadlineAsk,
			Pattern: `\b(?:competing|pending|outstanding)\s+(?:job\s+)?offers?\b|\b(?:other|existing)\s+(?:job\s+)?offers?\s+(?:of\s+employment|or\s+deadlines?|with\s+deadlines?)\b|\boffers?\s+(?:deadlines?|in\s+hand)\b|\boffers?\s+or\s+deadlines?\b|` +
				`\bany\s+deadlines?\s+(?:that\s+)?we\s+should\s+(?:be\s+aware\s+of|know\s+(?:about|of))\b`,
		},
		// The relocation benefit route 133 leaves alone (Baton, Scopely, Sony).
		{Priority: 574, Target: IDRelocationAssistance, Pattern: rxRelocationAssistanceAsk},
	}
}

// othUntrue is a clause of untrue answers ("any information I have provided
// is untrue"); othConsequence what follows from them ("cause for the denial of
// employment or immediate dismissal").
const (
	othUntrue      = `\b(?:information|statements?|answers?|responses?)\b[^.?]{0,40}\b(?:is|are|be|was|were|prove[sd]?\s+to\s+be)\s+(?:untrue|false|inaccurate|incomplete|misleading)\b`
	othConsequence = `\b(?:denial|dismissal|termination|rejection|disqualif\w*|withdraw\w*)\b`
)
