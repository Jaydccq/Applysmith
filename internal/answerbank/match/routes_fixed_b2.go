package match

// rxbWorkFromWhere is a question about the place the applicant will work
// from: "from where do you intend to work", "where will you be working from",
// Temporal's "What location do you intend to work from?".
const rxbWorkFromWhere = `\b(?:where|what\s+(?:location|city))\s+(?:do|will|would)\s+you\s+(?:(?:intend|plan|expect)\s+to\s+(?:work|be\s+(?:working|based))|be\s+(?:working|based))(?:\s+from)?\b`

// agreementSigner names the undersigned party of an agreement the form asks
// the applicant to sign ("Confidentiality Agreement Designee").
const agreementSigner = `\bagreement\s+(?:designee|signatory|signer)`

// typedNameAsk opens a label, or a sentence of it, by asking for the name to be
// typed: "By typing your name below, I hereby certify ...", "... By entering
// your name, you confirm that you have read the above statement:", GoFundMe's
// "By typing my name below, I confirm that the facts ... are true". What the
// label certifies is what the typed name signs.
// signByTypingAsk opens a sentence asking for the signature to be typed as
// the name, its parts named or not (Block: "Please sign by typing your Full
// Legal First, Middle Initial, and Last Name."; Energy Solutions: "Please sign
// the application by typing your full name into the text box below.").
const signByTypingAsk = `(?:^|[.!?]\s+)\W*(?:please\s+)?sign\s+(?:below\s+|here\s+|(?:the|this)\s+(?:application|form|document|statement|attestation)\s+)?by\s+typing\s+(?:in\s+)?your\s+(?:(?:full|legal)\s+)*(?:first\b[^.?!]{0,40}?\b(?:last|family)\s+)?name\b`

const typedNameAsk = `(?:^|[.!?]\s+)\W*by\s+(?:typing|entering)\s+(?:in\s+)?(?:your|my)\s+(?:(?:full|legal)\s+)*name(?:\s+(?:below|here|above))?\s*,`

// rxbContactRoutes: typed signatures, address parts, profile links and the
// competence screener that runs after every specific concept.
func rxbContactRoutes() []Route {
	return []Route{
		// Typed signature routes fill the applicant's own full name. The shared
		// guard keeps the date, upload, drawn and acknowledgement siblings out.
		{Priority: 491, Target: "applicant-full-name", Exclude: guardSignatureField,
			Pattern: `\b(?:type|typed|typing|enter|entered|entering|provide|input)\b[^?]{0,60}\b(?:initials|signature)\b`},
		{Priority: 492, Target: "applicant-full-name", Exclude: guardSignatureField,
			Pattern: `^\W*(?:(?:typed|electronic|e-?|digital|wet|applicant's|candidate's|employee's|your)\s*)?(?:signature|initials)\b`},
		// An agreement's designee or signatory is its undersigned party, the applicant.
		{Priority: 493, Target: "applicant-full-name", Exclude: guardSignatureField,
			Pattern: `\bsign\s+(?:your\s+)?(?:(?:full|legal)\s+)?name\b|^\W*(?:(?:full|legal)\s+)?name[\s(]*(?:(?:typed|electronic|e-)\s*)?signature\b|` + agreementSigner + `\s+(?:(?:full|legal)\s+)*name\W*$`},
		// Start-anchored: some picklist labels contain "... is your legal name".
		// A bare "Full Legal Name" label (xAI) is the whole field; one that goes
		// on ("... in Native Language") asks for another name.
		// A typed-name ask is the box the name is typed into. How the name is
		// pronounced is answered with the name as written, the owner's default
		// (Runway: "what is the phonetic spelling of your name?").
		// Rula: "First and last name (legal name)", the whole name as its two parts.
		{Priority: 500, Target: "applicant-full-name", Pattern: `^\W*your\s+(?:(?:full|legal)\s+)*name\b|^\W*(?:(?:full|legal)\s+)+name\W*$|` +
			`^\W*first\s+(?:and|&)\s+last\s+names?(?:\s*\(\s*(?:(?:full|legal)\s+)*name\s*\))?\W*$|` +
			// Figure: "Please provide your legal first and last name."
			`^\W*please\s+(?:provide|enter|state|write)\s+your\s+(?:(?:full|legal)\s+)*(?:first\s+(?:and|&)\s+last\s+)?names?\W*$|` + typedNameAsk + `|` + signByTypingAsk +
			`|\b(?:phonetic(?:al)?\s+spelling|pronunciation)\s+of\s+your\s+(?:(?:full|first)\s+)?name\b|\bhow\s+(?:do\s+you|to)\s+pronounce\s+your\s+(?:(?:full|first)\s+)?name\b|^\W*name\s+pronunciation\b`},

		{Priority: 503, Target: "address-street",
			Pattern: `\bstreet\s+address\b|\baddress\s+line\s+1\b|\baddress\s+1\b|\bhome\s+address\b|\bmailing\s+address\b`},
		{Priority: 504, Target: "address-street", Exclude: `\b(?:food|art|smart|cred|wise|light|car|fighter)\b`, Pattern: `^\W*street(?:\s+name)?\b`},
		// A label, not a sentence: a prose question that merely mentions a unit or a
		// suite (unit tests, a suite of tools) is not the address field.
		{Priority: 505, Target: "address-line2", Pattern: `\baddress\s+line\s+2\b|\baddress\s+2\b|\bapt\b|\bapartment\b|\bsuite\b|\bunit\b(?:\s+(?:number|#))?`,
			Exclude: `\b(?:unit|suite)\s+(?:test\w*|of\b)|\b(?:test|testing)\s+(?:unit|suite)\b`, Require: othMaxRunes(40)},

		{Priority: 506, Target: "currently-located-outside-us",
			Pattern: `\b(?:currently\s+)?(?:located|residing|reside|based|living|live)\b[^.?!]*\boutside\s+(?:of\s+)?(?:the\s+)?` + rxbUS},

		// A label that opens with "state" is the State field even when a City
		// label follows it later in the same text.
		// Character: "our New York or Redwood City, CA offices" names a place
		// whose name holds the word; a label whose every "city" is such a name
		// asks for no city.
		{Priority: 508, Target: "address-city", Veto: onlyCityNames,
			Pattern: `\bcity\b`,
			Exclude: `\bcity\s+(?:and|&)\s+(?:state|country)\b|\bcity\s*,\s*(?:state|country)\b|\bin[- ]person\b|\b\d+\s+days?\s+(?:a|per)\s+week\b|\bon[- ]?site\b|^\W*state\b|\bcountry\s*/\s*region\b|\blocation\s+preference\b|\blocated\b|\bwhere\s+are\s+you\s+located\b`},
		// The whole-label match keeps "Preferred location" and narrative
		// location prompts out; "Current location" is the applicant's own, also
		// with a note ("We use this ...", Ashby's "Let us know your place of
		// residence ...").
		// A note that asks about relocating or an office is not the place; one
		// that offers relocation assistance (Apex) is. Lime: "Location of Residence";
		// Infinitus: "Primary Location" on a location typeahead.
		// Hopper: "Current Location (Despite our remote first culture, some
		// teams hire only in specific timezones!)": a bracketed note that
		// explains asks nothing, so the excluded words count outside brackets.
		{Priority: 509, Target: "address-city", Pattern: `^\s*(?:current\s+|primary\s+)?location(?:\s*\(\s*city\s*\)|\s+of\s+residence)?\s*(?:\(\s*[^()?/]{12,}\)\s*)?(?:$|(?:we|this|let\s+us\s+know|please)\s[^?]*$)`,
			Exclude: `^[^(]*\b(?:prefer\w*|relocat(?:e|ed|ing)|remote|hybrid|office)\b|\)[^(]*\b(?:prefer\w*|relocat(?:e|ed|ing)|remote|hybrid|office)\b`},
		// "From where do you intend to work?" asks for a place, never for the
		// country alone; so does LiveKit's "Where is your specific working
		// location?" on Ashby's own location field, incident.io's "What is
		// your location?" and Aurora Solar's "Please note your working
		// location:" there.
		{Priority: 510, Target: "address-city", Pattern: `\bwhere\s+do\s+you\s+(?:currently\s+)?(?:reside|live)\b|\bwhat\s+is\s+your\s+(?:current\s+)?place\s+of\s+residence\b|` + rxbWorkFromWhere +
			`|\bwhere\s+is\s+your\s+(?:specific\s+|current\s+|exact\s+)?work(?:ing)?\s+location\b|^\W*what\s+is\s+your\s+(?:current\s+)?location\W*$` +
			`|^\W*(?:please\s+)?(?:note|enter|share|provide)\s+your\s+(?:specific\s+|current\s+|exact\s+)?work(?:ing)?\s+location\W*$` +
			// SurveyMonkey: "In which city are you located?" asks for the city
			// itself; route 508 leaves "located" alone for the yes/no forms.
			`|^\W*(?:in\s+)?(?:which|what)\s+city\s+(?:are|do)\s+you\s+(?:currently\s+|presently\s+)?(?:located|based|live|living|reside|residing)\b`, Veto: vetoWorkArrangement},

		{Priority: 511, Target: "intend-to-work-in-posted-state",
			Pattern: `\bintend\s+to\s+work\b[^.?!]*\bstate\b|\bwork\s+in\s+the\s+state\s+where\s+this\s+(?:job|position|role)\s+(?:was\s+|is\s+)?posted\b`},

		// The +4 add-on is a separate field that needs a lookup.
		{Priority: 514, Target: "address-zip", Exclude: `\+\s*4\b|\bplus\s+4\b`, Pattern: `\b(?:zip(?:\s*code)?|postal\s+code|post\s+code|postcode)\b|^\W*postal\W*$`},
		{Priority: 516, Target: "address-county", Pattern: `\bcounty\b`},
		// The extension and the dialling code beside a number are not the number.
		// Outtake: a bare "Contact Number" is the phone.
		{Priority: 523, Target: "phone-mobile", Pattern: `\b(?:phone|mobile|cell|telephone)\b|^\W*(?:(?:best|primary|preferred)\s+)?contact\s+(?:number|no\.?)\W*$`,
			Exclude: `\bext(?:ension)?\b|\bcountry\s+phone\s+code\b|\bphone\s+(?:country\s+)?code\b|\bdial(?:ling)?\s+code\b|` +
				// Ramp: mobile engineering is a field of work.
				`\bmobile\s+(?:engineer\w*|develop\w*|apps?|application\w*|platform\w*|software)\b`},

		// A count ask with no link word is a numeric box, and an ask for described
		// examples of work is an answer in words: neither is the profile URL.
		{Priority: 524, Target: "linkedin-url", Pattern: `\blinkedin\b`, Veto: vetoProfileURLAsk},
		// "Kaggle / Stack Overflow / other eng profile" asks for an engineering
		// profile, which the code-hosting one is (Sumble).
		{Priority: 525, Target: "github-url", Pattern: `\bgithub\b|\bother\s+(?:eng(?:ineering)?|developer|coding|technical)\s+profiles?\b`, Veto: vetoProfileURLAsk},
		{Priority: 526, Target: "portfolio-url", Pattern: `\bport?folio\b|\bpersonal\s+(?:website|site|page)\b`, Exclude: `\bport?folio\s+compan(?:y|ies)\b`, Veto: vetoProfileURLAsk},
		{Priority: 527, Target: "portfolio-url", Pattern: `\bwebsite\s+(?:url|link)\b`},

		// The bare "Website" label is left to the blank entry on purpose.
		{Priority: 528, Target: "other-website-url", Pattern: `^\s*website\s*\*?\s*$`},
		{Priority: 529, Target: "twitter-url", Pattern: `\btwitter\b`},
		{Priority: 530, Target: "twitter-url", Pattern: `\bx\s*\.\s*com\b`},
		// The bare letter x is never enough.
		{Priority: 531, Target: "twitter-url", Pattern: `\bx\s+(?:profile|url|link|handle|account)\b`},
		{Priority: 532, Target: "other-website-url", Pattern: `\bother\s+(?:website|site|url)\b`},

		// Rain: "Exclude internships or personal projects" in a count of years.
		{Priority: 536, Target: "portfolio-url", Exclude: guardNarrativeExample + `|\bexclud\w*\b[^.?!]*\bpersonal\s+projects?\b`, Pattern: `\bpersonal\s+projects?\b`},

		// Last on purpose: a broad competence screener, never an unconditional
		// claim when a count, credential or legal word is present.
		{Priority: 551, Target: "capability-self-assessment", Exclude: guardCapabilityAnti,
			Pattern: `\bdo\s+you\s+have\s+(?:(?:the|a|an)\s+)?[\w,'\- ]{0,80}?\b(?:understanding|knowledge|ability|abilities|skills?|aptitude|capability|capabilities)\b`},
		{Priority: 552, Target: "capability-self-assessment", Exclude: guardCapabilityAnti, Pattern: `\bare\s+you\s+(?:able|comfortable)\s+to\s+work\b`},
		{Priority: 553, Target: "capability-self-assessment", Exclude: guardCapabilityAnti + `|\bovertime\b`,
			Pattern: `\bare\s+you\s+(?:ready(?:\s+for)?|excited)\b[^?]{0,60}\bto\s+work\b[^?]{0,80}\b(?:environment|hours?|pace|schedule|shifts?|weekends?|nights?)\b`},
	}
}
