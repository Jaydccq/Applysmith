package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func oaPartial(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	b, _ := json.Marshal(text)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", b)
}

func ledgerErrorText(t *testing.T, e *env) string {
	t.Helper()
	var s string
	if err := e.st.Read().QueryRow(`SELECT error_text FROM llm_ledger ORDER BY id DESC LIMIT 1`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLLM_OpenAI_StreamWithoutTerminatorIsANetworkFailure(t *testing.T) {
	e, sa, sb := pair(t, func(w http.ResponseWriter, r *http.Request, n int) { oaPartial(w, "half an ans") }, okHandler("whole"))
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Text != "whole" || sa.count() != 1 || sb.count() != 1 {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if rows := e.ledger(); rows[0].Outcome != string(KindNetwork) {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Anthropic_StreamWithoutMessageStopIsANetworkFailure(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"half\"}}\n\n")
	}))
	t.Cleanup(srv.Close)
	e.provider(ProviderConfig{ID: "an", Kind: KindAnthropic, BaseURL: srv.URL}, fakeKey("an"))
	e.route("narrative", Route{Provider: "an", Model: "claude-sonnet-4-6", Thinking: ThinkOff, MaxOutputTokens: 1024})
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	wantKind(t, err, KindNetwork)
}

func TestLLM_OpenAI_MidStreamFailureWithoutUsageEstimatesTokens(t *testing.T) {
	e, _, _ := pair(t, func(w http.ResponseWriter, r *http.Request, n int) {
		oaPartial(w, strings.Repeat("word ", 40))
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream reset\"}}\n\n")
	}, okHandler("x"))
	e.route("narrative", Route{Provider: "a", Model: "m1", Thinking: ThinkOff})
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: strings.Repeat("prompt ", 40),
		OnDelta: func(string) {}})
	if err == nil {
		t.Fatal("the failure was swallowed")
	}
	rows := e.ledger()
	if rows[0].Input == 0 || rows[0].Output == 0 {
		t.Fatalf("a failed attempt that streamed counts no tokens: %+v", rows[0])
	}
	if !strings.Contains(ledgerErrorText(t, e), "estimated") {
		t.Fatalf("the row is not marked estimated: %q", ledgerErrorText(t, e))
	}
}

func TestLLM_OpenAI_ReportedUsageIsNotEstimated(t *testing.T) {
	e, _, _ := pair(t, okHandler("fine"), okHandler("x"))
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if rows := e.ledger(); rows[0].Input != 10 || rows[0].Output != 5 || strings.Contains(ledgerErrorText(t, e), "estimated") {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_Downgrade_MissingKeyOnTheHeadFallsBackToThePrimary(t *testing.T) {
	e := newEnv(t)
	dear := newOAServer(t, okHandler("dear answered"))
	cheap := newOAServer(t, okHandler("cheap answered"))
	pd := oaProvider("dear", dear)
	pd.Preset = "openai"
	e.provider(pd, fakeKey("dk"))
	pc := oaProvider("cheap", cheap)
	pc.Preset = "openai"
	e.provider(pc, "") // no key
	e.route("narrative", Route{Provider: "dear", Model: "gpt-6.1-sol", Thinking: ThinkOff,
		Fallbacks: []Fallback{{"cheap", "gpt-6-luna"}}})
	addSpend(t, e, 2.05)
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "dear" || cheap.count() != 0 {
		t.Fatalf("an over-cap call failed on a head without a key: resp %+v err %v", resp, err)
	}
}

func TestLLM_Price_CacheWritesCostMoreThanPlainInput(t *testing.T) {
	p := Price{InputPerM: 1.0, CachedInputPerM: 0.1, OutputPerM: 2.0}
	got := p.Cost(Usage{Input: 1000, CachedInput: 300, CacheWrite: 400})
	want := (300*0.1 + 400*1.25 + 300*1.0) / 1e6
	if got != roundMicro(want) {
		t.Fatalf("cost = %v, want %v", got, roundMicro(want))
	}
}

func TestLLM_Anthropic_CacheCreationTokensAreCountedAsWrites(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":200,\"cache_creation_input_tokens\":400}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	e.provider(ProviderConfig{ID: "an", Kind: KindAnthropic, BaseURL: srv.URL}, fakeKey("an"))
	e.route("narrative", Route{Provider: "an", Model: "claude-sonnet-4-6", Thinking: ThinkOff, MaxOutputTokens: 1024})
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage != (Usage{Input: 700, Output: 3, CachedInput: 200, CacheWrite: 400}) {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestLLM_Structured_PatternThatDoesNotCompileIsRefused(t *testing.T) {
	e, sa, _ := pair(t, okHandler(`"x"`), okHandler(`"x"`))
	var v string
	_, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "p"}, json.RawMessage(`{"type":"string","pattern":"(unclosed"}`), &v)
	wantKind(t, err, KindBadRequest)
	if sa.count() != 0 {
		t.Fatal("a call was made for a schema that cannot be checked")
	}
}

func TestLLM_Structured_RepairIsOneCallWithCombinedCost(t *testing.T) {
	e := newEnv(t)
	s := newOAServer(t, seqHandler(`{"name":"Ada Example"}`, `{"name":"Ada Example","age":36}`))
	p := oaProvider("a", s)
	p.Preset = "openai"
	e.provider(p, fakeKey("sr"))
	e.route("narrative", Route{Provider: "a", Model: "gpt-6.1-sol", Thinking: ThinkOff})
	var out person
	resp, err := e.r.Structured(bg(), Request{Task: "narrative", Prompt: "who"}, json.RawMessage(personSchema), &out)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	var sum float64
	if err := e.st.Read().QueryRow(`SELECT count(DISTINCT call_id), sum(cost_usd) FROM llm_ledger`).Scan(&calls, &sum); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || resp.CostUSD == nil || roundMicro(*resp.CostUSD) != roundMicro(sum) || resp.Usage.Input != 20 || resp.Attempts != 2 {
		t.Fatalf("distinct calls %d, ledger cost %v, response %+v", calls, sum, resp)
	}
	var attempts string
	_ = e.st.Read().QueryRow(`SELECT group_concat(attempt) FROM llm_ledger`).Scan(&attempts)
	if attempts != "1,2" {
		t.Fatalf("attempt numbers = %s", attempts)
	}
}

func TestLLM_Embed_VectorOfAnotherDimensionFailsTheReply(t *testing.T) {
	s := newOAServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3,4]},{"index":1,"embedding":[1,2]}],"usage":{"prompt_tokens":2}}`)
	})
	e := newEnv(t)
	e.provider(oaProvider("a", s), fakeKey("em"))
	e.route("embedding", Route{Provider: "a", Model: "emb-1"})
	_, err := e.r.Embed(bg(), []string{"a", "b"}, EmbedOptions{})
	wantKind(t, err, KindEmptyCompletion)
}
