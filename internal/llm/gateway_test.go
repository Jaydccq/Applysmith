package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// gwEnv routes the agent to DeepSeek "a" with OpenRouter "b" behind it, the
// shape of a real agent row.
func gwEnv(t *testing.T, primaryModel string, a, b func(http.ResponseWriter, *http.Request, int)) (*env, *oaServer, *oaServer) {
	t.Helper()
	e := newEnv(t)
	sa, sb := newOAServer(t, a), newOAServer(t, b)
	pa, pb := oaProvider("a", sa), oaProvider("b", sb)
	pa.Preset, pb.Preset = "deepseek", "openrouter"
	e.provider(pa, fakeKey("ga"))
	e.provider(pb, fakeKey("gb"))
	e.route(AgentTask, Route{Provider: "a", Model: primaryModel, Thinking: ThinkMedium, MaxOutputTokens: 16000,
		Fallbacks: []Fallback{{"b", "openai/gpt-6-luna"}}})
	return e, sa, sb
}

// piRequest is a request as pi builds it for a DeepSeek model: a developer
// message, a tool call with its reasoning, the tool result, and the ceiling in
// the field OpenAI names.
func piRequest(extra ...map[string]any) map[string]any {
	msgs := []any{
		map[string]any{"role": "developer", "content": "You are the agent."},
		map[string]any{"role": "user", "content": "How many applications?"},
		map[string]any{"role": "assistant", "content": "", "reasoning_content": "Count them.",
			"tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "tracker_query", "arguments": "{}"}}}},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "12"},
	}
	for _, m := range extra {
		msgs = append(msgs, m)
	}
	return map[string]any{"model": "deepseek-flash", "stream": true, "max_completion_tokens": 16000,
		"reasoning_effort": "medium", "store": false, "messages": msgs}
}

func (e *env) agentCall(session, turn string, body map[string]any) (*httptest.ResponseRecorder, error) {
	e.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	return w, e.r.ServeAgentCall(bg(), AgentCall{Wire: WireOpenAI, Body: raw, SessionID: session, TurnID: turn, Task: AgentTask}, w)
}

// attempts lists provider:reason:run per ledger row.
func (e *env) attempts() string {
	e.t.Helper()
	rows, err := e.st.Read().Query(`SELECT provider_id, reason, run_id FROM llm_ledger ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p, r, run string
		if err := rows.Scan(&p, &r, &run); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, p+":"+r+":"+run)
	}
	return strings.Join(out, ",")
}

func TestLLM_Gateway_MovesOnWhileNothingReachedPi(t *testing.T) {
	for name, primary := range map[string]func(http.ResponseWriter, *http.Request, int){
		"429": statusHandler(429, `{"error":{"message":"slow down"}}`),
		"an error event first": func(w http.ResponseWriter, r *http.Request, n int) {
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"server overloaded\"}}\n\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, _, _ := gwEnv(t, "deepseek-flash", primary, okHandler("from b"))
			w, err := e.agentCall("ses_1", "t1", piRequest())
			if err != nil {
				t.Fatal(err)
			}
			got := w.Body.String()
			if !strings.Contains(got, `"from b"`) || !strings.HasSuffix(got, "data: [DONE]\n\n") || strings.Contains(got, "overloaded") {
				t.Fatalf("pi got %q", got)
			}
			if r := e.attempts(); r != "a::ses_1,b:fallback:ses_1" {
				t.Fatalf("ledger = %s (%s)", r, outcomes(e.ledger()))
			}
		})
	}
}

func TestLLM_Gateway_AFailureAfterTheFirstEventIsPis(t *testing.T) {
	broken := func(w http.ResponseWriter, r *http.Request, n int) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}
	e, _, sb := gwEnv(t, "deepseek-flash", broken, okHandler("from b"))
	w, err := e.agentCall("ses_1", "t1", piRequest())
	if err != nil {
		t.Fatalf("a relayed reply returns nil, got %v", err)
	}
	if !strings.Contains(w.Body.String(), "partial") || sb.count() != 0 {
		t.Fatalf("pi got %q; fallback calls %d", w.Body.String(), sb.count())
	}
	if o := outcomes(e.ledger()); o != "a:network" {
		t.Fatalf("ledger = %s", o)
	}
}

func TestLLM_Gateway_EachEndpointGetsItsOwnFields(t *testing.T) {
	e, sa, sb := gwEnv(t, "deepseek-flash", statusHandler(503, `{}`), okHandler("ok"))
	if _, err := e.agentCall("ses_1", "t1", piRequest()); err != nil {
		t.Fatal(err)
	}
	a, b := sa.last, sb.last
	msgsA, msgsB := a["messages"].([]any), b["messages"].([]any)
	if a["model"] != "deepseek-flash" || a["max_tokens"] != 16000.0 || a["max_completion_tokens"] != nil || a["reasoning_effort"] != "medium" || a["store"] != nil {
		t.Fatalf("deepseek request = %v", a)
	}
	if msgsA[0].(map[string]any)["role"] != "system" || msgsA[2].(map[string]any)["reasoning_content"] != "Count them." {
		t.Fatalf("deepseek history = %v", msgsA)
	}
	if b["model"] != "openai/gpt-6-luna" || b["max_tokens"] != 16000.0 || b["reasoning_effort"] != nil || fmt.Sprint(b["reasoning"]) != "map[effort:medium]" {
		t.Fatalf("openrouter request = %v", b)
	}
	if _, kept := msgsB[2].(map[string]any)["reasoning_content"]; kept || msgsB[0].(map[string]any)["role"] != "system" {
		t.Fatalf("openrouter history = %v", msgsB)
	}
	if fmt.Sprint(b["stream_options"]) != "map[include_usage:true]" {
		t.Fatalf("usage not asked for: %v", b)
	}
}

func TestLLM_Gateway_AnImageSkipsTextOnlyModels(t *testing.T) {
	e, sa, _ := gwEnv(t, "deepseek-flash", okHandler("from a"), okHandler("from b"))
	shot := map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "the window"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
	}}
	w, err := e.agentCall("ses_1", "t1", piRequest(shot))
	if err != nil {
		t.Fatal(err)
	}
	if sa.count() != 0 || !strings.Contains(w.Body.String(), "from b") {
		t.Fatalf("deepseek-flash takes no images: a called %d times, pi got %q", sa.count(), w.Body.String())
	}
}

func TestLLM_Gateway_OverTheCap(t *testing.T) {
	t.Run("a cheaper candidate takes the call", func(t *testing.T) {
		e, sa, sb := gwEnv(t, "deepseek-v4-pro", okHandler("from a"), okHandler("from b"))
		addSpend(t, e, 5)
		if e.r.AgentCapBlocked(bg(), AgentTask) {
			t.Fatal("a cheaper candidate exists, the agent is not blocked")
		}
		if _, err := e.agentCall("ses_1", "t1", piRequest()); err != nil {
			t.Fatal(err)
		}
		if sb.count() != 1 || sa.count() != 0 || !strings.Contains(e.attempts(), "b:cap-downgrade") {
			t.Fatalf("a %d, b %d, ledger %s", sa.count(), sb.count(), e.attempts())
		}
	})
	t.Run("nothing cheaper stops the agent", func(t *testing.T) {
		e, sa, _ := gwEnv(t, "deepseek-flash", okHandler("from a"), okHandler("from b"))
		e.route(AgentTask, Route{Provider: "a", Model: "deepseek-flash", Thinking: ThinkMedium})
		if e.r.AgentCapBlocked(bg(), AgentTask) {
			t.Fatal("blocked under the cap")
		}
		addSpend(t, e, 5)
		e.clock.advance(capRefresh + time.Second) // the spend read before it is reused for capRefresh
		if !e.r.AgentCapBlocked(bg(), AgentTask) {
			t.Fatal("over the cap with nothing cheaper, the agent is not blocked")
		}
		_, err := e.agentCall("ses_1", "t1", piRequest())
		wantKind(t, err, KindCostCap)
		if sa.count() != 0 {
			t.Fatal("a refused call reached the provider")
		}
	})
}

func TestLLM_Gateway_AConversationStaysWhereItsCacheIs(t *testing.T) {
	var mu sync.Mutex
	failA := true
	a := func(w http.ResponseWriter, r *http.Request, n int) {
		mu.Lock()
		fail := failA
		failA = false
		mu.Unlock()
		if fail {
			oaFail(w, 503, `{}`)
			return
		}
		oaStream(w, "stop", 3000, 5, 2900, "from a")
	}
	b := func(w http.ResponseWriter, r *http.Request, n int) { oaStream(w, "stop", 3000, 5, 2000, "from b") }
	e, _, _ := gwEnv(t, "deepseek-flash", a, b)
	served := func(session, turn string) string {
		t.Helper()
		w, err := e.agentCall(session, turn, piRequest())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(w.Body.String(), "from a") {
			return "a"
		}
		return "b"
	}
	if got := served("ses_1", "t1"); got != "b" {
		t.Fatalf("a failed: %s served", got)
	}
	e.clock.advance(2 * time.Minute) // a's rest is over
	if got := served("ses_1", "t1"); got != "b" {
		t.Fatalf("within the turn the conversation stays on b, %s served", got)
	}
	if got := served("ses_2", "t9"); got != "a" {
		t.Fatalf("another conversation goes to the primary, %s served", got)
	}
	e.clock.advance(3 * time.Minute)
	if got := served("ses_1", "t2"); got != "b" {
		t.Fatalf("a new turn with b's cache warm stays on b, %s served", got)
	}
	e.clock.advance(6 * time.Minute)
	if got := served("ses_1", "t3"); got != "a" {
		t.Fatalf("a new turn after the cache went cold goes to the primary, %s served", got)
	}
	if r := e.attempts(); r != "a::ses_1,b:fallback:ses_1,b:affinity:ses_1,a::ses_2,b:affinity:ses_1,a::ses_1" {
		t.Fatalf("ledger = %s", r)
	}
}

func TestLLM_Gateway_RelaysAnthropicMessages(t *testing.T) {
	key := fakeKey("an")
	var mu sync.Mutex
	var hdr http.Header
	var model any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hdr, model = r.Header.Clone(), body["model"]
		mu.Unlock()
		anStream(w, "tool_use", 40, 12, 900, "hi")
	}))
	t.Cleanup(s.Close)
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "an", Kind: KindAnthropic, BaseURL: s.URL, Preset: "anthropic"}, key)
	e.route(AgentTask, Route{Provider: "an", Model: "claude-sonnet-5-5", Thinking: ThinkMedium})
	raw := []byte(`{"model":"claude-sonnet-5-5","stream":true,"max_tokens":16000,"messages":[{"role":"user","content":"hi"}]}`)
	w := httptest.NewRecorder()
	if err := e.r.ServeAgentCall(bg(), AgentCall{Wire: WireAnthropic, Body: raw, SessionID: "ses_1", TurnID: "t1", Task: AgentTask, Beta: "fine-grained-tool-streaming-2025-05-14"}, w); err != nil {
		t.Fatal(err)
	}
	if hdr.Get("x-api-key") != key || hdr.Get("anthropic-beta") != "fine-grained-tool-streaming-2025-05-14" || model != "claude-sonnet-5-5" {
		t.Fatalf("request headers %v, model %v", hdr, model)
	}
	if got := w.Body.String(); !strings.HasPrefix(got, "event: message_start\n") || !strings.Contains(got, "message_stop") {
		t.Fatalf("pi got %q", got)
	}
	rows := e.ledger()
	if len(rows) != 1 || rows[0].Outcome != "ok" || rows[0].Input != 940 || rows[0].Cached != 900 || rows[0].Output != 12 || rows[0].Finish != "tool_use" {
		t.Fatalf("ledger = %+v", rows)
	}
}

// The call's task picks the row: a light turn is served by agent-light's
// candidates, ledgered under it and capped by its own primary, and moving
// between rows never keeps a candidate the new row does not list.
func TestLLM_Gateway_TheTurnsRowServesItsCalls(t *testing.T) {
	e, sa, sb := gwEnv(t, "deepseek-v4-pro", okHandler("from heavy"), okHandler("from light"))
	e.route(AgentLightTask, Route{Provider: "b", Model: "openai/gpt-6-luna", Thinking: ThinkLow})
	call := func(session, turn, task string) (*httptest.ResponseRecorder, error) {
		raw, _ := json.Marshal(piRequest())
		w := httptest.NewRecorder()
		return w, e.r.ServeAgentCall(bg(), AgentCall{Wire: WireOpenAI, Body: raw, SessionID: session, TurnID: turn, Task: task}, w)
	}
	for i, task := range []string{AgentTask, AgentLightTask, AgentTask} {
		if _, err := call("ses_1", fmt.Sprint("t", i), task); err != nil {
			t.Fatalf("%s: %v", task, err)
		}
	}
	var tasks []string
	for _, r := range e.ledger() {
		tasks = append(tasks, r.Task+":"+r.Provider+":"+r.Source)
	}
	// The heavy answer is not kept for the light turn (its candidate is not on
	// the light row) and the light answer, which cached nothing, is not kept
	// for the next heavy turn.
	if got := strings.Join(tasks, " "); got != "agent-kernel:a:primary agent-light:b:primary agent-kernel:a:primary" || sa.count() != 2 || sb.count() != 1 {
		t.Fatalf("ledger = %s; heavy provider %d calls, light provider %d", got, sa.count(), sb.count())
	}
	for _, task := range []string{"", "deep-eval"} {
		_, err := call("ses_2", "t1", task)
		wantKind(t, err, KindBadRequest)
	}

	addSpend(t, e, 5)
	e.clock.advance(capRefresh + time.Second)
	if e.r.AgentCapBlocked(bg(), AgentTask) || !e.r.AgentCapBlocked(bg(), AgentLightTask) {
		t.Fatal("over the cap the heavy row has a cheaper candidate and the light row has none")
	}
}

// piStopsAtTheFinish is pi reading a reply up to its finish reason and then
// closing the connection, as it does at times before the stream's tail.
type piStopsAtTheFinish struct {
	*httptest.ResponseRecorder
	stop   context.CancelFunc
	closed bool
}

func (p *piStopsAtTheFinish) WriteString(s string) (int, error) { return p.Write([]byte(s)) }

func (p *piStopsAtTheFinish) Write(b []byte) (int, error) {
	if p.closed {
		return 0, errors.New("broken pipe")
	}
	n, err := p.ResponseRecorder.Write(b)
	if strings.Contains(string(b), `"finish_reason"`) {
		p.closed = true
		p.stop()
	}
	return n, err
}

// A reply pi read to its finish reason is the call's answer even when pi
// closes before the usage and the terminator: live, about one call in thirty
// on OpenRouter was recorded aborted and its answerer was not kept.
func TestLLM_Gateway_PiClosingAfterTheFinishIsNoAbort(t *testing.T) {
	tail := func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"from a\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		f.Flush()
		select { // the usage comes late, after pi has gone
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3000,\"completion_tokens\":5}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}
	e, _, _ := gwEnv(t, "deepseek-flash", tail, okHandler("from b"))
	raw, _ := json.Marshal(piRequest())
	ctx, cancel := context.WithCancel(bg())
	defer cancel()
	w := &piStopsAtTheFinish{ResponseRecorder: httptest.NewRecorder(), stop: cancel}
	if err := e.r.ServeAgentCall(ctx, AgentCall{Wire: WireOpenAI, Body: raw, SessionID: "ses_1", TurnID: "t1", Task: AgentTask}, w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "from a") {
		t.Fatalf("pi got %q", w.Body.String())
	}
	rows := e.ledger()
	if len(rows) != 1 || rows[0].Outcome != "ok" || rows[0].Finish != "tool_calls" {
		t.Fatalf("ledger = %+v", rows)
	}
	if st, ok := e.r.sticks["ses_1"]; !ok || !strings.HasPrefix(st.cand, "a/") {
		t.Fatalf("the answerer was not kept: %+v", e.r.sticks)
	}
}
