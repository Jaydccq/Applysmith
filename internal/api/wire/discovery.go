package wire

// ScanRequest starts a scan of one source. Every field but Source overrides
// the scan defaults of the profile.
type ScanRequest struct {
	Source        string   `json:"source"`
	Scope         string   `json:"scope,omitempty"`
	Sort          string   `json:"sort,omitempty"`
	TopN          *SafeInt `json:"topN,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	RetryKnown    bool     `json:"retryKnown,omitempty"`
	BudgetSeconds SafeInt  `json:"budgetSeconds,omitempty"`
	// Search, Title, City, State and URL make the run ad hoc: they replace the
	// source's saved searches with one. Limit caps the results of each search
	// and never makes a run ad hoc.
	Search string  `json:"search,omitempty"`
	Title  string  `json:"title,omitempty"`
	City   string  `json:"city,omitempty"`
	State  string  `json:"state,omitempty"`
	URL    string  `json:"url,omitempty"`
	Limit  SafeInt `json:"limit,omitempty"`
	// PostedWithin is the freshness window, such as 6h, 3d or 2w, or any.
	PostedWithin string `json:"postedWithin,omitempty"`
	// Fn, Seniority, Employment and Remote are the Speedrun board filters.
	Fn         string `json:"fn,omitempty"`
	Seniority  string `json:"seniority,omitempty"`
	Employment string `json:"employment,omitempty"`
	Remote     bool   `json:"remote,omitempty"`
	// Pages is how many result pages a browser source reads. IgnoreCooldown
	// runs a source that is cooling down after a blocked run, Force a scan the
	// host-pressure check refused, EasyApplyOnly keeps only the Easy Apply
	// rows of a LinkedIn scan.
	Pages          SafeInt `json:"pages,omitempty"`
	IgnoreCooldown bool    `json:"ignoreCooldown,omitempty"`
	Force          bool    `json:"force,omitempty"`
	EasyApplyOnly  bool    `json:"easyApplyOnly,omitempty"`
}

// ScanCounts are the numbers of one scan run. Every discovered row ends in
// exactly one of filtered, undated, duplicates or inserted.
type ScanCounts struct {
	Discovered         SafeInt            `json:"discovered"`
	Filtered           map[string]SafeInt `json:"filtered"`
	Undated            SafeInt            `json:"undated"`
	Duplicates         map[string]SafeInt `json:"duplicates"`
	Inserted           SafeInt            `json:"inserted"`
	Queued             SafeInt            `json:"queued"`
	Enriched           SafeInt            `json:"enriched"`
	EnrichmentDegraded SafeInt            `json:"enrichmentDegraded"`
	EnrichmentFailed   SafeInt            `json:"enrichmentFailed"`
	EnrichmentSkipped  SafeInt            `json:"enrichmentSkipped"`
	GateRejected       SafeInt            `json:"gateRejected"`
	Outcomes           map[string]SafeInt `json:"outcomes"`
	TruncatedPostings  SafeInt            `json:"truncatedPostings"`
	BoardErrors        SafeInt            `json:"boardErrors"`
}

// ScanRecord is the record of one scan run.
type ScanRecord struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// RunID is the run the dashboard started the scan under; it is empty for a
	// scheduled scan, which has no run to follow or cancel.
	RunID         string             `json:"runId,omitempty"`
	Status        string             `json:"status"`
	Reason        string             `json:"reason,omitempty"`
	Error         string             `json:"error,omitempty"`
	StartedAt     Time               `json:"startedAt"`
	UpdatedAt     Time               `json:"updatedAt"`
	FinishedAt    *Time              `json:"finishedAt,omitempty"`
	DurationMs    SafeInt            `json:"durationMs"`
	Counts        ScanCounts         `json:"counts"`
	BoardsVisited SafeInt            `json:"boardsVisited"`
	Truncated     []string           `json:"truncated,omitempty"`
	Deferred      map[string]SafeInt `json:"deferred,omitempty"`
	Suspended     []string           `json:"suspended,omitempty"`
	Errors        []string           `json:"errors,omitempty"`
}

// ScanSource is one entry of the source catalog with what the Run tab needs:
// whether the source can run now and why not, whether a scan of it is running
// or cooling down, and its last run. Needs is key or session when the source
// cannot run without one; Readiness is ready, needs key, needs browser, needs
// sign-in, sign-in unchecked, for the registry source off while portals.yml
// switches it off and needs boards while it has no board to read, needs
// searches for a saved-search source with no saved search, or needs companies
// for the tracked source with no enabled company, and Reason says in a
// sentence why it is not ready.
// Title is the name to show.
type ScanSource struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Needs string `json:"needs,omitempty"`
	// KeyName is the vault name of the key a keyed source reads, so Settings
	// can save it.
	KeyName       string      `json:"keyName,omitempty"`
	Readiness     string      `json:"readiness"`
	Reason        string      `json:"reason,omitempty"`
	Running       bool        `json:"running"`
	CooldownUntil *Time       `json:"cooldownUntil,omitempty"`
	LastScan      *ScanRecord `json:"lastScan,omitempty"`
}

// SourceFunnel answers GET /api/discovery/funnel: what each source produced over
// the last Days days, most evaluated first. A posting is counted once, under
// the source of its first finished evaluation; Applied, Responded and Interview
// are read from its tracker row.
type SourceFunnel struct {
	Days  int          `json:"days"`
	Lanes []FunnelLane `json:"lanes"`
	Total FunnelLane   `json:"total"`
}

// FunnelLane is the funnel of one source. The manual lane holds evaluations the
// user started by hand.
type FunnelLane struct {
	Lane         string `json:"lane"`
	Discovered   int    `json:"discovered"`
	Inserted     int    `json:"inserted"`
	Evaluated    int    `json:"evaluated"`
	Failed       int    `json:"failed"`
	GateRejected int    `json:"gateRejected"`
	High         int    `json:"high"`
	Applied      int    `json:"applied"`
	Responded    int    `json:"responded"`
	Interview    int    `json:"interview"`
}

// FunnelWeek answers GET /api/discovery/funnel/week: the last 7 local days,
// today included, and the 7 before. Since is the first of them, YYYY-MM-DD.
type FunnelWeek struct {
	Since    string       `json:"since"`
	Current  FunnelStages `json:"current"`
	Previous FunnelStages `json:"previous"`
}

// FunnelStages counts the postings that reached each stage in a week.
type FunnelStages struct {
	Found     int `json:"found"`
	Evaluated int `json:"evaluated"`
	Applied   int `json:"applied"`
	Responded int `json:"responded"`
	Interview int `json:"interview"`
	Offer     int `json:"offer"`
}

// ScanSourceList answers GET /api/scan-sources, in catalog order.
type ScanSourceList struct {
	Sources []ScanSource `json:"sources"`
}

// ScanList lists scan runs, newest first, one page of those that match the
// filters. Total counts every match.
type ScanList struct {
	Scans []ScanRecord `json:"scans"`
	Page  SafeInt      `json:"page"`
	Limit SafeInt      `json:"limit"`
	Total SafeInt      `json:"total"`
}

// Inbox statuses.
const (
	InboxPending   = "pending"
	InboxQueued    = "queued"
	InboxEvaluated = "evaluated"
	InboxDismissed = "dismissed"
	InboxExpired   = "expired"
)

// InboxRow is one row of the pipeline inbox.
type InboxRow struct {
	ID                SafeInt  `json:"id"`
	URL               string   `json:"url"`
	OriginalURL       string   `json:"originalUrl"`
	Aliases           []string `json:"aliases"`
	IdentityKey       string   `json:"identityKey"`
	Company           string   `json:"company"`
	Role              string   `json:"role"`
	Location          string   `json:"location"`
	Source            string   `json:"source"`
	Detail            string   `json:"detail"`
	Score             SafeInt  `json:"score"`
	Reasons           []string `json:"reasons"`
	EarlyCareer       bool     `json:"earlyCareer"`
	ClearanceRequired bool     `json:"clearanceRequired"`
	RequiredYears     *SafeInt `json:"requiredYears,omitempty"`
	Flags             []string `json:"flags"`
	Notes             string   `json:"notes"`
	Status            string   `json:"status"`
	Attempts          SafeInt  `json:"attempts"`
	JobID             string   `json:"jobId,omitempty"`
	RunID             string   `json:"runId,omitempty"`
	FirstSeenAt       Time     `json:"firstSeenAt"`
	UpdatedAt         Time     `json:"updatedAt"`
	// What an evaluated row's run came to.
	Outcome      string   `json:"outcome,omitempty"`
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
	ReportScore  *float64 `json:"reportScore,omitempty"`
}

// InboxPage is one page of the inbox with the count per status.
type InboxPage struct {
	Rows     []InboxRow         `json:"rows"`
	Page     SafeInt            `json:"page"`
	PageSize SafeInt            `json:"pageSize"`
	Total    SafeInt            `json:"total"`
	Counts   map[string]SafeInt `json:"counts"`
	// Unrouted names an evaluation task with no model it can use: until it
	// has one, scans and the scheduled inbox evaluation send no row.
	Unrouted string `json:"unrouted,omitempty"`
}

// InboxPromoteRequest promotes pending rows to evaluation: the rows ids names,
// or else the top best pending rows the gate admits.
type InboxPromoteRequest struct {
	IDs []SafeInt `json:"ids"`
	Top SafeInt   `json:"top,omitempty"`
}

// InboxPromoteResult lists the rows queued and, per rejected row id, the gate
// rule that kept it pending.
type InboxPromoteResult struct {
	Queued   []SafeInt         `json:"queued"`
	Rejected map[string]string `json:"rejected"`
}

// BlockedCompany is one entry of the blocked-companies store.
type BlockedCompany struct {
	ID         SafeInt `json:"id"`
	Name       string  `json:"name"`
	Normalized string  `json:"normalized"`
	Reason     string  `json:"reason"`
	AddedBy    string  `json:"addedBy"`
	AddedAt    Time    `json:"addedAt"`
}

// BlockedCompanyRequest blocks a company.
type BlockedCompanyRequest struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

// BlockedCompanyList is the blocked-companies store.
type BlockedCompanyList struct {
	Companies []BlockedCompany `json:"companies"`
}
