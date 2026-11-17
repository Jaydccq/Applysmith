package llm

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLLM_ProviderCapabilities(t *testing.T) {
	cases := []struct {
		p    ProviderConfig
		cap  Capability
		want bool
	}{
		{ProviderConfig{Kind: KindOpenAI}, CapTools, true},
		{ProviderConfig{Kind: KindOpenAI}, CapStructured, false},
		{ProviderConfig{Kind: KindOpenAI, StructuredOutput: true}, CapStructured, true},
		{ProviderConfig{Kind: KindOpenAI, Embeddings: true}, CapEmbeddings, true},
		{ProviderConfig{Kind: KindAnthropic}, CapStructured, true},
		{ProviderConfig{Kind: KindCLI}, CapTools, false},
	}
	for _, c := range cases {
		if got := c.p.Has(c.cap); got != c.want {
			t.Errorf("%s has %q = %v, want %v", c.p.Kind, c.cap, got, c.want)
		}
	}
}

func TestLLM_Routing_RejectsMissingCapability(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "codex", Kind: KindCLI, Program: "codex"}, "")
	e.provider(ProviderConfig{ID: "anth", Kind: KindAnthropic}, "")

	t.Run("agent loop needs tool calling", func(t *testing.T) {
		err := e.r.PatchRoutes(bg(), map[string]*Route{"agent-kernel": {Provider: "codex", Model: "default"}})
		if err == nil || !strings.Contains(err.Error(), "agent-kernel requires native tool calling") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("embeddings need an endpoint that has them", func(t *testing.T) {
		err := e.r.PatchRoutes(bg(), map[string]*Route{"embedding": {Provider: "anth", Model: "m"}})
		if err == nil || !strings.Contains(err.Error(), "no embeddings") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLLM_BaseURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://api.deepseek.com", true},
		{"http://llm.example.test/v1", false}, // plain-http remote URL is rejected
		{"http://127.0.0.1:11434/v1", true},   // local server
		{"http://[::1]:1234/v1", true},
		{"not a url", false},
	}
	for _, c := range cases {
		if err := ValidateBaseURL(c.url); (err == nil) != c.ok {
			t.Errorf("ValidateBaseURL(%q) = %v, want ok=%v", c.url, err, c.ok)
		}
	}
}

func TestLLM_PresetBaseURLs_AndNoKeyInStoredEntry(t *testing.T) {
	want := map[string]string{
		"openai":     "https://api.openai.com/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"deepseek":   "https://api.deepseek.com",
		"nvidia":     "https://integrate.api.nvidia.com/v1",
		"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai/",
	}
	for name, url := range want {
		p, _ := PresetByName(name)
		cfg := p.ProviderConfig()
		if cfg.Kind != KindOpenAI || cfg.BaseURL != url {
			t.Errorf("%s entry = %+v", name, cfg)
		}
	}
	e := newEnv(t)
	key := fakeKey("k1")
	if err := e.r.SetKey("deepseek", key); err != nil {
		t.Fatal(err)
	}
	if err := e.r.ConfigurePreset(bg(), "deepseek", ConfigureOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Read().Query(`SELECT value FROM llm_settings`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		if strings.Contains(v, key) {
			t.Fatalf("stored settings contain the key: %s", v)
		}
	}
}

func TestLLM_Registry_EveryTaskHasClassAndLimits(t *testing.T) {
	for _, tk := range tasks {
		if tk.Class == "" || tk.Purpose == "" {
			t.Errorf("%s has no class or no purpose", tk.ID)
		}
		if tk.Class == ClassEmbedding {
			continue
		}
		if tk.Thinking == "" || !validThinking(tk.Thinking) || tk.MaxTokens < minCeiling || tk.Timeout <= 0 {
			t.Errorf("%s has incomplete defaults: %+v", tk.ID, tk)
		}
		if tk.Thinking != ThinkOff && tk.MaxTokens < minThinkingMax {
			t.Errorf("%s: thinking on with ceiling %d", tk.ID, tk.MaxTokens)
		}
	}
	d, _ := LookupTask("deep-eval")
	// deepseek-v4-pro used up to 15,990 of 16,000 tokens on live evaluations
	// and one long posting ended truncated after 15,413 reasoning tokens.
	if d.Class != ClassDeep || d.Thinking != ThinkMedium || d.MaxTokens != 24000 || d.Timeout != 600*time.Second {
		t.Errorf("deep-eval defaults = %+v", d)
	}
	// A live 1,158-word posting hit the old 6,000-token ceiling on every try;
	// the decode returns as many quoted items as the evaluation.
	if j, _ := LookupTask("jd-decode"); j.MaxTokens < d.MaxTokens || j.Timeout < 180*time.Second {
		t.Errorf("jd-decode defaults = %+v", j)
	}
	// deepseek-v4-pro reasoned through the old 8,000-token ceiling on 3 of
	// 12 live résumé drafts (truncated, which does not advance); the ok
	// replies used up to 6,538.
	if r, _ := LookupTask("resume-write"); r.MaxTokens < 16000 || r.Timeout < 300*time.Second {
		t.Errorf("resume-write defaults = %+v", r)
	}
	// deepseek-flash letters used up to 3,591 of 4,096 tokens and one ended
	// truncated after 3,845 reasoning tokens; 6,144 still fits the 45-second
	// letter deadline of application-documents.
	if c, _ := LookupTask("cover-letter"); c.MaxTokens < 6144 {
		t.Errorf("cover-letter defaults = %+v", c)
	}
	// Measured on DeepSeek over three postings, six runs per setting, with
	// the drafting prompt that names what the gate refuses: thinking off
	// passed as often as low (no template fallback either way) in a quarter
	// to a half of the time and cost (docs/architecture/llm.md). The grounded
	// document tasks therefore draft without reasoning; the review keeps it.
	for _, id := range []string{"cover-letter", "resume-write"} {
		if tk, _ := LookupTask(id); tk.Thinking != ThinkOff {
			t.Errorf("%s thinking = %s, want off", id, tk.Thinking)
		}
	}
	// The review at thinking low ended empty at its old 4,096 ceiling on 2 of
	// 30 measured calls (22 s each, then a fallback).
	if r, _ := LookupTask("document-review"); r.MaxTokens < 6144 {
		t.Errorf("document-review defaults = %+v", r)
	}
}

func TestLLM_Row_ThinkingNeedsCeiling2048(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	err := e.r.PatchRoutes(bg(), map[string]*Route{"narrative": {Provider: "p1", Model: "m", Thinking: ThinkMedium, MaxOutputTokens: 1024}})
	if err == nil || !strings.Contains(err.Error(), "at least 2048") || !strings.Contains(err.Error(), "thinking must be off") {
		t.Fatalf("starved reasoning budget err = %v", err)
	}
	if err := e.r.PatchRoutes(bg(), map[string]*Route{"narrative": {Provider: "p1", Model: "m", Thinking: ThinkOff, MaxOutputTokens: 1024}}); err != nil {
		t.Fatalf("thinking off with a small ceiling must pass: %v", err)
	}
}

func TestLLM_Row_Bounds(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	good := Route{Provider: "p1", Model: "m", Thinking: ThinkOff}
	cases := map[string]func(*Route){
		"ceiling high": func(r *Route) { r.MaxOutputTokens = 400001 },
		"bad thinking": func(r *Route) { r.Thinking = "max" },
		"five fallbacks": func(r *Route) {
			r.Fallbacks = make([]Fallback, 5)
			for i := range r.Fallbacks {
				r.Fallbacks[i] = Fallback{"p1", "m"}
			}
		},
		"unknown provider": func(r *Route) { r.Provider = "ghost" },
	}
	for name, mutate := range cases {
		row := good
		mutate(&row)
		err := e.r.PatchRoutes(bg(), map[string]*Route{"narrative": &row})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if name == "unknown provider" && (err == nil || !strings.Contains(err.Error(), "ghost")) {
			t.Errorf("unknown provider error must name the id: %v", err)
		}
	}
	ok := good
	ok.MaxOutputTokens, ok.TimeoutSeconds = 400000, 1800
	ok.Fallbacks = []Fallback{{"p1", "a"}, {"p1", "b"}, {"p1", "c"}, {"p1", "d"}}
	if err := e.r.PatchRoutes(bg(), map[string]*Route{"narrative": &ok}); err != nil {
		t.Fatalf("boundary row rejected: %v", err)
	}
}

func TestLLM_Patch_AtomicAllOrNothing(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	err := e.r.PatchRoutes(bg(), map[string]*Route{
		"narrative":    {Provider: "p1", Model: "m"},
		"quick-screen": {Provider: "ghost", Model: "m"},
	})
	if err == nil {
		t.Fatal("patch with an invalid row succeeded")
	}
	s := e.load()
	if len(s.Routes) != 0 {
		t.Fatalf("a row changed despite the error: %v", s.Routes)
	}
	if err := e.r.PatchRoutes(bg(), map[string]*Route{"self-heal": {Provider: "p1", Model: "m"}}); err == nil {
		t.Fatal("unknown task accepted")
	}
}

func TestLLM_API_GetShowsSourcePatchNullClears(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	e.route("narrative", Route{Provider: "p1", Model: "m"})
	tab := e.show()
	var src string
	for _, row := range tab.Rows {
		if row.Task.ID == "narrative" {
			src = row.Source
		}
	}
	if src != SourceTable {
		t.Fatalf("source = %q", src)
	}
	if err := e.r.PatchRoutes(bg(), map[string]*Route{"narrative": nil}); err != nil {
		t.Fatal(err)
	}
	tab = e.show()
	if _, ok := tab.Unrouted["narrative"]; !ok {
		t.Fatal("a cleared row must leave the task unrouted when nothing else applies")
	}
}

func TestLLM_Resolve_Order(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	e.provider(ProviderConfig{ID: "p2", Kind: KindFake}, "")

	t.Run("nothing configured", func(t *testing.T) {
		_, err := e.r.Resolve(bg(), "narrative")
		ne := wantKind(t, err, KindNoRoute)
		if !errors.Is(err, ErrNoRoute) || !strings.Contains(ne.Message, "narrative") || !strings.Contains(ne.Message, "applysmith llm configure") {
			t.Fatalf("no-route error = %v", err)
		}
	})
	t.Run("environment wins over preset and keeps the preset limits", func(t *testing.T) {
		e.envs["APPLYSMITH_MODEL_QUICK_SCREEN"] = "p1/some/model-with-slash"
		res := e.resolve("quick-screen")
		if res.Source != SourceEnv || res.Provider != "p1" || res.Model != "some/model-with-slash" {
			t.Fatalf("res = %+v", res)
		}
		d, _ := LookupTask("quick-screen")
		if res.Thinking != d.Thinking || res.MaxOutputTokens != d.MaxTokens || res.Timeout != d.Timeout || len(res.Fallbacks) != 0 {
			t.Fatalf("env pin must inherit task defaults and no fallbacks: %+v", res)
		}
	})
	t.Run("table wins over environment", func(t *testing.T) {
		e.envs["APPLYSMITH_MODEL_NARRATIVE"] = "p1/env-model"
		e.route("narrative", Route{Provider: "p2", Model: "table-model"})
		res := e.resolve("narrative")
		if res.Source != SourceTable || res.Model != "table-model" {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("edit applies without restart", func(t *testing.T) {
		e.route("narrative", Route{Provider: "p2", Model: "table-model-2"})
		if res := e.resolve("narrative"); res.Model != "table-model-2" {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("malformed environment value is ignored", func(t *testing.T) {
		e.envs["APPLYSMITH_MODEL_COMPACTION"] = "noslash"
		_, err := e.r.Resolve(bg(), "compaction")
		wantKind(t, err, KindNoRoute)
	})
	t.Run("unknown task", func(t *testing.T) {
		_, err := e.r.Resolve(bg(), "self-heal")
		wantKind(t, err, KindUnknownTask)
	})
}

func TestLLM_Resolve_ProviderRemovedFallsThrough(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	e.provider(ProviderConfig{ID: "p2", Kind: KindFake}, "")
	e.route("narrative", Route{Provider: "p1", Model: "m"})
	e.envs["APPLYSMITH_MODEL_NARRATIVE"] = "p2/env-model"
	e.setting(func(s *Settings) {
		s.Providers = slices.DeleteFunc(s.Providers, func(p ProviderConfig) bool { return p.ID == "p1" })
	})
	res := e.resolve("narrative")
	if res.Source != SourceEnv {
		t.Fatalf("res = %+v, want the next source in the order", res)
	}
	tab := e.show()
	if tab.Broken["narrative"] == nil {
		t.Fatal("the broken row must be reported")
	}
}

func TestLLM_Resolve_NoTaskBorrowsAnother(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	e.route("narrative", Route{Provider: "p1", Model: "m"})
	if _, err := e.r.Resolve(bg(), "cover-letter"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("cover-letter borrowed a row: %v", err)
	}
}
