package wire

// ScheduleProposalChange is what accepting a proposal would do to its
// schedule: a new enabled flag, a new cadence (the schedule's own timezone
// stays when none is given), or both.
type ScheduleProposalChange struct {
	Enabled *bool           `json:"enabled,omitempty"`
	Preset  *SchedulePreset `json:"preset,omitempty"`
}

// ScheduleProposal is a schedule change the agent proposed and the user has
// not decided. It changes nothing until accepted.
type ScheduleProposal struct {
	ID           string                 `json:"id"`
	ScheduleID   SafeInt                `json:"scheduleId"`
	ScheduleName string                 `json:"scheduleName,omitempty"`
	Reason       string                 `json:"reason"`
	Change       ScheduleProposalChange `json:"change"`
	CreatedAt    Time                   `json:"createdAt"`
}

// ScheduleProposalList lists the pending proposals, newest first.
type ScheduleProposalList struct {
	Proposals []ScheduleProposal `json:"proposals"`
}

// ScheduleProposalDecided answers an accept or a dismiss. The schedule is the
// changed one after an accept.
type ScheduleProposalDecided struct {
	ID       string    `json:"id"`
	Accepted bool      `json:"accepted"`
	Schedule *Schedule `json:"schedule,omitempty"`
}
