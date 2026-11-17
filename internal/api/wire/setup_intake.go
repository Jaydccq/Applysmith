package wire

// Evidence import: several local career documents read into one reviewable
// change set against the profile. Nothing here is stored until the user
// accepts selected items against the profile revision the preview was built
// from.

// Document kinds. The first three carry facts the parser reads directly; the
// rest are signals, which can only suggest a question about a skill.
const (
	IntakeKindCV              = "cv"
	IntakeKindProfileExport   = "profile-export"
	IntakeKindEducationRecord = "education-record"
	IntakeKindProject         = "project"
	IntakeKindReference       = "reference"
	IntakeKindCourse          = "course"
	IntakeKindRepository      = "repository"
)

// Item types.
const (
	IntakeAddition = "addition"
	IntakeMatch    = "match"
	IntakeConflict = "conflict"
	IntakeSignal   = "signal"
)

// Source origins: Direct is a value the parser read from the document, Inferred
// is a term a signal document mentions.
const (
	IntakeDirect   = "direct"
	IntakeInferred = "inferred"
)

// IntakeDocument is one selected document: its text and what the user says it
// is. Label is shown beside every fact from it; it defaults to Name.
type IntakeDocument struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
}

// IntakeRequest asks for a preview.
type IntakeRequest struct {
	Documents []IntakeDocument `json:"documents"`
}

// IntakeSource says where one value came from: the document's label and content
// hash, the line and heading it sits under, and whether it was read or inferred.
type IntakeSource struct {
	Label   string `json:"label"`
	Hash    string `json:"hash"`
	Locator string `json:"locator"`
	Origin  string `json:"origin"`
}

// IntakeOption is one value a field could take and the sources that give it.
type IntakeOption struct {
	Value   string         `json:"value"`
	Sources []IntakeSource `json:"sources"`
}

// IntakeField is one field of a conflict: the approved profile value, when the
// profile has one, and every different value the documents give. A bare year
// is a date without a month; picking it needs the month.
type IntakeField struct {
	Field   string         `json:"field"`
	Profile string         `json:"profile,omitempty"`
	Options []IntakeOption `json:"options"`
}

// IntakeItem is one line of the change set. Entry holds the fields of an
// employment (employer, title, start, end), education (school, degree, start,
// end) or skill (name) entry. NeedsMonth lists the date fields given as a bare
// year, whose month the person types before the item can be accepted.
type IntakeItem struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Kind       string            `json:"kind"`
	Summary    string            `json:"summary"`
	Entry      map[string]string `json:"entry"`
	Sources    []IntakeSource    `json:"sources"`
	Fields     []IntakeField     `json:"fields"`
	NeedsMonth []string          `json:"needsMonth"`
	Question   string            `json:"question,omitempty"`
}

// IntakeDocInfo reports how one document was read. Duplicate is true when an
// earlier document in the batch has the same content hash; it adds nothing.
type IntakeDocInfo struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Hash      string `json:"hash"`
	Signal    bool   `json:"signal"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// IntakePreview answers a preview. Revision is the profile.yml revision it was
// built from, and acceptance must name it.
type IntakePreview struct {
	Revision  string          `json:"revision"`
	Documents []IntakeDocInfo `json:"documents"`
	Items     []IntakeItem    `json:"items"`
	Notes     []string        `json:"notes"`
}

// IntakeSelection accepts one item. Choices maps each field of a conflict to
// the value the person picked, "keep" for the profile's own. Months supplies
// the month of a date the document gave as a bare year. Confirm is required for
// a signal and means the person says the experience is theirs.
type IntakeSelection struct {
	ID      string            `json:"id"`
	Choices map[string]string `json:"choices,omitempty"`
	Months  map[string]string `json:"months,omitempty"`
	Confirm bool              `json:"confirm,omitempty"`
}

// IntakeAcceptRequest applies the selected items of the preview built from
// Revision. The documents are sent again so the change set is rebuilt from the
// same bytes; a changed profile revision refuses the request.
type IntakeAcceptRequest struct {
	Revision   string            `json:"revision"`
	Documents  []IntakeDocument  `json:"documents"`
	Selections []IntakeSelection `json:"selections"`
}

// IntakeResult answers an acceptance: whether profile.yml changed, its new
// revision, and the items applied.
type IntakeResult struct {
	Wrote    bool         `json:"wrote"`
	Revision string       `json:"revision"`
	Applied  []IntakeItem `json:"applied"`
	Notes    []string     `json:"notes"`
}
