package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

const schemaWorkspaceID = "0123456789abcdef0123456789abcdef"

// anthropicBanned are the keywords the documented limits of output_config.format for the
// Messages API (platform.claude.com, build-with-claude/structured-outputs) rejects.
var anthropicBanned = []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "maxItems", "pattern", "oneOf"}

type schemaStats struct{ optional, unions int }

func checkAnthropicRules(t *testing.T, node any, path string, st *schemaStats) {
	t.Helper()
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	for _, k := range anthropicBanned {
		if _, has := m[k]; has {
			t.Errorf("%s: keyword %q is not accepted by the Messages API", path, k)
		}
	}
	if n, has := m["minItems"]; has && n != float64(0) && n != float64(1) {
		t.Errorf("%s: minItems %v; only 0 and 1 are accepted", path, n)
	}
	if _, isList := m["type"].([]any); isList {
		st.unions++
	}
	_, hasProps := m["properties"]
	if m["type"] == "object" || hasProps {
		if ap, has := m["additionalProperties"]; !has || ap != false {
			t.Errorf("%s: an object needs additionalProperties:false, has %v", path, ap)
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		required := map[string]bool{}
		if req, ok := m["required"].([]any); ok {
			for _, r := range req {
				required[fmt.Sprint(r)] = true
			}
		}
		for name, sub := range props {
			if !required[name] {
				st.optional++
			}
			checkAnthropicRules(t, sub, path+"."+name, st)
		}
	}
	if items, ok := m["items"]; ok {
		checkAnthropicRules(t, items, path+"[]", st)
	}
	for _, kw := range []string{"anyOf", "allOf"} {
		if list, ok := m[kw].([]any); ok {
			if kw == "anyOf" {
				st.unions++
			}
			for i, sub := range list {
				checkAnthropicRules(t, sub, fmt.Sprintf("%s.%s[%d]", path, kw, i), st)
			}
		}
	}
}

type schemaEnv struct {
	r    *llm.Router
	mu   sync.Mutex
	body []map[string]any
	// reply is what the stub model answers.
	reply string
}

func newSchemaEnv(t *testing.T, reply string) *schemaEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.StateDir, store.DBFile), store.Options{WorkspaceID: schemaWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	v, err := vault.New(schemaWorkspaceID, vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	e := &schemaEnv{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		e.mu.Lock()
		e.body = append(e.body, b)
		e.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		txt, _ := json.Marshal(e.reply)
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
		fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", txt)
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	e.r = llm.New(st, v, llm.WithGetenv(func(string) string { return "" }))
	if err := e.r.SetProvider(ctx, llm.ProviderConfig{ID: "an", Kind: llm.KindAnthropic, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := e.r.SetKey("an", "sk-"+strings.Repeat("a", 8)+"zz"+strings.Repeat("b", 24)); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"quick-screen", "deep-eval"} {
		row := llm.Route{Provider: "an", Model: "claude-sonnet-5-5", MaxOutputTokens: 4096}
		if err := e.r.PatchRoutes(ctx, map[string]*llm.Route{task: &row}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *schemaEnv) sentSchema(t *testing.T, i int) any {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	oc, _ := e.body[i]["output_config"].(map[string]any)
	f, _ := oc["format"].(map[string]any)
	if f["type"] != "json_schema" || f["schema"] == nil {
		t.Fatalf("request has no json_schema format: %v", e.body[i])
	}
	return f["schema"]
}

func TestLLM_Anthropic_StructuredRequestObeysTheDocumentedSchemaSubset(t *testing.T) {
	cases := []struct {
		name   string
		task   string
		schema json.RawMessage
		reply  string
	}{
		{"screen", "quick-screen", evaluate.ScreenSchema(),
			`{"score":3,"decision":"review","legitimacy":"credible","blockers":[],"summary":"ok"}`},
		{"deep", "deep-eval", evaluate.DeepSchema(), `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSchemaEnv(t, c.reply)
			var out map[string]any
			_, _ = e.r.Structured(context.Background(), llm.Request{Task: c.task, Prompt: "p"}, c.schema, &out)
			var st schemaStats
			checkAnthropicRules(t, e.sentSchema(t, 0), "$", &st)
			t.Logf("optional=%d unions=%d", st.optional, st.unions)
			if st.optional > 24 || st.unions > 16 {
				t.Errorf("optional=%d unions=%d exceed the documented per-request limits of 24 and 16", st.optional, st.unions)
			}
		})
	}
}

func TestLLM_Anthropic_StrippedConstraintsStillEnforcedLocally(t *testing.T) {
	// The sent schema has no maximum, so the provider cannot enforce it; the
	// local validation of the original schema must still reject the reply.
	e := newSchemaEnv(t, `{"score":9,"decision":"advance","legitimacy":"credible","blockers":[],"summary":"x"}`)
	var out map[string]any
	_, err := e.r.Structured(context.Background(), llm.Request{Task: "quick-screen", Prompt: "p"}, evaluate.ScreenSchema(), &out)
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.KindInvalidStructure {
		t.Fatalf("err = %v", err)
	}
}
