package wire

// ReasonToolchainUnavailable is the reason of the unavailable error a PDF
// request gets when the document tools are missing; the message names them.
const ReasonToolchainUnavailable = "document-toolchain-unavailable"

// Reasons of a refused PDF.
const ReasonBelowScoreFloor = "below-score-floor"

// ReasonProposalPending is the reason of the conflict a PDF or a second
// revision request gets while a revision proposal waits for a decision.
const ReasonProposalPending = "proposal-pending"

// ReasonDocumentRunActive is the reason of the conflict a document request
// gets while a run for the same application works; details.runId names it.
const ReasonDocumentRunActive = "document-run-active"

// DocumentResumeRequest starts a résumé run, with its paired letter when
// includeCoverLetter is set. Exactly one of candidacyId and baseline is
// given. Paper is a4 or letter, pages 1 or 2; an omitted option takes the
// workspace preference.
type DocumentResumeRequest struct {
	CandidacyID        SafeInt `json:"candidacyId,omitempty"`
	Baseline           bool    `json:"baseline,omitempty"`
	IncludeCoverLetter bool    `json:"includeCoverLetter,omitempty"`
	Posting            string  `json:"posting,omitempty"`
	PDF                bool    `json:"pdf,omitempty"`
	Paper              string  `json:"paper,omitempty"`
	Pages              int     `json:"pages,omitempty"`
	Language           string  `json:"language,omitempty"`
}

// DocumentCoverRequest starts a letter run. It has no page limit: a letter is
// one page.
type DocumentCoverRequest struct {
	CandidacyID SafeInt `json:"candidacyId"`
	Posting     string  `json:"posting,omitempty"`
	PDF         bool    `json:"pdf,omitempty"`
	Paper       string  `json:"paper,omitempty"`
	Language    string  `json:"language,omitempty"`
}

// DocumentPDFRequest starts the PDF run of a stored version. The version is
// named by the path and the options were fixed when it was made, so the body
// is an empty object that keeps the JSON contract of the other generation
// routes.
type DocumentPDFRequest struct{}

// DocumentBullet is one sentence of a document with the evidence ids it rests
// on. An edit keeps the ids; the grounding gate refuses text they no longer
// support.
type DocumentBullet struct {
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

// DocumentEntryContent holds the bullets of one selected résumé entry.
type DocumentEntryContent struct {
	Entry   string           `json:"entry"`
	Bullets []DocumentBullet `json:"bullets"`
}

// DocumentCompetency is one labelled skill line of a résumé.
type DocumentCompetency struct {
	Label    string   `json:"label"`
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

// DocumentResumeContent is the editable text of a résumé. Contact details,
// titles, organisations and dates are not in it: they come from the evidence
// manifest.
type DocumentResumeContent struct {
	Profile         string                 `json:"profile"`
	ProfileEvidence []string               `json:"profileEvidence"`
	Competencies    []DocumentCompetency   `json:"competencies"`
	Experience      []DocumentEntryContent `json:"experience"`
	Projects        []DocumentEntryContent `json:"projects"`
}

// DocumentLetterContent is the editable text of a cover letter. The salutation,
// the company paragraph and the sign-off are written by the system.
type DocumentLetterContent struct {
	Opening         string           `json:"opening"`
	OpeningEvidence []string         `json:"openingEvidence"`
	Body            string           `json:"body"`
	Bullets         []DocumentBullet `json:"bullets"`
	Closing         string           `json:"closing"`
	CompanyFacts    []string         `json:"companyFacts"`
}

// DocumentSource answers GET /api/documents/source/{id}: the stored text of one
// version. Exactly one of resume and letter is set, by the document's kind.
// Text is the readable rendering. Frozen says the version is attached to a
// submitted application, so it cannot be edited. Evidence holds each source
// line the content cites, which a sentence's evidence ids name.
type DocumentSource struct {
	ID       SafeInt                `json:"id"`
	Kind     string                 `json:"kind"`
	Version  int                    `json:"version"`
	Frozen   bool                   `json:"frozen"`
	Text     string                 `json:"text"`
	Resume   *DocumentResumeContent `json:"resume,omitempty"`
	Letter   *DocumentLetterContent `json:"letter,omitempty"`
	Evidence []DocumentEvidence     `json:"evidence"`
}

// DocumentEvidence is one line of the user's material a document cites: its
// id, where it comes from (file#heading) and its text.
type DocumentEvidence struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

// DocumentEditRequest stores an edit of one document as a new draft version
// without a PDF. Send the content of the document's own kind: resume for a
// résumé, letter for a cover letter. The version the edit started from is
// never changed.
type DocumentEditRequest struct {
	Resume *DocumentResumeContent `json:"resume,omitempty"`
	Letter *DocumentLetterContent `json:"letter,omitempty"`
}

// DocumentView is one stored version of a résumé or letter. Paths are
// workspace-relative; no content is returned.
type DocumentView struct {
	ID           SafeInt  `json:"id"`
	Bundle       string   `json:"bundle"`
	Version      int      `json:"version"`
	Kind         string   `json:"kind"`
	Mode         string   `json:"mode"`
	CandidacyID  *SafeInt `json:"candidacyId,omitempty"`
	Path         string   `json:"path"`
	SourcePath   string   `json:"sourcePath"`
	PDFPath      string   `json:"pdfPath,omitempty"`
	PDFState     string   `json:"pdfState"`
	Pages        int      `json:"pages,omitempty"`
	Paper        string   `json:"paper"`
	PageLimit    int      `json:"pageLimit"`
	Language     string   `json:"language"`
	Generation   string   `json:"generation"`
	Warnings     []string `json:"warnings"`
	ContentHash  string   `json:"contentHash"`
	PDFSHA256    string   `json:"pdfSha256,omitempty"`
	ManifestHash string   `json:"manifestHash"`
	Current      bool     `json:"current"`
	Ready        bool     `json:"ready"`
	Frozen       bool     `json:"frozen"`
	// RefinePending says a revision proposal waits for a decision, which makes
	// the PDF of this version unavailable.
	RefinePending bool `json:"refinePending"`
	CreatedAt     Time `json:"createdAt"`
	// Verification is present when the version holds a PDF.
	Verification *DocumentVerification `json:"verification,omitempty"`
	// Review is the independent content review stored with the version.
	Review *DocumentReview `json:"review,omitempty"`
	// SourcesChanged names what this current version was written from that
	// changed since (cv.md, profile.yml, voice.md, the evaluation report).
	SourcesChanged []string `json:"sourcesChanged,omitempty"`
}

// ContentReviewUnavailable is the status of an independent content review
// that did not run; such a review is never approved.
const ContentReviewUnavailable = "unavailable"

// DocumentReview summarises the one model review of a version and its at most
// one revision. It is an additional content check; the mechanical checks of the
// PDF are separate.
type DocumentReview struct {
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Revisions int    `json:"revisions"`
	Factual   int    `json:"factual"`
	Narrative int    `json:"narrative"`
	Rejected  int    `json:"rejected"`
}

// ReviewPerformed is the visual review state of a PDF a review looked at. The
// mechanical checks never set it.
const ReviewPerformed = "performed"

// DocumentCheck is one verdict of one tool on the PDF. Reason may quote a few
// words of the document.
type DocumentCheck struct {
	Tool   string `json:"tool"`
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// DocumentPreview is one rasterised page of the PDF and its hash.
type DocumentPreview struct {
	Page   int    `json:"page"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// DocumentVisualReview is a person's look at the previews of one PDF. It is
// separate from the mechanical checks and performed only for the exact hash.
type DocumentVisualReview struct {
	Status    string `json:"status"`
	PDFSHA256 string `json:"pdfSha256,omitempty"`
}

// DocumentVerifiers names the scripts and tools that gave the verdict.
type DocumentVerifiers struct {
	Rules         string `json:"rules"`
	PDFScript     string `json:"pdfScript"`
	LayoutScript  string `json:"layoutScript"`
	LayoutProfile string `json:"layoutProfile"`
	Python        string `json:"python,omitempty"`
	Pypdf         string `json:"pypdf,omitempty"`
	Poppler       string `json:"poppler,omitempty"`
}

// DocumentVerification is the verdict stored for one PDF, bound to the hashes
// of the PDF, its TeX and text, the structured source and the evidence
// manifest. When stale is set the record does not describe the stored files,
// so state is unverified and the checks are empty.
type DocumentVerification struct {
	State        string               `json:"state"`
	Stale        string               `json:"stale,omitempty"`
	Pages        int                  `json:"pages"`
	PDFSHA256    string               `json:"pdfSha256"`
	TeXSHA256    string               `json:"texSha256,omitempty"`
	SourceSHA256 string               `json:"sourceSha256,omitempty"`
	ManifestHash string               `json:"manifestHash,omitempty"`
	Template     string               `json:"template,omitempty"`
	Verifiers    DocumentVerifiers    `json:"verifiers"`
	Checks       []DocumentCheck      `json:"checks"`
	Previews     []DocumentPreview    `json:"previews"`
	VisualReview DocumentVisualReview `json:"visualReview"`
}

// DocumentList answers GET /api/documents.
type DocumentList struct {
	Documents []DocumentView `json:"documents"`
	// ActiveRunID is the document run of the asked candidacy that is still
	// working; absent when none is.
	ActiveRunID string `json:"activeRunId,omitempty"`
	// NoReport says the asked candidacy links no evaluation report, so no PDF
	// can be made for it and there is no floor to override: the posting must be
	// evaluated first.
	NoReport bool `json:"noReport,omitempty"`
	// BelowFloor says the asked candidacy's report scored below the floor for
	// a tailored PDF and no override is recorded, so a run is refused until the
	// user allows one.
	BelowFloor bool `json:"belowFloor,omitempty"`
}

// DocumentPreferences are the workspace document defaults.
type DocumentPreferences struct {
	Language    string `json:"language"`
	Paper       string `json:"paper"`
	ResumePages int    `json:"resumePages"`
	// CoverWhenAsked is "always" (a form that asks for a cover letter gets one
	// whatever the posting scored) or "eligible" (only above the score floor).
	CoverWhenAsked string `json:"coverWhenAsked"`
	Revision       int    `json:"revision"`
}

// DocumentPreferencesPatch edits the defaults; it carries the revision it was
// read at, and an omitted field stays.
type DocumentPreferencesPatch struct {
	Language       *string `json:"language,omitempty"`
	Paper          *string `json:"paper,omitempty"`
	ResumePages    *int    `json:"resumePages,omitempty"`
	CoverWhenAsked *string `json:"coverWhenAsked,omitempty"`
	Revision       int     `json:"revision"`
}

// DocumentRetrievalItem is one retrieved section: its source and score, never
// its text.
type DocumentRetrievalItem struct {
	Source  string   `json:"source"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons,omitempty"`
}

// DocumentRetrieval is the metadata of a run's evidence retrieval.
type DocumentRetrieval struct {
	Query     string                  `json:"query"`
	Retrieved int                     `json:"retrieved"`
	Items     []DocumentRetrievalItem `json:"items"`
}

// DocumentRunView is the outcome of a document run.
type DocumentRunView struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Stage  string `json:"stage,omitempty"`
	// RemainingSeconds is the time a running run has left before its deadline.
	RemainingSeconds int               `json:"remainingSeconds,omitempty"`
	Reason           string            `json:"reason,omitempty"`
	Generation       string            `json:"generation,omitempty"`
	Documents        []SafeInt         `json:"documents"`
	Warnings         []string          `json:"warnings"`
	Retrieval        DocumentRetrieval `json:"retrieval"`
	// Pack names the prep pack revision an interview run stored.
	Pack *InterviewPackRef `json:"pack,omitempty"`
}

// DocumentRefineTurn is one turn of a refine conversation: an instruction with
// its text, a decision, or an error with its message.
type DocumentRefineTurn struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Decision string `json:"decision,omitempty"`
}

// DocumentRefineRequest asks for a revised draft of one stored version. The
// instruction holds at most 2000 characters; turns are up to 8 earlier
// instructions and decisions, oldest first. The answer is a run id; the
// revision waits as a pending proposal until it is accepted or discarded.
type DocumentRefineRequest struct {
	Instruction string               `json:"instruction"`
	Turns       []DocumentRefineTurn `json:"turns,omitempty"`
}

// DocumentDiffLine is one line of the diff between a version and a proposal.
type DocumentDiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// DocumentProposal is a model's revision of a version, held apart from it. Text
// is the readable text of the proposal and Diff its line diff against the
// version it revises; exactly one of Resume and Letter is set, by the kind.
type DocumentProposal struct {
	ID          SafeInt                `json:"id"`
	DocumentID  SafeInt                `json:"documentId"`
	Kind        string                 `json:"kind"`
	Instruction string                 `json:"instruction"`
	Text        string                 `json:"text"`
	Diff        []DocumentDiffLine     `json:"diff"`
	Resume      *DocumentResumeContent `json:"resume,omitempty"`
	Letter      *DocumentLetterContent `json:"letter,omitempty"`
	CreatedAt   Time                   `json:"createdAt"`
}

// DocumentRefinement answers GET /api/documents/refine/{id}: the pending
// proposal, or null, and the recent turns, oldest first.
type DocumentRefinement struct {
	Pending *DocumentProposal    `json:"pending" tstype:"DocumentProposal | null"`
	Turns   []DocumentRefineTurn `json:"turns"`
}

// DocumentRefineDecision accepts or discards one proposal by id, so a decision
// never lands on a proposal the user has not seen.
type DocumentRefineDecision struct {
	ProposalID SafeInt `json:"proposalId"`
}
