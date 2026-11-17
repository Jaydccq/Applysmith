package wire

// InterviewStageRequest records an interview stage of a candidacy. The id is
// 2 to 64 lowercase letters, digits and hyphens; the type is phone_screen,
// technical, system_design, behavioral, case, onsite, final or other; the
// format is phone, video, onsite or unknown; at is an RFC 3339 time.
// Recording an id again replaces it (Cancelled cancels or restores it), against
// ExpectedRevision, the revision read (0 for a new id), else conflict.
type InterviewStageRequest struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	At               string   `json:"at,omitempty"`
	Format           string   `json:"format,omitempty"`
	Interviewers     []string `json:"interviewers,omitempty"`
	UserConfirmed    bool     `json:"userConfirmed,omitempty"`
	Cancelled        bool     `json:"cancelled,omitempty"`
	ExpectedRevision *SafeInt `json:"expectedRevision"`
}

// InterviewStage is a recorded stage.
type InterviewStage struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	At            string   `json:"at,omitempty"`
	Format        string   `json:"format"`
	Interviewers  []string `json:"interviewers"`
	UserConfirmed bool     `json:"userConfirmed"`
	Cancelled     bool     `json:"cancelled,omitempty"`
	Revision      SafeInt  `json:"revision"`
	RecordedAt    Time     `json:"recordedAt"`
}

// InterviewPackRequest starts a prep pack run for the stage in the path. The
// body is an empty object that keeps the JSON contract of the other
// generation routes.
type InterviewPackRequest struct{}

// InterviewFeedbackRequest records feedback for the stage in the path. From
// is user, email or interviewer and defaults to user.
type InterviewFeedbackRequest struct {
	From string `json:"from,omitempty"`
	Text string `json:"text"`
}

// InterviewFeedback is recorded feedback.
type InterviewFeedback struct {
	ID         SafeInt `json:"id"`
	Stage      string  `json:"stage"`
	From       string  `json:"from"`
	Text       string  `json:"text"`
	RecordedAt Time    `json:"recordedAt"`
}

// InterviewMockTurnRequest is a practice answer to coach; it starts a run.
type InterviewMockTurnRequest struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// InterviewMockTurn is a practice answer with its coaching.
type InterviewMockTurn struct {
	ID         SafeInt `json:"id"`
	Stage      string  `json:"stage"`
	Question   string  `json:"question"`
	Answer     string  `json:"answer"`
	Coaching   string  `json:"coaching"`
	RecordedAt Time    `json:"recordedAt"`
}

// InterviewSourceRef is one source a pack rests on.
type InterviewSourceRef struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
	State string `json:"state"`
}

// InterviewHook is a company hook with the sources it cites.
type InterviewHook struct {
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
}

// InterviewQuestion is a likely question with the stories that answer it.
type InterviewQuestion struct {
	Category string   `json:"category"`
	Prompt   string   `json:"prompt"`
	StoryIDs []string `json:"storyIds"`
	Sources  []string `json:"sources"`
}

// InterviewStep is a gap or next action of a pack or a workspace: its text,
// and the kind and target of its fix (interview.Step).
type InterviewStep struct {
	Text   string `json:"text"`
	Kind   string `json:"kind,omitempty"`
	Target string `json:"target,omitempty"`
}

// InterviewPack is one immutable revision of a stage's prep pack.
type InterviewPack struct {
	CandidacyID   SafeInt              `json:"candidacyId"`
	Stage         string               `json:"stage"`
	Revision      int                  `json:"revision"`
	Generation    string               `json:"generation"`
	CreatedAt     Time                 `json:"createdAt"`
	RoleBrief     string               `json:"roleBrief"`
	CompanyBrief  string               `json:"companyBrief"`
	Hooks         []InterviewHook      `json:"hooks"`
	Claims        []string             `json:"claims"`
	Questions     []InterviewQuestion  `json:"questions"`
	PriorFeedback []string             `json:"priorFeedback"`
	Gaps          []InterviewStep      `json:"gaps"`
	NextActions   []InterviewStep      `json:"nextActions"`
	Sources       []InterviewSourceRef `json:"sources"`
	Warnings      []string             `json:"warnings"`
	// ReportNumber is the evaluation report the pack read; absent when it read
	// the submitted copy of the posting.
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
}

// InterviewPackInfo identifies a stored pack revision.
type InterviewPackInfo struct {
	Stage      string `json:"stage"`
	Revision   int    `json:"revision"`
	Generation string `json:"generation"`
	CreatedAt  Time   `json:"createdAt"`
}

// InterviewWorkspace is the latest stages, feedback, mock turns and packs of a
// candidacy. A candidacy with no interview records returns empty lists and a
// next action; Cancelled holds the cancelled stages. Degraded says some
// records could not be read; Repair names why.
type InterviewWorkspace struct {
	CandidacyID SafeInt             `json:"candidacyId"`
	Stages      []InterviewStage    `json:"stages"`
	Cancelled   []InterviewStage    `json:"cancelled"`
	Feedback    []InterviewFeedback `json:"feedback"`
	MockTurns   []InterviewMockTurn `json:"mockTurns"`
	Packs       []InterviewPackInfo `json:"packs"`
	Degraded    bool                `json:"degraded"`
	Repair      string              `json:"repair,omitempty"`
	NextActions []InterviewStep     `json:"nextActions"`
	// PackRun is the prep pack run of this application still working.
	PackRun *InterviewPackRun `json:"packRun,omitempty"`
	// CoachRun is the coaching of a practice answer still working.
	CoachRun *InterviewCoachRun `json:"coachRun,omitempty"`
}

// InterviewPackRun is a prep pack run still working and the stage it builds.
type InterviewPackRun struct {
	RunID string `json:"runId"`
	Stage string `json:"stage"`
}

// InterviewCoachRun is a practice answer being coached in the background.
type InterviewCoachRun struct {
	RunID    string `json:"runId"`
	Stage    string `json:"stage"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// InterviewPackRef names a stored pack revision.
type InterviewPackRef struct {
	CandidacyID SafeInt `json:"candidacyId"`
	Stage       string  `json:"stage"`
	Revision    int     `json:"revision"`
}

// InterviewsSoon answers GET /api/interviews/soon: the stages, not cancelled,
// of open applications that fall on today or tomorrow (local days), soonest
// first.
type InterviewsSoon struct {
	Items []InterviewSoon `json:"items"`
}

// InterviewSoon is one such stage. Packed says a prep pack exists for it,
// Debriefed that feedback was recorded for it.
type InterviewSoon struct {
	CandidacyID SafeInt        `json:"candidacyId"`
	Company     string         `json:"company"`
	Role        string         `json:"role"`
	Stage       InterviewStage `json:"stage"`
	Packed      bool           `json:"packed"`
	Debriefed   bool           `json:"debriefed"`
}
