package wire

// UsageCall is one attempt of one model call in the ledger; the attempts of one
// call share CallID. It carries no prompt or completion text. CostUSD is absent
// for a call the prices do not cover. CachedTokens is the part of InputTokens
// the provider served from its prompt cache.
type UsageCall struct {
	ID           SafeInt  `json:"id"`
	CallID       string   `json:"callId"`
	StartedAt    Time     `json:"startedAt"`
	Task         string   `json:"task"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Attempt      SafeInt  `json:"attempt"`
	Outcome      string   `json:"outcome"`
	InputTokens  SafeInt  `json:"inputTokens"`
	OutputTokens SafeInt  `json:"outputTokens"`
	CachedTokens SafeInt  `json:"cachedTokens"`
	CostUSD      *float64 `json:"costUsd,omitempty"`
	LatencyMs    SafeInt  `json:"latencyMs"`
	Downgraded   bool     `json:"downgraded"`
	RunID        string   `json:"runId,omitempty"`
	// Reason is why the attempt ran on this model: fallback (an earlier one
	// failed), first-token (an earlier one was too slow to start), rested (an
	// earlier-configured one was resting), cap-downgrade, affinity (an agent
	// conversation stayed on the model that answered it last), or rotate or
	// usage (a routing group's mode put this member before the first listed).
	// Empty for the configured first choice.
	Reason string `json:"reason,omitempty"`
	// TTFTMs is the time to the first token; absent when the attempt did not
	// stream one.
	TTFTMs *SafeInt `json:"ttftMs,omitempty"`
}

// UsageCallPage is one page of the call table, newest first; with the after
// filter it is the attempts past that ledger id, oldest first. Total counts the
// attempts the filters match.
type UsageCallPage struct {
	Calls []UsageCall `json:"calls"`
	Total SafeInt     `json:"total"`
}

// UsageDay is the spend of one local day.
type UsageDay struct {
	Day     string  `json:"day"`
	CostUSD float64 `json:"costUsd"`
	Calls   SafeInt `json:"calls"`
}

// UsageDays lists the last days, today last, with empty days as zero.
type UsageDays struct {
	Days []UsageDay `json:"days"`
}

// UsageTrace is the model calls one run or conversation made. Kind, Status and
// Stage come from the run record or, for an application, scan or evaluation the
// runs board holds, from its entry; Kind is "conversation" for an Ask Agent
// session and empty for an id with neither. Title is what to show: the board
// entry's title, else the kind, else the tasks the calls were for.
type UsageTrace struct {
	RunID     string  `json:"runId"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status,omitempty"`
	Stage     string  `json:"stage,omitempty"`
	StartedAt Time    `json:"startedAt"`
	Calls     SafeInt `json:"calls"`
	CostUSD   float64 `json:"costUsd"`
}

// UsageTraceList lists the traces, most recent first.
type UsageTraceList struct {
	Traces []UsageTrace `json:"traces"`
}

// Span kinds of a trace.
const (
	SpanRun  = "run"
	SpanCall = "call"
)

// UsageSpan is one bar of a trace's waterfall. StartMs is the offset from the
// start of the trace. Error is redacted.
type UsageSpan struct {
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	StartMs      SafeInt  `json:"startMs"`
	DurationMs   SafeInt  `json:"durationMs"`
	Outcome      string   `json:"outcome,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Attempt      SafeInt  `json:"attempt,omitempty"`
	InputTokens  SafeInt  `json:"inputTokens,omitempty"`
	OutputTokens SafeInt  `json:"outputTokens,omitempty"`
	CostUSD      *float64 `json:"costUsd,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// UsageTraceDetail is one trace with its spans in start order: the run's own
// span first when a run record exists, then one span per attempt.
type UsageTraceDetail struct {
	Trace UsageTrace  `json:"trace"`
	Spans []UsageSpan `json:"spans"`
}
