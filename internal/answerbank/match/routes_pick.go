package match

import (
	"applysmith/internal/lazyregexp"
)

// leadTime asks for a length of time; startVerb is starting the job.
const (
	leadTime  = `\bhow\s+(?:much\s+time|long|many\s+(?:days|weeks|months))\b`
	startVerb = `\b(?:start|begin|join|commence)\b`
)

// pickRoutes holds the pick-option family of the route catalog: routes whose
// target entry is answered by choosing one of the rendered options.
func pickRoutes() []Route {
	var rs []Route
	for _, part := range [][]Route{
		pickRoutesStatusAndIdentity(),
		pickRoutesLocationAndAvailability(),
		pickRoutesSourceAndConsent(),
		pickRoutesSelfID(),
		pickRoutesEducation(),
		pickRoutesMilitaryAndClearance(),
		pickRoutesAddress(),
	} {
		rs = append(rs, part...)
	}
	return rs
}

const (
	// pickUSCitizenPhrase is the person-specific "US citizen" wording shared by
	// the clearance dispatchers.
	pickUSCitizenPhrase = `\b(?:u\.?\s*s\.?|united\s+states)\s+citizen\b`

	// pickClearancePhrase is the clearance half of the citizen-and-clearance
	// conjunction.
	pickClearancePhrase = `\b(?:security\s+clearance|secret(?:\s+level)?|clearance)\b`

	// pickAccommodationAsk marks a request for an adjustment, which is a
	// different question from the disability self-identification.
	pickAccommodationAsk = `accommodat|adjustment|reasonable|assistance|essential function`

	// pickStrongestCodingLanguage asks for the one language the applicant is
	// best in ("What is your strongest coding language?"): the preferred
	// language, not the list of languages used. The plural keeps the list.
	pickStrongestCodingLanguage = `\b(?:strongest|best|primary|main|most\s+(?:proficient|comfortable|experienced))\b[^.?!]{0,30}\b(?:coding|programming|scripting)\s+language\b|\b(?:coding|programming|scripting)\s+language\b[^.?!]{0,40}\b(?:strongest|best|most\s+(?:proficient|comfortable|experienced))\b`

	// pickSpokenLanguage keeps natural-language questions off the coding
	// language route.
	pickSpokenLanguage = `\b(?:speak|spoken|fluent|fluency|native|written and verbal)\b|\bproficiency in (?:english|spanish|french|german|mandarin|chinese|hindi|japanese|portuguese|arabic|korean|russian|italian)\b`

	// pickSpouseLedService is spouse-led phrasing, which belongs to the
	// military-spouse entry. "service member" after a spouse is not a service
	// verb, so a question that leads with "you served" still routes.
	pickSpouseLedService = `\bspouse\b.*\b(?:serve|served|serves|serving|armed forces|national guard|reserve|military)\b|\bdomestic partner\b`

	pickOptOut  = `\bopt[- ]?out\b`
	pickAITopic = `\b(?:artificial intelligence|ai|automated(?: employment)? decision|machine learning)\b`
	// pickStemField is a STEM field by name. A bare "related field" is related
	// to the fields beside it ("Nursing or a related field" is no STEM field),
	// so it counts only as a related quantitative field.
	pickStemField = `(?:computer\s+(?:science|engineering)|data\s+science|statistics|mathematics|software\s+engineering|electrical\s+engineering|(?:a\s+)?(?:closely\s+)?related\s+quantitative\s+field)`
	// pickSentenceGap spans one sentence, letting the periods of "Ph.D." through.
	pickSentenceGap = `(?:ph\.\s?d\.?|[^.?!])*`
	pickDegree      = `(?:degree|bachelor(?:'s)?|master(?:'s)?|ph\.?d|doctorate|bs|ms|b\.s|m\.s)`
)

func pickRoutesStatusAndIdentity() []Route {
	return []Route{
		// Declared first so no broader immigration, located-outside or
		// sponsorship route can take the nonimmigrant status picker.
		{Priority: 0, Target: "current-immigration-status", Pattern: `\byour\s+current\s+non[- ]?immigrant\s+status\b`},
		// A second citizenship is not the recorded one (secondCitizenshipNone).
		{Priority: 14, Target: "country-of-citizenship", Exclude: `\b(?:second|secondary|2nd|additional)\s+(?:country\s+of\s+)?(?:citizenship|nationality)\b`, Pattern: `\b(?:select|choose|pick|enter|indicate|specify|state|provide)\s+your\s+(?:primary\s+|current\s+)?citizenship\W*$|\bcountr(?:y|ies)\b[^?]*\bcitizenship\b|\bcitizenship\b[^?]*\bcountr(?:y|ies)\b|\bwhat\s+is\s+your\s+(?:primary\s+|current\s+)?nationality\b|^\W*(?:(?:your|primary|current)\s+)?nationality\W*$|\bcountr(?:y|ies)(?:\(ies\))?\s+(?:for|of|in)\s+which\s+you\s+(?:are|hold)\s+(?:a\s+)?citizen|\b(?:which|what)\s+countr(?:y|ies)\s+(?:are\s+you|do\s+you\s+hold)\s+(?:a\s+)?citizen`},
		{Priority: 16, Target: "countries-lived-outside-us", Pattern: `\bcountries\b[^?]*\boutside\b[^?]*\b(?:lived|live)\b|\ball\s+the\s+countries\s+outside\b`},
		{Priority: 18, Target: "current-age-bracket", Pattern: `\bwhat\s+is\s+your\s+(?:current\s+)?age\b|\bage\s+(?:range|group|bracket)\b|\bcurrent\s+age\b`},
		{Priority: 22, Target: "application-submitter-identity", Pattern: `\bwho\s+is\s+(?:submitting|completing|filling\s+out|applying)\b.*\bapplication\b|\bis\s+this\s+application\s+being\s+(?:submitted|completed)\s+by\b|\bare\s+you\s+a\s+(?:real\s+)?(?:human|person)\b.*\b(?:submitting|completing|applying)\b`},
		{Priority: 23, Target: "primary-cloud-platform", Pattern: `\b(?:which|what)\b[^.?!]*\bcloud\s+(?:platform|provider|vendor)s?\b`},
		// Sardine: "available to attend an in-person interview if requested". A
		// consent to an identity check is interview-identity-verification's.
		{Priority: 26, Target: "interview-travel-onsite", Exclude: `\b(?:consent\w*|identity|verif\w*|facial)\b`, Pattern: `\b(?:travel|fly|come)\b[^.?!]*\b(?:for|to)\s+(?:an?\s+)?(?:in[- ]person|on-?site|face[- ]to[- ]face|final[- ]round)\b[^.?!]*\binterview` +
			`|\b(?:attend|participate\s+in|come\s+in\s+for)\s+(?:an?\s+)?(?:in[- ]person|on-?site|face[- ]to[- ]face)\s+interviews?\b`},
		// 15Five (Lever): "This role may require an in-person interview.
		// Please confirm you would be to travel if needed." asks the trip in
		// the sentence after the interview.
		{Priority: 27, Target: "interview-travel-onsite", Pattern: `\binterview\b[^.?!]*\b(?:travel|fly)\s+to\b` +
			`|\b(?:in[- ]person|on-?site|face[- ]to[- ]face)\s+interviews?\b[.!]?\s+(?:please\s+)?confirm\b[^.?!]*\b(?:travel|fly)\b`},
		{Priority: 56, Target: "employer-position-function", Pattern: `\b(?:position|job)\s+function\b|\bfunctional\s+area\b`},
		{Priority: 62, Target: "work-auth-and-future-sponsorship", Pattern: `\bauthori[sz](?:ed|ation)\s+to\s+work\b[^?!]*\b(?:require|need)\w*\b[^?!]*\bsponsor`},
		{Priority: 63, Target: "work-auth-and-future-sponsorship", Pattern: `^\W*sponsorship\s+requirements?\W*$`},
		{Priority: 66, Target: "under-18-work-permit", Pattern: `\bunder\s+(?:the\s+age\s+of\s+)?18\b[^?]*\bwork\s+permit\b`},
		// Verana Health: "require ... sponsorship for employment-based
		// immigration status?" asks the sponsorship, not the status.
		{Priority: 94, Target: "current-immigration-status", Pattern: `\b(?:current\s+)?immigration\s+status\b`, Exclude: `\bsponsor\w*`},
		{Priority: 95, Target: "current-immigration-status", Pattern: `\bcurrent\s+visa\s+status\b|\bwhat\s+(?:type|kind)\s+of\s+visa\s+(?:are\s+you\s+(?:currently\s+)?on|do\s+you\s+(?:currently\s+)?(?:hold|have))\b`},
		{Priority: 96, Target: "current-immigration-status", Pattern: `\bcitizenship\s+status\b|^\W*(?:current\s+)?legal\s+status\W*$`},
		// The sponsorship-describe carve-out: a short factual field about the
		// visa status, never the narrative sponsorship slot.
		// Peerspace (Lever): "Please let us know your work status in the
		// U.S." is the authorization; a work status with no country may be
		// employment.
		// CodeRabbit (Ashby): "If you require employer sponsorship ..., please
		// explain your current work authorization status" asks the status.
		{Priority: 97, Target: "current-immigration-status", Pattern: guardStatusAsk + `|` +
			`\bwork\s+status\s+in\s+(?:the\s+)?(?:u\.?\s*s\.?(?:a\.?)?|united\s+states(?:\s+of\s+america)?)(?:\W|$)`},
		{Priority: 98, Target: "current-immigration-status", Pattern: `\b(?:current\s+or\s+future|current|future)\s+visa\s+requirements?\b`},
		{Priority: 99, Target: "current-immigration-status", Pattern: `\benter\s+(?:your\s+)?(?:current\s+)?visa\b.*\b(?:sponsorship|h-?1b|tn|e-?3|f-?1|opt|cpt)\b`},
		{Priority: 103, Target: "has-stem-degree", Pattern: `\b` + pickDegree + `\b` + pickSentenceGap + `\bin\b` + pickSentenceGap + `\b` + pickStemField + `\b`},
		{Priority: 104, Target: "has-stem-degree", Pattern: `\bmajor(?:ing|ed)\s+in\b[^.?!]*\b(?:stem|engineering|computer\s+science|data\s+science|statistics|mathematics|quantitative)\b`},
		{Priority: 322, Target: "has-stem-degree", Pattern: `\bdo\s+you\s+have\b` + pickSentenceGap + `\b(?:bachelor(?:'s)?|master(?:'s)?|ph\.?d)\b` + pickSentenceGap + `\b(?:computer\s+science|data\s+science|mathematics|statistics|quantitative)\b`},
		{Priority: 371, Target: "current-immigration-status", Pattern: `\b(?:which\s+(?:best\s+)?describes|which\s+of\s+the\s+following|select\s+the)\b.*\bwork\s+authori[sz]ation\b` +
			// Doximity: "Which work authorization best describes you?"
			`|\bwhich\s+work\s+authori[sz]ation(?:\s+status)?\s+best\s+describes\b`},
		{Priority: 372, Target: "current-immigration-status", Pattern: `\b(?:u\.?\s*s\.?|united\s+states)\s+work\s+authori[sz]ation\b`, Exclude: `\bauthori[sz]ed\s+to\s+work\s+in\b`},
		// Momentic: "What is your current legal status to work in the USA".
		{Priority: 373, Target: "current-immigration-status", Pattern: `\bwhat\s+is\s+your\s+(?:current\s+)?work\s+authori[sz]ation\b|\bwhat\s+is\s+your\s+(?:current\s+)?(?:legal|work)\s+status\s+to\s+work\b`},
	}
}

func pickRoutesLocationAndAvailability() []Route {
	return []Route{
		{Priority: 116, Target: "based-in-us-or-canada", Pattern: `\bbased\s+(?:in|out\s+of)\s+(?:the\s+)?(?:us|usa|u\.s\.a?\.?|united\s+states)\s+or\s+canada\b`},
		// A checkbox group of offices, not the yes/no relocation question; a
		// free-text box keeps off it. Datadog: "In what cities are you
		// available to work?"; Lightmatter: "Select the location you can
		// commute or relocate to".
		// Range: a commitment to the office's working week names the hubs only
		// as the places to relocate to; it is the office attendance asked.
		// Footprint: "Which office could you commit to working from? Check all
		// that apply." picks among the offices, the attendance stated before.
		{Priority: 120, Target: "preferred-work-location", RequiresOptions: true, Exclude: officeWeekdaysAsk, Pattern: `\b(?:all|which|what|the\s+following|these|our)\s+(?:of\s+(?:the\s+following|these|our)\s+)?(?:(?:office|work|job)\s+)?(?:locations?|offices?|cities|metros?|hubs?)\b[^?]*\b(?:relocat\w*|move\s+to|interested\s+in|open\s+to|(?:willing|available)\s+to\s+work|commit\s+to\s+work(?:ing)?\s+(?:from|out\s+of|at|in))\b` +
			`|\bselect\s+(?:the\s+)?(?:(?:office|work|job)\s+)?locations?\b[^?]*\b(?:commute|relocate|move)\s+to\b`},
		// 4M: "Are you legally authorized to work in your preferred location?"
		// asks the authorization.
		{Priority: 134, Target: "preferred-work-location", Exclude: `\b(?:authori[sz]ed|eligible|permitted)\s+to\s+work\b`, Pattern: `\bpreferred\s+(?:work\s+)?(?:locations?|offices?)\b`},
		{Priority: 135, Target: "preferred-work-location", Pattern: `\bwhich\s+(?:office|location|hub)s?\b[^?]*\bprefer`},
		{Priority: 136, Target: "preferred-work-location", Pattern: `\blocation\s+preferences?\b|\bpreferences?\s+(?:by|for)\s+(?:work\s+|office\s+)?locations?\b`},
		// A label that is only "Location" reaches the office picker only when
		// options render; a typeahead stays on the address entries.
		{Priority: 137, Target: "preferred-work-location", RequiresOptions: true, Pattern: `^\W*(?:(?:work|office|job)\s+)?location\W*$`},
		{Priority: 138, Target: "preferred-work-location", Pattern: `\b(?:desired|wanted)\s+(?:(?:work|job|office)\s+)?locations?\b`},
		{Priority: 139, Target: "preferred-work-location", Pattern: `\boffice\s+locations?\b[^?]*\b(?:open\s+to|willing\s+to|able\s+to)\s+work`},
		{Priority: 140, Target: "preferred-work-location", Pattern: `\bselect\s+(?:an?\s+|the\s+|your\s+)?office\s+locations?\b`},
		{Priority: 141, Target: "days-per-week-onsite", Pattern: `\bhow\s+many\s+days\b[^?]*\b(?:per|a)\s+week\b[^?]*\b(?:on-?site|in\s+the\s+office|in[- ]person|at\s+(?:a|the|our)\b[^?]*\b(?:office|location|site))\b`},
		// Fluidstack: "If presented with an offer, when would be the earliest you would be available to start?".
		{Priority: 191, Target: "offer-start-availability", Pattern: `\b(?:when|how\s+soon)\b[^?]*\bavailab(?:le|ility)\b[^?]*\boffer\b|\boffer\b[^?]*\b(?:when|how\s+soon)\b[^?]*\bavailab(?:le|ility)\b`},
		{Priority: 192, Target: "offer-start-availability", Pattern: `\bavailab(?:le|ility)\b[^?]*\b(?:if|after|once|upon)\b[^?]*\b(?:offer\b[^?]*\b(?:accept\w*|offered)|offered)\b`},
		// The notice before a start: the notice period by name, the bare label
		// "Availability", or the availability to start. An availability for an
		// interview, a call or a shift, and a system's (Automox: "a high
		// availability SaaS environment"), are other questions.
		{Priority: 193, Target: "availability-notice-period", Pattern: `\bnotice\s+period\b|^\W*availability\W*$|\bavailability\s+(?:to\s+(?:start|begin|join|commence)|date)\b`},
		// Sent: "If you will relocate, how much time would you need?" is the
		// notice period, as the owner answers any question of when.
		{Priority: 639, Target: "availability-notice-period", Pattern: `\brelocat\w*\b[^?]*` + leadTime + `|` + leadTime + `[^?]*\brelocat\w*`, Exclude: `\b(?:assistance|support|package|stipend|bonus)\b`},
		// Q2: "From the point of an offer, how much time would you need before
		// you're able to start?" names no availability.
		{Priority: 564, Target: "offer-start-availability",
			Pattern: `\boffer\b[^?]*` + leadTime + `[^?]*` + startVerb + `|` + leadTime + `[^?]*(?:\boffer\b[^?]*` + startVerb + `|` + startVerb + `[^?]*\boffer\b)`},
		{Priority: 208, Target: "vaccination-policy-acknowledge", Pattern: `\bcovid(?:-?19)?\b[^?]*\bvaccinat|\bvaccination\s+policy\b|\bfully\s+vaccinated\b`},
		{Priority: 212, Target: "preferred-coding-language", Pattern: `\b(?:prefer(?:red|ence)?|choice\s+of|comfortable\s+coding\s+in)\b[^?]*\b(?:coding|programming|development)\s+language|\b(?:coding|programming)\s+languages?\b[^?]*\b(?:preference|prefer\w*|choice|interview\s+in)\b|` + pickStrongestCodingLanguage, Exclude: pickSpokenLanguage},
		{Priority: 213, Target: "full-time-start-availability", Pattern: `\b(?:when|how\s+soon)\b[^?]*\bavailab(?:le|ility)\b[^?]*\bfull[- ]?time\b`},
		{Priority: 215, Target: "desired-employment-type", Pattern: `\bdesired\s+(?:type\s+of\s+)?employment\b|\btype\s+of\s+employment\b|\bemployment\s+type\b`},
	}
}

// rxSourceDetail asks for more about the source the applicant named (Jane
// Street: "Additional information (for source)"): the source detail, no prose.
const rxSourceDetail = `\b(?:additional|more|further)\s+(?:info(?:rmation)?|details?)\s*\(?\s*(?:for|about|on|regarding)\s+(?:the\s+|your\s+)?(?:referral\s+)?source\b`

func pickRoutesSourceAndConsent() []Route {
	return []Route{
		// The follow-up box asks for the specific channel, so it precedes the
		// picker routes that would answer their own fallback.
		// Jane Street: "Additional information (for source)" is the same box.
		{Priority: 250, Target: "referral-source-specify", Pattern: `\bhow\s+(?:were|did)\s+you\s+` + pickFirstly + `(?:referred|hear\s+about|find\s+out\s+about|learn\s+about|find|come\s+across)\b.*\b(?:specify|explain|describe)\b` +
			`|` + rxSourceDetail,
			// Avride: "... or other (please describe fully)" is the ask of one example, not of the box.
			Exclude: `\bor\s+other\s*\(\s*(?:please\s+)?(?:describe|explain|specify|elaborate)\b`},
		{Priority: 251, Target: "referral-source", Pattern: `\bhow\s+(?:were|did)\s+you\s+` + pickFirstly + `(?:referred|hear\s+about)\b`},
		{Priority: 252, Target: "referral-source", Pattern: `\bhow\s+did\s+you\s+` + pickFirstly + `(?:find\s+out\s+about|learn\s+about|find)\b`},
		{Priority: 253, Target: "referral-source", Pattern: `\b(?:referral|source\s+of\s+referral|source\s+of\s+application)\s+(?:source|type)\b|\b(?:candidate|applicant|application)\s+source\b`},
		{Priority: 254, Target: "referral-source", Pattern: `\bhow\s+did\s+you\s+` + pickFirstly + `(?:come\s+across|come\s+to\s+know)\b`},
		{Priority: 260, Target: "ai-tools-familiarity", Pattern: `\bfamiliar(?:ity)?\b[^.?!]*\bai\s+tool(?:s|ing)?\b`},
		{Priority: 265, Target: "future-communications-consent", Pattern: `\bregarding\s+future\s+(?:positions?|openings?|opportunit(?:y|ies)|roles?)\b`},
		{Priority: 266, Target: "future-communications-consent", Pattern: `\breceive\s+communications?\b[^?]*\bfuture\b`},
		{Priority: 272, Target: "terms-and-conditions-consent", Pattern: `\bterms\s+(?:and|&)\s+conditions\b`},
		{Priority: 273, Target: "assessment-proctoring-consent", Pattern: `\bproctor(?:ing|ed)?\b`},
		{Priority: 274, Target: "assessment-proctoring-consent", Pattern: `\bconfirm\s+and\s+agree\b[\s\S]{0,160}\bassessments?\b`},
		{Priority: 283, Target: "years-relevant-experience", Pattern: `\byears?\s+of\s+relevant\s+professional\s+(?:work\s+)?experience\b`},
		// The job-source channel picklist follows the referral routes.
		{Priority: 340, Target: "job-source", Pattern: `\bhow\s+did\s+you\s+(?:learn|hear)\s+(?:about|of)\b.*\b(?:job|position|opening|role|opportunity)\b`},
		// Infisical: "How did you discover Infisical? Many engineers discover
		// Infisical through our open source GitHub repo ...".
		{Priority: 341, Target: "job-source", Pattern: `\bhow\s+did\s+you\s+(?:become\s+aware\s+(?:of|about)|(?:first\s+)?discover)\b`},
		// P&G: "What source(s) of information influenced your decision to apply?"
		{Priority: 567, Target: "job-source", Pattern: `\bsources?(?:\s*\(s\))?\s+of\s+information\b[^?]*\b(?:influenc\w*|led|prompt\w*)\b[^?]*\bapply\b`},
		{Priority: 342, Target: "job-source", Pattern: `\bhow\s+did\s+you\s+(?:hear|find\s+out)\s+about\s+us\b`},
		// CACI follows a "Job Board" source with "Which job board?". Qualtrics
		// asks "Where have you seen Qualtrics prior to applying?"; SentiLink
		// "Where did you learn about this job?" over a group of boxes.
		{Priority: 343, Target: "job-source", Pattern: `\bwhere\s+(?:did|do)\s+you\s+(?:first\s+)?(?:see|find|hear\s+about|learn\s+(?:about|of)|come\s+across)\b|\bwhere\s+have\s+you\s+(?:first\s+)?(?:seen|heard\s+(?:about|of)|come\s+across)\b|\bwhat\s+brought\s+you\s+to\s+(?:this|our|the)\s+(?:job\s+)?(?:posting|listing|ad|advert)\b|^\W*which\s+job\s+(?:board|site|website)(?:\s*\??\s*$|\s+did\s+you\b)`, Exclude: `\bseen?\s+yourself\b`},
		{Priority: 476, Target: "ai-screening-opt-out", Pattern: pickOptOut + `[^.?!]*` + pickAITopic + `|` + pickAITopic + `[^.?!]*` + pickOptOut},
		{Priority: 477, Target: "aedt-consent", Pattern: `\bautomated\s+employment\s+decision\s+tools?\b|` +
			`\bmay\s+use\s+(?:automated\s+tools?|ai|artificial\s+intelligence)\b[^.?!]{0,60}\b(?:review|screen\w*|evaluat\w*|assess\w*)\b[^.?!]{0,20}\byour\s+application\b`},
		{Priority: 478, Target: "aedt-consent", Pattern: `\baedt\b`},
		// HelloFresh: "Interview Recording and AI Notetaker — Notice" names the
		// recording and states the consent in its options ("I consent to ...");
		// Palantir: "we may use AI notetakers", Sierra: "an AI notetaking tool".
		{Priority: 488, Target: "interview-recording-consent", Pattern: `\b(?:brighthire|interview\s+recording|recorded|capture)\b[^.?!]*\b(?:consent|confirm)\b|` +
			`\binterview\s+recording\b|\binterviews?\b[^.?!]*\bnote-?\s?tak(?:ers?|ing)\b|\bnote-?\s?tak(?:ers?|ing)\b[^.?!]*\binterviews?\b` +
			// Block: "Do you consent to us using AI to transcribe and summarize your interview?"
			`|\b(?:ai|software|tool)\b[^.?!]*\b(?:transcrib\w*|summari[sz]\w*)\b[^.?!]*\binterviews?\b` +
			// Calendly: "the interview may be recorded/transcribed", a notice asked as an opt-in.
			`|\binterviews?\b[^.?!]{0,40}\b(?:may|will|might|could)\s+be\s+(?:audio[- ]|video[- ])?(?:recorded|transcribed)\b`},
		{Priority: 489, Target: "interview-recording-consent", Pattern: `\b(?:consent|confirm)\b[^.?!]*\b(?:brighthire|interview\s+recording|recorded|capture)\b`},
		{Priority: 501, Target: "salutation", Pattern: `\b(?:salutation|courtesy\s+title|name\s+prefix|title\s+prefix|honorific)\b`},
	}
}

func pickRoutesSelfID() []Route {
	return []Route{
		{Priority: 293, Target: "pronouns", Pattern: `\bpronouns?\b`},
		// An EEO "What is your Sex?" is the gender question (vetoSexNotGender).
		{Priority: 294, Target: "eeo-gender", Pattern: `\bgender\b|\bsex\b`, Veto: vetoSexNotGender},
		// "racial" keeps the combined racial/ethnic picker on race; the
		// exclusion leaves the Hispanic yes/no question to ethnicity.
		{Priority: 295, Target: "eeo-race", Pattern: `\braces?\b|\bracial\b`, Exclude: `\bare\s+you\s+hispanic\b`},
		{Priority: 296, Target: "eeo-ethnicity", Pattern: `\bethnicity\b|\bhispanic(?:\s*[/-]\s*|\s+or\s+|\s+)?latino\b|\bare\s+you\s+hispanic\b|\bethnic\s+(?:background|origin)\b`},
		{Priority: 297, Target: "eeo-veteran", Pattern: `\bveteran\b`},
		{Priority: 298, Target: "eeo-disability", Pattern: `\bdisability\s+status\b`},
		{Priority: 299, Target: "eeo-disability", Pattern: `\b(?:yes,?\s+i\s+have|have\s+had|currently\s+have)\s+a\s+disability\b`},
		{Priority: 300, Target: "eeo-disability", Pattern: `\bself[- ]?identify\b.*\bdisabilit`},
		{Priority: 301, Target: "eeo-disability", Pattern: `\bdisabilit(?:y|ies)\b.*\b(?:self[- ]?identify|status|self[- ]?disclose)`},
		{Priority: 302, Target: "eeo-disability", Pattern: `\b(?:do\s+you\s+(?:currently\s+)?have|are\s+you)\b.*\b(?:a\s+)?disabilit`},
		{Priority: 304, Target: "eeo-disability", Pattern: `\bdo\s+you\s+consider\s+yourself\b.*\bdisabilit\w*\s+or\s+(?:a\s+)?long[- ]term\s+health\s+condition`, Exclude: pickAccommodationAsk},
		{Priority: 305, Target: "eeo-disability", Pattern: `\bi\s+(?:have|do\s+not\s+have|don't\s+have)\s+a\s+disability\b`, Exclude: pickAccommodationAsk},
		{Priority: 306, Target: "eeo-disability", Pattern: `^\W*disability\b`, Exclude: pickAccommodationAsk + `|require`},
		// The binary identification framing wins over the category question.
		{Priority: 307, Target: "eeo-lgbtq-identification", Pattern: `\b(?:identify\s+as|consider\s+yourself)\b.*\b(?:2s)?lgbt`},
		{Priority: 308, Target: "eeo-orientation", Pattern: `\bsexual\s+orientation\b|\blgbtq?`},
		{Priority: 417, Target: "eeo-veteran", Pattern: `\byour\s+military\s+status\b`},
	}
}

// pickFirstly is the adverb a referral ask may put before its verb ("How did
// you first learn about Affirm as an employer?").
const pickFirstly = `(?:first\s+|initially\s+|originally\s+)?`

var (
	genderWordRE = lazyregexp.New(`(?i)\bgender\b`)
	sexOtherRE   = lazyregexp.New(`(?i)\bbirth\b|\boffen[cs]e|\boffender|\bconvict|\bharass|\bdiscriminat|\bsame\s+sex\b`)
)

// vetoSexNotGender keeps a question that says sex, not gender, off the gender
// entry when it asks about sex at birth, an offence, harassment or
// discrimination.
func vetoSexNotGender(text string) bool {
	return !genderWordRE.MatchString(text) && sexOtherRE.MatchString(text)
}

var (
	pickMajorRE        = lazyregexp.New(`(?i)\bmajor\b`)
	pickMajorAdjective = lazyregexp.New(`(?i)^\s+(?:illness|life|medical|health|surgeries|surgery)`)
	pickFieldOfStudyRE = lazyregexp.New(`(?i)\bfield\s+of\s+study\b|\barea\s+of\s+study\b|\bdiscipline\b`)
)

// requireFieldOfStudy holds the rule the old negative lookahead expressed: the
// text names a field of study, or holds a "major" that is not followed by an
// adjective-sense noun. One surviving occurrence is enough.
func requireFieldOfStudy(text string) bool {
	if pickFieldOfStudyRE.MatchString(text) {
		return true
	}
	for _, loc := range pickMajorRE.FindAllStringIndex(text, -1) {
		if !pickMajorAdjective.MatchString(text[loc[1]:]) {
			return true
		}
	}
	return false
}

func pickRoutesEducation() []Route {
	return []Route{
		{Priority: 313, Target: "field-of-study-choice", Pattern: `\bif\s+(?:you\s+(?:don't|do\s+not)\s+see|your)\b[^.?!]*\b(?:major|field\s+of\s+study|area\s+of\s+study|discipline)\b[^.?!]*\b(?:listed|enter|type|specify)\b`},
		{Priority: 314, Target: "current-degree-year", Pattern: `\bcurrent\s+degree\s+year\b|\b(?:what|which)\s+year\s+of\s+(?:your\s+)?(?:degree|programme|program|study|studies)\b`},
		// The current-level routes precede the bare "level of education".
		{Priority: 315, Target: "current-level-education-pursuing", Pattern: `\b(?:current\s+)?level\s+of\s+education\s+(?:you\s+are\s+|are\s+you\s+)?(?:currently\s+)?pursuing\b`},
		{Priority: 316, Target: "current-level-education-pursuing", Pattern: `\bcurrent\s+level\s+of\s+education\b`},
		{Priority: 317, Target: "undergraduate-major", Pattern: `\b(?:undergraduate|bachelor(?:'s)?|undergrad)\b[^?!]*\b(?:field\s+of\s+study|area\s+of\s+study|major|discipline)\b`},
		// Catch-all: Require carries the "major not followed by an
		// adjective-sense noun" rule RE2 cannot state as a lookahead.
		{Priority: 320, Target: "field-of-study-choice", Pattern: `\bfield\s+of\s+study\b|\barea\s+of\s+study\b|\bmajor\b|\bdiscipline\b`, Exclude: `\bdisabilit(?:y|ies)\b`, Require: requireFieldOfStudy},
		{Priority: 323, Target: "highest-level-education", Pattern: `^\s*degree\s*\*?\s*$`},
		{Priority: 324, Target: "highest-level-education", Pattern: `\b(?:highest(?:\s+level)?|level)\s+of\s+education\b|^\W*(?:highest\s+)?education(?:\s+level)?(?:\s+degree\b.*)?\W*$`},
		{Priority: 325, Target: "highest-level-education", Pattern: `\bhighest\s+education\s+level\b`},
		{Priority: 326, Target: "highest-level-education", Pattern: `\bhighest\s+degree\b`},
		{Priority: 327, Target: "highest-level-education", Pattern: `\bdegree\s+(?:attained|obtained|earned|completed|held|achieved)\b`},
		{Priority: 328, Target: "highest-level-education", Pattern: `\blevel\s+of\s+degree\b`},
		{Priority: 329, Target: "expected-graduation-month", Pattern: `\b(?:expected|actual)\s+graduation\s+month\b`},
		{Priority: 330, Target: "expected-graduation-year", Pattern: `\b(?:expected\s+)?graduation\s+year\b`},
		{Priority: 331, Target: "expected-graduation-year", Pattern: `\bwhen\s+(?:will\s+you(?:\s+be)?|do\s+you(?:\s+expect\s+to)?|are\s+you)\s+graduat`},
		// The term picker follows the month and year routes.
		{Priority: 334, Target: "expected-graduation-term", Pattern: `\b(?:expected|anticipated)\s+graduation\b`, Exclude: `\bgraduation\s+(?:month|year)\b`},
		// A label that names two of the admission tests is the SAT entry's
		// (Cursor: "SAT / ACT / GRE Score"); one test alone stays its own entry's.
		{Priority: 571, Target: "sat-score", Pattern: `\b(?:sat|act|gre)\b\s*(?:/|,|\bor\b|&|\band\b)\s*\b(?:sat|act|gre)\b`},
	}
}

func pickRoutesMilitaryAndClearance() []Route {
	return []Route{
		{Priority: 439, Target: "military-branch", Pattern: `\bmilitary\s+branch\b|\bbranch\s+of\s+(?:the\s+)?(?:military|service|armed\s+forces|uniformed\s+services)\b`},
		// The span of 440 stops at the period of "U.S."; 441 is the wider twin.
		{Priority: 440, Target: "military-service-status", Pattern: `\b(?:serve|served|serving|member)\b[^.?!]*\b(?:reserve\s+component|national\s+guard|air\s+guard|armed\s+forces|u\.?s\.?\s+military)\b`, Exclude: pickSpouseLedService},
		{Priority: 441, Target: "military-service-status", Pattern: `\bserv\w*\b[^?!]*\b(?:armed\s+forces|u\.?s\.?\s+military|military\s+service)\b`, Exclude: pickSpouseLedService},
		// A conjunction of the citizen phrase and the clearance phrase (either
		// order), or eligibility for a clearance, which is the citizenship it
		// requires (Sphinx Defense: "This position requires eligibility to
		// obtain and maintain a U.S. security clearance."); it precedes the
		// clearance level routes.
		{Priority: 442, Target: "clearance-citizenship-gated", Pattern: pickUSCitizenPhrase + `|\beligibility\s+(?:to\s+(?:obtain|hold|receive|maintain)|for)\b|\beligible\s+(?:to\s+hold|for)\b`, AllOf: []string{pickClearancePhrase}},
		{Priority: 443, Target: "willing-to-obtain-clearance", Pattern: `\b(?:willing|able|eligible)\s+to\s+obtain\b[^.?!]*\b(?:security\s+clearance|secret(?:\s+level)?|top\s+secret|ts\s*/\s*sci|clearances?)\b`, Exclude: pickUSCitizenPhrase},
		{Priority: 444, Target: "clearance-active-status", Pattern: `\bclearance\s+(?:is\s+)?(?:currently\s+)?(?:is\s+)?active\b`, Exclude: pickUSCitizenPhrase},
		{Priority: 445, Target: "clearance-agency", Pattern: `\bfrom\s+which\s+(?:government\s+|federal\s+)?agency\b`},
		// Arsenals: the plural label "Security Clearances" over Yes and No.
		{Priority: 449, Target: "security-clearance", Pattern: `\b(?:active\s+|current\s+)?security\s+clearances?\b`},
		{Priority: 450, Target: "security-clearance", Pattern: `\bgovernment\s+(?:security\s+)?clearances?\b`},
		{Priority: 451, Target: "security-clearance", Pattern: `\bclearance\s+(?:level|status|type)\b`},
		{Priority: 452, Target: "security-clearance", Pattern: `\blevel\s+(?:of\s+)?clearance\b|\bclearance\s+(?:do\s+you\s+)?(?:hold|have|possess)\b|\b(?:hold|have|possess)\s+(?:an?\s+|any\s+)?clearance\b`},
		{Priority: 453, Target: "us-citizen-status", Pattern: `\b(?:u\.?\s*s\.?\s*a?\.?|united\s+states)\s+citizen(?:s|ship)?\b|\bcitizen\s+of\s+the\s+` + rxbUS},
	}
}

var (
	pickStateVerbRE = lazyregexp.New(`(?i)\bstate\s+(?:your|their|his|her|its|the|whether|if|how|why|what|when|that|in (?:detail|brief|writing)|any|all|here|below|briefly|clearly|explicitly)\b`)
	pickDeterminer  = lazyregexp.New(`(?i)\b(?:the|which|what|a|any)\s$`)
)

// vetoStateVerb reports the verb sense of "state" ("state your reason", "state
// whether"). The old clause was a negative lookbehind: the verb reading does
// not apply when the word before "state" is a determiner, as in "list the
// state that you plan to be located in". Any occurrence that passes vetoes.
func vetoStateVerb(text string) bool {
	for _, loc := range pickStateVerbRE.FindAllStringIndex(text, -1) {
		if !pickDeterminer.MatchString(text[:loc[0]]) {
			return true
		}
	}
	return false
}

// pickAddressStateExclude holds the readings of "state" that are not the
// address field: compensation context, status wording, a request to state
// something, a technical noun adjunct, government wording and the place a
// credential was granted in.
const pickAddressStateExclude = `\b(?:salary|compensation|base\s+pay|wages?|incentive\s+pay)\b|\b401\s*\(?k\)?|\b(?:do\s+not|don't|cannot|can't|never)\s+state\b|\bstate\s+(?:negotiable|n/a)` +
	`|\bwork\s+authori[sz]ation\b|\bvisa\s+(?:state|status)\b|\bstate\s+of\s+(?:mind|residence\s+is|the\s+art)\b` +
	`|\b(?:please|kindly)\s+state\b` +
	`|\bstate\s+(?:management|machines?|transitions?|diagrams?|variables?|handling|stores?|updates?|synchronization|sync)\b|\b(?:application|shared|global|local|component|ui|server|client|internal|mutable|immutable)\s+state\b` +
	`|\b(?:federal|state|local)\b.{0,40}\bgovern(?:ments?|mental)\b|\bstate\s+street\b|\bstate[-\s]owned\b|` +
	guardCredentialPlace

func pickRoutesAddress() []Route {
	return []Route{
		{Priority: 507, Target: "address-country", Pattern: `\b(?:country|nation)(?:\s*/\s*region)?\b[^.?!]*\b(?:located|location|reside|residing|based|live|living)\b`, Exclude: guardNotAddressCountry},
		{Priority: 512, Target: "live-in-listed-states", Pattern: `\blive\s+in\s+any\s+of\s+the\s+following\s+states\b|\bdo\s+you\s+(?:currently\s+)?(?:live|reside)\s+in\s+(?:any\s+of\s+)?(?:the\s+)?(?:following|these)\s+(?:states|locations)\b`},
		{Priority: 513, Target: "address-state", Pattern: `\b(?:state|province)\b|^\W*region\W*$`, Exclude: pickAddressStateExclude, Veto: vetoStateVerb},
		{Priority: 515, Target: "address-country", Pattern: `\b(?:country|nation)\b`, Exclude: guardNotAddressCountry},
		// Crypto.com (Lever): "Please select the option(s) that best describes your location and schedule flexibility."
		{Priority: 517, Target: "time-zone", Pattern: `\btime\s*zone\b|\byour\s+location\s+and\s+(?:schedule|working\s+hours|work\s+hours|hours)\b`},
		{Priority: 520, Target: "phone-device-type", Pattern: `\bphone\s+(?:device\s+)?type\b`},
		{Priority: 521, Target: "phone-device-type", Pattern: `\bdevice\s+type\b|\btype\s+of\s+phone\b`},
	}
}
