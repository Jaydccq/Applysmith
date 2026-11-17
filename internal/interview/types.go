// Package interview prepares a candidate for the interview stages of a
// candidacy: it records stages, feedback and mock turns as an append-only
// event log, builds immutable prep pack revisions from the sources frozen at
// submission, and coaches mock answers. Everything a pack says comes from the
// user's own sources; a model may only reword questions and propose hooks that
// cite one.
package interview

import (
	"errors"
	"fmt"

	"applysmith/internal/lazyregexp"
)

// Bounds of the specification.
const (
	MaxBriefChars    = 1200
	MaxHooks         = 8
	MaxLineChars     = 500
	MaxClaims        = 12
	MaxQuestions     = 20
	MaxCategoryChars = 80
	MaxStoriesPerQ   = 5
	MaxRefsPerQ      = 8
	MaxFeedbackItems = 10
	MaxFeedbackChars = 1200
	MaxGaps          = 12
	MaxActions       = 12
	MaxSources       = 24
	MaxLabelChars    = 160
	MaxInterviewers  = 10
	MaxInterviewer   = 120
	MaxFeedbackIn    = 2000
	MaxMockQuestion  = 1000
	MaxMockAnswer    = 4000
	MaxCoaching      = 2000
	MaxEventBytes    = 16000
	ViewLimit        = 20
	// RequirementQuestions is how many top job requirements get a question.
	RequirementQuestions = 5
)

// Stage types and formats.
const (
	TypePhoneScreen  = "phone_screen"
	TypeTechnical    = "technical"
	TypeSystemDesign = "system_design"
	TypeBehavioral   = "behavioral"
	TypeCase         = "case"
	TypeOnsite       = "onsite"
	TypeFinal        = "final"
	TypeOther        = "other"
)

var (
	stageTypes   = []string{TypePhoneScreen, TypeTechnical, TypeSystemDesign, TypeBehavioral, TypeCase, TypeOnsite, TypeFinal, TypeOther}
	stageFormats = []string{"phone", "video", "onsite", "unknown"}
	stageIDRE    = lazyregexp.New(`^[a-z0-9-]{2,64}$`)
)

// Source reference kinds and verification states.
const (
	KindDossier      = "dossier"
	KindReport       = "report"
	KindCompanyBrief = "company-brief"
	KindStory        = "story"
	KindFeedback     = "feedback"

	StateFrozen        = "frozen"
	StateCurrent       = "current"
	StateUserConfirmed = "user-confirmed"
	StateUnverified    = "unverified"
)

// Feedback origins.
const (
	FromUser        = "user"
	FromEmail       = "email"
	FromInterviewer = "interviewer"
)

// Generation says who worded the pack.
const (
	GenModel         = "model"
	GenDeterministic = "deterministic"
)

// Typed failures. Callers branch with errors.Is.
var (
	ErrInvalid  = errors.New("interview: invalid input")
	ErrNotFound = errors.New("interview: not found")
	ErrTooLarge = errors.New("interview: the event is larger than 16,000 bytes")
	ErrConflict = errors.New("interview: changed since it was read")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Stage is one interview round of a candidacy.
type Stage struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	At            string   `json:"at,omitempty"`
	Format        string   `json:"format"`
	Interviewers  []string `json:"interviewers"`
	UserConfirmed bool     `json:"userConfirmed"`
	Cancelled     bool     `json:"cancelled,omitempty"`
	RecordedAt    string   `json:"recordedAt"`
	// Revision is the id of the event that recorded this version.
	Revision int64 `json:"-"`
}

// Feedback is something said about a stage.
type Feedback struct {
	ID         int64  `json:"id"`
	Stage      string `json:"stage"`
	From       string `json:"from"`
	Text       string `json:"text"`
	RecordedAt string `json:"recordedAt"`
}

// MockTurn is one practice answer with its coaching.
type MockTurn struct {
	ID         int64  `json:"id"`
	Stage      string `json:"stage"`
	Question   string `json:"question"`
	Answer     string `json:"answer"`
	Coaching   string `json:"coaching"`
	RecordedAt string `json:"recordedAt"`
}

// SourceRef is one source a pack rests on.
type SourceRef struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
	State string `json:"state"`
}

// Hook is a verified company hook; every hook cites at least one source.
type Hook struct {
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
}

// Question is a likely interview question with the stories that answer it.
type Question struct {
	Category string   `json:"category"`
	Prompt   string   `json:"prompt"`
	StoryIDs []string `json:"storyIds"`
	Sources  []string `json:"sources"`
}

// The kinds of a pack step: where its fix lives. Target is the requirement
// (StepStory), the company (StepBrief) or the stage (StepConfirm,
// StepPractice, StepDebrief); the others act on the candidacy itself.
const (
	StepStory     = "story"     // bank a story for a requirement
	StepBrief     = "brief"     // build the company brief
	StepEvaluate  = "evaluate"  // evaluate the posting
	StepDocuments = "documents" // the candidacy's résumé and cover letter
	StepConfirm   = "confirm"   // confirm the details of a stage
	StepPractice  = "practice"  // practise answers with mock turns
	StepDebrief   = "debrief"   // record how a stage went
)

var stepKinds = []string{StepStory, StepBrief, StepEvaluate, StepDocuments, StepConfirm, StepPractice, StepDebrief}

// The kinds only a workspace's next actions take: add a stage, and build the
// pack of the stage named by Target.
const (
	StepStage = "stage"
	StepPack  = "pack"
)

// Step is a gap or next action of a pack or of the workspace: the sentence,
// and the kind and target of what to do about it, so a view can link the
// place that fixes it.
type Step struct {
	Text   string `json:"text"`
	Kind   string `json:"kind,omitempty"`
	Target string `json:"target,omitempty"`
}

// Texts are the sentences of steps, for a plain-text view.
func Texts(steps []Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Text
	}
	return out
}

// Pack is one immutable revision of a stage's prep pack.
type Pack struct {
	Candidacy     int64       `json:"candidacyId"`
	Stage         string      `json:"stage"`
	Revision      int         `json:"revision"`
	Generation    string      `json:"generation"`
	CreatedAt     string      `json:"createdAt"`
	RoleBrief     string      `json:"roleBrief"`
	CompanyBrief  string      `json:"companyBrief"`
	Hooks         []Hook      `json:"hooks"`
	Claims        []string    `json:"claims"`
	Questions     []Question  `json:"questions"`
	PriorFeedback []string    `json:"priorFeedback"`
	Gaps          []Step      `json:"gaps"`
	NextActions   []Step      `json:"nextActions"`
	Sources       []SourceRef `json:"sources"`
	// ReportNumber is the evaluation report the pack read, so a view can say a
	// newer one is linked to the row now.
	ReportNumber int64 `json:"reportNumber,omitempty"`
	// Warnings say what the build could not do, such as a model reply it
	// refused; they are not part of the specified pack.
	Warnings []string `json:"warnings,omitempty"`
}

// PackInfo identifies a stored revision.
type PackInfo struct {
	Stage      string `json:"stage"`
	Revision   int    `json:"revision"`
	Generation string `json:"generation"`
	CreatedAt  string `json:"createdAt"`
}

// Workspace is the view of a candidacy's interview records.
type Workspace struct {
	CandidacyID int64      `json:"candidacyId"`
	Stages      []Stage    `json:"stages"`
	Cancelled   []Stage    `json:"cancelled"`
	Feedback    []Feedback `json:"feedback"`
	MockTurns   []MockTurn `json:"mockTurns"`
	Packs       []PackInfo `json:"packs"`
	// Degraded says some records could not be read; Repair names why.
	Degraded    bool   `json:"degraded"`
	Repair      string `json:"repair,omitempty"`
	NextActions []Step `json:"nextActions"`
}
