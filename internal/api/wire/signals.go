package wire

// SignalCold is the tier of a company no assessment has placed higher.
const SignalCold = "cold"

// SignalRow is one company in the list of hiring signals.
type SignalRow struct {
	CompanyID      SafeInt  `json:"companyId"`
	Company        string   `json:"company"`
	Tier           string   `json:"tier"`
	Momentum       float64  `json:"momentum"`
	ScanPriority   float64  `json:"scanPriority"`
	State          string   `json:"state"`
	Blocked        bool     `json:"blocked"`
	Sources        []string `json:"sources"`
	NewestEvidence *Time    `json:"newestEvidence,omitempty"`
}

// SignalList answers GET /api/signals. The filters are tier, source, state and
// the range from to to of the newest evidence date; page and pageSize page it.
type SignalList struct {
	Rows     []SignalRow `json:"rows"`
	Page     SafeInt     `json:"page"`
	PageSize SafeInt     `json:"pageSize"`
	Total    SafeInt     `json:"total"`
}

// SignalDocument is one source document of the evidence chain.
type SignalDocument struct {
	ID      SafeInt `json:"id"`
	Adapter string  `json:"adapter"`
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Date    Time    `json:"date"`
}

// SignalQuote is one evidence quote; Verified is true when the assessment
// passed the verbatim check.
type SignalQuote struct {
	DocumentID SafeInt `json:"documentId"`
	Quote      string  `json:"quote"`
	Verified   bool    `json:"verified"`
}

// SignalAssessment is the model's verdict on an event.
type SignalAssessment struct {
	ID         SafeInt       `json:"id"`
	Status     string        `json:"status"`
	EventType  string        `json:"eventType"`
	Intent     string        `json:"hiringIntent"`
	Teams      []string      `json:"affectedTeams"`
	Scale      string        `json:"scale"`
	Strength   SafeInt       `json:"strength"`
	Confidence float64       `json:"confidence"`
	Reasoning  string        `json:"reasoning"`
	Quotes     []SignalQuote `json:"quotes"`
}

// SignalEvent is a merged company event with its documents and assessment.
type SignalEvent struct {
	ID         SafeInt           `json:"id"`
	Type       string            `json:"type"`
	Time       Time              `json:"time"`
	Amount     *float64          `json:"amount,omitempty"`
	Documents  []SignalDocument  `json:"documents"`
	Assessment *SignalAssessment `json:"assessment,omitempty"`
	Held       bool              `json:"held"`
}

// SignalFactor is the five-factor decomposition of one assessment.
type SignalFactor struct {
	EventID      SafeInt `json:"eventId"`
	EventType    string  `json:"eventType"`
	EventTime    string  `json:"eventTime"`
	Intent       string  `json:"hiringIntent"`
	TypeWeight   float64 `json:"typeWeight"`
	Strength     float64 `json:"strengthFactor"`
	Confidence   float64 `json:"confidence"`
	Source       float64 `json:"sourceFactor"`
	TimeDecay    float64 `json:"timeDecay"`
	Contribution float64 `json:"contribution"`
	Weak         bool    `json:"weak"`
}

// SignalIdentity is the cached ATS identity of a company.
type SignalIdentity struct {
	Status     string `json:"status"`
	ATS        string `json:"ats,omitempty"`
	Slug       string `json:"slug,omitempty"`
	CareersURL string `json:"careersUrl,omitempty"`
	Lane       string `json:"lane,omitempty"`
}

// SignalPosting is a posting seen on the company's board with its stored gate
// verdict (pass or fail) and every failing reason.
type SignalPosting struct {
	ID        SafeInt  `json:"id"`
	Title     string   `json:"title"`
	Location  string   `json:"location"`
	URL       string   `json:"url"`
	Verdict   string   `json:"verdict"`
	Reasons   []string `json:"reasons"`
	FirstSeen Time     `json:"firstSeen"`
	HandedOff *Time    `json:"handedOff,omitempty"`
	Handoff   string   `json:"handoff,omitempty"`
}

// SignalOutcome is one row of the outcome ledger for a company.
type SignalOutcome struct {
	EventID   SafeInt `json:"eventId"`
	Type      string  `json:"type"`
	Status    string  `json:"status"`
	LeadDays  SafeInt `json:"leadDays,omitempty"`
	WindowEnd Time    `json:"windowEnd"`
}

// SignalDetail answers GET /api/signals/{companyId}: the chain from source
// document to event to quotes to assessment to tier. SearchLinks holds two
// web-search URLs when the company has no gate-passing posting.
type SignalDetail struct {
	Row          SignalRow       `json:"row"`
	BlockExpires *Time           `json:"blockExpires,omitempty"`
	Events       []SignalEvent   `json:"events"`
	Factors      []SignalFactor  `json:"factors"`
	Identity     *SignalIdentity `json:"identity,omitempty"`
	Postings     []SignalPosting `json:"postings"`
	Outcomes     []SignalOutcome `json:"outcomes"`
	SearchLinks  []string        `json:"searchLinks"`
}

// SignalReviewItem is a held assessment waiting for the user.
type SignalReviewItem struct {
	ID         SafeInt          `json:"id"`
	CompanyID  SafeInt          `json:"companyId"`
	Company    string           `json:"company"`
	Reason     string           `json:"reason"`
	State      string           `json:"state"`
	Assessment SignalAssessment `json:"assessment"`
	CreatedAt  Time             `json:"createdAt"`
}

// SignalReviewList answers GET /api/signals/review.
type SignalReviewList struct {
	Items []SignalReviewItem `json:"items"`
}

// SignalTickRequest starts a tick. A non-empty Sources limits it to those
// collectors, which then collect and ingest only.
type SignalTickRequest struct {
	Sources []string `json:"sources,omitempty"`
}

// SignalRescoreResult says how many companies a rescore under the current
// policy covered and how many sit in each tier afterwards.
type SignalRescoreResult struct {
	Companies SafeInt            `json:"companies"`
	Tiers     map[string]SafeInt `json:"tiers"`
}

// SignalEvaluateResult lists the inbox rows enqueued for evaluation.
type SignalEvaluateResult struct {
	Queued []SafeInt `json:"queued"`
}

// SignalLane is the health of one source.
type SignalLane struct {
	Source       string `json:"source"`
	Title        string `json:"title"`
	Enabled      bool   `json:"enabled"`
	LastProduced *Time  `json:"lastProduced,omitempty"`
	LastStatus   string `json:"lastStatus,omitempty"`
	// Disclosure is what turning the source on sends out, shown before it
	// is switched on (signals.EdgarDisclosure for the SEC source).
	Disclosure string `json:"disclosure,omitempty"`
}

// SignalAlarm is a source that produced before and now produced nothing.
type SignalAlarm struct {
	Source         string `json:"source"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	LastProduced   Time   `json:"lastProduced"`
	RequiredAction string `json:"requiredAction,omitempty"`
	Retryable      bool   `json:"retryable"`
}

// SignalTypeHit is the hit rate of one assessed event type.
type SignalTypeHit struct {
	Type    string  `json:"type"`
	Hits    SafeInt `json:"hits"`
	Misses  SafeInt `json:"misses"`
	Pending SafeInt `json:"pending"`
	HitRate float64 `json:"hitRate"`
}

// SignalOutcomes is the outcome ledger summary.
type SignalOutcomes struct {
	Hits       SafeInt         `json:"hits"`
	Misses     SafeInt         `json:"misses"`
	Pending    SafeInt         `json:"pending"`
	HitRate    float64         `json:"hitRate"`
	MedianLead float64         `json:"medianLeadDays"`
	ByType     []SignalTypeHit `json:"byType"`
}

// SignalSourceResult is a collector's line of a tick summary.
type SignalSourceResult struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason,omitempty"`
	Docs     SafeInt  `json:"docs"`
	New      SafeInt  `json:"new"`
	Deferred []string `json:"deferred,omitempty"`
}

// SignalTickSummary is the record of the last tick.
type SignalTickSummary struct {
	TickID        SafeInt              `json:"tickId"`
	Status        string               `json:"status"`
	StartedAt     Time                 `json:"startedAt"`
	Limited       bool                 `json:"limited"`
	Sources       []SignalSourceResult `json:"sources"`
	Ingested      SafeInt              `json:"ingested"`
	Events        SafeInt              `json:"events"`
	Assessed      SafeInt              `json:"assessed"`
	Held          SafeInt              `json:"held"`
	TierChanges   SafeInt              `json:"tierChanges"`
	Handoffs      SafeInt              `json:"handoffs"`
	Alarms        []SignalAlarm        `json:"alarms"`
	BudgetReached bool                 `json:"budgetReached"`
	Notes         []string             `json:"notes,omitempty"`
	Error         string               `json:"error,omitempty"`
}

// SignalStatus answers GET /api/signals/status.
type SignalStatus struct {
	LastTick   *SignalTickSummary `json:"lastTick,omitempty"`
	ActiveTick *SafeInt           `json:"activeTick,omitempty"`
	Outcomes   SignalOutcomes     `json:"outcomes"`
	Tiers      map[string]SafeInt `json:"tiers"`
	ReviewOpen SafeInt            `json:"reviewOpen"`
	Lanes      []SignalLane       `json:"lanes"`
}
