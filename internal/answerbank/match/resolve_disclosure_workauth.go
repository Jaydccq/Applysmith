package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Work authorization is a statement the applicant signs, like sponsorship, so it
// is read with the same closed grammar (resolve_disclosure_polarity.go): a
// question is read only when it is one sentence whose every word is inert or
// structural and whose structural words spell a recognised shape. A fixed
// work-authorization entry holds the user's own Yes or No; a negated question
// ("Are you not authorized ...") is answered with its complement, and a wording
// outside the shapes is left to the user.

const (
	idWorkAuthUS       = "work-auth-us"
	idUnrestrictedWork = "unrestricted-right-to-work-us"
)

// termDefinitionRE is a closing parenthesis that defines a quoted term of the
// question (Armada: "(“Without restriction” means not tied to a specific
// employer and not dependent on a pending or future government filing.)"): it
// says what the term means and asks nothing, so the question is read without it.
var termDefinitionRE = lazyregexp.New(`(?i)\s*\(\s*["\x{201C}'\x{2018}][^"\x{201D}'\x{2019}()]{1,40}["\x{201D}'\x{2019}]\s+means\b[^()]*\)\s*$`)

// authHeadingLeadRE is a heading the page set before the question (RobCo:
// "Legal Eligibility to Work Are you legally authorized ...?"): the question
// is read without it.
var authHeadingLeadRE = lazyregexp.New(`(?i)^\s*(?:legal\s+|u\.?s\.?\s+)?(?:work\s+|employment\s+)?(?:eligibility|authori[sz]ation)(?:\s+to\s+work)?(?:\s+status)?\s*:?\s+((?:are|do|will|can|have|is)\s+you\b)`)

// withoutRestrictionRE is the qualifier that is the unrestricted entry's own
// sense ("authorized to work without restrictions in the country where this
// position is based", Niantic; Baseten's "unrestricted work authorization"):
// read on that entry, the rest is the plain question. On any other entry it
// stays a word outside the shapes.
var withoutRestrictionRE = lazyregexp.New(`(?i)\s+without\s+(?:any\s+)?restrictions?\b|\b(?:unlimited\s+(?:and\s+)?)?unrestricted\s+(?:and\s+unlimited\s+)?`)

var workAuthShapes = func() []*lazyregexp.Regexp {
	const (
		subject = `(?:(?:BE|WILL) YOU|YOU (?:BE|WILL))`
		negTime = `(?: NEG(?: T)?| T(?: NEG)?)?`
	)
	patterns := []string{
		// "Are you [not] [currently] legally authorized to work in the US?",
		// "Is it true that you are eligible to work ...?", "I am authorized ...".
		`^(?:LEAD )?` + subject + negTime + ` ABLE(?: T)?$`,
		// "Can you legally work in the US?"
		`^CAN YOU(?: T)?$`,
		// "Do you [not] have authorization to work ...?", "Do you have the right
		// to work ...?"
		`^(?:DO )?YOU` + negTime + ` (?:HAVE|HOLD) (?:AID|RIGHT)(?: T)?$`,
		// A bare label ("US work authorization", "Legally authorized to work").
		`^(?:ABLE|AID)$`,
	}
	out := make([]*lazyregexp.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = lazyregexp.New(p)
	}
	return out
}()

// The owner's rule: being able to prove work authorization is the
// authorization itself, so a request for proof reads as the plain question.
var (
	proofLead = `^(?:if (?:you are )?(?:hired|selected|offered (?:(?:a|the) (?:position|role|job)(?: (?:with|within|at) [^,?]{1,60})?|employment)),?\s+)?(?:can|could|will|would) you,?\s+(?:(?:upon|on|after) (?:employment|hire|hiring),?\s+)?(?:be able to )?(?:provide|show|present|produce|furnish|submit|offer) (?:proof|documentation|documents|evidence|verification) `
	// The offer lead may name the employment itself (Metropolis: "If offered
	// employment, can you submit verification of legal right to work ...").
	// proofThatRE is "If hired, can you provide proof that you are ...", or
	// documentation "showing you are eligible" (Huntington Ingalls).
	proofThatRE = lazyregexp.New(`(?i)` + proofLead + `(?:(?:showing|proving) )?(?:that )?you are\b`)
	// proofOfRE is "Can you show proof of your right to work ...", and the I-9
	// request for "verification of both your identity and authorization to
	// work" (Axon): proof of identity goes with the authorization.
	proofOfRE = lazyregexp.New(`(?i)` + proofLead + `(?:of|proving|showing) (?:(?:both )?your identity and )?(?:your )?`)
	// proofDeadlineRE is the time the proof is due ("within 3 days of hire"),
	// or the law it is due under ("to the extent required by law").
	proofDeadlineRE = lazyregexp.New(`(?i)\s+(?:within|in) \d+ (?:business |working )?days (?:of|after|from) (?:hire|hiring|starting|your (?:start|hire) date)\b|,?\s+(?:to the extent|as) required by (?:applicable |federal )?law\b`)
	// speaksToRE is a note that addresses the applicant or tells the answer.
	speaksToRE = lazyregexp.New(`(?i)\b(?:your?|yes|no|not|select|answer|choose|pick|check|mark|indicate|respond)\b`)
	// checkNounRE is "check" as a noun after an article (Peloton's "a right to
	// work check such as US Form I-9"), not the instruction to check a box.
	checkNounRE = lazyregexp.New(`(?i)\b(?:a|an|the)\s+(?:[\w-]+\s+){0,4}check\b`)
	notWordRE   = lazyregexp.New(`(?i)\bnot\b`)
	// notLimitedToRE opens a list of examples (Aledade: "This includes, but is
	// not limited to, H-1B visas ..."): its "not" negates nothing asked.
	notLimitedToRE = lazyregexp.New(`(?i)\bnot\s+limited\s+to\b`)
	// equalOpportunityRE is the nondiscrimination boilerplate a note may end
	// with ("not on an applicant's national origin"): its "not" negates a basis
	// for decisions, never the question.
	equalOpportunityRE = lazyregexp.New(`(?i)\b(?:national origin|protected (?:characteristics?|class(?:es)?|status)|without regard to)\b`)
	// placeNoteRE is a note that only says the answer is the one for the
	// country the role is in (Booz Allen: "If this role is located in the
	// United States, answer 'Yes/No' based on your authorization to work in the
	// United States."), the country the question already reads.
	// placeLeadRE is a lead clause that places the applicant ("If located in the
	// US, are you currently authorized to work in the US?"; SpaceXSI: "If
	// working in the US, will you now, or in the future, require sponsorship
	// ...?"): the answer is the user's authorization or need in the country the
	// question names, wherever they live, so the lead is read as absent; so is
	// a lead addressing the applicants based there (Stedi: "For United
	// States-based applicants: Are you currently authorized to work in the
	// US?"), never those based elsewhere, and a lead placing the role (Extreme
	// Networks: "If the position you are applying for is in the United States,
	// are you authorized to work lawfully in the United States ...?").
	placeLeadRE = lazyregexp.New(`(?i)^if\s+(?:you\s+are\s+|you're\s+)?(?:currently\s+)?(?:located|based|living|residing|working|employed)\s+in\s+[^,?]{1,40},\s*|^if\s+(?:the|this)\s+(?:role|position|job)(?:\s+(?:you\s+are|you're)\s+applying\s+(?:for|to))?\s+is\s+(?:located\s+|based\s+)?in\s+[^,?]{1,40},\s*|^for\s+(?:(?:the\s+)?(?:U\.?S\.?|USA|United\s+States)[- ]based|(?:applicants|candidates)\s+(?:based|located|living|residing)\s+in\s+(?:the\s+)?(?:U\.?S\.?|USA|United\s+States))(?:\s+(?:applicants|candidates))?\s*[,:]\s*`)
	// citizenNoteRE is a note that a citizen of the role's country needs no
	// sponsorship (Bitdeer: "Generally, if you are a citizen of the country
	// where the job is located at - you will not require sponsorship of an
	// employment visa."): it states what the recorded need already says.
	citizenNoteRE = lazyregexp.New(`(?i)^(?:(?:generally|typically|usually|in\s+general|note)[,:]?\s+)?if\s+you\s+are\s+an?\s+citizen\s+of\s+the\s+country\s+(?:where|in\s+which)\s+(?:the|this)\s+(?:job|role|position)\s+is\s+(?:located|based)(?:\s+(?:at|in))?\s*[-,\x{2013}\x{2014}]?\s*you\s+(?:will|would|do)\s+not\s+(?:require|need)\s+(?:visa\s+)?sponsorship\b[^.?!]*\.?$`)
	// reassuranceNoteRE is a note that an answer costs the applicant nothing
	// (Rogo: "Responding “Yes” to this question will not preclude you from
	// consideration for employment."): it names an answer but tells none.
	reassuranceNoteRE = lazyregexp.New(`(?i)^(?:(?:please\s+)?note\s*:\s*)?(?:(?:responding|answering|selecting|choosing)\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?(?:yes|no)["'\x{2018}\x{2019}\x{201C}\x{201D}]?|an?\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?(?:yes|no)["'\x{2018}\x{2019}\x{201C}\x{201D}]?\s+(?:answer|response)|your\s+(?:answer|response))\s+(?:to\s+this\s+question\s+)?(?:will|does|would)\s+not\s+(?:automatically\s+|necessarily\s+)?(?:preclude|disqualify|exclude|eliminate|bar|remove|affect|impact)\s+(?:you|your)\b[^.?!]*[.!]?$`)
	// proofNoteRE is a note that proof of the authorization will be asked for
	// (SpotHero: "Proof of eligibility will be required before you can be
	// employed."): it asks nothing and tells no answer.
	proofNoteRE = lazyregexp.New(`(?i)^(?:(?:please\s+)?note\s*:\s*)?(?:proof|evidence|verification|documentation)\s+of\s+(?:your\s+)?(?:(?:employment|work)\s+)?(?:eligibility|authori[sz]ation)(?:\s+to\s+work)?\s+(?:will|may)\s+be\s+(?:required|requested|needed)\b[^.?!]*[.!]?$`)
	// sponsorNoteRE is the employer saying it sponsors (IEX: "(If not, don't
	// worry - we do sponsor visas, but it's helpful to know sooner rather than
	// later!)"): it reassures and asks nothing.
	sponsorNoteRE = lazyregexp.New(`(?i)^\(?\s*(?:if\s+(?:not|no)\s*,?\s*)?(?:(?:please\s+)?(?:don['\x{2019}]?t|do\s+not)\s+worry\s*[-\x{2013}\x{2014},]?\s*)?(?:we|the\s+company)\s+(?:do\s+|can\s+|will\s+|may\s+)?(?:sponsor|offer\s+(?:visa\s+)?sponsorship|provide\s+(?:visa\s+)?sponsorship)\b[^.?!]*[.!]?\s*\)?$`)
	placeNoteRE   = lazyregexp.New(`(?i)^if\s+(?:this|the)\s+(?:role|position|job)\s+is\s+(?:not\s+)?(?:located|based)\s+in\s+[^,.?!]{1,60},\s+(?:please\s+)?answer\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?yes\s*/\s*no["'\x{2018}\x{2019}\x{201C}\x{201D}]?\s+based\s+on\s+your\s+(?:authori[sz]ation\s+to\s+work|need\s+for\s+sponsorship(?:\s+to\s+work)?)\s+in\s+[^.?!]{1,80}[.]?$`)
)

// proofAlternativeRE is proof offered as the alternative to the authorization
// (Tanium: "Are you lawfully authorized to work in the US or can you provide
// proof of employment eligibility upon hire?"): by the owner's rule above, the
// authorization itself, so it is read as absent.
var proofAlternativeRE = lazyregexp.New(`(?i)\s+or\s+(?:can|could|will|would)\s+you\s+(?:be\s+able\s+to\s+)?(?:provide|show|present|produce|furnish|submit)\s+(?:proof|documentation|documents|evidence|verification)\s+of\s+(?:your\s+)?(?:employment\s+eligibility|eligibility\s+to\s+work|work\s+authori[sz]ation|authori[sz]ation\s+to\s+work)(?:\s+(?:upon|on|after)\s+(?:hire|hiring|employment))?`)

// proofRequirementRE states the proof required and then asks for "this
// documentation" (15Five: "We require that candidates provide proof of their
// legal right to work in the United States after hiring; will you be able to
// provide this documentation after you are hired?"): the request for proof the
// owner's rule reads, said in two clauses. The group is what the proof is of.
var proofRequirementRE = lazyregexp.New(`(?i)^\s*we\s+require\s+(?:that\s+)?(?:all\s+)?(?:candidates|applicants|you|new\s+hires)\s+(?:to\s+)?provide\s+proof\s+of\s+(?:their|your)\s+((?:legal\s+)?(?:right|eligibility|authori[sz]ation)\s+to\s+work\s+in\s+[^;.?]{1,60}?)(?:\s+(?:after|upon|on)\s+(?:hire|hiring|employment))?\s*[;.]\s*(?:will|can|could|would)\s+you\s+(?:be\s+able\s+to\s+)?provide\s+(?:this|that|such|the)\s+(?:documentation|proof|evidence)(?:\s+(?:after|once|when)\s+you\s+are\s+hired|\s+(?:upon|on|after)\s+(?:hire|hiring))?\s*\?\s*$`)

// confirmLeadRE asks the applicant to confirm the question's statement
// (Snowflake: "could you confirm you are currently authorized ..."): the
// statement asked as "is it true that". forPurposeLeadRE says what the
// company asks it for ("For Snowflake to anticipate possible immigration
// timelines and obligations, ..."), which leaves its polarity as it was.
var (
	confirmLeadRE    = lazyregexp.New(`(?i)^\s*(?:(?:can|could|would|will)\s+you\s+(?:please\s+)?|please\s+)confirm\s+(?:that\s+)?(you\s+are\b)`)
	forPurposeLeadRE = lazyregexp.New(`(?i)^\s*for\s+(?:us|the\s+company|[\pL&.-]+(?:\s+[\pL&.-]+)?)\s+to\s+(?:anticipate|plan|prepare|understand|assess|evaluate|process|determine|comply)\b[^,?]{0,120},\s*`)
)

// workAuthPolarity reads a work-authorization question.
func workAuthPolarity(text, company string) (negated, ok bool) {
	text = authHeadingLeadRE.ReplaceAllString(termDefinitionRE.ReplaceAllString(text, ""), "$1")
	text = confirmLeadRE.ReplaceAllString(forPurposeLeadRE.ReplaceAllString(text, ""), "Is it true that $1")
	text = proofRequirementRE.ReplaceAllString(text, "Will you be able to provide proof of your $1?")
	q := placeLeadRE.ReplaceAllString(questionBeforeNote(withoutPreamble(proofAlternativeRE.ReplaceAllString(immigrationLawRE.ReplaceAllString(text, ""), ""))), "")
	if proofThatRE.MatchString(q) || proofOfRE.MatchString(q) {
		q = proofDeadlineRE.ReplaceAllString(q, "")
		q = proofOfRE.ReplaceAllString(proofThatRE.ReplaceAllString(q, "Are you"), "Do you have ")
	}
	return closedReading(q, company, workAuthShapes)
}

// questionBeforeNote is the one question of a text whose question is followed
// by a note that neither speaks to the applicant nor tells the answer ("In
// compliance with federal law, all persons hired will be required to verify
// ..."); any other text is returned as it is.
func questionBeforeNote(text string) string {
	sentences := sentencesOf(text)
	at := -1
	for i, s := range sentences {
		if strings.HasSuffix(s, "?") {
			if at >= 0 {
				return text
			}
			at = i
		}
	}
	if at < 0 || at == len(sentences)-1 {
		return text
	}
	for _, s := range sentences[at+1:] {
		if speaks(s) {
			return text
		}
	}
	return sentences[at]
}

// speaks reports a note sentence that addresses the applicant or names an
// answer. The "not" of equal-opportunity boilerplate does neither, and a note
// that only places the answer in the role's country, says a citizen of it
// needs no sponsorship, or that the company may not sponsor (Legora), adds
// nothing to answer.
func speaks(sentence string) bool {
	if t := strings.TrimSpace(sentence); placeNoteRE.MatchString(t) || citizenNoteRE.MatchString(t) || reassuranceNoteRE.MatchString(t) || proofNoteRE.MatchString(t) || sponsorNoteRE.MatchString(t) ||
		noticeRE.MatchString(t) && !noticeBlockRE.MatchString(t) {
		return false
	}
	if equalOpportunityRE.MatchString(sentence) {
		sentence = notWordRE.ReplaceAllString(sentence, " ")
	}
	sentence = notLimitedToRE.ReplaceAllString(sentence, " ")
	return speaksToRE.MatchString(checkNounRE.ReplaceAllString(sentence, " "))
}

// workAuthFixed is the closed-shape gate of a fixed work_authorized entry. It
// runs before the literal answer: a plain question takes the user's answer, a
// negated one its complement, anything else abstains.
func workAuthFixed(s *Step, e *Entry) (Draft, bool) {
	if e.EffectiveAnswerKey() != "work_authorized" {
		return Draft{}, false
	}
	if d, ok := workAuthCompound(s, e); ok {
		return d, true
	}
	if d, ok := answerLedOption(s, e); ok {
		return d, true
	}
	stored := ownText(e)
	if stored == "" {
		return Draft{}, false
	}
	if d, ok := workAuthBehindNotice(s, e, stored); ok {
		return d, true
	}
	if d, ok := workAuthIndefinite(s, e, stored); ok {
		return d, true
	}
	if d, ok := workAuthResiding(s, e, stored); ok {
		return d, true
	}
	text := s.Q.Text
	if e.ID == idUnrestrictedWork {
		text = withoutRestrictionRE.ReplaceAllString(text, "")
	}
	negated, ok := workAuthPolarity(text, s.Req.Posting.Company)
	if !ok {
		return abstainDraft(ReasonAmbiguous, "", "the work-authorization wording is not a recognised shape: left for review"), true
	}
	if !negated {
		return Draft{}, false
	}
	var complement string
	switch normalizeLabel(stored) {
	case "yes":
		complement = "No"
	case "no":
		complement = "Yes"
	default:
		return abstainDraft(ReasonAmbiguous, "", "a negated work-authorization question has no complement of %q", stored), true
	}
	return bindEntryAnswer(s, e, complement, "work authorization negated phrasing -> "+complement), true
}

var (
	// indefiniteRE asks for authorization with no end ("for an indefinite
	// period of time"); only authorization that never needs sponsorship has none.
	// Navan: "on an ongoing indefinite basis".
	indefiniteRE = lazyregexp.New(`(?i),?\s+(?:for\s+an?\s+indefinite\s+period(?:\s+of\s+time)?|on\s+an?\s+(?:ongoing\s+)?indefinite\s+basis|indefinitely)\b`)
	// fullTimeBasisRE is "on a full-time basis", which authorization with no end
	// and no sponsorship always covers.
	fullTimeBasisRE = lazyregexp.New(`(?i),?\s+on\s+a\s+full[\s-]?time\s+basis\b`)
	// forEmployerJoinRE is a second clause naming the employer (Navan: "...
	// for this position, and authorized to work for Navan on an ongoing
	// indefinite basis?"): the same authorization, asked of the employer.
	forEmployerJoinRE = lazyregexp.New(`(?i),?\s+and\s+(?:(?:legally|lawfully)\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work\s+for\s+[^,?.]{1,40}`)
)

// residingAndAuthorizedRE asks where the applicant lives and the authorization
// in one question (Sentra: "Are you currently residing and authorized to work
// in the United States?"); the group is the country it names.
var residingAndAuthorizedRE = lazyregexp.New(`(?i)^\s*are\s+you\s+(?:currently\s+)?(?:residing|living|located|based)\s+and\s+(?:legally\s+)?(?:authori[sz]ed|eligible)\s+to\s+work\s+in\s+(?:the\s+)?([^?,]{2,40}?)\s*\?\s*$`)

// workAuthResiding answers that question Yes when both clauses hold: the
// recorded address is in the country it names and the stored authorization is
// Yes. Anything else is left to the plain reader, which does not read it.
func workAuthResiding(s *Step, e *Entry, stored string) (Draft, bool) {
	m := residingAndAuthorizedRE.FindStringSubmatch(s.Q.Text)
	if m == nil || normalizeLabel(stored) != "yes" {
		return Draft{}, false
	}
	country, ok := CountryCode(m[1])
	if !ok || recordedHomeCountry(s) != country {
		return Draft{}, false
	}
	d := bindEntryAnswer(s, e, "Yes", "residing and authorized: the recorded address is in "+country+" and the authorization is Yes")
	d.Source, d.Start = SourceDerived, startProfileDerive
	return d, true
}

// workAuthIndefinite answers a plain work-authorization question asked for an
// indefinite period (Booz Allen: "... on a full-time basis for an indefinite
// period of time ..."): Yes only for a recorded Yes with no sponsorship needed
// now or in the future; a recorded No, or any sponsorship need, is No.
func workAuthIndefinite(s *Step, e *Entry, stored string) (Draft, bool) {
	if !indefiniteRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	q := forEmployerJoinRE.ReplaceAllString(fullTimeBasisRE.ReplaceAllString(indefiniteRE.ReplaceAllString(s.Q.Text, ""), ""), "")
	if negated, ok := workAuthPolarity(q, s.Req.Posting.Company); !ok || negated {
		return abstainDraft(ReasonAmbiguous, "", "an indefinite work-authorization wording that is no plain question: left for review"), true
	}
	answer, why := "No", "the recorded answer is No"
	switch normalizeLabel(stored) {
	case "no":
	case "yes":
		need, known := sponsorshipNeed(s.Req.Facts, scopeEither)
		if !known {
			return abstainDraft(ReasonPlaceholderEmpty, "", "a sponsorship fact is unset"), true
		}
		if why = "sponsorship is needed now or in the future"; !need {
			answer, why = "Yes", "authorized with no sponsorship needed now or in the future"
		}
	default:
		return abstainDraft(ReasonAmbiguous, "", "the recorded work-authorization answer %q is neither Yes nor No", stored), true
	}
	d := bindEntryAnswer(s, e, answer, "work authorization for an indefinite period: "+why+" -> "+answer)
	d.Source, d.Start = SourceDerived, startProfileDerive
	return d, true
}

var (
	// authRequiredRE is a notice's frame saying the role needs work
	// authorization the applicant already holds ("This role requires you to
	// already be legally authorized to work in ..."). Its "you" is the frame's
	// own, so the frame is set aside before the sentence is judged a plain
	// statement.
	authRequiredRE = lazyregexp.New(`(?i)\b(?:requires?\s+(?:you|applicants|candidates)\s+to|(?:you|applicants|candidates)\s+must)\s+(?:already\s+)?(?:be\s+)?(?:legally\s+)?(?:authori[sz]ed|eligible|permitted)\s+to\s+work\b`)
	// residenceJoinRE is a residence conjunct that shares the question's place
	// ("Do you live in, and are you legally authorized to work in ...?").
	residenceJoinRE = lazyregexp.New(`(?i)^(?:do|are)\s+you\s+(?:currently\s+)?(?:live|living|reside|residing)\s+in\s*,?\s+and\s+`)
)

// authNotice is a plain work-authorization question behind the employer's
// notice: ask is the question (without a residence conjunct), residence
// whether it also asks where the applicant lives.
type authNotice struct {
	ask       string
	residence bool
}

// readAuthNotice reads the text as one plain work-authorization question after
// sentences that are all plain statements, a requires-authorization frame set
// aside, at least one of which is the notice that the role needs authorization
// already held or that the company does not sponsor. A negated question is not
// read here and keeps its own reading.
func readAuthNotice(text, company string) (authNotice, bool) {
	sentences := sentencesOf(text)
	last := len(sentences) - 1
	if last < 1 || !strings.HasSuffix(sentences[last], "?") {
		return authNotice{}, false
	}
	notice := false
	for _, sentence := range sentences[:last] {
		rest := authRequiredRE.ReplaceAllString(sentence, " ")
		if noticeBlockRE.MatchString(rest) {
			return authNotice{}, false
		}
		notice = notice || rest != sentence || noticeRE.MatchString(rest)
	}
	if !notice {
		return authNotice{}, false
	}
	a := authNotice{ask: sentences[last]}
	if loc := residenceJoinRE.FindStringIndex(a.ask); loc != nil {
		a.ask, a.residence = a.ask[loc[1]:], true
	}
	negated, ok := workAuthPolarity(a.ask, company)
	return a, ok && !negated
}

// workAuthBehindNotice answers the question behind the notice (F-10): it asks
// whether the applicant can work there as they are, so a recorded Yes is the
// answer only when no sponsorship is needed now, and every other case is No. A
// residence conjunct is No for an address outside the places asked about
// (residenceOf).
func workAuthBehindNotice(s *Step, e *Entry, stored string) (Draft, bool) {
	a, ok := readAuthNotice(s.Q.Text, s.Req.Posting.Company)
	if !ok {
		return Draft{}, false
	}
	lives := true
	if a.residence {
		home, places, _, _ := residenceOf(s, a.ask)
		lives = slices.Contains(places, home)
	}
	answer, why := "No", ""
	if !lives {
		why = "the recorded address is outside the places asked about"
	} else {
		switch normalizeLabel(stored) {
		case "no":
			why = "the recorded answer is No"
		case "yes":
			need, known := sponsorshipNeed(s.Req.Facts, scopeNow)
			if !known {
				return abstainDraft(ReasonPlaceholderEmpty, "", "the current sponsorship fact is unset"), true
			}
			if why = "sponsorship is needed now"; !need {
				answer, why = "Yes", "authorized with no sponsorship needed now"
			}
		default:
			return abstainDraft(ReasonAmbiguous, "", "the recorded work-authorization answer %q is neither Yes nor No", stored), true
		}
	}
	d := bindEntryAnswer(s, e, answer, "work authorization behind the employer's sponsorship notice: "+why+" -> "+answer)
	d.Source, d.Start = SourceDerived, startProfileDerive
	return d, true
}

// clauseKind says which fact a clause asks about.
type clauseKind int

const (
	clauseAuth clauseKind = iota + 1
	clauseSponsor
)

// compound is a question that joins exactly one work-authorization clause and
// one sponsorship clause with "and". The neg fields are the clauses' negations.
type compound struct {
	authNeg, sponsorNeg bool
	authFirst           bool
	// sponsorText is the sponsorship clause alone: its time words, not the
	// other clause's, decide which sponsorship fact it reads.
	sponsorText string
}

var (
	andRE = lazyregexp.New(`(?i)\band\b`)
	// pairOptionRE is an option that answers each clause in turn ("Yes / No").
	pairOptionRE = lazyregexp.New(`(?i)^\s*(yes|no)\s*[/,|\x{2013}-]\s*(yes|no)\s*$`)
	leadYesRE    = lazyregexp.New(`(?i)^\s*yes\b[\s,.:\x{2013}-]*`)
)

// clauseOf reads one clause as a work-authorization or a sponsorship question.
// A second clause may omit its subject ("... and need a visa"), so it is also
// read with one supplied.
func clauseOf(text, company string, elliptical bool) (negated bool, kind clauseKind, ok bool) {
	candidates := []string{text}
	if elliptical {
		candidates = append(candidates, "i "+text, "do you "+text)
	}
	for _, c := range candidates {
		if n, ok := workAuthPolarity(c, company); ok {
			return n, clauseAuth, true
		}
		if n, ok := sponsorshipPolarity(c, company); ok {
			return n, clauseSponsor, true
		}
	}
	return false, 0, false
}

// readCompound reads the question as the one recognised compound shape: split
// at an "and" so that each side is a recognised clause and the two clauses ask
// different facts. Several valid splits are not one reading.
func readCompound(text, company string) (compound, bool) {
	var found []compound
	for _, loc := range andRE.FindAllStringIndex(text, -1) {
		left, right := text[:loc[0]], text[loc[1]:]
		ln, lk, lok := clauseOf(left, company, false)
		rn, rk, rok := clauseOf(right, company, true)
		if !lok || !rok || lk == rk {
			continue
		}
		c := compound{authFirst: lk == clauseAuth}
		if lk == clauseAuth {
			c.authNeg, c.sponsorNeg, c.sponsorText = ln, rn, right
		} else {
			c.authNeg, c.sponsorNeg, c.sponsorText = rn, ln, left
		}
		found = append(found, c)
	}
	if len(found) != 1 {
		return compound{}, false
	}
	return found[0], true
}

// storedWorkAuth is the user's recorded Yes or No to the work-authorization
// question; known is false for a blank answer or one that is neither.
func storedWorkAuth(s *Step) (authorized, known bool) {
	if s.otherAuth != nil {
		return *s.otherAuth, true
	}
	switch normalizeLabel(entryText(s, idWorkAuthUS)) {
	case "yes":
		return true, true
	case "no":
		return false, true
	}
	return false, false
}

// compoundFacts is a compound question's two answers under the user's facts:
// what a clause asks, answered from the recorded work-authorization answer and
// the sponsorship fact that clause reads.
func compoundFacts(s *Step) (c compound, authorized bool, ansAuth, ansSponsor bool, d Draft, ok bool) {
	c, ok = readCompound(s.Q.Text, s.Req.Posting.Company)
	if !ok {
		return
	}
	authorized, known := storedWorkAuth(s)
	if !known {
		return c, false, false, false, abstainDraft(ReasonPlaceholderEmpty, "", "the recorded work-authorization answer is missing or not Yes or No"), true
	}
	need, known := sponsorshipNeed(s.Req.Facts, sponsorshipScope(c.sponsorText))
	if !known {
		return c, false, false, false, abstainDraft(ReasonPlaceholderEmpty, "", "the sponsorship fact this question reads is unset"), true
	}
	return c, authorized, authorized != c.authNeg, need != c.sponsorNeg, Draft{}, true
}

func compoundAmbiguous(why string) Draft {
	return abstainDraft(ReasonAmbiguous, "", "work authorization and sponsorship in one question: %s", why)
}

// workAuthCompound answers the two-clause question. Options that each state
// both facts decide it: exactly one must state the recorded work-authorization
// answer and the sponsorship fact. On a bare Yes/No control one word cannot
// answer two clauses, so it is given only when both clauses have the same
// answer under the user's facts, or when both state the favourable fact.
func workAuthCompound(s *Step, e *Entry) (Draft, bool) {
	c, authorized, ansAuth, ansSponsor, d, ok := compoundFacts(s)
	if !ok {
		return Draft{}, false
	}
	if d.Abstain != nil {
		return d, true
	}
	if !bareYesNo(s.Q.Options) {
		return statedOption(s, e, authorized)
	}
	if ansAuth != ansSponsor {
		// Avride's "I am authorized to work in the United States and do not
		// require current or future visa sponsorship." is one claim, the
		// standing without sponsorship: false when either clause is, so No.
		// Clauses that point opposite ways ("Are you authorized ... and will
		// you require sponsorship?") are two questions.
		if c.authNeg || !c.sponsorNeg {
			return compoundAmbiguous("a bare Yes or No would answer the two clauses differently"), true
		}
		d = bindEntryAnswer(s, e, "No", "compound: the statement claims the standing without sponsorship, and one clause is false under the recorded facts")
		d.Source, d.Start = SourceDerived, startProfileDerive
		return d, true
	}
	answer := "No"
	if ansAuth {
		answer = "Yes"
	}
	d = bindEntryAnswer(s, e, answer, "compound: both clauses are answered "+answer+" under the recorded facts")
	d.Source, d.Start = SourceDerived, startProfileDerive
	return d, true
}

// statedOption chooses, among options that each state both facts, the one that
// states the recorded work-authorization answer and a sponsorship need equal to
// the question's, as that option's own clause reads it. Nothing else is chosen.
func statedOption(s *Step, e *Entry, authorized bool) (Draft, bool) {
	c, _ := readCompound(s.Q.Text, s.Req.Posting.Company)
	need, _ := sponsorshipNeed(s.Req.Facts, sponsorshipScope(c.sponsorText))
	var matching []Option
	for _, o := range realOptions(s.Q.Options) {
		if noWordRE.MatchString(o.Label) {
			continue
		}
		oc, ok := readCompound(leadYesRE.ReplaceAllString(o.Label, ""), s.Req.Posting.Company)
		if !ok {
			continue
		}
		optNeed, known := sponsorshipNeed(s.Req.Facts, sponsorshipScope(oc.sponsorText))
		if !oc.authNeg == authorized && !oc.sponsorNeg == need && known && optNeed == need {
			matching = append(matching, o)
		}
	}
	if len(matching) != 1 {
		return compoundAmbiguous("no single option states both recorded facts"), true
	}
	d := optionDraft(optionBinding{Label: matching[0].Label, Value: matching[0].Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"compound: the option states both recorded facts -> " + quoteTerm(matching[0].Label)}
	return d, true
}

// compoundPairOption answers the two-clause question from an option that gives
// each clause its own answer in turn ("Yes / No", in the order of the clauses).
// It reports false for a question with no such options.
func compoundPairOption(s *Step, e *Entry) (Draft, bool) {
	var pairs []Option
	for _, o := range realOptions(s.Q.Options) {
		if pairOptionRE.MatchString(o.Label) {
			pairs = append(pairs, o)
		}
	}
	if len(pairs) == 0 {
		return Draft{}, false
	}
	c, _, ansAuth, ansSponsor, d, ok := compoundFacts(s)
	if !ok {
		return Draft{}, false
	}
	if d.Abstain != nil {
		return d, true
	}
	first, second := ansAuth, ansSponsor
	if !c.authFirst {
		first, second = ansSponsor, ansAuth
	}
	var hits []Option
	for _, o := range pairs {
		m := pairOptionRE.FindStringSubmatch(o.Label)
		if isYesWord(m[1]) == first && isYesWord(m[2]) == second {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return compoundAmbiguous("no single option gives each clause its answer"), true
	}
	d = optionDraft(optionBinding{Label: hits[0].Label, Value: hits[0].Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"compound: the option answers each clause in turn -> " + quoteTerm(hits[0].Label)}
	return d, true
}

// contradictsWorkAuth reports an option that states the work-authorization
// fact the other way round from the user's recorded answer. An option that
// states none, or a user who recorded none, contradicts nothing.
func contradictsWorkAuth(s *Step, label string) bool {
	stated, ok := statedAuthorization(label, s.Req.Posting.Company)
	if !ok {
		return false
	}
	authorized, known := storedWorkAuth(s)
	return known && stated != authorized
}

// bareYesNo reports options that are exactly Yes and No (a placeholder aside):
// only then does a bare answer bind to the label it names.
func bareYesNo(opts []Option) bool {
	real := realOptions(opts)
	if len(real) != 2 {
		return false
	}
	a, b := normalizeLabel(real[0].Label), normalizeLabel(real[1].Label)
	return a == "yes" && b == "no" || a == "no" && b == "yes"
}

func isYesWord(w string) bool { return strings.EqualFold(w, "yes") }

// statedAuthorization reads an option as the compound statement "I am
// authorized and do not require sponsorship" and reports the authorization it
// states. A leading "Yes" only confirms it; a leading "No" could negate either
// clause, so such an option is not read.
func statedAuthorization(label, company string) (authorized, ok bool) {
	if noWordRE.MatchString(label) {
		return false, false
	}
	c, ok := readCompound(leadYesRE.ReplaceAllString(label, ""), company)
	return !c.authNeg, ok
}

// unrestrictedOptionRE is a Yes that states its authorization has no
// restriction (Datadog: "Yes, no restriction."): no sponsorship is needed, now
// or later.
var unrestrictedOptionRE = lazyregexp.New(`(?i)^\W*(?:with\s+)?no\s+restrictions?\b|\bwithout\s+(?:any\s+)?restrictions?\b|\bunrestricted\b`)

// answerLedOption answers an authorization question whose options each lead
// with a Yes or No to it, every Yes going on to state a sponsorship need ("Yes,
// but I will require sponsorship for employment visa status in the future") or
// that it has no restriction; a No may state the authorization alone
// (Temporal: "No, I am not currently authorized to work in this location"). It
// chooses the one option whose lead is the recorded authorization answer and
// whose need the recorded facts do not contradict, and reports false for any
// other question or option set.
func answerLedOption(s *Step, e *Entry) (Draft, bool) {
	if sponsorMentionRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	if negated, ok := workAuthPolarity(s.Q.Text, s.Req.Posting.Company); !ok || negated {
		return Draft{}, false
	}
	opts := realOptions(s.Q.Options)
	if len(opts) < 2 {
		return Draft{}, false
	}
	yesLed := false
	for _, o := range opts {
		lead := leadYesRE.FindString(o.Label)
		yes := lead != ""
		if !yes {
			lead = noWordRE.FindString(o.Label)
		}
		if rest := o.Label[len(lead):]; lead == "" || yes && !sponsorMentionRE.MatchString(rest) && !unrestrictedOptionRE.MatchString(rest) {
			return Draft{}, false
		}
		yesLed = yesLed || yes
	}
	if !yesLed {
		return Draft{}, false
	}
	authorized, known := storedWorkAuth(s)
	if !known {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the work-authorization answer the options lead with is unset"), true
	}
	var hits []Option
	for _, o := range opts {
		if leadYesRE.MatchString(o.Label) != authorized {
			continue
		}
		if afterYes := leadYesRE.ReplaceAllString(o.Label, ""); authorized && unrestrictedOptionRE.MatchString(afterYes) {
			if need, known := sponsorshipNeed(s.Req.Facts, scopeEither); known && !need {
				hits = append(hits, o)
			}
			continue
		}
		rest := noWordRE.ReplaceAllString(leadYesRE.ReplaceAllString(o.Label, ""), "")
		need, known := sponsorshipNeed(s.Req.Facts, sponsorshipScope(rest))
		if known && !contradictsSponsorship(rest, need) {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return compoundAmbiguous("no single option leads with the recorded authorization and states the recorded sponsorship need"), true
	}
	d := optionDraft(optionBinding{Label: hits[0].Label, Value: hits[0].Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"the options lead with the authorization answer and state the sponsorship need -> " + quoteTerm(hits[0].Label)}
	return d, true
}

// idRightToWorkCountries lists the countries the applicant has the right to
// work in.
const idRightToWorkCountries = "right-to-work-countries"

// unrestrictedRightRE narrows the right to one without restriction or
// sponsorship.
var unrestrictedRightRE = lazyregexp.New(`(?i)\b(?:unrestricted|without\s+(?:any\s+)?(?:restrictions?|(?:visa\s+|employer\s+)?sponsorship)|full|permanent)\b`)

// deriveRightToWorkCountries answers "In what countries do you have the
// (unrestricted) right to work?" (Elastic) from the profile: every country of
// citizenship, and each recorded work-authorization country the right covers.
// A plain right covers every authorization; an unrestricted one covers the
// United States only when the unrestricted entry says Yes, never while
// sponsorship is needed, and no other authorization the profile cannot place.
// The user's own answer wins; an unrecorded citizenship leaves the box.
func deriveRightToWorkCountries(s *Step, e *Entry) (Draft, bool) {
	if ownText(e) != "" {
		return Draft{}, false
	}
	f := s.Req.Facts
	if len(f.Citizenships) == 0 {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: no citizenship is recorded", e.ID), true
	}
	unrestricted := unrestrictedRightRE.MatchString(s.Q.Text)
	var names []string
	seen := map[string]bool{}
	add := func(code string) {
		code = strings.ToUpper(strings.TrimSpace(code))
		if code != "" && !seen[code] {
			seen[code] = true
			names = append(names, countryName(code))
		}
	}
	for _, c := range f.Citizenships {
		add(c)
	}
	for _, c := range f.AuthorizedCountries {
		c = strings.ToUpper(strings.TrimSpace(c))
		switch {
		case seen[c]:
		case !unrestricted:
			add(c)
		case c != "US":
			return abstainDraft(ReasonAmbiguous, "", "%s: whether the authorization for %s is unrestricted is not recorded", e.ID, c), true
		default:
			free, known := unrestrictedUS(s)
			if !known {
				return abstainDraft(ReasonAmbiguous, "", "%s: whether the authorization for the United States is unrestricted is not recorded", e.ID), true
			}
			if free {
				add(c)
			}
		}
	}
	return entryDraft(strings.Join(names, ", "), "countries of citizenship and the authorizations the right covers -> "+quoteTerm(strings.Join(names, ", "))), true
}

// unrestrictedUS reads whether the applicant's right to work in the United
// States is unrestricted: the unrestricted entry's Yes or No, else No while
// sponsorship is needed now or later.
func unrestrictedUS(s *Step) (free, known bool) {
	if u := entryNamed(s, idUnrestrictedWork); u != nil {
		switch normalizeLabel(ownText(u)) {
		case "yes":
			return true, true
		case "no":
			return false, true
		}
	}
	f := s.Req.Facts
	if f.RequiresSponsorshipNow != nil && *f.RequiresSponsorshipNow || f.RequiresSponsorshipFuture != nil && *f.RequiresSponsorshipFuture {
		return false, true
	}
	return false, false
}
