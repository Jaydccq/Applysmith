package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

func script(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in executable is a shell script")
	}
	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecLauncher_StdoutSurvivesTheProcessExitAndStdinEndsIt(t *testing.T) {
	path := script(t, `echo '{"type":"agent_start"}'; echo '{"type":"agent_settled"}'; cat >/dev/null; echo '{"type":"turn_end"}'`)
	var stderr strings.Builder
	p, err := execLauncher{}.Start(Spec{Path: path, Env: []string{"PATH=/usr/bin:/bin"}, Dir: t.TempDir()}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(p.Stdout())
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	if got := <-lines; got != `{"type":"agent_start"}` {
		t.Fatalf("first line = %q", got)
	}
	if got := <-lines; got != `{"type":"agent_settled"}` {
		t.Fatalf("second line = %q", got)
	}
	p.CloseInput()
	if got := <-lines; got != `{"type":"turn_end"}` {
		t.Fatalf("line written after stdin closed = %q: output must survive the exit", got)
	}
	if _, open := <-lines; open {
		t.Fatal("stdout did not end with the process")
	}
	<-p.Done()
}

func TestExecLauncher_KillEndsAProcessThatIgnoresItsInput(t *testing.T) {
	path := script(t, `echo ready; exec sleep 60`)
	p, err := execLauncher{}.Start(Spec{Path: path, Env: []string{"PATH=/usr/bin:/bin"}, Dir: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(p.Stdout())
	if !sc.Scan() || sc.Text() != "ready" {
		t.Fatalf("no ready line: %v", sc.Err())
	}
	p.Kill()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not end after Kill")
	}
}

func TestChildEnvironment_OnlyWhatPiNeedsAndTheBridgeIsNotProxied(t *testing.T) {
	env := noProxyFor(baseEnv([]string{"PATH=/bin", "HOME=/home/alex", "APPLYSMITH_SECRET_API_TOKEN=x", "OPENAI_API_KEY=y", "NO_PROXY=internal.example.test", "HTTPS_PROXY=http://proxy.example.test:3128"}))
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/bin", "HOME=/home/alex", "HTTPS_PROXY=http://proxy.example.test:3128", "NO_PROXY=127.0.0.1,localhost,[::1],internal.example.test"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lacks %q:\n%s", want, joined)
		}
	}
	for _, bad := range []string{"APPLYSMITH_SECRET_API_TOKEN", "OPENAI_API_KEY"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%s reached the child", bad)
		}
	}
	if strings.Count(joined, "NO_PROXY=") != 1 {
		t.Errorf("NO_PROXY appears twice:\n%s", joined)
	}
}

func TestModelsJSON_DeclaresTheGatewayWithTheProcessCredentialAndNoKey(t *testing.T) {
	route := llm.AgentRoute{
		Resolved: llm.Resolved{Provider: "ds", Model: "deepseek-flash", Thinking: llm.ThinkMedium, MaxOutputTokens: 4000,
			Fallbacks: []llm.Fallback{{Provider: "or", Model: "openai/gpt-6-luna"}}},
		Config: llm.ProviderConfig{ID: "ds", Kind: llm.KindOpenAI, BaseURL: "https://api.deepseek.example.test", Preset: "deepseek"},
	}
	b, err := modelsJSON([]llm.AgentRoute{route}, "http://127.0.0.1:4100/")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL    string           `json:"baseUrl"`
			API        string           `json:"api"`
			APIKey     string           `json:"apiKey"`
			AuthHeader bool             `json:"authHeader"`
			Models     []map[string]any `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	p, ok := doc.Providers["applysmith"]
	if !ok || len(doc.Providers) != 1 || p.BaseURL != "http://127.0.0.1:4100/internal/agent/llm" || p.API != "openai-completions" ||
		p.APIKey != "$APPLYSMITH_AGENT_TOKEN" || !p.AuthHeader || len(p.Models) != 1 {
		t.Fatalf("models.json = %s", b)
	}
	m := p.Models[0]
	// DeepSeek asks for an assistant's reasoning back; behind the gateway's
	// address pi would not know to send it.
	if m["id"] != "deepseek-flash" || fmt.Sprint(m["compat"]) != "map[requiresReasoningContentOnAssistantMessages:true]" || fmt.Sprint(m["cost"]) != "map[cacheRead:0 cacheWrite:0 input:0 output:0]" {
		t.Fatalf("model = %v", m)
	}
	route.Config = llm.ProviderConfig{ID: "anthropic", Kind: llm.KindAnthropic}
	route.Model = "claude-sonnet-5-5"
	b, _ = modelsJSON([]llm.AgentRoute{route}, "http://127.0.0.1:4100")
	if !strings.Contains(string(b), `"api": "anthropic-messages"`) || strings.Contains(string(b), "compat") || strings.Contains(string(b), "api.anthropic.com") {
		t.Errorf("anthropic entry:\n%s", b)
	}
}

func TestAuditSalt_IsCreatedOnceWithOwnerOnlyPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	a, err := auditSalt(dir)
	if err != nil || len(a) < 16 {
		t.Fatalf("salt = %q, %v", a, err)
	}
	b, _ := auditSalt(dir)
	if a != b {
		t.Fatal("the salt changed between calls")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "audit-salt"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("salt file = %v, %v", info, err)
		}
	}
}

// A probe's turns run as the product's processes: its supervisor declares its
// row in the product's one models.json, at the product's origin, takes its
// slot from the product's three, and never the one kept for a person.
func TestSupervisor_Beside_SharesTheModelsFileTheOriginAndTheSlots(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.sup.cfg.AgentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.sup.writeModels(f.router.route); err != nil {
		t.Fatal(err)
	}
	guest := NewBridge(BridgeOptions{Registry: f.registry, Guard: f.guard, Sessions: f.sessions, Packer: ContextPacker{Sessions: f.sessions}, Gateway: f.router})
	probe, end := f.sup.Beside(guest, f.sessions)
	defer end()
	if err := probe.writeModels(*lightRoute()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"applysmith": {`) || !strings.Contains(got, `"applysmith-light": {`) {
		t.Errorf("models.json after a probe's start lost a row:\n%s", got)
	}
	if n := strings.Count(got, `"baseUrl"`); n == 0 || n != strings.Count(got, f.sup.cfg.BaseURL()+gatewayPath) {
		t.Errorf("models.json names an origin other than the product's:\n%s", got)
	}

	for range MaxProcesses - 1 {
		if err := f.sup.acquire(context.Background(), nil, true); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := probe.acquire(ctx, nil, probe.holdsBackground(&Turn{Channel: ChannelDashboard})); err == nil {
		t.Error("a probe started a process beside two background turns: the slot kept for a person was taken")
	}
}

// A probe's process calls the product's routes with its own credential, and is
// answered by the probe's bridge for as long as the probe runs.
func TestBridge_AGuestsProcessIsAnsweredOnTheHostsRoutesUntilItEnds(t *testing.T) {
	f := newFixture(t)
	only, err := NewRegistry(Tool{Name: "probe_only", Label: "Probe only", Description: "Reads nothing.", Class: ClassRead, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`, Run: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Text: "from the guest"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	guest := NewBridge(BridgeOptions{Registry: only, Guard: NewGuard(only, GuardOptions{Sessions: f.sessions, Salt: "salt"}), Sessions: f.sessions, Packer: ContextPacker{Sessions: f.sessions}})
	_, end := f.sup.Beside(guest, f.sessions)
	sess, err := f.sessions.Create(context.Background(), sessions.Dashboard, "")
	if err != nil {
		t.Fatal(err)
	}
	turn := &Turn{ID: store.NewID("turn"), SessionID: sess.ID, Channel: ChannelDashboard}
	token, r, err := guest.open(context.Background(), turn, func(wire.ChatEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	defer guest.close(token, r)
	if !f.bridge.Authenticate(token) {
		t.Fatal("the host's routes refuse the guest's credential")
	}
	cat, err := f.bridge.Catalog(token)
	if err != nil || len(cat.Tools) != 1 || cat.Tools[0].Name != "probe_only" {
		t.Fatalf("catalog on the host's routes = %+v, %v; want the guest's one tool", cat.Tools, err)
	}
	res, err := f.bridge.ToolCall(context.Background(), token, wire.AgentToolCall{ToolCallID: "c1", Name: "probe_only", Args: json.RawMessage(`{}`)})
	if err != nil || res.IsError || !strings.Contains(res.Text, "from the guest") {
		t.Fatalf("tool call on the host's routes = %+v, %v", res, err)
	}
	end()
	if f.bridge.Authenticate(token) {
		t.Error("the host's routes still accept the guest's credential after the probe ended")
	}
	if _, err := f.bridge.Catalog(token); !errors.Is(err, ErrBridgeAuth) {
		t.Errorf("catalog after the probe ended = %v, want ErrBridgeAuth", err)
	}
}

// Turns that start together each write pi's files: none of the writes fails
// for another's temporary file, and a process reading the extension beside a
// write reads all of it.
func TestSupervisor_StartsTogetherReplaceSettingsAndTheExtensionWhole(t *testing.T) {
	dir := t.TempDir()
	stop := make(chan struct{})
	torn := make(chan int, 1)
	go func() {
		defer close(torn)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(filepath.Join(dir, "extension", "applysmith.mjs")); err == nil && len(b) != len(extensionSource) {
				torn <- len(b)
				return
			}
		}
	}()
	var wg sync.WaitGroup
	errs := make(chan error, 2*MaxProcesses*200)
	for range MaxProcesses {
		wg.Go(func() {
			for range 200 {
				if err := writeSettings(dir); err != nil {
					errs <- err
				}
				if _, err := writeExtension(dir); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	close(errs)
	for err := range errs {
		t.Fatalf("a start beside another failed to write its files: %v", err)
	}
	if n, ok := <-torn; ok {
		t.Errorf("a reader saw %d bytes of the extension, want all %d or the file before it", n, len(extensionSource))
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	more, _ := filepath.Glob(filepath.Join(dir, "extension", "*.tmp"))
	if len(left)+len(more) > 0 {
		t.Errorf("temporary files were left behind: %v %v", left, more)
	}
}
