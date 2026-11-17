package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// ModelWritten reports a question the apply run's composing model answers
// afresh on every application, from the CV, voice and fit, and so never an
// Answer Bank answer (owner decision: every open-ended question is
// written by the model). It clears the deny-first list, asks for no short
// fact, and is an opinion, a narrative or essay ask, an open wording, an
// experience ask, or a long box. A choice is the model's only for an opinion.
func ModelWritten(question string, choice, long bool) bool {
	switch {
	case composeDenyRE.MatchString(askedPart(question)):
		return false
	case composeOpinionRE.MatchString(question), composeBeliefRE.MatchString(question) && !composeSelfClaimRE.MatchString(question):
		return true
	case choice || composeFactRE.MatchString(question):
		return false
	}
	return long || isNarrativeText(question) || isEssayQuestion(question) || composeOpenRE.MatchString(question) || composeBuiltRE.MatchString(question) ||
		composeExperienceRE.MatchString(question) && !composeExperienceNotRE.MatchString(question)
}

// askedPart is a question through its first question mark: the sentences after
// it guide the answer and name no fact it asks for (Nominal: "Why are you
// interested in Nominal? ... your first full-time role out of school ... Feel
// free to reference specific things about our product").
func askedPart(q string) string {
	if i := strings.Index(q, "?"); i >= 0 {
		return q[:i+1]
	}
	return q
}

var (
	composeOpinionRE = lazyregexp.New(`(?i)\b(?:why\s+(?:do\s+you|are\s+you|would\s+you|this|us|our)|what\s+(?:excites|interests|attracts|motivates|draws)\s+you|what\s+(?:do\s+you|would\s+you)\s+(?:like|enjoy|value|hope)|tell\s+us\s+(?:about\s+yourself|why)|describe\s+(?:your|a)\s+(?:interest|passion|approach|favou?rite|ideal)|fun\s+fact|icebreaker|favou?rite|your\s+(?:motivation|interest\s+in|thoughts?\s+on)|what\s+makes\s+you|how\s+(?:did|do)\s+you\s+(?:become|think)|self[-\s]?assess\w*|rate\s+your)\b`)
	// composeBeliefRE asks what the applicant thinks or believes (Mechanize:
	// "Do you think it's ethical to automate people's jobs?"); composeSelfClaimRE
	// asks it of the applicant's own standing ("Do you believe you meet the
	// qualifications?"), a claim and not an opinion.
	composeBeliefRE    = lazyregexp.New(`(?i)^\W*do\s+you\s+(?:think|believe)\b`)
	composeSelfClaimRE = lazyregexp.New(`(?i)\bdo\s+you\s+(?:think|believe)\s+(?:that\s+)?(?:you|your|yourself)\b`)
	composeDenyRE      = lazyregexp.New(`(?i)\b(?:work\s+authori[sz]\w*|authori[sz]ed\s+to\s+work|sponsor\w*|visa|eeo\w*|gender|ethnic\w*|race|veteran|disabilit\w*|salary|compensation|pay\s+expectation|desired\s+pay|rate\s+of\s+pay|date|when\s+can\s+you|notice\s+period|start|available\s+to|school|university|college|degree|gpa|signature|sign\s+here|initials|criminal|convict\w*|felony|employer|company\s+name|current\s+(?:title|company)|previous\s+(?:title|company)|licen[sc]e\w*|certif\w*|clearance|reference\w*|referr\w*|phone|e-?mail|(?:mailing|home|street|postal|your)\s+address|address\s+line|linkedin|github|background\s+check|drug\s+test|age|birth\w*|citizen\w*)\b`)
	// composeOpenRE are the open-ended wordings of a free-text question past the
	// opinion list and the matcher's narrative ones: a bare "Why <company>?",
	// what drew or excited the applicant, pride, accomplishments, an idea the
	// applicant had (Bastion: "What is the best idea you have ever had?"),
	// anything else to add.
	composeOpenRE = lazyregexp.New(`(?i)^\W*why\b|\bwhat\b[^?]{0,60}\b(?:excit|interest|attract|motivat|draw|drew|inspir|appeal|passion|love|enjoy)\w*|\bproud\b|\baccomplish\w*|\bachievement\w*|\b(?:best|favou?rite|proudest|biggest|greatest)\s+idea\b|\bidea\s+you\s+(?:have\s+|'ve\s+)?(?:ever\s+)?(?:had|come\s+up\s+with)\b|\banything\s+else\b`)
	// composeFactRE are the wordings of a short fact no composition may supply: a
	// count or length, a link or handle, a name, a place, how the applicant
	// heard of the role.
	composeFactRE = lazyregexp.New(`(?i)\b(?:how\s+(?:many|much|long)|number\s+of|years?|url|link|website|portfolio|profile|handle|username|name|where\s+(?:are|do)\s+you|located|based\s+in|currently\s+based|city|country|zip|postal|hear\s+about|pronouns?)\b`)
	// composeExperienceRE is a free-text ask about the applicant's own
	// experience ("Have you previously supported a SaaS company?"), answered
	// from the CV; composeExperienceNotRE keeps a statement about this
	// application or the AI that wrote it out.
	// composeBuiltRE is a "what" question about something the applicant built,
	// solved or shipped ("What was a model that you have built for someone
	// else?", "the most complex piece of logic that you have implemented"),
	// told from the CV's projects and work (Planting Space).
	composeBuiltRE         = lazyregexp.New(`(?i)\bwhat\b[^?]{0,80}\byou\s+(?:have\s+|'ve\s+|had\s+)?(?:ever\s+|personally\s+)?(?:built|implemented|designed|developed|shipped|created|solved|delivered|launched|worked\s+on)\b`)
	composeExperienceRE    = lazyregexp.New(`(?i)^\W*(?:have|do|did)\s+you\s+(?:ever\s+|previously\s+|already\s+|personally\s+)?(?:(?:have|had|gained)\s+(?:any\s+)?(?:prior\s+|previous\s+|professional\s+|direct\s+|hands[-\s]?on\s+)?experience|supported|built|used|developed|designed|deployed|implemented|shipped|led|managed|maintained|worked\s+(?:with|on))\b`)
	composeExperienceNotRE = lazyregexp.New(`(?i)\b(?:ai|chatgpt|artificial\s+intelligence|this\s+(?:application|form))\b`)
)
