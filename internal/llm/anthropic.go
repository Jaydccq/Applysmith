package llm

import (
	"context"
	"encoding/json"
	"strings"

	"applysmith/internal/lazyregexp"
)

const (
	anthropicVersion = "2023-06-01"
	anthropicDefault = "https://api.anthropic.com"
)

type anEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage anUsage `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage anUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

// budgetThinking matches the model ids before 4.6 (Claude 3, and 4 through
// 4.5, with or without a date suffix), which take a thinking token budget.
var budgetThinking = lazyregexp.New(`^claude-(3-|(haiku|sonnet|opus)-4(-[0-5])?(-\d{8})?$)`)

// anthropicThinking builds the thinking and effort fields. Models before 4.6
// take a token budget; 4.6 and later take adaptive thinking with an effort
// level, and the highest level maps to the maximum effort.
func anthropicThinking(model, level string, maxTokens int, body map[string]any) {
	if level == ThinkOff || level == "" {
		return
	}
	if budgetThinking.MatchString(model) {
		budgets := map[string]int{ThinkMinimal: 1024, ThinkLow: 2048, ThinkMedium: 8192, ThinkHigh: 16384, ThinkXHigh: 32000}
		if b := min(budgets[level], maxTokens-1024); b >= 1024 {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": b}
		}
		return
	}
	effort := map[string]string{ThinkMinimal: "low", ThinkLow: "low", ThinkMedium: "medium", ThinkHigh: "high", ThinkXHigh: "max"}[level]
	body["thinking"] = map[string]any{"type": "adaptive"}
	out, _ := body["output_config"].(map[string]any)
	if out == nil {
		out = map[string]any{}
		body["output_config"] = out
	}
	out["effort"] = effort
}

func (r *Router) anthropicComplete(ctx context.Context, a *attempt, in attemptInput) (completion, error) {
	p := a.cand.provider
	base := p.BaseURL
	if base == "" {
		base = anthropicDefault
	}
	body := map[string]any{
		"model": a.cand.model, "stream": true, "max_tokens": max(in.maxTokens, 1),
		"messages": []oaMessage{{"user", in.prompt}},
	}
	if in.system != "" {
		body["system"] = in.system
	}
	anthropicThinking(a.cand.model, in.thinking, in.maxTokens, body)
	if in.schema != nil {
		out, _ := body["output_config"].(map[string]any)
		if out == nil {
			out = map[string]any{}
			body["output_config"] = out
		}
		reduced, err := anthropicSchema(in.schema)
		if err != nil {
			return completion{}, &Error{Kind: KindBadRequest, Message: err.Error()}
		}
		out["format"] = map[string]any{"type": "json_schema", "schema": reduced}
	}
	resp, err := r.post(ctx, joinURL(base, "/v1/messages"),
		map[string]string{"x-api-key": a.key, "anthropic-version": anthropicVersion}, body, a.key)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()
	a.status = resp.StatusCode

	var out completion
	var text strings.Builder
	sawStop, started := false, false
	err = readSSE(resp.Body, func(data string) error {
		if data == "" {
			return nil
		}
		a.touch()
		var ev anEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return &Error{Kind: KindOther, Message: "unreadable stream event"}
		}
		switch ev.Type {
		case "message_start":
			u := ev.Message.Usage
			// Cache reads and writes are reported apart from input_tokens; the
			// ledger counts them in the input total.
			out.usage.Input = u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
			out.usage.CachedInput = u.CacheReadTokens
			out.usage.CacheWrite = u.CacheCreationTokens
			started = true
			out.usage.Output = u.OutputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				text.WriteString(ev.Delta.Text)
				in.emit(ev.Delta.Text)
			}
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				out.usage.Output = ev.Usage.OutputTokens
			}
			if f := anthropicFinish(ev.Delta.StopReason); f != "" {
				out.finish = f
			}
		case "message_stop":
			sawStop = true
		case "error":
			return anthropicStreamError(ev, a.key)
		}
		return nil
	})
	out.text = text.String()
	if !started {
		out.estimateUsage(in.system+in.prompt, out.text)
	}
	if err == nil && !sawStop {
		err = &Error{Kind: KindNetwork, Message: "the stream ended before message_stop"}
	}
	return out, err
}

// anthropicFinish is the ledger's finish reason for a stop reason; empty for none.
func anthropicFinish(stopReason string) string {
	switch stopReason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	}
	return stopReason
}

// anthropicStreamError classifies an error event of a Messages stream.
func anthropicStreamError(ev anEvent, key string) *Error {
	e := &Error{Kind: KindOther, Message: boundedText(ev.Error.Type+" "+ev.Error.Message, key)}
	switch ev.Error.Type {
	case "overloaded_error", "api_error":
		e.Kind = KindOverloaded
	case "rate_limit_error":
		e.Kind = KindRateLimited
	case "authentication_error", "permission_error":
		e.Kind = KindAuth
	case "not_found_error":
		e.Kind = KindModelGone
	case "invalid_request_error":
		e.Kind = KindBadRequest
	}
	return e
}
