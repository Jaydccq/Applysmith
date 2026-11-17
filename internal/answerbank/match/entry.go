package match

import (
	"regexp"
	"slices"
	"strings"
)

// Entry is one bank entry: an identity, a kind, the answer fields of that
// kind and optional metadata.
type Entry struct {
	ID          string `yaml:"id" json:"id"`
	Description string `yaml:"description" json:"description"`
	Kind        Kind   `yaml:"kind" json:"kind"`
	Body        `yaml:",inline"`
	Meta        `yaml:",inline"`

	// Runtime values are computed at load time and never read from or written
	// to the file.
	Runtime `yaml:"-" json:"-"`
}

// Body holds the answer fields. Which of them an entry carries depends on its
// Kind (see KindFields). A nil slice means the field is absent; for
// Selected, absent means unanswered while an empty list means "none of them".
type Body struct {
	Answer string `yaml:"answer" json:"answer,omitempty"`

	Preferred         []string `yaml:"preferred" json:"preferred"`
	FallbackText      string   `yaml:"fallback_text" json:"fallback_text,omitempty"`
	FallbackAnyOption bool     `yaml:"fallback_any_option" json:"fallback_any_option,omitempty"`

	Minimum       string `yaml:"minimum" json:"minimum,omitempty"`
	Maximum       string `yaml:"maximum" json:"maximum,omitempty"`
	DefaultAnswer string `yaml:"default_answer" json:"default_answer,omitempty"`
	Currency      string `yaml:"currency" json:"currency,omitempty"`

	Language           string   `yaml:"language" json:"language,omitempty"`
	Speaking           string   `yaml:"speaking" json:"speaking,omitempty"`
	Listening          string   `yaml:"listening" json:"listening,omitempty"`
	Reading            string   `yaml:"reading" json:"reading,omitempty"`
	Writing            string   `yaml:"writing" json:"writing,omitempty"`
	ProficiencyOptions []string `yaml:"proficiency_options" json:"proficiency_options"`

	AnswerIfMatch      string `yaml:"answer_if_match" json:"answer_if_match,omitempty"`
	AnswerIfNoMatch    string `yaml:"answer_if_no_match" json:"answer_if_no_match,omitempty"`
	AbstainIfAmbiguous *bool  `yaml:"abstain_if_ambiguous" json:"abstain_if_ambiguous,omitempty"`

	Selected []string `yaml:"selected" json:"selected"`
}

// AbstainsWhenAmbiguous reads the employer-history flag, whose default is true.
func (b Body) AbstainsWhenAmbiguous() bool {
	return b.AbstainIfAmbiguous == nil || *b.AbstainIfAmbiguous
}

// Meta is the optional metadata of an entry.
type Meta struct {
	Category   string   `yaml:"category,omitempty" json:"category,omitempty"`
	AnswerKey  string   `yaml:"answer_key,omitempty" json:"answer_key,omitempty"`
	InputTypes []string `yaml:"input_types,omitempty" json:"input_types,omitempty"`
	Confidence *float64 `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	// ConfidenceFloor is stored as written; use Floor for the effective value.
	ConfidenceFloor *float64 `yaml:"confidence_floor,omitempty" json:"confidence_floor,omitempty"`
	Examples        []string `yaml:"examples,omitempty" json:"examples,omitempty"`

	ExceptionTerms        []string `yaml:"exception_terms,omitempty" json:"exception_terms,omitempty"`
	AnswerIfException     string   `yaml:"answer_if_exception,omitempty" json:"answer_if_exception,omitempty"`
	QuestionNegationTerms []string `yaml:"question_negation_terms,omitempty" json:"question_negation_terms,omitempty"`

	NumericThreshold *NumericThreshold `yaml:"numeric_threshold,omitempty" json:"numeric_threshold,omitempty"`
	LocationPolicy   *LocationPolicy   `yaml:"location_policy,omitempty" json:"location_policy,omitempty"`
	CapturedScope    []ScopeItem       `yaml:"captured_scope,omitempty" json:"captured_scope,omitempty"`

	AllowedOptions      []string `yaml:"allowed_options,omitempty" json:"allowed_options,omitempty"`
	LinkedExplainID     string   `yaml:"linked_explain_id,omitempty" json:"linked_explain_id,omitempty"`
	SkillMode           string   `yaml:"skill_mode,omitempty" json:"skill_mode,omitempty"`
	SkillTerms          []string `yaml:"skill_terms,omitempty" json:"skill_terms,omitempty"`
	SkillBlacklistTerms []string `yaml:"skill_blacklist_terms,omitempty" json:"skill_blacklist_terms,omitempty"`
	SkillNegativeAnswer string   `yaml:"skill_negative_answer,omitempty" json:"skill_negative_answer,omitempty"`

	Placeholder bool   `yaml:"placeholder,omitempty" json:"placeholder,omitempty"`
	NeedsReview bool   `yaml:"needs_review,omitempty" json:"needs_review,omitempty"`
	Pinned      bool   `yaml:"pinned,omitempty" json:"pinned,omitempty"`
	State       string `yaml:"state,omitempty" json:"state,omitempty"`
	CreatedBy   string `yaml:"created_by,omitempty" json:"created_by,omitempty"`

	CreatedAt            string `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt            string `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	StaleAt              string `yaml:"stale_at,omitempty" json:"stale_at,omitempty"`
	ArchivedAt           string `yaml:"archived_at,omitempty" json:"archived_at,omitempty"`
	StaleCorrectionCount int    `yaml:"stale_correction_count,omitempty" json:"stale_correction_count,omitempty"`
}

// NumericThreshold answers an above-maximum value differently from the
// entry's own answer. MaxValue is stamped at load from the profile.
type NumericThreshold struct {
	Extractor          string `yaml:"extractor" json:"extractor"`
	MaxValueProfileKey string `yaml:"max_value_profile_key,omitempty" json:"max_value_profile_key,omitempty"`
	AboveAnswer        string `yaml:"above_answer,omitempty" json:"above_answer,omitempty"`
	OnParseMiss        string `yaml:"on_parse_miss,omitempty" json:"on_parse_miss,omitempty"`

	MaxValue *int `yaml:"-" json:"-"`
}

// MissMode reads on_parse_miss with its default.
func (t NumericThreshold) MissMode() string {
	if t.OnParseMiss == "" {
		return ParseMissNoOp
	}
	return t.OnParseMiss
}

// LocationPolicy binds an entry to the relocation policy. Resolved is
// attached at load and is nil while the policy is unconfigured.
type LocationPolicy struct {
	Predicate string `yaml:"predicate" json:"predicate"`
	IfTrue    string `yaml:"if_true,omitempty" json:"if_true,omitempty"`
	IfFalse   string `yaml:"if_false,omitempty" json:"if_false,omitempty"`

	Resolved *RelocationPolicy `yaml:"-" json:"-"`
}

// RelocationPolicy is the policy built from preferences.relocation and the
// home address of the profile. Place strings are as the user wrote them; alias
// expansion belongs to the matcher.
type RelocationPolicy struct {
	Default  string
	Places   []string
	Excluded []string
	Home     Home
}

// Home is the profile address reduced to the places that identify "home".
type Home struct {
	City    string
	County  string
	Region  string
	Country string
}

// ScopeItem binds an agent-captured entry to one host, one question text and
// one option set.
type ScopeItem struct {
	Host     string   `yaml:"host" json:"host"`
	Question string   `yaml:"question" json:"question"`
	Options  []string `yaml:"options,omitempty" json:"options,omitempty"`
}

// Runtime holds the values computed from other inputs at load time.
type Runtime struct {
	// RunContext marks the synthetic per-run entries (cover-letter-submitted,
	// resume-submitted): never stored, reached only by the statement rule.
	RunContext bool

	// NegationPatterns are the compiled valid question_negation_terms.
	NegationPatterns []*regexp.Regexp
	// StampedBody marks an entry whose body was replaced or created by a
	// load-time stamp, so a save never writes the stamped body.
	StampedBody bool
	// StampedCurrency marks a pay expectation whose blank currency the load
	// gave the targets step's, so a save writes it blank again.
	StampedCurrency bool
	// DerivedFrom names the setup step whose statement the load stamped as
	// the body; empty for every other entry.
	DerivedFrom string
	// Shipped marks an entry the load took from the shipped defaults: one the
	// file lacks, or a blank placeholder given the answer its default ships.
	Shipped bool
}

// EffectiveAnswerKey is the declared key, else the id with every '-' as '_'.
func (e Entry) EffectiveAnswerKey() string {
	if e.AnswerKey != "" {
		return e.AnswerKey
	}
	return strings.ReplaceAll(e.ID, "-", "_")
}

// EffectiveState reads the state with its default.
func (e Entry) EffectiveState() string {
	if e.State == "" {
		return StateActive
	}
	return e.State
}

// EffectiveCreatedBy reads created_by with its default.
func (e Entry) EffectiveCreatedBy() string {
	if e.CreatedBy == "" {
		return CreatedByUser
	}
	return e.CreatedBy
}

// Archived reports whether the entry is excluded from matching.
func (e Entry) Archived() bool { return e.State == StateArchived }

// Floor returns the entry's own abstain threshold, clamped to 0.70..1.00 by
// ConfidenceFloor; ok is false when the entry sets none.
func (e Entry) Floor() (floor float64, ok bool) {
	if e.ConfidenceFloor == nil {
		return 0, false
	}
	return ConfidenceFloor(*e.ConfidenceFloor), true
}

// AsksMe reports whether the entry holds the ask-me marker in the position
// its kind allows.
// A value counts as the marker after surrounding whitespace is trimmed.
func (e Entry) AsksMe() bool {
	switch e.Kind {
	case KindFixed:
		return IsAskMe(e.Answer)
	case KindPickOption:
		return len(e.Preferred) == 1 && IsAskMe(e.Preferred[0])
	case KindCompensationRange:
		return IsAskMe(e.DefaultAnswer)
	case KindMultiSelect:
		return len(e.Selected) == 1 && IsAskMe(e.Selected[0])
	}
	return false
}

// IsAskMe reports whether v is the ask-me marker, ignoring surrounding
// whitespace.
func IsAskMe(v string) bool { return strings.TrimSpace(v) == AskMe }

// StrayAskMe reports whether the entry holds the marker inside a list of
// several values, where it is a literal option label and not the marker.
func (e Entry) StrayAskMe() bool {
	if e.AsksMe() {
		return false
	}
	switch e.Kind {
	case KindPickOption:
		return slices.ContainsFunc(e.Preferred, IsAskMe)
	case KindMultiSelect:
		return slices.ContainsFunc(e.Selected, IsAskMe)
	}
	return false
}

// BodyEmpty reports whether every answer field of the entry's kind is empty.
// An auto-acknowledge entry has no answer fields and its body is never empty;
// a multi-select body is empty only when Selected is absent.
func (e Entry) BodyEmpty() bool {
	switch e.Kind {
	case KindFixed:
		return entryBlank(e.Answer)
	case KindPickOption:
		return !entryAnyText(e.Preferred) && entryBlank(e.FallbackText) && !e.FallbackAnyOption
	case KindCompensationRange:
		return entryBlank(e.Minimum) && entryBlank(e.Maximum) && entryBlank(e.DefaultAnswer) && entryBlank(e.Currency)
	case KindLanguageProficiency:
		return entryBlank(e.Language) && entryBlank(e.Speaking) && entryBlank(e.Listening) && entryBlank(e.Reading) &&
			entryBlank(e.Writing) && !entryAnyText(e.ProficiencyOptions)
	case KindEmployerHistory:
		return entryBlank(e.AnswerIfMatch) && entryBlank(e.AnswerIfNoMatch)
	case KindMultiSelect:
		return e.Selected == nil
	}
	return false
}

func entryBlank(s string) bool { return strings.TrimSpace(s) == "" }

func entryAnyText(list []string) bool {
	for _, s := range list {
		if !entryBlank(s) {
			return true
		}
	}
	return false
}
