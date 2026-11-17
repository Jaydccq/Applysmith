package wire

// ConfigIssue is one validation finding of a workspace file.
type ConfigIssue struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ConfigDocument is a workspace file as the profile page edits it: the text,
// the revision a save must quote and what validation says about it. Template is
// true while the file still equals the text it was created from. Values is the
// parsed profile, or the effective policy with its defaults filled in, and is
// present only for a file that validates.
type ConfigDocument struct {
	File     string         `json:"file"`
	Exists   bool           `json:"exists"`
	Template bool           `json:"template"`
	Content  string         `json:"content"`
	Revision string         `json:"revision"`
	Issues   []ConfigIssue  `json:"issues"`
	Values   map[string]any `json:"values,omitempty"`
}

// ConfigBackup is an earlier content of a file: what it held until the save at
// SavedAt replaced it. A page restores one by saving it like any other edit.
type ConfigBackup struct {
	SavedAt Time   `json:"savedAt"`
	Content string `json:"content"`
}

// ConfigBackupList answers GET /api/config/{file}/backups, newest first.
type ConfigBackupList struct {
	Backups []ConfigBackup `json:"backups"`
}

// ConfigSave replaces a file when ExpectedRevision is still its revision.
type ConfigSave struct {
	Content          string `json:"content"`
	ExpectedRevision string `json:"expectedRevision"`
}

// ConfigOp is one structured edit of the profile or policy by key path. Value is YAML
// text. Op is set, unset, append or remove; a remove names Index or Match.
type ConfigOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value,omitempty"`
	Index *int   `json:"index,omitempty"`
	Match string `json:"match,omitempty"`
}

// ConfigPatch applies Ops in order, as one write, against ExpectedRevision.
type ConfigPatch struct {
	ExpectedRevision string     `json:"expectedRevision"`
	Ops              []ConfigOp `json:"ops"`
}

// ConfigSaved reports the revision after a save. Changed is false when the new
// content equals the file.
type ConfigSaved struct {
	Revision string `json:"revision"`
	Changed  bool   `json:"changed"`
}

// ResumeUploaded names the file a résumé upload stored; the roster entry that
// uses it is added through ConfigPatch.
type ResumeUploaded struct {
	File string `json:"file"`
}

// CVJob is one employment entry the parser found in the CV.
type CVJob struct {
	Employer string `json:"employer"`
	Title    string `json:"title,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
}

// CVStudy is one education entry the parser found in the CV.
type CVStudy struct {
	School string `json:"school"`
	Degree string `json:"degree,omitempty"`
	Field  string `json:"field,omitempty"`
	Start  string `json:"start,omitempty"`
	End    string `json:"end,omitempty"`
}

// CVDraft is the history and skills extracted from cv.md at Revision, and
// Identity what its name and contact line state, by the identity step's field
// name (full_name, email, phone, city, region, country, linkedin, github,
// portfolio). Nothing in it is stored until the user confirms an entry.
// Dismissed holds the keys of the entries the user discarded, as the page
// named them (CVDraftDismiss).
type CVDraft struct {
	Revision   string            `json:"revision"`
	Employment []CVJob           `json:"employment"`
	Education  []CVStudy         `json:"education"`
	Skills     []string          `json:"skills"`
	Identity   map[string]string `json:"identity"`
	Dismissed  []string          `json:"dismissed"`
}

// CVDraftDismiss discards draft entries for good, by the page's keys for
// them; Restore puts them back (the Undo of a discard).
type CVDraftDismiss struct {
	Keys    []string `json:"keys"`
	Restore bool     `json:"restore,omitempty"`
}
