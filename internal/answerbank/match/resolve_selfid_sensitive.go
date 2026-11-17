package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// The three definitions of the sensitive-question requirement. No other list of
// sensitive or unsafe questions applies: a sensitive question names a protected
// characteristic; a disqualifying-disclosure question states a fact about the
// applicant that could disqualify them; an unsafe-for-composition shape is a
// slot no generative lane may fill.

var sensitiveCategoryRE = lazyregexp.New(`(?i)\b(?:` + strings.Join([]string{
	`rac(?:e|ial)`, `ethnic(?:ity)?`, `national(?:ity)?\s+origin`, `gender(?:\s+identity)?`, `sex`, `sexual\s+orientation`,
	`transgender`, `disab(?:led|ility|ilities)?`, `neuro\s*(?:diverse|divergent|diversity|divergence)`, `veteran`,
	`military\s+status`, `religio(?:n|us)`, `your\s+age`, `age\s+(?:range|bracket|group)`, `date\s+of\s+birth`, `dob`,
	`birth\s*date`, `citizenship(?:\s+status)?`, `immigration\s+status`, `pregnan(?:t|cy)`, `family\s+status`,
	`marital\s+status`, `caregiver`, `refugee`,
}, "|") + `)\b`)

// skillsFramingRE vetoes a sensitive word that sits inside a skills or
// experience question ("experience building accessibility features for users
// with disabilities").
var skillsFramingRE = lazyregexp.New(`(?i)\b(?:experience|building|develop(?:ing|ed)?|design(?:ing|ed)?|worked?\s+(?:on|with)|familiar(?:ity)?\s+with|knowledge\s+of|background\s+in|for\s+users?\s+with|features?\s+for|projects?\s+(?:involving|related\s+to)|passion\s+for|advocacy\s+for)\b`)

// nonprofitExemptionRE vetoes "religious" inside a charitable, civic or
// fraternal exemption of an outside-activities question.
var nonprofitExemptionRE = lazyregexp.New(`(?i)\b(?:charitable|civic|fraternal)\b[^.?]{0,60}\breligio(?:n|us)\b|\breligio(?:n|us)\b[^.?]{0,60}\b(?:charitable|civic|fraternal)\b`)

// isSensitiveCategoryQuestion reports a text naming a protected characteristic
// that does not frame a skills question or a nonprofit exemption.
func isSensitiveCategoryQuestion(text string) bool {
	t := collapseSpaces(text)
	if t == "" || !sensitiveCategoryRE.MatchString(t) {
		return false
	}
	return !skillsFramingRE.MatchString(t) && !nonprofitExemptionRE.MatchString(t)
}

var disqualifyingFactRE = lazyregexp.New(`(?i)\b(?:` + strings.Join([]string{
	`convict(?:ed|ion|ions)?`, `felon(?:y|ies)`, `misdemean(?:o|ou)r`, `criminal\s+(?:charge|record|history|offen[cs]e|conviction)`,
	`pleade?d?\s+(?:guilty|no\s+contest)`, `no\s+contest`, `guilty`, `terminated\s+for\s+cause`, `discharged`,
	`(?:asked|requested|forced)\s+to\s+resign`, `disciplinary\s+action`, `sponsorship`, `visa`, `work\s+authoriz(?:ation|ed)`,
	`authorized\s+to\s+work`, `immigration\s+sponsorship`,
}, "|") + `)`)

// certifiedAnswers is a certification that the answers on named topics are
// true; it states none of them (Valkyrie Trading: "I certify that all
// responses in this application—including my statements regarding current
// legal work authorization and future visa sponsorship requirements—are
// accurate and truthful.").
const certifiedAnswers = `\b(?:my|the|all(?:\s+of)?(?:\s+(?:my|the))?)\s+(?:responses?|answers?|statements?|information)\b[^.?!]{0,60}?\bincluding\s+(?:my\s+|the\s+|those\s+)?(?:(?:responses?|answers?|statements?)\s+)?(?:regarding|about|concerning|on|relating\s+to|related\s+to|as\s+to)\b[^.?!]{0,200}?\b(?:is|are)\s+(?:true|accurate|truthful|correct|complete)\b`

var (
	// A consent to a check names the check, not a fact about the applicant.
	checkConsentRE     = lazyregexp.New(`(?i)\b(?:authori[sz]e[sd]?|consent(?:s|ed)?\s+to|permit(?:s)?|permission\s+(?:for|to))\b[^.?!]{0,100}?\b(?:checks?|investigations?|screenings?|verifications?|reports?)\b(?:\s*(?:,|and)\s*[^.?!]{0,60}?\b(?:checks?|investigations?|screenings?|verifications?|reports?)\b)*(?:\s*,?\s*(?:including|such\s+as|which\s+(?:may|will)\s+include|to\s+include)\b[^.?!]*)?`)
	certifiedAnswersRE = lazyregexp.New(`(?i)` + certifiedAnswers)
	// A warning about consequences states no fact about the applicant either.
	consequenceRE = lazyregexp.New(`(?i)\b(?:(?:may|might|will|could|can|shall|would)\s+(?:result\s+in|lead\s+to|be\s+grounds\s+for|cause)|grounds\s+for|subject\s+to\s+(?:discipline|disciplinary|termination|discharge|dismissal))\b[^.?!]*`)
)

// isDisqualifyingFactDisclosure reports a text that asks about, or states, the
// applicant's own criminal history, termination for cause, discharge, a request
// to resign, disciplinary action, sponsorship need, visa or work authorization.
// There is NO skills-framing veto: a false positive only leaves a field blank,
// a false negative asserts a disqualifying fact for the user.
func isDisqualifyingFactDisclosure(text string) bool {
	t := collapseSpaces(text)
	if t == "" {
		return false
	}
	t = certifiedAnswersRE.ReplaceAllString(t, " ")
	t = checkConsentRE.ReplaceAllString(t, " ")
	t = consequenceRE.ReplaceAllString(t, " ")
	return disqualifyingFactRE.MatchString(t)
}

var (
	criminalNounRE    = lazyregexp.New(`(?i)\b(?:convict(?:ed|ion|ions)?|felony|felonies|misdemean(?:o|ou)r|offen[cs]e|criminal|plea|guilty|no\s+contest)\b`)
	explainVerbRE     = lazyregexp.New(`(?i)\b(?:explain|describe|identify|details?|circumstances|nature\s+of)\b`)
	conditionalSlotRE = lazyregexp.New(`(?i)^\s*if\s+(?:yes|no|so|applicable|you\s+answered\s+(?:yes|no))\b.{0,100}\b(?:please\s+)?(?:indicate|specify|provide|describe|identify|explain|state|list|enter|type|give)\b`)
	otherSpecifyRE    = lazyregexp.New(`(?i)^\s*if\s+other\b.{0,60}\b(?:specify|describe|explain|indicate)\b`)
	emergencyRE       = lazyregexp.New(`(?i)\bemergency\s+contact\b`)
	refereeWordRE     = lazyregexp.New(`(?i)\b(?:references?|referees?)\b`)
	refDetailRE       = lazyregexp.New(`(?i)\b(?:references?|referees?)\b[^?]{0,80}\b(?:names?|phone|telephone|e-?mail|contact\s+(?:info|information|details))\b|\b(?:names?|phone|telephone|e-?mail|contact\s+(?:info|information|details))\b[^?]{0,80}\b(?:references?|referees?)\b`)
	leadingAuxRE      = lazyregexp.New(`(?i)^\s*(?:are|is|do|does|did|can|could|would|will|may|have|has|shall)\b`)
	imperativeRefRE   = lazyregexp.New(`(?i)\b(?:provide|list|give|supply|include|enter|furnish|add)\b[^.?]{0,60}\b(?:references?|referees?)\b`)
	// consentToContactRefsRE is a sentence of consent to be contacted that
	// names the references one may give among those the employer may reach
	// (KnowBe4: "I hereby provide consent to KnowBe4 to communicate directly
	// with me, and any employment references I provide to KnowBe4, via phone
	// call, text message, email ..."): it asks for no one's details.
	consentToContactRefsRE = lazyregexp.New(`(?i)[^.?!]*\b(?:consent|authori[sz]e|permit|agree)\b[^.?!]*\b(?:contact|communicat|reach)\w*\b[^.?!]*\b(?:references?|referees?)\s+(?:i|you)\s+(?:may\s+)?provide\b[^.?!]*[.?!]?`)
)

// isCriminalHistoryExplanation reports the explanation slot of a criminal-history
// question; a plain Yes/No "Have you been convicted?" is not caught, since a
// curated entry answers it.
func isCriminalHistoryExplanation(text string) bool {
	t := collapseSpaces(text)
	return t != "" && criminalNounRE.MatchString(t) && explainVerbRE.MatchString(t)
}

// isConditionalDetailSlot reports the detail slot of a conditional follow-up: it
// wants a short factual value or N/A, never a paragraph.
func isConditionalDetailSlot(text string) bool {
	t := collapseSpaces(text)
	return conditionalSlotRE.MatchString(t) || otherSpecifyRE.MatchString(t)
}

// isThirdPartyContactRequest reports a request for other people's names or
// contact details: an emergency contact, or references asked for as details or
// by an imperative. A yes/no question about references keeps its curated route.
func isThirdPartyContactRequest(text string) bool {
	t := collapseSpaces(text)
	if t == "" {
		return false
	}
	if emergencyRE.MatchString(t) {
		return true
	}
	t = consentToContactRefsRE.ReplaceAllString(t, " ")
	if !refereeWordRE.MatchString(t) {
		return false
	}
	if refDetailRE.MatchString(t) {
		return true
	}
	if leadingAuxRE.MatchString(t) {
		return false
	}
	return imperativeRefRE.MatchString(t)
}

// isLeaveBlankInstruction reports a form that tells a person to leave the
// control empty, with no conditional tail ("Middle name (leave blank if none)"
// is a real optional field).
func isLeaveBlankInstruction(text string) bool {
	t := routeText(text)
	for _, re := range honeypotREs {
		for _, loc := range re.FindAllStringIndex(t, -1) {
			if !honeypotCondition.MatchString(t[loc[1]:]) {
				return true
			}
		}
	}
	return false
}

// isGenerativelyUnsafeQuestion reports a slot no generative lane may fill.
func isGenerativelyUnsafeQuestion(text string) bool {
	return isCriminalHistoryExplanation(text) || isConditionalDetailSlot(text) ||
		isThirdPartyContactRequest(text) || isLeaveBlankInstruction(text)
}

// documentRequirementRE is a statement that the employer will ask for
// documents proving something ("ASM will require me to present an original
// document ... confirming my citizenship / residency status"): agreeing to
// show proof later states no fact about the applicant.
var documentRequirementRE = lazyregexp.New(`(?i)\b(?:requires?|required|asks?|asked)\s+(?:(?:me|you|applicants?|candidates?|the\s+applicant)\s+)?to\s+(?:present|provide|produce|submit|show)\b[^.?!]*?\b(?:documents?|documentation|proof|evidence)\b[^.?!]*`)

// noDisabilityInquiryRE is the employer's promise to keep from asking about
// disability (Recharge's interview attestation: "I understand [the company]
// will avoid disability-related inquiries ..."): the phrase asks nothing and
// states no fact about the applicant, so it alone is read past.
var noDisabilityInquiryRE = lazyregexp.New(`(?i)\b(?:avoid|refrain\s+from|(?:will|shall)\s+not\s+(?:make|ask|pose))\s+(?:any\s+)?disability[- ]related\s+(?:inquir(?:y|ies)|questions?)\b`)

var (
	// disregardPromiseRE is a sentence in which the employer says it sets
	// protected data aside (AHEAD: "... any data that ... would indicate a
	// person's age, race, gender, disability status ... will be
	// disregarded"; GHX: "Applicants and Employees are evaluated without
	// regard to mental or physical disability, ...").
	disregardPromiseRE = lazyregexp.New(`(?i)\b(?:data|information|characteristics?)\b[^.?!]*\b(?:will|shall)\s+be\s+(?:disregarded|ignored)\b|\bwithout\s+regard\s+to\b`)
	// accommodationOfferRE is a sentence in which the employer speaks of the
	// accommodations it offers (Suno: "We will consider requests for
	// accommodation to this policy and ... will work with employees to explore
	// reasonable accommodations for physical or mental disabilities ...").
	accommodationOfferRE = lazyregexp.New(`(?i)\bwe\b.*\baccommodat|\baccommodat.*\bwe\b`)
	// applicantAddressRE is a sentence that speaks to or for the applicant.
	applicantAddressRE = lazyregexp.New(`(?i)\b(?:you|your|i|my|me)\b`)
)

// withoutAccommodationOffer drops the employer's own accommodation offer and
// its promise to disregard protected data: a policy sentence that neither asks
// nor speaks for the applicant states no fact about them.
func withoutAccommodationOffer(text string) string {
	var kept []string
	for _, s := range sentencesOf(text) {
		if (accommodationOfferRE.MatchString(s) || disregardPromiseRE.MatchString(s)) && !applicantAddressRE.MatchString(s) && !strings.HasSuffix(s, "?") {
			continue
		}
		kept = append(kept, s)
	}
	return strings.Join(kept, " ")
}

// isAttestationDefaultUnsafe is the single gate an affirmative attestation
// default must pass. A documentation requirement, the employer's promise not to
// ask about disability, its own accommodation offer and its promise to
// disregard protected data are read past.
func isAttestationDefaultUnsafe(text string) bool {
	t := withoutAccommodationOffer(noDisabilityInquiryRE.ReplaceAllString(documentRequirementRE.ReplaceAllString(text, " "), " "))
	return isDisqualifyingFactDisclosure(t) || isSensitiveCategoryQuestion(t) || isGenerativelyUnsafeQuestion(t)
}
