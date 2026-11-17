package wire

// KBLint is one finding of the snippet checks.
type KBLint struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// The kinds of a listed knowledge document: a page and a company brief.
const (
	KBDocSnippet = "snippet"
	KBDocBrief   = "company-brief"
)

// KBDocEntry is a snippet or a company brief in the knowledge listing. For a
// story, Principles are its competency tags and SourceType its origin; for a
// brief, SourceURL is the page it was built from and ModifiedAt its age.
type KBDocEntry struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Path        string   `json:"path"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	ModifiedAt  string   `json:"modifiedAt"`
	FileHash    string   `json:"fileHash"`
	Tags        []string `json:"tags"`
	Principles  []string `json:"principles"`
	SourceType  string   `json:"sourceType"`
	SourceURL   string   `json:"sourceUrl"`
	Lint        []KBLint `json:"lint"`
}

// KBDocumentList is every snippet and brief, with the applications whose
// interview prep drew on each story.
type KBDocumentList struct {
	Entries []KBDocEntry `json:"entries"`
	Uses    []KBStoryUse `json:"uses"`
	// BriefRunID is the company brief build that is still working.
	BriefRunID string `json:"briefRunId,omitempty"`
}

// KBStoryUse is one application whose newest prep pack drew on a story.
type KBStoryUse struct {
	StoryID     string `json:"storyId"`
	CandidacyID int64  `json:"candidacyId"`
	Company     string `json:"company"`
	Role        string `json:"role"`
}

// KBSection is one heading-delimited part of a document.
type KBSection struct {
	Heading string `json:"heading"`
	Level   int    `json:"level"`
	Text    string `json:"text"`
}

// KBDocument is one snippet or brief with its text. A save quotes
// Entry.FileHash.
type KBDocument struct {
	Entry       KBDocEntry  `json:"entry"`
	Body        string      `json:"body"`
	Sections    []KBSection `json:"sections"`
	HasPrevious bool        `json:"hasPrevious"`
	// Links are the links in Body to other pages; LinkedFrom the pages whose
	// text links to this one.
	Links      []KBPageLink `json:"links"`
	LinkedFrom []KBPageRef  `json:"linkedFrom"`
}

// KBPrevious answers GET /api/kb/documents/{id}/previous: what a snippet or
// story held until the save at SavedAt replaced it, split the way the editor
// shows it. Only one is kept. A page restores it by saving it like any edit.
type KBPrevious struct {
	SavedAt Time     `json:"savedAt"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Body    string   `json:"body"`
}

// KBSnippetCreate is a reusable text block written by the user.
type KBSnippetCreate struct {
	Title    string   `json:"title"`
	Category string   `json:"category,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Body     string   `json:"body"`
}

// KBDocumentRevise changes the fields that are present; a save quotes the hash
// the document was opened with.
type KBDocumentRevise struct {
	ExpectedHash string    `json:"expectedHash"`
	Title        *string   `json:"title,omitempty"`
	Tags         *[]string `json:"tags,omitempty"`
	Body         *string   `json:"body,omitempty"`
}

// KBBriefRequest builds or refreshes the brief of a company. The posting pages
// declare the company's own website; none is guessed.
type KBBriefRequest struct {
	Company        string `json:"company"`
	ApplicationURL string `json:"applicationUrl,omitempty"`
	JobURL         string `json:"jobUrl,omitempty"`
	Refresh        bool   `json:"refresh,omitempty"`
}

// KBStoryCreate is a story the user wrote or approved.
type KBStoryCreate struct {
	Title      string   `json:"title"`
	SourceType string   `json:"sourceType,omitempty"`
	Principles []string `json:"principles"`
	Situation  string   `json:"situation"`
	Task       string   `json:"task"`
	Action     string   `json:"action"`
	Result     string   `json:"result"`
}

// KBStoryFinding is one result of the story gates.
type KBStoryFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// KBStorySaved reports a story save. A story the gates blocked stays a draft:
// Published is false and Findings say what to fix.
type KBStorySaved struct {
	DraftID   SafeInt          `json:"draftId"`
	Published bool             `json:"published"`
	Path      string           `json:"path"`
	Findings  []KBStoryFinding `json:"findings"`
}

// KBStoryProposal is a story drafted from the CV. It is stored nowhere.
type KBStoryProposal struct {
	Title      string   `json:"title"`
	Principles []string `json:"principles"`
	Situation  string   `json:"situation"`
	Task       string   `json:"task"`
	Action     string   `json:"action"`
	Result     string   `json:"result"`
}

// KBStoryProposals lists the proposals of one request.
type KBStoryProposals struct {
	Proposals []KBStoryProposal `json:"proposals"`
}

// KBStoryProposalRef names a proposal by its title.
type KBStoryProposalRef struct {
	Title string `json:"title"`
}
