package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/llm"
)

// watchCatalog is a stand-in for OpenRouter's public catalog. It counts the
// requests it gets and answers with the models in listed, or a status when
// status is not zero. No test in this file reaches the real service.
type watchCatalog struct {
	srv    *httptest.Server
	hits   atomic.Int64
	status atomic.Int64
	listed atomic.Value // []string
}

func newWatchCatalog(t *testing.T, listed ...string) *watchCatalog {
	t.Helper()
	c := &watchCatalog{}
	c.listed.Store(listed)
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.hits.Add(1)
		if st := int(c.status.Load()); st != 0 {
			http.Error(w, "down", st)
			return
		}
		var items []string
		for _, id := range c.listed.Load().([]string) {
			items = append(items, fmt.Sprintf(`{"id":%q,"pricing":{"prompt":"0.000001","completion":"0.00001"}}`, id))
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(items, ","))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func watchHarness(t *testing.T, c *watchCatalog) *harness {
	t.Helper()
	// Not offline: simulated routing answers every task with the fake model, so
	// the stored rows would never be read.
	return startDomainHarness(t, false, func(cfg *Config) { cfg.Domain.LLM = append(cfg.Domain.LLM, llm.WithCatalogURL(c.srv.URL)) })
}

const watchKey = "or-FAKE-key-0123456789abcdef"

func addOpenRouter(t *testing.T, h *harness, withKey bool) {
	t.Helper()
	r := h.domainOf().router
	err := r.SetProvider(context.Background(), llm.ProviderConfig{ID: "or", Kind: llm.KindOpenAI, Preset: "openrouter", BaseURL: "https://openrouter.ai/api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if withKey {
		if err := r.SetKey("or", watchKey); err != nil {
			t.Fatal(err)
		}
	}
}

func routeNarrative(t *testing.T, h *harness, model string) {
	t.Helper()
	err := h.domainOf().router.PatchRoutes(context.Background(), map[string]*llm.Route{"narrative": {Provider: "or", Model: model, Thinking: llm.ThinkOff}})
	if err != nil {
		t.Fatal(err)
	}
}

func timerStatus(t *testing.T, h *harness, name string) (active bool, reason string) {
	t.Helper()
	for _, tm := range h.get("/api/schedules").data()["timers"].([]any) {
		m := tm.(map[string]any)
		if m["name"] == name {
			active, _ = m["active"].(bool)
			reason, _ = m["reason"].(string)
			return active, reason
		}
	}
	t.Fatalf("no timer %s", name)
	return false, ""
}

func TestLLM_Watch_GoneModelAlertsOnceUntilFixed(t *testing.T) {
	c := newWatchCatalog(t, "openai/listed-model")
	h := watchHarness(t, c)
	addOpenRouter(t, h, true)
	routeNarrative(t, h, "retired/model")
	alerts := func() int { return countOutbox(t, h, `kind = 'model-gone'`) }

	tickOnce(t, h)
	waitFor(t, "the alert", func() bool { return alerts() == 1 })
	var body string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT title || ' | ' || body || ' | ' || link FROM notify_outbox WHERE kind = 'model-gone'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "narrative") || !strings.Contains(body, "retired/model") || !strings.HasSuffix(body, "/settings#settings-models") {
		t.Fatalf("the alert does not name the task and the model: %q", body)
	}

	// The same day and the next day, while the row is still broken: nothing more.
	tickOnce(t, h)
	tickOnce(t, h)
	if c.hits.Load() != 1 {
		t.Fatalf("%d catalog reads in one day", c.hits.Load())
	}
	h.clock.advance(25 * time.Hour)
	tickOnce(t, h)
	if c.hits.Load() != 2 || alerts() != 1 {
		t.Fatalf("a second day: %d reads, %d alerts", c.hits.Load(), alerts())
	}

	// Fixed, then broken again: a new alert.
	routeNarrative(t, h, "openai/listed-model")
	h.clock.advance(25 * time.Hour)
	tickOnce(t, h)
	routeNarrative(t, h, "retired/model")
	h.clock.advance(25 * time.Hour)
	tickOnce(t, h)
	waitFor(t, "the second alert", func() bool { return alerts() == 2 })

	// The watch never changes a row.
	set, err := llm.LoadSettings(context.Background(), h.srv.store.Load())
	if err != nil || set.Routes["narrative"].Model != "retired/model" {
		t.Fatalf("the row was changed: %+v %v", set.Routes["narrative"], err)
	}
}

func TestLLM_Watch_NoKeyDoesNothingAndSaysSo(t *testing.T) {
	c := newWatchCatalog(t)
	h := watchHarness(t, c)
	addOpenRouter(t, h, false)
	routeNarrative(t, h, "retired/model")
	tickOnce(t, h)
	if c.hits.Load() != 0 || countOutbox(t, h, `kind = 'model-gone'`) != 0 {
		t.Fatalf("without a key: %d catalog reads, %d alerts", c.hits.Load(), countOutbox(t, h, `kind = 'model-gone'`))
	}
	if active, reason := timerStatus(t, h, "model-watch"); active || reason != llm.ErrWatchNoKey.Error() {
		t.Fatalf("the timer list does not say why it is idle: active %v reason %q", active, reason)
	}
	// Within the hour the watch stays idle; after it, a stored key lets it run.
	if err := h.domainOf().router.SetKey("or", watchKey); err != nil {
		t.Fatal(err)
	}
	tickOnce(t, h)
	if c.hits.Load() != 0 {
		t.Fatal("the watch retried before the hour was over")
	}
	h.clock.advance(61 * time.Minute)
	tickOnce(t, h)
	waitFor(t, "the alert", func() bool { return countOutbox(t, h, `kind = 'model-gone'`) == 1 })
	if active, _ := timerStatus(t, h, "model-watch"); !active {
		t.Fatal("the timer stayed idle after the key was stored")
	}
}
