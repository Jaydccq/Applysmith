package assist

import (
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// The composer refuses by question shape. A shape never becomes composable
// because of the control it sits in: a textarea does not turn a link request
// into an essay.

var (
	// optionalShareRE: the "anything else you would like to share" box.
	optionalShareRE = lazyregexp.New(`(?i)\banything\s+else\b[^?]{0,80}\b(?:share|add|tell\s+us|let\s+us\s+know|mention|include|consider|note|provide|comment)\b|\banything\b[^?]{0,40}\byou(?:'d|\s+would|\s+wou?ld)?\s+like\s+us\s+to\s+know\b|\bwould\s+you\s+like\s+to\s+(?:share|add)\b[^?]{0,60}\b(?:anything|else|additional|more|with\s+us)\b`)

	linkVerbRE      = `(?:share|paste|provide|enter|drop|send|attach|include|post|list|submit|give)`
	linkNounRE      = `(?:links?|urls?|websites?)`
	linkRequestRE   = lazyregexp.New(`(?i)\b` + linkVerbRE + `\b[^.!?]{0,80}\b` + linkNounRE + `\b|\b` + linkNounRE + `\b[^.!?]{0,80}\b` + linkVerbRE + `\b|\blinks?\s+to\s+(?:your\s+)?(?:project|portfolio|work|profile|site|repo|github)`)
	narrativeVerbRE = lazyregexp.New(`(?i)\b(?:describe|tell|explain|why|how|what)\b`)
	locatorLeadRE   = lazyregexp.New(`(?i)^\s*(?:[a-z']+\s+){0,2}(?:url|link|links|website)\b\s+\S`)

	contactIdentityRE = lazyregexp.New(`(?i)^\W*(?:confirm\s+|re-?enter\s+|your\s+)?(?:e-?mail(?:\s+address)?|phone(?:\s+number)?|mobile(?:\s+(?:phone|number))?|telephone(?:\s+number)?|cell(?:\s+phone)?)\W*$`)
	// otherPersonRE asks for someone else's name or contact details: a
	// referrer's, a reference's or a recruiter's ("What's their name or email
	// address?" under Adobe's How Did You Hear About Us).
	otherPersonRE = lazyregexp.New(`(?i)\b(?:their|his|her|referr\w*|referee|reference|recruiter|employee)\b[^?.]{0,60}\b(?:name|e-?mail|phone|contact)\b|\b(?:name|e-?mail|phone|contact)\b[^?.]{0,40}\b(?:of|for)\s+(?:the\s+|your\s+|a\s+)?(?:referr\w*|referee|reference|recruiter|employee|person)\b`)
	contactVetoRE = lazyregexp.New(`(?i)\b(?:consent|prefer|preference|hear\s+about|how\s+did\s+you|contact\s+(?:method|me)|opt)\b`)

	disclosureRE = lazyregexp.New(`(?i)\bdisclose\b[^.?!]{0,100}\b(?:relationship|conflicts?|affiliations?|interests?|ownership|employment|family|relatives?)\b|\b(?:any|current|past|existing)\s+conflicts?\s+of\s+interest\b|\bconflicts?\s+of\s+interest\b`)

	applicationStatementRE = lazyregexp.New(`(?i)\b(?:did|have)\s+you\s+(?:attach|upload|include|submit|use|used|get|receive|receive[d]|write|written|complete|completed)\b[^.?!]{0,80}\b(?:cover\s+letter|r[e\x{00E9}]sum[e\x{00E9}]|cv|this\s+application)\b|\b(?:did|have)\s+you\s+(?:use|used|get|receive|received)\b[^.?!]{0,80}\b(?:ai\b|artificial\s+intelligence|chatgpt|assistance|help)\b[^.?!]{0,80}\b(?:application|this\s+form|answers?|responses?)\b|\b(?:this|the)\s+application\b[^.?!]{0,60}\b(?:completed|filled|written|prepared|submitted)\s+(?:by|with|using)\b|\bwas\s+(?:this|the)\s+application\b|\bhow\s+was\s+this\s+application\b|\bwho\s+(?:completed|filled)\s+(?:out\s+)?this\s+application\b`)

	acknowledgementRE = lazyregexp.New(`(?i)\bi\s+(?:hereby\s+)?(?:acknowledge|agree|certify|consent|authori[sz]e|attest|declare|have\s+read|confirm\s+that|understand\s+and)\b|\bby\s+(?:checking|clicking|selecting|submitting|ticking|signing)\b|\bterms\s+(?:and|&)\s+conditions\b|\bprivacy\s+(?:policy|notice|statement)\b|\be-?signature\b|\bopt[- ]?in\b|\bconsent\s+to\b|\bi\s+accept\b|\bat[- ]will\b`)

	structuredFactRE = lazyregexp.New(`(?i)\b(?:salary|compensation|comp\s+expectations?|pay\s+(?:expectations?|range|rate)|expected\s+pay|desired\s+(?:salary|pay|comp)|sponsor(?:ship|ed)?|h-?1b|visa|work\s+authori[sz]ation|authori[sz]ed\s+to\s+work|legally\s+(?:authori[sz]ed|eligible|able)|right\s+to\s+work|immigration|citizen(?:ship)?|status\s+in\s+the\s+(?:us|u\.s\.|united\s+states)|start\s+date|end\s+date|date\s+of\s+(?:start|employment)|when\s+did\s+you\s+(?:start|finish|begin|leave)|years?\s+of\s+(?:\w+\s+){0,3}experience|how\s+many\s+years|how\s+long\s+have\s+you)\b`)

	yesNoLeadRE = lazyregexp.New(`(?i)^\W*(?:do|does|did|are|is|have|has|had|can|could|will|would|were|was|may|shall)\s+(?:you|your|i|we|the\s+applicant)\b`)

	// selfQualificationRE: an interrogative, a self attribute and a fitness tie
	// bound to the second person, all three.
	interrogativeRE = lazyregexp.New(`(?i)\?|^\W*(?:what|why|how|which|in\s+what)\b`)
	selfAttrRE      = lazyregexp.New(`(?i)\b(?:skills?|training|experience|background|qualifications?|strengths?|expertise)\b`)
	fitnessTieRE    = lazyregexp.New(`(?i)\bqualif(?:y|ies)\s+you\b|\bmakes?\s+you\b|\byou\s+(?:are|would\s+be|'re)\s+(?:a\s+|an\s+|the\s+)?(?:good|best|strong|great|right|ideal)\s+(?:fit|candidate|match)\b|\bbring\s+to\s+(?:the|this|our)\b`)

	// otherSpecifyRE: "Other, please specify" follow-ups.
	otherSpecifyRE = lazyregexp.New(`(?i)\bother\b[^.?!]{0,40}\b(?:please\s+)?(?:specify|describe|explain)\b|\(\s*(?:please\s+)?specify\s*\)`)
	// repeatedRowRE: an index marker in a section name.
	repeatedRowRE = lazyregexp.New(`(?i)#\s*\d+\b|\b(?:school|employer|company|position|job|experience|education|entry)\s+\d+\b|\b\d+(?:st|nd|rd|th)\b`)
	entityRowRE   = lazyregexp.New(`(?i)\b(?:education|work|employment|experience|school|history)\b`)
)

// isApplicationStatement reports a statement about how this application was
// made: the matcher's definition (cover letter, résumé, referral, AI assistance)
// plus the composer's wider wording of how the application was completed.
func isApplicationStatement(t string) bool {
	return match.IsApplicationStatement(t) || applicationStatementRE.MatchString(t)
}

// isAcknowledgementWording reports the wording of an acknowledgement, legal
// declaration, consent or opt-in.
func isAcknowledgementWording(t string) bool { return acknowledgementRE.MatchString(t) }

// isFactOrConsentWording reports wording that only the user can settle: an
// acknowledgement, a statement about this application, a structured fact or a
// disclosure. The anti-spam lane never answers it.
func isFactOrConsentWording(t string) bool {
	return isAcknowledgementWording(t) || isApplicationStatement(t) || structuredFactRE.MatchString(t) ||
		match.AsksHeldCompensation(t) || disclosureRE.MatchString(t)
}

// isLinkRequest reports a request for a URL, judged on the first sentence only;
// a narrative verb there ("describe your projects (include a link)") vetoes it.
func isLinkRequest(t string) bool {
	first := t
	if i := strings.IndexAny(t, ".!?"); i >= 0 {
		first = t[:i]
	}
	return linkRequestRE.MatchString(first) && !narrativeVerbRE.MatchString(first)
}

// isBareLocatorLabel reports a single-line label that leads with at most two
// ordinary words and then a locator noun followed by more text.
func isBareLocatorLabel(t string) bool {
	return locatorLeadRE.MatchString(t) && !narrativeVerbRE.MatchString(t)
}

func isContactIdentity(t string) bool {
	return contactIdentityRE.MatchString(t) && !contactVetoRE.MatchString(t) && !narrativeVerbRE.MatchString(t)
}

func isSelfQualification(t string) bool {
	return interrogativeRE.MatchString(t) && selfAttrRE.MatchString(t) && fitnessTieRE.MatchString(t) && !yearsCountRE.MatchString(t)
}

var yearsCountRE = lazyregexp.New(`(?i)\bhow\s+many\s+years\b`)

// isYesNoAboutApplicant reports a yes/no question about the applicant that asks
// for no description.
func isYesNoAboutApplicant(t string) bool {
	return yesNoLeadRE.MatchString(t) && !narrativeVerbRE.MatchString(t) && !match.IsNarrativeText(t) && !match.IsEssayQuestion(t)
}

// isRepeatedEntityRow reports a control inside an indexed education or work row.
func isRepeatedEntityRow(path []string) bool {
	entity, indexed := false, false
	for _, p := range path {
		entity = entity || entityRowRE.MatchString(p)
		indexed = indexed || repeatedRowRE.MatchString(p)
	}
	return entity && indexed
}

// refusal is the verdict of the composer's question gate.
type refusal struct {
	// Refused says the composer must not run.
	Refused bool
	// Reason is recorded on the result: sensitive-category is named, an
	// empty reason keeps the matcher's.
	Reason match.Reason
	// Why is a short label for logs and the trace.
	Why string
	// Verification marks an anti-spam challenge: it is not composed, but the
	// verification call may answer it.
	Verification bool
}

func refuse(why string, reason match.Reason) refusal {
	return refusal{Refused: true, Reason: reason, Why: why}
}

// composerRefusal applies "Questions the composer refuses". text is the
// sanitized question, raw the question as the page sent it.
func composerRefusal(text string, raw, prepared match.Question) refusal {
	switch {
	case match.IsContentless(raw.Text) || strings.TrimSpace(text) == "":
		return refuse("prompt-only text", "")
	case func() bool { _, trap := match.TrapAbstain(raw); return trap }():
		return refuse("trap", match.ReasonAIInstruction)
	case match.IsSensitiveCategoryQuestion(text) || match.IsDisqualifyingFactDisclosure(text) || match.IsSelfIdentificationSet(raw.Options):
		return refuse("sensitive or disqualifying disclosure", match.ReasonSensitiveCategory)
	case match.IsGenerativelyUnsafeQuestion(text) || match.IsParentReferential(text) || otherSpecifyRE.MatchString(text):
		return refuse("unsafe-for-composition shape", "")
	case optionalShareRE.MatchString(text):
		return refuse("optional share box", "")
	case isApplicationStatement(text):
		return refuse("statement about this application", "")
	case isAcknowledgementWording(text):
		return refuse("acknowledgement or consent", "")
	case isLinkRequest(text):
		return refuse("link request", "")
	case prepared.InputType != match.InputLongText && isBareLocatorLabel(text):
		return refuse("locator field name", "")
	case isContactIdentity(text):
		return refuse("contact identity", "")
	case otherPersonRE.MatchString(text):
		return refuse("another person's name or contact details", "")
	case disclosureRE.MatchString(text):
		return refuse("disclosure or conflict of interest", "")
	case structuredFactRE.MatchString(text) || match.AsksHeldCompensation(text):
		return refuse("structured fact", "")
	case isYesNoAboutApplicant(text):
		return refuse("yes/no about the applicant", "")
	case isRepeatedEntityRow(raw.SectionPath):
		return refuse("repeated entity row", "")
	}
	// Every refusal above outranks the anti-spam lane: a verification phrase in
	// front of a consent, a fact or a disclosure does not make it a challenge.
	if isVerificationAnnouncement(text) {
		return refusal{Verification: true, Why: "anti-spam challenge"}
	}
	switch prepared.InputType {
	case match.InputLongText:
	case match.InputText:
		// A live fact is a one-word lookup by nature: it reaches its own lane
		// from a single-line box, which only the web search may answer.
		if !(match.IsEssayQuestion(text) || match.IsNarrativeText(text) || isSelfQualification(text) || isLiveFactQuestion(text)) {
			return refuse("short field is not narrative-shaped", "")
		}
	default:
		return refuse("structured input type", "")
	}
	return refusal{}
}
