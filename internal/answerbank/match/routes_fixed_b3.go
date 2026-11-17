package match

// Sponsorship wording shared by the require/need routes below.
const (
	// Clera: "do you have work authorization to work"; a needed authorization is the sponsorship question's.
	rxbWorkVerb = `\b(?:authori[sz]ed|eligible|permitted|(?:have|hold)\s+(?:(?:valid|legal|current)\s+)?(?:work\s+)?authori[sz]ation)\s+to\s+` + guardAdverb + `work\s+` + guardAdverb
	rxbAnyEmpOr = rxbAnyEmployer + `|` + guardSponsorshipMention
	// permanentAuthAsk asks whether the authorization to work is permanent.
	permanentAuthAsk = `\bpermanent(?:ly)?\s+(?:work\s+)?(?:authori[sz]ation|authori[sz]ed|right)\s+to\s+work\b|\b(?:eligible|authori[sz]ed|able)\s+to\s+work\s+permanently\b`
	// rxbApplyCountry is the country the applicant applies in, said either way
	// round and in either tense ("the country in which you are applying", "the
	// country you are applying to", "the country for which you applied"), or with
	// the preposition dropped at the end of the question (Workable), or as the
	// region (Neo4j: "the region you are applying for").
	rxbApplyCountry = `(?:country|region)\s+(?:(?:in|for|to)\s+which\s+(?:(?:job|role|position)\s+)?you(?:\s+|['\x{2019}]re\s+)(?:(?:are\s+)?applying|(?:have\s+)?applied)\b|(?:where\s+)?you(?:\s+|['\x{2019}]re\s+)(?:(?:are\s+)?applying|(?:have\s+)?applied)(?:\s+(?:to|in|for)\b|\s*(?:[?.!]|$)))`
	// rxbCountry is the country the role is in, the role named either way
	// ("where the position to which you are applying is located", Gem; "in which
	// the job you're applying for is located", HappyRobot; "where the job
	// office is located", Mindbody; "the country this role is based", Ketryx).
	rxbCountry = `(?:jurisdiction\s*/\s*country|country\s*/\s*jurisdiction|city\s*/\s*country|country\s*/\s*city|country(?:\s+or\s+jurisdiction)?|jurisdiction(?:\s+or\s+country)?)\s+(?:(?:where|(?:in|for)\s+which|that)\s+)?(?:the|this)\s+(?:job|role|position)(?:\s+office)?(?:\s+(?:to|for)\s+which\s+you(?:\s+are|['\x{2019}]re)\s+applying|\s+you(?:\s+are|['\x{2019}]re)\s+applying\s+(?:to|for))?\s+(?:is\s+(?:located|posted|based)|will\s+be\s+(?:located|posted|based|worked|performed))\b`
)

// rxbAuthorizationRoutes: citizenship screens, the plain work-authorization
// family and the permit/I-9/country-relative shapes. Every plain route is
// excluded when the text says it requires sponsorship or a visa, so the plain
// family never shadows the sponsorship routes.
func rxbAuthorizationRoutes() []Route {
	return []Route{
		{Priority: 12, Target: "pr-another-country-after-citizenship",
			Pattern: `\bpermanent\s+resident\b[^.?!]*\b(?:another|other)\s+(?:country|region)\b|\bsince\s+obtaining\b[^.?!]*\bcitizenship\b`},
		// Yields to the biographical battery above it.
		{Priority: 13, Target: "citizenship-in-another-country", Exclude: `\bsince\s+obtaining\b|\bafterwards\s+become\b`,
			Pattern: `\b(?:currently\s+)?(?:hold|have)\b[^?]{0,60}\bcitizenship\b[^?]{0,40}\b(?:another|any\s+other|a\s+different)\s+countr(?:y|ies)\b|\bcitizen\s+of\s+(?:another|any\s+other)\s+country\b`},

		{Priority: 19, Target: "work-authorization-expiry", Pattern: `\b(?:work|employment)\s+authori[sz]ation\b[^.?!]*\bexpir\w*`},
		{Priority: 20, Target: "work-authorization-detail", Exclude: `\bsponsor`,
			Pattern: `\bamount\s+of\s+remaining\s+work\s+authori[sz]ation\b|\btype\s+of\s+visa\b[^.?!]*\bremaining\b|\bremaining\s+work\s+authori[sz]ation\b|` + guardVisaHeldAsk +
				// Axios: "What is your eligibility to work permanently in the US?" asks
				// the authorization in words.
				`|\bwhat\s+is\s+your\s+(?:current\s+)?(?:eligibility|authori[sz]ation|status)\b[^?]{0,40}\bpermanent(?:ly)?\b`},

		// Negated polarity: the matcher inverts the positive sponsorship entry.
		// "Permanently authorized ... without visa sponsorship" (Dave) asks
		// the permanence, route 61's.
		{Priority: 60, Target: "sponsorship-now-or-future", Tag: "negated-sponsorship", Pattern: guardNegatedSponsorship, Exclude: permanentAuthAsk},
		{Priority: 61, Target: "permanent-work-authorization", Pattern: permanentAuthAsk},

		{Priority: 64, Target: "work-auth-us", Exclude: rxbAnyEmpOr + `|` + guardEADCategory,
			Pattern: `\bauthori[sz]ed\s+to\s+` + guardAdverb + `(?:work|be\s+employed)\s+` + guardAdverb + `(?:full[- ]?time\s+)?in\s+(?:the\s+)?` + rxbUS},
		{Priority: 65, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:visa|work\s+permit)\b[^?]{0,60}\b(?:approved|in\s+process|already\s+have|already\s+hold|valid)\b|\b(?:have|hold)\b[^?]{0,30}\b(?:visa|work\s+permit)\b[^?]{0,60}\bcountry\s+you\s+are\s+applying\b`},

		// TensorWave: "require authorization to work in the United States" asks
		// the need, route 110's.
		// An unrestricted authorization is route 92's (EnergyHub).
		{Priority: 67, Target: "work-auth-us", Exclude: guardSponsorshipMention + `|` + rxbTrainingBasis + `|\b(?:require|need)\s+(?:an?\s+|any\s+)?authori[sz]ation\s+to\s+work\b|\bunrestricted\s+authori[sz]ation\b`,
			Pattern: `\bauthori[sz]ation\s+to\s+work\s+in\s+(?:the\s+)?` + rxbUS},
		{Priority: 69, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:provide|furnish|present|show)\b[^?]{0,60}\b(?:proof|documentation|evidence)\b[^?]{0,80}\bauthori[sz]ed\s+to\s+work\b|\bauthori[sz]ed\s+to\s+work\b[^?]{0,80}\bwithin\s+(?:\d+|three)\s+(?:business\s+)?days?\s+of\s+hire\b`},
		// Chime: "eligible to work legally in the United States of America";
		// Cohere Health: "eligible and legally able to work in the United States";
		// Automox: "Are you legally able to work in the US?".
		{Priority: 70, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:eligible\s+(?:and\s+(?:(?:legally|lawfully)\s+)?able\s+)?to|(?:legally|lawfully)\s+able\s+to|able\s+to\s+(?:legally|lawfully))\s+work\s+` + guardAdverb + `in\s+(?:the\s+)?` + rxbUS},

		// A named country beyond the United States: Canada, MotherDuck's "Are
		// you currently eligible to work in the Netherlands?", "authorized to
		// work in Germany", US Mobile's "authorized to work lawfully in Canada
		// for US Mobile Inc". The posting-country guard binds it to that country.
		{Priority: 71, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:eligible|authori[sz]ed|permitted|entitled)\s+to\s+(?:(?:legally|lawfully)\s+)?work\s+` + guardAdverb + `in\s+`, Require: worksInNamedCountry},
		{Priority: 72, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\bright\s+to\s+work\s+in\s+(?:the\s+)?(?:uk|united\s+kingdom|britain|england)\b`},
		{Priority: 73, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\blegally\s+permitted\s+to\s+work\s+in\s+the\s+country\s+where\s+this\s+job\s+is\s+located\b`},
		{Priority: 74, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:(?:legally\s+)?(?:authori[sz]ed|eligible|entitled)\s+to\s+` + guardAdverb + `|(?:legally|lawfully)\s+able\s+to\s+|able\s+to\s+(?:legally|lawfully)\s+)work\s+` + guardAdverb + `in\s+(?:the\s+)?` + rxbApplyCountry},
		// HackerOne: "proof of authorization to work in the country ..."; a
		// required authorization is the need, not the authorization held.
		{Priority: 75, Target: "work-auth-us", Exclude: guardSponsorshipMention + `|\b(?:require|need)\s+(?:an?\s+|any\s+)?authori[sz]ation\s+to\s+work\b`,
			Pattern: `\b(?:eligibility|authori[sz]ation)\s+to\s+work\s+in\s+(?:the\s+)?` + rxbApplyCountry},
		// Location-relative: the posting's location, or the countries it lists,
		// stands for the country; Artificial Analysis (Ashby): "the country of
		// the office you selected", the office the form asked for above; Aptos
		// Labs: "the location you are applying for".
		{Priority: 76, Target: "work-auth-us", Exclude: rxbAnyEmpOr,
			Pattern: `\b(?:legally\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+(?:(?:for\s+)?(?:our|the|this)\s+(?:company|organi[sz]ation|employer)\s+)?(?:in|at)\s+(?:any\s+of\s+)?(?:the\s+)?(?:posted|selected|listed|advertised|indicated)\s+locations?\b|\b(?:authori[sz]ed|eligible|permitted)\s+to\s+work\b[^.?!]*\b(?:locations?|offices?)\s+(?:which\s+|that\s+|where\s+)?you(?:\s+(?:have\s+)?(?:selected|chose|chosen|indicated|applied)|(?:\s+are|['\x{2019}]re)\s+applying)\b` +
				`|\b(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+in\s+(?:the\s+)?countr(?:y|ies)\s+(?:listed|named|specified)\s+(?:in|on)\s+(?:the|this)\s+(?:job\s+)?(?:posting|description|listing)\b` +
				`|\b(?:legally\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+(?:in|at)\s+(?:the\s+)?(?:job|role|position)(?:['\x{2019}]?s)?\s+location\b` +
				// 4M (Ashby): "in your preferred location", the place the
				// applicant would work for this posting.
				`|\b(?:legally\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+(?:in|at)\s+your\s+preferred\s+(?:work\s+)?location\b` +
				// HP (Workday): "in the job posting country".
				`|\b(?:legally\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+in\s+(?:the\s+)?(?:job\s+)?posting(?:['\x{2019}]?s)?\s+country\b` +
				// Navan: "eligible to work in the country outlined for this position".
				`|\b(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+in\s+(?:the\s+)?country\s+(?:outlined|listed|specified|named|indicated|stated)\s+(?:for|in|on)\s+(?:this|the)\s+(?:job|role|position|posting)\b` +
				// Block: "authorized to work lawfully in the location posted for this position".
				`|\b(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+(?:legally|lawfully)?\s*(?:in|at)\s+(?:the\s+)?locations?\s+(?:posted|listed|advertised)\b`},
		{Priority: 77, Target: "work-auth-us", Exclude: rxbAnyEmpOr,
			// Datadog: "authorised to work full-time in the country where this job is based";
			// SoFi: "authorized to lawfully work in the country where this role is located";
			// Cribl: "authorized to reside and work in the country where this role is based".
			// Dust: "the appropriate work authorization for this role".
			// Perk (Ashby): "have legal authorisation to work in the country where this role is based".
			// Ketryx (Greenhouse): "have the work authorization to legally work in the country this role is based".
			Pattern: `\b(?:(?:legally|lawfully)\s+)?authori[sz]ed\s+to\s+(?:(?:legally|lawfully)\s+)?(?:(?:live|reside)\s+and\s+)?work\s+(?:full[- ]?time\s+)?in\s+(?:the\s+)?` + rxbCountry + `|\b(?:work|(?:have|hold)\s+(?:legal|lawful)|(?:have|hold)\s+(?:the\s+)?)\s*authori[sz]ation\s+(?:to\s+(?:(?:legally|lawfully)\s+)?work\s+)?in\s+(?:the\s+)?` + rxbCountry +
				`|\b(?:appropriate|necessary|required|valid|proper)\s+work\s+authori[sz]ation\s+for\s+(?:this|the)\s+(?:role|position|job)\b`},
		{Priority: 78, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\beligible\s+to\s+work\s+(?:full[- ]?time\s+)?in\s+(?:the\s+)?` + rxbCountry},

		// A notice about verifying the right to work in front of the citizen,
		// resident, asylee or refugee question is that question's preamble.
		// The unrestricted right is route 91's (Elastic).
		{Priority: 79, Target: "work-auth-us", Exclude: guardSponsorshipMention + `|\bunrestricted\b|\bare\s+you\s+(?:either\s+)?an?\s+(?:u\.?s\.?\s+|united\s+states\s+)?citizen\b[^?]*\blawful\s+permanent\s+resident\b[^?]*\b(?:asylum|asylee|refugee)\b`,
			Pattern: `\b(?:(?:legal|lawful|valid)\s+)?right\s+to\s+work\s+in\s+(?:the\s+)?` + rxbUS},
		{Priority: 80, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:(?:legal|lawful|valid)\s+)?right\s+to\s+work\s+in\s+(?:this|the)\s+country\b`},
		{Priority: 81, Target: "work-auth-us", Exclude: rxbAnyEmpOr,
			Pattern: rxbWorkVerb + `in\s+(?:(?:this|that)\s+country|(?:the\s+)?country(?:\s*/\s*region)?\s+of\s+the\s+(?:job|role|position))\b`},
		{Priority: 82, Target: "work-auth-us", Exclude: guardSponsorshipMention + `|` + guardProofNotice,
			Pattern: `\b(?:present|provide|produce|show|furnish|submit)\b.*\b(?:documents?|documentation|proof|verification)\b.*\bright\s+to\s+` + guardAdverb + `work\b`},
		{Priority: 83, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\bright\s+to\s+(?:legally|lawfully)\s+work\b.*\b` + rxbCountry},
		{Priority: 84, Target: "work-auth-us", Exclude: guardSponsorshipMention + `|` + guardProofNotice,
			Pattern: `\bproof\s+of\b.*\bright\s+to\s+work\b|\bproof\b[^.?!]*\b(?:legally|lawfully)\s+(?:able|eligible|permitted)\s+to\s+work\s+in\s+(?:the\s+)?` + rxbUS},
		{Priority: 85, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\bdocumentation\s+of\b.*\blegal\s+right\s+to\s+work\b.*` + rxbUS},
		{Priority: 86, Target: "work-auth-us", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:form\s+)?i-9\b.*\b(?:legal\s+right|right\s+to\s+work|work\s+authori[sz]ation)\b`},
		{Priority: 87, Target: "work-auth-us", Exclude: rxbAnyEmployer + `|` + guardSponsorshipMention,
			Pattern: `\b(?:presently|currently)\s+have\b.*\bauthori[sz]ation\b.*\bwork\b`},

		// Pika: "Do you have authorization to work for any employer in the US?"
		// Rain: "... for ANY employer without ANY restrictions?" is the
		// unrestricted entry's (route 92).
		{Priority: 88, Target: "work-auth-us-any-employer", Exclude: guardSponsorshipMention + `|\bwithout\s+(?:any\s+)?restrictions?\b`,
			Pattern: `\bauthori[sz]ed\s+to\s+work\s+in\s+the\s+(?:united\s+states|u\.?s\.?)\s+for\s+any\s+employer\b|` +
				`\b(?:authori[sz]ed|(?:have|hold)\s+(?:(?:valid|legal)\s+)?authori[sz]ation)\s+to\s+work\s+for\s+any\s+employer\s+in\s+the\s+(?:united\s+states|u\.?s\.?a?)\b`},
		{Priority: 89, Target: "work-auth-us-any-employer", Exclude: guardSponsorshipMention,
			Pattern: `\b(?:(?:valid\s+)?legal\s+)?authori[sz]ation\s+to\s+work\s+for\s+any\s+employer\b.*\bcountry\s+where\s+this\s+job\s+is\s+(?:posted|located)\b`},
		{Priority: 90, Target: "work-auth-us-any-employer", Exclude: guardSponsorshipMention,
			Pattern: `\bauthori[sz]ed\s+to\s+work\b.*\bany\s+employer\b.*\bcountry\s+where\s+this\s+job\s+is\s+(?:posted|located)\b`},

		// Both precede the immigration-status picker and the citizen/resident
		// screen so the export-control question never reaches either.
		// Mattermost: "eligible to access export-controlled information" is the
		// U.S. person status, the one that gives access without a license.
		{Priority: 93, Target: "us-person-itar-ear",
			Pattern: `\bu\.?s\.?\s+person\b[^?]{0,80}\b(?:itar|ear|export\s+controls?)\b|\b(?:itar|ear|export\s+controls?)\b[^?]{0,80}\bu\.?s\.?\s+person\b` +
				`|\b(?:eligible|authori[sz]ed|permitted)\s+to\s+(?:access|receive)\s+(?:export|itar)[- ]controlled\s+(?:information|data|technology|technical\s+data)\b`},
		{Priority: 100, Target: "cpt-opt-work-authorization",
			Pattern: `\b(?:on|under|hold|holding|have)\s+(?:an?\s+)?(?:student|temporary)\b[\s\S]{0,40}\bvisa\b[\s\S]{0,60}\b(?:opt|cpt)\b|\bauthori[sz]ation\b[^?]{0,80}` + rxbTrainingBasis},

		{Priority: 336, Target: "us-person-itar-ear", Pattern: `\bu\.?s\.?\s+persons?\b`},
		// The entry is Yes for a citizen or a permanent resident, so the route
		// takes only the ask that names both; citizenship alone is route 453's
		// and one other class alone is no entry's.
		{Priority: 337, Target: "us-citizen-lpr-asylee-refugee",
			Pattern: `\blawful\s+permanent\s+resident\b`, AllOf: []string{`\bcitizen`}},
		{Priority: 338, Target: "citizen-cuba-iran-north-korea-syria", Pattern: `\bcitizen\s+of\s+(?:cuba|iran|north\s+korea|syria)\b`},
		{Priority: 339, Target: "citizen-cuba-iran-north-korea-syria", Pattern: `\bnational\b[^?!]*\b(?:cuba|iran|north\s+korea|syria|crimea)\b|` +
			// Cirrus Logic: "Are you a foreign person of an “Embargoed Country,” ...?"
			`\b(?:person|national|citizen)\s+(?:of|from)\s+an?\s+\W*embargoed\s+countr`},
	}
}
