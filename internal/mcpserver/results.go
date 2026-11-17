package mcpserver

import (
	"encoding/json"
	"unicode/utf8"

	"applysmith/internal/api/wire"
)

// maxResultBytes bounds the JSON of one result. A result shortened to fit
// carries truncated and originalBytes, the size of the JSON of the whole
// result before it was shortened; that is what originalBytes means in every
// tool.
const maxResultBytes = 100_000

// CandidacyItem is the projection of a tracker row. Fields are copied one by
// one: a field the API adds later reaches a client only when listed here.
type CandidacyItem struct {
	ID           int64    `json:"id"`
	Company      string   `json:"company"`
	Role         string   `json:"role"`
	Status       string   `json:"status"`
	Score        *float64 `json:"score,omitempty"`
	EvaluatedAt  string   `json:"evaluatedAt,omitempty"`
	AppliedAt    string   `json:"appliedAt,omitempty"`
	ReportNumber *int64   `json:"reportNumber,omitempty"`
	URL          string   `json:"url,omitempty"`
	Source       string   `json:"source,omitempty"`
	AppliedBy    string   `json:"appliedBy,omitempty"`
	UpdatedAt    string   `json:"updatedAt"`
}

func itemOf(c wire.Candidacy) CandidacyItem {
	it := CandidacyItem{
		ID: int64(c.ID), Company: c.Company, Role: c.Role, Status: c.Status, Score: c.Score,
		EvaluatedAt: c.Date, AppliedAt: c.AppliedAt, URL: c.PostingURL, Source: c.Source,
		AppliedBy: c.AppliedBy, UpdatedAt: c.UpdatedAt.Format(timeLayout),
	}
	if c.ReportNumber != nil {
		n := int64(*c.ReportNumber)
		it.ReportNumber = &n
	}
	return it
}

const timeLayout = "2006-01-02T15:04:05.000Z"

// CandidaciesResult is the output of list_candidacies.
type CandidaciesResult struct {
	Items         []CandidacyItem `json:"items"`
	Page          int             `json:"page"`
	PageSize      int             `json:"pageSize"`
	Total         int             `json:"total"`
	Truncated     bool            `json:"truncated,omitempty"`
	OriginalBytes int             `json:"originalBytes,omitempty"`
}

// HistoryEntry is one status a tracked application held.
type HistoryEntry struct {
	Status string `json:"status"`
	At     string `json:"at"`
	Actor  string `json:"actor"`
}

// LookupResult is the output of get_application_by_url: found, and when a
// tracked application matches, the item fields and its status history.
type LookupResult struct {
	Found        bool           `json:"found"`
	ID           *int64         `json:"id,omitempty"`
	Company      string         `json:"company,omitempty"`
	Role         string         `json:"role,omitempty"`
	Status       string         `json:"status,omitempty"`
	Score        *float64       `json:"score,omitempty"`
	EvaluatedAt  string         `json:"evaluatedAt,omitempty"`
	AppliedAt    string         `json:"appliedAt,omitempty"`
	ReportNumber *int64         `json:"reportNumber,omitempty"`
	URL          string         `json:"url,omitempty"`
	Source       string         `json:"source,omitempty"`
	AppliedBy    string         `json:"appliedBy,omitempty"`
	UpdatedAt    string         `json:"updatedAt,omitempty"`
	History      []HistoryEntry `json:"history,omitempty"`
}

func lookupOf(it CandidacyItem, history []HistoryEntry) LookupResult {
	return LookupResult{Found: true, ID: &it.ID, Company: it.Company, Role: it.Role, Status: it.Status, Score: it.Score,
		EvaluatedAt: it.EvaluatedAt, AppliedAt: it.AppliedAt, ReportNumber: it.ReportNumber, URL: it.URL,
		Source: it.Source, AppliedBy: it.AppliedBy, UpdatedAt: it.UpdatedAt, History: history}
}

// ReportResult is the output of get_report.
type ReportResult struct {
	Number        int64             `json:"number"`
	Company       string            `json:"company"`
	Role          string            `json:"role"`
	Score         *float64          `json:"score,omitempty"`
	URL           string            `json:"url,omitempty"`
	Header        map[string]string `json:"header"`
	Content       string            `json:"content"`
	Truncated     bool              `json:"truncated,omitempty"`
	OriginalBytes int               `json:"originalBytes,omitempty"`
}

// ApplyItem is one row of the apply list, without any answer or form value.
type ApplyItem struct {
	Company string `json:"company"`
	Role    string `json:"role"`
	Host    string `json:"host,omitempty"`
	Lane    string `json:"lane,omitempty"`
	State   string `json:"state,omitempty"`
	Reason  string `json:"reason,omitempty"`
	RunID   string `json:"runId,omitempty"`
}

// ApplyStatusResult is the output of apply_list_status. Truncated is also set
// when the list holds more rows than the tool reads; OriginalBytes is there
// only when the result was shortened to fit.
type ApplyStatusResult struct {
	Counts        map[string]int `json:"counts"`
	Lanes         map[string]int `json:"lanes"`
	Items         []ApplyItem    `json:"items"`
	Truncated     bool           `json:"truncated,omitempty"`
	OriginalBytes int            `json:"originalBytes,omitempty"`
}

// HealthComponent is the state of one server component.
type HealthComponent struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// HealthResult is the output of health.
type HealthResult struct {
	ServerRunning bool              `json:"serverRunning"`
	Hint          string            `json:"hint,omitempty"`
	Status        string            `json:"status,omitempty"`
	APIVersion    int               `json:"apiVersion,omitempty"`
	Version       string            `json:"version,omitempty"`
	BuildID       string            `json:"buildId,omitempty"`
	WorkspaceID   string            `json:"workspaceId,omitempty"`
	StartedAt     string            `json:"startedAt,omitempty"`
	UptimeSeconds *int64            `json:"uptimeSeconds,omitempty"`
	Components    []HealthComponent `json:"components,omitempty"`
}

func sizeOf(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// fitCandidacies keeps the longest prefix of items whose result fits the limit.
func fitCandidacies(r *CandidaciesResult) {
	full := sizeOf(r)
	if full <= maxResultBytes {
		return
	}
	all := r.Items
	n := longest(len(all), func(k int) bool {
		r.Items = all[:k]
		r.Truncated, r.OriginalBytes = true, full
		return sizeOf(r) <= maxResultBytes
	})
	r.Items = all[:n]
	r.Truncated, r.OriginalBytes = true, full
}

// fitApply keeps the longest prefix of items whose result fits the limit.
func fitApply(r *ApplyStatusResult) {
	full := sizeOf(r)
	if full <= maxResultBytes {
		return
	}
	all := r.Items
	n := longest(len(all), func(k int) bool {
		r.Items = all[:k]
		r.Truncated, r.OriginalBytes = true, full
		return sizeOf(r) <= maxResultBytes
	})
	r.Items = all[:n]
	r.Truncated, r.OriginalBytes = true, full
}

// fitReport shortens the Markdown, at a character boundary, until the whole
// result fits.
func fitReport(r *ReportResult) {
	full := sizeOf(r)
	if full <= maxResultBytes {
		return
	}
	content := r.Content
	n := longest(len(content), func(k int) bool {
		r.Content = content[:runeStart(content, k)]
		r.Truncated, r.OriginalBytes = true, full
		return sizeOf(r) <= maxResultBytes
	})
	r.Content = content[:runeStart(content, n)]
	r.Truncated, r.OriginalBytes = true, full
}

// longest returns the largest k in [0, max] for which fits(k) holds, assuming
// fits is monotone: a shorter prefix never fits worse.
func longest(max int, fits func(int) bool) int {
	lo, hi := 0, max
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// runeStart moves k back to the start of the character it falls inside.
func runeStart(s string, k int) int {
	for k > 0 && k < len(s) && !utf8.RuneStart(s[k]) {
		k--
	}
	return k
}
