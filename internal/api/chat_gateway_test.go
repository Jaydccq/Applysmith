package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// toUpstream sends every provider request to a local stand-in, whatever host
// the preset names.
type toUpstream struct{ target *url.URL }

func (t toUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestChatAPI_AModelCallOfATurnGoesThroughTheGateway(t *testing.T) {
	var mu sync.Mutex
	var gotAuth, gotModel, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		gotModel = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"No rows yet.\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond) // a reply that outlives the handler deadline
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1200,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":1100}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)

	relayed := make(chan string, 1)
	script := func(p *scriptedPi) {
		piStart(p)
		req, _ := http.NewRequest("POST", p.url+agent.ModelPathOpenAI, bytes.NewReader(jsonBody(map[string]any{
			"model": "openai/gpt-6.1-sol", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "How many?"}}})))
		req.Header.Set("Authorization", "Bearer "+p.token)
		req.Header.Set("Content-Type", "application/json")
		got := ""
		if resp, err := http.DefaultClient.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			got = fmt.Sprintf("%d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
		relayed <- got
		p.emit(assistantLine([]map[string]any{{"type": "text", "text": "No rows yet."}}, "stop"))
		p.emit(map[string]any{"type": "agent_settled"})
	}
	h, l := chatHarness(t, script, func(c *Config) {
		c.Domain.LLM = append(c.Domain.LLM, llm.WithHTTPClient(&http.Client{Transport: toUpstream{target}}))
	})
	// A model call streams for as long as the provider does, never cut by the
	// deadlines of the synchronous routes.
	h.srv.deadline, h.srv.readTimeout = 100*time.Millisecond, 100*time.Millisecond
	cookie := h.session()
	r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "Tell me about the Applied rows."})
	var acc wire.Success[wire.ChatTurnAccepted]
	strictInto(t, r.body, &acc)
	readStream(t, h, acc.Data.SessionID, "", terminalItem, l.release)

	got := <-relayed
	if !strings.HasPrefix(got, "200 text/event-stream ") || !strings.Contains(got, "No rows yet.") || !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Fatalf("pi got %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	// The provider is called with the vault's key, which pi never held.
	if gotAuth != "Bearer "+testSecret || gotPath != "/api/v1/chat/completions" || !strings.Contains(gotModel, `"model":"openai/gpt-6.1-sol"`) {
		t.Fatalf("upstream got %q %q %s", gotAuth, gotPath, gotModel)
	}
	var n, cached int
	var kind string
	err := h.srv.store.Load().Read().QueryRow(`SELECT count(*), max(provider_kind), max(cached_input_tokens) FROM llm_ledger
		WHERE task = 'agent-kernel' AND provider_id = 'openrouter' AND outcome = 'ok' AND run_id = ?`, acc.Data.SessionID).Scan(&n, &kind, &cached)
	if err != nil || n != 1 || kind != llm.KindOpenAI || cached != 1100 {
		t.Fatalf("ledger: %d rows of kind %q, cached %d, %v", n, kind, cached, err)
	}
}
