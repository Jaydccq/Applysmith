package match

const (
	languageNames = `english|spanish|french|german|italian|portuguese|dutch|russian|polish|mandarin|chinese|cantonese|japanese|korean|hindi|arabic|hebrew|turkish|vietnamese|tagalog`

	relocationBenefitWords  = `\b(?:assist\w*|package\w*|reimburs\w*|stipend\w*|require\w*|need\w*|support\w*|bonus\w*|benefit\w*)\b`
	disabilityAccommodation = `\b(?:accommodat\w*|adjustment\w*|reasonable|assistance|essential function\w*)\b`
	// companyResearchAsk asks which sources one used to research the hiring
	// company (KnowBe4).
	// officeWeekdaysAsk commits to the office on the working week: "commit to
	// being in office", or "in office Monday - Friday".
	officeWeekdaysAsk  = `\bcommit\s+to\s+(?:being|working)\s+in\s+(?:the\s+)?office\b|\bin[- ](?:the\s+)?office\s+monday\s*(?:-|\x{2013}|to|through|thru)\s*friday\b`
	companyResearchAsk = `\b(?:sources?|tools?|resources?|sites?|websites?)\b[^?.]{0,60}\b(?:used|use|consulted|visited)\s+to\s+research\b[^?.]{0,40}\b(?:us|our\s+company|the\s+company)\b|\bhow\s+(?:did|have)\s+you\s+research(?:ed)?\s+(?:us|our\s+company|the\s+company)\b`
)

// phrasingRoutes are the label variants of facts other families already route:
// bare labels and unusual wordings that would otherwise reach no entry. A
// route numbered inside a family's range walks with that family; the routes
// numbered from 558 walk after every family, so an earlier route of another
// entry that matches their wording has to exclude it
// (TestDefaultBank_EveryRouteWinsItsOwnExamples).
func phrasingRoutes() []Route {
	return []Route{
		// Render (Ashby): "Current or Most Recent Title".
		{Priority: 59, Target: "current-job-title", Pattern: `^\s*(?:what is your\s+)?(?:(?:current|present)(?:\s*\(?\s*(?:/|or)\s*(?:most\s+recent|previous|former|last)\s*\)?)?|most\s+recent)\s+title\b`, Exclude: `\b(?:company|employer|organization)\b`},
		{Priority: 68, Target: "work-auth-us", Pattern: `^\s*(?:us|usa|united states)\s+work authori[sz]ation\s*\*?\s*$`},
		// ngrok: "Do you currently have legal US work authorization?" asks
		// whether the applicant has it; route 372's bare "US work authorization"
		// is the status field.
		{Priority: 576, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:do|does)\s+you\s+(?:(?:currently|presently|already)\s+)?(?:have|hold|possess)\s+(?:(?:a|an|legal|lawful|valid|current|active)\s+)*` + rxbUS + `\s*work\s+authori[sz]ation\b`},
		{Priority: 109, Target: "sponsorship-now-or-future", RequiresOptions: true,
			Pattern: `^\s*(?:visa\s+support(?:\s+(?:required|needed))?|(?:visa\s+)?sponsorship)\s*\*?\s*$`},
		{Priority: 121, Target: "preferred-work-location", RequiresOptions: true,
			Pattern: `\boffice\s+(?:of\s+)?(?:choice|preference)\b|\bworking\s+(?:out of|from|in|at)\s+(?:any of\s+)?(?:the following|these|our)\s+(?:office\s+)?(?:locations?|offices?)\b`},
		// "Where will you work? ... Choose the office listed on this job posting."
		// over the offices.
		{Priority: 562, Target: "preferred-work-location", RequiresOptions: true, Pattern: `^\W*where\s+will\s+you\s+work\b|\bchoose\s+the\s+office\b`},
		// n8n: the share of time the role travels, then whether the applicant is
		// able or willing ("travelling approximately 30–50% of the time ... Are
		// you able and willing to meet this requirement?").
		{Priority: 575, Target: "willing-to-travel",
			Pattern: `(?:\btravell?(?:ing)?\b[^.?!;]{0,80}\b\d{1,3}\s*%|\b\d{1,3}\s*%[^.?!;]{0,40}\btravell?(?:ing)?\b)[^?]*?\b(?:are|would|will|can)\s+you\s+(?:be\s+)?(?:able|willing)\b`,
			Exclude: `\b(?:reimburs\w*|expenses?|costs?)\b`},
		// Peerspace (Lever): "Are you able to meet in person at least twice a
		// year at an offsite?" and Medium's "Are you able to attend in-person
		// company offsites twice a year?" ask for travel to the company's
		// offsites, which the recorded travel ceiling answers.
		{Priority: 597, Target: "willing-to-travel",
			Pattern: `\b(?:are|would|will|can|could)\s+you\s+(?:be\s+)?(?:able|willing|open|available)\b[^?]{0,80}\boff-?sites?\b`},
		// AuthZed (Ashby): "I understand that this role requires twice-yearly
		// travel for company offsites:" acknowledges the role's travel, which
		// the recorded travel ceiling answers. A stated share of time is left
		// to the share's own route.
		{Priority: 599, Target: "willing-to-travel", Exclude: `\d{1,3}\s*%|\b(?:reimburs\w*|expenses?|costs?)\b`,
			Pattern: `^\W*i\s+(?:understand|acknowledge|confirm|agree)\b[^?]{0,40}\b(?:role|position|job)\s+(?:requires?|involves?|includes?)\b[^.?!]{0,60}\btravel`},
		// Saronic (Ashby): "Are you able to accommodate an onsite full-time work schedule?"
		{Priority: 578, Target: "office-attendance-ok", Exclude: `^\W*(?:what|which)\b`,
			Pattern: `\b(?:on-?site|in[- ](?:office|person))\s+(?:full[- ]time\s+)?(?:work\s+)?schedule\b`},
		// Brex (Greenhouse): "Are you authorized to work in the stated location of this role?"; Vanta (Ashby): "in the
		// location where this role is based". The place is the posting's.
		{Priority: 581, Target: "work-auth-us", Exclude: rxbAnyEmpOr,
			Pattern: `\b(?:(?:legally\s+)?authori[sz]ed|eligible)\s+to\s+work\s+in\s+(?:the\s+)?(?:(?:(?:stated|listed|posted|specified)\s+)?location\s+of\s+(?:this|the)\s+(?:role|position|job)|(?:this|the)\s+(?:role|position|job)['\x{2019}]s\s+location|location\s+(?:where|in\s+which)\s+(?:this|the)\s+(?:role|position|job)\s+is\s+(?:located|based))\b`},
		// Zoox (Lever): "Which location are you applying for?" over the offices;
		// the picker takes the one the posting names (postingOfficePick).
		// Elastic (Greenhouse): "In what countries do you have the unrestricted
		// right to work?"
		{Priority: 587, Target: "right-to-work-countries",
			Pattern: `\b(?:what|which)\s+countr(?:y|ies)\b[^?]{0,40}?\b(?:(?:have|hold)\s+(?:the\s+|an?\s+)?(?:(?:legal|unrestricted|full|permanent)\s+)?right\s+to\s+work|(?:legally\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work)\b`},
		// Lyft (Greenhouse): "May we contact your current employer?"; the
		// owner's default is Yes.
		{Priority: 585, Target: "contact-current-employer",
			Pattern: `\b(?:contact|reach\s+out\s+to|call|speak\s+(?:to|with))\s+your\s+(?:current|present)\s+(?:employer|supervisor|manager)\b`},
		{Priority: 584, Target: "preferred-work-location", RequiresOptions: true,
			Pattern: `\b(?:which|what)\s+(?:office\s+|work\s+|job\s+)?locations?\s+(?:are\s+you|you(?:['\x{2019}]re|\s+are))\s+applying\s+(?:for|to)\b`},
		// Cohere (Ashby): "Are you authorized to work in the country you
		// currently reside in?"; the residence is the recorded address's
		// country (postingCountryGuard). ClickUp: "the country you are based in".
		{Priority: 583, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			// Lighthouse: "Can you offer verification of your legal right to work
			// within your current country of residence?". TRM: "your current
			// country of employment", where the applicant works now. Consensys:
			// "the country in which you are currently domiciled".
			Pattern: `(?:\b(?:legally\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+(?:full[- ]?time\s+)?|\b(?:verification|proof|evidence)\s+of\s+(?:your\s+)?(?:legal\s+|lawful\s+)?right\s+to\s+work\s+)(?:in|within)\s+(?:(?:the\s+)?country\s+(?:(?:where|in\s+which|that)\s+)?you\s+(?:currently\s+)?(?:reside|live|are\s+(?:currently\s+)?(?:residing|living|based|located|domiciled))\b|(?:your\s+|the\s+)?(?:current\s+)?country\s+of\s+(?:your\s+)?(?:current\s+)?(?:residence|employment|domicile)\b|` +
				// Camunda: "the country where you're planning to work from".
				`(?:the\s+)?country\s+(?:(?:where|in\s+which|that)\s+)?you(?:['\x{2019}]re|\s+are)?\s+(?:planning|plan|intend|intending)\s+to\s+(?:live\s+and\s+)?work\s+from\b)`},
		// Anthropic (Greenhouse): "AI Policy for Application" over Yes/No, its
		// description "... confirm your understanding by selecting “Yes.”": an
		// acknowledgement whose own text names the Yes that gives it. Zapier
		// (Ashby) names it first: "Please select "Yes" to confirm your
		// understanding and agreement."
		{Priority: 582, Target: "mandatory-acknowledge", Exclude: `\byes\W+(?:or|/)\W*no\b`,
			Pattern: `\bby\s+(?:responding|answering|selecting|entering|choosing|checking|marking)\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?yes\b` +
				`|\b(?:respond|answer|select|enter|choose|check|mark)\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?yes["'\x{2018}\x{2019}\x{201C}\x{201D}]?\s+to\s+(?:confirm|acknowledge|indicate)\s+(?:that\s+)?(?:your\s+(?:understanding|agreement|acknowledg(?:e)?ment)|you\s+(?:understand|agree|have\s+read|acknowledge))\b`},
		// Lithic (Greenhouse): "Can you confirm that you can keep your camera on for the entire duration of the
		// interview?". Recording the interview is the recording consent's (route 488).
		{Priority: 588, Target: "interview-camera-on", Exclude: `\brecord(?:ed|ing|ings)?\b`,
			Pattern: `\bkeep\s+(?:your\s+)?(?:camera|webcam|video)\s+(?:on|turned\s+on|enabled)\b|\b(?:camera|webcam|video)\s+(?:on|turned\s+on|enabled)\b[^?]*\binterviews?\b|\binterviews?\b[^?]*\b(?:camera|webcam)\s+(?:on|turned\s+on|enabled)\b`},
		// Basis (Lever): "I consent to participate in an in-person interview or facial ID verification if
		// required for this role." A background check's identity verification is not it.
		{Priority: 589, Target: "interview-identity-verification", Exclude: `\b(?:background|criminal|credit|drug|employment\s+eligibility|i-?9)\b`,
			Pattern: `\b(?:consent|agree)\b[^.?!]*\b(?:in[- ]person\s+interviews?|(?:facial|face)\s+(?:id\s+|identity\s+)?(?:verification|check|scan)|(?:facial|face)\s+recognition)\b|\b(?:facial|face)\s+(?:id\s+|identity\s+)?verification\b[^?]*\b(?:consent|agree)\b` +
				// Paxos (Ashby): "we may conduct identity verification during the interview process. ...
				// Do you understand and consent to this process?"
				`|\bidentity\s+verification\s+(?:during|in|throughout|as\s+part\s+of)\s+(?:the\s+|our\s+)?interviews?(?:\s+process)?\b.*\b(?:consent|agree)\b`},
		// Alchemy (Ashby): "Are you able to conduct a coding and system design interview?"
		{Priority: 591, Target: "interview-coding-system-design", Veto: namesAI, Exclude: `\brecord(?:ed|ing|ings)?\b|\b(?:camera|webcam)\b|\b(?:llm|chatgpt|copilot)\b`,
			Pattern: `\b(?:able|willing|comfortable|available|ready|can\s+you|could\s+you)\b[^?]{0,40}\b(?:coding|system\s+design|technical)\b[^?]{0,40}\binterviews?\b`},
		// Oklo (Greenhouse): "If selected for a preliminary interview, you may
		// be asked to work through a Python coding problem. Do you acknowledge
		// this?" is the same willingness, the problem after the interview.
		{Priority: 616, Target: "interview-coding-system-design", Veto: namesAI, Exclude: `\brecord(?:ed|ing|ings)?\b|\b(?:camera|webcam)\b|\b(?:llm|chatgpt|copilot)\b`,
			Pattern: `\binterviews?\b[^?]{0,120}\b(?:coding|programming)\s+(?:problems?|exercises?|challenges?|questions?|tests?)\b`,
			AllOf:   []string{`\b(?:acknowledge|understand|agree|accept|comfortable|willing|able)\b`}},
		// Pogo (Ashby): "Have you worked at a startup or quant firm? (i.e.,
		// internship, part-time, etc)" is the startup-experience entry, which a
		// fuzzy match only reached for review. Founding one, a length of time
		// (a floor of months or years asks more) and a reason are other
		// questions.
		{Priority: 617, Target: "startup-experience", Exclude: `\bfound(?:ed|er|ing)\b|\bhow\s+(?:many|long)\b|\b(?:years?|months?)\b|\bwhy\b`,
			Pattern: `\b(?:have\s+you|did\s+you|do\s+you\s+have\s+(?:any\s+)?experience)\b[^?]{0,20}\b(?:work(?:ed|ing)?)\s+(?:at|for|in)\s+(?:an?\s+|any\s+)?(?:early[-\s]stage\s+|venture[-\s]backed\s+)?start[-\s]?ups?\b`},
		// insitro (Ashby): "is that authorization pursuant to an Employment
		// Authorization Document (EAD)? If yes, please indicate the applicable
		// EAD category code."
		// insitro (Ashby): "Did you use an AI tool to tailor your résumé,
		// write responses, or submit this application?"; only the user's own
		// answer fills it (applicationStatement).
		{Priority: 593, Target: IDAIUseDisclosure, Pattern: aiUseStatement},
		{Priority: 592, Target: idEADCategoryCode, Pattern: guardEADCategory},
		// Roboflow: "Share one link to a page or project you built that
		// showcases why you'd be great at this role." asks for the showcase
		// link; a link to a kind of product (Finch's "mobile product") is the
		// applicant's to pick.
		{Priority: 594, Target: "portfolio-url", Veto: vetoProfileURLAsk,
			Pattern: `\blink\s+to\s+(?:a|an|one|your)\s+(?:(?:web\s*)?page|project|site|website)(?:\s+or\s+(?:a\s+|an\s+)?(?:(?:web\s*)?page|project|site|website))?\s+(?:that\s+)?you(?:['\x{2019}]ve|\s+have)?\s+(?:built|made|created)\b`},
		// Openly (Ashby): "Will the target salary range listed ($145,600 -
		// $163,800) meet your salary requirements?" asks the posted range's
		// comfort; route 229 reads "salary requirements" as the figure.
		{Priority: 595, Target: "comfortable-with-posted-range", Exclude: `^\W*(?:what|which|how\s+much)\b`,
			Pattern: `\b(?:salary|pay|compensation)\s+(?:range|band)\b[^?]{0,80}\b(?:meets?|match(?:es)?|fits?|satisf(?:y|ies))\s+your\s+(?:(?:salary|compensation|pay)\s+)?(?:requirements?|expectations?|needs)\b`},
		// KnowBe4 (Greenhouse): "Please select all of the following
		// sources/tools that you have used to research KnowBe4 and learn more
		// about us." asks how the applicant researched the company, not where
		// the posting was found.
		{Priority: 596, Target: "company-research-sources", RequiresOptions: true, Pattern: companyResearchAsk},
		// Rocket Money (Greenhouse): "Are you interested in working from one of our offices?"
		{Priority: 590, Target: "office-attendance-ok",
			Pattern: `\binterested in working (?:from|in|at|out of) (?:one of )?(?:our|the|an?) (?:offices?|hq|headquarters)\b`},
		// Range (Ashby): "Can you commit to being in office Monday - Friday?",
		// with relocation to a hub if needed; the days are the whole week.
		{Priority: 598, Target: "office-attendance-ok", Pattern: officeWeekdaysAsk},
		// GitLab (Greenhouse): "What's the name you'd prefer us to use throughout the interview process?"
		{Priority: 580, Target: "preferred-name", Exclude: `\b(?:last|family|sur|middle|legal|company|employer|school|team)\s*names?\b|\bpronouns?\b|\bpronounc`,
			Pattern: `\bname\s+(?:that\s+)?(?:you(?:['\x{2019}]d|\s+would)?|would\s+you)\s+(?:prefer|like)\s+(?:(?:us|people|others)\s+)?to\s+(?:use|call\s+you|be\s+called|go\s+by)\b|\bname\s+(?:do\s+)?you\s+(?:prefer|go\s+by)\b`},
		// Wonderschool (Greenhouse): "Where did you attend and graduate from your undergraduate studies?"
		{Priority: 579, Target: "undergraduate-school", Exclude: `\b(?:major|minor|gpa|grades?|concentration|field\s+of\s+study|when|years?|dates?|live[ds]?|living|located)\b`,
			Pattern: `\bwhere\b[^?]{0,60}\bundergrad(?:uate)?\b|\b(?:institutions?|schools?|universit(?:y|ies)|colleges?)\b[^?]{0,80}\bundergrad(?:uate)?\b|\bundergrad(?:uate)?\s+(?:institutions?|schools?|universit(?:y|ies)|colleges?)\b`},
		// Arsenals (Ashby): a bare "Available from" over a date picker.
		{Priority: 577, Target: "earliest-start-date", Pattern: `^\W*(?:available\s+(?:from|starting(?:\s+on)?|on)|availability\s+date)\W*$`},
		{Priority: 132, Target: "willing-to-relocate", Pattern: `\bconsider(?:ing)?\s+relocat(?:e|ing|ion)\b`, Exclude: relocationBenefitWords},
		// Based there, or ready to be, for a stated cadence; "or willing to
		// relocate" is route 122's.
		{Priority: 143, Target: "office-attendance-ok",
			Pattern: `\b(?:based|located|living)\b[^?]*\bor\s+(?:planning|willing|able|open)\s+to\s+(?:be based|live)\b[^?]*\b(?:on-?site|hybrid|in-office|in-person)\b`},
		{Priority: 203, Target: "language-proficiency",
			Pattern: `\b(?:proficiency|fluency)\s+(?:(?:in|of)\s+)?(?:the\s+)?(?:` + languageNames + `)\b|\b(?:` + languageNames + `)\s+(?:language\s+)?(?:proficiency|fluency)\b|^\s*(?:written|oral)\s+(?:level|proficiency)\s*\*?\s*$`},
		{Priority: 230, Target: "salary-expectation", Veto: hourlyPayOnly, Pattern: `\b(?:salary|compensation|pay)\s+range\s+(?:requirements?|expectations?)\b`},
		{Priority: 245, Target: "referrer-name", Pattern: `\bif\s+you\s+(?:selected|chose|picked)\s+referral\b.*\b(?:name|who)\b`},
		{Priority: 267, Target: "future-communications-consent",
			Pattern: `\b(?:agree|consent)\b.*\bcontact\s+you\b.*\b(?:jobs?|careers?|future|other\s+opportunit\w*|openings?|positions?|roles?)\b`},
		{Priority: 303, Target: "eeo-disability", Pattern: `\bidentify\s+as\b.*\bdisabilit(?:y|ies)\b|\b(?:believe|consider)\s+(?:that\s+)?you\s+(?:have|had)\s+a\s+disability\b`, Exclude: disabilityAccommodation},
		{Priority: 333, Target: "employer-industry", RequiresOptions: true,
			Pattern: `^\s*(?:(?:type|kind|nature)\s+of\s+business|business\s+type)\s*\*?\s*$`},
		{Priority: 403, Target: "employer-relationship-with-company",
			Pattern: `\b(?:current|present|former|previous)\s+(?:(?:or|and)\s+(?:former|previous|past)\s+)?employer\b.*\b(?:(?:customer|client|vendor|supplier|reseller)\s+of|(?:business\s+)?relationship\s+with)\b`},
		{Priority: 406, Target: "work-authorization-expiry", Pattern: `\b(?:employment|work)\s+authori[sz]ation\s+(?:end|expir\w*)\s+date\b`},
		// A residence menu ("Location of Residence and Language", Jobvite's
		// data-consent region): the country the applicant lives in. A text box
		// asking where you live wants more than a country.
		{Priority: 490, Target: "address-country", RequiresOptions: true, Pattern: `\b(?:location|place)\s+of\s+residence\b`, Exclude: guardNotAddressCountry},
		// MongoDB (Greenhouse): "Chosen Name", the name the applicant goes by.
		{Priority: 502, Target: "preferred-name", Pattern: `^\s*(?:what\s+is\s+your\s+)?(?:preferred|chosen)\s+(?:first\s+)?name\b`, Exclude: `\b(?:last|family|sur|middle)\s*name\b|\bpronouns?\b`},
		// Splice, TRM Labs: willingness to take part in an on-call rotation;
		// on-call pay is another question.
		{Priority: 605, Target: "on-call-ok", Exclude: `\b(?:compensat\w*|pay|paid|stipend|rate|bonus|premium)\b`,
			Pattern: `\b(?:willing|able|comfortable|open|available|prepared|happy)\b[^?]{0,80}\bon[- ]call\b|\bparticipat\w*\s+in\s+(?:an?\s+|the\s+)?on[- ]call\b`},
		// Everlywell: "Are you comfortable supporting executives and handling
		// urgent IT requests?", a duty of the role itself (the owner's default
		// Yes); Workstream: "... regularly communicating and collaborating with
		// cross-functional teams ...?". Pay, place, travel, schedule, physical demands and the
		// interview are other questions.
		// DuckDuckGo: "Please confirm you're open to learning and using any of
		// these languages as needed" asks the same of the role's own tools.
		{Priority: 615, Target: "comfortable-with-role-duties", Exclude: `\bspoken\b|\bbenefits?\b|\brelocat\w*`,
			Pattern: `\b(?:open|willing)\s+to\s+learn(?:ing)?\b(?:\s+and\s+(?:using|use|working\s+with))?[^?.]{0,40}?(?:\b(?:languages?|tools?|technolog(?:y|ies)|frameworks?|stacks?|rust|golang|perl|python|java|typescript|javascript|scala|kotlin|swift|ruby|elixir|node(?:\.js)?)\b|\bc\+\+)`},
		{Priority: 613, Target: "comfortable-with-role-duties", Veto: namesAI,
			Pattern: `^\W*(?:are\s+you|would\s+you\s+be)\s+(?:comfortable|ok|okay|happy)\s+(?:with\s+)?(?:(?:regularly|frequently|often)\s+)?(?:supporting|handling|working\s+(?:directly\s+)?with|interacting\s+with|providing|responding\s+to|troubleshooting|assisting|owning|taking\s+on|juggling|managing\s+(?:multiple|competing|several)|communicating|collaborating|partnering)\b`,
			Exclude: `\b(?:compensat\w*|pay|paid|salary|stipend|rate|bonus|equity|relocat\w*|office|on-?site|in[- ]person|remote|hybrid|commut\w*|travel\w*|weekends?|holidays?|nights?|overnight|evenings?|shifts?|overtime|on[- ]call|hours|lift\w*|lbs|pounds|physical\w*|standing|hazard\w*|chemicals?|interview\w*|background|drug|record(?:ed|ing)?|camera|contract(?:or)?|w-?2|1099|sponsor\w*)\b`},
		// Raft: "Do you have an active Security+ certification?"; obtaining
		// one, or another certification beside it, is another question.
		{Priority: 607, Target: "security-plus-certification",
			Exclude: `\b(?:obtain|willing|earn|get|pursue|acquire|within)\b|\bor\s+(?:an?\s+)?(?:equivalent|similar|higher|other)\b` +
				`|\b(?:cissp|cism|cisa|ceh|oscp|gsec|ccna|ccnp|cysa|casp)\b`,
			Pattern: `\bsecurity\s*(?:\+|plus\b)`},
		// TRM Labs: "What is your current job search timeline? Are there any
		// deadlines or competing processes we should be aware of?"
		{Priority: 608, Target: "job-search-timeline",
			Pattern: `\b(?:job\s+)?search\s+(?:timeline|time\s*frame|status)\b|\b(?:other|competing)\s+(?:interview(?:ing)?\s+|hiring\s+|recruiting\s+)?processes\b`},
		// TRM Labs: "What are your preferred working hours, and what hours are
		// you willing to work? Please include your time zone."; hours per week
		// and shifts are other questions.
		{Priority: 609, Target: "working-hours", Exclude: `\bper\s+week\b|\bhow\s+many\s+hours\b|\bovertime\b|\bshifts?\b`,
			Pattern: `\b(?:preferred|ideal|usual|typical)\s+(?:working|work)\s+hours\b|\bwhat\s+hours\b[^?]{0,40}\b(?:willing|able|available|prefer)\b[^?]{0,20}\bto\s+work\b`},
		// Dash0: "Are you fluent in English?"; SecurityScorecard: "Are you able
		// to communicate effectively in English, both verbally and in writing,
		// ...?". Other languages than English are the second-language entry's.
		{Priority: 610, Target: "language-proficiency",
			Exclude: `\b(?:other\s+than|besides)\s+english\b|\bnon[- ]?english\b|\bany\s+(?:other\s+)?languages?\b`,
			Pattern: `\b(?:fluent|proficient)\s+in\s+(?:the\s+)?(?:` + languageNames + `)\b|\b(?:communicate|converse|speak)\b[^?]{0,60}\bin\s+(?:` + languageNames + `)\b`},
		// Lime (Ashby): "Are you applying via an automated script or
		// spoofing?"; only the user's own answer fills it. AI help with the
		// application is the ai-use-disclosure entry's.
		{Priority: 618, Target: "automated-application-disclosure", Veto: namesAI, Exclude: `\b(?:chat\s?gpt|llms?|language\s+models?)\b`,
			Pattern: `\b(?:apply(?:ing)?|submit(?:ting)?|submitted|complet(?:e|ing|ed))\b[^?]{0,40}\b(?:via|through|using|with|by)\s+(?:an?\s+|any\s+)?(?:automated\s+(?:scripts?|tools?|bots?|software|programs?|means)|bots?|scripts?)\b|\bapply(?:ing)?\b[^?]{0,60}\bspoof(?:ing|ed)?\b`},
		// OpenAI (Ashby): bar admission, the user's own answer; the states or
		// courts of admission are a list the entry does not hold.
		{Priority: 619, Target: "bar-admission", Exclude: `^\W*(?:which|what|where|list|please\s+list|in\s+which|when)\b|\b(?:year|date|number)\b`,
			Pattern: `\b(?:admitted|licensed)\s+to\s+practi[cs]e\s+law\b|\badmitted\s+to\b[^?]{0,60}\b(?:state|federal)\s+bar\b|\bmember\s+(?:in\s+good\s+standing\s+)?of\s+(?:the\s+|a\s+|any\s+)?(?:[a-z.]+\s+){0,3}bar\b(?:\s+association)?|\bbar\s+(?:admission|membership)\b|\blicensed\s+(?:attorney|lawyer)\b`},
		// Trail (Ashby): "Current Salary", the pay the applicant holds, as a
		// yearly figure; a monthly, hourly or net figure, a total and a
		// target are other questions.
		{Priority: 620, Target: "current-salary",
			Exclude: `\b(?:expect\w*|requirements?|range|desired|target\w*|monthly|month|hourly|hour|weekly|net|bonus|equity|total|currency|ote)\b`,
			Pattern: `\b(?:current|present)\s+(?:annual\s+|yearly\s+|base\s+|basic\s+|gross\s+){0,3}salary\b`},
		// "Which languages do you speak?", "Please select all the languages
		// you speak fluently.", "Languages Spoken": the spoken languages the
		// user listed. A level per language is the proficiency entry's, and so
		// are the languages besides English. The language-proficiency routes
		// (198 to 203) match the same wording and every number before them is
		// taken, so the route sorts ahead of the table: a control the list
		// cannot take still falls to them.
		{Priority: -1, Target: "languages-spoken", Exclude: `\b(?:programming|coding|software|frameworks?|levels?|cefr|proficien\w*|rate|native\s+language\s+only)\b` +
			`|\b(?:other\s+than|besides|in\s+addition\s+to)\s+english\b|\bnon[- ]?english\b|\b(?:other|additional|another|foreign|second)\s+languages?\b`,
			Pattern: `\blanguages?\s+(?:do\s+)?you\s+(?:speak|are\s+fluent\s+in)\b|\blanguages?\s+are\s+you\s+fluent\s+in\b|\blanguages?\s+(?:that\s+)?you\s+(?:can\s+)?speak\b|\bspoken\s+languages?\b|\blanguages\s+spoken\b`},
		// Anduril (Greenhouse): "Are you willing to share your academic
		// transcripts?", the user's own answer. An upload of the transcript or
		// a grade read from it is another question.
		{Priority: 622, Target: "academic-transcripts-shareable", Exclude: `\b(?:upload\w*|attach\w*|gpa|grades?)\b`,
			Pattern: `\b(?:share|provide|submit|send|furnish|release)\b[^?]{0,40}\btranscripts?\b`},
		// Wonderschool: interest in a role at a named place is the willingness to
		// work there. The place must be one the tables know, so a role "in sales"
		// stays another question.
		{Priority: 623, Target: "willing-to-relocate",
			Pattern: `\b(?:are|would)\s+you\s+(?:be\s+)?interested\s+in\s+(?:an?|this|the|our)\s+(?:[\w/-]+\s+){0,4}(?:position|role|job|opening|opportunity)\s+(?:(?:based|located)\s+)?in\b`,
			Require: func(text string) bool {
				n := namedPlacesWith(text, nil, nil)
				return len(n.Cities)+len(n.States) > 0
			}},
		// Alnylam: "Which of the following shifts should we consider you for?"
		// is the shifts the applicant can work without naming availability; a
		// preferred shift pattern, or a shift in a market or a strategy, is
		// another question.
		{Priority: 624, Target: "shift-availability", Exclude: `\bprefer\w*|\bshifts?\s+(?:in|of)\b`,
			Pattern: `\b(?:which|what)\s+(?:of\s+(?:the\s+following|these)\s+)?(?:work\s+)?shifts?\b`},
		// Skydio: the FAA Part 107 remote pilot certificate held, the user's own
		// answer; being able to get one is another question.
		{Priority: 625, Target: "faa-part-107", Exclude: `\b(?:able|willing)\s+to\s+(?:get|obtain|earn|acquire)\b`,
			Pattern: `\b(?:have|hold|possess)\s+(?:your\s+|an?\s+|the\s+)?(?:faa\s+)?part\s*107\b`},
		// Applied Intuition: an immigration case in progress (an H-1B extension,
		// a green card), the user's own answer; the visa held is another question.
		// The owner's certifications list: whether a named certification or
		// certificate is held, or the list itself. Earning one, a license
		// (professional-licenses), certifying a statement and whether the
		// user's credentials meet the role are other questions.
		{Priority: 627, Target: "certifications-held",
			Exclude: `\b(?:obtain|willing|able|earn|get|pursue|acquire|within|plan|interested)\b|\b(?:required|requires?|necessary|needed|relevant|listed|mentioned|above|role|position|job)\b|\blicen[cs]\w*|\bcertify\b|\bcertifies\b|\btruth\w*|\bcertificate\s+of\b`,
			Pattern: `^\W*(?:do|does)\s+you\s+(?:currently\s+)?(?:have|hold|possess|maintain)\b[^?]{0,80}?\b(?:certifications?|certificates?|credentials?)\b|^\W*are\s+you\s+(?:currently\s+)?(?:[\w+.-]+\s+){1,4}certified\b|^\W*(?:please\s+)?(?:list|which|what)\b[^?]{0,40}?\b(?:certifications?|certificates?)\b[^?]{0,40}?\b(?:do\s+you\s+)?(?:hold|have|earned|obtained)\b|^\W*(?:certifications?|certificates?)\W*$`},
		// A licence, charter or registration held that the question names
		// ("Do you hold the Series 7 license?", "Are you a licensed CPA?", "Are
		// you a CFA charterholder?") is the certifications list's too; one
		// described in common words ("a valid license for this state") names
		// nothing the list can hold, and the earlier licence routes keep theirs.
		// "Registered" is left out: "registered as disabled", "registered to
		// vote" and "a registered nurse in <state>" are not the list's.
		{Priority: 641, Target: "certifications-held", Veto: func(t string) bool { named, _ := credentialWording(t); return !named },
			Exclude: `\b(?:obtain|willing|able|earn|get|pursue|acquire|within|plan|interested)\b|\b(?:required|requires?|necessary|needed|listed|mentioned|above)\b` +
				`|\bdriv(?:er'?s?|ing)\b|\bnumbers?\b|\bcertif(?:y|ies)\b|\b(?:expir|suspend|revok|disciplin)\w*|\boffenders?\b|\bselective\s+service\b|\bto\s+(?:vote|work)\b`,
			Pattern: `^\W*(?:do|does)\s+you\s+(?:currently\s+)?(?:have|hold|possess|maintain)\b[^?]{0,60}?\b(?:licen[cs]es?|charter|designation|registrations?)\b` +
				`|^\W*are\s+you\s+(?:currently\s+)?(?:an?\s+)?(?:[\w+.-]+\s+){0,3}(?:licensed|chartered|charter\s*holder)\b`},
		// Vultr (Ashby): "Are you currently employed?" names no employer; it
		// asks whether the applicant has a job now.
		{Priority: 601, Target: "currently-employed",
			Pattern: `^\W*(?:are\s+you\s+(?:currently|presently|now)\s+(?:employed|working)|are\s+you\s+employed\s+(?:currently|right\s+now|at\s+the\s+moment))\W*$`},
		// Abridge, the Institute of Foundation Models: a Google Scholar
		// profile, and the publications themselves, blank answered No.
		{Priority: 628, Target: "google-scholar-url", Pattern: `\bgoogle\s+scholar\b|\bscholar(?:ly)?\s+(?:citation\s+)?(?:profile|page)\b`},
		{Priority: 629, Target: "publications",
			Pattern: `^\W*(?:(?:research|relevant)\s+)?publications?\W*$|\bfirst[-\s]author(?:ed)?\b|\b(?:do|have)\s+you\b[^?]{0,40}\b(?:publications?|published\s+(?:any\s+)?(?:papers?|research|articles?))\b|\bpublications?\b[^?]{0,40}\bmost\s+proud\b|^\W*(?:please\s+)?list\s+(?:your|any)\s+(?:research\s+)?publications?\b`},
		// A competing offer's deadline, asked after whether one is held; the
		// question whether one is held is competing-offers.
		{Priority: 630, Target: "competing-offer-deadline", Exclude: `\b(?:do|does)\s+you\s+(?:currently\s+)?have\b`, Pattern: offerDeadlineAsk},
		// 15Five (Lever): whether the salary range given is base pay or
		// on-target earnings.
		{Priority: 631, Target: "salary-range-basis",
			Pattern: `\bbase\s+(?:pay|salary)\b[^?]{0,30}\b(?:or|vs\.?|versus)\b[^?]{0,20}\b(?:on[-\s]target\s+earnings|ote)\b`},
		// Relativity (Greenhouse): night shifts asked of the role, the owner's
		// Yes; which shifts one works is shift-availability.
		{Priority: 633, Target: "occasional-night-shifts", Exclude: `\b(?:which|what|prefer\w*|differential|premium|pay|paid)\b`,
			Pattern: `\b(?:willing|able|available|open|comfortable)\b[^?]{0,40}\bwork\b[^?]{0,30}\b(?:night|overnight|graveyard)\s+shifts?\b`},
		// Inkeep (Ashby): the kinds of companies, products and content of the
		// applicant's experience (owner decision: answered from the history).
		{Priority: 634, Target: "company-types-worked-at",
			Pattern: `\b(?:what|which)\s+(?:types?|kinds?|sorts?|stages?|sizes?)\s+of\s+compan(?:y|ies)\s+have\s+you\s+(?:worked|been)\b`},
		{Priority: 635, Target: "product-types-worked-on",
			Pattern: `\b(?:what|which)\s+(?:types?|kinds?|sorts?)\s+of\s+products?\s+have\s+you\s+(?:worked|built|shipped)\b`},
		{Priority: 636, Target: "content-types-created",
			Pattern: `\b(?:what|which)\s+(?:types?|kinds?|sorts?)\s+of\s+content\b[^?]{0,40}\b(?:experience|created?|creating|made|produced?)\b`},
		{Priority: 626, Target: "active-immigration-case",
			Pattern: `\b(?:active|pending|open|ongoing|current)\s+(?:immigration|visa|green[\s-]*card)\s+(?:case|petition|application|process)\b|\bimmigration\s+(?:case|petition)\b`},
		// A request for the address, with an aside after it (Institute of
		// Foundation Models: "Please enter your personal email ID (If you havent
		// already)").
		{Priority: 522, Target: "email-address", Pattern: `^\W*(?:(?:please\s+)?(?:enter|provide|share|add|give\s+us)\s+your\s+)?(?:(?:primary|personal|preferred|contact|your)\s+)?e-?mail(?:\s+(?:address|id))?\W*(?:\([^)]*\)\W*)?$|` + agreementSigner + `\s+(?:notice\s+)?e-?mail(?:\s+address)?\W*$`},
	}
}
