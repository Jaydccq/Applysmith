package wire

// MemoryFact is one stored statement about the user. Only a trusted fact is
// given to the model; a fact the agent extracted waits for approval.
type MemoryFact struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Subject    string `json:"subject"`
	SubjectRef string `json:"subjectRef,omitempty"`
	Body       string `json:"body"`
	Source     string `json:"source"`
	Trust      string `json:"trust"`
	PII        string `json:"pii"`
	CreatedAt  Time   `json:"createdAt"`
}

// MemoryFactList lists the active facts, newest first, and how full the store is.
type MemoryFactList struct {
	Facts       []MemoryFact `json:"facts"`
	MaxFacts    SafeInt      `json:"maxFacts"`
	MaxFactBody SafeInt      `json:"maxFactBody"`
}

// MemoryFactPatch is the user's edit of a fact's text. The edit makes a new
// version of the fact, trusted because the user wrote it.
type MemoryFactPatch struct {
	Body string `json:"body"`
}

// MemoryFactEdited answers an edit. A text that reads as an instruction to the
// assistant is not stored as a fact: it goes to the review queue and Fact stays
// empty.
type MemoryFactEdited struct {
	Fact     *MemoryFact `json:"fact,omitempty"`
	ReviewID string      `json:"reviewId,omitempty"`
}

// MemoryRule is a piece of operating guidance. Only an active rule reaches the
// prompt, and a rule never changes a gate.
type MemoryRule struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	State     string `json:"state"`
	Source    string `json:"source"`
	CreatedAt Time   `json:"createdAt"`
	UpdatedAt Time   `json:"updatedAt"`
}

// MemoryRuleList lists every rule with the limits.
type MemoryRuleList struct {
	Rules       []MemoryRule `json:"rules"`
	MaxActive   SafeInt      `json:"maxActive"`
	MaxRuleBody SafeInt      `json:"maxRuleBody"`
}

// MemoryRulePatch is the user's edit of a rule's text.
type MemoryRulePatch struct {
	Body string `json:"body"`
}

// MemoryReviewItem is a statement that waits for the user and is not a fact.
type MemoryReviewItem struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Subject    string `json:"subject"`
	SubjectRef string `json:"subjectRef,omitempty"`
	Body       string `json:"body"`
	Reason     string `json:"reason"`
	Source     string `json:"source"`
	CreatedAt  Time   `json:"createdAt"`
}

// MemoryReviewList lists the review queue, oldest first.
type MemoryReviewList struct {
	Items []MemoryReviewItem `json:"items"`
}

// MemoryDeleted answers a deletion or a dismissal.
type MemoryDeleted struct {
	ID string `json:"id"`
}

// MemoryExport is everything the memory holds, for the user to keep.
type MemoryExport struct {
	ExportedAt Time               `json:"exportedAt"`
	Facts      []MemoryFact       `json:"facts"`
	Rules      []MemoryRule       `json:"rules"`
	Review     []MemoryReviewItem `json:"review"`
}

// AgentSettings are the Ask Agent's runtime switches.
type AgentSettings struct {
	Compaction         bool    `json:"compaction"`
	Consolidation      bool    `json:"consolidation"`
	SelfImprove        bool    `json:"selfImprove"`
	TokenBudget        SafeInt `json:"tokenBudget"`
	TokenBudgetMin     SafeInt `json:"tokenBudgetMin"`
	TokenBudgetMax     SafeInt `json:"tokenBudgetMax"`
	TokenBudgetDefault SafeInt `json:"tokenBudgetDefault"`
}

// AgentSettingsPatch changes some of the switches; an absent field stays.
type AgentSettingsPatch struct {
	Compaction    *bool    `json:"compaction,omitempty"`
	Consolidation *bool    `json:"consolidation,omitempty"`
	SelfImprove   *bool    `json:"selfImprove,omitempty"`
	TokenBudget   *SafeInt `json:"tokenBudget,omitempty"`
}

// AgentHarness is the self-improvement view: the harness versions, the
// rounds that proposed them and the lessons kept for later rounds.
type AgentHarness struct {
	// Active is the id of the active version; 0 is the empty harness.
	Active   SafeInt               `json:"active"`
	Running  bool                  `json:"running"`
	Versions []AgentHarnessVersion `json:"versions"`
	Rounds   []AgentImproveRound   `json:"rounds"`
	Lessons  []AgentLesson         `json:"lessons"`
}

// AgentHarnessVersion is one harness version.
type AgentHarnessVersion struct {
	ID        SafeInt           `json:"id"`
	ParentID  SafeInt           `json:"parentId"`
	Source    string            `json:"source"`
	Summary   string            `json:"summary"`
	Guidance  []string          `json:"guidance"`
	ToolNotes map[string]string `json:"toolNotes"`
	Active    bool              `json:"active"`
	CreatedAt Time              `json:"createdAt"`
}

// AgentImproveRound is one self-improvement round.
type AgentImproveRound struct {
	ID         string  `json:"id"`
	StartedAt  Time    `json:"startedAt"`
	FinishedAt *Time   `json:"finishedAt,omitempty"`
	Outcome    string  `json:"outcome"`
	Detail     string  `json:"detail"`
	Adopted    SafeInt `json:"adopted"`
	Probes     SafeInt `json:"probes"`
}

// AgentLesson is one lesson the rounds keep for their proposer.
type AgentLesson struct {
	ID         string  `json:"id"`
	Cue        string  `json:"cue"`
	Lesson     string  `json:"lesson"`
	Evidence   string  `json:"evidence"`
	Confidence string  `json:"confidence"`
	Version    SafeInt `json:"version"`
	Undone     bool    `json:"undone"`
	CreatedAt  Time    `json:"createdAt"`
}

// AgentHarnessRestore names the version to make active again; 0 is the
// empty harness.
type AgentHarnessRestore struct {
	ID SafeInt `json:"id"`
}
