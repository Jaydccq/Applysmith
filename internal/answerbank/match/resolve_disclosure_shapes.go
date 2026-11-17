package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Question shapes of the conditional-disclosure family. The detectors are
// pure. The conditional follow-up pass and the resolvers of this package read
// them; the generative refusal list (isConditionalDetailSlot) and the model
// lanes of internal/answerbank/assist each hold their own patterns for a
// follow-up slot.

const imperativeAsk = `(?:indicate|specify|provide|describe|state|list|enter|type|give)`

var conditionalDetailREs = []*lazyregexp.Regexp{
	// A referred applicant's name box.
	lazyregexp.New(`(?i)\bif\s+(?:you\s+(?:were|are|have\s+been|had\s+been)\s+)?referred\b.*\b(?:employee|name|below)\b`),
	lazyregexp.New(`(?i)\bif\s+referred\b`),
	// "provide their name" needs its qualifier: a bare "provide name" inside a
	// references prompt once got an N/A in a required references box.
	lazyregexp.New(`(?i)\b(?:type|enter|provide|specify|give)\s+(?:(?:their|the|employee'?s?|referrer'?s?|full)\s+)+(?:full\s+)?names?\b`),
	// "explain" is not here: "If yes, please explain your non-compete" must
	// reach its reason entry and abstain until the user writes one.
	lazyregexp.New(`(?i)^\s*if\s+(?:yes|no|so|applicable)\b.{0,100}\b` + imperativeAsk + `\b`),
	lazyregexp.New(`(?i)^\s*if\s+you\s+answered\s+(?:yes|no)\b.{0,100}\b` + imperativeAsk + `\b`),
}

// isConditionalDetailText reports a follow-up slot whose subject is the answer
// before it, including the shapes that point back at the previous answer and
// may use "explain".
func isConditionalDetailText(text string) bool {
	for _, re := range conditionalDetailREs {
		if re.MatchString(text) {
			return true
		}
	}
	return isParentReferential(text)
}

var (
	specifyOtherREs = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\bother\b[^?]{0,30}\bspecify\b`),
		lazyregexp.New(`(?i)\bspecify\b[^?]{0,30}\bother\b`),
		lazyregexp.New(`(?i)^\s*if\s+(?:the\s+)?(?:above|previous|prior|preceding)\s+(?:question|answer|response|selection)\s+(?:is|was)\s+["“']?other\b`),
		lazyregexp.New(`(?i)^\s*if\s+you\s+(?:responded|answered|selected|chose)\s+["“']?other["”']?\s+(?:to|in|for|on)\s+the\s+(?:question\s+)?(?:above|previous)\b`),
		// Epirus: "If (f) Other, please explain:" under a lettered list.
		lazyregexp.New(`(?i)^\s*if\s+(?:\(?[a-z0-9]{1,2}\)\s*)?["“']?other["”']?[^?]{0,20}\b(?:specify|explain)\b`),
	}
	specifyNamedRE = lazyregexp.New(`(?i)^\s*if\s+(?:you\s+(?:selected|chose|picked|checked|answered)\s+)?["“']?([^,"”']{1,40}?)["”']?\s*,\s*(?:then\s+)?please\s+specify\b`)
	// specifySelectedRE is 'If "Employee Referral" was selected for "How did
	// you hear about this job?" please type ...' (iCapital).
	specifySelectedRE = lazyregexp.New(`(?i)^\s*if\s+["“']([^"”']{1,40})["”']\s+(?:was|is)\s+(?:selected|chosen|picked)\b`)
	// otherwiseTypeRE is the slot's own instruction for the other choices:
	// 'If not, please type in "N/A".', "If this is not applicable, please list
	// N/A." (Tenex).
	otherwiseTypeRE = lazyregexp.New(`(?i)\bif\s+(?:not|(?:this|it)\s+(?:is\s+not|isn't|does\s+not)\s+appl(?:icable|y)|not\s+applicable)\s*[,.]?\s+(?:please\s+)?(?:type|enter|write|put|input|list)\s+(?:in\s+)?["“']?(N/A|NA|None)["”']?`)
)

// specifyTrigger returns the option a "please specify" slot is keyed to: "Other"
// for the bare shape, the named option for "If you selected Referral, please
// specify who". "If yes, please specify" is a gate follow-up, not a specify
// slot.
func specifyTrigger(text string) (string, bool) {
	for _, re := range specifyOtherREs {
		if re.MatchString(text) {
			return "Other", true
		}
	}
	if m := specifySelectedRE.FindStringSubmatch(text); m != nil && !bareYesNoRE.MatchString(m[1]) {
		return strings.TrimSpace(m[1]), true
	}
	if m := specifyNamedRE.FindStringSubmatch(text); m != nil {
		t := strings.TrimSpace(m[1])
		// "If so, please specify" names no choice: it is the bare "If yes".
		if t != "" && !bareYesNoRE.MatchString(t) && !strings.EqualFold(t, "so") {
			return t, true
		}
	}
	return "", false
}

var (
	visaFollowUpA = lazyregexp.New(`(?i)\bvisa\b`)
	visaFollowUpB = lazyregexp.New(`(?i)\b(?:type|status)\b`)
	visaFollowUpC = lazyregexp.New(`(?i)\b(?:immigration|nonimmigrant)\s+status\b`)
)

// isVisaStatusFollowUp reports a visa or immigration-status follow-up: "visa"
// beside "type" or "status", or an immigration status.
func isVisaStatusFollowUp(text string) bool {
	return visaFollowUpA.MatchString(text) && visaFollowUpB.MatchString(text) || visaFollowUpC.MatchString(text)
}

var workAuthConditionalREs = [3]*lazyregexp.Regexp{
	lazyregexp.New(`(?i)did you answer\b`),
	lazyregexp.New(`(?i)\bno\b.*\bq(?:uestion)?\s*1\b`),
	lazyregexp.New(`(?i)\byes\b.*\bq(?:uestion)?\s*(?:2|3|two|three)\b`),
}

// isWorkAuthConditionalQuestion recognizes "Did you answer 'no' to Q1 and/or
// 'yes' to Q2 or 3?" by three tests.
func isWorkAuthConditionalQuestion(text string) bool {
	for _, re := range workAuthConditionalREs {
		if !re.MatchString(text) {
			return false
		}
	}
	return true
}

// isYesLabel and isNoLabel read the polarity of a gate answer.
var (
	gateYesLabelRE = lazyregexp.New(`(?i)^\s*(?:yes|yeah|yep|yup)\b|^\s*(?:true|i\s+(?:have|do|am|was|agree|confirm))\s*$`)
	gateNoLabelRE  = lazyregexp.New(`(?i)^\s*(?:no|nope|nah)\b|^\s*(?:false|none\s+of\s+the\s+above)\s*$`)
)

func isYesLabel(label string) bool { return label != "" && gateYesLabelRE.MatchString(label) }
func isNoLabel(label string) bool  { return label != "" && gateNoLabelRE.MatchString(label) }
