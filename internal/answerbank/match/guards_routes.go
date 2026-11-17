package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Shared route guards. Each constant is an RE2 source, written once so a veto
// added to one route cannot be forgotten on its twin. Routes read text that
// routeText has already folded, so only the straight apostrophe appears.
const (
	// guardQuantifiedExperience vetoes the skill gate on "3+ years",
	// "at least 5 years", "3-5 years" and the like.
	guardQuantifiedExperience = `(?:\d+\s*\+|\d+\s*(?:or more|or greater|plus)|at least\s*\d+|minimum(?: of)?\s*\d+|min\.?\s*\d+|more than\s*\d+|over\s*\d+|\d+\s*(?:-|to)\s*\d+)\s*\+?\s*(?:full[- ]?time\s+)?years?\b`

	// guardSuperlativePickOne vetoes "Yes" for a "which one" question.
	guardSuperlativePickOne = `\b(?:which|what)\b[^?]*\b(?:most|greatest|deepest|strongest|primary|best)\b[^?]*\bexperience\b|\b(?:which|what)\b[^?]*\bare you (?:strongest|most proficient|most experienced)\b`

	// guardListChoicePrompt vetoes the Yes gate on "select all that apply" and
	// "which of the following" questions.
	guardListChoicePrompt = `\b(?:select|check|tick|choose|mark|pick)\s+(?:all|any|each)\s+(?:that|which)\s+apply\b|\bwhich\s+of\s+the\s+following\b`

	// guardPastTenseEmployment marks employer-history wording; the currently
	// employed route requires its absence.
	guardPastTenseEmployment = `\b(?:ever|previously|formerly|former|in the past|been employed|been working|have been|have ever|worked (?:at|for|with))\b`

	// guardStatusAsk asks the applicant's current visa or work-authorization
	// status by name (CodeRabbit: "If you require employer sponsorship ...,
	// please explain your current work authorization status"): a short
	// factual field, not an essay.
	guardStatusAsk = `\b(?:describe|specify|provide|explain)\b[^.?!]*\b(?:current\s+)?(?:visa|work\s+authori[sz]ation)\s+status\b`

	// guardSponsorshipDescribe is the "describe the sponsorship you require"
	// text slot, a short factual field rather than an essay.
	guardSponsorshipDescribe = `\b(?:describe|specify|explain|provide details?|elaborate|what (?:type|kind) of)\b[^.?!]*\bsponsorship\b|\b(?:type|kind) of sponsorship\b|` + guardStatusAsk +
		`|\bsponsorship\b[^.?!]*\b(?:describe|specify|list)\b[^.?!]*\b(?:type|kind) of (?:support|assistance)\b` +
		`|\b(?:type|kind) of (?:immigration|visa) (?:support|assistance)\b` +
		// Stand Insurance: "If you need sponsorship, please provide details."
		`|\bif\s+you\s+(?:will\s+)?(?:need|require)\s+(?:(?:visa|employer|employment)\s+)?sponsorship\s*,?\s*(?:please\s+)?(?:provide\s+(?:details?|more\s+information)|explain|describe|specify|elaborate)\b` +
		// Mapbox: "Please briefly describe your current situation. Example: "On
		// an open work permit ...", "Will require H-1B transfer"": the
		// situation its examples name is the sponsorship one.
		`|\b(?:describe|explain|tell\s+us\s+about)\s+your\s+(?:current\s+)?(?:immigration\s+|visa\s+|work\s+authori[sz]ation\s+)?situation\b[^?]*\b(?:visas?|work\s+permit|h-?1-?b|sponsor\w*)\b` +
		// Casca: "Do you have any current or future sponsorship needs? Please
		// specify, such as H1B transfer/new H1B/TN/etc.": the ask to specify
		// follows the question.
		`|\bsponsorship\s+needs?\b[^?]*\?\s*(?:please\s+)?(?:specify|describe|explain)\b`

	// guardVisaHeldAsk is a work-authorization question whose box also asks
	// for the visa the applicant holds (Close: "Can you legally work in the
	// US? Please explain if you have a visa."): a short factual field the
	// recorded status and dates answer, not an essay.
	guardVisaHeldAsk = `\b(?:legally|lawfully|authori[sz]ed\s+to|eligible\s+to|permitted\s+to)\s+work\b[^?]{0,60}\?\s*(?:please\s+)?(?:explain|describe|specify|tell\s+us|let\s+us\s+know)\b[^.?!]{0,20}\b(?:if|whether|what|which)\s+(?:you\s+(?:have|hold|are\s+on)\s+an?\s+|(?:type|kind)\s+of\s+)?visa\b`

	// guardNegatedSponsorship is the polarity trap "work without sponsorship".
	// AssemblyAI's "without current or future sponsorship" joins two tenses.
	guardNegatedSponsorship = `\bwithout\s+(?:needing\s+|requiring\s+|the need for\s+)?(?:(?:a|an|the)\s+)?(?:(?:company|employer|corporate|organizational|our|their|its|any|further|additional|future|current|current or future|immigration|employment|visa\s*(?:/|or)\s*work\s+permit|work\s+permit|work|authorization|visa|required|renewal or)(?:'s)?\s+)*(?:(?:form|type|kind|sort)s?\s+of\s+)?(?:visa\s+)?sponsor(?:ship|ing)?\b` +
		`|\b(?:do(?:es)?\s+not|don't|doesn't|will\s+not|won't|no need to)\s+(?:need|require)\b[^.?!]*\bsponsor(?:ship)?\b` +
		`|\bwork without\s+(?:a\s+)?(?:visa\s+)?sponsor` +
		// Eventual: "authorized to work lawfully in the US, without employer support".
		`|\bwithout\s+(?:any\s+)?(?:company|employer)(?:'s)?\s+support\b` +
		// NMI: "authorized to work in the United States, without being sponsored".
		`|\bwithout\s+being\s+sponsored\b` +
		`|\bno\s+(?:visa\s+)?sponsorship\s+(?:is\s+)?(?:needed|required|necessary)\b`

	// guardNonCompeteReason is the reason slot of a non-compete question.
	guardNonCompeteReason = `non[- ]?(?:compete|solicit\w*)\b.*\b(?:explain|reason|detail|describe)\w*|(?:if yes|please (?:explain|provide|describe|specify)).*non[- ]?(?:compete|solicit\w*)`

	// guardSponsorshipMention is the anti pattern of every plain work-authorization
	// route: a question that says it requires sponsorship or a visa is not
	// "are you authorized to work".
	guardSponsorshipMention = `\b(?:require|requiring|need|needing)\b.*\b(?:sponsor(?:ship|ed|ing)?|visa)\b`
	// guardEADCategory asks for the category on the applicant's Employment
	// Authorization Document (insitro), route 592's: a work-authorization
	// route that reads the same question first leaves it.
	guardEADCategory = `\b(?:ead|employment\s+authori[sz]ation\s+(?:document|card))\b.*\bcategor(?:y|ies)\b|\bcategor(?:y|ies)\b.*\b(?:ead|employment\s+authori[sz]ation\s+(?:document|card))\b`

	// guardProofNotice is a statement, not a question, that proof may be asked
	// for later (Sysdig: "Please note that you may be required to provide a
	// proof of your right to work in the later stages of the hiring process"):
	// a notice to acknowledge, not the authorization question.
	guardProofNotice = `^[^?]*\byou\s+(?:may|might|will|would)\s+be\s+(?:required|asked)\s+to\s+(?:provide|present|produce|show|submit|furnish)\b[^?]*$`

	// guardCapabilityAnti keeps the broad competence screener honest: counts,
	// credentials and legal eligibility are never an unconditional Yes.
	guardCapabilityAnti = `\d+\s*\+?\s*(?:years?|yrs?|months?)\b|\bhow (?:many|much|long)\b|\b(?:certificat\w*|certified|licen[sc]ed?|clearance|degrees?|diplomas?|bachelors?|masters?|phd)\b|\b(?:legal(?:ly)?|authori[sz]\w*|sponsor\w*|visa|eligib\w*|felony|convict\w*|criminal|restrictions?)\b`

	// guardAdverb sits on either side of "work" so a route cannot accept one
	// placement and miss the other.
	guardAdverb = `(?:(?:legally|lawfully)\s+)?`

	// guardCredentialPlace is the place a licence, registration, certification
	// or bar admission was granted in, or a document issued by: the authority's
	// state or country, which need not be the applicant's home (FINRA, a state
	// board of accountancy).
	guardCredentialPlace = `\blicen[cs](?:e|es|ed|ure|ing)\b|\bcertifi(?:ed|cations?|cates?)\b|\bregistrations?\b|\bregistered\s+(?:with|as|to\s+practi[cs]e)\b|\b(?:admitted|admission)\b|\bbar\b|\bissu(?:ed|es|ing|er|ance)\b|\baccredit\w*|\bfinra\b`

	// guardNotAddressCountry lists the country questions that are not the
	// address-country field.
	guardNotAddressCountry = guardCredentialPlace + `|country of citizenship|citizenship.{0,40}country|country (?:where|in which) you (?:intend|plan|expect|will)|\b(?:anticipat\w*|expect\w*|intend\w*|plan(?:ning)?(?:\s+on)?)\s+(?:to\s+)?work(?:ing)?\b|\b(?:authori[sz]ed|eligible|permitted|entitled)\b.{0,40}\bwork\b|right to (?:legally |lawfully )?work|visa status|sponsorship|require sponsorship|\bwork\s+authori[sz]ation\b|\bauthori[sz]ed\s+to\s+be\s+employed\b|citizen of (?:another|any other) country|country\s*(?:/\s*region\s*)?code|\b(?:phone|mobile|cell|telephone|dial(?:ing)?|calling)\b[^.?!]{0,30}\bcode\b|\bphone number\b.{0,20}\bcountry|\bcountry\b.{0,20}\bphone number`

	// guardSignatureField keeps a typed-signature route off the date, upload,
	// drawn-signature and acknowledgement wording around a signature.
	guardSignatureField = `\b(?:date|dated|day|month|year|upload(?:ed)?|attach(?:ed)?|file|image|photo|draw(?:n)?|scan(?:ned)?|consent|agree|acknowledge|certif\w*|policy|policies|terms)\b|\b(?:do you|are you|will you|have you|willing)\b`

	// guardGovernmentHistory vetoes the government-employment history route for
	// a first-person attestation. The pronoun may follow any letter because the
	// extension concatenates text blocks without a separator; only a digit or an
	// underscore before it disqualifies the match. A family member's is the
	// family entry's.
	// An organization that is the employer's client or prime contractor is the
	// question, not the government it names as an exception (Booz Allen).
	guardGovernmentHistory = `\bsenior\s+government\s+official\b|(?:^|[^0-9_])i\s+(?:hereby\s+)?(?:acknowledge|certify|attest|confirm|affirm)\b|\bimmediate\s+family\b` +
		`|\bclients?\b|\bprime\s+contractors?\b|` + ownFamily +
		// Snowflake: "any government or military entity, state-owned
		// enterprise, or publicly-funded institution" is wider than a
		// government employee's history.
		`|\bmilitary\b|\bstate[-\s]owned\b|\bpublicly[-\s]funded\b` +
		// "... and been involved in procurement" narrows it (Everlywell); the
		// narrowed-disclosure stage carries only a No over.
		`|\band\s+(?:also\s+)?been\b`

	// guardNarrativeExample vetoes quantity and link routes on a prompt that
	// asks for a narrative.
	guardNarrativeExample = `\b(?:give|provide|share|list)\b[^.?!]{0,30}\bexamples?\b|\billustrate\b|\bareas used\b|\bwalk (?:me|us) through\b|\b(?:describe|explain|elaborate on)\b[^.?!]{0,40}\bproficienc\w*`
)

var (
	countAskRE = lazyregexp.New(`(?i)\b(?:how many|how much|number of)\b`)
	linkWordRE = lazyregexp.New(`(?i)\b(?:url|link|links)\b`)
	// quantityPhraseRE is "a number of" / "a large number of" as a statement
	// ("many"), not the ask for a count.
	quantityPhraseRE = lazyregexp.New(`(?i)\ban?\s+(?:\w+\s+)?number\s+of\b`)
)

// vetoProfileURLCountAsk reports a "how many / number of" ask that names no
// link after its last count phrase: a numeric box must not receive a URL, while
// "how many profile links do you have" still routes.
var (
	workFromWhereRE   = lazyregexp.New(`(?i)` + rxbWorkFromWhere)
	workArrangementRE = lazyregexp.New(`(?i)\b(?:remote(?:ly)?|hybrid|on[- ]?site|in[- ](?:person|office)|office|outside)\b`)
)

// vetoWorkArrangement keeps a where-will-you-work question about remote,
// hybrid or office work, or about working outside a country, away from the
// place answer: it asks for an arrangement, not a city.
func vetoWorkArrangement(text string) bool {
	return workFromWhereRE.MatchString(text) && workArrangementRE.MatchString(text)
}

// examplesAskRE is an ask for several pieces of work, or for words about them,
// that a profile link alone does not answer: "Share 2-3 of your open-source
// contributions with links ... and a quick note on your role".
var examplesAskRE = lazyregexp.New(`(?i)\b(?:share|list|give|provide)\s+(?:\d+|two|three|a\s+few|some)\b[^.?!]*\b(?:contributions?|projects?|examples?|pull\s+requests?|samples?)\b|\b(?:describe|explain)\b|\ba\s+(?:quick\s+|short\s+|brief\s+)?(?:note|description|summary)\s+(?:on|of|about)\b`)

// vetoProfileURLAsk keeps a profile link off a count ask and off an ask for
// examples of work described in words.
func vetoProfileURLAsk(text string) bool {
	return vetoProfileURLCountAsk(text) || examplesAskRE.MatchString(text)
}

func vetoProfileURLCountAsk(text string) bool {
	// "a large number of fabricated CVs" in an aside is no ask for a count.
	text = quantityPhraseRE.ReplaceAllString(text, " ")
	locs := countAskRE.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return false
	}
	last := locs[len(locs)-1]
	return !linkWordRE.MatchString(text[last[1]:])
}

// altGroup wraps a pattern source so it can be spliced into a larger one.
func altGroup(parts ...string) string { return "(?:" + strings.Join(parts, "|") + ")" }
