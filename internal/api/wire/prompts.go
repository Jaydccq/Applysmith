package wire

// States of a scheduled prompt the user can switch between.
const (
	PromptActive = "active"
	PromptPaused = "paused"
)

// PromptTriggerSpec is what a Gmail signal trigger fires on: any of the
// signals, optionally only for one employer. The signals are
// interview_scheduled, recruiter_reply_needed, rejection and online_assessment.
type PromptTriggerSpec struct {
	Signals []string `json:"signals"`
	Company string   `json:"company,omitempty"`
}

// PromptLast is the result of a prompt's latest run: answered, failed or
// timed_out, with the conversation it ran in.
type PromptLast struct {
	At        Time   `json:"at"`
	Status    string `json:"status"`
	SessionID string `json:"sessionId,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// PromptFiring is one firing of a trigger. Draft is a reply the user reviews
// and sends themselves; the product sends nothing. ProposedStatus is a
// proposal that also waits in the needs-you inbox.
type PromptFiring struct {
	SignalType     string `json:"signalType"`
	Employer       string `json:"employer,omitempty"`
	State          string `json:"state"`
	Detail         string `json:"detail,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	Draft          string `json:"draft,omitempty"`
	ProposedStatus string `json:"proposedStatus,omitempty"`
	ProposedReason string `json:"proposedReason,omitempty"`
	At             Time   `json:"at"`
}

// AgentPrompt is a scheduled prompt: on a cadence or on a Gmail signal.
// NextRun is the next slot of an active cadence prompt. Firings are the
// latest of a trigger, newest first.
type AgentPrompt struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	Text      string             `json:"text"`
	State     string             `json:"state"`
	Origin    string             `json:"origin"`
	Cadence   *GoalCadence       `json:"cadence,omitempty"`
	Trigger   *PromptTriggerSpec `json:"trigger,omitempty"`
	NextRun   *Time              `json:"nextRun,omitempty"`
	Last      *PromptLast        `json:"last,omitempty"`
	Firings   []PromptFiring     `json:"firings"`
	CreatedAt Time               `json:"createdAt"`
	UpdatedAt Time               `json:"updatedAt"`
}

// AgentPromptList lists the scheduled prompts of both kinds; Limit is how
// many may exist.
type AgentPromptList struct {
	Prompts []AgentPrompt `json:"prompts"`
	Limit   SafeInt       `json:"limit"`
}

// AgentPromptCreate creates an active prompt. Give a cadence or a trigger.
type AgentPromptCreate struct {
	Text    string             `json:"text"`
	Cadence *GoalCadence       `json:"cadence,omitempty"`
	Trigger *PromptTriggerSpec `json:"trigger,omitempty"`
}

// AgentPromptPatch edits a prompt; an absent field is left alone. State
// moves an active prompt to paused and a paused one to active.
type AgentPromptPatch struct {
	Text    *string            `json:"text,omitempty"`
	Cadence *GoalCadence       `json:"cadence,omitempty"`
	Trigger *PromptTriggerSpec `json:"trigger,omitempty"`
	State   *string            `json:"state,omitempty"`
}

// AgentPromptDeleted answers a deletion.
type AgentPromptDeleted struct {
	ID string `json:"id"`
}
