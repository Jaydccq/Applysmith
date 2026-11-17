package wire

// SchedulePreset is the recurrence of a schedule: daily and weekly carry a
// local time (HH:MM) and weekly its weekdays (sun to sat); rrule carries an
// RFC 5545 rule without a start line. Timezone is an IANA zone name.
type SchedulePreset struct {
	Kind     string   `json:"kind"`
	Time     string   `json:"time,omitempty"`
	Weekdays []string `json:"weekdays,omitempty"`
	RRule    string   `json:"rrule,omitempty"`
	Timezone string   `json:"timezone"`
}

// Schedule is one stored schedule with what the list adds: the next
// occurrences of an enabled schedule, a flag for a kind that no longer exists
// and the problem that keeps it from being evaluated, if any. LastReason and
// LastDetail say why the newest decision ended as it did (a code such as
// error, cancelled or needs-sign-in, and its account); empty for a success.
type Schedule struct {
	ID              SafeInt        `json:"id"`
	Name            string         `json:"name"`
	Kind            string         `json:"kind"`
	Inputs          map[string]any `json:"inputs"`
	Preset          SchedulePreset `json:"preset"`
	Enabled         bool           `json:"enabled"`
	MissedPolicy    string         `json:"missedPolicy"`
	DTStart         Time           `json:"dtstart"`
	PackID          string         `json:"packId,omitempty"`
	PackKey         string         `json:"packKey,omitempty"`
	LastFiredAt     *Time          `json:"lastFiredAt,omitempty"`
	LastEvaluatedAt *Time          `json:"lastEvaluatedAt,omitempty"`
	ActiveRunID     string         `json:"activeRunId,omitempty"`
	LastRunID       string         `json:"lastRunId,omitempty"`
	LastResult      string         `json:"lastResult,omitempty"`
	LastSkipReason  string         `json:"lastSkipReason,omitempty"`
	LastResultAt    *Time          `json:"lastResultAt,omitempty"`
	LastReason      string         `json:"lastReason,omitempty"`
	LastDetail      string         `json:"lastDetail,omitempty"`
	CreatedAt       Time           `json:"createdAt"`
	UpdatedAt       Time           `json:"updatedAt"`
	Next            []Time         `json:"next"`
	UnknownKind     bool           `json:"unknownKind,omitempty"`
	Problem         string         `json:"problem,omitempty"`
	// PausedBy says which bulk pause switched the schedule off, in the list
	// only: "all" for Pause all, or the job kind a lost connection paused
	// ("gmail.sync" when Gmail was disconnected). Empty for any other.
	PausedBy string `json:"pausedBy,omitempty"`
	// Attention is what the read-only hygiene report asks of this schedule
	// (a duplicate, a dead lane, one that never ran), in the list only.
	Attention []string `json:"attention,omitempty"`
}

// ScheduleTimer is a built-in timer, shown read-only. An inactive timer says
// why.
type ScheduleTimer struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	Reason      string `json:"reason,omitempty"`
}

// ScheduleList answers GET /api/schedules: the user's schedules and, in a
// separate read-only section, the built-in timers.
type ScheduleList struct {
	Schedules []Schedule      `json:"schedules"`
	Timers    []ScheduleTimer `json:"timers"`
	// PausedAll counts the schedules a pause-all switched off that resume-all
	// would switch back on.
	PausedAll SafeInt `json:"pausedAll,omitempty"`
}

// SchedulePauseResult answers the bulk pause and resume: the names of the
// schedules that were switched off or on.
type SchedulePauseResult struct {
	Changed []string `json:"changed"`
}

// ScheduleCreate creates a schedule. Enabled defaults to true, MissedPolicy to
// skip, and the recurrence starts at the time of the create.
type ScheduleCreate struct {
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Inputs       map[string]any `json:"inputs,omitempty"`
	Preset       SchedulePreset `json:"preset"`
	Enabled      *bool          `json:"enabled,omitempty"`
	MissedPolicy string         `json:"missedPolicy,omitempty"`
}

// SchedulePatch changes parts of a schedule. An absent field stays; an empty
// inputs object clears the inputs. The result is validated as a whole.
type SchedulePatch struct {
	Name         *string         `json:"name,omitempty"`
	Kind         *string         `json:"kind,omitempty"`
	Inputs       map[string]any  `json:"inputs,omitempty"`
	Preset       *SchedulePreset `json:"preset,omitempty"`
	Enabled      *bool           `json:"enabled,omitempty"`
	MissedPolicy *string         `json:"missedPolicy,omitempty"`
}

// ScheduleField is one input a job kind accepts.
type ScheduleField struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Enum []string `json:"enum,omitempty"`
	Min  SafeInt  `json:"min,omitempty"`
	Max  SafeInt  `json:"max,omitempty"`
	Doc  string   `json:"doc,omitempty"`
}

// ScheduleKind is one job kind of the registry. Title is its human name, the
// name itself for a kind that has none.
type ScheduleKind struct {
	Name     string          `json:"name"`
	Title    string          `json:"title"`
	Doc      string          `json:"doc"`
	Leases   []string        `json:"leases"`
	Network  bool            `json:"network"`
	Artifact string          `json:"artifact,omitempty"`
	Inputs   []ScheduleField `json:"inputs"`
}

// ScheduleKindList answers GET /api/schedule-kinds.
type ScheduleKindList struct {
	Kinds []ScheduleKind `json:"kinds"`
}

// ScheduleHistoryRow is one decision about an occurrence. Outcome is fired,
// manual, skipped or interrupted; deferred and ended explain a decision.
type ScheduleHistoryRow struct {
	ID           SafeInt `json:"id"`
	ScheduledFor Time    `json:"scheduledFor"`
	Outcome      string  `json:"outcome"`
	Reason       string  `json:"reason,omitempty"`
	Detail       string  `json:"detail,omitempty"`
	RunID        string  `json:"runId,omitempty"`
	StartedAt    *Time   `json:"startedAt,omitempty"`
	FinishedAt   *Time   `json:"finishedAt,omitempty"`
	Attempts     SafeInt `json:"attempts"`
	Summary      string  `json:"summary,omitempty"`
	CreatedAt    Time    `json:"createdAt"`
}

// ScheduleHistory answers GET /api/schedules/{id}/history, newest first.
type ScheduleHistory struct {
	History []ScheduleHistoryRow `json:"history"`
}

// SchedulePackEntry previews one schedule a pack would create.
type SchedulePackEntry struct {
	Key    string         `json:"key"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
	Preset SchedulePreset `json:"preset"`
	Policy string         `json:"missedPolicy"`
	// Available is false when the job kind does not exist yet.
	Available bool `json:"available"`
	// Next lists the first fire times the entry would have if applied now.
	Next []Time `json:"next"`
}

// SchedulePack is a named list of schedules defined as data.
type SchedulePack struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Setup       bool                `json:"setup"`
	Applied     SafeInt             `json:"applied"`
	Schedules   []SchedulePackEntry `json:"schedules"`
}

// SchedulePackList answers GET /api/schedule-packs. Listing writes nothing.
type SchedulePackList struct {
	Packs []SchedulePack `json:"packs"`
}

// SchedulePackUnavailable is a pack entry whose job kind does not exist yet.
type SchedulePackUnavailable struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// SchedulePackResult says what applying a pack did.
type SchedulePackResult struct {
	Created     []string                  `json:"created"`
	Updated     []string                  `json:"updated"`
	Removed     []string                  `json:"removed"`
	Unavailable []SchedulePackUnavailable `json:"unavailable"`
}

// SchedulePackDeleted names the schedules that carried a pack's marker.
type SchedulePackDeleted struct {
	Removed []string `json:"removed"`
}

// SchedulePreviewRequest asks for the next fire times of a recurrence that is
// not saved yet, starting now.
type SchedulePreviewRequest struct {
	Preset SchedulePreset `json:"preset"`
}

// SchedulePreview lists up to five fire times after now.
type SchedulePreview struct {
	Next []Time `json:"next"`
}
