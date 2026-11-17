package wire

// GoalStep is one ordered step of a goal. The link to an application or a
// company is a label for the user and authorizes nothing.
type GoalStep struct {
	Text          string `json:"text"`
	Status        string `json:"status,omitempty"`
	Note          string `json:"note,omitempty"`
	ApplicationID string `json:"applicationId,omitempty"`
	Company       string `json:"company,omitempty"`
}

// GoalCadence is the local weekdays (sun..sat) and the local time (HH:MM) of
// check-ins; both empty means none.
type GoalCadence struct {
	Days []string `json:"days"`
	Time string   `json:"time"`
}

// GoalProposal is a plan change the agent proposed and the user has not
// decided. Steps replace the steps that are not done; DueDate and Cadence
// replace the goal's when present.
type GoalProposal struct {
	ID        string       `json:"id"`
	Reason    string       `json:"reason"`
	Steps     []GoalStep   `json:"steps"`
	DueDate   *string      `json:"dueDate,omitempty"`
	Cadence   *GoalCadence `json:"cadence,omitempty"`
	CreatedAt Time         `json:"createdAt"`
}

// Goal is a job-search goal. NextCheckin is the next slot of an active goal
// with a cadence.
type Goal struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	DueDate     string        `json:"dueDate,omitempty"`
	State       string        `json:"state"`
	Version     SafeInt       `json:"version"`
	Cadence     GoalCadence   `json:"cadence"`
	Steps       []GoalStep    `json:"steps"`
	Proposal    *GoalProposal `json:"proposal,omitempty"`
	NextCheckin *Time         `json:"nextCheckin,omitempty"`
	CreatedAt   Time          `json:"createdAt"`
	UpdatedAt   Time          `json:"updatedAt"`
}

// GoalList lists the goals, the active ones first.
type GoalList struct {
	Goals []Goal `json:"goals"`
}

// GoalCreate creates an active goal.
type GoalCreate struct {
	Title   string       `json:"title"`
	DueDate string       `json:"dueDate,omitempty"`
	Steps   []GoalStep   `json:"steps,omitempty"`
	Cadence *GoalCadence `json:"cadence,omitempty"`
}

// GoalPatch is the user's edit of a goal; an absent field is left alone, an
// empty dueDate or an empty cadence clears it, and steps replace all steps.
type GoalPatch struct {
	Title   *string      `json:"title,omitempty"`
	DueDate *string      `json:"dueDate,omitempty"`
	Cadence *GoalCadence `json:"cadence,omitempty"`
	State   *string      `json:"state,omitempty"`
	Steps   *[]GoalStep  `json:"steps,omitempty"`
}

// GoalDeleted answers a deletion.
type GoalDeleted struct {
	ID string `json:"id"`
}

// AgentTaskStep is one step of a delegated task's plan; status is pending,
// doing, done or skipped.
type AgentTaskStep struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// AgentTask is a delegated task of the Ask Agent: an outcome the user handed
// to it in one conversation, which it works over several turns. State is
// working, waiting_runs, waiting_you, paused, done or stopped. Mandate is the
// user's own message that delegated it; Question is what it waits on, Outcome
// what it achieved. Turns counts the turns it took by itself, out of MaxTurns.
// Idea names the Home item it was handed over from, if any.
type AgentTask struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	Idea      string          `json:"idea,omitempty"`
	Title     string          `json:"title"`
	Mandate   string          `json:"mandate"`
	State     string          `json:"state"`
	Steps     []AgentTaskStep `json:"steps"`
	Question  string          `json:"question,omitempty"`
	Outcome   string          `json:"outcome,omitempty"`
	Turns     SafeInt         `json:"turns"`
	MaxTurns  SafeInt         `json:"maxTurns"`
	CreatedAt Time            `json:"createdAt"`
	UpdatedAt Time            `json:"updatedAt"`
}

// AgentTaskList lists the open tasks, newest first, then the latest closed
// ones.
type AgentTaskList struct {
	Tasks []AgentTask `json:"tasks"`
}

// AgentIdeaStarted names the conversation and the task a Home item was handed
// over to ("Let the agent do it"), or the ones already working on it.
type AgentIdeaStarted struct {
	SessionID string `json:"sessionId"`
	TaskID    string `json:"taskId"`
}
