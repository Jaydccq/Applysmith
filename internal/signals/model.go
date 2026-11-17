package signals

import "time"

// Source identifiers, as configured under sources in portals.yml.
const (
	SourceEdgar     = "signals-edgar"
	SourceAlerts    = "signals-alerts"
	SourceHN        = "signals-hn"
	SourceHNHiring  = "signals-hn-hiring"
	SourceWeb       = "signals-web"
	SourceX         = "signals-x"
	adapterEdgar    = "edgar"
	adapterAlerts   = "alerts"
	adapterHN       = "hn"
	adapterHNHiring = "hn-hiring"
	adapterWeb      = "web"
	adapterX        = "x"
)

// SourceIDs lists the collectors in run order.
var SourceIDs = []string{SourceEdgar, SourceAlerts, SourceHN, SourceHNHiring, SourceWeb, SourceX}

// SourceTitles are the names shown to the user.
var SourceTitles = map[string]string{SourceEdgar: "SEC EDGAR filings", SourceAlerts: "News alerts", SourceHN: "Hacker News",
	SourceHNHiring: "Hacker News: Who is hiring", SourceWeb: "Company web pages", SourceX: "X posts"}

// Event types of the rubric.
const (
	TypeFunding    = "funding"
	TypeLeadership = "leadership"
	TypeExpansion  = "expansion"
	TypeProduct    = "product"
	TypeOther      = "other"
)

// Hiring intent of an assessment.
const (
	IntentStrong   = "strong"
	IntentModerate = "moderate"
	IntentIndirect = "indirect"
	IntentNone     = "none"
)

// Scale bands of an assessment.
const (
	ScaleHeadcount    = "explicit_headcount"
	ScaleUnquantified = "explicit_unquantified"
	ScaleVague        = "vague"
)

// Assessment statuses. Only StatusOK contributes to a score.
const (
	StatusOK            = "ok"
	StatusSchemaInvalid = "schema_invalid"
	StatusQuoteFailed   = "quote_failed"
	StatusMidConfidence = "mid_confidence"
	StatusContradictory = "contradictory"
)

// Tiers.
const (
	TierHot   = "hot"
	TierWarm  = "warm"
	TierWatch = "watch"
	TierCold  = "cold"
)

// Company states.
const (
	StateNew       = "new"
	StateWatching  = "watching"
	StateDismissed = "dismissed"
)

// Identity statuses of a company's ATS.
const (
	IdentityResolved   = "resolved"
	IdentityUnresolved = "unresolved"
	IdentityNoATS      = "no_ats"
)

// RubricVersion names the assessment rubric; an assessment is cached per
// (event, rubric version).
const RubricVersion = "r1"

// Source statuses in a tick summary.
const (
	SrcOK       = "ok"
	SrcFailed   = "failed"
	SrcSkipped  = "skipped"
	SrcDisabled = "disabled"
)

// Tick statuses.
const (
	TickRunning   = "running"
	TickCompleted = "completed"
	TickFailed    = "failed"
)

// maxBodyBytes caps a stored document body.
const maxBodyBytes = 64 << 10

// Doc is the uniform document a collector emits.
type Doc struct {
	SourceID    string
	Adapter     string
	URL         string
	FetchedAt   time.Time
	PublishedAt time.Time
	Title       string
	Body        string
	// Filing is set for an SEC filing; the company event comes from it.
	Filing *Filing
}

// Filing is what a Form D says.
type Filing struct {
	Issuer    string    `json:"issuer"`
	Amount    float64   `json:"amount,omitempty"`
	Industry  string    `json:"industry,omitempty"`
	State     string    `json:"state,omitempty"`
	FirstSale time.Time `json:"firstSale"`
}

// Company is one identified company.
type Company struct {
	ID    int64
	Name  string
	Canon string
	State string
}

// Event is a merged company event.
type Event struct {
	ID        int64
	CompanyID int64
	Type      string
	Time      time.Time
	Amount    *float64
}

// Quote is one evidence quote of an assessment.
type Quote struct {
	DocumentID int64  `json:"documentId"`
	Quote      string `json:"quote"`
}

// Assessment is the stored model verdict on an event.
type Assessment struct {
	ID         int64
	EventID    int64
	Rubric     string
	Status     string
	Type       string
	Intent     string
	Teams      []string
	Growth     *bool
	Scale      string
	Strength   int
	Confidence float64
	Reasoning  string
	Quotes     []Quote
	CreatedAt  time.Time
}
