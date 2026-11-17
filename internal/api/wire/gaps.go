package wire

// GapRequest starts a skill-gap analysis over 1 to 100 final reports. Report
// numbers repeated in the list count once; a report that holds no requirement
// evidence, or repeats another report's posting, is listed as excluded.
type GapRequest struct {
	Reports []SafeInt `json:"reports"`
}

// GapConfirmRequest records that the user lacks a skill of the analysis, or
// takes the confirmation back.
type GapConfirmRequest struct {
	Skill     string `json:"skill"`
	Confirmed bool   `json:"confirmed"`
}

// GapPlanRequest asks for a learning plan over 1 to 5 confirmed gaps.
// LookupResources asks the research route for learning resources.
type GapPlanRequest struct {
	Skills          []string `json:"skills"`
	LookupResources bool     `json:"lookupResources,omitempty"`
}

// GapProgressRequest marks the study exercise of a planned skill complete or
// open again. It changes nothing in the profile or the answer bank.
type GapProgressRequest struct {
	Skill string `json:"skill"`
	Done  bool   `json:"done"`
}

// GapSource is one posting's statement of a requirement the profile does not
// fully evidence. Evidence is the profile evidence behind a partial hit.
type GapSource struct {
	Report      SafeInt `json:"report"`
	Company     string  `json:"company"`
	Role        string  `json:"role"`
	Requirement string  `json:"requirement"`
	Quote       string  `json:"quote"`
	Class       string  `json:"class"`
	Evidence    string  `json:"evidence"`
	Unsupported bool    `json:"unsupported"`
}

// Gap is one skill with the postings that ask for it. State is confirmed-gap
// (the user said they lack it), not-evidenced (the profile shows nothing) or
// needs-review (partial or unsupported evidence). Required and Preferred count
// distinct postings that state the requirement explicitly.
type Gap struct {
	Skill     string      `json:"skill"`
	Label     string      `json:"label"`
	State     string      `json:"state"`
	Required  SafeInt     `json:"required"`
	Preferred SafeInt     `json:"preferred"`
	Sources   []GapSource `json:"sources"`
}

// GapInferred is a signal read between the lines of a posting, listed apart
// from the gaps and never counted as demand.
type GapInferred struct {
	Report   SafeInt `json:"report"`
	Company  string  `json:"company"`
	Role     string  `json:"role"`
	Label    string  `json:"label"`
	Evidence string  `json:"evidence"`
}

// GapExclusion says why a selected report is outside the sample.
type GapExclusion struct {
	Report      SafeInt `json:"report"`
	Reason      string  `json:"reason"`
	DuplicateOf SafeInt `json:"duplicateOf"`
}

// GapInput is the revision of a report the analysis read.
type GapInput struct {
	Report      SafeInt `json:"report"`
	CreatedAt   Time    `json:"createdAt"`
	ContentHash string  `json:"contentHash"`
}

// GapResource is a learning resource the research route found and verified.
type GapResource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// GapPlanItem is the study plan for one confirmed gap. Effort is an estimate
// and says so.
type GapPlanItem struct {
	Skill     string        `json:"skill"`
	Label     string        `json:"label"`
	Exercise  string        `json:"exercise"`
	Criterion string        `json:"criterion"`
	Effort    string        `json:"effort"`
	Resources []GapResource `json:"resources"`
	Done      bool          `json:"done"`
}

// GapLookup says what became of the request for learning resources:
// not-requested, unavailable (no research source is configured), done or
// failed.
type GapLookup struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// GapPlan is a bounded learning plan.
type GapPlan struct {
	GeneratedAt Time          `json:"generatedAt"`
	Items       []GapPlanItem `json:"items"`
	Lookup      GapLookup     `json:"lookup"`
}

// GapReport is the analysis artifact. It carries no fit score and is not a
// posting report. Done lists the skills whose exercise was marked complete.
type GapReport struct {
	ID          string         `json:"id"`
	GeneratedAt Time           `json:"generatedAt"`
	Selected    []SafeInt      `json:"selected"`
	Sample      []GapInput     `json:"sample"`
	Excluded    []GapExclusion `json:"excluded"`
	Gaps        []Gap          `json:"gaps"`
	Inferred    []GapInferred  `json:"inferred"`
	Plan        *GapPlan       `json:"plan" tstype:"GapPlan | null"`
	Done        []string       `json:"done"`
}

// GapSummary is one earlier analysis in the list.
type GapSummary struct {
	ID          string  `json:"id"`
	GeneratedAt Time    `json:"generatedAt"`
	Sample      SafeInt `json:"sample"`
	Gaps        SafeInt `json:"gaps"`
}

// GapList is the earlier analyses, newest first.
type GapList struct {
	Analyses []GapSummary `json:"analyses"`
}
