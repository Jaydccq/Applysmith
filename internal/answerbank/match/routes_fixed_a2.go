package match

// Family A, part 2: eligibility batteries, status questions, location
// presence and office attendance.

const (
	faUSName   = `(?:united states|usa|u\.s\.|us)(?:[^a-z0-9]|$)`
	faOfficeAt = `(?:in[- ]person|on-?site|in (?:the |our )?office)`
)

func fixedAWotc() []Route {
	return []Route{
		{Priority: 3, Target: "wotc-long-term-unemployed",
			Pattern: `\bunemployed\b.*\b27 consecutive weeks\b`},
		// The benefit is written in capitals or beside its noun; "Snap" alone is
		// a company.
		{Priority: 4, Target: "wotc-snap-recipient",
			Pattern: `\bsupplemental nutrition assistance\b|\b(?-i:SNAP)\b.*\b(?:months?|received|benefits?)\b|\bsnap\s+(?:benefits?|assistance)\b`},
		{Priority: 5, Target: "wotc-vocational-rehab-referral",
			Pattern: `\bvocational rehab\w*`},
		{Priority: 6, Target: "wotc-tanf-family",
			Pattern: `\btanf\b|\btemporary assistance for needy families\b`},
		{Priority: 7, Target: "wotc-felony-year-before-hire",
			Pattern: `\bfelony\b.*\byear before\b.*\bhire|\breleased from (?:a )?prison\b.*\bfelony\b`},
		{Priority: 8, Target: "wotc-ssi-recipient",
			Pattern: `\bsupplemental security income\b|\bssi benefits?\b`},
		{Priority: 9, Target: "criminal-conviction-history",
			Pattern: `\b(?:convicted|convictions?|criminal (?:offen[cs]es?|records?|charges?|histor(?:y|ies))|plead(?:ed)?\s+(?:guilty|no contest))\b`,
			Exclude: `\b(?:firearms?|ammunition|domestic violence|under indictment|military court|export(?:ation)?|itar|defense articles|released from prison|fugitive)\b|\byear before\b.*\bhire|\brenounced\b.*\bcitizenship\b`},
		{Priority: 10, Target: "held-h1b-past-6-years",
			Pattern: `\bh-?1b\b.*\b(?:preceding|past|last|prior)\s+(?:six|6)\s+years?\b|\bheld\s+h-?1b\s+status\b.*\b(?:six|6)\s+years?\b`},
		{Priority: 11, Target: "ever-held-j1-status",
			Pattern: `\bheld\s+j-?1\s+status\b`},
		{Priority: 15, Target: "lived-outside-us-12-consecutive-months",
			Pattern: `\b(?:lived|located|living)\b[^?]*\boutside\b[^?]*\b(?:12|twelve)\s+consecutive\s+months\b`},
		{Priority: 17, Target: "age-minimum-threshold",
			Pattern: `\b(?:are|were) you\b.*(?:\b\d{2}\s*\+|\bat least \d{2}\b(?:\s*(?:years?|yrs?)\b|\s+(?:of age|old)\b|\s*[?.,;:)]|\s*$|\s+(?:and|or)\b)|\b\d{2} years(?: of age)? (?:or )?(?:older|above|over)\b|\baged? \d{2} (?:or|and) (?:older|above|over)\b)|\b\d{2}\s*\+\s*years of age\b`,
			Exclude: `\bwork permit\b`},
		{Priority: 21, Target: "stem-opt-extension-expiry",
			Pattern: `\bstem opt\b.*\b(?:expir\w*|end date|valid through)\b|\b(?:expir\w*|end date|valid through)\b.*\bstem opt\b`},
		{Priority: 29, Target: "date-of-birth", Pattern: `\bbirthday\b`},
		{Priority: 42, Target: "relatives-employed-list",
			Pattern: `\b(?:list|provide|identify|enter)\b.*\brelatives\b|\bnames? of (?:any )?relatives?\b` +
				// The detail after "are you related to an employee?" (Qualtrics: "If you answered yes to being
				// related to a current Qualtrics employee, please specify name and relationship"), which the
				// yes/no routes below had taken, writing a bare No into the text box.
				`|\brelated\s+to\b[^?]*\b(?:specify|provide|list|give|state|enter|include)\b[^?]*\bnames?\b[^?]*\brelationships?\b` +
				`|\bnames?\b[^?]*\brelationships?\b[^?]*\b(?:to|with|of)\s+(?:the|this|that|an?|any)\s+(?:current\s+)?(?:employee|staff\s+member|team\s+member)s?\b`},
		{Priority: 51, Target: "non-compete-or-non-solicitation-reason",
			Pattern: guardNonCompeteReason},
		{Priority: 52, Target: "non-compete-or-non-solicitation",
			Pattern: `\bnon[- ]?(?:compete|competition|solicit\w*)\b`},
		{Priority: 53, Target: "non-compete-or-non-solicitation",
			Pattern: `\bpost[- ]employment (?:restrictions?|obligations?|covenants?|agreements?|clauses?)\b`},
		{Priority: 54, Target: "non-compete-or-non-solicitation",
			Pattern: `\brestrictive covenants?\b|\bgarden leave\b`},
		{Priority: 55, Target: "non-compete-or-non-solicitation",
			Pattern: `\bcontractual restrictions?\b.*\b(?:ability to work|working (?:at|for|with))\b`},
		{Priority: 559, Target: "non-compete-or-non-solicitation",
			Pattern: `\b(?:agreements?|understandings?|contracts?)\b[^?]{0,60}\b(?:prevent|prohibit|restrict|preclude)\w*\s+(?:you\s+from\s+(?:joining|working|accepting|(?:fully\s+)?performing)|your\s+ability\s+to\s+(?:work|join|accept|compete))\b`},
		// ERCOT: "Are you currently being sponsored by another company in the
		// US?" asks about the visa held now, not a future need.
		{Priority: 560, Target: idCurrentlyEmployerSponsored,
			Pattern: `\b(?:are|is)\s+(?:you|your\s+(?:visa|status|work\s+authori[sz]ation))\s+(?:currently\s+|now\s+)?(?:being\s+)?sponsored\s+by\b`},
		{Priority: 91, Target: "unrestricted-right-to-work-us",
			Pattern: `\bunrestricted right to work in (?:the )?` + faUSName,
			Exclude: guardSponsorshipMention},
		// Niantic: "authorized to work without restrictions in the country where
		// this position is based"; the posting-country guard binds the country.
		{Priority: 92, Target: "unrestricted-right-to-work-us",
			// EnergyHub: "unlimited and unrestricted authorization to work in the United States".
			Pattern: `\bunrestricted (?:work )?authori[sz]ation\b|\bauthori[sz]ed\s+to\s+` + guardAdverb + `work\b[^?.!]{0,60}\bwithout\s+(?:any\s+)?restrictions?\b`,
			Exclude: guardSponsorshipMention},
		// Qrypt: "Will you now ... require Qrypt to sponsor ... any immigration
		// paperwork ..., including ... OPT to H-1B conversion" names OPT as one
		// sponsorship of many.
		{Priority: 101, Target: "currently-in-opt",
			Pattern: `\b(?:currently|presently|now)\b.*\b(?:period of )?(?:optional practical training|opt)\b|\bin a period of (?:optional practical training|opt)\b`,
			Exclude: guardSponsorshipMention},
		{Priority: 102, Target: "stem-opt-24-month-extension",
			Pattern: `\b24[- ]month\b.*\bopt\b.*\bextension\b|\b(?:stem )?opt extension\b.*\bstem\b|\beligible\b.*\bopt extension\b`},
		// Turbo AI: "... degree from a college or university located in the
		// United States?" places the school, not the applicant.
		{Priority: 118, Target: "currently-living-us-or-canada", Exclude: `\b(?:degree|college|universit\w*|school|institution)\b[^?]{0,40}\b(?:located|based) in\b`,
			Pattern: `\b(?:living|live|residing|located|reside|based) in the ` + faUSName},
		{Priority: 119, Target: "current-based-country",
			Pattern: `\bwhere are you (?:currently|presently|now) (?:based|located)\b`},
		{Priority: 124, Target: "based-in-named-metro",
			Pattern: `\b(?:are|do) you currently (?:based|located|living|live) (?:in|out of)\b|\bare you (?:currently )?local to\b|` +
				// Orkes: "Are you currently located within a drivable distance of
				// either of those areas?", the places named before it.
				`\b(?:are|do) you (?:currently )?(?:based|located|living|live) within (?:a )?(?:drivable|driving|commut\w*|reasonable) distance\b|` +
				// Middesk: "Are you located in the New York or SF area?", behind a hybrid office sentence;
				// FT Partners: "Do you live in the San Francisco Bay Area?".
				`\b(?:are you (?:based|located|living) (?:in|out of)|do you (?:live|reside) in) the [^?]{2,60}? (?:area|metro(?:politan)? area|region)\b|` +
				// Unify: "Are you located in San Francisco or New York City?", the places named alone. The
				// United States is route 118's, which runs first; another named country is read from the
				// recorded address's country.
				`\bare you (?:based|located|living) (?:in|out of) [^?]{2,60}\?\s*$`,
			// A time zone is no metro ("Are you based in a US or equivalent timezone?"),
			// and neither is a region (River: "Are you located in the Americas or Europe?").
			// "or willing to be based there" asks more than where the applicant is.
			Exclude: `\bor\s+(?:planning|willing|able|open)\s+to\s+(?:be\s+based|live)\b|^\W*(?:what|which)\b|\btime\s*zones?\b|\b(?:north|south|latin|central)\s+america\b|\bamericas\b|\b(?:latam|europe|emea|apac|asia|africa|middle\s+east)\b`},
		{Priority: 204, Target: "date-of-birth",
			Pattern: `\b(?:date of birth|birth date|dob)\b`},
	}
}

// fixedAOffice is the office-attendance family. A question that goes on to ask
// where the applicant lives is a residence question, and one that asks whether
// they need help to move is the relocation-assistance entry's (route 574): the
// exclusions are added to every route here, so no twin of a route that leaves
// such a question claims it instead.
func fixedAOffice() []Route {
	rs := fixedAOfficeList()
	for i := range rs {
		if rs[i].Exclude != "" {
			rs[i].Exclude += `|`
		}
		rs[i].Exclude += faResideAsk + `|` + rxRelocationAssistanceAsk
	}
	return rs
}

func fixedAOfficeList() []Route {
	return []Route{
		// Rogo (Ashby): "Are you able, willing, and excited to work full-time in an office?", a list before "to".
		{Priority: 142, Target: "office-attendance-ok",
			// Pika: "able to regularly work from our Palo Alto HQ", the HQ a place;
			// Character: "open to working full-time in our ... offices"; Flex:
			// "able to attend ... in-person New Hire Onboarding at our NYC HQ".
			Pattern: `\b(?:able(?:,?\s+(?:and\s+)?(?:willing|excited|eager|happy|ready|prepared|comfortable))*\s+to|(?:open|willing)\s+to|can you|could you) (?:(?:regularly|consistently|reliably|physically) )?(?:work(?:ing)?|come in|come into|come to|go to|attend)\b[^?]*\b(?:offices?|(?:from|in|at|out\s+of)\s+(?:(?:our|the)\s+)?(?:[\w-]+\s+){0,3}(?:hq|headquarters))\b`},
		// MaintainX (Ashby): "Are you able to work in the location listed for
		// this role?" asks to work at the posting's place, which the relocation
		// policy answers as it does route 131's "the listed location"; being
		// authorized to work there is the work-authorization entry's.
		{Priority: 171, Target: "willing-to-relocate", Exclude: `\b(?:authori[sz]ed|eligible|legally|permitted|sponsor\w*|visa)\b`,
			Pattern: `\b(?:able|willing)\s+to\s+work\s+(?:in|from|at)\s+(?:the\s+)?location\s+(?:(?:stated|listed|posted|specified)\s+)?(?:for|of)\s+(?:this|the)\s+(?:role|position|job)\b`},
		// Camber (Ashby): "Are you aware that Camber is based in New York City and
		// looking for team members to work hybrid from our office in Flatiron?"
		// asks the applicant to accept the office's attendance.
		{Priority: 600, Target: "office-attendance-ok",
			Pattern: `\b(?:are\s+you\s+aware|do\s+you\s+(?:understand|acknowledge))\b[^?]{0,200}\b(?:hybrid|in[- ]office|on-?site|in[- ]person)\b[^?]{0,80}\b(?:office|hq|headquarters)\b`},
		// Antithesis (Ashby): "... this role is fully onsite with the team. Are
		// you willing to commit to this?", the arrangement stated first and the
		// willingness asked of it. Relocating to it is the relocation entry's.
		{Priority: 604, Target: "office-attendance-ok", Exclude: `\brelocat`,
			Pattern: `\b(?:fully|entirely|completely|exclusively|100\s*%)\s+(?:on-?site|in[- ]office|in[- ]person)\b[^?]{0,200}\b(?:willing|able|comfortable|ok|okay|open|prepared|happy)\b[^?]{0,60}\?`},
		// Treasury Prime: "regularly commute to our San Francisco data center".
		{Priority: 144, Target: "office-attendance-ok",
			Pattern: `\bcommut(?:e|ing)\s+to\b.*\b(?:office|location|site|data\s+cent(?:er|re)|campus|facility|headquarters|hq)\b`},
		{Priority: 145, Target: "office-attendance-ok",
			Pattern: `\bable to work from\b.*\boffice\b.*\b` + faNum + ` ` + faWeek + `\b`},
		{Priority: 146, Target: "office-attendance-ok",
			Pattern: `\bwork(?:ing)? (?:from|out of)\b.*\b(?:office|hq|headquarters)\b.*\b` + faNum + ` ` + faWeek + `\b`},
		// CLEAR: "has a 5 days in-office expectation. Are you comfortable with this schedule?", the days first.
		{Priority: 147, Target: "office-attendance-ok",
			Pattern: `\b(?:in[- ]office|hybrid)\b.*\b` + faNum + ` (?:days?|times?)\b|\b` + faNum + ` days? (?:(?:a|per) week )?(?:in[- ]office|on-?site)\b`},
		{Priority: 148, Target: "office-attendance-ok",
			Pattern: `\b` + faNum + ` ` + faWeek + `\b.*\boffice\b`},
		// TRM: "Have you previously worked in a remote or hybrid environment?"
		// asks a past, not acceptance of this one.
		// Interest in a role in a named place is route 623's.
		{Priority: 149, Target: "office-attendance-ok", Exclude: `\bhave\s+you\s+(?:ever\s+|previously\s+|already\s+)?(?:worked|been\s+working)\s+(?:in|on|from)\b|\binterested\s+in\s+(?:an?|this|the|our)\b[^?]{0,40}\b(?:role|position|job)\b[^?]{0,20}\bin\b`,
			Pattern: `\bhybrid (?:position|role|job|opportunity|schedule|model|environment|set-?up|arrangement|(?:work\s+)?policy)\b`},
		{Priority: 150, Target: "office-attendance-ok",
			Pattern: `\b(?:working|come in|come into|report|reporting) on-?site (?:at least|at|every|on)\b`},
		{Priority: 151, Target: "office-attendance-ok",
			Pattern: `\b` + faNum + ` ` + faWeek + `\b.*\b(?:on-?site|in (?:the )?office)\b`},
		{Priority: 152, Target: "office-attendance-ok",
			Pattern: `\b(?:work|working|reporting) on-?site\b`},
		{Priority: 153, Target: "office-attendance-ok",
			Pattern: `\b(?:comfortable|ok|okay) with\b.*\b` + faNum + `\s*(?:-|to)\s*` + faNum + ` days?\b.*\b(?:office|on-?site)\b`},
		{Priority: 154, Target: "office-attendance-ok",
			Pattern: `\b` + faNum + `\s*(?:-|to)\s*` + faNum + `\s+days?(?:\s*/\s*week|\s+(?:a|per)\s+week)?\b.*\boffice\b`},
		{Priority: 155, Target: "office-attendance-ok",
			Pattern: `\b` + faNum + `\s+days?\s*/\s*week\b.*\boffice\b`},
		{Priority: 156, Target: "office-attendance-ok",
			Pattern: `\bin[- ]person\b.*\b` + faNum7 + ` ` + faWeek + `\b`},
		{Priority: 157, Target: "office-attendance-ok",
			Pattern: `\b` + faNum7 + ` ` + faWeek + `\b.*\bin[- ]person\b`},
		{Priority: 158, Target: "office-attendance-ok",
			Pattern: `\bon-?site\b.*\b` + faNum + ` ` + faWeek + `\b`},
		{Priority: 159, Target: "office-attendance-ok",
			Pattern: `\b` + faOfficeAt + `\b.*\b\d+\s?x (?:a|per) week\b`},
		{Priority: 160, Target: "office-attendance-ok",
			Pattern: `\b\d+\s?x (?:a|per) week\b.*\b` + faOfficeAt + `\b`},
		// Spara: "Would you be excited to work from our NYC office 3+ days per week?"
		{Priority: 161, Target: "office-attendance-ok",
			Pattern: `\b(?:willing|excited|happy|eager) to work (?:in|at|from|out of)\b.*\boffice\b`, Exclude: `\bwork (?:from|at) home\b`},
		{Priority: 162, Target: "office-attendance-ok",
			Pattern: `\bbased (?:out of|in|at)\b.*\boffice\b.*\bdays? (?:a|per) week\b`},
		// Conduct: "Are you happy being in person every day in our NY office?"
		{Priority: 163, Target: "office-attendance-ok",
			Pattern: `\b(?:comfortable (?:working|to work)|happy (?:working|to work|being in[- ]person|being on[- ]?site))\b.*\boffice\b`},
		{Priority: 164, Target: "office-attendance-ok",
			Pattern: `\b(?:on-?site|in[- ]person) (?:setting|environment|work environment|workplace)\b`,
			Exclude: `\b(?:which|what)\b.*\blocation\b|\blocation (?:would|do|could) you\b`},
		{Priority: 165, Target: "office-attendance-ok",
			Pattern: `\bthis (?:job|role|position) (?:will be|is) (?:physically|in[- ]person|on-?site|located)\b`,
			// Clera: "work authorization to work in the country where this job is located" names the place, not the office.
			// Hadrian: the office sentence leads a picker of the applicant's situation.
			// Orkes: the role's place, then where the applicant lives now.
			Exclude: `\bwhere this (?:job|role|position)\b|\bwhich of the following (?:best )?describes\b`},
	}
}
