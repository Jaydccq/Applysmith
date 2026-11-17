package engine

import "encoding/json"

// The shapes below mirror engine/src/protocol.ts: that file is the contract
// the in-page engine and this host both implement.

// Binding names, as the engine calls them in its isolated world.
const (
	BindingMatch    = "__ajMatch"
	BindingResume   = "__ajResume"
	BindingTerminal = "__ajTerminal"
)

// PostingContext describes the posting a form belongs to.
type PostingContext struct {
	Company  string `json:"company,omitempty"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	JDText   string `json:"jdText,omitempty"`
	Location string `json:"location,omitempty"`
}

// WireQuestion is one question the engine asks the matcher.
type WireQuestion struct {
	Selector  string   `json:"selector"`
	Text      string   `json:"text"`
	Section   []string `json:"section,omitempty"`
	Kind      string   `json:"kind"`
	InputType string   `json:"inputType"`
	Options   []string `json:"options"`
	Required  bool     `json:"required"`
	MaxLength int      `json:"maxLength,omitempty"`
	// Description is the text under the question that Text does not hold, at
	// most 4000 characters.
	Description string `json:"description,omitempty"`
}

// MatchRequest is the argument of the match binding.
type MatchRequest struct {
	RunID               string         `json:"runId"`
	Host                string         `json:"host"`
	Posting             PostingContext `json:"posting"`
	Questions           []WireQuestion `json:"questions"`
	CoverLetterAttached bool           `json:"coverLetterAttached,omitempty"`
	ResumeAttached      bool           `json:"resumeAttached,omitempty"`
	// RecordRun is the application run the answers are recorded under (usage
	// events, system writes); RunID is the engine's own fill. Never on the wire.
	RecordRun string `json:"-"`
}

// Resolution is the matcher's answer to one question.
type Resolution struct {
	Selector     string   `json:"selector"`
	Outcome      string   `json:"outcome"`
	Value        string   `json:"value,omitempty"`
	OptionLabel  string   `json:"optionLabel,omitempty"`
	OptionLabels []string `json:"optionLabels,omitempty"`
	// Alternatives are the answer's ranked stand-ins, in order, for a list the
	// matcher was asked without options: the engine takes the first a search
	// holds when it holds none of the answer.
	Alternatives []string `json:"alternatives,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Terminal     bool     `json:"terminal,omitempty"`
	Reviewed     bool     `json:"reviewed,omitempty"`
	// ClientMayOverwrite marks an abstain the profile pass may still fill.
	ClientMayOverwrite bool `json:"clientMayOverwrite,omitempty"`
	// OwnAnswer marks the user's own bank answer for the question, which the
	// consent rules let apply as written.
	OwnAnswer bool `json:"ownAnswer,omitempty"`
	// Overwrite asks for a box this run wrote, a choice included, to be
	// written again: the page changed since, or a review doubts the value. The
	// run's own earlier write does not keep it.
	Overwrite bool `json:"overwrite,omitempty"`
	// Replace writes a value the user's own words name over whatever the box
	// holds, a site's prefill or this run's own write.
	Replace bool `json:"replace,omitempty"`
}

// MatchResponse is the reply of the match binding.
type MatchResponse struct {
	Results []Resolution `json:"results"`
}

// ResumeRequest is the argument of the resume binding.
type ResumeRequest struct {
	RunID string `json:"runId"`
	// Document is "resume", "cover-letter" or "transcript".
	Document string `json:"document"`
	// Format "text" asks for the cover letter's plain text, for a text box the
	// page labels as the letter; MaxLength is that box's limit, 0 for none.
	Format    string `json:"format,omitempty"`
	MaxLength int    `json:"maxLength,omitempty"`
	// Origin is the origin of the frame that asked, set by the host from the
	// binding call and never read from the page's request.
	Origin string `json:"-"`
}

// ResumePayload is a document the engine attaches.
type ResumePayload struct {
	FileName      string `json:"fileName"`
	MediaType     string `json:"mediaType"`
	ContentBase64 string `json:"contentBase64"`
}

// TerminalNotice is the argument of the terminal binding.
type TerminalNotice struct {
	RunID   string   `json:"runId"`
	State   string   `json:"state"`
	Reason  string   `json:"reason,omitempty"`
	Message string   `json:"message,omitempty"`
	Summary *Summary `json:"summary,omitempty"`
}

// Row is one control in a summary.
type Row struct {
	Selector      string   `json:"selector"`
	Label         string   `json:"label"`
	Section       []string `json:"section"`
	Lane          string   `json:"lane"`
	Kind          string   `json:"kind"`
	Required      bool     `json:"required"`
	Outcome       string   `json:"outcome"`
	Review        bool     `json:"review"`
	Reason        string   `json:"reason,omitempty"`
	MatcherReason string   `json:"matcherReason,omitempty"`
	Value         string   `json:"value,omitempty"`
	FileName      string   `json:"fileName,omitempty"`
	OptionLabel   string   `json:"optionLabel,omitempty"`
	// Options are an unanswered choice control's labels (at most 100), so
	// the check can answer it; never a choice.
	Options []string `json:"options,omitempty"`
	// Detail is the site's own message when it refused the value written.
	Detail string `json:"detail,omitempty"`
}

// Counts totals the rows of a summary.
type Counts struct {
	Filled           int `json:"filled"`
	Prefilled        int `json:"prefilled"`
	Review           int `json:"review"`
	Abstained        int `json:"abstained"`
	Failed           int `json:"failed"`
	Skipped          int `json:"skipped"`
	RequiredUnfilled int `json:"requiredUnfilled"`
}

// Summary is the outcome of one frame's fill.
type Summary struct {
	Host             string          `json:"host"`
	URL              string          `json:"url"`
	Rows             []Row           `json:"rows"`
	Counts           Counts          `json:"counts"`
	Drafts           json.RawMessage `json:"drafts,omitempty"`
	MatchUnavailable bool            `json:"matchUnavailable,omitempty"`
	BlockedByDialog  bool            `json:"blockedByDialog,omitempty"`
	NotAnApplication bool            `json:"notAnApplication,omitempty"`
	Partial          bool            `json:"partial,omitempty"`
	// Site is the engine's site table id for the page, when it is in the table.
	Site     string          `json:"site,omitempty"`
	Restored []string        `json:"restored,omitempty"`
	Events   []LearningEvent `json:"events,omitempty"`
}

// LearningEvent is what the engine reports for the host to upload through the
// local API: an uncertainty about a control, a manual fill by the person, or a
// scrubbed note about a control the engine never touches. URLs are origin and
// path only, and a sensitive control carries no value.
type LearningEvent struct {
	EventID      string   `json:"eventId"`
	Kind         string   `json:"kind"`
	Host         string   `json:"host"`
	Site         string   `json:"site,omitempty"`
	URL          string   `json:"url"`
	Selector     string   `json:"selector,omitempty"`
	Question     string   `json:"question"`
	Section      []string `json:"section,omitempty"`
	ControlKind  string   `json:"controlKind"`
	InputType    string   `json:"inputType,omitempty"`
	Autocomplete string   `json:"autocomplete,omitempty"`
	Options      []string `json:"options,omitempty"`
	Time         int64    `json:"time"`
	Reason       string   `json:"reason,omitempty"`
	LabelSource  string   `json:"labelSource,omitempty"`
	Required     bool     `json:"required,omitempty"`
	Current      string   `json:"current,omitempty"`
	Before       string   `json:"before,omitempty"`
	After        string   `json:"after,omitempty"`
	OptionLabel  string   `json:"optionLabel,omitempty"`
	PriorValue   string   `json:"priorValue,omitempty"`
	FillOutcome  string   `json:"fillOutcome,omitempty"`
	TrustedInput bool     `json:"trustedInput,omitempty"`
	Edited       bool     `json:"edited,omitempty"`
	Incomplete   bool     `json:"incomplete,omitempty"`
	Fragment     string   `json:"fragment,omitempty"`
	// FrameURL is set by the handoff recorder only, for an edit inside a
	// same-origin iframe: the frame's origin and path, while URL is the top
	// page's.
	FrameURL string `json:"frameUrl,omitempty"`
}

// SubmitObservation is what the pass-through submit observer saw.
type SubmitObservation struct {
	Time   int64  `json:"time"`
	Kind   string `json:"kind"`
	Method string `json:"method,omitempty"`
	Status int    `json:"status,omitempty"`
	// URL is reduced to origin and path.
	URL string `json:"url"`
}

// Run states reported by the engine's status.
const (
	StateIdle    = "idle"
	StateRunning = "running"
	StatePaused  = "paused"
	StateDone    = "done"
	StateError   = "error"
	StateStopped = "stopped"
)

// Status is the engine's status report.
type Status struct {
	State   string   `json:"state"`
	RunID   string   `json:"runId"`
	Stamp   string   `json:"stamp"`
	Message string   `json:"message"`
	Reason  string   `json:"reason"`
	Summary *Summary `json:"summary"`
}

// Terminal reports whether the state ends a fill.
func (s Status) Terminal() bool {
	return s.State == StateDone || s.State == StateError || s.State == StateStopped
}

// Frame decision outcomes.
const (
	AdmittedRegistry   = "admitted-registry"
	AdmittedProbe      = "admitted-probe"
	SkippedZeroControl = "skipped-zero-controls"
	SkippedDenylist    = "skipped-denylist"
	FailedProbe        = "failed-probe"
	FailedInject       = "failed-inject"
	FailedNoListener   = "failed-no-listener"
)

// FrameDecision is the recorded outcome for one sub-frame.
type FrameDecision struct {
	FrameID  string `json:"frameId"`
	URL      string `json:"url"`
	Outcome  string `json:"outcome"`
	Controls int    `json:"controls,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Admitted reports whether the frame is filled.
func (d FrameDecision) Admitted() bool {
	return d.Outcome == AdmittedRegistry || d.Outcome == AdmittedProbe
}

// Failed reports whether the frame could not be reached.
func (d FrameDecision) Failed() bool {
	return d.Outcome == FailedProbe || d.Outcome == FailedInject || d.Outcome == FailedNoListener
}

type probeResult struct {
	Controls int `json:"controls"`
	Required int `json:"required"`
}

type startReply struct {
	Started bool   `json:"started"`
	Reason  string `json:"reason"`
	RunID   string `json:"runId"`
}
