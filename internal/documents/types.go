// Package documents makes the application documents of a candidacy: a frozen
// evidence manifest, one structured résumé and a paired cover letter, a
// grounding gate, the trusted TeX templates, the verified PDFs and the atomic,
// versioned publication of all of it. A model returns structured data only;
// Go encodes every value before it reaches a template.
package documents

import (
	"errors"
	"fmt"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/tracker"
)

// Mode says whether a document claims to be tailored to a posting.
type Mode string

const (
	ModeTailored Mode = "tailored"
	ModeBaseline Mode = "baseline"
)

// Kind is the kind of one stored document.
type Kind string

const (
	KindResume Kind = "resume"
	KindCover  Kind = "cover-letter"
)

// Action is what a run does.
type Action string

const (
	ActionResume Action = "resume"       // a résumé alone
	ActionCover  Action = "cover-letter" // a letter alone
	ActionPair   Action = "pair"         // one résumé and its letter from one manifest
	ActionPDF    Action = "pdf"          // the PDFs of an existing draft version
	ActionEdit   Action = "edit"         // a new draft version holding the user's edit of one document
	// ActionRefine asks the model for a revised draft of one version and keeps
	// it as a pending proposal.
	ActionRefine Action = "refine"
	// ActionInterview builds a new interview prep pack revision for one stage.
	ActionInterview Action = "interview"
)

// Stages of a run, in order. They are the run's stage and the stream's phase.
const (
	StageRetrieve = "retrieve"
	StageProfile  = "profile"
	StageManifest = "manifest"
	StageDraft    = "draft"
	StageReview   = "review"
	StageRevise   = "revise"
	StageCompile  = "compile"
	StageVerify   = "verify"
	StagePublish  = "publish"
)

// ScoreFloor is the floor for a tailored PDF.
const ScoreFloor = 4.5

// What a run does when an application form asks for a cover letter
// (Preferences.CoverWhenAsked): write one whatever the posting scored, the
// owner's default, or only when the posting's documents clear the floor.
const (
	CoverAlways   = "always"
	CoverEligible = "eligible"
)

// PDF states of a stored document.
const (
	PDFNone        = "none"
	PDFReady       = "ready"
	PDFUnverified  = "unverified"
	PDFFailed      = "failed"
	PDFUnavailable = "unavailable"
)

// Generation says who wrote the structured content.
type Generation string

const (
	GenModel         Generation = "model"
	GenDeterministic Generation = "deterministic"
)

// Options are the per-request document options. A zero field takes the
// workspace preference.
type Options struct {
	Language string
	Paper    assets.Paper
	Pages    int
}

// Request is one document run, the same from every ingress.
type Request struct {
	Action Action
	Mode   Mode
	// CandidacyID is required for a tailored run and absent for a baseline.
	CandidacyID int64
	// Posting is the job description; a tailored run without it reads the
	// candidacy's evaluation report instead.
	Posting string
	// PDF compiles and verifies; without it only sources and TeX are written.
	PDF bool
	// DocumentID names the version whose PDFs an ActionPDF run makes.
	DocumentID int64
	// Stage names the interview stage an ActionInterview run prepares.
	Stage string
	// Resume or Letter, exactly one, is the text an ActionEdit run stores as a
	// new version of DocumentID, which must be the same kind.
	Resume *ResumeDraft
	Letter *LetterDraft
	// Instruction and Turns are what an ActionRefine run revises DocumentID by.
	Instruction string
	Turns       []Turn
	// ProposalID makes an ActionEdit run the acceptance of that proposal; its
	// content fills Resume or Letter, and Voice the hash of the voice.md a
	// letter's revision read.
	ProposalID int64
	Voice      string
	// Asked marks a tailored cover letter an application form asks for: under
	// Preferences.CoverWhenAsked "always" it needs no score floor.
	Asked bool
	Options
	Actor tracker.Actor
	// toolchain is the toolchain admission opened for a PDF run; the run
	// builds with it instead of probing the machine again.
	toolchain *toolchain.Toolchain
}

// Preferences are the revisioned workspace defaults.
type Preferences struct {
	Language       string       `json:"language"`
	Paper          assets.Paper `json:"paper"`
	ResumePages    int          `json:"resumePages"`
	CoverWhenAsked string       `json:"coverWhenAsked"`
	Revision       int          `json:"revision"`
}

// Typed refusals. Callers branch with errors.Is.
var (
	ErrInvalid       = errors.New("documents: invalid request")
	ErrNotFound      = errors.New("documents: not found")
	ErrConflict      = errors.New("documents: conflict")
	ErrRunActive     = errors.New("documents: a run for this application is still working")
	ErrFrozen        = errors.New("documents: the version is attached to a submitted application")
	ErrNoPosting     = errors.New("documents: a tailored run needs the job description")
	ErrNoContact     = errors.New("documents: the profile has no name or email for the contact block")
	ErrNoValidDraft  = errors.New("documents: no draft passed the grounding checks")
	ErrQueueFull     = errors.New("documents: the document queue is full")
	ErrBaselineCover = errors.New("documents: a baseline résumé has no company-specific letter")
)

// RunActiveError refuses a run for an application that has one working;
// RunID names that run, so the caller can follow it. It is ErrRunActive.
type RunActiveError struct{ RunID string }

func (e *RunActiveError) Error() string { return ErrRunActive.Error() }
func (e *RunActiveError) Unwrap() error { return ErrRunActive }

// BelowFloorError refuses a tailored PDF for a posting that is not eligible.
type BelowFloorError struct {
	Score  *float64
	Report int64
}

func (e *BelowFloorError) Error() string {
	if e.Report == 0 {
		return fmt.Sprintf("documents: a tailored PDF needs the posting evaluated at %.1f or more, and this application has no evaluation report; evaluate the posting first", ScoreFloor)
	}
	have := "no final-kind report"
	if e.Score != nil {
		have = fmt.Sprintf("score %.1f", *e.Score)
	}
	return fmt.Sprintf("documents: %s is below the %.1f floor for a tailored PDF; override it with `applysmith report override %d` or Allow a PDF anyway in the application's Documents",
		have, ScoreFloor, e.Report)
}

// Is lets errors.Is(err, ErrInvalid) match: the caller fixes the request.
func (e *BelowFloorError) Is(target error) bool { return target == ErrInvalid }

// Result is the outcome of a run.
type Result struct {
	RunID      string
	Published  bool
	Documents  []int64
	Reason     string
	Generation Generation
	Warnings   []string
	Retrieval  Retrieval
	// Pack is the revision an interview run stored.
	Pack *PackRef
	// Proposal is the pending proposal a refine run stored. Such a run counts as
	// published with no document: its product is the proposal.
	Proposal int64
}

// PackRef names a stored interview pack revision.
type PackRef struct {
	Candidacy int64  `json:"candidacyId"`
	Stage     string `json:"stage"`
	Revision  int    `json:"revision"`
}

// Event is one message on the run stream.
type Event struct {
	RunID  string
	Stage  string
	Final  bool
	Result *Result
}

// Run failure reasons stored in the run's failure_reason.
const (
	ReasonCancelled   = "cancelled"
	ReasonInterrupted = "interrupted"
	ReasonTimeout     = "timeout"
	ReasonNoDraft     = "no-valid-draft"
	ReasonNoPosting   = "posting-required"
	ReasonNoContact   = "contact-missing"
	ReasonBelowFloor  = "below-floor"
	ReasonToolchain   = "document-toolchain-unavailable"
	ReasonWrite       = "write-failed"
	ReasonInput       = "input-invalid"
	ReasonStale       = "stale-source"
)

// Retrieval is the metadata of the evidence retrieval: counts and source
// paths, never retrieved text.
type Retrieval struct {
	Query     string          `json:"query"`
	Retrieved int             `json:"retrieved"`
	Items     []RetrievalItem `json:"items"`
}

// RetrievalItem is one retrieved section.
type RetrievalItem struct {
	Source  string   `json:"source"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons,omitempty"`
}
