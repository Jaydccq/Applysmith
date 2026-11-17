package match

import "math"

// Outcome is the closed result vocabulary of one question.
type Outcome string

const (
	// OutcomeFilled is written automatically.
	OutcomeFilled Outcome = "filled"
	// OutcomeFilledReview is written and flagged for the user to review.
	OutcomeFilledReview Outcome = "filled-review"
	// OutcomeFilledModel is written by a model-assisted lane and is always a
	// review fill whatever its number.
	OutcomeFilledModel Outcome = "filled-model"
	// OutcomeAbstained writes nothing; Result.Reason says why.
	OutcomeAbstained Outcome = "abstained"
)

// Reason is the closed set of abstain reasons. A reason is set at the site that
// abstains and is never derived by parsing the trace.
type Reason string

const (
	ReasonNoEntry           Reason = "no-entry"
	ReasonInputMismatch     Reason = "input-mismatch"
	ReasonNarrativeDeferred Reason = "narrative-deferred"
	ReasonPlaceholderEmpty  Reason = "placeholder-empty"
	ReasonAmbiguous         Reason = "ambiguous"
	ReasonLowConfidence     Reason = "low-confidence"
	ReasonDestructiveOption Reason = "destructive-option"
	ReasonSectionPolicy     Reason = "section-policy"
	ReasonContextPolicy     Reason = "context-policy"
	ReasonDuplicateLink     Reason = "duplicate-link"
	ReasonSelfIDPolicy      Reason = "self-id-policy"
	ReasonAskMe             Reason = "ask-me"
	ReasonAIInstruction     Reason = "ai-instruction"

	// The reasons below are set by the model-assisted lanes; the matcher sets
	// unsupported, budget-exhausted and no-grounding as well.
	ReasonUnsupported       Reason = "unsupported"
	ReasonResponseMismatch  Reason = "response-mismatch"
	ReasonModelAbstain      Reason = "model-abstain"
	ReasonBudgetExhausted   Reason = "budget-exhausted"
	ReasonNoRoute           Reason = "no-route"
	ReasonSensitiveCategory Reason = "sensitive-category"
	ReasonNoGrounding       Reason = "no-grounding"
	ReasonUpstreamError     Reason = "upstream-error"
	ReasonTooManyOptions    Reason = "too-many-options"
)

// Detail is the closed list of context-policy details: why a context policy
// abstained, or the presumption a filled answer of one rests on.
type Detail string

const (
	DetailPostingCountry Detail = "posting_country"
	// DetailPostingCountryPresumed is a filled answer about the posting's
	// country given as for a posting in the United States, which nothing
	// placed (owner decision, flow round 8).
	DetailPostingCountryPresumed Detail = "posting_country_presumed"
	DetailLocalCurrency          Detail = "local_currency"
	DetailHeldCompensation       Detail = "held_compensation"
	DetailEmployerDetail         Detail = "employer_detail"
	DetailSponsorshipConjunction Detail = "sponsorship_conjunction"
	DetailDisqualifyingDisclose  Detail = "disqualifying_disclosure"
	DetailLegalDeclarationOff    Detail = "legal_declaration_off"
	DetailMarketingOptIn         Detail = "marketing_opt_in"
	DetailApplicationStatement   Detail = "application_statement"
	// DetailCounterpartyEmployer is whether the applicant or a family member
	// works for a client, prime contractor or partner of the employer: a fact
	// about the employers' dealings that no entry holds (Booz Allen).
	DetailCounterpartyEmployer Detail = "counterparty_employer"
	// DetailResidenceClaim is a question that asks where the applicant lives
	// whose every Yes names a place the profile's home is not (Gusto: "Yes -
	// San Francisco"): the Yes would claim a home they do not have, the No
	// would decline an office they may move to.
	DetailResidenceClaim Detail = "residence_claim"
)

// Source is the closed set of answer sources.
type Source string

const (
	SourceEntry         Source = "entry"
	SourceDerived       Source = "derived"
	SourceComposite     Source = "composite"
	SourceObservation   Source = "observation"
	SourceFormText      Source = "form-text"
	SourceConditional   Source = "conditional"
	SourceSelfIDDecline Source = "self-id-decline"
	SourceModelOption   Source = "model-option"
	// SourceModelEntry is an answer of the user's own entry that a model
	// recognised as the question's and the matcher then resolved (MatchPinned).
	SourceModelEntry   Source = "model-entry"
	SourceComposed     Source = "composed"
	SourceRelationship Source = "relationship"
	// SourceOwnerDefault is the owner's standing default for a question no
	// fact settles (an experience Yes/No takes Yes, a list its least
	// experience), filled for review.
	SourceOwnerDefault Source = "owner-default"
)

// terminalReasons never reach a later lane: the walk, the post-passes and the
// model lanes all leave them alone.
func (r Reason) terminal() bool {
	switch r {
	case ReasonSectionPolicy, ReasonContextPolicy, ReasonDuplicateLink,
		ReasonSelfIDPolicy, ReasonAskMe, ReasonAIInstruction:
		return true
	}
	return false
}

// IsTerminal reports whether an abstain with this reason is terminal.
func (r Reason) IsTerminal() bool { return r.terminal() }

// Confidence bands. The review bands are fixed and stated only here.
const (
	AutoFillConfidence   = 0.90
	ReviewFillConfidence = 0.70
	// FuzzyConfidenceCap keeps a fuzzy match, an unconfirmed default and a stale
	// entry out of the automatic band.
	FuzzyConfidenceCap = 0.89
	// ClearWinnerFloor and ClearWinnerLead are the review-lift thresholds for a
	// best option that clearly beats the best option with a different value.
	ClearWinnerFloor = 0.45
	ClearWinnerLead  = 0.15
)

// Result is the answer for one question.
type Result struct {
	Selector     string
	Outcome      Outcome
	Answer       string
	ChosenLabel  string
	ChosenLabels []string
	// Alternatives are what the apply step may also take, in order, when the
	// page holds none of the Answer (see Draft.Alternatives).
	Alternatives []string
	EntryID      string
	AnswerKey    string
	Source       Source
	Confidence   float64
	Reason       Reason
	Detail       Detail
	Terminal     bool
	Trace        []string
	// Cited are the references of the facts a model-assisted fill rests on.
	Cited []string
}

// Abstain describes an abstain a stage produced.
type Abstain struct {
	Reason Reason
	Detail Detail
}

// Terminal reports whether the abstain stops every later step.
func (a Abstain) Terminal() bool { return a.Reason.terminal() }

// OutcomeForConfidence maps a reported confidence to a fill outcome. The second
// result is false below the review band.
func OutcomeForConfidence(c float64) (Outcome, bool) {
	switch {
	case c >= AutoFillConfidence:
		return OutcomeFilled, true
	case c >= ReviewFillConfidence:
		return OutcomeFilledReview, true
	}
	return OutcomeAbstained, false
}

// ClampConfidence maps NaN and infinities to 0 and clamps to 0..1.
func ClampConfidence(c float64) float64 {
	if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// ConfidenceFloor clamps an entry's own abstain threshold to 0.70..1.00. An
// unset floor (0) is the review floor.
func ConfidenceFloor(f float64) float64 {
	if f < ReviewFillConfidence || math.IsNaN(f) {
		return ReviewFillConfidence
	}
	if f > 1 {
		return 1
	}
	return f
}
