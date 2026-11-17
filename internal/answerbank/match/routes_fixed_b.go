package match

import "slices"

// Fragments shared by the routes of this family. Every name carries the rxb
// prefix so the other route families never collide with them.
const (
	// rxbUS ends a United States mention. A dotted "U.S." has no word boundary
	// after its last dot, so the character after it is consumed instead.
	rxbUS = `(?:united\s+states|u\.?s\.?a?)(?:[^a-z]|$)`

	// rxbCriminal is the literal criminal-history wording that sends a
	// regulatory-exclusion question away from the pharma entries.
	rxbCriminal = `\bcriminal\s+(?:arrests?|convictions?|offen[cs]es?|records?)\b`

	// rxbHybridAsk marks a stem whose real ask is hybrid or on-site attendance;
	// rxbHybridDays adds the days-a-week clause used by the first two
	// sponsorship routes.
	rxbHybridAsk  = `\bhybrid\s+(?:position|role|job|opportunity|schedule|model)\b|\b(?:working|come\s+in|report)\s+on[- ]?site\b|\bon[- ]?site\s+(?:at\s+least|requirement|presence|work)\b|\bin[- ]person\b|\bon[- ]?site\b`
	rxbHybridDays = rxbHybridAsk + `|\b\d+\s+days?\s+(?:a|per)\s+week\b.*\b(?:office|on[- ]?site)\b`

	// rxbUSPersonAsk opens by asking whether the applicant is a U.S. person: the
	// export-control entry's question, whatever its note says of sponsorship
	// (Okta: "Candidates who require visa sponsorship ... are not U.S. Persons").
	rxbUSPersonAsk = `^\W*are\s+you\s+an?\s+u\.?s\.?\s+persons?\b`

	// rxbAnyEmployer keeps the country-relative work-authorization routes off
	// the wording of the any-employer and the unrestricted entries.
	rxbAnyEmployer = `\bfor\s+any\s+employer\b|\bwithout\s+(?:any\s+)?restrictions?\b`

	// rxbTrainingBasis asks whether the authorization rests on practical
	// training, which is the practical-training entry's question.
	rxbTrainingBasis = `\b(?:involves?|based\s+on)\b[^?]{0,60}\b(?:cpt|opt|(?:curricular|optional)\s+practical\s+training)\b`
)

// fixedRoutesB holds the second half of the fixed-entry routes: disclosures,
// contact and links, work authorization, relocation, employment and education.
func fixedRoutesB() []Route {
	var all []Route
	for _, part := range [][]Route{
		rxbDisclosureRoutes(),
		rxbContactRoutes(),
		rxbAuthorizationRoutes(),
		rxbSponsorshipRoutes(),
		rxbLocationRoutes(),
		rxbEmploymentRoutes(),
		rxbEducationRoutes(),
	} {
		all = slices.Concat(all, part)
	}
	return all
}

// rxbDisclosureRoutes: federal procurement, pharma regulatory exclusion,
// recruiting consent and the small lifestyle screeners.
func rxbDisclosureRoutes() []Route {
	return []Route{
		// The GSA list of parties excluded from federal procurement is routes
		// 467 and 468's.
		{Priority: 454, Target: "federal-procurement-involvement", Pattern: `\bfederal\s+(?:agency\s+)?procurement\b`,
			Exclude: `\bexcluded\s+from\s+federal\b|\bgsa\b|\bgeneral\s+services\s+administration\b|\bepls\b`},
		{Priority: 455, Target: "federal-procurement-involvement", Pattern: `\bpending\b[^.?!]*\bfederal\s+(?:agency|government)\b[^.?!]*\bprocurement\b`},
		// The bridge allows periods because "U.S." sits inside the span.
		{Priority: 456, Target: "federal-procurement-involvement", Pattern: `\b(?:award|administration|administering|administer)\b[^?!]*\bcontracts?\b[^?!]*\b(?:department\s+of\s+defense|dod|federal\s+agency|u\.?s\.?\s+government|u\.?s\.?\s+military)\b`},
		{Priority: 457, Target: "financial-interests-conflict", Pattern: `\bmatters\s+(?:which|that)\s+may\s+(?:affect|impact)\b[^.?!]*\bfinancial\s+interests?\b`},
		{Priority: 458, Target: "financial-interests-conflict", Pattern: `\b(?:affect|impact)\s+(?:your\s+|the\s+)?financial\s+interests?\b`},

		{Priority: 459, Target: "fda-hhs-investigational-drug-restriction", Exclude: rxbCriminal,
			Pattern: `\b(?:fda|food\s+and\s+drug\s+administration|hhs|health\s+and\s+human\s+services)\b[^.?!]*\b(?:investigated|disqualified|restricted|debarred)\b|\b(?:investigated|disqualified|restricted|debarred)\b[^.?!]*\b(?:fda|food\s+and\s+drug\s+administration|hhs|health\s+and\s+human\s+services)\b`},
		{Priority: 460, Target: "fda-hhs-investigational-drug-restriction", Exclude: rxbCriminal, Pattern: `\b(?:investigational\s+(?:drug|product)|new\s+drug)s?\b`},
		{Priority: 461, Target: "generic-drug-enforcement-debarment", Exclude: rxbCriminal, Pattern: `\bgeneric\s+drug\s+enforcement\s+act\b`},
		{Priority: 462, Target: "generic-drug-enforcement-debarment", Exclude: rxbCriminal, Pattern: `\bdebar(?:red|ment)?\b[^?!]*(?:\bgeneric\s+drug\b|\b21\s+u\.?s\.?c\b|\b335a\b)`},
		{Priority: 463, Target: "generic-drug-enforcement-debarment", Exclude: rxbCriminal, Pattern: `\bdebarment\s+proceedings?\b`},
		{Priority: 464, Target: "generic-drug-enforcement-debarment", Exclude: rxbCriminal, Pattern: `\bdebar(?:red|ment)\b[^.?!]*\b(?:pending|threatened|proposed)\b`},

		{Priority: 465, Target: "federal-healthcare-exclusion", Exclude: rxbCriminal,
			Pattern: `\b(?:excluded|exclusion|debarred|suspended|ineligible)\b[^.?!]*\b(?:medicare|medicaid|federal\s+health\s*care\s+programs?)\b`},
		{Priority: 466, Target: "federal-healthcare-exclusion", Exclude: rxbCriminal, Pattern: `\blist\s+of\s+excluded\s+individuals\b|\bleie\b|\boig\s+exclusion\s+list\b`},
		{Priority: 467, Target: "gsa-federal-program-exclusion", Exclude: rxbCriminal, Pattern: `\bgeneral\s+services\s+administration\b|\bgsa\b[^.?!]*\b(?:list|exclu\w*)\b`},
		{Priority: 468, Target: "gsa-federal-program-exclusion", Exclude: rxbCriminal, Pattern: `\blist\s+of\s+parties\s+excluded\s+from\s+federal\b|\bexcluded\s+parties\s+list\b|\bepls\b`},
		{Priority: 469, Target: "healthcare-fraud-sanction", Exclude: rxbCriminal,
			Pattern: `\b(?:sanction(?:ed|s)?|investigated|convicted|charged)\b[^.?!]*\bhealth\s*care\s+(?:fraud|abuse)\b|\bhealth\s*care\s+fraud\s+(?:or|and)\s+abuse\b`},
		{Priority: 470, Target: "governmental-inquiry-pending", Exclude: rxbCriminal,
			Pattern: `\b(?:inquiry|investigation|proceeding)s?\b[^.?!]*\b(?:governmental\s+(?:entity|agency|body)|licensing\s+(?:association|board|authority|agency))\b`},
		{Priority: 471, Target: "governmental-inquiry-pending", Exclude: rxbCriminal, Pattern: `\badministrative\s+action\s+(?:has\s+)?been\s+imposed\s+upon\s+you\b`},
		{Priority: 472, Target: "governmental-inquiry-pending", Exclude: rxbCriminal, Pattern: `\bsubject\s+of\s+any\s+pending\s+inquiry\b`},

		{Priority: 473, Target: "us-licensed-physician", Pattern: `\blicensed\s+physician\b|\bphysician\s+licensed\b|\bare\s+you\s+an?\s+(?:u\.?s\.?\s+)?(?:medical\s+)?doctor\s+licensed\b`},
		{Priority: 474, Target: "disqualified-in-writing", Pattern: `\bdisqualified\s+in\s+writing\b`},
		{Priority: 475, Target: "disqualified-in-writing", Pattern: `\bbeen\s+disqualified\b[^.?!]*\bin\s+writing\b`},

		// Gopuff: "consent to texts from the recruiting team", the texts sent to the applicant.
		{Priority: 479, Target: "marketing-text-consent", Pattern: `\b(?:consent|permission|agree\w*|opt[- ]?in)\b[^.?!]*\b(?:text|whatsapp)\s+messages?\b|\b(?:consent|agree)\s+to\s+(?:receiv(?:e|ing)\s+)?texts\s+from\b`},
		{Priority: 480, Target: "marketing-text-consent", Pattern: `\bconsent\s+to\s+receiv(?:e|ing)\b[^.?!]*\b(?:text\s+messages?|sms)\b`},
		{Priority: 481, Target: "marketing-text-consent", Pattern: `\b(?:text\s+messages?|sms)\b[^.?!]*\b(?:recruiting|recruitment|application\s+process|hiring\s+process)\b`},
		{Priority: 482, Target: "marketing-text-consent", Pattern: `\bmay\s+we\s+text\s+you\b`},
		// Lucid Motors: "Would you like to receive communications via SMS and/or
		// WhatsApp to the number provided on your application?".
		{Priority: 483, Target: "marketing-text-consent", Pattern: `\b(?:authori[sz]e|consent|agree|permission|opt[- ]?in|would\s+you\s+like\s+to\s+receive)\b[^.?!]*\b(?:communicat\w*|contact\w*|reach\w*)\b[^.?!]*\bvia\s+(?:text|sms|whatsapp)\b`},
		{Priority: 484, Target: "marketing-text-consent", Pattern: `\bcommunicate\s+with\s+me\s+via\s+text\b`},

		{Priority: 485, Target: "tobacco-user", Pattern: `\b(?:tobacco\s+(?:user|product)s?|use\s+tobacco|cigarettes?|cigars?|vaping|smokeless\s+tobacco|snuff|chewing\s+tobacco)\b`},
		{Priority: 486, Target: "healthcare-professional-license",
			Pattern: `\b(?:physician|dentist|podiatrist|optometrist|chiropractor|nurse\s+practitioner|nurse\s+specialist|nurse\s+midwife|certified\s+registered\s+nurse)s?\b[^.?!]*\b(?:licens\w*|practic\w*)`},

		{Priority: 518, Target: "dog-friendly-office-ok",
			Pattern: `\bdog[- ]friendly\b|\b(?:comfortable|ok|okay)\b[^.?!]*\bdogs?\b[^.?!]*\b(?:present|office|environment|around)\b|\bdogs?\s+(?:are\s+)?present\b[^.?!]*\b(?:office|environment)\b`},

		// Avride (Greenhouse, live): "Please share your hourly salary
		// requirements for this role." An ask that also names the annual
		// figure is the salary's.
		{Priority: 537, Target: "hourly-pay-rate", Exclude: annualPayRE, Pattern: `\b(?:hourly|per\s+hour)\b[^.?!]*\b(?:pay|rate|wage|salary|comp(?:ensation)?)\b`},
		{Priority: 538, Target: "hourly-pay-rate", Exclude: annualPayRE, Pattern: `\b(?:pay|rate|wage|salary|comp(?:ensation)?)\b[^.?!]*\b(?:hourly|per\s+hour)\b`},
		{Priority: 539, Target: "hourly-pay-rate", Pattern: `\b(?:desired|expected|target|preferred)\s+hourly\b`},

		// Khan Academy: "This role is a W-2 position employed through a payroll
		// agency, rather than a 1099 independent contractor arrangement. Are
		// you comfortable with this employment arrangement?"; a C2C choice is
		// route 541's or the c2c entry's.
		{Priority: 540, Target: "w2-only-acceptable", Exclude: `\bc-?2-?c\b|\bcorp(?:oration)?[- ]?to[- ]?corp`,
			Pattern: `\bw-?2\s+only\b|\bw-?2\b[^?]{0,200}\b(?:comfortable|ok|okay|fine|willing|open|agree)\s+(?:with|to)\s+(?:this|that|the)\b`},
		{Priority: 541, Target: "w2-only-acceptable",
			Pattern: `\bw-?2\b[^.?!]*\b(?:only|exclusively|no\s+1099|no\s+c-?2-?c|no\s+corp(?:oration)?[- ]?to[- ]?corp(?:oration)?)\b`},
		{Priority: 542, Target: "c2c-preference", Exclude: rxbNoC2C, Pattern: `\bc-?2-?c\b`},
		{Priority: 543, Target: "c2c-preference", Exclude: rxbNoC2C, Pattern: `\bcorp(?:oration)?[- ]?to[- ]?corp(?:oration)?\b`},
	}
}

// rxbNoC2C lets a W2-only question that merely disclaims 1099 or C2C stay with
// the W2 entry.
const rxbNoC2C = `\bw-?2\s+only\b|\bno\s+(?:1099|c-?2-?c|corp(?:oration)?[- ]?to[- ]?corp(?:oration)?)\b`
