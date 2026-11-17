package wire

// KBPrincipleGrade is one cell of the competency grid.
type KBPrincipleGrade struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Grade   string   `json:"grade"`
	Stories []string `json:"stories"`
	Missing []string `json:"missing"`
	Suggest []string `json:"suggest"`
	// Questions are the theme's interview questions from the competency set,
	// worded as prompts: the library a practice drill picks from.
	Questions []string `json:"questions"`
}

// KBShapeGrade is one cell of the story-shape grid.
type KBShapeGrade struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Need    int      `json:"need"`
	Have    int      `json:"have"`
	Grade   string   `json:"grade"`
	Stories []string `json:"stories"`
}

// KBCoverage is both coverage grids.
type KBCoverage struct {
	Principles []KBPrincipleGrade `json:"principles"`
	Shapes     []KBShapeGrade     `json:"shapes"`
}

// KBFileNote names a repository path with the reason it was not read.
type KBFileNote struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// KBIngestReport describes one repository ingestion. Redacted files are listed
// by path and the kind of secret only.
type KBIngestReport struct {
	Status     string       `json:"status"`
	Commit     string       `json:"commit"`
	Branch     string       `json:"branch"`
	Listing    string       `json:"listing"`
	Considered int          `json:"considered"`
	Read       int          `json:"read"`
	Reused     int          `json:"reused"`
	Fetched    int          `json:"fetched"`
	Skipped    []KBFileNote `json:"skipped"`
	Redacted   []KBFileNote `json:"redacted"`
	Drafts     []SafeInt    `json:"drafts"`
	Notes      []string     `json:"notes"`
}

// KBRepo is a GitHub repository added as a knowledge source.
type KBRepo struct {
	ID         SafeInt        `json:"id"`
	Owner      string         `json:"owner"`
	Name       string         `json:"name"`
	Branch     string         `json:"branch"`
	CommitSHA  string         `json:"commitSha"`
	Status     string         `json:"status"`
	Report     KBIngestReport `json:"report"`
	CreatedAt  string         `json:"createdAt"`
	IngestedAt string         `json:"ingestedAt"`
	// ReadingRunID is the read of this repository that is still working.
	ReadingRunID string `json:"readingRunId,omitempty"`
}

// KBRepoAdd adds a repository, given as owner/repo or an https GitHub URL.
type KBRepoAdd struct {
	Repo string `json:"repo"`
}

// KBRepoList is the repository sources.
type KBRepoList struct {
	Repos []KBRepo `json:"repos"`
}

// KBIngestRequest starts an ingestion. Force reads a commit that was already
// ingested; files whose blob SHA is unchanged are reused.
type KBIngestRequest struct {
	Force bool `json:"force,omitempty"`
}

// KBMiningStart opens a story-mining session for one quadrant (project, team,
// customer or failure). Notes or NotesFile pre-fill the fields they state.
// Target, an application question that asks for a story, asks that story's
// fields instead and sets the quadrant; the session's transcript opens with it.
type KBMiningStart struct {
	Quadrant  string `json:"quadrant"`
	Notes     string `json:"notes,omitempty"`
	NotesFile string `json:"notesFile,omitempty"`
	Target    string `json:"target,omitempty"`
}

// KBMiningReply answers the question a session is waiting on. With Field it
// instead replaces the answer of that part, which must be answered already.
// ExpectedRevision is the length of the transcript as read, which every
// answer and edit grows, so a reply or an edit made against an older read is
// refused.
type KBMiningReply struct {
	Text             string `json:"text"`
	Field            string `json:"field,omitempty"`
	ExpectedRevision int    `json:"expectedRevision"`
}

// KBMiningValue is one filled field and its source: user, notes, reply (a
// passage of the reply to another question), skipped, or partial (a reply
// that still owes the figure asked for).
type KBMiningValue struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// KBMiningEntry is one line of a session transcript.
type KBMiningEntry struct {
	Role  string `json:"role"`
	Field string `json:"field"`
	Text  string `json:"text"`
	At    string `json:"at"`
}

// KBMiningQuestion is the prompt a collecting session waits on.
type KBMiningQuestion struct {
	Field string `json:"field"`
	Text  string `json:"text"`
}

// KBMiningSession is a story-mining session. Ignored is true on a reply that
// was not recorded because the session no longer takes replies.
type KBMiningSession struct {
	ID         string                   `json:"id"`
	Quadrant   string                   `json:"quadrant"`
	SourceType string                   `json:"sourceType"`
	State      string                   `json:"state"`
	NotesFrom  string                   `json:"notesFrom"`
	Fields     map[string]KBMiningValue `json:"fields"`
	Log        []KBMiningEntry          `json:"log"`
	Next       *KBMiningQuestion        `json:"next,omitempty"`
	DraftID    SafeInt                  `json:"draftId"`
	CreatedAt  string                   `json:"createdAt"`
	UpdatedAt  string                   `json:"updatedAt"`
	Ignored    bool                     `json:"ignored"`
}

// KBMiningList is the mining sessions, newest first.
type KBMiningList struct {
	Sessions []KBMiningSession `json:"sessions"`
}

// KBQuadrantSuggestion says why a quadrant is worth mining next.
type KBQuadrantSuggestion struct {
	Quadrant string   `json:"quadrant"`
	Score    int      `json:"score"`
	Reasons  []string `json:"reasons"`
}

// KBQuadrantSuggestions lists the quadrants, the one to mine first leading.
type KBQuadrantSuggestions struct {
	Quadrants []KBQuadrantSuggestion `json:"quadrants"`
}

// KBDraft is a knowledge draft: never retrievable until the user publishes it.
type KBDraft struct {
	ID        SafeInt `json:"id"`
	Revision  int     `json:"revision"`
	Status    string  `json:"status"`
	Kind      string  `json:"kind"`
	Origin    string  `json:"origin"`
	Category  string  `json:"category"`
	SnippetID string  `json:"snippetId"`
	Markdown  string  `json:"markdown"`
	// Body is the Markdown without its frontmatter, what the queue shows to
	// read before an edit.
	Body  string        `json:"body"`
	Facts []KBDraftFact `json:"facts"`
	// Principles are a story draft's competency themes, from its front matter.
	Principles []string `json:"principles,omitempty"`
}

// KBDraftFact is a statement of a draft; a pending one is not yet confirmed.
type KBDraftFact struct {
	Text    string `json:"text"`
	Origin  string `json:"origin"`
	Pending bool   `json:"pending"`
}

// KBDraftList answers GET /api/kb/drafts: the unpublished drafts.
type KBDraftList struct {
	Drafts []KBDraft `json:"drafts"`
}

// KBUndo answers a knowledge delete (a snippet or brief, a draft, a repository
// source, an interview) with the token that undoes it: POST it to
// /api/kb/restore while the server holds the delete (kb.UndoWindow, not across
// a restart).
type KBUndo struct {
	Undo string `json:"undo"`
}

// KBRestored names what an undo brought back: kind is snippet, brief, draft,
// repository or interview.
type KBRestored struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// KBDraftChange is the new text of a draft or, for a story draft, its
// competency themes (PATCH), or the positions of the pending statements to
// confirm, none meaning all.
type KBDraftChange struct {
	Markdown   string   `json:"markdown,omitempty"`
	Principles []string `json:"principles,omitempty"`
	Facts      []int    `json:"facts,omitempty"`
}
