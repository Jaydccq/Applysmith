package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestLLM_Preset_OneKeyRoutesEveryTask(t *testing.T) {
	e := newEnv(t)
	if err := e.r.SetKey("deepseek", fakeKey("k2")); err != nil {
		t.Fatal(err)
	}
	if err := e.r.ConfigurePreset(bg(), "deepseek", ConfigureOptions{}); err != nil {
		t.Fatal(err)
	}
	tab := e.show()
	for _, row := range tab.Rows {
		if row.Provider != "deepseek" || row.Source != SourcePreset {
			t.Errorf("%s routes to %s via %s", row.Task.ID, row.Provider, row.Source)
		}
	}
	if len(tab.Unrouted) != 1 || tab.Unrouted["embedding"] == "" {
		t.Fatalf("only embedding may be unrouted: %v", tab.Unrouted)
	}
}

func TestLLM_Preset_WithoutItsKey(t *testing.T) {
	e := newEnv(t)
	err := e.r.ConfigurePreset(bg(), "anthropic", ConfigureOptions{})
	var se *SetupError
	if !errors.As(err, &se) || !strings.Contains(se.Message, "console.anthropic.com") || !strings.Contains(se.Message, "applysmith llm keys set anthropic") {
		t.Fatalf("err = %v", err)
	}
	if s := e.load(); s.Preset != "" {
		t.Fatal("the preset became active without a key")
	}
}

func TestLLM_Preset_FallbackOption(t *testing.T) {
	e := newEnv(t)
	_ = e.r.SetKey("openrouter", fakeKey("k3"))
	_ = e.r.SetKey("deepseek", fakeKey("k4"))
	if err := e.r.ConfigurePreset(bg(), "openrouter", ConfigureOptions{Fallback: "deepseek"}); err != nil {
		t.Fatal(err)
	}
	res := e.resolve("quick-screen")
	if len(res.Fallbacks) != 1 || res.Fallbacks[0] != (Fallback{"deepseek", "deepseek-flash"}) {
		t.Fatalf("fallbacks = %+v", res.Fallbacks)
	}
	// A task the fallback cannot serve has no fallback; the primary stays.
	emb := e.resolve("embedding")
	if len(emb.Fallbacks) != 0 || emb.Provider != "openrouter" {
		t.Fatalf("embedding = %+v", emb)
	}
}

func TestLLM_Presets_NameNoRetiredModel(t *testing.T) {
	for _, p := range presets {
		for class, m := range p.Models {
			if IsRetired(m) {
				t.Errorf("preset %s routes %s to retired model %s", p.Name, class, m)
			}
		}
		if p.Embedding != "" && IsRetired(p.Embedding) {
			t.Errorf("preset %s embedding %s is retired", p.Name, p.Embedding)
		}
	}
	if !IsRetired("vendor/gpt-4-32k") {
		t.Error("a vendor-prefixed retired id must match")
	}
}

func TestLLM_Presets_NoFreeTierDefaults(t *testing.T) {
	for _, p := range presets {
		for _, m := range p.Models {
			if isFreeModel(m) {
				t.Errorf("preset %s defaults to free-tier model %s", p.Name, m)
			}
		}
	}
}

func TestLLM_Presets_CoverEveryClass(t *testing.T) {
	for _, p := range presets {
		for _, c := range []Class{ClassFast, ClassStandard, ClassDeep} {
			if p.Models[c] == "" {
				t.Errorf("preset %s has no %s model", p.Name, c)
			}
		}
	}
}

func TestLLM_Presets_OneTableFeedsEverySurface(t *testing.T) {
	task, _ := LookupTask("quick-screen")
	p, _ := PresetByName("openai")
	if p.ModelFor(task) != "gpt-6-luna" {
		t.Fatalf("model = %s", p.ModelFor(task))
	}
	e := newEnv(t)
	_ = e.r.SetKey("openai", fakeKey("k5"))
	_ = e.r.ConfigurePreset(bg(), "openai", ConfigureOptions{})
	if res := e.resolve("quick-screen"); res.Model != p.ModelFor(task) {
		t.Fatalf("resolved %s, table %s", res.Model, p.ModelFor(task))
	}
}

func TestLLM_Preset_ExplicitRowsUntouched(t *testing.T) {
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "p1", Kind: KindFake}, "")
	e.route("narrative", Route{Provider: "p1", Model: "mine"})
	_ = e.r.SetKey("deepseek", fakeKey("k6"))
	if err := e.r.ConfigurePreset(bg(), "deepseek", ConfigureOptions{}); err != nil {
		t.Fatal(err)
	}
	if res := e.resolve("narrative"); res.Source != SourceTable || res.Model != "mine" {
		t.Fatalf("res = %+v", res)
	}
}
