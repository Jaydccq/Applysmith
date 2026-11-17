package wire

// Setup types: the step catalog as the wizard shows it. The catalog itself
// lives in internal/onboarding; the command line, the wizard and the doctor
// all read it, and these types carry its read model.

// SetupReason names one outstanding field, file or value of a step in plain
// text. Ackable is true when an acknowledgement clears it; Blank when the
// value is simply missing, so a form that asks for the field says it already.
type SetupReason struct {
	Field   string `json:"field,omitempty"`
	Text    string `json:"text"`
	Ackable bool   `json:"ackable,omitempty"`
	Blank   bool   `json:"blank,omitempty"`
}

// SetupField is one input of a step writer. Type is string, bool, int,
// string-list, object or object-list.
type SetupField struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Doc      string   `json:"doc"`
	Enum     []string `json:"enum,omitempty"`
}

// SetupStep is one catalog step with its status. Fields is the input schema of
// its writer; Page is the dashboard page that hosts the feature when the step
// has no form of its own, and Instruction says what a person must do outside
// the dashboard.
type SetupStep struct {
	N           int           `json:"n"`
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Phase       string        `json:"phase"`
	Required    bool          `json:"required"`
	Status      string        `json:"status"`
	Reasons     []SetupReason `json:"reasons"`
	Warnings    []string      `json:"warnings"`
	Purpose     string        `json:"purpose"`
	SkipCost    string        `json:"skipCost,omitempty"`
	Storage     string        `json:"storage"`
	Guide       string        `json:"guide"`
	Commands    []string      `json:"commands"`
	Fields      []SetupField  `json:"fields"`
	NeedsHuman  bool          `json:"needsHuman,omitempty"`
	Instruction string        `json:"instruction,omitempty"`
	Page        string        `json:"page,omitempty"`
}

// SetupSecret says whether a secret is stored and when it was saved; it never
// carries the value.
type SetupSecret struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	SavedAt string `json:"savedAt,omitempty"`
}

// SetupState answers GET /api/setup/state. Progress is the percentage of
// required steps satisfied; Next is the id the wizard opens on, the first
// incomplete required step, or empty when every required step is satisfied.
type SetupState struct {
	Progress int           `json:"progress"`
	Next     string        `json:"next"`
	Steps    []SetupStep   `json:"steps"`
	Secrets  []SetupSecret `json:"secrets"`
}

// SetupApplyRequest is the input of a step writer, validated against the
// step's fields. DryRun validates and writes nothing.
type SetupApplyRequest struct {
	Input  map[string]any `json:"input"`
	DryRun bool           `json:"dryRun,omitempty"`
}

// SetupDraft is the history and skills the CV parser extracted; nothing is
// stored until the user confirms entries.
type SetupDraft struct {
	Employment []CVJob   `json:"employment"`
	Education  []CVStudy `json:"education"`
	Skills     []string  `json:"skills"`
}

// SetupApplied answers a writer call with the step's new status.
type SetupApplied struct {
	Step      SetupStep   `json:"step"`
	DryRun    bool        `json:"dryRun,omitempty"`
	Wrote     bool        `json:"wrote"`
	Notes     []string    `json:"notes"`
	Untouched []string    `json:"untouched"`
	Draft     *SetupDraft `json:"draft,omitempty"`
}

// DoctorResult is one check of the doctor: Status is pass, warn, fail or
// skipped.
type DoctorResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	Summary  string `json:"summary"`
	Fix      string `json:"fix,omitempty"`
	// Step is the setup step whose page fixes the check. Fix is worded for
	// the command line, so the dashboard shows a link to this step instead
	// and Fix only for a check that no step fixes.
	Step string `json:"step,omitempty"`
}

// DoctorReport answers POST /api/setup/doctor, the wizard's "Check
// everything". Platform is the supported-platform statement that opens every
// doctor report.
type DoctorReport struct {
	Platform string         `json:"platform"`
	Results  []DoctorResult `json:"results"`
}

// ToolchainComponent is one tool or package the document toolchain needs.
type ToolchainComponent struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// ToolchainExternal is a tool the user installs outside the app: how, and how
// much disk it takes.
type ToolchainExternal struct {
	Name    string `json:"name"`
	Install string `json:"install"`
	Size    string `json:"size"`
}

// ToolchainBudget separates what the app prepares by itself (Downloads and
// DiskBytes, for the private venv) from what the user installs outside the
// bundle (External): TeX, Python and Poppler are never part of the app.
type ToolchainBudget struct {
	Downloads []string            `json:"downloads"`
	DiskBytes SafeInt             `json:"diskBytes"`
	External  []ToolchainExternal `json:"external"`
}

// DocumentToolchain answers GET /api/documents/toolchain: whether new PDFs can
// be rendered. State is unavailable, verification-needed or ready. It is the
// rendering subcheck of the resumes step and changes nothing on the machine.
type DocumentToolchain struct {
	State      string               `json:"state"`
	Code       string               `json:"code,omitempty"`
	Missing    []string             `json:"missing"`
	Smoke      string               `json:"smoke"`
	Fix        string               `json:"fix,omitempty"`
	Components []ToolchainComponent `json:"components"`
	Budget     ToolchainBudget      `json:"budget"`
}

// SetupStatus is the cheap status every dashboard page reads. Demo is true in
// a workspace that carries the demo marker.
type SetupStatus struct {
	Demo bool `json:"demo"`
}

// DemoCreated reports the demo workspace the Setup page created or completed.
// Workspace is its folder as the person reads it (under the home folder it
// starts with ~/). Start is the command that opens it; the running server
// keeps serving its own workspace.
type DemoCreated struct {
	Workspace string   `json:"workspace"`
	Created   []string `json:"created"`
	Postings  int      `json:"postings"`
	Notes     []string `json:"notes"`
	Start     string   `json:"start"`
}

// CVExtracted is local file text for review; extraction does not store the CV.
type CVExtracted struct {
	Markdown string `json:"markdown"`
}
