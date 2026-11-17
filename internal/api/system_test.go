package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

func (h *harness) fakeProvider(id string) {
	h.t.Helper()
	ctx := context.Background()
	r := h.domainOf().router
	if err := r.SetProvider(ctx, llm.ProviderConfig{ID: id, Kind: llm.KindFake}); err != nil {
		h.t.Fatal(err)
	}
	if err := r.PatchRoutes(ctx, map[string]*llm.Route{"feed-classify": {Provider: id, Model: "fake-model"}}); err != nil {
		h.t.Fatal(err)
	}
}

// awaitTest waits for the running provider test to end and returns its result.
func (h *harness) awaitTest() *wire.LLMTestResult {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if view := routing(h.t, h.get("/api/llm/routing")); view.LastTest != nil && view.Testing == "" {
			return view.LastTest
		}
		if time.Now().After(deadline) {
			h.t.Fatal("the test did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProviderTest_RunsInTheBackgroundAndTheViewReportsTheResult(t *testing.T) {
	h := newRoutingHarness(t)
	if r := h.post("/api/llm/providers/ghost/test", map[string]any{}); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Fatalf("an unconfigured provider: %d %s", r.StatusCode, r.body)
	}
	if view := routing(t, h.get("/api/llm/routing")); view.LastTest != nil || view.Testing != "" {
		t.Fatalf("before any test: %+v", view)
	}
	h.fakeProvider("probe")
	// An empty body tests the provider's usual model.
	r := h.do(call{method: "POST", path: "/api/llm/providers/probe/test", useToken: true, contentType: "application/json"})
	if r.StatusCode != 202 || r.data()["provider"] != "probe" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if lt := h.awaitTest(); lt.Provider != "probe" || lt.Outcome != "ok" || lt.Model != "fake-model" || lt.TestedAt.IsZero() {
		t.Fatalf("%+v", lt)
	}
}

func TestProviderTest_OneAtATime(t *testing.T) {
	h := newRoutingHarness(t)
	h.fakeProvider("probe")
	if !h.srv.models.Test.Begin("other") {
		t.Fatal("nothing runs yet")
	}
	defer h.srv.models.Test.End()
	if r := h.post("/api/llm/providers/probe/test", map[string]any{}); r.StatusCode != 409 || r.errReason() != "busy" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if view := routing(t, h.get("/api/llm/routing")); view.Testing != "other" {
		t.Fatalf("the view names the running test: %q", view.Testing)
	}
}

func (h *harness) appendLog(lines ...string) {
	h.t.Helper()
	f, err := os.OpenFile(filepath.Join(h.ws.StateDir(), "logs", logFileName), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			h.t.Fatal(err)
		}
	}
}

func TestSystemLogs_ReturnsTheLastTwoHundredLinesRedacted(t *testing.T) {
	h := newRoutingHarness(t)
	if k := h.send("PUT", "/api/llm/keys/openrouter", map[string]any{"key": testSecret}); k.StatusCode != 200 {
		t.Fatalf("%d %s", k.StatusCode, k.body)
	}
	var lines []string
	for i := range 250 {
		lines = append(lines, fmt.Sprintf("level=INFO msg=probe n=%d", i))
	}
	lines = append(lines, "level=WARN msg=leak value="+testSecret)
	h.appendLog(lines...)
	got := h.get("/api/system/logs").data()["lines"].([]any)
	if len(got) != 200 {
		t.Fatalf("%d lines", len(got))
	}
	last := got[len(got)-1].(string)
	if strings.Contains(last, testSecret) || !strings.Contains(last, "[redacted]") {
		t.Fatalf("the last line: %q", last)
	}
	if first := got[0].(string); !strings.Contains(first, "n=51") {
		t.Fatalf("the window starts at the 200th from the end: %q", first)
	}
}

func TestSystemDiagnostics_IsADownloadWithNoSecret(t *testing.T) {
	h := startDomainHarness(t, false, func(c *Config) { c.Setup = &fakeSetup{} })
	if k := h.send("PUT", "/api/llm/keys/openrouter", map[string]any{"key": testSecret}); k.StatusCode != 200 {
		t.Fatalf("%d %s", k.StatusCode, k.body)
	}
	h.appendLog("level=WARN msg=leak value=" + testSecret)
	r := h.get("/api/system/diagnostics")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("%d %v", r.StatusCode, r.Header)
	}
	if strings.Contains(string(r.body), testSecret) {
		t.Fatal("the export holds a secret")
	}
	var doc struct {
		Version    string
		Doctor     []any
		Log        []string
		Components []any
		Platform   string
	}
	if err := json.Unmarshal(r.body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Platform == "" || len(doc.Doctor) != 1 || len(doc.Components) == 0 || len(doc.Log) == 0 {
		t.Fatalf("%s", r.body)
	}
}
