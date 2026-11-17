package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestLLM_OpenAI_StreamsAndAssemblesTheReply(t *testing.T) {
	e := newEnv(t)
	key := fakeKey("oa")
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		oaStream(w, "stop", 120, 30, 100, "Hello, ", "world")
	})
	e.provider(oaProvider("a", s), key)
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
	var deltas []string
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", System: "be brief", Prompt: "hi", OnDelta: func(d string) { deltas = append(deltas, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello, world" || strings.Join(deltas, "|") != "Hello, |world" {
		t.Fatalf("text %q deltas %v", resp.Text, deltas)
	}
	if s.auth != "Bearer "+key {
		t.Fatalf("authorization = %q", s.auth)
	}
	msgs := s.last["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || s.last["stream"] != true {
		t.Fatalf("request = %v", s.last)
	}
	if resp.Usage != (Usage{Input: 120, Output: 30, CachedInput: 100}) {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	rows := e.ledger()
	if len(rows) != 1 || rows[0].TTFT == nil || rows[0].Input != 120 || rows[0].Cached != 100 {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_EffectiveCeiling_LiftedUnlessThinkingOff(t *testing.T) {
	cases := []struct {
		name     string
		thinking string
		asked    int
		want     float64
	}{
		{"small ceiling with thinking on", ThinkLow, 400, 4096},
		{"small ceiling with thinking off", ThinkOff, 400, 400},
		{"larger caller ceiling is kept", ThinkLow, 9000, 9000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			s := newOAServer(t, okHandler("ok"))
			e.provider(oaProvider("a", s), fakeKey("oa"))
			e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: c.thinking, MaxOutputTokens: 4096})
			if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", MaxOutputTokens: c.asked}); err != nil {
				t.Fatal(err)
			}
			if got := s.last["max_tokens"]; got != c.want {
				t.Fatalf("max_tokens = %v, want %v", got, c.want)
			}
		})
	}
	t.Run("ceiling never below 2048 with thinking on", func(t *testing.T) {
		res := Resolved{Thinking: ThinkLow, MaxOutputTokens: 0}
		if m, _ := shape(res, Request{MaxOutputTokens: 100}); m != 2048 {
			t.Fatalf("ceiling = %d", m)
		}
	})
}

func TestLLM_OpenRouter_ReasoningMandatoryModelGetsLowestLevel(t *testing.T) {
	cases := []struct {
		name, preset, model, thinking string
		field                         string
		want                          any
	}{
		{"openai mandatory model, thinking off", "openai", "gpt-6-luna", ThinkOff, "reasoning_effort", "low"},
		{"openrouter mandatory model, thinking off", "openrouter", "openai/gpt-6-luna", ThinkOff, "reasoning", map[string]any{"effort": "low"}},
		{"optional-reasoning model, thinking off", "openai", "gpt-4.1", ThinkOff, "", nil},
		{"thinking low is passed through", "openai", "gpt-6.1-sol", ThinkLow, "reasoning_effort", "low"},
		{"highest level maps to the endpoint's maximum", "openai", "gpt-6.1-sol", ThinkXHigh, "reasoning_effort", "max"},
		{"gemini lowest level", "gemini", "gemini-3.8-flash", ThinkOff, "reasoning_effort", "low"},
		{"deepseek passes the level through", "deepseek", "deepseek-v4-pro", ThinkHigh, "reasoning_effort", "high"},
		{"deepseek thinking off is the disable value", "deepseek", "deepseek-flash", ThinkOff, "reasoning_effort", "none"},
		{"openrouter optional-reasoning model, thinking off", "openrouter", "deepseek/deepseek-flash", ThinkOff, "reasoning", map[string]any{"effort": "none"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			s := newOAServer(t, okHandler("ok"))
			pr, _ := PresetByName(c.preset)
			p := oaProvider("a", s)
			p.Preset = pr.Name
			e.provider(p, fakeKey("oa"))
			e.route("narrative", Route{Provider: "a", Model: c.model, Thinking: c.thinking})
			if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"reasoning_effort", "reasoning"} {
				got, has := s.last[f]
				if f != c.field {
					if has {
						t.Errorf("unexpected %s = %v", f, got)
					}
					continue
				}
				if c.field != "" && fmt.Sprint(got) != fmt.Sprint(c.want) {
					t.Errorf("%s = %v, want %v", f, got, c.want)
				}
			}
			if pr.mandatoryReasoning(c.model) && strings.Contains(fmt.Sprint(s.last["reasoning_effort"], s.last["reasoning"]), "none") {
				t.Error("a disable value was sent to a reasoning-mandatory model")
			}
		})
	}
}

func TestLLM_OpenAI_DeterministicTasksSendTemperatureZero(t *testing.T) {
	cases := []struct {
		name, preset, model, task, thinking string
		want                                bool
	}{
		{"evaluation task, reasoning off", "deepseek", "deepseek-flash", "jd-extract", ThinkOff, true},
		{"evaluation task, reasoning on: the field is ignored there", "deepseek", "deepseek-v4-pro", "deep-eval", ThinkMedium, false},
		{"prose task keeps the provider default", "deepseek", "deepseek-flash", "cover-letter", ThinkOff, false},
		{"reasoning-mandatory model", "openrouter", "openai/gpt-6-luna", "feed-classify", ThinkOff, false},
		{"preset without temperature", "openai", "gpt-4.1", "jd-extract", ThinkOff, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			s := newOAServer(t, okHandler("ok"))
			p := oaProvider("a", s)
			p.Preset = c.preset
			e.provider(p, fakeKey("oa"))
			e.route(c.task, Route{Provider: "a", Model: c.model, Thinking: c.thinking, MaxOutputTokens: 4096})
			if _, err := e.r.Complete(bg(), Request{Task: c.task, Prompt: "hi"}); err != nil {
				t.Fatal(err)
			}
			got, has := s.last["temperature"]
			if has != c.want || (has && fmt.Sprint(got) != "0") {
				t.Fatalf("temperature = %v (sent %v), want sent %v", got, has, c.want)
			}
		})
	}
}

// NVIDIA NIM's reasoning models think unless the chat template is told not
// to; with no switch an apply call at 512 tokens spent them all thinking and
// returned no text.
func TestLLM_NVIDIA_ThinkingOffTurnsTheTemplateSwitchOff(t *testing.T) {
	for _, c := range []struct {
		thinking string
		want     bool
	}{{ThinkOff, false}, {"", false}, {ThinkMedium, true}} {
		e := newEnv(t)
		s := newOAServer(t, okHandler("ok"))
		p := oaProvider("a", s)
		p.Preset = "nvidia"
		e.provider(p, fakeKey("oa"))
		e.route("apply-check", Route{Provider: "a", Model: "nvidia/nemotron-3.5-lightning-30b-a3b", Thinking: c.thinking, MaxOutputTokens: 4096})
		if _, err := e.r.Complete(bg(), Request{Task: "apply-check", Prompt: "hi"}); err != nil {
			t.Fatal(err)
		}
		kw, _ := s.last["chat_template_kwargs"].(map[string]any)
		if got, ok := kw["enable_thinking"].(bool); !ok || got != c.want {
			t.Errorf("thinking %q: chat_template_kwargs = %v, want enable_thinking %v", c.thinking, s.last["chat_template_kwargs"], c.want)
		}
		if _, has := s.last["reasoning_effort"]; has {
			t.Errorf("thinking %q: reasoning_effort was sent", c.thinking)
		}
	}
}

func TestLLM_OpenAI_TokenParamFollowsPreset(t *testing.T) {
	e := newEnv(t)
	s := newOAServer(t, okHandler("ok"))
	p := oaProvider("a", s)
	p.Preset = "openai"
	e.provider(p, fakeKey("oa"))
	e.route("narrative", Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff})
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.last["max_completion_tokens"]; !ok {
		t.Fatalf("request = %v", s.last)
	}
}

func anStream(w http.ResponseWriter, stop string, in, out, cacheRead int, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":%d,\"cache_read_input_tokens\":%d,\"output_tokens\":1}}}\n\n", in, cacheRead)
	fmt.Fprint(w, "data: {\"type\":\"ping\"}\n\n")
	for _, p := range pieces {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", b)
		f.Flush()
	}
	fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":%d}}\n\n", stop, out)
	fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	f.Flush()
}

func TestLLM_Anthropic_StreamsAndCountsCachedInputInTheTotal(t *testing.T) {
	e := newEnv(t)
	key := fakeKey("an")
	var mu sync.Mutex
	var hdr http.Header
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Unlock()
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		anStream(w, "end_turn", 200, 40, 800, "Bonjour")
	}))
	t.Cleanup(srv.Close)
	e.provider(ProviderConfig{ID: "an", Kind: KindAnthropic, BaseURL: srv.URL}, key)
	e.route("narrative", Route{Provider: "an", Model: "claude-sonnet-4-6", Thinking: ThinkMedium, MaxOutputTokens: 4096})
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", System: "sys", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Bonjour" || resp.Usage != (Usage{Input: 1000, Output: 40, CachedInput: 800}) || resp.FinishReason != "stop" {
		t.Fatalf("resp = %+v", resp)
	}
	if hdr.Get("x-api-key") != key || hdr.Get("anthropic-version") == "" {
		t.Fatalf("headers = %v", hdr)
	}
	th, _ := body["thinking"].(map[string]any)
	oc, _ := body["output_config"].(map[string]any)
	if body["system"] != "sys" || th["type"] != "adaptive" || oc["effort"] != "medium" || body["stream"] != true {
		t.Fatalf("request = %v", body)
	}
}

func TestLLM_Anthropic_HighestThinkingMapsToMaxEffortAndHaikuUsesBudget(t *testing.T) {
	body := map[string]any{}
	anthropicThinking("claude-opus-4-8", ThinkXHigh, 16000, body)
	if body["output_config"].(map[string]any)["effort"] != "max" {
		t.Fatalf("body = %v", body)
	}
	body = map[string]any{}
	anthropicThinking("claude-haiku-4-5", ThinkMedium, 4096, body)
	if th := body["thinking"].(map[string]any); th["type"] != "enabled" || th["budget_tokens"] != 3072 {
		t.Fatalf("haiku body = %v", body)
	}
	// Every model before 4.6 takes a budget; 4.6 and later take adaptive thinking.
	for model, budget := range map[string]bool{"claude-sonnet-4-5-20250929": true, "claude-opus-4-1": true, "claude-sonnet-4-20250514": true,
		"claude-3-7-sonnet-latest": true, "claude-opus-4-6": false, "claude-sonnet-5-5": false, "claude-haiku-5-5": false} {
		body = map[string]any{}
		anthropicThinking(model, ThinkMedium, 16000, body)
		if got := body["thinking"].(map[string]any)["type"] == "enabled"; got != budget {
			t.Errorf("%s: body = %v, want a budget %v", model, body, budget)
		}
	}
	body = map[string]any{}
	anthropicThinking("claude-sonnet-4-6", ThinkOff, 4096, body)
	if len(body) != 0 {
		t.Fatalf("thinking off must send nothing: %v", body)
	}
}

func TestLLM_Anthropic_ErrorEventsAreClassified(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n")
	}))
	t.Cleanup(srv.Close)
	e.provider(ProviderConfig{ID: "an", Kind: KindAnthropic, BaseURL: srv.URL}, fakeKey("an"))
	e.route("narrative", Route{Provider: "an", Model: "claude-haiku-4-5", Thinking: ThinkOff})
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	wantKind(t, err, KindOverloaded)
}

// An OpenAI-wire error inside a 200 stream is read by its code first: a
// number is the upstream status (OpenRouter), a string is the API's code.
func TestLLM_OpenAI_InStreamErrorReadsItsCode(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		want        Kind
	}{
		{"status 429", `{"error":{"code":429,"message":"Provider returned error"}}`, KindRateLimited},
		{"status 402", `{"error":{"code":402,"message":"Provider returned error"}}`, KindQuota},
		{"status 502", `{"error":{"code":502,"message":"Provider returned error"}}`, KindOverloaded},
		{"quota code", `{"error":{"code":"insufficient_quota","message":"Provider returned error"}}`, KindQuota},
		{"rate limit code", `{"error":{"code":"rate_limit_exceeded","message":"Provider returned error"}}`, KindRateLimited},
		{"no code, network text", `{"error":{"message":"connection reset by peer"}}`, KindNetwork},
		{"no code", `{"error":{"message":"Provider returned error"}}`, KindOverloaded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _ := pair(t, func(w http.ResponseWriter, r *http.Request, n int) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", tc.event)
			}, okHandler("from b"))
			if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
				t.Fatal(err)
			}
			if rows := e.ledger(); rows[0].Outcome != string(tc.want) {
				t.Fatalf("outcome = %s, want %s", rows[0].Outcome, tc.want)
			}
		})
	}
}

func TestLLM_Anthropic_HTTPStatusClassified(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oaFail(w, 529, `{"type":"error"}`) }))
	t.Cleanup(srv.Close)
	e.provider(ProviderConfig{ID: "an", Kind: KindAnthropic, BaseURL: srv.URL}, fakeKey("an"))
	e.route("narrative", Route{Provider: "an", Model: "claude-haiku-4-5", Thinking: ThinkOff})
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if ce := wantKind(t, err, KindOverloaded); ce.Attempts[0].Status != 529 {
		t.Fatalf("attempts = %+v", ce.Attempts)
	}
}

func TestLLM_ErrorText_RedactedAndBounded(t *testing.T) {
	key := fakeKey("rd")
	e := newEnv(t)
	long := strings.Repeat("x", 2000)
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		oaFail(w, 401, fmt.Sprintf(`{"error":"bad header %s: Authorization: Bearer %s %s"}`, r.Header.Get("Authorization"), key, long))
	})
	e.provider(oaProvider("a", s), key)
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	ce := wantKind(t, err, KindAuth)
	var rowText string
	_ = e.st.Read().QueryRow(`SELECT error_text FROM llm_ledger`).Scan(&rowText)
	for _, text := range []string{err.Error(), ce.Message, rowText} {
		if strings.Contains(text, key) || strings.Contains(text, key[:12]) {
			t.Fatalf("text holds part of the key: %q", text)
		}
	}
	if len(ce.Message) > maxErrText || len(rowText) > maxErrText {
		t.Fatalf("error text not bounded: %d %d", len(ce.Message), len(rowText))
	}
}

func TestLLM_Classify_ModelGoneFromMessageWithoutStatus(t *testing.T) {
	cases := map[string]Kind{
		"410 status code (no body)":        KindModelGone,
		"Connection error.":                KindNetwork,
		"dial tcp: lookup x: no such host": KindNetwork,
		"something odd":                    KindOther,
	}
	for msg, want := range cases {
		if got := classifyError(fmt.Errorf("%s", msg)).Kind; got != want {
			t.Errorf("classifyError(%q) = %s, want %s", msg, got, want)
		}
	}
}

func TestLLM_OpenRouter_WebSearchSendsThePluginAndReturnsTheCitations(t *testing.T) {
	e := newEnv(t)
	ann := func(u, title string) string {
		return fmt.Sprintf(`{"type":"url_citation","url_citation":{"url":%q,"title":%q,"content":"page text","start_index":0,"end_index":4}}`, u, title)
	}
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n > 1 {
			oaStream(w, "stop", 10, 5, 0, "plain")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"Go 2 is out [a](https://a.example/x).","annotations":[`+
			ann("https://a.example/x", "A")+","+ann("https://a.example/x", "A")+","+ann("https://b.example/y", "B")+`]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	})
	p := oaProvider("or", s)
	p.Preset = "openrouter"
	e.provider(p, fakeKey("or"))
	e.route("research-search", Route{Provider: "or", Model: "vendor/m", Thinking: ThinkOff})
	if !e.r.NativeSearch(bg(), "research-search") {
		t.Fatal("an OpenRouter route was not reported as searching")
	}
	resp, err := e.r.Complete(bg(), Request{Task: "research-search", Prompt: "q", WebSearch: 5})
	if err != nil {
		t.Fatal(err)
	}
	if s.last["stream"] != false {
		t.Fatalf("a search call streamed: %v", s.last["stream"])
	}
	if resp.Text != "Go 2 is out [a](https://a.example/x)." || resp.Usage.Input != 10 {
		t.Fatalf("reply = %+v", resp)
	}
	plugins, _ := s.last["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("plugins = %v", s.last["plugins"])
	}
	if pl := plugins[0].(map[string]any); pl["id"] != "web" || pl["engine"] != "exa" || pl["max_results"] != float64(5) {
		t.Fatalf("plugin = %v", pl)
	}
	want := []Source{{"https://a.example/x", "A", "page text"}, {"https://b.example/y", "B", "page text"}}
	if fmt.Sprint(resp.Sources) != fmt.Sprint(want) {
		t.Fatalf("sources = %+v", resp.Sources)
	}
	// A call that does not ask for a search sends no plugin.
	if _, err := e.r.Complete(bg(), Request{Task: "research-search", Prompt: "q"}); err != nil {
		t.Fatal(err)
	}
	if _, has := s.last["plugins"]; has || s.last["stream"] != true {
		t.Fatalf("a call without a search sent plugins %v or did not stream", s.last["plugins"])
	}
}

func TestLLM_WebSearch_OnlyAProviderThatSearchesServesTheCall(t *testing.T) {
	e := newEnv(t)
	s := newOAServer(t, okHandler("ok"))
	e.provider(oaProvider("plain", s), fakeKey("pl"))
	e.route("research-search", Route{Provider: "plain", Model: "m1", Thinking: ThinkOff})
	if e.r.NativeSearch(bg(), "research-search") {
		t.Fatal("a provider without a search was reported as searching")
	}
	_, err := e.r.Complete(bg(), Request{Task: "research-search", Prompt: "q", WebSearch: 5})
	wantKind(t, err, KindNoRoute)
	if s.count() != 0 {
		t.Fatalf("the provider was called %d times", s.count())
	}
	if e.r.NativeSearch(bg(), "no-such-task") {
		t.Fatal("an unknown task was reported as searching")
	}
}

func TestLLM_WebSearch_LedgerCostIncludesTheSearchFeeUnderTheTask(t *testing.T) {
	e := newEnv(t)
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			oaStream(w, "stop", 1000000, 1, 0, "ok")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`)
	})
	p := oaProvider("or", s)
	p.Preset = "openrouter"
	e.provider(p, fakeKey("or"))
	e.route("research-search", Route{Provider: "or", Model: "openai/gpt-6-luna", Thinking: ThinkOff})
	plain, err := e.r.Complete(bg(), Request{Task: "research-search", Prompt: "q"})
	if err != nil || plain.CostUSD == nil || roundMicro(*plain.CostUSD) != 0.100001 {
		t.Fatalf("plain call: %+v, %v", plain, err)
	}
	searched, err := e.r.Complete(bg(), Request{Task: "research-search", Prompt: "q", WebSearch: 5})
	if err != nil || searched.CostUSD == nil || *searched.CostUSD != 0.107 {
		t.Fatalf("search call: %+v, %v", searched, err)
	}
	rows := e.ledger()
	if len(rows) != 2 || rows[1].Task != "research-search" || rows[1].Cost == nil || *rows[1].Cost != 0.107 || rows[1].Billing != BillingMetered {
		t.Fatalf("ledger = %+v", rows)
	}
	byTask, _, err := e.r.CostReport(bg(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var task *ReportRow
	for i := range byTask {
		if byTask[i].Name == "research-search" {
			task = &byTask[i]
		}
	}
	if task == nil || task.Calls != 2 || task.CostUSD != 0.207001 {
		t.Fatalf("by task = %+v", byTask)
	}
}
