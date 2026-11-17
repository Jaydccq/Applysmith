package wire

// AnswerBankGap is one entry that needs the user's answer or review. Blocking
// marks a personal entry whose gap stops setup.
type AnswerBankGap struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Kind     string `json:"kind"`
	Blocking bool   `json:"blocking"`
}

// AnswerBankUnanswered is the one definition of "needs the user's answer".
// Blocking repeats the blocking members of Unanswered; NeedsReview holds
// defaults the user has not confirmed; AskMe holds entries the user chose to
// decide on every form.
type AnswerBankUnanswered struct {
	Unanswered  []AnswerBankGap `json:"unanswered"`
	Blocking    []AnswerBankGap `json:"blocking"`
	NeedsReview []AnswerBankGap `json:"needsReview"`
	AskMe       []string        `json:"askMe"`
	// Derived lists the entries answered from a setup step, never asked again.
	Derived []AnswerBankDerived `json:"derived"`
	// Asked lists the questions waiting runs left that the bank cannot answer
	// yet, each once.
	Asked []AnswerBankAsked `json:"asked"`
}

// AnswerBankAsked is a question stopped runs wait on that no entry answers or
// that reaches a blank entry. EntryID names the blank entry when the answer is
// written into it, which then answers every wording that reaches it. It is
// answered through RunID, the newest waiting run that left it, in that run's
// wording; Runs counts the waiting runs. Everywhere is the starting choice for
// the answer's scope (F-4).
type AnswerBankAsked struct {
	Question   string   `json:"question"`
	Kind       string   `json:"kind"`
	Options    []string `json:"options,omitempty"`
	EntryID    string   `json:"entryId,omitempty"`
	RunID      string   `json:"runId"`
	Host       string   `json:"host"`
	Runs       SafeInt  `json:"runs"`
	Everywhere bool     `json:"everywhere"`
}

// AnswerBankDerived is an entry the file leaves blank and the bank answers
// with what a structured setup step already states. Step is the setup step id;
// Answer is the derived answer as text. An answer written in the bank replaces
// it.
type AnswerBankDerived struct {
	ID     string `json:"id"`
	Step   string `json:"step"`
	Answer string `json:"answer"`
}

// AnswerBankSettings are the runtime settings of the answer bank.
type AnswerBankSettings struct {
	LLMFallbackEnabled          bool   `json:"llmFallbackEnabled"`
	GroundingMode               string `json:"groundingMode"`
	AutoAcceptLegalDeclarations bool   `json:"autoAcceptLegalDeclarations"`
	AllowGoogleSignIn           bool   `json:"allowGoogleSignIn"`
	WatchScans                  bool   `json:"watchScans"`
	KnowledgeSemanticRetrieval  bool   `json:"knowledgeSemanticRetrieval"`
}

// AnswerBankSettingsPatch changes the fields that are present. An unknown
// field, a non-boolean flag or an unknown grounding mode is validation_failed
// and nothing changes.
type AnswerBankSettingsPatch struct {
	LLMFallbackEnabled          *bool   `json:"llmFallbackEnabled,omitempty"`
	GroundingMode               *string `json:"groundingMode,omitempty"`
	AutoAcceptLegalDeclarations *bool   `json:"autoAcceptLegalDeclarations,omitempty"`
	AllowGoogleSignIn           *bool   `json:"allowGoogleSignIn,omitempty"`
	WatchScans                  *bool   `json:"watchScans,omitempty"`
	KnowledgeSemanticRetrieval  *bool   `json:"knowledgeSemanticRetrieval,omitempty"`
}

// AnswerBankTranscript is the academic transcript an application run attaches
// to a transcript upload slot. FileName is empty when none is stored.
type AnswerBankTranscript struct {
	FileName string `json:"fileName"`
}

// The bank travels with the keys of answer-bank.yml, so what the dashboard
// shows is what the file holds. A test keeps every type below in step with the
// entry types of internal/answerbank/match.

// AnswerBankBody holds the answer fields of every entry kind; an entry uses
// the ones its kind names. A list that is null is absent; an empty list is an
// answer (for selected, "none of them").
type AnswerBankBody struct {
	Answer             string   `json:"answer,omitempty"`
	Preferred          []string `json:"preferred" tstype:"string[] | null"`
	FallbackText       string   `json:"fallback_text,omitempty"`
	FallbackAnyOption  bool     `json:"fallback_any_option,omitempty"`
	Minimum            string   `json:"minimum,omitempty"`
	Maximum            string   `json:"maximum,omitempty"`
	DefaultAnswer      string   `json:"default_answer,omitempty"`
	Currency           string   `json:"currency,omitempty"`
	Language           string   `json:"language,omitempty"`
	Speaking           string   `json:"speaking,omitempty"`
	Listening          string   `json:"listening,omitempty"`
	Reading            string   `json:"reading,omitempty"`
	Writing            string   `json:"writing,omitempty"`
	ProficiencyOptions []string `json:"proficiency_options" tstype:"string[] | null"`
	AnswerIfMatch      string   `json:"answer_if_match,omitempty"`
	AnswerIfNoMatch    string   `json:"answer_if_no_match,omitempty"`
	AbstainIfAmbiguous *bool    `json:"abstain_if_ambiguous,omitempty"`
	Selected           []string `json:"selected" tstype:"string[] | null"`
}

// AnswerBankNumericThreshold answers an above-maximum value differently.
type AnswerBankNumericThreshold struct {
	Extractor          string `json:"extractor"`
	MaxValueProfileKey string `json:"max_value_profile_key,omitempty"`
	AboveAnswer        string `json:"above_answer,omitempty"`
	OnParseMiss        string `json:"on_parse_miss,omitempty"`
}

// AnswerBankLocationPolicy binds an entry to the relocation policy.
type AnswerBankLocationPolicy struct {
	Predicate string `json:"predicate"`
	IfTrue    string `json:"if_true,omitempty"`
	IfFalse   string `json:"if_false,omitempty"`
}

// AnswerBankScopeItem binds an entry to one host, one question and one option set.
type AnswerBankScopeItem struct {
	Host     string   `json:"host"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// AnswerBankEntry is one bank entry: an identity, a kind, the answer fields of
// that kind (those of AnswerBankBody) and the optional metadata. The fields are
// listed flat because the generated TypeScript has no inline embedding.
type AnswerBankEntry struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Kind        string `json:"kind"`

	Answer             string   `json:"answer,omitempty"`
	Preferred          []string `json:"preferred" tstype:"string[] | null"`
	FallbackText       string   `json:"fallback_text,omitempty"`
	FallbackAnyOption  bool     `json:"fallback_any_option,omitempty"`
	Minimum            string   `json:"minimum,omitempty"`
	Maximum            string   `json:"maximum,omitempty"`
	DefaultAnswer      string   `json:"default_answer,omitempty"`
	Currency           string   `json:"currency,omitempty"`
	Language           string   `json:"language,omitempty"`
	Speaking           string   `json:"speaking,omitempty"`
	Listening          string   `json:"listening,omitempty"`
	Reading            string   `json:"reading,omitempty"`
	Writing            string   `json:"writing,omitempty"`
	ProficiencyOptions []string `json:"proficiency_options" tstype:"string[] | null"`
	AnswerIfMatch      string   `json:"answer_if_match,omitempty"`
	AnswerIfNoMatch    string   `json:"answer_if_no_match,omitempty"`
	AbstainIfAmbiguous *bool    `json:"abstain_if_ambiguous,omitempty"`
	Selected           []string `json:"selected" tstype:"string[] | null"`

	Category   string   `json:"category,omitempty"`
	AnswerKey  string   `json:"answer_key,omitempty"`
	InputTypes []string `json:"input_types,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`

	ConfidenceFloor *float64 `json:"confidence_floor,omitempty"`
	Examples        []string `json:"examples,omitempty"`

	ExceptionTerms        []string `json:"exception_terms,omitempty"`
	AnswerIfException     string   `json:"answer_if_exception,omitempty"`
	QuestionNegationTerms []string `json:"question_negation_terms,omitempty"`

	NumericThreshold *AnswerBankNumericThreshold `json:"numeric_threshold,omitempty"`
	LocationPolicy   *AnswerBankLocationPolicy   `json:"location_policy,omitempty"`
	CapturedScope    []AnswerBankScopeItem       `json:"captured_scope,omitempty"`

	AllowedOptions      []string `json:"allowed_options,omitempty"`
	LinkedExplainID     string   `json:"linked_explain_id,omitempty"`
	SkillMode           string   `json:"skill_mode,omitempty"`
	SkillTerms          []string `json:"skill_terms,omitempty"`
	SkillBlacklistTerms []string `json:"skill_blacklist_terms,omitempty"`
	SkillNegativeAnswer string   `json:"skill_negative_answer,omitempty"`

	Placeholder bool   `json:"placeholder,omitempty"`
	NeedsReview bool   `json:"needs_review,omitempty"`
	Pinned      bool   `json:"pinned,omitempty"`
	State       string `json:"state,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`

	CreatedAt            string `json:"created_at,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
	StaleAt              string `json:"stale_at,omitempty"`
	ArchivedAt           string `json:"archived_at,omitempty"`
	StaleCorrectionCount int    `json:"stale_correction_count,omitempty"`
}

// AnswerBankEmployer is a canonical employer name and its aliases.
type AnswerBankEmployer struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

// AnswerBankOverride patches the named answer fields of one entry for pages on
// a host. Fields lists the keys of Body the override states.
type AnswerBankOverride struct {
	ID     string         `json:"id"`
	Kind   string         `json:"kind,omitempty"`
	Body   AnswerBankBody `json:"body"`
	Fields []string       `json:"fields"`
}

// AnswerBank is the whole bank. Overrides map a host, in the case the file
// spells it, to its overrides in order.
type AnswerBank struct {
	Version   int                             `json:"version"`
	Employers []AnswerBankEmployer            `json:"employers"`
	Entries   []AnswerBankEntry               `json:"entries"`
	Overrides map[string][]AnswerBankOverride `json:"overrides"`
}

// AnswerBankFile is the bank as the file holds it with the shipped defaults
// applied, without the values the profile stamps at load time, with the
// revision a save must quote. Exists is false while the file is absent;
// Warnings are what the load repaired. DefaultIDs are the ids of the shipped
// default entries: one the bank lacks is in it as shipped, so a deleted or
// renamed default is back at the next load. Shapes says, by entry id, what
// each entry's answer can be (answerbank.ShapeOf): the page offers exactly
// that, and a save of a changed answer outside it is refused.
type AnswerBankFile struct {
	Exists     bool                       `json:"exists"`
	Revision   string                     `json:"revision"`
	Warnings   []string                   `json:"warnings"`
	Bank       AnswerBank                 `json:"bank"`
	DefaultIDs []string                   `json:"defaultIds"`
	Shapes     map[string]AnswerBankShape `json:"shapes"`
}

// AnswerBankShape is what one entry's answer can be: yes-no, choice (one of
// Choices), choices (several of them), date, url, number, email, text,
// long-text, list, range, employer (Yes or No twice), language (a level of
// Choices for each skill) or none.
type AnswerBankShape struct {
	Type    string             `json:"type"`
	Choices []AnswerBankChoice `json:"choices"`
}

// AnswerBankChoice is one answer of a shape: the label the page shows, and
// the option labels the page writes for it, best first, which the matcher
// looks for on a form.
type AnswerBankChoice struct {
	Label   string   `json:"label"`
	Matches []string `json:"matches"`
}

// AnswerBankSave replaces the bank when ExpectedRevision is still the file's
// revision; otherwise the save is revision_conflict and nothing is written.
type AnswerBankSave struct {
	ExpectedRevision string     `json:"expectedRevision"`
	Bank             AnswerBank `json:"bank"`
}

// AnswerBankSaved reports the revision after a save. Changed is false when the
// saved bank equals the file.
type AnswerBankSaved struct {
	Revision string `json:"revision"`
	Changed  bool   `json:"changed"`
}

// Outcomes of a compose draft.
const (
	ComposeFilled    = "filled-model"
	ComposeAbstained = "abstained"
)

// ComposeNoRoute is the reason a compose draft is withheld when no model is
// routed for the task.
const ComposeNoRoute = "no-route"

// AnswerComposePosting is the posting a draft is written for. Description is
// sanitized before it reaches a prompt; ApplicationURL and JobURL name the
// pages that may declare the company's own website.
type AnswerComposePosting struct {
	Company        string `json:"company,omitempty"`
	Role           string `json:"role,omitempty"`
	Description    string `json:"description,omitempty"`
	ApplicationURL string `json:"applicationUrl,omitempty"`
	JobURL         string `json:"jobUrl,omitempty"`
}

// AnswerComposeRequest asks for a draft answer to one free-text question. A
// rewrite also carries the current text and an instruction; neither is a
// grounding fact.
type AnswerComposeRequest struct {
	Question    string               `json:"question"`
	Host        string               `json:"host"`
	MaxLength   int                  `json:"maxLength,omitempty"`
	Posting     AnswerComposePosting `json:"posting"`
	Current     string               `json:"current,omitempty"`
	Instruction string               `json:"instruction,omitempty"`
}

// AnswerComposeResult is a draft and never a write. Outcome filled-model
// carries the answer and the keys of the facts it cites; abstained carries the
// reason.
type AnswerComposeResult struct {
	Outcome string   `json:"outcome"`
	Answer  string   `json:"answer,omitempty"`
	Sources []string `json:"sources"`
	Reason  string   `json:"reason,omitempty"`
}

// AnswerMatchOption is one rendered choice of a control.
type AnswerMatchOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// AnswerMatchQuestion is one control of the page. InputType may be empty; the
// matcher then infers it from the question text and the options.
type AnswerMatchQuestion struct {
	Selector  string              `json:"selector"`
	Text      string              `json:"text"`
	Section   []string            `json:"section,omitempty"`
	Kind      string              `json:"kind"`
	InputType string              `json:"inputType,omitempty"`
	Options   []AnswerMatchOption `json:"options,omitempty"`
	Required  bool                `json:"required,omitempty"`
	MaxLength int                 `json:"maxLength,omitempty"`
}

// AnswerMatchPosting is the posting the form belongs to. Location is the page's
// own hint; the location of the job the URL resolves to outranks it.
type AnswerMatchPosting struct {
	Company     string `json:"company,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	URL         string `json:"url,omitempty"`
}

// AnswerMatchRequest carries up to 300 questions of one page. The attachment
// fields are observations of the run: absent means not observed.
type AnswerMatchRequest struct {
	Host                string                `json:"host"`
	RunID               string                `json:"runId"`
	Posting             AnswerMatchPosting    `json:"posting"`
	CoverLetterAttached *bool                 `json:"coverLetterAttached,omitempty"`
	ResumeAttached      *bool                 `json:"resumeAttached,omitempty"`
	Questions           []AnswerMatchQuestion `json:"questions"`
}

// AnswerMatchItem is the answer to one question. ClientMayOverwrite marks an
// abstain that the client's own profile pass may still fill; OwnAnswer marks the
// user's own bank answer for the question, which the consent rules let apply as
// written.
type AnswerMatchItem struct {
	Selector           string   `json:"selector"`
	Outcome            string   `json:"outcome"`
	Answer             string   `json:"answer"`
	ChosenLabel        string   `json:"chosenLabel,omitempty"`
	ChosenLabels       []string `json:"chosenLabels,omitempty"`
	EntryID            string   `json:"entryId,omitempty"`
	AnswerKey          string   `json:"answerKey,omitempty"`
	Source             string   `json:"source,omitempty"`
	Confidence         float64  `json:"confidence"`
	Reason             string   `json:"reason,omitempty"`
	Detail             string   `json:"detail,omitempty"`
	Terminal           bool     `json:"terminal"`
	Trace              []string `json:"trace"`
	Cited              []string `json:"cited,omitempty"`
	ClientMayOverwrite bool     `json:"clientMayOverwrite"`
	OwnAnswer          bool     `json:"ownAnswer"`
}

// AnswerMatchResult holds one item per question, in request order.
type AnswerMatchResult struct {
	Results []AnswerMatchItem `json:"results"`
}
