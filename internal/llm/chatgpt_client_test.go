// Clean-room: docs/provenance.md#CR-SUBSCRIPTIONS
// Written from OpenAI's public Sign in with ChatGPT documentation and the
// research spec (section 1a) alone.

package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// planRejected is the list of request fields the plan-usage route rejects
// (OpenAI, "Preview limitations"), plus the fields of other wires.
var planRejected = []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation", "multi_agent",
	"prompt", "prompt_cache_retention", "safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user",
	"previous_response_id", "max_tokens", "max_completion_tokens", "messages", "stream_options", "reasoning_effort"}

type seen struct {
	method, path, auth, contentType string
	session                         string
	body                            map[string]any
}

// recorder answers the plan API and remembers what it was sent.
type recorder struct {
	mu   sync.Mutex
	reqs []seen
	next func(w http.ResponseWriter, r *http.Request, n int)
}

func (rc *recorder) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	rc.mu.Lock()
	rc.reqs = append(rc.reqs, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("session-id"), body})
	n := len(rc.reqs)
	rc.mu.Unlock()
	rc.next(w, r, n)
}

func (rc *recorder) last() seen {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.reqs[len(rc.reqs)-1]
}

func (rc *recorder) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.reqs)
}

func crEvents(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range events {
		fmt.Fprintf(w, "data: %s\n\n", ev)
		w.(http.Flusher).Flush()
	}
}

func crDelta(s string) string {
	b, _ := json.Marshal(s)
	return `{"type":"response.output_text.delta","delta":` + string(b) + `}`
}

func crCompleted(in, cached, out, reasoning int) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":%d,"input_tokens_details":{"cached_tokens":%d},"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":%d},"total_tokens":%d}}}`,
		in, cached, out, reasoning, in+out)
}

func crOK(text string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		crEvents(w, `{"type":"response.created"}`, crDelta(text), crCompleted(10, 0, 5, 0))
	}
}

func crFail(status int, header map[string]string, body string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

// chatgptReady is a signed-in router whose narrative task goes to the plan.
func chatgptReady(t *testing.T, next func(http.ResponseWriter, *http.Request, int)) (*env, *fakeOpenAI, *recorder) {
	t.Helper()
	e, f := chatgptEnv(t)
	e.signIn(f)
	rc := &recorder{next: next}
	f.api = rc.serve
	e.route("narrative", Route{Provider: "chatgpt", Model: "gpt-6.1-sol"})
	return e, f, rc
}

func TestLLM_ChatGPT_RequestCarriesOnlyWhatPlanUsageAccepts(t *testing.T) {
	cases := []struct {
		name   string
		task   string
		route  Route
		effort string // "" when no reasoning field is expected
	}{
		{"thinking medium", "deep-eval", Route{Provider: "chatgpt", Model: "gpt-6.1-sol", Thinking: ThinkMedium}, "medium"},
		{"a model that cannot turn reasoning off", "quick-screen", Route{Provider: "chatgpt", Model: "gpt-6-luna", Thinking: ThinkOff}, "low"},
		{"thinking off on a deterministic task", "deep-eval", Route{Provider: "chatgpt", Model: "other-model", Thinking: ThinkOff}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _, rc := chatgptReady(t, crOK("ok"))
			e.route(tc.task, tc.route)
			if _, err := e.r.Complete(bg(), Request{Task: tc.task, System: "be brief", Prompt: "the question", MaxOutputTokens: 5000}); err != nil {
				t.Fatal(err)
			}
			got := rc.last()
			token, _ := e.vault.Get(vaultChatGPTAccess)
			if got.method != "POST" || got.path != "/v1/responses" || got.auth != "Bearer "+token || got.contentType != "application/json" {
				t.Errorf("request = %+v", got)
			}
			b := got.body
			if b["store"] != false || b["stream"] != true || b["model"] != tc.route.Model || b["instructions"] != "be brief" {
				t.Errorf("body = %v", b)
			}
			input, _ := b["input"].([]any)
			if len(input) != 1 {
				t.Fatalf("input = %v", b["input"])
			}
			if item := input[0].(map[string]any); item["role"] != "user" || item["content"] != "the question" {
				t.Errorf("input item = %v (system text goes in instructions; a system item is rejected)", item)
			}
			for _, k := range planRejected {
				if _, ok := b[k]; ok {
					t.Errorf("the request carries %q, which the plan route rejects", k)
				}
			}
			reasoning, has := b["reasoning"].(map[string]any)
			if (tc.effort != "") != has || (has && reasoning["effort"] != tc.effort) {
				t.Errorf("reasoning = %v, want effort %q", b["reasoning"], tc.effort)
			}
		})
	}
}

func TestLLM_ChatGPT_StreamedReplyUsageAndBilling(t *testing.T) {
	e, _, rc := chatgptReady(t, func(w http.ResponseWriter, r *http.Request, n int) {
		crEvents(w, `{"type":"response.created"}`, `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
			crDelta("Hel"), crDelta("lo"), crCompleted(100, 40, 30, 10))
	})
	var deltas []string
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", OnDelta: func(s string) { deltas = append(deltas, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello" || !slices.Equal(deltas, []string{"Hel", "lo"}) || resp.FinishReason != "stop" {
		t.Errorf("text %q deltas %v finish %s", resp.Text, deltas, resp.FinishReason)
	}
	if want := (Usage{Input: 100, CachedInput: 40, Output: 30, Reasoning: 10}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
	if resp.Billing != BillingSubscription || resp.CostUSD == nil || *resp.CostUSD != 0 {
		t.Errorf("billing %s cost %v", resp.Billing, resp.CostUSD)
	}
	rows := e.ledger()
	if len(rows) != 1 || rows[0].TTFT == nil || rows[0].Input != 100 || rows[0].Cached != 40 || rows[0].Status != 200 || rows[0].Billing != BillingSubscription {
		t.Errorf("ledger = %+v", rows)
	}
	if rc.count() != 1 {
		t.Errorf("%d requests", rc.count())
	}
}

func TestLLM_ChatGPT_JSONTasksUseTheStrictSchemaFormat(t *testing.T) {
	e, _, rc := chatgptReady(t, crOK(`{"name":"Ada","age":36,"tags":null}`))
	var p person
	if _, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &p); err != nil {
		t.Fatal(err)
	}
	if p != (person{"Ada", 36}) {
		t.Errorf("p = %+v", p)
	}
	text, _ := rc.last().body["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	schema, _ := format["schema"].(map[string]any)
	req, _ := schema["required"].([]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] == "" || schema["additionalProperties"] != false || len(req) != 3 {
		t.Errorf("text.format = %v", text)
	}
}

func TestLLM_ChatGPT_FailuresMapToTheRoutersKinds(t *testing.T) {
	code := func(c string) string { return fmt.Sprintf(`{"error":{"code":%q,"message":"m","param":"p1"}}`, c) }
	cases := []struct {
		name   string
		next   func(http.ResponseWriter, *http.Request, int)
		kind   Kind
		status int
		retry  int
		msg    string
	}{
		{"usage limit", crFail(429, nil, code("subscription_sharing_usage_limit_exceeded")), KindQuota, 429, 0, "ChatGPT settings, Usage"},
		{"usage limit with a Retry-After", crFail(429, map[string]string{"Retry-After": "120"}, code("subscription_sharing_usage_limit_exceeded")), KindQuota, 429, 120, ""},
		{"usage unavailable", crFail(503, nil, code("subscription_sharing_usage_unavailable")), KindOverloaded, 503, 0, ""},
		{"user unavailable", crFail(503, nil, code("subscription_sharing_user_unavailable")), KindOverloaded, 503, 0, ""},
		{"not eligible", crFail(403, nil, code("subscription_sharing_user_not_eligible")), KindAuth, 403, 0, "cannot be used by apps"},
		{"unsupported capability", crFail(400, nil, code("subscription_sharing_unsupported_capability")), KindBadRequest, 400, 0, "param p1"},
		{"route not supported", crFail(403, nil, code("subscription_sharing_route_not_supported")), KindAuth, 403, 0, ""},
		{"scope not authorized", crFail(403, nil, code("chatpass_v2_scope_not_authorized")), KindAuth, 403, 0, ""},
		{"invalid authorization context", crFail(403, nil, code("chatpass_v2_invalid_authorization_context")), KindAuth, 403, 0, ""},
		{"invalid user", crFail(401, nil, code("subscription_sharing_invalid_user")), KindAuth, 401, 0, "sign in again"},
		{"bare detail 401", crFail(401, nil, `{"detail":"signed identity not accepted"}`), KindAuth, 401, 0, "signed identity"},
		{"bare detail 403", crFail(403, nil, `{"detail":"region not permitted"}`), KindAuth, 403, 0, "region"},
		{"bare detail 503", crFail(503, nil, `{"detail":"direct routing is unavailable"}`), KindOverloaded, 503, 0, "direct routing"},
		{"plain rate limit with a long Retry-After", crFail(429, map[string]string{"Retry-After": "90"}, code("rate_limit_exceeded")), KindRateLimited, 429, 90, ""},
		{"model gone", crFail(404, nil, code("model_not_found")), KindModelGone, 404, 0, ""},
		{"usage limit inside the stream", func(w http.ResponseWriter, r *http.Request, n int) {
			crEvents(w, `{"type":"response.created"}`, `{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}}`)
		}, KindQuota, 429, 0, "ChatGPT settings, Usage"},
		{"usage unavailable inside the stream", func(w http.ResponseWriter, r *http.Request, n int) {
			crEvents(w, `{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_unavailable"}}}`)
		}, KindOverloaded, 503, 0, ""},
		{"error event", func(w http.ResponseWriter, r *http.Request, n int) {
			crEvents(w, `{"type":"error","code":"subscription_sharing_unsupported_capability","message":"m","param":"tools"}`)
		}, KindBadRequest, 400, 0, "param tools"},
		{"stream cut before completion", func(w http.ResponseWriter, r *http.Request, n int) {
			crEvents(w, crDelta("partial"))
		}, KindNetwork, 0, 0, "before response.completed"},
		{"incomplete at the output limit", func(w http.ResponseWriter, r *http.Request, n int) {
			crEvents(w, crDelta("cut"), `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`)
		}, KindTruncated, 0, 0, ""},
		{"incomplete for another reason", func(w http.ResponseWriter, r *http.Request, n int) {
			crEvents(w, crDelta("cut"), `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`)
		}, KindOther, 0, 0, "content_filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _ := chatgptReady(t, tc.next)
			token, _ := e.vault.Get(vaultChatGPTAccess)
			_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
			got := wantKind(t, err, tc.kind)
			if got.Status != tc.status && tc.status != 0 || got.retryAfter != tc.retry || !strings.Contains(got.Message, tc.msg) {
				t.Errorf("status %d retryAfter %d message %q", got.Status, got.retryAfter, got.Message)
			}
			if token != "" && strings.Contains(got.Error(), token) {
				t.Error("the access token reached the error text")
			}
		})
	}
}

func TestLLM_ChatGPT_UsageLimitRestsThePlanAndFallsBack(t *testing.T) {
	e, _, rc := chatgptReady(t, crFail(429, nil, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`))
	b := newOAServer(t, okHandler("from b"))
	e.provider(oaProvider("b", b), fakeKey("fb"))
	e.route("narrative", Route{Provider: "chatgpt", Model: "gpt-6.1-sol", Fallbacks: []Fallback{{"b", "m"}}})
	for i := range 2 {
		if resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || resp.Text != "from b" {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// The second call does not try the resting plan first.
	if got := outcomes(e.ledger()); got != "chatgpt:quota-exhausted,b:ok,b:ok" || rc.count() != 1 {
		t.Errorf("ledger = %s, %d plan requests", got, rc.count())
	}
	rests := e.r.Rests()
	if len(rests) != 1 || rests[0].Provider != "chatgpt" || rests[0].Why != RestQuota || rests[0].Model != "" {
		t.Errorf("rests = %+v", rests)
	}
}

func TestLLM_ChatGPT_ARefusedTokenIsDroppedAndRenewed(t *testing.T) {
	e, f, rc := chatgptReady(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			crFail(401, nil, `{"detail":"signed identity not accepted"}`)(w, r, n)
			return
		}
		crOK("ok")(w, r, n)
	})
	old, _ := e.vault.Get(vaultChatGPTAccess)
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); KindOf(err) != KindAuth {
		t.Fatalf("err = %v", err)
	}
	if e.hasToken(vaultChatGPTAccess) {
		t.Error("the refused access token stays in the vault")
	}
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := e.vault.Get(vaultChatGPTAccess); fresh == old || rc.last().auth != "Bearer "+fresh || len(f.tokenCalls("refresh_token")) != 1 {
		t.Error("the next call did not renew the token")
	}
}

func TestLLM_ChatGPT_SubscriptionNeverCountsOrDowngrades(t *testing.T) {
	t.Run("calls are free and the cap does not move them", func(t *testing.T) {
		e, _, _ := chatgptReady(t, crOK("ok"))
		addSpend(t, e, 2.05) // the day's metered spend is over the cap
		resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		if err != nil || resp.Downgraded || resp.Model != "gpt-6.1-sol" {
			t.Fatalf("%v %+v", err, resp)
		}
		e.r.spend(bg(), true)
		if total, _ := e.r.DailySpend(bg()); total != 2.05 {
			t.Errorf("spend = %v: a plan call must not count toward the cap", total)
		}
	})
	t.Run("a priced plan model is still not a downgrade target", func(t *testing.T) {
		e, _, _ := chatgptReady(t, crOK("ok"))
		oa := ProviderConfig{ID: "a", Kind: KindOpenAI, BaseURL: "http://127.0.0.1:1", Preset: "openai"}
		e.provider(oa, fakeKey("cp"))
		if err := e.r.SetPrice(bg(), "chatgpt", "cheap", Price{0.01, 0.01, 0.01}); err != nil { // a user price wins over every source
			t.Fatal(err)
		}
		cands := []candidate{{provider: oa, model: "gpt-6.1-sol"}, {provider: ProviderConfig{ID: "chatgpt", Kind: KindChatGPT, Preset: "chatgpt"}, model: "cheap"}}
		out := e.r.downgradeOrder(bg(), e.load(), cands)
		for _, c := range out {
			if c.provider.Kind == KindChatGPT && c.downgraded {
				t.Fatalf("the plan was chosen as the cheaper head: %+v", out)
			}
		}
		if c := e.r.costing(bg(), e.load(), cands[1].provider, "cheap", Usage{Input: 1000, Output: 1000}); c.Billing != BillingSubscription || *c.Cost != 0 {
			t.Errorf("costing = %+v: a plan call is never metered", c)
		}
	})
	t.Run("a plan primary keeps the configured order", func(t *testing.T) {
		e, _, _ := chatgptReady(t, crOK("ok"))
		oa := ProviderConfig{ID: "a", Kind: KindOpenAI, BaseURL: "http://127.0.0.1:1", Preset: "openai"}
		cands := []candidate{{provider: ProviderConfig{ID: "chatgpt", Kind: KindChatGPT, Preset: "chatgpt"}, model: "gpt-6.1-sol"}, {provider: oa, model: "gpt-6-luna"}}
		if out := e.r.downgradeOrder(bg(), e.load(), cands); out[0].provider.ID != "chatgpt" || out[0].downgraded {
			t.Fatalf("order = %+v", out)
		}
	})
}

func TestLLM_ChatGPT_ModelListIsTheAccountsAndCached(t *testing.T) {
	list := `{"models":[{"slug":"m-b","display_name":"B","visibility":"list"},{"slug":"hidden","display_name":"H","visibility":"hide"},{"slug":"m-a","display_name":"A","visibility":"list"}]}`
	e, _, rc := chatgptReady(t, func(w http.ResponseWriter, r *http.Request, n int) { fmt.Fprint(w, list) })
	got, err := e.r.ChatGPTModels(bg())
	if err != nil || len(got) != 2 || got[0] != (ChatGPTModel{"m-b", "B"}) || got[1].Slug != "m-a" {
		t.Fatalf("models = %+v err %v (visibility list only, the server's order)", got, err)
	}
	token, _ := e.vault.Get(vaultChatGPTAccess)
	if last := rc.last(); last.method != "GET" || last.path != "/v1/models" || last.auth != "Bearer "+token {
		t.Errorf("request = %+v", last)
	}
	if _, err := e.r.ChatGPTModels(bg()); err != nil || rc.count() != 1 {
		t.Fatalf("a second read within the cache window asked again (%d requests)", rc.count())
	}
	e.clock.advance(chatgptModelsTTL + time.Minute)
	if _, err := e.r.ChatGPTModels(bg()); err != nil || rc.count() != 2 {
		t.Fatalf("an old list was not refreshed (%d requests)", rc.count())
	}
	if _, err := e.r.SignOutChatGPT(bg(), false); err != nil {
		t.Fatal(err)
	}
	wantKind(t, func() error { _, err := e.r.ChatGPTModels(bg()); return err }(), KindUnconfigured)
}

func TestLLM_ChatGPT_PresetNeedsASignIn(t *testing.T) {
	e, f := chatgptEnv(t)
	err := e.r.ConfigurePreset(bg(), "chatgpt", ConfigureOptions{})
	if se, ok := err.(*SetupError); !ok || !contains(se.Message, "sign in") {
		t.Fatalf("err = %v", err)
	}
	e.signIn(f)
	if err := e.r.ConfigurePreset(bg(), "chatgpt", ConfigureOptions{}); err != nil {
		t.Fatal(err)
	}
	tab := e.show()
	if tab.Unrouted["embedding"] == "" || tab.Unrouted[AgentTask] != "" {
		t.Errorf("unrouted = %v", tab.Unrouted)
	}
	if res := e.resolve(AgentTask); res.Provider != "chatgpt" {
		t.Errorf("agent-kernel = %+v", res)
	}
	if res := e.resolve("narrative"); res.Provider != "chatgpt" || res.Model != "gpt-6.1-sol" {
		t.Errorf("narrative = %+v", res)
	}
}

// The plan route takes no output ceiling, so a reply that runs on (a
// deep-eval streamed about 178k tokens until its 600 s timeout) is left once
// its own text passes twice the row's ceiling, as a runaway, and the walk
// advances to the next candidate. A reply a little past the ceiling is kept.
func TestLLM_ChatGPT_AReplyPastTheCeilingIsCutThere(t *testing.T) {
	const chunk, most = 400, 500
	e, _, _ := chatgptReady(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < most && r.Context().Err() == nil; i++ {
			fmt.Fprintf(w, "data: %s\n\n", crDelta(strings.Repeat("x", chunk)))
			w.(http.Flusher).Flush()
		}
	})
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	wantKind(t, err, KindRunaway)
	rows := e.ledger()
	if len(rows) != 1 || rows[0].Outcome != "runaway" || rows[0].Output <= 2*2048 || rows[0].Output > 2*2048+chunk/4 {
		t.Errorf("ledger = %+v", rows)
	}
	if !advances[KindRunaway] {
		t.Error("a runaway does not advance the walk")
	}
	e, _, _ = chatgptReady(t, crOK(strings.Repeat("y", 4*2100)))
	if resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || len(resp.Text) != 4*2100 {
		t.Fatalf("a reply a little past the ceiling: %v", err)
	}
}
