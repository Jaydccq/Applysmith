package match

import (
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// consentAffirmativeIDs are resolved exactly like auto-acknowledge entries
// whatever their preferred values: every ATS words its consent option
// differently, while the affirmative/negated split is stable.
var consentAffirmativeIDs = setOf("terms-and-conditions-consent", "assessment-proctoring-consent")

const consentShapeVerbs = `agree|accept|consent|confirm|acknowledge|attest|certify|declare|affirm`

var (
	// The leading clause decides: negation first, then affirmation. The verbs
	// include attest, certify, declare and affirm because an ATS may render the
	// whole attestation sentence as the only option label.
	negatedLeadRE        = lazyregexp.New(`(?i)^\s*(?:no\b|i\s+(?:do|does|did|have|has|had|am|is|are|was|were|will|would|can|could|shall|should|may|might|must)\s+not\b|decline\b|disagree\b|i\s+disagree\b)`)
	affirmativeConsentRE = lazyregexp.New(`(?i)^\s*(?:yes\b|agree\b|accept\b|acknowledge\b|confirm\b|i\s+(?:have\s+read\s+and\s+)?(?:` + consentShapeVerbs + `)\b)`)
	consentShapeVerbRE   = lazyregexp.New(`^(?:` + consentShapeVerbs + `)$`)
	markerOnlyLabelRE    = lazyregexp.New(`^[*\x{2731}\x{2217}\x{066D}\x{2022}\x{00B7}\-\x{2013}\x{2014}:;.,()\[\]\s]+$`)
)

func isNegatedConsentLabel(label string) bool {
	label = expandContractions(label)
	if negatedLeadRE.MatchString(label) {
		return true
	}
	// The affirmative lead establishes the acknowledgement's polarity. Later
	// negative qualifications do not reverse "Yes" or "I certify ...".
	if affirmativeConsentRE.MatchString(label) {
		return false
	}
	tokens := tokenize(label)
	// Negative acknowledgement/consent verbs cannot return through a shape.
	// Shape-only verbs do not become option-only routing or scan vocabulary.
	for i, word := range tokens {
		if consentShapeVerbRE.MatchString(word) && hasNegationBefore(tokens, i, 4) {
			return true
		}
	}
	// A plain restatement carrying a negative/refusal is not an affirmative
	// acknowledgement, even when no consent verb is present.
	for _, word := range tokens {
		if refusalNegationTokens[word] {
			return true
		}
	}
	return false
}

func isAffirmativeConsentLabel(label string) bool {
	return !isNegatedConsentLabel(label) && affirmativeConsentRE.MatchString(expandContractions(label))
}

// ackPhrases are scanned in this order in every option.
var ackPhrases = []string{"accept", "acknowledge", "agree", "confirm", "consent", "i agree", "i acknowledge", "i confirm", "i consent"}

// isNonSemanticAckLabel reports a label that carries no election of its own:
// empty, only marker glyphs, or a restatement of the question.
func isNonSemanticAckLabel(label, questionText string) bool {
	t := foldSpaces(label)
	if strings.TrimSpace(t) == "" || markerOnlyLabelRE.MatchString(strings.TrimSpace(t)) {
		return true
	}
	nl := normalizeLabel(label)
	return nl != "" && strings.Contains(normalizeLabel(questionText), nl)
}

// marketing opt-in vocabulary: a subscription, not a condition of applying.
var marketingOptInRE = lazyregexp.New(`(?i)\b(?:marketing|newsletters?|promotions?|promotional|(?:special|promotional|exclusive|partner)\s+offers|receive\s+offers|job\s+alerts?|sms|text\s+messages?|mailing\s+list|subscribe|talent\s+(?:community|pool|network)|portfolio\s+compan(?:y|ies))\b|` +
	`\b(?:contact(?:ed)?|consider(?:ed)?|notify|notified|keep\s+me|hear\s+about|receive|send\s+me|email\s+me|reach\s+out)\b[^.]{0,60}?\b(?:future|other)\s+(?:job\s+)?(?:opportunit(?:y|ies)|roles?|openings?|positions?|vacanc(?:y|ies))\b`)

// isMarketingOptInText reports text offering a marketing opt-in. "Future
// opportunities" counts only as the object of an ask: a truth certification
// warning that false answers may affect consideration for future opportunities
// is a declaration, not an opt-in.
func isMarketingOptInText(text string) bool { return marketingOptInRE.MatchString(text) }

// readNoticeAckRE is an acknowledgement of having read a notice ("Please select
// "Acknowledge" below to signify that you have read and understand this
// notice."; "I have read and understand the Export Control statement included
// in the job description above.").
var readNoticeAckRE = lazyregexp.New(`(?i)\b(?:i|you)\s+have\s+read\s+and\s+(?:understand|understood)\s+(?:this|the|these|our|that)\b[^.?!]{0,80}?\b(?:notices?|statements?|polic(?:y|ies)|disclosures?|information|terms)\b`)

// ackFirstPersonRE is a sentence in the applicant's own voice.
var ackFirstPersonRE = lazyregexp.New(`(?i)\b(?:i|me|my|mine|myself)\b`)

// ackGateText is the question text the disclosure gate reads: the closing
// sentence acknowledging a notice as read, when every sentence before it is
// the notice in the company's voice (the owner's decision: a notice's topic, a
// sponsorship or an export-control notice, is no disclosure by the applicant,
// who only says it was read); else the whole text, so a statement of the
// applicant's own beside the acknowledgement is still gated.
func ackGateText(text string) string {
	sentences := sentencesOf(text)
	n := len(sentences)
	if n == 0 || !readNoticeAckRE.MatchString(sentences[n-1]) {
		return text
	}
	for _, sentence := range sentences[:n-1] {
		if ackFirstPersonRE.MatchString(sentence) {
			return text
		}
	}
	return sentences[n-1]
}

// rolePolicyAckRE acknowledges, as the whole question, a sponsorship the role
// does not offer (GHX: "I acknowledge that this role is not eligible for visa
// sponsorship."): a notice about the role, not a fact about the applicant.
var rolePolicyAckRE = lazyregexp.New(`(?i)^\W*i\s+(?:acknowledge|understand)\s+that\s+(?:this|the)\s+(?:role|position|job)\s+(?:is\s+not\s+eligible\s+for|does\s+not\s+(?:offer|provide|include)|will\s+not\s+(?:offer|provide))\s+(?:any\s+)?(?:visa\s+|immigration\s+|employment\s+)?sponsorship\W*$`)

// readNoticeYesNo answers Yes to a required Yes/No that only acknowledges a
// notice as read (Tenstorrent: "I have read and understand the Export Control
// statement included in the job description above."), or what the role does
// not offer (rolePolicyAckRE), as a ticked acknowledgement would be (the
// owner's decision): its last sentence is the acknowledgement, it asks no
// question, and it is no statement about how the application was made.
// mandatory-acknowledge takes boxes only.
func readNoticeYesNo(s *Step) (Draft, bool) {
	q := s.Q
	sentences := sentencesOf(q.Text)
	policy := rolePolicyAckRE.MatchString(q.Text)
	if len(sentences) == 0 || !policy && !readNoticeAckRE.MatchString(sentences[len(sentences)-1]) || strings.Contains(q.Text, "?") ||
		!s.Raw.IsRequired() || !s.Req.AutoAcceptLegalDeclarations || IsApplicationStatement(q.Text) || !policy && isAttestationDefaultUnsafe(ackGateText(q.Text)) {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	if _, okN := ownYesNo(q.Options, false); !okY || !okN || len(realOptions(q.Options)) != 2 {
		return Draft{}, false
	}
	return chosenOption(s, yes, "a Yes/No acknowledging a notice as read: Yes, as a ticked acknowledgement"), true
}

// typedAckRE asks for a quoted phrase of understanding or agreement to be
// typed (Energy Solutions: `Please type "I understand" in the text box
// below.`).
var typedAckRE = lazyregexp.New(`(?i)\b(?:type|enter|write)\s+(?:in\s+)?["“'‘]((?:i\s+)?(?:understand|agree|acknowledge|accept)|understood|agreed|acknowledged|accepted)\.?["”'’]`)

// typedAcknowledgement types the phrase a required text box asks for to
// acknowledge the notice before it, as a ticked acknowledgement would be (the
// owner's decision). A statement about how the application was made, or one
// the disclosure gate holds, stays the user's.
func typedAcknowledgement(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || len(q.Options) > 0 || !s.Raw.IsRequired() || !s.Req.AutoAcceptLegalDeclarations {
		return Draft{}, false
	}
	m := typedAckRE.FindStringSubmatch(q.Text)
	if m == nil || IsApplicationStatement(q.Text) || isAttestationDefaultUnsafe(q.Text) {
		return Draft{}, false
	}
	return derivedDraft(m[1], "", startProfileRow, "a typed acknowledgement: the phrase the box asks for, as a ticked acknowledgement"), true
}

// acknowledgeStage resolves an auto-acknowledge entry, in the order of the
// specification: the disclosure gate, the choice of an option, requiredness,
// then the legal-declaration setting. Every refusal that leaves the control for
// the user is a terminal context-policy abstain.
func acknowledgeStage(s *Step) (Draft, bool) {
	q := s.Q
	texts := make([]string, 0, len(q.Options)+1)
	texts = append(texts, ackGateText(q.Text))
	for _, o := range q.Options {
		texts = append(texts, o.Label)
	}
	for _, t := range texts {
		if isAttestationDefaultUnsafe(t) {
			return policyDraft(DetailDisqualifyingDisclose,
				"auto-acknowledge: disqualifying-fact or sensitive-category disclosure in %q: nothing is ticked", clip(t, 80)), true
		}
	}
	if readSelfIDSetUsing(q.Options, s.scorer()) != nil {
		return policyDraft(DetailDisqualifyingDisclose, "auto-acknowledge: the options are a self-identification set: nothing is ticked"), true
	}
	if d, ok := stateNoticeAck(s); ok {
		return d, true
	}

	chosen, trace, ok := chooseAckOption(q)
	// The label is read as the page wrote it: the sanitized text has lost an
	// "(Optional)" marker.
	required := s.Raw.IsRequired()
	if !required && marketingText(q) != "" && !ok {
		return policyDraft(DetailMarketingOptIn,
			"auto-acknowledge: optional marketing opt-in in %q: never ticked", clip(marketingText(q), 80)), true
	}
	if !ok {
		return Draft{Abstain: &Abstain{Reason: ReasonInputMismatch}, Trace: trace}, true
	}
	if !required {
		return Draft{Abstain: &Abstain{Reason: ReasonContextPolicy, Detail: DetailMarketingOptIn},
			Trace: append(trace, "auto-acknowledge: the control is not required: never ticked")}, true
	}
	if !s.Req.AutoAcceptLegalDeclarations {
		return Draft{Abstain: &Abstain{Reason: ReasonContextPolicy, Detail: DetailLegalDeclarationOff},
			Trace: append(trace, "auto-acknowledge: autoAcceptLegalDeclarations is off: legal declaration left for the user")}, true
	}
	if group, ok := ackGroup(q); ok {
		labels := make([]string, len(group))
		for i, o := range group {
			labels[i] = o.Label
		}
		return Draft{Answer: strings.Join(labels, ", "), ChosenLabel: labels[0], ChosenLabels: labels, Start: capStart(s, 0.90),
			Trace: append(trace, "auto-acknowledge: every box of the group is an acknowledgement: all ticked")}, true
	}
	return Draft{Answer: chosen.Value, ChosenLabel: chosen.Label, Start: capStart(s, 0.90), Trace: trace}, true
}

// ackGroup reports a checkbox group of two boxes that are each an
// acknowledgement (Gemini: "By clicking this box ..., you consent to our
// Applicant Privacy Statement." beside the same consent to a background
// check): both are ticked. A box that is negated, withdraws the candidacy or
// opts in to marketing keeps the group to the one acknowledgement.
func ackGroup(q Question) ([]Option, bool) {
	if q.Kind != KindCheckbox || len(q.Options) != 2 {
		return nil, false
	}
	for _, o := range q.Options {
		if isPlaceholderOption(o.Label) || isWithdrawalLabel(o.Label) || isMarketingOptInText(o.Label) || !ackPhraseIn(o.Label) {
			return nil, false
		}
	}
	return q.Options, true
}

// ackPhraseIn reports an acknowledgement phrase in the label with no negation
// before it.
func ackPhraseIn(label string) bool {
	toks := negationTokenList(label)
	for _, phrase := range ackPhrases {
		if start := optRunStart(toks, tokenize(phrase)); start >= 0 && !hasNegationBefore(toks, start, 4) {
			return true
		}
	}
	return false
}

func policyDraft(detail Detail, format string, args ...any) Draft {
	return Draft{Abstain: &Abstain{Reason: ReasonContextPolicy, Detail: detail}, Trace: []string{fmt.Sprintf(format, args...)}}
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// marketingText is the first text of the question that reads as a marketing
// opt-in, or "".
func marketingText(q Question) string {
	if isMarketingOptInText(q.Text) {
		return q.Text
	}
	for _, o := range q.Options {
		if isMarketingOptInText(o.Label) {
			return o.Label
		}
	}
	return ""
}

// chooseAckOption picks the option an acknowledgement ticks, or reports that no
// option may be chosen. The control must render one checkbox or one or two
// answerable options; three or more are a value set, a claim of fact rather than
// an acknowledgement.
func chooseAckOption(q Question) (Option, []string, bool) {
	var answerable []Option
	for _, o := range q.Options {
		if !isPlaceholderOption(o.Label) {
			answerable = append(answerable, o)
		}
	}
	if len(q.Options) == 0 || len(answerable) > 2 {
		return Option{}, []string{fmt.Sprintf("auto-acknowledge: answerable-option-count=%d of %d rendered (need 1..2): no option may be chosen", len(answerable), len(q.Options))}, false
	}
	var trace []string
	for _, o := range q.Options {
		if isWithdrawalLabel(o.Label) {
			trace = append(trace, fmt.Sprintf("auto-acknowledge: option %q withdraws the candidacy: never chosen", o.Label))
			continue
		}
		toks := negationTokenList(o.Label)
		for _, phrase := range ackPhrases {
			start := optRunStart(toks, tokenize(phrase))
			if start < 0 {
				continue
			}
			if hasNegationBefore(toks, start, 4) {
				trace = append(trace, fmt.Sprintf("auto-acknowledge: option %q has negation before %q: skip", o.Label, phrase))
				continue
			}
			return o, append(trace, fmt.Sprintf("auto-acknowledge: matched option %q via %q", o.Label, phrase)), true
		}
	}
	var affirmatives []Option
	for _, o := range answerable {
		if isAffirmativeConsentLabel(o.Label) && !isWithdrawalLabel(o.Label) {
			affirmatives = append(affirmatives, o)
		}
	}
	shape := ""
	switch {
	case len(answerable) == 1 && len(affirmatives) == 1:
		shape = "sole-real-option"
	case len(answerable) == 1 && !isNegatedConsentLabel(answerable[0].Label):
		shape = "sole-non-negative-option"
	case len(answerable) == 2 && len(affirmatives) == 1 && (isNegatedConsentLabel(answerable[0].Label) || isNegatedConsentLabel(answerable[1].Label)):
		shape = "affirmative/negative pair"
	case q.Kind == KindCheckbox && len(q.Options) == 1 && len(affirmatives) == 0 && !isNegatedConsentLabel(q.Options[0].Label) &&
		isNonSemanticAckLabel(q.Options[0].Label, q.Text):
		shape = "lone-checkbox"
	}
	if shape == "" {
		return Option{}, append(trace, "auto-acknowledge: no ack-token option and no affirmative shape"), false
	}
	var chosen Option
	switch {
	case len(affirmatives) > 0:
		chosen = affirmatives[0]
	case len(answerable) > 0:
		chosen = answerable[0]
	default:
		chosen = q.Options[0]
	}
	if isWithdrawalLabel(chosen.Label) {
		return Option{}, append(trace, fmt.Sprintf("auto-acknowledge: option %q withdraws the candidacy: never chosen", chosen.Label)), false
	}
	return chosen, append(trace, fmt.Sprintf("auto-acknowledge: affirmative option %q by shape (%s)", chosen.Label, shape)), true
}

var (
	// readCheckRE asks the applicant to pick an option by its place, to show
	// the instructions were read (Kobie: "To confirm you carefully read
	// instructions, please select the SECOND option below"); group 1 is the
	// place.
	readCheckRE = lazyregexp.New(`(?i)\b(?:select|choose|pick|click|mark)\s+(?:the\s+)?(first|second|third|fourth|fifth|last|1st|2nd|3rd|4th|5th)\s+(?:option|choice|answer|box)\b`)
	// bareOptionRE is an option that says nothing but its own name ("OPTION
	// 2", "Option B", "3").
	bareOptionRE    = lazyregexp.New(`(?i)^\s*(?:(?:option|choice|answer)\s*)?(?:[a-e]|\d{1,2})\s*[.)]?\s*$`)
	readCheckPlaces = map[string]int{"first": 0, "1st": 0, "second": 1, "2nd": 1, "third": 2, "3rd": 2, "fourth": 3, "4th": 3, "fifth": 4, "5th": 4}
)

// readCheck answers a check that the instructions were read by picking the
// option at the place the question names. Only options that state nothing
// are picked by place: an option that says something is answered for what it
// says, whatever the question tells.
func readCheck(s *Step) (Draft, bool) {
	m := readCheckRE.FindStringSubmatch(s.Q.Text)
	opts := realOptions(s.Q.Options)
	if m == nil || len(opts) < 2 || s.Q.Kind == KindCheckbox || s.Q.InputType == InputMultiSelect {
		return Draft{}, false
	}
	for _, o := range opts {
		if !bareOptionRE.MatchString(o.Label) {
			return Draft{}, false
		}
	}
	place, ok := readCheckPlaces[strings.ToLower(m[1])]
	if strings.EqualFold(m[1], "last") {
		place, ok = len(opts)-1, true
	}
	if !ok || place >= len(opts) {
		return Draft{}, false
	}
	o := opts[place]
	d := derivedDraft(o.Label, "", startProfileDerive, fmt.Sprintf("read check: the %s option -> %q", strings.ToLower(m[1]), o.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore, d.Deterministic = o.Label, true, 1, true
	return d, true
}
