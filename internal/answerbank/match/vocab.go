package match

// Kind is the answer shape of an entry. The set is closed.
type Kind string

const (
	KindFixed               Kind = "fixed"
	KindPickOption          Kind = "pick-option"
	KindCompensationRange   Kind = "compensation-range"
	KindLanguageProficiency Kind = "language-proficiency"
	KindEmployerHistory     Kind = "employer-history"
	KindAutoAcknowledge     Kind = "auto-acknowledge"
	KindMultiSelect         Kind = "multi-select"
)

// Kinds lists every kind in documentation order.
var Kinds = []Kind{KindFixed, KindPickOption, KindCompensationRange, KindLanguageProficiency, KindEmployerHistory, KindAutoAcknowledge, KindMultiSelect}

// Valid reports whether k is one of the seven kinds.
func (k Kind) Valid() bool { return entryIndexOf(Kinds, k) >= 0 }

// AskMe is the reserved answer meaning "leave this field for the user".
const AskMe = "@ask-me"

// The closed category set. The default bank ships a blank placeholder for
// every category except self-identification (a decline preference) and the
// acknowledgement entries of consent.
const (
	CategoryIdentity             = "identity"
	CategoryContact              = "contact"
	CategoryAddress              = "address"
	CategoryLinks                = "links"
	CategoryEmployment           = "employment"
	CategoryEducation            = "education"
	CategoryWorkAuthorization    = "work-authorization"
	CategoryExperience           = "experience"
	CategoryCompensation         = "compensation"
	CategoryReferences           = "references"
	CategoryLicenses             = "licenses"
	CategorySignature            = "signature"
	CategoryCriminalHistory      = "criminal-history"
	CategorySelfIdentification   = "self-identification"
	CategoryAvailability         = "availability"
	CategorySource               = "source"
	CategoryConsent              = "consent"
	CategoryCompliance           = "compliance"
	CategoryApplicationStatement = "application-statement"
	CategoryOther                = "other"
)

// Categories lists the 20 categories in documentation order.
var Categories = []string{
	CategoryIdentity, CategoryContact, CategoryAddress, CategoryLinks, CategoryEmployment, CategoryEducation,
	CategoryWorkAuthorization, CategoryExperience, CategoryCompensation, CategoryReferences, CategoryLicenses,
	CategorySignature, CategoryCriminalHistory, CategorySelfIdentification, CategoryAvailability, CategorySource,
	CategoryConsent, CategoryCompliance, CategoryApplicationStatement, CategoryOther,
}

// Input types an entry may declare. When none is declared the control-shape
// defaults of the matcher apply.
const (
	InputText             = "text"
	InputLongText         = "long_text"
	InputSingleSelect     = "single_select"
	InputMultiSelect      = "multi_select"
	InputYesNo            = "yes_no"
	InputNumber           = "number"
	InputDate             = "date"
	InputFile             = "file"
	InputURL              = "url"
	InputCheckbox         = "checkbox"
	InputAddress          = "address"
	InputEducationHistory = "education_history"
	InputWorkHistory      = "work_history"
)

// InputTypes lists the 13 input types.
var InputTypes = []string{
	InputText, InputLongText, InputSingleSelect, InputMultiSelect, InputYesNo, InputNumber, InputDate, InputFile,
	InputURL, InputCheckbox, InputAddress, InputEducationHistory, InputWorkHistory,
}

// Entry states and creators.
const (
	StateActive   = "active"
	StateStale    = "stale"
	StateArchived = "archived"

	CreatedByUser  = "user"
	CreatedByAgent = "agent"
)

// States and Creators list the closed sets.
var (
	States   = []string{StateActive, StateStale, StateArchived}
	Creators = []string{CreatedByUser, CreatedByAgent}
)

// Skill screening modes.
const (
	SkillManualYes    = "manual_yes"
	SkillProfileSkill = "profile_skill"
)

// SkillModes lists the closed set.
var SkillModes = []string{SkillManualYes, SkillProfileSkill}

// Numeric-threshold vocabulary.
const (
	ExtractorDaysPerWeekOnsite = "days_per_week_onsite"

	ParseMissNoOp    = "no-op"
	ParseMissAbstain = "abstain"
	ParseMissReview  = "review"
)

// ParseMisses lists the closed set of on_parse_miss values.
var ParseMisses = []string{ParseMissNoOp, ParseMissAbstain, ParseMissReview}

// Location-policy predicates.
const (
	PredicateWillingToWorkAt    = "willing_to_work_at"
	PredicateCurrentlyAt        = "currently_at"
	PredicateRequiresRelocation = "requires_relocation"
)

// Predicates lists the closed set.
var Predicates = []string{PredicateWillingToWorkAt, PredicateCurrentlyAt, PredicateRequiresRelocation}

// Relocation defaults.
const (
	RelocationOpen   = "open"
	RelocationClosed = "closed"
)

// ValidCategory reports membership of the closed category set.
func ValidCategory(c string) bool { return entryIndexOf(Categories, c) >= 0 }

// ValidInputType reports membership of the closed input-type set.
func ValidInputType(t string) bool { return entryIndexOf(InputTypes, t) >= 0 }

func entryIndexOf[T comparable](list []T, v T) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}
