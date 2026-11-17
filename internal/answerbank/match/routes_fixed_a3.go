package match

// Family A, part 3: consent flags, referrals, licences and the compliance
// disclosure groups.

const (
	faSeniorFamilyWho = `(?:family relationship|family member|close personal contact)`
	faThirdParty      = `(?:customer|client|vendor|third[- ]party)`
	faDisciplined     = `(?:disciplined|sanctioned|reprimanded|censured)`
	faLicenceKind     = `(?:professional|industry|job[- ]related|trade|state|regulatory)\s+(?:license|certification|credential|registration)s?`
	faBoardKind       = `(?:board of directors|advisory board|trustee board)`
	faTermination     = `(?:terminated|fired|let go|dismissed|discharged)`
	faDriverLicence   = `\b(?:driver'?s?|driving) licen[sc]e`
	faSeniorGovOnly   = `\bsenior government official\b`
)

// publicEmployers are the employers besides a government that a public
// office disclosure names; ownFamily is the applicant's family, never a royal one.
const (
	publicEmployers = `political\s+part(?:y|ies)|royal\s+family|candidate\s+for\s+(?:political\s+)?office`
	ownFamily       = `\b(?:your\s+(?:immediate\s+)?famil(?:y|ies)|family\s+members?|household|relatives?|spouse|close\s+personal\s+relationship)\b`
)

func fixedAConsent() []Route {
	return []Route{
		{Priority: 169, Target: "sign-onboarding-documents",
			Pattern: `\b(?:willing|able|prepared|happy|comfortable)\b.*\bsign\b.*\bonboarding documents?\b`},
		{Priority: 170, Target: "marketing-email-optin",
			Pattern: `\b(?:stay up to date|job alerts?|alerts? for similar|newsletter|keep me informed)\b`},
		{Priority: 175, Target: "ok-to-contact", Pattern: `\bok(?:ay)? to contact\b`},
		{Priority: 182, Target: "meets-role-requirements",
			Pattern: `\bdo you meet (?:(?:these|the|all|our|above|following|minimum|listed|stated)\s+)+(?:requirements|criteria|qualifications|eligibility)\b`},
		// DNSFilter (Ashby): "Before applying, please confirm that you have read
		// the entire job description and understand and have experience with
		// the role qualifications and responsibilities needed for this role."
		{Priority: 183, Target: "meets-role-requirements",
			Pattern: `\bdo you have (?:at least )?the (?:basic|minimum|required)\s+(?:job\s+)?(?:requirements|qualifications)\b` +
				`|\bconfirm\s+(?:that\s+)?you\s+(?:have\s+read|understand)\b[^?]{0,160}\b(?:have\s+(?:the\s+)?experience\s+(?:with|in)|meet|possess)\s+(?:all\s+)?(?:the\s+|our\s+)?(?:(?:role|job|position|minimum|basic|required|stated|listed)(?:['\x{2019}]s)?\s+)*(?:qualifications|requirements)\b`},
		{Priority: 195, Target: "speaks-non-english-language",
			Pattern: `\b(?:proficient|fluent|speak|able to speak|conversant)\b.*\blanguages?\b.*\b(?:other than english|besides english|non-english|in addition to english)\b`},
		{Priority: 196, Target: "speaks-non-english-language",
			Pattern: `\bdo you speak\b.*\b(?:additional|other|another|foreign|second)\s+languages?\b`},
		{Priority: 197, Target: "speaks-non-english-language",
			Pattern: `\b(?:bilingual|multilingual)\b`},
		// SpaceX: "Can you perform all of the essential functions of this role
		// with or without reasonable accommodations?" asks about ability, not
		// about an accommodation.
		{Priority: 565, Target: "essential-functions-able",
			Pattern: `\b(?:can|could|are)\s+you\s+(?:able\s+to\s+)?perform\b[^?]*\bessential\s+(?:job\s+)?functions?\b`},
		{Priority: 206, Target: "accommodations-none",
			Pattern: `\bwhat\b.*\b(?:reasonable )?accommodations?\b`},
		{Priority: 207, Target: "low-income-background",
			Pattern: `\blow[- ]?income\b`},
		{Priority: 209, Target: "accommodation-needed",
			Pattern: `\b(?:do|does|will|would) you (?:require|need)\b.*\b(?:reasonable )?accommodations?\b`},
		{Priority: 210, Target: "accommodation-needed",
			Pattern: `\brequire (?:any )?(?:reasonable )?accommodations?\b`},
		{Priority: 211, Target: "accommodation-needed",
			Pattern: `\bneed (?:any )?(?:reasonable )?accommodations?\b`},
		{Priority: 216, Target: "preferred-method-of-contact",
			Pattern: `\bpreferred (?:method|means|mode|way) of contact\b|\bpreferred contact (?:method|preference)\b|\bcommunication methods?\b|\bhow (?:would you|do you|you) (?:like|prefer|want) to (?:be contacted|receive information|receive updates)\b`},
		{Priority: 217, Target: "career-goal",
			Pattern: `\bwhat are you looking for in your next (?:role|opportunity|position)\b`},
		{Priority: 218, Target: "career-goal",
			Pattern: `\bwhat do you want in your next (?:role|opportunity|position)\b`},
		// Trail asks "Can you confirm you've reviewed the compensation band and
		// are comfortable proceeding?"; "what range are you comfortable with"
		// asks for a figure. Amplify: "Does the posted salary or hourly pay
		// range align with your expectations?", ahead of the hourly rate. Flex:
		// "Does your salary fall within our estimated range for this role?".
		{Priority: 224, Target: "comfortable-with-posted-range",
			Pattern: `\b(?:comfortable with|align(?:ed)? with)\b.*\b(?:posted salary|salary range|salary band)\b|\b(?:compensation|salary|pay)\s+(?:band|range)\b.*\bcomfortable\b|\b(?:compensation|salary|pay)\s+(?:band|range)\s+aligns?\s+with\s+your\b|\b(?:does|do|will|would)\s+your\s+(?:(?:salary|compensation|pay)\s+)?(?:salary|compensation|pay|expectations?|requirements?)\s+(?:fall|fit|lie|sit)\s+(?:with)?in\s+(?:our|the|this)\s+(?:\w+\s+){0,2}(?:range|band)\b`,
			Exclude: `^\W*(?:what|which|how\s+much)\b`},
		{Priority: 226, Target: "work-country",
			Pattern: `\bcountr(?:y|ies)\b.*\b(?:anticipat\w*|expect\w*|plan(?:ning)? on)\s+(?:to\s+)?work|\bwhich countr(?:y|ies)\b.*\bwill you be working\b`,
			Exclude: `\b(?:citizen|nationalit|passport|visa|authori[sz]|sponsor|permanent resident|green card)`},
		// The acceptability conjunction (compensation noun and acceptability
		// word, in either order) is the pattern plus an all-of entry.
		{Priority: 241, Target: "comfortable-with-posted-range",
			Pattern: `\b(?:compensation|salary|pay)\b`,
			AllOf:   []string{`\b(?:acceptable|agreeable|works?\s+for\s+you)`},
			Exclude: `\bacceptable\s+(?:\w+\s+){0,2}(?:salary|compensation|pay|rate|wage)\b`},
		{Priority: 242, Target: "referrer-name",
			Pattern: `^\s*referred by\s*[:*]?\s*$`},
		{Priority: 243, Target: "referrer-name",
			Pattern: `\breferrer(?:'s)? name\b`},
		// Allium: "Who referred you ..., or how did you find out about us?" takes
		// how the applicant found the company when no one did: the
		// referral-source route 252 claims it, and the job-source route 342
		// when that entry binds none of the options. Anima's "Who referred you
		// or where did you see our job ad?" takes where the applicant saw the
		// posting (route 343).
		{Priority: 244, Target: "referrer-name",
			// Tenex: "If you were referred by a Current Employee, please list
			// their name."; GHX: "referred to this position by ..., please
			// provide the person's name".
			Pattern: `\bname of (?:the )?(?:referrer|referring (?:employee|person))\b|\bwho\s+referred\s+you\b|` +
				`\bif\s+(?:you\s+(?:were|was|have\s+been)\s+)?referred\s+(?:to\s+(?:this|the)\s+(?:position|role|job)\s+)?by\b[^?.]*\b(?:list|provide|enter|share|give|include|type|write)\s+(?:their|the|his|her)\s+(?:(?:person|individual|employee|referrer)(?:['\x{2019}]s)?\s+)?(?:full\s+)?name\b`,
			Exclude: `\bor\s+how\s+did\s+you\s+(?:find\s+out|hear|learn)\s+(?:about|of)\b|\bor\s+where\s+did\s+you\s+(?:first\s+)?(?:see|find|hear\s+about|come\s+across)\b`},
		{Priority: 246, Target: "referred-by-current-employee",
			Pattern: `\b(?:were|are) you (?:being )?(?:referred|a referral)\b`,
			Exclude: `\bgovernment official\b`},
		// Leap: "Have been referred by a current employee ...?", the subject left out.
		{Priority: 247, Target: "referred-by-current-employee",
			Pattern: `\b(?:have|did) you (?:been )?(?:referred|received a referral)\b|^\W*have been referred\b`,
			Exclude: `\bgovernment official\b`},
		{Priority: 248, Target: "referred-by-current-employee",
			Pattern: `\b(?:referred|referral) by (?:a )?(?:current )?employee\b`,
			Exclude: `\b(?:if|when|please provide|provide the)\b`},
		{Priority: 249, Target: "referred-by-current-employee",
			Pattern: `\bdo you know anyone\b.*\b(?:work|works|worked|working|employed|employee)\b.*\b(?:at|for|with)\b`},
		{Priority: 262, Target: "consider-other-roles-consent",
			Pattern: `\b(?:considered|consideration)\b.*\bother (?:roles?|positions?|opportunit(?:y|ies))\b.*\bconsent\b`},
		{Priority: 263, Target: "consider-other-roles-consent",
			Pattern: `\b(?:willing|like|wish|happy|want|interested) to be considered for (?:other|future|additional) (?:roles?|positions?|opportunit(?:y|ies)|openings?)\b`},
		{Priority: 264, Target: "consider-other-roles-consent",
			Pattern: `\bconsent\b.*\b(?:us(?:e|ed|ing)|process\w*|transfer\w*|stor(?:e|ed|ing))\b.*\b(?:personal data|personal information)\b.*\b(?:worldwide|globally|other roles|other countries)\b`},
		{Priority: 268, Target: "human-verification-choice",
			Pattern: `\b(?:filter|prevent|stop|screen)\b.*\bautomated submissions?\b.*\bselect option b\b.*\bhuman\b`},
		{Priority: 269, Target: "human-verification-choice",
			Pattern: `\bselect option b\b.*\b(?:applying as a human|human applicant|not (?:a )?bot|not automated)\b`},
		{Priority: 277, Target: "nda-acknowledgment",
			Pattern: `\b(?:abide by|adhere to|comply with|agree to)\b.{0,80}\bnon[- ]?disclosure agreement\b`},
		{Priority: 278, Target: "nda-acknowledgment",
			Pattern: `\b(?:have read|read and agree|accept|acknowledge(?: that i have read)?)\b.{0,80}\bnon[- ]?disclosure agreement\b`},
		{Priority: 335, Target: "director-officer-investor-5pct",
			Pattern: `\b(?:director|officer|advisor|adviser|consultant|partner|investor)\b.*\bgreater than 5\s*%`},
		{Priority: 344, Target: "age-18", Pattern: `\b(?:over|at least)\s+(?:the age of\s+)?18\b`},
		{Priority: 345, Target: "age-18", Pattern: `\b18\s+(?:years|or older)\b`},
		{Priority: 346, Target: "age-18", Pattern: `\blegal age to work\b`},
		// The owner's rule: being able to prove one's age is the legal working age.
		{Priority: 347, Target: "age-18", Pattern: `\bof legal (?:working )?age\b|\b(?:proof|documentation|evidence|verification)\s+of\s+(?:your\s+)?age\b`},
		{Priority: 348, Target: "age-18", Pattern: `\bminimum (?:working )?age\b`},
		// The owner's rule: being able to verify one's identity on hire is the
		// identity the profile names.
		{Priority: 558, Target: "identity-verification",
			Pattern: `\b(?:provide|furnish|present|produce|show|submit)\b[^?]{0,40}\b(?:proof|documentation|documents|evidence|verification)\s+(?:of|verifying|establishing)\s+(?:your\s+)?identity\b`},
		{Priority: 349, Target: "age-21", Pattern: `\b(?:over|at least)\s+(?:the age of\s+)?21\b`},
		{Priority: 350, Target: "age-21", Pattern: `\b21\s+(?:years|or older)\b`},
		// The word is often misspelled or split (Auror: "backgound checks").
		// Crusoe's safety-sensitive "drug/alcohol program" is a testing
		// program, as a drug screen is a test.
		{Priority: 351, Target: "drug-bg-check",
			Pattern: `\bdrug(?:\s*(?:/|and|&)\s*alcohol)?\s+(?:test|screen|program)\w*|\bback\s?(?:ground|gound|groud|grond|round)\s+(?:check|investigation)\w*`},
		{Priority: 352, Target: "driving-requirements-attestation",
			Pattern: `\b(?:verifiable driving experience|monitoring of my mvr|revocation of driving privileges|driving privileges within)\b`},
		// Discipline on a licence is the yes/no disclosure, not the list of
		// licences held.
		{Priority: 309, Target: "admin-agency-discipline",
			Pattern: `\bdisciplinary\s+actions?\b[^?]{0,60}\b` + faLicenceKind + `\b|\b` + faLicenceKind + `\b[^?]{0,40}\b(?:revoked|suspended|disciplined|sanctioned)\b`,
			Exclude: faDriverLicence + `|\b(?:healthcare|health care|medicare|medicaid)\b`},
		{Priority: 353, Target: "professional-licenses",
			Pattern: `\b(?:hold|have|possess|maintain)\b.*\b` + faLicenceKind + `\b`,
			Exclude: faDriverLicence},
		{Priority: 354, Target: "professional-licenses",
			Pattern: `\b` + faLicenceKind + `\b`,
			Exclude: faDriverLicence},
		{Priority: 355, Target: "professional-licenses",
			Pattern: `\blicen[sc]es?\s*(?:,|/|and)\s*certifications?\b`,
			Exclude: faDriverLicence},
		{Priority: 356, Target: "finra-registered", Pattern: `\bfinra\b`},
		{Priority: 357, Target: "finra-registered", Pattern: `\bfinancial industry regulatory authority\b`},
		{Priority: 358, Target: "finra-registered",
			Pattern: `\b(?:registered|licensed) (?:representative|broker[- ]dealer)\b`},
		{Priority: 365, Target: "terminated-suspended",
			Pattern: `\b` + faTermination + `\b.*\b(?:employment|employer|employee|job|position|role|work)\b`},
		{Priority: 366, Target: "terminated-suspended",
			Pattern: `\b` + faTermination + `\s*(?:or|and|/)\s*suspended\b`},
		{Priority: 367, Target: "terminated-suspended",
			Pattern: `\bsuspended\b.*\b(?:previous|prior|past|former)\s+(?:employment|employer|job|position)\b`},
	}
}

func fixedAConflict() []Route {
	return []Route{
		{Priority: 374, Target: "senior-government-official",
			Pattern: `\bare you (?:a |currently a )?senior government official\b`},
		{Priority: 375, Target: "senior-government-official",
			Pattern: `\bserved? as (?:a )?senior government official\b`},
		{Priority: 376, Target: "senior-government-official-referral",
			Pattern: `\b(?:did|has) a senior government official\b.*\brefer\w*`},
		{Priority: 377, Target: "senior-government-official-family",
			Pattern: `\b` + faSeniorFamilyWho + `\b.*` + faSeniorGovOnly},
		{Priority: 378, Target: "senior-executive-referral",
			Pattern: `\b(?:did|has) a senior executive\b.*\b` + faThirdParty + `\b.*\brefer\w*`},
		{Priority: 379, Target: "senior-executive-referral",
			Pattern: `\bsenior executive of (?:a )?` + faThirdParty + `\b.*\brefer\w*`},
		{Priority: 380, Target: "senior-executive-family",
			Pattern: `\b` + faSeniorFamilyWho + `\b.*\bsenior executive\b.*\b` + faThirdParty + `\b`},
		{Priority: 381, Target: "client-affiliation-conflict",
			Pattern: `\b(?:officer|board member|director)\b.*\bclient\b`,
			Exclude: `\bsenior (?:government official|executive)\b|\bboard of directors\b`},
		{Priority: 382, Target: "client-affiliation-conflict",
			Pattern: `\bclient\b.*\b(?:officer|board member|director)\b`,
			Exclude: `\bsenior (?:government official|executive)\b|\bboard of directors\b`},
		{Priority: 383, Target: "client-affiliation-conflict",
			Pattern: `\b(?:worked (?:for|with|as)|employed by)\b.*\bclient of\b`},
		{Priority: 384, Target: "admin-agency-discipline",
			Pattern: `\b` + faDisciplined + `\b.*\bby an administrative agency\b`},
		{Priority: 385, Target: "admin-agency-discipline",
			Pattern: `\bsubject of an administrative (?:order|action|proceeding)\b`},
		{Priority: 386, Target: "admin-agency-discipline",
			Pattern: `\b(?:sanctioned|disciplined|reprimanded|censured|barred|debarred|fined|penalized)\b.*\b(?:professional|regulatory|licensing|government|administrative)\s+(?:body|authority|agency|board|commission|organization)\b`,
			Exclude: `\b(?:healthcare|health care|medicare|medicaid)\b`},
		{Priority: 387, Target: "admin-agency-discipline",
			Pattern: `\b` + faDisciplined + `\s+by\s+(?:any\s+)?(?:professional|regulatory|licensing)\b`,
			Exclude: `\b(?:healthcare|health care|medicare|medicaid)\b`},
		{Priority: 388, Target: "fiduciary-appointments",
			Pattern: `\bfiduciary (?:appointment|designation|capacity|role|position)s?\b`},
		{Priority: 389, Target: "fiduciary-appointments",
			Pattern: `\b(?:executor|personal representative|administrator|guardian|trustee)\b.*\b(?:customer|client|fiduciary)\b`},
		{Priority: 390, Target: "board-position",
			Pattern: `\b(?:serve|serving|served|member|sit|sitting)\b(?:\s+in\s+(?:any\s+)?capacity)?(?:\s+as\s+a\s+member)?\s+on\s+(?:a\s+|an\s+|any\s+)?` + faBoardKind + `\b`},
		{Priority: 391, Target: "board-position",
			Pattern: `\b` + faBoardKind + `\b.*\b(?:for[- ]profit|non[- ]?profit|organization)\b`},
		{Priority: 392, Target: "board-position",
			Pattern: `\bare you (?:currently )?(?:a )?member of\b.*\bboard\b`},
		{Priority: 397, Target: "competitive-outside-activities",
			Pattern: `\boutside activit(?:y|ies)\b.*\b(?:competitive|competition|competitors?|conflict\w*)\b`},
		{Priority: 398, Target: "competitive-outside-activities",
			Pattern: `\b(?:competitive|competition|competitors?|conflict\w*)\b.*\boutside activit(?:y|ies)\b`},
		{Priority: 399, Target: "business-ownership-25pct",
			Pattern: `(?:\b25\s*%|\b10\s*%|\btwenty[- ]five percent\b|\bten percent\b).*\bownership interest\b`},
		{Priority: 400, Target: "business-ownership-25pct",
			Pattern: `\bownership interest\b.*\bfor[- ]profit business\b`},
		{Priority: 401, Target: "position-of-control",
			Pattern: `\bposition of control\b`},
		{Priority: 402, Target: "government-position",
			Pattern: `\b(?:elected|appointed)\s+(?:official|member|director|officer)\b`,
			Exclude: faSeniorGovOnly},
		{Priority: 404, Target: "government-position",
			Pattern: `\b(?:government entity|public agency|city council|school board|political party committee)\b`,
			Exclude: faSeniorGovOnly + `|\bpolitical campaign\b`},
		{Priority: 405, Target: "government-employee-history",
			Pattern: `\b(?:employee|employed|employment|work|worked|working)\b[^.?!]*\b(?:federal|state|local)\b[^.?!]*\bgovernment\b`,
			Exclude: guardGovernmentHistory},
		// Booz Allen: a family member employed by a government, a party, a royal
		// family or a candidate is the wider entry's; a family member at the
		// employer itself is the relatives entry's.
		{Priority: 568, Target: "family-public-employment",
			Pattern: ownFamily + `[^?]*\b(?:` + publicEmployers + `)\b`},
		{Priority: 569, Target: "party-royal-candidate-employment",
			Pattern: `\b(?:employ\w*|work(?:ed|ing)?\s+for)\b[^?]*\b(?:` + publicEmployers + `)\b|\b(?:` + publicEmployers + `)\b[^?]*\bemploy\w*`,
			Exclude: ownFamily},
		{Priority: 570, Target: "relatives-at-employer",
			Pattern: `\bmember\s+of\s+your\s+(?:immediate\s+)?(?:family|household)\b[^?]*\b(?:employee|employed|board\s+of\s+directors)\b`,
			Exclude: `\bgovern|\bclient|\bcontractor|\bpartners?\b|` + publicEmployers},
		{Priority: 407, Target: "government-position-family", Exclude: publicEmployers,
			Pattern: `\b(?:you or (?:a member of your )?immediate family|immediate family member)\b.*\bgovernment official\b` +
				// HP: "Whether a family member or anyone with whom you have a close personal relationship is a government official".
				`|\bfamily\s+member\b[^?]*\bis\s+an?\s+government\s+official\b` +
				`|\bimmediate\s+family\b[^?]*\b(?:employee|employed|employment|worked)\b[^?]*\bgovern(?:ment|mental)\b`},
		// Snowflake: "Have you ever been directly employed by (1) any
		// government or military entity, state-owned enterprise, or
		// publicly-funded institution, or (2) a government contractor ...?".
		// The owner's word: the bank's government employment answer is the
		// answer to such a list of public employers.
		{Priority: 611, Target: "government-employee-history",
			Pattern: `\b(?:employed|worked|employee)\b[^?]*\bgovernment\b[^?]*\b(?:state[-\s]owned\s+(?:enterprises?|compan(?:y|ies)|entit(?:y|ies))|publicly[-\s]funded\s+(?:institutions?|organi[sz]ations?|entit(?:y|ies)))\b`,
			Exclude: `\bsenior\s+government\s+official\b|\bimmediate\s+family\b|` + ownFamily},
		{Priority: 409, Target: "government-employee-history",
			Pattern: `\bgovernment (?:employee|employment|service|job)\b`,
			Exclude: guardGovernmentHistory},
		// Everlywell: "Have you ever worked for a State agency ...?"; an agency
		// with no public qualifier is a staffing or recruiting one.
		{Priority: 612, Target: "government-employee-history",
			Pattern: `\b(?:employed\s+(?:by|with|at)|work(?:ed|ing)?\s+(?:for|at|in))\s+(?:an?\s+|any\s+|the\s+)?(?:u\.?s\.?\s+)?(?:federal|state|local|county|municipal|government|public)\s+agenc(?:y|ies)\b`,
			Exclude: guardGovernmentHistory},
		{Priority: 410, Target: "government-official-disclosure",
			Pattern: `\b(?:hold|held)\b.*\bgovernment official\b`},
		{Priority: 411, Target: "government-official-disclosure",
			Pattern: `\b(?:referred|recommended)\b.*\bby (?:a )?government official\b`},
		{Priority: 412, Target: "government-official-disclosure",
			Pattern: `\b(?:related to|close personal relationship|personal relationship)\b.*\bgovernment official\b`},
		// Coinbase: "Are you a current government official or were you a
		// government official in the last five years (e.g., ...)?" asks it of
		// the applicant directly.
		{Priority: 614, Target: "government-official-disclosure",
			Pattern: `\b(?:are|were|have)\s+you\s+(?:ever\s+)?(?:been\s+)?(?:an?\s+)?(?:current\s+|former\s+)?government\s+official\b`},
		// HP: "please advise if you would be engaging in any of the above listed
		// situations" after a list of conflicts; the relatives routes leave it.
		{Priority: 566, Target: idConflictSituations, Pattern: `\bconflicts?\b`, AllOf: []string{rxbListedSituations}},
		{Priority: 413, Target: "outside-business-activities",
			Pattern: `\boutside business activit(?:y|ies)\b`},
		{Priority: 414, Target: "competitor-or-related-investment",
			Pattern: `\binvestments? in (?:a )?private compan(?:y|ies)\b`},
		{Priority: 415, Target: "competitor-or-related-investment",
			Pattern: `\binvest(?:ment|ed)?\b.*\b(?:current )?competitor\b`},
		{Priority: 416, Target: "ip-ownership-to-retain",
			Pattern: `\bintellectual property (?:ownership|rights)\b`},
		{Priority: 418, Target: "government-employee-history",
			Pattern: `\b(?:employed|employee|employment)\b.*\bby\b.*(?:\bfederal\b|\bstate\b|\blocal\b|\bu\.s\.|\bunited states\b).*\bgovernment\b`,
			Exclude: guardGovernmentHistory},
		{Priority: 419, Target: "ip-ownership-to-retain",
			Pattern: `\b(?:patents?|trademarks?|copyrights?)\b.*\b(?:retain|own|create|develop)\w*\b`},
		{Priority: 420, Target: "political-campaign",
			Pattern: `\b(?:paid|unpaid|volunteer)\s+(?:position\s+)?(?:on\s+)?a political campaign\b|\bposition on a political campaign\b`},
		{Priority: 421, Target: "nda-government-seta",
			Pattern: `\bnon[- ]?disclosure agreement\b.*\bu\.?s\.? government\b.*\b(?:seta|advisory|similar capacity)\b`},
		{Priority: 422, Target: "nda-government-seta",
			Pattern: `\bsigned (?:an? )?(?:nondisclosure|non-disclosure|nda)\b.*\bu\.?s\.? government\b`},
		{Priority: 423, Target: "nda-government-seta",
			Pattern: `\bnda\b.*\bgovernment\b.*\b(?:seta|advisory)\b`},
		{Priority: 424, Target: "external-auditor-family",
			Pattern: `\b(?:parent|spouse|child|sibling|domestic partner)\b.*\b(?:current )?employee\b.*\bserv(?:es|ing)\b.*\bas\b.*\bauditor\b`},
		{Priority: 425, Target: "external-auditor-family",
			Pattern: `\b` + faSeniorFamilyWho + `\b.*\bemployee\b.*\bauditor\b`},
	}
}
