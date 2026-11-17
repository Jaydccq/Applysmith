package match

import (
	"applysmith/internal/lazyregexp"
)

// narrativePatterns are the wordings that ask for prose. The same list marks a
// text control long_text and feeds the narrative guard.
var narrativePatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\b(?:tell us|describe|explain|elaborate|walk us through)\b`),
	lazyregexp.New(`(?i)\bwhy (?:are you|do you|did you|would you|should)\b`),
	lazyregexp.New(`(?i)\bhow would you\b`),
	// A manner asked of something done (Kinetic: "How did you use AI to ship
	// this project?"); how one heard of or found the role is a source.
	lazyregexp.New(`(?i)\bhow (?:did|have) you (?:use|used|approach|approached|handle|handled|solve|solved|build|built|ship|shipped|apply|applied|lead|led|manage|managed|overcome|decide|decided|measure|measured|leverage|leveraged|navigate|navigated|tackle|tackled|contribute|contributed|go about|gone about|deal with|dealt with)\b`),
	lazyregexp.New(`(?i)\bwhat (?:motivates|interests|excites|attracts|inspires|drives)\b`),
	lazyregexp.New(`(?i)\b(?:share|provide|give us|tell us about)\b[^.?!]{0,30}\b(?:a|an|one|your)\s+(?:time|experience|example|project|story|challenge|situation|instance|occasion|reason)\b`),
	lazyregexp.New(`(?i)\b(?:share|provide|give us|give|list|tell us about)\b[^.?!]{0,30}\b(?:(?:\d+|two|three|four|five|six|seven|eight|nine|ten|several|some|a few)\s+)?(?:specific\s+)?(?:examples|accomplishments|achievements)\b`),
	lazyregexp.New(`(?i)\bcover letter\b`),
	lazyregexp.New(`(?i)\badditional (?:comments|information|details|notes|context)\b`),
	lazyregexp.New(`(?i)\bin your own words\b`),
	lazyregexp.New(`(?i)\bbriefly (?:describe|explain|share|tell)\b`),
	lazyregexp.New(`(?i)` + guardNarrativeExample),
}

var timeZoneWordsRE = lazyregexp.New(`(?i)\btime[\s-]*zones?\b`)

var careerGoalRE = lazyregexp.New(`(?i)\bwhat (?:are you looking for|do you want) in your next (?:role|opportunity|position)\b`)

// otherChoiceAskRE is the "other" choice of an example list with its own ask,
// "... recruiter outreach or other (please describe fully)" (Avride): it asks
// for prose only when that choice is the answer.
var otherChoiceAskRE = lazyregexp.New(`(?i)\bor other\s*\(\s*(?:please\s+)?(?:describe|explain|specify|elaborate)\b[^()]*\)`)

// optionalExplainRE is a bracketed invitation to add an explanation after a
// question that asks something else (Ello: "Will you require sponsorship ...
// right now? (Feel free to explain.)"): the explanation is optional, so the
// question asks for no prose.
var optionalExplainRE = lazyregexp.New(`(?i)\?\s*\(\s*(?:please\s+)?(?:feel\s+free\s+to|you\s+may|optionally)\s+(?:explain|elaborate|add\s+(?:more\s+)?(?:context|details?))\s*\.?\s*\)\s*$`)

// ifNotExplainTailRE closes a question a Yes or No answers by asking to
// explain a No (Relativity Space: "... are you willing to commute and/or
// relocate for this role? If not, please explain:").
var ifNotExplainTailRE = lazyregexp.New(`(?i)\?\s*if\s+(?:not|no)\s*,?\s*(?:please\s+)?(?:explain|elaborate|tell\s+us\s+why)\b[^?]*$`)

// wantsWordsOnlyOfANo reports a question whose only ask for prose is the
// explanation of a No: a Yes answers it whole, and a bare No never does
// (explainedNo).
func wantsWordsOnlyOfANo(text string) bool {
	loc := ifNotExplainTailRE.FindStringIndex(text)
	if loc == nil {
		return false
	}
	head := text[:loc[0]+1]
	return !isNarrativeText(head) && !isEssayQuestion(head)
}

// isNarrativeText reports a label that asks the applicant for prose.
// sourceDetailRE is the box for more about the named source: no prose.
var sourceDetailRE = lazyregexp.New(`(?i)` + rxSourceDetail)

func isNarrativeText(text string) bool {
	if sourceDetailRE.MatchString(text) {
		return false
	}
	text = otherChoiceAskRE.ReplaceAllString(text, "or other")
	text = optionalExplainRE.ReplaceAllString(text, "?")
	// Basis: "Please share your time zone." asks for no story about a time.
	text = timeZoneWordsRE.ReplaceAllString(text, "timezone")
	for _, re := range narrativePatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// isCareerGoalQuestion reports the career-goal question, a narrative preference.
func isCareerGoalQuestion(text string) bool { return careerGoalRE.MatchString(text) }

var essayPatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\bwalk (?:me|us) through\b`),
	lazyregexp.New(`(?i)\b(?:who|whom|whose|which)\b[^?]{0,60}\b(?:respect|admire|look up to|inspires?|inspired|follow most|you rate)\b`),
	lazyregexp.New(`(?i)\bbe specific\b`),
	lazyregexp.New(`(?i)\bwhat surprised you\b`),
	lazyregexp.New(`(?i)\b(?:examples?|instances?) of (?:your )?(?:accomplishments?|achievements?)\b`),
	lazyregexp.New(`(?i)\b(?:provide|share|give|list)\s+(?:us\s+)?(?:\d+|two|three|four|five|several|some)\s+(?:specific\s+)?examples\b`),
}

// isEssayQuestion reports an essay ask: an opinion about people, a walkthrough,
// "be specific", what surprised the applicant, or a counted list of examples.
func isEssayQuestion(text string) bool {
	for _, re := range essayPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

var (
	sponsorshipDescribeRE = lazyregexp.New(`(?i)` + guardSponsorshipDescribe + `|` + guardVisaHeldAsk)
	parentReferentialREs  = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\bif you (?:answered|responded|selected|chose|said)\b[^.?!]{0,60}\b(?:yes|no)\b[^.?!]{0,100}\b(?:indicate|specify|provide|describe|state|list|enter|type|give|explain)\b`),
		lazyregexp.New(`(?i)\bif your answer (?:above |below )?(?:was|is) (?:yes|no)\b`),
		lazyregexp.New(`(?i)\bif any of the above answers\b[^.?!]{0,60}\brequires? an explanation\b`),
		lazyregexp.New(`(?i)\bif your selection is not your most recent\b[^.?!]{0,60}\bindicate your most recent\b`),
	}
)

// isSponsorshipDescription reports the short factual "describe the sponsorship
// you require" or "explain if you have a visa" slot, which the narrative guard
// leaves alone.
func isSponsorshipDescription(text string) bool { return sponsorshipDescribeRE.MatchString(text) }

// isParentReferential reports a follow-up slot that points back at the previous
// question ("If you answered yes, please explain"); it is never composed.
func isParentReferential(text string) bool {
	for _, re := range parentReferentialREs {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}
