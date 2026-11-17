package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// piResponsesRequest is a request as pi builds it for the Responses API
// behind the gateway: the system text as an item, a tool call with its output,
// and the fields the plan route rejects, which pi omits only when it talks to
// api.openai.com itself.
func piResponsesRequest() map[string]any {
	return map[string]any{
		"model": "gpt-6.1-sol", "stream": true, "store": true,
		"input": []any{
			map[string]any{"role": "system", "content": "You are the agent."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "How many applications?"}}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "tracker_query", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "12"},
		},
		"tools":             []any{map[string]any{"type": "function", "name": "tracker_query", "parameters": map[string]any{"type": "object"}}},
		"reasoning":         map[string]any{"effort": "medium", "summary": "auto"},
		"include":           []any{"reasoning.encrypted_content"},
		"prompt_cache_key":  "pi-own-session",
		"max_output_tokens": 16000, "temperature": 0.2, "top_p": 1, "user": "pi",
		"prompt_cache_retention": "24h", "prompt_cache_options": map[string]any{"ttl": "30m"}, "metadata": map[string]any{"a": "b"},
	}
}

// planStream is the exact byte stream of a plan reply that calls a tool.
const planStream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"tracker_query\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"counting\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":120,\"input_tokens_details\":{\"cached_tokens\":100},\"output_tokens\":7,\"output_tokens_details\":{\"reasoning_tokens\":3},\"total_tokens\":127}}}\n\n"

func writeNow(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, s)
}

// planEnv is a signed-in router whose agent-kernel row is the plan, with a
// Chat Completions provider "a" behind it, and the plan API's recorder.
func planEnv(t *testing.T, plan func(http.ResponseWriter, *http.Request, int)) (*env, *recorder, *oaServer) {
	t.Helper()
	e, f := chatgptEnv(t)
	e.signIn(f)
	rc := &recorder{next: plan}
	f.api = rc.serve
	sa := newOAServer(t, okHandler("from a"))
	pa := oaProvider("a", sa)
	pa.Preset = "deepseek"
	e.provider(pa, fakeKey("ga"))
	e.route(AgentTask, Route{Provider: "chatgpt", Model: "gpt-6.1-sol", Thinking: ThinkMedium,
		Fallbacks: []Fallback{{"a", "deepseek-flash"}}})
	return e, rc, sa
}

func (e *env) responsesCall(session, turn string, body map[string]any) (*httptest.ResponseRecorder, error) {
	e.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	return w, e.r.ServeAgentCall(bg(), AgentCall{Wire: WireResponses, Body: raw, SessionID: session, TurnID: turn, Task: AgentTask}, w)
}

func TestLLM_Gateway_PassesTheResponsesRequestToThePlanAndRelaysItsStream(t *testing.T) {
	e, rc, sa := planEnv(t, func(w http.ResponseWriter, r *http.Request, n int) { writeNow(w, planStream) })
	w, err := e.responsesCall("ses_1", "t1", piResponsesRequest())
	if err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != planStream {
		t.Fatalf("the stream was not relayed byte for byte:\n%q", w.Body.String())
	}
	got := rc.last()
	if got.path != "/v1/responses" || got.contentType != "application/json" || got.session != "ses_1" {
		t.Fatalf("request = %+v", got)
	}
	token, err := e.r.chatgptToken(bg())
	if err != nil || got.auth != "Bearer "+token {
		t.Fatalf("authorization = %q, want the plan token", got.auth)
	}
	for _, f := range planRejected {
		if _, sent := got.body[f]; sent {
			t.Errorf("the plan route rejects %q and it was sent", f)
		}
	}
	items := got.body["input"].([]any)
	if got.body["model"] != "gpt-6.1-sol" || got.body["store"] != false || got.body["stream"] != true || got.body["prompt_cache_key"] != "ses_1" ||
		items[0].(map[string]any)["role"] != "developer" || len(items) != 4 || got.body["tools"] == nil || fmt.Sprint(got.body["reasoning"]) != "map[effort:medium summary:auto]" {
		t.Fatalf("body = %v", got.body)
	}
	if sa.count() != 0 {
		t.Fatal("a Responses call reached a Chat Completions provider")
	}
	rows := e.ledger()
	if len(rows) != 1 || rows[0].Provider != "chatgpt" || rows[0].Outcome != "ok" || rows[0].Billing != BillingSubscription ||
		rows[0].Cost == nil || *rows[0].Cost != 0 || rows[0].Input != 120 || rows[0].Cached != 100 || rows[0].Output != 7 || rows[0].Finish != "stop" {
		t.Fatalf("ledger = %+v", rows)
	}
}

// What the plan reports as a failure before the reply has begun is an error
// for pi, whether it comes as a status or as an event right after the stream's
// opening events; the provider rests, and no other wire is tried here.
func TestLLM_Gateway_APlanFailureBeforeTheReplyBeginsIsAnErrorForPi(t *testing.T) {
	limit := `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}`
	cases := map[string]func(http.ResponseWriter, *http.Request, int){
		"429 status": crFail(429, nil, limit),
		"failed event after created": func(w http.ResponseWriter, r *http.Request, n int) {
			crEvents(w, `{"type":"response.created"}`, `{"type":"response.in_progress"}`,
				`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}}`)
		},
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			e, _, sa := planEnv(t, plan)
			w, err := e.responsesCall("ses_1", "t1", piResponsesRequest())
			wantKind(t, err, KindQuota)
			if w.Body.Len() != 0 || sa.count() != 0 {
				t.Fatalf("nothing may be relayed, and no other wire is tried: body %q, chat calls %d", w.Body.String(), sa.count())
			}
			if o := outcomes(e.ledger()); o != "chatgpt:quota-exhausted" {
				t.Fatalf("ledger = %s", o)
			}
			// The rest puts the plan behind the Chat Completions provider.
			route, rerr := e.r.AgentRoute(bg(), AgentTask)
			if rerr != nil || len(route.Heads) != 2 || route.Heads[0].Wire != WireOpenAI || route.Heads[1].Wire != WireResponses {
				t.Fatalf("heads after the failure = %+v, %v", route.Heads, rerr)
			}
			if piNeverRetried.MatchString(AgentErrorText(err)) {
				t.Fatalf("pi would not retry this: %q", AgentErrorText(err))
			}
		})
	}
}

// A Responses call that carries an image is served by a model that takes
// images, as the other wires' calls are.
func TestLLM_Gateway_AResponsesImageSkipsTextOnlyModels(t *testing.T) {
	e, rc, _ := planEnv(t, func(w http.ResponseWriter, r *http.Request, n int) { writeNow(w, planStream) })
	req := piResponsesRequest()
	req["input"] = append(req["input"].([]any), map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}})
	if _, err := e.responsesCall("ses_1", "t1", req); err != nil || rc.count() != 1 {
		t.Fatalf("gpt-6.1-sol takes images: %v, %d calls", err, rc.count())
	}
	e.route(AgentTask, Route{Provider: "chatgpt", Model: "text-only-model"})
	_, err := e.responsesCall("ses_2", "t1", req)
	wantKind(t, err, KindNoRoute)
	if rc.count() != 1 {
		t.Fatal("a model that takes no images was sent one")
	}
}

// A plan stream that stops without its terminal event is a network failure in
// the ledger, though what was relayed stays relayed.
func TestLLM_Gateway_AResponsesStreamThatEndsWithoutItsTerminalEventIsRecordedAsFailed(t *testing.T) {
	cut := strings.SplitAfter(planStream, "\n\n")[:4] // created .. text delta, no response.completed
	e, _, _ := planEnv(t, func(w http.ResponseWriter, r *http.Request, n int) { writeNow(w, strings.Join(cut, "")) })
	w, err := e.responsesCall("ses_1", "t1", piResponsesRequest())
	if err != nil || !strings.Contains(w.Body.String(), "counting") {
		t.Fatalf("what was relayed stays relayed: %v %q", err, w.Body.String())
	}
	if o := outcomes(e.ledger()); o != "chatgpt:network" {
		t.Fatalf("ledger = %s", o)
	}
}

func TestLLM_AgentErrorText_NoPhraseLeftThatMakesPiGiveUp(t *testing.T) {
	err := &Error{Kind: KindQuota, Message: "ChatGPT plan usage limit reached; see ChatGPT settings, Usage (subscription_sharing_usage_limit_exceeded)"}
	text := AgentErrorText(err)
	if piNeverRetried.MatchString(text) || !strings.Contains(text, "ChatGPT plan usage limit reached") {
		t.Fatalf("text = %q", text)
	}
	if plain := &(Error{Kind: KindOverloaded, Message: "busy"}); AgentErrorText(plain) != plain.Error() {
		t.Fatalf("a text without the phrases changed: %q", AgentErrorText(plain))
	}
}
