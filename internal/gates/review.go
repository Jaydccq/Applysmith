// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"fmt"
	"regexp"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Gate names and reason codes of the page checks.
const (
	GateIdentityReview Gate = "identity_review"
	GateReviewFlag     Gate = "review_flag"
	GateReadiness      Gate = "submit_readiness"

	CodeIdentityMismatch  = "identity_mismatch"
	CodeFlagBlocks        = "flag_blocks"
	CodeFlagAdvice        = "flag_advice"
	CodeFlagNoProbability = "flag_no_probability"
	CodeFlagDropped       = "flag_dropped"
	CodeFlagNotListed     = "flag_not_listed"
	CodeFlagSelfCleared   = "flag_self_cleared"
	// CodeFlagRelocationNotPresence is a flag on a No to where the applicant
	// is now that rests on a willingness to relocate.
	CodeFlagRelocationNotPresence = "flag_relocation_not_presence"
	// CodeFlagOwnerDefaultAbsence is a flag on the owner's standing default
	// whose reason says only that the profile does not show it.
	CodeFlagOwnerDefaultAbsence = "flag_owner_default_absence"
	CodeFlagFallback            = "flag_fallback"
	// CodeFlagSoleOption is a flag on the only real option of its control.
	CodeFlagSoleOption = "flag_sole_option"
	// CodeFlagPreferenceNotPast is a flag on a No to whether the applicant has
	// worked some way before that rests on how they prefer to work.
	CodeFlagPreferenceNotPast = "flag_preference_not_past"
	// CodeFlagRecordAbsence is a flag on a value written from the user's
	// record whose reason says only that the review's profile lines do not
	// hold it.
	CodeFlagRecordAbsence   = "flag_record_absence"
	CodePageUnreadable      = "page_unreadable"
	CodeRequiredUnanswered  = "required_unanswered"
	CodeValueNotHeld        = "value_not_held"
	CodeReviewContradiction = "review_contradiction"
)

// Review bounds. No setting and no environment variable lowers them.
const (
	ReviewBlockFloor       = 0.70
	ReviewPhoneCodeFloor   = 0.85
	ReviewMaxRows          = 40
	ReviewValueMaxRunes    = 120
	ReviewProfileMaxLines  = 15
	ReviewMaxOutputTokens  = 512
	identityDetailMaxRunes = 60
)

// IdentityField is a profile field the identity review compares.
type IdentityField string

const (
	FieldFullName      IdentityField = "full_name"
	FieldFirstName     IdentityField = "first_name"
	FieldLastName      IdentityField = "last_name"
	FieldEmail         IdentityField = "email"
	FieldPhone         IdentityField = "phone"
	FieldNationalPhone IdentityField = "national_phone"
	FieldLinkedIn      IdentityField = "linkedin_url"
	FieldGitHub        IdentityField = "github_url"
	FieldPortfolio     IdentityField = "portfolio_url"
)

// NormalizeIdentity applies the comparison normalization of a field. Names
// are trimmed and white space collapsed, case staying significant; email is
// trimmed and case-folded; phone is reduced to digits with a leading country
// digit 1 dropped from 11 digits; a URL loses its scheme, a leading www. and
// trailing slashes.
func NormalizeIdentity(f IdentityField, v string) string {
	switch f {
	case FieldEmail:
		return strings.ToLower(strings.TrimSpace(v))
	case FieldPhone, FieldNationalPhone:
		var b strings.Builder
		for _, r := range v {
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		d := b.String()
		if len(d) == 11 && d[0] == '1' {
			d = d[1:]
		}
		return d
	case FieldLinkedIn, FieldGitHub, FieldPortfolio:
		s := strings.ToLower(strings.TrimSpace(v))
		s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		return strings.TrimRight(strings.TrimPrefix(s, "www."), "/")
	}
	return strings.Join(strings.Fields(v), " ")
}

// IdentityControl is one control the run's fill bound to an identity field.
type IdentityControl struct {
	Selector, Label string
	Field           IdentityField
	// PageValue is the value after the page was filled; ProfileValue is the
	// profile's value for the field.
	PageValue, ProfileValue string
	// ReRead is false when the page re-read did not return the control.
	ReRead bool
}

// IdentityMismatch names the control and both values.
type IdentityMismatch struct {
	Selector, Label         string
	Field                   IdentityField
	PageValue, ProfileValue string
}

// IdentityResult carries the mismatches and the controls left unverified.
type IdentityResult struct {
	Decision
	Mismatches []IdentityMismatch
	Unverified []string
}

// ReviewIdentity compares what each identity control holds with the profile.
// A control is compared only when its page value is non-empty after
// normalization and the profile holds a value; a control the re-read did not
// return is unverified and does not block by itself.
func ReviewIdentity(controls []IdentityControl) IdentityResult {
	var res IdentityResult
	for _, c := range controls {
		if !c.ReRead {
			res.Unverified = append(res.Unverified, c.Selector)
			continue
		}
		page, prof := NormalizeIdentity(c.Field, c.PageValue), NormalizeIdentity(c.Field, c.ProfileValue)
		if page == "" || prof == "" || page == prof {
			continue
		}
		res.Mismatches = append(res.Mismatches, IdentityMismatch{c.Selector, c.Label, c.Field, c.PageValue, c.ProfileValue})
	}
	if len(res.Mismatches) == 0 {
		res.Decision = decide(GateIdentityReview, Allow, CodeOK, "identity controls match the profile (%d unverified)", len(res.Unverified))
		return res
	}
	m := res.Mismatches[0]
	res.Decision = decide(GateIdentityReview, Deny, CodeIdentityMismatch,
		"identity mismatch (%d): control %s %q field %s holds %q, profile has %q",
		len(res.Mismatches), m.Selector, m.Label, m.Field, clip(m.PageValue, identityDetailMaxRunes), clip(m.ProfileValue, identityDetailMaxRunes)).withClass(ClassDefect)
	return res
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Flag is one finding of the advisory review or a deterministic check.
type Flag struct {
	Value string
	// Probability is meaningful only when HasProbability is true.
	Probability    float64
	HasProbability bool
	// Fallback marks a flag from a model other than the review task's own:
	// the blocking floor is calibrated on the task's model, not on a fallback.
	Fallback bool
	// Reason is the review's own words for the flag.
	Reason string
	// FromRecord marks a flag on a value the matcher wrote from the user's
	// record with no model: the lane read facts the review's few profile lines
	// may not hold (Gremlin's Java Yes from the profile's skills).
	FromRecord bool
	// OwnerDefault marks a flag on the owner's standing default for what no
	// fact settles (Arketa's "Have you managed, operated or taught at a
	// fitness/studio/wellness business?" = Yes): the absence of a fact is the
	// default's premise.
	OwnerDefault bool
	// SoleOption marks a flag on the only real option of its control (Lyft's
	// "Thank you" under an instruction): the form allows no other value.
	SoleOption bool
	// Choices are the options of the list the value was picked from, if any.
	Choices []string
}

var phoneCodeRE = lazyregexp.New(`^\+[0-9]{1,3}$`)

// FlagBlocks reports whether a flag blocks Submit: at least 0.70, or at least
// 0.85 when the flagged value is only a plus sign and one to three digits. A
// flag without a probability never blocks.
func FlagBlocks(f Flag) bool {
	if !f.HasProbability {
		return false
	}
	floor := ReviewBlockFloor
	if phoneCodeRE.MatchString(strings.TrimSpace(f.Value)) {
		floor = ReviewPhoneCodeFloor
	}
	return f.Probability >= floor
}

// OwnOffice reports whether a flagged value is the office named in the
// posting's own location line: equal to the whole line, to one of its offices
// separated by ";", "|" or "/", or to an office before its first comma or
// parenthesis.
func OwnOffice(value, location string) bool {
	v := strings.ToLower(strings.Join(strings.Fields(value), " "))
	if v == "" {
		return false
	}
	cands := []string{location}
	for _, o := range strings.FieldsFunc(location, func(r rune) bool { return r == ';' || r == '|' || r == '/' }) {
		cands = append(cands, o)
		if i := strings.IndexAny(o, ",("); i > 0 {
			cands = append(cands, o[:i])
		}
	}
	for _, c := range cands {
		if strings.ToLower(strings.Join(strings.Fields(c), " ")) == v {
			return true
		}
	}
	return false
}

var (
	notListedRE = lazyregexp.New(`(?i)^\s*other\b.*\bnot listed\b`)
	otherRE     = lazyregexp.New(`(?i)^\s*other\s*$`)
	schoolRE    = lazyregexp.New(`(?i)\b(?:schools?|universit(?:y|ies)|colleges?|institutions?)\b`)
	// otherStateRE is a state list's catch-all, which names itself (n8n: "Other state").
	otherStateRE = lazyregexp.New(`(?i)^\s*other\s+(?:u\.?s\.?\s+)?states?\b`)
)

// selfClearedRE reads a review reason that says the value is not contradicted:
// the review model listed the value and cleared it in the same breath (Runway's
// U.S.-or-Canada answer: "Yes is consistent; no values are contradicted").
var selfClearedRE = lazyregexp.New(`(?i)\bno\s+(?:values?\s+)?(?:is\s+|are\s+|was\s+|were\s+)?contradicted\b|\b(?:does|do)(?:\s+not|n't)\s+contradict\b|\bnot\s+(?:(?:a|the|any)\s+)?contradict(?:ed|ion)\b`)

// clearedVerdictRE is a reason's last sentence that clears the value it
// flagged (Applied Intuition's degree: "... is not the contradiction?
// Actually, it matches the profile."), or the clause that ends it after a
// comma (10a Labs' start year: "The profile lists 2024-08, so this value
// matches.").
var clearedVerdictRE = lazyregexp.New(`(?i)(?:^\W*|,\s*)(?:(?:actually|so|thus|therefore|in\s+fact)\W+)?(?:it|this|that|(?:the|this)\s+(?:value|degree|answer))\s+(?:(?:matches|fits|agrees\s+with)(?:\s+the\s+profile)?|is\s+(?:consistent|in\s+line)\s+with\s+the\s+profile)\W*$`)

// absenceReasonRE opens a review reason that says the profile holds no such
// thing, which is the No it flags (Akuity: "The profile shows no work at a B2B
// SaaS startup; ..."; Island: "The profile does not support professional
// low-level systems development ..."), or says it after granting what the
// profile does hold (Footprint: "The profile accepts on-site work and is open
// to relocating to New York, but does not say they can commit to five days a
// week in an office."), or as "but no" (Pogo: "..., but no startup or
// quant-firm work."). concessionContradictRE is wording that makes such a
// concession a contradiction ("lists a bachelor's degree only").
var (
	absenceReasonRE        = lazyregexp.New(`(?i)^\s*(?:the\s+)?(?:profile|r[ée]sum[ée]|record)\s+(?:(?:shows|lists|has|mentions|includes|indicates|contains)\s+no\b|(?:does\s+not|doesn't)\s+(?:show|list|mention|include|indicate|contain|support|say|state|specify)\b)|^\s*no\s+(?:evidence|mention|record)\s+of\b`)
	concessionAbsenceRE    = lazyregexp.New(`(?i)^\s*(?:the\s+)?(?:profile|r[ée]sum[ée]|record)\b[^.;]*?,?\s+but\s+(?:it\s+)?(?:(?:does\s+not|doesn't)\s+(?:show|list|mention|include|indicate|contain|support|say|state|specify|confirm)|(?:(?:shows|lists|mentions|has)\s+)?no)\b`)
	concessionContradictRE = lazyregexp.New(`(?i)\bonly\b|\bcontradict|\binstead\b|\brather\b|\bconflict`)
)

// absenceReason reports a review reason that says only that the profile does
// not hold the value.
func absenceReason(reason string) bool {
	return absenceReasonRE.MatchString(reason) || concessionAbsenceRE.MatchString(reason) && !concessionContradictRE.MatchString(reason)
}

// zeroFloorValueRE is a years answer that starts at zero ("0-2", "Less than 1
// year", "None"): it claims none of the experience a profile may lack.
var zeroFloorValueRE = lazyregexp.New(`(?i)^\s*(?:no|none|no\s+experience|0(?:\s*(?:-|\x{2013}|to)\s*\d{1,2})?(?:\s*(?:years?|yrs?))?|(?:less\s+than|fewer\s+than|under)\s+(?:\d{1,2}|one|a)\s+(?:years?|yrs?))\s*\.?\s*$`)

// SelfCleared reports whether a flag's own reason says the value is not
// contradicted, says the profile holds none of what a No or a years answer
// that starts at zero denies (Regard's "0-2" years of data engineering), ends
// by saying the value matches the profile, or ends by putting the figure
// inside the very range it flagged (Verkada's GPA: "... not 3.1–3.5? Actually
// 3.4 is within 3.1–3.5.").
func SelfCleared(reason, value string) bool {
	if selfClearedRE.MatchString(reason) || zeroFloorValueRE.MatchString(value) && absenceReason(reason) {
		return true
	}
	sentences := sentenceEndRE.Split(strings.TrimSpace(reason), -1)
	last := ""
	for _, s := range sentences {
		if strings.TrimSpace(s) != "" {
			last = s
		}
	}
	if clearedVerdictRE.MatchString(last) {
		return true
	}
	v := regexp.QuoteMeta(dashSpaceRE.ReplaceAllString(strings.TrimSpace(value), "-"))
	if v == "" {
		return false
	}
	return regexp.MustCompile(`(?i)\b(?:is|falls|lies)\s+(?:within|inside|in)\s+(?:the\s+)?(?:range\s+)?` + v + `(?:$|[^\d.])`).MatchString(dashSpaceRE.ReplaceAllString(last, "-"))
}

var (
	// dashSpaceRE is a range's dash with the spaces around it: "3.1 – 3.5" reads "3.1-3.5".
	dashSpaceRE = lazyregexp.New(`\s*[-–—]\s*`)
	// sentenceEndRE ends a sentence; the point inside a figure does not.
	sentenceEndRE = lazyregexp.New(`[!?]|\.(?:\s|$)`)
)

var (
	// noneValueRE is a value that picks none of a list's choices.
	noneValueRE = lazyregexp.New(`(?i)^\W*none(?:\s+of\s+(?:the\s+)?(?:above|these|them))?\W*$`)
	// negatedSpanRE is a negation and the rest of its clause, up to a stop or
	// a contrast ("no private equity, investment banking, or consulting
	// experience; ...").
	negatedSpanRE = lazyregexp.New(`(?i)\b(?:no|not|never|neither|nor|without|lacks?|lacking)\b[^.;:]*?(?:[.;:]|\b(?:but|however|although|though|yet|while|whereas)\b|$)`)
)

// NoneOfTheChoices reports a flag on a "None" picked from a list whose reason
// names the list's other choices only inside a negation (Wonderschool: "The
// profile shows software engineering and research experience, but no private
// equity, investment banking, or consulting experience; selecting None
// contradicts ..."): the reason agrees with the value, whatever it concludes.
// A choice named outside a negation is one the reason says the profile holds.
func NoneOfTheChoices(reason, value string, choices []string) bool {
	if !noneValueRE.MatchString(value) {
		return false
	}
	negated := negatedSpanRE.FindAllStringIndex(reason, -1)
	named := false
	for _, c := range choices {
		c = strings.TrimSpace(c)
		if c == "" || noneValueRE.MatchString(c) {
			continue
		}
		for _, at := range regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(c)+`\b`).FindAllStringIndex(reason, -1) {
			inside := false
			for _, n := range negated {
				if at[0] >= n[0] && at[1] <= n[1] {
					inside = true
					break
				}
			}
			if !inside {
				return false
			}
			named = true
		}
	}
	return named
}

// NotListedChoice reports whether a flagged value is a list's own "Other ...
// not listed" choice, the bare "Other" of a school list (Ensemble's Workday
// catalog), or a state list's "Other state" (n8n). The engine takes it only for
// a school, employer or state the list does not hold, so a profile naming that
// school or state does not contradict it (seen live on a Lever school menu
// without the persona's university; Equifax asks "one of the following
// schools", in the plural).
func NotListedChoice(value, label string) bool {
	return notListedRE.MatchString(value) || otherStateRE.MatchString(value) || otherRE.MatchString(value) && schoolRE.MatchString(label)
}

// FlagGate decides what one flag on the control labelled label does. A flag
// naming the posting's own office is dropped; one on a list's "Other ... not
// listed" choice, one whose own reason says the value is not contradicted, one
// on the only option a control offers, and one on a value written from the
// record whose reason says only that the profile does not mention it (the
// review is told an unmentioned value is no contradiction, and is shown a few
// profile lines, not the record the lane read), is advice; a flag at or above
// its floor blocks; anything else is advice.
func FlagGate(f Flag, label, postingLocation string) Decision {
	const g = GateReviewFlag
	switch {
	case OwnOffice(f.Value, postingLocation):
		return decide(g, Allow, CodeFlagDropped, "the flagged value is the posting's own office; dropped and recorded")
	case NotListedChoice(f.Value, label):
		return decide(g, Allow, CodeFlagNotListed, "the flagged value is the list's own \"Other ... not listed\" choice for a name the list lacks; recorded as advice")
	case SelfCleared(f.Reason, f.Value):
		return decide(g, Allow, CodeFlagSelfCleared, "the review's own reason says the value is not contradicted; recorded as advice")
	case NoneOfTheChoices(f.Reason, f.Value, f.Choices):
		return decide(g, Allow, CodeFlagSelfCleared, "the review's own reason names the list's choices only as ones the profile lacks, which is the \"None\" it flags; recorded as advice")
	case PreferenceAgainstPast(f.Reason, label, f.Value):
		return decide(g, Allow, CodeFlagPreferenceNotPast, "the review reads a work preference against a No to how the applicant has worked before; recorded as advice")
	case f.SoleOption:
		return decide(g, Allow, CodeFlagSoleOption, "the flagged value is the only option its control offers; recorded as advice")
	case f.OwnerDefault && absenceReason(f.Reason):
		return decide(g, Allow, CodeFlagOwnerDefaultAbsence, "the value is the owner's default for what no fact settles, and the review says only that the profile does not show it; recorded as advice")
	case f.FromRecord && absenceReason(f.Reason):
		return decide(g, Allow, CodeFlagRecordAbsence, "the review says only that its profile lines do not mention a value written from the record; recorded as advice")
	case RelocationAgainstPresence(f.Reason, label, f.Value):
		return decide(g, Allow, CodeFlagRelocationNotPresence, "the review reads a willingness to relocate against a No to where the applicant is now; recorded as advice")
	case !f.HasProbability:
		return decide(g, Allow, CodeFlagNoProbability, "a flag without a probability is advice")
	case f.Fallback:
		return decide(g, Allow, CodeFlagFallback, "a fallback model's flag is advice: the blocking floor is calibrated on the review task's own model")
	case FlagBlocks(f):
		return decide(g, Deny, CodeFlagBlocks, "the review contradicts a value this run wrote").withClass(ClassDefect)
	}
	return decide(g, Allow, CodeFlagAdvice, "flag below its blocking floor; recorded as advice")
}

// ReviewRow is one control the page fill touched.
type ReviewRow struct {
	Label, Value string
	// TextControl is true for a text or long-text control; HasOptions for one
	// that offers options.
	TextControl, HasOptions bool
	WrittenByRun            bool
	// Attestation, IdentityBound, Signature, ApplicationStatement and WorkAuth
	// mark rows the advisory review never receives. A work-authorization answer
	// comes from facts no model is shown, so the review could only guess at it
	// (Red Hat: "degrees in the UK and US … likely already have work
	// authorization").
	Attestation, IdentityBound, Signature, ApplicationStatement, WorkAuth bool
	// FromRecord marks a value the matcher wrote from the user's recorded
	// information with no model, neither for review nor by the check.
	FromRecord bool
	// InEntry marks a row of a repeated work-history or education entry
	// ("Education 1"), whose value the review reads against that entry's
	// other rows.
	InEntry bool
}

// SelectReviewRows returns the rows sent to the advisory review: rows this run
// wrote with a non-empty value, without the excluded kinds, the
// overall-experience questions, a Yes or No to a years threshold, a GPA
// bracket, a Yes or No to a graduation term or year, a Yes or No to living
// in, or needing to relocate to, the role's
// location, an age, a start, a degree level or the school of a question that
// names no degree level written from the record, the parts of the applicant's own address, a
// question about the employer's eligible places, a graduation question
// answered with a term, a government employment question written from the
// record, a security-clearance question,
// an export-control status and a citizenship question, the first 40 of them,
// each value longer than 120 characters cut to 119 and an ellipsis: the
// review is told a value ending so is shown cut short (Black Canyon: a value
// cut bare at "capacity exceeding" was flagged for leaving the number out).
func SelectReviewRows(rows []ReviewRow) []ReviewRow {
	var out []ReviewRow
	for _, r := range rows {
		if !r.WrittenByRun || strings.TrimSpace(r.Value) == "" || r.Attestation || r.IdentityBound || r.Signature || r.ApplicationStatement || r.WorkAuth || OverallExperience(r.Label) ||
			r.FromRecord && (GovernmentEmployment(r.Label) || YearsThresholdAnswer(r.Label, r.Value) || GPABracketAnswer(r.Label, r.Value) || GraduationYesNo(r.Label, r.Value) || EnrollmentYesNo(r.Label, r.Value) || RoleResidenceAnswer(r.Label, r.Value) || TravelShareAnswer(r.Label, r.Value) || AgeAnswer(r.Label) || SchoolAnswer(r.Label) || !r.InEntry && DegreeAnswer(r.Label) || r.TextControl && FieldAnswer(r.Label) || StartAnswer(r.Label)) || Residence(r.Label) || EligiblePlaces(r.Label) || GraduationAnswer(r.Label, r.Value) || Clearance(r.Label) || ExportControl(r.Label, r.Value) || Citizenship(r.Label) {
			continue
		}
		r.Value = CutReviewValue(r.Value)
		if out = append(out, r); len(out) == ReviewMaxRows {
			break
		}
	}
	return out
}

// CutReviewValue is the value as the review is shown it: one longer than
// ReviewValueMaxRunes is cut to one rune less and an ellipsis.
func CutReviewValue(v string) string {
	if r := []rune(v); len(r) > ReviewValueMaxRunes {
		return string(r[:ReviewValueMaxRunes-1]) + "…"
	}
	return v
}

// eligiblePlacesAskRE names the places an employer hires in (BILL: "one
// BILL's remote-eligible states").
var eligiblePlacesAskRE = lazyregexp.New(`(?i)\b(?:remote[- ])?eligible\s+(?:states?|locations?|regions?|countries)\b`)

// EligiblePlaces reports a question about the places the employer hires in.
// The review is shown no list of them: it read BILL's "Yes, I currently reside
// in one of the eligible states" for a Charleston, SC address as a
// contradiction, naming South Carolina ineligible on no evidence.
func EligiblePlaces(label string) bool { return eligiblePlacesAskRE.MatchString(label) }

// startAskRE asks when the applicant can start: "when would you be able to
// start", "how soon can you start", "when are you able to start", "when are you
// available for full time work", an earliest or available start date, a
// bare "Available from", "Availability date" or "Date available" label, or a
// notice period. Availability for an interview is no start.
var startAskRE = lazyregexp.New(`(?i)\b(?:when|how\s+soon)\s+(?:would|could|can|will|are)\s+you\s+(?:be\s+)?(?:(?:able|available|ready)\s+to\s+)?start\b|\bwhen\s+(?:are|would|will|could)\s+you\s+(?:be\s+)?available\s+for\s+(?:full[- ]?time\s+)?(?:work|employment)\b|\b(?:earliest|soonest|available|possible|desired)\s+(?:possible\s+)?start(?:ing)?\s+date\b|^\W*(?:start\s+date|available\s+from|availability\s+date|date\s+(?:available|of\s+availability))\W*$|\bnotice\s+period\b`)

// StartAnswer reports a question about when the applicant can start. Written
// from the record, the answer is the earliest start date's, which no model is
// shown: the review read Babylist's "Immediately available" against the
// current employment as a contradiction. A model's start is still reviewed.
func StartAnswer(label string) bool { return startAskRE.MatchString(label) }

// ageAskRE asks the applicant's age: how old they are, their current age, an
// age range or bracket, or whether they are at least an age.
var ageAskRE = lazyregexp.New(`(?i)\bhow\s+old\b|\b(?:your|current)\s+age\b|^\W*age(?:\s+(?:range|bracket|group))?\W*$|\bage\s+(?:range|bracket|group)\b|\b(?:at\s+least|over|older\s+than|above|under|younger\s+than)\s+(?:the\s+age\s+of\s+)?\d{2}\b`)

// AgeAnswer reports a question about the applicant's age. Written from the
// record, the answer is the date of birth's, which no model is shown: the
// review read Sierra's "29" against the education dates as a contradiction.
// A model's age is still reviewed.
func AgeAnswer(label string) bool { return ageAskRE.MatchString(label) }

var (
	// schoolAskRE asks which school the applicant attended or graduated from
	// (Reducto: "What institution(s) did you graduate from?", the schools
	// whose education has ended), or is a bare school label
	// ("College/University", "Most recent school").
	schoolAskRE = lazyregexp.New(`(?i)^\W*(?:(?:most\s+recent|current|last)\s+)?(?:school|college|university|institution)(?:\s*(?:/|or)\s*(?:school|college|university))?(?:\s+name)?\W*$|\b(?:what|which)\s+(?:school|college|universit(?:y|ie)|institution)s?(?:\s*\((?:s|ies)\))?(?:\s*(?:/|or)\s*(?:school|college|university))?\s+(?:did|do|have)\s+you\s+(?:attend|go\s+to|graduat\w*\s+from)\b|\bname\s+of\s+(?:your\s+)?(?:school|college|university)\b`)
	// schoolLevelRE names the degree level whose school is asked: a wrong pick
	// between the recorded schools is then an error the review can see.
	schoolLevelRE = lazyregexp.New(`(?i)\b(?:under)?grad(?:uate)?\b|\bbachelor|\bmaster|\bph\.?\s?d\b|\bdoctor|\bhigh\s+school\b|\bsecondary\b|\bmba\b|\b(?:law|medical|business)\s+school\b`)
	// graduateFromRE is the verb "graduate(d) from", no degree level.
	graduateFromRE = lazyregexp.New(`(?i)\bgraduat\w*\s+from\b`)
)

// SchoolAnswer reports a question that asks which school the applicant
// attended and names no degree level. Written from the record, its answer is
// one of the recorded schools: the review read Decagon's "What
// college/university did you attend?" with Carver University (the most recent)
// as a contradiction because Westbridge was attended too. A question that
// names a level is still reviewed.
func SchoolAnswer(label string) bool {
	return schoolAskRE.MatchString(label) && !schoolLevelRE.MatchString(graduateFromRE.ReplaceAllString(label, ""))
}

// degreeAskRE asks only for the applicant's degree or highest level of
// education ("Degree", "Highest degree obtained", "What is the highest level
// of education you have completed?"); a degree in progress or expected is
// another question.
var degreeAskRE = lazyregexp.New(`(?i)^\W*(?:(?:highest|most\s+recent)\s+)?(?:degree|degree\s+level|level\s+of\s+education|education(?:al)?\s+level)(?:\s+(?:obtained|earned|completed|attained))?\W*$|\bwhat\s+is\s+(?:the|your)\s+highest\s+(?:degree|level\s+of\s+(?:education|study))\b(?:\s+(?:you\s+have\s+)?(?:completed|obtained|earned|attained))?\W*$`)

// DegreeAnswer reports a question that asks only the applicant's degree level.
// Written from the record, the answer is the highest education whose end date
// has passed, which the matcher reads against today's date: the review twice
// read Applied Intuition's "Degree" = "Master's degree" for an M.S. that ended
// six months before as a contradiction ("a master's whose end date has not yet
// passed"). A model's degree is still reviewed, and so is one in a repeated
// education entry, which may sit beside the wrong school.
func DegreeAnswer(label string) bool { return degreeAskRE.MatchString(label) }

// fieldAskRE asks only for the applicant's major or field of study ("What was
// your major/minor?", "Field of Study").
var fieldAskRE = lazyregexp.New(`(?i)\bwhat\s+(?:was|is)\s+your\s+(?:academic\s+)?(?:major(?:\s*/\s*minor)?|field\s+of\s+study|area\s+of\s+study)\s*[?:.*]*\s*$|^\W*(?:academic\s+)?(?:major(?:\s*/\s*minor)?|field\s+of\s+study|area\s+of\s+study)\W*$`)

// FieldAnswer reports a question that asks the applicant's major or field of
// study and names no degree level. Typed into a text box from the record it is
// the user's own field entry: the review read Pogo's "What was your
// major/minor?" = "Computer Engineering" (the master's field) as wrong against
// the bachelor's major, as it read the most recent school against an earlier
// one (SchoolAnswer). A question that names a level, and a pick from a list,
// are still reviewed.
func FieldAnswer(label string) bool {
	return fieldAskRE.MatchString(label) && !schoolLevelRE.MatchString(label)
}

var (
	gpaAskRE = lazyregexp.New(`(?i)\bgpa\b|\bgrade\s+(?:point\s+)?average\b`)
	// gpaBracketRE is one of a form's grade-average brackets: "3.1 - 3.5",
	// "3.5+", "3.0 or under", "Below 3.0".
	gpaBracketRE = lazyregexp.New(`(?i)^\s*(?:\d(?:\.\d+)?\s*(?:[-\x{2013}\x{2014}]|to)\s*\d(?:\.\d+)?|\d(?:\.\d+)?\s*(?:\+|or\s+(?:under|below|less|lower|above|higher|more|over))|(?:under|below|above|over|less\s+than|more\s+than)\s+\d(?:\.\d+)?)\s*$`)
)

// GPABracketAnswer reports a grade-average question answered with one of the
// form's brackets. Written from the record, the bracket holds the most recent
// degree's average (or that of the level the question names); the review is
// shown every degree's average but not the rule, and read Verkada's "3.1 -
// 3.5" for a 3.4 master's as a contradiction twice, once saying 3.4 lay
// outside it. A model's bracket is still reviewed.
func GPABracketAnswer(label, value string) bool {
	return gpaAskRE.MatchString(label) && gpaBracketRE.MatchString(value)
}

var (
	// presenceAskRE asks where the applicant is now (Collective: "Are you
	// currently based in the San Francisco Bay Area and able to work from our
	// San Francisco office at least 3 days per week?"); the present tense alone
	// asks it too (Ivo: "Are you located near one of Ivo's US Offices?").
	presenceAskRE = lazyregexp.New(`(?i)\b(?:are|do)\s+you\s+(?:(?:currently|presently|now)\s+)?(?:based|located|living|live|reside|residing)\s+(?:in|out\s+of|near|within)\b`)
	// relocationReasonRE is a review reason that rests on a move;
	// presenceReasonRE one that says where the applicant is.
	relocationReasonRE = lazyregexp.New(`(?i)\brelocat(?:e|ed|ion|ing)\b`)
	presenceReasonRE   = lazyregexp.New(`(?i)\b(?:lives?|living|based|located|resides?|residing|address)\b`)
)

// RelocationAgainstPresence reports a flag on a No to where the applicant is
// now whose reason rests on a willingness to relocate and says nothing of
// where the applicant is: moving later is no presence now.
func RelocationAgainstPresence(reason, label, value string) bool {
	return strings.EqualFold(strings.Trim(value, " ."), "no") && presenceAskRE.MatchString(label) &&
		relocationReasonRE.MatchString(reason) && !presenceReasonRE.MatchString(reason)
}

var (
	// pastWorkAskRE asks how the applicant has worked before (TRM: "Have you
	// previously worked in a remote or hybrid environment?").
	pastWorkAskRE = lazyregexp.New(`(?i)\bhave\s+you\s+(?:ever\s+|previously\s+|already\s+)?(?:worked|been\s+working)\b`)
	// preferenceReasonRE is a review reason that rests on how the applicant
	// prefers or accepts to work; pastWorkReasonRE one that names a past job.
	preferenceReasonRE = lazyregexp.New(`(?i)\bprefer(?:s|red|ences?)?\b|\bwork\s+modes?\b|\b(?:willing|open)\s+to\b|\baccepts?\b`)
	pastWorkReasonRE   = lazyregexp.New(`(?i)\b(?:worked|works|working)\s+(?:remotely|from\s+home|hybrid)\b|\b(?:remote|hybrid)\s+(?:role|job|position|work)\s+at\b|\bemploy`)
	// negatedPastWorkRE is the No restated in a reason ("the answer that they
	// have never worked remotely"), which names no past job.
	negatedPastWorkRE = lazyregexp.New(`(?i)(?:\bnever|\bnot|n['\x{2019}]t)\s+(?:ever\s+|previously\s+|before\s+)?(?:worked|works|working)\s+(?:remotely|from\s+home|hybrid)\b`)
)

// PreferenceAgainstPast reports a flag on a No to how the applicant has worked
// before whose reason rests on how they prefer to work and names no past job:
// a preference now is no past arrangement.
func PreferenceAgainstPast(reason, label, value string) bool {
	return strings.EqualFold(strings.Trim(value, " ."), "no") && pastWorkAskRE.MatchString(label) &&
		preferenceReasonRE.MatchString(reason) && !pastWorkReasonRE.MatchString(negatedPastWorkRE.ReplaceAllString(reason, ""))
}

// roleResidenceAskRE asks whether the applicant lives in the role's own
// location; namedResidenceAskRE asks whether they live in places the question
// names (Gigs: "Are you currently based in New York or San Francisco, ...?").
var (
	roleResidenceAskRE  = lazyregexp.New(`(?i)\b(?:reside|residing|live|living|located|based)\s+(?:in|within)\s+(?:the\s+location\s+(?:(?:specified|listed|stated|posted|indicated|given)\s+)?(?:for|of)\s+(?:this|the)\s+(?:role|position|job|opening)|(?:the\s+(?:location|area)\s+)?where\s+(?:this|the)\s+(?:role|position|job)\s+is\s+(?:located|based))`)
	namedResidenceAskRE = lazyregexp.New(`(?i)^\W*(?:are|do)\s+you\s+(?:currently\s+|presently\s+)?(?:based|located|living|live|residing|reside)\s+(?:in|out\s+of)\b`)
)

var (
	// relocationNeedAskRE asks whether the applicant needs to relocate for the
	// role (the matcher's requires-relocation route); relocationBenefitRE is a
	// relocation benefit or an "or local" choice such a question may ask about
	// instead.
	relocationNeedAskRE = lazyregexp.New(`(?i)\b(?:will|would|do|does|might)\s+you\s+(?:currently\s+)?(?:require|need)\b[^?]{0,20}?\brelocat(?:e|ion|ing)\b`)
	relocationBenefitRE = lazyregexp.New(`(?i)\brelocation\s+(?:assistance|package|support|bonus|benefits?)\b|\bassist(?:ance)?\s+with\s+relocat\w*|\bor\b[^?]{0,40}\blocal\b`)
)

// yesNoRelocateRE is a Yes or No that also says whether the applicant would
// relocate (Orum: "Yes or willing to relocate", "No and not willing to
// relocate"), as the matcher picks it from the location policy.
var yesNoRelocateRE = lazyregexp.New(`(?i)^\W*(?:yes|no)\b[\s,;-]*(?:(?:or|and|but)\s+)?(?:i\s+am\s+|i['\x{2019}]m\s+)?(?:not\s+)?(?:willing|open|planning|able)\s+to\s+relocate\W*$`)

// RoleResidenceAnswer reports a Yes or No to living in the role's own
// location, or to needing to relocate there. Written from the record, it
// compares the recorded address with the posting's place; the review is shown
// both and read Nebius's "Yes" for a Charleston, SC address against a role in
// the "United States" as a contradiction, Gigs's "No" to living in New York or
// San Francisco as one for the same address, Orum's "Yes or willing to
// relocate" as one because willingness is not presence, and Lightfield's "Yes" to "Do you
// require relocation to the SF Bay Area?" as one because San Francisco is a
// preferred destination. A model's answer is still reviewed.
func RoleResidenceAnswer(label, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return (v == "yes" || v == "no" || yesNoRelocateRE.MatchString(v)) && (roleResidenceAskRE.MatchString(label) || namedResidenceAskRE.MatchString(label) ||
		relocationNeedAskRE.MatchString(label) && !relocationBenefitRE.MatchString(label))
}

// travelShareAskRE is a question that states the share of time the role
// travels.
var travelShareAskRE = lazyregexp.New(`(?i)\btravell?(?:ing|s)?\b[^?]{0,120}?\b\d{1,3}\s*%|\b\d{1,3}\s*%[^?]{0,60}\btravell?(?:ing|s)?\b`)

// TravelShareAnswer reports a Yes or No to a question that states the share of
// time the role travels. Written from the record, it holds that share against
// the recorded ceiling; the review is shown the ceiling and read n8n's "No" to
// "travelling approximately 30–50% of the time" against "up to 25%" as a
// contradiction, its own reason saying the ceiling is below the share. A
// model's answer is still reviewed.
func TravelShareAnswer(label, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return (v == "yes" || v == "no") && travelShareAskRE.MatchString(label)
}

var (
	graduationAskRE  = lazyregexp.New(`(?i)\bgraduat\w*\b`)
	graduationTermRE = lazyregexp.New(`(?i)^\s*(?:(?:fall|autumn|spring|summer|winter)\s+(?:of\s+)?\d{4}|(?:(?:later|earlier)\s+than|before|after|prior\s+to)\s+(?:(?:fall|autumn|spring|summer|winter)\b|\d{4}\b))`)
	// graduatedRE is an option saying the applicant has graduated or is not
	// studying, the graduation lane's pick once every education has ended.
	graduatedRE = lazyregexp.New(`(?i)\balready\s+graduat|\bhave\s+graduated\b|^\s*graduated\s*\.?\s*$|\bnot\b[^,.;]*\b(?:pursu\w*|enroll\w*|attend\w*|stud\w*|working\s+toward\w*|in\s+school)\b`)
)

// GraduationAnswer reports a graduation question answered with a term from the
// form's list ("Fall 2026 (September-December)") or with its option for a
// finished degree ("I am not currently studying"). The graduation lane picks
// either from the dated education entries, and for a finished degree on a list
// that offers only later terms the owner's rule takes the earliest; the review
// sees neither the list nor the rule and read Decagon's "Fall 2026" for a May
// 2026 graduate as a contradiction three times, P&G's "not currently
// studying" as one against a posting for later graduates, and Cursor's
// "Before spring 2027" for the same graduate as a finished degree that "does
// not fit".
func GraduationAnswer(label, value string) bool {
	return graduationAskRE.MatchString(label) && (graduationTermRE.MatchString(value) || graduatedRE.MatchString(value))
}

// graduationDateRE is a term or a year named in a graduation question.
var graduationDateRE = lazyregexp.New(`(?i)\b(?:fall|autumn|spring|summer|winter)\b|\b(?:19|20)\d{2}\b`)

// GraduationYesNo reports a Yes or No to a graduation question that names a
// term or a year (Scale AI: "I confirm that my graduation date will be either
// Fall 2026 or Spring 2027" = No for a May 2026 graduate). The graduation lane
// reads it from the dated education entries; written from the record it is
// not reviewed, as the review flagged that No while its own reason said the
// date is in neither term.
func GraduationYesNo(label, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return (v == "yes" || v == "no") && graduationAskRE.MatchString(label) && graduationDateRE.MatchString(label)
}

// enrollmentAskRE asks whether the applicant is a student now.
var enrollmentAskRE = lazyregexp.New(`(?i)\benrolled\b|\bcurrently\s+(?:a\s+)?student\b|\bcurrently\s+in\s+(?:full[-\s]?time\s+)?(?:education|school|college|university)\b`)

// EnrollmentYesNo reports a Yes or No to whether the applicant is enrolled
// (Reducto: "Are you currently enrolled in Full time Education?" = No for a
// May 2026 graduate). The record's latest education end month against the
// clock decides it; written from the record it is not reviewed, as the review
// flagged that No while its own reason said the applicant is not enrolled.
func EnrollmentYesNo(label, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return (v == "yes" || v == "no") && enrollmentAskRE.MatchString(label)
}

// residenceRE is a label that asks only for a part of the applicant's own
// address: a country, state, city, county, postal code, street line or a bare
// location, its primary one too ("Primary Location", Infinitus), or the whole
// ask "What is your location?" (incident.io).
var residenceRE = lazyregexp.New(`^(?:what is your (?:current )?location\?|(?:your |current |home |primary )?(?:country(?: ?(?:/|or) ?region| of residence)?|state(?: ?(?:/|or) ?(?:province|territory|region))?|province|city(?: ?(?:/|or) ?town)?|town|county|zip(?: ?(?:/|or) ?postal)?(?: code)?|postal code|postcode|(?:street |mailing |residential )?address(?: line ?[0-9])?|location(?: \(city(?:, state)?\))?))$`)

// Residence reports a label that asks only for a part of the applicant's own
// address. The review is shown no address fact, so it could only guess at one
// (Ripple: "Country" = United States flagged from the applicant's schools).
func Residence(label string) bool {
	l := strings.ToLower(strings.Join(strings.Fields(label), " "))
	l = strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(l, "*: "), "(required)"))
	return residenceRE.MatchString(strings.TrimRight(l, "*: "))
}

// clearanceRE is a question about a security clearance the applicant holds,
// held or may obtain.
var clearanceRE = lazyregexp.New(`(?i)\bsecurity\s+clearances?\b|\bclearance\s+(?:eligibility|level|status)\b|\b(?:ts/sci|public\s+trust)\b`)

// Clearance reports a question about a security clearance. Holding or
// obtaining one rests on citizenship and on the user's own record, neither of
// which the review is shown (Nominal: "No" for an F-1 student flagged because
// "the profile ... lists U.S. education and employment").
func Clearance(label string) bool { return clearanceRE.MatchString(label) }

// governmentEmploymentRE is a question about having worked for a government or
// another public employer.
var governmentEmploymentRE = lazyregexp.New(`(?i)\b(?:employ\w*|worked|work\s+for)\b[^?]*\b(?:government|state[-\s]owned|publicly[-\s]funded|public[-\s]sector)\b|\bgovernment\s+(?:employee|employment|service)\b`)

// GovernmentEmployment reports a question about government or other public
// employment. Whether a past employer counts is the user's own reading,
// recorded in the bank; the review sees only the profile's employer names and
// could only guess (Snowflake: the owner's No flagged because the profile
// lists a research internship at a publicly funded institute).
func GovernmentEmployment(label string) bool { return governmentEmploymentRE.MatchString(label) }

// citizenshipRE is a question about the applicant's citizenship or nationality.
var citizenshipRE = lazyregexp.New(`(?i)\bcitizen(?:s|ship)?\b|\bnationality\b`)

// Citizenship reports a question about citizenship, which the review is not
// shown (GRVTY: "Are you a U.S. citizen?" answered No from the recorded status
// for an applicant with a U.S. address and U.S. degrees).
func Citizenship(label string) bool { return citizenshipRE.MatchString(label) }

// exportControlRE is a question or answer about export-control status: being a
// "U.S. person" under ITAR or the EAR.
var exportControlRE = lazyregexp.New(`(?i)\bexport\s+(?:control|compliance)\b|\bitar\b|\bu\.?\s?s\.?\s+persons?\b`)

// ExportControl reports an export-control status, named by the label or by
// the value. Being a "U.S. person" rests on citizenship or permanent
// residence, which the review is not shown, and living in the U.S. is not it
// (Relativity: "I am not a U.S. Person" for an F-1 applicant flagged because
// "the profile lists the applicant as based in the United States").
func ExportControl(label, value string) bool {
	return exportControlRE.MatchString(label) || exportControlRE.MatchString(value)
}

var (
	bareAnswerRE  = lazyregexp.New(`(?i)^\s*(?:yes|no|y|n|true|false)\s*\.?\s*$`)
	asksSpecifyRE = lazyregexp.New(`(?i)\b(?:specify|describe|explain|elaborate|list|which one|own words)\b|\bif you (?:selected|answered|chose|checked|said|indicated)\b`)
	// asksWhetherRE is a specify verb that asks whether, which a bare yes or no
	// answers (Cadence: "Please list if you have ever worked under another
	// name", its names asked by the next box).
	asksWhetherRE = lazyregexp.New(`(?i)\b(?:specify|list)\s+(?:if|whether)\b`)
	// ownQuestionThenIfYesRE is a box that asks its own question and wants
	// detail only of a Yes (Maven Clinic: "Are you currently employed by ...?
	// If so, please list current role."): a bare No answers it whole. The
	// detail may sit in brackets (Scale AI: "...employment? (If yes, please
	// describe.)").
	ownQuestionThenIfYesRE = lazyregexp.New(`(?i)\?\s*[(\[]?\s*if\s+(?:yes|so)\b`)
	bareNoRE               = lazyregexp.New(`(?i)^\s*(?:no|n|false)\s*\.?\s*$`)
	// ownQuestionThenExplainNoRE asks to explain only a No (Relativity Space:
	// "... willing to commute and/or relocate for this role? If not, please
	// explain:"): a bare Yes answers it whole.
	ownQuestionThenExplainNoRE = lazyregexp.New(`(?i)\?\s*[(\[]?\s*if\s+(?:not|no)\s*,?\s*(?:please\s+)?(?:explain|elaborate|tell\s+us\s+why)\b[^?]*$`)
	bareYesRE                  = lazyregexp.New(`(?i)^\s*(?:yes|y|true)\s*\.?\s*$`)
	// A bare number never answers a question that asks why, unless it also
	// asks for a count (Color: "which of the three possible projects ...
	// resonates the most and why?" held "4").
	bareNumberRE = lazyregexp.New(`^\s*\d+(?:\.\d+)?\s*$`)
	asksWhyRE    = lazyregexp.New(`(?i)\bwhy\b`)
	asksCountRE  = lazyregexp.New(`(?i)\bhow\s+(?:many|much|long)\b|\bnumber\s+of\b`)
	// missingDataRE speaks of what the model was given instead of answering
	// (Evertune: "the information available here does not document a specific
	// system's architecture ..., so I cannot accurately provide those
	// project-specific details").
	missingDataRE = lazyregexp.New(`(?i)\b(?:no (?:such )?(?:data|information|details?) (?:was |were )?(?:provided|available|given)|(?:i )?(?:do not|don't|cannot|can't) (?:have|find|see) (?:any |enough |the )?(?:information|data|details|context)|not enough (?:information|data|context)|(?:information|data|details) (?:is|are) missing|missing (?:information|data|details)|as an ai|` +
		`(?:information|data|details|facts|notes?) (?:(?:available|provided|given|shown) )?(?:here|to me|above|below) (?:does|do) not (?:document|include|mention|cover|state|show|specify|describe|contain|say|provide)|` +
		`(?:cannot|can't|am unable to) accurately (?:provide|describe|state|answer|share|give))\b`)
)

// ShapeFlag raises the deterministic flags, each with probability 1: a bare
// yes or no written to an option-less text control whose label asks the
// applicant to specify or explain, a bare number to one that asks why, and a
// free-text value that talks about missing data instead of answering. The model's verdict cannot lower them.
func ShapeFlag(r ReviewRow) (Flag, bool) {
	if !r.TextControl {
		return Flag{}, false
	}
	if !r.HasOptions && bareAnswerRE.MatchString(r.Value) && asksSpecifyRE.MatchString(asksWhetherRE.ReplaceAllString(r.Label, "")) &&
		!(bareNoRE.MatchString(r.Value) && ownQuestionThenIfYesRE.MatchString(r.Label)) && !(bareYesRE.MatchString(r.Value) && ownQuestionThenExplainNoRE.MatchString(r.Label)) {
		return Flag{Value: r.Value, Probability: 1, HasProbability: true, Reason: "a bare yes or no where the question asks for detail"}, true
	}
	if !r.HasOptions && bareNumberRE.MatchString(r.Value) && asksWhyRE.MatchString(r.Label) && !asksCountRE.MatchString(r.Label) {
		return Flag{Value: r.Value, Probability: 1, HasProbability: true, Reason: "a bare number where the question asks why"}, true
	}
	if missingDataRE.MatchString(r.Value) {
		return Flag{Value: r.Value, Probability: 1, HasProbability: true, Reason: "it speaks of missing information instead of answering"}, true
	}
	return Flag{}, false
}

// Item names one failing control on one page.
type Item struct {
	Page  int
	Label string
	// WrongPolarity marks a Yes or No answer the page holds the opposite of.
	WrongPolarity bool
	// SitePrefilled marks an identity control the site filled in itself and
	// the run never wrote.
	SitePrefilled bool
	// Refusal is the page's own bound that refused the profile's value
	// ("the page accepts dates from <min> on").
	Refusal string
	// UserStatement marks a required statement about this application that
	// only the user makes (that no AI helped with it), left blank.
	UserStatement bool
	// Why says what the user adds so the control can be answered.
	Why string
}

// LabeledFlag is a flag with the label of the control it concerns.
type LabeledFlag struct {
	Label string
	Flag  Flag
}

// PageReport is what the check learned about one page.
type PageReport struct {
	Number             int
	ReReadFailed       bool
	IdentityMismatches []Item
	RequiredUnanswered []Item
	NotHeld            []Item
	Flags              []LabeledFlag
	// ReviewStatus is "ok", "failed" or "skipped"; a failed or skipped review
	// is recorded and never blocks.
	ReviewStatus string
}

// ReadinessInput is every page report of the run.
type ReadinessInput struct {
	Pages []PageReport
	// Final is true when the walk has ended and every item counts; during the
	// walk required leftovers are left to the walker's own gate.
	Final           bool
	PostingLocation string
	// ReviewAdvisory keeps for the user's review, blocking nothing (the
	// owner's decision), a flag at its blocking floor and a required statement
	// only the user makes left blank: the walk's own page verdicts, so the
	// fill goes on to its next page, and the final verdict of a run that fills
	// only. The final verdict of a run that presses Submit leaves it false, so
	// a doubted value still keeps Submit unpressed.
	ReviewAdvisory bool
}

// Failure is one failing item of the verdict.
type Failure struct {
	Code  string
	Page  int
	Label string
	// WrongPolarity marks a Yes or No answer the page holds the opposite of.
	WrongPolarity bool
	// SitePrefilled marks an identity control the site filled in itself.
	SitePrefilled bool
	// Refusal is the page's own bound that refused the profile's value.
	Refusal string
	// Why is a flag's own reason, said to the user beside the label.
	Why string
}

// ReadinessResult lists every failing item with its page number and label.
type ReadinessResult struct {
	Decision
	Failures []Failure
}

// SubmitReadiness is the one verdict over the page reports. It fails, in this
// precedence, on an unreadable page, an identity mismatch, an unanswered
// required control, a written value the page does not hold (a wrong
// polarity included) and a flag at its blocking floor.
func SubmitReadiness(in ReadinessInput) ReadinessResult {
	var fails []Failure
	add := func(code string, page int, label string) {
		fails = append(fails, Failure{Code: code, Page: page, Label: label})
	}
	for _, p := range in.Pages {
		if p.ReReadFailed {
			add(CodePageUnreadable, p.Number, "page")
		}
		for _, it := range p.IdentityMismatches {
			add(CodeIdentityMismatch, p.Number, it.Label)
			fails[len(fails)-1].SitePrefilled = it.SitePrefilled
		}
		if in.Final {
			for _, it := range p.RequiredUnanswered {
				if in.ReviewAdvisory && it.UserStatement {
					continue
				}
				add(CodeRequiredUnanswered, p.Number, it.Label)
				fails[len(fails)-1].Refusal, fails[len(fails)-1].Why = it.Refusal, it.Why
			}
		}
		for _, it := range p.NotHeld {
			add(CodeValueNotHeld, p.Number, it.Label)
			fails[len(fails)-1].WrongPolarity = it.WrongPolarity
		}
		for _, f := range p.Flags {
			if !in.ReviewAdvisory && !FlagGate(f.Flag, f.Label, in.PostingLocation).Allowed() {
				add(CodeReviewContradiction, p.Number, f.Label)
				fails[len(fails)-1].Why = f.Flag.Reason
			}
		}
	}
	if len(fails) == 0 {
		return ReadinessResult{Decision: decide(GateReadiness, Allow, CodeOK, "every page is verified")}
	}
	top := fails[0].Code
	for _, code := range []string{CodePageUnreadable, CodeIdentityMismatch, CodeRequiredUnanswered, CodeValueNotHeld, CodeReviewContradiction} {
		if hasCode(fails, code) {
			top = code
			break
		}
	}
	class := ClassDefect
	if top == CodeRequiredUnanswered {
		class = ClassAnswerGap
	}
	parts := make([]string, len(fails))
	for i, f := range fails {
		parts[i] = fmt.Sprintf("page %d %q: %s", f.Page, f.Label, failureWords[f.Code])
		if f.Code == CodePageUnreadable {
			parts[i] = fmt.Sprintf("page %d", f.Page)
		}
		if f.WrongPolarity {
			parts[i] += " (the page holds the opposite answer)"
		}
		if f.SitePrefilled {
			parts[i] += " (the site filled it in itself: set your profile's value there, or change your profile)"
		}
		if f.Refusal != "" {
			parts[i] += " (" + f.Refusal + ")"
		}
		if why := strings.TrimRight(strings.TrimSpace(f.Why), ". "); why != "" {
			parts[i] += " (" + clip(why, ReviewValueMaxRunes) + ")"
		}
	}
	reason := strings.Join(parts, "; ")
	if top == CodePageUnreadable {
		reason = "the page could not be re-read, so nothing on it is verified; " + reason
	}
	return ReadinessResult{Decision: decide(GateReadiness, Deny, top, "%s", reason).withClass(class), Failures: fails}
}

// failureWords says each failure code in plain words for the run's detail; the
// code itself stays on the decision for machines.
var failureWords = map[string]string{
	CodeIdentityMismatch:    "does not match your profile",
	CodeRequiredUnanswered:  "required and not answered",
	CodeValueNotHeld:        "the page does not hold the value that was written",
	CodeReviewContradiction: "looks wrong",
}

func hasCode(fs []Failure, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}
