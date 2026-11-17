package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// oaAnnotation is a url citation of a web search, as OpenRouter reports it.
type oaAnnotation struct {
	Type        string `json:"type"`
	URLCitation struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Content string `json:"content"`
	} `json:"url_citation"`
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content     string         `json:"content"`
			Annotations []oaAnnotation `json:"annotations"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// reasoningField returns the request field that carries a thinking level for
// an openai-compatible endpoint, or nil when the level is not sent. Thinking
// off is the preset's disable value, or omitted when it has none, except for a
// model that cannot turn reasoning off: that gets the lowest level the
// endpoint accepts, never a disable value.
func reasoningField(pr Preset, model, level string) (string, any) {
	if pr.ReasoningStyle == "" || pr.ReasoningStyle == "none" {
		return "", nil
	}
	if pr.ReasoningStyle == "template" {
		on := (level != ThinkOff && level != "") || pr.mandatoryReasoning(model)
		return "chat_template_kwargs", map[string]bool{"enable_thinking": on}
	}
	var effort string
	switch level {
	case ThinkOff, "":
		if !pr.mandatoryReasoning(model) {
			if pr.OffEffort == "" {
				return "", nil
			}
			effort = pr.OffEffort
			break
		}
		effort = pr.LowestEffort
	case ThinkMinimal:
		effort = pr.LowestEffort
	case ThinkMedium:
		effort = level
		if pr.MediumEffort != "" {
			effort = pr.MediumEffort
		}
	case ThinkXHigh:
		effort = pr.MaxEffort
	default:
		effort = level
	}
	if pr.ReasoningStyle == "nested" {
		return "reasoning", map[string]string{"effort": effort}
	}
	return "reasoning_effort", effort
}

// deterministicTasks are the evaluation tasks whose answers feed a computed
// score or a filter: the same input should give the same evidence.
var deterministicTasks = map[string]bool{
	"deep-eval": true, "quick-screen": true, "jd-decode": true, "jd-extract": true, "feed-classify": true,
}

// deterministicTemperature says a call sends temperature 0: a deterministic
// task, with reasoning off, on an endpoint that honors temperature then.
// Reasoning modes ignore or reject the field, so it is never sent with them.
func deterministicTemperature(pr Preset, model string, in attemptInput) bool {
	off := in.thinking == ThinkOff || in.thinking == ""
	return pr.Temperature && deterministicTasks[in.task] && off && !pr.mandatoryReasoning(model)
}

// OpenAI's models, through OpenAI or through OpenRouter, take a response
// schema only in the strict form and answer 400 to anything else: every object
// closed with additionalProperties:false and listing all its properties as
// required. The sent copy is that form, with each property the original leaves
// optional made nullable instead; dropOptionalNulls takes those nulls out of
// the reply, which the caller then validates against its own schema.

func openAISchema(raw json.RawMessage) (json.RawMessage, error) {
	var s any
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("schema is not JSON: %w", err)
	}
	return json.Marshal(strictSchema(s))
}

func strictSchema(node any) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	if props, ok := out["properties"].(map[string]any); ok {
		req := requiredSet(m)
		strict := make(map[string]any, len(props))
		names := make([]string, 0, len(props))
		for name, sub := range props {
			s := strictSchema(sub)
			if !req[name] {
				s = nullable(s)
			}
			strict[name], names = s, append(names, name)
		}
		sort.Strings(names)
		out["properties"], out["required"] = strict, names
	}
	if items, has := out["items"]; has {
		out["items"] = strictSchema(items)
	}
	for _, kw := range []string{"anyOf", "allOf", "oneOf"} {
		if list, ok := out[kw].([]any); ok {
			strict := make([]any, len(list))
			for i, sub := range list {
				strict[i] = strictSchema(sub)
			}
			out[kw] = strict
		}
	}
	if _, hasProps := out["properties"]; hasProps || out["type"] == "object" {
		out["additionalProperties"] = false
	}
	return out
}

func requiredSet(schema map[string]any) map[string]bool {
	set := map[string]bool{}
	list, _ := schema["required"].([]any)
	for _, n := range list {
		if s, ok := n.(string); ok {
			set[s] = true
		}
	}
	return set
}

// nullable lets a schema also take null.
func nullable(node any) any {
	m, ok := node.(map[string]any)
	if !ok || allowsNull(m) {
		return node
	}
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	switch t := out["type"].(type) {
	case string:
		out["type"] = []any{t, "null"}
	case []any:
		out["type"] = append(append([]any{}, t...), "null")
	default:
		return map[string]any{"anyOf": []any{out, map[string]any{"type": "null"}}}
	}
	if e, ok := out["enum"].([]any); ok {
		out["enum"] = append(append([]any{}, e...), nil)
	}
	return out
}

func allowsNull(node any) bool {
	m, ok := node.(map[string]any)
	if !ok {
		return false
	}
	switch t := m["type"].(type) {
	case string:
		if t == "null" {
			return true
		}
	case []any:
		for _, v := range t {
			if v == "null" {
				return true
			}
		}
	}
	if list, ok := m["anyOf"].([]any); ok {
		for _, sub := range list {
			if allowsNull(sub) {
				return true
			}
		}
	}
	return false
}

// dropOptionalNulls removes from a reply each null the original schema does
// not allow on a property it leaves optional: the strict form's stand-in for
// "not supplied". A reply that is not JSON is returned unchanged.
func dropOptionalNulls(text string, raw json.RawMessage) string {
	var schema, reply any
	if json.Unmarshal(raw, &schema) != nil || json.Unmarshal([]byte(text), &reply) != nil {
		return text
	}
	if !dropNulls(reply, schema) {
		return text
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return text
	}
	return string(b)
}

func dropNulls(v, schema any) bool {
	s, _ := schema.(map[string]any)
	changed := false
	switch x := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		req := requiredSet(s)
		for k, val := range x {
			if val == nil && !req[k] && !allowsNull(props[k]) {
				delete(x, k)
				changed = true
				continue
			}
			changed = dropNulls(val, props[k]) || changed
		}
	case []any:
		for _, el := range x {
			changed = dropNulls(el, s["items"]) || changed
		}
	}
	return changed
}

// maxWholeReply bounds a reply read in one piece.
const maxWholeReply = 8 << 20

func (r *Router) openAIComplete(ctx context.Context, cs *callState, a *attempt, in attemptInput) (completion, error) {
	p := a.cand.provider
	pr, _ := PresetByName(p.Preset)
	msgs := []oaMessage{}
	if in.system != "" {
		msgs = append(msgs, oaMessage{"system", in.system})
	}
	msgs = append(msgs, oaMessage{"user", in.prompt})
	body := map[string]any{"model": a.cand.model, "messages": msgs, "stream": true,
		"stream_options": map[string]any{"include_usage": true}}
	if in.webSearch > 0 {
		// A streamed reply carries no url citations for some models; the
		// whole reply does. The caller does not read deltas of a search.
		body["stream"] = false
		delete(body, "stream_options")
	}
	if in.maxTokens > 0 {
		param := pr.MaxTokensParam
		if param == "" {
			param = "max_tokens"
		}
		body[param] = in.maxTokens
	}
	if k, v := reasoningField(pr, a.cand.model, in.thinking); k != "" {
		body[k] = v
	}
	if deterministicTemperature(pr, a.cand.model, in) {
		body["temperature"] = 0
	}
	if in.webSearch > 0 {
		body["plugins"] = []any{map[string]any{"id": "web", "engine": "exa", "max_results": in.webSearch}}
	}
	if in.schema != nil {
		closed, err := openAISchema(in.schema)
		if err != nil {
			return completion{}, err
		}
		body["response_format"] = map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": "result", "schema": closed}}
	}
	session := cs.runID
	if session == "" {
		session = cs.id
	}
	resp, err := r.post(ctx, joinURL(p.BaseURL, "/chat/completions"), openAIRequestHeaders(pr, a.key, session), body, a.key)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()
	a.status = resp.StatusCode

	var out completion
	var text strings.Builder
	sawDone, sawUsage := false, false
	onData := func(data string) error {
		if data == "[DONE]" {
			sawDone = true
			return nil
		}
		if data == "" {
			return nil
		}
		a.touch()
		var ch oaChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return &Error{Kind: KindOther, Message: "unreadable stream event"}
		}
		if ch.Error != nil {
			e := oaStreamError(ch.Error.Code, ch.Error.Message)
			e.Message = boundedText(ch.Error.Message, a.key)
			return e
		}
		if u := ch.Usage; u != nil {
			sawUsage = true
			out.usage = Usage{Input: u.PromptTokens, Output: u.CompletionTokens,
				CachedInput: u.PromptTokensDetails.Cached, Reasoning: u.CompletionTokensDetails.Reasoning}
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				in.emit(c.Delta.Content)
			}
			if c.Message.Content != "" {
				text.WriteString(c.Message.Content)
				in.emit(c.Message.Content)
			}
			if c.FinishReason != "" {
				out.finish = c.FinishReason
			}
			out.addSources(c.Message.Annotations)
		}
		return nil
	}
	if in.webSearch > 0 {
		var raw []byte
		if raw, err = io.ReadAll(io.LimitReader(resp.Body, maxWholeReply)); err == nil {
			err = onData(string(raw))
			sawDone = true
		}
	} else {
		err = readSSE(resp.Body, onData)
	}
	out.text = text.String()
	if in.schema != nil {
		out.text = dropOptionalNulls(out.text, in.schema)
	}
	if !sawUsage {
		out.estimateUsage(in.system+in.prompt, out.text)
	}
	if err == nil && !sawDone && out.finish == "" {
		err = &Error{Kind: KindNetwork, Message: "the stream ended before its terminator"}
	}
	if out.finish == "" && err == nil {
		out.finish = "stop"
	}
	return out, err
}

// oaStreamError classifies an error event inside a 200 stream by its code: a
// number is the upstream status (OpenRouter), a string is the API's own code
// (OpenAI). Without a usable code the text decides, and a failure the text
// does not name is the server's, so the walk moves on.
func oaStreamError(code any, message string) *Error {
	switch c := code.(type) {
	case float64:
		if c >= 400 && c < 600 {
			return classifyHTTP(int(c), message)
		}
	case string:
		switch {
		case quotaBody.MatchString(c):
			return &Error{Kind: KindQuota, Message: message}
		case strings.Contains(c, "rate_limit"):
			return &Error{Kind: KindRateLimited, Message: message}
		}
	}
	e := classifyError(errors.New(message))
	if e.Kind == KindOther {
		e.Kind = KindOverloaded
	}
	return e
}

// addSources keeps each url citation once, in the order the stream sent them.
func (c *completion) addSources(list []oaAnnotation) {
	for _, a := range list {
		u := a.URLCitation.URL
		if a.Type != "url_citation" || u == "" || slices.ContainsFunc(c.sources, func(s Source) bool { return s.URL == u }) {
			continue
		}
		c.sources = append(c.sources, Source{URL: u, Title: a.URLCitation.Title, Content: a.URLCitation.Content})
	}
}

// openAIRequestHeaders identifies the client and conversation for Go's routing
// and prompt cache. It never claims that Applysmith is the OpenCode or Kimi client:
// Kimi Code's terms forbid a changed client identifier.
func openAIRequestHeaders(pr Preset, key, session string) map[string]string {
	headers := map[string]string{"Authorization": "Bearer " + key}
	switch pr.Name {
	case "opencode-go":
		headers["User-Agent"] = "applysmith/1.0"
		headers["x-opencode-session"] = session
	case "kimi-code":
		headers["User-Agent"] = "applysmith/1.0"
	}
	return headers
}
