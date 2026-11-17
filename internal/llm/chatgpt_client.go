// Clean-room: docs/provenance.md#CR-SUBSCRIPTIONS
// Written from OpenAI's public Sign in with ChatGPT documentation and the
// research spec (section 1a) alone.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The chatgpt kind calls POST /v1/responses with the access token of the plan
// sign-in. The route requires store:false and stream:true, takes the history in
// input and the system text in instructions, and rejects a list of fields
// (background, max_output_tokens, temperature, top_p, user and others), so
// this client never builds them: the output ceiling of a row cannot be sent.
// A reply has run a little past its row's ceiling (an apply-check of 546
// tokens against 512) and a runaway far past it (a deep-eval of about 178k
// against 24,000, until its 600 s timeout), so the client leaves a stream
// whose own text passes twice the ceiling (runawayFactor, by the length
// estimate the ledger uses) as a runaway, after which the walk advances as it
// did when such a reply ran into the timeout.

// chatgptRejected are the request fields the plan route refuses (research.md,
// section 1a). The router's own client never builds them; the agent gateway
// removes them from the Responses request pi sends.
var chatgptRejected = []string{
	"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation", "multi_agent",
	"prompt", "prompt_cache_retention", "prompt_cache_options", "safety_identifier", "temperature", "top_logprobs",
	"top_p", "truncation", "user",
}

// runawayFactor is how many times its row's output ceiling a plan-route reply
// may run before the client leaves it as a runaway; errPastCeiling stops the
// read there.
const runawayFactor = 2

var errPastCeiling = errors.New("the reply passed the output ceiling")

// chatgptModelsTTL is how long the account's model list is reused.
const chatgptModelsTTL = 10 * time.Minute

// ChatGPTModel is one model the signed-in account lists.
type ChatGPTModel struct {
	Slug string
	Name string
}

type chatgptModelCache struct {
	at     time.Time
	models []ChatGPTModel
}

// chatgptBase is the Responses base URL: the configured chatgpt provider's,
// else the preset's.
func (r *Router) chatgptBase(ctx context.Context) string {
	if s, err := LoadSettings(ctx, r.store); err == nil {
		for _, p := range s.Providers {
			if p.Kind == KindChatGPT && p.BaseURL != "" {
				return p.BaseURL
			}
		}
	}
	pr, _ := PresetByName("chatgpt")
	return pr.BaseURL
}

// ChatGPTModels lists the models of the signed-in account (visibility "list",
// in the server's order). The list depends on the account, so it is read live
// and reused for ten minutes.
func (r *Router) ChatGPTModels(ctx context.Context) ([]ChatGPTModel, error) {
	c := &r.chatgpt
	c.mu.Lock()
	cached, fresh := c.models, r.now().Sub(c.models.at) < chatgptModelsTTL && c.models.models != nil
	c.mu.Unlock()
	if fresh {
		return cached.models, nil
	}
	token, err := r.chatgptToken(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := r.chatgptRequest(ctx, http.MethodGet, joinURL(r.chatgptBase(ctx), "/models"), token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Models []struct {
			Slug       string `json:"slug"`
			Name       string `json:"display_name"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxWholeReply)).Decode(&list); err != nil {
		return nil, &Error{Kind: KindOther, Message: "the model list is unreadable"}
	}
	out := []ChatGPTModel{}
	for _, m := range list.Models {
		if m.Visibility == "list" && m.Slug != "" {
			out = append(out, ChatGPTModel{Slug: m.Slug, Name: m.Name})
		}
	}
	c.mu.Lock()
	c.models = chatgptModelCache{at: r.now(), models: out}
	c.mu.Unlock()
	return out, nil
}

// chatgptRequest sends one authorized request. A non-2xx reply becomes a
// classified *Error; a 401 also drops the access token, so the next call
// renews it or learns that the sign-in is over.
func (r *Router) chatgptRequest(ctx context.Context, method, url, token string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, &Error{Kind: KindOther, Message: Redact(err.Error(), token)}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
	}
	// The plan's own clients name the conversation in these headers as well as
	// in prompt_cache_key (pi 1.0.4, buildSSEHeaders). Without them a turn's
	// second request, four seconds after its first, read no cached token in
	// about 70% of the owner's calls.
	if m, _ := body.(map[string]any); m != nil {
		if key, _ := m["prompt_cache_key"].(string); key != "" {
			req.Header.Set("session-id", key)
			req.Header.Set("x-client-request-id", key)
		}
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	var eb struct {
		Error  struct{ Code, Message, Param string }
		Detail string
	}
	_ = json.Unmarshal(b, &eb)
	msg := eb.Error.Message
	if msg == "" {
		msg = eb.Detail
	}
	if msg == "" {
		msg = string(b)
	}
	e := classifyChatGPT(resp.StatusCode, eb.Error.Code, boundedText(msg, token), eb.Error.Param)
	e.retryAfter = retryHint(resp.Header.Get("Retry-After"), string(b), time.Now())
	if resp.StatusCode == http.StatusUnauthorized {
		r.forgetChatGPTAccess(token)
	}
	return nil, e
}

// classifyChatGPT maps a refusal to the router's kinds by the exact error code
// of the plan route, else by status. status is 0 for a failure that arrived
// inside the stream; the documented status of the code is then used.
func classifyChatGPT(status int, code, message, param string) *Error {
	const prefix = "subscription_sharing_"
	var e *Error
	switch code {
	case prefix + "usage_limit_exceeded":
		// The reply gives no reset time and an app-specific limit can apply, so
		// none is guessed; a Retry-After header, if sent, is used by the caller.
		e = &Error{Kind: KindQuota, Status: http.StatusTooManyRequests, Message: "ChatGPT plan usage limit reached; see ChatGPT settings, Usage (" + code + ")"}
	case prefix + "usage_unavailable", prefix + "user_unavailable":
		e = &Error{Kind: KindOverloaded, Status: http.StatusServiceUnavailable, Message: "ChatGPT could not check the plan usage right now (" + code + ")"}
	case prefix + "user_not_eligible":
		e = &Error{Kind: KindAuth, Status: http.StatusForbidden, Message: "this ChatGPT plan, workspace or policy cannot be used by apps (" + code + ")"}
	case prefix + "invalid_user":
		e = &Error{Kind: KindAuth, Status: http.StatusUnauthorized, Message: "ChatGPT could not validate the signed-in user; sign in again if this persists (" + code + ")"}
	case prefix + "route_not_supported", "chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
		e = &Error{Kind: KindAuth, Status: http.StatusForbidden, Message: "ChatGPT refused this request's permission (" + code + ")"}
	case prefix + "unsupported_capability":
		e = &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Message: "ChatGPT plan usage does not support part of this request (" + code + ", param " + param + ")"}
	case "model_not_found":
		e = &Error{Kind: KindModelGone, Status: http.StatusNotFound, Message: message}
	}
	if e != nil {
		if status != 0 {
			e.Status = status
		}
		return e
	}
	if status == 0 {
		return &Error{Kind: KindOverloaded, Message: message}
	}
	return classifyHTTP(status, message)
}

// crEvent is the part of a Responses stream event this client reads.
type crEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Param    string `json:"param"`
	Response *struct {
		Usage *struct {
			Input   int `json:"input_tokens"`
			Output  int `json:"output_tokens"`
			Details struct {
				Cached int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutDetails struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
		Incomplete *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

func (r *Router) chatgptComplete(ctx context.Context, a *attempt, in attemptInput) (completion, error) {
	p := a.cand.provider
	pr, _ := PresetByName(p.Preset)
	body := map[string]any{"model": a.cand.model, "store": false, "stream": true,
		"input": []map[string]any{{"role": "user", "content": in.prompt}}}
	if in.system != "" {
		body["instructions"] = in.system
	}
	if k, v := reasoningField(pr, a.cand.model, in.thinking); k != "" {
		body[k] = v
	}
	if in.schema != nil {
		closed, err := openAISchema(in.schema)
		if err != nil {
			return completion{}, err
		}
		body["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "result", "schema": closed, "strict": true}}
	}
	resp, err := r.chatgptRequest(ctx, http.MethodPost, joinURL(p.BaseURL, "/responses"), a.key, body)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()
	a.status = resp.StatusCode

	var out completion
	var text strings.Builder
	done, sawUsage := false, false
	onData := func(data string) error {
		if data == "" || data == "[DONE]" {
			return nil
		}
		a.touch()
		var ev crEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return &Error{Kind: KindOther, Message: "unreadable stream event"}
		}
		if ev.Response != nil && ev.Response.Usage != nil {
			u := ev.Response.Usage
			sawUsage = true
			out.usage = Usage{Input: u.Input, Output: u.Output, CachedInput: u.Details.Cached, Reasoning: u.OutDetails.Reasoning}
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
			in.emit(ev.Delta)
			if in.maxTokens > 0 && estimatedTokens(text.Len()) > runawayFactor*in.maxTokens {
				return errPastCeiling
			}
		case "response.completed":
			done, out.finish = true, "stop"
		case "response.incomplete":
			done = true
			reason := "unknown"
			if ev.Response != nil && ev.Response.Incomplete != nil {
				reason = ev.Response.Incomplete.Reason
			}
			if reason != "max_output_tokens" {
				return &Error{Kind: KindOther, Message: "ChatGPT ended the reply early (" + boundedText(reason, a.key) + ")"}
			}
			out.finish = "length"
		case "response.failed":
			f := struct{ Code, Message, Param string }{}
			if ev.Response != nil && ev.Response.Error != nil {
				f.Code, f.Message, f.Param = ev.Response.Error.Code, ev.Response.Error.Message, ev.Response.Error.Param
			}
			return classifyChatGPT(0, f.Code, boundedText(cmpOrText(f.Message, "the response failed"), a.key), f.Param)
		case "error":
			return classifyChatGPT(0, ev.Code, boundedText(cmpOrText(ev.Message, "the stream reported an error"), a.key), ev.Param)
		}
		return nil
	}
	if err = readSSE(resp.Body, onData); errors.Is(err, errPastCeiling) {
		err = &Error{Kind: KindRunaway, Message: fmt.Sprintf("the reply ran past %d times its %d-token output ceiling", runawayFactor, in.maxTokens)}
	}
	out.text = text.String()
	if in.schema != nil {
		out.text = dropOptionalNulls(out.text, in.schema)
	}
	if !sawUsage {
		out.estimateUsage(in.system+in.prompt, out.text)
	}
	if err == nil && !done {
		err = &Error{Kind: KindNetwork, Message: "the stream ended before response.completed"}
	}
	return out, err
}
