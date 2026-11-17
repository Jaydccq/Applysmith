package llm

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The agent gateway serves the model calls of Ask Agent turns. The pi runtime
// calls it as its only provider, and every call is walked like any other task's:
// turn's row's candidates in order (agent-kernel, or agent-light for a turn the
// intent classifier called light), resting ones behind, the daily cap's cheaper
// head first, one ledger row per attempt. It moves to the next
// candidate only while nothing of the reply has reached pi (the rule of
// magpie's gateway, gateway/fallback.go), and keeps a conversation on the
// candidate that answered it while that candidate's prompt cache is worth
// keeping (affinity.go). The request is passed through, not rebuilt: only the
// fields an endpoint reads differently are rewritten for each candidate. It
// never translates between wires: a request goes to the candidates of its own
// wire, and moving a turn to another wire is pi's doing (the virtual model of
// pi_extension.mjs, docs/architecture/agent-pi-contract.md).

// Wire formats an agent call arrives in, named as pi names its APIs.
const (
	WireOpenAI    = "openai-completions"
	WireAnthropic = "anthropic-messages"
	// WireResponses is the OpenAI Responses API, the wire of the chatgpt kind.
	WireResponses = "openai-responses"
)

// AgentCall is one model request of an agent turn as pi sent it.
type AgentCall struct {
	Wire string
	Body []byte
	// Task is the row that serves the call: AgentTask or AgentLightTask, the
	// one the turn was started on.
	Task string
	// SessionID is the conversation: the ledger's run id and the affinity key.
	SessionID string
	// TurnID is the user's turn the request belongs to.
	TurnID string
	// Beta is the anthropic-beta header pi sent, passed on to Anthropic.
	Beta string
}

// wireKind is the provider kind that serves a wire.
func wireKind(wire string) (string, bool) {
	switch wire {
	case WireOpenAI:
		return KindOpenAI, true
	case WireAnthropic:
		return KindAnthropic, true
	case WireResponses:
		return KindChatGPT, true
	}
	return "", false
}

// AgentWire is the wire pi speaks to the gateway for a provider; only
// candidates of the wire's kind serve a call made on it.
func AgentWire(p ProviderConfig) string {
	switch p.Kind {
	case KindAnthropic:
		return WireAnthropic
	case KindChatGPT:
		return WireResponses
	}
	return WireOpenAI
}

// ServeAgentCall walks the candidates of the call's row for one request and relays
// the reply of the first one that answers to w. It returns nil once a reply
// was relayed, even one that broke off later (the ledger records that), and
// the call's error when nothing was written, so the caller answers it.
func (r *Router) ServeAgentCall(ctx context.Context, call AgentCall, w http.ResponseWriter) error {
	want, ok := wireKind(call.Wire)
	if !ok {
		return &Error{Kind: KindBadRequest, Task: call.Task, Message: "unknown wire " + call.Wire}
	}
	if !agentRow(call.Task) {
		return &Error{Kind: KindBadRequest, Task: call.Task, Message: "not an agent task: " + call.Task}
	}
	var body map[string]any
	if err := json.Unmarshal(call.Body, &body); err != nil {
		return &Error{Kind: KindBadRequest, Task: call.Task, Message: "the request is not a JSON object"}
	}
	if body["stream"] != true {
		return &Error{Kind: KindBadRequest, Task: call.Task, Message: "the gateway serves streamed requests only"}
	}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return &Error{Kind: KindOther, Task: call.Task, Message: err.Error()}
	}
	cs, err := r.agentCandidates(ctx, s, call.Task, want, hasImage(body["messages"]) || hasImage(body["system"]) || hasImage(body["input"]), false)
	if err != nil {
		return err
	}
	cs.runID, cs.streams, cs.noCapture = call.SessionID, true, true
	cs.cands = r.affinityOrder(call, r.restOrder(cs.cands))
	// A group moves on once per conversation it places, not per call: a call
	// affinity keeps where it is uses up no turn of the rotation.
	r.advanceGroup(s, cs.res, !cs.cands[0].kept)

	sent := false
	err = r.walk(ctx, cs, func(actx context.Context, a *attempt) error {
		return r.relay(actx, cs, a, call, body, w, &sent)
	})
	if cs.won != nil {
		r.stick(call, cs.won)
		r.countGroupCall(s, cs.res, cs.won.cand)
	}
	if sent {
		return nil
	}
	return err
}

// piNeverRetried are the phrases after which pi 1.0.4 does not retry a failed
// request (NON_RETRYABLE_PROVIDER_LIMIT_ERROR_PATTERN in pi-ai's
// utils/retry.js), among them the ChatGPT plan's usage-limit code.
var piNeverRetried = lazyregexp.New(`(?i)GoUsageLimitError|FreeUsageLimitError|Monthly usage limit reached|available balance|insufficient_quota|out of budget|quota exceeded|billing|subscription_sharing_usage_limit_exceeded`)

// AgentErrorText is the message pi reads when a call failed before anything
// reached it. pi retries a failed request when its text looks transient, which
// an answer with status 429 or 5xx does, unless it holds a phrase pi takes for
// a limit that lasts (retry.js). The virtual model of pi_extension.mjs sends
// that retry to the next wire, so a plan limit must not carry the phrase: it
// is reworded, the rest of the text stays.
func AgentErrorText(err error) string {
	return piNeverRetried.ReplaceAllString(err.Error(), "limit")
}

// AgentCapBlocked reports whether the spend limits stop a turn on the row: they
// move its calls (overLimit), the primary is metered and no cheaper candidate
// can take them.
func (r *Router) AgentCapBlocked(ctx context.Context, taskID string) bool {
	if task, _ := LookupTask(taskID); !r.overLimit(ctx, task) {
		return false
	}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return false
	}
	res, err := r.resolveWith(s, taskID)
	if err != nil {
		return false
	}
	p, ok := s.Provider(s.head(res).Provider)
	if !ok {
		return false
	}
	_, err = r.agentCandidates(ctx, s, taskID, p.Kind, false, false)
	var le *Error
	return errors.As(err, &le) && le.Kind == KindCostCap
}

// agentCandidates builds the walk of an agent call: the task row's candidates of
// the wire's kind, without the models that take no images when the request
// carries one, ordered by a group's mode (groups.go) and then by the spend
// limits as every task's call is (overLimit). advance is false for a look that
// makes no call.
func (r *Router) agentCandidates(ctx context.Context, s Settings, taskID, kind string, images, advance bool) (*callState, error) {
	task, _ := LookupTask(taskID)
	res, err := r.resolveWith(s, taskID)
	if err != nil {
		return nil, err
	}
	if _, ok := PiLogin(res.Provider); ok {
		return nil, &Error{Kind: KindUnconfigured, Task: taskID, Message: "the agent route is served by a pi login, not the gateway"}
	}
	cs := &callState{id: newCallID(), task: task, res: res, settings: s}
	fits := func(c candidate) bool { return c.provider.Kind == kind && (!images || TakesImages(c.model)) }
	listed, byMode := r.callCandidates(ctx, s, res, advance)
	for i, f := range listed {
		p, ok := s.Provider(f.Provider)
		if !ok {
			continue
		}
		c := candidate{provider: p, model: f.Model, source: "primary"}
		if i > 0 {
			c.source = "fallback"
		} else {
			c.byMode = byMode
		}
		if fits(c) {
			cs.cands = append(cs.cands, c)
		}
	}
	if len(cs.cands) == 0 {
		return nil, &Error{Kind: KindNoRoute, Task: taskID, Message: "no candidate of the agent route can serve this request"}
	}
	if r.overLimit(ctx, task) {
		primary := cs.cands[0]
		order := r.downgradeOrder(ctx, s, cs.cands)
		// The preset's fast model a downgrade may add need not take images.
		cs.cands = cs.cands[:0]
		for _, c := range order {
			if c.authOnly || fits(c) {
				cs.cands = append(cs.cands, c)
			}
		}
		if !cs.cands[0].downgraded && r.costing(ctx, s, primary.provider, primary.model, Usage{}).Billing == BillingMetered {
			return nil, &Error{Kind: KindCostCap, Task: taskID, Message: "the daily model spend cap is reached and no cheaper model is routed for the agent"}
		}
	}
	return cs, nil
}

// hasImage reports an image part anywhere in a list of messages or blocks:
// image_url (OpenAI), input_image (Responses) or image (Anthropic).
func hasImage(v any) bool {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if hasImage(e) {
				return true
			}
		}
	case map[string]any:
		if t, _ := x["type"].(string); t == "image_url" || t == "input_image" || t == "image" {
			return true
		}
		for _, e := range x {
			if hasImage(e) {
				return true
			}
		}
	}
	return false
}

// agentBody is the request for one candidate: pi's request with the model, and
// on the OpenAI wire the fields each endpoint reads differently, set for it.
func agentBody(wire string, body map[string]any, c candidate, thinking, session string) map[string]any {
	out := maps.Clone(body)
	out["model"] = c.model
	switch wire {
	case WireAnthropic:
		return out
	case WireResponses:
		// pi omits the fields the plan rejects only when it talks to
		// api.openai.com itself, so they are dropped here.
		for _, k := range chatgptRejected {
			delete(out, k)
		}
		out["store"], out["stream"] = false, true
		if session != "" {
			out["prompt_cache_key"] = session
		}
		if in, ok := out["input"].([]any); ok {
			out["input"] = developerItems(in)
		}
		return out
	}
	pr, _ := PresetByName(c.provider.Preset)
	out["stream_options"] = map[string]any{"include_usage": true}
	var ceiling any
	for _, k := range []string{"max_completion_tokens", "max_tokens"} {
		if v, ok := out[k]; ok {
			ceiling = v
			delete(out, k)
		}
	}
	if ceiling != nil {
		out[cmp.Or(pr.MaxTokensParam, "max_tokens")] = ceiling
	}
	// The thinking level is the row's, sent as this endpoint takes it.
	for _, k := range []string{"reasoning_effort", "reasoning", "thinking"} {
		delete(out, k)
	}
	if k, v := reasoningField(pr, c.model, thinking); k != "" {
		out[k] = v
	}
	if pr.Name == "zai" || pr.Name == "bigmodel" || pr.Name == "opencode-go" {
		out["tool_stream"] = true
	}
	delete(out, "store")
	delete(out, "prompt_cache_key")
	if pr.Name == "openai" {
		// What pi sends to api.openai.com itself: the conversation as the cache key.
		out["prompt_cache_key"] = session
	}
	if msgs, ok := out["messages"].([]any); ok {
		out["messages"] = agentMessages(msgs, pr.PreserveReasoning)
	}
	return out
}

// developerItems sends a system item of a Responses history as a developer
// item: the plan route rejects the system role, and pi writes it for a model it
// does not know to reason.
func developerItems(items []any) []any {
	out := make([]any, len(items))
	for i, it := range items {
		if m, ok := it.(map[string]any); ok && m["role"] == "system" {
			c := maps.Clone(m)
			c["role"] = "developer"
			it = c
		}
		out[i] = it
	}
	return out
}

// agentMessages rewrites the history for an endpoint: a developer message is
// sent as system, which every endpoint takes, and an assistant message's
// reasoning_content is kept when the endpoint requires it for tool turns.
func agentMessages(msgs []any, preserveReasoning bool) []any {
	out := make([]any, len(msgs))
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			out[i] = m
			continue
		}
		_, reasoning := mm["reasoning_content"]
		if mm["role"] != "developer" && (preserveReasoning || !reasoning) {
			out[i] = m
			continue
		}
		c := maps.Clone(mm)
		if c["role"] == "developer" {
			c["role"] = "system"
		}
		if !preserveReasoning {
			delete(c, "reasoning_content")
		}
		out[i] = c
	}
	return out
}

// relay makes one attempt: it sends the candidate's request and copies the
// event stream to w. Events are held until the first one that is not an
// error, so a reply that fails at once still moves on; from then on the
// attempt is delivered and its failures are pi's to see. A Responses stream
// opens with lifecycle events that say nothing yet (the plan reports a usage
// limit right after them), so those are held too.
func (r *Router) relay(ctx context.Context, cs *callState, a *attempt, call AgentCall, body map[string]any, w http.ResponseWriter, sent *bool) error {
	p := a.cand.provider
	var url string
	headers := map[string]string{}
	var resp *http.Response
	var err error
	out := agentBody(call.Wire, body, a.cand, cs.res.Thinking, call.SessionID)
	switch call.Wire {
	case WireAnthropic:
		url = joinURL(cmp.Or(p.BaseURL, anthropicDefault), "/v1/messages")
		headers["x-api-key"], headers["anthropic-version"] = a.key, anthropicVersion
		if call.Beta != "" {
			headers["anthropic-beta"] = call.Beta
		}
		resp, err = r.post(ctx, url, headers, out, a.key)
	case WireResponses:
		// The plan's own request and error mapping; a.key is the plan token.
		resp, err = r.chatgptRequest(ctx, http.MethodPost, joinURL(p.BaseURL, "/responses"), a.key, out)
	default:
		url = joinURL(p.BaseURL, "/chat/completions")
		pr, _ := PresetByName(p.Preset)
		headers = openAIRequestHeaders(pr, a.key, cmp.Or(call.SessionID, cs.id))
		resp, err = r.post(ctx, url, headers, out, a.key)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	a.status = resp.StatusCode

	rc := http.NewResponseController(w)
	var held []string
	var text strings.Builder
	var u streamUsage
	commit := func() error {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		*sent, a.delivered = true, true
		for _, l := range held {
			if _, err := io.WriteString(w, l+"\n"); err != nil {
				return &Error{Kind: KindAborted, Message: "pi stopped reading"}
			}
		}
		held = nil
		_ = rc.Flush()
		return nil
	}
	// complete says pi has the whole reply: the stream ended, or a chat
	// completion gave its finish reason. pi may stop reading then, before the
	// usage and the terminator; live, an OpenRouter call pi had in full was
	// recorded aborted that way and its answerer was not kept.
	complete := func() bool { return u.ended || call.Wire == WireOpenAI && u.finish != "" }
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	var failed error
scan:
	for sc.Scan() {
		line := sc.Text()
		if data, ok := strings.CutPrefix(line, "data:"); ok && strings.TrimSpace(data) != "" {
			a.touch()
			if ev := u.read(call.Wire, strings.TrimSpace(data), &text, a.key); ev != nil && failed == nil {
				failed = ev
				if !a.delivered {
					return failed
				}
			}
			if !a.delivered {
				held = append(held, line)
				if u.preamble {
					continue
				}
				if err := commit(); err != nil {
					return err
				}
				continue
			}
		}
		if !a.delivered {
			held = append(held, line)
			continue
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			if complete() {
				break scan
			}
			return &Error{Kind: KindAborted, Message: "pi stopped reading"}
		}
		if line == "" {
			_ = rc.Flush()
		}
	}
	_ = rc.Flush()
	a.usage, a.finish, a.text = u.usage, u.finish, text.String()
	if !u.counted {
		a.usage, a.estimated = estimateUsage(string(call.Body), a.text), true
	}
	if err := sc.Err(); err != nil && failed == nil && !(a.delivered && complete()) {
		failed = err
	}
	// A chat-completions finish reason ends a stream as [DONE] does; a
	// Messages or Responses stream ends with its terminal event only.
	if failed == nil && !u.ended && (call.Wire != WireOpenAI || u.finish == "") {
		failed = &Error{Kind: KindNetwork, Message: "the stream ended before its terminator"}
	}
	return failed
}

// streamUsage reads the usage, finish and errors of a relayed stream.
type streamUsage struct {
	usage   Usage
	finish  string
	counted bool // the provider reported usage
	ended   bool // the stream's terminator arrived
	// preamble is set when the last event was a Responses lifecycle event,
	// which does not yet show that the reply has begun.
	preamble bool
}

// read takes one data payload; it returns the error the event reports.
func (s *streamUsage) read(wire, data string, text *strings.Builder, key string) error {
	s.preamble = false
	if wire == WireResponses {
		return s.readResponses(data, text, key)
	}
	if wire == WireAnthropic {
		var ev anEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "message_start":
			u := ev.Message.Usage
			s.usage = Usage{Input: u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens, CachedInput: u.CacheReadTokens,
				CacheWrite: u.CacheCreationTokens, Output: u.OutputTokens}
			s.counted = true
		case "content_block_delta":
			text.WriteString(ev.Delta.Text)
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				s.usage.Output = ev.Usage.OutputTokens
			}
			if f := anthropicFinish(ev.Delta.StopReason); f != "" {
				s.finish = f
			}
		case "message_stop":
			s.ended = true
		case "error":
			return anthropicStreamError(ev, key)
		}
		return nil
	}
	if data == "[DONE]" {
		s.ended = true
		return nil
	}
	var ch oaChunk
	if json.Unmarshal([]byte(data), &ch) != nil {
		return nil
	}
	if ch.Error != nil {
		e := oaStreamError(ch.Error.Code, ch.Error.Message)
		e.Message = boundedText(ch.Error.Message, key)
		return e
	}
	if u := ch.Usage; u != nil {
		s.usage = Usage{Input: u.PromptTokens, Output: u.CompletionTokens,
			CachedInput: u.PromptTokensDetails.Cached, Reasoning: u.CompletionTokensDetails.Reasoning}
		s.counted = true
	}
	for _, c := range ch.Choices {
		text.WriteString(c.Delta.Content)
		if c.FinishReason != "" {
			s.finish = c.FinishReason
		}
	}
	return nil
}

// readResponses reads a Responses event: the usage of the terminal event, the
// text deltas, and the failures the plan reports as events.
func (s *streamUsage) readResponses(data string, text *strings.Builder, key string) error {
	var ev crEvent
	if json.Unmarshal([]byte(data), &ev) != nil {
		return nil
	}
	if ev.Response != nil && ev.Response.Usage != nil {
		u := ev.Response.Usage
		s.usage = Usage{Input: u.Input, Output: u.Output, CachedInput: u.Details.Cached, Reasoning: u.OutDetails.Reasoning}
		s.counted = true
	}
	switch ev.Type {
	case "response.created", "response.in_progress", "response.queued":
		s.preamble = true
	case "response.output_text.delta":
		text.WriteString(ev.Delta)
	case "response.completed":
		s.ended, s.finish = true, "stop"
	case "response.incomplete":
		s.ended, s.finish = true, "incomplete"
		if ev.Response != nil && ev.Response.Incomplete != nil && ev.Response.Incomplete.Reason == "max_output_tokens" {
			s.finish = "length"
		}
	case "response.failed":
		f := struct{ Code, Message, Param string }{}
		if ev.Response != nil && ev.Response.Error != nil {
			f.Code, f.Message, f.Param = ev.Response.Error.Code, ev.Response.Error.Message, ev.Response.Error.Param
		}
		s.ended = true
		return classifyChatGPT(0, f.Code, boundedText(cmpOrText(f.Message, "the response failed"), key), f.Param)
	case "error":
		return classifyChatGPT(0, ev.Code, boundedText(cmpOrText(ev.Message, "the stream reported an error"), key), ev.Param)
	}
	return nil
}
